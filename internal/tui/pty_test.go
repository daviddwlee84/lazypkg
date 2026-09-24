package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
		snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "alpha", Version: "1.0", Latest: "1.1", Scope: "global", Evidence: []domain.Evidence{{Kind: "recorded", Source: "brew", Detail: "Fixture ownership"}}}, {Manager: "brew", ID: "bravo", Version: "2.0", Scope: "global"}}, ObservedAt: time.Now()},
		options:  []domain.SetupOption{{ID: "mpm", Name: "mpm backend", Recommended: true, Description: "Fixture setup; nothing will be installed."}},
	}
	if err := Run(context.Background(), &ptyService{fakeService: f}, "installed"); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "TUI_EXIT_OK")
}

type ptyService struct {
	*fakeService
	batchFailed bool
}

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

func (f *ptyService) StreamQuery(ctx context.Context, request domain.PackageQuery) <-chan domain.QueryEvent {
	events := make(chan domain.QueryEvent, 3)
	go func() {
		defer close(events)
		snapshot, err := f.Query(ctx, request)
		if err != nil {
			return
		}
		coverage := domain.Coverage{Manager: "brew", State: "complete", ObservedAt: time.Now().Add(-2 * time.Hour), Enrichment: "pending"}
		snapshot.Coverage = []domain.Coverage{coverage}
		emit := func(event domain.QueryEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !emit(domain.QueryEvent{Stage: "base", Manager: "brew", Snapshot: domain.CloneSnapshot(snapshot), Elapsed: 300 * time.Millisecond}) {
			return
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		snapshot.Coverage[0].Enrichment = "complete"
		if !emit(domain.QueryEvent{Stage: "enriched", Manager: "brew", Snapshot: domain.CloneSnapshot(snapshot), Elapsed: 1300 * time.Millisecond}) {
			return
		}
		emit(domain.QueryEvent{Stage: "done", Snapshot: snapshot})
	}()
	return events
}

func (f *ptyService) Diagnose(context.Context, string) (domain.DiagnosticReport, error) {
	return domain.DiagnosticReport{Scope: "fixture PATH", Executables: []domain.Executable{{Name: "alpha", Path: "/fixture/brew/alpha", Manager: "brew", Preferred: true}, {Name: "alpha", Path: "/fixture/uv/alpha", Manager: "uv"}}, Findings: []domain.Finding{{Kind: "shadowed", Name: "alpha", Message: "Two fixture installations"}}}, nil
}
func (f *ptyService) AssessConflict(context.Context, string) (domain.ConflictAssessment, error) {
	return domain.ConflictAssessment{Name: "alpha", Installations: []domain.ConflictInstallation{
		{ID: "keep-brew", Package: domain.Package{Manager: "brew", ID: "alpha", Version: "1"}, Status: "ready", Project: "alpha", Effective: true, Paths: []domain.Executable{{Path: "/fixture/brew/alpha"}}},
		{ID: "remove-uv", Package: domain.Package{Manager: "uv", ID: "alpha", Version: "1"}, Status: "ready", Project: "alpha", Paths: []domain.Executable{{Path: "/fixture/uv/alpha"}}, Commands: []string{"alpha"}},
	}}, nil
}
func (f *ptyService) PlanResolution(ctx context.Context, request domain.ResolutionRequest) (domain.ActionPlan, error) {
	assessment, _ := f.AssessConflict(ctx, request.Name)
	return domain.ActionPlan{Title: "Remove one fixture installation", Request: domain.ActionRequest{Operation: "remove", Manager: "uv", Package: "alpha"}, Resolution: &domain.ResolutionPlan{Request: request, Keep: assessment.Installations[0], Remove: assessment.Installations[1]}}, nil
}
func (f *ptyService) MaintenanceQueue(context.Context, []string, bool) (domain.MaintenanceQueue, error) {
	return domain.MaintenanceQueue{Jobs: []domain.MaintenanceJob{
		{ID: "brew", Category: "update", Title: "Fixture Homebrew", Representative: "brew", ApplySupported: true},
		{ID: "manual", Category: "guidance", Title: "Fixture manual owner", Representative: "uv", Reason: "Fixture instructions"},
	}}, nil
}
func (f *ptyService) PlanManagerUpdate(context.Context, string) (domain.ActionPlan, error) {
	return domain.ActionPlan{Title: "Update fixture manager", Kind: "manager-update", Request: domain.ActionRequest{Operation: "manager-update", Manager: "brew"}, ManagerUpdate: &domain.ManagerHealth{Manager: "brew", ApplySupported: true}}, nil
}

func (f *ptyService) ExecuteBatchUpgrade(ctx context.Context, plan domain.BatchUpgradePlan, in io.Reader, out, errout io.Writer) (domain.BatchUpgradeResult, error) {
	result := domain.BatchUpgradeResult{Message: "Fixture batch completed"}
	for i, entry := range plan.Entries {
		action, err := f.Execute(ctx, *entry.Plan, in, out, errout)
		if err == nil && len(plan.Entries) > 1 && i == 1 && !f.batchFailed {
			f.batchFailed = true
			err = errors.New("fixture batch pause")
		}
		state := "success"
		if err != nil {
			state = "failed"
		}
		result.Entries = append(result.Entries, domain.BatchUpgradeItemResult{Entry: entry, State: state, Result: action})
		if err != nil {
			result.Paused = true
			result.Message = "Fixture batch paused"
			for _, rest := range plan.Entries[i:] {
				result.Remaining = append(result.Remaining, rest.Package)
			}
			return result, err
		}
	}
	return result, nil
}
