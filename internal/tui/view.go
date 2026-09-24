package tui

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

var (
	accent        = lipgloss.NewStyle().Foreground(lipgloss.Color("#7AC7E3")).Bold(true)
	muted         = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B96A6"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E7F4FA")).Background(lipgloss.Color("#263D4B"))
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E8B86D"))
)

func (m *Model) View() tea.View {
	var content string
	if m.quitting {
		return tea.NewView("")
	}
	if m.width < 30 || m.height < 8 {
		content = "lazypkg · " + viewNames[m.view] + "\nResize for package actions.\nq quit · Esc back"
	} else if m.modal != noModal {
		content = m.modalView()
	} else {
		content = m.dashboardView()
	}
	content = fitScreen(content, m.width, m.height)
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		content = ansi.Strip(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "lazypkg"
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *Model) dashboardView() string {
	header := accent.Render(" lazypkg") + muted.Render("  Packages, providers, and the command you actually run")
	tabs := m.tabs()
	context := m.contextLine()
	bodyHeight := max(3, m.height-6)
	layout := m.layout()
	var body string
	if m.width >= 110 {
		leftWidth, rightWidth := layout.managers.w, layout.details.w
		middleWidth := layout.items.w
		left := m.managerPane(leftWidth, bodyHeight)
		middle := m.packagePane(middleWidth, bodyHeight)
		details := "Select an item to inspect its source and paths.\n\nEnter opens a scrollable detail view."
		if r, ok := m.selectedRow(); ok {
			details = m.rowDetails(r)
		}
		detailLines := wrapLines(details, rightWidth-4)
		detailStart := clamp(m.detailOffset, 0, max(0, len(detailLines)-(bodyHeight-3)))
		right := pane("Details", detailLines[detailStart:], rightWidth, bodyHeight, false)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, middle, right)
	} else if m.width >= 70 {
		leftWidth := layout.managers.w
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.managerPane(leftWidth, bodyHeight), m.packagePane(m.width-leftWidth, bodyHeight))
	} else if m.managerFocus {
		body = m.managerPane(m.width, bodyHeight)
	} else {
		body = m.packagePane(m.width, bodyHeight)
	}
	status := m.statusLine()
	footer, secondary := m.footer()
	return strings.Join([]string{line(header, m.width), line(tabs, m.width), line(context, m.width), body, line(status, m.width), line(footer, m.width), line(secondary, m.width)}, "\n")
}

func (m *Model) tabs() string {
	if m.width < 70 {
		return line(accent.Render(fmt.Sprintf(" ‹ %d/5 %s · h/l views", m.view+1, viewNames[m.view])), m.width-3) + " › "
	}
	var tabs []string
	for i, name := range viewNames {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		if viewID(i) == m.view {
			label = selectedStyle.Bold(true).Render(label)
		} else {
			label = muted.Render(label)
		}
		tabs = append(tabs, label)
	}
	return strings.Join(tabs, " ")
}

