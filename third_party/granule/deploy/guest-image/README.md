# Guest disk image

The `guest-image` stage in the top-level `Dockerfile` (packing) plus one
of the two `guest-rootfs-debian`/`guest-rootfs-alpine` stages (building
the rootfs `guest-image` packs, via the `guest-customized` stage in
between -- see "Running a custom setup script" below) build the disk
image baked into the final `granule` image at
`/var/lib/granule/guest/disk.img`, used automatically when a VM container
doesn't set `CHV_DISK_IMAGE` (see `pkg/config`). `GUEST_DISTRO`
(default `debian`) picks which of the two rootfs stages runs:

- `debian` (default): a minimal Debian rootfs (`debootstrap
  --variant=minbase`) with `systemd-sysv` (for `/sbin/init`), `iproute2`,
  `acpid`, `udev`, `kmod` and `e2fsprogs`. `GUEST_SUITE` (default
  `bookworm`) picks the Debian suite; `bullseye`, `bookworm` and `trixie`
  are the ones built and checked -- weekly, by
  `.grain/workflows/guest-suites.yml`, which builds and packs each of
  them so that a suite moving out from under this recipe is found
  without anybody passing the build arg by hand. That workflow is where
  the list above is kept honest: a suite added to it here is added to
  the matrix there in the same change, and a suite nobody is willing to
  pay a weekly build for is not one this file should claim is checked.
  `kmod` is listed explicitly because
  `--variant=minbase` installs priority-required packages and nothing
  else, so there is no `/sbin/modprobe` -- which `granule-agent.service`
  uses to load the virtio-vsock driver before it starts. `e2fsprogs` is
  listed for the same reason, and is what made `GUEST_SUITE` worth being
  careful about: `granule-growfs` runs `resize2fs`, and e2fsprogs is
  priority *required* on bookworm and bullseye (so `minbase` installed it
  unasked) but only *important* on trixie (so it did not) -- naming it
  means the suite's priority set can move without taking the guest's
  ability to grow its filesystem with it. `udev` has to be listed
  explicitly too -- `--variant=minbase` skips
  Recommends, and without `systemd-udevd` running, `dev-ttyS0.device`
  never gets marked ready, which stalls boot for
  `DefaultDeviceTimeoutSec` (systemd's default: 90s) before
  `serial-getty@ttyS0.service` gives up on it; `granule exec` doesn't
  depend on that device unit and isn't affected, but the console would
  otherwise never show a login prompt.
- `alpine`: a minimal Alpine rootfs (`apk add --root`, the same technique
  Alpine's own official base image is built with) with `alpine-base` (for
  `busybox`/`openrc`, standing in for `systemd-sysv`), `iproute2` and
  `acpid`. `GUEST_ALPINE_VERSION` (default `3.20`) picks the Alpine
  version.

Neither rootfs stage puts anything of granule's own into the filesystem
it builds: no overlay, no agents, no units. Those go on when the rootfs
is *packed*, by `deploy/guest-image/pack-guest` -- which is what
`granule guest pack` runs over a consumer's rootfs image, and what the
`guest-image` stage runs over this one. That is the whole point of
packing late: a rootfs published months ago is packed with today's
agents, and where granule's parts go inside a guest is written down in
one place rather than two. See "What's in the overlays" and
`docs/guest-images.md`.

Neither carries an SSH server. `granule exec` reaches the guest over its
virtio-vsock device instead, answered by `granule-agent`
(`cmd/granule-agent`, laid into both variants by the pack) -- see
"Getting a shell in the guest" below, and `pkg/guestexec` for why that
replaced SSH.

Packing is `mke2fs -d` straight from the rootfs directory -- no
loop-mount or `chroot` needed on the build host beyond what
`debootstrap`/`apk`/`mke2fs` already do, so `docker build` doesn't need
extra privileges for either variant.

This is a reference/demo guest, not a production one: any real workload
should bring its own `CHV_DISK_IMAGE` (see the top-level README's
"Configuration" table and `deploy/k8s/pod-example.yaml`) rather than
relying on what's baked in here.

## What's in the overlays

Copied verbatim into the rootfs by `pack-guest`, immediately before it is
packed into `disk.img`: `overlay-common/` on both variants, then
whichever of `overlay-debian/`/`overlay-alpine/` matches the init system
the rootfs runs. Nothing else places them -- not the rootfs stages, not a
consumer's build -- so granule's copy always wins and always comes from
the granule doing the packing.

These files are the guest contract, so adding to them is a versioned
change and not a free one. A guest packed today is booted by whatever
VMM half the operator has upgraded to since, and anything added here is
something a guest built before it existed does not have -- read
docs/guest-images.md, "Changing the contract", before adding a row to
the table below. Most of the time the answer is that the VMM half can do
it instead, which is why this list is as short as it is.

| File | Purpose |
|---|---|
| `overlay-debian/etc/systemd/system/granule-agent.service`, `overlay-alpine/etc/init.d/granule-agent` | Run `granule-agent` (a binary the pack lays in beside the overlay, from the granule image being packed onto, not part of either overlay) as a restart-always service, and `modprobe` the virtio-vsock driver first. Started in early boot and deliberately not ordered after the network -- see "Getting a shell in the guest". |
| `overlay-common/etc/acpi/events/powerbtn` | `acpid` event config matching the ACPI power button, pointing at `/etc/acpi/powerbtn.sh` -- see "Graceful shutdown" below for why that script's own contents differ per variant. |
| `overlay-debian/etc/acpi/powerbtn.sh` | Runs `systemctl poweroff`. |
| `overlay-alpine/etc/acpi/powerbtn.sh` | Runs `poweroff` (busybox's applet, which signals `/sbin/init` rather than powering off directly -- see "Graceful shutdown"). |
| `overlay-debian/etc/systemd/system/granule-mem-agent.service`, `overlay-alpine/etc/init.d/granule-mem-agent` | Run `granule-mem-agent` (a binary the pack lays in beside the overlay, from the granule image being packed onto, not part of either overlay) as a restart-always service. The agent signs its signals with a token granule writes, over the exec agent's vsock channel, to the root-only `/run/granule-mem-agent/token` once the guest is up; it sends nothing until that file exists. See the top-level README's "Memory hotplug". |
| `overlay-common/usr/local/libexec/granule-control-net` | Configures the guest's end of netshim's control link: brings up the second NIC at `169.254.100.2/24` and writes `GRANULE_MEM_AGENT_HOST` to `/run/granule-control-net.env` for `granule-mem-agent` to pick up. A guest with no second NIC (the control link disabled, or no netshim at all) has nothing to do and exits successfully, so the same image boots unchanged either way. See the top-level README's "Container networking". |
| `overlay-debian/etc/systemd/system/granule-control-net.service`, `overlay-alpine/etc/init.d/granule-control-net` | Run that script once at boot, ordered before `granule-mem-agent` so the address it writes is in place before the agent starts. |
| `overlay-common/usr/local/libexec/granule-configure-dns` | Writes `/etc/resolv.conf` from the nameservers on the kernel command line -- `granule.dns=` (either family, in order, at most three) when it is there, otherwise `ip=`'s `dns0`/`dns1` fields -- with the `search` and `options` lines `granule.dns_search=` and `granule.dns_options=` carry beside them. Run by `granule-configure-dns.service` (Debian) / `/etc/init.d/granule-configure-dns` (Alpine), in early boot. A command line naming no nameservers leaves the file alone. See "The resolver". |
| `overlay-debian/usr/local/libexec/granule-configure-net` | Configures the primary NIC from the kernel command line's `ip=` for a guest whose kernel lacks `CONFIG_IP_PNP` -- i.e. a `GUEST_KERNEL_PACKAGE` build. Exits immediately when the interface is already addressed, so it is a no-op under the bundled kernel. Waits for the device the `ip=` names and retries `ipconfig`, because this is the only thing that ever addresses such a guest. See "Networking". |
| `overlay-debian/etc/systemd/system/granule-net-cmdline.service` | Runs that script early, before `sysinit.target` is reached, so anything needing an address has one. Debian only: it exists to compensate for a distro kernel, which is a Debian-variant option. |
| `overlay-common/usr/local/libexec/granule-configure-routes` | Installs the routes the container runtime had on the sandbox's own interface, carried down the kernel command line as `granule.routes=` because `ip=` can carry an address, a netmask and a gateway and nothing else -- and the IPv6 table as `granule.routes6=`, in the same grammar, installed and pruned with `ip -6`, each family's failures kept apart from the other's. That is the whole difference between a bridge CNI, where the netmask is right, and a point-to-point one, where the subnet is deliberately not on-link. A command line with no `granule.routes=` on it (no netshim in front of this guest, or an operator's own `ip=`) leaves the table alone and exits successfully. See the top-level README's "Container networking". |
| `overlay-common/usr/local/libexec/granule-configure-ip6` | Gives the guest the IPv6 half of the sandbox's identity, carried as `granule.ip6=<global>/<plen>,<link-local>` because `ip=` is IPv4 only: turns off router advertisements, SLAAC and privacy addresses on the interface, stops the kernel generating a link-local address of its own (flushing one already generated), installs the pod's link-local and global addresses with `nodad`, and brings the link up. A command line with no `granule.ip6=` exits successfully, as does a kernel with no IPv6 (with one line on the console), so the IPv4 half still comes up. See docs/ipv6-dual-stack-design.md. |
| `overlay-debian/etc/systemd/system/granule-net-ip6.service`, `overlay-alpine/etc/init.d/granule-net-ip6` | Run that script once at boot, before `granule-net-routes`, whose IPv6 routes are installed over the addresses it puts in place. |
| `overlay-debian/etc/systemd/system/granule-net-routes.service`, `overlay-alpine/etc/init.d/granule-net-routes` | Run that script once at boot, after the address exists and before anything uses it -- on Debian, before `sysinit.target` is reached rather than merely wanted by it. |
| `overlay-common/usr/local/libexec/granule-growfs` | Grows the root filesystem onto whatever `/dev/vda` turned out to be, so `CHV_DISK_SIZE_MB` yields free space and not just a bigger block device. See "Growing the filesystem" below. |
| `overlay-debian/etc/systemd/system/granule-growfs.service`, `overlay-alpine/etc/init.d/granule-growfs` | Run that script in early boot, before anything starts writing into the space it makes. |
| `overlay-common/usr/local/libexec/granule-mem-online` | Writes `online` to `/sys/devices/system/memory/auto_online_blocks`, so memory hotplugged later (by `granule resize` or by `CHV_MEM_AGENT`) is onlined as it arrives rather than added and left offline. Needed because a distro kernel routinely leaves that off -- Debian's own, which `GUEST_KERNEL_PACKAGE=linux-image-amd64` installs, does -- while the bundled kernel has it on. A kernel with no memory hotplug at all exits successfully. See the top-level README's "Memory hotplug". |
| `overlay-debian/etc/systemd/system/granule-mem-online.service`, `overlay-alpine/etc/init.d/granule-mem-online` | Run that script in early boot, before the first hotplug can arrive -- which, with `CHV_MEM_AGENT`, is as soon as the guest is under pressure. |
| `overlay-common/usr/local/libexec/granule-declare-ready` | Creates the file this guest declares its own readiness with -- the path in `/etc/granule/ready-file`, which the `GUEST_READY_FILE` build arg writes. No config file, or an empty one, means there is nothing to declare and it exits successfully. See "Declaring the guest ready". |
| `overlay-common/etc/granule/guest-contract` | The version of the guest-side contract the pieces in this overlay implement, as one decimal number -- what `pkg/guestimage`'s `Contract` says, from inside the guest. Laid down by `pack-guest` with everything else here, and the same number goes on the packed image as the `dev.granule.guest.contract` label. See "The guest-side contract" below. |
| `overlay-debian/etc/systemd/system/granule-ready.service`, `overlay-debian/etc/systemd/system/granule-ready.target`, `overlay-alpine/etc/init.d/granule-ready` | Run that script once the guest reaches `sysinit.target` and `granule-agent.service` answers (or OpenRC's `after *` in the `default` runlevel on Alpine). Not enabled unconditionally: they ship in every image, and a pack enables them only in a rootfs carrying `/etc/granule/ready-file` -- which is what `GUEST_READY_FILE` writes. |

## Enabling services

A service that is installed and not enabled is a file, so `pack-guest`
enables granule's services as part of laying them down. Debian's
`systemd-sysv` and Alpine's `busybox`/`openrc` both need that done by
hand rather than through their normal tooling (`systemctl enable`,
`rc-update add`), since neither has a running service manager to ask --
each is just a symlink, written directly.

Where each one goes is read out of the service itself, so the fact lives
beside the thing that explains it rather than in the script:

- Debian: the unit's own `[Install] WantedBy=`, which is what `systemctl
  enable` would act on. `granule-mem-agent.service` and
  `granule-control-net.service` name `multi-user.target`.
  `granule-agent.service`, `granule-net-cmdline.service`,
  `granule-configure-dns.service`, `granule-net-ip6.service`,
  `granule-net-routes.service`,
  `granule-growfs.service` and `granule-mem-online.service` name
  `sysinit.target` instead, with `DefaultDependencies=no`: the first
  answers `granule exec` and is wanted before anything that could fail
  later in the boot has had the chance to, the second establishes the
  guest's address, the third its resolver, the fourth its IPv6
  address, the fifth gives it the routes
  that address is only usable off its own segment with, the sixth grows
  the root filesystem, which has to happen before anything starts
  writing into the space, and the last settles what happens to memory
  hotplugged later, which with `CHV_MEM_AGENT` can be minutes into the
  boot or seconds.
- Alpine: a `# granule-runlevel:` line in the init script, OpenRC having
  no `[Install]` of its own. `granule-agent`, `granule-configure-dns`,
  `granule-growfs`, `granule-mem-online`, `granule-net-ip6` and
  `granule-net-routes` name
  `boot`; `granule-control-net` and `granule-mem-agent` name `default`.

Two services are enabled *conditionally*, against a file in the guest
rather than unconditionally:

- `granule-ready`, where `/etc/granule/ready-file` names a path for it to
  create -- which is what the `GUEST_READY_FILE` build arg writes, and
  what a customized guest can write for itself later. See "Declaring the
  guest ready".
- `granule-container`, where `/etc/granule/container.json` describes a
  workload for it to run. A VM started with `CHV_CONTAINER_OVERRIDE`
  carries a per-VM override of that workload's process on a read-only
  disk with the serial `granule-ctr-override`; `granule-container` merges
  it in and writes the spec it runs to `/run/granule/container.json`,
  which is what `granule exec --container` enters. Before the workload
  starts it mounts each virtio-fs share the override's `mounts` name at
  its destination inside the container (see `docs/volumes.md`). It also serves the
  workload's stdout and stderr, each on its own, on vsock port 1025 for a
  host started with `GRANULE_WORKLOAD_STDOUT`/`GRANULE_WORKLOAD_STDERR`
  (see `pkg/workloadio`); the console still gets them as well. A workload
  whose spec opens its stdin (`openStdin`, or the override's `stdin`, set
  by `GRANULE_WORKLOAD_STDIN`) reads the host's stdin from that same
  connection, and one with `tty` runs on a pseudo-terminal the host can
  resize. It reports
  the workload's start, and its exit status before powering off, to the
  host on vsock port 1026, which is what granule-shim exits with.

What the rootfs stages still enable is the operating system's own
services, which nothing else would: on Alpine the `sysinit`/`boot`
baseline a busybox-init/OpenRC system needs to reach a usable state at
all (device nodes, `/proc`/`/sys`, hostname, clearing `/tmp`, syslog --
Debian's systemd does the equivalent internally, with nothing to enable
by hand for it), plus `local` and `acpid` in `default`. On Debian nothing
at all: `acpid`'s own postinst already enables `acpid.socket`/
`acpid.path` via `deb-systemd-helper`, which (like `systemctl enable`)
just writes symlinks and doesn't need a running systemd to do it.

## Getting a shell in the guest

`granule exec` (see the top-level README's "Execing into a VM") always has
a way in, and needs nothing set up: it connects to the VM's virtio-vsock
device, where `granule-agent` answers. There is no account to log into, no
key to authorize and no daemon to configure, because there is no login
involved -- the agent runs as root and runs each command as whichever
account the request names (`GRANULE_EXEC_USER`, defaulting to root).

That is a deliberate change from how this image used to work, and the
reason is worth knowing before adding sshd back out of habit. Exec used
to be SSH to a daemon in the guest, over netshim's control link, so
everything that brought that link up was load-bearing for being able to
get in at all: a guest whose networking had not come up was one granule
could not ask *why*. vsock is carried by the virtio device itself, so it
works before the guest has an address, while its network is broken, and
with no NIC attached -- which is exactly when you want a shell.
`granule-agent` is started in early boot for the same reason, ahead of
anything that could go wrong later in it.

It also means this image ships no SSH server, no host keys, no
`authorized_keys` and no per-boot keypair, all of which existed only to
serve that transport.

### If you want sshd anyway

For an operator's own interactive access, install it the way you would
install anything else in a guest -- in a setup script (see "Running a
custom setup script" below), which is also where the key to authorize
belongs:

```sh
cat > setup.sh <<'EOF'
#!/bin/sh
set -eux
apt-get update
apt-get install -y --no-install-recommends openssh-server
install -d -m0700 /root/.ssh
printf '%s\n' "ssh-ed25519 AAAA... you@example" > /root/.ssh/authorized_keys
chmod 0600 /root/.ssh/authorized_keys
EOF
granule guest build -from ghcr.io/gke-labs/granule:debian12 -setup setup.sh -t my-guest
```

Reaching it is then your own business, the same as any other service the
guest runs: the guest holds the pod's own address under flat mode, so an
sshd on it is reachable wherever that address is.

Note that a guest built this way needs its host keys scrubbed before the
image is shared, or every VM cloned from it has the same ones.
`granule guest build` already does this (see `pkg/guestbuild`'s
own `scrub`), which is why the recipe above is the recommended route
rather than committing a running guest by hand.

## Running a custom setup script

To customize the guest beyond what the overlays above do -- installing
extra packages, dropping in config files, enabling services, etc --
there are three mechanisms, and which one to reach for is mostly a
question of *when* the customization should happen rather than what it
can do. The exception is `granule guest build`, below, which is the
only one that can do things a container build cannot; it is also the one
to reach for if you are deriving a guest from a published image rather
than building granule yourself.

`GUEST_SETUP_SCRIPT` is a build arg holding the script's own text (not a
path), run inside the guest rootfs while the image is being built:

```sh
docker build --build-arg GUEST_SETUP_SCRIPT="$(cat my-setup.sh)" -t granule .
```

The result is baked into `disk.img`, so it is paid once by whoever
builds the image rather than once per host at first boot, and every VM
booted from that image already has it. Anything the script writes is as
public as the image is, so don't use it to place secrets.

One thing not to put in it: a kernel package. `GUEST_KERNEL_PACKAGE`
installs one *before* this script runs, and that ordering is load-bearing
-- see "Networking" for what a kernel installed at the wrong point does
to the initramfs, and the `guest-customized` stage for the mechanics.

It runs in the `guest-customized` stage, which promotes the rootfs to an
image of its own (`FROM scratch` + `COPY --from=guest-rootfs /rootfs/ /`)
and runs the script as an ordinary `RUN`. That is deliberate, and it is
what makes this mechanism usable at all: running something *in a
directory tree* means `chroot`, and a useful chroot needs `/proc` and
`/dev` bind-mounted into it -- `CAP_SYS_ADMIN`, which an ordinary
`docker build` does not have, and without which apt postinsts,
`systemctl enable` and `update-initramfs` variously misbehave or fail
outright. Running it as a container instead gives the script a real
`/proc`, a real `/dev`, working network and the rootfs as `/`, with no
extra privileges -- the same property the `guest-image` stage's
`mke2fs -d` already relies on. What it still doesn't get is a *running*
guest: no booted kernel, no service manager actually running, so
`systemctl enable` works but `systemctl start` does not.

`CHV_SETUP_SCRIPT` (see the top-level README's "Suspend and resume") is
the boot-time counterpart: it boots the actual guest and runs your script
inside it once, so it gets a running kernel, service manager and network
stack, and there's nothing it can't do that an interactively-administered
guest could. Pair it with `CHV_SNAPSHOT_PATH` to pay that cost only once:
the suspended snapshot after the first run stands in for what would
otherwise be a customized `disk.img`.

So: `GUEST_SETUP_SCRIPT` for installing packages and dropping in files,
`CHV_SETUP_SCRIPT` for anything that has to observe the guest actually
running. They compose -- a build-time script can install what a boot-time
one then configures against the live system.

## Declaring the guest ready

`granule-agent` answers seconds before whatever this guest boots into is
actually serving, so anything waiting on the VM from outside (`granule
vm wait`, a pod's readiness probe -- see the top-level README's
"Readiness") is by default waiting for the smaller of the two facts. A
guest closes that gap by creating one file when its own workload is up.

Both variants carry the machinery for that, and neither uses it unless
asked. `GUEST_READY_FILE` is the ask:

```sh
docker build --build-arg GUEST_READY_FILE=/run/granule/ready -t granule .
granule vm create web -ready-file /run/granule/ready -wait
```

That build enables one service in whichever overlay is being built --
`granule-ready.service` (`Type=oneshot`, `DefaultDependencies=no`, `After=sysinit.target granule-agent.service`, `WantedBy=sysinit.target`) on the
debian variant, `/etc/init.d/granule-ready` (`after *`, in the `default`
runlevel) on the alpine one -- and writes the path into
`/etc/granule/ready-file`. Both services run the same
`overlay-common/usr/local/libexec/granule-declare-ready`, which reads that
file and creates the path it names. The path lives in a config file
rather than in the service definitions so that the definitions stay
ordinary overlay files with nothing built into them, and so a guest
customized later can move its declaration by writing one line.

**Empty is the default, and installs nothing.** The published images are
built that way, so a stock guest is still ready at "the agent answers".
That is the same reason the check itself is opt-in: a guest that declares
nothing must not become a VM that is never ready, and a convention every
guest image has to follow is one some guest will not follow.

### What the reference guest can honestly declare

Early readiness reaching `sysinit.target` and the agent, and nothing more -- this guest boots into
nothing in particular, so there is no workload here whose readiness could
be reported. That is a real fact and a strictly stronger one than the
default (`granule-agent` is started in early boot, deliberately ahead of
anything that could fail later in it -- see "Getting a shell in the
guest"),
and it is what makes the feature demonstrable and testable without a
workload to hand. It is not a claim about anything *you* install.

So a guest with a workload in it should declare from the thing that knows
the workload is serving, not merely that sysinit was reached. Several ways, in
rough order of how little they cost:

- Specify a custom readiness condition or target via config:
  Write a condition command/script path into `/etc/granule/ready-condition`
  (e.g., `curl -sf http://127.0.0.1:8080/healthz`) or target into
  `/etc/granule/ready-target` (e.g., `granule-ready.target` or a custom target),
  which `granule-declare-ready` evaluates before creating the readiness file.
- Order the shipped unit after the workload, in a drop-in:
  `/etc/systemd/system/granule-ready.service.d/after.conf` with
  `[Unit]`/`After=nginx.service`. The alpine equivalent is an `after
  nginx` in `/etc/init.d/granule-ready`'s own `depend()`.
- Call `/usr/local/libexec/granule-declare-ready` from an
  `ExecStartPost=` on the unit that matters (an OpenRC `start_post` on
  the alpine variant), and leave `granule-ready` disabled.
- Run the check yourself and touch the file when it passes: `curl -sf
  localhost:8080/healthz && touch /run/granule/ready` in a loop is a
  guest-side concern that composes with this rather than something
  granule has to carry.

The last two need no `GUEST_READY_FILE` at all; the first works on a
guest built with it, and on one built without it after a
`systemctl enable granule-ready` and a line into `/etc/granule/ready-file`
(`granule guest build -setup`, or `GUEST_SETUP_SCRIPT` -- see "Running
a custom setup script").

The file only means anything to a VM told to look for it (`granule vm
create -ready-file /run/granule/ready`, or `GRANULE_READY_FILE` on the VM
container), and a guest that creates one nobody looks for is not harmed
by it either. `/run` is the natural place for it precisely because it is
a tmpfs: the declaration belongs to this boot, not to the disk image. A
path on the disk would still be there, stale and lying, on the next boot,
before the guest it describes has booted at all.

## Deriving a guest from a published image

`GUEST_SETUP_SCRIPT` needs a checkout and a full image build. To
customize a guest you *pulled*, boot it and commit the result:

```sh
granule guest build \
  -from ghcr.io/gke-labs/granule:debian12 \
  -setup ./my-setup.sh \
  -t my-guest:dev
```

That boots the base image's own guest under cloud-hypervisor, waits for
it to finish booting rather than for the first `granule exec` that answers
(the gap between the two is where a setup script races the boot), runs
`my-setup.sh` as root inside it over `granule exec`, scrubs per-boot
identity, powers the guest off, and commits the container. What comes out
is the same *kind* of image as what went in -- granule, cloud-hypervisor
and a bootable guest disk -- so `docker run` boots it exactly as the base
boots, and it can be the `-from` of another build.

Why bother, when `GUEST_SETUP_SCRIPT` installs packages perfectly well:
that hook runs in a container on the build host's kernel with no service
manager, so it can install and cannot *exercise*. This boots the real
guest, so `systemctl start` works, a docker image can be pulled into the
guest's own cache ahead of first use, and a test suite can be run against
the finished image before it is committed.

What it costs:

- **`/dev/kvm` on whatever runs the build.** It is a `docker run --device
  /dev/kvm --cap-add NET_ADMIN`, not a `docker build` -- deliberately, since
  a build cannot ask for a device without BuildKit's `security.insecure`
  entitlement, and this needs no entitlement, no privileged builder and
  no `mknod`. It does mean no building a customized guest on a machine
  without KVM.
- **The guest gets none of the builder's network context.** Its packages
  are fetched from inside a VM, which has no `HTTPS_PROXY`, no custom CA
  bundle and no registry credentials unless you pass them:
  `-docker-run-arg` is repeatable and goes straight to `docker run`. Its
  resolver is not the builder's either -- it is the build VM's own, and
  `-dns` names it, exactly as `vm create -dns` does for any other guest
  (see "The resolver"). Unset means `auto`: the build container's own
  resolver when the guest can reach it, and `8.8.8.8` when it cannot --
  as on a plain docker host, whose containers resolve through docker's
  embedded `127.0.0.11` or a link-local cloud resolver. On a restricted
  or air-gapped network whose resolver is neither, name it with `-dns`;
  a setup script that fetches a tarball by name is the one part of a
  build that notices. `-dns ''` leaves the guest image's own
  `/etc/resolv.conf` alone.
- **Each build adds a full copy of the disk as a layer.** `disk.img` is
  one large file in a read-only layer, so the guest's first write copies
  all of it up, and `commit` captures that copy. Deriving two or three
  deep is fine; a long chain wants squashing.

The scrub is not optional and not the caller's to remember: `/etc/machine-id`,
`/var/lib/dbus/machine-id`, any SSH host keys, systemd's random seed,
DHCP leases and `/var/log` are emptied after the setup script and before
shutdown. The host keys are on that list even though this image runs no
sshd, because a setup script is free to install one -- and a shared image
that carried its host keys would hand every VM cloned from it the same
identity. All of it is per-boot identity, and an image is cloned into many
VMs -- a baked machine-id gives every one of them the same journald
identity and DHCP DUID, and baked host keys make host-key verification
meaningless across the fleet. What the setup script installed or left
behind is left alone: that is size rather than correctness, and guessing
would delete things a caller meant to keep.

If the guest never becomes reachable, or its setup script fails,
`-keep-on-failure` leaves the container up and the error names it, so
`docker logs` shows the console and `docker exec <ctr> granule exec` gets
a shell in the guest.

## Exporting the built guest

The `guest-artifacts` target publishes the built guest for booting
somewhere other than inside this image:

```sh
docker build --target guest-artifacts --output type=local,dest=./out .
```

That yields `disk.img` and `guest-contract`, plus `vmlinuz` and `initrd.img`
when the guest
has its own -- i.e. when it was built with `GUEST_KERNEL_PACKAGE` (or a
`GUEST_SETUP_SCRIPT` that installs a kernel by hand), which a guest whose
workload needs a richer kernel config than the `build-kernel` stage's
`cloud-hypervisor/linux` build carries (overlayfs, cgroup v2,
bridge netfilter, veth, ...) will want. The newest `/boot/vmlinuz-*` and
`/boot/initrd.img-*` in the rootfs are what get published; a guest with
no kernel package installed produces neither.

On arm64 the kernel package is `linux-image-arm64`, and the `vmlinuz` is
published exactly as the package installed it. cloud-hypervisor's aarch64
direct kernel boot wants an uncompressed arm64 `Image`. Debian's arm64
`/boot/vmlinuz-*` already is one (bookworm and trixie alike). Ubuntu's is
gzip-compressed, though, and would have to be decompressed in the rootfs
before packing.

`GUEST_DISK_EXTRA_MB` adds headroom to that disk, and an image built to
be derived from wants it. The 20% the sizing above adds is space for logs
and runtime growth on a guest that is already finished; a guest
customized by `granule guest build` is provisioned *after* the
filesystem is packed, and cannot grow it. Without headroom an
`apt-get install` of anything substantial fails with "You don't have
enough free space in `/var/cache/apt/archives/`" after a completely
clean boot -- which reads as a broken setup script rather than a disk
sized before anyone knew what would go on it. It is not free, and the cost
is easy to misjudge: `truncate` leaves the extra as a hole and a pushed
layer of zeros compresses to almost nothing, but extracting a layer
materializes the hole, so every GB here is a GB on disk on every machine
that pulls the image. Size it against what the guest installs. granule's
own published `debian12` variant uses 2GB, which is more free space than
the 20% gave and small enough not to dominate the image.

The same pair is copied into the final image beside the disk, and
`pkg/config` boots whichever is there: the guest's own kernel when
the image has one, `build-kernel`'s `vmlinux` when it doesn't. So a
`GUEST_KERNEL_PACKAGE` build needs no `CHV_KERNEL`/`CHV_INITRAMFS` of its
own to boot correctly, and a default build behaves exactly as it always
has.

`guest-contract` is the contract version this disk speaks -- the copy of
`/etc/granule/guest-contract` that was packed into it, lifted out beside
it so a granule can read it before starting cloud-hypervisor. It has to
travel with `disk.img` wherever the disk is put: it describes that
filesystem and no other, and a granule booting a disk whose declaration it
cannot honour refuses rather than handing out a VM nothing can reach.
Moving the disk without it is not fatal today -- an undeclared guest
boots as version 0 -- but it throws away the only thing that would catch
the skew later. See docs/guest-images.md, "The guest contract".

The target is `FROM scratch` and contains nothing else, so the exported
directory holds exactly those files rather than a whole builder
filesystem. None of it bloats the `granule` runtime image: `final` is
still the Dockerfile's last stage and so still the default build target.

## Exporting the rootfs instead of the disk

`guest-artifacts` publishes the packed disk. The other half of the same
build is the rootfs it was packed *from*, and `guest-rootfs-image`
publishes that as an OCI image of its own. `deploy/publish-images.sh`
pushes it -- by hand, by a person with a credential; CI publishes
nothing -- so ordinarily there is nothing to build:

| | |
|---|---|
| `ghcr.io/gke-labs/granule/debian.rootfs:latest` | the default guest's rootfs |
| `ghcr.io/gke-labs/granule/alpine.rootfs:latest` | the Alpine one |
| `ghcr.io/gke-labs/granule/debian12.rootfs:latest` | the default rootfs with Debian's kernel package in it |
| `ghcr.io/gke-labs/granule/<variant>.rootfs:os-<key>` | the same three, immutable, for pinning |

The immutable tag is a *content key* and not a commit: twelve hex
characters of a hash over the rootfs recipe -- the Dockerfile's guest
rootfs stages, with their comments stripped, and the handful of build
args they read (`GUEST_DISTRO`, `GUEST_SUITE`, `GUEST_ALPINE_VERSION`,
`GUEST_KERNEL_PACKAGE`, `GUEST_SETUP_SCRIPT`, `GUEST_READY_FILE`). Print
one with `deploy/guest-rootfs-key.sh`, or ask the publish what it would
push with `deploy/publish-images.sh --print-rootfs-tags`.

The reason is what those stages now build. They build an operating
system and nothing of granule's -- granule's guest-side half goes on at
pack time -- so a tag keyed on the commit minted a new immutable rootfs
every time anything in this repository changed, none of which was the
rootfs. Keyed on the recipe instead, the tag means something to whoever
pins it: same key, same instructions; new key, somebody changed the OS.
Two consequences worth knowing:

- **A publish whose key is already on the registry pushes no rootfs at
  all**, moving tag included, rather than building over a tag somebody
  has pinned. The two builds would not be byte-identical -- the base
  images are pinned by digest, but the recipe names a suite rather than
  pinning its archive, so a `debootstrap` a month later picks up a month
  of Debian.
- **When that drift is the reason to republish** -- a point release, a
  security update, an unchanged recipe that now builds something
  different -- `ROOTFS_REVISION=<anything new>` mints a new key rather
  than overwriting the old one. `--force-rootfs` overwrites, and is for
  a tag pushed by mistake.

The packed guests keep their `:<commit>` tags: a packed guest
carries granule's own half and does move with every commit.

The variant names are the packed guests' own, with `.rootfs`: the
rootfs of `ghcr.io/gke-labs/granule/debian12` is
`ghcr.io/gke-labs/granule/debian12.rootfs`. To build one from this
tree instead -- because you are changing it, or want
`GUEST_SETUP_SCRIPT`/`GUEST_KERNEL_PACKAGE` applied to your own copy:

```sh
docker build --target guest-rootfs-image -t granule-guest-rootfs .
```

Either way a consumer derives from it with an ordinary Dockerfile, in
their own repository and their own CI, with no granule, no KVM and no
privileges beyond a `docker build`:

```dockerfile
FROM ghcr.io/gke-labs/granule/debian.rootfs
RUN apt-get update && apt-get install -y whatever
```

Two things make that worth having rather than an alias for
`guest-customized`:

- **Cost.** `disk.img` is one large file in a single layer, so a
  customized guest published as a disk is a full disk in the registry per
  version, pulled in full and retained in full. A rootfs image is
  ordinary Docker layers: a rebuild that changes one file pushes one small
  layer, and the disk becomes a *local* derived artifact, packed by
  whoever is about to boot it.
- **Who supplies what.** Packing late is what keeps the parts that must
  not come from a consumer out of the consumer's build. The guest-side
  agents and the overlay are laid over their rootfs at pack time, from
  the packer's own current build, so a guest published months ago boots
  with today's agent in it -- no rebuild on their side, and no code of
  theirs run to put it there.

The packing end of it is `granule guest pack -from <rootfs image> -t
<tag>`, which turns such an image into a bootable guest, and `granule
guest rebase -from <guest image> -t <tag>`, which makes an already-packed
one current -- a granule or cloud-hypervisor fix reaching a consumer's
image in seconds, without rebuilding it and without running any of it.
See [`docs/guest-images.md`](../../docs/guest-images.md) for both. The
front end -- validating that Dockerfile and supplying its base -- is
`granule guest build -f`, below.

The rootfs this publishes is whatever this build made, so a build that
was given `GUEST_SETUP_SCRIPT` or `GUEST_KERNEL_PACKAGE` publishes a base
with those already applied. The customary use is the plain one: build it
without either and let consumers add what they need as `RUN` lines they
can read back.

## The guest-side contract

Everything granule puts inside a guest and then depends on from outside
it: the agents and the vsock protocol they speak (`pkg/execwire`),
the units that start them, the helpers this overlay places
(`granule-growfs`, `granule-configure-dns` and the rest), and what
`pkg/guestexec` and `pkg/guestcp` assume about the session
those agents hand back. A guest built months ago under an older granule
may satisfy none of it, and the symptom is the characteristic one: a VM
that boots, looks healthy and never answers.

Overwriting the guest side is the mitigation, and it is what `guest
pack`, `granule wrap` and both complete forms of `guest rebase` do -- all
of them lay this overlay down, so all of them stamp the version they laid.
The version is the *backstop*, for the cheap rebase that deliberately
leaves the disk alone: it is the only thing that lets granule tell a guest
it may leave alone from one whose granule-side pieces predate something it
now requires. `guest verify` makes the same refusal, since the cheap
rebase it boots is `guest rebase`'s own.

The cheap rebase is not the only reader, though. A *boot* makes the same
refusal, for the same reason and at a higher stake: a granule about to
start cloud-hypervisor over somebody else's guest half checks what that
guest declares first, because a skewed control link is a VM the caller
cannot reach and a caller who was handed it believes otherwise. See
`pkg/guestimage/bootcontract.go` and docs/guest-images.md, "The
guest contract".

It is a single integer that counts up, with a floor each reader refuses
below (`Contract`, `MinimumContract` and `MinimumBootContract` in
`pkg/guestimage`), and it is written in three places because they
fail in different directions:

- `dev.granule.guest.contract`, a label on the packed image. Readable
  with one `docker image inspect`, which is what a cheap rebase can
  afford -- its whole argument is that it never opens `disk.img`. This
  is the copy a rebase's refusal is decided on.
- `/etc/granule/guest-contract`, inside the guest, placed by this
  overlay. It survives a repack by somebody else's tooling, a commit out
  of a running VM, and having the image's labels stripped or rewritten,
  none of which the label survives -- so it is the record of last
  resort, and what to read when an image's label and its disk disagree.
  It is also the authored one: the two below are copies of it.
- `guest-contract` beside the packed disk, which `pack-guest` lifts out
  of the rootfs it has just laid this overlay over. It is the only one a
  boot can read -- there is no image to inspect at boot, and the copy
  inside the disk is in an unmounted filesystem an untrusted consumer
  authored -- so it is what a boot's refusal is decided on. It has to
  travel with `disk.img`.

An image that declares nothing is every image published before granule
started writing a version down. That is reported, never refused, by
both readers: a backstop whose first act is to break every existing
guest is not one anybody can turn on. `granule guest rebase -force`
rebases below the floor anyway, for the caller who knows something the
label does not; it does not make the guest any newer, and says so.
`CHV_GUEST_CONTRACT_MIN` is the boot-side equivalent read the other way
-- it raises the floor, for a deployment that packs every guest it runs
and wants an undeclared one refused today.

Bumping the number is three edits held in step by
`pkg/guestimage`'s `TestContractIsDeclaredEverywhere`: the constant,
this overlay's `etc/granule/guest-contract`, and the label on the
top-level `Dockerfile`'s `final` stage (which is what the *bundled*
guest declares). The sidecar needs no edit: it is copied out of the
rootfs, so it cannot be forgotten and cannot disagree.

Moving a floor is the expensive one and is a deliberate act. Below
`MinimumContract`, every image published under it stops being rebasable
cheaply and its operator has to spend a repack or a boot. Below
`MinimumBootContract`, it stops booting at all -- which is why that one
sits lower, moves later, and needs the break it is preventing written
down beside it.

## Building a guest from a Dockerfile

`granule guest build` is the front end to the above: it validates a
Dockerfile against the subset below, supplies the base where the file
names none, runs an ordinary `docker build`, and packs what comes out
into a bootable image.

```sh
cat > Dockerfile <<'EOF'
RUN apt-get update && apt-get install -y --no-install-recommends nginx
COPY nginx-site.conf /etc/nginx/sites-available/default
RUN systemctl enable nginx
EOF

granule guest build -f Dockerfile -t my-guest:dev
docker run --rm --device /dev/kvm my-guest:dev                          # it boots

granule guest build -f Dockerfile -t my-guest:dev -print-dockerfile   # what docker is given
granule guest build -f Dockerfile -t my-rootfs:dev -rootfs-only       # stop before the packing
```

Or name the base in the file, which is the way to pin it:

```dockerfile
FROM ghcr.io/example/granule-guest-rootfs:2026-09
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
RUN apt-get update && apt-get install -y --no-install-recommends nginx
RUN systemctl enable nginx
EXPOSE 8080
```

And a file with nothing but a `FROM` in it repacks that image onto
today's granule, which is what a complete rebase is:

```sh
printf 'FROM ghcr.io/example/my-guest:v1-rootfs\n' |
  granule guest build -f /dev/stdin -t my-guest:v2
```

That names the *rootfs* image (`-rootfs-tag`'s, above) rather than a
packed one: a packed image's filesystem is granule's own half with the
guest sealed inside `disk.img`, and packing lays parts over a tree
rather than into a disk. The packing says so if you hand it one.

**Two artifacts, and the one to publish is the smaller one.** A `docker
build` produces a guest *rootfs* image -- ordinary layers, your packages
on granule's -- and what `docker run` boots is that rootfs packed into a
disk inside a granule image. The command does both, so `-t` names a
bootable image here exactly as it does on the `-setup` route, and the
rootfs is kept under a name of its own: `my-guest:dev-rootfs` above, or
whatever `-rootfs-tag` says.

Publish the rootfs. Packing lays granule's own guest-side pieces (the
agents, the overlay) over it out of the granule image being packed onto,
so a rootfs somebody pulls and packs next year boots with next year's
agents in it, with no rebuild on your side -- see "Deriving a guest from
a published image". A packed image is a local derived artifact and goes
stale the moment granule's guest half moves. `-rootfs-only` is the build
for a publisher: it stops at the rootfs, `-t` names that, and nothing is
packed. `-granule-image`, `-packer-image`, `-variant` and `-disk-extra-mb`
configure the packing and mean what they mean to `granule guest pack`.

A bare `granule guest build` means exactly that: no `-f` and no
`-setup` reads `./Dockerfile`. The booting route is asked for by name
(`-setup <script> -from <image>`), because it is the one with the
requirements -- a host with `/dev/kvm`, a boot and a shutdown per build
-- while this one runs on any machine and any CI runner that has docker,
and gets docker's layer cache across rebuilds for free. Where the two
overlap, which is most of what people actually do with them, the cheaper
machine should be the default and the expensive one should be explicit.

This is **not a Dockerfile interpreter**. Docker does the building and
the layer caching; granule decides what is valid and owns the packing,
and nothing in it parses beyond the instruction list
(`pkg/guestdockerfile`, and `pkg/guestpack` for the packing).

Two things it does not leave to the file:

- **The base, where the file names none.** Write a `FROM` and that is
  the base; write none and granule supplies one (`-from`, default
  `ghcr.io/gke-labs/granule/debian.rootfs:latest`, published from `main`
  as above, so the base is pulled rather than built; `-from` names a
  different *granule* rootfs image -- another variant, a pinned
  `:os-<key>` tag, your registry's mirror, or a
  `guest-rootfs-image` build of your own tree). Naming both a `FROM` and
  `-from` is an error rather than a silent precedence rule.

  What makes naming your own safe is the packing rather
  than a check: **the base supplies the filesystem payload, and granule's
  own half -- the agents, the control-net overlay, the units that start
  them -- is discarded and re-laid at pack time from the granule image
  being packed onto.** So a base that carried an old agent, or none,
  still produces a guest with the current one in it. The reason to name
  one is a *pinned* base: a granule-supplied base cannot be written down,
  so the same file silently builds on a different machine as granule
  moves.

  Only the last `FROM` is the base. Earlier ones are build stages and
  may name anything at all -- `FROM golang:1.23 AS builder` … `COPY
  --from=builder /out/app /usr/bin/app` -- since nothing of a stage
  reaches the guest but what a `COPY` takes out of it.

  Which bases actually *work* is decided where the filesystem is, which
  is the packing, and it says which of three kinds it was handed: a
  guest rootfs packs; a plain OS image (`debian:13` -- an OS that has
  never been through the customisation above) is refused; an image that
  is not an OS at all (`scratch`, `golang`) is refused. A build is not
  told at validation time, because a `FROM` is a string and nothing but
  the layers can answer the question.

  A file whose *only* instruction is a `FROM` of a guest **rootfs**
  image is not an empty build: it is a repack of that image onto today's
  granule, which is the same operation as `granule guest rebase
  -rootfs`. An already-*packed* image is the other thing, and `guest
  rebase` without `-rootfs` is the verb for it; the packing says so if
  you hand it one. See
  [`docs/guest-images.md`](../../docs/guest-images.md).
- **The instruction list.** The vocabulary is Docker's wherever Docker
  has a word for the thing, and what granule does not implement is
  refused loudly, naming the file, the line and the instruction. A
  format that quietly ignores half of what it was handed is worse than
  one that never claimed the resemblance -- a dropped instruction is a machine
  that comes up without the configuration the file specified, discovered in
  production rather than at build time.

| Instruction | |
|---|---|
| `RUN`, `COPY`, `ENV`, `WORKDIR`, `USER`, `ARG`, `LABEL` | Supported. They mean what they mean to Docker. |
| `ENTRYPOINT`, `CMD` | Specified, and refused here until something records them. `docs/granulefile.md` §7 gives them a native meaning in a packed stage -- the primary workload command inside the guest VM, without a custom service unit file, with trailing arguments replacing `CMD` at runtime -- and nothing in this tree carries them into a packed image yet, so this command refuses them rather than dropping them. `granule granulefile check` accepts them, because checking a file and packing one are different questions. |
| `ADD` of a local path | Supported: it is `COPY` with tar extraction. |
| `ADD` of a URL or a git remote | Refused. It fetches from outside the build context, with the network of whatever machine ran the build and nothing recording what came back. `COPY` it out of the context, or fetch it in a `RUN` where the checksum is yours to check. |
| `FROM` | Supported. The last one is the base; earlier ones are build stages and may name anything. Supplied by granule where the file names none, as above. |
| `EXPOSE` | Supported, and recorded rather than opened. It is this image's declaration of what it wants reachable, carried through the packing into the packed image's own OCI config -- and through a later `granule guest rebase`, since the declaration belongs to the machine rather than to the granule half being replaced. It opens nothing by itself, exactly as it opens nothing under `docker build`: a guest is shielded by default and stays shielded, because the image declares and something else grants. What grants it is whoever launches the image -- `granule vm create -publish-declared`, which publishes every declared port on the VM's own container the way `docker run -P` does, except that it binds to 127.0.0.1 unless `-publish-addr` names another address (and `docker run -P` itself, for an image run directly, which binds to every interface). See the top-level README's "Publishing a guest's ports". |
| `VOLUME` | Refused. A guest's filesystem is a packed disk the VM mounts as its root; nothing would act on the declaration at boot. |
| `HEALTHCHECK` | Refused. Docker health checks probe a container's main process, and this guest is not run as one. Health is a systemd unit's business inside, or `granule vm status`/`vm wait` outside. |
| `STOPSIGNAL` | Refused. A guest is stopped by an ACPI power button press (see "Graceful shutdown"), not by a signal to a process. |
| `SHELL` | Supported. It is build-time and entirely Docker's: `SHELL ["/bin/bash", "-euo", "pipefail", "-c"]` is worth writing, since without it a failure in the middle of a `RUN`'s pipeline is ignored. |
| `ONBUILD` | Refused. It defers instructions to whoever builds `FROM` the result, where none of them is validated and granule is not the one supplying the base. |
| `MAINTAINER` | Refused; Docker deprecated it. `LABEL org.opencontainers.image.authors="..."` is the supported spelling. |
| `# syntax=`, `# escape=`, `# check=` | Refused. A parser directive swaps out the frontend or the escape character, i.e. it changes what the file granule just validated means. granule also supplies the first lines of the file docker is handed, so a directive would be inert as well as unchecked. |
| `VM PACK`, `VM WRAP`, `VM SET`, `VM UNPACK`, `VM SNAPSHOT`, `VM FROM` | Refused as *unimplemented*, which is a different answer: `VM` is the one instruction the Granule language adds (`docs/granulefile.md`), and this command is not a Granule builder. The file it takes is already the container stage a `VM PACK` would convert -- the pack is the command rather than a line in the file. `granule granulefile check` validates a file that uses `VM`. |

A file this command takes is what the Granule specification's §7
calls a container stage above a `VM PACK`, and where the two agree the
reasons above are that document's. Where they differ, they differ on
purpose, because here granule is also the one supplying the pack: this
list records an `EXPOSE`, refuses a `MAINTAINER`, a remote `ADD` and (for
now) an `ENTRYPOINT` or `CMD` that a Granule's packed stage allows, and
reads a `FROM` as the base of a rootfs rather than as a header in a
language with two kinds of stage.
The lexing and the parser-directive refusals *are* shared --
`pkg/granulefile` is where they live and this command is a caller of
it -- so the two readings of a file can never disagree about what its
instructions are, only about which of them are allowed.

Everything below the header granule prepends is your own bytes, unchanged, so
`docker build`'s cache keys, its error messages and its own future
features keep working. The one seam: docker's line numbers are yours
plus the header granule prepends, and a build says how many on its way
past.

### What a container build cannot do, and what to do instead

This runs in `docker build` on the **build host's kernel**, as root but
with docker's default capabilities and no init running. The list of what
that rules out is short enough to state:

| Won't work here | Why, and where it belongs |
|---|---|
| `systemctl start`, `service x start` | No PID 1 to talk to. `systemctl enable` works -- it is symlinks in `/etc/systemd/system` -- and is what you want anyway: the service starts when the guest boots. |
| `modprobe`, anything reading `/sys/module` | The running kernel is the build host's, and its modules are not the guest's. |
| Anything needing `CAP_SYS_ADMIN`: loop devices, `mount`, bind mounts, overlayfs, mounting an image | An ordinary `docker build` does not have it (which is exactly why the guest rootfs is packed with `mke2fs -d` rather than a loop mount). |
| Running a container -- `docker run`, `podman`, a nested build | No daemon, no `CAP_SYS_ADMIN`, no nested cgroup. Warming an image cache means `docker save`/`COPY` of the tarball, or `-setup`. |
| Opening `/dev/kvm` or `/dev/net/tun` | Neither is in a build container. A guest that will *itself* run VMs still installs fine; it just cannot be tested here. |
| Making network configuration take effect -- `ip addr add`, `nft`, restarting a resolver | Writing the files works, and that is what matters for an image: the guest applies them when it boots. |

`granule guest build -setup <script> -from <image>` boots a real
machine and is the way to do any of the above during a build. It costs a
KVM host and a boot per build, which is why it is not the default; see
"Running a custom setup script" above for the three mechanisms compared.

### The one that does not announce itself

Everything above fails loudly. The failure that does not is **a step
that probes its environment and writes the answer down.**

`-march=native` in a `CFLAGS`, a worker pool sized to `nproc`, a JVM or
a database auto-tuning its heap to the RAM it sees, a config generated
from the host's CPU flags, a `configure` that detects a kernel feature
and compiles it in. Every one of those succeeds, and every one of them
bakes the *build host* into a guest that runs somewhere else -- on a
different CPU model, with a different core count, with `CHV_MEM_MB` of
memory rather than the builder's, on the guest kernel rather than the
builder's. No build error reports it. What you get is an illegal
instruction on one host and not another, a thread pool sized for a
32-core runner inside a 2-vCPU VM, or a daemon that reserves more heap
than the machine has and is killed at boot.

It is the same hazard any container build has, with one difference that
makes it sharper: a container at least runs on the kernel it was built
against most of the time, and a guest never does. So:

- Pin what you would otherwise detect. `-march=x86-64-v2` (or whatever
  floor you actually support), an explicit pool size, an explicit heap.
- Let the guest do the detecting at boot, not the build: a unit with
  `ExecStartPre=` that sizes things, `systemd`'s own `%` specifiers, or
  a config the guest generates on first boot.
- If a step genuinely has to observe the machine, it has to observe *the
  guest*: that is `-setup`, or `CHV_SETUP_SCRIPT` at first boot.

## Growing the filesystem

`CHV_DISK_SIZE_MB` (`granule vm create -disk-size-mb`; see the
top-level README's "Disk size") sizes the overlay before the VM
boots, which grows the *block device* the guest is offered and nothing
else. The ext4 inside it is still exactly what `mke2fs -d` made when the
image was built, so on its own that setting gives a guest an 8GiB
`/dev/vda` with the same few hundred MiB of free space it always had.

`granule-growfs` closes that on both variants: it runs `resize2fs` on the
root device in early boot, so free space in the guest reflects
`-disk-size-mb` without anyone logging in to do it by hand. Two things
make it a single command rather than a growpart-then-resize dance:

- this image puts ext4 **directly on `/dev/vda`, with no partition
  table** (the `guest-image` stage's `mke2fs` writes to the whole disk),
  so there is no partition whose end has to be moved first;
- ext4 grows **online**, with the filesystem mounted as root, so there is
  no initramfs hook and no unmount to arrange -- which also means a
  `GUEST_KERNEL_PACKAGE` guest needs nothing extra.

It runs on every boot and is a no-op on almost all of them: `resize2fs`
finds the filesystem already as long as the device and reports "Nothing
to do!". It also declines, successfully and with a line on the console
saying so, when the root is mounted read-only (`CHV_DISK_MODE=readonly`),
when the root filesystem isn't ext2/3/4, or when `resize2fs` isn't
installed -- and it treats a failed resize as a smaller disk rather than
a failed boot, since a guest with less free space than asked for is still
a guest you can log into and look at.

Ordering is what the service units add: `sysinit.target` on Debian
(`DefaultDependencies=no`, after `systemd-remount-fs.service`, the same
shape as systemd's own `systemd-growfs-root.service`) and the `boot`
runlevel on Alpine. Either way it is done before anything is writing
into the space it just made.

The Alpine rootfs installs `e2fsprogs-extra` for this, not just
`e2fsprogs`: Alpine splits `resize2fs` out of the base package. Debian's
`e2fsprogs` is unsplit and already there via `debootstrap`. Both rootfs
stages fail the build if `resize2fs` is missing, so a guest that would
silently never grow doesn't get built.

A guest that is *not* this one -- a `CHV_DISK_IMAGE` of your own, with a
partition table or a non-ext4 root -- has to do its own growing, exactly
as before.

## Graceful shutdown

`granule run`'s shutdown path (see the top-level README's "Shutdown"
section) presses the ACPI power button via the cloud-hypervisor API and
waits for the guest to power off on its own before forcing it. Reacting
to that button press needs *something* running in the guest to catch it
-- normally `systemd-logind` via `dbus`, but pulling those in just for a
power button is significant extra weight for a minimal image, so this
guest uses `acpid` instead (a few hundred KB, no `dbus` dependency) on
both variants. `overlay-common/etc/acpi/events/powerbtn` is the event
config acpid needs to match the button press (same config format on
both variants -- Debian's `acpid` and Alpine's busybox `acpid` applet are
config-compatible), pointing at `/etc/acpi/powerbtn.sh`:

- Debian: that script runs `systemctl poweroff`. `acpid.socket`/
  `acpid.path` are enabled automatically, as described in "Enabling
  services" above.
- Alpine: that script runs `poweroff` (busybox's applet). With busybox as
  PID 1 (`/sbin/init -> /bin/busybox`), `poweroff` doesn't call
  `reboot(2)` directly -- it signals init, which (per `/etc/inittab`'s
  `::shutdown:/sbin/openrc shutdown` line) runs the OpenRC `shutdown`
  runlevel, the same stop-services/unmount/sync sequence `systemctl
  poweroff` triggers on Debian. `acpid` itself is enabled by hand by the
  `guest-rootfs-alpine` stage, as described in "Enabling services" above
  (Alpine has no `deb-systemd-helper` equivalent that would do it
  automatically) -- it is the OS's own service, so the OS build is what
  turns it on; the handler script it runs is granule's, and goes on with
  the rest of the overlay at pack time.

## Networking

The guest itself doesn't run a DHCP client: it relies on the kernel's
`ip=` boot-time autoconfiguration (see the top-level README's
`CHV_CMDLINE` default and `deploy/k8s/pod-example.yaml`), same as every
other guest example in this repo, so no extra guest-side networking setup
is needed for a guest's own services to be reachable once a caller has
configured `CHV_CMDLINE` (or let `netshim` derive the `ip=` parameter for
it). This is also why neither variant enables an OpenRC/systemd
`networking` service. Note that `granule exec` needs none of this: it goes
over vsock, which is why a guest that gets this wrong is still one you
can get into and fix.

That is the *kernel's* built-in autoconfiguration, and it holds only for
a kernel with `CONFIG_IP_PNP` -- which the bundled `build-kernel` build
sets and Debian's stock `linux-image-amd64` does not. So a guest built
with `GUEST_KERNEL_PACKAGE` has nothing acting on `ip=` at all, and would
come up with no address and simply never be reachable.
`granule-net-cmdline.service` closes that: it reads `ip=` out of
`/proc/cmdline` and hands the spec to klibc's `ipconfig(8)`, which
implements the same syntax in userspace. It runs early, and exits
immediately when the interface is already addressed -- so on a guest
booting the bundled kernel it does nothing at all.

Both that unit and `granule-net-routes.service` get exactly one run per
boot, and nothing retries either: a guest they give up on has no address
and no default route for the rest of its life, while still answering
`granule exec` over vsock (`granule-agent` is ordered ahead of the network
on purpose), so the failure reads as a broken network rather than as a
failed boot. Four things follow from having only one chance, all of them
learned from a guest that took it and lost:

- **The scripts wait for their device.** `ipconfig` gives a NIC that has
  not been probed yet no time at all -- `SIOCGIFINDEX: No such device`,
  exit 1, within the millisecond -- and `granule-configure-routes` used to
  read the same absence as "no NIC to route over" and exit successfully.
  Both now wait for it (`GRANULE_NET_DEVICE_TIMEOUT`, 60s) and fail
  loudly if it never arrives. `granule-configure-net` uses a fast 25ms
  tight polling loop to detect device arrival without coarse delays or
  blocking sysinit on `udevadm settle`.
- **`granule-configure-net` judges itself on whether an address landed**,
  not on `ipconfig`'s exit status, and asks again if one did not
  (`GRANULE_NET_ATTEMPTS`, 3).
- **Both units are ordered `Before=sysinit.target`**, not just wanted by
  it. A unit merely wanted by a target it is not ordered before runs
  whenever the manager gets to it, which on a real guest has been *after*
  that target was reached. They also log to `journal+console`, so a guest
  with no network to be reached over still says what went wrong in
  `docker logs`/`kubectl logs`.

Both scripts are exercised from Go, against a fake `/sys/class/net`, a
fake `ip` and a fake `ipconfig` -- `pkg/netshim`'s
`guestnetscript_test.go` and `guestscript_test.go`, since a shell script
in an image is otherwise something only a booted guest on a cluster
tests.

`ip=` is also not the whole of what a container's interface carries. It
has one netmask and no route list, so a guest configured from it alone
believes its whole subnet is on-link -- true on a bridge CNI, and
exactly what a point-to-point one (kind's default, GKE's) arranges for
it not to be. `netshim` therefore passes the interface's actual routing
table down as a `granule.routes=` parameter of granule's own, and
`granule-net-routes.service` installs it. Both variants carry it, since
the kernel that acts on `ip=` has no equivalent for routes and never
will. See the top-level README's "Container networking".

Two details `ip=` depends on, both easy to lose:

- The NIC has to keep the name the `ip=` spec gives it. `netshim`
  hard-codes the guest's side as `eth0` (`pkg/netshim/guest.go`,
  whatever the sandbox's own interface is called), and udev's
  predictable naming would rename a virtio-net device to `ens2`; the
  `80-net-setup-link.rules` mask in the `guest-rootfs-debian` stage is
  what stops it, for this and for the control link's `eth1` alike.
- That mask has to reach the *initramfs*, whose own snapshot of
  `/etc/udev` is what runs first. It does because the mask is laid down
  by the rootfs stage and `GUEST_KERNEL_PACKAGE` is installed after it,
  so the initramfs its postinst generates already has it -- see the
  `guest-customized` stage. It is also why that one piece stayed in the
  OS build when the rest of granule's guest-side half moved to pack
  time: a mask written at pack time would be in the root filesystem and
  not in the initramfs, which is too late. A setup script that writes
  udev rules of its own has to run `update-initramfs -u` itself.


### The resolver

`debootstrap` copies the *build host's* `/etc/resolv.conf` into the
rootfs, and a build host's resolver is routinely an address that only
exists in its own network namespace -- docker's embedded `127.0.0.11`, a
cloud metadata service, a link-local stub. From a guest on a tap that is
simply unroutable, so an image built that way comes up with completely
open IP egress and no DNS at all: every name lookup hangs until it times
out, which reads as a blocked network rather than as a missing resolver.
It also makes the image unreproducible -- whichever machine built it
contributes its own unreachable resolver to every guest booted from it.

Two things settle it, deliberately at different levels:

- `GUEST_DNS` (a build arg, default `8.8.8.8`) is the baseline written
  into the image's own `/etc/resolv.conf`, so the image is the same
  wherever it was built. The rootfs stages delete the copy `debootstrap`
  made of the build host's, and the `guest-image` stage writes this one
  over the tree it is about to pack -- so setting it empty leaves the
  guest with no `resolv.conf` at all, rather than with the build host's.
  That also means the *published rootfs* carries none: a guest packed
  from it resolves through its `ip=` nameservers alone, below. A
  comma-separated value names more than one:
  `--build-arg GUEST_DNS=1.1.1.1,9.9.9.9` writes those two `nameserver`
  lines in that order.
- The nameservers on the guest's boot parameters are what a guest
  actually resolves through, and `granule-configure-dns` writes them over
  that baseline on every boot. `granule.dns=` carries the whole ordered
  list, in either family, at most three (glibc's and musl's `MAXNS`);
  `ip=`'s `dns0`/`dns1` carry its IPv4 part too, which is all an image
  older than `granule.dns=` reads. `granule.dns=` is left off when `ip=`
  already says everything -- IPv4 nameservers only -- so an IPv4 guest's
  command line is what it always was. They come from `NETSHIM_DNS` (granule's
  `-dns`), so a deployment names the resolver its own network has without
  rebuilding anything. Its default, `auto`, has the VM container read its
  own `/etc/resolv.conf` and use the nameservers there that a guest on a
  tap can route to -- only in the families the guest has an address in,
  neither loopback nor link-local (nor `::`), at most three and at most
  two of them IPv4, in the file's order -- together with that file's `search` domains and `ndots`, which travel
  as `granule.dns_search=` and `granule.dns_options=` and become the
  guest's `search` and `options` lines. That is a Kubernetes pod's cluster
  DNS and its `*.svc.cluster.local` search list, or a corporate or
  air-gapped network's own resolver. When nothing there qualifies --
  docker's embedded `127.0.0.11`, GCE's `169.254.169.254` -- the guest
  gets `8.8.8.8` and no search list, or `2001:4860:4860::8888` if it is
  IPv6 only and has no route to `8.8.8.8`; the VM container's log names
  which. An address named explicitly is used
  as given, with no search list, and a VM saved with `8.8.8.8` before
  `auto` existed keeps it. `-dns ''` leaves the baseline in place. A
  booting `guest build -setup` takes the same flag for the VM it
  provisions in, since that guest resolves names like any other.

Nothing else in a granule guest writes that file: there is no
systemd-resolved and no DHCP client, since the address is static on the
command line. Neither the kernel's own `CONFIG_IP_PNP` handling nor
klibc's `ipconfig(8)` writes `resolv.conf` either -- the kernel only
exposes what it was given in `/proc/net/pnp`, and `ipconfig` writes
`/run/net-<dev>.conf` and leaves the file to an initramfs script this
guest does not run. Hence a step of its own, applying to every guest
granule builds, bundled kernel or distro kernel, Debian or Alpine.
