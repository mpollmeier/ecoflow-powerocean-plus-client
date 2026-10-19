package main

import (
	"encoding/hex"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"reader/pb"
)

func TestLiveStateDerivesFromHeartbeats(t *testing.T) {
	var printed []*report
	state := newLiveState("R371TEST", func(r *report, _ []packInfo) {
		printed = append(printed, r)
	})
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

	r, packs, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if !r.derived {
		t.Error("expected derived snapshot")
	}
	if r.batteryW != -500 {
		t.Errorf("battery: got %v, want -500", r.batteryW)
	}
	if r.gridW != -6 {
		t.Errorf("grid: got %v, want -6 (meter total)", r.gridW)
	}
	if r.solarMPPT != 600 { // 200+300 strings + 100 PV inv
		t.Errorf("solar: got %v, want 600", r.solarMPPT)
	}
	// load = solar + grid - battery = 600 + (-6) - (-500) = 1094
	if r.loadW != 1094 {
		t.Errorf("load: got %v, want 1094", r.loadW)
	}
	if got := packSoc(packs); got != 83 { // (84*97 + 82*97) / (97+97) = 83
		t.Errorf("pack soc: got %d, want 83", got)
	}
	if len(packs) != 2 {
		t.Errorf("packs: got %d, want 2", len(packs))
	}
}

func TestLiveStatePrefersFreshSystemSample(t *testing.T) {
	state := newLiveState("R371TEST", func(*report, []packInfo) {})
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-500)})
	state.ingestSystem(newReport(&pb.EnergyStreamReport{BpSoc: 77, BpPwr: -300}), time.Now())

	r, _, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if r.derived {
		t.Error("fresh system sample must not be derived")
	}
	if r.batterySOC != 77 {
		t.Errorf("soc: got %d, want 77 (system sample)", r.batterySOC)
	}
}

func TestLiveStateIgnoresStaleSystemSample(t *testing.T) {
	state := newLiveState("R371TEST", func(*report, []packInfo) {})
	state.ingestSystem(newReport(&pb.EnergyStreamReport{BpSoc: 92, BpPwr: -519}), time.Now().Add(-2*time.Hour))
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-100)})

	r, _, ok := state.snapshot()
	if !ok {
		t.Fatal("no snapshot")
	}
	if !r.derived {
		t.Error("stale system sample must fall back to live derivation")
	}
	if r.batteryW != -100 {
		t.Errorf("battery: got %v, want -100 (live heartbeat)", r.batteryW)
	}
}

func sampleReport() *pb.EnergyStreamReport {
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
	in := sampleReport()
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
	pdata, err := proto.Marshal(sampleReport())
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

func TestDebounceCoalescesBurst(t *testing.T) {
	printed := make(chan *report, 8)
	state := newLiveState("R371TEST", func(r *report, _ []packInfo) { printed <- r })
	state.debounce = 50 * time.Millisecond

	// One fetch-reply burst: heartbeat first, then packs ~ms later.
	state.ingestHeartbeat(&pb.HeartbeatReport{EmsBpPower: proto.Float32(-500)})
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(85), BpRealSoc: proto.Float32(84)},
	})

	select {
	case r := <-printed:
		if !r.haveSoc {
			t.Error("coalesced print must already include SOC (no n/a)")
		}
	case <-time.After(time.Second):
		t.Fatal("no print")
	}
	select {
	case r := <-printed:
		t.Errorf("unexpected second print in same burst: %v", r.batterySOC)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPackOrderDeterministic(t *testing.T) {
	state := newLiveState("R371TEST", func(*report, []packInfo) {})
	// Insert in map-random order; renders must sort by SN.
	state.ingestPacks([]*pb.BpStaReport{
		{BpSn: []byte("packB"), BpSoc: proto.Uint32(82)},
		{BpSn: []byte("packA"), BpSoc: proto.Uint32(85)},
	})
	_, packs, _ := state.snapshot()
	if len(packs) != 2 || packs[0].sn != "packA" || packs[1].sn != "packB" {
		t.Errorf("packs not sorted by SN: %+v", packs)
	}
}

// The stream switch must match the app's captured set byte-for-byte
// (modulo the random seq), or the device may not honour it.
const capturedStreamSwitch = "0a3f0a0208011020186020012801406048615002580170eaf7e990027801800113880101ba0107616e64726f6964ca01105233373153414d504c45444556303031"

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
