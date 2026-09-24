package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type ghRunner struct {
	mu            sync.Mutex
	queries, runs []domain.Command
	output        func(domain.Command) (process.Result, error)
	run           func(domain.Command) error
}

func (r *ghRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if err := ctx.Err(); err != nil {
		return process.Result{}, err
	}
	r.mu.Lock()
	r.queries = append(r.queries, c)
	r.mu.Unlock()
	return r.output(c)
}
func (r *ghRunner) Run(ctx context.Context, c domain.Command, _ io.Reader, _, _ io.Writer) error {
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
func ghFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
}

type ghFixture struct {
	g           *GHExtensions
	r           *ghRunner
	root        string
	statuses    map[string]string
	old         bool
	gitStatus   string
	gitHead     string
	gitRemote   string
	gitUpstream string
}

func newGHFixture(t *testing.T) *ghFixture {
	t.Helper()
	dir := t.TempDir()
	f := &ghFixture{root: filepath.Join(dir, "data", "gh", "extensions"), statuses: map[string]string{}, gitHead: strings.Repeat("a", 40), gitRemote: "https://github.com/owner/gh-source.git", gitUpstream: "refs/remotes/origin/main"}
	f.r = &ghRunner{}
	f.g = &GHExtensions{Path: filepath.Join(dir, "bin", "gh"), GOOS: "linux", Home: dir, Env: map[string]string{"XDG_DATA_HOME": filepath.Join(dir, "data"), "GIT_SSH_COMMAND": "", "GIT_SSH": ""}, Runner: f.r}
	ghFile(t, f.g.Path, "fixture gh")
	f.r.output = func(c domain.Command) (process.Result, error) {
		if c.Path == "git" {
			switch strings.Join(c.Args[2:], " ") {
			case "config --get remote.origin.url":
				return process.Result{Stdout: f.gitRemote}, nil
			case "rev-parse --verify HEAD", "rev-parse --verify @{upstream}":
				return process.Result{Stdout: f.gitHead}, nil
			case "status --porcelain --untracked-files=normal":
				return process.Result{Stdout: f.gitStatus}, nil
			case "rev-parse --symbolic-full-name @{upstream}":
				return process.Result{Stdout: f.gitUpstream}, nil
			case "symbolic-ref -q refs/remotes/origin/HEAD":
				return process.Result{Stdout: "refs/remotes/origin/main"}, nil
			}
		}
		if reflect.DeepEqual(c.Args, []string{"extension", "upgrade", "--help"}) {
			if f.old {
				return process.Result{Stdout: "FLAGS:\n  --all  all\n"}, nil
			}
			return process.Result{Stdout: "FLAGS:\n  --dry-run   Only display upgrades\n"}, nil
		}
		if len(c.Args) == 4 && c.Args[0] == "extension" && c.Args[1] == "upgrade" && c.Args[3] == "--dry-run" {
			status, ok := f.statuses[c.Args[2]]
			if !ok {
				return process.Result{}, errors.New("unexpected extension query")
			}
			if status == "FAIL" {
				return process.Result{}, errors.New("token=must-not-escape")
			}
			name := strings.TrimPrefix(strings.Split(c.Args[2], "/")[1], "gh-")
			return process.Result{Stdout: "[  " + name + "]: " + status + "\n"}, nil
		}
		return process.Result{}, fmt.Errorf("unexpected query %s %v", c.Path, c.Args)
	}
	return f
}
func (f *ghFixture) binary(t *testing.T, name, owner, host, tag string, pin bool) {
	t.Helper()
	ghFile(t, filepath.Join(f.root, name, "manifest.yml"), fmt.Sprintf("owner: %s\nname: %s\nhost: %s\ntag: %s\nispinned: %v\n", owner, name, host, tag, pin))
	ghFile(t, filepath.Join(f.root, name, name), "fixture extension")
}
func (f *ghFixture) git(t *testing.T) {
	t.Helper()
	ghFile(t, filepath.Join(f.root, "gh-source", ".git", "config"), "[remote]\nfixture")
	ghFile(t, filepath.Join(f.root, "gh-source", "gh-source"), "fixture")
}

