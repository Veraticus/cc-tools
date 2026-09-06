import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { SessionManager } from "@earendil-works/pi-coding-agent";

import {
  PI_IDENTITY_DIAGNOSTIC,
  capturePiSettledNotification,
  createPiLifecycle,
  initializePiExtension,
  installFooter,
  statuslinePayload,
} from "./pi-extension.mjs";

const REPOSITORY_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");

/** @typedef {Parameters<typeof statuslinePayload>[0]} AdapterContext */
/** @typedef {ReturnType<AdapterContext["sessionManager"]["getBranch"]>[number]} AdapterEntry */
/** @typedef {Parameters<typeof installFooter>[0]} FooterExec */
/** @typedef {import("./pi-quota.mjs").PiQuotaSnapshot} PiQuotaSnapshot */
/** @typedef {Awaited<ReturnType<FooterExec["exec"]>>} FooterExecResult */
/** @typedef {{promise: Promise<FooterExecResult>, resolve: (value: FooterExecResult) => void, reject: (error: Error) => void}} DeferredExec */
/** @typedef {{promise: Promise<boolean>, resolve: (value: boolean) => void, reject: (error: Error) => void}} DeferredBoolean */

/** @param {number} cost @param {string} [text] @param {string} [id] */
function assistant(cost, text = "answer", id = `assistant-${cost}`) {
  return {
    type: "message",
    id,
    message: {
      role: "assistant",
      content: [{ type: "text", text }],
      usage: { cost: { total: cost } },
    },
  };
}

