# Project agent guidance

lazypkg is a Go CLI/TUI for global/user software inventory, selecting package
installation sources, and executable ownership/PATH diagnosis across macOS,
Windows and Linux. It uses mpm 8.0.1 subprocess JSON as its main package backend.

## Commands

- Go 1.26+; module/dependency versions are pinned in go.mod/go.sum.
- Run: `go run ./cmd/lazypkg`; build: `go build -o bin/lazypkg ./cmd/lazypkg`.
- Test: `go test ./...`; race: `go test -race ./...`; static checks: `go vet ./...`.
- POSIX terminal verification: `python3 scripts/pty_smoke.py` (temporary fake
  service, no real installations).
- Catalog parity: `python scripts/generate_catalog.py --check` in an environment
  installed from `scripts/catalog-requirements.txt`; generation never probes tools.
- Opt-in isolated native checks: `python3 scripts/integration_uv.py --mpm PATH`
  and `python3 scripts/integration_manager_npm.py --mpm PATH --mise PATH --node PATH`.
- Query/preflight examples: `go run ./cmd/lazypkg managers --json`,
  `go run ./cmd/lazypkg setup mpm --dry-run --json`.

## Architecture and constraints

- Cobra and Bubble Tea v2 share `domain.Service`, implemented by `internal/app`.
  Keep operation validation and execution out of CLI/TUI handlers.
- `backend` owns mpm schema isolation, native mise version semantics and exact
  PyPI lookup; `diagnostics` adds evidence and resolves executable candidates;
  `bootstrap` provisions managers independently of mpm.
- `catalog` embeds every pinned mpm adapter. All supported-platform managers
  are detected, but only reviewed global/user scopes allow package queries and
  mutations. Change generator scope policy and tests before enabling another.
- `maintenance` checks the selected manager's owner/runtime and prepares updates.
  Ambiguous ownership yields guidance; npm repair uses independent mise npm after
  checking current/global Node compatibility, preserving bundled npm and Node.
- `domain.PackageQuery` carries shared manager/group/set scope. Saved sets preserve
  order; Discover joins by manager instance and package ID, never by latest version.
- Inventory caches expire at 60 seconds and manager health at 24 hours. Failed
  reads retain explicitly stale observations. Mutation invalidates generations
  so late reads cannot repopulate caches.
- `Plan` is read-only. `Execute` requires an already-reviewed plan. Keep manager
  and exact package identity explicit; do not use unscoped mpm mutations.
- Adapter-generated pURLs protect native `@` IDs from mpm's version parser;
  preview and execution must use the same encoding and explicit manager selector.
- mpm `uvx` means uv tools; `uv` means uv pip and is excluded from default
  inventory. Public `uv` aliases `uvx`; public `uv-pip` names the passive uv-pip
  adapter. mpm's Go version probe is overridden to use `go version`.
- Do not use mpm's all-versions mise removal or Scoop purge as single-row removal.
  Runtime installation and global activation are separate operations.
- WinGet recognition is not installer history. PATH collision is not proof of
  same-tool duplication; shim aliases and intentional runtime versions differ.
- Default scope is global/user; no project/venv scans, usage tracking, cleanup
  recommendations or automatic PATH repairs.
- Normal uv tool environment is the chosen default for mpm provisioning. Never
  install or remove real user software during generic tests; use fakes or a
  disposable child environment. Read-only local manager probes are appropriate.
- TUI effects carry generations; refresh retains selection by identity. Native
  interactive actions release the terminal and retain results until acknowledged.
- Mouse targets share rendering geometry and validate press/release identity;
  overlay clicks must not fall through to package actions.
- This repository has no published release/install channel. Do not claim a
  cross-build proves native Windows/Linux installation or terminal behavior.

Keep this file grounded in the repository as implementation changes.
