// Package maintenance checks and updates the selected manager instance. It does
// not route around an incompatible manager by changing to a different owner.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/catalog"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type Engine struct {
	Runner                   process.Runner
	CacheDir                 string
	Env                      map[string]string // immutable caller-supplied child environment
	GOOS, Home, Dir          string
	HTTPClient               *http.Client
	RegistryURL, ReleasesURL string
	Now                      func() time.Time
	mu                       sync.Mutex
	additions                []string
}

func New(r process.Runner, cacheDir string) *Engine {
	if r == nil {
		r = process.ExecRunner{}
	}
	home, _ := os.UserHomeDir()
	dir, _ := os.Getwd()
	return &Engine{Runner: r, CacheDir: cacheDir, GOOS: runtime.GOOS, Home: home, Dir: dir, HTTPClient: &http.Client{Timeout: 15 * time.Second}, RegistryURL: "https://registry.npmjs.org", ReleasesURL: "https://api.github.com", Now: time.Now}
}

func (e *Engine) environment() map[string]string {
	m := map[string]string{}
	for _, s := range process.Environment(nil, domain.Command{Env: e.Env}) {
		k, v, ok := strings.Cut(s, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}
func (e *Engine) getenv(k string) string { return e.environment()[k] }
func (e *Engine) ChildEnv() map[string]string {
	e.mu.Lock()
	dirs := append([]string(nil), e.additions...)
	e.mu.Unlock()
	if len(dirs) == 0 {
		return nil
	}
	sep := string(os.PathListSeparator)
	if e.GOOS == "windows" {
		sep = ";"
	}
	dirs = append(dirs, e.getenv("PATH"))
	return map[string]string{"PATH": strings.Join(dirs, sep)}
}
func (e *Engine) command(path string, args ...string) domain.Command {
	env := map[string]string{"NO_COLOR": "1", "HOMEBREW_NO_AUTO_UPDATE": "1", "HOMEBREW_NO_INSTALL_CLEANUP": "1", "MISE_AUTO_INSTALL": "0"}
	for k, v := range e.Env {
		env[k] = v
	}
	for k, v := range e.ChildEnv() {
		env[k] = v
	}
	env["HOMEBREW_NO_AUTOREMOVE"] = "1"
	env["HOMEBREW_NO_INSTALL_CLEANUP"] = "1"
	return domain.Command{Path: path, Args: args, Dir: e.Dir, Env: env}
}
func (e *Engine) output(ctx context.Context, c domain.Command) (string, error) {
	// Queries never trigger mise's missing-tool installation, even if the
	// caller's environment enables it. Do not mutate caller-owned Env maps.
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	env["MISE_AUTO_INSTALL"] = "0"
	env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	env["GOTOOLCHAIN"] = "local"
	c.Env = env
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := e.Runner.Output(ctx, c)
	return r.Stdout, err
}
func (e *Engine) query(ctx context.Context, path string, args ...string) (string, error) {
	return e.output(ctx, e.command(path, args...))
}
func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func canonical(path string) string {
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}
func within(path, root string) bool {
	r, err := filepath.Rel(root, path)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func fileDigest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		return "unreadable"
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() || st.Size() > 2<<20 {
		return "unreadable"
	}
	h := sha256.New()
	_, _ = io.Copy(h, io.LimitReader(f, 2<<20))
	return hex.EncodeToString(h.Sum(nil))
}
func identity(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%s:%d:%d", path, canonical(path), st.Size(), st.ModTime().UnixNano())
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (e *Engine) paths(name string) []string {
	path := e.getenv("PATH")
	if child := e.ChildEnv(); child != nil {
		path = child["PATH"]
	}
	sep := ":"
	suffixes := []string{""}
	if e.GOOS == "windows" {
		sep = ";"
		suffixes = []string{".exe", ".cmd", ".ps1", ""}
	}
	var out []string
	seen := map[string]bool{}
	for _, dir := range strings.Split(path, sep) {
		if !filepath.IsAbs(dir) {
			continue
		}
		for _, ext := range suffixes {
			p := filepath.Join(dir, name+ext)
			st, err := os.Stat(p)
			if err != nil || st.IsDir() || (e.GOOS != "windows" && st.Mode()&0111 == 0) {
				continue
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
func (e *Engine) lookup(name string) string {
	p := e.paths(name)
	if len(p) > 0 {
		return p[0]
	}
	return ""
}
func (e *Engine) getJSON(ctx context.Context, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json, application/json")
	req.Header.Set("User-Agent", "lazypkg-manager-maintenance")
	client := e.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("update metadata returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 2<<20)).Decode(out)
}

type observation struct {
	Health        domain.ManagerHealth
	Binding       []string
	Nodes         []string
	NPMCLI        string
	NPMEntrypoint string
	MiseRegistry  string
	GlobalNode    string
}

func (e *Engine) Check(ctx context.Context, managers []domain.Manager, force bool) ([]domain.ManagerHealth, error) {
	out := make([]domain.ManagerHealth, len(managers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, m := range managers {
		wg.Add(1)
		go func(i int, m domain.Manager) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			work, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			o := e.observe(work, m)
			h := o.Health
			if h.Path == "" {
				out[i] = h
				return
			}
			cached, ok := e.cached(h.Fingerprint)
			age := e.now().Sub(cached.CheckedAt)
			if !force && ok && age >= 0 && age < 24*time.Hour {
				cached.Cached = true
				cached.Alternatives = h.Alternatives
				cached.Compatible = h.Compatible
				cached.Reason = h.Reason
				out[i] = cached
				return
			}
			if err := e.updates(work, &o); err != nil {
				h = o.Health
				h.UpdateStatus = "unknown"
				h.ApplySupported = false
				h.Recommendation = "Update check failed: " + err.Error()
				if ok {
					h.CandidateVersion = cached.CandidateVersion
					h.Stale = true
					h.Recommendation += ". Previously cached candidate is stale."
				}
			} else {
				h = o.Health
				h.CheckedAt = e.now()
				e.save(h)
			}
			out[i] = h
		}(i, m)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

type cacheRecord struct {
	Schema int                  `json:"schema"`
	Health domain.ManagerHealth `json:"health"`
}

// Revision 2 distinguishes a hosted component from its launcher. Old records
// could incorrectly route a missing shell plugin to the shell's package owner.
const healthCacheSchema = 2
const healthFingerprintRevision = "component-ownership-v2"

func (e *Engine) cached(key string) (domain.ManagerHealth, bool) {
	var c cacheRecord
	if e.CacheDir == "" || key == "" {
		return c.Health, false
	}
	err := readJSON(filepath.Join(e.CacheDir, key+".json"), &c)
	return c.Health, err == nil && c.Schema == healthCacheSchema && c.Health.Fingerprint == key
}
func (e *Engine) save(h domain.ManagerHealth) {
	if e.CacheDir == "" || h.Fingerprint == "" {
		return
	}
	if os.MkdirAll(e.CacheDir, 0700) != nil {
		return
	}
	f, err := os.CreateTemp(e.CacheDir, ".health-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_ = f.Chmod(0600)
	err = json.NewEncoder(f).Encode(cacheRecord{healthCacheSchema, h})
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), filepath.Join(e.CacheDir, h.Fingerprint+".json"))
	}
}

func (e *Engine) observe(ctx context.Context, m domain.Manager) observation {
	h := domain.ManagerHealth{Manager: m.ID, Path: m.Path, Version: m.Version, Requirement: m.Requirement, Compatible: m.Available, Reason: m.Reason, ReasonCode: m.ReasonCode, ComponentKind: m.ComponentKind, VersionSubject: m.VersionSubject, Launcher: m.Launcher, UpdateStatus: "not-checked", GuideURL: m.SourceURL}
	if entry, ok := catalog.Lookup(m.ID); ok {
		// Static adapter semantics are authoritative, including during execution
		// revalidation of a plan supplied by a caller.
		h.ComponentKind, h.VersionSubject, h.Launcher = entry.ComponentKind, entry.VersionSubject, entry.Launcher
		if h.GuideURL == "" {
			h.GuideURL = entry.SourceURL
		}
	}
	if h.Reason == "" {
		h.Reason = m.Status
	}
	o := observation{Health: h}
	if m.Path == "" {
		o.Health.UpdateStatus = "missing"
		o.Health.Recommendation = "Install or repair this manager before checking updates."
		return o
	}
	o.Binding = []string{healthFingerprintRevision, domain.MPMVersion, m.ID, identity(m.Path), m.Requirement, e.getenv("PATH"), e.Dir, e.RegistryURL, e.ReleasesURL, h.ComponentKind, h.VersionSubject, h.Launcher}
	if hostedComponent(h) {
		o.Health.Owner = "component installation; owner unverified"
		o.Health.UpdateStatus = "guidance"
		if h.VersionSubject == "launcher" {
			o.Health.Recommendation = "This adapter reports the " + h.Launcher + " host version, not a plugin version. Inspect the host and its package/plugin configuration separately; no host update is inferred from this adapter."
		} else {
			o.Health.Recommendation = "Inspect this component's source or plugin configuration in the selected " + h.Launcher + " context. The launcher is not the component's update target; use the component's verified installation method."
		}
		e.fingerprint(&o)
		return o
	}
	name := executableName(m.ID)
	for _, p := range e.paths(name) {
		if canonical(p) != canonical(m.Path) {
			o.Health.Alternatives = append(o.Health.Alternatives, p)
		}
	}
	if name == "npm" {
		e.observeNPM(ctx, &o)
	} else if name == "brew" {
		o.Health.Owner = "self"
		o.Health.OwnerPath = m.Path
		o.Health.Strategy = "brew-update"
		o.Health.GuideURL = "https://docs.brew.sh/Manpage"
		o.Health.Recommendation = "Refresh the selected Homebrew installation and formula metadata; this does not upgrade installed formulae."
	} else if !e.brewOwner(ctx, &o) {
		switch name {
		case "uv":
			e.uvOwner(&o)
		case "mise":
			e.miseOwner(ctx, &o)
		case "cargo":
			e.cargoOwner(ctx, &o)
		case "rustup":
			o.Health.Owner = "rustup installation; original source unverified"
			o.Health.OwnerPath = m.Path
			o.Health.GuideURL = "https://rust-lang.github.io/rustup/basics.html"
			o.Health.Recommendation = "Review `rustup self update` for rustup itself, or use its original package manager. Updating Rust/Cargo toolchains is a separate operation; no toolchain is changed by this guidance."
		case "go", "gem":
			e.runtimeOwner(ctx, &o, name)
		case "winget":
			o.Health.Owner = "Windows App Installer"
			o.Health.GuideURL = "https://learn.microsoft.com/windows/package-manager/winget/"
			o.Health.Recommendation = "Update App Installer through Microsoft Store or your Windows administrator; recognized apps do not establish WinGet's installer history."
		case "scoop":
			e.scoopOwner(&o)
		}
	}
	if o.Health.Owner == "" {
		o.Health.Owner = "unknown"
		if o.Health.Recommendation == "" {
			o.Health.Recommendation = "The selected executable's update owner could not be verified. Use its original installation method; later PATH alternatives will not be substituted."
		}
	}
	if v, ok := version(o.Health.Version); ok && o.Health.Requirement != "" && (m.Available || m.ReasonCode == "" || m.ReasonCode == "version_unsupported") {
		o.Health.Compatible = minimum(v, o.Health.Requirement)
		if !o.Health.Compatible {
			o.Health.ReasonCode = "version_unsupported"
			o.Health.Reason = "Selected version " + o.Health.Version + " does not satisfy backend requirement " + o.Health.Requirement
		}
	}
	e.fingerprint(&o)
	return o
}

func (e *Engine) fingerprint(o *observation) {
	o.Binding = append(o.Binding, o.Health.Version, o.Health.ReasonCode, o.Health.Reason, o.Health.Owner, o.Health.OwnerPath, o.Health.OwnerPackage, o.Health.RuntimePath, o.Health.RuntimeVersion, o.Health.Prefix, o.Health.ConfigPath, o.Health.Strategy)
	sort.Strings(o.Binding)
	o.Health.Fingerprint = hash(o.Binding)
}

func executableName(id string) string {
	if entry, ok := catalog.Lookup(id); ok && entry.Launcher != "" {
		return entry.Launcher
	}
	return id
}

func hostedComponent(h domain.ManagerHealth) bool {
	kind, subject := h.ComponentKind, h.VersionSubject
	if entry, ok := catalog.Lookup(h.Manager); ok {
		kind, subject = entry.ComponentKind, entry.VersionSubject
	}
	return kind == "shell" || kind == "hosted" || subject == "launcher"
}

func (e *Engine) updates(ctx context.Context, o *observation) error {
	h := &o.Health
	if h.Strategy == "npm-mise" {
		return e.npmUpdate(ctx, o)
	}
	if h.Strategy == "brew-package" {
		return nil
	}
	if h.Strategy == "" {
		h.UpdateStatus = "guidance"
		return nil
	}
	if h.Strategy == "scoop-update" {
		h.UpdateStatus = "refresh-available"
		h.ApplySupported = true
		return nil
	}
	repo := ""
	switch h.Strategy {
	case "uv-self":
		repo = "astral-sh/uv"
	case "mise-self":
		repo = "jdx/mise"
	case "brew-update":
		repo = "Homebrew/brew"
	}
	if repo == "" {
		h.UpdateStatus = "guidance"
		return nil
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if err := e.getJSON(ctx, strings.TrimRight(e.ReleasesURL, "/")+"/repos/"+repo+"/releases/latest", &release); err != nil {
		return err
	}
	latest, ok := version(release.Tag)
	current, currentOK := version(h.Version)
	if !ok || release.Prerelease || release.Draft {
		return errors.New("latest release is not a stable numeric version")
	}
	h.CandidateVersion = latest.String()
	if !currentOK && h.Strategy == "brew-update" {
		h.CandidateVersion = ""
		h.UpdateStatus = "refresh-available"
		h.ApplySupported = true
		h.Recommendation += " The current Git/development version is not compared with stable release tags."
		return nil
	}
	if currentOK && current.compare(latest) >= 0 {
		h.UpdateStatus = "current"
		return nil
	}
	if !currentOK && h.Strategy != "brew-update" {
		h.UpdateStatus = "guidance"
		h.Recommendation = "Current version has an unrecognized or development version format; use the original release channel."
		return nil
	}
	h.UpdateStatus = "available"
	h.ApplySupported = true
	return nil
}
