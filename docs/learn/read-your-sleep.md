# Project 2: Read your sleep

**Goal:** a report of your last two weeks of sleep in the terminal, then a script of your own,
about twenty lines, that adds deep and REM sleep to it.

**Time:** 15 minutes. **Level:** first lines of Go. **Needs:** [project 1](first-sync.md),
with sleep from a watch, a ring or the Health app.

## 1. Run the ready-made report

```sh
go run ./recipes/sleep-report
```

```text
Sleep, last 14 nights (apple_health)

Sun 04 Oct   8h00  ████████████████████████████████
Mon 05 Oct   6h38  ███████████████████████████
Tue 06 Oct   7h55  ████████████████████████████████
Wed 07 Oct   6h15  █████████████████████████
...
Average 7h07, shortest 6h15 (Wed 07 Oct), longest 8h00 (Sun 04 Oct)
Nights under 7 hours: 6 of 14
```

It works the same whether an Oura ring, a WHOOP band or your iPhone measured the nights, because
every source lands in the same `sleep` list with the same fields.

## 2. Start a program of your own

Every recipe begins with the same block: it reads `~/.ohs.env`, calls the API and follows the
pages of a list. `recipes/starter` is that block with a small `main` under it. Copy it into a
folder of your own:

```sh
mkdir my-sleep && cp recipes/starter/main.go my-sleep/
go run ./my-sleep
```

The block gives you one function that matters: `records[T]("sleep", url.Values{"start": {...}})`
returns every sleep session from that day on, whatever its source, as a list of `T`: a struct
with the fields you want.

## 3. Add deep and REM sleep

In `my-sleep/main.go`, replace everything from `// main is yours to replace.` to the end with:

```go
type session struct {
	BedtimeEnd        string  `json:"bedtime_end"`
	TotalSleepSeconds float64 `json:"total_sleep_seconds"`
	DeepSleepSeconds  float64 `json:"deep_sleep_seconds"`
	RemSleepSeconds   float64 `json:"rem_sleep_seconds"`
}

type night struct{ total, deep, rem float64 }

func main() {
	nights := map[string]*night{}
	for _, s := range records[session]("sleep", url.Values{"start": {daysAgo(14).Format(iso)}}) {
		end, err := time.Parse(time.RFC3339, s.BedtimeEnd)
		if err != nil {
			continue // a session still open has no end yet
		}
		// The morning it ends on, on your computer's clock: before 14:00, that morning.
		day := end.Local().Add(10 * time.Hour).Format(iso)
		if nights[day] == nil {
			nights[day] = &night{}
		}
		nights[day].total += s.TotalSleepSeconds
		nights[day].deep += s.DeepSleepSeconds
		nights[day].rem += s.RemSleepSeconds
	}
	for _, day := range slices.Sorted(maps.Keys(nights)) {
		n := nights[day]
		t, _ := time.Parse(iso, day)
		fmt.Printf("%s  %4.1f h   deep %3.0f min   REM %3.0f min\n", t.Format("Mon 02 Jan"), n.total/3600, n.deep/60, n.rem/60)
	}
}
```

```sh
go run ./my-sleep
```

```text
Sun 04 Oct   8.0 h   deep 100 min   REM 125 min
Mon 05 Oct   6.6 h   deep  80 min   REM 100 min
Tue 06 Oct   7.9 h   deep 100 min   REM 125 min
Wed 07 Oct   6.2 h   deep  80 min   REM  95 min
```

**What just happened.** A night can come in several pieces: a wake up in the middle, or one
piece per sleep stage from some apps. The script adds up every piece that ends on the same
morning. The API sends times in UTC, and `Local()` puts them back on your clock.

## Make it yours

- **One device only.** With a ring and a watch, both nights are added together. Add a
  `Provider` field to `session` (`json:"provider"`), then `if s.Provider != "oura" { continue }`
  at the top of the loop (the names are in the first
  column of `go run ./recipes/connect`).
- **Bedtime.** Add `bedtime_start` to `session`, keep the earliest of each night and print it. Late nights often
  explain short ones.
- **A goal.** Print a star next to the nights over your target, and count them at the end.

## Share it

Post your script, or a screenshot of the output, in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).

**Next:** [Project 3, Your HRV trend](hrv-trend.md).
