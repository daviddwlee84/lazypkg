package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestVimPagingUsesViewportAndClamps(t *testing.T) {
	for _, height := range []int{24, 16} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			m, _ := readyModel(t)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
			s := &m.states[installedView]
			s.snapshot.Packages = nil
			for i := 0; i < 100; i++ {
				s.snapshot.Packages = append(s.snapshot.Packages, domain.Package{Manager: "brew", ID: fmt.Sprint(i), Version: "1"})
			}
			m.reconcile(installedView, true)
			full := height - 11
			half := max(1, full/2)
			press(m, "ctrl+d")
			if s.position != half {
				t.Fatalf("half page=%d want %d", s.position, half)
			}
			press(m, "ctrl+u")
			if s.position != 0 {
				t.Fatal("half page up")
			}
			press(m, "ctrl+f")
			press(m, "pgdown")
			if s.position != full*2 {
				t.Fatal("full page aliases")
			}
			press(m, "ctrl+b")
			press(m, "pgup")
			if s.position != 0 {
				t.Fatal("full page back")
			}
			press(m, "G")
			press(m, "ctrl+f")
			if s.position != 99 {
				t.Fatal("end clamp")
			}
		})
	}
}
func TestModalPagingUsesContentHeightAndReturnsFromEnd(t *testing.T) {
	m, _ := readyModel(t)
	m.modal = helpModal
	press(m, "ctrl+d")
	if m.modalOffset != 8 {
		t.Fatalf("text modal half page=%d", m.modalOffset)
	}
	press(m, "G")
	limit := m.modalScrollLimit()
	if m.modalOffset != limit {
		t.Fatal("G did not store clamped offset")
	}
	press(m, "ctrl+u")
	if m.modalOffset != max(0, limit-8) {
		t.Fatal("half up stuck after G")
	}
	m.modalOffset = 1 << 30
	press(m, "up")
	if m.modalOffset != max(0, limit-1) {
		t.Fatal("existing excess offset was not normalized")
	}
	m.modal = setupModal
	for i := 0; i < 40; i++ {
		m.setup.options = append(m.setup.options, domain.SetupOption{ID: fmt.Sprint(i)})
	}
	press(m, "ctrl+d")
	if m.setup.position != 6 {
		t.Fatal("setup half page uses wrong viewport")
	}
	press(m, "pgdown")
	if m.setup.position != 18 {
		t.Fatal("setup full page changed only unused text offset")
	}
	m.modal = providersModal
	m.showAllManagers = true
	for i := 0; i < 40; i++ {
		m.managers = append(m.managers, domain.Manager{ID: fmt.Sprint(i)})
	}
	press(m, "ctrl+d")
	if m.providerPicker.position != 7 {
		t.Fatal("provider picker half page")
	}
}
func TestPagingKeysRemainTextEditingInEveryInput(t *testing.T) {
	for _, modal := range []modalKind{noModal, versionModal, saveSetModal, exportPromptModal} {
		m, _ := readyModel(t)
		m.modal = modal
		m.filtering = modal == noModal
		m.input.Focus()
		m.input.SetValue("abcd")
		m.input.CursorEnd()
		press(m, "ctrl+b")
		press(m, "ctrl+d")
		if m.input.Value() != "abc" {
			t.Fatalf("%d ctrl+d did not delete next character: %q", modal, m.input.Value())
		}
		press(m, "ctrl+u")
		if m.input.Value() != "" || m.modalOffset != 0 || m.states[installedView].position != 0 {
			t.Fatal("editing dispatched paging")
		}
	}
}
func TestWorkflowContextAndWheelNormalizeOffsets(t *testing.T) {
	m, _ := workflowFixture(t)
	m.modal = resolutionModal
	m.workflow.assessment.Installations = append(m.workflow.assessment.Installations, domain.ConflictInstallation{ID: "a", Warnings: []string{strings.Repeat("long detail ", 200)}})
	m.workflow.selectedID = "a"
	full := m.pageRows()
	press(m, "ctrl+d")
	if m.modalOffset != max(1, full/2) {
		t.Fatal("workflow half context page")
	}
	m.modalOffset = 1 << 30
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp}) // List wheel remains list navigation.
	m.modal = helpModal
	m.modalOffset = 1 << 30
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.modalOffset != max(0, m.modalScrollLimit()-3) {
		t.Fatal("wheel cannot scroll back from normalized end")
	}
}
