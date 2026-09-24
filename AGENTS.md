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
- Query/preflight examples: `go run ./cmd/lazypkg managers --json`,
  `go run ./cmd/lazypkg setup mpm --dry-run --json`.

## Architecture and constraints

- Cobra and Bubble Tea v2 share `domain.Service`, implemented by `internal/app`.
  Keep operation validation and execution out of CLI/TUI handlers.
- `backend` owns mpm schema isolation, native mise version semantics and exact
  PyPI lookup; `diagnostics` adds evidence and resolves executable candidates;
  `bootstrap` provisions managers independently of mpm.
- `Plan` is read-only. `Execute` requires an already-reviewed plan. Keep manager
  and exact package identity explicit; do not use unscoped mpm mutations.
- Adapter-generated pURLs protect native `@` IDs from mpm's version parser;
  preview and execution must use the same encoding and explicit manager selector.
- mpm `uvx` means uv tools; `uv` means uv pip and is excluded from default
  inventory. The public `--manager uv` alias resolves to `uvx`.
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
- This repository has no published release/install channel. Do not claim a
  cross-build proves native Windows/Linux installation or terminal behavior.

Keep this file grounded in the repository as implementation changes.
