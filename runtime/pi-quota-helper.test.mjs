import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { EventEmitter } from "node:events";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import { runPiQuotaHelper } from "./pi-quota-helper.mjs";

const MODEL = Object.freeze({
  provider: "openai-codex",
  id: "gpt-5.6-luna",
  baseUrl: "https://chatgpt.com/backend-api",
});
const ACCOUNT_KEY = "b".repeat(64);
const SUCCESS = Object.freeze({
  version: 1,
  ok: true,
  provider: "openai-codex",
  base_url: "https://chatgpt.com/backend-api",
  account_key: ACCOUNT_KEY,
  fetched_at: 1_700_000_000_000,
  windows: {
    five_hour: { remaining_percent: 75, reset_at: 1_700_003_600_000 },
    weekly: null,
  },
});

/** @param {string} source */
async function executable(source) {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-quota-helper-"));
  const path = join(root, "steward-pi-helper");
  await writeFile(path, `#!${process.execPath}\n${source}`, "utf8");
  await chmod(path, 0o755);
  return { root, path };
}

/** @param {string} path @param {Record<string, string>} [environment] @returns {{calls: Array<{command: string,args: string[]}>, spawn: import("./pi-quota-helper.mjs").PiQuotaSpawn}} */
function executableSpawn(path, environment = {}) {
  /** @type {Array<{command: string,args: string[]}>} */
  const calls = [];
  return {
    calls,
    spawn(command, args, options) {
      calls.push({ command, args });
      assert.deepEqual(options.stdio, ["pipe", "pipe", "ignore"]);
      assert.equal(options.shell, false);
      return spawn(path, args, { ...options, env: { ...process.env, ...environment } });
    },
  };
}

class SyntheticInput extends EventEmitter {
  destroyed = false;
  unrefCalls = 0;
  /** @type {((error?: Error | null) => void) | undefined} */ callback;
  /** @param {string} _wire @param {(error?: Error | null) => void} callback */
  end(_wire, callback) { this.callback = callback; }
  destroy() { this.destroyed = true; }
  unref() { this.unrefCalls += 1; }
}

class SyntheticOutput extends EventEmitter {
  destroyed = false;
  unrefCalls = 0;
  destroy() { this.destroyed = true; }
  unref() { this.unrefCalls += 1; }
}

class SyntheticChild extends EventEmitter {
  stdin = new SyntheticInput();
  stdout = new SyntheticOutput();
  exitCode = null;
  signalCode = null;
  killed = false;
  unrefCalls = 0;
  kill() { this.killed = true; return true; }
  unref() { this.unrefCalls += 1; }
}

test("helper transport writes the exact quota request only to stdin and returns strict success", async (t) => {
  const captureRoot = await mkdtemp(join(tmpdir(), "steward-pi-quota-capture-"));
  const capture = join(captureRoot, "capture.json");
  const fake = await executable(`
const fs=require("node:fs");let input=[];process.stdin.on("data",c=>input.push(c));process.stdin.on("end",()=>{const wire=Buffer.concat(input).toString("utf8");fs.writeFileSync(process.env.CAPTURE,JSON.stringify({argv:process.argv.slice(2),wire}));process.stdout.write(${JSON.stringify(`${JSON.stringify(SUCCESS)}\n`)});});
`);
  t.after(() => Promise.all([
    rm(fake.root, { force: true, recursive: true }),
    rm(captureRoot, { force: true, recursive: true }),
  ]));
  const harness = executableSpawn(fake.path, { CAPTURE: capture });
  assert.deepEqual(await runPiQuotaHelper(MODEL, { spawn: harness.spawn }), SUCCESS);
  assert.deepEqual(harness.calls, [{ command: "steward-pi-helper", args: [] }]);
  assert.deepEqual(JSON.parse(await readFile(capture, "utf8")), {
    argv: [],
    wire: `${JSON.stringify({ version: 1, operation: "quota", model: {
      provider: MODEL.provider,
      id: MODEL.id,
      base_url: MODEL.baseUrl,
    } })}\n`,
  });
});

test("helper binary uses absent-only default and preserves an explicitly empty override", async () => {
  /** @type {string[]} */
  const commands = [];
  assert.deepEqual(await runPiQuotaHelper(MODEL, {
    environment: { STEWARD_HELPER_BIN: "" },
    spawn(command) {
      commands.push(command);
      throw new Error("synthetic empty command failure");
    },
  }), { version: 1, ok: false, error: "fetch_failed" });
  assert.deepEqual(commands, [""]);
});

test("helper accepts all eight exact protocol failures and identity only as a complete tuple", async (t) => {
  /** @type {Array<import("./pi-quota-helper.mjs").PiQuotaFailure["error"]>} */
  const errors = ["invalid_request", "not_applicable", "auth_unavailable", "auth_rejected", "rate_limited", "fetch_failed", "invalid_response", "timeout"];
  for (const error of errors) {
    await t.test(error, async () => {
      const child = new SyntheticChild();
      const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
      child.stdin.callback?.();
      child.stdout.emit("data", Buffer.from(`${JSON.stringify({ version: 1, ok: false, error })}\n`));
      child.emit("close", 1, null);
      assert.deepEqual(await pending, { version: 1, ok: false, error });
    });
  }

  const child = new SyntheticChild();
  const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
  child.stdin.callback?.();
  child.stdout.emit("data", Buffer.from(`${JSON.stringify({ version: 1, ok: false, error: "rate_limited", provider: "openai-codex" })}\n`));
  child.emit("close", 1, null);
  assert.deepEqual(await pending, { version: 1, ok: false, error: "fetch_failed" });
});

