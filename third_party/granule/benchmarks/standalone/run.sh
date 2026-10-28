#!/bin/bash
# Runs granule (in its default "run" mode) directly on the host -- no
# Docker, no Kubernetes -- and times how long it takes from process launch
# to the guest's BOOT_COMPLETE marker appearing in the streamed console
# output. This is the "no container wrapper" baseline that benchmarks/gke
# is compared against; run both on the same machine (or the same node
# type) for the comparison to mean anything, since nested-virtualization
# boot time is very sensitive to the host it runs on.
#
# Usage: run.sh [iterations] [path-to-granule] [path-to-cloud-hypervisor]
set -u

N="${1:-10}"
GRANULE_BIN="${2:-./granule-shim}"
CLOUD_HYPERVISOR_BIN="${3:-./cloud-hypervisor}"
ASSETS_DIR="${ASSETS_DIR:-$(cd "$(dirname "$0")/../kernel/out" && pwd)}"
WORK_DIR="${WORK_DIR:-/tmp/granule-bench-standalone}"

mkdir -p "$WORK_DIR"
OUT="$WORK_DIR/results.csv"
echo "iter,launch_to_ready_s,launch_to_exit_s" > "$OUT"

export CHV_DISK_IMAGE="$ASSETS_DIR/disk.img"
export CHV_DISK_READONLY="${CHV_DISK_READONLY:-true}"
export CHV_NET_MODE="${CHV_NET_MODE:-none}"
export CHV_KERNEL="$ASSETS_DIR/vmlinux"
export CHV_INITRAMFS="$ASSETS_DIR/initramfs.img"
# No console= in the default: granule adds the one for this machine's
# architecture (ttyS0 on x86_64, ttyAMA0 on arm64), and BOOT_COMPLETE
# only reaches the log if the guest's console is a device it has.
export CHV_CMDLINE="${CHV_CMDLINE:-reboot=t panic=1 quiet loglevel=3}"
export CHV_CPUS=1
export CHV_MEMORY_MB=256
export CHV_BINARY_PATH="$CLOUD_HYPERVISOR_BIN"
export CHV_SHUTDOWN_TIMEOUT=5s

for i in $(seq 1 "$N"); do
  sock="$WORK_DIR/api-$i.sock"
  log="$WORK_DIR/run-$i.log"
  rm -f "$sock" "$log"
  export CHV_API_SOCKET="$sock"

  start_ns=$(date +%s%N)
  sudo -E "$GRANULE_BIN" > "$log" 2>&1 &
  pid=$!

  ready_ns=""
  for _ in $(seq 1 15000); do
    if grep -q "BOOT_COMPLETE" "$log" 2>/dev/null; then
      ready_ns=$(date +%s%N)
      break
    fi
    sleep 0.002
  done

  wait "$pid"
  exit_ns=$(date +%s%N)

  if [ -z "$ready_ns" ]; then
    echo "$i,FAILED,FAILED" >> "$OUT"
    echo "iter $i: never saw BOOT_COMPLETE, see $log" >&2
    continue
  fi

  ready_s=$(echo "scale=4; ($ready_ns - $start_ns) / 1000000000" | bc)
  exit_s=$(echo "scale=4; ($exit_ns - $start_ns) / 1000000000" | bc)
  echo "$i,$ready_s,$exit_s" >> "$OUT"
  echo "iter $i: ready=${ready_s}s exit=${exit_s}s"
done

echo "--- results ($OUT) ---"
cat "$OUT"
