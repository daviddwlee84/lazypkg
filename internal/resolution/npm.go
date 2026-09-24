package resolution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type npmManifest struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Bin        json.RawMessage `json:"bin"`
	Repository json.RawMessage `json:"repository"`
	Resolved   string          `json:"_resolved"`
}

func (m npmManifest) bins() map[string]string {
	bins := map[string]string{}
	if json.Unmarshal(m.Bin, &bins) != nil {
		var path string
		if json.Unmarshal(m.Bin, &path) == nil {
			bins[filepath.Base(m.Name)] = path
		}
	}
	return bins
}

func (m npmManifest) project(root string) string {
	var raw string
	if json.Unmarshal(m.Repository, &raw) != nil {
		var r struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(m.Repository, &r)
		raw = r.URL
	}
	if p := projectURL(raw); p != "" {
		return p
	}
	// Registry identity is useful when the package omits repository metadata.
	// Accept only an explicit public-registry receipt; names alone are insufficient.
	resolved := m.Resolved
	var lock struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	if readJSON(filepath.Join(root, ".package-lock.json"), &lock) == nil {
		if entry, ok := lock.Packages["node_modules/"+m.Name]; ok {
			resolved = entry.Resolved
		}
	}
	if strings.HasPrefix(resolved, "https://registry.npmjs.org/"+m.Name+"/-/") {
		return "npm:registry.npmjs.org/" + m.Name
	}
	return ""
}

func npmLocation(target string) (prefix, root, id string, ok bool) {
	marker := string(filepath.Separator) + "lib" + string(filepath.Separator) + "node_modules" + string(filepath.Separator)
	prefix, rest, ok := strings.Cut(canonical(target), marker)
	if !ok || !filepath.IsAbs(prefix) {
		return "", "", "", false
	}
	parts := strings.Split(rest, string(filepath.Separator))
	if len(parts) < 2 {
		return "", "", "", false
	}
	id = parts[0]
	if strings.HasPrefix(id, "@") {
		if len(parts) < 3 {
			return "", "", "", false
		}
		id += "/" + parts[1]
	}
	if !npmID.MatchString(id) {
		return "", "", "", false
	}
	return prefix, filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(id)), id, true
}

var npmID = regexp.MustCompile(`^(@[A-Za-z0-9._-]+/)?[A-Za-z0-9][A-Za-z0-9._-]*$`)

func npmPackage(path domain.Executable) (domain.Package, bool) {
	prefix, root, id, ok := npmLocation(path.Target)
	if !ok {
		return domain.Package{}, false
	}
	var m npmManifest
	if readJSON(filepath.Join(root, "package.json"), &m) != nil || m.Name != id || m.Version == "" {
		return domain.Package{}, false
	}
	bins := m.bins()
	target, ok := bins[path.Name]
	if !ok || !within(filepath.Join(root, filepath.FromSlash(target)), root) || canonical(filepath.Join(root, filepath.FromSlash(target))) != canonical(path.Target) {
		return domain.Package{}, false
	}
	// npm and corepack can be bundled runtime components, not standalone globals.
	if id == "npm" || id == "corepack" {
		return domain.Package{}, false
	}
	p := domain.Package{Manager: "npm", ID: id, Name: id, Version: m.Version, Root: root, Instance: prefix, Scope: "global"}
	for name, entry := range bins {
		if name == "" || strings.ContainsAny(name, "/\\") || !within(filepath.Join(root, filepath.FromSlash(entry)), root) {
			continue
		}
		p.Commands = append(p.Commands, name)
		p.ExecutablePaths = append(p.ExecutablePaths, filepath.Join(root, filepath.FromSlash(entry)))
	}
	p.Evidence = []domain.Evidence{{Kind: "recorded", Source: "global npm package.json bin", Detail: "Observed global prefix entrypoint matches the package's declared script; runtime containment is not package ownership"}}
	p.Commands = unique(p.Commands)
	p.ExecutablePaths = unique(p.ExecutablePaths)
	return p, true
}

