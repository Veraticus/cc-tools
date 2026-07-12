package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxNotificationTailLen bounds the fallback notification body when the
// judge errors — HookInput.LastAssistantMessage on Stop events, and
// HookInput.Message on Notification events: the tail (via truncateHeadWords)
// is what carries the meaningful content, matching the head/tail convention
// established in transcript.go/digest.go.
const maxNotificationTailLen = 160

// dryRunWouldArmWatchdogSuffix is appended to a decision's Reason whenever a
// dry run reaches an arm-the-watchdog branch without actually arming it.
const dryRunWouldArmWatchdogSuffix = " (would arm watchdog)"

// failOpenWindow rate-limits a compose/decide-mode judge-error fallback send
// to at most one per session within this long of its last notification: a
// run of judge failures must not each re-ping — the reliability invariant
// (an LLM failure may never lose a genuine ping) only requires that the
// FIRST fail-open after a quiet period gets through; repeats within the
// window are the same failure already surfaced.
const failOpenWindow = 10 * time.Minute

// DedupeState is Pipeline's interface onto per-session notify dedupe
// (last-notify time and message hash) and broadcast-claim coordination, so
// where that bookkeeping actually lives is a swappable concern: entirely in
// memory and confined to one goroutine (MemoryState via loopState —
// notifyd's dedupe store), or nowhere at all (NopState — the hook client's
// inline fallback, and Pipeline.dedupeState's own default). sessionID keys
// every session-scoped method because one DedupeState instance is shared
// across every session a process handles.
type DedupeState interface {
	// SinceLastNotify returns how long before now sessionID last notified;
	// negative means never. ctx carries no deadline today — it exists so
	// the daemon's loopState implementation can bail out of its loop round
	// trip on shutdown rather than storing ctx on itself.
	SinceLastNotify(ctx context.Context, sessionID string, now time.Time) time.Duration
	// SinceLastNotifySame returns how long before now sessionID last sent
	// message verbatim; negative when never, or the last send differed.
	SinceLastNotifySame(ctx context.Context, sessionID string, now time.Time, message string) time.Duration
	// MarkNotified records t/message as sessionID's last notification.
	MarkNotified(ctx context.Context, sessionID string, t time.Time, message string) error
	// ClaimSend atomically resolves whether a send for sessionID may
	// proceed at now — losing when the session already notified within
	// window, winning (and recording now/message as the last notification
	// in the same step) otherwise. Check and mark being one operation is
	// the point: the pre-judge SinceLastNotify snapshot goes stale during
	// the judge call, so two hook events racing through concurrent judge
	// evaluations would otherwise both pass the gate and double-ping. The
	// returned duration is how long before now the session last notified
	// (negative when never), for the caller's decision-log reason. dryRun
	// observes without recording.
	ClaimSend(
		ctx context.Context, sessionID string, now time.Time, message string, window time.Duration, dryRun bool,
	) (bool, time.Duration)
	// ClaimBroadcast atomically claims key for a window starting at now,
	// reporting whether this call won; see MemoryState.ClaimBroadcast's
	// first-claimant contract. dryRun observes without claiming.
	ClaimBroadcast(ctx context.Context, key string, window time.Duration, now time.Time, dryRun bool) bool
	// DeleteSession removes sessionID's dedupe record entirely, called on
	// SessionEnd so a MemoryState-backed daemon does not accumulate one
	// entry per session for its entire uptime — every session_id is a
	// fresh UUID, so without eviction the map only grows. ctx lets the
	// daemon's loopState implementation bail out of its loop round trip on
	// shutdown, exactly as its other methods do.
	DeleteSession(ctx context.Context, sessionID string)
}

