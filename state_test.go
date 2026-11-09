package powerocean

import (
	"encoding/hex"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client/pb"
)

func TestLiveStateDerivesFromHeartbeats(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	state.ingestHeartbeat(&pb.HeartbeatReport{
		EmsBpPower:     proto.Float32(-500), // discharging 500 W
		EmsPvInvPwr:    proto.Float32(100),
		MpptHeartBeat:  []*pb.MpptStaReport{{MpptPv: []*pb.MpptPVInfo{{Pwr: 200}, {Pwr: 300}}}},
		MeterHeartBeat: []*pb.MeterStaReport{{MeterData: []float32{-56, 79, -29, -6}}}, // total = -6
	})
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(85), BpRealSoc: proto.Float32(84), BpPwr: proto.Float32(-250), BpFullCap: proto.Uint32(97000)},
		{BpSn: []byte("packB"), BpSoc: proto.Uint32(82), BpRealSoc: proto.Float32(82), BpPwr: proto.Float32(-250), BpFullCap: proto.Uint32(97000)},
	})

	r, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if !r.Derived {
		t.Error("expected derived snapshot")
	}
	if r.BatteryW != -500 {
		t.Errorf("battery: got %v, want -500", r.BatteryW)
	}
	if r.GridW != -6 {
		t.Errorf("grid: got %v, want -6 (meter total)", r.GridW)
	}
	if r.SolarMPPT != 600 { // 200+300 strings + 100 PV inv
		t.Errorf("solar: got %v, want 600", r.SolarMPPT)
	}
	// load = solar + grid - battery = 600 + (-6) - (-500) = 1094
	if r.LoadW != 1094 {
		t.Errorf("load: got %v, want 1094", r.LoadW)
	}
	if !r.HasSOC || r.BatterySOC != 83 {
		t.Errorf("soc: got %d (has=%v), want 83", r.BatterySOC, r.HasSOC)
	}
	if len(r.Packs) != 2 {
		t.Errorf("packs: got %d, want 2", len(r.Packs))
	}
}

func TestLiveStatePrefersFreshSystemSample(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-500)})
	state.ingestSystem(reportFromESR(&pb.EnergyStreamReport{BpSoc: 77, BpPwr: -300}, time.Now()), time.Now())

	r, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if r.Derived {
		t.Error("fresh system sample must not be derived")
	}
	if r.BatterySOC != 77 {
		t.Errorf("soc: got %d, want 77 (system sample)", r.BatterySOC)
	}
}

func TestLiveStatePrefersSystemSOCOverPackAverage(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	state.ingestSystem(reportFromESR(&pb.EnergyStreamReport{BpSoc: 31, BpPwr: -300}, time.Now()), time.Now())
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(35), BpRealSoc: proto.Float32(35), BpFullCap: proto.Uint32(97000)},
		{BpSn: []byte("packB"), BpSoc: proto.Uint32(33), BpRealSoc: proto.Float32(33), BpFullCap: proto.Uint32(97000)},
	})

	r, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if r.Derived {
		t.Error("fresh system power fields must still take precedence")
	}
	if r.BatteryW != -300 {
		t.Errorf("battery: got %v, want -300 (system sample)", r.BatteryW)
	}
	if !r.HasSOC || r.BatterySOC != 31 {
		t.Errorf("soc: got %d (has=%v), want 31 (system BMS estimate)", r.BatterySOC, r.HasSOC)
	}
	if len(r.Packs) != 2 {
		t.Errorf("packs: got %d, want 2", len(r.Packs))
	}
}

func TestLiveStateChangeReportStoresLimits(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-100)})
	state.ingestChangeReport(&pb.EmsChangeReport{
		SysBatDsgDownLimit: proto.Uint32(3),
		SysBatChgUpLimit:   proto.Uint32(90),
		SysBatBackupRatio:  proto.Uint32(20),
	})

	r, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if !r.HasLimits {
		t.Fatal("limits not present")
	}
	if r.BackupReserve != 3 || r.ChargeLimit != 90 || r.BackupRatio != 20 {
		t.Errorf("limits: got %d/%d/%d, want 3/90/20", r.BackupReserve, r.ChargeLimit, r.BackupRatio)
	}
}

func TestLiveStateIgnoresStaleSystemSample(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	stale := reportFromESR(&pb.EnergyStreamReport{BpSoc: 92, BpPwr: -519}, time.Now().Add(-2*time.Hour))
	state.ingestSystem(stale, time.Now().Add(-2*time.Hour))
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-100)})

	r, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if !r.Derived {
		t.Error("stale system sample must fall back to live derivation")
	}
	if r.BatteryW != -100 {
		t.Errorf("battery: got %v, want -100 (live heartbeat)", r.BatteryW)
	}
}

