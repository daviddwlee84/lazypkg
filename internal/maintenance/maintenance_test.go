package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	mu      sync.Mutex
	outputs []domain.Command
	runs    []domain.Command
	output  func(domain.Command) (string, error)
	run     func(domain.Command) error
}

func (r *fakeRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	r.mu.Lock()
	r.outputs = append(r.outputs, c)
	r.mu.Unlock()
	s, err := r.output(c)
	return process.Result{Stdout: s}, err
}
func (r *fakeRunner) Run(ctx context.Context, c domain.Command, _ io.Reader, _, _ io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	r.runs = append(r.runs, c)
	r.mu.Unlock()
	if r.run != nil {
		return r.run(c)
	}
	return nil
}
func file(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

type npmFixture struct {
	e                                                       *Engine
	r                                                       *fakeRunner
	m                                                       domain.Manager
	dir, nodeRoot, node, cli, mise, config, newRoot, newCLI string
	mu                                                      sync.Mutex
	installed, activated, override                          bool
	globalVersion                                           string
	requests                                                int
	networkFailure                                          bool
	now                                                     time.Time
}

func npmSetup(t *testing.T) *npmFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the aqua npm installation recipe supports macOS/Linux; Windows guidance is tested separately")
	}
	dir := canonical(t.TempDir())
	f := &npmFixture{dir: dir, globalVersion: "24.13.0", now: time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)}
	f.nodeRoot = filepath.Join(dir, "data", "installs", "node", "24.13.0")
	f.node = filepath.Join(f.nodeRoot, "bin", "node")
	f.cli = filepath.Join(f.nodeRoot, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	f.mise = filepath.Join(dir, "tools", "mise")
	f.config = filepath.Join(dir, "config", "mise", "config.toml")
	f.newRoot = filepath.Join(dir, "data", "installs", "aqua-npm-cli", "11.19.1")
	f.newCLI = filepath.Join(f.newRoot, "bin", "npm-cli.js")
	for _, p := range []string{f.node, f.cli, f.mise, filepath.Join(f.nodeRoot, "bin", "npm")} {
		file(t, p, "fixture executable")
	}
	file(t, f.config, "[tools]\nnode='lts'\n")
	f.r = &fakeRunner{}
	f.r.output = func(c domain.Command) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		args := strings.Join(c.Args, " ")
		if c.Path == f.node {
			switch args {
			case "--version":
				return "v24.13.0", nil
			case f.cli + " --version":
				return "11.6.2", nil
			case f.cli + " prefix --global":
				return f.nodeRoot, nil
			case f.newCLI + " --version":
				if f.installed {
					return "11.19.1", nil
				}
			case f.newCLI + " prefix --global":
				return f.nodeRoot, nil
			}
		}
		if c.Path == f.mise {
			node := map[string]any{"version": "24.13.0", "install_path": f.nodeRoot, "installed": true, "requested_version": "lts", "source": map[string]string{"path": f.config}}
			globalNode := map[string]any{"version": f.globalVersion, "install_path": f.nodeRoot, "installed": true, "requested_version": "lts", "source": map[string]string{"path": f.config}}
			npm := map[string]any{"version": "11.19.1", "install_path": f.newRoot, "installed": true, "source": map[string]string{"path": f.config}}
			switch args {
			case "ls --installed --json":
				return jsonText(map[string]any{"node": []any{node}}), nil
			case "ls --current --json":
				return jsonText(map[string]any{"node": []any{node}}), nil
			case "ls --global --json":
				if c.Dir == f.e.Dir || c.Env["MISE_CEILING_PATHS"] != c.Dir {
					return "", errors.New("global query was not isolated")
				}
				x := map[string]any{"node": []any{globalNode}}
				if f.activated {
					x["aqua:npm/cli"] = []any{npm}
				}
				return jsonText(x), nil
			case "registry npm --json":
				return `{"short":"npm","backends":["aqua:npm/cli","npm:npm"]}`, nil
			case "where aqua:npm/cli@11.19.1":
				if f.installed {
					return f.newRoot, nil
				}
			case "bin-paths aqua:npm/cli@11.19.1":
				return filepath.Join(f.newRoot, "bin"), nil
			case "which npm":
				if f.override {
					return filepath.Join(f.nodeRoot, "bin", "npm"), nil
				}
				return filepath.Join(f.newRoot, "bin", "npm"), nil
			}
		}
		return "", fmt.Errorf("unexpected query: %s %s", c.Path, args)
	}
	f.r.run = func(c domain.Command) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if c.Path != f.mise {
			return errors.New("attempted another owner")
		}
		if c.Env["MISE_GLOBAL_CONFIG_FILE"] != f.config || c.Env["MISE_NODE_VERSION"] != "24.13.0" {
			return errors.New("runtime or global config not bound")
		}
		switch strings.Join(c.Args, " ") {
		case "install aqua:npm/cli@11.19.1":
			f.installed = true
			file(t, f.newCLI, "new npm CLI")
			file(t, filepath.Join(f.newRoot, "bin", "npm"), "new npm wrapper")
			return nil
		case "use --global --pin aqua:npm/cli@11.19.1":
			if !f.installed {
				return errors.New("activated before install")
			}
			f.activated = true
			file(t, f.config, "[tools]\nnode='lts'\n'aqua:npm/cli'='11.19.1'\n")
			return nil
		}
		return errors.New("unexpected mutation")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if f.networkFailure {
			http.Error(w, "offline", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/npm/11.19.1" {
			fmt.Fprint(w, `{"version":"11.19.1","engines":{"node":"^20.17.0 || >=22.9.0"}}`)
			return
		}
		if r.URL.Path != "/npm" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"versions":{"11.6.2":{"engines":{"node":">=20"}},"11.10.0":{"engines":{"node":"^20.17.0 || >=22.9.0"}},"11.19.1":{"engines":{"node":"^20.17.0 || >=22.9.0"}},"11.20.0":{"engines":{"node":">=26.0.0"}},"11.21.0-beta.1":{"engines":{"node":">=20"}},"12.0.0":{"engines":{"node":">=20"}}}}`)
	}))
	t.Cleanup(s.Close)
	f.e = New(f.r, filepath.Join(dir, "cache"))
	f.e.GOOS = "linux"
	f.e.Home = dir
	f.e.Dir = dir
	f.e.Env = map[string]string{"PATH": filepath.Dir(f.node) + ":" + filepath.Dir(f.mise), "MISE_GLOBAL_CONFIG_FILE": f.config}
	f.e.RegistryURL = s.URL
	f.e.Now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
	f.m = domain.Manager{ID: "npm", Path: filepath.Join(f.nodeRoot, "bin", "npm"), Version: "11.6.2", Requirement: ">=11.10.0", Reason: "Selected version is below the backend minimum"}
	return f
}

func TestNPMRepairKeepsNodeAndBundledWrapper(t *testing.T) {
	f := npmSetup(t)
	alternative := filepath.Join(f.dir, "homebrew", "bin", "npm")
	file(t, alternative, "a different npm context")
	f.e.Env["PATH"] += ":" + filepath.Dir(alternative)
	original := fileDigest(f.m.Path)
	p, err := f.e.Plan(context.Background(), f.m)
	if err != nil {
		t.Fatal(err)
	}
	h := p.ManagerUpdate
	if !h.ApplySupported || h.CandidateVersion != "11.19.1" || h.RuntimePath != f.node || h.Prefix != f.nodeRoot || h.Owner != "mise" || h.ConfigPath != f.config {
		t.Fatalf("incorrect context-aware health: %#v", h)
	}
	if len(h.Alternatives) != 1 || h.Alternatives[0] != alternative {
		t.Fatal("later npm context not reported", h.Alternatives)
	}
	if len(f.r.runs) != 0 || len(p.Steps) != 2 {
		t.Fatal("plan must not mutate")
	}
	if strings.Contains(p.Preview, "brew") || strings.Contains(p.Preview, "install -g npm") {
		t.Fatal("repair targets an alternative or overwrites bundled npm")
	}
	r, err := f.e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err, r)
	}
	if len(f.r.runs) != 2 || fileDigest(f.m.Path) != original {
		t.Fatal("Node's bundled wrapper was changed")
	}
	if child := f.e.ChildEnv(); child == nil || !strings.HasPrefix(child["PATH"], filepath.Join(f.newRoot, "bin")+":") {
		t.Fatalf("new child environment not available: %#v", child)
	}
	if !strings.Contains(r.Message, "invoking shell awaits") {
		t.Fatal("parent shell status not explained", r)
	}
}
func TestNPMUpdateBlocksIncompatibleGlobalNode(t *testing.T) {
	f := npmSetup(t)
	f.globalVersion = "18.20.0"
	r, err := f.e.Check(context.Background(), []domain.Manager{f.m}, true)
	if err != nil {
		t.Fatal(err)
	}
	if r[0].UpdateStatus != "blocked" || r[0].ApplySupported {
		t.Fatalf("global runtime incompatibility ignored: %#v", r[0])
	}
}

func TestNPMEnvironmentProfileDoesNotGuessWriteTarget(t *testing.T) {
	f := npmSetup(t)
	f.e.Env["MISE_ENV"] = "production"
	health, err := f.e.Check(context.Background(), []domain.Manager{f.m}, true)
	if err != nil {
		t.Fatal(err)
	}
	if health[0].ApplySupported || !strings.Contains(health[0].Recommendation, "configuration layer") {
		t.Fatal("profile override did not block an ambiguous global write", health)
	}
}
func TestNPMPlanRejectsConfigurationDrift(t *testing.T) {
	f := npmSetup(t)
	p, err := f.e.Plan(context.Background(), f.m)
	if err != nil {
		t.Fatal(err)
	}
	file(t, f.config, "[tools]\nnode='22'\n")
	_, err = f.e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || len(f.r.runs) != 0 {
		t.Fatal("stale plan executed", err)
	}
}

func TestNPMPlanBindsReviewedTargetVersion(t *testing.T) {
	f := npmSetup(t)
	p, err := f.e.Plan(context.Background(), f.m)
	if err != nil {
		t.Fatal(err)
	}
	p.ManagerUpdate.CandidateVersion = "11.18.0"
	if _, err = f.e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err == nil || len(f.r.runs) != 0 {
		t.Fatal("modified target executed", err)
	}
}
func TestNPMProjectOverrideDoesNotGetBypassed(t *testing.T) {
	f := npmSetup(t)
	f.override = true
	p, err := f.e.Plan(context.Background(), f.m)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if f.e.ChildEnv() != nil || !strings.Contains(r.Message, "overrides") {
		t.Fatal("project selection was bypassed", r)
	}
}
func TestHealthCacheExpiryForceAndStaleMetadata(t *testing.T) {
	f := npmSetup(t)
	ctx := context.Background()
	first, err := f.e.Check(ctx, []domain.Manager{f.m}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.e.Check(ctx, []domain.Manager{f.m}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Cached || !second[0].Cached || f.requests != 1 {
		t.Fatal("24 hour cache was not reused", first, second, f.requests)
	}
	_, err = f.e.Check(ctx, []domain.Manager{f.m}, true)
	if err != nil || f.requests != 2 {
		t.Fatal("force did not check metadata", err, f.requests)
	}
	f.now = f.now.Add(25 * time.Hour)
	f.networkFailure = true
	stale, err := f.e.Check(ctx, []domain.Manager{f.m}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !stale[0].Stale || stale[0].ApplySupported || stale[0].UpdateStatus != "unknown" {
		t.Fatal("stale candidate presented as executable", stale)
	}
}
func TestUnknownOwnerIsGuidanceAndCannotExecuteForgedSteps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manager")
	file(t, path, "binary")
	r := &fakeRunner{output: func(c domain.Command) (string, error) { return "", errors.New("unexpected query") }}
	e := New(r, "")
	e.Env = map[string]string{"PATH": dir}
	p, err := e.Plan(context.Background(), domain.Manager{ID: "unknown", Path: path, Version: "1.0.0", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.ManagerUpdate.ApplySupported || p.ManagerUpdate.UpdateStatus != "guidance" {
		t.Fatal(p)
	}
	p.Steps = []domain.Step{{Command: domain.Command{Path: "evil"}}}
	if _, err = e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err == nil || len(r.runs) != 0 {
		t.Fatal("forged action executed")
	}
}
func TestStandaloneUVUsesReceiptAndPreservesShell(t *testing.T) {
	dir := canonical(t.TempDir())
	name := "uv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, "bin", name)
	file(t, path, "binary")
	receipt := filepath.Join(dir, "config", "uv", "uv-receipt.json")
	file(t, receipt, jsonText(map[string]any{"install_prefix": filepath.Dir(path), "binaries": []string{"uv", "uvx"}, "source": map[string]string{"app_name": "uv", "owner": "astral-sh"}}))
	updated := false
	r := &fakeRunner{output: func(c domain.Command) (string, error) {
		if updated {
			return "uv 0.12.0", nil
		}
		return "uv 0.11.13", nil
	}, run: func(c domain.Command) error { updated = true; return nil }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"0.12.0"}`) }))
	defer server.Close()
	e := New(r, "")
	e.Home = dir
	e.Env = map[string]string{"PATH": filepath.Dir(path), "XDG_CONFIG_HOME": filepath.Join(dir, "config")}
	e.ReleasesURL = server.URL
	p, err := e.Plan(context.Background(), domain.Manager{ID: "uvx", Path: path, Version: "0.11.13", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.ManagerUpdate.Strategy != "uv-self" || p.Steps[0].Command.Env["UV_NO_MODIFY_PATH"] != "1" {
		t.Fatal(p)
	}
	if _, err = e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != 1 || r.runs[0].Path != path || strings.Join(r.runs[0].Args, " ") != "self update 0.12.0" {
		t.Fatal("wrong self update", r.runs)
	}
}
func TestBrewOwnerRecipeTargetsOnlyRecordedFormula(t *testing.T) {
	dir := canonical(t.TempDir())
	brew := filepath.Join(dir, "bin", "brew")
	path := filepath.Join(dir, "Cellar", "mise", "2026.9.1", "bin", "mise")
	file(t, brew, "brew")
	file(t, path, "mise")
	updated := false
	r := &fakeRunner{output: func(c domain.Command) (string, error) {
		if c.Path == path {
			if updated {
				return "2026.9.12", nil
			}
			return "2026.9.1", nil
		}
		switch strings.Join(c.Args, " ") {
		case "--cellar":
			return filepath.Join(dir, "Cellar"), nil
		case "info --json=v2 mise":
			return `{"formulae":[{"name":"mise","versions":{"stable":"2026.9.12"},"installed":[{"version":"2026.9.1"}]}]}`, nil
		}
		return "", errors.New("unexpected query")
	}, run: func(c domain.Command) error { updated = true; return nil }}
	e := New(r, "")
	e.Env = map[string]string{"PATH": filepath.Dir(brew)}
	p, err := e.Plan(context.Background(), domain.Manager{ID: "mise", Path: path, Version: "2026.9.1", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.ManagerUpdate.Owner != "brew" || p.Steps[0].Command.Path != brew || strings.Join(p.Steps[0].Command.Args, " ") != "upgrade --formula mise" {
		t.Fatal(p)
	}
	if _, err = e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestScoopVerificationUsesPowerShellAndRequiresVersion(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			dir := canonical(t.TempDir())
			root := filepath.Join(dir, "scoop")
			shim := filepath.Join(root, "shims", "scoop.ps1")
			script := filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1")
			ps := filepath.Join(dir, "system", "pwsh.exe")
			for _, p := range []string{shim, script, ps, filepath.Join(root, "apps", "scoop", "current", ".git")} {
				file(t, p, "fixture")
			}
			r := &fakeRunner{output: func(c domain.Command) (string, error) {
				if c.Path != ps || strings.Join(c.Args, " ") != "-NoProfile -File "+script+" --version" {
					return "", errors.New("Scoop was launched without PowerShell")
				}
				if bad {
					return "", nil
				}
				return "Scoop 0.5.3", nil
			}, run: func(c domain.Command) error {
				if c.Path != ps || strings.Join(c.Args, " ") != "-NoProfile -File "+script+" update" {
					return errors.New("wrong update command")
				}
				return nil
			}}
			e := New(r, "")
			e.GOOS = "windows"
			e.Home = dir
			e.Env = map[string]string{"PATH": filepath.Dir(shim) + ";" + filepath.Dir(ps), "SCOOP": root}
			p, err := e.Plan(context.Background(), domain.Manager{ID: "scoop", Path: shim, Version: "0.5.2", Available: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard)
			if bad {
				if err == nil || result.Steps[0].Status != "unverified" {
					t.Fatal("false success", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUVPipHealthKeepsItsOwnCatalogID(t *testing.T) {
	dir := canonical(t.TempDir())
	name := "uv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, "bin", name)
	file(t, path, "binary")
	receipt := filepath.Join(dir, "config", "uv", "uv-receipt.json")
	file(t, receipt, jsonText(map[string]any{"install_prefix": filepath.Dir(path), "binaries": []string{"uv"}, "source": map[string]string{"app_name": "uv", "owner": "astral-sh"}}))
	r := &fakeRunner{output: func(c domain.Command) (string, error) { return "", errors.New("unexpected command") }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"0.12.0"}`) }))
	defer server.Close()
	e := New(r, filepath.Join(dir, "cache"))
	e.Env = map[string]string{"PATH": filepath.Dir(path), "XDG_CONFIG_HOME": filepath.Join(dir, "config")}
	e.ReleasesURL = server.URL
	managers := []domain.Manager{{ID: "uvx", Path: path, Version: "0.11.13"}, {ID: "uv-pip", Path: path, Version: "0.11.13"}}
	for i := 0; i < 2; i++ {
		health, err := e.Check(context.Background(), managers, false)
		if err != nil {
			t.Fatal(err)
		}
		for j, h := range health {
			if h.Manager != managers[j].ID || h.Strategy != "uv-self" {
				t.Fatalf("shared executable confused catalog IDs: %#v", health)
			}
		}
	}
}

func TestReadQueriesDisableBothMiseAutoInstallModes(t *testing.T) {
	f := npmSetup(t)
	f.e.Env["MISE_AUTO_INSTALL"] = "1"
	f.e.Env["MISE_NOT_FOUND_AUTO_INSTALL"] = "true"
	if _, err := f.e.Check(context.Background(), []domain.Manager{f.m}, true); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.r.outputs {
		if c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" {
			t.Fatal("read query inherited auto-install settings", c)
		}
	}
	if f.e.Env["MISE_AUTO_INSTALL"] != "1" || f.e.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		t.Fatal("caller environment was mutated")
	}
}

func TestWindowsNPMRecipeHasConcreteGuidance(t *testing.T) {
	dir := canonical(t.TempDir())
	node := filepath.Join(dir, "node.exe")
	npm := filepath.Join(dir, "npm.cmd")
	file(t, node, "node")
	file(t, npm, "npm")
	r := &fakeRunner{output: func(c domain.Command) (string, error) {
		if c.Path == node && strings.Join(c.Args, " ") == "--version" {
			return "v24.13.0", nil
		}
		return "", errors.New("unexpected query")
	}}
	e := New(r, "")
	e.GOOS = "windows"
	e.Env = map[string]string{"PATH": dir}
	health, err := e.Check(context.Background(), []domain.Manager{{ID: "npm", Path: npm, Version: "11.6.2"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if health[0].ApplySupported || !strings.Contains(health[0].Recommendation, "macOS/Linux") {
		t.Fatal(health)
	}
}

func TestMiseSelfUpdateDisablesPluginsAndDoesNotCacheDoctorEnvironment(t *testing.T) {
	dir := canonical(t.TempDir())
	path := filepath.Join(dir, "mise")
	file(t, path, "mise")
	updated := false
	r := &fakeRunner{output: func(c domain.Command) (string, error) {
		switch strings.Join(c.Args, " ") {
		case "doctor --json":
			return `{"self_update_available":true,"build_info":{"features":"self_update,rustls"},"env_vars":{"SECRET":"fixture-sensitive-value"}}`, nil
		case "--version":
			if updated {
				return "2026.9.12", nil
			}
			return "2026.9.1", nil
		}
		return "", errors.New("unexpected query")
	}, run: func(c domain.Command) error {
		if strings.Join(c.Args, " ") != "self-update --no-plugins 2026.9.12" {
			return errors.New("plugin updates were not disabled")
		}
		updated = true
		return nil
	}}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"tag_name":"v2026.9.12"}`) }))
	defer s.Close()
	e := New(r, filepath.Join(dir, "cache"))
	e.Env = map[string]string{"PATH": dir}
	e.ReleasesURL = s.URL
	p, err := e.Plan(context.Background(), domain.Manager{ID: "mise", Path: path, Version: "2026.9.1", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(e.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(e.CacheDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "fixture-sensitive-value") {
			t.Fatal("doctor environment leaked into cache")
		}
	}
	if _, err = e.Execute(context.Background(), p, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestEngineRangesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		node, rangeText string
		want, known     bool
	}{{"24.13.0", "^20.17.0 || >=22.9.0", true, true}, {"22.8.0", "^20.17.0 || >=22.9.0", false, true}, {"20.16.0", "^20.17.0 || >=22.9.0", false, true}, {"20.17.0", ">=20.17.0 <21", true, true}, {"24.0.0", "mysterious-engine", false, false}} {
		v, _ := version(tc.node)
		got, known := satisfies(v, tc.rangeText)
		if got != tc.want || known != tc.known {
			t.Fatal(tc, got, known)
		}
	}
	if _, ok := version("11.20.0-beta.1"); ok {
		t.Fatal("prerelease accepted")
	}
}
func TestCancellationDoesNotRunOrWriteConfiguration(t *testing.T) {
	f := npmSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.e.Check(ctx, []domain.Manager{f.m}, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.r.runs) != 0 {
		t.Fatal("query mutated")
	}
}
