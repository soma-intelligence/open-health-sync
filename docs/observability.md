# Observability

An instance tells you what it is doing in four ways: JSON logs on stderr, health checks on
`/healthz` and `/readyz`, Prometheus metrics on `/metrics`, and, when you turn them on,
OpenTelemetry traces sent over OTLP. None of them carries a token, a key or a health value.

## Logs

One JSON object per line on stderr, at the level of `OHS_LOG_LEVEL` (`info` by default). A line
written while handling an API call carries the call's `request_id`, the value the client got
back in `X-Request-Id`. With tracing on, a line written inside a span also carries `trace_id`
and `span_id`, so you can go from a log line to its trace and back. Lines about pull jobs carry
`tenant_id`, `user_id`, `provider` and `job_id`; lines about ETL jobs and webhook deliveries
carry the job, delivery and endpoint ids.

## Health

A process that runs the `api` role answers `GET /healthz` (the server answers) and
`GET /readyz` (the database answers and is at the schema version of the binary, the raw store
answers, the vault opens keys) on the API listener.

A process without the `api` role (`OHS_ROLES=collect`, `etl` or both) serves no API. It serves
the same two routes, and `/metrics`, on a listener of its own: the ops listener.

| | |
|---|---|
| Setting | `OHS_OPS_LISTEN`, a listen address |
| Default without the `api` role | `127.0.0.1:8081` |
| Default with the `api` role | off: the API listener already serves the three routes |
| Turn it off | `OHS_OPS_LISTEN=off` |
| Routes | `GET /healthz`, `GET /readyz`, `GET /metrics` (unless `OHS_METRICS=false`), nothing else |
| Credential | none, on any of the three |

The listener asks for no credential, so its address decides who reaches it: that is why the
default is the loopback interface. In a container or on Kubernetes, where the probe and the
scraper come from outside the process's network namespace, set it to `:8081` and do not publish
the port (the Helm chart does this for the `collect` and `etl` pods). Port 8081 is the one next
to the API's 8080; 9090, the usual example, is Prometheus' own. Two workers on one host cannot
share the default: the second logs a warning and runs without the listener, so give each its
own address. An address you set yourself that is taken stops the start.

On the ops listener `/readyz` is the API's check, with the same answers. `/healthz` goes further
than the API's: it is the liveness of the background loops of the process.

| Loop | Role | Comes round |
|---|---|---|
| `pull_scheduler` | `collect` | every minute: enqueues the pulls that are due |
| `pull_subscriber` | `collect` | every minute: subscribes and renews provider notifications |
| `pull_workers` | `collect` | every 1 to 2 s: claims pull jobs |
| `etl_standard`, `etl_high_freq` | `etl` | every 1 to 2 s: claims ETL jobs of the tier |
| `webhooks` | `etl` | every second: claims webhook deliveries, prunes old ones |
| `deletion` | `etl` | every 5 s: runs user and provider deletions |
| `consent_purge` | `etl` | every 10 s: purges the records of a revoked consent |

Each loop beats once per round, whether the round found work or failed. A loop is **stalled**
when it has been silent for three of its rounds, and for one minute at least (a loop that polls
every second is not hung because one claim waited for the database). `/healthz` then answers
503 with the same problem document as the API's errors, naming the loop:

```json
{"type":"about:blank","title":"Service Unavailable","status":503,
 "detail":"background loops stalled: etl_standard (no heartbeat for 1m12s, limit 1m0s)"}
```

A long job is not a hung loop. The beat comes from the loop, not from the end of a job, and a
worker whose slots are all taken by long jobs waits for a slot instead of polling: while a job
it started is inside its time limit (15 minutes for an ETL job or a pull: the 9 minute job
timeout, then a settle retried while the database is away; 2 minutes for a webhook attempt; an
hour for a deletion or a consent purge), the loop is busy, not stalled. A job past its limit
excuses nothing: the detail then reads `a job ran past its limit`. A database that is away
fails `/readyz`, not `/healthz`: the loops log the error and keep coming round, and restarting
the process would not bring the database back.

The upkeep jobs of the `etl` role (reconciler, orphan scan, drainer, retention and the pruners)
do not beat yet: watch `ohs_etl_last_reconcile_unix_seconds` and `ohs_etl_run_failures_total`
for them.

