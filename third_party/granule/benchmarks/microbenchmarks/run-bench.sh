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

cleanup() {
    sudo rm -f /tmp/test.fio /tmp/docker_test.fio
    if [ "$CONTAINER_NAME" = "myvm" ]; then
        sudo docker rm -f myvm 2>/dev/null || true
    fi
}
trap cleanup EXIT

# 1. Setup Docker and Tools if not present
if ! command -v docker &> /dev/null || ! command -v sysbench &> /dev/null || ! command -v fio &> /dev/null; then
    sudo apt-get update
    sudo apt-get install -y docker.io sysbench fio
    if [ -n "${USER:-}" ]; then
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
    cat << 'EOF' > Dockerfile.bench
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y sysbench fio pciutils kmod strace net-tools
EOF
    sudo docker build -t bench:local -f Dockerfile.bench .
fi

echo "=== System Info ==="
lscpu | grep "Model name" || true

echo "=== [1] Host Native ==="
echo "CPU (events/sec):"
sysbench cpu --cpu-max-prime=20000 --threads=1 run | grep "events per second:"
echo "RAM (MiB/sec):"
sysbench memory --memory-block-size=1K --memory-total-size=10G --memory-oper=read --threads=1 run | grep -o '([0-9.]* MiB/sec)'
echo "Disk (IOPS):"
fio --name=host-randwrite --filename=/tmp/test.fio --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting 2>/dev/null | grep -o 'IOPS=[0-9A-Za-z.]*'
rm -f /tmp/test.fio

echo "=== [2] Docker Native ==="
echo "CPU (events/sec):"
sudo docker run --rm bench:local sysbench cpu --cpu-max-prime=20000 --threads=1 run | grep "events per second:"
echo "RAM (MiB/sec):"
sudo docker run --rm bench:local sysbench memory --memory-block-size=1K --memory-total-size=10G --memory-oper=read --threads=1 run | grep -o '([0-9.]* MiB/sec)'
echo "Disk (IOPS):"
sudo docker run --rm -v /tmp:/data bench:local fio --name=dock-randwrite --filename=/data/docker_test.fio --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting 2>/dev/null | grep -o 'IOPS=[0-9A-Za-z.]*'
sudo rm -f /tmp/docker_test.fio

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
sudo docker exec "$CONTAINER_NAME" granule exec -- fio --name=granule-randwrite --filename=/tmp/granule_test.fio --ioengine=libaio --iodepth=1 --rw=randwrite --bs=4k --direct=1 --size=1G --numjobs=1 --runtime=10 --group_reporting 2>/dev/null | grep -o 'IOPS=[0-9A-Za-z.]*'
