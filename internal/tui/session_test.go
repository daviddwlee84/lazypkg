package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func countQueries(f *fakeService, kind string) int {
	count := 0
	for _, q := range f.queryRequests {
		if q.Kind == kind {
			count++
		}
	}
	return count
}
func TestStartupWarmsUpdatesOnceWithoutChangingForeground(t *testing.T) {
	m, f := batchFixture(t)
	f.snapshot = domain.CloneSnapshot(m.states[installedView].snapshot)
	m = New(context.Background(), f, "installed")
	defer m.cancelAll()
	deliver(m, m.Init())
	if m.view != installedView || countQueries(f.fakeService, "installed") != 1 || countQueries(f.fakeService, "outdated") != 1 {
		t.Fatalf("startup reads or focus: %v %#v", m.view, f.queryRequests)
	}
	for _, q := range f.queryRequests {
		if q.CachePolicy != domain.CacheSession {
			t.Fatal("browse read bypassed session policy")
		}
	}
	deliver(m, m.switchView(int(updatesView)))
	deliver(m, m.switchView(int(installedView)))
	deliver(m, m.switchView(int(updatesView)))
	if countQueries(f.fakeService, "outdated") != 1 {
		t.Fatal("switching to warmed Updates caused another query")
	}
	deliver(m, press(m, "r"))
	if countQueries(f.fakeService, "outdated") != 2 || !f.queryRequests[len(f.queryRequests)-1].Refresh {
		t.Fatal("manual r did not force refresh")
	}
}
func TestWarmupWaitsForInstalledBaseAndForegroundRequestWins(t *testing.T) {
	m, _ := batchFixture(t)
	m.warmUpdatesPending = true
	m.prefsLoaded = true
	event := baseEvent("brew", "cache", domain.Package{Manager: "brew", ID: "alpha"})
	if m.installedProgress(event, false) != nil || m.states[updatesView].attempted {
		t.Fatal("disk seed launched update check before native base")
	}
	m.loadView(updatesView)
	event.Stage = "base"
	if m.installedProgress(event, false) != nil {
		t.Fatal("foreground Updates query was duplicated by warmup")
	}
	m2, _ := batchFixture(t)
	m2.warmUpdatesPending = true
	m2.prefsLoaded = true
	if m2.installedProgress(domain.QueryEvent{}, true) == nil {
		t.Fatal("empty/failed Installed termination did not unblock warmup")
	}
}
func TestFailedAndCancelledSessionReadsRequireManualRetry(t *testing.T) {
	m, _ := batchFixture(t)
	m.view = updatesView
	stream := streamFixture(m, updatesView)
	applyEvent(m, stream, domain.QueryEvent{Stage: "done", Err: errors.New("offline")})
	m.switchView(int(installedView))
	if m.switchView(int(updatesView)) != nil {
		t.Fatal("failed query retried on tab entry")
	}
	m.loadView(updatesView)
	press(m, "esc")
	m.switchView(int(installedView))
	if m.switchView(int(updatesView)) != nil {
		t.Fatal("cancelled read restarted on tab entry")
	}
	if press(m, "r") == nil {
		t.Fatal("manual retry missing")
	}
}
func TestMutationRefreshesCurrentAndUpdatesAndRejectsOldWarmResult(t *testing.T) {
	m, f := batchFixture(t)
	f.snapshot = domain.CloneSnapshot(m.states[installedView].snapshot)
	old := streamFixture(m, updatesView)
	generation := m.planGeneration
	_, cmd := m.Update(executedMsg{generation: generation, result: domain.ActionResult{Message: "done"}})
	deliver(m, cmd)
	if countQueries(f.fakeService, "installed") != 1 || countQueries(f.fakeService, "outdated") != 1 {
		t.Fatal("mutation did not refresh both observations")
	}
	applyEvent(m, old, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "old"}))
	for _, p := range m.states[updatesView].snapshot.Packages {
		if p.ID == "old" {
			t.Fatal("late warm result survived mutation invalidation")
		}
	}
}
func TestCleanMarkersAndUnknownMetadataStaySelectable(t *testing.T) {
	m, _ := batchFixture(t)
	s := &m.states[installedView]
	s.snapshot.Packages[0].Version = ""
	s.snapshot.Packages[0].InventoryStale = true
	s.snapshot.Packages[0].Identity = &domain.PackageIdentity{State: "unresolved", Reason: "Identity will be verified"}
	s.snapshot.Coverage[0].ObservedAt = time.Now().Add(-48 * time.Hour)
	m.reconcile(installedView, false)
	text := ansi.Strip(m.View().Content)
	if strings.Contains(text, "[ ]") || strings.Contains(text, "[-]") || strings.Contains(text, "[x]") {
		t.Fatal("package rows still show boxes")
	}
	press(m, " ")
	if len(s.marks) != 1 || !strings.Contains(ansi.Strip(m.View().Content), "✓") {
		t.Fatal("unknown old observation cannot be selected")
	}
	p := s.snapshot.Packages[1]
	p.ID = "meta-package-manager"
	s.snapshot.Packages[1] = p
	s.marks = nil
	s.markOrder = nil
	m.move(1)
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "!") {
		t.Fatal("known blocker has no icon")
	}
	if hasAction(m, "upgrade") || hasAction(m, "remove") {
		t.Fatal("protected backend advertises write actions")
	}
}
func TestProtectedIdentityAndLiveEligibilityRemainStrict(t *testing.T) {
	p := domain.Package{Manager: "brew", ID: "owner/tap/tool", Name: "meta-package-manager", Scope: "global"}
	manager := domain.Manager{ID: "brew", Available: true, Scope: "global", Capabilities: []string{"upgrade", "remove"}}
	if domain.ProtectedBackendPackage(p) {
		t.Fatal("untrusted display name was treated as backend identity")
	}
	p.Identity = &domain.PackageIdentity{State: "verified", Name: "meta-package-manager"}
	if !domain.ProtectedBackendPackage(p) {
		t.Fatal("verified canonical backend identity was ignored")
	}
	p.ID = "jq"
	p.Identity = nil
	p.InventoryStale = true
	if domain.BatchUpgradeBlocker(p, manager) != "" {
		t.Fatal("uncertain metadata blocked selection")
	}
	if ok, _ := domain.BatchUpgradeEligibility(p, manager, nil); ok {
		t.Fatal("selection eligibility authorized a live write")
	}
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	p.InventoryStale = false
	p.Version = "1"
	coverage := []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: now}}
	if ok, _ := domain.BatchUpgradeEligibilityAt(p, manager, coverage, now); ok {
		t.Fatal("unverified Homebrew identity allowed execution")
	}
	p.Identity = &domain.PackageIdentity{State: "verified", Name: "jq"}
	if ok, _ := domain.BatchUpgradeEligibilityAt(p, manager, coverage, now); !ok {
		t.Fatal("fresh verified entry was rejected")
	}
	if ok, _ := domain.BatchUpgradeEligibilityAt(p, manager, coverage, now.Add(time.Hour)); ok {
		t.Fatal("execution freshness was weakened")
	}
}
func TestPausedResultReopensAfterCancelledNewDraft(t *testing.T) {
	m, f := batchFixture(t)
	press(m, "ctrl+a")
	deliver(m, press(m, "u"))
	entry := m.batch.plan.Entries[0]
	m.Update(batchExecutedMsg{generation: m.batch.generation, result: domain.BatchUpgradeResult{Paused: true, Message: "Batch paused", Remaining: []domain.Package{entry.Package}, Entries: []domain.BatchUpgradeItemResult{{Entry: entry, State: "failed"}}}, err: errors.New("failure")})
	press(m, "esc")
	m.switchView(int(managersView))
	if strings.Contains(ansi.Strip(m.View().Content), "Batch paused") {
		t.Fatal("paused message leaked onto another tab")
	}
	press(m, "v")
	if m.modal != batchResultModal || len(m.batch.result.Remaining) != 1 {
		t.Fatal("v did not reopen resumable result")
	}
	press(m, "esc")
	m.view = installedView
	m.startBatchUpgrade("visible")
	press(m, "esc")
	press(m, "v")
	if m.modal != batchResultModal || m.batch.result.Message != "Batch paused" || len(m.batch.history) != 1 || f.executeCalls != 0 {
		t.Fatal("cancelled draft destroyed executed batch result")
	}
}
func TestCurrentAndExcludedVersionSummary(t *testing.T) {
	current := batchVersionText(domain.BatchUpgradeEntry{State: "current", Package: domain.Package{Version: "2"}})
	excluded := batchVersionText(domain.BatchUpgradeEntry{State: "excluded", Package: domain.Package{Version: "3"}, Targets: []domain.Package{{Version: "1"}}})
	if strings.Contains(current, "→") || !strings.Contains(current, "2") || !strings.Contains(current, "no available upgrade") {
		t.Fatal(current)
	}
	if strings.Contains(excluded, "→") || !strings.Contains(excluded, "Selected: 1") {
		t.Fatal(excluded)
	}
}

