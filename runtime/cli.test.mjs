import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { runCli, runCliEntrypoint } from './cli.mjs';
import { compose } from './compose.mjs';

/** @typedef {'minimal'|'low'|'medium'|'high'|'xhigh'|'max'} ThinkingLevel */
/** @typedef {{version: 1, operation: 'compose', model: {provider: string, id: string, thinking: ThinkingLevel}, input: {user: string, assistant: string}, label: {current: string, refresh: boolean}}} Request */
/** @typedef {import('./quota.mjs').QuotaRequest} QuotaRequest */
/** @typedef {{version: 1, ok: true, body: string, label?: string}|{version: 1, ok: false, error: 'invalid_request'|'unavailable_model'|'generation_failed'|'timeout'|'invalid_output'}} Result */
/** @typedef {import('./quota.mjs').QuotaResult} QuotaResult */
/** @typedef {{[Symbol.asyncIterator](): AsyncIterator<Uint8Array>, destroy?: () => void}} InputStream */
/** @typedef {{code: number|null, signal: NodeJS.Signals|null, stdout: string, stderr: string}} ChildOutcome */
/** @typedef {import('@earendil-works/pi-ai').Model<import('@earendil-works/pi-ai').Api>} PiModel */
/** @typedef {Pick<import('@earendil-works/pi-ai').AssistantMessage, 'stopReason'|'content'>} CompletionResponse */
/** @typedef {import('@earendil-works/pi-ai').Context} CompletionContext */
/** @typedef {{signal: AbortSignal, maxTokens: 256, maxRetries: 0, timeoutMs: 15000, reasoning: ThinkingLevel}} CompletionOptions */
/** @typedef {{getModel(provider: string, id: string): PiModel|undefined, completeSimple(model: PiModel, context: CompletionContext, options: CompletionOptions): Promise<CompletionResponse>}} RuntimePort */
/** @typedef {(options: {signal: AbortSignal, allowModelNetwork: false}) => Promise<RuntimePort>} RuntimeFactory */

/** @type {Request} */
const requestValue = {
  version: 1,
  operation: 'compose',
  model: { provider: 'openai-codex', id: 'gpt-5.6-luna', thinking: 'low' },
  input: { user: '', assistant: '' },
  label: { current: '', refresh: false },
};
const request = JSON.stringify(requestValue);
/** @type {QuotaRequest} */
const quotaRequestValue = {
  version: 1,
  operation: 'quota',
  model: { provider: 'openai-codex', id: 'gpt-5.6-luna', base_url: 'https://chatgpt.com/backend-api' },
};
const quotaRequest = JSON.stringify(quotaRequestValue);
/** @type {QuotaResult} */
const quotaSuccess = {
  version: 1,
  ok: true,
  provider: 'openai-codex',
  base_url: 'https://chatgpt.com/backend-api',
  account_key: '30a7b46a4904936da76ec3f2a807d1fd8014b278ae2ea426d45443dee6853258',
  fetched_at: 1_700_000_000_000,
  windows: { five_hour: null, weekly: null },
};
const entryUrl = new URL('./cli.mjs', import.meta.url).href;
/** @type {PiModel} */
const childModel = {
  id: 'gpt-5.6-luna',
  name: 'GPT-5.6 Luna',
  api: 'openai-codex-responses',
  provider: 'openai-codex',
  baseUrl: 'https://chatgpt.com/backend-api/codex',
  reasoning: true,
  input: ['text'],
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  contextWindow: 272000,
  maxTokens: 128000,
};

/** @param {Uint8Array[]} chunks @returns {InputStream} */
function streams(chunks) {
  return {
    async *[Symbol.asyncIterator]() {
      for (const chunk of chunks) yield chunk;
    },
  };
}

/** @param {Uint8Array} chunk @param {number} delayMs @returns {InputStream} */
function delayedStream(chunk, delayMs) {
  return {
    async *[Symbol.asyncIterator]() {
      await new Promise((resolve) => setTimeout(resolve, delayMs));
      yield chunk;
    },
  };
}

