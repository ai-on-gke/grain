#!/bin/bash
# Builds the minimal guest payload used by the standalone and GKE
# benchmarks: a PVH-bootable kernel plus an initramfs whose init does
# nothing but mount /proc, print a BOOT_COMPLETE marker, and power off.
# This isolates cloud-hypervisor/kernel boot time from any guest
# application startup cost.
#
# Outputs, under $OUT_DIR (default ./out):
#   vmlinux         - PVH-entry kernel (fetched, not built, to avoid a
#                      from-source kernel build in CI/sandboxes)
#   initramfs.img   - busybox-based initramfs with the marker init
#   disk.img        - empty placeholder disk (granule's "run" mode always
#                      attaches a boot disk, defaulting to the guest image
#                      baked into the OCI image if CHV_DISK_IMAGE is
#                      unset; these benchmarks point it at this empty
#                      placeholder instead, since they don't use a root
#                      filesystem, since init runs from the initramfs)
set -euo pipefail

OUT_DIR="${OUT_DIR:-$(dirname "$0")/out}"
KERNEL_URL="${KERNEL_URL:-https://github.com/cloud-hypervisor/linux/releases/download/ch-release-v6.16.9-20260508/vmlinux-x86_64}"

mkdir -p "$OUT_DIR"
cd "$OUT_DIR"

if [ ! -f vmlinux ]; then
  echo "fetching PVH kernel from $KERNEL_URL"
  curl -fsSL -o vmlinux "$KERNEL_URL"
fi

rm -rf initramfs-root
mkdir -p initramfs-root/bin initramfs-root/proc initramfs-root/sys initramfs-root/dev
BUSYBOX="$(command -v busybox-static || command -v busybox)"
cp "$BUSYBOX" initramfs-root/bin/busybox
ln -sf busybox initramfs-root/bin/sh
cat > initramfs-root/init <<'EOF'
#!/bin/busybox sh
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t sysfs sysfs /sys
UPTIME=$(/bin/busybox cat /proc/uptime)
/bin/busybox echo "BOOT_COMPLETE uptime=${UPTIME}"
/bin/busybox poweroff -f
EOF
chmod +x initramfs-root/init initramfs-root/bin/busybox

(cd initramfs-root && find . | cpio -o -H newc 2>/dev/null | gzip -9) > initramfs.img
rm -rf initramfs-root

if [ ! -f disk.img ]; then
  dd if=/dev/zero of=disk.img bs=1M count=1 2>/dev/null
fi

echo "wrote vmlinux, initramfs.img, disk.img to $OUT_DIR"
