package diagnostics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// Enrich returns a copy, keeping existing provider records usable when any one
// native query fails. Each manager has an independent bounded query budget.
func (e *Engine) Enrich(ctx context.Context, packages []domain.Package) ([]domain.Package, []domain.Issue) {
	v := e.defaults()
	out := make([]domain.Package, len(packages))
	groups := map[string][]int{}
	for i, p := range packages {
		out[i] = p
		out[i].Commands = append([]string(nil), p.Commands...)
		out[i].ExecutablePaths = append([]string(nil), p.ExecutablePaths...)
		out[i].Evidence = append([]domain.Evidence(nil), p.Evidence...)
		groups[p.Manager] = append(groups[p.Manager], i)
	}
	var wg sync.WaitGroup
	issueCh := make(chan domain.Issue, len(groups))
	limit := make(chan struct{}, 3)
	for manager, indices := range groups {
		if manager != "brew" && manager != "apt" && manager != "npm" && manager != "uv" && manager != "uvx" && manager != "cargo" && manager != "scoop" && manager != "mise" {
			continue
		}
		wg.Add(1)
		go func(manager string, indices []int) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				issueCh <- domain.Issue{Manager: manager, Message: ctx.Err().Error()}
				return
			}
			work, cancel := context.WithTimeout(ctx, 9*time.Second)
			defer cancel()
			rows := make([]*domain.Package, 0, len(indices))
			for _, i := range indices {
				rows = append(rows, &out[i])
			}
			var err error
			switch manager {
			case "brew":
				err = v.brew(work, rows)
			case "apt":
				err = v.dpkg(work, rows)
			case "npm":
				err = v.npm(work, rows)
			case "uv", "uvx":
				err = v.uv(work, rows)
			case "cargo":
				err = v.cargo(work, rows)
			case "scoop":
				if v.GOOS == "windows" {
					err = v.scoop(work, rows)
				}
			case "mise":
				for _, p := range rows {
					if err = work.Err(); err != nil {
						break
					}
					if p.Root != "" {
						v.binPaths(p)
						evidence(p, "mise", "Installed runtime path reported by mise; multiple versions can be intentional")
					}
				}
			}
			if err != nil {
				issueCh <- domain.Issue{Manager: manager, Message: "Executable ownership enrichment is partial: " + err.Error()}
			}
		}(manager, indices)
	}
	wg.Wait()
	close(issueCh)
	var issues []domain.Issue
	for issue := range issueCh {
		issues = append(issues, issue)
	}
	for i := range out {
		out[i].Commands = sortedUnique(out[i].Commands)
		out[i].ExecutablePaths = sortedUnique(out[i].ExecutablePaths)
	}
	return out, issues
}

func (e *Engine) binPaths(p *domain.Package) {
	dirs := []string{filepath.Join(p.Root, "bin"), filepath.Join(p.Root, "sbin")}
	if p.Manager == "mise" {
		if _, ok := miseOwnedRoot(p.Root); !ok {
			return
		}
		dirs = append(dirs, p.Root)
	}
	for _, dir := range dirs {
		f, err := os.Open(dir)
		if err != nil {
			continue
		}
		entries, _ := f.ReadDir(2048)
		_ = f.Close()
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if target, err := os.Stat(path); err == nil && (target.IsDir() || (e.GOOS != "windows" && target.Mode()&0111 == 0)) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if _, _, ok := e.eligible(entry.Name(), info); ok {
				addPath(p, path)
			}
		}
	}
}

