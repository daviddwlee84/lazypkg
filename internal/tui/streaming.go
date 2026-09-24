package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type providerState struct {
	stage, state, enrichment string
	ready, done              bool
	elapsed                  time.Duration
}
type streamTarget struct {
	view         viewID
	inventoryKey string
	generation   uint64
	query        string
}
type streamStartedMsg struct {
	target streamTarget
	ctx    context.Context
	events <-chan domain.QueryEvent
}
type streamEventMsg struct {
	streamStartedMsg
	event  domain.QueryEvent
	closed bool
}

func (m *Model) startStream(ctx context.Context, q domain.PackageQuery, target streamTarget) tea.Cmd {
	service := m.service
	return func() tea.Msg { return streamStartedMsg{target, ctx, service.StreamQuery(ctx, q)} }
}
func (m *Model) streamCurrent(target streamTarget) bool {
	if target.inventoryKey != "" {
		cached := m.inventories[target.inventoryKey]
		return cached != nil && cached.loading && cached.generation == target.generation
	}
	return m.states[target.view].loading && m.states[target.view].generation == target.generation
}
func readStream(stream streamStartedMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-stream.ctx.Done():
			return streamEventMsg{streamStartedMsg: stream, event: domain.QueryEvent{Stage: "done", Err: stream.ctx.Err()}}
		case event, ok := <-stream.events:
			return streamEventMsg{streamStartedMsg: stream, event: event, closed: !ok}
		}
	}
}
func (m *Model) acceptStreamStart(msg streamStartedMsg) tea.Cmd {
	if !m.streamCurrent(msg.target) {
		return nil
	}
	return readStream(msg)
}

func (m *Model) beginViewStream(view viewID) {
	s := &m.states[view]
	if view == discoverView && s.query != s.acceptedQuery {
		s.snapshot = domain.Snapshot{}
		s.candidates = domain.Snapshot{}
	}
	s.streaming = true
	s.providers = make(map[string]providerState)
	s.retainedManagers = nil
	for i := range s.snapshot.Coverage {
		s.snapshot.Coverage[i].Stale = true
	}
	for i := range s.snapshot.Packages {
		s.snapshot.Packages[i].InventoryStale = true
	}
}

func (m *Model) acceptStreamEvent(msg streamEventMsg) tea.Cmd {
	if !m.streamCurrent(msg.target) {
		return nil
	}
	event := msg.event
	done := msg.closed || event.Stage == "done"
	if msg.target.inventoryKey != "" {
		cached := m.inventories[msg.target.inventoryKey]
		if !done {
			if cached.providers == nil {
				cached.providers = make(map[string]providerState)
			}
			if event.Stage != "cache" || !cached.providers[event.Manager].done {
				cached.snapshot = replaceProvider(cached.snapshot, event)
				cached.providers[event.Manager] = providerProgress(event)
				cached.loaded = true
			}
		} else {
			cached.loading = false
			cached.err = event.Err
			if !msg.closed && snapshotPresent(event.Snapshot) {
				fresh, _ := retainFailedProviders(cached.snapshot, domain.CloneSnapshot(event.Snapshot))
				cached.snapshot = fresh
				cached.loaded = true
			}
			cached.stale = event.Err != nil
		}
		if cached == m.inventories[m.scopeKey()] {
			m.attachDiscover()
		}
	} else {
		s := &m.states[msg.target.view]
		if !done {
			if s.providers == nil {
				s.providers = make(map[string]providerState)
			}
			if event.Stage != "cache" || !s.providers[event.Manager].done {
				s.snapshot = replaceProvider(s.snapshot, event)
				s.providers[event.Manager] = providerProgress(event)
				s.loaded = true
				s.stale = false
				s.acceptedQuery = msg.target.query
			}
		} else {
			s.loading = false
			s.err = event.Err
			s.stale = event.Err != nil
			if !msg.closed && snapshotPresent(event.Snapshot) {
				fresh := domain.CloneSnapshot(event.Snapshot)
				var retained map[string]bool
				if msg.target.view != discoverView {
					fresh, retained = retainFailedProviders(s.snapshot, fresh)
				}
				s.snapshot = fresh
				s.retainedManagers = retained
				s.loaded = true
			}
			for _, coverage := range s.snapshot.Coverage {
				if old, ok := s.providers[coverage.Manager]; !ok || old.stage != "cache" {
					progress := providerFromCoverage("done", coverage)
					progress.elapsed = old.elapsed
					s.providers[coverage.Manager] = progress
				}
			}
			s.acceptedQuery = msg.target.query
		}
		if msg.target.view == discoverView {
			for i := range s.snapshot.Packages {
				s.snapshot.Packages[i].Candidate = true
			}
			s.candidates = domain.CloneSnapshot(s.snapshot)
			m.attachDiscover()
		}
		m.reconcile(msg.target.view, false)
		if msg.target.view == installedView {
			m.publishStreamInventory(s)
		}
	}
	var warm tea.Cmd
	if msg.target.inventoryKey != "" && m.inventories[msg.target.inventoryKey] == m.inventories[m.scopeKey()] || msg.target.inventoryKey == "" && msg.target.view == installedView && m.states[installedView].scopeKey == m.scopeKey() {
		warm = m.installedProgress(event, done)
	}
	if done {
		return warm
	}
	return tea.Batch(readStream(msg.streamStartedMsg), warm)
}

