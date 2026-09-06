# Steward Pi extension

Steward's Pi 0.85 adapter is `runtime/pi-extension.mjs`. The package extension order is intentional:

1. `runtime/pi-subagents.mjs` loads the pinned compiled pi-subagents factory.
2. `runtime/pi-extension.mjs` installs Steward's root-session behavior.

The adapter calls the pinned native child-context accessor once, synchronously, when its factory is constructed. A child returns before registering events, installing a footer, or submitting a notification. Coupling errors remain visible and are never interpreted as a root session. See [Pi child-context coupling](pi-child-context.md) for the physical-package invariant and construction proof.

This release owns settled notifications, shared root-TUI session labels, the
custom statusline footer, and its Codex quota producer. Quota remains root-TUI
only and uses the existing isolated helper protocol; child sessions and non-TUI
modes perform no quota path, auth, cache, watch, helper, timer, or network work.

## Settled notifications

Only Pi's native `agent_settled` event can submit a notification, and only in `tui` mode. `agent_end`, `turn_end`, tool events, `SessionEnd`, and the `rpc`, `json`, and `print` modes do not submit notifications. Pi 0.85 defines settled as the point after retry, compaction/retry, and queued continuation work has finished.

At the beginning of the settled handler the adapter freezes these values from the current native branch:

- the native session ID and cwd;
- the latest assistant **message entry**, including an assistant whose text is empty;
- that entry's `id`, which is the completion identity even when the current leaf is metadata;
- text blocks from that assistant; and
- text blocks from the latest preceding user message.

Thinking, images, tool calls, tool results, metadata, and the full transcript are not copied. The adapter does not use the leaf ID, message index, a hash, or a fabricated `settled` ID. Session and completion identities must be nonempty, well-formed strings of at most 256 UTF-8 bytes without control characters. An unavailable identity skips submission with one fixed diagnostic.

User and assistant text share the notifier's 8192-byte UTF-8 limit. Each side reserves 4096 bytes and donates unused reserved space to the other side. Oversized values keep rune-safe tails, matching the canonical Go preparation boundary.

The resulting stdin object is exactly:

```json
{
  "schema_version": 1,
  "harness": "pi",
  "session_id": "native-session-id",
  "hook_event_name": "TurnComplete",
  "completion_id": "native-assistant-entry-id",
  "cwd": "/work/project",
  "message": "latest preceding user text",
  "last_assistant_message": "latest assistant text"
}
```

`runtime/pi-notify.mjs` starts `steward` without a shell and passes only `notify --harness pi` as arguments. Canonical JSON plus a newline is written to stdin; source text is never placed in argv. One process is attempted, with a 90-second deadline and no adapter retry or fallback policy. The existing CLI remains responsible for parsing, admission, daemon claims, and inline fallback. Spawn, stdin, exit, and deadline failures produce only one fixed safe diagnostic; child stdout, stderr, and raw failures are not exposed. Transport success requires both a successful stdin write callback and a zero process exit, in either order. This confirms the local pipe write and CLI outcome; it does not claim remote notification delivery.

A submitted snapshot is independent of footer/session generations. `/new`, `/resume`, `/fork`, `/reload`, and `/tree` cannot redirect it to a replacement session. Child, pipe, and deadline handles are unreferenced so best-effort client work cannot keep a quitting TUI alive; accepted daemon work is owned by the existing notifier daemon.

## Shared session labels

After freezing the settled notification snapshot and starting its independent
submission, the root TUI schedules read-only
`steward session-metadata --harness pi --session-id <native-id>` queries for the
same native assistant entry. Session start also schedules a terminal assistant
source already present on resume or fork. Agent activity first cancels stale
work but does not erase the active session, so `agent_settled` can rearm it even
though Pi creates a fresh extension context for every event. Tree navigation
cancels the old branch and restores naming ownership before a later settled
event can schedule the selected branch.

