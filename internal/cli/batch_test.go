package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestBatchCLIFreezesFilteredTargetsAndRequiresApproval(t *testing.T) {
	f := &fakeService{packageRows: []domain.Package{{Manager: "brew", ID: "rg", Version: "1", Latest: "2"}, {Manager: "npm", ID: "rg", Version: "3", Latest: "4"}, {Manager: "brew", ID: "jq", Version: "1", Latest: "2"}}}
	out, _, err := invoke(f, "upgrade-batch", "--from", "installed", "--filter", "npm", "--dry-run", "--json")
	if err != nil || !json.Valid([]byte(out)) || len(f.batchRequest.Targets) != 1 || f.batchRequest.Targets[0].Manager != "npm" || f.batchExecutions != 0 {
		t.Fatal(out, err, f)
	}
	if f.queries[0].Kind != "installed" || !f.queries[0].Refresh {
		t.Fatal(f.queries)
	}
	_, _, err = invoke(f, "upgrade-batch", "--filter", "jq", "--json")
	if ExitCode(err) != 2 || f.batchExecutions != 0 {
		t.Fatal(err, f)
	}
	out, stderr, err := invoke(f, "upgrade-batch", "--filter", "jq", "--json", "--yes")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(stderr, "native batch output") || f.batchExecutions != 1 || len(f.batchRequest.Targets) != 1 {
		t.Fatal(out, stderr, err, f)
	}
}
func TestBatchCLIExplicitTargetsRetainOrderAndManagerIdentity(t *testing.T) {
	f := &fakeService{packageRows: []domain.Package{{Manager: "gh-ext", ID: "owner/gh-one", Version: "v1"}, {Manager: "npm", ID: "@scope/name", Version: "1"}}}
	_, _, err := invoke(f, "upgrade-batch", "--target", "npm:@scope/name", "--target", "gh-ext:owner/gh-one", "--dry-run", "--json")
	if err != nil || !reflect.DeepEqual(f.queries[0].Managers, []string{"npm", "gh-ext"}) || len(f.batchRequest.Targets) != 2 || f.batchRequest.Targets[0].Manager != "npm" {
		t.Fatal(err, f)
	}
	for _, args := range [][]string{
		{"upgrade-batch", "--target", "npm:@scope/name", "--manager", "npm"},
		{"upgrade-batch", "--target", "npm:@scope/name", "--filter", "name"},
		{"upgrade-batch", "--target", "npm:@scope/name", "--from", "installed"},
		{"upgrade-batch", "--target", "brew:pkg:brew/all"},
		{"upgrade-batch", "--target", "bogus"},
		{"upgrade-batch", "--from", "search"},
	} {
		_, _, err = invoke(f, args...)
		if ExitCode(err) != 2 {
			t.Fatal(args, err)
		}
	}
	if f.batchExecutions != 0 {
		t.Fatal("invalid request executed")
	}
}
func TestBatchCLIEmptyFilterIsNoOp(t *testing.T) {
	f := &fakeService{packageRows: []domain.Package{}}
	out, _, err := invoke(f, "upgrade-batch", "--filter", "absent", "--json")
	if err != nil || !json.Valid([]byte(out)) || len(f.batchRequest.Targets) != 0 {
		t.Fatal(out, err, f)
	}
}

func TestBatchCLIUsesUniqueVerifiedTapAliases(t *testing.T) {
	f := &fakeService{packageRows: []domain.Package{{Manager: "brew", ID: "owner/tap/dev-cli", Version: "0.3.0", Identity: &domain.PackageIdentity{State: "verified", CanonicalID: "owner/tap/dev-cli", Aliases: []string{"dev-cli", "owner/tap/dev-cli"}}}}}
	_, _, err := invoke(f, "upgrade-batch", "--target", "brew:dev-cli", "--dry-run", "--json")
	if err != nil || len(f.batchRequest.Targets) != 1 || f.batchRequest.Targets[0].ID != "owner/tap/dev-cli" {
		t.Fatal(f.batchRequest, err)
	}
	f.packageRows = append(f.packageRows, domain.Package{Manager: "brew", ID: "other/tap/dev-cli", Version: "1", Identity: &domain.PackageIdentity{State: "verified", CanonicalID: "other/tap/dev-cli", Aliases: []string{"dev-cli", "other/tap/dev-cli"}}})
	_, _, err = invoke(f, "upgrade-batch", "--target", "brew:dev-cli", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "more than one installation source") || f.batchExecutions != 0 {
		t.Fatal(err, f.batchExecutions)
	}
}

