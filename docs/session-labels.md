# Shared session labels

`notifyd` produces a shared, generated label for each exact harness/session pair. The first accepted completed exchange requests a label in the same Pi composition call that produces the notification body. After a successful generated label—or a successful `KEEP` response that omits a replacement—`notifyd` reconsiders naming after four additional completed exchanges. With uninterrupted success, requests therefore occur at exchanges 1, 5, and 9.

Naming also requires changed bounded user/assistant material since the last naming attempt. Distinct native completion IDs still count and notify when their text is identical, but identical material does not repeatedly request a name. A failed initial or later refresh keeps the prior label (or the cwd project fallback) and can retry on the next distinct exchange with changed material. A source retry with the same completion ID does not increment the exchange count or busy-loop naming.

## Persistent metadata

The daemon stores snapshots under `<state-base>/session-labels`. The directory is owner-only mode `0700`; regular snapshot files are owner-only mode `0600`. Each filename is a SHA-256 digest of the exact harness/session pair, so source identifiers cannot steer filesystem paths. Snapshots are bounded to 4 KiB and published atomically through a rooted temporary file and rename.

The strict JSON record contains only:

- schema version, harness, and session;
- current validated generated label;
- monotonically increasing source generation and latest native completion ID;
- completed exchange count and last successful refresh exchange; and
- a SHA-256 fingerprint of the last attempted bounded user/assistant pair.

It contains no source text, notification body, transcript path, cwd, environment, authentication material, or delivery claim. This state is minimal naming/source metadata, not an outbox and not a durable notification-delivery ledger. Corrupt, oversized, symlinked, incorrectly permissioned, or otherwise unavailable state is ignored safely: body composition and deterministic notification delivery continue with a fixed observable `labels unavailable` category and the already selected prior label, or the cwd fallback when no label was available.

The source generation prevents a late composition result from overwriting metadata for a newer accepted source. The older accepted notification is still delivered with its original harness, session, cwd, workspace, body, and valid captured/generated label. Metadata survives daemon reconstruction so a later Pi resume consumer can compare the native source identity and generation.

## Read-only metadata query

A local consumer can inspect one exact harness/session scope without contacting
`notifyd` or starting the Pi helper:

```bash
steward session-metadata --harness pi --session-id native-id
# For a non-default notify state root, add: --state-base /path/to/notify
```

A valid query exits zero, writes one newline-terminated JSON object of at most
2048 bytes to stdout, and leaves stderr and stdin untouched. Its fields are
exactly `version`, `status`, `harness`, `session_id`, `label`, `completion_id`,
`source_generation`, and `label_generation`; generation values are canonical
unsigned decimal strings so the full `uint64` range remains exact. `status` is
`known` for a validated snapshot, `missing` when the state base or exact
snapshot does not exist, and
`unavailable` when state is corrupt, oversized, unsafe, or unreadable. A known
record can have an empty `label` and a zero `label_generation`. Missing and
unavailable responses retain the requested `harness` and `session_id`, while
the label and completion ID are empty and both generations are `"0"`. Invalid
or duplicate flags, an unsupported harness, or an invalid session ID instead
return only `{"version":1,"status":"invalid_request"}` and exit 2.

`source_generation` is the validated record's monotonically increasing source
generation (internally equal to the completed exchange count), and
`completion_id` identifies that latest accepted source. `label_generation` is
the last successful refresh exchange, including a successful `KEEP` that left
the label text unchanged. Therefore source and label generations can differ.
Neither `known` nor matching generations prove that model generation or
notification delivery has completed; this naming record is not a delivery
ledger.

The command is strictly read-only. It does not create missing directories or
files, update counters or modification timestamps, invoke a model or sender,
expose source text or fingerprints, or rename a Pi session. A later Pi UI
consumer must still capture and recheck the same current session ID,
conversation branch, and manual/automatic naming ownership before applying a
label. That consumer may
use a small bounded number of rereads to observe an asynchronously published
snapshot, but this interface deliberately has no `pending` status.

## Consumers and fallbacks

A known shared session label replaces the cwd project component in normal daemon completion and explicit-input notification titles. Other harnesses can reuse that label for notifications, but `notifyd` does not force their terminal titles. Explicit input performs only a short local metadata read; it never advances counters, invokes the model, or waits for in-flight composition.

The inline outage fallback and dry-run processing do not open the label store or invoke naming. Missing completion identity, child/internal events, active goals, silent events, and `SessionEnd` do not mutate label metadata. `SessionEnd` deliberately retains resumable state.

Pi-side automatic/manual naming ownership, resume application, and rename-race guards are separate consumers. Persisting a shared label does not override a manual Pi session or pane name.
