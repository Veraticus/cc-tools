import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, mkdtemp, mkdir, readFile, readlink, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import process from "node:process";
import test from "node:test";
import { fileURLToPath } from "node:url";

const REPOSITORY_ROOT = resolve(fileURLToPath(new URL("..", import.meta.url)));
const SOURCE_RUNTIME = join(REPOSITORY_ROOT, "runtime");
const SOURCE_MODULES = join(REPOSITORY_ROOT, "node_modules");
const SOURCE_PACKAGE_JSON = join(REPOSITORY_ROOT, "package.json");
const SOURCE_HELPER_ENTRY = join(SOURCE_RUNTIME, "cli.mjs");
const SOURCE_PI_ENTRY = join(SOURCE_MODULES, "@earendil-works", "pi-coding-agent", "dist", "cli.js");
const SOURCE_BUNDLED_PI_ENTRY = join(SOURCE_MODULES, "@earendil-works", "pi-coding-agent", "dist", "bundle", "cli.js");
const SMOKE = join(SOURCE_RUNTIME, "package-smoke.mjs");

/** @param {string} wrapper @param {string} entry */
async function writeNodeWrapper(wrapper, entry) {
  await writeFile(wrapper, `#!${process.execPath}\nimport { spawnSync } from "node:child_process";\nconst result = spawnSync(process.execPath, [${JSON.stringify(entry)}, ...process.argv.slice(2)], { stdio: "inherit", env: process.env });\nif (result.error !== undefined) throw result.error;\nprocess.exitCode = result.status ?? 1;\n`);
  await chmod(wrapper, 0o755);
}

/** @param {string} runtimeRoot */
async function makeRuntimeFixture(runtimeRoot) {
  const runtimeLib = join(runtimeRoot, "lib", "steward");
  await Promise.all([
    mkdir(join(runtimeRoot, "bin"), { recursive: true }),
    mkdir(runtimeLib, { recursive: true }),
  ]);
  await Promise.all([
    symlink(SOURCE_RUNTIME, join(runtimeLib, "runtime")),
    symlink(SOURCE_MODULES, join(runtimeLib, "node_modules")),
    symlink(SOURCE_PACKAGE_JSON, join(runtimeLib, "package.json")),
    writeNodeWrapper(join(runtimeRoot, "bin", "steward-pi-helper"), SOURCE_HELPER_ENTRY),
    writeNodeWrapper(join(runtimeRoot, "bin", "pi"), SOURCE_PI_ENTRY),
  ]);
}

/** @param {string} root */
async function makeStewardFixture(root) {
  const steward = join(root, "steward-fixture");
  await writeFile(steward, `#!${process.execPath}\nprocess.stdout.write("steward 0.0.0-fixture\\n");\n`);
  await chmod(steward, 0o755);
  return steward;
}

/** @param {string} packageRoot @param {string} runtimeRoot @param {string} steward */
async function makeDefaultFixture(packageRoot, runtimeRoot, steward) {
  const packageLib = join(packageRoot, "lib", "steward");
  const runtimeLib = join(runtimeRoot, "lib", "steward");
  await Promise.all([
    mkdir(join(packageRoot, "bin"), { recursive: true }),
    mkdir(packageLib, { recursive: true }),
  ]);
  await Promise.all([
    symlink(steward, join(packageRoot, "bin", "steward")),
    symlink(join(runtimeRoot, "bin", "steward-pi-helper"), join(packageRoot, "bin", "steward-pi-helper")),
    symlink(join(runtimeRoot, "bin", "pi"), join(packageRoot, "bin", "pi")),
    symlink(join(runtimeLib, "runtime"), join(packageLib, "runtime")),
    symlink(join(runtimeLib, "node_modules"), join(packageLib, "node_modules")),
    symlink(join(runtimeLib, "package.json"), join(packageLib, "package.json")),
  ]);
}

/** @param {string[]} args */
function runSmoke(args) {
  return spawnSync(process.execPath, [SMOKE, ...args], {
    encoding: "utf8",
    env: { PATH: process.env.PATH ?? "/usr/bin:/bin" },
    timeout: 15_000,
  });
}

