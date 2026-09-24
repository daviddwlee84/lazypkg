package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type fakeService struct {
	mu                                       sync.Mutex
	managers                                 []domain.Manager
	snapshot                                 domain.Snapshot
	options                                  []domain.SetupOption
	managerCalls, packageCalls, executeCalls int
	packageArgs                              []string
	requests                                 []domain.ActionRequest
	setupIDs                                 []string
	result                                   domain.ActionResult
	executeErr                               error
	packageContext                           context.Context
	diagnosticName                           string
}

func (f *fakeService) Managers(context.Context) ([]domain.Manager, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.managerCalls++
	return f.managers, nil
}
func (f *fakeService) Packages(ctx context.Context, kind, query, manager string) (domain.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packageCalls++
	f.packageArgs = []string{kind, query, manager}
	f.packageContext = ctx
	return f.snapshot, nil
}
func (f *fakeService) Diagnose(_ context.Context, name string) (domain.DiagnosticReport, error) {
	f.diagnosticName = name
	return domain.DiagnosticReport{Scope: "inherited PATH"}, nil
}
func (f *fakeService) Plan(_ context.Context, request domain.ActionRequest) (domain.ActionPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	return domain.ActionPlan{Title: "Review " + request.Package, Request: request, Steps: []domain.Step{{ID: "change", Description: "Change package", Command: domain.Command{Path: "manager", Args: []string{request.Operation, request.Package}}}}}, nil
}
func (f *fakeService) Execute(_ context.Context, _ domain.ActionPlan, _ io.Reader, out io.Writer, _ io.Writer) (domain.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executeCalls++
	fmt.Fprintln(out, "native operation output")
	return f.result, f.executeErr
}
func (f *fakeService) SetupOptions(context.Context) ([]domain.SetupOption, error) {
	return f.options, nil
}
func (f *fakeService) PlanSetup(_ context.Context, ids []string) (domain.ActionPlan, error) {
	f.setupIDs = append([]string(nil), ids...)
	return domain.ActionPlan{Title: "Set up tools", SetupIDs: ids}, nil
}

func readyModel(t *testing.T) (*Model, *fakeService) {
	t.Helper()
	f := &fakeService{managers: []domain.Manager{{ID: "brew", Name: "Homebrew", Available: true, Supported: true, Status: "available", Version: "6.0.0", Capabilities: []string{"installed", "search", "install", "upgrade", "remove"}}, {ID: "mise", Name: "mise", Available: true, Supported: true, Status: "available", Capabilities: []string{"installed", "search", "install", "upgrade", "remove", "activate"}}}}
	m := New(context.Background(), f, "installed")
	m.managers = f.managers
	m.managersLoaded = true
	m.states[installedView] = viewState{loaded: true, snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "alpha", Version: "1", Scope: "user"}, {Manager: "brew", ID: "bravo", Version: "2", Scope: "user"}, {Manager: "mise", ID: "node", Version: "22.1.0", Latest: "22.2.0", Scope: "runtime"}}, ObservedAt: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}}
	m.reconcile(installedView, false)
	t.Cleanup(m.cancelAll)
	return m, f
}

func key(value string) tea.KeyPressMsg {
	codes := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown}
	if code, ok := codes[value]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	if value == "ctrl+c" {
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	if value == "shift+tab" {
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	}
	return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
}

func press(m *Model, value string) tea.Cmd { _, cmd := m.Update(key(value)); return cmd }
func deliver(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	message := cmd()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			deliver(m, child)
		}
		return
	}
	_, next := m.Update(message)
	if next != nil {
		deliver(m, next)
	}
}

func TestConstructionAndInitDoNotProbe(t *testing.T) {
	f := &fakeService{}
	m := New(context.Background(), f, "")
	defer m.cancelAll()
	cmd := m.Init()
	if f.managerCalls != 0 || f.packageCalls != 0 {
		t.Fatal("construction or Init blocked on service calls")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "loading") {
		t.Fatal("first frame does not report pending work")
	}
	if cmd == nil {
		t.Fatal("Init should schedule discovery")
	}
	press(m, "l")
	if m.view != discoverView {
		t.Fatal("navigation unavailable while reads are pending")
	}
}

