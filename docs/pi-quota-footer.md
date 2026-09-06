# Pi Codex quota footer

The statusline can render normalized Codex quota supplied by a Pi caller. This
is a renderer-only integration: it does not fetch quota, read credentials,
inspect authentication failures, cache responses, coalesce requests, or install
an extension.

## Input namespace

Quota rendering is opt-in. All of these scope fields must match exactly:

- top-level `harness` is `pi`;
- `model.provider` is `openai-codex`;
- `steward_quota` is present;
- `steward_quota.provider` is `openai-codex`; and
- `steward_quota.base_url` is
  `https://chatgpt.com/backend-api` (no alternate spelling or trailing slash).

For example:

```json
{"harness":"pi","model":{"provider":"openai-codex"},"steward_quota":{"provider":"openai-codex","base_url":"https://chatgpt.com/backend-api","fetched_at":1751700000000,"stale":false,"windows":{"five_hour":{"remaining_percent":75,"reset_at":1751707200000},"weekly":{"remaining_percent":20,"reset_at":1751959200000}}}}
```

`fetched_at` and both `reset_at` values are Unix timestamps in **milliseconds**.
This differs from the existing Claude `rate_limits.*.resets_at` fields, which
remain Unix timestamps in seconds and are otherwise unchanged.

The namespace intentionally contains no account key, raw account ID, bearer
token, authentication source, or environment-derived field. The account-aware
Pi producer owns immediate clearing after authentication metadata or account
changes; this renderer does not claim to detect those changes itself.

Inputs outside the exact scope, including an absent `steward_quota`, preserve
the existing Claude/Patchbay/cost rendering path byte-for-byte.

## Validation and freshness

Each quota window is independently known only when both fields are present:

- finite `remaining_percent` from 0 through 100; and
- a nonnegative millisecond `reset_at`.

A missing, null, or invalid window is shown as `5h?` or `7d?`; it is never
invented as 100% remaining or mistaken for known 0%. Percentages are displayed
at integer precision. Past reset timestamps are displayed as `now`, and very
distant resets are bounded at `>999d` so a timestamp cannot consume the width
budget.

Freshness is evaluated against the statusline's current clock:

- age less than 5 minutes with `stale:false` is **fresh**;
- `stale:true`, or age from exactly 5 minutes to less than 15 minutes, is
  **stale**;
- age exactly 15 minutes or older is **unknown**; and
- missing/nonpositive/future `fetched_at`, or a missing `stale` flag, is also
  **unknown**.

Unknown freshness clears both validated metrics before rendering, so an old
percentage cannot reappear in a compact or squeezed layout. A producer can send
an explicit cleared value as follows:

```json
{"harness":"pi","model":{"provider":"openai-codex"},"steward_quota":{"provider":"openai-codex","base_url":"https://chatgpt.com/backend-api","fetched_at":0,"stale":false,"windows":{"five_hour":null,"weekly":null}}}
```

The footer always labels both windows (`5h` and `7d`) and explicitly reports
`fresh`, `stale`, or `unknown`. A known 0% remaining value is quota depletion,
not the Claude monetary `EXTRA` alarm.

## Layout priority

Within the exact Pi/Codex scope, quota occupies the middle-chip slot ahead of
optional cost and decorative context information. Wide rendering reserves the
compact quota width before sizing left/right details. At terminal widths 80 and
below, quota is a protected narrow chip: environment, branch, context, cost,
and directory detail yield before quota. The compact form keeps both window
percentages, reset clocks, and freshness meaningful at widths 40, 60, and 80;
pathological smaller widths may fall back to an honest `quota` summary marker
rather than display a clipped metric.

## Pi producer freshness and invalidation

The root-TUI Pi extension now provides this renderer namespace. It refreshes at
exactly five minutes while an applicable Codex model is active and marks retained
same-account data stale from exactly five minutes through just before 15 minutes.
At exactly 15 minutes it clears the snapshot. Future timestamps are unknown.
Transient same-account failures may retain bounded stale data; unknown identity,
authentication rejection/unavailability, non-applicability, and known account
switches clear immediately.

The producer watches the parent directory containing Pi's `auth.json`, so atomic
replacement is observable. Every relevant auth event synchronously clears the
last-good footer line, aborts helper and renderer work, and fences late async
results before recovery. Model/session replacement and shutdown do the same.
The renderer remains independently defensive: it still validates freshness and
cannot receive account identity.
