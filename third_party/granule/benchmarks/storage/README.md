# Granule Storage Backend Benchmark: Extents vs Flat vs QCOW2

This compares every way Granule can back a guest's root disk against each
other and against the host's own filesystem. All of it runs on **one dedicated
GCE VM** (`n2-standard-16`, `--enable-nested-virtualization`, 100 GB `pd-ssd`
boot disk, Debian 12, us-central1-a), so no shared-cluster workload competes
for the disk:

| Backend | How it is set up |
|:---|:---|
| **Native Host** | `fio` straight onto the VM's ext4 root (`/var/tmp`) |
| **Flat raw `disk.img`** | `CHV_DISK_MODE=persistent`: cloud-hypervisor writes the image's `disk.img` in place |
| **QCOW2 overlay, 64 KiB clusters** | `CHV_DISK_MODE=overlay` with a `qemu-img create -o cluster_size=64k` overlay backed by `disk.img`, bind-mounted over `/var/lib/granule/overlay/disk.qcow2`. Granule only writes 2 MiB clusters itself, so this one is made by hand |
| **QCOW2 overlay, 2 MiB clusters** | `CHV_DISK_MODE=overlay`, granule's own default overlay (`pkg/qcow2`, no reflinks on ext4) |
| **Boot-assembled extents, 64 MiB** | `pkg/extent` directory from `granule guest split`, assembled into a flat overlay by `PrepareOverlay` at container start (standard backend) |
| **vhost-user extents, persistent, 16 / 64 MiB** | `pkg/extents` directory (`granule-shim extents split`), `CHV_STORAGE_BACKEND=extents`, `CHV_DISK_MODE=persistent`: each extent file in the image is opened `O_RDWR` in place on its first write |
| **vhost-user extents, overlay, 16 / 64 MiB** | the same directory with `CHV_DISK_MODE=overlay`: reads come from the image's extents `O_RDONLY`, and an extent is materialized into `/var/lib/granule/overlay/disk.extents` on its first write |

Every guest is the same image: granule's debian guest, built from this tree
with `fio` installed in it and `GUEST_DISK_EXTRA_MB=5120`. That gives a
6.1 GB `disk.img` with 4.7 GB free, which the 4 GiB dataset needs in
persistent mode, because only overlays can be resized at boot. The extent
images come from the same `disk.img` (`Dockerfile.extents`). Every VM runs with
`CHV_VCPUS=4` and `CHV_DISK_NUM_QUEUES=4`.

## Workloads

1. **fio**: `direct=1`, `ioengine=libaio`, `numjobs=4`, `group_reporting`,
   and a 4 GiB dataset made of four 1 GiB files that all five workloads
   share. Each workload is capped at `--runtime=15` s. Host page caches are
   dropped before each workload, and the guest's are too.
   - Sequential Write / Read, `bs=1M`: MB/s
   - Random Read / Write 4K, `iodepth=32`: IOPS
   - Mixed Random 70/30 4K, `iodepth=16`: IOPS
   - p50 / p95 / p99 completion latency (µs) and guest `usr` / `sys` CPU for each
2. **Start latency**: the time from `docker run -d` to `granule ready`
   succeeding, with host caches dropped first, on two fresh containers (cold),
   and from `docker start` of a stopped container whose disk is already
   prepared to `granule ready` (warm).
3. **OCI layer cost**: boot the image, write 10 MiB in the guest (two
   files, `dd conv=fsync`), `docker stop`, then time `docker commit`. The
   committed layer's size is the new image's size minus the base image's.

## Results

From grain CI run `d3c438fd5e8a35e2`, which ran `.grain/workflows/storage-benchmark.yml`
at `faebc17`. The raw data is in
[`benchmarks/results/storage/summary_storage_benchmark.json`](../results/storage/summary_storage_benchmark.json),
with one file per backend and workload alongside it. Each figure is a single run.

#### Throughput and IOPS

