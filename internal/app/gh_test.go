package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type ghAppRunner struct {
	mu              sync.Mutex
	path, manifest  string
	version, latest string
	old             bool
	runs            []domain.Command
	dryReads        int
	managerReads    int
}

func (r *ghAppRunner) Output(_ context.Context, c domain.Command) (process.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := strings.Join(c.Args, " ")
	if c.Path == "fake-mpm" {
		if args == "--version" {
			return process.Result{Stdout: "mpm, version 8.0.1"}, nil
		}
		if strings.Contains(args, "managers") {
			r.managerReads++
			return process.Result{Stdout: fmt.Sprintf(`{"gh-ext":{"id":"gh-ext","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":%q,"version":"2.101.0"}}`, r.path)}, nil
		}
		if strings.Contains(args, "--plan") && strings.Contains(args, "--gh-ext install -- pkg:gh-ext/owner/gh-new") {
			return process.Result{Stdout: r.path + " extension install owner/gh-new\n"}, nil
		}
		return process.Result{}, fmt.Errorf("gh inventory/update must not use mpm: %s", args)
	}
	if c.Path == r.path {
		if args == "extension upgrade --help" {
			if r.old {
				return process.Result{Stdout: "--all --force"}, nil
			}
			return process.Result{Stdout: "  --dry-run   Only display upgrades"}, nil
		}
		if args == "extension upgrade owner/gh-dash --dry-run" {
			r.dryReads++
			if r.version == r.latest {
				return process.Result{Stdout: "[dash]: already up to date\n"}, nil
			}
			return process.Result{Stdout: fmt.Sprintf("[dash]: would have upgraded from %s to %s\n", r.version, r.latest)}, nil
		}
	}
	return process.Result{}, fmt.Errorf("unexpected query %s %s", c.Path, args)
}
func (r *ghAppRunner) Run(_ context.Context, c domain.Command, _ io.Reader, _, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, c)
	if c.Path != r.path || strings.Join(c.Args, " ") != "extension upgrade owner/gh-dash" {
		return fmt.Errorf("unexpected mutation")
	}
	r.version = r.latest
	return os.WriteFile(r.manifest, []byte("owner: owner\nname: gh-dash\nhost: github.com\ntag: "+r.version+"\nispinned: false\n"), 0600)
}
func ghAppFixture(t *testing.T) (*App, *ghAppRunner) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	dir := filepath.Join(root, "gh", "extensions", "gh-dash")
	path := filepath.Join(root, "fixture-gh")
	if err := os.WriteFile(path, []byte("fixture launcher"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := "gh-dash"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture extension"), 0755); err != nil {
		t.Fatal(err)
	}
	r := &ghAppRunner{path: path, manifest: filepath.Join(dir, "manifest.yml"), version: "v1.0.0", latest: "v2.0.0"}
	if err := os.WriteFile(r.manifest, []byte("owner: owner\nname: gh-dash\nhost: github.com\ntag: v1.0.0\nispinned: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return testApp(t, r), r
}
func TestGHExtensionAppRoutesInventoryUpdateAndVerifiedMutation(t *testing.T) {
	a, r := ghAppFixture(t)
	ctx := context.Background()
	managers, err := a.Managers(ctx)
	if err != nil || len(managers) != 1 || managers[0].Scope != "global" || !managers[0].Supports("outdated") || managers[0].Instance == "" {
		t.Fatal(managers, err)
	}
	installed, err := a.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: []string{"gh-ext"}})
	if err != nil || len(installed.Packages) != 1 || installed.Packages[0].Extension == nil || !freshInventory(installed, "gh-ext") {
		t.Fatal(installed, err)
	}
	updates, err := a.Query(ctx, domain.PackageQuery{Kind: "outdated", Managers: []string{"gh-ext"}})
	if err != nil || len(updates.Packages) != 1 || updates.Packages[0].Latest != "v2.0.0" {
		t.Fatal(updates, err)
	}
	p, err := a.Plan(ctx, domain.ActionRequest{Operation: "upgrade", Manager: "gh-ext", Package: "owner/gh-dash"})
	if err != nil || p.GHExtension == nil || len(r.runs) != 0 {
		t.Fatal(p, err)
	}
	result, err := a.Execute(ctx, p, nil, io.Discard, io.Discard)
	if err != nil || len(r.runs) != 1 || result.Steps[0].Status != "success" {
		t.Fatal(result, err, r.runs)
	}
	p, err = a.Plan(ctx, domain.ActionRequest{Operation: "upgrade", Manager: "gh-ext", Package: "owner/gh-dash"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = a.Execute(ctx, p, nil, io.Discard, io.Discard)
	if err != nil || len(r.runs) != 1 || result.Steps[0].Status != "current" {
		t.Fatal("current extension was mutated", result, err, r.runs)
	}
}
func TestOldGHExtensionKeepsInventoryButDoesNotRunUpdates(t *testing.T) {
	a, r := ghAppFixture(t)
	r.old = true
	ctx := context.Background()
	s, err := a.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: []string{"gh-ext"}})
	if err != nil || len(s.Packages) != 1 {
		t.Fatal(s, err)
	}
	s, err = a.Query(ctx, domain.PackageQuery{Kind: "outdated", Managers: []string{"gh-ext"}})
	if err != nil || len(s.Coverage) != 1 || s.Coverage[0].State != "unsupported" || r.dryReads != 0 {
		t.Fatal(s, err, r.dryReads)
	}
	if _, err = a.Plan(ctx, domain.ActionRequest{Operation: "upgrade", Manager: "gh-ext", Package: "owner/gh-dash"}); err == nil {
		t.Fatal("old gh upgrade bypassed feature requirement")
	}
	if len(r.runs) > 0 {
		t.Fatal(r.runs)
	}
}

