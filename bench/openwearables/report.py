#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Print the median of each metric over the runs in a results directory, as Markdown.

    report.py RESULTS_DIR

Reads the <profile>-<system>-<run>.json files bench.py wrote.
"""

import json
import statistics
import sys
from collections import defaultdict
from pathlib import Path

SYSTEMS = [("ohs", "Open Health Sync"), ("ow", "Open Wearables")]
MB = 1024 * 1024


def metrics(r):
    ing, mem, cpu = r["ingest"], r["memory_bytes"], r["cpu_seconds"]
    app_cpu = sum(v for k, v in cpu.items() if k != "postgres")
    per10k = 10000 / r["dataset"]["records"]
    return {
        "End to end, upload to stored (s)": ing["end_to_end_seconds"],
        "End to end throughput (records/s)": ing["end_to_end_records_per_s"],
        "Upload phase alone (records/s)": ing["upload_records_per_s"],
        "Upload latency p50 (ms)": ing["upload_latency_ms"]["p50"],
        "Upload latency p95 (ms)": ing["upload_latency_ms"]["p95"],
        "Upload latency p99 (ms)": ing["upload_latency_ms"]["p99"],
        "Bytes sent (MB)": r["dataset"]["request_bytes"] / MB,
        "Idle memory, RSS (MB)": mem["idle"]["total"] / MB,
        "Peak memory, RSS (MB)": mem["peak"]["total"] / MB,
        "App CPU per 10k records (s)": app_cpu * per10k,
        "Postgres CPU per 10k records (s)": cpu["postgres"] * per10k,
        "Read one day of heart rate p50 (ms)": r["read"]["latency_ms"]["p50"],
        "Read one day of heart rate p95 (ms)": r["read"]["latency_ms"]["p95"],
        "Database size (MB)": r["storage_bytes"]["database"] / MB,
        "Raw payloads kept (MB)": r["storage_bytes"]["raw_files"] / MB,
        "Records stored, per run (of sent)": f"{ing['stored']:,} ({ing['expected']:,})",
    }


def fmt(v):
    if isinstance(v, str):
        return v
    if v >= 1000:
        return f"{v:,.0f}"
    if v >= 10:
        return f"{v:.1f}"
    return f"{v:.2f}"


def main():
    runs = defaultdict(list)
    for f in sorted(Path(sys.argv[1]).glob("*-*-*.json")):
        profile, system, _ = f.stem.rsplit("-", 2)
        runs[(profile, system)].append(json.loads(f.read_text()))
    for profile in sorted({p for p, _ in runs}, key=lambda p: (p != "baseline", p)):
        rs = {s: runs[(profile, s)] for s, _ in SYSTEMS if runs.get((profile, s))}
        any_r = next(iter(rs.values()))[0]
        d = any_r["dataset"]
        print(f"### {profile}: {d['records']:,} records in {d['requests']} requests "
              f"({d['days']} days, heart rate every {d['hr_interval']} s), "
              f"{any_r['concurrency']} clients, median of {min(len(v) for v in rs.values())} runs\n")
        print("| Metric | " + " | ".join(n for s, n in SYSTEMS if s in rs) + " |")
        print("|---|" + "---:|" * len(rs))
        names = list(metrics(any_r))
        for name in names:
            cells = []
            for s, _ in SYSTEMS:
                if s not in rs:
                    continue
                vals = [metrics(r)[name] for r in rs[s]]
                # Text metrics are listed run by run; numbers are the median.
                cells.append("<br>".join(vals) if isinstance(vals[0], str) else fmt(statistics.median(vals)))
            print(f"| {name} | " + " | ".join(cells) + " |")
        print()


if __name__ == "__main__":
    main()
