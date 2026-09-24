package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type preferencesMsg struct {
	generation  uint64
	preferences domain.ManagerPreferences
	err         error
}
type savedSetMsg struct {
	generation  uint64
	preferences domain.ManagerPreferences
	name        string
	err         error
}
type providerPicker struct {
	selected    []string
	position    int
	saveDefault bool
	saving      bool
	err         error
}
type providerChoice struct {
	id, label, kind string
	members         []string
}

func (m *Model) loadPreferences() tea.Cmd {
	if m.prefsCancel != nil {
		m.prefsCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.prefsCancel = cancel
	m.prefsGeneration++
	service, generation := m.service, m.prefsGeneration
	return func() tea.Msg { prefs, err := service.Preferences(ctx); return preferencesMsg{generation, prefs, err} }
}
func (m *Model) acceptPreferences(msg preferencesMsg) tea.Cmd {
	if msg.generation != m.prefsGeneration {
		return nil
	}
	m.prefsLoaded, m.prefsErr = true, msg.err
	if msg.err != nil {
		m.status = "Could not load provider preferences: " + msg.err.Error()
		return nil
	}
	m.preferences = msg.preferences
	if !m.mouseOverride {
		m.mouseEnabled = msg.preferences.Mouse
	}
	if !m.scopeChanged {
		oldKey := m.scopeKey()
		m.managerIDs = append([]string(nil), msg.preferences.Default...)
		if msg.preferences.DefaultSet != "" {
			m.scopeName = msg.preferences.DefaultSet
		}
		if cached := m.inventories[oldKey]; cached != nil && m.inventories[m.scopeKey()] == nil {
			m.inventories[m.scopeKey()] = cached
		}
		if m.states[installedView].scopeKey == oldKey {
			m.states[installedView].scopeKey = m.scopeKey()
		}
	}
	m.attachDiscover()
	return nil
}

func (m *Model) applyScope(ids []string, name string) tea.Cmd {
	m.filtering = false
	m.input.Blur()
	m.managerIDs = uniqueIDs(ids)
	m.managerFilter = ""
	if len(m.managerIDs) == 1 {
		m.managerFilter = m.managerIDs[0]
	}
	m.scopeName = name
	m.scopeChanged = true
	m.managerCursor = 0
	m.managerFocus = false
	m.modal = noModal
	m.modalOffset = 0
	m.status = "Provider selection applies to this session."
	for i := range m.states {
		m.cancelView(viewID(i))
		m.reconcile(viewID(i), false)
	}
	m.attachDiscover()
	return m.loadView(m.view)
}

func (m *Model) openProviders() tea.Cmd {
	m.providerPicker = providerPicker{selected: m.effectiveManagers()}
	m.modal = providersModal
	m.modalOffset = 0
	m.status = ""
	if !m.prefsLoaded {
		return m.loadPreferences()
	}
	return nil
}

func (m *Model) providerChoices() []providerChoice {
	choices := []providerChoice{{id: "default", label: "Configured default", kind: "preset", members: m.preferences.Default}}
	groupNames := make([]string, 0, len(m.preferences.Groups))
	for name := range m.preferences.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		choices = append(choices, providerChoice{id: "group:" + name, label: "Group · " + name, kind: "preset", members: m.preferences.Groups[name]})
	}
	setNames := make([]string, 0, len(m.preferences.Sets))
	for name := range m.preferences.Sets {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		choices = append(choices, providerChoice{id: "set:" + name, label: "Saved set · " + name, kind: "preset", members: m.preferences.Sets[name]})
	}
	byID := make(map[string]domain.Manager)
	for _, manager := range m.managers {
		byID[manager.ID] = manager
	}
	ids := append([]string(nil), m.providerPicker.selected...)
	for _, manager := range m.managers {
		if !slices.Contains(ids, manager.ID) {
			ids = append(ids, manager.ID)
		}
	}
	for _, id := range ids {
		manager, known := byID[id]
		label := id
		if known {
			label += " · " + manager.Status
			if manager.Scope != "" {
				label += " · " + manager.Scope
			}
		}
		choices = append(choices, providerChoice{id: id, label: label, kind: "manager", members: []string{id}})
	}
	return choices
}

