// Package backend translates the tested mpm CLI contract into application data.
package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

var Core = []string{"brew", "cask", "apt", "dnf", "pacman", "winget", "scoop", "choco", "npm", "uvx", "cargo", "mise", "flatpak", "snap", "pipx"}
var supported = map[string][]string{}

func init() {
	for _, id := range Core {
		supported[id] = []string{"installed", "search", "install", "outdated", "upgrade", "remove"}
	}
	supported["cargo"] = []string{"installed", "search", "install", "remove"}
	supported["uvx"] = []string{"installed", "install", "outdated", "upgrade", "remove"}
	supported["pipx"] = []string{"installed", "install", "outdated", "upgrade", "remove"}
}
func Capabilities(id string) []string { return append([]string(nil), supported[id]...) }
func Known(id string) bool            { _, ok := supported[id]; return ok }
func NormalizeManager(id string) string {
	if id == "uv" {
		return "uvx"
	}
	return id
}

type MPM struct {
	Path    string
	Runner  process.Runner
	Env     map[string]string
	Timeout time.Duration
	mu      sync.Mutex
	checked bool
}

func (m *MPM) Check(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checked {
		return nil
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := m.Runner.Output(cancelCtx, domain.Command{Path: m.Path, Args: []string{"--version"}, Env: m.Env, Unset: []string{"MPM_*"}})
	if err != nil {
		return fmt.Errorf("mpm is unavailable: %w; run lazypkg setup", err)
	}
	versions := regexp.MustCompile(`\b\d+\.\d+\.\d+(?:[a-zA-Z0-9.+-]*)?\b`).FindAllString(r.Stdout, -1)
	if len(versions) == 0 || versions[0] != domain.MPMVersion {
		return fmt.Errorf("mpm %s is required (found %q); review lazypkg setup mpm to change the installed version", domain.MPMVersion, strings.TrimSpace(r.Stdout))
	}
	m.checked = true
	return nil
}

// Recheck is used before writes because the external uv-managed backend can
// change while a dashboard remains open.
func (m *MPM) Recheck(ctx context.Context) error {
	m.mu.Lock()
	m.checked = false
	m.mu.Unlock()
	return m.Check(ctx)
}

// Config is temporary, explicit, and independent of the user's/project's mpm configuration.
func (m *MPM) command(args ...string) (domain.Command, func(), error) {
	f, err := os.CreateTemp("", "lazypkg-mpm-*.json")
	if err != nil {
		return domain.Command{}, nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err = f.WriteString(`{"mpm":{}}`); err != nil {
		f.Close()
		cleanup()
		return domain.Command{}, nil, err
	}
	if err = f.Close(); err != nil {
		cleanup()
		return domain.Command{}, nil, err
	}
	a := []string{"--config", f.Name(), "--no-color", "--no-progress"}
	a = append(a, args...)
	env := map[string]string{"NO_COLOR": "1", "HOMEBREW_NO_AUTO_UPDATE": "1"}
	for k, v := range m.Env {
		env[k] = v
	}
	return domain.Command{Path: m.Path, Args: a, Env: env, Unset: []string{"MPM_*"}}, cleanup, nil
}
func (m *MPM) query(ctx context.Context, args ...string) (process.Result, error) {
	if err := m.Check(ctx); err != nil {
		return process.Result{}, err
	}
	c, done, err := m.command(args...)
	if err != nil {
		return process.Result{}, err
	}
	defer done()
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return m.Runner.Output(ctx, c)
}

type managerJSON struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Path       string            `json:"cli_path"`
	Version    string            `json:"version"`
	Supported  bool              `json:"supported"`
	Available  bool              `json:"available"`
	Executable bool              `json:"executable"`
	Fresh      bool              `json:"fresh"`
	Errors     []json.RawMessage `json:"errors"`
}

func errorStrings(raw []json.RawMessage) []string {
	out := []string{}
	for _, v := range raw {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, s)
		} else {
			out = append(out, string(v))
		}
	}
	return out
}
func (m *MPM) Managers(ctx context.Context) ([]domain.Manager, error) {
	args := []string{"--table-format", "json"}
	for _, id := range Core {
		args = append(args, "--"+id)
	}
	args = append(args, "managers", "--view", "supported")
	r, err := m.query(ctx, args...)
	if err != nil {
		return nil, err
	}
	var data map[string]managerJSON
	if err = json.Unmarshal([]byte(r.Stdout), &data); err != nil {
		return nil, fmt.Errorf("mpm managers JSON: %w", err)
	}
	if data == nil {
		return nil, fmt.Errorf("mpm managers returned null")
	}
	out := []domain.Manager{}
	for id, v := range data {
		if !Known(id) || !v.Supported {
			continue
		}
		status := "missing"
		switch {
		case !v.Supported:
			status = "unsupported platform"
		case v.Available:
			status = "available"
		case v.Path != "" && !v.Executable:
			status = "not executable"
		case v.Path != "" && !v.Fresh:
			status = "version unsupported"
		case v.Path != "":
			status = "unavailable"
		}
		name := v.Name
		if id == "uvx" {
			name = "uv tools"
		}
		caps := Capabilities(id)
		if id == "mise" {
			caps = append(caps, "activate")
		}
		if id == "uvx" {
			caps = append(caps, "search")
		}
		out = append(out, domain.Manager{ID: id, Name: name, Path: v.Path, Version: v.Version, Supported: v.Supported, Available: v.Available, Status: status, Capabilities: caps, Errors: errorStrings(v.Errors)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Available != out[j].Available {
			return out[i].Available
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

type pkgJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Installed   string `json:"installed_version"`
	Latest      string `json:"latest_version"`
	Description string `json:"description"`
}
type inventoryJSON struct {
	ID       string            `json:"id"`
	Packages []pkgJSON         `json:"packages"`
	Errors   []json.RawMessage `json:"errors"`
}

func Decode(data []byte, manager string) (domain.Snapshot, error) {
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	var raw map[string]inventoryJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return s, fmt.Errorf("mpm package JSON: %w", err)
	}
	v, ok := raw[manager]
	if !ok {
		return s, fmt.Errorf("mpm omitted %s; it may be unavailable or not support this operation", manager)
	}
	if v.Packages == nil && len(v.Errors) == 0 {
		return s, fmt.Errorf("mpm %s response omitted the packages array", manager)
	}
	for _, msg := range errorStrings(v.Errors) {
		s.Issues = append(s.Issues, domain.Issue{Manager: manager, Message: msg})
	}
	for _, p := range v.Packages {
		// mpm 8.0.1's uvx regex also matches entrypoint lines such as
		// "- visidata" as ID "-", version "isidata". These are not packages.
		if manager == "uvx" && p.ID == "-" {
			continue
		}
		if p.ID == "" {
			return s, fmt.Errorf("mpm %s returned a package without an ID", manager)
		}
		kind := "recorded"
		detail := "Listed in the manager inventory"
		if manager == "winget" {
			kind = "recognized"
			detail = "WinGet recognizes this application; original installer is unknown"
		}
		s.Packages = append(s.Packages, domain.Package{Manager: manager, ID: p.ID, Name: p.Name, Version: p.Installed, Latest: p.Latest, Description: p.Description, Scope: "global/user", Evidence: []domain.Evidence{{Kind: kind, Source: manager, Detail: detail}}})
	}
	return s, nil
}
func (m *MPM) Packages(ctx context.Context, kind, query, manager string) (domain.Snapshot, error) {
	args := []string{"--table-format", "json", "--" + manager, kind}
	// Chocolatey's ID-only search drops short names such as git/vim. Extended
	// search asks its supported broader search path instead of claiming no match.
	if kind == "search" && manager == "choco" && len(query) < 4 {
		args = append(args, "--extended")
	}
	if query != "" {
		args = append(args, "--", query)
	}
	r, err := m.query(ctx, args...)
	if err != nil {
		return domain.Snapshot{}, err
	}
	s, err := Decode([]byte(r.Stdout), manager)
	if kind == "search" {
		for i := range s.Packages {
			s.Packages[i].Evidence = nil
		}
	}
	return s, err
}
func (m *MPM) Preview(ctx context.Context, req domain.ActionRequest) (string, error) {
	r, err := m.query(ctx, "--plan", "--"+req.Manager, req.Operation, "--", Specifier(req.Manager, req.Package))
	// mpm writes native planned commands to stdout. Its informational stderr
	// includes the random isolated config path, which is neither a command nor
	// stable review identity. Runtime failures retain stderr in their error.
	return strings.TrimSpace(r.Stdout), err
}
func (m *MPM) Mutation(req domain.ActionRequest) (domain.Command, func(), error) {
	return m.command("--"+req.Manager, req.Operation, "--", Specifier(req.Manager, req.Package))
}

// Specifier protects literal native IDs from mpm's name@version parser. Keep
// the explicit manager selector too: ecosystem pURLs (e.g. npm) can otherwise
// expand to several compatible managers. Never accept caller-supplied pURLs.
func Specifier(manager, id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(url.QueryEscape(p), "+", "%20")
	}
	return "pkg:" + manager + "/" + strings.Join(parts, "/")
}
