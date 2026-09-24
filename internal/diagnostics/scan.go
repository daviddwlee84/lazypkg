package diagnostics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (e *Engine) extensions() []string {
	raw := e.PathExt
	if raw == "" {
		raw = ".COM;.EXE;.BAT;.CMD"
	}
	var out []string
	for _, ext := range strings.Split(raw, ";") {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if strings.HasPrefix(ext, ".") {
			out = append(out, ext)
		}
	}
	return out
}

func (e *Engine) knownEntrypoints(ctx context.Context, name string, items []domain.Executable, packages []domain.Package, report *domain.DiagnosticReport) ([]domain.Executable, error) {
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.Path] = true
	}
	for _, p := range packages {
		for _, path := range p.ExecutablePaths {
			if err := ctx.Err(); err != nil {
				return items, err
			}
			if seen[path] || !filepath.IsAbs(path) {
				continue
			}
			command := filepath.Base(path)
			if e.GOOS == "windows" {
				command = strings.TrimSuffix(command, filepath.Ext(command))
			}
			matches := name == "" || name == command || name == filepath.Base(path)
			if e.GOOS == "windows" {
				matches = name == "" || strings.EqualFold(name, command) || strings.EqualFold(name, filepath.Base(path))
			}
			if !matches {
				continue
			}
			declared := false
			for _, public := range p.Commands {
				if public == command || (e.GOOS == "windows" && strings.EqualFold(public, command)) {
					declared = true
					break
				}
			}
			if !declared {
				continue
			}
			if len(items) >= maxEntries {
				report.Issues = append(report.Issues, domain.Issue{Message: "Installed entrypoint scan reached the 20000 candidate limit; results are partial"})
				return items, nil
			}
			seen[path] = true
			target, chain, problem := resolve(path)
			item := domain.Executable{Name: command, Path: path, Target: target, Chain: chain, PathIndex: -1, Problem: problem}
			if e.GOOS == "windows" {
				e.scoopShim(&item)
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func (e *Engine) eligible(name string, info os.FileInfo) (string, int, bool) {
	if e.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(name))
		for i, x := range e.extensions() {
			if x == ext {
				return strings.TrimSuffix(name, filepath.Ext(name)), i, !info.IsDir()
			}
		}
		return "", 0, false
	}
	return name, 0, !info.IsDir() && (info.Mode()&0111 != 0 || info.Mode()&os.ModeSymlink != 0)
}

func (e *Engine) scan(ctx context.Context, name string) ([]domain.Executable, []domain.Issue, error) {
	separator := ":"
	if e.GOOS == "windows" {
		separator = ";"
	}
	items := []domain.Executable{}
	var issues []domain.Issue
	seen := map[string]bool{}
	directories := map[string]bool{}
	count := 0
	for index, dir := range strings.Split(e.Path, separator) {
		if err := ctx.Err(); err != nil {
			return items, issues, err
		}
		if dir == "" {
			dir = e.Dir
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.Dir, dir)
		}
		dir = filepath.Clean(dir)
		if directories[dir] {
			continue
		}
		directories[dir] = true
		f, err := os.Open(dir)
		if err != nil {
			issues = append(issues, domain.Issue{Message: fmt.Sprintf("PATH directory %s: %v", dir, err)})
			continue
		}
		entries, err := f.ReadDir(maxEntries - count + 1)
		_ = f.Close()
		if err != nil && err != io.EOF {
			issues = append(issues, domain.Issue{Message: fmt.Sprintf("PATH directory %s: %v", dir, err)})
			continue
		}
		capped := len(entries) > maxEntries-count
		if capped {
			entries = entries[:maxEntries-count]
			issues = append(issues, domain.Issue{Message: "PATH scan reached the 20000 directory-entry limit; results are partial"})
		}
		count += len(entries)
		type candidate struct {
			item domain.Executable
			rank int
		}
		var candidates []candidate
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return items, issues, err
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			command, rank, ok := e.eligible(entry.Name(), info)
			if !ok {
				continue
			}
			if name != "" {
				match := command == name || entry.Name() == name
				if e.GOOS == "windows" {
					match = strings.EqualFold(command, name) || strings.EqualFold(entry.Name(), name)
				}
				if !match {
					continue
				}
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			item := domain.Executable{Name: command, Path: path, PathIndex: index}
			item.Target, item.Chain, item.Problem = resolve(path)
			if item.Problem == "" && e.GOOS != "windows" {
				if st, err := os.Stat(item.Target); err == nil && st.Mode()&0111 == 0 {
					continue
				}
			}
			if e.GOOS == "windows" {
				e.scoopShim(&item)
			}
			candidates = append(candidates, candidate{item, rank})
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			an, bn := a.item.Name, b.item.Name
			if e.GOOS == "windows" {
				an = strings.ToLower(an)
				bn = strings.ToLower(bn)
			}
			if an == bn {
				return a.rank < b.rank
			}
			return an < bn
		})
		for _, c := range candidates {
			items = append(items, c.item)
		}
		if capped {
			break
		}
	}
	return items, issues, nil
}

