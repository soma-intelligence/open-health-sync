# Spec: vendor Bluetooth protocols in ohs-bridge

Status: draft for review (2026-10-18). Extends [bluetooth-devices.md](bluetooth-devices.md).
Polar measurement streams are built; the other vendors are a roadmap, in
[plans/vendor-bluetooth.md](../plans/vendor-bluetooth.md).

## Problem

The standard Bluetooth health profiles reach straps, cuffs, scales, meters, oximeters and
thermometers. They do not reach what people value most in their wearables: the raw ECG of a
Polar H10, the beat-to-beat PP intervals of an optical sensor, the night a WHOOP or a ring
recorded, the history a Garmin watch keeps. Each of those sits behind its maker's own
protocol, and today reaches Open Health Sync only through the maker's app and cloud, when a
connector exists, or through a phone store, when the app writes there.

Several of those protocols are documented by the maker (Polar) or reverse engineered in open
projects (WHOOP, Garmin, Oura, Xiaomi, Huawei, scales). Others are not reachable at all. This
spec says which ones `ohs-bridge` speaks, how a vendor protocol fits the upload contract, and
the rules every vendor driver follows.

## Goals

1. A vendor driver model in `ohs-bridge`: one module per protocol, free of I/O, that the
   Bluetooth transport drives, so the same driver can later run inside a phone app.
2. The upload contract carries vendor values: a 128-bit characteristic UUID, and a short
   context for values that cannot be read alone.
3. Polar Measurement Data first: ECG, PPG, accelerometer, PP intervals, gyroscope and
   magnetometer streams from Polar straps and optical sensors, PP intervals mapped to heart
   rate and RR intervals on the server.
4. An honest map of the other vendors: what is reachable, what it needs from the user, what
   it costs, in what order.

## Non-goals

- Continuous glucose monitors over Bluetooth. Every open implementation embeds vendor key
  material or depends on a closed library, a third-party server or a companion app, and the
  sensors are regulated medical devices. CGM data comes from Apple Health, Health Connect or
  an existing CGM app's local export.
- Fitbit, Apple Watch and Samsung Galaxy Watch over Bluetooth: no open implementation of
  their sync exists. Their data reaches Open Health Sync through Apple Health and Health
  Connect, which the SDKs already read.
- Fetching keys from a vendor account. When a protocol needs a key (Oura, Xiaomi, Amazfit,
  Withings ScanWatch), the person supplies it; the bridge never logs in to a vendor cloud.
- Uploading to a vendor cloud.

## What is reachable

From a survey of the open implementations (2026-10-18). Feasibility asks one question: can
the bridge speak the protocol, on a computer and later on a phone, without the vendor app?

| Device | Protocol source | What the person must do | Verdict |
|---|---|---|---|
| Polar H10, H9, Verity Sense, OH1, 360 | Polar's published PMD documents | Nothing | **Built.** Live streams; offline recordings next |
| WHOOP 4.0, 5.0, MG | whoop-rs (Rust, Apache-2.0), OpenStrap (AGPL-3.0) | Bond with the computer | High: live heart rate, RR, raw motion and PPG, history. Legal flag below |
| Garmin watches and HRM 600 | Gadgetbridge (AGPL-3.0-or-later) | Pair the watch with the bridge as its phone | Medium-high: live values and FIT files. The pairing likely replaces Garmin Connect's on that watch |
| Generic scales (about 50 brands) | openScale (GPL-3.0-or-later) | Nothing | High: simple protocols |
| Huawei and Honor bands | Gadgetbridge | Nothing (pairing runs locally) | Medium: a large protocol |
| Xiaomi, Amazfit, Zepp | Gadgetbridge | Supply the band's 16-byte key | Medium: rich data, key-gated |
| Oura Ring 3, 4, 5 | open_oura (Rust, MIT), open_ring (GPL-3.0) | Supply the ring's key, or reset the ring and give up the Oura app | Medium-high technically, key-gated |
| Ultrahuman Ring Air | Gadgetbridge | Nothing | Medium: decoders not yet checked on hardware |
| Withings Steel HR | Gadgetbridge | Nothing | Medium: an older device |
| Withings ScanWatch, Body scales | scanwatch_ble (MIT) | Supply a per-device secret from an undocumented Withings API | Low |
| Withings BPM cuffs | none found | | None, unless a model serves the standard blood pressure profile |
| Fitbit, Apple Watch, Samsung Galaxy Watch | none | | Not reachable over Bluetooth (non-goals) |
| FreeStyle Libre, Dexcom | xDrip+, Juggluco | | Out of scope (non-goals) |

Two limits hold for every vendor:

- One app at a time. Most devices serve one phone at a time and many stop streaming to a
  second client. A person using the bridge closes the vendor app, or accepts that the vendor
  app loses the device (Garmin, Xiaomi, Oura with its own key).
- Phones are stricter than computers. iOS runs a Bluetooth app in the background only for
  its declared accessories and wakes it rarely; a vendor app's own background pairing is not
  available to a third-party app. The phone shells of the plan target live sessions and
  periodic catch-up, not continuous background capture.

## Architecture

```
 device ──BLE──▶ transport (btleplug on a computer; later a phone's own Bluetooth API)
                   │ notifications, reads          ▲ subscribe, read, write
                   ▼                               │
                 driver (one per protocol, no I/O): a state machine
                   │ values to upload, with their context; a live line; a note
                   ▼
                 spool ─▶ parts queue ─▶ POST /v1/ingest/bluetooth ─▶ raw store ─▶ ETL decode
```

A driver is fed what the device sends and answers with the actions to take: subscribe to a
characteristic, read one, write bytes to one. It never touches the radio, a clock or a file.
The transport owns the connection, runs the actions, times out a driver that waits too long,
and hands every value the driver owns back to it. Why:

