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

func streamFixture(m *Model, v viewID) streamStartedMsg {
	m.loadView(v)
	return streamStartedMsg{target: streamTarget{view: v, generation: m.states[v].generation, query: m.states[v].query}, ctx: context.Background()}
}
func baseEvent(id, kind string, packages ...domain.Package) domain.QueryEvent {
	return domain.QueryEvent{Stage: kind, Manager: id, Snapshot: domain.Snapshot{Packages: packages, Coverage: []domain.Coverage{{Manager: id, State: "complete", ObservedAt: time.Now(), Enrichment: "pending"}}, ObservedAt: time.Now()}}
}
func applyEvent(m *Model, stream streamStartedMsg, event domain.QueryEvent) {
	m.Update(streamEventMsg{streamStartedMsg: stream, event: event})
}
func hasAction(m *Model, operation string) bool {
	for _, action := range m.actions() {
		if action.operation == operation {
			return true
		}
	}
	return false
}

func TestStreamingBaseActionsDoNotWaitForOtherProvidersOrEnrichment(t *testing.T) {
	m, _ := readyModel(t)
	m.managerIDs = []string{"brew", "mise"}
	stream := streamFixture(m, installedView)
	p := domain.Package{Manager: "brew", ID: "alpha", Version: "1", Scope: "user"}
	applyEvent(m, stream, baseEvent("brew", "cache", p))
	if !strings.Contains(ansi.Strip(m.View().Content), "stale") || hasAction(m, "remove") {
		t.Fatal("disk seed was hidden or enabled mutation")
	}
	applyEvent(m, stream, baseEvent("brew", "base", p))
	if !m.states[installedView].loading || !hasAction(m, "remove") {
		t.Fatal("fresh base still waits for aggregate/enrichment")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "1/2 providers") {
		t.Fatal("incremental progress missing")
	}
	selected := m.states[installedView].selected
	p.Root = "/new/enriched/root"
	p.Commands = []string{"alpha"}
	applyEvent(m, stream, baseEvent("brew", "enriched", p))
	if m.states[installedView].selected != selected {
		t.Fatal("ownership enrichment changed package identity")
	}
	applyEvent(m, stream, baseEvent("brew", "cache", domain.Package{Manager: "brew", ID: "obsolete"}))
	if m.states[installedView].selected != selected || !hasAction(m, "remove") {
		t.Fatal("late disk seed overwrote live result")
	}
	applyEvent(m, stream, baseEvent("mise", "base"))
	if len(m.states[installedView].snapshot.Packages) != 1 {
		t.Fatal("successful empty provider retained obsolete rows")
	}
	m.Update(streamEventMsg{streamStartedMsg: stream, closed: true})
	if m.states[installedView].loading || !hasAction(m, "remove") {
		t.Fatal("channel close did not finish retaining live provider")
	}
	applyEvent(m, stream, baseEvent("brew", "enriched", domain.Package{Manager: "brew", ID: "late"}))
	if m.states[installedView].selected != selected {
		t.Fatal("late event after terminal close was accepted")
	}
}

func TestStreamingFailureRetainsOnlyFailedProviderAndRejectsOldGeneration(t *testing.T) {
	m, _ := readyModel(t)
	stream := streamFixture(m, installedView)
	applyEvent(m, stream, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "fresh", Version: "2"}))
	failure := domain.QueryEvent{Stage: "base", Manager: "mise", Snapshot: domain.Snapshot{Coverage: []domain.Coverage{{Manager: "mise", State: "failed", Message: "timeout"}}, Issues: []domain.Issue{{Manager: "mise", Message: "timeout"}}}}
	applyEvent(m, stream, failure)
	press(m, "G")
	if p, _ := m.selectedRow(); p.pkg == nil || p.pkg.Manager != "mise" || !p.pkg.InventoryStale {
		t.Fatal("failed provider did not retain stale row")
	}
	if hasAction(m, "remove") {
		t.Fatal("failed provider enabled removal")
	}
	press(m, "g")
	press(m, "g")
	if !hasAction(m, "remove") {
		t.Fatal("one failure disabled another live provider")
	}
	next := streamFixture(m, installedView)
	applyEvent(m, stream, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "old"}))
	if m.states[installedView].snapshot.Packages[0].ID == "old" {
		t.Fatal("old generation accepted")
	}
	applyEvent(m, next, domain.QueryEvent{Stage: "done", Err: errors.New("cancelled")})
	if hasAction(m, "remove") {
		t.Fatal("failed refresh re-enabled stale rows")
	}
}