func TestForegroundInventoryCompletionSatisfiesMutationInvalidation(t *testing.T) {
	m, f := batchFixture(t)
	f.snapshot = domain.CloneSnapshot(m.states[installedView].snapshot)
	key := m.scopeKey()
	m.inventories[key] = &inventoryState{loaded: true, needsReload: true, stale: true}
	deliver(m, m.loadView(installedView))
	if m.inventories[key].needsReload || m.ensureInventory(false) != nil {
		t.Fatal("fresh foreground inventory left a redundant background reload")
	}
}
func TestCancelledForegroundDoesNotLeavePhantomInventoryRead(t *testing.T) {
	m, _ := batchFixture(t)
	stream := streamFixture(m, installedView)
	applyEvent(m, stream, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "alpha", Version: "1"}))
	m.cancelView(installedView)
	cached := m.inventories[m.scopeKey()]
	if cached == nil || cached.loading || !cached.attempted {
		t.Fatal("cancelled foreground left background inventory marked running")
	}
	if m.ensureInventory(false) != nil {
		t.Fatal("cancelled shared read restarted without r")
	}
}

func TestWarmupFollowsNewScopeAndIgnoresOtherInventoryReads(t *testing.T) {
	m, _ := batchFixture(t)
	m.warmUpdatesPending = true
	m.prefsLoaded = true
	oldKey := m.scopeKey()
	m.inventories[oldKey] = &inventoryState{loading: true, generation: 1}
	old := streamStartedMsg{ctx: context.Background(), target: streamTarget{inventoryKey: oldKey, generation: 1}}
	m.applyScope([]string{"brew"}, "brew")
	applyEvent(m, old, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "old-scope"}))
	if m.states[updatesView].attempted {
		t.Fatal("old-scope inventory stole current startup warmup")
	}
	current := streamStartedMsg{ctx: context.Background(), target: streamTarget{view: installedView, generation: m.states[installedView].generation}}
	applyEvent(m, current, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "new-scope"}))
	if !m.states[updatesView].attempted || m.states[updatesView].scopeKey != m.scopeKey() {
		t.Fatal("warmup did not follow the explicitly chosen initial scope")
	}
}

func TestSelectedRestrictionPrecedesUnrelatedCoverageErrors(t *testing.T) {
	m, _ := batchFixture(t)
	s := &m.states[installedView]
	s.snapshot.Packages[0].ID = "meta-package-manager"
	s.snapshot.Issues = []domain.Issue{{Manager: "npm", Kind: "failed", Message: "unrelated provider failed"}}
	m.reconcile(installedView, true)
	if text := ansi.Strip(m.statusLine()); !strings.Contains(text, "active mpm backend") || strings.Contains(text, "Partial results") {
		t.Fatal("unrelated coverage hid the selected restriction", text)
	}
	m.status = "Explicit notice"
	if !strings.Contains(m.statusLine(), "Explicit notice") {
		t.Fatal("explicit notice lost priority")
	}
	press(m, "j")
	if m.status != "" || !strings.Contains(m.statusLine(), "Partial results") {
		t.Fatal("cursor move retained an obsolete notice")
	}
	m.status = "Old row notice"
	mouseClick(m, target(t, m, "row", s.snapshot.Packages[0].Key()))
	if m.status != "" || !strings.Contains(m.statusLine(), "active mpm backend") {
		t.Fatal("mouse selection retained an obsolete notice")
	}
}
