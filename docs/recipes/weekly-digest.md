# Weekly digest

A short note about last week, against the week before: steps, sleep, resting heart rate, HRV and
training. Markdown, so it pastes into notes, a journal or a chat.

```sh
go run ./recipes/weekly-digest                 # prints the note
go run ./recipes/weekly-digest -out week.md    # writes it to a file
```

```markdown
## Health, week of 05 October 2026

- **Steps**: 9,651 a day → +2% vs the week before
- **Sleep**: 7.4 h a night → +2% vs the week before
- **Resting heart rate**: 54.0 bpm → -1% vs the week before
- **HRV**: 51 ms ↓ -12% vs the week before
- **Training**: 89 min in 2 workouts ↓ -18% vs the week before
  1 × traditional strength training, 1 × running
```

The week is the last full one, Monday to Sunday. A change under 3% shows as →.

## Send it every Monday

```text
0 8 * * 1  cd ~/open-health-sync && go run ./recipes/weekly-digest -out ~/notes/health-$(date +\%F).md
```

To post it to a chat, pipe the note to the chat's incoming webhook, for example with `curl`.

## How it works

It reads two weeks of `GET /v1/users/{id}/daily`, `sleep` and `workouts`. When several apps
report the same figure, it keeps the one that reports it on the most days, so a week is never
compared with another device's week.
