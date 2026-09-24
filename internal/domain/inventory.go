package domain

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

var pythonSeparators = regexp.MustCompile(`[._-]+`)

func NormalizePackageID(manager, id string) string {
	switch manager {
	case "uvx", "pipx", "uv-pip", "pip":
		return pythonSeparators.ReplaceAllString(strings.ToLower(id), "-")
	case "winget", "scoop", "choco":
		return strings.ToLower(id)
	}
	return id
}

func CloneSnapshot(s Snapshot) Snapshot {
	s.Packages = append([]Package(nil), s.Packages...)
	s.Issues = append([]Issue(nil), s.Issues...)
	s.Coverage = append([]Coverage(nil), s.Coverage...)
	s.InventoryCoverage = append([]Coverage(nil), s.InventoryCoverage...)
	for i := range s.Packages {
		p := &s.Packages[i]
		if p.Identity != nil {
			copy := *p.Identity
			copy.Aliases = append([]string(nil), copy.Aliases...)
			p.Identity = &copy
		}
		if p.Extension != nil {
			copy := *p.Extension
			p.Extension = &copy
		}
		p.Commands = append([]string(nil), p.Commands...)
		p.ExecutablePaths = append([]string(nil), p.ExecutablePaths...)
		p.Evidence = append([]Evidence(nil), p.Evidence...)
		p.InstalledVersions = append([]string(nil), p.InstalledVersions...)
		p.PathMatches = append([]Executable(nil), p.PathMatches...)
		for j := range p.PathMatches {
			p.PathMatches[j].Chain = append([]string(nil), p.PathMatches[j].Chain...)
			p.PathMatches[j].Evidence = append([]Evidence(nil), p.PathMatches[j].Evidence...)
		}
	}
	return s
}

// AttachInventory matches provider identity, not catalog version or display name.
// Coverage distinguishes a completed empty inventory from an unqueried provider.
func AttachInventory(candidates, inventory Snapshot) Snapshot {
	out := CloneSnapshot(candidates)
	out.InventoryCoverage = append([]Coverage(nil), inventory.Coverage...)
	for i := range out.Packages {
		p := &out.Packages[i]
		p.Candidate = true
		p.InstallState = "not_checked"
		p.InstalledVersions = nil
		p.Version = ""
		p.InventoryStale = false
		p.InventoryAt = time.Time{}
		p.Commands = nil
		p.ExecutablePaths = nil
		p.Evidence = nil
		var coverage *Coverage
		for j := range inventory.Coverage {
			c := &inventory.Coverage[j]
			if c.Manager == p.Manager && (c.Instance == "" || p.Instance == "" || c.Instance == p.Instance) {
				coverage = c
				break
			}
		}
		if coverage != nil {
			p.InventoryAt = coverage.ObservedAt
			p.InventoryStale = coverage.Stale
			switch coverage.State {
			case "complete":
				p.InstallState = "not_installed"
			case "pending":
				p.InstallState = "checking"
			case "failed":
				p.InstallState = "check_failed"
			case "unavailable":
				p.InstallState = "unavailable"
			}
		}
		if p.Identity != nil && p.Identity.State != "verified" {
			p.InstallState = "identity_unknown"
			continue
		}
		matched := false
		identityIncomplete := false
		versions := map[string]bool{}
		for _, record := range inventory.Packages {
			if record.Manager != p.Manager || (p.Instance != "" && record.Instance != "" && p.Instance != record.Instance) {
				continue
			}
			if record.Identity != nil && record.Identity.State != "verified" {
				identityIncomplete = true
				continue
			}
			if !SamePackageIdentity(record, *p) {
				continue
			}
			matched = true
			if record.Version != "" {
				versions[record.Version] = true
			}
			p.Commands = append([]string(nil), record.Commands...)
			p.ExecutablePaths = append([]string(nil), record.ExecutablePaths...)
			p.Evidence = append([]Evidence(nil), record.Evidence...)
		}
		if matched {
			p.InstallState = "installed"
			if coverage == nil {
				p.InventoryAt = inventory.ObservedAt
			} else if coverage.State != "complete" {
				p.InventoryStale = true
			}
			for version := range versions {
				p.InstalledVersions = append(p.InstalledVersions, version)
			}
			sort.Strings(p.InstalledVersions)
			if len(p.InstalledVersions) == 1 {
				p.Version = p.InstalledVersions[0]
			}
		} else if identityIncomplete && p.InstallState == "not_installed" {
			p.InstallState = "check_failed"
		}
	}
	return out
}

func SortCandidates(packages []Package, query string, order []string) {
	ranks := map[string]int{}
	for i, id := range order {
		if _, ok := ranks[id]; !ok {
			ranks[id] = i
		}
	}
	relevance := func(p Package) int {
		q := strings.ToLower(strings.TrimSpace(query))
		id, name := strings.ToLower(p.ID), strings.ToLower(p.Name)
		if id == q || name == q {
			return 0
		}
		if strings.HasPrefix(id, q) || strings.HasPrefix(name, q) {
			return 1
		}
		return 2
	}
	rank := func(id string) int {
		if n, ok := ranks[id]; ok {
			return n
		}
		return len(order)
	}
	sort.SliceStable(packages, func(i, j int) bool {
		a, b := packages[i], packages[j]
		if relevance(a) != relevance(b) {
			return relevance(a) < relevance(b)
		}
		ai, bi := a.InstallState == "installed" && !a.InventoryStale, b.InstallState == "installed" && !b.InventoryStale
		if ai != bi {
			return ai
		}
		if rank(a.Manager) != rank(b.Manager) {
			return rank(a.Manager) < rank(b.Manager)
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Manager != b.Manager {
			return a.Manager < b.Manager
		}
		return a.Instance < b.Instance
	})
}
