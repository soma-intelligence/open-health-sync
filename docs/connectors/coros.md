# COROS (research notes)

There is **no COROS connector** yet. This page records what COROS offers developers, as
checked on 2026-10-20 in the COROS help center, and how COROS data reaches Open Health Sync
today.

## How COROS data reaches Open Health Sync today

- **Activity files.** In the COROS Training Hub, *Activity List*, *Export Data* saves FIT files
  that the [activity file import](activity-files.md#1-get-the-files) stores, with no API
  access needed.
- **intervals.icu.** Activities that COROS syncs to intervals.icu come through the
  [intervals.icu connector](intervals-icu.md), which keeps COROS as their upstream source.

Daily health data (resting heart rate, HRV, sleep) has no path yet.

## The three ways in

COROS lists three developer paths under *API and MCP Connections*
([Connect Your Data](https://support.coros.com/hc/en-us/articles/53181260265492-Connect-Your-Data),
updated 2026-09-02):

| Path | For | Approval | What it gives |
|---|---|---|---|
| Personal MCP connector | one person linking their own account to an assistant | none | reads training data and health metrics, writes workout plans |
| Build on COROS MCP | developers whose users connect their own COROS account | none, self-service | per-user OAuth 2.0 against `https://mcp.coros.com/mcp`, 22 data fields (activities, health metrics, fitness assessments), training plan writes. No webhooks, no route import or export, no two-way activity sync |
| Partner API | established platforms | application and the API Terms of Use | multi-user OAuth 2.0 credentials, webhooks, two-way activity sync, daily health data, FIT downloads, routes, training plans |

## Partner API

From [Partner API Access](https://support.coros.com/hc/en-us/articles/53181766856724-Partner-API-Access)
(updated 2026-09-03):

- **Who can apply:** an established platform with a demonstrated user base, run by a registered
  company with an authorized technical representative, meeting standard security and privacy
  requirements, that accepts the API Terms of Use.
- **How:** the representative sends company details, technical contacts and the OAuth 2.0
  redirect URIs through the application form and to api@coros.com
  ([Submit an API Application](https://support.coros.com/hc/en-us/articles/17085887816340-Submit-an-API-Application),
  updated 2026-09-04). After identity and security checks, COROS issues a client id and secret.
  The pages state no fee and no review timeline.
- **Auth:** OAuth 2.0 with authorize, access token, refresh, deauthorize and bind state.
- **Reads:** `getUserInfo`; `getWorkoutRecords` (at most 30 days per call, 3 months back);
  `getDailyData`; `getWorkoutDetailFit`, the workout as a FIT file.
- **Push:** a workout summary is pushed to the partner's endpoint within about 5 minutes, with
  retries, authenticated by the client id and secret in a header. No other pushed type is
  listed.
- **Writes:** training plans (`tp/list/push`, `tp/workout/deleteById`), routes (`route/push`,
  GPX or KML up to 50 MB) and activity uploads (`file/synchronous`, `file/upload`,
  `file/upload/get`).
- **Rate limit:** 1,000 calls per minute.

The help center lists the endpoints by name only: request and response formats are not
public.

## What a connector would map

| COROS data | Open Health Sync |
|---|---|
| Workout records and FIT details | workouts, with the FIT file read by the same decoder as the [activity file import](activity-files.md) |
| Daily data | daily figures and samples (to map once the response format is known) |
| Workout summary push | a notification that triggers a pull of that workout |

## Revisit

- The self-service MCP path needs no approval and covers activities and health metrics, but it
  has no webhooks (the server would poll) and its tools are built for an assistant, not as a
  data API. Worth a test before choosing it over the Partner API.
- If you have Partner API access, or COROS publishes the request and response formats, open an
  issue with the [connector request form](https://github.com/soma-intelligence/open-health-sync/issues/new/choose).
- The help center articles change without notice: check the dates above against them.