/** @param {string} [text] @param {string} [id] */
function user(text = "question", id = `user-${text}`) {
  return {
    type: "message",
    id,
    message: { role: "user", content: [{ type: "text", text }] },
  };
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

/** @param {string} id */
function metadata(id) {
  return { type: "custom", id };
}

function makeUIHarness() {
  /** @type {{render(width: number): string[], invalidate(): void, dispose?(): void} | undefined} */
  let component;
  /** @type {(() => void) | undefined} */
  let branchChange;
  let renders = 0;
  let unsubscribeCalls = 0;
  let clearCalls = 0;

  /** @type {AdapterContext["ui"]} */
  const ui = {
    setTitle(_title) {},
    setFooter(factory) {
      component?.dispose?.();
      if (factory === undefined) {
        clearCalls += 1;
        component = undefined;
        return;
      }
      component = factory(
        { requestRender: () => { renders += 1; } },
        {},
        {
          onBranchChange(callback) {
            branchChange = callback;
            let subscribed = true;
            return () => {
              if (!subscribed) return;
              subscribed = false;
              unsubscribeCalls += 1;
              branchChange = undefined;
            };
          },
        },
      );
    },
  };

  return {
    ui,
    component: () => component,
    branchChange: () => branchChange?.(),
    renders: () => renders,
    unsubscribeCalls: () => unsubscribeCalls,
    clearCalls: () => clearCalls,
  };
}

/**
 * @param {{
 *   mode?: "tui" | "rpc" | "json" | "print",
 *   cwd?: string,
 *   sessionId?: string,
 *   leafId?: string | null,
 *   branch?: AdapterEntry[],
 *   ui?: AdapterContext["ui"],
 *   model?: {id: string, name: string, provider: string, contextWindow: number, api?: string, baseUrl?: string} | undefined,
 *   thinkingLevel?: string,
 *   contextUsage?: {tokens: number | null, contextWindow: number, percent: number | null} | undefined,
 * }} [overrides]
 * @returns {AdapterContext}
 */
function makeContext(overrides = {}) {
  const branch = overrides.branch ?? [user(), assistant(0.25), assistant(0.5, "final answer")];
  const sessionManager = {
    getSessionId: () => overrides.sessionId ?? "session-1",
    getLeafId: () => overrides.leafId ?? "leaf-1",
    getEntries: () => branch,
    getBranch: () => branch,
  };
  return {
    mode: overrides.mode ?? "tui",
    cwd: overrides.cwd ?? "/work/project",
    model: overrides.model === undefined && !("model" in overrides)
      ? {
          id: "gpt-5.6",
          name: "GPT-5.6",
          provider: "openai-codex",
          contextWindow: 200_000,
        }
      : overrides.model,
    thinkingLevel: overrides.thinkingLevel ?? "high",
    sessionManager,
    getContextUsage: () => overrides.contextUsage ?? {
      tokens: 75_000,
      contextWindow: 200_000,
      percent: 37.5,
    },
    ui: overrides.ui ?? makeUIHarness().ui,
  };
}

/** @template T */
function deferred() {
  /** @type {(value: T) => void} */
  let resolve = () => {};
  /** @type {(error: Error) => void} */
  let reject = () => {};
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

/** @param {string} stdout @param {number} [code] */
function execResult(stdout, code = 0) {
  return { stdout, stderr: "", code, killed: false };
}

test("package declares the pinned subagents bridge before the owned Pi adapter", async () => {
  const manifest = JSON.parse(await readFile(join(REPOSITORY_ROOT, "package.json"), "utf8"));
  assert.deepEqual(manifest.pi?.extensions, [
    "runtime/pi-subagents.mjs",
    "runtime/pi-extension.mjs",
  ]);
});

test("construction classifies exactly once, suppresses children before registration, and propagates coupling failures", () => {
  let classifications = 0;
  let registrations = 0;
  assert.equal(initializePiExtension(
    () => {
      classifications += 1;
      return true;
    },
    () => {
      registrations += 1;
    },
  ), true);
  assert.equal(classifications, 1);
  assert.equal(registrations, 0);

  assert.equal(initializePiExtension(
    () => {
      classifications += 1;
      return false;
    },
    () => {
      registrations += 1;
    },
  ), false);
  assert.equal(classifications, 2);
  assert.equal(registrations, 1);

  const couplingFailure = new Error("coupling failure");
  assert.throws(
    () => initializePiExtension(() => { throw couplingFailure; }, () => { registrations += 1; }),
    (error) => error === couplingFailure,
  );
  assert.equal(registrations, 1);
});

test("statusline payload preserves the existing schema and maps native Pi state", () => {
  const payload = JSON.parse(statuslinePayload(makeContext(), 120));
  assert.deepEqual(payload, {
    harness: "pi",
    columns: 120,
    session_id: "session-1",
    model: {
      id: "gpt-5.6",
      provider: "openai-codex",
      display_name: "GPT-5.6",
    },
    cost: { total_cost_usd: 0.75 },
    context_window: {
      used_percentage: 37.5,
      context_window_size: 200_000,
    },
    workspace: {
      project_dir: "/work/project",
      current_dir: "/work/project",
      cwd: "/work/project",
    },
    cwd: "/work/project",
    effort: { level: "high" },
  });
});

test("statusline payload includes only the renderer quota snapshot and never account identity", () => {
  /** @type {PiQuotaSnapshot} */
  const quota = {
    provider: "openai-codex",
    base_url: "https://chatgpt.com/backend-api",
    fetched_at: 1_700_000_000_000,
    stale: false,
    windows: { five_hour: { remaining_percent: 75, reset_at: 1_700_003_600_000 }, weekly: null },
  };
  const payload = JSON.parse(statuslinePayload(makeContext(), 120, quota));
  assert.deepEqual(payload.steward_quota, quota);
  assert.doesNotMatch(JSON.stringify(payload), /account_key|bearer|credential/i);
  const absent = JSON.parse(statuslinePayload(makeContext(), 120));
  assert.equal(Object.hasOwn(absent, "steward_quota"), false);
});

test("footer renders cached output and coalesces width and branch refreshes", async () => {
  const harness = makeUIHarness();
  /** @type {Array<{command: string, args: string[], options: {cwd?: string, timeout?: number, signal?: AbortSignal}}>} */
  const calls = [];
  /** @type {FooterExec} */
  const pi = {
    async exec(command, args, options = {}) {
      calls.push({ command, args, options });
      const payload = JSON.parse(args[1] ?? "null");
      return execResult(`statusline-${payload.columns}\n`);
    },
  };
  const ctx = makeContext({ ui: harness.ui });
  const controller = installFooter(pi, ctx);

  assert.deepEqual(harness.component()?.render(100), [""]);
  await flush();
  assert.equal(calls[0]?.command, "steward");
  assert.equal(calls[0]?.args[0], "statusline");
  assert.equal(calls[0]?.options.cwd, "/work/project");
  assert.equal(calls[0]?.options.timeout, 5_000);
  assert.ok(calls[0]?.options.signal instanceof AbortSignal);
  assert.deepEqual(harness.component()?.render(100), ["statusline-100"]);

  harness.component()?.render(80);
  harness.branchChange();
  harness.branchChange();
  await flush();
  assert.equal(calls.length, 2, "overlapping width and branch changes should coalesce");
  assert.deepEqual(harness.component()?.render(80), ["statusline-80"]);

  controller.dispose();
});

test("footer rejects each context, width, and branch generation race", async (t) => {
  /** @type {Array<"context" | "width" | "branch">} */
  const races = ["context", "width", "branch"];
  for (const race of races) {
    await t.test(race, async () => {
      const harness = makeUIHarness();
      /** @type {DeferredExec} */
      const first = deferred();
      /** @type {DeferredExec} */
      const second = deferred();
      const results = [first, second];
      /** @type {string[]} */
      const calls = [];
      /** @type {FooterExec} */
      const pi = {
        exec(_command, args) {
          const payload = JSON.parse(args[1] ?? "null");
          calls.push(`${payload.model.id}:${payload.columns}`);
          return results[calls.length - 1]?.promise ?? Promise.resolve(execResult("unexpected"));
        },
      };
      const controller = installFooter(pi, makeContext({ ui: harness.ui }));
      let width = 100;

      harness.component()?.render(width);
      await Promise.resolve();
      if (race === "context") {
        controller.update(makeContext({
          ui: harness.ui,
          model: {
            id: "claude-opus-5",
            name: "Claude Opus 5",
            provider: "anthropic",
            contextWindow: 1_000_000,
          },
        }));
      } else if (race === "width") {
        width = 80;
        harness.component()?.render(width);
      } else {
        harness.branchChange();
      }
      assert.deepEqual(calls, ["gpt-5.6:100"]);

      first.resolve(execResult("stale\n"));
      await flush();
      assert.equal(calls.length, 2);
      assert.deepEqual(harness.component()?.render(width), [""]);

      second.resolve(execResult("current\n"));
      await flush();
      assert.deepEqual(harness.component()?.render(width), ["current"]);
      controller.dispose();
    });
  }
});

test("quota invalidation synchronously erases a last-good line and rejects an old renderer result", async () => {
  const harness = makeUIHarness();
  /** @type {DeferredExec} */
  const oldAccountRender = deferred();
  let calls = 0;
  /** @type {FooterExec} */
  const pi = {
    exec(_command, args) {
      calls += 1;
      if (calls === 1) return Promise.resolve(execResult("old-account-quota\n"));
      if (calls === 2) return oldAccountRender.promise;
      const payload = JSON.parse(args[1] ?? "null");
      assert.equal(payload.steward_quota.fetched_at, 0);
      return Promise.resolve(execResult("explicit-unknown\n"));
    },
  };
  const controller = installFooter(pi, makeContext({ ui: harness.ui }), {
    provider: "openai-codex",
    base_url: "https://chatgpt.com/backend-api",
    fetched_at: 1_700_000_000_000,
    stale: false,
    windows: { five_hour: null, weekly: null },
  });
  harness.component()?.render(100);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["old-account-quota"]);

  controller.update(makeContext({ ui: harness.ui }));
  await Promise.resolve();
  controller.updateQuota({
    provider: "openai-codex",
    base_url: "https://chatgpt.com/backend-api",
    fetched_at: 0,
    stale: false,
    windows: { five_hour: null, weekly: null },
  }, true);
  assert.deepEqual(harness.component()?.render(100), [""]);
  oldAccountRender.resolve(execResult("late-old-account-quota\n"));
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["explicit-unknown"]);
  controller.dispose();
});

