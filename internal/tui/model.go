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
	snapshot         domain.Snapshot
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
	stale            bool
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
func New(ctx context.Context, service domain.Service, initialView string) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	input := textinput.New()
	input.CharLimit = 512
	input.SetWidth(60)
	m := &Model{ctx: ctx, service: service, width: 80, height: 24, input: input}
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
	case "setup":
		m.view = managersView
		m.startSetup = true
	}
	return m
}

// Run owns the terminal until the dashboard is closed.
func Run(ctx context.Context, service domain.Service, initialView string) error {
	m := New(ctx, service, initialView)
	defer m.cancelAll()
	_, err := tea.NewProgram(m, tea.WithContext(m.ctx)).Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.loadManagers()}
	if m.startSetup {
		m.modal = setupModal
		commands = append(commands, m.loadSetup())
	} else if m.view != managersView {
		commands = append(commands, m.loadView(m.view))
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
	if view == managersView {
		return m.loadManagers()
	}
	s := &m.states[view]
	if s.cancel != nil {
		s.cancel()
	}
	s.generation++
	if view == discoverView && strings.TrimSpace(s.query) == "" {
		s.loading = false
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.loading, s.stale, s.err = true, s.loaded, nil
	generation, query, manager := s.generation, s.query, m.managerFilter
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
		query, manager = "", ""
	}
	if view == updatesView {
		kind = "outdated"
	}
	return func() tea.Msg {
		snapshot, err := service.Packages(ctx, kind, query, manager)
		return packagesMsg{view, generation, query, snapshot, err}
	}
}

func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
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
			m.managers, m.managersLoaded = msg.managers, true
			m.managerCursor = 0
			for i, manager := range m.managers {
				if manager.ID == cursorID {
					m.managerCursor = i + 1
				}
			}
		}
		m.reconcile(managersView, false)
		return m, nil
	case packagesMsg:
		s := &m.states[msg.view]
		if msg.generation != s.generation {
			return m, nil
		}
		s.loading, s.err = false, msg.err
		if msg.err == nil {
			snapshot := msg.snapshot
			s.retainedManagers = nil
			if msg.view != discoverView {
				snapshot, s.retainedManagers = retainFailedProviders(s.snapshot, snapshot)
			}
			s.snapshot, s.acceptedQuery, s.loaded, s.stale = snapshot, msg.query, true, false
		} else {
			s.stale = s.loaded
		}
		m.reconcile(msg.view, false)
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
		m.status = "Completed. Press v to view the result."
		if msg.err != nil {
			m.status = "Operation failed or stopped; review its result with v."
		}
		for i := range m.states {
			if m.states[i].loaded {
				m.states[i].stale = true
			}
		}
		m.setup.loaded = false
		commands := []tea.Cmd{m.loadManagers()}
		if m.view != managersView {
			commands = append(commands, m.loadView(m.view))
		}
		return m, tea.Batch(commands...)
	case tea.KeyPressMsg:
		if m.executing {
			return m, nil
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
		if m.filtering || m.modal == versionModal {
			return m, m.inputMessage(msg)
		}
		return m, nil
	default:
		if m.filtering || m.modal == versionModal {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(message)
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) inputMessage(message tea.Msg) tea.Cmd {
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
	if name != "g" {
		m.pendingG = false
	}
	switch name {
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
	case "pgup":
		m.move(-m.pageSize())
	case "pgdown":
		m.move(m.pageSize())
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
		m.managerFilter = ""
		m.managerCursor = 0
		m.reconcile(m.view, true)
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
	m.view = viewID(index)
	m.pendingG = false
	m.reconcile(m.view, false)
	if m.view == managersView {
		if !m.managersLoaded && !m.managersLoading {
			return m.loadManagers()
		}
		return nil
	}
	s := &m.states[m.view]
	if !s.loading && (!s.loaded || s.stale) {
		return m.loadView(m.view)
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
	m.managerFilter = m.managerCursorID()
	m.managerFocus = false
	// Search results belong to the provider selection that produced them, even
	// if the user changes that selection from another view while search runs.
	m.cancelView(discoverView)
	for i := range m.states {
		m.reconcile(viewID(i), true)
	}
	if m.view == discoverView && m.states[m.view].query != "" {
		return m.loadView(m.view)
	}
	return nil
}

func (m *Model) managerCursorID() string {
	if m.managerCursor > 0 && m.managerCursor <= len(m.managers) {
		return m.managers[m.managerCursor-1].ID
	}
	return ""
}

func (m *Model) move(delta int) {
	if m.managerFocus {
		m.managerCursor = clamp(m.managerCursor+delta, 0, len(m.managers))
		return
	}
	s := &m.states[m.view]
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
	if s.cancel != nil {
		s.cancel()
	}
	s.generation++
	s.loading = false
	s.stale = s.loaded
}

func (m *Model) cancelAll() {
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
			rows = append(rows, row{key: manager.ID, label: manager.Name, secondary: manager.Status, manager: manager.ID, managerInfo: manager})
		}
	case diagnosticsView:
		for i := range s.report.Findings {
			f := &s.report.Findings[i]
			rows = append(rows, row{key: fmt.Sprintf("finding:%s:%s:%s", f.Kind, f.Name, strings.Join(f.Paths, "|")), label: f.Name, secondary: f.Kind, finding: f})
		}
		for i := range s.report.Executables {
			e := &s.report.Executables[i]
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
			}
			if version == "" {
				version = "?"
			}
			if view == updatesView {
				version += " → " + orUnknown(p.Latest)
			}
			if s.retainedManagers[p.Manager] {
				version += " (stale)"
			}
			rows = append(rows, row{key: p.Key(), label: p.ID, secondary: version, manager: p.Manager, pkg: p})
		}
	}
	query := strings.ToLower(strings.TrimSpace(s.query))
	return slices.DeleteFunc(rows, func(r row) bool {
		if m.managerFilter != "" && view != managersView && r.manager != m.managerFilter {
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
	retained := make(map[string]bool)
	for _, p := range previous.Packages {
		if failed[p.Manager] {
			fresh.Packages = append(fresh.Packages, p)
			retained[p.Manager] = true
		}
	}
	return fresh, retained
}
