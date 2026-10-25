// Command ecoflow-powerocean-plus-client streams live energy telemetry from an
// EcoFlow Power Ocean Plus battery to the console, or — with
// --set-backup-reserve=<0-100> — writes the battery's backup-reserve level once
// and exits.
//
// Credentials come from the environment (see README). Its only writes are the
// energy-stream switch keepalive (cmd 97, opt-out via ECOFLOW_STREAM=0) and
// the explicit backup-reserve set (cmd 112, only with --set-backup-reserve).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client"
)

func main() {
	setBackupReserve := flag.Int("set-backup-reserve", -1,
		"set the battery's backup reserve (0-100 percent) and exit")
	flag.Parse()

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

	// One-shot write: set the battery's backup reserve, then exit. Verify the
	// result in the app.
	if *setBackupReserve >= 0 {
		if *setBackupReserve > 100 {
			fmt.Fprintln(os.Stderr, "config error: backup reserve must be 0-100")
			os.Exit(1)
		}
		if err := client.SetBackupReserve(ctx, uint32(*setBackupReserve)); err != nil {
			fmt.Fprintln(os.Stderr, "ecoflow-powerocean-plus-client:", err)
			os.Exit(1)
		}
		fmt.Printf("backup reserve set to %d%%\n", *setBackupReserve)
		return
	}

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
