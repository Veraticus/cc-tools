import { isPiChildSession } from "./pi-child-context.mjs";
import { createPiLabels } from "./pi-labels.mjs";
import {
  PI_NOTIFY_FAILURE,
  sendPiNotification,
} from "./pi-notify.mjs";
import { createPiQuotaController } from "./pi-quota.mjs";

export const PI_IDENTITY_DIAGNOSTIC =
  "Steward skipped the Pi settled notification because native identity was unavailable";

const MAXIMUM_ID_BYTES = 256;
const MAXIMUM_TEXT_BYTES = 8192;
const RESERVED_TEXT_BYTES = 4096;
const FOOTER_TIMEOUT_MS = 5_000;

/**
 * @typedef {{
 *   type: string,
 *   id: string,
 *   message?: {
 *     role: string,
 *     content?: unknown,
 *     usage?: {cost?: {total?: number}},
 *   },
 * }} AdapterEntry
 */
/**
 * @typedef {{
 *   id: string,
 *   provider: string,
 *   name?: string,
 *   api?: string,
 *   baseUrl?: string,
 *   contextWindow: number,
 * }} AdapterModel
 */
/** @typedef {{tokens: number | null, contextWindow: number, percent: number | null}} AdapterContextUsage */
/** @typedef {{render(width: number): string[], invalidate(): void, dispose?(): void}} FooterComponent */
/** @typedef {{requestRender(): void}} FooterTui */
/** @typedef {{onBranchChange(callback: () => void): () => void}} FooterData */
/** @typedef {(tui: FooterTui, theme: unknown, footerData: FooterData) => FooterComponent} FooterFactory */
/** @typedef {{setFooter(factory: FooterFactory | undefined): void}} FooterUI */
/**
 * @typedef {{
 *   mode: "tui" | "rpc" | "json" | "print",
 *   cwd: string,
 *   model: AdapterModel | undefined,
 *   thinkingLevel?: string,
 *   sessionManager: {
 *     getSessionId(): string,
 *     getBranch(): AdapterEntry[],
 *     getEntries(): unknown[],
 *   },
 *   getContextUsage(): AdapterContextUsage | undefined,
 *   ui: FooterUI & {setTitle(title: string): void},
 * }} AdapterContext
 */
/**
 * @typedef {{
 *   setSessionName?(name: string): void,
 *   appendEntry?(type: string, data: unknown): void,
 *   exec(
 *     command: string,
 *     args: string[],
 *     options?: {signal?: AbortSignal, timeout?: number, cwd?: string},
 *   ): Promise<{stdout: string, stderr: string, code: number, killed: boolean}>,
 * }} FooterExec
 */
/** @typedef {import("./pi-quota.mjs").PiQuotaFooterSnapshot} PiQuotaFooterSnapshot */
/** @typedef {{update(ctx: AdapterContext): void, updateQuota(snapshot: PiQuotaFooterSnapshot, invalidateLastGood: boolean): void, dispose(): void}} FooterController */
/**
 * @typedef {{
 *   sendNotification?: typeof sendPiNotification,
 *   report?: (diagnostic: string) => void,
 *   readMetadata?: (sessionID: string, options?: {signal?: AbortSignal}) => ReturnType<import("./pi-metadata.mjs").readPiMetadata>,
 *   labelSchedule?: (callback: () => void, delay: number) => (() => void),
 *   createQuota?: (onChange: (snapshot: PiQuotaFooterSnapshot, invalidateLastGood: boolean) => void) => import("./pi-quota.mjs").PiQuotaController,
 * }} LifecycleDependencies
 */

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {string} value @returns {boolean} */
function containsControl(value) {
  for (const character of value) {
    const codepoint = character.codePointAt(0);
    if (codepoint !== undefined && (codepoint <= 0x1f || (codepoint >= 0x7f && codepoint <= 0x9f))) {
      return true;
    }
  }
  return false;
}

/** @param {string} value @returns {boolean} */
function validNativeID(value) {
  return value !== "" && value.isWellFormed() && Buffer.byteLength(value, "utf8") <= MAXIMUM_ID_BYTES &&
    !containsControl(value);
}

/** @param {string} value @returns {boolean} */
function validSessionID(value) {
  return validNativeID(value) && value !== "." && !value.includes("/") && !value.includes("..");
}

