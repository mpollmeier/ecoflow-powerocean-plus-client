# Protocol notes — Power Ocean Plus telemetry (cloud MQTT)

Reverse-engineering findings and wire-format documentation behind the
`reader/` implementation. Everything here describes functional protocol
behaviour; no app code is reproduced.

## Confirmed facts (from APK 6.17.4.14)

- **Device**: PowerOceanPlus = productType **87**, SN prefix **R37** (R371=15kW … R374=30kW) — *verified on a real R371 (15kW) unit*
- **Driver**: `com.ecoflow.jtmodule.s1` → protocol `JtS1Sys` (protobuf) over EcoFlow **cloud MQTT**
- **Repo assets**: `power-ocean-plus-findings.md`, `decompiled/` (32 jadx extracts, *authoritative*), `dexrefs.py`
- **APK**: `apks/6.17.4.14/unzipped/com.ecoflow.apk` (71 dex)
- **Dex map**: `classes9.dex` = databus/MQTT/`Common` envelope proto, `classes13.dex` = JtS1 protos,
  `classes7.dex` = `q.A` XOR helper, `classes46.dex` = S1 parse/dispatch (`x00_c`)
- **Full jadx tree** at `apks/6.17.4.14/decompiled/sources` is **partial** (decompile crashed) — missing
  classes must be pulled with `jadx --single-class <class> classesN.dex -d <out>`

### Wire schema (reconstructed)

MQTT payloads are **not** raw `EcoPacket`/BLE frames. They are `Common.Send_Header_Msg` protobufs
(`com.ecoflow.cbb.databus.proto.Common`, classes9.dex; schema from the `FileDescriptorProto`
embedded in the generated class):

```proto
// proto/common.proto
message Header {
  bytes  pdata      = 1;   // inner proto payload (possibly XOR-obfuscated)
  int32  src        = 2;
  int32  dest       = 3;
  int32  d_src      = 4;
  int32  d_dest     = 5;
  int32  enc_type   = 6;   // 1 = pdata XOR'd with (byte)seq
  int32  check_type = 7;
  int32  cmd_func   = 8;   // = cmdSet
  int32  cmd_id     = 9;
  int32  data_len   = 10;
  int32  need_ack   = 11;
  int32  is_ack     = 12;
  int32  seq        = 14;
  int32  product_id = 15;
  int32  version    = 16;
  int32  payload_ver = 17;
  optional int32  time_snap = 18;
  optional int32  is_rw_cmd = 19;
  optional int32  is_queue  = 20;
  optional int32  ack_type  = 21;
  optional string code      = 22;  // "-1"=not bound, "-2"=offline
  optional string from      = 23;
  optional string module_sn = 24;
  optional string device_sn = 25;
  optional string src_sn    = 26;
  optional string dest_sn   = 27;
}
message Send_Header_Msg { repeated Header msg = 1; }
```

Decode path (from `hk.d0.a`, classes9.dex): `Send_Header_Msg.parseFrom(payload)` → for each header,
if `enc_type == 1` then `pdata[i] ^= byte(seq)` (`com.ecoflow.base.utils.q.A`) → inner proto parse.

```proto
// proto/jt_s1_sys.proto — telemetry (schema verified in decompiled/JtS1Sys.java)
message EnergyStreamReport {
  float sysLoadPwr   = 1;  // consumption
  float sysGridPwr   = 2;  // grid (sign = import/export)
  float mpptPwr      = 3;  // solar (MPPT)
  float bpPwr        = 4;  // battery power
  int32 bpSoc        = 5;  // battery SOC %
  float pv1Pwr       = 6;
  float pv2Pwr       = 7;
  float pvInvPwr     = 8;
  float pv3Pwr       = 9;
  float otherPvPwr   = 10;
  float spain4GPvPwr = 11;
}
```

