import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { SessionManager } from "@earendil-works/pi-coding-agent";

import { PI_LABEL_FAILURE, createPiLabels } from "./pi-labels.mjs";

const OFFSETS = [0, 250, 1_000, 3_000, 8_000, 16_000];
const TYPE = "steward-pi-label";

/** @param {string} id */
function manager(id = "session-1") {
  return SessionManager.inMemory("/work/project", { id });
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

/** @param {SessionManager} session @param {string} id @param {string} [text] */
function appendExchange(session, id, text = "answer") {
  session.appendMessage({ role: "user", content: [{ type: "text", text: `question ${id}` }], timestamp: Date.now() });
  session.appendMessage(nativeAssistantMessage(text));
  const entry = session.getEntries().at(-1);
  assert.equal(entry?.type, "message");
  if (entry?.type !== "message") throw new Error("missing native assistant entry");
  // SessionManager owns native IDs, so replace only through another native manager fixture when an exact ID matters.
  return entry.id;
}

/** @param {SessionManager} session */
function currentAssistantID(session) {
  for (const entry of session.getBranch().toReversed()) {
    if (entry.type === "message" && entry.message.role === "assistant") return entry.id;
  }
  return undefined;
}

function scheduler() {
  /** @type {Array<{callback: () => void, delay: number, cancelled: boolean, fired: boolean}>} */
  const tasks = [];
  return {
    schedule(/** @type {() => void} */ callback, /** @type {number} */ delay) {
      const task = { callback, delay, cancelled: false, fired: false };
      tasks.push(task);
      return () => { task.cancelled = true; };
    },
    tasks,
    fire(/** @type {number} */ index, includeCancelled = false) {
      const task = tasks[index];
      assert.ok(task, `scheduled task ${index} must exist`);
      if (task.fired || (task.cancelled && !includeCancelled)) return false;
      task.fired = true;
      task.callback();
      return true;
    },
  };
}

/** @template T */
function deferred() {
  /** @type {(value: T) => void} */ let resolve = () => {};
  /** @type {(error: Error) => void} */ let reject = () => {};
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

/** @param {string} completionID @param {Partial<{label: string, sourceGeneration: string, labelGeneration: string}>} [overrides] */
function known(completionID, overrides = {}) {
  return {
    status: /** @type {const} */ ("known"),
    label: overrides.label ?? "Shared useful label",
    completionID,
    sourceGeneration: overrides.sourceGeneration ?? "1",
    labelGeneration: overrides.labelGeneration ?? "1",
  };
}

/** @param {SessionManager} initialManager */
function nativeHarness(initialManager) {
  let activeManager = initialManager;
  /** @type {ReturnType<typeof createPiLabels> | undefined} */ let labels;
  /** @type {Array<{kind: "native" | "shared", title: string}>} */ const titles = [];
  /** @type {string[]} */ const setNames = [];
  /** @type {((label: string) => void) | undefined} */ let duringSet;
  /** @type {((type: string, data: unknown) => void) | undefined} */ let appendOverride;
  const pi = {
    /** @param {string} label */
    setSessionName(label) {
      setNames.push(label);
      activeManager.appendSessionInfo(label);
      titles.push({ kind: "native", title: `Pi - ${activeManager.getSessionName()} - project` });
      duringSet?.(label);
      labels?.infoChanged({ type: "session_info_changed", name: activeManager.getSessionName() }, harness.context());
    },
    /** @param {string} type @param {unknown} data */
    appendEntry(type, data) {
      if (appendOverride !== undefined) return appendOverride(type, data);
      activeManager.appendCustomEntry(type, data);
    },
  };
  const harness = {
    pi,
    titles,
    setNames,
    get manager() { return activeManager; },
    switchManager(/** @type {SessionManager} */ next) { activeManager = next; },
    setLabels(/** @type {ReturnType<typeof createPiLabels>} */ next) { labels = next; },
    setDuringSet(/** @type {(label: string) => void} */ callback) { duringSet = callback; },
    setAppendOverride(/** @type {(type: string, data: unknown) => void} */ callback) { appendOverride = callback; },
    context() {
      const captured = activeManager;
      return {
        mode: /** @type {const} */ ("tui"),
        sessionManager: captured,
        ui: { setTitle(/** @type {string} */ title) { titles.push({ kind: "shared", title }); } },
      };
    },
    /** @param {string} name */
    manualName(name) {
      activeManager.appendSessionInfo(name);
      titles.push({ kind: "native", title: name === "" ? "Pi - project" : `Pi - ${name} - project` });
      labels?.infoChanged({ type: "session_info_changed", name: activeManager.getSessionName() }, harness.context());
    },
  };
  return harness;
}

/** @param {SessionManager} session @returns {import("@earendil-works/pi-coding-agent").CustomEntry[]} */
function ownershipEntries(session) {
  /** @type {import("@earendil-works/pi-coding-agent").CustomEntry[]} */
  const entries = [];
  for (const entry of session.getEntries()) {
    if (entry.type === "custom" && entry.customType === TYPE) entries.push(entry);
  }
  return entries;
}

/** @param {SessionManager} session @param {{sourceGeneration: string, labelGeneration: string, label: string}} values */
function appendAutomaticOwnership(session, values) {
  const infoID = session.appendSessionInfo(values.label);
  session.appendCustomEntry(TYPE, {
    version: 1,
    ownership: "automatic",
    sessionID: session.getSessionId(),
    sessionInfoID: infoID,
    sourceGeneration: values.sourceGeneration,
    labelGeneration: values.labelGeneration,
    label: values.label,
  });
  return infoID;
}

test("fresh Pi event contexts apply a normalized shared label and persist its one exact native row", async () => {
  const session = manager();
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  const labels = createPiLabels(harness.pi, {
    read: async () => known(source, {
      label: "  Shared useful label  ",
      sourceGeneration: "18446744073709551615",
      labelGeneration: "9007199254740993",
    }),
    schedule: clock.schedule,
  });
  harness.setLabels(labels);

  labels.start(harness.context());
  labels.activity(harness.context());
  labels.settled(harness.context(), source);
  assert.deepEqual(clock.tasks.slice(-6).map(({ delay }) => delay), OFFSETS);
  clock.fire(clock.tasks.length - 6);
  await flush();

  const infos = session.getEntries().filter((entry) => entry.type === "session_info");
  assert.equal(infos.length, 1);
  assert.deepEqual(harness.setNames, ["Shared useful label"]);
  assert.deepEqual(harness.titles.slice(-2), [
    { kind: "native", title: "Pi - Shared useful label - project" },
    { kind: "shared", title: "Shared useful label" },
  ]);
  assert.deepEqual(ownershipEntries(session).map((entry) => entry.data), [{
    version: 1,
    ownership: "automatic",
    sessionID: session.getSessionId(),
    sessionInfoID: infos[0]?.id,
    sourceGeneration: "18446744073709551615",
    labelGeneration: "9007199254740993",
    label: "Shared useful label",
  }]);
});

test("session start resumes a terminal source, and tree plus agent activity cancel then rearm on settled", async () => {
  const session = manager();
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  let reads = 0;
  const labels = createPiLabels(harness.pi, {
    read: async () => { reads += 1; return known(source); },
    schedule: clock.schedule,
  });
  harness.setLabels(labels);

  labels.start(harness.context());
  assert.equal(clock.tasks.length, 6, "resume/start schedules the current terminal assistant");
  labels.activity(harness.context());
  assert.ok(clock.tasks.every(({ cancelled }) => cancelled));
  labels.settled(harness.context(), source);
  assert.equal(clock.tasks.length, 12);
  labels.tree(harness.context());
  assert.ok(clock.tasks.slice(6).every(({ cancelled }) => cancelled));
  labels.settled(harness.context(), source);
  clock.fire(12);
  await flush();
  assert.equal(reads, 1);
  assert.equal(session.getSessionName(), "Shared useful label");
});

test("unnamed sessions are eligible but existing names, manual clears, and corrupt ownership remain manual", async (t) => {
  /** @type {Array<[string, (session: SessionManager) => void, boolean]>} */
  const fixtures = [
    ["unnamed", () => {}, true],
    ["manual name", (session) => { session.appendSessionInfo("Personal choice"); }, false],
    ["manual clear", (session) => { session.appendSessionInfo(""); }, false],
    ["corrupt automatic state with name", (session) => {
      session.appendSessionInfo("Personal choice");
      session.appendCustomEntry(TYPE, {
        version: 1,
        ownership: "automatic",
        sessionID: session.getSessionId(),
        sessionInfoID: "fabricated",
        sourceGeneration: "0",
        labelGeneration: "0",
        label: "",
      });
    }, false],
    ["unsafe ownership session ID", (session) => {
      const infoID = session.appendSessionInfo("Shared useful label");
      session.appendCustomEntry(TYPE, {
        version: 1,
        ownership: "automatic",
        sessionID: "bad/session",
        sessionInfoID: infoID,
        sourceGeneration: "1",
        labelGeneration: "1",
        label: "Shared useful label",
      });
    }, false],
  ];
  for (const [name, setup, applies] of fixtures) {
    await t.test(name, async () => {
      const session = manager(`session-${name.replaceAll(" ", "-")}`);
      const source = appendExchange(session, "source");
      setup(session);
      const harness = nativeHarness(session);
      const clock = scheduler();
      const labels = createPiLabels(harness.pi, { read: async () => known(source), schedule: clock.schedule });
      harness.setLabels(labels);
      labels.start(harness.context());
      for (let index = 0; index < clock.tasks.length; index += 1) clock.fire(index);
      await flush();
      assert.equal(harness.setNames.length > 0, applies);
    });
  }
});

test("same-text reentrant manual naming appends an extra native row and is never claimed as automatic", async () => {
  const session = manager();
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  const labels = createPiLabels(harness.pi, { read: async () => known(source), schedule: clock.schedule });
  harness.setLabels(labels);
  let reentered = false;
  harness.setDuringSet((label) => {
    if (reentered) return;
    reentered = true;
    session.appendSessionInfo(label);
    harness.titles.push({ kind: "native", title: `Pi - ${label} - project` });
    labels.infoChanged({ type: "session_info_changed", name: label }, harness.context());
  });

  labels.start(harness.context());
  clock.fire(0);
  await flush();
  assert.equal(session.getEntries().filter((entry) => entry.type === "session_info").length, 2);
  assert.equal(ownershipEntries(session).length, 0);
  assert.equal(harness.titles.filter(({ kind }) => kind === "shared").length, 0);

  const next = appendExchange(session, "next");
  labels.settled(harness.context(), next);
  for (let index = 6; index < clock.tasks.length; index += 1) clock.fire(index);
  await flush();
  assert.equal(harness.setNames.length, 1, "the reentrant native row remains manual even with identical text");
});

test("a delayed native event for the owned row does not cancel later automatic updates", async () => {
  const session = manager();
  const first = appendExchange(session, "first");
  const harness = nativeHarness(session);
  const clock = scheduler();
  let completion = first;
  const labels = createPiLabels(harness.pi, {
    read: async () => known(completion, {
      label: completion === first ? "First shared label" : "Second shared label",
      sourceGeneration: completion === first ? "1" : "2",
      labelGeneration: completion === first ? "1" : "2",
    }),
    schedule: clock.schedule,
  });
  harness.setLabels(labels);

  labels.start(harness.context());
  clock.fire(0);
  await flush();
  labels.infoChanged({ type: "session_info_changed", name: "First shared label" }, harness.context());
  completion = appendExchange(session, "second");
  labels.settled(harness.context(), completion);
  clock.fire(6);
  await flush();
  assert.deepEqual(harness.setNames, ["First shared label", "Second shared label"]);
});

test("persisted ownership generations survive resume but scope to native forks and tree branches", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-label-native-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const sessionA = SessionManager.create(root, join(root, "sessions"), { id: "session-a" });
  const sourceA = appendExchange(sessionA, "a");
  const harness = nativeHarness(sessionA);
  const clock = scheduler();
  let response = known(sourceA, {
    label: "Saved shared label",
    sourceGeneration: "10",
    labelGeneration: "10",
  });
  const labels = createPiLabels(harness.pi, { read: async () => response, schedule: clock.schedule });
  harness.setLabels(labels);

  labels.start(harness.context());
  clock.fire(0);
  await flush();
  assert.equal(sessionA.getSessionName(), "Saved shared label");
  const sessionAFile = sessionA.getSessionFile();
  assert.ok(sessionAFile);

  const resumed = SessionManager.open(sessionAFile);
  assert.equal(resumed.getSessionId(), sessionA.getSessionId());
  harness.switchManager(resumed);
  response = known(sourceA, {
    label: "Older resumed label",
    sourceGeneration: "9",
    labelGeneration: "9",
  });
  labels.start(harness.context());
  clock.fire(6);
  await flush();
  assert.equal(resumed.getSessionName(), "Saved shared label", "older same-session metadata stays rejected after reopen");
  assert.deepEqual(harness.setNames, ["Saved shared label"]);

  const ownedLeaf = resumed.getLeafEntry();
  assert.equal(ownedLeaf?.type, "custom");
  assert.equal(ownedLeaf?.type === "custom" ? ownedLeaf.customType : undefined, TYPE);
  const originalSessionID = resumed.getSessionId();
  const forkFile = resumed.createBranchedSession(ownedLeaf.id);
  assert.ok(forkFile);
  assert.notEqual(resumed.getSessionId(), originalSessionID);
  assert.equal(resumed.getSessionFile(), forkFile);

  const forkSource = appendExchange(resumed, "fork");
  response = known(forkSource, {
    label: "Saved shared label",
    sourceGeneration: "1",
    labelGeneration: "1",
  });
  const forkTitleBaseline = harness.titles.length;
  labels.start(harness.context());
  clock.fire(12);
  await flush();
  assert.deepEqual(harness.titles.slice(forkTitleBaseline), [{ kind: "shared", title: "Saved shared label" }]);
  assert.equal(harness.setNames.length, 1, "same-text fork adoption does not append another native naming row");
  const inheritedInfo = resumed.getEntries().filter((entry) => entry.type === "session_info").at(-1);
  assert.deepEqual(ownershipEntries(resumed).at(-1)?.data, {
    version: 1,
    ownership: "automatic",
    sessionID: resumed.getSessionId(),
    sessionInfoID: inheritedInfo?.id,
    sourceGeneration: "1",
    labelGeneration: "1",
    label: "Saved shared label",
  });

  const forkOwnershipLeaf = resumed.getLeafEntry();
  assert.equal(forkOwnershipLeaf?.type, "custom");
  resumed.branch(forkOwnershipLeaf.id);
  labels.tree(harness.context());
  const treeSource = appendExchange(resumed, "tree");
  response = known(treeSource, {
    label: "Tree branch label",
    sourceGeneration: "2",
    labelGeneration: "2",
  });
  labels.settled(harness.context(), treeSource);
  clock.fire(18);
  await flush();
  assert.equal(resumed.getSessionName(), "Tree branch label");
  const treeInfo = resumed.getEntries().filter((entry) => entry.type === "session_info").at(-1);
  assert.deepEqual(ownershipEntries(resumed).at(-1)?.data, {
    version: 1,
    ownership: "automatic",
    sessionID: resumed.getSessionId(),
    sessionInfoID: treeInfo?.id,
    sourceGeneration: "2",
    labelGeneration: "2",
    label: "Tree branch label",
  });
});

test("same-text manual naming and clear remain manual after native reopen", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-pi-label-manual-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const session = SessionManager.create(root, join(root, "sessions"), { id: "manual-session" });
  let source = appendExchange(session, "initial");
  const harness = nativeHarness(session);
  const clock = scheduler();
  let response = known(source);
  const labels = createPiLabels(harness.pi, { read: async () => response, schedule: clock.schedule });
  harness.setLabels(labels);
  labels.start(harness.context());
  clock.fire(0);
  await flush();
  assert.equal(session.getSessionName(), "Shared useful label");

  session.appendSessionInfo("Shared useful label");
  session.appendSessionInfo("");
  const sessionFile = session.getSessionFile();
  assert.ok(sessionFile);
  const resumed = SessionManager.open(sessionFile);
  harness.switchManager(resumed);
  source = appendExchange(resumed, "resumed");
  response = known(source, {
    label: "Replacement shared label",
    sourceGeneration: "2",
    labelGeneration: "2",
  });
  labels.start(harness.context());
  for (let index = 6; index < clock.tasks.length; index += 1) clock.fire(index);
  await flush();

  assert.equal(resumed.getSessionName(), undefined);
  assert.deepEqual(harness.setNames, ["Shared useful label"]);
  assert.equal(ownershipEntries(resumed).length, 1, "manual native rows do not append redundant ownership state");
});

