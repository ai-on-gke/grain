#!/usr/bin/env bash
#
# gce-bench.sh: run benchmarks/storage/run_storage_benchmark.py --live on a
# dedicated Google Compute Engine VM.
#
# Storage numbers taken in a grain sandbox or on a shared cluster node are
# mostly a measurement of whoever else is on the disk. This creates one
# n2-standard-16 VM with nested virtualization and a pd-ssd boot disk for
# the length of one run, builds granule's debian guest on it (with fio
# installed in the guest), derives the extent-backed images from that
# guest, and runs the whole storage comparison there: native host, flat
# disk.img, QCOW2 64 KiB and 2 MiB, boot-assembled extents and the runtime
# vhost-user-blk extent backend at 16 and 64 MiB in both disk modes.
#
# Modeled on test/gce-build.sh, which documents the VM handling in full:
# the VM is deleted when this script exits however it exits, and created
# with --max-run-duration so GCE deletes it even if this script is killed
# before its trap runs.
#
# The results are printed between STORAGE-BENCH-JSON-BEGIN/END and
# STORAGE-BENCH-MARKDOWN-BEGIN/END markers, so a CI log is enough to
# harvest them; with --out DIR they are also copied back to DIR.
#
# Usage: benchmarks/storage/gce-bench.sh [--keep] [--out DIR] [-- run_storage_benchmark.py args]
#
# Environment: as test/gce-build.sh (GCE_PROJECT, GCE_ZONE, GCE_NETWORK,
# GCE_KEY_FILE, GCE_IMAGE_FAMILY, GCE_IMAGE_PROJECT), plus
#   GCE_MACHINE_TYPE  machine type (default n2-standard-16; it has to
#                     support nested virtualization, which e2 does not)
#   GCE_DISK_GB       pd-ssd boot disk size (default 100)
#   GCE_MAX_RUN       --max-run-duration (default 2h)
#   GUEST_DISK_EXTRA_MB  headroom baked into the guest disk (default 5120):
#                     the 4 GiB fio dataset has to fit in persistent mode,
#                     which cannot be resized at boot

set -euo pipefail

keep=false
out=
bench_args=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --keep) keep=true ;;
    --out) out=$2; shift ;;
    --) shift; bench_args=("$@"); break ;;
    -h|--help) sed -n '3,/^$/s/^# \{0,1\}//p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

zone=${GCE_ZONE:-us-central1-a}
machine_type=${GCE_MACHINE_TYPE:-n2-standard-16}
disk_gb=${GCE_DISK_GB:-100}
image_family=${GCE_IMAGE_FAMILY:-debian-12}
image_project=${GCE_IMAGE_PROJECT:-debian-cloud}
network=${GCE_NETWORK:-default}
max_run=${GCE_MAX_RUN:-2h}
extra_mb=${GUEST_DISK_EXTRA_MB:-5120}

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