func TestGHInventoryIncludesLocalPinnedAndInvalidWithoutExecutingExtensions(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-dash", "owner", "github.com", "v1.0.0", false)
	f.binary(t, "gh-pin", "owner", "ghe.example", "v2", true)
	f.git(t)
	ghFile(t, filepath.Join(f.root, "gh-local"), filepath.Join(t.TempDir(), "gh-local"))
	ghFile(t, filepath.Join(f.root, "gh-broken", "manifest.yml"), "tag: [invalid")
	s, err := f.g.Installed(context.Background())
	if err != nil || len(s.Packages) != 5 || len(s.Issues) != 1 {
		t.Fatal(s, err)
	}
	by := map[string]domain.Package{}
	for _, p := range s.Packages {
		by[p.ID] = p
		if p.Extension == nil || p.Scope != "global" || p.Instance != f.g.Instance() {
			t.Fatal(p)
		}
	}
	if by["owner/gh-pin"].Extension.Status != "pinned" || by["local:gh-local"].Extension.Status != "local" || by["unknown:gh-broken"].Extension.Status != "unknown" || by["owner/gh-source"].Extension.FullVersion != f.gitHead {
		t.Fatal(by)
	}
	for _, c := range f.r.queries {
		if c.Path != "git" {
			t.Fatal("inventory called extension or network", c)
		}
	}
	if len(f.r.runs) != 0 {
		t.Fatal(f.r.runs)
	}
}
func TestGHOpaqueVersionsAndStrictPerItemOutput(t *testing.T) {
	x := domain.GHExtension{ID: "owner/gh-tool", Name: "tool", Version: "a1b2c3d4"}
	for _, tc := range []struct {
		out, status, latest string
		bad                 bool
	}{
		{"[ tool]: would have upgraded from a1b2c3d4 to 9999abcd", "available", "9999abcd", false},
		{"[tool]: already up to date", "current", "a1b2c3d4", false},
		{"[tool]: pinned extensions can not be upgraded", "pinned", "", false},
		{"[tool]: local extensions can not be upgraded", "local", "", false},
		{"[wrong]: already up to date", "", "", true},
		{"[tool]: would have upgraded from stale to next", "", "", true},
		{"[tool]: already up to date\n[tool]: already up to date", "", "", true},
		{"[tool]: an unfamiliar success message", "", "", true},
	} {
		got, err := ghParseCheck(x, tc.out)
		if (err != nil) != tc.bad || !tc.bad && (got.Status != tc.status || got.Latest != tc.latest) {
			t.Fatal(tc, got, err)
		}
	}
}
func TestGHOutdatedRetainsPartialRowsAndNeverUsesAll(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
	f.binary(t, "gh-offline", "owner", "github.com", "v1", false)
	f.binary(t, "gh-pin", "owner", "github.com", "v1", true)
	f.statuses["owner/gh-dash"] = "would have upgraded from v1 to release-2026"
	f.statuses["owner/gh-offline"] = "FAIL"
	s, err := f.g.Outdated(context.Background())
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Latest != "release-2026" || len(s.Issues) != 1 {
		t.Fatal(s, err)
	}
	if strings.Contains(s.Issues[0].Message, "must-not-escape") {
		t.Fatal("raw command error escaped")
	}
	for _, c := range f.r.queries {
		if strings.Contains(strings.Join(c.Args, " "), "--all") || strings.Contains(strings.Join(c.Args, " "), "owner/gh-pin") {
			t.Fatal(c)
		}
	}
}
func TestGHOldVersionKeepsInventoryWithoutUnsafeFallback(t *testing.T) {
	f := newGHFixture(t)
	f.old = true
	f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
	if s, e := f.g.Installed(context.Background()); e != nil || len(s.Packages) != 1 {
		t.Fatal(s, e)
	}
	s, err := f.g.Outdated(context.Background())
	if err != nil || len(s.Issues) != 1 || s.Issues[0].Kind != "unsupported" {
		t.Fatal(s, err)
	}
	if _, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "upgrade"}); err == nil {
		t.Fatal("old gh upgrade planned without check")
	}
	for _, c := range f.r.queries {
		if len(c.Args) != 3 || c.Args[2] != "--help" {
			t.Fatal("unsafe fallback", c)
		}
	}
}
func TestGHPlanRevalidatesOwnerHostPinBinaryAndRoot(t *testing.T) {
	for _, change := range []string{"owner", "host", "pin", "version", "binary", "root"} {
		t.Run(change, func(t *testing.T) {
			f := newGHFixture(t)
			f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
			f.statuses["owner/gh-dash"] = "would have upgraded from v1 to v2"
			p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "upgrade"})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "owner":
				f.binary(t, "gh-dash", "other", "github.com", "v1", false)
			case "host":
				f.binary(t, "gh-dash", "owner", "enterprise.example", "v1", false)
			case "pin":
				f.binary(t, "gh-dash", "owner", "github.com", "v1", true)
			case "version":
				f.binary(t, "gh-dash", "owner", "github.com", "v2", false)
			case "binary":
				ghFile(t, filepath.Join(f.root, "gh-dash", "gh-dash"), "changed binary identity")
			case "root":
				f.g.Env["XDG_DATA_HOME"] = t.TempDir()
			}
			if _, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard); err == nil || len(f.r.runs) != 0 {
				t.Fatal("stale plan ran", err, f.r.runs)
			}
		})
	}
}
func TestGHExecuteRebuildsCommandAndVerifiesUpdatedOrCurrent(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(fmt.Sprint(current), func(t *testing.T) {
			f := newGHFixture(t)
			f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
			f.statuses["owner/gh-dash"] = "would have upgraded from v1 to v2"
			if current {
				f.statuses["owner/gh-dash"] = "already up to date"
			}
			p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "upgrade"})
			if err != nil {
				t.Fatal(err)
			}
			p.Steps = []domain.Step{{Command: domain.Command{Path: "evil", Args: []string{"--all", "--force"}}}}
			f.r.run = func(c domain.Command) error {
				if c.Path != f.g.Path || !reflect.DeepEqual(c.Args, []string{"extension", "upgrade", "owner/gh-dash"}) {
					t.Fatal(c)
				}
				f.binary(t, "gh-dash", "owner", "github.com", "v2", false)
				f.statuses["owner/gh-dash"] = "already up to date"
				return nil
			}
			r, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard)
			if err != nil {
				t.Fatal(r, err)
			}
			want := "success"
			runs := 1
			if current {
				want = "current"
				runs = 0
			}
			if r.Steps[0].Status != want || len(f.r.runs) != runs {
				t.Fatal(r, f.r.runs)
			}
		})
	}
}
func TestGHVerificationCannotTreatFailedCheckAsCurrent(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
	f.statuses["owner/gh-dash"] = "would have upgraded from v1 to v2"
	p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	f.r.run = func(domain.Command) error {
		f.binary(t, "gh-dash", "owner", "github.com", "v2", false)
		f.statuses["owner/gh-dash"] = "FAIL"
		return nil
	}
	r, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err == nil || r.Steps[0].Status != "unverified" {
		t.Fatal(r, err)
	}
}
func TestGHPinnedLocalAndDirtyCheckoutsCannotBeChanged(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-pin", "owner", "github.com", "v1", true)
	ghFile(t, filepath.Join(f.root, "gh-local"), filepath.Join(t.TempDir(), "gh-local"))
	f.git(t)
	f.gitStatus = " M gh-source\n"
	for _, id := range []string{"owner/gh-pin", "local:gh-local", "owner/gh-source"} {
		for _, op := range []string{"upgrade", "remove"} {
			if id == "owner/gh-pin" && op == "remove" {
				continue
			}
			if _, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: id, Operation: op}); err == nil {
				t.Fatal(id, op)
			}
		}
	}
	if len(f.r.runs) != 0 {
		t.Fatal(f.r.runs)
	}
}

