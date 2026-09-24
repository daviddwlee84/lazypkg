package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

const inventoryTTL = 60 * time.Second

func (a *App) settings() config.Config { a.mu.Lock(); defer a.mu.Unlock(); return a.Config }
func (a *App) Preferences(context.Context) (domain.ManagerPreferences, error) {
	return a.settings().Preferences(), nil
}
func (a *App) SaveManagerSet(ctx context.Context, name string, ids []string, makeDefault bool) (domain.ManagerPreferences, error) {
	if err := ctx.Err(); err != nil {
		return domain.ManagerPreferences{}, err
	}
	if !a.writeMu.TryLock() {
		return domain.ManagerPreferences{}, fmt.Errorf("another operation is running")
	}
	defer a.writeMu.Unlock()
	c := a.settings()
	next, err := c.SaveSet(name, ids, makeDefault)
	if err != nil {
		return domain.ManagerPreferences{}, err
	}
	a.mu.Lock()
	a.Config = next
	a.mu.Unlock()
	return next.Preferences(), nil
}
func (a *App) Managers(ctx context.Context) ([]domain.Manager, error) {
	observedAt := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	contextKey := a.queryContext()
	a.cacheMu.Lock()
	epoch := a.cacheEpoch
	if len(a.managerCache) > 0 && a.managerCacheContext == contextKey && time.Since(a.managerCacheAt) < 5*time.Second {
		out := append([]domain.Manager(nil), a.managerCache...)
		a.cacheMu.Unlock()
		return out, nil
	}
	a.cacheMu.Unlock()
	out, err := a.managerJobs.do(ctx, fmt.Sprintf("%d:%s", epoch, contextKey), func(work context.Context) ([]domain.Manager, error) {
		m, err := a.provider(work)
		if err != nil {
			return nil, err
		}
		out, err := m.Managers(work)
		if err == nil {
			a.refineGH(work, out)
		}
		if err != nil {
			a.mu.Lock()
			if a.mpm == m {
				a.mpm = nil
			}
			a.mu.Unlock()
		}
		if err == nil && work.Err() == nil && a.queryContext() == contextKey {
			a.cacheMu.Lock()
			if epoch == a.cacheEpoch && !a.managerCacheAt.After(observedAt) {
				a.managerCache = append([]domain.Manager(nil), out...)
				a.managerCacheAt = observedAt
				a.managerCacheContext = contextKey
			}
			a.cacheMu.Unlock()
		}
		return out, err
	})
	return append([]domain.Manager(nil), out...), err
}
func (a *App) mise() (backend.Mise, error) {
	p, err := a.Bootstrap.Lookup("mise")
	return backend.Mise{Path: p, Runner: a.Runner, Env: a.childEnv(), Timeout: a.settings().Timeout()}, err
}
func instance(m domain.Manager) string {
	if m.Instance != "" {
		return m.Instance
	}
	if path, err := filepath.EvalSymlinks(m.Path); err == nil {
		return path
	}
	return m.Path
}
func inventoryKey(m domain.Manager) string { return m.ID + "\x00" + instance(m) + "\x00" + m.Version }

