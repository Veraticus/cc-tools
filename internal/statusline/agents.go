package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// This file is the handoff between Claude Code's two statusline hooks.
// Claude tells the subagentStatusLine hook which models the session's
// agents run, but never tells the main statusLine hook anything about
// agents — its payload is session-scoped on every screen, including
// the agent view. So the subagent hook (which cc-tools also renders)
// writes a tiny per-session state file on every tick, and the main
// statusline reads it back: when agents are running, the model chip
// swaps from the session model to the running agents' models.

const (
	// agentsStateTTL bounds how stale a state file may be before the
	// main statusline ignores it. The subagent hook rewrites the file
	// every tick (~1-7s observed) while any task row exists; once a
	// session stops ticking (exit, crash) the swap must not outlive
	// it by more than this.
	agentsStateTTL = 30 * time.Second

	// agentsStatePrefix namespaces the per-session state files inside
	// the cache dir (default /dev/shm, shared with the git-status and
	// cost caches).
	agentsStatePrefix = "cc-tools-agents-"

	// agentsMaxGroups caps how many distinct model groups the summary
	// renders before collapsing the rest into "+N".
	agentsMaxGroups = 3

	// agentsLabelMaxRunes caps one group label read back from the
	// state file. Labels are written by our own subagent hook and are
	// already short; the cap is defense against a corrupted or
	// hand-edited file distorting the chip.
	agentsLabelMaxRunes = 24
)

// validAgentsSessionID matches the session ids Claude Code generates
// (UUID-shaped). Anything else — path separators, dots, control bytes
// — is refused outright so the id can never traverse out of the cache
// dir when joined into a filename.
var validAgentsSessionID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// agentsState is the on-disk format. Updated is a Unix timestamp
// stamped by the writer (mtime would also work, but an explicit field
// survives file copies and is trivially testable). Running holds one
// label per running task — duplicates intact, so the reader can count
// them ("2×O5"). An empty label means "this task runs the session's
// own model" (Claude omits model for inherited-model tasks); the
// reader substitutes the session model display for those.
type agentsState struct {
	Updated int64    `json:"updated"`
	Running []string `json:"running"`
}

// agentsStatePath joins the cache dir and session id into the state
// file path, refusing empty dirs and malformed ids.
func agentsStatePath(cacheDir, sessionID string) (string, bool) {
	if cacheDir == "" || !validAgentsSessionID.MatchString(sessionID) {
		return "", false
	}
	return filepath.Join(cacheDir, agentsStatePrefix+sessionID+".json"), true
}

// WriteAgentsState persists the running-agent labels for one session.
// Called by the subagent hook on every invocation — including with an
// empty labels slice, which is how the swap clears the moment every
// agent completes. Writes are atomic (tmp + rename) so a concurrent
// main-statusline read never sees a torn file.
func WriteAgentsState(cacheDir, sessionID string, labels []string, now time.Time) error {
	path, ok := agentsStatePath(cacheDir, sessionID)
	if !ok {
		return fmt.Errorf("agents state: unusable cache dir %q or session id %q", cacheDir, sessionID)
	}
	if labels == nil {
		labels = []string{}
	}
	raw, err := json.Marshal(agentsState{Updated: now.Unix(), Running: labels})
	if err != nil {
		return fmt.Errorf("agents state: marshal: %w", err)
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if writeErr := os.WriteFile(tmp, raw, 0o600); writeErr != nil {
		return fmt.Errorf("agents state: write: %w", writeErr)
	}
	if renameErr := os.Rename(tmp, path); renameErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("agents state: rename: %w", renameErr)
	}
	return nil
}

// ReadAgentsDisplay returns the running-agents summary for the model
// chip ("2×O5 · sol ×XH"), or "" when the chip should show the
// session model as usual: no state file, a stale one, garbage
// content, or simply no running agents. sessionModel fills in for
// tasks that inherit the session's model.
//
// Grouping preserves first-seen order; equal labels collapse into one
// group with a "N×" prefix when N > 1. At most agentsMaxGroups groups
// render, with the remainder folded into "+N".
func ReadAgentsDisplay(fr FileReader, cacheDir, sessionID, sessionModel string, now time.Time) string {
	path, ok := agentsStatePath(cacheDir, sessionID)
	if !ok {
		return ""
	}
	raw, err := fr.ReadFile(path)
	if err != nil {
		return ""
	}
	var st agentsState
	if unmarshalErr := json.Unmarshal(raw, &st); unmarshalErr != nil {
		return ""
	}
	age := now.Unix() - st.Updated
	if age > int64(agentsStateTTL.Seconds()) || age < -int64(agentsStateTTL.Seconds()) {
		return ""
	}
	if len(st.Running) == 0 {
		return ""
	}
	return formatAgentGroups(groupAgentLabels(st.Running, sessionModel))
}

// agentGroup is one distinct label with its occurrence count, in
// first-seen order.
type agentGroup struct {
	label string
	count int
}

// groupAgentLabels sanitizes and de-duplicates raw labels, counting
// repeats. Empty labels (inherited-model tasks) take sessionModel;
// labels that sanitize to nothing are dropped.
func groupAgentLabels(running []string, sessionModel string) []agentGroup {
	groups := make([]agentGroup, 0, len(running))
	index := make(map[string]int, len(running))
	for _, rawLabel := range running {
		label := sanitizeAgentLabel(rawLabel)
		if label == "" {
			label = sessionModel
		}
		if label == "" {
			continue
		}
		if i, seen := index[label]; seen {
			groups[i].count++
			continue
		}
		index[label] = len(groups)
		groups = append(groups, agentGroup{label: label, count: 1})
	}
	return groups
}

// formatAgentGroups renders groups as "2×O5 · sol ×XH", capping at
// agentsMaxGroups with the remaining task count folded into "+N".
func formatAgentGroups(groups []agentGroup) string {
	if len(groups) == 0 {
		return ""
	}
	shown := groups
	overflow := 0
	if len(groups) > agentsMaxGroups {
		shown = groups[:agentsMaxGroups]
		for _, g := range groups[agentsMaxGroups:] {
			overflow += g.count
		}
	}
	parts := make([]string, 0, len(shown)+1)
	for _, g := range shown {
		if g.count > 1 {
			parts = append(parts, fmt.Sprintf("%d×%s", g.count, g.label))
		} else {
			parts = append(parts, g.label)
		}
	}
	if overflow > 0 {
		parts = append(parts, fmt.Sprintf("+%d", overflow))
	}
	return strings.Join(parts, " · ")
}

// sanitizeAgentLabel neutralizes a label read from disk: control
// bytes stripped (ANSI-injection defense — the chip body lands raw in
// the terminal), whitespace trimmed, length capped.
func sanitizeAgentLabel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	clean := strings.TrimSpace(b.String())
	runes := []rune(clean)
	if len(runes) > agentsLabelMaxRunes {
		clean = string(runes[:agentsLabelMaxRunes-1]) + "…"
	}
	return clean
}

// ResolveCacheDir is the canonical cache-dir resolution for every
// cc-tools binary: explicit env overrides first, then /dev/shm so the
// hot-path caches (git status, costs, agents state) stay off disk.
// Shared here because the statusline and subagent-statusline commands
// must agree on the directory for the agents-state handoff to work.
func ResolveCacheDir() string {
	if dir := os.Getenv("CC_TOOLS_STATUSLINE_CACHE_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("CLAUDE_STATUSLINE_CACHE_DIR"); dir != "" {
		return dir
	}
	return "/dev/shm"
}
