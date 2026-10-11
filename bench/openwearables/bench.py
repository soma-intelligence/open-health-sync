#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Run the ingestion benchmark against one system and write its results as JSON.

    bench.py --system ohs --data DIR --out results/ohs.json
    bench.py --system ow  --data DIR --out results/ow.json

Each run starts from an empty database, starts the system's processes, uploads
every request of DIR/manifest.json (written by gen.py) with --concurrency
clients, waits until every record is stored, then reads one day of heart rate
back --reads times. It samples the RSS and CPU time of the system's own
processes every 100 ms and the CPU time of the shared Postgres container.
Standard library only. setup.sh prepares Postgres, the Open Health Sync
binary and the Open Wearables checkout; see docs/benchmarks/open-wearables.md.
"""

import argparse
import base64
import json
import os
import shutil
import signal
import statistics
import subprocess
import threading
import time
from datetime import datetime, timedelta
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

PAGE = os.sysconf("SC_PAGE_SIZE")
TICK = os.sysconf("SC_CLK_TCK")
PG_CONTAINER = os.environ.get("BENCH_PG_CONTAINER", "ohs-bench-pg")


def sh(*cmd, env=None, check=True, cwd=None):
    r = subprocess.run(cmd, env=env, cwd=cwd, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit(f"{' '.join(cmd)} failed:\n{r.stdout}\n{r.stderr}")
    return r.stdout


def psql(db, sql):
    # The host client, so the Postgres container's CPU time holds only the server's work.
    port = os.environ.get("BENCH_PG_PORT", "15432")
    return sh("psql", "-h", "127.0.0.1", "-p", port, "-U", "postgres", "-d", db, "-tAc", sql).strip()


def reset_db(name, owner):
    psql("postgres", f'DROP DATABASE IF EXISTS "{name}" WITH (FORCE)')
    psql("postgres", f'CREATE DATABASE "{name}" OWNER "{owner}"')


def pg_cpu_seconds():
    """CPU time used so far by the Postgres container (cgroup v1 or v2)."""
    cid = sh("docker", "inspect", "-f", "{{.Id}}", PG_CONTAINER).strip()
    for path in (f"/sys/fs/cgroup/cpuacct/docker/{cid}/cpuacct.usage",):
        if os.path.exists(path):
            return int(Path(path).read_text()) / 1e9
    for path in (f"/sys/fs/cgroup/system.slice/docker-{cid}.scope/cpu.stat", f"/sys/fs/cgroup/docker/{cid}/cpu.stat"):
        if os.path.exists(path):
            for line in Path(path).read_text().splitlines():
                if line.startswith("usage_usec"):
                    return int(line.split()[1]) / 1e6
    return float("nan")


def http(method, url, body=None, headers=None, timeout=300):
    req = urllib.request.Request(url, data=body, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read(), dict(r.headers)
    except urllib.error.HTTPError as e:
        return e.code, e.read(), dict(e.headers)


def wait_http(url, ok=lambda s: s < 500, timeout=120):
    end = time.time() + timeout
    while time.time() < end:
        try:
            s, _, _ = http("GET", url, timeout=2)
            if ok(s):
                return
        except OSError:
            pass
        time.sleep(0.2)
    raise SystemExit(f"{url} did not come up")


def proc_tree(roots):
    """PIDs of roots and all their descendants."""
    children = {}
    for p in os.listdir("/proc"):
        if p.isdigit():
            try:
                stat = Path(f"/proc/{p}/stat").read_text()
            except OSError:
                continue
            ppid = int(stat.rsplit(")", 1)[1].split()[1])
            children.setdefault(ppid, []).append(int(p))
    out, todo = [], list(roots)
    while todo:
        pid = todo.pop()
        out.append(pid)
        todo.extend(children.get(pid, []))
    return out


def usage(pids):
    """(RSS bytes, CPU seconds) summed over pids."""
    rss, cpu = 0, 0.0
    for pid in pids:
        try:
            rss += int(Path(f"/proc/{pid}/statm").read_text().split()[1]) * PAGE
            f = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
            cpu += (int(f[11]) + int(f[12])) / TICK  # utime, stime
        except OSError:
            pass
    return rss, cpu


class Sampler(threading.Thread):
    """Samples RSS and CPU of named process groups every interval."""

    def __init__(self, groups, interval=0.1):
        super().__init__(daemon=True)
        self.groups, self.interval = groups, interval
        self.samples = []  # (t, {group: rss})
        self.stop_evt = threading.Event()

    def snapshot(self):
        return {g: usage(proc_tree(pids)) for g, pids in self.groups.items()}

    def run(self):
        while not self.stop_evt.is_set():
            snap = self.snapshot()
            self.samples.append((time.time(), {g: v[0] for g, v in snap.items()}))
            self.stop_evt.wait(self.interval)

    def peak(self):
        out = {}
        for _, snap in self.samples:
            for g, v in snap.items():
                out[g] = max(out.get(g, 0), v)
        out["total"] = max((sum(s.values()) for _, s in self.samples), default=0)
        return out


def pct(xs, p):
    xs = sorted(xs)
    if not xs:
        return None
    k = (len(xs) - 1) * p / 100
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


def summary(ms):
    return {
        "n": len(ms), "mean": statistics.fmean(ms), "p50": pct(ms, 50), "p95": pct(ms, 95),
        "p99": pct(ms, 99), "max": max(ms),
    }


class System:
    """What bench.py needs from a system under test."""

    procs: dict  # group name -> list of Popen
    log_dir: Path

    def spawn(self, group, cmd, env=None, cwd=None, log=None):
        f = open(self.log_dir / f"{log or group}.log", "w")
        p = subprocess.Popen(cmd, env=env, cwd=cwd, stdout=f, stderr=subprocess.STDOUT, start_new_session=True)
        self.procs.setdefault(group, []).append(p)
        return p

    def groups(self):
        return {g: [p.pid for p in ps] for g, ps in self.procs.items()}

    def stop(self):
        for ps in self.procs.values():
            for p in ps:
                try:
                    os.killpg(p.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
        for ps in self.procs.values():
            for p in ps:
                try:
                    p.wait(timeout=40)
                except subprocess.TimeoutExpired:
                    os.killpg(p.pid, signal.SIGKILL)


class OHS(System):
    name = "open-health-sync"
    db = "ohs"

    def __init__(self, args):
        self.args, self.procs = args, {}
        self.base = "http://127.0.0.1:18080"
        self.raw = Path(args.work) / "ohs-raw"
        self.env = dict(
            os.environ,
            OHS_DATABASE_URL=f"postgres://ohs:ohs@127.0.0.1:{args.pg_port}/ohs?sslmode=disable&pool_max_conns=20",
            OHS_ENCRYPTION_KEY=base64.b64encode(os.urandom(32)).decode(),  # a fresh key per run
            OHS_RAW_STORE=f"file://{self.raw}",
            OHS_LISTEN="127.0.0.1:18080",
            OHS_PUBLIC_URL="http://127.0.0.1:18080",
            OHS_LOG_LEVEL="warn",
        )

    def version(self):
        return sh(self.args.ohs_bin, "version", env=self.env).strip()

    def start(self):
        reset_db(self.db, "ohs")
        shutil.rmtree(self.raw, ignore_errors=True)
        self.raw.mkdir(parents=True)
        sh(self.args.ohs_bin, "migrate", "up", env=self.env)
        self.spawn("server", [self.args.ohs_bin, "serve"], env=self.env)
        wait_http(self.base + "/readyz", ok=lambda s: s == 200)

    def setup_user(self):
        out = sh(self.args.ohs_bin, "admin", "tenant", "create", "--name", "bench", env=self.env)
        self.key = next(w for w in out.split() if w.startswith("ohs_sk_"))
        s, b, _ = http("POST", self.base + "/v1/users", b"{}", self.tenant_headers())
        self.user = json.loads(b)["id"]
        body = json.dumps({"scopes": ["upload"], "ttl_seconds": 86400}).encode()
        s, b, _ = http("POST", f"{self.base}/v1/users/{self.user}/tokens", body, self.tenant_headers())
        self.token = json.loads(b)["token"]

    def tenant_headers(self):
        return {"Authorization": f"Bearer {self.key}", "Content-Type": "application/json"}

    def upload(self, data_dir, r):
        body = (data_dir / "ohs" / f"{r['name']}.ndjson.gz").read_bytes()
        headers = {
            "Authorization": f"Bearer {self.token}", "Content-Type": "application/gzip",
            "X-OHS-Type": r["type"], "X-OHS-Day": r["day"], "X-OHS-Batch": "bench", "X-OHS-Part": str(r["part"]),
        }
        return http("POST", self.base + "/v1/ingest/apple-health", body, headers), len(body)

    def stored(self):
        return int(psql(self.db, "SELECT count(*) FROM ohs_data.quantity_samples"))

    def queue_empty(self):
        s, b, _ = http("GET", f"{self.base}/v1/users/{self.user}/coverage", headers=self.tenant_headers())
        return s == 200 and json.loads(b)["pending_jobs"] == 0

    def read_day(self, day, next_day):
        rows, cursor = 0, None
        while True:
            q = {"type": "heart_rate", "start": day, "end": next_day, "limit": 1000}
            if cursor:
                q["cursor"] = cursor
            s, b, _ = http("GET", f"{self.base}/v1/users/{self.user}/samples?{urllib.parse.urlencode(q)}",
                           headers=self.tenant_headers())
            assert s == 200, (s, b[:300])
            page = json.loads(b)
            rows += len(page["data"])
            cursor = page.get("next_cursor")
            if not cursor:
                return rows


class OW(System):
    name = "open-wearables"
    db = "open-wearables"

    def __init__(self, args):
        self.args, self.procs = args, {}
        self.base = "http://127.0.0.1:18000"
        self.backend = Path(args.ow_dir) / "backend"
        self.py = self.backend / ".venv" / "bin"
        self.env = dict(
            os.environ,
            DB_HOST="127.0.0.1", DB_PORT=str(args.pg_port), REDIS_HOST="127.0.0.1", REDIS_PORT="16379",
            ENVIRONMENT="production", SENTRY_ENABLED="false", TELEMETRY_ENABLED="false",
            OUTGOING_WEBHOOKS_ENABLED="false", ACCESS_LOG_LEVEL="errors", LOG_LEVEL="WARNING",
            ADMIN_EMAIL="admin@admin.com", ADMIN_PASSWORD="bench-password",
        )

    def version(self):
        sha = sh("git", "-C", str(self.backend.parent), "rev-parse", "--short", "HEAD").strip()
        ver = sh(str(self.py / "python"), "-c", "import tomllib;print(tomllib.load(open('pyproject.toml','rb'))['project']['version'])",
                 cwd=self.backend).strip()
        return f"{ver} ({sha})"

    def start(self):
        reset_db(self.db, "open-wearables")
        self.spawn("redis", ["redis-server", "--port", "16379", "--save", "", "--appendonly", "no"])
        time.sleep(0.5)
        # The schema and seed steps of scripts/start/app.sh that ingest needs. Not timed.
        sh(str(self.py / "alembic"), "upgrade", "head", env=self.env, cwd=self.backend)
        for script in ("scripts/init_provider_settings.py", "scripts/init_device_priorities.py",
                       "scripts/init_provider_priorities.py", "scripts/init/seed_admin.py",
                       "scripts/init/seed_series_types.py", "scripts/init/seed_archival_settings.py"):
            sh(str(self.py / "python"), script, env=self.env, cwd=self.backend)
        # The production commands of scripts/start/app.sh and worker.sh: one FastAPI
        # process, and the I/O worker that consumes the sdk_sync queue (thread pool).
        self.spawn("api", [str(self.py / "fastapi"), "run", "app/main.py", "--host", "127.0.0.1", "--port", "18000"],
                   env=self.env, cwd=self.backend)
        self.spawn("worker", [str(self.py / "celery"), "-A", "app.main:celery_app", "worker", "--loglevel=warning",
                              "--pool=threads", "-Q", "default,sdk_sync,garmin_sync,webhook_sync", "-n", "io@bench"],
                   env=self.env, cwd=self.backend)
        wait_http(self.base + "/api/v1/users", ok=lambda s: s in (401, 403))
        end = time.time() + 60
        while time.time() < end:  # the worker is up once it answers a ping
            r = subprocess.run([str(self.py / "celery"), "-A", "app.main:celery_app", "inspect", "ping", "-t", "2"],
                               env=self.env, cwd=self.backend, capture_output=True)
            if r.returncode == 0:
                return
        raise SystemExit("celery worker did not come up")

    def setup_user(self):
        form = urllib.parse.urlencode({"username": "admin@admin.com", "password": "bench-password"}).encode()
        s, b, _ = http("POST", self.base + "/api/v1/auth/login", form,
                       {"Content-Type": "application/x-www-form-urlencoded"})
        jwt = json.loads(b)["access_token"]
        s, b, _ = http("POST", self.base + "/api/v1/developer/api-keys", b'{"name":"bench"}',
                       {"Authorization": f"Bearer {jwt}", "Content-Type": "application/json"})
        self.key = json.loads(b)["key"]
        s, b, _ = http("POST", self.base + "/api/v1/users", b"{}", self.key_headers())
        self.user = json.loads(b)["id"]

    def key_headers(self):
        return {"X-Open-Wearables-API-Key": self.key, "Content-Type": "application/json"}

    def upload(self, data_dir, r):
        body = (data_dir / "ow" / f"{r['name']}.json").read_bytes()
        return http("POST", f"{self.base}/api/v1/sdk/users/{self.user}/sync", body, self.key_headers()), len(body)

    def stored(self):
        return int(psql(self.db, "SELECT count(*) FROM data_point_series"))

    def queue_empty(self):
        out = sh("redis-cli", "-p", "16379", "llen", "sdk_sync").strip()
        return out == "0"

    def read_day(self, day, next_day):
        rows, cursor = 0, None
        while True:
            q = {"types": "heart_rate", "start_time": f"{day}T00:00:00+01:00", "end_time": f"{next_day}T00:00:00+01:00",
                 "limit": 1000}
            if cursor:
                q["cursor"] = cursor
            s, b, _ = http("GET", f"{self.base}/api/v1/users/{self.user}/timeseries?{urllib.parse.urlencode(q)}",
                           headers=self.key_headers())
            assert s == 200, (s, b[:300])
            page = json.loads(b)
            rows += len(page["data"])
            cursor = (page.get("pagination") or {}).get("next_cursor")
            if not cursor:
                return rows


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--system", choices=["ohs", "ow"], required=True)
    ap.add_argument("--data", type=Path, required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--work", default="/tmp/ohs-bench")
    ap.add_argument("--concurrency", type=int, default=4)
    ap.add_argument("--reads", type=int, default=50)
    ap.add_argument("--pg-port", type=int, default=int(os.environ.get("BENCH_PG_PORT", "15432")))
    ap.add_argument("--ohs-bin", default="bin/ohs")
    ap.add_argument("--ow-dir", default="/tmp/ohs-bench/open-wearables")
    ap.add_argument("--timeout", type=int, default=3600, help="seconds to wait for processing")
    args = ap.parse_args()

    manifest = json.loads((args.data / "manifest.json").read_text())
    expected = manifest["records"]
    sut = OHS(args) if args.system == "ohs" else OW(args)
    sut.log_dir = Path(args.work) / "logs" / args.system
    sut.log_dir.mkdir(parents=True, exist_ok=True)

    try:
        sut.start()
        sut.setup_user()
        time.sleep(5)  # let start-up work settle before the idle sample
        idle = Sampler(sut.groups())
        idle_rss = {g: v[0] for g, v in idle.snapshot().items()}
        idle_rss["total"] = sum(idle_rss.values())

        sampler = Sampler(sut.groups())
        cpu0 = {g: v[1] for g, v in sampler.snapshot().items()}
        pg0 = pg_cpu_seconds()
        sampler.start()

        lat, statuses, retries, sent_bytes = [], {}, 0, 0
        lock = threading.Lock()

        def send(r):
            nonlocal retries, sent_bytes
            while True:
                t = time.perf_counter()
                (s, b, h), n = sut.upload(args.data, r)
                ms = (time.perf_counter() - t) * 1000
                if s == 503:  # back-pressure: honour Retry-After, count it
                    with lock:
                        retries += 1
                    time.sleep(float(h.get("Retry-After", "1")))
                    continue
                with lock:
                    lat.append(ms)
                    statuses[s] = statuses.get(s, 0) + 1
                    sent_bytes += n
                if s >= 400:
                    print(f"upload {r['name']}: {s} {b[:200]!r}")
                return

        t0 = time.perf_counter()
        with ThreadPoolExecutor(args.concurrency) as pool:
            list(pool.map(send, manifest["requests"]))
        t_uploaded = time.perf_counter() - t0

        # Processed: every record stored, or the queue empty and the count still
        # for 10 s (some records were not stored).
        last, t_last = -1, time.perf_counter()
        deadline = time.perf_counter() + args.timeout
        while True:
            n = sut.stored()
            now = time.perf_counter()
            if n != last:
                last, t_last = n, now
            if n >= expected:
                break
            if now - t_last > 10 and sut.queue_empty():
                break
            if now > deadline:
                print("timeout waiting for processing")
                break
            time.sleep(0.25)
        t_done = t_last - t0
        sampler.stop_evt.set()
        sampler.join()
        cpu1 = {g: v[1] for g, v in sampler.snapshot().items()}
        pg1 = pg_cpu_seconds()

        # Reads: one local day of heart rate, every page, after a short warm-up.
        start = datetime.strptime(manifest["start"], "%Y-%m-%d")
        day = (start + timedelta(days=manifest["days"] // 2)).strftime("%Y-%m-%d")
        next_day = (start + timedelta(days=manifest["days"] // 2 + 1)).strftime("%Y-%m-%d")
        for _ in range(5):
            rows = sut.read_day(day, next_day)
        read_ms = []
        for _ in range(args.reads):
            t = time.perf_counter()
            rows = sut.read_day(day, next_day)
            read_ms.append((time.perf_counter() - t) * 1000)
        db_bytes = int(psql("postgres", f"SELECT pg_database_size('{sut.db}')"))
        raw_bytes = sum(f.stat().st_size for f in Path(sut.raw).rglob("*") if f.is_file()) if args.system == "ohs" else 0

        result = {
            "system": sut.name, "version": sut.version(), "concurrency": args.concurrency,
            "dataset": {k: manifest[k] for k in ("seed", "days", "start", "hr_interval", "records")}
            | {"requests": len(manifest["requests"]), "request_bytes": sent_bytes},
            "ingest": {
                "statuses": statuses, "retries_503": retries, "stored": last, "expected": expected,
                "upload_seconds": t_uploaded, "end_to_end_seconds": t_done,
                "upload_records_per_s": expected / t_uploaded, "end_to_end_records_per_s": last / t_done,
                "upload_latency_ms": summary(lat),
            },
            "memory_bytes": {"idle": idle_rss, "peak": sampler.peak()},
            "cpu_seconds": {g: cpu1[g] - cpu0[g] for g in cpu1} | {"postgres": pg1 - pg0},
            "read": {"day": day, "rows": rows, "latency_ms": summary(read_ms)},
            "storage_bytes": {"database": db_bytes, "raw_files": raw_bytes},
        }
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(result, indent=1))
        print(json.dumps(result, indent=1))
    finally:
        sut.stop()


if __name__ == "__main__":
    main()