test("footer keeps its last good line and retries after rejection and nonzero exit", async () => {
  const harness = makeUIHarness();
  let call = 0;
  /** @type {FooterExec} */
  const pi = {
    async exec() {
      call += 1;
      if (call === 2) throw new Error("private spawn failure");
      if (call === 3) return execResult("must-not-render\n", 1);
      return execResult(call === 1 ? "first\n" : "recovered\n");
    },
  };
  const ctx = makeContext({ ui: harness.ui });
  const controller = installFooter(pi, ctx);

  harness.component()?.render(100);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["first"]);

  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["first"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["first"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["recovered"]);
  controller.dispose();
});

test("footer ignores newline-only success and accepts a later valid refresh", async () => {
  const harness = makeUIHarness();
  const outputs = ["old\n", "\r\n\n", "later\n"];
  const pi = { async exec() { return execResult(outputs.shift() ?? "unexpected\n"); } };
  const ctx = makeContext({ ui: harness.ui });
  const controller = installFooter(pi, ctx);

  harness.component()?.render(100);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["old"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["old"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["later"]);
  controller.dispose();
});

test("footer keeps good output when a timed-out zero-exit result is killed, then refreshes", async () => {
  const harness = makeUIHarness();
  const results = [
    execResult("good\n"),
    { ...execResult("partial\n"), killed: true },
    execResult("next\n"),
  ];
  const pi = { async exec() { return results.shift() ?? execResult("unexpected\n"); } };
  const ctx = makeContext({ ui: harness.ui });
  const controller = installFooter(pi, ctx);

  harness.component()?.render(100);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["good"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["good"]);
  controller.update(ctx);
  await flush();
  assert.deepEqual(harness.component()?.render(100), ["next"]);
  controller.dispose();
});

test("footer disposal unsubscribes and aborts exactly once with no late apply or render", async () => {
  const harness = makeUIHarness();
  /** @type {DeferredExec} */
  const pending = deferred();
  /** @type {AbortSignal | undefined} */
  let signal;
  /** @type {FooterExec} */
  const pi = {
    exec(_command, _args, options = {}) {
      signal = options.signal;
      return pending.promise;
    },
  };
  const controller = installFooter(pi, makeContext({ ui: harness.ui }));
  harness.component()?.render(100);
  await Promise.resolve();
  assert.equal(signal?.aborted, false);
  const rendersBefore = harness.renders();

  controller.dispose();
  controller.dispose();
  assert.equal(signal?.aborted, true);
  assert.equal(harness.unsubscribeCalls(), 1);
  assert.equal(harness.clearCalls(), 1);
  pending.resolve(execResult("late\n"));
  await flush();
  assert.equal(harness.renders(), rendersBefore);
  assert.equal(harness.component(), undefined);
});

test("settled snapshot uses the native assistant entry id, not a custom leaf", () => {
  const result = capturePiSettledNotification(makeContext({
    leafId: "custom-leaf-must-not-be-used",
    branch: [
      user("question"),
      assistant(0.5, "answer", "native-assistant-id"),
      metadata("custom-leaf-must-not-be-used"),
    ],
  }));
  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.deepEqual(result.payload, {
    schema_version: 1,
    harness: "pi",
    session_id: "session-1",
    hook_event_name: "TurnComplete",
    completion_id: "native-assistant-id",
    cwd: "/work/project",
    message: "question",
    last_assistant_message: "answer",
  });
  assert.equal(Object.isFrozen(result.payload), true);
});

test("settled snapshot keeps an empty latest assistant and never borrows an older identity or text", () => {
  const result = capturePiSettledNotification(makeContext({
    branch: [
      user("old question"),
      assistant(1, "old answer", "old-assistant"),
      user("latest question"),
      assistant(2, "", "latest-assistant"),
      metadata("metadata-leaf"),
    ],
  }));
  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.equal(result.payload.completion_id, "latest-assistant");
  assert.equal(result.payload.message, "latest question");
  assert.equal(result.payload.last_assistant_message, "");
});

test("settled snapshot selects only text blocks from the latest preceding user and assistant", () => {
  const branch = [
    user("older user"),
    {
      type: "message",
      id: "selected-user",
      message: {
        role: "user",
        content: [
          { type: "image", data: "private-image" },
          { type: "text", text: "selected" },
          { type: "text", text: "user" },
        ],
      },
    },
    metadata("between"),
    {
      type: "message",
      id: "selected-assistant",
      message: {
        role: "assistant",
        content: [
          { type: "thinking", thinking: "private thinking" },
          { type: "toolCall", id: "tool", name: "read", arguments: { secret: true } },
          { type: "text", text: "selected" },
          { type: "text", text: "assistant" },
        ],
        usage: { cost: { total: 0 } },
      },
    },
  ];
  const result = capturePiSettledNotification(makeContext({ branch }));
  assert.equal(result.ok, true);
  if (!result.ok) return;
  assert.equal(result.payload.message, "selected\nuser");
  assert.equal(result.payload.last_assistant_message, "selected\nassistant");
  assert.doesNotMatch(JSON.stringify(result.payload), /private|secret|toolCall|thinking/);
});

test("settled identity accepts 256 UTF-8 bytes and rejects 257 bytes or controls with one fixed diagnostic", () => {
  const valid = capturePiSettledNotification(makeContext({
    branch: [user(), assistant(0, "answer", "é".repeat(128))],
  }));
  assert.equal(valid.ok, true);

  for (const id of ["é".repeat(128) + "x", "bad\nid"]) {
    const invalid = capturePiSettledNotification(makeContext({
      branch: [user(), assistant(0, "answer", id)],
    }));
    assert.deepEqual(invalid, { ok: false, diagnostic: PI_IDENTITY_DIAGNOSTIC });
  }

  const missing = capturePiSettledNotification(makeContext({ branch: [user()] }));
  assert.deepEqual(missing, { ok: false, diagnostic: PI_IDENTITY_DIAGNOSTIC });
});

test("settled text applies the Go 8192-byte reservation, donation, and rune-safe tail rules", () => {
  const equal = capturePiSettledNotification(makeContext({
    branch: [
      user("é".repeat(5_000)),
      assistant(0, "界".repeat(3_000), "assistant-id"),
    ],
  }));
  assert.equal(equal.ok, true);
  if (!equal.ok) return;
  assert.equal(Buffer.byteLength(equal.payload.message), 4_096);
  assert.equal(Buffer.byteLength(equal.payload.last_assistant_message), 4_095);
  assert.ok(Buffer.from(equal.payload.message).toString("utf8") === equal.payload.message);
  assert.ok(Buffer.from(equal.payload.last_assistant_message).toString("utf8") === equal.payload.last_assistant_message);

  const donated = capturePiSettledNotification(makeContext({
    branch: [
      user("u".repeat(100)),
      assistant(0, "a".repeat(9_000), "assistant-id"),
    ],
  }));
  assert.equal(donated.ok, true);
  if (!donated.ok) return;
  assert.equal(Buffer.byteLength(donated.payload.message), 100);
  assert.equal(Buffer.byteLength(donated.payload.last_assistant_message), 8_092);
  assert.equal(
    Buffer.byteLength(donated.payload.message) + Buffer.byteLength(donated.payload.last_assistant_message),
    8_192,
  );
});

test("root lifecycle installs and notifies only in TUI and only on agent_settled", async () => {
  /** @type {AdapterContext["mode"][]} */
  const silentModes = ["rpc", "json", "print"];
  for (const mode of silentModes) {
    const harness = makeUIHarness();
    let notifications = 0;
    const lifecycle = createPiLifecycle(
      { async exec() { return execResult(""); } },
      {
        async sendNotification() { notifications += 1; return true; },
        report() {},
      },
    );
    const ctx = makeContext({ mode, ui: harness.ui });
    lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, ctx);
    lifecycle.handlers.agent_settled({ type: "agent_settled" }, ctx);
    await flush();
    assert.equal(harness.component(), undefined, mode);
    assert.equal(notifications, 0, mode);
  }

  const harness = makeUIHarness();
  /** @type {Array<object>} */
  const payloads = [];
  const lifecycle = createPiLifecycle(
    { async exec() { return execResult("line\n"); } },
    {
      async sendNotification(payload) { payloads.push(payload); return true; },
      report() {},
    },
  );
  const ctx = makeContext({ ui: harness.ui });
  lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, ctx);
  for (const handler of [
    lifecycle.handlers.agent_start,
    lifecycle.handlers.turn_end,
    lifecycle.handlers.tool_execution_end,
    lifecycle.handlers.model_select,
    lifecycle.handlers.thinking_level_select,
    lifecycle.handlers.session_compact,
    lifecycle.handlers.session_tree,
  ]) {
    handler({}, ctx);
  }
  assert.equal(payloads.length, 0);
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, ctx);
  await flush();
  assert.equal(payloads.length, 1);
  lifecycle.handlers.session_shutdown({ type: "session_shutdown", reason: "quit" }, ctx);
  assert.equal(harness.component(), undefined);
});

