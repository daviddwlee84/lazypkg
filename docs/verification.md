# Verification record

## Unreleased workflows — 2026-09-25

The implementation retains mpm 8.0.1 and was checked with the active mise Go
1.27.0 environment. A login shell selected Homebrew Go 1.27.1 while retaining
mise's GOROOT; checks therefore used the inherited, consistent environment.
No shell configuration or toolchain installation was changed to run tests.

The opt-in read-only profiling test records service events, not terminal paint
times. It isolates Rustup settings and query caches:

```sh
LAZYPKG_BENCH_MPM=/absolute/path/to/mpm go test ./internal/app -run TestLiveProgressiveQueries -count=1 -v
```

| Query | First rows | First live rows | Complete |
|---|---:|---:|---:|
| Cold Installed (527 records) | 4.914 s | 4.914 s | 7.448 s |
| Installed after reopening | 1 ms cached | 1.669 s | 3.700 s |
| Cold Updates (252 records) | 2.307 s | 2.307 s | 19.638 s |
| Updates after reopening | under 1 ms cached | 2.705 s | 21.918 s |

Earlier aggregate-only CLI samples took 13.290 s for Installed and 22.050 s for
Updates. These are local samples with varying native/network caches, not a
controlled universal speedup claim. uv's configured PyPI mirror retried a failed
request for about 20 seconds; progressive delivery lets other managers finish
visibly while that failure remains isolated and reported.

Race and fixture coverage checks fast-provider publication before a blocked
provider, shared-work cancellation, complete base data before enrichment, disk
seeds and empty replacement, cache invalidation, stable selection, component
version identity, manager queue grouping, exact-prefix removal, and prompt
render/export consistency. The real fake-service PTY additionally exercises
guided removal, maintenance skip/recheck and prompt export, with three explicitly
approved fake mutations and restored terminal state.

Native conflict inspection identified two yt-dlp installations: uv tools and
one Brew keg with two equivalent paths. Brew's copy is required by `summarize`,
so that removal is blocked. Other observed cases include Brew/uv `thefuck` and
`pre-commit`, and npm global commands in separate prefixes. Runtime coexistence
and unresolved dispatchers remain distinct from removable duplicates.

The actual CLI assessment showed two installations and grouped Brew's two paths.
Two successive dry-run plans retaining Brew and removing uv were identical;
prompt rendering preserved the `summarize` blocker. Neither plan was executed.

The disposable npm resolution test passed using physically copied Node and npm
with two private prefixes and a local fixture package. It checked read-only
planning, dependency graph/impact assessment, an exact-prefix uninstall and
retained-entrypoint verification. Original Node/npm/npmrc hashes were unchanged:

```sh
LAZYPKG_NATIVE_NPM_NODE=/absolute/path/to/copyable/node \
LAZYPKG_NATIVE_NPM_CLI=/absolute/path/to/npm/bin/npm-cli.js \
  go test ./internal/resolution -run TestNativeNPMDisposablePrefixes -count=1 -v
```

This requires a relocatable Node executable (the tested mise distribution works;
copying only a Homebrew Node executable may miss its shared library). Native
Homebrew/uv removals are covered by fixtures, not by removing user installations.

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
