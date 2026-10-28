#!/usr/bin/env python3
"""
Storage benchmark: Granule's extent storage backends vs flat disk.img, QCOW2
and the native host.

Backends compared (all on the same host, see benchmarks/storage/gce-bench.sh
for the dedicated GCE VM this is meant to run on):

  host                   fio directly on the host filesystem
  flat                   CHV_DISK_MODE=persistent, raw disk.img read-write
  qcow_64k               CHV_DISK_MODE=overlay, QCOW2 overlay, 64 KiB clusters
                         (made with qemu-img and bind-mounted over the
                         overlay path, since granule itself only writes 2 MiB)
  qcow_2m                CHV_DISK_MODE=overlay, granule's own 2 MiB QCOW2 overlay
  extent_boot_64         pkg/extent directory (granule guest split, 64 MiB),
                         assembled into a flat overlay by PrepareOverlay
  extents_persistent_N   CHV_STORAGE_BACKEND=extents, CHV_DISK_MODE=persistent,
                         N MiB extents served over vhost-user-blk
  extents_overlay_N      CHV_STORAGE_BACKEND=extents, CHV_DISK_MODE=overlay
                         (lazy O_RDONLY -> O_RDWR promotion per extent)

For every VM backend it measures:

  1. the fio suite (direct=1, libaio, numjobs=4, 4 GiB dataset as four 1 GiB
     files, CHV_VCPUS=4, CHV_DISK_NUM_QUEUES=4): sequential write/read
     (bs=1M), random read/write 4K (iodepth=32) and mixed 70/30 4K
     (iodepth=16); bandwidth, IOPS, p50/p95/p99 completion latency, CPU;
  2. cold start (docker run -> granule ready, host caches dropped) and warm
     start (docker start of the same stopped container -> granule ready);
  3. the OCI layer a small guest change costs: 10 MiB written in the guest,
     the container stopped, then docker commit timed and the committed
     layer's size taken from docker.

--live runs all of that and writes the parsed results to --out-dir (and
prints them between STORAGE-BENCH-*-BEGIN/END markers, which is how
gce-bench.sh's CI log is harvested). Without --live it renders the report
from a summary JSON written earlier (benchmarks/results/storage by default).
"""

import argparse
import json
import os
import subprocess
import sys
import time

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
RESULTS_DIR = os.path.join(SCRIPT_DIR, "..", "results", "storage")
SUMMARY_NAME = "summary_storage_benchmark.json"

GUEST_FIO_DIR = "/var/tmp/fio"
HOST_FIO_DIR = "/var/tmp/bench_host"

# Display order and labels.
VARIANT_LABELS = {
    "host": "Native Host",
    "flat": "Flat raw disk.img (persistent)",
    "qcow_64k": "QCOW2 overlay, 64 KiB clusters",
    "qcow_2m": "QCOW2 overlay, 2 MiB clusters",
    "extent_boot_64": "Boot-assembled extents, 64 MiB (pkg/extent)",
    "extents_persistent_16": "vhost-user extents, persistent, 16 MiB",
    "extents_persistent_64": "vhost-user extents, persistent, 64 MiB",
    "extents_overlay_16": "vhost-user extents, overlay, 16 MiB",
    "extents_overlay_64": "vhost-user extents, overlay, 64 MiB",
}

# Variants whose committed OCI layer is measured. The others share a disk
# layout with one of these (qcow_64k's overlay is a bind mount, so docker
# never sees it at all).
OCI_VARIANTS = ["flat", "qcow_2m", "extent_boot_64", "extents_persistent_16",
                "extents_persistent_64", "extents_overlay_16", "extents_overlay_64"]


def log(msg):
    print(f"[bench {time.strftime('%H:%M:%S')}] {msg}", flush=True)


def run(cmd, check=True, quiet=False):
    if not quiet:
        log(f"$ {cmd}")
    res = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if check and res.returncode != 0:
        raise RuntimeError(f"{cmd!r} exited {res.returncode}: {res.stderr.strip()[-2000:]}")
    return res.stdout


