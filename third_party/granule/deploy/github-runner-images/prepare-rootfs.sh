#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "prepare-rootfs.sh must run as root" >&2
  exit 1
fi
if [[ $# -ne 4 ]]; then
  echo "usage: prepare-rootfs.sh ROOTFS AGENT_ARTIFACTS OUTPUT_DIR INSTALL_KERNEL" >&2
  exit 2
fi

rootfs=$(readlink -f "$1")
agents=$(readlink -f "$2")
output=$(readlink -m "$3")
install_kernel=$4
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)

case "$install_kernel" in true|false) ;; *) echo "INSTALL_KERNEL must be true or false" >&2; exit 2 ;; esac
[[ -d $rootfs/etc ]] || { echo "$rootfs is not a Linux root filesystem" >&2; exit 1; }
[[ -x $agents/granule-agent && -x $agents/granule-mem-agent ]] || {
  echo "$agents does not contain the Granule guest agents" >&2
  exit 1
}

# The imported filesystem is x86-64 Ubuntu, so its package tools can run
# directly in a chroot on the x86-64 image builder. Mount only for the
# duration of package installation and initramfs generation.
mounts=()
parts=""
cleanup() {
  local i
  for ((i=${#mounts[@]}-1; i>=0; i--)); do
    umount -R "${mounts[$i]}" 2>/dev/null || true
  done
  if [[ -n $parts ]]; then
    rm -rf "$parts"
  fi
}
trap cleanup EXIT

mount -t proc proc "$rootfs/proc"; mounts+=("$rootfs/proc")
mount --rbind /sys "$rootfs/sys"; mount --make-rslave "$rootfs/sys"; mounts+=("$rootfs/sys")
mount --rbind /dev "$rootfs/dev"; mount --make-rslave "$rootfs/dev"; mounts+=("$rootfs/dev")

cp -L /etc/resolv.conf "$rootfs/etc/resolv.conf"
packages=(systemd-sysv acpid iproute2 udev kmod klibc-utils e2fsprogs ca-certificates)
if [[ $install_kernel == true ]]; then
  packages+=(linux-image-kvm)
fi
chroot "$rootfs" /usr/bin/env DEBIAN_FRONTEND=noninteractive \
  apt-get update
chroot "$rootfs" /usr/bin/env DEBIAN_FRONTEND=noninteractive \
  apt-get install -y --no-install-recommends "${packages[@]}"
chroot "$rootfs" apt-get clean
rm -rf "$rootfs/var/lib/apt/lists"/*

# Azure recreates the Packer administration user when it instantiates an
# image; exporting the managed disk directly skips that deployment step.
# Recreate GitHub's conventional account so callers can choose either
# `runner` compatibility or Granule's root default.
if ! chroot "$rootfs" id runner >/dev/null 2>&1; then
  chroot "$rootfs" useradd --create-home --shell /bin/bash runner
fi
install -d -m 0750 "$rootfs/etc/sudoers.d"
printf 'runner ALL=(ALL) NOPASSWD:ALL\n' > "$rootfs/etc/sudoers.d/runner"
chmod 0440 "$rootfs/etc/sudoers.d/runner"
if chroot "$rootfs" getent group docker >/dev/null 2>&1; then
  chroot "$rootfs" usermod -aG docker runner
fi

# Granule's own guest-side half goes on the same way it goes onto every
# other guest: deploy/guest-image/pack-guest lays the overlay trees and
# the agents down, sets their modes, enables the services and checks the
# operating system underneath provides what their boot runs. -lay-only
# stops it before the disk, which this script makes itself below (its
# headroom and its kernel handling are the runner images' own).
#
# The one thing it needs that the repository does not already hold in
# that shape is a parts directory -- the overlays and the agent binaries
# together -- which is what the granule image carries at
# pkg/guestimage's PartsDir and what is assembled here instead.
parts=$(mktemp -d)
cp -a "$repo_root/deploy/guest-image/overlay-common" \
  "$repo_root/deploy/guest-image/overlay-debian" \
  "$repo_root/deploy/guest-image/overlay-alpine" "$parts/"
install -m 0755 "$agents/granule-agent" "$parts/granule-agent"
install -m 0755 "$agents/granule-mem-agent" "$parts/granule-mem-agent"

# The udev naming mask goes down before the parts rather than with them,
# because it has to be in the initramfs regenerated at the end of this
# script: Ubuntu renames eth0 out from under granule's ip= configuration
# otherwise. It is the one piece of this the Dockerfile's own rootfs
# stages keep too, for the same reason -- see guest-rootfs-debian.
install -d "$rootfs/etc/udev/rules.d"
ln -sf /dev/null "$rootfs/etc/udev/rules.d/80-net-setup-link.rules"

GRANULE_GUEST_VARIANT=debian \
  /bin/sh "$repo_root/deploy/guest-image/pack-guest" -lay-only "$rootfs" "$parts"

# Azure's identity and network configuration belong to the Packer VM,
# not to the many guests cloned from this disk.
install -d "$rootfs/etc/cloud"
touch "$rootfs/etc/cloud/cloud-init.disabled"
rm -f "$rootfs/etc/netplan"/*.yaml "$rootfs/etc/ssh/ssh_host_"* \
  "$rootfs/var/lib/systemd/random-seed"
: > "$rootfs/etc/machine-id"
rm -f "$rootfs/var/lib/dbus/machine-id"
find "$rootfs/var/log" -type f -exec truncate -s 0 {} +
printf '/dev/vda / ext4 defaults 0 1\n' > "$rootfs/etc/fstab"
printf 'nameserver 8.8.8.8\n' > "$rootfs/etc/resolv.conf"

# The udev naming mask must also be present in the initramfs; otherwise
# Ubuntu renames eth0 before Granule's ip= configuration can use it.
chroot "$rootfs" update-initramfs -u -k all

cleanup
mounts=()
parts=""
trap - EXIT

kernel=$(find "$rootfs/boot" -maxdepth 1 -type f -name 'vmlinuz-*' -printf '%f\n' | sort -V | tail -1)
[[ -n $kernel ]] || { echo "the imported image contains no kernel" >&2; exit 1; }
version=${kernel#vmlinuz-}
[[ -f $rootfs/boot/initrd.img-$version ]] || {
  echo "the imported image contains no initramfs for $kernel" >&2
  exit 1
}

install -d "$output"
cp "$rootfs/boot/$kernel" "$output/vmlinuz"
cp "$rootfs/boot/initrd.img-$version" "$output/initrd.img"

# The guest-side contract this rootfs implements, beside the disk as well
# as inside it. deploy/guest-image/pack-guest does the same, and for the
# same reason: the copy inside the disk is unreachable to the granule
# about to boot it, and the label on the image is unreadable from inside
# the container that image became. Copied out of the rootfs the overlay
# was just laid into, so it cannot claim a version this guest does not
# carry. See pkg/guestimage's ContractSidecar.
[[ -f $rootfs/etc/granule/guest-contract ]] || {
  echo "the prepared rootfs has no /etc/granule/guest-contract: the granule overlay was not laid into it" >&2
  exit 1
}
cp "$rootfs/etc/granule/guest-contract" "$output/guest-contract"

size_kb=$(du -sk "$rootfs" | cut -f1)
image_kb=$((size_kb * 12 / 10 + 1048576))
truncate -s "${image_kb}K" "$output/disk.img"
mke2fs -F -t ext4 -L granule-root -d "$rootfs" "$output/disk.img"
chmod 0644 "$output/disk.img" "$output/vmlinuz" "$output/initrd.img" "$output/guest-contract"