func stableMinimum(value, requirement string) bool {
	parse := func(s string) ([3]int, bool) {
		var out [3]int
		parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(s, "v")), ".")
		if len(parts) != 3 {
			return out, false
		}
		for n, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 {
				return out, false
			}
			out[n] = v
		}
		return out, true
	}
	v, ok := parse(value)
	if !ok {
		return false
	}
	if requirement == "" {
		requirement = ">=11.10.0"
	}
	if !strings.HasPrefix(requirement, ">=") {
		return false
	}
	r, ok := parse(strings.TrimPrefix(requirement, ">="))
	if !ok {
		return false
	}
	for n := range v {
		if v[n] != r[n] {
			return v[n] > r[n]
		}
	}
	return true
}

func (e *Engine) npmCommand(i domain.ConflictInstallation, args ...string) domain.Command {
	// npm rejects loading /dev/null twice under two configuration roles. The
	// second path must be verified absent; npm does not create it on uninstall.
	base := []string{i.ManagerPath, "--global", "--prefix", i.Prefix, "--userconfig", os.DevNull, "--globalconfig", filepath.Join(i.Prefix, ".lazypkg-no-global-npmrc"), "--ignore-scripts", "--no-audit", "--no-fund", "--no-update-notifier", "--offline"}
	if cache := e.Env["NPM_CONFIG_CACHE"]; filepath.IsAbs(cache) {
		base = append(base, "--cache", cache)
	}
	c := e.command(i.RuntimePath, append(base, args...)...)
	c.Unset = []string{"NPM_CONFIG_*", "npm_config_*", "NODE_OPTIONS", "NODE_PATH"}
	for key := range c.Env {
		if strings.HasPrefix(strings.ToLower(key), "npm_config_") || key == "NODE_OPTIONS" || key == "NODE_PATH" {
			delete(c.Env, key)
		}
	}
	c.Env["PATH"] = filepath.Dir(i.RuntimePath) + string(os.PathListSeparator) + e.getenv("PATH")
	return c
}

func (e *Engine) npmRemove(i domain.ConflictInstallation) domain.Command {
	return e.npmCommand(i, "uninstall", "--", i.Package.ID)
}

func (e *Engine) selectedNPM(ctx context.Context, m domain.Manager) (string, string) {
	lookup := func(name string) string {
		for _, dir := range filepath.SplitList(e.getenv("PATH")) {
			if !filepath.IsAbs(dir) {
				continue
			}
			path := filepath.Join(dir, name)
			if executable(path, e.GOOS) {
				return canonical(path)
			}
		}
		return ""
	}
	cli, node := canonical(m.Path), lookup("node")
	mise := lookup("mise")
	if mise != "" && cli == mise {
		if value, err := e.output(ctx, e.command(mise, "which", "npm")); err == nil && filepath.IsAbs(strings.TrimSpace(value)) {
			cli = canonical(strings.TrimSpace(value))
		}
	}
	if mise != "" && node == mise {
		if value, err := e.output(ctx, e.command(mise, "which", "node")); err == nil && filepath.IsAbs(strings.TrimSpace(value)) {
			node = canonical(strings.TrimSpace(value))
		}
	}
	return cli, node
}

