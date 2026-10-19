# EcoFlow Power Ocean Plus — telemetry reader (Go)

Unofficial, independent reader for the **EcoFlow Power Ocean Plus** home battery
(product type 87, serial prefix `R37`). It connects to the EcoFlow cloud MQTT
API and live-streams energy telemetry: battery SOC and power, PV (per-string
MPPT + PV inverter), house load, and grid power — the same values as the
official app's dashboard, at the same cadence.

**Not affiliated with, endorsed by, or supported by EcoFlow.** Use at your own
risk.

## Usage

```bash
export ECOFLOW_EMAIL='you@example.com'
export ECOFLOW_PASSWORD='your password'   # handled as UTF-8, then base64, like the app
export ECOFLOW_DEVICE_SN='R37…'           # device serial

cd reader
go run .
```

Example output (updated every few seconds while the stream switch is on):

```
[22:54:13] PowerOcean Plus telemetry
  Battery SOC    : 73%  (packs 76% / 74%)
  Battery        : -521 W (discharging)
  Solar (MPPT)   : 0 W
  Solar (PV1-3)  : 0 / 0 / 0 W
  Solar (PV inv) : 0 W
  Load           : 519 W
  Grid           : -2 W (exporting)
```

Sign conventions: battery `> 0` = charging, `< 0` = discharging; grid `> 0` =
importing, `< 0` = exporting. System SOC is capacity-weighted from the
individual battery packs; a `()` suffix shows each pack.

### Environment variables

| Variable | Meaning |
|---|---|
| `ECOFLOW_EMAIL`, `ECOFLOW_PASSWORD`, `ECOFLOW_DEVICE_SN` | required credentials and target device |
| `ECOFLOW_STREAM=0` | disable the stream-switch write (strictly read-only, slower cadence) |
| `ECOFLOW_DEBUG=1` | raw payload hex dumps and request sniff logs |

## Protocol summary

Details and provenance: [`docs/protocol.md`](docs/protocol.md) (wire schema and
transport findings) and [`docs/auth.md`](docs/auth.md) (login → MQTT
credentials). In brief:

- Transport: EcoFlow cloud MQTT (TLS, username/password from
  `/iot-auth/app/certification`).
- Envelope: `Common.Send_Header_Msg` protobuf — repeated `Header{pdata, src,
  dest, cmd_func, cmd_id, enc_type, seq, …}`; `enc_type=1` payloads are XOR'd
  with `(byte)seq`.
- Telemetry: `JtS1Sys.EnergyStreamReport` (`cmd_func=96, cmd_id=33`), plus live
  `HeartbeatReport` (cmd 1) and per-pack `BpHeartbeatReport` (cmd 7).
- Schemas in [`proto/`](proto/), reconstructed from the wire interface; Go
  bindings generated into `reader/pb/`.

## Development

```bash
cd reader
go test ./...        # includes byte-level tests against captured wire frames
go vet ./...
```

Regenerate Go bindings after editing protos:

```bash
protoc -I . --go_out=reader --go_opt=module=reader \
  --go_opt=Mproto/Common.proto=reader/pb \
  --go_opt=Mproto/jt_s1_cmd.proto=reader/pb \
  --go_opt=Mproto/jt_s1_sys.proto=reader/pb \
  proto/Common.proto proto/jt_s1_cmd.proto proto/jt_s1_sys.proto
```

## Note: the reader sends one write command

By default the reader publishes `ENERGY_STREAM_SWITCH = 1` (a **set** command)
every 10 seconds. This is the same command the official app sends whenever its
dashboard is open: it opens the telemetry *reporting stream*, which makes the
device push `EnergyStreamReport` every few seconds instead of its default
~30–60 s cadence. It does **not** touch any power-flow, charge, or inverter
setting — only how often the device reports.

- `ECOFLOW_STREAM=0` disables even this. The reader then only **subscribes** and
  sends a single read-only "fetch all" *get* at connect time (plus one per
  reconnect), and telemetry arrives at the device's slower natural cadence.
- No other write commands are ever sent (no charge/discharge limits, no
  work-mode changes, no reset — nothing else).

If you want a strictly read-only tool, run with `ECOFLOW_STREAM=0`.

## Legal note

This project was created by independent reverse engineering of the official
Android app **for interoperability purposes**, which is a widely recognised
legitimate practice (e.g. EU Software Directive Art. 6; US case law on reverse
engineering for interoperability). **No copyrighted material from the app is
included in this repository** — no app code, no decompiled sources, no APK
contents. Everything here is independently written. Protocol field names and
wire schemas are functional interface specifications.

Be aware that the app's terms of service likely prohibit reverse engineering;
using this software may be against those terms. That is your decision to make.

## License

[MIT](LICENSE)
