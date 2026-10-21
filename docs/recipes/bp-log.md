# Blood pressure log

A list of your blood pressure readings with their averages, to print or send before a doctor's
appointment, from any cuff that reaches your server.

```sh
go run ./recipes/bp-log                  # the last 30 days
go run ./recipes/bp-log -days 90
go run ./recipes/bp-log -csv > bp.csv    # the readings as CSV, for a spreadsheet
```

```text
Date                        mmHg  Source
Sun 20 Sep 07:22        114 / 71  withings
Sun 27 Sep 07:13        120 / 74  omron
Sun 27 Sep 21:40        124 / 79  omron
...
Sun 18 Oct 07:12        116 / 73  withings

Average:               118 / 75 mmHg over 9 readings
Morning (before noon): 116 / 74 mmHg over 7 readings
Evening (from 6 pm):   123 / 78 mmHg over 2 readings
```

The recipe lists and averages your readings; what they mean is for your doctor.

## How it works

A cuff writes each reading as two samples with the same time: `blood_pressure_systolic` and
`blood_pressure_diastolic`, in mmHg. The recipe reads both from `GET /v1/users/{id}/samples`
in the merged view, so a Withings reading that also reached Apple Health comes once, and pairs
them by app and time. OMRON and Withings send them directly; other cuffs arrive through
[Apple Health](../connectors/apple-health.md) or [Health Connect](../connectors/health-connect.md).
Times are shown in your computer's time zone.

## Make it yours

- Add the pulse: OMRON and Withings also write a `heart_rate` sample at the time of the reading.
- Print it as a FHIR bundle for a clinic: `GET /v1/users/{id}/fhir/Observation` serves the same
  readings as FHIR R4 blood pressure observations.
- Mark the readings taken after a workout, from `GET /v1/users/{id}/workouts`.
