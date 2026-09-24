package resolution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type fakeRunner struct {
	output func(domain.Command) (string, error)
	run    func(domain.Command) error
	calls  []domain.Command
	runs   []domain.Command
}

func (r *fakeRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	r.calls = append(r.calls, c)
	out, err := r.output(c)
	return process.Result{Stdout: out}, err
}
func (r *fakeRunner) Run(ctx context.Context, c domain.Command, _ io.Reader, _ io.Writer, _ io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.runs = append(r.runs, c)
	if r.run != nil {
		return r.run(c)
	}
	return nil
}
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}
func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
}
func jsonText(value any) string { data, _ := json.Marshal(value); return string(data) }

type fixture struct {
	engine                                 *Engine
	runner                                 *fakeRunner
	root, brew, uv, brewBin, uvBin, cellar string
	deps                                   string
	packages                               []domain.Package
	managers                               []domain.Manager
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{root: t.TempDir()}
	f.cellar = filepath.Join(f.root, "Cellar")
	f.brew = filepath.Join(f.cellar, "yt-dlp", "1.0")
	f.uv = filepath.Join(f.root, "tools", "yt-dlp")
	f.brewBin = filepath.Join(f.root, "brew-bin", "yt-dlp")
	f.uvBin = filepath.Join(f.root, "uv-bin", "yt-dlp")
	write(t, filepath.Join(f.brew, "bin", "yt-dlp"), "brew executable")
	write(t, filepath.Join(f.brew, "INSTALL_RECEIPT.json"), `{}`)
	write(t, filepath.Join(f.uv, "bin", "yt-dlp"), "uv executable")
	write(t, filepath.Join(f.uv, "uv-receipt.toml"), "version = 1")
	write(t, filepath.Join(f.uv, "lib", "python3.13", "site-packages", "yt_dlp-1.0.dist-info", "METADATA"), "Name: yt-dlp\nVersion: 1.0\nProject-URL: Repository, https://github.com/yt-dlp/yt-dlp\n\n")
	symlink(t, filepath.Join(f.brew, "bin", "yt-dlp"), f.brewBin)
	symlink(t, filepath.Join(f.uv, "bin", "yt-dlp"), f.uvBin)
	brewCLI := filepath.Join(f.root, "managers", "brew")
	uvCLI := filepath.Join(f.root, "managers", "uv")
	write(t, brewCLI, "brew")
	write(t, uvCLI, "uv")
	f.packages = []domain.Package{{Manager: "brew", ID: "yt-dlp", Version: "1.0", Scope: "global", Root: f.brew, Commands: []string{"yt-dlp"}, ExecutablePaths: []string{f.brewBin}}, {Manager: "uvx", ID: "yt-dlp", Version: "1.0", Scope: "global", Root: f.uv, Commands: []string{"yt-dlp"}, ExecutablePaths: []string{f.uvBin}}}
	f.managers = []domain.Manager{{ID: "brew", Path: brewCLI, Available: true, Capabilities: []string{"remove"}}, {ID: "uvx", Path: uvCLI, Available: true, Capabilities: []string{"remove"}}}
	f.runner = &fakeRunner{output: func(c domain.Command) (string, error) {
		if c.Path == brewCLI {
			switch strings.Join(c.Args, " ") {
			case "--cellar":
				return f.cellar, nil
			case "uses --installed --recursive yt-dlp":
				return f.deps, nil
			case "info --json=v2 --formula yt-dlp":
				return `{"formulae":[{"name":"yt-dlp","full_name":"yt-dlp","homepage":"https://github.com/yt-dlp/yt-dlp","installed":[{"version":"1.0"}]}]}`, nil
			}
		}
		if c.Path == uvCLI && strings.Join(c.Args, " ") == "--color never --no-progress tool list --show-paths" {
			return "yt-dlp v1.0 (" + f.uv + ")\n- yt-dlp (" + f.uvBin + ")\n", nil
		}
		return "", errors.New("unexpected query: " + process.Display(c))
	}}
	f.engine = New(f.runner)
	f.engine.Dir = f.root
	f.engine.Env = map[string]string{"PATH": filepath.Dir(f.uvBin) + string(os.PathListSeparator) + filepath.Dir(f.brewBin), "MISE_AUTO_INSTALL": "1"}
	f.engine.Fresh = func(ctx context.Context, name string) (domain.ConflictAssessment, error) {
		report := domain.DiagnosticReport{Directory: f.root, Scope: "fixture"}
		pkgs := []domain.Package{}
		for _, p := range f.packages {
			if _, err := os.Stat(p.Root); err != nil {
				continue
			}
			pkgs = append(pkgs, p)
			path := f.brewBin
			index := 1
			if p.Manager == "uvx" {
				path = f.uvBin
				index = 0
			}
			report.Executables = append(report.Executables, domain.Executable{Name: name, Path: path, Target: canonical(path), PathIndex: index, PackageKey: p.Key(), Manager: p.Manager})
			if p.Manager == "brew" {
				report.Executables = append(report.Executables, domain.Executable{Name: name, Path: filepath.Join(f.brew, "bin", "yt-dlp"), Target: canonical(path), PathIndex: -1, PackageKey: p.Key(), Manager: p.Manager, EquivalentTo: path})
			}
		}
		winner := ""
		best := 99
		for _, p := range report.Executables {
			if p.PathIndex >= 0 && p.PathIndex < best {
				best = p.PathIndex
				winner = p.Path
			}
		}
		for index := range report.Executables {
			report.Executables[index].Preferred = report.Executables[index].Path == winner
		}
		for n := range pkgs {
			pkgs[n].InventoryAt = time.Now()
		}
		return f.engine.Assess(ctx, name, domain.Snapshot{Packages: pkgs, Coverage: []domain.Coverage{{Manager: "brew", State: "complete"}, {Manager: "uvx", State: "complete"}}}, report, f.managers)
	}
	return f
}
func requestFor(t *testing.T, a domain.ConflictAssessment, keep, remove string) domain.ResolutionRequest {
	t.Helper()
	r := domain.ResolutionRequest{Name: a.Name}
	for _, i := range a.Installations {
		if i.Package.Manager == keep {
			r.KeepID = i.ID
		}
		if i.Package.Manager == remove {
			r.RemoveID = i.ID
		}
	}
	if r.KeepID == "" || r.RemoveID == "" {
		t.Fatalf("missing sources %#v", a)
	}
	return r
}

