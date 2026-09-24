package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type inventoryState struct {
	snapshot               domain.Snapshot
	loading, loaded, stale bool
	generation             uint64
	cancel                 context.CancelFunc
	err                    error
	providers              map[string]providerState
}
type inventoryMsg struct {
	key        string
	generation uint64
	snapshot   domain.Snapshot
	err        error
}
type healthMsg struct {
	generation uint64
	health     []domain.ManagerHealth
	err        error
}

// Match the shared service's inventory lifetime. Provider observation times,
// rather than the time an aggregate response arrived, own freshness.
const inventoryTTL = 60 * time.Second

func observationExpired(observedAt, now time.Time) bool {
	return observedAt.IsZero() || observedAt.After(now) || now.Sub(observedAt) >= inventoryTTL
}

func (s *inventoryState) expire(now time.Time) {
	if !s.loaded {
		return
	}
	if len(s.snapshot.Coverage) == 0 {
		s.stale = s.stale || observationExpired(s.snapshot.ObservedAt, now)
		return
	}
	for i := range s.snapshot.Coverage {
		coverage := &s.snapshot.Coverage[i]
		if coverage.Stale || observationExpired(coverage.ObservedAt, now) {
			coverage.Stale = true
			s.stale = true
		}
	}
}

func (m *Model) effectiveManagers() []string {
	if len(m.managerIDs) > 0 {
		return append([]string(nil), m.managerIDs...)
	}
	if m.managerFilter != "" {
		return []string{m.managerFilter}
	}
	return nil
}
func (m *Model) includesManager(id string) bool {
	ids := m.effectiveManagers()
	return len(ids) == 0 || slices.Contains(ids, id)
}
func (m *Model) scopeKey() string {
	ids := m.effectiveManagers()
	if len(ids) == 0 {
		return "default"
	}
	slices.Sort(ids)
	return strings.Join(ids, "\x00")
}
func (m *Model) candidateOrder() []string {
	if len(m.effectiveManagers()) > 0 {
		return m.effectiveManagers()
	}
	return m.preferences.Order
}

