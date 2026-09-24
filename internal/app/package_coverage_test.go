package app

import (
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func coveragePackage(id, name, tap, state string) domain.Package {
	return domain.Package{Manager: "cask", Instance: "brew-instance", ID: id, Version: "1.0", Identity: &domain.PackageIdentity{State: state, Name: name, Tap: tap, Kind: "cask", CanonicalID: id, Aliases: []string{name, id, tap + "/" + name}}}
}
func conflictedCaskSnapshot() (domain.Snapshot, domain.Package) {
	target := coveragePackage("alacritty", "alacritty", "homebrew/cask", "verified")
	conflict := coveragePackage("superset", "superset", "homebrew/cask", "ambiguous")
	s := domain.Snapshot{Packages: []domain.Package{target, conflict}, Coverage: []domain.Coverage{{Manager: "cask", Instance: target.Instance, State: "failed", ObservedAt: time.Now()}}, Issues: []domain.Issue{{Manager: "cask", PackageID: "superset", Kind: "identity", Message: "receipt tap differs"}}}
	return s, target
}
func TestPackageCoverageIsolatesSupersetReceiptConflict(t *testing.T) {
	s, target := conflictedCaskSnapshot()
	if !freshPackageInventory(s, target) {
		t.Fatal("known unrelated receipt blocked target")
	}
	derived := verifiedPackageCoverage(s, target)
	if derived[0].State != "complete" || derived[0].ObservedAt != s.Coverage[0].ObservedAt || s.Coverage[0].State != "failed" || len(s.Issues) != 1 {
		t.Fatal("provider state was changed", derived, s)
	}
	if freshPackageInventory(s, s.Packages[1]) {
		t.Fatal("ambiguous target was authorized")
	}
	// Outdated may omit a current target; the same unrelated slot cannot conceal it.
	s.Packages = s.Packages[1:]
	if !freshPackageInventory(s, target) {
		t.Fatal("safe target absence was not proven")
	}
	// An available update can still be present with its verified canonical identity.
	target.Latest = "2.0"
	s.Packages = append(s.Packages, target)
	if !freshPackageInventory(s, target) {
		t.Fatal("available target blocked")
	}
}
func TestPackageCoverageRejectsUncertainFailures(t *testing.T) {
	for _, tc := range []string{"alias", "raw", "canonical", "global", "native", "missing-row", "missing-aliases", "missing-name", "unknown-row", "stale", "old", "future", "other-instance", "target-ambiguous"} {
		t.Run(tc, func(t *testing.T) {
			s, target := conflictedCaskSnapshot()
			switch tc {
			case "alias":
				s.Packages[1].Identity.Aliases = append(s.Packages[1].Identity.Aliases, "alacritty")
			case "raw":
				s.Packages[1].ID = "alacritty"
				s.Issues[0].PackageID = "alacritty"
				s.Packages[1].Identity.Aliases = append(s.Packages[1].Identity.Aliases, "alacritty")
			case "canonical":
				s.Packages[1].Identity.CanonicalID = "alacritty"
			case "global":
				s.Issues[0].PackageID = ""
			case "native":
				s.Issues = append(s.Issues, domain.Issue{Manager: "cask", Kind: "query", Message: "native read failed"})
			case "missing-row":
				s.Packages = s.Packages[:1]
			case "missing-aliases":
				s.Packages[1].Identity.Aliases = nil
			case "missing-name":
				s.Packages[1].Identity.Name = ""
			case "unknown-row":
				s.Packages = append(s.Packages, domain.Package{Manager: "cask", ID: "unreported", Instance: target.Instance})
			case "stale":
				s.Coverage[0].Stale = true
			case "old":
				s.Coverage[0].ObservedAt = time.Now().Add(-2 * time.Minute)
			case "future":
				s.Coverage[0].ObservedAt = time.Now().Add(time.Minute)
			case "other-instance":
				s.Coverage[0].Instance = "another-brew"
			case "target-ambiguous":
				target.Identity = &domain.PackageIdentity{State: "ambiguous"}
			}
			if freshPackageInventory(s, target) {
				t.Fatal("unsafe relaxation", tc)
			}
			if got := verifiedPackageCoverage(s, target); got[0].State != "failed" {
				t.Fatal("unproven coverage promoted", got)
			}
		})
	}
}
func TestPackageCoveragePreservesStrictOtherProviders(t *testing.T) {
	s := domain.Snapshot{Coverage: []domain.Coverage{{Manager: "npm", State: "complete"}}}
	p := domain.Package{Manager: "npm", ID: "foo"}
	if !freshPackageInventory(s, p) {
		t.Fatal("nonbrew strict path changed")
	}
	s.Coverage[0].State = "failed"
	if freshPackageInventory(s, p) {
		t.Fatal("nonbrew failure relaxed")
	}
}

func TestPackageCoverageUsesCapturedOverviewTimeUniformly(t *testing.T) {
	s, target := conflictedCaskSnapshot()
	at := time.Now().Add(-2 * time.Minute)
	s.Coverage[0].ObservedAt = at
	if freshPackageInventory(s, target) {
		t.Fatal("old failed coverage accepted at execution")
	}
	if !freshPackageInventory(s, target, at) || verifiedPackageCoverage(s, target, at)[0].State != "complete" {
		t.Fatal("captured overview time rejected unrelated identity proof")
	}
	s.Coverage[0].State = "complete"
	s.Issues = nil
	s.Packages = s.Packages[:1]
	if freshPackageInventory(s, target) || !freshPackageInventory(s, target, at) {
		t.Fatal("complete and relaxed coverage use different time rules")
	}
	s.Coverage[0].Instance = "different-brew"
	if freshPackageInventory(s, target, at) {
		t.Fatal("complete coverage from another provider instance accepted")
	}
}
