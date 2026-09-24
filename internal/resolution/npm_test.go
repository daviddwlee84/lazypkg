package resolution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type npmFixture struct {
	engine   *Engine
	runner   *fakeRunner
	prefixes []string
	manager  domain.Manager
	outside  bool
	version  string
}

func newNPMFixture(t *testing.T) *npmFixture {
	if runtime.GOOS == "windows" {
		t.Skip("multi-prefix npm execution currently supports POSIX layouts")
	}
	f := &npmFixture{prefixes: []string{filepath.Join(t.TempDir(), "selected"), filepath.Join(t.TempDir(), "alternative")}, version: "11.10.0"}
	for _, prefix := range f.prefixes {
		root := filepath.Join(prefix, "lib", "node_modules", "@example", "tool")
		write(t, filepath.Join(root, "package.json"), `{"name":"@example/tool","version":"1.0.0","repository":"https://github.com/example/tool.git","bin":{"tool":"cli.js","other":"other.js"}}`)
		for _, name := range []string{"cli", "other"} {
			write(t, filepath.Join(root, name+".js"), "#!/usr/bin/env node\n")
		}
		symlink(t, filepath.Join(root, "cli.js"), filepath.Join(prefix, "bin", "tool"))
		symlink(t, filepath.Join(root, "other.js"), filepath.Join(prefix, "bin", "other"))
		write(t, filepath.Join(prefix, "bin", "node"), "node")
		write(t, filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js"), "npm")
		write(t, filepath.Join(prefix, "lib", "node_modules", "npm", "node_modules", "@npmcli", "arborist", "package.json"), `{"version":"9.0.0"}`)
	}
	f.manager = domain.Manager{ID: "npm", Path: filepath.Join(f.prefixes[0], "lib", "node_modules", "npm", "bin", "npm-cli.js"), Version: "11.10.0", Requirement: ">=11.10.0", Available: true, Capabilities: []string{"remove"}}
	f.runner = &fakeRunner{output: func(c domain.Command) (string, error) {
		if len(c.Args) > 0 && c.Args[0] == "-e" {
			if c.Args[1] != npmImpactScript {
				return "", errors.New("unknown script")
			}
			if _, err := os.Stat(c.Args[5]); err != nil {
				t.Fatal("private impact cache missing", err)
			}
			root := filepath.Join(c.Args[3], "node_modules", filepath.FromSlash(c.Args[4]))
			changes := []npmChange{{Action: "REMOVE", Name: "@example/tool", Version: "1.0.0", Path: root}}
			if f.outside {
				changes = append(changes, npmChange{Action: "REMOVE", Name: "different", Version: "2", Path: filepath.Join(c.Args[3], "node_modules", "different")})
			}
			return jsonText(map[string]any{"changes": changes}), nil
		}
		if c.Args[len(c.Args)-1] == "--version" {
			return f.version, nil
		}
		if c.Args[len(c.Args)-1] == "root" {
			for index, arg := range c.Args {
				if arg == "--prefix" {
					return filepath.Join(c.Args[index+1], "lib", "node_modules"), nil
				}
			}
		}
		return "", errors.New("unexpected npm query: " + process.Display(c))
	}}
	f.engine = New(f.runner)
	f.engine.Dir = t.TempDir()
	f.engine.Env = map[string]string{"PATH": filepath.Join(f.prefixes[0], "bin"), "NODE_OPTIONS": "--require unsafe", "NPM_CONFIG_PREFIX": "wrong"}
	f.engine.Fresh = func(ctx context.Context, name string) (domain.ConflictAssessment, error) {
		report := domain.DiagnosticReport{Directory: f.engine.Dir, Scope: "test"}
		for index, prefix := range f.prefixes {
			path := filepath.Join(prefix, "bin", name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			report.Executables = append(report.Executables, domain.Executable{Name: name, Path: path, Target: canonical(path), Manager: "mise", PathIndex: index, Preferred: len(report.Executables) == 0})
		}
		return f.engine.Assess(ctx, name, domain.Snapshot{Coverage: []domain.Coverage{{Manager: "npm", State: "complete"}}}, report, []domain.Manager{f.manager})
	}
	return f
}

func TestNPMContextsRemainSeparateAndNoUnsupportedBypass(t *testing.T) {
	f := newNPMFixture(t)
	a, err := f.engine.Fresh(context.Background(), "tool")
	if err != nil || len(a.Installations) != 2 {
		t.Fatal(a, err)
	}
	for _, i := range a.Installations {
		if i.Package.Manager != "npm" || i.Status != "ready" || len(i.Commands) != 2 {
			t.Fatal(i)
		}
	}
	if a.Installations[0].ID == a.Installations[1].ID || a.Installations[0].Prefix == a.Installations[1].Prefix {
		t.Fatal("contexts merged", a)
	}
	f.manager.Available = false
	f.runner.calls = nil
	a, err = f.engine.Fresh(context.Background(), "tool")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runner.calls) != 0 {
		t.Fatal("queried another npm despite selected adapter unavailable", f.runner.calls)
	}
	for _, i := range a.Installations {
		if i.Status != "blocked" || !strings.Contains(strings.Join(i.Blockers, " "), "Repair the selected npm") {
			t.Fatal(i)
		}
	}
}

func TestNPMImpactAndVersionAreRequired(t *testing.T) {
	for _, mode := range []string{"outside effects", "old version", "failed probe"} {
		t.Run(mode, func(t *testing.T) {
			f := newNPMFixture(t)
			switch mode {
			case "outside effects":
				f.outside = true
			case "old version":
				f.version = "11.6.2"
			case "failed probe":
				f.runner.output = func(domain.Command) (string, error) { return "", errors.New("failure") }
			}
			a, err := f.engine.Fresh(context.Background(), "tool")
			if err != nil {
				t.Fatal(err)
			}
			for _, i := range a.Installations {
				if i.Status != "blocked" {
					t.Fatal(i)
				}
			}
		})
	}
}

func TestNPMBoundRemovalAndKeepPostcheck(t *testing.T) {
	f := newNPMFixture(t)
	a, _ := f.engine.Fresh(context.Background(), "tool")
	req := domain.ResolutionRequest{Name: "tool", KeepID: a.Installations[0].ID, RemoveID: a.Installations[1].ID}
	p, err := f.engine.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	removed := p.Resolution.Remove
	if removed.Prefix != canonical(f.prefixes[1]) || p.Resolution.Command.Path != canonical(filepath.Join(f.prefixes[1], "bin", "node")) {
		t.Fatal("wrong npm context", p)
	}
	if p.Resolution.Command.Env["NODE_OPTIONS"] != "" || p.Resolution.Command.Env["NPM_CONFIG_PREFIX"] != "" {
		t.Fatal("unsafe config retained", p.Resolution.Command)
	}
	f.runner.run = func(c domain.Command) error {
		if !strings.Contains(process.Display(c), "--global --prefix") || !strings.Contains(process.Display(c), "--ignore-scripts") {
			t.Fatal(c)
		}
		if err := os.RemoveAll(removed.Package.Root); err != nil {
			return err
		}
		for _, name := range []string{"tool", "other"} {
			if err := os.Remove(filepath.Join(removed.Prefix, "bin", name)); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err = f.engine.Execute(context.Background(), p, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.runner.calls {
		if len(c.Args) > 0 && c.Args[0] == "-e" {
			if _, err := os.Stat(c.Args[5]); !os.IsNotExist(err) {
				t.Fatal("private probe cache leaked", c.Args[5], err)
			}
		}
	}
}

func TestRetainedNPMSharedDependencyPreventsRemoval(t *testing.T) {
	f := newNPMFixture(t)
	a, err := f.engine.Fresh(context.Background(), "tool")
	if err != nil {
		t.Fatal(err)
	}
	a.Installations[0].RequiredPaths = []string{filepath.Join(a.Installations[1].Package.Root, "dependency")}
	req := domain.ResolutionRequest{Name: a.Name, KeepID: a.Installations[0].ID, RemoveID: a.Installations[1].ID}
	if _, err = f.engine.plan(req, a); err == nil || !strings.Contains(err.Error(), "retained installation requires") {
		t.Fatal("shared dependency was not blocked", err)
	}
}

func TestSelectedIndependentNPMThroughMiseShims(t *testing.T) {
	f := newNPMFixture(t)
	dir := t.TempDir()
	mise := filepath.Join(dir, "manager", "mise")
	write(t, mise, "mise")
	for _, name := range []string{"node", "npm", "mise"} {
		symlink(t, mise, filepath.Join(dir, "shims", name))
	}
	cli := f.manager.Path
	f.manager.Path = filepath.Join(dir, "shims", "npm")
	f.engine.Env["PATH"] = filepath.Join(dir, "shims") + string(os.PathListSeparator) + filepath.Join(f.prefixes[0], "bin")
	original := f.runner.output
	f.runner.output = func(c domain.Command) (string, error) {
		if c.Path == canonical(mise) {
			switch strings.Join(c.Args, " ") {
			case "which npm":
				return cli, nil
			case "which node":
				return filepath.Join(f.prefixes[0], "bin", "node"), nil
			}
		}
		return original(c)
	}
	a, err := f.engine.Fresh(context.Background(), "tool")
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range a.Installations {
		if i.Status != "ready" {
			t.Fatal(i)
		}
		if i.Effective && i.ManagerPath != canonical(cli) {
			t.Fatal("selected shim npm replaced with another context", i)
		}
	}
}
