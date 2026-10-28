#!/usr/bin/env bash
#
# gce-build.sh: build granule's images on a fresh Google Compute Engine VM.
#
# Every other build of these images that CI does happens in a grain
# sandbox, and a sandbox is a peculiar machine: its DNS, its registry
# mirror, its CA and its docker daemon are all grain's. A build that only
# ever runs there can grow to depend on any of that without anything
# noticing, and the first person to find out is the one running
# deploy/publish-images.sh on an ordinary machine. This script is that
# ordinary machine: a stock Debian image on GCE, docker installed the way
# Docker's own documentation installs it, the tree copied over, and
# deploy/publish-images.sh --no-push run on it exactly as a person would.
#
# The VM is created for one run and deleted when the script exits, however
# it exits. A run killed before its trap fires (a CI job cancelled at its
# timeout, a sandbox destroyed under it) cannot delete anything, so the VM
# is also created with --max-run-duration and GCE deletes it itself once
# the run could not possibly still be going.
#
# Usage: test/gce-build.sh [--keep] [image ...]
#
#   image    what deploy/publish-images.sh builds: "guests" (the default),
#            or any of the guest variants it names (debian alpine debian12).
#   --keep   leave the VM running afterwards, for poking at a failure. It
#            still deletes itself at --max-run-duration.
#
# Environment:
#   GCE_PROJECT       project to create the VM in (default: the active gcloud
#                     project, else the service-account key's project_id)
#   GCE_ZONE          zone (default us-central1-a)
#   GCE_MACHINE_TYPE  machine type (default e2-standard-8)
#   GCE_DISK_GB       boot disk size (default 100)
#   GCE_IMAGE_FAMILY  boot image family (default debian-12)
#   GCE_IMAGE_PROJECT project of that family (default debian-cloud)
#   GCE_NETWORK       VPC network (default "default"); it has to let this
#                     machine reach the VM's external address on port 22
#   GCE_MAX_RUN       --max-run-duration for the VM (default 2h)
#   GCE_KEY_FILE      service-account key to activate when gcloud has no
#                     active account (default $GOOGLE_APPLICATION_CREDENTIALS,
#                     else /home/debian/.gcp-service-account.json)

set -euo pipefail

keep=false
images=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --keep) keep=true ;;
    -h|--help) sed -n '3,/^$/s/^# \{0,1\}//p' "$0"; exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) images+=("$1") ;;
  esac
  shift
