package bootstrap

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
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	paths    map[string]string
	versions map[string]string
	runs     []domain.Command
	outputs  []domain.Command
	toolDir  string
	psState  powershellState
	onRun    func(domain.Command) error
}

func (r *fakeRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	r.outputs = append(r.outputs, c)
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == powershellProbe {
		b, _ := json.Marshal(r.psState)
		return process.Result{Stdout: string(b)}, nil
	}
	if reflect.DeepEqual(c.Args, []string{"tool", "dir", "--bin"}) {
		return process.Result{Stdout: r.toolDir + "\n"}, nil
	}
	path := c.Path
	if len(c.Args) >= 4 && c.Args[1] == "-File" {
		path = c.Args[2]
	}
	if v, ok := r.versions[path]; ok {
		return process.Result{Stdout: v}, nil
	}
	return process.Result{}, errors.New("not installed")
}
func (r *fakeRunner) Run(_ context.Context, c domain.Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	r.runs = append(r.runs, c)
	if r.onRun != nil {
		return r.onRun(c)
	}
	return nil
}
func fixture(t *testing.T) (*Engine, *fakeRunner) {
	t.Helper()
	r := &fakeRunner{paths: map[string]string{}, versions: map[string]string{}, psState: powershellState{Major: 7, Language: "FullLanguage", Policy: "RemoteSigned", MachinePolicy: "Undefined", UserPolicy: "Undefined"}}
	dir := t.TempDir()
	r.toolDir = filepath.Join(dir, "tool-bin")
	e := New(r, filepath.Join(dir, "data"))
	e.home = filepath.Join(dir, "home")
	e.GOOS = "linux"
	e.GOARCH = "amd64"
	e.lookPath = func(n string) (string, error) {
		if p, ok := r.paths[n]; ok {
			return p, nil
		}
		return "", errors.New("missing")
	}
	e.HTTPClient = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected HTTP request: %s", req.URL)
	})}
	return e, r
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func httpBytes(e *Engine, body string) {
	e.HTTPClient = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
}
func ids(steps []domain.Step) []string {
	out := []string{}
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return out
}