test("ownership and generations restore per native session and never leak from session A into session B", async () => {
  const sessionA = manager("session-a");
  const sourceA = appendExchange(sessionA, "a");
  appendAutomaticOwnership(sessionA, {
    sourceGeneration: "10",
    labelGeneration: "5",
    label: "Saved shared label",
  });
  const harness = nativeHarness(sessionA);
  const clock = scheduler();
  let response = known(sourceA, {
    label: "Saved shared label",
    sourceGeneration: "10",
    labelGeneration: "5",
  });
  const labels = createPiLabels(harness.pi, { read: async () => response, schedule: clock.schedule });
  harness.setLabels(labels);

  labels.start(harness.context());
  clock.fire(0);
  await flush();
  assert.deepEqual(harness.setNames, []);
  assert.deepEqual(harness.titles, [{ kind: "shared", title: "Saved shared label" }]);

  response = known(sourceA, {
    label: "Source regressed label",
    sourceGeneration: "9",
    labelGeneration: "5",
  });
  labels.tree(harness.context());
  labels.settled(harness.context(), sourceA);
  clock.fire(6);
  await flush();
  assert.deepEqual(harness.setNames, []);

  response = known(sourceA, {
    label: "Label regressed value",
    sourceGeneration: "10",
    labelGeneration: "4",
  });
  labels.tree(harness.context());
  labels.settled(harness.context(), sourceA);
  clock.fire(12);
  await flush();
  assert.deepEqual(harness.setNames, []);
  assert.equal(sessionA.getSessionName(), "Saved shared label");

  const sessionB = manager("session-b");
  const sourceB = appendExchange(sessionB, "b");
  response = known(sourceB, {
    label: "Fresh session label",
    sourceGeneration: "1",
    labelGeneration: "1",
  });
  harness.switchManager(sessionB);
  labels.start(harness.context());
  clock.fire(18);
  await flush();
  assert.equal(sessionB.getSessionName(), "Fresh session label");
});

