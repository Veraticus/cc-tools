import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

const PACKAGE_NAME = "@tintinweb/pi-subagents";
const PACKAGE_VERSION = "0.19.0";
const COUPLING_ERROR = "Steward cannot verify the pinned Pi child-context coupling";
const nativeRequire = createRequire(import.meta.url);

/** @param {unknown} value @returns {value is Record<string, unknown>} */
function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** @param {unknown} value @returns {value is () => unknown} */
function isAccessor(value) {
  return typeof value === "function";
}

function couplingError() {
  return new Error(COUPLING_ERROR);
}

/** @type {() => unknown} */
let nativeAccessor;
try {
  const manifestPath = nativeRequire.resolve(`${PACKAGE_NAME}/package.json`);
  /** @type {unknown} */
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  if (
    !isRecord(manifest) ||
    manifest.name !== PACKAGE_NAME ||
    manifest.version !== PACKAGE_VERSION
  ) {
    throw couplingError();
  }

  /** @type {unknown} */
  const nativeModule = nativeRequire(join(dirname(manifestPath), "dist", "child-context.js"));
  if (!isRecord(nativeModule) || !isAccessor(nativeModule.inChildSessionContext)) {
    throw couplingError();
  }
  nativeAccessor = nativeModule.inChildSessionContext;
} catch {
  throw couplingError();
}

export function isPiChildSession() {
  try {
    const child = nativeAccessor();
    if (typeof child === "boolean") {
      return child;
    }
  } catch {
    throw couplingError();
  }
  throw couplingError();
}
