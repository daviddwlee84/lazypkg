package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type rectangle struct{ x, y, w, h int }

func (r rectangle) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type hitTarget struct {
	id, kind, value string
	rect            rectangle
}
type pressedTarget struct {
	id    string
	epoch uint64
}
type screenLayout struct {
	managers, items, details rectangle
	targets                  []hitTarget
}
type uiButton struct{ key, label string }

// layout is pure and is shared by rendering and hit testing. No mouse authority
// depends on whether Bubble Tea happened to call View since the last resize.
func (m *Model) layout() screenLayout {
	l := screenLayout{}
	if m.width < 30 || m.height < 8 {
		return l
	}
	add := func(kind, value string, r rectangle) {
		if r.x < 0 || r.y < 0 || r.x+r.w > m.width || r.y+r.h > m.height || r.w < 1 || r.h < 1 {
			return
		}
		id := fmt.Sprintf("%d:%d:%s:%s", m.modal, m.view, kind, value)
		if m.modal == planModal {
			id += fmt.Sprintf(":%d", m.planGeneration)
		}
		if m.modal == batchReviewModal || m.modal == batchResultModal {
			id += fmt.Sprintf(":%d", m.batch.generation)
		}
		l.targets = append(l.targets, hitTarget{id: id, kind: kind, value: value, rect: r})
	}
	if m.modal != noModal {
		addContent := func(kind, value string, r rectangle) {
			if r.y >= 3 && r.y+r.h <= m.height-4 {
				add(kind, value, r)
			}
		}
		if m.modal == setupModal && !m.setup.loading && m.setup.err == nil {
			start, count := m.setupWindow()
			for i := start; i < min(len(m.setup.options), start+count); i++ {
				addContent("setup", m.setup.options[i].ID, rectangle{1, 5 + i - start, m.width - 2, 1})
			}
		}
		if m.modal == providersModal {
			choices := m.providerChoices()
			start, count := m.providerWindow()
			for i := start; i < min(len(choices), start+count); i++ {
				addContent("provider", choices[i].id, rectangle{1, 5 + i - start, m.width - 2, 1})
			}
		}
		if (m.modal == resolutionModal || m.modal == maintenanceModal) && !m.workflow.loading && m.workflow.err == nil {
			start, count := m.workflowWindow()
			for i := start; i < min(len(m.workflowRows()), start+count); i++ {
				id := ""
				if m.modal == resolutionModal {
					id = m.workflow.assessment.Installations[i].ID
				} else {
					id = m.workflow.queue.Jobs[i].ID
				}
				addContent("workflow", id, rectangle{1, 5 + i - start, m.width - 2, 1})
			}
		}
		if m.modal == exportPromptModal {
			addContent("input", "", rectangle{1, 5, m.width - 2, 1})
		}
		if m.modal == saveSetModal {
			addContent("input", "", rectangle{1, 5, m.width - 2, 1})
			if !m.providerPicker.saving {
				addContent("default", "", rectangle{1, 7, min(m.width-2, 40), 1})
			}
		}
		_, buttons := buttonLine(m.modalButtons(), m.width, m.height-1)
		for _, button := range buttons {
			add("key", button.value, button.rect)
		}
		return l
	}
	bodyHeight := max(3, m.height-6)
	if m.width >= 110 {
		left, right := 22, min(42, m.width/3)
		l.managers = rectangle{0, 3, left, bodyHeight}
		l.items = rectangle{left, 3, m.width - left - right, bodyHeight}
		l.details = rectangle{m.width - right, 3, right, bodyHeight}
	} else if m.width >= 70 {
		l.managers = rectangle{0, 3, 20, bodyHeight}
		l.items = rectangle{20, 3, m.width - 20, bodyHeight}
	} else if m.managerFocus {
		l.managers = rectangle{0, 3, m.width, bodyHeight}
	} else {
		l.items = rectangle{0, 3, m.width, bodyHeight}
	}
	if m.width >= 70 {
		x := 0
		for i, name := range viewNames {
			w := ansi.StringWidth(fmt.Sprintf(" %d %s ", i+1, name))
			add("tab", strconv.Itoa(i), rectangle{x, 1, w, 1})
			x += w + 1
		}
	} else {
		add("key", "h", rectangle{0, 1, 3, 1})
		add("key", "l", rectangle{m.width - 3, 1, 3, 1})
	}
	if m.filtering {
		add("input", "", rectangle{0, 2, m.width, 1})
	} else {
		add("key", "f", rectangle{0, 2, min(24, m.width), 1})
	}
	if l.managers.w > 0 {
		add("key", "f", rectangle{l.managers.x + 1, 4, l.managers.w - 2, 1})
		managers := m.sidebarManagers()
		count := max(1, l.managers.h-4)
		start := clamp(m.managerCursor-count+1, 0, max(0, len(managers)+1-count))
		for i := start; i < min(start+count, len(managers)+1); i++ {
			id := ""
			if i > 0 {
				id = managers[i-1].ID
			}
			if y := 5 + i - start; y+1 <= l.managers.y+l.managers.h-1 {
				add("manager", id, rectangle{l.managers.x + 1, y, l.managers.w - 2, 1})
			}
		}
	}
	if l.items.w > 0 {
		rows := m.rows(m.view)
		count := max(1, l.items.h-5)
		start := clamp(m.states[m.view].offset, 0, max(0, len(rows)-count))
		for i := start; i < min(start+count, len(rows)); i++ {
			if y := 6 + i - start; y+1 <= l.items.y+l.items.h-1 {
				if packageView(m.view) && rows[i].pkg != nil {
					_, marked := m.states[m.view].marks[rows[i].key]
					eligible, _ := m.upgradeEligibility(m.view, *rows[i].pkg)
					if marked || eligible {
						add("mark", rows[i].key, rectangle{l.items.x + 3, y, 3, 1})
					}
				}
				add("row", rows[i].key, rectangle{l.items.x + 1, y, l.items.w - 2, 1})
			}
		}
	}
	first, second := m.footerButtons()
	for i, buttons := range [][]uiButton{first, second} {
		_, hits := buttonLine(buttons, m.width, m.height-2+i)
		for _, hit := range hits {
			add("key", hit.value, hit.rect)
		}
	}
	return l
}