/** @param {unknown} content @returns {string} */
function messageText(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";

  /** @type {string[]} */
  const text = [];
  for (const block of content) {
    if (isRecord(block) && block.type === "text" && typeof block.text === "string") {
      text.push(block.text);
    }
  }
  return text.join("\n");
}

/** @param {string} value @param {number} budget @returns {string} */
function utf8Tail(value, budget) {
  const bytes = Buffer.from(value, "utf8");
  if (bytes.byteLength <= budget) return value;
  let start = bytes.byteLength - budget;
  while (start < bytes.byteLength && (bytes[start] & 0xc0) === 0x80) start += 1;
  return bytes.subarray(start).toString("utf8");
}

/** @param {string} user @param {string} assistant @returns {[string, string]} */
function boundNotificationText(user, assistant) {
  const userBytes = Buffer.byteLength(user, "utf8");
  const assistantBytes = Buffer.byteLength(assistant, "utf8");
  const userBudget = MAXIMUM_TEXT_BYTES - Math.min(assistantBytes, RESERVED_TEXT_BYTES);
  const assistantBudget = MAXIMUM_TEXT_BYTES - Math.min(userBytes, RESERVED_TEXT_BYTES);
  return [utf8Tail(user, userBudget), utf8Tail(assistant, assistantBudget)];
}

/** @param {AdapterContext} ctx @returns {number} */
function sessionCost(ctx) {
  let total = 0;
  for (const entry of ctx.sessionManager.getBranch()) {
    if (entry.type !== "message" || entry.message?.role !== "assistant") continue;
    const cost = entry.message.usage?.cost?.total;
    if (typeof cost === "number" && Number.isFinite(cost)) total += cost;
  }
  return total;
}

/**
 * @param {AdapterContext} ctx
 * @param {number} columns
 * @param {PiQuotaFooterSnapshot} [quota]
 * @returns {string}
 */
export function statuslinePayload(ctx, columns, quota) {
  const model = ctx.model;
  const context = ctx.getContextUsage();
  return JSON.stringify({
    harness: "pi",
    columns,
    session_id: ctx.sessionManager.getSessionId(),
    model: {
      id: model?.id ?? "pi",
      provider: model?.provider ?? "",
      display_name: model?.name ?? model?.id ?? "Pi",
    },
    cost: { total_cost_usd: sessionCost(ctx) },
    context_window: {
      used_percentage: context?.percent ?? 0,
      context_window_size: context?.contextWindow ?? model?.contextWindow ?? 0,
    },
    workspace: {
      project_dir: ctx.cwd,
      current_dir: ctx.cwd,
      cwd: ctx.cwd,
    },
    cwd: ctx.cwd,
    effort: ctx.thinkingLevel ? { level: ctx.thinkingLevel } : undefined,
    steward_quota: quota,
  });
}

/**
 * @param {FooterExec} pi
 * @param {AdapterContext} initialCtx
 * @param {PiQuotaFooterSnapshot} [initialQuota]
 * @returns {FooterController}
 */
