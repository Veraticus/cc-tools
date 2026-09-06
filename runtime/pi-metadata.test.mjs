import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import { readPiMetadata } from "./pi-metadata.mjs";

const MODULE_PATH = fileURLToPath(new URL("./pi-metadata.mjs", import.meta.url));
const KNOWN = {
  version: 1,
  status: "known",
  harness: "pi",
  session_id: "session-1",
  label: "Shared useful label",
  completion_id: "assistant/branch-1",
  source_generation: "18446744073709551615",
  label_generation: "9007199254740993",
};

/** @param {unknown} response @param {{timeout?: number}} [options] */
async function readResponse(response, options = {}) {
  return readPiMetadata("session-1", {
    command: process.execPath,
    args: ["--eval", `process.stdout.write(${JSON.stringify(`${JSON.stringify(response)}\n`)})`],
    timeout: options.timeout ?? 1_000,
  });
}

/** @param {number} pid */
async function waitForExit(pid) {
  const deadline = Date.now() + 2_000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
      await new Promise((resolve) => setTimeout(resolve, 10));
    } catch (error) {
      if (error instanceof Error && "code" in error && error.code === "ESRCH") return;
      throw error;
    }
  }
  throw new Error(`metadata helper ${pid} did not exit`);
}

/** @param {string} path @param {string} description @param {number} [timeout] */
async function waitForPID(path, description, timeout = 1_800) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const pid = Number(await readFile(path, "utf8"));
      if (!Number.isSafeInteger(pid) || pid <= 0) throw new Error(`invalid ${description} pid`);
      return pid;
    } catch (error) {
      if (!(error instanceof Error && "code" in error && error.code === "ENOENT")) throw error;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }
  throw new Error(`timed out waiting for ${description} readiness`);
}

/** @param {number} pid */
async function stopPID(pid) {
  try {
    process.kill(pid, "SIGKILL");
  } catch (error) {
    if (error instanceof Error && "code" in error && error.code === "ESRCH") return;
    throw error;
  }
  await waitForExit(pid);
}

/** @param {Array<() => void | Promise<void>>} actions */
async function attemptAll(actions) {
  /** @type {unknown[]} */ const errors = [];
  for (const action of actions) {
    try {
      await action();
    } catch (error) {
      errors.push(error);
    }
  }
  if (errors.length > 0) throw new AggregateError(errors, "metadata test cleanup failed");
}

/** @param {import("node:child_process").ChildProcess} child @param {number} [timeout] */
async function childExit(child, timeout = 2_500) {
  return new Promise((resolve, reject) => {
    /** @type {NodeJS.Timeout | undefined} */ let timer;
    let done = false;
    function cleanup() {
      child.off("error", onError);
      child.off("exit", onExit);
      if (timer !== undefined) clearTimeout(timer);
    }
    /** @param {unknown} value @param {boolean} failed */
    function finish(value, failed) {
      if (done) return;
      done = true;
      cleanup();
      if (failed) reject(value);
      else resolve(value);
    }
    /** @param {Error} error */
    function onError(error) { finish(error, true); }
    /** @param {number | null} code @param {NodeJS.Signals | null} signal */
    function onExit(code, signal) { finish({ code, signal }, false); }
    child.once("error", onError);
    child.once("exit", onExit);
    timer = setTimeout(() => finish(new Error("timed out waiting for child exit"), true), timeout);
    if (child.exitCode !== null || child.signalCode !== null) onExit(child.exitCode, child.signalCode);
  });
}

/** @param {import("node:child_process").ChildProcess} child */
async function stopChild(child) {
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  await childExit(child, 2_000);
}

test("metadata reader accepts exact canonical states, uint64 strings, padded labels, and slash completion IDs", async () => {
  assert.deepEqual(await readResponse(KNOWN), {
    status: "known",
    label: "Shared useful label",
    completionID: "assistant/branch-1",
    sourceGeneration: "18446744073709551615",
    labelGeneration: "9007199254740993",
  });
  assert.deepEqual(await readResponse({ ...KNOWN, label: "  Shared useful label  " }), {
    status: "known",
    label: "  Shared useful label  ",
    completionID: "assistant/branch-1",
    sourceGeneration: "18446744073709551615",
    labelGeneration: "9007199254740993",
  });
  assert.deepEqual(await readResponse({
    ...KNOWN,
    label: "",
    label_generation: "0",
    source_generation: "1",
  }), {
    status: "known",
    label: "",
    completionID: "assistant/branch-1",
    sourceGeneration: "1",
    labelGeneration: "0",
  });
  for (const status of ["missing", "unavailable"]) {
    assert.deepEqual(await readResponse({
      version: 1,
      status,
      harness: "pi",
      session_id: "session-1",
      label: "",
      completion_id: "",
      source_generation: "0",
      label_generation: "0",
    }), {
      status,
      label: "",
      completionID: "",
      sourceGeneration: "0",
      labelGeneration: "0",
    });
  }
});

