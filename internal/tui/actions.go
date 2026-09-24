package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type binding struct{ key, label, operation string }

// actions is shared by dispatch and help so unavailable actions are never advertised.
func (m *Model) actions() []binding {
	if m.filtering || m.executing || m.width < 30 || m.height < 10 {
		return nil
	}
	if m.modal != noModal && m.modal != detailsModal {
		return nil
	}
	r, ok := m.selectedRow()
	if !ok || r.pkg == nil {
		return nil
	}
	p := r.pkg
	var out []binding
	if len(p.Commands) > 0 {
		out = append(out, binding{"d", "diagnose command", "diagnose"})
	}
	s := &m.states[m.view]
	if s.loading || s.stale || s.err != nil || s.retainedManagers[p.Manager] {
		return out
	}
	if m.view == discoverView && s.acceptedQuery != s.query {
		return out
	}
	var manager *domain.Manager
	for i := range m.managers {
		if m.managers[i].ID == p.Manager {
			manager = &m.managers[i]
			break
		}
	}
	if manager == nil || !manager.Available {
		return out
	}
	if m.view == discoverView {
		if manager.Supports("install") {
			out = append(out, binding{"i", "install", "install"})
		}
		return out
	}
	if m.view != installedView && m.view != updatesView {
		return nil
	}
	if manager.Supports("upgrade") && !m.pendingActivation(p) {
		out = append(out, binding{"u", "upgrade", "upgrade"})
	}
	if manager.Supports("remove") {
		out = append(out, binding{"x", "remove", "remove"})
	}
	if p.Manager == "mise" && m.activationVersion(p) != "" && manager.Supports("activate") {
		label := "activate globally"
		if m.pendingActivation(p) {
			label = "activate " + p.Latest + " globally"
		}
		out = append(out, binding{"a", label, "activate"})
	}
	return out
}

