package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type workflowService struct {
	promptRequests []domain.PromptRequest
	queueScopes    [][]string
	*fakeService
	assessment                   domain.ConflictAssessment
	queue                        domain.MaintenanceQueue
	resolutionRequests           []domain.ResolutionRequest
	updates                      []string
	refreshes                    []bool
	assessmentCalls, promptCalls int
	markdown                     string
}

func (f *workflowService) AssessConflict(context.Context, string) (domain.ConflictAssessment, error) {
	f.assessmentCalls++
	return f.assessment, nil
}
func (f *workflowService) PlanResolution(_ context.Context, q domain.ResolutionRequest) (domain.ActionPlan, error) {
	f.resolutionRequests = append(f.resolutionRequests, q)
	plan := domain.ActionPlan{Title: "Remove one installation", Request: domain.ActionRequest{Operation: "remove", Manager: "uv", Package: "alpha"}, Resolution: &domain.ResolutionPlan{Request: q}}
	for _, item := range f.assessment.Installations {
		if item.ID == q.KeepID {
			plan.Resolution.Keep = item
		}
		if item.ID == q.RemoveID {
			plan.Resolution.Remove = item
		}
	}
	return plan, nil
}
func (f *workflowService) MaintenanceQueue(_ context.Context, ids []string, refresh bool) (domain.MaintenanceQueue, error) {
	f.queueScopes = append(f.queueScopes, append([]string(nil), ids...))
	f.refreshes = append(f.refreshes, refresh)
	return f.queue, nil
}
func (f *workflowService) PlanManagerUpdate(_ context.Context, id string) (domain.ActionPlan, error) {
	f.updates = append(f.updates, id)
	return domain.ActionPlan{Title: "Update " + id, ManagerUpdate: &domain.ManagerHealth{Manager: id, ApplySupported: true}}, nil
}
func (f *workflowService) RenderPrompt(_ context.Context, request domain.PromptRequest) (domain.RenderedPrompt, error) {
	f.promptRequests = append(f.promptRequests, request)
	f.promptCalls++
	return domain.RenderedPrompt{Markdown: f.markdown}, nil
}
func workflowFixture(t *testing.T) (*Model, *workflowService) {
	m, f := readyModel(t)
	wf := &workflowService{fakeService: f, markdown: "# Reviewed fixture\n\nLiteral $` commands stay data.\n"}
	wf.assessment = domain.ConflictAssessment{Name: "alpha", Installations: []domain.ConflictInstallation{
		{ID: "brew-one", Package: domain.Package{Manager: "brew", ID: "alpha", Version: "1"}, Status: "ready", Project: "same", Effective: true, Paths: []domain.Executable{{Path: "/brew/bin/alpha"}}},
		{ID: "uv-two", Package: domain.Package{Manager: "uv", ID: "alpha", Version: "2"}, Status: "ready", Project: "same", Prefix: "/uv/tools/alpha", Commands: []string{"alpha", "alpha-helper"}, Paths: []domain.Executable{{Path: "/uv/bin/alpha"}}},
		{ID: "unknown", Package: domain.Package{ID: "alpha"}, Status: "unknown", Blockers: []string{"unproven owner"}},
	}}
	wf.queue = domain.MaintenanceQueue{Jobs: []domain.MaintenanceJob{
		{ID: "owner-brew", Category: "update", Title: "Homebrew", Representative: "brew", ApplySupported: true},
		{ID: "owner-apt", Category: "refresh", Title: "APT indexes", Representative: "apt", ApplySupported: true},
		{ID: "manual", Category: "guidance", Title: "Unknown runtime", Representative: "unknown", Reason: "Owner unresolved"},
	}}
	m.service = wf
	m.states[installedView].snapshot.Packages[0].Commands = []string{"alpha"}
	return m, wf
}
func TestResolutionKeepsExplicitIdentityAndReviewsOneRemoval(t *testing.T) {
	m, f := workflowFixture(t)
	deliver(m, press(m, "R"))
	if m.modal != resolutionModal || f.assessmentCalls != 1 {
		t.Fatal("resolution did not assess asynchronously")
	}
	press(m, "enter") // Keep only; Enter does not remove anything.
	if m.workflow.keepID != "brew-one" || len(f.resolutionRequests) != 0 {
		t.Fatal("first Enter did not only select retained instance")
	}
	press(m, "j")
	deliver(m, press(m, "enter"))
	if m.modal != planModal || len(f.resolutionRequests) != 1 || f.resolutionRequests[0] != (domain.ResolutionRequest{Name: "alpha", KeepID: "brew-one", RemoveID: "uv-two"}) {
		t.Fatal("not an explicit one-target resolution plan")
	}
	if text := m.planText(); !strings.Contains(text, "KEEP: brew") || !strings.Contains(text, "alpha-helper") {
		t.Fatal("review hides retained target or affected commands")
	}
	if press(m, "enter") != nil || f.executeCalls != 0 {
		t.Fatal("Enter executed a resolution")
	}
	press(m, "esc")
	press(m, "j")
	if press(m, "enter") != nil || len(f.resolutionRequests) != 1 {
		t.Fatal("unknown owner reached removal plan")
	}
	press(m, "k")
	deliver(m, press(m, "enter"))
	generation := m.planGeneration
	_, cmd := m.Update(executedMsg{generation: generation, result: domain.ActionResult{Message: "removed"}})
	deliver(m, cmd)
	if m.modal != resolutionModal || f.assessmentCalls != 2 || m.workflow.keepID != "brew-one" || f.executeCalls != 0 {
		t.Fatal("resolution did not re-assess and preserve retained identity")
	}
}
func TestMaintenanceReviewSkipStopAndRecheckEachJob(t *testing.T) {
	m, f := workflowFixture(t)
	deliver(m, press(m, "U"))
	if m.modal != maintenanceModal || len(f.updates) != 0 {
		t.Fatal("queue auto-planned or failed to open")
	}
	deliver(m, press(m, "enter"))
	if len(f.updates) != 1 || f.updates[0] != "brew" {
		t.Fatal("wrong representative")
	}
	press(m, "s")
	if m.modal != maintenanceModal || m.workflow.attempted["owner-brew"] != "skipped" || m.workflow.position != 1 {
		t.Fatal("skip did not advance stable job")
	}
	deliver(m, press(m, "enter"))
	if len(f.updates) != 2 || f.updates[1] != "apt" {
		t.Fatal("refresh job did not use per-manager plan")
	}
	generation := m.planGeneration
	_, cmd := m.Update(executedMsg{generation: generation, result: domain.ActionResult{Message: "refreshed"}})
	deliver(m, cmd)
	if m.modal != maintenanceModal || len(f.refreshes) != 2 || !f.refreshes[1] || m.workflow.attempted["owner-apt"] != "completed" {
		t.Fatal("execution did not freshly recheck queue")
	}
	if f.executeCalls != 0 || len(f.updates) != 2 {
		t.Fatal("queue ran or planned next item automatically")
	}
	press(m, "G")
	if press(m, "enter") != nil || len(f.updates) != 2 {
		t.Fatal("guidance produced mutation plan")
	}
	press(m, "g")
	deliver(m, press(m, "enter"))
	press(m, "q")
	if m.modal != noModal {
		t.Fatal("stop from review did not end queue")
	}
}
func TestPromptExportUsesPreviewBytesAndPreservesExistingFile(t *testing.T) {
	m, f := workflowFixture(t)
	deliver(m, press(m, "p"))
	if m.modal != promptModal || !strings.Contains(ansi.Strip(m.View().Content), "Reviewed fixture") {
		t.Fatal("missing preview")
	}
	press(m, "e")
	path := filepath.Join(t.TempDir(), "prompt.md")
	m.input.SetValue(path)
	press(m, "U")
	if len(m.input.Value()) != len(path)+1 || m.modal != exportPromptModal {
		t.Fatal("path typing invoked workflow shortcut")
	}
	m.input.SetValue(path)
	f.markdown = "changed after preview"
	deliver(m, press(m, "enter"))
	data, err := os.ReadFile(path)
	if err != nil || string(data) != m.workflow.prompt.Markdown || f.promptCalls != 1 {
		t.Fatalf("export recollected or changed preview: %q %v", data, err)
	}
	press(m, "e")
	m.input.SetValue(path)
	deliver(m, press(m, "enter"))
	if m.modal != exportPromptModal || !strings.Contains(m.status, "failed") {
		t.Fatal("overwrite was not refused")
	}
	press(m, "esc")
	if m.modal != promptModal {
		t.Fatal("export cancel lost preview")
	}
	press(m, "esc")
	if m.modal != noModal {
		t.Fatal("preview back did not return")
	}
}
func TestWorkflowMouseUsesIdentityAndNeverClickThrough(t *testing.T) {
	m, _ := workflowFixture(t)
	deliver(m, press(m, "R"))
	target := hitTarget{}
	for _, hit := range m.layout().targets {
		if hit.kind == "workflow" && hit.value == "uv-two" {
			target = hit
		}
	}
	if target.value == "" {
		t.Fatal("visible workflow row has no shared hit target")
	}
	m.Update(tea.MouseClickMsg{X: target.rect.x, Y: target.rect.y, Button: tea.MouseLeft})
	m.Update(tea.WindowSizeMsg{Width: 81, Height: 24})
	m.Update(tea.MouseReleaseMsg{X: target.rect.x, Y: target.rect.y, Button: tea.MouseLeft})
	if m.workflow.selectedID != "brew-one" {
		t.Fatal("resize allowed stale click")
	}
	m.activateHit(target)
	if m.workflow.selectedID != "uv-two" || m.workflow.keepID != "" || m.modal != resolutionModal {
		t.Fatal("row click changed more than selection")
	}
}
func TestInitialWorkflowsDoNotProbeUntilCommandsRun(t *testing.T) {
	for _, initial := range []string{"maintenance", "resolve:alpha"} {
		m, f := workflowFixture(t)
		n := New(context.Background(), f, initial)
		cmd := n.Init()
		if f.assessmentCalls != 0 || len(f.refreshes) > 0 {
			t.Fatal("constructor/Init blocked")
		}
		deliver(n, cmd)
		if initial == "maintenance" && n.modal != maintenanceModal || initial == "resolve:alpha" && n.modal != resolutionModal {
			t.Fatal("initial workflow absent")
		}
		n.cancelAll()
		m.cancelAll()
	}
}
func TestWorkflowRenderFitsNarrowAndStandardTerminals(t *testing.T) {
	m, _ := workflowFixture(t)
	for _, size := range [][2]int{{80, 24}, {48, 16}, {120, 36}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, modal := range []modalKind{resolutionModal, maintenanceModal, promptModal, exportPromptModal} {
			m.modal = modal
			m.workflow.loading = false
			content := ansi.Strip(m.View().Content)
			lines := strings.Split(content, "\n")
			if len(lines) > size[1] {
				t.Fatal("workflow exceeds terminal height")
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("workflow exceeds terminal width")
				}
			}
		}
	}
}

