package tui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

var (
	updateSorts     = []string{"name", "type", "provider"}
	updateFilters   = []string{"all", "major", "minor", "patch", "0.x", "other"}
	updateKindOrder = []domain.UpdateKind{domain.UpdateMajor, domain.UpdateMinor, domain.UpdatePatch, domain.UpdatePrerelease, domain.UpdateRevision, domain.UpdateCalendar, domain.UpdateUnknown}
)

func updateFilterMatches(filter string, class domain.UpdateClass) bool {
	switch filter {
	case "major", "minor", "patch":
		return string(class.Kind) == filter
	case "0.x":
		return class.Unstable
	case "other":
		return class.Kind != domain.UpdateMajor && class.Kind != domain.UpdateMinor && class.Kind != domain.UpdatePatch
	}
	return true
}

// sortUpdateRows orders by the chosen key and falls back to the package ID,
// so selection (kept by row key) stays stable across sort changes.
func sortUpdateRows(rows []row, mode string) {
	byName := func(a, b row) int { return cmp.Compare(strings.ToLower(a.label), strings.ToLower(b.label)) }
	switch mode {
	case "type":
		slices.SortStableFunc(rows, func(a, b row) int {
			return cmp.Or(cmp.Compare(slices.Index(updateKindOrder, a.update.Kind), slices.Index(updateKindOrder, b.update.Kind)), byName(a, b))
		})
	case "provider":
		slices.SortStableFunc(rows, func(a, b row) int { return cmp.Or(cmp.Compare(a.manager, b.manager), byName(a, b)) })
	}
}
