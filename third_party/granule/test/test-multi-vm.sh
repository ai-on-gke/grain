#!/usr/bin/env bash
# test-multi-vm.sh - Runs multiple granule VMs concurrently on the same host
# and subjects them to CPU, Disk I/O, and Inter-VM Networking stress tests.

set -euo pipefail

IMAGE="${GRANULE_TEST_IMAGE:-granule:latest}"

NUM_VMS="${NUM_VMS:-3}"
STATE_DIR="$(mktemp -d /tmp/multivm-state.XXXXXX)"

CLI="./granule"
if [ ! -x "$CLI" ]; then
  if [ -d "./cmd/granule" ]; then
    echo "Building granule CLI..."
    go build -o ./granule ./cmd/granule
    CLI="./granule"
  fi
fi

echo "=========================================================="
echo " Starting Multi-VM Stress Test on $(hostname)"
echo " Image: $IMAGE, Number of VMs: $NUM_VMS"
echo " State Dir: $STATE_DIR"
echo "=========================================================="

cleanup() {
  echo "--> Cleaning up any remaining test VMs..."
  for i in $(seq 1 "$NUM_VMS"); do
    "$CLI" vm delete "stress-vm$i" -state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  done
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

echo "--> Step 1: Concurrently creating $NUM_VMS VMs..."
pids=()
for i in $(seq 1 "$NUM_VMS"); do
  "$CLI" vm create "stress-vm$i" \
    -kontur-image "$IMAGE" \
    -cpus 1 \
    -memory-mb 512 \
    -disk-size-mb 1024 \
    -state-dir "$STATE_DIR" \
    -wait \
    -ready-timeout 3m &
  pids+=($!)
done

for pid in "${pids[@]}"; do
  wait "$pid"
done
echo "--> All $NUM_VMS VMs created and reported ready."

echo "--> Step 2: Checking vm list -status..."
"$CLI" vm list -status -state-dir "$STATE_DIR"

echo "--> Step 3: Resolving IP addresses..."
declare -A VM_IPS
for i in $(seq 1 "$NUM_VMS"); do
  ip=$("$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- sh -c "ip -4 -br addr show eth0 | awk '{print \$3}' | cut -d/ -f1" | tr -d '\r\n')
  VM_IPS["stress-vm$i"]="$ip"
  echo "  stress-vm$i -> $ip"
done

echo "--> Step 4: Running concurrent CPU intensive workload..."
pids=()
for i in $(seq 1 "$NUM_VMS"); do
  "$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- bash -c "dd if=/dev/zero bs=1M count=30 2>/dev/null | sha256sum" &
  pids+=($!)
done
for pid in "${pids[@]}"; do
  wait "$pid"
done
echo "--> Concurrent CPU workload passed."

echo "--> Step 5: Running concurrent Disk I/O stress & overlay isolation test..."
pids=()
for i in $(seq 1 "$NUM_VMS"); do
  "$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- bash -c '
    dd if=/dev/urandom of=/test-io.bin bs=1M count=40 conv=fsync 2>/dev/null
    sha256sum /test-io.bin > /test-io.sha256
    sha256sum -c /test-io.sha256
    rm -f /test-io.bin /test-io.sha256
  ' &
  pids+=($!)
done
for pid in "${pids[@]}"; do
  wait "$pid"
done
echo "--> Concurrent Disk I/O & integrity test passed."

echo "--> Step 6: Running inter-VM network throughput & integrity test..."
# Setup listeners
for i in $(seq 1 "$NUM_VMS"); do
  port=$((9200 + i))
  "$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- bash -c "
    systemctl stop netrx-$i.service 2>/dev/null || true
    systemd-run --unit=netrx-$i perl -e '
      use Socket;
      socket(S, PF_INET, SOCK_STREAM, 0) or die \"\$!\";
      setsockopt(S, SOL_SOCKET, SO_REUSEADDR, 1);
      bind(S, sockaddr_in($port, INADDR_ANY)) or die \"\$!\";
      listen(S, 1) or die \"\$!\";
      my \$p = accept(C, S);
      open(my \$out, \">\", \"/netrx-$i.bin\") or die \"\$!\";
      my \$buf;
      while (my \$n = read(C, \$buf, 65536)) {
        print \$out \$buf;
      }
      close \$out;
      close C;
      close S;
    '
  "
done

sleep 1

# Run senders in parallel
declare -A TX_HASHES
pids=()
for i in $(seq 1 "$NUM_VMS"); do
  next=$(( (i % NUM_VMS) + 1 ))
  target_ip="${VM_IPS["stress-vm$next"]}"
  target_port=$((9200 + next))
  
  "$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- bash -c "
    dd if=/dev/urandom of=/netsend-$i.bin bs=1M count=20 2>/dev/null
    sha256sum /netsend-$i.bin | awk '{print \$1}' > /netsend-$i.sha256
    perl -e '
      use Socket;
      socket(S, PF_INET, SOCK_STREAM, 0) or die \"\$!\";
      connect(S, sockaddr_in($target_port, inet_aton(\"$target_ip\"))) or die \"\$!\";
      open(my \$in, \"<\", \"/netsend-$i.bin\") or die \"\$!\";
      my \$buf;
      while (my \$n = read(\$in, \$buf, 65536)) {
        send(S, \$buf, 0);
      }
      close \$in;
      close S;
    '
  " &
  pids+=($!)
done

for pid in "${pids[@]}"; do
  wait "$pid"
done

# Check received hashes
for i in $(seq 1 "$NUM_VMS"); do
  next=$(( (i % NUM_VMS) + 1 ))
  tx_hash=$("$CLI" vm exec "stress-vm$i" -state-dir "$STATE_DIR" -- cat "/netsend-$i.sha256" | tr -d '\r\n')
  rx_hash=$("$CLI" vm exec "stress-vm$next" -state-dir "$STATE_DIR" -- sh -c "sha256sum /netrx-$next.bin | awk '{print \$1}'" | tr -d '\r\n')
  echo "  stress-vm$i -> stress-vm$next: TX=$tx_hash RX=$rx_hash"
  if [ "$tx_hash" != "$rx_hash" ]; then
    echo "ERROR: Hash mismatch between stress-vm$i and stress-vm$next!"
    exit 1
  fi
done
echo "--> Inter-VM network integrity verified for all streams."

echo "--> Step 7: Concurrently deleting all VMs..."
pids=()
for i in $(seq 1 "$NUM_VMS"); do
  "$CLI" vm delete "stress-vm$i" -state-dir "$STATE_DIR" &
  pids+=($!)
done
for pid in "${pids[@]}"; do
  wait "$pid"
done

echo "=========================================================="
echo " Multi-VM Stress Test Completed Successfully!"
echo "=========================================================="
