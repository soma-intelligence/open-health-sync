# Spec: Bluetooth devices and the desktop bridge

Status: draft for review (2026-10-17). Roadmap item D15 (standard Bluetooth sensors).

## Problem

A chest strap, a blood pressure cuff, a scale, a glucose meter, a pulse oximeter or a
thermometer reaches Open Health Sync today only through a vendor cloud (OMRON, Withings,
Polar) or through a phone store (Apple Health, Health Connect), when the vendor app writes
there. Each path costs something:

- a vendor cloud needs a partner program or a developer app per vendor, and the data leaves
  the person's hands before it reaches their own server;
- a phone store keeps what the vendor app chose to write: Apple Health and Health Connect
  hold heart rate, but most apps never write a strap's beat-to-beat (RR) intervals, and a
  glucose meter without an app that writes there is invisible;
- both need a phone in the middle, even when the device sits next to a computer or a
  Raspberry Pi that already runs the server.

Most of these devices speak the same open standard: the Bluetooth SIG GATT profiles for
health devices. A Polar H10, a Wahoo TICKR or a Garmin HRM strap sends the Heart Rate
Measurement characteristic with RR intervals; many OMRON, A&D and Beurer cuffs send Blood
Pressure Measurement; Contour, Accu-Chek and other meters serve Glucose Measurement through
the Record Access Control Point. One decoder for the standard covers hundreds of devices,
without a vendor account, a partner program or a phone.

A desktop menu bar app for Garmin watches (wristbeam, MIT, 2026) showed the appeal of
the direct path: the watch talks to the computer, no phone app in the middle, live values in
view. It does it with Garmin's private watch protocol. This spec takes the idea, not that
protocol: standard profiles for live sensors, and the files the watch already writes for
activities. No code is taken from it.

## Goals

1. A device connector `bluetooth` and route `POST /v1/ingest/bluetooth` that stores the
   values a device sent, byte for byte, and decodes them on the server into canonical
   samples with provenance.
2. A desktop bridge, `ohs-bridge`, in Rust: macOS, Linux (Raspberry Pi included) and
   Windows. It connects to standard Bluetooth health devices, keeps what they send in an
   on-disk queue, and uploads it with a user token. It also watches a folder (a watch
   mounted over USB) and uploads new FIT files to the existing activity files route.
3. The same capture in the Swift SDK (CoreBluetooth, iOS and macOS) and the Kotlin SDK
   (line and part building, with Android glue in the example app), so a mobile app can add
   a strap or a cuff without writing a decoder.
4. A live view without a cloud: `ohs-bridge watch` prints heart rate, RR and the device
   battery as they arrive, and `ohs-bridge run --status-file` keeps a one-line status that
   menu bar tools (SwiftBar, xbar, waybar) can show.

## Non-goals

- Garmin's private watch protocol (GFDI) and its live stress, Body Battery and steps: it is
  reverse engineered from a vendor app, works on few verified models, and the Garmin partner
  application is pending. A Garmin watch still syncs without a phone here: activities through
  its FIT files over USB, live heart rate through its standard heart rate broadcast.
- Uploading to a vendor cloud (Garmin Connect, Polar Flow). Data goes to the person's own
  server only.
- Vendor-specific Bluetooth protocols (Withings, Oura, WHOOP, Xiaomi bands). Their cloud or
  phone store paths stay.
- Continuous glucose monitors (the CGM Service needs session handling and, for most
  sensors, vendor encryption), fitness machines (FTMS), cycling power and running speed.
  Listed in the plan as next steps.
- A native tray icon in v1. The status file covers the menu bar; a tray is a later step.

## Architecture

```
 strap, cuff, scale,          ohs-bridge (Rust)              Open Health Sync
 meter, oximeter,   BLE GATT  on a laptop or a Pi    HTTPS   POST /v1/ingest/bluetooth
 thermometer      ─────────▶  spool ─▶ parts queue  ───────▶ raw store (bytes as sent)
                                                              ETL: decode, map, provenance
 watch over USB   ──files───▶ folder watch          ───────▶ POST /v1/ingest/activity-files
 (FIT)                                                        (existing route)

 iOS / macOS app: Swift SDK, OpenHealthSyncBluetooth (CoreBluetooth) ─▶ same route
 Android app: Kotlin SDK BluetoothRecorder + BluetoothGatt glue      ─▶ same route
```

