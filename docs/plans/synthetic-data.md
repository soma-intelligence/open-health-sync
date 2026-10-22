# Plan: synthetic data for every provider

Spec: [specs/synthetic-data.md](../specs/synthetic-data.md).

## Done (branch `test/synthetic-data`)

1. Vendor documentation collected from the public pages of every provider, each file with its
   URL, date and SHA-256.
2. The generator: personas, timeline, signals, time zones (DST, travel), the emitter and its
   document scenarios (`internal/synth`).
3. The harness that runs lines through the worker's steps and checks the invariants
   (`internal/synth/synthtest`).
4. Renderers, written from the vendor documentation: Apple Health (SDK lines and export.zip),
   Oura, WHOOP, Polar, Withings, Strava, Fitbit (Google Health API), Dexcom, intervals.icu,
   Suunto, Omron, Health Connect (with the Xiaomi, Zepp and RingConn apps), FIT, GPX and TCX
   files, Bluetooth GATT devices, self reports.
5. Six bugs fixed in the validator, the unit table and the Health Connect mapper, each with a
   unit test.
6. A light run in `go test ./...` and a nightly full run (`.github/workflows/synthetic-data.yml`,
   `OHS_SYNTH_FULL=1`).

## Next

1. **Decide the open findings** (spec, "Seen and left as they are"): a lower bound for
   timestamps, an exclusive zero for glucose and HRV, a ceiling for body mass, the skipped
   HealthKit types. Each is a one line rule change plus a test once decided.
2. **Fetch level tests.** The renderers produce what each API answers; serving those answers
   from the existing fakes (`connectors/<provider>/<provider>fake`) would run `Fetch`, paging and the day
   grouping on the same data, not only `Map`.
3. **End to end.** Upload the device lines (Apple Health, Health Connect, Bluetooth, activity
   files, self reports) to a running server in the e2e flow and compare the read API with the
   timeline: a step count, a night, a workout per day.
4. **A command.** `ohs-synth --provider oura --scenario all --days 30 --seed 7 --out dir/` to
   write the lines and files to disk, for a demo instance or a load test.
5. **The four providers without a public schema.** Garmin (partner Health API), Ultrahuman
   (authenticated reference), Eight Sleep (no documentation) and the Mi Fitness export: add a
   renderer when a public document appears, or when a partner account gives access to the
   reference and its terms allow using it.
6. **Coverage report.** A test that lists, per provider, the documented fields no renderer
   emits, so a new API version shows up as a gap.