func (a *App) invalidateDetection() {
	a.mu.Lock()
	a.cacheMu.Lock()
	a.cacheEpoch++
	a.managerCache = nil
	a.managerCacheContext = ""
	a.mpm = nil
	a.ghProviders = nil
	a.cacheMu.Unlock()
	a.mu.Unlock()
}
func (a *App) invalidateInventory() {
	a.diskMu.Lock()
	defer a.diskMu.Unlock()
	a.mu.Lock()
	a.mpm = nil
	a.cacheMu.Lock()
	a.cacheEpoch++
	a.inventory = nil
	a.updateCache = nil
	a.managerCache = nil
	a.managerCacheContext = ""
	a.managerCacheAt = time.Time{}
	a.cacheMu.Unlock()
	a.mu.Unlock()
	a.clearQueryDisk()
}
func (a *App) cached(m domain.Manager, freshOnly bool) (domain.Snapshot, bool) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	s, ok := a.inventory[inventoryKey(m)]
	if !ok {
		return domain.Snapshot{}, false
	}
	s = domain.CloneSnapshot(s)
	fresh := len(s.Coverage) == 1 && s.Coverage[0].State == "complete" && !s.Coverage[0].Stale && time.Since(s.Coverage[0].ObservedAt) < inventoryTTL
	if freshOnly && !fresh {
		return domain.Snapshot{}, false
	}
	if !fresh {
		for i := range s.Coverage {
			s.Coverage[i].Stale = true
		}
	}
	return s, true
}
func mergeSnapshot(into *domain.Snapshot, s domain.Snapshot) {
	into.Packages = append(into.Packages, s.Packages...)
	into.Issues = append(into.Issues, s.Issues...)
	into.Coverage = append(into.Coverage, s.Coverage...)
}

// Packages retains source compatibility for internal callers from v0.1.0.
func (a *App) Packages(ctx context.Context, kind, query, manager string) (domain.Snapshot, error) {
	q := domain.PackageQuery{Kind: kind, Query: query}
	if manager != "" {
		q.Managers = []string{manager}
	}
	return a.Query(ctx, q)
}

// Write validation always uses fresh native inventory, without optional enrichment.
func (a *App) packages(ctx context.Context, kind, query, manager string, enrich bool) (domain.Snapshot, error) {
	q := domain.PackageQuery{Kind: kind, Query: query, Refresh: true}
	if manager != "" {
		q.Managers = []string{manager}
	}
	return a.read(ctx, q, enrich, false)
}

func (a *App) searchQuery(ctx context.Context, q domain.PackageQuery) (domain.Snapshot, error) {
	if q.Refresh {
		a.invalidateDetection()
	}
	s, err := a.read(ctx, q, q.Kind == "installed", q.Kind == "installed" && !q.Refresh)
	if err != nil {
		return s, err
	}
	if q.Kind != "search" {
		return s, nil
	}
	cfg := a.settings()
	ids, err := cfg.Select(q)
	if err != nil {
		return s, err
	}
	inventory := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	if q.DeferInventory {
		managers, e := a.Managers(ctx)
		if e != nil {
			return s, e
		}
		lookup := map[string]domain.Manager{}
		for _, m := range managers {
			lookup[m.ID] = m
		}
		for _, id := range ids {
			m, ok := lookup[id]
			if ok {
				if cached, found := a.cached(m, false); found {
					mergeSnapshot(&inventory, cached)
					continue
				}
			}
			inventory.Coverage = append(inventory.Coverage, domain.Coverage{Manager: id, Instance: instance(m), State: "pending"})
		}
	} else {
		inventory, err = a.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: ids, Refresh: q.Refresh})
		if err != nil {
			if ctx.Err() != nil {
				return s, ctx.Err()
			}
			for _, id := range ids {
				inventory.Coverage = append(inventory.Coverage, domain.Coverage{Manager: id, State: "failed", Message: err.Error()})
			}
		}
	}
	s = domain.AttachInventory(s, inventory)
	// Command presence is a separate observation, not an assertion of package identity.
	name := strings.TrimSpace(q.Query)
	if identifier.MatchString(name) && !strings.ContainsAny(name, "/@:") {
		for _, p := range s.Packages {
			if strings.EqualFold(p.ID, name) {
				report, e := a.diagnosticEngine().Diagnose(ctx, name, inventory.Packages)
				if e == nil {
					for i := range s.Packages {
						if strings.EqualFold(s.Packages[i].ID, name) {
							s.Packages[i].PathMatches = report.Executables
						}
					}
				}
				break
			}
		}
	}
	order, err := cfg.Order(q)
	if err != nil {
		return s, err
	}
	domain.SortCandidates(s.Packages, q.Query, order)
	if err := ctx.Err(); err != nil {
		return s, err
	}
	return s, nil
}

