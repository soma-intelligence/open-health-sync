# Move from the Terra API to a self-hosted aggregator

Open Health Sync is an open source, self-hosted aggregator you can move to from the Terra API:
most Terra concepts have a direct counterpart. A Terra user becomes one Open Health Sync user with
your own `external_ref`, the widget session becomes a connect session, the webhook destination
becomes signed outgoing webhooks, and the data sits in your own Postgres. What changes most is
that you register your own developer app with each vendor.

Facts about Terra here come from the [Terra comparison](../compare/terra.md), checked on
2026-10-17 against Terra's own site. Read its "Pick Terra when" list first: it names the cases
where staying on Terra is the better fit, Garmin access among them.

## 1. Start an instance

Follow the [quickstart](../../README.md#quickstart): Docker Compose starts it, and
`ohs seed --demo` fills it with 90 days of synthetic data from six sources, so you can call every
route before you move a real account. For a server of your own, see
[self-hosting](../self-hosting.md) or [deploy on a platform](../deploy-platforms.md).

## 2. Register your own vendor apps

Terra uses its own developer apps for providers such as Fitbit, Oura, Polar and Withings. Here you
register yours, for every cloud provider you need, and set its keys on the instance with the
`OHS_<PROVIDER>_*` environment variables or from the [console](../console.md). Each
[connector guide](../connectors/README.md) starts with that step, including the redirect URL
`<OHS_PUBLIC_URL>/oauth/<provider>/callback`. `GET /v1/providers` lists what is enabled.

Apple Health and Health Connect data comes from your mobile app, through the
[Swift and Kotlin SDKs](../sdks/README.md) or the upload contract.

## 3. Map the concepts

The full table of Terra concepts and data types is in
[Moving from Terra](../compare/terra.md#moving-from-terra). The short version:

- Terra user with a `reference_id`: one Open Health Sync user per person, with your own
  `external_ref` and any number of connections.
- Widget session: a connect session (`POST /v1/users/{id}/connect-sessions`).
- Webhook, database and storage destinations: signed outgoing webhooks, plus the data already in
  your Postgres and CSV or Parquet exports.
- Activity, Sleep, Daily, Body, Nutrition, Menstruation: `workouts`, `sleep`, `daily`, `samples`,
  `nutrition`, `category-samples` and `menstrual-cycles`, in one canonical model with one unit per
  quantity ([data model](../data-model.md)).
- FHIR processing and the AI Interface: the [FHIR R4](../fhir.md) routes and the
  [MCP server](../mcp.md).

## 4. Create users and send them to the connect page

```sh
curl -s -X POST "$OHS_URL/v1/users" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' -d '{"external_ref":"customer-42"}'

curl -s -X POST "$OHS_URL/v1/users/$OHS_USER/connect-sessions" -H "Authorization: Bearer $OHS_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"redirect_uri":"https://app.example.org/connected","app_name":"Sleep Diary","providers":["withings","oura"]}'
# 201 {"url":"https://ohs.example.org/connect?session=...","expires_at":"..."}
```

Send the person to `url`. The page lists the providers with their status and starts each
provider's consent; afterwards they land on `redirect_uri?status=connected&provider=withings`.
A session lasts 30 minutes by default (`ttl_seconds`, 60 to 3600): create a fresh one each time
you show the page. Connections are made with your own vendor apps, so each person goes through
this page once for each source. To keep your own UI instead, use
`POST /v1/users/{id}/connections/{provider}` ([API guide](../api.md#connecting-a-provider)).

## 5. Replace the webhook destination

```sh
curl -s -X POST "$OHS_URL/v1/webhooks" -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://app.example.com/hooks/health","event_types":["data.written","connection.needs_reauth"]}'
```

Store `secret` from the answer: it is shown once. Events follow Standard Webhooks and carry no
health value: `data.written` says which user, provider and days changed, and your server reads
the records with the API. `connection.needs_reauth` says a person must connect a source again.
See [webhooks](../webhooks.md).

## 6. Read the history

When a person connects, the history arrives in the background. `GET
/v1/users/{id}/coverage` shows `pending_jobs` until it's done; then read any window:

```sh
curl -s "$OHS_URL/v1/users/$OHS_USER/sleep?start=2026-09-01&end=2026-10-01" \
  -H "Authorization: Bearer $OHS_KEY"
```

Every payload is also kept as received, so a mapper fix can re-process history.

## Related

- [Terra API alternative](../compare/terra.md)
- [Hosted connection page](../connect-page.md)
- [Combine data from several wearables](merge-multiple-wearables.md)
- [Delete a user's health data](delete-user-data.md)
- [Export WHOOP data to CSV or Parquet](whoop-export.md)
- [README](../../README.md)
