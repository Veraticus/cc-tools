import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import process from "node:process";
import test from "node:test";

import { installFooter } from "./pi-extension.mjs";
import { createPiQuotaController } from "./pi-quota.mjs";
import { runPiQuotaHelper } from "./pi-quota-helper.mjs";

/** @typedef {Parameters<typeof installFooter>[0]} FooterExec */
/** @typedef {Parameters<typeof installFooter>[1]} AdapterContext */
/** @typedef {Awaited<ReturnType<FooterExec["exec"]>>} FooterExecResult */
/** @typedef {{promise: Promise<FooterExecResult>, resolve: (value: FooterExecResult) => void}} DeferredExec */
/** @typedef {import("./pi-quota.mjs").PiQuotaFooterSnapshot} PiQuotaFooterSnapshot */

const CREDENTIAL_SENTINEL = "SYNTHETIC-QUOTA-CREDENTIAL-SENTINEL";

/** @template T */
function deferred() {
  /** @type {(value: T) => void} */ let resolve = () => {};
  const promise = new Promise((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

/** @param {() => boolean | Promise<boolean>} predicate @param {string} label */
async function waitFor(predicate, label) {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`timed out waiting for ${label}`);
}

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

function uiHarness() {
  /** @type {{render(width: number): string[], invalidate(): void, dispose?(): void}|undefined} */
  let component;
  /** @type {AdapterContext["ui"]} */
  const ui = {
    setTitle() {},
    setFooter(factory) {
      component?.dispose?.();
      component = factory?.(
        { requestRender() {} },
        {},
        { onBranchChange() { return () => {}; } },
      );
    },
  };
  return { ui, component: () => component };
}

/** @param {ReturnType<typeof uiHarness>["ui"]} ui @param {string} cwd @returns {AdapterContext} */
function context(ui, cwd) {
  return {
    mode: "tui",
    cwd,
    model: {
      provider: "openai-codex",
      id: "gpt-5.6-luna",
      name: "GPT-5.6 Luna",
      api: "openai-codex-responses",
      baseUrl: "https://chatgpt.com/backend-api",
      contextWindow: 200_000,
    },
    thinkingLevel: "high",
    sessionManager: {
      getSessionId: () => "synthetic-quota-session",
      getBranch: () => [],
      getEntries: () => [],
    },
    getContextUsage: () => ({ tokens: 1, contextWindow: 200_000, percent: 1 }),
    ui,
  };
}

/** @param {string} value */
function visible(value) {
  return value.replace(/\x1b\[[0-9;]*m/g, "");
}

test("production watch/cache/helper feeds the actual Go footer and fences auth races", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-integration-"));
  const bin = join(root, "bin");
  const home = join(root, "home");
  const agent = join(root, "agent");
  const cacheRoot = join(root, "cache");
  const marker = join(root, "quota-marker.jsonl");
  const release = join(root, "release");
  const auth = join(agent, "auth.json");
  const previous = { ...process.env };
  t.after(async () => {
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, previous);
    await rm(root, { force: true, recursive: true });
  });
  await Promise.all([
    mkdir(bin, { recursive: true }),
    mkdir(home, { recursive: true }),
    mkdir(agent, { recursive: true, mode: 0o700 }),
  ]);
  await writeFile(auth, "opaque-auth-revision-one\n", { mode: 0o600 });

  const steward = process.env.STEWARD_TEST_BIN ?? join(bin, "steward");
  if (process.env.STEWARD_TEST_BIN === undefined) {
    const build = spawnSync("go", ["build", "-o", steward, "./cmd/steward"], {
      encoding: "utf8", timeout: 30_000,
    });
    assert.equal(build.status, 0, build.stderr);
  }
  const helper = join(bin, "steward-pi-helper");
  await writeFile(helper, `#!${process.execPath}\nimport { runCliEntrypoint } from ${JSON.stringify(new URL("./cli.mjs", import.meta.url).href)};\nawait runCliEntrypoint();\n`, "utf8");
  await chmod(helper, 0o755);

  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, {
    HOME: home,
    PI_CODING_AGENT_DIR: agent,
    XDG_CACHE_HOME: cacheRoot,
    PATH: `${bin}${delimiter}/usr/bin:/bin`,
    NODE_OPTIONS: `--import ${new URL("./pi-quota-model.mjs", import.meta.url).pathname}`,
    STEWARD_QUOTA_FIXTURE_MARKER: marker,
    STEWARD_STATUSLINE_GCLOUD: "/dev/null",
    STEWARD_STATUSLINE_KUBECONFIG: "/dev/null",
    STEWARD_STATUSLINE_HOSTNAME: "quota-fixture",
  });

  const harness = uiHarness();
  const ctx = context(harness.ui, root);
  /** @type {ReturnType<typeof installFooter> | undefined} */
  let footer;
  /** @type {PiQuotaFooterSnapshot} */
  let latest;
  let invalidations = 0;
  let clearedSynchronously = false;
  /** @type {string[]} */
  const helperArgv = [];
  /** @type {string[]} */
  const rendererArgv = [];
  let holdRenderer = false;
  /** @type {DeferredExec} */
  const oldRenderer = deferred();
  /** @type {FooterExec} */
  const pi = {
    async exec(command, args) {
      assert.equal(command, "steward");
      rendererArgv.push(...args);
      if (holdRenderer) {
        holdRenderer = false;
        return oldRenderer.promise;
      }
      const result = spawnSync(steward, args, { cwd: root, encoding: "utf8", env: process.env, timeout: 5_000 });
      return {
        stdout: result.stdout,
        stderr: result.stderr,
        code: result.status ?? 1,
        killed: result.signal !== null,
      };
    },
  };
  const controller = createPiQuotaController((snapshot, invalidate) => {
    latest = snapshot;
    footer?.updateQuota(snapshot, invalidate);
    if (invalidate) {
      invalidations += 1;
      clearedSynchronously ||= harness.component()?.render(40)[0] === "";
    }
  }, {
    runHelper(model, options) {
      return runPiQuotaHelper(model, {
        ...options,
        spawn(command, args, spawnOptions) {
          helperArgv.push(...args);
          return spawn(command, args, spawnOptions);
        },
      });
    },
  });
  t.after(() => { controller.dispose(); footer?.dispose(); });

  await controller.start(ctx.model);
  footer = installFooter(pi, ctx, latest);
  for (const width of [40, 60, 80, 120]) {
    harness.component()?.render(width);
    await waitFor(() => {
      const line = harness.component()?.render(width)[0] ?? "";
      return visible(line).includes("5h75%") && visible(line).includes("7d20%") && visible(line).includes("fresh");
    }, `quota footer width ${width}`);
  }

  const cacheDirectory = join(cacheRoot, "steward", "pi-quota");
  const cacheFiles = await readdir(cacheDirectory);
  assert.equal(cacheFiles.length, 1);
  const cacheWire = await readFile(join(cacheDirectory, cacheFiles[0]), "utf8");
  assert.doesNotMatch(cacheWire, new RegExp(CREDENTIAL_SENTINEL));
  assert.doesNotMatch(JSON.stringify({ latest, helperArgv, rendererArgv }), new RegExp(CREDENTIAL_SENTINEL));
  assert.deepEqual(helperArgv, []);

  holdRenderer = true;
  footer.update(ctx);
  await flush();
  process.env.STEWARD_QUOTA_FIXTURE_WAIT_FILE = release;
  void controller.settled(ctx.model, "assistant-race-source");
  await waitFor(async () => {
    try { return (await readFile(marker, "utf8")).includes('"phase":"started"'); }
    catch { return false; }
  }, "old helper start");
  delete process.env.STEWARD_QUOTA_FIXTURE_WAIT_FILE;
  const replacement = join(agent, "auth.next");
  await writeFile(replacement, "opaque-auth-revision-two-with-different-size\n", { mode: 0o600 });
  await rename(replacement, auth);
  await waitFor(() => invalidations > 0 && clearedSynchronously, "synchronous auth clear");
  assert.deepEqual(harness.component()?.render(40), [""]);

  oldRenderer.resolve({ stdout: "OLD-ACCOUNT-QUOTA\n", stderr: "", code: 0, killed: false });
  await writeFile(release, "release\n");
  await waitFor(() => {
    const line = harness.component()?.render(40)[0] ?? "";
    return line !== "" && (visible(line).includes("5h") || visible(line).includes("quota")) && !line.includes("OLD-ACCOUNT-QUOTA");
  }, "post-auth unknown or refreshed footer");
  assert.doesNotMatch(harness.component()?.render(40)[0] ?? "", /OLD-ACCOUNT-QUOTA/);

  const observations = (await readFile(marker, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
  const completed = observations.filter((entry) => entry.phase === "complete");
  assert.ok(completed.length >= 1);
  for (const entry of completed) {
    assert.equal(entry.getAuthCalls, 1);
    assert.equal(entry.fetchCalls, 1);
    assert.equal(entry.authorizationMatches, true);
    assert.equal(entry.accountMatches, true);
    assert.doesNotMatch(JSON.stringify(entry), new RegExp(CREDENTIAL_SENTINEL));
  }
});
