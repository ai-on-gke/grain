# The granule demo

> **Note**: this project was renamed from **kontur** to **granule**. The
> CLI is `granule` (formerly `konturctl`), the in-container runtime is
> `granule-shim` (formerly `kontur`), and the repository is `gke-labs/granule`. A
> few flag names have not moved yet -- `-kontur-image`, which appears
> throughout this demo, is the old spelling of "the granule image
> supplying the VMM half" and is what the CLI still accepts.

[`demo.sh`](demo.sh) is a presenter's script: it walks the operations
granule claims, one line at a time, printing each command before it runs
it and printing what came back with the exit status and how long it took.
Nothing is hidden behind a helper -- **every line shown is the line
executed**, so an attendee can copy any of them out of the recording and
have it work. A demo whose script does something other than what it shows
is a demo that teaches the wrong thing.

```sh
demo/demo.sh --dry-run          # rehearse: print every line, run none
demo/demo.sh                    # the whole thing, a keypress per step
demo/demo.sh --auto --delay 3   # unattended, for a recording
demo/demo.sh build rebase       # just those two chapters
demo/demo.sh cleanup            # take away what it made
```

## What it demonstrates, in order

| Chapter | The claim it makes |
| --- | --- |
| `provision` | What the rest needs, built in front of the audience: this tree's CLI, the whole image, and the VMM half on its own. |
| `publish` | granule's images live in GHCR, and `deploy/publish-images.sh` is what puts them there -- the bootable image and the guest rootfs beside it. |
| `local` | One VM from that published image on a laptop's docker: create, exec *into the guest*, read the guest's console out of `docker logs`, copy a file in. |
| `gke` | The same image, the same exec, the same logs, on a GKE cluster -- with no controller, no CRD and no runtime class. One pod manifest. |
| `pod` | Several VMs in one pod, one per container -- and then a fourth VM injected into that pod *while it runs*, as an ephemeral container. |
| `build` | Two ways to make a guest of your own: a Dockerfile that describes a machine, and the pack of an ordinary OCI image underneath it. Plus `guest verify`. |
| `rebase` | A guest somebody else built, moved onto today's granule in seconds without rebuilding it and without opening its disk. |
| `cleanup` | Not run by default. Removes the VMs, containers, images, pods and the state directory. Leaves the GKE cluster alone, deliberately. |

With no chapter named, everything but `cleanup` runs in that order.

## The three modes

- **Interactive** (the default). Each step prints its command and waits.
  `enter` runs it, `s` skips it, `q` ends the demo. This is the mode to
  present in: the pause is where you talk.
- **`--auto`**, with `--delay N` between steps. For an unattended
  recording, or for running the whole thing to see that it still works.
  A non-terminal stdin implies it.
- **`--dry-run`**. Prints every line of every chapter and runs none of
  them. It needs no docker, no KVM, no registry credential and no
  cluster, and it makes nothing -- which is why CI runs it (the "demo
  script dry run" step in `.grain/workflows/ci.yml`). Rehearse in this
  mode: it is how you check that a chapter still says what you think
  before you say it to a room.

A failed step stops the demo, because carrying on from a VM that never
booted produces ten more failures that say nothing. `--keep-going`
overrides that. A few steps are *expected* to fail -- a refused
Dockerfile instruction, a rebase onto an image that is not a granule
image -- and those are marked in the script and reported as
`✔ exit N -- which is the point of this step`.

## What each chapter needs

Everything is skipped rather than failed when its prerequisite is
missing, saying which one. In `--dry-run` nothing is skipped: a rehearsal
shows every line whatever the machine has.

| Chapter | Needs |
| --- | --- |
| `provision` | `docker`, `go`, and a real `/dev/kvm` on this host (nested virtualization, if the host is itself a VM). |
| `publish` | `GHCR_USER` and `GHCR_TOKEN` -- a GitHub username and a token with `write:packages`. |
| `local` | `docker` and `/dev/kvm`. |
| `gke` | `gcloud`, `kubectl`, and `GKE_PROJECT` set to a project you may create a cluster in. "May create a cluster in" is two grants, not one: `roles/container.admin` *and* `roles/iam.serviceAccountUser` on the node pool's service account (the project's `<number>-compute@developer.gserviceaccount.com`, unless you pass `--service-account`). Without the second, `clusters create` gets eleven seconds in and stops -- see the note below. |
| `pod` | `kubectl` against a cluster with KVM-capable nodes, and a `kubectl` new enough for `kubectl debug --custom` (**1.31+** -- 1.30 has `--profile` but not `--custom`). On an older client the first three VMs still run and the chapter says why the fourth does not. |
| `build`, `rebase` | `docker` and `/dev/kvm`. |

