package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type batchService struct {
	*fakeService
	batches []domain.BatchUpgradeRequest
}

func (f *batchService) PlanBatchUpgrade(ctx context.Context, request domain.BatchUpgradeRequest) (domain.BatchUpgradePlan, error) {
	f.batches = append(f.batches, domain.BatchUpgradeRequest{Targets: clonePackages(request.Targets), Source: request.Source})
	return f.fakeService.PlanBatchUpgrade(ctx, request)
}
func batchFixture(t *testing.T) (*Model, *batchService) {
	m, f := readyModel(t)
	service := &batchService{fakeService: f}
	m.service = service
	for i := range m.states[installedView].snapshot.Packages {
		p := &m.states[installedView].snapshot.Packages[i]
		p.Scope = "global"
		p.Instance = "/fixture/" + p.Manager
	}
	m.states[installedView].snapshot.Coverage = []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: time.Now()}, {Manager: "mise", State: "complete", ObservedAt: time.Now()}}
	m.reconcile(installedView, false)
	return m, service
}
func TestMarksPersistAcrossFilterAndSelectedBatchIncludesHidden(t *testing.T) {
	m, f := batchFixture(t)
	press(m, " ")
	press(m, "j")
	press(m, " ")
	press(m, "/")
	press(m, "a")
	press(m, "l")
	press(m, "p")
	press(m, "enter")
	total, hidden := m.markCounts(installedView)
	if total != 2 || hidden != 1 {
		t.Fatalf("marks lost across filter: %d/%d", total, hidden)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "2 selected (1 hidden)") {
		t.Fatal("hidden marks not disclosed")
	}
	cmd := press(m, "u")
	// The request owns deep copies before a late stream can change visible rows.
	m.states[installedView].snapshot.Packages[0].ID = "changed-after-freeze"
	deliver(m, cmd)
	if m.modal != batchReviewModal || len(f.batches) != 1 || len(f.batches[0].Targets) != 2 || f.batches[0].Targets[0].ID != "alpha" {
		t.Fatalf("not a frozen marked batch: %#v", f.batches)
	}
	if !strings.Contains(m.batchReviewText(), "1 selected targets are hidden") {
		t.Fatal("review hides hidden target count")
	}
	if press(m, "enter") != nil || f.executeCalls != 0 {
		t.Fatal("Enter approved aggregate review")
	}
	press(m, "esc")
	if f.executeCalls != 0 {
		t.Fatal("cancel executed batch")
	}
}
func TestVisibleBatchAndControlAUseAllFilteredRows(t *testing.T) {
	m, f := batchFixture(t)
	s := &m.states[installedView]
	for i := 0; i < 35; i++ {
		s.snapshot.Packages = append(s.snapshot.Packages, domain.Package{Manager: "brew", ID: strings.Repeat("a", i+1), Version: "1", Scope: "global"})
	}
	m.reconcile(installedView, false)
	press(m, "ctrl+a")
	if len(s.marks) != 38 {
		t.Fatalf("Ctrl+A selected only viewport: %d", len(s.marks))
	}
	press(m, "ctrl+a")
	if len(s.marks) != 0 {
		t.Fatal("Ctrl+A did not toggle off")
	}
	press(m, " ")
	s.query = "bravo"
	m.reconcile(installedView, true)
	deliver(m, press(m, "U"))
	if len(f.batches) != 1 || len(f.batches[0].Targets) != 1 || f.batches[0].Targets[0].ID != "bravo" || f.batches[0].Source != "visible" {
		t.Fatal("filtered batch included hidden marks")
	}
}
func TestMarksStayPerViewAndNeverRebindVersionOrInstance(t *testing.T) {
	m, _ := batchFixture(t)
	press(m, " ")
	key := m.states[installedView].markOrder[0]
	m.view = updatesView
	m.states[updatesView].snapshot = domain.CloneSnapshot(m.states[installedView].snapshot)
	m.states[updatesView].loaded = true
	m.reconcile(updatesView, false)
	if len(m.states[updatesView].marks) != 0 {
		t.Fatal("marks leaked between views")
	}
	m.view = installedView
	m.states[installedView].snapshot.Packages[0].Root = "enriched-root"
	m.reconcile(installedView, false)
	if _, ok := m.states[installedView].marks[key]; !ok {
		t.Fatal("enrichment lost identity")
	}
	m.states[installedView].snapshot.Packages[0].Version = "99"
	m.reconcile(installedView, false)
	if len(m.states[installedView].marks) != 0 {
		t.Fatal("version change rebound old selection")
	}
	press(m, " ")
	m.states[installedView].snapshot.Packages[0].Instance = "new-instance"
	m.reconcile(installedView, false)
	if len(m.states[installedView].marks) != 0 {
		t.Fatal("instance change rebound mark")
	}
	press(m, " ")
	m.applyScope([]string{"brew"}, "brew")
	if len(m.states[installedView].marks) != 0 {
		t.Fatal("provider scope did not clear marks")
	}
}
func TestMarksExcludeStaleUnsupportedAndPendingActivation(t *testing.T) {
	m, _ := batchFixture(t)
	s := &m.states[installedView]
	s.snapshot.Packages[0].InventoryStale = true
	s.snapshot.Packages[2].LatestInstalled = true
	press(m, "ctrl+a")
	if len(s.marks) != 1 || s.marks[s.markOrder[0]].ID != "bravo" {
		t.Fatal("ineligible rows selected")
	}
	s.marks = nil
	s.markOrder = nil
	s.snapshot.Packages[0].InventoryStale = false
	s.snapshot.Coverage[0].ObservedAt = time.Now().Add(-time.Minute * 2)
	press(m, "ctrl+a")
	if len(s.marks) != 0 {
		t.Fatal("expired observation enabled marking")
	}
}
func TestBatchPauseRecheckAndSkipAlwaysNeedAnotherReview(t *testing.T) {
	m, f := batchFixture(t)
	press(m, "ctrl+a")
	deliver(m, press(m, "u"))
	plan := m.batch.plan
	generation := m.batch.generation
	m.Update(batchExecutedMsg{generation: generation, result: domain.BatchUpgradeResult{Paused: true, Message: "Paused after second item", Entries: []domain.BatchUpgradeItemResult{{Entry: plan.Entries[0], State: "success"}, {Entry: plan.Entries[1], State: "failed"}}, Remaining: []domain.Package{plan.Entries[1].Package, plan.Entries[2].Package}}, err: errors.New("provider failed")})
	if m.modal != batchResultModal || len(m.batch.history) != 2 || f.executeCalls != 0 || len(m.lastResult.Steps) != 2 {
		t.Fatal("paused results missing or automatically resumed")
	}
	deliver(m, press(m, "r"))
	if m.modal != batchReviewModal || len(f.batches) != 2 || len(f.batches[1].Targets) != 2 {
		t.Fatal("recheck did not prepare new frozen review")
	}
	if press(m, "enter") != nil || f.executeCalls != 0 {
		t.Fatal("recheck/Enter implicitly approved")
	}
	// A second paused result can be skipped without replaying the successful item.
	m.Update(batchExecutedMsg{generation: m.batch.generation, result: domain.BatchUpgradeResult{Paused: true, Remaining: f.batches[1].Targets}})
	deliver(m, press(m, "s"))
	if len(f.batches) != 3 || len(f.batches[2].Targets) != 1 || f.batches[2].Targets[0].ID != "node" {
		t.Fatal("skip targeted wrong paused group")
	}
	press(m, "esc")
	if m.modal != noModal || f.executeCalls != 0 {
		t.Fatal("stop continued batch")
	}
}
func TestBatchLatePlansAndMouseCheckboxInvalidation(t *testing.T) {
	m, _ := batchFixture(t)
	first := m.rows(installedView)[0].key
	hit := target(t, m, "mark", first)
	m.Update(tea.MouseClickMsg{X: hit.rect.x, Y: hit.rect.y, Button: tea.MouseLeft})
	m.Update(tea.WindowSizeMsg{Width: 81, Height: 24})
	m.Update(tea.MouseReleaseMsg{X: hit.rect.x, Y: hit.rect.y, Button: tea.MouseLeft})
	if len(m.states[installedView].marks) != 0 {
		t.Fatal("stale checkbox press survived resize")
	}
	mouseClick(m, target(t, m, "mark", first))
	if len(m.states[installedView].marks) != 1 {
		t.Fatal("checkbox did not mark")
	}
	command := press(m, "u")
	press(m, "esc")
	deliver(m, command)
	if m.modal != noModal || m.executing {
		t.Fatal("cancelled plan reopened or executed")
	}
}

