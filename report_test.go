package powerocean

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

func TestPackSocCapacityWeighted(t *testing.T) {
	packs := []Pack{
		{SN: "packA", SOC: 85, RealSOC: 84, FullCap: 97000},
		{SN: "packB", SOC: 82, RealSOC: 82, FullCap: 97000},
	}
	// (84*97000 + 82*97000) / (97000+97000) = 83
	if got := packSoc(packs); got != 83 {
		t.Errorf("packSoc = %d, want 83", got)
	}
	if got := packSoc(nil); got != 0 {
		t.Errorf("packSoc(nil) = %d, want 0", got)
	}
}
