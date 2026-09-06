# Steward Pi extension

Steward's Pi 0.85 adapter is `runtime/pi-extension.mjs`. The package extension order is intentional:

1. `runtime/pi-subagents.mjs` loads the pinned compiled pi-subagents factory.
2. `runtime/pi-extension.mjs` installs Steward's root-session behavior.

The adapter calls the pinned native child-context accessor once, synchronously, when its factory is constructed. A child returns before registering events, installing a footer, or submitting a notification. Coupling errors remain visible and are never interpreted as a root session. See [Pi child-context coupling](pi-child-context.md) for the physical-package invariant and construction proof.

This release owns settled notifications and the existing custom statusline footer. It does not add session-label UI, quota fetching, account lifecycle, authentication inspection, or provider calls.

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

`runtime/pi-notify.mjs` starts `cc-tools` without a shell and passes only `notify --harness pi` as arguments. Canonical JSON plus a newline is written to stdin; source text is never placed in argv. One process is attempted, with a 90-second deadline and no adapter retry or fallback policy. The existing CLI remains responsible for parsing, admission, daemon claims, and inline fallback. Spawn, stdin, exit, and deadline failures produce only one fixed safe diagnostic; child stdout, stderr, and raw failures are not exposed. Transport success requires both a successful stdin write callback and a zero process exit, in either order. This confirms the local pipe write and CLI outcome; it does not claim remote notification delivery.

A submitted snapshot is independent of footer/session generations. `/new`, `/resume`, `/fork`, `/reload`, and `/tree` cannot redirect it to a replacement session. Child, pipe, and deadline handles are unreferenced so best-effort client work cannot keep a quitting TUI alive; accepted daemon work is owned by the existing notifier daemon.

## Custom footer

The footer invokes the existing command boundary:

```text
cc-tools statusline <canonical-statusline-json>
```

Each call has a five-second timeout and receives an abort signal. The JSON retains the existing model ID/provider/display name, summed native assistant cost on the active branch, context percentage and window size, cwd/workspace fields, and thinking effort, with top-level `harness: "pi"`. It does not invent a quota or account namespace.

Rendering is cache-only. A width, branch, model, thinking, tool, turn, compaction, tree, or settled change schedules asynchronous refresh work. Concurrent changes coalesce, and a result is applied only if its context/branch/width generation is still current. A rejection, killed result (including a timeout reported as zero exit), nonzero exit, or output that is empty after trailing newline normalization keeps the last good line; a later lifecycle change retries.

Disposal is idempotent. It unsubscribes the branch listener once, aborts and clears an in-flight statusline process, restores Pi's built-in footer, and prevents late results or render requests. Session replacement and shutdown dispose the old footer before a replacement TUI can install another one.

## Source integration test

Run the owned source integration slice with:

```sh
node --test runtime/pi-integration.test.mjs
```

The test builds the current Go `cc-tools` command into an isolated temporary directory, starts its real `notifyd`, invokes the real lifecycle handler and default notification client, and exercises the real daemon pipeline/composer against an executable fake Pi helper and loopback HTTP ntfy capture. Its only lifecycle seam is a transparent `sendNotification` observer: it calls the real sender with the unchanged payload/dependencies, records native session and completion IDs, and requires every actual client submission promise to report success. It verifies same-ID admission dedupe, distinct assistant IDs with identical text, generated helper body markers, and silence from non-settled lifecycle events using helper and HTTP completion barriers rather than quiet sleeps. After those barriers, it requires `notifyd` to drain accepted work and exit successfully before asserting exact final helper/HTTP counts and session metadata generations.

Build, daemon startup/shutdown, HTTP listen/closure, and temporary-directory removal are bounded; child exit/error observation is installed immediately, and every spawned daemon is registered for cleanup before another await. Deterministic cleanup regressions cover already-exited and spawn-error children and prove later owned resources are still attempted after an earlier cleanup failure. Failure-safe cleanup attempts every child and server socket before restoring the environment and removing owned temporary state. The test replaces the runtime environment with an explicit local allowlist for the CLI, daemon, and helper, and uses synthetic credential sentinels to prove they reach neither helper nor HTTP transport. This is a credentials-free source integration test, not a package or live-acceptance test: it does not call a model/provider, authenticated ntfy service, or external network, and the helper and final HTTP endpoint remain controlled fakes.
