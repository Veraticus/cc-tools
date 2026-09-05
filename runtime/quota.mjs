import { createHash } from 'node:crypto';

const encoder = new TextEncoder();
const fatalDecoder = new TextDecoder('utf-8', { fatal: true });
const controls = /[\u0000-\u001f\u007f-\u009f]/u;
const jwtPart = /^[A-Za-z0-9_-]+$/u;
const canonicalBaseUrl = 'https://chatgpt.com/backend-api';
const usageEndpoint = `${canonicalBaseUrl}/wham/usage`;
const maximumBodyBytes = 64 * 1024;
const maximumJwtBytes = 64 * 1024;
const maximumAccountBytes = 1024;
const maximumWireBytes = 4096;
const totalBudgetMs = 15000;
const httpBudgetMs = 5000;
const cleanupBudgetMs = 25;
const fiveHourSeconds = 18000;
const weeklySeconds = 604800;
const jwtClaimPath = 'https://api.openai.com/auth';

/** @typedef {{version: 1, operation: 'quota', model: {provider: string, id: string, base_url: string}}} QuotaRequest */
/** @typedef {'invalid_request'|'not_applicable'|'auth_unavailable'|'auth_rejected'|'rate_limited'|'fetch_failed'|'invalid_response'|'timeout'} QuotaError */
/** @typedef {{remaining_percent: number, reset_at: number}} QuotaWindow */
/** @typedef {{version: 1, ok: true, provider: 'openai-codex', base_url: 'https://chatgpt.com/backend-api', account_key: string, fetched_at: number, windows: {five_hour: QuotaWindow|null, weekly: QuotaWindow|null}}} QuotaSuccess */
/** @typedef {{version: 1, ok: false, error: QuotaError, provider?: 'openai-codex', base_url?: 'https://chatgpt.com/backend-api', account_key?: string}} QuotaFailure */
/** @typedef {QuotaSuccess|QuotaFailure} QuotaResult */
/** @typedef {{provider: 'openai-codex', baseUrl: 'https://chatgpt.com/backend-api', accountKey: string}} AccountIdentity */
/** @typedef {import('@earendil-works/pi-ai').Model<import('@earendil-works/pi-ai').Api>} PiModel */
/** @typedef {import('@earendil-works/pi-ai').AuthResult} AuthResult */
/** @typedef {{signal: AbortSignal, allowModelNetwork: false}} RuntimeCreateOptions */
/** @typedef {{signal: AbortSignal}} AuthOptions */
/** @typedef {{getModel(provider: string, id: string): PiModel|undefined, getAuth(model: PiModel, options: AuthOptions): Promise<AuthResult|undefined>}} RuntimePort */
/** @typedef {(options: RuntimeCreateOptions) => Promise<RuntimePort>} RuntimeFactory */
/** @typedef {typeof globalThis.fetch} FetchFunction */
/** @typedef {{runtimeFactory?: RuntimeFactory, fetch?: FetchFunction, timeoutMs?: number, httpTimeoutMs?: number, now?: () => number}} QuotaDependencies */
/** @typedef {{kind: 'text', text: string}|{kind: 'invalid'}|{kind: 'fetch_failed'}|{kind: 'timeout'}} BodyResult */
/** @typedef {{kind: 'valid', windows: {five_hour: QuotaWindow|null, weekly: QuotaWindow|null}}|{kind: 'invalid'}} WindowsResult */

/** @param {string} value */
function bytes(value) {
  return encoder.encode(value).byteLength;
}

/** @param {unknown} value @param {string[]} keys */
function exactObject(value, keys) {
  return value !== null
    && typeof value === 'object'
    && !Array.isArray(value)
    && Object.keys(value).length === keys.length
    && keys.every((key) => Object.hasOwn(value, key));
}

/** @param {unknown} value */
function objectRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/** @param {string} value */
function validUrl(value) {
  try {
    new URL(value);
    return true;
  } catch {
    return false;
  }
}

