package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

const maxBatchTargets = 5000

// Batch requests freeze the caller's entire selection, including marked rows
// hidden by a UI filter. Native operations remain singular and sequential.
func (a *App) PlanBatchUpgrade(ctx context.Context, req domain.BatchUpgradeRequest) (domain.BatchUpgradePlan, error) {
	p := domain.BatchUpgradePlan{Entries: []domain.BatchUpgradeEntry{}}
	if len(req.Targets) > maxBatchTargets {
		return p, fmt.Errorf("selected %d package rows; the maximum is %d and none were truncated", len(req.Targets), maxBatchTargets)
	}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	if len(req.Targets) == 0 {
		p.Request = req
		p.Context = a.queryContext()
		p.Fingerprint = batchPlanFingerprint(p)
		return p, nil
	}
	a.invalidateDetection()
	managers, err := a.Managers(ctx)
	if err != nil {
		return p, err
	}
	p.Context = a.queryContext()
	p.Request.Source = req.Source
	lookup := map[string]domain.Manager{}
	for _, m := range managers {
		lookup[m.ID] = m
	}
	groups := map[string][]domain.Package{}
	order := []string{}
	ids := []string{}
	seenIDs := map[string]bool{}
	for _, selected := range req.Targets {
		selected = batchPackage(selected)
		selected.Manager = backend.NormalizeManager(selected.Manager)
		if m, ok := lookup[selected.Manager]; ok && selected.Instance == "" {
			selected.Instance = instance(m)
		}
		p.Request.Targets = append(p.Request.Targets, selected)
		key := domain.BatchUpgradeTargetKey(selected)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], selected)
		if m, ok := lookup[selected.Manager]; ok && backend.Known(m.ID) && !seenIDs[m.ID] {
			ids = append(ids, m.ID)
			seenIDs[m.ID] = true
		}
	}
	data, err := a.batchRead(ctx, ids, managers, p.Request.Targets)
	if err != nil {
		return p, err
	}
	// A selection with unknown identity is intent, not authority. Resolve it
	// from this fresh inventory before grouping aliases into a single target.
	groups = map[string][]domain.Package{}
	order = nil
	for i, selected := range p.Request.Targets {
		if (selected.Manager == "brew" || selected.Manager == "cask") && (selected.Identity == nil || selected.Identity.State != "verified") {
			if rows := batchRows(data.installed, selected); len(rows) > 0 && freshPackageInventory(data.installed, rows[0]) {
				selected.ID = rows[0].ID
				selected.Identity = batchPackage(rows[0]).Identity
				p.Request.Targets[i] = selected
			}
		}
		key := domain.BatchUpgradeTargetKey(selected)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], selected)
	}
	eligibilityAt := time.Now()
	for _, key := range order {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		selected := groups[key][0]
		entry := domain.BatchUpgradeEntry{ID: digestJSON(key)[:20], Package: selected, Targets: append([]domain.Package(nil), groups[key]...), State: "excluded"}
		m, ok := lookup[selected.Manager]
		if !ok {
			entry.Reason = "Manager is unavailable on this platform"
			p.Entries = append(p.Entries, entry)
			continue
		}
		if selected.Instance != instance(m) {
			entry.Reason = "The selected manager instance changed; refresh the selection"
			p.Entries = append(p.Entries, entry)
			continue
		}
		if err := Validate(domain.ActionRequest{Operation: "upgrade", Manager: selected.Manager, Package: selected.ID}); err != nil {
			entry.Reason = err.Error()
			p.Entries = append(p.Entries, entry)
			continue
		}
		if reason := domain.BatchUpgradeBlocker(selected, m); reason != "" {
			entry.Reason = reason
			p.Entries = append(p.Entries, entry)
			continue
		}
		rows := batchRows(data.installed, selected)
		if len(rows) == 0 {
			entry.Reason = "The selected installation could not be uniquely matched in fresh inventory; inspect its source and refresh"
			p.Entries = append(p.Entries, entry)
			continue
		}
		if !freshPackageInventory(data.installed, rows[0], eligibilityAt) {
			entry.Reason = "Fresh verified installed inventory is required for this package"
			p.Entries = append(p.Entries, entry)
			continue
		}
		if !batchSelectedRootsMatch(groups[key], rows) {
			entry.Reason = "The selected installation root context changed or could not be verified; refresh the marked rows"
			p.Entries = append(p.Entries, entry)
			continue
		}
		entry.Package = batchPackage(rows[0])
		entry.ObservedVersions = batchVersions(rows)
		entry.Context = batchContext(m, rows)
		update, hasUpdate := batchUpdate(data.outdated, entry.Package)
		if hasUpdate {
			entry.Package.Latest = update.Latest
			entry.Package.LatestInstalled = update.LatestInstalled
			if update.Extension != nil {
				entry.Package.Extension = batchPackage(update).Extension
			}
		}
		eligible, reason := domain.BatchUpgradeEligibilityAt(entry.Package, m, verifiedPackageCoverage(data.installed, entry.Package, eligibilityAt), eligibilityAt)
		// A completed native update check may establish a safe no-op for an
		// otherwise manageable extension. Pinned/local/dirty records stay excluded.
		ghCurrent := reason == "The extension has no verified available update" && m.ID == "gh-ext" && entry.Package.Extension != nil && entry.Package.Extension.BlockedReason == "" && !entry.Package.Extension.Pinned && (entry.Package.Extension.Kind == "git" || entry.Package.Extension.Kind == "binary") && (entry.Package.Extension.Status == "current" || entry.Package.Extension.Status == "not-checked") && m.Supports("outdated") && freshInventory(data.outdated, m.ID) && !hasUpdate
		if !eligible && !ghCurrent {
			entry.Reason = reason
			p.Entries = append(p.Entries, entry)
			continue
		}
		if m.Supports("outdated") {
			if !freshPackageInventory(data.outdated, entry.Package, eligibilityAt) {
				entry.Reason = "Fresh complete update status is required"
				p.Entries = append(p.Entries, entry)
				continue
			}
			if !hasUpdate {
				entry.State = "current"
				entry.Reason = "Fresh update status reports no available upgrade"
				p.Entries = append(p.Entries, entry)
				continue
			}
		}
		selectedUnchanged := true
		for _, target := range groups[key] {
			found := false
			for _, row := range rows {
				if (target.Version == "" || row.Version == target.Version) && (target.Root == "" || batchCanonical(row.Root) == batchCanonical(target.Root)) {
					found = true
					break
				}
			}
			if !found {
				selectedUnchanged = false
			}
		}
		if !selectedUnchanged {
			entry.Reason = "A selected installed version or root changed; refresh the marked rows before reviewing a new batch"
			p.Entries = append(p.Entries, entry)
			continue
		}
		entry.TargetVersion = entry.Package.Latest
		action := domain.ActionRequest{Operation: "upgrade", Manager: m.ID, Package: entry.Package.ID}
		if m.ID == "mise" {
			if entry.TargetVersion == "" {
				entry.Reason = "An exact new runtime version could not be determined"
				p.Entries = append(p.Entries, entry)
				continue
			}
			if batchContains(entry.ObservedVersions, entry.TargetVersion) {
				entry.State = "current"
				entry.Reason = "The target runtime is already installed; activation remains separate"
				p.Entries = append(p.Entries, entry)
				continue
			}
			action.Version = entry.TargetVersion
		}
		single, err := a.Plan(ctx, action)
		if err != nil {
			entry.Reason = err.Error()
			p.Entries = append(p.Entries, entry)
			continue
		}
		if single.Request.Operation != "upgrade" || single.Request.Manager != m.ID || !sameID(m.ID, single.Request.Package, entry.Package.ID) {
			entry.Reason = "Native plan did not preserve the selected singular upgrade"
			p.Entries = append(p.Entries, entry)
			continue
		}
		if m.ID == "mise" && single.Request.Version != entry.TargetVersion {
			entry.Reason = "The exact runtime target changed while planning"
			p.Entries = append(p.Entries, entry)
			continue
		}
		entry.State = "planned"
		entry.Plan = &single
		if len(groups[key]) > 1 {
			entry.Reason = fmt.Sprintf("%d selected rows grouped into one package upgrade", len(groups[key]))
		}
		entry.Fingerprint = batchEntryFingerprint(entry)
		p.Entries = append(p.Entries, entry)
	}
	if a.queryContext() != p.Context {
		return p, fmt.Errorf("the package-manager context changed while planning; review a fresh selection")
	}
	p.Fingerprint = batchPlanFingerprint(p)
	return p, nil
}

