package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func updatesFixture(t *testing.T, width int) *Model {
	t.Helper()
	m, _ := readyModel(t)
	m.view = updatesView
	m.states[updatesView] = viewState{loaded: true, snapshot: domain.Snapshot{Packages: []domain.Package{
		{Manager: "brew", ID: "ada-url", Version: "4.0.0", Latest: "4.0.0_1", Scope: "global"},
		{Manager: "brew", ID: "antigravity", Version: "1.21.9,4905428782613504", Latest: "1.22.0,5108899221737472", Scope: "global"},
		{Manager: "mise", ID: "bundler", Version: "2.6.9", Latest: "4.0.23", Scope: "global"},
	}}}
	m.reconcile(updatesView, false)
	m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	return m
}

func TestUpdatesViewShowsFullVersionsAndKinds(t *testing.T) {
	content := ansi.Strip(updatesFixture(t, 200).View().Content)
	for _, want := range []string{"1.21.9,4905428782613504 → 1.22.0,5108899221737472", "MAJOR", "minor", "rev", "1 major · 1 minor · 1 revision"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in:\n%s", want, content)
		}
	}
	arrows := map[int]bool{}
	for _, l := range strings.Split(content, "\n") {
		if i := strings.Index(l, " → "); i >= 0 && (strings.Contains(l, "ada-url") || strings.Contains(l, "bundler")) {
			arrows[ansi.StringWidth(l[:i])] = true
		}
	}
	if len(arrows) != 1 {
		t.Fatalf("arrows not aligned: %v\n%s", arrows, content)
	}
}

func TestUpdatesViewFitsNarrowTerminal(t *testing.T) {
	m := updatesFixture(t, 80)
	for _, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("line width %d exceeds terminal: %q", w, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "bundler") {
		t.Fatal("label column collapsed")
	}
}

func TestUpdatesFilterMatchesKind(t *testing.T) {
	m := updatesFixture(t, 200)
	m.states[updatesView].query = "major"
	rows := m.rows(updatesView)
	if len(rows) != 1 || rows[0].label != "bundler" {
		t.Fatalf("rows = %v", rows)
	}
}
