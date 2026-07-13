package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const codexJudgeRubric = `You are judging whether a Codex turn should notify the user away from their terminal, and writing the notification when it should.

Decision policy:
- Set notify=true and urgency="blocked" when the user can act now and progress needs their answer, decision, approval, recovery action, or attention to a material blocker or failure. If there is uncertainty involving possible intervention, choose blocked.
- Set notify=true and urgency="done" for a genuinely useful final deliverable or material completed outcome.
- Set notify=false for routine worker, reviewer, or scout reports, acknowledgements, internal progress, or turns waiting on other work.

Output contract:
- Respond with a single raw JSON object and nothing else. Do not wrap it in fences or add commentary.
- Use exactly these keys: notify (bool), urgency (string), task (string), body (string), reason (string).
- task names the work in at most 6 words and at most 60 UTF-8 bytes.
- body is one sentence, at most 180 characters, summarizing the outcome in your own words.
- For urgency="blocked", body must state the exact answer, decision, approval, recovery action, or other concrete input the user must provide or take.
- reason briefly explains how the final assistant message supports the summary.
- task and body must be plain text with no Markdown, JSON, headings, bullets, backticks, emphasis, links, or fences.

Source authority:
- Use the complete digest as input, without ignoring or shortening either section.
- The FINAL ASSISTANT MESSAGE alone establishes actual delivery or completion.
- USER INPUT is context for task naming only, not proof that requested work was delivered.
- Summarize in your own words. Do not use tools.`

// CodexJudge invokes a Codex model to decide whether a completed turn merits
// a notification and summarize notified outcomes. It never retries another model.
type CodexJudge struct {
	Bin     string
	Model   string
	Timeout time.Duration
}

type codexJudgeResult struct {
	verdict JudgeVerdict
	err     error
}

// ResolveCodexJudgeModel returns a non-empty Codex judge model override or
// fallback when the override is absent or empty.
func ResolveCodexJudgeModel(environ []string, fallback string) string {
	if model := parseEnviron(environ)["CC_TOOLS_CODEX_JUDGE_MODEL"]; model != "" {
		return model
	}
	return fallback
}

// Evaluate sends digest to Codex on stdin and returns its validated verdict.
// Every failure returns a zero verdict and a wrapped, bounded error.
func (j CodexJudge) Evaluate(ctx context.Context, digest string) (JudgeVerdict, error) {
	result := j.evaluateWithWorkdir(ctx, digest)
	return result.verdict, result.err
}

func (j CodexJudge) evaluateWithWorkdir(ctx context.Context, digest string) *codexJudgeResult {
	//nolint:forbidigo // os.MkdirTemp is the secure standard-library primitive required for this isolated workdir
	workdir, err := os.MkdirTemp("", "cc-tools-codex-judge-")
	if err != nil {
		return &codexJudgeResult{err: fmt.Errorf("codex exec: creating temporary workdir: %w", err)}
	}
	result := &codexJudgeResult{}
	defer func() {
		if cleanupErr := os.RemoveAll(workdir); cleanupErr != nil {
			result.verdict = JudgeVerdict{}
			result.err = fmt.Errorf(
				"codex exec: removing temporary workdir: %w",
				errors.Join(result.err, cleanupErr),
			)
		}
	}()

	result.verdict, result.err = j.evaluateInWorkdir(ctx, digest, workdir)
	return result
}

