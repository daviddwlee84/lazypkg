// Package bootstrap installs explicitly selected managers without depending on mpm.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

const installerToken = "<downloaded-official-installer>"

type Engine struct {
	Runner                process.Runner
	GOOS, GOARCH, DataDir string
	HTTPClient            *http.Client
	mu                    sync.Mutex
	known                 map[string]string
	additions             []string
	lookPath              func(string) (string, error)
	home                  string
}

func New(r process.Runner, dataDir string) *Engine {
	home, _ := os.UserHomeDir()
	return &Engine{Runner: r, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, DataDir: dataDir,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute}, known: map[string]string{}, lookPath: exec.LookPath, home: home}
}

func (e *Engine) standalonePath() string {
	name := "mpm"
	if e.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e.DataDir, "bin", name)
}

// Lookup also searches conventional user installation locations. It never installs.
func (e *Engine) Lookup(name string) (string, error) {
	e.mu.Lock()
	p := e.known[name]
	e.mu.Unlock()
	if p != "" {
		return p, nil
	}
	// An explicitly installed standalone backend remains usable across sessions,
	// even when an older mpm happens to precede it in the user's shell PATH.
	if name == "mpm" && filepath.IsAbs(e.DataDir) {
		p := e.standalonePath()
		if info, err := os.Stat(p); err == nil && !info.IsDir() && (e.GOOS == "windows" || info.Mode()&0111 != 0) {
			return p, nil
		}
	}
	lookup := e.lookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	names := []string{name}
	if e.GOOS == "windows" && name == "scoop" {
		names = []string{"scoop.ps1", "scoop"}
	}
	for _, n := range names {
		if p, err := lookup(n); err == nil {
			return p, nil
		}
	}
	for _, p := range e.candidates(name) {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && (e.GOOS == "windows" || info.Mode()&0111 != 0) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s was not found in PATH or known installation locations", name)
}

func (e *Engine) candidates(name string) []string {
	ext := ""
	if e.GOOS == "windows" {
		ext = ".exe"
	}
	paths := []string{filepath.Join(e.home, ".local", "bin", name+ext), filepath.Join(e.home, ".cargo", "bin", name+ext)}
	if name == "uv" {
		if dir := os.Getenv("UV_INSTALL_DIR"); filepath.IsAbs(dir) {
			paths = append([]string{filepath.Join(dir, "uv"+ext)}, paths...)
		}
	}
	if name == "mise" {
		if path := os.Getenv("MISE_INSTALL_PATH"); filepath.IsAbs(path) {
			paths = append([]string{path}, paths...)
		}
	}
	if name == "mpm" {
		paths = append(paths, e.standalonePath())
	}
	if e.GOOS == "darwin" {
		paths = append(paths, filepath.Join("/opt/homebrew/bin", name), filepath.Join("/usr/local/bin", name))
	}
	if e.GOOS == "windows" {
		scoop := os.Getenv("SCOOP")
		if scoop == "" {
			scoop = filepath.Join(e.home, "scoop")
		}
		if name == "scoop" {
			paths = append(paths, filepath.Join(scoop, "shims", "scoop.ps1"), filepath.Join(scoop, "apps", "scoop", "current", "bin", "scoop.ps1"))
		} else {
			paths = append(paths, filepath.Join(scoop, "shims", name+".exe"))
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			paths = append(paths, filepath.Join(local, "Microsoft", "WindowsApps", name+".exe"))
			// WinGet portable installers publish aliases here. Their User PATH
			// update does not reach an already-running lazypkg process.
			paths = append(paths, filepath.Join(local, "Microsoft", "WinGet", "Links", name+".exe"))
		}
		if name == "choco" {
			root := os.Getenv("ChocolateyInstall")
			if root == "" {
				root = filepath.Join(os.Getenv("ProgramData"), "chocolatey")
			}
			paths = append(paths, filepath.Join(root, "bin", "choco.exe"))
		}
	}
	return paths
}

// ChildEnv preserves inherited PATH entries, including project/runtime activation.
// It changes neither this process's environment nor the invoking shell's environment.
func (e *Engine) ChildEnv() map[string]string {
	e.mu.Lock()
	additions := append([]string(nil), e.additions...)
	e.mu.Unlock()
	if len(additions) == 0 {
		return nil
	}
	separator := string(os.PathListSeparator)
	if e.GOOS == "windows" {
		separator = ";"
	}
	seen := map[string]bool{}
	parts := []string{}
	for _, p := range append(additions, strings.Split(os.Getenv("PATH"), separator)...) {
		key := p
		if e.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if p != "" && !seen[key] {
			seen[key] = true
			parts = append(parts, p)
		}
	}
	return map[string]string{"PATH": strings.Join(parts, separator)}
}

func (e *Engine) remember(name, path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.known == nil {
		e.known = map[string]string{}
	}
	e.known[name] = path
	dir := filepath.Dir(path)
	for _, p := range e.additions {
		if p == dir {
			return
		}
	}
	e.additions = append(e.additions, dir)
}

func (e *Engine) command(name string, args ...string) domain.Command {
	p, err := e.Lookup(name)
	if err != nil {
		p = name
	}
	return domain.Command{Path: p, Args: args, Env: e.ChildEnv()}
}

// ResolveMPM selects a tested backend among app-owned, PATH and uv-tool candidates.
// The provider additionally checks its JSON protocol before using it.
func (e *Engine) ResolveMPM(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	candidates := []string{}
	if p, err := e.Lookup("mpm"); err == nil {
		candidates = append(candidates, p)
	}
	lookup := e.lookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	if p, err := lookup("mpm"); err == nil {
		candidates = append(candidates, p)
	}
	for _, p := range e.candidates("mpm") {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			candidates = append(candidates, p)
		}
	}
	if _, err := e.Lookup("uv"); err == nil {
		res, err := e.output(ctx, e.command("uv", "tool", "dir", "--bin"))
		if err == nil {
			dir := strings.TrimSpace(res.Stdout)
			if filepath.IsAbs(dir) {
				name := "mpm"
				if e.GOOS == "windows" {
					name += ".exe"
				}
				p := filepath.Join(dir, name)
				if info, err := os.Stat(p); err == nil && !info.IsDir() {
					candidates = append(candidates, p)
				}
			}
		}
	}
	seen := map[string]bool{}
	problems := []string{}
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res, err := e.output(ctx, domain.Command{Path: p, Args: []string{"--version"}, Env: e.ChildEnv()})
		if err == nil && versionPattern.FindString(res.Stdout) == domain.MPMVersion {
			return p, nil
		}
		if err != nil {
			problems = append(problems, p+": version probe failed")
		} else {
			problems = append(problems, p+": reports "+strings.TrimSpace(res.Stdout))
		}
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("mpm %s is required; %s. Review lazypkg setup to install the tested version; existing tools were not changed", domain.MPMVersion, strings.Join(problems, "; "))
	}
	return "", errors.New("mpm was not found; run lazypkg setup and select mpm (uv tool) or mpm standalone")
}

