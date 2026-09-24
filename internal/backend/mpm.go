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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// Core is retained for callers compiled against the original API. It is now the
// generated catalog, not a separately maintained allowlist.
var Core = func() []string {
	ids := []string{}
	for _, e := range catalog.All() {
		ids = append(ids, e.ID)
	}
	return ids
}()

func Capabilities(id string) []string {
	e, ok := catalog.Lookup(id)
	if !ok {
		return nil
	}
	caps := e.Capabilities
	// These are actual lazypkg-native additions, not guesses about mpm support.
	if e.ID == "mise" {
		caps = append(caps, "activate")
	}
	if e.ID == "uvx" {
		caps = append(caps, "search")
	}
	if e.ID == "gh-ext" {
		caps = append(caps, "outdated") // native per-extension dry-run; feature-gated by the app
	}
	return caps
}
func Known(id string) bool              { _, ok := catalog.Lookup(id); return ok }
func NormalizeManager(id string) string { return catalog.Normalize(id) }

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
	r, err := m.Runner.Output(cancelCtx, domain.Command{Path: m.Path, Args: []string{"--version"}, Env: readOnlyEnv(m.Env), Unset: []string{"MPM_*"}})
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

// Config is temporary, explicit, and independent of the user's/project's mpm
// configuration. mpm 8.0.1's Go adapter inherits the incorrect --version probe;
// its documented version_cli_options override supplies Go's native subcommand.
const isolatedConfig = `{"mpm":{"suggest_contribs":false,"overrides":{"go":{"version_cli_options":["version"]},"yazi":{"version_regexes":["^Ya[ \\t]+(?P<version>\\S+)","(?m)^[ \\t]*Version:[ \\t]+(?P<version>\\S+)"]}}}}`

func (m *MPM) command(args ...string) (domain.Command, func(), error) {
	f, err := os.CreateTemp("", "lazypkg-mpm-*.json")
	if err != nil {
		return domain.Command{}, nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err = f.WriteString(isolatedConfig); err != nil {
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
	// A reviewed single-package action must not auto-remove unrelated formulae
	// or trigger cleanup through inherited Homebrew preferences.
	env["HOMEBREW_NO_AUTOREMOVE"] = "1"
	env["HOMEBREW_NO_INSTALL_CLEANUP"] = "1"
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
	c.Env = readOnlyEnv(c.Env)
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return m.Runner.Output(ctx, c)
}

// mise's documented auto-install controls also cover executable shims. Query
// calls must report missing tools rather than let a version probe install them.
// Go's documented local mode likewise disables automatic toolchain downloads
// and reports the selected executable's bundled version (go.dev/doc/toolchain).
func readOnlyEnv(base map[string]string) map[string]string {
	env := make(map[string]string, len(base)+3)
	for k, v := range base {
		env[k] = v
	}
	env["MISE_AUTO_INSTALL"] = "0"
	env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	env["GOTOOLCHAIN"] = "local"
	return env
}

func (m *MPM) probeTimeoutSeconds() int {
	// Leave most of the outer query deadline for startup and JSON rendering.
	// The native timeout is per manager, so a slow probe remains a row-level
	// failure instead of destroying the complete discovery response.
	seconds := 5
	if m.Timeout > 0 && m.Timeout < 10*time.Second {
		seconds = max(1, int(m.Timeout/time.Second)/2)
	}
	return seconds
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
	// The supported view omits unmaintained adapters even when installed. Ask
	// for all metadata, then filter platform support without hiding that state.
	args := []string{"--timeout", strconv.Itoa(m.probeTimeoutSeconds()), "--table-format", "json", "managers", "--view", "all"}
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
	metadata := map[string]catalog.Entry{}
	for _, e := range catalog.All() {
		metadata[e.BackendID] = e
	}
	for backendID, v := range data {
		e, known := metadata[backendID]
		if !known || !v.Supported {
			continue
		}
		status, reasonCode, reason := "available", "ready", ""
		if !v.Available {
			status, reasonCode, reason = m.componentReason(e, v)
		}
		out = append(out, domain.Manager{ID: e.ID, BackendID: e.BackendID, Name: e.Name, Path: v.Path, Version: v.Version, Supported: v.Supported, Available: v.Available, Status: status, Capabilities: Capabilities(e.ID), Errors: errorStrings(v.Errors), Requirement: e.Requirement, Reason: reason, ReasonCode: reasonCode, ComponentKind: e.ComponentKind, VersionSubject: e.VersionSubject, Launcher: e.Launcher, Scope: e.Scope, Groups: e.Groups, Maintained: e.Maintained, SourceURL: e.SourceURL})
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
	manager = catalog.Normalize(manager)
	s := domain.Snapshot{Packages: []domain.Package{}, ObservedAt: time.Now()}
	var raw map[string]inventoryJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return s, fmt.Errorf("mpm package JSON: %w", err)
	}
	v, ok := raw[catalog.BackendID(manager)]
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
		if manager == "go" {
			// The pinned adapter scans GOBIN/GOPATH binaries with go version -m.
			// Embedded module/build metadata is not an installation receipt.
			kind = "recognized"
			detail = "Go build metadata identifies this command's module; its original installer is unknown"
		}
		scope := "unknown"
		if e, ok := catalog.Lookup(manager); ok {
			scope = e.Scope
		}
		s.Packages = append(s.Packages, domain.Package{Manager: manager, ID: p.ID, Name: p.Name, Version: p.Installed, Latest: p.Latest, Description: p.Description, Scope: scope, Evidence: []domain.Evidence{{Kind: kind, Source: manager, Detail: detail}}})
	}
	return s, nil
}
func (m *MPM) Packages(ctx context.Context, kind, query, manager string) (domain.Snapshot, error) {
	manager = catalog.Normalize(manager)
	if !Known(manager) {
		return domain.Snapshot{}, fmt.Errorf("unknown manager %q", manager)
	}
	args := []string{"--table-format", "json", "--" + catalog.BackendID(manager), kind}
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
	r, err := m.query(ctx, "--plan", "--"+catalog.BackendID(req.Manager), req.Operation, "--", Specifier(req.Manager, req.Package))
	// mpm writes native planned commands to stdout. Its informational stderr
	// includes the random isolated config path, which is neither a command nor
	// stable review identity. Runtime failures retain stderr in their error.
	return strings.TrimSpace(r.Stdout), err
}
func (m *MPM) Mutation(req domain.ActionRequest) (domain.Command, func(), error) {
	return m.command("--"+catalog.BackendID(req.Manager), req.Operation, "--", Specifier(req.Manager, req.Package))
}

// Specifier protects literal native IDs from mpm's name@version parser. Keep
// the explicit manager selector too: ecosystem pURLs (e.g. npm) can otherwise
// expand to several compatible managers. Never accept caller-supplied pURLs.
func Specifier(manager, id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(url.QueryEscape(p), "+", "%20")
	}
	return "pkg:" + catalog.BackendID(manager) + "/" + strings.Join(parts, "/")
}
