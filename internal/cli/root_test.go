package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeService struct {
	executed       int
	planned        int
	queries        []domain.PackageQuery
	healthChecks   int
	maintenanceIDs []string
	saved          []string
}

func (f *fakeService) StreamQuery(ctx context.Context, q domain.PackageQuery) <-chan domain.QueryEvent {
	ch := make(chan domain.QueryEvent, 1)
	s, err := f.Query(ctx, q)
	ch <- domain.QueryEvent{Stage: "done", Snapshot: s, Err: err}
	close(ch)
	return ch
}
func (f *fakeService) AssessConflict(context.Context, string) (domain.ConflictAssessment, error) {
	return domain.ConflictAssessment{Name: "tool", Installations: []domain.ConflictInstallation{{ID: "keep", Package: domain.Package{Manager: "uvx", ID: "tool"}}}}, nil
}
func (f *fakeService) PlanResolution(_ context.Context, r domain.ResolutionRequest) (domain.ActionPlan, error) {
	f.planned++
	return domain.ActionPlan{Kind: "resolution", Title: "Remove one installation", Resolution: &domain.ResolutionPlan{Request: r}}, nil
}
func (f *fakeService) MaintenanceQueue(_ context.Context, ids []string, _ bool) (domain.MaintenanceQueue, error) {
	f.maintenanceIDs = append([]string(nil), ids...)
	return domain.MaintenanceQueue{Jobs: []domain.MaintenanceJob{{ID: "npm", Representative: "npm", ManagerIDs: []string{"npm"}, Category: "repair"}}}, nil
}
func (f *fakeService) RenderPrompt(context.Context, domain.PromptRequest) (domain.RenderedPrompt, error) {
	return domain.RenderedPrompt{Recipe: "path-conflict", Markdown: "# Review\n\nCaptured evidence.\n"}, nil
}

func (f *fakeService) Managers(context.Context) ([]domain.Manager, error) {
	return []domain.Manager{}, nil
}
func (f *fakeService) Packages(context.Context, string, string, string) (domain.Snapshot, error) {
	return domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "jq", Version: "1.8"}}, Issues: []domain.Issue{{Manager: "cargo", Message: "unavailable"}}}, nil
}
func (f *fakeService) Query(ctx context.Context, q domain.PackageQuery) (domain.Snapshot, error) {
	f.queries = append(f.queries, q)
	return f.Packages(ctx, q.Kind, q.Query, "")
}
func (f *fakeService) Preferences(context.Context) (domain.ManagerPreferences, error) {
	return domain.ManagerPreferences{Default: []string{"brew"}, Order: []string{"brew", "mise", "uvx"}, Sets: map[string][]string{"daily": {"mise", "brew"}}, Groups: map[string][]string{"python": {"uvx", "pipx"}}, Mouse: true}, nil
}
func (f *fakeService) SaveManagerSet(ctx context.Context, name string, ids []string, def bool) (domain.ManagerPreferences, error) {
	f.saved = append([]string(nil), ids...)
	p, _ := f.Preferences(ctx)
	p.Sets[name] = ids
	if def {
		p.DefaultSet = name
	}
	return p, nil
}
func (f *fakeService) CheckManagers(context.Context, []string, bool) ([]domain.ManagerHealth, error) {
	f.healthChecks++
	return []domain.ManagerHealth{{Manager: "npm", Version: "11.6.2", Requirement: ">=11.10.0", UpdateStatus: "available"}}, nil
}
func (f *fakeService) PlanManagerUpdate(_ context.Context, id string) (domain.ActionPlan, error) {
	f.planned++
	return domain.ActionPlan{Kind: "manager", Title: "Update " + id}, nil
}
func (f *fakeService) Diagnose(context.Context, string) (domain.DiagnosticReport, error) {
	return domain.DiagnosticReport{}, nil
}
func (f *fakeService) Plan(_ context.Context, r domain.ActionRequest) (domain.ActionPlan, error) {
	f.planned++
	return domain.ActionPlan{Kind: "package", Request: r, Title: "Install selected package"}, nil
}
func (f *fakeService) Execute(_ context.Context, _ domain.ActionPlan, _ io.Reader, out, errout io.Writer) (domain.ActionResult, error) {
	f.executed++
	out.Write([]byte("native output\n"))
	return domain.ActionResult{Message: "verified", Steps: []domain.StepResult{{ID: "package", Status: "success"}}}, nil
}
func (f *fakeService) SetupOptions(context.Context) ([]domain.SetupOption, error) {
	return []domain.SetupOption{{ID: "mpm", Recommended: true}}, nil
}
func (f *fakeService) PlanSetup(context.Context, []string) (domain.ActionPlan, error) {
	f.planned++
	return domain.ActionPlan{Kind: "setup", Title: "Setup"}, nil
}
func invoke(s *fakeService, args ...string) (string, string, error) {
	cmd := newRoot(s)
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errout.String(), err
}
func TestJSONDoesNotContainDiagnosticsOrProgress(t *testing.T) {
	s := &fakeService{}
	out, _, err := invoke(s, "list", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatal(out, err)
	}
	out, stderr, err := invoke(s, "install", "jq", "--manager", "brew", "--json", "--yes")
	if err != nil || !json.Valid([]byte(out)) || stderr == "" || s.executed != 1 {
		t.Fatal(out, stderr, err, s)
	}
}
func TestNonTTYDoesNotConfirmOrExecute(t *testing.T) {
	s := &fakeService{}
	_, _, err := invoke(s, "install", "jq", "--manager", "brew")
	if ExitCode(err) != 2 || s.executed != 0 {
		t.Fatal(err, s)
	}
	out, _, err := invoke(s, "install", "jq", "--manager", "brew", "--dry-run", "--json")
	if err != nil || !json.Valid([]byte(out)) || s.executed != 0 {
		t.Fatal(out, err, s)
	}
}
func TestHelpAndInvalidInputAvoidBackend(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"setup", "--help"}, {"completion", "bash"}} {
		s := &fakeService{}
		out, _, err := invoke(s, args...)
		if err != nil || out == "" || s.planned != 0 || s.executed != 0 {
			t.Fatal(args, out, err)
		}
	}
	for _, args := range [][]string{{"search"}, {"install", "jq"}, {"list", "--typo"}, {"setup", "--interactive", "--json"}, {"list", "--timeout", "0"}, {"setup", "--timeout", "-1"}, {"list", "--manager", "bogus"}} {
		s := &fakeService{}
		_, _, err := invoke(s, args...)
		if ExitCode(err) != 2 || s.executed != 0 {
			t.Fatal(args, err)
		}
	}
}
func TestSetupJSONListsWithoutInstallation(t *testing.T) {
	s := &fakeService{}
	out, _, err := invoke(s, "setup", "--json")
	if err != nil || !json.Valid([]byte(out)) || s.executed != 0 || s.planned != 0 {
		t.Fatal(out, err, s)
	}
}