func TestGroupingDependenciesAndRetainedSource(t *testing.T) {
	f := newFixture(t)
	a, err := f.engine.Fresh(context.Background(), "yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Installations) != 2 {
		t.Fatalf("three paths must be two installations: %#v", a)
	}
	for _, i := range a.Installations {
		if i.Status != "ready" {
			t.Fatalf("preflight failed: %#v", i)
		}
	}
	f.deps = "summarize\n"
	a, err = f.engine.Fresh(context.Background(), "yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.engine.Plan(context.Background(), requestFor(t, a, "uvx", "brew")); err == nil || !strings.Contains(err.Error(), "summarize") {
		t.Fatalf("dependent formula removal allowed: %v", err)
	}
	if _, err = f.engine.Plan(context.Background(), requestFor(t, a, "brew", "uvx")); err != nil {
		t.Fatalf("blocked-for-removal is valid retained source: %v", err)
	}
	for _, c := range f.runner.calls {
		if c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["MISE_NOT_FOUND_AUTO_INSTALL"] != "false" {
			t.Fatal("read auto-install guard missing", c)
		}
	}
}

func TestRevalidationTamperAndNativePostcheck(t *testing.T) {
	for _, mode := range []string{"tamper", "changed dependency", "no removal", "success"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			a, _ := f.engine.Fresh(context.Background(), "yt-dlp")
			p, err := f.engine.Plan(context.Background(), requestFor(t, a, "uvx", "brew"))
			if err != nil {
				t.Fatal(err)
			}
			if !p.Resolution.Keep.Package.InventoryAt.IsZero() || !p.Resolution.Remove.Package.InventoryAt.IsZero() {
				t.Fatal("observation time leaked into reviewed binding")
			}
			switch mode {
			case "tamper":
				p.Preview = "unreviewed"
			case "changed dependency":
				f.deps = "summarize"
			case "success":
				f.runner.run = func(c domain.Command) error {
					if c.Env["HOMEBREW_NO_AUTOREMOVE"] != "1" {
						t.Fatal("Homebrew defaults to autoremove", c)
					}
					if strings.Join(c.Args, " ") != "uninstall --formula yt-dlp" {
						t.Fatal(c)
					}
					if err := os.RemoveAll(f.brew); err != nil {
						return err
					}
					return os.Remove(f.brewBin)
				}
			}
			result, err := f.engine.Execute(context.Background(), p, nil, io.Discard, io.Discard)
			if mode == "success" {
				if err != nil || !strings.Contains(result.Message, "retained") || result.Steps[0].Status != "success" {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("unsafe/failed plan was accepted", result)
			}
			if mode == "no removal" && result.Steps[0].Status != "unverified" {
				t.Fatal("postcheck failure was reported successful", result)
			}
			if (mode == "tamper" || mode == "changed dependency") && len(f.runner.runs) > 0 {
				t.Fatal("mutation before revalidation")
			}
		})
	}
}

