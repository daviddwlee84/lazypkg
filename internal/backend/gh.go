package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
	"go.yaml.in/yaml/v3"
)

// GHExtensions supplements mpm's extension adapter with complete local identity
// and a feature-gated, per-extension update check. It never executes extensions.
type GHExtensions struct {
	Path, GOOS, Home string
	Runner           process.Runner
	Env              map[string]string
	Timeout          time.Duration
	mu               sync.Mutex
	featureKey       string
	featureSupported bool
}

func (g *GHExtensions) runner() process.Runner {
	if g.Runner != nil {
		return g.Runner
	}
	return process.ExecRunner{}
}
func (g *GHExtensions) goos() string {
	if g.GOOS != "" {
		return g.GOOS
	}
	return runtime.GOOS
}
func (g *GHExtensions) environment() map[string]string {
	m := map[string]string{}
	for _, entry := range process.Environment(nil, domain.Command{Env: g.Env}) {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			m[strings.ToUpper(k)] = v
		}
	}
	return m
}
func ghCanonical(path string) string {
	if p, e := filepath.EvalSymlinks(path); e == nil {
		return p
	}
	return filepath.Clean(path)
}
func ghDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ghIdentity(path string) string {
	s, e := os.Stat(path)
	if e != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%s:%d:%d", path, ghCanonical(path), s.Size(), s.ModTime().UnixNano())
}
func (g *GHExtensions) root() (string, error) {
	e := g.environment()
	base := e["XDG_DATA_HOME"]
	if base != "" {
		base = filepath.Join(base, "gh")
	} else if g.goos() == "windows" && e["LOCALAPPDATA"] != "" {
		base = filepath.Join(e["LOCALAPPDATA"], "GitHub CLI")
	} else {
		home := g.Home
		if home == "" {
			if g.goos() == "windows" {
				home = e["USERPROFILE"]
			} else {
				home = e["HOME"]
			}
		}
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		base = filepath.Join(home, ".local", "share", "gh")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("gh extension data directory is not absolute")
	}
	return ghCanonical(filepath.Join(base, "extensions")), nil
}
func (g *GHExtensions) Instance() string {
	root, _ := g.root()
	e := g.environment()
	return "gh-ext:" + ghDigest([]string{ghCanonical(g.Path), root, e["GH_HOST"], e["GH_CONFIG_DIR"]})
}

// ContextDescription makes the installation namespace reviewable without
// exposing authentication configuration.
func (g *GHExtensions) ContextDescription() (string, error) {
	_, description, err := g.InstallContext()
	return description, err
}

// InstallContext mirrors go-gh auth.defaultHost: GH_HOST, the sole configured
// host, then github.com. Only sorted host keys survive YAML parsing; token and
// account values are neither retained nor included in the binding.
func (g *GHExtensions) InstallContext() (string, string, error) {
	root, err := g.root()
	if err != nil {
		return "", "", err
	}
	e := g.environment()
	dir := e["GH_CONFIG_DIR"]
	if dir == "" && e["XDG_CONFIG_HOME"] != "" {
		dir = filepath.Join(e["XDG_CONFIG_HOME"], "gh")
	}
	if dir == "" && g.goos() == "windows" && e["APPDATA"] != "" {
		dir = filepath.Join(e["APPDATA"], "GitHub CLI")
	}
	if dir == "" {
		home := g.Home
		if home == "" {
			if g.goos() == "windows" {
				home = e["USERPROFILE"]
			} else {
				home = e["HOME"]
			}
		}
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		dir = filepath.Join(home, ".config", "gh")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", errors.New("cannot resolve gh configuration directory")
	}
	dir = ghCanonical(dir)
	general, err := ghHostKeys(filepath.Join(dir, "config.yml"), true)
	if err != nil {
		return "", "", err
	}
	hosts, err := ghHostKeys(filepath.Join(dir, "hosts.yml"), false)
	if err != nil {
		return "", "", err
	}
	if len(hosts) == 0 {
		hosts = general
	}
	host, source := e["GH_HOST"], "GH_HOST"
	if host == "" {
		host = "github.com"
		source = "default"
		if len(hosts) == 1 {
			host = hosts[0]
			source = "sole configured host"
		}
	}
	if !ghHost.MatchString(host) {
		return "", "", errors.New("gh install host is invalid")
	}
	host = strings.ToLower(host)
	key := ghDigest([]any{"gh-host-context-v1", host, dir, hosts})
	description := fmt.Sprintf("gh extension install context: launcher %s; host %s (%s); extension root %s; gh configuration directory %s", g.Path, host, source, root, dir)
	return key, description, nil
}

