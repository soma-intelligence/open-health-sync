# COROS (research notes)

There is **no COROS connector**. This page records what COROS offers developers, as checked on
2026-11-12 in the COROS help center and the COROS MCP documentation, why no connector is built
on it yet, and how COROS data reaches Open Health Sync today.

## How COROS data reaches Open Health Sync today

- **Activity files.** In the COROS Training Hub, *Activity List*, *Export Data* saves FIT files
  that the [activity file import](activity-files.md#1-get-the-files) stores, with no API
  access needed.
- **intervals.icu.** Activities that COROS syncs to intervals.icu come through the
  [intervals.icu connector](intervals-icu.md), which keeps COROS as their upstream source.
- **Strava.** Activities COROS uploads to Strava come through the [Strava connector](strava.md).
- **Apple Health and Health Connect.** What the COROS app writes there (check its settings)
  arrives through the [Apple Health](apple-health.md) and [Health Connect](health-connect.md)
  connectors.

Daily health data (resting heart rate, HRV, sleep) has no path unless the COROS app writes it to
Apple Health or Health Connect.

## Why there is no connector

Open Health Sync builds a connector only on an official API whose request and response formats
are documented, so the connector and its test fixtures follow the vendor's own examples. COROS
offers two paths, and neither fits yet:

- **The Partner API** has formats that are not public: the help center lists its endpoints by
  name only. It is also for established platforms that COROS approves. Without the formats there
  is nothing to build or test against.
- **The self-service COROS MCP server** is official and documented, but it is made for AI
  assistants: most of its tools answer human-readable text (a sleep overview reads like a short
  report), not data records, and FIT downloads are capped at 50 files per user per 24 hours. A
  connector parsing that text would break whenever COROS rewords it.

## The three ways in

COROS lists three developer paths under *API and MCP Connections*
([Connect Your Data](https://support.coros.com/hc/en-us/articles/53181260265492-Connect-Your-Data),
updated 2026-10-14):

| Path | For | Approval | What it gives |
|---|---|---|---|
| Personal MCP connector | one person linking their own account to an assistant | none | reads training data and health metrics, writes workout plans |
| Build on COROS MCP | developers whose users connect their own COROS account | none, self-service | per-user OAuth 2.0 (dynamic client registration, PKCE) against `https://mcp.coros.com/mcp`: activities, sleep, heart rate, HRV, stress, fitness assessments, training plan writes. No webhooks, no route import or export, no two-way activity sync |
| Partner API | established platforms | application and the API Terms of Use | multi-user OAuth 2.0 credentials, webhooks, two-way activity sync, daily health data, FIT downloads, routes, training plans |

Sources: [Build on COROS MCP](https://support.coros.com/hc/en-us/articles/53181619102996-Build-on-COROS-MCP)
(updated 2026-10-31) and the tool reference at
[github.com/coroslab/COROS-MCP](https://github.com/coroslab/COROS-MCP). Per its FAQ, access
tokens last 30 days and refresh tokens 180 days, rotated on every refresh.

## Partner API

From [Partner API Access](https://support.coros.com/hc/en-us/articles/53181766856724-Partner-API-Access)
(updated 2026-11-07):

- **Who can apply:** an established platform with a demonstrated user base, run by a registered
  company with an authorized technical representative, meeting standard security and privacy
  requirements, that accepts the API Terms of Use (not published).
- **How:** the representative sends company details, technical contacts and the OAuth 2.0
  redirect URIs through the application form and to api@coros.com
  ([Submit an API Application](https://support.coros.com/hc/en-us/articles/17085887816340-Submit-an-API-Application),
  updated 2026-11-10). After identity and security checks, COROS issues a client id and secret.
  The pages state no fee and no review timeline.
- **Auth:** OAuth 2.0 with authorize, access token, refresh, deauthorize and bind state.
- **Reads:** `getUserInfo`; `getWorkoutRecords` (at most 30 days per call, 3 months back);
  `getDailyData`; `getWorkoutDetailFit`, the workout as a FIT file.
- **Push:** a workout summary is pushed to the partner's endpoint within about 5 minutes, with
  retries, authenticated by the client id and secret in a header. No other pushed type is
  listed.
- **Writes:** training plans, routes (GPX or KML up to 50 MB) and activity uploads.
- **Rate limit:** 1,000 calls per minute.

## What a connector would map

| COROS data | Open Health Sync |
|---|---|
| Workout records and FIT details | workouts, with the FIT file read by the same decoder as the [activity file import](activity-files.md) |
| Daily data | daily figures and samples (to map once the response format is known) |
| Workout summary push | a notification that triggers a pull of that workout |

## Revisit

- If COROS publishes the Partner API request and response formats, or you have Partner API
  access and its documentation, open an issue with the
  [connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
- If the MCP tools start answering structured data (only the lap data tool does today), the
  self-service path becomes worth a connector.
- The help center articles change without notice: check the dates above against them.