// Pipeline is the top-level orchestrator for a single hook invocation: it
// scans the transcript, computes the deterministic Decide() gates, and
// routes to a plain send, silence, or the LLM judge — then delivers,
// records session state, and appends a decision-log entry. It never
// returns an error for a condition it can itself recover from: a failing
// transcript scan, judge call, or notification send all degrade to a
// documented fallback rather than losing the hook's exit code 0 contract.
type Pipeline struct {
	DryRun bool
	Judge  Judge
	Sender Sender
	Log    DecisionLog
	// State is where dedupe/broadcast-claim bookkeeping lives. The zero
	// value (nil) defaults to NopState{} — every session reports "never
	// notified" — so only callers that want real dedupe (notifyd's
	// loopState, backed by MemoryState) need to set it.
	State   DedupeState
	Environ []string
	Stdout  io.Writer
	SelfBin string
	// Workspace is the tmux locator (session:window-index, see
	// WorkspaceName) this hook's pane lives in, or "" outside tmux —
	// background jobs included. Notification titles use it as the
	// where-to-go segment, falling back to Host; judged and watchdog
	// bodies carry it as a locatorSuffix trailer.
	Workspace string
	// Host is the short hostname for titles and falls back to
	// ShortHostname() when empty; tests inject a fixed value.
	Host string
	// ParentPID is the claude process that invoked this hook (Frame.
	// ParentPID), forwarded into an armed watchdog's dead-session probe.
	// The hook client's inline fallback leaves it zero (Watchdog is nil
	// there anyway); the daemon overwrites it per connection from the
	// frame, alongside Environ and Workspace.
	ParentPID int
	// Watchdog arms/reaps the in-daemon watchdog for a session with
	// live/pending work. A nil Watchdog makes Pipeline.arm a no-op — the
	// hook client's inline fallback's documented degraded mode (see
	// Watchdog's doc comment).
	Watchdog Watchdog
}

// dedupeState returns p.State, or NopState{} when State is unset — see the
// State field's doc comment.
//
//nolint:ireturn // DedupeState is intentionally swappable (memory/nop); this is the field's single accessor.
func (p Pipeline) dedupeState() DedupeState {
	if p.State != nil {
		return p.State
	}
	return NopState{}
}

// Run executes the pipeline for one hook payload. It always returns nil:
// every failure mode (transcript scan, judge call, watchdog arm, delivery)
// is a documented fallback or a swallowed best-effort operation, never a
// reason to fail the hook itself.
func (p Pipeline) Run(ctx context.Context, in HookInput) error {
	now := time.Now()
	project := filepath.Base(in.CWD)
	host := p.Host
	if host == "" {
		host = ShortHostname()
	}
	// The where-to-go segment of generic notification titles: the tmux
	// workspace when the session lives in one, the machine otherwise. A
	// ping's urgency already rides the ntfy sound/priority and its content
	// rides the body, so a generic label ("needs input") would waste the
	// title slot on what the ping itself already says.
	locus := p.Workspace
	if locus == "" {
		locus = host
	}

	if in.HookEventName == "SessionEnd" {
		if p.Watchdog != nil {
			p.Watchdog.Reap(in.SessionID)
		}
		p.dedupeState().DeleteSession(ctx, in.SessionID)
		p.logRecord(in, now, DecisionRecord{Outcome: OutcomeSilent.String(), Reason: "session end"})
		return nil
	}

	var res ScanResult
	var scanErr error
	if in.TranscriptPath != "" {
		res, scanErr = p.scanTranscript(in.TranscriptPath)
	}
	reasonSuffix := ""
	if scanErr != nil {
		reasonSuffix = fmt.Sprintf(" (transcript error: %s)", scanErr)
	}

	// SinceLastNotifySame costs a state-file read plus a sha256 of the
	// message, and only the blocked-tier Notification gates ever read it —
	// so the dominant Stop path (every assistant-turn end) skips it, keeping
	// the sentinel that means "never/differs" instead.
	sinceSame := neverNotifiedDuration
	if in.HookEventName == eventNotification {
		sinceSame = p.dedupeState().SinceLastNotifySame(ctx, in.SessionID, now, in.Message)
	}
	env := Env{
		SinceLastNotify:     p.dedupeState().SinceLastNotify(ctx, in.SessionID, now),
		SinceLastNotifySame: sinceSame,
		Broadcast:           p.broadcastFacts(ctx, in, now),
	}
	d := Decide(in, res, env)

	switch d.Outcome {
	case OutcomeSilent:
		p.handleSilent(in, res, now, project, host, d, reasonSuffix)
	case OutcomeSend:
		p.handleSend(ctx, in, now, project, locus, host, d, reasonSuffix)
	case OutcomeJudge:
		p.handleJudge(ctx, in, res, env, now, project, locus, host, d, reasonSuffix)
	}
	return nil
}

