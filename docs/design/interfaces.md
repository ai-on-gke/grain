# grain's three interfaces: Agent, Sandbox, Capability, and named endpoints

Status: design, not implemented. This revision (task 484) replaces the
one merged from task 478. That revision carried every remote byte over a
tunnel agent of grain's own. This one carries them over **`granule
forward`** (gke-labs/granule task 480), and chooses a carrier for each
kind of traffic from task 483's measurements:
[docs/test-runs/2026-10-24-tunnel-vs-direct.md](../test-runs/2026-10-24-tunnel-vs-direct.md),
cited below as **[483]**. The model itself (the three interfaces, MCP, and
named endpoints) was settled with the operator in the director chat of
2026-10-24 and has not changed. Stage 1 is task 482, which waits on this
doc.

**Dependency.** `granule forward` (`gke-labs/granule#13`, grain task 480)
is vendored in `third_party/granule/pkg/forward` and
`third_party/granule/pkg/cli/forward.go` (`docs/granule-local-patches.md`),
and wrapped in `pkg/granule/forward.go` (`d7d5e032b`). This doc designs
against that real API (IF-27, IF-31), and marks the three extensions that
the initial `granule forward` patch does not include yet (F-4 unix-socket
endpoints, F-5 dynamic spec addition/removal on a live session, and F-8
configurable stream flow-control windows) as **(extension: IF-27)**.

Requirement headings carry `IF-<n>` IDs (docs/design-docs.md). Headings
without an ID are background, rationale or history. IF-15 and IF-25 were
dropped in this revision and are not reused (see "Changes from the
previous revision").

## Summary

grain is a **controller** plus **three kinds of object**, each defined by
its interface:

| object         | what it is                                          | its interface                                                   |
|----------------|-----------------------------------------------------|-----------------------------------------------------------------|
| **Agent**      | a model CLI with a login and a quota, or a person   | sessions (start, resume, send, cancel) and the events they stream |
| **Sandbox**    | one attempt's environment                           | lifecycle, `exec`, and port forwarding                          |
| **Capability** | a server that mints grants for an attempt           | the existing protocol (docs/capabilities-spec.md) and its outputs |

The controller is the daemon as it is today: state, scheduling, the merge
queue, the CI judge, the UI and the API. Apart from the data plane of port
forwarding, **every interface is a set of MCP calls**.

The controller is also a **switchboard of named endpoints**. Every service
a sandbox or an agent reaches is a name (`coordinator:git-proxy`,
`sandbox:<attempt>/mcp`, ...). Inside a sandbox a name always appears at
the same fixed local address. For example, `127.0.0.1:9728` is the git
proxy and `127.0.0.1:9729` is the package cache. Only the controller knows
what carries a name. There are two carriers:

- **granule forward.** This is out-of-band TCP forwarding into and out of
  a VM guest. It runs over granule's exec→agent→vsock channel, and works
  the same way on Docker (`docker exec`) and Kubernetes (`kubectl exec`).
  It needs no guest network at all. It is the carrier for every granule
  sandbox the controller cannot reach directly, and for every
  `network: none` granule sandbox.
- **the direct route.** This is today's private bridge (`192.168.9.1` and
  the port block), unchanged. It stays the fast path for local sandboxes
  with `network: open`, because [483] shows it is 10 to 30 times cheaper
  per byte than any exec-carried tunnel.

grain does **not** build a tunnel agent of its own. Remote sandboxes must
be granule VMs (IF-16).

The decisions, with the numbers behind them (IF-16):

| traffic | local, `network: open` | local, `network: none` | remote (Kubernetes, controller outside the cluster) |
|---|---|---|---|
| MCP (`sandbox:<attempt>/mcp`) | as today (exec per call) | as today | granule forward. +2 to 6 ms over distance, against 0.52 s for each `kubectl exec` [483] |
| git proxy | direct | granule forward | granule forward. +1 s on a shallow clone, +4 s on a full one [483] |
| model proxy | direct | granule forward | granule forward |
| package cache | direct | granule forward | stage 1: granule forward to the controller's cache. Later: **a cache co-located with the cluster** (IF-29) |
| Actions cache, bulk | direct | granule forward, with a byte budget | granule forward, with a byte budget. Later: co-located |
| staging, sandbox ports | direct | granule forward | granule forward |

Stage 1 is a controller on a corp VM that accepts no inbound connections.
It drives a GKE cluster in another project through a kubeconfig. Each
`kubernetes-granule` sandbox pod has one `granule forward` session, which
carries `coordinator:*` traffic into the guest and the guest's half of
`sandbox:<attempt>/mcp` out of it. There is no relay, no load balancer, no
inbound connection, and no grain tunnel agent anywhere.

## Problem

Today each of these is its own mechanism, written for its own case:

- **Agent frameworks.** `pkg/agent/antigravity`, `claude`, `codex` and
  `custom` each implement `agent.Framework.Run(ctx, RunConfig)`
  (`pkg/agent/agent.go`). Behind that one call are implicit contracts:
  how a usage limit is reported (`agent.UsageLimitError`, `LimitScope`,
  `ResumeAt`), how a run is adopted after a restart (`ResultParser`,
  `AttemptsParser`), how interactive and terminal sessions work
  (`TerminalFramework`), and how the sandbox is reached. `RunConfig`
  names the sandbox by backend: `SandboxRoot`, `GranuleVM`, `Container`
  or `K8sPod`. Each framework turns that into `grain mcpserver` arguments.
- **Sandboxes.** `orchestrator.Sandboxes.Acquire(name, Shape)` returns a
  `Sandbox` with `Tools`, `ConfigureGitCredentials` and `Release`. Extra
  abilities are optional interfaces discovered by type assertion:
  `rootedSandbox`, `SandboxPlacer`, `vmNamedSandbox`,
  `containerNamedSandbox`, `podNamedSandbox`, `SandboxCommander`. Agent
  tools reach a sandbox in one of four ways: a host directory,
  `granule exec`, `docker exec` or `kubectl exec`
  (`pkg/mcp/docker_exec_runner.go`, `kubectl_exec_runner.go`).
- **Reaching the controller.** A sandbox reaches the controller's services
  (git proxy, package cache, Actions cache, model proxies, the capability
  port relay) at `SandboxAdvertiseHost(cfg)` on a fixed port block
  (`pkg/daemon/sandboxnet.go`: `Fabric`, `Direct`, `SandboxPortOffsets`;
  docs/ports.md). That address is the Docker bridge gateway, the granule
  host gateway (`192.168.9.1`) or the daemon pod's IP (`$GRAIN_POD_IP`, or
  a Service through `AdvertiseVia`). Each service authenticates the
  sandbox in its own way: a bearer token for the git and model proxies,
  the source address for the package cache, and a per-forward token for
  capability ports (`pkg/capability/portforward`).
- **`network: none`.** A local granule guest with no network still has a
  NIC. A host nftables chain (`pkg/orchestrator/granule_firewall.go`,
  `granuleIsolationRuleset`) drops everything it sends except traffic to
  the gateway on the deployment's service ports, so that it can still
  reach the git proxy and the package cache. That chain is about 500 lines
  of reasoning about bridge hooks, spoofing, ARP and IPv6 neighbour
  discovery, and it needs `nft` as root on the host. A `kubernetes-granule`
  guest with no network gets no NIC at all (`CHV_NET_MODE=none`), so it
  cannot reach those services in any way.
- **Reaching a sandbox.** The controller reaches a sandbox's services
  through mechanisms written for each case: `handleStagingProxy` for
  persistent sandboxes, `grain mcpserver` forked beside the agent CLI,
  and the executor's tunnel of MCP calls (`pkg/modelexec`, over
  `pkg/tunnel/mux`).

Every one of these assumes **the sandbox can open a TCP connection to the
controller**. That stops being true when the controller and the sandboxes
are on different networks. The case that needs it now is a GKE cluster in
another project, driven from a corp VM that accepts no inbound
connections. The rendezvous relay that once covered this case was removed
on 2026-10-20 (docs/relay-removed.md). A load balancer or IAP in front of
the controller is the alternative the operator rejected (task 457).

## Terminology

- **Controller**: the grain daemon. It owns all state and all policy.
- **Attempt**: one dispatch of one task (`model.Dispatch`, run id
  `<task>-<n>`). An attempt holds at most one sandbox at a time, at least
  one agent session, and any number of capability grants. Its id is the
  `attempt_id` of docs/capabilities-spec.md.
- **Provider**: an implementation of an object's interface. Examples are
  the `k8s-granule` sandbox provider and the `claude` agent provider.
- **Endpoint name**: a string naming a service, from the grammar in
  IF-9.
- **Registrant**: whoever serves a name: the controller itself, a sandbox
  provider, a capability server, or an executor.
- **Forward**: a listener in a stated place whose connections are carried
  to a named target.
- **Route**: how one name reaches one sandbox: `direct`,
  `granule-forward` or `executor` (IF-14).
- **Carrier**: what carries a route's bytes. It is `granule forward` (one
  long-lived exec session per VM container, carrying many streams), the
  direct route (a TCP connection over a bridge or a pod network), or an
  executor's dial-out session.
- **VM container**: the Docker container or Kubernetes pod container that
  runs a granule VM. An exec into it reaches the guest through granule's
  in-guest agent over vsock.
- **Outage**: a failure of infrastructure that the task did not cause.
  An outage requeues the attempt. It is never a task failure. See
  IF-8.

## Interfaces are MCP {#IF-1}

Each object's interface is defined as MCP tools and notifications. The
schemas in this doc are the contract.

- **Out-of-process providers** are MCP servers that the controller reaches
  over stdio or Streamable HTTP. Today these are custom agent backends,
  capability servers, and anything run through an executor.
- **In-process providers** are the built-in Go implementations: the
  docker, granule, k8s and k8s-granule sandboxes, and the antigravity,
  claude and codex frameworks. They implement a Go interface whose
  methods correspond one to one with the MCP tools, and the controller
  calls them directly. The interface lives in `pkg/iface/{agent,sandbox}`.
  A conformance test runs the same suite against each in-process provider
  and against an MCP adapter of it (`iface.ServeMCP(provider)`). That
  test is what keeps the two forms equal.
- Every call made on behalf of an attempt carries that `attempt`. The
  exceptions are the `*.describe` calls, and the registrations of
  `coordinator:*` names, which belong to no attempt. Every provider
  rejects a call naming an attempt it was not given.
- Streamed output (session events, exec output, sandbox events) is
  carried in MCP notifications. Each notification has a per-stream `seq`
  that increases monotonically. Each streaming tool accepts `from_seq`, so
  a controller that restarts can resume a stream from the cursor it
  stored. This is the same rule that run-host spools follow today
  (`pkg/runhost/spool.go`).
