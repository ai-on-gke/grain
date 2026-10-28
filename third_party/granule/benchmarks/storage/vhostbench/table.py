#!/usr/bin/env python3
"""Turns sweep.sh JSON lines into the markdown tables in
benchmarks/storage/results/vhost-user-random-read.md.

    table.py NAME=results.jsonl [NAME=results.jsonl ...]
"""
import json
import sys


def load(path):
    rows = {}
    for line in open(path):
        j = json.loads(line)
        _, cache, bs, jobs, qd = j["label"].split()
        r = j["result"]
        rows[(cache, bs, jobs, qd)] = r
    return rows


def main():
    sets = []
    for arg in sys.argv[1:]:
        name, path = arg.split("=", 1)
        sets.append((name, load(path)))
    keys = list(sets[0][1].keys())
    for cache in ("cold", "warm"):
        print(f"\n**{cache} page cache**\n")
        print("| bs | jobs x QD | " + " | ".join(f"{n} IOPS" for n, _ in sets)
              + " | " + " | ".join(f"{n} p50 / p99 (µs)" for n, _ in sets)
              + " | " + " | ".join(f"{n} CPU %" for n, _ in sets) + " |")
        print("|" + "---|" * (2 + 3 * len(sets)))
        for k in keys:
            if k[0] != cache:
                continue
            _, bs, jobs, qd = k
            bs = bs.split("=")[1]
            bs = f"{int(bs) // 1024}k"
            cells = [bs, f"{jobs.split('=')[1]} x {qd.split('=')[1]}"]
            for _, rows in sets:
                r = rows.get(k)
                cells.append(f"{r['iops']:,.0f}" if r else "–")
            for _, rows in sets:
                r = rows.get(k)
                cells.append(f"{r['p50_us']:,.0f} / {r['p99_us']:,.0f}" if r else "–")
            for _, rows in sets:
                r = rows.get(k)
                cpu = r.get("server_cpu_pct", r.get("host_cpu_pct")) if r else None
                cells.append(f"{cpu:.0f}" if cpu is not None else "–")
            print("| " + " | ".join(cells) + " |")


if __name__ == "__main__":
    main()
