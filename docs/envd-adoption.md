# Adopting envd as grain's guest agent

**Decided.** Grain's sandboxes get [E2B's
`envd`](https://github.com/e2b-dev/runtime) as the agent inside the
guest, replacing the exec-a-command-and-collect-its-output surface every
sandbox operation is built on today. granule's own agent keeps the VM's
lifecycle; envd takes everything about the workload.

This is a sketch of the shape and the order, not a plan with dates.
Nothing here is implemented.

It came out of [the task lifecycle design](task-lifecycle-sandbox.md) and
is deliberately not part of it: that design needs a byte pipe to a unix
socket and does not care what provides it, so it can ship against a
forwarder of grain's own and move to this later without redesign. This
change, by contrast, touches four sandbox backends, the MCP tool surface,
credential placement, console sessions and `pkg/ci`'s own `Sandbox`
interface. Two things that arrive together arrive late.

The granule-side consequences are in [customizing and running granule guest
images](granule-guest-images.md), which is where the guest contract and
its version live.

## What grain has today

One verb. `pkg/mcp`'s `remoteRunner` is the whole guest-facing API:

```go
Run(ctx context.Context, argv []string, stdin string) (stdout, stderr string, exitCode int)
```

with a `RunStream` variant that additionally tees both streams into one
writer (`exec_stream.go` exists to serialize them, because `os/exec`
drains stdout and stderr on separate goroutines).

Everything else is composed from it:

- **File operations are shell commands.** `ssh_tools.go` implements
  `read_file`, `write_file` and `edit_file` by sending `cat`, `dd` and
  `mkdir`, with `shellquote.go` to make that safe.
- **Credential placement is a three-command dance.**
  `PlaceFileOverSSH` runs `mkdir -p`, then `install -m <mode> /dev/null
  <path>`, then writes. The `install` is not a stylistic choice: a `dd`
  that creates the file under the login user's umask "leaves the file
  world-readable for as long as it takes the next command to run", and
  everything placed this way is credential material.
- **CI steps are the same verb again.** `ci.Sandbox.Exec`, with
  `StreamingSandbox` for the live log, over `orchestrator.SandboxShell`'s
  `RunShell` and its shared `shellScript` composing working-directory and
  environment under `set -e`.
- **Four backends implement it**: `DockerExecRunner` (a granule VM, via
  `docker exec … granule exec`), `KubectlExecRunner`, `ContainerExecRunner`,
  and a plain host directory that has no VM at all.

The cost of one verb is visible in the interfaces around it.
`rootedSandbox` exists because a host-directory sandbox has a filesystem
the daemon can write to and a granule VM does not -- its comment says the
VM's "filesystem is inside a guest this process can only reach by exec'ing
into it" -- so placement has two implementations that must agree.

## What envd is

A daemon inside the guest exposing processes, PTYs, filesystem
operations, watchers and port forwarding over Connect RPC and REST.
Apache-2.0. Relevant facts, read from `packages/envd/main.go` rather than
from documentation:

| | |
| --- | --- |
| `-port` | Listen port, default 49983. |
| `-isnotfc` | Skip the Firecracker specifics: MMDS polling and the HTTP log exporter. This is what makes it run anywhere. |
| `-no-cgroups`, `-cgroup-root` | Cgroup management, disableable. |
| `-resume-handover` | Internal, for re-adopting processes across a live upgrade of envd itself. |
| Bind address | `0.0.0.0`. **Not a flag.** See below. |
| Auth | Bootstrapped by the first authenticated `/init`; requests then carry the token. Upgrades are refused until initialized. |

It is demonstrably liftable out of E2B's own stack: the Kubernetes SIG
agent-sandbox project builds it from upstream and runs it in an ordinary
container, no KVM.

## Transport: nothing about networking changes

This was got wrong twice while working through it, so it is worth stating
positively.

envd listens on a TCP port *inside the guest*. The client-proxy to
orchestrator-proxy chain in front of it is E2B's deployment choice, not
envd's requirement. Grain reaches a guest by exec'ing into its container
already, so it dials `127.0.0.1:49983` over that same pipe: a `net.Conn`
backed by an exec's stdio, behind an ordinary `http.Transport`.

```go
transport := &http.Transport{
    DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
        return sandbox.DialStream(ctx, []string{"nc", "127.0.0.1", "49983"})
    },
}
```

`DialStream` is the same seam the lifecycle design already needed. So:
no netshim change, no DNAT, no per-VM port assignment, no route into a
guest, and `pkg/granule`'s "never resolves an address for a VM" is
untouched. envd *replaces* the forwarder that design was going to carry
rather than sitting behind a second one.

### The one upstream change that has to land first

envd binds `0.0.0.0` and only the port is configurable. Under
`NetModeFlat` a guest is spliced onto the segment the container runtime
put its namespace on -- which for grain carries the git proxy, the model
proxies and every other sandbox on that host. A process-execution and
filesystem API listening there, gated only by a bearer token, is not
something to ship.

The fix is a bind-address flag, so it can be held to loopback or to the
control link. Small, obviously correct, and Apache-2.0.

**It goes upstream before anything here depends on it**, and is not
carried as a local patch. `third_party/granule/VENDORED.md` states the
discipline for exactly this situation -- a change grain needs "belongs on
[upstream's] `main` and reaches here by a resync, not by being applied to
this copy" -- and the reasoning transfers unchanged. A local patch to a
security-relevant bind address is the worst possible thing to carry out of
tree.

Until it lands, nothing is blocked except deployment: the adapter work
below can be built and tested against a loopback-only guest.

## The split with granule's agent

envd does not replace granule's agent, and pretending otherwise would grow
the guest surface by accident. The boundary should be drawn on purpose:

| | Owner |
| --- | --- |
| vsock, readiness declaration, graceful shutdown | granule-agent |
| memory and CPU hotplug | granule-mem-agent |
| the control-net link that makes the guest reachable at all | granule's overlay |
| running processes, PTYs | envd |
| reading, writing, watching files | envd |

The principle: **granule's agent owns the machine, envd owns the
workload.** A guest that boots, is reachable and can be resized is
granule's business and has to work before envd is even running. What
happens inside it afterwards is envd's.

That is still a larger guest surface than today, which is in tension with
keeping it small -- and the mitigation is the one already decided in the
granule document: the agent binaries are overwritten from the deployment's
own side on every rebase, so neither of them carries a version that can
drift far.

## What this retires

Deleting code is the strongest argument here, and it is specific:

- `ssh_tools.go`'s `cat`/`dd`/`mkdir` implementations of read, write and
  edit, and most of `shellquote.go` with them.
- `ssh_placements.go` entirely, including the `install -m` umask
  workaround -- a filesystem call that takes a mode retires that class of
  bug rather than stepping around it.
- The two-path placement problem: with a filesystem API, a granule VM has
  something close enough to `rootedSandbox.Root()` that placement stops
  needing one implementation per backend shape.
- `exec_stream.go`'s stream serialization, which exists because `os/exec`
  hands back two pipes; envd's process API streams natively.
- The forwarder the lifecycle design would otherwise carry.

And one it does not retire: `granule exec` stays, because it is how envd is
reached in the first place and how a guest is debugged when envd is the
thing that is broken.

## Migration

In stages, each of which is shippable and none of which is a flag day.

1. **Run envd, change nothing.** Add it to the guest image, started by the
   overlay. Nothing calls it. This proves the bind flag, the token
   bootstrap and the boot ordering in isolation.
2. **Implement `remoteRunner` over envd.** One adapter with the same two
   methods, chosen per backend by configuration. Every existing caller --
   MCP tools, CI steps, placement, git credential configuration -- keeps
   working unchanged, because they all go through that interface. This is
   the step where the two implementations can be run against each other
   on the same sandbox and compared.
3. **Move file operations to the filesystem API**, deleting the shell
   implementations behind them. `read_file`/`write_file`/`edit_file` keep
   their names, schemas and output shapes; only what is underneath moves.
4. **Move placement**, and collapse the `rootedSandbox` split if it turns
   out to be collapsible.
5. **Per-backend rollout.** The host-directory backend is last and is the
   one that needs a decision of its own: it has no VM, so running a daemon
   there is a daemon where there is currently an `os/exec` call. `-isnotfc`
   makes it possible; whether it is worth it, or whether that backend keeps
   the local implementation permanently behind the same interface, is open.
6. **Console sessions.** `pkg/console` runs an agent CLI on a PTY and
   carries bytes to whoever is attached. envd has PTYs natively, and this
   is the one place where adopting it might *add* a capability rather than
   relocate one -- but it is also the most user-visible thing to break, so
   it goes last and on its own.

## Auth

One token per sandbox, generated by grain at create time, handed to envd
through the first authenticated `/init`, held by the daemon and never
placed in the sandbox's filesystem or exposed to an agent.

Two things to be careful about. The token replaces rather than joins
granule's per-boot keypair for envd's own surface, so there are two
authentication mechanisms in the guest with different lifetimes and they
should not be confused in code or in a log line. And `/init` is a
bootstrap window: envd refuses upgrades until initialized, and grain
should treat a sandbox as not ready until its `/init` has succeeded, the
same way `enforceGuestNetwork` is fatal to an acquire rather than a
warning.

## Vendoring and version policy

envd becomes a dependency with a compatibility surface, which is most of
the reason to adopt it and also the thing to be deliberate about.

Mirror what `third_party/granule` already does, because it already solved
this: a source snapshot, byte-for-byte upstream, with local patches
recorded as a list that is expected to be empty, and a resync that moves
the whole tree rather than cherry-picking. The property that makes it work
for granule -- "granule is built from this tree and talks to a guest built
from this tree, and the two agree ... because they are the same files" --
applies here too, and is worth preserving rather than rediscovering.

## Open questions

- **Does the host-directory backend run envd?** Stage 5 above. There is a
  real argument for keeping a local implementation behind the same
  interface for ever: it is the backend used where there is no isolation
  to speak of anyway, and a daemon adds a failure mode to the simplest
  case.
- **What happens to cgroup management?** envd manages cgroups by default
  and granule does memory and CPU hotplug. Those are not obviously in
  conflict and are not obviously not. Worth answering before stage 1, not
  after.
- **How much of envd's surface should grain expose to agents?** The MCP
  tools are a deliberate roster, and gaining a filesystem API is not a
  reason to widen it. Watchers and port forwarding in particular should
  stay unexposed until something asks.
- **Does this change the reducer's REST contract?** No, and it should not.
  The lifecycle definition serves HTTP on a unix socket; whether grain
  reaches it through a forwarder of its own or through envd is grain's
  side of that boundary. The repository writing a reducer should not be
  able to tell.