- Errors are MCP tool errors carrying a `class` field with one of three
  values: `task` (the task's own fault), `outage` (infrastructure), or
  `refused` (policy or validation). The controller's failure handling
  (IF-8) acts on `class`, and never on the error's text.

The data plane of port forwarding is the one exception. It carries bytes,
not calls (IF-11).

## The Agent interface {#IF-2}

An agent provider runs sessions for one framework: `agy`, `claude`,
`codex`, a custom backend, or a person driving an interactive session.
It holds the framework's login and quota. It never holds grain state, and
it never reaches a sandbox except through the MCP endpoints it is given.

### Tools

| tool                | arguments                                                                                       | result |
|---------------------|--------------------------------------------------------------------------------------------------|--------|
| `agent.describe`    | none                                                                                            | `{framework, version, protocol, models[], interactive, adoptable, login: {state, account}, quota?: {remaining, reset_at}}` |
| `session.start`     | `{attempt, prompt, conversation[], model, mcp_servers[], cwd, env, deadline, max_turns, interactive}` | `{session_id}` |
| `session.resume`    | `{attempt, session_id, prompt?}`                                                                | `{session_id}` |
| `session.send`      | `{attempt, session_id, message}`. Only valid on an interactive session                          | `{}` |
| `session.cancel`    | `{attempt, session_id, reason}`                                                                 | `{}` |
| `session.events`    | `{attempt, session_id, from_seq}`. Replays the events from `from_seq`, then streams new ones    | stream |
| `session.result`    | `{attempt, session_id}`                                                                         | `{exit, result}`. `result` is `agent.Result` |

- `mcp_servers` is a list of `{name, url, headers}` entries. A URL here
  is always an address on the agent provider's own loopback: the
  `address` of a forward that the controller opened for that provider
  (IF-10). Agent providers never see endpoint names. The first entry is
  always `grain-sandbox`, which resolves to `sandbox:<attempt>/mcp`. The
  rest are the attempt's capability `mcp_endpoint`s
  (docs/capabilities-spec.md §5.2).
- `conversation` is the task's thread. The provider renders it into the
  prompt in its own framework's way, as `BuildPrompt` does today.
- `cwd` is a scratch directory for the CLI's own state, on the agent
  provider's own machine. It is never the sandbox's root working
  directory, which the CLI only sees through `grain-sandbox` tools.

### Events

`notifications/session/event`:
`{attempt, session_id, seq, type, at, ...}`. `type` is one of:

| type          | fields                                              | today |
|---------------|-----------------------------------------------------|-------|
| `output`      | `text`                                              | CLI stdout and the stream-json text parts |
| `tool_call`   | `id, server, tool, arguments`                       | `agent.ToolCall` |
| `tool_result` | `id, ok, summary`                                   | the transcript items |
| `usage`       | `model, input_tokens, output_tokens, cache_tokens`  | the usage in `agent.Result` |
| `usage_limit` | `scope: model\|account\|deployment, reset_at?, raw` | `agent.UsageLimitError`, `LimitScope`, `ResumeAt` |
| `transcript`  | `items[]`                                           | `transcript_items.go`, `sessionstore` |
| `exited`      | `code, reason`                                      | the end of `Framework.Run` |

`usage_limit` is a signal, not a failure. The controller pauses the
attempt and requeues it for `reset_at` (or the scope's default backoff),
and charges nothing to the task (IF-8, step 5).

### What existing frameworks implement implicitly

This table writes down the contract that the four frameworks already
satisfy without stating it. An adapter for each is a thin translation.

| contract                         | antigravity                          | claude                              | codex                       | custom (`docs/custom-agent-backends.md`) |
|----------------------------------|--------------------------------------|-------------------------------------|-----------------------------|------------------------------------------|
| start a session                  | `agy` with a generated settings dir  | `claude -p` with `--mcp-config`     | `codex exec` with its config | the executable, using the versioned JSON protocol |
| resume                           | the session id from `sessionstore`   | `--resume <id>`                     | `codex exec resume`         | protocol `resume` if declared             |
| send (interactive)               | `TerminalFramework`                  | `TerminalFramework`                 | `TerminalFramework`         | not offered                               |
| cancel                           | process-group kill (`pkg/procgroup`) | same                                | same                        | same                                      |
| events                           | log tail (`sessionwatch.go`)         | stream-json on stdout               | JSON events on stdout       | protocol events                           |
| usage limit                      | the AGY diagnostic log, then `ClassifyLimit` | the stderr text, then `ClassifyLimit` | same                | protocol `usage_limit`                    |
| adopt after restart              | `ResultParser` on the run-host spool | `ResultParser`, `AttemptsParser`    | `ResultParser`              | no                                        |
| tools                            | `grain mcpserver` forked by the CLI  | same                                | same                        | same                                      |

The last row is the only one that changes in meaning. Today each CLI forks
`grain mcpserver -granule-vm|-container|-k8s-pod|-sandbox-root ...`,
which reaches the sandbox by backend. Under this design each CLI is given
a URL for `sandbox:<attempt>/mcp`, and the backend never appears in an
agent's configuration. `RunConfig.SandboxRoot`, `GranuleVM`, `Container`
and `K8sPod` are removed once every provider registers that name (stage 2,
IF-24).

## The Sandbox interface

A sandbox provider builds, runs and destroys sandboxes of one backend.
A sandbox is one attempt's environment. The sandbox user is `debian`, and
the root working directory is the provider's workspace
(`K8sWorkspaceFor`).

### Lifecycle {#IF-3}

| tool                | arguments | result |
|---------------------|-----------|--------|
| `sandbox.describe`  | none      | the scheduling facts (IF-21) |
| `sandbox.acquire`   | `{attempt, shape: {cpus, memory_mb, disk_gb}, image, network: none\|open, labels, deadline, parent?}` | `{sandbox_id, workspace, user, routes: {<name pattern>: direct\|granule-forward\|executor}}` |
| `sandbox.extend`    | `{attempt, sandbox_id, deadline}` | `{deadline}` |
| `sandbox.release`   | `{attempt, sandbox_id}` | `{}`. Idempotent, and succeeds on a sandbox that is already gone |
| `sandbox.recreate`  | `{attempt, sandbox_id}` | `{sandbox_id}`. A fresh sandbox for the same attempt, with the same shape, image, network and labels |
| `sandbox.events`    | `{attempt, sandbox_id, from_seq}` | stream |

- `shape` and `network` are `orchestrator.Shape` and `model.NetworkAccess`
  (`open` or `none`). `image` is the sandbox image reference
  (`pkg/sandboximage`). `labels` are grain's own (task, attempt,
  deployment). The provider writes them wherever its backend keeps
  labels, and adoption matches on them.
- `parent` names the sandbox this one is created beneath. It places the
  new sandbox in its parent's address family (`pkg/sandboxaddr`), and
  releasing a parent releases its descendants.
- `deadline` is a hard stop. A provider releases a sandbox whose deadline
  passes without an `extend`, so a sandbox the controller has forgotten
  cannot outlive its attempt by more than one deadline.
- `acquire` returns once the sandbox can run `exec`. `routes` says how
  each name reaches this sandbox (IF-14). A granule provider whose
  routes include `granule-forward` returns only once the forward session
  is up (IF-27).

`notifications/sandbox/event`: `{sandbox_id, seq, type, at, detail}`.
`type` is one of `ready`, `died`, `oom_killed`, `preempted`,
`deadline_exceeded`, `carrier_down`, `carrier_up` or `released`.
`oom_killed` means the whole sandbox was killed for memory. An `exec`
killed by the OOM killer inside a living sandbox is not a sandbox event:
that exec reports `oom: true` in its result instead. The controller's
response to each event is in IF-8.

### exec {#IF-4}

| tool          | arguments | result |
|---------------|-----------|--------|
| `exec.start`  | `{attempt, sandbox_id, argv[], env{}, cwd, user, stdin: none\|pipe, pty?: {rows, cols}, limits: {timeout_s, output_bytes, memory_mb?}}` | `{exec_id}` |
| `exec.write`  | `{attempt, exec_id, data_b64, eof}` | `{}` |
| `exec.resize` | `{attempt, exec_id, rows, cols}` | `{}` |
| `exec.signal` | `{attempt, exec_id, signal}` | `{}` |
| `exec.wait`   | `{attempt, exec_id}` | `{exit_code, signal?, oom, timed_out, truncated}` |
| `exec.output` | `{attempt, exec_id, from_seq}` | stream of `{seq, stream: stdout\|stderr, data_b64}` |

- `argv` is executed directly, with no shell. `env` is the complete
  environment, with no inheritance from anywhere. `user` defaults to
  `debian`, and `root` is accepted only from the controller itself, never
  from a tool call an agent made.
- `limits.output_bytes` bounds what is retained for replay. The
  `truncated` field says whether output was dropped. A `pty` exec merges
  stderr into stdout.
- `exec.signal` sends the signal to the exec's process group.
- For granule sandboxes, `exec` is `docker exec <vm> granule exec ...`
  locally and `kubectl exec <pod> -c <vm> -- granule exec ...` on
  Kubernetes. These are today's `mcp.DockerExecRunner` and
  `kubectl_exec_runner.go` paths. For plain Docker it is `docker exec`,
  and for host sandboxes it is a fork.

**Files are built on exec.** There is no separate file API:

- `read_file` is `exec ["cat", "--", path]` with an output limit.
- `write_file` and each `placements` entry are one exec of a fixed script
  that takes the path, mode and owner as arguments (never interpolated
  into the script) and the content on stdin. The script writes to a
  temporary file in the target directory and renames it into place,
  which makes the write atomic (§5.1.3).
- A capability pass's placements are written by one exec of
  `tar -x --no-same-owner` that reads a tar stream on stdin. The
  controller has already validated every path (§5.1.2), and the tar
  members carry the validated mode and owner.
- Large or binary payloads (an image layer, a binary the guest needs) may
  use `granule cp` instead, where the provider has it. `granule cp` is an
  optimization of the same operation, with the same validation.

**Cost.** Setting up one `kubectl exec` session from a controller 34 ms
from the API server takes 0.52 s at p50 and 0.61 s at worst [483,
Environment]. Batching a pass's placements into one exec makes that
cost negligible at materialization. It is not negligible for an agent's
tool calls, which is why a remote granule sandbox serves its tools from
the guest through a forward (IF-5) instead of running one `kubectl exec`
per call. Building files on exec was chosen because one primitive, with
one set of limits and one audit trail, is easier to secure than two.

### The agent-facing MCP server {#IF-5}

The agent's tools (`run_command`, `read_file`, `write_file`,
`recreate_sandbox` and the rest of `pkg/mcp`'s roster) are an MCP server
**built on exec**. It is registered under the name
`sandbox:<attempt>/mcp` and reached through a forward. Its tools are the
same ones `grain mcpserver` serves today.

The server has two halves:

- **The front** serves every tool that talks to grain itself
  (`comment_on_issue`, `propose_task`, `wait_for_checks`,
  `recreate_sandbox`, and so on). It also holds the attempt's grain API
  token. It always runs on the controller's side, as a per-attempt
  `grain mcpserver` process started in the attempt's run host, as today.
  It therefore survives a controller restart the same way the agent CLI
  does. It never runs in a sandbox.
- **The sandbox tools** (`run_command`, `read_file`, `write_file`,
  `edit_file`, `list_dir`, and the others that touch the sandbox) are
  built on `exec`. Where they run is the provider's choice:
  - **exec per call from the front.** Docker, local granule and in-cluster
    Kubernetes do this, as today. A local `docker exec` costs
    milliseconds.
  - **a guest tools server, reached through a forward.** A remote granule
    sandbox does this. At acquire, one exec starts
    `grain mcpserver -sandbox-root <workspace> -sandbox-tools-only -listen
    127.0.0.1:9760` in the guest as `debian`. In the guest, `exec` is a
    fork. The front reaches the server through a granule forward
    (`-L`, IF-27) and passes the sandbox tools through to it. An agent's
    tool call then costs one stream on the existing forward session,
    about 36 to 40 ms [483, MCP table], instead of a new `kubectl exec`
    at 0.52 s. It also stops opening and closing an API-server session
    for every call, which is what the session cap in IF-30 counts.

The guest tools server is not a tunnel agent. It carries no traffic for
anything else, holds no credential, opens no connection, and binds only
the guest's loopback. Anything in the guest that runs as root can replace
it, but that gains nothing the guest could not do already: it can only run
commands in its own guest. The `grain` binary is copied into the guest at
acquire with `granule cp` (or the tar exec), at the controller's own
version, so that it never drifts from the front. When envd becomes the
guest's process API (docs/envd-adoption.md), envd takes this server's
place behind the same forward, and the front calls envd instead.

`recreate_sandbox` calls `sandbox.recreate`, and then has the controller
re-materialize the attempt's cached grants (§5.5.5). The names do not
change, so the agent's forward to `sandbox:<attempt>/mcp` carries on. A
remote sandbox gets a new forward session and a new guest tools server.
The front sees one reset stream and retries it.

### Port forwarding on the sandbox side {#IF-6}

For each forward whose listener is in the sandbox (IF-10), the sandbox
provider makes the target appear at the requested guest address:

- **On a `granule-forward` route**, granule's in-guest agent binds the
  listener on the guest's loopback (`-R`, IF-27). Each connection becomes
  a stream over the forward session and ends at the controller's
  per-attempt listener for that forward. The guest needs no NIC, no
  route and no DNS for this.
- **On a `direct` route**, the provider binds nothing for the controller's
  own services. The sandbox is handed the controller's address, as today.
  For capability ports it starts the guest forwarder
  (`portforward.GuestForwarder`) through `exec`, as today.

For the reverse direction (a name the sandbox serves, such as
`sandbox:<attempt>/port/<n>`), a `granule-forward` provider adds an `-L`
from a controller-side listener to guest `127.0.0.1:<n>`. A `direct`
provider registers the guest's own address.

## The Capability interface {#IF-7}

The capability protocol is docs/capabilities-spec.md: `tools/list`,
`mint_<method>`, `release_<method>` and `configure`. This doc does not
change it. What this doc fixes is **how each output of §5 reaches the
sandbox**: always through the sandbox interface, and never through a path
specific to one backend. That holds even when the capability server, the
controller and the sandbox are on three different machines.

| output (§5)                 | materialized by | how |
|-----------------------------|-----------------|-----|
| `placements` (§5.1)         | `exec`          | one `tar -x` exec for each pass (IF-4) |
| `setup_root`, `setup` (§5.5)| `exec`          | `exec.start {argv: [path], user: debian, cwd: <root>, stdin: none, limits.timeout_s}` |
| credentials                 | `exec`          | they are `placements`. Nothing else carries a credential into a sandbox |
| `ports` (§5.3)              | a forward       | the capability's service is registered as `capability:<id>/port/<host_port>`: by the controller for a local server, after the ownership check in §5.3.2, or by the executor that hosts it. The controller opens `forward.open {listen: sandbox 127.0.0.1:<sandbox_port>, target: that name}` |
| `mcp_endpoint` (§5.2)       | a forward       | the controller's §5.2.2 path proxy is registered as `capability:<id>/mcp/<attempt>`. The agent provider is given a forward to it on its own loopback |
| `prompt_section` (§5.4)     | the session     | appended to `session.start`'s prompt |

The §5 order still holds: forwards, then placements, then setup, then MCP
and the prompt, then the session.

A capability server that is not on the controller's machine registers its
names over the same control plane (IF-10), through an executor (IF-20).
The controller reaches it over the executor's session and splices the
stream onto the sandbox's route. The ownership check in §5.3.2 becomes a
check that the registrant is the executor hosting that capability's
service. The executor's allowlist entry declares that service.

## Run order, failure handling and requeueing {#IF-8}

An attempt runs these steps, in order:

1. **Agent.** Choose an agent provider and model for the attempt
   (`AgentChoice`). Reserve its connection slot.
2. **Sandbox.** `sandbox.acquire`. For a `granule-forward` route this
   includes starting the forward session (IF-27), and the provider also
   opens the attempt's fixed `coordinator:*` forwards (IF-16) before
   returning. Register `sandbox:<attempt>/*`.
3. **Capabilities.** Mint every grant of the pass, then materialize them
   in the §5 order (IF-7).
4. **Forwards for the agent.** Open the forwards the agent provider needs
   (`sandbox:<attempt>/mcp`, and each capability `mcp_endpoint`).
5. **Sessions.** `session.start`, then stream events until `exited`.
   Use `resume` and `send` as the task requires.
6. **Release.** Close the attempt's forwards (which ends its forward
   session), release every grant (§4.4), call `sandbox.release` and free
   the agent slot. This step runs whatever happened before it.