func buttonLine(buttons []uiButton, width, y int) (string, []hitTarget) {
	var hits []hitTarget
	var text strings.Builder
	for i, button := range buttons {
		if i > 0 {
			text.WriteString(" ")
		}
		x := ansi.StringWidth(text.String())
		label := strings.ReplaceAll(clean(button.label), "\n", " ")
		if button.key != "" {
			label = "[" + button.label + "]"
		}
		w := ansi.StringWidth(label)
		if button.key != "" && x+w <= width {
			hits = append(hits, hitTarget{kind: "key", value: button.key, rect: rectangle{x, y, w, 1}})
		}
		text.WriteString(label)
	}
	return line(text.String(), width), hits
}

func (m *Model) footerButtons() ([]uiButton, []uiButton) {
	if m.filtering {
		label := "Enter accept filter"
		if m.view == discoverView {
			label = "Enter search"
		}
		return []uiButton{{"enter", label}, {"esc", "Esc clear / back"}}, []uiButton{{"", "Printable keys edit text; ↑/↓ selects visible rows."}}
	}
	first := []uiButton{{"", "↑↓/jk select"}, {"enter", "Enter details"}, {"/", "/ query"}}
	if m.managerFocus {
		first = []uiButton{{"", "↑↓/jk select"}, {"enter", "Enter filter"}, {"tab", "Tab items"}, {"f", "f providers"}}
	} else {
		for _, a := range m.actions() {
			first = append(first, uiButton{a.key, a.key + " " + a.label})
		}
	}
	if m.view == managersView {
		label := "b all catalog"
		if m.showAllManagers {
			label = "b detected only"
		}
		first = append(first, uiButton{"b", label})
	}
	extra := []uiButton{}
	if packageView(m.view) && !m.managerFocus {
		extra = append(extra, uiButton{" ", "Space mark"}, uiButton{"ctrl+a", "^A all"}, uiButton{"U", "U filtered"})
	}
	if m.view == managersView {
		extra = append(extra, uiButton{"U", "U maintain"}, uiButton{"p", "p prompt"})
	} else if m.commandTarget() != "" {
		extra = append(extra, uiButton{"R", "R resolve"}, uiButton{"p", "p prompt"})
	}
	second := append(extra, []uiButton{{"f", "f providers"}, {"s", "s setup"}, {"r", "r refresh"}, {"?", "? help"}, {"M", "M mouse"}, {"q", "q quit"}}...)
	return first, second
}
func (m *Model) modalButtons() []uiButton {
	switch m.modal {
	case batchReviewModal:
		buttons := []uiButton{{"esc", "Cancel"}}
		if m.batchExecutable() {
			buttons = append(buttons, uiButton{"y", "y Upgrade batch"})
		}
		if !m.batch.loading {
			buttons = append(buttons, uiButton{"r", "r recheck"})
		}
		return buttons
	case batchResultModal:
		buttons := []uiButton{{"esc", "Stop / back"}}
		if len(m.batch.result.Remaining) > 0 {
			buttons = append(buttons, uiButton{"r", "r recheck remaining"}, uiButton{"s", "s skip paused"})
		}
		return buttons
	case setupModal:
		return []uiButton{{"enter", "Enter review"}, {"esc", "Esc back"}, {"r", "r refresh"}}
	case providersModal:
		return []uiButton{{"enter", "Apply"}, {"S", "Save as…"}, {"esc", "Cancel"}}
	case saveSetModal:
		if m.providerPicker.saving {
			return nil
		}
		return []uiButton{{"enter", "Save set"}, {"esc", "Back"}}
	case resolutionModal:
		buttons := []uiButton{{"esc", "Stop"}}
		if !m.workflow.loading && m.workflow.err == nil {
			if item, ok := m.conflictItem(); ok {
				buttons = append(buttons, uiButton{"K", "K keep"})
				if m.workflow.keepID == "" {
					buttons = append(buttons, uiButton{"enter", "Enter keep"})
				} else if item.ID != m.workflow.keepID && item.Status == "ready" && len(item.Blockers) == 0 {
					buttons = append(buttons, uiButton{"enter", "Enter review"})
				}
			}
			buttons = append(buttons, uiButton{"p", "p prompt"}, uiButton{"r", "r recheck"})
		}
		return buttons
	case maintenanceModal:
		buttons := []uiButton{{"esc", "Stop"}}
		if !m.workflow.loading && m.workflow.err == nil {
			if job, ok := m.queueJob(); ok {
				if job.ApplySupported {
					buttons = append(buttons, uiButton{"enter", "Enter review"})
				}
				buttons = append(buttons, uiButton{"s", "s skip"}, uiButton{"p", "p prompt"})
			}
			buttons = append(buttons, uiButton{"r", "r recheck"})
		}
		return buttons
	case promptModal:
		buttons := []uiButton{{"esc", "Back"}}
		if !m.workflow.loading && m.workflow.err == nil && !m.workflow.ioBusy {
			buttons = append(buttons, uiButton{"c", "c copy"}, uiButton{"e", "e export"})
		}
		return buttons
	case exportPromptModal:
		if m.workflow.ioBusy {
			return nil
		}
		return []uiButton{{"enter", "Export"}, {"esc", "Back"}}
	case planModal:
		buttons := []uiButton{{"esc", "Cancel"}}
		if !m.planLoading && m.planErr == nil && m.planExecutable() && m.width >= 40 && m.height >= 10 {
			buttons = append(buttons, uiButton{"y", "y Execute"})
		}
		if m.planReturn == maintenanceModal {
			buttons = append(buttons, uiButton{"s", "s skip"}, uiButton{"q", "q stop"})
		}
		if m.planReturn == maintenanceModal || m.planReturn == resolutionModal {
			buttons = append(buttons, uiButton{"p", "p prompt"})
		}
		return buttons
	case versionModal:
		return []uiButton{{"enter", "Enter review"}, {"esc", "Esc cancel"}}
	case detailsModal:
		buttons := []uiButton{{"esc", "Esc back"}}
		for _, a := range m.actions() {
			buttons = append(buttons, uiButton{a.key, a.key + " " + a.label})
		}
		return buttons
	default:
		return []uiButton{{"esc", "Esc back"}}
	}
}
func (m *Model) setupWindow() (int, int) {
	count := max(1, max(3, m.height-4)-8)
	return clamp(m.setup.position-count+1, 0, max(0, len(m.setup.options)-count)), count
}
func (m *Model) providerWindow() (int, int) {
	count := max(1, max(3, m.height-4)-6)
	return clamp(m.providerPicker.position-count+1, 0, max(0, len(m.providerChoices())-count)), count
}