func (m *Model) toggleProvider() {
	choices := m.providerChoices()
	if len(choices) == 0 {
		return
	}
	m.providerPicker.position = clamp(m.providerPicker.position, 0, len(choices)-1)
	choice := choices[m.providerPicker.position]
	if choice.kind == "preset" {
		m.providerPicker.selected = uniqueIDs(choice.members)
	} else {
		index := slices.Index(m.providerPicker.selected, choice.id)
		if index < 0 {
			m.providerPicker.selected = append(m.providerPicker.selected, choice.id)
		} else {
			m.providerPicker.selected = slices.Delete(m.providerPicker.selected, index, index+1)
		}
	}
	m.selectProviderChoice(choice.id)
}
func (m *Model) selectProviderChoice(id string) {
	for i, c := range m.providerChoices() {
		if c.id == id {
			m.providerPicker.position = i
			return
		}
	}
	m.providerPicker.position = 0
}
func (m *Model) reorderProvider(delta int) {
	choices := m.providerChoices()
	if len(choices) == 0 {
		return
	}
	choice := choices[clamp(m.providerPicker.position, 0, len(choices)-1)]
	i := slices.Index(m.providerPicker.selected, choice.id)
	j := i + delta
	if i < 0 || j < 0 || j >= len(m.providerPicker.selected) {
		return
	}
	m.providerPicker.selected[i], m.providerPicker.selected[j] = m.providerPicker.selected[j], m.providerPicker.selected[i]
	m.selectProviderChoice(choice.id)
}

func (m *Model) applyPicker() tea.Cmd {
	if len(m.providerPicker.selected) == 0 {
		m.status = "Select at least one provider before applying."
		return nil
	}
	return m.applyScope(m.providerPicker.selected, "Custom selection")
}
func (m *Model) startSaveSet() tea.Cmd {
	if len(m.providerPicker.selected) == 0 {
		m.status = "Select at least one provider before saving."
		return nil
	}
	m.modal = saveSetModal
	m.providerPicker.saveDefault = false
	m.providerPicker.err = nil
	m.input.SetValue("")
	m.input.Prompt = "Name: "
	m.input.Placeholder = "A name for this ordered set"
	return m.input.Focus()
}
func (m *Model) providersKey(key tea.KeyPressMsg) tea.Cmd {
	choices := m.providerChoices()
	switch key.String() {
	case "esc", "ctrl+c":
		m.modal = noModal
	case "up", "k":
		m.providerPicker.position = clamp(m.providerPicker.position-1, 0, len(choices)-1)
	case "down", "j":
		m.providerPicker.position = clamp(m.providerPicker.position+1, 0, len(choices)-1)
	case "pgup":
		m.providerPicker.position = clamp(m.providerPicker.position-m.pageSize(), 0, len(choices)-1)
	case "pgdown":
		m.providerPicker.position = clamp(m.providerPicker.position+m.pageSize(), 0, len(choices)-1)
	case "home", "g":
		m.providerPicker.position = 0
	case "end", "G":
		m.providerPicker.position = max(0, len(choices)-1)
	case "space", " ":
		m.toggleProvider()
	case "[":
		m.reorderProvider(-1)
	case "]":
		m.reorderProvider(1)
	case "enter":
		return m.applyPicker()
	case "S":
		return m.startSaveSet()
	}
	return nil
}
func (m *Model) saveSetInput(message tea.Msg) tea.Cmd {
	if m.providerPicker.saving {
		return nil
	}
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.modal = providersModal
			m.input.Blur()
			return nil
		case "tab", "shift+tab":
			m.providerPicker.saveDefault = !m.providerPicker.saveDefault
			return nil
		case "enter", "ctrl+s":
			return m.saveSet()
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(message)
	return cmd
}
func (m *Model) saveSet() tea.Cmd {
	if m.providerPicker.saving {
		return nil
	}
	name := strings.TrimSpace(m.input.Value())
	if name == "" {
		m.status = "Enter a set name."
		return nil
	}
	ctx, service := m.ctx, m.service
	ids := append([]string(nil), m.providerPicker.selected...)
	asDefault := m.providerPicker.saveDefault
	m.setGeneration++
	// Ignore any older preferences read after this explicit save.
	if m.prefsCancel != nil {
		m.prefsCancel()
	}
	m.prefsGeneration++
	generation := m.setGeneration
	m.providerPicker.saving = true
	m.status = "Saving ordered set…"
	return func() tea.Msg {
		prefs, err := service.SaveManagerSet(ctx, name, ids, asDefault)
		return savedSetMsg{generation, prefs, name, err}
	}
}
func (m *Model) acceptSavedSet(msg savedSetMsg) {
	if msg.generation != m.setGeneration {
		return
	}
	m.providerPicker.saving = false
	m.providerPicker.err = msg.err
	if msg.err != nil {
		m.status = "Save failed: " + msg.err.Error()
		return
	}
	m.preferences = msg.preferences
	m.prefsLoaded = true
	m.modal = providersModal
	m.input.Blur()
	m.status = fmt.Sprintf("Saved %q. Apply to use this selection now.", msg.name)
}

func uniqueIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
