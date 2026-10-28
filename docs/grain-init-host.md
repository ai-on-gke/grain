# A cloud host for `grain init`

[`grain init`](grain-init.md) demands `/dev/kvm` and refuses to turn on
nested virtualization "on an operator's behalf" -- but says nothing
about where to get a host that already has it. This is that: a GCE
instance shape that works, verified by bringing one up and running
`grain init` on it, not a recommendation arrived at some other way.

## The instance

```bash
gcloud compute instances create HOST \
  --zone ZONE \
  --machine-type n2-standard-8 \
  --enable-nested-virtualization \
  --image-family debian-12 --image-project debian-cloud
```

`--enable-nested-virtualization` is a first-class `gcloud` flag. It
needs no VMX-licensed disk image -- `third_party/granule`'s own GKE
walkthrough said otherwise until gke-labs/granule#172 corrected it, and
the same correction applies here.

It needs a machine family that actually offers nesting: N1, N2, N2D or
C2, Haswell or later. **E2 does not support it.** Neither does T2A --
which matters for a different reason: nested virtualization on GCE is
**x86 only**. There is no Arm shape that offers it, so this host always
runs the **amd64** guest image, never the arm64 one some other part of
this project builds for Apple Silicon. The Granulefiles are arch-aware
and will do the right thing automatically; the thing to avoid is
wondering why an arm64 image won't load on a host that was never going
to accept one.

## Sizing, from evidence

`n2-standard-8` (8 vCPU, 31 GiB) with an 8 GiB host reserve (see
`-sandbox-memory-reserve-mb` in [`grain-init.md`](grain-init.md) and
`model.DefaultReserveMemoryMB`) leaves a 23.4 GiB sandbox budget --
enough for one 4 vCPU / 8 GiB sandbox at a time with room to spare, or
several smaller ones.

That headroom matters more than it looks. On an 8 vCPU host, `go build
./...` on this repository takes about 32 seconds inside a sandbox. On a
2 vCPU / 2 GiB sandbox, the same build did not finish in 34 minutes.
Undersizing the sandbox doesn't make builds slow -- it makes them not
complete.

## Verifying acceleration, not just its symptoms

`/dev/kvm` existing, and `kvm-ok` reporting success, can both be true on
a host where KVM has silently fallen back to software emulation. That
failure mode doesn't announce itself: a granule guest on an emulated
CPU boots. It just never finishes becoming usable, which looks like a
hung sandbox rather than a missing prerequisite.

The check that actually proves acceleration works is to ask QEMU for it
and watch what happens when it can't deliver:

```bash
qemu-system-x86_64 -accel kvm -kernel /boot/vmlinuz-$(uname -r) -nographic -no-reboot
```

Pointed at the host's own kernel only because it's guaranteed to exist
-- nothing about the test depends on that kernel actually finishing
boot. `-accel kvm` (unlike `-accel kvm:tcg`, which is permitted to fall
back) makes QEMU refuse to start at all if KVM acceleration isn't
really available, and exits non-zero saying so. A host that passes this
check has real acceleration; a host that merely has `/dev/kvm` and a
passing `kvm-ok` has not yet been checked.

## Suspend/resume, and its one trap

A RUNNING `n2-standard-8` bills for 8 vCPU continuously, which is wrong
for a host that is idle most of the time between runs. Suspending it is
safe and cheap:

```bash
gcloud compute instances suspend HOST --zone ZONE
gcloud compute instances resume  HOST --zone ZONE
```

Measured: suspend takes about 30 seconds, resume about 10. `/dev/kvm`
survives the round trip, and the instance's clock comes back within
about 3 seconds of correct. A suspended instance bills its persistent
disk only, not its vCPUs -- the whole reason to do this.

**The trap: a resumed host comes back with its processes still
running.** Suspend preserves RAM rather than restarting the machine, so
a dropped ssh session is not evidence the daemon stopped, and starting
a second daemon next to a resumed one that never stopped is a real
failure mode, not a hypothetical one -- it is what produced a
deployment serving a stale binary from a previous build, because the
newly started daemon never got the port the old one still held. Check
for a running daemon before starting one on a host that might have been
suspended rather than freshly booted.

Suspend only while quiescent. Nothing here drains running sandboxes
first, so suspending mid-run strands them.

## Capacity

n2 capacity is not reliably available in every zone. Bringing this host
up hit `ZONE_RESOURCE_POOL_EXHAUSTED` in four zones before one had room.
Shopping zones at creation time is faster than waiting for one to free
up, and a zone pinned into a script is a liability for the same reason
-- it will eventually be the zone that's out.

## Reaching the UI

A fresh `grain init` writes `-ui-addr 0.0.0.0:4242` into the unit
(the daemon's own default too), so the UI/API is at
`http://HOST:4242/`. It also writes `-ui-allowed-hosts`, set to the
machine's own names. An edition may add more for its own hosts
(`pkg/hostinit.RegisterAllowedHosts`): a web proxy that fronts the
machine, say. Such a proxy's requests aren't loopback ones, so they still
have to sign in. A re-run over a unit naming only the machine's own names
adds them. An explicit
`-daemon-arg -ui-allowed-hosts=...` is written unchanged. A re-run without `-ui-addr` keeps the address the
installed unit already passes. A unit written by an older `grain init`
names none, so a re-run pins the `127.0.0.1:8420` it was serving on.
Every caller, loopback included, presents the deployment's access code
(`grain access-code` prints it), so anyone who has it and can reach the
address has owner access. To keep the UI off the network, pass
`-ui-addr 127.0.0.1:4242` and use an ssh forward:

```bash
ssh -L 14242:localhost:4242 HOST
```

Forward to a *local* port other than 4242 itself if a locally run
deployment might also be listening there -- two deployments sharing one
local port silently point the browser at whichever one bound last,
with nothing to say which that was.

## Core vs. per-deployment state

The distinction that keeps a teardown from turning into archaeology:

- **Core** -- outlives any single deployment: the credentials used to
  create and reach a host (a GitHub token, GCP auth) and the scripts
  that bring one up. These belong wherever you keep durable local state,
  not on the host itself.
- **Per-deployment** -- everything `-data-dir` holds on the host: the
  store, caches, the credentials and connections `grain init` writes. Disposable, and
  the test for that is: it must be possible to delete the whole thing
  and reconstruct an equivalent deployment from the core pieces plus one
  script, with nothing unique lost.

That test matters because failing it is a real failure mode: a launch
script that lives *inside* the data directory it configures cannot be
torn down without first reading the script out of the thing being
deleted, to find out how to make the next one. Keep the recipe outside
the thing it produces.
