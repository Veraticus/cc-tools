package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code broadcasts a background job's agent_needs_input /
// agent_completed Notification event to every live session with agent view
// open, and each of those sessions runs this hook independently. Left
// alone, one job event therefore produces N pushes (one per receiving
// session) on top of the job session's own Stop-hook send. This file
// computes the cross-session facts that let Decide collapse that fan-out:
// an atomic first-claimant ledger shared by all hook processes, and a
// resolution of the broadcast back to its source job so the claim winner
// can defer to the job's own (richer, judge-composed) notification.

const (
	// broadcastClaimWindow is how long a claim suppresses identical
	// agent_needs_input broadcasts from other sessions. Duplicates of one
	// event land within ~seconds of each other; a genuinely new event with
	// identical text (the same job asking the same generic question again)
	// is minutes away at the earliest.
	broadcastClaimWindow = 2 * time.Minute

	// broadcastClaimWindowCompleted is broadcastClaimWindow's agent_completed
	// counterpart: a finished background job's harness-emitted "job
	// finished" broadcast has been observed re-firing for tens of minutes
	// after the job actually stopped, so its claim window is far wider than
	// agent_needs_input's.
	broadcastClaimWindowCompleted = 30 * time.Minute

	// broadcastCoveredWindow is how recently the source job's own session
	// must have sent for the broadcast to be considered covered. The job's
	// Stop-hook send has been observed landing 0.3–1.2s before the
	// broadcast; anything close to this window's edge is a different,
	// earlier notification.
	broadcastCoveredWindow = 60 * time.Second

	// broadcastClaimTTL is when an old claim file becomes garbage to
	// opportunistically sweep. It must stay >= the largest claim window
	// (broadcastClaimWindowCompleted) — sweeping a claim before its own
	// window elapses would let a still-live agent_completed claim get swept
	// and re-claimed, defeating the dedupe it exists to provide.
	broadcastClaimTTL = broadcastClaimWindowCompleted

	// maxJobStateBytes bounds the read of a job's state.json — the fields
	// wanted here are near the front of a small file; anything bigger is
	// not a job state file.
	maxJobStateBytes = 256 * 1024
)

// claimWindowFor returns how long a claim suppresses identical broadcasts
// for notificationType: see broadcastClaimWindow and
// broadcastClaimWindowCompleted.
func claimWindowFor(notificationType string) time.Duration {
	if notificationType == notifTypeAgentCompleted {
		return broadcastClaimWindowCompleted
	}
	return broadcastClaimWindow
}

// broadcastClaimsDirName is the ledger directory inside StateBase, a
// sibling of the per-session state directories.
const broadcastClaimsDirName = "broadcast-claims"

// The two broadcast notification types (see the package comment above):
// harness-emitted, one per live session with agent view open.
const (
	notifTypeAgentNeedsInput = "agent_needs_input"
	notifTypeAgentCompleted  = "agent_completed"
)

// BroadcastFacts carries the pipeline's I/O-derived context for one
// broadcast Notification event, consumed by Decide's agent_needs_input /
// agent_completed gates. A nil *BroadcastFacts on Env means the event is
// not a broadcast type.
type BroadcastFacts struct {
	// Duplicate means another hook process already claimed this exact
	// broadcast inside broadcastClaimWindow — someone else owns delivery.
	Duplicate bool
	// Covered means the broadcast resolved to a local source job whose own
	// session sent a notification within broadcastCoveredWindow — the
	// richer Stop-hook ping already went out.
	Covered bool
	// CoveredAgo is how long ago that send was (set only with Covered).
	CoveredAgo time.Duration
	// JobProject is the project name derived from the source job's own
	// cwd, replacing the receiving session's project in the notification
	// title. Empty when the source job could not be resolved.
	JobProject string
}