func (m *Model) contextLine() string {
	manager := providerLabel(m.scopeName)
	if manager == "" {
		manager = "Configured default"
	}
	if ids := m.effectiveManagers(); len(ids) > 0 {
		if len(ids) > 3 {
			manager += fmt.Sprintf(" · %d providers", len(ids))
		} else if len(ids) != 1 || manager != providerLabel(ids[0]) {
			labels := append([]string(nil), ids...)
			for i := range labels {
				labels[i] = providerLabel(labels[i])
			}
			manager += " · " + strings.Join(labels, ", ")
		}
	}
	manager = ansi.Truncate(clean(manager), max(15, m.width/2), "…")
	s := &m.states[m.view]
	state := "ready"
	if m.view == managersView {
		if m.managersLoading {
			state = "detecting…"
		} else if m.managersErr != nil {
			state = "discovery failed · s setup / e errors"
		} else if m.healthLoading {
			state = "checking manager updates…"
		} else if m.healthErr != nil {
			state = "update check incomplete · e details"
		} else if m.showAllManagers {
			state = "all catalog · b detected only"
		} else {
			state = "detected managers · b all catalog"
		}
	} else {
		switch {
		case s.loading && s.streaming:
			state = "loading · " + m.streamStatus(s)
		case s.loading && s.loaded:
			state = "refreshing · previous results"
		case s.loading:
			state = "loading…"
		case s.err != nil:
			state = "read failed · e details / s setup"
		case s.stale:
			state = "stale · r refresh"
		case len(s.retainedManagers) > 0:
			state = fmt.Sprintf("partial · %d stale provider(s) · e details", len(s.retainedManagers))
		case !s.loaded:
			state = "not queried"
		default:
			state = "observed " + observed(s.snapshot.ObservedAt)
		}
		if m.view == diagnosticsView && s.loaded && !s.loading && s.err == nil {
			state = orUnknown(s.report.Scope)
			if m.scopeChanged {
				state += " · full PATH peers retained"
			}
		}
		if m.view == diagnosticsView && m.diagnosticName != "" {
			state += " · command: " + m.diagnosticName
		}
	}
	if m.filtering {
		return m.input.View()
	}
	query := ""
	if s.query != "" {
		query = " · / " + clean(s.query)
	}
	marks := ""
	if total, hidden := m.markCounts(m.view); total > 0 {
		marks = fmt.Sprintf(" · %d selected (%d hidden)", total, hidden)
	}
	return " " + manager + marks + " · " + state + query
}

func (m *Model) managerPane(width, height int) string {
	var lines []string
	add := func(index int, label string) {
		marker := "  "
		if m.managerFocus && m.managerCursor == index {
			marker = "> "
		}
		value := line(marker+label, width-2)
		if m.managerFocus && m.managerCursor == index {
			value = selectedStyle.Render(value)
		}
		lines = append(lines, value)
	}
	all := "Configured default"
	if !m.scopeChanged || m.scopeName == "Default" {
		all += " *"
	}
	items := []string{all}
	for _, manager := range m.sidebarManagers() {
		mark := "○"
		if manager.Available {
			mark = "✓"
		} else if manager.Path != "" {
			mark = "!"
		}
		label := mark + " " + providerLabel(manager.ID)
		if m.includesManager(manager.ID) {
			label += " *"
		}
		items = append(items, label)
	}
	visible := max(1, height-4)
	start := clamp(m.managerCursor-visible+1, 0, max(0, len(items)-visible))
	for i := start; i < min(start+visible, len(items)); i++ {
		add(i, items[i])
	}
	if m.managersLoading {
		lines = append(lines, muted.Render("Detecting managers…"))
	}
	if m.managersErr != nil {
		lines = append(lines, warningStyle.Render("Discovery failed"), "s setup · e details")
	}
	if !m.managersLoading && len(m.managers) == 0 && m.managersErr == nil {
		lines = append(lines, "No providers found", "s setup")
	}
	return pane("Managers · f sets", lines, width, height, m.managerFocus)
}