func TestMultiManagerQueryAndMutationSelection(t *testing.T) {
	s := &fakeService{}
	_, _, err := invoke(s, "list", "--manager", "brew,mise", "--manager", "uvx")
	if err != nil || len(s.queries) != 1 || len(s.queries[0].Managers) != 3 {
		t.Fatal(s, err)
	}
	_, _, err = invoke(s, "list", "--set", "daily")
	if err != nil || s.queries[1].Set != "daily" {
		t.Fatal(s, err)
	}
	_, _, err = invoke(s, "list", "--group", "python")
	if err != nil || s.queries[2].Group != "python" {
		t.Fatal(s, err)
	}
	for _, args := range [][]string{{"list", "--manager", "brew", "--set", "daily"}, {"install", "jq", "--manager", "brew,mise"}, {"install", "jq", "--group", "python"}} {
		_, _, err = invoke(s, args...)
		if ExitCode(err) != 2 {
			t.Fatal(args, err)
		}
	}
}

func TestSessionScopeAppliesBeforePreferencesLoad(t *testing.T) {
	base := &fakeService{}
	s := &sessionService{Service: base, ids: []string{"mise", "brew"}}
	// This is the Init order when Query completes before async Preferences.
	if _, err := s.Query(context.Background(), domain.PackageQuery{Kind: "installed"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.queries[0].Managers, []string{"mise", "brew"}) {
		t.Fatal("startup queried the configured default", base.queries)
	}
	prefs, _ := s.Preferences(context.Background())
	if !reflect.DeepEqual(prefs.Default, base.queries[0].Managers) {
		t.Fatal("startup query and preference scope differ", prefs, base.queries)
	}
	for _, q := range []domain.PackageQuery{
		{Kind: "installed", Managers: []string{"uvx"}},
		{Kind: "installed", Managers: []string{}},
		{Kind: "installed", Group: "rust"},
		{Kind: "installed", Set: "daily"},
	} {
		_, _ = s.Query(context.Background(), q)
		if !reflect.DeepEqual(base.queries[len(base.queries)-1], q) {
			t.Fatal("session override replaced an explicit query", q)
		}
	}
}

func TestWorkflowCommandsKeepReadsSeparateFromExplicitSingleApproval(t *testing.T) {
	f := &fakeService{}
	for _, args := range [][]string{{"resolve", "tool", "--json"}, {"managers", "maintain", "--dry-run", "--json"}, {"prompt", "render", "path-conflict", "tool", "--json"}, {"prompt", "list", "--json"}} {
		out, _, err := invoke(f, args...)
		if err != nil || !json.Valid([]byte(out)) || f.executed != 0 || f.planned != 0 {
			t.Fatal(args, out, err, f)
		}
	}
	_, _, err := invoke(f, "resolve", "tool", "--keep", "keep", "--remove", "remove", "--dry-run", "--json")
	if err != nil || f.planned != 1 || f.executed != 0 {
		t.Fatal(err, f)
	}
	_, _, err = invoke(f, "resolve", "tool", "--keep", "keep", "--remove", "remove", "--json")
	if ExitCode(err) != 2 || f.executed != 0 {
		t.Fatal(err, f)
	}
	_, _, err = invoke(f, "resolve", "tool", "--keep", "keep", "--remove", "remove", "--yes", "--json")
	if err != nil || f.executed != 1 {
		t.Fatal(err, f)
	}
	for _, args := range [][]string{{"resolve", "tool", "--keep", "keep"}, {"resolve", "tool", "--yes"}, {"resolve", "tool", "--interactive"}, {"managers", "maintain", "--interactive"}, {"managers", "maintain", "--yes"}, {"prompt", "render", "path-conflict"}, {"prompt", "render", "missing", "tool"}} {
		_, _, err = invoke(f, args...)
		if ExitCode(err) != 2 || f.executed != 1 {
			t.Fatal(args, err, f)
		}
	}
}

func TestPromptExportMatchesPrintedPayloadAndProtectsExistingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "review.md")
	out, _, err := invoke(&fakeService{}, "prompt", "render", "path-conflict", "tool", "--output", file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != out {
		t.Fatal(out, string(data), err)
	}
	_, _, err = invoke(&fakeService{}, "prompt", "render", "path-conflict", "tool", "--output", file)
	if err == nil {
		t.Fatal("existing prompt overwritten")
	}
}

