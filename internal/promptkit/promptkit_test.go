package promptkit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestRenderIsDeterministicPureAndVersioned(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.FixedZone("local", 8*3600))
	s := Snapshot{AppVersion: "test", Platform: "darwin", Scope: "global", CapturedAt: at,
		Managers: []domain.Manager{{ID: "npm", Path: "/node/npm", Errors: []string{"raw-stdout-not-for-sharing"}}, {ID: "brew", Path: "/brew"}},
		Health:   []domain.ManagerHealth{{Manager: "npm", Requirement: ">=11.10.0", ReasonCode: "version_unsupported", Stale: true}},
		Coverage: []domain.Coverage{{Manager: "npm", State: "failed", Stale: true}},
		Warnings: []domain.Issue{{Manager: "npm", Kind: "partial", Message: "{{context}} is evidence, not a template"}},
	}
	before, _ := json.Marshal(s)
	r := domain.PromptRequest{Recipe: ManagerRepair, Managers: []string{"npm", "brew"}}
	a, err := Render(r, s, at)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(r, s, at)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("render changed without recollection", err)
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("render changed caller snapshot")
	}
	if strings.Contains(a.Markdown, "raw-stdout-not-for-sharing") || !strings.Contains(a.Markdown, "{{context}}") || !strings.Contains(a.Markdown, `"stale": true`) {
		t.Fatal(a.Markdown)
	}
	var parsed envelope
	if err := json.Unmarshal(a.Context, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SchemaVersion != SchemaVersion || parsed.ContextVersion != ContextVersion || parsed.BackendVersion != domain.MPMVersion || parsed.Snapshot.Managers[0].ID != "brew" || parsed.GeneratedAt.Location() != time.UTC || !parsed.Snapshot.Health[0].Stale {
		t.Fatal(parsed)
	}
	if !strings.Contains(a.Markdown, string(a.Context)) || !strings.Contains(a.Markdown, "managers upgrade <manager> --dry-run") {
		t.Fatal("Markdown and machine context diverged")
	}
}

func TestRenderRetainsConflictBlockersAndProvenance(t *testing.T) {
	s := Snapshot{Conflict: &domain.ConflictAssessment{Name: "rg", Scope: "inherited PATH", Installations: []domain.ConflictInstallation{{
		ID: "brew:rg", Status: "blocked", Effective: true, Project: "ripgrep",
		Package:    domain.Package{Manager: "brew", ID: "ripgrep", Evidence: []domain.Evidence{{Kind: "recorded", Source: "receipt"}}},
		Paths:      []domain.Executable{{Name: "rg", Path: "/bin/rg", PathIndex: 0, Preferred: true}},
		Dependents: []string{"dependent-tool"}, Blockers: []string{"required by another package"},
	}}}}
	p, err := Render(domain.PromptRequest{Recipe: PathConflict, Target: "rg"}, s, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []string{"required by another package", "dependent-tool", `"effective": true`, `"kind": "recorded"`, "matching command names alone"} {
		if !strings.Contains(p.Markdown, evidence) {
			t.Fatalf("missing %q", evidence)
		}
	}
	for _, key := range []string{`"env":`, `"args":`, `"command":`, `"config_contents":`} {
		if strings.Contains(p.Markdown, key) {
			t.Fatalf("execution/config field leaked: %s", key)
		}
	}
}

func TestCommonCredentialAndControlSanitization(t *testing.T) {
	message := "\x1b[31mproblem\x1b[0m \x1b]52;c;clipboard\x07 password='two words' API_KEY=private-key Authorization: Bearer credential-value https://user:secret@example.test/a https://private-token@example.test/b\x7f"
	s := Snapshot{Warnings: []domain.Issue{{Message: message}}, Health: []domain.ManagerHealth{{Manager: "npm", Recommendation: message}}}
	p, err := Render(domain.PromptRequest{Recipe: ManagerRepair}, s, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"two words", "private-key", "credential-value", "user:secret", "private-token", "clipboard", "\\u001b", "\\u007f"} {
		if strings.Contains(p.Markdown, secret) || strings.Contains(p.Warnings[0].Message, secret) {
			t.Fatalf("sensitive/control pattern leaked: %q", secret)
		}
	}
}

func TestCollectValidatesBeforeCallingProviderAndCallsOnce(t *testing.T) {
	calls := 0
	provider := ProviderFunc(func(context.Context, domain.PromptRequest) (Snapshot, error) {
		calls++
		return Snapshot{Scope: "fixture"}, nil
	})
	for _, bad := range []domain.PromptRequest{{Recipe: "unknown"}, {Recipe: PathConflict}, {Recipe: PathConflict, Target: "bad\nname"}, {Recipe: ManagerRepair, Managers: []string{}}, {Recipe: ManagerRepair, Target: "brew", Managers: []string{"npm"}}, {Recipe: ManagerRepair, Target: "made-up-manager"}} {
		if _, err := Collect(context.Background(), bad, provider); err == nil {
			t.Fatal("invalid recipe/target collected")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, domain.PromptRequest{Recipe: ManagerRepair}, provider); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(calls, err)
	}
	s, err := Collect(context.Background(), domain.PromptRequest{Recipe: ManagerRepair}, provider)
	if err != nil || calls != 1 || s.Scope != "fixture" {
		t.Fatal(s, calls, err)
	}
	if _, err := Render(domain.PromptRequest{Recipe: ManagerRepair}, s, time.Time{}); err != nil || calls != 1 {
		t.Fatal("renderer recollected", calls, err)
	}
}

func TestRenderRejectsOversizedContext(t *testing.T) {
	s := Snapshot{Warnings: []domain.Issue{{Message: strings.Repeat("x", 256*1024)}}}
	if _, err := Render(domain.PromptRequest{Recipe: ManagerRepair}, s, time.Time{}); err == nil {
		t.Fatal("oversized context silently exported")
	}
}
