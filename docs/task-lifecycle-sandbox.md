# Task lifecycle in a sandbox, over REST

Mostly a sketch. *What is implemented* at the end says which parts of this
exist; the rest is the shape the change would take and the interfaces it
would have to fix.

`codex/default-python-state-machine` moves task lifecycle policy out of Go
and into a reducer: `pkg/statemachine`'s engine persists a snapshot and
dispatches effects, and a definition decides everything else. That branch
runs the definition as `python3 -I -c runner.py <task.py>` -- a fresh
process per event, on the daemon's own host, out of a directory grain pins
from the *config* repository (`pkg/statemachine/process.go`,
`definitions.go`, and `docs/reducer-isolation.md`, which is honest that the
ten-second deadline and the bounded output "are operational safeguards, not
a sandbox: code runs with the container's file, network, and environment
access").

This document changes two things about that and nothing else:

1. **Where the definition runs.** Not a subprocess of the controller but a
   long-lived HTTP server in one of grain's own sandboxes, reached over the
   same control link everything else in a sandbox is reached over. Grain
   calls `POST /v1/reduce` where it used to write a line of NDJSON to a
   pipe.
2. **Where the definition is declared and built.** Not `workflows/task.py`
   in the deployment's config repository but `.grain/lifecycle.yml` in the
   repository whose tasks it governs, beside `.grain/workflows` -- reviewed
   in the pull request that changes the thing it describes, branching with
   it, live when it merges.

The two are one change because neither is worth much alone. A REST reducer
still owned by the deployment's config repo buys isolation and nothing
else. A per-repo definition still executed as trusted Python inside the
controller is a repository every dispatched agent can edit choosing what
runs as the daemon.

## What does not change

Worth saying first, because it is most of the branch:

- `pkg/statemachine`'s engine, store, transition log, effect outbox,
  idempotency keys, per-instance locking, `reducer.failed` retry and
  compare-and-swap sequencing. Untouched.
- The `Reducer` interface: `Reduce(ctx, state, event) (Result, error)`.
  This change is one more implementation of it, and the one that replaces
  `ProcessReducer`.
- `Result` and its validation (`validateResult`): `state`, `effects`,
  `accepts`, `lifecycle`, `attention`, `presentation`, `inputs`, with the
  same tone/activity vocabularies and the same refusal of an input whose
  event is not in `accepts`.
- The effect catalogue and its executor. Effects are still grain's to
  perform; a definition still requests and never acts.
- The event envelope (CloudEvents-shaped), `task.observed`'s fact set,
  `timer.fired`, `reducer.failed`, and deduplication by instance, source
  and event id.
- `POST /api/tasks/{task_id}/inputs/{input_id}` and its
  sequence-precondition. A person clicking a button in the UI cannot tell
  that any of this moved.
- `Instance.Definition` stays a string whose shape nothing depends on, so
  no store migration: what is written into it changes, not its width. It
  becomes `owner/name@sha256:...`, or `owner/name@commit:<sha>` for a
  repository that declared no image. The repository is in it because
  `Definitions.Reducer` is handed a version and nothing else, and a bare
  digest cannot say whose declaration it belongs to or how to serve it.

## Where the declaration lives

```text
.grain/
  workflows/          # CI: many files, many workflows (pkg/ci)
  lifecycle.yml       # this: one file, one lifecycle
```

One file rather than a directory because a repository has exactly one
answer to "how do tasks here move", where it has as many checks as it
likes. And `lifecycle.yml` rather than anything with `workflow` in it
because `.grain/workflows` is already CI and has been since before this
existed: two directories a syllable apart, one holding checks and one
holding task policy, is a naming collision somebody would pay for every
time they read a stack trace. The reducer's own source goes wherever the
repository wants it; the declaration names the paths.

A repository with no `.grain/lifecycle.yml` gets the deployment's default
lifecycle, which is what every repository gets today.
### `.grain/lifecycle.yml`

```yaml
name: grain task lifecycle        # optional; prose for a listing

# The image the definition is served from. A name, not a digest: grain
# resolves it to the digest this repository's release job last pushed,
# and that digest is the pinned version. Constrained to this
# repository's own package namespace -- see below.
image: ghcr.io/acme/billing/grain-lifecycle

serve:
  run: python3 -m lifecycle.server
  working-directory: /opt/grain-lifecycle
  listen: /run/grain/lifecycle.sock    # optional; this is the default
  env:                                 # optional; literals only
    LIFECYCLE_LOG_LEVEL: info
  ready-timeout-seconds: 30            # optional; default 30
  request-timeout-ms: 5000             # optional; default 5000, max 30000
  sandbox:
    cpus: 1
    memory-mb: 1024
```

There is no `build:` block, and that is the whole of this design's
build story: see *The build is a release job* below.

`image:` is optional. Omitted, the definition is served from the stock
guest with the repository checked out at the pinned commit -- no registry,
no release job, no capability, and no dependency beyond what the guest
already carries. That is the path a first lifecycle should be written
against, and `image:` is what a repository adds when its reducer grows a
dependency.

Decoded with `KnownFields` set, like every other declaration grain reads
(`pkg/repodecl/parse.go`, `pkg/ci/yaml.go`): an unknown key is an error
naming the key, and a field that is deliberately absent is a refusal
somebody finds out about rather than a setting that quietly did nothing.

The fields that are absent on purpose, and would each want a `reserved`
entry so the error can say why: `capabilities`, `secrets`, `network` on
the serve sandbox, and anything naming another repository -- including an
`image:` outside this repository's own namespace. This is
`.grain/README.md`'s rule unchanged: a declaration says what work to do in
this repository and when, never what that work may touch, because every
agent grain dispatches here can rewrite it.

## The build is a release job

The build needs no new machinery in grain at all, because a sandbox guest
is already an OCI image and grain already has a way for a repository to
publish one.

The stock guest's own build shows the first half: the repository's
`Granulefile` ends in a `VM PACK`, and what that produces is an ordinary
OCI image -- granule, cloud-hypervisor and a bootable guest disk -- so
`docker run` on it boots a VM. That is why grain has one artifact here
rather than two: that image *is* the sandbox container a deployment runs.
A guest is a thing a registry can hold.

The second half is `package-publish` (`docs/ci-capabilities.md`), which
places this deployment's own GitHub credential for a repository at
`/home/debian/.package-publish-{registry,username,token}` -- and places it
"only for a trusted post-merge release or an operator-triggered
`workflow_dispatch` run. Pull-request and ordinary push runs cannot obtain
it, and an agent's task-scoped CI request remains an ordinary push. The
repository whose credential is resolved is the job's own, never one a
workflow file names."

Put those together and the lifecycle build is an ordinary
`.grain/workflows` job:

```yaml
# .grain/workflows/lifecycle.yml
name: lifecycle
on:
  release:                      # post-merge, trusted, grain-created
  workflow_dispatch:            # an operator rebuilding by hand
jobs:
  image:
    sandbox: {cpus: 4, memory-mb: 8192}
    timeout-minutes: 30
    cache: false                # a publish is not a verdict to remember
    steps:
      - name: build the guest
        run: |
          granule guest build \
            -f .grain/lifecycle.Dockerfile \
            -t "$GRAIN_LIFECYCLE_IMAGE:$GRAIN_COMMIT"
      - name: publish
        run: |
          docker login "$(cat ~/.package-publish-registry)" \
            -u "$(cat ~/.package-publish-username)" \
            --password-stdin < ~/.package-publish-token
          docker push "$GRAIN_LIFECYCLE_IMAGE:$GRAIN_COMMIT"
```

The Dockerfile is an ordinary one, with the base supplied by granule
rather than named in the file, and the subset it may use is
[granule's side](granule-guest-images.md). Nothing above is new except
`GRAIN_LIFECYCLE_IMAGE`, the `image:` from the declaration, which grain
supplies to a job in a repository that declares a lifecycle.

What this buys, against the alternatives that were considered and are
recorded in *Builds that were not chosen* below:

- **Grain never builds anything.** No builder, no artifact store, no
  build log surface, no cache of its own. A build is a CI job, with CI's
  fingerprint, CI's log, CI's sandbox, CI's live view.
- **No repository-owned code runs on the daemon host.** The obvious
  shortcut -- reuse the guest derivation `pkg/sandboximage` used to do
  for the (since retired) sandbox setup script -- was wrong for exactly
  one reason, and it was a decisive one: that derivation ran host-side,
  as root against the daemon's docker, driven by *operator* config.
  Pointing it at a file every dispatched agent can rewrite is the hole
  this whole change exists to close. (A deployment's own image is now an
  OCI reference built elsewhere, `model.Config.SandboxImage`, and the
  daemon builds nothing.)
- **The push is already gated where the version should change.**
  `package-publish` materializes only for a release, which is the commit
  at which a repository's lifecycle should change anyway. A pull request
  cannot publish a lifecycle image, so it cannot change how its own
  repository's tasks behave.
- **The namespace scoping is by construction.** The credential is the
  deployment's credential *for the job's own repository*, so it writes
  that repository's packages and no others. Repository A cannot poison
  repository B's lifecycle even by naming its image, because it cannot
  push to it. This is the one load-bearing security property here and it
  wants a test of its own, in `cmd/grain/ci_hostile_test.go`'s manner.

The nested guest build inside a CI sandbox is the cost: `granule guest
build` boots the guest to provision it, so the job needs `/dev/kvm` and
minutes rather than seconds. Grain's CI sandboxes have KVM -- measured on
this deployment on 16 September 2026 and written down in
`.grain/workflows/tests.yml` -- and the cost lands once per lifecycle
change, in CI, cached by the registry for every host in the deployment,
rather than at task-dispatch time. That same file records the other side
of this: nothing has published `guest:inputs-<key>` since 2026-09-16
because doing so "needs a GHCR push credential a grain CI job can reach".
That credential is `package-publish`, and this path is the one that makes
grain's own guest publishing possible too.

One thing not to oversell: this is *an* artifacts path, the image-shaped
one. It does not give grain CI artifacts in general -- `needs:` between
jobs still cannot pass outputs (`pkg/ci/yaml.go:109`), and a 2KB test
report is still a thing that dies with its sandbox.

## Pinning: the digest is the version

```text
version = owner/name @ the OCI digest of `image:` as this repository
          last published it
```

That is the whole rule, and it is better than any hash grain could
compute over declared source paths: a digest is a content hash of the
thing that actually runs, produced by the registry, covering the
interpreter and every dependency the build resolved rather than the
lockfile that was meant to pin them.

- Grain resolves `image:` to a digest after a successful release run for
  the repository -- a `docker manifest inspect` of the tag, which needs
  no CI feature that does not exist. There is no need for the job to
  report an output.
- A task pins that digest when its instance is created.
  `Instance.Definition` holds it as a string, prefixed with the
  repository it belongs to (`owner/name@sha256:...`), which the column
  already accommodates. Serving a version this host has not prepared is a
  refusal rather than a silent re-resolve: an in-flight task keeps its
  digest across releases, and handing it the definition the repository
  declares *now* would be changing a machine's rules underneath it.
- An in-flight task keeps its digest across releases and daemon
  restarts. The image is pulled and retained while any live instance
  pins it.
- If the release job fails, or the tag does not resolve, or the pulled
  image is refused (below), **nothing falls back**. The repository's
  tasks hold, with the error on the workflow, the way
  `sandboximage.Image` blocks rather than handing back the stock guest.
  A broken lifecycle build silently reverting a repository to the
  deployment default would mean a bad merge quietly changing what
  "approved" means there.

Two checks on the pulled image, both refusals rather than warnings:

- **It must carry a guest at `/var/lib/granule/guest/`,** which is the
  only part of it grain uses: the image is never booted, its guest
  directory is assembled onto this deployment's own container half, and
  an image with nothing there is refused on pull. A guest that is not
  really derived from the stock one is not refused at all -- it simply
  never answers its control link and times out. See *The wrinkle* below
  for why that is the right shape for this check, and why the base-label
  refusal an earlier draft proposed here was both unenforceable and
  harmful.
- **It must be in the repository's own namespace.** Checked when the
  declaration is read, not when the image is pulled, so the error
  arrives in the pull request that wrote it.

A deployment with neither `package-publish` nor `registry-push`
configured for a repository has no artifacts path, and so that repository
cannot declare a lifecycle at all. That is the same gate publishing
already has, and it should fail the same way: loudly, at declaration-read
time, naming the capability and the command that grants it, the way
`docs/ci-capabilities.md` describes a refused release job.

## Builds that were not chosen

Recorded because each was the obvious answer at some point, and the
reason each is wrong is the reason the release job is right.

| | Why not |
| --- | --- |
| A `build:` block in `.grain/lifecycle.yml`, run by a new builder in grain | Every part of it -- sandbox acquisition, step execution, logs, fingerprinting, caching -- is `pkg/ci` rewritten a second time and a worse one. |
| `pkg/sandboximage`, host-side, pointed at the repository's script | Fast and already written, and it runs repository-owned shell as root against the daemon's docker. Decisive. |
| A prepared tree (tar) streamed into a stock guest at serve time | Avoids nesting and needs no registry, at the price of inventing an artifact store, an artifact transport, and a build fingerprint -- all three of which the registry already is. Also cannot install a system package, since it never gets root in the guest. |
| A plain OCI *container* image rather than a guest | Far cheaper to build -- an ordinary `docker build`, no KVM, no nesting -- but it can only run on the container and k8s backends, and `ContainerSandboxes.IsolatesNetwork()` answers false: `NetworkNone` is a thing "only a real VM backend can give" (`pkg/model/network_access.go`). The serve sandbox having no network is the load-bearing half of this design's security argument, so the cheaper build buys a weaker promise. This is the trade most worth revisiting if guest build times become the problem. |
| No build at all -- stdlib only, on the stock guest | Still the right answer for a definition that needs no dependencies: omit `image:` and grain serves it from the stock guest with the repository checked out. No registry, no release job, no capability. Worth keeping as the zero-config path rather than as the only one, and it is what a first lifecycle should be written against. |

## The wrinkle: a repository's image is not a machine grain can trust

Resolved, by not treating it as one. The problem is stated first because
the shape of the answer follows from it.

Everything above is the first time grain boots a sandbox whose image was
chosen by repository content. Until now an image is either the stock guest
`cmd/grain/sandboximage.go` stamps into the binary, or a derivation of it
that `pkg/sandboximage` built host-side from an *operator's* script. "Is
this really a granule guest" has never been a question, because grain
either shipped the answer or built it.

It is a question now, and it does not have a check-shaped answer.

**A label cannot answer it.** `org.grain.guest.base=<digest>` is a string
the image supplies about itself. An image that is a plain container with
that label set is indistinguishable, from the outside, from a real guest.

**Layer provenance answers less than it looks like it does.** Grain could
require the image's layer digests to begin with the stock guest's, which
is not self-asserted -- layer digests are content hashes the registry
computes and the pull verifies -- and a fake cannot claim layers it does
not contain. But a `docker commit` layer is a whole filesystem diff, and
the top layer is the attacker's: it can replace `/usr/bin/granule`, the
entrypoint, and the guest disk itself. Deriving from the stock guest is
not behaving like it.

**And the consequence is not merely "a container instead of a VM".** On
the granule backend, `NetworkNone` is enforced *cooperatively, inside the
guest*: `enforceGuestNetwork` execs `ip route del default` over the
control link and then reads `/proc/net/route` back through the same link
(`pkg/orchestrator/granule_sandboxes.go:870-975`). The comment there is
careful that it reads the kernel's table "rather than trusting the tool
that just edited it" -- but tool and table alike arrive through
`granule exec`, which runs the image's own binary. An image that is not a
guest answers exit 0 with an empty route table and is handed a sandbox
that still has the namespace's network -- which under flat mode is the
docker segment carrying this deployment's git proxy and model proxies.
Grain logs that the promise was kept.

None of this is a bug today. `pkg/model/network_access.go` already says
what this mechanism is: "one narrowing of docs/design.md's egress_policy
rather than an airgap". A cooperative narrowing is a perfectly good
answer while every image is one the operator built. It stops being one at
exactly the moment this design introduces.

Worth noting the other backends differ: the k8s backend enforces
`NetworkNone` with a pod label and a NetworkPolicy, outside the image's
reach entirely (`k8s_sandboxes.go:270-313`), and the container backend
refuses `NetworkNone` outright rather than pretending
(`container_sandboxes.go:99`). Granule is the one where the enforcement is
inside the thing being constrained.

### The answer: grain assembles, the repository supplies a disk

A granule guest image has two halves, and grain already knows where the
seam is:

```dockerfile
COPY --from=granule /usr/local/bin/cloud-hypervisor /usr/local/bin/cloud-hypervisor
COPY --from=granule /usr/local/bin/granule           /usr/local/bin/granule
COPY --from=granule /var/lib/granule/guest/          /var/lib/granule/guest/
```

with the distinction between what each half is: "cloud-hypervisor is the VMM
itself; granule is what forks for `run`, `ready`, `exec` and `cp`;
and /var/lib/granule/guest is the disk, kernel and initramfs its
internal/config finds by default". Everything grain's isolation rests on
is in the first two lines. Everything a repository needs to customize is
in the third.

So grain does not boot the repository's image. It **assembles** one, from
its own container half and the repository's guest half:

```dockerfile
FROM <this deployment's stock guest image>
COPY --from=<the repository's published image> \
     /var/lib/granule/guest/disk.img       /var/lib/granule/guest/disk.img
COPY --from=<the repository's published image> \
     /var/lib/granule/guest/layer.*.qcow2  /var/lib/granule/guest/
```

The disk and the qcow2 layers stacked on it, and nothing else: the kernel
and initramfs beside them stay this deployment's. The layer convention,
and why its paths have to be exact, is
[granule's side](granule-guest-images.md).

No `RUN`. A `COPY --from` is a file copy out of a pulled layer, so
assembly executes no code from the repository's image -- which is what
makes it safe to do host-side, next to the daemon, where an earlier
draft's host-side *build* was not.

What this settles:

- **`/usr/bin/granule` is grain's.** The fake-VMM attack is gone at the
  root: grain's binary runs, cloud-hypervisor really boots a VM, and
  `granule exec` really SSHes to a guest. An image cannot substitute the
  thing that would have to lie.
- **The VM boundary is real**, because the hypervisor is grain's, not a
  claim the image makes.
- **"Is it a granule guest" answers itself, by function rather than by
  label.** A guest disk not derived from the stock guest has no
  `granule-control-net` service, so it has no route in under flat mode
  (`pkg/granule/granule.go`: "a guest image not built from that overlay has
  no route in at all under this mode"), so `granule exec` never answers,
  so readiness times out and the lifecycle is `failed`. A repository must
  genuinely derive from the stock guest or its definition never serves.
  That is the check, and it needed no checking.
- **A repository keeps root in its own guest.** `apt-get install` works,
  which the extract-a-tree alternative could not offer, and there is no
  ABI question and no companion userland image to publish: the guest was
  built from the stock guest, so it *is* the userland it runs on.
- **An upgrade does not strand anybody.** The container half is always
  this deployment's current one, so a granule or cloud-hypervisor fix
  reaches every repository's lifecycle without a single rebuild. This
  reverses what an earlier draft of *Pinning* said: refusing an image
  whose base no longer matches would have stranded every repository on
  every upgrade, to enforce something a label could not enforce anyway.

What it does not settle, stated plainly because the rest of this section
was about not overstating a check:

`enforceGuestNetwork` is still cooperative. Grain execs `ip route del
default` into the guest and reads `/proc/net/route` back, and both the
shell and the userland answering are the repository's. A hostile guest
can keep its default route and report success.

The difference is what that now costs. Before assembly, a forged image
put repository code in the *container's* namespace with the isolation
mechanism itself faked. After it, the worst case is a real VM that kept
the network an ordinary sandbox has -- `NetworkNone` silently degrading
to `NetworkOpen`, which is the default every dispatched agent task
already runs with, on the same segment, reaching the same git proxy and
model proxies behind the same authentication. The failure is a promise
unkept, not a boundary crossed, and it lands where grain already stands.
`.grain/README.md`'s rule holds: a repository cannot grant itself
anything it did not already have.

Which makes hardening `enforceGuestNetwork` -- applying it from the host
in the netns holder's namespace, before the VM container starts, where no
guest has a say -- worth doing on its own merits and no longer this
design's prerequisite. It is cooperative today for every sandbox grain
runs, and `pkg/model/network_access.go` already says so: "one narrowing
of docs/design.md's egress_policy rather than an airgap."

### What this needs from granule

The assembly above is grain's, and works against granule as it stands. The
rest of the image story is granule's, and is designed separately in
[customizing and running granule guest images](granule-guest-images.md) --
split out because "a guest image somebody other than the operator
customized, run on a VMM half the operator controls" is a shape grain will
want again, and because the work belongs on granule's `main` rather than in
this repository (`third_party/granule/VENDORED.md`).

What this design assumes that document delivers:

- **A rootfs image as the published artifact**, which an ordinary
  Dockerfile build produces: dependencies installed, source copied, a unit
  enabled, none of which needs a booted machine. Grain then packs the disk
  itself, on pull and again on every upgrade, which is what makes the
  kernel, the overlay and the agent binaries this deployment's current
  ones rather than whatever a repository built against months ago. A
  repository that genuinely needs a running machine at build time takes
  granule's boot phase, publishes a packed guest, and grain copies its disk
  rather than building one -- but nothing a reducer image needs is in that
  category.
- **A Dockerfile as the build input**, so a repository writes the format
  it already knows, the published artifact is an ordinary OCI image whose
  layers the registry already dedupes, and the build needs no KVM and no
  nested VM boot. The disk becomes a local artifact grain packs on pull
  and repacks on upgrade, rather than something published, pulled and
  retained per version. Until that exists this design works and is simply
  expensive: `granule guest build -setup` boots a VM and commits a
  squashed disk.
- **A declared guest-contract version, and a small contract.** Grain's
  upgrade moves the container half for every deployment at once and does
  not move a guest a repository built six weeks ago, which is the point --
  a granule fix reaches every lifecycle without a rebuild -- and which
  deliberately breaks the invariant granule relies on today, that
  `granule` and its guest agree "because they are the same files".
  Grain's own addition to that guest surface is one static binary
  (`grain-connect`), and should stay that.

None of it is a prerequisite for the rest of this document. A first
implementation can publish squashed disks and pay for them.

## Transport: how grain reaches the sandbox

This is the part with a real constraint in it, and it is worth stating
before the API, because it decides the API's shape.

Grain has no route to a sandbox's guest. `pkg/granule`'s package comment is
explicit that it "never resolves an address for a VM, because nothing here
needs one": the Port, PodIP and DockerPodIP it used to carry all existed to
describe a route in from outside the VM's network namespace and went away
with the SSH-to-a-forwarded-port transport. A guest is reached by exec'ing
into its own container -- `docker exec <granule-vm-name> granule exec -- argv`
(`pkg/mcp/docker_exec_runner.go`), `kubectl exec` under the k8s backend,
`docker exec` under the container backend. Under `NetModeFlat` the guest
answers to the namespace's own address, so there is not an address to dial
even from inside.

So "listens on a REST interface" cannot mean "grain connects to a port".
It means:

- The definition server binds a **unix socket inside the guest**,
  `/run/grain/lifecycle.sock` by default. Nothing outside the guest can
  reach it, nothing needs to be assigned, and no port has to be discovered
  or forwarded. (`listen:` may name `127.0.0.1:<port>` instead for a
  runtime that cannot bind a unix socket; grain dials either the same
  way.)
- Grain dials it by exec'ing a one-connection forwarder in the guest and
  treating that exec's stdio as a `net.Conn`:

```go
// One sandbox's HTTP client. The exec is the dial.
transport := &http.Transport{
    DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
        return sandbox.DialStream(ctx, []string{"grain-connect", listen})
    },
    MaxConnsPerHost:     maxInFlight,
    IdleConnTimeout:     idleTTL,
    DisableCompression:  true,
}
```

`DialStream` is the new seam: one method on the sandbox backends, wrapping
what `RunStream` already does with a command's two pipes
(`pkg/mcp/exec_stream.go` has the half that serializes them) into a
`net.Conn` whose Read is the exec's stdout and whose Write is its stdin.
Every backend can implement it, because every backend already has an exec.

`grain-connect` is a few lines of Go -- copy stdin to the socket, socket to
stdout -- and it is **grain's**, installed in the stock sandbox guest by
the repository's `Granulefile`, not something a declaration provides.

`grain-connect` is also provisional. Grain has decided to adopt E2B's
`envd` as its guest agent ([the adoption design](envd-adoption.md)), and
envd's own process API is reached over exactly this dial, so the forwarder
becomes one more thing envd provides rather than something grain ships.
Nothing in this document changes when that happens: the definition still
serves HTTP on a unix socket, and which side of that socket grain arrives
from is not something a repository writing a reducer can tell.

Note which half of the assembled image each end of this comes from
(*The wrinkle*). `granule exec` is the container half's, so grain's, on
every sandbox it will ever build. `grain-connect` is in the *guest*, so
it arrives in a lifecycle sandbox only because the repository derived its
guest from the stock one -- which is also the only way its control link
works at all. The two travel together, which is why there is nothing
extra to verify: a guest that could not serve `grain-connect` is a guest
grain could not have reached in the first place.

Which is why a repository builds its guest from the stock one rather than
from a base of its own. An image built `FROM` anything else is an image
with no route in at all: no control link, no forwarder, nothing for
`DialStream` to exec. That is a constraint rather than a policy grain
could relax by being more permissive -- it is the difference between a
sandbox grain can talk to and one it cannot. Letting a repository bring a
base of its own means giving grain a way to inject the forwarder into an
arbitrary image, which is a change to the sandbox backends rather than to
a declaration.

And it is a constraint that enforces itself, which is the point of *The
wrinkle*: there is no base-label check here, because a label is a string
the image supplies about itself and a guest that is not really derived
from the stock one simply never answers. An earlier draft of this
paragraph claimed such a refusal; it would have been both unenforceable
and harmful, since it would have stranded every repository on every
upgrade.

HTTP over a pipe, rather than a bespoke framing on the same pipe, buys the
things the branch's NDJSON adapter had to hand-roll: request/response
correlation, content length, status codes, timeouts, and a definition
author writing against a web framework they already know instead of
against "write exactly one line of JSON to stdout and print your
diagnostics to stderr".

## The REST contract

What the repository's server implements. HTTP/1.1 over the socket above;
`Content-Type: application/json` both ways; UTF-8; no redirects.

Auth is a bearer token grain generates per sandbox and passes as
`GRAIN_LIFECYCLE_TOKEN` in the serve command's environment. The server must
compare it in constant time and refuse anything else with 401. The socket
is already inside a sandbox with no network, so this is a thin boundary --
its job is to keep something *else* that ends up in the guest from driving
the reducer, not to be the isolation.

### `GET /v1/health`

Readiness, polled by grain after start and before the first reduce, and
again on a schedule while the sandbox is held.

```json
{"protocol": "1", "ready": true, "definition": "acme task lifecycle v4"}
```

`protocol` is the contract version and must be `"1"`. `definition` is free
prose for operators and logs; grain never parses it. 200 with
`ready: true` is ready; anything else, including a connection refused
while the process is still starting, is not yet, until
`ready-timeout-seconds` runs out.

### `POST /v1/reduce`

The whole of the job.

Request:

```json
{
  "id": "observation-12",
  "instance": "123",
  "sequence": 11,
  "state": {"phase": "waiting_for_ci"},
  "event": {
    "specversion": "1.0",
    "id": "observation-12",
    "source": "grain://task-observer",
    "type": "task.observed",
    "subject": "123",
    "time": "2026-09-13T12:00:00Z",
    "datacontenttype": "application/json",
    "data": {"approved": true, "has_pull_request": true, "active_run": ""}
  }
}
```

Headers, all of which duplicate something in the body so a handler can
route and log without parsing it, and none of which are authoritative:
`Grain-Instance`, `Grain-Sequence`, `Grain-Event-Id`, `Grain-Deadline-Ms`,
and `Idempotency-Key` -- the SHA-256 of the engine's own
`instance\x00sequence\x00event-id`, hashed rather than carried literally
because that triple is NUL-separated and a NUL cannot go in a header. It
is the same construction the effect outbox already uses for its keys, for
the same reason it uses it: what a key has to be is stable and distinct,
not readable.

Response, 200:

```json
{
  "id": "observation-12",
  "state": {"phase": "ready_to_submit"},
  "effects": [{"type": "task.submit", "data": {}}],
  "accepts": ["task.observed", "submission.cancelled"],
  "lifecycle": "active",
  "attention": false,
  "presentation": {
    "label": "Ready to submit",
    "description": "CI is green on the third attempt",
    "tone": "success",
    "activity": "idle"
  },
  "inputs": [{
    "id": "cancel_submission",
    "kind": "confirm",
    "label": "Cancel submission",
    "event": "submission.cancelled"
  }]
}
```

Identical to the branch's NDJSON response, `id` echo included, and
validated by the same `validateResult`. `id` must equal the request's; a
mismatch is a reducer failure, which is what the process adapter already
does and is the cheapest possible check that a server has not confused two
requests.

What the server must guarantee, and what a definition author has to be
told in as many words:

- **Pure.** `state` is the entire memory. Nothing that survives a request
  may affect an answer -- not a module global, not a file, not a cache.
  Grain is free to start a second sandbox on the same version and route
  the next request to it.
- **Idempotent.** The same `Idempotency-Key` may arrive twice, after a
  crash between the answer and the commit, and must produce the same
  answer. Since the function is pure, this comes free -- unless it is not.
- **No clock.** Use `event.time`, not `now()`. Deadlines are expressed as
  `timer.schedule` effects, which grain fires back as `timer.fired`.
- **No side effects.** There is no network, and the filesystem dies with
  the sandbox. Every outward action is an effect in the response.
- **Bounded.** 8 MiB of response, matching `defaultMaxResponseBytes`.
  Larger is a reducer failure, not a truncation.
- **Concurrent.** One sandbox serves every task in its repository on its
  version. Grain serializes per instance and does not serialize across
  them.

### Errors

| What grain sees | What grain does |
| --- | --- |
| 200, valid body | Commit the transition, dispatch effects. |
| 200, body fails `validateResult` | Reducer failure: `reducer.failed` back to the definition once, against the last committed state. |
| 400, 422 | Reducer failure, with the body's `error` string as the diagnostic. |
| 401, 404, 405 | Definition is not serving the contract. Held as a lifecycle error on the repository, not charged to the event; no `reducer.failed`. |
| 429, `Retry-After` | Not a failure. Back off and resubmit the same event; the engine's deduplication makes that safe. |
| 500-599 | Reducer failure. |
| Timeout at `request-timeout-ms` | Reducer failure. The connection is dropped; a handler still running dies with the request. |
| Transport failure (exec died, sandbox gone) | Not a reducer failure. Restart the sandbox and resubmit; the observer retries on a later cycle if that fails too. |

The distinction in the last row is the one that matters and the one the
process adapter could not draw: "the definition said something wrong" is
the definition's problem and belongs in `reducer.failed`, where a
definition can react to its own bug. "The machine went away" is grain's
problem and must not consume the event or move the snapshot.

An error body is `{"error": "human-readable"}`, surfaced on the workflow's
public `error` field, which the branch already exposes independently of the
committed presentation.

## Grain's own API

Additions to `pkg/ui`, all operator-facing. Nothing here is reachable by a
definition.

```text
GET  /api/repos/{owner}/{name}/lifecycle
POST /api/repos/{owner}/{name}/lifecycle/refresh
```

```json
{
  "declared": true,
  "image": "ghcr.io/acme/billing/grain-lifecycle",
  "version": "sha256:9f2c...e1",
  "state": "ready",
  "resolved_at": "2026-09-16T09:12:00Z",
  "release_run": "run_01J...",
  "sandbox": {"state": "serving", "since": "2026-09-17T02:00:00Z", "in_flight": 0},
  "error": "",
  "instances": {"pinned_here": 14, "on_older_versions": 3}
}
```

`state` is one of `none` (no declaration; the deployment default applies),
`unpublished` (declared, but the tag has never resolved), `pulling`,
`ready`, `refused` (base-label mismatch, or an image outside the
repository's namespace) and `failed`.

There is deliberately no build endpoint and no build log here. A build is
a release run, so it is already an object with a log and a live view
(`pkg/ci/live.go`), reachable where every other run of that repository is,
and `release_run` points at it. `POST .../refresh` only re-resolves the
tag and pulls -- what an operator needs after publishing an image out of
band. Rebuilding is
`POST /api/repos/{owner}/{name}/release-runs`, which already exists and is
already "the one caller Grain trusts to say `release` besides its own
merge reconciler" (`docs/ci-capabilities.md`).

On a task, the existing `workflow` object gains the repository's version
and the health of the sandbox serving it, beside the sequence and view
fields it already carries. It still does not expose private state.

## The Go seam

Small, which is the argument for it:

```go
// pkg/statemachine/http.go -- beside ProcessReducer, same interface.
type HTTPReducer struct {
    Client   *http.Client   // its transport is one sandbox's exec dialer
    Endpoint string         // "http://lifecycle/v1/reduce"; the host is a label
    Token    string
    Timeout  time.Duration
    MaxBytes int64
}

func (r HTTPReducer) Reduce(ctx context.Context, state json.RawMessage, event Event) (Result, error)
```

```go
// The Definitions implementation the engine already takes, keyed by repo.
type RepoLifecycles struct { /* declarations, resolved digests, sandbox pool */ }

func (l *RepoLifecycles) Reducer(ctx context.Context, version string) (Reducer, error)
```

`Reducer(ctx, version)` acquiring a warm sandbox is why that method takes a
context and returns an error today. `Engine.Create` already calls it to
validate a version before creating an instance, which becomes a real
readiness check rather than an `os.Stat`.

Deleted: `pkg/statemachine/process.go`, `python/runner.py`,
`definitions.go`, `default.go`, `defaults/task.py`, and
`Store.EnableTaskWorkflows`'s `source` argument pointing at the config
repo's `workflows/` directory. Python leaves the controller image with
them.

The deployment default becomes a **Go** reducer compiled into the daemon --
the same `Reducer` interface, the same `Result`, the bundled `task.py`'s
policy transliterated. Not an image grain has to build and not a language
runtime the controller has to carry. The extensibility the branch was
after now lives entirely in the per-repository declaration, where it is
isolated; a deployment that wants different default behaviour for a
repository writes `.grain/lifecycle.yml` for it.

## Trust, and what the sandboxes get

Two sandboxes with deliberately different shapes:

| | Build (a release job) | Serve |
| --- | --- | --- |
| Checkout | This repository, at the merged commit, read-only | None |
| Network | Open -- it has to fetch dependencies | `none` |
| Capabilities | `package-publish` or `registry-push`, if an operator granted one | None |
| Credentials | That publishing login, and nothing else | None |
| Lives | One job | Idle TTL, or until its version is superseded |
| Inbound | Nothing | Grain's exec-dialed socket, one token |

The build is an ordinary CI job and gets exactly what CI already gives one,
under the rule `.grain/README.md` already states: a workflow can spend
sandbox time and outbound network, in a sandbox where an agent that could
have edited the file was already running whatever it liked, and it cannot
widen its own grants. The publishing credential is the single exception,
and it is one that predates this design: operator-granted per repository,
materialized only for a trusted post-merge release, and scoped to the
job's own repository's packages.

The serve sandbox is the interesting one, and it is strictly better than
what it replaces. Today's reducer runs in the controller's container with
the controller's filesystem, network and environment -- next to the
deployment's credentials -- and `docs/reducer-isolation.md` defers the
seccomp work that would narrow it. Here the same code runs with
`NetworkAccess: none` (`pkg/model/network_access.go`: no package index, no
API, no repository host except through grain's git proxy), no checkout, no
capability material, and no inbound route but one unix socket grain exec's
its way to. The reducer cannot reach the daemon, the store, the state
repository or GitHub, because it cannot reach anything. Which is also what
makes it safe for the definition to be a file in a repository every
dispatched agent can rewrite.

That does leave the publishing credential as the widest thing in reach of
a file the repository writes, and it is worth being plain about what that
is and is not. A release job can push whatever image it likes to its own
repository's packages, and grain will boot it -- which is the same
repository whose dispatched agents already run arbitrary code, so it is
not a new capability. What it must not become is a route to *another*
repository's lifecycle, and it is not one: the credential resolved is "the
job's own, never one a workflow file names" (`docs/ci-capabilities.md`),
and an `image:` outside the declaring repository's namespace is refused
when the declaration is read. Those two together are the load-bearing
security property of this design, and the pair of tests worth writing
first.

## Lifetime and cold start

One serving sandbox per `(repo, version)`, shared by every task of that
repository on that version. Started on the first reduce that needs it, or
eagerly when the repository's version is built. Released after an idle TTL
(15 minutes, say) and started again on the next reduce.

Cold start is the cost this design adds and the branch's design does not
have: a fresh `python3` is tens of milliseconds and a fresh sandbox is
seconds to tens of seconds. It falls on the first event after a quiet
period -- which is very often a person clicking a button and watching --
so it is worth handling rather than absorbing:

- Hold the sandbox while any instance on that version is `active` with
  `attention` set, since that is precisely the state a person is about to
  act on.
- Start the sandbox at the moment a task is dispatched, in parallel with
  the run's own sandbox, rather than at the first observation.
- Keep the idle TTL a setting, since a deployment with fifty repositories
  and one with three want different answers.

Concurrency is capped per sandbox (`MaxConnsPerHost`), with anything beyond
it queued behind the cap rather than opening a new exec per request. A
sandbox that stops answering is replaced, not retried in place.

## Failure modes

| | Effect |
| --- | --- |
| Declaration does not parse | Repository's lifecycle is `failed`, with the parse error naming the file and key. Pinned instances keep serving on their last good version; new tasks do not get created against a version that does not exist. |
| Release job fails, or the tag never resolves | Lifecycle is `unpublished` or `failed`, with the release run linked. No fallback to the deployment default -- see *Pinning*. |
| Image has no `/var/lib/granule/guest/disk.img`, or is outside the repository's namespace | `refused` on pull or on declaration read, naming which check failed and what would fix it. There is no base-label check to fail (*The wrinkle*). |
| A qcow2 layer's backing file does not resolve in the assembled image | `refused` before the first boot, rather than discovered as a guest that will not start. Walk the chain at assembly and check every backing path exists; this is what the fixed naming convention is for. |
| Guest declares a control-link version the container half does not speak | `refused`, naming both versions. Not a warning: a skewed control link is a sandbox grain cannot reach and must not pretend to have. |
| Assembled guest never answers its control link | The readiness timeout, reported as a lifecycle failure naming it. This is also what a guest not really derived from the stock one looks like, and the two want one message that names both possibilities. |
| Registry unreachable | Not a lifecycle failure while the image is already pulled and any instance pins it. A version that has never been pulled holds, and retries. |
| Server never becomes ready | Lifecycle error naming the `ready-timeout-seconds` and the serve command's last output. No event is consumed. A definition image should have been through granule's `verify` before this point, so that a guest which cannot boot at all is refused with a verdict rather than discovered as a timeout ([granule's side](granule-guest-images.md)). |
| Server dies mid-reduce | Transport failure, not a reducer failure. Sandbox replaced, event resubmitted, snapshot unmoved. |
| Definition returns garbage | `reducer.failed`, once, against the last committed state, exactly as today. |
| Repository deletes `.grain/lifecycle.yml` | Existing instances keep their pinned digest and their pulled image; new tasks use the deployment default. |
| Local image pruned while an instance pins it | Re-pulled by digest, which is why the digest rather than the tag is what an instance holds. If the registry no longer has it, the task is stranded: local retention must keep every digest any live instance pins, and the registry's retention policy is now something a deployment has to think about. |
| Host restart | Instances reload from the store; sandboxes are gone and started again on demand; the outbox redelivers unacknowledged effects by key, unchanged. |


## What is implemented

Kept here rather than left to be discovered, because this document is
longer than what exists and somebody picking it up should know where the
edge is. It grows as the pieces land.

- **The declaration.** `.grain/lifecycle.yml`, read by `pkg/repodecl`'s
  `LoadLifecycle` into `model.LifecycleDeclaration`: `KnownFields`, the
  defaults above, and every refusal this document names -- the reserved
  keys, the `build:` block, a `request-timeout-ms` over the ceiling, a
  `listen:` that is neither a socket nor loopback, and an `image:` outside
  the repository's own package namespace
  (`model.ValidateLifecycleImage`). `GRAIN_LIFECYCLE_IMAGE` reaches a CI
  job in a repository that declares a lifecycle, filled from the
  declaration and never from a workflow file.

- **The engine's side of the contract.** `Result` carries `lifecycle`,
  `attention`, `presentation` and `inputs`, and `validateResult` holds
  them to their vocabularies and refuses an input whose event is not in
  `accepts`. What the engine knows about a call that `Reduce`'s signature
  does not carry -- which instance, at which sequence -- travels beside it
  as a `ReduceContext`. And `ErrReducerUnavailable` is the row of the
  error table below that matters: an unreachable reducer is not charged to
  the event, so the snapshot does not move and there is no
  `reducer.failed`.

- **The REST contract.** `statemachine.HTTPReducer`: `GET /v1/health`,
  `POST /v1/reduce`, the headers, the id echo, the 8 MiB bound, and every
  row of the error table below. `ProcessReducer`, the
  subprocess-per-event adapter it replaces, is gone.

- **The image.** `pkg/lifecycle`'s `DockerImages` resolves the release tag
  to a digest, opens the repository's image without running any of it
  (`docker create` starts nothing and `docker cp` reads a filesystem),
  keeps the guest disk and its qcow2 chain and nothing else, and refuses
  an image with no guest in it or a chain with a hole in it. The container
  half is always this deployment's current one, which is what makes an
  upgrade reach every repository's lifecycle without a rebuild -- and what
  makes the assembled images accumulate, since every upgrade names each
  repository's image afresh. So an assembly is labelled with the lifecycle
  image and digest it came from (`grain.lifecycle.image`,
  `grain.lifecycle.digest`) and, once it succeeds, removes that lifecycle
  image's other assemblies: the retired `sandboximage.Customizer.pruneOthers`' shape,
  best effort, with docker's refusal to remove an image a container was
  created from being the right answer for one a sandbox is still serving
  from. The repository image the assembly was pulled from is not pruned
  with it -- that is somebody else's name, removable only by digest, and
  wants more care than grain's own tags do.

- **The registry, and the operator API over it.** `pkg/lifecycle`'s
  `Registry` is this document's `RepoLifecycles`: resolve, pin, one
  serving sandbox per `(repo, version)`, readiness polling, the idle TTL,
  the attention hold, prewarm, and every failure holding rather than
  falling back. `GET /api/repos/{owner}/{name}/lifecycle` and `POST
  .../lifecycle/refresh` report it, and answer `none` for every repository
  on a deployment that has no registry wired up.

- **The serving sandbox.** `pkg/orchestrator`'s `ServingSandboxes`, which
  only the granule backend implements: it boots an image named per sandbox
  rather than per deployment, withholds the network whatever was asked for,
  runs the serve command as a transient systemd unit so it outlives the
  exec that started it, and hands back the exec-dialed connection. A
  readiness timeout now carries what the server wrote, out of the guest's
  journal, which is usually the whole answer. `CannotServe` says why the
  other backends cannot -- the container one refuses `NetworkNone` rather
  than pretending, and an assembled granule guest is not a pod image.

  Nothing in it has been run against a real guest: a boot needs `/dev/kvm`,
  which neither this repository's Actions runners nor the environment it
  was written in has. What is covered is the invocation, the image
  override, the refusals and the seam -- the places a mistake would
  otherwise be found at dispatch. The first exercise against a real host is
  the cheapest possible one: resolve, serve, `GET /v1/health`, release.

What is left, and what each piece needs:
- **Daemon wiring.** Nothing constructs a `Registry`, so nothing passes one
  to an engine or to `pkg/ui`'s `Client.Lifecycles`, and
  `ci.Request.LifecycleImage` is filled by no caller. The adapter between
  the two halves is `ServeEnvironment` and a `ServeSpec` built from a
  `ServeRequest`: `orchestrator.ServingSandbox` already satisfies
  `lifecycle.Serving` structurally, which a test pins, so neither package
  imports the other.
- **Task lifecycle as a reducer at all.** `pkg/statemachine` is still a
  standalone engine: no task creates an instance, no observer submits
  `task.observed`, the effect catalogue and its executor do not exist here,
  and the store has no columns for `presentation`, `lifecycle`,
  `attention` or `inputs`. The deployment default becoming a Go reducer is
  part of that work rather than part of this, and it is the only part that
  changes how a repository declaring nothing behaves.
- **Everything under *What this needs from granule*.** Unchanged: a first
  implementation publishes squashed disks and pays for them.

## Open questions

- **A lifecycle now depends on grain CI being on for the repository.**
  This is the one real cost of building in a release job rather than in
  grain, and it should be stated in the declaration's own error rather
  than discovered: `grain-ci` is a capability an operator attaches per
  repository, and with it off there is no release run, so there is no
  image and the repository's tasks cannot move to a declared lifecycle at
  all. Declaring a lifecycle therefore requires two operator grants
  (`grain-ci`, and one of `package-publish`/`registry-push`) plus a
  registry. Acceptable -- it is the same gate publishing already has --
  but it means a repository cannot adopt this on its own, which the
  earlier grain-side build would have allowed.
- **How is a lifecycle change tested before it merges?** A pull request
  cannot publish, which is exactly what makes the release gate worth
  having, and it also means a change to a repository's lifecycle is
  first exercised on the tasks of the repository it just merged into. A
  task cannot test its own lifecycle change either: it is governed by
  the digest it pinned at creation. The cheap answer is replay rather
  than a live instance -- a recorded event stream posted at a
  `workflow_dispatch`-built image's `/v1/reduce`, with the snapshots
  diffed -- and it needs grain to accept a digest for replay that it
  would never pin an instance to.
- **`registry-push` is not scoped the way `package-publish` is.**
  `package-publish` resolves the deployment's credential for the job's
  own repository, so it can only write that repository's packages, and
  the namespace rule holds by construction. `registry-push` is a JSON
  secret an operator pasted, whose token may well be able to write
  anywhere in that registry -- in which case repository A's release job
  could publish into repository B's lifecycle namespace, and grain would
  resolve and boot it. The declaration-read namespace check does not
  catch that, because it checks what B *declares*, not who pushed. Either
  `registry-push` is not a valid lifecycle credential, or the digest has
  to be recorded from the release run that produced it rather than
  resolved from the tag afterwards. The second is better and costs job
  outputs, which grain CI does not have.
- **Is one version per repository enough?** A repository with two very
  different kinds of task -- a release sweep and an agent task -- might
  want two definitions and a way to say which a task gets. Deferred:
  `Instance.Definition` can already hold anything, so this is a
  declaration question, not an engine one.
- **Cold start against timers.** A `timer.fired` at 03:00 for a repository
  nobody has touched since 18:00 pays a full sandbox boot. Acceptable for
  a timer; the question is whether a deployment with many repositories
  wants them batched.
- **Guest build time.** `granule guest build` boots a VM to provision
  it: KVM, and minutes. The full disk copy per version is the part the
  qcow chain is meant to retire, and that needs granule to offer an
  overlay-mode build whose overlay is the artifact
  ([granule's side](granule-guest-images.md)). The
  cheap alternatives both cost something this design would rather keep --
  a container serve sandbox gives up `NetworkNone`, and extracting a tree
  into a stock guest gives up `apt-get` and needs a published userland
  image to build against. See *Builds that were not chosen*.
- **Hardening `enforceGuestNetwork`.** Worth doing, no longer a
  prerequisite (*The wrinkle*). Applying `NetworkNone` from the host in
  the netns holder's namespace, before the VM container starts, would
  make it a promise rather than a report, for every sandbox grain runs
  and not only these. Whoever picks it up should know it is the one place
  where this design's worst case still differs from its intent.
- **Pull cost on a multi-host deployment.** A guest image is hundreds of
  megabytes, and every host that might serve a repository's lifecycle
  pays for the first one. Pre-pull on resolve, and block rather than fall
  back, the way a sandbox image build already does.
- **Definition-owned genesis.** Today Go creates an instance with `null`
  state and `["task.observed"]`. A `POST /v1/init` would let the
  definition own its own first state. Cheap to add, easy to defer, and
  not obviously worth a second endpoint when an `instance.created` event
  through `/v1/reduce` would do the same job.
