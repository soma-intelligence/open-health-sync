#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Generate one Apple Health dataset in the upload format of both systems.

The same source records are written twice, grouped the same way: one request
per (day, type). For Open Health Sync a request is a gzip NDJSON part
(POST /v1/ingest/apple-health); for Open Wearables it is a JSON SyncRequest
(POST /api/v1/sdk/users/{id}/sync). Output is deterministic for a given seed.

    gen.py --out DIR [--days 14] [--hr-interval 60] [--seed 42]

Writes DIR/ohs/<day>_<part>.ndjson.gz, DIR/ow/<day>_<part>.json and
DIR/manifest.json (requests, record counts, headers).
"""

import argparse
import gzip
import json
import random
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

TZ_NAME = "Europe/Paris"
TZ = timezone(timedelta(hours=1))  # January in Paris: UTC+01:00, no DST change
ZONE_OFFSET = "+01:00"
SOURCE = {
    "name": "Apple Watch",
    "bundleIdentifier": "com.apple.health.BENCH",
    "deviceModel": "Watch",
    "productType": "Watch7,1",
    "deviceType": "watch",
}

# (HealthKit type, HealthKit unit, seconds between samples; None takes --hr-interval)
TYPES = [
    ("HKQuantityTypeIdentifierHeartRate", "count/min", None),
    ("HKQuantityTypeIdentifierStepCount", "count", 300),
    ("HKQuantityTypeIdentifierActiveEnergyBurned", "kcal", 300),
    ("HKQuantityTypeIdentifierOxygenSaturation", "%", 3600),
]


def values(rng, hk_type, n):
    """A plausible series of n values for hk_type."""
    if hk_type.endswith("HeartRate"):
        v, out = 70.0, []
        for _ in range(n):
            v = min(170.0, max(48.0, v + rng.gauss(0, 2.5)))
            out.append(round(v, 1))
        return out
    if hk_type.endswith("StepCount"):
        return [float(rng.choice([0, 0, 0, rng.randint(10, 600)])) for _ in range(n)]
    if hk_type.endswith("ActiveEnergyBurned"):
        return [round(rng.uniform(0.5, 25.0), 3) for _ in range(n)]
    return [round(rng.uniform(0.94, 0.99), 2) for _ in range(n)]


def iso_utc(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--days", type=int, default=14)
    ap.add_argument("--start", default="2026-01-05", help="first local day, YYYY-MM-DD")
    ap.add_argument("--hr-interval", type=int, default=60, help="seconds between heart rate samples")
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    rng = random.Random(args.seed)
    uid = uuid.UUID(int=rng.getrandbits(128))  # deterministic record ids
    (args.out / "ohs").mkdir(parents=True, exist_ok=True)
    (args.out / "ow").mkdir(parents=True, exist_ok=True)

    start = datetime.strptime(args.start, "%Y-%m-%d").replace(tzinfo=TZ)
    requests, total, counter = [], 0, 0
    for d in range(args.days):
        day0 = start + timedelta(days=d)
        day = day0.strftime("%Y-%m-%d")
        for part, (hk_type, unit, interval) in enumerate(TYPES):
            step = interval or args.hr_interval
            n = 86400 // step
            vals = values(rng, hk_type, n)
            ohs_lines, ow_records = [], []
            for i, v in enumerate(vals):
                t0 = day0 + timedelta(seconds=i * step)
                t1 = t0 if hk_type.endswith(("HeartRate", "OxygenSaturation")) else t0 + timedelta(seconds=step)
                counter += 1
                rid = str(uuid.UUID(int=(uid.int + counter) % (1 << 128)))
                ohs_lines.append({
                    "quantityType": hk_type, "startDate": iso_utc(t0), "endDate": iso_utc(t1),
                    "value": v, "unit": unit, "uuid": rid,
                    "sourceName": SOURCE["name"], "sourceBundleId": SOURCE["bundleIdentifier"],
                    "device": SOURCE["name"], "deviceModel": SOURCE["deviceModel"],
                    "productType": SOURCE["productType"], "wasUserEntered": False,
                    "metadata": {"HKTimeZone": TZ_NAME},
                })
                ow_records.append({
                    "id": rid, "type": hk_type, "startDate": iso_utc(t0), "endDate": iso_utc(t1),
                    "zoneOffset": ZONE_OFFSET, "source": SOURCE, "value": v, "unit": unit,
                    "metadata": {"HKTimeZone": TZ_NAME},
                })
            name = f"{day}_{part}"
            body = "".join(json.dumps(line, separators=(",", ":")) + "\n" for line in ohs_lines).encode()
            # mtime=0 keeps the gzip bytes identical across runs.
            (args.out / "ohs" / f"{name}.ndjson.gz").write_bytes(gzip.compress(body, mtime=0))
            ow_body = {
                "provider": "apple", "sdkVersion": "bench", "syncTimestamp": iso_utc(day0 + timedelta(days=1)),
                "data": {"records": ow_records, "sleep": [], "workouts": []},
            }
            (args.out / "ow" / f"{name}.json").write_text(json.dumps(ow_body, separators=(",", ":")))
            requests.append({"name": name, "day": day, "part": part, "type": hk_type, "records": n})
            total += n

    manifest = {
        "seed": args.seed, "days": args.days, "start": args.start, "hr_interval": args.hr_interval,
        "requests": requests, "records": total,
    }
    (args.out / "manifest.json").write_text(json.dumps(manifest, indent=1))
    print(f"{len(requests)} requests, {total} records -> {args.out}")


if __name__ == "__main__":
    main()