test("metadata rereads serialize and coalesce, and every invalidation aborts the active read", async (t) => {
  await t.test("coalescing", async () => {
    const session = manager();
    appendExchange(session, "source");
    const source = currentAssistantID(session);
    assert.ok(source);
    const harness = nativeHarness(session);
    const clock = scheduler();
    /** @type {Array<ReturnType<typeof deferred>>} */ const reads = [];
    let running = 0;
    let maximum = 0;
    const labels = createPiLabels(harness.pi, {
      read(_sessionID, options) {
        running += 1;
        maximum = Math.max(maximum, running);
        const pending = deferred();
        reads.push(pending);
        options?.signal?.addEventListener("abort", () => pending.resolve({ status: "unavailable" }), { once: true });
        return pending.promise.finally(() => { running -= 1; });
      },
      schedule: clock.schedule,
    });
    harness.setLabels(labels);
    labels.start(harness.context());
    for (let index = 0; index < 6; index += 1) clock.fire(index);
    assert.equal(reads.length, 1);
    reads[0]?.resolve({ status: "unavailable" });
    await flush();
    assert.equal(reads.length, 2, "five overlapping timers become one pending reread");
    reads[1]?.resolve(known(source));
    await flush();
    assert.equal(maximum, 1);
  });

  for (const action of ["agent activity", "tree", "session replacement", "manual name", "new source", "disposal"]) {
    await t.test(action, async () => {
      const session = manager(`session-${action.replaceAll(" ", "-")}`);
      const source = appendExchange(session, "source");
      const harness = nativeHarness(session);
      const clock = scheduler();
      const pending = deferred();
      /** @type {AbortSignal | undefined} */ let signal;
      const labels = createPiLabels(harness.pi, {
        read(_sessionID, options) {
          signal = options?.signal;
          options?.signal?.addEventListener("abort", () => pending.resolve({ status: "unavailable" }), { once: true });
          return pending.promise;
        },
        schedule: clock.schedule,
      });
      harness.setLabels(labels);
      labels.start(harness.context());
      clock.fire(0);
      await Promise.resolve();
      assert.equal(signal?.aborted, false);

      if (action === "agent activity") labels.activity(harness.context());
      if (action === "tree") labels.tree(harness.context());
      if (action === "session replacement") {
        harness.switchManager(manager("replacement"));
        labels.start(harness.context());
      }
      if (action === "manual name") harness.manualName("Personal choice");
      if (action === "new source") {
        const next = appendExchange(session, "next");
        labels.settled(harness.context(), next);
      }
      if (action === "disposal") labels.dispose();

      assert.equal(signal?.aborted, true, action);
      await pending.promise;
      await flush();
      assert.equal(harness.setNames.length, 0);
    });
  }
});

