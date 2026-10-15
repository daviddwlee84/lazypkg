package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// UpdateKind names which part of the version text changed. It describes the
// version position only, not compatibility, and never gates an upgrade.
type UpdateKind string

const (
	UpdateMajor      UpdateKind = "major"
	UpdateMinor      UpdateKind = "minor"
	UpdatePatch      UpdateKind = "patch"
	UpdatePrerelease UpdateKind = "prerelease"
	UpdateRevision   UpdateKind = "revision" // same upstream version, rebuilt (brew _N, cask build)
	UpdateCalendar   UpdateKind = "calver"
	UpdateUnknown    UpdateKind = ""
)

// UpdateClass separates the changed version position from compatibility
// hints. Unstable marks a 0.x installed version: such releases make no
// stability promise, which does not prove this update breaks anything.
type UpdateClass struct {
	Kind     UpdateKind `json:"kind,omitempty"`
	Unstable bool       `json:"unstable,omitempty"`
}

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

// ClassifyUpdate reports which version position changed from installed to
// latest. Unparseable or non-increasing versions are UpdateUnknown; versions
// that look calendar-based are UpdateCalendar.
func ClassifyUpdate(installed, latest string) UpdateClass {
	a, okA := parseVersion(installed)
	b, okB := parseVersion(latest)
	if !okA || !okB {
		return UpdateClass{}
	}
	if a.calendar || b.calendar {
		if installed == latest {
			return UpdateClass{}
		}
		return UpdateClass{Kind: UpdateCalendar}
	}
	class := UpdateClass{Unstable: a.numbers[0] == 0}
	for i := range max(len(a.numbers), len(b.numbers)) {
		x, y := segment(a.numbers, i), segment(b.numbers, i)
		if x == y {
			continue
		}
		if y < x {
			return UpdateClass{}
		}
		switch i {
		case 0:
			class.Kind = UpdateMajor
		case 1:
			class.Kind = UpdateMinor
		default:
			class.Kind = UpdatePatch
		}
		return class
	}
	switch {
	case a.pre != b.pre:
		class.Kind = UpdatePrerelease
	case a.revision != b.revision:
		class.Kind = UpdateRevision
	default:
		return UpdateClass{}
	}
	return class
}

func segment(numbers []int, i int) int {
	if i < len(numbers) {
		return numbers[i]
	}
	return 0
}

// UpdateClass classifies the package's installed version against its latest.
func (p Package) UpdateClass() UpdateClass { return ClassifyUpdate(p.Version, p.Latest) }
