package app

import (
	"context"
	"fmt"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (a *App) homebrew(m domain.Manager) *backend.Homebrew {
	return &backend.Homebrew{Path: m.Path, Runner: a.Runner, Env: a.childEnv(), Timeout: a.settings().Timeout()}
}

// Query identities share only work currently in flight. There is no retained
// identity-index cache that could authorize a write after a tap change.
func (a *App) normalizeHomebrew(ctx context.Context, m domain.Manager, kind, query string, s domain.Snapshot) (domain.Snapshot, error) {
	if m.ID != "brew" && m.ID != "cask" {
		return s, nil
	}
	g := a.homebrew(m)
	if kind == "search" {
		return g.Catalog(ctx, m.ID, query, s)
	}
	a.cacheMu.Lock()
	epoch := a.cacheEpoch
	a.cacheMu.Unlock()
	key := fmt.Sprintf("%d:%s:%s", epoch, a.queryContext(), inventoryKey(m))
	index, err := a.homebrewJobs.do(ctx, key, func(work context.Context) (*backend.HomebrewIndex, error) { return g.InstalledIndex(work, m.ID) })
	if err != nil {
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		s = domain.CloneSnapshot(s)
		for i := range s.Packages {
			s.Packages[i].Identity = &domain.PackageIdentity{State: "unresolved", Source: "Homebrew installed metadata", Reason: err.Error()}
		}
		s.Issues = append(s.Issues, domain.Issue{Manager: m.ID, Kind: "identity", Message: "Homebrew package identity could not be verified: " + err.Error()})
		return s, nil
	}
	return index.Normalize(s), nil
}

// Action resolution is always a fresh native read, never a session snapshot or
// a shared index started by an earlier view query.
func (a *App) resolveBrewAction(ctx context.Context, req domain.ActionRequest, m domain.Manager) (domain.ActionRequest, *domain.PackageIdentity, error) {
	if req.Manager != "brew" && req.Manager != "cask" {
		return req, nil, nil
	}
	g := a.homebrew(m)
	var id domain.PackageIdentity
	var err error
	if req.Operation == "install" {
		id, err = g.ResolveCatalog(ctx, req.Manager, req.Package)
	} else {
		var index *backend.HomebrewIndex
		index, err = g.InstalledIndex(ctx, req.Manager)
		if err == nil {
			id, err = index.Resolve(req.Package)
		}
	}
	if err != nil {
		return req, &id, err
	}
	if id.State != "verified" || id.CanonicalID == "" {
		return req, &id, fmt.Errorf("Homebrew identity is %s: %s", id.State, id.Reason)
	}
	req.Package = id.CanonicalID
	return req, &id, nil
}
