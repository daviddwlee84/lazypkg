package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type brewActionRunner struct {
	mu                                 sync.Mutex
	path, cellar, tap, version, latest string
	noChange, badMetadata              bool
	runs                               []domain.Command
}

func (r *brewActionRunner) receipt() error {
	dir := filepath.Join(r.cellar, "dev-cli", r.version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"source": map[string]string{"tap": r.tap}})
	return os.WriteFile(filepath.Join(dir, "INSTALL_RECEIPT.json"), data, 0600)
}

func (r *brewActionRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := strings.Join(c.Args, " ")
	if c.Path == r.path || c.Path == "brew" {
		if args == "--cellar" {
			return process.Result{Stdout: r.cellar}, nil
		}
		if strings.HasPrefix(args, "info --json=v2") {
			if r.badMetadata {
				return process.Result{}, errors.New("metadata unavailable")
			}
			tap := r.tap
			if !strings.Contains(args, "--installed") {
				parts := strings.Split(c.Args[len(c.Args)-1], "/")
				if len(parts) == 3 {
					tap = strings.Join(parts[:2], "/")
				}
			}
			full := tap + "/dev-cli"
			if tap == "homebrew/core" {
				full = "dev-cli"
			}
			data, _ := json.Marshal(map[string]any{"formulae": []any{map[string]any{
				"name": "dev-cli", "full_name": full, "tap": tap, "aliases": []string{"dev-alias"},
				"installed": []any{map[string]string{"version": r.version}},
			}}, "casks": []any{}})
			return process.Result{Stdout: string(data)}, nil
		}
	}
	if c.Path == "fake-mpm" {
		switch {
		case args == "--version":
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		case strings.Contains(args, "managers"):
			return process.Result{Stdout: fmt.Sprintf(`{"brew":{"id":"brew","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":%q,"version":"4.6.0"}}`, r.path)}, nil
		case strings.Contains(args, "--plan"):
			return process.Result{Stdout: "brew action " + c.Args[len(c.Args)-1]}, nil
		case strings.Contains(args, "installed"), strings.Contains(args, "outdated"):
			rows := []any{}
			outdated := strings.Contains(args, "outdated")
			if !outdated || r.version != r.latest {
				id := "dev-cli"
				if outdated && r.tap != "homebrew/core" {
					id = r.tap + "/dev-cli"
				}
				rows = append(rows, map[string]string{"id": id, "installed_version": r.version, "latest_version": r.latest})
			}
			data, _ := json.Marshal(map[string]any{"brew": map[string]any{"packages": rows, "errors": []string{}}})
			return process.Result{Stdout: string(data)}, nil
		}
	}
	return process.Result{}, fmt.Errorf("unexpected fixture command %s", process.Display(c))
}

func (r *brewActionRunner) Run(_ context.Context, c domain.Command, _ io.Reader, _, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, c)
	if !r.noChange {
		r.version = r.latest
		return r.receipt()
	}
	return nil
}

func brewActionFixture(t *testing.T) (*App, *brewActionRunner) {
	t.Helper()
	root := t.TempDir()
	r := &brewActionRunner{path: filepath.Join(root, "brew"), cellar: filepath.Join(root, "Cellar"), tap: "owner/tap", version: "0.3.0", latest: "0.3.2"}
	if err := os.WriteFile(r.path, []byte("fake launcher"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := r.receipt(); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, r)
	a.Config.Managers = []string{"brew"}
	return a, r
}

func TestBrewTapInventoryAndBatchUseOneCanonicalInstallation(t *testing.T) {
	for _, kind := range []string{"installed", "outdated"} {
		t.Run(kind, func(t *testing.T) {
			a, r := brewActionFixture(t)
			ctx := context.Background()
			s, err := a.Query(ctx, domain.PackageQuery{Kind: kind, Managers: []string{"brew"}})
			if err != nil || !freshInventory(s, "brew") || len(s.Packages) != 1 || s.Packages[0].ID != "owner/tap/dev-cli" {
				t.Fatal(s, err)
			}
			p, err := a.PlanBatchUpgrade(ctx, domain.BatchUpgradeRequest{Targets: s.Packages})
			if err != nil || len(p.Entries) != 1 || p.Entries[0].State != "planned" || p.Entries[0].TargetVersion != "0.3.2" {
				t.Fatal(p, err)
			}
			if p.Entries[0].Plan.Request.Package != "owner/tap/dev-cli" || !strings.Contains(p.Entries[0].Plan.Preview, "owner/tap/dev-cli") {
				t.Fatal(p)
			}
			result, err := a.ExecuteBatchUpgrade(ctx, p, nil, io.Discard, io.Discard)
			if err != nil || result.Paused || len(r.runs) != 1 || result.Entries[0].State != "success" {
				t.Fatal(result, err, r.runs)
			}
			if !strings.Contains(strings.Join(r.runs[0].Args, " "), "pkg:brew/owner/tap/dev-cli") {
				t.Fatal(r.runs)
			}
		})
	}
}

func TestBrewSingleNoOpCannotHideFullyQualifiedOutdatedRow(t *testing.T) {
	a, r := brewActionFixture(t)
	r.noChange = true
	p, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "brew", Operation: "upgrade", Package: "dev-alias"})
	if err != nil || p.Request.Package != "owner/tap/dev-cli" {
		t.Fatal(p, err)
	}
	result, err := a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil || result.Steps[0].Status != "unverified" || len(r.runs) != 1 {
		t.Fatal(result, err, r.runs)
	}
}

