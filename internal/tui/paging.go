package tui

// Paging always uses the same viewport capacity as rendering. These keys are
// dispatched only after text inputs, so their editing bindings stay intact.
func pageDelta(key string, rows int) (int, bool) {
	rows = max(1, rows)
	switch key {
	case "ctrl+d":
		return max(1, rows/2), true
	case "ctrl+u":
		return -max(1, rows/2), true
	case "pgdown", "ctrl+f":
		return rows, true
	case "pgup", "ctrl+b":
		return -rows, true
	}
	return 0, false
}
func (m *Model) pageRows() int {
	switch m.modal {
	case setupModal:
		_, count := m.setupWindow()
		return count
	case providersModal:
		_, count := m.providerWindow()
		return count
	case resolutionModal, maintenanceModal:
		return m.workflowDetailRows()
	case noModal:
		if m.managerFocus {
			return max(1, max(3, m.height-6)-4)
		}
		return m.pageSize()
	default:
		return max(1, max(3, m.height-4)-3)
	}
}
func (m *Model) pageKey(key string) bool {
	delta, ok := pageDelta(key, m.pageRows())
	if !ok {
		return false
	}
	switch m.modal {
	case noModal:
		m.move(delta)
	case setupModal:
		m.setup.position = clamp(m.setup.position+delta, 0, len(m.setup.options)-1)
	case providersModal:
		m.providerPicker.position = clamp(m.providerPicker.position+delta, 0, len(m.providerChoices())-1)
	default:
		m.scrollModalBy(delta)
	}
	return true
}
func (m *Model) workflowDetailRows() int {
	start, count := m.workflowWindow()
	shown := max(0, min(count, len(m.workflowRows())-start))
	return max(1, m.height-10-shown)
}
func (m *Model) modalScrollLimit() int {
	text := ""
	if m.modal == resolutionModal || m.modal == maintenanceModal {
		text = m.workflowDescription()
	} else {
		_, text = m.modalText()
	}
	return max(0, len(wrapLines(text, m.width-4))-m.pageRows())
}
func (m *Model) scrollModalBy(delta int) {
	limit := m.modalScrollLimit()
	m.modalOffset = clamp(clamp(m.modalOffset, 0, limit)+delta, 0, limit)
}
func (m *Model) scrollDetailsBy(delta int) {
	if r, ok := m.selectedRow(); ok {
		rect := m.layout().details
		limit := max(0, len(wrapLines(m.rowDetails(r), rect.w-4))-max(1, rect.h-3))
		m.detailOffset = clamp(clamp(m.detailOffset, 0, limit)+delta, 0, limit)
	}
}
