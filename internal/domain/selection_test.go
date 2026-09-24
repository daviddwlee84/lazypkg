package domain

import "testing"

func TestSelectorsRequireUniqueNativeAliasesAndKeepVersions(t *testing.T) {
	rows := []Package{
		{Manager: "brew", Instance: "/brew", ID: "owner/tap/tool", Version: "1", Identity: &PackageIdentity{State: "verified", CanonicalID: "owner/tap/tool", Aliases: []string{"tool", "owner/tap/tool"}}},
		{Manager: "brew", Instance: "/brew", ID: "owner/tap/tool", Version: "2", Identity: &PackageIdentity{State: "verified", CanonicalID: "owner/tap/tool", Aliases: []string{"tool", "owner/tap/tool"}}},
		{Manager: "cask", Instance: "/brew", ID: "tool", Version: "3"},
	}
	selected, err := SelectPackageRecords(rows, "brew", "tool", "/brew")
	if err != nil || len(selected) != 2 || selected[0].ID != "owner/tap/tool" {
		t.Fatal(selected, err)
	}
	rows = append(rows, Package{Manager: "brew", Instance: "/brew", ID: "other/tap/tool", Identity: &PackageIdentity{State: "verified", CanonicalID: "other/tap/tool", Aliases: []string{"tool", "other/tap/tool"}}})
	if _, err := SelectPackageRecords(rows, "brew", "tool", "/brew"); err == nil {
		t.Fatal("ambiguous short name selected more than one source")
	}
	selected, err = SelectPackageRecords(rows, "brew", "owner/tap/tool", "/brew")
	if err != nil || len(selected) != 2 {
		t.Fatal(selected, err)
	}
	rows[0].Identity.State = "unresolved"
	rows[1].Identity.State = "unresolved"
	selected, err = SelectPackageRecords(rows[:2], "brew", "tool", "/brew")
	if err != nil || len(selected) != 0 {
		t.Fatal("unverified alias selected", selected, err)
	}
}

func TestInventoryJoinDoesNotBorrowTapAliasesOrClaimUnknownAbsence(t *testing.T) {
	core := Package{Manager: "brew", ID: "tool", Identity: &PackageIdentity{State: "verified", CanonicalID: "tool", Tap: "homebrew/core"}}
	tap := Package{Manager: "brew", ID: "owner/tap/tool", Version: "1", Identity: &PackageIdentity{State: "verified", CanonicalID: "owner/tap/tool", Aliases: []string{"tool"}}}
	coverage := []Coverage{{Manager: "brew", State: "complete"}}
	joined := AttachInventory(Snapshot{Packages: []Package{core}}, Snapshot{Packages: []Package{tap}, Coverage: coverage})
	if joined.Packages[0].InstallState != "not_installed" {
		t.Fatal("catalog core row borrowed installed tap alias", joined)
	}
	core.Identity.State = "unresolved"
	joined = AttachInventory(Snapshot{Packages: []Package{core}}, Snapshot{Packages: []Package{tap}, Coverage: coverage})
	if joined.Packages[0].InstallState != "identity_unknown" {
		t.Fatal("unknown search identity became absence", joined)
	}
	core.Identity.State = "verified"
	tap.Identity.State = "unresolved"
	joined = AttachInventory(Snapshot{Packages: []Package{core}}, Snapshot{Packages: []Package{tap}, Coverage: coverage})
	if joined.Packages[0].InstallState != "check_failed" {
		t.Fatal("unresolved inventory identity became negative proof", joined)
	}
}
