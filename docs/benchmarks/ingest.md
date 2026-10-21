# Benchmark: Apple Health ingestion

How fast Open Health Sync takes in a batch of Apple Health data, how much memory and CPU it
uses doing it, and how fast it reads a day back. Measured on 2026-10-11 at `c8fdf12` (v0.0.2
plus one commit). Everything needed to run it again is in [`bench/ingest/`](../../bench/ingest),
and the raw results of the runs below are in
[`bench/ingest/results/2026-10-11/`](../../bench/ingest/results/2026-10-11).

## Summary

On a 4 vCPU machine, one `ohs serve` process stored 28,560 records (two weeks of an Apple Watch,
heart rate every minute) in 2.4 s end to end and 250,320 records (heart rate every 5 seconds) in
11 s, at 11,900 and 22,800 records a second. It answered uploads in about 10 ms (median), idled
in **22 MB** of memory and peaked at 45 MB and 300 MB. Every record was stored in every run, and
every payload was kept verbatim in the raw store.

These are numbers for one user, one machine and a synthetic dataset. They say what the ingest
path costs, not what it does on your hardware.

## What is measured

A phone app reads HealthKit and posts batches to `POST /v1/ingest/apple-health`: gzip NDJSON,
one HealthKit sample per line. The server answers `200` once the raw part is stored and its job
queued in Postgres (`FOR UPDATE SKIP LOCKED`), then normalizes the part in the background (4
ETL jobs at once, same process) into `ohs_data.quantity_samples`. The raw payload is kept on
disk and never rewritten. `ohs serve` runs all roles, with the database pool size of
`deploy/docker-compose.yml`.

## Method

**Data.** `ingest gen` generates one user's Apple Watch data for 14 days (2026-01-05 to
2026-01-18, Europe/Paris) from a fixed seed. Four quantity types:

| Type | Every | A day |
|---|---|---:|
| `HKQuantityTypeIdentifierHeartRate` | 60 s (baseline) or 5 s (dense) | 1,440 or 17,280 |
| `HKQuantityTypeIdentifierStepCount` | 5 min | 288 |
| `HKQuantityTypeIdentifierActiveEnergyBurned` | 5 min | 288 |
| `HKQuantityTypeIdentifierOxygenSaturation` | 1 h | 24 |

One request carries one type for one day: 56 requests, 0.36 MB of gzip for the baseline set
and 2.8 MB for the dense one.

**Run.** `ingest run`:

1. Drops and creates the database, applies the migrations, starts `ohs serve` (not timed).
2. Creates a tenant, an API key, one user and an upload token, then waits 5 s and records the
   idle memory.
3. Sends the 56 requests with 4 concurrent clients, recording each request's latency.
4. Polls the stored record count every 250 ms until every record is stored, or until the
   queue is empty and the count has not moved for 10 s. End to end time runs from the first
   request sent to the moment the last record was seen stored.
5. Reads one local day of heart rate (2026-01-12) back through the API, every page at 1,000
   rows, 5 times to warm up and then 50 times.
6. Records the database size and the raw store size, stops the server.

During steps 3 and 4 it samples, every 100 ms, the RSS and CPU time of the server process,
and the CPU time of the Postgres container from its cgroup. Postgres is not in the memory
figures.

`bench/ingest/run.sh` runs both profiles three times, and `ingest report` prints the median of
each metric.

**Machine.** A 4 vCPU cloud VM (Intel Xeon at 2.10 GHz), 15 GB RAM, Linux 6.18, no swap. The
server runs on the host. Postgres is one `timescale/timescaledb:latest-pg17` container (the
image the compose file pins) with default settings. Go 1.26.0. The load generator runs on the
same machine.

## Results

### baseline: 28,560 records in 56 requests (14 days, heart rate every 60 s), 4 clients, median of 3 runs

