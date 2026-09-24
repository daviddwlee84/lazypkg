package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestSessionCachePreservesObservationTimePastTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &streamingRunner{}
		a := testApp(t, r)
		q := domain.PackageQuery{Kind: "outdated", Managers: []string{"gem"}, CachePolicy: domain.CacheSession}
		first, err := a.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		next, err := a.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if r.queries != 1 || r.managerQueries != 1 || !next.ObservedAt.Equal(first.ObservedAt) || !next.Coverage[0].ObservedAt.Equal(first.Coverage[0].ObservedAt) || !next.Packages[0].InventoryAt.Equal(first.Packages[0].InventoryAt) {
			t.Fatal("session refetched or fabricated a new observation", first, next, r.queries, r.managerQueries)
		}
		q.CachePolicy = ""
		if _, err = a.Query(context.Background(), q); err != nil || r.queries != 2 {
			t.Fatal("default cache lost its TTL", err, r.queries)
		}
		q.CachePolicy = domain.CacheSession
		q.Refresh = true
		if _, err = a.Query(context.Background(), q); err != nil || r.queries != 3 {
			t.Fatal("explicit refresh reused a session result", err, r.queries)
		}
	})
}

func TestSessionCacheRetainsFailedRefreshState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &streamingRunner{}
		a := testApp(t, r)
		q := domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}, CachePolicy: domain.CacheSession}
		original, err := a.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		r.fail = true
		q.Refresh = true
		failed, err := a.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(failed.Packages) != 1 || failed.Coverage[0].State != "failed" || !failed.Coverage[0].Stale || !failed.Packages[0].InventoryStale {
			t.Fatal(failed)
		}
		time.Sleep(2 * time.Minute)
		q.Refresh = false
		next, err := a.Query(context.Background(), q)
		if err != nil || r.queries != 2 {
			t.Fatal(next, err, r.queries)
		}
		if next.Coverage[0].State != "failed" || !next.Coverage[0].Stale || !next.Packages[0].InventoryStale || len(next.Issues) == 0 || !next.ObservedAt.Equal(failed.ObservedAt) || !next.Coverage[0].ObservedAt.Equal(original.Coverage[0].ObservedAt) {
			t.Fatal("session navigation cleared stale/failure state", next)
		}
		r.fail = false
		q.CachePolicy = ""
		fresh, err := a.Query(context.Background(), q)
		if err != nil || r.queries != 3 || fresh.Coverage[0].State != "complete" || fresh.Coverage[0].Stale {
			t.Fatal("default client did not retry failed observation", fresh, err, r.queries)
		}
	})
}

func TestSessionCachesEmptyAndInvalidatesOnMutationOrContextChange(t *testing.T) {
	r := &streamingRunner{empty: true}
	a := testApp(t, r)
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}, CachePolicy: domain.CacheSession}
	for range 2 {
		s, err := a.Query(context.Background(), q)
		if err != nil || len(s.Packages) != 0 {
			t.Fatal(s, err)
		}
	}
	if r.queries != 1 {
		t.Fatal("empty result was not reusable", r.queries)
	}
	a.invalidateInventory()
	if _, err := a.Query(context.Background(), q); err != nil || r.queries != 2 {
		t.Fatal(err, r.queries)
	}
	t.Setenv("GOBIN", filepath.Join(t.TempDir(), "changed-context"))
	if _, err := a.Query(context.Background(), q); err != nil || r.queries != 3 {
		t.Fatal(err, r.queries)
	}
}

func TestSessionRemembersFailureBeforeInventoryCanRun(t *testing.T) {
	r := &streamingRunner{}
	a := testApp(t, r)
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}, CachePolicy: domain.CacheSession}
	original, err := a.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	r.failManagers = true
	q.Refresh = true
	failed, err := a.Query(context.Background(), q)
	if err == nil || len(failed.Packages) != 1 || failed.Coverage[0].State != "failed" || !failed.Coverage[0].Stale || !failed.Coverage[0].ObservedAt.Equal(original.Coverage[0].ObservedAt) {
		t.Fatal(failed, err)
	}
	q.Refresh = false
	next, err := a.Query(context.Background(), q)
	if err != nil || next.Coverage[0].State != "failed" || !next.Packages[0].InventoryStale || len(next.Issues) == 0 || !next.ObservedAt.Equal(failed.ObservedAt) || r.queries != 1 || r.managerQueries != 2 {
		t.Fatal("navigation cleared failed detection", next, err, r.queries, r.managerQueries)
	}
	r.failManagers = false
	q.CachePolicy = ""
	retried, err := a.Query(context.Background(), q)
	if err != nil || retried.Coverage[0].State != "complete" || retried.Coverage[0].Stale || r.queries != 2 {
		t.Fatal("default cache policy did not retry", retried, err, r.queries)
	}
}

func TestSessionMemoryPrecedesOlderDiskAndIsDetached(t *testing.T) {
	r := &streamingRunner{}
	a := testApp(t, r)
	a.Config.CacheDir = t.TempDir()
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}, CachePolicy: domain.CacheSession}
	original, err := a.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := a.loadQueryDisk(a.queryContext(), q.Kind, "gem")
	if !ok {
		t.Fatal("disk fixture missing")
	}
	record.Snapshot.Packages[0].Version = "older-disk"
	record.Snapshot.Coverage[0].ObservedAt = time.Now().Add(-time.Hour)
	data, _ := json.Marshal(record)
	if err = os.WriteFile(a.queryDiskPath(a.queryContext(), q.Kind, "gem"), data, 0600); err != nil {
		t.Fatal(err)
	}
	original.Packages[0].Version = "caller-mutated"
	count := 0
	for event := range a.StreamQuery(context.Background(), q) {
		count++
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Stage != "done" && (event.Stage != "base" || !event.Cached) {
			t.Fatal("session observation emitted as provisional seed", event)
		}
		if len(event.Snapshot.Packages) != 1 || event.Snapshot.Packages[0].Version != "1" {
			t.Fatal("disk/caller rolled back memory", event)
		}
	}
	if count != 2 || r.queries != 1 {
		t.Fatal("expected one memory event and done", count, r.queries)
	}
}

func TestDiskV2RejectsLegacySchemaAndUnverifiedHomebrewIdentity(t *testing.T) {
	a := testApp(t, &streamingRunner{})
	a.Config.CacheDir = t.TempDir()
	key := a.queryContext()
	if _, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}}); err != nil {
		t.Fatal(err)
	}
	record, ok := a.loadQueryDisk(key, "installed", "gem")
	if !ok {
		t.Fatal("missing fixture")
	}
	record.Schema = 1
	data, _ := json.Marshal(record)
	path := a.queryDiskPath(key, "installed", "gem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadQueryDisk(key, "installed", "gem"); ok {
		t.Fatal("legacy identity schema accepted")
	}
	legacy := filepath.Join(a.Config.CacheDir, "queries-v1", filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadQueryDisk(key, "installed", "gem"); ok {
		t.Fatal("legacy directory was used")
	}
	record.Schema = 2
	record.Manager.ID = "brew"
	record.Snapshot.Coverage[0].Manager = "brew"
	record.Snapshot.Packages[0].Manager = "brew"
	record.Snapshot.Packages[0].Identity = nil
	data, _ = json.Marshal(record)
	if err := os.WriteFile(a.queryDiskPath(key, "installed", "brew"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadQueryDisk(key, "installed", "brew"); ok {
		t.Fatal("unverified Homebrew cache record accepted")
	}
}
