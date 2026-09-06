import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import process from "node:process";
import test from "node:test";

import { createPiLifecycle } from "./pi-extension.mjs";
import { sendPiNotification } from "./pi-notify.mjs";

/** @param {() => boolean | Promise<boolean>} predicate @param {string} description */
async function waitFor(predicate, description) {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error(`timed out waiting for ${description}`);
}

/** @template T @param {Promise<T>} promise @param {string} description @returns {Promise<T>} */
async function within(promise, description) {
  /** @type {NodeJS.Timeout | undefined} */
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`timed out waiting for ${description}`)), 5_000); }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

/** @param {Array<() => void | Promise<void>>} actions */
async function attemptAll(actions) {
  /** @type {unknown[]} */
  const errors = [];
  for (const action of actions) {
    try {
      await action();
    } catch (error) {
      errors.push(error);
    }
  }
  if (errors.length > 0) throw new AggregateError(errors, "resource cleanup failed");
}

/** @param {import("node:child_process").ChildProcess} child */
function observeChild(child) {
  /** @type {(value: {code: number | null, signal: NodeJS.Signals | null}) => void} */
  let resolveExit = () => {};
  const completion = new Promise((resolve) => { resolveExit = resolve; });
  child.once("exit", (code, signal) => resolveExit({ code, signal }));
  child.once("error", () => resolveExit({ code: child.exitCode, signal: child.signalCode }));
  if (child.exitCode !== null) resolveExit({ code: child.exitCode, signal: child.signalCode });
  return { child, completion };
}

/** @param {{child: import("node:child_process").ChildProcess, completion: Promise<{code: number | null, signal: NodeJS.Signals | null}>}} watched */
async function stopChild(watched) {
  if (watched.child.exitCode === null) watched.child.kill("SIGTERM");
  try {
    return await within(watched.completion, "child SIGTERM exit");
  } catch {
    if (watched.child.exitCode === null) watched.child.kill("SIGKILL");
    return await within(watched.completion, "child SIGKILL exit");
  }
}

/** @param {string} sessionID @param {string} assistantID @param {string} text */
function context(sessionID, assistantID, text) {
  return {
    mode: /** @type {const} */ ("tui"),
    cwd: process.cwd(),
    model: undefined,
    sessionManager: {
      getSessionId: () => sessionID,
      getBranch: () => [
        { type: "message", id: `user-${assistantID}`, message: { role: "user", content: [{ type: "text", text: "integration question" }] } },
        { type: "message", id: assistantID, message: { role: "assistant", content: [{ type: "text", text }] } },
      ],
    },
    getContextUsage: () => undefined,
    ui: { setFooter() {} },
  };
}

test("bounded cleanup handles already-exited and spawn-failure children", async () => {
  const alreadyExited = observeChild(spawn(process.execPath, ["--eval", ""], { stdio: "ignore" }));
  const exited = await within(alreadyExited.completion, "already-exited child observation");
  assert.equal(exited.code, 0);
  await within(stopChild(alreadyExited), "already-exited child cleanup");

  const spawnFailure = observeChild(spawn(join(tmpdir(), `missing-cc-tools-child-${process.pid}`), [], { stdio: "ignore" }));
  const failed = await within(spawnFailure.completion, "spawn-failure child observation");
  assert.notEqual(failed.code, 0);
  await within(stopChild(spawnFailure), "spawn-failure child cleanup");
});

test("cleanup attempts every owned resource after an earlier failure", async () => {
  /** @type {string[]} */
  const attempts = [];
  await assert.rejects(
    attemptAll([
      async () => { attempts.push("first child"); throw new Error("simulated stop failure"); },
      async () => { attempts.push("second child"); },
      async () => { attempts.push("server sockets"); },
    ]),
    AggregateError,
  );
  assert.deepEqual(attempts, ["first child", "second child", "server sockets"]);
});