test("non-TUI lifecycle never constructs a quota producer", () => {
  /** @type {Array<"rpc"|"json"|"print">} */
  const modes = ["rpc", "json", "print"];
  for (const mode of modes) {
    let constructions = 0;
    const lifecycle = createPiLifecycle(
      { async exec() { return execResult(""); } },
      {
        createQuota() {
          constructions += 1;
          throw new Error("quota must remain TUI-only");
        },
        report() {},
      },
    );
    lifecycle.handlers.session_start(
      { type: "session_start", reason: "startup" },
      makeContext({ mode }),
    );
    assert.equal(constructions, 0, mode);
  }
});

test("root TUI quota lifecycle follows model and unique settled sources across fresh contexts", async () => {
  const harness = makeUIHarness();
  /** @type {Array<[string, string|undefined, string?]>} */
  const calls = [];
  let disposed = 0;
  const lifecycle = createPiLifecycle(
    { async exec() { return execResult("line\n"); } },
    {
      async sendNotification() { return true; },
      createQuota(onChange) {
        onChange(undefined, true);
        return {
          start(model) { calls.push(["start", model?.id]); return Promise.resolve(); },
          model(model) { calls.push(["model", model?.id]); return Promise.resolve(); },
          settled(model, source) { calls.push(["settled", model?.id, source]); return Promise.resolve(); },
          dispose() { disposed += 1; },
        };
      },
      report() {},
    },
  );
  const model = {
    id: "gpt-5.6-luna",
    name: "GPT-5.6 Luna",
    provider: "openai-codex",
    api: "openai-codex-responses",
    baseUrl: "https://chatgpt.com/backend-api",
    contextWindow: 200_000,
  };
  const first = makeContext({ ui: harness.ui, model });
  lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, first);
  lifecycle.handlers.model_select({}, makeContext({ ui: harness.ui, model: { ...model } }));
  lifecycle.handlers.agent_settled({}, makeContext({
    ui: harness.ui,
    model: { ...model },
    branch: [user("quota question"), assistant(0, "quota answer", "quota-source")],
  }));
  await flush();
  assert.deepEqual(calls, [
    ["start", "gpt-5.6-luna"],
    ["model", "gpt-5.6-luna"],
    ["settled", "gpt-5.6-luna", "quota-source"],
  ]);
  lifecycle.handlers.session_shutdown({ type: "session_shutdown", reason: "quit" }, first);
  assert.equal(disposed, 1);
});

