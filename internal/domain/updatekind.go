package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// UpdateKind describes how far an available version is from the installed one.
// It is derived from version text for display only and never gates an upgrade.
type UpdateKind string

const (
	UpdateMajor    UpdateKind = "major"
	UpdateMinor    UpdateKind = "minor"
	UpdatePatch    UpdateKind = "patch"
	UpdateRevision UpdateKind = "revision" // same upstream version, rebuilt (brew _N, cask build)
	UpdateUnknown  UpdateKind = ""
)

var dateVersion = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

type parsedVersion struct {
	numbers  []int
	pre      string
	revision string
	calendar bool
}

func parseVersion(text string) (parsedVersion, bool) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "v")
	if text == "" {
		return parsedVersion{}, false
	}
	if dateVersion.MatchString(text) {
		return parsedVersion{calendar: true}, true
	}
	var v parsedVersion
	// Homebrew casks append a build after a comma; formulae append a revision after an underscore.
	if core, build, ok := strings.Cut(text, ","); ok {
		text, v.revision = core, build
	}
	if i := strings.LastIndex(text, "_"); i > 0 {
		v.revision = text[i+1:] + v.revision
		text = text[:i]
	}
	if i := strings.IndexAny(text, "-+"); i > 0 {
		text, v.pre = text[:i], text[i:]
	}
	for _, part := range strings.Split(text, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return parsedVersion{}, false
		}
		v.numbers = append(v.numbers, n)
	}
	v.calendar = v.numbers[0] >= 1000
	return v, true
}

// ClassifyUpdate reports the semantic distance from installed to latest.
// Calendar, unparseable or non-increasing versions are UpdateUnknown. For 0.x
// versions a minor change is treated as major and a patch change as minor,
// following semantic versioning's initial-development rule.
func ClassifyUpdate(installed, latest string) UpdateKind {
	a, okA := parseVersion(installed)
	b, okB := parseVersion(latest)
	if !okA || !okB || a.calendar || b.calendar {
		return UpdateUnknown
	}
	for i := range max(len(a.numbers), len(b.numbers)) {
		x, y := segment(a.numbers, i), segment(b.numbers, i)
		if x == y {
			continue
		}
		if y < x {
			return UpdateUnknown
		}
		if a.numbers[0] == 0 && i > 0 {
			i--
		}
		switch i {
		case 0:
			return UpdateMajor
		case 1:
			return UpdateMinor
		default:
			return UpdatePatch
		}
	}
	if a.pre != b.pre {
		return UpdatePatch
	}
	if a.revision != b.revision {
		return UpdateRevision
	}
	return UpdateUnknown
}

func segment(numbers []int, i int) int {
	if i < len(numbers) {
		return numbers[i]
	}
	return 0
}

// UpdateKind classifies the package's installed version against its latest.
func (p Package) UpdateKind() UpdateKind { return ClassifyUpdate(p.Version, p.Latest) }