func TestStaleOrMissingInventoryCannotAuthorizeRemoval(t *testing.T) {
	f := newFixture(t)
	a, err := f.engine.Fresh(context.Background(), "yt-dlp")
	if err != nil {
		t.Fatal(err)
	}
	report := domain.DiagnosticReport{Directory: f.root}
	for _, i := range a.Installations {
		report.Executables = append(report.Executables, i.Paths...)
	}
	for _, coverage := range [][]domain.Coverage{nil, {{Manager: "brew", State: "failed"}, {Manager: "uvx", State: "complete", Stale: true}}} {
		assessment, err := f.engine.Assess(context.Background(), "yt-dlp", domain.Snapshot{Packages: f.packages, Coverage: coverage}, report, f.managers)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range assessment.Installations {
			if i.Status != "blocked" || !strings.Contains(strings.Join(i.Blockers, " "), "Fresh complete") {
				t.Fatal(i)
			}
		}
	}
}

func TestCancellationAndUnverifiedOwners(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.engine.Fresh(ctx, "yt-dlp"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	paths := []domain.Executable{{Name: "thing", Path: filepath.Join(f.root, "manual1"), Target: filepath.Join(f.root, "manual1"), PathIndex: 0}, {Name: "thing", Path: filepath.Join(f.root, "manual2"), Target: filepath.Join(f.root, "manual2"), PathIndex: 1}}
	a, err := f.engine.Assess(context.Background(), "thing", domain.Snapshot{}, domain.DiagnosticReport{Executables: paths}, nil)
	if err != nil || len(a.Installations) != 2 || a.Installations[0].ID == a.Installations[1].ID {
		t.Fatal(a, err)
	}
	for _, i := range a.Installations {
		if i.Status != "unknown" || len(i.Blockers) == 0 {
			t.Fatal(i)
		}
	}
	if projectURL("https://evilgithub.com/yt-dlp/yt-dlp") != "" || projectURL("https://github.com.evil/x/y") != "" {
		t.Fatal("untrusted repository host matched")
	}
}

func TestAllOtherRemovableProvidersOfferAssessment(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"apt", "cargo", "cask", "choco", "dnf", "flatpak", "gem", "mise", "pacman", "pipx", "rustup", "scoop", "snap", "winget"} {
		t.Run(id, func(t *testing.T) {
			p := domain.Package{Manager: id, ID: "thing", Version: "1", Root: filepath.Join(f.root, id)}
			path := domain.Executable{Name: "thing", Path: filepath.Join(p.Root, "thing"), PackageKey: p.Key(), PathIndex: 0}
			a, err := f.engine.Assess(context.Background(), "thing", domain.Snapshot{Packages: []domain.Package{p}}, domain.DiagnosticReport{Executables: []domain.Executable{path}}, []domain.Manager{{ID: id, Available: true, Capabilities: []string{"remove"}}})
			if err != nil || len(a.Installations) != 1 || a.Installations[0].Status == "ready" || len(a.Installations[0].Blockers) == 0 {
				t.Fatal(a, err)
			}
		})
	}
}