// scanTranscript opens and scans path, returning a zero ScanResult on any
// failure (open or scan) so the caller can fall back to a pass-through
// decision rather than losing the hook event entirely.
func (p Pipeline) scanTranscript(path string) (ScanResult, error) {
	f, err := os.Open(path) //nolint:gosec // Path comes from the hook's own payload
	if err != nil {
		return ScanResult{}, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	res, err := ScanTranscript(f)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scanning transcript: %w", err)
	}
	return res, nil
}

// handleSilent implements the OutcomeSilent branch: log the silence, and
// either arm the watchdog (real run) or note that it would have (dry run).
func (p Pipeline) handleSilent(
	in HookInput,
	res ScanResult,
	now time.Time,
	project, host string,
	d Decision,
	reasonSuffix string,
) {
	reason := d.Reason + reasonSuffix
	if d.ArmWatchdog {
		if p.DryRun {
			reason += dryRunWouldArmWatchdogSuffix
		} else {
			p.arm(in, res, now, project, host)
		}
	}
	p.logRecord(in, now, DecisionRecord{Outcome: OutcomeSilent.String(), Reason: reason})
}

// handleSend implements the OutcomeSend branch: a deterministic send with
// no judge involved. The title's second segment is where to go, not what
// happened — the ping's sound/priority and body already carry the what. A
// broadcast is about a headless job, so the receiving session's workspace
// would be misleading; those use the host.
func (p Pipeline) handleSend(
	ctx context.Context,
	in HookInput,
	now time.Time,
	project, locus, host string,
	d Decision,
	reasonSuffix string,
) {
	body := d.Message
	if body == "" {
		body = sendLabel(in.NotificationType)
	}
	where := locus
	if in.NotificationType == notifTypeAgentNeedsInput || in.NotificationType == notifTypeAgentCompleted {
		where = host
	}
	n := Notification{Title: project + " · " + where, Body: body, Urgency: d.Urgency}

	sendSuffix := p.deliver(ctx, n)
	if !p.DryRun {
		_ = p.dedupeState().MarkNotified(ctx, in.SessionID, now, n.Body)
	}
	p.logRecord(in, now, DecisionRecord{
		Outcome: OutcomeSend.String(), Reason: d.Reason + reasonSuffix + sendSuffix,
		Urgency: n.Urgency, Title: n.Title, Body: n.Body,
	})
}

// sendLabel names the fallback body for a deterministic OutcomeSend whose
// event carried no message, by NotificationType. Titles never use it: the
// title slot carries where to go, not what happened.
func sendLabel(notificationType string) string {
	switch notificationType {
	case "agent-turn-complete":
		return "turn complete"
	case "permission_prompt":
		return "needs permission"
	case notifTypeAgentNeedsInput:
		return "needs input"
	case notifTypeAgentCompleted:
		return "job finished"
	default:
		return "notification"
	}
}

// handleJudge implements the OutcomeJudge branch: build the digest, call
// the judge, and route the verdict (or its absence) per JudgeMode.
func (p Pipeline) handleJudge(
	ctx context.Context, in HookInput, res ScanResult, env Env, now time.Time,
	project, locus, host string, d Decision, reasonSuffix string,
) {
	tasks := EnrichTasks(res.LiveTasks, now)
	digest := BuildDigest(DigestMeta{
		Project: project, Host: host, Event: in.HookEventName, LastAssistantMessage: in.LastAssistantMessage,
	}, res, tasks, now)

	start := time.Now()
	verdict, jerr := p.Judge.Evaluate(ctx, digest, d.JudgeMode)
	judgeMs := time.Since(start).Milliseconds()

	switch d.JudgeMode {
	case JudgeModeCompose:
		p.handleComposeVerdict(
			ctx, in, res, env, now, project, locus, host, d, verdict, jerr, digest, judgeMs, reasonSuffix,
		)
	case JudgeModeDecide:
		p.handleDecideVerdict(
			ctx, in, res, env, now, project, locus, host, d, verdict, jerr, digest, judgeMs, reasonSuffix,
		)
	case JudgeModeNone:
		// Decide never returns OutcomeJudge with JudgeModeNone; nothing to do.
	}
}