log() { printf '[gce-bench %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { echo "::error::$*" >&2; exit 1; }

for cmd in gcloud ssh ssh-keygen tar git; do
  command -v "$cmd" >/dev/null || die "missing required command: $cmd"
done

# Credentials: see test/gce-build.sh for why a key file wins over whatever
# gcloud already reports as active.
key_file=${GCE_KEY_FILE:-${GOOGLE_APPLICATION_CREDENTIALS:-/home/debian/.gcp-service-account.json}}
if [[ -r $key_file ]]; then
  if ! o=$(gcloud auth activate-service-account --key-file="$key_file" --quiet 2>&1); then
    printf '%s\n' "$o" >&2
    die "could not activate the service-account key at $key_file"
  fi
  project=${GCE_PROJECT:-$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["project_id"])' "$key_file")}
elif [[ -z $(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null) ]]; then
  die "gcloud has no active account and there is no service-account key at $key_file (set GCE_KEY_FILE)"
fi
project=${project:-${GCE_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}}
[[ -n $project ]] || die "no GCP project: set GCE_PROJECT"
gc=(gcloud --project "$project" --quiet)

commit=$(git rev-parse HEAD)
name="granule-bench-${commit:0:10}-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"

work=$(mktemp -d)
created=false
ip=
cleanup() {
  local rc=$?
  if [[ $created == true ]]; then
    if [[ $keep == true ]]; then
      log "--keep: leaving $name in $zone (ssh -i $work/id granule@${ip:-?}; GCE deletes it after $max_run)"
    else
      log "deleting $name"
      "${gc[@]}" compute instances delete "$name" --zone "$zone" >/dev/null 2>&1 ||
        echo "::warning::could not delete $name in $zone; GCE will delete it after $max_run" >&2
    fi
  fi
  [[ $keep == true ]] || rm -rf "$work"
  exit $rc
}
trap cleanup EXIT
trap 'exit 130' INT TERM

ssh-keygen -q -t ed25519 -N '' -C "$name" -f "$work/id"
printf 'granule:%s\n' "$(cat "$work/id.pub")" >"$work/ssh-keys"

log "creating $name ($machine_type, nested virtualization, ${disk_gb}GB pd-ssd) in $project/$zone"
created=true
ip=$("${gc[@]}" compute instances create "$name" \
  --zone "$zone" \
  --machine-type "$machine_type" \
  --enable-nested-virtualization \
  --image-family "$image_family" \
  --image-project "$image_project" \
  --boot-disk-size "${disk_gb}GB" \
  --boot-disk-type pd-ssd \
  --network "$network" \
  --no-service-account --no-scopes \
  --max-run-duration "$max_run" \
  --instance-termination-action DELETE \
  --labels "purpose=granule-storage-bench,commit=$commit" \
  --metadata block-project-ssh-keys=TRUE \
  --metadata-from-file ssh-keys="$work/ssh-keys" \
  --format 'value(networkInterfaces[0].accessConfigs[0].natIP)' 2> >(grep -v -e 'larger than image size' -e 'resize the root' -e 'generated warnings' -e '^$' >&2))
[[ -n $ip ]] || die "$name has no external address to reach it on"

ssh_opts=(-i "$work/id" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
  -o LogLevel=ERROR -o ConnectTimeout=10 -o ServerAliveInterval=30 -o ServerAliveCountMax=10)
vm() { ssh "${ssh_opts[@]}" "granule@$ip" "$@"; }

log "waiting for ssh on $ip"
for i in $(seq 1 60); do
  vm true 2>/dev/null && break
  [[ $i -lt 60 ]] || die "$name never answered on $ip:22"
  sleep 5
done
vm 'echo "vm: $(. /etc/os-release && echo "$PRETTY_NAME"), kernel $(uname -r), $(nproc) cpus, $(free -g | awk "/Mem:/ {print \$2}")G ram"; ls -l /dev/kvm'

# cloud-hypervisor itself ships inside the granule image (the Dockerfile's
# fetch-chv stage); the host needs docker to run it, fio for the native
# baseline and qemu-img for the 64 KiB QCOW2 overlay granule does not make.
log "installing docker, fio and qemu-utils"
vm 'set -euo pipefail
  apt() { sudo DEBIAN_FRONTEND=noninteractive apt-get "$@"; }
  apt update -qq
  apt install -y -qq ca-certificates curl git fio qemu-utils python3 >/dev/null
  sudo install -m 0755 -d /etc/apt/keyrings
  sudo curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" |
    sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  apt update -qq
  apt install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin >/dev/null
  docker --version; fio --version; qemu-img --version | head -n 1'

log "copying the tree at ${commit:0:12}"
{ git ls-files -z --cached --others --exclude-standard; printf '.git\0'; } |
  tar --null -T - -czf - | vm 'mkdir -p granule && tar -xzf - -C granule'

# The guest: granule's own debian guest built from this tree (granule-shim,
# granule and cloud-hypervisor included), with fio installed into the guest
# rootfs and enough headroom for the dataset.
log "building the debian guest with fio (GUEST_DISK_EXTRA_MB=$extra_mb)"
start=$(date +%s)
if ! vm "cd granule && sudo docker build --file Dockerfile --tag granule-bench:flat \
    --build-arg GUEST_DISTRO=debian --build-arg GUEST_DISK_EXTRA_MB=$extra_mb \
    --build-arg GUEST_SETUP_SCRIPT='apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends fio' \
    . >../build.log 2>&1"; then
  vm 'tail -n 150 build.log' >&2 || true
  die "building the guest failed after $(( ($(date +%s) - start) / 60 ))m"
fi
log "guest built in $(( ($(date +%s) - start) / 60 ))m"

log "deriving the extent images"
vm 'set -euo pipefail
  cd granule
  # The build stage of the main Dockerfile, cached from the guest build:
  # it holds the full granule CLI, which "granule guest split" needs.
  sudo docker build --target build -t granule-bench:build . >../derive.log 2>&1 || { tail -n 50 ../derive.log; exit 1; }
  b() { sudo docker build -f benchmarks/storage/Dockerfile.extents --build-arg BASE=granule-bench:flat --build-arg BUILD=granule-bench:build "$@" . >>../derive.log 2>&1 || { tail -n 50 ../derive.log; exit 1; }; }
  b --target extent-boot --build-arg EXTENT_SIZE_MB=64 -t granule-bench:extent-boot-64
  b --target extents --build-arg EXTENT_SIZE_MB=64 -t granule-bench:extents-64
  b --target extents --build-arg EXTENT_SIZE_MB=16 -t granule-bench:extents-16
  b --target tools --build-arg EXTENT_SIZE_MB=64 -t granule-bench:tools
  sudo docker run --rm granule-bench:tools cat /out/disk.size >../disk.size
  sudo docker images --format "{{.Repository}}:{{.Tag}} {{.Size}}" | grep granule-bench
  echo "guest disk.img: $(cat ../disk.size) bytes"'

log "running the storage benchmark"
start=$(date +%s)
vm "cd granule && sudo python3 -u benchmarks/storage/run_storage_benchmark.py --live \
  --disk-bytes \$(cat ../disk.size) --out-dir ../bench-results ${bench_args[*]:-}" || die "run_storage_benchmark.py failed after $(( ($(date +%s) - start) / 60 ))m"
log "benchmark finished in $(( ($(date +%s) - start) / 60 ))m"

if [[ -n $out ]]; then
  mkdir -p "$out"
  vm 'tar -czf - -C bench-results .' | tar -xzf - -C "$out"
  log "results copied to $out"
fi
