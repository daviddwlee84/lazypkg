package domain

import "fmt"

// SelectPackageRecords resolves a user selector within one observed manager
// instance. Aliases require native identity evidence and must not identify two
// different packages. Multiple versions of one package remain separate records.
// This expresses selection intent; it does not authorize an operation.
func SelectPackageRecords(records []Package, manager, id, instance string) ([]Package, error) {
	var matches []Package
	identities := map[string]bool{}
	for _, p := range records {
		if p.Manager != manager || instance != "" && p.Instance != instance {
			continue
		}
		match := NormalizePackageID(manager, p.ID) == NormalizePackageID(manager, id)
		if p.Identity != nil && p.Identity.State == "verified" {
			for _, alias := range p.Identity.Aliases {
				match = match || NormalizePackageID(manager, alias) == NormalizePackageID(manager, id)
			}
		}
		if match {
			matches = append(matches, p)
			identities[p.Instance+"\x00"+CanonicalPackageID(p)] = true
		}
	}
	if len(identities) > 1 {
		return nil, fmt.Errorf("%s:%s identifies more than one installation source; select an exact provider-qualified package ID", manager, id)
	}
	return matches, nil
}