test("real SessionManager lifecycle accepts fresh event contexts after agent activity and tree invalidation", async () => {
  const sessionManager = SessionManager.inMemory("/work/project", { id: "native-lifecycle-session" });
  const uiHarness = makeUIHarness();
  /** @type {string[]} */
  const titles = [];
  const ui = {
    ...uiHarness.ui,
    setTitle(/** @type {string} */ title) { titles.push(title); },
  };
  /** @type {Array<{callback: () => void, cancelled: boolean}>} */
  const scheduled = [];
  /** @type {ReturnType<typeof createPiLifecycle>} */
  let lifecycle;
  const pi = {
    async exec() { return execResult(""); },
    setSessionName(/** @type {string} */ name) {
      sessionManager.appendSessionInfo(name);
      lifecycle.handlers.session_info_changed(
        { type: "session_info_changed", name: sessionManager.getSessionName() },
        freshContext(),
      );
    },
    appendEntry(/** @type {string} */ type, /** @type {unknown} */ data) { sessionManager.appendCustomEntry(type, data); },
  };
  function freshContext() {
    return {
      ...makeContext({ ui }),
      sessionManager,
    };
  }
  /** @type {object[]} */
  const notifications = [];
  let expectedSource = "";
  lifecycle = createPiLifecycle(pi, {
    async sendNotification(payload) { notifications.push(payload); return true; },
    async readMetadata() {
      return {
        status: /** @type {const} */ ("known"),
        label: "Lifecycle shared label",
        completionID: expectedSource,
        sourceGeneration: "1",
        labelGeneration: "1",
      };
    },
    labelSchedule(callback) {
      const task = { callback, cancelled: false };
      scheduled.push(task);
      return () => { task.cancelled = true; };
    },
    report() {},
  });

  lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, freshContext());
  sessionManager.appendMessage({ role: "user", content: [{ type: "text", text: "question" }], timestamp: Date.now() });
  lifecycle.handlers.agent_start({ type: "agent_start" }, freshContext());
  sessionManager.appendMessage(nativeAssistantMessage("answer"));
  const assistantEntry = sessionManager.getEntries().at(-1);
  assert.equal(assistantEntry?.type, "message");
  if (assistantEntry?.type !== "message") return;
  expectedSource = assistantEntry.id;
  lifecycle.handlers.turn_end({ type: "turn_end" }, freshContext());
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, freshContext());
  assert.equal(scheduled.length, 6);
  scheduled[0]?.callback();
  await flush();
  assert.equal(sessionManager.getSessionName(), "Lifecycle shared label");
  assert.deepEqual(titles, ["Lifecycle shared label"]);
  assert.equal(notifications.length, 1);

  lifecycle.handlers.session_tree({ type: "session_tree" }, freshContext());
  assert.ok(scheduled.every(({ cancelled }) => cancelled));
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, freshContext());
  assert.equal(scheduled.length, 12);
  scheduled[6]?.callback();
  await flush();
  assert.equal(notifications.length, 2);
});

