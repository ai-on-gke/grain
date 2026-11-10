#!/bin/bash
set -euo pipefail

if [ "${1:-}" = "--dry-run" ]; then
    echo "[DRY-RUN] Microbenchmarks suite configured successfully:"
    echo "  - Host vs Docker vs Granule MicroVM"
    echo "  - CPU sysbench primes, memory bandwidth, random write 4k IOPS"
    exit 0
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

CONTAINER_NAME="${CONTAINER_NAME:-myvm}"
GRANULE_IMAGE="${GRANULE_IMAGE:-granule:local}"
# fio runs with --direct=1 (O_DIRECT), which tmpfs rejects with EINVAL, and
# /tmp is tmpfs on stock Debian/systemd hosts. Even where O_DIRECT were
# accepted, a tmpfs target would measure RAM rather than block storage, so
# the disk benchmarks default to the disk-backed /var/tmp instead.
BENCH_SCRATCH_DIR="${BENCH_SCRATCH_DIR:-/var/tmp}"
GUEST_SCRATCH_DIR="/var/tmp"
GRANULE_FIO_STARTED=0

cleanup() {
    sudo rm -f "$BENCH_SCRATCH_DIR/test.fio" "$BENCH_SCRATCH_DIR/docker_test.fio"
    if [ "$GRANULE_FIO_STARTED" = "1" ]; then
        sudo docker exec "$CONTAINER_NAME" granule exec -- rm -f "$GUEST_SCRATCH_DIR/granule_test.fio" >/dev/null 2>&1 || true
    fi
    if [ "$CONTAINER_NAME" = "myvm" ]; then
        sudo docker rm -f myvm 2>/dev/null || true
    fi
}
trap cleanup EXIT

# run_fio runs the given fio command line and prints its IOPS figure. The
# output is captured before it is parsed so that when fio fails, its own
# diagnostic (e.g. "destination does not support O_DIRECT") is shown rather
# than discarded, and the script stops with fio's exit status.
run_fio() {
    local out rc=0
    out="$("$@" 2>&1)" || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "fio failed with exit status $rc: $*" >&2
        printf '%s\n' "$out" >&2
        return "$rc"
    fi
    if ! printf '%s\n' "$out" | grep -o 'IOPS=[0-9A-Za-z.]*'; then
        echo "fio output did not contain an IOPS figure: $*" >&2
        printf '%s\n' "$out" >&2
        return 1
    fi
}

if [ ! -d "$BENCH_SCRATCH_DIR" ]; then
    echo "BENCH_SCRATCH_DIR=$BENCH_SCRATCH_DIR is not a directory" >&2
    exit 1
fi

# 1. Setup Docker and Tools if not present
missing_pkgs=()
command -v docker &>/dev/null || missing_pkgs+=(docker.io)
command -v sysbench &>/dev/null || missing_pkgs+=(sysbench)
command -v fio &>/dev/null || missing_pkgs+=(fio)
if [ "${#missing_pkgs[@]}" -gt 0 ]; then
    sudo apt-get update
    sudo apt-get install -y "${missing_pkgs[@]}"
    if [[ " ${missing_pkgs[*]} " == *" docker.io "* ]] && [ -n "${USER:-}" ]; then
        sudo usermod -aG docker "$USER" || true
    fi
fi

if [ "$(sudo docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || true)" != "true" ]; then
    if ! sudo docker image inspect "$GRANULE_IMAGE" >/dev/null 2>&1; then
        echo "Building $GRANULE_IMAGE..."
        sudo docker build --build-arg GUEST_DISK_EXTRA_MB=2048 --build-arg GUEST_SETUP_SCRIPT="apt-get update && apt-get install -y sysbench fio" -t "$GRANULE_IMAGE" .
    fi
fi

if ! sudo docker image inspect bench:local >/dev/null 2>&1; then
    echo "Building bench:local..."
    sudo docker build -t bench:local - <<'EOF'
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y sysbench fio pciutils kmod strace net-tools
EOF
fi

echo "=== System Info ==="
lscpu | grep "Model name" || true

echo "=== [1] Host Native ==="
echo "CPU (events/sec):"
sysbench cpu --cpu-max-prime=20000 --threads=1 run | grep "events per second:"
echo "RAM (MiB/sec):"
sysbench memory --memory-block-size=1K --memory-total-size=10G --memory-oper=read --threads=1 run | grep -o '([0-9.]* MiB/sec)'
echo "Disk (IOPS):"
run_fio fio --name=host-randwrite --filename="$BENCH_SCRATCH_DIR/test.fio" --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting
rm -f "$BENCH_SCRATCH_DIR/test.fio"

echo "=== [2] Docker Native ==="
echo "CPU (events/sec):"
sudo docker run --rm bench:local sysbench cpu --cpu-max-prime=20000 --threads=1 run | grep "events per second:"
echo "RAM (MiB/sec):"
sudo docker run --rm bench:local sysbench memory --memory-block-size=1K --memory-total-size=10G --memory-oper=read --threads=1 run | grep -o '([0-9.]* MiB/sec)'
echo "Disk (IOPS):"
run_fio sudo docker run --rm -v "$BENCH_SCRATCH_DIR:/data" bench:local fio --name=dock-randwrite --filename=/data/docker_test.fio --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting
sudo rm -f "$BENCH_SCRATCH_DIR/docker_test.fio"

echo "=== [3] Granule (VM) ==="
if [ "$(sudo docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || true)" != "true" ]; then
    sudo docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    echo "Starting granule VM..."
    sudo docker run -d --name "$CONTAINER_NAME" --privileged -e CHV_CPUS=2 -e CHV_MEMORY_MB=2048 -e CHV_MEMORY_MAX_MB=2048 "$GRANULE_IMAGE"
    sudo docker exec "$CONTAINER_NAME" granule ready -timeout 120s
fi

echo "CPU (events/sec):"
sudo docker exec "$CONTAINER_NAME" granule exec -- sysbench cpu --cpu-max-prime=20000 --threads=1 run | grep "events per second:"
echo "RAM (MiB/sec):"
sudo docker exec "$CONTAINER_NAME" granule exec -- sysbench memory --memory-block-size=1K --memory-total-size=10G --memory-oper=read --threads=1 run | grep -o '([0-9.]* MiB/sec)'
echo "Disk (IOPS):"
GRANULE_FIO_STARTED=1
run_fio sudo docker exec "$CONTAINER_NAME" granule exec -- fio --name=granule-randwrite --filename="$GUEST_SCRATCH_DIR/granule_test.fio" --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting
sudo docker exec "$CONTAINER_NAME" granule exec -- rm -f "$GUEST_SCRATCH_DIR/granule_test.fio"
GRANULE_FIO_STARTED=0
