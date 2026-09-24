package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"io"
	"testing"
)

type fakeService struct {
	executed int
	planned  int
}

func (f *fakeService) Managers(context.Context) ([]domain.Manager, error) {
	return []domain.Manager{}, nil
}
func (f *fakeService) Packages(context.Context, string, string, string) (domain.Snapshot, error) {
	return domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "jq", Version: "1.8"}}, Issues: []domain.Issue{{Manager: "cargo", Message: "unavailable"}}}, nil
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
