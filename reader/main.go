// Command reader is a telemetry reader for EcoFlow Power Ocean Plus
// (product type 87, SN prefix R37) via the EcoFlow cloud MQTT API.
//
// It decodes the protobuf envelope (Send_Header_Msg -> EnergyStreamReport)
// and assembles reports from the live push sources. Its only write is the
// energy-stream switch (cmd 97, same as the app's dashboard open) which opens
// the reporting stream for few-seconds cadence — no power-flow setting is
// touched. ECOFLOW_STREAM=0 disables even that, making it subscribe-only.
// See plan-reader.md and docs/auth.md.
package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	env, err := mustEnvVars("ECOFLOW_EMAIL", "ECOFLOW_PASSWORD", "ECOFLOW_DEVICE_SN")
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	log.Println("logging in…")
	creds, mqttCreds, err := loginAndFetchMQTT(env["ECOFLOW_EMAIL"], env["ECOFLOW_PASSWORD"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth failed:", err)
		os.Exit(1)
	}
	log.Printf("user %s, broker %s:%s", creds.UserID, mqttCreds.URL, mqttCreds.Port)

	sn := env["ECOFLOW_DEVICE_SN"]
	state := newLiveState(sn, func(r *report, packs []packInfo) {
		fmt.Print(r.String(packs))
	})
	client, err := connectAndSubscribe(mqttCreds, creds.UserID, sn, state)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mqtt failed:", err)
		os.Exit(1)
	}
	defer client.Disconnect(250)

	// Open the energy-stream reporting switch (the reader's only write; same
	// cmd 97 the app sends on dashboard open): makes the device push cmd 33
	// every few seconds instead of its default ~30-60s cadence. Disable with
	// ECOFLOW_STREAM=0 to keep the reader strictly read-only.
	startStreamSwitch(client, creds.UserID, sn)

	log.Println("waiting for live telemetry (heartbeats, packs, cmd 33/34); Ctrl-C to stop")
	select {}
}
