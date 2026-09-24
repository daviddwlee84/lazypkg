package resolution

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type brewFormula struct {
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	Homepage  string `json:"homepage"`
	LinkedKeg string `json:"linked_keg"`
	URLs      struct {
		Stable struct {
			URL string `json:"url"`
		} `json:"stable"`
	} `json:"urls"`
	Installed []struct {
		Version string `json:"version"`
	} `json:"installed"`
}

func (e *Engine) brew(ctx context.Context, i *domain.ConflictInstallation) []string {
	if i.ManagerPath == "" {
		return nil
	}
	p := i.Package
	text, err := e.output(ctx, e.command(i.ManagerPath, "info", "--json=v2", "--formula", p.ID))
	var result struct {
		Formulae []brewFormula `json:"formulae"`
	}
	if err != nil || json.Unmarshal([]byte(text), &result) != nil || len(result.Formulae) != 1 {
		blocked(i, "Homebrew formula metadata could not be verified.")
		return nil
	}
	f := result.Formulae[0]
	if p.ID != f.Name && p.ID != f.FullName {
		blocked(i, "Homebrew returned a different formula identity.")
		return nil
	}
	i.Project = projectURL(f.Homepage)
	if i.Project == "" {
		i.Project = projectURL(f.URLs.Stable.URL)
	}
	cellar, err := e.output(ctx, e.command(i.ManagerPath, "--cellar"))
	cellar = strings.TrimSpace(cellar)
	if err != nil || !filepath.IsAbs(cellar) || len(f.Installed) != 1 || f.Installed[0].Version != p.Version || canonical(p.Root) != canonical(filepath.Join(cellar, f.Name, p.Version)) {
		blocked(i, "The exact single Homebrew keg/version is not verified; multiple retained kegs require native review.")
	}
	deps, err := e.output(ctx, e.command(i.ManagerPath, "uses", "--installed", "--recursive", p.ID))
	if err != nil {
		blocked(i, "Installed Homebrew dependents could not be checked.")
	} else {
		for _, dep := range strings.Fields(deps) {
			i.Dependents = append(i.Dependents, dep)
		}
		if len(i.Dependents) > 0 {
			blocked(i, "Installed packages depend on this formula: "+strings.Join(unique(i.Dependents), ", "))
		}
	}
	if len(i.Commands) == 0 {
		blocked(i, "The formula's complete executable impact is unknown.")
	}
	i.Evidence = append(i.Evidence, domain.Evidence{Kind: "recorded", Source: "brew info and brew uses --installed --recursive", Detail: "Exact formula keg and installed reverse dependencies queried without refreshing metadata"})
	i.Warnings = append(i.Warnings, "Removes the whole formula, including all its recorded commands. No force, dependency override, autoremove or cleanup is requested.")
	return []string{digest(f), deps, identity(p.Root), fileHash(filepath.Join(p.Root, "INSTALL_RECEIPT.json"))}
}

type uvRecord struct {
	ID, Version, Root string
	Paths             []string
}

func uvRecords(text string) []uvRecord {
	out := []uvRecord{}
	index := -1
	s := bufio.NewScanner(strings.NewReader(text))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		label, path, ok := strings.Cut(line, " (")
		if !ok || !strings.HasSuffix(path, ")") {
			continue
		}
		path = strings.TrimSuffix(path, ")")
		if !filepath.IsAbs(path) {
			continue
		}
		if strings.HasPrefix(label, "- ") {
			if index >= 0 {
				out[index].Paths = append(out[index].Paths, path)
			}
			continue
		}
		id, version, ok := strings.Cut(label, " v")
		if !ok {
			continue
		}
		out = append(out, uvRecord{ID: id, Version: version, Root: path})
		index = len(out) - 1
	}
	return out
}

