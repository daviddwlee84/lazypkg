// Package diagnostics associates installed records with executable paths. It
// inspects files and known manager query commands; it never runs discovered tools.
package diagnostics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

const maxEntries = 20000

type Engine struct {
	Runner  process.Runner
	GOOS    string
	Path    string
	PathExt string
	Dir     string
}

func New(r process.Runner) *Engine {
	dir, _ := os.Getwd()
	return &Engine{Runner: r, GOOS: runtime.GOOS, Path: os.Getenv("PATH"), PathExt: os.Getenv("PATHEXT"), Dir: dir}
}

func (e *Engine) defaults() Engine {
	v := *e
	if v.GOOS == "" {
		v.GOOS = runtime.GOOS
	}
	if v.Dir == "" {
		v.Dir, _ = os.Getwd()
	}
	if v.Path == "" {
		v.Path = os.Getenv("PATH")
	}
	if v.PathExt == "" {
		v.PathExt = os.Getenv("PATHEXT")
	}
	if v.Runner == nil {
		v.Runner = process.ExecRunner{}
	}
	return v
}

func (e *Engine) query(ctx context.Context, executable string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	r, err := e.Runner.Output(ctx, domain.Command{Path: executable, Args: args, Dir: e.Dir,
		Env: map[string]string{"PATH": e.Path, "PATHEXT": e.PathExt, "LC_ALL": "C", "LANG": "C", "NO_COLOR": "1", "HOMEBREW_NO_AUTO_UPDATE": "1", "HOMEBREW_NO_ANALYTICS": "1", "MISE_AUTO_INSTALL": "0", "MISE_NOT_FOUND_AUTO_INSTALL": "false"}})
	return r.Stdout, err
}

func evidence(p *domain.Package, source, detail string) {
	v := domain.Evidence{Kind: "recorded", Source: source, Detail: detail}
	for _, x := range p.Evidence {
		if x == v {
			return
		}
	}
	p.Evidence = append(p.Evidence, v)
}

func addPath(p *domain.Package, path string) {
	if path == "" {
		return
	}
	path = filepath.Clean(path)
	for _, v := range p.ExecutablePaths {
		if v == path {
			return
		}
	}
	p.ExecutablePaths = append(p.ExecutablePaths, path)
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".exe" || ext == ".cmd" || ext == ".bat" || ext == ".ps1" || ext == ".com" {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	for _, v := range p.Commands {
		if v == name {
			return
		}
	}
	p.Commands = append(p.Commands, name)
}

func within(path, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (e *Engine) Diagnose(ctx context.Context, name string, packages []domain.Package) (domain.DiagnosticReport, error) {
	v := e.defaults()
	report := domain.DiagnosticReport{Directory: v.Dir, Scope: "Inherited PATH and known installed entrypoints; shell aliases, functions, command caches and undiscovered manager contexts are not inspected", Executables: []domain.Executable{}, Findings: []domain.Finding{}}
	if strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return report, fmt.Errorf("diagnose requires a command name, not a path")
	}
	items, issues, err := v.scan(ctx, name)
	report.Issues = issues
	if err != nil {
		return report, err
	}
	items, err = v.knownEntrypoints(ctx, name, items, packages, &report)
	if err != nil {
		return report, err
	}
	if err := v.miseShims(ctx, name, items, packages, &report); err != nil {
		return report, err
	}
	for i := range items {
		v.attribute(&items[i], packages)
	}
	groups := make(map[string][]int)
	order := []string{}
	for i := range items {
		key := items[i].Name
		if v.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], i)
	}
	for _, key := range order {
		indices := groups[key]
		valid := []int{}
		onPath := []int{}
		for _, i := range indices {
			item := &items[i]
			if item.Problem != "" {
				report.Findings = append(report.Findings, domain.Finding{Kind: "broken", Name: item.Name, Message: item.Problem, Paths: []string{item.Path}})
				continue
			}
			for _, j := range valid {
				if equivalent(*item, items[j]) {
					item.EquivalentTo = items[j].Path
					if item.PackageKey == "" && items[j].PackageKey != "" {
						copyOwner(item, items[j])
					} else if items[j].PackageKey == "" && item.PackageKey != "" {
						copyOwner(&items[j], *item)
					}
					break
				}
			}
			if len(onPath) == 0 && item.PathIndex >= 0 {
				item.Preferred = true
			}
			if item.EquivalentTo == "" {
				valid = append(valid, i)
				if item.PathIndex >= 0 {
					onPath = append(onPath, i)
				} else {
					kind, message := "not-on-path", "Installed executable is not reachable through the inherited PATH."
					if item.Manager == "mise" {
						kind, message = "inactive-runtime", "Installed runtime entrypoint is not on this PATH; inactive versions can be intentional."
					}
					report.Findings = append(report.Findings, domain.Finding{Kind: kind, Name: item.Name, Message: message, Paths: []string{item.Path}})
				}
			}
		}
		if len(onPath) > 1 {
			paths := make([]string, 0, len(onPath))
			runtimeOnly := true
			for _, i := range onPath {
				paths = append(paths, items[i].Path)
				if items[i].Manager != "mise" {
					runtimeOnly = false
				}
			}
			kind, message := "shadowed", "Multiple distinct executable targets share this command name; PATH order prefers the first. This alone does not prove duplicate software."
			if runtimeOnly {
				kind, message = "runtime-versions", "Multiple managed runtime targets are available; version coexistence can be intentional."
			}
			report.Findings = append(report.Findings, domain.Finding{Kind: kind, Name: items[onPath[0]].Name, Message: message, Paths: paths})
		}
	}
	report.Executables = items
	return report, nil
}

