# Project 5: Does training cost you sleep?

**Goal:** a script of your own that puts two lists side by side, your workouts and your nights,
and answers a question your apps answer separately: do you sleep less after a hard day?

**Time:** 30 minutes. **Level:** some Go. **Needs:** [project 2](read-your-sleep.md), with
workouts from a watch, Strava, Polar, Suunto, intervals.icu or the Health app, and sleep from any
source. They do not need to come from the same device.

## 1. Start from the connection block

```sh
mkdir training-vs-sleep && cp recipes/starter/main.go training-vs-sleep/
```

## 2. Join workouts and nights

In `training-vs-sleep/main.go`, replace everything from `// main is yours to replace.` to the
end with:

```go
type workout struct {
	StartDate string            `json:"start_date"`
	DurationS float64           `json:"duration_s"`
	Metadata  map[string]string `json:"metadata"`
}

type session struct {
	BedtimeEnd        string            `json:"bedtime_end"`
	TotalSleepSeconds float64           `json:"total_sleep_seconds"`
	Metadata          map[string]string `json:"metadata"`
}

// local puts a UTC time from the API on the clock of the place it was
// recorded, or on your computer's clock when the source does not say.
func local(stamp string, metadata map[string]string) time.Time {
	t, _ := time.Parse(time.RFC3339, stamp)
	if zone, err := time.LoadLocation(metadata["timezone"]); err == nil && metadata["timezone"] != "" {
		return t.In(zone)
	}
	return t.Local()
}

func main() {
	since := daysAgo(56)
	q := url.Values{"start": {since.Format(iso)}}

	// Minutes of training per day.
	training := map[string]float64{}
	for _, w := range records[workout]("workouts", q) {
		training[local(w.StartDate, w.Metadata).Format(iso)] += w.DurationS / 60
	}

	// Hours of sleep per night, named by the morning it ends on.
	sleep := map[string]float64{}
	for _, s := range records[session]("sleep", q) {
		if s.BedtimeEnd != "" {
			morning := local(s.BedtimeEnd, s.Metadata).Add(10 * time.Hour).Format(iso)
			sleep[morning] += s.TotalSleepSeconds / 3600
		}
	}

	// The night after each day: trained 45 minutes or more, or rested.
	groups := map[string][]float64{}
	for morning, hours := range sleep {
		t, _ := time.Parse(iso, morning)
		minutes := training[t.AddDate(0, 0, -1).Format(iso)]
		if minutes >= 45 {
			groups["after 45+ min of training"] = append(groups["after 45+ min of training"], hours)
		} else if minutes == 0 {
			groups["after a rest day"] = append(groups["after a rest day"], hours)
		}
	}

	fmt.Println("Sleep since " + since.Format("02 Jan"))
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		var sum float64
		for _, h := range groups[name] {
			sum += h
		}
		n := len(groups[name])
		fmt.Printf("  %-27s %.2f h   (%d nights)\n", name, sum/float64(n), n)
	}
}
```

```sh
go run ./training-vs-sleep
```

```text
Sleep since 18 Aug
  after 45+ min of training   6.76 h   (18 nights)
  after a rest day            7.16 h   (30 nights)
```

**How it works.**

1. `records[workout]("workouts", q)` and `records[session]("sleep", q)` read the two lists. A workout from Strava and a
   night from an Oura ring have the same shape here, so the script never asks where they came
   from.
2. Both are put on your local clock. The API sends UTC; `metadata.timezone` says where a record
   was made when the source knows it, otherwise the script uses your computer's zone.
3. Each night is matched with the day before it: the night that ends on Tuesday morning follows
   Monday's training.
4. Days with a short workout (under 45 minutes) are left out of both groups, so the comparison is
   between clearly hard days and clearly easy ones.

**Read it with care.** A difference over a handful of nights can be chance, and a hard day can
fall on the day you also went out. The count next to each average is there to remind you: the
more nights, the more it means.

## Make it yours

- **Late sessions.** Split the training days by the hour the workout started: before or after
  18:00. Evening sessions are often the ones that shorten a night.
- **HRV, not hours.** Read `records[...]("daily", q)` and its `hrv_avg_ms` (rings and bands) in
  place of sleep hours, matched the same way.
- **By sport.** Each workout has an `activity_type` (add it to `workout`) (`running`, `cycling`, `swimming`...): keep
  one and see if the answer changes.

## Share it

Your script and what you found, even "no difference at all", in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).
A finding that holds over months is worth a post of its own.

**Next:** [Project 6, Your own dashboard](your-dashboard.md).
