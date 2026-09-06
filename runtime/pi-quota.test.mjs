import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { chmod, lstat, mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  PI_QUOTA_CACHE_FAILURE,
  PI_QUOTA_UNKNOWN,
  createPiQuotaController,
  piQuotaModel,
} from "./pi-quota.mjs";

/** @typedef {import("./pi-quota-helper.mjs").PiQuotaResult} PiQuotaResult */
/** @typedef {import("./pi-quota-helper.mjs").PiQuotaSuccess} PiQuotaSuccess */
/** @typedef {import("./pi-quota-helper.mjs").PiQuotaFailure} PiQuotaFailure */
/** @typedef {import("./pi-quota-cache.mjs").PiQuotaCacheRecord} PiQuotaCacheRecord */
/** @typedef {import("./pi-quota.mjs").PiQuotaSnapshot} PiQuotaSnapshot */
/** @typedef {import("./pi-quota.mjs").PiQuotaFooterSnapshot} PiQuotaFooterSnapshot */
/** @typedef {{snapshot: PiQuotaFooterSnapshot, invalidate: boolean}} Change */
/** @typedef {{reads: number, writes: number, invalidations: number, path: string, read(current: () => boolean): Promise<import("./pi-quota-cache.mjs").PiQuotaCacheRead>, write(record: PiQuotaCacheRecord, current: () => boolean): Promise<boolean>, invalidate(current: () => boolean): Promise<boolean>, value(): PiQuotaCacheRecord|undefined, set(next: PiQuotaCacheRecord|undefined): void}} MemoryCache */

const NOW = 1_700_000_000_000;
const ACCOUNT_A = "a".repeat(64);
const ACCOUNT_B = "b".repeat(64);
const APPLICABLE = Object.freeze({
  provider: "openai-codex",
  id: "gpt-5.6-luna",
  api: "openai-codex-responses",
  baseUrl: "https://chatgpt.com/backend-api/codex/",
});

/** @param {string} accountKey @param {number} [fetchedAt] @param {PiQuotaCacheRecord["windows"]} [windows] @returns {PiQuotaSuccess} */
function success(accountKey, fetchedAt = NOW, windows = {
  five_hour: { remaining_percent: 75, reset_at: NOW + 3_600_000 },
  weekly: { remaining_percent: 20, reset_at: NOW + 604_800_000 },
}) {
  return {
    version: 1,
    ok: true,
    provider: "openai-codex",
    base_url: "https://chatgpt.com/backend-api",
    account_key: accountKey,
    fetched_at: fetchedAt,
    windows,
  };
}

/** @param {PiQuotaFailure["error"]} error @param {string} [accountKey] @returns {PiQuotaFailure} */
function failure(error, accountKey) {
  return accountKey === undefined
    ? { version: 1, ok: false, error }
    : {
        version: 1,
        ok: false,
        error,
        provider: "openai-codex",
        base_url: "https://chatgpt.com/backend-api",
        account_key: accountKey,
      };
}

/** @returns {{tasks: Array<{callback: () => void, delay: number, cancelled: boolean}>, schedule(callback: () => void, delay: number): () => void}} */
function scheduler() {
  /** @type {Array<{callback: () => void, delay: number, cancelled: boolean}>} */
  const tasks = [];
  return {
    tasks,
    schedule(callback, delay) {
      const task = { callback, delay, cancelled: false };
      tasks.push(task);
      return () => { task.cancelled = true; };
    },
  };
}

class SyntheticWatcher extends EventEmitter {
  closed = false;
  unrefCalls = 0;
  close() { this.closed = true; }
  unref() { this.unrefCalls += 1; }
}

/** @returns {{calls: Array<{directory: string, options: {persistent: false}}>, watch: import("./pi-quota.mjs").WatchFactory, event(event: string, filename?: string|Buffer|null): void, error(error: Error): void, watcher(): SyntheticWatcher|undefined}} */
function watchHarness() {
  /** @type {((event: string, filename?: string | Buffer | null) => void) | undefined} */
  let callback;
  /** @type {SyntheticWatcher | undefined} */
  let watcher;
  /** @type {Array<{directory: string, options: {persistent: false}}>} */
  const calls = [];
  return {
    calls,
    watch(directory, options, selected) {
      calls.push({ directory, options });
      callback = selected;
      watcher = new SyntheticWatcher();
      return watcher;
    },
    event(event, filename) { callback?.(event, filename); },
    error(error) { watcher?.emit("error", error); },
    watcher: () => watcher,
  };
}

