# Building, packing and rebasing guest images

> **Note**: This project has been renamed from **kontur** to **granule** (`konturctl` -> `granule`, `kontur` -> `granule-shim`, `Konturfile` -> `Granule` / `Granulefile`).


A granule image carries two halves in one artifact: the VMM half
(`/usr/local/bin/granule`, `cloud-hypervisor`, netshim, the entrypoint)
and the guest half (`/var/lib/granule/guest/` -- a packed root disk and,
sometimes, the kernel and initramfs that disk needs). `docker run` on it
boots a VM, which is why there is one artifact here rather than two.

Today those halves are built together and move together. This document is
about the case where they do not: a guest customized by somebody who is
not the operator, run on a VMM half the operator controls and upgrades
independently. That is what `granule guest build -from ... -setup ...`
reaches for today by booting a machine and committing the whole disk, and
what the verbs below are meant to do properly.

It is written from a consumer's request -- see grain's
`docs/granule-guest-images.md`, which states the requirement in more
detail and works through the alternatives that were rejected -- but the
shape is general: "an image somebody else customized, booted by machinery
that stayed mine" is not specific to that consumer.

## Where the seam is

`pkg/guestimage` is the guest half as a list of paths rather than as
"whatever is in that directory". That list is the whole interface between
the two halves, and its paths are fixed on purpose:

```text
/var/lib/granule/guest/disk.img      the packed guest root disk (raw)
/var/lib/granule/guest/guest-contract  which contract version that disk speaks
/var/lib/granule/guest/vmlinuz       the guest's own kernel, when it has one
/var/lib/granule/guest/initrd.img    the initramfs matching it
/var/lib/granule/guest/vmlinux       the VMM half's kernel -- not the guest's
/var/lib/granule/overlay/disk.qcow2   per-VM writes, created at boot
```

Two of those properties are load-bearing:

- **The paths are absolute and resolved inside the VM container**, where
  cloud-hypervisor runs too.
- **The suffixes are functional.** `hypervisor.BuildArgs` configures disk arguments from them.

`vmlinux` sits in the guest directory but belongs to the VMM half: it is
granule's own build, it moves when granule moves, and a guest that carries
one pins a kernel the deployment has since replaced. `guestimage.Files()`
excludes it for that reason.

### Which half brings the kernel

