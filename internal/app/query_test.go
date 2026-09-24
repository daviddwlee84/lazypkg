package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type queryRunner struct {
	mu             sync.Mutex
	inventoryCalls int
	managerCalls   int
	started        chan struct{}
	release        chan struct{}
	blockManagers  bool
	calls          []domain.Command
}

func (r *queryRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	args := strings.Join(c.Args, " ")
	if args == "--version" {
		r.mu.Unlock()
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	}
	if strings.Contains(args, "managers") {
		r.managerCalls++
		n := r.managerCalls
		block := r.blockManagers && n == 1
		start, release := r.started, r.release
		r.mu.Unlock()
		if block {
			close(start)
			<-release
		}
		version := "1.27.0"
		if r.blockManagers && n > 1 {
			version = "1.28.0"
		}
		return process.Result{Stdout: fmt.Sprintf(`{"go":{"id":"go","name":"Go","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"go","version":%q}}`, version)}, nil
	}
	if strings.Contains(args, "installed") {
		r.inventoryCalls++
		n := r.inventoryCalls
		block := !r.blockManagers && r.started != nil && n == 1
		start, release := r.started, r.release
		r.mu.Unlock()
		if block {
			close(start)
			<-release
		}
		return process.Result{Stdout: fmt.Sprintf(`{"go":{"packages":[{"id":"example.com/tool","installed_version":"%d"}],"errors":[]}}`, n)}, nil
	}
	r.mu.Unlock()
	return process.Result{}, fmt.Errorf("unexpected command %s", args)
}
func (r *queryRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	return fmt.Errorf("query attempted a mutation")
}

func TestLateInventoryCannotRepopulateAfterInvalidation(t *testing.T) {
	r := &queryRunner{started: make(chan struct{}), release: make(chan struct{})}
	a := testApp(t, r)
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"go"}}
	done := make(chan error, 1)
	go func() { _, err := a.Query(context.Background(), q); done <- err }()
	<-r.started
	a.invalidateInventory()
	fresh, err := a.Query(context.Background(), q)
	if err != nil || fresh.Packages[0].Version != "2" {
		t.Fatal(fresh, err)
	}
	close(r.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cached, err := a.Query(context.Background(), q)
	if err != nil || cached.Packages[0].Version != "2" {
		t.Fatal("old query poisoned cache", cached, err)
	}
	if r.inventoryCalls != 2 {
		t.Fatal("fresh cache not reused", r.inventoryCalls)
	}
}
func TestLateManagerDetectionCannotRepopulateAfterInvalidation(t *testing.T) {
	r := &queryRunner{started: make(chan struct{}), release: make(chan struct{}), blockManagers: true}
	a := testApp(t, r)
	done := make(chan error, 1)
	go func() { _, err := a.Managers(context.Background()); done <- err }()
	<-r.started
	a.invalidateInventory()
	fresh, err := a.Managers(context.Background())
	if err != nil || fresh[0].Version != "1.28.0" {
		t.Fatal(fresh, err)
	}
	close(r.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cached, err := a.Managers(context.Background())
	if err != nil || cached[0].Version != "1.28.0" {
		t.Fatal(cached, err)
	}
}

func TestOverlappingReadsShareWorkInSameEpoch(t *testing.T) {
	for _, managers := range []bool{false, true} {
		name := "inventory"
		if managers {
			name = "managers"
		}
		t.Run(name, func(t *testing.T) {
			r := &queryRunner{started: make(chan struct{}), release: make(chan struct{}), blockManagers: managers}
			a := testApp(t, r)
			read := func() (string, error) {
				if managers {
					rows, err := a.Managers(context.Background())
					if err != nil {
						return "", err
					}
					return rows[0].Version, nil
				}
				s, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"go"}})
				if err != nil {
					return "", err
				}
				return s.Packages[0].Version, nil
			}
			done := make(chan error, 2)
			go func() { _, err := read(); done <- err }()
			<-r.started
			go func() { _, err := read(); done <- err }()
			close(r.release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := read(); err != nil {
				t.Fatal(err)
			}
			if r.managerCalls != 1 || !managers && r.inventoryCalls != 1 {
				t.Fatal("identical reads were duplicated", r.managerCalls, r.inventoryCalls)
			}
		})
	}
}
func TestInstallPreflightIncludesTargetOutsideDefaultSet(t *testing.T) {
	r := &actionRunner{installed: true}
	a := testApp(t, r)
	a.Config.ManagerSets = map[string][]string{"python": {"uvx"}}
	a.Config.DefaultManagerSet = "python"
	_, err := a.Plan(context.Background(), domain.ActionRequest{Operation: "install", Manager: "winget", Package: "Test.Tool"})
	if err == nil || !strings.Contains(err.Error(), "already listed") {
		t.Fatal("target inventory omitted", err)
	}
	if len(r.runs) != 0 {
		t.Fatal("preflight mutated")
	}
}
func TestInstallPreflightDoesNotWarnForUndetectedManagers(t *testing.T) {
	a := testApp(t, &actionRunner{})
	p, err := a.Plan(context.Background(), domain.ActionRequest{Operation: "install", Manager: "winget", Package: "Test.Tool"})
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range p.Warnings {
		if strings.Contains(warning, "could not be inventoried") {
			t.Fatal("undetected managers caused incomplete coverage warning", p.Warnings)
		}
	}
}