| Backend | Seq Write 1M (MB/s) | Seq Read 1M (MB/s) | Rand Read 4K (IOPS) | Rand Write 4K (IOPS) | Mixed 70/30 4K (IOPS) |
|:---|---:|---:|---:|---:|---:|
| Native Host | 289 | 289 | 10,047 | 9,297 | 9,302 (6,490 r / 2,812 w) |
| Flat raw disk.img (persistent) | 1,791 | 289 | 10,048 | 36,527 | 13,776 (9,623 r / 4,153 w) |
| QCOW2 overlay, 64 KiB clusters | 220 | 235 | 10,298 | 5,902 | 8,008 (5,587 r / 2,421 w) |
| QCOW2 overlay, 2 MiB clusters | 145 | 439 | 17,380 | 270 | 1,598 (1,107 r / 491 w) |
| Boot-assembled extents, 64 MiB (pkg/extent) | 1,797 | 289 | 10,046 | 127,054 | 14,129 (9,869 r / 4,260 w) |
| vhost-user extents, persistent, 16 MiB | 2,408 | 289 | 10,047 | 54,570 | 9,936 (6,934 r / 3,002 w) |
| vhost-user extents, persistent, 64 MiB | 1,979 | 289 | 10,047 | 51,864 | 10,042 (7,010 r / 3,032 w) |
| vhost-user extents, overlay, 16 MiB | 3,098 | 289 | 10,050 | 54,521 | 10,023 (6,998 r / 3,025 w) |
| vhost-user extents, overlay, 64 MiB | 2,103 | 289 | 10,047 | 50,743 | 9,922 (6,927 r / 2,995 w) |

#### Completion latency p50 / p95 / p99 (µs) and guest CPU (usr / sys %)

| Workload | Backend | p50 | p95 | p99 | usr / sys |
|:---|:---|---:|---:|---:|---:|
| **Seq Write 1M** | Native Host | 14,484 | 15,008 | 16,318 | 0.5 / 0.7 |
|  | Flat raw disk.img (persistent) | 1,581 | 3,719 | 3,850 | 1.7 / 3.0 |
|  | QCOW2 overlay, 64 KiB clusters | 3,621 | 5,931 | 7,176 | 0.4 / 0.7 |
|  | QCOW2 overlay, 2 MiB clusters | 16,908 | 36,438 | 41,681 | 0.2 / 0.2 |
|  | Boot-assembled extents, 64 MiB (pkg/extent) | 1,630 | 3,392 | 3,621 | 2.1 / 1.9 |
|  | vhost-user extents, persistent, 16 MiB | 1,012 | 1,417 | 1,597 | 3.1 / 3.6 |
|  | vhost-user extents, persistent, 64 MiB | 832 | 1,122 | 1,434 | 2.3 / 3.3 |
|  | vhost-user extents, overlay, 16 MiB | 782 | 1,286 | 1,483 | 2.6 / 4.9 |
|  | vhost-user extents, overlay, 64 MiB | 832 | 1,597 | 1,892 | 2.6 / 3.1 |
| **Seq Read 1M** | Native Host | 13,828 | 14,614 | 15,532 | 0.0 / 0.7 |
|  | Flat raw disk.img (persistent) | 13,828 | 15,532 | 19,005 | 0.1 / 0.6 |
|  | QCOW2 overlay, 64 KiB clusters | 12,124 | 23,724 | 38,535 | 0.1 / 0.6 |
|  | QCOW2 overlay, 2 MiB clusters | 6,783 | 21,103 | 23,462 | 0.1 / 1.0 |
|  | Boot-assembled extents, 64 MiB (pkg/extent) | 12,517 | 20,840 | 21,889 | 0.1 / 0.8 |
|  | vhost-user extents, persistent, 16 MiB | 10,158 | 26,870 | 31,588 | 0.2 / 0.7 |
|  | vhost-user extents, persistent, 64 MiB | 10,420 | 21,103 | 23,462 | 0.1 / 0.7 |
|  | vhost-user extents, overlay, 16 MiB | 10,420 | 20,316 | 29,229 | 0.1 / 0.7 |
|  | vhost-user extents, overlay, 64 MiB | 10,027 | 22,676 | 24,248 | 0.1 / 0.6 |
| **Rand Read 4K** | Native Host | 14,221 | 14,484 | 14,614 | 0.9 / 3.6 |
|  | Flat raw disk.img (persistent) | 13,959 | 17,170 | 22,938 | 1.9 / 5.4 |
|  | QCOW2 overlay, 64 KiB clusters | 13,959 | 16,712 | 22,676 | 1.6 / 4.9 |
|  | QCOW2 overlay, 2 MiB clusters | 1,794 | 16,318 | 21,627 | 2.1 / 5.3 |
|  | Boot-assembled extents, 64 MiB (pkg/extent) | 13,959 | 17,433 | 23,200 | 1.4 / 5.8 |
|  | vhost-user extents, persistent, 16 MiB | 12,386 | 15,270 | 19,268 | 0.7 / 0.9 |
|  | vhost-user extents, persistent, 64 MiB | 12,255 | 15,139 | 17,433 | 0.6 / 0.9 |
|  | vhost-user extents, overlay, 16 MiB | 12,124 | 15,270 | 22,151 | 0.6 / 0.9 |
|  | vhost-user extents, overlay, 64 MiB | 12,255 | 15,270 | 16,908 | 0.6 / 0.8 |
| **Rand Write 4K** | Native Host | 14,221 | 14,484 | 14,746 | 0.7 / 2.5 |
|  | Flat raw disk.img (persistent) | 774 | 1,221 | 1,466 | 2.6 / 7.3 |
|  | QCOW2 overlay, 64 KiB clusters | 12,780 | 30,540 | 39,584 | 0.4 / 0.7 |
|  | QCOW2 overlay, 2 MiB clusters | 287,310 | 1,669,333 | 2,399,142 | 0.0 / 0.0 |
|  | Boot-assembled extents, 64 MiB (pkg/extent) | 946 | 1,483 | 1,728 | 9.1 / 29.7 |
|  | vhost-user extents, persistent, 16 MiB | 255 | 1,516 | 1,728 | 3.1 / 16.7 |
|  | vhost-user extents, persistent, 64 MiB | 285 | 1,384 | 1,696 | 3.2 / 17.6 |
|  | vhost-user extents, overlay, 16 MiB | 259 | 1,384 | 1,696 | 3.0 / 17.5 |
|  | vhost-user extents, overlay, 64 MiB | 313 | 1,384 | 1,679 | 3.0 / 17.0 |
| **Mixed 70/30 4K** | Native Host | 7,111 / 7,045 | 7,373 / 7,373 | 7,635 / 8,290 | 0.8 / 3.3 |
|  | Flat raw disk.img (persistent) | 6,783 / 228 | 9,110 / 2,867 | 11,338 / 5,276 | 1.5 / 7.5 |
|  | QCOW2 overlay, 64 KiB clusters | 5,996 / 4,358 | 12,124 / 9,241 | 15,139 / 11,731 | 0.8 / 1.8 |
|  | QCOW2 overlay, 2 MiB clusters | 13,435 / 20,840 | 154,141 / 128,451 | 250,610 / 212,861 | 0.1 / 0.4 |
|  | Boot-assembled extents, 64 MiB (pkg/extent) | 6,783 / 239 | 9,110 / 2,933 | 11,338 / 5,276 | 1.9 / 7.2 |
|  | vhost-user extents, persistent, 16 MiB | 4,948 / 4,489 | 10,289 / 9,765 | 14,484 / 13,566 | 0.6 / 1.2 |
|  | vhost-user extents, persistent, 64 MiB | 4,817 / 4,293 | 10,027 / 9,372 | 14,352 / 13,435 | 0.6 / 1.1 |
|  | vhost-user extents, overlay, 16 MiB | 4,817 / 4,358 | 9,896 / 9,503 | 14,221 / 13,828 | 0.7 / 1.1 |
|  | vhost-user extents, overlay, 64 MiB | 4,686 / 4,227 | 9,896 / 9,372 | 14,221 / 13,828 | 0.7 / 1.0 |

