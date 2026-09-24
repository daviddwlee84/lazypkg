package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type batchRecord struct{ version, latest string }
type batchRunner struct {
	mu              sync.Mutex
	items           map[string]batchRecord
	path            string
	fail, noChange  string
	runs            []domain.Command
	afterRun        func(string)
	failedInventory bool
	previewDelay    time.Duration
	delayed         bool
}

func (r *batchRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	args := strings.Join(c.Args, " ")
	if args == "--version" {
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	}
	if strings.Contains(args, "managers") {
		return process.Result{Stdout: `{"winget":{"id":"winget","name":"WinGet","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"` + r.path + `","version":"1.29.280"}}`}, nil
	}
	if strings.Contains(args, "--plan") {
		if r.previewDelay > 0 && !r.delayed {
			r.delayed = true
			time.Sleep(r.previewDelay)
		}
		return process.Result{Stdout: "winget upgrade " + c.Args[len(c.Args)-1]}, nil
	}
	if strings.Contains(args, "installed") || strings.Contains(args, "outdated") {
		if r.failedInventory {
			return process.Result{}, errors.New("inventory unavailable")
		}
		rows := []map[string]string{}
		ids := []string{}
		for id := range r.items {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			v := r.items[id]
			if strings.Contains(args, "outdated") && v.version == v.latest {
				continue
			}
			rows = append(rows, map[string]string{"id": id, "installed_version": v.version, "latest_version": v.latest})
		}
		data, _ := json.Marshal(map[string]any{"winget": map[string]any{"packages": rows, "errors": []string{}}})
		return process.Result{Stdout: string(data)}, nil
	}
	return process.Result{}, errors.New("unexpected fake command: " + process.Display(c))
}
func (r *batchRunner) Run(ctx context.Context, c domain.Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, c)
	id := strings.TrimPrefix(c.Args[len(c.Args)-1], "pkg:winget/")
	if id == r.fail {
		return errors.New("native upgrade failed")
	}
	if id != r.noChange {
		v := r.items[id]
		v.version = v.latest
		r.items[id] = v
	}
	if r.afterRun != nil {
		r.afterRun(id)
	}
	return nil
}
func batchApp(t *testing.T) (*App, *batchRunner, domain.BatchUpgradeRequest) {
	t.Helper()
	r := &batchRunner{path: "winget", items: map[string]batchRecord{"Test.A": {"1.0", "2.0"}, "Test.B": {"1.0", "2.0"}, "Test.C": {"1.0", "2.0"}}}
	a := testApp(t, r)
	req := domain.BatchUpgradeRequest{Source: "marked, including hidden rows"}
	for _, id := range []string{"Test.A", "Test.B", "Test.C"} {
		req.Targets = append(req.Targets, domain.Package{Manager: "winget", Instance: "winget", ID: id, Version: "1.0", Latest: "2.0", Scope: "global"})
	}
	return a, r, req
}

func TestBatchFreezesHiddenMarksAndUsesOnlySingularUpgrades(t *testing.T) {
	a, r, req := batchApp(t)
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 || len(r.runs) != 0 {
		t.Fatal(p, r.runs)
	}
	for _, e := range p.Entries {
		if e.State != "planned" || e.Plan == nil {
			t.Fatal(e)
		}
	}
	req.Targets[0].ID = "Changed.After.Planning"
	result, err := a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || result.Paused || len(r.runs) != 3 {
		t.Fatal(result, err, r.runs)
	}
	for n, c := range r.runs {
		args := strings.Join(c.Args, " ")
		if strings.Contains(args, "upgrade_all") || strings.Contains(args, "--all") || !strings.Contains(args, "--winget upgrade -- pkg:winget/Test.") {
			t.Fatal(c)
		}
		if result.Entries[n].State != "success" {
			t.Fatal(result)
		}
	}
}

