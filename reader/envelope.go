package main

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"reader/pb"
)

// decodeEnvelope parses a raw MQTT payload as Send_Header_Msg and returns its
// headers with any XOR obfuscation undone. Encrypted pdata (enc_type == 1) is
// XOR'd with the low byte of seq, mirroring com.ecoflow.base.utils.q.A.
func decodeEnvelope(payload []byte) ([]*pb.Header, error) {
	var msg pb.Send_Header_Msg
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return nil, fmt.Errorf("Send_Header_Msg: %w", err)
	}
	headers := msg.GetMsg()
	for _, h := range headers {
		if h.GetEncType() == 1 {
			h.Pdata = xorSeq(h.GetPdata(), h.GetSeq())
		}
	}
	return headers, nil
}

func xorSeq(pdata []byte, seq int32) []byte {
	key := byte(seq)
	out := make([]byte, len(pdata))
	for i, b := range pdata {
		out[i] = b ^ key
	}
	return out
}

// EMS command dispatch (cmd_set 96). The device sources reports from several
// addresses (src 96 = EMS, src 53 = IOT relay), so only cmd_set/cmd_id decide.
func isEms(h *pb.Header, id pb.EmsCmdId) bool {
	return h.GetCmdFunc() == int32(pb.CmdSets_EMS_CMD_SETS) &&
		h.GetCmdId() == int32(id)
}

func isEnergyStreamReport(h *pb.Header) bool {
	return isEms(h, pb.EmsCmdId_EMS_CMD_ID_ENERGY_STREAM_REPORT)
}

func isEnergyStreamDetail(h *pb.Header) bool {
	return isEms(h, pb.EmsCmdId_EMS_CMD_ID_ENERGY_STREAM_DETAIL)
}

func isBpHeartbeat(h *pb.Header) bool {
	return isEms(h, pb.EmsCmdId_EMS_CMD_ID_BP_HEARTBEAT_REPORT)
}

func isHeartbeat(h *pb.Header) bool {
	return isEms(h, pb.EmsCmdId_EMS_CMD_ID_HEARTBEAT_REPORT)
}

func parseEnergyStreamReport(h *pb.Header) (*pb.EnergyStreamReport, error) {
	var r pb.EnergyStreamReport
	if err := proto.Unmarshal(h.GetPdata(), &r); err != nil {
		return nil, fmt.Errorf("EnergyStreamReport: %w", err)
	}
	return &r, nil
}

// parseEnergyStreamDetail returns the newest sample of an EnergyStreamDetail
// by its device timestamp.
func parseEnergyStreamDetail(h *pb.Header) (*pb.EnergyStreamItem, error) {
	var d pb.EnergyStreamDetail
	if err := proto.Unmarshal(h.GetPdata(), &d); err != nil {
		return nil, fmt.Errorf("EnergyStreamDetail: %w", err)
	}
	items := d.GetSysEnergyStream()
	if len(items) == 0 {
		return nil, fmt.Errorf("EnergyStreamDetail: no samples")
	}
	newest := items[0]
	for _, it := range items[1:] {
		if it.GetTimestamp() > newest.GetTimestamp() {
			newest = it
		}
	}
	return newest, nil
}

// parseBpHeartbeat returns the per-pack battery snapshots (cmd 7).
func parseBpHeartbeat(h *pb.Header) ([]*pb.BpStaReport, error) {
	var r pb.BpHeartbeatReport
	if err := proto.Unmarshal(h.GetPdata(), &r); err != nil {
		return nil, fmt.Errorf("BpHeartbeatReport: %w", err)
	}
	return r.GetBpHeartBeat(), nil
}

func parseHeartbeat(h *pb.Header) (*pb.HeartbeatReport, error) {
	var r pb.HeartbeatReport
	if err := proto.Unmarshal(h.GetPdata(), &r); err != nil {
		return nil, fmt.Errorf("HeartbeatReport: %w", err)
	}
	return &r, nil
}
