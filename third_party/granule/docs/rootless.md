# Running the VM container rootless

A granule VM container needs no root on the host. It runs in a user
namespace whose root is mapped to a host subuid, reaches `/dev/kvm` through
the host's `kvm` group held as a supplementary group, and has no
`CAP_SYS_ADMIN` anywhere: not on the host, and not inside its own
namespace either. This page lists what the runtime has to give it, what
granule does in that setup, and what does not work.

`test/rootless-vm.sh` checks all of it on a real VM. Grain CI runs it as the
`rootless-vm` job in `.grain/workflows/rootless-vm.yml`. It covers two setups:

* **unshare**: `unshare --user --map-root-user --net --mount`. Container
  root is the caller's own uid, and `/dev/kvm` comes through the caller's
  kvm group, which the namespace shows as gid 65534.
* **crun**: a rootless crun container whose root is host uid 100000, on a
  rootfs unpacked as those subuids. This is the layout rootless storage
  gives a runtime, and it is how Silo's local backend runs "vm class"
  containers.

In both, the script boots the published alpine guest with this tree's shim
and checks that:

* the guest answers `granule ready`;
* it holds the namespace's address;
* it reaches a peer across netshim's splice;
* `SIGTERM` shuts it down cleanly.

Under crun the script also checks that:

* the container holds no `CAP_SYS_ADMIN`;
* the qcow2 overlay is created on the subuid-owned rootfs;
* a second boot keeps what the first wrote to it;
* a virtio-fs share works through an unprivileged virtiofsd, with the
  guest's `chown` landing on a subuid;
* without the host's groups, the shim refuses to start and names the fix.

If the caller cannot open `/dev/kvm` read-write, the run skips (or fails,
with `REQUIRE_KVM=1`).

## What the runtime has to provide

The fields are shown in OCI `config.json` form, since that is what crun
reads. `crun_config` in `test/rootless-vm.sh` builds a complete working
example.

