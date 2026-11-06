# `CHV_DISK_DIRECT`: direct=on vs off

Task 2577 made `CHV_DISK_DIRECT=auto` the default. Since then, every writable
disk whose image and backing chain accept `O_DIRECT` is opened `direct=on`, so
the VMM's writes no longer sit in host page cache charged to the VM's
container. The first timings came from a grain sandbox: nested virtualization,
a virtio host disk, and other work on the same host. There,
`test/oom-pagecache.sh` took 633 s with direct=on against 351 s buffered on the
qcow2 overlay, but a second buffered run took more than 590 s. Those numbers
measure noise, not direct I/O.

These numbers come from one dedicated GCE VM instead: `n2-standard-16`, nested
virtualization, a 100 GB `pd-ssd` boot disk, Debian 12, kernel 6.1, 62 GiB of
RAM. That is the machine shape a GKE node running granule has. Nothing else
ran on the VM. It was produced by grain CI run `5d936794a61c5184`, which ran
`.grain/workflows/disk-direct-benchmark.yml`, that is,
`benchmarks/storage/gce-bench.sh --disk-direct`. The VMs ran under docker on
the VM with cgroup v2, which applies a container memory limit the same way the
kubelet does. They were not scheduled by a kubelet. The raw data is in
[`benchmarks/results/storage-disk-direct/summary_storage_benchmark.json`](../results/storage-disk-direct/summary_storage_benchmark.json).

The two disks:

- **Raw**: `CHV_DISK_MODE=persistent` on the image's own `disk.img`. A raw
  reflink overlay on XFS or btrfs takes the same write path, but the VM's
  ext4 cannot make reflinks.
- **QCOW2**: `CHV_DISK_MODE=overlay`, granule's own overlay with 2 MiB
  clusters, backed by `disk.img`.

## `test/oom-pagecache.sh`

The test boots a 4 GiB guest under a 4608 MiB container limit with no swap.
The guest fills its page cache with 5 GiB of writes and reads, then formats
and copies a 3 GiB image with `mke2fs`, `dd` and `cp`. The four combinations
ran in turn, then again in the same order, with host caches dropped before
each run.

| Disk | direct | Run 1 (s) | Run 2 (s) | Container peak (MiB of 4608) | oom_kill |
|:---|:---|---:|---:|---:|---:|
| QCOW2 overlay | on | 112 | 113 | 4608, 4608 | 0, 0 |
| QCOW2 overlay | off | 80 | 81 | 4608, 4608 | 0, 0 |
| Raw disk.img | on | 62 | 63 | 4146, 4168 | 0, 0 |
| Raw disk.img | off | 53 | 55 | 4608, 4608 | 0, 0 |

## fio, in the guest

This is `run_storage_benchmark.py`'s suite (`direct=1` in the guest, libaio,
4 jobs, a 4 GiB dataset, 15 s per workload, 4 vCPUs, 4 queues). The containers
had no memory limit.

| Disk | Seq Write 1M (MB/s) | Seq Read 1M (MB/s) | Rand Read 4K (IOPS) | Rand Write 4K (IOPS) | Mixed 70/30 4K (IOPS) |
|:---|---:|---:|---:|---:|---:|
| Raw disk.img, direct=off | 1,809 | 289 | 10,046 | 184,284 | 14,152 (9,885 r / 4,267 w) |
| Raw disk.img, direct=on | 289 | 289 | 10,044 | 9,468 | 9,473 (6,608 r / 2,865 w) |
| QCOW2 overlay, direct=off | 145 | 408 | 14,760 | 289 | 1,571 (1,089 r / 482 w) |
| QCOW2 overlay, direct=on | 144 | 547 | 19,640 | 452 | 1,403 (971 r / 432 w) |

For comparison, fio directly on this VM's ext4 root measured 289 MB/s
sequential and about 9,300 random 4K write IOPS (`README.md`). Those are the
pd-ssd's own limits. The full latency table is
`python3 benchmarks/storage/run_storage_benchmark.py --summary benchmarks/results/storage-disk-direct/summary_storage_benchmark.json`.

## What the numbers say

- **On the raw disk, direct=on runs at the disk's own speed.** Buffered, the
  guest's "1.8 GB/s" sequential and 184k random write IOPS were host page
  cache absorbing writes the pd-ssd could not keep up with. With direct=on,
  those figures fall to the native host's 289 MB/s and about 9.5k IOPS. Under
  a container limit, the same buffering is what fills the container up:
  `oom-pagecache.sh` reached the limit (4608 MiB) every time with direct=off
  and stayed about 450 MiB under it with direct=on. The cost on that
  write-heavy workload is 15% of wall-clock time (62 s against 54 s).
- **On the qcow2 overlay, direct I/O costs nothing in fio.** Sequential write
  is the same (144 against 145 MB/s). Random write is faster with direct=on
  (452 against 289 IOPS), and so are reads. The 2 MiB-cluster overlay is
  bounded by its own cluster allocation, not by the host cache. Its
  `oom-pagecache.sh` run is 40% slower with direct=on (113 s against 81 s),
  and the container still reaches its limit. The overlay's backing
  `disk.img` is opened read-only and is never opened direct, so its reads
  still fill page cache. That cache is clean, and the kernel reclaims it
  rather than OOM-killing the container.
- **No configuration was OOM-killed**, including direct=off, with task 2577's
  balloon and headroom in place. Buffered I/O is not unsafe in this one test.
  It does leave the container sitting at its limit, so whether the VMM
  survives depends on reclaim keeping up with dirty pages.

## Decision

`auto` stays the default and keeps meaning "every writable disk that accepts
`O_DIRECT`", qcow2 included. Making `auto` mean "raw only" would only have
been worth it if direct I/O cost a lot on the qcow2 path, and fio shows it
costs nothing there. The overlay's 40% slowdown on the `mke2fs`/`cp`
workload is the price of keeping that workload's writes out of the
container's memory. Defaulting to off would mean every VM runs at its
container's limit whenever it writes faster than its disk, with only the
balloon and headroom keeping it alive. A VM that values bulk write
throughput over memory headroom can still set `CHV_DISK_DIRECT=off`.

Each figure is from a single run, or two runs for `oom-pagecache.sh`. To
measure again, run `benchmarks/storage/gce-bench.sh --disk-direct`, either by
hand or as the on-demand `disk-direct` job in
`.github/workflows/disk-direct-benchmark.yml`.
