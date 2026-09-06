import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import process from "node:process";
import test from "node:test";

import { SessionManager } from "@earendil-works/pi-coding-agent";

import { createPiLifecycle } from "./pi-extension.mjs";
import { readPiMetadata } from "./pi-metadata.mjs";
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

/** @param {string} text @returns {import("@earendil-works/pi-ai").AssistantMessage} */
function nativeAssistantMessage(text) {
  return {
    role: "assistant",
    content: [{ type: "text", text }],
    api: "anthropic-messages",
    provider: "anthropic",
    model: "synthetic",
    usage: {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 0,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason: "stop",
    timestamp: Date.now(),
  };
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

test("bounded cleanup handles already-exited and spawn-failure children", async () => {
  const alreadyExited = observeChild(spawn(process.execPath, ["--eval", ""], { stdio: "ignore" }));
  const exited = await within(alreadyExited.completion, "already-exited child observation");
  assert.equal(exited.code, 0);
  await within(stopChild(alreadyExited), "already-exited child cleanup");

  const spawnFailure = observeChild(spawn(join(tmpdir(), `missing-steward-child-${process.pid}`), [], { stdio: "ignore" }));
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
  const suppliedCli = process.env.STEWARD_TEST_BIN;
  const cli = suppliedCli === undefined ? join(bin, "steward") : suppliedCli;
  if (suppliedCli === undefined) {
    const build = spawnSync("go", ["build", "-o", cli, "./cmd/steward"], { encoding: "utf8", timeout: 30_000 });
    assert.equal(build.status, 0, build.stderr);
  } else {
    const supplied = spawnSync(cli, ["--help"], { encoding: "utf8", timeout: 5_000 });
    assert.equal(supplied.status, 0, supplied.stderr);
    await symlink(cli, join(bin, "steward"));
  }
  const helper = join(bin, "fake-helper");
  await writeFile(helper, `#!${process.execPath}\nconst fs=require("node:fs");let s="";process.stdin.on("data",c=>s+=c);process.stdin.on("end",()=>{const i=JSON.parse(s);fs.appendFileSync(process.env.HELPER_LOG,JSON.stringify({request:i,env:{token:process.env.STEWARD_NTFY_TOKEN,tokenFile:process.env.STEWARD_NTFY_TOKEN_FILE,hooks:process.env.STEWARD_TEST,model:process.env.STEWARD_MODEL_ID,proxy:process.env.HTTPS_PROXY,credential:process.env.AWS_SECRET_ACCESS_KEY}})+"\\n");const r={version:1,ok:true,body:"GENERATED-BODY-"+i.input.assistant};if(i.label.refresh)r.label="Fresh Pi Session";process.stdout.write(JSON.stringify(r)+"\\n")});\n`);
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
    STEWARD_NTFY_TOKEN: "ambient-token-sentinel",
    STEWARD_NTFY_TOKEN_FILE: "/never-read-ambient-token-file",
    STEWARD_TEST: "ambient-hooks-sentinel",
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
    STEWARD_NTFY_URL: `http://127.0.0.1:${address.port}/topic`,
    STEWARD_HELPER_BIN: helper,
    HELPER_LOG: helperLog,
  };
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, isolatedEnv);

  const failedDaemon = observeChild(spawn(cli, ["notifyd", "--state-base", state], {
    env: { ...isolatedEnv, STEWARD_NTFY_URL: "" }, stdio: ["ignore", "ignore", "ignore"],
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
    try { await stat(join(runtime, "steward", "notifyd.sock")); return true; } catch { return false; }
  }, `notifyd socket (${daemonError})`);

  /** @type {Array<{payload: import("./pi-notify.mjs").PiNotificationPayload, promise: Promise<boolean>}>} */
  const submissions = [];
  /** @type {Array<{callback: () => void, cancelled: boolean}>} */
  const labelTasks = [];
  /** @type {Array<{kind: "native" | "shared", title: string}>} */
  const titles = [];
  const nativeSession = SessionManager.inMemory(process.cwd(), { id: "pi-session-real" });
  const projectName = basename(process.cwd());
  let activeSession = nativeSession;
  /** @type {ReturnType<typeof createPiLifecycle>} */
  let lifecycle;
  /** @type {Promise<void> | undefined} */
  let metadataHold;
  let metadataFetches = 0;
  let metadataDeliveries = 0;
  function freshContext() {
    const captured = activeSession;
    return {
      mode: /** @type {const} */ ("tui"),
      cwd: process.cwd(),
      model: undefined,
      sessionManager: captured,
      getContextUsage: () => undefined,
      ui: {
        setFooter() {},
        setTitle(/** @type {string} */ title) { titles.push({ kind: "shared", title }); },
      },
    };
  }
  const pi = {
    async exec() { return { stdout: "", stderr: "", code: 0, killed: false }; },
    setSessionName(/** @type {string} */ name) {
      activeSession.appendSessionInfo(name);
      titles.push({ kind: "native", title: name === "" ? `Pi - ${projectName}` : `Pi - ${activeSession.getSessionName()} - ${projectName}` });
      lifecycle.handlers.session_info_changed(
        { type: "session_info_changed", name: activeSession.getSessionName() },
        freshContext(),
      );
    },
    appendEntry(/** @type {string} */ type, /** @type {unknown} */ data) { activeSession.appendCustomEntry(type, data); },
  };
  lifecycle = createPiLifecycle(pi, {
    sendNotification(payload, dependencies) {
      const promise = sendPiNotification(payload, dependencies);
      submissions.push({ payload, promise });
      return promise;
    },
    async readMetadata(sessionID, options) {
      const value = await readPiMetadata(sessionID, { command: cli, stateBase: state, signal: options?.signal });
      metadataFetches += 1;
      if (metadataHold !== undefined) await metadataHold;
      metadataDeliveries += 1;
      return value;
    },
    labelSchedule(callback) {
      const task = { callback, cancelled: false };
      labelTasks.push(task);
      return () => { task.cancelled = true; };
    },
    report() {},
  });

  lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, freshContext());
  nativeSession.appendMessage({ role: "user", content: [{ type: "text", text: "integration question" }], timestamp: Date.now() });
  lifecycle.handlers.agent_start({ type: "agent_start" }, freshContext());
  nativeSession.appendMessage(nativeAssistantMessage("same source text"));
  const firstID = nativeSession.getEntries().at(-1)?.id;
  assert.equal(typeof firstID, "string");
  lifecycle.handlers.turn_end({ type: "turn_end" }, freshContext());
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, freshContext());
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, freshContext());
  lifecycle.handlers.tool_execution_end({ type: "tool_execution_end" }, freshContext());
  await waitFor(() => submissions.length === 2, "both same-ID real client submissions");
  const sameIDOutcomes = await within(Promise.all(submissions.map(({ promise }) => promise)), "same-ID real client completions");
  assert.deepEqual(sameIDOutcomes, [true, true]);
  await waitFor(() => notifications.length === 1, "one deduplicated daemon delivery");
  await waitFor(async () => (await readFile(helperLog, "utf8")).trim().split("\n").length === 1, "one helper request");
  await waitFor(() => {
    const result = spawnSync(cli, ["session-metadata", "--harness", "pi", "--session-id", "pi-session-real", "--state-base", state], { encoding: "utf8", env: isolatedEnv, timeout: 5_000 });
    return result.status === 0 && JSON.parse(result.stdout).source_generation === "1";
  }, "first real metadata publication");
  assert.equal(labelTasks.length, 6, "duplicate settled source coalesces one label schedule");
  labelTasks[0]?.callback();
  await waitFor(() => nativeSession.getSessionName() === "Fresh Pi Session", "shared label native name application");
  assert.deepEqual(titles.slice(-2), [
    { kind: "native", title: `Pi - Fresh Pi Session - ${projectName}` },
    { kind: "shared", title: "Fresh Pi Session" },
  ]);

  const generatedInfo = nativeSession.getEntries().filter((entry) => entry.type === "session_info").at(-1);
  const automaticState = nativeSession.getEntries().filter((entry) => entry.type === "custom" && entry.customType === "steward-pi-label").at(-1);
  assert.equal(generatedInfo?.type, "session_info");
  assert.equal(automaticState?.type, "custom");
  assert.deepEqual(automaticState?.data, {
    version: 1,
    ownership: "automatic",
    sessionID: nativeSession.getSessionId(),
    sessionInfoID: generatedInfo?.id,
    sourceGeneration: "1",
    labelGeneration: "1",
    label: "Fresh Pi Session",
  });

  nativeSession.appendMessage({ role: "user", content: [{ type: "text", text: "integration question" }], timestamp: Date.now() });
  lifecycle.handlers.agent_start({ type: "agent_start" }, freshContext());
  nativeSession.appendMessage(nativeAssistantMessage("same source text"));
  const secondID = nativeSession.getEntries().at(-1)?.id;
  assert.equal(typeof secondID, "string");
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, freshContext());
  await waitFor(() => submissions.length === 3, "distinct-ID real client submission");
  const distinctOutcome = await within(
    submissions[2]?.promise ?? Promise.reject(new Error("missing distinct submission")),
    "distinct-ID real client completion",
  );
  assert.equal(distinctOutcome, true);
  await waitFor(() => notifications.length === 2, "distinct-ID daemon delivery");
  await waitFor(async () => (await readFile(helperLog, "utf8")).trim().split("\n").length === 2, "distinct helper request");
  await waitFor(() => {
    const result = spawnSync(cli, ["session-metadata", "--harness", "pi", "--session-id", "pi-session-real", "--state-base", state], { encoding: "utf8", env: isolatedEnv, timeout: 5_000 });
    return result.status === 0 && JSON.parse(result.stdout).source_generation === "2";
  }, "second real metadata publication");

  let releaseMetadata = () => {};
  metadataHold = new Promise((resolve) => { releaseMetadata = resolve; });
  const fetchBaseline = metadataFetches;
  const deliveryBaseline = metadataDeliveries;
  labelTasks[6]?.callback();
  await waitFor(() => metadataFetches === fetchBaseline + 1, "actual late metadata CLI read");
  nativeSession.appendSessionInfo("Manual integration name");
  titles.push({ kind: "native", title: `Pi - Manual integration name - ${projectName}` });
  lifecycle.handlers.session_info_changed(
    { type: "session_info_changed", name: nativeSession.getSessionName() },
    freshContext(),
  );
  const replacement = SessionManager.inMemory(process.cwd(), { id: "replacement-session" });
  activeSession = replacement;
  lifecycle.handlers.session_start({ type: "session_start", reason: "resume" }, freshContext());
  releaseMetadata();
  metadataHold = undefined;
  await waitFor(() => metadataDeliveries === deliveryBaseline + 1, "late metadata delivery to the disposed UI controller");
  await waitFor(() => labelTasks.slice(6, 12).every(({ cancelled }) => cancelled), "old UI label schedule cancellation");
  assert.equal(nativeSession.getSessionName(), "Manual integration name");
  assert.equal(replacement.getSessionName(), undefined);
  assert.equal(titles.at(-1)?.title, `Pi - Manual integration name - ${projectName}`);

  const daemonExit = await stopChild(daemon);
  assert.deepEqual(daemonExit, { code: 0, signal: null }, "notifyd drains accepted work and exits successfully");

  assert.deepEqual(submissions.map(({ payload }) => payload.session_id), ["pi-session-real", "pi-session-real", "pi-session-real"]);
  assert.deepEqual(submissions.map(({ payload }) => payload.completion_id), [firstID, firstID, secondID]);
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
    completion_id: secondID,
    source_generation: "2",
    label_generation: "1",
  });
});
