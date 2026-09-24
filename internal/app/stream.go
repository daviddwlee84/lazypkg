package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/backend"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (a *App) limits() {
	a.limitsOnce.Do(func() { a.querySem = make(chan struct{}, 4); a.enrichmentSem = make(chan struct{}, 3) })
}
func acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) Query(ctx context.Context, q domain.PackageQuery) (domain.Snapshot, error) {
	var out domain.Snapshot
	var err error
	for event := range a.StreamQuery(ctx, q) {
		if event.Stage == "done" {
			out, err = event.Snapshot, event.Err
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func (a *App) StreamQuery(ctx context.Context, q domain.PackageQuery) <-chan domain.QueryEvent {
	ch := make(chan domain.QueryEvent, 8)
	go func() {
		defer close(ch)
		emit := func(e domain.QueryEvent) bool {
			e.Snapshot = domain.CloneSnapshot(e.Snapshot)
			select {
			case ch <- e:
				return true
			case <-ctx.Done():
				return false
			}
		}
		start := time.Now()
		var result domain.Snapshot
		var err error
		if q.Kind == "search" {
			result, err = a.searchQuery(ctx, q)
		} else {
			result, err = a.streamRead(ctx, q, emit)
		}
		end := domain.QueryEvent{Stage: "done", Snapshot: result, Err: err, Elapsed: time.Since(start)}
		// Cancellation may leave no consumer. Closing is also terminal, and
		// the final event is best-effort only in that case.
		if ctx.Err() != nil {
			select {
			case ch <- end:
			default:
			}
			return
		}
		emit(end)
	}()
	return ch
}

func (a *App) streamRead(ctx context.Context, q domain.PackageQuery, emit func(domain.QueryEvent) bool) (domain.Snapshot, error) {
	all := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	if q.Kind != "installed" && q.Kind != "outdated" {
		return all, fmt.Errorf("unknown query %q", q.Kind)
	}
	cfg := a.settings()
	ids, err := cfg.Select(q)
	if err != nil {
		return all, err
	}
	contextKey := a.queryContext()
	if q.Refresh {
		a.invalidateDetection()
	}
	a.cacheMu.Lock()
	if a.memoryContext != "" && a.memoryContext != contextKey {
		a.inventory = nil
		a.updateCache = nil
		a.managerCache = nil
		a.cacheEpoch++
	}
	a.memoryContext = contextKey
	epoch := a.cacheEpoch
	a.cacheMu.Unlock()
	seeds := map[string]queryDisk{}
	batches := map[string]domain.Snapshot{}
	for _, id := range ids {
		if disk, ok := a.loadQueryDisk(contextKey, q.Kind, id); ok {
			seeds[id] = disk
			s := staleSnapshot(disk.Snapshot)
			batches[id] = s
			if !emit(domain.QueryEvent{Stage: "cache", Manager: id, Snapshot: filterSnapshot(s, q.Query), Cached: true}) {
				return aggregate(batches, q.Query), ctx.Err()
			}
		}
	}
	managers, err := a.Managers(ctx)
	if err != nil {
		return aggregate(batches, q.Query), err
	}
	lookup := map[string]domain.Manager{}
	for _, m := range managers {
		lookup[m.ID] = m
	}
	explicit := q.Managers != nil || q.Group != "" || q.Set != ""
	a.limits()
	events := make(chan domain.QueryEvent, len(ids)*2+1)
	var wg sync.WaitGroup
	for _, id := range ids {
		m, exists := lookup[id]
		c := domain.Coverage{Manager: id, Instance: instance(m), ObservedAt: time.Now()}
		switch {
		case !exists:
			c.State = "unsupported"
			c.Message = "not supported on this platform"
		case m.Scope != "" && m.Scope != "global":
			c.State = "excluded"
			c.Message = "scope " + m.Scope + " is not enabled for global/user queries"
		case !m.Available:
			c.State = "unavailable"
			c.Message = m.Status
			if m.Requirement != "" {
				c.Message += " (requires " + m.Requirement + ")"
			}
		case !m.Supports(q.Kind):
			c.State = "unsupported"
			c.Message = "does not support " + q.Kind
		}
		if c.State != "" {
			s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now(), Coverage: []domain.Coverage{c}}
			if explicit || m.Path != "" {
				s.Issues = []domain.Issue{{Manager: id, Kind: c.State, Message: c.Message}}
			}
			batches[id] = s
			if !emit(domain.QueryEvent{Stage: "base", Manager: id, Snapshot: s}) {
				return aggregate(batches, q.Query), ctx.Err()
			}
			continue
		}
		wg.Add(1)
		go func(m domain.Manager) {
			defer wg.Done()
			start := time.Now()
			var s domain.Snapshot
			var cached bool
			if !q.Refresh {
				s, cached = a.cachedQuery(m, q.Kind, true)
			}
			if !cached {
				key := fmt.Sprintf("%d:%s:%s:%s", epoch, contextKey, q.Kind, inventoryKey(m))
				s, err := a.queryJobs.do(ctx, key, func(work context.Context) (domain.Snapshot, error) {
					return a.liveBatch(work, m, q.Kind, contextKey, epoch)
				})
				s = domain.CloneSnapshot(s)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					s = domain.Snapshot{Coverage: []domain.Coverage{{Manager: m.ID, Instance: instance(m), State: "failed", Message: err.Error(), ObservedAt: start}}, Issues: []domain.Issue{{Manager: m.ID, Message: err.Error()}}}
				}
				if !completeBatch(s) {
					old, ok := a.cachedQuery(m, q.Kind, false)
					if !ok {
						disk, found := seeds[m.ID]
						if found && inventoryKey(disk.Manager) == inventoryKey(m) {
							old, ok = disk.Snapshot, true
						}
					}
					if ok && len(s.Packages) == 0 {
						s.Packages = staleSnapshot(old).Packages
						for i := range s.Coverage {
							s.Coverage[i].Stale = true
							if len(old.Coverage) > 0 {
								s.Coverage[i].ObservedAt = old.Coverage[0].ObservedAt
							}
						}
					}
				}
				select {
				case events <- domain.QueryEvent{Stage: "base", Manager: m.ID, Snapshot: s, Elapsed: time.Since(start)}:
				case <-ctx.Done():
					return
				}
				if q.Kind == "installed" && completeBatch(s) {
					a.sendEnrichment(ctx, m, s, contextKey, epoch, start, events)
				}
				return
			}
			select {
			case events <- domain.QueryEvent{Stage: "base", Manager: m.ID, Snapshot: s, Cached: true, Elapsed: time.Since(start)}:
			case <-ctx.Done():
				return
			}
			if q.Kind == "installed" && completeBatch(s) && s.Coverage[0].Enrichment != "complete" {
				a.sendEnrichment(ctx, m, s, contextKey, epoch, start, events)
			}
		}(m)
	}
	go func() { wg.Wait(); close(events) }()
	for event := range events {
		batches[event.Manager] = event.Snapshot
		event.Snapshot = filterSnapshot(event.Snapshot, q.Query)
		if !emit(event) {
			return aggregate(batches, q.Query), ctx.Err()
		}
	}
	return aggregate(batches, q.Query), ctx.Err()
}

func (a *App) liveBatch(ctx context.Context, m domain.Manager, kind, contextKey string, epoch uint64) (domain.Snapshot, error) {
	if err := acquire(ctx, a.querySem); err != nil {
		return domain.Snapshot{}, err
	}
	defer func() { <-a.querySem }()
	observed := time.Now()
	var s domain.Snapshot
	var err error
	if m.ID == "mise" {
		var mi backend.Mise
		mi, err = a.mise()
		if err == nil {
			if kind == "installed" {
				s, err = mi.Installed(ctx)
			} else {
				s, err = mi.Outdated(ctx)
			}
		}
	} else if m.ID == "gh-ext" {
		g := a.ghProvider(m)
		if kind == "installed" {
			s, err = g.Installed(ctx)
		} else {
			s, err = g.Outdated(ctx)
		}
	} else {
		var mpm *backend.MPM
		mpm, err = a.provider(ctx)
		if err == nil {
			s, err = mpm.Packages(ctx, kind, "", m.ID)
		}
	}
	c := domain.Coverage{Manager: m.ID, Instance: instance(m), State: "complete", ObservedAt: observed}
	if kind == "installed" {
		c.Enrichment = "pending"
	}
	if err != nil {
		c.State = "failed"
		c.Message = err.Error()
		s.Issues = append(s.Issues, domain.Issue{Manager: m.ID, Message: err.Error()})
	}
	for _, issue := range s.Issues {
		if issue.Kind != "notice" && issue.Kind != "enrichment" {
			c.State = "failed"
			if c.Message == "" {
				c.Message = issue.Message
			}
		}
	}
	s.ObservedAt = observed
	s.Coverage = []domain.Coverage{c}
	if s.Packages == nil {
		s.Packages = []domain.Package{}
	}
	for i := range s.Packages {
		s.Packages[i].Instance = c.Instance
		s.Packages[i].InventoryAt = observed
	}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	if completeBatch(s) {
		a.storeQuery(m, kind, contextKey, s, epoch)
	}
	return s, nil
}

func (a *App) sendEnrichment(ctx context.Context, m domain.Manager, s domain.Snapshot, contextKey string, epoch uint64, start time.Time, events chan<- domain.QueryEvent) {
	key := fmt.Sprintf("%d:%s:%s:%d", epoch, contextKey, inventoryKey(m), s.ObservedAt.UnixNano())
	enriched, err := a.enrichmentJobs.do(ctx, key, func(work context.Context) (domain.Snapshot, error) {
		if err := acquire(work, a.enrichmentSem); err != nil {
			return s, err
		}
		defer func() { <-a.enrichmentSem }()
		out := domain.CloneSnapshot(s)
		var issues []domain.Issue
		out.Packages, issues = a.diagnosticEngine().Enrich(work, out.Packages)
		if work.Err() != nil {
			return out, work.Err()
		}
		out.Coverage[0].Enrichment = "complete"
		if len(issues) > 0 {
			out.Coverage[0].Enrichment = "partial"
		}
		for i := range issues {
			issues[i].Kind = "enrichment"
		}
		out.Issues = append(out.Issues, issues...)
		a.storeQuery(m, "installed", contextKey, out, epoch)
		return out, nil
	})
	if err != nil {
		return
	}
	select {
	case events <- domain.QueryEvent{Stage: "enriched", Manager: m.ID, Snapshot: enriched, Elapsed: time.Since(start)}:
	case <-ctx.Done():
	}
}

func completeBatch(s domain.Snapshot) bool {
	return len(s.Coverage) == 1 && s.Coverage[0].State == "complete" && !s.Coverage[0].Stale
}
func staleSnapshot(s domain.Snapshot) domain.Snapshot {
	s = domain.CloneSnapshot(s)
	for i := range s.Coverage {
		s.Coverage[i].Stale = true
	}
	for i := range s.Packages {
		s.Packages[i].InventoryStale = true
	}
	return s
}
func (a *App) cachedQuery(m domain.Manager, kind string, freshOnly bool) (domain.Snapshot, bool) {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	cache := a.inventory
	if kind == "outdated" {
		cache = a.updateCache
	}
	s, ok := cache[inventoryKey(m)]
	if !ok {
		return domain.Snapshot{}, false
	}
	s = domain.CloneSnapshot(s)
	fresh := completeBatch(s) && time.Since(s.Coverage[0].ObservedAt) >= 0 && time.Since(s.Coverage[0].ObservedAt) < inventoryTTL
	if freshOnly && !fresh {
		return domain.Snapshot{}, false
	}
	if !fresh {
		s = staleSnapshot(s)
	}
	return s, true
}
func (a *App) storeQuery(m domain.Manager, kind, contextKey string, s domain.Snapshot, epoch uint64) {
	a.diskMu.Lock()
	defer a.diskMu.Unlock()
	a.cacheMu.Lock()
	if epoch != a.cacheEpoch {
		a.cacheMu.Unlock()
		return
	}
	cache := a.inventory
	if kind == "outdated" {
		cache = a.updateCache
	}
	if cache == nil {
		cache = map[string]domain.Snapshot{}
		if kind == "outdated" {
			a.updateCache = cache
		} else {
			a.inventory = cache
		}
	}
	key := inventoryKey(m)
	old, ok := cache[key]
	if ok && (old.ObservedAt.After(s.ObservedAt) || old.ObservedAt.Equal(s.ObservedAt) && len(old.Coverage) > 0 && old.Coverage[0].Enrichment == "complete" && s.Coverage[0].Enrichment != "complete") {
		a.cacheMu.Unlock()
		return
	}
	cache[key] = domain.CloneSnapshot(s)
	a.cacheMu.Unlock()
	a.saveQueryDisk(contextKey, kind, m, s, epoch)
}
func filterSnapshot(s domain.Snapshot, query string) domain.Snapshot {
	s = domain.CloneSnapshot(s)
	if query == "" {
		return s
	}
	q := strings.ToLower(query)
	rows := []domain.Package{}
	for _, p := range s.Packages {
		if strings.Contains(strings.ToLower(p.ID+" "+p.Name+" "+strings.Join(p.Commands, " ")), q) {
			rows = append(rows, p)
		}
	}
	s.Packages = rows
	return s
}
func aggregate(batches map[string]domain.Snapshot, query string) domain.Snapshot {
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	ids := make([]string, 0, len(batches))
	for id := range batches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		mergeSnapshot(&s, batches[id])
	}
	sort.SliceStable(s.Packages, func(i, j int) bool {
		a, b := s.Packages[i], s.Packages[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Key() < b.Key()
	})
	return filterSnapshot(s, query)
}
