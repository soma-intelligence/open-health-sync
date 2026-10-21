# Desktop bridge: `ohs-bridge`

`ohs-bridge` runs on a computer or a Raspberry Pi next to your devices. It connects to heart rate
straps, blood pressure cuffs, scales, glucose meters, thermometers and pulse oximeters that use
the standard Bluetooth health profiles, keeps every value they send on disk, and uploads it to
your Open Health Sync server. It also picks up the FIT files of a watch mounted over USB. No
vendor account, no vendor app, no phone.

It is one native binary (about 3 MB), written in Rust, in [`bridge/`](../bridge). It does not
decode anything: each value goes up as the bytes the device sent, and the server decodes it
([Bluetooth connector](connectors/bluetooth.md)). The supported devices are listed there.

| Platform | Bluetooth stack | Notes |
|---|---|---|
| macOS 11 or later (Apple silicon and Intel) | CoreBluetooth | macOS asks for the Bluetooth permission on the first run |
| Linux, Raspberry Pi OS included | BlueZ through D-Bus | BlueZ and D-Bus, present on Raspberry Pi OS and desktop distributions |
| Windows 10 and 11 | WinRT | |

## Install

Build it from this repository with Rust 1.89 or later ([rustup](https://rustup.rs)):

```sh
cd bridge
cargo build --release
sudo install -m 755 target/release/ohs-bridge /usr/local/bin/
```

On Linux, the build needs the D-Bus headers first:

```sh
sudo apt install build-essential pkg-config libdbus-1-dev   # Debian, Ubuntu, Raspberry Pi OS
sudo dnf install gcc pkgconf-pkg-config dbus-devel          # Fedora
```

On a Raspberry Pi, use the 64-bit Raspberry Pi OS; the first build takes a while on the board.

## Configure

The bridge reads two environment variables:

| Variable | Value |
|---|---|
| `OHS_URL` | Your server, for example `https://ohs.example.org` |
| `OHS_TOKEN` | A user token (`ohs_ut_`) with the `upload` scope. It can upload and nothing else: it cannot read data back |

Mint the token with the tenant key, once per person:

```sh
export OHS=https://ohs.example.org
export KEY=ohs_sk_...    # the tenant key

USER_ID=$(curl -s -X POST $OHS/v1/users -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"me"}' | jq -r .id)
curl -s -X POST $OHS/v1/users/$USER_ID/tokens -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"scopes":["upload"]}' | jq -r .token
```

Plain `http://` is accepted for `localhost` only, since the token would travel in clear; pass
`--allow-http` to use it on a network you trust.

State (the queue, the status and the list of files already sent) lives in one directory, private
to your user (mode 0700):

| Platform | Directory |
|---|---|
| macOS | `~/Library/Application Support/ohs-bridge` |
| Linux | `$XDG_STATE_HOME/ohs-bridge`, else `~/.local/state/ohs-bridge` |
| Windows | `%LOCALAPPDATA%\ohs-bridge` |

`OHS_BRIDGE_STATE` sets another one. One bridge runs per state directory at a time.

## Find and pair your devices

Wake the device (put the strap on, start a measurement, press the cuff's Bluetooth button), then:

```console
$ ohs-bridge scan
scanning for 10 seconds...
name       signal   services
Polar H10  -58 dBm  heart rate
BLEsmart   -71 dBm  blood pressure
```

`--seconds N` scans longer, and `--all` also lists named devices that advertise no supported
service (a device can serve one without advertising it).

Straps, scales and thermometers usually connect as they are. Cuffs and glucose meters usually
need pairing, once, with the operating system:

- **macOS**: System Settings, Bluetooth, then Connect next to the device while it is in pairing
  mode. The first time the bridge runs, macOS asks whether it may use Bluetooth: from a terminal
  it asks for the terminal app, under launchd for `ohs-bridge` itself. To change the answer
  later: System Settings, Privacy and Security, Bluetooth.
- **Linux**: `bluetoothctl`, then `scan on`, `pair <address>` and `trust <address>`.
  The bridge's user needs access to BlueZ: on most distributions that is any user logged in at
  the console, or a member of the `bluetooth` group.
- **Windows**: Settings, Bluetooth and devices, Add device.

Many cuffs and meters show a passkey on their screen during pairing; the operating system asks
for it.

## See values live

```console
$ ohs-bridge watch --device "Polar H10"
looking for devices, Ctrl-C to stop
07:42:10 Polar H10 connected
07:42:10 Polar H10  battery 80%
07:42:11 Polar H10  64 bpm  RR 938 941 ms
```

`watch` uploads nothing: it is for checking that a device works and where to wear it. Without
`--device` it connects to every supported device in range.

## Run

```sh
ohs-bridge run --device "Polar H10" --device BLEsmart
```

`run` is the daemon. It connects to the named devices, reconnects when one comes back in range
(after a few seconds, then up to a minute apart), and uploads in the background. `--device NAME`
matches part of the advertised name, ignoring case, and can repeat; `--any` takes every device
that advertises a supported service, which is convenient alone at home and unwise in an office.

Each connection reads Device Information (manufacturer, model, firmware), sets the device clock
when the device allows it (so a cuff's stored readings carry the right time), subscribes to every
measurement, and asks a glucose meter for the records after the last one already sent (all of
them the first time). A cuff or scale that keeps readings in memory sends them when it connects.

On a scale or cuff shared by several people, each person holds a user index chosen on the device.
Pass yours with `--device-user N` (0 to 254): the server then stores only your readings.

Stop it with Ctrl-C, or SIGTERM from a service manager: it seals what it received before exiting,
and the next start (or `ohs-bridge flush`) sends it.

## Activity files from a watch over USB

Garmin and other watches that write FIT files sync without a phone when plugged in:

```sh
ohs-bridge run --device "Polar H10" --files /Volumes/GARMIN/Garmin/Activity
ohs-bridge files /Volumes/GARMIN/Garmin/Activity          # once, without Bluetooth
ohs-bridge files --watch /Volumes/GARMIN/Garmin/Activity  # keeps looking every 10 seconds
```

Each folder is searched (eight levels deep, symbolic links not followed) for `.fit` files of at
most 20 MiB. A file is sent once to the [activity files](connectors/activity-files.md) route,
with the same batch naming as `ohs import files`, so importing an export later adds no
duplicate. Files the server does not take as an activity (a monitoring or settings file) are
remembered and not tried again. File names are never printed or sent.

Where a watch appears when plugged in:

| Platform | Typical folder |
|---|---|
| macOS | `/Volumes/GARMIN/Garmin/Activity` |
| Linux | `/media/$USER/GARMIN/Garmin/Activity` |
| Windows | `E:\Garmin\Activity`, with the drive letter Windows gives it |

Point `--files` at the activity folder rather than the whole drive: the other folders hold
monitoring and settings files, which the server does not take. Many recent Garmin watches
connect with MTP instead of as a drive. On Linux, mount them first
(`jmtpfs ~/watch`, or the file manager's MTP mount under `/run/user/$UID/gvfs/`) and point
`--files` at the mount. On macOS and Windows, copy the activity folder with the vendor's tool or
an MTP client into a folder of your own and point `--files` at that folder.

## Status

```console
$ ohs-bridge status
state      /home/pi/.local/state/ohs-bridge
queue      0 waiting, 0 KiB
daemon     running (updated 2026-10-17 07:45:02)
last sent  2026-10-17 07:44:58
device     Polar H10: connected, last value 2026-10-17 07:45:01, 64 bpm, battery 80%
device     BLEsmart: not connected, last value 2026-10-17 06:30:12
```

`run --status-file PATH` also keeps a one-line status in a file of your choice, for a menu bar or
a panel:

```text
OHS ♥ 64 · 2 devices · sent 07:44
```

It shows the live heart rate while a strap is connected, the devices connected, the parts
waiting, the last upload, `not sending` while uploads are paused, and `OHS stopped` after the
bridge exits. It is rewritten when something changes, and at least once a minute.

- macOS: a [SwiftBar](https://github.com/swiftbar/SwiftBar) or [xbar](https://xbarapp.com)
  plugin, `ohs.5s.sh`, containing `cat "$HOME/Library/Application Support/ohs-bridge/line.txt"`,
  with the bridge run with `--status-file "$HOME/Library/Application Support/ohs-bridge/line.txt"`.
- Linux: a waybar `custom` module or an i3blocks block that runs `cat` on the same file.

Both the status file and `status.json` in the state directory hold your last heart rate. They
stay on this computer.

## Run as a service

### Linux (systemd)

`/etc/ohs-bridge.env`, readable by root only (`sudo chmod 600 /etc/ohs-bridge.env`):

```sh
OHS_URL=https://ohs.example.org
OHS_TOKEN=ohs_ut_...
```

`/etc/systemd/system/ohs-bridge.service`, with `User` set to the account that paired the devices:

```ini
[Unit]
Description=Open Health Sync bridge
After=bluetooth.target network-online.target
Wants=bluetooth.target network-online.target

[Service]
User=pi
EnvironmentFile=/etc/ohs-bridge.env
ExecStart=/usr/local/bin/ohs-bridge run --device "Polar H10" --device BLEsmart --status-file /home/pi/.local/state/ohs-bridge/line.txt
Restart=on-failure
RestartSec=10
NoNewPrivileges=true
ProtectSystem=full
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now ohs-bridge
journalctl -u ohs-bridge -f
```

### macOS (launchd)

`~/Library/LaunchAgents/org.openhealthsync.bridge.plist`, readable by you only
(`chmod 600`), since it holds the token:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>org.openhealthsync.bridge</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/ohs-bridge</string>
    <string>run</string>
    <string>--device</string><string>Polar H10</string>
    <string>--files</string><string>/Volumes/GARMIN/Garmin/Activity</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>OHS_URL</key><string>https://ohs.example.org</string>
    <key>OHS_TOKEN</key><string>ohs_ut_...</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/tmp/ohs-bridge.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/org.openhealthsync.bridge.plist
```

macOS then asks once whether `ohs-bridge` may use Bluetooth: the binary carries its own usage
description for that. After rebuilding the binary, macOS may ask again.

### Windows

Set `OHS_URL` and `OHS_TOKEN` as user environment variables (`setx OHS_URL https://...` and
`setx OHS_TOKEN ohs_ut_...`), then create a Task Scheduler task that runs
`ohs-bridge.exe run --device "Polar H10"` at log on.

## Queue and retries

Nothing received is lost on a crash, a reboot or a network loss:

1. each value is appended to a spool file in `spool/`;
2. every minute (or 5,000 values) the spool is compressed into a part in `parts/`, with its
   upload headers next to it, written and synced before the spool file is removed;
3. parts are sent oldest first. A part keeps its bytes from creation to deletion, so a retry is a
   byte-identical upload that the server answers `duplicate: true`.

| Server answer | What the bridge does |
|---|---|
| 200 | Deletes the part |
| 408, 429, 5xx, network error | Waits (`Retry-After`, else 1 second doubling up to 5 minutes) and tries again |
| 401, 403 | Stops uploading and says the token was refused; tries again 5 minutes later |
| 404, 405, a redirect | Stops uploading and says to check `OHS_URL` |
| Another 4xx | Moves the part to `rejected/`, with the server's answer next to it, and goes on |

`ohs-bridge flush` sends the queue now, for example after a long time offline with the daemon
stopped. `flush` and `files` cannot run while `run` holds the state directory; `run` already
does their work.

## Privacy

- The upload format has no field for a device address or a serial number. Advertised names that
  end in a serial (`Polar H10 0A1B2C3D`) are sent without it.
- Health values are printed by `watch` and `status`, and written to the status files, because
  you ask for them. The daemon's log has device names, counts and errors, never a value, and
  Bluetooth addresses are cut out of the error texts it relays.
- The state directory is private to your user. The token is never written to it.

## Develop

```sh
cd bridge
cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test
```

The unit tests cover everything but the radio: upload lines, the spool and queue, the answers
to each status code against a local fake server, FIT file discovery, arguments and the status
line. `test/e2e/bridge.sh` runs the bridge against the compose stack: it uploads spooled heart
rate and blood pressure values and two FIT files, and reads the samples, RR intervals and
workout back.
