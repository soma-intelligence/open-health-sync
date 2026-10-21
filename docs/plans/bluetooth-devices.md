# Plan: Bluetooth devices and the desktop bridge

Spec: [specs/bluetooth-devices.md](../specs/bluetooth-devices.md).

## Server (Go)

1. Tests first: byte vectors for every characteristic in the mapping table, written from
   the GATT Specification Supplement layouts with invented values: each flag that adds or
   moves a field, both unit systems, SFLOAT and FLOAT reserved values, device clocks (unset,
   before 2000, in the future, flagged as not set), shared device user indexes, glucose
   control solution, PLX status bits, truncated values. Envelope tests for `ParseUpload`
   (missing fields, bad hex, odd length, over 512 bytes, bad session, no line).
2. `connectors/bluetooth`: `gatt.go` (SFLOAT, FLOAT, Date Time, a bounds-checked reader),
   one decoder per characteristic, `source.go` (vendor slug and device names), `connector.go`
   (`ParseUpload`, `Map`). Registered in `connectors/all`.
3. `internal/model`: kPa conversions for the two blood pressure types (metric scoped, next to
   the glucose one).
4. Route `POST /v1/ingest/bluetooth`: `Deps.Bluetooth` in `internal/api`, wired in
   `internal/app`; OpenAPI path; regenerated server SDKs and Postman collection
   (`make sdk`); contract test in `test/contract`.
5. Docs: connector guide `docs/connectors/bluetooth.md` (auth flow, rate limits, backfill,
   upload contract, mapping table, provenance, testing), connectors table, API page,
   CHANGELOG.

## Desktop bridge (Rust, `bridge/`)

6. Crate skeleton, `Cargo.toml` with the dependencies of the spec, `Cargo.lock` committed,
   SPDX headers, `rust-toolchain` not pinned (stable).
7. Pure modules first, with unit tests: `hex`, `base64`, `line` (the upload line),
   `live` (heart rate, RR and battery decoding for the live view), `spool` (append, rotate,
   part files with headers), `queue` (ordering, answers to actions), `files` (walk, hash,
   seen set), `gatt` (UUIDs and RACP bytes), `cli` (arguments).
8. `upload`: ureq client, headers, retries; tested against a local `TcpListener` fake that
   answers 200, duplicate, 400, 401, 429 with Retry-After and 503.
9. `ble`: btleplug adapter, scan filter on supported services, connect, Device Information,
   Current Time, subscriptions, glucose RACP, reconnect with backoff. Not unit tested (needs
   a radio); kept thin over the pure modules.
10. Commands `scan`, `watch`, `run`, `files`, `flush`, `status`.
11. CI workflow `bridge.yml`: `cargo fmt --check`, `cargo clippy -D warnings`, `cargo test`,
    `cargo build --release` on Linux (with `libdbus-1-dev`), macOS and Windows; and an end to
    end job that uploads spooled lines and a FIT file to the compose stack.
12. Docs: `docs/bridge.md` (install, pair, run as a service with systemd or launchd, menu bar
    status line, USB watch folders by platform), README pointer.

## Mobile SDKs

13. Swift: `BluetoothRecorder` and `BluetoothPart` in `OpenHealthSync`,
    `OHSClient.uploadBluetoothPart`, tests on Linux and macOS; `OpenHealthSyncBluetooth`
    target (CoreBluetooth, compiled on Apple platforms only), built for iOS in CI.
14. Kotlin: `BluetoothRecorder`, `BluetoothPart`, `GattUuids` and
    `OhsClient.uploadBluetoothPart`, tests; Android glue (`BluetoothGatt` callback feeding the
    recorder) in `examples/android`, built by `examples.yml`.
15. SDK docs: Bluetooth sections in `docs/sdks/swift.md` and `docs/sdks/kotlin.md`.

## Later, each its own PR

- Release binaries for `ohs-bridge` in `release.yml` (needs the owner's yes on signing).
- Cycling power and running speed, FTMS, CGM.
- A native tray icon.
- Glucose Measurement Context (meal relation) joined on sequence number.

No migration. No new Go dependency. New language: Rust, in `bridge/` only.
