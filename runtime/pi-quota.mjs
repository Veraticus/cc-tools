import { watch as fsWatch } from "node:fs";
import { lstat as fsLstat } from "node:fs/promises";
import { join, resolve } from "node:path";

import { createPiQuotaCache, PI_QUOTA_CACHE_FAILURE } from "./pi-quota-cache.mjs";
import { runPiQuotaHelper } from "./pi-quota-helper.mjs";
import { normalizeUpstream } from "./quota.mjs";

export { PI_QUOTA_CACHE_FAILURE } from "./pi-quota-cache.mjs";

const PROVIDER = "openai-codex";
const API = "openai-codex-responses";
const BASE_URL = "https://chatgpt.com/backend-api";
const REFRESH_MS = 5 * 60_000;
const RETAIN_MS = 15 * 60_000;
const PI_QUOTA_AUTH_FAILURE = "Steward Pi quota authentication watch unavailable";

export const PI_QUOTA_UNKNOWN = Object.freeze({
  provider: /** @type {const} */ ("openai-codex"),
  base_url: /** @type {const} */ ("https://chatgpt.com/backend-api"),
  fetched_at: 0,
  stale: false,
  windows: Object.freeze({ five_hour: null, weekly: null }),
});

/** @typedef {import("./pi-quota-helper.mjs").PiQuotaResult} PiQuotaResult */
/** @typedef {import("./pi-quota-helper.mjs").PiQuotaSuccess} PiQuotaSuccess */
/** @typedef {import("./pi-quota-cache.mjs").PiQuotaCacheRecord} PiQuotaCacheRecord */
/** @typedef {{provider: string, id: string, api?: string, baseUrl?: string}} NativeModel */
/** @typedef {{provider: "openai-codex", id: string, baseUrl: "https://chatgpt.com/backend-api"}} ApplicableModel */
/** @typedef {{provider: "openai-codex", base_url: "https://chatgpt.com/backend-api", fetched_at: number, stale: boolean, windows: PiQuotaCacheRecord["windows"]}} PiQuotaSnapshot */
/** @typedef {PiQuotaSnapshot|undefined} PiQuotaFooterSnapshot */
/** @typedef {{close(): void, unref?(): void, on(event: "error", listener: (error: Error) => void): unknown, removeListener(event: "error", listener: (error: Error) => void): unknown}} AuthWatcher */
/** @typedef {(directory: string, options: {persistent: false}, callback: (event: string, filename?: string|Buffer|null) => void) => AuthWatcher} WatchFactory */
/** @typedef {{path: string, read(current: () => boolean): Promise<import("./pi-quota-cache.mjs").PiQuotaCacheRead>, write(value: PiQuotaCacheRecord, current: () => boolean): Promise<boolean>, invalidate(current: () => boolean): Promise<boolean>}} QuotaCache */
/** @typedef {{epoch: number, serial: number, authGeneration: number, healthy: boolean, watcher: AuthWatcher, errorListener: (error: Error) => void, authPath: string, parentPath: string, parentIdentity: string, revision: AuthRevision, cache: QuotaCache}} WatchState */
/** @typedef {{parent: string, auth: string}} AuthRevision */
/** @typedef {{epoch: number, serial: number, authGeneration: number}} AuthToken */
/** @typedef {{getAgentDir?: () => string|Promise<string>, watch?: WatchFactory, lstat?: (path: string, options: {bigint: true}) => Promise<import("node:fs").BigIntStats>, createCache?: (authPath: string) => QuotaCache, runHelper?: (model: ApplicableModel, options: {signal: AbortSignal}) => Promise<PiQuotaResult>, now?: () => number, schedule?: (callback: () => void, delay: number) => () => void, report?: (diagnostic: string) => void}} PiQuotaDependencies */
/** @typedef {{start(model: NativeModel|undefined): Promise<void>, model(model: NativeModel|undefined): Promise<void>, settled(model: NativeModel|undefined, source: string): Promise<void>, dispose(): void}} PiQuotaController */

/** @param {unknown} value @returns {value is string} */
function validIdentity(value) {
  return typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
}

/** @param {NativeModel|undefined} model @returns {ApplicableModel|undefined} */
export function piQuotaModel(model) {
  if (model?.provider !== PROVIDER || model.api !== API || typeof model.baseUrl !== "string") return undefined;
  if (normalizeUpstream(model.baseUrl) !== BASE_URL || typeof model.id !== "string" || model.id === "") return undefined;
  return { provider: PROVIDER, id: model.id, baseUrl: BASE_URL };
}

