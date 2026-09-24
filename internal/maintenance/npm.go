package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type toolEntry struct {
	Version   string `json:"version"`
	Path      string `json:"install_path"`
	Installed *bool  `json:"installed"`
	Requested string `json:"requested_version"`
	Source    *struct {
		Path string `json:"path"`
	} `json:"source"`
}
type toolList map[string][]toolEntry

func (e *Engine) global(ctx context.Context, mise string, args ...string) (string, error) {
	dir, err := os.MkdirTemp("", "lazypkg-manager-global-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	dir = canonical(dir)
	c := e.command(mise, args...)
	c.Dir = dir
	c.Env["MISE_CEILING_PATHS"] = dir
	for k := range e.environment() {
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "MISE_") && strings.HasSuffix(upper, "_VERSION") {
			c.Unset = append(c.Unset, k)
			delete(c.Env, k)
		}
	}
	return e.output(ctx, c)
}
func parseTools(text string) (toolList, error) {
	var tools toolList
	err := json.Unmarshal([]byte(text), &tools)
	if err == nil && tools == nil {
		err = errors.New("mise returned no structured tool list")
	}
	return tools, err
}
func (e *Engine) globalConfig() string {
	if p := e.getenv("MISE_GLOBAL_CONFIG_FILE"); p != "" {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(e.Dir, p)
	}
	dir := e.getenv("MISE_CONFIG_DIR")
	if !filepath.IsAbs(dir) {
		dir = e.getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.Home, ".config")
		}
		dir = filepath.Join(dir, "mise")
	}
	return filepath.Join(dir, "config.toml")
}

