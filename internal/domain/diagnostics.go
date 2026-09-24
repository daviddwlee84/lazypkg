package domain

import "strings"

// FilterDiagnostics selects relevant command groups, not individual competing
// executables. Every participant and the true winner remain visible; unknown
// ownership is retained so a filter cannot manufacture a clean result.
func FilterDiagnostics(report DiagnosticReport, ids []string) DiagnosticReport {
	if len(ids) == 0 {
		return report
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	names := map[string]bool{}
	paths := map[string]bool{}
	for _, e := range report.Executables {
		if selected[e.Manager] || e.Manager == "" {
			names[e.Name] = true
			paths[e.Path] = true
		}
	}
	findings := []Finding{}
	for _, f := range report.Findings {
		keep := names[f.Name]
		for _, p := range f.Paths {
			keep = keep || paths[p]
		}
		if keep {
			findings = append(findings, f)
			names[f.Name] = true
			for _, p := range f.Paths {
				paths[p] = true
			}
		}
	}
	executables := []Executable{}
	for _, e := range report.Executables {
		if names[e.Name] || paths[e.Path] {
			executables = append(executables, e)
		}
	}
	report.Findings = findings
	report.Executables = executables
	report.Scope += "; selected providers " + strings.Join(ids, ", ") + " (all peers and unknown owners retained)"
	return report
}