func TestBrewActionRejectsTapDriftAndMissingIdentity(t *testing.T) {
	for _, missing := range []bool{false, true} {
		a, r := brewActionFixture(t)
		p, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "brew", Operation: "upgrade", Package: "dev-cli"})
		if err != nil {
			t.Fatal(err)
		}
		if missing {
			r.badMetadata = true
		} else {
			r.tap = "different/tap"
			if err := r.receipt(); err != nil {
				t.Fatal(err)
			}
		}
		_, err = a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
		if err == nil || len(r.runs) != 0 {
			t.Fatal("changed source executed", err, r.runs)
		}
	}
}

func TestBrewCoreMutationUsesQualifiedNativeTarget(t *testing.T) {
	a, r := brewActionFixture(t)
	r.tap = "homebrew/core"
	if err := r.receipt(); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "brew", Operation: "upgrade", Package: "homebrew/core/dev-cli"})
	if err != nil || p.Request.Package != "dev-cli" || p.ProviderTarget != "homebrew/core/dev-cli" || !strings.Contains(p.Preview, "homebrew/core/dev-cli") {
		t.Fatal(p, err)
	}
	_, err = a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || len(r.runs) != 1 || !strings.Contains(strings.Join(r.runs[0].Args, " "), "pkg:brew/homebrew/core/dev-cli") {
		t.Fatal(err, r.runs)
	}
}

func TestBrewBatchGroupsRawAndQualifiedSelectorsAfterFreshResolution(t *testing.T) {
	a, r := brewActionFixture(t)
	p, err := a.PlanBatchUpgrade(context.Background(), domain.BatchUpgradeRequest{Targets: []domain.Package{
		{Manager: "brew", Instance: batchCanonical(r.path), ID: "dev-cli", Version: "0.3.0", Scope: "global", InventoryStale: true},
		{Manager: "brew", Instance: batchCanonical(r.path), ID: "owner/tap/dev-cli", Version: "0.3.0", Scope: "global"},
	}})
	if err != nil || len(p.Entries) != 1 || p.Entries[0].State != "planned" || len(p.Entries[0].Targets) != 2 {
		t.Fatal(p, err)
	}
}

type conflictingBrewRunner struct{ *brewActionRunner }

func (r conflictingBrewRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	args := strings.Join(c.Args, " ")
	if strings.HasPrefix(args, "info --json=v2 --formula homebrew/core/new-tool") {
		return process.Result{Stdout: `{"formulae":[{"name":"new-tool","full_name":"new-tool","tap":"homebrew/core","aliases":[],"installed":[]}],"casks":[]}`}, nil
	}
	result, err := r.brewActionRunner.Output(ctx, c)
	if err != nil {
		return result, err
	}
	if strings.HasPrefix(args, "info --json=v2") && strings.Contains(args, "--installed") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return result, err
		}
		payload["formulae"] = append(payload["formulae"].([]any), map[string]any{"name": "conflict", "full_name": "conflict", "tap": "homebrew/core", "installed": []any{map[string]string{"version": "1"}}})
		data, _ := json.Marshal(payload)
		result.Stdout = string(data)
	} else if c.Path == "fake-mpm" && (strings.Contains(args, "installed") || strings.Contains(args, "outdated")) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return result, err
		}
		provider := payload["brew"].(map[string]any)
		provider["packages"] = append(provider["packages"].([]any), map[string]string{"id": "conflict", "installed_version": "1", "latest_version": "2"})
		data, _ := json.Marshal(payload)
		result.Stdout = string(data)
	}
	return result, nil
}

func TestUnrelatedReceiptConflictDoesNotBlockVerifiedPlansOrPostcheck(t *testing.T) {
	a, runner := brewActionFixture(t)
	dir := filepath.Join(runner.cellar, "conflict", "1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "INSTALL_RECEIPT.json"), []byte(`{"source":{"tap":"old/tap"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.Runner = conflictingBrewRunner{runner}
	ctx := context.Background()
	install, err := a.Plan(ctx, domain.ActionRequest{Manager: "brew", Operation: "install", Package: "new-tool"})
	if err != nil || install.ProviderTarget != "homebrew/core/new-tool" {
		t.Fatal(install, err)
	}
	query, err := a.Query(ctx, domain.PackageQuery{Kind: "outdated", Managers: []string{"brew"}, Refresh: true})
	if err != nil || len(query.Issues) == 0 || query.Coverage[0].State != "failed" {
		t.Fatal(query, err)
	}
	var target domain.Package
	for _, p := range query.Packages {
		if p.ID == "owner/tap/dev-cli" {
			target = p
		}
	}
	plan, err := a.PlanBatchUpgrade(ctx, domain.BatchUpgradeRequest{Targets: []domain.Package{target}})
	if err != nil || len(plan.Entries) != 1 || plan.Entries[0].State != "planned" {
		t.Fatal(plan, err)
	}
	result, err := a.ExecuteBatchUpgrade(ctx, plan, nil, io.Discard, io.Discard)
	if err != nil || result.Paused || len(runner.runs) != 1 || result.Entries[0].State != "success" {
		t.Fatal(result, err, runner.runs)
	}
	if query.Coverage[0].State != "failed" {
		t.Fatal("targeted completeness changed original provider observation")
	}
}
