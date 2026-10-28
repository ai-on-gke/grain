# Running on GKE

granule needs nested virtualization (a real `/dev/kvm` inside the pod) to
run at all; everything else it needs to actually boot a VM (a guest disk
image and kernel) is already baked into the image itself (see the main
README's "Guest disk image and kernel"), so GKE only has to supply the
first. This is a concrete, tested walkthrough of that, ending in
[`gke-pod-example.yaml`](gke-pod-example.yaml) actually booting a VM.

## Direct startup

With a freshly built image containing inline networking, a KVM-enabled
node and [`kvm-device-plugin.yaml`](kvm-device-plugin.yaml) applied (see
section 1), no Granule-specific manifest or CLI is necessary:

```sh
kubectl run granule --image=YOUR_GRANULE_IMAGE --restart=Never --overrides='{"spec":{"containers":[{
  "name":"granule","image":"YOUR_GRANULE_IMAGE",
  "resources":{"limits":{"devic.es/kvm":"1","devic.es/tun":"1"}},
  "securityContext":{"capabilities":{"drop":["ALL"],"add":["NET_ADMIN"]},"allowPrivilegeEscalation":false}}]}}'
kubectl wait --for=condition=Ready pod/granule --timeout=120s
kubectl exec granule -- granule ready -timeout 120s
kubectl exec -it granule -- sh
```

The single container sets up the pod network before booting its guest.
Use the explicit manifests below for scheduling constraints, resource limits,
readiness probes and a read-only root filesystem. None of them is
privileged (see "Privileges" below for what each one holds instead).
GKE Autopilot is not supported: it offers no nested virtualization, and
does not admit the device plugin DaemonSet. The validation notes below describe
end-to-end verification of inline startup, image variants, and guest
operations on a live GKE cluster.

## 1. Create a cluster with nested virtualization

```sh
gcloud container clusters create granule-demo \
  --zone us-central1-a \
  --num-nodes 1 \
  --machine-type n2-standard-4 \
  --image-type COS_CONTAINERD \
  --enable-nested-virtualization
```

`--enable-nested-virtualization` is a real, first-class GKE node pool
flag (`gcloud container node-pools create --help`) -- no custom node
image or VMX license juggling needed. A plain GCE instance takes the
same flag on `gcloud compute instances create` and needs no VMX-licensed
disk image either, so a single host is a reasonable alternative to a
cluster when one is all that is wanted; see "On a plain GCE instance"
below. It requires either `COS_CONTAINERD` on GKE 1.28.4-gke.
1083000+ or `UBUNTU_CONTAINERD` (any version); every release channel
available at the time of writing satisfies that easily. Any nested-virt-
capable machine type works (N1/N2/N2D/C2 families, Haswell or later) --
`n2-standard-4` above is just comfortably enough for the 2 vCPU / 2GiB
example VM plus the container/kubelet overhead around it.

Confirm it worked: `/dev/kvm` should exist on the node with `crw-rw-rw-`
permissions (world-accessible -- no group or capability is needed to
open it, so a pod can run as any non-root user).

Then hand the device to pods. Being on the node is not enough: a
container's device cgroup only admits the devices it was given, and a
`/dev/kvm` hostPath puts the file in the container without the cgroup
rule, so cloud-hypervisor fails with `EPERM`. A device plugin adds both,
and is the stock Kubernetes way to do so:

```sh
kubectl apply -f deploy/k8s/kvm-device-plugin.yaml
kubectl get node -o jsonpath='{.items[*].status.allocatable}'   # devic.es/kvm, devic.es/tun
```

The plugin is itself unprivileged (no capabilities, read-only `/dev`).

## 2. Build and push the image

```sh
docker build -t granule .
docker tag granule us-central1-docker.pkg.dev/<project>/<repo>/granule:latest
docker push us-central1-docker.pkg.dev/<project>/<repo>/granule:latest
```

Whatever service account the node pool runs as (the default Compute
Engine service account, unless overridden) needs the Artifact Registry
Reader role (`roles/artifactregistry.reader`) on that repo to pull the
image -- already true by default for a repo in the same project as the
cluster in most setups.

## 3. Apply the pod

Substitute the pushed image into
[`gke-pod-example.yaml`](gke-pod-example.yaml) and apply it:

```sh
sed 's|IMAGE_PLACEHOLDER|us-central1-docker.pkg.dev/<project>/<repo>/granule:latest|' \
  gke-pod-example.yaml | kubectl apply -f -
kubectl logs -f granule-gke-example
```

No other cluster setup is needed beyond section 1's device plugin: that
one manifest boots the guest disk image and kernel already baked into
the `granule` image, and gets KVM by requesting `devic.es/kvm`. It runs
as an unprivileged user with every capability dropped. The console log
should reach a login prompt within a few seconds of the container
starting.

The `CHV_CPUS` and `CHV_MEMORY_MB` these manifests carry are a pod author
overriding what the image asks for, the way any `env:` overrides an `ENV`.
A machine built from a Granule records its own sizing in its config (`VM
SET cpus=`, `memory-mb=`, `cpus-max=`, `memory-max-mb=`, `disk-mode=` --
see [docs/granulefile.md](../../docs/granulefile.md) §5.5), so a manifest
that leaves those variables out boots it at the size it was built for.
There is no resolution step on this path: nothing reads the image's config
for a pod, so what the manifest omits is decided by the VM container's own
defaults (`pkg/config`) working from the image's `ENV`, and what it sets
wins. `granule vm create` resolves the same two sources on the docker
path, and writes the answer into the VM's saved state.

`deploy/k8s/pod-split-example.yaml` -- the optional split-privilege example the main
README points to, where the guest takes over the pod's own IP -- is the
same shape with one prerequisite this one deliberately drops: a
node-local `/var/lib/vm-images` holding a disk image and kernel. `gke-pod-exec-example.yaml` in the next section is the closer
equivalent -- a single container with inline networking, with neither prerequisite.

## 4. Run something in the guest

The pod above boots a VM with no network of its own, which is not the
same thing as one you cannot reach: exec goes over the VM's own vsock
device, so `kubectl exec granule-gke-example -- granule exec -- uname -a`
already runs a command inside that guest, with no `netshim` and no
control link involved (verified on a cluster -- see "Validated" below).

What the pod above has no answer for is a guest that needs a *network*.
Apply [`gke-pod-exec-example.yaml`](gke-pod-exec-example.yaml) instead --
identical, with inline networking enabled (`CHV_NET_MODE: inline`),
which gives the guest the pod's own IP and MAC -- and the same `kubectl exec`
reaches a guest that can also talk to the rest of the cluster:

```sh
sed 's|IMAGE_PLACEHOLDER|us-central1-docker.pkg.dev/<project>/<repo>/granule:latest|' \
  gke-pod-exec-example.yaml | kubectl apply -f -
kubectl wait --for=condition=Ready pod/granule-gke-exec-example --timeout=180s
kubectl exec granule-gke-exec-example -- granule exec -- uname -a
```

That guest also resolves names the way the pod would. With `NETSHIM_DNS`
left unset (or set to `auto`), the VM container reads the pod's own
`/etc/resolv.conf` and passes its nameserver -- the cluster DNS
ClusterIP, `34.118.224.10` on a default GKE cluster -- to the guest on
`ip=`, with the pod's `search` domains and `ndots` beside it as
`granule.dns_search=` and `granule.dns_options=`. The guest holds the pod's
address and route table, so it reaches that ClusterIP exactly as the pod
would, and `kubernetes.default` or `my-service.my-namespace` resolve inside
it with no manifest changes. A manifest that sets `NETSHIM_DNS` to an
address uses that address instead, with no search list; one that sets it
to the empty string leaves the guest image's own `/etc/resolv.conf` alone.

The main README's "User flows" section walks that, guest customization
and copying files in and out through to the end, alongside the docker
equivalents.

Both manifests, exactly as they are checked in here, have been applied
to a live GKE cluster and walked through all four of those flows. See
"Validated" below for what that settles -- including the thing that used
to settle unhappily, a spliced guest that could not exchange packets
with other pods on GKE's own CNI, and which a later pass has since
measured working in both directions.

## Privileges

None of the manifests here is privileged. What each holds instead, all
measured (see the end of this section):

| Manifest | User | Capabilities | Devices | Root filesystem | Pod Security level |
|---|---|---|---|---|---|
| `gke-pod-example.yaml` (no guest network) | 65534 | none | `devic.es/kvm` | read-only | `restricted` |
| `gke-pod-exec-example.yaml`, `pod-example.yaml` (inline networking) | root | `NET_ADMIN` only | `devic.es/kvm`, `devic.es/tun` | read-only | `privileged`\* |
| `pod-split-example.yaml`, `netshim` init container | root | `NET_ADMIN` only | `devic.es/tun` | read-only | `privileged`\* |
| `pod-split-example.yaml`, VM container | 65534 | none | `devic.es/kvm`, `devic.es/tun` | read-only | `privileged`\* (pod-wide) |
| `kvm-device-plugin.yaml` | root | none | read-only `/dev` mount | read-only | `privileged`\* (hostPath) |

All set `allowPrivilegeEscalation: false` and seccomp `RuntimeDefault`.
\* Pod Security Admission has no level between `baseline` and
`privileged`, and `baseline` rejects any added capability, so one
`NET_ADMIN` puts a pod in the same admission bucket as
`privileged: true`. The two are nothing alike in effect, which is the
point of this section; a cluster that enforces Pod Security per
namespace wants granule's networked pods in a namespace of their own.

### What `privileged: true` gives away

A privileged container has every capability, no seccomp filter, no
AppArmor/SELinux confinement, every host device, and a writable `/sys`
and `/proc/sys`. With `CAP_SYS_ADMIN` and the host's block devices it
can mount the node's disk; with `CAP_SYS_MODULE` it can load a kernel
module. It is root on the node in all but name.

For granule that matters because the container is the second of two
walls. The guest is held by KVM; cloud-hypervisor and granule's own
supervisor (`granule run`, the vsock exec relay) run in the container
around it and parse what the guest sends them -- virtio queues, vsock
streams. A bug in either that lets a guest run code in that process
lands the attacker wherever that process is. If the container is
privileged, that is the node, and from the node every other pod on it,
its service-account tokens and the kubelet's credentials. If the
container is one of the above, the attacker gets an unprivileged
process with no capabilities (or `NET_ADMIN` over one pod's own network
namespace), behind seccomp, in a cgroup that admits exactly `/dev/kvm`
and `/dev/net/tun` -- and has to find a second, kernel-level escape to
go further. That second wall is the reason not to run privileged, and
the reason the README's "only `/dev/kvm`" is only true without it.

### What is still needed, and why

- **`/dev/kvm` through a device plugin.** It is mode `0666` on a GKE
  nested-virt node, so any user can open it once it is in the device
  cgroup. On a node where it is `0660 root:kvm`, a non-root pod needs
  that node's kvm GID in `supplementalGroups`; the plugin passes the
  node's ownership through unchanged.
- **`/dev/net/tun` and `NET_ADMIN`, for a guest with a network.** Inline
  networking (or `netshim`) creates a tap, splices it onto `eth0` with
  tc and builds the control link -- all inside the pod's own network
  namespace, which is as far as the capability reaches. `NET_RAW` is not
  needed. Because Kubernetes grants an added capability to root only
  (it sets no ambient capabilities), a networked pod runs as UID 0 --
  root with one capability, not root with the default set.
- **The split layout keeps `NET_ADMIN` out of the VM container.** There
  `netshim` holds the capability, as root, and only while it builds the
  taps. cloud-hypervisor and `granule run` then attach to those taps with
  no capabilities, as UID 65534, and these are the processes that parse
  guest input. Two things make that possible. `netshim` creates the taps
  owned by `NETSHIM_TAP_OWNER` (`UID:GID`, `0:0` when unset), and the
  kernel lets a process attach to a tap without `NET_ADMIN` only if its
  UID and GID match the tap's owner and group. And granule leaves `mtu=`
  off cloud-hypervisor's `--net` when the tap already has the pod
  interface's MTU, which `netshim` has set. Given `mtu=`,
  cloud-hypervisor sets the tap's MTU itself (`SIOCSIFMTU`), which needs
  `NET_ADMIN`. Without it, cloud-hypervisor reads the MTU off the tap
  and still advertises it to the guest. Pod Security Admission judges
  the pod as a whole, so the pod is still `privileged`\* because of
  `netshim`. The single-container `pod-example.yaml` is simpler and is
  still the default.
- **Writable scratch.** With a read-only root, `granule run` writes only
  to `/run` (its API and vsock sockets), `/tmp`, and the default overlay
  disk mode's copy-on-write layer in `/var/lib/granule/overlay`; the
  manifests give each an `emptyDir`. `CHV_DISK_MODE=persistent` writes
  the baked-in guest disk in place and needs `readOnlyRootFilesystem`
  dropped.

Where `privileged: true` remains, it is for want of a way to hand a
device over, not because granule needs the rest:

- The README's `kubectl debug --profile=sysadmin` debug VM: an ephemeral
  container cannot request an extended resource, so the only route to
  `/dev/kvm` is the profile's `privileged: true`.
- `demo/assets/*.yaml` and `benchmarks/gke/pod.template.yaml` still use
  `privileged: true` and a hostPath; they are demos and benchmarks, not
  deployment examples.
- Any cluster without a device plugin. A plugin is the only stock way to
  add a device to a container's cgroup. CDI (a spec under `/etc/cdi`
  plus a pod annotation, or Dynamic Resource Allocation) is the other,
  and needs node configuration that GKE does not offer without a
  DaemonSet anyway. If a cluster truly cannot run one, `privileged: true`
  plus the `/dev/kvm` hostPath still works -- with everything above
  accepted as the cost.

