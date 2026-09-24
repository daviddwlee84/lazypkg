package maintenance

import (
	"fmt"
	"strconv"
	"strings"
)

type semver struct{ major, minor, patch int }

func (v semver) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }
func (v semver) compare(w semver) int {
	for i, x := range []int{v.major, v.minor, v.patch} {
		y := []int{w.major, w.minor, w.patch}[i]
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
func version(text string) (semver, bool) {
	text = strings.TrimSpace(text)
	for _, word := range strings.Fields(text) {
		word = strings.TrimPrefix(strings.TrimPrefix(word, "go"), "v")
		if strings.ContainsAny(word, "-+") {
			continue
		}
		parts := strings.Split(word, ".")
		if len(parts) != 3 {
			continue
		}
		var values [3]int
		ok := true
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				ok = false
				break
			}
			values[i] = n
		}
		if ok {
			return semver{values[0], values[1], values[2]}, true
		}
	}
	return semver{}, false
}
func minimum(v semver, requirement string) bool {
	r := strings.TrimSpace(requirement)
	if r == "" {
		return true
	}
	r = strings.TrimPrefix(r, ">=")
	if w, ok := version(r); ok {
		return v.compare(w) >= 0
	}
	ok, known := satisfies(v, requirement)
	return known && ok
}

// npm currently publishes simple engines ranges. Unsupported syntax fails
// closed instead of guessing compatibility (including future range syntax).
func satisfies(v semver, rangeText string) (bool, bool) {
	if strings.TrimSpace(rangeText) == "" {
		return false, false
	}
	anyKnown := true
	matched := false
	for _, branch := range strings.Split(rangeText, "||") {
		branch = strings.TrimSpace(branch)
		if branch == "*" {
			matched = true
			continue
		}
		if lo, hi, ok := strings.Cut(branch, " - "); ok {
			a, oka := version(lo)
			b, okb := version(hi)
			if !oka || !okb {
				return false, false
			}
			matched = matched || (v.compare(a) >= 0 && v.compare(b) <= 0)
			continue
		}
		terms := strings.Fields(strings.ReplaceAll(branch, ",", " "))
		pass := len(terms) > 0
		for i := 0; i < len(terms); i++ {
			term := terms[i]
			if term == ">=" || term == "<=" || term == ">" || term == "<" || term == "=" || term == "^" || term == "~" {
				i++
				if i >= len(terms) {
					return false, false
				}
				term += terms[i]
			}
			result, known := constraint(v, term)
			if !known {
				anyKnown = false
			}
			pass = pass && result && known
		}
		matched = matched || pass
	}
	return matched, anyKnown
}
func constraint(v semver, term string) (bool, bool) {
	op := ""
	for _, candidate := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(term, candidate) {
			op = candidate
			term = strings.TrimPrefix(term, candidate)
			break
		}
	}
	parts := strings.Split(strings.TrimPrefix(term, "v"), ".")
	if len(parts) > 3 || len(parts) == 0 {
		return false, false
	}
	count := len(parts)
	values := [3]int{}
	wild := -1
	for i, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			for _, later := range parts[i+1:] {
				if later != "x" && later != "X" && later != "*" {
					return false, false
				}
			}
			wild = i
			count = i
			break
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false, false
		}
		values[i] = n
	}
	w := semver{values[0], values[1], values[2]}
	if count < 3 && (op == ">" || op == "<=") {
		return false, false
	}
	cmp := v.compare(w)
	if wild >= 0 || op == "" && count < 3 {
		if op != "" && op != "=" {
			return false, false
		}
		if count == 0 {
			return true, true
		}
		upper := semver{w.major + 1, 0, 0}
		if count == 2 {
			upper = semver{w.major, w.minor + 1, 0}
		}
		return cmp >= 0 && v.compare(upper) < 0, true
	}
	switch op {
	case "", "=":
		return cmp == 0, true
	case ">=":
		return cmp >= 0, true
	case "<=":
		return cmp <= 0, true
	case ">":
		return cmp > 0, true
	case "<":
		return cmp < 0, true
	case "^":
		upper := semver{w.major + 1, 0, 0}
		if w.major == 0 {
			upper = semver{0, w.minor + 1, 0}
			if w.minor == 0 {
				upper = semver{0, 0, w.patch + 1}
			}
		}
		return cmp >= 0 && v.compare(upper) < 0, true
	case "~":
		return cmp >= 0 && v.compare(semver{w.major, w.minor + 1, 0}) < 0, true
	}
	return false, false
}
