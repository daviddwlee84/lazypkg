package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/config"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

// Opt in with a known mpm path. No package mutations; Rustup settings and query
// caches are isolated so listing cannot migrate the user's Rustup settings.
func TestLiveProgressiveQueries(t *testing.T) {
	mpm := os.Getenv("LAZYPKG_BENCH_MPM")
	if mpm == "" {
		t.Skip("set LAZYPKG_BENCH_MPM for native read-only profiling")
	}
	root := t.TempDir()
	rust := os.Getenv("RUSTUP_HOME")
	if rust == "" {
		home, _ := os.UserHomeDir()
		rust = filepath.Join(home, ".rustup")
	}
	privateRust := filepath.Join(root, "rustup")
	if err := os.Mkdir(privateRust, 0700); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(rust, "settings.toml")); err == nil {
		if err = os.WriteFile(filepath.Join(privateRust, "settings.toml"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(rust, "toolchains")); err == nil {
		if err = os.Symlink(filepath.Join(rust, "toolchains"), filepath.Join(privateRust, "toolchains")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RUSTUP_HOME", privateRust)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	c := config.Config{MPMPath: mpm, DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache", "lazypkg"), TimeoutSeconds: 30}
	for _, run := range []struct{ name, kind string }{{"cold-installed", "installed"}, {"disk-installed", "installed"}, {"cold-updates", "outdated"}, {"disk-updates", "outdated"}} {
		a := New(c)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		var first, live time.Duration
		var final domain.Snapshot
		for e := range a.StreamQuery(ctx, domain.PackageQuery{Kind: run.kind}) {
			if len(e.Snapshot.Packages) > 0 && first == 0 {
				first = time.Since(start)
			}
			if e.Stage == "base" && len(e.Snapshot.Packages) > 0 && live == 0 {
				live = time.Since(start)
			}
			if e.Stage == "done" {
				if e.Err != nil {
					t.Error(e.Err)
				}
				final = e.Snapshot
			}
		}
		cancel()
		t.Logf("%s first=%s first-live=%s complete=%s rows=%d issues=%d", run.name, first.Round(time.Millisecond), live.Round(time.Millisecond), time.Since(start).Round(time.Millisecond), len(final.Packages), len(final.Issues))
		if first == 0 {
			t.Error("no package rows observed")
		}
	}
}