## Metrics

`GET /metrics` serves the Prometheus text format unless `OHS_METRICS=false`. On the API
listener it is instance wide and refuses every scrape until you open it with
`OHS_METRICS_TOKEN` (a bearer token) or `OHS_METRICS_LISTEN` (a private address); see
[Metrics](self-hosting.md#metrics).

| Metric | Type | Meaning |
|---|---|---|
| `ohs_http_requests_total{route,code}` | counter | API requests by route pattern and status code |
| `ohs_ingest_in_flight` | gauge | Device uploads being processed now |
| `ohs_ingest_rejected_total` | counter | Device uploads refused with 503 because `OHS_INGEST_CONCURRENCY` or, for a tenant past its share (`OHS_INGEST_TENANT_CONCURRENCY`), the reserve kept for other tenants was reached |
| `ohs_rate_limited_total{route_group}` | counter | Requests refused with 429 by a [rate limiter](self-hosting.md#rate-limits). `route_group` is `inbound`, `oauth_callback`, `setup` or `share` for the routes without a credential, `api` for a credential over `OHS_RATE_LIMIT_API`, `auth_failure` for an address past `OHS_RATE_LIMIT_AUTH_FAILURES` |
| `ohs_auth_failures_total` | counter | Well formed API keys and user tokens refused with 401. A steady rise is a client with a stale key, or someone guessing |
| `ohs_audit_write_failures_total` | counter | [Audit events](audit-log.md) that could not be written; alert when it rises |
| `ohs_audit_dropped_total` | counter | Audit events of reads and uploads lost after a batch failed three times (a full queue falls back to a synchronous write and loses nothing); alert when it rises |
| `ohs_etl_pending_failed_records` | gauge | Records that failed and wait for a replay; alert when above 0 |
| `ohs_etl_parked_jobs` | gauge | ETL jobs that ran out of attempts |
| `ohs_etl_unprocessed_objects` | gauge | Raw objects past the grace period with no finished job |
| `ohs_etl_unregistered_files` | gauge | Raw files in the store with no database row |
| `ohs_etl_orphans_enqueued_total` | counter | Jobs created for raw objects that had none |
| `ohs_etl_drainer_replays_total` | counter | Raw objects queued again by the drainer |
| `ohs_etl_retention_rows_total`, `ohs_etl_retention_prefixes_total` | counter | Rows and raw day prefixes deleted by `OHS_RETENTION_DAYS` |
| `ohs_etl_retention_chunks_total` | counter | Whole hypertable chunks dropped by `OHS_RETENTION_DAYS`, in place of deleting their rows one by one |
| `ohs_etl_tracking_rows_pruned_total` | counter | Sync log rows and finished processing jobs deleted by age (`OHS_TRACKING_RETENTION_DAYS`) |
| `ohs_etl_run_failures_total` | counter | Background job runs that returned an error |
| `ohs_etl_last_reconcile_unix_seconds` | gauge | When the last reconcile pass finished |

The `ohs_etl_` metrics above appear when the process serving `/metrics` also runs the `etl`
role.

### Worker metrics

The ops listener (`OHS_OPS_LISTEN`, see [Health](#health)) serves `/metrics` for the process it
runs in: the worker metrics below, the `ohs_etl_` metrics above when it runs the `etl` role, and
the vault's. It does not serve the API's `ohs_http_`, `ohs_ingest_` and `ohs_sync_` metrics. To
get the worker metrics of a process that runs every role, set `OHS_OPS_LISTEN` on it and scrape
both listeners.

| Metric | Type | Meaning |
|---|---|---|
| `ohs_queue_depth{queue}` | gauge | Jobs due and not claimed yet. `queue` is `etl_standard`, `etl_high_freq` or `webhook` (`etl` role), `pull` (`collect` role). Jobs waiting for a retry or a provider's rate limit are not due and not counted |
| `ohs_queue_oldest_job_age_seconds{queue}` | gauge | How long the oldest due job has waited; 0 with none. Alert when it keeps rising: the workers do not keep up, or do not run |
| `ohs_job_duration_seconds{queue,kind,outcome}` | histogram | How long jobs ran, from claim to settle. `queue="etl"`: `kind` is the tier (`standard`, `high_freq`), `outcome` is `completed`, `failed`, `claim_lost` or `settle_error`. `queue="pull"`: `kind` is what started the pull (`scheduled`, `notification`, `backfill`, `manual`), `outcome` is the `ohs.job.outcome` of the [span](#spans) |
| `ohs_pull_duration_seconds{provider}` | histogram | How long pulls of a provider took, every outcome together |
| `ohs_webhook_delivery_duration_seconds{outcome}` | histogram | How long each webhook delivery attempt took; `outcome` is `delivered` or `failed` |
| `ohs_etl_rows_written_total{table}` | counter | Rows the ETL wrote, by table (`quantity_samples`, `sleep_sessions`, ...). Not fed yet: the series appear with the release that wires the ETL writer to it |
| `ohs_loop_last_tick_unix_seconds{loop}` | gauge | When each background loop of the process last beat |
| `ohs_loop_stalled{loop}` | gauge | 1 while the loop is stalled and `/healthz` answers 503 |

The histograms share their buckets: 10, 50, 100, 250 and 500 ms, then 1, 2.5, 5, 10, 30, 60,
120, 300 and 600 s. A histogram or counter has no series until its first observation. The queue
gauges are counted every 15 seconds from the index of pending jobs, not on each scrape: a
scrape never waits for the database, and a queue of millions of jobs is not counted more often
than that. They are instance wide, so every replica of a role reports the same values: take
`max`, not `sum`. Label values are fixed sets (queues, tiers, outcomes, provider slugs, tables,
loops): no tenant, user or job id.

## Traces

Tracing is off by default. It turns on when `OTEL_EXPORTER_OTLP_ENDPOINT` (or
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) is set, unless `OTEL_SDK_DISABLED=true`. Spans then go
over OTLP to the collector or tracing backend at that address. Off, no exporter runs, no
`traceparent` header is read, and the instrumentation is the OpenTelemetry no-op: it costs next
to nothing.

These are the standard OpenTelemetry variables, read as the OpenTelemetry SDK documents them:

| Variable | Default | Meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | none (tracing off) | Base URL of the OTLP receiver, for example `http://collector:4318`. Traces go to `<endpoint>/v1/traces` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | none | Full URL for traces only; wins over the one above |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | `http/protobuf` (port 4318) or `grpc` (port 4317). `http/json` is not supported and stops startup with an error. `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL` wins over it |
| `OTEL_EXPORTER_OTLP_HEADERS` | none | Headers sent with every export, for example `authorization=Bearer ...` for a hosted backend |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` | `true` sends without TLS to an endpoint given without a scheme. An `http://` endpoint already does |
| `OTEL_EXPORTER_OTLP_CERTIFICATE`, `OTEL_EXPORTER_OTLP_TIMEOUT`, `OTEL_EXPORTER_OTLP_COMPRESSION` | SDK defaults | CA file, export timeout (10 s) and `gzip` compression |
| `OTEL_SERVICE_NAME` | `ohs` | `service.name` of every span. Give each process its own when you split roles, for example `ohs-api` and `ohs-etl` |
| `OTEL_RESOURCE_ATTRIBUTES` | none | More resource attributes, for example `deployment.environment.name=prod` |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | `parentbased_always_on` | Which traces are kept. `parentbased_traceidratio` with `0.1` keeps one in ten new traces and follows the caller's decision otherwise |
| `OTEL_BSP_*` | SDK defaults | Batch size and delay of the span exporter |
| `OTEL_SDK_DISABLED` | `false` | `true` keeps tracing off whatever else is set |

`service.version` is the release of the binary (what `ohs version` prints). Spans are sent in
batches every few seconds; on SIGTERM the process exports what is left, waiting at most 5
seconds for the collector, after the roles have stopped. A collector that is down does not stop
the instance: export errors are logged as warnings and the spans are dropped.

With docker compose, put the variables in `.env`: the `ohs` service reads every variable of that
file.

### Try it with Jaeger

Save this overlay as `deploy/docker-compose.tracing.yml` (any name works):

```yaml
services:
  jaeger:
    # All in one: an OTLP receiver (4317 gRPC, 4318 HTTP), in-memory storage and the UI.
    image: jaegertracing/jaeger:2.21.0
    restart: unless-stopped
    ports:
      - "127.0.0.1:16686:16686" # UI

  ohs:
    environment:
      OTEL_EXPORTER_OTLP_ENDPOINT: http://jaeger:4318
      OTEL_SERVICE_NAME: ohs
```

```sh
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.tracing.yml up -d
```

Open `http://localhost:16686`, pick the `ohs` service and make an API call. Jaeger keeps traces
in memory only: use it to look around, not in production.

### Through an OpenTelemetry Collector

In production, send spans to an [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/)
and let it batch, sample, filter and forward them to your backend:

```yaml
services:
  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.162.0
    restart: unless-stopped
    command: ["--config=/etc/otelcol/config.yaml"]
    volumes:
      - ./otel-collector.yaml:/etc/otelcol/config.yaml:ro

  ohs:
    environment:
      OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector:4318
```

with `deploy/otel-collector.yaml`:

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
processors:
  batch: {}
exporters:
  otlphttp:
    endpoint: https://traces.example.org # your backend
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlphttp]
```

### Spans

| Span | Kind | When | Attributes |
|---|---|---|---|
| `GET /v1/users/{id}/samples` (method and route pattern) | server | Every API request | `http.request.method`, `http.route`, `http.response.status_code`, `ohs.request_id` |
| `pull job` | consumer | Each pull of a provider (collect role) | `ohs.job.id`, `ohs.job.kind` (`scheduled` or `notification`), `ohs.provider`, `ohs.tenant_id`, `ohs.user_id`, `ohs.job.attempts`, `ohs.job.outcome` |
| `etl job` | consumer | Each raw object processed (etl role) | `ohs.job.id`, `ohs.etl.tier`, `ohs.provider`, `ohs.job.attempts`, `ohs.job.outcome` |
| `webhook delivery` | client | Each delivery attempt to a webhook endpoint | `ohs.webhook.delivery_id`, `ohs.webhook.endpoint_id`, `ohs.webhook.event_type`, `ohs.webhook.attempt`, `http.response.status_code` |
| `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `WITH`, `BEGIN`, `COMMIT`, `COPY`, `BATCH`, ... | client | Each database query made inside one of the spans above | `db.system.name`, `db.operation.name`, `db.collection.name` (COPY only), `db.operation.batch.size` (BATCH only), `db.response.status_code` (the SQLSTATE of a failure) |

Notes:

- **Incoming trace context.** An API request carrying a W3C `traceparent` header joins the
  caller's trace, so a client or a proxy that traces sees the instance's work in its own trace.
  Nothing is added to the response.
- **Route, not path.** A server span is named by the matched route pattern
  (`/v1/users/{id}/samples`), never by the path the client sent, so user ids and external
  references do not appear. A path no route matches is named by the catch-all route `/`. A
  method outside the standard ones is recorded as `_OTHER` (span name `HTTP`).
- **Outcomes.** `ohs.job.outcome` is `completed`, `failed`, `deferred` (the provider's rate
  limit), `needs_reauth`, `inactive` (the connection or user is gone, or the provider is turned off for the tenant), `claim_lost` (another
  worker owns the job now) or `settle_error`. A failed job, a failed delivery attempt, a 5xx API answer and
  a failed query set the span status to error, without a description.
- **Database spans** exist only under a request, job or delivery span. The polling of the job
  queues, which runs every second or so on every instance, starts no trace.
- **No outgoing propagation.** Webhook receivers and providers are not sent a `traceparent`.

### What never leaves the instance

Spans follow the same rule as the logs: no secrets and no health values. Concretely, a span
never records:

- a request path as sent, a query string, a request or response header, a request or response
  body;
- an SQL statement text or argument, or a database error message (only its SQLSTATE code);
- an error message of a pull, an ETL job or a delivery (they can quote a provider's answer);
- a webhook endpoint URL, payload or signature;
- a token, a key, a secret or a measurement.

The ids a span may carry are the ones the logs already carry: tenant and user UUIDs on pull
jobs, job, delivery and endpoint ids, the request id and the provider slug. They are
pseudonymous: they identify a row of this instance, not a person, but a backend that keeps
traces does learn which user was pulled when. If that matters for your deployment, drop them in
the collector (an `attributes` processor with `action: delete` on `ohs.tenant_id` and
`ohs.user_id`), or keep traces inside your own network.