/** @param {unknown} value @returns {QuotaRequest|undefined} */
export function validateQuotaRequest(value) {
  if (!exactObject(value, ['version', 'operation', 'model'])) return undefined;
  const candidate = /** @type {Record<string, unknown>} */ (value);
  if (candidate.version !== 1 || candidate.operation !== 'quota' || !exactObject(candidate.model, ['provider', 'id', 'base_url'])) return undefined;
  const selected = /** @type {Record<string, unknown>} */ (candidate.model);
  if (typeof selected.provider !== 'string' || !selected.provider || bytes(selected.provider) > 256 || controls.test(selected.provider)) return undefined;
  if (typeof selected.id !== 'string' || !selected.id || bytes(selected.id) > 256 || controls.test(selected.id)) return undefined;
  if (typeof selected.base_url !== 'string' || bytes(selected.base_url) > 512 || !validUrl(selected.base_url)) return undefined;
  return /** @type {QuotaRequest} */ (value);
}

/** @param {string} value @returns {'https://chatgpt.com/backend-api'|undefined} */
function normalizeUpstream(value) {
  if (!value || value !== value.trim() || controls.test(value)) return undefined;
  try {
    const url = new URL(value);
    if (url.protocol !== 'https:'
      || url.hostname !== 'chatgpt.com'
      || url.port !== ''
      || url.username !== ''
      || url.password !== ''
      || url.search !== ''
      || url.hash !== ''
      || url.href.includes('?')
      || url.href.includes('#')) return undefined;
    if (!['/backend-api', '/backend-api/', '/backend-api/codex', '/backend-api/codex/'].includes(url.pathname)) return undefined;
    return canonicalBaseUrl;
  } catch {
    return undefined;
  }
}

/** @param {QuotaError} error @param {AccountIdentity|undefined} [identity] @returns {QuotaFailure} */
function failure(error, identity) {
  if (!identity) return { version: 1, ok: false, error };
  return {
    version: 1,
    ok: false,
    error,
    provider: identity.provider,
    base_url: identity.baseUrl,
    account_key: identity.accountKey,
  };
}

/** @param {string} apiKey @returns {string|undefined} */
function accountIdFromJwt(apiKey) {
  if (!apiKey || bytes(apiKey) > maximumJwtBytes) return undefined;
  const parts = apiKey.split('.');
  if (parts.length !== 3 || parts.some((part) => !part || !jwtPart.test(part))) return undefined;
  const payloadPart = parts[1];
  if (payloadPart === undefined || payloadPart.length > maximumJwtBytes) return undefined;

  let decoded;
  try {
    decoded = Buffer.from(payloadPart, 'base64url');
    if (decoded.toString('base64url') !== payloadPart) return undefined;
  } catch {
    return undefined;
  }
  if (decoded.byteLength > maximumJwtBytes) return undefined;

  let payload;
  try {
    payload = JSON.parse(fatalDecoder.decode(decoded));
  } catch {
    return undefined;
  }
  if (!objectRecord(payload)) return undefined;
  const claim = /** @type {Record<string, unknown>} */ (payload)[jwtClaimPath];
  if (!objectRecord(claim)) return undefined;
  const accountId = /** @type {Record<string, unknown>} */ (claim).chatgpt_account_id;
  if (typeof accountId !== 'string'
    || !accountId
    || accountId !== accountId.trim()
    || controls.test(accountId)
    || bytes(accountId) > maximumAccountBytes) return undefined;
  return accountId;
}

/** @param {string} accountId @returns {AccountIdentity} */
function identityFor(accountId) {
  const accountKey = createHash('sha256')
    .update(JSON.stringify(['openai-codex', canonicalBaseUrl, accountId]))
    .digest('hex');
  return { provider: 'openai-codex', baseUrl: canonicalBaseUrl, accountKey };
}

/** @param {() => Promise<unknown>|unknown} action */
async function settleBounded(action) {
  let operation;
  try {
    operation = Promise.resolve(action());
  } catch {
    return;
  }
  await new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(undefined);
    };
    const timer = setTimeout(finish, cleanupBudgetMs);
    operation.then(finish, finish);
  });
}