One controller runs at most one metadata read at a time. Its six reread offsets
are approximately 0, 250 ms, 1 s, 3 s, 8 s, and 16 s; overlapping timers
coalesce. Each subprocess is limited to two seconds and 2048 stdout bytes,
receives the native ID only on argv, does not use stdin, and requires bounded
valid JSON plus a zero close outcome. Child, stdout pipe, and timer handles are
unreferenced. Cancellation destroys the owned pipe and aborts the helper with
`SIGKILL`, so shutdown does not retain a stale output listener or keep the TUI
alive. Missing, unavailable, and known snapshots without a label end after the
same bounded schedule rather than creating a pending protocol or busy loop.

Metadata must identify the current terminal native assistant row and carry
canonical uint64 decimal generations. Known source generation is positive;
label generation is zero exactly for an empty label and otherwise identifies a
validated three- or four-word, at-most-60-byte label. The adapter compares the
full decimal range exactly, permits source and label generations to differ,
can apply a prior nonempty label before a later refresh arrives, and observes
remaining scheduled reads even after a matching result. It neither generates
labels nor recreates the daemon's exchange cadence.

Automatic ownership is a minimal versioned Pi `custom` entry, not a
`custom_message`, and therefore never enters model context. It records the
originating native session ID, native `session_info` row ID, normalized label,
and source/label generations. The latest naming row must match that record to
remain automatic across resume, fork, or tree navigation. An inherited fork can
therefore retain automatic provenance, but its generation baseline applies only
to the originating native session; the fork records its new native session ID
on its first accepted metadata result, including when the label text is
unchanged. Otherwise an existing name or clear is manual, including same-text
names and malformed state. Manual ownership persists until a different session
lifecycle provides sound automatic provenance.

`pi.setSessionName()` appends the native row and emits its title event before
its extension callback finishes. The adapter records rows before and after its
synchronous call and claims ownership only when exactly one expected row is
still latest. A reentrant `/name`, even with the same text, appends an extra row
and remains manual. A delayed event for a successfully persisted owned row is
recognized rather than canceling the controller. Persistence failure leaves the
new native row unowned and reports only the fixed `Steward labels unavailable`
diagnostic.

The native setter first emits Pi's supported `Pi - name - cwd` terminal title.
After rechecking lifecycle, session, branch source, generations, and native row
ownership, the adapter calls public `ctx.ui.setTitle(label)` for the exact shared
label. A later manual rename emits a newer native title and wins. Normal OSC 0
handling lets tmux expose that value as pane `#T`; Steward does not rename tmux
windows or make atomicity claims about unrelated out-of-band title commands.
Non-TUI modes, child sessions, and other harness terminals do no label work.

## Codex quota lifecycle

For an exact `openai-codex` / `openai-codex-responses` model on the normalized
ChatGPT backend, one root-local controller launches the bounded quota helper.
Only that credential-owning helper uses Pi's public model/auth API and performs
the single fixed usage fetch. The extension itself never reads auth content and
never performs provider HTTP.

Before statting auth, the controller installs a nonpersistent parent-directory
watch for `auth.json`. This is intentional: Pi can atomically replace the file.
The path must be an owner-only regular file. Relevant rename/change events,
missing filenames, watch failures, unsafe metadata, parent replacement, model
replacement, and disposal synchronously clear last-good quota, abort owned work,
and advance generations. Auth metadata is checked before and after helper and
cache awaits, including a final awaited stat fence, so an obsolete completion
cannot publish or repopulate cache.

Successful identity is only the helper's scoped `account_key`. A per-auth-path
SHA-256 cache name under `${XDG_CACHE_HOME:-$HOME/.cache}/steward/pi-quota` avoids
putting identity in filenames. The directory and canonical JSON file are
owner-only, bounded, non-symlink regular paths; writes are serialized atomic
replacement and generation-guarded on both sides of awaited filesystem work.
Corrupt or unsafe cache state fails closed with a fixed diagnostic. Matching
cache data can be used only after the helper establishes identity. Same-account
transient failures can retain stale data, while unknown/rejected auth,
non-applicability, and different accounts clear immediately.