type enrichmentRunner struct {
	actionRunner
	cancel context.CancelFunc
	cellar string
	reads  int
}

func (r *enrichmentRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "managers"):
		return process.Result{Stdout: `{"brew":{"id":"brew","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"brew","version":"4.6.0"}}`}, nil
	case c.Path == "brew":
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
			return process.Result{}, ctx.Err()
		}
		if args == "--cellar" {
			return process.Result{Stdout: r.cellar}, nil
		}
		return process.Result{Stdout: `{"formulae":[]}`}, nil
	case strings.Contains(args, "installed"):
		r.reads++
		return process.Result{Stdout: `{"brew":{"packages":[{"id":"example","installed_version":"1"}],"errors":[]}}`}, nil
	default:
		return r.actionRunner.Output(ctx, c)
	}
}

func TestCancellationDuringEnrichmentRetainsPublishedBase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &enrichmentRunner{cancel: cancel, cellar: t.TempDir()}
	a := testApp(t, r)
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"brew"}}
	if _, err := a.Query(ctx, q); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation during optional enrichment was lost", err)
	}
	if _, err := a.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if r.reads != 1 {
		t.Fatal("cancelled enrichment discarded completed inventory", r.reads)
	}
}
func TestExplicitEmptySelectionDoesNotExpandToDefaults(t *testing.T) {
	a := New(config.Config{MPMPath: "not-invoked", TimeoutSeconds: 2})
	_, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{}})
	if err == nil {
		t.Fatal("empty explicit scope accepted")
	}
}
func TestCoverageMarksUnqueriedPlatforms(t *testing.T) {
	r := &queryRunner{}
	a := testApp(t, r)
	s, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"go", "winget"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Coverage) != 2 || s.Coverage[0].State != "complete" || s.Coverage[1].State != "unsupported" {
		t.Fatal(s.Coverage)
	}
}

type failedInventoryRunner struct {
	actionRunner
	fail bool
}

func (r *failedInventoryRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if r.fail && strings.Contains(strings.Join(c.Args, " "), " installed") {
		return process.Result{Stdout: `{"winget":{"packages":[],"errors":["native inventory failed"]}}`}, nil
	}
	return r.actionRunner.Output(ctx, c)
}
func TestMutationPlanCannotUseStaleFallbackInventory(t *testing.T) {
	r := &failedInventoryRunner{actionRunner: actionRunner{installed: true}}
	a := testApp(t, r)
	if _, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"winget"}}); err != nil {
		t.Fatal(err)
	}
	r.fail = true
	for _, op := range []string{"remove", "upgrade", "install"} {
		_, err := a.Plan(context.Background(), domain.ActionRequest{Operation: op, Manager: "winget", Package: "Test.Tool"})
		if err == nil || !strings.Contains(err.Error(), "fresh winget inventory") {
			t.Fatal("stale data authorized mutation", op, err)
		}
	}
	if len(r.runs) > 0 {
		t.Fatal("preflight mutated")
	}
}
func TestEmptyMaintenanceSelectionDoesNotDiscoverAllManagers(t *testing.T) {
	a := New(config.Config{MPMPath: "must-not-run"})
	if _, err := a.CheckManagers(context.Background(), []string{}, false); err == nil {
		t.Fatal("empty scope expanded")
	}
}