type batchData struct{ installed, outdated domain.Snapshot }

func (a *App) batchRead(ctx context.Context, ids []string, managers []domain.Manager, targets []domain.Package) (batchData, error) {
	d := batchData{}
	if len(ids) == 0 {
		return d, nil
	}
	var err error
	d.installed, err = a.read(ctx, domain.PackageQuery{Kind: "installed", Managers: ids, Refresh: true}, false, false)
	if err != nil {
		return d, err
	}
	// Provider inventory is authoritative and complete, while filesystem
	// ownership work is restricted to the selected package IDs.
	selected := []domain.Package{}
	indices := []int{}
	wanted := map[string]bool{}
	for _, target := range targets {
		for _, row := range batchRows(d.installed, target) {
			wanted[row.Key()] = true
		}
	}
	for index, row := range d.installed.Packages {
		if wanted[row.Key()] {
			selected = append(selected, row)
			indices = append(indices, index)
		}
	}
	if len(selected) > 0 {
		enriched, issues := a.diagnosticEngine().Enrich(ctx, selected)
		for index, row := range enriched {
			d.installed.Packages[indices[index]] = row
		}
		for _, issue := range issues {
			issue.Kind = "enrichment"
			d.installed.Issues = append(d.installed.Issues, issue)
		}
	}
	updates := []string{}
	for _, id := range ids {
		for _, m := range managers {
			if m.ID == id && m.Available && m.Supports("outdated") {
				updates = append(updates, id)
				break
			}
		}
	}
	if len(updates) > 0 {
		d.outdated, err = a.read(ctx, domain.PackageQuery{Kind: "outdated", Managers: updates, Refresh: true}, false, false)
	}
	return d, err
}

