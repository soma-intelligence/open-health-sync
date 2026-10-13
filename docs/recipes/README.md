# Build with your own health data

Your ring knows your sleep, your watch your heart rate, your phone your steps, and each keeps its
share in its own app. Open Health Sync brings them into one place you run yourself, in the same
units and the same shape. From there, a useful tool is a few lines of code.

The [`recipes/`](../../recipes) folder holds small tools that do one thing each. They are single
Python files with no dependency: copy one, run it, change it.

New to this? [Learn by building](../learn/README.md) walks you through them in seven short
projects, from a first sync to a server of your own.

| Recipe | What you get |
|---|---|
| [Sleep report](sleep-report.md) | your last nights in the terminal, whatever device measured them |
| [Heart trend](heart-trend.md) | resting heart rate and HRV week by week, and how this week compares |
| [Compare sources](compare-sources.md) | the same figure from your ring, your watch and your phone, side by side |
| [Weekly digest](weekly-digest.md) | a short Markdown note about last week, ready for your notes or a chat |
| [Dashboard](dashboard.md) | a one page HTML dashboard of steps, sleep, resting heart rate and HRV |
| [Export everything](export-all.md) | a copy of all your data as CSV for a spreadsheet, or Parquet for pandas |
| [Ask an AI](ai-agent.md) | a read-only token and the lines that let an AI assistant read your data |
| [Connect](connect.md) | connect Oura, WHOOP, Fitbit and the rest, and see how fresh each one is |

## For you, or for the people you build for

**For yourself.** One instance on a laptop, a home server or a Raspberry Pi holds your accounts
and nobody else's. The recipes run against it in a few seconds.

**For developers.** The recipes call the same REST API your product would, with the same tenant
key. Each one is a worked example of an endpoint: reading a list page by page, choosing between
two devices that report the same day, an export, a token for an agent. Start from the one closest
to your idea.

## Set up in five minutes

1. Start an instance with the [quickstart](../../README.md#quickstart) (Docker Compose, about
   two minutes).
2. Make it yours. This creates a `personal` tenant, a user named `me` and an API key, and writes
   them where every recipe looks for them:

   ```sh
   docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
     /ohs admin personal setup > ~/.ohs.env
   chmod 600 ~/.ohs.env
   ```

   The file holds three lines, `OHS_URL`, `OHS_KEY` and `OHS_USER`. Running the command again
   keeps the same user and prints a new key (earlier keys keep working until you revoke them).
   For a second person in the household, add `--person alex` and write to another file; point a
   recipe at it with `OHS_ENV_FILE=~/.ohs-alex.env`.

3. Bring your data in. Connect an account (set its keys on the server first, see
   [Connectors](../connectors/README.md)):

   ```sh
   python3 recipes/connect.py oura
   ```

   For an iPhone, export everything from the Health app (your picture, *Export All Health
   Data*) and import the zip with the `ohs` binary, through an upload token minted for `me`:

   ```sh
   set -a; . ~/.ohs.env; set +a
   export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
     -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
   ohs import apple-health export.zip
   ```

   Details in [Apple Health](../connectors/apple-health.md#importing-a-health-app-export).

4. Run a recipe:

   ```sh
   python3 recipes/sleep-report.py
   ```

The key in `~/.ohs.env` reads everything on the instance: keep the file private, and use the
[AI recipe](ai-agent.md) to hand out a narrower, read-only token instead.

## Write your own

Every recipe starts with the same block of about 60 lines: it reads `~/.ohs.env`, calls the
API and follows the pages of a list. Below it, a tool is usually one loop:

```python
for night in records("sleep", start="2026-09-01"):
    print(night["bedtime_start"], night["total_sleep_seconds"] / 3600, night["provider"])
```

`records(name, **filters)` reads any list of the [API](../api.md#reading-data): `samples`
(with `type`, such as `heart_rate` or `step_count`), `sleep`, `workouts`, `daily`, `nutrition`,
`category-samples`. Every record carries `provider`, the app it came from, so a tool can pick
one device or compare them. Quantity names and units are in the [data model](../data-model.md).

Made something useful? Open a pull request that adds it to `recipes/` with a page here.