Configuration is by environment variable. `provision`'s first step
prints the lot, so a recording says what it ran with.

| Variable | Default | |
| --- | --- | --- |
| `REGISTRY`, `REPO`, `IMAGE_TAG` | `ghcr.io`, `gke-labs/granule`, `latest` | where the publish pushes |
| `IMAGE`, `ROOTFS_IMAGE` | derived from those | what the rest of the demo consumes |
| `LOCAL_IMAGE`, `RUNTIME_IMAGE` | `granule:latest`, `granule:runtime` | built from this tree: the whole image, and the VMM half alone |
| `VMM_IMAGE` | `$LOCAL_IMAGE` | what `-kontur-image` names on every pack, rebase and verify (see the note below) |
| `GKE_PROJECT` | *unset* | no default on purpose: a demo that guesses which project to create a cluster in is a demo that bills somebody |
| `GKE_CLUSTER`, `GKE_ZONE`, `GKE_MACHINE`, `GKE_NODES` | `granule-demo`, `us-central1-a`, `n2-standard-4`, `1` | |
| `GHCR_USER`, `GHCR_TOKEN` | `$GITHUB_USER`, `$GITHUB_TOKEN` | |
| `DEMO_STATE` | `<repo>/.demo` | everything the demo makes locally, in one directory |

`DEMO_STATE` is also where the demo points `XDG_STATE_HOME`, so the VMs
it creates are kept out of `~/.local/state` and `/var/lib/granule` and
cannot disturb VMs the operator of that machine created for real. It is
why no `granule vm` line below carries a `-state-dir` flag.

## Before you present

The slow parts are the two `docker build`s in `provision` (the full image
debootstraps a guest rootfs: tens of minutes on a cold cache) and the GKE
cluster creation. That last one is the cheaper of the two surprises: one
zonal `n2-standard-4` took **5m42s** end to end, measured once in
`us-central1-a`, rather than the fifteen minutes this file used to
estimate. Treat six minutes as the number to plan around and fifteen as
the one to quote if you are promising somebody a margin. Run both before
the audience arrives:

```sh
demo/demo.sh --auto provision          # warms the docker layer cache
GKE_PROJECT=... demo/demo.sh --auto gke   # creates the cluster, then tears its pod down
```

Both are idempotent -- the cluster step creates only if `describe` says
there is none -- so running them again during the demo costs seconds and
the audience still sees the line.

---

## The narrative

What follows is what each chapter is actually showing, for a presenter
reading it beforehand and for a reader who was not in the room.

### 1. Publishing to GHCR, and booting what was published

granule ships as **one OCI image** carrying both halves: the runtime and
cloud-hypervisor (the *VMM half*), and a packed guest root disk with the
kernel it needs (the *guest half*). That is why `docker run` on it boots
a VM rather than starting a process -- there is no operator, no CRD and
no runtime class between the image and a running machine.

Nothing in CI publishes those images. `deploy/publish-images.sh` is run
by a person with a credential, and the demo runs it in front of the
audience because that is the honest version of the operation. It pushes two images per guest variant:

