# Project 1: Your data in one place

**Goal:** your own Open Health Sync running on your computer, your iPhone, watch or ring
connected to it, and one command that tells you how many days of data came in.

**Time:** 20 minutes, most of it waiting for an export or a download. **Level:** first steps.

## 1. Start your instance

Follow the [quickstart](../../README.md#quickstart) up to *Start the stack*: clone the repository,
create `.env` with a fresh encryption key, and start it with Docker Compose. Every command below
runs from the repository root.

```sh
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
curl -s http://localhost:8080/healthz
```

```text
{"status":"ok"}
```

## 2. Make it yours

This creates a tenant called `personal`, a user called `me`, and a key, and writes them to a file
every project of this series reads:

```sh
docker compose --env-file .env -f deploy/docker-compose.yml exec -T ohs \
  /ohs admin personal setup > ~/.ohs.env
chmod 600 ~/.ohs.env
```

The key in that file reads everything on your instance: treat it like a password.

## 3. Bring your data in

Pick the source you have. You can add the others later, and they all land in the same lists.

**An iPhone (and an Apple Watch).** On the phone, open the Health app, tap your picture, then
*Export All Health Data*. Send the `export.zip` to your computer (AirDrop works). Install the
`ohs` command from the [latest release](https://github.com/soma-intelligence/open-health-sync/releases):
take the archive for your system (`darwin` is macOS), unpack it and put `ohs` on your `PATH`.
Then import the export through a token that can only upload:

```sh
set -a; . ~/.ohs.env; set +a
export OHS_TOKEN=$(curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/tokens" \
  -H "Authorization: Bearer $OHS_KEY" -d '{"scopes":["upload"]}' | jq -r .token)
ohs import apple-health export.zip
```

A few years of data take a minute or two. The command prints counts, never a health value.

**A ring, a band or another watch** (Withings, Oura, WHOOP, Fitbit, Polar, Suunto, Strava,
Ultrahuman, intervals.icu). Each provider asks for your own developer keys first, because nothing
is shared between installs: the page of each [connector](../connectors/README.md) says where to
get them, in about ten minutes. Then:

```sh
python3 recipes/connect.py withings    # or oura, whoop, fitbit...
```

Allow access on the page that opens. The first data arrives within seconds, and your history
follows in the background.

## 4. See what came in

```sh
python3 recipes/connect.py
```

```text
Source          Status        Synced through        Last error
apple_health    fresh         2026-10-13 01:37
```

And how many days each source covers:

```sh
set -a; . ~/.ohs.env; set +a
curl -s "$OHS_URL/v1/users/$OHS_USER/coverage" -H "Authorization: Bearer $OHS_KEY" \
  | jq -r '.days | group_by(.provider)[] | "\(.[0].provider): \(length) days, \(map(.records) | add) records"'
```

```text
apple_health: 71 days, 245 records
```

That is your checkpoint: one place, one shape, whatever measured it.

## Make it yours

- Connect a second source, then run the last command again: two lines, one per app.
- Add `?start=2026-01-01` to the coverage address to count this year only.
- Read one night as the API serves it, every field named the same whatever the device:
  `curl -s "$OHS_URL/v1/users/$OHS_USER/sleep?limit=1" -H "Authorization: Bearer $OHS_KEY" | jq`

## Share it

Post your coverage line (or just "it works on a Raspberry Pi 5") in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).

**Next:** [Project 2, Read your sleep](read-your-sleep.md).
