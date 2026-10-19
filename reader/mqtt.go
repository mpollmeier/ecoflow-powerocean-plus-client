package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// newClientID builds a unique client id in the app's convention.
func newClientID(userID string) string {
	return fmt.Sprintf("ANDROID_%08X-%04X-%04X-%04X-%012X_%s",
		rand.Uint32(), rand.Uint32()&0xFFFF, rand.Uint32()&0xFFFF,
		rand.Uint32()&0xFFFF, rand.Uint64()&0xFFFFFFFFFFFF, userID)
}

// devicePropertyTopic is the topic carrying the live push stream for a device.
func devicePropertyTopic(sn string) string {
	return "/app/device/property/" + sn
}

// deviceTopics lists the topics carrying Send_Header_Msg telemetry for a device
// (see plan-reader.md / nv.h message handling in the APK).
func deviceTopics(userID, sn string) []string {
	return []string{
		devicePropertyTopic(sn),
		fmt.Sprintf("/app/%s/%s/thing/property/get_reply", userID, sn),
	}
}

// requestTopics are the app's outbound request topics. Subscribing (read-only)
// lets us observe exactly what the app publishes — e.g. when the phone app is
// opened — without publishing anything ourselves.
func requestTopics(userID, sn string) []string {
	return []string{
		fmt.Sprintf("/app/%s/%s/thing/property/get", userID, sn),
		fmt.Sprintf("/app/%s/%s/thing/property/set", userID, sn),
		fmt.Sprintf("/app/%s/1/biz/data/request", userID),
		fmt.Sprintf("/app/%s/2/biz/data/request", userID),
	}
}

// connectAndSubscribe opens the TLS MQTT connection and subscribes to the
// device's telemetry topics. Read-only: no publishes on any set/request topic
// except the fetch-all get. It returns once all subscriptions are active, so
// callers can safely trigger a fetch without racing the subscriptions.
func connectAndSubscribe(creds mqttCredentials, userID, sn string, state *liveState) (mqtt.Client, error) {
	subscribed := make(chan struct{})
	var subscribeOnce sync.Once
	broker := fmt.Sprintf("%s://%s:%s", creds.Protocol, creds.URL, creds.Port)
	client := mqtt.NewClient(mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(newClientID(userID)).
		SetUsername(creds.CertificateAccount).
		SetPassword(creds.CertificatePassword).
		SetKeepAlive(60 * time.Second).
		SetConnectTimeout(30 * time.Second).
		SetAutoReconnect(true).
		SetTLSConfig(insecureTLSConfig()).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("connected to %s", broker)
			topics := append(deviceTopics(userID, sn), requestTopics(userID, sn)...)
			for i, topic := range topics {
				handler := func(_ mqtt.Client, msg mqtt.Message) {
					handleMessage(msg, state)
				}
				if i >= len(deviceTopics(userID, sn)) {
					handler = func(_ mqtt.Client, msg mqtt.Message) {
						handleRequestMessage(msg)
					}
				}
				token := c.Subscribe(topic, 0, handler)
				token.Wait()
				if err := token.Error(); err != nil {
					log.Printf("subscribe %s: %v", topic, err)
					continue
				}
				log.Printf("subscribed %s", topic)
			}
			subscribeOnce.Do(func() { close(subscribed) })
			// Seed/resync from the device's cached state. Note: the device
			// answers from its last pushed measurement — the fetch does not
			// trigger a fresh one. Repeats on every reconnect.
			if err := publishFetchAll(c, userID, sn); err != nil {
				log.Printf("fetch-all: %v", err)
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("connection lost: %v (auto-reconnect will resubscribe)", err)
		}))

	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("mqtt connect %s: %w", broker, err)
	}
	<-subscribed // all subscriptions active before any fetch can be published
	return client, nil
}

// handleRequestMessage watches the app's outbound request topics (read-only
// sniff; we publish nothing but the fetch-all get). Logging is debug-only:
// the broker echoes our own fetch-all back here, which is just noise.
func handleRequestMessage(msg mqtt.Message) {
	payload := msg.Payload()
	if !isDebugEnabled() {
		return
	}
	log.Printf("REQUEST on %s: %d bytes hex=%s", msg.Topic(), len(payload), hex.EncodeToString(payload))
	if headers, err := decodeEnvelope(payload); err == nil {
		for _, h := range headers {
			log.Printf("  decoded: src=%d dst=%d cmd_set=%d cmd_id=%d seq=%d need_ack=%d pdata=%d bytes hex=%s",
				h.GetSrc(), h.GetDest(), h.GetCmdFunc(), h.GetCmdId(), h.GetSeq(),
				h.GetNeedAck(), len(h.GetPdata()), hex.EncodeToString(h.GetPdata()))
		}
	} else {
		log.Printf("  not a Send_Header_Msg: %v", err)
	}
}

// handleMessage decodes a raw MQTT payload and feeds live telemetry sources
// into the state assembler.
func handleMessage(msg mqtt.Message, state *liveState) {
	headers, err := decodeEnvelope(msg.Payload())
	if err != nil {
		log.Printf("envelope decode on %s: %v", msg.Topic(), err)
		return
	}
	for _, h := range headers {
		if code := h.GetCode(); code == "-1" || code == "-2" {
			log.Printf("device %s on %s", map[string]string{"-1": "not bound", "-2": "offline"}[code], msg.Topic())
			return
		}
		if isDebugEnabled() {
			log.Printf("msg on %s: src=%d cmd_set=%d cmd_id=%d payload=%d bytes hex=%s",
				msg.Topic(), h.GetSrc(), h.GetCmdFunc(), h.GetCmdId(),
				len(h.GetPdata()), hex.EncodeToString(h.GetPdata()))
		}
		switch {
		case isHeartbeat(h):
			hb, err := parseHeartbeat(h)
			if err != nil {
				log.Printf("heartbeat parse: %v", err)
				continue
			}
			state.ingestHeartbeat(hb)
		case isBpHeartbeat(h):
			packs, err := parseBpHeartbeat(h)
			if err != nil {
				log.Printf("bp heartbeat parse: %v", err)
				continue
			}
			state.ingestPacks(packs)
		case isEnergyStreamReport(h):
			// A cmd 33 in a get_reply burst is the device's frozen cache (only
			// refreshed while the phone app holds the stream switch open) —
			// never trust it. Push-topic reports are the live stream.
			if msg.Topic() != devicePropertyTopic(state.sn) {
				logReportDebug(h, "cmd33 cached (ignored)")
				continue
			}
			logReportDebug(h, "cmd33 EnergyStreamReport")
			esr, err := parseEnergyStreamReport(h)
			if err != nil {
				log.Printf("energy stream report parse: %v", err)
				continue
			}
			state.ingestSystem(newReport(esr), time.Now())
		case isEnergyStreamDetail(h):
			logReportDebug(h, "cmd34 EnergyStreamDetail")
			item, err := parseEnergyStreamDetail(h)
			if err != nil {
				log.Printf("energy stream detail parse: %v", err)
				continue
			}
			at := time.Now()
			if ts := item.GetTimestamp(); ts > 0 {
				at = time.Unix(int64(ts), 0)
			}
			state.ingestSystem(newReportFromItem(item), at)
		}
	}
}