export function installFooter(pi, initialCtx, initialQuota) {
  let ctx = initialCtx;
  let quota = initialQuota;
  let columns = 0;
  let line = "";
  let generation = 0;
  let dirty = false;
  let scheduled = false;
  let running = false;
  let disposed = false;
  let cleared = false;
  /** @type {AbortController | undefined} */
  let activeController;
  /** @type {(() => void) | undefined} */
  let requestRender;
  /** @type {(() => void) | undefined} */
  let unsubscribe;

  function scheduleRefresh() {
    if (disposed) return;
    generation += 1;
    dirty = true;
    if (scheduled) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      void refresh();
    });
  }

  async function refresh() {
    if (disposed || running || columns <= 0 || !dirty) return;
    running = true;
    try {
      do {
        dirty = false;
        const refreshGeneration = generation;
        const refreshCtx = ctx;
        const refreshColumns = columns;
        const controller = new AbortController();
        activeController = controller;
        try {
          const result = await pi.exec(
            "steward",
            ["statusline", statuslinePayload(refreshCtx, refreshColumns, quota)],
            {
              cwd: refreshCtx.cwd,
              timeout: FOOTER_TIMEOUT_MS,
              signal: controller.signal,
            },
          );
          const candidate = result.stdout.replace(/[\r\n]+$/, "");
          if (
            !disposed &&
            refreshGeneration === generation &&
            result.code === 0 &&
            !result.killed &&
            candidate !== ""
          ) {
            line = candidate;
          }
        } catch {
          // Keep the last good line. A later lifecycle generation retries.
        } finally {
          if (activeController === controller) activeController = undefined;
        }
      } while (!disposed && dirty);
    } finally {
      running = false;
      if (!disposed) requestRender?.();
    }
  }

  function disposeState() {
    if (disposed) return;
    disposed = true;
    dirty = false;
    activeController?.abort();
    activeController = undefined;
    const stopBranchUpdates = unsubscribe;
    unsubscribe = undefined;
    stopBranchUpdates?.();
  }

  initialCtx.ui.setFooter((tui, _theme, footerData) => {
    requestRender = () => tui.requestRender();
    unsubscribe = footerData.onBranchChange(scheduleRefresh);
    return {
      render(width) {
        if (width !== columns) {
          columns = width;
          scheduleRefresh();
        }
        return [line];
      },
      invalidate() {},
      dispose: disposeState,
    };
  });

  return {
    update(nextCtx) {
      if (disposed) return;
      ctx = nextCtx;
      scheduleRefresh();
    },
    updateQuota(nextQuota, invalidateLastGood) {
      if (disposed) return;
      quota = nextQuota;
      if (invalidateLastGood) {
        const hadLine = line !== "";
        line = "";
        activeController?.abort();
        if (hadLine) requestRender?.();
      }
      scheduleRefresh();
    },
    dispose() {
      disposeState();
      if (cleared) return;
      cleared = true;
      initialCtx.ui.setFooter(undefined);
    },
  };
}

/**
 * @param {AdapterContext} ctx
 * @returns {{ok: true, payload: import("./pi-notify.mjs").PiNotificationPayload} | {ok: false, diagnostic: string}}
 */
export function capturePiSettledNotification(ctx) {
  const sessionID = ctx.sessionManager.getSessionId();
  const branch = ctx.sessionManager.getBranch();
  let assistantIndex = -1;
  /** @type {AdapterEntry | undefined} */
  let assistantEntry;
  for (let index = branch.length - 1; index >= 0; index -= 1) {
    const entry = branch[index];
    if (entry.type === "message" && entry.message?.role === "assistant") {
      assistantIndex = index;
      assistantEntry = entry;
      break;
    }
  }

  if (
    assistantEntry === undefined ||
    !validSessionID(sessionID) ||
    !validNativeID(assistantEntry.id)
  ) {
    return { ok: false, diagnostic: PI_IDENTITY_DIAGNOSTIC };
  }

  let userText = "";
  for (let index = assistantIndex - 1; index >= 0; index -= 1) {
    const entry = branch[index];
    if (entry.type === "message" && entry.message?.role === "user") {
      userText = messageText(entry.message.content);
      break;
    }
  }
  const assistantText = messageText(assistantEntry.message?.content);
  const [message, lastAssistantMessage] = boundNotificationText(userText, assistantText);
  const payload = Object.freeze({
    schema_version: /** @type {const} */ (1),
    harness: /** @type {const} */ ("pi"),
    session_id: sessionID,
    hook_event_name: /** @type {const} */ ("TurnComplete"),
    completion_id: assistantEntry.id,
    cwd: ctx.cwd,
    message,
    last_assistant_message: lastAssistantMessage,
  });
  return { ok: true, payload };
}

/** @param {(diagnostic: string) => void} report @param {string} diagnostic */
function safeReport(report, diagnostic) {
  try {
    report(diagnostic);
  } catch {
    // Extension diagnostics never expose source data or destabilize Pi lifecycle hooks.
  }
}

/** @param {string} diagnostic */
function defaultReport(diagnostic) {
  try {
    process.stderr.write(`${diagnostic}\n`);
  } catch {
    // A closed terminal is safe during shutdown.
  }
}

/**
 * @param {FooterExec} pi
 * @param {LifecycleDependencies} [dependencies]
 */