func (m *Model) packagePane(width, height int) string {
	rows := m.rows(m.view)
	s := &m.states[m.view]
	var lines []string
	if len(rows) == 0 {
		lines = wrapLines(m.emptyText(), max(1, width-4))
	} else {
		heading := "Item"
		if m.view == diagnosticsView {
			heading = "Finding / executable"
		} else if m.view == managersView {
			heading = "Provider"
		}
		lines = append(lines, muted.Render(fmt.Sprintf("%s · %d", heading, len(rows))))
		count := max(1, height-5)
		start := clamp(s.offset, 0, max(0, len(rows)-count))
		for i := start; i < min(start+count, len(rows)); i++ {
			r := rows[i]
			marker := "  "
			if r.key == s.selected {
				marker = "> "
			}
			if packageView(m.view) && r.pkg != nil {
				check := "  "
				if _, marked := s.marks[r.key]; marked {
					check = "✓ "
				} else if ok, _ := m.upgradeEligibility(m.view, *r.pkg); !ok {
					check = "! "
				}
				marker += check
			}
			secondary := r.secondary
			if r.manager != "" && m.view != managersView {
				secondary = providerLabel(r.manager) + " · " + secondary
			}
			available := max(1, width-4)
			var value string
			if available >= 38 {
				right := min(25, available/2)
				value = marker + line(clean(r.label), available-right-1-ansi.StringWidth(marker)) + " " + line(clean(secondary), right)
			} else {
				value = marker + clean(r.label)
			}
			value = line(value, width-2)
			if r.key == s.selected && !m.managerFocus {
				value = selectedStyle.Render(value)
			}
			lines = append(lines, value)
		}
		if len(rows) > count {
			lines = append(lines, muted.Render(fmt.Sprintf("%d–%d of %d", start+1, min(start+count, len(rows)), len(rows))))
		}
	}
	return pane(viewNames[m.view], lines, width, height, !m.managerFocus)
}

func (m *Model) emptyText() string {
	s := &m.states[m.view]
	if m.view == managersView {
		if m.managersLoading {
			return "Detecting package managers…\n\nYou can switch views while this runs."
		}
		if m.managersErr != nil {
			return "Manager discovery could not finish.\n\nPress e for the error or s to set up the backend."
		}
		if !m.showAllManagers && len(m.managers) > 0 {
			return "No detected managers match this view.\n\nb shows the full catalog, including missing managers.\ns opens setup."
		}
		return "No managers match this filter.\n\nEsc clears the filter; s opens setup."
	}
	if s.loading {
		return "Loading…\n\nOther views and navigation remain available.\nEsc cancels this read."
	}
	if s.err != nil {
		return "This view could not be read.\n\n" + clean(s.err.Error()) + "\n\ne details · r retry · s setup"
	}
	if m.view == discoverView && s.query == "" {
		return "Find a package\n\nPress /, type a tool or package name, then Enter.\n\nChoose a result and press i to review installation.\n\nOnly available providers can be searched. uv tools uses exact PyPI names."
	}
	failed, excluded := m.queryCoverageCounts()
	if failed > 0 {
		return "No results in the completed portion of this scan.\n\nSome providers could not be queried; this is not a complete empty inventory.\n\ne shows coverage details; r retries."
	}
	if excluded > 0 && len(s.snapshot.Packages) == 0 {
		return "No packages were returned by the providers that support this query.\n\nSome selected providers are unavailable, unsupported or outside this scope.\ne explains coverage; f changes providers."
	}
	if s.query != "" || m.managerFilter != "" {
		return "No items match this query or manager.\n\nEsc clears the query/filter.\nChoose All managers to broaden the view."
	}
	if m.view == updatesView {
		return "No updates were reported by supported providers.\n\nThis does not include providers without update support.\ne shows query issues."
	}
	if m.view == diagnosticsView {
		return "No diagnostic entries were returned.\n\nPress r to scan again; e shows coverage details."
	}
	return "No installed packages were reported.\n\nDiscover can search for tools; s opens manager setup."
}

func (m *Model) footer() (string, string) {
	first, second := m.footerButtons()
	a, _ := buttonLine(first, m.width, m.height-2)
	b, _ := buttonLine(second, m.width, m.height-1)
	return a, muted.Render(b)
}