/** @param {number} seconds @returns {number|undefined} */
function secondsToSafeMilliseconds(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return undefined;
  const milliseconds = seconds * 1000;
  return Number.isSafeInteger(milliseconds) ? milliseconds : undefined;
}

/** @param {Record<string, unknown>} snapshot @param {number} fetchedAt @returns {QuotaWindow|null} */
function parseWindow(snapshot, fetchedAt) {
  const used = snapshot.used_percent;
  if (typeof used !== 'number' || !Number.isFinite(used) || used < 0 || used > 100) return null;

  let resetAt;
  if (Object.hasOwn(snapshot, 'reset_at')) {
    const absolute = snapshot.reset_at;
    if (typeof absolute !== 'number') return null;
    resetAt = secondsToSafeMilliseconds(absolute);
  } else {
    const relative = snapshot.reset_after_seconds;
    if (typeof relative !== 'number') return null;
    const relativeMs = secondsToSafeMilliseconds(relative);
    if (relativeMs !== undefined) {
      const calculated = fetchedAt + relativeMs;
      if (Number.isSafeInteger(calculated) && calculated >= 0) resetAt = calculated;
    }
  }
  if (resetAt === undefined) return null;
  return { remaining_percent: 100 - used, reset_at: resetAt };
}

/** @param {unknown} payload @param {number} fetchedAt @returns {WindowsResult} */
function parseWindows(payload, fetchedAt) {
  if (!objectRecord(payload)) return { kind: 'invalid' };
  const rateLimit = /** @type {Record<string, unknown>} */ (payload).rate_limit;
  if (!objectRecord(rateLimit)) return { kind: 'invalid' };

  /** @type {{five_hour: QuotaWindow|null, weekly: QuotaWindow|null}} */
  const windows = { five_hour: null, weekly: null };
  const recognized = new Set();
  const rateLimitRecord = /** @type {Record<string, unknown>} */ (rateLimit);
  for (const field of ['primary_window', 'secondary_window']) {
    const candidate = rateLimitRecord[field];
    if (!objectRecord(candidate)) continue;
    const snapshot = /** @type {Record<string, unknown>} */ (candidate);
    const duration = snapshot.limit_window_seconds;
    if (duration !== fiveHourSeconds && duration !== weeklySeconds) continue;
    if (recognized.has(duration)) return { kind: 'invalid' };
    recognized.add(duration);
    const parsed = parseWindow(snapshot, fetchedAt);
    if (duration === fiveHourSeconds) windows.five_hour = parsed;
    else windows.weekly = parsed;
  }
  return { kind: 'valid', windows };
}

/** @param {ReadableStreamDefaultReader<Uint8Array>} reader */
async function cancelReader(reader) {
  await settleBounded(() => reader.cancel());
}

/** @param {ReadableStream<Uint8Array>} stream */
async function cancelStream(stream) {
  await settleBounded(() => stream.cancel());
}

/**
 * Read a response body without relying on Response.text(), which has no size
 * bound. The HTTP controller is shared with the encompassing deadline.
 * @param {Response} response
 * @param {AbortSignal} signal
 * @param {(reader: ReadableStreamDefaultReader<Uint8Array>) => void} selectedReader
 * @returns {Promise<BodyResult>}
 */
async function readBody(response, signal, selectedReader) {
  if (!response.body) return { kind: 'invalid' };
  const reader = response.body.getReader();
  selectedReader(reader);
  /** @type {Uint8Array[]} */
  const chunks = [];
  let size = 0;
  let finished = false;
  try {
    while (!finished) {
      const item = await reader.read();
      if (signal.aborted) return { kind: 'timeout' };
      if (item.done) {
        finished = true;
        break;
      }
      if (!(item.value instanceof Uint8Array)) return { kind: 'invalid' };
      size += item.value.byteLength;
      if (size > maximumBodyBytes) return { kind: 'invalid' };
      chunks.push(item.value);
    }
  } catch {
    return signal.aborted ? { kind: 'timeout' } : { kind: 'fetch_failed' };
  } finally {
    if (!finished) await cancelReader(reader);
    try {
      reader.releaseLock();
    } catch {
      // A pending uncooperative read remains governed by the deadline race.
    }
  }

  const combined = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    combined.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try {
    return { kind: 'text', text: fatalDecoder.decode(combined) };
  } catch {
    return { kind: 'invalid' };
  }
}

