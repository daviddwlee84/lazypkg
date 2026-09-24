# lazypkg

A terminal software inventory and package-manager dashboard. See what is
installed, choose an installation source, and understand which executable PATH
will find. Go provides the CLI/TUI; [Meta Package Manager](https://mpm.run/)
provides most package operations.

This is an unreleased development version targeting macOS, Windows and Linux.
Native macOS read-only checks and an isolated uv tool install/remove workflow
are part of local verification. Windows/Linux native installer and terminal
acceptance must be run on those platforms; cross-compilation is not that proof.

## Run from this checkout

Requires **Go 1.26 or newer**. No Python is needed to build lazypkg.

```sh
go run ./cmd/lazypkg
# Or build a local executable:
go build -o bin/lazypkg ./cmd/lazypkg
```

If mpm is missing, press `s` for setup. Select **mpm (uv tool)** to install
`meta-package-manager==8.0.1` in your normal uv tool environment. uv is added
to the reviewed plan if missing, and may download Python for mpm. Existing
incompatible installations are never silently replaced. The alternative
**mpm standalone** downloads the official executable, verifies its pinned
SHA-256 and version, and stores it in lazypkg's data directory.

Setup can also install Homebrew on macOS, Scoop on Windows, and uv/mise on all
three platforms. WinGet/Chocolatey bootstrap and repair use official guidance.
There is no requirement to install Scoop when WinGet already serves your needs.
All setup changes have one review step, dependency ordering, per-item results,
and post-install checks. Cancelling does not roll back completed installations.

The backend contract currently accepts **mpm 8.0.1**. Configure `--mpm PATH`,
`LAZYPKG_MPM`, or `mpm_path` to select an existing executable. A normal mpm
installation is discoverable through PATH or uv's tool bin directory.

## Dashboard

Five views: **Installed**, **Discover**, **Updates**, **Diagnostics**, **Managers**.
Wide terminals include a detail pane; `Enter` opens scrollable details at any
usable width. Manager filters, selection and queries survive view changes.

| Key | Action |
|---|---|
| `↑/↓`, `j/k`, `gg/G` | Select / first / last item |
| `←/→`, `h/l`, `1`–`5` | Switch view |
| `Tab`, `Shift+Tab` | Switch manager/list focus |
| `/` | Filter locally; in Discover, type and Enter to search |
| `Enter` | Details / accept filter |
| `i`, `u`, `x` | Review install, upgrade, removal when supported |
| `a` | Review global activation of an installed mise version |
| `d` | Diagnose a selected package's first known command |
| `s`, `r`, `e`, `?` | Setup, refresh, errors/coverage, help |
| `Esc`, `q` | Back/cancel, quit |

Typing owns printable keys. A package action first prepares an exact plan;
`y` executes it and `Esc` cancels. Native prompts run with the TUI terminal
released, then an acknowledgement returns to the dashboard. Views remain
usable during reads; failed providers do not erase their previously shown
installed records.

## Scriptable commands

Examples below use a built `lazypkg` executable; `go run ./cmd/lazypkg` is equivalent.

```sh
lazypkg list --json
lazypkg list --manager brew
lazypkg search ripgrep
lazypkg install ripgrep --manager brew --dry-run
lazypkg install ripgrep --manager brew --yes
lazypkg updates --manager brew
lazypkg upgrade ripgrep --manager brew --dry-run
lazypkg remove ripgrep --manager brew --dry-run
lazypkg diagnose rg --json
lazypkg managers --json
lazypkg setup --json                 # list setup choices without a prompt
lazypkg setup mpm mise --dry-run
lazypkg setup mpm --yes
lazypkg completion zsh
```

`--manager` is mandatory for package mutations. `--json` keeps stdout free of
native command logs. Non-interactive mutation requires `--yes`; `--dry-run`
performs read queries but applies no changes. Native managers may still require
a real terminal for credentials. Exit codes: 0 success, 1 runtime failure,
2 usage/configuration error, 130 cancellation.

mise operations are version-aware:

```sh
lazypkg install node --manager mise --version 22 --dry-run
lazypkg activate node --manager mise --version 22.0.0 --dry-run
lazypkg remove node --manager mise --version 22.0.0 --dry-run
```

Install/update resolves a concrete version and downloads it without changing
project configuration or pruning old versions. Activation explicitly writes
the global mise configuration. Project overrides may still select another
version, and the current parent shell cannot be changed by a child process.
Removal leaves configuration references intact and warns about known selections.
If a newer mise version is already installed, Updates offers activation of that
version instead of repeating its installation. Global selection remains visible
even when the current project or environment selects a different version.
Scoop removal targets the current-user installation and retains persisted data
rather than using mpm's `--purge` behavior; global Scoop removal is not exposed.

## Coverage and evidence

The supported core includes Homebrew formulae/casks, apt, WinGet, Scoop,
Chocolatey, npm globals, uv tools, Cargo and mise. DNF, pacman, Flatpak, Snap
and pipx use the same mpm boundary. A detected manager must also satisfy mpm's
version requirement; unsupported actions remain unavailable.

- **Global scope:** no project dependency/venv scan or inactive npm-context sweep.
  System inventories may include libraries and dependencies as well as CLI tools.
- **uv tools:** UI/CLI alias `uv` maps to mpm `uvx`, not `uv pip`. Search is an
  exact PyPI name lookup, not full-text search. PyPI metadata does not establish
  that a project supplies CLI entrypoints. The adapter filters a verified mpm
  8.0.1 parsing artifact that treats entrypoint lines beginning with `v` as
  packages named `-`.
- **Cargo:** inventory/search/install/remove; no mpm update support.
- **Missing managers:** no remote catalog is invented for a manager that is not
  installed. Search coverage and failures remain visible.
- **Provenance:** recorded ownership, recognized/manageable software, inferred
  paths and unknown origin are distinct. WinGet recognition does not prove the
  original installer. Counts describe installation records, not unique apps.
- **PATH diagnostics:** inherited environment and current directory, including
  Windows PATHEXT, symlinks, known shims and registered off-PATH entrypoints.
  Same-target aliases are not duplicate installations. Same command names can
  refer to different tools. Multiple mise versions can be intentional.
- **Limits:** no parent-shell alias/function/cache introspection, no whole-disk
  history reconstruction, no usage tracking, cleanup scoring or automatic PATH
  repair. Cargo nonstandard configured roots and unsupported ownership sources
  can remain unknown. Reads are bounded and partial coverage is reported.

## Configuration

No config file is required. On macOS/Linux, use
`$XDG_CONFIG_HOME/lazypkg/config.toml` (default `~/.config/lazypkg/config.toml`).
On Windows, use `%APPDATA%\lazypkg\config.toml`. `--config PATH` selects a file
explicitly; a missing explicit file is an error.

```toml
# Optional explicit backend:
# mpm_path = "/path/to/mpm"
timeout_seconds = 30
# Optional default query selection; an explicit --manager overrides it:
# managers = ["brew", "cask", "mise", "uvx"]
```

`lazypkg config show` prints effective paths/settings. Explicit flags override
`LAZYPKG_MPM`, which overrides the file. Standalone mpm lives below
`$XDG_DATA_HOME/lazypkg` (default `~/.local/share/lazypkg`) or
`%LOCALAPPDATA%\lazypkg\data`. Relative XDG variables are ignored.

lazypkg isolates mpm's own configuration and `MPM_*` overrides. Native managers
retain their user configuration. New setup paths are added only to later child
process environments; existing project/runtime PATH entries are retained.

## Development and verification

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/lazypkg
python3 scripts/pty_smoke.py
# Optional native checks in disposable environments (requires installed tools):
python3 scripts/integration_uv.py --mpm /absolute/path/to/mpm
LAZYPKG_TEST_MISE=/absolute/path/to/mise go test ./internal/backend -run TestMiseLiveGlobalScope -v
```

The PTY script is POSIX-only and uses a fake service with temporary receipts;
it does not install software. Unit tests use fake runners, temporary files and
HTTP fixtures. CI runs tests/builds on macOS, Ubuntu and Windows, and the PTY
check on macOS/Linux.

There are no published lazypkg releases or verified package-manager recipes
yet. Rebuild this checkout to update a development binary. An application
self-updater is deliberately deferred until versioned installation channels
exist; `upgrade` currently updates a selected managed package, not lazypkg.

See [architecture](docs/architecture.md) and [verification](docs/verification.md).
