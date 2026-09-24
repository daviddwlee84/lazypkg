// Package tui presents the shared package service as an interactive dashboard.
package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type viewID int

const (
	installedView viewID = iota
	discoverView
	updatesView
	diagnosticsView
	managersView
)

var viewNames = []string{"Installed", "Discover", "Updates", "Diagnostics", "Managers"}

type viewState struct {
	marks            map[string]domain.Package
	markOrder        []string
	snapshot         domain.Snapshot
	candidates       domain.Snapshot
	scopeKey         string
	report           domain.DiagnosticReport
	query            string
	acceptedQuery    string
	selected         string
	position         int
	offset           int
	generation       uint64
	cancel           context.CancelFunc
	loading          bool
	loaded           bool
	attempted        bool
	needsReload      bool
	stale            bool
	force            bool
	streaming        bool
	providers        map[string]providerState
	retainedManagers map[string]bool
	err              error
}

type modalKind int

const (
	noModal modalKind = iota
	detailsModal
	helpModal
	setupModal
	planModal
	versionModal
	resultModal
	issuesModal
	providersModal
	saveSetModal
	resolutionModal
	maintenanceModal
	promptModal
	exportPromptModal
	batchReviewModal
	batchResultModal
)

type setupState struct {
	options    []domain.SetupOption
	selected   map[string]bool
	position   int
	loading    bool
	loaded     bool
	err        error
	generation uint64
	cancel     context.CancelFunc
}

// Model owns UI state. Service calls run only in commands, never during New or View.
type Model struct {
	ctx                context.Context
	service            domain.Service
	view               viewID
	states             [5]viewState
	width              int
	height             int
	managerFocus       bool
	managerFilter      string
	managerIDs         []string
	scopeName          string
	scopeChanged       bool
	preferences        domain.ManagerPreferences
	prefsLoaded        bool
	prefsErr           error
	prefsGeneration    uint64
	prefsCancel        context.CancelFunc
	providerPicker     providerPicker
	inventories        map[string]*inventoryState
	mouseEnabled       bool
	mouseOverride      bool
	mouseEpoch         uint64
	mousePressed       *pressedTarget
	showAllManagers    bool
	healthGeneration   uint64
	healthCancel       context.CancelFunc
	healthLoading      bool
	healthForcePending bool
	healthErr          error
	setGeneration      uint64
	diagnosticName     string
	managerCursor      int
	managers           []domain.Manager
	managersLoading    bool
	managersLoaded     bool
	managersErr        error
	managersGeneration uint64
	managersCancel     context.CancelFunc
	input              textinput.Model
	filtering          bool
	modal              modalKind
	modalOffset        int
	detailKey          string
	detailOffset       int
	setup              setupState
	startSetup         bool
	plan               domain.ActionPlan
	planLoading        bool
	planErr            error
	planGeneration     uint64
	planCancel         context.CancelFunc
	planReturn         modalKind
	versionRequest     domain.ActionRequest
	executing          bool
	lastResult         domain.ActionResult
	lastError          error
	status             string
	pendingG           bool
	quitting           bool
	workflow           workflowState
	batch              batchState
	lastBatch          *batchState
	lastOperation      string
	warmUpdatesPending bool
	warmInstalledReady bool
}

type packagesMsg struct {
	view       viewID
	generation uint64
	query      string
	snapshot   domain.Snapshot
	err        error
}
type diagnosticsMsg struct {
	generation uint64
	report     domain.DiagnosticReport
	err        error
}
type managersMsg struct {
	generation uint64
	managers   []domain.Manager
	err        error
}
type setupMsg struct {
	generation uint64
	options    []domain.SetupOption
	err        error
}
type planMsg struct {
	generation uint64
	plan       domain.ActionPlan
	err        error
}
type executedMsg struct {
	generation uint64
	result     domain.ActionResult
	err        error
}

// New constructs a dashboard without probing the machine or network.
type Options struct{ Mouse *bool }