Exactly one of them has to, and either may. `vmlinux` is the VMM half's
answer, and it is what boots an ordinary guest -- one packed from the
reference rootfs, which brings no kernel at all. `vmlinuz` plus
`initrd.img` are the guest's own, present only where the rootfs was
built with `GUEST_KERNEL_PACKAGE`; where they are, they win
(`pkg/config`'s `defaultKernel`).

The combination that cannot boot is neither, and it is reachable by
accident, because **a `docker build --target runtime` granule image
carries no `vmlinux`**. That is deliberate: `runtime` is the VMM half
for a deployment assembling a guest that brings its own kernel -- the
runner images under `deploy/github-runner-images` are exactly that --
and the few MB are left out along with the bundled guest disk. It does
mean the lean image is an incomplete half rather than a small one, and
that packing, wrapping or rebasing an ordinary guest onto it yields an
image that builds, pushes and then stops at

```text
granule: CHV_KERNEL (/var/lib/granule/guest/vmlinux): stat ...: no such file or directory
```

on whichever node runs it. So `guest pack`, `wrap` and `guest rebase`
establish at *build* time that one of the two halves brings a kernel and
refuse when neither does, naming both images and both ways out:
`guestpack.KonturImageHasKernel` asks the granule image what it carries
(starting nothing from it), and a pack whose granule image has no
`vmlinux` asserts a `/boot/vmlinuz-*` in the rootfs before it packs
anything. A refusal at build time costs a build; the same mistake found
at boot time costs an outage.

What to name with `-kontur-image`, then, is the full image for an
ordinary guest -- its bundled disk stays in the result's lower layers,
which `guest pack` reports, and those bytes are the price of the kernel
-- and the `--target runtime` build for a guest that brings its own.

## The guest contract

The paths above say where the two halves meet. The contract says what
they have to agree *about*, and the version says whether they do.

Both halves of an image built from one tree agree by construction: they
are the same files. Everything this document is about breaks that on
purpose. The operator's upgrade moves the VMM half for every deployment
at once; a guest packed six weeks ago by another repository does not move
with it, and is not supposed to -- that is exactly what makes a granule or
cloud-hypervisor fix reach a customized guest without a rebuild. What
was guaranteed by construction therefore has to be guaranteed by a
version.

**What is in the contract** is the whole guest side, and it is short:

- the control-net overlay -- `granule-configure-net`, the units and init
  scripts that run it, and the addressing scheme they and `netshim`
  agree on;
- the guest agents, `granule-agent` and `granule-mem-agent`, and the vsock
  protocol they speak to the VMM half -- which is also how `granule exec`
  gets in, and how a guest declares itself ready;
- the disk-mode expectations: root on `/dev/vda`, `granule-growfs`
  resizing it onto whatever `CHV_DISK_SIZE_MB` gave it, and what an
  overlay or a direct disk means for what is writable;
- the boot-time parameters the VMM half passes down -- the kernel command
  line, the devices it attaches -- and the guest-side machinery that
  reads them.

Four items, and one of them used to be five. The guest carried sshd, an
account to log into and a per-boot keypair, purely so that `granule exec`
had a way in; moving exec onto the vsock the agent was already using took
the whole apparatus back out. That is the shape of the argument in
"Changing the contract" below, made before there was a version to
protect: the surface that is not there cannot skew.

**Where the version is declared.** `guestimage.Contract` is the number
this build of granule implements, and one authored file states the same
number for the guest side:
`deploy/guest-image/overlay-common/etc/granule/guest-contract`. It
reaches two more carriers, which exist because they fail in different
directions -- each is readable at a moment the others are not:

| carrier | written by | read by |
| --- | --- | --- |
| `/etc/granule/guest-contract`, inside the rootfs | the overlay itself, so `pack`, `wrap` and `rebase -rootfs`/`-boot` all place it without knowing about it -- and the Dockerfile's own bundled guest gets it from a `pack` like everybody else | anything holding the filesystem: a commit out of a running VM, somebody else's tooling, a label that has been stripped |
| the `dev.granule.guest.contract` label, on the packed image | `guest pack` and the complete forms of `guest rebase`, from `guestimage.ContractValue()` | a *cheap* `guest rebase`, with one `docker image inspect` and no disk opened |
| `/var/lib/granule/guest/guest-contract`, beside the packed disk | `pack-guest` and `deploy/github-runner-images/prepare-rootfs.sh`, by copying the file above out of the rootfs they just packed; `rebase -boot` restates it before it commits | the VMM half, at boot |

The label is the one that is written down twice rather than copied --
there is no way for a `docker build` to take a label's value out of a
file -- so `TestContractIsDeclaredEverywhere` is what keeps it, the
overlay's copy and the constant saying the same thing.

The sidecar beside the disk is the copy a boot reads, and it is outside
the disk on purpose. The check has to happen *before* cloud-hypervisor
starts: a VM that boots and is then killed has already been handed to a
caller as running, and a refusal that arrives after that is worth
nothing. The label is no use at that moment either -- at boot there is no
image to inspect, only a container that already is one. Reading a file
from inside the disk would mean either mounting a filesystem an untrusted
consumer authored -- privileges the VM container deliberately does not
have -- or putting an ext4 reader inside the one binary whose smallness
is itself an isolation property. A plain file beside the disk needs
neither. It is resolved relative to whichever disk is being booted rather
than at a fixed path, so a `CHV_DISK_IMAGE` pointing elsewhere is not
validated by the bundled guest's declaration. Being copied out of the
packed tree rather than written separately is what stops it and the copy
inside the disk coming to disagree.

**A mismatch is a refusal, not a warning** (`guestimage.BootContract`'s
`Check`, called from `config.FromEnv` before any overlay is made or any
VM started). A guest declaring a version this granule does not implement
is a guest whose control link, agents or disk expectations are not the
ones being provided -- a VM that boots, looks healthy from outside and
cannot be reached, which a caller then hands out as a working sandbox.
The error names both versions and what would satisfy the newer one: boot
it under a granule implementing that version, or repack the guest against
this one.

A declaration that is present and does not parse is refused too, as its
own case. A file sitting there with something that is not a version in it
is evidence that something went wrong assembling the image, not evidence
that the image is old. (A cheap `rebase` reports that case rather than
refusing it, which is the same asymmetry as the two floors below: a
rebase is deciding whether to spend a build, and a boot is deciding
whether to hand out a sandbox nothing can reach.)

**Two floors, deliberately.** `guestimage.MinimumContract` is what a
cheap `rebase` refuses below; `guestimage.MinimumBootContract` is what a
boot refuses below. They are not a duplicate of each other, because
being too strict costs differently: a rebase that refuses wrongly costs
the caller a repack at a terminal, and a boot that refuses wrongly costs
somebody's workload. So the boot floor moves later and more reluctantly,
and today it sits below every version anything declares -- which is what
the next section is about.

### Absent is version 0

An image carrying no declaration boots, as `ContractUndeclaredVersion`
(0), and
is logged as undeclared on every boot. The alternative -- refusing it --
was rejected, and the reasoning is the point rather than the verdict:

- **Absence is not evidence of skew.** Every image built before this file
  existed was built against a VMM half that agreed with it by
  construction, and version 1 is that same contract with a name on it. An
  undeclared guest is a version-0 guest today's VMM half can boot.
- **Refusing it would break the field on an upgrade, for skew that has
  not happened.** This change would stop every guest already built,
  including the documented ordinary case of an operator pointing
  `CHV_DISK_IMAGE` at a disk of their own -- and a check that refuses
  everything on the day it ships is a check that gets turned off.

It is a deferral rather than a hole. `guestimage.MinimumBootContract` is
the floor, it rises when a contract change leaves an older guest
unreachable, and
every undeclared guest is then refused by the same named error --
which is why the undeclared case has its own wording in that error and
its own line in every boot's log: an operator can find those images
before an upgrade does. `CHV_GUEST_CONTRACT_MIN` raises the floor for a
deployment that packs every guest it runs and wants a declaration
required today. It only ever tightens; nothing lowers it, because that
would be the warning this check exists not to be.

### Changing the contract

The version check catches skew. Keeping the contract small is what means
there is little to skew, and that is the larger half of the mitigation.
Now that guests outlive the VMM halves they were built against, **every
addition to the guest side is a future incompatibility with a guest
somebody built before it existed**, so the bar for one is high. Before
adding to it:

1. **Can it live in the VMM half instead?** The VMM half is always the
   deployment's own and moves with the upgrade, so anything implemented
   there is a thing that cannot skew. This is the answer most of the
   time, and preferring it is why the guest side is four items long.
2. **Can the guest side ignore it?** A VMM half that tolerates a guest
   not having the new thing costs no version bump at all: the guest is
   still reachable, just without the new capability. Degrade rather than
   require, where the feature allows it.
3. **If it must go in the guest, bump `guestimage.Contract`** and say in
   this document, and in that constant's own history, what changed. Three
   lines move together -- the constant, the overlay's
   `etc/granule/guest-contract`, and the `LABEL` in the Dockerfile's final
   stage -- and `TestContractIsDeclaredEverywhere` fails until they
   agree. A bump alone does not stop old guests; it says what is new.
4. **Only raise a floor when an older guest is actually unreachable**,
   not merely missing something new. A floor is what makes guests in the
   field stop booting, so it moves for a reason somebody has written
   down here, with the older guests' repack path named in the same
   breath -- and `MinimumBootContract` is the more expensive of the two
   to move, since what it stops is a workload starting rather than a
   rebase running.

`pack` and `rebase` are the reason the bar is survivable: both lay
today's overlay, agents and declaration over a rootfs, so a guest that is
repacked has no version left to disagree about. The check in this
section is the backstop for images assembled elsewhere, and is built as
one -- it is not the mechanism by which guests stay current.

## The verbs

Six, of which five exist -- `build`, `pack`, `wrap`, `rebase` and
`verify`:

| | |
| --- | --- |
| **build** | Turn a consumer's Dockerfile into a guest rootfs image. Today's `guest build -setup` is the one-instruction case of this, done by booting. |
| **pack** | Turn an OCI image's filesystem into a bootable guest disk, laying granule's own agents and overlay on during the packing. |
| **wrap** | Turn an *ordinary container image* -- one that was never meant to be a machine -- into a guest that runs it. Pack supplies the disk; wrap supplies the machine the consumer's image is placed inside rather than made of. |
| **rebase** | Make an existing guest image's granule half current. Cheap form: replace the VMM half. Complete form: repack (or a boot, where nobody has the rootfs any more), so what is *inside* the disk is current too. |
| **verify** | Boot an image, run checks, produce a verdict and no artifact. |
| **flatten** | Collapse a chain of disks into one and reclaim what is unreachable. Maintenance on an image, not a way of making one. |

`pack` and `rebase` are the same intent at different stages -- make the
trusted parts current. `pack` gets it for free because it is building the
disk; `rebase` is that refresh applied to an image already packed, by
somebody else, some time ago.

The division of labour that matters for security: **`pack`, `wrap` and
`rebase` execute nothing from the image they operate on**, so they are
safe to run on the machine that runs the daemon. `build` executes plenty
-- it is running a consumer's `RUN` lines -- and belongs in that
consumer's own CI, or in a sandbox, and not on a host whose isolation is
the point.

`rebase -boot` is the one crossing of that line, and it crosses it the
way the design allows rather than by accident: it runs the guest's own
code *in a VM*, which is the distinction the whole thing rests on, and it
needs `/dev/kvm` for exactly that reason. The other two forms of a
rebase, and every pack, stay on the safe side of it.

## What has landed

- **`pkg/guestimage`** -- the guest half named, with `Files()` for
  what a copy has to move, `Under()` for assembling one somewhere else,
  and `Check()` for an assembly that forgot the disk or brought a kernel
  without its initramfs -- or `CheckPresent()`, the same rule against
  something that is not a directory this process can stat, which is how a
  rebase checks the *image* it built without unpacking a disk measured in
  gigabytes onto the host. `pkg/config` takes its defaults from it,
  so there is one statement of the convention rather than five.
- **`guest-rootfs-image`** -- the guest rootfs published as an OCI image
  in its own right, so a consumer can write `FROM` against it. It was
  already an internal build stage (`guest-customized`); making it a named
  target is what turns "a Dockerfile against the granule rootfs" from
  something nobody can express into the ordinary thing.
  `deploy/publish-images.sh` pushes it to
  `ghcr.io/gke-labs/granule/<variant>.rootfs`, separate packages carrying
  the `:latest` moving tag, each with an immutable `:os-<key>` beside it --
  a hash of the rootfs recipe rather than a commit, because these stages
  build an operating system and nothing of granule's, and what a pin
  should follow is the OS rather than this repository
  (`deploy/guest-rootfs-key.sh`). So a consumer pulls a base rather than
  building one -- the argument this document already makes for
  publishing the packed guests, applied to the thing they are meant to
  derive *from*.
  See `deploy/guest-image/README.md`.
- **A Dockerfile front end for `build`** -- `granule guest build -f
  Dockerfile -t <tag>` (`pkg/guestdockerfile`). It validates the
  instruction list, supplies the `FROM` where the file names none, and
  drives an ordinary `docker build`; docker keeps the building and the
  layer caching, and granule keeps the two decisions it cannot delegate.
  The subset is refused loudly rather than partially honoured, naming the
  file, the line and the instruction. A bare
  `granule guest build` is now this route, with the booting one asked
  for by name (`-setup`): the two overlap for most of what people do,
  and where they overlap this one needs no KVM, no boot and no VM. What
  the docker build produces is a rootfs image, which is `pack`'s input,
  so the command packs it too and `-t` is a bootable image on both
  routes. The rootfs keeps a name of its own (`-rootfs-tag`, default
  `-t`'s tag plus `-rootfs`) because that is what a consumer publishes
  and what `rebase` repacks; `-rootfs-only` is the build that stops
  there.
- **A base a file can name.** `FROM` is accepted, and what makes it safe
  is the mechanism `pack` already has rather than a check: the base
  supplies the filesystem payload, and granule's own half is discarded and
  re-laid from the current build at pack time. What it buys is a *pinned*
  base -- an invisible granule-supplied one cannot be written down, so the
  same file silently builds on a different machine as granule moves. The
  last `FROM` is the base and earlier ones are build stages, which may
  name anything (`FROM golang AS builder` … `COPY --from=builder`), since
  nothing of a stage reaches the guest but what a `COPY` takes out of it.
  Which bases actually work is `pack`'s question, not a string's: a guest
  rootfs packs, and a plain OS image (`debian:13` -- an OS that has never
  been through the guest customisation) and an image that is not an OS at
  all (`scratch`, `golang`) are refused by `deploy/guest-image/pack-guest`,
  each saying which of the three it is -- a fourth message for an image
  that is *already packed*, since that is the one this shape invites and
  `rebase` is the verb for it. A file whose only instruction is a `FROM`
  of a guest rootfs image is therefore the same operation as `rebase
  -rootfs`: that image's payload, repacked onto today's granule.
  `SHELL` and `EXPOSE` are accepted too -- see the front end's own table
  in `deploy/guest-image/README.md`.
- **`pack`** -- `granule guest pack` turns an OCI image's filesystem
  into a bootable guest disk and emits a granule image around it, laying
  the agents and the overlay on during the packing. It runs `mke2fs -d`
  rather than a VM, so it needs no KVM and executes nothing from the
  image it is packing. The script that does the laying down and the
  `mke2fs -d` is `deploy/guest-image/pack-guest`, shipped *in* the granule
  image at `guestimage.PartsDir` so that a pack's guest-side pieces come
  from the image being packed onto. The contract declaration is laid down
  with them and copied out beside the packed disk, so a repacked guest
  declares the version it now actually speaks rather than the one its
  base was built with. See `pkg/guestpack`.
- **`wrap`** -- `granule wrap` (`pkg/guestwrap`), with
  `cmd/granule-container` as its guest-side half. It is a `pack` whose
  rootfs is assembled in the same build: granule's guest rootfs, with an
  *ordinary container image's* filesystem placed inside it at
  `/var/lib/granule/container/rootfs` and that image's OCI configuration
  written beside it, which the guest pivots into, in a mount namespace of
  its own, and runs at boot. The
  point is the images it accepts -- anything a registry holds, rather
  than only what was built to be a machine -- and it keeps pack's rule
  that nothing out of the consumer's image is executed to build it: the
  image appears once, as a `COPY`, and its configuration is read with
  `docker image inspect`. See the top-level README's "Wrapping a
  container image".
- **`rebase`** -- `granule guest rebase` (`pkg/guestrebase`), all
  three forms below. One verb rather than two: the cheap and the
  complete forms are the same intent, and what separates them is what
  the caller still has rather than what they want. The expense is never
  silent either -- the default is the cheap form, and a complete one
  happens only when the caller names the rootfs (`-rootfs`) or asks for
  a boot (`-boot`). A cheap rebase ends by saying what it did *not*
  refresh and naming the flag that would, including the rootfs image
  recorded in the `dev.granule.guest.packed-from` label a pack leaves
  behind. Reading that label and repacking from it unasked was the
  alternative, and it is the one thing here deliberately not done:
  minutes and a new disk is not a decision an image should make on a
  caller's behalf. Where the image declares a guest-side contract older
  than this granule can drive, that report becomes a refusal instead --
  see the item below it.

  The cheap form is `FROM` the granule image with the guest half copied
  in, and not the reverse: an entrypoint and an environment are as much
  the VMM half as the binaries are. The one thing of the rebased image's
  own config that does come across is what it declares with `EXPOSE`:
  that is a statement about the machine inside rather than about the
  half being replaced, so a rebase re-declares it (declared by the
  image, granted by whoever launches it -- `granule vm create
  -publish-declared`, on loopback unless `-publish-addr` says
  otherwise; see the front end's `EXPOSE` in
  `deploy/guest-image/README.md` and the top-level README's "Publishing
  a guest's ports"). What crosses is `guestimage.Files()`,
  so `vmlinux` stays the deployment's. The mirror image of that hazard is
  refused rather than built -- a granule image carrying a *bundled* guest
  kernel, rebased under a guest that brings none, would leave that pair
  beside a stranger's disk, and there is no `RUN` in this build to delete
  it with.

  The complete form by repack is a `pack` of the rootfs image with the
  current agents in it -- which is the same operation as a Dockerfile
  whose only instruction is a `FROM` of that rootfs image, run through
  `guest build`. Since the front end accepts a `FROM` (below), those two
  are one path reached by two names, and that is a property worth
  keeping rather than a duplication to remove: `rebase -rootfs` is the
  verb for somebody making an image current, and the one-line Dockerfile
  is the spelling for somebody who is already writing one. Neither
  applies to an *already packed* image, which is what the cheap form and
  `-boot` are for; `pack-guest` says so if it is handed one. The one by boot lays the same parts down with
  `pack-guest -lay-only`, inside the guest, having first built the cheap
  rebase and booted *that* -- so the VMM half running the guest is
  already the operator's. It is `guest build -setup` with a
  one-instruction script, plus a staging step
  (`guestbuild.Options.Stage`, over `guestboot`'s session) to get the
  agents in: a script travels on a command line and a 5MB static binary
  does not.
- **A declared guest-side contract version** -- `pkg/guestimage`'s
  `Contract`, written into every guest granule packs and read back by a
  cheap `rebase`. Rebasing overwrites the agents, which is the
  mitigation for a guest built months ago under an older granule; the
  declared version is the backstop, because it is the only thing that
  lets the cheap form -- the one that leaves the disk alone -- refuse
  rather than warn. It is a single integer with a floor
  (`MinimumContract`) rather than a set of named capabilities: what the
  refusal needs is one ordering, so that "older than granule can drive"
  is a comparison rather than a set difference, and that is the shape
  the `pack-guest` check about a rootfs enabling none of granule's units
  already has. The fleet question a capability set answers ("can this
  guest do X yet") can be added beside it later; it cannot replace the
  floor, because only the floor can refuse.

  It is written three times, in places that fail in different
  directions: the `dev.granule.guest.contract` label, readable with one
  `docker image inspect` and so affordable to a form whose whole
  argument is that it never opens `disk.img`;
  `/etc/granule/guest-contract` inside the guest, placed by the overlay,
  which survives a repack by somebody else's tooling, a commit out of a
  running VM and a rewritten label; and
  `/var/lib/granule/guest/guest-contract`, the sidecar beside the packed
  disk, which is the only one a *boot* can read. The label decides a
  rebase's refusal, the sidecar decides a boot's, the file in the guest
  is the record of last resort. An image that declares nothing -- every
  image published before this landed -- is reported rather than refused,
  since a backstop whose first act is to break every existing guest is
  not one anybody can turn on; `-force` goes ahead below the floor for
  the caller who knows something the label does not. See
  `deploy/guest-image/README.md`'s "The guest-side contract".
- **A boot that refuses a skewed guest** -- the same version, read by the
  VMM half from the sidecar beside the disk before cloud-hypervisor is
  started, and a mismatch is a refusal naming both versions and what
  would satisfy the newer one. `pkg/guestimage/bootcontract.go`,
  checked from `config.FromEnv`. It is the backstop for a guest somebody
  else assembled -- a repack has no version left to disagree about -- and
  it has a floor of its own (`MinimumBootContract`) because refusing a
  boot costs an outage where refusing a cheap rebase costs a build. "The
  guest contract" above is what is in the contract, why an absent
  declaration is version 0, and what it takes to add to the guest side
  now that guests outlive the VMM halves they were built against.
- **`verify`** -- `granule guest verify` boots an image, runs a named
  set of checks inside it as root, and prints a verdict: no commit, no
  tag, no image. `-granule-image` boots it after the cheap form of a
  rebase, which is the state a deployment running its own VMM half will
  actually boot; the verdict is keyed on both digests and the check set
  and cached; and a failure is an error, not a warning. That rebase is
  `rebase`'s own (`guestrebase.Cheap`, under a deterministic throwaway
  tag verify deletes again), so the machine verify judges and the machine
  a rebase produces cannot drift apart -- including the refusals above,
  which verify now makes before it boots anything. See
  `pkg/guestverify`, and `pkg/guestboot` for the boot-and-exec
  loop it shares with `build`.
- **`sparse=on`** -- passed on every `--disk`, so `fstrim` in a guest
  reclaims what it deletes. See `pkg/hypervisor`.

## What is next, and why in that order

**`flatten`**, last, because nothing else waits on it: it is maintenance
on an image rather than a way of making one, and the packed path this
tree took keeps disk chains short enough that it is not yet paid for.

## Deliberately not scheduled

**A qcow2 layer chain.** `/var/lib/granule/guest/layer.NN.qcow2`, each
recording a backing file and an L1 table. Superseded in favor of flat multi-extent
VMDKs with opportunistic reflink cloning, which eliminate cluster allocation overhead
and table serialization while preserving $O(1)$ extent cloning on reflink filesystems.

A packed raw `disk.img` with `image_type=raw` has no backing file at all, so the mitigation stays
on however untrusted the source -- the cheap path is also the safe one,
which is not usually how that goes.

Worth building if a guest genuinely built by booting (snapshots, resume)
turns up wanting it. Not on the path for packing a rootfs.

**A virtio-fs root**, which would have deleted most of this document, is
rejected on the published record rather than on a measurement: two
critical guest-to-host escapes through virtio-fs in the month before this
was written, in a threat model where the CVEs' own precondition (root in
the guest) is a feature of the design, plus an `overlayfs` incompatibility
that puts a block device back under Docker anyway. grain's document has
the full argument.

## Two things to pass on to cloud-hypervisor's configuration generally

Independent of all of the above, and about the VMM rather than the image:
Landlock sandboxing, and running cloud-hypervisor as an unprivileged
user. Both are mitigations the advisories above recommend for exactly the
posture this project runs in.