func TestRefreshRejectsStaleSuccessAndFailureAndPreservesIdentity(t *testing.T) {
	m, _ := readyModel(t)
	press(m, "j")
	selected := m.states[installedView].selected
	oldCommand := m.loadView(installedView)
	oldGeneration := m.states[installedView].generation
	newCommand := m.loadView(installedView)
	generation := m.states[installedView].generation
	_ = newCommand
	packages := m.states[installedView].snapshot.Packages
	m.Update(packagesMsg{view: installedView, generation: generation, snapshot: domain.Snapshot{Packages: []domain.Package{packages[1], packages[0]}}})
	if m.states[installedView].selected != selected || m.states[installedView].position != 0 {
		t.Fatal("refresh lost selected package identity")
	}
	m.Update(packagesMsg{view: installedView, generation: oldGeneration, err: errors.New("late failure")})
	if m.states[installedView].err != nil {
		t.Fatal("stale failure replaced current status")
	}
	m.Update(packagesMsg{view: installedView, generation: oldGeneration, snapshot: domain.Snapshot{}})
	if len(m.states[installedView].snapshot.Packages) != 2 {
		t.Fatal("stale success replaced current data")
	}
	// Even a command that ignores its cancelled context cannot overwrite the newer request.
	deliver(m, oldCommand)
	if len(m.states[installedView].snapshot.Packages) != 2 {
		t.Fatal("cancelled command overwrote newer data")
	}
}

func TestFailedRefreshRetainsUsefulRowsAndDisablesMutation(t *testing.T) {
	m, _ := readyModel(t)
	m.loadView(installedView)
	m.Update(packagesMsg{view: installedView, generation: m.states[installedView].generation, err: errors.New("provider offline")})
	if len(m.rows(installedView)) != 3 || !m.states[installedView].stale {
		t.Fatal("failed refresh discarded or mislabeled previous data")
	}
	if cmd := press(m, "x"); cmd != nil {
		t.Fatal("stale data enabled a mutation")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "failed") {
		t.Fatal("failure is not visible")
	}
}

func TestPartialRefreshRetainsOnlyFailedProviderAndLabelsItStale(t *testing.T) {
	m, _ := readyModel(t)
	fresh := domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "alpha", Version: "3", Scope: "user"}}, Issues: []domain.Issue{{Manager: "mise", Message: "timed out"}}}
	m.loadView(installedView)
	m.Update(packagesMsg{view: installedView, generation: m.states[installedView].generation, snapshot: fresh})
	if len(m.rows(installedView)) != 2 || !m.states[installedView].retainedManagers["mise"] {
		t.Fatal("missing failed provider was treated as an empty successful result")
	}
	if len(m.actions()) == 0 {
		t.Fatal("a fresh provider was unnecessarily disabled")
	}
	press(m, "G")
	if press(m, "x") != nil {
		t.Fatal("a retained stale provider permitted removal")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "stale") {
		t.Fatal("retained rows are not visibly stale")
	}
	press(m, "enter")
	if !strings.Contains(ansi.Strip(m.View().Content), "STALE") {
		t.Fatal("detail omitted stale provenance")
	}
	press(m, "esc")
	m.loadView(installedView)
	m.Update(packagesMsg{view: installedView, generation: m.states[installedView].generation, snapshot: domain.Snapshot{Packages: fresh.Packages}})
	if len(m.rows(installedView)) != 1 || len(m.states[installedView].retainedManagers) != 0 {
		t.Fatal("recovered empty provider kept obsolete rows")
	}
	// Search responses are query-specific and must never merge older packages.
	m.states[discoverView] = viewState{loaded: true, query: "new", snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "mise", ID: "old-query"}}}}
	m.loadView(discoverView)
	m.Update(packagesMsg{view: discoverView, generation: m.states[discoverView].generation, query: "new", snapshot: domain.Snapshot{Issues: fresh.Issues}})
	if len(m.states[discoverView].snapshot.Packages) != 0 {
		t.Fatal("search retained rows from an earlier query")
	}
}

func TestDiagnoseShortcutUsesRecordedCommandAcrossProviders(t *testing.T) {
	m, f := readyModel(t)
	m.states[installedView].snapshot.Packages[0].Commands = []string{"rg"}
	m.managerFilter = "brew"
	deliver(m, press(m, "d"))
	if m.view != diagnosticsView || f.diagnosticName != "rg" || m.managerFilter != "" {
		t.Fatal("diagnose shortcut lost command or hid other providers")
	}
	r := row{executable: &domain.Executable{Name: "rg", Path: "/registered/rg", PathIndex: -1}}
	if detail := m.rowDetails(r); strings.Contains(detail, "PATH entry: -1") || !strings.Contains(detail, "outside PATH") {
		t.Fatal("registered executable was presented as a PATH entry")
	}
}

