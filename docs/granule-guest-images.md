# Customizing and running granule guest images

A sketch, and a request rather than a plan: everything here is a change to
[bwsalmon/granule](https://github.com/bwsalmon/granule), not to this
repository. `third_party/granule/VENDORED.md` is explicit about the
direction -- "grain is granule's primary consumer, so a change grain needs
belongs on granule's `main` and reaches here by a resync, not by being
applied to this copy" -- so this document says what grain needs a guest
image to be able to do, in enough detail to be executed in granule's own
repository and its own terms. `third_party/granule` is a byte-for-byte
snapshot and should stay one.

It was split out of [task lifecycle in a sandbox](task-lifecycle-sandbox.md),
which is the first consumer and not the only imaginable one: "a guest
image somebody other than the operator customized, run on a VMM half the
operator controls" is a shape grain will want again.

## What granule does today

A granule guest image is an OCI image carrying the VMM and the machine
together. What grain's own `Granulefile` produces with its `VM PACK` is an
ordinary OCI image -- granule, cloud-hypervisor and a bootable guest disk
-- so `docker run` on it boots a VM. That is why grain has one artifact
here rather than two: that image *is* the sandbox container a deployment
runs.

Inside it, the two halves sit in different places, and grain already
separates them:

```dockerfile
COPY --from=granule /usr/local/bin/cloud-hypervisor /usr/local/bin/cloud-hypervisor
COPY --from=granule /usr/local/bin/granule           /usr/local/bin/granule
COPY --from=granule /var/lib/granule/guest/          /var/lib/granule/guest/
```

- **The VMM half**: `cloud-hypervisor`, the `granule` binary, netshim, the
  entrypoint. Everything an isolation property rests on.
- **The guest half**: `/var/lib/granule/guest/` -- `disk.img`, `vmlinuz`,
  `initrd.img`. The machine that boots.

Disks are already layered. `internal/config` offers three modes, and the
default is `overlay`: "the guest writes into a thin qcow2 of its own,
backed by the disk image, which is only ever read. Boot costs no copy of
the image however large it is, several VMs on a host share it, and
discarding the overlay resets the guest." `PrepareOverlay` creates that
per-VM qcow2 at `/var/lib/granule/overlay/disk.qcow2`, backed by whatever
`Disks[0]` names, and repoints `Disks[0]` at it. `CHV_DISK_IMAGE` is what
names the bottom.

Customization is `granule guest build -from <image> -setup <script> -t
<tag>`, which boots the base, runs the script as root against a real
kernel and a real systemd, scrubs the per-boot identity a cloned image
must not carry, and commits. It runs in `DiskModePersistent` -- "the guest
writes through to the disk image itself. What `granule guest build`
needs, since the point there is for the changes to end up in the image
being committed."

So a chain exists, but only one link of it is durable: every build
collapses the whole disk into a new image. granule's own guest-image README
says what that costs -- "Each build adds a full copy of the disk as a
layer. `disk.img` is one large file in a read-only layer, so the guest's
first write copies all of it up, and `commit` captures that copy. Deriving
two or three deep is fine; a long chain wants squashing."

## What grain wants to do with it

Three things, each of which today's shape allows only partly.

**1. Assemble an image from two provenances.** Grain wants to run a guest
somebody else built -- specifically, one built by a repository's own CI,
from a declaration in that repository -- while keeping the VMM half its
own. The point is that the isolation boundary must not come from the
customized artifact: `/usr/bin/granule` and cloud-hypervisor stay the
deployment's, so the VM is real rather than claimed. The assembly is an
ordinary `COPY --from`, with no `RUN`, so it runs no code from the image
being consumed.

This half mostly works already, because the seam is already clear.
What it needs from granule is that the
guest half be *addressable* as a fixed, documented set of files rather
than as "whatever is in that directory".

**2. Stack customizations instead of copying the disk.** A repository's
lifecycle image is rebuilt whenever its reducer changes, which for an
active repository is often. Paying a full disk copy per version -- in the
registry, in every pull, and in local retention for every version any live
instance still pins -- is the dominant cost of the whole design.

What grain wants is for a build to emit *one more qcow2 layer* holding its
diff, and for a VM to boot the chain.

**3. Run a VMM half newer than its guest half.** This is the one that
breaks an invariant granule currently enjoys, and it should be broken
deliberately and with its eyes open. VENDORED.md states it: "`granule`
is built from this tree and talks to a guest built from this tree, and the
two agree on the guest-side contract (`granule-authorized-key`, the
control-net overlay, the mem-agent, the disk modes) because they are the
same files -- rather than because someone remembered to move a pinned tag
and a vendored SHA together."

Grain's upgrade moves the VMM half for every deployment at once. A guest
built by a repository six weeks ago does not move with it. That is
*desirable* -- it means a granule or cloud-hypervisor fix reaches every
customized guest without anyone rebuilding -- and it means the contract
those two ends share stops being guaranteed by construction and has to be
guaranteed by a version.

## What granule already has

Checked against `gke-labs/granule` `main` rather than assumed, because two
things this document asks for are already there:

- **The guest half is already addressable.** `pkg/guestimage` exists and
  is exactly what "What grain wants to do with it" item 1 asks for -- its
  own doc comment: "names the guest half of a granule OCI image: the fixed
  set of files that make up a bootable machine, as opposed to the
  machinery that boots it", with the two halves and their differing
  provenance spelled out. Nothing further is needed for assembly by `COPY
  --from`.
- **`flatten` is implemented**, as `FlattenChain`/`Flatten` in
  `pkg/qcow2` with a `granule guest flatten` CLI entry, described in
  granule's own `docs/qcow2-storage-design.md` section 3.4.
- **Size-based sealing is implemented too**, as `AddLayer`'s 64MB
  threshold (that doc's section 3.3): consecutive builds extend the top
  layer in place rather than adding one, explicitly to keep chains
  shallow. The depth ceiling above is the number that heuristic was
  written against without naming.

So of the five verbs, `flatten` exists, `build` has a CLI surface and
`pkg/guestimage` beneath it, and `pack`, `rebase` and `verify` are the
ones with CLI mentions but no implementation behind them in the sense
this document means.

That leaves the remaining work smaller and differently shaped than a
reading of "The operations" alone suggests: the chain itself, the
`backing_files=on` path validation that has to come with it, and a depth
check that refuses an eleventh layer at write time rather than letting it
become an image that cannot boot.

## The operations

Everything below resolves into five verbs, and naming them is most of the
design. Three make an image, one judges one, one repairs one:

| | |
| --- | --- |
| **rebase** | Take an existing guest image and replace its granule half with this deployment's trusted one. The guest stays; the machinery around it becomes current. |
| **pack** | Take an OCI image and pack its filesystem into a granule guest disk, adding the trusted overlay on the way. Turns something docker built into something granule boots. |
| **build** | Take a Dockerfile and run it in an actual VM, instruction by instruction, sealing layers as it goes. |
| **verify** | Boot an image, run checks against it, produce no artifact. A verdict, not an image. |
| **flatten** | Collapse a chain into one disk and reclaim what is no longer reachable. Maintenance on an image rather than a way of making one. |

They are not alternatives; they sit at different points and compose.

**Who invokes which.** Grain is a `pack` and `rebase` consumer: a
repository publishes an ordinary OCI image built by ordinary `docker
build`, grain packs it on pull and rebases it on every upgrade. `build` is
for the consumer whose image genuinely needs a running machine to
produce, and it runs in that repository's own CI, never in the daemon.
That division is what keeps repository-authored instructions off the host
that runs the daemon: `pack` and `rebase` execute nothing from the image
they operate on, and `build` executes plenty but does it in a sandbox.

**`build` generalizes what `guest build -setup` already is.** Today's
`granule guest build -from X -setup s -t Y` boots X, runs `s`, scrubs
and commits. That is exactly `build` with a one-instruction Dockerfile
(`FROM X` / `RUN <s>`), so this is a generalization with a migration path
rather than a new verb competing with an old one.

**`pack` and `rebase` are the same intent at different stages** -- make
the trusted parts current -- which is worth noticing because it says when
each applies. `pack` gets it for free: it is building the disk, so the
overlay and agents it lays in are whatever the packer currently has.
`rebase` is that same refresh applied to an image already packed, by
somebody else, some time ago.

### rebase has a cheap form and a complete one

Which matters, because the decision above -- overwrite the guest agent on
every rebase -- does not fit in the cheap one.

The seam granule's own artifact already has is between the container
filesystem (the `granule` binary, cloud-hypervisor, the entrypoint) and the
guest data (`disk.img`, kernel, initramfs). Swapping the first and keeping
the second is a `COPY --from` with no `RUN`: seconds, no execution, works
on any guest image whatever produced it. Call that the cheap form, and it
is what makes a granule or cloud-hypervisor fix reach every consumer's
image without a rebuild.

But the *guest agent* is inside `disk.img`, not beside it. Refreshing that
means writing into an ext4 inside a qcow2, which needs one of:

- **the rootfs**, if the image came from `pack` and grain still holds the
  OCI image it was packed from -- then a rebase is just a repack, and the
  agent goes in as a file copy before `mke2fs -d`. Fast, and the case
  grain is in.
- **a boot**, otherwise: start the guest, replace the binary, scrub,
  power off, seal. That is `build` with a one-instruction Dockerfile, so
  the complete form of `rebase` is implementable on `build`'s machinery
  rather than needing its own.
- **an ext4 writer**, which is the option to avoid: much larger than the
  qcow2 writer granule has, for a job the other two already do.

So: cheap rebase for the VMM half, on anything; complete rebase via
repack where the rootfs is held and via a one-step build where it is not.

### verify

Boot an image, run checks, emit a verdict and no artifact. That last part
is what makes it a verb of its own rather than a flag on `build`: nothing
it does can change what is being judged.

Three things decide whether it is worth having rather than being a script
somebody runs:

- **It must boot the image as it will actually run**, which means *after*
  `rebase`, with this deployment's own VMM half under it. Verifying the
  published artifact proves something about an image nobody boots. This is
  also the first place a guest-contract version mismatch shows up as a
  sentence rather than as a guest that never answers.
- **Its result is keyed and cached**, on the image digest and the check
  set, or a consumer that verifies before use ends up verifying on every
  sandbox start.
- **A failure blocks rather than warns**, the rule this design uses
  everywhere else: an image that does not pass does not go into service,
  and nothing silently falls back to one that would have.

grain's own `systemctl enable docker && systemctl start docker && docker
info` is the worked example, and the reason this verb exists: it is the
single thing in a 747-line setup script that needs a booted machine, it
changes nothing in the image, and having nowhere to put it is what kept it
inside a build.

### flatten

Collapse a chain into one disk, and reclaim what is no longer reachable.
Two jobs that are separable and almost always wanted together, which is
the argument for one verb: depth and size are different problems, and one
pass over the data fixes both more cheaply than two.

The reason it is worth stating as an operation rather than an internal
step of `build`: an image can arrive needing it. A consumer who published
a deep chain, or a chain built against a smaller depth ceiling than this
deployment enforces, has an image that needs repairing before it will
boot, and repairing it is not rebuilding it.

One thing falls out pleasantly. The zero-cluster trap above -- that
deallocating a zeroed cluster in an overlay reverts it to whatever the
backing file holds -- is a property of *overlays*. `flatten` removes the
backing file, so in a flattened image deallocating an all-zero cluster is
exactly correct: there is nothing underneath for it to fall through to.
Flatten is therefore the one place the aggressive reclaim is legal, which
is a good reason for the aggressive reclaim to live there.

It should be content-deterministic, so that flattening the same chain
twice produces the same bytes and a registry can still dedupe the result.

## What that asks of granule

### For `build`: a named, ordered layer chain

**Decided: qcow2, and this is the chain.** The alternatives were
considered and are not available: a root served over virtio-fs is
rejected on security, and a disk segmented into numbered extents has no
supported implementation and carries the same path-containment hazard one
layer down. So where a build stacks -- which is where it boots, per the
artifact rule below -- it stacks qcow2 overlays, exactly as
`PrepareOverlay` already does for one level.

Two things come with that decision rather than after it:

- **The convention below is a security control**, not only a resolution
  mechanism. Stacking means running with `backing_files=on`, which is a
  CVE mitigation turned off on purpose, so the containment it replaces
  has to be granule's: every layer's backing path validated against the
  fixed convention before boot, on every layer rather than the top one.
  *Why `backing_files` defaults to off* below has the whole argument.
- **A build that does not boot does not stack at all**, and keeps the
  mitigation on: it produces a rootfs image, and `pack` turns that into
  one raw `disk.img` with `image_type=raw` and no backing file anywhere.
  That remains the path grain's own lifecycle images take, so the cheap
  path and the safe one are the same path.

A convention for where a customization layer lives and what it is called,
so that a file copied out of one image and into another still resolves.
Something like:

```text
/var/lib/granule/guest/disk.img          the base, raw, read-only
/var/lib/granule/guest/layer.01.qcow2    backed by disk.img
/var/lib/granule/guest/layer.02.qcow2    backed by layer.01.qcow2
...
/var/lib/granule/overlay/disk.qcow2      the per-VM overlay, as today
```

Two existing properties make the naming load-bearing rather than
cosmetic, and any convention has to respect both:

- **A qcow2 records its backing file as a path, and that path is resolved
  inside the container.** `PrepareOverlay`'s own comment: "The backing
  file is recorded as the path Disks[0] already holds, which is a path
  inside this container -- the same place cloud-hypervisor will open it
  from, since it runs here too." Layers that move between images with
  their backing pointers intact only work if the convention puts them at
  the same absolute path in both.
- **The `.qcow2` suffix is functional.** config.go: "The .qcow2 suffix is
  load-bearing: it is what hypervisor.Args uses to decide image_type and
  backing_files."

Three constraints come out of granule's own qcow2 writer rather than from
anything grain wants, and they bound what a chain can be:

- **`WriteOverlay` pins the backing format to `raw`.** `qcow2BackingFormat
  = "raw"`, with the comment that "Every overlay WriteOverlay creates is
  backed by a raw source disk image". A layer backed by another *layer*
  is backed by a qcow2, so the writer needs the backing format as a
  parameter. Small and specific, and the first thing a chain needs.
- **cloud-hypervisor enforces a backing-chain depth.** The same comment
  records tripping its "Maximum disk nesting depth exceeded" guard --
  spuriously, from an unpinned format being mis-probed, but the guard is
  real. Flattening is therefore not only a performance question: past some
  depth a chain does not boot. What that depth actually is should be
  measured before any policy is written against it.
- **A qcow2 records its backing file's path and virtual size, not its
  content.** So a base that changes underneath a layer is not an error --
  it is a silently wrong filesystem, where unchanged blocks read from
  content the layer never saw. This is a real tension with wanting the
  deployment to move the base: layers are only valid against the base
  they were built on, so the base has to be pinned per chain and retained
  while any chain needs it. One base per granule version, shared across
  consumers, rather than one per consumer.

Whether granule discovers the chain itself (glob the convention, sort,
take the highest as `Disks[0]`) or leaves the caller to point
`CHV_DISK_IMAGE` at the top is a real choice. Discovery is friendlier and
makes the convention granule's rather than each caller's; an explicit
pointer keeps granule's config surface as literal as it is now. Grain can
work with either, and mildly prefers discovery, because the alternative
means every consumer reimplements "sort these filenames correctly".

Either way it would be good for granule to **validate the chain before
boot** -- walk it, check each backing file exists and is the size its
child expects -- so a broken assembly fails with something that names the
missing link, rather than as a guest that does not come up.

### `build`, `pack`, and what each one costs

The image-building story above was hand-waved across two documents --
"publish a guest image" and "somehow that costs less than a full disk" --
and this is the explicit version. It also substantially simplifies what
comes before it; see *What this supersedes*.

**granule's rootfs is already an OCI image, and a Dockerfile is already
the language for transforming one.** The `guest-customized` stage
"promotes the rootfs to an image of its own (`FROM scratch` +
`COPY --from=guest-rootfs /rootfs/ /`) and runs the script as an ordinary
`RUN`", and `guest-image` packs whatever that holds with `mke2fs -d`. A
consumer's Dockerfile, with that rootfs image as its base, *is* a guest
customization already -- it just has nowhere to say so.

So the ask is small, and it is not "write a Dockerfile interpreter":

1. **Publish the rootfs as an image in its own right**, beside the guest
   image. Today it is an intermediate build stage; as a tagged artifact
   it becomes something a consumer can write `FROM` against.
2. **A thin build front end** -- `granule guest build -f Dockerfile` --
   that validates the subset below, supplies the base rather than
   trusting the consumer to name it, drives an ordinary `docker build`,
   and packs the result with `mke2fs -d` plus the current overlay.

docker does the building and the layer caching; granule decides what is
valid and owns the packing. Nothing needs a Dockerfile parser beyond
enough to check the instruction list.

#### The subset

This is `pkg/ci`'s own argument in a different format, and it should be
made the same way: the vocabulary is Docker's wherever Docker has a word
for the thing, and what is not implemented is *rejected*, loudly, naming
the file and the instruction. A format that quietly ignores half of what
it was handed is worse than one that never claimed the resemblance.

| Instruction | |
| --- | --- |
| `RUN`, `COPY`, `ENV`, `WORKDIR`, `USER`, `ARG`, `LABEL` | Supported. They mean what they mean. |
| `FROM` | Supplied by granule, not by the file. A consumer naming its own base is naming one with no control-net overlay and therefore no route in (see *What grain wants*), so this is refused rather than honoured. |
| `ADD` with a URL | Refused. `ADD` of a local path is `COPY`. |
| `ENTRYPOINT`, `CMD` | Refused, and this is the interesting one -- see below. |
| `EXPOSE`, `VOLUME`, `HEALTHCHECK`, `STOPSIGNAL`, `SHELL`, `ONBUILD` | Refused. None of them has a meaning for a machine that boots systemd. |

`ENTRYPOINT` and `CMD` are refused because a guest is not a container: it
boots an init and runs services, and there is no single process whose exit
is the machine's exit. What a consumer means by `CMD` is a service, and
the way to say that is a systemd unit `COPY`d in and `systemctl enable`d
-- which works on this path, where `systemctl start` does not
(the guest-image README is explicit: "`systemctl enable` works but
`systemctl start` does not"). For grain's own consumer that is already
true: the lifecycle declaration carries `serve.run` on grain's side,
which is the right place for it -- a deployment decides how a definition
is served, and the image only has to contain it.

#### What actually fails on the container path

Worth enumerating rather than gesturing at, because the list turns out to
be short and the cheap path turns out to cover almost everything.

The container path runs a `RUN` on the build host's kernel, as root but
with docker's default capability set -- no `CAP_SYS_ADMIN`, no
`CAP_SYS_MODULE`, no `CAP_NET_ADMIN` -- with a real `/proc`, docker's
restricted `/dev`, working network, the rootfs as `/`, and no init
running. What that rules out:

| | |
| --- | --- |
| **Starting a service** | `systemctl start`. There is no systemd running to ask. `systemctl enable` works, because it is symlinks. |
| **Loading a kernel module** | `modprobe`, `insmod`. No capability for it, and the module would be for the build host's kernel anyway. |
| **Mounting anything** | Needs `CAP_SYS_ADMIN`: loop devices, bind mounts, overlayfs, mounting an image to inspect it. Note that `mke2fs` *on a file* is fine; it is mounting that is not. |
| **Running a container** | dockerd, docker-in-docker, pre-pulling images into a daemon. |
| **Opening a device** | `/dev/kvm`, `/dev/fuse`, `/dev/net/tun`, raw block devices. |
| **Applying network config** | `ip link`, nftables, iptables. Writing their config files works; making them take effect does not. |
| **udev rules taking effect** | udevd is not running, so nothing is autoloaded or renamed. |

And the one that does not announce itself: **a step that probes its
environment and writes the answer down.** `-march=native`, a thread pool
sized to `nproc`, a daemon auto-tuning to available RAM at install time, a
config generated from `uname -r`, cgroup-version detection. Those read the
build host and bake it into a guest that will run somewhere else, and no
build error reports it.

Things people expect to fail here and which do not, because they matter
for judging the size of the list: `apt-get install` (and postinsts, which
generally no-op without an init -- see below), `systemctl enable`,
`initdb`, starting a server *directly* with `pg_ctl` or the binary itself
to create a role or a schema, `mke2fs` on a file, adding users and groups,
`setcap`, and compiling anything.

**Grain's own guest image is the useful measurement here**, because it is
the most demanding consumer either project has. Before it became the
`Granulefile`, it was provisioned by `scripts/granule/guest-setup.sh`: 747
lines that installed a Go toolchain, Node, three agent CLIs, kubectl,
granule, accounts, ssh keys and docker -- and that needed a booted machine
for exactly one thing:

```sh
systemctl enable docker
systemctl start docker
docker info >/dev/null
```

whose own comment says what it is for: "Asserted rather than assumed: this
is the whole reason the guest boots a distro kernel instead of granule's
own... A dockerd that cannot start is a guest that looks fine until the
first dispatched task tries to run anything."

That is a **verification**, not a provisioning step. It changes nothing in
the image; it proves something about it. The `enable` half works on the
container path and only the `start` and the `docker info` do not. The rest
of that file -- every package, every account, every file -- is container
path work. It even declines to pre-pull kind's node image, which is the
one place it could have needed a running dockerd to *build* something.

The script's header also recorded a hazard that runs the other way, and is
worth weighing before treating booted as strictly better: "A package whose
postinst *starts* a service starts it here for real, with whatever
configuration is on disk at that moment -- see the docker block below,
which writes /etc/docker/daemon.json before installing docker precisely
because dockerd would otherwise come up on the default bridge subnet and
stay there." The container path does not have that problem, because
nothing comes up.

#### So: rootfs by default, and booting is a phase rather than a step

Given a list that short, the earlier proposal here -- boot steps declared
inline, interleaved with container steps, with an ordering rule to stop
them being reordered -- is more machinery than the problem needs. Simpler,
and recommended:

- **A build is container-path.** Full stop. `RUN`, `COPY`, `ENV` and the
  rest, built and cached by docker, no KVM, and the artifact is a rootfs
  image with ordinary layer dedup.
- **Booting is a separate phase with a separate purpose**: `granule
  guest build -setup`, as it exists today, for a consumer that genuinely
  needs a running machine -- and, more often, for *verifying* a built
  image rather than building one. Grain's own `docker info` assertion is
  that, and belongs after the build rather than inside it.

What this gives up is the case where a build step must both run on a real
kernel *and* have its output land in the published image. Nothing either
project does today is in that case, and if one appears, the declared-boot-
step design above is written down and can be revived -- along with its
"once booted, stay booted" rule, which is what stops it being silently
reordered.

What it keeps is the property worth most: a consumer can tell what their
build will do by reading their own Dockerfile, and there is one artifact
shape rather than two.

The residual risk is the environment-probing class, which no rule catches
and which booting would have fixed. It is worth saying plainly rather than
designing around: a guest whose build auto-tunes to the build host is
wrong, the wrongness will not be reported, and the mitigation is that the
kinds of software that do this at *install* time are rarer than the kinds
that do it at *start* time -- a JVM reads memory when it starts, not when
it is unpacked.

#### The other model: always boot

Worth working through rather than assuming, because it is the more
*correct* model and the question is only what it costs.

**The shape.** Each instruction runs inside a booted guest, and the disk
after it is a layer. What makes this fit is that it is already Docker's
own semantics: a `RUN` is a fresh process against a filesystem that
persists, which is exactly what "reboot between instructions, keep the
disk" means. Nothing has to be redefined to make a Dockerfile mean
something here.

**The snapshot is cheaper than it sounds, because a clean shutdown is
the boundary.** The instinct is to snapshot a live VM, which means
`sync`, `fsfreeze` and a consistency argument. None of that is needed if
the VM powers off between instructions: a clean shutdown flushes and
unmounts, so the overlay left behind is already consistent. The per-step
loop is then:

```text
boot with (base + layers 1..N-1) and a fresh overlay
  run instruction N inside the guest
  scrub per-boot identity
  poweroff
seal the overlay as layer N
```

granule needs no new primitive for the sealing -- the overlay is a file
`PrepareOverlay` already created. And where a build does want to keep a
running guest between steps, the primitive for *that* also exists:
`granule run` already "pauses the VM, snapshots its full state (memory
included)... via the cloud-hypervisor API's `vm.pause`/`vm.snapshot`, and
resumes it", and already copies the disk overlay into the snapshot while
paused, because otherwise "the guest resumes onto a filesystem that has
moved under it".

**What it buys.** The whole failure table above stops existing.
`systemctl start`, `modprobe`, `mount`, a loop device, dockerd, `/dev/kvm`,
`ip link`, udev rules -- all ordinary. And the class that cannot be fixed
any other way goes with them: a step that probes its environment now
probes *the guest*, so `-march=native`, a pool sized to `nproc` and a
daemon tuning itself to available memory all bake in the right answer
rather than the build host's. grain's own `docker info` assertion becomes
an ordinary instruction instead of a reason the whole build path exists.

One build path, one artifact shape, and no table of what does and does not
work for a consumer to read before writing a Dockerfile.

**What it costs.**

- **KVM for every build, everywhere.** This is the big one and it is not
  an engineering cost that goes away. It is also the thing
  `.grain/workflows/tests.yml` already measured going wrong under host
  contention.
- **A boot and a shutdown per instruction.** Seconds each against a
  Debian guest with systemd, so tens of seconds to a couple of minutes
  added to a cold ten-instruction build -- and one boot on a warm one
  that changed only its last step, which is the case that matters.
- **The scrub moves to every layer.** Per-boot identity -- machine-id,
  host keys, the random seed, DHCP leases, `/var/log` -- is written on
  every boot, so it has to be removed before every seal rather than once
  at the end. Mechanical, and `guestbuild` already has the code.
- **A layer store and a cache key.** Keyed like Docker's, on the parent
  layer and the instruction text (and the content, for `COPY`). This is
  the real new machinery, and it is what the whole model is for: without
  caching, every build is every instruction.
- **The postinst hazard comes back**, the one the old guest-setup.sh's
  header recorded: a package whose postinst starts a service starts it for real,
  with whatever configuration is on disk at that moment.

**Sealing is a decision per step, not a consequence of one.** An
instruction does not have to end a layer. After each one the build can
either keep the same qcow2 as its writable top -- so the next instruction
accumulates into it -- or seal it and start a fresh overlay backed by it.
The rule that decides is size:

- **Below a threshold, keep writing into the same file.** A step that
  edits a config file or drops in a script has no business becoming a
  layer of its own. Nothing is sealed, nothing is copied, and the chain
  does not grow.
- **Above it, seal and start a new overlay.** The file stops being
  written, becomes immutable and shared, and the chain grows by one.

The cost model behind the threshold is what makes it the right rule
rather than an arbitrary one. Sealing buys immutability -- a layer that
can be cached, shared between builds and deduped by a registry -- and the
price of *not* sealing is that caching the file means copying it, because
it is still being written. A small file is cheap to copy, so immutability
is not worth a chain slot. A large one is expensive to copy, which is
exactly when it is worth sealing so it never has to be copied again. Pay
for immutability when the thing is big enough to earn it back.

What it does to the cache is coarsen it, which is the honest trade: if
instructions 5 through 12 all landed in one qcow2, changing instruction 8
re-runs all eight. That is the same property a Dockerfile already has
when its author writes `RUN a && b && c`, except decided by measurement
rather than by hand. The cache key follows: a sealed layer is keyed on its
parent and on the *list* of instructions folded into it, not on one.

One mechanical note before the next section: rebooting between
instructions and sealing after them are independent. The reboot is what
makes a `RUN` a fresh process, and it happens every step whether or not a
layer ends there.

#### Thinning a layer, and the trap in it

A qcow2 does not shrink on its own. A step that writes 500MB and a later
one that deletes it leaves the clusters allocated, so a size-triggered
seal measures what was *allocated* rather than what is useful, and a
delete-heavy build seals early for nothing.

This is solvable with ordinary effort -- zero the guest's free blocks,
then reclaim the zeroed clusters -- and the second half has a trap in it
that is worth naming before anyone writes the obvious loop.

**Do not deallocate a zeroed cluster in an overlay.** In qcow2 those are
two different statements:

- *Cluster not allocated* means "read this range from the backing file".
  granule's own writer relies on it: "a zero L1 entry means no L2 table
  allocated for this range", which is what makes a fresh overlay empty.
- *Cluster allocated and full of zeros* means "this range is zeros",
  overriding whatever the backing file has there.

Removing the second by turning it into the first is correct in a base
image, where unallocated reads as zero because there is nothing
underneath. In a layer it is corruption: a file deleted in this layer and
present in the one below reverts to the old content, silently, and only
for the ranges that happened to be zero.

The transformation that *is* correct is to mark the cluster zero without
allocating data for it -- qcow2's zero-cluster flag on the L2 entry. Space
reclaimed, semantics preserved. granule's writer has no concept of that
flag today (it only ever produces empty overlays), so it is something to
add rather than something to reuse.

Cheaply, that also means zeroing free space is affordable even where it
shadows a lower layer's blocks: a zero-flagged cluster costs an L2 entry
and no data, so covering a whole 30GB disk costs single-digit megabytes of
L2 tables.

**Check discard first, though.** If the pinned cloud-hypervisor honours a
discard option on `--disk`, then `fstrim` in the guest reclaims
continuously and cheaply, with no zeroing pass and no write amplification,
and the elbow grease is unnecessary. granule passes no such option today.
Its own `--disk` argument building is where to look, and its comment on
`backing_files=on` records that flag as "undocumented beyond `--disk`'s
own `--help` listing it" -- which is how the last one was found and is
probably how this one is.

**Where the zeroing runs, if it is needed.** The build already powers the
guest off between instructions, so the natural place is guest-side just
before that: remount the root read-only and run `zerofree`, which needs
nothing on the host that understands ext4. Doing it host-side after
poweroff means either an ext4 reader of granule's own or `qemu-nbd`, which
wants root and the nbd module.

**And measure the threshold on the guest's view, not the file's.** If
sealing is triggered by how much the qcow2 has allocated, a step that
writes and deletes trips it for nothing. The guest knows what it actually
used -- a `df` delta across the instruction -- so decide with that, and
run the thinning pass once at seal time rather than after every step.

**One thing thinning does not fix**, and it should be said in a document
about images built from a repository's own Dockerfile and pushed to a
registry: a layer chain carries deleted data in its lower layers, exactly
as Docker's does. A secret written by an early instruction and removed by
a later one is still in the published artifact. Zeroing the upper layer
shadows it for anyone who boots the image and does nothing about anyone
who reads the layers.

**The unknown, which this demotes.** The chain still grows, and
cloud-hypervisor's backing-chain budget is not known at depth greater
than one. What granule measured was that `backing_files=on`
is required before a chain is followed at all -- without it "having a
backing file at all exceeds the default nesting budget whatever the
image_type says" -- and everything it has run since is a single overlay
over a raw base. Whether ten deep boots, and what the ceiling is, is
untested.

It still wants measuring, but with size-based sealing it is a parameter
rather than a gate: depth is now a function of how much a build *writes*,
not of how many instructions it has, so a twenty-step Dockerfile whose
steps are mostly small config edits produces two or three layers rather
than twenty.

The measurement should then be turned into an invariant rather than a
hope. Make the depth ceiling a bound the builder enforces -- as it
approaches it, stop sealing and accumulate regardless of size -- so a
build that writes more than anyone expected produces a fatter top layer
instead of an image that will not boot. The size threshold is the
heuristic that normally keeps a build well clear; the ceiling is the rule
that holds when it does not.

**Where this leaves the two.** The container path is nearly free -- publish
the rootfs image, add a front end that validates and packs -- and covers
everything either project does today except one assertion. The always-boot
path is more correct, costs KVM and a layer store, and removes a class of
silent wrongness that the container path can only document.

They are not mutually exclusive, and the sequencing that seems right is to
build the cheap one first and treat always-boot as the version that
supersedes it, *provided* the depth measurement comes back well. The thing
not to do is design the container path in a way that makes the other one
harder later: keeping the Dockerfile as the input format, the subset as
the contract, and the artifact as a rootfs image are all shared, so that
sequencing costs a front end rather than a rewrite.

#### The two artifacts

**A packed guest image**, as `guest build` produces today: disk packed,
kernel and VMM alongside, one artifact that `docker run` boots. Familiar,
one thing to publish and pull, and what "build granule images" most
naturally means. Its cost is that `disk.img` is a single large file in a
single layer, so every published version is a full disk in the registry
whatever changed -- which is exactly the cost the layer chain above was
invented to avoid, still unavoided. This is what the boot phase produces,
and what a consumer gets who needs one.

**A rootfs image**: the filesystem alone, as an OCI image, packed into a
disk later by whoever runs it. Ordinary Docker layers, so the registry
dedupes them and a rebuild that changes one source file pushes one small
layer. The disk becomes a *local* derived artifact, packed on pull and
repacked on upgrade -- never published, never pulled, and never something
a retention policy has to reason about. This is what an ordinary
Dockerfile build produces, and it can be packed into a guest image as well
where a consumer wants both.

Grain's lifecycle use wants the rootfs one, for the dedup and for a reason
worth more than that: packing late is what lets this deployment's own
overlay and agent binaries go on at pack time, on every upgrade, from an
image it already holds. A consumer who takes the boot phase gets the
packed guest, pays the full disk per version, and the layer-chain section
above becomes relevant to them again.

#### What this supersedes

Worth being explicit, because it reverses two earlier decisions in this
document rather than adding to them. Both follow from the rootfs output,
so both hold for an ordinary Dockerfile build and neither holds for a
consumer who takes the boot phase.

**The qcow layer chain becomes unnecessary for this purpose**, because
its whole job was to avoid the full disk copy per published version that
a rootfs artifact does not have.

Three problems go with it: the backing-format parameter `WriteOverlay`
would have needed, cloud-hypervisor's backing-chain depth guard, and the
one that would have hurt -- a qcow2 recording its backing file's path and
size but not its content, so that a base moving under a layer is a
silently wrong filesystem rather than an error. Repacking has no such
failure mode: the inputs are a rootfs image and an overlay, and the output
is a whole filesystem built from them.

**And the guest agent overwrite becomes trivial.** The decision above --
overwrite the agent binary during the rebase -- needed either an ext4
writer or pack-time access. Packing *is* pack time: the overlay and the
agents are copied over the consumer's rootfs before `mke2fs -d` runs, so
they are always the deployment's current ones, and no consumer code has
executed to put them there.

A layer chain may still be worth having for other reasons -- a guest that
is genuinely built by booting, snapshots, resume -- so the section above
is kept rather than deleted. It is no longer on the path for this.

### For `rebase`: a declared guest-contract version

The version the VMM half checks against, given that "same tree" no longer
implies "same contract". What it covers is the guest-side surface the
container half talks to: the control-net overlay that makes `granule exec`
reachable under flat mode, the agent, the authorized-key mechanism, the
disk-mode expectations.

Two properties worth fixing in the design rather than discovering:

- **The guest declares it; the VMM half refuses on mismatch.** Not a
  warning. A skewed control link is a VM the caller cannot reach, and a
  caller that believes otherwise will hand it out as a working sandbox.
  Refusing at boot, naming both versions, is the behaviour that fails
  where somebody can read it.
- **Keep the contract small, because that is the real mitigation.** A
  version check catches skew; a small surface means there is little to
  skew. Today the guest side is the control-net overlay, the agent, sshd
  with an authorized key, and whatever the consumer adds -- grain adds one
  static binary that copies bytes between a pipe and a socket. Every
  addition to that surface is a future incompatibility with a guest
  somebody built before it existed, so the bar for adding to it should go
  up now that guests outlive the VMM halves they were built against.

- **Decided: overwrite the agent binary during the rebase.** Rather than
  versioning whatever a consumer baked in months ago, the assembly that
  puts this deployment's VMM half under a consumer's guest also lays this
  deployment's current agent over it. A guest agent that is replaced on
  every rebase has no version to disagree about, which turns "keep the
  contract small" from a discipline somebody has to maintain into a
  property of how the image is built. The version check stays as a
  backstop for images assembled elsewhere.

  Where it has to land is the part with a constraint in it. Not the base
  `disk.img`: that is shared between consumers and versioned with the
  deployment, and mutating it invalidates every layer stacked on it (see
  *A named, ordered layer chain*). Not a consumer's own layers: those are
  theirs. So either a grain-owned layer above the consumer's chain, which
  means writing a file into an ext4 through a qcow2 and therefore an ext4
  writer -- a much larger lift than the qcow2 writer granule already has --
  or at pack time, where `mke2fs -d` builds the filesystem from a
  directory tree and overwriting a file in it is a file copy.

  That is a second reason to want *An OCI image as a rootfs source*
  below: it makes this operation trivial and, better, repeatable. Grain
  holds the consumer's published image, so it can re-pack on every upgrade
  with the current agent, with no consumer involvement and no rebuild.

  And it is why "small" remains a requirement rather than an aspiration:
  a binary that is overwritten into every consumer's image on every
  upgrade should be cheap to carry and have little in it to be
  incompatible with.

## Rejected: root over virtio-fs

**Not doing this.** Kept because the reasoning is the argument to revisit
if the ground moves, and because what it would have deleted is worth
knowing when the qcow2 machinery below looks expensive.

It was worth taking seriously, because if it worked it would delete most
of this document.

**The shape.** Do not pack a rootfs into a disk at all. Share it into the
guest over virtio-fs from the host, where the host side is the OCI image's
own filesystem -- composed from its layers the way any container runtime
composes one. The guest mounts that as `/`, writes into an overlay above
it, and never sees a qcow2.

The boot half is smaller than it first appears. granule already passes the
kernel and initramfs to cloud-hypervisor as host files
(`--kernel`/`--initramfs` in `internal/hypervisor`), not from inside the
disk, so there is no boot partition to preserve -- what is needed is an
initramfs that can mount a virtio-fs root and switch to it, which is
ordinary and is how Kata boots.

**What it deletes.** Nearly everything above it:

- The layer chain, its naming convention, and the fixed-path constraint
  that a qcow2 records its backing file by path.
- The size threshold for sealing, and the coarsened cache that comes with
  it.
- The depth ceiling, and `WriteOverlay` needing a backing-format
  parameter.
- Flattening, thinning, `zerofree`, and the zero-cluster trap.
- The base-mutation hazard -- a base moving under a layer being a
  silently wrong filesystem rather than an error.
- `pack`, which becomes "compose the tree and share it".
- `flatten`, which has no chain left to collapse -- though thinning has an
  analogue, since a host-side upper directory accumulates deleted files
  the same way a layer does.
- Not `verify`, which is unaffected: it boots an image and judges it,
  whatever the root is served from.
- The hard half of `rebase`. The trusted overlay and the agent binaries
  are host-side files in the shared tree, so making them current is
  writing a file on the host. No ext4 writer, no repack, no boot, and the
  cheap/complete split above stops existing.

**And it dissolves the tension this document has been circling.** The
qcow model forces a choice: run every instruction in a real VM and get
layers that are qcow2 files with all the problems above, or build in a
container and get Docker's layering but a kernel that is not the guest's.
Over virtio-fs there is no choice to make. A build boots the guest for
every instruction -- so every step sees the real kernel, the real CPU
topology, the real memory, and the silent-wrongness class stays dead --
and its writes land in a host-side upper directory that is committed as an
ordinary OCI layer with ordinary tooling. Correctness of always-boot,
layering of Docker, neither paid for with the other.

**The cost is performance, and it is the right thing to be unsure about.**
virtio-fs is a FUSE protocol over a virtio transport, and its weak spot is
metadata: `stat`, `open`, directory traversal. Grain's sandboxes are
almost pure metadata load -- `npm ci`, `go build`, `apt-get`, git
operations over large trees -- which is the worst case rather than a
favourable one. And this is not a data share bolted onto a block root: it
*is* the root, so every `exec` and every shared-library load goes through
virtiofsd too. There are levers (`cache=always`, a DAX window) and Kata
runs production workloads this way, so the answer is not obviously bad --
it is unknown, and it is measurable.

**Two costs that are not performance, and matter more than they look.**

*A new host-side boundary.* With a qcow2, a hostile guest can corrupt the
image file it was given. With virtio-fs it is talking to virtiofsd, a host
process holding an open tree, and granule's own substrate document notes
what that deployment grants one: `DAC_READ_SEARCH`, `seccomp:
Unconfined`, AppArmor unconfined. This design exists to run images a
*repository* authored, and the argument for it has been that the VM is a
real boundary owned by the deployment. Adding a host-side filesystem
daemon on the other end of that boundary is a change to exactly the thing
being argued.

*Snapshot and resume probably do not survive as they are.* granule copies
the disk overlay into a snapshot specifically so "a resumed guest lands on
the disk its memory was taken against". With virtio-fs there is no
overlay to copy, and vhost-user device state across `vm.snapshot`/restore
is hard -- granule's own substrate document lists cross-node restore among
the parts that are hard for anyone.

**And the gravity well.** granule's tree has already thought about this,
and its verdict on the neighbouring idea is worth reading before
adopting this one. On sharing an OCI bundle into the guest over virtio-fs
and driving it with a container runtime: "Closing this means putting a
container runtime in granule's guest, giving granule a virtio-fs share, and
teaching `execwire` (or replacing it with) the Kata agent protocol. At
which point the guest is a Kata guest and the question is what granule is
still contributing."

What is proposed here is deliberately short of that -- the guest is still
the workload, with no OCI runtime in it and no agent protocol swap; only
where its root comes from changes. That is a real and defensible middle.
It is also one step into a well whose bottom that paragraph describes, and
anyone taking the step should know which direction they are walking.

### What the field has already found

This was written expecting to end in "measure it". The published record
answers enough of it that the measurement is now the second question
rather than the first, and it answers it against this architecture.

**The attack surface is not theoretical, and it is being exercised.**

- **CVE-2026-47243**, critical, in Kata Containers: guest root to host
  root, through virtio-fs. The guest takes the virtio-fs PCI device's
  virtqueue from its own userspace, sends raw `FUSE_SYMLINK` requests
  carrying absolute *host* paths, plants a symlink in `/etc/cron.d`
  resolving through `/proc/<pid>/root/` to a guest-controlled payload,
  and host cron runs it as root. Fixed in runtime-rs 3.31.0.
- **CVE-2026-77179** (critical) and **CVE-2026-79994** (high), in
  Docker's own microVM sandboxes, fixed on 7 September 2026 -- ten days
  before this was written. Both are path-validation races in the
  virtio-fs host server and its socket relay: a guest replaces a parent
  directory with a symlink between the moment a path is validated and the
  moment it is used.

The shape of the difference is worth stating in one line, and somebody
already has: virtio-blk asks the host to move bytes at an offset, while
virtio-fs asks the host to interpret a filesystem request from an
untrusted guest. Strictly more powerful, and strictly more attack
surface.

**Two details make this worse for this design specifically, not better.**

The Kata advisory notes the vulnerability "requires an attacker to
already possess root-equivalent execution inside the guest VM" and "does
not affect unprivileged guest users". In most deployments that is a
meaningful mitigation. Here it is not a mitigation at all: the entire
point of this design is that a *repository* authors the guest image, so
whoever wrote that Dockerfile has root in their own guest by
construction. The precondition is satisfied by the feature.

And the affected configuration was `virtiofsd` launched with `--sandbox
none --seccomp none`. granule's own substrate document records what a
deployment in this space grants virtiofsd -- `DAC_READ_SEARCH`,
`seccomp: Unconfined`, AppArmor unconfined -- which is that posture. The
one deployment shape granule's tree has written down is the shape the CVE
exploits.

None of this says virtio-fs is unusable: the bugs are fixed, virtiofsd
has sandboxing modes, and the vulnerable configurations had them turned
off. It says the boundary this design's whole argument rests on would be
a demonstrably exercised one rather than "the guest can corrupt the disk
image it was given".

**And there is a compatibility blocker that is not about performance at
all.** virtio-fs cannot be the upper layer of an `overlayfs` without a
custom kernel patch, so Docker inside such a guest either fails to start
or silently falls back to a worse storage driver like `vfs`. Grain's
sandboxes run Docker -- the `Granulefile` installs `docker-ce`, and a
working dockerd is the reason the guest boots a distro kernel. Kata's documented workaround is to
loop-mount a sparse disk image at `/var/lib/docker`, which is a block
device reintroduced for the part of the workload that matters most, and
Kata's own devicemapper snapshotter takes the same route deliberately:
block devices rather than a shared filesystem.

So the saving is smaller than it looked. A virtio-fs root removes the
qcow2 machinery from the base image and puts a disk back underneath the
thing that does the heavy file work.

**Performance proper is the part still unmeasured**, and it is now the
least interesting of the three. DAX -- the mechanism that would close
most of the gap -- is still described as experimental, and the design
documentation is explicit that it shares file *contents* while metadata
and pathname lookup cannot be shared and need invalidation, which is the
half grain's workloads are made of.

### Where that leaves it -- rejected

Not "measure it and decide": measure it if the other two answers stop
being decisive. On the record above, a virtio-fs root trades a boundary
that has held for one with two critical escapes in the last month, in a
threat model where the CVE's own precondition is a feature of the design,
and does not even remove the block device from the workload that most
wanted the simplification.

The two need not be one answer, and this is where that matters. A
lifecycle reducer sandbox is small, read-mostly, runs no Docker and does
no build -- it answers an HTTP request -- so it is the case where
virtio-fs would cost least. It is also the case where the guest is most
directly repository-authored, which is where the security argument bites
hardest. Those point opposite ways, which is itself a reason not to split
the architecture on this axis.

## Segmented disks, and two things that were open

### Can cloud-hypervisor read a disk split into numbered extents?

Asked because it would be an elegant way out of the chain: one logical
disk, physically many files, so a rebuild that changes part of the disk
republishes one extent and a registry dedupes the rest -- layer-like
sharing with no backing-file semantics, no depth ceiling, no base-mutation
hazard.

**Not today, and the reason it is not is familiar.** The closest thing
cloud-hypervisor has is VMDK, whose descriptor format is exactly a list of
numbered extents. What it implements is the *flat* (monolithic) layout,
and multi-extent path handling is an open problem rather than a feature:
its issue #8713 is "missing containment for extent paths", and the pull
request against it is unmerged, disputed over whether `openat2()` is the
right containment, and settles in the meantime on requiring
`backing_files=true` to open a VMDK at all -- treating the format as
trusted input that a caller must opt into.

Which is the same hazard that rejected virtio-fs, one layer down: a
descriptor that names extents is a guest-adjacent file asking the VMM to
resolve paths on its behalf. Segmenting the disk means handing the VMM a
list of filenames from an image a repository authored.

If segmentation is ever genuinely needed, the safe route is
**vhost-user-blk**, which cloud-hypervisor supports (`vhost_user=true` and
a socket on `--disk`): a block daemon of one's own, free to lay bytes out
in as many files as it likes. And it is worth noting that this is *not*
the trade that was just rejected, by exactly the distinction that rejected
it -- a block backend is asked to move bytes at an offset, where a
filesystem backend is asked to interpret a filesystem request. A custom
block daemon is a far smaller surface than virtiofsd, which is why it
stays on the table when virtio-fs does not.

### Answered: discard works, so most of the thinning design is unnecessary

The same reading settles the open question above about reclaiming space,
and settles it the easy way. cloud-hypervisor v51.0 -- two releases below
the v53.0 `third_party/granule` pins -- added `DISCARD` and `WRITE_ZEROES`
to `virtio-blk` for both qcow2 and RAW, with a `sparse=on|off` option on
`--disk`, "enabling thin provisioning and efficient space reclamation when
guests trim filesystems".

So `fstrim` in the guest reclaims, and what granule needs is to pass the
option rather than to grow a zeroing pass. The `zerofree`-and-compact
design above is not needed: keep it as the fallback for an image that
arrives already fat, and reach for it only there.

The same release notes record "Zero bit in L2 entries" among the qcow2
features implemented, which is the other half: the zero-cluster flag this
document describes as the *correct* transformation is one the VMM reads.
A thinning pass that marks clusters zero rather than deallocating them
produces something cloud-hypervisor understands, so if that fallback is
ever built it is buildable correctly.

### Why `backing_files` defaults to off, and what that means here

It defaults to off because of **CVE-2026-27211** (GHSA-jmr4-g2hv-mjj6),
"Host File Exfiltration via QCOW Backing File Abuse": CVSS 7.2, CWE-73,
external control of a file name or path. Affected v34.0 through v50.0,
fixed in v50.1 and v51.0.

The attack: a guest overwrites its own disk header with a crafted qcow2
structure whose backing file names a path on the host. On the next boot
or disk scan, format auto-detection parses that header and serves the
host file's contents into the guest. A guest-initiated reboot triggers
the scan, so one compromised VM does the whole thing without the
management stack doing anything.

**A backing file is a qcow2 feature, and the disk that gets attacked is
a raw one.** That is worth holding in one sentence, because it is why
three mitigations were needed rather than one. The advisory's own framing
is exfiltration "when using virtio-block devices backed by *raw* images":
the victim is a raw disk, and qcow2 is the format the guest *forges* into
it. So `backing_files=off` says do not follow such a pointer, explicit
`image_type` says do not go looking for one, and the sector-zero refusal
says do not let a raw disk be turned into a forged header in the first
place. Any one alone leaves a path in.

The general hazard is not qcow2's either -- it is any disk format that
names other files. qcow2's backing file is one instance and VMDK's extent
descriptor, above, is the other one cloud-hypervisor touches; both are
being contained the same way and for the same reason.

granule's own dispatch is by filename suffix -- `.qcow2` gets
`image_type=qcow2,backing_files=on` and everything else gets
`image_type=raw`. Worth noticing that this fails closed in both
directions: a raw file named `.qcow2` fails to parse, and a qcow2 named
`.img` is read as raw and refused at the first sector-zero write. It is
still a decision made from a *name*, which in a design where those names
come from a published image is a thing to state rather than leave
implied.

Its preconditions are the two sentences that matter for this document:
"the backing image is writable by the guest, **or** the image originates
from an untrusted source", and "deployments utilizing only trusted,
read-only images are not affected".

**Three mitigations landed together, and granule has already met one
without recognising it.** `backing_files` defaulting to off, explicit
`image_type` replacing auto-detection, and a refusal of sector-zero
writes to an auto-detected raw image. That last one is what
`internal/hypervisor/args.go` records hitting: a raw image "needs
`image_type=raw`, or cloud-hypervisor refuses the first write to sector 0
('Attempting to write to sector 0 on a disk without specifying
image_type')". The comment reads it as a quirk to work around. It is a
security control, and naming the type explicitly is precisely the
intended response -- worth writing down so that nobody later simplifies
it away as a redundant flag.

**Today granule is safe by construction, and it is worth being clear
why.** It sets `backing_files=on` for every `.qcow2`, unconditionally.
But the only qcow2 in play is the per-VM overlay granule itself creates in
`PrepareOverlay`, whose backing path granule chose, and the base beneath it
is raw with `image_type=raw`. Both preconditions fail: the image is
trusted and this project wrote it.

**The layer chain breaks that invariant on both preconditions at once.**
Its layers come from a repository -- an untrusted source, by the
definition of the whole design -- and a qcow2's backing file is a *path
string inside the layer*, which is to say a field that repository
controls. With `backing_files=on`, the VMM follows it. That is the CVE's
own primitive, arrived at by design rather than by bug, and it needs no
guest write and no header overwrite: it needs a published layer whose
backing path is `/etc/shadow`, or this deployment's secret directory, or
another sandbox's disk.

**So the naming convention is the containment, not a convenience.**
Earlier in this document the fixed absolute paths are justified by
resolution -- a layer only resolves after moving between images if the
convention put it at the same path both times. They are also the security
control, and the better reason: if the only legal backing path is
`/var/lib/granule/guest/disk.img` or `.../layer.NN.qcow2`, then validating
one is a string comparison made before boot rather than a path-resolution
problem solved during it. It has to be enforced on **every** layer in the
chain rather than the top one, by whoever assembles the image. This is the
same containment the VMDK extent work above is arguing about, and it
wants the same answer.

**And it is an argument for `pack` over the chain that is independent of
everything else.** A packed raw `disk.img` with `image_type=raw` has no
backing file at all, so the mitigation stays on and the CVE's second
precondition is never met however untrusted the source. The chain model
requires turning a CVE mitigation off and rebuilding its containment in
granule. That is not a reason the chain cannot be built. It is a reason to
notice that the cheap path is also the safe one, which is not usually how
that goes.

Two of the advisory's other mitigations are worth passing on to granule
generally, since they are about the VMM rather than about this design:
Landlock sandboxing, and running cloud-hypervisor as an unprivileged
user.

## Open questions

- **Does the guest kernel stay with the VMM half?** Grain's intent is yes:
  copy `disk.img` and the layer chain, leave `vmlinuz` and `initrd.img`
  to the deployment. That works because the guest kernel is already
  granule's rather than the rootfs's -- "baked into the granule OCI image
  itself... matching what the bundled disk image itself needs to boot",
  with virtio built in -- so a customization that adds packages does not
  usually add kernel modules. It skews for a guest that installs a DKMS
  module or pins a kernel version. Whether granule wants to support that
  case, refuse it, or leave it documented is granule's call.
- **How does a layer declare what it was built against?** A layer backed
  by the wrong base is a corrupt disk rather than an error, so the chain
  probably wants the base's identity recorded somewhere checkable. qcow2
  already records a backing path and size; whether that is enough, or
  whether a manifest beside the chain is wanted, is a design question.
- **How deep a qcow2 backing chain will cloud-hypervisor boot?**
  **Answered: ten.** A chain of up to 10 qcow2 layers over a raw base
  boots; the 11th is refused with `MaxNestingDepthExceeded`, a hard limit
  in cloud-hypervisor's own block layer rather than a performance cliff.
  Reproducible across depths 9 through 13, measured against the version
  `third_party/granule` vendors; recorded upstream in granule's
  `docs/qcow2-storage-design.md` section 2.1 (gke-labs/granule#195), with
  the method, because the first pass of the measurement reported a false
  success at depth 12 -- its boot test also matched the word "Booting" in
  cloud-hypervisor's own error output.

  Ten changes the model rather than setting a parameter in it, which is
  the opposite of what this entry assumed. "One layer per lifecycle
  version" is not available to any active repository: a chain has to seal
  long before ten, and the thing that seals it is not optional. It also
  counts the whole chain *including* `PrepareOverlay`'s per-VM overlay, so
  a published image's usable budget is nine.

  The cost argument it was weighed against survives intact, though, and by
  a wide margin: 64 sparse layers over a 6.3GB base occupied 2.3MB in
  total.
- **What does a virtio-fs root cost?** Largely answered against, on the
  published record rather than on a measurement -- two critical escapes
  in the last month whose precondition this design supplies, and an
  `overlayfs` incompatibility that puts a block device back under Docker
  anyway. Reopen it if virtiofsd's sandboxed mode and a DAX that is no
  longer experimental change the picture, since the prize -- deleting the
  layer chain, the sealing threshold, the depth ceiling, thinning and the
  hard half of `rebase` -- stays large.
- **Does `flatten` subsume thinning, or sit beside it?** They are one
  pass over the data and almost always wanted together, which argues for
  one verb -- but a caller who wants depth fixed and size left alone
  (because the size is legitimately used) has no way to say so.
- **Is `rebase` one verb or two?** Its cheap form (swap the container
  half) and its complete form (also refresh what is inside the disk) have
  different costs, different prerequisites and different failure modes,
  and a caller usually knows which it wants. One verb that silently does
  the expensive thing when it can is friendlier; two are honest about
  what a call will cost.
- ~~Does the pinned cloud-hypervisor honour a discard option?~~
  **Answered: yes.** `DISCARD`/`WRITE_ZEROES` and `sparse=on|off` landed
  in v51.0, below the v53.0 pin. See *Segmented disks* above.
- **What is the size threshold, and is there an escape hatch?** It wants
  choosing against the depth ceiling and a typical image size rather than
  picked round. And a consumer who knows a step produces something large
  and shareable may want to force a seal, the way a Dockerfile author
  today controls layering by how they group `RUN` lines -- worth having
  eventually, not worth having first.
- **Does `guest build -setup` keep its place beside the Dockerfile
  path?** Yes on the evidence here -- booting the guest is the only way to
  start a service, load a module, run a container or test a finished image
  -- but the two now overlap for the large middle of what people actually
  do, and which one a bare `granule guest build` means wants deciding
  rather than defaulting.
- **The guest agent is E2B's `envd`.** Decided, and designed separately
  in [adopting envd as grain's guest agent](envd-adoption.md) -- it is a
  grain-wide change touching four sandbox backends, the MCP tool surface,
  credential placement and console sessions, rather than anything granule
  has to do.

  What it means for this document is only the split: **granule's agent
  owns the machine, envd owns the workload.** vsock, readiness, graceful
  shutdown, memory and CPU hotplug and the control-net link stay granule's
  and have to work before envd is running at all; processes, PTYs and
  files are envd's. So the guest contract versioned above covers granule's
  half, and grows by one more binary that the rebase overwrites along
  with the rest.

  It needs no networking change on either side: envd listens inside the
  guest and is dialled over the same exec pipe everything else uses, so
  nothing about netshim, DNAT, per-VM ports or "never resolve an address
  for a VM" moves. The one change it does need -- a bind-address flag, so
  it is not listening on the segment under `NetModeFlat` -- goes upstream
  to E2B rather than being carried locally, for the reason `VENDORED.md`
  gives about granule.
- **Is any of this useful to granule beyond grain?** The layering ask is
  general -- it is "docker layers, for guest disks" -- and the version ask
  only arises for a consumer that mixes provenances. Granule may reasonably
  want the first and not the second.
