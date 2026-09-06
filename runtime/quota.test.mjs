import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { normalizeUpstream, quota, validateQuotaRequest } from './quota.mjs';

/** @typedef {import('./quota.mjs').QuotaRequest} QuotaRequest */
/** @typedef {import('./quota.mjs').QuotaError} QuotaError */
/** @typedef {import('./quota.mjs').QuotaWindow} QuotaWindow */
/** @typedef {import('./quota.mjs').QuotaResult} QuotaResult */
/** @typedef {import('@earendil-works/pi-ai').Model<import('@earendil-works/pi-ai').Api>} PiModel */
/** @typedef {import('@earendil-works/pi-ai').AuthResult} AuthResult */
/** @typedef {{signal: AbortSignal, allowModelNetwork: false}} RuntimeCreateOptions */
/** @typedef {{signal: AbortSignal}} AuthOptions */
/** @typedef {{getModel(provider: string, id: string): PiModel|undefined, getAuth(model: PiModel, options: AuthOptions): Promise<AuthResult|undefined>}} RuntimePort */
/** @typedef {(options: RuntimeCreateOptions) => Promise<RuntimePort>} RuntimeFactory */
/** @typedef {Parameters<typeof globalThis.fetch>[0]} FetchInput */
/** @typedef {Parameters<typeof globalThis.fetch>[1]} FetchInit */
/** @typedef {Response|Promise<Response>|Error|((input: FetchInput, init: FetchInit) => Response|Promise<Response>)} FetchPlan */
/** @typedef {AuthResult|undefined|Promise<AuthResult|undefined>|Error} AuthPlan */
/** @typedef {{selectedModel?: PiModel, missingModel?: boolean, auth?: AuthPlan, fetch?: FetchPlan}} FakeOptions */
/** @typedef {{factoryCalls: number, getModelCalls: number, authCalls: number, fetchCalls: number, factoryOptions?: RuntimeCreateOptions, requestedModel?: [string, string], selectedModel?: PiModel, authOptions?: AuthOptions, fetchInput?: FetchInput, fetchInit?: FetchInit}} Seen */

const canonicalBaseUrl = 'https://chatgpt.com/backend-api';
const usageEndpoint = 'https://chatgpt.com/backend-api/wham/usage';
const fetchedAt = 1_700_000_000_000;
const accountOneKey = '30a7b46a4904936da76ec3f2a807d1fd8014b278ae2ea426d45443dee6853258';
const accountTwoKey = 'c7355acfd2e0fcc629c6a89fa26c6e056e3fe9c505f51b20d8402ff72dc939cf';

/** @type {QuotaRequest} */
const request = {
  version: 1,
  operation: 'quota',
  model: { provider: 'openai-codex', id: 'gpt-5.6-luna', base_url: canonicalBaseUrl },
};

/** @type {PiModel} */
const model = {
  id: 'gpt-5.6-luna',
  name: 'GPT-5.6 Luna',
  api: 'openai-codex-responses',
  provider: 'openai-codex',
  baseUrl: 'https://chatgpt.com/backend-api/codex/',
  reasoning: true,
  input: ['text'],
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  contextWindow: 272000,
  maxTokens: 128000,
};

/** @param {unknown} payload @param {string} [signature] */
function jwtPayload(payload, signature = 'signature') {
  return `${Buffer.from('{"alg":"RS256"}').toString('base64url')}.${Buffer.from(JSON.stringify(payload)).toString('base64url')}.${Buffer.from(signature).toString('base64url')}`;
}

/** @param {string} accountId @param {string} [signature] */
function jwt(accountId, signature) {
  return jwtPayload({ 'https://api.openai.com/auth': { chatgpt_account_id: accountId } }, signature);
}