/** @param {ApplicableModel|undefined} model */
function modelKey(model) {
  return model === undefined ? "" : JSON.stringify([model.provider, model.id, model.baseUrl]);
}

/** @param {() => void} callback @param {number} delay */
function defaultSchedule(callback, delay) {
  const timer = setTimeout(callback, Math.max(0, delay));
  timer.unref();
  return () => clearTimeout(timer);
}

async function publicAgentDir() {
  const { getAgentDir } = await import("@earendil-works/pi-coding-agent");
  return getAgentDir();
}

/** @param {import("node:fs").BigIntStats} stats */
function owned(stats) {
  return typeof process.getuid !== "function" || stats.uid === BigInt(process.getuid());
}

/** @param {import("node:fs").BigIntStats} stats */
function safeParent(stats) {
  return stats.isDirectory() && !stats.isSymbolicLink() && owned(stats);
}

/** @param {import("node:fs").BigIntStats} stats */
function safeAuth(stats) {
  return stats.isFile() && !stats.isSymbolicLink() && owned(stats) && (stats.mode & 0o077n) === 0n;
}

/** @param {import("node:fs").BigIntStats} stats */
function parentIdentity(stats) {
  return `${stats.dev}:${stats.ino}`;
}

/** @param {import("node:fs").BigIntStats} stats */
function authIdentity(stats) {
  return `${stats.dev}:${stats.ino}:${stats.size}:${stats.mtimeNs}:${stats.ctimeNs}`;
}

/** @param {AuthRevision} left @param {AuthRevision} right */
function sameRevision(left, right) {
  return left.parent === right.parent && left.auth === right.auth;
}

/** @param {PiQuotaSuccess|PiQuotaCacheRecord} sample */
function cacheRecord(sample) {
  return {
    version: /** @type {const} */ (1),
    provider: /** @type {const} */ ("openai-codex"),
    base_url: /** @type {const} */ ("https://chatgpt.com/backend-api"),
    account_key: sample.account_key,
    fetched_at: sample.fetched_at,
    windows: sample.windows,
  };
}

/** @param {PiQuotaCacheRecord} sample @param {boolean} stale */
function footerSnapshot(sample, stale) {
  return {
    provider: /** @type {const} */ ("openai-codex"),
    base_url: /** @type {const} */ ("https://chatgpt.com/backend-api"),
    fetched_at: sample.fetched_at,
    stale,
    windows: sample.windows,
  };
}

/**
 * Own one root-process quota producer. It is inert until an applicable TUI
 * lifecycle calls start().
 * @param {(snapshot: PiQuotaFooterSnapshot, invalidateLastGood: boolean) => void} onChange
 * @param {PiQuotaDependencies} [dependencies]
 * @returns {PiQuotaController}
 */
