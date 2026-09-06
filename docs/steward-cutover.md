# Steward cutover inventory

This branch prepares a canonical Steward rename. It is not a completed global
deployment: existing packages, services, user configuration, and consumers stay
on their current release until the coordinated nix-config cutover.

## Old-to-new inventory

| Surface | Old | Canonical Steward destination |
| --- | --- | --- |
| CLI | `cc-tools` | `steward` |
| Statusline CLI | `cc-tools-statusline` | `steward-statusline` |
| Go module | `github.com/Veraticus/cc-tools` | `github.com/joshsymonds/steward` |
| General environment | `CC_TOOLS_*` and `CLAUDE_HOOKS_*` | `STEWARD_*` |
| Statusline cache duration | `CLAUDE_STATUSLINE_CACHE_SECONDS` | `STEWARD_STATUSLINE_CACHE_SECONDS` |
| Statusline cache directory | `CLAUDE_STATUSLINE_CACHE_DIR` | `STEWARD_STATUSLINE_CACHE_DIR` |
| Statusline gcloud chip | `CLAUDE_STATUSLINE_GCLOUD` | `STEWARD_STATUSLINE_GCLOUD` |
| Statusline width override | `CLAUDE_STATUSLINE_WIDTH` | `STEWARD_STATUSLINE_WIDTH` |
| Statusline Kubernetes chip | `CLAUDE_STATUSLINE_KUBECONFIG` | `STEWARD_STATUSLINE_KUBECONFIG` |
| Statusline hostname chip | `CLAUDE_STATUSLINE_HOSTNAME` | `STEWARD_STATUSLINE_HOSTNAME` |
| Statusline devspace chip | `CLAUDE_STATUSLINE_DEVSPACE` | `STEWARD_STATUSLINE_DEVSPACE` |
| Configuration | `~/.config/cc-tools` | `~/.config/steward` |
| Debug configuration | `~/.claude/debug-config.json` | `~/.config/steward/debug-config.json` |
| State | `~/.local/state/cc-tools` | `~/.local/state/steward` |
| Cache | `~/.cache/cc-tools` | `~/.cache/steward` |
| Runtime socket | `${XDG_RUNTIME_DIR}/cc-tools` (or `/tmp/cc-tools-<uid>`) | `${XDG_RUNTIME_DIR}/steward` (or `/tmp/steward-<uid>`) |
| Debug logs | `/tmp/cc-tools-*.log` | `/tmp/steward-*.log` |
| User service | `cc-tools-notifyd` | `steward-notifyd` |
| Flake package/app | `cc-tools` | `steward` |

Native input variables such as `COLUMNS`, `AWS_PROFILE`, and `KUBECONFIG` keep
their existing meanings. The statusline override variables listed above are
Steward-only; old override names are not runtime fallbacks. The historical
`~/.claude/debug-config.json` is neither read nor changed. It remains intact
while debug configuration is loaded and saved at the canonical XDG path.

## Coordinated release work still required

The destination deployment surface is
`nix-config/home-manager/steward/default.nix`, including the shared package,
service, environment, and secret-file wiring. The release must also update:

- `flake.nix` and `flake.lock`
- `home-manager/common.nix`
- `home-manager/claude-code/default.nix`, `settings.json`,
  `hooks/aws-profile-mirror.sh`, and `hooks/README.md`
- `home-manager/pi/default.nix`, `cc-tools.ts`, and `cc-tools.test.ts`
- `home-manager/codex/managed-config.nix`
- `home-manager/hosts/shrike.nix`
- `home-manager/starship/default.nix`
- `home-manager/statusline-aliases/default.nix`

Packaging still must physically pin the helper and extension to the same Pi
0.85.0 / subagents 0.19.0 peer dependency graph with a minimal `PATH`. Consumer
cutover still needs native root Stop hook trust and its exact user
`trusted_hash`, labels and quota production wiring, live verification, review,
and release. Retire the old daemon only during that coordinated cutover; do not
migrate or delete user state independently.