/** @param {string} accountId @param {Partial<AuthResult['auth']>} [auth] @returns {AuthResult} */
function authentication(accountId, auth = {}) {
  return {
    auth: {
      apiKey: jwt(accountId),
      ...auth,
    },
    env: { PRIVATE_ENV_SENTINEL: 'PRIVATE-ENV-SENTINEL' },
    source: 'PRIVATE-SOURCE-SENTINEL',
  };
}

/** @param {unknown} rateLimit @returns {Response} */
function usageResponse(rateLimit) {
  return new Response(JSON.stringify({ rate_limit: rateLimit }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/** @param {FakeOptions} [options] @returns {{seen: Seen, factory: RuntimeFactory, fetcher: typeof globalThis.fetch}} */
function fake(options = {}) {
  /** @type {Seen} */
  const seen = { factoryCalls: 0, getModelCalls: 0, authCalls: 0, fetchCalls: 0 };
  const selectedModel = options.selectedModel ?? model;
  const authPlan = Object.hasOwn(options, 'auth') ? options.auth : authentication('acct-one');
  const fetchPlan = options.fetch ?? usageResponse({});
  /** @type {RuntimeFactory} */
  const factory = async (factoryOptions) => {
    seen.factoryCalls += 1;
    seen.factoryOptions = factoryOptions;
    return {
      getModel(provider, id) {
        seen.getModelCalls += 1;
        seen.requestedModel = [provider, id];
        return options.missingModel ? undefined : selectedModel;
      },
      async getAuth(selected, authOptions) {
        seen.authCalls += 1;
        seen.selectedModel = selected;
        seen.authOptions = authOptions;
        if (authPlan instanceof Error) throw authPlan;
        return authPlan;
      },
    };
  };
  /** @type {typeof globalThis.fetch} */
  const fetcher = async (input, init) => {
    seen.fetchCalls += 1;
    seen.fetchInput = input;
    seen.fetchInit = init;
    if (fetchPlan instanceof Error) throw fetchPlan;
    if (typeof fetchPlan === 'function') return fetchPlan(input, init);
    return fetchPlan;
  };
  return { seen, factory, fetcher };
}

/** @param {FakeOptions} [options] @param {{timeoutMs?: number, httpTimeoutMs?: number, now?: () => number}} [budgets] @returns {Promise<{result: QuotaResult, seen: Seen}>} */
async function runFake(options = {}, budgets = {}) {
  const f = fake(options);
  const result = await quota(request, {
    runtimeFactory: f.factory,
    fetch: f.fetcher,
    now: budgets.now ?? (() => fetchedAt),
    ...(budgets.timeoutMs === undefined ? {} : { timeoutMs: budgets.timeoutMs }),
    ...(budgets.httpTimeoutMs === undefined ? {} : { httpTimeoutMs: budgets.httpTimeoutMs }),
  });
  return { result, seen: f.seen };
}

/** @param {QuotaError} error @param {string} [accountKey] @returns {QuotaResult} */
function failure(error, accountKey) {
  return accountKey === undefined
    ? { version: 1, ok: false, error }
    : {
        version: 1,
        ok: false,
        error,
        provider: 'openai-codex',
        base_url: canonicalBaseUrl,
        account_key: accountKey,
      };
}

test('imports the public Pi SDK entrypoint without creating a runtime or using auth', async () => {
  const sdk = await import('@earendil-works/pi-coding-agent');
  assert.equal(typeof sdk.ModelRuntime.create, 'function');
});

test('validates the exact quota request, URL, field types, controls, and UTF-8 byte boundaries', () => {
  assert.deepEqual(validateQuotaRequest(request), request);
  assert.ok(validateQuotaRequest({ ...request, model: { ...request.model, provider: 'p'.repeat(256), id: 'é'.repeat(128) } }));

  const prefix = 'https://example.com/';
  const exactUrl = `${prefix}${'x'.repeat(512 - Buffer.byteLength(prefix))}`;
  assert.equal(Buffer.byteLength(exactUrl), 512);
  assert.ok(validateQuotaRequest({ ...request, model: { ...request.model, base_url: exactUrl } }));

  /** @type {unknown[]} */
  const invalid = [
    null,
    [],
    { ...request, extra: true },
    { version: 1, operation: 'quota', model: request.model, extra: undefined },
    { ...request, version: 2 },
    { ...request, operation: 'compose' },
    { ...request, model: { ...request.model, extra: true } },
    { ...request, model: { provider: request.model.provider, id: request.model.id } },
    { ...request, model: { ...request.model, provider: 1 } },
    { ...request, model: { ...request.model, provider: '' } },
    { ...request, model: { ...request.model, provider: 'openai\nother' } },
    { ...request, model: { ...request.model, provider: 'é'.repeat(129) } },
    { ...request, model: { ...request.model, id: null } },
    { ...request, model: { ...request.model, id: '' } },
    { ...request, model: { ...request.model, id: 'model\u007fother' } },
    { ...request, model: { ...request.model, id: 'x'.repeat(257) } },
    { ...request, model: { ...request.model, base_url: 1 } },
    { ...request, model: { ...request.model, base_url: '' } },
    { ...request, model: { ...request.model, base_url: '/backend-api' } },
    { ...request, model: { ...request.model, base_url: `${exactUrl}x` } },
  ];
  for (const value of invalid) assert.equal(validateQuotaRequest(value), undefined);
});

test('exports the shared pure upstream normalization without auth or HTTP work', () => {
  for (const value of [
    canonicalBaseUrl,
    `${canonicalBaseUrl}/`,
    `${canonicalBaseUrl}/codex`,
    `${canonicalBaseUrl}/codex/`,
  ]) assert.equal(normalizeUpstream(value), canonicalBaseUrl);
  for (const value of [
    'https://gateway.example/backend-api',
    'http://chatgpt.com/backend-api',
    'https://chatgpt.com/backend-api?query=1',
  ]) assert.equal(normalizeUpstream(value), undefined);
});

test('rejects non-Codex providers and request upstreams before runtime, auth, or HTTP', async () => {
  for (const value of [
    { ...request, model: { ...request.model, provider: 'openai' } },
    { ...request, model: { ...request.model, base_url: 'https://gateway.example/backend-api' } },
    { ...request, model: { ...request.model, base_url: 'http://chatgpt.com/backend-api' } },
    { ...request, model: { ...request.model, base_url: 'https://user@chatgpt.com/backend-api' } },
    { ...request, model: { ...request.model, base_url: 'https://chatgpt.com/backend-api?next=gateway' } },
    { ...request, model: { ...request.model, base_url: 'https://chatgpt.com/backend-api#fragment' } },
    { ...request, model: { ...request.model, base_url: 'https://chatgpt.com/backend-api/other' } },
  ]) {
    const f = fake();
    assert.deepEqual(await quota(value, { runtimeFactory: f.factory, fetch: f.fetcher }), failure('not_applicable'));
    assert.equal(f.seen.factoryCalls, 0);
    assert.equal(f.seen.authCalls, 0);
    assert.equal(f.seen.fetchCalls, 0);
  }
});

test('requires an exact selected Codex model and upstream before resolving auth', async () => {
  const alternatives = [
    /** @type {FakeOptions} */ ({ missingModel: true }),
    { selectedModel: { ...model, provider: 'openai' } },
    { selectedModel: { ...model, api: 'openai-responses' } },
    { selectedModel: { ...model, baseUrl: 'https://gateway.example/backend-api' } },
    { selectedModel: { ...model, baseUrl: 'https://chatgpt.com/backend-api?gateway=1' } },
  ];
  for (const options of alternatives) {
    const { result, seen } = await runFake(options);
    assert.deepEqual(result, failure('not_applicable'));
    assert.equal(seen.factoryCalls, 1);
    assert.deepEqual(seen.requestedModel, ['openai-codex', 'gpt-5.6-luna']);
    assert.equal(seen.authCalls, 0);
    assert.equal(seen.fetchCalls, 0);
  }
});

test('rejects an auth-owned gateway before HTTP and accepts only normalized upstream auth URLs', async () => {
  const rejected = await runFake({
    auth: authentication('acct-one', { baseUrl: 'https://gateway.example/backend-api' }),
  });
  assert.deepEqual(rejected.result, failure('not_applicable'));
  assert.equal(rejected.seen.authCalls, 1);
  assert.equal(rejected.seen.fetchCalls, 0);

  const accepted = await runFake({
    auth: authentication('acct-one', { baseUrl: 'https://chatgpt.com/backend-api/codex/' }),
  });
  assert.equal(accepted.result.ok, true);
  assert.equal(accepted.seen.fetchCalls, 1);
});

test('maps missing, malformed, badly encoded, mistyped, and oversized JWT auth to auth_unavailable without HTTP', async () => {
  const invalidUtf8 = Buffer.from([0xc3, 0x28]).toString('base64url');
  const encodedHeader = Buffer.from('{}').toString('base64url');
  const encodedSignature = Buffer.from('signature').toString('base64url');
  /** @type {AuthPlan[]} */
  const invalidAuth = [
    undefined,
    /** @type {AuthResult} */ (/** @type {unknown} */ ({ auth: {} })),
    /** @type {AuthResult} */ (/** @type {unknown} */ ({ auth: { apiKey: 1 } })),
    { auth: { apiKey: '' } },
    { auth: { apiKey: 'not-a-jwt' } },
    { auth: { apiKey: `${encodedHeader}.***.${encodedSignature}` } },
    { auth: { apiKey: `${encodedHeader}.${invalidUtf8}.${encodedSignature}` } },
    { auth: { apiKey: `${encodedHeader}.${Buffer.from('{').toString('base64url')}.${encodedSignature}` } },
    { auth: { apiKey: jwtPayload(null) } },
    { auth: { apiKey: jwtPayload({ 'https://api.openai.com/auth': [] }) } },
    { auth: { apiKey: jwtPayload({ 'https://api.openai.com/auth': { chatgpt_account_id: 7 } }) } },
    { auth: { apiKey: jwtPayload({ 'https://api.openai.com/auth': { chatgpt_account_id: '' } }) } },
    { auth: { apiKey: jwtPayload({ 'https://api.openai.com/auth': { chatgpt_account_id: 'acct\nother' } }) } },
    { auth: { apiKey: jwtPayload({ filler: 'x'.repeat(70 * 1024), 'https://api.openai.com/auth': { chatgpt_account_id: 'acct-one' } }) } },
  ];
  for (const auth of invalidAuth) {
    const { result, seen } = await runFake({ auth });
    assert.deepEqual(result, failure('auth_unavailable'));
    assert.equal(seen.fetchCalls, 0);
    assert.equal(JSON.stringify(result).includes('PRIVATE'), false);
  }

  const thrown = await runFake({ auth: new Error('AUTH-TOKEN-SECRET-SENTINEL') });
  assert.deepEqual(thrown.result, failure('auth_unavailable'));
  assert.equal(thrown.seen.fetchCalls, 0);
  assert.equal(JSON.stringify(thrown.result).includes('SECRET-SENTINEL'), false);
});

test('uses public create/getAuth once with one outer signal and fetches only the fixed endpoint and two headers', async () => {
  const auth = authentication('acct-one', {
    apiKey: jwt('acct-one', 'rotated-signature'),
    baseUrl: 'https://chatgpt.com/backend-api/codex',
    headers: {
      Authorization: 'Bearer PRIVATE-WRONG-TOKEN',
      'ChatGPT-Account-Id': 'PRIVATE-WRONG-ACCOUNT',
      'X-Private-Header': 'PRIVATE-HEADER-SENTINEL',
    },
  });
  const { result, seen } = await runFake({
    auth,
    fetch: usageResponse({
      primary_window: {
        used_percent: 80,
        limit_window_seconds: 604800,
        reset_after_seconds: 20,
      },
      secondary_window: {
        used_percent: 25,
        limit_window_seconds: 18000,
        reset_after_seconds: 999,
        reset_at: 1_700_000_500,
      },
    }),
  });

  assert.deepEqual(result, {
    version: 1,
    ok: true,
    provider: 'openai-codex',
    base_url: canonicalBaseUrl,
    account_key: accountOneKey,
    fetched_at: fetchedAt,
    windows: {
      five_hour: { remaining_percent: 75, reset_at: 1_700_000_500_000 },
      weekly: { remaining_percent: 20, reset_at: fetchedAt + 20_000 },
    },
  });
  assert.equal(Buffer.byteLength(`${JSON.stringify(result)}\n`) <= 4096, true);
  assert.equal(seen.factoryCalls, 1);
  assert.equal(seen.getModelCalls, 1);
  assert.equal(seen.authCalls, 1);
  assert.equal(seen.fetchCalls, 1);
  assert.ok(seen.factoryOptions);
  assert.ok(seen.authOptions);
  assert.deepEqual(seen.factoryOptions, { signal: seen.factoryOptions.signal, allowModelNetwork: false });
  assert.deepEqual(seen.authOptions, { signal: seen.factoryOptions.signal });
  assert.equal(seen.authOptions.signal, seen.factoryOptions.signal);
  assert.equal(seen.selectedModel, model);
  assert.equal(String(seen.fetchInput), usageEndpoint);
  assert.equal(seen.fetchInit?.method, 'GET');
  assert.equal(seen.fetchInit?.redirect, 'error');
  assert.equal(seen.fetchInit?.signal instanceof AbortSignal, true);
  const headers = new Headers(seen.fetchInit?.headers);
  assert.deepEqual([...headers.entries()].sort(), [
    ['authorization', `Bearer ${auth.auth.apiKey}`],
    ['chatgpt-account-id', 'acct-one'],
  ]);
  const serialized = JSON.stringify(result);
  for (const secret of ['acct-one', auth.auth.apiKey ?? '', 'PRIVATE', 'plus', 'billing']) {
    assert.equal(serialized.includes(secret), false);
  }
});

test('classifies windows by duration regardless of position and ignores unknown or malformed windows without inventing 100%', async () => {
  /** @type {Array<[unknown, {five_hour: QuotaWindow|null, weekly: QuotaWindow|null}]>} */
  const cases = [
    [{}, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: 5, limit_window_seconds: 3600, reset_at: 1_700_000_100 } }, { five_hour: null, weekly: null }],
    [{ primary_window: { limit_window_seconds: 18000, reset_at: 1_700_000_100 } }, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: -1, limit_window_seconds: 18000, reset_at: 1_700_000_100 } }, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: 101, limit_window_seconds: 18000, reset_at: 1_700_000_100 } }, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: 10, limit_window_seconds: 18000, reset_after_seconds: 3, reset_at: null } }, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: 10, limit_window_seconds: 18000, reset_after_seconds: -1 } }, { five_hour: null, weekly: null }],
    [{ primary_window: { used_percent: 10, limit_window_seconds: 18000, reset_at: Number.MAX_SAFE_INTEGER } }, { five_hour: null, weekly: null }],
    [{ secondary_window: { used_percent: 0, limit_window_seconds: 18000, reset_after_seconds: 0 } }, { five_hour: { remaining_percent: 100, reset_at: fetchedAt }, weekly: null }],
  ];
  for (const [rateLimit, expected] of cases) {
    const { result } = await runFake({ fetch: usageResponse(rateLimit) });
    assert.equal(result.ok, true);
    if (result.ok) assert.deepEqual(result.windows, expected);
  }
});

