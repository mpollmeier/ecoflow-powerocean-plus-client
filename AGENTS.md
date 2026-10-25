# AGENTS.md — workspace notes

Quick orientation for followup sessions (and contributors) working on this
repo. Public-safe: real serials/credentials stay in the operator's environment,
never in tracked files.

## What this is

Unofficial Go client for the EcoFlow **Power Ocean Plus** home battery (product
type 87, SN prefix `R37`): live energy telemetry over the EcoFlow cloud MQTT
API — battery SOC/power, per-string PV, house load, grid power. Created by
independent reverse engineering of the Android app for interoperability; no app
code is included anywhere in this repo.

- Module `github.com/mpollmeier/ecoflow-powerocean-plus-client`, library
  package `powerocean` at the repo root (call sites: `powerocean.New(...)`).
- CLI: `cmd/ecoflow-powerocean-plus-client`.
- Docs: `docs/protocol.md` (wire-format findings), `docs/auth.md` (login →
  MQTT credentials), `README.md` (usage + protocol overview).

## Repo layout (tracked files)

| Path | What |
|---|---|
| `client.go` | public API: `Config`, `Client`, `New`, `Run(ctx)` |
| `auth.go` | REST login + `/iot-auth/app/certification` (ctx-aware HTTP) |
| `envelope.go` | `Send_Header_Msg` codec, enc_type XOR, cmd dispatch, report conversion |
| `state.go` | live-state assembler: merges sources, debounces, dedups, derives |
| `report.go` | `Report`/`Pack` types, rendering, SOC weighting, sign conventions |
| `mqtt.go` | connection, topics, message handlers (Client methods) |
| `request.go` | request builders: fetch-all get + cmd-97 stream switch + cmd-112 backup-reserve set |
| `control.go` | `SetBackupReserve` — one-shot cmd-112 write (opt-in) |
| `pb/` | generated Go protobuf bindings — never hand-edit, regenerate |
| `proto/` | reconstructed wire schemas (`Common`, `jt_s1_cmd`, `jt_s1_sys`) |
| `*_test.go` | unit tests incl. byte-fidelity tests vs captured wire frames |

## Local RE artifacts (gitignored — not present in a fresh clone)

All under `/home/mp/Projects/ecoflow-powerocean-plus-client/`:

| Path | What |
|---|---|
| `apks/6.17.4.14/unzipped/com.ecoflow.apk` | the APK (71 dex). Full `.xapk` next to it |
| `apks/6.17.4.14/decompiled/sources/` | full jadx tree — **INCOMPLETE** (jadx crashed mid-run) |
| `decompiled/` | 32 authoritative single-class jadx extracts (`JtS1Sys.java` = full proto schema, `JtS1Cmd.java` = enums, `x00_c.java` = S1 dispatch, `EcoPacket.java`, `MqttContracts.java`, …) |
| `dexrefs.py` | dex string/bytecode scanner: `python3 dexrefs.py <dex> <regex>` |
| `plan-reader.md` | working protocol notes / original plan (local; `docs/protocol.md` is its scrubbed published copy) |
| `power-ocean-plus-findings.md` | early RE findings (product id, SN prefixes, driver layout) |
| `powerocean-mqtt/` | older Go prototype (JSON path — the *other* driver, not used here) |
| `reader.out` | capture log — **contains the real device SN, do not publish** |

The `.gitignore` is a whitelist (`/*` + un-ignore list). `apks/`, `decompiled/`,
`plan-reader.md`, `reader.out`, `dexrefs.py` etc. must never be committed.

## Looking things up in the APK

Missing class from the incomplete jadx tree? Pull it from the right dex with
`jadx --single-class <fqcn> -d <out> apks/6.17.4.14/unzipped/<classesN>.dex`.
Dex map (extracted from `com.ecoflow.apk`, 71 dex):

| Dex | Contents |
|---|---|
| `classes9.dex` | `com.ecoflow.cbb.databus.proto.Common` (MQTT envelope), MQTT layer, auth beans |
| `classes13.dex` | `JtS1Sys` / `JtS1Cmd` protos (the telemetry schema) |
| `classes7.dex` | `com.ecoflow.base.utils.q.A` — the enc_type XOR |
| `classes46.dex` | `x00_c` — S1 parse/dispatch (cmd id → message mapping) |

Wire schemas were recovered from `FileDescriptorProto` blobs embedded in the
generated Java classes and are byte-verified; `proto/*.proto` is the source of
truth now. Regeneration command is in the README.

## Protocol in 30 seconds (details in docs/protocol.md)

- MQTT payload = `Common.Send_Header_Msg` (repeated `Header{pdata, cmd_func,
  cmd_id, enc_type, seq, …}`); `enc_type=1` → `pdata[i] ^= byte(seq)`.
- Dispatch by `cmd_func`/`cmd_id` (EMS = 96): **33** `EnergyStreamReport`,
  **34** `EnergyStreamDetail` (timestamped), **1** `HeartbeatReport` (live
  powers ~60 s), **7** `BpHeartbeatReport` (per-pack ~40 s).
- **cmd 33 in `get_reply` is a frozen cache** — ignore it; only push-topic
  (`/app/device/property/{sn}`) cmd 33 is live.
- Signs (JtS1Sys driver!): battery `>0` charging, grid `>0` importing — the
  *opposite* of the pocommon JSON driver. Derived `load = solar + grid − battery`.
- Outbound surface is three publishes: the fetch-all **get** (seed only — does
  not trigger a fresh measurement), the opt-in **cmd 97** stream switch
  keepalive (opens reporting cadence ~5 s), and the opt-in **cmd 112**
  backup-reserve set (`SysBatChgDsgSet.sys_bat_dsg_down_limie` — the discharge
  floor the German app labels "Backup-Reserve", CLI flag `--set-backup-reserve`).
  The two writes are strictly opt-in.

## Build / test / run

```bash
go test -race ./...   # includes byte-fidelity tests vs captured frames
go vet ./... && gofmt -l .
go run ./cmd/ecoflow-powerocean-plus-client   # needs ECOFLOW_* env (see README)
```

`protoc-gen-go` lives in `~/go/bin` — prefix `PATH="$HOME/go/bin:$PATH"` when
regenerating `pb/`. Tests use the scrubbed placeholder SN `R371SAMPLEDEV001`
(16 chars, same length as the real one) inside captured wire hex — keep it that
way.

## Guardrails

- **The two writes, both opt-in**: `Config.StreamSwitch` (CLI default on,
  `ECOFLOW_STREAM=0` to disable) sends cmd 97 — a reporting-stream toggle only,
  no power-flow settings. `SetBackupReserve` (CLI flag
  `--set-backup-reserve`, numbers 0-100) sends cmd 112
  (`SysBatChgDsgSet.sys_bat_dsg_down_limie`) — a real battery-setting write. Do
  not add any other write cmd ids (`*_SET`, reset, …) without an explicit user
  decision.
- Real device/pack serials and user IDs were scrubbed from tracked files before
  open-sourcing; keep them out. Live credentials come from the operator's env.
- The operator performs git remotes/pushes/PRs themselves — don't.
- Session cwd may be stale after directory renames (this workspace was once
  `ecoflow-android`); if bash fails with `NotFound: …/ecoflow-android`, pass an
  explicit existing workdir or `cd` inside the command.
