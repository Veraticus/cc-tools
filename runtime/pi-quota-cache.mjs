import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { lstat, mkdir, open, rename, unlink } from "node:fs/promises";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import process from "node:process";

export const PI_QUOTA_CACHE_FAILURE = "Steward Pi quota cache unavailable";
const MAXIMUM_CACHE_BYTES = 4096;
const ACCOUNT_KEY = /^[a-f0-9]{64}$/u;
let temporarySequence = 0;
/** @type {Map<string, Promise<void>>} */
const operationQueues = new Map();

/** @typedef {import("./pi-quota-helper.mjs").PiQuotaWindow} PiQuotaWindow */
/** @typedef {{version: 1, provider: "openai-codex", base_url: "https://chatgpt.com/backend-api", account_key: string, fetched_at: number, windows: {five_hour: PiQuotaWindow|null, weekly: PiQuotaWindow|null}}} PiQuotaCacheRecord */
/** @typedef {{status: "known", record: PiQuotaCacheRecord}|{status: "missing"}|{status: "unavailable"}} PiQuotaCacheRead */
/** @typedef {{close?: (handle: import("node:fs/promises").FileHandle) => Promise<void>}} PiQuotaCacheFilesystem */
/** @typedef {{environment?: Record<string, string|undefined>, filesystem?: PiQuotaCacheFilesystem, report?: (diagnostic: string) => void}} PiQuotaCacheOptions */

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function record(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {Record<string, unknown>} value @param {string[]} keys */
function exact(value, keys) {
  return Object.keys(value).length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

/** @param {unknown} value @returns {value is PiQuotaWindow|null} */
function validWindow(value) {
  if (value === null) return true;
  if (!record(value) || !exact(value, ["remaining_percent", "reset_at"])) return false;
  return typeof value.remaining_percent === "number" && Number.isFinite(value.remaining_percent) &&
    value.remaining_percent >= 0 && value.remaining_percent <= 100 &&
    typeof value.reset_at === "number" && Number.isSafeInteger(value.reset_at) && value.reset_at >= 0;
}

/** @param {unknown} value @returns {value is PiQuotaCacheRecord} */
function validRecord(value) {
  if (!record(value) || !exact(value, ["version", "provider", "base_url", "account_key", "fetched_at", "windows"]) ||
      value.version !== 1 || value.provider !== "openai-codex" ||
      value.base_url !== "https://chatgpt.com/backend-api" ||
      typeof value.account_key !== "string" || !ACCOUNT_KEY.test(value.account_key) ||
      typeof value.fetched_at !== "number" || !Number.isSafeInteger(value.fetched_at) || value.fetched_at <= 0 ||
      !record(value.windows) || !exact(value.windows, ["five_hour", "weekly"])) return false;
  return validWindow(value.windows.five_hour) && validWindow(value.windows.weekly);
}

/** @param {unknown} error @param {string} code */
function hasCode(error, code) {
  return record(error) && error.code === code;
}

/** @param {import("node:fs").Stats} stats @param {number} mode @param {"file"|"directory"} kind */
function secure(stats, mode, kind) {
  const typeMatches = kind === "file" ? stats.isFile() : stats.isDirectory();
  const ownerMatches = typeof process.getuid !== "function" || stats.uid === process.getuid();
  return typeMatches && !stats.isSymbolicLink() && ownerMatches && (stats.mode & 0o777) === mode;
}

/** @param {(diagnostic: string) => void} report */
function reportFailure(report) {
  try {
    report(PI_QUOTA_CACHE_FAILURE);
  } catch {
    // Cache diagnostics cannot destabilize the footer producer.
  }
}

/** @param {string} path */
function ancestry(path) {
  const paths = [];
  let cursor = path;
  while (true) {
    paths.push(cursor);
    const parent = dirname(cursor);
    if (parent === cursor) return paths.reverse();
    cursor = parent;
  }
}

/** @param {import("node:fs").Stats} stats */
function safeAncestor(stats) {
  return stats.isDirectory() && !stats.isSymbolicLink();
}

/**
 * Serialize all owned operations for one canonical cache path, including across
 * replacement controller/cache instances. The registry entry exists only while
 * its current tail is pending.
 * @template T
 * @param {string} path
 * @param {() => Promise<T>} operation
 * @returns {Promise<T>}
 */
function serialized(path, operation) {
  const previous = operationQueues.get(path) ?? Promise.resolve();
  const result = previous.then(operation, operation);
  const tail = result.then(() => undefined, () => undefined);
  operationQueues.set(path, tail);
  void tail.then(() => {
    if (operationQueues.get(path) === tail) operationQueues.delete(path);
  });
  return result;
}

/**
 * Create the one cache scope owned by an auth-file location.
 * @param {string} authPath
 * @param {PiQuotaCacheOptions} [options]
 */
export function createPiQuotaCache(authPath, options = {}) {
  const environment = options.environment ?? process.env;
  const home = environment.HOME ?? homedir();
  const cacheBase = resolve(environment.XDG_CACHE_HOME || join(home, ".cache"));
  const directory = join(cacheBase, "steward", "pi-quota");
  const directoryAncestry = ancestry(directory);
  const authDigest = createHash("sha256").update(resolve(authPath)).digest("hex");
  const path = join(directory, `${authDigest}.json`);
  const report = options.report ?? (() => {});
  const closeFile = options.filesystem?.close ?? ((handle) => handle.close());

  /** @param {() => boolean} current @param {boolean} create */
  async function ensureDirectory(current, create) {
    let missing = directoryAncestry.length;
    for (let index = 0; index < directoryAncestry.length; index += 1) {
      if (!current()) return false;
      try {
        const stats = await lstat(directoryAncestry[index]);
        if (!current()) return false;
        if (!safeAncestor(stats)) throw new Error("unsafe cache ancestry");
      } catch (error) {
        if (!hasCode(error, "ENOENT")) throw error;
        missing = index;
        break;
      }
    }
    if (missing !== directoryAncestry.length) {
      if (!create) return false;
      for (let index = missing; index < directoryAncestry.length; index += 1) {
        if (!current()) return false;
        try {
          await mkdir(directoryAncestry[index], { mode: 0o700 });
        } catch (error) {
          if (!hasCode(error, "EEXIST")) throw error;
        }
        if (!current()) return false;
        const stats = await lstat(directoryAncestry[index]);
        if (!current()) return false;
        if (!safeAncestor(stats)) throw new Error("unsafe cache ancestry");
      }
    }
    const stats = await lstat(directory);
    if (!current()) return false;
    if (!secure(stats, 0o700, "directory")) throw new Error("unsafe cache directory");
    return true;
  }

  /** @returns {Promise<import("node:fs").Stats|undefined>} */
  async function targetStats() {
    try {
      return await lstat(path);
    } catch (error) {
      if (hasCode(error, "ENOENT")) return undefined;
      throw error;
    }
  }

  return {
    path,

    /** @param {() => boolean} current @returns {Promise<PiQuotaCacheRead>} */
    read(current) {
      return serialized(path, async () => {
        /** @type {import("node:fs/promises").FileHandle|undefined} */
        let handle;
        try {
          if (!current()) return { status: "unavailable" };
          if (!await ensureDirectory(current, false)) {
            return current() ? { status: "missing" } : { status: "unavailable" };
          }
          const stats = await targetStats();
          if (!current()) return { status: "unavailable" };
          if (stats === undefined) return { status: "missing" };
          if (!secure(stats, 0o600, "file") || stats.size <= 0 || stats.size > MAXIMUM_CACHE_BYTES) {
            throw new Error("unsafe cache snapshot");
          }
          handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
          if (!current()) return { status: "unavailable" };
          const opened = await handle.stat();
          if (!current()) return { status: "unavailable" };
          if (!secure(opened, 0o600, "file") || opened.size <= 0 || opened.size > MAXIMUM_CACHE_BYTES) {
            throw new Error("unsafe opened cache snapshot");
          }
          const bounded = Buffer.allocUnsafe(MAXIMUM_CACHE_BYTES + 1);
          const { bytesRead } = await handle.read(bounded, 0, bounded.byteLength, 0);
          if (!current()) return { status: "unavailable" };
          if (bytesRead !== opened.size || bytesRead > MAXIMUM_CACHE_BYTES) {
            throw new Error("changed cache snapshot");
          }
          const contents = bounded.subarray(0, bytesRead);
          const wire = new TextDecoder("utf-8", { fatal: true }).decode(contents);
          const value = JSON.parse(wire);
          if (!validRecord(value) || wire !== `${JSON.stringify(value)}\n`) {
            throw new Error("invalid cache snapshot");
          }
          return { status: "known", record: value };
        } catch {
          reportFailure(report);
          return { status: "unavailable" };
        } finally {
          await handle?.close().catch(() => {});
        }
      });
    },

    /** @param {unknown} value @param {() => boolean} current */
    write(value, current) {
      return serialized(path, async () => {
        /** @type {string|undefined} */
        let temporary;
        /** @type {import("node:fs/promises").FileHandle|undefined} */
        let handle;
        try {
          if (!validRecord(value) || !current()) throw new Error("invalid cache record");
          const wire = `${JSON.stringify(value)}\n`;
          if (Buffer.byteLength(wire, "utf8") > MAXIMUM_CACHE_BYTES) throw new Error("oversized cache record");
          if (!await ensureDirectory(current, true)) throw new Error("stale cache write");
          const existing = await targetStats();
          if (!current()) throw new Error("stale cache write");
          if (existing !== undefined && !secure(existing, 0o600, "file")) {
            throw new Error("unsafe cache target");
          }
          temporarySequence += 1;
          temporary = join(directory, `.${authDigest}.${process.pid}.${temporarySequence}.tmp`);
          handle = await open(temporary, constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY, 0o600);
          if (!current()) throw new Error("stale cache write");
          await handle.writeFile(wire, "utf8");
          if (!current()) throw new Error("stale cache write");
          await handle.chmod(0o600);
          if (!current()) throw new Error("stale cache write");
          await handle.sync();
          if (!current()) throw new Error("stale cache write");
          await closeFile(handle);
          if (!current()) {
            handle = undefined;
            throw new Error("stale cache write");
          }
          handle = undefined;
          await rename(temporary, path);
          if (!current()) {
            temporary = undefined;
            await unlink(path).catch(() => {});
            throw new Error("stale cache publish");
          }
          temporary = undefined;
          return true;
        } catch {
          reportFailure(report);
          return false;
        } finally {
          await handle?.close().catch(() => {});
          if (temporary !== undefined) await unlink(temporary).catch(() => {});
        }
      });
    },

    /** @param {() => boolean} current */
    invalidate(current) {
      return serialized(path, async () => {
        try {
          if (!current()) return false;
          if (!await ensureDirectory(current, false)) return current();
          const existing = await targetStats();
          if (!current()) return false;
          if (existing === undefined) return true;
          if (!secure(existing, 0o600, "file")) throw new Error("unsafe cache target");
          await unlink(path);
          return current();
        } catch {
          reportFailure(report);
          return false;
        }
      });
    },
  };
}