func snapshotPresent(s domain.Snapshot) bool {
	return s.Packages != nil || s.Coverage != nil || s.Issues != nil
}
func providerFromCoverage(stage string, c domain.Coverage) providerState {
	return providerState{stage: stage, state: c.State, enrichment: c.Enrichment, ready: stage != "cache" && c.State == "complete" && !c.Stale, done: stage != "cache" && c.State != "pending"}
}
func providerProgress(event domain.QueryEvent) providerState {
	for _, c := range event.Snapshot.Coverage {
		if c.Manager == event.Manager {
			p := providerFromCoverage(event.Stage, c)
			p.elapsed = event.Elapsed
			return p
		}
	}
	return providerState{stage: event.Stage}
}

func replaceProvider(previous domain.Snapshot, event domain.QueryEvent) domain.Snapshot {
	if event.Manager == "" {
		return previous
	}
	out := domain.CloneSnapshot(previous)
	batch := domain.CloneSnapshot(event.Snapshot)
	old := []domain.Package{}
	for _, p := range out.Packages {
		if p.Manager == event.Manager {
			old = append(old, p)
		}
	}
	out.Packages = slices.DeleteFunc(out.Packages, func(p domain.Package) bool { return p.Manager == event.Manager })
	out.Coverage = slices.DeleteFunc(out.Coverage, func(c domain.Coverage) bool { return c.Manager == event.Manager })
	out.Issues = slices.DeleteFunc(out.Issues, func(issue domain.Issue) bool { return issue.Manager == event.Manager })
	failed := false
	for i := range batch.Coverage {
		c := &batch.Coverage[i]
		if event.Stage == "cache" {
			c.Stale = true
		}
		if c.State == "failed" {
			failed = true
		}
	}
	if failed && len(batch.Packages) == 0 && !providerInstanceChanged(previous, batch, event.Manager) {
		batch.Packages = old
		for i := range batch.Coverage {
			batch.Coverage[i].Stale = true
		}
	}
	for i := range batch.Packages {
		if failed || event.Stage == "cache" {
			batch.Packages[i].InventoryStale = true
		}
	}
	out.Packages = append(out.Packages, batch.Packages...)
	out.Coverage = append(out.Coverage, batch.Coverage...)
	out.Issues = append(out.Issues, batch.Issues...)
	if batch.ObservedAt.After(out.ObservedAt) {
		out.ObservedAt = batch.ObservedAt
	}
	sort.SliceStable(out.Packages, func(i, j int) bool {
		a, b := out.Packages[i], out.Packages[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Manager != b.Manager {
			return a.Manager < b.Manager
		}
		return a.Version < b.Version
	})
	return out
}

func (m *Model) publishStreamInventory(s *viewState) {
	key := s.scopeKey
	if key == "" {
		key = m.scopeKey()
	}
	cached := m.inventories[key]
	if cached == nil {
		cached = &inventoryState{}
		m.inventories[key] = cached
	}
	cached.snapshot = domain.CloneSnapshot(s.snapshot)
	cached.attempted = true
	cached.needsReload = false
	cached.loaded = s.loaded
	cached.loading = s.loading
	cached.stale = s.stale
	cached.err = s.err
	if key == m.scopeKey() {
		m.attachDiscover()
	}
}

func (m *Model) streamStatus(s *viewState) string {
	complete, enriching := 0, 0
	for _, p := range s.providers {
		if p.done {
			complete++
		}
		if p.enrichment == "pending" {
			enriching++
		}
	}
	total := len(m.effectiveManagers())
	if total < len(s.providers) {
		total = len(s.providers)
	}
	if total == 0 {
		return "discovering providers…"
	}
	status := fmt.Sprintf("%d/%d providers finished", complete, total)
	if enriching > 0 {
		status += fmt.Sprintf(" · %d ownership pending", enriching)
	}
	return status
}
