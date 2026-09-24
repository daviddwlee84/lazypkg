package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypkg/internal/domain"
	"github.com/daviddwlee84/lazypkg/internal/process"
)

type batchState struct {
	generation uint64
	cancel     context.CancelFunc
	loading    bool
	err        error
	origin     viewID
	request    domain.BatchUpgradeRequest
	plan       domain.BatchUpgradePlan
	result     domain.BatchUpgradeResult
	history    []domain.BatchUpgradeItemResult
	hidden     int
}
type batchPlanMsg struct {
	generation uint64
	plan       domain.BatchUpgradePlan
	err        error
}
type batchRefreshMsg struct {
	generation uint64
	request    domain.BatchUpgradeRequest
	snapshot   domain.Snapshot
	err        error
}
type batchExecutedMsg struct {
	generation uint64
	result     domain.BatchUpgradeResult
	err        error
}

func packageView(view viewID) bool { return view == installedView || view == updatesView }
func clonePackages(packages []domain.Package) []domain.Package {
	return domain.CloneSnapshot(domain.Snapshot{Packages: packages}).Packages
}
func (m *Model) upgradeEligibility(view viewID, p domain.Package) (bool, string) {
	if !packageView(view) {
		return false, "Upgrade selection is available in Installed and Updates."
	}
	manager := domain.Manager{}
	for _, candidate := range m.managers {
		if candidate.ID == p.Manager {
			manager = candidate
			break
		}
	}
	reason := domain.BatchUpgradeBlocker(p, manager)
	return reason == "", reason
}
func (m *Model) toggleMark(key string) {
	if !packageView(m.view) || m.managerFocus {
		return
	}
	s := &m.states[m.view]
	if _, ok := s.marks[key]; ok {
		delete(s.marks, key)
		s.markOrder = slices.DeleteFunc(s.markOrder, func(k string) bool { return k == key })
		return
	}
	for _, row := range m.rows(m.view) {
		if row.key == key && row.pkg != nil {
			if ok, reason := m.upgradeEligibility(m.view, *row.pkg); !ok {
				m.status = reason
				return
			}
			if s.marks == nil {
				s.marks = make(map[string]domain.Package)
			}
			s.marks[key] = clonePackages([]domain.Package{*row.pkg})[0]
			s.markOrder = append(s.markOrder, key)
			return
		}
	}
}
func (m *Model) toggleVisibleMarks() {
	if !packageView(m.view) || m.managerFocus {
		return
	}
	s := &m.states[m.view]
	eligible := []domain.Package{}
	all := true
	for _, row := range m.rows(m.view) {
		if row.pkg != nil {
			if ok, _ := m.upgradeEligibility(m.view, *row.pkg); ok {
				eligible = append(eligible, *row.pkg)
				if _, marked := s.marks[row.key]; !marked {
					all = false
				}
			}
		}
	}
	if len(eligible) == 0 {
		m.status = "No fresh, eligible packages can be selected in this filter."
		return
	}
	if s.marks == nil {
		s.marks = make(map[string]domain.Package)
	}
	for _, p := range clonePackages(eligible) {
		key := p.Key()
		if all {
			delete(s.marks, key)
		} else if _, marked := s.marks[key]; !marked {
			s.marks[key] = p
			s.markOrder = append(s.markOrder, key)
		}
	}
	if all {
		s.markOrder = slices.DeleteFunc(s.markOrder, func(key string) bool { _, marked := s.marks[key]; return !marked })
	}
}

