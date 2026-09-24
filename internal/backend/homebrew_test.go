package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type hbFixture struct {
	h              *Homebrew
	root, metadata string
	calls          []domain.Command
	catalog        func(domain.Command) (process.Result, error)
}

func hbFixtureFor(t *testing.T) *hbFixture {
	t.Helper()
	f := &hbFixture{root: t.TempDir()}
	f.h = &Homebrew{Path: "brew", Env: map[string]string{"HOMEBREW_NO_AUTO_UPDATE": "0", "HOMEBREW_NO_INSTALL_CLEANUP": "0", "MISE_AUTO_INSTALL": "1"}}
	f.h.Runner = fakeRunner{output: func(c domain.Command) (process.Result, error) {
		f.calls = append(f.calls, c)
		if c.Env["HOMEBREW_NO_AUTO_UPDATE"] != "1" || c.Env["HOMEBREW_NO_INSTALL_CLEANUP"] != "1" || c.Env["HOMEBREW_NO_AUTOREMOVE"] != "1" || c.Env["MISE_AUTO_INSTALL"] != "0" {
			t.Fatal("unsafe identity query", c.Env)
		}
		if len(c.Args) == 1 && (c.Args[0] == "--cellar" || c.Args[0] == "--caskroom") {
			return process.Result{Stdout: f.root}, nil
		}
		if len(c.Args) == 4 && c.Args[0] == "info" && c.Args[2] == "--installed" {
			return process.Result{Stdout: f.metadata}, nil
		}
		if f.catalog != nil {
			return f.catalog(c)
		}
		return process.Result{}, errors.New("unexpected metadata query")
	}}
	return f
}
func hbJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func hbFormulaJSON(name, tap string, aliases ...string) map[string]any {
	full := tap + "/" + name
	if tap == "homebrew/core" {
		full = name
	}
	return map[string]any{"name": name, "full_name": full, "tap": tap, "aliases": aliases, "oldnames": []string{}, "installed": []any{map[string]string{"version": "1.0"}}}
}
func hbFormulaReceipt(t *testing.T, root, name, tap string) {
	t.Helper()
	ghFile(t, filepath.Join(root, name, "1.0", "INSTALL_RECEIPT.json"), hbJSON(map[string]any{"source": map[string]string{"tap": tap}}))
}
func TestHomebrewInstalledAndOutdatedBecomeOneVerifiedTapIdentity(t *testing.T) {
	f := hbFixtureFor(t)
	f.metadata = hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("dev-cli", "daviddwlee84/tap")}})
	hbFormulaReceipt(t, f.root, "dev-cli", "daviddwlee84/tap")
	index, err := f.h.InstalledIndex(context.Background(), "brew")
	if err != nil {
		t.Fatal(err)
	}
	s := index.Normalize(domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "dev-cli", Version: "1.0"}, {Manager: "brew", ID: "daviddwlee84/tap/dev-cli", Version: "1.0", Latest: "1.1"}}})
	if len(s.Issues) != 0 || len(s.Packages) != 2 || !domain.SamePackageIdentity(s.Packages[0], s.Packages[1]) || s.Packages[0].ID != "daviddwlee84/tap/dev-cli" {
		t.Fatal(s)
	}
	if len(f.calls) != 2 {
		t.Fatal("identity uses per-row subprocesses", f.calls)
	}
	x, _ := index.Resolve("dev-cli")
	x.Aliases[0] = "modified"
	again, _ := index.Resolve("dev-cli")
	if reflect.DeepEqual(x.Aliases, again.Aliases) {
		t.Fatal("resolver shared alias slice")
	}
	if f.h.Env["HOMEBREW_NO_AUTO_UPDATE"] != "0" {
		t.Fatal("caller env was mutated")
	}
}
func TestHomebrewReceiptMismatchAndMissingVersionStayUnresolved(t *testing.T) {
	f := hbFixtureFor(t)
	f.metadata = hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("foo", "one/tap"), hbFormulaJSON("foo", "two/tap"), hbFormulaJSON("missing", "one/tap")}})
	hbFormulaReceipt(t, f.root, "foo", "one/tap")
	index, err := f.h.InstalledIndex(context.Background(), "brew")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, state string }{{"foo", "ambiguous"}, {"two/tap/foo", "ambiguous"}, {"missing", "unresolved"}, {"other/tap/foo", "unresolved"}} {
		x, e := index.Resolve(tc.id)
		if e == nil || x.State != tc.state {
			t.Fatal(tc, x, e)
		}
	}
	if x, e := index.Resolve("one/tap/foo"); e != nil || x.State != "verified" {
		t.Fatal(x, e)
	}
	s := index.Normalize(domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "one/tap/foo", Version: "0.9"}}})
	if len(s.Issues) != 1 || s.Issues[0].Kind != "identity" || s.Packages[0].Identity.State != "unresolved" {
		t.Fatal(s)
	}
}
func TestHomebrewAliasCollisionsNeverLastWin(t *testing.T) {
	f := hbFixtureFor(t)
	f.metadata = hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("a", "one/tap", "shared"), hbFormulaJSON("b", "two/tap", "shared")}})
	hbFormulaReceipt(t, f.root, "a", "one/tap")
	hbFormulaReceipt(t, f.root, "b", "two/tap")
	index, err := f.h.InstalledIndex(context.Background(), "brew")
	if err != nil {
		t.Fatal(err)
	}
	if x, e := index.Resolve("shared"); e == nil || x.State != "ambiguous" {
		t.Fatal(x, e)
	}
	for id, want := range map[string]string{"one/tap/shared": "one/tap/a", "two/tap/shared": "two/tap/b"} {
		if x, e := index.Resolve(id); e != nil || x.CanonicalID != want {
			t.Fatal(id, x, e)
		}
	}
}
func TestHomebrewCaskIdentityUsesItsReceiptAndVersion(t *testing.T) {
	f := hbFixtureFor(t)
	f.metadata = `{"formulae":[],"casks":[{"token":"app","full_token":"user/apps/app","tap":"user/apps","old_tokens":[],"installed":"1.0"}]}`
	path := filepath.Join(f.root, "app", ".metadata", "INSTALL_RECEIPT.json")
	ghFile(t, path, `{"source":{"tap":"user/apps","version":"1.0"}}`)
	index, err := f.h.InstalledIndex(context.Background(), "cask")
	if err != nil {
		t.Fatal(err)
	}
	s := index.Normalize(domain.Snapshot{Packages: []domain.Package{{Manager: "cask", ID: "app", Version: "1.0", Latest: "2.0"}}})
	if len(s.Issues) != 0 || s.Packages[0].ID != "user/apps/app" || s.Packages[0].Identity.Kind != "cask" {
		t.Fatal(s)
	}
	ghFile(t, path, `{"source":{"tap":"user/apps","version":"0.9"}}`)
	index, err = f.h.InstalledIndex(context.Background(), "cask")
	if err != nil {
		t.Fatal(err)
	}
	if x, e := index.Resolve("app"); e == nil || x.State != "unresolved" {
		t.Fatal(x, e)
	}
}
func TestHomebrewCatalogDoesNotBorrowInstalledTapAlias(t *testing.T) {
	f := hbFixtureFor(t)
	f.metadata = hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("foo", "custom/tap")}})
	hbFormulaReceipt(t, f.root, "foo", "custom/tap")
	installed, err := f.h.InstalledIndex(context.Background(), "brew")
	if err != nil {
		t.Fatal(err)
	}
	local := installed.Normalize(domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "foo", Version: "1.0"}}})
	f.catalog = func(c domain.Command) (process.Result, error) {
		if !reflect.DeepEqual(c.Args, []string{"info", "--json=v2", "--formula", "homebrew/core/foo"}) {
			t.Fatal(c.Args)
		}
		return process.Result{Stdout: hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("foo", "homebrew/core")}})}, nil
	}
	s, err := f.h.Catalog(context.Background(), "brew", "foo", domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "foo", Candidate: true}}})
	if err != nil || len(s.Issues) > 0 || s.Packages[0].Identity.CanonicalID != "foo" || domain.SamePackageIdentity(s.Packages[0], local.Packages[0]) {
		t.Fatal(s, local, err)
	}
}
func TestHomebrewCatalogRetainsQualifiedQueryHintAndVersionedIDs(t *testing.T) {
	f := hbFixtureFor(t)
	f.catalog = func(c domain.Command) (process.Result, error) {
		if c.Args[3] != "user/tap/old" {
			t.Fatal(c.Args)
		}
		return process.Result{Stdout: hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("new", "user/tap", "old")}})}, nil
	}
	s, err := f.h.Catalog(context.Background(), "brew", "user/tap/old", domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "new"}}})
	if err != nil || len(s.Issues) > 0 || s.Packages[0].ID != "user/tap/new" {
		t.Fatal(s, err)
	}
	f.catalog = func(c domain.Command) (process.Result, error) {
		if c.Args[3] != "homebrew/core/python@3.13" {
			t.Fatal(c.Args)
		}
		return process.Result{Stdout: hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("python@3.13", "homebrew/core")}})}, nil
	}
	x, err := f.h.ResolveCatalog(context.Background(), "brew", "python@3.13")
	if err != nil || x.CanonicalID != "python@3.13" {
		t.Fatal(x, err)
	}
}
func TestHomebrewCatalogChunksDeduplicateAndMapByIdentity(t *testing.T) {
	f := hbFixtureFor(t)
	var input domain.Snapshot
	for n := 0; n < 150; n++ {
		input.Packages = append(input.Packages, domain.Package{Manager: "brew", ID: fmt.Sprintf("tool%d", n)})
	}
	input.Packages = append(input.Packages, input.Packages[0])
	f.catalog = func(c domain.Command) (process.Result, error) {
		if len(c.Args) > homebrewIdentityChunk+3 {
			t.Fatal("unbounded metadata argv")
		}
		records := []any{}
		for i := len(c.Args) - 1; i >= 3; i-- {
			id := strings.TrimPrefix(c.Args[i], "homebrew/core/")
			records = append(records, hbFormulaJSON(id, "homebrew/core"))
		}
		return process.Result{Stdout: hbJSON(map[string]any{"formulae": records})}, nil
	}
	s, err := f.h.Catalog(context.Background(), "brew", "tool", input)
	if err != nil || len(s.Issues) > 0 || len(f.calls) != 3 || len(s.Packages) != 151 {
		t.Fatal(len(f.calls), s.Issues, err)
	}
	for i, p := range s.Packages {
		if p.ID != input.Packages[i].ID || p.Identity.State != "verified" {
			t.Fatal(p)
		}
	}
}
func TestHomebrewFailedCatalogBatchRemainsVisibleAndUnknown(t *testing.T) {
	f := hbFixtureFor(t)
	f.catalog = func(domain.Command) (process.Result, error) { return process.Result{}, errors.New("failed") }
	s, err := f.h.Catalog(context.Background(), "brew", "foo", domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "foo"}, {Manager: "npm", ID: "foo"}}})
	if err != nil || len(s.Packages) != 2 || s.Packages[0].Identity.State != "unresolved" || len(s.Issues) != 1 || s.Issues[0].Kind != "identity" || s.Packages[1].Identity != nil {
		t.Fatal(s, err)
	}
}
func TestHomebrewCLIBareCatalogFallbackOnlyAfterKnownMissingCore(t *testing.T) {
	f := hbFixtureFor(t)
	f.catalog = func(c domain.Command) (process.Result, error) {
		if c.Args[3] == "homebrew/core/foo" {
			return process.Result{Stderr: "Error: No available formula with the name homebrew/core/foo"}, errors.New("exit1")
		}
		if c.Args[3] != "foo" {
			t.Fatal(c.Args)
		}
		return process.Result{Stdout: hbJSON(map[string]any{"formulae": []any{hbFormulaJSON("foo", "custom/tap")}})}, nil
	}
	x, err := f.h.ResolveCatalog(context.Background(), "brew", "foo")
	if err != nil || x.CanonicalID != "custom/tap/foo" || len(f.calls) != 2 {
		t.Fatal(x, err, f.calls)
	}
	f.calls = nil
	f.catalog = func(domain.Command) (process.Result, error) {
		return process.Result{Stderr: "network unavailable"}, errors.New("timeout")
	}
	if _, err := f.h.ResolveCatalog(context.Background(), "brew", "foo"); err == nil || len(f.calls) != 1 {
		t.Fatal("uncertain core lookup guessed another tap", err, f.calls)
	}
}
func TestHomebrewCancelledIdentityDoesNotQuery(t *testing.T) {
	f := hbFixtureFor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.h.InstalledIndex(ctx, "brew"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.calls) > 0 {
		t.Fatal(f.calls)
	}
}
func TestLiveHomebrewIdentityReadOnly(t *testing.T) {
	path := os.Getenv("LAZYPKG_TEST_BREW")
	if path == "" {
		t.Skip("set LAZYPKG_TEST_BREW for native read-only identity checks")
	}
	h := &Homebrew{Path: path, Runner: process.ExecRunner{}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	index, err := h.InstalledIndex(ctx, "brew")
	if err != nil {
		t.Fatal(err)
	}
	x, err := index.Resolve("dev-cli")
	if err != nil {
		t.Fatal(x, err)
	}
	t.Logf("dev-cli identity: %s (%s)", x.CanonicalID, x.State)
	for _, id := range []string{"homebrew/core/jq", "daviddwlee84/tap/dev-cli"} {
		x, err := h.ResolveCatalog(ctx, "brew", id)
		if err != nil {
			t.Fatal(id, x, err)
		}
		t.Logf("catalog %s -> %s", id, x.CanonicalID)
	}
	if path := os.Getenv("LAZYPKG_TEST_MPM"); path != "" {
		m := &MPM{Path: path, Runner: process.ExecRunner{}}
		for _, manager := range []string{"brew", "cask"} {
			index, e := h.InstalledIndex(ctx, manager)
			if e != nil {
				t.Fatal(manager, e)
			}
			raw, e := m.Packages(ctx, "installed", "", manager)
			if e != nil {
				t.Fatal(manager, e)
			}
			normalized := index.Normalize(raw)
			reasons := map[string]int{}
			verified := 0
			for _, p := range normalized.Packages {
				if p.Identity.State == "verified" {
					verified++
				} else {
					reasons[p.Identity.State+": "+p.Identity.Reason]++
				}
			}
			t.Logf("%s raw=%d verified=%d unresolved reasons=%v", manager, len(raw.Packages), verified, reasons)
		}
	}
}
