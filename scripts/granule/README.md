# The granule guest image

`pkg/granule`'s own package doc comment, and `README.md`'s
`grain mcpserver -granule-vm` section, both state the same assumption: a
granule-managed VM's guest image already carries the operator's SSH key and
a running sshd (and, per `README.md`, git) before this repo ever tries
to reach it. Nothing in this repo built that image -- v1 has an equivalent
job for its own libvirt-managed sandboxes (v1's own sandbox provisioning script, run as
cloud-init user-data against a shared base image on every VM's first boot);
v2 had no successor, granule-backed or otherwise. The successor is the
repository's `Granulefile`: a Dockerfile whose ordinary `RUN` steps
install the guest's packages, toolchains, account and units, ending in a
`VM PACK` that packs that rootfs onto a granule image. What comes out of
`granule granulefile build -f Granulefile` is itself a runnable granule
image, carrying a guest cloud-hypervisor direct-kernel-boots with no
bootloader, firmware, or partition table anywhere in the picture. This
directory holds what that build needs besides the Granulefiles:
`build-granule-image.sh` builds the granule image every guest here is
packed onto, out of `third_party/granule`, and `guest-inputs.sh` names
the paths the build reads, which is what the guest cache is keyed on.
`scripts/release-images.sh` is what runs the build and publishes it.

Until b615c833e ("Convert sandbox guest image build pipeline to native
Granulefiles") the guest was built by two scripts in this directory,
`build-guest.sh` and `guest-setup.sh`, and much of what follows records
why it was built that way. Where a section below names either script it
is describing that history; neither exists any more.

bwsalmon/agents#267 first answered "decide, don't just wire" for what the
image needs beyond sshd+key, how it gets built and published, and whether
one image serves every dispatch slot -- see the sections below for that.
bwsalmon/agents#478 (this directory's current shape) revisited *how* the
image and its kernel get built, because #267's own Packer/QEMU pipeline
had never actually been run for real, and the one kernel anyone had tried
booting it with (bwsalmon/granule's own `benchmarks/kernel/build.sh`,
Firecracker's own firecracker-ci build) panics with "Cannot open root
device vda" against any cloud-hypervisor guest, direct-kernel-booted or
not: Firecracker uses virtio-mmio, cloud-hypervisor uses virtio-pci, and
that kernel only has a driver for the former. Building a purpose-built
PVH/virtio-pci kernel from source looked, going in, like the only way
past that. It turned out not to be needed at all -- see "Why no custom
kernel" below.

## Converged on granule's own guest build

This directory used to run its own pipeline -- `build.sh` driving
`provision.sh` through debootstrap and chroot, as root -- which duplicated
work `third_party/granule`'s own Dockerfile already does: it debootstraps a
`--variant=minbase` rootfs, applies its overlays, and packs the result
with `mke2fs -d` -- *"no extra privileges beyond an ordinary docker
build"*. That pipeline is gone, and so is the one that replaced it
(granule's build-time guest setup hook, `GUEST_SETUP_SCRIPT`, fed
`guest-setup.sh`). The rootfs is now the `Granulefile`'s own container
stages, and `VM PACK` packs it with granule's own tooling.

Beyond the one-build-instead-of-two win below, packing with granule
rather than beside it means the guest carries granule's own guest
overlays: `VM PACK` lays the guest-side pieces it copies out of the
granule image (`/usr/local/share/granule/guest-parts/`) over the
rootfs. That is what makes granule's flat networking mode usable here:
its control link is configured guest-side by `granule-control-net`, one of
those overlays, which a rootfs built from scratch in this directory never
had.

What that buys: one build instead of two, no root/debootstrap requirement
for a guest image build, and -- since bwsalmon/granule#35 -- a guest image
with no key in it at all, which retired `-granule-exec-key` and the
key-staging step `scripts/setup.sh` used to do for it.

### Status: both the kernel and the hook have landed upstream

The `third_party/granule` resync (upstream `dd6e306`) bakes a guest kernel
into the granule OCI image -- a new `fetch-kernel` stage pulling
cloud-hypervisor's own published PVH `vmlinux`, plus a `CHV_KERNEL`
default pointing at it. That closes granule's "a kernel comes from
somewhere outside the image" gap **for granule's own bundled guest**.

It does not close it for grain's, and grain should not adopt that kernel:
this guest runs docker and `kind`, which need a far richer kernel config
(overlayfs, cgroup v2, bridge netfilter, veth) than a release kernel built
for cloud-hypervisor's own CI is likely to carry, and "Why no custom
kernel" below records Debian's stock `linux-image-amd64` being verified by
hand under real KVM for exactly this guest. So the `Granulefile` installs
`linux-image-amd64` itself, and `VM PACK` packs the resulting
`vmlinuz`/`initrd.img` with the disk -- onto a granule image built with
`--target runtime`, which carries no kernel of its own
(`build-granule-image.sh`).

The build-time setup hook has now landed too, on granule's `main`
(bwsalmon/granule#28), and is in this repo's `third_party/granule`
snapshot. granule's build args are now `GO_VERSION`,
`CLOUD_HYPERVISOR_VERSION`, `GRANULE_KERNEL_VERSION`, `GUEST_DISTRO`,
`GUEST_SUITE`, `GUEST_ALPINE_VERSION`, `GUEST_SSH_AUTHORIZED_KEY` and
`GUEST_SETUP_SCRIPT` -- the last being the one this directory was waiting
on. It holds the script's own *text*, not a path.

### What the hook does, and the one problem it exists to solve

Customizing granule's guest rootfs is not simply "run a script in it",
because the rootfs is a *directory* in a build stage. Running anything in
it means `chroot`, and a useful chroot needs `/proc` and `/dev`
bind-mounted -- which needs `CAP_SYS_ADMIN`, which an ordinary
`docker build` does not have. Without them, apt postinsts,
`systemctl enable` and `update-initramfs` variously misbehave or fail.
This is precisely why `build.sh` needed root while it existed, and why
keeping that build would have undone the "no extra privileges beyond an
ordinary docker build" property granule's own `guest-image` stage calls out
for `mke2fs -d`.

It sidesteps that rather than paying it: a `guest-customized` stage
promotes the rootfs to an image of its own
(`FROM scratch` + `COPY --from=guest-rootfs /rootfs/ /`), so
`GUEST_SETUP_SCRIPT` runs as an ordinary `RUN` -- real `/proc`, real
`/dev`, working network, rootfs as `/` -- with no chroot and no
privileges. `guest-image` then packs that stage instead of the raw
rootfs. There is also a `guest-artifacts` target, so
`docker build --target guest-artifacts --output type=local,dest=<dir>`
yields `disk.img` plus the guest's own kernel and initramfs when it has
them, without bloating the runtime image with any of it. `final` stays
last, so the default build target is unchanged. See
`third_party/granule/deploy/guest-image/README.md`'s "Running a custom
setup script" and "Exporting the built guest".

### Not yet verified

The hook was written and reviewed, and landed upstream, without ever
being built: no image registry is reachable from where it was written
(the egress policy denies Docker's blob CDN, so `docker build` cannot
resolve a single base image), and there is no `/dev/kvm` to boot the
result under. What needed checking on a machine that can:

1. **Device nodes survive the copy.** `COPY --from=guest-rootfs /rootfs/ /`
   has to carry debootstrap's `/dev` entries through BuildKit. If it
   chokes, empty `/dev` in the rootfs stages first -- both variants'
   kernels mount devtmpfs over it during early boot anyway.
2. **`RUN` works on the scratch-derived stage.** It needs the `/bin/sh`
   the rootfs provides; the patch sets `PATH` explicitly rather than
   relying on the runtime's default for an image config that sets none.
3. **The Alpine variant still builds.** The hook is written to be
   distro-agnostic (busybox `sh` satisfies it), but only the Debian path
   is what grain exercises.
4. **`update-initramfs` inside the RUN** produces an initramfs the guest
   actually boots from -- the step `guest-setup.sh` depended on for its
   `eth0`/`ip=` units to stick.

**Verified live (bwsalmon/agents#577), for the Debian path.** A fresh
n2-standard-4 GCE VM with nested virtualization, running nothing but
`scripts/setup.sh GRAIN_GRANULE_ENABLE=1`, built this image
(`docker build --target guest-artifacts`) end to end with no patching,
`granule vm create` booted it under real `cloud-hypervisor`/KVM, and a
real `grain daemon` dispatch reached it through the full production path
(`pkg/mcp/docker_exec_runner.go`'s `docker exec <container> granule exec --
whoami`, over the guest's own sshd) and got back `debian`. All four items
above check out: the device nodes, the `RUN` stage, and the regenerated
initramfs all worked without local changes. Only item 3 (the Alpine
variant) remains unverified -- this run only ever exercised Debian, same
as every other deployment. `scripts/granule-diag.sh` needed a real fix
first (it predated `-net flat` becoming the default and misdiagnosed a
healthy flat-mode guest as broken); see that script's own header for
what changed.

### Two things measured while porting, both load-bearing

Neither is a matter of taste; each produces a guest that boots cleanly and
then fails in a way that says nothing about its cause.

**granule's guest has no `ip=` handling, and grain's kernel needs some.**
granule's `deploy/guest-image/README.md` states its guest needs no
guest-side networking setup because *"it relies on the kernel's built-in
`ip=` boot-time autoconfiguration"*. That holds only for a kernel with
`CONFIG_IP_PNP`, which granule ships none to have. Debian's stock kernel
does **not** enable it, so nothing acts on `ip=` without the klibc
`ipconfig` unit `guest-setup.sh` kept -- and `granule` hard-codes
`eth0` in the `ip=` it derives, which systemd's predictable-naming policy
renames away from without the link file beside it. Neither has any
equivalent in granule's overlays.

**granule's SSH console wrapper used to break grain's sandbox tools, and
is gone.** For the record of why this directory once cared: the guest
forced every SSH session through `granule-ssh-console-wrap`, which ran it
under `script` -- i.e. under a pty, which is not a transparent pipe.
Measured against the real wrapper at the time:

| | through the wrapper |
|---|---|
| `cat` of a file with `\n` endings | came back `\r\n` -- `read_file` corrupted every file it read |
| stdout vs stderr | merged onto the one pty -- `run_command` lost the split, and a failed `cat`/`dd` reported an empty error |
| exit status | survived (`script --return`) |
| stdin | survived byte-for-byte |

`guest-setup.sh` used to undo that drop-in, and then the base was built
with `GUEST_CONSOLE_WRAP=0` so there was nothing to undo. Neither is
needed now: `granule exec` does not go over SSH at all
(bwsalmon/granule#46), the guest runs no sshd, and a session is
byte-transparent with stdout and stderr as separate frames on the wire.
A pty is allocated only when a caller asks for one by running
`granule exec` with a terminal on stdin.

### Wiring: done, then superseded

For a while `build-guest.sh` ran exactly one `docker build` against
`third_party/granule`, handing `guest-setup.sh` to its
`GUEST_SETUP_SCRIPT` build arg and exporting `disk.img`/`vmlinuz`/
`initrd.img` through `--target guest-artifacts --output type=local`. Then
it derived the guest from a granule base image by booting it, and now
the `Granulefile` builds it. See "How the image gets built and
published" below.

Two variables that shaped this directory's history are worth recording
because both are gone. `SANDBOX_SETUP_SCRIPT` was a pass-through for a
caller wanting to add to `guest-setup.sh`, spliced into that script's own
text; deriving from a published image replaced it (see "Customizing this
guest"). `OPERATOR_SSH_PUBLIC_KEY` was what made the image
deployment-specific at all, and its removal is what made everything since
possible: bwsalmon/granule#35 moved the `granule exec` keypair to one
generated in the VM's own container on every boot, handed to the guest on
its kernel command line, with `granule vm create -guest-user` naming
the account to authorize it for -- which closed the last gap here, granule
authorizing **root** while this guest's sandbox account is `debian`.
`-granule-exec-key` and the key staging `scripts/setup.sh` did for it are
both gone.

The setup script's own inputs went the same way. `build-guest.sh` spliced
`GO_VERSION` and a URL into its text, and served the four dependency
manifests over HTTP from a small file server for the length of the build
(`manifest-server/`), because the script reached the guest as a single
`granule exec` argument -- which Linux caps at 128KB (`MAX_ARG_STRLEN`) --
and the manifests had crossed that cap in September 2026. None of that
survives: the `Granulefile` is a docker build whose context is the
repository, so nothing has to be smuggled into a guest.

## Why no custom kernel

Debian's own `linux-image-amd64` package -- the same kernel package any
`apt-get install` on a stock Debian box would pull in, nothing special
selected or configured -- already has everything a cloud-hypervisor
direct-kernel-boot guest needs, confirmed by hand, under real KVM, against
a real cloud-hypervisor binary:

- **`CONFIG_PVH=y`** is already set in Debian's shipped kernel config
  (`/boot/config-<version>`) -- the entry point cloud-hypervisor's direct
  kernel boot uses, with nothing to enable or rebuild.
- **`virtio-pci`, `virtio_blk` and `virtio_net`** are all present as
  modules and bind automatically to cloud-hypervisor's virtio-pci
  transport -- this is precisely the driver the firecracker-ci kernel
  above lacks, and the reason that kernel panics on this VMM specifically.

So the "kernel" this image ships is just `/boot/vmlinuz-*` and
`/boot/initrd.img-*` out of a normal `apt-get install linux-image-amd64`.
The `Granulefile` runs that install itself, in the same `apt-get install`
as the rest of the base packages, and regenerates the initramfs once the
interface-naming mask below is in place; `VM PACK` packs both with the
disk.
There is no from-source kernel build anywhere in this pipeline, and no
firmware/GRUB/UEFI boot path either (cloud-hypervisor's other option,
`CHV_FIRMWARE` -- see below for why that path was ruled out, not just left
unexplored).

Two gaps still had to be closed by hand, both confirmed by booting the
actual built image under real KVM before this pipeline was considered
done. Both are consequences of granule's addressing rather than anything
grain wants: the naming mask is in the `Granulefile` as it is in
granule's own Debian rootfs, and the addressing helper is in granule's
Debian guest overlay (bwsalmon/granule#36), which `VM PACK` lays over the
rootfs:

- **The guest's one NIC needs to be named `eth0`.** `granule` itself
  (`internal/staticpod/spec.go`) derives each VM's static-addressing
  kernel command line as `ip=<ip>::<gateway>:<netmask>::eth0:off`, with
  `eth0` hard-coded -- but systemd's predictable-naming policy renames a
  virtio-net device to something like `ens2` by default, and the rename
  happens inside the *initramfs* (its own bundled, and by default stale,
  copy of `/etc/udev`), not just the final root. The `Granulefile` turns
  predictable naming off wholesale -- masking udev's
  `80-net-setup-link.rules`, the rule that applies `.link` files at all,
  the same mask granule's own Debian rootfs carries -- and re-runs
  `update-initramfs -u` afterwards, so the initramfs's own udev snapshot
  has it too; skipping that second step was confirmed by hand to leave
  the guest unreachable regardless. A `.link` file alone is not enough:
  one forcing `Name=eth0` on `Type=ether` matches every NIC and can only
  win once, so granule's flat mode -- which gives this guest two, the
  spliced NIC plus the control link -- left the second unnamed and the
  control link unconfigured. With the rule masked, the `00-eth0.link` the
  `Granulefile` also writes is never applied; the kernel's own
  `eth0`/`eth1` stand in PCI probe order, and a single-NIC NAT-mode guest
  still gets exactly `eth0`.
- **Static addressing itself needs a small userspace helper.** Debian's
  kernel does not set `CONFIG_IP_PNP`, so nothing in the kernel itself
  acts on `ip=` the way a kernel built with in-kernel IP-config would.
  klibc's `ipconfig(8)` (pulled in transitively by `initramfs-tools`, via
  `klibc-utils`) implements the same static-addressing syntax, but --
  unlike the in-kernel code -- does not read `/proc/cmdline` itself; it
  only takes the spec as an explicit argument. granule's Debian guest
  overlay installs a small wrapper (`granule-configure-net`) plus a
  `systemd` oneshot unit
  that extracts `ip=`'s value from `/proc/cmdline` and hands it to
  `ipconfig` directly. Also needed, and easy to miss: `virtio_net` has to
  be `modprobe`d explicitly (nothing auto-loads it early enough on its
  own) and the unit has to run after `systemd-udev-trigger`, or `ipconfig`
  finds no configurable device at all.

### Why `CHV_FIRMWARE` (a real alternative) was ruled out, not just skipped

cloud-hypervisor also supports booting through firmware (an EDK2/OVMF-like
build, `CLOUDHV.fd`) instead of a direct kernel -- which would let a
guest run its own bootloader/kernel of its choosing, sidestepping the
"does this specific kernel support direct boot" question entirely.
It was not used here because it does not compose with granule's own
per-slot static addressing at all, not because of any missing
kernel/firmware feature: `internal/hypervisor/args.go`'s `BuildArgs` only
passes `--cmdline` (and so the `ip=` granule derived) when a *kernel* is
given; the `else` branch (`--firmware` alone) never passes a command line
to the guest at all. There is no other channel in granule today for
getting a static address into a firmware-booted guest, so a NAT-mode
deployment -- the one that still passes an address at all, through
`GranuleConfig.IP`/`Port` (bwsalmon/agents#466, when those were still a
per-slot `BaseIP`/`BasePort`) -- would have no way to actually reach the
guest. Direct kernel boot is the only path that is already wired all the
way through.

## What's in the image, and why

The `Granulefile` mirrors v1's sandbox provisioning package-for-package: git,
build tooling, Docker + kind (the node image is not pre-pulled: nothing
in a docker build has a running dockerd to pull it with), and `gcloud`/`terraform`
for tasks whose deployment mints a per-task GCP key. bwsalmon/agents#267's
own text asks for exactly this -- "whatever v1's own sandbox image
already carries" -- rather than a leaner image scoped to only what v2's
four current MCP sandbox tools (`run_command`/`read_file`/`edit_file`/
`write_file`) exercise directly. `run_command` runs arbitrary shell, so a
dispatched task's own build/test step can reach for anything in that list
the same way a v1 sandbox lets it; trimming the list now would just move
the "actually I needed X" discovery from this decision to some later
task's failed dispatch, for an image that is not cheap to iterate on the
way a Python provisioning script is (see "One image, uniform" below).

That last sentence is not hypothetical: `procps`, `file` and `shellcheck`
were added on exactly that evidence (grain/task-215). Counted across 183
runs of this deployment's own task history, 37 hit a `command not found`,
and 42 of the 67 misses were procps' binaries -- `pgrep`, `ps`, `pkill`,
`free`, `uptime`. The cost of a miss is not only the wasted turn: `exit
127` in front of an `&&` chain takes the rest of the chain with it, and
in one run that dropped the `git commit` on the end. So the list here
grows by what dispatched runs are measured reaching for, and the place to
look for the next addition is the same stderr.

OCR -- `tesseract-ocr`, plus `imagemagick` and `poppler-utils` to feed it
-- is the one addition that did not come from that stderr, because the
failure it fixes produces none. A task's attachments are placed in the
sandbox as files (`pkg/orchestrator/attachments.go`), and `read_file` and
`cat` hand back what is in them: for a screenshot of the failing page, or
an error dialog pasted into a comment, that is binary, and a run that
cannot read it does not report a missing command -- it quietly answers
from the task's prose instead. `tesseract shot.png -` is the whole fix,
and ~130MB of the image.

`fonts-urw-base35` -- ~17MB, with the xfonts tooling it depends on -- is
the same three tools used in the other direction, drawing an image
rather than reading one, and it is here because the failure it fixes is
loud in a way that says nothing. ImageMagick resolves its *default* font
through `/etc/ImageMagick-6/type.xml`, which `imagemagick-6-common`
ships listing the URW base-35 faces whether or not any is installed, and
those normally arrive over the Recommends this list turns off. With the
files missing, `convert -annotate` and `label:` raise SIGABRT -- a core
dump, exit 134, nothing on stderr -- so a run that annotates a
screenshot before attaching it back learns nothing from the crash, and
the obvious guess does not help either: `fonts-dejavu-core` is already
installed here (`fontconfig-config` depends on it) and the abort happens
anyway. It is named on the install line beside URW, for no bytes, only
so that it stays -- apt would otherwise satisfy that dependency with the
URW package and quietly drop DejaVu, which is the font `fc-match sans`
answers with and the one anything naming a font by path means.

One thing the `Granulefile` does that v1's sandbox script didn't, because
a granule VM has no per-VM provisioning hook analogous to
`LibvirtAdapter.render_domain_xml`/cloud-init NoCloud user-data (granule
manages a VM's lifecycle itself -- `pkg/granule`'s doc comment -- not
through a cloud provider's metadata service a NoCloud datasource would
ride on):

- **The `debian` account itself is created here.** On v1's own sandbox
  base (a stock Debian cloud image), `debian` is cloud-init's
  `default_user`, already present before v1's sandbox script ever
  runs (that script's own comment: "The default cloud-init user"). This
  image has no cloud-init and no cloud image underneath it at all, so
  nothing creates that account except the `Granulefile` itself -- same
  name, so every downstream assumption (`granule vm create -guest-user`,
  v1's own libvirt-driver convention, the docker- and kvm-group grants)
  keeps holding, with the same passwordless-sudo grant a cloud image's
  own `default_user` normally carries.

There is no sshd and no authorized key to bake in: `granule exec` reaches
the guest over virtio-vsock with a keypair generated per boot
(bwsalmon/granule#35, #46).

**Not** baked in, on purpose, matching v1's controller provisioning's own
rule ("no secret is ever baked into an image or a provisioning script",
`docs/design.md`, "Secrets on /data"): no GitHub token, no GCP key, no git
identity/credential helper. Per-dispatch git configuration (`credential.
helper = store`, the `grain agent` identity, the proxy token) is set at
runtime against a live sandbox the same way v1's `configure_git_credentials`
(v1's Python dispatch) and `mcp.ConfigureGitCredentials`
already do it -- "arrives with git already configured" (`README.md`)
turns out to mean only "the `git` binary is on `PATH`", confirmed against
both functions: neither one assumes any baked-in `.gitconfig`.

Claude Code itself stays off the guest, for the same reason
v1's sandbox script gave for itself: it runs against this
VM's SSH-exposed sandbox tools from the controller/orchestrator side, not
on the guest, so there is nothing here worth a credential leak protecting
in the first place.

### The Go and Node toolchains, and why their caches are the point

A sandbox is where the merge queue's own fix tasks run
(`orchestrator.fileFixTask`). Every one of them used to end its commit
message with some version of the same sentence -- *"not built or run:
this sandbox has no Go toolchain and no network to fetch one"* -- and a
fix agent that cannot run `go test ./...` is guessing. A merge fix that
is a guess costs another queue cycle when it turns out not to be the fix,
which is the whole thing the queue exists to avoid. So the image carries:

- **Go**, at the version `go.mod` asks for. The `Granulefile`'s
  `ARG GO_VERSION` names it, and `tests/deploy` holds that to `go.mod`'s
  own line, so the image and the module cannot drift apart while only one
  of them is ever edited; `GOTOOLCHAIN=local` (also `Dockerfile.build`'s
  pin, for the same reason) turns a stale image into an error naming both
  versions rather than a network fetch.
- **Node**, at the `Granulefile`'s `NODE_VERSION`. Debian's own `nodejs`
  is 18, and `vitest` -- what `npm test` runs -- needs 20.19 or newer.

The guest build before the Granulefile also warmed a Go module cache and
an npm cache from `go.sum` and `ui/package-lock.json`. The `Granulefile`
does not: it installs the toolchains and resolves no manifest, so a
sandbox fetches its modules and packages the first time it builds.
`guest-inputs.sh` still names the four manifests, so a change to one
still rebuilds the guest.

Those caches were written as the load-bearing half rather than an
optimization, on the premise that a dispatched sandbox reached nothing
but the git proxy and a cold cache therefore could not build anything at
all. That premise was a bug, twice over: flat mode derived the guest's
`ip=` parameter with an empty gateway field, so every sandbox booted
without a default route and lost the open egress `docs/design.md` says it
has -- and once that was fixed, the guest still had no working resolver,
since its `/etc/resolv.conf` was whatever the machine that built the
granule base happened to have (`grain/task-200`). Both fixes are in the
vendored runtime tree, not this one
(`third_party/granule/VENDORED.md`, "Local patches") -- nothing in this
directory changed. A deployment names its own resolver with
`GRAIN_GRANULE_DNS` (`scripts/setup.sh`), which reaches the guest on its
`ip=` parameter per boot, so it needs no image of its own. With it, a
cold cache is survivable -- which, with no warmed cache in the image, is
now every sandbox's first build. A deployment that narrows egress again
(`egress_policy(allowlist)`) has to leave the module proxy and the npm
registry reachable, or route them through the package cache
(`pkg/pkgcache`).

**Playwright's browsers are installed once, not per `npm ci`.** The
`Granulefile` installs chromium and webkit into `/var/cache/playwright`
and sets `PLAYWRIGHT_BROWSERS_PATH` to it. `@playwright/test` is in
`ui/`'s devDependencies, and its install script would otherwise fetch
the browsers again on every plain `npm ci` in `ui/` -- which is what
`make frontend` and therefore `make test` runs. The image wraps `npm` to
default `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD` on for exactly that, with
`PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD= npm ...` as the way back to fetching
them.

The cost is disk, paid on every pull of the published image: the
toolchains and browsers are well over a gigabyte of the rootfs `VM PACK`
sizes `disk.img` to (rootfs plus 20%). Weighed against a fix agent that
can only read a diff and reason about what CI would have done with it,
that is still the cheaper side.

## Running a custom setup script
## Customizing this guest

There is no environment variable for it any more, and that is a
simplification rather than a removal. `SANDBOX_SETUP_SCRIPT` existed
because the only way to add to this guest was to get a second script into
a `docker build` that could not otherwise reach one -- `build-guest.sh`
spliced its text into `guest-setup.sh` before handing that to granule's
`GUEST_SETUP_SCRIPT` build arg, which is as awkward as it sounds.

Customizing is now deriving from a published image. For a deployment's
own sandboxes, build an image `FROM ghcr.io/bwsalmon/grain/guest-base`
and name it in Settings → Sandboxes → Custom sandbox image
(`model.Config.SandboxImage`, `docs/sandbox-image-contract.md`). By hand
it is:

```sh
granule guest build \
  -from ghcr.io/bwsalmon/grain/guest:latest \
  -setup ./my-setup.sh \
  -t my-guest:dev
```

What comes out is another runnable granule image, which can itself be the
`-from` of the next one. So the extension point is the tool, the
customization is an ordinary script rather than a variable holding a
script's text, and nothing here has to know it happened.

The environment such a script gets is a *booted* guest: systemd is
running, so `systemctl start` works and dockerd can actually run --
unlike the container build the old hook ran in, where anything that
needed a running service manager was simply unavailable. It needs
`/dev/kvm` on the machine doing the derivation, which the stock build
itself no longer does (see below).

Like everything else in this image, the rule from "What's in the image,
and why" above still applies: no secret belongs in a script baked in at
build time, since it ends up in the shipped image for anyone with that
image to read back out.

## How the image gets built and published

### It is a Granulefile now

Each earlier answer to "how does this get built" is worth keeping
straight, because the reasons for each are what the current one rests
on.

The original pipeline used [Packer](https://www.packer.io/)'s `qemu`
builder: boot a stock Debian cloud image, reach it over SSH via a
throwaway NoCloud seed, provision the live VM. That was ruled out because
it could not produce the file shape a direct-kernel-booting guest needs --
a stock cloud image ships as one bootloader-dependent GRUB/UEFI qcow2,
not a kernel/initramfs/disk triple -- and because its provisioning step
was a shell script over SSH, no more capable than a chroot for the one
thing anyone wanted a boot for (pre-pulling the kind node image, which
needs a running dockerd).

A `docker build` against `third_party/granule`'s own Dockerfile replaced
it and needed neither root nor a VM: `debootstrap` in one stage,
`guest-setup.sh` as an ordinary `RUN` in the next, `mke2fs -d` in the
third. After that, for a while, the guest was derived from a published
granule image by `granule guest build` (`build-guest.sh`): boot it, run
`guest-setup.sh` inside over `granule exec`, scrub per-boot identity,
commit the container. That cost `/dev/kvm` on whoever built, and tens of
minutes of apt and toolchain downloads inside a VM nested in a CI
sandbox.

The `Granulefile` is back to the first shape in a better form. Every
`RUN` -- every `apt-get`, every `curl` -- runs in an ordinary docker
build on the host, and `VM PACK` packs the result as one more step in a
container. Nothing boots, so building needs no `/dev/kvm`, and
`granule granulefile build -print -f Granulefile` prints the generated
Dockerfile for anyone who wants to see exactly what runs.

It is still not something a deployment pays. The guest is built once,
in CI, and every host pulls the result. A deployment used to run this
build itself, on every host, on every deploy generation -- because the
old setup script baked the deployment's own SSH public key into
`authorized_keys`, so no generic published image could exist. granule
generates that keypair per VM boot now, and nothing deployment-specific
reaches the disk.

### What comes out is another granule image

Not a disk to hand somewhere. The Dockerfile `VM PACK` generates ends
`FROM <granule image>` and copies granule's guest-side pieces out of it,
so the result carries `granule`, `cloud-hypervisor` and the guest disk,
and `docker run` on it boots a VM. Three consequences worth naming:

- The sandbox container and the guest are one artifact. `scripts/setup.sh`
  pulls one image and `granule` is given one `-kontur-image` -- by the
  daemon, out of the reference stamped into its own build, unless
  `GRAIN_GRANULE_OCI_IMAGE` pinned a different one; there is no
  `-disk`/`-kernel`/`-initramfs` and no host directory to resolve them
  against.
- It can be the input to another build. Customizing this guest is
  `granule guest build -from` it (above), so the extension point is the
  tool rather than an environment variable spliced into a script.
- Everything packed onto the granule image inherits its labels,
  including `org.opencontainers.image.source`. The vendored Dockerfile's
  names *granule's* repository, and GHCR uses that label alone to decide
  which repository a package belongs to -- so `build-granule-image.sh`
  relabels when `GUEST_SOURCE_REPO` is set. Without it the build is
  green, the push is green, and the package lands in someone else's
  namespace.

### Building and publishing

From the repository root:

```sh
./scripts/granule/build-granule-image.sh    # -> granule:vendored
go run github.com/bwsalmon/granule/cmd/granule granulefile build \
  -kontur-image granule:vendored -f Granulefile -t grain-guest:dev
```

`build-granule-image.sh` builds the granule image out of
`third_party/granule` -- not pulled, because a granule from anywhere else
is not the one this guest is built to match -- and the `granulefile
build` packs onto it by name, since granulefile's own default
(`granule:latest`) is an image nothing here builds. `granule` itself is
run from the vendored module for the same reason. It needs docker and
Go. `scripts/release-images.sh` is the same two steps with the caches
below in front of them, and `IMAGE=... scripts/granule-guest-resolve.sh`
is what CI jobs that need a guest run.

The disk that comes out is the rootfs plus 20% plus whatever
`GUEST_DISK_EXTRA_MB` asked for, and it is the floor a sandbox's disk
starts from rather than the whole of it: `granule` gives each VM an
overlay at exactly its backing image's size, and grain then asks for one
the size of `sandbox-disk-gb` (Settings -> Sandbox, or a per-task
override), which is 30 GiB on a deployment that has set nothing --
`granule vm create -disk-size-mb`, always passed. The guest's own half
of it is the `grain-growfs` unit the `Granulefile` installs, which grows
the root filesystem onto the larger device on each boot. Nothing about
the image build changes for it -- a bigger disk is a create-time
argument, not a different image.

No SSH key is involved anywhere in this. The image carries none
(bwsalmon/granule#35 -- granule generates one per VM boot instead), so
there is nothing here for `push-secrets.sh` to carry and no keypair for a
deployment and its guest image to keep in sync.

The base is built too, by `granule granulefile build -f
Granulefile.guest-base`. It used to be a script of its own,
`build-guest-base.sh`, called with the two args granule's CI publishes
its `debian12` variant with plus the disk headroom this guest installs
into; both of those went into the Granulefile itself when the pipeline
was converted, and neither was lost in the move, which is worth stating
because the shape of the change invites the opposite assumption:

- `GUEST_KERNEL_PACKAGE=linux-image-amd64` is now the literal
  `linux-image-amd64` in the base's own `apt-get install` line. Same
  kernel.
- `GUEST_DISK_EXTRA_MB=6144` is gone, and correctly. It was added
  (41c6f9521) because the old `guest-setup.sh` installed ~434MB *inside a booted
  guest, after packing*, so that space had to be reserved at pack time or
  apt ran out of it. Nothing is installed after packing any more --
  every `RUN` is in the container stage that `VM PACK` then sizes, at
  rootfs plus 20% -- so there is nothing to reserve for. And it is not
  what a running sandbox's disk is in any case: `vm create` is passed
  `-disk-size-mb` from `SandboxDiskGB` (grain's own default,
  `granule.DefaultDiskGB`, is 30GiB), and `grain-growfs` grows the
  filesystem onto it on each boot.

(Re-checked by grain task 517; `granule granulefile build -print -f
Granulefile` shows the generated `GUEST_DISK_EXTRA_MB=0` and the apt
line, if you would rather look than take it on trust.)

The base was pulled at
first, by the immutable per-commit tag granule's CI writes -- which does
not work from grain's CI at all, for a reason worth recording rather than
rediscovering: a GitHub Actions `GITHUB_TOKEN` is scoped to its own
repository, so grain's can push grain's packages and cannot read
granule's, and the pull is denied however the login went.

Building it is the better answer regardless of that. `granule` is built
from the vendored tree and drives a guest built from the vendored tree,
so "they are one commit" holds by construction -- rather than by a pinned
tag someone has to remember to move alongside the vendored SHA.

CI skips building it when it can, and the way it does matters: it caches the
base in *grain's* own packages under a tag derived from the git hashes of
what `guest-inputs.sh base` names -- `Granulefile.guest-base` (which
holds the build args now), `scripts/granule/build-granule-image.sh` and
`third_party/granule` -- and pulls that instead of building when it is
there. The
scoping problem above does not apply to a package this repository owns,
and a content-addressed tag is not the kind of pin the paragraph above
argues against: it names the build's own inputs, so a vendored resync
invalidates it by arithmetic rather than by anyone remembering anything.

The finished guest is cached the same way and one level up, which is
what makes this build cost nothing on most commits. `guest-inputs.sh`
names the paths that decide what comes out of here -- today the three
Granulefiles, `scripts/granule/build-granule-image.sh`,
`third_party/granule`, `cmd/grain-connect/main.go`, `cmd/grain-sandbox`,
`pkg/hostscriptcli`, and the four
dependency manifests (`go.mod`, `go.sum`, `ui/package.json`,
`ui/package-lock.json`). Run it rather than trusting this list, which
has moved twice in three days. `third_party/granule` being on it is what
keeps "the `granule` that drives the guest and the guest it drives are
one commit" true by arithmetic rather than by anyone remembering: a
re-vendor invalidates the guest cache and pays for a rebuild, which is
the cost that buys it. CI
publishes each guest it builds under an
`inputs-<hash>` tag as well as its commit's own. A commit touching none
of those paths pulls that image and publishes its own tags for it
instead of building anything, so `guest:sha-<commit>` can name an image
built at an earlier commit: the same build of the same inputs, which is
the point rather than a compromise. What it costs is that the outside
world the build reaches -- the Debian archive, proxy.golang.org, npm,
the kind node image -- is frozen between changes to those paths, which
is the same trade the base cache makes and the same direction: every
commit between two lock-file changes gets the same guest.

The list is what has to be right, and `tests/deploy` is what keeps it
right: it reads the scripts and fails if any path they reach for is not
covered, because a missing entry is a change that does not invalidate
the cache -- a green build publishing a guest that is missing what that
commit added.

What no such check can see is an input that is not a path in this
repository at all: an upstream package version, a base image tag
resolved at build time, a default that changed inside a tool the
`Granulefile`'s `RUN` steps shell out to. Those move without the hash moving, so
CI republishes a stale guest while going *faster*, which is the failure
mode nobody investigates. `.github/workflows/nightly.yml`'s
`guest-cache-drift` job is the check for it: once a night it builds this
guest from scratch with both caches ignored, boots it and the guest the
cache would have served, and diffs what each actually contains
(`scripts/granule-guest-probe.sh`, which boots each image and writes down
its toolchains, package set, units, and module and npm cache contents).
What this repository pins has to match, and a difference fails the job
with the diff; what the trade above deliberately freezes -- package
versions, the guest kernel, the base image's own userland -- is reported
without failing it, because a difference there is that trade working
rather than breaking. `docs/nightly-test-review.md` says how to read
each.

`scripts/release-images.sh` runs exactly this on every merge into `main`,
from the Grain CI release job (`.grain/workflows/release.yml`);
`.github/workflows/build-artifacts.yml` runs it on every branch where
GitHub Actions still run. Both publish the result as `guest`, read and
fill the same content-addressed `guest:inputs-<key>` cache, and stamp the
per-commit tag into the grain image built from the same commit
(`cmd/grain/sandboximage.go`). A deployment resolves it by asking its own
binary, so grain and its sandbox are always one commit's worth of each
other -- including after a rollback, which asks for its own older sandbox
rather than whatever is newest.

## Networking

Covered above under "Why no custom kernel" (the `eth0`-naming and
`ip=`-handling gaps and how the guest closes them) since both are
consequences of the kernel decision, not independent choices.

### Two guest fixes that left with the vendored kontur tree

`third_party/kontur` is gone -- the runtime is granule now, vendored at
`third_party/granule` -- and its `VENDORED.md` went with it. That file
was the only record of two local patches this repo was carrying on the
runtime, both written for upstream and neither ever landed on kontur's
`main`. **One of them survived the move to granule; the other did not,
and no longer needs to.** That is worth writing down rather than
rediscovering: a patch nobody knows about is a sandbox that quietly
stops working. Both live on in this repo's history at the commits named
below, which is where to read either one in full.

Both claims below were re-checked against the vendored tree by grain
task 517 -- read, and run, rather than inferred.

**The guest's one attempt at addressing itself** -- commit `b8503b73d`
(grain task 137), and **present in granule**, so nothing is outstanding.
Four guest-image files (`kontur-configure-net`, `kontur-configure-routes`
and the two systemd units that ran them) plus tests in
`internal/netshim`, in the names the vendored kontur tree used. Without
it, a guest built with `GUEST_KERNEL_PACKAGE` -- the `debian12` variant,
the only one grain runs -- boots, answers `granule exec` over vsock, and
has no address and no default route for the rest of its life. Debian's
kernel does not set `CONFIG_IP_PNP`, so nothing acts on the `ip=`
parameter netshim derives, and klibc's `ipconfig(8)` -- run once, as
early in the boot as a unit can -- is the only thing that ever addresses
such a guest. Against a NIC udev is still probing it exits 1 inside a
millisecond (`SIOCGIFINDEX: No such device`) and nothing retries; the
route script then pruned the default route it had just failed to
install. The fix waits for the device, retries, judges itself on whether
an address landed rather than on `ipconfig`'s exit status, orders both
units `Before=sysinit.target` as well as `WantedBy=` it, and fails
loudly on the console. This is the failure read as a test-concurrency
problem three times (grain tasks 109, 116) before anyone found it.

Granule carries all of it, renamed rather than copied:
`deploy/guest-image/overlay-debian/usr/local/libexec/granule-configure-net`
and `.../overlay-common/.../granule-configure-routes` wait on
`GRANULE_NET_DEVICE_TIMEOUT` and retry `GRANULE_NET_ATTEMPTS` times,
judging themselves on `ip -4 -o addr show`; the routes script skips its
pruning pass when an install failed; `granule-net-cmdline.service` and
`granule-net-routes.service` are ordered `Before=...sysinit.target` and
log `journal+console`; the tests are
`pkg/netshim/guestnetscript_test.go` and `guestscript_test.go`, and
`go test ./pkg/netshim/` in `third_party/granule` is green.

Upstream has since gone one better than the patch this section is
about. `granule-configure-net` polls sysfs every 25ms
(`GRANULE_NET_POLL_INTERVAL`) rather than sleeping a second a turn, and
`udevadm settle` is gone from the unit altogether instead of being kept
as a best-effort `ExecStartPre=-` -- the script waiting for its own
device is the whole mechanism now, which is what the `-` was conceding
anyway. So the behaviour grain needs is present and then some;
re-checked against the resynced tree by grain task 517.

**`guest build` takes a resolver** -- commit `facc079ac` (grain task
371): a `-dns` flag on `guest build`, spelled and defaulted as `vm
create`'s own. **This one did not make it, and the hole it was patching
has since been filled from the other side.**

Granule's `guest build` still takes no `-dns`, and neither does
`granulefile build`: `granule guest build -dns 1.1.1.1 ...` answers
`flag provided but not defined: -dns`, `guestbuild.Options` has no DNS
field (`third_party/granule/pkg/cli/guest.go`), and the flag exists on
`vm create` alone (`pkg/cli/vm.go` -- which is a different thing, and
the one `scripts/setup.sh` passes `GRAIN_GRANULE_DNS` to).

What changed is that grain's guest build no longer boots a VM. Since the
Granulefile conversion it is `granule granulefile build -f Granulefile`,
and a Granulefile is a Dockerfile with `VM PACK` in it: every `RUN` --
every `apt-get`, every `curl` -- runs in an ordinary docker build on the
host, and the packing is one more `RUN` in a container. Nothing boots,
so there is no netshim in front of anything, so there is no `8.8.8.8`
default to override; the build resolves exactly as every other fetch in
the same job does. `granule granulefile build -print -f Granulefile`
prints the generated Dockerfile, which is where to check that rather
than take it on trust.

So the failure grain task 130 measured -- 268MB of apt archives at
28MB/s from inside the build VM, then `curl: (6) Could not resolve host:
nodejs.org` -- cannot recur in this shape, and `-dns` is not owed to
granule on grain's account. The plumbing built for it has been removed
(`scripts/granule-guest-build-dns.sh`, and the `-dns` that
`granule-guest-resolve.sh` computed and then dropped on the floor). If a
`-setup`-style build that really boots a VM is ever reintroduced here,
this becomes outstanding again, and re-landing it is a change to granule
rather than a patch kept here.

## One image, uniform

v1 uses exactly one sandbox base image for every sandbox VM, with no
per-task or per-target-repo variant -- whatever a task needs beyond the
base image is either already on it (this directory's whole premise) or
fetched by the task itself at dispatch time via `run_command`. This
directory follows the same rule: `scripts/granule/` builds one image, and a
deployment's `GranuleConfig.CreateArgs` (once wired) points every dispatch
slot at the same one. Varying it per task or repo would mean rebuilding
and republishing an image on a per-dispatch cadence, for toolchain
differences `run_command` can already paper over inside a single uniform
image (installing an extra package or language runtime for one run costs
that run some seconds; it doesn't justify a second image to maintain,
test, and keep from drifting out of sync with the first).
