package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypkg/internal/domain"
)

type extendedService struct {
	*fakeService
	prefs        domain.ManagerPreferences
	saveCalls    int
	savedName    string
	savedIDs     []string
	savedDefault bool
	saveErr      error
	checks       []bool
	managerPlans []string
}

func (f *extendedService) Preferences(context.Context) (domain.ManagerPreferences, error) {
	return f.prefs, nil
}
func (f *extendedService) SaveManagerSet(_ context.Context, name string, ids []string, asDefault bool) (domain.ManagerPreferences, error) {
	f.saveCalls++
	f.savedName = name
	f.savedIDs = append([]string(nil), ids...)
	f.savedDefault = asDefault
	if f.saveErr != nil {
		return domain.ManagerPreferences{}, f.saveErr
	}
	prefs := f.prefs
	prefs.Sets = map[string][]string{name: append([]string(nil), ids...)}
	if asDefault {
		prefs.DefaultSet = name
		prefs.Default = append([]string(nil), ids...)
	}
	return prefs, nil
}
func (f *extendedService) CheckManagers(_ context.Context, ids []string, force bool) ([]domain.ManagerHealth, error) {
	f.checks = append(f.checks, force)
	var health []domain.ManagerHealth
	for _, id := range ids {
		h := domain.ManagerHealth{Manager: id, UpdateStatus: "update available", ApplySupported: true, CandidateVersion: "new", CheckedAt: time.Now()}
		for _, manager := range f.managers {
			if manager.ID == id {
				h.Path = manager.Path
				h.Version = manager.Version
			}
		}
		health = append(health, h)
	}
	return health, nil
}
func (f *extendedService) PlanManagerUpdate(_ context.Context, id string) (domain.ActionPlan, error) {
	f.managerPlans = append(f.managerPlans, id)
	return domain.ActionPlan{Kind: "manager-update", Title: "Update " + id}, nil
}

func extendedModel(t *testing.T) (*Model, *extendedService) {
	m, f := readyModel(t)
	x := &extendedService{fakeService: f, prefs: domain.ManagerPreferences{Mouse: true, Default: []string{"brew", "mise"}, Order: []string{"brew", "mise"}, Groups: map[string][]string{"global": {"brew", "mise"}, "runtimes": {"mise"}}, Sets: map[string][]string{"daily": {"mise", "brew"}}}}
	m.service = x
	deliver(m, m.loadPreferences())
	return m, x
}
func target(t *testing.T, m *Model, kind, value string) hitTarget {
	t.Helper()
	for _, hit := range m.layout().targets {
		if hit.kind == kind && hit.value == value {
			return hit
		}
	}
	t.Fatalf("no visible target %s/%q; view:\n%s", kind, value, ansi.Strip(m.View().Content))
	return hitTarget{}
}
func mouseClick(m *Model, hit hitTarget) tea.Cmd {
	x, y := hit.rect.x+hit.rect.w/2, hit.rect.y
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, command := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return command
}

func TestMouseUsesVisibleSemanticTargetsAndSharedActions(t *testing.T) {
	m, f := readyModel(t)
	bravo := m.rows(installedView)[1].key
	mouseClick(m, target(t, m, "row", bravo))
	if m.states[installedView].selected != bravo {
		t.Fatal("mouse row did not select its identity")
	}
	mouseClick(m, target(t, m, "key", "enter"))
	if m.modal != detailsModal {
		t.Fatal("Details click did not share keyboard action")
	}
	mouseClick(m, target(t, m, "key", "esc"))
	cmd := mouseClick(m, target(t, m, "key", "x"))
	deliver(m, cmd)
	if m.modal != planModal || len(f.requests) != 1 || f.executeCalls != 0 {
		t.Fatal("mouse removal skipped the plan")
	}
	mouseClick(m, target(t, m, "key", "esc"))
	mouseClick(m, target(t, m, "tab", "1"))
	if m.view != discoverView {
		t.Fatal("tab click did not switch views")
	}
}

