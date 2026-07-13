package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const codexJudgeRubric = `You write a notification summary for a Codex turn from the supplied digest.

Output contract:
- Respond with a single raw JSON object and nothing else; use no markdown code fences.
- Use exactly these keys: notify (bool), urgency (string), task (string), body (string), reason (string).
- Always set notify=true and urgency=done.
- task names the work in at most 6 words and at most 60 UTF-8 bytes.
- body is one sentence, at most 180 characters, summarizing the outcome in your own words.
- reason briefly explains how the final assistant message supports the summary.

Source authority:
- The FINAL ASSISTANT MESSAGE alone establishes actual delivery or completion.
- USER INPUT is context for task naming only, not proof that requested work was delivered.
- Preserve recommendations, validation, partial progress, blockers, and completed work as distinct outcomes.
- Summarize in your own words. Do not use tools.`

// CodexJudge invokes a compose-only Codex model to summarize a completed
// turn. It does not choose whether to notify and never retries another model.
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
	parsed, err := parseVerdict(out)
	if err != nil {
		return JudgeVerdict{}, fmt.Errorf("codex exec: invalid verdict: %s", snippet(err.Error()))
	}
	if !parsed.Notify {
		return JudgeVerdict{}, fmt.Errorf("codex exec: verdict set notify=false (stdout: %s)", snippet(out))
	}
	if parsed.Urgency != UrgencyDone {
		return JudgeVerdict{}, fmt.Errorf(
			"codex exec: verdict set urgency %q, want %q (stdout: %s)",
			parsed.Urgency, UrgencyDone, snippet(out),
		)
	}
	return parsed, nil
}

func buildCodexJudgePrompt(digest string) string {
	return codexJudgeRubric + "\n\nDIGEST\n" + digest
}