test('rejects duplicate recognized duration windows', async () => {
  for (const duration of [18000, 604800]) {
    const snapshot = { used_percent: 10, limit_window_seconds: duration, reset_at: 1_700_000_100 };
    const { result } = await runFake({
      fetch: usageResponse({ primary_window: snapshot, secondary_window: { ...snapshot, used_percent: 20 } }),
    });
    assert.deepEqual(result, failure('invalid_response', accountOneKey));
  }
});

test('maps 401/403, 429, redirects, and other HTTP failures without reading or exposing response bodies', async () => {
  /** @type {Array<[number, QuotaError]>} */
  const cases = [
    [401, 'auth_rejected'],
    [403, 'auth_rejected'],
    [429, 'rate_limited'],
    [302, 'fetch_failed'],
    [500, 'fetch_failed'],
  ];
  for (const [status, error] of cases) {
    let cancels = 0;
    const body = new ReadableStream({
      start(controller) {
        controller.enqueue(Buffer.from('PRIVATE-PLAN-BILLING-RESPONSE-SENTINEL'));
      },
      cancel() { cancels += 1; },
    });
    const { result, seen } = await runFake({ fetch: new Response(body, { status }) });
    assert.deepEqual(result, failure(error, accountOneKey));
    assert.equal(seen.fetchCalls, 1);
    assert.equal(cancels, 1);
    assert.equal(JSON.stringify(result).includes('PRIVATE'), false);
  }
});

