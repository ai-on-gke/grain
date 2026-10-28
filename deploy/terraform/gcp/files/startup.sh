#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# GCE startup script. Runs as root on every boot, and must be idempotent.
#
# Mirrors v1's own startup script's shape exactly: mount the
# disks, install and enable config-sync, and nothing else. All the
# real work is in deploy.sh, which config-sync fetches from instance
# metadata so it can change without recreating the instance.
#
# Two disks, for two different reasons:
#
#   grain-data     /var/lib/grain/data, the state a redeploy must not
#                  lose and the one directory to back up (the SQLite
#                  store, the secrets database). Small, and carries
#                  prevent_destroy in instance.tf.
#   grain-sandbox  /mnt/grain-sandbox, everything a *sandbox* writes:
#                  docker's data root (the sandbox image and the qcow2
#                  overlay each granule VM's container writes its root
#                  filesystem into) and grain's scratch space, mounted
#                  at /var/lib/grain/scratch (per-run checkouts, caches).
#                  Nothing here is worth keeping; the point is that the
#                  disk a runaway task fills is not the one the OS, the
#                  journal, the store and the deploy itself need to write
#                  to. See variables.tf's own sandbox_disk_gb. Never
#                  back it up.
#
# /var/lib/grain itself stays on the boot disk, root-owned, holding
# nothing but those two mount points.
set -euo pipefail

readonly MD="http://metadata.google.internal/computeMetadata/v1"
readonly DATA_DEV="/dev/disk/by-id/google-grain-data"
readonly DATA_MNT="/var/lib/grain/data"
readonly SANDBOX_DEV="/dev/disk/by-id/google-grain-sandbox"
readonly SANDBOX_MNT="/mnt/grain-sandbox"
# Docker's data root, and so where the sandbox image and every VM's
# overlay actually land. Deliberately *not* under SANDBOX_WORK_DIR:
# orchestrator.HostSandboxes.ReapOrphans removes everything under its own
# base directory at startup, which would take docker's whole store with
# it.
readonly SANDBOX_DOCKER_DIR="$SANDBOX_MNT/docker"
readonly SANDBOX_CONTAINERD_DIR="$SANDBOX_MNT/containerd"
readonly SANDBOX_WORK_DIR="$SANDBOX_MNT/sandboxes"
# scripts/setup.sh's own $GRAIN_SANDBOX_DIR default (scratch/), bind-mounted
# onto SANDBOX_WORK_DIR so the daemon needs no override to find its per-run
# checkouts and caches on this disk.
readonly HOST_SANDBOX_MNT="/var/lib/grain/scratch"
readonly DOCKER_CONF="/etc/docker/daemon.json"
readonly DOCKER_DROPIN_DIR="/etc/systemd/system/docker.service.d"
readonly DOCKER_DROPIN="$DOCKER_DROPIN_DIR/grain-sandbox-disk.conf"
readonly CONTAINERD_CONF="/etc/containerd/config.toml"
readonly CONTAINERD_DROPIN_DIR="/etc/systemd/system/containerd.service.d"
readonly CONTAINERD_DROPIN="$CONTAINERD_DROPIN_DIR/grain-sandbox-disk.conf"
# config-sync.sh's record of the last generation it deployed. Removing it
# is what asks for a deploy that metadata alone would not trigger.
readonly DEPLOY_STATE="$DATA_MNT/.deploy-state"
readonly SYNC="/opt/grain-deploy/config-sync.sh"

log() { echo "grain-startup: $*"; }

md() { curl -fsS -H "Metadata-Flavor: Google" "$MD/$1"; }

# Empty rather than fatal for an attribute that may not be set at all --
# an instance created before this script knew to ask has no
# grain-sandbox-disk key, and `set -e` would otherwise take the whole
# boot down over a missing one.
md_optional() { curl -fsS -H "Metadata-Flavor: Google" "$MD/$1" 2>/dev/null || true; }

# An attached disk shows up under a name GCE assigns in attachment order,
# which nothing here controls; the /dev/disk/by-id link derived from
# attached_disk's own device_name (instance.tf) is the stable one. It
# appears a moment after boot rather than at it, hence the wait.
wait_for_disk() {
  local dev="$1"
  for _ in $(seq 1 60); do
    if [ -b "$dev" ]; then return 0; fi
    sleep 1
  done
  return 1
}