func ghHostKeys(path string, general bool) ([]string, error) {
	b, err := ghRead(path, 2<<20)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, errors.New("cannot inspect gh host configuration metadata")
	}
	var doc yaml.Node
	if yaml.Unmarshal(b, &doc) != nil {
		return nil, errors.New("cannot parse gh host configuration metadata")
	}
	if len(doc.Content) == 0 {
		return []string{}, nil
	}
	node := doc.Content[0]
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("gh host configuration is not a mapping")
	}
	if general {
		var found *yaml.Node
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == "hosts" {
				if found != nil {
					return nil, errors.New("gh host configuration has duplicate entries")
				}
				found = node.Content[i+1]
			}
		}
		if found == nil {
			return []string{}, nil
		}
		node = found
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("gh hosts metadata is not a mapping")
	}
	keys := []string{}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode || !ghHost.MatchString(key.Value) || seen[key.Value] {
			return nil, errors.New("gh host configuration contains invalid host keys")
		}
		seen[key.Value] = true
		keys = append(keys, key.Value)
	}
	sort.Strings(keys)
	return keys, nil
}

var ghUnset = []string{"GH_FORCE_TTY", "GH_DEBUG", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_TRACE*", "GIT_CURL_VERBOSE"}

func (g *GHExtensions) command(path string, args ...string) domain.Command {
	env := readOnlyEnv(g.Env)
	if g.Home != "" {
		if g.goos() == "windows" {
			env["USERPROFILE"] = g.Home
		} else {
			env["HOME"] = g.Home
		}
	}
	for k := range env {
		for _, remove := range ghUnset {
			if strings.EqualFold(k, remove) || strings.HasSuffix(remove, "*") && strings.HasPrefix(strings.ToUpper(k), strings.TrimSuffix(remove, "*")) {
				delete(env, k)
			}
		}
	}
	for k, v := range map[string]string{"GH_PROMPT_DISABLED": "1", "GH_NO_UPDATE_NOTIFIER": "1", "GH_NO_EXTENSION_UPDATE_NOTIFIER": "1", "GH_TELEMETRY": "false", "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0", "GCM_INTERACTIVE": "never", "NO_COLOR": "1"} {
		env[k] = v
	}
	// A fixed SSH command keeps normal ~/.ssh/config and agent use, but cannot
	// prompt. A custom wrapper is rejected before network queries, not replaced.
	if e := g.environment(); e["GIT_SSH_COMMAND"] == "" && e["GIT_SSH"] == "" {
		env["GIT_SSH_COMMAND"] = "ssh -oBatchMode=yes"
	}
	return domain.Command{Path: path, Args: args, Env: env, Unset: append([]string(nil), ghUnset...)}
}
func (g *GHExtensions) output(ctx context.Context, c domain.Command) (process.Result, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return g.runner().Output(work, c)
}
func (g *GHExtensions) SupportsOutdated(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := ghIdentity(g.Path)
	if g.featureKey == key {
		return g.featureSupported, nil
	}
	r, err := g.output(ctx, g.command(g.Path, "extension", "upgrade", "--help"))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("could not check gh extension dry-run support")
	}
	g.featureKey = key
	g.featureSupported = regexp.MustCompile(`(?m)^\s+--dry-run\s+`).MatchString(r.Stdout)
	return g.featureSupported, nil
}

var ghPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var ghHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*(?::[0-9]+)?$`)
var ghCommit = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func ghRepoID(id string) bool {
	p := strings.Split(id, "/")
	return len(p) == 2 && ghPart.MatchString(p[0]) && ghPart.MatchString(p[1]) && strings.HasPrefix(p[1], "gh-")
}
func ghRemote(raw string) (host, id string, err error) {
	raw = strings.TrimSpace(raw)
	var path string
	if strings.Contains(raw, "://") {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", errors.New("unrecognized Git remote")
		}
		host = u.Host
		path = u.Path
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git" {
			return "", "", errors.New("unsupported Git remote transport")
		}
	} else {
		left, right, ok := strings.Cut(raw, ":")
		if !ok {
			return "", "", errors.New("unrecognized Git remote")
		}
		_, host, ok = strings.Cut(left, "@")
		if !ok {
			host = left
		}
		path = right
	}
	id = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	host = strings.ToLower(host)
	if !ghHost.MatchString(host) || !ghRepoID(id) {
		return "", "", errors.New("invalid extension repository identity")
	}
	return host, id, nil
}

type ghManifest struct {
	Owner  string `yaml:"owner"`
	Name   string `yaml:"name"`
	Host   string `yaml:"host"`
	Tag    string `yaml:"tag"`
	Pinned bool   `yaml:"ispinned"`
	Path   string `yaml:"path"`
}

func ghRead(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() || s.Size() > limit {
		return nil, errors.New("metadata is not a bounded regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, errors.New("metadata exceeds size limit")
	}
	return b, e
}
func ghWithin(path, root string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func ghVersionValid(v string) bool {
	return v != "" && len(v) <= 512 && !strings.ContainsAny(v, " \t\r\n\x00\x1b")
}

func (g *GHExtensions) Inspect(ctx context.Context) ([]domain.GHExtension, []domain.Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	root, err := g.root()
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []domain.GHExtension{}, nil, nil
	}
	if err != nil {
		return nil, nil, errors.New("cannot read gh extension directory")
	}
	out := []domain.GHExtension{}
	var issues []domain.Issue
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return out, issues, err
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "gh-") {
			continue
		}
		if !ghPart.MatchString(name) || name == "gh-" {
			issues = append(issues, domain.Issue{Manager: "gh-ext", Kind: "metadata", Message: "Unrecognized extension directory name"})
			continue
		}
		path := filepath.Join(root, name)
		x := domain.GHExtension{ID: "unknown:" + name, Name: strings.TrimPrefix(name, "gh-"), Kind: "unknown", Root: root, Path: path, Launcher: g.Path, Status: "unknown"}
		binding := []string{"gh-native-v1", g.Instance(), ghIdentity(g.Path), ghIdentity(path)}
		if !entry.IsDir() {
			x.ID = "local:" + name
			x.Kind = "local"
			x.Status = "local"
			x.Reason = "Local extension links are visible but are not managed by package batch operations."
			if entry.Type()&os.ModeSymlink != 0 {
				target, e := os.Readlink(path)
				if e != nil {
					x.Kind = "unknown"
					x.Status = "unknown"
					x.Reason = "Cannot read local extension link"
				}
				binding = append(binding, target)
			} else {
				b, e := ghRead(path, 64<<10)
				if e != nil || !filepath.IsAbs(strings.TrimSpace(string(b))) {
					x.Kind = "unknown"
					x.Status = "unknown"
					x.Reason = "Invalid local extension path reference"
				}
				binding = append(binding, ghDigest(b))
			}
		} else if _, e := os.Lstat(filepath.Join(path, "manifest.yml")); e == nil {
			manifestPath := filepath.Join(path, "manifest.yml")
			var b []byte
			var e error
			if ghWithin(ghCanonical(manifestPath), ghCanonical(path)) {
				b, e = ghRead(manifestPath, 1<<20)
			} else {
				e = errors.New("manifest resolves outside its installation")
			}
			binding = append(binding, ghDigest(b))
			var m ghManifest
			decoder := yaml.NewDecoder(strings.NewReader(string(b)))
			decoder.KnownFields(true)
			if e != nil || decoder.Decode(&m) != nil || !ghRepoID(m.Owner+"/"+m.Name) || !strings.EqualFold(m.Name, name) || !ghHost.MatchString(m.Host) || !ghVersionValid(m.Tag) {
				x.Reason = "Binary extension metadata is missing, invalid, or outside its installation"
			} else {
				x.ID = m.Owner + "/" + m.Name
				x.Host = strings.ToLower(m.Host)
				x.Kind = "binary"
				x.Version = m.Tag
				x.FullVersion = m.Tag
				x.Pinned = m.Pinned
				x.Status = "not-checked"
				bin := filepath.Join(path, name)
				if g.goos() == "windows" {
					bin += ".exe"
				}
				binding = append(binding, ghIdentity(bin))
				if st, e := os.Stat(bin); e != nil || !st.Mode().IsRegular() || !ghWithin(ghCanonical(bin), ghCanonical(path)) {
					x.BlockedReason = "The registered extension binary is missing or outside its installation"
				}
			}
		} else {
			gitDir := filepath.Join(path, ".git")
			st, e := os.Lstat(gitDir)
			if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !ghWithin(ghCanonical(filepath.Join(gitDir, "config")), ghCanonical(gitDir)) {
				x.Reason = "Extension has no supported Git checkout or binary manifest"
			} else {
				remote, e1 := g.output(ctx, g.command("git", "-C", path, "config", "--get", "remote.origin.url"))
				head, e2 := g.output(ctx, g.command("git", "-C", path, "rev-parse", "--verify", "HEAD"))
				host, id, e3 := ghRemote(remote.Stdout)
				full := strings.TrimSpace(head.Stdout)
				config, _ := ghRead(filepath.Join(gitDir, "config"), 1<<20)
				binding = append(binding, ghDigest(remote.Stdout), ghDigest(config), full)
				if e1 != nil || e2 != nil || e3 != nil || !ghCommit.MatchString(full) || !strings.EqualFold(strings.TrimPrefix(id, strings.Split(id, "/")[0]+"/"), name) {
					x.Reason = "Git extension identity could not be verified"
				} else {
					x.ID = id
					x.Host = host
					x.Kind = "git"
					x.FullVersion = full
					x.Version = full[:8]
					x.Status = "not-checked"
					_, e := os.Stat(filepath.Join(path, ".pin-"+full))
					if e == nil {
						x.Pinned = true
					} else if !os.IsNotExist(e) {
						x.Status = "unknown"
						x.Reason = "Cannot inspect extension pin state"
					}
					status, e1 := g.output(ctx, g.command("git", "-C", path, "status", "--porcelain", "--untracked-files=normal"))
					binding = append(binding, ghDigest(status.Stdout))
					dirty := false
					for _, line := range strings.Split(strings.TrimSuffix(status.Stdout, "\n"), "\n") {
						line = strings.TrimSuffix(line, "\r")
						if line != "" && !(x.Pinned && line == "?? .pin-"+full) {
							dirty = true
						}
					}
					if e1 != nil || dirty {
						x.BlockedReason = "Git extension has local changes or an unreadable worktree; review the checkout manually before changing it"
					}
					if !x.Pinned {
						upstream, e2 := g.output(ctx, g.command("git", "-C", path, "rev-parse", "--symbolic-full-name", "@{upstream}"))
						origin, e3 := g.output(ctx, g.command("git", "-C", path, "symbolic-ref", "-q", "refs/remotes/origin/HEAD"))
						base, e4 := g.output(ctx, g.command("git", "-C", path, "rev-parse", "--verify", "@{upstream}"))
						binding = append(binding, strings.TrimSpace(upstream.Stdout), strings.TrimSpace(origin.Stdout), strings.TrimSpace(base.Stdout))
						if e2 != nil || e3 != nil || e4 != nil || !strings.HasPrefix(strings.TrimSpace(origin.Stdout), "refs/remotes/origin/") || strings.TrimSpace(upstream.Stdout) != strings.TrimSpace(origin.Stdout) || strings.TrimSpace(base.Stdout) != full {
							x.BlockedReason = "Git extension has local changes or an unverified/default-branch mismatch; review the checkout manually before changing it"
						}
					}
				}
			}
		}
		if x.Pinned {
			x.Status = "pinned"
			x.Reason = "Pinned extension; batch operations do not change its pin."
		}
		if x.Status == "unknown" {
			issues = append(issues, domain.Issue{Manager: "gh-ext", Kind: "metadata", Message: name + ": " + x.Reason})
		}
		x.Fingerprint = ghDigest(append(binding, x.ID, x.Host, x.Kind, x.FullVersion, fmt.Sprint(x.Pinned), x.BlockedReason))
		out = append(out, x)
	}
	if err := ctx.Err(); err != nil {
		return out, issues, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, issues, nil
}

func (g *GHExtensions) packageOf(x domain.GHExtension) domain.Package {
	copy := x
	return domain.Package{Manager: "gh-ext", ID: x.ID, Name: "gh " + x.Name, Version: x.Version, Latest: x.Latest, Scope: "global", Root: x.Path, Instance: g.Instance(), Extension: &copy, Evidence: []domain.Evidence{{Kind: "recorded", Source: "gh extension registration", Detail: "Registered gh command; the gh launcher is separate from this extension"}}}
}
func (g *GHExtensions) Installed(ctx context.Context) (domain.Snapshot, error) {
	xs, issues, err := g.Inspect(ctx)
	s := domain.Snapshot{Packages: []domain.Package{}, Issues: issues, ObservedAt: time.Now()}
	for _, x := range xs {
		s.Packages = append(s.Packages, g.packageOf(x))
	}
	return s, err
}

var ghStatusLine = regexp.MustCompile(`^\[\s*([^\]\s]+)\]: (.+)$`)

func ghParseCheck(x domain.GHExtension, text string) (domain.GHExtension, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 1 {
		return x, errors.New("unexpected gh extension update output")
	}
	m := ghStatusLine.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if m == nil || m[1] != x.Name {
		return x, errors.New("gh update result does not match the selected extension")
	}
	switch m[2] {
	case "already up to date":
		x.Status = "current"
		x.Latest = x.Version
		return x, nil
	case "pinned extensions can not be upgraded":
		x.Status = "pinned"
		x.Pinned = true
		x.Reason = "Pinned extension; no update applied"
		return x, nil
	case "local extensions can not be upgraded":
		x.Status = "local"
		x.Reason = "Local extension cannot be upgraded"
		return x, nil
	}
	from, to, ok := strings.Cut(strings.TrimPrefix(m[2], "would have upgraded from "), " to ")
	if !strings.HasPrefix(m[2], "would have upgraded from ") || !ok || from != x.Version || !ghVersionValid(to) || to == from {
		return x, errors.New("unrecognized or stale gh extension update result")
	}
	x.Status = "available"
	x.Latest = to
	return x, nil
}
func (g *GHExtensions) Check(ctx context.Context, x domain.GHExtension) (domain.GHExtension, error) {
	if err := ctx.Err(); err != nil {
		return x, err
	}
	if x.Status == "local" || x.Status == "pinned" || x.Status == "unknown" {
		return x, nil
	}
	if x.Kind != "binary" && x.Kind != "git" || !ghRepoID(x.ID) || !ghVersionValid(x.Version) {
		return x, errors.New("extension identity is incomplete")
	}
	ok, err := g.SupportsOutdated(ctx)
	if err != nil {
		x.Status = "unknown"
		x.Reason = err.Error()
		return x, err
	}
	if !ok {
		x.Status = "unsupported"
		x.Reason = "This gh version has no extension upgrade --dry-run; update checks are unavailable"
		return x, nil
	}
	e := g.environment()
	if x.Kind == "git" && (e["GIT_SSH_COMMAND"] != "" || e["GIT_SSH"] != "") {
		x.Status = "unknown"
		x.Reason = "A custom Git SSH command cannot be guaranteed noninteractive"
		return x, errors.New(x.Reason)
	}
	r, err := g.output(ctx, g.command(g.Path, "extension", "upgrade", x.ID, "--dry-run"))
	if err != nil {
		x.Status = "unknown"
		x.Reason = "Could not retrieve this extension's current update status; authentication, network or metadata may need attention"
		if ctx.Err() != nil {
			return x, ctx.Err()
		}
		return x, errors.New(x.Reason)
	}
	checked, err := ghParseCheck(x, r.Stdout)
	if err != nil {
		x.Status = "unknown"
		x.Reason = err.Error()
		return x, err
	}
	return checked, nil
}
func (g *GHExtensions) Outdated(ctx context.Context) (domain.Snapshot, error) {
	xs, issues, err := g.Inspect(ctx)
	s := domain.Snapshot{Packages: []domain.Package{}, Issues: issues, ObservedAt: time.Now()}
	if err != nil {
		return s, err
	}
	ok, err := g.SupportsOutdated(ctx)
	if err != nil || !ok {
		s.Issues = append(s.Issues, domain.Issue{Manager: "gh-ext", Kind: "unsupported", Message: "This gh installation cannot provide reliable extension dry-run update checks"})
		return s, err
	}
	results := make([]domain.GHExtension, len(xs))
	errs := make([]error, len(xs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i, x := range xs {
		wg.Add(1)
		go func(i int, x domain.GHExtension) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			results[i], errs[i] = g.Check(ctx, x)
		}(i, x)
	}
	wg.Wait()
	for i, x := range results {
		if errs[i] != nil {
			s.Issues = append(s.Issues, domain.Issue{Manager: "gh-ext", Kind: "update-check", Message: xs[i].ID + ": " + errs[i].Error()})
		}
		if x.Status == "available" {
			s.Packages = append(s.Packages, g.packageOf(x))
		}
	}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	return s, nil
}