func TestBatchStopsRemainingOnFailureUnverifiedAndDrift(t *testing.T) {
	for _, mode := range []string{"failed", "unverified", "version drift", "target drift", "instance drift", "context drift", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			a, r, req := batchApp(t)
			p, err := a.PlanBatchUpgrade(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			expectedRuns := 1
			switch mode {
			case "failed":
				r.fail = "Test.A"
			case "unverified":
				r.noChange = "Test.A"
			case "version drift":
				r.afterRun = func(id string) {
					if id == "Test.A" {
						r.items["Test.B"] = batchRecord{"1.5", "2.0"}
					}
				}
			case "target drift":
				r.afterRun = func(id string) {
					if id == "Test.A" {
						r.items["Test.B"] = batchRecord{"1.0", "3.0"}
					}
				}
			case "instance drift":
				r.path = "winget-other"
				expectedRuns = 0
			case "context drift":
				t.Setenv("NPM_CONFIG_PREFIX", t.TempDir())
				expectedRuns = 0
			case "tampered":
				p.Entries[0].Plan.Request.Package = "Test.C"
				expectedRuns = 0
			}
			result, err := a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard)
			if err == nil || len(r.runs) != expectedRuns {
				t.Fatal(result, err, r.runs)
			}
			if mode != "tampered" && (!result.Paused || len(result.Remaining) == 0) {
				t.Fatal("missing explicit paused/resume state", result)
			}
			if mode == "failed" && result.Entries[0].State != "failed" {
				t.Fatal(result)
			}
			if mode == "unverified" && result.Entries[0].State != "unverified" {
				t.Fatal(result)
			}
			for _, c := range r.runs {
				if strings.HasSuffix(strings.Join(c.Args, " "), "Test.C") {
					t.Fatal("ran a later item after pause", r.runs)
				}
			}
		})
	}
}

func TestBatchSkipsFreshlyCurrentAndSupportsExplicitReplan(t *testing.T) {
	a, r, req := batchApp(t)
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r.items["Test.A"] = batchRecord{"2.0", "2.0"}
	r.fail = "Test.B"
	result, err := a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil || result.Entries[0].State != "current" || len(r.runs) != 1 || len(result.Remaining) != 2 {
		t.Fatal(result, err, r.runs)
	}
	// The caller explicitly skips B, then requests and approves a new overview.
	remaining := result.Remaining[1:]
	r.fail = ""
	resume, err := a.PlanBatchUpgrade(context.Background(), domain.BatchUpgradeRequest{Targets: remaining, Source: "resume after skip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ExecuteBatchUpgrade(context.Background(), resume, nil, io.Discard, io.Discard); err != nil || len(r.runs) != 2 {
		t.Fatal(err, r.runs)
	}
}

func TestBatchRejectsStaleInputCoverageAndHonorsWriteLock(t *testing.T) {
	a, r, req := batchApp(t)
	r.failedInventory = true
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Entries {
		if e.State == "planned" {
			t.Fatal("failed inventory authorized batch", p)
		}
	}
	r.failedInventory = false
	p, err = a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	a.writeMu.Lock()
	_, err = a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard)
	a.writeMu.Unlock()
	if err == nil || len(r.runs) != 0 {
		t.Fatal("batch ignored existing writer", err)
	}
}

func TestBatchEligibilitySharedRulesAndStatuses(t *testing.T) {
	p := domain.Package{Manager: "mise", ID: "node", Version: "22", Latest: "24", Scope: "user runtime"}
	m := domain.Manager{ID: "mise", Available: true, Scope: "global", Capabilities: []string{"upgrade"}}
	coverage := []domain.Coverage{{Manager: "mise", State: "complete", ObservedAt: time.Now()}}
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); !ok {
		t.Fatal("mise should be eligible")
	}
	p.LatestInstalled = true
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); ok {
		t.Fatal("activation-only row eligible")
	}
	p = domain.Package{Manager: "uvx", ID: "meta_package_manager", Version: "8"}
	m.ID = "uvx"
	coverage[0].Manager = "uvx"
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); ok {
		t.Fatal("active backend eligible")
	}
	for _, status := range []string{"success", "current", "skipped", "unverified"} {
		if state := batchActionState(domain.ActionResult{Steps: []domain.StepResult{{Status: status}}}, nil, nil); state != status {
			t.Fatal(status, state)
		}
	}
}

func TestBatchEmptySelectionAndExplicitLimit(t *testing.T) {
	a, r, _ := batchApp(t)
	p, err := a.PlanBatchUpgrade(context.Background(), domain.BatchUpgradeRequest{Source: "empty filter"})
	if err != nil || len(p.Entries) != 0 || p.Fingerprint == "" {
		t.Fatal(p, err)
	}
	result, err := a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || result.Paused || len(result.Entries) != 0 || len(r.runs) != 0 {
		t.Fatal(result, err)
	}
	if _, err = a.PlanBatchUpgrade(context.Background(), domain.BatchUpgradeRequest{Targets: make([]domain.Package, 5001)}); err == nil || !strings.Contains(err.Error(), "none were truncated") {
		t.Fatal(err)
	}
}