// Invoking npm uninstall --dry-run still reaches npm's reifyFinish, which can
// rewrite a bundled npmrc. Probe its bundled Arborist directly with dryRun=true:
// no reifyFinish, no scripts/network and all cache output in an owned temp dir.
const npmImpactScript = `const A=require(process.argv[1]);const P=require('node:path');const opts={path:process.argv[2],global:true,dryRun:true,ignoreScripts:true,audit:false,fund:false,offline:true,save:false,packageLock:false,cache:process.argv[4],rm:[process.argv[3]]};(async()=>{const a=new A(opts);const tree=await a.loadActual();const target=tree.children.get(process.argv[3]);if(!target)throw Error('selected package missing');const inside=p=>p===target.realpath||p.startsWith(target.realpath+P.sep);const dependents=new Set(),required=new Set();if(tree.inventory.size>20000)throw Error('dependency graph exceeds inspection limit');for(const n of tree.inventory.values()){if(n.isLink){if(inside(n.realpath)&&!inside(n.path))dependents.add(n.name);if(inside(n.path)&&!inside(n.realpath))required.add(n.realpath)}for(const edge of n.edgesOut.values()){if(!edge.to||n.isRoot)continue;if(!inside(n.realpath)&&inside(edge.to.realpath))dependents.add(n.name);if(inside(n.realpath)&&!inside(edge.to.realpath))required.add(edge.to.realpath)}}await a.reify(opts);const changes=[];const visit=d=>{if(!d)return;if(d.action){const n=d.actual||d.ideal;changes.push({action:d.action,name:n.name,version:n.package.version,path:n.path})}for(const c of d.children||[])visit(c)};visit(a.diff);console.log(JSON.stringify({changes,dependents:[...dependents],required:[...required]}))})().catch(e=>{console.error(e.message);process.exitCode=1})`

type npmChange struct{ Action, Name, Version, Path string }

func (e *Engine) npm(ctx context.Context, i *domain.ConflictInstallation, m domain.Manager) []string {
	p := i.Package
	prefix, root, id, ok := npmLocation(filepath.Join(p.Root, "package.json"))
	if !ok || id != p.ID || canonical(root) != canonical(p.Root) {
		blocked(i, "The exact global npm prefix is not verified.")
		return nil
	}
	i.Prefix = prefix
	if _, err := os.Lstat(filepath.Join(prefix, ".lazypkg-no-global-npmrc")); err == nil || !os.IsNotExist(err) {
		blocked(i, "The reserved empty npm config path is present or unreadable; no custom npm configuration will be loaded.")
		return nil
	}
	var manifest npmManifest
	if readJSON(filepath.Join(root, "package.json"), &manifest) != nil || manifest.Name != p.ID || manifest.Version != p.Version {
		blocked(i, "The installed npm manifest changed or is unreadable.")
		return nil
	}
	i.Project = manifest.project(filepath.Join(prefix, "lib"))
	if i.Project == "" {
		i.Project = manifest.project(filepath.Join(prefix, "lib", "node_modules"))
	}
	if e.GOOS == "windows" {
		blocked(i, "Multi-prefix npm removal currently needs the reviewed POSIX Node layout; Windows requires a separate prefix/shim preflight.")
		return nil
	}
	if !m.Available {
		blocked(i, "Repair the selected npm adapter first; another prefix's npm will not bypass its unsupported state.")
		return []string{fileHash(filepath.Join(root, "package.json"))}
	}
	if p.ID == "npm" || p.ID == "corepack" {
		blocked(i, "Bundled npm/runtime components are not global tool removal targets.")
		return nil
	}
	i.RuntimePath = canonical(filepath.Join(prefix, "bin", "node"))
	i.ManagerPath = filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	// Independently installed npm may service the selected Node prefix after a
	// manager repair. Use it only with that exact current Node interpreter.
	selected, node := e.selectedNPM(ctx, m)
	if strings.HasSuffix(selected, string(filepath.Separator)+"npm-cli.js") && node == i.RuntimePath {
		i.ManagerPath = selected
	}
	if !executable(i.RuntimePath, e.GOOS) {
		blocked(i, "A Node interpreter belonging to this prefix was not found.")
		return nil
	}
	if _, err := os.Stat(i.ManagerPath); err != nil {
		blocked(i, "The exact npm CLI in this Node context is missing.")
		return nil
	}
	version, err := e.output(ctx, e.npmCommand(*i, "--version"))
	if err != nil {
		blocked(i, "The bound Node/npm version probe failed: "+err.Error())
		return nil
	}
	if !stableMinimum(strings.TrimSpace(version), m.Requirement) {
		blocked(i, "This prefix's npm does not satisfy the adapter requirement; repair this context before removal.")
		return nil
	}
	globalRoot, err := e.output(ctx, e.npmCommand(*i, "root"))
	if err != nil || canonical(strings.TrimSpace(globalRoot)) != canonical(filepath.Join(prefix, "lib", "node_modules")) {
		blocked(i, "The explicit npm global root could not be verified.")
		return nil
	}
	i.Commands = nil
	for command, target := range manifest.bins() {
		if command == "" || strings.ContainsAny(command, "/\\") || !within(filepath.Join(root, filepath.FromSlash(target)), root) {
			blocked(i, "npm bin metadata points outside its package.")
			continue
		}
		public := filepath.Join(prefix, "bin", command)
		if canonical(public) != canonical(filepath.Join(root, filepath.FromSlash(target))) {
			blocked(i, "A global command is missing or owned by a different package: "+command)
		}
		i.Commands = append(i.Commands, command)
	}
	if len(i.Commands) == 0 {
		blocked(i, "This npm package has no verified public commands.")
	}
	impact, dependencies, required, binding, err := e.npmImpact(ctx, *i)
	if err != nil {
		blocked(i, "npm removal effects could not be verified: "+err.Error())
		return nil
	}
	if len(impact) == 0 {
		blocked(i, "npm reported no removal for the selected installation.")
	}
	i.Dependents = append(i.Dependents, dependencies...)
	i.RequiredPaths = unique(required)
	if len(dependencies) > 0 {
		blocked(i, "Other packages refer to this global npm installation: "+strings.Join(unique(dependencies), ", "))
	}
	targetFound := false
	for _, change := range impact {
		if change.Action != "REMOVE" || !within(change.Path, root) {
			blocked(i, "npm's transaction includes changes outside this tool's directory.")
		}
		if canonical(change.Path) == canonical(root) && change.Name == p.ID && change.Version == p.Version {
			targetFound = true
		}
	}
	if !targetFound {
		blocked(i, "npm did not identify the exact selected package/version in its removal transaction.")
	}
	i.Evidence = append(i.Evidence, domain.Evidence{Kind: "recorded", Source: "npm bundled Arborist dry-run", Detail: "Exact Node/npm context; every planned package change is restricted to the selected tool directory"})
	i.Warnings = append(i.Warnings, "Removes this global package and its private dependencies and commands in the explicit prefix. npm may refresh metadata in this prefix; scripts, audit, downloads and user npmrc are disabled.")
	return append(binding, strings.TrimSpace(version), fileHash(filepath.Join(root, "package.json")), fileHash(filepath.Join(prefix, "lib", "node_modules", ".package-lock.json")))
}

