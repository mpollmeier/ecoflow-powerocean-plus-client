package powerocean

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Pack is one battery pack's latest live snapshot.
type Pack struct {
	SN      string
	SOC     uint32  // displayed state of charge, %
	RealSOC float32 // coulomb-counted SOC, %
	PowerW  float32 // + charging, - discharging
	FullCap uint32  // full capacity (device units)
}

// Report is one telemetry snapshot of the whole system.
//
// Sign conventions (verified against live power balance on the JtS1Sys
// driver): GridW is positive when importing, negative when exporting; BatteryW
// is positive when charging, negative when discharging. (The other EcoFlow
// driver's JSON protocol uses the opposite battery sign.)
type Report struct {
	Time time.Time

	BatterySOC uint32 // system SOC, capacity-weighted across packs
	HasSOC     bool   // false while no pack has reported yet
	Packs      []Pack // per-pack detail, sorted by SN

	BatteryW     float32
	SolarMPPT    float32 // total solar (MPPT + PV inverter)
	SolarPV1     float32
	SolarPV2     float32
	SolarPV3     float32
	SolarPVInv   float32
	SolarOtherPV float32
	LoadW        float32
	GridW        float32

	// Battery setters, reported by the device in EmsChangeReport (cmd 8).
	// HasLimits is true once these have been received and reflect the current
	// device settings.
	//
	//	BackupReserve = sys_bat_dsg_down_limit — the discharge floor, labelled
	//	                "Backup-Reserve" in the German app.
	//	ChargeLimit   = sys_bat_chg_up_limit — "Ladegrenze".
	//	BackupRatio   = sys_bat_backup_ratio — a separate (AI-mode) ratio.
	BackupReserve uint32
	ChargeLimit   uint32
	BackupRatio   uint32
	HasLimits     bool

	// Derived is true when the report was assembled from live heartbeats and
	// pack updates rather than a full system sample pushed by the device.
	// Derived reports compute LoadW from the power balance
	// load = solar + grid - battery.
	Derived bool
}

// String renders the report like the app dashboard: battery, solar, load, grid.
func (r Report) String() string {
	var b strings.Builder
	kind := ""
	if r.Derived {
		kind = " (live heartbeats)"
	}
	fmt.Fprintf(&b, "[%s] PowerOcean Plus telemetry%s\n", r.Time.Format("15:04:05"), kind)
	soc := "n/a"
	if r.HasSOC {
		soc = fmt.Sprintf("%d%%", r.BatterySOC)
	}
	fmt.Fprintf(&b, "  Battery SOC    : %s%s\n", soc, packDetail(r.Packs))
	fmt.Fprintf(&b, "  Battery        : %s\n", wattsSigned("Battery", r.BatteryW))
	fmt.Fprintf(&b, "  Solar (MPPT)   : %.0f W\n", r.SolarMPPT)
	fmt.Fprintf(&b, "  Solar (PV1-3)  : %.0f / %.0f / %.0f W\n", r.SolarPV1, r.SolarPV2, r.SolarPV3)
	fmt.Fprintf(&b, "  Solar (PV inv) : %.0f W\n", r.SolarPVInv)
	fmt.Fprintf(&b, "  Load           : %.0f W\n", r.LoadW)
	fmt.Fprintf(&b, "  Grid           : %s\n", wattsSigned("Grid", r.GridW))
	if r.HasLimits {
		fmt.Fprintf(&b, "  Settings       : backup reserve %d%%, charge limit %d%%\n",
			r.BackupReserve, r.ChargeLimit)
	}
	return b.String()
}

func packDetail(packs []Pack) string {
	if len(packs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(packs))
	for _, p := range packs {
		parts = append(parts, fmt.Sprintf("%d%%", p.SOC))
	}
	return fmt.Sprintf("  (packs %s)", strings.Join(parts, " / "))
}

func wattsSigned(label string, w float32) string {
	switch {
	case label == "Grid" && w < 0:
		return fmt.Sprintf("%.0f W (exporting)", w)
	case label == "Grid" && w > 0:
		return fmt.Sprintf("%.0f W (importing)", w)
	case label == "Battery" && w > 0:
		return fmt.Sprintf("%.0f W (charging)", w)
	case label == "Battery" && w < 0:
		return fmt.Sprintf("%.0f W (discharging)", w)
	default:
		return fmt.Sprintf("%.0f W", w)
	}
}

// packSoc is the capacity-weighted pack SOC (plain mean when capacities are
// unknown), used when no fresh full-system SOC is available. The system BMS
// estimate shown by the app can legitimately differ from a pack average.
func packSoc(packs []Pack) uint32 {
	if len(packs) == 0 {
		return 0
	}
	var sum, weight float32
	for _, p := range packs {
		w := float32(p.FullCap)
		if w == 0 {
			w = 1
		}
		soc := p.RealSOC
		if soc == 0 {
			soc = float32(p.SOC)
		}
		sum += soc * w
		weight += w
	}
	return uint32(math.Round(float64(sum / weight)))
}