// composeFallbackBody picks the judge-error fallback text by event: a Stop
// describes the turn that just ended (from LastAssistantMessage, the field
// Stop populates), a Notification describes the idle session it fired on
// (from Message, the field Notification populates) — so the fallback never
// claims a turn ended on an event that never stopped one.
func composeFallbackBody(in HookInput) string {
	if in.HookEventName == eventNotification {
		body := truncateHeadWords(in.Message, maxNotificationTailLen)
		if body == "" {
			body = "session idle — waiting for input"
		}
		return body
	}
	body := truncateHeadWords(in.LastAssistantMessage, maxNotificationTailLen)
	if body == "" {
		body = "turn ended"
	}
	return body
}

// handleComposeVerdict implements the compose route: the send is already
// decided, the judge only writes better text. A judge error falls back to
// a deterministic notification titled by locus (where to go — a generic
// "session idle" label would waste the slot) — never silent, per the
// reliability invariant that an LLM failure may never lose a genuine ping.
func (p Pipeline) handleComposeVerdict(
	ctx context.Context, in HookInput, res ScanResult, env Env, now time.Time,
	project, locus, host string,
	d Decision, verdict JudgeVerdict, jerr error, digest string, judgeMs int64, reasonSuffix string,
) {
	if jerr != nil && failOpenSuppressed(env) {
		p.suppressJudgeError(in, res, env, now, project, host, JudgeModeCompose, jerr, digest, judgeMs, reasonSuffix)
		return
	}

	var n Notification
	if jerr == nil {
		n = Notification{Title: project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
	} else {
		n = Notification{Title: project + " · " + locus, Body: composeFallbackBody(in), Urgency: UrgencyDone}
	}
	n.Body += locatorSuffix(p.Workspace, host)

	claim := judgedClaim{
		in: in, res: res, now: now, project: project, host: host,
		d: d, mode: JudgeModeCompose, jerr: jerr, digest: digest, judgeMs: judgeMs,
		reasonSuffix: reasonSuffix + retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
	}
	if !p.claimJudgedSend(ctx, claim, n.Body) {
		return
	}

	sendSuffix := p.deliver(ctx, n)
	p.logJudged(
		in, now, OutcomeSend.String(),
		d.Reason+reasonSuffix+sendSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
		n, JudgeModeCompose, jerr, digest, judgeMs,
	)
}

// judgedClaim carries the context claimJudgedSend needs to log a suppressed
// judged send and keep watchdog coverage on it — everything its handler
// already had in scope, bundled because Go has no keyword arguments.
type judgedClaim struct {
	in            HookInput
	res           ScanResult
	now           time.Time
	project, host string
	d             Decision
	mode          JudgeMode
	jerr          error
	digest        string
	judgeMs       int64
	reasonSuffix  string
}

// claimJudgedSend runs the pre-delivery ClaimSend gate shared by every
// judged send path and reports whether the send may proceed. A lost claim
// means some other send for this session landed within dedupeWindow — most
// often while this evaluation's judge call was in flight (a racing hook
// event or watchdog), which the pre-judge SinceLastNotify snapshot cannot
// see. The suppression is logged as a silent outcome, and — exactly like the
// pre-judge silent branches — still arms the watchdog when the decision
// wanted one, so suppressing the ping never drops coverage of the live work
// behind it.
func (p Pipeline) claimJudgedSend(ctx context.Context, c judgedClaim, body string) bool {
	won, since := p.dedupeState().ClaimSend(ctx, c.in.SessionID, c.now, body, dedupeWindow, p.DryRun)
	if won {
		return true
	}

	reason := fmt.Sprintf("dedupe: notified %s ago (post-judge)", humanDuration(since)) + c.reasonSuffix
	if c.d.ArmWatchdog {
		if p.DryRun {
			reason += dryRunWouldArmWatchdogSuffix
		} else {
			p.arm(c.in, c.res, c.now, c.project, c.host)
		}
	}
	p.logJudged(c.in, c.now, OutcomeSilent.String(), reason, Notification{}, c.mode, c.jerr, c.digest, c.judgeMs)
	return false
}

// locatorSuffix renders the where-did-this-come-from trailer appended to
// judged and watchdog notification bodies, whose titles carry the judge's
// task label rather than a location: the tmux locator (session:window-index,
// see WorkspaceName) plus the host, whichever of the two are known.
func locatorSuffix(workspace, host string) string {
	switch {
	case workspace != "" && host != "":
		return "\n\n— " + workspace + " @ " + host
	case workspace != "":
		return "\n\n— " + workspace
	case host != "":
		return "\n\n— " + host
	default:
		return ""
	}
}

// failOpenSuppressed reports whether a compose/decide-mode judge error
// should be suppressed under failOpenWindow rather than falling back to a
// deterministic send: true only when this session notified within the
// window. SinceLastNotify negative means never notified, which must always
// send — the epic's anti-pattern guard is that the FIRST judge error may
// never fail to notify.
func failOpenSuppressed(env Env) bool {
	return env.SinceLastNotify >= 0 && env.SinceLastNotify < failOpenWindow
}

// suppressJudgeError implements the failOpenWindow rate limit shared by the
// compose- and decide-mode judge-error fallbacks: silent, watchdog armed,
// logged as the distinct "judge error" outcome so a suppressed repeat stays
// distinguishable in the decision log from a genuine silent verdict.
func (p Pipeline) suppressJudgeError(
	in HookInput, res ScanResult, env Env, now time.Time, project, host string,
	mode JudgeMode, jerr error, digest string, judgeMs int64, reasonSuffix string,
) {
	reason := fmt.Sprintf("suppressed: notified %s ago", humanDuration(env.SinceLastNotify)) + reasonSuffix
	if p.DryRun {
		reason += dryRunWouldArmWatchdogSuffix
	} else {
		p.arm(in, res, now, project, host)
	}
	p.logJudged(in, now, "judge error", reason, Notification{}, mode, jerr, digest, judgeMs)
}

// retriedWithoutModelSuffix renders whether the judge's runRetrying path
// retried without --model, as a decision-log Reason suffix — so an operator
// reading the log can see when the no-model retry path ran.
func retriedWithoutModelSuffix(retried bool) string {
	if retried {
		return " (judge retried without --model)"
	}
	return ""
}

// handleDecideVerdict implements the decide route: the judge itself chooses
// notify-or-silence. A judge error falls back to a deterministic send
// (documented asymmetry vs. the watchdog's silent decide-error path: the
// watchdog rechecks repeatedly so a silent skip is safe, but this hook path
// has no retry, so it fails open to a send instead of risking a lost ping).
func (p Pipeline) handleDecideVerdict(
	ctx context.Context,
	in HookInput,
	res ScanResult,
	env Env,
	now time.Time,
	project, locus, host string,
	d Decision,
	verdict JudgeVerdict,
	jerr error,
	digest string,
	judgeMs int64,
	reasonSuffix string,
) {
	if jerr != nil {
		if failOpenSuppressed(env) {
			p.suppressJudgeError(in, res, env, now, project, host, JudgeModeDecide, jerr, digest, judgeMs, reasonSuffix)
			return
		}
		body := truncateHeadWords(in.LastAssistantMessage, maxNotificationTailLen)
		if body == "" {
			body = "session has running background work"
		}
		n := Notification{Title: project + " · " + locus, Body: body, Urgency: UrgencyInfo}
		n.Body += locatorSuffix(p.Workspace, host)
		claim := judgedClaim{
			in: in, res: res, now: now, project: project, host: host,
			d: d, mode: JudgeModeDecide, jerr: jerr, digest: digest, judgeMs: judgeMs, reasonSuffix: reasonSuffix,
		}
		if !p.claimJudgedSend(ctx, claim, n.Body) {
			return
		}
		sendSuffix := p.deliver(ctx, n)
		p.logJudged(
			in, now, OutcomeSend.String(), d.Reason+reasonSuffix+sendSuffix, n, JudgeModeDecide, jerr, digest, judgeMs,
		)
		return
	}

	if !verdict.Notify {
		if d.ArmWatchdog && !p.DryRun {
			p.arm(in, res, now, project, host)
		}
		p.logJudged(
			in,
			now,
			OutcomeSilent.String(),
			verdict.Reason+reasonSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
			Notification{},
			JudgeModeDecide,
			nil,
			digest,
			judgeMs,
		)
		return
	}

	n := Notification{Title: project + " · " + verdict.Task, Body: verdict.Body, Urgency: verdict.Urgency}
	n.Body += locatorSuffix(p.Workspace, host)
	claim := judgedClaim{
		in: in, res: res, now: now, project: project, host: host,
		d: d, mode: JudgeModeDecide, digest: digest, judgeMs: judgeMs,
		reasonSuffix: reasonSuffix + retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
	}
	if !p.claimJudgedSend(ctx, claim, n.Body) {
		return
	}
	sendSuffix := p.deliver(ctx, n)
	p.logJudged(
		in, now, OutcomeSend.String(),
		d.Reason+reasonSuffix+sendSuffix+retriedWithoutModelSuffix(verdict.RetriedWithoutModel),
		n, JudgeModeDecide, nil, digest, judgeMs,
	)
}

// deliver sends n: in DryRun mode it writes a "DRY RUN: ..." line to Stdout
// (and never fails); otherwise it calls Sender.Send. A send failure never
// propagates as an error — it returns a " (send failed: ...)" suffix for
// the caller to fold into the decision log's Reason field, since delivery
// failure must never fail the hook.
func (p Pipeline) deliver(ctx context.Context, n Notification) string {
	if p.DryRun {
		_, _ = fmt.Fprintf(p.Stdout, "DRY RUN: [%s] %s — %s\n", n.Urgency, n.Title, n.Body)
		return ""
	}
	if err := p.Sender.Send(ctx, n); err != nil {
		return fmt.Sprintf(" (send failed: %s)", err)
	}
	return ""
}

// arm notifies p.Watchdog to start (or supersede) covering this session's
// live/pending work. A nil Watchdog — the hook client's inline fallback —
// makes this a no-op: see Watchdog's doc comment for the documented degraded
// mode that leaves.
func (p Pipeline) arm(in HookInput, res ScanResult, now time.Time, project, host string) {
	if p.Watchdog == nil {
		return
	}
	p.Watchdog.Arm(WatchdogArmRequest{
		SessionID:  in.SessionID,
		Transcript: in.TranscriptPath,
		Offset:     res.BytesScanned,
		ParentPID:  p.ParentPID,
		Workspace:  p.Workspace,
		Meta:       DigestMeta{Project: project, Host: host, Event: "recheck"},
		ArmedAt:    now,
		GoalArmed:  res.Goal.Status == GoalActive,
	})
}

// logRecord appends rec to the decision log with Time/SessionID/Event
// filled from now/in. Append errors are swallowed: logging must never be
// able to fail the hook.
func (p Pipeline) logRecord(in HookInput, now time.Time, rec DecisionRecord) {
	rec.Time = now
	rec.SessionID = in.SessionID
	rec.Event = in.HookEventName
	_ = p.Log.Append(rec)
}

// logJudged builds and appends a decision record for a judged evaluation:
// every judged record carries Digest, JudgeMode, JudgeMs, and (if non-nil)
// JudgeErr, regardless of which route the verdict took.
func (p Pipeline) logJudged(
	in HookInput, now time.Time, outcome, reason string, n Notification, mode JudgeMode, jerr error,
	digest string, judgeMs int64,
) {
	rec := DecisionRecord{
		Outcome: outcome, Reason: reason,
		Urgency: n.Urgency, Title: n.Title, Body: n.Body,
		JudgeMode: judgeModeLabel(mode), JudgeMs: judgeMs, Digest: digest,
	}
	if jerr != nil {
		rec.JudgeErr = jerr.Error()
	}
	p.logRecord(in, now, rec)
}

// ShortHostname returns os.Hostname() with any domain suffix stripped
// (everything from the first '.' onward). Returns "" if os.Hostname()
// errors.
func ShortHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	short, _, _ := strings.Cut(h, ".")
	return short
}