```proto
// proto/jt_s1_cmd.proto — enums (values verified in decompiled/JtS1Cmd.java)
enum CmdSets { NONE=0; ADPT=2; BP=3; PCS=4; DAB=7; APP=50; IOT=53; EMS=96; MPPT=97; AFCI=210; }
enum EmsCmdId {
  HEARTBEAT_REPORT=1; SYS_METER_REPORT=2; ERROR_CODE_REPORT=3;
  BP_HEARTBEAT_REPORT=7; CHANGE_REPORT=8; STATE_CHANGE_REPORT=17;
  ENERGY_STREAM_REPORT=33; ENERGY_STREAM_DETAIL=34;
  GET_PAREM=37; PV_INV_ENERGY_STREAM_REPORT=39;
  ECOLOGY_DEV_ENERGY_STREAM_REPORT=41;
  ENERGY_STREAM_REPORT_PARALLEL=50; ENERGY_STREAM_DETAIL_PARALLEL=51;
  ENERGY_STREAM_SWITCH=97; SYS_PARAM_GET=103;  // … full list in JtS1Cmd.java
}
```

Dispatch rule (from `x00_c.Z`, classes46.dex — S1 parse/dispatch):
`src==96 && cmd_func==96 && cmd_id==33` → `EnergyStreamReport.parseFrom(pdata)`.
Other inbound ids seen live: 1=`HeartbeatReport`, 7=`BpHeartbeatReport` (repeated
`BpStaReport`, one per pack), 3=`ErrorChangeReport`, 8/17=`EmsChangeReport`,
34=`EnergyStreamDetail`, 39=`EmsPVInvEnergyStreamReport` (often empty), 103=`SysParamGetAck`.

### Live-wire facts (verified against a real PowerOcean Plus, 2026-10-19)

- **Sparse proto3 payloads**: cmd 33 arrives as 12/17/32-byte payloads depending on which
  fields are non-zero (zeros omitted); all decode with the same schema.
- **enc_type=1 confirmed live**: cmd 34 `EnergyStreamDetail` arrives XOR'd with `(byte)seq`;
  decoded samples match cmd 33 exactly.
- **Battery sign convention (JtS1Sys)**: `bpPwr > 0` = **charging**, `< 0` = discharging
  (verified by exact power balance twice; note the *other* driver's JSON protocol uses the
  opposite sign). Grid: `> 0` = importing, `< 0` = exporting.
- **cmd 33 in fetch replies is a FROZEN CACHE** (critical): `get_reply` replies carry a
  byte-identical `EnergyStreamReport` snapshot for hours (observed: `bp_soc 92% /
  −519 W / 518 W` at 19:35, 19:52 and 21:05). It only refreshes while the phone app holds
  the cmd-97 stream switch open. **Never trust cmd 33 from `get_reply`**; live sources are:
  - cmd 1 `HeartbeatReport` (~60s pushes on `/app/device/property/{sn}`): live
    `ems_bp_power` (battery), `meter_heart_beat[].meter_data` (4 values = 3 phases + total
    → grid), `mppt_heart_beat[].mppt_pv[].pwr` (per-string solar), `ems_pv_inv_pwr`.
  - cmd 7 `BpHeartbeatReport` (~40s pushes; src=53 *and* src=96 — dispatch on
    `cmd_set/cmd_id` only, **not** `src`): per-pack `bp_soc`, `bp_real_soc`, `bp_pwr`,
    `bp_sn`, `bp_full_cap`.
  - cmd 34 `EnergyStreamDetail` (~10 min): timestamped full system samples — the *newest
    item by timestamp* is a trustworthy `EnergyStreamReport` equivalent.
  - cmd 33 on the **push** topic while the app is open = the live stream.
  - Derived load: `load = solar + grid − battery` (validated against the device's own
    cmd 33 values exactly).
  - System SOC vs pack SOCs can differ by ±1 (device coulomb accounting); the reader
    shows capacity-weighted pack SOC plus per-pack detail, and prefers a fresh cmd 34
    sample when available.