func (e *Engine) output(ctx context.Context, c domain.Command) (process.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// A discovered manager may itself be a mise shim. Read-only setup and
	// backend detection must not install the shim's missing runtime.
	env := make(map[string]string, len(c.Env)+2)
	for k, v := range c.Env {
		env[k] = v
	}
	env["MISE_AUTO_INSTALL"] = "0"
	env["MISE_NOT_FOUND_AUTO_INSTALL"] = "false"
	c.Env = env
	return e.Runner.Output(ctx, c)
}

var versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

func (e *Engine) installed(ctx context.Context, name string) bool {
	if name == "mpm-standalone" {
		name = "mpm"
	}
	if name == "mpm" {
		_, err := e.ResolveMPM(ctx)
		return err == nil
	}
	if _, err := e.Lookup(name); err != nil {
		return false
	}
	c := e.command(name, "--version")
	if name == "scoop" {
		var err error
		c, err = e.scoopCommand("--version")
		if err != nil {
			return false
		}
	}
	res, err := e.output(ctx, c)
	return err == nil && (versionPattern.FindString(res.Stdout) != "" || name == "scoop" && strings.Contains(strings.ToLower(res.Stdout), "scoop"))
}

func (e *Engine) Options(ctx context.Context) ([]domain.SetupOption, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts := []domain.SetupOption{
		{ID: "mpm", Name: "mpm (uv tool)", Description: "Install mpm " + domain.MPMVersion + " in your normal uv tool environment; uv is added if missing.", Recommended: true},
		{ID: "mpm-standalone", Name: "mpm standalone", Description: "Alternative: verified official mpm binary in lazypkg's data directory; no Python or uv required."},
		{ID: "uv", Name: "uv", Description: "Python package and tool manager; does not select or install a Python runtime by itself.", Recommended: true},
		{ID: "mise", Name: "mise", Description: "Runtime and tool version manager. Shell activation is a separate choice.", Recommended: true},
	}
	switch e.GOOS {
	case "darwin":
		opts = append(opts, domain.SetupOption{ID: "brew", Name: "Homebrew", Description: "macOS packages and applications; official installer may request administrator rights.", Recommended: true})
	case "windows":
		opts = append(opts, domain.SetupOption{ID: "scoop", Name: "Scoop", Description: "User-level Windows CLI packages; requires a non-administrator PowerShell.", Recommended: true}, domain.SetupOption{ID: "winget", Name: "WinGet", Description: "Use existing WinGet; installation and App Installer repair use official guidance.", GuideURL: "https://learn.microsoft.com/windows/package-manager/winget/"}, domain.SetupOption{ID: "choco", Name: "Chocolatey", Description: "Use existing Chocolatey; bootstrap uses official guidance.", GuideURL: "https://chocolatey.org/install"})
	case "linux":
	default:
		return nil, fmt.Errorf("setup is not supported on %s", e.GOOS)
	}
	var mpmInstalled bool
	for i := range opts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts[i].ID == "mpm-standalone" {
			opts[i].Installed = mpmInstalled
		} else {
			opts[i].Installed = e.installed(ctx, opts[i].ID)
		}
		if opts[i].ID == "mpm" {
			mpmInstalled = opts[i].Installed
		}
		if !opts[i].Installed && opts[i].ID != "mpm" && opts[i].ID != "mpm-standalone" {
			if path, err := e.Lookup(opts[i].ID); err == nil {
				opts[i].Description = "Detected at " + path + ", but its version probe failed. Repair the existing installation before setup."
				opts[i].GuideURL = managerGuide(opts[i].ID)
				opts[i].Recommended = false
			}
		}
	}
	return opts, nil
}

