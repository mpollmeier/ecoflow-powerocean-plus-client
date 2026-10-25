package powerocean

import (
	"context"
	"errors"
	"fmt"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/mpollmeier/ecoflow-powerocean-plus-client/pb"
)

// setAckTimeout is how long SetBackupReserve waits for the device to ack its
// set before treating the operation as "sent but unconfirmed".
const setAckTimeout = 10 * time.Second

// SetBackupReserve sets the battery's backup reserve — the minimum state of
// charge the battery discharges to in normal operation (kept for backup power)
// — to percent (0–100). In the German app this setting is labelled
// "Backup-Reserve".
//
// It authenticates, connects, and publishes one SYS_BAT_CHG_DSG_SET command
// (cmd 112, field sys_bat_dsg_down_limie) on .../thing/property/set, the same
// command the online app's battery settings send. It waits briefly for the
// device's acknowledgement and disconnects. This is a real write: it changes a
// battery setting, so it is only ever sent when the caller explicitly invokes
// this method (the console client does so via --set-backup-reserve).
func (c *Client) SetBackupReserve(ctx context.Context, percent uint32) error {
	if percent > 100 {
		return fmt.Errorf("powerocean: backup reserve %d%% out of range 0..100", percent)
	}

	cli, userID, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cli.Disconnect(250)

	sn := c.cfg.DeviceSN
	req, seq := buildBackupReserveRequest(sn, percent)

	// Subscribe to the ack topic before publishing so the acknowledgement is
	// not missed. The device echoes cmd_func/cmd_id (96/112) in its set_reply.
	setTopic := fmt.Sprintf("/app/%s/%s/thing/property/set", userID, sn)
	replyTopic := fmt.Sprintf("/app/%s/%s/thing/property/set_reply", userID, sn)

	ack := make(chan int32, 1)
	token := cli.Subscribe(replyTopic, 0, func(_ mqtt.Client, msg mqtt.Message) {
		headers, err := decodeEnvelope(msg.Payload())
		if err != nil {
			return
		}
		for _, h := range headers {
			if !isEms(h, pb.EmsCmdId_EMS_CMD_ID_SYS_BAT_CHG_DSG_SET) {
				continue
			}
			// The ack normally echoes our seq; if it does and it doesn't
			// match, skip (a concurrent set from another session).
			if s := h.GetSeq(); s != 0 && *seq != 0 && s != *seq {
				continue
			}
			var a pb.SysBatChgDsgSetAck
			if err := proto.Unmarshal(h.GetPdata(), &a); err != nil {
				continue
			}
			select {
			case ack <- a.GetResult():
			default:
			}
		}
	})
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("powerocean: subscribe %s: %w", replyTopic, err)
	}

	token = cli.Publish(setTopic, 0, false, req)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("powerocean: publish backup reserve: %w", err)
	}
	c.log.Info("backup reserve set request published", "percent", percent, "seq", *seq)

	select {
	case result := <-ack:
		if result == 0 {
			c.log.Info("backup reserve set acknowledged", "percent", percent)
			return nil
		}
		return fmt.Errorf("powerocean: device rejected backup reserve: result=%d", result)
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(setAckTimeout):
		// The publish succeeded; the device may still be applying the value.
		// Fail loudly but allow the operator to verify in the app.
		return errors.New("powerocean: backup reserve sent but no acknowledgement received (verify in the app)")
	}
}