func TestTextEntryOwnsNavigationAndActionKeys(t *testing.T) {
	m, f := readyModel(t)
	press(m, "/")
	for _, r := range "jkhqliuxa?/" {
		press(m, string(r))
	}
	m.Update(tea.PasteMsg{Content: "node\ny\n"})
	if m.view != installedView || m.quitting || m.modal != noModal || len(f.requests) != 0 || f.executeCalls != 0 {
		t.Fatal("typing activated a shortcut")
	}
	if !m.filtering || !strings.Contains(m.input.Value(), "jkhqliuxa?/") {
		t.Fatalf("input lost text: %q", m.input.Value())
	}
	press(m, "ctrl+c")
	if m.quitting || m.filtering {
		t.Fatal("Ctrl+C from input should cancel input, not exit")
	}
}

func TestFilterEnterOnlyAcceptsAndHiddenSelectionCannotAct(t *testing.T) {
	m, f := readyModel(t)
	press(m, "/")
	press(m, "missing")
	press(m, "enter")
	if m.modal != noModal || m.filtering {
		t.Fatal("Enter should only accept a filter")
	}
	if len(m.rows(installedView)) != 0 || m.states[installedView].selected != "" {
		t.Fatal("filter retained a hidden selection")
	}
	if cmd := press(m, "x"); cmd != nil || len(f.requests) != 0 {
		t.Fatal("hidden package could be removed")
	}
	press(m, "esc")
	press(m, "tab")
	if cmd := press(m, "x"); cmd != nil {
		t.Fatal("manager-pane focus activated an item action")
	}
}

func TestDiscoverSubmitsCorrectServiceArguments(t *testing.T) {
	m, f := readyModel(t)
	press(m, "2")
	m.managerFilter = "mise"
	press(m, "/")
	press(m, "node")
	cmd := press(m, "enter")
	if f.packageCalls != 0 {
		t.Fatal("search ran synchronously")
	}
	deliver(m, cmd)
	if strings.Join(f.packageArgs, "|") != "search|node|mise" {
		t.Fatalf("wrong service query arguments: %#v", f.packageArgs)
	}
	if m.modal != noModal {
		t.Fatal("search submission unexpectedly opened details")
	}
}

func TestChangingManagerFromAnotherViewInvalidatesSearch(t *testing.T) {
	m, _ := readyModel(t)
	m.states[discoverView].query = "node"
	m.states[discoverView].loaded = true
	m.loadView(discoverView)
	old := m.states[discoverView].generation
	m.managerCursor = 2
	m.applyManagerFilter()
	if !m.states[discoverView].stale || m.states[discoverView].generation == old {
		t.Fatal("search request did not lose ownership when filter changed")
	}
	m.Update(packagesMsg{view: discoverView, generation: old, snapshot: domain.Snapshot{Packages: []domain.Package{{ID: "wrong-manager"}}}})
	if len(m.states[discoverView].snapshot.Packages) != 0 {
		t.Fatal("old provider results leaked into new selection")
	}
}

func TestPlanNeedsExplicitApprovalAndEscRejectsLateReply(t *testing.T) {
	m, f := readyModel(t)
	cmd := press(m, "x")
	if m.modal != planModal || !m.planLoading || f.executeCalls != 0 {
		t.Fatal("action bypassed plan preparation")
	}
	if press(m, "y") != nil {
		t.Fatal("loading plan accepted approval")
	}
	deliver(m, cmd)
	if press(m, "enter") != nil || f.executeCalls != 0 {
		t.Fatal("Enter accidentally confirmed a destructive plan")
	}
	if cmd := press(m, "y"); cmd == nil || !m.executing {
		t.Fatal("explicit approval did not schedule terminal handoff")
	}
	if press(m, "y") != nil {
		t.Fatal("repeated approval scheduled another execution")
	}
	if f.executeCalls != 0 {
		t.Fatal("Execute ran before terminal handoff")
	}
	m.executing = false
	old := m.planGeneration
	press(m, "esc")
	m.Update(planMsg{generation: old, plan: domain.ActionPlan{Title: "late"}})
	if m.modal != noModal {
		t.Fatal("cancelled plan reopened after late completion")
	}
}

func TestTinyReviewCannotApprove(t *testing.T) {
	m, _ := readyModel(t)
	m.modal = planModal
	m.Update(tea.WindowSizeMsg{Width: 35, Height: 9})
	if press(m, "y") != nil || m.executing {
		t.Fatal("a clipped review was approved")
	}
}