test('known HTTP failures win over hanging body cleanup and short HTTP or outer deadlines', async () => {
  /** @type {Array<[number, QuotaError]>} */
  const cases = [
    [401, 'auth_rejected'],
    [403, 'auth_rejected'],
    [429, 'rate_limited'],
    [500, 'fetch_failed'],
  ];
  const budgets = [
    { timeoutMs: 100, httpTimeoutMs: 5 },
    { timeoutMs: 5, httpTimeoutMs: 100 },
  ];
  for (const [status, error] of cases) {
    for (const budget of budgets) {
      let cancels = 0;
      const body = new ReadableStream({
        cancel() {
          cancels += 1;
          return new Promise(() => {});
        },
      });
      const started = Date.now();
      const { result, seen } = await runFake({ fetch: new Response(body, { status }) }, budget);
      assert.ok(Date.now() - started < 100);
      assert.deepEqual(result, failure(error, accountOneKey));
      assert.equal(seen.fetchCalls, 1);
      assert.equal(cancels, 1);
    }
  }
});

test('maps transport and body-read failures to fetch_failed with known identity and no diagnostics', async () => {
  const transport = await runFake({ fetch: new Error('TOKEN-RESPONSE-SECRET-SENTINEL') });
  assert.deepEqual(transport.result, failure('fetch_failed', accountOneKey));
  assert.equal(JSON.stringify(transport.result).includes('SECRET-SENTINEL'), false);

  const brokenBody = new ReadableStream({
    pull(controller) {
      controller.error(new Error('BODY-SECRET-SENTINEL'));
    },
  });
  const body = await runFake({ fetch: new Response(brokenBody, { status: 200 }) });
  assert.deepEqual(body.result, failure('fetch_failed', accountOneKey));
  assert.equal(JSON.stringify(body.result).includes('SECRET-SENTINEL'), false);
});