test("a native ownership change while metadata is pending is rejected even before its event arrives", async () => {
  const session = manager();
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  const pending = deferred();
  const labels = createPiLabels(harness.pi, { read: () => pending.promise, schedule: clock.schedule });
  harness.setLabels(labels);
  labels.start(harness.context());
  clock.fire(0);
  session.appendSessionInfo("Unannounced manual name");
  pending.resolve(known(source));
  await flush();
  assert.deepEqual(harness.setNames, []);
  assert.equal(session.getSessionName(), "Unannounced manual name");
});

test("a changed ownership session while metadata is pending is rejected", async () => {
  const session = manager();
  const infoID = appendAutomaticOwnership(session, {
    sourceGeneration: "1",
    labelGeneration: "1",
    label: "Shared useful label",
  });
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  const pending = deferred();
  const labels = createPiLabels(harness.pi, { read: () => pending.promise, schedule: clock.schedule });
  harness.setLabels(labels);
  labels.start(harness.context());
  clock.fire(0);
  await Promise.resolve();
  session.appendCustomEntry(TYPE, {
    version: 1,
    ownership: "automatic",
    sessionID: "other-session",
    sessionInfoID: infoID,
    sourceGeneration: "1",
    labelGeneration: "1",
    label: "Shared useful label",
  });
  pending.resolve(known(source, { sourceGeneration: "2", labelGeneration: "2" }));
  await flush();

  assert.equal(ownershipEntries(session).length, 2);
  assert.deepEqual(harness.setNames, []);
  assert.deepEqual(harness.titles, []);
});

