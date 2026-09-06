# Steward Nix packaging

The flake exposes `packages.steward-pi-runtime` and includes it in `packages.default` (`steward`). The runtime owns `bin/pi`, `bin/steward-pi-helper`, and `lib/steward/{runtime,node_modules,package.json}`. `pi` starts the official unbundled `@earendil-works/pi-coding-agent/dist/cli.js` with Node 24; it is not the npm bundled CLI.

The runtime prepares the locked package graph before copying it: the Pi SDK, AI, TUI, and pi-subagents 0.19.0 resolve from one physical graph. Its evaluated `extensionRoot` and `nodeModules` passthru paths identify that graph. The Pi wrapper PATH contains its helper plus `steward`, `rg`, and `fd`; the helper is an absolute Node 24 wrapper.

Build and run the isolated packaged smoke probe from a checkout:

```sh
runtime=$(nix build .#steward-pi-runtime --no-link --print-out-paths)
package=$(nix build .#default --no-link --print-out-paths)
node runtime/package-smoke.mjs --package-root "$package" --runtime-root "$runtime"
```

The smoke probe requires the default package itself to provide the helper, Pi wrapper, runtime assets, and SDK/AI/TUI/subagents manifests. It compares their resolved file paths with the explicit paired runtime, then runs the default package's `bin/pi --version`. A test-only `NODE_OPTIONS --import` observer records the actual CLI entry and Node executable: the entry must be the runtime's unbundled `dist/cli.js`, never `dist/bundle/cli.js`, and the recorded Node must be version 24. That recorded packaged Node runs the physical graph identity check and the real generic-child construction probe with an isolated offline home and blocked network APIs.

The real default helper wrapper is exercised separately with a test-only Node `registerHooks` model fixture. The fixture redirects only the coding-agent model runtime and records one controlled call. No model, authentication, ntfy, or deployment service is contacted.

Consumer configuration does not yet replace its existing Pi package. That coordinated cutover remains responsible for pointing Pi at the runtime's evaluated extension root and for user-level wiring.