test('accepts a valid JSON body of exactly 64 KiB and rejects one extra byte', async () => {
  const encoded = Buffer.from(JSON.stringify({ rate_limit: {} }));
  const exact = Buffer.concat([encoded, Buffer.alloc(65536 - encoded.byteLength, 0x20)]);
  const oversized = Buffer.concat([exact, Buffer.from(' ')]);
  assert.equal(exact.byteLength, 65536);
  assert.equal(oversized.byteLength, 65537);

  const accepted = await runFake({ fetch: new Response(exact, { status: 200 }) });
  assert.deepEqual(accepted.result, {
    version: 1,
    ok: true,
    provider: 'openai-codex',
    base_url: canonicalBaseUrl,
    account_key: accountOneKey,
    fetched_at: fetchedAt,
    windows: { five_hour: null, weekly: null },
  });
  const rejected = await runFake({ fetch: new Response(oversized, { status: 200 }) });
  assert.deepEqual(rejected.result, failure('invalid_response', accountOneKey));
});

test('rejects missing rate_limit, malformed JSON, and fatal UTF-8 as invalid_response', async () => {
  const malformedResponses = [
    new Response('{}', { status: 200 }),
    new Response('{', { status: 200 }),
    new Response(JSON.stringify({ rate_limit: null }), { status: 200 }),
    new Response(JSON.stringify({ rate_limit: [] }), { status: 200 }),
    new Response(Buffer.from([0x7b, 0xc3, 0x28, 0x7d]), { status: 200 }),
  ];
  for (const response of malformedResponses) {
    const { result } = await runFake({ fetch: response });
    assert.deepEqual(result, failure('invalid_response', accountOneKey));
  }
});