// Only operational snapshot fields enter the reviewed plan; read timestamps and
// cache/UI fields change on every fresh query and must not invalidate approval.
func batchPackage(p domain.Package) domain.Package {
	out := domain.Package{Manager: p.Manager, ID: p.ID, Name: p.Name, Version: p.Version, Latest: p.Latest, LatestInstalled: p.LatestInstalled, Scope: p.Scope, Root: p.Root, Instance: p.Instance, Active: p.Active, Global: p.Global, ConfigSource: p.ConfigSource, InventoryStale: p.InventoryStale, Candidate: p.Candidate}
	if p.Identity != nil {
		identity := *p.Identity
		identity.Aliases = append([]string(nil), p.Identity.Aliases...)
		out.Identity = &identity
	}
	if p.Extension != nil {
		data, _ := json.Marshal(p.Extension)
		_ = json.Unmarshal(data, &out.Extension)
	}
	return out
}
func batchRows(s domain.Snapshot, p domain.Package) []domain.Package {
	rows := []domain.Package{}
	if (p.Manager == "brew" || p.Manager == "cask") && (p.Identity == nil || p.Identity.State != "verified") {
		matched, err := domain.SelectPackageRecords(s.Packages, p.Manager, p.ID, p.Instance)
		if err != nil {
			return rows
		}
		rows = matched
		sort.Slice(rows, func(i, j int) bool { return rows[i].Key() < rows[j].Key() })
		return rows
	}
	for _, row := range s.Packages {
		if domain.SamePackageIdentity(row, p) && (p.Instance == "" || row.Instance == p.Instance) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key() < rows[j].Key() })
	return rows
}
func batchUpdate(s domain.Snapshot, p domain.Package) (domain.Package, bool) {
	rows := batchRows(s, p)
	if len(rows) == 0 {
		return domain.Package{}, false
	}
	return rows[0], true
}
func batchVersions(rows []domain.Package) []string {
	versions := []string{}
	seen := map[string]bool{}
	for _, p := range rows {
		if !seen[p.Version] {
			seen[p.Version] = true
			versions = append(versions, p.Version)
		}
	}
	sort.Strings(versions)
	return versions
}
func batchContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func batchCanonical(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
func batchRootContext(p domain.Package) string {
	root := p.Root
	if root != "" && (p.Manager == "brew" || p.Manager == "mise") {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			return filepath.Dir(resolved)
		}
		root = filepath.Dir(root)
	}
	// Resolve the parent after dropping the version. An already-upgraded
	// installation's old version directory may no longer exist.
	return batchCanonical(root)
}
func batchSelectedRootsMatch(targets, rows []domain.Package) bool {
	for _, target := range targets {
		if target.Root == "" {
			continue
		}
		found := false
		for _, row := range rows {
			if row.Root != "" && batchRootContext(target) == batchRootContext(row) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func batchContext(m domain.Manager, rows []domain.Package) string {
	contexts := []string{}
	seen := map[string]bool{}
	for _, p := range rows {
		root := batchRootContext(p)
		context := strings.Join([]string{p.Manager, p.Instance, p.Scope, root}, "\x00")
		if p.Extension != nil {
			context += "\x00" + strings.Join([]string{p.Extension.Host, p.Extension.Kind, p.Extension.Root, p.Extension.Launcher, fmt.Sprint(p.Extension.Pinned), p.Extension.BlockedReason}, "\x00")
		}
		if !seen[context] {
			contexts = append(contexts, context)
			seen[context] = true
		}
	}
	sort.Strings(contexts)
	path := batchCanonical(m.Path)
	identity := path
	if info, err := os.Stat(m.Path); err == nil {
		identity = fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
	}
	capabilities := append([]string(nil), m.Capabilities...)
	sort.Strings(capabilities)
	return digestJSON([]any{m.ID, instance(m), m.Version, m.Scope, m.Available, capabilities, identity, contexts})
}
func batchEntryFingerprint(e domain.BatchUpgradeEntry) string {
	e.Fingerprint = ""
	return digestJSON(e)
}
func batchPlanFingerprint(p domain.BatchUpgradePlan) string { p.Fingerprint = ""; return digestJSON(p) }

func (a *App) ExecuteBatchUpgrade(ctx context.Context, p domain.BatchUpgradePlan, in io.Reader, out, errout io.Writer) (domain.BatchUpgradeResult, error) {
	r := domain.BatchUpgradeResult{Entries: []domain.BatchUpgradeItemResult{}}
	for _, entry := range p.Entries {
		state := "pending"
		if entry.State != "planned" {
			state = entry.State
		}
		r.Entries = append(r.Entries, domain.BatchUpgradeItemResult{Entry: entry, State: state, Message: entry.Reason})
	}
	if p.Fingerprint == "" || p.Fingerprint != batchPlanFingerprint(p) {
		r.Paused = true
		r.Remaining = batchRemaining(r.Entries)
		r.Message = "The reviewed batch was modified; prepare a new overview. No operation ran."
		return r, fmt.Errorf("the reviewed batch was modified; prepare a new overview")
	}
	if len(p.Entries) == 0 {
		r.Message = "No package targets were selected; no operations ran."
		return r, nil
	}
	if !a.writeMu.TryLock() {
		r.Paused = true
		r.Remaining = batchRemaining(r.Entries)
		r.Message = "Another operation is running; recheck this batch after it finishes."
		return r, fmt.Errorf("another operation is already running")
	}
	defer a.writeMu.Unlock()
	if out == nil {
		out = io.Discard
	}
	if errout == nil {
		errout = io.Discard
	}
	pause := func(index int, state string, err error) (domain.BatchUpgradeResult, error) {
		r.Entries[index].State = state
		r.Entries[index].Message = err.Error()
		r.Paused = true
		r.Message = "Batch paused. Recheck or skip this item and review the remaining overview; no automatic retry was attempted."
		r.Remaining = batchRemaining(r.Entries)
		return r, err
	}
	for index, entry := range p.Entries {
		if entry.State != "planned" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return pause(index, "cancelled", err)
		}
		if entry.Plan == nil || entry.Fingerprint != batchEntryFingerprint(entry) || entry.Plan.Request.Operation != "upgrade" {
			return pause(index, "drift", fmt.Errorf("the singular upgrade plan changed; review it again"))
		}
		if a.queryContext() != p.Context {
			return pause(index, "drift", fmt.Errorf("the package-manager environment changed since approval"))
		}
		a.invalidateDetection()
		managers, err := a.Managers(ctx)
		if err != nil {
			return pause(index, "unverified", err)
		}
		var m domain.Manager
		for _, candidate := range managers {
			if candidate.ID == entry.Package.Manager {
				m = candidate
				break
			}
		}
		data, err := a.batchRead(ctx, []string{entry.Package.Manager}, managers, []domain.Package{entry.Package})
		if err != nil {
			return pause(index, "unverified", err)
		}
		if !freshPackageInventory(data.installed, entry.Package) {
			return pause(index, "unverified", fmt.Errorf("fresh installed inventory could not be verified for %s", entry.Package.Manager))
		}
		rows := batchRows(data.installed, entry.Package)
		if len(rows) == 0 || batchContext(m, rows) != entry.Context {
			return pause(index, "drift", fmt.Errorf("the selected manager instance or installation context changed for %s", entry.Package.ID))
		}
		update, available := batchUpdate(data.outdated, entry.Package)
		if m.Supports("outdated") && !freshPackageInventory(data.outdated, entry.Package) {
			return pause(index, "unverified", fmt.Errorf("fresh update status could not be verified for %s", m.ID))
		}
		if m.ID == "gh-ext" {
			ext := rows[0].Extension
			if ext == nil || ext.Pinned || ext.Kind == "local" || ext.Kind == "unknown" || ext.BlockedReason != "" {
				return pause(index, "drift", fmt.Errorf("extension metadata no longer permits a batch upgrade; inspect its pin, source and checkout state"))
			}
		}
		if m.ID == "mise" && batchContains(batchVersions(rows), entry.TargetVersion) || m.Supports("outdated") && !available {
			r.Entries[index].State = "current"
			r.Entries[index].Message = "Fresh checks show this target is already current/installed; no upgrade or activation was run."
			continue
		}
		if !reflect.DeepEqual(batchVersions(rows), entry.ObservedVersions) {
			return pause(index, "drift", fmt.Errorf("installed versions changed for %s; review a new overview", entry.Package.ID))
		}
		if available && update.Latest != entry.TargetVersion {
			return pause(index, "drift", fmt.Errorf("the available target changed for %s; review a new overview", entry.Package.ID))
		}
		live := rows[0]
		if available {
			live.Latest = update.Latest
			live.LatestInstalled = update.LatestInstalled
			if update.Extension != nil {
				live.Extension = update.Extension
			}
		}
		if ok, reason := domain.BatchUpgradeEligibility(live, m, verifiedPackageCoverage(data.installed, live)); !ok {
			return pause(index, "drift", fmt.Errorf("%s: %s", entry.Package.ID, reason))
		}
		single, err := a.Plan(ctx, entry.Plan.Request)
		if err != nil {
			return pause(index, "unverified", err)
		}
		if !reflect.DeepEqual(single, *entry.Plan) {
			return pause(index, "drift", fmt.Errorf("the native plan or known effects changed for %s", entry.Package.ID))
		}
		fmt.Fprintf(out, "[%d/%d] %s / %s\n", index+1, len(p.Entries), entry.Package.Manager, entry.Package.ID)
		result, err := a.execute(ctx, single, in, out, errout)
		r.Entries[index].Result = result
		state := batchActionState(result, err, ctx.Err())
		r.Entries[index].State = state
		r.Entries[index].Message = result.Message
		if state == "failed" || state == "unverified" || state == "cancelled" {
			if err == nil {
				err = fmt.Errorf("%s upgrade was not verified", entry.Package.ID)
			}
			return pause(index, state, err)
		}
	}
	r.Message = "Batch completed; each package was checked and handled individually."
	return r, nil
}
func batchActionState(r domain.ActionResult, err, contextErr error) string {
	if contextErr != nil {
		return "cancelled"
	}
	for _, step := range r.Steps {
		if step.Status == "unverified" {
			return "unverified"
		}
	}
	if err != nil {
		return "failed"
	}
	if len(r.Steps) == 0 {
		return "unverified"
	}
	state := "success"
	for _, step := range r.Steps {
		switch step.Status {
		case "success", "updated":
		case "current":
			state = "current"
		case "skipped":
			state = "skipped"
		default:
			return "unverified"
		}
	}
	return state
}
func batchRemaining(entries []domain.BatchUpgradeItemResult) []domain.Package {
	remaining := []domain.Package{}
	for _, entry := range entries {
		switch entry.State {
		case "success", "current", "skipped", "excluded":
			continue
		}
		if len(entry.Entry.Targets) > 0 {
			for _, target := range entry.Entry.Targets {
				remaining = append(remaining, batchPackage(target))
			}
		} else {
			remaining = append(remaining, batchPackage(entry.Entry.Package))
		}
	}
	return remaining
}