func (j CodexJudge) evaluateInWorkdir(ctx context.Context, digest, workdir string) (JudgeVerdict, error) {
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"exec",
		"--ignore-user-config",
		"--ignore-rules",
		"--ephemeral",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--color", "never",
		"-C", workdir,
		"-m", j.Model,
		"-c", `model_reasoning_effort="low"`,
		"-",
	}
	//nolint:gosec // j.Bin is trusted app configuration ("codex" in production, a test stub path), not turn input
	cmd := exec.CommandContext(runCtx, j.Bin, args...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(buildCodexJudgePrompt(digest))
	cmd.Env = append(os.Environ(), "CC_TOOLS_NTFY_DISABLED=true")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return JudgeVerdict{}, fmt.Errorf(
				"codex exec: %w (stdout: %s, stderr: %s)",
				errors.Join(ctxErr, runErr), snippet(stdout.String()), snippet(stderr.String()),
			)
		}
		return JudgeVerdict{}, fmt.Errorf(
			"codex exec: exited with error: %w (stdout: %s, stderr: %s)",
			runErr, snippet(stdout.String()), snippet(stderr.String()),
		)
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return JudgeVerdict{}, errors.New("codex exec: returned empty stdout")
	}
	parsed, err := parseCodexVerdict(out)
	if err != nil {
		return JudgeVerdict{}, fmt.Errorf("codex exec: invalid verdict: %s", snippet(err.Error()))
	}
	return parsed, nil
}

func parseCodexVerdict(raw string) (JudgeVerdict, error) {
	var verdict JudgeVerdict
	if err := json.Unmarshal([]byte(stripFences(raw)), &verdict); err != nil {
		return JudgeVerdict{}, fmt.Errorf(
			"judge: malformed verdict JSON: %w (stdout: %s)", err, snippet(raw),
		)
	}
	if !verdict.Notify {
		return JudgeVerdict{Notify: false, Reason: truncate(verdict.Reason, maxBodyBytes)}, nil
	}
	if verdict.Urgency != UrgencyBlocked && verdict.Urgency != UrgencyDone {
		return JudgeVerdict{}, fmt.Errorf(
			"judge: invalid notified urgency (invalid urgency %q) (stdout: %s)",
			verdict.Urgency, snippet(raw),
		)
	}

	verdict.Task = truncateWords(normalizeCodexPlainText(verdict.Task), maxTaskBytes)
	verdict.Body = truncateWords(normalizeCodexPlainText(verdict.Body), maxBodyBytes)
	return verdict, nil
}

func normalizeCodexPlainText(raw string) string {
	raw = strings.ToValidUTF8(raw, "")
	if isCodexRawJSON(raw) {
		return ""
	}

	lines := strings.Split(raw, "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			continue
		}
		line = stripCodexMarkdownPrefix(line)
		line = unwrapCodexMarkdownLinks(line)
		line = strings.ReplaceAll(line, "`", "")
		for _, marker := range []string{"**", "__", "~~", "*", "_"} {
			line = unwrapCodexMarkdownMarker(line, marker)
		}
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}

	plain := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if isCodexRawJSON(plain) {
		return ""
	}
	return plain
}

func isCodexRawJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 || (s[0] != '{' && s[0] != '[') {
		return false
	}
	return json.Valid([]byte(s))
}

func stripCodexMarkdownPrefix(line string) string {
	for {
		before := line
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, ">") {
			line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
		}
		if headingLen := codexHeadingPrefixLen(line); headingLen > 0 {
			line = strings.TrimSpace(line[headingLen:])
		}
		if listLen := codexListPrefixLen(line); listLen > 0 {
			line = strings.TrimSpace(line[listLen:])
		}
		if line == before {
			return line
		}
	}
}

func codexHeadingPrefixLen(line string) int {
	count := 0
	for count < len(line) && count < 6 && line[count] == '#' {
		count++
	}
	if count > 0 && count < len(line) && unicode.IsSpace(rune(line[count])) {
		return count + 1
	}
	return 0
}

const (
	codexListMarkerWidth    = 2
	codexLinkSeparatorWidth = len("](")
)

func codexListPrefixLen(line string) int {
	if len(line) >= codexListMarkerWidth && strings.ContainsRune("-*+", rune(line[0])) &&
		unicode.IsSpace(rune(line[1])) {
		return codexListMarkerWidth
	}

	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(line) && (line[digits] == '.' || line[digits] == ')') &&
		unicode.IsSpace(rune(line[digits+1])) {
		return digits + codexListMarkerWidth
	}
	return 0
}

