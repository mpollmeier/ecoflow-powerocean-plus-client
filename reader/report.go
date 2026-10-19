package main

import (
	"crypto/tls"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"reader/pb"
)

// insecureTLSConfig matches the app's client behaviour (no server cert pinning).
func insecureTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // matches app behaviour
}

// packInfo is one battery pack's latest live snapshot (cmd 7).
type packInfo struct {
	sn      string
	soc     uint32
	realSoc float32
	pwr     float32
	fullCap uint32
}

// liveState accumulates telemetry from the live sources. The device's cmd 33
// reports in fetch replies are a frozen cache (only refreshed while the phone
// app holds the energy-stream switch open), so they are NOT trusted; instead:
//   - cmd 34 detail items (timestamped system samples, every ~10 min)
//   - cmd 1 heartbeat (powers, grid, solar, every ~60 s)
//   - cmd 7 pack heartbeats (per-pack SOC/power, every ~40 s)
//
// When no fresh system sample exists, load/grid/solar/battery are derived from
// the heartbeat and SOC from the packs. Load uses the power balance
// load = solar + grid - battery (validated against the device's own cmd 33).
type liveState struct {
	mu           sync.Mutex
	sn           string
	hbAt         time.Time
	solarStr     []float32 // per-string MPPT power, in string order
	solarInv     float32   // PV inverter power
	grid         float32   // +import / -export
	battery      float32   // +charging / -discharging (ems_bp_power)
	packs        map[string]packInfo
	sysAt        time.Time
	sysReport    *report // newest cmd 33/34 system sample
	printTimer   *time.Timer
	debounce     time.Duration
	lastRendered string
	onPrint      func(*report, []packInfo)
}

// printDebounce coalesces a burst of messages (a fetch reply delivers cmd 1 and
// cmd 7 within milliseconds of each other) into a single complete print.
const printDebounce = 2 * time.Second

func newLiveState(sn string, onPrint func(*report, []packInfo)) *liveState {
	return &liveState{
		sn:       sn,
		packs:    map[string]packInfo{},
		debounce: printDebounce,
		onPrint:  onPrint,
	}
}

// armPrintLocked (re)starts the debounced print timer: one print per quiet
// window, always with the full picture assembled so far.
func (s *liveState) armPrintLocked() {
	if s.printTimer == nil {
		s.printTimer = time.AfterFunc(s.debounce, s.printPending)
		return
	}
	s.printTimer.Reset(s.debounce)
}

// printPending renders and prints the coalesced snapshot (timer callback).
func (s *liveState) printPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, packs, ok := s.snapshotLocked()
	if !ok {
		return
	}
	key := r.contentKey(packs)
	if key == s.lastRendered {
		return
	}
	s.lastRendered = key
	s.onPrint(r, packs)
}

const sysSampleFresh = 15 * time.Minute

// ingestSystem stores a full system sample (cmd 33 push or cmd 34 detail item).
func (s *liveState) ingestSystem(r *report, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sysReport != nil && !at.After(s.sysAt) {
		return // older than what we already have
	}
	s.sysAt = at
	s.sysReport = r
	s.sysReport.time = at
	s.armPrintLocked()
}

// ingestHeartbeat stores the live power measurements (cmd 1).
func (s *liveState) ingestHeartbeat(h *pb.HeartbeatReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hbAt = time.Now()
	s.solarInv = h.GetEmsPvInvPwr()
	var strings []float32
	for _, m := range h.GetMpptHeartBeat() {
		for _, pv := range m.GetMpptPv() {
			strings = append(strings, pv.GetPwr())
		}
	}
	s.solarStr = strings
	if hb := h.GetMeterHeartBeat(); len(hb) > 0 {
		if data := hb[len(hb)-1].GetMeterData(); len(data) > 0 {
			s.grid = data[len(data)-1] // last meter value is the total
		}
	}
	s.battery = h.GetEmsBpPower()
	s.armPrintLocked()
}

// ingestPacks stores live per-pack snapshots (cmd 7, any source address).
func (s *liveState) ingestPacks(packs []*pb.BpStaReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range packs {
		sn := string(p.GetBpSn())
		s.packs[sn] = packInfo{
			sn:      sn,
			soc:     p.GetBpSoc(),
			realSoc: p.GetBpRealSoc(),
			pwr:     p.GetBpPwr(),
			fullCap: p.GetBpFullCap(),
		}
	}
	s.armPrintLocked()
}

// snapshot renders the freshest coherent report and its per-pack detail.
// ok is false when no source has delivered anything yet.
func (s *liveState) snapshot() (r *report, packs []packInfo, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// snapshotLocked is snapshot with s.mu already held. Packs are sorted by SN so
// rendering (and thus print dedup) is deterministic.
func (s *liveState) snapshotLocked() (r *report, packs []packInfo, ok bool) {
	packs = make([]packInfo, 0, len(s.packs))
	for _, p := range s.packs {
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].sn < packs[j].sn })
	if s.sysReport != nil && time.Since(s.sysAt) < sysSampleFresh {
		return s.sysReport, packs, true
	}
	if s.hbAt.IsZero() && len(packs) == 0 {
		return nil, nil, false
	}
	solar := s.solarInv
	for _, p := range s.solarStr {
		solar += p
	}
	var soc uint32
	var haveSoc bool
	if len(packs) > 0 {
		soc = packSoc(packs)
		haveSoc = true
	}
	r = &report{
		time:       s.hbAt,
		batterySOC: soc,
		haveSoc:    haveSoc,
		batteryW:   s.battery,
		solarMPPT:  solar,
		solarPVInv: s.solarInv,
		solarPV1:   stringAt(s.solarStr, 0),
		solarPV2:   stringAt(s.solarStr, 1),
		solarPV3:   stringAt(s.solarStr, 2),
		gridW:      s.grid,
		loadW:      solar + s.grid - s.battery,
		derived:    true,
	}
	return r, packs, true
}

