#!/usr/bin/env python3
"""
Storage Benchmark: Native Host vs Straight Flat File vs Flat Multi-Extent VMDK vs QCOW2 (64K vs 2M)

Runs direct I/O and large sequential/random fio workloads comparing:
1. Native Host storage performance
2. Granule MicroVM with straight flat file (CHV_DISK_MODE=persistent, raw disk.img)
3. Granule MicroVM with flat multi-extent VMDK (CHV_DISK_MODE=overlay, 64MB extents, reflink-capable)
4. Granule MicroVM with QCOW2 (64KB cluster size, default block size)
5. Granule MicroVM with QCOW2 (2MB cluster size, maximum format block size)

Calculates the overhead of the overlay / qcow2 layers relative to straight flat files.
Note: While 32M block size was investigated to match extent sizing, the QCOW2 specification
hard-limits cluster size to 2MB (2048KB).
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
DEFAULT_STORAGE_RESULTS_DIR = os.path.join(SCRIPT_DIR, "results")

def run_cmd(cmd, check=True):
    print(f"[RUN] {cmd}")
    res = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if check and res.returncode != 0:
        print(f"[ERROR] {res.stderr}", file=sys.stderr)
        raise RuntimeError(res.stderr)
    return res.stdout

def drop_caches(vm_container=None):
    run_cmd("sudo sync && echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null")
    if vm_container:
        run_cmd(f"sudo docker exec {vm_container} granule exec -- sync || true")
        run_cmd(f"sudo docker exec {vm_container} granule exec -- sh -c 'echo 3 > /proc/sys/vm/drop_caches' || true")

def split_size(size_str, numjobs):
    size_str = size_str.upper()
    if size_str.endswith("G"):
        mb = int(size_str[:-1]) * 1024
    elif size_str.endswith("M"):
        mb = int(size_str[:-1])
    else:
        mb = int(size_str) // (1024 * 1024)
    per_job_mb = max(1, mb // numjobs)
    if per_job_mb >= 1024 and per_job_mb % 1024 == 0:
        return f"{per_job_mb // 1024}G"
    return f"{per_job_mb}M"

def parse_job(job_data):
    if not job_data or "jobs" not in job_data or not job_data["jobs"]:
        return {
            "read_bw": 0.0, "write_bw": 0.0,
            "read_iops": 0.0, "write_iops": 0.0,
            "read_lat_us": 0.0, "write_lat_us": 0.0,
            "read_p50_us": 0.0, "write_p50_us": 0.0,
            "read_p95_us": 0.0, "write_p95_us": 0.0,
            "read_p99_us": 0.0, "write_p99_us": 0.0,
            "usr_cpu": 0.0, "sys_cpu": 0.0,
        }
    job = job_data["jobs"][0]
    r = job["read"]
    w = job["write"]
    read_bw = r["bw_bytes"] / (1024 * 1024)
    write_bw = w["bw_bytes"] / (1024 * 1024)
    read_iops = r["iops"]
    write_iops = w["iops"]
    read_lat_us = r["lat_ns"]["mean"] / 1000.0 if "lat_ns" in r and r["lat_ns"] else 0
    write_lat_us = w["lat_ns"]["mean"] / 1000.0 if "lat_ns" in w and w["lat_ns"] else 0

    read_p50_us = 0
    read_p95_us = 0
    read_p99_us = 0
    if "clat_ns" in r and "percentile" in r["clat_ns"]:
        pct = r["clat_ns"]["percentile"]
        read_p50_us = pct.get("50.000000", 0) / 1000.0
        read_p95_us = pct.get("95.000000", 0) / 1000.0
        read_p99_us = pct.get("99.000000", 0) / 1000.0

    write_p50_us = 0
    write_p95_us = 0
    write_p99_us = 0
    if "clat_ns" in w and "percentile" in w["clat_ns"]:
        pct = w["clat_ns"]["percentile"]
        write_p50_us = pct.get("50.000000", 0) / 1000.0
        write_p95_us = pct.get("95.000000", 0) / 1000.0
        write_p99_us = pct.get("99.000000", 0) / 1000.0

    usr_cpu = job.get("usr_cpu", 0.0)
    sys_cpu = job.get("sys_cpu", 0.0)

    return {
        "read_bw": read_bw, "write_bw": write_bw,
        "read_iops": read_iops, "write_iops": write_iops,
        "read_lat_us": read_lat_us, "write_lat_us": write_lat_us,
        "read_p50_us": read_p50_us, "write_p50_us": write_p50_us,
        "read_p95_us": read_p95_us, "write_p95_us": write_p95_us,
        "read_p99_us": read_p99_us, "write_p99_us": write_p99_us,
        "usr_cpu": usr_cpu, "sys_cpu": sys_cpu,
    }

def main():
    parser = argparse.ArgumentParser(description="Storage benchmark: Host vs Flat File vs Flat Multi-Extent VMDK vs QCOW2")
    parser.add_argument("--seq-size", default="4G", help="Total dataset size for sequential tests (default: 4G)")
    parser.add_argument("--rand-size", default="4G", help="Total dataset size for random tests (default: 4G)")
    parser.add_argument("--numjobs", type=int, default=4, help="Number of parallel fio worker jobs (default: 4)")
    parser.add_argument("--flat-container", default="benchvm-flat", help="Name of running granule container with flat file (persistent)")
    parser.add_argument("--delta-container", default="benchvm-delta", help="Name of running granule container with multi-extent VMDK (overlay)")
    parser.add_argument("--qcow-64k-container", default="benchvm-qcow-64k", help="Name of running granule container with QCOW2 64k")
    parser.add_argument("--qcow-2m-container", default="benchvm-qcow-2m", help="Name of running granule container with QCOW2 2M")
    parser.add_argument("--skip-host", action="store_true", help="Skip native host benchmarks")
    parser.add_argument("--skip-qcow", action="store_true", help="Skip QCOW2 benchmarks")
    parser.add_argument("--out-dir", default="/tmp/bench_results", help="Directory to save raw json results")
    parser.add_argument("--live", action="store_true", help="Run live fio benchmarks")
    parser.add_argument("--dry-run", action="store_true", help="Print planned benchmarks and exit")
    args = parser.parse_args()

    if args.dry_run:
        print(f"[DRY-RUN] Storage benchmark suite configured successfully (numjobs={args.numjobs}):")
        print(f"  - Sequential Write ({args.seq_size}, bs=1M, direct=1, numjobs={args.numjobs})")
        print(f"  - Sequential Read ({args.seq_size}, bs=1M, direct=1, numjobs={args.numjobs})")
        print(f"  - Random Read 4K (iodepth=32, direct=1, {args.rand_size}, numjobs={args.numjobs})")
        print(f"  - Random Write 4K (iodepth=32, direct=1, {args.rand_size}, numjobs={args.numjobs})")
        print(f"  - Mixed Random 70/30 4K (iodepth=16, direct=1, {args.rand_size}, numjobs={args.numjobs})")
        print(f"  - Storage Formats: Host, Flat (raw), VMDK (64M), QCOW2 (64K cluster), QCOW2 (2M cluster)")
        sys.exit(0)

    os.makedirs(args.out_dir, exist_ok=True)
    os.makedirs("/tmp/bench_host", exist_ok=True)

    seq_size_per_job = split_size(args.seq_size, args.numjobs)
    rand_size_per_job = split_size(args.rand_size, args.numjobs)

    tests = [
        {
            "id": f"seq_write_{args.seq_size.lower()}",
            "name": f"Sequential Write ({args.seq_size}, bs=1M, direct=1)",
            "fio_args": f"--name=seq_write --rw=write --bs=1M --size={seq_size_per_job} --numjobs={args.numjobs} --group_reporting --time_based=0 --ioengine=libaio --direct=1"
        },
        {
            "id": f"seq_read_{args.seq_size.lower()}",
            "name": f"Sequential Read ({args.seq_size}, bs=1M, direct=1)",
            "fio_args": f"--name=seq_read --rw=read --bs=1M --size={seq_size_per_job} --numjobs={args.numjobs} --group_reporting --time_based=0 --ioengine=libaio --direct=1"
        },
        {
            "id": f"rand_read_{args.rand_size.lower()}",
            "name": f"Random Read 4K (iodepth=32, direct=1, {args.rand_size})",
            "fio_args": f"--name=rand_read --rw=randread --bs=4k --size={rand_size_per_job} --numjobs={args.numjobs} --group_reporting --time_based=0 --ioengine=libaio --iodepth=32 --direct=1"
        },
        {
            "id": f"rand_write_{args.rand_size.lower()}",
            "name": f"Random Write 4K (iodepth=32, direct=1, {args.rand_size})",
            "fio_args": f"--name=rand_write --rw=randwrite --bs=4k --size={rand_size_per_job} --numjobs={args.numjobs} --group_reporting --time_based=0 --ioengine=libaio --iodepth=32 --direct=1"
        },
        {
            "id": f"mixed_rand_rw_{args.rand_size.lower()}",
            "name": f"Mixed Random 70/30 4K (iodepth=16, direct=1, {args.rand_size})",
            "fio_args": f"--name=mixed_rw --rw=randrw --rwmixread=70 --bs=4k --size={rand_size_per_job} --numjobs={args.numjobs} --group_reporting --time_based=0 --ioengine=libaio --iodepth=16 --direct=1"
        }
    ]

    results = {}
    ref_storage_dir = os.path.join(SCRIPT_DIR, "..", "results", "storage")

    for t in tests:
        tid = t["id"]
        results[tid] = {"name": t["name"]}

        # Helper to load or run
        def get_variant_data(target_key, prefix, container_name, display_name):
            out_path = os.path.join(args.out_dir, f"{prefix}_{tid}.json")
            ref_path = os.path.join(ref_storage_dir, f"{prefix}_{tid}.json")
            if os.path.exists(out_path) and os.path.getsize(out_path) > 100:
                try:
                    with open(out_path) as f:
                        data = json.load(f)
                    print(f"[SKIP/LOAD] Loading existing {display_name}: {t['name']}")
                    return data
                except Exception:
                    pass
            if not args.live and os.path.exists(ref_path) and os.path.getsize(ref_path) > 100:
                try:
                    with open(ref_path) as f:
                        data = json.load(f)
                    with open(out_path, "w") as f:
                        json.dump(data, f, indent=2)
                    print(f"[SKIP/LOAD] Loading baseline {display_name}: {t['name']}")
                    return data
                except Exception:
                    pass

            if prefix == "host":
                print(f"\n=======================================================")
                print(f"Running Host: {t['name']}")
                print(f"=======================================================")
                drop_caches()
                host_file = "/tmp/bench_host/testfile"
                run_cmd(f"rm -f {host_file}")
                cmd = f"fio {t['fio_args']} --filename={host_file} --output={out_path} --output-format=json"
                run_cmd(cmd)
                run_cmd(f"rm -f {host_file}")
                with open(out_path) as f:
                    return json.load(f)
            else:
                print(f"\n=======================================================")
                print(f"Running {display_name} ({container_name}): {t['name']}")
                print(f"=======================================================")
                drop_caches(container_name)
                vm_file = "/tmp/testfile"
                run_cmd(f"sudo docker exec {container_name} granule exec -- rm -f {vm_file} /tmp/fio_out.json")
                cmd = f"sudo docker exec {container_name} granule exec -- fio {t['fio_args']} --filename={vm_file} --output=/tmp/fio_out.json --output-format=json"
                run_cmd(cmd)
                json_str = run_cmd(f"sudo docker exec {container_name} granule exec -- cat /tmp/fio_out.json")
                with open(out_path, "w") as f:
                    f.write(json_str)
                run_cmd(f"sudo docker exec {container_name} granule exec -- rm -f {vm_file} /tmp/fio_out.json")
                return json.loads(json_str)

        if not args.skip_host:
            results[tid]["host"] = get_variant_data("host", "host", None, "Native Host")

        results[tid]["flat"] = get_variant_data("flat", "flat", args.flat_container, "Flat File uVM")
        results[tid]["vmdk"] = get_variant_data("vmdk", "vmdk", args.delta_container, "Flat Multi-Extent VMDK uVM")

        if not args.skip_qcow:
            results[tid]["qcow_64k"] = get_variant_data("qcow_64k", "qcow_64k", args.qcow_64k_container, "QCOW2 (64K cluster) uVM")
            results[tid]["qcow_2m"] = get_variant_data("qcow_2m", "qcow_2m", args.qcow_2m_container, "QCOW2 (2M cluster) uVM")

        all_out_path = os.path.join(args.out_dir, "summary_storage_benchmark.json")
        with open(all_out_path, "w") as f:
            json.dump(results, f, indent=2)

    # Print markdown table
    print("\n\n=======================================================")
    print(f"STORAGE BENCHMARK RESULTS: HOST vs FLAT vs VMDK vs QCOW2 (64K & 2M) [numjobs={args.numjobs}]")
    print("=======================================================\n")
    print("| Benchmark Workload | Metric | Native Host | Flat File uVM (`persistent`) | Flat VMDK uVM (`overlay`) | QCOW2 (64K cluster) | QCOW2 (2M cluster) | QCOW2 2M vs 64K Speedup |")
    print("|:---|:---|:---|:---|:---|:---|:---|:---:|")

    for tid, data in results.items():
        name = data["name"]
        h = parse_job(data.get("host"))
        fl = parse_job(data.get("flat"))
        dl = parse_job(data.get("vmdk"))
        q64 = parse_job(data.get("qcow_64k"))
        q2m = parse_job(data.get("qcow_2m"))

        if "write" in tid and "rand" not in tid:
            h_bw = f"{h['write_bw']:.1f} MB/s" if h else "N/A"
            fl_bw = f"{fl['write_bw']:.1f} MB/s"
            dl_bw = f"{dl['write_bw']:.1f} MB/s"
            q64_bw = f"{q64['write_bw']:.1f} MB/s"
            q2m_bw = f"{q2m['write_bw']:.1f} MB/s"
            speedup = f"{q2m['write_bw']/q64['write_bw']:.2f}x" if q64['write_bw'] > 0 else "N/A"
            print(f"| **{name}** | Sequential Write BW | {h_bw} | {fl_bw} | {dl_bw} | {q64_bw} | {q2m_bw} | **{speedup}** |")

        elif "read" in tid and "rand" not in tid:
            h_bw = f"{h['read_bw']:.1f} MB/s" if h else "N/A"
            fl_bw = f"{fl['read_bw']:.1f} MB/s"
            dl_bw = f"{dl['read_bw']:.1f} MB/s"
            q64_bw = f"{q64['read_bw']:.1f} MB/s"
            q2m_bw = f"{q2m['read_bw']:.1f} MB/s"
            speedup = f"{q2m['read_bw']/q64['read_bw']:.2f}x" if q64['read_bw'] > 0 else "N/A"
            print(f"| **{name}** | Sequential Read BW | {h_bw} | {fl_bw} | {dl_bw} | {q64_bw} | {q2m_bw} | **{speedup}** |")

        elif "rand_read" in tid:
            h_str = f"{h['read_iops']:,.0f} IOPS" if h else "N/A"
            fl_str = f"{fl['read_iops']:,.0f} IOPS"
            dl_str = f"{dl['read_iops']:,.0f} IOPS"
            q64_str = f"{q64['read_iops']:,.0f} IOPS"
            q2m_str = f"{q2m['read_iops']:,.0f} IOPS"
            speedup = f"{q2m['read_iops']/q64['read_iops']:.2f}x" if q64['read_iops'] > 0 else "N/A"
            print(f"| **{name}** | Random Read IOPS | {h_str} | {fl_str} | {dl_str} | {q64_str} | {q2m_str} | **{speedup}** |")

        elif "rand_write" in tid:
            h_str = f"{h['write_iops']:,.0f} IOPS" if h else "N/A"
            fl_str = f"{fl['write_iops']:,.0f} IOPS"
            dl_str = f"{dl['write_iops']:,.0f} IOPS"
            q64_str = f"{q64['write_iops']:,.0f} IOPS"
            q2m_str = f"{q2m['write_iops']:,.0f} IOPS"
            speedup = f"{q2m['write_iops']/q64['write_iops']:.2f}x" if q64['write_iops'] > 0 else "N/A"
            print(f"| **{name}** | Random Write IOPS | {h_str} | {fl_str} | {dl_str} | {q64_str} | {q2m_str} | **{speedup}** |")

        elif "mixed" in tid:
            h_tot = (h['read_iops'] + h['write_iops']) if h else 0
            fl_tot = fl['read_iops'] + fl['write_iops']
            dl_tot = dl['read_iops'] + dl['write_iops']
            q64_tot = q64['read_iops'] + q64['write_iops']
            q2m_tot = q2m['read_iops'] + q2m['write_iops']
            h_str = f"{h_tot:,.0f} IOPS" if h else "N/A"
            fl_str = f"{fl_tot:,.0f} IOPS"
            dl_str = f"{dl_tot:,.0f} IOPS"
            q64_str = f"{q64_tot:,.0f} IOPS"
            q2m_str = f"{q2m_tot:,.0f} IOPS"
            speedup = f"{q2m_tot/q64_tot:.2f}x" if q64_tot > 0 else "N/A"
            print(f"| **{name}** | Mixed 70/30 IOPS | {h_str} | {fl_str} | {dl_str} | {q64_str} | {q2m_str} | **{speedup}** |")

    print("\n### Latency Percentiles (p50 / p95 / p99) & CPU Utilization")
    for tid, data in results.items():
        name = data["name"]
        fl = parse_job(data.get("flat"))
        dl = parse_job(data.get("vmdk"))
        q64 = parse_job(data.get("qcow_64k"))
        q2m = parse_job(data.get("qcow_2m"))
        print(f"\n- **{name}**:")
        if "write" in tid and "rand" not in tid:
            print(f"  - Flat uVM:     p50: {fl['write_p50_us']:,.1f} µs, p95: {fl['write_p95_us']:,.1f} µs, p99: {fl['write_p99_us']:,.1f} µs, usr: {fl['usr_cpu']:.1f}%, sys: {fl['sys_cpu']:.1f}%")
            print(f"  - Flat VMDK:    p50: {dl['write_p50_us']:,.1f} µs, p95: {dl['write_p95_us']:,.1f} µs, p99: {dl['write_p99_us']:,.1f} µs, usr: {dl['usr_cpu']:.1f}%, sys: {dl['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (64k):  p50: {q64['write_p50_us']:,.1f} µs, p95: {q64['write_p95_us']:,.1f} µs, p99: {q64['write_p99_us']:,.1f} µs, usr: {q64['usr_cpu']:.1f}%, sys: {q64['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (2M):   p50: {q2m['write_p50_us']:,.1f} µs, p95: {q2m['write_p95_us']:,.1f} µs, p99: {q2m['write_p99_us']:,.1f} µs, usr: {q2m['usr_cpu']:.1f}%, sys: {q2m['sys_cpu']:.1f}%")
        elif "read" in tid and "rand" not in tid:
            print(f"  - Flat uVM:     p50: {fl['read_p50_us']:,.1f} µs, p95: {fl['read_p95_us']:,.1f} µs, p99: {fl['read_p99_us']:,.1f} µs, usr: {fl['usr_cpu']:.1f}%, sys: {fl['sys_cpu']:.1f}%")
            print(f"  - Flat VMDK:    p50: {dl['read_p50_us']:,.1f} µs, p95: {dl['read_p95_us']:,.1f} µs, p99: {dl['read_p99_us']:,.1f} µs, usr: {dl['usr_cpu']:.1f}%, sys: {dl['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (64k):  p50: {q64['read_p50_us']:,.1f} µs, p95: {q64['read_p95_us']:,.1f} µs, p99: {q64['read_p99_us']:,.1f} µs, usr: {q64['usr_cpu']:.1f}%, sys: {q64['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (2M):   p50: {q2m['read_p50_us']:,.1f} µs, p95: {q2m['read_p95_us']:,.1f} µs, p99: {q2m['read_p99_us']:,.1f} µs, usr: {q2m['usr_cpu']:.1f}%, sys: {q2m['sys_cpu']:.1f}%")
        elif "rand_read" in tid:
            print(f"  - Flat uVM:     p50: {fl['read_p50_us']:,.1f} µs, p95: {fl['read_p95_us']:,.1f} µs, p99: {fl['read_p99_us']:,.1f} µs, usr: {fl['usr_cpu']:.1f}%, sys: {fl['sys_cpu']:.1f}%")
            print(f"  - Flat VMDK:    p50: {dl['read_p50_us']:,.1f} µs, p95: {dl['read_p95_us']:,.1f} µs, p99: {dl['read_p99_us']:,.1f} µs, usr: {dl['usr_cpu']:.1f}%, sys: {dl['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (64k):  p50: {q64['read_p50_us']:,.1f} µs, p95: {q64['read_p95_us']:,.1f} µs, p99: {q64['read_p99_us']:,.1f} µs, usr: {q64['usr_cpu']:.1f}%, sys: {q64['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (2M):   p50: {q2m['read_p50_us']:,.1f} µs, p95: {q2m['read_p95_us']:,.1f} µs, p99: {q2m['read_p99_us']:,.1f} µs, usr: {q2m['usr_cpu']:.1f}%, sys: {q2m['sys_cpu']:.1f}%")
        elif "rand_write" in tid:
            print(f"  - Flat uVM:     p50: {fl['write_p50_us']:,.1f} µs, p95: {fl['write_p95_us']:,.1f} µs, p99: {fl['write_p99_us']:,.1f} µs, usr: {fl['usr_cpu']:.1f}%, sys: {fl['sys_cpu']:.1f}%")
            print(f"  - Flat VMDK:    p50: {dl['write_p50_us']:,.1f} µs, p95: {dl['write_p95_us']:,.1f} µs, p99: {dl['write_p99_us']:,.1f} µs, usr: {dl['usr_cpu']:.1f}%, sys: {dl['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (64k):  p50: {q64['write_p50_us']:,.1f} µs, p95: {q64['write_p95_us']:,.1f} µs, p99: {q64['write_p99_us']:,.1f} µs, usr: {q64['usr_cpu']:.1f}%, sys: {q64['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (2M):   p50: {q2m['write_p50_us']:,.1f} µs, p95: {q2m['write_p95_us']:,.1f} µs, p99: {q2m['write_p99_us']:,.1f} µs, usr: {q2m['usr_cpu']:.1f}%, sys: {q2m['sys_cpu']:.1f}%")
        elif "mixed" in tid:
            print(f"  - Flat uVM:     Read p50: {fl['read_p50_us']:,.1f} µs, Write p50: {fl['write_p50_us']:,.1f} µs, usr: {fl['usr_cpu']:.1f}%, sys: {fl['sys_cpu']:.1f}%")
            print(f"  - Flat VMDK:    Read p50: {dl['read_p50_us']:,.1f} µs, Write p50: {dl['write_p50_us']:,.1f} µs, usr: {dl['usr_cpu']:.1f}%, sys: {dl['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (64k):  Read p50: {q64['read_p50_us']:,.1f} µs, Write p50: {q64['write_p50_us']:,.1f} µs, usr: {q64['usr_cpu']:.1f}%, sys: {q64['sys_cpu']:.1f}%")
            print(f"  - QCOW2 (2M):   Read p50: {q2m['read_p50_us']:,.1f} µs, Write p50: {q2m['write_p50_us']:,.1f} µs, usr: {q2m['usr_cpu']:.1f}%, sys: {q2m['sys_cpu']:.1f}%")

if __name__ == "__main__":
    main()