#### Start latency (`docker run` / `docker start` -> `granule ready`, ms)

| Backend | Cold start | Cold start (2nd sample) | Warm restart (disk already prepared) |
|:---|---:|---:|---:|
| Flat raw disk.img (persistent) | 32,085 | 31,845 | 2,050 |
| QCOW2 overlay, 64 KiB clusters | 2,076 | 2,084 | – |
| QCOW2 overlay, 2 MiB clusters | 2,116 | 2,107 | 2,059 |
| Boot-assembled extents, 64 MiB (pkg/extent) | 7,470 | 7,419 | 2,082 |
| vhost-user extents, persistent, 16 MiB | 2,146 | 2,050 | 2,066 |
| vhost-user extents, persistent, 64 MiB | 3,105 | 2,590 | 2,054 |
| vhost-user extents, overlay, 16 MiB | 2,094 | 2,146 | 2,050 |
| vhost-user extents, overlay, 64 MiB | 2,602 | 2,538 | 2,049 |

#### OCI layer cost of a 10 MiB guest change (`docker commit`)

| Backend | Guest write (MiB) | Committed layer (MiB) | `docker commit` (ms) | Files under /var/lib/granule in `docker diff` |
|:---|---:|---:|---:|---:|
| Flat raw disk.img (persistent) | 10 | 6,101 | 65,344 | 1 |
| QCOW2 overlay, 2 MiB clusters | 10 | 74.4 | 20,804 | 1 |
| Boot-assembled extents, 64 MiB (pkg/extent) | 10 | 6,101 | 87,450 | 1 |
| vhost-user extents, persistent, 16 MiB | 10 | 165 | 23,962 | 10 |
| vhost-user extents, persistent, 64 MiB | 10 | 483 | 28,359 | 8 |
| vhost-user extents, overlay, 16 MiB | 10 | 165 | 24,250 | 11 |
| vhost-user extents, overlay, 64 MiB | 10 | 483 | 28,833 | 9 |