func (e *Engine) npmImpact(ctx context.Context, i domain.ConflictInstallation) ([]npmChange, []string, []string, []string, error) {
	module := filepath.Join(filepath.Dir(filepath.Dir(i.ManagerPath)), "node_modules", "@npmcli", "arborist")
	if _, err := os.Stat(filepath.Join(module, "package.json")); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("bundled Arborist is unavailable")
	}
	dir, err := os.MkdirTemp("", "lazypkg-resolution-npm-*")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer os.RemoveAll(dir)
	c := e.npmCommand(i)
	c.Args = []string{"-e", npmImpactScript, module, filepath.Join(i.Prefix, "lib"), i.Package.ID, dir}
	c.Dir = dir
	text, err := e.output(ctx, c)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var result struct {
		Changes    []npmChange `json:"changes"`
		Dependents []string    `json:"dependents"`
		Required   []string    `json:"required"`
	}
	if err = json.Unmarshal([]byte(text), &result); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unrecognized npm impact output")
	}
	sort.Slice(result.Changes, func(a, b int) bool { return result.Changes[a].Path < result.Changes[b].Path })
	return result.Changes, result.Dependents, result.Required, []string{digest(result.Changes), digest(unique(result.Dependents)), digest(unique(result.Required)), fileHash(filepath.Join(module, "package.json"))}, nil
}