test('rotated bearer tokens keep one account key while a changed account changes failures too', async () => {
  const first = await runFake({ auth: { auth: { apiKey: jwt('acct-one', 'first-token') } } });
  const rotated = await runFake({ auth: { auth: { apiKey: jwt('acct-one', 'second-token') } } });
  const changed = await runFake({
    auth: { auth: { apiKey: jwt('acct-two', 'third-token') } },
    fetch: new Error('TRANSIENT-SECRET-SENTINEL'),
  });
  assert.equal(first.result.ok, true);
  assert.equal(rotated.result.ok, true);
  if (first.result.ok && rotated.result.ok) {
    assert.equal(first.result.account_key, accountOneKey);
    assert.equal(rotated.result.account_key, accountOneKey);
  }
  assert.deepEqual(changed.result, failure('fetch_failed', accountTwoKey));
  assert.notEqual(accountOneKey, accountTwoKey);
  const output = JSON.stringify([first.result, rotated.result, changed.result]);
  for (const raw of ['acct-one', 'acct-two', 'first-token', 'second-token', 'third-token', 'TRANSIENT']) {
    assert.equal(output.includes(raw), false);
  }
});

test('the five-second HTTP deadline aborts and races an ignored fetch', async () => {
  /** @type {AbortSignal|undefined} */
  let fetchSignal;
  const never = new Promise(/** @param {(value: Response) => void} _resolve */ (_resolve) => {});
  const f = fake({
    fetch: (_input, init) => {
      fetchSignal = init?.signal ?? undefined;
      return never;
    },
  });
  const started = Date.now();
  const result = await quota(request, {
    runtimeFactory: f.factory,
    fetch: f.fetcher,
    timeoutMs: 100,
    httpTimeoutMs: 5,
    now: () => fetchedAt,
  });
  assert.ok(Date.now() - started < 500);
  assert.deepEqual(result, failure('timeout', accountOneKey));
  assert.equal(f.seen.fetchCalls, 1);
  assert.equal(fetchSignal?.aborted, true);
});