def fio_tests(numjobs, size_per_job, runtime):
    common = (f"--name=bench --directory={{dir}} --size={size_per_job} --numjobs={numjobs} "
              f"--group_reporting --ioengine=libaio --direct=1 --runtime={runtime} --output-format=json")
    return [
        ("seq_write", "Sequential Write 1M", "--rw=write --bs=1M --iodepth=1"),
        ("seq_read", "Sequential Read 1M", "--rw=read --bs=1M --iodepth=1"),
        ("rand_read", "Random Read 4K (iodepth=32)", "--rw=randread --bs=4k --iodepth=32"),
        ("rand_write", "Random Write 4K (iodepth=32)", "--rw=randwrite --bs=4k --iodepth=32"),
        ("mixed_rw", "Mixed Random 70/30 4K (iodepth=16)", "--rw=randrw --rwmixread=70 --bs=4k --iodepth=16"),
    ], common


def parse_fio(data):
    job = data["jobs"][0]
    out = {}
    for side in ("read", "write"):
        s = job[side]
        pct = (s.get("clat_ns") or {}).get("percentile") or {}
        out[side] = {
            "bw_mbs": round(s["bw_bytes"] / (1024 * 1024), 1),
            "iops": round(s["iops"], 0),
            "mean_us": round(((s.get("lat_ns") or {}).get("mean", 0)) / 1000.0, 1),
            "p50_us": round(pct.get("50.000000", 0) / 1000.0, 1),
            "p95_us": round(pct.get("95.000000", 0) / 1000.0, 1),
            "p99_us": round(pct.get("99.000000", 0) / 1000.0, 1),
        }
    out["usr_cpu"] = round(job.get("usr_cpu", 0.0), 1)
    out["sys_cpu"] = round(job.get("sys_cpu", 0.0), 1)
    return out


def fio_json(text):
    # fio can print warnings ahead of its JSON document.
    i = text.find("{")
    if i < 0:
        raise ValueError(f"no JSON in fio output: {text[:500]!r}")
    return json.loads(text[i:])


def drop_host_caches():
    run("sync && echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null", quiet=True)


def gexec(container, cmd, check=True):
    return run(f"sudo docker exec {container} granule exec -- sh -c {sh_quote(cmd)}", check=check, quiet=True)


def sh_quote(s):
    return "'" + s.replace("'", "'\\''") + "'"


