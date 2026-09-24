package domain

import (
	"testing"
	"time"
)

func TestInventoryJoinIsIndependentOfLatestAndHasDistinctAbsenceStates(t *testing.T) {
	candidates := Snapshot{Packages: []Package{{Manager: "brew", ID: "herdr", Latest: ""}, {Manager: "mise", ID: "herdr"}, {Manager: "npm", ID: "herdr"}, {Manager: "cargo", ID: "herdr"}, {Manager: "uvx", ID: "MY_tool"}}}
	inventory := Snapshot{Packages: []Package{{Manager: "brew", ID: "herdr", Version: "1.0"}, {Manager: "mise", ID: "herdr", Version: "1.0"}, {Manager: "mise", ID: "herdr", Version: "2.0"}, {Manager: "uvx", ID: "my-tool", Version: "3"}}, Coverage: []Coverage{{Manager: "brew", State: "complete"}, {Manager: "mise", State: "complete"}, {Manager: "npm", State: "failed"}, {Manager: "cargo", State: "complete"}, {Manager: "uvx", State: "complete"}}}
	s := AttachInventory(candidates, inventory)
	if s.Packages[0].InstallState != "installed" || s.Packages[0].Version != "1.0" || s.Packages[0].Latest != "" {
		t.Fatal(s.Packages[0])
	}
	if len(s.Packages[1].InstalledVersions) != 2 || s.Packages[1].Version != "" {
		t.Fatal(s.Packages[1])
	}
	if s.Packages[2].InstallState != "check_failed" || s.Packages[3].InstallState != "not_installed" || s.Packages[4].InstallState != "installed" {
		t.Fatal(s)
	}
	if candidates.Packages[0].Candidate || candidates.Packages[0].Version != "" {
		t.Fatal("join mutated input")
	}
}
func TestInventoryJoinDoesNotCrossInstancesOrInventVersions(t *testing.T) {
	c := Snapshot{Packages: []Package{{Manager: "npm", ID: "tool", Instance: "npm-from-node24", Candidate: true}}}
	i := Snapshot{Packages: []Package{{Manager: "npm", ID: "tool", Instance: "npm-from-node22", Version: "9"}}, Coverage: []Coverage{{Manager: "npm", Instance: "npm-from-node24", State: "complete"}}}
	if s := AttachInventory(c, i); s.Packages[0].InstallState != "not_installed" {
		t.Fatal(s)
	}
	i.Packages = []Package{{Manager: "npm", ID: "tool", Instance: "npm-from-node24"}}
	i.Coverage[0].Stale = true
	i.Coverage[0].ObservedAt = time.Now().Add(-time.Hour)
	s := AttachInventory(c, i)
	if s.Packages[0].InstallState != "installed" || len(s.Packages[0].InstalledVersions) != 0 || !s.Packages[0].InventoryStale {
		t.Fatal(s)
	}
	if s.Packages[0].Key() != c.Packages[0].Key() {
		t.Fatal("inventory changed candidate identity")
	}
}
func TestSearchOrderPrefersRelevantInstalledThenManagerOrder(t *testing.T) {
	p := []Package{{ID: "herdr-extra", Manager: "brew", InstallState: "installed"}, {ID: "herdr", Manager: "brew"}, {ID: "herdr", Manager: "mise", InstallState: "installed"}, {ID: "herdr", Manager: "cargo"}}
	SortCandidates(p, "herdr", []string{"cargo", "brew", "mise"})
	if p[0].Manager != "mise" || p[1].Manager != "cargo" || p[2].Manager != "brew" || p[3].ID != "herdr-extra" {
		t.Fatal(p)
	}
}

func TestRejoiningAbsentInventoryClearsPriorOwnership(t *testing.T) {
	old := time.Now().Add(-time.Minute)
	c := Snapshot{Packages: []Package{{Manager: "brew", ID: "tool", Instance: "/brew", PathMatches: []Executable{{Path: "/other/bin/tool"}}}}}
	i := Snapshot{Packages: []Package{{Manager: "brew", ID: "tool", Instance: "/brew", Version: "1", Commands: []string{"tool"}, ExecutablePaths: []string{"/brew/bin/tool"}, Evidence: []Evidence{{Kind: "recorded", Source: "brew"}}}}, Coverage: []Coverage{{Manager: "brew", Instance: "/brew", State: "complete", ObservedAt: old}}}
	joined := AttachInventory(c, i)
	for _, coverage := range [][]Coverage{
		{{Manager: "brew", Instance: "/brew", State: "complete", ObservedAt: time.Now()}},
		nil,
	} {
		s := AttachInventory(joined, Snapshot{Coverage: coverage})
		p := s.Packages[0]
		if len(p.Commands)+len(p.ExecutablePaths)+len(p.Evidence)+len(p.InstalledVersions) != 0 || p.Version != "" {
			t.Fatal("old ownership survived rejoin", p)
		}
		if len(p.PathMatches) != 1 {
			t.Fatal("independent PATH observation was lost", p)
		}
		if coverage == nil && (!p.InventoryAt.IsZero() || p.InstallState != "not_checked") {
			t.Fatal("unqueried inventory retained old observation", p)
		}
		if coverage != nil && p.InstallState != "not_installed" {
			t.Fatal("empty completed inventory was not represented", p)
		}
	}
}