func (m *Model) invalidateMouse() { m.mouseEpoch++; m.mousePressed = nil }
func (m *Model) hit(x, y int) (hitTarget, bool) {
	for _, target := range m.layout().targets {
		if target.rect.contains(x, y) {
			return target, true
		}
	}
	return hitTarget{}, false
}
func (m *Model) mouseMessage(message tea.Msg) tea.Cmd {
	if !m.mouseEnabled || m.executing {
		return nil
	}
	switch msg := message.(type) {
	case tea.MouseClickMsg:
		m.mousePressed = nil
		if msg.Button == tea.MouseLeft {
			if target, ok := m.hit(msg.X, msg.Y); ok {
				m.mousePressed = &pressedTarget{target.id, m.mouseEpoch}
			}
		}
	case tea.MouseReleaseMsg:
		pressed := m.mousePressed
		m.mousePressed = nil
		if pressed == nil || pressed.epoch != m.mouseEpoch || msg.Button != tea.MouseLeft && msg.Button != tea.MouseNone {
			return nil
		}
		if target, ok := m.hit(msg.X, msg.Y); ok && target.id == pressed.id {
			m.invalidateMouse()
			return m.activateHit(target)
		}
	case tea.MouseWheelMsg:
		m.invalidateMouse()
		delta := 3
		if msg.Button == tea.MouseWheelUp {
			delta = -3
		} else if msg.Button != tea.MouseWheelDown {
			return nil
		}
		if m.modal != noModal {
			switch m.modal {
			case providersModal:
				m.providerPicker.position = clamp(m.providerPicker.position+delta, 0, len(m.providerChoices())-1)
			case resolutionModal, maintenanceModal:
				if !m.workflow.loading {
					m.workflow.position = clamp(m.workflow.position+delta, 0, len(m.workflowRows())-1)
					if m.modal == resolutionModal {
						m.selectConflictPosition()
					}
				}
			case setupModal:
				m.setup.position = clamp(m.setup.position+delta, 0, len(m.setup.options)-1)
			default:
				m.scrollModalBy(delta)
			}
			return nil
		}
		l := m.layout()
		if l.managers.contains(msg.X, msg.Y) {
			m.managerFocus = true
			m.move(delta)
		} else if l.items.contains(msg.X, msg.Y) {
			m.managerFocus = false
			m.move(delta)
		} else if l.details.contains(msg.X, msg.Y) {
			m.scrollDetailsBy(delta)
		}
	}
	return nil
}
func (m *Model) activateHit(target hitTarget) tea.Cmd {
	switch target.kind {
	case "tab":
		index, _ := strconv.Atoi(target.value)
		return m.switchView(index)
	case "mark":
		m.managerFocus = false
		m.toggleMark(target.value)
	case "row":
		m.managerFocus = false
		m.detailOffset = 0
		for i, r := range m.rows(m.view) {
			if r.key == target.value {
				s := &m.states[m.view]
				s.position, s.selected = i, r.key
				m.ensureVisible(s)
				break
			}
		}
	case "manager":
		m.managerCursor = 0
		for i, manager := range m.sidebarManagers() {
			if manager.ID == target.value {
				m.managerCursor = i + 1
			}
		}
		return m.applyManagerFilter()
	case "workflow":
		if m.modal == resolutionModal {
			for i, item := range m.workflow.assessment.Installations {
				if item.ID == target.value {
					m.workflow.position = i
					m.selectConflictPosition()
					break
				}
			}
		} else {
			for i, job := range m.workflow.queue.Jobs {
				if job.ID == target.value {
					m.workflow.position = i
					break
				}
			}
		}
	case "provider":
		m.selectProviderChoice(target.value)
		m.toggleProvider()
	case "setup":
		for i, option := range m.setup.options {
			if option.ID == target.value {
				m.setup.position = i
				if !option.Installed {
					m.setup.selected[option.ID] = !m.setup.selected[option.ID]
				}
			}
		}
	case "default":
		m.providerPicker.saveDefault = !m.providerPicker.saveDefault
	case "input":
		return m.input.Focus()
	case "key":
		key := keyMessage(target.value)
		if m.modal != noModal {
			return m.modalKey(key)
		}
		if m.filtering {
			if target.value == "enter" || target.value == "esc" {
				return m.inputMessage(key)
			}
			m.filtering = false
			m.input.Blur()
		}
		return m.navigationKey(key)
	}
	return nil
}
func keyMessage(value string) tea.KeyPressMsg {
	if strings.HasPrefix(value, "ctrl+") {
		return tea.KeyPressMsg{Code: []rune(strings.TrimPrefix(value, "ctrl+"))[0], Mod: tea.ModCtrl}
	}
	switch value {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
}