test("helper requires stdout, successful stdin callback, and matching close status in either order", async (t) => {
  await t.test("stdout and stdin then close", async () => {
    const child = new SyntheticChild();
    const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
    child.stdout.emit("data", Buffer.from(`${JSON.stringify(SUCCESS)}\n`));
    child.stdin.callback?.();
    child.emit("close", 0, null);
    assert.deepEqual(await pending, SUCCESS);
  });
  await t.test("close then stdin", async () => {
    const child = new SyntheticChild();
    const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
    child.stdout.emit("data", Buffer.from(`${JSON.stringify(SUCCESS)}\n`));
    child.emit("close", 0, null);
    child.stdin.callback?.();
    assert.deepEqual(await pending, SUCCESS);
  });
  await t.test("stdin error after close", async () => {
    const child = new SyntheticChild();
    const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
    child.stdout.emit("data", Buffer.from(`${JSON.stringify(SUCCESS)}\n`));
    child.emit("close", 0, null);
    child.stdin.callback?.(new Error("write failed"));
    assert.deepEqual(await pending, { version: 1, ok: false, error: "fetch_failed" });
  });
  await t.test("nonzero success output", async () => {
    const child = new SyntheticChild();
    const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
    child.stdin.callback?.();
    child.stdout.emit("data", Buffer.from(`${JSON.stringify(SUCCESS)}\n`));
    child.emit("close", 1, null);
    assert.deepEqual(await pending, { version: 1, ok: false, error: "fetch_failed" });
  });
});

test("helper rejects overflow before accumulation, malformed schema, non-UTF8, and multiple values", async (t) => {
  const outputs = [
    Buffer.alloc(4097, 0x20),
    Buffer.from("{}\n"),
    Buffer.from([0xff]),
    Buffer.from(`${JSON.stringify(SUCCESS)}\n${JSON.stringify(SUCCESS)}\n`),
    Buffer.from(`${JSON.stringify({ ...SUCCESS, extra: true })}\n`),
    Buffer.from(`${JSON.stringify({ ...SUCCESS, windows: { five_hour: null } })}\n`),
  ];
  for (const output of outputs) {
    await t.test(`bytes-${output.byteLength}`, async () => {
      const child = new SyntheticChild();
      const pending = runPiQuotaHelper(MODEL, { spawn: () => child });
      child.stdout.emit("data", output);
      if (output.byteLength <= 4096) {
        child.stdin.callback?.();
        child.emit("close", 0, null);
      }
      assert.deepEqual(await pending, { version: 1, ok: false, error: "fetch_failed" });
      if (output.byteLength > 4096) assert.equal(child.killed, true);
    });
  }
});

test("helper timeout and caller abort kill and unref every owned handle", async (t) => {
  await t.test("timeout", async () => {
    const child = new SyntheticChild();
    assert.deepEqual(await runPiQuotaHelper(MODEL, { spawn: () => child, timeoutMs: 10 }), {
      version: 1, ok: false, error: "timeout",
    });
    assert.equal(child.killed, true);
    assert.equal(child.unrefCalls, 1);
    assert.equal(child.stdin.unrefCalls, 1);
    assert.equal(child.stdout.unrefCalls, 1);
    assert.equal(child.stdin.destroyed, true);
    assert.equal(child.stdout.destroyed, true);
  });
  await t.test("abort", async () => {
    const child = new SyntheticChild();
    const controller = new AbortController();
    const pending = runPiQuotaHelper(MODEL, { spawn: () => child, signal: controller.signal });
    controller.abort();
    assert.deepEqual(await pending, { version: 1, ok: false, error: "timeout" });
    assert.equal(child.killed, true);
  });
});

test("an uncooperative helper cannot keep a finished parent process alive", async (t) => {
  const fake = await executable("process.stdin.resume();setTimeout(()=>{},1500);\n");
  t.after(() => rm(fake.root, { force: true, recursive: true }));
  const entry = `import { runPiQuotaHelper } from ${JSON.stringify(pathToFileURL(join(import.meta.dirname, "pi-quota-helper.mjs")).href)};void runPiQuotaHelper(${JSON.stringify(MODEL)});`;
  const result = spawnSync(process.execPath, ["--input-type=module", "--eval", entry], {
    encoding: "utf8",
    env: { ...process.env, PATH: `${fake.root}${delimiter}${process.env.PATH ?? ""}` },
    timeout: 1000,
  });
  assert.equal(result.error, undefined);
  assert.equal(result.signal, null);
  assert.equal(result.status, 0, result.stderr);
});