- **App start sequence** (captured via request-topic sniff): on dashboard open the app
  publishes (a) `cmd_id=97 ENERGY_STREAM_SWITCH` **set** `pdata=0801` (re-sent every 10s
  as keepalive — enables few-seconds cmd 33 streaming; never sent as "off", expires
  server-side) and (b) a pure **fetch-all get**
  `Send_Header_Msg{msg:[{src:32, dest:32, seq, from:"Android", src_sn:"", dest_sn:""}]}`
  on `/app/{userId}/{sn}/thing/property/get`. The reader reproduces **both** (user
  decision: stream switch is default-on — it only toggles the reporting stream, no
  power-flow setting; `ECOFLOW_STREAM=0` disables it for strictly read-only operation).
  The switch set is byte-verified against the app capture: src=32, dest=96, d_src=1,
  d_dest=1, cmd_func=96, cmd_id=97, data_len=2, need_ack=1, product_id=1, version=19,
  payload_ver=1, from="android" (lowercase!), device_sn=<sn>, pdata=`0801`.
- **Fetch-all does NOT trigger a fresh measurement** (verified byte-level: `get_reply`
  cmd 1/7 payloads are identical to the last push). It reads the device's cached
  snapshot — useful only to seed state at startup/reconnect. Telemetry cadence is the
  **device's own push schedule**: cmd 1 ~60s, cmd 7 bursts ~40-60s, cmd 34 ~10min
  — accelerated to ~5s cmd 33 pushes while the cmd-97 stream switch is held open.
- **Multi-pack**: the system has 2 battery packs (`BpStaReport.bp_sn`, e.g. two packs); system SOC ≈ capacity-weighted pack SOC.

### MQTT topics (`decompiled/MqttContracts.java`, confirmed in `nv/h.java`)

Binary `Send_Header_Msg` payloads arrive on:

```
/app/device/property/{sn}                          # MQTT_TOPIC_PROPERTY
/app/{userId}/{sn}/thing/property/get_reply
```

`/app/device/status/{sn}` is JSON status (`{"params":{"status":"1"}}`), not telemetry.
`/app/{userId}/{type}/biz/data/request|response|notify` (type 1=JSON, 2=protobuf) is the
request/response channel (`/app/{userId}/{sn}/thing/property/get` is the device-get channel).

## Step 1 — Proto reconstruction

1. Extract `FileDescriptorProto` blobs embedded in generated Java classes
   (`internalBuildGeneratedFileFrom(new String[]{…})`), unescape, `protoc --decode` → authoritative
   field numbers/types for `Common`, `JtS1Cmd`, `JtS1Sys`.
2. Write `proto/common.proto` (envelope above), `proto/jt_s1_cmd.proto` (enums `CmdSets`, `EmsCmdId`
   from `decompiled/JtS1Cmd.java`), `proto/jt_s1_sys.proto`:
   - `EnergyStreamReport` (verified), `EmsGetParam` (GET_PAREM request), `SysParamGet` + `SysParamGetAck`,
     passthrough reports: `HeartbeatReport`, `SysMeterReport`, `StateChangeReport`, `BpStaReport`.
3. Generate Go bindings: `protoc --go_out=reader/pb --go_opt=paths=source_relative proto/*.proto`.
4. **Acceptance**: Go test round-trips `EnergyStreamReport(bpSoc=55, …)` and an `enc_type=1`
   XOR-obfuscated `Send_Header_Msg` carrying one.

## Step 2 — Transport (largely reverse-engineered; verify live)

1. **Auth flow** — implemented in `auth.go` (Go), documented in `docs/auth.md`:
   - `POST https://api.ecoflow.com/auth/login` (body: email, base64(UTF-8 password), `scene=IOT_APP`,
     `userType=ECOFLOW`) → token + userId.
   - `GET /iot-auth/app/certification?userId=…` (Bearer token) → MQTT broker host/port +
     `certificateAccount`/`certificatePassword` (username/password auth; TLS server-side, `InsecureSkipVerify`
     in the PoC).
   - Cache key `mqtt_login_certificate` seen in `S1DeviceDetailViewModel`; no client-cert flow needed
     for this login type (verified by the working Go prototype).
