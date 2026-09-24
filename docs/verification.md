# Verification record

Local verification on 2026-09-24 used macOS arm64, Go 1.27.0, mpm 8.0.1,
uv 0.11.13 and mise 2026.9.1. mpm was installed into a temporary test venv;
the user's normal uv tool environment was not provisioned or changed.

## Automated checks

- `go test -race ./...`: package, CLI, provider, setup, diagnostics and TUI tests.
- `go vet ./...` and a native `go build ./cmd/lazypkg`.
- Cross-builds for Windows amd64 and Linux amd64.
- `python3 scripts/pty_smoke.py`: a real POSIX PTY with fake service operations.
  Covers delayed reads, query input ownership, manager filtering, review cancel,
  Enter not approving, explicit approval, native stdin, result acknowledgement,
  resize, bracketed paste, and restored ECHO/ICANON/cursor/alternate screen.

Fixtures cover per-manager errors with exit zero, schema/version rejection,
unsupported operations, stale query responses, retained partial refresh data,
exact mutation scoping, no-op updates, child PATH isolation, bootstrap dependency
failure/cancellation, download checksum mismatch, and Windows path/shim cases.
Native `@` package IDs and scoped npm IDs have adapter encoding regressions.

## Native checks

Read-only probes confirmed manager discovery, uv-tool inventory and entrypoint
ownership, mise installed/global/contextual versions, exact PyPI lookup, and
mpm's native command preview.

`diagnose rg` found Homebrew's preferred executable, a separate later executable
bundled with another application, and Homebrew's equivalent keg path. Missing
inherited PATH directories were reported as partial scan issues.

The optional integration script performed a real lifecycle in disposable
`UV_TOOL_DIR` / `UV_TOOL_BIN_DIR` / cache/Python directories:

```sh
python3 scripts/integration_uv.py --mpm /absolute/path/to/mpm
```

It verified empty inventory → non-mutating preview → install pycowsay → installed
record and entrypoint → remove → empty inventory. Temporary directories were
removed afterward. It requires network access and must not be adapted to point
at a user's normal tool directories.

The native mise scope check uses temporary global/project configuration and
installation metadata, without downloading a runtime:

```sh
LAZYPKG_TEST_MISE=/absolute/path/to/mise go test ./internal/backend -run TestMiseLiveGlobalScope -v
```

It verifies that a project/environment selecting node 22 does not hide the
global node 20 selection. Global queries use an isolated directory, an ancestor
discovery ceiling and no per-tool environment version overrides; current-context
queries keep the original context.

## Platform acceptance still required

Native Windows and Linux terminal/installer behavior has not been run locally.
The checked-in CI matrix runs fixtures, race tests, vet and builds on all three
OSes, with the PTY check on POSIX. It has not been claimed as a completed remote
CI run. Windows UAC/PowerShell policy, normal Scoop/WinGet installations and
real apt package mutations still require disposable platform environments.

No project files, shell profiles or package installations on the development
machine were modified by package-operation tests. Source builds are development
artifacts; no lazypkg release, Git tag or package-manager recipe was published.