// contentKey is the report's values without timestamps, for deduplication.
func (r *report) contentKey(packs []packInfo) string {
	return fmt.Sprintf("%.0f|%.0f|%.0f|%.0f|%.0f|%.0f|%d|%t",
		r.batteryW, r.solarMPPT, r.solarPV1, r.solarPV2, r.solarPV3,
		r.gridW, r.batterySOC, r.derived) + packDetail(packs)
}

// packSoc is the capacity-weighted pack SOC (plain mean when capacities are
// unknown). The device's system SOC can differ by ±1 due to its own coulomb
// accounting; the last cmd 34 system sample is preferred when fresh.
func packSoc(packs []packInfo) uint32 {
	if len(packs) == 0 {
		return 0
	}
	var sum, weight float32
	for _, p := range packs {
		w := float32(p.fullCap)
		if w == 0 {
			w = 1
		}
		soc := p.realSoc
		if soc == 0 {
			soc = float32(p.soc)
		}
		sum += soc * w
		weight += w
	}
	return uint32(math.Round(float64(sum / weight)))
}

func stringAt(s []float32, i int) float32 {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// report is a telemetry snapshot for display.
type report struct {
	time         time.Time
	batterySOC   uint32
	haveSoc      bool
	batteryW     float32
	solarMPPT    float32
	solarPV1     float32
	solarPV2     float32
	solarPV3     float32
	solarPVInv   float32
	solarOtherPV float32
	loadW        float32
	gridW        float32
	derived      bool // true when assembled from heartbeats/packs
}

func newReport(r *pb.EnergyStreamReport) *report {
	return &report{
		time:         time.Now(),
		batterySOC:   r.GetBpSoc(),
		haveSoc:      true,
		batteryW:     r.GetBpPwr(),
		solarMPPT:    r.GetMpptPwr(),
		solarPV1:     r.GetPv1Pwr(),
		solarPV2:     r.GetPv2Pwr(),
		solarPV3:     r.GetPv3Pwr(),
		solarPVInv:   r.GetPvInvPwr(),
		solarOtherPV: r.GetOtherPvPwr(),
		loadW:        r.GetSysLoadPwr(),
		gridW:        r.GetSysGridPwr(),
	}
}

func newReportFromItem(it *pb.EnergyStreamItem) *report {
	r := newReport(&pb.EnergyStreamReport{
		SysLoadPwr:    it.GetLoadPwr(),
		SysGridPwr:    it.GetGridPwr(),
		MpptPwr:       it.GetMpptPwr(),
		BpPwr:         it.GetBpPwr(),
		BpSoc:         it.GetBpSoc(),
		Pv1Pwr:        it.GetPv1Pwr(),
		Pv2Pwr:        it.GetPv2Pwr(),
		PvInvPwr:      it.GetPvInvPwr(),
		Pv3Pwr:        it.GetPv3Pwr(),
		OtherPvPwr:    it.GetOtherPvPwr(),
		Spain_4GPvPwr: it.GetSpain_4GPvPwr(),
	})
	if ts := it.GetTimestamp(); ts > 0 {
		r.time = time.Unix(int64(ts), 0)
	}
	return r
}

// String renders the report like the app dashboard: battery, solar, load, grid.
// Sign conventions (verified against live power balance, JtS1Sys driver):
//   - grid: positive = importing, negative = exporting
//   - battery: positive = charging, negative = discharging
//     (note: the other driver's JSON protocol uses the opposite battery sign)
func (r *report) String(packs []packInfo) string {
	var b strings.Builder
	kind := ""
	if r.derived {
		kind = " (live heartbeats)"
	}
	fmt.Fprintf(&b, "[%s] PowerOcean Plus telemetry%s\n", r.time.Format("15:04:05"), kind)
	soc := "n/a"
	if r.haveSoc {
		soc = fmt.Sprintf("%d%%", r.batterySOC)
	}
	fmt.Fprintf(&b, "  Battery SOC    : %s%s\n", soc, packDetail(packs))
	fmt.Fprintf(&b, "  Battery        : %s\n", wattsSigned("Battery", r.batteryW))
	fmt.Fprintf(&b, "  Solar (MPPT)   : %.0f W\n", r.solarMPPT)
	fmt.Fprintf(&b, "  Solar (PV1-3)  : %.0f / %.0f / %.0f W\n", r.solarPV1, r.solarPV2, r.solarPV3)
	fmt.Fprintf(&b, "  Solar (PV inv) : %.0f W\n", r.solarPVInv)
	fmt.Fprintf(&b, "  Load           : %.0f W\n", r.loadW)
	fmt.Fprintf(&b, "  Grid           : %s\n", wattsSigned("Grid", r.gridW))
	return b.String()
}

func packDetail(packs []packInfo) string {
	if len(packs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(packs))
	for _, p := range packs {
		parts = append(parts, fmt.Sprintf("%d%%", p.soc))
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
