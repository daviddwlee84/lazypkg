# Architecture

`cmd/lazypkg` handles process exit and cancellation. `internal/cli` validates
command intent and output mode; `internal/tui` renders asynchronous views and
review/setup overlays. Both call the same `domain.Service` in `internal/app`.

The service selects explicit providers, coordinates bounded concurrent reads,
prepares a reviewable plan, and verifies installation state after mutation.
Ownership enrichment is optional evidence and must not decide whether an
installation succeeded. A manager read error is not an empty inventory.

`internal/backend` owns the version-gated mpm boundary. JSON is a manager-keyed
object with package records and per-manager errors, even when the process exits
zero. Every call receives an explicit temporary JSON configuration. Query
stdout/stderr are captured separately; mutations keep native terminal I/O.
The adapter normalizes a known uvx 8.0.1 entrypoint parsing artifact.
Preview and mutation encode native IDs as manager-scoped pURLs, preserving
Homebrew IDs such as `python@3.13` and npm scopes. Raw `name@version` would be
reinterpreted by mpm. Public commands accept native IDs, not caller-provided
pURLs that could change routing.

Native mise reads retain installed paths and configuration selection. The
service uses exact-version install/uninstall and explicit global `use --pin`;
it never routes row removal through mpm's `uninstall --all`. PyPI lookup is a
small exact-name HTTP adapter, not a search-engine replacement.

`internal/diagnostics` combines manager records with filesystem observations.
Installation records, executable candidates and source evidence remain separate.
Recorded entrypoints outside PATH are visible without becoming PATH winners.
Only known managers and dispatcher queries execute; discovered arbitrary
binaries are not run for version guessing.

`internal/bootstrap` detects and provisions managers without mpm. Plans name
dependencies, official installer URLs and effects. Execution downloads scripts
to temporary files, uses explicit interpreters, verifies the result, and
continues independent steps after failures. Standalone mpm uses a pinned digest.
New known executable directories enter later child environments only.

`internal/process` is the shared executable/argv boundary. It applies explicit
environment overlays, bounds captured output and resolves executables against
the child PATH. No plan preview is evaluated as shell code.

## Upstream contracts

- [mpm v8.0.1 query implementation](https://github.com/kdeldycke/meta-package-manager/blob/v8.0.1/meta_package_manager/cli_explore.py)
- [mpm v8.0.1 mise semantics](https://github.com/kdeldycke/meta-package-manager/blob/v8.0.1/meta_package_manager/managers/mise.py)
- [mpm uv and uvx distinction](https://github.com/kdeldycke/meta-package-manager/blob/v8.0.1/meta_package_manager/managers/uv.py)
- [WinGet list includes other installers](https://learn.microsoft.com/windows/package-manager/winget/list)
- [uv tool lifecycle](https://docs.astral.sh/uv/concepts/tools/)
- [mise configuration selection](https://mise.jdx.dev/configuration.html)

Future mpm version support requires updating contract fixtures and testing the
native commands. Package-manager minimum versions are reported by mpm, not
inferred merely from executable presence.
