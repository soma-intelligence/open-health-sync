# Bluetooth devices

Slug `bluetooth`. Heart rate straps, blood pressure cuffs, scales, glucose meters, thermometers
and pulse oximeters that speak the standard Bluetooth health profiles send their readings
straight to your own server: no vendor account, no partner program, no phone in the middle.
Polar straps and optical sensors also send what their own measurement service streams: PP
intervals, raw ECG and motion.

| Path | What it brings | How |
|---|---|---|
| Bluetooth devices | Heart rate with beat-to-beat (RR) intervals, blood pressure and pulse, weight, BMI and body composition, blood glucose, body temperature, SpO2 | [`ohs-bridge`](../bridge.md) on a computer or a Raspberry Pi, or an app built with the [Swift](../sdks/swift.md#bluetooth-devices) or [Kotlin](../sdks/kotlin.md#bluetooth-devices) SDK, forwards what the device sends to `POST /v1/ingest/bluetooth` |

The clients never decode: each value goes up as the bytes the device sent, with the time it
arrived, and the server decodes it. The raw store keeps the bytes, so a decoder fix re-maps
stored data.

## 1. Devices

A device works when it implements one of these Bluetooth SIG services. Vendor apps and
proprietary protocols are not used.

| Service | Characteristic | Typical devices |
|---|---|---|
| Heart Rate `180d` | Heart Rate Measurement `2a37` | Chest straps and arm bands (Polar H10, Wahoo TICKR, Garmin HRM straps), watches in heart rate broadcast mode |
| Blood Pressure `1810` | Blood Pressure Measurement `2a35` | Bluetooth cuffs from OMRON, A&D, Beurer and others |
| Weight Scale `181d` | Weight Measurement `2a9d` | Bluetooth scales |
| Body Composition `181b` | Body Composition Measurement `2a9c` | Body composition scales |
| Glucose `1808` | Glucose Measurement `2a18`, with the Record Access Control Point `2a52` | Blood glucose meters such as Contour Next One and Accu-Chek Guide |
| Health Thermometer `1809` | Temperature Measurement `2a1c` | Bluetooth thermometers |
| Pulse Oximeter `1822` | PLX Spot-check `2a5e` and Continuous `2a5f` Measurement | Bluetooth pulse oximeters |

`ohs-bridge scan` lists the nearby devices that advertise one of these services. Garmin, COROS
and other watches also sync their activities without a phone: mounted over USB, their FIT files
go to the [activity files](activity-files.md) route (`ohs-bridge files`).

### Vendor services

A few devices stream more on a service of their maker's than on the standard profiles. The
client runs that service's protocol (it starts the streams) and forwards the values like any
other, keyed by the characteristic's full UUID.

| Service | Data characteristic | Typical devices | Streams |
|---|---|---|---|
| Polar Measurement Data `fb005c80-02e7-f387-1cad-8acd2d8df0c8` | `fb005c82-02e7-f387-1cad-8acd2d8df0c8` | Polar H10 and H9 straps, Verity Sense, OH1 and 360 | ECG (H10: 130 Hz, microvolts), PPG, accelerometer, PP intervals (optical sensors), gyroscope, magnetometer |

`ohs-bridge` starts ECG and PP intervals by default and the other streams on request
(`--polar`, see [the bridge guide](../bridge.md#polar-streams)). Polar documents this protocol
with its BLE SDK; the bridge implements it from that description and uses no SDK code.

Not supported: continuous glucose monitors, fitness machines (FTMS), cycling power and running
speed sensors, and devices whose vendor protocol no client here speaks yet (Withings, Oura,
WHOOP, Xiaomi bands). Those reach Open Health Sync through their own connector or a phone store.

## 2. Pairing

Most cuffs and meters need a bond: pair them once with the operating system (System Settings,
Bluetooth on macOS; `bluetoothctl` then `pair` on Linux; Settings on iOS and Android, or the
system prompt the app triggers). Straps, scales and thermometers usually need none.

On a shared scale or cuff, each person holds a user index (often 1 or 2, chosen on the device).
Set it on the client (`ohs-bridge run --device-user 1`, or the SDK's `deviceUser`) so the other
person's readings are not stored in this account.

## 3. Auth flow

No OAuth and no operator keys: the connector is always on. The client uploads with a user token
(`ohs_ut_`) that has the `upload` scope, minted by your backend or with
`POST /v1/users/{id}/tokens`. The token cannot read anything back.

## 4. Rate limits

Nothing is pulled from a vendor. Uploads share the instance's limit of uploads in flight with the
other device routes (`503` with `Retry-After` above it). `ohs-bridge` and the SDKs queue parts and
retry `408`, `429`, `5xx` and network errors with backoff.

## 5. Backfill and updates

What the device keeps in memory is the backfill: a cuff or a scale sends its stored readings when
it connects, and a glucose meter answers the Record Access Control Point. `ohs-bridge` and the
SDK ask a meter for the records after the last sequence number they stored, and all records the
first time. Live values (heart rate, continuous SpO2) are only there while connected.

## 6. Upload contract

`POST /v1/ingest/bluetooth` works like the [Apple Health upload](apple-health.md): a user token
with the `upload` scope, the headers `X-OHS-Type` (always `gatt`), `X-OHS-Day`, `X-OHS-Batch` and
`X-OHS-Part`, and a gzip NDJSON body of at most 64 MiB. One line per value received:

```json
{"at":"2026-10-16T21:04:05.120+02:00","char":"2a37","value":"1048000433030004","session":"9f2c4e1a7b3d5c60","device":{"name":"Polar H10","manufacturer":"Polar Electro Oy","model":"H10","firmware":"5.0.0"}}
```

| Field | Required | Meaning |
|---|---|---|
| `at` | yes | RFC 3339 time the value arrived, with the uploader's UTC offset |
| `char` | yes | Characteristic UUID: 16-bit (`2a37`), the full Bluetooth base UUID form, or the full 128-bit UUID of a vendor characteristic (`fb005c82-02e7-f387-1cad-8acd2d8df0c8`), any case |
| `value` | yes, unless `sample` | The value's bytes in hex, 1 to 512 bytes |
| `sample` | instead of `value` | A value the client decoded itself, for a vendor protocol this server does not decode: `type` (a declared quantity type, or `rr_interval`), `value` (a finite number), `unit` (the type's storage unit or one that converts to it; `ms` for `rr_interval`), `start` and optional `end` (RFC 3339, not before 2000, not more than ten minutes after `at`). See below |
| `ctx` | no | Vendor values only: what the client sent to start the stream the value belongs to, in hex, at most 64 bytes, so each line decodes on its own. For Polar Measurement Data: the measurement type, then the settings of the start request and the factor the device answered, in the PMD settings encoding |
| `session` | yes | 8 to 64 letters, digits, `_` or `-`, chosen per connection to one device. It names the RR series |
| `device` | no | `name` (advertised name), `manufacturer`, `model`, `firmware` (Device Information service), at most 128 characters each |
| `device_user` | no | This person's user index (0 to 254) on a shared device |

Never send a device address or a serial number: the format has no field for them. Many devices
advertise a name that ends in a serial (`Polar H10 0A1B2C3D`): `ohs-bridge` and the SDKs drop a
last word of at least six hex digits, or four with a letter among them, and the server applies
the same rule to what it stores (`Edge 1040` is kept). `ohs-bridge`
sends `X-OHS-Day` as the UTC day of a part's first line, a batch `bt1-<install id>-<sequence>`
and part 0, and keeps each part's bytes until it gets a 200, so a retry is answered
`duplicate: true`.

The part is refused with 400, and nothing is stored, when a line is not a JSON object of this
format (a field missing, `at` without an offset, `value` or `ctx` not hex, `char` neither a
16-bit nor a 128-bit UUID), `X-OHS-Type` is not `gatt`, or the part holds no line. A value that does not decode (shorter than its flags say) is a failed
record of its own line, shown in sync health; the other lines are kept. A characteristic not in
the mapping (battery level, a vendor service) is stored and not mapped.

A client that speaks a vendor protocol the server does not decode (a driver built on
`ohs-bridge`, see [bridge.md](../bridge.md)) sends what it decoded instead of the bytes:

```json
{"at":"2026-10-18T07:12:00+02:00","char":"61080003-8d6d-82b8-614a-1c8cb0f8dcc6","sample":{"type":"skin_temperature","value":33.25,"unit":"celsius","start":"2026-10-18T03:00:00.000Z"},"session":"9f2c4e1a7b3d5c60"}
```

A line carries `value` or `sample`, never both. The part is refused when a sample's type is not
declared, its unit does not convert to the type's storage unit, its value is not finite, or its
times are out of bounds. The server cannot re-decode such a line, so a client decoder fix does
not re-map what was already sent.

## 7. Mapping

Values are read as the GATT Specification Supplement lays them out (little endian; SFLOAT and
FLOAT are the IEEE 11073-20601 16-bit and 32-bit floats, whose NaN, NRes and infinity values give
no sample).

| Characteristic | Field | Canonical record |
|---|---|---|
| Heart Rate Measurement `2a37` | heart rate | `heart_rate`, bpm, at `at`. None when the sensor reports no skin contact, or 0 |
| | RR intervals (1/1024 s) | RR intervals in ms, to the microsecond, series `session`. None without skin contact |
| Blood Pressure Measurement `2a35` | systolic, diastolic (mmHg or kPa) | `blood_pressure_systolic`, `blood_pressure_diastolic`, stored in mmHg. Neither when one is not a number |
| | mean arterial pressure | metadata `mean_arterial_pressure`, mmHg |
| | pulse rate | `heart_rate`, metadata `context` `blood_pressure` |
| | measurement status | metadata `measurement_status`: `body_movement`, `cuff_too_loose`, `irregular_pulse`, `pulse_rate_out_of_range`, `improper_position` |
| Weight Measurement `2a9d` | weight (0.005 kg or 0.01 lb) | `body_mass`, stored in kg. None for 0xFFFF (unsuccessful) |
| | BMI, height | `body_mass_index`; `height`, stored in cm |
| Body Composition Measurement `2a9c` | body fat (0.1 %) | `body_fat_percentage`. None for 0xFFFF |
| | muscle mass, fat free mass, body water mass, weight, height | `muscle_mass`, `lean_body_mass`, `water_mass`, `body_mass`, `height` |
| Glucose Measurement `2a18` | concentration (kg/L or mol/L) | `blood_glucose`, stored in mg/dL; metadata `sequence_number`, `specimen` (`capillary_whole_blood`... `interstitial_fluid`), `sample_location`, `sensor_status`. None for control solution |
| Temperature Measurement `2a1c` | temperature (°C or °F) | `body_temperature`, stored in °C; metadata `measurement_location` (`armpit`, `ear`, `mouth`...) |
| PLX Spot-check Measurement `2a5e` | SpO2, pulse rate | `oxygen_saturation` (percent); `heart_rate`, metadata `context` `pulse_oximeter` |
| PLX Continuous Measurement `2a5f` | SpO2PR-Normal | the same, at `at` |
| Polar Measurement Data `fb005c82-...`, PP interval frames | heart rate | `heart_rate`, bpm, at `at`, metadata `polar_measurement` `pp_interval`. None off the skin |
| | PP intervals (ms) | RR intervals in ms, series `session`. One flagged as not valid is left out, and still places the beats before it in time. None off the skin |
| Any characteristic, a `sample` line | | A quantity of its `type` in its storage unit, from `start` to `end` (or `start`), metadata `decoded_by` `client` and `clock` `device`; an `rr_interval` is an RR interval of series `session` at `start`. Hashed on type, start and value, so a record the device sends again maps to the same row |
| Polar Measurement Data, ECG, PPG and motion frames | | Stored, not mapped yet: a continuous waveform (130 ECG samples a second) needs its own storage decision. The raw store keeps every frame with its `ctx`, so a later decoder maps what was already sent |

- Pulse oximeter status: data for demonstration or testing, measurement unavailable and invalid
  measurement give no sample; early estimated data and questionable measurement are kept and
  listed in `measurement_status`.
- Shared devices: with `device_user` on the line, a blood pressure, weight or body composition
  value that carries another user index is not mapped. The "unknown user" index (255) is mapped.
- A body composition measurement split over two packets is mapped packet by packet.
- Glucose `specimen` `interstitial_fluid` is coded in FHIR with its own LOINC, as for Dexcom.
- Every sample carries metadata `gatt_characteristic`, `session`, `clock`, `utc_offset_minutes`,
  `timezone` (an `Etc/GMT` zone when the offset is a whole hour), `firmware` and `device_user`
  when known.

## 8. Time

- Heart rate, RR, PP intervals and continuous SpO2 have no time of their own: they are stamped
  with `at`. The RR intervals of one notification end at `at`, the last beat then, each earlier
  beat at the next one minus the next interval. Polar frames carry a timestamp on the device's
  own clock, which nothing sets: it spaces samples and does not tell time.
- Other measurements may carry the device's clock (a wall time without zone). It is read in the
  offset of `at` and used when it is a full date, not before 2000-01-01, not more than 10 minutes
  after `at`, and not flagged by the device as unset (pulse oximeter) or faulty (glucose).
  Otherwise the sample is stamped `at`. Metadata `clock` says which: `device` or `received`.
- Glucose: the base time plus the time offset field.
- `ohs-bridge` and the SDKs set the device's clock through the Current Time service when the
  device allows it, so stored readings carry a right time.

## 9. Provenance, and other sources of the same reading

- Provider `bluetooth`. Source slug from the manufacturer, then the device name: `polar`,
  `garmin`, `wahoo`, `omron`, `withings`, `beurer`, `and` (A&D), `ihealth`, `nonin`, `masimo`,
  `roche`, `ascensia`, `coros`, `suunto`, `scosche`, `decathlon`; otherwise `bluetooth`.
- `device` is the vendor and model (`Polar H10`), `device_model` the model number,
  `source_name` the advertised name without a serial-like last word. `was_user_entered` is
  false.
- Readings are hashed on user, provider, source, type, time and value. A cuff that sends its
  memory again on the next connection, or a meter asked for all records, gives the same rows.
- The same reading may also reach the server through a vendor cloud (OMRON, Withings, Polar) or a
  phone store (Apple Health, Health Connect). Each provider keeps its copy; the merged view
  (`view=merged`, the default) shows the best ranked source, in the user's
  [source priorities](../api.md) or the defaults.

## 10. Testing

`go test ./connectors/bluetooth/` runs the mapping tests: byte vectors built from the GATT
Specification Supplement layouts and Polar's published frame layouts with invented readings, for every flag that adds or moves a
field, both unit systems, the float special values, device clocks, shared device indexes, glucose
control solution and pulse oximeter status bits, and truncated values. `test/contract` uploads a
part to a running server and reads the samples and RR intervals back. No test uses a capture
from a person.
