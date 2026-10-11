# Benchmark: Open Health Sync and Open Wearables

How fast each one takes in the same Apple Health data, how much memory and CPU it uses doing
it, and how fast it reads a day back. Measured on 2026-10-11 with Open Health Sync at
`c8fdf12` (v0.0.2 plus one commit) and Open Wearables 0.9.0 at `d07f0d5`. Everything needed
to run it again is in [`bench/openwearables/`](../../bench/openwearables), and the raw results
of the runs below are in
[`bench/openwearables/results/2026-10-11/`](../../bench/openwearables/results/2026-10-11).

## Summary

On this machine and this data, Open Health Sync stored the same Apple Health records
**2 to 3 times faster** end to end, answered uploads **5 to 23 times faster** (median
latency), ran in **22 times less memory idle and 5 to 13 times less at peak**, and used
**40 to 50 percent of the application CPU** per record. It also read a day of heart rate back 2 to 3 times
faster. Open Wearables stored the data in **3.4 times less disk**, and Open Health Sync keeps
every payload verbatim on top of that.

In 2 of the 6 Open Wearables runs, records sent were never stored although the upload was
answered `202`: see [Records lost by Open Wearables](#records-lost-by-open-wearables).

These are numbers for one user, one machine and a synthetic dataset. They say how the two
ingest paths compare, not what either one does on your hardware.

## What is compared

Both systems take Apple Health data from a phone app the same way: the app reads HealthKit
and posts batches to the server, which answers at once and normalizes the batch in the
background.

| | Open Health Sync | Open Wearables |
|---|---|---|
| Upload route | `POST /v1/ingest/apple-health` | `POST /api/v1/sdk/users/{id}/sync` |
| Body | gzip NDJSON, one HealthKit sample per line | JSON `SyncRequest`, samples in `data.records` |
| Answer | `200` once the raw part is stored and its job queued | `202` once the task is queued |
| Queue | Postgres (`FOR UPDATE SKIP LOCKED`) | Redis, Celery |
| Background work | 4 ETL jobs at once, same process | Celery I/O worker, thread pool (4 threads on 4 CPUs) |
| Processes | `ohs serve` (all roles) | `fastapi run` (API), `celery worker` (I/O queues), `redis-server` |
| Raw payload | kept on disk, never rewritten | not kept (`RAW_PAYLOAD_STORAGE=disabled`, the default) |
| Stored as | `ohs_data.quantity_samples` | `data_point_series` |

Each system runs with its default production settings, as its own start scripts run it:
Open Wearables with `scripts/start/app.sh` and the I/O worker of `scripts/start/worker.sh`
(the CPU worker serves only the XML import queue and Celery beat only schedules periodic
jobs, so neither is started); Open Health Sync with `ohs serve` and the database pool size of
`deploy/docker-compose.yml`. Open Wearables is told not to send telemetry, Sentry is off,
and outgoing webhooks are off (its default).

## Method

**Data.** [`gen.py`](../../bench/openwearables/gen.py) generates one user's Apple Watch
data for 14 days (2026-01-05 to 2026-01-18, Europe/Paris) from a fixed seed, and writes it in
the upload format of each system. The records are the same in both: same HealthKit type,
times, value, unit, source and device. Four quantity types:

| Type | Every | A day |
|---|---|---:|
| `HKQuantityTypeIdentifierHeartRate` | 60 s (baseline) or 5 s (dense) | 1,440 or 17,280 |
| `HKQuantityTypeIdentifierStepCount` | 5 min | 288 |
| `HKQuantityTypeIdentifierActiveEnergyBurned` | 5 min | 288 |
| `HKQuantityTypeIdentifierOxygenSaturation` | 1 h | 24 |

One request carries one type for one day, for both systems: 56 requests. Each is sent in its
system's own wire format, so Open Health Sync sends gzip (2.8 MB for the dense set) and Open
Wearables plain JSON (95 MB), since it does not accept a compressed body.

**Run.** [`bench.py`](../../bench/openwearables/bench.py) runs one system at a time:

1. Drop and create its database, apply its migrations and seeds, start its processes (not
   timed).
2. Create a tenant or developer, an API key and one user, then wait 5 s and record the idle
   memory.
3. Send the 56 requests with 4 concurrent clients, recording each request's latency.
4. Poll the stored record count every 250 ms until every record is stored, or until the
   queue is empty and the count has not moved for 10 s. End to end time runs from the first
   request sent to the moment the last record was seen stored.
5. Read one local day of heart rate (2026-01-12) back through the API, every page at the
   largest page size both allow (1,000), 5 times to warm up and then 50 times.
6. Record the database size, stop the processes.

During steps 3 and 4 it samples, every 100 ms, the RSS of every process of the system under
test (with Redis counted for Open Wearables, since it cannot ingest without it) and their CPU
time, and the CPU time of the Postgres container from its cgroup. Postgres is not in the
memory figures: it is the same server for both.

[`run.sh`](../../bench/openwearables/run.sh) runs both profiles three times, alternating the
systems, and [`report.py`](../../bench/openwearables/report.py) prints the median of each
metric.

**Machine.** A 4 vCPU cloud VM (Intel Xeon at 2.10 GHz), 15 GB RAM, Linux 6.18, no swap. Both
systems run on the host. Postgres is one `timescale/timescaledb:latest-pg17` container (the
image Open Health Sync's compose file pins; Open Wearables' compose file uses `postgres:18`)
with default settings, shared by both systems but used by one at a time, each in its own
database. Go 1.26.0, Python 3.14.6 (Open Wearables' locked `uv` environment), Redis 7.0.15.
The load generator runs on the same machine.

## Results

### baseline: 28,560 records in 56 requests (14 days, heart rate every 60 s), 4 clients, median of 3 runs

| Metric | Open Health Sync | Open Wearables |
|---|---:|---:|
| End to end, upload to stored (s) | 2.40 | 4.90 |
| End to end throughput (records/s) | 11,890 | 5,772 |
| Upload phase alone (records/s) | 158,604 | 30,354 |
| Upload latency p50 (ms) | 11.9 | 56.5 |
| Upload latency p95 (ms) | 27.2 | 191.4 |
| Upload latency p99 (ms) | 36.0 | 212.8 |
| Bytes sent (MB) | 0.36 | 10.8 |
| Idle memory, RSS (MB) | 22.1 | 493.7 |
| Peak memory, RSS (MB) | 45.3 | 589.2 |
| App CPU per 10k records (s) | 0.81 | 1.62 |
| Postgres CPU per 10k records (s) | 0.83 | 1.21 |
| Read one day of heart rate p50 (ms) | 29.9 | 59.2 |
| Read one day of heart rate p95 (ms) | 36.6 | 255.2 |
| Database size (MB) | 39.0 | 17.8 |
| Raw payloads kept (MB) | 0.36 | 0.00 |
| Records stored, per run (of sent) | 28,560 (28,560)<br>28,560 (28,560)<br>28,560 (28,560) | 28,272 (28,560)<br>27,120 (28,560)<br>28,560 (28,560) |

### dense: 250,320 records in 56 requests (14 days, heart rate every 5 s), 4 clients, median of 3 runs

| Metric | Open Health Sync | Open Wearables |
|---|---:|---:|
| End to end, upload to stored (s) | 11.0 | 34.9 |
| End to end throughput (records/s) | 22,760 | 7,178 |
| Upload phase alone (records/s) | 862,841 | 49,716 |
| Upload latency p50 (ms) | 10.3 | 234.6 |
| Upload latency p95 (ms) | 50.5 | 1,024 |
| Upload latency p99 (ms) | 57.8 | 1,165 |
| Bytes sent (MB) | 2.82 | 94.7 |
| Idle memory, RSS (MB) | 22.1 | 493.6 |
| Peak memory, RSS (MB) | 299.3 | 1,381 |
| App CPU per 10k records (s) | 0.60 | 1.58 |
| Postgres CPU per 10k records (s) | 0.60 | 0.84 |
| Read one day of heart rate p50 (ms) | 315.2 | 917.6 |
| Read one day of heart rate p95 (ms) | 383.4 | 1,017 |
| Database size (MB) | 253.4 | 75.0 |
| Raw payloads kept (MB) | 2.82 | 0.00 |
| Records stored, per run (of sent) | 250,320 (250,320)<br>250,320 (250,320)<br>250,320 (250,320) | 250,320 (250,320)<br>250,320 (250,320)<br>250,320 (250,320) |

Read the tables with these in mind:

- **End to end on the baseline set is bounded by Open Health Sync's idle poll.** Its uploads
  took 0.2 to 0.3 s in all, and an idle ETL worker looks for new jobs every 1 to 2 s, so the
  first job of a run waits up to 2 s. That is why the three baseline runs spread from 1.6 to
  3.1 s. On the dense set the work dominates and the runs agree within 2 s.
- **"Upload phase alone"** is the records sent divided by the time to send every request: how
  fast a client is let go, not how fast data is processed. Open Health Sync answers once the
  raw part is written and its job queued; Open Wearables once the Celery task is queued.
- **Memory.** Open Wearables' total is its API (about 240 MB idle), its worker (about 240 MB
  idle) and Redis. Under the dense load its worker peaked near 950 MB and Redis at 140 MB,
  because the whole JSON payload of each queued task sits in Redis. Open Health Sync is one
  process; its peak grows with the size of the part being normalized (45 MB baseline, about
  300 MB dense, where a part holds 17,280 heart rate lines).
- **Disk.** Open Health Sync's database is 3.4 times larger for the same records: each row
  keeps the provider's fields and the original value and unit in `metadata` (see
  [data-model.md](../data-model.md)), and daily summaries are derived as well. It also keeps
  every payload, compressed, in the raw store. Open Wearables keeps a narrower row and no
  payload.