- the protocol logic is tested byte for byte without a radio, against the exchanges the
  vendor documents or the open projects record;
- the same driver runs on any transport: btleplug today, a phone's Bluetooth API through a
  thin binding later, without rewriting the protocol in Swift and Kotlin;
- a driver only starts streams and labels values. Decoding stays on the server, as for the
  standard profiles: the raw store keeps what the device sent, and a decoder fix re-maps old
  data.

The transport picks a driver from the characteristics a device serves, after the standard
profiles are set up. A device with no driver works as before.

The driver interface is its own crate, `ohs-driver`, and `ohs-bridge` is also a library whose
`main_with` takes more protocols. A driver kept outside this repository (WHOOP, open question 2)
builds its own binary on the bridge without forking it. Such a driver decodes on the client:
the server has no decoder for its bytes, so it sends `sample` lines (a declared type, value,
unit and time) that the server checks and stores as sent. The trade is that a decoder fix there
does not re-map old data. A driver is told whether the connection uploads (`run`) or only shows
(`watch`), and on `watch` takes no step that loses data on the device.

## Upload contract

Two changes to the line of [bluetooth-devices.md](bluetooth-devices.md#upload-contract):

| Field | Change |
|---|---|
| `char` | Also a full 128-bit vendor UUID (`fb005c82-02e7-f387-1cad-8acd2d8df0c8`), any case, stored in canonical lowercase form. A Bluetooth base UUID still reduces to its 16-bit form |
| `ctx` | New, optional. Hex, at most 64 bytes: what the driver knows that the value alone does not say, so that each line still decodes on its own. Absent for the standard profiles |

For Polar, `ctx` is the measurement type, then the settings the stream was started with,
then the conversion factor the device answered, all as PMD settings fields. A frame whose
stream the driver did not start goes up with an empty context and is stored, not mapped.

## Polar Measurement Data

Written from the documents Polar publishes next to its BLE SDK, not from the SDK's code
(its license allows distribution in object code only and treats translations of its code as
confidential): "Polar Measurement Data" (online measurement, 2024) for the UUIDs, commands,
settings, frame layouts and example exchanges; the offline measurement document for the
width of each setting; "Device Error Codes" for the answer codes; "PPI Data" for the PP
interval flags. The tests replay the documents' example exchanges byte for byte.

One connection: read the control point for the streams the device offers; for each stream
asked for, get its settings, start it with the highest sample rate, resolution, range and
channel count offered, and forward its frames. PP intervals start without settings. A stream
the device refuses, or does not answer within five seconds, is skipped with its reason.
`--polar` picks the streams on `watch` and `run`; the default is ECG and PP intervals.

Mapping on the server:

| Stream | Mapped to |
|---|---|
| PP intervals (frame type 0) | `heart_rate` (the last on-skin beat of the frame) and RR intervals, valid beats only: on skin, or skin contact not supported, and not flagged invalid. Metadata `polar_measurement: pp_interval` |
| ECG, PPG, accelerometer, gyroscope, magnetometer | Stored, not mapped (open question 1) |

The frame timestamp counts nanoseconds since 2000 on the device's own clock, which nothing
here sets: it spaces samples within a session and does not tell time. Times come from `at`.

The 2024 document words the PP interval flag bit 2 as "1 if sensor contact is not
supported", while Polar's "PPI Data" page and the open decoders read it as "supported". The
server follows the latter; a recording from a real optical sensor settles it.

## Rules for every driver

- License. Port only from code under AGPL-3.0, GPL-3.0, Apache-2.0, MIT, BSD or MPL-2.0, and
  keep its notice in the module. Never port code under a proprietary SDK license, a
  non-commercial license, or no license: those inform only through facts stated elsewhere.
  Each driver's module documentation names its sources.
- Keys. A key the protocol needs is the person's to supply, by flag or file. The bridge
  never fetches one from a vendor account and never ships one.
- No destructive command on an ordinary path. Factory reset, forgetting bonds, firmware
  update and reboot run only from an explicit command that says what it costs (the Oura
  own-key path resets the ring and locks out the Oura app).
- Names. A vendor's name appears only to say which devices work ("works with Polar H10"),
  never in the product's own name or branding.
- Medical. ECG, SpO2 and other clinical-looking values carry no diagnostic claim.

## Requirements

- Tests first. Each driver is tested on byte exchanges: the vendor's published examples when
  they exist, otherwise exchanges built from the cited sources with invented values, never a
  capture from a person.
- Server decoders for vendor values follow the standard ones: bounds-checked, a truncated
  value is a failed record naming the characteristic, unknown frames are stored and not
  mapped.
- No migration for the Polar work. No new Go or Rust dependency.
- Privacy as in the parent spec: no health value, device address or serial number in logs.

## Open questions

1. ECG storage. A night of H10 ECG is about 3.7 million voltage samples, several hundred
   megabytes as rows of `ecg_samples`. Options: map it as one ECG recording per minute (the existing ECG
   read routes serve it as is), map only on request (a session flagged by the person), or
   keep it in the raw store until an analysis needs it. Recommended: one recording per minute,
   with a per-user switch.
2. WHOOP. The protocol is open and well documented, but WHOOP has asked developers to take
   their apps down (2026). Ship the driver in this public repository, in a separate one, or
   not at all? Recommended: a separate repository, so a takedown cannot touch Open Health
   Sync.
3. Phones. Run the drivers on iOS and Android through a binding of the Rust core (UniFFI),
   or port each driver into the Swift and Kotlin SDKs? Recommended: the binding, since the
   vendor protocols are where the code is.
4. Hardware bench. Which devices to keep for release checks (a Polar H10 and a Verity Sense
   first), and who records the reference exchanges.