done
[[ ${#images[@]} -gt 0 ]] || images=(guests)

zone=${GCE_ZONE:-us-central1-a}
machine_type=${GCE_MACHINE_TYPE:-e2-standard-8}
disk_gb=${GCE_DISK_GB:-100}
image_family=${GCE_IMAGE_FAMILY:-debian-12}
image_project=${GCE_IMAGE_PROJECT:-debian-cloud}
network=${GCE_NETWORK:-default}
max_run=${GCE_MAX_RUN:-2h}

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

log() { printf '[gce-build %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { echo "::error::$*" >&2; exit 1; }

for cmd in gcloud ssh ssh-keygen tar git; do
  command -v "$cmd" >/dev/null || die "missing required command: $cmd"
done

# Credentials. A machine that is already logged in keeps its login; one
# that is not (a CI runner, a fresh sandbox) gets the service-account key.
key_file=${GCE_KEY_FILE:-${GOOGLE_APPLICATION_CREDENTIALS:-/home/debian/.gcp-service-account.json}}
if [[ -z $(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null) ]]; then
  [[ -r $key_file ]] || die "gcloud has no active account and there is no service-account key at $key_file (set GCE_KEY_FILE)"
  gcloud auth activate-service-account --key-file="$key_file" --quiet >/dev/null 2>&1 ||
    die "could not activate the service-account key at $key_file"
fi
project=${GCE_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}
if [[ -z $project && -r $key_file ]]; then
  project=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["project_id"])' "$key_file")
fi
[[ -n $project ]] || die "no GCP project: set GCE_PROJECT"
gc=(gcloud --project "$project" --quiet)

commit=$(git rev-parse HEAD)
# GCE names: lowercase, start with a letter, at most 63 characters.
name="granule-build-${commit:0:10}-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"

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

# A key for this run only, put on this instance only: nothing is added to
# the project's metadata (which is what `gcloud compute ssh` would do), and
# block-project-ssh-keys keeps the project's keys off a machine that has no
# business with them.
ssh-keygen -q -t ed25519 -N '' -C "$name" -f "$work/id"
printf 'granule:%s\n' "$(cat "$work/id.pub")" >"$work/ssh-keys"

log "creating $name ($machine_type, $image_project/$image_family, ${disk_gb}GB) in $project/$zone"
created=true
ip=$("${gc[@]}" compute instances create "$name" \
  --zone "$zone" \
  --machine-type "$machine_type" \
  --image-family "$image_family" \
  --image-project "$image_project" \
  --boot-disk-size "${disk_gb}GB" \
  --boot-disk-type pd-balanced \
  --network "$network" \
  --no-service-account --no-scopes \
  --max-run-duration "$max_run" \
  --instance-termination-action DELETE \
  --labels "purpose=granule-gce-build,commit=$commit" \
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
vm 'echo "vm: $(. /etc/os-release && echo "$PRETTY_NAME"), kernel $(uname -r), $(nproc) cpus, $(free -g | awk "/Mem:/ {print \$2}")G ram"'

# Docker the way https://docs.docker.com/engine/install/debian/ installs it,
# which is what somebody standing up a build machine of their own does --
# not whatever docker the grain sandbox image happened to bake in.
log "installing docker and git"
vm 'set -euo pipefail
  apt() { sudo DEBIAN_FRONTEND=noninteractive apt-get "$@"; }
  apt update -qq
  apt install -y -qq ca-certificates curl git >/dev/null
  sudo install -m 0755 -d /etc/apt/keyrings
  sudo curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" |
    sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  apt update -qq
  apt install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin >/dev/null
  sudo usermod -aG docker "$USER"
  docker --version'

# The tree as it stands, .git included: publish-images.sh names the commit
# in its tags and asks git whether the tree is dirty. Anything ignored
# (build output, a stray granule binary) stays behind.
log "copying the tree at ${commit:0:12}"
{ git ls-files -z --cached --others --exclude-standard; printf '.git\0'; } |
  tar --null -T - -czf - | vm 'mkdir -p granule && tar -xzf - -C granule'

# A tree with changes nobody has committed yet is what a person trying this
# by hand usually has; publish-images.sh refuses one unless told otherwise.
publish=(deploy/publish-images.sh --no-push --no-login)
[[ -z $(git status --porcelain) ]] || publish+=(--allow-dirty)
publish+=("${images[@]}")

# The build's own output is tens of thousands of lines of docker progress,
# so it is kept on the VM and only its tail printed when it is what a reader
# needs, which is when the build failed. A new ssh session, so the docker
# group membership above is in effect.
log "building: ${publish[*]}"
vm 'sudo cp /etc/hosts /tmp/hosts.before'
start=$(date +%s)
if ! vm "cd granule && ${publish[*]} >../build.log 2>&1"; then
  vm 'tail -n 200 build.log' >&2 || true
  die "deploy/publish-images.sh failed on a stock $image_family GCE VM after $(( ($(date +%s) - start) / 60 ))m -- the end of its output is above"
fi
vm 'grep -E "^(== |publishing to|warning:)" build.log' || true
log "built in $(( ($(date +%s) - start) / 60 ))m"

# publish-images.sh carries a block of grain-sandbox plumbing -- a
# nameserver, /etc/hosts entries for ghcr.io and github.com, a CA and a
# registry mirror -- which it applies only inside a grain sandbox. On this
# VM it has to leave the machine alone: run here, it once pointed ghcr.io
# and github.com at the metadata server, which a build never notices and a
# push from this VM would. So any change to /etc/hosts, or the script saying
# it took this for a grain sandbox, fails the run.
if ! vm 'diff -u /tmp/hosts.before /etc/hosts' >"$work/hosts.diff"; then
  cat "$work/hosts.diff"
  die "deploy/publish-images.sh rewrote /etc/hosts on an ordinary GCE VM (above): that is grain-sandbox plumbing, and it points ghcr.io and github.com somewhere they are not on this machine"
fi
if vm 'grep -q "^grain sandbox:" build.log'; then
  die "deploy/publish-images.sh took an ordinary GCE VM for a grain sandbox and rewrote its DNS, CA and registry config"
fi

# What came out, read back from the VM's own docker rather than inferred
# from the script having exited 0: every image publish-images.sh says it
# built has to be there.
vm 'set -euo pipefail
  built=$(sed -n "/^built (not pushed):/,\$p" build.log | sed -n "s/^  //p")
  [ -n "$built" ] || { echo "::error::publish-images.sh listed no images it built" >&2; exit 1; }
  for image in $built; do
    size=$(docker image inspect -f "{{.Size}}" "$image") || { echo "::error::$image was reported built but is not in docker" >&2; exit 1; }
    printf "  %-75s %5d MB\n" "$image" $((size / 1000000))
  done'
log "ok: granule's images build on a stock $image_family GCE VM"