func TestDebounceCoalescesBurst(t *testing.T) {
	printed := make(chan Report, 8)
	state := newLiveState("R371TEST", func(r Report) { printed <- r })
	state.debounce = 50 * time.Millisecond

	// One fetch-reply burst: heartbeat first, then packs ~ms later.
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-500)})
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(85), BpRealSoc: proto.Float32(84)},
	})

	select {
	case r := <-printed:
		if !r.HasSOC {
			t.Error("coalesced report must already include SOC (no n/a)")
		}
	case <-time.After(time.Second):
		t.Fatal("no report")
	}
	select {
	case r := <-printed:
		t.Errorf("unexpected second report in same burst: %v", r.BatterySOC)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPackOrderDeterministic(t *testing.T) {
	state := newLiveState("R371TEST", func(Report) {})
	// Insert in map-random order; reports must sort by SN.
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packB"), BpSoc: proto.Uint32(82)},
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(85)},
	})
	r, _ := state.snapshot()
	if len(r.Packs) != 2 || r.Packs[0].SN != "packA" || r.Packs[1].SN != "packB" {
		t.Errorf("packs not sorted by SN: %+v", r.Packs)
	}
}

func sampleESR() *pb.EnergyStreamReport {
	return &pb.EnergyStreamReport{
		SysLoadPwr:    2150.5,
		SysGridPwr:    -800,
		MpptPwr:       3200,
		BpPwr:         -1200,
		BpSoc:         55,
		Pv1Pwr:        1500,
		Pv2Pwr:        1700,
		PvInvPwr:      640,
		Pv3Pwr:        0,
		OtherPvPwr:    12.25,
		Spain_4GPvPwr: 1,
	}
}

// Step 1 acceptance: EnergyStreamReport round-trips through the generated
// bindings with the wire schema reconstructed from the APK.
func TestEnergyStreamReportRoundTrip(t *testing.T) {
	in := sampleESR()
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out pb.EnergyStreamReport
	if err := proto.Unmarshal(wire, &out); err != nil {
		t.Fatal(err)
	}
	if out.GetBpSoc() != 55 || out.GetSysLoadPwr() != 2150.5 || out.GetSysGridPwr() != -800 {
		t.Errorf("round trip mismatch: %+v", &out)
	}
}

func TestEnvelopeXORDecode(t *testing.T) {
	pdata, err := proto.Marshal(sampleESR())
	if err != nil {
		t.Fatal(err)
	}
	const seq = 0x5A
	obfuscated := xorSeq(pdata, seq)

	env := &pb.Send_Header_Msg{Msg: []*pb.Header{{
		Pdata:    obfuscated,
		Src:      int32(pb.CmdSets_EMS_CMD_SETS),
		Dest:     33,
		EncType:  1,
		CmdFunc:  int32(pb.CmdSets_EMS_CMD_SETS),
		CmdId:    int32(pb.EmsCmdId_EMS_CMD_ID_ENERGY_STREAM_REPORT),
		Seq:      seq,
		ModuleSn: proto.String("R371SAMPLEDEV001"),
	}}}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	headers, err := decodeEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 {
		t.Fatalf("got %d headers, want 1", len(headers))
	}
	h := headers[0]
	if !isEnergyStreamReport(h) {
		t.Fatalf("header not recognised as energy stream report: %+v", h)
	}
	report, err := parseEnergyStreamReport(h)
	if err != nil {
		t.Fatal(err)
	}
	if report.GetBpSoc() != 55 || report.GetSysLoadPwr() != 2150.5 {
		t.Errorf("decoded report mismatch: %+v", report)
	}
}

// The fetch-all must match the app's captured request byte-for-byte
// (modulo the random seq). The app capture (device_sn omitted, empty sns).
const capturedFetchAll = "0a1a1020182070ebf7e99002ba0107416e64726f6964d20100da0100"

// The stream switch must match the app's captured set byte-for-byte
// (modulo the random seq), or the device may not honour it. The capture's
// device_sn has been replaced with a placeholder of the same length.
const capturedStreamSwitch = "0a3f0a0208011020186020012801406048615002580170eaf7e990027801800113880101ba0107616e64726f6964ca01105233373153414d504c45444556303031"

func TestFetchAllMatchesAppCapture(t *testing.T) {
	captured, err := hex.DecodeString(capturedFetchAll)
	if err != nil {
		t.Fatal(err)
	}
	var want, got pb.Send_Header_Msg
	if err := proto.Unmarshal(captured, &want); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(buildFetchAllRequest(), &got); err != nil {
		t.Fatal(err)
	}
	want.GetMsg()[0].Seq = 0
	got.GetMsg()[0].Seq = 0
	if !proto.Equal(&want, &got) {
		t.Errorf("fetch-all mismatch:\n got %v\nwant %v", &got, &want)
	}
}

func TestStreamSwitchMatchesAppCapture(t *testing.T) {
	captured, err := hex.DecodeString(capturedStreamSwitch)
	if err != nil {
		t.Fatal(err)
	}
	var want, got pb.Send_Header_Msg
	if err := proto.Unmarshal(captured, &want); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(buildStreamSwitchRequest("R371SAMPLEDEV001"), &got); err != nil {
		t.Fatal(err)
	}
	want.GetMsg()[0].Seq = 0
	got.GetMsg()[0].Seq = 0
	if !proto.Equal(&want, &got) {
		t.Errorf("stream switch mismatch:\n got %v\nwant %v", &got, &want)
	}
}