## What the numbers say

**1. Reads are capped by the disk, and every raw backend reaches that cap.**
A 100 GB `pd-ssd` is provisioned for about 6k–15k IOPS and 240–290 MB/s, and
the host itself measures 289 MB/s and 10,047 IOPS. The guests read through
the host's page cache after it has been dropped, so they hit the same
ceiling. Flat, boot-assembled extents and all four vhost-user extent
configurations land on exactly that line: 289 MB/s and 10,046–10,050 IOPS.
p99 latency is within the noise (17–23 ms). **On this disk, the vhost-user
extent backend adds no measurable read overhead.** The QCOW2 read figures
look higher (439 MB/s and 17,380 IOPS at 2 MiB). They are an artifact, not a
win: QCOW2's sequential write was too slow to lay out the whole 4 GiB within
its 15 s cap, so some of the dataset was still unwritten ext4 extents, which
the guest reads as zeros without touching the disk.

**2. Writes land in the host page cache, so they beat the native disk.**
Neither cloud-hypervisor's raw disk nor the vhost-user server opens its files
`O_DIRECT` on the host. A guest's `direct=1` write is complete once it is in
host memory. That is why every raw-path backend writes faster than the native
host (289 MB/s, 9.3k IOPS), and these write numbers measure the VMM's I/O path,
not the disk. On that path:
- vhost-user extents do 2.0–3.1 GB/s sequential write and 51–55k random-write
  IOPS, against flat `disk.img` in place at 1.8 GB/s and 36.5k IOPS. Write
  p50/p99 is 0.26–0.31 / 1.7 ms, against 0.77 / 1.5 ms for flat. Extent size
  (16 vs 64 MiB) makes no difference past the noise of a single run.
- The 127k random-write IOPS for boot-assembled extents is an outlier of a single
  run: its overlay is a plain raw file whose every page was written moments
  earlier, during assembly. Its other workloads match flat, which is what it
  boots as.
- Mixed 70/30 is where vhost-user extents fall behind: about 10.0k IOPS,
  against 13.8k for flat and 14.1k for boot-assembled. Its write p50 is
  4.3–4.5 ms, against 0.23 ms for the raw paths, so in this workload its
  writes queue behind the disk-bound reads instead of completing from cache.
  This is the one place in the suite where the runtime extent backend costs
  something, and it is the place to look first if it is tuned.

**3. QCOW2 overlays collapse on random writes.** 2 MiB clusters managed 270
random-write IOPS, with a 2.4 s p99, and 1.6k mixed IOPS. Each first 4K write to
a cluster allocates 2 MiB and copies it from the backing `disk.img`, which is up
to 512x write amplification against a disk that tops out at 290 MB/s. 64 KiB
clusters cut the copy and reach 5.9k IOPS, but their sequential write (220
MB/s) and read (235 MB/s) are the slowest in the table. The flat and extent
paths do none of this, because the first write to a region is the write itself.

**4. Cold start: extents boot as fast as QCOW2, and flat pays for copy-up.**
- Flat persistent takes **32 s** to cold start. The first `O_RDWR` open of
  `disk.img` makes overlayfs copy up the whole 6.1 GB file before the guest can
  boot.
- Boot-assembled extents take **7.5 s**. `PrepareOverlay` assembles the extents into
  a flat overlay, and on ext4 that is a sparse copy, because there is no
  `FICLONERANGE`. A reflink filesystem (XFS or Btrfs) would remove most of that cost.
- QCOW2 overlays and vhost-user extents start in **2.1 s** (16 MiB extents) to
  2.6–3.1 s (64 MiB extents). The extents are opened lazily, so nothing is
  copied before boot. 64 MiB extents cost 0.5–1 s more, because the guest's
  boot-time writes copy up or materialize larger extents.
- A warm restart of any backend is about 2.05 s. That is the floor set by
  booting the VM itself.

**5. OCI layers: extents keep a small change small.** The guest wrote 10 MiB:

| Backend | Committed layer |
|:---|---:|
| Flat `disk.img` | **6,101 MiB**: the whole copied-up image |
| Boot-assembled extents | **6,101 MiB**: the assembled flat overlay is in the container layer |
| vhost-user extents, 64 MiB | **483 MiB**: 6 extent files, plus the manifest in overlay mode |
| vhost-user extents, 16 MiB | **165 MiB**: 8 extent files, plus the manifest in overlay mode |
| QCOW2 2 MiB | **74 MiB**: the overlay holds only the allocated clusters |

