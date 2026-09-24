package app

import (
	"regexp"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

var packageIdentityName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@+._-]*$`)
var packageIdentityTapPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// identitySpellings validates that the native metadata describes a complete
// known slot. It does not promote an unresolved receipt to verified provenance.
func identitySpellings(p domain.Package) (map[string]bool, bool) {
	i := p.Identity
	if i == nil || i.Name == "" || i.Tap == "" || i.CanonicalID == "" || len(i.Aliases) == 0 {
		return nil, false
	}
	kind, core := "formula", "homebrew/core"
	if p.Manager == "cask" {
		kind, core = "cask", "homebrew/cask"
	} else if p.Manager != "brew" {
		return nil, false
	}
	if i.Kind != kind || !packageIdentityName.MatchString(i.Name) {
		return nil, false
	}
	parts := strings.Split(i.Tap, "/")
	if len(parts) != 2 || !packageIdentityTapPart.MatchString(parts[0]) || !packageIdentityTapPart.MatchString(parts[1]) {
		return nil, false
	}
	canonical := i.Tap + "/" + i.Name
	if i.Tap == core {
		canonical = i.Name
	}
	if i.CanonicalID != canonical {
		return nil, false
	}
	valid := func(id string) bool {
		p := strings.Split(id, "/")
		return len(p) == 1 && packageIdentityName.MatchString(id) || len(p) == 3 && packageIdentityTapPart.MatchString(p[0]) && packageIdentityTapPart.MatchString(p[1]) && packageIdentityName.MatchString(p[2])
	}
	spellings := map[string]bool{}
	for _, alias := range i.Aliases {
		if !valid(alias) {
			return nil, false
		}
		spellings[strings.ToLower(alias)] = true
	}
	// Both representations must have come from the metadata index. Otherwise a
	// raw row cannot be tied to this slot solely from its display name.
	if !spellings[strings.ToLower(i.Name)] || !spellings[strings.ToLower(i.CanonicalID)] || !spellings[strings.ToLower(i.Tap+"/"+i.Name)] || !spellings[strings.ToLower(p.ID)] {
		return nil, false
	}
	spellings[strings.ToLower(p.ID)] = true
	spellings[strings.ToLower(i.CanonicalID)] = true
	return spellings, true
}
func matchingPackageInstance(a, b string) bool { return a == "" || b == "" || a == b }

// freshPackageInventory is a target-specific completeness proof, not a repair
// of manager-wide failed coverage. A receipt conflict in a known distinct slot
// cannot conceal the selected verified package. Unnamed/global/native failures
// and incomplete alias metadata cannot establish either presence or absence.
func freshPackageInventory(s domain.Snapshot, target domain.Package, at ...time.Time) bool {
	now := time.Now()
	if len(at) > 0 {
		now = at[0]
	}
	var coverage *domain.Coverage
	for n := range s.Coverage {
		c := &s.Coverage[n]
		if c.Manager != target.Manager {
			continue
		}
		if coverage != nil || !matchingPackageInstance(c.Instance, target.Instance) {
			return false
		}
		coverage = c
	}
	if coverage == nil {
		return false
	}
	if target.Manager != "brew" && target.Manager != "cask" {
		return freshInventory(s, target.Manager)
	}
	if target.Identity == nil || target.Identity.State != "verified" || target.InventoryStale {
		return false
	}
	targetNames, valid := identitySpellings(target)
	if !valid {
		return false
	}
	if coverage.Stale || coverage.ObservedAt.IsZero() {
		return false
	}
	age := now.Sub(coverage.ObservedAt)
	if age < 0 || age >= inventoryTTL {
		return false
	}
	if freshInventory(s, target.Manager) {
		return true
	}
	if coverage.State != "failed" {
		return false
	}
	bound := map[int]bool{}
	failureCount := 0
	for _, issue := range s.Issues {
		if issue.Manager != "" && issue.Manager != target.Manager {
			continue
		}
		if issue.Kind == "notice" || issue.Kind == "enrichment" {
			continue
		}
		if issue.Manager != target.Manager || issue.Kind != "identity" || issue.PackageID == "" {
			return false
		}
		rowIndex := -1
		for n, p := range s.Packages {
			if p.Manager == target.Manager && p.ID == issue.PackageID && matchingPackageInstance(p.Instance, target.Instance) {
				if rowIndex != -1 {
					return false
				}
				rowIndex = n
			}
		}
		if rowIndex < 0 {
			return false
		}
		row := s.Packages[rowIndex]
		if row.InventoryStale || row.Identity == nil || (row.Identity.State != "unresolved" && row.Identity.State != "ambiguous") {
			return false
		}
		spellings, ok := identitySpellings(row)
		if !ok {
			return false
		}
		for name := range spellings {
			if targetNames[name] {
				return false
			}
		}
		bound[rowIndex] = true
		failureCount++
	}
	if failureCount == 0 {
		return false
	}
	for n, p := range s.Packages {
		if p.Manager != target.Manager {
			continue
		}
		if !matchingPackageInstance(p.Instance, target.Instance) || p.InventoryStale {
			return false
		}
		if p.Identity == nil || p.Identity.State != "verified" {
			if !bound[n] {
				return false
			}
			continue
		}
		if _, ok := identitySpellings(p); !ok {
			return false
		}
	}
	return true
}

// verifiedPackageCoverage is ephemeral eligibility input for this one target.
// Never store/export it as provider coverage: the source snapshot remains failed
// and retains every identity issue, timestamp and stale marker.
func verifiedPackageCoverage(s domain.Snapshot, target domain.Package, at ...time.Time) []domain.Coverage {
	copy := append([]domain.Coverage(nil), s.Coverage...)
	if !freshPackageInventory(s, target, at...) {
		return copy
	}
	for n := range copy {
		if copy[n].Manager == target.Manager && matchingPackageInstance(copy[n].Instance, target.Instance) {
			copy[n].State = "complete"
		}
	}
	return copy
}
