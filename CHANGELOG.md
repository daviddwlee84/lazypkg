# Changelog

## Unreleased


## v0.2.1 - 2026-10-16

- Publish the v0.2.0 changes. The v0.2.0 tag has no release: its Go module would have included a committed agent plan, so `.claude/plans` now has the same nested `go.mod` boundary as `.specstory`.

## v0.2.0 - 2026-10-16

- Label available updates by the changed version position (MAJOR/MINOR/PATCH/PRE/REV/CALVER) with a separate `0.x` stability marker, in the Updates view, details pane, `/` filter and `lazypkg updates` TYPE column. Display-only; it never gates upgrades.
- Updates: `o` cycles sort (name/type/provider) and `t` cycles a type filter (all/major/minor/patch/0.x/other); selection and marks are retained.
- Size package-list columns from visible content so long versions are no longer truncated on wide terminals, and align update arrows.

## v0.1.4 - 2026-09-25

- Fix Homebrew upgrades when `brew` is a symlink, including the standard Linuxbrew layout: retain its invocation path while binding and revalidating the resolved manager file.
- Keep the existing exact-formula, receipt, changed-target and no-fallback checks.

## v0.1.3 - 2026-09-25

- Publish macOS/Linux amd64/arm64 binary archives, checksums and a filtered source archive.
- Add the personal Homebrew formula channel with generated Bash/Zsh completions.
- Provide `lazypkg self upgrade` and read-only `--check` through verified Homebrew ownership; preserve standalone/local copies and document their external update paths.
- Verify source/module packaging independently; retain embedded resources and development history outside release payloads.
- Distribute the application under MIT.

## v0.1.2 — 2026-09-25

- Reconcile Homebrew tap identities across inventory, Updates, Discover and
  operations; preserve fully qualified native targets and expose source conflicts.
- Replace checkbox rows with compact selection/restriction markers and separate
  selection intent from live operation verification.
- Reuse TUI observations for the session, warm Updates once at startup, and retain
  paused batch results across navigation and cancelled previews.
- Enable GitHub CLI extensions in global/user inventory, with native per-item
  update checks, bound upgrade/removal plans and local/pinned/modified guidance.
- Add Installed/Updates multi-selection and filtered batch upgrades, a shared
  `upgrade-batch` command, one batch review and pause/recheck after uncertain results.
- Add Vim half/full-page movement across lists and overlays, preserve input
  editing shortcuts, and fix setup scrolling and duplicate coverage messages.
- Stream Installed/Updates provider batches before slow queries and ownership
  enrichment finish; share in-flight work and seed marked stale disk snapshots.
- Add guided conflict assessment and bound removals for Brew formulae, uv tools
  and npm prefixes, including dependencies and retained-entrypoint verification.
- Add a per-job manager maintenance queue and distinguish component failures
  from host launchers; fix Yazi's multiline version probe.
- Generate versioned advisory prompts with preview, clipboard and private file
  export, using the same immutable context.
- Strengthen exact entrypoint attribution, instance-safe stale retention,
  mutation cancellation and Homebrew scoped-removal behavior.

## v0.1.1 — 2026-09-25

- Detect the full pinned mpm catalog, with requirements, capabilities and scope;
  enable Go, RubyGems and rustup in the global/user inventory.
- Explain npm incompatibility and add cached manager update checks, proven-owner
  update plans, and an independent mise/npm repair that preserves Node.
- Join Discover candidates to provider inventory, expose stale/failed observations
  and separate same-name PATH commands from installation evidence.
- Add mouse clicks/scrolling, ordered multi-manager selection, built-in groups
  and saved sets shared by CLI and TUI.
- Fix Go detection, bound native version probes, disable implicit runtime
  installation during reads, and protect inventory from late cancelled results.
- Verify real npm repair and uv package lifecycle in disposable environments;
  add catalog regeneration checks and terminal mouse regressions.

## v0.1.0 — 2026-09-24

- Add a five-view package-management dashboard and matching scriptable CLI.
- Integrate mpm 8.0.1 with manager-scoped plans and installation verification.
- Add explicit mise version installation, removal and global activation.
- Add executable ownership evidence, PATH/shim diagnosis and off-PATH records.
- Add manager setup with normal uv-tool and verified standalone mpm options.
- Add partial-result handling, exact PyPI lookup and terminal handoff tests.
