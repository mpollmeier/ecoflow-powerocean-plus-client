// Command ecoflow-powerocean-plus-client streams live energy telemetry from an
// EcoFlow Power Ocean Plus battery to the console.
//
// Configuration is via environment (see README). Its only write is the
// energy-stream switch keepalive (cmd 97) which opens the telemetry reporting
// stream for few-seconds cadence — ECOFLOW_STREAM=0 disables it, making the
// client strictly read-only.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client"
)

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	client, err := powerocean.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ecoflow-powerocean-plus-client:", err)
		os.Exit(1)
	}
}

// configFromEnv maps the documented environment variables to a Config.
func configFromEnv() (powerocean.Config, error) {
	cfg := powerocean.Config{
		Email:    os.Getenv("ECOFLOW_EMAIL"),
		Password: os.Getenv("ECOFLOW_PASSWORD"),
		DeviceSN: os.Getenv("ECOFLOW_DEVICE_SN"),
		OnReport: func(r powerocean.Report) { fmt.Print(r) },
		DebugLog: os.Getenv("ECOFLOW_DEBUG") != "",
		// Stream switch is on by default (few-seconds cadence, like the app's
		// dashboard); ECOFLOW_STREAM=0 makes the client strictly read-only.
		StreamSwitch: os.Getenv("ECOFLOW_STREAM") != "0",
	}
	var missing []string
	for k, v := range map[string]string{
		"ECOFLOW_EMAIL":     cfg.Email,
		"ECOFLOW_PASSWORD":  cfg.Password,
		"ECOFLOW_DEVICE_SN": cfg.DeviceSN,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return powerocean.Config{}, fmt.Errorf("missing environment variables: %v", missing)
	}
	return cfg, nil
}