func TestStreamingDiscoverInventoryJoinsBeforeAllProvidersFinish(t *testing.T) {
	m, _ := readyModel(t)
	m.view = discoverView
	m.managerIDs = []string{"brew", "mise"}
	s := &m.states[discoverView]
	s.loaded = true
	s.query = "alpha"
	s.acceptedQuery = "alpha"
	s.candidates = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "alpha", Candidate: true}, {Manager: "mise", ID: "alpha", Candidate: true}}}
	m.states[installedView].loading = false
	m.ensureInventory(true)
	cached := m.inventories[m.scopeKey()]
	stream := streamStartedMsg{target: streamTarget{inventoryKey: m.scopeKey(), generation: cached.generation}, ctx: context.Background()}
	m.attachDiscover()
	selected := s.selected
	applyEvent(m, stream, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "alpha", Version: "3"}))
	if !cached.loading || s.snapshot.Packages[0].InstallState != "installed" || s.snapshot.Packages[0].Version != "3" {
		t.Fatal("discover waits for full inventory")
	}
	if s.selected != selected || s.snapshot.Packages[1].InstallState != "checking" {
		t.Fatal("selection or pending provider state lost")
	}
}

func TestStreamingFreshMemoryCacheRemainsActionable(t *testing.T) {
	m, _ := readyModel(t)
	stream := streamFixture(m, installedView)
	event := baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "alpha", Version: "1"})
	event.Cached = true
	applyEvent(m, stream, event)
	if !hasAction(m, "remove") {
		t.Fatal("fresh memory cache incorrectly treated as disk seed")
	}
}

func TestStreamingNeverRetainsFailureFromDifferentManagerInstance(t *testing.T) {
	prior := domain.Snapshot{Packages: []domain.Package{{Manager: "npm", ID: "alpha", Instance: "prefix-A"}}, Coverage: []domain.Coverage{{Manager: "npm", Instance: "prefix-A", State: "complete"}}}
	fresh := domain.Snapshot{Coverage: []domain.Coverage{{Manager: "npm", Instance: "prefix-B", State: "failed"}}, Issues: []domain.Issue{{Manager: "npm", Message: "new prefix failed"}}}
	event := domain.QueryEvent{Stage: "base", Manager: "npm", Snapshot: fresh}
	if got := replaceProvider(prior, event); len(got.Packages) != 0 {
		t.Fatal("stream retained rows owned by a different prefix")
	}
	if got, _ := retainFailedProviders(prior, fresh); len(got.Packages) != 0 {
		t.Fatal("terminal aggregate retained different instance")
	}
	fresh.Coverage[0].State = "complete"
	fresh.Coverage[0].Instance = "prefix-A"
	if got, _ := retainFailedProviders(prior, fresh); len(got.Packages) != 0 {
		t.Fatal("complete empty batch with enrichment issue retained old package")
	}
}
func TestStreamingSearchDoesNotRetainPreviousQueryAndShowsElapsed(t *testing.T) {
	m, _ := readyModel(t)
	m.view = discoverView
	s := &m.states[discoverView]
	s.loaded = true
	s.query = "new"
	s.acceptedQuery = "old"
	s.snapshot = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "old"}}}
	stream := streamFixture(m, discoverView)
	if len(s.snapshot.Packages) > 0 {
		t.Fatal("new query carried old candidates")
	}
	event := baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "new", Candidate: true})
	event.Elapsed = 1500 * time.Millisecond
	applyEvent(m, stream, event)
	if !strings.Contains(m.issuesText(), "1.5s") {
		t.Fatal("per-provider elapsed missing")
	}
}

func TestMutationCompletionRejectsLateHiddenViewStreams(t *testing.T) {
	m, _ := readyModel(t)
	hidden := streamFixture(m, updatesView)
	m.view = installedView
	m.planGeneration = 9
	m.Update(executedMsg{generation: 9, result: domain.ActionResult{Message: "changed"}})
	applyEvent(m, hidden, baseEvent("brew", "base", domain.Package{Manager: "brew", ID: "pre-mutation"}))
	if len(m.states[updatesView].snapshot.Packages) > 0 || m.states[updatesView].generation == hidden.target.generation {
		t.Fatal("late hidden read restored pre-mutation inventory")
	}
}

func TestSessionMemoryBaseAndDonePreserveSelectableObservation(t *testing.T) {
	m, _ := batchFixture(t)
	stream := streamFixture(m, installedView)
	p := domain.Package{Manager: "brew", ID: "alpha", Version: "1", Scope: "global"}
	event := baseEvent("brew", "base", p)
	event.Cached = true
	event.Snapshot.Coverage[0].ObservedAt = time.Now().Add(-24 * time.Hour)
	applyEvent(m, stream, event)
	applyEvent(m, stream, domain.QueryEvent{Stage: "done", Snapshot: event.Snapshot})
	if !hasAction(m, "upgrade") || !hasAction(m, "remove") || m.states[installedView].snapshot.Packages[0].InventoryStale {
		t.Fatal("reused session memory was treated as an unverified disk seed")
	}
	if !m.states[installedView].snapshot.Coverage[0].ObservedAt.Equal(event.Snapshot.Coverage[0].ObservedAt) {
		t.Fatal("session reuse fabricated an observation time")
	}
}
