# Shibu API specification, v1

> Silo ([bwsalmon/silo](https://github.com/bwsalmon/silo/blob/main/docs/silo/README.md)) is the current direction; this document is kept for reference.

## Status

Draft specification, no code. It is derived from the design in
[`../shibu.md`](../shibu.md), as revised up to task 1771, and was
written in task 1844. Task 1887 tightened the kernel API to match the
design's thirteenth revision: egress and public exposure are grants on
`/sys/egress` and to `/sys/ingress`, the kernel has five endpoint
kinds, secrets are `put_secret` and `read`, allocator limits are
trimmed, `apply` and `create_agent` are standard-distribution
components on top of a new `batch` operation. Task 1904 follows the
design's fourteenth revision: design questions 6.8–6.13 and findings
F21–F24, F26 and F32 are decided for v1 and specified where they
belong ([Decided for v1 in task 1904](#decided-for-v1-in-task-1904)),
and the kernel state store is a new kernel contract, `state/v1`,
embedded SQLite in v1
([`extension-points.md`](extension-points.md#kernel-state-store-contract-statev1)).

The design calls the names and signatures in its section 3
"illustrative". This specification fixes them: it gives every
operation a name, a wire shape, its errors and its rules, and it does
the same for every contract between the kernel and a component that
plugs into it.

- **Where the design decides something, this specification follows
  it**, and links to the section it comes from. If the two disagree,
  the design wins and this document is wrong.
- **Where the design leaves a shape open**, this specification picks
  one so that the API can be built. Every such choice is listed in
  [Choices this specification makes](#choices-this-specification-makes),
  so that a reviewer can check them in one place.
- **Where the design leaves a question open on purpose** (its section
  6, and the findings it marks as proposals), this specification
  doesn't answer it. [Open items](#open-items) lists each one and says
  what the specification does in the meantime.

The migration plan, [`../shibu-migration.md`](../shibu-migration.md),
puts the kernel behind an in-process Go API, `pkg/shibu`, before it
becomes its own service. That Go API follows the operations here one
to one. The HTTP binding in this specification is what Phase 3's
versioned API serves, and what `pkg/shibu` becomes a client of
([`shibu-migration.md`, Phase 3](../shibu-migration.md#phase-3-split-the-process-grain-becomes-components)).

## Documents

| Document | What it specifies |
| --- | --- |
| This file | Scope, conventions shared by every API (transport, naming, references, errors, idempotency, dry run, pagination, versioning), and the index of choices and open items |
| [`kernel-api.md`](kernel-api.md) | The kernel's control API: its object types, every operation, the events `watch` streams, the audit and cost records, and `batch` |
| [`web-proxy.md`](web-proxy.md) | The data plane: how endpoints are addressed, identity by origin, how grants are enforced on requests, the `Shibu-Caller` assertion, tunnels, egress through `/sys/egress`, public exposure through `/sys/ingress` and pane origins |
| [`agents.md`](agents.md) | The kernel's agent object and an agent's standard endpoints (`job`, `messages`, `artifacts`, `exit`, `agent-mcp`, `usage`), the harness contract, the adapter manifest, the VM `mcp` contract, the agent control MCP; and the standard distribution's `create_agent` helper, with `runs_in`, `tools` and placements |
| [`extension-points.md`](extension-points.md) | Every contract a pluggable piece implements: component allocators, backend drivers, secret storage backends, the metering API, `runner/v1` (which any VM may serve), and the distribution contracts `model`, `runner`, `egress`, `ingress`, `git`, `forge`, `credential-proxy`, `pkgcache`, `reconciler`, `ui`, `warm-pool`, `reaper` and `agent-helper`, with their conformance suites |
| [`sys-spec.md`](sys-spec.md) | The `/sys` spec: its schema, how it is applied, history and rollback, repo mode, and the staged swap |
| [`ui-conventions.md`](ui-conventions.md) | The conventions any UI renders: `messages` types, status labels, `artifacts`, the `requests` endpoint, agent-served panes and the pane bridge |
| [`cli.md`](cli.md) | `shibu`, the reference CLI, its convention commands (`requests`, `approve`), and the CLI-parity rule the `ui` contract is tested against |

## Layers and who implements what

Shibu has three layers
([design, Layers](../shibu.md#layers-kernel-standard-distribution-additional-components)).
This specification covers the APIs of the first, and the contracts
between the first and the other two.

| Layer | Implements | Specified in |
| --- | --- | --- |
| **Kernel** | the control API (namespaces, VMs, volumes, endpoints, grants, secrets, leases, labels, `move`, `adopt`, `watch`/`audit`/`cost` and `batch`), the web proxy, the front door, identity, the agent object and its standard endpoints (`messages`, `artifacts`, the agent control MCP), the harness contract, the `runner/v1` and VM `mcp/v1` contracts, the exposure query, the allocator contract, the driver contract, the metering API | [`kernel-api.md`](kernel-api.md), [`web-proxy.md`](web-proxy.md), [`agents.md`](agents.md), [`extension-points.md`](extension-points.md) |
| **Kernel plug-ins**, loaded by the kernel and chosen in the `/sys` spec | backend drivers, secret storage backends | [`extension-points.md`](extension-points.md#backend-driver-contract-driverv1) |
| **Standard distribution**, under `/sys/`, each behind a versioned contract | `model`, `runner`, `egress` (bootstrap set); `ingress`, `git`, `forge`, `credential-proxy`, `pkgcache`, `reconciler`, `ui`, `warm-pool`, `reaper`, the `create_agent` helper, the tunnel shim (optional) | [`extension-points.md`](extension-points.md#distribution-contracts), [`sys-spec.md`](sys-spec.md) |
| **Additional components** | anything else: inboxes, approval services, component allocators, application MCP servers, panes, grain's own components | the conventions they may follow, in [`ui-conventions.md`](ui-conventions.md) and [`extension-points.md`](extension-points.md#component-allocators) |

The extension points, in one list:

| Extension point | Who may plug in | Contract |
| --- | --- | --- |
| Component allocator | any namespace | [`allocator/v1`](extension-points.md#component-allocators) |
| Metered endpoint (an endpoint that enforces a budget) | any namespace | [metering API](extension-points.md#metering-api) |
| Backend driver | root, in the `/sys` spec | [`driver/v1`](extension-points.md#backend-driver-contract-driverv1) |
| Secret storage backend | root, in the `/sys` spec; compiled into the kernel | [`secret-backend/v1`](extension-points.md#secret-storage-backend-contract-secret-backendv1) |
| Distribution component | root, in the `/sys` spec | [`model/v1`, `runner/v1`, `egress/v1`, `ingress/v1`, `git/v1`, `forge/v1`, `credential-proxy/v1`, `pkgcache/v1`, `reconciler/v1`, `ui/v1`, `warm-pool/v1`, `reaper/v1`, `agent-helper/v1`](extension-points.md#distribution-contracts) |
| Runner | any VM; root for `/sys/runner` | [`runner/v1`](extension-points.md#runnerv1) |
| Harness adapter | a runner's operator, in its image | [adapter manifest](agents.md#adapter-manifest) |
| VM tools | any VM | [VM `mcp`, `mcp/v1`](agents.md#vm-mcp-contract-mcpv1) |
| Application tools for agents | any namespace | [application MCP servers](agents.md#application-mcp-servers) |
| Panes in the UI | any namespace | [pane declaration and bridge](ui-conventions.md#agent-served-panes) |
| Requests and approvals | any namespace | [`requests` convention](ui-conventions.md#requests) |
| Artifacts | any namespace | [`artifacts` shape](ui-conventions.md#artifacts) |

## Conventions

These hold for every API in this specification unless a section says
otherwise. The key words **must**, **must not**, **should** and
**may** are used as in RFC 2119.

### Transport

- **The control API** is HTTP with JSON bodies. Every operation is a
  `POST` to `/_shibu/v1/<operation>`, with the operation's arguments as
  a JSON object in the body, and returns a JSON object. The operation
  names are the design's section 3 names (`create_namespace`, `grant`,
  `batch`, …). `watch` and the streaming forms of `read` return
  a `text/event-stream` instead ([Streams](#streams)).
- **Inside a VM** the control API is at `http://shibu.internal/_shibu/v1/`.
  The kernel also exports `SHIBU_ENDPOINT=http://shibu.internal` in
  every VM. The connection is plain HTTP to the web proxy over the VM's
  own interface. It is authenticated by origin, not by anything in the
  request ([`web-proxy.md`, Identity](web-proxy.md#identity)).
- **Outside Shibu** the control API is at
  `https://<shibu>/_shibu/v1/`, behind the front door
  ([`web-proxy.md`, The front door](web-proxy.md#the-front-door)).
- **Endpoints** are reached by path through the web proxy:
  `http://shibu.internal/<path>` inside a VM, and
  `https://<shibu>/<path>` from outside, for a caller the front door
  has authenticated ([`web-proxy.md`, Addressing](web-proxy.md#addressing)).
- **Kernel plug-ins** are not reached over the web proxy.
  [`extension-points.md`](extension-points.md) says how each one is
  called.

The operation tables in [`kernel-api.md`](kernel-api.md) list each
operation's name. Its HTTP form is always
`POST /_shibu/v1/<name>`, so they don't repeat it.

### Names and paths

- A **namespace path** is a `/`-separated list of segments, written
  with a leading `/` when it is absolute: `/factory/lead/workers/w17`.
  The API accepts a path without the leading `/` and treats it as
  absolute. The root namespace is `/`.
- A **segment** matches `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`: lower
  case, digits and inner hyphens, 1–63 characters. So a segment never
  starts with `_` or `.`. The web proxy reserves the URL segments that
  do (`_shibu`, `_self`, `_contract`, `_health`, …) for itself.
- **Reserved top-level namespaces.** The kernel creates `/sys` and
  `/users` at install. `/sys` holds the standard distribution and
  `/sys/secrets`. `/users/<name>` is one human principal's namespace
  ([design 2.2.18](../shibu.md#2218-human-as-a-principal)). Nothing but
  root can create directly under either.
- An **object path** is the path of the namespace an object lives in
  (its **home**), followed by one more segment that names the object:
  `/factory/svc/db` is the endpoint `db` in `/factory/svc`. Namespaces
  and the objects in them share one name space, so a namespace and an
  object in the same parent can't have the same name.
- **A snapshot** is a read-only volume, named like any other object:
  `/factory/svc/repo-warm-2026-10-02`. There is no `@` form.
- **Relative paths.** Wherever an argument takes a path, a path that
  starts with neither `/` nor `id:` is resolved against the caller's
  own namespace first, and as an absolute path if nothing matches. The
  design's examples (`lead/vm-std`, `w17/sbx`) are written this way.
  A response always gives absolute paths.

### Stable IDs and references

Every object has a **stable ID**, assigned when it is created and never
reused ([design 2.2.10](../shibu.md#2210-stable-ids)). It is an opaque
string made of a type prefix and a random part:

| Prefix | Object |
| --- | --- |
| `ns_` | namespace |
| `ep_` | endpoint (including handles, allocators, control endpoints and the agent standard endpoints) |
| `gr_` | grant |
| `vm_` | VM |
| `vol_` | volume, including images and snapshots |
| `ag_` | agent object |
| `sec_` | secret |
| `msg_` | message |
| `art_` | artifact entry |
| `ev_` | event |
| `aud_` | audit record |
| `req_` | admission-queue waiter |
| `sys_` | `/sys` spec version |

An argument that takes a **reference** accepts either a path or
`id:<stable ID>`. References that are stored, grants above all, are
stored by ID, so a `move` or a rename doesn't break them. A client
that keeps a reference beyond one call should keep the ID.

### Visibility and existence

The API never says that something exists to a caller who can't see it
([design, Visibility](../shibu.md#visibility)). A reference to an
object outside the caller's visibility fails with `not_found`, the same
as a reference to nothing. `permission_denied` is returned only when
the caller can see the object but the operation needs an op it doesn't
hold.

### Common fields

Every object returned by the API carries these fields, beside its own:

```json
{
  "id": "ns_3f9k2a",
  "path": "/factory/lead",
  "kind": "namespace",
  "labels": {"kind": "agent", "title": "planner"},
  "created_at": "2026-10-04T05:30:00Z",
  "created_by": {"ns_id": "ns_0000root", "ns_path": "/", "via": null},
  "lease": {"expires_at": "2026-10-04T13:30:00Z"}
}
```

- `kind` is one of `namespace`, `endpoint`, `grant`, `vm`, `volume`,
  `agent`, `secret`. The kernel's resource kinds are VM and volume;
  images and snapshots are read-only volumes. There is no `warm-pool`,
  `public-endpoint` or `egress` kind: a warm pool is a component
  allocator built on `adopt`
  ([`extension-points.md`, `warm-pool/v1`](extension-points.md#warm-poolv1)),
  public exposure a grant to `/sys/ingress`, and outside access a grant
  on `/sys/egress` ([`kernel-api.md`, Egress and ingress](kernel-api.md#egress-and-ingress)).
- An endpoint's `endpoint_kind` is one of `http`, `volume`,
  `allocator`, `control`, `secret`. Every other service (an agent's
  `messages`, `/sys/model`, a runner, a VM's `mcp`) is an `http`
  endpoint, reached by paths and methods
  ([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits)).
- `labels` is a map of string to string. Keys match
  `([a-z0-9.-]+/)?[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?`. Keys with no
  prefix and keys prefixed `shibu.dev/` are the kernel's vocabulary
  (`kind`, `adapter`, `title`, `status`, `phase`); anything else is
  free for applications, and the kernel doesn't interpret it.
- `lease` is `null` when the object has none.

### Scalars

- **Durations** are strings of decimal numbers with a unit suffix,
  `s`, `m`, `h` or `d`: `"45m"`, `"8h"`, `"14d"`, `"1h30m"`.
- **Times** are RFC 3339 in UTC.
- **Quantities of bytes** use binary suffixes, `Ki`, `Mi`, `Gi`, `Ti`:
  `"32Gi"`. A bare integer is bytes.
- **CPU** is a decimal number of cores: `2`, `0.5`.
- **Money** is a decimal string in US dollars: `"20.00"`.

### Errors

A failed operation returns an HTTP status and a body:

```json
{
  "error": {
    "code": "quota_exceeded",
    "message": "creating 8 cpu would take /factory/ci's grant gr_77 over its cap of 32 cpu",
    "details": {"link": "gr_77", "dimension": "cpu", "cap": "32", "used": "28", "requested": "8"},
    "retryable": false
  }
}
```

| `code` | HTTP | Meaning |
| --- | --- | --- |
| `invalid_argument` | 400 | The request is malformed, or an argument is out of range. |
| `unauthenticated` | 401 | Only outside Shibu: the front door has no session for the caller. A request from a VM is never unauthenticated: its origin is its identity. |
| `permission_denied` | 403 | The caller can see the object but doesn't hold the op the operation needs, or the operation needs root. Also a `grant` from a received handle that doesn't carry `regrant: true`. |
| `not_found` | 404 | No such object, or one the caller can't see ([Visibility and existence](#visibility-and-existence)). |
| `already_exists` | 409 | The target path is taken. |
| `conflict` | 409 | An idempotency key was reused with different arguments, or a precondition on the object's state failed (for example `move` from outside the caller's subtree). |
| `deleting` | 409 | The namespace, or an ancestor, is `deleting`, so nothing new can be created in it ([design 2.2.4](../shibu.md#224-lifecycle-and-keeping-results)). |
| `quota_exceeded` | 409 | A link of the grant chain has no headroom, and the call didn't ask to wait. `details.link` names the first link that is full. |
| `budget_exhausted` | 429 | A metered endpoint's budget is spent, on the named link. |
| `pinned` | 409 | The VM's runner hosts a live session, so it can't be adopted or moved ([`agents.md`, The agent object](agents.md#the-agent-object)). |
| `not_narrowing` | 403 | A grant asks for ops or limits beyond what the granter holds. |
| `runner_unavailable` | 503 | Only from the `create_agent` helper: its `runs_in` runner is missing, failing its contract, or full. The helper deletes the namespace it made. |
| `launch_refused` | 403 | Only from a runner or the `create_agent` helper: the runner refused the `launch`, applying the grant's limits or its own policy. `details.reason` is the runner's. |
| `component_unavailable` | 503 | A component allocator, or another component an operation depends on, didn't answer. |
| `adopt_refused` | 409 | A component allocator answered a `create` by naming a resource the kernel won't adopt: not owned by the allocator's namespace, already adopted, or not of the kind and shape asked for. `details.reason` says which ([`kernel-api.md`, `adopt`](kernel-api.md#adopt)). |
| `wait_timeout` | 409 | A `create` with `wait` was still queued when its timeout ran out. It has been dropped from the admission queue. |
| `egress_denied` | 403 | Only from `/sys/egress`: the caller holds no grant on it, or the destination is outside its grant's `hosts` ([`web-proxy.md`, Egress](web-proxy.md#egress)). |
| `rate_limited` | 429 | Reserved. v1 has no rate limits ([design F11](../shibu.md#f11-rate-limits-should-be-hierarchical)). |
| `unsupported` | 400 | The operation or option isn't supported by this kernel, or by the driver or component it would need. |
| `internal` | 500 | A kernel fault. It is always safe to retry with the same idempotency key. |

`retryable` says whether the same call can succeed later without a
change: true for `quota_exceeded` (headroom may free up),
`component_unavailable` and `internal`, false otherwise.

### Idempotency

- `create_namespace`, `create`, `grant`, `put_secret`, `batch`, and
  `POST` on an agent's `messages` and `artifacts` accept an **idempotency key**, either
  as the `idempotency_key` argument or as an `Idempotency-Key` header
  ([design 2.2.20](../shibu.md#2220-api-versioning-and-idempotency-keys)).
- A key is scoped to the calling namespace and the operation. A second
  call with the same key and the same arguments returns the first
  call's result, including its error, and has no other effect. A second
  call with the same key and different arguments fails with
  `conflict`.
- The kernel keeps a key for **at least 24 hours** after its first use.
- Every other operation is either naturally idempotent (`delete`,
  `revoke`, `label`, `set_lease`, `stop`, `exit`) or reads
  only.

### Dry run

`create_namespace`, `create`, `grant` and `batch` accept
`dry_run: true`
([design 2.2.19](../shibu.md#2219-dry-run-and-quota-preview)). A dry run
does every check the real call does, creates nothing and returns a
**plan**:

```json
{
  "dry_run": true,
  "would_create": [{"kind": "namespace", "path": "/factory"}, {"kind": "grant", "object": "/vm-std", "to": "/factory"}],
  "charges": [
    {"payer": "gr_12", "chain": ["/vm-std", "gr_12"], "amounts": {"cpu": "64", "ram": "256Gi"},
     "headroom_after": [{"link": "/vm-std", "cpu": "192"}, {"link": "gr_12", "cpu": "0"}]}
  ],
  "refusals": []
}
```

A refused dry run returns the plan with `refusals` filled in, and the
HTTP status and error the real call would have returned, with the plan
in `error.details.plan`.

### Pagination

`list`, `list_grants`, `audit`, `cost` and the non-streaming `read`
operations take `page_size` (default 100, at most 1000) and
`page_token`, and return `next_page_token`, empty on the last page.

### Streams

`watch`, and `read` on `messages` with `stream: true`, return a
`text/event-stream`. Each event is one SSE event whose `id:` is the
item's stable ID and whose `data:` is the item as JSON. A client
resumes with the `Last-Event-ID` header, or `since` in the body, and
the kernel replays what it missed, for as long as it keeps the items.
The kernel sends a comment line at least every 30 seconds, so idle
connections aren't cut by intermediaries.

### Versioning

- The API version is the path segment: `/_shibu/v1/`
  ([design 2.2.20](../shibu.md#2220-api-versioning-and-idempotency-keys)).
  Within a version, changes are additive only: new operations, new
  optional arguments, new fields in responses, new event types and new
  error codes. Clients must ignore fields they don't know.
- Every contract in [`extension-points.md`](extension-points.md) has its
  own version, written `<contract>/v<N>` (`model/v1`, `driver/v1`),
  with the same additive rule within a version. The `/sys` spec names
  the contract version each implementation conforms to
  ([`sys-spec.md`](sys-spec.md)).
- `GET /_shibu/version` returns the kernel's version and the API and
  contract versions it serves:
  `{"kernel": "1.0.0", "api": ["v1"], "contracts": {"model": ["v1"], "driver": ["v1"], …}}`.

## Choices this specification makes

The design leaves these shapes open, or calls its own names
illustrative. This specification picks one in each case. They are
choices, not decisions the design made, and each can be changed
without changing the model.

1. **RPC-style HTTP binding.** Every control operation is
   `POST /_shibu/v1/<operation>`, named as in the design, rather than a
   resource-per-URL REST layout. Namespace paths contain `/`, which
   makes resource URLs awkward, and the operation names are already the
   CLI's vocabulary. ([Transport](#transport))
2. **Reserved URL segments and the segment grammar.** Segments can't
   start with `_`, so the web proxy can use `/_shibu/`, `/_self/` and
   the like without colliding with a namespace.
   ([Names and paths](#names-and-paths))
3. **Relative paths** resolve against the caller's namespace, as the
   design's examples assume. ([Names and paths](#names-and-paths))
4. **Typed stable IDs** and the `id:` reference form.
   ([Stable IDs and references](#stable-ids-and-references))
5. **The error codes** and their HTTP statuses, including `not_found`
   for objects the caller can't see. ([Errors](#errors))
6. **Idempotency keys** are kept for at least 24 hours, scoped to
   caller and operation. ([Idempotency](#idempotency))
7. **Handle names.** A grant's handle in the grantee is named after the
   object's last segment unless `grant` passes `as`. The design shows
   handles (`# → lead/db`) but doesn't say how they are named.
   ([`kernel-api.md`, `grant`](kernel-api.md#grant))
8. **Ops and limits vocabularies** for each endpoint kind, including
   the `read` and `write` method groups for HTTP endpoints and the
   dimension names of allocator caps.
   ([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits))
9. **Budget windows** are UTC calendar days for `per_day`.
   ([`kernel-api.md`, Budgets](kernel-api.md#budgets))
10. **`create_endpoint` and `delete_endpoint`.** The design attaches
    endpoints to a VM's ports when the VM is created
    (`endpoints: {8080: svc/db}`). This specification keeps that, and
    adds two operations so an endpoint can be added to, or removed
    from, a running VM.
    ([`kernel-api.md`, Endpoints](kernel-api.md#endpoints))
11. **The `Shibu-Caller` assertion** is a JWS (ES256) with a 60-second
    lifetime, its claims as listed, and its keys at a JWKS endpoint.
    ([`web-proxy.md`, The caller assertion](web-proxy.md#the-caller-assertion))
12. **Acting as a namespace from outside** is the `Shibu-Act-As` header,
    honoured only at the front door.
    ([`web-proxy.md`, The front door](web-proxy.md#the-front-door))
13. **Kernel calls to components** (an allocator's `create` and
    `delete`, a reconciler's call on root's behalf) carry a caller assertion with `kernel: true`, so a
    component can tell the kernel from a caller.
    ([`web-proxy.md`, The caller assertion](web-proxy.md#the-caller-assertion))
14. **The metering API**, `meter_check` and `meter_charge`, through
    which a metered endpoint such as `/sys/model` enforces a budget
    across the whole grant chain and puts its usage in `cost()`. The
    design says the endpoint enforces the budget and that usage adds up
    along the chain, but not how the endpoint learns the chain.
    ([`extension-points.md`, Metering API](extension-points.md#metering-api))
15. **Secret references.** A caller names a secret to a component by
    path (`create_agent`'s `credential`, `/sys/cred`'s `secret_ref`,
    the `credential` limit on a `/sys/model` grant), and the component
    reads it through its own `read` grant. (`bound_reads` and
    kernel-issued bound grants were removed in task 1887.)
    ([`kernel-api.md`, Secrets](kernel-api.md#secrets))
16. **The VM `mcp` endpoint** is `<vm>/mcp`, on port 7070 of the VM,
    speaking MCP over streamable HTTP, with the five VM tools mapped to
    the ops `read`, `write` and the contract op `exec`, on an `http`
    endpoint.
    ([`agents.md`, VM `mcp`](agents.md#vm-mcp-contract-mcpv1))
17. **Application MCP servers** are named in `create_agent`'s `tools`,
    which the adapter adds to the CLI's strict MCP configuration. The
    design says such a server is "granted to the agent like any
    endpoint" but not how the harness learns of it.
    ([`agents.md`, Application MCP servers](agents.md#application-mcp-servers))
18. **The runner's wire form** (`launch`, `resume`, `stop` and
    `status`, called by a launch-grant holder, `capacity`, `adapters` and the event
    stream) and the runner's session reports back to the kernel.
    ([`extension-points.md`, `runner/v1`](extension-points.md#runnerv1))
19. **The driver contract's wire form** is gRPC, over a Unix socket or
    mTLS, with the messages listed; **the secret backend contract** is
    a Go interface, since its code runs inside the kernel.
    ([`extension-points.md`](extension-points.md#backend-driver-contract-driverv1))
20. **Every distribution component serves `/_contract` and `/_health`**,
    and conformance suites are images the kernel runs in a throwaway
    namespace during a staged swap.
    ([`extension-points.md`, Common to every distribution contract](extension-points.md#common-to-every-distribution-contract))
21. **The `/sys` spec's schema** beyond the design's example, including
    `rollback_window` (default 30 minutes) and the repo-mode fields.
    ([`sys-spec.md`](sys-spec.md))
22. **Secret rotation** keeps the previous value readable for a fixed
    overlap, the `/sys` spec's `secrets.overlap` (default 10 minutes),
    the same for every secret.
    ([`kernel-api.md`, `put_secret`](kernel-api.md#put_secret))
23. **The reconciler's document format**, and that it deletes objects
    missing from the document only when `prune: true`; and `batch`'s
    shape, on which it is built.
    ([`extension-points.md`, `reconciler/v1`](extension-points.md#reconcilerv1),
    [`kernel-api.md`, `batch`](kernel-api.md#batch))
24. **The `forge/v1` resource layout**, modelled on grain's `pkg/forge`
    and the health values it already uses.
    ([`extension-points.md`, `forge/v1`](extension-points.md#forgev1))
25. **`messages` sequence numbers and `wait`** for long-polling.
    ([`agents.md`, `messages`](agents.md#messages))
26. **`/sys/cred` is a component allocator.** A caller creates a
    credential-proxy or ingress-verifier endpoint in its own namespace
    with an ordinary `create` through a grant of `/sys/cred`, so the
    allocator contract's handles, grants and cleanup carry it. The
    design says only that such an endpoint is "created through
    `/sys/cred` in the caller's namespace".
    ([`extension-points.md`, `credential-proxy/v1`](extension-points.md#credential-proxyv1))
27. **The agent control MCP's endpoint and tool shapes**
    (`<agent>/agent-mcp`), and the exposure query's request and answer.
    ([`agents.md`, Agent control MCP](agents.md#agent-control-mcp),
    [`kernel-api.md`, `exposure`](kernel-api.md#exposure))
28. **An idle session.** When a harness's turn ends without `exit`, the
    agent stays `running` with its session `idle`, and the next message
    from its creator resumes it.
    ([`agents.md`, The harness contract](agents.md#the-harness-contract))
29. **Smaller additions**: `get` and `whoami`; the `sys_*` operations for reading the
    spec's history and status; the runner's `report_session`; the root
    allocators' names (`/vm-std`, `/vm-<class>`, `/volume`); the
    `/sys/conformance/<run>` namespace; the `pane/v1` bridge message
    envelope; the `requests` convention's resource layout, filled
    in from the design's `{want, for, lease, why}`; and a restriction
    the design doesn't state, that an agent's `exit` and `tools` answer
    only the agent itself, not its ancestors. (That a secret-`read`
    grant can't be made with `subtree: true` was one too; the design
    states it, with its reason, since 1886.)
30. **Shapes for the 1904 decisions** the design states without a wire
    form: the `person` claim's fields (`idp`, `sub`, `name`, or
    `anonymous: true`); a grant's `last_used`, kept to the hour, and
    `list_grants`' `unused_since`; `unused_grants` in `audit` and
    `cost`, and `charged: false` on a runner's report that isn't
    accepted; `messages`' `cap_bytes`; the session state `waiting` with
    `reason` and `until`; that `usage: "reported"` may be added but not
    dropped by a re-grant; and the `state/v1` Go interface, chosen on
    the kernel host rather than in the `/sys` spec.
    ([`web-proxy.md`](web-proxy.md#the-caller-assertion),
    [`kernel-api.md`](kernel-api.md#list_grants),
    [`agents.md`](agents.md#operations),
    [`extension-points.md`](extension-points.md#kernel-state-store-contract-statev1),
    [`sys-spec.md`](sys-spec.md#state-store))

## Open items

What the design leaves open on purpose, and what this specification
does until it is decided.

| Design item | What this specification does |
| --- | --- |
| [2.2.27](../shibu.md#2227-kernel-state-store) the kernel state store as a bootstrap root VM (`/sys/state`) | Not in v1, and no names are reserved. The store is embedded SQLite behind `state/v1`; a Postgres-compatible store, in a VM or not, can replace it without API changes ([`state/v1`](extension-points.md#kernel-state-store-contract-statev1)). |
| [F34](../shibu.md#f34-cleanup-leaks-when-a-component-allocator-is-down) writing off a leak | Not specified: the design marks it *suggested*. Leaks are visible in `watch`, `list` and `cost`, as decided. |
| [F35](../shibu.md#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted) periodic origin tests | The origin test is part of `driver/v1` conformance at admission and on every swap, as decided. Running it periodically is left to the kernel. |
| [2.4](../shibu.md#24-beyond-v1-native-port-endpoints) native `tcp` endpoints | Not in v1. The `tcp` endpoint kind and the `connect` op are reserved names. |
| [2.5](../shibu.md#25-beyond-v1-bearer-grants) bearer grants | Not in v1, and no names are reserved. |
| [2.7](../shibu.md#27-beyond-v1-policy) policy: grant conditions and deny predicates | Not in v1, and no names are reserved. Authorization is already decided per call, and grants are the only thing that allows, so policy can be added later as a subtraction in the web proxy's check. |
| [F33](../shibu.md#f33-a-component-allocator-can-misreport-usage) limiting which component allocators a subtree uses | Not in v1. Reported usage is marked as such in `cost` and never sets a limit, as decided; confining a subtree's allocators waits on policy. |

## Decided for v1 in task 1904

The design's fourteenth revision decided these for v1. They were open
items here until then; this is where each is now specified.

| Design item | Where it is specified |
| --- | --- |
| [6.8](../shibu.md#68-how-a-service-declares-its-operations) How a service declares named operations | No declaration mechanism. The web proxy enforces methods, `read`/`write`/`tunnel` and paths; service-level names are documentation in each contract, and contract ops (`exec`, `launch`, `fetch`, `push`) are opaque strings the kernel narrows and passes in the assertion ([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits)). |
| [6.9](../shibu.md#69-callers-with-no-vm) Callers with no VM | The `person` claim names the identity provider and subject of a front-door caller; an anonymous caller through `/sys/ingress` gets `person: {"anonymous": true}` with `/sys/ingress` as the caller ([`web-proxy.md`, The caller assertion](web-proxy.md#the-caller-assertion)). |
| [6.10](../shibu.md#610-per-vendor-rate-limits-on-shared-subscriptions) Per-vendor limits on shared subscriptions | Session state `waiting`, reason `vendor-limit`, with `until` when known; `out-of-quota` past the session's lease; no scheduler ([`runner/v1`](extension-points.md#runnerv1), [`agents.md`, The agent object](agents.md#the-agent-object)). |
| [6.11](../shibu.md#611-where-an-agents-messages-history-lives) Where `messages` history lives | Kept by the kernel with the agent object for the namespace's life, capped per agent (`cap_bytes`), oldest dropped first, all of it readable by any `read` holder ([`agents.md`, `messages`](agents.md#operations)). |
| [6.12](../shibu.md#612-session-isolation-inside-pool-runners) and [F26](../shibu.md#f26-the-shared-runner-is-a-high-value-target) Session isolation | `container` or better, reported in `capacity`; the `isolation` limit on a launch grant; neighbouring sessions in `exposure` only below the required level ([`runner/v1`](extension-points.md#runnerv1), [`kernel-api.md`, `exposure`](kernel-api.md#exposure)). |
| [6.13](../shibu.md#613-accepting-a-runners-usage-reports) Accepting a runner's usage reports | `usage: "reported"` on the launch grant; otherwise `charged: false` in `cost`; a silent runner's session stops at the end of its lease ([`runner/v1`](extension-points.md#runnerv1), [`kernel-api.md`, Cost records](kernel-api.md#cost-records)). |
| [F21](../shibu.md#f21-visibility-leaks-are-a-matter-of-policy-and-audit) Visibility leaks | `audit` and `exposure`, as specified; nothing else in v1. |
| [F22](../shibu.md#f22-cascading-revokes-can-be-expensive) Cascading revokes | The grant-chain index, an implementation requirement on the kernel and `state/v1` ([`revoke`](kernel-api.md#revoke)). No API change. |
| [F23](../shibu.md#f23-grant-sprawl-in-long-lived-namespaces) Grant sprawl | `last_used` on a grant, `list_grants`' `unused_since`, and `unused_grants` in `audit` and `cost`; no expiry beyond leases ([`list_grants`](kernel-api.md#list_grants)). |
| [F24](../shibu.md#f24-does-regrant-need-a-depth-limit) `regrant` depth | No limit; `regrant: false` by default. |
| [F32](../shibu.md#f32-when-idle-races-with-a-create-in-flight) Idle deletion races | The reaper re-checks idleness at the moment it deletes ([`reaper/v1`](extension-points.md#reaperv1)). |
| [F25](../shibu.md#f25-the-backend-carries-the-whole-identity-model) Backend identity | Decided earlier (1765): the origin test in [`driver/v1`](extension-points.md#conformance) conformance. |