func TestGHExplicitPinnedRemovalPreservesPinBinding(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-pin", "owner", "github.com", "v1", true)
	p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-pin", Operation: "remove"})
	if err != nil || !p.GHExtension.Before.Pinned || !strings.Contains(p.Preview, "Pinned registration") {
		t.Fatal(p, err)
	}
	f.r.run = func(c domain.Command) error {
		if !reflect.DeepEqual(c.Args, []string{"extension", "remove", "owner/gh-pin"}) {
			t.Fatal(c)
		}
		return os.RemoveAll(filepath.Join(f.root, "gh-pin"))
	}
	r, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || r.Steps[0].Status != "success" {
		t.Fatal(r, err)
	}
	f.binary(t, "gh-pin", "owner", "github.com", "v1", true)
	p, err = f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-pin", Operation: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	f.binary(t, "gh-pin", "owner", "github.com", "v1", false)
	if _, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard); err == nil || len(f.r.runs) != 1 {
		t.Fatal("pin change bypassed binding", err)
	}
}

func TestGHPinnedGitRemovalStillProtectsLocalChanges(t *testing.T) {
	f := newGHFixture(t)
	f.git(t)
	ghFile(t, filepath.Join(f.root, "gh-source", ".pin-"+f.gitHead), "")
	f.gitStatus = "?? .pin-" + f.gitHead + "\n"
	request := domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-source", Operation: "remove"}
	if _, err := f.g.Plan(context.Background(), request); err != nil {
		t.Fatal("pin marker alone is not a local edit", err)
	}
	f.gitStatus += " M gh-source\n"
	if _, err := f.g.Plan(context.Background(), request); err == nil {
		t.Fatal("pinned checkout local changes were removable")
	}
}
func TestGHRemovalUsesBoundRepoAndVerifiesItsSlot(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
	p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	f.r.run = func(c domain.Command) error {
		if !reflect.DeepEqual(c.Args, []string{"extension", "remove", "owner/gh-dash"}) {
			t.Fatal(c)
		}
		return os.RemoveAll(filepath.Join(f.root, "gh-dash"))
	}
	r, err := f.g.Execute(context.Background(), p, nil, io.Discard, io.Discard)
	if err != nil || r.Steps[0].Status != "success" {
		t.Fatal(r, err)
	}
}
func TestGHQueryEnvironmentAndInstanceIsolation(t *testing.T) {
	f := newGHFixture(t)
	f.g.Env["GH_FORCE_TTY"] = "100"
	f.g.Env["GH_DEBUG"] = "api"
	f.g.Env["GIT_DIR"] = "/other"
	f.g.Env["MISE_AUTO_INSTALL"] = "1"
	c := f.g.command(f.g.Path, "extension", "list")
	if c.Env["GH_PROMPT_DISABLED"] != "1" || c.Env["GIT_TERMINAL_PROMPT"] != "0" || c.Env["GH_FORCE_TTY"] != "" || c.Env["GH_DEBUG"] != "" || c.Env["GIT_DIR"] != "" || c.Env["MISE_AUTO_INSTALL"] != "0" || c.Env["GIT_OPTIONAL_LOCKS"] != "0" {
		t.Fatal(c.Env)
	}
	if f.g.Env["GH_DEBUG"] != "api" {
		t.Fatal("caller env mutated")
	}
	before := f.g.Instance()
	f.g.Env["GH_HOST"] = "enterprise.example"
	if f.g.Instance() == before {
		t.Fatal("host not isolated")
	}
	before = f.g.Instance()
	f.g.Env["XDG_DATA_HOME"] = t.TempDir()
	if f.g.Instance() == before {
		t.Fatal("root not isolated")
	}
	f.g.GOOS = "windows"
	f.g.Env["XDG_DATA_HOME"] = ""
	f.g.Env["LOCALAPPDATA"] = t.TempDir()
	root, err := f.g.root()
	if err != nil || root != filepath.Join(f.g.Env["LOCALAPPDATA"], "GitHub CLI", "extensions") {
		t.Fatal(root, err)
	}
}
func TestGHRemoteCredentialsAreNotExported(t *testing.T) {
	f := newGHFixture(t)
	f.git(t)
	f.gitRemote = "https://user:super-secret@enterprise.example/owner/gh-source.git"
	s, err := f.g.Installed(context.Background())
	b, _ := json.Marshal(s)
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Extension.Host != "enterprise.example" || strings.Contains(string(b), "super-secret") {
		t.Fatal(string(b), err)
	}
}
func TestGHCancelledPlanNeverStartsMutation(t *testing.T) {
	f := newGHFixture(t)
	f.binary(t, "gh-dash", "owner", "github.com", "v1", false)
	f.statuses["owner/gh-dash"] = "would have upgraded from v1 to v2"
	p, err := f.g.Plan(context.Background(), domain.ActionRequest{Manager: "gh-ext", Package: "owner/gh-dash", Operation: "upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.g.Execute(ctx, p, nil, io.Discard, io.Discard); err == nil || len(f.r.runs) != 0 {
		t.Fatal(err)
	}
}

func TestGHCancelledEmptyInventoryDoesNotBecomeSuccessful(t *testing.T) {
	f := newGHFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.g.Installed(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := f.g.SupportsOutdated(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.r.queries) != 0 {
		t.Fatal("cancelled query started", f.r.queries)
	}
}

func TestGHInstallContextKeepsOnlyHostPolicyAndDoesNotLeakCredentials(t *testing.T) {
	f := newGHFixture(t)
	dir := t.TempDir()
	f.g.Env["GH_HOST"] = ""
	f.g.Env["GH_CONFIG_DIR"] = dir
	path := filepath.Join(dir, "hosts.yml")
	ghFile(t, path, "enterprise.example:\n  oauth_token: first-private-value\n  user: first-account\n")
	one, description, err := f.g.InstallContext()
	if err != nil || !strings.Contains(description, "host enterprise.example (sole configured host)") {
		t.Fatal(description, err)
	}
	ghFile(t, path, "enterprise.example:\n  oauth_token: second-private-value\n  user: second-account\n")
	two, again, err := f.g.InstallContext()
	if err != nil || one != two || description != again {
		t.Fatal("credential-only change affected host policy", err)
	}
	for _, s := range []string{one, two, description, again} {
		if strings.Contains(s, "private-value") || strings.Contains(s, "account") {
			t.Fatal("credential information exported")
		}
	}
	ghFile(t, path, "enterprise.example:\n  oauth_token: secret\ngithub.com:\n  user: account\n")
	multi, desc, err := f.g.InstallContext()
	if err != nil || multi == one || !strings.Contains(desc, "host github.com (default)") {
		t.Fatal(desc, err)
	}
	ghFile(t, path, "github.com:\n  oauth_token: replaced\nenterprise.example:\n  user: another\n")
	reordered, _, err := f.g.InstallContext()
	if err != nil || reordered != multi {
		t.Fatal("host order affected binding", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ghFile(t, filepath.Join(dir, "config.yml"), "hosts:\n  fallback.example:\n    oauth_token: hidden\nprompt: enabled\n")
	_, desc, err = f.g.InstallContext()
	if err != nil || !strings.Contains(desc, "host fallback.example") {
		t.Fatal("general config host fallback", desc, err)
	}
}
func TestLiveGHExtensionsReadOnly(t *testing.T) {
	path := os.Getenv("LAZYPKG_TEST_GH")
	if path == "" {
		t.Skip("set LAZYPKG_TEST_GH for native read-only inventory/dry-run checks")
	}
	g := &GHExtensions{Path: path, Runner: process.ExecRunner{}, Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := g.Installed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("registered extensions: %d; metadata issues: %d", len(s.Packages), len(s.Issues))
	u, err := g.Outdated(ctx)
	if err != nil || len(u.Issues) > 0 {
		t.Fatal(u, err)
	}
	t.Logf("verified available updates: %d", len(u.Packages))
}
