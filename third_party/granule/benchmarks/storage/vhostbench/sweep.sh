#!/usr/bin/env bash
# Random-read sweep for one vhost-user-blk backend, or for the host's own
# io_uring path as the ceiling. Prints one JSON line per point.
#
#   sweep.sh go   <vhostbench> <data dir>      # pkg/extents server, overlay over img.extents
#   sweep.sh rust <rust server binary> <data dir>
#   sweep.sh host - <data dir>                 # fio, io_uring, on the flat disk.img
#
# The data dir comes from `vhostbench mkdata -flat disk.img -dir img.extents`.
# "cold" points drop the host page cache first; "warm" points read it twice.
# Needs passwordless sudo for drop_caches.
set -euo pipefail
kind=$1 bin=$2 data=$3
DUR=${DUR:-8s}
QUEUES=${QUEUES:-4}
VB=${VB:-$(dirname "$0")/../../../vhostbench}
sock=$(mktemp -u /tmp/vhostbench-XXXX.sock)

start_server() {
	case $kind in
	go)
		rm -rf "$data/ovl"
		"$bin" serve -dir "$data/ovl" -base "$data/img.extents" -socket "$sock" -queues "$QUEUES" ${SERVE_FLAGS:-} >/tmp/vhostbench-server.log 2>&1 &
		;;
	rust)
		"$bin" --dir "$data/img.extents" --socket "$sock" --queues "$QUEUES" >/tmp/vhostbench-server.log 2>&1 &
		;;
	esac
	pid=$!
	for _ in $(seq 100); do [[ -S $sock ]] && break; sleep 0.05; done
}

stop_server() {
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	rm -f "$sock"
}

drop() { sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null; }

for cache in cold warm; do
	for bs in 4096 65536; do
		for shape in "1 1" "1 32" "4 32"; do
			read -r q qd <<<"$shape"
			label="$kind $cache bs=$bs jobs=$q qd=$qd"
			if [[ $kind == host ]]; then
				[[ $cache == cold ]] && drop
				[[ $cache == warm ]] && cat "$data/disk.img" >/dev/null
				fio --name=r --filename="$data/disk.img" --rw=randread --bs="$bs" --ioengine=io_uring \
					--iodepth="$qd" --numjobs="$q" --group_reporting --runtime="${DUR%s}" --time_based \
					--output-format=json | python3 -c '
import json,sys
j=json.load(sys.stdin)["jobs"][0]; r=j["read"]; p=r["clat_ns"]["percentile"]
print(json.dumps({"label":sys.argv[1],"result":{"iops":r["iops"],"p50_us":p["50.000000"]/1e3,"p99_us":p["99.000000"]/1e3,"host_cpu_pct":j["usr_cpu"]+j["sys_cpu"]}}))' "$label"
				continue
			fi
			start_server
			[[ $cache == cold ]] && drop
			[[ $cache == warm ]] && cat "$data"/img.extents/extents/* >/dev/null
			"$VB" run -socket "$sock" -queues "$q" -qd "$qd" -bs "$bs" -dur "$DUR" -span 4G -pid "$pid" -label "$label"
			stop_server
		done
	done
done