| Need | OCI config | Why |
| --- | --- | --- |
| A user namespace, root mapped to subuids | `linux.namespaces` has `user`; `linux.uidMappings`/`gidMappings` = `[{containerID: 0, hostID: <subuid>, size: 65536}]` | Container root is the account that owns the rootfs and creates the overlay. Nothing on the host runs as root. |
| A network namespace **owned by that user namespace** | `linux.namespaces` has `network`, created with the container (crun's default), not joined from elsewhere | netshim creates the taps, bridge and tc splice with `CAP_NET_ADMIN`. The kernel checks that capability against the user namespace that *owns* the network namespace. See "What does not work" below. |
| `CAP_NET_ADMIN` | in `process.capabilities` (bounding, effective, permitted) | Namespaced: it is power over the container's own network namespace only. Nothing needs `CAP_SYS_ADMIN`. The tested set is podman's rootless default plus `CAP_NET_ADMIN`. |
| `/dev/kvm` | `linux.devices` entry (a rootless crun bind-mounts the host node) | Opened read-write by cloud-hypervisor. |
| The host's kvm group, kept | annotation `run.oci.keep_original_groups: "1"` (podman: `--group-add keep-groups`) | `/dev/kvm` is `root:kvm 0660` on the host. Inside the namespace it appears as `65534:65534`, because neither id is mapped, and only a supplementary group the process kept from the caller opens it. A runtime that calls `setgroups` drops the group, and the shim then refuses to start. |
| `/dev/net/tun` | `linux.devices` entry | The host's node is mode 0666, so no group is needed. |
| A writable `/run` | e.g. a `tmpfs` mount at `/run` | The API socket (`/run/cloud-hypervisor/api.sock`) and the vsock socket (`/run/granule/vsock.sock`) live there. `CHV_API_SOCKET` and `CHV_VSOCK_SOCKET` move them elsewhere. |
| A rootfs the mapped root can write, at least the overlay's directory | rootless storage, or a writable volume at `CHV_DISK_OVERLAY_PATH`'s directory | The guest writes into its overlay, not into the image. |
| An interface to take over | a veth or pasta/slirp device with an address and a default route, before the shim starts | Inline netshim takes over the namespace's identity, so it has to exist by then. The test plumbs a veth in from outside with `nsenter -t <pid> -U -n`, as a CNI would. |
| Optional: `RLIMIT_NOFILE` above crun's default 1024 | `process.rlimits` | Only virtiofsd cares. It asks for 1000000 and falls back to the hard limit. |

`no_new_privileges` is fine, and so is the default seccomp profile of
crun or podman.

## What granule does in that namespace, and why it works

* **`/dev/kvm`.** cloud-hypervisor opens it, and the kernel's permission
  check counts supplementary groups. Nothing in granule reads the device's
  owner or mode to decide whether to start. The shim's preflight
  (`hypervisor.CheckDevice`, called before any disk or network is
  prepared) *opens* the device. When that fails it reports the process's
  uid, gid and groups, the device's owner and mode as the container sees
  them, and the fix. Without the preflight, cloud-hypervisor's own report
  is `CreateHypervisor(VmCreate(Permission denied (os error 13)))`.
  `granule doctor` also opens the device; it uses `stat` only to tell a
  missing device from an unopenable one.
* **Networking.** netshim does its work over netlink: tuntap creation
  (`TUNSETIFF`), a bridge, addresses, an ingress qdisc and tc
  u32 filters with mirred redirects for the splice. Each is checked against
  `CAP_NET_ADMIN` in the network namespace's owning user namespace, which
  a mapped root holds. netshim writes nothing under `/proc/sys`. It never
  did, because that is read-only in an unprivileged container anyway.
  `NETSHIM_TAP_OWNER` (default `0:0`) names the tap's owner as a uid and
  gid *inside* the namespace. An id the namespace does not map is refused
  at tap creation with `EINVAL`.
* **Disks.** The qcow2 overlay is written by `pkg/qcow2` in Go: there is no
  `qemu-img`, loop device, `mknod` or mount. A raw overlay is a copy, or a
  `FICLONE` reflink, which is unprivileged, with a sparse-copy fallback.
  Both work on any filesystem the mapped root can write. The overlay ends
  up owned by the container's root, which is a subuid on the host.
* **No chown, mknod or mount.** Neither `granule-shim run` nor the packages
  behind it (`pkg/config`, `pkg/hypervisor`, `pkg/netshim`) calls chown,
  mknod or mount.
* **virtio-fs.** granule does not start virtiofsd. `CHV_FS=tag=<tag>,socket=<path>`
  points cloud-hypervisor at one the container (or a sidecar sharing its
  `/run`) has already started. As the container's mapped root, virtiofsd
  1.13 runs with `--sandbox=chroot`, which needs `CAP_SYS_CHROOT`, or with
  `--sandbox=none`. Its default capability set includes `CAP_MKNOD`, which
  podman's rootless set lacks, so give it `--modcaps=-mknod` or add
  `CAP_MKNOD`. Otherwise it exits at once with "can't apply the child
  capabilities". Ownership the guest sets lands on the host as subuids: a
  guest `chown 1234` becomes host uid `<subuid>+1234`. Files owned by host
  ids the namespace does not map appear as 65534 and cannot be chowned.

## What does not work rootless

* **Joining a network namespace owned by another user namespace.** If the
  runtime puts the container in a pre-existing network namespace (a path
  in `linux.namespaces`) that a different user namespace created, the
  container's root has no `CAP_NET_ADMIN` there. Inline netshim then fails
  creating its tap with `EPERM`, and the shim reports it as "setting up
  inline networking (requires NET_ADMIN and /dev/net/tun ...)". The network
  namespace must be created inside the container's user namespace, or the
  container must join the user namespace that owns it. A separate
  `granule netshim` init container works only under the same condition.
* **`/dev/kvm` without the group.** A runtime that calls `setgroups` for
  the container, which is crun's and runc's default, removes the host's kvm
  group. The only alternatives are mapping the host's kvm gid into the
  namespace, which needs an `/etc/subgid` entry granting it, and listing
  it in `process.user.additionalGids`, or making `/dev/kvm` mode 0666 on
  the host. The shim refuses to start, naming these remedies.
* **virtiofsd's `--sandbox=namespace`** needs `CAP_SYS_ADMIN` to unshare
  its namespaces, so it fails with `EPERM`. Use `chroot` or `none`.
* **Old crun.** Debian bookworm's crun 1.8.1 cannot create a rootless
  container on the 7.0 kernel these tests run on: its parent's
  `unshare(CLONE_NEWNS)` returns `EPERM`, before granule is involved.
  crun 1.24 works, and it is what the test pins.
* **Not covered by the test**: suspend/resume to `CHV_SNAPSHOT_PATH`, and
  pasta or slirp4netns as the namespace's uplink. The splice treats
  whatever interface holds the namespace's address and default route the
  same way, but neither has been booted here.

## Running the test

```sh
sudo apt-get install -y uidmap jq busybox-static   # newuidmap, the bundle's config, the entrypoint's shell
test/rootless-vm.sh                                # both modes
test/rootless-vm.sh crun                           # one
VIRTIOFSD_ROOTFS=/path/to/rootfs test/rootless-vm.sh crun   # with the virtio-fs check
```

The caller needs a 65536-id range in `/etc/subuid` and `/etc/subgid`, and
must be able to open `/dev/kvm` read-write: in practice, be in the kvm
group. In a grain sandbox, `GITHUB_PROXY=<the sandbox's nameserver>`
fetches crun through grain's proxy, as the Dockerfile does for
cloud-hypervisor.