func TestMousePressInvalidatedByResizeDataAndModal(t *testing.T) {
	for _, event := range []string{"resize", "data", "modal", "drag"} {
		t.Run(event, func(t *testing.T) {
			m, f := readyModel(t)
			hit := target(t, m, "key", "x")
			x, y := hit.rect.x+1, hit.rect.y
			m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			switch event {
			case "resize":
				m.Update(tea.WindowSizeMsg{Width: 79, Height: 24})
			case "data":
				m.Update(packagesMsg{view: installedView, generation: m.states[installedView].generation, snapshot: m.states[installedView].snapshot})
			case "modal":
				press(m, "?")
			case "drag":
				x = 0
				y = 0
			}
			_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
			if cmd != nil || len(f.requests) > 0 || m.modal == planModal {
				t.Fatal("stale press performed an action")
			}
		})
	}
	m, _ := readyModel(t)
	row := target(t, m, "row", m.rows(installedView)[1].key)
	selected := m.states[installedView].selected
	press(m, "?")
	mouseClick(m, row)
	if m.modal != helpModal || m.states[installedView].selected != selected {
		t.Fatal("mouse click reached behind modal")
	}
}

func TestMousePreferenceOverrideToggleAndWheel(t *testing.T) {
	m, f := readyModel(t)
	disabled := false
	m = New(context.Background(), f, "installed", Options{Mouse: &disabled})
	m.Update(preferencesMsg{preferences: domain.ManagerPreferences{Mouse: true}})
	if m.mouseEnabled {
		t.Fatal("late preference overwrote CLI override")
	}
	press(m, "M")
	m.Update(preferencesMsg{preferences: domain.ManagerPreferences{Mouse: false}})
	if !m.mouseEnabled || m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("late preference overwrote session toggle")
	}
	m, f = readyModel(t)
	_ = f
	x, y := m.layout().items.x+2, 8
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	if m.states[installedView].position == 0 {
		t.Fatal("wheel did not navigate hovered list")
	}
	press(m, "M")
	selected := m.states[installedView].selected
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp})
	if m.states[installedView].selected != selected || m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("disabled mouse still handled events")
	}
}

func TestOrderedProviderDraftApplyCancelAndSave(t *testing.T) {
	m, f := extendedModel(t)
	press(m, "f")
	m.selectProviderChoice("set:daily")
	press(m, " ")
	if strings.Join(m.providerPicker.selected, ",") != "mise,brew" {
		t.Fatal("set order lost")
	}
	m.selectProviderChoice("brew")
	press(m, "[")
	if strings.Join(m.providerPicker.selected, ",") != "brew,mise" {
		t.Fatal("priority change failed")
	}
	if f.packageCalls != 0 || f.saveCalls != 0 {
		t.Fatal("editing draft performed work")
	}
	press(m, "esc")
	if strings.Join(m.effectiveManagers(), ",") != "brew,mise" {
		t.Fatal("cancel changed active scope")
	}
	press(m, "f")
	m.selectProviderChoice("group:runtimes")
	mouseClick(m, target(t, m, "provider", "group:runtimes"))
	cmd := mouseClick(m, target(t, m, "key", "enter"))
	deliver(m, cmd)
	if len(f.queryRequests) != 1 || strings.Join(f.queryRequests[0].Managers, ",") != "mise" {
		t.Fatalf("Apply did not submit one scoped query: %#v", f.queryRequests)
	}
	press(m, "f")
	press(m, "S")
	press(m, "daily tools")
	press(m, "tab")
	if f.saveCalls != 0 {
		t.Fatal("save occurred before explicit submission")
	}
	deliver(m, mouseClick(m, target(t, m, "key", "enter")))
	if f.savedName != "daily tools" || strings.Join(f.savedIDs, ",") != "mise" || !f.savedDefault {
		t.Fatal("save did not carry exact ordered draft/default choice")
	}
	if m.modal != providersModal || m.preferences.DefaultSet != "daily tools" {
		t.Fatal("saved settings not reconciled")
	}
}