func (m *Model) requestAction(operation string) tea.Cmd {
	eligible := false
	for _, action := range m.actions() {
		if action.operation == operation {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil
	}
	r, ok := m.selectedRow()
	if !ok || r.pkg == nil {
		return nil
	}
	p := r.pkg
	if operation == "diagnose" {
		m.diagnosticName = p.Commands[0]
		m.managerFilter, m.managerCursor = "", 0
		m.view, m.modal, m.managerFocus = diagnosticsView, noModal, false
		m.states[diagnosticsView].query = ""
		m.reconcile(diagnosticsView, true)
		return m.loadView(diagnosticsView)
	}
	request := domain.ActionRequest{Operation: operation, Manager: p.Manager, Package: p.ID}
	if p.Manager == "mise" {
		switch operation {
		case "install":
			m.versionRequest = request
			m.modal = versionModal
			m.modalOffset = 0
			m.input.Prompt = "Version: "
			m.input.Placeholder = "Exact version or latest (resolved before review)"
			m.input.SetValue(p.Latest)
			if p.Latest == "" {
				m.input.SetValue("latest")
			}
			return m.input.Focus()
		case "remove":
			request.Version = p.Version
		case "activate":
			request.Version = m.activationVersion(p)
		case "upgrade":
			request.Version = p.Latest
		}
	}
	return m.startPlan(request)
}

func (m *Model) pendingActivation(p *domain.Package) bool {
	return m.view == updatesView && p.Manager == "mise" && p.LatestInstalled && p.Latest != ""
}

func (m *Model) activationVersion(p *domain.Package) string {
	if m.pendingActivation(p) {
		return p.Latest
	}
	return p.Version
}

func (m *Model) startPlan(request domain.ActionRequest) tea.Cmd {
	if m.planCancel != nil {
		m.planCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.planCancel = cancel
	m.planGeneration++
	generation := m.planGeneration
	m.planReturn = noModal
	m.modal = planModal
	m.modalOffset = 0
	m.planLoading = true
	m.planErr = nil
	m.plan = domain.ActionPlan{}
	service := m.service
	return func() tea.Msg { plan, err := service.Plan(ctx, request); return planMsg{generation, plan, err} }
}

func (m *Model) loadSetup() tea.Cmd {
	if m.setup.cancel != nil {
		m.setup.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.setup.cancel = cancel
	m.setup.generation++
	m.setup.loading = true
	m.setup.err = nil
	generation, service := m.setup.generation, m.service
	return func() tea.Msg { options, err := service.SetupOptions(ctx); return setupMsg{generation, options, err} }
}

func (m *Model) reviewSetup() tea.Cmd {
	if m.setup.loading || m.setup.err != nil {
		return nil
	}
	var selected []string
	for _, option := range m.setup.options {
		if !option.Installed && m.setup.selected[option.ID] {
			selected = append(selected, option.ID)
		}
	}
	if len(selected) == 0 {
		m.status = "Choose at least one component with Space."
		return nil
	}
	if m.planCancel != nil {
		m.planCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.planCancel = cancel
	m.planGeneration++
	generation, service := m.planGeneration, m.service
	m.planReturn = setupModal
	m.modal = planModal
	m.modalOffset = 0
	m.planLoading = true
	m.planErr = nil
	m.plan = domain.ActionPlan{}
	return func() tea.Msg { plan, err := service.PlanSetup(ctx, selected); return planMsg{generation, plan, err} }
}

func (m *Model) modalKey(key tea.KeyPressMsg) tea.Cmd {
	name := key.String()
	if m.modal == versionModal {
		return m.inputMessage(key)
	}
	if name == "esc" || name == "ctrl+c" {
		m.closeModal()
		return nil
	}
	switch m.modal {
	case setupModal:
		switch name {
		case "up", "k":
			m.setup.position = clamp(m.setup.position-1, 0, len(m.setup.options)-1)
		case "down", "j":
			m.setup.position = clamp(m.setup.position+1, 0, len(m.setup.options)-1)
		case "home", "g":
			m.setup.position = 0
		case "end", "G":
			m.setup.position = max(0, len(m.setup.options)-1)
		case "space", " ":
			if !m.setup.loading && len(m.setup.options) > 0 {
				option := m.setup.options[m.setup.position]
				if !option.Installed {
					m.setup.selected[option.ID] = !m.setup.selected[option.ID]
				}
			}
		case "enter":
			return m.reviewSetup()
		case "r":
			return m.loadSetup()
		}
	case planModal:
		if name == "y" && !key.IsRepeat && !m.planLoading && m.planErr == nil && !m.executing && m.width >= 40 && m.height >= 10 {
			m.executing = true
			runner := &execution{ctx: m.ctx, service: m.service, plan: m.plan}
			generation := m.planGeneration
			return tea.Exec(runner, func(err error) tea.Msg { return executedMsg{generation, runner.result, err} })
		}
		// Enter deliberately never approves a plan. The footer always names y explicitly.
		if name == "enter" {
			m.status = "Press y to execute this reviewed plan, or Esc to cancel."
			return nil
		}
	case detailsModal:
		for _, action := range m.actions() {
			if name == action.key {
				return m.requestAction(action.operation)
			}
		}
		if name == "enter" {
			m.closeModal()
			return nil
		}
	case helpModal, issuesModal, resultModal:
		if name == "enter" || name == "q" {
			m.closeModal()
			return nil
		}
	}
	switch name {
	case "up", "k":
		m.modalOffset = max(0, m.modalOffset-1)
	case "down", "j":
		m.modalOffset++
	case "pgup":
		m.modalOffset = max(0, m.modalOffset-m.pageSize())
	case "pgdown":
		m.modalOffset += m.pageSize()
	case "home", "g":
		m.modalOffset = 0
	case "end", "G":
		m.modalOffset = 1 << 30
	}
	return nil
}

func (m *Model) closeModal() {
	if m.modal == setupModal {
		if m.setup.cancel != nil {
			m.setup.cancel()
		}
		m.setup.generation++
		m.setup.loading = false
	}
	if m.modal == planModal {
		if m.planCancel != nil {
			m.planCancel()
		}
		m.planGeneration++
		m.planLoading = false
		m.modal = m.planReturn
		m.modalOffset = 0
		return
	}
	m.modal = noModal
	m.modalOffset = 0
	m.input.Blur()
}

func (m *Model) helpText() string {
	lines := []string{
		"Browse", "↑/↓ or j/k   Select an item", "Tab/Shift+Tab   Move focus between managers and items",
		"←/→ or h/l   Change view; 1–5 jump directly", "Home/End or gg/G   First/last item",
		"Enter   Inspect selected item; Esc returns", "/   Filter; in Discover, enter a remote search",
		"While typing, letters remain text. Enter accepts the query; Esc clears it.", "r   Refresh the current view; Esc cancels a pending read",
		"", "Manage", "s   Set up the backend or additional managers", "e   Read errors and partial-coverage details", "v   View the last operation result",
		"i   Install a Discover result", "u   Upgrade an installed package when supported", "x   Review removal; a   Review mise global activation", "d   Diagnose a package's first recorded command across all providers",
		"Only applicable actions appear in the footer. Every change requires a plan and y to confirm.",
		"", "Scope", "Installed packages, recognized applications, and executables are different observations.",
		"mise activation is directory-dependent. Configured/installed does not prove a PATH winner.",
		"Diagnostics show the inherited process PATH. Shell aliases/functions are outside this scan.",
		"Missing managers and unsupported operations are excluded from search; e shows coverage issues.",
		"", "q or Ctrl+C   Quit from navigation; Esc cancels the nearest overlay first",
	}
	return strings.Join(lines, "\n")
}

func (m *Model) issuesText() string {
	var lines []string
	if m.managersErr != nil {
		lines = append(lines, "Manager discovery: "+m.managersErr.Error())
	}
	for _, manager := range m.managers {
		for _, err := range manager.Errors {
			lines = append(lines, manager.ID+": "+err)
		}
	}
	s := &m.states[m.view]
	if s.err != nil {
		lines = append(lines, viewNames[m.view]+": "+s.err.Error())
	}
	issues := s.snapshot.Issues
	if m.view == diagnosticsView {
		issues = s.report.Issues
	}
	for _, issue := range issues {
		lines = append(lines, strings.TrimSpace(issue.Manager+": "+issue.Message))
	}
	if len(lines) == 0 {
		lines = append(lines, "No errors reported for this view.")
	}
	lines = append(lines, "", "Press s from the dashboard to set up missing components.")
	return strings.Join(lines, "\n\n")
}

func resultText(result domain.ActionResult, err error) string {
	var lines []string
	if err != nil {
		lines = append(lines, "Error: "+err.Error())
	}
	if result.Message != "" {
		lines = append(lines, result.Message)
	}
	for _, step := range result.Steps {
		lines = append(lines, fmt.Sprintf("%s  %s  %s", step.Status, step.ID, step.Message))
	}
	if len(lines) == 0 {
		return "No operation has run in this session."
	}
	return strings.Join(lines, "\n")
}
