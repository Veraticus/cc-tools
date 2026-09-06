import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import { EventEmitter } from "node:events";

import {
  PI_NOTIFY_FAILURE,
  sendPiNotification,
} from "./pi-notify.mjs";

/** @typedef {NonNullable<Parameters<typeof sendPiNotification>[1]>} NotifyDependencies */
/** @typedef {NonNullable<NotifyDependencies["spawn"]>} NotifySpawn */

const SAMPLE_PAYLOAD = Object.freeze({
  schema_version: 1,
  harness: "pi",
  session_id: "session-1",
  hook_event_name: "TurnComplete",
  completion_id: "assistant-native-id",
  cwd: process.cwd(),
  message: "question",
  last_assistant_message: "answer",
});

/** @param {string} source */
async function executable(source) {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-notify-"));
  const path = join(root, "cc-tools");
  await writeFile(path, `#!${process.execPath}\n${source}`, "utf8");
  await chmod(path, 0o755);
  return { root, path };
}

/**
 * @param {string} path
 * @param {Record<string, string>} [environment]
 */
function spawnExecutable(path, environment = {}) {
  let calls = 0;
  /** @type {NotifySpawn} */
  const spawnReplacement = (command, args, options) => {
    calls += 1;
    assert.equal(command, "cc-tools");
    assert.deepEqual(args, ["notify", "--harness", "pi"]);
    assert.equal(options.shell, false);
    assert.deepEqual(options.stdio, ["pipe", "ignore", "ignore"]);
    return spawn(path, args, {
      ...options,
      env: { ...process.env, ...environment },
    });
  };
  return { calls: () => calls, spawn: spawnReplacement };
}

class SyntheticInput extends EventEmitter {
  destroyed = false;
  unrefCalls = 0;
  /** @type {((error?: Error | null) => void) | undefined} */
  callback;

  /** @param {string} _wire @param {(error?: Error | null) => void} callback */
  end(_wire, callback) {
    this.callback = callback;
  }

  destroy() {
    this.destroyed = true;
  }

  unref() {
    this.unrefCalls += 1;
  }
}

class SyntheticChild extends EventEmitter {
  stdin = new SyntheticInput();
  killed = false;
  unrefCalls = 0;

  kill() {
    this.killed = true;
    return true;
  }

  unref() {
    this.unrefCalls += 1;
  }
}

