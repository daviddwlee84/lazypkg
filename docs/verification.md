# Verification record

## v0.1.1 — 2026-09-25

Local verification used macOS arm64, Go 1.27.0, mpm 8.0.1, uv 0.11.13 and
mise 2026.9.1. Native probes inherited the active shell environment rather than
starting a login shell that would select different Node/npm executables.

Automated checks:

- `go test -race ./...`, `go vet ./...`, native build and Windows/Linux cross-builds.
- `python3 scripts/pty_smoke.py`: real POSIX PTY with a fake service. Covers
  delayed reads, filters, review/cancel, exactly one approved mutation, native
  stdin, acknowledgement, resize, bracketed paste and terminal restoration.
  SGR mouse tests cover tabs, rows, wheel scrolling, filter application, modal
  isolation and clicks that must not approve an action.
- `python scripts/generate_catalog.py --check` in the pinned generator environment
  (local Python 3.13.3). The artifact has 149 adapters, including 144 maintained
  adapters and 18 enabled global/user scopes. CI also checks generation on Python
  3.14; the remote CI run is not claimed as locally completed.

Regressions cover provider/package/instance inventory joins, multiple installed
versions, empty versus failed inventories, positive-to-negative joins, stale
observation age, late results after invalidation and out-of-order concurrent
reads. Selection tests cover initial CLI scope before async preferences arrive,
ordered multi-selection, manager-order-aware groups, explicit saved-set order,
atomic TOML preservation, concurrent config edits and an explicit install target
outside the default set. Probe fixtures check missing/unsupported capabilities,
Go's native version command and disabled implicit runtime downloads.

Read-only native checks found 31 detected managers. Go 1.27.0, RubyGems 3.6.9 and
rustup 1.29.0 became available; their inventories returned 2 Go records, 91 gems
and 2 toolchains. Rustup's native list command can rewrite its settings, so that
check used a private RUSTUP_HOME with copied settings and existing toolchains.
Go records correctly report recognized build metadata, not installer history.

Searching `herdr` through Brew and mise returned two separate candidates, both
without an installed record from that provider. Both exposed the existing
same-name PATH command without inventing ownership or an available version.

The selected npm 11.6.2 was correctly rejected by mpm's >=11.10.0 requirement.
Its health check identified mise Node 24.13.0 and selected compatible npm 11.20.0
at the time of verification. Planning was read-only. Maintenance fixtures cover
owner/runtime/config/target drift, cached failures, Node engine constraints,
unsupported Windows npm recipes, ambiguous configuration scopes, failed
postchecks, and native update commands for proven owners.

### Disposable native mutations

Both scripts below passed. They require network access and existing tools.

```sh
python3 scripts/integration_uv.py --mpm /absolute/path/to/mpm
python3 scripts/integration_manager_npm.py --mpm /absolute/path/to/mpm --mise /absolute/path/to/mise --node /absolute/path/to/node
```

The uv script verified empty inventory → non-mutating preview → install
pycowsay → installed record and entrypoint → remove → empty inventory, all in
private tool/bin/cache/Python directories.

The npm script physically copied Node 24.13.0 and bundled npm 11.6.2 into a
private mise installation. A real CLI dry-run left configuration unchanged;
`managers upgrade npm --yes` then installed independent npm 11.20.0 and pinned
it globally inside that environment. It verified effective npm selection,
unchanged Node selection, byte-for-byte preservation of copied Node/npm/wrappers,
and unchanged host tools/configuration. The exact temporary tree was removed.
These scripts must not be adapted to use a user's normal installation directories.

## Earlier v0.1.0 verification — 2026-09-24

The initial dashboard passed package/CLI/provider/setup/diagnostics/TUI race
checks, vet, native build, Windows/Linux cross-builds and POSIX PTY verification.
Native checks covered uv inventory/entrypoints, exact PyPI lookup, mpm command
preview and PATH diagnosis. `diagnose rg` distinguished Homebrew's preferred
executable, another application's copy and an equivalent keg path.

The native mise scope test remains available without runtime downloads:

```sh
LAZYPKG_TEST_MISE=/absolute/path/to/mise go test ./internal/backend -run TestMiseLiveGlobalScope -v
```

It verifies that project/environment selection does not hide global selection.
Global reads use an isolated directory, an ancestor-discovery ceiling and no
per-tool environment version overrides; contextual reads keep the original scope.

## Remaining platform acceptance

Native Windows and Linux terminal/installer behavior has not been run locally.
The CI matrix is configured for fixtures, race tests, vet and builds on all three
OSes, with PTY checks on POSIX. Cross-builds do not prove native installation.
Windows UAC/PowerShell policy, normal Scoop/WinGet installations and real apt
mutations still need disposable platform environments. Manager recipes other
than the isolated npm repair have fixture coverage, not a claim of real upgrades
on this development machine.

Local Git tags identify v0.1.0 and v0.1.1; they are not published release channels.
No host package installations or shell profiles were changed by these tests.
