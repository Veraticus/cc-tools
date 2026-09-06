import { spawn as nodeSpawn } from "node:child_process";
import process from "node:process";

const MAXIMUM_OUTPUT_BYTES = 4096;
const DEFAULT_TIMEOUT_MS = 15_000;
const ACCOUNT_KEY = /^[a-f0-9]{64}$/u;
const ERRORS = new Set([
  "invalid_request",
  "not_applicable",
  "auth_unavailable",
  "auth_rejected",
  "rate_limited",
  "fetch_failed",
  "invalid_response",
  "timeout",
]);
const TRANSPORT_FAILURE = Object.freeze({ version: /** @type {const} */ (1), ok: /** @type {const} */ (false), error: /** @type {const} */ ("fetch_failed") });
const TIMEOUT_FAILURE = Object.freeze({ version: /** @type {const} */ (1), ok: /** @type {const} */ (false), error: /** @type {const} */ ("timeout") });

/** @typedef {{remaining_percent: number, reset_at: number}} PiQuotaWindow */
/** @typedef {{version: 1, ok: true, provider: "openai-codex", base_url: "https://chatgpt.com/backend-api", account_key: string, fetched_at: number, windows: {five_hour: PiQuotaWindow|null, weekly: PiQuotaWindow|null}}} PiQuotaSuccess */
/** @typedef {{version: 1, ok: false, error: "invalid_request"|"not_applicable"|"auth_unavailable"|"auth_rejected"|"rate_limited"|"fetch_failed"|"invalid_response"|"timeout", provider?: "openai-codex", base_url?: "https://chatgpt.com/backend-api", account_key?: string}} PiQuotaFailure */
/** @typedef {PiQuotaSuccess|PiQuotaFailure} PiQuotaResult */
/** @typedef {{provider: string, id: string, baseUrl: string}} PiQuotaHelperModel */
/** @typedef {{end(wire: string, callback: (error?: Error|null) => void): void, destroy(): void, once(event: "error", listener: (error: Error) => void): unknown, removeListener(event: "error", listener: (error: Error) => void): unknown, unref?(): void}} PiQuotaInput */
/** @typedef {{destroy(): void, on(event: "data", listener: (chunk: Buffer|string) => void): unknown, once(event: "error", listener: (error: Error) => void): unknown, removeListener(event: "data", listener: (chunk: Buffer|string) => void): unknown, removeListener(event: "error", listener: (error: Error) => void): unknown, unref?(): void}} PiQuotaOutput */
/** @typedef {{stdin: PiQuotaInput|null, stdout: PiQuotaOutput|null, exitCode: number|null, signalCode: NodeJS.Signals|null, once(event: "error", listener: (error: Error) => void): unknown, once(event: "close", listener: (code: number|null) => void): unknown, removeListener(event: "error", listener: (error: Error) => void): unknown, removeListener(event: "close", listener: (code: number|null) => void): unknown, kill(signal?: NodeJS.Signals|number): boolean, unref(): void}} PiQuotaChild */
/** @typedef {(command: string, args: string[], options: {shell: false, stdio: ["pipe", "pipe", "ignore"], windowsHide: true}) => PiQuotaChild} PiQuotaSpawn */
/** @typedef {{spawn?: PiQuotaSpawn, signal?: AbortSignal, timeoutMs?: number, environment?: Record<string, string|undefined>}} PiQuotaHelperOptions */

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function record(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {Record<string, unknown>} value @param {string[]} keys */
function exact(value, keys) {
  const present = Object.keys(value);
  return present.length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

/** @param {unknown} value @returns {value is PiQuotaWindow|null} */
function window(value) {
  if (value === null) return true;
  if (!record(value) || !exact(value, ["remaining_percent", "reset_at"])) return false;
  return typeof value.remaining_percent === "number" && Number.isFinite(value.remaining_percent) &&
    value.remaining_percent >= 0 && value.remaining_percent <= 100 &&
    typeof value.reset_at === "number" && Number.isSafeInteger(value.reset_at) && value.reset_at >= 0;
}

/** @param {unknown} value @returns {PiQuotaResult|undefined} */
function parseResult(value) {
  if (!record(value) || value.version !== 1 || typeof value.ok !== "boolean") return undefined;
  if (value.ok) {
    if (!exact(value, ["version", "ok", "provider", "base_url", "account_key", "fetched_at", "windows"]) ||
        value.provider !== "openai-codex" || value.base_url !== "https://chatgpt.com/backend-api" ||
        typeof value.account_key !== "string" || !ACCOUNT_KEY.test(value.account_key) ||
        typeof value.fetched_at !== "number" || !Number.isSafeInteger(value.fetched_at) || value.fetched_at <= 0 ||
        !record(value.windows) || !exact(value.windows, ["five_hour", "weekly"]) ||
        !window(value.windows.five_hour) || !window(value.windows.weekly)) return undefined;
    return /** @type {PiQuotaSuccess} */ (value);
  }

  if (typeof value.error !== "string" || !ERRORS.has(value.error)) return undefined;
  if (exact(value, ["version", "ok", "error"])) return /** @type {PiQuotaFailure} */ (value);
  if (!exact(value, ["version", "ok", "error", "provider", "base_url", "account_key"]) ||
      value.provider !== "openai-codex" || value.base_url !== "https://chatgpt.com/backend-api" ||
      typeof value.account_key !== "string" || !ACCOUNT_KEY.test(value.account_key)) return undefined;
  return /** @type {PiQuotaFailure} */ (value);
}

/** @param {unknown} value @returns {value is {unref(): void}} */
function canUnref(value) {
  return record(value) && typeof value.unref === "function";
}

/** @type {PiQuotaSpawn} */
const spawnHelper = (command, args, options) => nodeSpawn(command, args, options);

/**
 * Run the credential-owning quota helper over one bounded stdin/stdout exchange.
 * @param {PiQuotaHelperModel} model
 * @param {PiQuotaHelperOptions} [options]
 * @returns {Promise<PiQuotaResult>}
 */
export function runPiQuotaHelper(model, options = {}) {
  if (options.signal?.aborted) return Promise.resolve(TIMEOUT_FAILURE);
  const environment = options.environment ?? process.env;
  const command = environment.STEWARD_HELPER_BIN === undefined
    ? "steward-pi-helper"
    : environment.STEWARD_HELPER_BIN;
  const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const wire = `${JSON.stringify({
    version: 1,
    operation: "quota",
    model: { provider: model.provider, id: model.id, base_url: model.baseUrl },
  })}\n`;

  /** @type {PiQuotaChild} */
  let child;
  try {
    child = (options.spawn ?? spawnHelper)(command, [], {
      shell: false,
      stdio: ["pipe", "pipe", "ignore"],
      windowsHide: true,
    });
  } catch {
    return Promise.resolve(TRANSPORT_FAILURE);
  }

  return new Promise((resolve) => {
    let done = false;
    let inputComplete = false;
    /** @type {number|null|undefined} */
    let closeCode;
    /** @type {Buffer[]} */
    const chunks = [];
    let outputBytes = 0;
    const input = child.stdin;
    const output = child.stdout;

    /** @param {Error} _error */
    const onError = (_error) => finish(TRANSPORT_FAILURE, true);
    /** @param {Buffer|string} chunk */
    const onData = (chunk) => {
      if (done) return;
      const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      if (outputBytes + bytes.byteLength > MAXIMUM_OUTPUT_BYTES) {
        finish(TRANSPORT_FAILURE, true);
        return;
      }
      chunks.push(bytes);
      outputBytes += bytes.byteLength;
    };
    /** @param {number|null} code */
    const onClose = (code) => {
      closeCode = code;
      maybeFinish();
    };
    const abort = () => finish(TIMEOUT_FAILURE, true);
    const timer = setTimeout(abort, Math.max(0, timeoutMs));
    timer.unref();

    function maybeFinish() {
      if (done || closeCode === undefined || !inputComplete) return;
      let parsed;
      try {
        const outputWire = new TextDecoder("utf-8", { fatal: true })
          .decode(Buffer.concat(chunks, outputBytes));
        if (!outputWire.endsWith("\n") || outputWire.slice(0, -1).includes("\n")) {
          finish(TRANSPORT_FAILURE, false);
          return;
        }
        const value = JSON.parse(outputWire.slice(0, -1));
        parsed = parseResult(value);
        if (parsed === undefined || `${JSON.stringify(parsed)}\n` !== outputWire ||
            (parsed.ok ? closeCode !== 0 : closeCode === 0)) {
          finish(TRANSPORT_FAILURE, false);
          return;
        }
      } catch {
        finish(TRANSPORT_FAILURE, false);
        return;
      }
      finish(parsed, false);
    }

    /** @param {PiQuotaResult} result @param {boolean} terminate */
    function finish(result, terminate) {
      if (done) return;
      done = true;
      clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
      child.removeListener("error", onError);
      child.removeListener("close", onClose);
      input?.removeListener("error", onError);
      output?.removeListener("error", onError);
      output?.removeListener("data", onData);
      try {
        input?.destroy();
        output?.destroy();
        if (terminate && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
      } catch {
        result = TRANSPORT_FAILURE;
      }
      resolve(result);
    }

    child.once("error", onError);
    child.once("close", onClose);
    input?.once("error", onError);
    output?.once("error", onError);
    output?.on("data", onData);
    options.signal?.addEventListener("abort", abort, { once: true });
    try {
      child.unref();
      if (canUnref(input)) input.unref();
      if (canUnref(output)) output.unref();
      if (input === null || output === null) {
        finish(TRANSPORT_FAILURE, true);
        return;
      }
      input.end(wire, (error) => {
        if (error) {
          finish(TRANSPORT_FAILURE, true);
          return;
        }
        inputComplete = true;
        maybeFinish();
      });
      if (options.signal?.aborted) abort();
    } catch {
      finish(TRANSPORT_FAILURE, true);
    }
  });
}
