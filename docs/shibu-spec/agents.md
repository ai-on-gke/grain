# Shibu agents, v1

*Kernel: the agent object (its status labels and exit status), its
standard endpoints, the session binding, the agent control MCP, the
harness contract and the VM `mcp` contract. Standard distribution: the
`create_agent` helper (`agent-helper/v1`); in the bootstrap set, a pool
runner and its adapters (`/sys/runner`), the agent base image, and the
model proxy (`/sys/model`)*
([design 4.1](../shibu.md#41-agents-a-kernel-composite)).

An agent is a namespace labelled `kind: agent`, holding an **agent
object**, a harness session on a runner (`runs_in`), grants on zero or
more VM `mcp` endpoints (`tools`), and the standard endpoints. There is
no agent allocator: a VM made for an agent is charged to an ordinary VM
allocator or grant, model access is a launch grant on the runner, and
counting agents is a label query.

**Kernel parts and distribution parts.** The kernel knows the agent
object and what hangs off it: the namespace made with
`create_namespace`'s `agent`, its standard endpoints, its status labels
and exit status, and the session binding it issues to its `runs_in`
runner. `create_agent` is not a kernel operation: it is a
standard-distribution helper (a library, `shibu agent create`, and the
`/sys/agents` component) that makes those kernel calls and then calls
the runner's `launch` ([`create_agent`](#create_agent)). Anything it
does, a caller can do itself with the same calls.

## Contents

1. [`create_agent`](#create_agent)
2. [The agent object](#the-agent-object)
3. [`stop` and `exit`](#stop-and-exit)
4. [Standard endpoints](#standard-endpoints)
5. [`messages`](#messages)
6. [`artifacts`](#artifacts)
7. [Status labels](#status-labels)
8. [The harness contract](#the-harness-contract)
9. [Adapter manifest](#adapter-manifest)
10. [VM `mcp` contract, `mcp/v1`](#vm-mcp-contract-mcpv1)
11. [Agent control MCP](#agent-control-mcp)
12. [Application MCP servers](#application-mcp-servers)
13. [Placements](#placements)

---

## `create_agent`

*Standard distribution* ([`agent-helper/v1`](extension-points.md#agent-helperv1)).
`create_agent` is a helper defined entirely in terms of kernel
operations and `runner/v1`. It runs as the caller, as a library, as
`shibu agent create` ([`cli.md`](cli.md)), or as the optional
`/sys/agents` component (`POST /sys/agents/create`, which acts as the
caller through the front door or through a grant of the caller's
`control` endpoint with `write`).

```json
{
  "path": "/factory/lead/workers/w17",
  "payer": "/factory/lead/workers/vm-std",
  "idempotency_key": "item-17",
  "dry_run": false,
  "spec": {
    "adapter": "claude-code",
    "model": "claude-sonnet-4",
    "runs_in": "/factory/lead/workers/runner",
    "tools": [
      {"endpoint": "vm/mcp"},
      {"endpoint": "/factory/lead/workers/build/b2/mcp", "ops": ["exec", "read", "write"]},
      {"name": "factory", "endpoint": "/factory/lead/tools"}
    ],
    "credential": "/users/alice/secrets/anthropic",
    "job": "Fix item 17: retry the flaky upload. Open a pull request when it passes.",
    "config": {"effort": "high"},
    "budget": [{"unit": "usd", "per_day": "20"}],
    "regrant_launch": false,
    "shape": {"cpu": 2, "ram": "8Gi", "disk": "40Gi",
              "volumes": {"/src": "/factory/lead/workers/src-17"}},
    "grants": [
      {"object": "/factory/lead/git", "ops": ["fetch", "push"], "limits": {"repos": ["acme/app"]}},
      {"object": "/factory/lead/egress", "ops": ["tunnel"], "limits": {"hosts": ["proxy.golang.org:443"]}},
      {"object": "/factory/lead/workers/vm-std", "ops": ["create"], "limits": {"cap": {"cpu": "8"}}, "as": "vm-alloc"}
    ],
    "title": "item 17: retry the flaky upload",
    "lease": "8h",
    "keep": "5Gi"
  }
}
```

Returns the agent object ([The agent object](#the-agent-object)).

| Field | Required | Meaning |
| --- | --- | --- |
| `path` | yes | The agent's namespace. It must be in the caller's subtree. |
| `payer` | when the call creates a VM | A VM allocator or grant the caller holds. The VM the call creates is charged to it. |
| `spec.adapter` | yes | An adapter the `runs_in` runner lists in `adapters`, and the caller's launch grant allows. |
| `spec.model` | yes | The model, a [launch parameter](extension-points.md#runnerv1). It must be inside the launch grant's `models`, if it has any. |
| `spec.runs_in` | no | A runner endpoint the caller holds `launch` on: `/sys/runner`, a handle granted from it, or `<vm>/runner` on any VM. Default: the runner of the VM this call creates (`<path>/vm/runner`). |
| `spec.tools` | no | MCP endpoints for the harness: VM `mcp` endpoints (`{"endpoint", "ops"}`, ops from `exec`, `read`, `write`, all three by default), and [application MCP servers](#application-mcp-servers) (`{"name", "endpoint"}`). `vm/mcp` names the VM this call creates. Default: `[{"endpoint": "vm/mcp"}]`. `[]` is valid: an agent with no VM tools. |
| `spec.credential` | no | A **reference** to a secret, passed to the runner in `launch`. The runner reads it through its own `read` grant; the helper never reads it, and the agent gets no grant on it. Without one, the runner uses an account of its own, mapped from the caller by its policy. |
| `spec.job` | yes | What the agent is asked to do. A string, served at `<path>/job`. |
| `spec.config` | no | Non-secret adapter settings, validated against the manifest's `config`. |
| `spec.budget` | yes | The agent's budget ([`kernel-api.md`, Budgets](kernel-api.md#budgets)), as a limit on the launch grant the agent's namespace receives, narrowed from the caller's grant on `runs_in`. |
| `spec.regrant_launch` | no | `true` gives that launch grant `regrant: true`, as the handle `<path>/runner`, so the agent can launch agents of its own. Default `false`. |
| `spec.shape` | no | The created VM's `cpu`, `ram` and `disk` (image defaults otherwise), and `volumes` to attach: volumes in the caller's subtree, or handles created by this call's `grants`. |
| `spec.grants` | no | Grants the caller issues to `path`, as in `create_namespace`. Outside access is one of them, a grant on `/sys/egress`. |
| `spec.title` | no | The `title` label ([Status labels](#status-labels)). |
| `spec.lease` | no | A lease on the namespace, the only thing besides `delete` that deletes it ([design 2.2.6](../shibu.md#226-leases-and-ttls)). Allowed to be absent. The agent's exit never deletes the namespace, so a finished agent's chat stays readable. |
| `spec.keep` | no | A deposit area in the caller, appearing in the agent as `<path>/keep` ([`kernel-api.md`, `create_namespace`](kernel-api.md#create_namespace)). |

**What it does.** Three steps, each a call anyone could make:

1. **One kernel [`batch`](kernel-api.md#batch)**, all or nothing:
   - `create_namespace` at `path` with `agent: {adapter, model,
     runs_in, tools, job, config, title}`, its lease, `keep` and the
     `grants`. The kernel labels it `kind: agent`, `adapter` and
     `title`, and creates the agent object and the standard endpoints
     `job`, `messages`, `artifacts`, `exit`, `agent-mcp` and `usage`;
   - unless `runs_in` and `tools` both name existing handles, `create`
     of a **VM**, `<path>/vm`, through `payer`, booted from the agent
     base image, with restart policy `on-failure`. It serves `mcp`
     ([VM `mcp` contract](#vm-mcp-contract-mcpv1)) and `runner`
     ([`runner/v1`](extension-points.md#runnerv1));
   - `grant`s on each VM `mcp` named in `tools`, with the entry's ops,
     from the caller's own (by the tree, or a grant with `regrant`);
   - the **launch grant**: `grant` of `runs_in` to `path`, ops
     `["launch"]`, limits `{budget, adapters: [adapter], models:
     [model], sessions: 1}` (no `sessions` with `regrant_launch`),
     `regrant: regrant_launch`. The caller must
     be able to grant from `runs_in`: it owns the runner by the tree,
     or holds its launch grant with `regrant: true`;
   - when the runner reaches models through `/sys/model` (its
     `/_contract` metadata reports `model_route: sys-model`), a `grant`
     of the caller's `/sys/model` handle to `path`, with the `budget`
     and, if a `credential` is named, the contract limit
     `credential: {<vendor>: <secret>}`. The session calls
     `/sys/model` as the agent, through its binding, so the meter
     charges the agent's chain.
2. **The runner's `launch`**, `POST <runs_in>/launch` with the agent's
   path, adapter, model, `tools`, `credential` (a reference) and config
   ([`runner/v1`](extension-points.md#runnerv1)). The caller makes it
   through its own launch grant. The runner fetches the session binding
   from the kernel, which issues one only to `runs_in`, and only while
   the agent's namespace holds a live launch grant on it
   ([The agent object](#the-agent-object)).
3. **On a failed launch**, `delete` of the namespace, so nothing is
   left behind, and the runner's refusal returned.

Pinning: while a session lives on a VM's runner, that VM can't be
adopted or moved, and deleting it stops the agent. `runs_in` can be
changed only between sessions, with `POST <path>/agent/runs_in` on the
agent's standard `agent` endpoint, which needs `write` on it
([design, `create_agent`](../shibu.md#create_agent)).

Labels beyond `kind`, `adapter` and `title` are set with `label`
afterwards.

**Errors**, beside the usual ones ([`README.md`](README.md#errors)):

| Code | When |
| --- | --- |
| `runner_unavailable` | The `runs_in` runner is missing, failing its contract, or has no `capacity`. |
| `launch_refused` | The runner refused the `launch`: an adapter, model or budget outside the grant's limits as the runner applies them, a `credential` it holds no `read` grant on, or its own policy. Its reason is passed on. |
| `invalid_argument` | The adapter isn't in the runner's `adapters`; `config` doesn't match the manifest; `credential` is of a kind the manifest doesn't accept; a `tools` entry isn't an MCP endpoint. |
| `permission_denied` | The caller holds no `launch` on `runs_in`, or can't grant the ops a `tools` entry asks for. |
| `not_narrowing` | The budget, adapter or model is outside the launch grant it is narrowed from. |
| `quota_exceeded` | `payer` has no headroom for the VM. The helper takes no `wait`. |

**Dry run** is the batch's dry run, which reports every charge along
each grant chain, and the runner's answer to a dry-run `launch`.

---

## The agent object

```json
{
  "kind": "agent",
  "id": "ag_17",
  "path": "/factory/lead/workers/w17/agent",
  "namespace": {"id": "ns_w17", "path": "/factory/lead/workers/w17"},
  "adapter": "claude-code",
  "model": "claude-sonnet-4",
  "runs_in": {"endpoint": "/sys/runner", "kind": "pool"},
  "tools": ["/factory/lead/workers/w17/vm/mcp", "/factory/lead/workers/build/b2/mcp"],
  "status": "running",
  "exit": null,
  "session": {"id": "ses_9", "state": "idle", "restarts": 0},
  "launch_grant": "gr_l17",
  "binding": {"runner": "/sys/runner", "issued_at": "2026-10-04T05:31:00Z"},
  "labels": {"title": "item 17: retry the flaky upload", "status": "waiting for CI", "phase": "working"}
}
```

| Field | Meaning |
| --- | --- |
| `status` | `running` or `exited` ([design 2.2.17](../shibu.md#2217-exit-status-and-reason)). |
| `exit` | Once exited: `{"status": "succeeded" \| "failed", "reason": "<text>" \| "stopped" \| "out-of-quota" \| "deleted", "at"}`. `reason` is the agent's own text when it called `exit`, or one of the three kernel reasons. |
| `session.state` | `starting`, `running` (the CLI is working a turn), `idle` (its turn ended without `exit`; the next message from the creator resumes it), `waiting` (paused on a vendor's limit: `session.reason` is `vendor-limit`, and `session.until` the reset time when the vendor gave one; [design 6.10](../shibu.md#610-per-vendor-rate-limits-on-shared-subscriptions)), `resuming`, or `ended`. The agent stays `running` while its session waits. |
| `runs_in.kind` | `vm` when the runner is a VM in the agent's namespace (its sessions use the VM's origin), `pool` otherwise (session binding, `via: "runner"`). |
| `launch_grant` | The launch grant on `runs_in` that the agent's namespace holds, which carries its budget. `null` until one is granted. |
| `binding` | The session binding the kernel has issued, if any: to which runner, and when. Never the binding itself. |

**The session binding.** The kernel issues it, and nothing else does.
A pool runner asks for one with `POST <path>/agent/bind`
(`{"session_id"}` → `{"binding", "launch_grant": {"id", "limits"}}`),
and the kernel answers only if the request comes from the origin of a
VM serving the agent's `runs_in` endpoint, the agent is `running`, its
namespace holds a live launch grant on `runs_in`, and no other binding
is live. The binding names one agent and one session, and stops working
when the session ends or the launch grant ends
([`web-proxy.md`, Identity](web-proxy.md#identity)). A runner on a VM
in the agent's own namespace needs no binding. This is the kernel's
whole part in launching: who calls `launch` is the runner's business,
under its launch grants.

Each change of `status`, `exit` or `session.state` is an
`agent.status` event ([`kernel-api.md`, Events](kernel-api.md#events)).

---

## `stop` and `exit`

| Operation | Arguments | Who may call it |
| --- | --- | --- |
| `stop` | `agent` (the agent's namespace or `<path>/agent`), `reason` (free text, recorded beside `stopped`) | the creator, an ancestor, or a holder of `write` on the agent's control endpoint (`<path>/control`) or on `<path>/agent` |
| `exit` | `status` (`succeeded` or `failed`), `reason` | the agent's own namespace only: its VMs, or its harness through its session binding |

Either one ends the harness session, stops the VMs in the agent's namespace, writes the
transcript and final logs into the namespace as volumes, sets the
`phase` label, and records the status and reason on the agent object.
**The namespace is not deleted**: only `delete` or its lease does
that. A creator that wants a throwaway worker's namespace gone deletes
it on the exit event, itself or through a reaper
([design 2.2.6](../shibu.md#226-leases-and-ttls)). `messages`,
`artifacts` and the transcript stay readable for as long as the
namespace exists. Both are idempotent: a second call on an exited agent
returns it unchanged.

The kernel also ends an agent itself, with reason `out-of-quota` when
its model budget is spent and the runner or `/sys/model` reports that
it can't continue, or when a vendor's limit lasts beyond its session's
lease and the runner ends the session
([design 6.10](../shibu.md#610-per-vendor-rate-limits-on-shared-subscriptions)), `deleted` when its namespace is deleted, and
`stopped` when the VM its session runs on is deleted.

---

## Standard endpoints

Every agent's namespace has these endpoints, served by the kernel,
whatever VMs the agent has. Each is an ordinary `http` endpoint
([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits)): it
is granted with HTTP methods, `read` and `write`, and `paths`, like any
other, and the creator reaches all of them through the tree
([design, Harness interface](../shibu.md#harness-interface)). A grant
of `read` on `messages` lets the grantee read the chat; `write` lets it
post to it too.

| Endpoint | Paths and methods | Grant ops |
| --- | --- | --- |
| `job` | `GET <path>/job` → `{"job": "…", "config": {...}, "title": "…", "creator": "/factory/lead"}` | `read` |
| `messages` | `GET`, `POST <path>/messages` ([below](#messages)) | `read`; `write` to send |
| `artifacts` | `GET`, `POST <path>/artifacts` ([below](#artifacts)) | `read`; `write` to post |
| `exit` | `POST <path>/exit` with `{"status", "reason"}` | the agent's own namespace only |
| `agent` | `GET <path>/agent` → the agent object; `POST <path>/agent/stop`; `POST <path>/agent/runs_in`; `POST <path>/agent/bind` (the `runs_in` runner only) | `read`; `write` to stop it or change `runs_in` |
| `agent-mcp` | MCP over `POST`, [below](#agent-control-mcp) | the agent's own namespace only |
| `usage` | `GET <path>/usage` → the agent's usage and budget, from its launch grant's meter, whether `/sys/model` or the runner reported it | `read` |

Inside the agent's own VMs, and to its session through the binding,
these are reachable as `http://shibu.internal/_self/<endpoint>`.

---

## `messages`

*Kernel* ([design 2.2.25](../shibu.md#2225-messages),
[2.6.2](../shibu.md#262-messages-types)). A two-way channel between an
agent and its creator, per agent, not routed.

### A message

```json
{
  "id": "msg_204",
  "seq": 204,
  "at": "2026-10-04T05:40:00Z",
  "side": "agent",
  "from": {"ns_path": "/factory/lead/workers/w17", "via": "runner"},
  "type": "question",
  "text": "The upload test also fails on main. Fix it here, or file it separately?",
  "choices": ["fix it here", "file it separately"],
  "reply_to": null
}
```

| Field | Meaning |
| --- | --- |
| `seq` | A sequence number, increasing by one per message on this endpoint. |
| `side` | `agent` when sent by the agent's own namespace (its VMs or its harness); `creator` when sent by anyone else holding `write`. |
| `from` | Who sent it, from the caller identity: never set by the sender. |
| `type` | `text`, `question`, `approval`, `progress`, `attachment`, or `reply`. |
| `text` | Always present, so a client that knows no types has something to show. |
| `reply_to` | For a `reply`, the `id` of the `question` or `approval` it answers. |

Fields by type ([`ui-conventions.md`, `messages` types](ui-conventions.md#messages-types)
says how a UI renders them):

| `type` | Extra fields |
| --- | --- |
| `text` | none |
| `question` | `choices` (optional list of strings) |
| `approval` | `subject`: what is to be approved, `{"summary", "detail"?, "ref"?}` |
| `progress` | `fraction` (optional, 0 to 1) |
| `attachment` | `attachment`: `{"name", "media_type", "url"? \| "ref"? \| "inline_base64"?}`. A `ref` to a volume or Shibu endpoint is a reference, not a grant. Inline content is at most 1 MiB. |
| `reply` | `reply_to`, and `choice` (one of the question's `choices`), or `decision` (`approve` or `deny`) with an optional `reason`; `text` holds free text |

A `question` or `approval` is **open** until a `reply` names it. Only
the other side may reply, and only once; a second reply fails with
`conflict`.

### Operations

`messages` is an `http` endpoint, and these are its paths. There is no
control-API form: the CLI, the UI and a harness all use the endpoint.

| To | HTTP on the endpoint | Arguments | Returns |
| --- | --- | --- | --- |
| send | `POST <agent>/messages` | the message's `type`, `text` and type fields; `Idempotency-Key` header | the message, with `id` and `seq` |
| read | `GET <agent>/messages?since=<seq>&wait=<duration>` | `since` (exclusive; default 0), `wait` (long-poll up to this long if nothing is newer; at most `60s`), `stream=true` (SSE instead), `page_size` | `{"messages": [...], "next_since": 204, "truncated": false, "cap_bytes": 10485760, "open": ["msg_204"]}` |

- Sending needs `write` (or `POST`) on the endpoint, and reading needs
  `read`. The creator has both through the tree; anyone else needs a
  grant: `grant(<agent>/messages, to=/users/alice, ops=[write])`
  ([design 2.2.25](../shibu.md#2225-messages)).
- `open` lists the open questions and approvals.
- `truncated` is true if the kernel no longer holds messages older than
  the first one returned.
- **History.** The kernel keeps the history with the agent object, in
  the agent's namespace, in its state store
  ([`state/v1`](extension-points.md#kernel-state-store-contract-statev1)),
  and it lives exactly as long as that namespace. It is capped per
  agent at a size the kernel states in every `read` answer's
  `cap_bytes`, dropping the oldest messages first. Anyone holding `read` can read all of what
  is kept, including a grantee added after the messages were posted.
  Results meant to outlive the agent go to a deposit area, not
  `messages` ([design 6.11](../shibu.md#611-where-an-agents-messages-history-lives)).
- Each message is a `message.posted` event, without its text, for
  anyone who can see the endpoint
  ([`kernel-api.md`, Events](kernel-api.md#events)).
- `messages` stays readable after the agent exits, for as long as its
  namespace exists. After the agent exits, sending fails with
  `conflict`.

---

## `artifacts`

*Kernel* ([design 2.6.4](../shibu.md#264-artifacts)). An append-only
list of what the agent produced.

```json
{"id": "art_3", "seq": 3, "at": "…", "artifact_kind": "pr", "title": "Retry the flaky upload",
 "ref": "https://github.com/acme/app/pull/912", "labels": {}}
```

`artifact_kind` is `pr`, `url`, `file` or `volume`. `ref` is a URL for
`pr` and `url`, a volume path for `volume`, and a volume path plus a
file path (`/factory/lead/workers/w17/keep:/report.html`) for `file`.
An entry is a reference: it gives the reader nothing they couldn't
reach already.

| Operation | HTTP on the endpoint | Who |
| --- | --- | --- |
| post | `POST <agent>/artifacts` with an entry | the agent's own namespace, or a holder of `write` |
| read | `GET <agent>/artifacts?since=<seq>` | the creator, by the tree, or a holder of `read` |

Entries can't be changed or removed. Any other namespace may serve an
endpoint of the same shape ([`ui-conventions.md`](ui-conventions.md#artifacts)).

---

## Status labels

Every agent namespace carries three labels so that any UI can draw the
same list of agents ([design 2.6.3](../shibu.md#263-status-labels)):

| Label | Values | Set by |
| --- | --- | --- |
| `title` | a few words | the creator at `create_namespace` (`agent.title`, which `create_agent`'s `spec.title` fills), and the agent |
| `status` | one line on what it is doing now | the agent |
| `phase` | `working`, `waiting-on-human`, `done`, `failed` | the adapter (`working` ↔ `waiting-on-human`), and the kernel at exit (`done` for `succeeded`, `failed` otherwise) |

The kernel enforces who may set what: only the agent's own namespace may
set `status`, or set `phase` to `working` or `waiting-on-human`, and
nobody may set `done` or `failed` but the kernel. The agent sets them
with `label` on its own namespace, which the agent control MCP's
`set_status` tool wraps.

"Everything waiting on me" is
`list(path, selector="kind:agent,phase:waiting-on-human", include_grants=true)`.

---

## The harness contract

*Kernel.* A harness is an agent CLI run headless by an adapter, in a
session of a runner. The contract is what Shibu gives every harness,
and what every harness has to do, whatever runner it runs in
([design, Harness interface](../shibu.md#harness-interface)).

**What the harness gets:**

| | Runner on a VM in the agent's namespace | Any other runner (a pool runner) |
| --- | --- | --- |
| Its identity | that VM's origin | the agent's namespace, through the session binding, `via: "runner"` |
| Its tools | the agent control MCP and each `tools` entry, at `http://shibu.internal/<handle path>` | the same, through the binding |
| Its model | as the runner is configured: `/sys/model` at `http://shibu.internal/_self/model/<protocol>/…` with a dummy key, a key or login the runner holds, or a self-hosted model | the same |
| Its job, messages, artifacts and exit | `http://shibu.internal/_self/{job,messages,artifacts,exit}` | the same paths, through the binding |
| Its credential | none, unless the runner reads one it was named in `launch` through its own `read` grant | the same |

The runner sets these environment variables for the harness:

| Variable | Value |
| --- | --- |
| `SHIBU_ENDPOINT` | `http://shibu.internal` |
| `SHIBU_AGENT` | the agent's namespace path |
| `SHIBU_AGENT_ID` | the agent object's ID |
| `SHIBU_AGENT_MCP_URL` | the agent control MCP's URL |
| `SHIBU_TOOLS` | JSON: the `tools` entries, with their URLs and ops |
| `SHIBU_MODEL` | the `model` launch parameter |
| `SHIBU_SESSION` | `new` or `resume` |
| the manifest's `model.base_url_env` | `http://shibu.internal/_self/model/<protocol>`, where the runner reaches models through `/sys/model` |
| the manifest's `model.api_key_env`, if any | `shibu-dummy`, where `/sys/model` attaches the real key |

**What the harness must do** (the adapter does it on the CLI's behalf):

1. **Tools.** Disable every built-in tool, and load an MCP
   configuration that is strict and lists only the agent control MCP
   and the agent's `tools`.
2. **Job.** Read `job` and run it as the first turn.
3. **Messages in.** When a `creator` message arrives, deliver it to the
   CLI: as the next turn if the session is idle, resuming it with the
   manifest's `resume` command; or as input to the turn in progress if
   the CLI accepts that. A `reply` to an open question or approval is
   returned to whatever is blocked on it.
4. **Messages out.** The agent sends messages, questions, approvals,
   progress and artifacts through the [agent control MCP](#agent-control-mcp).
   The adapter may also map the CLI's own output onto `text` and
   `progress` messages.
5. **Phase.** Set `phase` to `waiting-on-human` while a question or
   approval it posted is open, and back to `working` when it is
   answered.
6. **Idle.** When a turn ends without `exit`, leave the session `idle`.
   The agent stays `running`.
7. **Exit.** Call `exit` when the agent calls the agent control MCP's
   `exit` tool. If the CLI crashes, the runner resumes the session;
   after the manifest's `max_resumes` (default 3) in a row without
   progress, the runner exits the agent with `failed` and reason
   `harness-failed`.

---

## Adapter manifest

A declarative file shipped with each adapter, in a runner's image and
installed by the runner's operator
([design, Harness interface](../shibu.md#harness-interface)). A runner
serves every manifest it can run at `GET <runner>/adapters`
([`runner/v1`](extension-points.md#runnerv1)). `create_agent` checks a
spec against it, and the UI and `shibu secret put` build their
credential and config forms from it.

```yaml
name: claude-code
version: 1.2.0
model:
  protocol: anthropic-messages         # a model/v1 protocol, when the runner uses /sys/model
  base_url_env: ANTHROPIC_BASE_URL
  api_key_env: ANTHROPIC_API_KEY       # set to a dummy value when /sys/model attaches the key
  arg: [--model, "{model}"]            # how the model launch parameter reaches the CLI
credentials:
  - id: anthropic
    label: "Anthropic API key or Claude subscription"
    help_url: https://docs.anthropic.com/
    kinds:
      api-key:
        inject: {header: x-api-key}    # attached by /sys/model on the way out
        deliver: {env: ANTHROPIC_API_KEY}   # or handed to the CLI, when the runner holds the key
      oauth:
        flow: {provider: anthropic, scopes: [user:inference]}   # the runner or the UI runs the login flow
        deliver: {env: CLAUDE_CODE_OAUTH_TOKEN}                  # handed to the CLI; can't go through a proxy
config:
  - {id: effort, label: Effort, type: enum, options: [low, high], default: high}
  - {id: max_turns, label: "Max turns per session", type: int, default: 200}
launch:
  command: [claude, -p, --output-format, stream-json, --tools, "", --strict-mcp-config,
            --mcp-config, "{mcp_config_path}", --model, "{model}"]
  prompt: stdin                        # stdin | arg | file
resume:
  command: [claude, -p, --resume, "{session_ref}", --output-format, stream-json, --tools, "",
            --strict-mcp-config, --mcp-config, "{mcp_config_path}"]
stop: {signal: SIGTERM, timeout: 30s}
mcp_config:
  format: claude-json                  # how the strict MCP configuration file is written
max_resumes: 3
```

| Field | Meaning |
| --- | --- |
| `model.protocol` | Which `model/v1` protocol the CLI speaks, for a runner that reaches models through `/sys/model` ([`model/v1`](extension-points.md#modelv1)). |
| `model.arg` | How the `model` launch parameter is passed to the CLI. |
| `credentials[].kinds` | The kinds of credential accepted for this slot: `api-key`, `oauth` or `subscription`. `api-key` says how `/sys/model` attaches it (`inject`: a header, with an optional `format` such as `"Bearer {value}"`), and how it reaches the CLI when the runner holds it (`deliver`). `oauth` and `subscription` say how the runner or the UI runs the login flow (`flow`) and how the login reaches the CLI (`deliver`: `env` or `file`). They can't be proxied ([design 2.2.23](../shibu.md#2223-model-access-and-budgets)). |
| `config` | Non-secret settings. `type` is `enum`, `string`, `int` or `bool`. Delivered next to `job`. |
| `launch`, `resume`, `stop` | How to run the CLI headless. `{…}` placeholders are filled by the runner: `mcp_config_path`, `session_ref`, `model`, `config.<id>`. |
| `mcp_config.format` | How to write the strict MCP configuration for this CLI. |

**Keys are entered only in forms built from the manifest**, in the UI's
own pages or by `shibu secret put --adapter <name>`, never by a harness
and never in a pane ([design, Harness interface](../shibu.md#harness-interface)).
A credential typed into a form goes straight to `put_secret`. An
`oauth` or `subscription` login is not a kernel flow: the runner that
will use it (`POST <runner>/logins`, [`runner/v1`](extension-points.md#runnerv1))
or the UI component (`/sys/ui`, [`ui/v1`](extension-points.md#uiv1))
runs the vendor's flow from the manifest's `flow`, and writes the
result to the secret's path with `put_secret`. It needs `write` on that
secret: the owner, acting through the UI, has it by the tree; a runner
is granted `write` on the one secret.

Adapters are installed by a runner's operator: root for `/sys/runner`,
by changing the runner in the `/sys` spec; a namespace for a runner on
its own VM. Granting `launch` on a runner, or `read` on a login to it,
is trusting its operator's choice of adapters
([design F27](../shibu.md#f27-adapter-trust)).

---

## VM `mcp` contract, `mcp/v1`

*Kernel: the contract. Standard distribution: the `mcp` server in the
agent base image*
([design, VM `mcp`](../shibu.md#vm-mcp-and-the-agent-control-mcp)).

Any VM may serve a standard `mcp` endpoint with the **VM tools**. It is
optional per VM, and on by default in agent images.

- **Transport.** MCP over streamable HTTP, at path `/mcp` on port
  `7070` of the VM. The kernel creates the endpoint `<vm>/mcp` on that
  port when the VM's image declares it.
- **Who may call it.** Anyone who can reach the endpoint: the VM's own
  namespace and its ancestors by the tree, and grantees. It is granted
  like any endpoint, with ops.
- **Ops.** `mcp` is an `http` endpoint speaking this contract. A grant
  on it carries `read`, `write` and the contract op `exec`, and the
  contract limit `files`. The web proxy passes the grant's ops and
  limits in the caller assertion, and the server refuses a tool call
  the grant doesn't cover.

| Tool | Op | Arguments | Result |
| --- | --- | --- | --- |
| `exec` | `exec` | `command` (string, run by `/bin/sh -c`), `cwd`, `env`, `stdin`, `timeout` (default `10m`) | `stdout`, `stderr` (each truncated to a limit the server reports), `exit_code`, `timed_out` |
| `read_file` | `read` | `path`, `offset`, `limit` (lines) | the content |
| `list_dir` | `read` | `path`, `depth` | entries with type and size |
| `write_file` | `write` | `path`, `content`, `mode` | bytes written |
| `edit_file` | `write` | `path`, `old`, `new`, `replace_all` | replacements made |

A grant may narrow `read` and `write` with `limits={files: [...]}`
(globs on the VM's filesystem), which the server applies. `exec` can't
be narrowed by path: a command can reach anything the VM can.

**`exec` lends identity.** A command run through `exec` runs in the VM,
with the VM's origin: its traffic goes through the web proxy under the
grants of the VM's **home namespace**, its `/sys/egress` grant
included, not the caller's
([`web-proxy.md`, Egress](web-proxy.md#egress)). A grant of `exec` on
another namespace's VM deliberately lends that identity, and whatever
the VM holds. It is the strongest grant op, and `exposure` lists every
holder ([`kernel-api.md`, `exposure`](kernel-api.md#exposure)).

**Conformance**, `shibu/conformance-mcp:1`: each tool round trip; op
refusal for every tool; `files` narrowing on `read` and
`write`; that `exec` traffic leaves with the VM's origin, not the
caller's; output truncation and timeouts.

---

## Agent control MCP

*Kernel* ([design, VM `mcp`](../shibu.md#vm-mcp-and-the-agent-control-mcp)).

The **agent tools** are an MCP server the kernel serves at
`<path>/agent-mcp`, over streamable HTTP, on the agent's own
endpoints. It needs no VM, so an agent with `tools: []` has it too.
Only the agent's own namespace may call it: its VMs, or its session
through the binding.

| Tool | Arguments | Result |
| --- | --- | --- |
| `send_message` | `text`, `type` (`text` or `progress`), `fraction` | the message id |
| `ask` | `text`, `choices` | blocks until the creator replies; the reply. Sets `phase` around the wait |
| `request_approval` | `text`, `subject` | blocks until answered; the `decision` and `reason`. Sets `phase` around the wait |
| `attach` | `name`, `media_type`, and `url`, or `vm` and `path` (a file read through that VM's `mcp`, which the agent must hold `read` on) | the message id |
| `post_artifact` | `artifact_kind`, `title`, `ref` | the artifact id |
| `set_status` | `status`, `title` | none |
| `exit` | `status` (`succeeded` or `failed`), `reason` | none; the session ends |

---

## Application MCP servers

*Additional components.* An application's own tools, such as grain's
`propose_task`, are an MCP server the application serves, granted to
the agent like any endpoint
([design, Harness interface](../shibu.md#harness-interface)).

- The application serves MCP over streamable HTTP on an `http`
  endpoint in its own namespace.
- The agent's `tools` names it:
  `{"name": "factory", "endpoint": "/factory/lead/tools", "ops": ["POST"]}`.
  The agent's namespace must reach the endpoint: by the tree, through a
  grant in `spec.grants`, or one it already holds. The `create_agent`
  helper adds the grant to its batch if `endpoint` is the caller's own
  and the entry has `ops`.
- The adapter adds each entry to the CLI's strict MCP configuration,
  under `name`, at `http://shibu.internal/<handle path>`. A pool
  runner's forwarder carries those calls as the agent, like every other
  tool call.
- The server sees the agent's namespace in each call's caller
  assertion, and applies its own policy to it.
- Its tools run where the application runs, with the application's
  authority. The application decides what that authority is.

---

## Placements

([design, Placements](../shibu.md#placements))

A placement is a combination of `runs_in` and `tools`, not a field. The
common ones:

| Combination | `runs_in` | `tools` | Harness identity |
| --- | --- | --- | --- |
| `same-vm` (the default) | `<path>/vm/runner`, created by the call | `<path>/vm/mcp` | the VM's origin |
| `split` | a pool runner, such as `/sys/runner` | a VM `mcp` elsewhere, often `<path>/vm/mcp` | session binding, `via: "runner"` |
| planner | a pool runner | `[]` | session binding, `via: "runner"` |
| shared build VM | any runner | `exec` on a VM in another namespace | as `runs_in`; commands run as that VM's namespace |

- **Subscriptions and OAuth** need a runner that holds the login: a
  runner whose namespace the login's owner granted `read` on it,
  usually leased and with `regrant: false`. A login can't go through
  `/sys/model`.
- **API keys** work in any runner: through `/sys/model`, which the
  key's owner granted `read` on it, so no VM holds the key; or read by
  the runner through its own `read` grant.
- **Budget** is enforced by `/sys/model` when the runner reaches models
  through it, and by the runner otherwise
  ([`kernel-api.md`, Budgets](kernel-api.md#budgets)).
- **Charged to:** a VM the call creates, to `payer`; a pool runner's
  session, to nobody's VM grant (the agent pays through its budget).

There is no kernel refusal tied to placement or to credential-holding
([design 2.2.7](../shibu.md#227-credentials-only-behind-proxy-endpoints)).
Who may run where is the runner owner's `launch` grants, the VM owner's
`mcp` grants, and the secret owner's `read` grants.

**Placement is invisible from outside.** The namespace, the budget,
`messages`, `artifacts` and the exit behave the same. The only
difference a service sees is `via: "runner"` on calls from a session
that acts through a binding.