func (a *App) read(ctx context.Context, q domain.PackageQuery, enrich, useCache bool) (domain.Snapshot, error) {
	a.cacheMu.Lock()
	epoch := a.cacheEpoch
	a.cacheMu.Unlock()
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	if q.Kind != "installed" && q.Kind != "search" && q.Kind != "outdated" {
		return s, fmt.Errorf("unknown query %q", q.Kind)
	}
	if q.Kind == "search" {
		q.Query = strings.TrimSpace(q.Query)
		if q.Query == "" {
			return s, fmt.Errorf("search query is required")
		}
		if strings.ContainsAny(q.Query, "\x00\r\n\"`$;&|<>%!\u001b") {
			return s, fmt.Errorf("query contains shell metacharacters unsupported by downstream managers")
		}
	}
	cfg := a.settings()
	ids, err := cfg.Select(q)
	if err != nil {
		return s, err
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return s, err
	}
	mpm, err := a.provider(ctx)
	if err != nil {
		return s, err
	}
	lookup := map[string]domain.Manager{}
	for _, m := range managers {
		lookup[m.ID] = m
	}
	targets := []domain.Manager{}
	explicit := q.Managers != nil || q.Group != "" || q.Set != ""
	for _, id := range ids {
		m, ok := lookup[id]
		coverage := domain.Coverage{Manager: id, Instance: instance(m), ObservedAt: time.Now()}
		switch {
		case !ok:
			coverage.State = "unsupported"
			coverage.Message = "not supported on this platform"
		case m.Scope != "" && m.Scope != "global":
			coverage.State = "excluded"
			coverage.Message = "scope " + m.Scope + " is not enabled for global/user queries"
		case !m.Available:
			coverage.State = "unavailable"
			coverage.Message = m.Status
			if m.Requirement != "" {
				coverage.Message += " (requires " + m.Requirement + ")"
			}
		case !m.Supports(q.Kind):
			coverage.State = "unsupported"
			coverage.Message = "does not support " + q.Kind
		default:
			if useCache {
				if cached, ok := a.cached(m, true); ok {
					mergeSnapshot(&s, cached)
					continue
				}
			}
			targets = append(targets, m)
			continue
		}
		s.Coverage = append(s.Coverage, coverage)
		if explicit || m.Path != "" {
			s.Issues = append(s.Issues, domain.Issue{Manager: id, Message: coverage.Message, Kind: coverage.State})
		}
	}
	type reply struct {
		m   domain.Manager
		s   domain.Snapshot
		err error
	}
	ch := make(chan reply, len(targets))
	a.limits()
	limit := a.querySem
	for _, m := range targets {
		go func(m domain.Manager) {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				ch <- reply{m: m, err: ctx.Err()}
				return
			}
			defer func() { <-limit }()
			var result domain.Snapshot
			var e error
			if m.ID == "mise" && q.Kind != "search" {
				var mi backend.Mise
				mi, e = a.mise()
				if e == nil {
					if q.Kind == "installed" {
						result, e = mi.Installed(ctx)
					} else {
						result, e = mi.Outdated(ctx)
					}
				}
			} else if m.ID == "gh-ext" && q.Kind != "search" {
				g := a.ghProvider(m)
				if q.Kind == "installed" {
					result, e = g.Installed(ctx)
				} else {
					result, e = g.Outdated(ctx)
				}
			} else if m.ID == "uvx" && q.Kind == "search" {
				result, e = backend.PyPIExact(ctx, a.HTTPClient, "", q.Query)
			} else {
				query := ""
				if q.Kind == "search" {
					query = q.Query
				}
				result, e = mpm.Packages(ctx, q.Kind, query, m.ID)
			}
			ch <- reply{m, result, e}
		}(m)
	}
	queried := map[string]domain.Manager{}
	for range targets {
		r := <-ch
		queried[r.m.ID] = r.m
		c := domain.Coverage{Manager: r.m.ID, Instance: instance(r.m), State: "complete", ObservedAt: s.ObservedAt}
		if r.err != nil {
			c.State = "failed"
			c.Message = r.err.Error()
			r.s.Issues = append(r.s.Issues, domain.Issue{Manager: r.m.ID, Message: c.Message})
		}
		for _, issue := range r.s.Issues {
			if issue.Kind != "notice" && issue.Kind != "enrichment" {
				c.State = "failed"
				if c.Message == "" {
					c.Message = issue.Message
				}
			}
		}
		if len(r.s.Packages) == 0 && c.State == "failed" && q.Kind == "installed" {
			if old, ok := a.cached(r.m, false); ok {
				r.s.Packages = old.Packages
				c.Stale = true
				if len(old.Coverage) > 0 {
					c.ObservedAt = old.Coverage[0].ObservedAt
				}
			}
		}
		for i := range r.s.Packages {
			p := &r.s.Packages[i]
			p.Instance = c.Instance
			p.InventoryStale = c.Stale
			if q.Kind == "search" {
				p.Candidate = true
				p.InstallState = "checking"
			}
		}
		r.s.Coverage = []domain.Coverage{c}
		mergeSnapshot(&s, r.s)
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if enrich && len(queried) > 0 {
		var issues []domain.Issue
		s.Packages, issues = a.diagnosticEngine().Enrich(ctx, s.Packages)
		for i := range issues {
			issues[i].Kind = "enrichment"
		}
		s.Issues = append(s.Issues, issues...)
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	if q.Kind == "installed" && enrich {
		a.cacheMu.Lock()
		if epoch == a.cacheEpoch {
			if a.inventory == nil {
				a.inventory = map[string]domain.Snapshot{}
			}
			for id, m := range queried {
				one := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: s.ObservedAt}
				for _, p := range s.Packages {
					if p.Manager == id {
						one.Packages = append(one.Packages, p)
					}
				}
				for _, c := range s.Coverage {
					if c.Manager == id {
						one.Coverage = append(one.Coverage, c)
					}
				}
				for _, issue := range s.Issues {
					if issue.Manager == id {
						one.Issues = append(one.Issues, issue)
					}
				}
				key := inventoryKey(m)
				// Overlapping scopes can launch concurrent reads in the same
				// epoch. A slow earlier request must not replace newer data.
				if old, exists := a.inventory[key]; !exists || !old.ObservedAt.After(one.ObservedAt) {
					a.inventory[key] = domain.CloneSnapshot(one)
				}
			}
		}
		a.cacheMu.Unlock()
	}
	if q.Kind != "search" && q.Query != "" {
		rows := s.Packages[:0]
		query := strings.ToLower(q.Query)
		for _, p := range s.Packages {
			if strings.Contains(strings.ToLower(p.ID+" "+p.Name+" "+strings.Join(p.Commands, " ")), query) {
				rows = append(rows, p)
			}
		}
		s.Packages = rows
	}
	sort.SliceStable(s.Packages, func(i, j int) bool {
		a, b := s.Packages[i], s.Packages[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Manager != b.Manager {
			return a.Manager < b.Manager
		}
		return a.Version < b.Version
	})
	sort.Slice(s.Coverage, func(i, j int) bool { return s.Coverage[i].Manager < s.Coverage[j].Manager })
	return s, nil
}

func mergeEnvironment(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		if k == "PATH" && out[k] != "" {
			seen := map[string]bool{}
			var paths []string
			for _, p := range append(filepath.SplitList(v), filepath.SplitList(out[k])...) {
				if !seen[p] {
					seen[p] = true
					paths = append(paths, p)
				}
			}
			out[k] = strings.Join(paths, string(os.PathListSeparator))
		} else {
			out[k] = v
		}
	}
	return out
}
func (a *App) childEnvLocked() map[string]string {
	return mergeEnvironment(a.Bootstrap.ChildEnv(), a.maintenanceEnv)
}
func (a *App) childEnv() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.childEnvLocked()
}