export function createPiLifecycle(pi, dependencies = {}) {
  const notify = dependencies.sendNotification ?? sendPiNotification;
  const report = dependencies.report ?? defaultReport;
  /** @type {FooterController | undefined} */
  let footer;
  /** @type {import("./pi-quota.mjs").PiQuotaController | undefined} */
  let quota;
  const labels = createPiLabels(pi, {
    read: dependencies.readMetadata,
    schedule: dependencies.labelSchedule,
    report,
  });

  /** @param {AdapterContext} ctx */
  function updateFooter(ctx) {
    footer?.update(ctx);
  }

  const handlers = {
    /** @param {{type?: string, reason: string}} _event @param {AdapterContext} ctx */
    session_start(_event, ctx) {
      quota?.dispose();
      quota = undefined;
      footer?.dispose();
      footer = undefined;
      if (ctx.mode === "tui") {
        footer = installFooter(pi, ctx);
        const createQuota = dependencies.createQuota ?? ((onChange) => createPiQuotaController(onChange, { report }));
        quota = createQuota((snapshot, invalidateLastGood) => footer?.updateQuota(snapshot, invalidateLastGood));
        void quota.start(ctx.model);
      }
      labels.start(ctx);
    },
    /** @param {{type?: string, reason: string}} _event @param {AdapterContext} _ctx */
    session_shutdown(_event, _ctx) {
      quota?.dispose();
      quota = undefined;
      footer?.dispose();
      footer = undefined;
      labels.dispose();
    },
    /** @param {{type?: string, name?: string}} event @param {AdapterContext} ctx */
    session_info_changed(event, ctx) { labels.infoChanged(event, ctx); updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    agent_start(_event, ctx) { labels.activity(ctx); updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    turn_end(_event, ctx) { updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    tool_execution_end(_event, ctx) { updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    model_select(_event, ctx) { void quota?.model(ctx.model); updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    thinking_level_select(_event, ctx) { updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    session_compact(_event, ctx) { updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    session_tree(_event, ctx) { labels.tree(ctx); updateFooter(ctx); },
    /** @param {unknown} _event @param {AdapterContext} ctx */
    agent_settled(_event, ctx) {
      if (ctx.mode !== "tui") return;
      const snapshot = capturePiSettledNotification(ctx);
      updateFooter(ctx);
      if (!snapshot.ok) {
        safeReport(report, snapshot.diagnostic);
        return;
      }

      try {
        const submission = notify(snapshot.payload, { report });
        void Promise.resolve(submission).catch(() => safeReport(report, PI_NOTIFY_FAILURE));
      } catch {
        safeReport(report, PI_NOTIFY_FAILURE);
      }
      labels.settled(ctx, snapshot.payload.completion_id);
      void quota?.settled(ctx.model, snapshot.payload.completion_id);
    },
  };

  return { handlers };
}

/**
 * Classify before constructing any root runtime state.
 * @param {() => boolean} classify
 * @param {() => void} registerRoot
 * @returns {boolean} whether this is a child session
 */
export function initializePiExtension(classify, registerRoot) {
  const child = classify();
  if (child) return true;
  registerRoot();
  return false;
}

/** @type {import("@earendil-works/pi-coding-agent").ExtensionFactory} */
export default function stewardPiExtension(pi) {
  let lifecycle;
  const child = initializePiExtension(isPiChildSession, () => {
    lifecycle = createPiLifecycle(pi);
    pi.on("session_start", lifecycle.handlers.session_start);
    pi.on("session_shutdown", lifecycle.handlers.session_shutdown);
    pi.on("session_info_changed", lifecycle.handlers.session_info_changed);
    pi.on("agent_start", lifecycle.handlers.agent_start);
    pi.on("turn_end", lifecycle.handlers.turn_end);
    pi.on("tool_execution_end", lifecycle.handlers.tool_execution_end);
    pi.on("model_select", lifecycle.handlers.model_select);
    pi.on("thinking_level_select", lifecycle.handlers.thinking_level_select);
    pi.on("session_compact", lifecycle.handlers.session_compact);
    pi.on("session_tree", lifecycle.handlers.session_tree);
    pi.on("agent_settled", lifecycle.handlers.agent_settled);
  });

  if (child) return;
}