func New(ctx context.Context, service domain.Service, initialView string, options ...Options) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textinput.New()
	input.CharLimit = 512
	input.SetWidth(60)
	m := &Model{ctx: ctx, service: service, width: 80, height: 24, input: input, scopeName: "Default", mouseEnabled: true, inventories: make(map[string]*inventoryState)}
	for _, option := range options {
		if option.Mouse != nil {
			m.mouseEnabled = *option.Mouse
			m.mouseOverride = true
		}
	}
	m.setup.selected = make(map[string]bool)
	switch strings.ToLower(initialView) {
	case "discover", "search":
		m.view = discoverView
	case "updates", "outdated":
		m.view = updatesView
	case "diagnostics", "diagnose", "doctor":
		m.view = diagnosticsView
	case "managers":
		m.view = managersView
	case "maintenance", "maintenance:refresh":
		m.view = managersView
		m.workflow.initial = initialView
	case "setup":
		m.view = managersView
		m.startSetup = true
	}
	if strings.HasPrefix(initialView, "resolve:") {
		m.view = diagnosticsView
		m.workflow.initial = initialView
	}
	return m
}

// Run owns the terminal until the dashboard is closed.
func Run(ctx context.Context, service domain.Service, initialView string, options ...Options) error {
	m := New(ctx, service, initialView, options...)
	defer m.cancelAll()
	_, err := tea.NewProgram(m, tea.WithContext(m.ctx)).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.loadManagers(), m.loadPreferences()}
	m.warmUpdatesPending = m.workflow.initial == "" && !m.startSetup && m.view != updatesView
	m.warmInstalledReady = false
	if strings.HasPrefix(m.workflow.initial, "maintenance") {
		commands = append(commands, m.openMaintenance(strings.HasSuffix(m.workflow.initial, ":refresh")))
	} else if strings.HasPrefix(m.workflow.initial, "resolve:") {
		commands = append(commands, m.loadConflict(strings.TrimPrefix(m.workflow.initial, "resolve:")))
	} else if m.startSetup {
		m.modal = setupModal
		commands = append(commands, m.loadSetup())
	} else {
		if m.view != managersView {
			commands = append(commands, m.loadView(m.view))
		}
		if m.warmUpdatesPending && m.view != installedView && m.view != discoverView {
			commands = append(commands, m.loadView(installedView))
		}
	}
	return tea.Batch(commands...)
}

