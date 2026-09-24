package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/maintenance"
)

func (a *App) maintenanceEngine() *maintenance.Engine {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.maintenance == nil {
		dir := a.Config.CacheDir
		if dir == "" {
			dir = filepath.Join(a.Config.DataDir, "cache")
		}
		engine := maintenance.New(a.Runner, dir)
		engine.Env = a.childEnvLocked()
		a.maintenance = engine
	}
	return a.maintenance
}
func (a *App) CheckManagers(ctx context.Context, ids []string, force bool) ([]domain.ManagerHealth, error) {
	if force {
		a.cacheMu.Lock()
		a.cacheEpoch++
		a.managerCache = nil
		a.cacheMu.Unlock()
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	filtered := len(ids) > 0
	for _, id := range ids {
		id = backend.NormalizeManager(id)
		if !backend.Known(id) {
			return nil, fmt.Errorf("unknown manager %q", id)
		}
		selected[id] = true
	}
	var targets []domain.Manager
	for _, m := range managers {
		if (!filtered && m.Path != "") || selected[m.ID] {
			targets = append(targets, m)
			delete(selected, m.ID)
		}
	}
	for id := range selected {
		return nil, fmt.Errorf("%s is not supported on this platform", id)
	}
	return a.maintenanceEngine().Check(ctx, targets, force)
}
func (a *App) PlanManagerUpdate(ctx context.Context, id string) (domain.ActionPlan, error) {
	id = backend.NormalizeManager(id)
	managers, err := a.Managers(ctx)
	if err != nil {
		return domain.ActionPlan{}, err
	}
	for _, m := range managers {
		if m.ID == id {
			return a.maintenanceEngine().Plan(ctx, m)
		}
	}
	return domain.ActionPlan{}, fmt.Errorf("manager %q is not supported on this platform", id)
}
