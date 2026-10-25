package powerocean

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client/pb"
)

// The backup-reserve set must map to the app's SYS_BAT_CHG_DSG_SET command
// (cmd 112, pdata = SysBatChgDsgSet{sys_bat_dsg_down_limie}), the same message
// the battery settings screen sends when changing "Backup-Reserve". The field
// is a plain proto3 uint32 (field 2, varint), so 30% encodes as two bytes:
// 0x10 (key) 0x1e (value).
func TestBuildBackupReserveRequest(t *testing.T) {
	raw, seq := buildBackupReserveRequest("R371SAMPLEDEV001", 30)
	if seq == nil {
		t.Fatal("no seq returned")
	}

	headers, err := decodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 {
		t.Fatalf("got %d headers, want 1", len(headers))
	}
	h := headers[0]

	if h.GetCmdId() != int32(pb.EmsCmdId_EMS_CMD_ID_SYS_BAT_CHG_DSG_SET) {
		t.Errorf("cmd_id: got %d, want %d", h.GetCmdId(), pb.EmsCmdId_EMS_CMD_ID_SYS_BAT_CHG_DSG_SET)
	}
	if h.GetCmdFunc() != int32(pb.CmdSets_EMS_CMD_SETS) {
		t.Errorf("cmd_func: got %d, want %d", h.GetCmdFunc(), pb.CmdSets_EMS_CMD_SETS)
	}
	if !isEms(h, pb.EmsCmdId_EMS_CMD_ID_SYS_BAT_CHG_DSG_SET) {
		t.Error("header not recognised as the chg/dsg set command")
	}
	if h.GetSrc() != 32 || h.GetDest() != 96 {
		t.Errorf("src/dest: got %d/%d, want 32/96", h.GetSrc(), h.GetDest())
	}
	if h.GetDataLen() != int32(len(h.GetPdata())) {
		t.Errorf("data_len: got %d, want %d", h.GetDataLen(), len(h.GetPdata()))
	}
	if h.GetSeq() != *seq {
		t.Errorf("seq: got %d, want %d", h.GetSeq(), *seq)
	}

	wantWire := []byte{0x10, 0x1e} // field 2 varint = 30
	if !bytes.Equal(h.GetPdata(), wantWire) {
		t.Errorf("pdata: got %x, want %x", h.GetPdata(), wantWire)
	}

	var set pb.SysBatChgDsgSet
	if err := proto.Unmarshal(h.GetPdata(), &set); err != nil {
		t.Fatal(err)
	}
	if set.GetSysBatDsgDownLimie() != 30 {
		t.Errorf("sys_bat_dsg_down_limie: got %d, want 30", set.GetSysBatDsgDownLimie())
	}
	if set.GetSysBatChgUpLimit() != 0 || set.GetSysBatBackupRatio() != 0 {
		t.Errorf("only sys_bat_dsg_down_limie must be set: %+v", &set)
	}
}