func TestBatchRecheckRefreshesVersionsOnlyWithinTheSelectedInstance(t *testing.T) {
	m, f := batchFixture(t)
	p := m.states[installedView].snapshot.Packages[0]
	request := domain.BatchUpgradeRequest{Targets: []domain.Package{p}, Source: "resume"}
	m.refreshBatch(request)
	generation := m.batch.generation
	fresh := p
	fresh.Version = "2"
	snapshot := domain.Snapshot{Packages: []domain.Package{fresh}, Coverage: []domain.Coverage{{Manager: p.Manager, Instance: p.Instance, State: "complete", ObservedAt: time.Now()}}}
	deliver(m, m.acceptBatchRefresh(batchRefreshMsg{generation: generation, request: request, snapshot: snapshot}))
	if f.batches[0].Targets[0].Version != "2" || m.modal != batchReviewModal || f.executeCalls != 0 {
		t.Fatal("explicit recheck did not review fresh version separately")
	}
	m.refreshBatch(request)
	fresh.Instance = "/different/manager"
	snapshot.Packages = []domain.Package{fresh}
	snapshot.Coverage[0].Instance = fresh.Instance
	deliver(m, m.acceptBatchRefresh(batchRefreshMsg{generation: m.batch.generation, request: request, snapshot: snapshot}))
	if got := f.batches[1].Targets[0]; got.Instance != p.Instance || got.Version != p.Version {
		t.Fatal("recheck silently rebound to another manager instance")
	}
}
func TestReapplyingSameProviderScopePreservesMarks(t *testing.T) {
	m, _ := batchFixture(t)
	m.managerIDs = []string{"brew", "mise"}
	press(m, " ")
	m.applyScope([]string{"brew", "mise"}, "same sources")
	if len(m.states[installedView].marks) != 1 {
		t.Fatal("unchanged provider scope unexpectedly cleared selection")
	}
}
func TestBatchViewsFitAndHaveOnlyVisibleApprovalTargets(t *testing.T) {
	m, _ := batchFixture(t)
	press(m, "ctrl+a")
	deliver(m, press(m, "u"))
	for _, size := range [][2]int{{80, 24}, {48, 16}, {35, 9}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		text := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(text, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("batch overview exceeds width")
			}
		}
		if len(strings.Split(text, "\n")) > size[1] {
			t.Fatal("batch overview exceeds height")
		}
		if size[0] < 40 {
			for _, hit := range m.layout().targets {
				if hit.kind == "key" && hit.value == "y" {
					t.Fatal("tiny viewport still offers approval")
				}
			}
			if press(m, "y") != nil {
				t.Fatal("tiny viewport approved unseen batch")
			}
		}
	}
}