/**
 * Fetch quota under a deadline independent of the larger helper budget.
 * @param {FetchFunction} fetcher
 * @param {string} apiKey
 * @param {string} accountId
 * @param {AccountIdentity} identity
 * @param {AbortSignal} outerSignal
 * @param {number} timeoutMs
 * @param {() => number} now
 * @returns {Promise<QuotaResult>}
 */
async function fetchQuota(fetcher, apiKey, accountId, identity, outerSignal, timeoutMs, now) {
  const controller = new AbortController();
  /** @type {Response|undefined} */
  let response;
  /** @type {ReadableStreamDefaultReader<Uint8Array>|undefined} */
  let reader;
  let timedOut = false;
  /** @type {ReadableStreamDefaultReader<Uint8Array>|undefined} */
  let canceledReader;
  /** @type {ReadableStream<Uint8Array>|undefined} */
  let canceledStream;
  /** @returns {Promise<void>} */
  const cleanup = async () => {
    if (reader) {
      if (canceledReader === reader) return;
      canceledReader = reader;
      await cancelReader(reader);
      return;
    }
    const stream = response?.body ?? undefined;
    if (!stream || canceledStream === stream) return;
    canceledStream = stream;
    await cancelStream(stream);
  };
  /** @type {(value: QuotaResult) => void} */
  let expire = () => {};
  /** @type {Promise<QuotaResult>} */
  const deadline = new Promise((resolve) => { expire = resolve; });
  const finishTimeout = () => {
    if (timedOut) return;
    timedOut = true;
    controller.abort();
    void cleanup();
    expire(failure('timeout', identity));
  };
  const timer = setTimeout(finishTimeout, Math.max(0, timeoutMs));
  outerSignal.addEventListener('abort', finishTimeout, { once: true });
  if (outerSignal.aborted) finishTimeout();

  try {
    /** @type {Promise<QuotaResult>} */
    const work = (async () => {
      try {
        response = await fetcher(usageEndpoint, {
          method: 'GET',
          headers: {
            Authorization: `Bearer ${apiKey}`,
            'ChatGPT-Account-Id': accountId,
          },
          redirect: 'error',
          signal: controller.signal,
        });
        if (timedOut || outerSignal.aborted || controller.signal.aborted) {
          await cleanup();
          return failure('timeout', identity);
        }

        if (response.status === 401 || response.status === 403) {
          void cleanup();
          return failure('auth_rejected', identity);
        }
        if (response.status === 429) {
          void cleanup();
          return failure('rate_limited', identity);
        }
        if (response.status < 200 || response.status >= 300) {
          void cleanup();
          return failure('fetch_failed', identity);
        }

        const body = await readBody(response, controller.signal, (selected) => { reader = selected; });
        if (timedOut || outerSignal.aborted || body.kind === 'timeout') return failure('timeout', identity);
        if (body.kind === 'fetch_failed') return failure('fetch_failed', identity);
        if (body.kind === 'invalid') return failure('invalid_response', identity);

        let parsed;
        try {
          parsed = JSON.parse(body.text);
        } catch {
          return failure('invalid_response', identity);
        }
        let fetchedAt;
        try {
          fetchedAt = now();
        } catch {
          return failure('invalid_response', identity);
        }
        if (!Number.isSafeInteger(fetchedAt) || fetchedAt < 0) return failure('invalid_response', identity);
        const parsedWindows = parseWindows(parsed, fetchedAt);
        if (parsedWindows.kind === 'invalid') return failure('invalid_response', identity);
        /** @type {QuotaSuccess} */
        const result = {
          version: 1,
          ok: true,
          provider: 'openai-codex',
          base_url: canonicalBaseUrl,
          account_key: identity.accountKey,
          fetched_at: fetchedAt,
          windows: parsedWindows.windows,
        };
        return bytes(`${JSON.stringify(result)}\n`) <= maximumWireBytes
          ? result
          : failure('invalid_response', identity);
      } catch {
        return timedOut || outerSignal.aborted || controller.signal.aborted
          ? failure('timeout', identity)
          : failure('fetch_failed', identity);
      }
    })();
    return await Promise.race([work, deadline]);
  } finally {
    clearTimeout(timer);
    outerSignal.removeEventListener('abort', finishTimeout);
    if (timedOut) void cleanup();
  }
}

