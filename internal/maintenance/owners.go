package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (e *Engine) brewOwner(ctx context.Context, o *observation) bool {
	brew := e.lookup("brew")
	if brew == "" {
		return false
	}
	target := canonical(o.Health.Path)
	// Probe Homebrew only for a plausible Cellar target, avoiding a subprocess
	// for every unrelated native manager in a catalog scan.
	marker := string(filepath.Separator) + "Cellar" + string(filepath.Separator)
	if !strings.Contains(target, marker) {
		return false
	}
	cellar, err := e.query(ctx, brew, "--cellar")
	if err != nil {
		return false
	}
	cellar = canonical(strings.TrimSpace(cellar))
	if !within(target, cellar) {
		return false
	}
	rel, _ := filepath.Rel(cellar, target)
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 3 {
		return false
	}
	pkg := parts[0]
	text, err := e.query(ctx, brew, "info", "--json=v2", pkg)
	if err != nil {
		return false
	}
	var data struct {
		Formulae []struct {
			Name     string `json:"name"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
			Installed []struct {
				Version string `json:"version"`
			} `json:"installed"`
		} `json:"formulae"`
	}
	if json.Unmarshal([]byte(text), &data) != nil {
		return false
	}
	for _, f := range data.Formulae {
		if f.Name != pkg {
			continue
		}
		owned := false
		for _, i := range f.Installed {
			owned = owned || i.Version == parts[1]
		}
		if !owned {
			return false
		}
		h := &o.Health
		h.Owner = "brew"
		h.OwnerPath = brew
		h.OwnerPackage = pkg
		h.GuideURL = "https://docs.brew.sh/Manpage"
		o.Binding = append(o.Binding, identity(brew), text)
		formulaName, _, _ := strings.Cut(pkg, "@")
		formulaVersion, _, _ := strings.Cut(parts[1], "_")
		installed, installedOK := version(formulaVersion)
		component, componentOK := version(h.Version)
		if (formulaName != h.Manager && formulaName != executableName(h.Manager)) || !installedOK || !componentOK || installed.compare(component) != 0 {
			h.Recommendation = "Homebrew records this executable under " + pkg + ", but a matching component/formula version was not established. Review the owning formula and its bundled component separately; no automatic host or runtime update is planned."
			return true
		}
		if h.ReasonCode != "" && h.ReasonCode != "ready" && h.ReasonCode != "version_unsupported" {
			h.Recommendation = "The selected component probe needs repair before comparing its Homebrew update target. Inspect the reported probe issue and the owning formula " + pkg + "."
			return true
		}
		h.Strategy = "brew-package"
		h.CandidateVersion = f.Versions.Stable
		h.Recommendation = "Upgrade the owning Homebrew formula only; its native dependency changes will be shown by Homebrew."
		h.UpdateStatus = "unknown"
		if a, ok := version(h.Version); ok {
			if b, ok := version(f.Versions.Stable); ok {
				h.UpdateStatus = "current"
				if a.compare(b) < 0 {
					h.UpdateStatus = "available"
					h.ApplySupported = true
				}
			}
		}
		return true
	}
	return false
}

func (e *Engine) uvOwner(o *observation) {
	o.Health.GuideURL = "https://docs.astral.sh/uv/getting-started/installation/"
	config := e.getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(e.Home, ".config")
	}
	receipt := filepath.Join(config, "uv", "uv-receipt.json")
	var record struct {
		Prefix   string   `json:"install_prefix"`
		Binaries []string `json:"binaries"`
		Source   struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
			App   string `json:"app_name"`
		} `json:"source"`
	}
	if readJSON(receipt, &record) != nil {
		return
	}
	target := canonical(o.Health.Path)
	expected := filepath.Join(record.Prefix, "uv")
	if e.GOOS == "windows" {
		expected += ".exe"
	}
	if record.Source.Owner != "astral-sh" || record.Source.App != "uv" || canonical(expected) != target {
		return
	}
	o.Health.Owner = "standalone"
	o.Health.OwnerPath = o.Health.Path
	o.Health.Prefix = record.Prefix
	o.Health.Strategy = "uv-self"
	o.Health.Channel = "stable"
	o.Health.GuideURL = "https://docs.astral.sh/uv/getting-started/installation/"
	o.Health.Recommendation = "Update this standalone uv installation using its installer receipt; shell profile changes are disabled."
	o.Binding = append(o.Binding, receipt, fileDigest(receipt))
}
func (e *Engine) miseOwner(ctx context.Context, o *observation) {
	o.Health.GuideURL = "https://mise.jdx.dev/cli/self-update.html"
	text, err := e.query(ctx, o.Health.Path, "doctor", "--json")
	if err != nil {
		return
	}
	// Never retain doctor JSON: it can contain personal environment variables.
	var data struct {
		Self  bool `json:"self_update_available"`
		Build struct {
			Features string `json:"features"`
		} `json:"build_info"`
	}
	if json.Unmarshal([]byte(text), &data) != nil || !data.Self || !strings.Contains(data.Build.Features, "self_update") {
		return
	}
	o.Health.Owner = "self-update capable"
	o.Health.OwnerPath = o.Health.Path
	o.Health.Strategy = "mise-self"
	o.Health.Channel = "stable"
	o.Health.GuideURL = "https://mise.jdx.dev/cli/self-update.html"
	o.Health.Recommendation = "The selected binary reports native self-update support. Update mise only; plugin updates are disabled."
	o.Binding = append(o.Binding, data.Build.Features)
}
func (e *Engine) cargoOwner(ctx context.Context, o *observation) {
	rustup := e.lookup("rustup")
	if rustup == "" || canonical(o.Health.Path) != canonical(rustup) {
		return
	}
	toolchain, err := e.query(ctx, rustup, "show", "active-toolchain")
	if err != nil || len(strings.Fields(toolchain)) == 0 {
		return
	}
	target, err := e.query(ctx, rustup, "which", "cargo")
	if err != nil {
		return
	}
	o.Health.Owner = "rustup"
	o.Health.OwnerPath = rustup
	o.Health.Runtime = "Rust toolchain"
	o.Health.RuntimePath = strings.TrimSpace(target)
	o.Health.Channel = strings.Fields(toolchain)[0]
	o.Health.GuideURL = "https://rust-lang.github.io/rustup/basics.html"
	o.Health.Recommendation = "Cargo belongs to Rust toolchain " + o.Health.Channel + ". Review `rustup update <selected-channel> --no-self-update`; a pinned toolchain requires a separate version-selection decision. `cargo update` updates project dependencies, not Cargo."
	o.Binding = append(o.Binding, toolchain, target, identity(rustup), e.getenv("RUSTUP_TOOLCHAIN"))
}
func (e *Engine) runtimeOwner(ctx context.Context, o *observation, name string) {
	if name == "gem" {
		o.Health.GuideURL = "https://guides.rubygems.org/installation/"
	} else {
		o.Health.GuideURL = "https://go.dev/doc/manage-install"
	}
	target := canonical(o.Health.Path)
	if target == "/usr/bin/gem" {
		o.Health.Owner = "operating system"
		o.Health.Recommendation = "This is the operating system's RubyGems. Use OS maintenance or explicitly select a separate Ruby runtime; do not overwrite the system Ruby installation."
		return
	}
	marker := string(filepath.Separator) + "installs" + string(filepath.Separator)
	prefix, rest, ok := strings.Cut(target, marker)
	if !ok {
		return
	}
	parts := strings.Split(rest, string(filepath.Separator))
	if len(parts) < 3 {
		return
	}
	kind := parts[0]
	if name == "gem" && kind != "ruby" || name == "go" && kind != "go" {
		return
	}
	mise := e.lookup("mise")
	if mise == "" {
		return
	}
	root := filepath.Join(prefix, "installs", parts[0], parts[1])
	o.Health.Owner = "mise"
	o.Health.OwnerPath = mise
	o.Health.OwnerPackage = kind
	o.Health.Runtime = kind
	o.Health.RuntimeVersion = parts[1]
	o.Health.RuntimePath = root
	o.Health.Channel = "configured runtime"
	o.Health.GuideURL = "https://mise.jdx.dev/cli/upgrade.html"
	if name == "go" {
		o.Health.Recommendation = "Update the owning mise Go runtime while retaining its configured channel. `go get` is not a Go self-update command."
	} else {
		o.Health.Recommendation = "RubyGems belongs to the selected mise Ruby. Review that Ruby runtime or an interpreter-bound `gem update --system <version>`; the system Ruby and other Ruby installations must remain separate."
	}
	o.Binding = append(o.Binding, identity(mise))
}
func (e *Engine) scoopOwner(o *observation) {
	root := e.getenv("SCOOP")
	if !filepath.IsAbs(root) {
		root = filepath.Join(e.Home, "scoop")
	}
	script := filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1")
	if _, err := os.Stat(script); err != nil {
		return
	}
	target := canonical(o.Health.Path)
	if target != canonical(script) && !within(target, filepath.Join(root, "shims")) {
		return
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "scoop", "current", ".git")); err != nil {
		return
	}
	o.Health.Owner = "Scoop"
	o.Health.OwnerPath = script
	o.Health.Strategy = "scoop-update"
	o.Health.GuideURL = "https://github.com/ScoopInstaller/Scoop/wiki/Commands"
	o.Health.Recommendation = "Refresh this Scoop installation and its bucket metadata. Installed apps are not upgraded by this manager refresh."
	o.Binding = append(o.Binding, identity(script))
}
