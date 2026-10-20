# ecoflow-powerocean-plus-client

Unofficial Go client for the EcoFlow **Power Ocean Plus** home battery
(product type 87, serial prefix `R37`).

It connects to the EcoFlow cloud MQTT API and live-streams energy telemetry:
battery SOC and power, PV (per-string MPPT + PV inverter), house load, and grid
power — the same values as the official app's dashboard, at the same cadence.
Usable as a library (`import powerocean "github.com/mpollmeier/ecoflow-powerocean-plus-client"`) or via
the included console client.

**Not affiliated with, endorsed by, or supported by EcoFlow.** Use at your own
risk.

## Library usage

```go
import powerocean "github.com/mpollmeier/ecoflow-powerocean-plus-client"

client, err := powerocean.New(powerocean.Config{
    Email:    "you@example.com",
    Password: "your password", // handled as UTF-8, then base64, like the app
    DeviceSN: "R37…",          // device serial
    // StreamSwitch: true,    // the one write — see the warning above; default off
    OnReport: func(r powerocean.Report) { fmt.Print(r) },
})
if err != nil {
    return err
}
return client.Run(ctx) // blocks until ctx is cancelled
```

`Report` carries battery SOC (system + per-pack), battery/solar/load/grid
watts, and a `Derived` flag (see wire-format notes below). The zero config is
strictly read-only; `StreamSwitch: true` opts into the stream keepalive write.

## Console client

```bash
export ECOFLOW_EMAIL='you@example.com'
export ECOFLOW_PASSWORD='your password'
export ECOFLOW_DEVICE_SN='R37…'           # device serial

go run ./cmd/ecoflow-powerocean-plus-client
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
| `ECOFLOW_STREAM=0` | disable the stream-switch write (strictly read-only, slower cadence; library equivalent: leave `Config.StreamSwitch` false) |
| `ECOFLOW_DEBUG=1` | raw payload hex dumps and request sniff logs |

## Protocol summary

Details and provenance: [`docs/protocol.md`](docs/protocol.md) (wire schema and
transport findings) and [`docs/auth.md`](docs/auth.md) (login → MQTT
credentials). Schemas live in [`proto/`](proto/), Go bindings in [`pb/`](pb/).

### Connection flow

1. **Login** — `POST https://api.ecoflow.com/auth/login` (email + base64 of the
   UTF-8 password) → bearer token + user ID.
2. **MQTT credentials** — `GET /iot-auth/app/certification?userId=…` → broker
   host/port and MQTT username/password (despite the name, no TLS client
   certificates are involved).
3. **Connect** — TLS MQTT (`mqtts://broker:8883`) with those credentials,
   client ID `ANDROID_<uuid>_<userId>`.
4. **Subscribe** to the device's topics, all under the user's account:
   - `/app/device/property/{sn}` — device → us telemetry pushes
   - `/app/{userId}/{sn}/thing/property/get_reply` — replies to get requests
   - (the `get`/`set` and `biz/data/request` request topics are also
     subscribed, but only to log echoes in debug mode)

### Messages we send (all on the two `thing/property` topics)

| What | Topic | When | Effect |
|---|---|---|---|
| **Fetch-all** `Send_Header_Msg{src:32, dest:32, from:"Android"}` (a get, no cmd) | `…/thing/property/get` | once at connect / reconnect | device replies on `get_reply` with its cached snapshot (seed data) |
| **Stream switch** `ENERGY_STREAM_SWITCH=1` (`cmd 97` set, `pdata=0801`) | `…/thing/property/set` | every 10 s keepalive (**the one write**, see warning above) | device accelerates `EnergyStreamReport` pushes from ~30-60 s to a few seconds |

That is the complete outbound surface — nothing else is ever published.

### Messages we receive

| Message | cmd | Cadence | Content |
|---|---|---|---|
| `EnergyStreamReport` | 96/33 | few seconds with stream switch, ~30-60 s without | load, grid, MPPT/PV, battery W, SOC |
| `HeartbeatReport` | 96/1 | ~60 s | live powers, grid meter (per phase), per-string PV |
| `BpHeartbeatReport` | 96/7 | ~40-60 s bursts | per-pack SOC, power, capacity, serial |
| `EnergyStreamDetail` | 96/34 | ~10 min | timestamped history samples of the same telemetry |

### Wire format

Everything rides MQTT as `Common.Send_Header_Msg` protobuf — a list of
`Header{pdata, src, dest, cmd_func, cmd_id, enc_type, seq, …}`, where `pdata`
is the command-specific protobuf (`enc_type=1` payloads are XOR-obfuscated with
`(byte)seq` and decoded transparently). Dispatch is by `cmd_func`/`cmd_id`
(EMS = 96). Note that `EnergyStreamReport`s arriving in `get_reply` are the
device's **cached** snapshot — the reader ignores those and uses the live push
sources above; system SOC is capacity-weighted from the individual packs, and
`load` is derived as `solar + grid − battery`.

## Development

```bash
go test ./...        # includes byte-level tests against captured wire frames
go vet ./...
```

Regenerate Go bindings after editing protos:

```bash
protoc -I . --go_out=. --go_opt=module=github.com/mpollmeier/ecoflow-powerocean-plus-client \
  --go_opt=Mproto/Common.proto=github.com/mpollmeier/ecoflow-powerocean-plus-client/pb \
  --go_opt=Mproto/jt_s1_cmd.proto=github.com/mpollmeier/ecoflow-powerocean-plus-client/pb \
  --go_opt=Mproto/jt_s1_sys.proto=github.com/mpollmeier/ecoflow-powerocean-plus-client/pb \
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
