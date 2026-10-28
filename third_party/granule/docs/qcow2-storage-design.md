# Granule QCOW2 Storage Backend Design & Architecture

## 1. Executive Summary & Problem Statement

Cloud-Hypervisor (CHV) does not support multiple extents in VMDK descriptors at runtime. A fallback single flat file copy on start is expensive ($O(N)$ disk write and allocation on non-reflink filesystems).

Granule uses a high-performance **QCOW2 Layered Storage Backend** with 2MB cluster sizing and opportunistic reflink support:
- **Fast Startup via Layering & Reflink**: On boot, if the underlying disk is a flat file and the local filesystem supports reflinks (`FICLONE`), granule creates an instant copy-on-write clone. Otherwise, it generates a thin QCOW2 overlay pointing to the backing image.
- **Layer Optimization**: Consecutive builds extend and overwrite the existing top layer in-place if it remains below 64MB, preventing excessive backing chain depths.
- **2MB Large Cluster Size**: Decreases metadata overhead and L1/L2 lookups by 32x compared to standard 64KB clusters, achieving near-raw sequential write performance.
- **Flatten Replay**: A consolidation utility (`granule guest flatten`) replays all modified clusters from the QCOW2 chain directly against a flat base image, producing a single raw rootfs layer.

---

## 2. Storage Architecture

```
Layer Build / Boot Workflow:
┌─────────────────────────────────────────────────────────┐
│ Flat Base Image (disk.img)                              │
└───────────────────────────┬─────────────────────────────┘
                            │
              Reflink Supported on Flat File?
             /                             \
           YES                              NO
           /                                 \
┌─────────────────────────┐       ┌─────────────────────────────────────┐
│ Fast Reflink Clone      │       │ QCOW2 Layer 1 (disk.qcow2)          │
│ ioctl(FICLONE)          │       │ • 2MB cluster size                  │
│ O(1) Instant CoW File   │       │ • Backing file: disk.img            │
└─────────────────────────┘       └──────────────────┬──────────────────┘
                                                     │
                                       Next Layer Added (Small <64MB)?
                                      /                               \
                                    YES                                NO
                                    /                                   \
                      ┌───────────────────────────┐       ┌───────────────────────────┐
                      │ In-place Layer Extension  │       │ New Layer in Chain        │
                      │ (Grow/Overwrite top QCOW2)│       │ (Backing: previous QCOW2) │
                      └───────────────────────────┘       └─────────────┬─────────────┘
                                                                        │
                                                          Flatten requested at end?
                                                                        │
                                                          ┌─────────────▼─────────────┐
                                                          │ Flatten Replay Chain      │
                                                          │ Replay L1/L2 clusters to  │
                                                          │ single flat rootfs image  │
                                                          └───────────────────────────┘
```

---

### 2.1 The backing-chain depth ceiling

**cloud-hypervisor boots a chain of at most 10 qcow2 layers over a raw
base, and refuses the 11th.** Measured against the version this tree
vendors, on a KVM-capable host:

| chain depth | result |
| --- | --- |
| 1 - 10 | boots |
| 11 and deeper | `MaxNestingDepthExceeded`, VM never starts |

The refusal is a hard limit in cloud-hypervisor's own block layer, not a
performance cliff, and it names itself in the error:

```
Fatal error: VmBoot(VmBoot(DeviceManager(Disk(BlockError { kind: Overflow,
  source: Some(BackingFileOpen("layer.15.qcow2",
    BackingFileOpen("layer.14.qcow2", ... MaxNestingDepthExceeded))))
```

This is the number the "preventing excessive backing chain depths"
language in section 1 is implicitly about, and it is worth stating
because of how the failure presents: an over-deep chain is not a slow
image, it is an image that cannot boot at all, and nothing before boot
reports it. `AddLayer`'s 64MB in-place extension (3.3) and
`FlattenChain` (3.4) are what keep a chain under the ceiling; what the
ceiling *is* decides how much headroom either needs.

Two consequences worth designing against rather than discovering:

- **A depth check belongs before the write, not at boot.** Anything that
  adds a layer can know the resulting depth cheaply -- qcow2 records each
  backing path -- and refusing an 11th layer with a clear error is far
  better than producing an artifact whose only symptom is a VM that will
  not start.
- **The ceiling counts the whole chain, including the per-VM overlay.**
  A guest shipped at depth 10 leaves no room for `PrepareOverlay`'s own
  layer, so a published image's usable budget is one less than the limit.
  Worth confirming against whatever depth an image is published at.

#### How it was measured

A 6.3GB raw `disk.img` taken from a packed guest image, 64 sparse qcow2
layers created over it with `qemu-img create -f qcow2 -F <prev> -b
<prev>`, and each depth booted directly:

```sh
cloud-hypervisor --kernel vmlinuz --initramfs initrd.img \
  --cmdline "console=ttyS0 root=/dev/vda rw panic=-1" \
  --disk path=layer.NN.qcow2,image_type=qcow2,backing_files=on \
  --cpus boot=2 --memory size=1024M --serial tty --console off
```

A boot counts only if the kernel banner (`Linux version`) appears; the
first pass of this measurement reported a false success because it also
matched the word "Booting" in cloud-hypervisor's own error output.

Incidentally, the 64-layer chain over that 6.3GB base occupied **2.3MB**
in total, which is the cost argument for stacking rather than copying,
in one number.

## 3. Key Subsystems (`pkg/qcow2`)

### 3.1 2MB Cluster Allocation & Header Generation
`WriteOverlay(path, backingFile, size)` initializes a QCOW2 v3 image with:
- `ClusterBits = 21` (2MiB cluster size).
- Initial L1 table and refcount blocks sized to cover the virtual disk capacity.
- Backing file header extension pointing to the parent image (relative or absolute).

### 3.2 Reflink & Fallback Boot Probing
`PrepareOverlay` in `pkg/config`:
- Detects whether the source image is raw flat format.
- Probes target directory for reflink capability (`FICLONE`).
- Clones via reflink if supported; otherwise creates a QCOW2 overlay with 2MB clusters.

### 3.3 Small Layer Extension (`AddLayer`)
`AddLayer(newLayerPath, currentTopLayer, newSize, smallThreshold)`:
- If the current top layer size is below the threshold (default: 64MB), it extends and updates the existing top layer rather than introducing a new chain link.
- If the layer is large or has reached capacity, a new child QCOW2 layer is appended.

### 3.4 Flatten Replay (`FlattenChain` / `Flatten`)
- Traverses the backing chain from the root base image to the newest child layer.
- Resolves all allocated clusters from L1/L2 tables and writes modified cluster blocks sequentially into the flat output file.
- Replaces complex multi-layer chains with a unified, contiguous disk file.
