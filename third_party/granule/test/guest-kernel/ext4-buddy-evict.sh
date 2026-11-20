#!/bin/bash
# ext4-buddy-evict.sh: boot a guest kernel under qemu and check that ext4
# survives a write after its buddy cache was evicted.
#
#   test/guest-kernel/ext4-buddy-evict.sh <vmlinuz> <modules-dir>
#
# <modules-dir> is that kernel's /lib/modules/<version> (an extracted
# linux-image-*.deb works). Exits 0 when the guest survives, 1 when the
# kernel oopses or hangs, and 2 when the harness itself could not run.
#
# Why this test exists: Debian's 6.1.0-55-amd64 (6.1.192-1) carries the
# 6.1.y backport of "ext4: fix error pointer dereference in
# ext4_mb_load_buddy_gfp()" (stable a8d83471c560, mainline c6a6c9694aad),
# which tests __filemap_get_folio()'s result with IS_ERR() only. On 6.1
# that function still returns NULL on a page-cache miss (it returns
# ERR_PTR only from 6.4 on), so the first allocation from a block group
# whose buddy-cache folio reclaim has dropped dereferences NULL in
# ext4_mb_load_buddy_gfp, from writeback (wb_workfn) or from close
# (ext4_discard_preallocations). The oops leaves the group locked, and
# every later writer -- jbd2, the flush worker, ext4lazyinit -- hangs
# behind it. In a real guest it takes tens of GB of buffered writes before
# reclaim gets to the buddy cache; drop_caches does the same eviction in
# a second, so this reproduces in under a minute on a 2 GB disk.
#
# Needs: qemu-system-x86_64 (KVM if /dev/kvm is usable, TCG otherwise),
# busybox-static, mkfs.ext4, cpio, xz/zstd for compressed modules.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <vmlinuz> <modules-dir>" >&2
  exit 2
fi
kernel=$1
moddir=$2
export PATH=$PATH:/sbin:/usr/sbin
for tool in qemu-system-x86_64 busybox mkfs.ext4 cpio; do
  command -v "$tool" >/dev/null || { echo "$0: $tool is not installed" >&2; exit 2; }
done
[[ -r $kernel ]] || { echo "$0: no kernel at $kernel" >&2; exit 2; }
[[ -d $moddir ]] || { echo "$0: no modules directory at $moddir" >&2; exit 2; }

work=$(mktemp -d "${TMPDIR:-/var/tmp}/ext4-buddy-evict.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work"/ir/{bin,mod,proc,sys,dev,mnt}
cp "$(command -v busybox)" "$work/ir/bin/busybox"

# Whatever of these the kernel builds as modules, in load order; a
# built-in one has no file and is skipped. Debian builds ext4 and virtio
# as modules, the cloud-hypervisor kernel builds them in.
mods=(crc32c_generic crc16 mbcache jbd2 ext4 virtio_ring virtio
  virtio_pci_modern_dev virtio_pci_legacy_dev virtio_pci virtio_blk)
for m in "${mods[@]}"; do
  f=$(find "$moddir/kernel" -name "$m.ko*" -print -quit)
  case $f in
    "") ;;
    *.ko.xz) xz -dc "$f" >"$work/ir/mod/$m.ko" ;;
    *.ko.zst) zstd -qdc "$f" >"$work/ir/mod/$m.ko" ;;
    *.ko) cp "$f" "$work/ir/mod/$m.ko" ;;
  esac
done

cat >"$work/ir/init" <<'EOF'
#!/bin/busybox sh
/bin/busybox --install -s /bin
export PATH=/bin
mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev
for m in crc32c_generic crc16 mbcache jbd2 ext4 virtio_ring virtio virtio_pci_modern_dev virtio_pci_legacy_dev virtio_pci virtio_blk; do
  [ -f /mod/$m.ko ] && insmod /mod/$m.ko
done
i=0; while [ ! -b /dev/vda ] && [ $i -lt 50 ]; do sleep 0.2; i=$((i+1)); done
mount -t ext4 /dev/vda /mnt || { echo EVICT-HARNESS-FAILED: mount; poweroff -f; }
# Initialize the buddy cache of every group the writes land in.
for i in 1 2 3 4 5 6; do dd if=/dev/zero of=/mnt/f$i bs=1M count=200 2>/dev/null; done; sync
# Evict clean page cache, the buddy cache included: what reclaim does on
# its own once a long buffered write has filled memory.
echo 3 > /proc/sys/vm/drop_caches
for i in 1 2 3; do dd if=/dev/zero of=/mnt/g$i bs=1M count=100 2>/dev/null; done; sync
rm -f /mnt/f*; sync
echo EVICT-SURVIVED
poweroff -f
EOF
chmod +x "$work/ir/init"
(cd "$work/ir" && find . | cpio -o -H newc 2>/dev/null | gzip >"$work/initrd.gz")

truncate -s 2G "$work/disk.img"
mkfs.ext4 -q -F "$work/disk.img"

accel=(-accel tcg)
[[ -w /dev/kvm ]] && accel=(-enable-kvm)
# oops=panic with panic=-1 and -no-reboot turns an oops into qemu
# exiting at once, rather than the guest hanging until the timeout.
timeout "${EVICT_TIMEOUT:-600}" qemu-system-x86_64 "${accel[@]}" -m 1024 -smp 2 \
  -nographic -no-reboot -kernel "$kernel" -initrd "$work/initrd.gz" \
  -append "console=ttyS0 oops=panic panic=-1 loglevel=4" \
  -drive "file=$work/disk.img,format=raw,if=virtio" >"$work/console.log" 2>&1 || true

if grep -q EVICT-SURVIVED "$work/console.log"; then
  echo "PASS: $(basename "$kernel") survived a write after ext4 buddy-cache eviction"
  exit 0
fi
if grep -q EVICT-HARNESS-FAILED "$work/console.log"; then
  cat "$work/console.log" >&2
  exit 2
fi
grep -aE "BUG:|Oops|RIP:|Workqueue:|Call Trace|ext4_|Kernel panic" "$work/console.log" | head -30 >&2 || true
echo "FAIL: $(basename "$kernel") oopsed or hung writing after ext4 buddy-cache eviction" >&2
exit 1
