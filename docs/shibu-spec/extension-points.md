# Shibu extension points, v1

Every contract something implements to plug into Shibu. Each one names
who may implement it, how the kernel finds and calls it, what it must
do, and how it is tested.

| Contract | Implemented by | Chosen by | Called by |
| --- | --- | --- | --- |
| [`allocator/v1`](#component-allocators) | any namespace | the implementer; trusted by whoever uses its grants | the kernel |
| [Metering API](#metering-api) | the kernel, for any metered endpoint | the endpoint's owner | the endpoint |
| [`driver/v1`](#backend-driver-contract-driverv1) | a backend driver | root, in the `/sys` spec | the kernel |
| [`secret-backend/v1`](#secret-storage-backend-contract-secret-backendv1) | a secret storage backend, compiled into the kernel | root, in the `/sys` spec | the kernel |
| [`state/v1`](#kernel-state-store-contract-statev1) | the kernel state store, compiled into the kernel; embedded SQLite in v1 | the kernel host's configuration | the kernel |
| [`model/v1`](#modelv1) and the other [distribution contracts](#distribution-contracts) | a distribution component | root, in the `/sys` spec | callers, through the web proxy; the kernel, for a few calls |
| [`runner/v1`](#runnerv1) | any VM; `/sys/runner` in the distribution | the runner's operator; used by whoever holds `launch` on it | holders of `launch` on it (the `create_agent` helper, or anyone), through the web proxy |
| [VM `mcp`, `mcp/v1`](agents.md#vm-mcp-contract-mcpv1) | any VM; the agent base image in the distribution | the VM's owner | anyone granted ops on `<vm>/mcp`, through the web proxy |
| [Harness adapter](agents.md#adapter-manifest) | a runner's image | the runner's operator | the runner and the harness |

## Contents

1. [Component allocators](#component-allocators)
2. [Metering API](#metering-api)
3. [Backend driver contract, `driver/v1`](#backend-driver-contract-driverv1)
4. [Secret storage backend contract, `secret-backend/v1`](#secret-storage-backend-contract-secret-backendv1)
5. [Kernel state store contract, `state/v1`](#kernel-state-store-contract-statev1)
6. [Distribution contracts](#distribution-contracts)
   - [Common to every distribution contract](#common-to-every-distribution-contract)
   - [`model/v1`](#modelv1)
   - [`runner/v1`](#runnerv1)
   - [`egress/v1`](#egressv1)
   - [`ingress/v1`](#ingressv1)
   - [`git/v1`](#gitv1)
   - [`forge/v1`](#forgev1)
   - [`credential-proxy/v1`](#credential-proxyv1)
   - [`pkgcache/v1`](#pkgcachev1)
   - [`reconciler/v1`](#reconcilerv1)
   - [`ui/v1`](#uiv1)
   - [`warm-pool/v1`](#warm-poolv1)
   - [`reaper/v1`](#reaperv1)
   - [`agent-helper/v1`](#agent-helperv1)

---

## Component allocators

*Kernel: the contract, and the grants, accounting and cleanup around a
component allocator. The component's own code is the component's*
([design, Component allocators](../shibu.md#component-allocators)).

"Allocator" is a contract, not a kernel-only object. Any namespace can
implement one (a GPU pool, a cloud-account broker, a credits pool), and
grants work on it exactly as on a kernel allocator: narrowing, caps,
`reserved`, `regrant`, leases, and `revoke` with `kill`.

### Declaring one

A component makes a port of one of its VMs into an allocator with
[`create_endpoint`](kernel-api.md#endpoints), or with `kind: allocator`
on the port in the VM's spec, and a **declaration**:

```json
{
  "path": "/gpu/pool",
  "kind": "allocator",
  "target": {"vm": "/gpu/pool-vm", "port": 9000},
  "allocator": {
    "contract": "allocator/v1",
    "kind": {"name": "gpu-slice",
             "dimensions": [{"name": "gpu-slices", "unit": "slice"}, {"name": "gpu-mem", "unit": "bytes"}],
             "spec_schema": {"type": "object", "properties": {"model": {"enum": ["l4", "a100"]}}}},
    "capacity": {"gpu-slices": "16", "gpu-mem": "640Gi"}
  }
}
```

| Field | Meaning |
| --- | --- |
| `kind` | The one kind this allocator endpoint makes. A component that makes several declares an allocator endpoint per kind, so that granting one is granting that kind (there is no `kinds` limit). |
| `kind.dimensions` | What the kernel counts for caps and headroom. |
| `kind.spec_schema` | A JSON Schema for the kind's `spec`, which the kernel checks before calling the component. |
| `kind.adopts` | Optional: `vm` or `volume`, if the component may answer creates by handing over a kernel resource it owns ([Adopt](#adopt)). |

Every created resource's handle is an `http` endpoint the component
serves, at `/_alloc/v1/resources/<handle_id>/…`; it must answer at
least `GET` with the resource's status. A resource with nothing more to
serve still answers that.
| `capacity` | The root amounts. The implementing namespace is the root of the chain, and may change them with `create_endpoint` again, freely. |

### Creating through one

Callers use the ordinary [`create`](kernel-api.md#create). The spec
**must declare the amounts** it takes in every dimension, because the
kernel enforces caps only from what it can count itself:

```
create(allocator=/factory/gpu, target=/factory/ci/c17-1/gpu-0,
       spec={kind: gpu-slice, amounts: {gpu-slices: 1, gpu-mem: 40Gi}, model: l4})
```

The kernel checks every link of the chain against the declared
amounts, refuses before the component sees anything if a link is full,
records the handle (home, payer, grant), and only then calls the
component.

### What the kernel calls

HTTP on the allocator endpoint, `POST`, JSON, each request carrying a
caller assertion with `kernel: true`
([`web-proxy.md`](web-proxy.md#the-caller-assertion)). The component
must reject any call to `/_alloc/v1/` without one.

| Call | Request | Response |
| --- | --- | --- |
| `/_alloc/v1/create` | `handle_id`, `kind`, `spec`, `amounts`, `home` (`{ns_id, ns_path}`), `grant_id`, `chain`, `on_behalf_of` (the caller's namespace) | `{"ref": "<component's own id>", "detail": {...}}`, or `{"adopt": {"object_id": "…"}}` to hand over a kernel resource it already owns ([Adopt](#adopt)) |
| `/_alloc/v1/delete` | `handle_id`, `ref` | `{}`, or `{"gone": true}` if it never existed |
| `/_alloc/v1/usage` | `handle_ids` and/or `grant_ids`, `since` | `{"usage": [{"handle_id"?, "grant_id"?, "amounts": {...}, "usd"?: "…"}]}`, which the kernel shows in `get` and `cost` |
| `/_alloc/v1/list` | `grant_id` or `all: true` | `{"resources": [{"handle_id", "ref"}]}` |

- **Idempotent on `handle_id`.** The kernel retries `create` and
  `delete` with the same `handle_id`, and a component must treat a
  repeat as the same call.
- **Create failures.** A non-2xx answer to `create` fails the caller's
  `create` with `component_unavailable` (5xx, timeout) or
  `invalid_argument` (4xx, with the component's message). The charge is
  released.
- **Delete** is called on `delete`, on `revoke(kill=true)`, on an
  expired lease, and when the handle's home is deleted. It is retried with backoff. A handle whose `delete` has kept
  failing for an hour is a **leak**: it stays charged to the grantor,
  counts against the grantor's headroom, and is shown by `watch`
  (`allocator.leak`), `list` and `cost`, until a retry succeeds
  ([design F34](../shibu.md#f34-cleanup-leaks-when-a-component-allocator-is-down)).
- **Usage** is polled by the kernel, at least every five minutes, and
  shown in `cost` as **reported**. It never sets or loosens a limit
  ([design F33](../shibu.md#f33-a-component-allocator-can-misreport-usage)).
- **Reconciliation.** The kernel's record of handles is the truth about
  what should exist. The kernel calls `list` when the component comes
  back after failing, and periodically. A `ref` the kernel doesn't know
  should be deleted by the component; a handle the component doesn't
  list is recorded as gone.

**Kernel resources a component uses** to fulfil a create come from the
kernel grants it already holds and are charged to it like anything
else. Nothing is counted twice. A component allocator can never mint
kernel quota, and nothing but root changes the kernel's own allocators
or `/sys/model`'s budget.

**Trust** is the grantee's decision, made when it uses the grant. In
v1 no ancestor can confine a subtree to allocators implemented in a
given path; that is a requirement on policy beyond v1
([design 2.7](../shibu.md#27-beyond-v1-policy),
[F33](../shibu.md#f33-a-component-allocator-can-misreport-usage)).
There is no conformance gate for component allocators.

### Adopt

A component may answer `create` with a kernel resource it already owns,
instead of making something of its own: a VM, or a writable volume
(often a clone), homed in the component's own namespace or its
subtree. The kernel **adopts** it: it re-homes the resource into the
create's `target`, makes the grant the create named its payer, remaps a
VM's origin in the web proxy, and injects no credential. The kernel
checks that the component owns the resource and that it is of the kind
and shape asked for; otherwise the caller's create fails with
`adopt_refused`. The full rules, errors, idempotency and audit record
are in [`kernel-api.md`, `adopt`](kernel-api.md#adopt).

- An allocator whose creates the component may answer by adopting declares
  `"adopts": "vm"` or `"adopts": "volume"`, and its `spec_schema` must
  include that kernel kind's spec fields
  ([`kernel-api.md`, `create`](kernel-api.md#create)), so the kernel
  can compare them.
- An adopted handle is no longer the component's. The kernel deletes
  the resource itself and never calls `delete` for it, and `list`
  doesn't report it.
- Its kernel units stay on the kernel grant the component made it
  through (above), so a component pays, in kernel units, for every
  resource it has handed out until that resource is deleted.

---

## Metering API

*Kernel.* A **metered endpoint** enforces a budget on the grants made
on it ([design 2.2.9](../shibu.md#229-time-based-budgets)). `/sys/model`
is one, and the runner meters time and turns through the same API. Any
namespace may meter its own endpoints the same way.

The budget is a limit on the grant
([`kernel-api.md`, Budgets](kernel-api.md#budgets)), so the endpoint
needs the whole chain, not just the grant in the caller assertion. The
kernel keeps the sums; the endpoint checks and charges through two
operations, callable only by the namespace that owns the endpoint
named by the grant's chain:

| Operation | Arguments | Returns |
| --- | --- | --- |
| `meter_check` | `grant_id` | `{"allowed": true, "links": [{"link": "gr_…", "budget": {...}, "used": {...}, "headroom": {...}}], "exhausted": null}` |
| `meter_charge` | `grant_id`, `amounts` (`usd`, `tokens`, `hours`, `turns`, or the endpoint's own units), `idempotency_key`, `detail` (free-form, for the audit line) | the same shape, after the charge |

- An endpoint calls `meter_check` before serving a call, and refuses
  with `429` `budget_exhausted` if `allowed` is false, naming
  `exhausted.link`. It calls `meter_charge` after, with what the call
  actually cost.
- A charge is recorded against every link of the chain, and shown in
  `cost` as **reported**, with the endpoint as reporter
  ([`kernel-api.md`, Cost records](kernel-api.md#cost-records)). A
  runner's charges count against budgets only when its launch grant
  carries `usage: "reported"`; otherwise they are shown and not charged
  ([`runner/v1`](#runnerv1)).
- A charge that takes a link over its budget is still recorded (the
  call already happened). The next `meter_check` refuses, and the
  kernel emits `budget.exhausted`.
- The kernel caches headroom per link; `meter_check` is meant to be
  called on every request.

---

## Backend driver contract, `driver/v1`

*Kernel. A driver is a plug-in the kernel loads, chosen in the `/sys`
spec* ([design, Backend drivers](../shibu.md#backend-drivers)).

VMs and volumes come from backend drivers, and the kernel allocators'
roots come from their capacity. v1 ships one driver, docker + granule
(`pkg/granule` today). k8s, GCE and AWS are planned against the same
contract.

### Loading

A driver runs as its own process, from the image its `/sys` spec entry
names ([`sys-spec.md`, Drivers](sys-spec.md#drivers)), and serves the
gRPC service below on a Unix socket the kernel gives it, or on a TCP
address with mutual TLS when it runs on another host. The kernel is its
only client. Several drivers run at once, and each placement class maps
to exactly one.

### Service

```proto
syntax = "proto3";
package shibu.driver.v1;

service Driver {
  // What the driver is and offers. Called at load and on every staged swap.
  rpc Describe(DescribeRequest) returns (DescribeResponse);
  // What it can host now; the kernel's root allocators follow it.
  rpc Capacity(CapacityRequest) returns (CapacityResponse);

  rpc CreateVM(CreateVMRequest) returns (VM);      // creates and boots
  rpc StartVM(VMRef) returns (VM);                 // restart after stop or crash
  rpc StopVM(StopVMRequest) returns (VM);
  rpc DeleteVM(VMRef) returns (Empty);
  rpc GetVM(VMRef) returns (VM);
  rpc ListVMs(ListVMsRequest) returns (ListVMsResponse);

  rpc CreateVolume(CreateVolumeRequest) returns (Volume);
  rpc DeleteVolume(VolumeRef) returns (Empty);
  rpc AttachVolume(AttachRequest) returns (Empty);
  rpc DetachVolume(AttachRequest) returns (Empty);
  rpc Snapshot(SnapshotRequest) returns (SnapshotInfo);
  rpc Clone(CloneRequest) returns (Volume);
  rpc DeleteSnapshot(SnapshotRef) returns (Empty);
  rpc ImportImage(ImportImageRequest) returns (Volume);  // OCI ref or volume → read-only image

  // Narrows what the VM's interface may send beyond the web proxy. In v1
  // everything but the proxy is dropped, except for /sys/egress's own VMs
  // (`network: outside`); beyond v1, tcp egress.
  rpc SetEgress(SetEgressRequest) returns (Empty);

  // Status changes, preemption notices and capacity changes, in order.
  rpc WatchEvents(WatchEventsRequest) returns (stream Event);
}

message DescribeResponse {
  string name = 1;                     // "granule"
  string version = 2;
  repeated string contracts = 3;       // ["driver/v1"]
  repeated PlacementClass classes = 4; // the classes it offers
  bool supports_snapshots = 5;
  bool supports_preemption_notice = 6;
}

message PlacementClass { string name = 1; bool preemptible = 2; repeated string regions = 3; }

message CapacityResponse { repeated Pool pools = 1; }
message Pool {
  string name = 1;                     // a host, or a cloud project's quota
  string class = 2;
  map<string, string> total = 3;       // {"cpu": "64", "ram": "256Gi", "disk": "4Ti", "vms": "200"}
  map<string, string> free = 4;
}

message CreateVMRequest {
  string vm_id = 1;                    // the kernel's stable ID; idempotency key
  string class = 2;
  Shape shape = 3;
  string image_volume_id = 4;
  repeated Mount mounts = 5;
  map<string, string> env = 6;         // includes SHIBU_ENDPOINT, HTTP(S)_PROXY
  repeated int32 ports = 7;            // ports the web proxy will reach
  string proxy_address = 8;            // where this VM's traffic must go
}
message Shape { string cpu = 1; string ram = 2; string disk = 3; }
message Mount { string volume_id = 1; string path = 2; bool read_only = 3; }

message VM {
  string vm_id = 1;
  string status = 2;                   // running | restarting | exited | preempted
  int32 exit_code = 3;
  string exit_reason = 4;              // completed | failed | preempted | oom | deleted
  int32 restart_count = 5;
  Origin origin = 6;                   // set before the VM can send its first packet
  string pool = 7;
}
// The VM's network origin: what the web proxy sees on every packet from
// it, and what nothing else can present.
message Origin { string address = 1; string interface = 2; }

message StopVMRequest { string vm_id = 1; string reason = 2; }  // reason: deleted | stopped
message Event {
  string vm_id = 1;
  string type = 2;                     // status | preemption-notice | capacity
  VM vm = 3;
  CapacityResponse capacity = 4;
  int64 seq = 5;
}
// … the remaining request and response messages carry the IDs and fields
// their names say.
```

The kernel, not the driver, decides restarts: on an `exited` event it
calls `StartVM` if the VM's restart policy says so, and counts restarts.

### What a driver must guarantee

| Area | Guarantee |
| --- | --- |
| **Origin** | Each VM has its own interface. No shared bridge where one VM could claim another's address. Source checks on every packet. An origin is never reused while the kernel still maps it; the driver reports a new VM's origin before the VM can send ([design, Identity](../shibu.md#identity)). |
| **Proxy path** | Every packet a VM sends goes to `proxy_address` or is dropped. `shibu.internal` resolves to it inside the VM. |
| **VM boundary** | A VM is an isolation boundary: a plain container is not enough. A k8s driver needs Kata or a gVisor-style runtime ([design, Backend drivers](../shibu.md#backend-drivers)). |
| **Events** | Every status change, in order, at least once. Preemption is reported as `preempted`, not `failed`. |
| **Capacity** | `Capacity` and capacity events reflect what it can host; the kernel turns them into root allocators held by `/`. |
| **Idempotency** | Every create is idempotent on the kernel's ID. |
| **No migration** | A VM stays on the driver that created it. |

### Conformance

Each driver contract version has a conformance suite, which runs at
admission and again on every staged swap
([`sys-spec.md`, Staged swap](sys-spec.md#staged-swap)). It includes:

- the VM and volume lifecycle, events and restart behaviour;
- **the origin test**: a VM tries to send as another VM, on the same
  driver and across drivers, and to reach the web proxy other than
  through its own interface. Every attempt must fail
  ([design F35](../shibu.md#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted));
- egress: a VM tries to reach the internet without the proxy;
- address reuse: an origin is not handed to a new VM while the kernel
  still maps it.

A driver that fails is **not admitted**: the `/sys` spec naming it
doesn't apply, and no placement class maps to it.

---

## Secret storage backend contract, `secret-backend/v1`

*Kernel.* Where the secret store keeps its encrypted values is
pluggable: built-in, HashiCorp Vault, or GCP Secret Manager
([design 2.2.24](../shibu.md#2224-secret-store)). The store's API, its
access rule and its audit are the kernel's. A backend is chosen in the
`/sys` spec, but **its code runs inside the kernel**, because anything
that can read every secret is part of the trusted computing base.

It is therefore a Go interface, not a network contract:

```go
// Package secretbackend is the kernel's interface to where secret values
// are kept. A Backend stores and returns bytes; it never decides who may
// read them, and never logs them.
package secretbackend

type Ref struct {
	SecretID string // the kernel's stable ID, sec_…
	Version  int
}

type Backend interface {
	// Put stores a new version. It must be durable when it returns.
	Put(ctx context.Context, ref Ref, value []byte) error
	// Get returns a stored version. ErrNotFound if it was never stored or
	// has been destroyed.
	Get(ctx context.Context, ref Ref) ([]byte, error)
	// Destroy removes one version for good (a previous version whose
	// overlap has ended).
	Destroy(ctx context.Context, ref Ref) error
	// DestroyAll removes every version of a secret (the secret was deleted).
	DestroyAll(ctx context.Context, secretID string) error
	// Health reports whether the backend can serve Get and Put now.
	Health(ctx context.Context) error
}

var ErrNotFound = errors.New("secretbackend: not found")
```

- **Built-in** keeps values in the kernel's own database, encrypted at
  rest with envelope encryption: a data key per secret, wrapped by a
  key from a KMS or a local key file named in the `/sys` spec.
- **Vault** and **GCP Secret Manager** store each version as a version
  of one external secret, named from the stable ID, never the path.
- **Changing backend** is a `sys_plugins` call with a new `secrets`
  section ([`sys-spec.md`](sys-spec.md#operations)). The kernel copies
  every current version, and every previous one still in its overlap,
  to the new backend, checks
  each one with `Get`, switches, and then destroys them in the old one.
  If any copy fails, nothing changes.
- **Conformance** is a Go test package every backend runs: round trip,
  durability across restart, destroy, not-found, and a check that no
  value appears in logs.

---

## Kernel state store contract, `state/v1`

*Kernel. Decided for v1 (task 1904); the store as a VM is Open*
([design 2.2.27](../shibu.md#2227-kernel-state-store)).

The kernel keeps all of its state in one store: objects, grants and
their chains, leases, labels, agent objects and their `messages`, the
meter's charges, the change log behind `watch` and `audit`, and, with
the built-in secret backend, the encrypted secret values. **v1 ships one
implementation: an embedded SQLite database inside the kernel
process**, on the kernel host's disk. There is no separate database
service and nothing to start before the kernel.

The store is a contract so that a Postgres-compatible store can replace
SQLite later without any change to the kernel API, the web proxy or a
component. Like the secret storage backend, **its code runs inside the
kernel**, because whatever holds the kernel's state can rewrite every
grant. It is a Go interface, not a network contract:

```go
// Package state is the kernel's interface to where it keeps its own
// state. A Store holds rows and a change log; it never decides who may
// do what, and nothing outside the kernel sees which Store is in use.
package state

type Store interface {
	// Begin starts a transaction. Transactions are serializable across
	// every row they touch: batch, the grant-chain walk and cascading
	// revokes rely on it.
	Begin(ctx context.Context) (Tx, error)
	// Changes streams the committed change log from a position, in
	// commit order. watch and audit are served from it.
	Changes(ctx context.Context, from Position) (ChangeStream, error)
	// Backup writes a consistent copy of the whole store.
	Backup(ctx context.Context, w io.Writer) error
	// Health reports whether the store can serve reads and commits now.
	Health(ctx context.Context) error
}

type Tx interface {
	Get(table string, key []byte) ([]byte, error)
	Put(table string, key, value []byte) error
	Delete(table string, key []byte) error
	// Scan reads a key range of a table or of one of its indexes, in
	// key order. The grant-chain index (grants by issued_from) is one.
	Scan(table, index string, from, to []byte) (Iterator, error)
	// Commit is durable when it returns, and appends to the change log.
	Commit() error
	Rollback() error
}

var ErrNotFound = errors.New("state: not found")
var ErrConflict = errors.New("state: serialization conflict") // retried by the kernel
```

- **Indexes the kernel needs.** Grants by the grant they were issued
  from, so a cascading revoke walks an index rather than scanning
  grants ([design F22](../shibu.md#f22-cascading-revokes-can-be-expensive));
  grants by grantee and by last use, for `list_grants`
  ([`kernel-api.md`, `list_grants`](kernel-api.md#list_grants));
  objects by path and by label.
- **SQLite** runs in WAL mode with one writer. `Backup` is SQLite's
  online backup. A deployment backs the store up by copying that file
  off the kernel host.
- **Choosing the store** is the kernel's startup configuration on the
  kernel host, not a section of the `/sys` spec, because the spec is
  itself kept in the store ([`sys-spec.md`, State store](sys-spec.md#state-store)).
  Changing store is a migration done with the kernel stopped: `Backup`
  from the old store, load into the new, start the kernel on it. No
  API changes.
- **Conformance** is a Go test package every store runs: serializable
  transactions under concurrent writers (two creates racing for the
  last headroom on a chain, one wins), durability across a crash after
  `Commit`, change-log order and resumption from a position, index
  scans, and a round trip through `Backup`.

**Future option, Open: the store as a bootstrap root VM.** Recorded,
not decided ([design 2.2.27](../shibu.md#2227-kernel-state-store)). A
Postgres-compatible store could run as a VM, for example `/sys/state`,
that the backend driver starts directly at boot rather than through an
allocator. It would be pinned (no `delete`, `move` or `adopt`, and no
lease), visible in the tree with `watch`, `audit` and `cost`, backed up
with volume snapshots, and would need a recovery path that doesn't
depend on the store. Its costs are bootstrap ordering and circular
failure. Nothing in this specification depends on it, and no names are
reserved for it.

Services' own databases are not this store: they are ordinary VMs
behind ordinary endpoints. No endpoint or grant reaches the kernel's
store.

---

## Distribution contracts

*Standard distribution* ([design 2.3](../shibu.md#23-the-standard-distribution-and-the-sys-spec)).
The kernel depends on the distribution only through these named,
versioned contracts, never through an implementation. Root chooses the
implementation of each in the `/sys` spec ([`sys-spec.md`](sys-spec.md)).

| Contract | Endpoint | Class | Holds credentials |
| --- | --- | --- | --- |
| [`model/v1`](#modelv1) | `/sys/model` | bootstrap | yes: the keys their owners grant it `read` on |
| [`runner/v1`](#runnerv1) | `/sys/runner`, and `<vm>/runner` on any VM | bootstrap (`/sys/runner`) | yes: the logins and keys their owners grant it `read` on |
| [`egress/v1`](#egressv1) | `/sys/egress` | bootstrap | no |
| [`ingress/v1`](#ingressv1) | `/sys/ingress` | optional | no |
| [`git/v1`](#gitv1) | `/sys/git` | optional | GitHub-backed: yes; hosted: no |
| [`forge/v1`](#forgev1) | `/sys/forge` | optional | GitHub-backed: yes; hosted: no |
| [`credential-proxy/v1`](#credential-proxyv1) | `/sys/cred` | optional | yes: the secrets their owners grant it `read` on |
| [`pkgcache/v1`](#pkgcachev1) | `/sys/pkgcache` | optional | no |
| [`reconciler/v1`](#reconcilerv1) | `/sys/reconciler`, and any namespace that runs the image | bootstrap (`/sys/reconciler`) | no; it acts as `/` for `/sys`, and through a `control` grant elsewhere |
| [`ui/v1`](#uiv1) | `/sys/ui` | optional | no |
| [`warm-pool/v1`](#warm-poolv1) | `/sys/pool`, and any namespace that runs the image | optional | no |
| [`reaper/v1`](#reaperv1) | `/sys/reaper`, and any namespace that runs the image | optional | no; it holds `write` on the `control` endpoints of the subtrees it reaps |
| [`agent-helper/v1`](#agent-helperv1) | a library, `shibu agent create`, and `/sys/agents` | optional (`/sys/agents`) | no; it acts as its caller |

### Common to every distribution contract

- **Where it runs.** In its `/sys/<name>` namespace, on VMs the
  reconciler creates from the `/sys` spec entry, with the spec's shape,
  and a grant on `/sys/egress` for the entry's `egress` hosts.
- **How it is reached.** Callers reach `/sys/<name>` through the web
  proxy. Root grants each endpoint to `/` with `subtree: true`, narrowed
  per endpoint: `/sys/model` with no budget, so it can be called only
  through a budget someone grants; `/sys/git`, `/sys/forge` and
  `/sys/cred` usable only through a grant on a scope of them.
- **Caller assertion.** It must verify the `Shibu-Caller` assertion on
  every request and refuse requests without a valid one
  ([`web-proxy.md`](web-proxy.md#the-caller-assertion)). Its policy is
  applied to what the assertion says, never to anything else in the
  request.
- **Metadata.** It serves `GET /_contract` →
  `{"contract": "model/v1", "implementation": "shibu/model", "version": "1.4.2"}`
  and `GET /_health` → `200` when it can serve.
- **Credentials.** It holds a credential only through `read` grants:
  the ones its spec entry lists, from root, and the ones other owners
  issue it for their own secrets
  ([`kernel-api.md`, Secrets](kernel-api.md#secrets)). With any, its
  namespace is marked credential-holding, and `exposure` shows who can
  reach it. Root's naming it in the `/sys` spec, and each owner's grant,
  is that owner's decision to trust it with those keys
  ([design 2.2.7](../shibu.md#227-credentials-only-behind-proxy-endpoints)).
- **Secret references.** A component told to use a secret by
  reference (a runner's `credential`, `/sys/model`'s `credential`
  limit, `/sys/cred`'s `secret_ref`) uses it only on behalf of the
  namespaces its `read` grant's `for` limit names, by default the
  granter's subtree: the `launch` or `create` caller, or for
  `/sys/model` a granter on the request's grant chain, from the caller
  assertion. Otherwise it refuses with `403`. This is what stops one
  holder of the component's endpoint spending another's key, now that
  there is no `use` grant ([design 2.2.24](../shibu.md#2224-secret-store)).
- **Conformance.** Each contract version publishes a conformance suite
  as an image, `shibu/conformance-<contract>:<version>`. During a
  staged swap, the kernel runs it in a throwaway namespace,
  `/sys/conformance/<run>`, given test grants on the new implementation
  and test secrets, against the new implementation before the endpoint
  is re-pointed. The suite exits zero to pass, and writes a report to
  its namespace.
- **Replacing and turning off.** Only root, through the `/sys` spec.
  Bootstrap components can be replaced but not turned off. Turning an
  optional one off revokes its root grant, so its endpoints stop
  answering.

### `model/v1`

*Bootstrap* ([design 2.2.23](../shibu.md#2223-model-access-and-budgets)).
A root-level, metered model proxy that routes by vendor and model name.
It is one way for a runner to reach models without holding a key, not
the definition of model access, which is a launch grant on a runner
([`runner/v1`](#runnerv1)). A runner that uses it is given the
agent's grant on `/sys/model`, and the session calls through it. It
can't carry subscription or OAuth logins: redirecting a CLI's base URL
takes it out of subscription mode.

**Protocols.** A caller speaks a vendor's own protocol, under a path
that names it. An implementation must serve these, and may serve more:

| Protocol name (adapter manifest) | Path under the endpoint | Upstream |
| --- | --- | --- |
| `anthropic-messages` | `anthropic/v1/messages`, `anthropic/v1/messages/count_tokens` | Anthropic Messages API |
| `openai-responses` | `openai/v1/responses` | OpenAI Responses API |
| `openai-chat` | `openai/v1/chat/completions` | OpenAI Chat Completions |
| `gemini` | `gemini/v1beta/models/{model}:generateContent`, `…:streamGenerateContent` | Gemini API |

So a harness with `ANTHROPIC_BASE_URL=http://shibu.internal/_self/model/anthropic`
calls `…/v1/messages` exactly as it would call the vendor. Streaming
responses pass through as the vendor sends them.

**Grant limits** it enforces:

| Limit | Meaning |
| --- | --- |
| `budget` | budgets in `usd` or `tokens` ([`kernel-api.md`, Budgets](kernel-api.md#budgets)) |
| `vendors` | allowed vendors: `anthropic`, `openai`, `google`, … |
| `models` | allowed model names, as globs |
| `credential` | per vendor, a reference to the secret whose key it attaches for calls through this grant: `{"anthropic": "/users/alice/secrets/anthropic"}`. `/sys/model` must hold its own `read` grant on it, from the secret's owner. Narrowing can only remove entries. Without one, it uses root's key for that vendor, if its spec entry reads one |

It is an `http` endpoint; a model call is a `POST`, so the op is
`write` (or `POST`). Grants are made by granting a `/sys/model` handle
onward, narrowed; root holds `/sys/model` and grants budgets down.

**Per request**, it must:

1. verify the caller assertion and take `grant.id` from it; a request
   with no grant (by the tree, which only root has) is served with no
   budget check;
2. refuse with `403` if the vendor or model is outside the grant's
   limits;
3. `meter_check` the grant, and refuse with `429` `budget_exhausted`
   if any link is spent ([Metering API](#metering-api));
4. take the secret the grant's `credential` limit names for the vendor
   (from the assertion), check the reference rule above, `read` it
   through its own `read` grant, and
   attach it as the adapter manifest's `inject` says, replacing
   whatever key the caller sent; refuse with `403` if it holds no
   `read` grant on it;
5. forward to the vendor, through its own `/sys/egress` grant;
6. `meter_charge` the grant with the call's cost in `usd` and
   `tokens`, from the vendor's reported usage and the implementation's
   price table.

A refusal uses the vendor protocol's own error shape, with the Shibu
code in it (for Anthropic, `{"type": "error", "error": {"type":
"rate_limit_error", "message": "…", "shibu_code": "budget_exhausted"}}`),
so an unmodified CLI reports it sensibly.

**Usage.** `GET <grant handle>/_usage` returns the grant's usage and
budget, from the kernel's meter. Its charges are metered outside any
runner. An agent's own usage, whichever way its runner reaches models,
is `GET <agent>/usage` ([`agents.md`, Standard endpoints](agents.md#standard-endpoints)).

**Conformance** includes: each protocol round trip against a stub
vendor; that the caller's key is replaced and never forwarded; budget
refusal at every link of a three-link chain; vendor and model
narrowing; that a grant whose `credential` names a key never gets
another grant's key; streaming pass-through.

### `runner/v1`

*Kernel contract; `/sys/runner` is the bootstrap set's pool runner*
([design, Runners](../shibu.md#runners)). Any VM may serve a `runner`
endpoint, `<vm>/runner`. The runner launches every harness: there is
no other launch path.

- A **same-vm runner** is the VM's own, usually one or a few sessions,
  all in the VM's namespace, using the VM's origin. The agent base
  image serves one.
- A **pool runner** is a few VMs serving many agents from many
  namespaces, one isolated session each (a container or better), each
  acting as its agent through a session binding. `/sys/runner` is
  one.

Adapters ship in the runner's image and are installed by its operator.

**What it serves:**

| Call | Caller | Request → response |
| --- | --- | --- |
| `GET /adapters` | anyone who can reach the endpoint | the [adapter manifests](agents.md#adapter-manifest) it can run |
| `GET /capacity` | anyone who can reach the endpoint | `{free_sessions, total_sessions, isolation: "container" \| "microvm" \| "vm"}` |
| `POST /launch` | a holder of `launch` | `{agent_path, model, tools: [{url, ops}], credential, config, dry_run}` → `{session_id, state}`, or `{refused: "<reason>"}` |
| `POST /sessions/{id}/resume` | a holder of `launch`, or the kernel | `{session_ref}` → `{state}` |
| `POST /sessions/{id}/stop` | a holder of `launch`, or the kernel (on `stop`, `delete` or a revoked launch grant) | `{reason}` → `{state: "ended"}` |
| `GET /sessions/{id}` | a holder of `read` | `{state, reason?, until?, session_ref, turns, hours, usage, last_error}` |
| `GET /events` | a holder of `read`, or the kernel | a stream of session events: `state` (including `waiting` with reason `vendor-limit`), `exit`, `usage` |
| `POST /logins` | a holder of `launch` | `{secret, adapter, credential}` → `{url}`: runs the manifest's `oauth` or `subscription` `flow`, and writes the result to `secret` with `put_secret`, through a `write` grant on it from its owner |

A runner endpoint is an `http` endpoint speaking this contract; `launch`
is a contract op ([`kernel-api.md`, Ops and limits](kernel-api.md#ops-and-limits)).
The caller assertion on `launch` carries the caller's launch grant and
its `limits`: `adapters`, `models`, `budget`, `sessions`, `isolation`,
`usage` and its lease.
The web proxy has already checked the grant's chain and lease. The
runner then:

1. reads the agent object at `agent_path` (`GET <agent>/agent`; a
   runner named in an agent's `runs_in` may read it) for its adapter,
   `runs_in` and `tools`, and checks that `runs_in` is this runner;
2. for a pool runner, asks the kernel for the **session binding** with
   `POST <agent>/agent/bind`, which returns the binding and the launch
   grant the agent's namespace holds, with its limits
   ([`agents.md`, The agent object](agents.md#the-agent-object)). A
   same-vm runner gets no binding: its sessions call out with the VM's
   origin;
3. applies the agent's launch grant's limits, the caller's, and its own
   policy, refusing (`{refused: "isolation"}`) if either grant's
   `isolation` is stronger than the isolation it provides, which maps the caller to one of its configured accounts when
   the launch names no `credential`; with a `credential`, reads it
   through its own `read` grant under the reference rule
   ([Common to every distribution contract](#common-to-every-distribution-contract));
4. starts the session, or refuses rather than half-starting.

The runner must keep the binding in its forwarder, outside the
session's container (or microVM or VM), and add `Shibu-Session-Binding` to the
session's requests to Shibu
([`web-proxy.md`, Identity](web-proxy.md#identity)).

`model_route` says how the session reaches `model`: `sys-model` (with
the agent's grant on `/sys/model`) or `runner` (the runner's own key,
login or model). Which one is the runner's configuration, reported in
its `/_contract` metadata.

**What it calls back** (kernel operations only a runner may call, for
its own sessions):

| Operation | Arguments | Meaning |
| --- | --- | --- |
| `report_session` | `session_id`, `state` (`running`, `idle`, `waiting`, `resuming`, `ended`), `session_ref`, `detail` | Keeps the agent object's `session` current. `waiting` carries `detail.reason: "vendor-limit"` and, when the vendor gave one, `detail.until`, the reset time. `ended` with `detail.exit` carries the exit the harness called, `harness-failed`, or `out-of-quota`. |
| `meter_charge` | the session's launch grant, `usd`, `tokens`, `hours` and `turns` | Reports a session's usage, marked **reported** with the runner as reporter ([Metering API](#metering-api)). It is **charged** to the launching agent's payer along the grant chain only if the launch grant carries `limits: {usage: "reported"}`; otherwise it appears in `cost` marked **reported** and is not charged ([design 6.13](../shibu.md#613-accepting-a-runners-usage-reports)). |

**What it must do:**

- run each session isolated from the others at container level or
  better (`container`, `microvm` or `vm`; an OS user alone is not
  enough), holding only that session's credential, report that level
  in `capacity`, and refuse a launch whose grant requires more
  ([design 6.12](../shibu.md#612-session-isolation-inside-pool-runners));
- launch, resume and stop the CLI as its manifest says, passing the
  `model` launch parameter, with built-in tools disabled and a strict
  MCP configuration listing only the agent control MCP and the
  session's `tools`
  ([`agents.md`, The harness contract](agents.md#the-harness-contract));
- read the agent's `job` and `messages` and post to them as the agent;
- apply the launch grant's limits, with `meter_check` before each
  turn, ending the session with `out-of-quota` when a budget is spent;
- on restart, relaunch each live session with `resume` in the same
  runner, or report it `ended` with `harness-failed`;
- report a vendor's own rate limit or usage cap as a pause: state
  `waiting`, reason `vendor-limit`, with the reset time when known; keep
  the agent running and retry after the reset; and, if the limit lasts
  beyond the session's lease, end the session with `out-of-quota`
  ([design 6.10](../shibu.md#610-per-vendor-rate-limits-on-shared-subscriptions)).
  The runner doesn't schedule sessions per subscription; how many
  agents may share one is its own policy.

**What the kernel guarantees whatever the runner does:** a binding
names one agent and one session, and stops working when the session
ends; a session whose runner stops reporting is stopped at the end of
its lease, when the kernel ends its binding and calls `stop`; the
runner can't mint one, widen its grants, or act outside the
grants its sessions' agents hold
([design, Trust](../shibu.md#trust)).

**Conformance**, `shibu/conformance-runner:1`: `adapters` and
`capacity` shapes; a session through each listed adapter against a stub
vendor, with the `model` parameter reaching the CLI; that no built-in
tool runs (a tool call that would touch the runner VM's filesystem must
fail); that every tool call lands in a `tools` VM; refusal of a launch
outside the grant's `adapters`, `models` and `sessions`; turn and hour
budgets; resume after a runner restart, in the same runner; usage
reports per session; a vendor limit reported as `waiting` with reason
`vendor-limit`, then `out-of-quota` past the lease; refusal of a launch
whose `isolation` is stronger than the runner's; and that one session
can't read another's credential, transcript or files, or use another's
binding, at the isolation the runner reports.

### `git/v1`

*Optional* ([design, `/sys/git` and `/sys/forge`](../shibu.md#sysgit-and-sysforge)).
Git smart HTTP, with per-caller policy on the caller assertion. Agents
see one endpoint, `https://<shibu>/sys/git/<owner>/<repo>.git`, however
many implementations serve it.

**Wire.** The standard smart-HTTP paths under
`<owner>/<repo>.git/`: `info/refs?service=git-upload-pack`,
`git-upload-pack`, `info/refs?service=git-receive-pack`,
`git-receive-pack`. Any git client works, through `shibu.internal`.

**Ops and limits** on a grant of `/sys/git`, or of a folder or repo
under it (`/sys/git/acme/`, `/sys/git/acme/app`):

| Op | Maps to |
| --- | --- |
| `fetch` | `GET info/refs?service=git-upload-pack`, `POST git-upload-pack` |
| `push` | `GET info/refs?service=git-receive-pack`, `POST git-receive-pack` |

| Limit | Meaning |
| --- | --- |
| `repos` | globs of `owner/repo` |
| `refs` | globs of refs a `push` may update (`refs/heads/grain/*`) |
| `force` | whether a push may be non-fast-forward (default false) |

The implementation enforces `refs` and `force` itself, from the
assertion, and may apply more policy of its own (only `/factory/mq` may
push `main`). A refusal is a smart-HTTP error the git client prints.

**Implementations.** GitHub-backed (holding the GitHub App key through
a standing `read` grant, and credential-holding) or Shibu-hosted (bare
repos on a volume, no credential). Both may run at once, each serving
the repo patterns its spec entry lists. A hosted implementation should
also serve `POST /_repos` (`{owner, name}`) and `DELETE
/_repos/<owner>/<name>` for callers holding `write` on `/sys/git/<owner>/`.

**Conformance** includes: clone, fetch and push through the proxy;
`refs` and `force` enforcement; that a caller with no grant on a repo
gets `not_found`, not an empty repo; for GitHub-backed, that the App
key is never sent to a client.

### `forge/v1`

*Optional* ([design, `/sys/git` and `/sys/forge`](../shibu.md#sysgit-and-sysforge)).
Pull requests, reviews, checks and merges on repos `/sys/git` serves,
modelled on grain's `pkg/forge`. A repo with git but no forge still
works for agents; it just has no PR flow.

**Resources**, JSON over HTTP under
`/sys/forge/repos/<owner>/<repo>/`:

| Method and path | Op | Request → response |
| --- | --- | --- |
| `GET pulls?state=&head=&base=` | `read` | pull requests |
| `POST pulls` | `write` | `{title, body, head, base, draft}` → pull request |
| `GET pulls/{n}` | `read` | `{number, title, body, head: {ref, sha}, base: {ref, sha}, state: open \| closed \| merged, draft, author, url}` |
| `PATCH pulls/{n}` | `write` | `{title?, body?, state?: closed, base?}` |
| `GET pulls/{n}/health` | `read` | `{health: clean \| pending \| conflicted \| failing \| merged \| closed \| unknown, head_sha, mergeable: clean \| conflicted \| behind \| unknown, checks: [...]}` |
| `POST pulls/{n}/merge` | `merge` | `{method: merge \| squash \| rebase, expected_head_sha, message?}` → `{merged: true, sha}`, or `409` with `{reason: head-moved \| conflicted \| checks-failing \| not-allowed}` |
| `GET`, `POST pulls/{n}/reviews` | `read`, `write` | `{verdict: approve \| request-changes \| comment, body, comments: [{path, line, body}]}` |
| `GET`, `POST pulls/{n}/comments` | `read`, `write` | `{body, path?, line?, reply_to?}` |
| `GET commits/{sha}/checks` | `read` | check runs |
| `POST commits/{sha}/checks` | `checks` | `{name, status: queued \| in_progress \| completed, conclusion?: success \| failure \| neutral \| cancelled \| skipped \| timed_out, summary, details_url}` → `{id, …}` |
| `PATCH checks/{id}` | `checks` | the same fields |
| `GET events?since=` | `read` | an SSE stream: `pull.opened`, `pull.synchronized`, `pull.closed`, `pull.merged`, `review.submitted`, `comment.created`, `check.updated` |

The health values are grain's `forge.Health` values, with the same
meaning ([`../data-model.md`](../data-model.md)).

**Ops** on a grant of `/sys/forge` or a scope under it
(`/sys/forge/repos/acme/`): `read`, `write`, `checks`, `merge`. Each
maps to the rows above. **Limits:** `repos` (globs). The author of
every pull request, review, comment and check is recorded as the
caller's namespace from the assertion, beside whatever identity the
backing forge uses.

**Implementations.** GitHub-backed (grain's `pkg/forge`, `pkg/github`
and `pkg/checkpost` today, holding the GitHub App key through a
standing `read` grant) or minimal hosted, for repos served by hosted
git, with no issues and no Actions.

**Conformance** includes: the pull-request lifecycle; a merge refused
when `expected_head_sha` is stale; health values for clean,
conflicting, failing and pending pull requests; ops enforcement per
row; the event stream resuming with `since`.

### `credential-proxy/v1`

*Optional* ([design F9](../shibu.md#f9-secrets-have-no-shibu-home)).
`/sys/cred` attaches a stored credential to requests on the way out, and
verifies inbound webhook signatures, so the key never enters the
caller's VM.

**`/sys/cred` is a component allocator.** A caller creates a proxy
endpoint in its own namespace with an ordinary `create` through a grant
of `/sys/cred` ([Component allocators](#component-allocators)):

```
create(allocator=/factory/svc/cred-proxy, target=/factory/svc/gh,
       spec={kind: credential-proxy, amounts: {endpoints: 1},
             upstream: "https://api.github.com", secret_ref: /factory/svc/secrets/gh-app,
             inject: github-app, params: {installation_id: "123"}})

create(allocator=/factory/lead/cred-verifier, target=/factory/lead/gh-hook-verify,
       spec={kind: ingress-verifier, amounts: {endpoints: 1},
             secret_ref: /factory/lead/secrets/webhook, verify: hmac-sha256,
             params: {header: X-Hub-Signature-256, prefix: "sha256="}})
```

- `secret_ref` is a reference. `/sys/cred` must hold a `read` grant on
  it from its owner, and uses it only under the reference rule
  ([Common to every distribution contract](#common-to-every-distribution-contract)):
  the create's caller must be that owner or in its subtree. The caller
  never gets `read`.
- **Kinds:** `credential-proxy` with `inject` one of `header`, `basic`,
  `github-app`, `gcp-sa`, `registry`; and `ingress-verifier` with
  `verify: hmac-sha256` in v1. `github-app` and `gcp-sa` mint
  short-lived upstream tokens per request rather than sending the
  stored key. `registry` is a streaming proxy for image pushes and
  pulls.
- **Limits.** `/sys/cred` declares one allocator endpoint per kind
  (`/sys/cred/proxy` and `/sys/cred/verifier`), each with an
  `endpoints` dimension; a grant of the first may carry the contract
  limit `upstreams` (URL globs).
- **Using a proxy endpoint.** Requests to
  `http://shibu.internal/<target>/<rest>` reach `/sys/cred`, which reads
  the secret through its `read` grant, attaches it, and forwards to
  `<upstream>/<rest>`. Access to the credential is a grant on that
  endpoint, narrowed by paths and methods like any `http` endpoint.
- **Verifying.** `/sys/ingress` calls an `ingress-verifier` handle's
  `POST /_verify` with the inbound request (method, path, headers, and
  body as base64), and gets `{"allow": true}` or
  `{"allow": false, "reason": "…"}`
  ([`ingress/v1`](#ingressv1)).
- **Rotation** needs nothing here: the next read gets the new version.
- **Turning `/sys/cred` off** leaves existing proxy endpoints
  unreachable, as for any revoked grant.

A credential-injecting proxy for a non-HTTP protocol is not this
contract: it is a component behind a tunnel, with its own `read` grant
([design 2.2.26](../shibu.md#2226-non-http-services-tunnels-over-http-endpoints)).

**Conformance** includes: each `inject` kind against a stub upstream;
that the stored key never appears in a response or in what the caller
sent; that a `secret_ref` from a caller outside the secret's
granter's subtree is refused; HMAC verification, including a replayed and a tampered body.

### `pkgcache/v1`

*Optional.* A read-through cache for package registries, holding no
credential. Paths under `/sys/pkgcache/`:

| Ecosystem | Path | Client setting |
| --- | --- | --- |
| Go modules | `go/` | `GOPROXY=http://shibu.internal/sys/pkgcache/go` |
| npm | `npm/` | `registry=http://shibu.internal/sys/pkgcache/npm/` |
| PyPI | `pypi/simple/` | `index-url` |
| crates.io | `crates/` | a sparse registry |
| Maven | `maven/` | a mirror |
| OCI images | `oci/` | registry v2 pull-through, read-only |

It serves `GET` only. It must return byte-identical content to the
upstream, check what each ecosystem lets it check (Go's sumdb, npm
integrity), and never serve a package it couldn't verify. Conformance
fetches a fixed set of packages through it and compares them.

### `reconciler/v1`

*Bootstrap* ([design 2.2.8](../shibu.md#228-orchestration-surface),
[The `/sys` spec](../shibu.md#the-sys-spec)). Declarative reconciling
is not a kernel operation: the reconciler diffs a document against
`list` and makes the changes with one [`batch`](kernel-api.md#batch).
`/sys/reconciler` applies the `/sys` spec as root
([`sys-spec.md`](sys-spec.md)); any namespace may run the same image on
its own subtree, holding `write` on that subtree's `control` endpoint
(by the tree, or a grant).

**A subtree document:**

```yaml
apiVersion: shibu/v1
kind: Subtree
root: /factory
prune: false
namespaces:
  - path: svc
  - path: ci
grants:
  - {object: /vm-std, to: /factory, ops: [create], limits: {cap: {cpu: "256", ram: 1Ti}}, regrant: true, as: vm-std}
  - {object: /sys/egress, to: /factory/svc, ops: [tunnel], limits: {hosts: ["*:443"]}, as: egress}
  - {object: /factory/svc/images, to: /factory, ops: [read], subtree: true}
objects:
  - {kind: volume, path: svc/db-data, allocator: svc/volume-alloc, spec: {size: 200Gi}}
  - kind: vm
    path: svc/db-vm
    allocator: svc/vm-std
    spec: {shape: {cpu: 4, ram: 16Gi}, image: svc/images/postgres, restart: always,
           volumes: {/var/lib/postgresql: svc/db-data}, endpoints: {"8080": db}}
agents:                                   # made with the create_agent helper after the batch
  - path: lead
    payer: vm-std
    spec: {adapter: claude-code, model: claude-sonnet-4, credential: /users/alice/secrets/anthropic,
           job: "…", budget: [{unit: usd, per_day: "300"}], regrant_launch: true}
```

| Call | Request → response |
| --- | --- |
| `POST apply` | `{document, dry_run}` → `{applied, created, changed, deleted}`, or the plan |
| `GET status` | `{watching: [root], last_applied: {source, at}, last_error}` |
| `POST proposals` | `/sys` in repo mode: `{spec, message}` → `{pull_request_url}` or `{branch}` |

- **Scope.** `root` must be the reconciler's own subtree, or one it
  holds `write` on through `control`. Every path in the document is
  relative to `root` unless absolute.
- **Identity.** Namespaces and objects are matched by path. Grants are
  matched by `(object, to, as)`.
- **Diff.** Fields that can't change in place (a VM's shape or image, a
  volume's size downwards, an agent's adapter) fail the apply with
  `409` unless the entry has `replace: true`, which deletes and
  re-creates it.
- **Prune.** With `prune: true`, objects and grants under `root` that
  the reconciler created and the document doesn't name are deleted or
  revoked. Without it, nothing is deleted. Objects created by agents
  below `root` are never pruned unless they are named.
- **Atomic.** Every control-plane change is one `batch`. Agents in
  `agents` are made after it, with the `create_agent` helper
  ([`agent-helper/v1`](#agent-helperv1)); a launch failure shows as that
  entry's error, not as a failed apply.
- **Dry run** is the batch's dry run.
- **Secrets.** Values are never in a document. A document may grant
  `read` on a secret like any grant.
- **Repo mode.** With `mode: repo` it watches a `/sys/git` repo and
  applies the file at `path` on `branch` whenever it changes, passing
  the commit as the spec's `source`; `POST apply` is then refused with
  `409` and the repo's address, and the UI sends edits to
  `POST proposals`, which opens a pull request through `/sys/forge` or,
  without a forge, pushes a branch.

### `ui/v1`

*Optional* ([design 2.6](../shibu.md#26-the-ui-and-the-sysui-conventions)).

- **Where.** `/sys/ui` serves the UI's assets at the front door, under
  `/_ui/`, and `https://<shibu>/` redirects there. Its root grant is
  its only grant, and it has no `read` grant, so it isn't
  credential-holding.
- **No authority of its own.** The UI runs in the person's browser and
  calls the API as the namespace the person is acting as
  (`Shibu-Act-As`), with the front door's session.
- **It must render** the conventions in
  [`ui-conventions.md`](ui-conventions.md): agents with their status
  labels, `messages` by type, `artifacts`, requests, and panes framed
  as [`ui-conventions.md`](ui-conventions.md#agent-served-panes)
  requires.
- **It must build credential and config forms from adapter manifests**,
  send their values only to `put_secret`, and never accept a credential
  anywhere else. For an `oauth` or `subscription` credential it runs the
  manifest's `flow` itself, in the person's browser, and writes the
  result with `put_secret` as the person, or asks a runner to with
  `POST <runner>/logins` ([`runner/v1`](#runnerv1)).
- **Main navigation.** It reads the trusted pane list from its own
  `/sys` spec entry's `nav` (`{"nav": ["/grain/lead", …]}`, passed in
  its config) and lets only those namespaces' panes appear in main
  navigation.
- **Conformance is CLI parity.** Anything a UI can do, the CLI can do.
  The suite drives the UI (in a headless browser) and `shibu` through
  the same scenarios, as the same principal, and requires the same
  calls in the audit log and the same resulting state
  ([`cli.md`](cli.md)). It also checks pane isolation: origins,
  `frame-ancestors`, the bridge's allowed messages, and that no
  credential field appears in a pane.

### `warm-pool/v1`

*Optional* ([design 2.2.2](../shibu.md#222-caps-reservations-and-adoption)).
A warm pool is a [component allocator](#component-allocators) that
keeps pre-booted VMs ready and hands them out with [`adopt`](#adopt).
The kernel has no warm-pool kind and relies on nothing here beyond
`allocator/v1`; this contract fixes what a pool does so that one image,
`shibu/warm-pool`, serves everyone, with a conformance suite like any
distribution contract.

- **Where.** Any namespace may run it in its own subtree, as `factory/ci`
  does ([design 4.2.6](../shibu.md#426-ci)), and root may run a shared
  instance at `/sys/pool` through the `/sys` spec. Either way it holds a
  **VM allocator grant**, and its waiting VMs live in its own namespace,
  charged to that grant. It holds no credential and no `read` grant.
- **Configuration.** A list of pools, each a `shape`, an `image` and a
  target `size` N, all in the placement class of its VM grant. It declares one allocator kind, `vm`,
  with `"adopts": "vm"`, the kernel `vm` dimensions (`cpu`, `ram`,
  `disk`, `vms`), and a `capacity` no larger than its VM grant's cap.
- **Create.** A create whose spec matches a pool's shape and image is
  answered with `{"adopt": …}` naming a waiting VM. On a miss
  (no waiting VM, or no pool for that spec) it cold-boots one through
  its VM grant in its own namespace and adopts that, so a miss is slower
  but never refused for lack of a waiting VM. A create it can't serve
  in its VM grant's headroom fails as `quota_exceeded` would.
- **Refill.** Every take starts a boot to bring the pool back to N,
  within its VM grant's headroom. A VM handed out never comes back:
  delete destroys it.
- **Stats.** It serves `GET /stats` on its allocator endpoint, per pool:
  `{shape, image, size, waiting, booting, hits, misses,
  boot_seconds_p50, boot_seconds_p95, refill_failures}`.

**The same pattern** covers other pre-made resources, with the same
image configured differently or a component of one's own:

- **pre-cloned volumes**: a pool holding a volume allocator grant keeps
  N clones of a warm snapshot, itself a read-only volume (a repo
  checkout with caches), and answers
  `kind: volume` creates by adopting one;
- **pre-warmed agent VMs**: a pool of `agent-base` VMs, adopted by a
  `create` through the pool's grant, for the build and tool VMs agents use
  ([design 4.2.5](../shibu.md#425-worker-agents-and-their-sandboxes)).
  The `create_agent` helper still charges a VM it creates for an agent to an ordinary
  VM allocator or grant ([`agents.md`](agents.md)).

**Conformance** includes: a hit is adopted with the requested shape and
the target's origin; a miss cold-boots and is adopted; a pool refills
after a take; `GET /stats` counts hits and misses; and an answer naming
a VM outside the pool's namespace is refused by the kernel
(`adopt_refused`).

### `reaper/v1`

*Optional* ([design 2.2.6](../shibu.md#226-leases-and-ttls)). The
kernel deletes a namespace only on `delete` or when its lease expires.
"Delete it once every VM and agent in it has exited" is a policy, and
the reaper is the standard place to run it, for a creator that would
rather not watch its own children. It uses only `watch` and `delete`,
and the kernel relies on nothing from it.

- **Where.** Any namespace may run it in its own subtree, and root may
  run a shared one as `/sys/reaper`. It needs `watch` and `delete` on
  each subtree it reaps: by the tree, or a grant on that subtree's
  `control` endpoint with `ops: ["write"]` (`watch` is `read`, and
  `delete` `write`).
- **What it reaps.** A namespace labelled
  `reaper.shibu.dev/reap: when-idle`, in a subtree it watches. Nothing
  else.
- **Idle.** A namespace is idle when it has held at least one VM or
  agent, and every VM and agent in its subtree has exited: none is
  running or `restarting`, and no create for the subtree is waiting in
  an admission queue. Volumes and endpoints don't count. The label
  `reaper.shibu.dev/after: <duration>` adds a settle time: the
  namespace must stay idle that long.
- **Delete.** When a namespace becomes idle, the reaper **re-checks
  idleness at the moment it deletes**, with a fresh `list` of the
  subtree rather than what it saw on the stream, and calls `delete`
  only if it is still idle
  ([design F32](../shibu.md#f32-when-idle-races-with-a-create-in-flight)). A create racing that `delete` fails with `deleting`,
  as any create into a deleting namespace does; the reaper never keeps
  anything first, so what should survive goes in a deposit area.
- **Stats.** `GET /status` returns `{watching: [path], reaped: N,
  last: [{path, at}]}`.

**Conformance** includes: a namespace that never held a VM is not
reaped; one whose last VM exits is; a `restarting` VM and a queued
create keep a namespace from being reaped; a create that lands between
idleness and the delete keeps it; `reaper.shibu.dev/after` is
honoured; and an unlabelled namespace is never touched.

### `egress/v1`

*Bootstrap* ([design 2.2.13](../shibu.md#2213-egress-policy)). The only
way out of Shibu. `/sys/egress` is a forward proxy on an `http`
endpoint, and its VMs are the only ones with a route outside
(`network: outside` in the `/sys` spec). There is no egress policy,
egress object or egress allocator in the kernel: outside access is a
grant on this endpoint, and no grant means none.

- **Reaching it.** VMs send outside traffic to `HTTP_PROXY`
  (`http://shibu.internal:3128`), and the web proxy turns it into a
  request on `/sys/egress` through the caller's handle on it
  ([`web-proxy.md`, Egress](web-proxy.md#egress)).
- **Ops.** `tunnel` for `CONNECT host:port` (HTTPS and anything else
  over TCP); `read` and `write` for plain-HTTP forward requests, by
  method.
- **Limits.** `hosts`, a list of `host:port` globs (`*.npmjs.org:443`,
  `github.com:443`, `*:443`), narrowed as a list: a grantee's globs must
  each be covered by the granter's. `max_connections` and `bandwidth`
  as for any tunnel.
- **Per request** it verifies the caller assertion, checks the
  destination against `hosts`, resolves the name itself, refuses
  private and link-local addresses, and connects. A refusal is `403`
  `egress_denied`, and is reported to the kernel for the audit log.
- **Grants.** Root holds it by the tree and grants it down with
  `regrant: true`; the `/sys` spec's `egress.grant` is that grant. Each
  `/sys` component's `egress` hosts are a grant from root.
- **Revoking** a grant closes every connection open through it.

**Conformance** includes: `hosts` narrowing at three links; refusal of
a host outside `hosts` and of a namespace with no grant; that private
addresses are refused even when a glob matches; connections closed on
revoke.

### `ingress/v1`

*Optional* ([design 2.2.12](../shibu.md#2212-pre-approved-publish)).
Public exposure. There is no public-endpoint object or allocator in the
kernel: publishing an `http` endpoint is granting it **to**
`/sys/ingress`, and unpublishing is revoking that grant
([`web-proxy.md`, Public exposure](web-proxy.md#public-exposure)).

- **Routes.** Each grant it receives is a route at
  `https://ingress.<shibu>/<handle name>`. It watches its own namespace
  for `grant.created` and `grant.ended` to keep routes current.
- **Audience.** From the grant's labels:
  `ingress.shibu.dev/audience: anyone`, or a comma-separated list of
  people and groups, which it checks with an identity-aware sign-in of
  its own (an IAP-style reconciler of its configuration, OIDC in the
  default implementation). A grant without the label serves no one.
- **Verifier.** `ingress.shibu.dev/verifier: <path>` names an
  `ingress-verifier` endpoint of `/sys/cred` to call before forwarding
  ([`credential-proxy/v1`](#credential-proxyv1)); the granter also
  grants `/sys/ingress` `POST` on it.
- **Forwarding.** An allowed request is forwarded through the route's
  grant, so its ops and `paths` bound what the outside can do. The
  endpoint sees a caller assertion with `/sys/ingress` as the caller,
  `via: "ingress"`, and the route, the audience match and the verifier.
  An anonymous caller (audience `anyone`) is marked so, with
  `person: {"anonymous": true}`
  ([`web-proxy.md`, The caller assertion](web-proxy.md#the-caller-assertion)).
- **Status.** `GET /routes` lists routes visible to the caller: `{handle,
  url, endpoint, audience, verifier, granter}`.

**Conformance** includes: a route appears on grant and disappears on
revoke; `anyone`, a named audience and a missing label; a verifier deny;
a request outside the grant's ops or `paths` refused.

### `agent-helper/v1`

*Optional as a service; the library and the CLI ship with the
distribution* ([design, `create_agent`](../shibu.md#create_agent)).
`create_agent` is defined in terms of kernel operations and
`runner/v1`, and adds nothing to the kernel: a
[`batch`](kernel-api.md#batch) making the agent namespace (with
`create_namespace`'s `agent`), any VM, and its grants, including the
launch grant; then the runner's `launch`; then, if the launch fails,
`delete` of the namespace. Its request and errors are in
[`agents.md`, `create_agent`](agents.md#create_agent).

- **As a library** and as `shibu agent create`, it runs as the caller.
- **As `/sys/agents`**, it serves `POST /create` and acts as the
  caller: a front-door caller's session, or, for a namespace, through a
  grant of that namespace's `control` endpoint with `write`, given to
  `/sys/agents` for the purpose. It holds nothing else.

**Conformance** includes: the batch it sends for each placement; that a
refused launch leaves no namespace behind; and that every call it makes
is one a caller could make itself.
