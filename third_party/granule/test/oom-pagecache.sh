#!/usr/bin/env bash
# oom-pagecache.sh - boot a granule VM under a hard container memory limit,
# fill its RAM with page cache, then write a multi-GB disk image with
# mke2fs and cp -- the workload that had the VMM OOM-killed from the host
# while the guest saw nothing (grain's "release images" job, 2026-11-02).
#
# The container gets the limit grain's kubernetes-granule backend used to
# give a VM -- guest memory + 512 MiB, no swap -- unless LIMIT_MB says
# otherwise, and is run the way the docker backend runs one. It prints the
# container's peak memory, its oom_kill count and whether the VM survived,
# and exits non-zero if the VM was killed.
#
# Usage:
#   test/oom-pagecache.sh
#
# Environment:
#   GRANULE_IMAGE   the granule image to boot (default granule:latest)
#   MEM_MB          guest memory (default 4096)
#   LIMIT_MB        container memory limit (default MEM_MB + 512)
#   FILL_MB         how much to write and read back to fill the page cache
#                   (default MEM_MB * 5/4)
#   IMAGE_MB        size of the image mke2fs formats and cp copies
#                   (default 3072)
#   DISK            "overlay" (default): the per-VM qcow2 overlay over the
#                   image's own disk; "raw": a raw copy of that disk,
#                   bind-mounted in from WORKDIR and written through
#                   (CHV_DISK_MODE=persistent) -- the io_uring path a raw
#                   reflink overlay also takes
#   WORKDIR         where DISK=raw keeps its copy (default a mktemp -d
#                   under /var/tmp)
#   CHV_BALLOON, CHV_DISK_DIRECT
#                   passed to the VM container when set, to compare the
#                   mechanisms; unset leaves granule's defaults
#   KEEP=1          leave the container behind
set -euo pipefail

image=${GRANULE_IMAGE:-granule:latest}
mem=${MEM_MB:-4096}
limit=${LIMIT_MB:-$((mem + 512))}
fill=${FILL_MB:-$((mem * 5 / 4))}
img=${IMAGE_MB:-3072}
name=granule-oom-pagecache-$$

env_args=(-e CHV_NET_MODE=none -e CHV_MEMORY_MB="$mem" -e CHV_MEMORY_HOTPLUG=off -e CHV_CPUS=4)
for v in CHV_BALLOON CHV_DISK_DIRECT; do
	if [ -n "${!v+x}" ]; then
		env_args+=(-e "$v=${!v}")
	fi
done

work=
cleanup() {
	if [ "${KEEP:-}" != 1 ]; then
		docker rm -f "$name" >/dev/null 2>&1 || true
		if [ -n "$work" ]; then
			rm -rf "$work"
		fi
	fi
}
trap cleanup EXIT

mount_args=()
if [ "${DISK:-overlay}" = raw ]; then
	work=${WORKDIR:-$(mktemp -d /var/tmp/oom-pagecache.XXXXXX)}
	src=$(docker create "$image")
	docker cp "$src:/var/lib/granule/guest/disk.img" "$work/disk.img"
	docker rm "$src" >/dev/null
	# granule-growfs grows the guest's filesystem to the device at boot.
	truncate -s $(((fill + 2 * img + 4096) * 1024 * 1024)) "$work/disk.img"
	mount_args=(-v "$work:/data")
	env_args+=(-e CHV_DISK_MODE=persistent -e CHV_DISK_IMAGE=/data/disk.img)
else
	env_args+=(-e CHV_DISK_SIZE_MB=$((fill + 2 * img + 4096)))
fi

echo "guest ${mem} MiB, container limit ${limit} MiB (no swap), fill ${fill} MiB, image ${img} MiB"
docker run -d --name "$name" --device /dev/kvm \
	--memory "${limit}m" --memory-swap "${limit}m" \
	"${mount_args[@]}" "${env_args[@]}" "$image" >/dev/null

# The container's own cgroup, read from the host: the image carries no
# cat to read it from inside. Docker's systemd and cgroupfs drivers put
# it in different places.
id=$(docker inspect -f '{{.Id}}' "$name")
cgdir=
for d in "/sys/fs/cgroup/system.slice/docker-$id.scope" "/sys/fs/cgroup/docker/$id"; do
	if [ -d "$d" ]; then
		cgdir=$d
	fi
done
if [ -z "$cgdir" ]; then
	echo "cannot find container $id's cgroup" >&2
	docker logs "$name" 2>&1 | tail -20 >&2
	exit 2
fi
cg() { cat "$cgdir/$1" 2>/dev/null; }

for _ in $(seq 120); do
	if docker exec "$name" granule exec -- true >/dev/null 2>&1; then
		break
	fi
	sleep 1
done

# Sample the VMM's resident set and the cgroup's usage while the workload
# runs, so the result shows where the memory went and not only the peak.
(
	while sleep 5; do
		cur=$(cg memory.current) || exit 0
		file=$(cg memory.stat | awk '$1=="file"{print $2}')
		anon=$(cg memory.stat | awk '$1=="anon"{print $2}')
		shmem=$(cg memory.stat | awk '$1=="shmem"{print $2}')
		dirty=$(cg memory.stat | awk '$1=="file_dirty"{print $2}')
		printf 'sample: current=%dM anon=%dM file=%dM (shmem=%dM dirty=%dM)\n' \
			$((cur >> 20)) $((anon >> 20)) $((file >> 20)) $((shmem >> 20)) $((dirty >> 20))
	done
) &
sampler=$!

start=$(date +%s)
set +e
docker exec "$name" granule exec -- sh -euc "
	cd /root
	echo 'filling the page cache: writing and reading ${fill} MiB'
	dd if=/dev/zero of=fill bs=1M count=${fill} status=none
	cat fill > /dev/null
	grep -E '^(MemFree|Cached|Dirty):' /proc/meminfo
	rm fill
	fstrim / || true
	echo 'writing a ${img} MiB image with mke2fs and cp'
	truncate -s ${img}M disk.img
	mke2fs -q -F -t ext4 -E lazy_itable_init=0,lazy_journal_init=0,nodiscard disk.img
	dd if=/dev/zero of=disk.img bs=1M count=${img} conv=notrunc status=none
	cp disk.img disk2.img
	sync
	grep -E '^(MemFree|Cached|Dirty):' /proc/meminfo
	rm -f disk.img disk2.img
"
rc=$?
set -e
end=$(date +%s)
kill "$sampler" 2>/dev/null || true

peak=$(cg memory.peak || echo 0)
ooms=$(cg memory.events | awk '$1=="oom_kill"{print $2}')
# The cgroup goes with the container, so read it before anything removes
# it; a VMM that was killed takes the container with it, but docker
# keeps the cgroup until the container is removed.
echo "exec exit status: ${rc} after $((end - start))s"
echo "container peak: $((peak >> 20)) MiB of ${limit} MiB; memory.events oom_kill=${ooms:-?}"
if [ "$rc" -ne 0 ] || [ "${ooms:-0}" != 0 ]; then
	echo "FAIL: the VM did not survive (see above)"
	docker logs "$name" 2>&1 | tail -5
	exit 1
fi
echo "PASS: the VM survived"