// broadcastJob is the slice of a job's state.json this file needs: the
// job's display name (the broadcast message's prefix), the session it runs
// as, and where.
type broadcastJob struct {
	Name      string `json:"name"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

// broadcastFacts computes BroadcastFacts for in, or nil when in is not a
// broadcast-type Notification event. Claiming writes to the shared ledger;
// a dry run only observes it, so rehearsals never suppress a real
// session's send.
func (p Pipeline) broadcastFacts(ctx context.Context, in HookInput, now time.Time) *BroadcastFacts {
	if in.HookEventName != eventNotification || in.AgentID != "" {
		return nil
	}
	if in.NotificationType != notifTypeAgentNeedsInput && in.NotificationType != notifTypeAgentCompleted {
		return nil
	}

	facts := &BroadcastFacts{}
	key := in.NotificationType + "\n" + in.Message
	facts.Duplicate = !p.dedupeState().ClaimBroadcast(ctx, key, claimWindowFor(in.NotificationType), now, p.DryRun)

	job, ok := resolveBroadcastSource(jobsDir(p.Environ), in.Message)
	if !ok {
		return facts
	}
	facts.JobProject = filepath.Base(job.CWD)
	since := p.dedupeState().SinceLastNotify(ctx, job.SessionID, now)
	if since >= 0 && since < broadcastCoveredWindow {
		facts.Covered = true
		facts.CoveredAgo = since
	}
	return facts
}

// claimBroadcast atomically claims the broadcast identified by key and
// reports whether this process won. The claim is a content-hash-named file
// created with O_EXCL, so exactly one of N concurrent hook processes
// succeeds; a claim older than window is stale (a previous, distinct event)
// and is removed and re-claimed. dryRun observes without writing and wins
// whenever no live claim exists.
func claimBroadcast(stateBase, key string, window time.Duration, now time.Time, dryRun bool) bool {
	sum := sha256.Sum256([]byte(key))
	dir := filepath.Join(stateBase, broadcastClaimsDirName)
	path := filepath.Join(dir, hex.EncodeToString(sum[:12]))

	if dryRun {
		fi, err := os.Stat(path)
		return err != nil || now.Sub(fi.ModTime()) >= window
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		// Ledger unavailable: fail toward sending — a duplicate ping beats
		// a lost one.
		return true
	}
	sweepBroadcastClaims(dir, now)

	for range 2 {
		//nolint:gosec // path is a hash under our own state dir
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return true
		}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			// The claim vanished between OpenFile and Stat (swept or
			// removed by a peer); retry the create.
			continue
		}
		if now.Sub(fi.ModTime()) < window {
			return false
		}
		// Stale claim from an earlier event: remove and retry. If a peer
		// races us to the re-create, the second loop iteration sees the
		// fresh claim and yields.
		_ = os.Remove(path)
	}
	return false
}

// sweepBroadcastClaims best-effort removes claim files older than
// broadcastClaimTTL so the ledger directory stays a handful of entries.
func sweepBroadcastClaims(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		if now.Sub(info.ModTime()) > broadcastClaimTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// resolveBroadcastSource maps a broadcast message back to the local job it
// is about. The harness composes these messages as "<job name> needs your
// input: …" / "<job name> finished", so the job whose name prefixes the
// message (longest match wins, guarding against prefix-nested names) is
// the source. Returns false when no local job matches — a remote job, a
// renamed message format, or a jobs dir this host doesn't have.
func resolveBroadcastSource(dir, message string) (broadcastJob, bool) {
	if message == "" {
		return broadcastJob{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return broadcastJob{}, false
	}

	var best broadcastJob
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		//nolint:gosec // fixed filename under the harness's own jobs dir
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name(), "state.json"))
		if readErr != nil || len(data) > maxJobStateBytes {
			continue
		}
		var job broadcastJob
		if json.Unmarshal(data, &job) != nil {
			continue
		}
		if job.Name == "" || !strings.HasPrefix(message, job.Name+" ") {
			continue
		}
		if !validSessionID(job.SessionID) {
			continue
		}
		if len(job.Name) > len(best.Name) {
			best = job
		}
	}
	return best, best.Name != ""
}

// jobsDir resolves the background-jobs directory for the Claude Code
// context this hook runs in: <CLAUDE_CONFIG_DIR>/jobs, defaulting the
// config dir to ~/.claude. Broadcasts are scoped to a config-dir context,
// so the receiving hook's own context is where the source job lives.
func jobsDir(environ []string) string {
	configDir := parseEnviron(environ)["CLAUDE_CONFIG_DIR"]
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		configDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(configDir, "jobs")
}
