import assert from "node:assert/strict";
import { lstat, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  PI_QUOTA_CACHE_FAILURE,
  createPiQuotaCache,
} from "./pi-quota-cache.mjs";

const ACCOUNT_KEY = "a".repeat(64);
const REPLACEMENT_ACCOUNT_KEY = "b".repeat(64);
const CREDENTIAL_SENTINEL = "PRIVATE-BEARER-CREDENTIAL-SENTINEL";

/** @param {number} [fetchedAt] */
function record(fetchedAt = 1_700_000_000_000) {
  return {
    version: 1,
    provider: "openai-codex",
    base_url: "https://chatgpt.com/backend-api",
    account_key: ACCOUNT_KEY,
    fetched_at: fetchedAt,
    windows: {
      five_hour: { remaining_percent: 75, reset_at: fetchedAt + 3_600_000 },
      weekly: null,
    },
  };
}

/** @template T */
function deferred() {
  /** @type {(value: T) => void} */ let resolve = () => {};
  const promise = new Promise((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

/** @param {() => boolean} predicate @param {string} label */
async function waitFor(predicate, label) {
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for ${label}`);
}

/** @param {import("node:test").TestContext} t */
async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-cache-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  /** @type {string[]} */
  const diagnostics = [];
  const cache = createPiQuotaCache(join(root, "agent", "auth.json"), {
    environment: { HOME: join(root, "home"), XDG_CACHE_HOME: join(root, "cache") },
    report: (message) => diagnostics.push(message),
  });
  return { cache, diagnostics, root };
}

test("cache writes one canonical owner-only atomic snapshot scoped by auth location", async (t) => {
  const { cache, diagnostics, root } = await fixture(t);
  assert.equal(await cache.write(record(), () => true), true);
  assert.deepEqual(await cache.read(() => true), { status: "known", record: record() });
  assert.deepEqual(diagnostics, []);

  const directory = dirname(cache.path);
  assert.equal((await lstat(directory)).mode & 0o777, 0o700);
  assert.equal((await lstat(cache.path)).mode & 0o777, 0o600);
  assert.match(cache.path, /\/steward\/pi-quota\/[a-f0-9]{64}\.json$/);
  assert.deepEqual(await readdir(directory), [cache.path.split("/").at(-1)]);
  assert.equal(await readFile(cache.path, "utf8"), `${JSON.stringify(record())}\n`);
  assert.doesNotMatch(`${cache.path}\n${await readFile(cache.path, "utf8")}`, new RegExp(CREDENTIAL_SENTINEL));
  assert.doesNotMatch(cache.path, /auth\.json/);
  assert.ok(cache.path.startsWith(join(root, "cache", "steward", "pi-quota")));
});

test("cache uses HOME/.cache only when XDG_CACHE_HOME is absent", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-cache-home-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const cache = createPiQuotaCache(join(root, "agent", "auth.json"), {
    environment: { HOME: join(root, "home") },
    report() {},
  });
  assert.ok(cache.path.startsWith(join(root, "home", ".cache", "steward", "pi-quota")));
});

test("cache rejects every noncanonical record without creating a snapshot", async (t) => {
  const invalid = [
    { ...record(), extra: true },
    { ...record(), version: 2 },
    { ...record(), provider: "openai" },
    { ...record(), base_url: "https://gateway.invalid" },
    { ...record(), account_key: "A".repeat(64) },
    { ...record(), fetched_at: 0 },
    { ...record(), windows: { five_hour: null } },
    { ...record(), windows: { ...record().windows, weekly: { remaining_percent: -1, reset_at: 1 } } },
    { ...record(), windows: { ...record().windows, weekly: { remaining_percent: 1, reset_at: -1 } } },
    { ...record(), windows: { ...record().windows, weekly: { remaining_percent: 1, reset_at: 1, extra: true } } },
  ];
  for (const candidate of invalid) {
    await t.test(JSON.stringify(candidate).slice(0, 80), async (t) => {
      const { cache, diagnostics } = await fixture(t);
      assert.equal(await cache.write(candidate, () => true), false);
      assert.deepEqual(await cache.read(() => true), { status: "missing" });
      assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
    });
  }
});

test("cache fails closed for corrupt, oversized, non-UTF8, symlink, nonregular, and permissive files", async (t) => {
  /** @type {Array<[string, Buffer]>} */
  const cases = [
    ["corrupt", Buffer.from("{}\n")],
    ["oversized", Buffer.alloc(4097, 0x20)],
    ["non-UTF8", Buffer.from([0xff])],
  ];
  for (const [name, contents] of cases) {
    await t.test(name, async (t) => {
      const { cache, diagnostics } = await fixture(t);
      await mkdir(dirname(cache.path), { recursive: true, mode: 0o700 });
      await writeFile(cache.path, contents, { mode: 0o600 });
      assert.deepEqual(await cache.read(() => true), { status: "unavailable" });
      assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
    });
  }

  await t.test("symlink", async (t) => {
    const { cache, diagnostics, root } = await fixture(t);
    await mkdir(dirname(cache.path), { recursive: true, mode: 0o700 });
    const target = join(root, "outside");
    await writeFile(target, `${JSON.stringify(record())}\n`, { mode: 0o600 });
    await symlink(target, cache.path);
    assert.deepEqual(await cache.read(() => true), { status: "unavailable" });
    assert.equal(await cache.write(record(), () => true), false);
    assert.equal(await readFile(target, "utf8"), `${JSON.stringify(record())}\n`);
    assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE, PI_QUOTA_CACHE_FAILURE]);
  });

  await t.test("nonregular", async (t) => {
    const { cache, diagnostics } = await fixture(t);
    await mkdir(cache.path, { recursive: true, mode: 0o700 });
    assert.deepEqual(await cache.read(() => true), { status: "unavailable" });
    assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
  });

  await t.test("permissive", async (t) => {
    const { cache, diagnostics } = await fixture(t);
    await mkdir(dirname(cache.path), { recursive: true, mode: 0o700 });
    await writeFile(cache.path, `${JSON.stringify(record())}\n`, { mode: 0o644 });
    assert.deepEqual(await cache.read(() => true), { status: "unavailable" });
    assert.deepEqual(diagnostics, [PI_QUOTA_CACHE_FAILURE]);
  });
});

test("cache rejects symlinked XDG, steward, and final-directory ancestry without touching outside state", async (t) => {
  for (const level of ["xdg", "steward", "pi-quota"]) {
    await t.test(level, async (t) => {
      const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-cache-ancestry-"));
      t.after(() => rm(root, { force: true, recursive: true }));
      const cacheRoot = join(root, "cache");
      const steward = join(cacheRoot, "steward");
      const quotaDirectory = join(steward, "pi-quota");
      const outside = join(root, "outside");
      const protectedFile = join(outside, "protected");
      await mkdir(outside, { mode: 0o700 });
      await writeFile(protectedFile, "outside-state\n", { mode: 0o640 });
      if (level === "xdg") await symlink(outside, cacheRoot);
      else if (level === "steward") {
        await mkdir(cacheRoot, { mode: 0o700 });
        await symlink(outside, steward);
      } else {
        await mkdir(steward, { recursive: true, mode: 0o700 });
        await symlink(outside, quotaDirectory);
      }

      /** @type {string[]} */
      const diagnostics = [];
      const cache = createPiQuotaCache(join(root, "agent", "auth.json"), {
        environment: { HOME: join(root, "home"), XDG_CACHE_HOME: cacheRoot },
        report: (message) => diagnostics.push(message),
      });
      const beforeMode = (await lstat(protectedFile)).mode & 0o777;
      assert.deepEqual(await cache.read(() => true), { status: "unavailable" });
      assert.equal(await cache.write(record(), () => true), false);
      assert.equal(await cache.invalidate(() => true), false);
      assert.deepEqual(await readdir(outside), ["protected"]);
      assert.equal(await readFile(protectedFile, "utf8"), "outside-state\n");
      assert.equal((await lstat(protectedFile)).mode & 0o777, beforeMode);
      assert.deepEqual(diagnostics, [
        PI_QUOTA_CACHE_FAILURE,
        PI_QUOTA_CACHE_FAILURE,
        PI_QUOTA_CACHE_FAILURE,
      ]);
    });
  }
});

test("cache instances share one path queue and a stale delayed close cannot erase replacement data", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-cache-race-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const environment = { HOME: join(root, "home"), XDG_CACHE_HOME: join(root, "cache") };
  const authPath = join(root, "agent", "auth.json");
  const closeRelease = deferred();
  t.after(() => closeRelease.resolve(undefined));
  let closeBlocked = false;
  const oldCache = createPiQuotaCache(authPath, {
    environment,
    filesystem: {
      async close(handle) {
        closeBlocked = true;
        await closeRelease.promise;
        await handle.close();
      },
    },
    report() {},
  });
  const replacementCache = createPiQuotaCache(authPath, { environment, report() {} });
  let oldCurrent = true;
  const oldWrite = oldCache.write(record(), () => oldCurrent);
  await waitFor(() => closeBlocked, "old cache close fence");

  oldCurrent = false;
  let replacementChecks = 0;
  const replacement = { ...record(1_700_000_000_001), account_key: REPLACEMENT_ACCOUNT_KEY };
  const replacementWrite = replacementCache.write(replacement, () => {
    replacementChecks += 1;
    return true;
  });
  await Promise.resolve();
  await Promise.resolve();
  const replacementWasQueued = replacementChecks === 0;

  closeRelease.resolve(undefined);
  assert.equal(await oldWrite, false);
  assert.equal(await replacementWrite, true);
  assert.equal(replacementWasQueued, true);
  assert.deepEqual(await replacementCache.read(() => true), { status: "known", record: replacement });
});

test("cache generation guards run across awaited work and stale writes never publish", async (t) => {
  const { cache } = await fixture(t);
  let checks = 0;
  assert.equal(await cache.write(record(), () => {
    checks += 1;
    return checks < 3;
  }), false);
  assert.ok(checks >= 3);
  assert.deepEqual(await cache.read(() => true), { status: "missing" });
});

test("serialized invalidation wins over an already queued owned write", async (t) => {
  const { cache } = await fixture(t);
  const publishing = cache.write(record(), () => true);
  const invalidating = cache.invalidate(() => true);
  assert.equal(await publishing, true);
  assert.equal(await invalidating, true);
  assert.deepEqual(await cache.read(() => true), { status: "missing" });
});
