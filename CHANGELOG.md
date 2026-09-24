# Changelog

## Unreleased

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