test("package smoke unit fixtures use the prepared source graph", async (t) => {
  const source = await readFile(fileURLToPath(import.meta.url), "utf8");
  assert.doesNotMatch(source, /PACKAGED_(?:DEFAULT|RUNTIME)/);

  const root = await mkdtemp(join(tmpdir(), "steward-package-smoke-unit-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const runtimeRoot = join(root, "runtime");
  const packageRoot = join(root, "package");
  const steward = await makeStewardFixture(root);
  await makeRuntimeFixture(runtimeRoot);
  await makeDefaultFixture(packageRoot, runtimeRoot, steward);

  const runtimeLib = join(runtimeRoot, "lib", "steward");
  assert.equal(await readlink(join(runtimeLib, "runtime")), SOURCE_RUNTIME);
  assert.equal(await readlink(join(runtimeLib, "node_modules")), SOURCE_MODULES);
  assert.equal(await readlink(join(runtimeLib, "package.json")), SOURCE_PACKAGE_JSON);
  const helperWrapper = await readFile(join(runtimeRoot, "bin", "steward-pi-helper"), "utf8");
  const piWrapper = await readFile(join(runtimeRoot, "bin", "pi"), "utf8");
  assert.ok(helperWrapper.startsWith(`#!${process.execPath}\n`));
  assert.ok(helperWrapper.includes(JSON.stringify(SOURCE_HELPER_ENTRY)));
  assert.ok(piWrapper.startsWith(`#!${process.execPath}\n`));
  assert.ok(piWrapper.includes(JSON.stringify(SOURCE_PI_ENTRY)));

  const packageLib = join(packageRoot, "lib", "steward");
  assert.equal(await readlink(join(packageRoot, "bin", "steward-pi-helper")), join(runtimeRoot, "bin", "steward-pi-helper"));
  assert.equal(await readlink(join(packageRoot, "bin", "pi")), join(runtimeRoot, "bin", "pi"));
  assert.equal(await readlink(join(packageLib, "runtime")), join(runtimeLib, "runtime"));
  assert.equal(await readlink(join(packageLib, "node_modules")), join(runtimeLib, "node_modules"));
  assert.equal(await readlink(join(packageLib, "package.json")), join(runtimeLib, "package.json"));
});

test("package smoke rejects missing explicit packaged roots", () => {
  const result = runSmoke([]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /package[- ]root/i);
});

test("package smoke rejects a package root without its steward executable", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-package-smoke-unit-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const packageRoot = join(root, "package");
  const runtimeRoot = join(root, "runtime");
  await Promise.all([
    mkdir(packageRoot, { recursive: true }),
    mkdir(join(runtimeRoot, "bin"), { recursive: true }),
  ]);
  const result = runSmoke(["--package-root", packageRoot, "--runtime-root", runtimeRoot]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /packaged steward/i);
});

test("package smoke rejects a package root without its paired runtime", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-package-smoke-unit-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const packageRoot = join(root, "package");
  await mkdir(join(packageRoot, "bin"), { recursive: true });
  await writeFile(join(packageRoot, "bin", "steward"), "#!/bin/sh\nexit 0\n");
  const result = runSmoke(["--package-root", packageRoot, "--runtime-root", join(root, "missing")]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /runtime root/i);
});

test("package smoke rejects a Go-only default even with a valid external runtime", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-package-smoke-unit-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const packageRoot = join(root, "package");
  const runtimeRoot = join(root, "runtime");
  const steward = await makeStewardFixture(root);
  await makeRuntimeFixture(runtimeRoot);
  await mkdir(join(packageRoot, "bin"), { recursive: true });
  await symlink(steward, join(packageRoot, "bin", "steward"));
  const result = runSmoke(["--package-root", packageRoot, "--runtime-root", runtimeRoot]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /default packaged helper is unavailable/i);
});

test("package smoke rejects a default Pi wrapper that executes the bundled entry", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "steward-package-smoke-unit-"));
  t.after(() => rm(root, { force: true, recursive: true }));
  const packageRoot = join(root, "package");
  const runtimeRoot = join(root, "runtime");
  const steward = await makeStewardFixture(root);
  await makeRuntimeFixture(runtimeRoot);
  await makeDefaultFixture(packageRoot, runtimeRoot, steward);
  await rm(join(packageRoot, "bin", "pi"));
  await writeNodeWrapper(join(packageRoot, "bin", "pi"), SOURCE_BUNDLED_PI_ENTRY);
  const result = runSmoke(["--package-root", packageRoot, "--runtime-root", runtimeRoot]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Pi wrapper executed the bundled Pi entry/);
});