- `ghcr.io/gke-labs/granule/<variant>:latest` -- the bootable one;
- `ghcr.io/gke-labs/granule/<variant>.rootfs:latest` -- the rootfs it was
  packed from, which is what a consumer writes `FROM` to customize a
  guest (chapter 6);

each also under an immutable tag nobody moves -- `<commit>` for
the bootable image, which carries granule's own guest-side half and so
changes whenever granule does, and `os-<key>` for the rootfs,
which is an operating system and is named after the recipe that built it
(`deploy/guest-rootfs-key.sh`). A publish whose rootfs recipe has not
changed leaves the published rootfs alone rather than pushing a fresh
build over a tag somebody has pinned.

> A package GHCR creates on its first push is **private**, and a private
> guest rootfs is one nobody can build `FROM`. Making it public is a
> one-off in that package's GHCR settings. Until it is done, the pull
> steps in this chapter answer `denied` -- which is the same answer a
> package that does not exist gives, and is worth saying out loud during
> the demo rather than debugging live.

Then the local chapter boots it, and makes the one distinction the whole
project rests on:

```sh
docker exec granule-vm-demo-web cat /proc/version    # fails: no cat in there
granule vm exec demo-web      -- cat /proc/version   # the guest's kernel
docker exec granule-vm-demo-web sh -c 'cat /proc/version'   # also the guest's
```

The first line is *supposed* to fail, and the demo marks it so. The VM
container is a `scratch` image holding cloud-hypervisor and one Go
binary: no shell, no coreutils, none of the workload, so `docker exec`'s
usual landing place is not there to land on. The third line works anyway
because `/bin/sh` and `/bin/bash` in that container are symlinks to that
same binary, which recognizes being called as a shell and forwards into
the guest -- so tooling that knows nothing about granule gets the guest
rather than an error.

> Two different binaries answer to `granule` in this demo, and it is
> worth knowing which is which. On your machine it is the CLI built in
> `provision` (`granule vm ...`, `granule guest ...`). *Inside* a VM
> container it is `granule-shim` under a symlink, which is what
> `kubectl exec ... -- granule exec` and the manifests' `granule ready`
> readiness probe invoke. The CLI's `vm exec` is that same session with
> the container name and the inner invocation folded away.

`granule vm exec` lands **inside the guest**, over the VM's own
virtio-vsock device. That
matters more than it looks: vsock is carried by the virtio device rather
than by IP, so exec works on a guest whose network has not come up, has
no address, or has no NIC at all. Everything else in this demo that
reaches into a VM -- on docker, on GKE, into an ephemeral container --
is that same session.

`docker logs granule-vm-demo-web` is the guest's serial console, so a
kernel booting is what `kubectl logs` shows too, with no extra plumbing.

### 2. The same thing on GKE

The GKE chapter deliberately does nothing granule-specific to the
cluster. GKE has to supply exactly one thing -- a real `/dev/kvm` in the
pod -- and `--enable-nested-virtualization` is a first-class node pool
flag, not a custom node image. Everything else the VM needs is inside the
image already.

The manifest is the repository's own
[`deploy/k8s/gke-pod-exec-example.yaml`](../deploy/k8s/gke-pod-exec-example.yaml)
with the published image substituted in. Two things in it are worth
pointing at while it applies:

- **`CHV_NET_MODE: inline`.** The container sets up the pod's network and
  hands it to the guest, which takes over the pod's IP, MAC, MTU and
  routes. The pod is then addressed exactly as any other pod is --
  Services and all -- and what answers is a VM.
- **The readiness probe is `granule ready`.** A VM container is "up" the
  moment cloud-hypervisor is exec'd, which is a long way before anything
  can run inside the guest. Without the probe,
  `kubectl wait --for=condition=Ready` returns on a VM you cannot yet
  reach. With it, Ready means the guest answered.

### 3. Several VMs in one pod, and one injected into a running one