func (m *Model) loadManagers() tea.Cmd {
	if m.managersCancel != nil {
		m.managersCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.managersCancel = cancel
	m.managersGeneration++
	generation := m.managersGeneration
	m.managersLoading = true
	service := m.service
	return func() tea.Msg { managers, err := service.Managers(ctx); return managersMsg{generation, managers, err} }
}

func (m *Model) loadView(view viewID) tea.Cmd {
	if view == updatesView {
		m.warmUpdatesPending = false
	}
	if view == managersView {
		m.healthForcePending = true
		return m.loadManagers()
	}
	s := &m.states[view]
	if s.cancel != nil {
		s.cancel()
	}
	s.generation++
	s.attempted = true
	s.needsReload = false
	if view == discoverView && strings.TrimSpace(s.query) == "" {
		s.loading = false
		return m.ensureInventory(false)
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.loading, s.stale, s.err = true, s.loaded, nil
	generation, query := s.generation, s.query
	s.scopeKey = m.scopeKey()
	if view == installedView {
		if cached := m.inventories[s.scopeKey]; cached != nil && cached.loading {
			if cached.cancel != nil {
				cached.cancel()
			}
			cached.generation++
			cached.loading = false
		}
	}
	service := m.service
	if view == diagnosticsView {
		name := m.diagnosticName
		return func() tea.Msg {
			report, err := service.Diagnose(ctx, name)
			return diagnosticsMsg{generation, report, err}
		}
	}
	kind := "installed"
	if view == discoverView {
		kind = "search"
	} else {
		query = ""
	}
	if view == updatesView {
		kind = "outdated"
	}
	request := domain.PackageQuery{Kind: kind, Query: query, Managers: m.effectiveManagers(), DeferInventory: view == discoverView, Refresh: s.force, CachePolicy: domain.CacheSession}
	s.force = false
	m.beginViewStream(view)
	command := m.startStream(ctx, request, streamTarget{view: view, generation: generation, query: query})
	if view == discoverView {
		return tea.Batch(command, m.ensureInventory(false))
	}
	return command
}

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := message.(tea.MouseMsg); ok {
		return m, m.mouseMessage(message)
	}
	switch message.(type) {
	case tea.WindowSizeMsg, tea.KeyPressMsg, tea.PasteMsg, managersMsg, packagesMsg, diagnosticsMsg, setupMsg, planMsg, executedMsg, preferencesMsg, inventoryMsg, healthMsg, savedSetMsg, streamStartedMsg, streamEventMsg, conflictMsg, maintenanceMsg, promptMsg, promptIOResultMsg, batchPlanMsg, batchExecutedMsg, batchRefreshMsg:
		m.invalidateMouse()
	}
	switch msg := message.(type) {
	case batchRefreshMsg:
		return m, m.acceptBatchRefresh(msg)
	case batchPlanMsg:
		m.acceptBatchPlan(msg)
		return m, nil
	case batchExecutedMsg:
		return m, m.acceptBatchExecuted(msg)
	case streamStartedMsg:
		return m, m.acceptStreamStart(msg)
	case streamEventMsg:
		return m, m.acceptStreamEvent(msg)
	case conflictMsg:
		m.acceptConflict(msg)
		return m, nil
	case maintenanceMsg:
		m.acceptMaintenance(msg)
		return m, nil
	case promptMsg:
		m.acceptPrompt(msg)
		return m, nil
	case promptIOResultMsg:
		m.acceptPromptIO(msg)
		return m, nil
	case preferencesMsg:
		return m, m.acceptPreferences(msg)
	case inventoryMsg:
		m.acceptInventory(msg)
		return m, m.installedProgress(domain.QueryEvent{}, true)
	case healthMsg:
		m.acceptHealth(msg)
		return m, nil
	case savedSetMsg:
		m.acceptSavedSet(msg)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
		m.input.SetWidth(max(1, m.width-8))
		m.reconcile(m.view, false)
		return m, nil
	case managersMsg:
		if msg.generation != m.managersGeneration {
			return m, nil
		}
		m.managersLoading = false
		m.managersErr = msg.err
		if msg.err == nil {
			cursorID := m.managerCursorID()
			previous := m.managers
			m.managers, m.managersLoaded = append([]domain.Manager(nil), msg.managers...), true
			for i := range m.managers {
				for _, old := range previous {
					if old.ID == m.managers[i].ID && old.Path == m.managers[i].Path && old.Version == m.managers[i].Version && m.managers[i].Health == nil && old.Health != nil {
						health := *old.Health
						m.managers[i].Health = &health
					}
				}
			}
			m.managerCursor = 0
			for i, manager := range m.sidebarManagers() {
				if manager.ID == cursorID {
					m.managerCursor = i + 1
				}
			}
		}
		m.reconcile(managersView, false)
		if m.view == managersView {
			force := m.healthForcePending
			m.healthForcePending = false
			return m, m.checkManagerHealth(force)
		}
		return m, nil
	case packagesMsg:
		s := &m.states[msg.view]
		if msg.generation != s.generation {
			return m, nil
		}
		s.loading, s.err, s.streaming = false, msg.err, false
		if msg.err == nil {
			snapshot := domain.CloneSnapshot(msg.snapshot)
			s.retainedManagers = nil
			if msg.view != discoverView {
				snapshot, s.retainedManagers = retainFailedProviders(s.snapshot, snapshot)
			}
			s.snapshot, s.acceptedQuery, s.loaded, s.stale = snapshot, msg.query, true, false
			if msg.view == discoverView {
				for i := range snapshot.Packages {
					snapshot.Packages[i].Candidate = true
				}
				s.candidates = domain.CloneSnapshot(snapshot)
				m.attachDiscover()
			}
		} else {
			s.stale = s.loaded
		}
		m.reconcile(msg.view, false)
		if msg.view == installedView {
			m.cacheInstalled(s)
			return m, m.installedProgress(domain.QueryEvent{}, true)
		}
		return m, nil
	case diagnosticsMsg:
		s := &m.states[diagnosticsView]
		if msg.generation != s.generation {
			return m, nil
		}
		s.loading, s.err = false, msg.err
		if msg.err == nil {
			s.report, s.loaded, s.stale = msg.report, true, false
		} else {
			s.stale = s.loaded
		}
		m.reconcile(diagnosticsView, false)
		return m, nil
	case setupMsg:
		if msg.generation != m.setup.generation || m.modal != setupModal {
			return m, nil
		}
		m.setup.loading, m.setup.err = false, msg.err
		if msg.err == nil {
			if !m.setup.loaded {
				for _, option := range msg.options {
					if option.Recommended && !option.Installed {
						m.setup.selected[option.ID] = true
					}
				}
			}
			m.setup.options, m.setup.loaded = msg.options, true
			for _, option := range msg.options {
				if option.Installed {
					delete(m.setup.selected, option.ID)
				}
			}
			m.setup.position = clamp(m.setup.position, 0, len(msg.options)-1)
		}
		return m, nil
	case planMsg:
		if msg.generation != m.planGeneration || m.modal != planModal {
			return m, nil
		}
		m.planLoading, m.plan, m.planErr = false, msg.plan, msg.err
		return m, nil
	case executedMsg:
		if msg.generation != m.planGeneration {
			return m, nil
		}
		m.executing = false
		m.modal = noModal
		m.lastResult, m.lastError = msg.result, msg.err
		m.lastOperation = "single"
		m.status = "Completed. Press v to view the result."
		if msg.err != nil {
			m.status = "Operation failed or stopped; review its result with v."
		}
		m.invalidateViews()
		m.setup.loaded = false
		if cmd, handled := m.afterWorkflowExecution(msg); handled {
			return m, cmd
		}
		return m, m.refreshAfterMutation(m.view)
	case tea.KeyPressMsg:
		if m.executing {
			return m, nil
		}
		if msg.String() == "M" && !m.filtering && m.modal != versionModal && m.modal != saveSetModal && m.modal != exportPromptModal {
			return m, m.navigationKey(msg)
		}
		if m.modal != noModal {
			return m, m.modalKey(msg)
		}
		if m.filtering {
			return m, m.inputMessage(msg)
		}
		return m, m.navigationKey(msg)
	case tea.PasteMsg:
		// Bracketed paste never falls through into navigation or approval.
		if m.filtering || m.modal == versionModal || m.modal == saveSetModal || m.modal == exportPromptModal {
			return m, m.inputMessage(msg)
		}
		return m, nil
	default:
		if m.filtering || m.modal == versionModal || m.modal == saveSetModal || m.modal == exportPromptModal {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(message)
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) inputMessage(message tea.Msg) tea.Cmd {
	if m.modal == exportPromptModal {
		return m.exportInput(message)
	}
	if m.modal == saveSetModal {
		return m.saveSetInput(message)
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.input.Blur()
			if m.modal == versionModal {
				m.modal = noModal
				return nil
			}
			m.filtering = false
			m.states[m.view].query = ""
			if m.view == discoverView {
				m.clearSearch()
			}
			m.reconcile(m.view, true)
			return nil
		case "enter":
			if m.modal == versionModal {
				version := strings.TrimSpace(m.input.Value())
				if version == "" {
					m.status = "Enter a version or latest; Esc cancels."
					return nil
				}
				request := m.versionRequest
				request.Version = version
				m.input.Blur()
				return m.startPlan(request)
			}
			m.states[m.view].query = strings.TrimSpace(m.input.Value())
			m.filtering = false
			m.input.Blur()
			if m.view == discoverView {
				if m.states[m.view].query == "" {
					m.clearSearch()
					return nil
				}
				return m.loadView(m.view)
			}
			return nil
		case "up", "down":
			if m.modal != versionModal {
				if key.String() == "up" {
					m.move(-1)
				} else {
					m.move(1)
				}
				return nil
			}
		}
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	if m.filtering && m.view != discoverView && before != m.input.Value() {
		m.states[m.view].query = m.input.Value()
		m.reconcile(m.view, true)
	}
	return cmd
}

func (m *Model) navigationKey(key tea.KeyPressMsg) tea.Cmd {
	name := key.String()
	if m.pageKey(name) {
		m.pendingG = false
		return nil
	}
	if name != "g" {
		m.pendingG = false
	}
	switch name {
	case "R":
		return m.startConflict()
	case "U":
		if packageView(m.view) {
			return m.startBatchUpgrade("visible")
		}
		if m.view == managersView {
			return m.openMaintenance()
		}
		return nil
	case "u":
		if packageView(m.view) && !m.managerFocus && len(m.states[m.view].marks) > 0 {
			return m.startBatchUpgrade("selected")
		}
		if m.view == managersView {
			return m.requestAction("manager-update")
		}
		return m.requestAction("upgrade")
	case "space", " ":
		if row, ok := m.selectedRow(); ok {
			m.toggleMark(row.key)
		}
		return nil
	case "ctrl+a":
		m.toggleVisibleMarks()
		return nil
	case "p":
		return m.openPrompt()
	case "M":
		m.mouseEnabled = !m.mouseEnabled
		m.mouseOverride = true
		m.status = fmt.Sprintf("Mouse %s. M toggles terminal capture.", map[bool]string{true: "enabled", false: "disabled"}[m.mouseEnabled])
		return nil
	case "f":
		return m.openProviders()
	case "b":
		if m.view == managersView {
			m.showAllManagers = !m.showAllManagers
			m.managerCursor = 0
			m.reconcile(managersView, true)
		}
		return nil
	case "q", "ctrl+c":
		m.quitting = true
		m.cancelAll()
		return tea.Quit
	case "tab", "shift+tab":
		m.managerFocus = !m.managerFocus
		return nil
	case "left", "h":
		return m.switchView((int(m.view) + 4) % 5)
	case "right", "l":
		return m.switchView((int(m.view) + 1) % 5)
	case "1", "2", "3", "4", "5":
		return m.switchView(int(name[0] - '1'))
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "home":
		m.move(-1 << 30)
	case "end", "G":
		m.move(1 << 30)
	case "g":
		if m.pendingG {
			m.move(-1 << 30)
			m.pendingG = false
		} else {
			m.pendingG = true
		}
	case "/":
		m.managerFocus = false
		m.filtering = true
		m.input.SetValue(m.states[m.view].query)
		m.input.Prompt = "/ "
		m.input.Placeholder = "Filter visible items"
		if m.view == discoverView {
			m.input.Placeholder = "Search packages, then Enter"
		}
		return m.input.Focus()
	case "esc":
		if m.states[m.view].loading {
			m.cancelView(m.view)
			m.status = "Read cancelled; retained results are stale."
			return nil
		}
		if m.states[m.view].query != "" {
			m.states[m.view].query = ""
			if m.view == discoverView {
				m.clearSearch()
			}
			m.reconcile(m.view, true)
			return nil
		}
		if m.view == diagnosticsView && m.diagnosticName != "" {
			m.diagnosticName = ""
			return m.loadView(diagnosticsView)
		}
		return m.applyScope(m.preferences.Default, "Default")
	case "enter":
		if m.managerFocus {
			return m.applyManagerFilter()
		}
		if row, ok := m.selectedRow(); ok {
			m.modal = detailsModal
			m.detailKey = row.key
			m.modalOffset = 0
		}
	case "r":
		m.states[m.view].force = true
		if m.view == discoverView {
			inventory := m.ensureInventory(true)
			return tea.Batch(m.loadView(m.view), inventory)
		}
		return m.loadView(m.view)
	case "s":
		m.modal = setupModal
		m.modalOffset = 0
		return m.loadSetup()
	case "?":
		m.modal = helpModal
		m.modalOffset = 0
	case "e":
		m.modal = issuesModal
		m.modalOffset = 0
	case "v":
		if m.lastOperation == "batch" && m.lastBatch != nil {
			m.reopenBatch()
			return nil
		}
		m.modal = resultModal
		m.modalOffset = 0
	default:
		for _, action := range m.actions() {
			if name == action.key {
				return m.requestAction(action.operation)
			}
		}
	}
	return nil
}

func (m *Model) switchView(index int) tea.Cmd {
	m.status = ""
	m.filtering = false
	m.input.Blur()
	m.view = viewID(index)
	m.pendingG = false
	m.reconcile(m.view, false)
	if m.view == managersView {
		if !m.managersLoaded && !m.managersLoading {
			return m.loadManagers()
		}
		return m.checkManagerHealth(false)
	}
	s := &m.states[m.view]
	if !s.loading && ((!s.attempted && !s.loaded) || s.needsReload) {
		return m.loadView(m.view)
	}
	if m.view == discoverView {
		return m.ensureInventory(false)
	}
	return nil
}

func (m *Model) clearSearch() {
	m.cancelView(discoverView)
	s := &m.states[discoverView]
	s.snapshot = domain.Snapshot{}
	s.query = ""
	s.acceptedQuery = ""
	s.loaded = false
	s.stale = false
	s.err = nil
	m.reconcile(discoverView, true)
}

func (m *Model) applyManagerFilter() tea.Cmd {
	id := m.managerCursorID()
	m.managerFocus = false
	if id == "" {
		return m.applyScope(m.preferences.Default, "Default")
	}
	return m.applyScope([]string{id}, id)
}

func (m *Model) managerCursorID() string {
	managers := m.sidebarManagers()
	if m.managerCursor > 0 && m.managerCursor <= len(managers) {
		return managers[m.managerCursor-1].ID
	}
	return ""
}

func (m *Model) move(delta int) {
	m.status = ""
	if m.managerFocus {
		m.managerCursor = clamp(m.managerCursor+delta, 0, len(m.sidebarManagers()))
		return
	}
	s := &m.states[m.view]
	m.detailOffset = 0
	rows := m.rows(m.view)
	s.position = clamp(s.position+delta, 0, len(rows)-1)
	if len(rows) > 0 {
		s.selected = rows[s.position].key
	} else {
		s.selected = ""
	}
	m.ensureVisible(s)
}

func (m *Model) reconcile(view viewID, reset bool) {
	m.reconcileMarks(view)
	s := &m.states[view]
	rows := m.rows(view)
	if reset {
		s.selected = ""
		s.position = 0
		s.offset = 0
	}
	if s.selected != "" {
		for i, row := range rows {
			if row.key == s.selected {
				s.position = i
				break
			}
		}
	}
	s.position = clamp(s.position, 0, len(rows)-1)
	if len(rows) > 0 {
		s.selected = rows[s.position].key
	} else {
		s.selected = ""
	}
	m.ensureVisible(s)
}

func (m *Model) pageSize() int { return max(1, m.height-11) }
func (m *Model) ensureVisible(s *viewState) {
	if s.position < s.offset {
		s.offset = s.position
	}
	if s.position >= s.offset+m.pageSize() {
		s.offset = s.position - m.pageSize() + 1
	}
	s.offset = max(0, s.offset)
}

func (m *Model) cancelView(view viewID) {
	s := &m.states[view]
	if view == installedView && s.loading {
		if cached := m.inventories[s.scopeKey]; cached != nil && cached.loading {
			cached.loading = false
			cached.attempted = true
			cached.err = context.Canceled
			cached.stale = cached.loaded
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.generation++
	s.streaming = false
	s.loading = false
	s.stale = s.loaded
}

func (m *Model) cancelAll() {
	if m.batch.cancel != nil {
		m.batch.cancel()
	}
	m.batch.generation++
	m.cancelWorkflow()
	for i := range m.states {
		m.cancelView(viewID(i))
	}
	if m.managersCancel != nil {
		m.managersCancel()
	}
	m.managersGeneration++
	if m.setup.cancel != nil {
		m.setup.cancel()
	}
	m.setup.generation++
	if m.planCancel != nil {
		m.planCancel()
	}
	m.planGeneration++
	if m.healthCancel != nil {
		m.healthCancel()
	}
	m.healthGeneration++
	if m.prefsCancel != nil {
		m.prefsCancel()
	}
	m.prefsGeneration++
	for _, inventory := range m.inventories {
		if inventory.cancel != nil {
			inventory.cancel()
		}
		inventory.generation++
	}
}

type row struct {
	key         string
	label       string
	secondary   string
	manager     string
	pkg         *domain.Package
	managerInfo *domain.Manager
	finding     *domain.Finding
	executable  *domain.Executable
}

func (m *Model) rows(view viewID) []row {
	s := &m.states[view]
	var rows []row
	switch view {
	case managersView:
		for i := range m.managers {
			manager := &m.managers[i]
			if !m.showAllManagers && !manager.Available && manager.Path == "" {
				continue
			}
			status := manager.Status
			if manager.Health != nil {
				status += " · " + manager.Health.UpdateStatus
			}
			rows = append(rows, row{key: manager.ID, label: manager.Name, secondary: status, manager: manager.ID, managerInfo: manager})
		}
	case diagnosticsView:
		report := s.report
		if m.scopeChanged {
			report = domain.FilterDiagnostics(report, m.effectiveManagers())
		}
		for i := range report.Findings {
			f := &report.Findings[i]
			rows = append(rows, row{key: fmt.Sprintf("finding:%s:%s:%s", f.Kind, f.Name, strings.Join(f.Paths, "|")), label: f.Name, secondary: f.Kind, finding: f})
		}
		for i := range report.Executables {
			e := &report.Executables[i]
			status := "shadowed"
			if e.PathIndex < 0 {
				status = "outside PATH"
			}
			if e.Preferred {
				status = "effective"
			}
			if e.EquivalentTo != "" {
				status = "same target"
			}
			rows = append(rows, row{key: "exe:" + e.Path, label: e.Name, secondary: status, manager: e.Manager, executable: e})
		}
	default:
		for i := range s.snapshot.Packages {
			p := &s.snapshot.Packages[i]
			version := p.Version
			if view == discoverView {
				version = p.Latest
				if version == "" {
					version = "version not reported"
				}
				if p.InstallState == "installed" {
					version = "installed · " + version
				} else if p.InstallState == "checking" {
					version += " · checking"
				}
			}
			if version == "" {
				version = "?"
			}
			if view == updatesView {
				version += " → " + orUnknown(p.Latest)
			}
			if s.retainedManagers[p.Manager] || p.InventoryStale {
				version += " (stale)"
			}
			rows = append(rows, row{key: p.Key(), label: p.ID, secondary: version, manager: p.Manager, pkg: p})
		}
	}
	query := strings.ToLower(strings.TrimSpace(s.query))
	return slices.DeleteFunc(rows, func(r row) bool {
		if len(m.effectiveManagers()) > 0 && view != managersView && view != diagnosticsView && !m.includesManager(r.manager) {
			// Findings concern several providers; retain them when their paths cannot be attributed.
			if r.finding == nil {
				return true
			}
		}
		if query == "" || view == discoverView {
			return false
		}
		haystack := strings.Join([]string{r.label, r.secondary, r.manager}, " ")
		if r.pkg != nil {
			haystack += " " + r.pkg.Name + " " + r.pkg.Description + " " + strings.Join(r.pkg.Commands, " ")
		}
		if r.executable != nil {
			haystack += " " + r.executable.Path + " " + r.executable.Target
		}
		if r.finding != nil {
			haystack += " " + r.finding.Message
		}
		return !strings.Contains(strings.ToLower(haystack), query)
	})
}

func (m *Model) selectedRow() (row, bool) {
	if m.managerFocus {
		return row{}, false
	}
	selected := m.states[m.view].selected
	if m.modal == detailsModal {
		selected = m.detailKey
	}
	if selected == "" {
		return row{}, false
	}
	for _, r := range m.rows(m.view) {
		if r.key == selected {
			return r, true
		}
	}
	return row{}, false
}

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	return min(max(value, low), high)
}
func orUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
func observed(t time.Time) string {
	if t.IsZero() {
		return "not observed"
	}
	return t.Local().Format("15:04:05")
}

// A partial scan is not proof that a failed provider uninstalled its packages.
// Preserve its previous group only when the new response has no group at all.
func retainFailedProviders(previous, fresh domain.Snapshot) (domain.Snapshot, map[string]bool) {
	present := make(map[string]bool)
	for _, p := range fresh.Packages {
		present[p.Manager] = true
	}
	failed := make(map[string]bool)
	for _, issue := range fresh.Issues {
		if issue.Manager != "" && !present[issue.Manager] {
			failed[issue.Manager] = true
		}
	}
	for _, coverage := range fresh.Coverage {
		failed[coverage.Manager] = coverage.State == "failed" && !present[coverage.Manager]
	}
	retained := make(map[string]bool)
	for _, p := range previous.Packages {
		if failed[p.Manager] && !providerInstanceChanged(previous, fresh, p.Manager) {
			p.InventoryStale = true
			fresh.Packages = append(fresh.Packages, p)
			retained[p.Manager] = true
		}
	}
	return fresh, retained
}

func providerInstanceChanged(previous, fresh domain.Snapshot, id string) bool {
	instance := func(s domain.Snapshot) string {
		for _, c := range s.Coverage {
			if c.Manager == id && c.Instance != "" {
				return c.Instance
			}
		}
		for _, p := range s.Packages {
			if p.Manager == id && p.Instance != "" {
				return p.Instance
			}
		}
		return ""
	}
	old, next := instance(previous), instance(fresh)
	return old != "" && next != "" && old != next
}
