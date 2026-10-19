package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"reader/pb"
)

// streamKeepalive matches the app's re-send cadence for the energy-stream
// switch while its dashboard is open.
const streamKeepalive = 10 * time.Second

// buildStreamSwitchRequest encodes the app's ENERGY_STREAM_SWITCH set
// (cmd 97, pdata = ems_open_energy_stream:1), captured live from the phone app
// on .../thing/property/set. This only opens the telemetry *reporting* stream
// (few-seconds cmd 33 pushes) — it touches no power-flow setting. The switch
// expires server-side unless kept alive, exactly like the app does.
func buildStreamSwitchRequest(sn string) []byte {
	env := &pb.Send_Header_Msg{Msg: []*pb.Header{{
		Pdata:      []byte{0x08, 0x01}, // EnergyStreamSwitch{ems_open_energy_stream: 1}
		Src:        32,
		Dest:       96,
		DSrc:       1,
		DDest:      1,
		CmdFunc:    int32(pb.CmdSets_EMS_CMD_SETS),
		CmdId:      int32(pb.EmsCmdId_EMS_CMD_ID_ENERGY_STREAM_SWITCH),
		DataLen:    2,
		NeedAck:    1,
		Seq:        rand.Int31(),
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
	return raw
}

// startStreamSwitch opens the energy-stream reporting switch and keeps it open
// (the reader's only write). ECOFLOW_STREAM=0 disables it, falling back to the
// device's slower push cadence.
func startStreamSwitch(c mqtt.Client, userID, sn string) {
	if os.Getenv("ECOFLOW_STREAM") == "0" {
		log.Println("energy-stream switch disabled (ECOFLOW_STREAM=0); expect 30-60s cadence")
		return
	}
	go func() {
		topic := fmt.Sprintf("/app/%s/%s/thing/property/set", userID, sn)
		for {
			token := c.Publish(topic, 0, false, buildStreamSwitchRequest(sn))
			token.Wait()
			if err := token.Error(); err != nil {
				log.Printf("stream switch: %v", err)
			}
			time.Sleep(streamKeepalive)
		}
	}()
}