func (m *Model) statusLine() string {
	if m.status != "" {
		return " " + clean(m.status)
	}
	if r, ok := m.selectedRow(); ok && r.pkg != nil && packageView(m.view) {
		if ok, reason := m.upgradeEligibility(m.view, *r.pkg); !ok {
			return muted.Render(" ! " + reason)
		}
	}
	s := &m.states[m.view]
	failed, excluded := m.queryCoverageCounts()
	if failed > 0 {
		return warningStyle.Render(fmt.Sprintf(" Partial results · %d provider issue(s) · e details", failed))
	}
	if excluded > 0 {
		return muted.Render(fmt.Sprintf(" %d provider(s) excluded/unavailable · e coverage", excluded))
	}
	if s.err != nil || m.managersErr != nil {
		return warningStyle.Render(" Read failed · e details · s setup · r retry")
	}
	if m.pendingG {
		return " g… press g again for the first item"
	}
	if r, ok := m.selectedRow(); ok && r.pkg != nil {
		return muted.Render(" " + clean(providerLabel(r.pkg.Manager)+" / "+r.pkg.ID+" · "+orUnknown(r.pkg.Scope)))
	}
	return muted.Render(" Changes are reviewed before execution.")
}

func (m *Model) rowDetails(r row) string {
	var lines []string
	if p := r.pkg; p != nil {
		lines = append(lines, p.ID)
		if p.Name != "" && p.Name != p.ID {
			lines = append(lines, p.Name)
		}
		latest := p.Latest
		if latest == "" {
			latest = "not reported by provider"
		}
		lines = append(lines, "", "Provider: "+providerLabel(p.Manager), "Installed: "+installationLabel(p), "Available version: "+latest, "Scope: "+orUnknown(p.Scope))
		if !p.InventoryAt.IsZero() {
			lines = append(lines, "Inventory observed: "+observed(p.InventoryAt))
		}
		if p.InventoryStale {
			lines = append(lines, "", "Previous observation; native state will be rechecked before changes.")
		}
		if progress, ok := m.states[m.view].providers[p.Manager]; ok && progress.enrichment == "pending" {
			lines = append(lines, "Ownership details are still being collected; base inventory is ready.")
		}
		if m.states[m.view].retainedManagers[p.Manager] {
			lines = append(lines, "", "STALE: retained from the previous inventory because this provider's refresh failed. Refresh before changing it.")
		}
		if p.Manager == "gh-ext" {
			lines = append(lines, "", "Native command: gh extension", "Displayed version belongs to the extension; gh is its launcher.")
			if extension := p.Extension; extension != nil {
				lines = append(lines, "Extension kind: "+extension.Kind, "Update status: "+extension.Status, "Full local version: "+orUnknown(extension.FullVersion), "Launcher: "+orUnknown(extension.Launcher))
				if extension.Reason != "" {
					lines = append(lines, extension.Reason)
				}
				if extension.BlockedReason != "" {
					lines = append(lines, "Blocked: "+extension.BlockedReason)
				}
				if extension.Pinned {
					lines = append(lines, "Pinned: no automatic upgrade")
				}
			}
		}
		if p.Identity != nil {
			lines = append(lines, "Package identity: "+p.Identity.State)
			if p.Identity.CanonicalID != "" {
				lines = append(lines, "Canonical ID: "+p.Identity.CanonicalID)
			}
			if p.Identity.Reason != "" {
				lines = append(lines, p.Identity.Reason)
			}
		}
		if packageView(m.view) {
			if ok, reason := m.upgradeEligibility(m.view, *p); !ok {
				lines = append(lines, "", "Upgrade unavailable: "+reason)
			}
		}
		if p.Description != "" {
			lines = append(lines, "", p.Description)
		}
		if p.Root != "" {
			lines = append(lines, "", "Install root:", p.Root)
		}
		if len(p.Commands) > 0 {
			lines = append(lines, "", "Commands: "+strings.Join(p.Commands, ", "))
		}
		if len(p.ExecutablePaths) > 0 {
			lines = append(lines, "", "Executable paths:")
			lines = append(lines, p.ExecutablePaths...)
		}
		if len(p.PathMatches) > 0 {
			lines = append(lines, "", "Also present on PATH (presence is not installation ownership):")
			for _, match := range p.PathMatches {
				label := match.Path
				if match.Preferred {
					label += " (first PATH match)"
				}
				lines = append(lines, label)
			}
		}
		if p.Manager == "mise" {
			if m.pendingActivation(p) {
				lines = append(lines, "", "Latest "+p.Latest+" is already installed; pending global activation.", "Press a to review global activation of "+p.Latest+".")
			}
			lines = append(lines, "", fmt.Sprintf("Selected by current mise context: %t", p.Active), fmt.Sprintf("Recorded in global configuration: %t", p.Global), "Activation is separate from installation and PATH resolution.")
			if p.ConfigSource != "" {
				lines = append(lines, "Config: "+p.ConfigSource)
			}
		}
		lines = append(lines, "", "Source evidence:")
		if len(p.Evidence) == 0 {
			lines = append(lines, "unknown — no ownership evidence was provided")
		}
		for _, e := range p.Evidence {
			lines = append(lines, e.Kind+" · "+e.Source+": "+e.Detail)
		}
	} else if manager := r.managerInfo; manager != nil {
		lines = append(lines, manager.Name, "", "ID: "+manager.ID, "Status: "+manager.Status, "Version: "+orUnknown(manager.Version), "CLI: "+orUnknown(manager.Path), "", "Capabilities: "+strings.Join(manager.Capabilities, ", "))
		lines = append(lines, "Scope: "+orUnknown(manager.Scope), "Required: "+orUnknown(manager.Requirement))
		if manager.ComponentKind != "" {
			lines = append(lines, "Component: "+manager.ComponentKind)
		}
		if manager.VersionSubject != "" {
			lines = append(lines, "Version describes: "+manager.VersionSubject)
		}
		if manager.Launcher != "" {
			lines = append(lines, "Launcher: "+manager.Launcher)
		}
		if manager.ReasonCode != "" {
			lines = append(lines, "Detection: "+manager.ReasonCode)
		}
		if manager.ID == "gh-ext" {
			lines = append(lines, "Native command: gh extension", "Manager version describes the gh launcher; package rows describe registered extensions.")
		}
		if manager.Reason != "" {
			lines = append(lines, "Reason: "+manager.Reason)
		}
		if len(manager.Groups) > 0 {
			lines = append(lines, "Groups: "+strings.Join(manager.Groups, ", "))
		}
		if health := manager.Health; health != nil {
			lines = append(lines, "", "Manager update: "+health.UpdateStatus, "Candidate: "+orUnknown(health.CandidateVersion), "Owner: "+orUnknown(health.Owner), "Channel: "+orUnknown(health.Channel), "Strategy: "+orUnknown(health.Strategy), "Checked: "+observed(health.CheckedAt))
			if health.Cached {
				lines = append(lines, "Result from the 24-hour check cache; r forces a check.")
			}
			if health.Stale {
				lines = append(lines, "STALE update check")
			}
			if health.Recommendation != "" {
				lines = append(lines, health.Recommendation)
			}
			if health.GuideURL != "" {
				lines = append(lines, health.GuideURL)
			}
		} else if manager.Path != "" {
			lines = append(lines, "", "Update check: pending; u reviews an update through the owning manager.")
		}
		if !manager.Available {
			lines = append(lines, "", "Press s after closing this panel to review setup.")
		}
		for _, err := range manager.Errors {
			lines = append(lines, "Error: "+err)
		}
	} else if f := r.finding; f != nil {
		lines = append(lines, f.Name, "Kind: "+f.Kind, "", f.Message, "", "Paths:")
		lines = append(lines, f.Paths...)
	} else if e := r.executable; e != nil {
		pathEntry := fmt.Sprintf("PATH entry: %d", e.PathIndex)
		if e.PathIndex < 0 {
			pathEntry = "Registered executable, outside PATH"
		}
		lines = append(lines, e.Name, "", e.Path, "", "Resolved target: "+orUnknown(e.Target), pathEntry, fmt.Sprintf("First match in scanned PATH: %t", e.Preferred), "Provider: "+orUnknown(e.Manager))
		if e.EquivalentTo != "" {
			lines = append(lines, "Same target as: "+e.EquivalentTo)
		}
		if len(e.Chain) > 0 {
			lines = append(lines, "", "Link/shim chain:")
			lines = append(lines, e.Chain...)
		}
		if e.Problem != "" {
			lines = append(lines, "", "Problem: "+e.Problem)
		}
		for _, evidence := range e.Evidence {
			lines = append(lines, evidence.Kind+" · "+evidence.Source+": "+evidence.Detail)
		}
		lines = append(lines, "", "This scan cannot account for shell aliases, functions, or a different shell environment.")
	}
	return clean(strings.Join(lines, "\n"))
}

