package app

import (
	"context"
	"fmt"
	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
	"io"
	"strings"
	"sync"
	"testing"
)

type actionRunner struct {
	mu             sync.Mutex
	installed      bool
	remainOutdated bool
	runs           []domain.Command
}

func (r *actionRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := strings.Join(c.Args, " ")
	switch {
	case args == "--version":
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	case strings.Contains(args, "managers"):
		return process.Result{Stdout: `{"winget":{"id":"winget","name":"WinGet","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"winget","version":"1.29.280"}}`}, nil
	case strings.Contains(args, "--plan"):
		return process.Result{Stdout: "winget action exact package"}, nil
	case strings.Contains(args, "installed"):
		packages := "[]"
		if r.installed {
			packages = `[{"id":"Test.Tool","installed_version":"1.0"}]`
		}
		return process.Result{Stdout: `{"winget":{"packages":` + packages + `,"errors":[]}}`}, nil
	case strings.Contains(args, "outdated"):
		packages := "[]"
		if r.remainOutdated {
			packages = `[{"id":"Test.Tool","installed_version":"1.0","latest_version":"2.0"}]`
		}
		return process.Result{Stdout: `{"winget":{"packages":` + packages + `,"errors":[]}}`}, nil
	}
	return process.Result{}, fmt.Errorf("unexpected %s", args)
}
func (r *actionRunner) Run(_ context.Context, c domain.Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, c)
	if strings.Contains(strings.Join(c.Args, " "), " remove ") {
		r.installed = false
	} else {
		r.installed = true
	}
	return nil
}
func testApp(t *testing.T, r process.Runner) *App {
	t.Helper()
	a := New(config.Config{MPMPath: "fake-mpm", DataDir: t.TempDir(), TimeoutSeconds: 2})
	a.Runner = r
	return a
}
func TestPlanNeverMutatesAndExecScopesManager(t *testing.T) {
	r := &actionRunner{}
	a := testApp(t, r)
	p, err := a.Plan(context.Background(), domain.ActionRequest{Operation: "install", Manager: "winget", Package: "Test.Tool"})
	if err != nil || len(r.runs) != 0 {
		t.Fatal(p, err)
	}
	result, err := a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || len(r.runs) != 1 || result.Steps[0].Status != "success" {
		t.Fatal(result, err, r.runs)
	}
	args := strings.Join(r.runs[0].Args, " ")
	if !strings.Contains(args, "--winget install -- pkg:winget/Test.Tool") {
		t.Fatal(args)
	}
}
func TestNoOpUpgradeIsNotReportedSuccessful(t *testing.T) {
	r := &actionRunner{installed: true, remainOutdated: true}
	a := testApp(t, r)
	p, err := a.Plan(context.Background(), domain.ActionRequest{Operation: "upgrade", Manager: "winget", Package: "Test.Tool"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil || result.Steps[0].Status != "unverified" {
		t.Fatal(result, err)
	}
}
func TestRejectShellExpressionsAndProtectBackend(t *testing.T) {
	for _, pkg := range []string{"--all", "x; rm", "x&calc", "x%PATH%", "x\nfoo"} {
		if Validate(domain.ActionRequest{Manager: "winget", Operation: "install", Package: pkg}) == nil {
			t.Fatal(pkg)
		}
	}
	a := testApp(t, &actionRunner{})
	if _, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "uvx", Operation: "remove", Package: "meta-package-manager"}); err == nil {
		t.Fatal("backend removal allowed")
	}
}
