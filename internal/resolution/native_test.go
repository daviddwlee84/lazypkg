package resolution

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// Explicitly opted-in integration test. Copies Node and npm into owned temp
// prefixes, creates two tiny local tool fixtures, removes only one fixture, and
// verifies the retained copy and original host Node/npm files are unchanged.
func TestNativeNPMDisposablePrefixes(t *testing.T) {
	node, cli := os.Getenv("LAZYPKG_NATIVE_NPM_NODE"), os.Getenv("LAZYPKG_NATIVE_NPM_CLI")
	if node == "" || cli == "" {
		t.Skip("set LAZYPKG_NATIVE_NPM_NODE and LAZYPKG_NATIVE_NPM_CLI to opt in")
	}
	node = canonical(node)
	cli = canonical(cli)
	originalNode, originalCLI := identity(node), fileHash(cli)
	originalRC := fileHash(filepath.Join(filepath.Dir(filepath.Dir(cli)), "npmrc"))
	base := t.TempDir()
	prefixes := []string{filepath.Join(base, "first"), filepath.Join(base, "second")}
	copyOne := func(source, destination string, mode fs.FileMode) error {
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode.Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	for _, prefix := range prefixes {
		if err := copyOne(node, filepath.Join(prefix, "bin", "node"), 0755); err != nil {
			t.Fatal(err)
		}
		source := filepath.Dir(filepath.Dir(cli))
		destination := filepath.Join(prefix, "lib", "node_modules", "npm")
		if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(destination, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0755)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				if filepath.IsAbs(target) {
					t.Fatalf("native fixture refuses absolute symlink: %s", path)
				}
				return os.Symlink(target, dest)
			}
			return copyOne(path, dest, info.Mode())
		}); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(prefix, "lib", "node_modules", "@lazypkg", "resolution-fixture")
		write(t, filepath.Join(root, "package.json"), `{"name":"@lazypkg/resolution-fixture","version":"1.0.0","repository":"https://github.com/lazypkg/resolution-fixture","bin":{"resolution-fixture":"cli.js"}}`)
		write(t, filepath.Join(root, "cli.js"), "#!/usr/bin/env node\nconsole.log('fixture');\n")
		symlink(t, filepath.Join(root, "cli.js"), filepath.Join(prefix, "bin", "resolution-fixture"))
	}
	e := New(process.ExecRunner{})
	e.Dir = base
	e.Env = map[string]string{"PATH": filepath.Join(prefixes[0], "bin"), "NPM_CONFIG_CACHE": filepath.Join(base, "cache")}
	m := domain.Manager{ID: "npm", Path: filepath.Join(prefixes[0], "lib", "node_modules", "npm", "bin", "npm-cli.js"), Available: true, Requirement: ">=11.10.0", Capabilities: []string{"remove"}}
	e.Fresh = func(ctx context.Context, name string) (domain.ConflictAssessment, error) {
		r := domain.DiagnosticReport{Directory: base, Scope: "disposable test"}
		for n, prefix := range prefixes {
			path := filepath.Join(prefix, "bin", name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			r.Executables = append(r.Executables, domain.Executable{Name: name, Path: path, Target: canonical(path), PathIndex: n, Preferred: len(r.Executables) == 0})
		}
		return e.Assess(ctx, name, domain.Snapshot{Coverage: []domain.Coverage{{Manager: "npm", State: "complete"}}}, r, []domain.Manager{m})
	}
	a, err := e.Fresh(context.Background(), "resolution-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Installations) != 2 {
		t.Fatal(a)
	}
	for _, i := range a.Installations {
		if i.Status != "ready" {
			t.Fatalf("native preflight failed: %#v", i)
		}
	}
	p, err := e.Plan(context.Background(), domain.ResolutionRequest{Name: a.Name, KeepID: a.Installations[0].ID, RemoveID: a.Installations[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Execute(context.Background(), p, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if identity(node) != originalNode || fileHash(cli) != originalCLI || fileHash(filepath.Join(filepath.Dir(filepath.Dir(cli)), "npmrc")) != originalRC {
		t.Fatal("host Node/npm changed")
	}
}