func (e *Engine) brew(ctx context.Context, rows []*domain.Package) error {
	cellar, err := e.query(ctx, "brew", "--cellar")
	if err != nil {
		return err
	}
	cellar = strings.TrimSpace(cellar)
	if !filepath.IsAbs(cellar) {
		return fmt.Errorf("brew returned a non-absolute cellar path")
	}
	data, err := e.query(ctx, "brew", "info", "--json=v2", "--installed")
	if err != nil {
		return err
	}
	var info struct {
		Formulae []struct {
			Name      string `json:"name"`
			FullName  string `json:"full_name"`
			LinkedKeg string `json:"linked_keg"`
			Installed []struct {
				Version string `json:"version"`
			} `json:"installed"`
		} `json:"formulae"`
	}
	if err = json.Unmarshal([]byte(data), &info); err != nil {
		return err
	}
	for _, p := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, f := range info.Formulae {
			if p.ID != f.Name && p.ID != f.FullName {
				continue
			}
			version := p.Version
			if version == "" {
				version = f.LinkedKeg
			}
			if version == "" && len(f.Installed) == 1 {
				version = f.Installed[0].Version
			}
			found := false
			for _, installed := range f.Installed {
				if installed.Version == version {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			root := filepath.Join(cellar, f.Name, version)
			if !within(root, cellar) {
				continue
			}
			p.Root = root
			e.binPaths(p)
			evidence(p, "brew info --json=v2 --installed", "Installed formula and keg recorded by Homebrew")
		}
	}
	return nil
}

func (e *Engine) dpkg(ctx context.Context, rows []*domain.Package) error {
	items, _, err := e.scan(ctx, "")
	if err != nil {
		return err
	}
	lookup := map[string]*domain.Package{}
	for _, p := range rows {
		lookup[p.ID] = p
	}
	var paths []string
	for _, item := range items {
		paths = append(paths, item.Path)
		if item.Target != "" {
			paths = append(paths, item.Target)
		}
	}
	paths = sortedUnique(paths)
	limited := len(paths) > 512
	if limited {
		paths = paths[:512]
	}
	for start := 0; start < len(paths); start += 128 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := paths[start:min(start+128, len(paths))]
		// dpkg -S uses glob patterns, even with --. Escape metacharacters and
		// additionally require an exact returned path before accepting ownership.
		args := []string{"-S", "--"}
		expected := map[string]bool{}
		for _, path := range batch {
			expected[path] = true
			args = append(args, escapeGlob(path))
		}
		text, queryErr := e.query(ctx, "dpkg-query", args...)
		if queryErr != nil && text == "" {
			return queryErr
		}
		scanner := bufio.NewScanner(strings.NewReader(text))
		for scanner.Scan() {
			owners, path, ok := strings.Cut(scanner.Text(), ": ")
			if !ok || !expected[path] {
				continue
			}
			for _, owner := range strings.Split(owners, ", ") {
				p := lookup[owner]
				if p == nil {
					base, _, _ := strings.Cut(owner, ":")
					p = lookup[base]
				}
				if p != nil {
					addPath(p, path)
					evidence(p, "dpkg-query -S", "Path registered to this package in dpkg; alternatives and generated files may not be recorded")
				}
			}
		}
		if err = scanner.Err(); err != nil {
			return err
		}
	}
	if limited {
		return fmt.Errorf("dpkg ownership lookup limited to 512 PATH paths")
	}
	return nil
}

func escapeGlob(path string) string {
	var b strings.Builder
	for _, c := range path {
		switch c {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 2*1024*1024)).Decode(out)
}

type mapOrString struct {
	Values map[string]string
	Single string
}

func (v *mapOrString) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &v.Values); err == nil {
		return nil
	}
	return json.Unmarshal(data, &v.Single)
}

func (e *Engine) npm(ctx context.Context, rows []*domain.Package) error {
	root, err := e.npmQuery(ctx, "root", "--global")
	if err != nil {
		return err
	}
	root = strings.TrimSpace(root)
	prefix, err := e.npmQuery(ctx, "prefix", "--global")
	if err != nil {
		return err
	}
	prefix = strings.TrimSpace(prefix)
	if !filepath.IsAbs(root) || !filepath.IsAbs(prefix) {
		return fmt.Errorf("npm returned a non-absolute global root/prefix")
	}
	for _, p := range rows {
		if err = ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(root, filepath.FromSlash(p.ID))
		if !within(dir, root) {
			continue
		}
		var manifest struct {
			Name string          `json:"name"`
			Bin  json.RawMessage `json:"bin"`
		}
		if readJSON(filepath.Join(dir, "package.json"), &manifest) != nil || manifest.Name != p.ID {
			continue
		}
		bins := map[string]string{}
		if json.Unmarshal(manifest.Bin, &bins) != nil {
			var target string
			if json.Unmarshal(manifest.Bin, &target) == nil {
				bins[filepath.Base(p.ID)] = target
			}
		}
		p.Root = dir
		for command, target := range bins {
			if command == "" || strings.ContainsAny(command, "/\\") || !within(filepath.Join(dir, filepath.FromSlash(target)), dir) {
				continue
			}
			binDir := filepath.Join(prefix, "bin")
			suffixes := []string{""}
			if e.GOOS == "windows" {
				binDir = prefix
				suffixes = []string{".cmd", ".ps1", ""}
			}
			for _, suffix := range suffixes {
				path := filepath.Join(binDir, command+suffix)
				if _, err := os.Lstat(path); err == nil {
					if e.GOOS == "windows" || samePath(path, filepath.Join(dir, filepath.FromSlash(target))) {
						addPath(p, path)
					}
				}
			}
			// The package's actual script also provides ownership through Unix links.
			p.ExecutablePaths = append(p.ExecutablePaths, filepath.Join(dir, filepath.FromSlash(target)))
			p.Commands = append(p.Commands, command)
		}
		evidence(p, "npm global package.json bin", "Declared entrypoints in the currently selected npm global prefix")
	}
	return nil
}

