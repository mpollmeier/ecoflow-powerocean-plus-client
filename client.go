// Package powerocean is a client for the EcoFlow Power Ocean (Plus) home
// battery, speaking the EcoFlow cloud MQTT protocol. It streams live energy
// telemetry — battery state of charge and power, per-string PV, house load,
// and grid power — at the same cadence as the official app.
//
// It was created by independent reverse engineering of the official Android
// app for interoperability purposes. No copyrighted material from the app is
// included. Be aware that the app's terms of service likely prohibit reverse
// engineering; using this software may be against those terms.
//
// The zero-config stance is read-only: with Config.StreamSwitch unset the
// client only subscribes and sends one read-only fetch at connect, and
// telemetry arrives at the device's natural push cadence (~30-60 s). Setting
// StreamSwitch enables the write that opens the telemetry reporting stream
// (ENERGY_STREAM_SWITCH keepalive, cmd 97 = 1); SetBackupReserve is the other,
// one-shot write that changes a battery setting (cmd 112). Both are opt-in.
//
// Basic use:
//
//	client, err := powerocean.New(powerocean.Config{
//		Email:    "...",
//		Password: "...",
//		DeviceSN: "R37…",
//		OnReport: func(r powerocean.Report) { fmt.Println(r) },
//	})
//	if err != nil {
//		return err
//	}
//	return client.Run(ctx) // blocks until ctx is cancelled
package powerocean

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Config configures a Client.
type Config struct {
	// Email, Password and DeviceSN are required. The password is sent to the
	// EcoFlow API as base64 of its UTF-8 bytes, exactly like the official app.
	Email    string
	Password string
	DeviceSN string

	// StreamSwitch enables the energy-stream keepalive write: the
	// ENERGY_STREAM_SWITCH (cmd 97 = 1, re-sent every 10 s), the same command
	// the official app sends while its dashboard is open. It opens the
	// telemetry *reporting stream* — the device pushes EnergyStreamReport
	// every few seconds instead of every ~30-60 s. It does NOT touch any
	// power-flow, charge, or inverter setting. (The other write is
	// SetBackupReserve.)
	//
	// Leave false for a strictly read-only client (slower cadence).
	StreamSwitch bool

	// OnReport is called with each coalesced telemetry update (reports within
	// a short window are merged into one). Required.
	OnReport func(Report)

	// Logger receives diagnostics. Defaults to slog.Default().
	Logger *slog.Logger

	// DebugLog additionally logs raw MQTT payload hex dumps. Verbose.
	DebugLog bool
}

// Client is a live telemetry connection to one device. Create with New,
// run with Run.
type Client struct {
	cfg   Config
	log   *slog.Logger
	state *liveState
}

// New validates cfg and returns a ready Client. It performs no I/O; the
// connection is made in Run.
func New(cfg Config) (*Client, error) {
	if cfg.Email == "" || cfg.Password == "" || cfg.DeviceSN == "" {
		return nil, errors.New("powerocean: Email, Password and DeviceSN are required")
	}
	if cfg.OnReport == nil {
		return nil, errors.New("powerocean: OnReport is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		cfg:   cfg,
		log:   log,
		state: newLiveState(cfg.DeviceSN, cfg.OnReport),
	}, nil
}

// dial authenticates and connects to the MQTT broker, subscribing to the
// device's telemetry topics. The caller owns the returned client (call
// Disconnect when done).
func (c *Client) dial(ctx context.Context) (mqtt.Client, string, error) {
	creds, mqttCreds, err := c.loginAndFetchMQTT(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("powerocean: auth: %w", err)
	}
	c.log.Info("authenticated", "user", creds.UserID, "broker", mqttCreds.URL)

	cli, err := c.connectAndSubscribe(mqttCreds, creds.UserID, c.cfg.DeviceSN)
	if err != nil {
		return nil, "", fmt.Errorf("powerocean: mqtt: %w", err)
	}
	return cli, creds.UserID, nil
}

// Run connects and delivers telemetry reports through Config.OnReport until
// ctx is cancelled, then disconnects cleanly and returns nil.
//
// Outbound traffic is limited to: one fetch-all *get* per (re)connect that
// seeds state from the device's cache, and — only if Config.StreamSwitch is
// set — the energy-stream switch keepalive.
func (c *Client) Run(ctx context.Context) error {
	cli, userID, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect(250)

	if c.cfg.StreamSwitch {
		c.startStreamSwitch(ctx, cli, userID, c.cfg.DeviceSN)
	}

	<-ctx.Done()
	return nil
}