func pythonProject(root, id string) (string, []string) {
	// Inspect only the tool environment's own dist-info metadata, not user code.
	files, _ := filepath.Glob(filepath.Join(root, "lib", "python*", "site-packages", "*.dist-info", "METADATA"))
	windowsFiles, _ := filepath.Glob(filepath.Join(root, "Lib", "site-packages", "*.dist-info", "METADATA"))
	files = append(files, windowsFiles...)
	if len(files) > 1024 {
		return "", nil
	}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(io.LimitReader(f, 1<<20))
		name := ""
		project := ""
		for s.Scan() {
			line := s.Text()
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Name: ") {
				name = strings.TrimPrefix(line, "Name: ")
			}
			if strings.HasPrefix(line, "Home-page: ") {
				if p := projectURL(strings.TrimPrefix(line, "Home-page: ")); p != "" {
					project = p
				}
			}
			if strings.HasPrefix(line, "Project-URL: ") {
				_, raw, ok := strings.Cut(line, ", ")
				if ok {
					if p := projectURL(raw); p != "" {
						project = p
					}
				}
			}
		}
		_ = f.Close()
		if domain.NormalizePackageID("uvx", name) == domain.NormalizePackageID("uvx", id) {
			return project, []string{fileHash(path)}
		}
	}
	return "", nil
}

func (e *Engine) uv(ctx context.Context, i *domain.ConflictInstallation) []string {
	p := i.Package
	i.Project, _ = pythonProject(p.Root, p.ID)
	if p.ID == "meta-package-manager" {
		blocked(i, "meta-package-manager is the active package backend; switch backend before removing it.")
	}
	if i.ManagerPath == "" {
		return nil
	}
	// The catalog's uvx adapter invokes uv, but some snapshots expose a uvx path.
	if filepath.Base(i.ManagerPath) == "uvx" || filepath.Base(i.ManagerPath) == "uvx.exe" {
		name := "uv"
		if e.GOOS == "windows" {
			name += ".exe"
		}
		i.ManagerPath = filepath.Join(filepath.Dir(i.ManagerPath), name)
	}
	text, err := e.output(ctx, e.command(i.ManagerPath, "--color", "never", "--no-progress", "tool", "list", "--show-paths"))
	if err != nil {
		blocked(i, "The selected uv tool registry could not be read.")
		return nil
	}
	var found *uvRecord
	for _, record := range uvRecords(text) {
		if domain.NormalizePackageID("uvx", record.ID) == domain.NormalizePackageID("uvx", p.ID) && record.Version == p.Version && canonical(record.Root) == canonical(p.Root) {
			r := record
			found = &r
			break
		}
	}
	if found == nil || len(found.Paths) == 0 {
		blocked(i, "The exact uv tool environment and installed entrypoints could not be verified.")
		return nil
	}
	i.Prefix = filepath.Dir(canonical(p.Root))
	binDir := ""
	for _, path := range found.Paths {
		if binDir != "" && canonical(filepath.Dir(path)) != binDir {
			blocked(i, "uv entrypoints span multiple bin directories; effects require native review.")
		}
		binDir = canonical(filepath.Dir(path))
		i.Commands = append(i.Commands, filepath.Base(path))
		if !within(canonical(path), p.Root) {
			if e.GOOS == "windows" {
				blocked(i, "Copied Windows uv launchers need additional current-file ownership proof; registration alone does not authorize removal.")
			} else {
				blocked(i, "A registered uv entrypoint no longer targets its tool environment.")
			}
		}
	}
	// Keep the public bin directory separate from the environment's bin directory.
	i.BinDir = binDir
	i.Evidence = append(i.Evidence, domain.Evidence{Kind: "recorded", Source: "uv tool list --show-paths", Detail: "One isolated tool environment and its registered public entrypoints"})
	i.Warnings = append(i.Warnings, "Removes the entire isolated uv tool environment and all its registered commands.")
	_, binding := pythonProject(p.Root, p.ID)
	return append(binding, digest(found), fileHash(filepath.Join(p.Root, "uv-receipt.toml")))
}

func (e *Engine) removeCommand(i domain.ConflictInstallation) (domain.Command, error) {
	switch i.Package.Manager {
	case "brew":
		return e.command(i.ManagerPath, "uninstall", "--formula", i.Package.ID), nil
	case "uvx":
		c := e.command(i.ManagerPath, "--color", "never", "--no-progress", "tool", "uninstall", i.Package.ID)
		c.Env["UV_TOOL_DIR"] = i.Prefix
		c.Env["UV_TOOL_BIN_DIR"] = i.BinDir
		return c, nil
	case "npm":
		return e.npmRemove(i), nil
	}
	return domain.Command{}, fmt.Errorf("%s has no reviewed removal adapter", i.Package.Manager)
}