/** @param {unknown} value @returns {value is string[]} */
function isStringArray(value) {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

/** @returns {{ diagnostics: string[], report: (message: string) => void }} */
function diagnosticRecorder() {
  /** @type {string[]} */
  const diagnostics = [];
  return { diagnostics, report: (message) => diagnostics.push(message) };
}

test("sender invokes exactly cc-tools notify --harness pi and writes canonical JSON only to stdin", async (t) => {
  const outputRoot = await mkdtemp(join(tmpdir(), "steward-pi-notify-output-"));
  const output = join(outputRoot, "capture.json");
  const fake = await executable(`
const { writeFileSync } = require("node:fs");
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  writeFileSync(process.env.STEWARD_CAPTURE, JSON.stringify({ argv: process.argv.slice(2), input }));
});
`);
  t.after(() => Promise.all([
    rm(fake.root, { force: true, recursive: true }),
    rm(outputRoot, { force: true, recursive: true }),
  ]));
  const harness = spawnExecutable(fake.path, { STEWARD_CAPTURE: output });
  const diagnostics = diagnosticRecorder();

  assert.equal(await sendPiNotification(SAMPLE_PAYLOAD, {
    spawn: harness.spawn,
    report: diagnostics.report,
  }), true);
  assert.equal(harness.calls(), 1);
  assert.deepEqual(diagnostics.diagnostics, []);

  /** @type {unknown} */
  const captured = JSON.parse(await readFile(output, "utf8"));
  assert.ok(typeof captured === "object" && captured !== null && !Array.isArray(captured));
  const argv = Reflect.get(captured, "argv");
  const input = Reflect.get(captured, "input");
  assert.ok(isStringArray(argv));
  assert.equal(typeof input, "string");
  assert.deepEqual(argv, ["notify", "--harness", "pi"]);
  assert.equal(input, `${JSON.stringify(SAMPLE_PAYLOAD)}\n`);
  assert.ok(!argv.some((argument) => argument.includes("session-1")));
});

test("sender starts one process and never retries a nonzero exit", async (t) => {
  const fake = await executable("process.exitCode = 23;\n");
  t.after(() => rm(fake.root, { force: true, recursive: true }));
  const harness = spawnExecutable(fake.path);
  const diagnostics = diagnosticRecorder();

  assert.equal(await sendPiNotification(SAMPLE_PAYLOAD, {
    spawn: harness.spawn,
    report: diagnostics.report,
  }), false);
  assert.equal(harness.calls(), 1);
  assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
});

test("sender maps missing executable, broken stdin, and synchronous spawn failures to one fixed diagnostic", async (t) => {
  await t.test("missing executable", async () => {
    const diagnostics = diagnosticRecorder();
    let calls = 0;
    const ok = await sendPiNotification(SAMPLE_PAYLOAD, {
      spawn(command, args, options) {
        calls += 1;
        return spawn(command, args, {
          ...options,
          env: { ...process.env, PATH: "" },
        });
      },
      report: diagnostics.report,
    });
    assert.equal(ok, false);
    assert.equal(calls, 1);
    assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
  });

  await t.test("broken stdin", async (t) => {
    const fake = await executable("setInterval(() => {}, 1000);\n");
    t.after(() => rm(fake.root, { force: true, recursive: true }));
    const diagnostics = diagnosticRecorder();
    const secret = "source text must not leak";
    let calls = 0;
    /** @type {NotifySpawn} */
    const brokenSpawn = (_command, args, options) => {
      calls += 1;
      const child = spawn(fake.path, args, options);
      child.stdin.destroy();
      return child;
    };
    const pending = sendPiNotification(
      { ...SAMPLE_PAYLOAD, message: secret },
      { spawn: brokenSpawn, report: diagnostics.report },
    );
    assert.equal(await pending, false);
    assert.equal(calls, 1);
    assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
    assert.doesNotMatch(diagnostics.diagnostics.join(" "), /source text must not leak/);
  });

  await t.test("synchronous spawn failure", async () => {
    const diagnostics = diagnosticRecorder();
    assert.equal(await sendPiNotification(SAMPLE_PAYLOAD, {
      spawn() {
        throw new Error("source text must not leak");
      },
      report: diagnostics.report,
    }), false);
    assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
  });
});

test("sender requires zero exit and successful stdin completion in either order", async (t) => {
  await t.test("stdin then exit", async () => {
    const child = new SyntheticChild();
    const pending = sendPiNotification(SAMPLE_PAYLOAD, { spawn: () => child, report() {} });
    child.stdin.callback?.();
    child.emit("exit", 0, null);
    assert.equal(await pending, true);
  });

  await t.test("exit then stdin", async () => {
    const child = new SyntheticChild();
    const pending = sendPiNotification(SAMPLE_PAYLOAD, { spawn: () => child, report() {} });
    child.emit("exit", 0, null);
    child.stdin.callback?.();
    assert.equal(await pending, true);
  });
});

test("sender treats a stdin callback error after zero exit as failure", async () => {
  const child = new SyntheticChild();
  const diagnostics = diagnosticRecorder();
  const pending = sendPiNotification(SAMPLE_PAYLOAD, { spawn: () => child, report: diagnostics.report });
  child.emit("exit", 0, null);
  child.stdin.callback?.(new Error("late write failure"));
  assert.equal(await pending, false);
  assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
});

test("sender enforces its deadline and bounds cleanup even when process and stdin stall", async () => {
  const diagnostics = diagnosticRecorder();
  const child = new SyntheticChild();
  const started = Date.now();

  assert.equal(await sendPiNotification(SAMPLE_PAYLOAD, {
    spawn: () => child,
    timeoutMs: 25,
    report: diagnostics.report,
  }), false);
  assert.ok(Date.now() - started < 500, "stalled transport must settle promptly");
  assert.equal(child.killed, true);
  assert.equal(child.unrefCalls, 1);
  assert.equal(child.stdin.unrefCalls, 1);
  assert.equal(child.stdin.destroyed, true);
  assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
});

test("sender kills an actual timed-out child once without exposing its output", async (t) => {
  const fake = await executable(`
process.stdout.write("private stdout");
process.stderr.write("private stderr");
setInterval(() => {}, 1000);
`);
  t.after(() => rm(fake.root, { force: true, recursive: true }));
  const harness = spawnExecutable(fake.path);
  const diagnostics = diagnosticRecorder();
  const started = Date.now();

  assert.equal(await sendPiNotification(SAMPLE_PAYLOAD, {
    spawn: harness.spawn,
    timeoutMs: 50,
    report: diagnostics.report,
  }), false);
  assert.ok(Date.now() - started < 1000);
  assert.equal(harness.calls(), 1);
  assert.deepEqual(diagnostics.diagnostics, [PI_NOTIFY_FAILURE]);
});

test("fire-and-forget notification handles do not keep a quitting Pi process alive", async (t) => {
  const fake = await executable("setTimeout(() => {}, 1500);\n");
  t.after(() => rm(fake.root, { force: true, recursive: true }));
  const entry = `
import { sendPiNotification } from ${JSON.stringify(pathToFileURL(join(import.meta.dirname, "pi-notify.mjs")).href)};
void sendPiNotification(${JSON.stringify(SAMPLE_PAYLOAD)}, { report() {} });
`;
  const started = Date.now();
  const result = spawnSync(process.execPath, ["--input-type=module", "--eval", entry], {
    encoding: "utf8",
    env: {
      ...process.env,
      PATH: `${fake.root}${delimiter}${process.env.PATH ?? ""}`,
    },
    timeout: 1000,
  });

  assert.equal(result.error, undefined);
  assert.equal(result.signal, null);
  assert.equal(result.status, 0, result.stderr);
  assert.ok(Date.now() - started < 1000, "submitted client must not keep the UI process alive");
});