test('the HTTP deadline covers a stalled body and performs bounded cancellation', async () => {
  let cancels = 0;
  const stalled = new ReadableStream({
    pull() {},
    cancel() { cancels += 1; },
  });
  const started = Date.now();
  const { result, seen } = await runFake(
    { fetch: new Response(stalled, { status: 200 }) },
    { timeoutMs: 100, httpTimeoutMs: 5 },
  );
  assert.ok(Date.now() - started < 500);
  assert.deepEqual(result, failure('timeout', accountOneKey));
  assert.equal(seen.fetchCalls, 1);
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(cancels, 1);
});

test('an uncooperative late fetch response is canceled after the timeout result', async () => {
  let cancels = 0;
  const body = new ReadableStream({
    pull() {},
    cancel() { cancels += 1; },
  });
  const { result } = await runFake({
    fetch: async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
      return new Response(body, { status: 200 });
    },
  }, { timeoutMs: 100, httpTimeoutMs: 5 });
  assert.deepEqual(result, failure('timeout', accountOneKey));
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(cancels, 1);
});

test('the outer deadline races uncooperative runtime work and guards against late model/auth/HTTP calls', async () => {
  /** @type {RuntimeCreateOptions|undefined} */
  let neverOptions;
  /** @type {RuntimeFactory} */
  const neverFactory = async (options) => {
    neverOptions = options;
    return new Promise(/** @param {(value: RuntimePort) => void} _resolve */ (_resolve) => {});
  };
  const started = Date.now();
  const neverResult = await quota(request, { runtimeFactory: neverFactory, timeoutMs: 5 });
  assert.ok(Date.now() - started < 500);
  assert.deepEqual(neverResult, failure('timeout'));
  assert.equal(neverOptions?.signal.aborted, true);

  let getModelCalls = 0;
  let authCalls = 0;
  let fetchCalls = 0;
  /** @type {RuntimeFactory} */
  const lateFactory = async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
    return {
      getModel() { getModelCalls += 1; return model; },
      async getAuth() { authCalls += 1; return authentication('acct-one'); },
    };
  };
  assert.deepEqual(await quota(request, {
    runtimeFactory: lateFactory,
    fetch: async () => { fetchCalls += 1; return usageResponse({}); },
    timeoutMs: 5,
  }), failure('timeout'));
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(getModelCalls, 0);
  assert.equal(authCalls, 0);
  assert.equal(fetchCalls, 0);
});

