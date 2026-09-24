package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestCoverageDeduplicatesProviderExplanation(t *testing.T) {
	m, _ := readyModel(t)
	message := "does not support outdated"
	m.states[installedView].snapshot = domain.Snapshot{Coverage: []domain.Coverage{{Manager: "cargo", State: "unsupported", Message: message}, {Manager: "cargo", State: "unsupported", Message: message}}, Issues: []domain.Issue{{Manager: "cargo", Kind: "unsupported", Message: message}}}
	m.managers = append(m.managers, domain.Manager{ID: "cargo", Errors: []string{message}})
	if count := strings.Count(m.issuesText(), message); count != 1 {
		t.Fatalf("same explanation repeated %d times", count)
	}
	text := m.emptyText()
	if strings.Contains(text, "could not be queried") || !strings.Contains(text, "unsupported") {
		t.Fatal("excluded coverage was called a failed query")
	}
	m.states[installedView].snapshot.Coverage[0].State = "failed"
	m.states[installedView].snapshot.Coverage[0].Message = "network failed"
	m.states[installedView].snapshot.Issues = nil
	if !strings.Contains(m.emptyText(), "not a complete empty inventory") {
		t.Fatal("failed coverage implied successful empty")
	}
}
func TestGHExtensionLabelsAndUpgradeExclusion(t *testing.T) {
	m, _ := readyModel(t)
	p := domain.Package{Manager: "gh-ext", ID: "owner/gh-tool", Version: "v1", Scope: "global", Extension: &domain.GHExtension{Kind: "git", Status: "available", FullVersion: "abcdef123", Launcher: "/bin/gh", Pinned: true, BlockedReason: "Local changes"}}
	m.managers = append(m.managers, domain.Manager{ID: "gh-ext", Available: true, Scope: "global", Capabilities: []string{"installed", "upgrade"}})
	m.states[installedView].snapshot = domain.Snapshot{Packages: []domain.Package{p}, Coverage: []domain.Coverage{{Manager: "gh-ext", State: "complete", ObservedAt: time.Now()}}}
	m.reconcile(installedView, true)
	row, _ := m.selectedRow()
	text := m.rowDetails(row)
	for _, want := range []string{"Provider: gh ext", "gh extension", "abcdef123", "Local changes", "Pinned"} {
		if !strings.Contains(text, want) {
			t.Fatalf("extension detail lacks %q", want)
		}
	}
	press(m, " ")
	if len(m.states[installedView].marks) != 0 {
		t.Fatal("pinned extension was marked for batch upgrade")
	}
}