# format_and_mount formats a blank disk, records it in /etc/fstab by
# UUID and mounts it. Idempotent in both halves: a disk that already
# carries a filesystem is never reformatted, and a UUID already in fstab
# is not added a second time.
#
# Every step that can fail returns rather than relying on `set -e`: this
# is called from an `if` for the sandbox disk (whose failure is survivable
# -- see mount_sandbox_disk), and inside a condition `set -e` is
# suspended for the whole call, so an unchecked mkfs failure would carry
# on to blkid and record an empty UUID in fstab.
format_and_mount() {
  local dev="$1" mnt="$2" uuid fstype

  if ! blkid -o value -s TYPE "$dev" >/dev/null 2>&1; then
    log "formatting $dev (first boot)"
    if ! command -v mkfs.xfs >/dev/null 2>&1; then
      apt-get update && apt-get install -y xfsprogs || return 1
    fi
    mkfs.xfs -f "$dev" || return 1
  fi

  uuid="$(blkid -o value -s UUID "$dev")" || return 1
  [ -n "$uuid" ] || return 1
  fstype="$(blkid -o value -s TYPE "$dev")" || fstype="xfs"
  mkdir -p "$mnt" || return 1
  if ! grep -q "^UUID=$uuid " /etc/fstab; then
    log "adding $mnt to /etc/fstab"
    echo "UUID=$uuid $mnt $fstype defaults,discard,nofail 0 2" >> /etc/fstab
  fi
  if ! mountpoint -q "$mnt"; then
    mount "$mnt" || return 1
  fi
  log "$mnt mounted ($(df -h --output=size,avail "$mnt" | tail -1 | tr -s ' '))"
}

mount_data_disk() {
  if ! wait_for_disk "$DATA_DEV"; then
    log "FATAL: $DATA_DEV never appeared; is the data disk attached?"
    exit 1
  fi
  if ! format_and_mount "$DATA_DEV" "$DATA_MNT"; then
    log "FATAL: could not mount $DATA_DEV at $DATA_MNT"
    exit 1
  fi
}

# mount_sandbox_disk brings up the sandbox volume and points the two
# things that fill a disk at it. Unlike the data disk, a failure here is
# not fatal: nothing on this volume is state, so a host that cannot mount
# it still deploys and still runs tasks -- on the boot disk, which is the
# arrangement this exists to improve on rather than a broken one. It says
# so loudly instead.
#
# Returns non-zero when the volume is not in use, which is what tells the
# caller to undo any configuration a previous boot left behind.
mount_sandbox_disk() {
  if [ "$(md_optional instance/attributes/grain-sandbox-disk)" != "true" ]; then
    log "no sandbox disk for this deployment (sandbox_disk_gb = 0); docker's data root and sandbox checkouts stay on the boot disk"
    return 1
  fi
  if ! wait_for_disk "$SANDBOX_DEV"; then
    log "WARNING: $SANDBOX_DEV never appeared though this deployment declares one; carrying on with docker's data root and sandbox checkouts on the boot disk"
    return 1
  fi

  if ! format_and_mount "$SANDBOX_DEV" "$SANDBOX_MNT"; then
    log "WARNING: could not mount $SANDBOX_DEV at $SANDBOX_MNT; carrying on with docker's data root and sandbox checkouts on the boot disk"
    return 1
  fi
  install -d -m 0710 "$SANDBOX_DOCKER_DIR" || return 1
  install -d -m 0710 "$SANDBOX_CONTAINERD_DIR" || return 1
  install -d -m 0755 "$SANDBOX_WORK_DIR" || return 1
  if ! bind_sandbox_dir; then
    log "WARNING: could not bind-mount $SANDBOX_WORK_DIR onto $HOST_SANDBOX_MNT; a host-directory sandbox's checkout stays on the boot disk"
  fi
  return 0
}

# bind_sandbox_dir puts SANDBOX_WORK_DIR at the path the daemon already
# looks in -- scripts/setup.sh's GRAIN_SANDBOX_DIR default, which
# grain-daemon.service both passes as -sandbox-dir and bind-mounts into
# the container. A bind mount rather than a GRAIN_SANDBOX_DIR override
# threaded through grain-config and deploy.sh: the mount is this file's
# business either way, and one place deciding where the volume goes is
# one place to keep in step.
#
# Anything a previous deployment left in that directory on the boot
# disk is shadowed by this, not deleted -- unmount it to reach it,
# and it is a per-run checkout, so it is safe to delete once nothing is
# dispatched into it.
bind_sandbox_dir() {
  mkdir -p "$HOST_SANDBOX_MNT" || return 1
  if ! grep -q " $HOST_SANDBOX_MNT " /etc/fstab; then
    log "bind-mounting $SANDBOX_WORK_DIR onto $HOST_SANDBOX_MNT"
    echo "$SANDBOX_WORK_DIR $HOST_SANDBOX_MNT none bind,nofail 0 0" >> /etc/fstab
  fi
  if ! mountpoint -q "$HOST_SANDBOX_MNT"; then
    mount "$HOST_SANDBOX_MNT" || return 1
  fi
}