func TestCancelReadRejectsLateMessage(t *testing.T) {
	m, _ := readyModel(t)
	m.loadView(installedView)
	generation := m.states[installedView].generation
	press(m, "esc")
	m.Update(packagesMsg{view: installedView, generation: generation, snapshot: domain.Snapshot{}})
	if len(m.states[installedView].snapshot.Packages) != 3 || m.states[installedView].loading {
		t.Fatal("cancelled read changed inventory")
	}
}

func TestMiseRequestsUseExactVersionsAndSeparateActivation(t *testing.T) {
	for _, test := range []struct{ key, operation, version string }{{"x", "remove", "22.1.0"}, {"a", "activate", "22.1.0"}, {"u", "upgrade", "22.2.0"}} {
		t.Run(test.operation, func(t *testing.T) {
			m, f := readyModel(t)
			press(m, "G")
			deliver(m, press(m, test.key))
			if len(f.requests) != 1 {
				t.Fatal("no request")
			}
			got := f.requests[0]
			if got.Operation != test.operation || got.Version != test.version {
				t.Fatalf("wrong request: %#v", got)
			}
		})
	}
	m, f := readyModel(t)
	m.view = discoverView
	m.states[discoverView] = viewState{loaded: true, query: "node", acceptedQuery: "node", snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "mise", ID: "node"}}}}
	m.reconcile(discoverView, false)
	press(m, "i")
	if m.modal != versionModal || len(f.requests) != 0 {
		t.Fatal("mise installation skipped explicit version entry")
	}
	m.input.SetValue("22.2.0")
	deliver(m, press(m, "enter"))
	if len(f.requests) != 1 || f.requests[0].Version != "22.2.0" || f.requests[0].Operation != "install" {
		t.Fatal("version was not carried into the plan")
	}
}

func TestMiseUpdateAlreadyInstalledOffersLatestActivation(t *testing.T) {
	for _, test := range []struct {
		name            string
		view            viewID
		latestInstalled bool
		wantUpgrade     bool
		wantVersion     string
	}{
		{"pending update activation", updatesView, true, false, "22.2.0"},
		{"ordinary update", updatesView, false, true, "22.1.0"},
		{"installed row keeps its version", installedView, true, true, "22.1.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, f := readyModel(t)
			p := domain.Package{Manager: "mise", ID: "node", Version: "22.1.0", Latest: "22.2.0", LatestInstalled: test.latestInstalled, Scope: "runtime"}
			m.view = test.view
			m.states[test.view] = viewState{loaded: true, snapshot: domain.Snapshot{Packages: []domain.Package{p}}}
			m.reconcile(test.view, true)
			hasUpgrade := false
			activationLabel := ""
			for _, action := range m.actions() {
				if action.operation == "upgrade" {
					hasUpgrade = true
				}
				if action.operation == "activate" {
					activationLabel = action.label
				}
			}
			if hasUpgrade != test.wantUpgrade {
				t.Fatalf("upgrade availability: got %t, want %t", hasUpgrade, test.wantUpgrade)
			}
			if !test.wantUpgrade {
				if press(m, "u") != nil || len(f.requests) != 0 {
					t.Fatal("pending activation scheduled another upgrade")
				}
				if activationLabel != "activate 22.2.0 globally" {
					t.Fatalf("activation target absent from label: %q", activationLabel)
				}
				if detail := m.rowDetails(m.rows(test.view)[0]); !strings.Contains(detail, "already installed; pending global activation") {
					t.Fatalf("details hide pending activation: %s", detail)
				}
			}
			deliver(m, press(m, "a"))
			if len(f.requests) != 1 || f.requests[0].Operation != "activate" || f.requests[0].Version != test.wantVersion {
				t.Fatalf("wrong activation request: %#v", f.requests)
			}
			press(m, "esc")
			deliver(m, press(m, "x"))
			if len(f.requests) != 2 || f.requests[1].Version != p.Version {
				t.Fatal("pending activation changed removal target")
			}
		})
	}
}

