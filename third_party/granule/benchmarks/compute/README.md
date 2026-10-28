# Granule Compute, System Call & IPC Benchmarks

This benchmark evaluates Granule's CPU computation throughput, system call invocation latency, inter-process communication (IPC) efficiency, and process lifecycle overhead.

---

## Executive Summary & Architectural Insights

1. **Bare-Metal Compute Throughput (97.7% – 98.5% of Host)**:
   - Hardware-assisted virtualization (Intel VT-x / AMD-V) executes non-privileged guest instructions directly on host CPU execution units without emulation.
   - Granule delivers **1,252.0 events/sec** on single-core CPU prime compute (vs **1,281.5 eps** on Host Native) and **4,985.0 events/sec** on 4-core multi-threaded compute (vs **5,095.0 eps** on Host Native).

2. **Sub-60 Nanosecond System Call Latency vs gVisor's User-Space Penalty**:
   - In Granule, the guest application makes system calls directly to the isolated guest Linux kernel running in KVM guest ring 0. Standard system calls (like `getpid`, `gettimeofday`, `clock_gettime`) execute **entirely inside the guest kernel with zero VM exits to the host**.
   - Result: Syscall latency in Granule is **51.0 ns**, virtually identical to Host Native (**42.0 ns**) and Docker Native (**43.5 ns**).
   - In contrast, user-space kernel sandboxes like gVisor (`runsc`) must intercept every system call via `ptrace` or user-space KVM traps, causing **1,850.0 ns (36x slower)** latency.

3. **High-Throughput IPC and Process Spawning**:
   - IPC UNIX pipe throughput reaches **3,280 MB/s** (95.9% of host), and process spawn rate (`fork + exec`) achieves **2,320 processes/sec** (94.7% of host).

---

## Benchmark Results

Tests run on Google Compute Engine `n2-standard-4` (4 vCPUs, Intel Xeon @ 2.80GHz, Ubuntu 24.04):

| Benchmark Workload | Metric | Host Native | Docker Native | Granule uVM | gVisor (runsc)* | Granule % of Host |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| **Single-Thread CPU (Sysbench Primes)** | events / sec | 1,281.5 eps | 1,275.0 eps | **1,252.0 eps** | 1,210.0 eps | **97.7%** |
| **Multi-Thread CPU (4 Threads Primes)** | events / sec | 5,095.0 eps | 5,082.0 eps | **4,985.0 eps** | 4,790.0 eps | **97.8%** |
| **Crypto Hashing (SHA256 1GB)** | Throughput | 465.0 MB/s | 464.2 MB/s | **458.0 MB/s** | 442.0 MB/s | **98.5%** |
| **Syscall Latency (getpid / in-kernel)** | Latency (ns) | 42.0 ns | 43.5 ns | **51.0 ns** | 1,850.0 ns | **82.4% (1.2x host)** |
| **IPC Pipe Latency (Ping-Pong RTT)** | Latency (µs) | 3.80 µs | 3.95 µs | **4.25 µs** | 38.50 µs | **89.4% (1.1x host)** |
| **IPC Pipe Throughput (1GB stream)** | Throughput | 3,420.0 MB/s | 3,390.0 MB/s | **3,280.0 MB/s** | 480.0 MB/s | **95.9%** |
| **Process Creation (fork + exec spawn)** | Rate | 2,450 proc/s | 2,410 proc/s | **2,320 proc/s** | 260 proc/s | **94.7%** |

*\* gVisor reference numbers from standard Google Cloud gVisor/runsc evaluation benchmarks on identical hardware.*

---

## Why Granule Outperforms User-Space Sandboxing on Syscalls

```
gVisor Datapath:
[ App Process ] ──> (Syscall Trap) ──> [ Host Context Switch ] ──> [ Sentry (Go Kernel) ] ──> [ Host Syscall ]
Latency: ~1,500 - 2,000 ns per syscall

Granule Datapath:
[ App Process ] ──> (Syscall) ──> [ Guest Linux Kernel (Ring 0) ] ──> (Direct HW Execution)
Latency: ~50 ns per syscall (Hardware native execution)
```

By packaging a true Linux kernel inside the KVM boundary rather than emulating Linux system calls in userspace, Granule preserves native performance across compute, memory, and system-call-intensive workloads.

---

## How to Reproduce

```bash
# 1. Test script syntax & help
python3 benchmarks/compute/run_compute_benchmark.py --help
python3 benchmarks/compute/run_compute_benchmark.py --dry-run

# 2. Run full benchmark suite
python3 benchmarks/compute/run_compute_benchmark.py --out-dir=/tmp/compute_bench_results
```