func (e *Engine) npmQuery(ctx context.Context, args ...string) (string, error) {
	if e.GOOS != "windows" {
		return e.query(ctx, "npm", args...)
	}
	items, _, err := e.scan(ctx, "npm")
	if err != nil {
		return "", err
	}
	for _, item := range items {
		for _, path := range []string{item.Path, item.Target} {
			if path == "" {
				continue
			}
			dir := filepath.Dir(path)
			cli := filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js")
			if st, err := os.Stat(cli); err != nil || st.IsDir() {
				continue
			}
			node := filepath.Join(dir, "node.exe")
			if _, err := os.Stat(node); err != nil {
				node = "node"
			}
			return e.query(ctx, node, append([]string{cli}, args...)...)
		}
	}
	return "", fmt.Errorf("cannot locate npm-cli.js beside the Windows npm launcher; global ownership remains unknown")
}

func parseNamedPath(text string) (string, string) {
	text = strings.TrimSpace(text)
	name, path, ok := strings.Cut(text, " (")
	if !ok || !strings.HasSuffix(path, ")") {
		return text, ""
	}
	return name, strings.TrimSuffix(path, ")")
}

func (e *Engine) uv(ctx context.Context, rows []*domain.Package) error {
	text, err := e.query(ctx, "uv", "--color", "never", "--no-progress", "tool", "list", "--show-paths")
	if err != nil {
		return err
	}
	lookup := map[string]*domain.Package{}
	for _, p := range rows {
		lookup[p.ID] = p
	}
	var current *domain.Package
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "- ") {
			if current == nil {
				continue
			}
			command, path := parseNamedPath(strings.TrimPrefix(line, "- "))
			if path != "" && filepath.IsAbs(path) {
				addPath(current, path)
				current.Commands = append(current.Commands, command)
			}
			continue
		}
		label, path := parseNamedPath(line)
		id, _, ok := strings.Cut(label, " v")
		current = nil
		if !ok {
			continue
		}
		current = lookup[id]
		if current != nil && filepath.IsAbs(path) {
			current.Root = path
			evidence(current, "uv tool list --show-paths", "Tool environment and installed entrypoints registered by uv")
		}
	}
	return scanner.Err()
}

type cargoRecord struct {
	version string
	bins    []string
}

func cargoRecords(text string) map[string]*cargoRecord {
	result := map[string]*cargoRecord{}
	current := ""
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if current != "" {
				result[current].bins = append(result[current].bins, strings.TrimSpace(line))
			}
			continue
		}
		id, remainder, ok := strings.Cut(line, " v")
		current = ""
		if ok && strings.HasSuffix(line, ":") {
			current = id
			version, _, _ := strings.Cut(remainder, " ")
			result[id] = &cargoRecord{version: strings.TrimSuffix(version, ":")}
		}
	}
	return result
}

func (e *Engine) cargo(ctx context.Context, rows []*domain.Package) error {
	// Asking about an explicit root prevents config install.root from silently
	// making ~/.cargo/bin look authoritative for a different installation root.
	root := os.Getenv("CARGO_INSTALL_ROOT")
	if root == "" {
		root = os.Getenv("CARGO_HOME")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".cargo")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(e.Dir, root)
	}
	text, err := e.query(ctx, "cargo", "install", "--list", "--root", root)
	if err != nil {
		return err
	}
	records := cargoRecords(text)
	for _, p := range rows {
		record, ok := records[p.ID]
		if !ok || (p.Version != "" && p.Version != record.version) {
			continue
		}
		p.Root = root
		for _, name := range record.bins {
			if name == "" || strings.ContainsAny(name, "/\\") {
				continue
			}
			addPath(p, filepath.Join(root, "bin", name))
		}
		evidence(p, "cargo install --list --root", "Tracked binary installation at this explicit Cargo root; untracked and other configured roots are not inferred")
	}
	return nil
}

func (e *Engine) scoop(ctx context.Context, rows []*domain.Package) error {
	text, err := e.query(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "& { scoop shim list | Select-Object Name,Path,Source,Type,IsGlobal | ConvertTo-Json -Compress }")
	if err != nil {
		return err
	}
	type shim struct {
		Name, Path, Source, Type string
		IsGlobal                 bool
	}
	var shims []shim
	if strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "null" {
		return nil
	}
	if err = json.Unmarshal([]byte(text), &shims); err != nil {
		var one shim
		if err = json.Unmarshal([]byte(text), &one); err != nil {
			return err
		}
		shims = []shim{one}
	}
	for _, s := range shims {
		if s.Source == "External" || s.Path == "" {
			continue
		}
		for _, p := range rows {
			if p.ID != s.Source {
				continue
			}
			addPath(p, s.Path)
			p.Commands = append(p.Commands, s.Name)
			evidence(p, "scoop shim list", "Scoop records this shim as belonging to the installed app")
		}
	}
	return nil
}