Every failure falls into one of three classes:

- **task**: the task caused it. It counts against the task's attempt
  budget, and the attempt's outcome says why.
- **outage**: infrastructure caused it. The attempt is requeued with
  backoff, and its attempt count is not charged. The failure counts
  toward that provider's circuit breaker (tasks 418/419): a provider with
  repeated outages stops being chosen until a probe succeeds, and work
  that can use another provider moves there.
- **refused**: policy or validation refused it. The task is parked with
  the reason in its conversation, and no retry happens until something
  changes.

**A lost sandbox or a lost agent is an outage, never a task failure.**

| step | failure | class | what happens |
|------|---------|-------|--------------|
| 1 | no agent with capacity; a usage limit is already known | none | the task stays queued. No attempt is created |
| 1 | the agent login is invalid or expired | outage | the provider is marked unhealthy, and a human is told (the existing auth-fix path, `pkg/authfix`) |
| 2 | no capacity, over budget (task 459), or the cluster's forward cap reached (IF-30) | none | the task waits for capacity. No attempt is created |
| 2 | provider error, timeout, or image pull failure for a deployment image | outage | requeue. Circuit breaker |
| 2 | an image the task itself named does not exist | task | the attempt fails, naming the image |
| 2 | the forward session cannot be established within 60 s | outage | release the sandbox, then requeue |
| 3 | `mint_` refused (§4.3.4) | refused | release every grant of the pass |
| 3 | capability server unreachable | outage | release, then requeue |
| 3 | placement or port validation fails | refused | the §5 rollback |
| 3 | a setup command exits non-zero (§5.5.4) | task | the attempt fails. The output tail goes to the conversation |
| 3 | `exec` transport lost during materialization | outage | release, then requeue |
| 5 | `usage_limit` event | none | cancel the session. The attempt is *paused*, not failed, and requeued for `reset_at`. The next attempt resumes the session when the provider is adoptable |
| 5 | the agent provider is lost (executor offline, process killed by the host) | outage | requeue. An adoptable provider is resumed from its spool cursor (`from_seq`) |
| 5 | sandbox `died` or `preempted` | outage | release, then requeue, unless the agent is mid-session and the provider supports `recreate`. In that case the controller recreates the sandbox, re-materializes it, and tells the agent through a `recreate_sandbox`-style result on its next tool call |
| 5 | sandbox `oom_killed` | outage if the sandbox was overcommitted (task 459), task otherwise | when overcommitted, requeue with overcommit off for this task. Otherwise the attempt fails, reporting the shape it ran out of |
| 5 | `carrier_down` | none | reconnect (IF-30). Streams in flight reset. The attempt continues |
| 5 | `carrier_down` lasting 5 minutes | outage | treated as a lost sandbox: release, then requeue |
| 5 | a forward's byte budget is spent (IF-13) | none | new streams on that forward are refused, with a reason the agent sees. The attempt continues |
| 5 | the session ends without a result for a reason the agent reported | task | today's run-failure rules (`pkg/agent/runfailure.go`) |
| 5 | deadline | task | today's deadline rule |
| 6 | release fails | none | the leftover is logged, and the reaper retries it, matching on `labels`. A failed release never changes an attempt's outcome |

The rule behind the table: a failure is charged to a task only when
running the same task again on healthy infrastructure would fail the
same way.

## Port forwarding: named endpoints

The controller is a switchboard. A service is never addressed by a host
and port in grain's own configuration. It is addressed by a **name**. A
forward always targets a name, and policy decides which names an attempt
may open.

### Endpoint names {#IF-9}

```
name      = kind ":" path
kind      = "coordinator" | "sandbox" | "capability" | "executor"
path      = segment *( "/" segment )
segment   = 1*( ALPHA / DIGIT / "-" / "_" / "." )
```

| name | registrant | served | fixed address in the guest |
|------|------------|--------|----------------------------|
| `coordinator:git-proxy` | controller | `pkg/gitproxy` | `127.0.0.1:9728` |
| `coordinator:package-cache` | controller | `pkg/pkgcache`, its explicit HTTP endpoint | `127.0.0.1:9729` |
| `coordinator:package-cache/http`, `.../https` | controller | the cache's interception listeners | `127.0.0.1:80`, `127.0.0.1:443` |
| `coordinator:package-cache/dns` | controller | the cache's DNS service, as DNS over TCP (RFC 7766) | `127.0.0.1:53` (TCP) |
| `coordinator:actions-cache` | controller | `pkg/actionscache` | `127.0.0.1:9730` |
| `coordinator:model-proxy/<vendor>` | controller | `pkg/modelproxy`, one name per `modelproxy.Vendors()` entry | `127.0.0.1:9744`+ |
| `coordinator:capability-ports` | controller | `pkg/capability/portforward`'s relay, for `direct` routes only | `127.0.0.1:9731` (direct: the gateway) |
| `capability:<id>/port/<host_port>` | controller or executor | a capability `ports` entry (§5.3) | `127.0.0.1:<sandbox_port>` from the grant |
| `capability:<id>/mcp/<attempt>` | controller or executor | a capability's attempt-scoped MCP path proxy (§5.2.2) | not in the guest |
| `sandbox:<attempt>/mcp` | controller (the front, IF-5) | the agent-facing MCP server | not in the guest |
| `sandbox:<attempt>/tools` | sandbox provider | the guest tools server, for remote granule sandboxes | `127.0.0.1:9760` |
| `sandbox:<attempt>/port/<n>` | sandbox provider | TCP port `n` on the sandbox's loopback, for staging UIs and dev servers | `127.0.0.1:<n>` |
| `executor:<id>/...` | executor | services an executor hosts (IF-20) | not in the guest |

`<attempt>` is the run id. A persistent or standing sandbox is registered
under its own stable name in the attempt position
(`sandbox:persist-<name>/port/<n>`). That is how `/staging/<name>/`
keeps working across attempts.

The guest addresses are the port block of docs/ports.md
(`SandboxPortOffsets()`), moved onto the guest's loopback. On a `direct`
route the same ports are on the gateway (`192.168.9.1:9728` and so on),
exactly as today. 9760 is block offset 32, which is new and is added to
`SandboxPortOffsets` and docs/ports.md when it is implemented. The names
are how the table's entries are addressed. The offset table remains the
reason that a daemon restart does not move a port.

### Control plane {#IF-10}

These MCP tools are served by the controller to registrants (sandbox
providers, executors, out-of-process capability servers). The controller
also uses them itself:

