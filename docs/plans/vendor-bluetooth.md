# Plan: vendor Bluetooth protocols in ohs-bridge

Spec: [specs/vendor-bluetooth.md](../specs/vendor-bluetooth.md).

## Polar measurement streams (this change)

1. Tests first: the driver against the example exchanges of Polar's published document
   (H10 ECG and accelerometer starts, Verity Sense accelerometer with its factor), settings
   over several packets, refusals and timeouts; the server against PP interval frames (on
   skin, off skin, contact not supported, invalid beat) and vendor lines in `ParseUpload`.
2. `bridge/src/vendor`: the `Driver` trait (actions in, steps out), `detect`, and
   `polar.rs`. `ble.rs` runs a driver's actions, routes its values and times it out; `line`
   gets `ctx` and 128-bit characteristics; `--polar` on `watch` and `run`.
3. Server: `char` accepts 128-bit vendor UUIDs, `ctx` is checked, `pmd.go` decodes PP
   intervals; the RR mapping skips invalid beats.
4. End to end: the bridge's flush test uploads a PP interval frame and an ECG frame and
   reads back heart rate and RR intervals.
5. Docs: connector guide (vendor services, contract, mapping), bridge guide (Polar
   streams), OpenAPI description, regenerated SDKs and collection, CHANGELOG.

## Next, each its own PR

6. ECG mapping, once open question 1 is answered.
7. Polar offline recordings (Verity Sense, 360): start and stop a recording, list and fetch
   its files over Polar's file transfer service, from the offline measurement document. The
   frames are PMD frames; the server reuses `pmd.go`.
8. Scales: the openScale drivers people ask for most, each file keeping its GPL-3.0 notice.
9. Garmin: Multi-Link and GFDI from Gadgetbridge, live heart rate, HRV and stress, and FIT
   files over the file transfer service, handed to the existing activity files route.
   Pairing as the watch's phone is a separate command that says it replaces Garmin
   Connect's pairing.
10. Oura with a key the person supplies (`--key-file`), from open_oura (MIT).
11. WHOOP, where open question 2 says.
12. Phones: a UniFFI binding of the driver and line modules, a Swift and a Kotlin transport
    that feed it, built in the existing SDK workflows.
13. Hardware bench: recorded exchanges from real devices (Polar H10, Verity Sense first),
    replayed by the tests.