# use_sandbox_disk_for_docker moves docker's data root and containerd's root
# onto the sandbox volume, which is what actually gets the sandbox image and
# every VM's disk overlay off the boot disk: granule gives each VM a writable
# root as a qcow2 created *inside* its own container (bwsalmon/granule#37), so
# every byte a task's guest writes to its root filesystem lands in
# docker's/containerd's storage rather than in any directory this script could
# mount on its own.
#
# The drop-ins are the other half, and the more important one: with
# RequiresMountsFor, dockerd and containerd do not start at all until the volume
# is mounted. Without it a boot that raced the mount would have daemons
# create roots on the boot disk and then have the mount hide it
# -- the failure this whole arrangement is meant to prevent, arrived at
# invisibly.
use_sandbox_disk_for_docker() {
  local want moved=0
  want="$(printf '{\n  "data-root": "%s"\n}\n' "$SANDBOX_DOCKER_DIR")"

  install -d -m 0755 /etc/docker
  if [ ! -f "$DOCKER_CONF" ] || [ "$(cat "$DOCKER_CONF")" != "$want" ]; then
    # Kept, once, rather than silently discarded: this module owns
    # docker's configuration on this host, but an operator who put
    # something here by hand should be able to get it back.
    if [ -f "$DOCKER_CONF" ] && [ ! -f "$DOCKER_CONF.pre-grain" ]; then
      log "WARNING: replacing an existing $DOCKER_CONF; the previous one is at $DOCKER_CONF.pre-grain"
      cp "$DOCKER_CONF" "$DOCKER_CONF.pre-grain"
    fi
    log "pointing docker's data root at $SANDBOX_DOCKER_DIR"
    printf '%s\n' "$want" > "$DOCKER_CONF"
    moved=1
  fi

  install -d -m 0755 "$DOCKER_DROPIN_DIR"
  if ! grep -qs "RequiresMountsFor=$SANDBOX_MNT" "$DOCKER_DROPIN"; then
    cat > "$DOCKER_DROPIN" <<UNIT
[Unit]
# Written by terraform/gcp/files/startup.sh: docker's data root is on the
# sandbox volume, so dockerd must not start before it is mounted.
RequiresMountsFor=$SANDBOX_MNT
UNIT
    systemctl daemon-reload
    moved=1
  fi

  local containerd_want
  containerd_want="$(printf 'root = "%s"\n' "$SANDBOX_CONTAINERD_DIR")"

  install -d -m 0755 /etc/containerd
  if [ ! -f "$CONTAINERD_CONF" ] || [ "$(cat "$CONTAINERD_CONF")" != "$containerd_want" ]; then
    if [ -f "$CONTAINERD_CONF" ] && [ ! -f "$CONTAINERD_CONF.pre-grain" ]; then
      log "WARNING: replacing an existing $CONTAINERD_CONF; the previous one is at $CONTAINERD_CONF.pre-grain"
      cp "$CONTAINERD_CONF" "$CONTAINERD_CONF.pre-grain"
    fi
    log "pointing containerd's root at $SANDBOX_CONTAINERD_DIR"
    printf '%s\n' "$containerd_want" > "$CONTAINERD_CONF"
    moved=1
  fi

  install -d -m 0755 "$CONTAINERD_DROPIN_DIR"
  if ! grep -qs "RequiresMountsFor=$SANDBOX_MNT" "$CONTAINERD_DROPIN"; then
    cat > "$CONTAINERD_DROPIN" <<UNIT
[Unit]
# Written by terraform/gcp/files/startup.sh: containerd's root is on the
# sandbox volume, so containerd must not start before it is mounted.
RequiresMountsFor=$SANDBOX_MNT
UNIT
    systemctl daemon-reload
    moved=1
  fi

  if [ "$moved" -eq 0 ]; then
    return
  fi

  # Only reached on the one boot that moves the root. A host that has
  # been running with images under /var/lib/docker or /var/lib/containerd keeps them there,
  # unused -- named rather than deleted here, since reclaiming that space
  # is an operator's call and not a startup script's.
  if systemctl is-active --quiet containerd; then
    log "restarting containerd onto its new root (/var/lib/containerd is now unused and can be removed by hand)"
    systemctl restart containerd || log "WARNING: containerd did not restart cleanly; see journalctl -u containerd"
  fi
  if systemctl is-active --quiet docker; then
    log "restarting docker onto its new data root (/var/lib/docker is now unused and can be removed by hand)"
    systemctl restart docker || log "WARNING: docker did not restart cleanly; see journalctl -u docker"
  fi
  # The images the daemon runs from live in the root we just left, so ask
  # config-sync for a deploy on its next pass: it only runs one when the
  # generation changes or the last one did not succeed, and neither is
  # true of a plain reboot.
  rm -f "$DEPLOY_STATE"
  log "asked config-sync to redeploy, so this host re-pulls its images into the new data root"
}

