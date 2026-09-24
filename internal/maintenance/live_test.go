package maintenance

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

// Opt-in read-only contract check. It queries existing managers and public npm
// metadata, hashes applicable configuration, and writes only disposable cache.
// It never calls Execute, installs a tool, or changes mise configuration.
func TestLiveNPMHealth(t *testing.T) {
	path := os.Getenv("LAZYPKG_TEST_NPM_HEALTH")
	if path == "" {
		t.Skip("set LAZYPKG_TEST_NPM_HEALTH to an existing mise-managed npm for the read-only native contract check")
	}
	e := New(process.ExecRunner{}, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h, err := e.Check(ctx, []domain.Manager{{ID: "npm", Path: path, Requirement: ">=11.10.0"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 1 || h[0].Owner != "mise" || h[0].Strategy != "npm-mise" || h[0].CandidateVersion == "" || h[0].Stale {
		t.Fatalf("npm health did not resolve a verified repair: %#v", h)
	}
	t.Logf("selected npm %s; Node %s; candidate npm %s; status %s", h[0].Version, h[0].RuntimeVersion, h[0].CandidateVersion, h[0].UpdateStatus)
}
