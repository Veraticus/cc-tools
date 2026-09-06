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

/** @param {unknown} value @returns {value is import("@earendil-works/pi-coding-agent").ExtensionFactory} */
function isExtensionFactory(value) {
  return typeof value === "function";
}

function couplingError() {
  return new Error(COUPLING_ERROR);
}

/** @type {import("@earendil-works/pi-coding-agent").ExtensionFactory} */
let nativeFactory;
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
  const nativeModule = nativeRequire(join(dirname(manifestPath), "dist", "index.js"));
  if (!isRecord(nativeModule) || !isExtensionFactory(nativeModule.default)) {
    throw couplingError();
  }
  nativeFactory = nativeModule.default;
} catch {
  throw couplingError();
}

/** @type {import("@earendil-works/pi-coding-agent").ExtensionFactory} */
export default function piSubagents(pi) {
  return nativeFactory(pi);
}