Native `agent_settled` assistant entry IDs deduplicate completion refreshes across
fresh Pi contexts. Concurrent triggers coalesce to one pending rerun, and one
periodic refresh is armed at exactly five minutes while applicable. Snapshot
freshness changes at exactly five minutes and expires at exactly 15 minutes;
timers also invalidate the footer even without another Pi event. Session/model
replacement and shutdown close the auth watch, cancel timers, abort the helper,
and prevent late cache or UI effects.

## Custom footer

The footer invokes the existing command boundary:

```text
steward statusline <canonical-statusline-json>
```

Each call has a five-second timeout and receives an abort signal. The JSON retains the existing model ID/provider/display name, summed native assistant cost on the active branch, context percentage and window size, cwd/workspace fields, and thinking effort, with top-level `harness: "pi"`. When available it also includes
the validated `steward_quota` renderer snapshot. It never includes account key,
raw identity, auth data, or arbitrary helper fields.

Rendering is cache-only. A width, branch, model, thinking, tool, turn,
compaction, tree, settled, or quota change schedules asynchronous refresh work.
Concurrent changes coalesce, and a result is applied only if its
context/branch/width/quota generation is still current. Ordinary renderer
rejection, killed result (including a timeout reported as zero exit), nonzero
exit, or output empty after trailing-newline normalization keeps the last good
line. Security-sensitive quota invalidation is different: it synchronously
erases that line and aborts the old renderer before requesting replacement.

Disposal is idempotent. It unsubscribes the branch listener once, aborts and clears an in-flight statusline process, restores Pi's built-in footer, and prevents late results or render requests. Session replacement and shutdown dispose the old footer before a replacement TUI can install another one.

## Source integration test

Run the owned source integration slice with:

```sh
node --test runtime/pi-integration.test.mjs
```

The test builds the current Go `steward` command into an isolated temporary directory, starts its real `notifyd`, invokes the real lifecycle handler and default notification client, and exercises the real daemon pipeline/composer against an executable fake Pi helper and loopback HTTP ntfy capture. Its three lifecycle seams are a transparent `sendNotification` observer that calls the real sender with unchanged payload/dependencies, a metadata-read wrapper that calls the production reader and actual CLI with the isolated integration state base plus deterministic delivery barriers, and an injected schedule that selects bounded label-read moments without replacing the reader. The observer records native session and completion IDs and requires every actual client submission promise to report success. The test verifies same-ID admission dedupe, distinct assistant IDs with identical text, generated helper body markers, and silence from non-settled lifecycle events using helper and HTTP completion barriers rather than quiet sleeps. The same lifecycle uses real native `SessionManager` entries. It asserts the generated native name and exact title, persisted ownership of the original native naming row and session, a manual override during a late real metadata result, and rejection of that result after replacement by a new UI. After those barriers, it requires `notifyd` to drain accepted work and exit successfully before asserting exact final helper/HTTP counts and session metadata generations.

Build, daemon startup/shutdown, HTTP listen/closure, and temporary-directory removal are bounded; child exit/error observation is installed immediately, and every spawned daemon is registered for cleanup before another await. Deterministic cleanup regressions cover already-exited and spawn-error children and prove later owned resources are still attempted after an earlier cleanup failure. Failure-safe cleanup attempts every child and server socket before restoring the environment and removing owned temporary state. The test replaces the runtime environment with an explicit local allowlist for the CLI, daemon, and helper, and uses synthetic credential sentinels to prove they reach neither helper nor HTTP transport. This is a credentials-free source integration test, not a package or live-acceptance test: it does not call a model/provider, authenticated ntfy service, or external network, and the helper and final HTTP endpoint remain controlled fakes.
