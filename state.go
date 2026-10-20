package powerocean

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client/pb"
)

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
	packs        map[string]Pack
	sysAt        time.Time
	sysReport    *Report // newest cmd 33/34 system sample
	printTimer   *time.Timer
	debounce     time.Duration
	lastRendered string
	onReport     func(Report)
}

// reportDebounce coalesces a burst of messages (a fetch reply delivers cmd 1
// and cmd 7 within milliseconds of each other) into a single complete report.
const reportDebounce = 2 * time.Second

func newLiveState(sn string, onReport func(Report)) *liveState {
	return &liveState{
		sn:       sn,
		packs:    map[string]Pack{},
		debounce: reportDebounce,
		onReport: onReport,
	}
}

// armReportLocked (re)starts the debounced report timer: one delivery per
// quiet window, always with the full picture assembled so far.
func (s *liveState) armReportLocked() {
	if s.printTimer == nil {
		s.printTimer = time.AfterFunc(s.debounce, s.deliverPending)
		return
	}
	s.printTimer.Reset(s.debounce)
}

// deliverPending delivers the coalesced snapshot (timer callback).
func (s *liveState) deliverPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.snapshotLocked()
	if !ok {
		return
	}
	key := r.contentKey()
	if key == s.lastRendered {
		return
	}
	s.lastRendered = key
	s.onReport(r)
}

const sysSampleFresh = 15 * time.Minute

// ingestSystem stores a full system sample (cmd 33 push or cmd 34 detail item).
func (s *liveState) ingestSystem(r Report, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sysReport != nil && !at.After(s.sysAt) {
		return // older than what we already have
	}
	s.sysAt = at
	r.Time = at
	s.sysReport = &r
	s.armReportLocked()
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
	s.armReportLocked()
}

// ingestPacks stores live per-pack snapshots (cmd 7, any source address).
func (s *liveState) ingestPacks(packs []*pb.BpStaReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range packs {
		sn := string(p.GetBpSn())
		s.packs[sn] = Pack{
			SN:      sn,
			SOC:     p.GetBpSoc(),
			RealSOC: p.GetBpRealSoc(),
			PowerW:  p.GetBpPwr(),
			FullCap: p.GetBpFullCap(),
		}
	}
	s.armReportLocked()
}

// snapshot renders the freshest coherent report (test/debug helper).
func (s *liveState) snapshot() (Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// snapshotLocked renders the freshest coherent report. Packs are sorted by SN
// so rendering (and thus delivery dedup) is deterministic.
func (s *liveState) snapshotLocked() (Report, bool) {
	if s.sysReport != nil && time.Since(s.sysAt) < sysSampleFresh {
		r := *s.sysReport
		r.Packs = s.sortedPacks()
		return r, true
	}
	if s.hbAt.IsZero() && len(s.packs) == 0 {
		return Report{}, false
	}
	solar := s.solarInv
	for _, p := range s.solarStr {
		solar += p
	}
	packs := s.sortedPacks()
	r := Report{
		Time:       s.hbAt,
		BatteryW:   s.battery,
		SolarMPPT:  solar,
		SolarPVInv: s.solarInv,
		SolarPV1:   stringAt(s.solarStr, 0),
		SolarPV2:   stringAt(s.solarStr, 1),
		SolarPV3:   stringAt(s.solarStr, 2),
		GridW:      s.grid,
		LoadW:      solar + s.grid - s.battery,
		Packs:      packs,
		Derived:    true,
	}
	if len(packs) > 0 {
		r.BatterySOC = packSoc(packs)
		r.HasSOC = true
	}
	return r, true
}

func (s *liveState) sortedPacks() []Pack {
	packs := make([]Pack, 0, len(s.packs))
	for _, p := range s.packs {
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].SN < packs[j].SN })
	return packs
}

// contentKey is the report's values without timestamps, for deduplication.
func (r Report) contentKey() string {
	return fmt.Sprintf("%.0f|%.0f|%.0f|%.0f|%.0f|%.0f|%d|%t",
		r.BatteryW, r.SolarMPPT, r.SolarPV1, r.SolarPV2, r.SolarPV3,
		r.GridW, r.BatterySOC, r.Derived) + packDetail(r.Packs)
}

func stringAt(s []float32, i int) float32 {
	if i < len(s) {
		return s[i]
	}
	return 0
}
