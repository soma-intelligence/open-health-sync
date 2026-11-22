# Record a Bluetooth heart rate strap, blood pressure cuff or scale without a phone

Run `ohs-bridge`, the Open Health Sync desktop bridge, on a Mac, a Linux machine, a Raspberry Pi
or a Windows PC near your devices. It connects to straps, cuffs, scales, glucose meters,
thermometers and pulse oximeters that speak the standard Bluetooth health profiles, keeps every
value on disk, and uploads it to your own Open Health Sync server. No vendor account, no vendor
app and no phone are involved.

## 1. Check that your device fits

A device works when it implements one of these Bluetooth SIG services:

| Service | Typical devices |
|---|---|
| Heart Rate `180d` | chest straps and arm bands (Polar H10, Wahoo TICKR, Garmin HRM straps), watches in heart rate broadcast mode |
| Blood Pressure `1810` | Bluetooth cuffs from OMRON, A&D, Beurer and others |
| Weight Scale `181d`, Body Composition `181b` | Bluetooth scales and body composition scales |
| Glucose `1808` | blood glucose meters such as Contour Next One and Accu-Chek Guide |
| Health Thermometer `1809` | Bluetooth thermometers |
| Pulse Oximeter `1822` | Bluetooth pulse oximeters |

Not supported: continuous glucose monitors, fitness machines (FTMS), cycling power and running
speed sensors, and devices that only speak a vendor protocol (Withings, Oura, WHOOP, Xiaomi
bands). Those come in through their own [connector](../connectors/README.md).

## 2. Build the bridge

It's one native binary written in Rust, in `bridge/`. With Rust 1.89 or later:

```sh
cd bridge
cargo build --release
sudo install -m 755 target/release/ohs-bridge /usr/local/bin/
```

| Platform | Bluetooth stack | Before building |
|---|---|---|
| macOS 11 or later | CoreBluetooth | nothing; macOS asks for the Bluetooth permission on the first run |
| Linux, Raspberry Pi OS included | BlueZ through D-Bus | `sudo apt install build-essential pkg-config libdbus-1-dev` (Debian, Ubuntu, Raspberry Pi OS) |
| Windows 10 and 11 | WinRT | nothing |

On a Raspberry Pi, use the 64-bit Raspberry Pi OS. Fedora: `sudo dnf install gcc pkgconf-pkg-config dbus-devel`.

## 3. Give it an upload token

The bridge reads two variables: `OHS_URL`, your server, and `OHS_TOKEN`, a user token with the
`upload` scope that can upload and nothing else. With the instance from the
[quickstart](../../README.md#quickstart) and the `OHS_URL`, `OHS_KEY` (tenant key) and
`OHS_USER` that `ohs admin personal setup` prints, mint one per person:

```sh
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"],"ttl_seconds":86400}' | jq -r .token)
```

A token lives one hour by default and at most 24 hours, so a bridge that runs for days needs a
new one when it expires (the bridge then reports the token as refused and retries every 5
minutes).

Plain `http://` is accepted for `localhost` only; `--allow-http` allows it on a network you trust.

## 4. Find and pair the devices

Wake the device (put the strap on, start a measurement), then:

```sh
ohs-bridge scan                          # nearby devices with a supported service
ohs-bridge watch --device "Polar H10"    # live values, uploads nothing
```

Cuffs and glucose meters usually need pairing once with the operating system: System Settings,
Bluetooth on macOS; `bluetoothctl` then `scan on`, `pair <address>` and `trust <address>` on
Linux; Settings, Bluetooth and devices, Add device on Windows. Straps, scales and thermometers
usually connect as they are.

## 5. Run it

```sh
ohs-bridge run --device "Polar H10" --device BLEsmart
```

`run` connects to the named devices (part of the advertised name, any case), reconnects when
they come back in range, sets the device clock when it allows it, and asks a glucose meter for
the records after the last one sent. A cuff or scale sends its stored readings when it connects.
On a scale or cuff shared by several people, add `--device-user N` (your index on the device,
0 to 254) so only your readings are stored. `ohs-bridge status` shows the queue and each device.

Values are spooled to disk, sealed into parts every minute, and sent oldest first, so a crash, a
reboot or a lost network loses nothing. A retry sends the same bytes and the server answers
`duplicate: true`.

## 6. Run it as a service

On Linux and the Raspberry Pi, put `OHS_URL` and `OHS_TOKEN` in `/etc/ohs-bridge.env`
(`chmod 600`) and use a systemd unit whose `User` is the account that paired the devices:

```ini
[Service]
User=pi
EnvironmentFile=/etc/ohs-bridge.env
ExecStart=/usr/local/bin/ohs-bridge run --device "Polar H10" --device BLEsmart
Restart=on-failure
```

```sh
sudo systemctl enable --now ohs-bridge && journalctl -u ohs-bridge -f
```

On macOS, a LaunchAgent (`~/Library/LaunchAgents/org.openhealthsync.bridge.plist`) runs it at
login; on Windows, a Task Scheduler task at log on. Both are spelled out in the
[bridge guide](../bridge.md#run-as-a-service), with the full systemd unit.

## What you get

The bridge never decodes: it sends the bytes each device sent, and the server decodes them
([mapping](../connectors/bluetooth.md#7-mapping)). Records have provider `bluetooth` and a source
slug from the manufacturer (`polar`, `omron`, `garmin`...).

| Device | Records |
|---|---|
| Heart rate strap | `heart_rate`, plus RR intervals per connection, read at `GET /v1/users/{id}/rr-intervals` |
| Blood pressure cuff | `blood_pressure_systolic` and `blood_pressure_diastolic` in mmHg, the pulse as `heart_rate` |
| Scale | `body_mass` in kg, `body_mass_index`, `body_fat_percentage` and the other body composition figures |
| Glucose meter, thermometer, pulse oximeter | `blood_glucose` (mg/dL), `body_temperature` (°C), `oxygen_saturation` |

The upload format has no field for a device address or a serial number, and a serial at the end
of an advertised name is dropped. The same bridge also uploads FIT files from a watch mounted
over USB (`--files`, see [importing activity files](fit-gpx-tcx-import.md)).

From a phone app instead, the [Swift](../sdks/swift.md#bluetooth-devices) and
[Kotlin](../sdks/kotlin.md#bluetooth-devices) SDKs upload the same values.
## Related

- [Import FIT, GPX and TCX activity files into PostgreSQL](fit-gpx-tcx-import.md)
- [Export Withings weight and blood pressure to CSV or SQL](withings-export.md)
- [Send Android Health Connect data to your own server](health-connect-to-server.md)
- [Desktop bridge reference](../bridge.md)
- [Bluetooth connector](../connectors/bluetooth.md)
- [README](../../README.md)
