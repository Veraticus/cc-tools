import { spawn } from "node:child_process";

const MAXIMUM_OUTPUT_BYTES = 2048;
const MAXIMUM_ID_BYTES = 256;
const MAXIMUM_LABEL_BYTES = 60;
const MAXIMUM_GENERATION = 18_446_744_073_709_551_615n;
const DEFAULT_TIMEOUT_MS = 2_000;
const UNAVAILABLE = Object.freeze({ status: /** @type {const} */ ("unavailable") });

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function record(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {string} value */
function containsControl(value) {
  return /\p{Cc}/u.test(value);
}

/** @param {unknown} value @returns {value is string} */
export function validPiGeneration(value) {
  return typeof value === "string" && /^(?:0|[1-9][0-9]*)$/.test(value) &&
    BigInt(value) <= MAXIMUM_GENERATION;
}

/** @param {unknown} value @returns {value is string} */
export function validPiCompletionID(value) {
  return typeof value === "string" && value !== "" && value.isWellFormed() &&
    Buffer.byteLength(value, "utf8") <= MAXIMUM_ID_BYTES && !containsControl(value);
}

/** @param {unknown} value @returns {value is string} */
export function validPiGeneratedLabel(value) {
  if (typeof value !== "string" || !value.isWellFormed() ||
      Buffer.byteLength(value, "utf8") > MAXIMUM_LABEL_BYTES || containsControl(value)) return false;
  const normalized = value.trim();
  if (normalized === "") return false;
  const words = normalized.split(/\s+/u);
  return words.length >= 3 && words.length <= 4;
}

/** @param {string} value */
function validSessionID(value) {
  return validPiCompletionID(value) && value !== "." && !value.includes("/") && !value.includes("..");
}

/** @param {unknown} value @param {string} sessionID */
function parse(value, sessionID) {
  if (!record(value) || Object.keys(value).length !== 8 || value.version !== 1 ||
      value.harness !== "pi" || value.session_id !== sessionID ||
      (value.status !== "known" && value.status !== "missing" && value.status !== "unavailable") ||
      typeof value.label !== "string" || typeof value.completion_id !== "string" ||
      !validPiGeneration(value.source_generation) || !validPiGeneration(value.label_generation)) {
    return UNAVAILABLE;
  }

  if (value.status === "missing" || value.status === "unavailable") {
    if (value.label !== "" || value.completion_id !== "" ||
        value.source_generation !== "0" || value.label_generation !== "0") return UNAVAILABLE;
  } else {
    if (value.source_generation === "0" || !validPiCompletionID(value.completion_id) ||
        BigInt(value.label_generation) > BigInt(value.source_generation)) return UNAVAILABLE;
    if (value.label === "") {
      if (value.label_generation !== "0") return UNAVAILABLE;
    } else if (value.label_generation === "0" || !validPiGeneratedLabel(value.label)) {
      return UNAVAILABLE;
    }
  }

  return {
    status: value.status,
    label: value.label,
    completionID: value.completion_id,
    sourceGeneration: value.source_generation,
    labelGeneration: value.label_generation,
  };
}

/** @param {unknown} value @returns {value is {unref(): void}} */
function canUnref(value) {
  return record(value) && typeof value.unref === "function";
}

/**
 * Read the daemon's bounded read-only metadata response.
 * @param {string} sessionID
 * @param {{command?: string, args?: string[], timeout?: number, stateBase?: string, signal?: AbortSignal}} [options]
 */
export async function readPiMetadata(sessionID, options = {}) {
  if (!validSessionID(sessionID) || options.signal?.aborted) return UNAVAILABLE;
  const command = options.command ?? "steward";
  const args = options.args ?? [
    "session-metadata",
    "--harness",
    "pi",
    "--session-id",
    sessionID,
    ...(options.stateBase === undefined ? [] : ["--state-base", options.stateBase]),
  ];
  const requestedTimeout = options.timeout ?? DEFAULT_TIMEOUT_MS;
  const timeout = Math.min(DEFAULT_TIMEOUT_MS, Math.max(0, requestedTimeout));

  return new Promise((resolve) => {
    let done = false;
    /** @type {NodeJS.Timeout | undefined} */ let timer;
    /** @type {import("node:child_process").ChildProcess | undefined} */ let child;
    /** @type {Buffer[]} */
    const chunks = [];
    let outputBytes = 0;

    /** @param {ReturnType<typeof parse> | typeof UNAVAILABLE} result @param {boolean} [terminate] */
    function finish(result, terminate = false) {
      if (done) return;
      done = true;
      if (timer !== undefined) clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
      if (child !== undefined) {
        child.removeListener("error", onError);
        child.removeListener("close", onClose);
        child.stdout?.removeListener("data", onData);
        try {
          child.stdout?.destroy();
          if (terminate && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        } catch {
          result = UNAVAILABLE;
        }
      }
      resolve(result);
    }

    function abort() {
      finish(UNAVAILABLE, true);
    }

    /** @param {Error} _error */
    function onError(_error) {
      finish(UNAVAILABLE, true);
    }

    /** @param {Buffer | string} chunk */
    function onData(chunk) {
      if (done) return;
      const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      if (outputBytes + bytes.byteLength > MAXIMUM_OUTPUT_BYTES) {
        finish(UNAVAILABLE, true);
        return;
      }
      chunks.push(bytes);
      outputBytes += bytes.byteLength;
    }

    /** @param {number | null} code */
    function onClose(code) {
      if (done) return;
      if (code !== 0 || outputBytes === 0) {
        finish(UNAVAILABLE);
        return;
      }
      try {
        const wire = new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks, outputBytes));
        finish(parse(JSON.parse(wire), sessionID));
      } catch {
        finish(UNAVAILABLE);
      }
    }

    try {
      child = spawn(command, args, { stdio: ["ignore", "pipe", "ignore"] });
      child.once("error", onError);
      child.once("close", onClose);
      child.stdout?.on("data", onData);
      child.unref();
      if (canUnref(child.stdout)) child.stdout.unref();
      timer = setTimeout(abort, timeout);
      timer.unref();
      options.signal?.addEventListener("abort", abort, { once: true });
      if (options.signal?.aborted) abort();
    } catch {
      finish(UNAVAILABLE, true);
    }
  });
}
