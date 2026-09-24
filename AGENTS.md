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
  PyPI lookup, plus native GitHub extension inventory/update checks/actions;
  `diagnostics` adds evidence and resolves executable candidates;
  `bootstrap` provisions managers independently of mpm.
- `catalog` embeds every pinned mpm adapter. All supported-platform managers
  are detected, but only reviewed global/user scopes allow package queries and
  mutations. Change generator scope policy and tests before enabling another.
- `maintenance` checks the selected manager's owner/runtime and prepares updates.
  Ambiguous ownership yields guidance; npm repair uses independent mise npm after
  checking current/global Node compatibility, preserving bundled npm and Node.
- `domain.PackageQuery` carries shared manager/group/set scope. Saved sets preserve
  order; Discover joins by manager instance and package ID, never by latest version.
- Default service inventory caching expires at 60 seconds; TUI `CacheSession`
  retains observations until explicit refresh/context change/mutation. Manager
  health expires at 24 hours. Failed reads retain their failure evidence; tab
  navigation does not retry them. Mutation invalidates generations so late reads
  cannot repopulate caches. Updates warms once after the first Installed base.
- `StreamQuery` publishes whole-provider cache/base/enriched batches and a final
  aggregate. `Query` collects that stream. Disk snapshots up to 24 hours old are
  always unverified seeds; context/instance changes invalidate their identity.
  Package keys exclude derived ownership roots so enrichment retains selection.
- `resolution` assesses all removable global/user providers and implements
  bound Brew/uv-tool/npm-prefix removal preflights. Exact entrypoint ownership
  outranks runtime-root inference; reverse dependencies and uncertain effects
  block guided removal. No force/cascade/autoremove, or parent-shell PATH edits.
- `promptkit` separates typed context collection from pure Markdown rendering;
  `promptio` copies/exports that exact payload. Prompts never launch an agent or
  authorize a write, and contain no raw environment/configuration dumps.
- Manager maintenance distinguishes the versioned component from its launcher.
  Never offer upgrading zsh/Python/Neovim merely because an adapter uses it.
  Queue writes remain individually approved and freshly planned.
- `Plan` is read-only. `Execute` requires an already-reviewed plan. Keep manager
  and exact package identity explicit; do not use unscoped mpm mutations.
- `PlanBatchUpgrade` freezes explicit installation records; `ExecuteBatchUpgrade`
  holds the shared writer lock and calls the internal unlocked executor for
  sequential single-package operations. Never use upgrade_all. Revalidate each
  target; failure, drift or uncertain verification pauses remaining work. Resume
  requires a fresh overview and approval. mise versions coalesce without activation.
- Selection expresses intent and has no age deadline. Known upgrade restrictions
  render `!`, marks render `✓`, and ordinary rows leave the marker blank. Unknown
  metadata is checked during planning. Keep marker/hint/mouse eligibility shared;
  removal has separate restrictions. Preserve executed batch results across drafts.
- Homebrew package IDs are canonicalized before base publication and joins using
  native metadata and receipts. Installed aliases never establish catalog identity.
  Never match taps by basename. Plans retain logical canonical IDs and bind the
  fully qualified native selector in `ProviderTarget`, including core targets.
  Item-bound identity issues cannot authorize that item; only proven distinct
  native slots can be excluded from a selected target's completeness check.
- `gh-ext` is a hosted adapter for `gh extension`, displayed as `gh ext`.
  Inventory roots, host/config environment and launcher bind the manager instance.
  Native updates require detected per-item `--dry-run` support. Repo/host/root,
  pin state and full installed reference bind plans; preserve local/dirty/pinned
  records and their restrictions. Pinned remote removal is an explicit separate
  action. Never fall back to `gh extension upgrade --all`.
- Adapter-generated pURLs protect native `@` IDs from mpm's version parser;
  preview and execution must use the same encoding and explicit manager selector.
- mpm `uvx` means uv tools; `uv` means uv pip and is excluded from default
  inventory. Public `uv` aliases `uvx`; public `uv-pip` names the passive uv-pip
  adapter. mpm's Go version probe is overridden to use `go version`.
- Do not use mpm's all-versions mise removal or Scoop purge as single-row removal.
  Runtime installation and global activation are separate operations.
- WinGet recognition is not installer history. PATH collision is not proof of
  same-tool duplication; shim aliases and intentional runtime versions differ.
- Default scope is global/user; no project/venv scans, usage tracking or automatic
  PATH repairs. Guided removal is limited to explicitly selected conflicts.
- Normal uv tool environment is the chosen default for mpm provisioning. Never
  install or remove real user software during generic tests; use fakes or a
  disposable child environment. Read-only local manager probes are appropriate.
- TUI effects carry generations; refresh retains selection by identity. Native
  interactive actions release the terminal and retain results until acknowledged.
- Mouse targets share rendering geometry and validate press/release identity;
  overlay clicks must not fall through to package actions.
- Paging uses the active rendered viewport: Ctrl+d/u half-page, Ctrl+f/b and
  PgDn/PgUp full-page. Text-input and native-terminal editing take precedence.
  Package marks persist across text filters but clear when manager scope changes.
- This repository has no published release/install channel. Do not claim a
  cross-build proves native Windows/Linux installation or terminal behavior.

Keep this file grounded in the repository as implementation changes.
