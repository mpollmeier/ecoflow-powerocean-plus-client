package powerocean

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client/pb"
)

// buildFetchAllRequest encodes the app's "fetch all data" request, captured
// live from the phone app on .../thing/property/get:
//
//	Send_Header_Msg{ msg: [{ src: 32, dest: 32, seq: <ts>, from: "Android",
//	                        src_sn: "", dest_sn: "" }] }
//
// It carries no cmd_set/cmd_id and no pdata — the device answers on
// .../thing/property/get_reply with its cached snapshot (incl. cmd 33 reports).
func buildFetchAllRequest() []byte {
	env := &pb.Send_Header_Msg{Msg: []*pb.Header{{
		Src:    32,
		Dest:   32,
		Seq:    rand.Int31(),
		From:   proto.String("Android"),
		SrcSn:  proto.String(""),
		DestSn: proto.String(""),
	}}}
	raw, err := proto.Marshal(env)
	if err != nil {
		panic(err) // static message, cannot fail
	}
	return raw
}

// publishFetchAll sends the fetch-all request (a pure get, never a set). The
// device answers from its cache of the last pushed state — it does not trigger
// a fresh measurement — so this is only useful to seed state at startup and
// resync after reconnects.
func (c *Client) publishFetchAll(cli mqtt.Client, userID, sn string) error {
	topic := fmt.Sprintf("/app/%s/%s/thing/property/get", userID, sn)
	token := cli.Publish(topic, 0, false, buildFetchAllRequest())
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish fetch-all to %s: %w", topic, err)
	}
	c.log.Debug("published fetch-all", "topic", topic)
	return nil
}

// streamKeepalive matches the app's re-send cadence for the energy-stream
// switch while its dashboard is open.
const streamKeepalive = 10 * time.Second

// buildEMSRequest encodes one EMS set command on .../thing/property/set with
// the field layout the app uses for every set it sends (src 32 → dest 96,
// cmd_func 96, need_ack 1, from "android"). It returns the encoded envelope
// and the random seq stamped into its header, so callers can correlate the
// device's set_reply acknowledgement.
func buildEMSRequest(sn string, cmd pb.EmsCmdId, pdata []byte) ([]byte, *int32) {
	seq := rand.Int31()
	env := &pb.Send_Header_Msg{Msg: []*pb.Header{{
		Pdata:      pdata,
		Src:        32,
		Dest:       96,
		DSrc:       1,
		DDest:      1,
		CmdFunc:    int32(pb.CmdSets_EMS_CMD_SETS),
		CmdId:      int32(cmd),
		DataLen:    int32(len(pdata)),
		NeedAck:    1,
		Seq:        seq,
		ProductId:  1,
		Version:    19,
		PayloadVer: 1,
		From:       proto.String("android"),
		DeviceSn:   proto.String(sn),
	}}}
	raw, err := proto.Marshal(env)
	if err != nil {
		panic(err) // static message, cannot fail
	}
	return raw, &seq
}

// buildStreamSwitchRequest encodes the app's ENERGY_STREAM_SWITCH set
// (cmd 97, pdata = ems_open_energy_stream:1), captured live from the phone app
// on .../thing/property/set. This only opens the telemetry *reporting* stream
// (few-seconds cmd 33 pushes) — it touches no power-flow setting. The switch
// expires server-side unless kept alive, exactly like the app does.
func buildStreamSwitchRequest(sn string) []byte {
	raw, _ := buildEMSRequest(sn, pb.EmsCmdId_EMS_CMD_ID_ENERGY_STREAM_SWITCH,
		[]byte{0x08, 0x01}) // EnergyStreamSwitch{ems_open_energy_stream: 1}
	return raw
}

// buildBackupReserveRequest encodes the SYS_BAT_CHG_DSG_SET command that the
// app's battery settings screen sends: pdata is a SysBatChgDsgSet with only
// sys_bat_dsg_down_limie set — the battery's discharge floor, which the German
// app labels "Backup-Reserve". See docs/protocol.md.
func buildBackupReserveRequest(sn string, percent uint32) ([]byte, *int32) {
	pdata, err := proto.Marshal(&pb.SysBatChgDsgSet{SysBatDsgDownLimie: proto.Uint32(percent)})
	if err != nil {
		panic(err) // static message, cannot fail
	}
	return buildEMSRequest(sn, pb.EmsCmdId_EMS_CMD_ID_SYS_BAT_CHG_DSG_SET, pdata)
}

// startStreamSwitch opens the energy-stream reporting switch and keeps it open
// until ctx is cancelled. This is the client's only write (Config.StreamSwitch).
func (c *Client) startStreamSwitch(ctx context.Context, cli mqtt.Client, userID, sn string) {
	c.log.Info("opening energy-stream switch (the only write this client performs)",
		"keepalive", streamKeepalive)
	topic := fmt.Sprintf("/app/%s/%s/thing/property/set", userID, sn)
	go func() {
		ticker := time.NewTicker(streamKeepalive)
		defer ticker.Stop()
		for {
			token := cli.Publish(topic, 0, false, buildStreamSwitchRequest(sn))
			token.Wait()
			if err := token.Error(); err != nil {
				c.log.Error("stream switch publish failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
