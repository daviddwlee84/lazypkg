package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type noOutdatedRunner struct {
	version          string
	change           bool
	unsupportedReads int
}

func (r *noOutdatedRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case args == "--version":
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	case strings.Contains(args, "managers"):
		return process.Result{Stdout: `{"pipx":{"id":"pipx","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"pipx","version":"1.8.0"}}`}, nil
	case strings.Contains(args, "--plan"):
		return process.Result{Stdout: "pipx upgrade tool"}, nil
	case strings.Contains(args, "installed"):
		return process.Result{Stdout: fmt.Sprintf(`{"pipx":{"packages":[{"id":"tool","installed_version":%q}],"errors":[]}}`, r.version)}, nil
	case strings.Contains(args, "outdated"):
		r.unsupportedReads++
		return process.Result{}, fmt.Errorf("unsupported outdated query")
	}
	return process.Result{}, fmt.Errorf("unexpected %s", args)
}
func (r *noOutdatedRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	if r.change {
		r.version = "2.0"
	}
	return nil
}
func TestUpgradeWithoutOutdatedCapabilityVerifiesActualVersionChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		r := &noOutdatedRunner{version: "1.0", change: changed}
		a := testApp(t, r)
		// Exercise the service contract for a provider with upgrade but no update
		// query. The pinned pipx adapter itself does support outdated.
		a.managerCache = []domain.Manager{{ID: "pipx", Path: "pipx", Available: true, Scope: "global", Capabilities: []string{"installed", "upgrade"}}}
		a.managerCacheAt = time.Now()
		a.managerCacheContext = a.queryContext()
		p, err := a.Plan(context.Background(), domain.ActionRequest{Operation: "upgrade", Manager: "pipx", Package: "tool"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
		if r.unsupportedReads != 0 {
			t.Fatal("called unsupported outdated operation")
		}
		if changed {
			if err != nil || result.Steps[0].Status != "success" || !strings.Contains(result.Message, "cannot verify") {
				t.Fatal(result, err)
			}
		} else {
			if err == nil || result.Steps[0].Status != "unverified" {
				t.Fatal(result, err)
			}
		}
	}
}