func TestPlanNormalUVToolEnvironmentAndReadOnly(t *testing.T) {
	e, _ := fixture(t)
	p, err := e.Plan(context.Background(), []string{"mpm"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(p.Steps), []string{"uv", "mpm"}) {
		t.Fatalf("steps: %+v", p.Steps)
	}
	c := p.Steps[1].Command
	if !reflect.DeepEqual(c.Args, []string{"tool", "install", "meta-package-manager==8.0.1"}) {
		t.Fatalf("args: %v", c.Args)
	}
	for k := range c.Env {
		if strings.HasPrefix(k, "UV_") {
			t.Fatalf("normal uv environment was overridden: %s", k)
		}
	}
	if _, err := os.Stat(e.DataDir); !os.IsNotExist(err) {
		t.Fatalf("planning changed data directory: %v", err)
	}
	if !reflect.DeepEqual(p.Steps[1].DependsOn, []string{"uv"}) {
		t.Fatal("missing uv dependency")
	}
}

func TestReadOnlyProbeDisablesMiseAutoInstallWithoutChangingOverlay(t *testing.T) {
	e, r := fixture(t)
	r.versions["shim"] = "mpm, version 8.0.1"
	env := map[string]string{"PATH": "private", "MISE_AUTO_INSTALL": "1", "MISE_NOT_FOUND_AUTO_INSTALL": "true"}
	if _, err := e.output(context.Background(), domain.Command{Path: "shim", Args: []string{"--version"}, Env: env}); err != nil {
		t.Fatal(err)
	}
	got := r.outputs[0].Env
	if got["PATH"] != "private" || got["MISE_AUTO_INSTALL"] != "0" || got["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" {
		t.Fatal(got)
	}
	if env["MISE_AUTO_INSTALL"] != "1" || env["MISE_NOT_FOUND_AUTO_INSTALL"] != "true" {
		t.Fatal("probe mutated caller environment", env)
	}
}

func TestResolveRejectsUntestedExistingVersionWithoutChangingIt(t *testing.T) {
	e, r := fixture(t)
	r.paths["mpm"] = "/fixture/mpm"
	r.versions["/fixture/mpm"] = "mpm, version 7.2.0"
	p, err := e.ResolveMPM(context.Background())
	if err == nil || p != "" || !strings.Contains(err.Error(), "8.0.1 is required") {
		t.Fatalf("%s %v", p, err)
	}
	opts, err := e.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if opts[0].Installed {
		t.Fatal("incompatible mpm was marked as the tested version")
	}
	if len(r.runs) > 0 {
		t.Fatal("discovery ran an installer")
	}
}

func TestBrokenExistingManagerGetsRepairGuidance(t *testing.T) {
	e, r := fixture(t)
	r.paths["uv"] = "/fixture/broken-uv"
	p, err := e.Plan(context.Background(), []string{"mpm"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].URL != "" || p.Steps[0].GuideURL == "" || !strings.Contains(p.Steps[0].Description, "version probe failed") {
		t.Fatalf("broken uv would be reinstalled: %+v", p.Steps[0])
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil || len(r.runs) > 0 || res.Steps[1].Status != "blocked" {
		t.Fatalf("broken dependency was used: %+v, %v", res, err)
	}
}

func TestCompatibleStandaloneWinsOverIncompatiblePATH(t *testing.T) {
	e, r := fixture(t)
	r.paths["mpm"] = "/fixture/old-mpm"
	r.versions["/fixture/old-mpm"] = "mpm, version 7.2.0"
	if err := os.MkdirAll(filepath.Dir(e.standalonePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.standalonePath(), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	r.versions[e.standalonePath()] = "mpm, version 8.0.1"
	p, err := e.ResolveMPM(context.Background())
	if err != nil || p != e.standalonePath() {
		t.Fatalf("%s %v", p, err)
	}
}

func TestCompatibleUVToolBackendWinsOverIncompatiblePATH(t *testing.T) {
	e, r := fixture(t)
	r.paths["mpm"] = "/fixture/old-mpm"
	r.versions["/fixture/old-mpm"] = "mpm, version 7.2.0"
	r.paths["uv"] = "/fixture/uv"
	r.versions["/fixture/uv"] = "uv 0.9.0"
	if err := os.MkdirAll(r.toolDir, 0700); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(r.toolDir, "mpm")
	if err := os.WriteFile(backend, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	r.versions[backend] = "mpm, version 8.0.1"
	p, err := e.ResolveMPM(context.Background())
	if err != nil || p != backend {
		t.Fatalf("%s %v", p, err)
	}
}

func TestPlanValidation(t *testing.T) {
	for _, selection := range [][]string{nil, {"typo"}, {"mpm", "mpm-standalone"}, {"scoop"}} {
		e, _ := fixture(t)
		if _, err := e.Plan(context.Background(), selection); err == nil {
			t.Fatalf("accepted %v", selection)
		}
	}
}

func TestInstallUVThenMPMWithoutRestartingParent(t *testing.T) {
	e, r := fixture(t)
	before := os.Getenv("PATH")
	uv := filepath.Join(t.TempDir(), "uv")
	mpm := filepath.Join(r.toolDir, "mpm")
	p, err := e.Plan(context.Background(), []string{"mpm"})
	if err != nil {
		t.Fatal(err)
	}
	httpBytes(e, "# official fixture installer\n")
	r.onRun = func(c domain.Command) error {
		if c.Path == "/bin/sh" {
			if c.Args[0] == installerToken {
				t.Fatal("installer placeholder was not materialized")
			}
			if b, err := os.ReadFile(c.Args[0]); err != nil || !strings.Contains(string(b), "fixture") {
				t.Fatalf("installer file: %s %v", b, err)
			}
			r.paths["uv"] = uv
			r.versions[uv] = "uv 0.9.0"
			return nil
		}
		if c.Path != uv {
			t.Fatalf("expected newly installed absolute uv, got %s", c.Path)
		}
		if !strings.Contains(c.Env["PATH"], filepath.Dir(uv)) {
			t.Fatal("subsequent child lacks new uv path")
		}
		r.versions[mpm] = "mpm, version 8.0.1"
		return nil
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 2 || res.Steps[0].Status != "succeeded" || res.Steps[1].Status != "succeeded" {
		t.Fatalf("result: %+v", res)
	}
	if path, err := e.ResolveMPM(context.Background()); err != nil || path != mpm {
		t.Fatalf("new mpm: %s %v", path, err)
	}
	if os.Getenv("PATH") != before {
		t.Fatal("parent process PATH was changed")
	}
	if _, err := os.Stat(r.runs[0].Args[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary script left behind: %v", err)
	}
}

func TestFailureBlocksDependentsAndContinuesIndependentSteps(t *testing.T) {
	e, r := fixture(t)
	p, err := e.Plan(context.Background(), []string{"mpm", "mise"})
	if err != nil {
		t.Fatal(err)
	}
	httpBytes(e, "fixture")
	count := 0
	r.onRun = func(c domain.Command) error {
		count++
		if count == 1 {
			return errors.New("uv installer failed")
		}
		r.paths["mise"] = "/fixture/mise"
		r.versions["/fixture/mise"] = "mise 2026.9.0"
		return nil
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected failure")
	}
	if len(r.runs) != 2 {
		t.Fatalf("ran %d commands", len(r.runs))
	}
	if res.Steps[0].Status != "failed" || res.Steps[1].Status != "blocked" || res.Steps[2].Status != "succeeded" {
		t.Fatalf("results: %+v", res)
	}
}

func TestCancellationStopsNewSteps(t *testing.T) {
	e, r := fixture(t)
	p, err := e.Plan(context.Background(), []string{"uv", "mise"})
	if err != nil {
		t.Fatal(err)
	}
	httpBytes(e, "fixture")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.onRun = func(domain.Command) error { cancel(); return nil }
	res, err := e.Execute(ctx, p, nil, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: %v", err)
	}
	if len(r.runs) != 1 || res.Steps[1].Status != "cancelled" {
		t.Fatalf("started after cancel: %+v", res)
	}
}

func TestInstalledSelectionIsSkipped(t *testing.T) {
	e, r := fixture(t)
	r.paths["uv"] = "/fixture/uv"
	r.versions["/fixture/uv"] = "uv 0.9.0"
	p, err := e.Plan(context.Background(), []string{"uv"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.runs) > 0 || res.Steps[0].Status != "skipped" {
		t.Fatalf("existing uv was reinstalled: %+v", res)
	}
}

func TestInstallationAppearingAfterReviewIsKept(t *testing.T) {
	e, r := fixture(t)
	p, err := e.Plan(context.Background(), []string{"uv"})
	if err != nil {
		t.Fatal(err)
	}
	r.paths["uv"] = "/fixture/new-uv"
	r.versions["/fixture/new-uv"] = "uv 0.9.0"
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.runs) != 0 || res.Steps[0].Status != "skipped" {
		t.Fatalf("overwrote newly installed uv: %+v", res)
	}
}

func TestWindowsPolicyBecomesGuideAndBlocksDependent(t *testing.T) {
	e, r := fixture(t)
	e.GOOS = "windows"
	r.paths["pwsh"] = "/fixture/pwsh.exe"
	r.psState.MachinePolicy = "AllSigned"
	r.psState.Policy = "AllSigned"
	p, err := e.Plan(context.Background(), []string{"mise"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(p.Steps), []string{"scoop", "mise"}) || p.Steps[0].GuideURL == "" {
		t.Fatalf("plan: %+v", p)
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected policy guidance")
	}
	if len(r.runs) > 0 || res.Steps[0].Status != "needs-attention" || res.Steps[1].Status != "blocked" {
		t.Fatalf("policy bypassed: %+v", res)
	}
}

func TestWindowsExistingWinGetDoesNotAddScoop(t *testing.T) {
	e, r := fixture(t)
	e.GOOS = "windows"
	r.paths["winget"] = "/fixture/winget.exe"
	r.versions["/fixture/winget.exe"] = "v1.12.0"
	p, err := e.Plan(context.Background(), []string{"mise"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(p.Steps), []string{"mise"}) || p.Steps[0].Command.Path != "/fixture/winget.exe" {
		t.Fatalf("plan: %+v", p)
	}
}

func TestWindowsWinGetPortableInstallIsUsableInCurrentSession(t *testing.T) {
	e, r := fixture(t)
	e.GOOS = "windows"
	local := filepath.Join(t.TempDir(), "Local AppData")
	t.Setenv("LOCALAPPDATA", local)
	r.paths["winget"] = "/fixture/winget.exe"
	r.versions["/fixture/winget.exe"] = "v1.12.0"
	p, err := e.Plan(context.Background(), []string{"mise"})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(local, "Microsoft", "WinGet", "Links", "mise.exe")
	r.onRun = func(c domain.Command) error {
		if c.Path != "/fixture/winget.exe" {
			t.Fatalf("unexpected installer: %+v", c)
		}
		if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(alias, []byte("fixture"), 0600); err != nil {
			return err
		}
		r.versions[alias] = "2026.9.1 windows-x64"
		return nil
	}
	res, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps[0].Status != "succeeded" {
		t.Fatalf("result: %+v", res)
	}
	if path, err := e.Lookup("mise"); err != nil || path != alias {
		t.Fatalf("%s %v", path, err)
	}
	if !strings.Contains(e.ChildEnv()["PATH"], filepath.Dir(alias)) {
		t.Fatal("portable links directory was not propagated to subsequent children")
	}
}

func TestScoopUsesScriptLauncherAndLiteralArguments(t *testing.T) {
	e, r := fixture(t)
	e.GOOS = "windows"
	r.paths["scoop.ps1"] = filepath.Join(t.TempDir(), "user 空 白", "scoop.ps1")
	r.paths["pwsh"] = "/fixture/pwsh.exe"
	c, err := e.scoopCommand("search", "x;$(not-a-command)")
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/fixture/pwsh.exe" || !reflect.DeepEqual(c.Args, []string{"-NoProfile", "-File", r.paths["scoop.ps1"], "search", "x;$(not-a-command)"}) {
		t.Fatalf("unsafe launcher: %+v", c)
	}
}

func TestPinnedReleaseMetadata(t *testing.T) {
	e, _ := fixture(t)
	a, _ := e.expectedAsset()
	body := fmt.Sprintf(`{"tag_name":"v8.0.1","assets":[{"name":%q,"browser_download_url":%q,"digest":%q}]}`, a.Name, a.URL, "sha256:"+a.Digest)
	httpBytes(e, body)
	p, err := e.Plan(context.Background(), []string{"mpm-standalone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Digest != a.Digest {
		t.Fatalf("plan: %+v", p)
	}
	httpBytes(e, strings.Replace(body, a.Digest, strings.Repeat("0", 64), 1))
	if _, err := e.Plan(context.Background(), []string{"mpm-standalone"}); err == nil {
		t.Fatal("accepted changed upstream digest")
	}
}

func TestDownloadDigestAndCleanup(t *testing.T) {
	e, _ := fixture(t)
	dir := t.TempDir()
	httpBytes(e, "valid fixture")
	sum := sha256.Sum256([]byte("valid fixture"))
	digest := hex.EncodeToString(sum[:])
	path, err := e.download(context.Background(), "https://example.invalid/fixture", digest, dir, ".bin", 100)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if _, err := e.download(context.Background(), "https://example.invalid/fixture", strings.Repeat("0", 64), dir, ".bin", 100); err == nil {
		t.Fatal("accepted checksum mismatch")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatalf("failed download left files: %v", files)
	}
	if _, err := e.download(context.Background(), "https://example.invalid/fixture", digest, dir, ".bin", 3); err == nil {
		t.Fatal("accepted oversized download")
	}
}

func TestRejectUnrecognizedInstallerAndDependencies(t *testing.T) {
	e, _ := fixture(t)
	for _, p := range []domain.ActionPlan{{Kind: "install"}, {Kind: "setup", Steps: []domain.Step{{ID: "uv", URL: "https://attacker.invalid/install.sh"}}}, {Kind: "setup", Steps: []domain.Step{{ID: "mpm", DependsOn: []string{"uv"}}}}, {Kind: "setup", Steps: []domain.Step{{ID: "uv", Command: domain.Command{Path: "/bin/sh", Args: []string{"-c", "arbitrary command"}}}}}} {
		if _, err := e.Execute(context.Background(), p, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestWindowsScoopElevatedSessionIsGuidanceOnly(t *testing.T) {
	e, r := fixture(t)
	e.GOOS = "windows"
	r.paths["pwsh"] = "/fixture/pwsh.exe"
	r.psState.Elevated = true
	p, err := e.Plan(context.Background(), []string{"scoop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].URL != "" || p.Steps[0].GuideURL == "" || !strings.Contains(p.Steps[0].Description, "non-administrator") {
		t.Fatalf("plan: %+v", p)
	}
}

func TestExistingMPMVersionChangeIsVisibleInReview(t *testing.T) {
	e, r := fixture(t)
	r.paths["mpm"] = "/fixture/old-mpm"
	r.versions["/fixture/old-mpm"] = "mpm, version 7.2.0"
	p, err := e.Plan(context.Background(), []string{"mpm"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Preview, "version 7.2.0") || !strings.Contains(p.Preview, "normal uv tool constraint to 8.0.1") {
		t.Fatalf("missing version change: %s", p.Preview)
	}
}