func TestGHInstallApprovalBindsHostRootAndConfiguration(t *testing.T) {
	for _, selector := range []string{"GH_HOST", "XDG_DATA_HOME", "GH_CONFIG_DIR"} {
		t.Run(selector, func(t *testing.T) {
			a, r := ghAppFixture(t)
			t.Setenv("GH_HOST", "github.com")
			t.Setenv("GH_CONFIG_DIR", t.TempDir())
			p, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-new", Operation: "install"})
			if err != nil || p.ProviderContext == "" {
				t.Fatal(p, err)
			}
			warning := strings.Join(p.Warnings, "\n")
			if !strings.Contains(warning, r.path) || !strings.Contains(warning, "github.com") || !strings.Contains(warning, filepath.Join(os.Getenv("XDG_DATA_HOME"), "gh", "extensions")) {
				t.Fatal("install scope is not reviewable", p)
			}
			value := t.TempDir()
			if selector == "GH_HOST" {
				value = "enterprise.example"
			}
			t.Setenv(selector, value)
			_, err = a.Execute(context.Background(), p, nil, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "changed") || len(r.runs) != 0 {
				t.Fatal("changed install scope was approved", err, r.runs)
			}
		})
	}
}

func TestGHManagerCacheChangesInstanceWithoutRefresh(t *testing.T) {
	a, r := ghAppFixture(t)
	ctx := context.Background()
	t.Setenv("GH_HOST", "github.com")
	first, err := a.Managers(ctx)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	again, err := a.Managers(ctx)
	if err != nil || again[0].Instance != first[0].Instance || r.managerReads != 1 {
		t.Fatal("same-context discovery was not cached", again, err, r.managerReads)
	}
	t.Setenv("GH_HOST", "enterprise.example")
	host, err := a.Managers(ctx)
	if err != nil || host[0].Instance == first[0].Instance || r.managerReads != 2 {
		t.Fatal("host drift borrowed manager cache", host, err, r.managerReads)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, err := a.Managers(ctx)
	if err != nil || root[0].Instance == host[0].Instance || r.managerReads != 3 {
		t.Fatal("root drift borrowed manager cache", root, err, r.managerReads)
	}
	s, err := a.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: []string{"gh-ext"}})
	if err != nil || len(s.Packages) != 0 || len(s.Coverage) != 1 || s.Coverage[0].Instance != root[0].Instance {
		t.Fatal("new root borrowed old inventory or instance", s, err)
	}
}

func TestGHInstallRejectsConfiguredDefaultHostDrift(t *testing.T) {
	a, r := ghAppFixture(t)
	t.Setenv("GH_HOST", "")
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	hosts := filepath.Join(dir, "hosts.yml")
	if err := os.WriteFile(hosts, []byte("enterprise.example:\n  oauth_token: private-fixture-token\n  user: example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-new", Operation: "install"})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(p.Warnings, "\n")
	if !strings.Contains(warnings, "host enterprise.example") || strings.Contains(warnings, "private-fixture-token") {
		t.Fatal("configured host was not safely presented", warnings)
	}
	if err := os.WriteFile(hosts, []byte("enterprise.example:\n  oauth_token: private-fixture-token\ngithub.com:\n  user: another\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Execute(context.Background(), p, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "changed") || len(r.runs) != 0 {
		t.Fatal("sole-host to multi-host drift reused approval", err, r.runs)
	}
}