- **Reads.** Both read 1,440 rows (baseline) or 17,280 rows (dense) for the day: 2 or 18 pages.
  The two responses do not carry the same fields, so this compares the read paths as each
  ships them, not equal work.

## Records lost by Open Wearables

In baseline runs 1 and 2, Open Wearables stored 28,272 and 27,120 of 28,560 records (288
records, one day of a 5-minute type, then 1,440, one day of heart rate), although all 56 uploads answered `202`. Its worker log
shows, for each lost batch:

```
IntegrityError('(psycopg.errors.UniqueViolation) duplicate key value violates unique
constraint "ix_user_connection_user_provider"
DETAIL:  Key (user_id, provider)=(..., apple) already exists.')
```

The first batches of a new user reach the worker together. Each one creates the user's Apple
connection (`ensure_sdk_connection` in `process_sdk_upload`); one insert wins, the others
fail on the unique index, and the task fails with its batch, after the client was already
told `202`. The dense runs and baseline run 3 lost nothing: it depends on which batches
start together. A phone app that sends its first batches one after another would not hit
it. The records missing from those two runs make Open Wearables' end to end time for them
slightly shorter, not longer.

Open Health Sync stored every record in every run. It answers an upload only after the raw
part is on disk, and a job that fails is retried and then parked where `GET /coverage` and
`ohs_data.failed_records` show it.