/** @param {RuntimeCreateOptions} options @returns {Promise<RuntimePort>} */
async function createRuntime(options) {
  const { ModelRuntime } = await import('@earendil-works/pi-coding-agent');
  if (options.signal.aborted) throw new Error('runtime creation aborted');
  return ModelRuntime.create(options);
}

/** @param {unknown} value @param {QuotaDependencies} [dependencies] @returns {Promise<QuotaResult>} */
export async function quota(value, dependencies = {}) {
  const request = validateQuotaRequest(value);
  if (!request) return failure('invalid_request');
  if (request.model.provider !== 'openai-codex') return failure('not_applicable');
  const requestedBaseUrl = normalizeUpstream(request.model.base_url);
  if (!requestedBaseUrl) return failure('not_applicable');

  const timeoutMs = dependencies.timeoutMs ?? totalBudgetMs;
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) return failure('timeout');
  const controller = new AbortController();
  /** @type {AccountIdentity|undefined} */
  let identity;
  let timedOut = false;
  /** @type {(value: QuotaResult) => void} */
  let expire = () => {};
  /** @type {Promise<QuotaResult>} */
  const deadline = new Promise((resolve) => { expire = resolve; });
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
    expire(failure('timeout', identity));
  }, timeoutMs);
  const factory = dependencies.runtimeFactory ?? createRuntime;

  try {
    /** @type {Promise<QuotaResult>} */
    const work = (async () => {
      try {
        const runtime = await factory({ signal: controller.signal, allowModelNetwork: false });
        if (timedOut || controller.signal.aborted) return failure('timeout', identity);
        const selected = runtime.getModel(request.model.provider, request.model.id);
        if (!selected) return failure('not_applicable');
        if (selected.provider !== request.model.provider
          || selected.api !== 'openai-codex-responses'
          || typeof selected.baseUrl !== 'string'
          || normalizeUpstream(selected.baseUrl) !== requestedBaseUrl) return failure('not_applicable');
        if (timedOut || controller.signal.aborted) return failure('timeout', identity);

        const resolved = await runtime.getAuth(selected, { signal: controller.signal });
        if (timedOut || controller.signal.aborted) return failure('timeout', identity);
        if (!resolved || !objectRecord(resolved.auth)) return failure('auth_unavailable');
        const auth = /** @type {AuthResult['auth']} */ (resolved.auth);
        if (auth.baseUrl !== undefined
          && (typeof auth.baseUrl !== 'string' || normalizeUpstream(auth.baseUrl) !== requestedBaseUrl)) return failure('not_applicable');
        if (typeof auth.apiKey !== 'string') return failure('auth_unavailable');
        const accountId = accountIdFromJwt(auth.apiKey);
        if (!accountId) return failure('auth_unavailable');
        identity = identityFor(accountId);
        if (timedOut || controller.signal.aborted) return failure('timeout', identity);

        const httpTimeoutMs = dependencies.httpTimeoutMs ?? httpBudgetMs;
        if (!Number.isFinite(httpTimeoutMs) || httpTimeoutMs <= 0) return failure('timeout', identity);
        return fetchQuota(
          dependencies.fetch ?? globalThis.fetch,
          auth.apiKey,
          accountId,
          identity,
          controller.signal,
          httpTimeoutMs,
          dependencies.now ?? Date.now,
        );
      } catch {
        return timedOut || controller.signal.aborted
          ? failure('timeout', identity)
          : failure(identity ? 'fetch_failed' : 'auth_unavailable', identity);
      }
    })();
    return await Promise.race([work, deadline]);
  } finally {
    clearTimeout(timer);
  }
}
