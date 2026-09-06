import {
  readPiMetadata,
  validPiCompletionID,
  validPiGeneratedLabel,
  validPiGeneration,
} from "./pi-metadata.mjs";

export const PI_LABEL_FAILURE = "Steward labels unavailable";
const TYPE = "steward-pi-label";
const OFFSETS = [0, 250, 1_000, 3_000, 8_000, 16_000];

/** @typedef {{version: 1, ownership: "automatic", sessionID: string, sessionInfoID: string, sourceGeneration: string, labelGeneration: string, label: string}} Ownership */
/** @typedef {{getSessionId(): string, getEntries(): unknown[], getBranch(): unknown[]}} Manager */
/** @typedef {{mode: string, sessionManager: Manager, ui: {setTitle(title: string): void}}} LabelContext */
/** @typedef {{status: "known" | "missing" | "unavailable", label?: string, completionID?: string, sourceGeneration?: string, labelGeneration?: string}} Metadata */
/** @typedef {{manager: Manager, sessionID: string, ui: LabelContext["ui"], sessionEpoch: number, baseline?: Ownership, blockedSource?: string}} Active */
/** @typedef {{active: Active, sourceID: string, workEpoch: number, running: boolean, pending: boolean, failed: boolean, controller?: AbortController, observed?: {sourceGeneration: string, labelGeneration: string}}} Cycle */
/** @typedef {{info?: Record<string, unknown>, infos: Record<string, unknown>[], owner?: Ownership}} OwnershipSnapshot */

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function record(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {unknown} entry @returns {Ownership | undefined} */
function ownershipState(entry) {
  if (!record(entry) || entry.type !== "custom" || entry.customType !== TYPE || !record(entry.data)) return undefined;
  const value = entry.data;
  if (Object.keys(value).length !== 7 || value.version !== 1 || value.ownership !== "automatic" ||
      !validPiCompletionID(value.sessionID) || value.sessionID === "." || value.sessionID.includes("/") ||
      value.sessionID.includes("..") || !validPiCompletionID(value.sessionInfoID) ||
      !validPiGeneration(value.sourceGeneration) || !validPiGeneration(value.labelGeneration) ||
      value.sourceGeneration === "0" || value.labelGeneration === "0" || typeof value.label !== "string" ||
      value.label !== value.label.trim() || !validPiGeneratedLabel(value.label) ||
      BigInt(value.labelGeneration) > BigInt(value.sourceGeneration)) return undefined;
  return {
    version: 1,
    ownership: "automatic",
    sessionID: value.sessionID,
    sessionInfoID: value.sessionInfoID,
    sourceGeneration: value.sourceGeneration,
    labelGeneration: value.labelGeneration,
    label: value.label,
  };
}

/** @param {unknown[]} entries @returns {Record<string, unknown>[]} */
function sessionInfoEntries(entries) {
  /** @type {Record<string, unknown>[]} */
  const infos = [];
  for (const entry of entries) {
    if (record(entry) && entry.type === "session_info") infos.push(entry);
  }
  return infos;
}

/** @param {unknown[]} entries @returns {OwnershipSnapshot} */
function inspectOwnership(entries) {
  const infos = sessionInfoEntries(entries);
  const info = infos.at(-1);
  const saved = entries.toReversed().map(ownershipState).find((value) => value !== undefined);
  const owner = saved !== undefined && info?.id === saved.sessionInfoID && info.name === saved.label
    ? saved
    : undefined;
  return { info, infos, owner };
}

/** @param {OwnershipSnapshot} left @param {OwnershipSnapshot} right */
function sameOwnership(left, right) {
  if (left.info?.id !== right.info?.id || left.info?.name !== right.info?.name) return false;
  if (left.owner === undefined || right.owner === undefined) return left.owner === right.owner;
  return left.owner.sessionID === right.owner.sessionID &&
    left.owner.sessionInfoID === right.owner.sessionInfoID &&
    left.owner.sourceGeneration === right.owner.sourceGeneration &&
    left.owner.labelGeneration === right.owner.labelGeneration &&
    left.owner.label === right.owner.label;
}

/** @param {unknown[]} branch */
function currentAssistant(branch) {
  const entry = branch.toReversed().find((item) =>
    record(item) && item.type === "message" && record(item.message) && item.message.role === "assistant");
  return record(entry) && typeof entry.id === "string" ? entry.id : undefined;
}

/** @param {Metadata} metadata @returns {metadata is Required<Metadata> & {status: "known"}} */
function validKnownMetadata(metadata) {
  if (metadata.status !== "known" || typeof metadata.label !== "string" ||
      !validPiCompletionID(metadata.completionID) || !validPiGeneration(metadata.sourceGeneration) ||
      !validPiGeneration(metadata.labelGeneration) || metadata.sourceGeneration === "0" ||
      BigInt(metadata.labelGeneration) > BigInt(metadata.sourceGeneration)) return false;
  return metadata.label === ""
    ? metadata.labelGeneration === "0"
    : metadata.labelGeneration !== "0" && validPiGeneratedLabel(metadata.label);
}

/**
 * @param {{setSessionName?: (name: string) => void, appendEntry?: (type: string, data: Ownership) => void}} pi
 * @param {{read?: (sessionID: string, options?: {signal?: AbortSignal}) => Promise<Metadata>, schedule?: (callback: () => void, delay: number) => (() => void), report?: (diagnostic: string) => void}} [dependencies]
 */
export function createPiLabels(pi, dependencies = {}) {
  const read = dependencies.read ?? readPiMetadata;
  const schedule = dependencies.schedule ?? ((callback, delay) => {
    const timer = setTimeout(callback, delay);
    timer.unref();
    return () => clearTimeout(timer);
  });
  let sessionEpoch = 0;
  let workEpoch = 0;
  /** @type {Active | undefined} */ let active;
  /** @type {Cycle | undefined} */ let cycle;
  /** @type {Array<() => void>} */ let cancelTimers = [];
  let applying = false;
  let disposed = false;
  /** @type {string | undefined} */ let titledOwnership;

  function reportFailure() {
    try {
      dependencies.report?.(PI_LABEL_FAILURE);
    } catch {
      // A diagnostic sink must not destabilize lifecycle work.
    }
  }

  function cancelWork() {
    workEpoch += 1;
    const cancellations = cancelTimers;
    cancelTimers = [];
    for (const cancel of cancellations) {
      try {
        cancel();
      } catch {
        reportFailure();
      }
    }
    cycle?.controller?.abort();
    cycle = undefined;
  }

  /** @param {LabelContext} ctx @returns {{manager: Manager, sessionID: string, ui: LabelContext["ui"]} | undefined} */
  function snapshotContext(ctx) {
    try {
      if (ctx.mode !== "tui") return undefined;
      const manager = ctx.sessionManager;
      const sessionID = manager.getSessionId();
      const ui = ctx.ui;
      return { manager, sessionID, ui };
    } catch {
      reportFailure();
      return undefined;
    }
  }

  /** @param {Manager} manager @returns {OwnershipSnapshot | undefined} */
  function snapshotOwnership(manager) {
    try {
      return inspectOwnership(manager.getEntries());
    } catch {
      reportFailure();
      return undefined;
    }
  }

  /** @param {Manager} manager @returns {string | undefined} */
  function assistantSource(manager) {
    try {
      return currentAssistant(manager.getBranch());
    } catch {
      reportFailure();
      return undefined;
    }
  }

  /** @param {Cycle} target */
  function isCurrent(target) {
    if (disposed || cycle !== target || workEpoch !== target.workEpoch || active !== target.active ||
        sessionEpoch !== target.active.sessionEpoch) return false;
    try {
      return target.active.manager.getSessionId() === target.active.sessionID &&
        currentAssistant(target.active.manager.getBranch()) === target.sourceID;
    } catch {
      reportFailure();
      target.failed = true;
      return false;
    }
  }

  /** @param {Cycle} target @param {string} label @param {string} infoID */
  function applyTitle(target, label, infoID) {
    const key = `${infoID}\u0000${label}`;
    if (titledOwnership === key || !isCurrent(target)) return;
    const state = snapshotOwnership(target.active.manager);
    if (state?.owner?.sessionInfoID !== infoID || state.owner.label !== label || !isCurrent(target)) return;
    try {
      target.active.ui.setTitle(label);
      titledOwnership = key;
    } catch {
      reportFailure();
    }
  }

  /** @param {Cycle} target @param {OwnershipSnapshot} before @param {Required<Metadata> & {status: "known"}} metadata @param {string} label */
  function persistExistingOwnership(target, before, metadata, label) {
    const owner = before.owner;
    if (owner === undefined) return;
    const unchanged = owner.sessionID === target.active.sessionID &&
      owner.sourceGeneration === metadata.sourceGeneration &&
      owner.labelGeneration === metadata.labelGeneration && owner.label === label;
    if (!unchanged) {
      if (!isCurrent(target)) return;
      const checked = snapshotOwnership(target.active.manager);
      if (checked === undefined || !sameOwnership(before, checked)) return;
      const saved = {
        version: /** @type {const} */ (1),
        ownership: /** @type {const} */ ("automatic"),
        sessionID: target.active.sessionID,
        sessionInfoID: owner.sessionInfoID,
        sourceGeneration: metadata.sourceGeneration,
        labelGeneration: metadata.labelGeneration,
        label,
      };
      try {
        pi.appendEntry?.(TYPE, saved);
      } catch {
        reportFailure();
        target.failed = true;
        target.active.blockedSource = target.sourceID;
        return;
      }
      if (!isCurrent(target)) return;
      const after = snapshotOwnership(target.active.manager);
      if (after?.owner?.sessionID !== saved.sessionID || after.owner.sessionInfoID !== saved.sessionInfoID ||
          after.owner.sourceGeneration !== saved.sourceGeneration ||
          after.owner.labelGeneration !== saved.labelGeneration || after.owner.label !== saved.label) return;
      target.active.baseline = saved;
    }
    applyTitle(target, label, owner.sessionInfoID);
  }

  /** @param {Cycle} target @param {OwnershipSnapshot} before @param {Required<Metadata> & {status: "known"}} metadata @param {string} label */
  function setNewLabel(target, before, metadata, label) {
    const beforeIDs = new Set(before.infos.map((entry) => entry.id));
    applying = true;
    try {
      pi.setSessionName?.(label);
    } catch {
      reportFailure();
      target.failed = true;
      target.active.blockedSource = target.sourceID;
      return;
    } finally {
      applying = false;
    }
    if (!isCurrent(target)) return;
    const after = snapshotOwnership(target.active.manager);
    if (after === undefined) {
      target.failed = true;
      return;
    }
    const added = after.infos.filter((entry) => !beforeIDs.has(entry.id));
    const ownInfo = added[0];
    if (after.infos.length !== before.infos.length + 1 || added.length !== 1 ||
        after.info !== ownInfo || typeof ownInfo?.id !== "string" || ownInfo.name !== label) {
      cancelWork();
      return;
    }
    const saved = {
      version: /** @type {const} */ (1),
      ownership: /** @type {const} */ ("automatic"),
      sessionID: target.active.sessionID,
      sessionInfoID: ownInfo.id,
      sourceGeneration: metadata.sourceGeneration,
      labelGeneration: metadata.labelGeneration,
      label,
    };
    if (!isCurrent(target)) return;
    try {
      pi.appendEntry?.(TYPE, saved);
    } catch {
      reportFailure();
      target.failed = true;
      target.active.blockedSource = target.sourceID;
      return;
    }
    if (!isCurrent(target)) return;
    const persisted = snapshotOwnership(target.active.manager);
    if (persisted?.owner?.sessionID !== saved.sessionID || persisted.owner.sessionInfoID !== saved.sessionInfoID ||
        persisted.owner.sourceGeneration !== saved.sourceGeneration ||
        persisted.owner.labelGeneration !== saved.labelGeneration || persisted.owner.label !== saved.label) return;
    target.active.baseline = saved;
    applyTitle(target, label, ownInfo.id);
  }

  /** @param {Cycle} target */
  async function refresh(target) {
    if (!isCurrent(target) || target.failed) return;
    const before = snapshotOwnership(target.active.manager);
    if (before === undefined) {
      target.failed = true;
      return;
    }
    if (before.info !== undefined && before.owner === undefined) {
      target.failed = true;
      return;
    }

    const controller = new AbortController();
    target.controller = controller;
    /** @type {Metadata} */
    let metadata;
    try {
      metadata = await read(target.active.sessionID, { signal: controller.signal });
    } catch {
      if (!controller.signal.aborted && isCurrent(target)) reportFailure();
      return;
    } finally {
      if (target.controller === controller) target.controller = undefined;
    }
    if (controller.signal.aborted || !isCurrent(target) || !validKnownMetadata(metadata) ||
        metadata.completionID !== target.sourceID) return;

    const baseline = target.active.baseline;
    if (baseline !== undefined &&
        (BigInt(metadata.sourceGeneration) < BigInt(baseline.sourceGeneration) ||
         BigInt(metadata.labelGeneration) < BigInt(baseline.labelGeneration))) return;
    if (target.observed !== undefined &&
        (BigInt(metadata.sourceGeneration) < BigInt(target.observed.sourceGeneration) ||
         BigInt(metadata.labelGeneration) < BigInt(target.observed.labelGeneration))) return;
    target.observed = {
      sourceGeneration: metadata.sourceGeneration,
      labelGeneration: metadata.labelGeneration,
    };

    const checked = snapshotOwnership(target.active.manager);
    if (checked === undefined || !sameOwnership(before, checked) || !isCurrent(target) || metadata.label === "") return;
    const label = metadata.label.trim();
    if (checked.owner !== undefined && checked.owner.label === label) {
      persistExistingOwnership(target, checked, metadata, label);
      return;
    }
    setNewLabel(target, checked, metadata, label);
  }

  /** @param {Cycle} target */
  function queueRefresh(target) {
    if (!isCurrent(target) || target.failed) return;
    if (target.running) {
      target.pending = true;
      return;
    }
    target.running = true;
    void (async () => {
      try {
        do {
          target.pending = false;
          await refresh(target);
        } while (target.pending && isCurrent(target) && !target.failed);
      } catch {
        if (isCurrent(target)) {
          target.failed = true;
          target.active.blockedSource = target.sourceID;
          reportFailure();
        }
      } finally {
        target.running = false;
      }
    })();
  }

  /** @param {Active} target @param {string} sourceID */
  function beginCycle(target, sourceID) {
    if (disposed || active !== target || target.blockedSource === sourceID) return;
    if (cycle !== undefined && cycle.active === target && cycle.sourceID === sourceID) return;
    cancelWork();
    const next = {
      active: target,
      sourceID,
      workEpoch,
      running: false,
      pending: false,
      failed: false,
      observed: target.baseline === undefined ? undefined : {
        sourceGeneration: target.baseline.sourceGeneration,
        labelGeneration: target.baseline.labelGeneration,
      },
    };
    cycle = next;
    for (const offset of OFFSETS) {
      try {
        cancelTimers.push(schedule(() => queueRefresh(next), offset));
      } catch {
        next.failed = true;
        target.blockedSource = sourceID;
        reportFailure();
        break;
      }
    }
  }

  /** @param {LabelContext} ctx */
  function start(ctx) {
    cancelWork();
    sessionEpoch += 1;
    titledOwnership = undefined;
    active = undefined;
    disposed = false;
    if (pi.setSessionName === undefined || pi.appendEntry === undefined) return;
    const snapshot = snapshotContext(ctx);
    if (snapshot === undefined) return;
    const ownership = snapshotOwnership(snapshot.manager);
    if (ownership === undefined) return;
    const target = {
      ...snapshot,
      sessionEpoch,
      baseline: ownership.owner?.sessionID === snapshot.sessionID ? ownership.owner : undefined,
    };
    active = target;
    const sourceID = assistantSource(snapshot.manager);
    if (sourceID !== undefined) beginCycle(target, sourceID);
  }

  /** @param {LabelContext} ctx @returns {Active | undefined} */
  function currentEvent(ctx) {
    const snapshot = snapshotContext(ctx);
    if (snapshot === undefined || active === undefined || snapshot.manager !== active.manager ||
        snapshot.sessionID !== active.sessionID || active.sessionEpoch !== sessionEpoch) return undefined;
    active.ui = snapshot.ui;
    return active;
  }

  /** @param {LabelContext} ctx */
  function activity(ctx) {
    if (currentEvent(ctx) === undefined) return;
    cancelWork();
  }

  /** @param {LabelContext} ctx */
  function tree(ctx) {
    const target = currentEvent(ctx);
    if (target === undefined) return;
    cancelWork();
    const restored = snapshotOwnership(target.manager);
    target.baseline = restored?.owner?.sessionID === target.sessionID ? restored.owner : undefined;
    titledOwnership = undefined;
  }

  /** @param {LabelContext} ctx @param {string} completionID */
  function settled(ctx, completionID) {
    const target = currentEvent(ctx);
    if (target === undefined || !validPiCompletionID(completionID) || assistantSource(target.manager) !== completionID) return;
    beginCycle(target, completionID);
  }

  /** @param {{type?: string, name?: string}} _event @param {LabelContext} ctx */
  function infoChanged(_event, ctx) {
    if (applying) return;
    const target = currentEvent(ctx);
    if (target === undefined) return;
    const ownership = snapshotOwnership(target.manager);
    if (ownership?.owner !== undefined) {
      target.baseline = ownership.owner.sessionID === target.sessionID ? ownership.owner : undefined;
      return;
    }
    cancelWork();
    titledOwnership = undefined;
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    cancelWork();
    active = undefined;
  }

  return {
    start,
    settled,
    activity,
    tree,
    infoChanged,
    dispose,
  };
}