func TestSetNameOwnsTypingAndFailedSavePreservesDraft(t *testing.T) {
	m, f := extendedModel(t)
	press(m, "f")
	press(m, "S")
	beforeMouse := m.mouseEnabled
	press(m, "Mjkhqliuxa?/")
	m.Update(tea.PasteMsg{Content: "y\nq"})
	if m.mouseEnabled != beforeMouse || m.modal != saveSetModal || m.quitting {
		t.Fatal("name input activated shortcuts")
	}
	f.saveErr = errors.New("write denied")
	deliver(m, press(m, "enter"))
	if m.modal != saveSetModal || m.providerPicker.err == nil || m.input.Value() == "" {
		t.Fatal("failed save lost editable draft")
	}
	press(m, "esc")
	if m.modal != providersModal {
		t.Fatal("Back did not preserve selection picker")
	}
}

func TestDiscoverInventoryJoinPreservesCandidateIdentityAndVersions(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = discoverView
	m.states[discoverView].query = "herdr"
	m.ensureInventory(false)
	cached := m.inventories[m.scopeKey()]
	m.states[discoverView].generation++
	m.Update(packagesMsg{view: discoverView, generation: m.states[discoverView].generation, query: "herdr", snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr"}, {Manager: "mise", ID: "herdr"}}}})
	if p := m.states[discoverView].snapshot.Packages[0]; p.InstallState != "checking" {
		t.Fatalf("pending inventory falsely reported absence: %#v", p)
	}
	selected := m.states[discoverView].selected
	m.Update(inventoryMsg{key: m.scopeKey(), generation: cached.generation, snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "mise", ID: "herdr", Version: "1.0"}, {Manager: "mise", ID: "herdr", Version: "2.0"}}, Coverage: []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: time.Now()}, {Manager: "mise", State: "complete", ObservedAt: time.Now()}}}})
	s := &m.states[discoverView]
	if s.selected != selected || s.position != 1 {
		t.Fatal("inventory reranking changed the user's selected candidate")
	}
	if p := s.snapshot.Packages[0]; p.Manager != "mise" || p.InstallState != "installed" || strings.Join(p.InstalledVersions, ",") != "1.0,2.0" || p.Latest != "" {
		t.Fatalf("join confused installed and available versions: %#v", p)
	}
	detail := m.rowDetails(m.rows(discoverView)[0])
	if !strings.Contains(detail, "Installed: 1.0, 2.0") || !strings.Contains(detail, "Available version: not reported") {
		t.Fatal(detail)
	}
	if s.snapshot.Packages[1].InstallState != "not_installed" {
		t.Fatal("successful empty provider was not distinguished")
	}
}