func TestSetupOneReviewContainsOnlySelectedMissingComponents(t *testing.T) {
	m, f := readyModel(t)
	f.options = []domain.SetupOption{{ID: "mpm", Name: "mpm", Recommended: true}, {ID: "uv", Name: "uv", Installed: true}, {ID: "mise", Name: "mise"}}
	deliver(m, press(m, "s"))
	press(m, "j")
	press(m, " ") // Installed uv cannot be selected.
	press(m, "j")
	press(m, " ")
	deliver(m, press(m, "enter"))
	if strings.Join(f.setupIDs, ",") != "mpm,mise" {
		t.Fatalf("unexpected setup plan: %#v", f.setupIDs)
	}
	if m.modal != planModal || f.executeCalls != 0 {
		t.Fatal("setup changed the machine before review")
	}
	press(m, "esc")
	if m.modal != setupModal || !m.setup.selected["mise"] {
		t.Fatal("back from review lost setup draft")
	}
	press(m, "esc")
	old := m.setup.generation
	m.Update(setupMsg{generation: old - 1, options: []domain.SetupOption{{ID: "late"}}})
	if m.modal != noModal {
		t.Fatal("cancelled setup reopened")
	}
}

func TestRenderingBoundariesAndUsefulViews(t *testing.T) {
	m, _ := readyModel(t)
	m.states[installedView].snapshot.Packages[0].ID = "專案 e\u0301 👩🏽‍💻"
	m.states[installedView].snapshot.Packages[0].Description = "\x1b]52;c;secret\aNice tool\x1b[2J"
	m.reconcile(installedView, true)
	for _, size := range [][2]int{{0, 0}, {20, 6}, {48, 16}, {80, 24}, {140, 36}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, modal := range []modalKind{noModal, detailsModal, helpModal, setupModal, planModal, resultModal} {
			m.modal = modal
			m.detailKey = m.states[m.view].selected
			content := m.View().Content
			lines := strings.Split(content, "\n")
			if len(lines) > max(1, size[1]) {
				t.Fatalf("height overflow %v modal %d: %d", size, modal, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > max(1, size[0]) {
					t.Fatalf("width overflow at %v: %q", size, line)
				}
			}
			if strings.Contains(content, "]52;") || strings.Contains(content, "\x1b[2J") {
				t.Fatal("untrusted terminal control sequence rendered")
			}
		}
	}
	m.modal = noModal
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"Installed", "Discover", "Updates", "Diagnostics", "Managers", "專案", "Enter details", "s setup"} {
		if !strings.Contains(content, want) {
			t.Errorf("80×24 dashboard missing %q:\n%s", want, content)
		}
	}
	press(m, "enter")
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, "Provider: brew") || !strings.Contains(content, "Source evidence") {
		t.Fatalf("detail overlay missing source facts:\n%s", content)
	}
}

func TestNoColorAndSelectionAfterSuccessfulEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m, _ := readyModel(t)
	if strings.Contains(m.View().Content, "\x1b[") {
		t.Fatal("NO_COLOR output contains styling")
	}
	m.loadView(installedView)
	m.Update(packagesMsg{view: installedView, generation: m.states[installedView].generation, snapshot: domain.Snapshot{}})
	if m.states[installedView].selected != "" || len(m.rows(installedView)) != 0 {
		t.Fatal("successful empty result retained obsolete rows")
	}
}

func TestTerminalExecutionKeepsResultUntilAcknowledgement(t *testing.T) {
	f := &fakeService{result: domain.ActionResult{Message: "Installed alpha", Steps: []domain.StepResult{{ID: "install", Status: "done"}}}}
	var out bytes.Buffer
	read := &ackReader{before: func() {
		if !strings.Contains(out.String(), "Installed alpha") || !strings.Contains(out.String(), "Press Enter") {
			t.Fatal("result disappeared before acknowledgement")
		}
	}}
	e := &execution{ctx: context.Background(), service: f, plan: domain.ActionPlan{Title: "Install alpha"}}
	e.SetStdin(read)
	e.SetStdout(&out)
	e.SetStderr(&out)
	if err := e.Run(); err != nil {
		t.Fatal(err)
	}
	if !read.called || f.executeCalls != 1 {
		t.Fatal("execution skipped result acknowledgement")
	}
	f.executeErr = errors.New("interrupted; partial changes may remain")
	e.SetStdin(strings.NewReader("\n"))
	if err := e.Run(); err == nil || !strings.Contains(out.String(), "partial changes") {
		t.Fatal("native failure was hidden")
	}
}

type ackReader struct {
	before func()
	called bool
}

func (r *ackReader) Read(p []byte) (int, error) {
	if r.called {
		return 0, io.EOF
	}
	r.called = true
	r.before()
	p[0] = '\n'
	return 1, nil
}
