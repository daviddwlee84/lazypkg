package domain

// PackageIdentity is provider evidence for the native package address. Aliases
// are alternative spellings proven by that provider; they are never a license
// to merge two different canonical tap/package identities.
type PackageIdentity struct {
	State       string   `json:"state"` // verified, unresolved, ambiguous
	CanonicalID string   `json:"canonical_id,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Tap         string   `json:"tap,omitempty"`
	Name        string   `json:"name,omitempty"`
	Kind        string   `json:"kind,omitempty"` // formula, cask
	Source      string   `json:"source,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

func CanonicalPackageID(p Package) string {
	if p.Identity != nil && p.Identity.State == "verified" && p.Identity.CanonicalID != "" {
		return p.Identity.CanonicalID
	}
	return p.ID
}

// SamePackageIdentity never compares alias-set intersections. Discover and
// installed sources must each resolve their own meaning before they can match.
func SamePackageIdentity(a, b Package) bool {
	if a.Manager != b.Manager || a.Instance != "" && b.Instance != "" && a.Instance != b.Instance {
		return false
	}
	for _, p := range []Package{a, b} {
		if p.Identity != nil && (p.Identity.State != "verified" || p.Identity.CanonicalID == "") {
			return false
		}
	}
	return NormalizePackageID(a.Manager, CanonicalPackageID(a)) == NormalizePackageID(b.Manager, CanonicalPackageID(b))
}
