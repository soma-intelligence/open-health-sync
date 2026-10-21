# Learn by building

Seven small projects that take you from "my data is stuck in five apps" to tools of your own: a
sleep report, an HRV trend, an AI assistant that reads your training, a dashboard, a message on
your phone the morning your night syncs. Each one takes 10 to 30 minutes, works with the device
you already wear, and ends with something you can show.

No health data background is needed. If you can copy a command into a terminal, you can do the
first four. The last three ask for a little Go, and every line is given.

| # | Project | Time | You end up with |
|---|---|---|---|
| 1 | [Your data in one place](first-sync.md) | 20 min | your own instance, your watch or ring connected, a count of what came in |
| 2 | [Read your sleep](read-your-sleep.md) | 15 min | a sleep report, then the same report with deep and REM sleep, written by you |
| 3 | [Your HRV trend](hrv-trend.md) | 15 min | resting heart rate and HRV week by week, and a flag for the weeks that stand out |
| 4 | [Ask an AI about your training](ask-an-ai.md) | 10 min | Claude or another assistant answering questions from your real numbers |
| 5 | [Does training cost you sleep?](training-vs-sleep.md) | 30 min | a script of your own that joins two lists and answers a real question |
| 6 | [Your own dashboard](your-dashboard.md) | 20 min | a one page dashboard, with a chart you added |
| 7 | [A message when your night syncs](sleep-alert.md) | 30 min | a small server that hears about new data and writes you a morning line |

Then keep going with the [prompt library](prompts.md): ready-made questions for an AI assistant
connected to your data, for runners, cyclists, triathletes and anyone training for an event.

## How each project works

Every page has the same shape:

- **Goal**: what you will have at the end, in one sentence.
- **Steps**: commands and code to copy, in order, with what you should see.
- **Make it yours**: two or three changes to try once it works. This is where you learn the most.
- **Share it**: post a screenshot or your script in
  [Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).
  Leave your numbers out if you prefer: the code and the idea are what help the next person.

The projects build on each other, so do project 1 first. After that, take them in any order.

## What you need

- A computer with Docker, [Go](https://go.dev/dl/) 1.23 or later, `curl` and `jq`. A Raspberry
  Pi 5 works too.
- Data from at least one source: an iPhone with the Health app (an Apple Watch adds heart rate
  variability), or an account with Withings, Oura, WHOOP, Fitbit, Polar, Suunto, Strava,
  Ultrahuman or intervals.icu.

Everything runs on your machine. Your data does not leave it unless you send it somewhere
yourself, and project 4 explains what an AI assistant sees before you connect one.

## Stuck?

Ask in [Q&A](https://github.com/soma-intelligence/open-health-sync/discussions/categories/q-a)
with the project number, the command you ran and what it printed. Someone has probably hit the
same thing.