| tool | arguments | result |
|------|-----------|--------|
| `endpoint.register` | `{name, attempt?, protocol: tcp\|http, token}` | `{}` |
| `endpoint.unregister` | `{name, attempt?}` | `{}` |
| `forward.open` | `{attempt, listen: {side: sandbox\|controller\|executor:<id>, address}, target: <name>, protocol: tcp\|http, token}` | `{id, address}` |
| `forward.close` | `{attempt, id}` | `{}` |
| `forward.list` | `{attempt?}` | `[{id, attempt, listen, target, protocol, route, opened_at, streams, bytes_in, bytes_out}]` |
| `forward.health` | `{attempt, id}` | `{state: up\|degraded\|down, route, carrier_pid?, last_error?, rtt_ms?}` |

- `endpoint.register`'s `token` is the secret the controller presents to
  the registrant when it relays a stream to it, so a registrant can
  refuse streams that did not come from the controller. A name is
  registered once. A second registration of the same name and attempt is
  refused, unless it comes from the same registrant identity re-registering
  after a reconnect (IF-18).
- `forward.open`'s `token` is the per-attempt forward token: 32 random
  bytes, base64url, minted by the controller. It is stored with the
  attempt, encrypted like other attempt secrets, so that a restarted
  controller can re-establish its forwards (IF-18). How a stream
  presents it depends on the route (IF-11).
- `listen.address` is `host:port`. The port may be 0, in which case
  `address` in the result is the address that was actually bound. The
  host must satisfy IF-13. `side: sandbox` is carried out by the sandbox
  provider (IF-6). `side: controller` binds on the controller.
  `side: executor:<id>` is carried out by that executor, which is how an
  agent CLI on a cloudtop gets `sandbox:<attempt>/mcp` on its own
  loopback.
- Only the controller calls `forward.open`. Registrants register. They
  never open forwards, for their own attempt or for any other.

### Data plane {#IF-11}

A connection accepted on a forward's listener becomes a **stream**, which
the controller splices to the target. How the stream proves which
forward it belongs to depends on the route:

