package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func providerLabel(id string) string {
	if id == "gh-ext" {
		return "gh ext"
	}
	return id
}
func excludedState(state string) bool {
	return state == "excluded" || state == "unsupported" || state == "unavailable"
}
func (m *Model) queryCoverageCounts() (int, int) {
	s := &m.states[m.view]
	failed, excluded := map[string]bool{}, map[string]bool{}
	for _, c := range s.snapshot.Coverage {
		if c.State == "failed" {
			failed[c.Manager] = true
		}
		if excludedState(c.State) {
			excluded[c.Manager] = true
		}
	}
	issues := s.snapshot.Issues
	if m.view == diagnosticsView {
		issues = s.report.Issues
	}
	for _, issue := range issues {
		id := issue.Manager
		if id == "" {
			id = issue.Message
		}
		if excludedState(issue.Kind) || excluded[id] {
			excluded[id] = true
		} else {
			failed[id] = true
		}
	}
	return len(failed), len(excluded)
}
func (m *Model) coverageText() string {
	var lines []string
	seen := map[string]bool{}
	add := func(manager, message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		key := manager + "\x00" + message
		if seen[key] {
			return
		}
		seen[key] = true
		prefix := ""
		if manager != "" {
			prefix = providerLabel(manager) + ": "
		}
		lines = append(lines, prefix+message)
	}
	if m.managersErr != nil {
		add("", "Manager discovery: "+m.managersErr.Error())
	}
	if m.healthErr != nil {
		add("", "Manager update check: "+m.healthErr.Error())
	}
	if m.prefsErr != nil {
		add("", "Provider preferences: "+m.prefsErr.Error())
	}
	s := &m.states[m.view]
	if s.err != nil {
		add("", viewNames[m.view]+": "+s.err.Error())
	}
	coverageSeen := map[string]bool{}
	appendCoverage := func(c domain.Coverage, kind string) {
		key := strings.Join([]string{kind, c.Manager, c.Instance, c.State, c.Message, c.Enrichment, fmt.Sprint(c.Stale)}, "\x00")
		if coverageSeen[key] {
			return
		}
		coverageSeen[key] = true
		label := kind + c.State
		if c.Stale {
			label += " (stale)"
		}
		if progress := s.providers[c.Manager]; progress.elapsed > 0 && kind == "" {
			label += " · " + progress.elapsed.Round(time.Millisecond).String()
		}
		if c.Enrichment != "" {
			label += " · ownership " + c.Enrichment
		}
		add(c.Manager, label)
		add(c.Manager, c.Message)
	}
	for _, c := range s.snapshot.Coverage {
		appendCoverage(c, "")
	}
	if m.view == discoverView {
		for _, c := range s.snapshot.InventoryCoverage {
			if c.State != "complete" && c.State != "pending" {
				appendCoverage(c, "inventory: ")
			}
		}
	}
	for _, manager := range m.managers {
		for _, err := range manager.Errors {
			add(manager.ID, err)
		}
	}
	issues := s.snapshot.Issues
	if m.view == diagnosticsView {
		issues = s.report.Issues
	}
	for _, issue := range issues {
		add(issue.Manager, issue.Message)
	}
	if len(lines) == 0 {
		lines = append(lines, "No errors reported for this view.")
	}
	lines = append(lines, "", "f changes providers; s opens setup for missing components.")
	return strings.Join(lines, "\n\n")
}
