package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/promptio"
)

type workflowState struct {
	generation               uint64
	cancel                   context.CancelFunc
	loading                  bool
	err                      error
	assessment               domain.ConflictAssessment
	name, keepID, selectedID string
	position                 int
	queue                    domain.MaintenanceQueue
	queueIDs                 []string
	attempted                map[string]string
	activeJob                string
	prompt                   domain.RenderedPrompt
	promptRequest            domain.PromptRequest
	promptReturn             modalKind
	ioBusy                   bool
	initial                  string
}
type conflictMsg struct {
	generation uint64
	assessment domain.ConflictAssessment
	err        error
}
type maintenanceMsg struct {
	generation uint64
	queue      domain.MaintenanceQueue
	err        error
}
type promptMsg struct {
	generation uint64
	prompt     domain.RenderedPrompt
	err        error
}
type promptIOResultMsg struct {
	generation   uint64
	action, path string
	err          error
}

func (m *Model) workflowContext() (context.Context, uint64) {
	if m.workflow.cancel != nil {
		m.workflow.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.workflow.cancel = cancel
	m.workflow.generation++
	m.workflow.loading = true
	m.workflow.err = nil
	m.modalOffset = 0
	return ctx, m.workflow.generation
}
func (m *Model) cancelWorkflow() {
	if m.workflow.cancel != nil {
		m.workflow.cancel()
	}
	m.workflow.generation++
	m.workflow.loading = false
}
func (m *Model) commandTarget() string {
	if r, ok := m.selectedRow(); ok {
		if r.executable != nil {
			return r.executable.Name
		}
		if r.finding != nil {
			return r.finding.Name
		}
		if r.pkg != nil && len(r.pkg.Commands) > 0 {
			return r.pkg.Commands[0]
		}
	}
	return ""
}
func (m *Model) startConflict() tea.Cmd {
	name := m.commandTarget()
	if name == "" {
		m.status = "Choose a diagnostic command or a package with a recorded executable first."
		return nil
	}
	m.workflow.keepID = ""
	m.workflow.selectedID = ""
	m.workflow.position = 0
	return m.loadConflict(name)
}
func (m *Model) loadConflict(name string) tea.Cmd {
	ctx, generation := m.workflowContext()
	m.workflow.name = name
	m.modal = resolutionModal
	service := m.service
	return func() tea.Msg {
		assessment, err := service.AssessConflict(ctx, name)
		return conflictMsg{generation, assessment, err}
	}
}
func (m *Model) acceptConflict(msg conflictMsg) {
	if msg.generation != m.workflow.generation || m.modal != resolutionModal {
		return
	}
	m.workflow.loading, m.workflow.err = false, msg.err
	if msg.err != nil {
		return
	}
	m.workflow.assessment = msg.assessment
	keepFound := false
	selectedFound := false
	for i, item := range msg.assessment.Installations {
		if item.ID == m.workflow.keepID {
			keepFound = true
		}
		if item.ID == m.workflow.selectedID {
			m.workflow.position = i
			selectedFound = true
		}
	}
	if !keepFound {
		m.workflow.keepID = ""
	}
	if !selectedFound {
		m.workflow.position = clamp(m.workflow.position, 0, len(msg.assessment.Installations)-1)
	}
	m.selectConflictPosition()
}
func (m *Model) selectConflictPosition() {
	if len(m.workflow.assessment.Installations) > 0 {
		m.workflow.position = clamp(m.workflow.position, 0, len(m.workflow.assessment.Installations)-1)
		m.workflow.selectedID = m.workflow.assessment.Installations[m.workflow.position].ID
	} else {
		m.workflow.selectedID = ""
	}
}
func (m *Model) conflictItem() (domain.ConflictInstallation, bool) {
	for _, item := range m.workflow.assessment.Installations {
		if item.ID == m.workflow.selectedID {
			return item, true
		}
	}
	return domain.ConflictInstallation{}, false
}
func (m *Model) keepConflict() {
	if m.workflow.loading || m.workflow.err != nil {
		return
	}
	if item, ok := m.conflictItem(); ok {
		m.workflow.keepID = item.ID
		m.status = "Retaining " + item.Package.Manager + " / " + item.Package.ID + ". Choose one other installation to review removal."
	}
}
func (m *Model) reviewConflictRemoval() tea.Cmd {
	if m.workflow.loading || m.workflow.err != nil {
		return nil
	}
	if m.workflow.keepID == "" {
		m.keepConflict()
		return nil
	}
	item, ok := m.conflictItem()
	if !ok || item.ID == m.workflow.keepID {
		m.status = "Choose a different installation to remove; K changes the retained choice."
		return nil
	}
	if item.Status != "ready" || len(item.Blockers) > 0 {
		m.status = "Automatic removal is unavailable: " + strings.Join(item.Blockers, "; ")
		if len(item.Blockers) == 0 {
			m.status = "Automatic removal is unavailable for status " + item.Status + "; p prepares a handoff prompt."
		}
		return nil
	}
	if m.planCancel != nil {
		m.planCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.planCancel = cancel
	m.planGeneration++
	generation, service := m.planGeneration, m.service
	request := domain.ResolutionRequest{Name: m.workflow.name, KeepID: m.workflow.keepID, RemoveID: item.ID}
	m.modal = planModal
	m.planReturn = resolutionModal
	m.modalOffset = 0
	m.planLoading = true
	m.planErr = nil
	m.plan = domain.ActionPlan{}
	return func() tea.Msg {
		plan, err := service.PlanResolution(ctx, request)
		return planMsg{generation, plan, err}
	}
}

func (m *Model) openMaintenance(force ...bool) tea.Cmd {
	m.workflow.attempted = make(map[string]string)
	m.workflow.position = 0
	m.workflow.activeJob = ""
	m.workflow.queueIDs = nil
	if m.scopeChanged {
		m.workflow.queueIDs = m.effectiveManagers()
	}
	return m.loadMaintenance(len(force) > 0 && force[0])
}
func (m *Model) loadMaintenance(refresh bool) tea.Cmd {
	ctx, generation := m.workflowContext()
	m.modal = maintenanceModal
	service := m.service
	ids := append([]string(nil), m.workflow.queueIDs...)
	return func() tea.Msg {
		queue, err := service.MaintenanceQueue(ctx, ids, refresh)
		return maintenanceMsg{generation, queue, err}
	}
}
func (m *Model) acceptMaintenance(msg maintenanceMsg) {
	if msg.generation != m.workflow.generation || m.modal != maintenanceModal {
		return
	}
	m.workflow.loading, m.workflow.err = false, msg.err
	if msg.err != nil {
		return
	}
	m.workflow.queue = msg.queue
	m.workflow.position = clamp(m.workflow.position, 0, len(msg.queue.Jobs)-1)
	for i, job := range msg.queue.Jobs {
		if job.ApplySupported && m.workflow.attempted[job.ID] == "" {
			m.workflow.position = i
			break
		}
	}
}
func (m *Model) queueJob() (domain.MaintenanceJob, bool) {
	if len(m.workflow.queue.Jobs) == 0 {
		return domain.MaintenanceJob{}, false
	}
	return m.workflow.queue.Jobs[clamp(m.workflow.position, 0, len(m.workflow.queue.Jobs)-1)], true
}
func (m *Model) reviewMaintenance() tea.Cmd {
	if m.workflow.loading || m.workflow.err != nil {
		return nil
	}
	job, ok := m.queueJob()
	if !ok {
		return nil
	}
	if !job.ApplySupported {
		m.status = job.Category + ": " + job.Reason + " · p previews a repair prompt"
		return nil
	}
	m.workflow.activeJob = job.ID
	command := m.startManagerPlan(job.Representative)
	m.planReturn = maintenanceModal
	return command
}
func (m *Model) skipMaintenance() {
	if m.workflow.loading {
		return
	}
	job, ok := m.queueJob()
	if !ok {
		return
	}
	if m.workflow.attempted == nil {
		m.workflow.attempted = make(map[string]string)
	}
	m.workflow.attempted[job.ID] = "skipped"
	m.status = "Skipped " + job.Title
	for step := 1; step <= len(m.workflow.queue.Jobs); step++ {
		i := (m.workflow.position + step) % len(m.workflow.queue.Jobs)
		if m.workflow.attempted[m.workflow.queue.Jobs[i].ID] == "" {
			m.workflow.position = i
			return
		}
	}
}
func (m *Model) afterWorkflowExecution(msg executedMsg) (tea.Cmd, bool) {
	switch m.planReturn {
	case resolutionModal:
		m.status = resultText(msg.result, msg.err) + " · reassessing installed instances"
		return tea.Batch(m.loadConflict(m.workflow.name), m.loadManagers(), m.loadView(updatesView)), true
	case maintenanceModal:
		status := "completed"
		if msg.err != nil {
			status = "failed"
		}
		if m.workflow.attempted == nil {
			m.workflow.attempted = make(map[string]string)
		}
		m.workflow.attempted[m.workflow.activeJob] = status
		m.status = "Previous job " + status + ". Rechecking the remaining queue; each item needs its own review."
		return tea.Batch(m.loadMaintenance(true), m.loadManagers(), m.loadView(updatesView)), true
	}
	return nil, false
}

func (m *Model) openPrompt() tea.Cmd {
	if m.modal == planModal && (m.planLoading || m.planErr != nil) {
		return nil
	}
	request := domain.PromptRequest{Managers: m.effectiveManagers()}
	parent := m.modal
	switch m.modal {
	case resolutionModal:
		request.Recipe = "path-conflict"
		request.Target = m.workflow.name
	case maintenanceModal:
		if job, ok := m.queueJob(); ok {
			request.Recipe = "manager-repair"
			request.Target = job.Representative
		}
	case planModal:
		if m.planReturn == resolutionModal {
			request.Recipe = "path-conflict"
			request.Target = m.workflow.name
		} else if m.plan.ManagerUpdate != nil {
			request.Recipe = "manager-repair"
			request.Target = m.plan.ManagerUpdate.Manager
		} else if m.planReturn == maintenanceModal {
			if job, ok := m.queueJob(); ok {
				request.Recipe = "manager-repair"
				request.Target = job.Representative
			}
		}
	default:
		if row, ok := m.selectedRow(); ok && row.managerInfo != nil {
			request.Recipe = "manager-repair"
			request.Target = row.managerInfo.ID
		} else {
			request.Recipe = "path-conflict"
			request.Target = m.commandTarget()
		}
	}
	if request.Recipe == "path-conflict" || request.Recipe == "manager-repair" && request.Target != "" {
		request.Managers = nil
	}
	if request.Target == "" {
		m.status = "Choose a manager or a recorded command to prepare a prompt."
		return nil
	}
	ctx, generation := m.workflowContext()
	m.workflow.promptReturn = parent
	m.workflow.promptRequest = request
	m.workflow.prompt = domain.RenderedPrompt{}
	m.modal = promptModal
	service := m.service
	return func() tea.Msg {
		prompt, err := service.RenderPrompt(ctx, request)
		return promptMsg{generation, prompt, err}
	}
}
func (m *Model) acceptPrompt(msg promptMsg) {
	if msg.generation != m.workflow.generation || m.modal != promptModal {
		return
	}
	m.workflow.loading, m.workflow.err = false, msg.err
	if msg.err == nil {
		m.workflow.prompt = msg.prompt
	}
}
func (m *Model) copyPrompt() tea.Cmd {
	if m.workflow.loading || m.workflow.err != nil || m.workflow.ioBusy {
		return nil
	}
	text, ctx, generation := m.workflow.prompt.Markdown, m.ctx, m.workflow.generation
	m.workflow.ioBusy = true
	return func() tea.Msg {
		return promptIOResultMsg{generation: generation, action: "copy", err: promptio.Copy(ctx, text)}
	}
}
func (m *Model) exportPrompt() tea.Cmd {
	if m.workflow.loading || m.workflow.err != nil || m.workflow.ioBusy {
		return nil
	}
	m.modal = exportPromptModal
	m.input.Prompt = "Path: "
	m.input.Placeholder = "New Markdown file (existing files are preserved)"
	m.input.SetValue("lazypkg-prompt.md")
	return m.input.Focus()
}
func (m *Model) exportInput(message tea.Msg) tea.Cmd {
	if m.workflow.ioBusy {
		return nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.modal = promptModal
			m.input.Blur()
			return nil
		case "enter":
			path := strings.TrimSpace(m.input.Value())
			if path == "" {
				m.status = "Choose an export file path."
				return nil
			}
			text, generation := m.workflow.prompt.Markdown, m.workflow.generation
			m.workflow.ioBusy = true
			return func() tea.Msg {
				return promptIOResultMsg{generation: generation, action: "export", path: path, err: promptio.Export(path, text)}
			}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	return cmd
}
func (m *Model) acceptPromptIO(msg promptIOResultMsg) {
	if msg.generation != m.workflow.generation {
		return
	}
	m.workflow.ioBusy = false
	if msg.err != nil {
		m.status = msg.action + " failed: " + msg.err.Error()
		return
	}
	if msg.action == "copy" {
		m.status = "Copied the previewed Markdown."
	} else {
		m.status = "Exported the previewed Markdown to " + msg.path
		m.modal = promptModal
		m.input.Blur()
	}
}

func (m *Model) workflowKey(key tea.KeyPressMsg) tea.Cmd {
	if m.modal == exportPromptModal {
		return m.exportInput(key)
	}
	name := key.String()
	if m.pageKey(name) {
		return nil
	}
	if name == "esc" || name == "ctrl+c" || name == "q" {
		m.closeWorkflow()
		return nil
	}
	if m.modal == promptModal {
		switch name {
		case "c":
			return m.copyPrompt()
		case "e":
			return m.exportPrompt()
		case "up", "k":
			m.scrollModalBy(-1)
		case "down", "j":
			m.scrollModalBy(1)
		}
		return nil
	}
	if m.workflow.loading {
		return nil
	}
	if name == "p" {
		return m.openPrompt()
	}
	if name == "r" {
		if m.modal == resolutionModal {
			return m.loadConflict(m.workflow.name)
		}
		return m.loadMaintenance(true)
	}
	previousPosition := m.workflow.position
	count := len(m.workflow.queue.Jobs)
	if m.modal == resolutionModal {
		count = len(m.workflow.assessment.Installations)
	}
	switch name {
	case "up", "k":
		m.workflow.position = clamp(m.workflow.position-1, 0, count-1)
	case "down", "j":
		m.workflow.position = clamp(m.workflow.position+1, 0, count-1)
	case "home", "g":
		m.workflow.position = 0
	case "end", "G":
		m.workflow.position = max(0, count-1)
	}
	if previousPosition != m.workflow.position {
		m.modalOffset = 0
	}
	if m.modal == resolutionModal {
		m.selectConflictPosition()
		if name == "K" {
			m.keepConflict()
		}
		if name == "enter" {
			return m.reviewConflictRemoval()
		}
	} else {
		if name == "s" {
			m.skipMaintenance()
		}
		if name == "enter" {
			return m.reviewMaintenance()
		}
	}
	return nil
}
func (m *Model) closeWorkflow() {
	if m.workflow.ioBusy {
		m.status = "Finishing the requested copy/export…"
		return
	}
	if m.modal == exportPromptModal {
		m.modal = promptModal
		m.input.Blur()
		return
	}
	if m.modal == promptModal {
		parent := m.workflow.promptReturn
		m.cancelWorkflow()
		m.workflow.err = nil
		m.modal = parent
		m.modalOffset = 0
		return
	}
	m.cancelWorkflow()
	m.modal = noModal
	m.modalOffset = 0
}

func (m *Model) workflowRows() []string {
	var rows []string
	if m.modal == resolutionModal {
		for _, item := range m.workflow.assessment.Installations {
			mark := ""
			if item.ID == m.workflow.keepID {
				mark = "[KEEP] "
			}
			winner := ""
			if item.Effective {
				winner = " · PATH first"
			}
			rows = append(rows, mark+item.Package.Manager+" / "+item.Package.ID+" "+item.Package.Version+" · "+item.Status+winner)
		}
	} else {
		for _, job := range m.workflow.queue.Jobs {
			status := m.workflow.attempted[job.ID]
			if status != "" {
				status = " [" + status + "]"
			}
			rows = append(rows, job.Category+" · "+job.Title+status)
		}
	}
	return rows
}
func (m *Model) workflowDescription() string {
	if m.modal == resolutionModal {
		item, ok := m.conflictItem()
		if !ok {
			return "No installed instances were found."
		}
		text := fmt.Sprintf("Project: %s\nPrefix: %s\nManager: %s\n", orUnknown(item.Project), orUnknown(item.Prefix), orUnknown(item.ManagerPath))
		for _, path := range item.Paths {
			text += path.Path + "\n"
		}
		if len(item.Dependents) > 0 {
			text += "Dependents: " + strings.Join(item.Dependents, ", ") + "\n"
		}
		if len(item.Commands) > 0 {
			text += "Other commands: " + strings.Join(item.Commands, ", ") + "\n"
		}
		for _, blocker := range item.Blockers {
			text += "Blocked: " + blocker + "\n"
		}
		for _, warning := range item.Warnings {
			text += "Warning: " + warning + "\n"
		}
		return text
	}
	job, ok := m.queueJob()
	if !ok {
		return "No manager maintenance jobs were found."
	}
	return job.Reason + "\nTarget: " + job.Target + "\nAdapters: " + strings.Join(job.ManagerIDs, ", ") + "\nEach actionable item has a separate plan and confirmation."
}

// workflowWindow reserves room for the selected item's context even at 80×24.
func (m *Model) workflowWindow() (int, int) {
	count := max(1, min(6, (m.height-10)/2))
	return clamp(m.workflow.position-count+1, 0, max(0, len(m.workflowRows())-count)), count
}
func (m *Model) workflowView() string {
	title := "Resolve " + m.workflow.name
	summary := "Choose an installation to KEEP; then review one removal."
	if m.modal == maintenanceModal {
		title = "Manager maintenance"
		summary = "Review each item separately; skip or stop at any time."
	}
	lines := []string{summary, ""}
	if m.workflow.loading {
		lines = append(lines, "Checking current state…")
	} else if m.workflow.err != nil {
		lines = append(lines, wrapLines(m.workflow.err.Error(), m.width-4)...)
	} else {
		rows := m.workflowRows()
		start, count := m.workflowWindow()
		for i := start; i < min(len(rows), start+count); i++ {
			prefix := "  "
			if i == m.workflow.position {
				prefix = "> "
			}
			label := line(clean(prefix+rows[i]), m.width-2)
			if i == m.workflow.position {
				label = selectedStyle.Render(label)
			}
			lines = append(lines, label)
		}
		lines = append(lines, "")
		description := wrapLines(m.workflowDescription(), m.width-4)
		available := m.workflowDetailRows()
		offset := clamp(m.modalOffset, 0, max(0, len(description)-available))
		lines = append(lines, description[offset:]...)
	}
	footer, _ := buttonLine(m.modalButtons(), m.width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / "+title), m.width), pane(title, lines, m.width, max(3, m.height-4), true), line(clean(m.status), m.width), line(" ↑↓/jk/click selects · PgUp/PgDn context · review before execution", m.width), footer}, "\n")
}
func (m *Model) exportPromptView() string {
	lines := []string{"Export the exact previewed Markdown to a new file.", "", m.input.View(), "", "Existing files will not be overwritten."}
	if m.workflow.ioBusy {
		lines = append(lines, "Saving…")
	}
	footer, _ := buttonLine(m.modalButtons(), m.width, m.height-1)
	return strings.Join([]string{line(accent.Render(" lazypkg / Export prompt"), m.width), pane("Markdown export", lines, m.width, max(3, m.height-4), true), line(clean(m.status), m.width), line(" Printable keys edit the file path.", m.width), footer}, "\n")
}
