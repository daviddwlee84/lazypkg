package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type streamingRunner struct {
	mu             sync.Mutex
	block          chan struct{}
	started        chan struct{}
	once           sync.Once
	queries        int
	managerQueries int
	empty          bool
}

func (r *streamingRunner) Output(ctx context.Context, c domain.Command) (process.Result, error) {
	args := strings.Join(c.Args, " ")
	if args == "--version" {
		return process.Result{Stdout: "mpm, version 8.0.1"}, nil
	}
	if strings.Contains(args, "managers") {
		r.mu.Lock()
		r.managerQueries++
		r.mu.Unlock()
		return process.Result{Stdout: `{"go":{"id":"go","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"go","version":"1.27.0"},"gem":{"id":"gem","available":true,"supported":true,"fresh":true,"executable":true,"cli_path":"gem","version":"3.6.9"}}`}, nil
	}
	if strings.Contains(args, "installed") || strings.Contains(args, "outdated") {
		r.mu.Lock()
		r.queries++
		empty := r.empty
		r.mu.Unlock()
		manager := "go"
		if strings.Contains(args, "--gem") {
			manager = "gem"
		}
		if manager == "gem" && r.block != nil {
			if r.started != nil {
				r.once.Do(func() { close(r.started) })
			}
			select {
			case <-r.block:
			case <-ctx.Done():
				return process.Result{}, ctx.Err()
			}
		}
		rows := `[{"id":"example","installed_version":"1","latest_version":"2"}]`
		if empty {
			rows = "[]"
		}
		return process.Result{Stdout: fmt.Sprintf(`{%q:{"packages":%s,"errors":[]}}`, manager, rows)}, nil
	}
	return process.Result{}, fmt.Errorf("unexpected %s", args)
}
func (r *streamingRunner) Run(context.Context, domain.Command, io.Reader, io.Writer, io.Writer) error {
	return fmt.Errorf("read mutated")
}

func TestStreamPublishesFastProviderBeforeSlowProvider(t *testing.T) {
	r := &streamingRunner{block: make(chan struct{}), started: make(chan struct{})}
	a := testApp(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := a.StreamQuery(ctx, domain.PackageQuery{Kind: "installed", Managers: []string{"go", "gem"}})
	fast := false
	for e := range ch {
		if e.Manager == "go" && e.Stage == "base" {
			if len(e.Snapshot.Packages) != 1 {
				t.Fatal(e)
			}
			fast = true
			close(r.block)
		}
		if e.Stage == "done" && e.Err != nil {
			t.Fatal(e.Err)
		}
	}
	if !fast {
		t.Fatal("fast provider waited for blocked provider")
	}
}

func TestDiskSeedsStaleThenSuccessfulEmptyReplacesThem(t *testing.T) {
	cache := t.TempDir()
	first := testApp(t, &streamingRunner{})
	first.Config.CacheDir = cache
	q := domain.PackageQuery{Kind: "installed", Managers: []string{"gem"}}
	if _, err := first.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r := &streamingRunner{empty: true, block: make(chan struct{})}
	next := testApp(t, r)
	next.Config.CacheDir = cache
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := next.StreamQuery(ctx, q)
	seed := <-ch
	if seed.Stage != "cache" || len(seed.Snapshot.Packages) != 1 || !seed.Snapshot.Coverage[0].Stale {
		t.Fatal(seed)
	}
	close(r.block)
	var done domain.QueryEvent
	for e := range ch {
		if e.Stage == "done" {
			done = e
		}
	}
	if done.Err != nil || len(done.Snapshot.Packages) != 0 || done.Snapshot.Coverage[0].Stale {
		t.Fatal(done)
	}
	files, _ := filepath.Glob(filepath.Join(cache, "queries-v1", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	if info, err := os.Stat(files[0]); err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal(info, err)
	}
	if _, err := next.Query(context.Background(), domain.PackageQuery{Kind: "outdated", Managers: []string{"gem"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := next.Query(context.Background(), domain.PackageQuery{Kind: "outdated", Managers: []string{"gem"}}); err != nil {
		t.Fatal(err)
	}
	if r.queries != 2 {
		t.Fatal("Updates memory cache missed", r.queries)
	}
	next.invalidateInventory()
	files, _ = filepath.Glob(filepath.Join(cache, "queries-v1", "*.json"))
	if len(files) != 0 {
		t.Fatal("mutation retained disk seeds", files)
	}
}

func TestQueryCacheDisabled(t *testing.T) {
	a := testApp(t, &streamingRunner{})
	a.Config.CacheDir = t.TempDir()
	disabled := false
	a.Config.QueryCache = &disabled
	if _, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"go"}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(a.Config.CacheDir)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestDiskCacheRejectsExpiredFutureAndDifferentContext(t *testing.T) {
	a := testApp(t, &streamingRunner{})
	a.Config.CacheDir = t.TempDir()
	if _, err := a.Query(context.Background(), domain.PackageQuery{Kind: "installed", Managers: []string{"go"}}); err != nil {
		t.Fatal(err)
	}
	key := a.queryContext()
	original, ok := a.loadQueryDisk(key, "installed", "go")
	if !ok {
		t.Fatal("live complete batch wasn't saved")
	}
	path := a.queryDiskPath(key, "installed", "go")
	for _, at := range []time.Time{time.Now().Add(-25 * time.Hour), time.Now().Add(time.Hour)} {
		record := original
		record.Snapshot = domain.CloneSnapshot(original.Snapshot)
		record.Snapshot.Coverage[0].ObservedAt = at
		b, _ := json.Marshal(record)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, ok := a.loadQueryDisk(key, "installed", "go"); ok {
			t.Fatal("invalid age accepted", at)
		}
	}
	b, _ := json.Marshal(original)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBIN", filepath.Join(t.TempDir(), "other-bin"))
	nextKey := a.queryContext()
	if nextKey == key {
		t.Fatal("namespace change did not affect identity")
	}
	if _, ok := a.loadQueryDisk(nextKey, "installed", "go"); ok {
		t.Fatal("different provider context borrowed cache")
	}
}

func TestSharedWorkOneCancellationKeepsOtherSubscriber(t *testing.T) {
	var shared sharedWork[int]
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	fn := func(ctx context.Context) (int, error) {
		close(started)
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	go func() { _, err := shared.do(ctx, "key", fn); first <- err }()
	<-started
	second := make(chan int, 1)
	go func() {
		v, _ := shared.do(context.Background(), "key", func(context.Context) (int, error) { return -1, nil })
		second <- v
	}()
	deadline := time.Now().Add(time.Second)
	for {
		shared.mu.Lock()
		users := shared.jobs["key"].users
		shared.mu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second subscriber did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-first; err == nil {
		t.Fatal("cancel ignored")
	}
	close(release)
	if got := <-second; got != 42 {
		t.Fatal("other subscriber was cancelled", got)
	}
}