func TestBatchHumanCurrentAndExcludedPreserveSelectedVersions(t *testing.T) {
	var output strings.Builder
	ShowBatchPlan(&output, domain.BatchUpgradePlan{Entries: []domain.BatchUpgradeEntry{
		{Package: domain.Package{Manager: "brew", ID: "tool", Version: "1"}, State: "current"},
		{Package: domain.Package{Manager: "brew", ID: "missing", Version: "3", Latest: "4"}, State: "excluded", Reason: "Identity is unavailable"},
	}})
	text := output.String()
	if !strings.Contains(text, "Installed: 1 · no available upgrade") || !strings.Contains(text, "Selected version: 3") || !strings.Contains(text, "Observed candidate: 4") || strings.Contains(text, "→") {
		t.Fatal(text)
	}
}

func TestBatchNativeInputNeverPromptsInJSONMode(t *testing.T) {
	input := strings.NewReader("native answer\n")
	for _, test := range []struct{ json, terminal, wantInput bool }{{false, true, true}, {true, true, false}, {false, false, false}, {true, false, false}} {
		got := batchNativeInput(test.json, test.terminal, input)
		if (got != nil) != test.wantInput {
			t.Fatalf("JSON=%t terminal=%t passed unexpected native stdin", test.json, test.terminal)
		}
		if test.wantInput && got != input {
			t.Fatal("interactive native input was replaced")
		}
	}
}
func TestBatchHumanOverviewIncludesObservedAndTargetVersions(t *testing.T) {
	var out strings.Builder
	ShowBatchPlan(&out, domain.BatchUpgradePlan{Request: domain.BatchUpgradeRequest{Source: "updates"}, Entries: []domain.BatchUpgradeEntry{
		{Package: domain.Package{Manager: "brew", ID: "jq", Version: "1"}, ObservedVersions: []string{"1"}, TargetVersion: "2", State: "planned"},
		{Package: domain.Package{Manager: "mise", ID: "node"}, ObservedVersions: []string{"20", "22"}, TargetVersion: "24", State: "planned"},
		{Package: domain.Package{Manager: "cargo", ID: "tool"}, State: "excluded", Reason: "unsupported"},
	}})
	text := out.String()
	for _, want := range []string{"Installed: 1 → candidate: 2", "Installed: 20, 22 → candidate: 24", "native manager chooses", "exact mise target pinned", "activation is a separate action", "not reported"} {
		if !strings.Contains(text, want) {
			t.Fatalf("overview lacks %q: %s", want, text)
		}
	}
}

type batchSnapshotService struct {
	*fakeService
	snapshot domain.Snapshot
}

