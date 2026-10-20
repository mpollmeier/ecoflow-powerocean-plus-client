package powerocean

import (
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// insecureTLSConfig matches the app's client behaviour (no server cert pinning).
func insecureTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} //nolint:gosec // matches app behaviour
}

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

// deviceTopics lists the topics carrying Send_Header_Msg telemetry for a device.
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
// device's telemetry topics. It returns once all subscriptions are active, so
// the caller can trigger a fetch without racing the subscriptions.
func (c *Client) connectAndSubscribe(creds mqttCredentials, userID, sn string) (mqtt.Client, error) {
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
		SetOnConnectHandler(func(cli mqtt.Client) {
			c.log.Info("connected", "broker", broker)
			topics := append(deviceTopics(userID, sn), requestTopics(userID, sn)...)
			for i, topic := range topics {
				handler := func(_ mqtt.Client, msg mqtt.Message) {
					c.handleMessage(msg)
				}
				if i >= len(deviceTopics(userID, sn)) {
					handler = func(_ mqtt.Client, msg mqtt.Message) {
						c.handleRequestMessage(msg)
					}
				}
				token := cli.Subscribe(topic, 0, handler)
				token.Wait()
				if err := token.Error(); err != nil {
					c.log.Error("subscribe failed", "topic", topic, "err", err)
					continue
				}
				c.log.Debug("subscribed", "topic", topic)
			}
			subscribeOnce.Do(func() { close(subscribed) })
			// Seed/resync from the device's cached state. Note: the device
			// answers from its last pushed measurement — the fetch does not
			// trigger a fresh one. Repeats on every reconnect.
			if err := c.publishFetchAll(cli, userID, sn); err != nil {
				c.log.Error("fetch-all failed", "err", err)
			}
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			c.log.Warn("connection lost (auto-reconnect will resubscribe)", "err", err)
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
// sniff). DebugLog only: the broker echoes our own fetch-all back here, which
// is just noise.
func (c *Client) handleRequestMessage(msg mqtt.Message) {
	payload := msg.Payload()
	if !c.cfg.DebugLog {
		return
	}
	c.log.Debug("request sniff", "topic", msg.Topic(), "bytes", len(payload),
		"hex", hex.EncodeToString(payload))
	if headers, err := decodeEnvelope(payload); err == nil {
		for _, h := range headers {
			c.log.Debug("request decoded", "src", h.GetSrc(), "dst", h.GetDest(),
				"cmd_set", h.GetCmdFunc(), "cmd_id", h.GetCmdId(), "seq", h.GetSeq(),
				"pdata", hex.EncodeToString(h.GetPdata()))
		}
	}
}

// handleMessage decodes a raw MQTT payload and feeds live telemetry sources
// into the state assembler.
func (c *Client) handleMessage(msg mqtt.Message) {
	headers, err := decodeEnvelope(msg.Payload())
	if err != nil {
		c.log.Error("envelope decode failed", "topic", msg.Topic(), "err", err)
		return
	}
	for _, h := range headers {
		if code := h.GetCode(); code == "-1" || code == "-2" {
			status := map[string]string{"-1": "not bound", "-2": "offline"}[code]
			c.log.Warn("device status", "status", status, "topic", msg.Topic())
			return
		}
		if c.cfg.DebugLog {
			c.log.Debug("msg", "topic", msg.Topic(), "src", h.GetSrc(),
				"cmd_set", h.GetCmdFunc(), "cmd_id", h.GetCmdId(),
				"bytes", len(h.GetPdata()), "hex", hex.EncodeToString(h.GetPdata()))
		}
		switch {
		case isHeartbeat(h):
			hb, err := parseHeartbeat(h)
			if err != nil {
				c.log.Error("heartbeat parse failed", "err", err)
				continue
			}
			c.state.ingestHeartbeat(hb)
		case isBpHeartbeat(h):
			packs, err := parseBpHeartbeat(h)
			if err != nil {
				c.log.Error("bp heartbeat parse failed", "err", err)
				continue
			}
			c.state.ingestPacks(packs)
		case isEnergyStreamReport(h):
			// A cmd 33 in a get_reply burst is the device's frozen cache (only
			// refreshed while the phone app holds the stream switch open) —
			// never trust it. Push-topic reports are the live stream.
			if msg.Topic() != devicePropertyTopic(c.cfg.DeviceSN) {
				c.log.Debug("cmd33 cached (ignored)", "seq", h.GetSeq())
				continue
			}
			esr, err := parseEnergyStreamReport(h)
			if err != nil {
				c.log.Error("energy stream report parse failed", "err", err)
				continue
			}
			c.state.ingestSystem(reportFromESR(esr, time.Now()), time.Now())
		case isEnergyStreamDetail(h):
			item, err := parseEnergyStreamDetail(h)
			if err != nil {
				c.log.Error("energy stream detail parse failed", "err", err)
				continue
			}
			r := reportFromItem(item)
			c.state.ingestSystem(r, r.Time)
		}
	}
}