func TestInventorySupersessionFailureAndCacheReuse(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = discoverView
	m.states[discoverView].query = "herdr"
	m.ensureInventory(false)
	key := m.scopeKey()
	cached := m.inventories[key]
	old := cached.generation
	m.ensureInventory(true)
	newGeneration := cached.generation
	m.Update(inventoryMsg{key: key, generation: old, snapshot: domain.Snapshot{Packages: []domain.Package{{ID: "late"}}}})
	if cached.loaded {
		t.Fatal("superseded inventory populated cache")
	}
	m.Update(inventoryMsg{key: key, generation: newGeneration, err: errors.New("inventory unavailable")})
	m.states[discoverView].generation++
	m.Update(packagesMsg{view: discoverView, generation: m.states[discoverView].generation, query: "herdr", snapshot: domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr"}}}})
	if m.states[discoverView].snapshot.Packages[0].InstallState != "check_failed" {
		t.Fatal("inventory failure shown as absence")
	}
	m.ensureInventory(true)
	m.Update(inventoryMsg{key: key, generation: cached.generation, snapshot: domain.Snapshot{Coverage: []domain.Coverage{{Manager: "brew", State: "complete", ObservedAt: time.Now()}}}})
	if m.ensureInventory(false) != nil {
		t.Fatal("complete per-scope inventory was not reused")
	}
	m.applyScope([]string{"mise"}, "runtime")
	if m.scopeKey() == key {
		t.Fatal("different manager set shared inventory key")
	}
}

func TestManagerCatalogAndUpdateOfIncompatibleManager(t *testing.T) {
	m, f := extendedModel(t)
	f.managers = []domain.Manager{{ID: "npm", Name: "npm", Path: "/bin/npm", Version: "old", Available: false, Status: "version unsupported", Requirement: ">=11", Scope: "global"}, {ID: "missing", Name: "Missing", Supported: true, Status: "missing"}}
	m.view = managersView
	deliver(m, m.loadManagers())
	if len(m.rows(managersView)) != 1 || len(f.checks) != 1 || f.checks[0] {
		t.Fatal("default catalog/check did not use detected managers/cache")
	}
	if cmd := press(m, "u"); cmd == nil {
		t.Fatal("incompatible manager's owner update was disabled")
	} else {
		deliver(m, cmd)
	}
	if len(f.managerPlans) != 1 || f.managerPlans[0] != "npm" || f.executeCalls != 0 {
		t.Fatal("manager update did not require review")
	}
	press(m, "esc")
	deliver(m, press(m, "r"))
	if len(f.checks) < 2 || !f.checks[len(f.checks)-1] {
		t.Fatal("r failed to force the manager update check")
	}
	press(m, "b")
	if len(m.rows(managersView)) != 2 {
		t.Fatal("missing catalog entries could not be shown")
	}
	old := m.healthGeneration
	m.checkManagerHealth(false)
	m.Update(healthMsg{generation: old, health: []domain.ManagerHealth{{Manager: "npm", UpdateStatus: "late wrong result"}}})
	if m.managers[0].Health != nil && m.managers[0].Health.UpdateStatus == "late wrong result" {
		t.Fatal("late manager check overwrote latest state")
	}
}

func TestNewModalGeometryRemainsVisibleAndClippedButtonsCannotClick(t *testing.T) {
	m, _ := extendedModel(t)
	for _, size := range [][2]int{{30, 10}, {48, 16}, {80, 24}, {140, 40}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, modal := range []modalKind{providersModal, saveSetModal, planModal, setupModal, detailsModal} {
			m.modal = modal
			content := m.View().Content
			for _, line := range strings.Split(content, "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("modal overflow")
				}
			}
			for _, hit := range m.layout().targets {
				if hit.rect.x+hit.rect.w > m.width || hit.rect.y+hit.rect.h > m.height {
					t.Fatal("clipped button kept hit target")
				}
				if hit.kind != "key" && hit.rect.y+hit.rect.h > m.height-4 {
					t.Fatal("invisible modal content retained a mouse target")
				}
			}
		}
	}
}

func TestGuidanceOnlyManagerPlanCannotExecute(t *testing.T) {
	m, f := readyModel(t)
	m.modal = planModal
	m.plan = domain.ActionPlan{Title: "Update owner", ManagerUpdate: &domain.ManagerHealth{Manager: "npm", ApplySupported: false, Recommendation: "Update the owning runtime", GuideURL: "https://example.invalid/docs"}}
	if press(m, "y") != nil || m.executing || f.executeCalls != 0 {
		t.Fatal("guidance-only plan was executed")
	}
	for _, hit := range m.layout().targets {
		if hit.kind == "key" && hit.value == "y" {
			t.Fatal("guidance-only plan exposed an Execute button")
		}
	}
	if text := m.planText(); !strings.Contains(text, "Guidance only") || !strings.Contains(text, "Update the owning runtime") || !strings.Contains(text, "https://example.invalid/docs") {
		t.Fatal(text)
	}
}

func TestExplicitDiagnosticSubsetRetainsPeersAndUnknownOwners(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = diagnosticsView
	m.states[diagnosticsView].report = domain.DiagnosticReport{Executables: []domain.Executable{{Name: "rg", Path: "/brew/rg", Manager: "brew"}, {Name: "rg", Path: "/mise/rg", Manager: "mise", Preferred: true}, {Name: "other", Path: "/cargo/other", Manager: "cargo"}, {Name: "manual", Path: "/unknown/manual"}}}
	if len(m.rows(diagnosticsView)) != 4 {
		t.Fatal("default PATH view was filtered")
	}
	m.managerIDs = []string{"brew"}
	m.scopeChanged = true
	rows := m.rows(diagnosticsView)
	if len(rows) != 3 {
		t.Fatalf("wrong subset peer count %d", len(rows))
	}
	foundWinner := false
	for _, row := range rows {
		if row.executable != nil && row.executable.Preferred {
			foundWinner = true
		}
	}
	if !foundWinner {
		t.Fatal("actual PATH winner was hidden")
	}
}

func TestExplicitSaveOutranksLatePreferenceReadAndRefreshBypassesCache(t *testing.T) {
	m, _ := extendedModel(t)
	generation := m.prefsGeneration
	press(m, "f")
	press(m, "S")
	press(m, "new set")
	press(m, "tab")
	deliver(m, press(m, "enter"))
	m.Update(preferencesMsg{generation: generation, preferences: domain.ManagerPreferences{DefaultSet: "old"}})
	if m.preferences.DefaultSet != "new set" {
		t.Fatal("late preference read overwrote explicit save")
	}
	press(m, "esc")
	f := m.service.(*extendedService)
	deliver(m, press(m, "r"))
	if !f.queryRequests[len(f.queryRequests)-1].Refresh {
		t.Fatal("refresh did not bypass the inventory cache")
	}
}

func TestClickingFilterInputDoesNotTypeScopeShortcut(t *testing.T) {
	m, _ := readyModel(t)
	press(m, "/")
	press(m, "alpha")
	mouseClick(m, target(t, m, "input", ""))
	if m.input.Value() != "alpha" || !m.filtering {
		t.Fatal("clicking query field inserted a shortcut")
	}
	mouseClick(m, target(t, m, "tab", "1"))
	if m.filtering || m.view != discoverView {
		t.Fatal("switching tabs leaked another view's input focus")
	}
}

func TestCachedServiceInstallationRemainsVisibleDuringBackgroundRead(t *testing.T) {
	m, _ := extendedModel(t)
	m.view = discoverView
	s := &m.states[discoverView]
	s.query = "herdr"
	s.loaded = true
	s.candidates = domain.Snapshot{Packages: []domain.Package{{Manager: "brew", ID: "herdr", Candidate: true, InstallState: "installed", InstalledVersions: []string{"1.2"}, InventoryAt: time.Now()}}}
	m.ensureInventory(false)
	if p := s.snapshot.Packages[0]; p.InstallState != "installed" || strings.Join(p.InstalledVersions, ",") != "1.2" {
		t.Fatal("pending refresh erased valid cached service observation")
	}
}

func TestInventoryReadersSupersedeEachOtherWithoutLateOverwrite(t *testing.T) {
	m, _ := extendedModel(t)
	m.loadView(installedView)
	old := m.states[installedView].generation
	m.ensureInventory(true)
	m.Update(packagesMsg{view: installedView, generation: old, snapshot: domain.Snapshot{Packages: []domain.Package{{ID: "old foreground"}}}})
	if m.states[installedView].snapshot.Packages[0].ID == "old foreground" {
		t.Fatal("forced background refresh accepted older foreground reply")
	}
	cached := m.inventories[m.scopeKey()]
	oldBackground := cached.generation
	m.loadView(installedView)
	m.Update(inventoryMsg{key: m.scopeKey(), generation: oldBackground, snapshot: domain.Snapshot{Packages: []domain.Package{{ID: "old background"}}}})
	if cached.loaded {
		t.Fatal("new foreground read accepted superseded background reply")
	}
}
