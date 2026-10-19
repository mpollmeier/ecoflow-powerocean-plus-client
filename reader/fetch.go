package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"os"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"reader/pb"
)

// buildFetchAllRequest encodes the app's "fetch all data" request, captured
// live from the phone app on .../thing/property/get:
//
//	Send_Header_Msg{ msg: [{ src: 32, dest: 32, seq: <ts>, from: "Android",
//	                        src_sn: "", dest_sn: "" }] }
//
// It carries no cmd_set/cmd_id and no pdata — the device answers on
// .../thing/property/get_reply with current values (incl. cmd 33 reports).
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
// resync after reconnects. Cadence of real telemetry is the device's own push
// schedule (~30-60s).
func publishFetchAll(c mqtt.Client, userID, sn string) error {
	topic := fmt.Sprintf("/app/%s/%s/thing/property/get", userID, sn)
	token := c.Publish(topic, 0, false, buildFetchAllRequest())
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("publish fetch-all to %s: %w", topic, err)
	}
	if isDebugEnabled() {
		log.Printf("published fetch-all request to %s (read-only get)", topic)
	}
	return nil
}

// logReportDebug dumps the envelope metadata and raw pdata of a telemetry
// header so sparse/multi-pack payloads can be diagnosed. Enabled with
// ECOFLOW_DEBUG=1; also see the 32-byte full-field cmd 33 note in
// plan-reader.md (stale cached snapshot served in fetch replies).
func isDebugEnabled() bool { return os.Getenv("ECOFLOW_DEBUG") != "" }

func logReportDebug(h *pb.Header, what string) {
	if !isDebugEnabled() {
		return
	}
	log.Printf("%s debug: enc_type=%d seq=%d module_sn=%q src_sn=%q dest_sn=%q pdata(%d)=%s",
		what, h.GetEncType(), h.GetSeq(), h.GetModuleSn(), h.GetSrcSn(), h.GetDestSn(),
		len(h.GetPdata()), hex.EncodeToString(h.GetPdata()))
}