2. **Codec** — DONE (statically). MQTT frame = `Send_Header_Msg` (see above). BLE-style
   `EcoPacket` V2/V3 framing (`EcoProtocolEncryptTypeDefault`) is **not** used on cloud MQTT;
   kept as reference for outbound crafting only.
3. **Topic dispatch** — CONFIRMED statically (`nv/h.h`, `x00_c.Z`). Subscribe
   `/app/device/property/{sn}` and `/app/{userId}/{sn}/thing/property/get_reply`; filter
   `cmd_func==96 && cmd_id==33`.
4. **Acceptance**: live capture from a real device decodes to `EnergyStreamReport` and values
   match the app dashboard.

## Step 3 — Go prototype (`reader/`)

1. Layout: Go module `github.com/mpollmeier/ecoflow-powerocean-plus-client`, library package
   `powerocean` at the repo root (`client.go`, `auth.go`, `envelope.go`,
   `state.go`, `report.go`, `mqtt.go`, `request.go`), console client at
   `cmd/ecoflow-powerocean-plus-client`, generated bindings in `pb/`, tests at the root.
   Config via env: `ECOFLOW_EMAIL`, `ECOFLOW_PASSWORD`, `ECOFLOW_DEVICE_SN` (secrets stay in env);
   `ECOFLOW_STREAM=0` (disable the cmd-97 stream switch = strictly read-only, 30-60s cadence),
   `ECOFLOW_DEBUG=1` (raw payload dumps + request sniff logs).
2. Flow: REST login → cert/account → mqtts connect → subscribe device topics → decode
   `Send_Header_Msg` → assemble a **live** report from heartbeats (cmd 1), pack reports
   (cmd 7) and timestamped cmd 34 samples; cmd 33 is used only when it arrives on the
   push topic (live stream). Derived fields: `load = solar + grid − battery`,
   SOC = capacity-weighted pack SOC. Prints per-pack SOC detail.
3. Write guard: the reader publishes exactly two things — the fetch-all **get**
   (`Send_Header_Msg{src:32,dest:32,from:"Android"}` on `.../thing/property/get`) and the
   energy-stream switch **set** (cmd 97 = 1, on `.../thing/property/set`, re-sent every 10s
   keepalive). Both byte-verified against the app's own requests. No other `set*`, no other
   write cmdIds (`*_SET`, `SYS_FACTORY_RESET`, …). `ECOFLOW_STREAM=0` removes even cmd 97,
   making the reader strictly subscribe+get-only (30-60s cadence). On reconnect: re-subscribe,
   re-fetch, re-open stream.
4. **Acceptance**: values match the app dashboard within one report interval — DONE live
   (SOC/load/grid/battery match; few-seconds cadence with stream switch; fetch-all start
   sequence works with the app closed).

## Risks

- ~~Inbound solved, outbound not~~ — RESOLVED: the app's fetch-all **get** request framing is
  captured and byte-verified; the reader publishes it to trigger on-demand refreshes.
  `GET_PAREM` (37) publishing remains unimplemented but is unnecessary (fetch-all covers it).
- ~~Stale snapshots in fetch replies~~ — RESOLVED: cmd 33 from `get_reply` is ignored
  (frozen cache); live heartbeats/packs/cmd-34 samples drive the report instead.
- **Cert/session lifetime** — fail loudly in the prototype; refresh logic later.
- **Password encoding** — app base64s the UTF-8 bytes of the password (`ß` etc. must survive;
  if login fails, check encoding first).
- **Stream switch is a write** (user-approved default-on): cmd 97 only toggles the
  reporting stream (like the app's dashboard open) and expires without keepalive, so no
  persistent device state changes; still, `ECOFLOW_STREAM=0` exists for strictly
  read-only use. With it off, cadence is the device's push schedule (30-60s).

## Execution order

All steps DONE: Step 1 (protos + Go bindings + tests) → Step 2 (`docs/auth.md`, envelope/topic
confirmed live) → Step 3 (`reader/`, live run vs a real device accepted).