test("settled captures immutable notification identity before synchronous naming-side mutation", async () => {
  const sessionManager = SessionManager.inMemory("/work/project", { id: "capture-before-label-session" });
  sessionManager.appendMessage({ role: "user", content: [{ type: "text", text: "original question" }], timestamp: Date.now() });
  sessionManager.appendMessage(nativeAssistantMessage("original answer"));
  const source = sessionManager.getEntries().at(-1);
  assert.equal(source?.type, "message");
  if (source?.type !== "message") return;
  /** @type {object | undefined} */
  let submitted;
  const lifecycle = createPiLifecycle(
    { async exec() { return execResult(""); } },
    {
      async sendNotification(payload) {
        submitted = payload;
        sessionManager.appendMessage(nativeAssistantMessage("later answer"));
        return true;
      },
      report() {},
    },
  );
  const ctx = { ...makeContext(), sessionManager };
  lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, { ...ctx });
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, { ...ctx });
  await flush();
  assert.deepEqual(submitted, {
    schema_version: 1,
    harness: "pi",
    session_id: "capture-before-label-session",
    hook_event_name: "TurnComplete",
    completion_id: source.id,
    cwd: "/work/project",
    message: "original question",
    last_assistant_message: "original answer",
  });
});

test("missing assistant identity is an observable fixed diagnostic and never enqueues", async () => {
  /** @type {string[]} */
  const diagnostics = [];
  let notifications = 0;
  const lifecycle = createPiLifecycle(
    { async exec() { return execResult(""); } },
    {
      async sendNotification() { notifications += 1; return true; },
      report(message) { diagnostics.push(message); },
    },
  );
  const ctx = makeContext({ branch: [user()] });
  lifecycle.handlers.agent_settled({ type: "agent_settled" }, ctx);
  await flush();
  assert.equal(notifications, 0);
  assert.deepEqual(diagnostics, [PI_IDENTITY_DIAGNOSTIC]);
});