class Bench:
    def __init__(self, args):
        self.args = args
        self.tests, self.fio_common = fio_tests(args.numjobs, args.size_per_job, args.runtime)
        self.summary = {"meta": {}, "fio": {}, "startup": {}, "oci": {}, "errors": {}}

    # -- containers ---------------------------------------------------------

    def spec(self, variant):
        a = self.args
        env = {"CHV_VCPUS": "4", "CHV_DISK_NUM_QUEUES": "4"}
        vols = []
        image = a.image_flat
        if variant == "flat":
            env["CHV_DISK_MODE"] = "persistent"
        elif variant == "qcow_2m":
            env["CHV_DISK_MODE"] = "overlay"
        elif variant == "qcow_64k":
            env["CHV_DISK_MODE"] = "overlay"
            vols.append(f"{a.qcow64k_overlay}:/var/lib/granule/overlay/disk.qcow2")
        elif variant == "extent_boot_64":
            env["CHV_DISK_MODE"] = "overlay"
            image = a.image_extent_boot
        elif variant.startswith("extents_"):
            _, mode, size = variant.split("_")
            env["CHV_DISK_MODE"] = mode
            env["CHV_STORAGE_BACKEND"] = "extents"
            env["CHV_EXTENT_SIZE_MB"] = size
            image = a.image_extents_16 if size == "16" else a.image_extents_64
        else:
            raise ValueError(variant)
        return image, env, vols

    def fresh_qcow64k(self):
        a = self.args
        run(f"rm -f {a.qcow64k_overlay} && qemu-img create -q -f qcow2 -u -o cluster_size=65536 "
            f"-b /var/lib/granule/guest/disk.img -F raw {a.qcow64k_overlay} {a.disk_bytes}")
        run(f"chmod 666 {a.qcow64k_overlay}")

    def start(self, variant, name):
        image, env, vols = self.spec(variant)
        if variant == "qcow_64k":
            self.fresh_qcow64k()
        run(f"sudo docker rm -f {name} >/dev/null 2>&1 || true", quiet=True)
        envs = " ".join(f"-e {k}={v}" for k, v in env.items())
        mounts = " ".join(f"-v {v}" for v in vols)
        drop_host_caches()
        t0 = time.monotonic()
        run(f"sudo docker run -d --name {name} --privileged {envs} {mounts} {image}")
        self.ready(name)
        return (time.monotonic() - t0) * 1000.0

    def ready(self, name):
        try:
            run(f"sudo docker exec {name} granule ready -timeout {self.args.ready_timeout}s", quiet=True)
        except RuntimeError:
            print(run(f"sudo docker logs --tail 60 {name} 2>&1", check=False), flush=True)
            raise

    def remove(self, name):
        run(f"sudo docker rm -f {name} >/dev/null 2>&1 || true", quiet=True)

    # -- workloads ----------------------------------------------------------

    def fio_host(self):
        res = {}
        run(f"sudo rm -rf {HOST_FIO_DIR} && sudo mkdir -p {HOST_FIO_DIR}")
        for tid, title, extra in self.tests:
            drop_host_caches()
            out = run(f"sudo fio {self.fio_common.format(dir=HOST_FIO_DIR)} {extra}", quiet=True)
            res[tid] = parse_fio(fio_json(out))
            log(f"host {tid}: {res[tid]}")
        run(f"sudo rm -rf {HOST_FIO_DIR}")
        return res

    def fio_guest(self, variant):
        name = f"bench-{variant.replace('_', '-')}"
        res = {}
        try:
            ms = self.start(variant, name)
            self.summary["startup"].setdefault(variant, {})["cold_ms_2"] = round(ms)
            df = gexec(name, f"mkdir -p {GUEST_FIO_DIR} && df -h {GUEST_FIO_DIR} | tail -1", check=False).strip()
            log(f"{variant}: guest {GUEST_FIO_DIR} on: {df}")
            self.summary["meta"].setdefault("guest_df", {})[variant] = df
            for tid, title, extra in self.tests:
                drop_host_caches()
                gexec(name, "sync; echo 3 > /proc/sys/vm/drop_caches", check=False)
                out = gexec(name, f"fio {self.fio_common.format(dir=GUEST_FIO_DIR)} {extra} --output=/tmp/fio.json >/dev/null 2>&1; cat /tmp/fio.json")
                res[tid] = parse_fio(fio_json(out))
                log(f"{variant} {tid}: {res[tid]}")
        finally:
            self.remove(name)
        return res

    def oci_and_startup(self, variant):
        a = self.args
        name = f"oci-{variant.replace('_', '-')}"
        tag = f"granule-bench-commit:{variant.replace('_', '-')}"
        image, _, _ = self.spec(variant)
        st = self.summary["startup"].setdefault(variant, {})
        try:
            st["cold_ms"] = round(self.start(variant, name))
            log(f"{variant}: cold start {st['cold_ms']} ms")
            if variant not in OCI_VARIANTS:
                return
            gexec(name, f"dd if=/dev/urandom of=/root/bench-delta-1.bin bs=1M count={a.delta_mb // 2} conv=fsync 2>/dev/null && "
                        f"dd if=/dev/urandom of=/root/bench-delta-2.bin bs=1M count={a.delta_mb - a.delta_mb // 2} conv=fsync 2>/dev/null && sync")
            run(f"sudo docker stop -t 60 {name}")
            diff = run(f"sudo docker diff {name}", quiet=True).splitlines()
            changed = [l for l in diff if "/var/lib/granule" in l and not l.rstrip().endswith(("/var/lib/granule", "/guest", "/overlay"))]
            t0 = time.monotonic()
            run(f"sudo docker commit {name} {tag}")
            commit_ms = (time.monotonic() - t0) * 1000.0
            base = int(run(f"sudo docker image inspect -f '{{{{.Size}}}}' {image}", quiet=True).strip())
            new = int(run(f"sudo docker image inspect -f '{{{{.Size}}}}' {tag}", quiet=True).strip())
            self.summary["oci"][variant] = {
                "guest_write_mib": a.delta_mb,
                "layer_mib": round((new - base) / (1024 * 1024), 1),
                "commit_ms": round(commit_ms),
                "changed_files": len(changed),
                "changed_sample": changed[:12],
            }
            log(f"{variant}: oci {self.summary['oci'][variant]}")
            run(f"sudo docker rmi -f {tag} >/dev/null", check=False, quiet=True)
            # Warm start: the same container, its disk already prepared.
            drop_host_caches()
            t0 = time.monotonic()
            run(f"sudo docker start {name}")
            self.ready(name)
            st["warm_ms"] = round((time.monotonic() - t0) * 1000.0)
            log(f"{variant}: warm start {st['warm_ms']} ms")
        finally:
            self.remove(name)

    def run_all(self):
        a = self.args
        self.summary["meta"].update({
            "numjobs": a.numjobs, "size_per_job": a.size_per_job, "runtime_s": a.runtime,
            "vcpus": 4, "disk_num_queues": 4, "disk_bytes": a.disk_bytes,
            "host": run("uname -srm; nproc; free -g | awk '/Mem:/{print $2\"G\"}'; df -h / | tail -1", check=False, quiet=True).split("\n"),
        })
        for variant in a.variants:
            log(f"===== {variant}: {VARIANT_LABELS.get(variant, variant)} =====")
            try:
                if variant == "host":
                    self.summary["fio"]["host"] = self.fio_host()
                else:
                    self.oci_and_startup(variant)
                    self.summary["fio"][variant] = self.fio_guest(variant)
            except Exception as e:  # keep going: one broken backend should not cost the rest
                log(f"{variant} FAILED: {e}")
                self.summary["errors"][variant] = str(e)[-1500:]
            print("STORAGE-BENCH-PARTIAL " + json.dumps({variant: {k: self.summary[k].get(variant) for k in ("fio", "startup", "oci", "errors")}}), flush=True)
        return self.summary