func TestBatchNeverRebindsChangedMarkedVersions(t *testing.T) {
	a, r, req := batchApp(t)
	r.items["Test.A"] = batchRecord{"1.5", "2.0"}
	r.items["Test.B"] = batchRecord{"2.0", "2.0"}
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Entries[0].State != "excluded" || p.Entries[0].Plan != nil || !strings.Contains(p.Entries[0].Reason, "selected installed version") {
		t.Fatal(p)
	}
	if p.Entries[1].State != "current" || p.Entries[1].Plan != nil {
		t.Fatal("already current must remain a no-op", p)
	}
	if p.Entries[0].Targets[0].Version != "1.0" {
		t.Fatal("original selection was overwritten", p)
	}
}

func TestBatchRejectsSearchCandidateEvenWhenInstalledVersionPresent(t *testing.T) {
	a, _, req := batchApp(t)
	req.Targets = req.Targets[:1]
	req.Targets[0].Candidate = true
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 || p.Entries[0].State != "excluded" || p.Entries[0].Plan != nil || !strings.Contains(p.Entries[0].Reason, "search candidate") {
		t.Fatal(p)
	}
	if !p.Request.Targets[0].Candidate {
		t.Fatal("snapshot lost its selection provenance")
	}
}

func TestBatchFreshnessExtensionConstraintsAndRemainingGroups(t *testing.T) {
	m := domain.Manager{ID: "gh-ext", Available: true, Scope: "global", Capabilities: []string{"upgrade"}}
	p := domain.Package{Manager: "gh-ext", ID: "owner/tool", Version: "v1", Extension: &domain.GHExtension{Kind: "git", Status: "available"}}
	coverage := []domain.Coverage{{Manager: "gh-ext", State: "complete", ObservedAt: time.Now()}}
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); !ok {
		t.Fatal("safe extension ineligible")
	}
	before := batchContext(m, []domain.Package{p})
	p.Extension.BlockedReason = "Local changes"
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); ok || batchContext(m, []domain.Package{p}) == before {
		t.Fatal("changed checkout bypassed binding")
	}
	p.Extension.BlockedReason = ""
	p.Extension.Pinned = true
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); ok {
		t.Fatal("pinned extension eligible")
	}
	p.Extension.Pinned = false
	coverage[0].ObservedAt = time.Now().Add(-2 * time.Minute)
	if ok, _ := domain.BatchUpgradeEligibility(p, m, coverage); ok {
		t.Fatal("old coverage eligible")
	}
	versions := []domain.Package{{Manager: "mise", ID: "node", Version: "20"}, {Manager: "mise", ID: "node", Version: "22"}}
	remaining := batchRemaining([]domain.BatchUpgradeItemResult{{Entry: domain.BatchUpgradeEntry{Package: versions[0], Targets: versions}, State: "drift"}})
	if len(remaining) != 2 || remaining[0].Version != "20" || remaining[1].Version != "22" {
		t.Fatal("resume lost marked versions", remaining)
	}
}

func TestBatchCancellationStopsBeforeAnotherWrite(t *testing.T) {
	a, r, req := batchApp(t)
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.afterRun = func(string) { cancel() }
	result, err := a.ExecuteBatchUpgrade(ctx, p, nil, io.Discard, io.Discard)
	if err == nil || !result.Paused || result.Entries[0].State != "cancelled" || len(r.runs) != 1 || len(result.Remaining) != 3 {
		t.Fatal(result, err, r.runs)
	}
}

func TestBatchSlowOverviewDoesNotAgeOutLaterTargets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, r, req := batchApp(t)
		a.Config.TimeoutSeconds = 180
		r.previewDelay = 61 * time.Second
		start := time.Now()
		p, err := a.PlanBatchUpgrade(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) < time.Minute {
			t.Fatal("fixture did not cross freshness boundary")
		}
		for _, entry := range p.Entries {
			if entry.State != "planned" {
				t.Fatalf("preparing earlier rows expired a later target: %#v", entry)
			}
		}
		if _, err = a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard); err != nil || len(r.runs) != 3 {
			t.Fatal(err, r.runs)
		}
	})
}

type miseBatchRunner struct {
	path, root string
	global     string
	noUpdates  bool
	versions   []string
	runs       []domain.Command
}