test("prior labels apply before refresh, later labels replace them, and KEEP advances state without another native row", async (t) => {
  await t.test("late replacement", async () => {
    const session = manager();
    const source = appendExchange(session, "source");
    const harness = nativeHarness(session);
    const clock = scheduler();
    const responses = [
      known(source, { label: "Earlier shared label", sourceGeneration: "5", labelGeneration: "1" }),
      known(source, { label: "Later refreshed label", sourceGeneration: "5", labelGeneration: "5" }),
    ];
    const labels = createPiLabels(harness.pi, { read: async () => responses.shift() ?? known(source), schedule: clock.schedule });
    harness.setLabels(labels);
    labels.start(harness.context());
    clock.fire(0); await flush();
    clock.fire(1); await flush();
    assert.deepEqual(harness.setNames, ["Earlier shared label", "Later refreshed label"]);
  });

  await t.test("KEEP", async () => {
    const session = manager();
    const source = appendExchange(session, "source");
    const harness = nativeHarness(session);
    const clock = scheduler();
    const responses = [
      known(source, { label: "Stable shared label", sourceGeneration: "5", labelGeneration: "1" }),
      known(source, { label: "Stable shared label", sourceGeneration: "5", labelGeneration: "5" }),
    ];
    const labels = createPiLabels(harness.pi, { read: async () => responses.shift() ?? known(source), schedule: clock.schedule });
    harness.setLabels(labels);
    labels.start(harness.context());
    clock.fire(0); await flush();
    clock.fire(1); await flush();
    assert.deepEqual(harness.setNames, ["Stable shared label"]);
    assert.equal(session.getEntries().filter((entry) => entry.type === "session_info").length, 1);
    assert.deepEqual(ownershipEntries(session).map((entry) => entry.data).at(-1), {
      version: 1,
      ownership: "automatic",
      sessionID: session.getSessionId(),
      sessionInfoID: session.getEntries().find((entry) => entry.type === "session_info")?.id,
      sourceGeneration: "5",
      labelGeneration: "5",
      label: "Stable shared label",
    });
  });
});