func (m *Model) modalView() string {
	switch m.modal {
	case versionModal:
		return m.versionView()
	case setupModal:
		return m.setupView()
	case providersModal:
		return m.providersView()
	case saveSetModal:
		return m.saveSetView()
	case resolutionModal, maintenanceModal:
		return m.workflowView()
	case exportPromptModal:
		return m.exportPromptView()
	}
	title, text := m.modalText()
	return m.scrollModal(title, text, "")
}
func (m *Model) modalText() (string, string) {
	switch m.modal {
	case batchReviewModal:
		return "Review package upgrades", m.batchReviewText()
	case batchResultModal:
		return "Package upgrade results", batchResultText(m.batch.result, m.batch.history, m.batch.err)
	case detailsModal:
		if r, ok := m.selectedRow(); ok {
			return "Package details", m.rowDetails(r)
		}
		return "Package details", "This item is no longer in the visible results. Close this panel and choose a current item."
	case helpModal:
		return "Keyboard & scope", m.helpText()
	case issuesModal:
		return "Errors & coverage", m.issuesText()
	case resultModal:
		return "Last operation", resultText(m.lastResult, m.lastError)
	case promptModal:
		if m.workflow.loading {
			return "Prompt preview", "Collecting current context…"
		}
		if m.workflow.err != nil {
			return "Prompt preview", "Could not prepare prompt: " + m.workflow.err.Error()
		}
		return "Prompt preview", m.workflow.prompt.Markdown
	case planModal:
		if m.width < 40 || m.height < 10 {
			return "Review changes", "Resize to at least 40×10 to review and approve this plan."
		}
		if m.planLoading {
			return "Review changes", "Preparing the exact operation plan…\n\nNo changes have been made."
		}
		if m.planErr != nil {
			return "Review changes", "Could not prepare this operation.\n\n" + m.planErr.Error()
		}
		return "Review changes", m.planText()
	}
	return "", ""
}

