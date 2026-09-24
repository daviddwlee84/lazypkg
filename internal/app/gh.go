package app

import (
	"context"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (a *App) ghProvider(m domain.Manager) *backend.GHExtensions {
	g := &backend.GHExtensions{Path: m.Path, Runner: a.Runner, Env: a.childEnv(), Timeout: a.settings().Timeout()}
	key := g.Instance() + ":" + m.Version + ":" + a.queryContext()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ghProviders == nil {
		a.ghProviders = map[string]*backend.GHExtensions{}
	}
	if cached := a.ghProviders[key]; cached != nil {
		return cached
	}
	a.ghProviders[key] = g
	return g
}

func (a *App) refineGH(ctx context.Context, managers []domain.Manager) {
	for i := range managers {
		m := &managers[i]
		if m.ID != "gh-ext" || m.Path == "" {
			continue
		}
		g := a.ghProvider(*m)
		m.Instance = g.Instance()
		if !m.Available {
			continue
		}
		work, cancel := context.WithTimeout(ctx, 3*time.Second)
		supported, err := g.SupportsOutdated(work)
		cancel()
		caps := make([]string, 0, len(m.Capabilities)+1)
		for _, cap := range m.Capabilities {
			if cap != "outdated" {
				caps = append(caps, cap)
			}
		}
		if supported {
			caps = append(caps, "outdated")
		}
		m.Capabilities = caps
		if err != nil {
			m.Errors = append(m.Errors, "Extension update checks unavailable: "+err.Error())
		}
	}
}