func (s *batchSnapshotService) Query(_ context.Context, q domain.PackageQuery) (domain.Snapshot, error) {
	s.queries = append(s.queries, q)
	return s.snapshot, nil
}
func invokeBatchService(s domain.Service, args ...string) (string, string, error) {
	cmd := newRoot(s)
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errout.String(), err
}
func TestBatchCLIEmptyFailedSelectionReturnsEvidenceWithoutExecution(t *testing.T) {
	for _, dry := range []bool{false, true} {
		f := &batchSnapshotService{fakeService: &fakeService{}, snapshot: domain.Snapshot{Coverage: []domain.Coverage{{Manager: "brew", State: "failed", Message: "provider timeout"}}, Issues: []domain.Issue{{Manager: "brew", Message: "provider timeout"}}}}
		args := []string{"upgrade-batch", "--json"}
		if dry {
			args = append(args, "--dry-run")
		} else {
			args = append(args, "--yes")
		}
		out, stderr, err := invokeBatchService(f, args...)
		if ExitCode(err) != 1 || !json.Valid([]byte(out)) || f.batchExecutions != 0 {
			t.Fatalf("dry=%t output=%s err=%v executions=%d", dry, out, err, f.batchExecutions)
		}
		var output struct {
			SelectionCoverage []domain.Coverage `json:"selection_coverage"`
			SelectionIssues   []domain.Issue    `json:"selection_issues"`
		}
		if err := json.Unmarshal([]byte(out), &output); err != nil {
			t.Fatal(err)
		}
		if len(output.SelectionCoverage) != 1 || output.SelectionCoverage[0].State != "failed" || len(output.SelectionIssues) != 1 {
			t.Fatal("empty failure lost structured selection evidence", out)
		}
		if strings.Count(stderr, "provider timeout") != 1 {
			t.Fatal("query issue and coverage duplicated", stderr)
		}
	}
}
func TestBatchCLIEmptyFailedSelectionExplainsHumanDryAndNormalOutput(t *testing.T) {
	for _, dry := range []bool{false, true} {
		f := &batchSnapshotService{fakeService: &fakeService{}, snapshot: domain.Snapshot{Issues: []domain.Issue{{Manager: "brew", Message: "unmatched query failure"}}}}
		args := []string{"upgrade-batch"}
		if dry {
			args = append(args, "--dry-run")
		}
		out, _, err := invokeBatchService(f, args...)
		if ExitCode(err) != 1 || f.batchExecutions != 0 || !strings.Contains(out, "Selection collection is incomplete") || !strings.Contains(out, "No operations ran") {
			t.Fatal(out, err, f.batchExecutions)
		}
	}
}
func TestBatchCLIExpectedExclusionsRemainNoOpWithCoverage(t *testing.T) {
	f := &batchSnapshotService{fakeService: &fakeService{}, snapshot: domain.Snapshot{Coverage: []domain.Coverage{{Manager: "cargo", State: "unsupported", Message: "no outdated operation"}, {Manager: "pip", State: "excluded", Message: "environment scope"}, {Manager: "winget", State: "unavailable", Message: "not detected"}}}}
	out, stderr, err := invokeBatchService(f, "upgrade-batch", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, "selection_coverage") {
		t.Fatal(out, err)
	}
	for _, state := range []string{"unsupported", "excluded", "unavailable"} {
		if !strings.Contains(stderr, state) {
			t.Fatal("coverage exclusion was not explained", stderr)
		}
	}
}
func TestBatchCLIJSONPreservesEvidenceOutsideSealedPlanAndResult(t *testing.T) {
	for _, dry := range []bool{false, true} {
		f := &batchSnapshotService{fakeService: &fakeService{}, snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "jq", Version: "1"}}, Coverage: []domain.Coverage{{Manager: "brew", State: "complete"}, {Manager: "npm", State: "failed", Message: "offline"}}, Issues: []domain.Issue{{Manager: "npm", Message: "offline"}}}}
		args := []string{"upgrade-batch", "--json"}
		if dry {
			args = append(args, "--dry-run")
		} else {
			args = append(args, "--yes")
		}
		out, _, err := invokeBatchService(f, args...)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &fields); err != nil {
			t.Fatal(err)
		}
		if fields["entries"] == nil || fields["selection_coverage"] == nil || fields["selection_issues"] == nil {
			t.Fatal("wrapper removed existing fields or evidence", out)
		}
		if dry && string(fields["fingerprint"]) != "\"fake\"" {
			t.Fatal("CLI metadata changed the sealed plan", out)
		}
		if len(f.batchRequest.Targets) != 1 || f.batchRequest.Targets[0].ID != "jq" {
			t.Fatal("failed manager introduced a target")
		}
	}
}
func TestBatchCLICoveredEnrichmentWarningDoesNotInvalidateEmptyInventory(t *testing.T) {
	snapshot := domain.Snapshot{Coverage: []domain.Coverage{{Manager: "brew", State: "complete", Enrichment: "partial"}}, Issues: []domain.Issue{{Manager: "brew", Message: "optional executable metadata unavailable"}}}
	if batchSelectionIncomplete(snapshot) {
		t.Fatal("optional enrichment was treated as failed inventory collection")
	}
}
