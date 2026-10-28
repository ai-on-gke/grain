# Granule

**Self-Contained VM Containers for Kubernetes and Docker**  
*(formerly Kontur)*

`granule` packages self-contained microVMs into standard OCI container images for frictionless distribution, execution, and isolation. Each image encapsulates the guest Linux kernel, lightweight hypervisor runtime ([Cloud-Hypervisor](https://www.cloudhypervisor.org/)), and rootfs layers.

The same image runs natively under Docker, containerd, and vanilla Kubernetes with zero custom host shims and no CRI plugins. What it needs from the host is two device nodes passed into an unprivileged container: `/dev/kvm`, and `/dev/net/tun` when the guest has a network. Docker passes them with `--device`; on Kubernetes a small, generic device-plugin DaemonSet ([`deploy/k8s/kvm-device-plugin.yaml`](deploy/k8s/kvm-device-plugin.yaml)) does. No container needs `privileged: true` (see [Privileges](deploy/k8s/gke.md#privileges) for what that would cost).

---

## Executive Summary: Granule at a Glance

* **Standard OCI Artifacts & Distribution** (*Docker Hub · GHCR · AWS ECR · Artifact Registry*)
  * **Self-Contained VM Images**: Encapsulates guest kernel, lightweight hypervisor runtime, and rootfs into standard container layers.
  * **Familiar Tooling**: Push, pull, version, and scan using standard Docker and OCI container workflows.
* **Sub-Second Boot & Hardware VM Isolation** (*<100ms MicroVM Startup · Zero Data Duplication*)
  * **Strong Isolation**: Hardware-level KVM virtualization boundary around every guest container.
  * **Layered Storage**: Sparse Copy-on-Write (QCow2) disk chains eliminate redundant disk block duplication.
* **Zero Host Setup & Orchestration Compatibility** (*Vanilla Kubernetes · Unmodified Docker & Containerd*)
  * **Zero Host Modification**: Runs on vanilla nodes with `/dev/kvm` access—no custom hypervisors or CRI plugins, and no privileged containers.
  * **Native Orchestration**: Managed directly via standard `kubectl run`, Pod specs, and Helm charts.
* **Native Ergonomics & Spliced Datapaths** (*`kubectl exec` / `logs` · In-Kernel `splice()` Networking*)
  * **Direct Control Plane**: Exec, logs, and signals communicate via Point-to-Point UDS/vsock agent sockets.
  * **Direct Datapath**: In-kernel `splice()` and direct disk I/O route packets with zero supervisor bottleneck.

---

## Granule puts VMs in containers, not containers in VMs

Traditional VM-isolated container architectures (such as Kata Containers) run containers inside hypervisors managed by custom host-level runtime plugins (CRI / shim-v2):

```
Traditional (Kata):
[ Host / Kubelet ] ──> [ Kata Shim ] ──> [ VM ] ──> [ Container Runtime ] ──> [ App Container ]
```

**Granule reverses this relationship**:

```
Granule:
[ Host / Kubelet ] ──> [ Standard OCI Container ] ──> [ Granule Supervisor ] ──> [ MicroVM ] ──> [ App Workload ]
```

By wrapping the microVM lifecycle inside a standard OCI container:
* The host container engine (Docker, containerd, CRI-O) treats Granule as an ordinary container.
* Kubernetes manages Pods, sidecars, lifecycle probes, resource limits, and networking naturally.
* No node-level configuration, custom RuntimeClasses, or daemon installations are needed.

---

## Section 01: Using Granule Images

### Running on Unmodified Kubernetes

Granule images deploy onto any standard Kubernetes cluster where worker nodes have `/dev/kvm` access. Install the device plugin once per cluster; it publishes `/dev/kvm` and `/dev/net/tun` as the resources `devic.es/kvm` and `devic.es/tun`, which is what puts each device in a pod's device cgroup without making the pod privileged:

```sh
# 0. Once per cluster
kubectl apply -f deploy/k8s/kvm-device-plugin.yaml
```

```yaml
# 1. Apply a Pod that asks for the two devices
cat << EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: myvm
spec:
  securityContext:
    seccompProfile: { type: RuntimeDefault }
  containers:
  - name: myvm
    image: granule/alpine:latest
    resources:
      limits: { devic.es/kvm: "1", devic.es/tun: "1" }
    securityContext:
      capabilities: { drop: [ALL], add: [NET_ADMIN] }
      allowPrivilegeEscalation: false
EOF
```

`NET_ADMIN` is for the guest's network, and reaches no further than the pod's own network namespace. A guest with no network (`CHV_NET_MODE=none`) needs no capability and no `devic.es/tun`, and can run as a non-root user with a read-only root filesystem: see [`deploy/k8s/gke-pod-example.yaml`](deploy/k8s/gke-pod-example.yaml), which passes the `restricted` Pod Security Standard.

```sh
# 2. Capture guest VM boot & kernel logs
kubectl logs -f myvm

# 3. Exec directly inside the running VM
kubectl exec -it myvm -- sh
```

* **Host Integration**: Runs as a standard, unprivileged Pod on any Kubernetes node whose `/dev/kvm` a device plugin publishes. A `/dev/kvm` hostPath is not enough on its own: the device is not in the container's device cgroup, and cloud-hypervisor fails with `EPERM`.
* **Guest Console Streaming**: `kubectl logs` streams the guest VM kernel log and serial console output live.
* **Guest Exec**: `kubectl exec` connects over vsock/UDS directly into the VM workload via the in-guest agent.
* **Network Transparency**: The Pod IP and ClusterIP service traffic route straight into the guest network interface.
  The guest takes over the address, MAC and MTU of the sandbox interface carrying the default route (IPv4 or IPv6),
  found inside the VM container at boot (or, with no default route, the only non-loopback interface with an IPv4 or
  global IPv6 address), so it need not be called `eth0`. Where that is ambiguous -- several networks attached, Multus
  secondary interfaces, IPv4 and IPv6 default routes on different interfaces -- name it with `NETSHIM_EXTERNAL_IFACE`
  (`granule vm create -external-iface`). The guest takes the pod's IPv6 identity too: a dual-stack pod's guest
  holds both its addresses, and an IPv6-only pod's guest is IPv6-only, with an IPv6 nameserver. `NETSHIM_IPV6=off`
  (`granule vm create -ipv6 off`) discards the IPv6 half altogether.
* **Native Lifecycle**: Seamlessly integrates with Kubernetes readiness/liveness probes, resource limits, and scheduling.

---

### Multi-VM Pods: VM per Container, Many Containers per Pod

Kubernetes Pods can run multiple containers, each executing in its own isolated microVM:

```yaml
# 1. Multi-VM Pod: App VM + Isolated Sidecar VM
cat << EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: multi-vm-pod
spec:
  containers:
  - name: app-vm
    image: granule/alpine:latest
    resources: { limits: { devic.es/kvm: "1", devic.es/tun: "1" } }
    securityContext: { capabilities: { drop: [ALL], add: [NET_ADMIN] } }
  - name: sidecar-vm
    image: granule/alpine:latest
    env: [{ name: CHV_NET_MODE, value: none }]
    resources: { limits: { devic.es/kvm: "1" } }
    securityContext: { capabilities: { drop: [ALL] } }
EOF
```

```sh
# 2. Attach an Ephemeral Debug VM dynamically
kubectl debug multi-vm-pod \
  --image=granule/alpine:latest \
  --profile=sysadmin \
  -c debug-vm

# 3. Exec directly into the Ephemeral Debug VM
kubectl exec multi-vm-pod -c debug-vm -- sh
```

* **1:1 Container-to-VM Mapping**: Each container in a Pod runs its own independent microVM with its own isolated guest kernel.
* **Isolated Sidecars**: Service proxies and agents run in dedicated VMs without sharing guest kernels or memory spaces.
* **Ephemeral Debug Pods**: Dynamically attach a temporary debug microVM via `kubectl debug` to live running pods. This is the one flow that still runs privileged: `--profile=sysadmin` sets `privileged: true`, because an ephemeral container cannot request a device-plugin resource and so has no other way to reach `/dev/kvm`. Treat it as node-level access (see [Privileges](deploy/k8s/gke.md#privileges)) and keep it to clusters and namespaces where that is acceptable.
* **Independent Lifecycle**: Kubernetes manages health probes, restarts, and resource limits per container/VM.
* **Shared Pod Namespace**: The Pod network namespace and shared volumes bridge cleanly across multiple VM containers.

---

### Running on Unmodified Docker

Launch microVM containers on Docker using standard CLI commands:

```sh
# 1. Run container on host with /dev/kvm and /dev/net/tun mapped
docker run -d --name myvm \
  --device /dev/kvm \
  --device /dev/net/tun \
  --cap-drop=ALL --cap-add=NET_ADMIN \
  --security-opt no-new-privileges \
  -p 8080:80 \
  granule/alpine:latest

# A guest with no network needs no capability at all, and runs as any user
# in the host's kvm group with a read-only root:
docker run -d --name myvm --device /dev/kvm --cap-drop=ALL \
  --security-opt no-new-privileges --user 65534 --group-add "$(getent group kvm | cut -d: -f3)" \
  --read-only --tmpfs /run --tmpfs /tmp --tmpfs /var/lib/granule/overlay \
  -e CHV_NET_MODE=none granule/alpine:latest

# 2. Stream guest VM console logs to host
docker logs -f myvm

# 3. Exec inside the running VM
docker exec -it myvm sh
```

* **Device Mapping**: Launches microVM with `--device /dev/kvm` and `--device /dev/net/tun`. `--privileged` also works, and gives the container the run of the host; nothing in granule needs it.
* **Console Logging**: `docker logs` streams the guest VM serial console output live.
* **In-VM Exec**: `docker exec` connects over vsock directly into the running VM.
* **Port Forwarding**: Standard Docker port mappings (`-p 8080:80`) route traffic straight to guest ports.
* **Graceful Termination**: `docker stop` sends a graceful ACPI power button event to the guest kernel.

#### Checking a new host: `granule doctor`

Before the first VM on a host that is not already known to work (bare metal, another cloud, WSL2, rootless docker, an SELinux distribution, a minimal kernel), run `granule doctor`. It reports one line per requirement -- `/dev/kvm` and whether it can be opened, the KVM module and nested virtualization, `/dev/net/tun`, the container daemon (and whether it is rootless), the bridge/tap/ingress-qdisc/mirred kernel pieces netshim uses (probed in a scratch network namespace when run as root), the architecture, SELinux/AppArmor, and io_uring -- each `PASS`, `WARN`, `FAIL` or `SKIP` with what to do about it, and exits non-zero when a required check fails. `-only kvm,tun` runs and judges just the named checks.

#### Using a container CLI other than `docker`

By default every `granule` command execs `docker` from `PATH`. To drive a different binary, set `GRANULE_DOCKER` to a path, or to a name that is looked up on `PATH`. You can also pass `-docker` before the command, which sets the same variable for that run:

```sh
GRANULE_DOCKER=/opt/docker/bin/docker granule vm create myvm --image granule/alpine:latest
granule -docker podman vm list
```

The setting covers everything granule execs: `vm create`/`delete`/`exec`/`wait`/`status`/`list`, `guest build`/`pack`/`rebase`/`verify`, and `granulefile build`.

This setting only selects the binary. It does not paper over CLI differences. Granule is tested against the Docker CLI only, through a fake that speaks Docker's dialect. Podman and nerdctl have **not** been run end to end with granule, so treat them as untested. Here is what granule relies on that another CLI has to match:

* **`run` flags**: VM containers are started with `--device /dev/kvm --device /dev/net/tun --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges` and `--label granule.dev/vm=<name>`. Rootless podman and nerdctl handle devices, capability sets and `no-new-privileges` differently, and `/dev/kvm` must be accessible to the user the runtime maps to. In a user namespace whose root is a subuid, that means the host's kvm group kept as a supplementary group; [docs/rootless.md](docs/rootless.md) lists what a rootless runtime such as crun has to provide, and what does not work.
* **`inspect -f` Go templates**: granule reads `.State.Running`, `.Config.Env` and `.Config.ExposedPorts` this way. The answer has to use Docker's field names.
* **Error wording**: granule recognises a missing container from Docker's `No such container` and `No such object` messages, and an unresolvable image from BuildKit's (or the classic builder's) wording. A CLI that words these differently turns an "already gone" into a hard failure.
* **Builds**: the `guest` and `granulefile build` paths run `<cli> build --file … --tag …` with multi-stage `COPY --from=<image>`. On current Docker that is BuildKit (`docker buildx`), using the caller's builder, credentials and proxy settings. `podman build` (Buildah) and `nerdctl build` (needs `buildkitd`) differ in caching, progress output and error messages.
* **The `podman-docker` shim** (`docker` → `podman`) goes through exactly the same calls as `GRANULE_DOCKER=podman`, with the same caveats.

---

## Section 02: Building Granule Images

Granule introduces the **Granulefile** format—a strict superset of Dockerfile syntax that supports authoring, modifying, and packaging bootable microVMs. See [The Granule Specification](docs/granulefile.md) for full syntax details.

### A Dockerfile is a Granule File

Every valid Dockerfile is a valid Granule file with identical semantics:

```dockerfile
FROM alpine:latest

# Runs in a normal alpine container
RUN apk add --no-cache redis

# Standard container CMD
CMD ["redis-server", "--protected-mode", "no"]
```

* Standard instructions (`FROM`, `RUN`, `COPY`, `ENV`, `CMD`) execute in normal container stages.
* Builds standard OCI container layers offline—no `/dev/kvm` needed.
* Serves as the foundation for adding VM stages (`VM FROM`, `VM PACK`, `VM WRAP`).

---

### Live In-VM Execution (`VM FROM`)

Execute build steps inside a live booted microVM:

```dockerfile
# Inherits from a bootable VM image
VM FROM granule/alpine:latest

# Injects files directly into VM disk
COPY redis.conf /etc/redis/

# Runs inside the live VM during build
RUN apk add --no-cache redis

# VM runs this on boot
CMD ["redis-server", "/etc/redis/redis.conf"]
```

* **VM Stage**: Inherits directly from a bootable microVM image.
* **Live in-VM Execution**: `RUN` commands execute inside the live booted VM.
* **Build Requirement**: Requires `/dev/kvm` on the build machine.
* **Boot Behavior**: Guest microVM runs `CMD` directly on boot.

---

### Multi-Stage Granulefiles

Isolate build dependencies and toolchains in builder VMs:

```dockerfile
# Stage 1: Build environment in a live VM
VM FROM granule/alpine:latest AS builder
RUN apk add --no-cache gcc make musl-dev
COPY src/ /src/
RUN make -C /src

# Stage 2: Clean runtime microVM
VM FROM granule/alpine:latest
COPY --from=builder /src/redis-server /usr/local/bin/
COPY redis.conf /etc/redis/

# VM runs this on boot
CMD ["redis-server", "/etc/redis/redis.conf"]
```

* **VM-to-VM Multi-Stage**: Both builder and runtime execute in microVMs.
* **Zero Toolchain Bloat**: Compilers and build headers stay in the builder VM.
* **Cross-VM Copy**: `COPY --from=builder` copies files between VM disks.
* **Clean Production VM**: Final microVM contains only the runtime binary and configuration.

---

### Mix and Match: Offline Rootfs Composition (`VM PACK`)

Build a custom rootfs in an offline container stage, then pack it into a bootable VM:

```dockerfile
# Container stage: Modifies VM rootfs offline
FROM granule/alpine.rootfs:latest

COPY redis.conf /etc/redis/
RUN apk add --no-cache redis
CMD ["redis-server", "/etc/redis/redis.conf"]

# Builds a bootable vm image from the rootfs (offline)
VM PACK AS redis_vm
```

* **Container Stage**: `COPY` and `RUN` modify the guest OS rootfs offline without virtualization.
* **VM Stage**: `VM PACK` installs VM boot scripts, kernel agents, and generates a bootable raw disk.
* **Build Requirement**: Zero virtualization needed during build (no `/dev/kvm` required).
* **Runtime**: Boots a dedicated microVM directly with the custom rootfs.

---

### Container Wrapping (`VM WRAP`)

Embed an existing container image into a dedicated microVM:

```dockerfile
FROM alpine:latest

# Runs in a normal alpine container
RUN apk add --no-cache redis
CMD ["redis-server", "--protected-mode", "no"]

# Builds a vm image with the previous container running inside
VM WRAP redis AS redis_vm
```

* **Container Stage**: Builds standard OCI image layers offline.
* **VM Stage**: `VM WRAP` packages the container rootfs into a bootable VM disk.
* **Runtime**: The container entrypoint executes inside its own guest VM.
* **Build Requirement**: Runs entirely offline—no `/dev/kvm` required.
* **Per-VM process**: one wrapped image can run as many containers, each with its own process, without being rewrapped. Set `CHV_CONTAINER_OVERRIDE` on the VM's container to a JSON object with any of `entrypoint`, `cmd`, `env`, `user` and `working_dir`; it is merged over the wrapped spec the way `docker run` merges its flags (a new `entrypoint` drops the image's `cmd` unless `cmd` is given too, and `env` entries are set over the image's by key). It reaches the guest on a small read-only disk of its own, and `granule exec --container` enters the merged container.

```sh
docker run ... -e CHV_CONTAINER_OVERRIDE='{"cmd":["redis-server","--port","6380"],"env":["TZ=UTC"]}' redis_vm
```

* **Volumes**: a virtio-fs share given a destination in `CHV_FS` or `CHV_VOLUMES` (`tag:/run/vfs.sock:/data[:ro]`, or `tag=...,socket=...,destination=/data`) is mounted at that path inside the wrapped container before its process starts, resolved so it cannot escape the container's root, and read-only when asked. `granule exec --container` sees it too. See [docs/volumes.md](docs/volumes.md).

* **Separate output streams**: the wrapped container's stdout and stderr reach the VM's console (and so `docker logs`) merged with the kernel's and init's, as they always have. A caller that needs them apart -- a container runtime keeping CRI's stream identity -- sets `GRANULE_WORKLOAD_STDOUT` and/or `GRANULE_WORKLOAD_STDERR` on the VM's container to absolute paths: regular files are appended to, and FIFOs are waited on until something reads them. Each stream arrives on its own over vsock from `granule-container` (see `pkg/workloadio`), which keeps up to 4 MiB of what the workload writes before anything is attached, and before a finished workload's VM powers off waits up to 5s for the host to have the rest.

* **Stdin and terminal**: a wrapped container reads `/dev/null` unless asked otherwise. `GRANULE_WORKLOAD_STDIN` on the VM's container, an absolute path, starts it with its stdin open (`docker run -i`): a FIFO is waited on for a writer, or a regular file is read, and what is written reaches the workload over the same vsock connection as its output. When the writer closes the FIFO, or the file ends, the workload reads end-of-file. `GRANULE_WORKLOAD_TTY=1` runs it on a pseudo-terminal instead (`docker run -t`), so its output arrives as one stream on `GRANULE_WORKLOAD_STDOUT`, and `GRANULE_WORKLOAD_RESIZE` names a FIFO that `COLS ROWS` lines are written to, to size the terminal. A FIFO is reopened after each writer, so a caller can open it once per resize. On a terminal, end-of-file is delivered as the terminal's EOF character, which a reader in raw mode does not treat as end-of-file. Both settings travel to the guest as the container override's `stdin` and `tty` fields, which `CHV_CONTAINER_OVERRIDE` can also set.
* **Exit status**: the VM's container exits with the wrapped container's own exit status (128+N for signal N), so `docker run` of a wrapped image returns what `docker run` of the original would have. `granule-container` reports it over vsock (host port 1026) before the VM powers off. A VM whose workload started but never reported an exit -- the guest crashed, or was stopped before the workload finished -- exits with 125 instead. A caller that is not the container's parent sets `GRANULE_WORKLOAD_EXIT_FILE` to an absolute path. Once the VM has stopped, that file holds `{"exitCode":N,"reported":true|false,"reason":...}`, and `reported` tells a workload that exited 125 apart from one that never reported. Setting it also marks the VM as a wrapped one: if nothing was reported at all, it gets 125 rather than cloud-hypervisor's exit code.

---

### Granulefile CLI Tooling

Validate and build Granulefiles using the `granule` CLI:

```sh
# Validate a Granulefile without building
granule granulefile check -f Granulefile

# Build a Granulefile into a bootable VM image
granule granulefile build -f Granulefile -t my-custom-vm:latest

# Print the generated multi-stage Dockerfile pipeline
granule granulefile build -f Granulefile -print
```

---

## Section 03: Granule Architecture & Storage

### Key Architectural Properties

* **Runs on Unmodified OCI Runtimes & Orchestration**: Executes natively under standard runc, containerd, Docker, and vanilla Kubernetes. Requires zero custom host shims and no CRI plugins—only `/dev/kvm` (plus `/dev/net/tun` for guest networking) handed to an unprivileged container, which on Kubernetes is a generic device plugin's job.
* **Self-Contained, Portable OCI Artifacts**: Each image encapsulates the hypervisor runtime, guest Linux kernel, initramfs, and rootfs into standard OCI layers that push, pull, cache, and scan across standard container registries (Docker Hub, GHCR, Artifact Registry).
* **Zero Datapath Interposition**: The granule supervisor is completely out of high-throughput datapaths. Network packets stream directly between virtio-net / VMM and host kernel (`tap0` ↔ `veth`) via in-kernel `splice()`; storage routes directly to flat multi-extent VMDK / raw disk files.
* **Control Plane Independent of Guest Networking**: `kubectl exec`, `docker exec`, logs, signals, and exit codes communicate directly via point-to-point Unix Domain Sockets (UDS) / vsock to the in-guest agent—fully functional even with no IP, strict firewall rules, or broken networking.
* **Sub-Second MicroVM Boot & Isolation**: Boots in milliseconds (<100ms) with copy-on-write sparse disk layering, delivering strong hardware-level VM isolation while preserving the ergonomics and density of standard containers.

---

### Under the Hood: Runtime Architecture

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│ GRANULE CONTAINER (Host Namespace)                                              │
│                                                                                 │
│  ┌───────────────────────┐             ┌─────────────────────────────────────┐  │
│  │ granule (PID 1)       │             │ cloud-hypervisor (VMM)              │  │
│  │ VM Supervisor         │             │                                     │  │
│  │                       │             │  ┌───────────────────────────────┐  │  │
│  │ /bin/bash (exec shim) ├─(UDS/vsock)─┼─>│ Guest microVM                 │  │  │
│  └───────────────────────┘             │  │                               │  │  │
│                                        │  │ ┌───────────────────────────┐ │  │  │
│  CONTROL PLANE:                        │  │ │ granule-agent (daemon)    │ │  │  │
│  kubectl / docker exec / logs ─────────┼──┼─┼─> Workload Process        │ │  │  │
│                                        │  │ └───────────────────────────┘ │  │  │
│                                        │  │                               │  │  │
│                                        │  │ Storage:                      │  │  │
│                                        │  │ • disk.img (base, read-only)  │  │  │
│                                        │  │ • disk.qcow2 (2MB clusters)   │  │  │
│                                        │  └──────────────┬────────────────┘  │  │
│                                        └─────────────────┼───────────────────┘  │
│                                                          │ (virtio-net)         │
│  DATA PLANE (NETWORK):                                   ▼                      │
│  ┌──────────────────────────────────────────────────────────────┐               │
│  │ tap0 (VMM NIC) <──────── splice() (zero-copy) ────────> veth │               │
│  └──────────────────────────────────────────────────────────────┘               │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

### Inside the Granule Container: Filesystem & Asset Layout

```
/ (Container Filesystem)
├── granule                   # PID 1 VM supervisor
├── bin/bash -> /granule      # Command symlink (exec shim)
├── usr/bin/cloud-hypervisor  # Lightweight VMM binary
├── vmlinux                   # Guest Linux kernel binary
├── disk.img                  # Base rootfs (read-only)
├── overlay/                  # Overlay directory
│   └── disk.qcow2            # QCOW2 overlay (2MB clusters, backing disk.img)
├── etc/granule/config.json   # MicroVM configuration
└── run/granule/              # Ephemeral IPC sockets (tmpfs)
    ├── vmm.sock              # Hypervisor API socket
    └── agent.sock            # UDS / vsock agent socket

/ (Guest MicroVM Rootfs — disk.qcow2 overlay)
├── sbin/init                 # Guest init system (PID 1)
├── usr/bin/granule-agent     # In-guest agent (exec/logs)
├── bin/bash                  # Target workload shell
├── etc/                      # Guest OS configuration
└── app/...                   # Workload application payload
```

* **Self-Contained OCI Packaging**: All virtualization assets (kernel, VMM hypervisor binary, base disk image, and configuration) are packaged into standard OCI container layers.
* **QCOW2 Overlays with 2MB Clusters**: Writable disks use QCOW2 overlays with 2MB cluster size (`disk.qcow2`) pointing to the base image. On reflink-capable host filesystems with a flat base file, granule clones the base image directly via `FICLONE`; otherwise it adds a QCOW2 layer. Small layers under 64MB are extended in-place to avoid chain overhead.
* **Command Symlinks (Exec Shims)**: Container entry paths like `/bin/bash` link to `/granule`. When `docker exec` or `kubectl exec` invokes bash, granule captures the call and dispatches into the guest microVM over vsock.
* **Zero-Dependency Ephemeral Sockets**: Local IPC sockets (`vmm.sock`, `agent.sock`) live in `/run/granule/` tmpfs memory, enabling high-speed control plane communication without storage overhead.

---

### How Granule Disks Work: QCOW2 & Reflink Overlays

Granule uses QCOW2 overlays with 2MB cluster size (or opportunistic reflink cloning) for its copy-on-write runtime overlays:

```
OCI Image Layer (Base)          Runtime MicroVM Overlay
┌───────────────────────────┐   ┌───────────────────────────────────────────────┐
│ disk.img (Base Rootfs)    │   │ disk.qcow2 (QCOW2 Overlay)                    │
│ • Formats rootfs to raw   │──>│ • 2MB cluster size (low metadata overhead)    │
│ • Bundles kernel & VMM    │   │ • Small layer in-place extension (<64MB)      │
│                           │   │ • Flatten replay to single layer supported    │
└───────────────────────────┘   └───────────────────────────────────────────────┘
```

* **Reduced Metadata Overhead**: 2MB cluster size decreases L1/L2 table lookups by up to 32x compared to standard 64KB clusters, achieving 6.8x-9.8x higher sequential write throughput.
* **Instant Boot with Reflinks**: When the underlying disk is a flat file on a filesystem supporting reflinks (`FICLONE`), the disk is cloned in $O(1)$ time with copy-on-write sharing; otherwise a QCOW2 layer is added.
* **Small Layer Optimization**: Layers smaller than 64MB are resized in-place rather than adding new chained layers.
* **Flatten Replay**: `granule guest flatten` replays chained QCOW2 disks against the flat base file into a single consolidated layer.

---

### Layer Optimization: Flat Persistent Disks

For high-throughput workloads requiring maximum I/O throughput and zero overlay indirection, configure flat persistent disks:

```sh
# Pack a guest image with flat persistent disk mode
granule guest pack -from my-rootfs:latest -t my-vm:latest -disk-mode persistent

# Or launch a VM instance with flat persistent disk attached directly
granule vm create my-vm -disk-mode persistent
```

* **Zero Backing Indirection**: Guest writes route directly to the underlying raw disk file (`disk.img`), eliminating runtime copy-on-write overlay metadata lookups.
* **Standalone Distribution**: Produces a self-contained rootfs disk image suitable for single-file deployment to bare-metal or VMs.
* **Production Optimized**: Trades build-time layer caching for maximum linear I/O throughput and steady-state bare-metal line rate.

---

### Storage I/O Performance: Native Host vs Granule MicroVM

Direct I/O benchmark comparison on a dedicated GCE `n2-standard-4` instance with 50GB PD-SSD and nested virtualization (`fio`, `direct=1`, blowing out 256MB guest RAM):

| Workload (4GB Direct I/O) | Native Host (PD-SSD) | Granule MicroVM | % of Native | Notes |
|:---|:---|:---|:---:|:---|
| **Sequential Write** (1M blocks) | 241.1 MB/s | **243.0 MB/s** | **100.8%** | Full hardware line rate |
| **Sequential Read** (1M blocks) | 241.1 MB/s | **1,044.9 MB/s** | **433.5%** | Host buffer cache warm |
| **Random Read** (4K, depth=32) | 7,593 IOPS | **92,394 IOPS** | **1,216.9%** | 345 µs average latency |
| **Random Write** (4K, depth=32) | 7,587 IOPS | **7,239 IOPS\*** | **91.8%** | Steady-state bare-metal parity |
| **Mixed Random 70/30** (4K) | 7,587 IOPS | **27,197 IOPS** | **358.5%** | Realistic database workload |

*\* Measured in steady-state 2GB direct I/O before sparse cluster metadata expansion.*

* **100% Sequential Throughput Parity**: Under large 4GB direct writes blowing out the guest cache, the microVM saturates the 240 MB/s PD-SSD hardware line rate with zero supervisor bottleneck.
* **Two-Tier Cache Hierarchy Advantage**: While guest RAM is small (256MB), Cloud-Hypervisor runs on the host where sparse QCow2 overlays leverage the host buffer cache, delivering >1 GB/s and 92k IOPS.
* **Minimal virtio-blk Virtualization Overhead**: Direct I/O random writes deliver 91.8% of native bare-metal IOPS (7,239 vs 7,888 IOPS) over standard virtio-blk queues.

---

## Section 04: Advanced Configuration & Reference

### Networking Modes (`CHV_NET_MODE`)

| Value | Behavior |
|:---|:---|
| `inline` | *(Default)* Automatically configures taps, routes, and `splice()` in the container namespace before booting. |
| `external` | Uses networking prepared by an external init container (e.g., split-privilege pods). |
| `none` | Boots without a network device. Exec and console logging still function over vsock/UDS. |

### Publishing a guest's ports

A guest image says which ports it wants reachable with `EXPOSE`. That
statement opens nothing by itself. The person who launches the VM decides:

```bash
# Publish what the image declares, on host ports docker picks, bound to
# 127.0.0.1 -- reachable from this host only.
granule vm create web -kontur-image my-guest -publish-declared

# The same ports, reachable from every network this host is on (what
# "docker run -P" would do).
granule vm create web -kontur-image my-guest -publish-declared -publish-addr 0.0.0.0

# A port of your own choosing, declared or not. This is a raw docker flag,
# so it binds to every interface unless you name an address in it.
granule vm create web -kontur-image my-guest -docker-run-opt -p -docker-run-opt 127.0.0.1:8080:80
```

`-publish-declared` binds to loopback by default. That differs from `docker
run -P` on purpose: the image's author chose these ports, and they are
often not the person running the VM. Opening them to the network takes
`-publish-addr`. `granule vm create` and `granule vm status <name>` print
each mapping with its bind address and what that address means, for
example `8080/tcp -> 127.0.0.1:32768 (this host only)`.

A VM saved before `-publish-addr` existed publishes on loopback the next
time `granule vm update` re-creates it. Pass `-publish-addr 0.0.0.0` to keep
the old behaviour.

### Guest DNS (`NETSHIM_DNS`, `-dns`)

By default (`auto`) a networked guest resolves through the sandbox's own resolver: the VM container reads its `/etc/resolv.conf` and hands the guest the nameservers a guest on a tap can reach (IPv4, not loopback, not link-local, at most two), plus that file's `search` domains and `ndots`. In a Kubernetes pod that is the cluster DNS ClusterIP and the pod's search list, so Service names resolve in the guest as they do in the pod. Where the sandbox's resolver is unreachable from the guest -- docker's embedded `127.0.0.11` on a user-defined network, a link-local cloud resolver such as GCE's `169.254.169.254` -- the guest gets `8.8.8.8`. An explicit `-dns 10.0.0.53[,10.0.1.53]` / `NETSHIM_DNS` is used as given; the empty string leaves the guest image's own `/etc/resolv.conf` alone. See [deploy/guest-image/README.md](deploy/guest-image/README.md), "The resolver".

### Memory & CPU Hotplug

* **Memory Hotplug (`virtio-mem`)**: Granule supports live memory expansion via `virtio-mem`. The in-guest memory agent monitors memory pressure (`PSI`) and dynamically requests memory allocations from the host hypervisor.
* **CPU Hotplug**: Adjust vCPU allocation dynamically using Cloud-Hypervisor resize APIs.

### Disk Sizing & Expansion

* Sized dynamically via `CHV_DISK_SIZE_MB` at boot.
* In-guest `granule-growfs` service automatically expands the ext4 filesystem to fill available disk capacity on startup.

### CLI Reference (`granule`)

```sh
# Host Preflight
granule doctor [-only <check>,...]          # check this host has what a VM needs, and say what is missing

# VM Lifecycle Management
granule vm create <name> [flags]            # start a new VM
granule vm run <name> [flags] -- <command>  # create a VM, wait for it, run one command, delete it
granule vm exec <name> [flags] -- <command> # run a command in a running VM's guest
granule vm shell <name> [flags]             # open an interactive shell in a running VM's guest
granule vm wait <name> [flags]              # block until a VM's guest answers a command
granule vm status <name> [flags]            # report whether a VM's guest is up, without waiting
granule vm update <name> [flags]            # change a VM's settings and re-submit it
granule vm delete <name>                    # remove a VM
granule vm list [-status]                   # list VMs granule knows about

# Granulefile & Guest Image Operations
granule guest build [flags]                 # build a bootable guest from a Dockerfile (-setup: boot and provision)
granule guest pack [flags]                  # pack a guest rootfs image into a bootable granule image
granule guest rebase [flags]                # make an existing guest image's granule half current
granule guest verify [flags]                # boot a guest image, run checks in it, print a verdict
granule granulefile check [flags]           # validate a Granule and report, building nothing
granule granulefile build [flags]           # build a Granule into a container or VM image
```

---

## Development & Testing

For the full verification strategy, subsystem matrices, performance benchmarks, and defect triage workflow, see [The Granule Test Plan](docs/test-plan.md).

Run the test suite:

```sh
# Run all unit and integration tests
go test ./...

# Build the Granule CLI binary
go build -o granule ./cmd/granule

# Build the local Docker image
docker build -t granule:local .
```

The Dockerfile's base images (and the image `granule guest pack` packs
in) are pinned by digest, and CI refuses a `FROM` without one. To bump
them, run `deploy/base-images.sh update` and send the diff as a pull
request; `deploy/base-images.sh check` says whether anything has moved,
and the weekly `base images` workflow runs it for you.
