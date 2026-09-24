package app

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/maintenance"
	"github.com/daviddwlee84/lazypkg/internal/promptkit"
	"github.com/daviddwlee84/lazypkg/internal/resolution"
)

func (a *App) resolutionEngine() *resolution.Engine {
	e := resolution.New(a.Runner)
	e.Env = a.childEnv()
	e.Fresh = a.AssessConflict
	return e
}
func (a *App) AssessConflict(ctx context.Context, name string) (domain.ConflictAssessment, error) {
	if !identifier.MatchString(name) {
		return domain.ConflictAssessment{}, fmt.Errorf("select an exact command name")
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return domain.ConflictAssessment{}, err
	}
	ids := []string{}
	for _, m := range managers {
		if m.Path != "" && m.Scope == "global" {
			ids = append(ids, m.ID)
		}
	}
	var inventory domain.Snapshot
	if len(ids) > 0 {
		inventory, err = a.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: ids, Refresh: true})
	}
	if err != nil {
		return domain.ConflictAssessment{}, err
	}
	managers, err = a.Managers(ctx)
	if err != nil {
		return domain.ConflictAssessment{}, err
	}
	report, err := a.diagnosticEngine().Diagnose(ctx, name, inventory.Packages)
	if err != nil {
		return domain.ConflictAssessment{}, err
	}
	return a.resolutionEngine().Assess(ctx, name, inventory, report, managers)
}
func (a *App) PlanResolution(ctx context.Context, req domain.ResolutionRequest) (domain.ActionPlan, error) {
	return a.resolutionEngine().Plan(ctx, req)
}
func (a *App) MaintenanceQueue(ctx context.Context, ids []string, refresh bool) (domain.MaintenanceQueue, error) {
	health, err := a.CheckManagers(ctx, ids, refresh)
	return maintenance.BuildQueue(health, time.Now()), err
}
func (a *App) RenderPrompt(ctx context.Context, req domain.PromptRequest) (domain.RenderedPrompt, error) {
	if err := promptkit.Validate(req); err != nil {
		return domain.RenderedPrompt{}, err
	}
	work, cancel := context.WithTimeout(ctx, a.settings().Timeout())
	defer cancel()
	s, err := promptkit.Collect(work, req, promptkit.ProviderFunc(a.collectPrompt))
	if err != nil {
		return domain.RenderedPrompt{}, err
	}
	if ctx.Err() != nil {
		return domain.RenderedPrompt{}, ctx.Err()
	}
	return promptkit.Render(req, s, time.Now())
}
func (a *App) collectPrompt(ctx context.Context, req domain.PromptRequest) (promptkit.Snapshot, error) {
	dir, _ := os.Getwd()
	s := promptkit.Snapshot{AppVersion: a.Version, Platform: runtime.GOOS, Architecture: runtime.GOARCH, Directory: dir, CapturedAt: time.Now(), Scope: "inherited context; no shell alias/function inspection"}
	if s.AppVersion == "" {
		s.AppVersion = "development"
	}
	if req.Recipe == promptkit.PathConflict {
		assessment, err := a.AssessConflict(ctx, req.Target)
		if err != nil {
			s.Warnings = append(s.Warnings, domain.Issue{Message: "Conflict collection incomplete: " + err.Error()})
			return s, nil
		}
		s.Conflict = &assessment
		s.Warnings = append(s.Warnings, assessment.Issues...)
		return s, nil
	}
	ids := req.Managers
	if req.Target != "" {
		if len(ids) > 0 {
			return s, fmt.Errorf("choose a manager target or a selection, not both")
		}
		ids = []string{req.Target}
	}
	health, err := a.CheckManagers(ctx, ids, req.Refresh)
	s.Health = health
	queue := maintenance.BuildQueue(health, time.Now())
	s.Queue = &queue
	if err != nil {
		s.Warnings = append(s.Warnings, domain.Issue{Message: "Manager collection incomplete: " + err.Error()})
	}
	return s, nil
}
