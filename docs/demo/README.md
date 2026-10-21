# README demos

Three recordings in the [README](../../README.md): the quickstart in a terminal, a user
connecting their sources in a browser, and a tour of the hosted developer portal.

## Platform tour

`platform-tour.mp4` (1080p, 60 fps, 75 seconds) walks through the developer portal of the hosted
beta on test data: sign up, create an organization and a project, connect a coding agent over MCP,
load the synthetic sample data, read it on the Data page and in the API explorer, then open a page
with Ctrl K. Keys and access tokens are masked on screen. The README shows a poster
(`docs/assets/readme/platform-tour-light.jpg`, `-dark.jpg`) that links to the file.

## Quickstart

`demo-dark.gif` and `demo-light.gif` are terminal recordings of the [quickstart](../../README.md#quickstart),
made with [VHS](https://github.com/charmbracelet/vhs). Nothing in them is mocked: the commands are
the quickstart's, run against the image `deploy/docker-compose.yml` names
(`ghcr.io/soma-intelligence/open-health-sync:0.1.0`), and the data is the synthetic set of `ohs seed --demo`
([docs/demo.md](../demo.md)), one invented person seen through six devices.

What it shows, in about a minute:

1. `docker compose ... up -d --wait` starts `ohs` and Postgres with TimescaleDB.
2. `ohs seed --demo` writes 90 days from Oura, WHOOP, Withings, Fitbit, Polar and Apple Health.
3. `GET /v1/users/{id}/daily`: one day of the merged view, each figure with the source it came from.
4. The same heart rate reading from `/samples`, `/fhir/Observation` (LOINC `8867-4`, UCUM `/min`)
   and `/omh/data-points` (`omh:heart-rate`).
5. `tools/list` on the read-only MCP server at `/mcp`.

### Files

| File | Role |
|---|---|
| `demo.tape` | The commands, the timing and the terminal size. |
| `dark.tape`, `light.tape` | The colors and the output file of each variant, then `Source demo.tape`. |
| `demo-dark.gif`, `demo-light.gif` | The recordings. The README picks one with `prefers-color-scheme`. |

### Render it again

You need [VHS](https://github.com/charmbracelet/vhs#installation) (it brings `ttyd` and `ffmpeg`),
Docker with Compose v2.24 or later, `curl`, `jq`, `openssl` and the
[JetBrains Mono](https://www.jetbrains.com/lp/mono/) font. Port 8080 must be free. From the
repository root:

```sh
vhs docs/demo/dark.tape
vhs docs/demo/light.tape
```

Each run takes about three minutes and works in a temporary folder with a copy of `deploy/` and
`.env.example`, under the compose project `ohs-demo`, so your `.env` and an install already
running under the default project are not touched. It removes its containers and volumes at the
end (`down -v` on `ohs-demo` only). Run the two tapes one after the other, not at the same time.

Two steps are cut from the recording: the wait for the health checks after `up`, and the export
of `OHS_URL`, `OHS_API_KEY` and `USER_ID` after the seed (a second key of the demo tenant stands in
for the one the seed prints, since that one is shown only once). On Linux as root, set
`VHS_NO_SANDBOX=1` so VHS can start its browser.

After a change to the API or to the seed, render both variants and check that every command
still fits in the frame.

## Provider flow

`flow-dark.gif` and `flow-light.gif` are browser recordings of the [admin console](../console.md)
and the [hosted connect page](../connect-page.md), made with Playwright against `ohs` built from
this repository (`scripts/image-without-buildx.sh`), with Postgres and TimescaleDB from
`deploy/docker-compose.yml` and `OHS_CONSOLE=true`.

What it shows, in about 50 seconds:

1. The console of a fresh instance, signed in with the key of its first tenant, which is the
   instance admin. The overview shows the pipeline with nothing in it yet. On **Providers**, Oura
   is not set up: its client ID and secret are pasted and saved, and Oura is on at once, with no
   restart. In this recording WHOOP, Withings, Polar and Strava are configured by environment
   variables.
2. An app called Sleep Diary creates its user `user-42` and a connection link for them
   (`POST /v1/users/{id}/connect-sessions` with its name and logo), and opens the link.
3. The user connects Oura, then Withings. Each **Connect** goes to the provider's consent screen
   with the scopes `ohs` asks for, **Allow** comes back to the callback, and the page shows the
   source as connected.
4. Back in the console: the overview with the connections, the sync runs and the raw data stored,
   the users, then the user's page with both connections active and healthy, the freshness of
   each source, the recent sync runs and the records that landed.

What is real and what stands in:

- Real: the console and the keys it saves, the connection link and page, the redirect with its
  `state` and scopes, the callback and the code exchange, the sealed tokens, the background
  backfill and the ETL.
- Standing in: the providers. The test servers of the connector tests (`connectors/oura/ourafake`,
  `connectors/withings/withingsfake`) answer the token and data calls, behind a local TLS proxy
  that `ohs` trusts through `HTTPS_PROXY` and `SSL_CERT_FILE`. Their fixture dates were moved into
  the last 30 days so the backfill picks them up. The consent screen is a plain page that says it
  is a test server.
- Invented: the user, Sleep Diary and its logo.

The frames are captured without loss from the browser (the DevTools screencast), so a still page
costs nothing in the GIF. The captions were added afterwards with `ffmpeg`, timed on the
recorder's own marks. The scripts that record it are not in the repository yet.
