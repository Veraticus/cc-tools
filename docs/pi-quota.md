# Pi quota helper

The quota operation is a stateless, one-request JSON-lines helper implemented by
`runtime/cli.mjs` and `runtime/quota.mjs`. It does not cache results, maintain UI
state, refresh in the background, or call a model. It reads at most 64 KiB as
one UTF-8 JSON value at EOF and writes exactly one JSON result line.

## Request

```json
{"version":1,"operation":"quota","model":{"provider":"openai-codex","id":"gpt-5.6-luna","base_url":"https://chatgpt.com/backend-api"}}
```

All keys are required and exact. `version` is `1` and `operation` is `quota`.
Provider and model ID are nonempty, control-free strings no larger than 256 UTF-8
bytes. `base_url` is a valid URL no larger than 512 bytes.

Only the `openai-codex` provider, the `openai-codex-responses` API, and upstream
HTTPS URLs on `chatgpt.com` with `/backend-api` or `/backend-api/codex` (with an
optional trailing slash) apply. User information, query strings, fragments,
other hosts, gateways, and redirects are not allowed. Allowed variants are
canonicalized to `https://chatgpt.com/backend-api`. A non-applicable provider,
model, API, or URL is rejected before authentication or HTTP as soon as that
fact is known.

The helper lazily imports the public Pi SDK and calls
`ModelRuntime.create({ signal, allowModelNetwork: false })`. It resolves exactly
the requested model and asks Pi for that model's authentication with the same
outer signal. Pi remains responsible for stored credentials, OAuth refresh, and
refresh locking; the helper does not read or implement an OAuth store. A valid
bearer JWT must contain a bounded string claim at
`https://api.openai.com/auth.chatgpt_account_id`. Only that claim is decoded.

For applicable authenticated requests, the helper performs one `GET` to the
fixed endpoint `https://chatgpt.com/backend-api/wham/usage` with only
`Authorization: Bearer ...` and `ChatGPT-Account-Id` authentication headers and
`redirect: "error"`. It never forwards arbitrary Pi auth headers, environment
values, or gateway URLs, and it never retries.

The CLI has one 15-second total budget covering stdin, lazy SDK import, runtime
initialization, auth resolution, HTTP, and output selection. The HTTP fetch and
bounded 64 KiB response-body read have their own 5-second deadline within that
total budget. Both deadlines abort cooperative work and race work that ignores
cancellation. Input and response bodies use fatal UTF-8 decoding, and unfinished
streams are canceled with bounded cleanup.

## Results

Success is one JSON line no larger than 4096 bytes:

```json
{"version":1,"ok":true,"provider":"openai-codex","base_url":"https://chatgpt.com/backend-api","account_key":"<64 lowercase hex characters>","fetched_at":1700000000000,"windows":{"five_hour":{"remaining_percent":75,"reset_at":1700000500000},"weekly":null}}
```

`account_key` is
`SHA-256(JSON.stringify([provider, canonicalBaseURL, accountID]))`. It is stable
when a bearer token rotates for the same account and changes when the account
changes. The bearer token itself is never hashed. Once identity is known,
failures also include `provider`, canonical `base_url`, and `account_key`, so a
future caller can detect an account switch even when the fetch fails. Failures
before identity is known omit those fields.

The upstream `rate_limit.primary_window` and `secondary_window` positions do not
determine output names. A `limit_window_seconds` duration of `18000` maps to
`five_hour`, and `604800` maps to `weekly`; these are protocol contract cases,
not claims about live observations. Unknown durations and malformed individual
windows are represented as `null`, never as invented 100% availability. Two
windows with the same recognized duration invalidate the response. A valid
window has finite `used_percent` from 0 through 100 and a safe reset time;
`remaining_percent` is `100 - used_percent`. A valid absolute `reset_at` in
seconds is preferred. A bounded nonnegative `reset_after_seconds` is used only
when the absolute field is missing. Missing or non-object `rate_limit` is
invalid, while an empty `rate_limit` object validly yields two null windows.

Failures use exactly one of:

- `invalid_request` — malformed, mistyped, non-exact, oversized, or unreadable input
- `not_applicable` — unsupported provider, model, API, or upstream URL
- `auth_unavailable` — Pi auth is absent, fails, or lacks a valid bounded account claim
- `auth_rejected` — upstream returned HTTP 401 or 403
- `rate_limited` — upstream returned HTTP 429
- `fetch_failed` — another HTTP status, redirect, or transport/body-read failure
- `invalid_response` — oversized, non-UTF-8, malformed, or structurally invalid success body
- `timeout` — the total or HTTP deadline expired

Results contain no raw account ID, token, arbitrary headers, environment values,
auth source, plan or billing metadata, response body, provider diagnostics, or
stack trace. The production entrypoint waits for the stdout callback to confirm
the single line is flushed and then exits explicitly.
