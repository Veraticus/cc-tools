import { pathToFileURL } from 'node:url';
import { compose, validateRequest } from './compose.mjs';

const maximumInputBytes = 64 * 1024;
const totalBudgetMs = 15000;
const cleanupBudgetMs = 25;

/** @typedef {import('./compose.mjs').Request} Request */
/** @typedef {import('./compose.mjs').Result} Result */
/** @typedef {{[Symbol.asyncIterator](): AsyncIterator<Uint8Array>, destroy?: () => void}} InputStream */
/** @typedef {{stdin?: InputStream, write?: (line: string) => void|Promise<void>, compose?: typeof compose, timeoutMs?: number}} CliDependencies */
/** @typedef {{kind: 'request', request: Request}|{kind: 'invalid'}|{kind: 'timeout'}} ReadResult */
/** @typedef {{kind: 'item', item: IteratorResult<Uint8Array>}|{kind: 'error'}} NextResult */

/** @returns {Result} */
function invalidRequest() {
  return { version: 1, ok: false, error: 'invalid_request' };
}

/** @returns {Result} */
function generationFailed() {
  return { version: 1, ok: false, error: 'generation_failed' };
}

/** @returns {Result} */
function timedOut() {
  return { version: 1, ok: false, error: 'timeout' };
}

/** @param {InputStream} stdin @param {AsyncIterator<Uint8Array>} iterator */
async function cleanupInput(stdin, iterator) {
  try {
    stdin.destroy?.();
  } catch {
    // Input cleanup failures are mapped to the already-selected safe result.
  }

  const close = iterator.return;
  if (close === undefined) return;
  await new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(undefined);
    };
    const timer = setTimeout(finish, cleanupBudgetMs);
    Promise.resolve()
      .then(() => close.call(iterator))
      .then(finish, finish);
  });
}

/** @param {InputStream} stdin @param {number} timeoutMs @returns {Promise<ReadResult>} */
async function readRequest(stdin, timeoutMs) {
  const decoder = new TextDecoder('utf-8', { fatal: true });
  const iterator = stdin[Symbol.asyncIterator]();
  /** @type {Uint8Array[]} */
  const chunks = [];
  let size = 0;
  let finished = false;
  /** @type {() => void} */
  let expire = () => {};
  /** @type {Promise<{kind: 'timeout'}>} */
  const deadline = new Promise((resolve) => {
    expire = () => resolve({ kind: 'timeout' });
  });
  const timer = setTimeout(expire, Math.max(0, timeoutMs));

  try {
    while (!finished) {
      /** @type {Promise<NextResult>} */
      const next = Promise.resolve()
        .then(() => iterator.next())
        .then(
          (item) => ({ kind: 'item', item }),
          () => ({ kind: 'error' }),
        );
      const outcome = await Promise.race([next, deadline]);
      if (outcome.kind === 'timeout') return { kind: 'timeout' };
      if (outcome.kind === 'error') return { kind: 'invalid' };
      if (outcome.item.done) {
        finished = true;
        break;
      }
      size += outcome.item.value.byteLength;
      if (size > maximumInputBytes) return { kind: 'invalid' };
      chunks.push(outcome.item.value);
    }

    let raw;
    try {
      raw = chunks.map((chunk) => decoder.decode(chunk, { stream: true })).join('') + decoder.decode();
    } catch {
      return { kind: 'invalid' };
    }
    if (!raw) return { kind: 'invalid' };
    try {
      const request = validateRequest(JSON.parse(raw));
      return request ? { kind: 'request', request } : { kind: 'invalid' };
    } catch {
      return { kind: 'invalid' };
    }
  } finally {
    clearTimeout(timer);
    if (!finished) await cleanupInput(stdin, iterator);
  }
}

/** @param {string} line @returns {Promise<void>} */
function writeStdout(line) {
  return new Promise((resolve, reject) => {
    process.stdout.write(line, (error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

/** @param {CliDependencies} [dependencies] */
export async function runCli(dependencies = {}) {
  const started = Date.now();
  const stdin = dependencies.stdin ?? process.stdin;
  const write = dependencies.write ?? writeStdout;
  const budget = dependencies.timeoutMs ?? totalBudgetMs;
  /** @type {ReadResult} */
  let input;
  try {
    input = await readRequest(stdin, budget);
  } catch {
    input = { kind: 'invalid' };
  }
  /** @type {Result} */
  let result = invalidRequest();

  if (input.kind === 'timeout') {
    result = timedOut();
  } else if (input.kind === 'request') {
    const remaining = budget - (Date.now() - started);
    if (remaining <= 0) {
      result = timedOut();
    } else {
      try {
        result = await (dependencies.compose ?? compose)(input.request, { timeoutMs: remaining });
      } catch {
        result = generationFailed();
      }
    }
  }

  await write(`${JSON.stringify(result)}\n`);
  return result.ok ? 0 : 1;
}

/**
 * Production process boundary. Unlike runCli(), this explicitly exits after the
 * stdout write callback has confirmed that the one result line was flushed.
 * @param {CliDependencies} [dependencies]
 * @returns {Promise<never>}
 */
export async function runCliEntrypoint(dependencies = {}) {
  let code = 1;
  try {
    code = await runCli(dependencies);
  } catch {
    // runCli maps request/provider failures; an output failure cannot be retried
    // without risking a second line, so terminate nonzero without disclosure.
  }
  process.exit(code);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await runCliEntrypoint();
}