func (m *Model) ensureInventory(force bool) tea.Cmd {
	key := m.scopeKey()
	cached := m.inventories[key]
	if cached == nil {
		cached = &inventoryState{}
		m.inventories[key] = cached
	}
	cached.expire(time.Now())
	if s := &m.states[installedView]; s.loading && s.scopeKey == key && !force {
		m.attachDiscover()
		return nil
	}
	if s := &m.states[installedView]; force && s.loading && s.scopeKey == key {
		m.cancelView(installedView)
	}
	if !force && (cached.loading || cached.loaded && !cached.stale) {
		m.attachDiscover()
		return nil
	}
	if cached.cancel != nil {
		cached.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	cached.cancel = cancel
	cached.generation++
	cached.loading = true
	generation := cached.generation
	request := domain.PackageQuery{Kind: "installed", Managers: m.effectiveManagers(), Refresh: force}
	cached.providers = make(map[string]providerState)
	m.attachDiscover()
	return m.startStream(ctx, request, streamTarget{inventoryKey: key, generation: generation})
}

func (m *Model) acceptInventory(msg inventoryMsg) {
	cached := m.inventories[msg.key]
	if cached == nil || cached.generation != msg.generation {
		return
	}
	cached.loading, cached.err = false, msg.err
	if msg.err == nil {
		fresh, retained := retainFailedProviders(cached.snapshot, domain.CloneSnapshot(msg.snapshot))
		for i := range fresh.Coverage {
			if retained[fresh.Coverage[i].Manager] {
				fresh.Coverage[i].Stale = true
			}
		}
		cached.snapshot, cached.loaded, cached.stale = fresh, true, false
	} else {
		cached.stale = cached.loaded
		for i := range cached.snapshot.Coverage {
			cached.snapshot.Coverage[i].Stale = true
			cached.snapshot.Coverage[i].State = "failed"
		}
		if len(cached.snapshot.Coverage) == 0 {
			for _, id := range m.effectiveManagers() {
				cached.snapshot.Coverage = append(cached.snapshot.Coverage, domain.Coverage{Manager: id, State: "failed", Message: msg.err.Error()})
			}
		}
	}
	if cached == m.inventories[m.scopeKey()] {
		m.attachDiscover()
	}
}

func (m *Model) cacheInstalled(s *viewState) {
	key := s.scopeKey
	if key == "" {
		key = m.scopeKey()
	}
	cached := m.inventories[key]
	if cached == nil {
		cached = &inventoryState{}
		m.inventories[key] = cached
	}
	if cached.cancel != nil {
		cached.cancel()
	}
	cached.generation++
	cached.loading = false
	if s.loaded {
		cached.snapshot = domain.CloneSnapshot(s.snapshot)
		cached.loaded = true
		cached.stale = s.stale
		cached.err = s.err
		for i := range cached.snapshot.Coverage {
			if s.retainedManagers[cached.snapshot.Coverage[i].Manager] || s.stale {
				cached.snapshot.Coverage[i].Stale = true
			}
		}
	} else {
		cached.err = s.err
	}
	if key == m.scopeKey() {
		m.attachDiscover()
	}
}

func (m *Model) attachDiscover() {
	s := &m.states[discoverView]
	if !s.loaded {
		return
	}
	var inventory domain.Snapshot
	cached := m.inventories[m.scopeKey()]
	if cached != nil {
		cached.expire(time.Now())
		inventory = domain.CloneSnapshot(cached.snapshot)
	}
	// Search can finish before an inventory started from another view. Represent
	// its missing provider observations as pending, never as not installed.
	for _, candidate := range s.candidates.Packages {
		present := false
		for _, c := range inventory.Coverage {
			if c.Manager == candidate.Manager {
				present = true
				break
			}
		}
		if !present {
			coverage := domain.Coverage{Manager: candidate.Manager, State: "pending"}
			if cached != nil && cached.err != nil {
				coverage.State = "failed"
				coverage.Message = cached.err.Error()
			}
			if cached != nil && cached.loaded && !cached.loading {
				coverage.State = "unsupported"
			}
			inventory.Coverage = append(inventory.Coverage, coverage)
		}
	}
	s.snapshot = domain.AttachInventory(s.candidates, inventory)
	// The shared service may have a newer observation than this UI cache. Never
	// overwrite it with an older installed version or an older negative result.
	now := time.Now()
	for i := range s.snapshot.Packages {
		p := &s.snapshot.Packages[i]
		for _, candidate := range s.candidates.Packages {
			if candidate.Key() == p.Key() && (candidate.InstallState == "installed" || candidate.InstallState == "not_installed") && candidate.InventoryAt.After(p.InventoryAt) {
				p.InstallState = candidate.InstallState
				p.Version = candidate.Version
				p.InstalledVersions = append([]string(nil), candidate.InstalledVersions...)
				p.InventoryAt = candidate.InventoryAt
				p.InventoryStale = candidate.InventoryStale || observationExpired(candidate.InventoryAt, now)
				p.Commands = append([]string(nil), candidate.Commands...)
				p.ExecutablePaths = append([]string(nil), candidate.ExecutablePaths...)
				p.Evidence = append([]domain.Evidence(nil), candidate.Evidence...)
			}
		}
	}
	domain.SortCandidates(s.snapshot.Packages, s.query, m.candidateOrder())
	m.reconcile(discoverView, false)
}

func (m *Model) invalidateInventories() {
	for _, cached := range m.inventories {
		if cached.cancel != nil {
			cached.cancel()
		}
		cached.generation++
		cached.loading = false
		cached.stale = true
		for i := range cached.snapshot.Coverage {
			cached.snapshot.Coverage[i].Stale = true
		}
	}
}

func (m *Model) checkManagerHealth(force bool) tea.Cmd {
	var ids []string
	for _, manager := range m.managers {
		if manager.Path != "" {
			ids = append(ids, manager.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if m.healthCancel != nil {
		m.healthCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.healthCancel = cancel
	m.healthGeneration++
	m.healthLoading = true
	m.healthErr = nil
	generation, service := m.healthGeneration, m.service
	return func() tea.Msg {
		health, err := service.CheckManagers(ctx, ids, force)
		return healthMsg{generation, health, err}
	}
}
func (m *Model) acceptHealth(msg healthMsg) {
	if msg.generation != m.healthGeneration {
		return
	}
	m.healthLoading, m.healthErr = false, msg.err
	if msg.err != nil {
		for i := range m.managers {
			if m.managers[i].Health != nil {
				health := *m.managers[i].Health
				health.Stale = true
				m.managers[i].Health = &health
			}
		}
	}
	for i := range m.managers {
		for j := range msg.health {
			if msg.health[j].Manager == m.managers[i].ID && msg.health[j].Path == m.managers[i].Path && msg.health[j].Version == m.managers[i].Version {
				health := msg.health[j]
				m.managers[i].Health = &health
			}
		}
	}
	m.reconcile(managersView, false)
}

func (m *Model) sidebarManagers() []domain.Manager {
	var managers []domain.Manager
	for _, manager := range m.managers {
		if m.showAllManagers || manager.Available || manager.Path != "" || m.scopeChanged && m.includesManager(manager.ID) {
			managers = append(managers, manager)
		}
	}
	return managers
}

func installationLabel(p *domain.Package) string {
	if !p.Candidate {
		if p.Version == "" {
			return "Installed; version unavailable"
		}
		return p.Version
	}
	var label string
	switch p.InstallState {
	case "installed":
		if len(p.InstalledVersions) > 0 {
			label = strings.Join(p.InstalledVersions, ", ")
		} else {
			label = "Installed; version unavailable"
		}
	case "not_installed":
		label = "Not installed via " + p.Manager
	case "checking":
		label = "Checking installed inventory…"
	case "check_failed":
		label = "Inventory check failed"
	case "unavailable":
		label = "Provider unavailable; not checked"
	default:
		label = "Not checked"
	}
	if p.InventoryStale {
		label += " (stale)"
	}
	return label
}