func managerGuide(id string) string {
	return map[string]string{"uv": "https://docs.astral.sh/uv/getting-started/installation/", "mise": "https://mise.jdx.dev/installing-mise.html", "brew": "https://docs.brew.sh/Installation", "scoop": "https://github.com/ScoopInstaller/Install", "winget": "https://learn.microsoft.com/windows/package-manager/winget/", "choco": "https://chocolatey.org/install"}[id]
}

func (e *Engine) scoopCommand(args ...string) (domain.Command, error) {
	p, err := e.Lookup("scoop")
	if err != nil {
		return domain.Command{}, err
	}
	if strings.EqualFold(filepath.Ext(p), ".exe") {
		return domain.Command{Path: p, Args: args, Env: e.ChildEnv()}, nil
	}
	if strings.EqualFold(filepath.Ext(p), ".cmd") {
		p = strings.TrimSuffix(p, filepath.Ext(p)) + ".ps1"
		if _, err := os.Stat(p); err != nil {
			return domain.Command{}, errors.New("Scoop CMD launcher has no companion PowerShell script")
		}
	}
	if !strings.EqualFold(filepath.Ext(p), ".ps1") {
		return domain.Command{}, errors.New("Scoop needs a PowerShell launcher on Windows")
	}
	ps, err := e.powerShell()
	if err != nil {
		return domain.Command{}, err
	}
	return domain.Command{Path: ps, Args: append([]string{"-NoProfile", "-File", p}, args...), Env: e.ChildEnv()}, nil
}

func (e *Engine) powerShell() (string, error) {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := e.Lookup(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("PowerShell 5.1 or later is required")
}

type powershellState struct {
	Major         int    `json:"major"`
	Minor         int    `json:"minor"`
	Language      string `json:"language"`
	Policy        string `json:"policy"`
	MachinePolicy string `json:"machinePolicy"`
	UserPolicy    string `json:"userPolicy"`
	Elevated      bool   `json:"elevated"`
}

const powershellProbe = `[pscustomobject]@{ major=$PSVersionTable.PSVersion.Major; minor=$PSVersionTable.PSVersion.Minor; language=$ExecutionContext.SessionState.LanguageMode.ToString(); policy=(Get-ExecutionPolicy).ToString(); machinePolicy=(Get-ExecutionPolicy -Scope MachinePolicy).ToString(); userPolicy=(Get-ExecutionPolicy -Scope UserPolicy).ToString(); elevated=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) } | ConvertTo-Json -Compress`

func (e *Engine) windowsReady(ctx context.Context, scoop bool) (string, error) {
	ps, err := e.powerShell()
	if err != nil {
		return "", err
	}
	res, err := e.output(ctx, domain.Command{Path: ps, Args: []string{"-NoProfile", "-NonInteractive", "-Command", powershellProbe}})
	if err != nil {
		return "", fmt.Errorf("PowerShell prerequisites could not be checked: %w", err)
	}
	var s powershellState
	if err := json.Unmarshal([]byte(res.Stdout), &s); err != nil {
		return "", fmt.Errorf("invalid PowerShell prerequisite report: %w", err)
	}
	if s.Major < 5 || (s.Major == 5 && s.Minor < 1) || s.Language != "FullLanguage" {
		return "", errors.New("PowerShell 5.1+ with FullLanguage mode is required")
	}
	for _, policy := range []string{s.MachinePolicy, s.UserPolicy} {
		if policy != "" && policy != "Undefined" && policy != "Bypass" && policy != "Unrestricted" && policy != "RemoteSigned" {
			return "", errors.New("organization execution policy blocks this installer; follow your administrator's guidance")
		}
	}
	if scoop && s.Elevated {
		return "", errors.New("Scoop setup requires a non-administrator terminal")
	}
	if scoop && s.Policy != "RemoteSigned" && s.Policy != "Unrestricted" && s.Policy != "Bypass" {
		return "", errors.New("Scoop requires an allowed execution policy; review the official CurrentUser RemoteSigned setup instructions")
	}
	return ps, nil
}
