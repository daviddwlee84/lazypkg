package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// TestPTYHelper is an opt-in child process for a real terminal smoke test. Its
// providers are entirely fake: this test cannot install or remove host tools.
func TestPTYHelper(t *testing.T) {
	if os.Getenv("LAZYPKG_TUI_TEST_HELPER") != "1" {
		t.Skip("PTY helper only")
	}
	f := &fakeService{
		managers: []domain.Manager{{ID: "brew", Name: "Homebrew", Available: true, Supported: true, Status: "available", Version: "6.0.0", Capabilities: []string{"installed", "search", "install", "upgrade", "remove"}}},
		snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "alpha", Version: "1.0", Latest: "1.1", Scope: "user", Evidence: []domain.Evidence{{Kind: "recorded", Source: "brew", Detail: "Fixture ownership"}}}, {Manager: "brew", ID: "bravo", Version: "2.0", Scope: "user"}}, ObservedAt: time.Now()},
		options:  []domain.SetupOption{{ID: "mpm", Name: "mpm backend", Recommended: true, Description: "Fixture setup; nothing will be installed."}},
	}
	if err := Run(context.Background(), &ptyService{fakeService: f}, "installed"); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "TUI_EXIT_OK")
}

type ptyService struct{ *fakeService }

func (f *ptyService) Query(ctx context.Context, request domain.PackageQuery) (domain.Snapshot, error) {
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return domain.Snapshot{}, ctx.Err()
	case <-timer.C:
	}
	return f.fakeService.Query(ctx, request)
}

func (f *ptyService) Execute(ctx context.Context, plan domain.ActionPlan, in io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	fmt.Fprint(out, "Native prompt: type continue: ")
	if _, err := bufio.NewReader(in).ReadString('\n'); err != nil {
		return domain.ActionResult{}, err
	}
	f.result = domain.ActionResult{Message: "Fixture operation completed", Steps: []domain.StepResult{{ID: "fixture", Status: "done"}}}
	result, err := f.fakeService.Execute(ctx, plan, in, out, errout)
	if path := os.Getenv("LAZYPKG_TUI_TEST_RECEIPT"); path != "" {
		data, marshalErr := json.Marshal(struct {
			Calls   int                  `json:"calls"`
			Request domain.ActionRequest `json:"request"`
		}{f.executeCalls, plan.Request})
		if marshalErr != nil {
			return result, marshalErr
		}
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			return result, writeErr
		}
	}
	return result, err
}