test("real Pi lifecycle traverses CLI, notifyd, composer, and loopback sender", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-integration-"));
  const bin = join(root, "bin");
  const home = join(root, "home");
  const runtime = join(root, "runtime");
  const state = join(root, "state");
  const helperLog = join(root, "helper.log");
  const previous = { ...process.env };
  /** @type {Array<ReturnType<typeof observeChild>>} */
  const ownedChildren = [];
  /** @type {Set<import("node:net").Socket>} */
  const connections = new Set();
  /** @type {import("node:http").Server | undefined} */
  let server;

  t.after(async () => {
    try {
      await attemptAll([
        ...ownedChildren.map((watched) => async () => { await stopChild(watched); }),
        async () => {
          const ownedServer = server;
          if (ownedServer === undefined) return;
          /** @type {Promise<Error | undefined>} */
          const closure = new Promise((resolve) => {
            try {
              ownedServer.close((error) => resolve(error));
            } catch (error) {
              resolve(error instanceof Error ? error : new Error("HTTP server close failed"));
            }
          });
          await attemptAll([
            ...Array.from(connections, (connection) => async () => { connection.destroy(); }),
            async () => {
              const error = await within(closure, "HTTP server close");
              if (error !== undefined) throw error;
            },
          ]);
        },
      ]);
    } finally {
      await attemptAll([
        () => {
          for (const key of Object.keys(process.env)) delete process.env[key];
          Object.assign(process.env, previous);
        },
        async () => { await within(rm(root, { recursive: true, force: true }), "temporary directory removal"); },
      ]);
    }
  });

  await Promise.all([mkdir(bin), mkdir(home), mkdir(runtime), mkdir(state)]);
  const cli = join(bin, "cc-tools");
  const build = spawnSync("go", ["build", "-o", cli, "./cmd/cc-tools"], { encoding: "utf8", timeout: 30_000 });
  assert.equal(build.status, 0, build.stderr);
  const helper = join(bin, "fake-helper");
  await writeFile(helper, `#!${process.execPath}\nconst fs=require("node:fs");let s="";process.stdin.on("data",c=>s+=c);process.stdin.on("end",()=>{const i=JSON.parse(s);fs.appendFileSync(process.env.HELPER_LOG,JSON.stringify({request:i,env:{token:process.env.CC_TOOLS_NTFY_TOKEN,tokenFile:process.env.CC_TOOLS_NTFY_TOKEN_FILE,hooks:process.env.CLAUDE_HOOKS_TEST,model:process.env.STEWARD_MODEL_ID,proxy:process.env.HTTPS_PROXY,credential:process.env.AWS_SECRET_ACCESS_KEY}})+"\\n");const r={version:1,ok:true,body:"GENERATED-BODY-"+i.input.assistant};if(i.label.refresh)r.label="Fresh Pi Session";process.stdout.write(JSON.stringify(r)+"\\n")});\n`);
  await chmod(helper, 0o755);

  /** @type {Array<{body: string, headers: import("node:http").IncomingHttpHeaders}>} */
  const notifications = [];
  server = createServer((request, response) => {
    let body = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => { body += chunk; });
    request.on("end", () => { notifications.push({ body, headers: request.headers }); response.writeHead(200).end(); });
  });
  server.on("connection", (connection) => {
    connections.add(connection);
    connection.once("close", () => connections.delete(connection));
  });
  const listeningServer = server;
  /** @type {Promise<void>} */
  const listening = new Promise((resolve, reject) => {
    /** @type {(error: Error) => void} */
    const onError = (error) => reject(error);
    listeningServer.once("error", onError);
    listeningServer.listen(0, "127.0.0.1", () => {
      listeningServer.off("error", onError);
      resolve();
    });
  });
  await within(listening, "HTTP server listen");
  const address = listeningServer.address();
  assert.ok(address && typeof address === "object");

  Object.assign(process.env, {
    CC_TOOLS_NTFY_TOKEN: "ambient-token-sentinel",
    CC_TOOLS_NTFY_TOKEN_FILE: "/never-read-ambient-token-file",
    CLAUDE_HOOKS_TEST: "ambient-hooks-sentinel",
    STEWARD_MODEL_ID: "ambient-model-sentinel",
    HTTPS_PROXY: "http://ambient-proxy.invalid",
    AWS_SECRET_ACCESS_KEY: "ambient-credential-sentinel",
  });
  const isolatedEnv = {
    HOME: home,
    XDG_CACHE_HOME: join(home, ".cache"),
    XDG_CONFIG_HOME: join(home, ".config"),
    XDG_DATA_HOME: join(home, ".local", "share"),
    XDG_RUNTIME_DIR: runtime,
    PATH: bin,
    CC_TOOLS_NTFY_URL: `http://127.0.0.1:${address.port}/topic`,
    STEWARD_HELPER_BIN: helper,
    HELPER_LOG: helperLog,
  };
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, isolatedEnv);

  const failedDaemon = observeChild(spawn(cli, ["notifyd", "--state-base", state], {
    env: { ...isolatedEnv, CC_TOOLS_NTFY_URL: "" }, stdio: ["ignore", "ignore", "ignore"],
  }));
  ownedChildren.push(failedDaemon);
  const failedExit = await within(failedDaemon.completion, "failed daemon startup");
  assert.equal(failedExit.code, 1, "failed daemon startup is observed without waiting for a later exit handler");
  await stopChild(failedDaemon);

  const daemon = observeChild(spawn(cli, ["notifyd", "--state-base", state], {
    env: isolatedEnv, stdio: ["ignore", "ignore", "pipe"],
  }));
  ownedChildren.push(daemon);
  let daemonError = "";
  daemon.child.stderr?.setEncoding("utf8").on("data", (chunk) => { daemonError += chunk; });
  await waitFor(async () => {
    try { await stat(join(runtime, "cc-tools", "notifyd.sock")); return true; } catch { return false; }
  }, `notifyd socket (${daemonError})`);

  /** @type {Array<{payload: import("./pi-notify.mjs").PiNotificationPayload, promise: Promise<boolean>}>} */
  const submissions = [];
  const lifecycle = createPiLifecycle(
    { async exec() { return { stdout: "", stderr: "", code: 0, killed: false }; } },
    {
      sendNotification(payload, dependencies) {
        const promise = sendPiNotification(payload, dependencies);
        submissions.push({ payload, promise });
        return promise;
      },
      report() {},
    },
  );
  const first = context("pi-session-real", "assistant-id-1", "same source text");
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, first);
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, first);
  lifecycle.handlers.turn_end({ type: "turn_end" }, first);
  lifecycle.handlers.tool_execution_end({ type: "tool_execution_end" }, first);
  await waitFor(() => submissions.length === 2, "both same-ID real client submissions");
  const sameIDOutcomes = await within(Promise.all(submissions.map(({ promise }) => promise)), "same-ID real client completions");
  assert.deepEqual(sameIDOutcomes, [true, true]);
  await waitFor(() => notifications.length === 1, "one deduplicated daemon delivery");
  await waitFor(async () => (await readFile(helperLog, "utf8")).trim().split("\n").length === 1, "one helper request");

  lifecycle.handlers.agent_settled(
    { type: "agent_settled" },
    context("pi-session-real", "assistant-id-2", "same source text"),
  );
  await waitFor(() => submissions.length === 3, "distinct-ID real client submission");
  const distinctOutcome = await within(
    submissions[2]?.promise ?? Promise.reject(new Error("missing distinct submission")),
    "distinct-ID real client completion",
  );
  assert.equal(distinctOutcome, true);
  await waitFor(() => notifications.length === 2, "distinct-ID daemon delivery");
  await waitFor(async () => (await readFile(helperLog, "utf8")).trim().split("\n").length === 2, "distinct helper request");

  const daemonExit = await stopChild(daemon);
  assert.deepEqual(daemonExit, { code: 0, signal: null }, "notifyd drains accepted work and exits successfully");

  assert.deepEqual(submissions.map(({ payload }) => payload.session_id), ["pi-session-real", "pi-session-real", "pi-session-real"]);
  assert.deepEqual(submissions.map(({ payload }) => payload.completion_id), ["assistant-id-1", "assistant-id-1", "assistant-id-2"]);
  assert.deepEqual(submissions.map(({ payload }) => payload.last_assistant_message), ["same source text", "same source text", "same source text"]);
  assert.equal(notifications.length, 2);
  assert.equal(notifications[0]?.body, "GENERATED-BODY-same source text");
  assert.equal(notifications[1]?.body, "GENERATED-BODY-same source text");
  for (const notification of notifications) {
    assert.equal(notification.headers["x-access-token"], undefined);
    assert.equal(notification.headers.authorization, undefined);
    assert.doesNotMatch(JSON.stringify(notification.headers), /ambient-(?:token|hooks|model|credential)|never-read|proxy/i);
  }

  const helperLines = (await readFile(helperLog, "utf8")).trim().split("\n").map((line) => JSON.parse(line));
  assert.equal(helperLines.length, 2);
  for (const helperLine of helperLines) {
    assert.deepEqual(helperLine.request.model, { provider: "openai-codex", id: "gpt-5.6-luna", thinking: "low" });
    assert.deepEqual(helperLine.request.input, { user: "integration question", assistant: "same source text" });
    assert.deepEqual(helperLine.env, {});
  }
  assert.deepEqual(helperLines.map((line) => line.request.label), [
    { current: "", refresh: true },
    { current: "Fresh Pi Session", refresh: false },
  ]);

  const metadata = spawnSync(cli, ["session-metadata", "--harness", "pi", "--session-id", "pi-session-real", "--state-base", state], {
    encoding: "utf8", env: isolatedEnv, timeout: 5_000,
  });
  assert.equal(metadata.status, 0, metadata.stderr);
  assert.deepEqual(JSON.parse(metadata.stdout), {
    version: 1,
    status: "known",
    harness: "pi",
    session_id: "pi-session-real",
    label: "Fresh Pi Session",
    completion_id: "assistant-id-2",
    source_generation: "2",
    label_generation: "1",
  });
});
