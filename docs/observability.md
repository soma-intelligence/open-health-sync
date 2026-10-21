# Observability

An instance tells you what it is doing in three ways: JSON logs on stderr, Prometheus metrics on
`/metrics`, and, when you turn them on, OpenTelemetry traces sent over OTLP. None of the three
carries a token, a key or a health value.

## Logs

One JSON object per line on stderr, at the level of `OHS_LOG_LEVEL` (`info` by default). A line
written while handling an API call carries the call's `request_id`, the value the client got
back in `X-Request-Id`. With tracing on, a line written inside a span also carries `trace_id`
and `span_id`, so you can go from a log line to its trace and back. Lines about pull jobs carry
`tenant_id`, `user_id`, `provider` and `job_id`; lines about ETL jobs and webhook deliveries
carry the job, delivery and endpoint ids.

## Metrics

`GET /metrics` serves the Prometheus text format unless `OHS_METRICS=false`. It is instance
wide and refuses every scrape until you open it with `OHS_METRICS_TOKEN` (a bearer token) or
`OHS_METRICS_LISTEN` (a private address); see [Metrics](self-hosting.md#metrics).

| Metric | Type | Meaning |
|---|---|---|
| `ohs_http_requests_total{route,code}` | counter | API requests by route pattern and status code |
| `ohs_ingest_in_flight` | gauge | Device uploads being processed now |
| `ohs_ingest_rejected_total` | counter | Device uploads refused with 503 because `OHS_INGEST_CONCURRENCY` was reached |
| `ohs_etl_pending_failed_records` | gauge | Records that failed and wait for a replay; alert when above 0 |
| `ohs_etl_parked_jobs` | gauge | ETL jobs that ran out of attempts |
| `ohs_etl_unprocessed_objects` | gauge | Raw objects past the grace period with no finished job |
| `ohs_etl_unregistered_files` | gauge | Raw files in the store with no database row |
| `ohs_etl_orphans_enqueued_total` | counter | Jobs created for raw objects that had none |
| `ohs_etl_drainer_replays_total` | counter | Raw objects queued again by the drainer |
| `ohs_etl_retention_rows_total`, `ohs_etl_retention_prefixes_total` | counter | Rows and raw day prefixes deleted by `OHS_RETENTION_DAYS` |
| `ohs_etl_run_failures_total` | counter | Background job runs that returned an error |
| `ohs_etl_last_reconcile_unix_seconds` | gauge | When the last reconcile pass finished |

The `ohs_etl_` metrics appear when the process serving `/metrics` also runs the `etl` role.

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