One decoder, on the server. Clients forward characteristic values as they arrive, as hex,
with the time they arrived. Why:

- raw first: the raw store keeps what the device sent, and a decoder fix re-maps old data
  by re-running the ETL, as for every other connector;
- clients stay thin and alike in three languages: scan, connect, subscribe, timestamp,
  queue, upload. A decoding bug is fixed once, in Go, under test;
- the bridge decodes only what its live view shows (heart rate, RR, battery).

## Upload contract

Route `POST /v1/ingest/bluetooth`, user token with the `upload` scope, the same headers,
limits, idempotency and answers as the other device routes. `X-OHS-Type` is `gatt`.

One line per characteristic value received (a notification, an indication or a read):

```json
{"at":"2026-10-16T21:04:05.120+02:00","char":"2a37","value":"1640e803","session":"9f2c4e1a7b3d5c60",
 "device":{"name":"Polar H10 0A1B2C3D","manufacturer":"Polar Electro Oy","model":"H10","firmware":"5.0.0"}}
```

| Field | Required | Meaning |
|---|---|---|
| `at` | yes | RFC 3339 time the value arrived, with the uploader's UTC offset. The offset also places device clocks (below) |
| `char` | yes | Characteristic UUID: 16-bit short form (`2a37`) or the full 128-bit form of the Bluetooth base UUID, any case |
| `value` | yes | The value bytes, hex, at most 512 bytes (the ATT maximum) |
| `session` | yes | 8 to 64 letters, digits, `_` or `-`, chosen by the uploader for one connection to one device. It names the RR series |
| `device` | no | `name` (advertised local name), `manufacturer` (Device Information 0x2A29), `model` (0x2A24), `firmware` (0x2A26). Strings of at most 128 characters. Serial numbers and addresses are never sent |
| `device_user` | no | The user index (0 to 254) this person holds on a shared device (a family scale or cuff). Measurements that carry another user index are not mapped |

`ParseUpload` refuses a part (400, nothing stored) when a line is not a JSON object, a
required field is missing or malformed, `value` is not even-length hex or is too long, or
the part holds no line. It does not decode values: a value that does not decode is a
failed record in the ETL (visible in sync health), and the other lines of the part are
stored.

A characteristic outside the table below gives an empty result, no error (battery level
included: device status, not health data).

## Mapping

Values are little endian, as the Bluetooth Core spec defines; SFLOAT is the 16-bit and
FLOAT the 32-bit IEEE 11073-20601 float. Their reserved values (NaN, NRes, +INF, -INF)
give no sample. Units follow the GATT Specification Supplement and are stored in the
canonical units of [data-model.md](../data-model.md) through the ETL's unit conversion.

| Characteristic (service) | Field | Canonical record |
|---|---|---|
| Heart Rate Measurement `2a37` (Heart Rate `180d`) | heart rate (uint8 or uint16) | `heart_rate`, bpm, at `at`; skipped when the sensor reports no skin contact, or 0 |
| | RR intervals (uint16, 1/1024 s) | `HeartbeatSample` per interval, ms, series `session` |
| Blood Pressure Measurement `2a35` (`1810`) | systolic, diastolic (SFLOAT, mmHg or kPa) | `blood_pressure_systolic`, `blood_pressure_diastolic` |
| | mean arterial pressure | metadata `mean_arterial_pressure` (mmHg) on both |
| | pulse rate (SFLOAT) | `heart_rate`, bpm, metadata `context: blood_pressure` |
| | measurement status | metadata `measurement_status`: `body_movement`, `cuff_too_loose`, `irregular_pulse`, `pulse_rate_out_of_range`, `improper_position` |
| Weight Measurement `2a9d` (Weight Scale `181d`) | weight (uint16: 0.005 kg or 0.01 lb) | `body_mass` |
| | BMI (0.1), height (0.001 m or 0.1 in) | `body_mass_index`, `height` |
| Body Composition Measurement `2a9c` (`181b`) | body fat (0.1 %) | `body_fat_percentage` |
| | muscle mass, fat free mass, body water mass, weight, height | `muscle_mass`, `lean_body_mass`, `water_mass`, `body_mass`, `height` |
| Glucose Measurement `2a18` (Glucose `1808`) | concentration (SFLOAT, kg/L or mol/L) | `blood_glucose`, mg/dL; metadata `sequence_number`, `specimen`, `sample_location` |
| Temperature Measurement `2a1c` (Health Thermometer `1809`) | temperature (FLOAT, °C or °F) | `body_temperature`; metadata `measurement_location` |
| PLX Spot-check Measurement `2a5e` (Pulse Oximeter `1822`) | SpO2, pulse rate (SFLOAT) | `oxygen_saturation`, percent; `heart_rate`, bpm, metadata `context: pulse_oximeter` |
| PLX Continuous Measurement `2a5f` (`1822`) | SpO2PR-Normal | same, at `at` |

