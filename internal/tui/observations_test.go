package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestSessionInventoryRetainsObservationUntilManualRefresh(t *testing.T) {
	m, f := extendedModel(t)
	m.view = discoverView
	s := &m.states[discoverView]
	s.loaded = true
	s.query = "herdr"
	s.candidates = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr", Candidate: true}, {Manager: "mise", ID: "herdr", Candidate: true}}}
	old := time.Now().Add(-2 * time.Hour)
	m.inventories[m.scopeKey()] = &inventoryState{loaded: true, snapshot: domain.Snapshot{ObservedAt: time.Now(), Packages: []domain.Package{{Manager: "brew", ID: "herdr", Version: "1"}}, Coverage: []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: old}, {Manager: "mise", State: "complete", ObservedAt: old}}}}
	m.attachDiscover()
	m.move(1)
	selected := s.selected
	if m.ensureInventory(false) != nil {
		t.Fatal("elapsed time triggered an automatic inventory read")
	}
	command := m.ensureInventory(true)
	if command == nil || !m.inventories[m.scopeKey()].loading {
		t.Fatal("manual refresh did not schedule a read")
	}
	if m.ensureInventory(false) != nil {
		t.Fatal("expired cache scheduled duplicate reads while pending")
	}
	for _, p := range s.snapshot.Packages {
		if p.InventoryStale {
			t.Fatal("observation age changed its classification")
		}
	}
	if s.selected != selected {
		t.Fatal("starting inventory refresh changed selection")
	}
	f.snapshot = domain.Snapshot{ObservedAt: time.Now(), Packages: []domain.Package{{Manager: "brew", ID: "herdr", Version: "2"}}, Coverage: []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: time.Now()}, {Manager: "mise", State: "complete", ObservedAt: time.Now()}}}
	deliver(m, command)
	if s.selected != selected {
		t.Fatal("completed inventory refresh changed candidate identity")
	}
	if p := s.snapshot.Packages[0]; strings.Join(p.InstalledVersions, ",") != "2" || p.InventoryStale {
		t.Fatalf("fresh result not attached: %#v", p)
	}
	if m.ensureInventory(false) != nil {
		t.Fatal("fresh observation was not reused")
	}
}

func TestFreshServiceObservationWinsOverOlderUICache(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = discoverView
	s := &m.states[discoverView]
	s.loaded = true
	s.query = "herdr"
	s.candidates = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr", Candidate: true, InstallState: "installed", Version: "2", InstalledVersions: []string{"2"}, InventoryAt: time.Now(), Commands: []string{"new-command"}}}}
	m.inventories[m.scopeKey()] = &inventoryState{loaded: true, snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr", Version: "1", Commands: []string{"old-command"}}}, Coverage: []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: time.Now().Add(-2 * time.Hour)}}}}
	if m.ensureInventory(false) != nil {
		t.Fatal("older observation unexpectedly triggered a read")
	}
	p := s.snapshot.Packages[0]
	if p.Version != "2" || p.InventoryStale || strings.Join(p.Commands, ",") != "new-command" {
		t.Fatalf("old UI data overwrote newer service observation: %#v", p)
	}
	// A newer negative observation must also remove ownership metadata from an
	// older positive record, while keeping independent PATH presence untouched.
	s.candidates.Packages[0].InstallState = "not_installed"
	s.candidates.Packages[0].Version = ""
	s.candidates.Packages[0].InstalledVersions = nil
	s.candidates.Packages[0].Commands = nil
	m.attachDiscover()
	p = s.snapshot.Packages[0]
	if p.InstallState != "not_installed" || len(p.Commands) > 0 || p.InventoryStale {
		t.Fatal("old positive installation evidence survived a newer negative observation")
	}
}

func TestUnverifiedServiceObservationDoesNotReplacePendingInventory(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = discoverView
	s := &m.states[discoverView]
	s.loaded = true
	s.query = "herdr"
	s.candidates = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr", Candidate: true, InstallState: "installed", InstalledVersions: []string{"1"}, InventoryAt: time.Now().Add(-2 * time.Hour), InventoryStale: true}}}
	m.ensureInventory(false)
	if s.snapshot.Packages[0].InstallState != "checking" {
		t.Fatal("unverified service seed replaced pending inventory observation")
	}
}

func TestHealthReplyBindsManagerPathAndVersion(t *testing.T) {
	m, _ := readyModel(t)
	m.managers = []domain.Manager{{ID: "npm", Path: "/new/npm", Version: "12"}}
	for _, health := range []domain.ManagerHealth{
		{Manager: "npm", Path: "/old/npm", Version: "12", UpdateStatus: "wrong path"},
		{Manager: "npm", Path: "/new/npm", Version: "11", UpdateStatus: "wrong version"},
	} {
		m.acceptHealth(healthMsg{generation: m.healthGeneration, health: []domain.ManagerHealth{health}})
		if m.managers[0].Health != nil {
			t.Fatal("manager identity changed but old health result was accepted")
		}
	}
	m.acceptHealth(healthMsg{generation: m.healthGeneration, health: []domain.ManagerHealth{{Manager: "npm", Path: "/new/npm", Version: "12", UpdateStatus: "current"}}})
	if m.managers[0].Health == nil || m.managers[0].Health.UpdateStatus != "current" {
		t.Fatal("matching health identity was rejected")
	}
}