func (e *Engine) observeNPM(ctx context.Context, o *observation) {
	h := &o.Health
	h.GuideURL = "https://docs.npmjs.com/downloading-and-installing-node-js-and-npm/"
	h.Recommendation = "npm must be updated within its selected Node context. An npm later on PATH is a different installation."
	node := e.lookup("node")
	if node == "" {
		return
	}
	mise := e.lookup("mise")
	if mise != "" && canonical(node) == canonical(mise) {
		resolved, err := e.query(ctx, mise, "which", "node")
		if err != nil || !filepath.IsAbs(strings.TrimSpace(resolved)) {
			return
		}
		node = strings.TrimSpace(resolved)
	}
	node = canonical(node)
	h.Runtime = "node"
	h.RuntimePath = node
	text, err := e.query(ctx, node, "--version")
	if err != nil {
		return
	}
	v, ok := version(text)
	if !ok {
		return
	}
	h.RuntimeVersion = v.String()
	if e.GOOS == "windows" {
		h.Recommendation = "The verified independent npm recipe uses aqua:npm/cli, whose registry currently supports macOS/Linux. Keep this Node context and follow the official npm/mise Windows guidance; no alternative npm is selected."
		return
	}
	o.Nodes = append(o.Nodes, v.String())
	o.Binding = append(o.Binding, identity(node))
	marker := string(filepath.Separator) + "installs" + string(filepath.Separator) + "node" + string(filepath.Separator)
	miseBase, rest, ok := strings.Cut(node, marker)
	if !ok {
		return
	}
	parts := strings.Split(rest, string(filepath.Separator))
	if len(parts) < 3 || parts[1] != "bin" {
		return
	}
	nodeRoot := filepath.Join(miseBase, "installs", "node", parts[0])
	if mise == "" {
		return
	}
	h.GuideURL = "https://mise.jdx.dev/lang/node.html"
	npmTarget := canonical(h.Path)
	if npmTarget == canonical(mise) {
		resolved, err := e.query(ctx, mise, "which", "npm")
		if err != nil || !filepath.IsAbs(strings.TrimSpace(resolved)) {
			return
		}
		npmTarget = canonical(strings.TrimSpace(resolved))
	}
	if !within(npmTarget, nodeRoot) && !within(npmTarget, filepath.Join(miseBase, "installs", "npm")) && !within(npmTarget, filepath.Join(miseBase, "installs", "aqua-npm-cli")) {
		h.Owner = "unknown"
		h.Recommendation = "Node is managed by mise but this npm executable belongs to another prefix. Use its original owner; no Node or PATH replacement is proposed."
		return
	}
	cli := filepath.Join(nodeRoot, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if !within(npmTarget, nodeRoot) {
		if strings.HasSuffix(npmTarget, "npm-cli.js") {
			cli = npmTarget
		} else {
			return
		}
	}
	if _, err := os.Stat(cli); err != nil {
		return
	}
	o.NPMCLI = cli
	o.NPMEntrypoint = npmTarget
	text, err = e.query(ctx, node, cli, "--version")
	if err != nil {
		return
	}
	npmVersion, ok := version(text)
	if !ok {
		return
	}
	h.Version = npmVersion.String()
	prefix, err := e.query(ctx, node, cli, "prefix", "--global")
	if err != nil {
		return
	}
	prefix = strings.TrimSpace(prefix)
	if !filepath.IsAbs(prefix) {
		return
	}
	h.Prefix = canonical(prefix)
	h.Owner = "mise"
	h.OwnerPath = mise
	h.OwnerPackage = "node"
	if !within(npmTarget, nodeRoot) {
		h.OwnerPackage = "npm"
	}
	h.Channel = "npm major " + fmt.Sprint(npmVersion.major)
	h.ConfigPath = e.globalConfig()
	configDigest := fileDigest(h.ConfigPath)
	if configDigest == "unreadable" {
		h.Recommendation = "The global mise configuration is unreadable; review it before planning npm activation."
		return
	}
	o.Binding = append(o.Binding, identity(mise), identity(cli), identity(npmTarget), h.Prefix, identity(h.ConfigPath), configDigest, e.getenv("MISE_ENV"), e.getenv("MISE_NODE_VERSION"), e.getenv("NPM_CONFIG_PREFIX"), e.getenv("npm_config_prefix"))
	if e.getenv("MISE_ENV") != "" {
		h.Recommendation = "An environment-specific mise profile is active. Review an explicit npm pin in that profile; this recipe will not guess which global configuration layer to change."
		return
	}
	installedText, err := e.query(ctx, mise, "ls", "--installed", "--json")
	if err != nil {
		return
	}
	installed, err := parseTools(installedText)
	if err != nil {
		return
	}
	found := false
	for _, entry := range installed["node"] {
		if canonical(entry.Path) == canonical(nodeRoot) && entry.Version == v.String() && (entry.Installed == nil || *entry.Installed) {
			found = true
			break
		}
	}
	if !found {
		h.Recommendation = "The selected Node path could not be matched to mise's installed records."
		return
	}
	currentText, err := e.query(ctx, mise, "ls", "--current", "--json")
	if err != nil {
		return
	}
	current, err := parseTools(currentText)
	if err != nil {
		return
	}
	globalText, err := e.global(ctx, mise, "ls", "--global", "--json")
	if err != nil {
		return
	}
	global, err := parseTools(globalText)
	if err != nil {
		return
	}
	if len(global["node"]) == 0 {
		h.Recommendation = "No global mise Node context was verified. Select a global Node runtime before adding a global npm."
		return
	}
	for _, tools := range []toolList{current, global} {
		for id, entries := range tools {
			for _, entry := range entries {
				if entry.Source != nil && entry.Source.Path != "" {
					o.Binding = append(o.Binding, identity(entry.Source.Path), fileDigest(entry.Source.Path))
				}
				if id != "node" {
					continue
				}
				if _, ok := version(entry.Version); !ok {
					h.Recommendation = "A current or global Node context has a non-numeric version; compatibility needs manual review."
					return
				}
				o.Nodes = append(o.Nodes, entry.Version)
				o.Binding = append(o.Binding, entry.Version, entry.Requested, identity(filepath.Join(entry.Path, "bin", nodeName(e.GOOS))))
			}
		}
	}
	o.GlobalNode = global["node"][0].Version
	h.Channel = "npm major " + fmt.Sprint(npmVersion.major) + "; Node " + global["node"][0].Requested
	for _, entries := range global {
		for _, entry := range entries {
			if entry.Source != nil {
				o.Binding = append(o.Binding, entry.Source.Path, fileDigest(entry.Source.Path))
			}
		}
	}
	registry, err := e.query(ctx, mise, "registry", "npm", "--json")
	if err != nil {
		h.Recommendation = "This mise does not expose independent npm registry support. Update mise or follow its Node/npm guide."
		return
	}
	var reg struct {
		Short    string   `json:"short"`
		Backends []string `json:"backends"`
	}
	if json.Unmarshal([]byte(registry), &reg) != nil || reg.Short != "npm" {
		return
	}
	supported := false
	for _, backend := range reg.Backends {
		supported = supported || backend == "aqua:npm/cli"
	}
	if !supported {
		h.Recommendation = "Independent npm installation through the verified aqua:npm/cli backend is unavailable in this mise."
		return
	}
	// Do not introduce a second differently configured npm provider. A prior
	// explicit aqua installation from this workflow is safe to update in place.
	for id, entries := range global {
		if id == "npm" || id == "aqua:npm/cli" {
			if len(entries) > 0 && within(npmTarget, nodeRoot) {
				h.Recommendation = "A separate npm is already configured globally. Activate the shell and inspect that selection before adding another npm."
				return
			}
			for _, entry := range entries {
				if entry.Source != nil && entry.Source.Path != "" && canonical(entry.Source.Path) != canonical(h.ConfigPath) {
					h.Recommendation = "The global npm selection comes from another mise configuration layer. Review an update to that exact file; the default global file will not be changed."
					return
				}
			}
		}
	}
	for id, entries := range current {
		if id != "npm" && id != "aqua:npm/cli" {
			continue
		}
		for _, entry := range entries {
			if entry.Source != nil && entry.Source.Path != "" && canonical(entry.Source.Path) != canonical(h.ConfigPath) {
				h.Recommendation = "The current project selects npm independently. A global pin would not update that selection; review its project configuration explicitly."
				return
			}
		}
	}
	o.MiseRegistry = registry
	o.Binding = append(o.Binding, registry, hash(current), hash(global))
	sort.Strings(o.Nodes)
	h.Strategy = "npm-mise"
	h.Recommendation = "Keep Node and its bundled npm wrapper unchanged. Install a compatible same-major npm independently with mise and explicitly pin it in the global configuration. Existing global packages remain in their Node-specific prefix."
}
func nodeName(goos string) string {
	if goos == "windows" {
		return "node.exe"
	}
	return "node"
}

type npmVersionInfo struct {
	Version    string `json:"version"`
	Deprecated string `json:"deprecated"`
	Engines    struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func (e *Engine) npmUpdate(ctx context.Context, o *observation) error {
	var data struct {
		Versions map[string]npmVersionInfo `json:"versions"`
	}
	if err := e.getJSON(ctx, strings.TrimRight(e.RegistryURL, "/")+"/npm", &data); err != nil {
		return err
	}
	current, ok := version(o.Health.Version)
	if !ok {
		return errors.New("npm's current version is not stable")
	}
	var candidate semver
	found := false
	requirement := o.Health.Requirement
	if requirement == "" {
		requirement = ">=11.10.0"
	}
	for key, info := range data.Versions {
		v, ok := version(key)
		if !ok || v.major != current.major || v.compare(semver{11, 10, 0}) < 0 || info.Deprecated != "" || !minimum(v, requirement) || v.compare(current) < 0 {
			continue
		}
		compatible := true
		for _, n := range o.Nodes {
			node, ok := version(n)
			match, known := satisfies(node, info.Engines.Node)
			if !ok || !known || !match {
				compatible = false
				break
			}
		}
		if compatible && (!found || v.compare(candidate) > 0) {
			found = true
			candidate = v
		}
	}
	if !found {
		o.Health.UpdateStatus = "blocked"
		o.Health.Recommendation = "No stable npm in the current major satisfies both the backend minimum and every observed current/global Node context. Review a runtime change explicitly."
		return nil
	}
	o.Health.CandidateVersion = candidate.String()
	o.Health.UpdateStatus = "current"
	if candidate.compare(current) > 0 {
		o.Health.UpdateStatus = "available"
		o.Health.ApplySupported = true
	}
	return nil
}

func (e *Engine) npmVersionCheck(ctx context.Context, o observation, target string) error {
	v, ok := version(target)
	current, currentOK := version(o.Health.Version)
	if !ok || !currentOK || v.major != current.major || v.compare(semver{11, 10, 0}) < 0 || !minimum(v, o.Health.Requirement) {
		return errors.New("reviewed npm target no longer satisfies the selected manager requirement")
	}
	var data npmVersionInfo
	if err := e.getJSON(ctx, strings.TrimRight(e.RegistryURL, "/")+"/npm/"+target, &data); err != nil {
		return err
	}
	if data.Version != target || data.Deprecated != "" {
		return errors.New("reviewed npm release metadata is unavailable or deprecated")
	}
	for _, n := range o.Nodes {
		node, ok := version(n)
		match, known := satisfies(node, data.Engines.Node)
		if !ok || !known || !match {
			return errors.New("reviewed npm version is incompatible with an observed Node context")
		}
	}
	return nil
}

func (e *Engine) npmCommands(h domain.ManagerHealth) []domain.Step {
	spec := "aqua:npm/cli@" + h.CandidateVersion
	install := e.command(h.OwnerPath, "install", spec)
	activate := e.command(h.OwnerPath, "use", "--global", "--pin", spec)
	for _, c := range []*domain.Command{&install, &activate} {
		c.Env["MISE_GLOBAL_CONFIG_FILE"] = h.ConfigPath
		c.Env["MISE_NODE_VERSION"] = h.RuntimeVersion
		c.Env["PATH"] = filepath.Dir(h.RuntimePath) + string(os.PathListSeparator) + e.getenv("PATH")
	}
	return []domain.Step{{ID: "manager-install", Description: "Install independent npm " + h.CandidateVersion + " with mise; keep Node and bundled npm unchanged", Command: install}, {ID: "manager-activate", Description: "Pin independent npm in global configuration " + h.ConfigPath, Command: activate, DependsOn: []string{"manager-install"}}}
}