func (e *Engine) attribute(item *domain.Executable, packages []domain.Package) {
	target := item.Target
	if target == "" {
		target = item.Path
	}
	for _, p := range packages {
		matched := false
		for _, path := range p.ExecutablePaths {
			// Avoid statting every installed file for every PATH candidate. All
			// supported managers record the public entrypoint or its real target.
			base := filepath.Base(path)
			if !strings.EqualFold(filepath.Base(item.Path), base) && !strings.EqualFold(filepath.Base(target), base) {
				continue
			}
			if samePath(item.Path, path) || samePath(target, path) {
				matched = true
				break
			}
		}
		// A recorded runtime/keg root is a manager installation, unlike generic
		// directories such as ~/.local/bin or /usr/bin.
		if !matched && (p.Manager == "mise" || p.Manager == "brew") && p.Root != "" {
			root := p.Root
			if p.Manager == "mise" {
				if resolved, ok := miseOwnedRoot(root); ok {
					root = resolved
				} else {
					continue
				}
			} else if resolved, err := filepath.EvalSymlinks(root); err == nil {
				root = resolved
			}
			matched = within(target, root)
		}
		if !matched {
			continue
		}
		item.PackageKey = p.Key()
		item.Manager = p.Manager
		if e.GOOS == "windows" && p.Manager == "npm" {
			e.npmShim(item, p)
		}
		item.Evidence = append(item.Evidence, p.Evidence...)
		if len(item.Evidence) == 0 {
			item.Evidence = append(item.Evidence, domain.Evidence{Kind: "recorded", Source: p.Manager, Detail: "Matched an executable path in the manager's installed record"})
		}
		return
	}
	if item.Manager == "" {
		item.Evidence = append(item.Evidence, domain.Evidence{Kind: "unknown", Source: "PATH", Detail: "No supported manager record was matched; this does not establish manual installation"})
	}
}

// Some mise plugins expose a shared dispatcher directory as install_path
// (notably rust -> ~/.cargo/bin). That directory is not owned by one version.
func miseOwnedRoot(root string) (string, bool) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	prefix, _, ok := strings.Cut(root, string(filepath.Separator)+"installs"+string(filepath.Separator))
	if ok {
		base, err := filepath.EvalSymlinks(filepath.Join(prefix, "installs"))
		if err != nil || !within(canonical, base) {
			return "", false
		}
	}
	return canonical, true
}

func copyOwner(to *domain.Executable, from domain.Executable) {
	to.Manager = from.Manager
	to.PackageKey = from.PackageKey
	to.Evidence = append([]domain.Evidence(nil), from.Evidence...)
	to.Evidence = append(to.Evidence, domain.Evidence{Kind: "recorded", Source: "resolved executable target", Detail: "Same target file as the registered entrypoint " + from.Path})
}

// Runtime shims are dispatchers, not another installed runtime. A focused
// diagnosis asks mise for its context-dependent selection; a whole-PATH scan
// keeps dispatchers explicit without spawning one subprocess per command.
func (e *Engine) miseShims(ctx context.Context, name string, items []domain.Executable, packages []domain.Package, report *domain.DiagnosticReport) error {
	var dirs []string
	for _, p := range packages {
		if p.Manager != "mise" || p.Root == "" {
			continue
		}
		root, _, ok := strings.Cut(p.Root, string(filepath.Separator)+"installs"+string(filepath.Separator))
		if ok {
			dirs = append(dirs, filepath.Join(root, "shims"))
		}
	}
	resolved := ""
	queried := false
	for i := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		isShim := false
		for _, dir := range dirs {
			if samePath(filepath.Dir(items[i].Path), dir) {
				isShim = true
				break
			}
		}
		if !isShim {
			continue
		}
		items[i].Manager = "mise"
		items[i].Evidence = append(items[i].Evidence, domain.Evidence{Kind: "inferred", Source: "mise shim directory", Detail: "Runtime dispatcher; target depends on the current directory and mise configuration"})
		if name == "" {
			continue
		}
		if !queried {
			queried = true
			out, err := e.query(ctx, "mise", "which", items[i].Name)
			if err != nil {
				report.Issues = append(report.Issues, domain.Issue{Manager: "mise", Message: "Could not resolve the current shim target: " + err.Error()})
				continue
			}
			resolved = strings.TrimSpace(out)
		}
		if !filepath.IsAbs(resolved) {
			continue
		}
		target, chain, problem := resolve(resolved)
		items[i].Target = target
		items[i].Chain = append([]string{items[i].Path}, chain...)
		items[i].Problem = problem
	}
	return ctx.Err()
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

func equivalent(a, b domain.Executable) bool {
	if a.Target == "" || b.Target == "" {
		return samePath(a.Path, b.Path)
	}
	return samePath(a.Target, b.Target)
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
