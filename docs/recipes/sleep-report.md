# Sleep report

How did you sleep these last two weeks? One command answers, whether a ring, a watch, a band or
your phone measured the nights.

```sh
python3 recipes/sleep-report.py            # the last 14 nights
python3 recipes/sleep-report.py --days 60
```

```text
Sleep, last 7 nights (oura)

Mon 05 Oct   7h28  ██████████████████████████████
Tue 06 Oct   8h11  █████████████████████████████████
Wed 07 Oct   7h00  ████████████████████████████
Thu 08 Oct   6h45  ███████████████████████████
Fri 09 Oct   6h48  ███████████████████████████
Sat 10 Oct   8h41  ███████████████████████████████████
Sun 11 Oct   6h50  ███████████████████████████

Average 7h23, shortest 6h45 (Thu 08 Oct), longest 8h41 (Sat 10 Oct)
Nights under 7 hours: 3 of 7
```

## How it works

It reads `GET /v1/users/{id}/sleep` from the start of the window. A night is named by the
morning it ends on: every session that ends between 14:00 the day before and 14:00 counts for
that date, in the time zone the device recorded, so a night split in two by a wake up is added
up. A session with no sleep total (some Apple Health sources only record time in bed) counts its
time in bed, less the time awake when the app gives it. When two apps measured your nights, the report uses the one that measured the most of them
and says which; [Compare sources](compare-sources.md) shows both.

## Make it yours

- Add deep and REM sleep: each session also has `deep_sleep_seconds` and `rem_sleep_seconds`.
- Print the bedtime: late nights often explain short ones.
- Run it from cron every morning and send yourself the result.
