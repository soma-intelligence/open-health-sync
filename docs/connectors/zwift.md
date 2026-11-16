# Zwift (research notes)

There is **no Zwift connector**, because Zwift offers no public API to read a rider's data. This
page records what was checked on 2026-11-12 and how Zwift activities can still reach Open Health
Sync.

## Why there is no connector

- Zwift documents no public API for reading activities.
- The [Zwift Training API](https://news.zwift.com/en-WW/236781-zwift-launches-new-training-api-for-coaching-platforms)
  goes the other way: approved coaching platforms push structured workouts into Zwift. It reads
  nothing out.
- Community libraries call the private API of the Zwift game and companion app with the rider's
  Zwift password. Open Health Sync does not build on undocumented APIs when the activity files
  below already carry the data.

## How Zwift data reaches Open Health Sync

- **Activity files.** Zwift saves a FIT file for every activity, on the computer that ran it and
  in the rider's activity history on zwift.com
  ([Zwift support](https://support.zwift.com/en_us/your-activity-history-and-fit-files-SJbHywdNH)).
  The [activity file import](activity-files.md) stores them with power, cadence and heart rate.
- **Strava and Garmin.** Zwift uploads activities to linked Strava and Garmin Connect accounts;
  they arrive through the [Strava](strava.md) and [Garmin](garmin.md) connectors.
- **intervals.icu.** Zwift activities synced to intervals.icu come through the
  [intervals.icu connector](intervals-icu.md), with Zwift kept as their source.

## Revisit

If Zwift publishes a documented API to read activities, open an issue with the
[connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