## Not measured

- Many users at once, or uploads spread over time (here one user sends 14 days at once).
- Pulls from cloud providers (Withings, Oura, Whoop), sleep, workouts and other line kinds.
- Horizontal scaling: more Celery workers, more `ohs` processes with the `etl` role.
- Open Wearables with `RAW_PAYLOAD_STORAGE=s3` or `SDK_PAYLOAD_S3_OFFLOAD=true`, which change
  its upload path, and Open Health Sync with an S3 raw store.
- A Raspberry Pi or another small machine.

## Running it again

Needs Docker, Go, git, [uv](https://docs.astral.sh/uv/), `psql` and `redis-server` on the
host. From the repository root:

```sh
bench/openwearables/setup.sh   # Postgres container on 127.0.0.1:15432, bin/ohs, Open Wearables checkout
bench/openwearables/run.sh     # both profiles, 3 runs each, then the tables above
```

`run.sh` writes its results to `/tmp/ohs-bench/results` (`BENCH_WORK` changes the
directory) and took about 12 minutes here. `BENCH_RUNS` and `BENCH_CONCURRENCY` change the number
of runs and of clients; `OW_REF` picks another Open Wearables commit. One run of one system:

```sh
python3 bench/openwearables/gen.py --out /tmp/ohs-bench/data-dense --hr-interval 5
python3 bench/openwearables/bench.py --system ohs --data /tmp/ohs-bench/data-dense --out /tmp/ohs-bench/ohs.json
```

Each run drops and recreates the system's database in the benchmark container. Do not point
it at a Postgres that holds anything you want to keep.