# -- report -----------------------------------------------------------------

def fmt(v, unit=""):
    if v is None:
        return "–"
    if isinstance(v, float) and v < 100:
        return f"{v:,.1f}{unit}"
    return f"{v:,.0f}{unit}"


def markdown(summary):
    fio = summary.get("fio", {})
    variants = [v for v in VARIANT_LABELS if v in fio and fio[v]]
    lines = []
    tests = [("seq_write", "Seq Write 1M", "write", "bw_mbs", "MB/s"),
             ("seq_read", "Seq Read 1M", "read", "bw_mbs", "MB/s"),
             ("rand_read", "Rand Read 4K", "read", "iops", "IOPS"),
             ("rand_write", "Rand Write 4K", "write", "iops", "IOPS"),
             ("mixed_rw", "Mixed 70/30 4K", None, "iops", "IOPS")]

    lines.append("### Throughput and IOPS")
    lines.append("")
    lines.append("| Backend | " + " | ".join(f"{t[1]} ({t[4]})" for t in tests) + " |")
    lines.append("|:---|" + "---:|" * len(tests))
    for v in variants:
        cells = []
        for tid, _, side, metric, _ in tests:
            r = fio[v].get(tid)
            if not r:
                cells.append("–")
            elif side is None:
                cells.append(f"{fmt(r['read'][metric] + r['write'][metric])} ({fmt(r['read'][metric])} r / {fmt(r['write'][metric])} w)")
            else:
                cells.append(fmt(r[side][metric]))
        lines.append(f"| {VARIANT_LABELS[v]} | " + " | ".join(cells) + " |")

    lines.append("")
    lines.append("### Completion latency p50 / p95 / p99 (µs) and guest CPU (usr / sys %)")
    lines.append("")
    lines.append("| Workload | Backend | p50 | p95 | p99 | usr / sys |")
    lines.append("|:---|:---|---:|---:|---:|---:|")
    for tid, title, side, _, _ in tests:
        first = True
        for v in variants:
            r = fio[v].get(tid)
            if not r:
                continue
            sides = [side] if side else ["read", "write"]
            p = lambda k: " / ".join(fmt(r[s][k]) for s in sides)
            lines.append(f"| {'**' + title + '**' if first else ''} | {VARIANT_LABELS[v]} | {p('p50_us')} | {p('p95_us')} | {p('p99_us')} | {fmt(r['usr_cpu'])} / {fmt(r['sys_cpu'])} |")
            first = False

    st = summary.get("startup", {})
    if st:
        lines.append("")
        lines.append("### Start latency (`docker run` / `docker start` -> `granule ready`, ms)")
        lines.append("")
        lines.append("| Backend | Cold start | Cold start (2nd sample) | Warm restart (disk already prepared) |")
        lines.append("|:---|---:|---:|---:|")
        for v in VARIANT_LABELS:
            if v in st:
                s = st[v]
                lines.append(f"| {VARIANT_LABELS[v]} | {fmt(s.get('cold_ms'))} | {fmt(s.get('cold_ms_2'))} | {fmt(s.get('warm_ms'))} |")

    oci = summary.get("oci", {})
    if oci:
        lines.append("")
        lines.append("### OCI layer cost of a 10 MiB guest change (`docker commit`)")
        lines.append("")
        lines.append("| Backend | Guest write (MiB) | Committed layer (MiB) | `docker commit` (ms) | Files under /var/lib/granule in `docker diff` |")
        lines.append("|:---|---:|---:|---:|---:|")
        for v in VARIANT_LABELS:
            if v in oci:
                o = oci[v]
                lines.append(f"| {VARIANT_LABELS[v]} | {o['guest_write_mib']} | {fmt(o['layer_mib'])} | {fmt(o['commit_ms'])} | {o['changed_files']} |")

    if summary.get("errors"):
        lines.append("")
        lines.append("### Backends that failed")
        lines.append("")
        for v, e in summary["errors"].items():
            lines.append(f"- `{v}`: {e.splitlines()[-1] if e else ''}")
    return "\n".join(lines)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--live", action="store_true", help="run the benchmarks (default: render the report from --summary)")
    p.add_argument("--dry-run", action="store_true", help="print the plan and exit")
    p.add_argument("--variants", default=",".join(VARIANT_LABELS), help="comma-separated backends to run")
    p.add_argument("--numjobs", type=int, default=4)
    p.add_argument("--size-per-job", default="1G", help="fio file size per job (4 jobs x 1G = 4 GiB dataset)")
    p.add_argument("--runtime", type=int, default=15, help="fio --runtime cap per workload in seconds")
    p.add_argument("--delta-mb", type=int, default=10, help="MiB the OCI-diff test writes in the guest")
    p.add_argument("--ready-timeout", type=int, default=600)
    p.add_argument("--image-flat", default="granule-bench:flat")
    p.add_argument("--image-extent-boot", default="granule-bench:extent-boot-64")
    p.add_argument("--image-extents-16", default="granule-bench:extents-16")
    p.add_argument("--image-extents-64", default="granule-bench:extents-64")
    p.add_argument("--disk-bytes", type=int, default=0, help="virtual size of the guest disk.img (for the 64 KiB qcow2 overlay)")
    p.add_argument("--qcow64k-overlay", default="/var/tmp/bench-qcow64k/disk.qcow2")
    p.add_argument("--out-dir", default=RESULTS_DIR)
    p.add_argument("--summary", default=os.path.join(RESULTS_DIR, SUMMARY_NAME))
    args = p.parse_args()
    args.variants = [v for v in args.variants.split(",") if v]

    if args.dry_run:
        tests, common = fio_tests(args.numjobs, args.size_per_job, args.runtime)
        print("backends: " + ", ".join(args.variants))
        for tid, title, extra in tests:
            print(f"  fio {tid}: {common.format(dir='<dir>')} {extra}")
        return

    if not args.live:
        with open(args.summary) as f:
            print(markdown(json.load(f)))
        return

    if "qcow_64k" in args.variants:
        if args.disk_bytes <= 0:
            sys.exit("--disk-bytes is required for qcow_64k")
        os.makedirs(os.path.dirname(args.qcow64k_overlay), exist_ok=True)
    summary = Bench(args).run_all()
    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, SUMMARY_NAME), "w") as f:
        json.dump(summary, f, indent=2)
    print("===== STORAGE-BENCH-JSON-BEGIN =====")
    print(json.dumps(summary, separators=(",", ":")))
    print("===== STORAGE-BENCH-JSON-END =====")
    print("===== STORAGE-BENCH-MARKDOWN-BEGIN =====")
    print(markdown(summary))
    print("===== STORAGE-BENCH-MARKDOWN-END =====", flush=True)


if __name__ == "__main__":
    main()
