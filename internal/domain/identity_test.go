package domain

import "testing"

func TestPackageIdentityUsesCanonicalProviderMeaning(t *testing.T) {
	core := Package{Manager: "brew", ID: "foo", Instance: "brew-a", Identity: &PackageIdentity{State: "verified", CanonicalID: "foo", Aliases: []string{"foo", "homebrew/core/foo"}}}
	tap := Package{Manager: "brew", ID: "user/tap/foo", Instance: "brew-a", Identity: &PackageIdentity{State: "verified", CanonicalID: "user/tap/foo", Aliases: []string{"foo", "user/tap/foo"}}}
	if SamePackageIdentity(core, tap) {
		t.Fatal("alias intersection merged different taps")
	}
	other := tap
	other.ID = "foo"
	if !SamePackageIdentity(tap, other) || CanonicalPackageID(other) != "user/tap/foo" {
		t.Fatal("verified canonical spelling ignored")
	}
	other.Identity = &PackageIdentity{State: "unresolved", CanonicalID: tap.ID}
	if SamePackageIdentity(tap, other) {
		t.Fatal("unresolved identity matched")
	}
	other = core
	other.Manager = "cask"
	if SamePackageIdentity(core, other) {
		t.Fatal("formula/cask crossed")
	}
	other = core
	other.Instance = "brew-b"
	if SamePackageIdentity(core, other) {
		t.Fatal("different provider instance matched")
	}
	if !SamePackageIdentity(Package{Manager: "brew", ID: "jq"}, Package{Manager: "brew", ID: "jq"}) {
		t.Fatal("legacy exact identity broken")
	}
}

func TestCloneSnapshotDetachesIdentityAliases(t *testing.T) {
	s := Snapshot{Packages: []Package{{Identity: &PackageIdentity{State: "verified", CanonicalID: "user/tap/foo", Aliases: []string{"foo"}}}}}
	copy := CloneSnapshot(s)
	copy.Packages[0].Identity.Aliases[0] = "changed"
	copy.Packages[0].Identity.State = "unknown"
	if s.Packages[0].Identity.Aliases[0] != "foo" || s.Packages[0].Identity.State != "verified" {
		t.Fatal("identity shared backing state")
	}
}