### How this was measured

All four pod manifests in this directory booted a guest and answered
`kubectl exec ... -- granule exec` on a `kind` cluster (v0.32.0,
Kubernetes v1.36.1, containerd 2.3.1, cgroup v2) with
`kvm-device-plugin.yaml` applied, nothing changed but the images
(pointed at locally loaded copies) and, for `gke-pod-example.yaml`, the
`kind` node's kvm GID in `supplementalGroups`, its `/dev/kvm` being
`0660`. On GKE (1.35.8-gke.1225000, one
`n2-standard-4` `COS_CONTAINERD` node with
`--enable-nested-virtualization`, containerd 2.2.7, private nodes) the
device plugin pulled and registered as checked in;
`gke-pod-example.yaml`, with nothing changed but the image, booted as UID 65534 with no capabilities and
answered `granule exec` and `granule resize -memory-mb=2048 -cpus=4`;
`gke-pod-exec-example.yaml` took over the pod IP and was pinged from
another pod at 0% loss; and the hostPath-only control still failed with
`VmCreate(Operation not permitted)`. The process status of
cloud-hypervisor and `granule run` in the running containers read
`CapEff: 0000000000001000` (`cap_net_admin`) for the inline pod and
`0000000000000000` for the networkless one, with `NoNewPrivs: 1` and
`Seccomp: 2` in both. On the same `kind` setup, `pod-split-example.yaml`
(with the baked-in guest in place of the `/images` hostPath, and the
node's kvm GID in `supplementalGroups`) booted a guest that answered
`granule exec` and was pinged on the pod IP with full-size, unfragmented
frames. cloud-hypervisor and `granule run` in its VM container read
`Uid: 65534`, `CapEff: 0000000000000000`, `NoNewPrivs: 1` and
`Seccomp: 2`. The VM container's `--net` carried no `mtu=`. Without
`NETSHIM_TAP_OWNER` the same container failed with
`TapOpen(ConfigureTap(... Operation not permitted))`, because the tap
was owned by root. With `mtu=` passed it failed with
`TapSetMtu(... Operation not permitted)`. `netshim` needed `NET_ADMIN`
and the device and nothing else. Outside Kubernetes, cloud-hypervisor
v53, run as an unprivileged user on a tap with MTU 1460 and given no
`mtu=`, gave the guest an `eth0` with MTU 1460.
Pod Security Admission, dry-run on the same `kind` cluster, admitted the
networkless pod at `restricted` and rejected every `NET_ADMIN` pod at
`baseline`.

## On a plain GCE instance

A cluster is not required to run granule on GCE. One instance created
with the same flag works, and is the smaller answer when a cluster is
not otherwise wanted:

```sh
gcloud compute instances create granule-host \
  --zone us-east1-b \
  --machine-type n2-standard-8 \
  --image-family debian-12 --image-project debian-cloud \
  --enable-nested-virtualization
```

Nested virtualisation needs N1/N2/N2D/C2, Haswell or later -- the same
families as the node pool above, and x86 only, so an instance runs the
**amd64** guest, never the arm64 one. E2 does not support it and neither
does T2A.

Confirm it the same way: `/dev/kvm` should exist, and `kvm-ok` from
`cpu-checker` should agree. Both can pass on a machine where
acceleration silently falls back to emulation, though, so the check
worth running is a real one -- `qemu-system-x86_64 -accel kvm` booting
the host's own kernel exits non-zero rather than falling back if KVM is
not usable:

```sh
sudo qemu-system-x86_64 -accel kvm -m 512 -nographic -no-reboot \
  -kernel /boot/vmlinuz-$(uname -r) -append 'console=ttyS0 panic=-1'
```

A granule VM on an emulated CPU boots and is unusably slow rather than
failing, which is a more expensive thing to discover later.

Two operational notes for a long-lived host:

- `gcloud compute instances suspend` / `resume` work on a nested-virt
  instance. Measured: ~30s down, ~10s up, `/dev/kvm` intact afterwards,
  and the instance clock back within a few seconds. A suspended
  instance bills its disk and not its vCPU.
- **A resumed instance comes back with its processes still running.**
  Suspend preserves RAM, so "my ssh session ended" is not "the VM
  manager stopped" -- worth knowing before assuming a resumed host is a
  fresh one.

N2 capacity is not uniform: `ZONE_RESOURCE_POOL_EXHAUSTED` is a common
answer in us-central1, and shopping zones is often faster than waiting.

## Validated

Everything in this file has been run end to end on a real GKE Standard
cluster (not Autopilot -- its restricted `securityContext` would reject
`privileged: true`), created as section 1 describes -- the same flags,
with a roomier machine type and the node service account noted below:
GKE 1.35.7-gke.1027000 on the REGULAR channel, one `n2-standard-8`
`COS_CONTAINERD` node with `--enable-nested-virtualization`,
`containerd://2.1.9`, node kernel `6.12.94+`, Intel Xeon with `vmx`. Both
manifests were applied exactly as they are checked in here, with nothing
substituted but `IMAGE_PLACEHOLDER`, for an image built from this tree
and pushed to Artifact Registry.

### The GKE-specific mechanics

- **`--enable-nested-virtualization` works as advertised.** The node pool
  comes back with `advancedMachineFeatures.enableNestedVirtualization:
  True`, and `/dev/kvm` on the node is `crw-rw-rw-  root 409  10, 232` --
  exactly the world-accessible mode section 1 predicts. No custom node
  image and no VMX license juggling, and nothing in granule had to change
  for it.
- **The Artifact Registry pull needs nothing in the manifest**: no
  `imagePullSecrets`, just the node pool's own service account with read
  on the repo -- `Successfully pulled image ... in 5.317s. Image size:
  140082919 bytes`. This pass ran the node pool as a non-default service
  account (`--service-account=...` on the create above, because the
  identity doing the creating had no `iam.serviceAccountUser` on the
  default Compute Engine one), which is the "unless overridden" case
  section 2 mentions, and it pulled with no further setup.
- **A `/dev/kvm` hostPath alone is not enough for an unprivileged
  container**, on this much newer runtime as much as on the one that
  first found it. With `privileged: false` and only the hostPath,
  cloud-hypervisor fails `CreateHypervisor(VmCreate(Operation not
  permitted (os error 1)))`. That is the container's device cgroup, not
  the file's own permissions -- which is why these passes ran
  privileged, and why the manifests now get the device from
  `kvm-device-plugin.yaml` instead (see "Privileges").
- **`netshim` without privilege died at `creating tap tap-web: no such
  file or directory`**, because a non-privileged pod has no
  `/dev/net/tun`, which the netlink library opens to create the tap.
  The read-only `/proc/sys/net` masking the first pass blamed is real,
  but netshim never writes there. Given `/dev/net/tun` through the
  device plugin, `NET_ADMIN` alone is enough; see "Privileges".

### The flows, on that cluster

- Both manifests boot. `gke-pod-example.yaml` reached a guest login
  prompt in `kubectl logs`; `gke-pod-exec-example.yaml` configured inline
  networking and booted its VM container, with `kubectl exec ... -- granule exec -- uname -a`
  answering promptly after `kubectl apply` on a node that already had the image.
- **`kubectl exec ... -- granule exec` works on the `netshim`-less pod**,
  whose guest really does have no interface but `lo` (`ip -o addr` in the
  guest returns exactly one line). Exec is vsock, so section 4 above is
  right that no network and no control link are involved.
- **Pod-to-pod traffic to the guest did not work on GKE either** -- on
  the build that pass ran, which predates the routes fix; a later pass
  re-ran both probes against a build carrying it, and they pass. This
  was the open question the previous pass left,
  and the answer was that nothing in GKE's configuration saves it. GKE's
  node CNI config
  (`/etc/cni/net.d/10-containerd-net.conflist`) is a `ptp` plugin over
  `host-local` IPAM, and the splice leaves the guest holding
  `10.84.0.15/24` with an on-link `10.84.0.0/24 dev eth0` route. From
  inside the guest the gateway (`10.84.0.1`, `REACHABLE` in its ARP
  table), the node (`10.128.0.15:10250`) and a ClusterIP
  (`34.118.224.1:443`) are all fine; another pod's address is not:
  - guest -> another pod (`10.84.0.14:8080`): `No route to host`, and
    `10.84.0.14 dev eth0 FAILED` left behind in the guest's ARP table --
    it ARPed for a peer it believes is on-link, and nothing answered
    (`proxy_arp` is `0` on the pod's host-side veth, and on `all`).
  - another pod -> the guest's nginx on the pod IP: `curl` times out
    (exit 28), `ping` is 100% loss -- while the *node* fetches the same
    nginx over the same address with a `200`. The asymmetry is the tell:
    a request from the node has a source outside the pod subnet, so the
    guest routes its reply via the gateway it can reach; a request from
    another pod has a source inside it, so the guest tries to reply
    on-link and ARPs into a segment that is not there.

  The mechanism is written up in the main README's "Container
  networking".

  **Both probes above are a *before*.** The fix landed on `main` while
  that pass was running and was not in the image it tested:
  `granule.routes=` carries the interface's real routing table into the
  guest rather than one inferred from the netmask (README, "Container
  networking" -> "The CNI's routes"). A later pass, on a second cluster
  created with the same section 1 flags and the same GKE version, ran
  the same two probes against a build carrying it. Both work:

  - guest -> another pod (`10.28.0.13:80`, an `nginx:alpine` pod):
    `200` and the page body, `ping` 3/3 at 0% loss, and one entry in the
    guest's ARP table afterwards -- its gateway. It no longer ARPs for
    the peer at all.
  - another pod -> a listener in the guest (`10.28.0.15:8080`): `200`
    from a pod on the same node, from a pod on a second node
    (`10.28.1.5`), and through a `ClusterIP` Service selecting the pod;
    `ping` 0% loss from each. The node still fetches it as well.

  Nothing that worked before regressed: from the guest, the gateway (0%
  loss), the node (`10.128.0.17:10250`), a ClusterIP
  (`34.118.224.1:443`) and public DNS through `8.8.8.8` all still
  answer, and `nslookup kubernetes.default.svc.cluster.local
  34.118.224.10` resolves -- cluster DNS being the same pod-to-pod path
  once more, over UDP through `kube-dns`'s ClusterIP. The guest's table is now a copy of the pod's --
  `10.28.0.1 dev eth0 scope link`, `10.28.0.0/24 via 10.28.0.1`,
  `default via 10.28.0.1` -- from a command line carrying
  `granule.routes=10.28.0.1/32,10.28.0.0/24@10.28.0.1,0.0.0.0/0@10.28.0.1`
  beside the usual `ip=`, with the on-link `/24` that `ip=` left behind
  dropped for want of a CNI route to replace it.

  That pass also corrects one point of mechanism the earlier one got
  wrong: GKE's `ptp` **does** install the explicit
  `<subnet> via <gateway>` route, exactly as a `kind` `ptp` cluster
  does. Only the default route appears in the conflist's own `routes`
  list, which is where "GKE has only a default route" came from; the
  `ptp` plugin adds the gateway host route and the subnet route itself,
  whatever IPAM was asked for. An ordinary pod on this cluster has
  `default via 10.28.0.1`, `10.28.0.0/24 via 10.28.0.1 src 10.28.0.14`
  and `10.28.0.1 dev eth0 scope link src 10.28.0.14`.

  Two caveats on that pass. The image was built with `--build-arg
  GUEST_DISTRO=alpine`, because `debootstrap` kept losing a `.deb` or
  two on the build host's egress; `granule-configure-routes` is the same
  script under both guest variants, enabled on both. And the guest's
  listener was a small static HTTP server copied in with `kubectl exec
  -i ... -- granule cp -mode 0755 - /usr/local/bin/probe` and started
  over `granule exec`, the reference guest shipping no `httpd` of its
  own.

  **A pass beside it ran the same two probes on `kind`**, the cluster
  shape that first found the gap: kind v0.32.0, `kindest/node:v1.36.1`,
  kindnet's `ptp` + `portmap`, the host's `/dev/kvm` mounted into the
  node container with `extraMounts` so this manifest's `hostPath`
  resolves, pods on `10.244.0.0/24` -- and on a GKE cluster of its own
  (same section 1 flags, pods on `10.60.0.0/24`), with the default
  Debian guest plus `nginx`. Both probes reversed on both clusters, the
  guest's `ip route` came up equal to its container's,
  `granule-net-routes.service` `active (exited)`, and `kubectl logs
  <pod> -c web` named the three routes carried. The gateway, the node's
  kubelet, the `kubernetes` ClusterIP (`10.96.0.1:443` on `kind`), a
  ClusterIP in front of the peer pod, egress and the node's own fetch of
  the guest's `nginx` all still worked.
- **`granule resize` against the ceilings these manifests now carry**:
  `kubectl exec ... -- granule resize -memory-mb=2048 -cpus=4` took the
  running guest from 1000828 kB to 2049404 kB of `MemTotal` and added
  `cpu2`/`cpu3` (`nproc` still says 2 until the guest onlines them).
- **`CHV_SETUP_SCRIPT` in a pod spec**: its output lands in `kubectl logs
  <pod> -c web` followed by `granule: setup script complete`, the file it
  wrote was there afterwards, and the pod stayed `Running`. A script
  exiting 3 instead logs `granule: setup script exited with status 3` and
  fails the container.
- **Flow 4's `granule cp` through `kubectl exec`**, which had never run on
  a cluster: `kubectl exec -i ... -- granule cp - /srv/app.tar` and
  `kubectl exec ... -- granule cp /srv/app.tar -` round-trip a 100KB
  random binary byte for byte (matching md5 at both ends), the `-tar`
  form round-trips a directory, stdout and stderr stay separate with no
  CRLF rewriting, and dropping `-i` does exactly what the README warns:
  exit 0 and a 0-byte file in the guest.
- **Flow 2's GKE half**: a `granule guest build` image built
  off-cluster, pushed, and named in the manifest with nothing else
  changed -- `systemctl is-active nginx` answers `active` in the pod.
- **`granule exec` still becomes available before the guest's own units
  finish starting**, but by a much smaller margin here than the `kind`
  pass measured. From the guest's own monotonic clock:
  `granule-agent.service` active at 1.816s, `nginx.service` at 2.158s,
  `multi-user.target` at 2.279s -- a 0.34s window rather than ~3s, with
  the whole guest booting in 2.3s. A single-shot `systemctl is-active`
  fired the instant the first exec succeeds can still lose that race, so
  the retry Flow 2 asks for is still the right advice; it just usually
  wins on the first go here. End to end, `kubectl apply` to first
  successful exec was 20s on a node that already had the image.
- `kubectl delete pod --wait` returned in about 1.5s, well inside
  `terminationGracePeriodSeconds: 40`.

The off-cluster half of that Flow 2 build hit one thing worth recording:
`granule guest build` failed on two of three attempts on the build
host, having staged its setup script in a `/tmp` the guest's boot then
emptied under it. Nothing to do with GKE, and fixed since -- the script
is staged outside `/tmp` and the build waits out the guest's boot before
provisioning (see `pkg/guestbuild`).

### The earlier passes

Two earlier passes are folded into the above rather than kept separately,
since this one re-ran what they covered on the platform they were about:

- The **first GKE pass** established the mechanics under "The
  GKE-specific mechanics" on a much older cluster, back when
  `gke-pod-example.yaml` still had a `fetch-kernel` init container
  staging a kernel into an `emptyDir`. Every one of its findings still
  holds, except that it attributed `netshim`'s need for `privileged` to
  read-only `/proc/sys/net`; see above for what it actually hits first.
- A **stand-in pass on a `kind` cluster** (Kubernetes v1.34.0 on a
  nested-virt host, the host's `/dev/kvm` mounted into the node
  container) was run because the machine it ran on had no GCP
  credentials. It is what put the hotplug ceilings into both manifests,
  removed the `GRANULE_EXEC_ADDR` the code no longer reads, and first
  measured the pod-to-pod gap -- flagging it as the thing to check on
  GKE. Every measurement it made has now been repeated on GKE, and each
  one came out the same, the pod-to-pod gap included. That gap was fixed
  on `main` after both passes and before either could measure the fix;
  two passes since have, on GKE and on a `kind` `ptp` cluster -- see the
  pod-to-pod bullet above.
