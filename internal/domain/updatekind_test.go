package domain

import "testing"

func TestClassifyUpdate(t *testing.T) {
	for _, tc := range []struct {
		installed, latest string
		want              UpdateKind
		unstable          bool
	}{
		{"4.0.0", "4.0.0_1", UpdateRevision, false},
		{"4.2.3", "4.4.4", UpdateMinor, false},
		{"2.6.9", "4.0.23", UpdateMajor, false},
		{"1.18.2_1", "1.18.3", UpdatePatch, false},
		{"5.3.15", "5.3.20", UpdatePatch, false},
		{"2.4.28,f3f5cec400aa", "2.4.29,0011bbcc", UpdatePatch, false},
		{"1.1.2156,4e9bb23e", "1.1.2156,5aa00000", UpdateRevision, false},
		{"2026-08-13", "2026-09-01", UpdateCalendar, false},
		{"2026.7.22", "2026.8.1", UpdateCalendar, false},
		{"2026.8.19_1", "2026.8.19_2", UpdateCalendar, false},
		{"v4.26.0", "v4.26.1", UpdatePatch, false},
		{"0.6.0", "0.10.0", UpdateMinor, true},
		{"0.15.6", "0.15.9", UpdatePatch, true},
		{"0.9.9", "1.0.0", UpdateMajor, true},
		{"9.11", "9.12", UpdateMinor, false},
		{"306", "308", UpdateMajor, false},
		{"10.0.203", "10.0.400", UpdatePatch, false},
		{"1.0.0-rc1", "1.0.0", UpdatePrerelease, false},
		{"2.0.0", "1.9.0", UpdateUnknown, false},
		{"1.0", "1.0", UpdateUnknown, false},
		{"", "1.0", UpdateUnknown, false},
		{"3.6a", "3.8", UpdateUnknown, false},
	} {
		got := ClassifyUpdate(tc.installed, tc.latest)
		if got.Kind != tc.want || got.Unstable != tc.unstable {
			t.Errorf("ClassifyUpdate(%q, %q) = %+v, want %q unstable=%v", tc.installed, tc.latest, got, tc.want, tc.unstable)
		}
	}
}
