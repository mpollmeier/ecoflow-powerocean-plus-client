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
	"strconv"
	"syscall"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client"
)

func main() {
	setBackupReserve := flag.Int("set-backup-reserve", -1,
		"set the battery's backup reserve (percent SOC kept for backup power, 0-100) and exit")
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
	// result in the app. Fall back to the env var for backwards compatibility.
	if percent, ok, err := backupReservePercent(*setBackupReserve); err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	} else if ok {
		if err := client.SetBackupReserve(ctx, percent); err != nil {
			fmt.Fprintln(os.Stderr, "ecoflow-powerocean-plus-client:", err)
			os.Exit(1)
		}
		fmt.Printf("backup reserve set to %d%%\n", percent)
		return
	}

	if err := client.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ecoflow-powerocean-plus-client:", err)
		os.Exit(1)
	}
}

// backupReservePercent resolves the backup-reserve target from the command-line
// flag, falling back to ECOFLOW_SET_BACKUP_RESERVE for backwards compatibility.
// ok is false when neither is set (i.e. the streaming path should run).
func backupReservePercent(flagVal int) (uint32, bool, error) {
	v := flagVal
	if v < 0 {
		env := os.Getenv("ECOFLOW_SET_BACKUP_RESERVE")
		if env == "" {
			return 0, false, nil
		}
		n, err := strconv.ParseUint(env, 10, 32)
		if err != nil {
			return 0, false, fmt.Errorf("ECOFLOW_SET_BACKUP_RESERVE must be a whole number 0-100")
		}
		v = int(n)
	}
	if v > 100 {
		return 0, false, fmt.Errorf("backup reserve %d out of range 0-100", v)
	}
	return uint32(v), true, nil
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
