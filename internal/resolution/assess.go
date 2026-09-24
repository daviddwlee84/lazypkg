package resolution

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// Assess is focused: only metadata associated with this command's observed
// paths is inspected. It does not sweep inactive npm or project environments.
func (e *Engine) Assess(ctx context.Context, name string, inventory domain.Snapshot, report domain.DiagnosticReport, managers []domain.Manager) (domain.ConflictAssessment, error) {
	a := domain.ConflictAssessment{Name: name, Directory: report.Directory, Scope: report.Scope, Installations: []domain.ConflictInstallation{}}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") || strings.HasPrefix(name, "-") {
		return a, fmt.Errorf("resolution requires one command name")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.Issues = append(a.Issues, report.Issues...)
	a.Issues = append(a.Issues, inventory.Issues...)
	packages := map[string]domain.Package{}
	for _, p := range inventory.Packages {
		packages[p.Key()] = p
	}
	groups := map[string]int{}
	for _, path := range report.Executables {
		if path.Name != name && !(e.GOOS == "windows" && strings.EqualFold(path.Name, name)) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return a, err
		}
		p := packages[path.PackageKey]
		// A runtime may contain independently managed global npm entrypoints.
		// A validated manifest/bin link is more precise than runtime containment.
		if np, ok := npmPackage(path); ok {
			p = np
			path.PackageKey = p.Key()
			path.Manager = "npm"
		}
		// Observation times and UI search/cache fields are not installation
		// identity. Freshness is checked through provider coverage below.
		p = boundPackage(p)
		key := p.Key() + "\x00" + canonical(p.Root)
		if p.Manager == "" {
			key = "path:" + canonical(path.Target)
			if path.Target == "" {
				key = "path:" + canonical(path.Path)
			}
		}
		index, ok := groups[key]
		if !ok {
			index = len(a.Installations)
			groups[key] = index
			a.Installations = append(a.Installations, domain.ConflictInstallation{Package: p, Paths: []domain.Executable{}, Status: "unknown", Commands: append([]string(nil), p.Commands...), Evidence: append([]domain.Evidence(nil), p.Evidence...)})
		}
		i := &a.Installations[index]
		i.Paths = append(i.Paths, path)
		i.Effective = i.Effective || path.Preferred
	}
	for index := range a.Installations {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		i := &a.Installations[index]
		p := i.Package
		m := managerByID(managers, p.Manager)
		i.ManagerPath = m.Path
		if p.Manager == "" {
			i.Blockers = append(i.Blockers, "No exact package ownership record; manual files, dispatchers and system components are not removal targets.")
			for _, path := range i.Paths {
				for _, ev := range path.Evidence {
					if ev.Source == "mise shim directory" {
						i.Status = "runtime"
						i.Blockers = []string{"The runtime dispatcher has no verified separate installation; resolve its target before treating it as a duplicate."}
					}
				}
			}
			e.finish(i)
			continue
		}
		i.Status = "guidance"
		if p.Manager == "mise" || p.Manager == "rustup" || runtimePackage(p) {
			i.Status = "runtime"
			i.Blockers = append(i.Blockers, "Runtime versions and bundled commands can coexist intentionally. This workflow does not remove a runtime to resolve one command.")
			e.finish(i)
			continue
		}
		if !regexp.MustCompile(`^[A-Za-z0-9@][A-Za-z0-9._/@+-]*$`).MatchString(p.ID) {
			blocked(i, "Package identifier is not a safe exact native target.")
			e.finish(i)
			continue
		}
		if !m.Available || !m.Supports("remove") || (m.Scope != "" && m.Scope != "global") {
			blocked(i, "The selected manager is unavailable or cannot remove packages; repair it and refresh before planning removal.")
		}
		fresh := false
		for _, coverage := range inventory.Coverage {
			if coverage.Manager == p.Manager && coverage.State == "complete" && !coverage.Stale {
				fresh = true
			}
		}
		if (p.Manager == "brew" || p.Manager == "uvx" || p.Manager == "npm") && !fresh {
			blocked(i, "Fresh complete provider inventory is required; stale, failed or missing coverage cannot authorize removal.")
		}
		if index >= 8 {
			blocked(i, "This command has more than eight installation instances; review the remaining contexts separately.")
			e.finish(i)
			continue
		}
		var binding []string
		switch p.Manager {
		case "brew":
			binding = e.brew(ctx, i)
		case "uvx":
			binding = e.uv(ctx, i)
		case "npm":
			binding = e.npm(ctx, i, m)
		default:
			i.Blockers = append(i.Blockers, providerGuidance(p.Manager))
		}
		for _, path := range i.Paths {
			if path.Problem != "" {
				blocked(i, "An entrypoint is broken: "+path.Problem)
			}
		}
		if i.Project == "" && (p.Manager == "brew" || p.Manager == "uvx" || p.Manager == "npm") {
			blocked(i, "Upstream project identity is not verified; matching command names alone do not establish duplicate software.")
		}
		if len(i.Blockers) == 0 && (p.Manager == "brew" || p.Manager == "uvx" || p.Manager == "npm") {
			i.Status = "ready"
		}
		e.finish(i, binding...)
	}
	sort.SliceStable(a.Installations, func(i, j int) bool {
		if a.Installations[i].Effective != a.Installations[j].Effective {
			return a.Installations[i].Effective
		}
		return a.Installations[i].ID < a.Installations[j].ID
	})
	a.Fingerprint = digest([]any{a.Name, a.Directory, a.Installations, e.getenv("PATH")})
	return a, ctx.Err()
}

func boundPackage(p domain.Package) domain.Package {
	return domain.Package{Manager: p.Manager, ID: p.ID, Name: p.Name, Version: p.Version, Scope: p.Scope, Root: p.Root, Instance: p.Instance, Commands: unique(p.Commands), ExecutablePaths: unique(p.ExecutablePaths), Evidence: append([]domain.Evidence(nil), p.Evidence...), Active: p.Active, Global: p.Global, ConfigSource: p.ConfigSource}
}

func runtimePackage(p domain.Package) bool {
	if p.Manager != "brew" {
		return false
	}
	id := filepath.Base(p.ID)
	for _, name := range []string{"node", "go", "ruby", "rust", "python", "python3", "deno", "bun"} {
		if id == name || strings.HasPrefix(id, name+"@") {
			return true
		}
	}
	return false
}

func providerGuidance(id string) string {
	switch id {
	case "cargo":
		return "Cargo removes all registered binaries for a crate. Exact root, crate receipt and retained entrypoints need a reviewed Cargo preflight."
	case "gem":
		return "RubyGems requires the exact Ruby/GEM_HOME and version, exclusion of default gems, and reverse-dependency checks."
	case "pipx":
		return "pipx removal deletes a tool environment and its injected packages; inspect the exact environment and all exposed apps first."
	case "cask":
		return "A Cask command can belong to an entire application. Review its uninstall artifacts and scripts before removing the application."
	case "apt", "dnf", "pacman", "choco", "scoop", "winget", "flatpak", "snap":
		return "This provider needs a verified native removal transaction, scope and dependency preflight before guided execution is enabled."
	default:
		return "No reviewed removal preflight exists for this provider; inspect its native removal effects before acting."
	}
}
