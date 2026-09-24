package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (m *Model) maybeWarmUpdates() tea.Cmd {
	if !m.warmUpdatesPending || !m.warmInstalledReady || !m.prefsLoaded || m.executing {
		return nil
	}
	s := &m.states[updatesView]
	if s.attempted || s.loaded || s.loading {
		m.warmUpdatesPending = false
		return nil
	}
	return m.loadView(updatesView)
}
func (m *Model) installedProgress(event domain.QueryEvent, done bool) tea.Cmd {
	if done {
		m.warmInstalledReady = true
	} else if event.Stage == "base" || event.Stage == "enriched" {
		for _, c := range event.Snapshot.Coverage {
			if c.State == "complete" {
				m.warmInstalledReady = true
				break
			}
		}
	}
	return m.maybeWarmUpdates()
}
func (m *Model) invalidateViews() {
	m.warmUpdatesPending = false
	for i := range m.states {
		m.cancelView(viewID(i))
		m.states[i].needsReload = true
	}
	m.invalidateInventories()
}
func (m *Model) refreshAfterMutation(view viewID) tea.Cmd {
	commands := []tea.Cmd{m.loadManagers()}
	if view != managersView {
		commands = append(commands, m.loadView(view))
	}
	if view != updatesView {
		commands = append(commands, m.loadView(updatesView))
	}
	return tea.Batch(commands...)
}

// Retain an executed batch independently from a cancellable new draft. Snapshots
// share no mutable result slices with subsequent skip/recheck drafts.
func (m *Model) saveBatchResult() {
	saved := m.batch
	saved.cancel = nil
	saved.loading = false
	saved.history = append([]domain.BatchUpgradeItemResult(nil), m.batch.history...)
	saved.result.Entries = append([]domain.BatchUpgradeItemResult(nil), m.batch.result.Entries...)
	saved.result.Remaining = clonePackages(m.batch.result.Remaining)
	saved.request.Targets = clonePackages(m.batch.request.Targets)
	m.lastBatch = &saved
	m.lastOperation = "batch"
}
func (m *Model) reopenBatch() {
	if m.lastBatch == nil {
		return
	}
	generation := m.batch.generation + 1
	if m.batch.cancel != nil {
		m.batch.cancel()
	}
	m.batch = *m.lastBatch
	m.batch.generation = generation
	m.batch.cancel = nil
	m.batch.history = append([]domain.BatchUpgradeItemResult(nil), m.lastBatch.history...)
	m.batch.result.Remaining = clonePackages(m.lastBatch.result.Remaining)
	m.modal = batchResultModal
	m.modalOffset = 0
	m.status = ""
}
