#!/bin/bash
# Times pod creation to guest-ready for granule's "run" mode running as a
# real GKE pod, using pod.template.yaml.
#
# Deliberately avoids polling the Kubernetes API in a tight loop to
# determine readiness: a kubectl client that isn't co-located with the
# control plane can see multi-second stalls on individual calls, which
# would swamp the few-second latencies this is trying to measure. Instead
# each iteration creates the pod once, sleeps a fixed window, then makes a
# single `kubectl logs --timestamps` call and diffs the BOOT_COMPLETE
# line's own timestamp (recorded by containerd on the node) against the
# client-side time the create was issued. Both are real UTC on GCP
# infrastructure, so the diff is accurate without needing more round
# trips than that.
#
# Usage: NAME_PREFIX=... IMAGE=... run.sh [iterations]
set -u

N="${1:-8}"
NAME_PREFIX="${NAME_PREFIX:?set NAME_PREFIX, e.g. chv-bench-<agent-id>}"
POD_TEMPLATE="${POD_TEMPLATE:-$(dirname "$0")/pod.rendered.yaml}"
WORK_DIR="${WORK_DIR:-/tmp/granule-bench-gke}"

mkdir -p "$WORK_DIR"
OUT="$WORK_DIR/results.csv"
echo "iter,create_to_ready_s" > "$OUT"

kubectl_retry() {
  for _ in 1 2 3 4 5; do
    if out=$(timeout 15 kubectl "$@" 2>&1); then
      echo "$out"
      return 0
    fi
    sleep 2
  done
  echo "$out" >&2
  return 1
}

for i in $(seq 1 "$N"); do
  name="${NAME_PREFIX}-${i}"
  sed "s/NAME_PLACEHOLDER/$name/" "$POD_TEMPLATE" > "$WORK_DIR/pod-$i.yaml"

  start_iso=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)
  kubectl_retry create --request-timeout=10s -f "$WORK_DIR/pod-$i.yaml" > /dev/null

  sleep 12

  logs=$(kubectl_retry logs --timestamps --request-timeout=10s "$name")
  ready_iso=$(echo "$logs" | grep "BOOT_COMPLETE" | head -1 | awk '{print $1}')

  if [ -z "$ready_iso" ]; then
    echo "$i,FAILED" >> "$OUT"
    echo "iter $i: no BOOT_COMPLETE seen" >&2
  else
    start_s=$(date -u -d "$start_iso" +%s.%N)
    ready_s=$(date -u -d "$ready_iso" +%s.%N)
    delta=$(echo "scale=4; $ready_s - $start_s" | bc)
    echo "$i,$delta" >> "$OUT"
    echo "iter $i: create_to_ready=${delta}s"
  fi

  kubectl_retry delete pod "$name" --wait=false > /dev/null 2>&1
done

echo "--- results ($OUT) ---"
cat "$OUT"