func TestQueryRefreshFlagAndSessionStreamScope(t *testing.T) {
	f := &fakeService{}
	_, _, err := invoke(f, "updates", "--manager", "gem", "--refresh", "--json")
	if err != nil || !f.queries[0].Refresh {
		t.Fatal(err, f.queries)
	}
	s := &sessionService{Service: f, ids: []string{"mise", "brew"}}
	for range s.StreamQuery(context.Background(), domain.PackageQuery{Kind: "installed"}) {
	}
	if !reflect.DeepEqual(f.queries[1].Managers, s.ids) {
		t.Fatal(f.queries)
	}
	if _, err = s.MaintenanceQueue(context.Background(), nil, true); err != nil || !reflect.DeepEqual(f.maintenanceIDs, s.ids) {
		t.Fatal("interactive maintenance lost CLI scope", f.maintenanceIDs, err)
	}
}
func TestManagerMaintenanceUsesExplicitApproval(t *testing.T) {
	s := &fakeService{}
	out, _, err := invoke(s, "managers", "check", "npm", "--json")
	if err != nil || !json.Valid([]byte(out)) || s.executed != 0 || s.healthChecks != 1 {
		t.Fatal(out, err, s)
	}
	_, _, err = invoke(s, "managers", "upgrade", "npm")
	if ExitCode(err) != 2 || s.executed != 0 {
		t.Fatal(err, s)
	}
	out, _, err = invoke(s, "managers", "upgrade", "npm", "--dry-run", "--json")
	if err != nil || !json.Valid([]byte(out)) || s.executed != 0 {
		t.Fatal(out, err, s)
	}
}
func TestSaveSetPreservesExplicitOrder(t *testing.T) {
	s := &fakeService{}
	out, _, err := invoke(s, "sets", "save", "tools", "--manager", "mise,brew,uv", "--default", "--json")
	if err != nil || !json.Valid([]byte(out)) || len(s.saved) != 3 || s.saved[0] != "mise" || s.saved[2] != "uvx" {
		t.Fatal(out, err, s.saved)
	}
}