func (m *Model) markCounts(view viewID) (int, int) {
	s := &m.states[view]
	if len(s.marks) == 0 {
		return 0, 0
	}
	visible := map[string]bool{}
	for _, row := range m.rows(view) {
		visible[row.key] = true
	}
	hidden := 0
	for key := range s.marks {
		if !visible[key] {
			hidden++
		}
	}
	return len(s.marks), hidden
}
func (m *Model) selectedUpgradeTargets() []domain.Package {
	s := &m.states[m.view]
	var targets []domain.Package
	for _, key := range s.markOrder {
		if p, ok := s.marks[key]; ok {
			targets = append(targets, p)
		}
	}
	return clonePackages(targets)
}
func (m *Model) visibleUpgradeTargets() []domain.Package {
	var targets []domain.Package
	for _, row := range m.rows(m.view) {
		if row.pkg != nil {
			targets = append(targets, *row.pkg)
		}
	}
	return clonePackages(targets)
}
func (m *Model) reconcileMarks(view viewID) {
	if !packageView(view) {
		return
	}
	s := &m.states[view]
	if len(s.marks) == 0 {
		return
	}
	present := map[string]bool{}
	for _, p := range s.snapshot.Packages {
		present[p.Key()] = true
	}
	completeProviders := map[string]bool{}
	for _, c := range s.snapshot.Coverage {
		if c.State == "complete" && !c.Stale {
			completeProviders[c.Manager] = true
		}
	}
	removed := 0
	for key, p := range s.marks {
		if present[key] {
			continue
		}

		if completeProviders[p.Manager] {
			delete(s.marks, key)
			removed++
		}
	}
	s.markOrder = slices.DeleteFunc(s.markOrder, func(key string) bool { _, ok := s.marks[key]; return !ok })
	if removed > 0 {
		m.status = fmt.Sprintf("%d selected target(s) changed or disappeared; they were unmarked, not replaced.", removed)
	}
}
func (m *Model) startBatchUpgrade(source string) tea.Cmd {
	if !packageView(m.view) || m.managerFocus {
		return nil
	}
	targets := m.visibleUpgradeTargets()
	hidden := 0
	if source == "selected" {
		targets = m.selectedUpgradeTargets()
		_, hidden = m.markCounts(m.view)
	}
	if len(targets) == 0 {
		m.status = "No package targets are selected for this upgrade."
		return nil
	}
	m.batch.history = nil
	m.batch.origin = m.view
	m.batch.hidden = hidden
	m.batch.result = domain.BatchUpgradeResult{}
	return m.planBatch(domain.BatchUpgradeRequest{Targets: targets, Source: source})
}
func (m *Model) planBatch(request domain.BatchUpgradeRequest) tea.Cmd {
	if m.batch.cancel != nil {
		m.batch.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.batch.cancel = cancel
	m.batch.generation++
	m.batch.request = domain.BatchUpgradeRequest{Targets: clonePackages(request.Targets), Source: request.Source}
	m.batch.plan = domain.BatchUpgradePlan{}
	m.batch.loading = true
	m.batch.err = nil
	m.modal = batchReviewModal
	m.modalOffset = 0
	generation, service := m.batch.generation, m.service
	frozen := domain.BatchUpgradeRequest{Targets: clonePackages(request.Targets), Source: request.Source}
	return func() tea.Msg {
		plan, err := service.PlanBatchUpgrade(ctx, frozen)
		return batchPlanMsg{generation, plan, err}
	}
}
func (m *Model) acceptBatchPlan(msg batchPlanMsg) {
	if msg.generation != m.batch.generation || m.modal != batchReviewModal {
		return
	}
	m.batch.loading, m.batch.err, m.batch.plan = false, msg.err, msg.plan
}
func (m *Model) batchExecutable() bool {
	if m.batch.loading || m.batch.err != nil || m.executing || m.width < 40 || m.height < 10 {
		return false
	}
	for _, entry := range m.batch.plan.Entries {
		if entry.State == "planned" && entry.Plan != nil {
			return true
		}
	}
	return false
}
func (m *Model) executeBatch() tea.Cmd {
	if !m.batchExecutable() {
		return nil
	}
	m.executing = true
	for i := range m.states {
		m.cancelView(viewID(i))
	}
	m.invalidateInventories()
	runner := &batchExecution{ctx: m.ctx, service: m.service, plan: m.batch.plan}
	generation := m.batch.generation
	return tea.Exec(runner, func(err error) tea.Msg { return batchExecutedMsg{generation, runner.result, err} })
}
func (m *Model) acceptBatchExecuted(msg batchExecutedMsg) tea.Cmd {
	if msg.generation != m.batch.generation {
		return nil
	}
	m.executing = false
	m.batch.result = msg.result
	m.batch.err = msg.err
	m.modal = batchResultModal
	m.modalOffset = 0
	for _, entry := range msg.result.Entries {
		if entry.State != "pending" {
			m.batch.history = append(m.batch.history, entry)
		}
	}
	m.lastResult = domain.ActionResult{Message: msg.result.Message}
	m.lastError = msg.err
	for i, item := range m.batch.history {
		m.lastResult.Steps = append(m.lastResult.Steps, domain.StepResult{ID: fmt.Sprintf("%d. %s / %s", i+1, providerLabel(item.Entry.Package.Manager), item.Entry.Package.ID), Status: item.State, Message: item.Message})
	}
	m.invalidateViews()
	s := &m.states[m.batch.origin]
	for _, result := range msg.result.Entries {
		if result.State == "success" || result.State == "current" {
			target := domain.BatchUpgradeTargetKey(result.Entry.Package)
			for key, p := range s.marks {
				if domain.BatchUpgradeTargetKey(p) == target {
					delete(s.marks, key)
				}
			}
		}
	}
	s.markOrder = slices.DeleteFunc(s.markOrder, func(key string) bool { _, ok := s.marks[key]; return !ok })
	m.status = ""
	m.saveBatchResult()
	return m.refreshAfterMutation(m.batch.origin)
}
func (m *Model) resumeBatch(skip bool) tea.Cmd {
	remaining := clonePackages(m.batch.result.Remaining)
	if len(remaining) == 0 {
		return nil
	}
	if skip {
		first := remaining[0]
		target := domain.BatchUpgradeTargetKey(first)
		m.batch.history = append(m.batch.history, domain.BatchUpgradeItemResult{Entry: domain.BatchUpgradeEntry{ID: target, Package: first}, State: "skipped", Message: "Skipped by user"})
		remaining = slices.DeleteFunc(remaining, func(p domain.Package) bool { return domain.BatchUpgradeTargetKey(p) == target })
		m.batch.result.Remaining = remaining
		m.saveBatchResult()
	}
	if len(remaining) == 0 {
		m.batch.result.Paused = false
		m.batch.result.Message = "No remaining targets. Completed changes are retained."
		m.saveBatchResult()
		m.status = ""
		return nil
	}
	return m.refreshBatch(domain.BatchUpgradeRequest{Targets: remaining, Source: "resume"})
}
func (m *Model) closeBatch() {
	if m.batch.cancel != nil {
		m.batch.cancel()
	}
	m.batch.generation++
	m.batch.loading = false
	m.status = ""
	m.modal = noModal
	m.modalOffset = 0
}
func (m *Model) batchKey(key tea.KeyPressMsg) tea.Cmd {
	name := key.String()
	if m.pageKey(name) {
		return nil
	}
	switch name {
	case "esc", "ctrl+c", "q":
		m.closeBatch()
	case "up", "k":
		m.scrollModalBy(-1)
	case "down", "j":
		m.scrollModalBy(1)
	case "home", "g":
		m.modalOffset = 0
	case "end", "G":
		m.modalOffset = m.modalScrollLimit()
	case "enter":
		m.status = "Review every target, then press y to approve this batch."
	case "y":
		if m.modal == batchReviewModal && !key.IsRepeat {
			return m.executeBatch()
		}
	case "r":
		if m.modal == batchResultModal {
			return m.resumeBatch(false)
		}
		if m.batch.request.Source == "resume" {
			return m.refreshBatch(m.batch.request)
		}
		return m.planBatch(m.batch.request)
	case "s":
		if m.modal == batchResultModal {
			return m.resumeBatch(true)
		}
	}
	return nil
}
func (m *Model) batchReviewText() string {
	if m.width < 40 || m.height < 10 {
		return "Resize to at least 40×10 to review the complete batch."
	}
	if m.batch.loading {
		return fmt.Sprintf("Preparing %d frozen package targets…\nNo changes have been made. New stream results will not join this batch.", len(m.batch.request.Targets))
	}
	if m.batch.err != nil {
		return "Could not prepare this batch.\n\n" + m.batch.err.Error() + "\n\nr rechecks the same frozen targets."
	}
	ready := 0
	for _, entry := range m.batch.plan.Entries {
		if entry.State == "planned" {
			ready++
		}
	}
	lines := []string{fmt.Sprintf("%d planned / %d grouped targets · %s", ready, len(m.batch.plan.Entries), m.batch.request.Source), fmt.Sprintf("%d selected targets are hidden by the current filter and included.", m.batch.hidden), "One confirmation authorizes only this frozen list. Native prompts remain interactive.", "A failure or changed plan pauses the batch; completed changes remain."}
	for i, entry := range m.batch.plan.Entries {
		p := entry.Package
		lines = append(lines, "", fmt.Sprintf("%d. [%s] %s / %s", i+1, entry.State, providerLabel(p.Manager), p.ID), batchVersionText(entry))
		if entry.Reason != "" {
			lines = append(lines, entry.Reason)
		}
		if plan := entry.Plan; plan != nil {
			if plan.Preview != "" {
				lines = append(lines, plan.Preview)
			} else {
				for _, step := range plan.Steps {
					if step.Command.Path != "" {
						lines = append(lines, process.Display(step.Command))
					}
				}
			}
			for _, warning := range plan.Warnings {
				lines = append(lines, "WARNING: "+warning)
			}
		}
	}
	return strings.Join(lines, "\n")
}
func batchResultText(result domain.BatchUpgradeResult, history []domain.BatchUpgradeItemResult, err error) string {
	lines := []string{result.Message}
	if err != nil {
		lines = append(lines, "Error: "+err.Error())
	}
	for _, item := range history {
		lines = append(lines, "", fmt.Sprintf("[%s] %s / %s", item.State, providerLabel(item.Entry.Package.Manager), item.Entry.Package.ID), item.Message)
	}
	if len(result.Remaining) > 0 {
		lines = append(lines, "", fmt.Sprintf("%d package target(s) remain. r rechecks, s skips the paused target, Esc stops.", len(result.Remaining)), "Resuming always prepares a new aggregate review.")
	}
	return strings.Join(lines, "\n")
}

type batchExecution struct {
	ctx         context.Context
	service     domain.Service
	plan        domain.BatchUpgradePlan
	in          io.Reader
	out, errout io.Writer
	result      domain.BatchUpgradeResult
}

func (e *batchExecution) SetStdin(in io.Reader)   { e.in = in }
func (e *batchExecution) SetStdout(out io.Writer) { e.out = out }
func (e *batchExecution) SetStderr(out io.Writer) { e.errout = out }
func (e *batchExecution) Run() error {
	if e.out == nil {
		e.out = io.Discard
	}
	if e.errout == nil {
		e.errout = e.out
	}
	fmt.Fprint(e.out, "\nRunning reviewed package upgrades in order.\n\n")
	var err error
	e.result, err = e.service.ExecuteBatchUpgrade(e.ctx, e.plan, e.in, e.out, e.errout)
	fmt.Fprintln(e.out, "\n"+clean(batchResultText(e.result, e.result.Entries, err)))
	if e.in != nil && e.ctx.Err() == nil {
		fmt.Fprint(e.out, "\nPress Enter to return to lazypkg. ")
		_, _ = bufio.NewReader(e.in).ReadString('\n')
	}
	return err
}

// Explicit recheck refreshes observed versions, while retaining each manager
// instance identity. It only prepares another overview, never resumes execution.
func (m *Model) refreshBatch(request domain.BatchUpgradeRequest) tea.Cmd {
	if m.batch.cancel != nil {
		m.batch.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.batch.cancel = cancel
	m.batch.generation++
	m.batch.request = domain.BatchUpgradeRequest{Targets: clonePackages(request.Targets), Source: "resume"}
	m.batch.loading = true
	m.batch.err = nil
	m.batch.plan = domain.BatchUpgradePlan{}
	m.modal = batchReviewModal
	m.modalOffset = 0
	ids := []string{}
	for _, p := range request.Targets {
		if !slices.Contains(ids, p.Manager) {
			ids = append(ids, p.Manager)
		}
	}
	generation, service := m.batch.generation, m.service
	frozen := domain.BatchUpgradeRequest{Targets: clonePackages(request.Targets), Source: "resume"}
	return func() tea.Msg {
		snapshot, err := service.Query(ctx, domain.PackageQuery{Kind: "installed", Managers: ids, Refresh: true})
		return batchRefreshMsg{generation, frozen, snapshot, err}
	}
}
func (m *Model) acceptBatchRefresh(msg batchRefreshMsg) tea.Cmd {
	if msg.generation != m.batch.generation || m.modal != batchReviewModal {
		return nil
	}
	if msg.err != nil {
		m.batch.loading = false
		m.batch.err = fmt.Errorf("refresh remaining packages: %w", msg.err)
		return nil
	}
	groups := map[string][]domain.Package{}
	order := []string{}
	for _, p := range msg.request.Targets {
		key := domain.BatchUpgradeTargetKey(p)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], p)
	}
	var targets []domain.Package
	for _, key := range order {
		var fresh []domain.Package
		for _, p := range msg.snapshot.Packages {
			if domain.BatchUpgradeTargetKey(p) != key {
				continue
			}
			for _, c := range msg.snapshot.Coverage {
				if c.Manager == p.Manager && c.State == "complete" && !c.Stale && (c.Instance == "" || c.Instance == p.Instance) {
					fresh = append(fresh, p)
					break
				}
			}
		}
		if len(fresh) > 0 {
			targets = append(targets, fresh...)
		} else {
			targets = append(targets, groups[key]...)
		}
	}
	return m.planBatch(domain.BatchUpgradeRequest{Targets: targets, Source: "resume"})
}

func batchVersionText(entry domain.BatchUpgradeEntry) string {
	observed := strings.Join(entry.ObservedVersions, ", ")
	if observed == "" {
		observed = entry.Package.Version
	}
	if observed == "" {
		observed = "not reported"
	}
	target := entry.TargetVersion
	if target == "" {
		target = entry.Package.Latest
	}
	if entry.State == "current" {
		return "Installed: " + observed + " · no available upgrade"
	}
	if entry.State == "excluded" {
		versions := []string{}
		for _, p := range entry.Targets {
			if p.Version != "" && !slices.Contains(versions, p.Version) {
				versions = append(versions, p.Version)
			}
		}
		if len(versions) > 0 {
			observed = strings.Join(versions, ", ")
		}
		text := "Selected: " + observed
		if target != "" {
			text += " · observed candidate: " + target
		}
		return text
	}
	if target == "" {
		return "Installed: " + observed + " · target not determined"
	}
	return "Installed: " + observed + " → " + target
}
