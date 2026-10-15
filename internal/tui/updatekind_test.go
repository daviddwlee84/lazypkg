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
		{Manager: "brew", ID: "zellij", Version: "0.43.1", Latest: "0.45.1", Scope: "global"},
	}}}
	m.reconcile(updatesView, false)
	m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	return m
}

func TestUpdatesViewShowsFullVersionsAndKinds(t *testing.T) {
	content := ansi.Strip(updatesFixture(t, 200).View().Content)
	for _, want := range []string{"1.21.9,4905428782613504 → 1.22.0,5108899221737472", "MAJOR", "    MINOR", "REV", "0.x MINOR", "1 major · 2 minor · 1 revision · 1 0.x"} {
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

func TestUpdatesSortAndTypeFilter(t *testing.T) {
	m := updatesFixture(t, 200)
	labels := func() string {
		var out []string
		for _, r := range m.rows(updatesView) {
			out = append(out, r.label)
		}
		return strings.Join(out, ",")
	}
	press(m, "o")
	if got := labels(); got != "bundler,antigravity,zellij,ada-url" {
		t.Fatalf("type sort = %s", got)
	}
	press(m, "o")
	if got := labels(); got != "ada-url,antigravity,zellij,bundler" {
		t.Fatalf("provider sort = %s", got)
	}
	press(m, "o")
	selected := m.states[updatesView].selected
	press(m, "t")
	if got := labels(); got != "bundler" {
		t.Fatalf("major filter = %s", got)
	}
	press(m, "t")
	press(m, "t")
	press(m, "t")
	if got := labels(); got != "zellij" {
		t.Fatalf("0.x filter = %s", got)
	}
	press(m, "t")
	press(m, "t")
	if got := labels(); got != "ada-url,antigravity,bundler,zellij" || m.states[updatesView].selected == "" {
		t.Fatalf("all filter = %s (selected %q, was %q)", got, m.states[updatesView].selected, selected)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "o sort: name · t type: all") {
		t.Fatal("sort/filter state not shown")
	}
}

func TestSelectedUpdateBadgeKeepsColour(t *testing.T) {
	m := updatesFixture(t, 200)
	m.states[updatesView].selected = m.rows(updatesView)[2].key // bundler, MAJOR
	plain := strings.Split(ansi.Strip(m.View().Content), "\n")
	raw := strings.Split(m.View().Content, "\n")
	for i, l := range plain {
		if strings.Contains(l, "bundler") && strings.Contains(l, "→") {
			if !strings.Contains(raw[i], warningStyle.Bold(true).Background(selectedBackground).Render("MAJOR ")) {
				t.Fatalf("selected badge lost its style: %q", raw[i])
			}
			return
		}
	}
	t.Fatal("bundler row not rendered")
}