test("missing, unavailable, and known-empty cycles stay bounded and do not stop later scheduled observations", async () => {
  const session = manager();
  const source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const clock = scheduler();
  const responses = [
    { status: /** @type {const} */ ("missing"), label: "", completionID: "", sourceGeneration: "0", labelGeneration: "0" },
    { status: /** @type {const} */ ("unavailable"), label: "", completionID: "", sourceGeneration: "0", labelGeneration: "0" },
    known(source, { label: "", sourceGeneration: "1", labelGeneration: "0" }),
    known(source),
  ];
  let reads = 0;
  const labels = createPiLabels(harness.pi, {
    read: async () => { reads += 1; return responses.shift() ?? known(source); },
    schedule: clock.schedule,
  });
  harness.setLabels(labels);
  labels.start(harness.context());
  for (let index = 0; index < 4; index += 1) { clock.fire(index); await flush(); }
  assert.equal(session.getSessionName(), "Shared useful label");
  for (let index = 4; index < 6; index += 1) { clock.fire(index); await flush(); }
  assert.equal(reads, 6);
  assert.equal(clock.tasks.length, 6);
  assert.deepEqual(harness.setNames, ["Shared useful label"]);
  assert.equal(ownershipEntries(session).length, 1);
});

test("a naming failure is fixed-diagnostic, not retried for the same source, and retries on a later source", async () => {
  const session = manager();
  let source = appendExchange(session, "source");
  const harness = nativeHarness(session);
  const originalSet = harness.pi.setSessionName;
  let fail = true;
  harness.pi.setSessionName = (label) => {
    if (fail) { fail = false; throw new Error("private name failure"); }
    originalSet(label);
  };
  /** @type {string[]} */ const diagnostics = [];
  const clock = scheduler();
  const labels = createPiLabels(harness.pi, {
    read: async () => known(source, {
      label: source === currentAssistantID(session) ? "Retry shared label" : "Old shared label",
      sourceGeneration: source === currentAssistantID(session) ? "2" : "1",
      labelGeneration: source === currentAssistantID(session) ? "2" : "1",
    }),
    schedule: clock.schedule,
    report(message) { diagnostics.push(message); },
  });
  harness.setLabels(labels);

  labels.start(harness.context());
  clock.fire(0); await flush();
  for (let index = 1; index < 6; index += 1) clock.fire(index);
  await flush();
  assert.deepEqual(diagnostics, [PI_LABEL_FAILURE]);
  assert.equal(harness.setNames.length, 0);

  labels.settled(harness.context(), source);
  assert.equal(clock.tasks.length, 6, "same-source settled does not create a timer retry loop");
  source = appendExchange(session, "next");
  labels.settled(harness.context(), source);
  clock.fire(6); await flush();
  assert.equal(session.getSessionName(), "Retry shared label");
});

