package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// The same native receipt fixture used by actions also exercises view queries.
// Search deliberately returns a short catalog ID, independent of the installed
// custom-tap record, so only native catalog metadata may establish its source.
type brewSearchRunner struct{ *brewActionRunner }

func (r *brewSearchRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	if c.Path == "fake-mpm" && strings.Contains(strings.Join(c.Args, " "), " search ") {
		return process.Result{Stdout: `{"brew":{"packages":[{"id":"dev-cli","name":"Developer CLI"}],"errors":[]}}`}, nil
	}
	return r.brewActionRunner.Output(ctx, c)
}

func (*brewSearchRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	return fmt.Errorf("query attempted a mutation")
}

func assertTapIdentity(t *testing.T, s domain.Snapshot, id string) {
	t.Helper()
	if len(s.Packages) != 1 || s.Packages[0].ID != id || s.Packages[0].Identity == nil || s.Packages[0].Identity.State != "verified" || s.Packages[0].Identity.CanonicalID != id {
		t.Fatal("unresolved or wrong source", s)
	}
	if len(s.Coverage) != 1 || s.Coverage[0].State != "complete" {
		t.Fatal("identity evidence was not complete", s)
	}
}

func TestBrewRawReadsCanonicalizeBeforeFiltering(t *testing.T) {
	for _, kind := range []string{"installed", "outdated"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := brewActionFixture(t)
			s, err := a.read(context.Background(), domain.PackageQuery{Kind: kind, Query: "owner/tap", Managers: []string{"brew"}, Refresh: true}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			assertTapIdentity(t, s, "owner/tap/dev-cli")
		})
	}
}

func TestBrewStreamPublishesCanonicalIdentityBeforeBaseAndCachesIt(t *testing.T) {
	a, _ := brewActionFixture(t)
	a.Config.CacheDir = t.TempDir()
	q := domain.PackageQuery{Kind: "installed", Query: "owner/tap", Managers: []string{"brew"}, CachePolicy: domain.CacheSession}
	var key string
	stages := map[string]bool{}
	for event := range a.StreamQuery(context.Background(), q) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		assertTapIdentity(t, event.Snapshot, "owner/tap/dev-cli")
		stages[event.Stage] = true
		if key == "" {
			key = event.Snapshot.Packages[0].Key()
		} else if key != event.Snapshot.Packages[0].Key() {
			t.Fatal("enrichment changed canonical selection key", key, event)
		}
	}
	if !stages["base"] || !stages["enriched"] || !stages["done"] {
		t.Fatal(stages)
	}
	record, ok := a.loadQueryDisk(a.queryContext(), q.Kind, "brew")
	if !ok {
		t.Fatal("normalized disk record missing")
	}
	assertTapIdentity(t, record.Snapshot, "owner/tap/dev-cli")
	for event := range a.StreamQuery(context.Background(), q) {
		if event.Stage != "done" && (event.Stage != "base" || !event.Cached) {
			t.Fatal("session restarted inventory", event)
		}
		assertTapIdentity(t, event.Snapshot, "owner/tap/dev-cli")
	}
}

func TestBrewCatalogCannotBorrowInstalledTapIdentity(t *testing.T) {
	a, native := brewActionFixture(t)
	a.Runner = &brewSearchRunner{native}
	if _, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"brew"}, CachePolicy: domain.CacheSession}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, id, state string }{
		{"dev", "dev-cli", "not_installed"},
		{"owner/tap/dev-cli", "owner/tap/dev-cli", "installed"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			s, err := a.Query(context.Background(), domain.PackageQuery{Kind: "search", Query: tc.query, Managers: []string{"brew"}, DeferInventory: true, CachePolicy: domain.CacheSession})
			if err != nil {
				t.Fatal(err)
			}
			assertTapIdentity(t, s, tc.id)
			if s.Packages[0].InstallState != tc.state {
				t.Fatal("catalog and installed source were conflated", s)
			}
		})
	}
}

func TestBrewUnresolvedIdentityStaysVisibleAndFailsCoverage(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		t.Run(fmt.Sprint(streamed), func(t *testing.T) {
			a, r := brewActionFixture(t)
			a.Config.CacheDir = t.TempDir()
			r.badMetadata = true
			q := domain.PackageQuery{Kind: "installed", Managers: []string{"brew"}}
			var s domain.Snapshot
			var err error
			if streamed {
				s, err = a.Query(context.Background(), q)
			} else {
				s, err = a.read(context.Background(), q, false, false)
			}
			if err != nil || len(s.Packages) != 1 || s.Packages[0].ID != "dev-cli" || s.Packages[0].Identity == nil || s.Packages[0].Identity.State != "unresolved" || len(s.Issues) == 0 || s.Issues[0].Kind != "identity" || s.Coverage[0].State != "failed" || freshInventory(s, "brew") {
				t.Fatal("identity failure disappeared or became complete", s, err)
			}
			if _, ok := a.loadQueryDisk(a.queryContext(), "installed", "brew"); ok {
				t.Fatal("unresolved identity was persisted as a complete cache")
			}
		})
	}
}

func TestBrewActionResolutionAlwaysRefreshesIdentityOutsideSession(t *testing.T) {
	a, r := brewActionFixture(t)
	ctx := context.Background()
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"brew"}, CachePolicy: domain.CacheSession}
	s, err := a.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	assertTapIdentity(t, s, "owner/tap/dev-cli")
	m := domain.Manager{ID: "brew", Path: r.path}
	req := domain.ActionRequest{Operation: "upgrade", Manager: "brew", Package: "dev-alias"}
	first, _, err := a.resolveBrewAction(ctx, req, m)
	if err != nil || first.Package != "owner/tap/dev-cli" {
		t.Fatal(first, err)
	}
	r.tap = "different/tap"
	if err = r.receipt(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.resolveBrewAction(ctx, first, m); err == nil {
		t.Fatal("old fully qualified source redirected to a different tap")
	}
	fresh, id, err := a.resolveBrewAction(ctx, req, m)
	if err != nil || fresh.Package != "different/tap/dev-cli" || id.Tap != "different/tap" {
		t.Fatal("action borrowed old session identity", fresh, id, err)
	}
	s, err = a.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	assertTapIdentity(t, s, "owner/tap/dev-cli")
}
