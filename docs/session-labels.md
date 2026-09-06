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

## Consumers and fallbacks

A known shared session label replaces the cwd project component in normal daemon completion and explicit-input notification titles. Other harnesses can reuse that label for notifications, but `notifyd` does not force their terminal titles. Explicit input performs only a short local metadata read; it never advances counters, invokes the model, or waits for in-flight composition.

The inline outage fallback and dry-run processing do not open the label store or invoke naming. Missing completion identity, child/internal events, active goals, silent events, and `SessionEnd` do not mutate label metadata. `SessionEnd` deliberately retains resumable state.

Pi-side automatic/manual naming ownership, resume application, and rename-race guards are separate consumers. Persisting a shared label does not override a manual Pi session or pane name.
