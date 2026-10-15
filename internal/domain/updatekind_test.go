package domain

import "testing"

func TestClassifyUpdate(t *testing.T) {
	for _, tc := range []struct {
		installed, latest string
		want              UpdateKind
	}{
		{"4.0.0", "4.0.0_1", UpdateRevision},
		{"4.2.3", "4.4.4", UpdateMinor},
		{"2.6.9", "4.0.23", UpdateMajor},
		{"1.18.2_1", "1.18.3", UpdatePatch},
		{"5.3.15", "5.3.20", UpdatePatch},
		{"2.4.28,f3f5cec400aa", "2.4.29,0011bbcc", UpdatePatch},
		{"1.1.2156,4e9bb23e", "1.1.2156,5aa00000", UpdateRevision},
		{"2026-08-13", "2026-09-01", UpdateUnknown},
		{"2026.7.22", "2026.8.1", UpdateUnknown},
		{"v4.26.0", "v4.26.1", UpdatePatch},
		{"0.1.12", "0.2.1", UpdateMajor},
		{"0.3.0", "0.3.3", UpdateMinor},
		{"9.11", "9.12", UpdateMinor},
		{"10.0.203", "10.0.400", UpdatePatch},
		{"1.0.0-rc1", "1.0.0", UpdatePatch},
		{"1.3.13", "1.4.3", UpdateMinor},
		{"2.0.0", "1.9.0", UpdateUnknown},
		{"1.0", "1.0", UpdateUnknown},
		{"", "1.0", UpdateUnknown},
		{"latest", "1.0", UpdateUnknown},
	} {
		if got := ClassifyUpdate(tc.installed, tc.latest); got != tc.want {
			t.Errorf("ClassifyUpdate(%q, %q) = %q, want %q", tc.installed, tc.latest, got, tc.want)
		}
	}
}