export function createPiQuotaController(onChange, dependencies = {}) {
  const getAgentDir = dependencies.getAgentDir ?? publicAgentDir;
  const watch = dependencies.watch ?? (/** @type {WatchFactory} */ (fsWatch));
  const stat = dependencies.lstat ?? ((path, options) => fsLstat(path, options));
  const createCache = dependencies.createCache ?? ((authPath) => createPiQuotaCache(authPath));
  const helper = dependencies.runHelper ?? runPiQuotaHelper;
  const now = dependencies.now ?? Date.now;
  const schedule = dependencies.schedule ?? defaultSchedule;
  const report = dependencies.report ?? (() => {});

  let disposed = false;
  let epoch = 0;
  let watchSerial = 0;
  /** @type {ApplicableModel|undefined} */ let selected;
  let selectedKey = "";
  /** @type {WatchState|undefined} */ let authWatch;
  /** @type {AbortController|undefined} */ let activeHelper;
  /** @type {Promise<void>|undefined} */ let running;
  let pending = false;
  let lastSource = "";
  /** @type {string|undefined} */ let accountKey;
  /** @type {PiQuotaCacheRecord|undefined} */ let lastRecord;
  /** @type {(() => void)|undefined} */ let cancelPeriodic;
  /** @type {Array<() => void>} */ let freshnessDeadlines = [];

  /** @param {string} diagnostic */
  function safeReport(diagnostic) {
    try { report(diagnostic); } catch { /* Diagnostics never alter producer state. */ }
  }

  /** @param {PiQuotaFooterSnapshot} snapshot @param {boolean} invalidate */
  function emit(snapshot, invalidate) {
    try { onChange(snapshot, invalidate); } catch { /* UI teardown can race producer cleanup. */ }
  }

  function clearFreshness() {
    for (const cancel of freshnessDeadlines) cancel();
    freshnessDeadlines = [];
  }

  function closeWatch() {
    const state = authWatch;
    authWatch = undefined;
    if (state === undefined) return;
    state.healthy = false;
    state.watcher.removeListener("error", state.errorListener);
    try { state.watcher.close(); } catch { /* State is already fail-closed. */ }
  }

  function reset() {
    epoch += 1;
    pending = false;
    activeHelper?.abort();
    activeHelper = undefined;
    cancelPeriodic?.();
    cancelPeriodic = undefined;
    clearFreshness();
    closeWatch();
    accountKey = undefined;
    lastRecord = undefined;
    lastSource = "";
  }

  /** @param {WatchState} state */
  function tokenFor(state) {
    return { epoch: state.epoch, serial: state.serial, authGeneration: state.authGeneration };
  }

  /** @param {AuthToken} token */
  function current(token) {
    return !disposed && epoch === token.epoch && authWatch?.healthy === true &&
      authWatch.serial === token.serial && authWatch.authGeneration === token.authGeneration;
  }

  /** @param {WatchState} state @param {boolean} recover */
  function invalidateAuth(state, recover) {
    if (disposed || authWatch !== state || state.epoch !== epoch) return Promise.resolve(false);
    state.authGeneration += 1;
    state.revision = { parent: state.parentIdentity, auth: "" };
    accountKey = undefined;
    lastRecord = undefined;
    clearFreshness();
    activeHelper?.abort();
    emit(PI_QUOTA_UNKNOWN, true);
    const invalidation = tokenFor(state);
    const clearing = invalidateCache(state, invalidation);
    if (recover && state.healthy) queueMicrotask(() => {
      if (current(invalidation)) void requestRefresh();
    });
    return clearing;
  }

  /** @param {WatchState} state */
  function failWatch(state) {
    if (authWatch !== state) return;
    state.healthy = false;
    state.authGeneration += 1;
    activeHelper?.abort();
    accountKey = undefined;
    lastRecord = undefined;
    clearFreshness();
    try { state.watcher.close(); } catch { /* State remains unknown. */ }
    emit(PI_QUOTA_UNKNOWN, true);
    safeReport(PI_QUOTA_AUTH_FAILURE);
  }

  /** @param {WatchState} state @param {AuthToken} token @returns {Promise<AuthRevision|undefined>} */
  async function readRevision(state, token) {
    if (!current(token)) return undefined;
    try {
      const before = await stat(state.parentPath, { bigint: true });
      if (!current(token) || !safeParent(before)) return undefined;
      const beforeIdentity = parentIdentity(before);
      if (state.parentIdentity !== "" && beforeIdentity !== state.parentIdentity) return undefined;

      const auth = await stat(state.authPath, { bigint: true });
      if (!current(token) || !safeAuth(auth)) return undefined;

      const after = await stat(state.parentPath, { bigint: true });
      if (!current(token) || !safeParent(after) || parentIdentity(after) !== beforeIdentity) return undefined;
      return { parent: beforeIdentity, auth: authIdentity(auth) };
    } catch {
      return undefined;
    }
  }

  /**
   * Re-stat after an awaited cache operation and reject any candidate produced
   * under different auth metadata before it can reach the footer.
   * @param {WatchState} state
   * @param {AuthToken} token
   * @param {AuthRevision} expected
   */
  async function revisionStillCurrent(state, token, expected) {
    if (!current(token)) return false;
    const observed = await readRevision(state, token);
    if (!current(token)) return false;
    if (observed === undefined || observed.parent !== expected.parent) {
      await invalidateAuth(state, false);
      failWatch(state);
      return false;
    }
    if (!sameRevision(observed, expected)) {
      await invalidateAuth(state, true);
      return false;
    }
    state.revision = observed;
    return true;
  }

  /** @param {number} expectedEpoch @returns {Promise<WatchState|undefined>} */
  async function ensureWatch(expectedEpoch) {
    if (disposed || epoch !== expectedEpoch || selected === undefined) return undefined;
    if (authWatch?.healthy) return authWatch;
    closeWatch();

    let agentDir;
    try {
      agentDir = await getAgentDir();
    } catch {
      emit(PI_QUOTA_UNKNOWN, true);
      safeReport(PI_QUOTA_AUTH_FAILURE);
      return undefined;
    }
    if (disposed || epoch !== expectedEpoch || typeof agentDir !== "string" || agentDir === "") return undefined;
    const parentPath = resolve(agentDir);
    const authPath = join(parentPath, "auth.json");
    /** @type {QuotaCache} */
    let cache;
    try {
      cache = createCache(authPath);
    } catch {
      emit(PI_QUOTA_UNKNOWN, true);
      safeReport(PI_QUOTA_CACHE_FAILURE);
      return undefined;
    }
    /** @type {WatchState|undefined} */
    let state;
    try {
      const serial = ++watchSerial;
      const watcher = watch(parentPath, { persistent: false }, (_event, filename) => {
        const active = state;
        if (active === undefined || authWatch !== active || !active.healthy) return;
        if (filename !== undefined && filename !== null && String(filename) !== "auth.json") return;
        void invalidateAuth(active, true);
      });
      const errorListener = () => { if (state !== undefined) failWatch(state); };
      state = {
        epoch: expectedEpoch,
        serial,
        authGeneration: 0,
        healthy: true,
        watcher,
        errorListener,
        authPath,
        parentPath,
        parentIdentity: "",
        revision: { parent: "", auth: "" },
        cache,
      };
      authWatch = state;
      watcher.on("error", errorListener);
      watcher.unref?.();
    } catch {
      emit(PI_QUOTA_UNKNOWN, true);
      safeReport(PI_QUOTA_AUTH_FAILURE);
      return undefined;
    }

    const setupToken = tokenFor(state);
    const revision = await readRevision(state, setupToken);
    if (!current(setupToken) || revision === undefined) {
      failWatch(state);
      return undefined;
    }
    state.parentIdentity = revision.parent;
    state.revision = revision;
    return state;
  }

  /** @param {WatchState} state @param {AuthToken} token */
  async function invalidateCache(state, token) {
    const ok = await state.cache.invalidate(() => current(token));
    if (current(token) && !ok) safeReport(PI_QUOTA_CACHE_FAILURE);
    return ok;
  }

  /** @param {PiQuotaCacheRecord} sample */
  function sampleAge(sample) {
    let currentTime;
    try { currentTime = now(); } catch { return undefined; }
    if (!Number.isSafeInteger(currentTime) || !Number.isSafeInteger(sample.fetched_at) ||
        sample.fetched_at <= 0 || sample.fetched_at > currentTime) return undefined;
    return currentTime - sample.fetched_at;
  }

  /** @param {PiQuotaCacheRecord} sample @param {boolean} forceStale */
  function publish(sample, forceStale) {
    const age = sampleAge(sample);
    if (age === undefined || age >= RETAIN_MS) {
      lastRecord = undefined;
      clearFreshness();
      emit(PI_QUOTA_UNKNOWN, true);
      return false;
    }
    lastRecord = sample;
    emit(footerSnapshot(sample, forceStale || age >= REFRESH_MS), false);
    clearFreshness();
    if (age < REFRESH_MS) {
      freshnessDeadlines.push(schedule(() => {
        if (disposed || lastRecord !== sample) return;
        const nextAge = sampleAge(sample);
        if (nextAge !== undefined && nextAge >= REFRESH_MS && nextAge < RETAIN_MS) {
          emit(footerSnapshot(sample, true), false);
        }
      }, REFRESH_MS - age));
    }
    freshnessDeadlines.push(schedule(() => {
      if (disposed || lastRecord !== sample) return;
      const nextAge = sampleAge(sample);
      if (nextAge === undefined || nextAge >= RETAIN_MS) {
        lastRecord = undefined;
        clearFreshness();
        emit(PI_QUOTA_UNKNOWN, true);
        const state = authWatch;
        if (state?.healthy) {
          const expiration = tokenFor(state);
          void invalidateCache(state, expiration);
        }
      }
    }, RETAIN_MS - age));
    return true;
  }

  /** @param {WatchState} state @param {AuthToken} token */
  async function clearAccountState(state, token) {
    accountKey = undefined;
    lastRecord = undefined;
    clearFreshness();
    emit(PI_QUOTA_UNKNOWN, true);
    await invalidateCache(state, token);
  }

  /** @param {PiQuotaResult} result @param {WatchState} state @param {AuthToken} token @param {AuthRevision} expectedRevision */
  async function applyResult(result, state, token, expectedRevision) {
    if (!current(token)) return;
    if (result.ok) {
      if (!validIdentity(result.account_key)) {
        await clearAccountState(state, token);
        return;
      }
      if (accountKey !== undefined && accountKey !== result.account_key) {
        accountKey = undefined;
        lastRecord = undefined;
        clearFreshness();
        emit(PI_QUOTA_UNKNOWN, true);
      }
      accountKey = result.account_key;
      const sample = cacheRecord(result);
      const age = sampleAge(sample);
      if (age === undefined || age >= RETAIN_MS) {
        await clearAccountState(state, token);
        return;
      }
      const written = await state.cache.write(sample, () => current(token));
      if (!await revisionStillCurrent(state, token, expectedRevision)) return;
      if (!written) {
        lastRecord = undefined;
        emit(PI_QUOTA_UNKNOWN, true);
        safeReport(PI_QUOTA_CACHE_FAILURE);
        return;
      }
      publish(sample, false);
      return;
    }

    if (["invalid_request", "not_applicable", "auth_unavailable", "auth_rejected"].includes(result.error)) {
      await clearAccountState(state, token);
      return;
    }
    const resultAccount = result.account_key;
    if (!validIdentity(resultAccount) || result.provider !== PROVIDER || result.base_url !== BASE_URL) {
      await clearAccountState(state, token);
      return;
    }
    if (accountKey !== undefined && accountKey !== resultAccount) {
      accountKey = undefined;
      lastRecord = undefined;
      clearFreshness();
      emit(PI_QUOTA_UNKNOWN, true);
    }
    accountKey = resultAccount;
    if (current(token) && lastRecord?.account_key === resultAccount && publish(lastRecord, true)) return;

    const cached = await state.cache.read(() => current(token));
    if (!await revisionStillCurrent(state, token, expectedRevision)) return;
    if (cached.status === "unavailable") {
      emit(PI_QUOTA_UNKNOWN, true);
      safeReport(PI_QUOTA_CACHE_FAILURE);
      return;
    }
    if (cached.status === "known" && cached.record.account_key === resultAccount && publish(cached.record, true)) return;
    if (cached.status === "known" && cached.record.account_key !== resultAccount) {
      await invalidateCache(state, token);
    }
    emit(PI_QUOTA_UNKNOWN, true);
  }

  async function refresh() {
    const refreshEpoch = epoch;
    const model = selected;
    if (disposed || model === undefined) return;
    const state = await ensureWatch(refreshEpoch);
    if (state === undefined || !state.healthy || epoch !== refreshEpoch) return;
    const token = tokenFor(state);
    const initial = await readRevision(state, token);
    if (!current(token)) return;
    if (initial === undefined) {
      failWatch(state);
      return;
    }
    if (state.revision.auth !== "" &&
        (initial.parent !== state.revision.parent || initial.auth !== state.revision.auth)) {
      void invalidateAuth(state, true);
      return;
    }
    state.revision = initial;

    const controller = new AbortController();
    activeHelper = controller;
    /** @type {PiQuotaResult} */
    let result;
    try {
      result = await helper(model, { signal: controller.signal });
    } catch {
      result = { version: 1, ok: false, error: "fetch_failed" };
    } finally {
      if (activeHelper === controller) activeHelper = undefined;
    }
    if (!current(token)) return;

    const finalRevision = await readRevision(state, token);
    if (!current(token)) return;
    if (finalRevision === undefined || finalRevision.parent !== initial.parent) {
      failWatch(state);
      return;
    }
    if (finalRevision.auth !== initial.auth) {
      void invalidateAuth(state, true);
      return;
    }
    state.revision = finalRevision;
    await applyResult(result, state, token, finalRevision);
  }

  function requestRefresh() {
    if (disposed || selected === undefined) return Promise.resolve();
    pending = true;
    if (running !== undefined) return running;
    running = (async () => {
      while (!disposed && pending && selected !== undefined) {
        pending = false;
        await refresh();
      }
    })().finally(() => { running = undefined; });
    return running;
  }

  function armPeriodic() {
    cancelPeriodic?.();
    cancelPeriodic = schedule(() => {
      cancelPeriodic = undefined;
      if (disposed || selected === undefined) return;
      void requestRefresh();
      armPeriodic();
    }, REFRESH_MS);
  }

  /** @param {NativeModel|undefined} model */
  function select(model) {
    const applicable = piQuotaModel(model);
    reset();
    selected = applicable;
    selectedKey = modelKey(applicable);
    if (applicable === undefined) {
      emit(undefined, true);
      return Promise.resolve();
    }
    emit(PI_QUOTA_UNKNOWN, true);
    armPeriodic();
    return requestRefresh();
  }

  return {
    start(model) {
      return select(model);
    },
    model(model) {
      const applicable = piQuotaModel(model);
      if (modelKey(applicable) === selectedKey) return running ?? Promise.resolve();
      return select(model);
    },
    settled(model, source) {
      const applicable = piQuotaModel(model);
      if (modelKey(applicable) !== selectedKey) return select(model);
      if (applicable === undefined || source === "" || source === lastSource) return running ?? Promise.resolve();
      lastSource = source;
      return requestRefresh();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      reset();
      selected = undefined;
      selectedKey = "";
    },
  };
}
