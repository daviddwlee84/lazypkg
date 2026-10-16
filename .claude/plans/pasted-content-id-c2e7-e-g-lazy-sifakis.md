# Updates view: show the update type (major/minor/patch) and stop truncating versions

## Context
In the Updates tab (311 rows), every row looks the same: you can't tell a major upgrade
(`cmake 4.2.3 → 4.4.4`, `bundler 2.6.9 → 4.0.23`) from a patch or a Homebrew rebuild
(`ada-url 4.0.0 → 4.0.0_1`). Long versions are also cut off with `…` even on a very wide
terminal, because `packagePane` limits the right column to `min(25, available/2)`
(`internal/tui/view.go:259`). The label column gets all the extra width while the version
text gets truncated.

On git: local `main` is **3 commits behind `origin/main`**. Those commits are our own work
(d97841f binary distribution + verified brew upgrades, d5a1ef2, 703d1d0 brew symlink
fix). They are not an awesome-list change. The only local changes are in `.specstory/`,
and none of those files are touched upstream.

## Step 0 — sync first
`git pull --ff-only origin main` (fast-forward; the local .specstory edits don't overlap).
Then create a branch, e.g. `feat/update-kind-columns`.

## Step 1 — update classification (domain, pure + tested)
New `internal/domain/updatekind.go`:
- `type UpdateKind string` with values `major`, `minor`, `patch`, `revision`, `unknown`.
- `ClassifyUpdate(installed, latest string) UpdateKind`, which is tolerant:
  - trim `v` prefix; split cask `version,build` on `,` (compare the first part, and if only
    the build part differs → `revision`); split brew `_N` revision (`4.0.0`→`4.0.0_1` → `revision`);
    drop `-pre`/`+meta` suffixes for the numeric core.
  - compare dot-separated numeric segments: first differing index 0→major, 1→minor, ≥2→patch.
  - semver 0.x rule: when major is 0, a minor change counts as `major` (it may break things)
    and a patch change counts as `minor`.
  - calendar versions (first segment ≥ 1000, or `YYYY-MM-DD`, e.g. ca-certificates, certifi,
    cloudflared) → `unknown`. These are routine updates, so flagging them as major would be noise.
  - empty/non-numeric/latest ≤ installed → `unknown`.
- Add a method `(Package) UpdateKind()` that calls it with `Version`/`Latest`. Don't add a stored
  field, so caches and snapshots stay unchanged.
- Table test `updatekind_test.go` covering the real rows from the screenshot (ada-url, cmake,
  bundler, chafa `1.18.2_1→1.18.3`, cursor cask commit builds, ca-certificates dates, gh ext
  `v4.26.0→v4.26.x`, 0.x tap packages).

## Step 2 — Updates view rendering (`internal/tui`)
- `model.go:978` `rows()`: for `updatesView`, keep `secondary` as the plain text
  `installed → latest`, plus `(stale)` when the row is stale, so the `/` filter keeps working. Add
  the kind word (`major`, …) to the filter haystack (`model.go:998`) so `/major` narrows the list.
  Store the kind on `row` (new field `kind domain.UpdateKind`).
- `view.go:221` `packagePane`: replace the fixed `min(25, available/2)` with content-aware
  widths computed once over the **visible** rows:
  - `need = max display width of secondary (+ provider prefix)` and `labelNeed = max label width`.
  - right column = `need`, and the label column gets the rest. If the two don't fit, keep the
    label at `min(labelNeed, max(available/3, 16))` and give the remainder to the right column.
    Truncate only then. On wide screens like the screenshot nothing is truncated.
  - Updates view gets aligned sub-columns: `provider │ installed → latest │ kind`, with the
    provider and installed parts padded to their max width so the arrows line up. Short badge
    at the end: `MAJOR` (warningStyle), `minor` (accent), `patch` / `rev` (muted), blank for unknown.
    Keep the badge out of the selected-row background clash (render the badge after the row's
    style is applied, or style the whole line consistently; follow how `selectedStyle` is
    applied today).
- Details pane `view.go:366`: add `Update type: major` (etc.) under Available version.
- Other package views (Installed/Discover) also use the content-aware width, which fixes
  truncation there too. Managers/Diagnostics keep their current behaviour.
- Optional but cheap: a header line hint like `Item · 311 · 42 major · 120 minor · …` in
  Updates view (uses the same classifier).

## Step 3 — CLI parity
`internal/cli/root.go:200` updates table: add a `TYPE` column; JSON output gets
`update_kind` (add a `json:"update_kind,omitempty"` field only on the CLI output struct, or
compute it if the command already marshals `domain.Package`. Check which before adding a field).

## Step 4 — docs
One line in README/help (`actions.go:394` help text if useful) and AGENTS.md: "Updates
classify major/minor/patch/revision from version text; calendar/unparseable versions are
unclassified; classification is display-only and never gates upgrades."

## Non-goals
No sorting changes, and no gating or auto-selection based on the kind. Batch upgrade eligibility
(`upgradeEligibility`) stays unchanged.

## Verification
- `go test ./...`, `go test -race ./internal/tui ./internal/domain`, `go vet ./...`.
- New tests: classifier table test; `packagePane` render test at width ~200 asserting a long
  cask version (`1.21.9,4905428782…`) appears untruncated and arrows align; narrow width (~80)
  still renders without overflow (reuse existing view tests in `model_test.go`/`paging_test.go`).
- `python3 scripts/pty_smoke.py`.
- Manual: `go run ./cmd/lazypkg`, open tab 3, check badges/alignment, `/major` filter, details pane;
  `go run ./cmd/lazypkg updates` (or the existing updates subcommand) shows TYPE.
