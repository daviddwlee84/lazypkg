# Architecture

`cmd/lazypkg` handles process exit and cancellation. `internal/cli` validates
command intent and output mode; `internal/tui` renders asynchronous views and
review/setup overlays. Both call the same `domain.Service` in `internal/app`.

The service selects explicit providers, coordinates bounded concurrent reads,
prepares a reviewable plan, and verifies installation state after mutation.
Ownership enrichment is optional evidence and must not decide whether an
installation succeeded. A manager read error is not an empty inventory.

`StreamQuery` publishes whole-provider cache, base and enriched batches followed
by one final aggregate. `Query` collects the same stream for CLI callers. Basic
rows are usable before enrichment finishes; freshness is tracked per provider.
TUI subscriptions consume one event per effect and reject superseded generations.
Package identity includes manager instance/ID/version/scope, excluding derived
roots so later ownership evidence cannot move selection to another row.

Backend resolution, discovery and overlapping provider reads share cancellable
work. Each consumer can unsubscribe independently; no consumers cancels the job.
Package reads share a four-job limit and enrichment a three-job limit. Backend
resolution performs native probes outside application-state locks. Explicit
refresh rechecks the pinned backend and supersedes the relevant cache epoch.

`domain.PackageQuery` is the CLI/TUI selection contract. An explicit manager
list, built-in group or saved ordered set selects providers; environment and
unknown scopes are excluded from package operations. `internal/catalog` embeds
all 149 pinned adapters and their exact capabilities. Its Python generator reads
static metadata only; runtime discovery probes supported platforms with a
five-second native timeout per manager. Scope policy lives in the generator.
Public `uv` aliases `uvx`; `uv-pip` names mpm's environment-specific `uv` adapter.

Inventory coverage records complete/failed/unavailable/unsupported/excluded
states, manager instance and observation time. The service caches inventory for
60 seconds for Installed/Updates and detection for five seconds. Complete
provider batches can also seed startup from a private disk cache up to 24 hours
old. Disk seeds are always stale until live validation; context fingerprints
include platform, cwd, PATH and provider namespace environment settings, with
only the hash persisted. Mutations invalidate memory and disk query data;
request timestamps prevent older concurrent reads from overwriting newer ones.
Discover matches provider + normalized package ID + instance, retaining all
installed versions without comparing them to the remote version. Same-name PATH
observations remain separate. Relevance precedes installed-source preference,
which precedes the selected manager order. TUI inventory generations, cache age
and stable candidate keys preserve selection as asynchronous joins finish.

`internal/backend` owns the version-gated mpm boundary. JSON is a manager-keyed
object with package records and per-manager errors, even when the process exits
zero. Every call receives an explicit temporary JSON configuration. Query
stdout/stderr are captured separately; mutations keep native terminal I/O.
The adapter normalizes a known uvx 8.0.1 entrypoint parsing artifact.
Its isolated mpm config fixes the Go adapter's version subcommand. Read probes
disable mise auto-install and use local Go toolchains to avoid implicit downloads.
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

`internal/maintenance` checks the selected manager executable, its owner,
runtime and prefix. Its 24-hour cache binds those identities, requirements and
configuration fingerprints; force refresh bypasses it. Update plans are rebuilt
and compared before execution, and include a precise target plus postchecks.
Unknown ownership or configuration ambiguity yields guidance without writable
steps. Compatibility failure does not prevent preparing a manager repair.

The npm repair uses `aqua:npm/cli`, checks `engines.node` against both observed
current and global Node runtimes, and chooses a stable compatible version in
the existing npm major. It installs and verifies an independent package before
pinning mise's global npm selection. Original Node/npm files remain intact.
The recipe is enabled on macOS/Linux; Windows npm gets guidance. Other recipes
require owner evidence, such as Homebrew formula paths plus metadata or a uv
standalone receipt; Cargo/rustup proxies and recognized package records alone
are not ownership proof.

`internal/resolution` groups aliases into installation instances and assesses
retention/removal of a focused command. Exact registered entrypoints outrank
runtime containment. Runtime coexistence and unresolved dispatchers remain
distinct from removable application duplicates. Guided removal currently has
reviewed Brew formula, uv tool and POSIX npm-prefix preflights; other removable
providers expose guidance. A plan binds both installations, dependencies, all
affected commands and expected PATH result. Transient inventory timestamps do
not enter operation identity. Execute reconstructs the plan, compares it, runs
one exact removal and verifies the retained/removed records and entrypoints.
Generic package plans also reject failed/stale selected-provider inventory.

Manager metadata identifies the component and the launcher separately. Hosted
plugins cannot become host-upgrade plans. The maintenance queue merges only
proven equivalent update targets and obtains a new plan for every individually
approved operation; native completion does not authorize the next item.

`internal/promptkit` collects typed evidence once and renders a versioned context
plus advisory Markdown. Data strings are serialized without recursive template
evaluation. `internal/promptio` copies or exports those exact rendered bytes;
neither subsystem invokes an agent or accepts generated instructions as commands.

Configuration saves preserve unrelated TOML content and file permissions, with
digest checks, a cooperative lock and atomic replacement. Mouse hit targets
share the rendered layout, and press/release identity checks prevent stale
coordinates or overlay fall-through from approving an action.

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
native commands, pinned generator dependencies and embedded catalog. CI runs
the generator in check mode. Package-manager minimum versions are reported by mpm, not
inferred merely from executable presence.
