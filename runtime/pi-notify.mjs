import { spawn as nodeSpawn } from "node:child_process";
import process from "node:process";

export const PI_NOTIFY_FAILURE = "Steward could not submit the Pi settled notification";
const NOTIFY_TIMEOUT_MS = 90_000;

/**
 * @typedef {{
 *   schema_version: 1,
 *   harness: "pi",
 *   session_id: string,
 *   hook_event_name: "TurnComplete",
 *   completion_id: string,
 *   cwd: string,
 *   message: string,
 *   last_assistant_message: string,
 * }} PiNotificationPayload
 */

/**
 * @typedef {{
 *   end(wire: string, callback: (error?: Error | null) => void): void,
 *   destroy(): void,
 *   once(event: "error", listener: (error: Error) => void): unknown,
 *   removeListener(event: "error", listener: (error: Error) => void): unknown,
 * }} NotificationInput
 */

/**
 * @typedef {{
 *   stdin: NotificationInput | null,
 *   once(event: "error", listener: (error: Error) => void): unknown,
 *   once(event: "exit", listener: (code: number | null, signal: NodeJS.Signals | null) => void): unknown,
 *   removeListener(event: "error", listener: (error: Error) => void): unknown,
 *   removeListener(event: "exit", listener: (code: number | null, signal: NodeJS.Signals | null) => void): unknown,
 *   kill(signal?: NodeJS.Signals | number): boolean,
 *   unref(): void,
 * }} NotificationChild
 */

/**
 * @typedef {{
 *   cwd: string,
 *   shell: false,
 *   stdio: ["pipe", "ignore", "ignore"],
 *   windowsHide: true,
 * }} NotificationSpawnOptions
 */

/** @typedef {(command: string, args: string[], options: NotificationSpawnOptions) => NotificationChild} NotificationSpawn */
/**
 * @typedef {{
 *   spawn?: NotificationSpawn,
 *   timeoutMs?: number,
 *   report?: (diagnostic: string) => void,
 * }} NotificationDependencies
 */

/** @param {unknown} value @returns {value is { unref(): void }} */
function canUnref(value) {
  return typeof value === "object" && value !== null && "unref" in value &&
    typeof value.unref === "function";
}

/** @param {(diagnostic: string) => void} report */
function reportFailure(report) {
  try {
    report(PI_NOTIFY_FAILURE);
  } catch {
    // The transport result is already fixed and must never become a rejection.
  }
}

/** @param {string} diagnostic */
function reportToStderr(diagnostic) {
  try {
    process.stderr.write(`${diagnostic}\n`);
  } catch {
    // A closed UI stderr must not turn best-effort notification work into a rejection.
  }
}

/** @type {NotificationSpawn} */
const spawnNotification = (command, args, options) => nodeSpawn(command, args, options);

/**
 * Submit one canonical Pi completion to the existing notifier CLI. The promise
 * always resolves: callers can safely fire-and-forget it during UI shutdown.
 *
 * @param {PiNotificationPayload} payload
 * @param {NotificationDependencies} [dependencies]
 * @returns {Promise<boolean>}
 */
export function sendPiNotification(payload, dependencies = {}) {
  const spawn = dependencies.spawn ?? spawnNotification;
  const report = dependencies.report ?? reportToStderr;
  const timeoutMs = dependencies.timeoutMs ?? NOTIFY_TIMEOUT_MS;
  const wire = `${JSON.stringify(payload)}\n`;

  /** @type {NotificationChild} */
  let child;
  try {
    child = spawn(
      "cc-tools",
      ["notify", "--harness", "pi"],
      {
        cwd: payload.cwd,
        shell: false,
        stdio: ["pipe", "ignore", "ignore"],
        windowsHide: true,
      },
    );
  } catch {
    reportFailure(report);
    return Promise.resolve(false);
  }

  return new Promise((resolve) => {
    let settled = false;
    let exitedSuccessfully = false;
    let inputCompleted = false;
    const input = child.stdin;

    /** @param {Error} _error */
    const onChildError = (_error) => finish(false, true);
    /** @param {Error} _error */
    const onInputError = (_error) => finish(false, true);
    /** @param {number | null} code @param {NodeJS.Signals | null} _signal */
    const onExit = (code, _signal) => {
      if (code !== 0) {
        finish(false, false);
        return;
      }
      exitedSuccessfully = true;
      if (inputCompleted) finish(true, false);
    };

    const timer = setTimeout(() => finish(false, true), Math.max(0, timeoutMs));
    timer.unref();

    /** @param {boolean} ok @param {boolean} terminate */
    function finish(ok, terminate) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);

      if (terminate) {
        try {
          child.kill("SIGKILL");
        } catch {
          // The same fixed transport failure covers a process that already vanished.
        }
      }

      child.removeListener("error", onChildError);
      child.removeListener("exit", onExit);
      if (input !== null) {
        input.removeListener("error", onInputError);
        try {
          input.destroy();
        } catch {
          // Cleanup is bounded and the transport result is already selected.
        }
      }

      if (!ok) reportFailure(report);
      resolve(ok);
    }

    child.once("error", onChildError);
    child.once("exit", onExit);
    try {
      child.unref();
    } catch {
      finish(false, true);
      return;
    }

    if (input === null) {
      finish(false, true);
      return;
    }
    input.once("error", onInputError);
    try {
      if (canUnref(input)) input.unref();
      input.end(wire, (error) => {
        if (error) {
          finish(false, true);
          return;
        }
        inputCompleted = true;
        if (exitedSuccessfully) finish(true, false);
      });
    } catch {
      finish(false, true);
    }
  });
}