test('late auth cannot start HTTP, while an outer network timeout retains known identity', async () => {
  let resolveAuth = /** @type {(value: AuthResult|undefined) => void} */ (() => {});
  const delayedAuth = new Promise((resolve) => { resolveAuth = resolve; });
  const late = fake({ auth: delayedAuth });
  const lateResult = await quota(request, {
    runtimeFactory: late.factory,
    fetch: late.fetcher,
    timeoutMs: 5,
    httpTimeoutMs: 100,
  });
  assert.deepEqual(lateResult, failure('timeout'));
  assert.equal(late.seen.authOptions?.signal, late.seen.factoryOptions?.signal);
  assert.equal(late.seen.authOptions?.signal.aborted, true);
  resolveAuth(authentication('acct-one'));
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(late.seen.fetchCalls, 0);

  const neverFetch = fake({
    fetch: new Promise(/** @param {(value: Response) => void} _resolve */ (_resolve) => {}),
  });
  const networkResult = await quota(request, {
    runtimeFactory: neverFetch.factory,
    fetch: neverFetch.fetcher,
    timeoutMs: 5,
    httpTimeoutMs: 100,
  });
  assert.deepEqual(networkResult, failure('timeout', accountOneKey));
  assert.equal(neverFetch.seen.fetchInit?.signal?.aborted, true);
});

test('protocol documentation specifies the exact stateless quota contract, budgets, errors, and account key', async () => {
  const documentation = await readFile(new URL('../docs/pi-quota.md', import.meta.url), 'utf8');
  assert.match(documentation, /stateless/i);
  assert.match(documentation, /64 KiB.*one UTF-8 JSON value/s);
  assert.match(documentation, /https:\/\/chatgpt\.com\/backend-api\/wham\/usage/);
  assert.match(documentation, /15-second.*5-second/s);
  assert.match(documentation, /SHA-256.*JSON\.stringify\(\[provider, canonicalBaseURL, accountID\]\)/s);
  for (const error of ['invalid_request', 'not_applicable', 'auth_unavailable', 'auth_rejected', 'rate_limited', 'fetch_failed', 'invalid_response', 'timeout']) {
    assert.match(documentation, new RegExp(`\\b${error}\\b`));
  }
  assert.match(documentation, /no cache|does not cache/i);
  assert.match(documentation, /token.*headers.*environment.*source.*plan.*billing.*response bod/si);
});