func resolve(path string) (string, []string, string) {
	chain := []string{path}
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		if seen[path] {
			return "", chain, "Symbolic link cycle"
		}
		seen[path] = true
		st, err := os.Lstat(path)
		if err != nil {
			return "", chain, fmt.Sprintf("Unavailable executable target: %v", err)
		}
		if st.IsDir() {
			return "", chain, "Executable target is a directory"
		}
		if st.Mode()&os.ModeSymlink == 0 {
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", chain, fmt.Sprintf("Cannot resolve executable: %v", err)
			}
			if canonical != path {
				chain = append(chain, canonical)
			}
			return canonical, chain, ""
		}
		next, err := os.Readlink(path)
		if err != nil {
			return "", chain, err.Error()
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(path), next)
		}
		path = filepath.Clean(next)
		chain = append(chain, path)
	}
	return "", chain, "Symbolic link chain exceeds 40 links"
}

// Only parse Scoop's adjacent data file, never arbitrary wrapper scripts. Shims
// with fixed arguments retain their own identity because behavior can differ.
func (e *Engine) scoopShim(item *domain.Executable) {
	metadata := strings.TrimSuffix(item.Path, filepath.Ext(item.Path)) + ".shim"
	f, err := os.Open(metadata)
	if err != nil {
		return
	}
	defer f.Close()
	var target, args string
	scanner := bufio.NewScanner(io.LimitReader(f, 64*1024))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		switch strings.TrimSpace(key) {
		case "path":
			target = value
		case "args":
			args = value
		}
	}
	if target == "" || args != "" {
		return
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(item.Path), target)
	}
	resolved, chain, problem := resolve(target)
	item.Target = resolved
	item.Chain = append([]string{item.Path, metadata}, chain...)
	item.Problem = problem
}

// npm emits several wrappers for one entrypoint on Windows. Resolve only
// wrappers containing the exact declared script path; arbitrary .cmd files
// beside a package are not assumed equivalent to its executable.
func (e *Engine) npmShim(item *domain.Executable, p domain.Package) {
	ext := strings.ToLower(filepath.Ext(item.Path))
	if ext != ".cmd" && ext != ".ps1" && ext != "" {
		return
	}
	var manifest struct {
		Name string      `json:"name"`
		Bin  mapOrString `json:"bin"`
	}
	if readJSON(filepath.Join(p.Root, "package.json"), &manifest) != nil || manifest.Name != p.ID {
		return
	}
	target, ok := manifest.Bin.Values[item.Name]
	if !ok && manifest.Bin.Single != "" && strings.EqualFold(item.Name, filepath.Base(p.ID)) {
		target, ok = manifest.Bin.Single, true
	}
	if !ok {
		for name, path := range manifest.Bin.Values {
			if strings.EqualFold(name, item.Name) {
				target, ok = path, true
				break
			}
		}
	}
	if !ok {
		return
	}
	path := filepath.Join(p.Root, filepath.FromSlash(target))
	if !within(path, p.Root) {
		return
	}
	f, err := os.Open(item.Path)
	if err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 64*1024))
	_ = f.Close()
	if err != nil {
		return
	}
	needle := "node_modules/" + p.ID + "/" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(target)), "./")
	content := strings.ToLower(strings.ReplaceAll(string(data), "\\", "/"))
	position := strings.Index(content, strings.ToLower(needle))
	if position < 0 {
		return
	}
	// Generated wrappers forward arguments immediately after the script. A
	// wrapper injecting fixed arguments can have different behavior.
	tail := strings.TrimSpace(strings.TrimLeft(content[position+len(needle):], "\"'"))
	if (ext == ".cmd" && !strings.HasPrefix(tail, "%*")) || (ext == ".ps1" && !strings.HasPrefix(tail, "$args")) {
		return
	}
	resolved, chain, problem := resolve(path)
	item.Target = resolved
	item.Chain = append([]string{item.Path}, chain...)
	item.Problem = problem
}