Rules:

- A weight or mass of 0xFFFF, a body fat of 0xFFFF ("measurement unsuccessful") give no
  sample. A glucose sample of type control solution (type 10 or location 4) gives none: it
  is a meter check, not a reading. Interstitial fluid (type 9) sets `specimen` to
  `interstitial_fluid`, as Health Connect glucose does, so FHIR codes it with its own LOINC.
- PLX measurement status: "data for demonstration", "data for testing", "measurement
  unavailable" and "invalid measurement detected" give no sample; "questionable measurement
  detected" and "early estimated data" are kept and listed in `measurement_status`.
- Shared devices: with `device_user` set on the line, a blood pressure, weight or body
  composition measurement that carries a different user index is not mapped. 0xFF ("unknown
  user") is mapped.
- A body composition measurement sent in two packets (flag bit 12) is mapped packet by
  packet; the packet without a time stamp falls back to `at`.

## Time

- Heart rate, RR and continuous SpO2 carry no time: the sample is stamped `at`. RR
  intervals of one notification end at `at`: the last interval's beat is stamped `at`, each
  earlier one at the following beat's time minus the following interval. A notification
  arrives within one connection interval of the last beat, so the error is tens of
  milliseconds.
- The other measurements may carry a time stamp (the device's wall clock, no zone, often
  from a memory of past readings). It is read in the UTC offset of `at`. It is used when
  it is a real date (year, month and day set), not before 2000-01-01, not more than
  10 minutes after `at`, and the PLX flag "device clock is not set" is clear. Otherwise the
  sample is stamped `at` and metadata `clock` is `received` instead of `device`, so a
  reader can tell a reading time from an upload time.
- Glucose: base time plus the time offset field, in minutes.
- `timezone` metadata: an `Etc/GMT` zone from the offset of `at` when it is a whole hour,
  and `utc_offset_minutes` always, as the activity files connector does.
- The bridge and the SDKs set the device clock when it exposes a writable Current Time
  characteristic (`2a2b`), so stored readings get a right time stamp.

## Provenance

- Provider `bluetooth`.
- Source slug from the manufacturer name, then the device name: `polar`, `garmin`,
  `wahoo`, `omron`, `withings`, `beurer`, `and` (A&D), `ihealth`, `nonin`, `masimo`,
  `roche` (Accu-Chek), `ascensia` (Contour), `coros`, `suunto`, `scosche`, `decathlon`;
  otherwise `bluetooth`.
- `DeviceModel` the model number string, `DeviceID` the vendor's display name and model
  (`Polar H10`), `SourceName` the advertised name with any trailing serial-like token
  removed.
- `WasUserEntered` false: a device measured it.
- Metadata on every sample: `gatt_characteristic`, `session`, `clock`, `firmware` when
  sent.

Fusion: these samples rank like any other source. A chest strap is the reference for heart
rate and RR; whether `polar`, `garmin` and the strap vendors should rank above wrist sources
in the heart group by default is an open question below.

## Deduplication

- The same part twice: the usual `duplicate: true`.
- The same reading twice (a cuff resends its memory on each connection, a meter answers
  "all records"): samples are hashed on user, provider, source slug, type, time and value,
  so the copy collapses. RR samples are hashed on series, user, time and value: the same
  notification re-sent in another session would be stored twice, which the bridge avoids by
  never re-sending a part it got a 200 for.
- The bridge asks a glucose meter only for records after the last sequence number it
  stored for that meter.

## The desktop bridge, `ohs-bridge`

Directory `bridge/`, one Rust crate, one binary. Commands:

| Command | Does |
|---|---|
| `ohs-bridge scan [--seconds N]` | Lists nearby devices that advertise a supported service: name, services, signal |
| `ohs-bridge watch [--device NAME]` | Connects and prints heart rate, RR and battery as they arrive. Uploads nothing |
| `ohs-bridge run --device NAME... [--files DIR...] [--status-file PATH]` | The daemon: connects (and reconnects) to the named devices, spools every value, uploads parts; optionally watches folders for FIT files and keeps a one-line status file |
| `ohs-bridge files DIR... [--watch]` | Uploads new `.fit` files from folders (a watch mounted over USB) to `POST /v1/ingest/activity-files` |
| `ohs-bridge flush` | Uploads queued parts now |
| `ohs-bridge status` | Queue length, last upload, devices seen, the last live values |

Configuration: `OHS_URL` and `OHS_TOKEN` (a user token, `ohs_ut_`, upload scope only),
flags for the rest. State lives in one directory (`OHS_BRIDGE_STATE`, by default
`~/Library/Application Support/ohs-bridge` on macOS, `$XDG_STATE_HOME/ohs-bridge` or
`~/.local/state/ohs-bridge` on Linux, `%LOCALAPPDATA%\ohs-bridge` on Windows), mode 0700.

Queue, crash safe:

1. each value is appended as a line to the open spool file, `spool/<id>.ndjson`;
2. every 60 seconds (or 5,000 lines) the spool is closed, gzip compressed into
   `parts/<id>.gz` with a `parts/<id>.json` holding its headers (`X-OHS-Day` the UTC day of
   its first line, batch `bt1-<install id>-<sequence>`, part 0), and the spool file removed
   once both are written and synced;
3. the uploader sends parts oldest first. 200 (stored or duplicate) deletes the part; 401
   and 403 stop uploads and say the token was refused (404, 405 and redirects stop them too:
   the URL is wrong); 408, 429 and 5xx back off
   (Retry-After, else 1 s doubling to 5 minutes); another 4xx moves the part to `rejected/`
   with the server's message.

A part keeps its bytes and headers from creation to deletion, so a retry after a crash or a
timeout is a byte-identical re-send, answered `duplicate: true`.

Files: a folder is walked for `.fit` files (any case), at most 20 MiB each. A file is
uploaded as one activity files part, the same line and batch naming as `ohs import files`
(`af1-<first 32 hex of its SHA-256>`, part 0, day the UTC day of its modification time). Its
hash is then recorded in `files.seen`; a file the server refuses as not an activity (a
monitoring or settings file) is recorded too and not retried. File names are never printed
or sent.

Device handling:

- Subscribe (notify or indicate) to every supported characteristic the device serves.
- Read Device Information (manufacturer, model, firmware) once per connection.
- Write Current Time when the device lets us.
- Glucose: subscribe to Glucose Measurement and the Record Access Control Point, then
  write "report stored records, greater than or equal to sequence number N" with N the last
  stored plus one (all records the first time).
- Battery level: read, and subscribe when notified. Kept for the live view and status only.
- Pairing: devices that need bonding (most cuffs and meters) are paired once with the
  operating system (System Settings on macOS, `bluetoothctl` on Linux).

Privacy: the bridge never prints or logs a health value outside `watch`, `status` and the
status file (which the user asks for), never sends a device address or serial number, and
keeps its state directory private to the user.

### Dependencies

A new language in the repository: Rust, for a native binary that talks to the Bluetooth
stack of each platform without a runtime. The crate is `bridge/`, outside the Go module;
the `ohs` binary does not change size.

| Crate | Why | License |
|---|---|---|
| `btleplug` | Bluetooth LE central on CoreBluetooth (macOS), BlueZ (Linux) and WinRT (Windows) behind one API. Writing three platform bindings is the alternative | MIT, Apache-2.0 or BSD-3-Clause |
| `tokio` | The async runtime `btleplug` runs on | MIT |
| `futures-util` | Stream helpers for notification streams | MIT or Apache-2.0 |
| `uuid` | Characteristic UUIDs, the type `btleplug` uses | MIT or Apache-2.0 |
| `ureq` | Small blocking HTTPS client (rustls) for uploads | MIT or Apache-2.0 |
| `flate2` | gzip, pure Rust backend | MIT or Apache-2.0 |
| `serde`, `serde_json` | Lines, part headers and state files | MIT or Apache-2.0 |
| `sha2` | SHA-256 of FIT files, for the same batch ids as `ohs import files` | MIT or Apache-2.0 |
| `chrono` (clock and serde only) | The local UTC offset of `at`, and the times in the status file; the standard library has no local time zone | MIT or Apache-2.0 |

All compatible with AGPL-3.0. Hex and base64 are written by hand (a few lines each);
arguments are parsed by hand.

On Linux `btleplug` reaches BlueZ through D-Bus: building needs `libdbus-1-dev`, running
needs BlueZ and D-Bus, present on Raspberry Pi OS.

## Mobile SDKs

- Swift: `BluetoothRecorder` (platform neutral, in `OpenHealthSync`) builds lines and
  parts; `OHSClient.uploadBluetoothPart` sends them; a new product
  `OpenHealthSyncBluetooth` (CoreBluetooth, iOS and macOS) scans, connects, subscribes,
  reads Device Information, sets the clock, asks glucose meters for new records and feeds
  the recorder. iOS needs `NSBluetoothAlwaysUsageDescription`.
- Kotlin: `BluetoothRecorder` and `OhsClient.uploadBluetoothPart` in the SDK (plain JVM, as
  the SDK is today). The Android `BluetoothGatt` glue lives in the Android example app,
  which CI already builds; it moves into an Android library module once the SDK gets one.
- Both reuse the GATT UUIDs and RACP bytes defined in one table in this spec. Neither
  decodes values.

## Standards

- Bluetooth SIG: Heart Rate Service 1.0, Blood Pressure Service 1.1, Weight Scale Service
  1.0, Body Composition Service 1.0, Glucose Service 1.0, Health Thermometer Service 1.0,
  Pulse Oximeter Service 1.0.1, Device Information Service 1.1, Current Time Service 1.1,
  and the GATT Specification Supplement for the characteristic formats.
- IEEE 11073-20601 SFLOAT and FLOAT.
- HL7 FHIR Personal Health Device Implementation Guide: these are the device types it
  profiles. Output stays the existing FHIR R4 Observations, with the device in the
  Observation's `device` and Provenance.
- Canonical units: `bpm` (UCUM `/min`), `ms`, `mmHg` (UCUM `mm[Hg]`), `kg`, `cm`, `kg/m2`,
  `percent`, `mg/dL`, `celsius`.

## Requirements

- Auth flow, rate limits (no vendor call; the shared in-flight upload limit; the bridge's
  backoff), backfill (what the device keeps in memory: a cuff's or a meter's stored
  readings arrive on the first connection), mapping table and provenance: in the connector
  guide, `docs/connectors/bluetooth.md`.
- Tests first. Mapping tests use byte vectors built from the GATT Specification Supplement
  layouts with invented values, never a capture from a person. Every flag combination that
  moves a field is covered; truncated values give an error naming the characteristic.
- No migration. No new Go dependency.
- Privacy: no health value, device address or serial number in logs. The line format has no
  field for an address or a serial.

## Open questions

1. Rust in the repository: a new language for contributors and CI. The alternative is a Go
   bridge on a Go Bluetooth library (`tinygo-org/bluetooth`), one language, but with a less
   complete macOS and Windows story. Recommended: Rust, as asked.
2. Heart group order: should chest strap vendors (`polar`, `wahoo`, `garmin` straps) rank
   first for `heart_rate` and RR by default? A strap is the reference, but a wrist watch of
   the same vendor shares the slug.
3. Release: ship `ohs-bridge` binaries (macOS arm64 and x86_64, Linux arm64 and x86_64)
   with each release, and sign the macOS binary? Signing needs an Apple Developer account.
4. Hardware bench: which devices to keep for release checks (a Polar H10 and one cuff
   are enough to start), and whether a self-hosted runner with Bluetooth runs them.
5. Next profiles: cycling power and running speed (sensors and smart trainers), FTMS, and
   CGM, in that order.