A granule image is one container that boots one VM, so "several VMs in a
pod" needs no new machinery: it is several containers.
[`assets/pod-multi-vm.yaml`](assets/pod-multi-vm.yaml) runs three, and
the demo shows three distinct `/proc/sys/kernel/random/boot_id` values to
make the point that they are three machines rather than three views of
one.

What it does need an answer for is the one thing a pod has exactly one
of: its IP address.

- **`web`** runs `CHV_NET_MODE=inline` and its guest takes the pod's
  address over. Exactly one container in a pod may do this.
- **`builder`** and **`sidecar`** run `CHV_NET_MODE=none`: no network is
  set up and none is derived, and their guests boot with no NIC.

They are not unreachable for it -- `kubectl exec ... -c builder --
granule exec` still gets a shell in that guest, because exec is over
vsock. That is the property that makes a pod full of VMs practical
rather than a networking argument. (A guest that wants IP without holding
the pod's can be reached on the control link instead; see the main
README's "Container networking".)

Then the part people usually have not seen: **a whole VM injected into
that pod while it runs**.

```sh
kubectl debug granule-multi-vm \
  --image=ghcr.io/gke-labs/granule:latest -c vm-injected \
  --profile=sysadmin --custom demo/assets/ephemeral-vm.yaml
```

An ephemeral container is Kubernetes' own way of adding a container to a
live pod -- normally a debugging shell in a pod whose image has none.
Here the image is a granule image, so what arrives is another
cloud-hypervisor VM with its own kernel, root filesystem and memory,
beside the three that were already there. **Nothing already in the pod
restarts**, which is the whole attraction.

Three properties of ephemeral containers shape
[`assets/ephemeral-vm.yaml`](assets/ephemeral-vm.yaml), and each is worth
saying while it applies:

- **No `resources`, `ports`, `lifecycle` or probes.** The API refuses
  them: an ephemeral container is not scheduled, it is squeezed into a
  pod whose resources were decided when it was admitted. So the injected
  VM has no readiness probe and the demo waits for its guest by hand,
  with `granule ready` -- and its memory is sized to fit in what the
  pod's own containers left unclaimed on the node.
- **It cannot be removed or restarted.** Deleting the pod is how the
  injected VM goes away.
- **It still needs `/dev/kvm`.** `--profile=sysadmin` is what makes it
  privileged; the explicit `volumeMounts` entry in the custom spec is the
  pod's own `dev-kvm` volume, declared so the dependency is visible
  rather than inherited from privilege.

> The floor is **kubectl 1.31**, not 1.30: `--profile=sysadmin` arrived
> in 1.30 but `--custom` in 1.31, and 1.30 answers `unknown flag:
> --custom`. The two flags do compose -- kubectl lays the custom file
> over the profile's output, so the container is admitted privileged
> *and* carries the args, env and mount from the file. On an older
> client, put `securityContext: {privileged: true}` into
> `assets/ephemeral-vm.yaml`, drop `--profile`, and hand the result to
> `kubectl replace --raw
> /api/v1/namespaces/NS/pods/granule-multi-vm/ephemeralcontainers` with
> the full pod object. The chapter checks for `--custom` and skips just
> the injection if it is missing, rather than dying at that line.

> **Two waits, not one.** `kubectl debug` returns as soon as the API
> accepts the container, and an injected image tagged `:latest` gets
> `imagePullPolicy: Always` from kubectl whatever the node already has
> -- so `kubectl exec` straight afterwards fails with `unable to upgrade
> connection: container not found`. The chapter waits for
> `.status.ephemeralContainerStatuses[?(@.name=="vm-injected")].state.running.startedAt`
> first (there is no Ready condition to wait on: an ephemeral container
> may not have a probe), and only then waits for the guest with `granule
> ready`.

### 4. Building a guest: a Dockerfile, and the pack under it

`granule guest build` takes an ordinary Dockerfile with a smaller
vocabulary and produces **a bootable granule image**.
[`assets/Dockerfile.guest`](assets/Dockerfile.guest) installs nginx,
drops a site config in and `systemctl enable`s it -- and what boots is a
machine already serving.

Two things about the route are the demo's actual point:

- **It is two steps in one command.** A docker build produces a *rootfs*
  image; the *packing* turns that filesystem into a bootable disk and
  lays granule's own guest half -- the agents, the control-net overlay,
  the units that start them -- over whatever the base carried. So the
  rootfs keeps a name of its own (`-rootfs-tag`), because the rootfs, not
  the packed disk, is what a consumer publishes for somebody else to
  pack. Whoever packs it gets *that day's* agents rather than this
  build's, which is what makes chapter 5 possible.
- **What it refuses, it refuses by name.** A remote `ADD` is rejected
  naming the file, the line and the instruction, rather than dropped --
  a dropped instruction is a machine that is quietly not the one the file
  described. The demo shows that failing on purpose.

The harder limit is not the instruction list, though: the build runs
under the *build host's* kernel with no init, so nothing in a Dockerfile
can start a service, run a container or open `/dev/kvm`. Enabling a unit
is a build-time operation and works; starting one is not. `guest build
-setup <script>` is the other route for exactly that case -- it boots the
guest and provisions it from inside -- and it costs a KVM host and a boot
per build, which is why the route that works everywhere is the default.

`granule guest pack` is the same packing on its own, against an OCI image
somebody else authored. It **executes nothing** out of that image and
needs no KVM, which is what lets it run on the machine holding the daemon
rather than in that consumer's CI.

> **Which image `-kontur-image` should name.** The one that brings the
> kernel. A `docker build --target runtime` build is much smaller -- a
> full granule image's bundled guest disk stays in the packed result's
> lower layers, and `guest pack` reports it (1.25 GB against 649 MB,
> measured on this demo's own guest) -- but it carries no `vmlinux`
> either, deliberately: it is the VMM half for a guest that brings its
> own kernel. This demo's guest is the reference one and brings none, so
> it packs onto the full image and pays for the bundled disk. Packing it
> onto the lean one is refused at build time, rather than producing an
> image that builds and pushes and then stops at `granule: CHV_KERNEL
> (/var/lib/granule/guest/vmlinux): no such file or directory` on
> whatever node runs it. A guest built from a rootfs that carries a
> kernel (the `debian12` variant, or any `GUEST_KERNEL_PACKAGE` build)
> can set `VMM_IMAGE` to the lean one.

The chapter ends with `granule guest verify`, which boots the image, runs
[`assets/checks.txt`](assets/checks.txt) inside it as root and prints a
verdict -- no commit, no tag, no image produced. `-kontur-image` boots it
under the VMM half a deployment will actually run it on, which is the
machine worth judging rather than the one the consumer happened to build.

### 5. Rebase: somebody else's guest, on today's runtime

This is the chapter that explains why the two halves are separable at
all.

A consumer built a guest six weeks ago against whatever granule they had.
A cloud-hypervisor CVE lands. The operator upgrades their fleet's VMM
half -- and that guest has to move with it, without anybody rebuilding
somebody else's machine.

**The cheap form is the default and the one to reach for.** It is a
`docker build` whose `FROM` is the granule image, with the guest half
copied across: seconds, no disk opened, nothing out of the guest
executed. `-print-dockerfile` shows exactly that build before it runs.
Note the direction -- `FROM` the *granule* image, not the reverse: an
entrypoint and an environment are as much the VMM half as the binaries
are. The one thing of the guest's own configuration that crosses is its
`EXPOSE`, because that is a statement about the machine inside rather
than about the half being replaced.

What the cheap form cannot refresh is the guest *side*: the agents and
the overlay inside the disk stay as they were packed. Two complete forms
exist for when that matters, and which one is possible depends on what
you still have:

- **`-rootfs`**, where the rootfs image still exists: repack it with
  today's agents. Minutes and a new disk, still running nothing out of
  the guest. (It is the same operation as a Dockerfile whose only
  instruction is a `FROM` of that rootfs -- two names for one path, kept
  because they are reached by different people.)
- **`-boot`**, where nobody has the rootfs any more: build the cheap
  rebase, boot *that* -- so the half doing the booting is already the
  operator's -- and lay the current agents down from inside. Needs
  `/dev/kvm`, and is the only form that runs the guest's own code.

Underneath all three is a **declared contract version**: an integer the
guest states for the granule-side pieces it carries, written in three
places that fail in different directions -- a label on the packed image
(which is what makes a cheap rebase affordable: one `docker image
inspect`, no disk opened), a file inside the rootfs, and a sidecar beside
the packed disk. A guest below granule's floor is *refused* rather than
warned about, and the boot-time check reads the sidecar before
cloud-hypervisor is started, because a VM that boots and is then killed
has already been handed to a caller as running. An image that declares
nothing is *reported* instead -- that is every image published before the
declaration existed, and a backstop whose first act is to break every
existing guest is not one anybody turns on. `-force` goes ahead anyway
for the caller who knows something the label does not.

See [`docs/guest-images.md`](../docs/guest-images.md) for the whole
design, and `deploy/guest-image/README.md` for what a guest rootfs is
allowed to contain.

## Files

| | |
| --- | --- |
| [`demo.sh`](demo.sh) | the runner and all seven chapters |
| [`assets/Dockerfile.guest`](assets/Dockerfile.guest) | the guest the `build` chapter builds |
| [`assets/nginx-demo.conf`](assets/nginx-demo.conf) | the site that guest serves |
| [`assets/checks.txt`](assets/checks.txt) | what `guest verify` asserts about it |
| [`assets/pod-multi-vm.yaml`](assets/pod-multi-vm.yaml) | three VMs in one pod, one per container |
| [`assets/ephemeral-vm.yaml`](assets/ephemeral-vm.yaml) | the partial spec `kubectl debug --custom` injects a fourth with |

## Known limits of this script

- **What has actually been run, and what has not.** Every chapter has now
  been run for real -- none of them is rehearsal-only -- and the two
  cluster chapters have been run on GKE itself. One chapter is still run
  against a stand-in for the service it names, so read this before
  claiming the whole demo works as written:
  - `provision`, `local`, `build` and `rebase`: **end to end on a Debian
    host** with docker and a working `/dev/kvm`. Every step passes,
    including booting the guest the Dockerfile describes and getting an
    HTTP response out of it, and a five-of-five `guest verify` verdict.
  - `publish`: **every step, against a registry that is not GHCR** --
    `REGISTRY=localhost:5000` and a `registry:2` container. The build,
    both `<variant>-<commit>` tags, all four pushes, the two pulls back
    and the contract-label inspect all pass. What that does *not* cover
    is GHCR's own authentication and its habit of making a first-push
    package private; those two are still untried.
  - `gke`: **every step on GKE**, including the two `gcloud container
    clusters` lines this file used to say had only been rehearsed. The
    chapter's own `clusters create` built a single-node GKE Standard
    cluster -- one `n2-standard-4`, `--image-type COS_CONTAINERD
    --enable-nested-virtualization`, control plane 1.35.7-gke.1222000,
    node image Container-Optimized OS 6.12.94+, containerd 2.1.9 -- and
    `get-credentials` then pointed `kubectl` at it. The flag does what
    the chapter says it does: `kvm-check` finds
    `crw-rw-rw- 1 root 409 10, 232 /dev/kvm` on the node, world readable,
    on a stock COS node image with no custom image and no licence
    juggling. `deploy/k8s/gke-pod-exec-example.yaml` applies, the pod
    goes Ready in 14 seconds (its readiness probe is `granule ready`, so
    that means the guest answered), `kubectl exec ... -- granule exec`
    reaches the guest, the guest holds the pod IP (10.52.0.14 on both
    sides), and `kubectl logs` is its serial console. **The create took
    5m42s, not fifteen minutes** -- see "Before you present" above.

    Getting that far needs the second grant named in the table at the
    top, and it is worth knowing what its absence looks like, because it
    is neither the cluster nor the flags that are refused:

    ```
    ERROR: (gcloud.container.clusters.create) ResponseError: code=400,
    message=The user does not have access to service account
    "<number>-compute@developer.gserviceaccount.com". Ask a project owner
    to grant you the iam.serviceAccountUser role on the service account.
    ```

    That costs eleven seconds to discover, so check it before the
    audience arrives. A credential that cannot act as *any* service
    account cannot create the cluster at all -- passing
    `--service-account` pointing at the credential's own service account
    fails the same way, because acting as yourself is still acting as
    somebody.
  - `pod`: **end to end on that same GKE cluster**, including the fourth
    VM injected into the running pod. Three guests boot with distinct
    boot ids, only `web` holds the pod's address, `kubectl debug
    --profile=sysadmin --custom` is accepted and admitted privileged, and
    the injected VM answers `granule exec` with a fourth boot id while
    the other three keep running. The injected image is pulled for real
    -- kubectl gives an injected `:latest` image
    `imagePullPolicy: Always`, and the kubelet logs the pull of all
    143 MB from Artifact Registry.

    The manifest fits one `n2-standard-4`, but it fits it almost exactly,
    which is worth knowing before you size a node for it. GKE's own
    system daemonsets already request 753m of the node's 3920m
    allocatable CPU; the three containers ask for 1 CPU each on top of
    that, which takes the node to **3753m, or 95% of allocatable, with
    167m to spare**. It schedules, and it would not schedule twice: a
    second copy of this pod, or a cluster carrying heavier system
    add-ons, needs a bigger machine type. Memory is nowhere near as
    tight -- 4.3 GB of requests against 13.0 GB allocatable. The
    injected fourth VM is free of that arithmetic entirely, because an
    ephemeral container may not declare resources at all: it adds
    nothing to the node's requests, and its `CHV_MEMORY_MB: 512` comes
    out of real free memory, of which there was 14 GB (the three guests
    that were already running had a working set of 1.3 GB between them,
    since a guest takes its memory lazily rather than up front).

    Earlier runs on a single-node `kind` cluster are what turned up the
    quoting bug in the uptime line, the exec-before-the-container-exists
    race, and the kubectl version floor being 1.31 rather than 1.30; the
    chapter has been run on three clients, and 1.36 and 1.31 inject the
    fourth VM where 1.30 runs the first ten steps and says in one line
    why the fourth is not coming.

  What is still untried on GKE is Autopilot, which refuses privileged
  pods outright and is out of scope for this manifest.
- **The `pod` chapter's `kubectl debug --custom` line was the part to
  rehearse first, and it works.** The custom file and `--profile=sysadmin`
  compose; a privileged ephemeral container is admitted on a cluster that
  does not enforce a restrictive Pod Security policy. GKE Standard is one
  of those, and measurably so rather than by reputation: its `default`
  namespace carries no `pod-security.kubernetes.io/*` label at all, which
  leaves Pod Security at `privileged`, and both the privileged pod and the
  privileged ephemeral container were admitted there without complaint.
  (Autopilot refuses privileged pods outright and is out of scope for this
  manifest.) The client floor is 1.31 -- see the chapter's own note above
  for what to do on an older one. Where Pod Security *is* enforced, the
  whole chapter stops at its first line rather than at the injection, and
  says why -- this is GKE's own wording, in a namespace labelled
  `enforce=baseline`, and a `kind` cluster refuses it in the same words:

  ```
  pods "granule-multi-vm" is forbidden: violates PodSecurity
  "baseline:latest": hostPath volumes (volume "dev-kvm"), privileged
  (containers "web", "builder", "sidecar" must not set
  securityContext.privileged=true)
  ```

  Run the chapter in a namespace labelled
  `pod-security.kubernetes.io/enforce=privileged`, or in one with no
  label at all.
- **The GHCR packages are not public yet**, so the demo does not assume
  them: run the `gke` and `pod` chapters with `IMAGE=` pointing at a
  registry the cluster can pull from -- an Artifact Registry repository
  in the same project is the obvious one on GKE, and is what the GKE runs
  above used: a `granule` repository in `us-central1`, `IMAGE` naming
  `<region>-docker.pkg.dev/<project>/granule/granule:latest`. Two things
  that needs which are easy to miss -- the node pool's service account
  must be able to read the repository (`roles/artifactregistry.reader` on
  it, if the default grant has been taken away), and the repository should
  be in the same region as the cluster. Naming an image already on the
  node works too, and is worth knowing about, with one catch: give that
  one a tag that is not `:latest`. kubectl gives an injected `:latest`
  image `imagePullPolicy: Always`, so the fourth VM alone would go
  `ErrImagePull` against a registry that has never heard of it while the
  first three ran happily. Against a real registry that is a feature
  rather than a trap: the injected VM's pull is the one part of the demo
  that proves the image is genuinely fetchable, and it took 209ms here. Until somebody publishes
  `ghcr.io/gke-labs/granule` and flips the
  package to public, the defaults in the table above name an image no
  cluster can pull, and the `publish` chapter's two pull steps fail on a
  first publish until the package is made public by hand.
- **`publish` refuses a dirty tree**, because `publish-images.sh` will not
  put a `<variant>-<commit>` tag on an image built from uncommitted
  changes. Its `--dry-run` only warns, so without a check the chapter
  would log in, show a dry run that looks fine and then fail; the chapter
  therefore skips itself up front if `git status --porcelain` says
  anything. Commit before you present, or run the script by hand with
  `--allow-dirty`.
- **`cleanup` leaves the GKE cluster alone**, on purpose: it costs money
  and takes minutes to make. The chapter prints the `gcloud
  container clusters delete` line rather than running it. Do run that
  line: an idle single-node `n2-standard-4` cluster is about $0.29 an
  hour of node, boot disk and cluster management fee, which is nothing
  for the length of a demo and real money for the length of a weekend.

## Ephemeral MicroVM Demo & Presentation Benchmarks

The `demo/assets/` directory includes a standalone demonstration and recorded video assets showing multiple hardware-isolated Cloud-Hypervisor microVMs provisioned concurrently in a single Kubernetes Pod, as well as dynamic ephemeral microVM container injection via `kubectl debug --custom`:

- `demo/assets/ephemeral-vms-demo.mp4`: High-resolution (1080p) terminal recording video of the multi-VM pod and ephemeral container lifecycle.
- `demo/assets/ephemeral-vms-demo.svg`: Animated vector SVG recording playable in browsers.
- `demo/assets/ephemeral-vms-demo.cast`: Raw asciicast recording reproducible via `asciinema play`.
- `benchmarks/BENCHMARK_REPORT.md` & `benchmarks/benchmark_results.json`: Full benchmark results and startup timing comparison.

### Key Performance Numbers (Presentation Summary)

| Metric | Cold Boot (granule:local) | Snapshot Resume (granule:snapshot) | Speedup |
|---|:---:|:---:|:---:|
| **Container Admission & Start** | 0.67s | 0.78s | - |
| **Guest VM Readiness (`granule ready`)** | 37.48s | 3.38s | **11.1x** |
| **Total Startup (`kubectl debug` to Ready)** | 38.15s | 4.16s | **9.17x** |
| **End-to-End Execution (`hello world`)** | 38.54s | 4.49s | **8.57x** |

### Running & Re-recording the Demo

```sh
# Run the standalone ephemeral multi-VM demo
bash demo/run_ephemeral_demo.sh

# Run the automated benchmark suite
python3 benchmarks/benchmark_ephemeral.py 5

# Re-render MP4 and SVG video assets from asciicast
python3 demo/render_cast_video.py demo/assets/ephemeral-vms-demo.cast demo/assets/ephemeral-vms-demo.mp4

