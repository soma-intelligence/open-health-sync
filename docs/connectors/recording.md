# Recording a connector

A recording captures what a provider's real API answered a connector for a real account, anonymized
on your machine. Once committed under `test/recorded/testdata/<slug>/`, CI replays it on every
change.

It takes about five minutes per source. Nothing personal leaves your machine: the recorder
anonymizes in memory and refuses to write a file if a token or an original identifier survives.

## What you need

- Go (the version in `go.mod`) and a clone of this repository.
- An account with the provider that holds some recent data (a week is enough).
- For a cloud API: your own developer app with the provider (client id and secret), the same one a
  self-hosted instance uses. Each provider's guide says how to create it. Add
  `http://localhost:8765/callback` to the app's allowed redirect addresses. If the provider refuses
  a localhost address, set `OHS_RECORD_REDIRECT_URL` to one it accepts that forwards to port 8765
  on your machine.
- For Apple Health: an export from the Health app on your iPhone (profile picture, then *Export All
  Health Data*), copied to your computer as `export.zip`.

## Record a cloud API (Withings as the example)

```sh
export OHS_WITHINGS_CLIENT_ID=...       # your developer app
export OHS_WITHINGS_CLIENT_SECRET=...
make record PROVIDER=withings
```

The command prints the provider's consent address. Open it, sign in and allow access. The recorder
then pulls the last 7 days (`OHS_RECORD_DAYS`, at most 30), anonymizes the answers, replays them
through the connector to check they still map, and writes two files:

- `test/recorded/testdata/withings/<day>.json`, the recording;
- `test/recorded/testdata/withings/<day>.summary.json`, the record counts CI expects.

It prints counts only, never a value. The token exchange is not recorded. If you already hold an
access token, set `OHS_RECORD_ACCESS_TOKEN` (and `OHS_RECORD_PROVIDER_USER_ID` when the provider
needs one) to skip the consent page.

Keys that are not secret and change the API the connector calls (`OHS_DEXCOM_REGION`,
`OHS_OMRON_ENVIRONMENT`) are kept in the recording so the replay calls the same API. Every other
`OHS_<PROVIDER>_*` value is treated as a secret.

## Record Apple Health

```sh
OHS_RECORD_EXPORT=~/Downloads/export.zip make record PROVIDER=apple_health
```

The recorder converts the export the way `ohs import apple-health` does and keeps the last 7 days.
Of each type and day it keeps the first 10 lines (`OHS_RECORD_MAX_LINES`) and every workout, so a
workout's route is cut to a few points. It then anonymizes the lines and checks they replay through
the connector. Health Connect has no export file yet, so it cannot be recorded this way.

## Before you commit

1. Read the recording. It is plain JSON. Look for anything that looks like you: a name, a place, a
   device you named. If you find one, do not commit; open an issue saying which field it was (not
   its value), and we will add a rule.
2. Run `go test ./test/recorded/`.
3. Commit the two files in one pull request.

## How the anonymization works

The rules live in `internal/connector/cassette` (`AnonymizeHTTP`). In short:

| What | Becomes |
|---|---|
| Request headers, cookies, every response header but `Content-Type`, `Retry-After`, `Link` and rate limits | Dropped |
| Credentials (`access_token`, `client_secret`, `code` and the like) | `REDACTED` |
| Identifiers, names, free text, emails, device names, serial numbers, pagination tokens | A stand-in of the same shape, the same everywhere it appears |
| Dates and times (ISO strings, Unix seconds and milliseconds) | Moved by one secret number of weeks, forgotten after the recording |
| Measured values | Scaled by a factor between 0.95 and 1.05, type and decimals kept |
| Latitude, longitude, polylines, altitude, course | 0 or empty |
| Time zone names (`Europe/Paris`) | `Etc/UTC` |
| Birth dates | 1980-01-01 |
| A body that is not JSON | Dropped |

Codes and small integers (types, units, sleep stages, enumerations) stay as they are, because the
mapping depends on them. Only the day of the recording is kept unshifted, so we
know how fresh a recording is.

When the anonymized recording no longer maps (a provider puts something the mapping reads in a
field the rules change), the recorder stops and says so. The fix is to add that field to the
provider's `Keep` list in `test/recorded/recorded.go`, after checking it holds nothing personal.

## When a replay fails in CI

`go test ./test/recorded/` fails when the connector sends a request the recording never saw, no
longer sends one it saw, cannot map a recorded line, or maps to different counts. If your change
was meant to alter the requests or the mapping, record again (or, for a count change you meant,
run `go test ./test/recorded -update` and explain the diff in your pull request). Otherwise, the
failure is the point: the change breaks what a real account sends.