- **`granule-forward`.** Clients in the guest are ordinary programs (git,
  npm, a browser), so they cannot send a header. Instead, the identity is
  **the listener**. For each `side: sandbox` forward the controller binds
  a private listener of its own, which serves only that forward, and
  starts the guest end with `-R <guest port>:<that listener>`. Every
  connection that arrives on it belongs to that forward and that attempt.
  The listener is `127.0.0.1:<ephemeral>` today (since `forward.Spec` in
  `third_party/granule/pkg/forward` dials TCP `host:port`), and moves to
  a unix socket at `/run/grain/fwd/<attempt>/<forward id>.sock`
  (directory mode 0700, owned by the daemon's account) once IF-27 F-4
  lands. The forward
  token is checked when the controller starts the session. It is never
  sent through the guest.
- **`direct`.** There is no header. The listener is the controller's own
  service listener at its published port, so the forward is implied by
  the port. The token is the service's existing credential: the bearer
  token of the git and model proxies, or the capability relay's forward
  token. The package cache keeps telling sandboxes apart by source
  address (`pkgcache.ClientSet`). This is the one place where a route has
  no per-attempt token. It stays that way so that direct sandboxes keep
  working exactly as they do now.
- **Between the controller and a remote registrant** (an executor, or a
  capability server behind one), each stream on the executor's mux
  begins with one header line:

  ```
  {"v":1,"forward_id":"<id>","name":"<name>","attempt":"<attempt>","token":"<token>"}\n
  ```

  The header is at most 4 KiB, and must arrive within 10 s. The receiver
  replies with `{"ok":true}\n`, or with `{"ok":false,"error":"<reason>"}\n`
  and then closes. After that the stream carries raw bytes. The token is
  the forward token on streams the executor opens toward the controller,
  and the registration token on streams the controller opens toward the
  executor.

For every stream, the controller then:

1. finds the forward, checks that its attempt is live and that policy
   (IF-13) still allows the target, and counts the stream against the
   limits;
2. serves the target itself if it is a controller name. The stream is
   handed to that service's handler as a `net.Conn`, with the attempt
   attached to its context. The git proxy sees an ordinary connection,
   and the package cache attributes the request to the attempt instead
   of a source address;
3. otherwise opens a stream to the registrant's route: an `-L` listener
   of another sandbox's forward session, or the registrant's executor
   session;
4. splices the two directions, propagating half-close (`CloseWrite`,
   `shutdown(SHUT_WR)`). It never buffers more than one flow-control
   window per direction.

### HTTP forwards {#IF-12}

A forward with `protocol: http` is routed by request instead of by
connection. That serves names a person reaches through the UI:

- **By path.** `/staging/<name>/...` on the UI listener resolves `<name>`
  to `sandbox:persist-<name>/port/<n>` (the port comes from the persistent
  sandbox's configuration) and proxies to it. Plain HTTP, SSE and
  WebSocket upgrades all work, as `handleStagingProxy` does today. The
  change in the route is that `httputil.ReverseProxy`'s `Dial` opens a
  stream to the name instead of dialling the sandbox's address. Access is
  the UI's (`requiresAccess`), not a forward token.
- **By host port.** `forward.open {listen: {side: controller, address:
  127.0.0.1:0}, protocol: http, target: sandbox:<attempt>/port/<n>}`
  gives a person or a local tool a port that is that sandbox port.

### Rules, limits and policy {#IF-13}

- **Binding.** A listener binds only to loopback (`127.0.0.1`, `::1`), to
  a unix socket under `/run/grain/fwd/`, or to an address inside the
  sandbox's own network namespace (for a `direct` route, the gateway
  address on the sandbox's private link, which only that sandbox can
  reach). `forward.open` refuses any other address, including `0.0.0.0`.
  On a `granule-forward` route, every guest listener is on the guest's
  loopback.
- **Tokens.** Every stream is tied to one attempt's forward, by its
  private listener, its service credential, or its header (IF-11). The
  package cache on a `direct` route is the one exception, stated there.
- **Teardown.** Everything an attempt opened is closed when the attempt
  ends: its forwards, its registrations, their streams and its forward
  session. This happens in step 6 of IF-8, and in the reaper for attempts
  the controller lost track of.
- **Limits.** Each attempt has 64 forwards, 256 concurrent streams, and
  16 stream opens per second (bursts of 64). Each name has 1024
  concurrent streams across attempts. A limit that is hit refuses the new
  stream and keeps the session.
- **Bytes.** On a remote `granule-forward` route, each attempt has a
  budget of bytes moved through forwards: 4 GiB by default
  (`-forward-byte-budget`). That is about 3.5 minutes at the 19 MiB/s
  [483, Bulk] that one stream manages, and enough for a full clone plus a
  cold `npm ci` and `go mod download` several times over. When it is
  spent, new streams are refused with a reason that names the budget.
  Each forward session also has a soft rate of 32 MiB/s, below the 38 to
  49 MiB/s measured per tunnel [483], so that one bulk transfer cannot
  starve that sandbox's own MCP calls ("Where kubectl carriers stop being
  acceptable", [483]). Local `granule-forward` routes have the budget and
  no rate.
- **Abuse.** The controller closes a session that its mux's abuse limits
  trip on (`pkg/tunnel/mux` `abuse_test.go` for an executor session;
  granule's own for a forward session). That is a carrier drop (IF-30),
  and the attempt survives it.

**Policy: which names an attempt may open.**

| name | allowed when |
|------|--------------|
| `coordinator:git-proxy` | always. The proxy's own token scopes the repositories |
| `coordinator:package-cache` and its `/http`, `/https`, `/dns` | always, including `network: none` |
| `coordinator:actions-cache` | CI attempts |
| `coordinator:model-proxy/<vendor>` | the attempt runs a model client inside its sandbox (CI jobs, nested agents) and holds that vendor's grant |
| `coordinator:capability-ports` | `direct` routes, with a §5.3 grant |
| `capability:<id>/...` | the attempt holds a live grant of `<id>` that returned that output |
| `sandbox:<self>/...` | the controller, or the agent provider of that attempt (with `listen.side` being that provider's side) |
| `sandbox:<other>/...` | never for an attempt. The controller itself opens these for the UI (staging) |

Policy is evaluated at `forward.open` and again at the start of each
stream, so revoking a grant stops new streams at once.

## Carriers

### Routes {#IF-14}

A name is resolved **per sandbox and per name**, when the forward is
opened:

```go
// pkg/endpoint
type Route string // "direct", "granule-forward", "executor"

type Resolution struct {
	Route   Route
	Address string // what the sandbox (or agent) is handed: host:port, or a URL for http
}

type Resolver interface {
	Resolve(attempt, name string) (Resolution, error)
}
```

- **`direct`** is today's behaviour, reached a different way.
  `daemon.Direct` (the existing `Fabric`) becomes the direct resolver.
  `Address` is `SandboxAdvertiseHost(cfg)` (or `AdvertiseVia`'s host for
  the names a Kubernetes Service fronts) plus `base +
  SandboxPortOffsets()[name]`. Sandboxes are handed byte-for-byte the
  addresses they are handed now. For each backend, a test pins this
  against the URLs written into a sandbox today.
- **`granule-forward`**: `Address` is the guest-loopback address in the
  IF-9 table. It is the same in every granule sandbox, local or remote.
- **`executor`**: the name is served by, or reached through, an
  executor's session (IF-20).
- The provider declares a route for each name pattern in
  `sandbox.acquire`'s result, following IF-16. The operator can override
  the routes per backend with `-sandbox-routes=<pattern>=<route>,...`.
- What a sandbox is handed (the git credentials URL, `/etc/resolv.conf`,
  `/etc/hosts`, the base URLs of the model proxies, capability `ports`)
  always comes from `Resolve`. No code path hands a sandbox
  `SandboxAdvertiseHost` directly any more. That is the whole migration
  for direct routes.

Whatever route a name takes, its name, policy, control plane and
teardown are the same.

### Which carrier for which traffic {#IF-16}

The input to these decisions is [483]. That report measured `docker exec
-i` and `kubectl exec -i` carrying `pkg/tunnel/mux`. Its README notes that
this is "the shape of `granule forward` without the agent→vsock hop".
Measured against direct, that shape costs:

| | MCP-sized call, p50 | one stream | 16 streams | CPU per GiB, both ends | `go mod download` (48 modules) |
|---|---:|---:|---:|---:|---:|
| direct (local bridge) | 0.20 ms | 3,032 MiB/s | 11,187 MiB/s | about 0.6 s | 4.04 s |
| exec tunnel, local | 0.86 ms | 99 MiB/s | 391 MiB/s | about 15 s | 4.30 s |
| exec tunnel, remote (34 ms away) | 39.7 ms | 19 MiB/s | 38 to 49 MiB/s | about 21 s | 8.58 s |

The rows that matter to each decision are cited where the decision is
made. The decisions:

1. **Remote sandboxes are granule VMs, and their carrier is granule
   forward for every name.** A controller that cannot reach the pod
   network (stage 1) has no other route. Latency is not a problem: MCP is
   +2 to 6 ms over distance, and stayed flat from 1 to 32 streams and from
   1 to 64 tunnels. git costs +1 s on a shallow clone and +4 s on a full
   clone, and the model proxy's first token costs +3 to 9 ms against
   model latency of hundreds of ms [483, Recommendation table]. Bytes are
   the limit, and IF-13 and IF-30 bound them.
2. **Local `network: open` granule and Docker sandboxes keep the direct
   route for every name. granule forward supplements it and does not
   replace it.** Locally, an exec-carried tunnel adds only 0.7 ms to an MCP
   call, and git stays within 6%. But it moves bytes 30 to 100 times more
   slowly than the bridge, and costs about 15 CPU-seconds per GiB against
   about 0.6 [483, Bulk]. A cold package cache, the Actions cache and
   artifacts would pay that on every run, so moving the bytes off the
   bridge gains nothing and costs CPU. Moving only git and MCP would gain
   nothing either, and would split one sandbox across two carriers for no
   reason.
3. **Local `network: none` granule sandboxes move to granule forward for
   every name, and get no NIC (IF-28).** Here the trade goes the other
   way. The direct route is what forces the nftables gateway exception,
   and that exception is the most intricate security code in the sandbox
   path. Over granule forward, `go mod download` takes 4.3 s instead of
   4.0 s, and the fetch phase of an `npm ci` takes 0.41 s instead of
   0.02 s [483, Package cache]. The CPU cost is bounded by the byte budget.
   A cold `npm ci` of `ui/` (150 MiB) costs about 2 CPU-seconds across
   both ends. **Provisional:** [483] measured `docker exec`, without the
   vsock hop. Before stage 3 ships, re-run `tunnel-vs-direct.sh local`
   with `granule forward`, as [483] says to, and confirm that the
   per-byte CPU stays within twice the `docker exec` figure.
4. **In-cluster controllers keep the direct route** (pod IP or Service)
   for `network: open` `kubernetes-granule` and plain `kubernetes`
   sandboxes, as today. Their `network: none` granule sandboxes have no
   NIC and use granule forward, as in decision 3. Today they cannot reach
   the git proxy at all.
5. **grain does not build a tunnel agent of its own (carrier 3 in the
   brief).** A sandbox that is not a granule VM must be directly
   reachable: local Docker, or plain Kubernetes with the controller in
   the cluster. An out-of-cluster controller refuses a plain `kubernetes`
   backend at startup, and names `kubernetes-granule` as the fix. The
   reasons are:
   - [483]'s tunnel *is* the shape grain's agent would have had (exec
     stdio plus `pkg/tunnel/mux`), so a second implementation would buy
     no performance;
   - granule forward's guest end sits behind the VM boundary, whereas a
     sidecar tunnel agent in a plain pod shares the sandbox's network
     namespace and is protected only by a session key (the previous
     revision's own open question);
   - one carrier means one reconnect story and one set of tests.

   If a non-granule remote sandbox is ever needed, the previous revision's
   tunnel agent (IF-15, now dropped) is the fallback, recorded under
   Alternatives.

### granule forward: contract and landed API {#IF-27}

`granule forward` (`gke-labs/granule#13`, task 480) is vendored in
`third_party/granule/pkg/forward`, `third_party/granule/pkg/cli/forward.go`,
and `third_party/granule/pkg/guestexec/forward.go`, and wrapped by
`pkg/granule/forward.go` (`d7d5e032b`). Its CLI is:

```
granule forward [-kube] [-kubectl <bin>] [-kubeconfig <path>] [-n <ns>] [-c <container>] \
  [-L [bind_host:]port:target_host:target_port]... \
  [-R [bind_host:]port:target_host:target_port]... <name>
```

On Docker it runs `docker exec -i granule-vm-<name> granule forward ...`;
with `-kube` it runs `kubectl [--kubeconfig <path>] -n <ns> exec -i -c
<container> <pod> -- granule forward ...`. Inside the VM container,
`granule-shim` connects to `granule-agent` over virtio-vsock (`forward`
session request), which binds the `-R` listeners inside the guest and
runs the guest mux (`forward.ServeGuest`). The caller runs
`forward.RunClient(ctx, stdoutPipe, stdinPipe, specs, onReady)` over the
exec stdio stream using a 9-byte frame header (`typ[1] | streamID[4] |
length[4]`: `frameReady=1`, `frameOpen=2`, `frameOpenOK=3`,
`frameOpenErr=4`, `frameData=5`, `frameCloseWrite=6`, `frameClose=7`).

The contract grain uses, showing what is landed today and what is a
follow-up extension:

- **F-1. One session per VM container (landed).** One `granule forward`
  session holds one exec stream (`docker exec -i` locally, `kubectl exec
  -i` on Kubernetes). That stream carries every forward of the sandbox as
  multiplexed streams in both directions over exec → `granule-shim` →
  vsock → `granule-agent`. When the guest binds all `-R` listeners it
  sends `frameReady`, which fires `onReady`. On Kubernetes this is **one
  long-lived API-server streaming session for each sandbox**, which is
  what IF-30 counts.
- **F-2. `-L [bind_host:]port:target_host:target_port` (landed,
  `forward.LocalToRemote`).** Listen on `bind_host:port` on the caller
  (`127.0.0.1` when `bind_host` is omitted), and dial `target_host:
  target_port` inside the guest for each connection (controller → guest:
  `sandbox:<attempt>/tools`, `sandbox:<attempt>/port/<n>`, staging).
- **F-3. `-R [bind_host:]port:target_host:target_port` (landed,
  `forward.RemoteToLocal`).** `granule-agent` listens on `bind_host:port`
  inside the guest (`127.0.0.1` by default), and the caller dials
  `target_host:target_port` on the host/controller for each connection
  (guest → controller: every `coordinator:*` and `capability:*` name).
  The guest port may be privileged (53, 80, 443), because `granule-agent`
  runs as root.
- **F-4. A unix-socket local end (`-R gport:unix:/path`) (extension:
  IF-27).** `forward.Spec` currently parses TCP `host:port` endpoints.
  Adding a `unix:/path` target gives IF-11 a listener that only the
  daemon's account can reach. Until F-4 lands, grain binds a loopback port
  (`127.0.0.1:<port>`), and IF-22 records the residual risk.
- **F-5. Forwards added and removed at runtime (extension: IF-27).**
  `forward.RunClient` and `forward.BindGuestListeners` currently take the
  full slice of `[]forward.Spec` when the session starts. Adding a control
  frame (or library methods on the live mux) to add and remove `-L`/`-R`
  entries without restarting the session lets capability ports, staging
  and recreated tools servers attach after acquire without resetting
  in-flight streams. Until F-5 lands, changing the spec set restarts the
  forward session.
- **F-6. It works with no guest network (landed).** With
  `CHV_NET_MODE=none` (`pkg/orchestrator/k8s_granule_sandboxes.go`), the
  whole path is exec → `granule-shim` → vsock → `granule-agent`.
- **F-7. It exits non-zero when its session is lost (landed)**, and grain
  reconnects it (IF-30; `K8sGranuleSandboxes` supervises `ForwardKubectl`
  with `DefaultGranuleForwardBackoff = 500ms`). Desirable extension: keep
  the guest's `-R` listeners for 30 s across a drop so a connection during
  a reconnect is delayed rather than refused.
- **F-8. Flow control sized to the path (extension: IF-27).** [483] found
  that `pkg/tunnel/mux`'s fixed 1 MiB stream window caps one remote stream
  at about 20 MiB/s. `third_party/granule/pkg/forward` currently uses a
  64-frame per-stream channel (`copyChunkSize = 32 KiB`, ~2 MiB buffered)
  without per-stream credit frames; sizing per-stream flow control to the
  bandwidth-delay product bounds head-of-line blocking under mixed bulk
  and MCP traffic.
- **F-9. TCP only, with half-close (landed).** `frameCloseWrite`
  propagates TCP half-close (`CloseWrite`), and grain does not need UDP
  because DNS is carried over TCP (IF-28).

`pkg/granule/forward.go` drives `kubectl exec -i ... -- granule forward`
and runs `forward.RunClient` in-process over its stdio pipes (IF-31).
When run hosts supervise the forward session out-of-process so it survives
a controller restart (IF-18), the run host runs `granule forward` as a
subprocess.

### `network: none` over granule forward {#IF-28}

A `network: none` granule sandbox (local, or Kubernetes in-cluster or
not):

- **boots with no NIC** (`CHV_NET_MODE=none`, as `kubernetes-granule`
  already does: `chvNetMode` in `pkg/orchestrator/k8s_granule_sandboxes.go`).
  A guest with no network device cannot send a packet anywhere, so there is
  nothing for a firewall to filter;
- **reaches its permitted services only through `-R` listeners on its own
  loopback** (IF-9's guest addresses). The set of listeners is the
  policy: the guest can reach a name only if the controller opened a
  forward for it;
- **resolves names this way.** At acquire, the provider writes
  `/etc/hosts`, listing each host the package cache intercepts
  (`pkgcache`'s host list) as `127.0.0.1`. It also writes
  `/etc/resolv.conf` with `nameserver 127.0.0.1` and `options use-vc`, so
  that glibc's and Go's resolvers query over TCP. Guest TCP 53 is forwarded
  to `coordinator:package-cache/dns`, which answers intercepted names with
  `127.0.0.1` and everything else with `NXDOMAIN`, as it does for
  zero-egress sandboxes today. A resolver that ignores `use-vc` (musl)
  still resolves the intercepted hosts from `/etc/hosts`, and fails
  closed on everything else;
- **fetches packages** at guest `127.0.0.1:80` and `:443`, which are
  forwarded to `coordinator:package-cache/http` and `/https`. TLS
  interception uses the internal CA, as today. The cache attributes the
  request to the forward's attempt (IF-11).

**This drops the nftables exception.** For a granule `network: none`
sandbox, the controller installs no chain at all:
`enforceGranuleHostIsolation`, `granuleIsolationRuleset`,
`restrictGuestDNS` and their cleanup do not run. The daemon also no longer
needs `nft` as root for this. The chain stays only for local `network:
open` sandboxes that a deployment isolates by other rules, and for plain
Docker `network: none` sandboxes, which are not VMs and keep today's
mechanism. A test asserts that a `network: none` granule guest has no
interface other than `lo`, and that a connection to the gateway and to the
internet fails while `git fetch` through `127.0.0.1:9728` succeeds.

### The package cache, by carrier {#IF-29}

| carrier | where the cache is | how the guest reaches it | per [483] |
|---|---|---|---|
| direct (local, `network: open`) | the controller's `pkgcache` | gateway 80, 443 and 53 interception, as today | baseline: `go mod download` 4.04 s, npm fetch 0.022 s |
| granule forward, local (`network: none`) | the controller's `pkgcache` | `-R` loopback 53, 80 and 443 (IF-28) | 4.30 s, 0.41 s. Acceptable. About 15 CPU-s/GiB is bounded by the budget |
| granule forward, remote, **stage 1** | the controller's `pkgcache` | `-R` loopback 53, 80 and 443. The same in `network: open` guests, so that remote guests never fetch packages from the internet behind the cache's back | 8.58 s, npm 3.55 s, apt replay 4.92 s. Each tarball takes at least one round trip, and all of it shares the sandbox's ~40 MiB/s with MCP and git |
| remote, **co-located** (stage 4, IF-33) | a `pkgcache` instance in the cluster's region (a Deployment and a Service in the sandbox namespace), filled from upstream | `network: open` guests: direct, over pod networking, at the Service address, with interception as on a local direct route. `network: none` guests: still granule forward to the controller's cache (they have no NIC) | removes the round trips and the per-tunnel bytes for open guests. [483] recommends this |

**Recommendation: co-locate a cache with each remote cluster** once more
than a handful of remote sandboxes run at a time. Tunnelled, the cache
doubles `go mod download`, and it is the largest consumer of the
~40 MiB/s per tunnel and of the controller's CPU (11 to 20 CPU-s/GiB)
[483, Package cache]. It is not in stage 1, because stage 1 adds nothing
to the cluster except the granule pods. Until then the forward to the
controller's cache is the route, and after it the forward remains the
fallback when the co-located cache is down. The same applies to the
Actions cache for remote CI.

### Kubernetes limits and reconnect {#IF-30}

From [483]'s concurrency and soak results:

- **Session cap.** With 64 tunnels (128 `kubectl exec` sessions from one
  controller to one API server), the first session failure appeared
  (`connect: connection timed out`). Up to 64 sessions there were none.
  Each forwarded sandbox holds one long-lived session (F-1). Its other
  sessions are short exec sessions for materialization and for recreating
  the tools server, because tool calls no longer use exec (IF-5). So
  the controller caps **forwarded sandboxes at 32 per cluster**
  (`-k8s-max-forwarded-sandboxes`, default 32). This leaves room for an
  equal number of concurrent short execs before the measured failure
  point. At the cap, acquisition waits for capacity (IF-8 step 2). It is
  not an error. Raising the cap needs a measurement above 64 tunnels, or
  a route that does not pass through the API server (a co-located
  executor; see Open questions).
- **Bytes per tunnel.** Each tunnel tops out at 38 to 49 MiB/s, and one
  stream at about 19 MiB/s. That is enforced by IF-13's rate and budget,
  not by counting calls.
- **Exec setup.** Setting up a new session takes 0.52 s at p50 and
  0.61 s at worst. That is the floor on a reconnect, and the reason tool
  calls do not use exec.
- **Stability.** Over 60 minutes, two tunnels (one idle, one busy at
  25,189 calls) saw 0 errors and 0 drops, and their latency did not drift.
  A 2-hour soak and a deliberate API-server or kubelet restart were not
  measured. Control-plane upgrades, kubelet restarts and
  `streaming-connection-idle-timeout` (4 h by default) still end
  sessions, so grain must reconnect:
  - **Keepalive.** Every 30 s, the forward session sends a keepalive at
    the protocol level (F-7). This also keeps the session from hitting
    the idle timeout.
  - **Reconnect.** When the process exits or reports `down`, the
    provider emits `carrier_down`, and restarts it with exponential
    backoff (0.5 s, doubling, capped at 30 s, with jitter) until the
    sandbox is released. It then re-adds every forward of the attempt
    and emits `carrier_up`. Streams in flight reset: git and package
    managers retry, MCP over Streamable HTTP reconnects with
    `Mcp-Session-Id`, and a client of a sandbox port sees a reset.
  - **Health.** `forward.health` reports `degraded` while reconnecting.
    A carrier that stays down for 5 minutes is an outage for the attempt
    (IF-8).
  - **Accounting.** Each session reports its bytes and streams, which
    are shown per cluster, so that an operator can see the cap and the
    budget approaching.

The local forward (`docker exec`) has no API server in the path, so it
has no session cap. It reconnects by the same rules.

### Hairpinning {#IF-17}

A stream from one sandbox to a name that another sandbox registered (for
example, a task's sandbox calling a persistent sandbox's port) travels
sandbox A → controller → sandbox B. It crosses A's route and B's route,
spliced at the controller. It is correct in every combination of routes.
Policy (IF-13) allows it only for names the attempt was granted. Under
remote `granule-forward` it crosses the API server twice, and counts
against both sandboxes' byte budgets. When both sandboxes are in the
same cluster and are `network: open`, `Resolve` may later return B's pod
address instead, so that the bytes never reach the controller. That is
not part of any stage yet.

The agent CLI on a cloudtop (IF-19) is also a hairpin: executor →
controller → sandbox.

### Controller restart {#IF-18}

- **Direct routes** behave as today. The fixed port block means that a
  restarted daemon serves the same addresses, and sandboxes holding them
  carry on (`NewDirectWithPortBlock`).
- **granule-forward routes.** The `granule forward` processes run in each
  attempt's run host, so they outlive the daemon. While the daemon is
  down, their `-R` targets (the daemon's private listeners) refuse
  connections, so a guest's `git fetch` fails and is retried, exactly as
  a direct route's does today. On start, the new controller lists its
  sandboxes by label (the existing adoption pass,
  `orchestrator.AdoptHost`), and reads each live attempt's forward table
  and tokens from the store. Then it does the following:
  - it rebinds each private listener at the same path, or the same port,
    which is recorded in the store;
  - it re-attaches to the surviving forward process through its control
    channel (F-5), or restarts the process if it is gone;
  - it checks the forward set and re-adds anything missing.

  A sandbox sees a pause of a few seconds.
- **Agent CLIs and MCP fronts** are adopted from their run hosts
  (`pkg/runhost`), as today. The front's listener address is recorded in
  the store, so the URL the agent CLI was given stays valid.
- **Registrants** (executors and remote capability servers) see their
  control session drop. They retry with the same backoff, and
  re-register their names under the same registrant identity, which the
  IF-10 duplicate rule accepts. **Forwards on an executor** (an agent
  CLI's `sandbox:<attempt>/mcp`) are kept by the executor across the gap.
  Their streams reset, and the MCP client retries.

## Required cases, end to end {#IF-19}

Each case below must work on every carrier. Each is an integration test:

- **direct**: on Docker and local granule, as today;
- **granule forward, local**: a local granule `network: none` sandbox;
- **granule forward, remote**: a kind cluster with a `kubernetes-granule`
  pod and the controller outside the cluster, driving it through a
  kubeconfig. This is the same code path as GKE, with the same `kubectl
  exec` streaming.

**C** is the controller, and **GF** is the sandbox's `granule forward`
session. Under granule forward, local and remote differ only in what
F-1's exec goes through (`docker exec` or `kubectl exec`), so each case
is written once for both.

### `git` to `coordinator:git-proxy`

- **Direct.** The same as today. `ConfigureGitCredentials` writes
  `http://<Resolve address>/...` plus the bearer token, and `git fetch`
  connects to C's git proxy listener.
- **granule forward.** At acquire, C opens `forward.open {listen: sandbox
  127.0.0.1:9728, target: coordinator:git-proxy}`. C binds the private
  listener `/run/grain/fwd/<attempt>/<id>.sock`, and adds `-R
  9728:unix:<that path>` to GF. The guest's `.git-credentials` names
  `http://127.0.0.1:9728`, with the bearer token as today. `git fetch`
  connects to guest loopback. granule's agent carries the connection over
  vsock and the exec session, and GF dials the private listener. C
  attributes the connection to the attempt, checks policy, and hands it
  to the git proxy's handler, which checks the bearer token as it always
  has. Expected cost [483]: +0.7 s (6%) locally for a full clone, and +1 s
  (shallow) to +4 s (full) remotely.

### `npm` and `go` to `coordinator:package-cache`

- **Direct.** The same as today: DNS interception at the gateway, 80 and
  443 to the cache, with attribution by source address.
- **granule forward.** IF-28's arrangement: `/etc/hosts` and `use-vc`
  resolution to `127.0.0.1`, and `-R` 53, 80 and 443 (plus 9729 for the
  explicit `GOPROXY`, pip and apt configuration) to the cache's names.
  `npm install` resolves `registry.npmjs.org` to `127.0.0.1`, connects to
  guest 443, and is carried to the cache's interception listener, which
  terminates TLS with the internal CA. `go mod download` takes the same
  path, at `GOPROXY=http://127.0.0.1:9729/...`. In stage 1 this is also the
  path for remote `network: open` guests (IF-29).

### The agent CLI on a cloudtop to `sandbox:<attempt>/mcp`

The front (IF-5) is a per-attempt `grain mcpserver` in a run host beside
C, listening on a private address that C records. C registers it as
`sandbox:<attempt>/mcp`.

- **Agent on the controller host, direct sandbox.** `session.start` gets
  the front's address as `grain-sandbox`. The front runs sandbox tools by
  exec per call (`docker exec`, `granule exec`). This replaces the forked
  `grain mcpserver -granule-vm|-container ...`.
- **Agent on the controller host, granule forward (stage 1).** The same,
  except that the front passes sandbox tools through to
  `sandbox:<attempt>/tools`: GF has `-L <private>:9760`, so each tool
  call is one stream into the guest tools server, which forks the
  command. Grain-side tools never leave the controller.
- **Agent on a cloudtop, through an executor.** The executor is
  connected to C (IF-20). C opens `forward.open {listen: executor:<id>
  127.0.0.1:0, target: sandbox:<attempt>/mcp}`. The executor binds that
  address, and streams each connection to C over its own mux with the
  IF-11 header. C splices it to the front. The rest is either of the
  two cases above. This is how `pkg/modelexec` tunnels MCP calls today,
  generalized to a name.

### A capability's `ports` output (§5.3)

- **Direct.** The same as today. The guest forwarder (started by exec)
  listens on `127.0.0.1:<sandbox_port>`, and upgrades each connection to
  the `coordinator:capability-ports` relay with its forward token. The
  relay dials `host_port` and checks its owner (§5.3.2).
- **granule forward.** C opens `forward.open {listen: sandbox 127.0.0.1:
  <sandbox_port>, target: capability:<id>/port/<host_port>}`, which
  becomes an `-R <sandbox_port>:<private listener>` added to the live
  GF (F-5). C serves the name by dialling `host_port` locally, after the
  same ownership check, or relays the stream to the executor that
  registered the name. No guest forwarder and no relay port are needed.

### A staging UI

- **Direct.** `/staging/<name>/` resolves to
  `sandbox:persist-<name>/port/<n>`, which C registers with the
  sandbox's direct address. The reverse proxy dials it, as
  `handleStagingProxy` does today.
- **granule forward.** The same route. The persistent sandbox's GF has
  `-L <private>:<n>`, and the reverse proxy's `Dial` opens a connection
  to that private listener. SSE and WebSockets work, because each one is
  a TCP connection with half-close.

## Remote deployment {#IF-20}

This section is how the objects come to run away from the controller.
It is not part of the model: everything above works with every object
in-process.

- **Sandboxes on Kubernetes need no in-cluster component** in stage 1,
  beyond the granule VM pods themselves. The controller drives the
  cluster through a kubeconfig (`pkg/k8s` runs `kubectl` and honours
  `KUBECONFIG`), and carries traffic with granule forward. The co-located
  cache (IF-33) is the first optional in-cluster component.
- **Transport for executors.** An executor dials out to the controller
  (an HTTPS upgrade, as `grain executor` does today), and runs one
  `pkg/tunnel/mux` session over it. Stream 1 is MCP: the executor serves
  the agent and capability interfaces of the providers it hosts, and
  calls `endpoint.register` and the rest of the control plane. Other
  streams are data-plane streams (IF-11).
- **The grain executor** is hostd generalized. hostd starts only what
  `/etc/grain/hostd.yaml` names, with each argv, environment and working
  directory checked against the entry (`pkg/hostd/allowlist.go`). The
  executor keeps that rule, and adds a transport and object types. Its
  allowlist is **a local list of named commands with argument
  contracts**. Each entry gives:
  - the object type it provides (`agent` or `capability`);
  - the command;
  - a schema for each argument and environment variable it accepts.

  Presets are shipped as reviewable files in the release, and an operator
  enables them by name: `agent-agy`, `agent-claude`, `agent-codex`,
  `capability-gcp`, `capability-host-scripts`. The controller can only ask
  for an entry by name, with arguments that its contract accepts. It
  cannot run anything else on the executor's machine.
- **Identity, scopes and revocation** (tasks 441, 442 and 444). Each
  executor has its own identity: a key pair registered with the
  controller, and an IAP identity where IAP fronts it
  (`pkg/modelexec/iap_auth.go`). Its scopes list the object types and
  preset names it may provide, and the names it may register
  (`executor:<id>/...`, and `capability:<id>/...` for the capabilities it
  hosts). Revoking an executor closes its session, unregisters its names,
  and treats its in-flight attempts as outages.
- **Version skew.** Each MCP interface carries `protocol: <n>` in
  `*.describe`. The controller supports the current version and the one
  before it. It refuses to schedule onto an executor that is outside
  that range, and says so in the executor's health. The guest tools
  server is copied in at the controller's version (IF-5), so it cannot
  drift. The vendored granule version is recorded in `sandbox.describe`,
  and the guest image's `granule-agent` must support forward. A provider
  whose guest agent predates 480 declares no `granule-forward` routes,
  so `network: none` and remote placement are not offered on it.
- **Drain.** `executor.drain` stops new sessions. Work in flight
  finishes or reaches its deadline, and then the executor reports
  `drained`.
- **Offline.** A dropped executor session is a lost agent for each
  session it ran, and a lost registrant for each name it served. Both
  reconnect for 5 minutes, and after that they are outages (IF-8).

## Mapping today's mechanisms

| today | under this design |
|-------|-------------------|
| `agent.Framework` and the antigravity, claude, codex and custom frameworks | agent providers (IF-2), each behind an adapter. `RunConfig`'s backend fields are removed in stage 2 |
| `agent.TerminalFramework`, console sessions | `session.start {interactive: true}` and `session.send` |
| `grain executor` / `pkg/modelexec` | an executor (IF-20) hosting `agent-*` presets. Its MCP tunnel becomes forwards to `sandbox:<attempt>/mcp` |
| grain hostd | the executor's allowlist model, with a local transport only. hostd stays as the unix-socket transport of an executor on the same machine |
| run hosts (`pkg/runhost`) | where an attempt's long-lived processes run so that they survive a controller restart: the agent CLI, the MCP front, and the `granule forward` session. `session.events {from_seq}` is the spool cursor |
| `GranuleSandboxes`, `ContainerSandboxes`, `K8sSandboxes`, `K8sGranuleSandboxes`, `HostSandboxes` | sandbox providers (IF-3 to IF-6). Their optional interfaces (`SandboxPlacer`, `rootedSandbox`, ...) are replaced by `exec` |
| the `granule` backend | `direct` routes for `network: open`, and `granule-forward` routes with no NIC for `network: none` (stage 3) |
| the `docker` backend | `direct` routes, unchanged |
| the `kubernetes` backend | `direct` routes in-cluster. Refused out of cluster (IF-16, decision 5) |
| the `kubernetes-granule` backend | in-cluster: `direct` for `network: open`, and `granule-forward` for `network: none`. Out of cluster: `granule-forward` for every name (stage 1) |
| persistent and standing sandboxes | sandboxes acquired with a stable name and no attempt deadline. Their ports are `sandbox:persist-<name>/port/<n>`, and their forward session lives as long as they do |
| capability servers (`pkg/capability/servers`, tasks 432/434) | capability providers. Out-of-process servers register their names through an executor |
| `pkg/capability/portforward` (guest forwarder, relay) | the `direct` route for `capability:*/port/*`, kept as is. `granule-forward` routes need neither part |
| `Fabric`, `Direct`, `SandboxPortOffsets`, `SandboxAdvertiseHost`, `AdvertiseVia` | the direct resolver (IF-14). The offset table maps names to ports, both on the gateway and on guest loopback |
| `ContainerGitProxyHost`, `GranuleGitProxyHost`, `K8sGitProxyHost`, `$GRAIN_POD_IP` | inputs to the direct resolver. They are no longer read anywhere else |
| `-k8s-git-proxy-host` (required for k8s backends) | not required when the routes are `granule-forward` |
| the fixed-address proxies (git 9728, cache 9729, Actions cache 9730, model proxies 9744+) | `coordinator:*` names. Same port numbers, on the gateway for `direct` and on guest loopback for `granule-forward` |
| nftables `network: none` (`granule_firewall.go`) | removed for granule guests, which get no NIC (IF-28). It stays for plain Docker |
| `restrictGuestDNS` | replaced by `/etc/hosts`, `use-vc` and the forwarded DNS name (IF-28) |
| `handleStagingProxy` | an HTTP forward by path (IF-12) |
| `recreate_sandbox` | `sandbox.recreate` plus re-materialization. Names survive it. A remote sandbox gets a new forward session and tools server |
| `grain mcpserver -granule-vm/-container/-k8s-pod/-sandbox-root` | the front registered as `sandbox:<attempt>/mcp`. `-sandbox-root` also serves as the guest tools server |
| `pkg/mcp` `DialStream` / `grain-connect` (one connection per exec) | still valid for one-off connections. Long-lived connections use the forward session instead |
| `pkg/tunnel/mux` | the executor transport. It is not used for sandboxes |
| `scripts/bench`, `tests/bench/tunnelbench` | re-run with `granule forward` (IF-16, decision 3) |

## The grain ↔ granule split {#IF-31}

`pkg/granule` stays a shallow dependency: it wraps the vendored `granule`
CLI (`tool github.com/bwsalmon/granule/cmd/granule` in `go.mod`) and
`github.com/bwsalmon/granule/pkg/forward`, and it knows nothing of names,
policy or attempts. Today `pkg/granule/forward.go` (`d7d5e032b`) provides:

```go
// pkg/granule (landed in d7d5e032b)
type ForwardSpec struct {
	BindHost   string
	BindPort   int
	TargetHost string
	TargetPort int
}

type KubectlForwardConfig struct {
	Kubectl, Kubeconfig, Namespace, Pod, Container string
	RemotePorts                                    []int
	Remotes, Locals                                []ForwardSpec
	OnReady                                        func()
}

func LoopbackRemoteSpecs(ports []int) []ForwardSpec
func (c Command) ForwardArgs(cfg KubectlForwardConfig) []string
func (c Command) Forward(ctx context.Context, cfg KubectlForwardConfig) error
func KubectlForwardArgs(cfg KubectlForwardConfig) []string
func ForwardKubectl(ctx context.Context, cfg KubectlForwardConfig) error
func ServeForwardClient(ctx context.Context, r io.Reader, w io.Writer, remotes, locals []ForwardSpec, onReady func()) error
func ServeForwardGuest(ctx context.Context, r io.Reader, w io.Writer, remotes []ForwardSpec) error
```

To support local Docker VMs alongside Kubernetes pods and dynamic
addition/removal of forwards on a live session (IF-27 F-4, F-5),
`pkg/granule` grows a session handle:

```go
// pkg/granule (with local Docker target and IF-27 F-4 / F-5)
type ForwardTarget struct {
	Docker     string // the VM container, for a local VM
	Kubeconfig string // and Namespace, Pod, Container, for a VM in a pod
	Namespace, Pod, Container string
}

type Forward struct{ /* one granule forward session */ }

func StartForward(ctx context.Context, t ForwardTarget) (*Forward, error)
func (f *Forward) Local(local, guestPort string) error  // -L
func (f *Forward) Remote(guestPort, local string) error // -R; local may be "unix:/path"
func (f *Forward) Remove(spec string) error
func (f *Forward) Events() <-chan ForwardEvent          // ready, down, error
func (f *Forward) Close() error
```

Everything above that (`pkg/endpoint`'s resolver, the private
listeners, policy, budgets, reconnect, adoption) is grain's. granule
owns the VM, its exec, its agent and the forward's wire protocol. grain
asks nothing else of granule for this design. In particular, grain does
not ask granule to know about names, tokens or the git proxy.

## Scheduling across the three {#IF-21}

`sandbox.describe` reports:

```
{capacity: {cpus, memory_mb, sandboxes, forwarded_sandboxes},
 isolation: vm|container|process, nested_virt,
 network_position: same-host|routable|unreachable,
 routes, granule_forward: bool, trust_tier, overcommit}
```

`agent.describe` reports the models, the login and the quota.

- The scheduler picks an agent first, because quota is the scarcest
  resource and a usage limit is known before anything is built. It then
  picks a sandbox provider that satisfies the task's shape, network and
  isolation requirements, and then mints capabilities.
- A `network: none` task is placed only on a provider that either has
  `granule_forward` or keeps today's direct mechanism (plain Docker).
- **Trust tiers** (task 459). A task's trust tier must be at or below the
  tiers of the providers it is placed on. Budget and auto overcommit apply
  per sandbox provider. An OOM kill under overcommit is the scheduler's
  outage (IF-8).
- **Capacity.** A remote cluster's `forwarded_sandboxes` is capped by
  IF-30 (32 by default), on top of its node capacity.
- **Latency** [483]. A remote sandbox adds about 36 to 40 ms to each tool
  call (it was about 0.5 s per call over `kubectl exec`), and 1 to 4 s to
  a clone. It doubles a tunnelled `go mod download` until a cache is
  co-located. The scheduler prefers a local provider when two are
  otherwise equal. When it has the choice, it places an agent on the
  executor nearest the agent's sandbox.

## Threat model and isolation between attempts {#IF-22}

Assets: the controller's state and credentials, each attempt's grants,
and the operator's networks.

**Who can forward.** Anyone who can exec into a VM container can run
`granule forward` against it, and so reach that guest's loopback, or
offer the guest listeners. That means anyone with the Docker socket on a
local host, or `pods/exec` on the sandbox namespace on a cluster. This is
the same set of principals that can already run any command in the guest
through `granule exec`, so forwarding grants nobody a new reach. It does
mean that `pods/exec` on the sandbox namespace must be held only by the
controller's identity. The stage-1 kubeconfig needs `pods` (create,
get, list, delete, watch) and `pods/exec` in that one namespace. It does
not need `pods/portforward`.

| threat | control |
|--------|---------|
| a sandbox reaches another attempt's services | names are scoped by attempt. A guest can reach only the `-R` listeners of its own session, and each lands on a private listener that serves only that forward (IF-11). Policy is checked at `forward.open` and at every stream |
| a guest reaches anything with no forward (`network: none`) | it has no NIC (IF-28). Its only path out is its own forward session, whose listener set is the policy |
| a guest tampers with its forward | the guest end is granule's in-guest agent. A root guest can kill it (a carrier drop, its own loss) or connect to its own listeners, both of which it may already do. It cannot open a forward the controller did not add, because forwards are added from the controller's side of the exec |
| another process on the controller host connects to an attempt's private listener | with F-4 (unix sockets), only the daemon's account can. Without it, any local process can reach a loopback port, which matters only where it is not checked again: the git and model proxies check bearer tokens. The package cache would misattribute a request (an accounting error, not an access path), because the cache serves only public packages. Agent CLIs run under hostd as another account, but on a loopback port they are in the same position, so F-4 is required before untrusted code runs on the controller host |
| the guest tools server is subverted | it runs as `debian` in the guest and holds nothing. Grain-side tools and the API token are in the front, on the controller (IF-5) |
| a sandbox impersonates a registrant | registration is controller-only for `coordinator:*`. For other kinds it is limited to the registrant's identity and scopes. A sandbox registers nothing: its provider registers `sandbox:<attempt>/...` for it |
| a sandbox floods the controller or the API server | per-attempt and per-name limits, the byte budget and rate (IF-13), and the per-cluster session cap (IF-30) |
| the kubeconfig is misused | stage 1 needs the namespace-scoped RBAC above and nothing else. The kubeconfig lives with the controller, never in a sandbox |
| a compromised executor | it can run only its allowlist. It can register only its scoped names. Revocation closes it out (IF-20) |
| a token leaks into a transcript | forward tokens never enter the guest on `granule-forward` routes. On direct routes, today's tokens stay where they are today, and redaction (`pkg/redact`) covers them |
| the controller accepts inbound connections | stage 1 adds none. Every carrier is dialled by the controller (`kubectl exec`) or by an executor (dial-out) |

An attempt's sandbox may reach only:

- its own `sandbox:` names;
- the `coordinator:` names that policy allows it;
- the `capability:` names of its own grants.

When the attempt ends every forward and its session close, so nothing of
the attempt outlives it.

## Migration in stages

### Stage 1: off-cluster controller, granule forward per pod {#IF-23}

Stage 1 is task 482 (initial runtime landed in `d7d5e032b`, task 652). A
controller on a corp VM, which accepts no inbound connections, drives
`kubernetes-granule` sandboxes in a GKE cluster in another project. It
uses `granule forward` (IF-27) and a guest image whose `granule-agent`
supports forward.

- **Configuration.** `-k8s-kubeconfig` (or `KUBECONFIG`, or auto-discovered
  `<data-dir>/kubeconfig` at `/var/lib/grain/kubeconfig`) names the remote
  cluster, and `-k8s-sandboxes` (with `-sandbox-backend=kubernetes-granule`)
  uses the `kubernetes-granule` backend. Routes are `granule-forward` for
  every name whenever the daemon is out of cluster (`!InKubernetesPod()`).
  The `-k8s-git-proxy-host` requirement (`pkg/cli/daemon.go`) is lifted
  for that case (`d7d5e032b`), and `SandboxAdvertiseHost` advertises
  `127.0.0.1`. `-k8s-max-forwarded-sandboxes` defaults to 32.
- **Pods.** These are unchanged `kubernetes-granule` pods. There is no
  sidecar, no Secret, and no grain process in the pod outside the guest.
- **Carrier.** At acquire, the provider starts `granule forward` against
  the pod's VM container through `kubectl exec`, in the attempt's run
  host. It adds the `-R` forwards for `coordinator:git-proxy`, the
  package cache (53, 80, 443 and 9729), and `coordinator:model-proxy/*`
  where policy allows. It adds the `-L` forward to the guest tools
  server, and an `-L` forward for each capability port and staging port
  as they arrive. Reconnect, keepalive, health, budget and cap follow
  IF-13 and IF-30.
- **Guest.** At acquire, one exec writes `/etc/hosts`, `/etc/resolv.conf`
  and the CA, copies in `grain` at the controller's version, and starts
  the guest tools server. `network: none` pods boot with no NIC.
  `network: open` pods keep `inline` networking for the internet, but
  reach grain's services only through the forward.
- **Out of the pod:** the sandbox tools half of `sandbox:<attempt>/mcp`
  (through `sandbox:<attempt>/tools`), and `sandbox:persist-<name>/port/<n>`
  for staging. The agent CLI runs on the corp VM (in the daemon's
  container, or through hostd for `agy`), and its front runs beside it.
- **Not in stage 1:** no relay, no load balancer, no inbound connection
  to the corp VM, no grain tunnel agent, no in-cluster component, no
  co-located cache, and no executor. Direct routes change only in that
  their addresses come from `Resolve` (IF-14).
- **Done when:** the IF-19 cases pass against a kind cluster with an
  out-of-cluster controller, and one real task runs on the GKE cluster
  end to end (clone, `npm install`, push, a pull request opened). The
  task must survive a forced carrier drop (kill the forward process) and
  a controller restart mid-run, and must report its forward bytes.

### Stage 2: one sandbox interface, MCP by name {#IF-24}

Every sandbox provider implements IF-3 to IF-6, and registers
`sandbox:<attempt>/mcp` through the front. Every agent provider is given
URLs, not backends. `RunConfig`'s backend fields and the backend-specific
`grain mcpserver` flags are removed.

### Stage 3: granule forward for local `network: none` {#IF-32}

Re-run [483]'s local benchmark with `granule forward` in place of
`docker exec` (the swap its "What was not measured" section describes).
If the per-byte CPU is within twice the `docker exec` figure, local
granule `network: none` sandboxes boot with no NIC and take
`granule-forward` routes for every name (IF-28). The nftables chain and
`restrictGuestDNS` are then deleted for granule. In-cluster
`kubernetes-granule` `network: none` sandboxes gain the same routes. If
the CPU is above that, stage 3 stops and the operator decides between the
CPU and the chain. `network: open` sandboxes are not moved (IF-16,
decision 2).

### Stage 4: co-located caches for remote clusters {#IF-33}

A `pkgcache` (and Actions cache) Deployment and Service in each remote
cluster's sandbox namespace, filled from upstream. Remote `network: open`
guests get `direct` routes to it, while `network: none` guests keep their
forward to the controller's cache. This is the first in-cluster component.
It is optional for each cluster, and recommended once a cluster regularly
runs more than a few sandboxes at a time (IF-29).

### Stage 5: agents and capabilities through the executor {#IF-26}

`grain executor`'s agent role and remote capability servers move onto
IF-20: allowlisted presets, scoped identities, and names registered over
the control plane. `pkg/modelexec`'s own MCP tunnel is retired.

## Changes from the previous revision

- **IF-15 (grain's tunnel agent) is dropped.** granule forward replaces
  it for granule sandboxes, and other remote sandboxes are not supported
  (IF-16, decision 5).
- **IF-25 (the in-cluster executor as sandbox carrier) is dropped.** It
  existed to take the API server off the data path at scale. The
  co-located cache (IF-33) takes the bulk off instead, and the session
  cap (IF-30) is the bound until something needs more (see Open
  questions).
- `kubectl port-forward` is not used. [483] found it no better than exec
  for MCP, and clearly worse with 32 streams sharing one connection (the
  model-proxy table). It also needs one more RBAC verb.
- IF-3, IF-5, IF-6, IF-11, IF-13, IF-14, IF-16 to IF-24 changed. IF-27 to
  IF-33 are new.

## Alternatives considered

- **grain's own tunnel agent everywhere (task 478, the previous
  revision).** A `grain tunnel-agent` in each pod would carry a
  `pkg/tunnel/mux` session over `kubectl port-forward` or `exec`. It
  would work for non-granule pods too. It was rejected: it has the same
  cost profile as granule forward [483], it is a second implementation
  of the same thing, and it protects a sidecar in a shared network
  namespace with only a session key. It remains the design to return to
  if a non-granule remote sandbox is ever required.
- **A generic executor as the whole model (task 467).** Every object
  would be "a process an executor starts". That is good for deployment,
  but it says nothing about what an agent or a sandbox *is*, so every
  caller would still need backend-specific knowledge. It is kept as the
  remote-deployment section (IF-20), not as the model.
- **A connector with typed offers (task 462).** Remote machines would
  offer typed resources that the controller binds. It overlaps with
  capabilities, and needs its own protocol. The three interfaces plus
  named endpoints cover it with MCP alone.
- **An in-cluster relay plus a load balancer or IAP (task 457).** This
  needs inbound reachability to something, a new deployed component, and
  its operation. The operator ruled out inbound connections to the corp
  VM, and the Cloud Run relay was removed (docs/relay-removed.md).
  granule forward needs only `pods/exec`, which grain already uses.
- **URLs everywhere.** Every service would be handed out as a URL that
  the operator makes reachable. That does not work when the sandbox cannot
  reach the controller at all, and it leaves policy to network
  configuration instead of grain.
- **granule forward for every local sandbox too.** That would give one
  carrier everywhere. It was rejected for `network: open`, because [483]
  shows the bridge is 30 to 100 times faster for bulk at about 1/25 of
  the CPU, for no gain in isolation (IF-16, decision 2).
- **exec per tool call for remote sandboxes** (no guest tools server). It
  would leave no grain code in the guest, but it costs 0.52 s and one new
  API-server session per call [483]. That is about nine minutes over a run
  of a thousand tool calls, and churn against the session cap.

## Open questions

- **IF-27 extensions (`third_party/granule/pkg/forward`).** The initial
  `granule forward` patch (`gke-labs/granule#13`, `d7d5e032b`) implements
  F-1, F-2, F-3, F-6, F-7 and F-9. Still open as extensions are F-4
  (unix-socket local endpoints, used by IF-11 and IF-22), F-5 (dynamic
  runtime add/remove of `-L`/`-R` specs without restarting the session),
  and F-8 (BDP-sized per-stream flow-control windows).
- **More than 32 remote sandboxes per cluster.** That needs either a
  measurement above 64 sessions to one API server, or a route that does
  not pass through the API server. That route would be an in-cluster
  executor carrying forward sessions (the dropped IF-25), or the pod
  network from an in-VPC controller ([483]'s unmeasured carrier 5). Not
  decided until it is needed.
- **The byte budget's default** (4 GiB per attempt on remote routes) is
  a guess from [483]'s workloads. The operator may want another value.
- **Remote `network: open` package fetches.** Stage 1 sends them through
  the controller's cache over the forward. That is slower (IF-29) but
  keeps every remote fetch cached and attributed. The other choice is to
  let open guests fetch from the internet directly until stage 4.

## Review record

The brief asks for a review by a reader with no grain context. No second
model or person could be reached from the authoring sandbox, which has no
model CLI and no API key. As in the previous revision, the author did a
cold read as a newcomer would, and resolved what it found:

1. "Is the guest tools server the 'grain agent in the guest' that stage 1
   rules out?" No. IF-5 says what it is and is not: it carries no
   traffic, holds nothing, and can be replaced by exec per call at a
   stated cost (see Alternatives). Stage 1 rules out a tunnel agent.
2. "How does a stream carry a per-attempt token if git and npm cannot
   send one?" Through its listener: each forward has a private
   controller-side listener (IF-11). The residual risk without unix
   sockets is in IF-22.
3. "UDP DNS cannot cross a TCP-only forward." `/etc/hosts` plus
   `options use-vc`, with the musl fallback stated (IF-28).
4. "Why not use granule forward for every local sandbox, for
   uniformity?" Because of the measured cost (IF-16, decision 2, and
   Alternatives).
5. "What happens to an agent's MCP connection when the controller
   restarts?" The front, the agent CLI and the forward all live in the
   run host. Only the daemon's own listeners pause (IF-18).
6. "Who can open a forward?" Anyone with exec into the VM container,
   which is no one who could not already run commands there (IF-22).
7. "Where does each number come from?" Every figure cites a table in
   [483].

The pull request for this doc is open to a human reviewer for the
no-context reading the brief intended. Their points are to be resolved
here, or recorded under Open questions.