| Metric | Open Health Sync |
|---|---:|
| End to end, upload to stored (s) | 2.40 |
| End to end throughput (records/s) | 11,890 |
| Upload phase alone (records/s) | 158,604 |
| Upload latency p50 (ms) | 11.9 |
| Upload latency p95 (ms) | 27.2 |
| Upload latency p99 (ms) | 36.0 |
| Bytes sent (MB) | 0.36 |
| Idle memory, RSS (MB) | 22.1 |
| Peak memory, RSS (MB) | 45.3 |
| Server CPU per 10k records (s) | 0.81 |
| Postgres CPU per 10k records (s) | 0.83 |
| Read one day of heart rate p50 (ms) | 29.9 |
| Read one day of heart rate p95 (ms) | 36.6 |
| Database size (MB) | 39.0 |
| Raw payloads kept (MB) | 0.36 |
| Records stored, per run (of sent) | 28,560 (28,560)<br>28,560 (28,560)<br>28,560 (28,560) |

### dense: 250,320 records in 56 requests (14 days, heart rate every 5 s), 4 clients, median of 3 runs

| Metric | Open Health Sync |
|---|---:|
| End to end, upload to stored (s) | 11.0 |
| End to end throughput (records/s) | 22,760 |
| Upload phase alone (records/s) | 862,841 |
| Upload latency p50 (ms) | 10.3 |
| Upload latency p95 (ms) | 50.5 |
| Upload latency p99 (ms) | 57.8 |
| Bytes sent (MB) | 2.82 |
| Idle memory, RSS (MB) | 22.1 |
| Peak memory, RSS (MB) | 299.3 |
| Server CPU per 10k records (s) | 0.60 |
| Postgres CPU per 10k records (s) | 0.60 |
| Read one day of heart rate p50 (ms) | 315.2 |
| Read one day of heart rate p95 (ms) | 383.4 |
| Database size (MB) | 253.4 |
| Raw payloads kept (MB) | 2.82 |
| Records stored, per run (of sent) | 250,320 (250,320)<br>250,320 (250,320)<br>250,320 (250,320) |

Read the tables with these in mind:

- **End to end on the baseline set is bounded by the idle poll.** Uploads took 0.2 to 0.3 s in
  all, and an idle ETL worker looks for new jobs every 1 to 2 s, so the first job of a run
  waits up to 2 s. That is why the three baseline runs spread from 1.6 to 3.1 s. On the dense
  set the work dominates and the runs agree within 2 s.
- **"Upload phase alone"** is the records sent divided by the time to send every request: how
  fast a client is let go, not how fast data is processed.
- **Memory.** The peak grows with the size of the part being normalized (45 MB baseline, about
  300 MB dense, where a part holds 17,280 heart rate lines).
- **Disk.** Each row keeps the provider's fields and the original value and unit in `metadata`
  (see [data-model.md](../data-model.md)), and daily summaries are derived as well. Every
  payload is also kept, compressed, in the raw store.
- **No record lost.** An upload is answered only after the raw part is on disk, and a job that
  fails is retried and then parked where `GET /coverage` and `ohs_data.failed_records` show it.

The 2026-10-11 runs used an earlier version of this harness with the same dataset shape:
the values come from the same distributions, but not the same numbers byte for byte.

## Not measured

- Many users at once, or uploads spread over time (here one user sends 14 days at once).
- Pulls from cloud providers (Withings, Oura, WHOOP), sleep, workouts and other line kinds.
- Horizontal scaling: more `ohs` processes with the `etl` role.
- An S3 raw store.
- A Raspberry Pi or another small machine.

## Running it again

Needs Docker, Go and `psql` on the host. From the repository root:

```sh
bench/ingest/setup.sh   # Postgres container on 127.0.0.1:15432, bin/ohs
bench/ingest/run.sh     # both profiles, 3 runs each, then the tables above
```

`run.sh` writes its results to `/tmp/ohs-bench/results` (`BENCH_WORK` changes the directory).
`BENCH_RUNS` and `BENCH_CONCURRENCY` change the number of runs and of clients. One run:

```sh
go run ./bench/ingest gen -out /tmp/ohs-bench/data-dense -hr-interval 5
go run ./bench/ingest run -data /tmp/ohs-bench/data-dense -out /tmp/ohs-bench/results/dense-1.json
go run ./bench/ingest report /tmp/ohs-bench/results
```

Each run drops and recreates the `ohs` database in the benchmark container. Do not point it at
a Postgres that holds anything you want to keep.