/** @param {PiQuotaCacheRecord} [initial] @returns {MemoryCache} */
function memoryCache(initial) {
  let value = initial;
  return {
    path: "memory-cache",
    reads: 0,
    writes: 0,
    invalidations: 0,
    async read(current) {
      this.reads += 1;
      if (!current()) return { status: "unavailable" };
      return value === undefined ? { status: "missing" } : { status: "known", record: value };
    },
    async write(record, current) {
      this.writes += 1;
      if (!current()) return false;
      value = record;
      return true;
    },
    async invalidate(current) {
      this.invalidations += 1;
      if (!current()) return false;
      value = undefined;
      return true;
    },
    value: () => value,
    set(next) { value = next; },
  };
}

/** @template T */
function deferred() {
  /** @type {(value: T) => void} */ let resolve = () => {};
  const promise = new Promise((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

/** @param {import("node:test").TestContext} t */
async function authFixture(t) {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-controller-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const agent = join(root, "agent");
  const auth = join(agent, "auth.json");
  await mkdir(agent, { mode: 0o700 });
  await writeFile(auth, "opaque-auth-fixture-a\n", { mode: 0o600 });
  await chmod(auth, 0o600);
  return { agent, auth, root };
}

/** @param {PiQuotaSuccess|PiQuotaCacheRecord} snapshot @returns {PiQuotaSnapshot} */
function footer(snapshot) {
  return {
    provider: snapshot.provider,
    base_url: snapshot.base_url,
    fetched_at: snapshot.fetched_at,
    stale: false,
    windows: snapshot.windows,
  };
}

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

/** @returns {Change[]} */
function changeLog() { return []; }

/** @returns {string[]} */
function stringLog() { return []; }

/** @param {() => boolean} predicate @param {string} label */
async function waitFor(predicate, label) {
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for ${label}`);
}

test("model applicability requires the native provider, API, and normalized upstream URL", () => {
  assert.deepEqual(piQuotaModel(APPLICABLE), {
    provider: "openai-codex",
    id: "gpt-5.6-luna",
    baseUrl: "https://chatgpt.com/backend-api",
  });
  for (const model of [
    undefined,
    { ...APPLICABLE, provider: "openai" },
    { ...APPLICABLE, api: "openai-responses" },
    { ...APPLICABLE, baseUrl: "https://gateway.invalid/backend-api" },
    { ...APPLICABLE, baseUrl: undefined },
  ]) assert.equal(piQuotaModel(model), undefined);
});

test("inapplicable models perform no SDK path, watch, cache, helper, or timer work", async () => {
  const changes = changeLog();
  let agentDirCalls = 0;
  let helperCalls = 0;
  let watchCalls = 0;
  let cacheCalls = 0;
  let scheduleCalls = 0;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    async getAgentDir() { agentDirCalls += 1; return "/must-not-use"; },
    watch() { watchCalls += 1; throw new Error("must not watch"); },
    createCache() { cacheCalls += 1; return memoryCache(); },
    async runHelper() { helperCalls += 1; return success(ACCOUNT_A); },
    schedule() { scheduleCalls += 1; return () => {}; },
  });
  await controller.start({ ...APPLICABLE, provider: "anthropic" });
  assert.deepEqual(changes, [{ snapshot: undefined, invalidate: true }]);
  assert.deepEqual({ agentDirCalls, watchCalls, cacheCalls, helperCalls, scheduleCalls }, {
    agentDirCalls: 0, watchCalls: 0, cacheCalls: 0, helperCalls: 0, scheduleCalls: 0,
  });
  controller.dispose();
});

test("healthy refresh watches before stat, uses only helper identity, writes cache, and publishes footer-safe data", async (t) => {
  const fixture = await authFixture(t);
  const watch = watchHarness();
  const cache = memoryCache();
  const timeline = stringLog();
  const changes = changeLog();
  const schedules = scheduler();
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch(directory, options, callback) {
      timeline.push("watch");
      return watch.watch(directory, options, callback);
    },
    async lstat(path, options) {
      timeline.push(`stat:${path.split("/").at(-1)}`);
      return lstat(path, options);
    },
    createCache: () => cache,
    async runHelper(model, options) {
      timeline.push("helper");
      assert.deepEqual(model, {
        provider: "openai-codex", id: APPLICABLE.id, baseUrl: "https://chatgpt.com/backend-api",
      });
      assert.ok(options.signal instanceof AbortSignal);
      return success(ACCOUNT_A);
    },
    now: () => NOW,
    schedule: schedules.schedule,
  });

  await controller.start(APPLICABLE);
  assert.equal(timeline[0], "watch");
  assert.ok(timeline.indexOf("helper") > timeline.indexOf("stat:auth.json"));
  assert.equal(watch.calls[0]?.options.persistent, false);
  assert.equal(watch.watcher()?.unrefCalls, 1);
  assert.equal(cache.writes, 1);
  assert.deepEqual(changes.at(-1), { snapshot: footer(success(ACCOUNT_A)), invalidate: false });
  assert.doesNotMatch(JSON.stringify(changes), /account_key/);
  assert.ok(schedules.tasks.some((task) => task.delay === 300_000));
  assert.ok(schedules.tasks.some((task) => task.delay === 900_000));
  controller.dispose();
  assert.equal(watch.watcher()?.closed, true);
  assert.ok(schedules.tasks.every((task) => task.cancelled));
});

test("watch filters lock neighbors but target and missing filenames synchronously clear and abort", async (t) => {
  const fixture = await authFixture(t);
  const watch = watchHarness();
  const cache = memoryCache();
  const pending = deferred();
  const changes = changeLog();
  let helperCalls = 0;
  /** @type {AbortSignal|undefined} */
  let activeSignal;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watch.watch.bind(watch),
    createCache: () => cache,
    async runHelper(_model, options) {
      helperCalls += 1;
      activeSignal = options.signal;
      if (helperCalls === 1) return success(ACCOUNT_A);
      return pending.promise;
    },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  void controller.settled({ ...APPLICABLE }, "assistant-1");
  await waitFor(() => helperCalls === 2, "settled helper start");
  const before = changes.length;
  watch.event("rename", "auth.json.lock");
  assert.equal(changes.length, before);
  assert.equal(activeSignal?.aborted, false);

  watch.event("change", "auth.json");
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  assert.equal(activeSignal?.aborted, true);
  assert.ok(cache.invalidations >= 1);

  const afterTarget = changes.length;
  watch.event("rename", undefined);
  assert.equal(changes.length, afterTarget + 1);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  pending.resolve(failure("timeout", ACCOUNT_A));
  controller.dispose();
});

test("watch setup and runtime errors fail closed without helper or cache reads", async (t) => {
  const fixture = await authFixture(t);
  for (const kind of ["setup", "runtime"]) {
    await t.test(kind, async () => {
      const watch = watchHarness();
      const changes = changeLog();
      const diagnostics = stringLog();
      const cache = memoryCache();
      let helperCalls = 0;
      const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
        getAgentDir: () => fixture.agent,
        watch: kind === "setup" ? () => { throw new Error("setup failed"); } : watch.watch.bind(watch),
        createCache: () => cache,
        async runHelper() { helperCalls += 1; return success(ACCOUNT_A); },
        report: (message) => diagnostics.push(message),
        schedule: scheduler().schedule,
      });
      if (kind === "runtime") {
        const start = controller.start(APPLICABLE);
        await flush();
        watch.error(new Error("watch failed"));
        await start;
      } else {
        await controller.start(APPLICABLE);
      }
      assert.equal(helperCalls, 0);
      assert.equal(cache.reads, 0);
      assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
      assert.ok(diagnostics.length >= 1);
      controller.dispose();
    });
  }
});

test("a watch event after the final auth stat result but before continuation rejects publication", async (t) => {
  const fixture = await authFixture(t);
  const watch = watchHarness();
  const cache = memoryCache();
  const changes = changeLog();
  let authStats = 0;
  let helperCalls = 0;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watch.watch.bind(watch),
    async lstat(path, options) {
      const result = await lstat(path, options);
      if (path === fixture.auth) {
        authStats += 1;
        if (authStats === 3) watch.event("change", "auth.json");
      }
      return result;
    },
    createCache: () => cache,
    async runHelper() {
      helperCalls += 1;
      return helperCalls === 1 ? success(ACCOUNT_A) : failure("auth_unavailable");
    },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  assert.ok(authStats >= 5);
  assert.equal(helperCalls, 2);
  assert.equal(cache.writes, 0);
  assert.ok(!changes.some(({ snapshot, invalidate }) => snapshot?.fetched_at === NOW && !invalidate));
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  controller.dispose();
});

test("auth metadata changing during a cache write is re-statted before publication and clears the candidate", async (t) => {
  const fixture = await authFixture(t);
  const cache = memoryCache();
  const writeStarted = deferred();
  const writeRelease = deferred();
  t.after(() => writeRelease.resolve(undefined));
  cache.write = async (candidate, current) => {
    cache.writes += 1;
    if (!current()) return false;
    cache.set(candidate);
    writeStarted.resolve(undefined);
    await writeRelease.promise;
    return current();
  };
  const changes = changeLog();
  let helperCalls = 0;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => cache,
    async runHelper() {
      helperCalls += 1;
      return helperCalls === 1 ? success(ACCOUNT_A) : failure("auth_unavailable");
    },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  t.after(() => controller.dispose());

  const started = controller.start(APPLICABLE);
  await writeStarted.promise;
  await writeFile(fixture.auth, "opaque-auth-fixture-replaced-during-cache-write\n");
  writeRelease.resolve(undefined);
  await started;
  await flush();

  assert.equal(cache.value(), undefined);
  assert.ok(!changes.some(({ snapshot, invalidate }) => snapshot?.fetched_at === NOW && !invalidate));
  assert.equal(helperCalls, 2);
  assert.ok(cache.invalidations >= 1);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
});

test("auth metadata changing during a cache read is re-statted before retained publication and clears the candidate", async (t) => {
  const fixture = await authFixture(t);
  const cached = success(ACCOUNT_A, NOW - 4 * 60_000);
  const cache = memoryCache(cached);
  const readStarted = deferred();
  const readRelease = deferred();
  t.after(() => readRelease.resolve(undefined));
  cache.read = async (current) => {
    cache.reads += 1;
    if (!current()) return { status: "unavailable" };
    readStarted.resolve(undefined);
    await readRelease.promise;
    const candidate = cache.value();
    return candidate === undefined ? { status: "missing" } : { status: "known", record: candidate };
  };
  const changes = changeLog();
  let helperCalls = 0;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => cache,
    async runHelper() {
      helperCalls += 1;
      return helperCalls === 1 ? failure("rate_limited", ACCOUNT_A) : failure("auth_unavailable");
    },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  t.after(() => controller.dispose());

  const started = controller.start(APPLICABLE);
  await readStarted.promise;
  await writeFile(fixture.auth, "opaque-auth-fixture-replaced-during-cache-read\n");
  readRelease.resolve(undefined);
  await started;
  await flush();

  assert.equal(cache.value(), undefined);
  assert.ok(!changes.some(({ snapshot, invalidate }) => snapshot?.fetched_at === cached.fetched_at && !invalidate));
  assert.equal(helperCalls, 2);
  assert.ok(cache.invalidations >= 1);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
});

test("refresh coalesces to one pending rerun and compares native source values across fresh contexts", async (t) => {
  const fixture = await authFixture(t);
  const first = deferred();
  const second = deferred();
  const plans = [first.promise, second.promise];
  let calls = 0;
  const controller = createPiQuotaController(() => {}, {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => memoryCache(),
    runHelper() { calls += 1; return plans[calls - 1] ?? Promise.resolve(success(ACCOUNT_A)); },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  const started = controller.start({ ...APPLICABLE });
  await waitFor(() => calls === 1, "initial coalesced helper start");
  void controller.model({ ...APPLICABLE });
  void controller.settled({ ...APPLICABLE }, "assistant-1");
  void controller.settled({ ...APPLICABLE }, "assistant-1");
  void controller.settled({ ...APPLICABLE }, "assistant-1");
  assert.equal(calls, 1);
  first.resolve(success(ACCOUNT_A));
  await waitFor(() => calls === 2, "single pending helper rerun");
  assert.equal(calls, 2);
  second.resolve(success(ACCOUNT_A, NOW + 1));
  await started;
  await flush();
  await controller.settled({ ...APPLICABLE }, "assistant-1");
  assert.equal(calls, 2);
  await controller.settled({ ...APPLICABLE }, "assistant-2");
  assert.equal(calls, 3);
  controller.dispose();
});

test("periodic refresh fires at exactly five minutes while applicable", async (t) => {
  const fixture = await authFixture(t);
  const schedules = scheduler();
  let helperCalls = 0;
  const controller = createPiQuotaController(() => {}, {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => memoryCache(),
    async runHelper() { helperCalls += 1; return success(ACCOUNT_A); },
    now: () => NOW,
    schedule: schedules.schedule,
  });
  await controller.start(APPLICABLE);
  const periodic = schedules.tasks[0];
  assert.equal(periodic?.delay, 5 * 60_000);
  periodic?.callback();
  await waitFor(() => helperCalls === 2, "periodic helper refresh");
  controller.dispose();
});

test("model changes and disposal abort helpers, close watches, cancel timers, and reject late results", async (t) => {
  const fixture = await authFixture(t);
  const watch = watchHarness();
  const schedules = scheduler();
  const pending = deferred();
  const changes = changeLog();
  /** @type {AbortSignal|undefined} */
  let signal;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watch.watch,
    createCache: () => memoryCache(),
    runHelper(_model, options) { signal = options.signal; return pending.promise; },
    now: () => NOW,
    schedule: schedules.schedule,
  });
  const started = controller.start(APPLICABLE);
  await waitFor(() => signal !== undefined, "lifecycle helper start");
  await controller.model({ ...APPLICABLE, provider: "anthropic" });
  assert.equal(signal?.aborted, true);
  assert.equal(watch.watcher()?.closed, true);
  assert.ok(schedules.tasks.every((task) => task.cancelled));
  assert.deepEqual(changes.at(-1), { snapshot: undefined, invalidate: true });
  pending.resolve(success(ACCOUNT_A));
  await started;
  assert.ok(!changes.some(({ snapshot }) => snapshot?.fetched_at === NOW));
  controller.dispose();
});

test("parent replacement during a helper closes the obsolete watch and rejects publication", async (t) => {
  const fixture = await authFixture(t);
  const watch = watchHarness();
  const pending = deferred();
  const changes = changeLog();
  let helperStarted = false;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watch.watch,
    createCache: () => memoryCache(),
    runHelper() { helperStarted = true; return pending.promise; },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  const started = controller.start(APPLICABLE);
  await waitFor(() => helperStarted, "parent replacement helper");
  const replaced = `${fixture.agent}.replaced`;
  await rename(fixture.agent, replaced);
  await mkdir(fixture.agent, { mode: 0o700 });
  await writeFile(fixture.auth, "opaque-auth-new-parent\n", { mode: 0o600 });
  pending.resolve(success(ACCOUNT_A));
  await started;
  assert.equal(watch.watcher()?.closed, true);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  assert.ok(!changes.some(({ snapshot }) => snapshot?.fetched_at === NOW));
  controller.dispose();
});

test("unsafe or nonregular auth files fail closed before helper and cache access", async (t) => {
  for (const kind of ["permissive", "directory"]) {
    await t.test(kind, async (t) => {
      const fixture = await authFixture(t);
      if (kind === "permissive") await chmod(fixture.auth, 0o644);
      else {
        await rm(fixture.auth);
        await mkdir(fixture.auth, { mode: 0o700 });
      }
      let helpers = 0;
      const cache = memoryCache();
      const controller = createPiQuotaController(() => {}, {
        getAgentDir: () => fixture.agent,
        watch: watchHarness().watch,
        createCache: () => cache,
        async runHelper() { helpers += 1; return success(ACCOUNT_A); },
        schedule: scheduler().schedule,
      });
      await controller.start(APPLICABLE);
      assert.equal(helpers, 0);
      assert.equal(cache.reads, 0);
      assert.equal(cache.writes, 0);
      controller.dispose();
    });
  }
});

test("an identity-known startup transient can load only a matching retained cache", async (t) => {
  const fixture = await authFixture(t);
  const cached = success(ACCOUNT_A, NOW - 4 * 60_000);
  const changes = changeLog();
  const cache = memoryCache(cached);
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => cache,
    async runHelper() { return failure("rate_limited", ACCOUNT_A); },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  assert.equal(cache.reads, 1);
  assert.deepEqual(changes.at(-1), { snapshot: { ...footer(cached), stale: true }, invalidate: false });
  controller.dispose();
});

test("same-account transient failures retain stale data until exact 15 minutes, then clear", async (t) => {
  const fixture = await authFixture(t);
  let now = NOW;
  /** @type {PiQuotaResult[]} */
  const plans = [success(ACCOUNT_A), failure("rate_limited", ACCOUNT_A)];
  const changes = changeLog();
  const schedules = scheduler();
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => memoryCache(),
    async runHelper() { return plans.shift() ?? failure("timeout", ACCOUNT_A); },
    now: () => now,
    schedule: schedules.schedule,
  });
  await controller.start(APPLICABLE);
  now = NOW + 5 * 60_000;
  await controller.settled(APPLICABLE, "assistant-1");
  assert.deepEqual(changes.at(-1), {
    snapshot: { ...footer(success(ACCOUNT_A)), stale: true },
    invalidate: false,
  });

  now = NOW + 15 * 60_000;
  const expiry = schedules.tasks.findLast((task) => task.delay === 15 * 60_000);
  assert.ok(expiry);
  expiry.callback();
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  controller.dispose();
});

test("known different-account failure clears old data before loading only its matching cache", async (t) => {
  const fixture = await authFixture(t);
  const cachedB = success(ACCOUNT_B, NOW - 6 * 60_000);
  const cache = memoryCache(cachedB);
  /** @type {PiQuotaResult[]} */
  const plans = [success(ACCOUNT_A), failure("fetch_failed", ACCOUNT_B)];
  const changes = changeLog();
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => cache,
    async runHelper() { return plans.shift() ?? failure("fetch_failed", ACCOUNT_B); },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  cache.set(cachedB);
  const boundary = changes.length;
  await controller.settled(APPLICABLE, "assistant-1");
  assert.deepEqual(changes.slice(boundary), [
    { snapshot: PI_QUOTA_UNKNOWN, invalidate: true },
    { snapshot: { ...footer(cachedB), stale: true }, invalidate: false },
  ]);
  assert.equal(cache.reads, 1);
  controller.dispose();
});

test("unknown identity never establishes cache and auth/not-applicable failures clear immediately", async (t) => {
  const fixture = await authFixture(t);
  /** @type {PiQuotaFailure["error"][]} */
  const errors = ["auth_unavailable", "auth_rejected", "not_applicable"];
  for (const error of errors) {
    await t.test(error, async () => {
      const cache = memoryCache(success(ACCOUNT_A));
      const changes = changeLog();
      const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
        getAgentDir: () => fixture.agent,
        watch: watchHarness().watch,
        createCache: () => cache,
        async runHelper() { return failure(error, error === "auth_rejected" ? ACCOUNT_A : undefined); },
        now: () => NOW,
        schedule: scheduler().schedule,
      });
      await controller.start(APPLICABLE);
      assert.equal(cache.reads, 0);
      assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
      controller.dispose();
    });
  }
});

test("freshness handles exact 5m, exact 15m, future timestamps, and missing windows", async (t) => {
  const fixture = await authFixture(t);
  let now = NOW;
  const schedules = scheduler();
  const changes = changeLog();
  /** @type {PiQuotaResult[]} */
  const plans = [
    success(ACCOUNT_A, NOW, { five_hour: null, weekly: null }),
    success(ACCOUNT_A, NOW + 1),
  ];
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => memoryCache(),
    async runHelper() { return plans.shift() ?? failure("timeout", ACCOUNT_A); },
    now: () => now,
    schedule: schedules.schedule,
  });
  await controller.start(APPLICABLE);
  assert.deepEqual(changes.at(-1)?.snapshot?.windows, { five_hour: null, weekly: null });

  now = NOW + 5 * 60_000;
  const staleDeadline = schedules.tasks.findLast((task) => task.delay === 5 * 60_000);
  assert.ok(staleDeadline);
  staleDeadline.callback();
  assert.equal(changes.at(-1)?.snapshot?.stale, true);

  now = NOW + 15 * 60_000;
  const expiry = schedules.tasks.findLast((task) => task.delay === 15 * 60_000);
  assert.ok(expiry);
  expiry.callback();
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });

  now = NOW;
  await controller.settled(APPLICABLE, "assistant-future");
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  controller.dispose();
});

test("cache construction failure is unknown with one fixed diagnostic", async (t) => {
  const fixture = await authFixture(t);
  const diagnostics = stringLog();
  const changes = changeLog();
  let helperCalls = 0;
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache() { throw new Error("synthetic cache construction details"); },
    async runHelper() { helperCalls += 1; return success(ACCOUNT_A); },
    schedule: scheduler().schedule,
    report: (diagnostic) => diagnostics.push(diagnostic),
  });
  await assert.doesNotReject(controller.start(APPLICABLE));
  assert.equal(helperCalls, 0);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
  controller.dispose();
});

test("cache operation failure is unknown with one fixed diagnostic", async (t) => {
  const fixture = await authFixture(t);
  const diagnostics = stringLog();
  const cache = memoryCache();
  cache.write = async () => false;
  const changes = changeLog();
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    watch: watchHarness().watch,
    createCache: () => cache,
    async runHelper() { return success(ACCOUNT_A); },
    report: (message) => diagnostics.push(message),
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  assert.deepEqual(changes.at(-1), { snapshot: PI_QUOTA_UNKNOWN, invalidate: true });
  assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
  controller.dispose();
});

test("quota integration documentation states producer, cache, auth-watch, and deployment boundaries", async () => {
  const [quota, footer, extension, cutover, packaging] = await Promise.all([
    readFile(new URL("../docs/pi-quota.md", import.meta.url), "utf8"),
    readFile(new URL("../docs/pi-quota-footer.md", import.meta.url), "utf8"),
    readFile(new URL("../docs/pi-extension.md", import.meta.url), "utf8"),
    readFile(new URL("../docs/steward-cutover.md", import.meta.url), "utf8"),
    readFile(new URL("../docs/packaging.md", import.meta.url), "utf8"),
  ]);
  assert.match(quota, /public.*getAuth.*credential-owning helper/is);
  assert.match(footer, /five minutes.*15 minutes.*auth.*immediate/is);
  assert.match(extension, /parent-directory.*auth\.json.*last-good/is);
  assert.match(cutover, /quota producer.*implemented.*consumer.*not.*deployed/is);
  assert.match(packaging, /default packaged helper.*both composition and\s+quota/is);
  assert.match(packaging, /quota\s+fixture.*one public.*getAuth.*one fixed.*fetch.*argv.*output/is);
  assert.match(packaging, /source integration.*cache.*credential sentinel/is);
  assert.doesNotMatch(packaging, /sentinel in argv, cache, or output/i);
});

test("real parent-directory watcher observes owned atomic auth replacement and clears", async (t) => {
  const fixture = await authFixture(t);
  const changes = changeLog();
  const controller = createPiQuotaController((snapshot, invalidate) => changes.push({ snapshot, invalidate }), {
    getAgentDir: () => fixture.agent,
    createCache: () => memoryCache(),
    async runHelper() { return success(ACCOUNT_A); },
    now: () => NOW,
    schedule: scheduler().schedule,
  });
  await controller.start(APPLICABLE);
  const source = join(fixture.agent, "auth.next");
  await writeFile(source, "opaque-auth-fixture-b-with-new-metadata\n", { mode: 0o600 });
  await rename(source, fixture.auth);
  await waitFor(
    () => changes.some(({ snapshot, invalidate }, index) => index > 1 && snapshot === PI_QUOTA_UNKNOWN && invalidate),
    "auth watcher invalidation",
  );
  controller.dispose();
});
