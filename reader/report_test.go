package main

import "testing"

// Sign conventions verified against live power balance on a real PowerOcean Plus:
// night:  battery -519 W  = load 517 W + grid export 1 W   (negative = discharging)
// midday: solar 1870 W    = load 650 W + battery 1220 W    (positive = charging)
func TestWattsSigned(t *testing.T) {
	for _, tc := range []struct {
		label string
		w     float32
		want  string
	}{
		{"Battery", -519, "-519 W (discharging)"},
		{"Battery", 1220, "1220 W (charging)"},
		{"Battery", 0, "0 W"},
		{"Grid", -3, "-3 W (exporting)"},
		{"Grid", 250, "250 W (importing)"},
		{"Grid", 0, "0 W"},
	} {
		if got := wattsSigned(tc.label, tc.w); got != tc.want {
			t.Errorf("wattsSigned(%q, %v) = %q, want %q", tc.label, tc.w, got, tc.want)
		}
	}
}