func (m *Model) versionView() string {
	width, height := m.width, max(3, m.height-4)
	lines := wrapLines(m.versionRequest.Package+"\n\nChoose a version or latest; the plan resolves it before installation.\nInstallation does not activate the tool.", width-4)
	lines = append(lines, "", m.input.View())
	footer, _ := buttonLine(m.modalButtons(), width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / Install with mise"), width), pane("Version", lines, width, height, true), line(clean(m.status), width), line(" Printable keys edit the version.", width), footer}, "\n")
}

func (m *Model) scrollModal(title, text, footer string) string {
	width, height := m.width, max(3, m.height-4)
	lines := wrapLines(text, width-4)
	available := max(1, height-3)
	offset := clamp(m.modalOffset, 0, max(0, len(lines)-available))
	visible := lines[offset:min(len(lines), offset+available)]
	body := pane(title, visible, width, height, true)
	count := fmt.Sprintf(" %d–%d / %d lines", min(offset+1, len(lines)), min(offset+available, len(lines)), len(lines))
	footer, _ = buttonLine(m.modalButtons(), width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / "+title), width), body, line(muted.Render(count+" · ↑↓/wheel scroll"), width), line(clean(m.status), width), line(footer, width)}, "\n")
}

func (m *Model) setupView() string {
	width, height := m.width, max(3, m.height-4)
	var lines []string
	if m.setup.loading {
		lines = append(lines, "Discovering setup choices…", "", "Esc returns without changes.")
	} else if m.setup.err != nil {
		lines = wrapLines("Setup discovery failed:\n"+m.setup.err.Error()+"\n\nr retry · Esc back", width-4)
	} else {
		lines = append(lines, "Choose components; review once before anything runs.", "")
		start, count := m.setupWindow()
		for i := start; i < min(start+count, len(m.setup.options)); i++ {
			option := m.setup.options[i]
			check := "[ ]"
			if m.setup.selected[option.ID] {
				check = "[x]"
			}
			if option.Installed {
				check = "[✓]"
			}
			prefix := "  "
			if i == m.setup.position {
				prefix = "> "
			}
			label := prefix + check + " " + option.Name
			if option.Installed {
				label += " (installed)"
			} else if option.Recommended {
				label += " (recommended)"
			}
			label = line(clean(label), width-2)
			if i == m.setup.position {
				label = selectedStyle.Render(label)
			}
			lines = append(lines, label)
		}
		if len(m.setup.options) > 0 {
			option := m.setup.options[m.setup.position]
			lines = append(lines, "")
			lines = append(lines, wrapLines(option.Description, width-4)...)
			if option.GuideURL != "" {
				lines = append(lines, clean(option.GuideURL))
			}
		} else {
			lines = append(lines, "No additional setup options are available.")
		}
	}
	footer, _ := buttonLine(m.modalButtons(), width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / Setup"), width), pane("Manager setup", lines, width, height, true), line(clean(m.status), width), line(" Space/click toggle · ↑↓/jk select", width), footer}, "\n")
}

func (m *Model) providersView() string {
	width, height := m.width, max(3, m.height-4)
	lines := []string{fmt.Sprintf("%d selected · draft only until Apply", len(m.providerPicker.selected)), ""}
	choices := m.providerChoices()
	start, count := m.providerWindow()
	for i := start; i < min(len(choices), start+count); i++ {
		choice := choices[i]
		prefix := "  "
		if i == m.providerPicker.position {
			prefix = "> "
		}
		label := choice.label
		if choice.kind == "manager" {
			index := -1
			for j, id := range m.providerPicker.selected {
				if id == choice.id {
					index = j
					break
				}
			}
			check := "[ ]"
			if index >= 0 {
				check = fmt.Sprintf("[%d]", index+1)
			}
			label = check + " " + label
		} else {
			label = "+ " + label
		}
		label = line(prefix+clean(label), width-2)
		if i == m.providerPicker.position {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, label)
	}
	footer, _ := buttonLine(m.modalButtons(), width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / Ordered provider sets"), width), pane("Groups, saved sets, and managers", lines, width, height, true), line(clean(m.status), width), line(" Space/click load or toggle · [/] move priority · S save", width), footer}, "\n")
}

func (m *Model) saveSetView() string {
	width, height := m.width, max(3, m.height-4)
	check := "[ ]"
	if m.providerPicker.saveDefault {
		check = "[x]"
	}
	lines := []string{"Priority: " + strings.Join(m.providerPicker.selected, " > "), "", m.input.View(), "", check + " Make this the configured default"}
	if m.providerPicker.err != nil {
		lines = append(lines, "", clean(m.providerPicker.err.Error()))
	}
	footer, _ := buttonLine(m.modalButtons(), width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / Save ordered set"), width), pane("Save preferences", lines, width, height, true), line(clean(m.status), width), line(" Tab/click toggles default · Enter saves · Esc returns", width), footer}, "\n")
}

func (m *Model) planText() string {
	plan := m.plan
	lines := []string{plan.Title, ""}
	if resolution := plan.Resolution; resolution != nil {
		lines = append(lines, "KEEP: "+resolution.Keep.Package.Manager+" / "+resolution.Keep.Package.ID+" "+resolution.Keep.Package.Version, "REMOVE: "+resolution.Remove.Package.Manager+" / "+resolution.Remove.Package.ID+" "+resolution.Remove.Package.Version, "Removed prefix: "+resolution.Remove.Prefix, "Affected commands: "+strings.Join(resolution.Remove.Commands, ", "))
		for _, path := range resolution.Remove.Paths {
			lines = append(lines, "Removed executable: "+path.Path)
		}
		for _, path := range resolution.Keep.Paths {
			lines = append(lines, "Retained executable: "+path.Path)
		}
		for _, warning := range resolution.Remove.Warnings {
			lines = append(lines, "WARNING: "+warning)
		}
	}
	if health := plan.ManagerUpdate; health != nil {
		lines = append(lines, "Manager: "+health.Manager, "Owner: "+orUnknown(health.Owner), "Strategy: "+orUnknown(health.Strategy))
		if !health.ApplySupported {
			lines = append(lines, "Guidance only — no command will be executed.")
		}
		if health.Recommendation != "" {
			lines = append(lines, health.Recommendation)
		}
		if health.GuideURL != "" {
			lines = append(lines, health.GuideURL)
		}
	}
	if plan.Request.Operation != "" {
		lines = append(lines, "Operation: "+plan.Request.Operation, "Provider: "+plan.Request.Manager, "Package: "+plan.Request.Package)
		if plan.Request.Version != "" {
			lines = append(lines, "Version: "+plan.Request.Version)
		}
	}
	for _, warning := range plan.Warnings {
		lines = append(lines, "", "WARNING: "+warning)
	}
	if plan.Preview != "" {
		lines = append(lines, "", "Native command preview:", plan.Preview)
	}
	for i, step := range plan.Steps {
		lines = append(lines, "", fmt.Sprintf("%d. %s", i+1, step.Description))
		if step.Command.Path != "" {
			lines = append(lines, process.Display(step.Command))
		}
		if step.URL != "" {
			lines = append(lines, "Download: "+step.URL)
		}
		if step.Destination != "" {
			lines = append(lines, "Destination: "+step.Destination)
		}
		if step.Digest != "" {
			lines = append(lines, "SHA-256: "+step.Digest)
		}
		if step.GuideURL != "" {
			lines = append(lines, "Guide: "+step.GuideURL)
		}
		if step.Verify != nil {
			lines = append(lines, "Verify: "+process.Display(*step.Verify))
		}
	}
	if m.planExecutable() {
		lines = append(lines, "", "Native package-manager output will open in this terminal.", "Changes already made cannot be assumed rolled back if interrupted.")
	}
	return strings.Join(lines, "\n")
}

func pane(title string, content []string, width, height int, focused bool) string {
	innerWidth, innerHeight := max(1, width-2), max(1, height-2)
	prefix := " "
	if focused {
		prefix = ">"
	}
	titleLine := line(prefix+" "+title, innerWidth)
	if focused {
		titleLine = accent.Render(titleLine)
	} else {
		titleLine = muted.Render(titleLine)
	}
	lines := []string{titleLine}
	for _, s := range content {
		if len(lines) >= innerHeight {
			break
		}
		lines = append(lines, line(s, innerWidth))
	}
	for len(lines) < innerHeight {
		lines = append(lines, strings.Repeat(" ", innerWidth))
	}
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if focused {
		style = style.BorderForeground(lipgloss.Color("#7AC7E3"))
	} else {
		style = style.BorderForeground(lipgloss.Color("#586273"))
	}
	return style.Render(strings.Join(lines, "\n"))
}

func clean(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "\t", "  ")
}

func line(s string, width int) string {
	width = max(1, width)
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func wrapLines(s string, width int) []string {
	return strings.Split(ansi.Hardwrap(clean(s), max(1, width), true), "\n")
}

func fitScreen(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = line(lines[i], width)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", max(1, width)))
	}
	return strings.Join(lines, "\n")
}
