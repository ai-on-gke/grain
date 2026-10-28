# Shibu kernel API, v1

*Kernel.* This is the control API every Shibu serves: its object
types, every operation, the events `watch` streams, and the audit and
cost records. The conventions it relies on (transport, paths,
references, errors, idempotency, dry run, pagination and streams) are
in [`README.md`](README.md#conventions). The agent object, `messages`
and `artifacts` are in [`agents.md`](agents.md), and the `/sys` spec is
in [`sys-spec.md`](sys-spec.md).

**The kernel is:** namespaces, VMs, volumes, endpoints, grants,
secrets, leases, labels, `move`, `adopt`, `watch`/`audit`/`cost` and
`batch`. Everything else is a standard-distribution component built on
those ([design, Layers](../shibu.md#layers-kernel-standard-distribution-additional-components)):
outside access is a grant on `/sys/egress`, public exposure a grant to
`/sys/ingress`, declarative `apply` the reconciler, `create_agent` a
helper over kernel operations and `runner/v1`
([`extension-points.md`](extension-points.md#distribution-contracts)).
The kernel's own endpoint kinds are `http`, `volume`, `allocator`,
`control` and `secret`; every other service, the kernel's own agent
endpoints included, is an `http` endpoint reached by paths and methods.

Every operation is `POST /_shibu/v1/<name>`, made **as** a namespace:
the caller's ambient identity, or the namespace a front-door caller is
acting as ([`web-proxy.md`, Identity](web-proxy.md#identity)). That
namespace is called **the caller** below.

## Contents

1. [Authority](#authority)
2. [Object types](#object-types)
3. [Ops and limits](#ops-and-limits)
4. [Namespaces](#namespaces)
5. [Grants](#grants)
6. [Endpoints](#endpoints)
7. [Resources](#resources)
8. [Leases and labels](#leases-and-labels)
9. [Secrets](#secrets), including [`exposure`](#exposure)
10. [Orchestration](#orchestration)
11. [Events](#events)
12. [Audit records](#audit-records)
13. [Cost records](#cost-records)
14. [Enforcement order](#enforcement-order)

---

## Authority

What the caller may do comes from two places and nowhere else
([design, The tree](../shibu.md#the-tree), [Grants](../shibu.md#grants)).

**The tree.** A namespace has implicit authority over its own subtree,
itself included. Over any object homed there, it may:

- see it, in `list`, `watch`, `audit` and `cost`;
- reach it, if it is an endpoint, with every op the endpoint's kind has,
  except `read` on a secret;
- stop, delete, label, lease and `move` it, within the subtree;
- revoke any grant given or received inside the subtree;
- grant it onward, narrowed, if the object is owned in the subtree.

A handle that a descendant *received* is reachable by the ancestor
with the ops the handle carries. The ancestor may grant from it only if
the received grant has `regrant: true`
([design, Grants](../shibu.md#grants)).

**Grants.** Everything else is a grant received by the caller, or
received with `subtree: true` by one of its ancestors. The grant's
`ops` and `limits` say what the caller may do with the object.

**Exceptions to the tree.**

- **Secrets.** Owning a secret, by the tree or otherwise, never lets
  the owner read it. A secret is read only through a `read` grant
  ([Secrets](#secrets)).
- **An agent's own endpoints.** `exit` and `agent-mcp` in an agent's
  namespace answer only the agent's own namespace (its VMs, or its
  harness through its session binding), not its ancestors
  ([`agents.md`](agents.md#standard-endpoints)). An ancestor ends an
  agent with `stop`.
- **Root-only operations.** Changing the `/sys` spec, creating
  directly under `/sys` or `/users`, and changing the kernel allocators
  need the caller to be `/`. A person acts as `/` only if the front door maps
  them to it ([`web-proxy.md`, The front door](web-proxy.md#the-front-door)).

---

## Object types

Every object carries the common fields in
[`README.md`](README.md#common-fields). These are the fields each type
adds.

### Namespace

```json
{
  "kind": "namespace",
  "id": "ns_3f9k2a",
  "path": "/factory/ci/c17-1",
  "parent_id": "ns_9a1",
  "lease": {"expires_at": "2026-10-04T06:15:00Z"},
  "state": "active",
  "deleting": null,
  "credential_holding": false,
  "keep": {"area": "/factory/ci/keep/c17-1", "grant_id": "gr_88"},
  "agent": null
}
```

| Field | Meaning |
| --- | --- |
| `lease` | The common field ([`README.md`](README.md#common-fields)), shown because it is one of only two things that govern a namespace's lifetime: when it expires, the namespace is deleted. `null` for none ([design 2.2.6](../shibu.md#226-leases-and-ttls)). |
| `state` | `active` or `deleting` ([design, Namespace](../shibu.md#namespace)). There is no lifecycle policy: a namespace is deleted by [`delete`](#delete) or when its lease expires. |
| `deleting` | While `deleting`: `{"cause": "delete" \| "lease" \| "ancestor", "started_at"}`. Otherwise `null`. `deleting` is brief and can't be configured. |
| `credential_holding` | True while the namespace holds any secret-`read` grant ([design 2.2.7](../shibu.md#227-credentials-only-behind-proxy-endpoints)). The kernel sets it; nobody else can. It is information, for `list` and [`exposure`](#exposure); the kernel refuses nothing because of it. |
| `keep` | If it was created with `keep`, the deposit area in its creator and the grant into it. |
| `agent` | For an agent namespace, the agent object's ID; otherwise `null` ([`create_namespace`](#create_namespace), [`agents.md`](agents.md#the-agent-object)). |

Namespaces don't exit and have no exit status. VMs and agents in them
do ([design 2.2.17](../shibu.md#2217-exit-status-and-reason)).

### Endpoint

An endpoint is a name in a namespace pointing at something
([design, Endpoint](../shibu.md#endpoint)). Every object a namespace
holds is reachable as an endpoint, and a grant appears in the grantee
as one, a **handle**.

```json
{
  "kind": "endpoint",
  "id": "ep_51x",
  "path": "/factory/svc/db",
  "endpoint_kind": "http",
  "target": {"vm_id": "vm_2b", "port": 8080},
  "handle_of": null
}
```

| `endpoint_kind` | Points at | `target` |
| --- | --- | --- |
| `http` | a VM port spoken to over HTTP, or a path the kernel serves itself | `{"vm_id", "port"}`, or `{"kernel": "<service>", "agent_id"?}` |
| `volume` | a volume (images and snapshots are volumes) | `{"volume_id"}` |
| `control` | a namespace's control endpoint | `{"ns_id"}` |
| `allocator` | a kernel allocator, or a component allocator | `{"allocator_kind", "vm_id"?, "port"?}` |
| `secret` | a secret in the store | `{"secret_id"}` |

There are no other kinds. Every other service is an `http` endpoint,
whoever serves it, and is reached by paths and methods:

- **Served by the kernel:** an agent's standard endpoints (`agent`,
  `job`, `messages`, `artifacts`, `exit`, `agent-mcp`, `usage`), with
  target `{"kernel": "agent", "agent_id"}`
  ([`agents.md`, Standard endpoints](agents.md#standard-endpoints)).
- **Served by a VM, speaking a contract:** a VM's `mcp`
  ([`agents.md`, `mcp/v1`](agents.md#vm-mcp-contract-mcpv1)) and
  `runner` ([`runner/v1`](extension-points.md#runnerv1)) endpoints, and
  every `/sys` component: `/sys/model`, `/sys/egress`, `/sys/ingress`,
  `/sys/git` and the rest.
- **A resource a component allocator made**, which the component serves
  (at least `GET` for its status) ([`allocator/v1`](extension-points.md#component-allocators)).

`tcp` is *reserved, beyond v1*
([design 2.4](../shibu.md#24-beyond-v1-native-port-endpoints)).

`handle_of` is `null` for an endpoint the namespace owns. For a handle
it is `{"grant_id", "object_id", "object_path"}`, and the handle's
`endpoint_kind` is the object's.

**Reserved names.** The kernel creates and serves `control` in every
namespace. In an agent's namespace it also creates `agent`, `job`,
`messages`, `artifacts`, `exit`, `agent-mcp` and `usage`, and `keep`
when the namespace has a deposit area ([`agents.md`](agents.md)). By
convention `ui` and `requests` are left to the namespace's own panes
and request endpoint ([`ui-conventions.md`](ui-conventions.md)). A
create or grant that would use a reserved name fails with
`already_exists`.

### Grant

```json
{
  "kind": "grant",
  "id": "gr_77",
  "object": {"id": "ep_51x", "path": "/factory/svc/db", "endpoint_kind": "http"},
  "granter": {"ns_id": "ns_svc", "ns_path": "/factory/svc"},
  "grantee": {"ns_id": "ns_lead", "ns_path": "/factory/lead"},
  "handle": "/factory/lead/db",
  "ops": ["tunnel"],
  "limits": {"max_connections": 40},
  "subtree": false,
  "regrant": true,
  "reserved": false,
  "issued_from": null,
  "chain": ["ep_51x", "gr_77"],
  "state": "active",
  "last_used": "2026-10-04T18:00:00Z",
  "ended": null
}
```

| Field | Meaning |
| --- | --- |
| `regrant` | Whether the grantee may grant onward from this grant, narrowed. `false` unless the granter set it ([`grant`](#grant)). |
| `reserved` | Allocator grants only: whether the whole `cap` is set aside from the granter's headroom ([`grant`](#grant), [Allocator limits](#allocator-limits)). |
| `usage` | Allocator and metered grants only, in `get`: per dimension and budget, the amount charged, the cap, and the headroom left, at every link of the chain the caller can see ([`get`](#get-and-list)). |
| `issued_from` | The grant this one was narrowed from, when the granter held the object through a grant; `null` when the granter owns the object by the tree. |
| `chain` | The object, then every grant from the owner down to this one. For an allocator this is the quota chain ([design, Quota](../shibu.md#quota)). |
| `state` | `active` or `ended`. |
| `last_used` | When a request, create, read or launch last went through this grant, or a grant issued from it; `null` if never. Recorded to the hour, so busy grants don't write on every request ([design F23](../shibu.md#f23-grant-sprawl-in-long-lived-namespaces)). |
| `ended` | Once ended: `{"at", "reason": "revoked" \| "lease" \| "cascade" \| "invalid" \| "grantee-deleted" \| "target-deleted", "by"?}`. |

### VM

```json
{
  "kind": "vm",
  "id": "vm_2b",
  "path": "/factory/svc/db-vm",
  "payer": "gr_12",
  "spec": {
    "shape": {"cpu": 4, "ram": "16Gi", "disk": "50Gi"},
    "image": "/factory/svc/images/postgres",
    "restart": "always",
    "volumes": {"/var/lib/postgresql": "/factory/svc/db-data"},
    "endpoints": {"8080": "db"},
    "env": {"PGDATA": "/var/lib/postgresql/data"}
  },
  "driver": "granule",
  "class": "standard",
  "status": "running",
  "exit": null,
  "restart_count": 0
}
```

`class` is the placement class of the kernel VM allocator it was
created through: each kernel VM allocator offers one class
([design 2.2.15](../shibu.md#2215-placement-classes-and-restart-policy)).
`status` is `running`, `restarting`, `exited` or `preempted`. `exit`,
once it has exited, is `{"code": 1, "reason": "completed" | "failed" |
"preempted" | "oom" | "deleted", "at"}`
([design 2.2.17](../shibu.md#2217-exit-status-and-reason)).

### Volume

Images and snapshots are volumes: a volume created read-only from
another volume is a snapshot of it, and one created read-only from an
OCI image is an image ([design 2.2.3](../shibu.md#223-volume-snapshot-and-clone),
[2.2.14](../shibu.md#2214-images)).

```json
{"kind": "volume", "id": "vol_4", "path": "/factory/svc/db-data", "payer": "gr_13",
 "size": "200Gi", "read_only": false, "attached_to": ["vm_2b"], "from": null}
{"kind": "volume", "id": "vol_9", "path": "/factory/svc/images/postgres", "payer": "gr_14",
 "size": "2Gi", "read_only": true, "from": {"oci": "docker.io/library/postgres:17"}}
{"kind": "volume", "id": "vol_3", "path": "/factory/svc/repo-warm-2026-10-02", "payer": "gr_15",
 "size": "40Gi", "read_only": true, "from": {"volume_id": "vol_6", "at": "2026-10-02T04:00:00Z"}}
```

`from` records what it was made from: `null`, a volume at a point in
time, or an OCI image. A VM boots from a read-only volume it can see.
There is no public-endpoint or egress object: public exposure is a
grant to `/sys/ingress`, and outside access a grant on `/sys/egress`
([Egress and ingress](#egress-and-ingress)).

### Secret

Only metadata. No operation ever returns a value except `read` with a
`read` grant ([Secrets](#secrets)).

```json
{"kind": "secret", "id": "sec_1", "path": "/users/alice/secrets/anthropic",
 "version": 3, "written_at": "…", "previous_readable_until": "…",
 "manifest": {"adapter": "claude-code", "credential": "anthropic", "kind": "api-key"}}
```

`version` counts writes. `previous_readable_until` is set for the
store's fixed overlap after a write, while the previous value can still
be read ([`put_secret`](#put_secret)), and `null` otherwise.

### Agent

The agent object, its status labels, its exit status and its standard
endpoints are the kernel's whole agent concept, and are in
[`agents.md`](agents.md#the-agent-object). An agent namespace is made
with [`create_namespace`](#create_namespace)'s `agent`; `create_agent`
is a standard-distribution helper
([`agents.md`, `create_agent`](agents.md#create_agent)).

---

## Ops and limits

A grant's `ops` and `limits` can only narrow what the granter holds
([design, Grants](../shibu.md#grants)). Each of the five endpoint kinds
has its own vocabulary. An op or a limit the kind doesn't have fails
the grant with `invalid_argument`.

| Endpoint kind | `ops` | `limits` |
| --- | --- | --- |
| `http` | HTTP method names (`GET`, `POST`, …); `read` (= `GET`, `HEAD`, `OPTIONS`); `write` (= `POST`, `PUT`, `PATCH`, `DELETE`; implies `read`); `tunnel` (a WebSocket or `CONNECT` upgrade); and any op the endpoint's contract defines | `paths`: a list of path globs under the endpoint, `*` matching one segment and `**` any number; `max_connections`, `bandwidth` (bytes per second, e.g. `"10Mi"`), `max_duration` (per tunnel) for `tunnel` ([design 2.2.26](../shibu.md#2226-non-http-services-tunnels-over-http-endpoints)); and any limit the endpoint's contract defines |
| `volume` | `read`; `write` (implies `read`) | `sub_path` |
| `control` | `read` (`get`, `list`, `watch`, `audit`, `cost`, `exposure`, `list_grants`); `write` (every other operation on that namespace; implies `read`) | none |
| `allocator` | `create` (which also allows `delete` of what was created through the grant, and its usage in `get`) | [Allocator limits](#allocator-limits) |
| `secret` | `read` (the value); `write` (`put_secret` a new value, write-only) ([Secrets](#secrets)) | `for`, on `read`: path globs of the namespaces the reader may use the value on behalf of; by default the granter's subtree. The reader enforces it ([reference rule](extension-points.md#common-to-every-distribution-contract)) |

**`http` endpoints and contracts.** The web proxy enforces methods,
`read`, `write`, `tunnel` and `paths` on every request
([`web-proxy.md`](web-proxy.md#enforcing-a-grant-on-a-request)). A
contract may define ops and limits of its own, which its implementation
enforces from the caller assertion
([`extension-points.md`](extension-points.md#distribution-contracts));
for those the proxy checks only the paths and passes the grant's `ops`
and `limits` on in the assertion. The kernel stores and narrows them
like any op or limit, with the rule that a grantee's value for a key
must be no looser than the granter's: a subset for ops and lists, a
smaller or equal number for numbers, a narrower [budget](#budgets) for
budgets. The endpoints that used to be kinds of their own:

| Endpoint | Ops | Contract limits |
| --- | --- | --- |
| an agent's `agent`, `job`, `messages`, `artifacts`, `usage` | `read` to read; `write` to stop the agent, send a message, post an artifact ([`agents.md`, Standard endpoints](agents.md#standard-endpoints)) | none |
| `/sys/model` | `write` (`POST` a model call) | `budget`, `vendors`, `models` ([`model/v1`](extension-points.md#modelv1)) |
| a VM's `mcp` | `read` (`read_file`, `list_dir`), `write` (`write_file`, `edit_file`; implies `read`), and the contract op `exec`. **`exec` lends the VM's identity**: commands run with the VM's origin, under its home namespace's grants. It is the strongest op | `files`, globs on the VM's filesystem for `read` and `write` ([`agents.md`, `mcp/v1`](agents.md#vm-mcp-contract-mcpv1)) |
| a `runner` | `read` (adapters, capacity, sessions); the contract op `launch` (start sessions; also `status` and `stop` of them). A grant with `launch` is a **launch grant** | `adapters`, `models` (globs), `budget`, `sessions` (at once); `isolation`, the minimum isolation between sessions (`container` < `microvm` < `vm`; a re-grant may only raise it) ([design 6.12](../shibu.md#612-session-isolation-inside-pool-runners)); `usage: "reported"`, which accepts the runner's usage reports as charges ([design 6.13](../shibu.md#613-accepting-a-runners-usage-reports)); since charges only take headroom away, a re-grant may add it, not drop it ([`runner/v1`](extension-points.md#runnerv1)) |
| `/sys/egress` | `tunnel` (`CONNECT host:port`), `read`, `write` (plain-HTTP forward requests) | `hosts`: `host:port` globs ([Egress and ingress](#egress-and-ingress)) |
| `/sys/git`, `/sys/forge` | the contract ops `fetch`, `push`; `read`, `write`, `checks`, `merge` | `repos`, `refs`, `force` ([`git/v1`](extension-points.md#gitv1), [`forge/v1`](extension-points.md#forgev1)) |
| a resource a component allocator made | `read` (its status), and what its contract says | what its contract says |

There are no percentage limits: a limit is an absolute value, and a
granter that wants a share of its own limit to follow changes to it
re-grants ([design F10](../shibu.md#f10-a-supervisor-shares-its-budget-with-what-it-supervises)).

### Allocator limits

```json
{
  "cap": {"cpu": "64", "ram": "256Gi", "vms": 20},
  "into": "/factory/ci/results/c17-1/*",
  "shapes": {"max": {"cpu": 8, "ram": "32Gi", "disk": "100Gi"}},
  "images": ["/factory/svc/images/**"],
  "budget": {"unit": "cpu_hours", "per_day": "200"}
}
```

| Limit | Meaning |
| --- | --- |
| `cap` | A ceiling per **dimension**, drawn from the granter's headroom ([design 2.2.2](../shibu.md#222-caps-reservations-and-adoption)). The caps a namespace grants may add up to more than it holds, unless they are `reserved`. |
| `into` | A path glob that every create's `target` must match. A grant with `into` outside the grantee's subtree is a **deposit** grant ([design, Quota](../shibu.md#quota)). |
| `shapes` | A maximum per created object. |
| `images` | Path globs of the read-only volumes a VM may boot from. |
| `budget` | A budget over time ([Budgets](#budgets)). |

Every limit narrows. There is no per-kind, per-class or label limit:
an allocator makes one kind (a component that makes several declares
an allocator endpoint per kind), a kernel VM allocator offers one
placement class (`/vm-std`, `/vm-spot`), and labels are set by whoever
creates the object.

**`reserved`.** A boolean on the grant, not a limit. A reserved grant
sets its whole `cap` aside from the granter's headroom at grant time,
used or not; a shared one (the default) only bounds what the grantee
may draw from headroom it shares with its siblings. A guarantee plus
burst is two grants: a reserved one for the guarantee and a shared one
for the burst. A reserved grant can only be issued from **reserved
headroom**: the granter owns the allocator by the tree, or holds it
through a reserved grant whose cap isn't yet reserved onward. A
reserved grant fails with `quota_exceeded` if the cap doesn't fit
([design 2.2.2](../shibu.md#222-caps-reservations-and-adoption)).

The design's shorthand maps onto these: `limits={cap: 8 cpu, 32Gi}` is
`{"cap": {"cpu": "8", "ram": "32Gi"}}`, and a deposit's
`{into: …, bytes: 5Gi}` is `{"into": …, "cap": {"bytes": "5Gi"}}`.

**Dimensions.** The kernel allocators count these:

| Allocator kind | Dimensions |
| --- | --- |
| `vm` | `cpu`, `ram`, `disk`, `vms` (a count) |
| `volume` | `bytes`, `volumes` (a count; images and snapshots are volumes) |

A component allocator declares its own dimensions
([`allocator/v1`](extension-points.md#component-allocators)).

### Budgets

A budget limits use over time, on an allocator grant or as a contract
limit on a metered endpoint ([design 2.2.9](../shibu.md#229-time-based-budgets)).
Every budget has one shape:

```json
{"unit": "usd", "per_day": "20"}
{"unit": "tokens", "total": "5000000"}
```

`budget` is one budget or a list of them, all of which apply. Each has
a `unit` and exactly one of `per_day` and `total`:

| `unit` | Counted by |
| --- | --- |
| `usd` | a metered endpoint, or a kernel allocator whose driver entry in the `/sys` spec gives prices ([`sys-spec.md`](sys-spec.md#drivers)) |
| `tokens` | model tokens, input and output together: `/sys/model`, or the runner's reports |
| `hours` | wall-clock hours of a running session: the runner |
| `turns` | harness turns: the runner |
| `cpu_hours`, `vm_hours` | resource time: the kernel, on a VM allocator grant |

A grantee's budget narrows the granter's when, for every budget the
granter has, the grantee has one of the same unit and period no larger.
`per_day` resets at 00:00 UTC. A budget is checked at every link of the
chain: a call is refused when any link's budget is spent. Metered
endpoints enforce budgets through the [metering
API](extension-points.md#metering-api), and the kernel adds their usage
up along the chain.

A budget on a launch grant is enforced outside the runner only when
the runner reaches models through `/sys/model`. Otherwise it rests on
the runner's own enforcement and reports, which is the granter's choice
([design, Trust](../shibu.md#trust)).

### Egress and ingress

Neither is a kernel concept ([design 2.2.13](../shibu.md#2213-egress-policy)).

- **What every VM can reach.** The control API at `shibu.internal`, its
  own namespace's endpoints, and the endpoints its namespace holds
  grants on, all through the web proxy. Nothing else: there is no
  direct route out.
- **Egress is a grant.** Outside access is a grant on the
  standard-distribution `/sys/egress` endpoint, a forward proxy
  ([`egress/v1`](extension-points.md#egressv1)), with a `hosts` limit of
  `host:port` globs. No grant means no outside access. Root grants
  `/sys/egress` with `regrant: true` to whoever it lets out, and each
  holder grants narrower `hosts` onward. A `/sys` component's egress is
  the grant its `/sys` spec entry lists
  ([`sys-spec.md`](sys-spec.md#schema)).
- **Ingress is a grant.** Public exposure is a grant of an `http`
  endpoint **to** the standard-distribution `/sys/ingress` component
  ([`ingress/v1`](extension-points.md#ingressv1)), which serves the
  public URL and decides who may reach it. Revoking the grant
  unpublishes it.

---

## Namespaces

| Operation | Arguments | Returns |
| --- | --- | --- |
| `create_namespace` | `path`, `labels`, `lease`, `keep`, `grants`, `agent`, `idempotency_key`, `dry_run` | the namespace |
| `delete` | `object` (a namespace) | the namespace, now `deleting` |

### `create_namespace`

Creates an **empty** namespace at `path`, which must be in the
caller's subtree ([design section 3](../shibu.md#namespaces)).

```json
{
  "path": "/factory/ci/c17-1",
  "labels": {"factory.dev/change": "17"},
  "lease": "45m",
  "keep": {"bytes": "5Gi", "from": "/factory/ci/volume-alloc", "into": "/factory/ci/keep/c17-1"},
  "grants": [
    {"object": "/factory/ci/vm-spot", "ops": ["create"], "limits": {"cap": {"cpu": "32"}}, "as": "vms"},
    {"object": "/factory/ci/egress", "ops": ["tunnel"], "limits": {"hosts": ["proxy.golang.org:443"]}, "as": "egress"}
  ]
}
```

- **Parents.** The parent of `path` must exist and be `active`.
  Intermediate namespaces are not created.
- **Empty.** It holds nothing but its `control` endpoint until
  something is granted to it.
- **`lease`.** Optional. With one, the namespace is deleted when it
  expires; without one, it lives until something calls `delete`. There
  is no other way for it to go
  ([design 2.2.6](../shibu.md#226-leases-and-ttls)). Deleting it once
  its VMs and agents have exited is the creator's job, or a reaper's.
- **No egress of its own.** A new namespace reaches nothing outside
  Shibu until something grants it `/sys/egress`, here the `egress`
  entry in `grants` ([Egress and ingress](#egress-and-ingress)).
- **`keep`.** Sugar for a deposit area
  ([design 2.2.4](../shibu.md#224-lifecycle-and-keeping-results)).
  `keep: "5Gi"` is short for `{"bytes": "5Gi"}`. The kernel creates the
  folder `<caller>/keep/<name>/` (or `into`), issues
  `grant(<from>, to=path, ops=[create], limits={into: "<area>/*", cap: {bytes}}, as: "keep")`,
  and records it in the namespace's `keep` field. `from` may be left out
  when the caller holds exactly one volume allocator or grant.
- **`grants`.** Each entry is a [`grant`](#grant) without `to`; `to` is
  the new namespace. They are issued by the caller.
- **`agent`.** Makes it an agent namespace:
  `{"adapter", "model", "runs_in", "tools", "job", "config", "title"}`.
  The kernel labels it `kind: agent`, `adapter` and `title`, and
  creates the agent object, `<path>/agent`, in state `running` with no
  session yet, and the standard endpoints. `job` and `config` are
  served at `<path>/job`. `runs_in` names the runner endpoint whose
  sessions may act as the agent; the kernel issues a session binding
  only to it ([`agents.md`, The agent object](agents.md#the-agent-object)).
  The kernel launches nothing: the harness starts when someone holding
  `launch` on `runs_in` calls the runner's `launch`, which is what the
  `create_agent` helper does next ([`agents.md`, `create_agent`](agents.md#create_agent)).
- **Atomic.** The namespace, its agent object and every grant are
  created together, or none is.

### `delete`

`delete` takes any object. For a namespace, the namespace itself or an
ancestor may call it, or a holder of `delete` on its control endpoint.

- The namespace and its subtree enter `deleting`, with cause `delete`
  (and `ancestor` for every namespace below).
- Every VM is stopped with its usual stop signal and timeout (for a
  harness, the runner's SIGTERM and 30 seconds) and exits with reason
  `deleted`, and every agent ends with reason `deleted`. Creates queued
  for the subtree are dropped.
- Then every grant the namespaces received is revoked (cascading),
  every object is deleted, every component-allocated handle is deleted
  through its allocator's `delete`, and charges drop off their payers
  ([design 2.2.4](../shibu.md#224-lifecycle-and-keeping-results)).
- There is no grace period, and nothing can be moved out of a
  `deleting` namespace. A caller that wants to keep something moves or
  snapshots it first, then calls `delete`.
- A `delete` of a namespace that is already `deleting` returns it
  unchanged.
- For any other object, `delete` deletes the resource and its charge
  drops off its payer. Deleting a VM stops it with exit reason
  `deleted`. Deleting an object made by a component allocator calls the
  allocator's `delete` ([Component allocators](extension-points.md#component-allocators)).

---

## Grants

| Operation | Arguments | Returns |
| --- | --- | --- |
| `grant` | `object`, `to`, `ops`, `limits`, `subtree`, `regrant`, `reserved`, `lease`, `as`, `labels`, `idempotency_key`, `dry_run` | the grant |
| `revoke` | `grant_id`, `kill` | the grant, ended |
| `list_grants` | `path`, `direction` (`given`, `received` or `both`), `state`, `unused_since`, `page_size`, `page_token` | grants |

### `grant`

Gives `to` access to `object`, narrowed ([design, Grants](../shibu.md#grants)).

```json
{
  "object": "/factory/svc/db",
  "to": "/factory/lead",
  "ops": ["tunnel"],
  "limits": {"max_connections": 40},
  "subtree": false,
  "regrant": true,
  "lease": "8h",
  "as": "db"
}
```

- **What can be granted.** An endpoint the caller owns by the tree, or
  holds through a grant with `regrant: true`; a received grant without
  it fails with `permission_denied`. `object` may also be a
  namespace used as a folder: the grant then covers every endpoint in
  it, including ones created later, and the handle is a folder of
  handles.
- **Narrowing.** `ops` must be a subset of the caller's ops on the
  object, and every limit no looser than the caller's. Otherwise
  `not_narrowing`. There is no exception.
- **`reserved`.** Allocator grants only. The whole `cap` is set aside
  from the caller's headroom now. It can be issued only from reserved
  headroom, and fails with `quota_exceeded` if the cap doesn't fit
  ([Allocator limits](#allocator-limits)).
- **`subtree`.** Makes the handle visible in the grantee's whole
  subtree. Not allowed on `control` or `allocator` endpoints
  ([design, Grants](../shibu.md#grants)), nor on a `secret` with
  `read`, nor on a VM's `mcp` with `exec`, which would hand a secret or a VM's
  identity to a whole subtree, including namespaces created after the
  owner checked who could reach it: `invalid_argument`.
- **`regrant`.** `false` by default: the grantee can use the grant but
  not grant from it. With `true`, the grantee may grant onward, and
  each re-grant must narrow, as above. An ancestor of the grantee needs
  the same flag to grant from the handle ([Authority](#authority)).
  Objects the caller owns by the tree are unaffected. Set it only where
  delegation is meant, such as allocator caps, a `/sys/egress` grant,
  or a launch budget for a lead agent to split ([design, Grants](../shibu.md#grants)).
- **`as`.** The handle's name in the grantee. Defaults to the object's
  last path segment. A name that is taken fails with `already_exists`.
- **Secrets.** A `read` grant is the only way a value leaves the store,
  to a runner, `/sys/model`, `/sys/cred` or anything else. It marks the
  grantee credential-holding ([Secrets](#secrets)), as information. It
  is never refused because of what the grantee's subtree runs, agent
  VMs, VM `mcp` endpoints and runners included: where a secret may go
  is its owner's decision, and the owner asks
  [`exposure`](#exposure) who could reach it before granting. The
  usual form is leased, with `regrant: false`. The one restriction is `subtree`, above: a `read` grant
  names its reader, so that the exposure the owner checked covers every
  namespace that gets the value.
- **Audit.** Every grant is an audit line naming the granter, the
  grantee, the object, the ops and limits, and the chain
  ([Audit records](#audit-records)).

### `revoke`

Revokes a grant. The granter, an ancestor of the granter, or an
ancestor of the grantee may call it.

- It cascades to every grant issued from it, found through the
  kernel's grant-chain index (grants by `issued_from`) rather than a
  scan ([design F22](../shibu.md#f22-cascading-revokes-can-be-expensive),
  [`state/v1`](extension-points.md#kernel-state-store-contract-statev1)).
- For an `http`, `volume` or `control` endpoint, new requests are
  refused at once and in-flight HTTP requests finish. **Every tunnel
  and connection opened through the grant, or a grant it cascades to,
  is closed at once** ([design, Grants](../shibu.md#grants)).
- For an allocator grant, new creates are refused at once. Objects
  already created run until deleted or their lease expires. With
  `kill: true` they are deleted too.
- For a secret `read` grant, further reads are refused. The grantee
  stops being credential-holding once it holds no other `read` grant.
- For a `/sys/egress` grant, every connection opened through it is
  closed, as for any tunnel. For a grant to `/sys/ingress`, the public
  URL stops answering.
- For a launch grant on a runner, new launches are refused at once, and
  the sessions launched through it are stopped, with reason `stopped`.
- For a grant on a VM's `mcp`, further tool calls are refused at once; an
  `exec` in flight is killed.

The same happens when a grant's lease expires, and when it becomes
invalid under the validity rule ([design, Grants](../shibu.md#grants)):
its target is deleted, a `move` takes the target out of the granter's
authority, or the granter's own authority ends.

### `list_grants`

Lists the grants a namespace has given, received, or both, for audit.
The caller sees grants given or received in its subtree, and grants it
holds.

| Argument | Meaning |
| --- | --- |
| `path` | the namespace |
| `direction` | `given`, `received` or `both` (default) |
| `state` | `active`, `ended` or `any` |
| `unused_since` | a time or a duration (`30d`): only grants whose `last_used` is before it, or `null` and issued before it |
| `page_size`, `page_token` | [Pagination](README.md#pagination) |

`unused_since` is how a long-lived namespace finds grant sprawl. There
is no automatic expiry beyond leases: an unused grant without a lease
stays until it is revoked or becomes invalid
([design F23](../shibu.md#f23-grant-sprawl-in-long-lived-namespaces)).

---

## Endpoints

| Operation | Arguments | Returns |
| --- | --- | --- |
| `create_endpoint` | `path`, `kind` (`http` or `allocator`), `target` (`{"vm": <ref>, "port": <n>}`), `contract` (for `http`, optional: the contract it speaks, such as `runner/v1`), `allocator` (for `allocator`: its declaration) | the endpoint |
| `delete_endpoint` | `path` | the endpoint |

Most endpoints are made as a side effect: a VM's `endpoints` in its
spec, a volume's path, a grant's handle, an agent's standard endpoints.
These two operations add or remove an endpoint on a VM port in the
caller's subtree while the VM runs. `delete_endpoint` ends every grant
on the endpoint as if its target were deleted.

An `allocator` endpoint makes the VM's service a **component
allocator**. Its declaration, the kinds and dimensions it serves, is
in [`allocator/v1`](extension-points.md#component-allocators).

---

## Resources

| Operation | Arguments | Returns |
| --- | --- | --- |
| `create` | `allocator`, `target`, `spec`, `lease`, `labels`, `wait`, `idempotency_key`, `dry_run` | the object, or a waiter |
| `move` | `object`, `from`, `to`, `payer` | the object |

Snapshots and clones are `create`s of a volume with `spec.from`, and
usage is part of [`get`](#get-and-list) and [`cost`](#cost).

### `create`

Creates a resource through an allocator or a grant of one, writes its
handle to `target`, and charges the allocator or grant named in
`allocator` ([design, Allocator](../shibu.md#allocator)).

```json
{
  "allocator": "/factory/ci/c17-1/vms",
  "target": "/factory/ci/c17-1/vm-1",
  "spec": {"kind": "vm", "shape": {"cpu": 8, "ram": "32Gi"}, "image": "/factory/svc/images/ci-base",
           "restart": "on-failure"},
  "lease": "45m",
  "wait": "10m",
  "idempotency_key": "c17-1-vm-1"
}
```

- **`target`** must be in the caller's subtree, or match the grant's
  `into`.
- **Charge.** The kernel walks the grant chain from the named
  allocator up to its root, checks headroom (cap minus use minus
  reservations held by others) for every dimension at every link, and
  charges every link ([Implementation note](#enforcement-order)). The
  resource's **payer** is the named allocator or grant, and its
  **home** is `target`'s namespace.
- **`wait`.** If any link is full, the request queues FIFO on that
  allocator and returns at once with
  `{"waiter": {"id": "req_…", "position": 3, "blocked_on": "gr_77"}}`.
  When charges free up it is admitted, and `watch` reports
  `admission.admitted` with the object. If `wait` runs out first it is
  dropped, with `admission.dropped` and reason `timeout`
  ([design 2.2.21](../shibu.md#2221-admission-queue)). Without `wait`, a
  full link fails with `quota_exceeded`.
- **`spec.kind`** is the allocator's kind, and may be left out. The
  kernel has two resource kinds:

| `kind` | `spec` |
| --- | --- |
| `vm` | `shape` (`cpu`, `ram`, `disk`), `image` (a read-only volume), `restart` (`never`, `on-failure` or `always`; default `never`), `volumes` (mount point → volume ref, with `:ro` for read-only), `endpoints` (port → name, or → `{"name", "contract"}`), `env` (non-secret strings). Its placement class is its allocator's |
| `volume` | `size`, `read_only` (default `false`), and optionally `from`: a volume, copied as it is at the create, or `{"oci": "<image ref>"}`. A read-only copy of a volume is a **snapshot**, a writable copy of a snapshot a **clone**, and a volume from an OCI image an **image** (always read-only). `size` defaults to the source's. The copy is charged to the create's `allocator`, like any volume ([design 2.2.3](../shibu.md#223-volume-snapshot-and-clone)) |

  A component allocator's kinds take whatever its declaration says
  ([`allocator/v1`](extension-points.md#component-allocators)).
- **Adopt.** A component allocator may answer a `create` by naming a
  resource it already owns instead of making one, and the kernel
  adopts it into `target` ([`adopt`](#adopt)). The caller sees an
  ordinary `create`. This is how a warm pool hands out a pre-booted VM
  ([`warm-pool/v1`](extension-points.md#warm-poolv1)).
- **Pinned VMs.** A VM whose runner hosts a live session can't be
  adopted or moved, and fails with `pinned`
  ([design, `create_agent`](../shibu.md#create_agent)). Deleting it
  stops the agent.
- **Errors.** `quota_exceeded`, `wait_timeout`, `deleting`,
  `pinned`, `component_unavailable`, `adopt_refused` (a
  component allocator named a resource the kernel won't adopt,
  [`adopt`](#adopt)), `unsupported` (a `from` the driver can't
  snapshot).

### `move`

Re-homes an object ([design, Move](../shibu.md#move)).

- `from` and `to` must both be in the caller's subtree, and either may
  be the caller.
- After the move, the object's visibility and lifetime are `to`'s.
- Its payer stays the same unless `payer` names a new one, which must
  be an allocator or grant the caller holds that allows `to`. Apart from
  an [adopt](#adopt), a move is the only way a payer changes.
- Neither `from` nor `to` may be `deleting`; such a move fails with
  `deleting`. A parent keeping something from a child it is about to
  delete moves it first, and names a `payer` outside the child if the
  object was charged to a grant the child received, since that grant
  is revoked with the child
  ([design 2.2.4](../shibu.md#224-lifecycle-and-keeping-results)).
- Grants on the object survive the move, unless it takes the object out
  of their granter's authority, in which case they end with reason
  `invalid`.

### `adopt`

Not an operation anyone calls: a way a component allocator answers
[`create`](#create) ([design 2.2.2](../shibu.md#222-caps-reservations-and-adoption)).
`move` re-homes only within the caller's subtree; `adopt` hands a
resource from an allocator's namespace to the target of a create made
through it, wherever that is.

**The answer.** The component's response to `/_alloc/v1/create`
([`allocator/v1`](extension-points.md#what-the-kernel-calls)) may name
an existing object instead of a `ref` of its own:

```json
{"adopt": {"object_id": "vm_7c"}}
```

**What the kernel checks**, before it changes anything:

1. **Ownership.** The object is homed in the allocator's own namespace
   (the namespace that owns the allocator endpoint) or in its subtree.
   It is not in a `deleting` namespace, and it hasn't already been
   adopted under another handle.
2. **Kind.** The object is of the kind the create asked for, and one
   the kernel can adopt: `vm`, or a writable `volume` (including a
   clone). Read-only volumes, endpoints, agents and secrets can't be
   adopted.
3. **Shape.** For a `vm`: the same `shape` and `image` as the create's
   spec, the placement class of the allocator's own VM grant, `restart`
   as asked, and no `env` the spec doesn't have. For a `volume`: the
   same `size`, and for a spec with `from`, a clone of that volume. The object has no volumes attached that the
   spec doesn't name.

**What it does**, in one commit:

- **Re-homes** the object to `target`'s namespace, at `target`'s path.
  Its visibility and lifetime are the target's from then on. Grants the
  old home gave on it end with reason `invalid`, and endpoints on its
  ports in the old home are removed.
- **Moves its charge.** The object's payer becomes the grant the create
  named, charged in the allocator's declared dimensions like any handle
  of it. Its kernel units stay charged where the allocator created it,
  on the allocator's own kernel grant, so nothing is counted twice and
  the allocator can't mint kernel quota
  ([design, Component allocators](../shibu.md#component-allocators)).
  Revoking that kernel grant with `kill` deletes the object wherever it
  now lives, as for any resource the grant paid for.
- **Applies the rest of the create**: `lease` and `labels`; for a `vm`, the spec's `volumes` (attached through the
  driver) and `endpoints`.
- **Remaps the origin.** For a `vm`, the web proxy's map from origin to
  namespace changes to the new home before the create returns
  ([`web-proxy.md`, Identity](web-proxy.md#identity)). Nothing in the
  VM changes, and no credential is injected.

The kernel records the handle as adopted. It deletes an adopted object
itself, as a kernel object, and never calls the allocator's `delete`
for it; the allocator's `list` doesn't report it, and reconciliation
doesn't expect it to.

**Errors.** A check that fails fails the caller's `create` with
`adopt_refused`, with `details.reason`: `not_owned`, `deleting`,
`already_adopted`, `kind_mismatch`, `not_adoptable` or
`shape_mismatch`. The object stays where it was, the create's charge is
released, and the kernel doesn't ask the component again: a component
that wants to cold-boot instead must do so before it answers. A failure
to attach the spec's volumes after the commit shows in the VM's status,
as any driver failure does ([Enforcement order](#enforcement-order)).

**Idempotency.** The kernel retries `/_alloc/v1/create` with the same
`handle_id`, and the component must answer with the same object. An
answer naming the object already adopted under this `handle_id` is the
same call, and returns the same result; a different object is
`adopt_refused` with `already_adopted`, and the first adoption stands.
A caller's retry with the same `idempotency_key` returns the adopted
object.

**Audit and events.** An adopt is one audit record with action `adopt`,
made by the kernel on the allocator's behalf
([Audit records](#audit-records)), alongside the caller's `create`:

```json
{"id": "aud_9", "at": "…", "action": "adopt",
 "actor": {"ns_id": "ns_pool", "ns_path": "/factory/ci/pool", "vm_id": "vm_p1", "via": "kernel", "person": null},
 "object": {"id": "vm_7c", "path": "/factory/ci/c17-1/vm-1"},
 "detail": {"handle_id": "h_31", "allocator": "/factory/ci/pool/alloc", "create_audit_id": "aud_8",
            "from": "/factory/ci/pool/w-3", "to": "/factory/ci/c17-1/vm-1",
            "payer_before": "gr_pool_vm", "payer_after": "gr_c17", "kernel_payer": "gr_pool_vm"}}
```

`watch` reports `object.adopted` to whoever can see the old home or the
new one ([Events](#events)).

---

## Leases and labels

| Operation | Arguments | Returns |
| --- | --- | --- |
| `set_lease` | `object`, `ttl` (a duration, or `null` to remove) | the object |
| `label` | `object`, `labels` (a map; a `null` value removes the key) | the object |

- `set_lease` works on any object, grants included
  ([design 2.2.6](../shibu.md#226-leases-and-ttls)). When a lease
  expires the object is deleted, or the grant revoked; a namespace
  enters `deleting` with cause `lease`. Any lease may be removed by a
  caller allowed to set it; requiring one is policy, beyond v1
  ([design 2.7](../shibu.md#27-beyond-v1-policy)).
- `watch` can warn before a lease expires: see `lease_warning` in
  [`watch`](#watch).
- `label` sets labels and metadata on any object the caller may label.
  The kernel's own labels (`kind`, `adapter`, `phase`) can't be changed
  this way, except as [`agents.md`](agents.md#status-labels) allows for
  `phase`. A grant has no say over the labels of what is created through
  it: the creator labels its own objects.

---

## Secrets

*Kernel* ([design 2.2.24](../shibu.md#2224-secret-store)). A secret is
written with `put_secret` and read with `read`, through a `read` grant.
There is nothing else: no `use`, no kernel-issued grants, no version
API and no login flow in the kernel.

| Operation | Arguments | Returns |
| --- | --- | --- |
| `put_secret` | `path`, `value`, `manifest`, `idempotency_key` | the secret's metadata |
| `read` | `secret`, `previous` (optional, `true` for the previous value during the overlap) | `{"value", "version"}` |

### `put_secret`

Writes a new value of the secret at `path`. The caller must own the
path by the tree (it is in the caller's subtree) or hold `write` on the
secret through a grant. The first write creates the secret.

- **Write-only.** There is no `get`, and writing doesn't let the writer
  read the value back.
- `value` is a string of at most 64 KiB, or `{"base64": "…"}` for
  bytes.
- `manifest` optionally records which adapter credential it is for
  (`{"adapter": "claude-code", "credential": "anthropic", "kind": "api-key"}`),
  so a UI can show it in the right form
  ([`agents.md`, Adapter manifest](agents.md#adapter-manifest)).
- **Rotation is writing a new value.** Readers get it on their next
  `read`. The store keeps the previous value readable for a fixed,
  short overlap, the same for every secret (the `/sys` spec's
  `secrets.overlap`, default `10m`, `0` for none,
  [`sys-spec.md`](sys-spec.md#secrets-backend)), so a reader that
  cached the old value can finish what it was doing. The overlap is a
  property of the store, not an argument.
- **Logins are written like anything else.** An OAuth or subscription
  login is obtained by the runner or the UI component, which runs the
  vendor's flow and writes the result with `put_secret`
  ([`agents.md`, Adapter manifest](agents.md#adapter-manifest)).
- Each write is an audit line. The value is never logged.

### `read`

Returns the value to a caller holding a `read` grant on the secret.
Nothing else can read one, the secret's owner included.

- A `read` grant is an ordinary grant with `ops: ["read"]`, issued by
  the secret's owner (or a holder with `regrant`, which is rare) to
  whatever needs the value: a runner that hands a login to a CLI,
  `/sys/model` or `/sys/cred` attaching a key on the way out, a `/sys`
  component through its spec entry's `reads`
  ([`sys-spec.md`](sys-spec.md)). The usual form is leased and
  `regrant: false`.
- What the reader does with the value is the reader's. A caller that
  names a secret to a component (`create_agent`'s `credential`, a
  `/sys/cred` endpoint's `secret_ref`, a `/sys/model` grant's
  `credential` limit) passes a **reference**; the component reads it
  through its own grant, and the call fails if it holds none. A
  component uses a reference only for the namespaces its `read`
  grant's `for` limit names, by default the granter's subtree
  ([`extension-points.md`](extension-points.md#common-to-every-distribution-contract)),
  so holding the component's endpoint never lends anyone else's key.
- `previous: true` returns the previous value during the overlap, and
  `not_found` after it.
- Every read is an audit line naming the secret, the version, the
  reader's namespace and the `read` grant.
- Holding a `read` grant marks the holder's namespace credential-holding
  ([design 2.2.7](../shibu.md#227-credentials-only-behind-proxy-endpoints)).

### `exposure`

`POST /_shibu/v1/exposure` with `{"object": "<VM, runner endpoint or secret>"}`.
Says who can `exec` on, or reach, a VM or runner, and who holds `read`
on a secret, computed by the kernel from the grant graph
([design 2.2.7](../shibu.md#227-credentials-only-behind-proxy-endpoints)).
The caller must own the object, by the tree, or hold a grant on it; a
grantee sees only the part of the answer below its own grant.

```json
{
  "object": {"kind": "vm", "id": "vm_b2", "path": "/factory/lead/workers/build/b2"},
  "exec": [{"ns_path": "/factory/lead/workers/w17", "grant_chain": ["gr_41"], "lease_until": "2026-10-04T20:00:00Z"}],
  "write": [],
  "reach": [{"ns_path": "/factory/lead", "via": "tree", "endpoints": ["mcp", "runner"]}],
  "sessions": [],
  "secrets_delivered": [{"secret": "/factory/secrets/pkg-token", "grant": "gr_9"}]
}
```

- For a **VM**: `exec`, `write` and `reach` list every namespace that
  holds those ops, or any endpoint on it, with the chain;
  `sessions` lists the runner sessions on it; `secrets_delivered`
  lists the secrets its namespace holds `read` on.
- For a **runner**: `launch` lists the holders of `launch`, `sessions`
  the agents on it now, and `vms` the VM answer for each VM it runs on.
- For a **secret**: `read` lists every holder, and
  `vms` the VM answer for each VM in those namespaces.

Every holder of `exec` on a VM is treated as able to read everything
the VM holds. For a session on a runner, the other sessions on the
same runner VM are listed as able to reach it only when the runner's
reported isolation is below the `isolation` the session's launch grant
required ([design 6.12](../shibu.md#612-session-isolation-inside-pool-runners)). `watch` with `{"exposure": "<object>"}` streams a change
event whenever a grant changes the answer.

---

## Orchestration

| Operation | Arguments | Returns |
| --- | --- | --- |
| `get` | `object` | the object |
| `list` | `path`, `selector`, `depth`, `include_grants`, `page_size`, `page_token` | objects |
| `watch` | `path`, `selector`, `types`, `since`, `lease_warning` | an event stream |
| `audit` | `path`, `since`, `until`, `types`, `unused_grants`, `page_size`, `page_token` | audit records, and unused grants |
| `cost` | `path`, `since`, `until`, `group_by` | cost records, and unused grants |
| `batch` | `ops`, `idempotency_key`, `dry_run` | every result, or the first failure |

On a `control` endpoint, `get`, `list`, `watch`, `audit`, `cost`,
`exposure` and `list_grants` need `read`, and every other operation on
that namespace needs `write` ([Ops and limits](#ops-and-limits)).

### `get` and `list`

`get` returns one object. `list` returns the objects under `path` that
the caller can see: its subtree, and the handles it holds or that are
visible to it with `subtree: true`
([design 2.2.8](../shibu.md#228-orchestration-surface)).

- `selector` filters on `kind`, labels and a few fields, as
  comma-separated terms ANDed together: `kind:agent,phase:waiting-on-human`,
  `kind:vm,status:exited`, `label:factory.dev/item=17`,
  `credential_holding:true`. A term `key:value` matches `kind`, the
  object's `status` or `phase`, `state`, or a label of that name.
- `depth` limits how far below `path` to go: `1` for direct children,
  unlimited by default.
- `include_grants: true` also lists, under `path`, the objects reached
  through handles, as the handles point at them.
- **Usage.** `get` of an allocator handle, an allocator grant, a
  metered grant or a resource includes a `usage` field: per dimension
  and budget, the amount charged, the cap, what is `reserved`, and the
  headroom, at every link of the chain the caller can see. For a
  component allocator it includes the allocator's own **reported**
  figures, marked so ([design, Component allocators](../shibu.md#component-allocators)).
  Use over a period is [`cost`](#cost).

### `watch`

Streams [events](#events) about objects under `path` that the caller
can see, filtered by `selector` and by event `types`
([Streams](README.md#streams)). The stream starts with the current
state if `since` is not given: one `object.snapshot` event per matching
object, then `watch.synced`, then changes.

- `lease_warning` is a duration, `null` by default. With it, the stream
  carries `lease.expiring` for each matching object whose lease will
  expire within that lead time: once when it comes within the lead
  time, and again if `set_lease` moves the expiry out and it comes
  within it again. The lead time belongs to the watch, so each watcher
  picks its own and the object stores nothing but its lease
  ([design 2.2.6](../shibu.md#226-leases-and-ttls)).

### `audit`

Returns the [audit records](#audit-records) for the caller's subtree
and for actions taken on objects it holds. With `unused_grants: true`
it also returns `unused_grants`: the grants given or received under
`path` that were not used between `since` and `until`, with their
`last_used` ([design F23](../shibu.md#f23-grant-sprawl-in-long-lived-namespaces)).

### `cost`

Returns [cost records](#cost-records) for `path`, attributed along
grant chains ([design 2.2.8](../shibu.md#228-orchestration-surface)).
`group_by` is `namespace` (default), `payer`, `allocator`, `dimension`
or `label:<key>`. The answer also lists `unused_grants`: allocator and
metered grants under `path` that charged nothing in the period, so a
grant that holds headroom and is never used shows up beside what was
spent ([design F23](../shibu.md#f23-grant-sprawl-in-long-lived-namespaces)).

### `batch`

Runs a list of kernel operations as one, all or nothing
([design 2.2.8](../shibu.md#228-orchestration-surface)).

```json
{
  "idempotency_key": "factory-sync-41",
  "dry_run": false,
  "ops": [
    {"op": "create_namespace", "args": {"path": "/factory/ci", "lease": null}},
    {"op": "grant", "args": {"object": "/factory/egress", "to": "/factory/ci", "ops": ["tunnel"],
                              "limits": {"hosts": ["proxy.golang.org:443"]}, "as": "egress"}},
    {"op": "create", "args": {"allocator": "/factory/svc/volume-alloc", "target": "/factory/svc/db-data",
                              "spec": {"kind": "volume", "size": "200Gi"}}}
  ]
}
```

- **Ops.** Any kernel operation that changes state: `create_namespace`,
  `delete`, `grant`, `revoke`, `create`, `move`, `create_endpoint`,
  `delete_endpoint`, `set_lease`, `label`, `put_secret` and `stop`.
  Not `batch` itself, and not a call to any component: a runner's
  `launch` is made after the batch commits.
- **Order.** Ops run in order against the state the earlier ones left,
  so a later op may name a path an earlier one created. Each is checked
  as if called alone ([Enforcement order](#enforcement-order)).
- **Atomic.** Every op commits together, or none does, and the result
  is the first failure with its index (`details.op_index`). Booting
  VMs, driver calls and component-allocator calls happen after the
  commit; a failure there shows as the object's status, not as a failed
  batch. A `create` with `wait` isn't allowed in a batch.
- **Idempotency key** covers the whole batch
  ([Idempotency](README.md#idempotency)). **Dry run** returns each op's
  result and the charges along every chain, and changes nothing
  ([Dry run](README.md#dry-run)).
- **Results.** `{"results": [<each op's result>]}`, in order.

Declarative `apply` of a document is not a kernel operation. The
standard-distribution reconciler diffs a document against `list` and
calls `batch` ([`reconciler/v1`](extension-points.md#reconcilerv1)),
for any subtree and for `/sys`.

---

## Events

Every event has this envelope, and `watch` delivers them in order per
object:

```json
{"id": "ev_9f", "type": "vm.status", "at": "2026-10-04T05:31:02Z",
 "object": {"id": "vm_2b", "path": "/factory/ci/c17-1/vm-1", "kind": "vm"},
 "data": {"status": "exited", "exit": {"code": 1, "reason": "failed"}, "restart_count": 3}}
```

| `type` | `data` |
| --- | --- |
| `object.snapshot` | the object, at the start of a `watch` without `since` |
| `watch.synced` | none: the snapshot is complete |
| `object.created`, `object.changed`, `object.deleted` | the object, or for `deleted` its last state |
| `object.moved` | `from`, `to`, `payer_before`, `payer_after` |
| `object.adopted` | `from`, `to`, `allocator`, `handle_id`, `payer_before`, `payer_after` ([`adopt`](#adopt)) |
| `object.labeled` | the changed labels |
| `namespace.deleting` | `cause` (`delete`, `lease` or `ancestor`) |
| `lease.expiring` | `expires_at`, `lead_time`: the object's lease expires within the watch's `lease_warning` ([`watch`](#watch)) |
| `namespace.credential_holding` | `value` (true or false), and the grant that changed it |
| `batch.committed` | `batch_id`, `idempotency_key`, `op_count` ([`batch`](#batch)) |
| `grant.created` | the grant, to the granter and the grantee |
| `grant.ended` | `reason` (`revoked`, `lease`, `cascade`, `invalid`, `grantee-deleted`, `target-deleted`), `closed_connections` |
| `vm.status` | `status`, `exit`, `restart_count` |
| `agent.status` | `status`, `exit` ([`agents.md`](agents.md#the-agent-object)) |
| `admission.queued` | `waiter`, `position`, `blocked_on` |
| `admission.admitted` | `waiter`, the created object |
| `admission.dropped` | `waiter`, `reason` (`timeout`, `deleting`, `revoked`) |
| `allocator.leak` | `handle`, `allocator`, `attempts`, `last_error` |
| `allocator.leak_resolved` | `handle` |
| `message.posted` | `endpoint`, `message_id`, `type`, `from` ([`agents.md`, `messages`](agents.md#messages)); never the message's text |
| `artifact.posted` | `endpoint`, `artifact_id`, `artifact_kind`, `title` |
| `secret.written` | `version`; never the value |
| `budget.exhausted` | `grant`, `budget_key`, `link` |
| `sys.applied` | `version`, `source` ([`sys-spec.md`](sys-spec.md)) |
| `sys.swap` | `component`, `step` (`started`, `conformance`, `repointed`, `retired`, `rolled-back`, `refused`), `detail` |
| `driver.status` | `driver`, `status` (`admitted`, `refused`, `draining`, `removed`), `detail` |

A grant's grantee sees `grant.created` and `grant.ended` for grants it
receives, even outside its subtree. Everyone else sees events only for
objects they can see.

---

## Audit records

Every change and every secret read is an audit record
([design, Grants](../shibu.md#grants), [2.2.24](../shibu.md#2224-secret-store)):

```json
{"id": "aud_7", "at": "…", "action": "grant",
 "actor": {"ns_id": "ns_lead", "ns_path": "/factory/lead", "vm_id": "vm_5", "via": "runner", "person": null},
 "object": {"id": "ep_db", "path": "/factory/lead/db"},
 "detail": {"grant_id": "gr_g", "to": "/factory/lead/workers/w17", "ops": ["tunnel"],
            "limits": {"max_connections": 4}, "lease": "8h", "chain": ["ep_51x", "gr_77", "gr_g"]}}
```

`action` is one of: `create_namespace`, `delete`, `deleting.started`,
`deleting.finished`, `grant`, `revoke`, `grant.ended`, `set_lease`,
`label`, `create`, `move`, `adopt`, `create_endpoint`,
`delete_endpoint`, `stop`, `exit`, `session.bound`, `put_secret`,
`secret.read`, `batch`, `sys.apply`, `front_door.login`,
`front_door.act_as`, `allocator.delete_failed`. A `batch` record lists
the audit IDs of the ops it committed, each of which has its own
record.

`actor.via` is `runner` for a harness session acting as an agent
through a session binding, and
`kernel` for actions the kernel took itself (a lease expiring, a
cascade). `actor.person` names the person behind a front-door caller,
as in the caller assertion
([`web-proxy.md`, The caller assertion](web-proxy.md#the-caller-assertion)),
and is `null` for a VM.

---

## Cost records

```json
{"namespace": "/factory/ci/c17-1", "since": "…", "until": "…",
 "lines": [
   {"payer": "gr_c17", "chain": ["/vm-spot", "gr_ci", "gr_c17"], "dimension": "cpu_hours",
    "amount": "12.5", "source": "counted"},
   {"payer": "gr_m", "chain": ["/sys/model", "gr_lead", "gr_m"], "dimension": "usd",
    "amount": "3.41", "source": "reported", "reporter": "/sys/model"},
   {"payer": "gr_gpu", "chain": ["/gpu/pool", "gr_gpu"], "dimension": "gpu-slices",
    "amount": "2", "source": "leak", "handle": "ep_slice-4"},
   {"payer": null, "chain": ["/sys/runner", "gr_lead", "gr_r9"], "dimension": "usd",
    "amount": "1.20", "source": "reported", "reporter": "/sys/runner", "charged": false}
 ],
 "unused_grants": [{"grant": "gr_old", "object": "/vm-gpu", "last_used": null}]}
```

- `source` is `counted` for what the kernel counts itself, `reported`
  for what a component allocator, a metered endpoint or a runner reported
  ([design F33](../shibu.md#f33-a-component-allocator-can-misreport-usage)),
  and `leak` for a handle whose allocator's `delete` keeps failing,
  charged to the grantor
  ([design F34](../shibu.md#f34-cleanup-leaks-when-a-component-allocator-is-down)).
- `charged: false` marks a runner's report that is shown but not
  charged, because its launch grant doesn't carry `usage: "reported"`
  ([design 6.13](../shibu.md#613-accepting-a-runners-usage-reports)).
  Such a line counts against no budget and has no payer.
- Each charge is recorded against every link of its chain, so `cost`
  adds up per namespace at every level
  ([design, Quota](../shibu.md#quota)).

---

## Enforcement order

What every mutating operation checks, in this order. The first failure
is the error returned.

1. **Identity.** Who the caller is, from origin or the front door.
2. **Visibility.** Every reference resolves to something the caller can
   see, or `not_found`.
3. **Authority.** The caller holds the ops the operation needs on each
   object, by the tree or a grant, or `permission_denied`. To grant
   from a handle it received, the caller (or the grantee it acts for as
   an ancestor) must hold it with `regrant: true`.
4. **State.** No target is in or under a `deleting` namespace
   (`deleting`); idempotency key conflicts (`conflict`). In a
   [`batch`](#batch), against the state the earlier ops left.
5. **Pinning.** A VM whose runner hosts a live session isn't adopted
   or moved (`pinned`).
6. **Narrowing**, for grants (`not_narrowing`). Every limit narrows;
   a `reserved` grant is checked against reserved headroom in the next
   step.
7. **Quota.** One walk of the grant chain, root first, taking per-link
   locks in that fixed order so concurrent creates can't deadlock, with
   headroom cached per link
   ([design, Implementation note](../shibu.md#implementation-note-walking-the-grant-chain)).
   `quota_exceeded`, or a waiter with `wait`.
8. **Commit**, of the operation or of the whole batch, and only then
   the driver, component or runner calls, whose failures show as
   status.

Cascading revokes walk the same chains in the other direction, through
the grant-chain index ([`revoke`](#revoke)).

v1 has no policy step. Deny predicates beyond v1 would be evaluated
per call by the web proxy, after authority, failing closed
([design 2.7](../shibu.md#27-beyond-v1-policy)).