/** @param {'open-stdin'|'oversized-stdin'|'ref-provider'|'ref-quota'} mode @returns {import('node:child_process').ChildProcessWithoutNullStreams} */
function spawnFixture(mode) {
  const child = spawn(process.execPath, [fileURLToPath(import.meta.url), mode], {
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  child.stdin.on('error', () => {});
  return child;
}

/** @param {import('node:child_process').ChildProcessWithoutNullStreams} child @param {number} [timeoutMs] @returns {Promise<ChildOutcome>} */
function collectChild(child, timeoutMs = 2000) {
  return new Promise((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`child did not exit within ${timeoutMs}ms`));
    }, timeoutMs);
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal, stdout, stderr });
    });
  });
}

const fixtureMode = process.argv[2];
if (fixtureMode === 'open-stdin') {
  await runCliEntrypoint({ timeoutMs: 30 });
} else if (fixtureMode === 'oversized-stdin') {
  await runCliEntrypoint({ timeoutMs: 250 });
} else if (fixtureMode === 'ref-provider') {
  /** @type {RuntimeFactory} */
  const runtimeFactory = async () => ({
    getModel: () => childModel,
    async completeSimple() {
      setInterval(() => {}, 1000);
      return { stopReason: 'stop', content: [{ type: 'text', text: '{"body":"Provider completed"}' }] };
    },
  });
  await runCliEntrypoint({
    timeoutMs: 250,
    compose: (value, options) => compose(value, { ...options, runtimeFactory }),
  });
} else if (fixtureMode === 'ref-quota') {
  await runCliEntrypoint({
    timeoutMs: 250,
    quota: async () => {
      setInterval(() => {}, 1000);
      return quotaSuccess;
    },
  });
} else {

test('CLI emits exactly one safe failure for invalid, multiple, oversized, and malformed UTF-8 input', async () => {
  const inputs = [
    Buffer.from('{}'),
    Buffer.from(`${request}\n${request}`),
    Buffer.alloc(65537),
    Buffer.from([0xc3, 0x28]),
  ];
  for (const input of inputs) {
    let output = '';
    const code = await runCli({ stdin: streams([input]), write: (line) => { output += line; } });
    assert.equal(code, 1);
    assert.equal(output, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  }
});

test('CLI maps a secret-bearing input stream rejection and bounded rejecting cleanup to one safe failure', async () => {
  let returns = 0;
  let destroyed = 0;
  /** @type {InputStream} */
  const broken = {
    destroy() { destroyed += 1; },
    [Symbol.asyncIterator]() {
      return {
        async next() {
          throw new Error('STREAM-SECRET-SENTINEL');
        },
        async return() {
          returns += 1;
          throw new Error('RETURN-SECRET-SENTINEL');
        },
      };
    },
  };
  let output = '';
  const code = await runCli({ stdin: broken, timeoutMs: 100, write: (line) => { output += line; } });
  assert.equal(code, 1);
  assert.equal(output, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  assert.equal(output.includes('SECRET-SENTINEL'), false);
  assert.equal(returns, 1);
  assert.equal(destroyed, 1);
});

test('CLI maps a secret-bearing iterator construction failure to one safe failure', async () => {
  /** @type {InputStream} */
  const broken = {
    [Symbol.asyncIterator]() {
      throw new Error('ITERATOR-SECRET-SENTINEL');
    },
  };
  let output = '';
  const code = await runCli({ stdin: broken, timeoutMs: 100, write: (line) => { output += line; } });
  assert.equal(code, 1);
  assert.equal(output, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  assert.equal(output.includes('SECRET-SENTINEL'), false);
});

test('CLI bounds cleanup when an oversized unfinished iterator return never settles', async () => {
  let returns = 0;
  let destroyed = 0;
  /** @type {InputStream} */
  const oversized = {
    destroy() { destroyed += 1; },
    [Symbol.asyncIterator]() {
      return {
        async next() {
          return { done: false, value: Buffer.alloc(65537) };
        },
        async return() {
          returns += 1;
          return new Promise(() => {});
        },
      };
    },
  };
  let output = '';
  const started = Date.now();
  const code = await runCli({ stdin: oversized, timeoutMs: 100, write: (line) => { output += line; } });
  assert.ok(Date.now() - started < 500);
  assert.equal(code, 1);
  assert.equal(output, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  assert.equal(returns, 1);
  assert.equal(destroyed, 1);
});

test('CLI accepts an exact 64 KiB JSON input but rejects one extra byte', async () => {
  const encoded = Buffer.from(request);
  const exact = Buffer.concat([encoded, Buffer.alloc(65536 - encoded.byteLength, 0x20)]);
  const oversized = Buffer.concat([exact, Buffer.from(' ')]);
  let successOutput = '';
  const successCode = await runCli({
    stdin: streams([exact]),
    write: (line) => { successOutput += line; },
    compose: async () => ({ version: 1, ok: true, body: 'Boundary accepted' }),
  });
  assert.equal(successCode, 0);
  assert.equal(successOutput, '{"version":1,"ok":true,"body":"Boundary accepted"}\n');

  let failureOutput = '';
  const failureCode = await runCli({
    stdin: streams([oversized]),
    write: (line) => { failureOutput += line; },
    compose: async () => {
      assert.fail('oversized input must not reach composition');
    },
  });
  assert.equal(failureCode, 1);
  assert.equal(failureOutput, '{"version":1,"ok":false,"error":"invalid_request"}\n');
});

test('CLI sends valid input through composition with only the positive remaining total budget', async () => {
  let output = '';
  /** @type {unknown} */
  let received;
  /** @type {number|undefined} */
  let receivedTimeout;
  const totalBudget = 100;
  const code = await runCli({
    stdin: delayedStream(Buffer.from(request), 20),
    timeoutMs: totalBudget,
    write: (line) => { output += line; },
    compose: async (value, options) => {
      received = value;
      assert.ok(options);
      receivedTimeout = options.timeoutMs;
      return { version: 1, ok: true, body: 'Composition completed' };
    },
  });
  assert.equal(code, 0);
  assert.deepEqual(received, requestValue);
  assert.ok(receivedTimeout !== undefined && receivedTimeout > 0);
  assert.ok(receivedTimeout < totalBudget);
  assert.equal(output, '{"version":1,"ok":true,"body":"Composition completed"}\n');
});

test('CLI does not start composition or quota auth for invalid or unknown operations', async () => {
  let composeCalls = 0;
  let quotaCalls = 0;
  const inputs = [
    {},
    { ...quotaRequestValue, extra: 'PRIVATE-REQUEST-SENTINEL' },
    { ...quotaRequestValue, operation: 'unknown' },
  ];
  for (const input of inputs) {
    let output = '';
    const code = await runCli({
      stdin: streams([Buffer.from(JSON.stringify(input))]),
      write: (line) => { output += line; },
      compose: async () => {
        composeCalls += 1;
        return { version: 1, ok: false, error: 'generation_failed' };
      },
      quota: async () => {
        quotaCalls += 1;
        return { version: 1, ok: false, error: 'auth_unavailable' };
      },
    });
    assert.equal(code, 1);
    assert.equal(output, '{"version":1,"ok":false,"error":"invalid_request"}\n');
    assert.equal(output.includes('PRIVATE-REQUEST-SENTINEL'), false);
  }
  assert.equal(composeCalls, 0);
  assert.equal(quotaCalls, 0);
});

test('CLI delegates exact quota validation and passes only the positive remaining total budget', async () => {
  let output = '';
  let calls = 0;
  /** @type {unknown} */
  let received;
  /** @type {number|undefined} */
  let receivedTimeout;
  const code = await runCli({
    stdin: delayedStream(Buffer.from(quotaRequest), 20),
    timeoutMs: 100,
    write: (line) => { output += line; },
    compose: async () => {
      assert.fail('quota input must not reach composition');
    },
    quota: async (value, options) => {
      calls += 1;
      received = value;
      assert.ok(options);
      receivedTimeout = options.timeoutMs;
      return quotaSuccess;
    },
  });
  assert.equal(code, 0);
  assert.equal(calls, 1);
  assert.deepEqual(received, quotaRequestValue);
  assert.ok(receivedTimeout !== undefined && receivedTimeout > 0 && receivedTimeout < 100);
  assert.equal(output, `${JSON.stringify(quotaSuccess)}\n`);
});

test('CLI maps a secret-bearing quota dependency failure to one safe fetch_failed result', async () => {
  let output = '';
  const code = await runCli({
    stdin: streams([Buffer.from(quotaRequest)]),
    write: (line) => { output += line; },
    quota: async () => { throw new Error('QUOTA-AUTH-SECRET-SENTINEL'); },
  });
  assert.equal(code, 1);
  assert.equal(output, '{"version":1,"ok":false,"error":"fetch_failed"}\n');
  assert.equal(output.includes('SECRET-SENTINEL'), false);
});

test('CLI maps stalled stdin to timeout within its total request budget', async () => {
  let output = '';
  /** @type {InputStream} */
  const stalled = {
    async *[Symbol.asyncIterator]() {
      await new Promise(() => {});
    },
  };
  const started = Date.now();
  const code = await runCli({ stdin: stalled, timeoutMs: 5, write: (line) => { output += line; } });
  assert.ok(Date.now() - started < 500);
  assert.equal(code, 1);
  assert.equal(output, '{"version":1,"ok":false,"error":"timeout"}\n');
});

test('entrypoint flushes one timeout line and exits with a real stdin pipe left open', async () => {
  const child = spawnFixture('open-stdin');
  const outcome = await collectChild(child);
  assert.equal(outcome.code, 1);
  assert.equal(outcome.signal, null);
  assert.equal(outcome.stdout, '{"version":1,"ok":false,"error":"timeout"}\n');
  assert.equal(outcome.stderr, '');
});

test('entrypoint closes an oversized real stdin pipe after one safe failure', async () => {
  const child = spawnFixture('oversized-stdin');
  child.stdin.write(Buffer.alloc(65537));
  const outcome = await collectChild(child);
  assert.equal(outcome.code, 1);
  assert.equal(outcome.signal, null);
  assert.equal(outcome.stdout, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  assert.equal(outcome.stderr, '');
});

test('entrypoint flushes success and exits despite a fake provider ref-ed handle', async () => {
  const child = spawnFixture('ref-provider');
  child.stdin.end(request);
  const outcome = await collectChild(child);
  assert.equal(outcome.code, 0);
  assert.equal(outcome.signal, null);
  assert.equal(outcome.stdout, '{"version":1,"ok":true,"body":"Provider completed"}\n');
  assert.equal(outcome.stderr, '');
});

test('entrypoint flushes one quota line with empty stderr and exits despite a local fake ref-ed handle', async () => {
  const child = spawnFixture('ref-quota');
  child.stdin.end(quotaRequest);
  const outcome = await collectChild(child);
  assert.equal(outcome.code, 0);
  assert.equal(outcome.signal, null);
  assert.equal(outcome.stdout, `${JSON.stringify(quotaSuccess)}\n`);
  assert.equal(outcome.stderr, '');
});

test('protocol documentation states timeout, final-answer, and flushed-exit behavior', async () => {
  const documentation = await readFile(new URL('../docs/pi-helper.md', import.meta.url), 'utf8');
  const resultsSection = documentation.split('## Results\n')[1]?.split('## Packaging workaround\n')[0];
  assert.ok(resultsSection);
  assert.match(documentation, /stalled stdin.*`timeout`/s);
  assert.match(documentation, /`final_answer`.*commentary/s);
  assert.match(resultsSection, /stdout write callback.*flushed.*exits/s);
});

test('production entrypoint keeps invalid request text secret-safe', async () => {
  const child = spawn(process.execPath, [fileURLToPath(entryUrl)], { stdio: ['pipe', 'pipe', 'pipe'] });
  child.stdin.on('error', () => {});
  child.stdin.end('{"secret":"SECRET-SENTINEL"}');
  const outcome = await collectChild(child);
  assert.equal(outcome.code, 1);
  assert.equal(outcome.signal, null);
  assert.equal(outcome.stdout, '{"version":1,"ok":false,"error":"invalid_request"}\n');
  assert.equal(`${outcome.stdout}${outcome.stderr}`.includes('SECRET-SENTINEL'), false);
});

}
