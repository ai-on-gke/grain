#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Turn tunnel-vs-direct.sh's results/*.jsonl into the report's markdown tables.

    scripts/bench/summarize.py /var/tmp/bench/results > tables.md
"""
import collections
import json
import pathlib
import statistics
import sys

CARRIERS = ["direct", "docker-exec", "kubectl-exec", "kubectl-port-forward"]


def main(results_dir):
    rows = []
    for path in sorted(pathlib.Path(results_dir).glob("*.jsonl")):
        for line in path.read_text().splitlines():
            if line.strip():
                rows.append(json.loads(line))
    carriers = [c for c in CARRIERS if any(r["carrier"] == c for r in rows)]
    by = collections.defaultdict(list)
    for r in rows:
        by[(r["workload"], r.get("conc"), r["carrier"])].append(r)

    def table(title, workload, concs, cols):
        print(f"\n### {title}\n")
        print("| carrier | conc | " + " | ".join(h for h, _ in cols) + " |")
        print("|---|---:|" + "---:|" * len(cols))
        for conc in concs:
            for c in carriers:
                rs = by.get((workload, conc, c))
                if not rs:
                    continue
                r = rs[-1]
                print(f"| {c} | {conc} | " + " | ".join(fmt(f(r)) for _, f in cols) + " |")

    lat = [("p50 ms", lambda r: r["p50_ms"]), ("p95 ms", lambda r: r["p95_ms"]),
           ("p99 ms", lambda r: r["p99_ms"]), ("calls/s", lambda r: r["ops_per_s"]),
           ("errors", lambda r: r["errors"]),
           ("sandbox CPU ms/call", lambda r: 1000 * r["sandbox_cpu_s"] / max(r["ops"], 1)),
           ("controller CPU ms/call", lambda r: 1000 * r["controller_cpu_s"] / max(r["ops"], 1))]
    table("MCP-sized JSON-RPC round trip", "rpc", [1, 8, 32], lat)
    table("Model proxy: streamed 2,000-token completion (stub upstream)", "sse", [1, 8, 32],
          [("first token p50 ms", lambda r: r["ttfb_p50_ms"]), ("whole stream p50 ms", lambda r: r["p50_ms"]),
           ("p95 ms", lambda r: r["p95_ms"]), ("errors", lambda r: r["errors"]),
           ("sandbox CPU s", lambda r: r["sandbox_cpu_s"]), ("controller CPU s", lambda r: r["controller_cpu_s"])])
    bulk = [("wall s", lambda r: r["wall_s"]), ("MiB/s", lambda r: r["mib_per_s"]),
            ("errors", lambda r: r["errors"]),
            ("sandbox CPU s/GiB", lambda r: r["sandbox_cpu_s"] * (1 << 30) / max(r["bytes"], 1)),
            ("controller CPU s/GiB", lambda r: r["controller_cpu_s"] * (1 << 30) / max(r["bytes"], 1))]
    table("Bulk: 1 GiB single stream (conc 1) and 16 x 64 MiB (conc 16)", "bulk", [1, 16], bulk)
    table("Package cache replay: npm ci of ui/ (378 tarballs, conc 16) and apt-get install postgresql-15 (36 debs, conc 1)",
          "replay", [16, 1], bulk[:3] + [("p50 ms/object", lambda r: r["p50_ms"]), ("p95 ms/object", lambda r: r["p95_ms"])] + bulk[3:])

    print("\n### Real tools: git through the git endpoint, go mod download through the package cache (median of 3)\n")
    tools = ["git-clone-shallow", "git-clone-full", "git-fetch-100", "go-mod-download"]
    print("| workload | " + " | ".join(carriers) + " |")
    print("|---|" + "---:|" * len(carriers))
    for w in tools:
        cells = []
        for c in carriers:
            rs = by.get((w, None, c))
            if not rs:
                cells.append("-")
                continue
            cells.append("%.2f s (sbx %.1f / ctl %.1f CPU s)" % (
                statistics.median(r["wall_s"] for r in rs),
                statistics.median(r["sandbox_cpu_s"] for r in rs),
                statistics.median(r["controller_cpu_s"] for r in rs)))
        print(f"| {w} | " + " | ".join(cells) + " |")


def fmt(v):
    if isinstance(v, float):
        return "%.3g" % v if abs(v) < 100 else "%.0f" % v
    return str(v)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "/var/tmp/bench/results")
