# Guest kernel regression tests

## ext4 NULL dereference in `ext4_mb_load_buddy_gfp` (Debian 6.1.0-55)

`ext4-buddy-evict.sh` boots a guest kernel under qemu, writes to an ext4
disk, drops the page cache, and writes again. It exits 0 when the kernel
survives this and 1 when it oopses or hangs:

    test/guest-kernel/ext4-buddy-evict.sh /boot/vmlinuz-<ver> /lib/modules/<ver>

CI's `guest-kernel` job runs it against the kernel `linux-image-amd64`
currently resolves to on bookworm, which is the kernel the `debian12`
guest installs (`GUEST_KERNEL_PACKAGE`).

### Symptom

Guests booted from the `debian12` guest image, which ran Debian's
`6.1.0-55-amd64` (6.1.192-1), hung during long heavy writes such as a cold
multi-GB `docker build`. The hang came at 24 to 41 GB written, on both 59
and 100 GB disks:

- `jbd2/vda-8`, `ext4lazyinit` and `flush-254:0` were stuck in D state,
  and every new command blocked;
- `/proc/pressure/io` showed `full` at about 99%, and the `vda` write
  counters stopped moving;
- `dmesg` showed `BUG: kernel NULL pointer dereference` at
  `RIP: ext4_mb_load_buddy_gfp+0xbc`, from `Workqueue: writeback
  wb_workfn` (via `ext4_writepages -> ext4_mb_new_blocks ->
  ext4_mb_regular_allocator`), or from `close()` via
  `ext4_release_file -> ext4_discard_preallocations`.

### Cause

The bug is in the kernel. In October 2026, 6.1.y backported the mballoc
folio conversion along with "ext4: fix error pointer dereference in
ext4_mb_load_buddy_gfp()" (6.1.y `a8d83471c560`, mainline `c6a6c9694aad`).
As a result, `ext4_mb_load_buddy_gfp` handles the result of
`__filemap_get_folio()` with the mainline idiom:

    folio = __filemap_get_folio(inode->i_mapping, pnum, FGP_ACCESSED, 0);
    if (IS_ERR(folio) || !folio_test_uptodate(folio) || ...)

Mainline only changed `__filemap_get_folio()` to return `ERR_PTR` in 6.4.
On 6.1 it still returns `NULL` when the page is not cached. So when a
block group's buddy-cache folio is not in the page cache,
`folio_test_uptodate(NULL)` faults. The faulting bytes in the oops
(`48 3d 00 f0 ff ff 0f 87 ...`, `cmp rax,-4096; ja`) are that `IS_ERR`
check, followed by a dereference of `RAX=0`.

The buddy cache is ordinary page cache on an internal inode, so memory
reclaim evicts it. A guest writing tens of GB fills its memory with page
cache, reclaim reaches the buddy cache, and the next allocation from an
affected group oopses. The oops kills the flush worker while it holds the
group's state, and everything that writes to the disk then queues behind
it.

`drop_caches` evicts the buddy cache immediately. This is why the
reproducer needs a 2 GB disk and well under a minute instead of a 40 GB
write.

### What it was not

- **`granule-mem-agent`.** The suspect process shows up in `ps` as
  `granule-mem-age` because the kernel truncates process names to 15
  characters. The agent reads `/proc/pressure/memory` and `/proc/meminfo`
  and sends signals to the host. It never reclaims memory or drops page
  cache inside the guest. In the affected sandboxes it had no token, so it
  sent nothing at all. The reproducer fails with no agent present.
- **Writeback throttling or mount options.** A flush worker waiting in
  `rq_qos_wait` (`wbt_wait`) is ordinary throttling while the disk is busy,
  and it was waiting on a writer that had already died. The reproducer
  fails with default mount options. `nodelalloc`, xfs for
  `/var/lib/docker`, and stopping the agent each only changed which
  allocations happened or when, so they made the bug rarer without
  removing it.

### Fix

Debian's `6.1.0-56-amd64` (6.1.192-2, bookworm-security) and bookworm-
backports' `6.12.95+deb12-amd64` both pass `ext4-buddy-evict.sh`;
`6.1.0-55-amd64` fails it in about 30 seconds. The Dockerfile's
`GUEST_KERNEL_PACKAGE` step refuses to pack `6.1.0-55`, so an image cannot
ship it again, and editing that recipe moves the rootfs content key
(`deploy/guest-rootfs-key.sh`), so the next publish rebuilds the rootfs
instead of reusing the one that carries the broken kernel.