func (r *miseBatchRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	args := strings.Join(c.Args, " ")
	if c.Path == r.path {
		switch {
		case args == "ls --installed --json" || args == "ls --global --json" || args == "ls --current --json":
			rows := []map[string]any{}
			global := r.global
			if global == "" {
				global = "22.0.0"
			}
			for _, v := range r.versions {
				if args != "ls --installed --json" && v != global {
					continue
				}
				rows = append(rows, map[string]any{"version": v, "install_path": filepath.Join(r.root, "installs", "node", v), "installed": true})
			}
			data, _ := json.Marshal(map[string]any{"node": rows})
			return process.Result{Stdout: string(data)}, nil
		case args == "outdated --json --bump":
			if r.noUpdates {
				return process.Result{Stdout: `{}`}, nil
			}
			return process.Result{Stdout: `{"node":{"current":"22.0.0","latest":"24.0.0"}}`}, nil
		case args == "latest node@24.0.0":
			return process.Result{Stdout: "24.0.0"}, nil
		}
	}
	if args == "--version" {
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	}
	if strings.Contains(args, "managers") {
		data, _ := json.Marshal(map[string]any{"mise": map[string]any{"id": "mise", "name": "mise", "available": true, "supported": true, "fresh": true, "executable": true, "cli_path": r.path, "version": "2026.9.0"}})
		return process.Result{Stdout: string(data)}, nil
	}
	return process.Result{}, errors.New("unexpected mise batch query: " + process.Display(c))
}
func (r *miseBatchRunner) Run(_ context.Context, c domain.Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	r.runs = append(r.runs, c)
	if strings.Join(c.Args, " ") != "install node@24.0.0" {
		return errors.New("unexpected mutation")
	}
	r.versions = append(r.versions, "24.0.0")
	return nil
}
func TestBatchMiseGroupsVersionsAndNeverActivates(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX fake mise launcher fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mise")
	if err := os.WriteFile(path, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &miseBatchRunner{path: path, root: dir, versions: []string{"20.0.0", "22.0.0"}}
	a := testApp(t, r)
	req := domain.BatchUpgradeRequest{Source: "marked"}
	for _, v := range r.versions {
		req.Targets = append(req.Targets, domain.Package{Manager: "mise", ID: "node", Version: v, Latest: "24.0.0", Instance: batchCanonical(path), Scope: "user runtime"})
	}
	p, err := a.PlanBatchUpgrade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 || p.Entries[0].State != "planned" || len(p.Entries[0].ObservedVersions) != 2 || p.Entries[0].Plan.Request.Version != "24.0.0" {
		t.Fatal(p)
	}
	if _, err = a.ExecuteBatchUpgrade(context.Background(), p, nil, io.Discard, io.Discard); err != nil || len(r.runs) != 1 {
		t.Fatal(err, r.runs)
	}
	if !batchContains(r.versions, "20.0.0") || !batchContains(r.versions, "22.0.0") {
		t.Fatal("old runtimes removed", r.versions)
	}
}

func TestBatchCurrentRequiresMarkedRootContext(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX fake mise launcher fixture")
	}
	for _, differentRoot := range []bool{false, true} {
		t.Run(fmt.Sprint(differentRoot), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mise")
			if err := os.WriteFile(path, []byte("fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			root := filepath.Join(dir, "data")
			current := filepath.Join(root, "installs", "node", "24.0.0")
			if err := os.MkdirAll(current, 0755); err != nil {
				t.Fatal(err)
			}
			selectedRoot := filepath.Join(root, "installs", "node", "22.0.0")
			if differentRoot {
				selectedRoot = filepath.Join(dir, "other-data", "installs", "node", "22.0.0")
			}
			r := &miseBatchRunner{path: path, root: root, versions: []string{"24.0.0"}, global: "24.0.0", noUpdates: true}
			a := testApp(t, r)
			req := domain.BatchUpgradeRequest{Targets: []domain.Package{{Manager: "mise", Instance: batchCanonical(path), ID: "node", Version: "22.0.0", Latest: "24.0.0", Scope: "user runtime", Root: selectedRoot}}}
			p, err := a.PlanBatchUpgrade(context.Background(), req)
			if err != nil || len(p.Entries) != 1 {
				t.Fatal(p, err)
			}
			want := "current"
			if differentRoot {
				want = "excluded"
			}
			if p.Entries[0].State != want || p.Entries[0].Plan != nil || len(r.runs) != 0 {
				t.Fatal(p, r.runs)
			}
			if differentRoot && !strings.Contains(p.Entries[0].Reason, "root context changed") {
				t.Fatal(p)
			}
		})
	}
	for _, manager := range []string{"npm", "uvx", "gh-ext"} {
		target := domain.Package{Manager: manager, Root: "/one/tool"}
		rows := []domain.Package{{Manager: manager, Root: "/two/tool"}}
		if batchSelectedRootsMatch([]domain.Package{target}, rows) {
			t.Fatal(manager, "merged distinct tool roots")
		}
	}
	for _, manager := range []string{"brew", "mise"} {
		target := domain.Package{Manager: manager, Root: "/one/tool/1"}
		rows := []domain.Package{{Manager: manager, Root: "/one/tool/2"}}
		if !batchSelectedRootsMatch([]domain.Package{target}, rows) {
			t.Fatal(manager, "failed same-parent version transition")
		}
	}
}