The extent backends shrink the layer **37x** (16 MiB) or **13x** (64 MiB)
compared with flat. The extents touched are the ones holding ext4 metadata
(journal, bitmaps, inode tables) as well as the data, which is why the layer
is several extents rather than one. QCOW2 produces an even smaller layer here,
but at the price of finding 3. `docker commit` took 65–87 s for the 6 GB
layers and 21–29 s for the rest, so it has a fixed floor of about 20 s on
this VM.

**Recommendation.** For images that are derived and committed (Granulefile
stages, snapshots), `CHV_STORAGE_BACKEND=extents` with `CHV_EXTENT_SIZE_MB=16`
gives the best overall trade-off. It has flat-level read throughput, faster
writes than flat, the fastest cold start, and a layer 37x smaller than flat
for a small change. 64 MiB extents performed the same but made 3x larger
layers. Flat `persistent` is still the simplest for a single long-lived VM
that is never committed, but it pays a full copy-up on first boot. Boot
assembly only helps on a reflink-capable host filesystem.

## Running it

The whole run happens on a fresh VM that is deleted afterwards:

```bash
# Needs gcloud and a service-account key that can create instances
# (GCE_KEY_FILE, default /home/debian/.gcp-service-account.json).
benchmarks/storage/gce-bench.sh --out /tmp/storage-results
```

In CI, run `.grain/workflows/storage-benchmark.yml`. It is
`workflow_dispatch` only and holds the `gcp-key` capability. It takes about 35
minutes: about 5 to build the guest, 5 to derive the extent images and 20 to
benchmark. The results appear in the job log between the
`STORAGE-BENCH-JSON-BEGIN/END` and `STORAGE-BENCH-MARKDOWN-BEGIN/END`
markers. After each backend finishes, its own data is also printed on a
`STORAGE-BENCH-PARTIAL` line, so a run that times out still yields what it
measured.

`gce-bench.sh` builds these images on the VM:

| Image | What it is |
|:---|:---|
| `granule-bench:flat` | `docker build --build-arg GUEST_DISTRO=debian --build-arg GUEST_DISK_EXTRA_MB=5120 --build-arg GUEST_SETUP_SCRIPT='apt-get ... install fio' .` |
| `granule-bench:extent-boot-64` | `Dockerfile.extents --target extent-boot` (`granule guest split -extent-size-mb 64`) |
| `granule-bench:extents-16` / `-64` | `Dockerfile.extents --target extents` (`granule-shim extents split ... 16` or `64`) |

To run the suite on a machine you already have, with those images built:

```bash
sudo python3 benchmarks/storage/run_storage_benchmark.py --live \
  --disk-bytes <size of the guest disk.img> --out-dir /tmp/storage-results
```

Pass `--variants host,flat,extents_overlay_16` to run a subset, and
`--runtime` / `--size-per-job` to change the fio caps. Run without `--live`,
the script renders the report above from
`benchmarks/results/storage/summary_storage_benchmark.json`.

## Configuring Virtio-Blk Parameters in Granule

Granule provides unified interfaces to configure these parameters across environment variables, CLI commands, Granulefiles, and guest build tooling.

### 1. Environment Variables (Direct Docker Run / Pods)
- `CHV_DISK_NUM_QUEUES`: Number of virtio-blk queues (e.g. `2`, `4`).
- `CHV_DISK_IOTHREADS`: Enable dedicated hypervisor I/O thread (`true` / `false` / `on` / `off`).
- `CHV_DISK_IO_ENGINE`: Host I/O backend engine (`io_uring`, `epoll`, `sync`).
- `CHV_VCPUS` (or `CHV_CPUS`): Number of guest vCPUs.

```bash
docker run -d --name db-vm \
  --privileged \
  -e CHV_VCPUS=4 \
  -e CHV_DISK_NUM_QUEUES=4 \
  -e CHV_DISK_IOTHREADS=true \
  -e CHV_DISK_IO_ENGINE=io_uring \
  granule:latest
```

### 2. Granulefile (`VM SET`)
```dockerfile
FROM debian:bookworm
VM WRAP

VM SET vcpus=4
VM SET disk-num-queues=4
VM SET disk-iothreads=on
VM SET disk-io-engine=io_uring
```

### 3. Granule CLI (`granule vm create`)
```bash
granule vm create db-vm \
  -vcpus 4 \
  -disk-num-queues 4 \
  -disk-iothreads \
  -disk-io-engine io_uring
```

---