# The mirror image of use_sandbox_disk_for_docker, for a deployment that
# turns the volume off again (sandbox_disk_gb = 0, or a disk detached by
# hand): a RequiresMountsFor on a mount that will never happen keeps
# dockerd and containerd from starting at all, and a data root under an unmounted
# /mnt/grain-sandbox is the boot disk wearing a confusing name. Both are
# undone here rather than left as a trap.
stop_using_sandbox_disk_for_docker() {
  local reverted=0

  if [ -f "$DOCKER_DROPIN" ]; then
    log "removing $DOCKER_DROPIN: there is no sandbox volume to wait for"
    rm -f "$DOCKER_DROPIN"
    systemctl daemon-reload
    reverted=1
  fi
  if [ -f "$CONTAINERD_DROPIN" ]; then
    log "removing $CONTAINERD_DROPIN: there is no sandbox volume to wait for"
    rm -f "$CONTAINERD_DROPIN"
    systemctl daemon-reload
    reverted=1
  fi
  if grep -qs "\"data-root\": \"$SANDBOX_DOCKER_DIR\"" "$DOCKER_CONF"; then
    log "putting docker's data root back on the boot disk"
    if [ -f "$DOCKER_CONF.pre-grain" ]; then
      mv "$DOCKER_CONF.pre-grain" "$DOCKER_CONF"
    else
      rm -f "$DOCKER_CONF"
    fi
    reverted=1
  fi
  if grep -qs "root = \"$SANDBOX_CONTAINERD_DIR\"" "$CONTAINERD_CONF"; then
    log "putting containerd's root back on the boot disk"
    if [ -f "$CONTAINERD_CONF.pre-grain" ]; then
      mv "$CONTAINERD_CONF.pre-grain" "$CONTAINERD_CONF"
    else
      rm -f "$CONTAINERD_CONF"
    fi
    reverted=1
  fi
  if grep -q " $HOST_SANDBOX_MNT \| $SANDBOX_MNT " /etc/fstab; then
    log "dropping the sandbox volume's own fstab entries"
    if mountpoint -q "$HOST_SANDBOX_MNT"; then
      umount "$HOST_SANDBOX_MNT" || log "WARNING: $HOST_SANDBOX_MNT is still bind-mounted; it goes away on the next boot"
    fi
    # `\|`-delimited so the paths' own slashes need no escaping.
    sed -i "\| $HOST_SANDBOX_MNT |d;\| $SANDBOX_MNT |d" /etc/fstab
    reverted=1
  fi

  if [ "$reverted" -eq 1 ]; then
    if systemctl is-active --quiet containerd; then
      systemctl restart containerd || log "WARNING: containerd did not restart cleanly; see journalctl -u containerd"
    fi
    if systemctl is-active --quiet docker; then
      systemctl restart docker || log "WARNING: docker did not restart cleanly; see journalctl -u docker"
    fi
    rm -f "$DEPLOY_STATE"
  fi
}

install_config_sync() {
  install -d -m 0700 /opt/grain-deploy
  md instance/attributes/grain-config-sync-script > "$SYNC.new"
  install -m 0700 "$SYNC.new" "$SYNC"
  rm -f "$SYNC.new"

  cat > /etc/systemd/system/grain-config-sync.service <<'UNIT'
[Unit]
Description=Watch instance metadata and redeploy grain v2 when it changes
After=network-online.target google-guest-agent.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/grain-deploy/config-sync.sh
Restart=always
RestartSec=10
# The deploy writes /var/lib/grain/data, creates a system user, and reads
# secrets from its own instance metadata. It needs to be root.
User=root

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable --now grain-config-sync.service
  log "grain-config-sync.service enabled"
}

# Both volumes before install_config_sync, so the deploy it starts
# already writes into them rather than being the thing that has to be
# redone once they arrive.
mount_data_disk
if mount_sandbox_disk; then
  use_sandbox_disk_for_docker
else
  stop_using_sandbox_disk_for_docker
fi
install_config_sync
log "done; watch the rollout with: journalctl -u grain-config-sync -f"