func unwrapCodexMarkdownLinks(s string) string {
	var out strings.Builder
	for {
		open := strings.IndexByte(s, '[')
		if open < 0 {
			out.WriteString(s)
			return out.String()
		}
		labelEnd := strings.Index(s[open+1:], "](")
		if labelEnd < 0 {
			out.WriteString(s)
			return out.String()
		}
		labelEnd += open + 1
		urlEnd := strings.IndexByte(s[labelEnd+codexLinkSeparatorWidth:], ')')
		if urlEnd < 0 {
			out.WriteString(s)
			return out.String()
		}
		urlEnd += labelEnd + codexLinkSeparatorWidth

		prefix := strings.TrimSuffix(s[:open], "!")
		out.WriteString(prefix)
		out.WriteString(s[open+1 : labelEnd])
		s = s[urlEnd+1:]
	}
}

func unwrapCodexMarkdownMarker(s, marker string) string {
	for searchFrom := 0; searchFrom < len(s); {
		openOffset := strings.Index(s[searchFrom:], marker)
		if openOffset < 0 {
			return s
		}
		open := searchFrom + openOffset
		if codexMarkerStartsLiteralTechnicalToken(s, open, marker) {
			searchFrom = open + len(marker)
			continue
		}
		if !codexMarkerCanOpen(s, open, len(marker)) {
			searchFrom = open + len(marker)
			continue
		}

		closeFrom := open + len(marker)
		for closeFrom < len(s) {
			closeOffset := strings.Index(s[closeFrom:], marker)
			if closeOffset < 0 {
				return s
			}
			markerClose := closeFrom + closeOffset
			if codexMarkerCanClose(s, markerClose, len(marker)) {
				s = s[:open] + s[open+len(marker):markerClose] + s[markerClose+len(marker):]
				searchFrom = open
				break
			}
			closeFrom = markerClose + len(marker)
		}
	}
	return s
}

func codexMarkerStartsLiteralTechnicalToken(s string, at int, marker string) bool {
	token := strings.TrimRight(codexTokenAt(s, at), ".,;:!?)]}\"'")
	paired := strings.HasPrefix(token, marker) && strings.HasSuffix(token, marker) &&
		len(token) > 2*len(marker)
	if paired {
		content := token[len(marker) : len(token)-len(marker)]
		return strings.IndexFunc(content, func(r rune) bool {
			return unicode.IsLetter(r) || unicode.IsDigit(r)
		}) < 0
	}
	return strings.ContainsAny(token, `/\.`)
}

func codexTokenAt(s string, at int) string {
	start := at
	for start > 0 {
		previous, size := utf8.DecodeLastRuneInString(s[:start])
		if unicode.IsSpace(previous) {
			break
		}
		start -= size
	}
	end := at
	for end < len(s) {
		next, size := utf8.DecodeRuneInString(s[end:])
		if unicode.IsSpace(next) {
			break
		}
		end += size
	}
	return s[start:end]
}

func codexMarkerCanOpen(s string, at, markerLen int) bool {
	if at+markerLen >= len(s) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(s[at+markerLen:])
	if unicode.IsSpace(next) {
		return false
	}
	if at == 0 {
		return true
	}
	previous, _ := utf8.DecodeLastRuneInString(s[:at])
	return unicode.IsSpace(previous) || strings.ContainsRune("([{>\"'", previous)
}

func codexMarkerCanClose(s string, at, markerLen int) bool {
	if at == 0 {
		return false
	}
	previous, _ := utf8.DecodeLastRuneInString(s[:at])
	if unicode.IsSpace(previous) {
		return false
	}
	if at+markerLen == len(s) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(s[at+markerLen:])
	return unicode.IsSpace(next) || unicode.IsPunct(next)
}

func buildCodexJudgePrompt(digest string) string {
	return codexJudgeRubric + "\n\nDIGEST\n" + digest
}