test("settled attribution is immutable across new, resume, fork, reload, and tree lifecycle changes", async () => {
  for (const reason of ["new", "resume", "fork", "reload"]) {
    const oldHarness = makeUIHarness();
    const nextHarness = makeUIHarness();
    /** @type {DeferredBoolean} */
    const pending = deferred();
    /** @type {object | undefined} */
    let submitted;
    const lifecycle = createPiLifecycle(
      { async exec() { return execResult(""); } },
      {
        sendNotification(payload) {
          submitted = payload;
          return pending.promise;
        },
        report() {},
      },
    );
    const origin = makeContext({
      cwd: "/origin",
      sessionId: `origin-${reason}`,
      branch: [user("origin user"), assistant(0, "origin answer", `origin-assistant-${reason}`)],
      ui: oldHarness.ui,
    });
    lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, origin);
    lifecycle.handlers.agent_settled({ type: "agent_settled" }, origin);

    const replacement = makeContext({
      cwd: "/replacement",
      sessionId: `replacement-${reason}`,
      branch: [user("replacement user"), assistant(0, "replacement answer", "replacement-assistant")],
      ui: nextHarness.ui,
    });
    lifecycle.handlers.session_start({ type: "session_start", reason }, replacement);
    lifecycle.handlers.session_tree({ type: "session_tree" }, replacement);

    assert.deepEqual(submitted, {
      schema_version: 1,
      harness: "pi",
      session_id: `origin-${reason}`,
      hook_event_name: "TurnComplete",
      completion_id: `origin-assistant-${reason}`,
      cwd: "/origin",
      message: "origin user",
      last_assistant_message: "origin answer",
    });
    pending.resolve(true);
    await flush();
  }
});