test("metadata reader rejects every malformed status, identity, generation, label, and shape boundary", async (t) => {
  /** @type {Array<[string, object]>} */
  const invalid = [
    ["extra field", { ...KNOWN, extra: true }],
    ["wrong version", { ...KNOWN, version: 2 }],
    ["wrong harness", { ...KNOWN, harness: "codex" }],
    ["wrong session", { ...KNOWN, session_id: "session-2" }],
    ["unknown status", { ...KNOWN, status: "pending" }],
    ["known zero source", { ...KNOWN, source_generation: "0" }],
    ["known empty completion", { ...KNOWN, completion_id: "" }],
    ["known controlled completion", { ...KNOWN, completion_id: "bad\nsource" }],
    ["known oversized completion", { ...KNOWN, completion_id: "x".repeat(257) }],
    ["two-word label", { ...KNOWN, label: "Only two" }],
    ["five-word label", { ...KNOWN, label: "These are five label words" }],
    ["oversized label", { ...KNOWN, label: `Three useful ${"x".repeat(50)}` }],
    ["controlled label", { ...KNOWN, label: "Three useful\u007fwords" }],
    ["empty label with generation", { ...KNOWN, label: "", label_generation: "1" }],
    ["label with zero generation", { ...KNOWN, label_generation: "0" }],
    ["label generation after source", { ...KNOWN, source_generation: "1", label_generation: "2" }],
    ["leading-zero source", { ...KNOWN, source_generation: "01" }],
    ["uint64 overflow", { ...KNOWN, source_generation: "18446744073709551616" }],
    ["missing with material", {
      ...KNOWN,
      status: "missing",
      source_generation: "0",
      label_generation: "0",
    }],
  ];
  for (const [name, response] of invalid) {
    await t.test(name, async () => {
      assert.deepEqual(await readResponse(response), { status: "unavailable" });
    });
  }
});

test("metadata reader bounds stdout before accumulation and requires successful process completion", async () => {
  const overflow = await readPiMetadata("session-1", {
    command: process.execPath,
    args: ["--eval", "process.stdout.write('x'.repeat(2049));setInterval(()=>{},1000)"],
    timeout: 1_000,
  });
  assert.deepEqual(overflow, { status: "unavailable" });

  const failed = await readPiMetadata("session-1", {
    command: process.execPath,
    args: ["--eval", `process.stdout.write(${JSON.stringify(`${JSON.stringify({ ...KNOWN, completion_id: "assistant-1" })}\n`)});process.exitCode=1`],
    timeout: 1_000,
  });
  assert.deepEqual(failed, { status: "unavailable" });

  const invalidUTF8 = Buffer.from(`${JSON.stringify({ ...KNOWN, label: "Shared x label" })}\n`);
  invalidUTF8[invalidUTF8.indexOf("x")] = 0xff;
  assert.deepEqual(await readPiMetadata("session-1", {
    command: process.execPath,
    args: ["--eval", `process.stdout.write(Buffer.from(${JSON.stringify(invalidUTF8.toString("base64"))},'base64'))`],
    timeout: 1_000,
  }), { status: "unavailable" });

  assert.deepEqual(await readPiMetadata("session-1", {
    command: resolve(tmpdir(), `missing-steward-metadata-${process.pid}`),
    timeout: 1_000,
  }), { status: "unavailable" });
});

test("timeout and abort SIGKILL a ready helper that ignores SIGTERM and release its pipe", async (t) => {
  for (const action of ["timeout", "abort"]) {
    await t.test(action, async (t) => {
      const root = await mkdtemp(join(tmpdir(), "steward-pi-metadata-stop-"));
      const pidPath = join(root, "pid");
      const controller = new AbortController();
      const result = readPiMetadata("session-1", {
        command: process.execPath,
        args: [
          "--eval",
          "require('node:fs').writeFileSync(process.argv[1],String(process.pid));process.on('SIGTERM',()=>{});setTimeout(()=>process.exit(97),4000);setInterval(()=>{},1000)",
          pidPath,
        ],
        timeout: 2_000,
        signal: controller.signal,
      });
      /** @type {number | undefined} */ let pid;
      t.after(async () => {
        controller.abort();
        await attemptAll([
          async () => { await result; },
          async () => {
            if (pid === undefined) {
              try {
                pid = Number(await readFile(pidPath, "utf8"));
              } catch (error) {
                if (!(error instanceof Error && "code" in error && error.code === "ENOENT")) throw error;
              }
            }
            if (pid !== undefined) await stopPID(pid);
          },
          () => rm(root, { recursive: true, force: true }),
        ]);
      });

      pid = await waitForPID(pidPath, `${action} helper`);
      if (action === "abort") controller.abort();
      assert.deepEqual(await result, { status: "unavailable" });
      await waitForExit(pid);
    });
  }
});

test("an unawaited metadata read does not keep an isolated UI process alive", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-metadata-unref-"));
  const pidPath = join(root, "helper.pid");
  const helperScript = "require('node:fs').writeFileSync(process.argv[1],String(process.pid));setTimeout(()=>process.exit(97),4000);setInterval(()=>{},1000)";
  const source = `import {readPiMetadata} from ${JSON.stringify(pathToFileURL(MODULE_PATH).href)};void readPiMetadata('session-1',{command:process.execPath,args:['--eval',${JSON.stringify(helperScript)},${JSON.stringify(pidPath)}],timeout:2000});`;
  const started = Date.now();
  const child = spawn(process.execPath, ["--input-type=module", "--eval", source], { stdio: "ignore" });
  /** @type {number | undefined} */ let pid;
  t.after(async () => {
    await attemptAll([
      () => stopChild(child),
      async () => {
        if (pid === undefined) pid = await waitForPID(pidPath, "cleanup metadata helper");
        await stopPID(pid);
      },
      () => rm(root, { recursive: true, force: true }),
    ]);
  });

  const outcome = await childExit(child);
  assert.deepEqual(outcome, { code: 0, signal: null });
  assert.ok(Date.now() - started < 1_000, "unreferenced read must not hold the UI process open");
  pid = await waitForPID(pidPath, "unreferenced metadata helper");
  await stopPID(pid);
});

test("metadata reader rejects invalid native session identifiers before spawning", async () => {
  for (const sessionID of [".", "bad/id", "bad\nidentifier", "x".repeat(257)]) {
    assert.deepEqual(await readPiMetadata(sessionID, {
      command: resolve(tmpdir(), `must-not-spawn-${process.pid}`),
    }), { status: "unavailable" });
  }
});