test("persistence, observer, title, and reporting failures remain bounded and conservative", async (t) => {
  await t.test("persistence failure leaves the new native row unowned", async () => {
    const session = manager();
    const source = appendExchange(session, "source");
    const harness = nativeHarness(session);
    const clock = scheduler();
    harness.setAppendOverride(() => { throw new Error("private persist failure"); });
    const labels = createPiLabels(harness.pi, {
      read: async () => known(source),
      schedule: clock.schedule,
      report() { throw new Error("private reporter failure"); },
    });
    harness.setLabels(labels);
    labels.start(harness.context());
    clock.fire(0);
    await flush();
    assert.equal(session.getSessionName(), "Shared useful label");
    assert.equal(ownershipEntries(session).length, 0);
    const next = appendExchange(session, "next");
    labels.settled(harness.context(), next);
    for (let index = 6; index < clock.tasks.length; index += 1) clock.fire(index);
    await flush();
    assert.equal(harness.setNames.length, 1);
  });

  await t.test("disposed context getters fail with one safe diagnostic", async () => {
    const session = manager();
    const source = appendExchange(session, "source");
    const harness = nativeHarness(session);
    const clock = scheduler();
    const pending = deferred();
    /** @type {string[]} */ const diagnostics = [];
    const labels = createPiLabels(harness.pi, {
      read: () => pending.promise,
      schedule: clock.schedule,
      report(message) { diagnostics.push(message); },
    });
    harness.setLabels(labels);
    labels.start(harness.context());
    clock.fire(0);
    session.getBranch = () => { throw new Error("disposed native observer"); };
    pending.resolve(known(source));
    await flush();
    assert.deepEqual(diagnostics, [PI_LABEL_FAILURE]);
    assert.deepEqual(harness.setNames, []);
  });

  await t.test("disposed context observer and title failures do not reject", async () => {
    const session = manager();
    const source = appendExchange(session, "source");
    const harness = nativeHarness(session);
    const clock = scheduler();
    /** @type {string[]} */ const diagnostics = [];
    const ctx = harness.context();
    ctx.ui.setTitle = () => { throw new Error("disposed UI"); };
    const labels = createPiLabels(harness.pi, {
      read: async () => known(source),
      schedule: clock.schedule,
      report(message) { diagnostics.push(message); },
    });
    harness.setLabels(labels);
    labels.start(ctx);
    clock.fire(0);
    await flush();
    assert.deepEqual(diagnostics, [PI_LABEL_FAILURE]);
  });
});