func TestMaintenanceDefaultCatalogAndTargetedPromptScope(t *testing.T) {
	m, f := workflowFixture(t)
	m.managerIDs = []string{"brew", "mise"}
	deliver(m, m.openMaintenance(true))
	if len(f.queueScopes[0]) != 0 || !f.refreshes[0] {
		t.Fatal("default maintenance missed detected managers or ignored force")
	}
	deliver(m, press(m, "p"))
	request := f.promptRequests[0]
	if request.Recipe != "manager-repair" || request.Target != "brew" || len(request.Managers) > 0 {
		t.Fatal("targeted prompt also specified manager subset")
	}
	press(m, "esc")
	press(m, "esc")
	m.scopeChanged = true
	deliver(m, m.openMaintenance())
	if strings.Join(f.queueScopes[1], ",") != "brew,mise" {
		t.Fatal("explicit maintenance subset was lost")
	}
}

func TestNonActionableWorkflowRowsDoNotAdvertiseReview(t *testing.T) {
	m, _ := workflowFixture(t)
	deliver(m, press(m, "R"))
	press(m, "K")
	press(m, "G")
	for _, button := range m.modalButtons() {
		if button.key == "enter" {
			t.Fatal("unowned installation advertises a removal review")
		}
	}
	press(m, "esc")
	deliver(m, press(m, "U"))
	press(m, "G")
	for _, button := range m.modalButtons() {
		if button.key == "enter" {
			t.Fatal("guidance job advertises executable review")
		}
	}
}