test("new, resume, fork, reload, and shutdown dispose in-flight footers without late UI", async () => {
  for (const reason of ["new", "resume", "fork", "reload"]) {
    const firstHarness = makeUIHarness();
    const secondHarness = makeUIHarness();
    /** @type {DeferredExec} */
    const pending = deferred();
    /** @type {AbortSignal | undefined} */
    let signal;
    const lifecycle = createPiLifecycle(
      {
        exec(_command, _args, options = {}) {
          signal = options.signal;
          return pending.promise;
        },
      },
      { async sendNotification() { return true; }, report() {} },
    );
    const first = makeContext({ ui: firstHarness.ui });
    lifecycle.handlers.session_start({ type: "session_start", reason: "startup" }, first);
    firstHarness.component()?.render(100);
    await Promise.resolve();
    lifecycle.handlers.session_start(
      { type: "session_start", reason },
      makeContext({ ui: secondHarness.ui }),
    );
    assert.equal(signal?.aborted, true, reason);
    pending.resolve(execResult("late\n"));
    await flush();
    assert.equal(firstHarness.component(), undefined, reason);
    assert.equal(firstHarness.renders(), 0, reason);
    lifecycle.handlers.session_shutdown(
      { type: "session_shutdown", reason: "quit" },
      makeContext({ ui: secondHarness.ui }),
    );
    assert.equal(secondHarness.component(), undefined, reason);
  }
});
