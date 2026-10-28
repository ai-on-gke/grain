# The `/sys` spec, v1

*Kernel: the plug-ins it loads (backend drivers and the secret storage
backend), set with the root-only `sys_plugins`, and the driver swap.
Standard distribution: the reconciler, which applies the rest of the
spec with [`batch`](kernel-api.md#batch) and runs component swaps,
and the components the spec names*
([design 2.3](../shibu.md#23-the-standard-distribution-and-the-sys-spec)).

`/sys` has one declarative source, the `/sys` spec. It says which
implementation of each distribution contract runs, which backend
drivers and which secret storage backend the kernel loads, which
secrets each component may read, and which outside hosts each may
reach. There is no `apply` in the kernel: the standard-distribution
reconciler ([`reconciler/v1`](extension-points.md#reconcilerv1)) is
the same component that reconciles any subtree, run by root on `/sys`.
It is a bootstrap component.

## Contents

1. [Operations](#operations)
2. [Schema](#schema)
3. [Secrets backend](#secrets-backend)
4. [State store](#state-store)
5. [Drivers](#drivers)
6. [Components](#components)
7. [What applying does](#what-applying-does)
8. [Staged swap](#staged-swap)
9. [UI mode and repo mode](#ui-mode-and-repo-mode)
10. [The default spec](#the-default-spec)

---

## Operations

All are root-only: the caller must be `/`.

**Kernel**, one operation, for what only the kernel can do:

| Operation | Arguments | Returns |
| --- | --- | --- |
| `sys_plugins` | `drivers`, `classes`, `secrets` (the spec's sections of those names), `dry_run` | the plug-ins loaded, and `{"swaps": [...]}` for drivers being replaced |

**The reconciler**, on its endpoint `/sys/reconciler`, which root holds
by the tree and nobody else is granted:

| Call | Request → response |
| --- | --- |
| `POST apply` | `{spec, dry_run}` (with `kind: Sys`) → a plan, then `{"version": "sys_…", "swaps": [...]}` |
| `GET spec?version=` | the spec (default: the one applied) |
| `GET history` | versions: `{version, applied_at, applied_by, source, summary}` |
| `POST rollback` | `{version}` → the same as `apply` of that version's spec |
| `GET status` | each component, driver, the secrets backend and the state store (read-only): implementation, version, state (`running`, `swapping`, `draining`, `failed`), conformance result, and the old implementation still warm, if any |

In repo mode `POST apply` and `POST rollback` are refused with `409`
and the repo's address ([UI mode and repo mode](#ui-mode-and-repo-mode)).

---

## Schema

```yaml
apiVersion: shibu/v1
kind: Sys
source: {commit: 3f2a9c1, repo: /sys/git/acme/shibu-config}   # set by the reconciler; optional
secrets:
  backend: builtin                         # builtin | vault | gcp-secret-manager
  builtin: {kek: {kms: "gcp-kms://projects/p/locations/l/keyRings/r/cryptoKeys/k"}}
  overlap: 10m                             # how long a secret's previous value stays readable after a write
drivers:
  granule:
    driver: shibu/driver-granule:1.0.0
    contract: driver/v1
    endpoint: unix:///run/shibu/drivers/granule.sock
    hosts: [host-1, host-2]
    classes: [standard, spot]
    prices: {cpu_hour: "0.03", ram_gib_hour: "0.004"}   # optional; enables usd budgets on its allocators
  # gce: {driver: shibu/driver-gce:0.3.0, contract: driver/v1, project: my-proj,
  #       reads: [/sys/secrets/gce-sa], classes: [gpu]}
classes:                                   # placement class → driver; each class maps to one driver
  standard: granule
  spot: granule
components:
  model:
    image: shibu/model:1.4.2
    contract: model/v1
    shape: {cpu: 2, ram: 4Gi}
    replicas: 2
    egress: [api.anthropic.com:443, api.openai.com:443, generativelanguage.googleapis.com:443]
    reads: []                              # key owners grant /sys/model read on their own keys
    config: {prices: {...}}
  runner:
    image: shibu/runner:2.0.1
    contract: runner/v1
    adapters: [claude-code, codex, gemini]
    egress: [vendors-of-adapters]
    reads: []                              # owners grant /sys/runner read on the logins it should use
    config: {model_route: sys-model, isolation: container, sessions: 200}
    launch: {to: /, adapters: [claude-code, codex, gemini]}   # root's launch grant, granted down from there
  git:
    contract: git/v1
    implementations:
      - {image: shibu/git-github:0.9.0, repos: ["bwsalmon/*"], egress: [github.com:443],
         reads: [/sys/secrets/gh-app-key]}
      - {image: shibu/git-hosted:0.2.0, repos: ["scratch/*", "internal/*"], volume: 200Gi}
  forge:
    image: shibu/forge-github:0.4.0
    contract: forge/v1
    egress: [api.github.com:443]
    reads: [/sys/secrets/gh-app-key]
  egress:
    image: shibu/egress:1.0.0
    contract: egress/v1
    network: outside                       # the only component with a route out
    grant: {to: /, regrant: true, hosts: ["*:443", "*:80"]}   # root's grant on /sys/egress, granted down from there
  ingress:
    image: shibu/ingress:1.0.0
    contract: ingress/v1
    config: {host: ingress.shibu.example.com, signin: {oidc: {issuer: "https://accounts.google.com"}}}
  cred:
    image: shibu/cred:0.5.0
    contract: credential-proxy/v1
    egress: ["*:443"]
    reads: []                              # owners grant /sys/cred read on what it should attach
  pkgcache: {image: shibu/pkgcache:1.1.0, contract: pkgcache/v1, egress: ["*:443"], volume: 500Gi}
  agents: {image: shibu/agents:1.0.0, contract: agent-helper/v1}   # optional; create_agent as a service
  pool:                                    # optional; namespaces may also run shibu/warm-pool themselves
    image: shibu/warm-pool:1.0.0
    contract: warm-pool/v1
    vm_grant: {from: /vm-std, cap: {cpu: "64", ram: "256Gi"}}
    config: {pools: [...]}                 # shape, image, class and size of each pool
  ui:
    image: shibu/ui:1.0.0
    contract: ui/v1
    nav: [/grain/lead, /grain/mq]
  reconciler:                              # bootstrap: it applies this spec
    image: shibu/reconciler:1.0.0
    contract: reconciler/v1
    mode: ui                               # ui | repo
rollback_window: 30m
```

---

## Secrets backend

`secrets.backend` chooses the [secret storage
backend](extension-points.md#secret-storage-backend-contract-secret-backendv1),
and the key of the same name holds its settings:

| Backend | Settings |
| --- | --- |
| `builtin` | `kek`: `{kms: <KMS key URI>}` or `{file: <path on the kernel host>}` |
| `vault` | `address`, `mount`, `auth` (`{kubernetes: {role}}` or `{approle: {role_id, secret_id_file}}`) |
| `gcp-secret-manager` | `project`, and the kernel's own service account |

`secrets.overlap` (default `10m`, `0` for none) is how long the store
keeps a secret's previous value readable after `put_secret` writes a
new one. It is the same for every secret, and not an argument of any
operation ([`kernel-api.md`, `put_secret`](kernel-api.md#put_secret)).

A backend's own credentials live outside the store, on the kernel host,
because the store can't hold the key that opens it.

---

## State store

The kernel's own state store
([`state/v1`](extension-points.md#kernel-state-store-contract-statev1),
[design 2.2.27](../shibu.md#2227-kernel-state-store)) is the one
kernel plug-in the `/sys` spec does **not** choose. The spec, its
history and everything `sys_plugins` loads are kept in the store, so
the kernel has to open the store before it can read the spec. Which
store it opens is the kernel's startup configuration on the kernel
host:

```yaml
# kernel host configuration, not part of the /sys spec
state:
  store: sqlite                            # v1 ships only sqlite
  sqlite: {path: /var/lib/shibu/state.db}
```

The reconciler's `GET status` reports the store's implementation,
version and health, read-only. There is no `state` section in the
`/sys` spec and no operation that changes the store at run time;
moving to another store is a migration with the kernel stopped
([`state/v1`](extension-points.md#kernel-state-store-contract-statev1)).

---

## Drivers

Each entry under `drivers` loads one [backend
driver](extension-points.md#backend-driver-contract-driverv1):

| Field | Meaning |
| --- | --- |
| `driver` | the driver's image and version |
| `contract` | the driver contract version it conforms to |
| `endpoint` | where it serves: `unix://…`, or `https://…` with mutual TLS |
| `classes` | the placement classes it offers here |
| `reads` | secrets it may read (a cloud driver's service account), as `read` grants from root |
| `prices` | optional prices per resource-hour, so its allocators can carry `usd` budgets |
| anything else | passed to the driver's `Describe` as its configuration (`hosts`, `project`) |

`classes` at the top level maps each placement class to exactly one
driver.

Each driver's capacity becomes root allocators held by `/`: `/vm-std`
for the class `standard`, `/vm-<class>` for any other class (`/vm-spot`,
`/vm-gpu`), and `/volume` for volumes (images and snapshots included),
so root can grant down from them
([design, Backend drivers](../shibu.md#backend-drivers)). A kernel VM
allocator offers exactly one class; which class a grantee may use is
which allocator it is granted. There are no other kernel allocators.

---

## Components

Each entry under `components` names one distribution contract.

| Field | Meaning |
| --- | --- |
| `image` | the implementation's image and version, or `repo: <git ref>` for one built from a repo |
| `contract` | the contract version it conforms to |
| `shape`, `replicas` | its VMs |
| `egress` | `host:port` globs. The reconciler grants the component's namespace `/sys/egress` with these `hosts`, and nothing else gives it outside access: least privilege for a component that may hold keys ([design, Trust](../shibu.md#trust)). `vendors-of-adapters` expands to the hosts the runner's adapters declare. |
| `network` | `egress` only: `outside` gives its VMs the one route out of Shibu ([`web-proxy.md`, Egress](web-proxy.md#egress)). |
| `grant` | `egress` only: root's grant on `/sys/egress`, from which outside access is granted down. |
| `reads` | root's own secrets it may read, issued as `read` grants by the reconciler. Any other owner grants a component `read` on its own secrets directly, as it would any namespace; `exposure` shows every holder. |
| `volume` | a volume for its state, charged to `/` |
| `implementations` | instead of `image`, for a contract served by several at once (only `git` in v1): each with its own `repos` patterns and the fields above |
| `config` | passed to the implementation as-is |
| `adapters` | `runner` only: which of its adapters to enable |
| `launch` | `runner` only: the launch grant root holds on `/sys/runner`, with its limits, from which model access is granted down ([design 2.2.23](../shibu.md#2223-model-access-and-budgets)). `config.model_route` says whether it reaches models through `/sys/model` (`sys-model`) or its own keys (`runner`) |
| `vm_grant` | `pool` only: the VM allocator grant root gives `/sys/pool`, which pays for its waiting VMs and its cold boots ([`warm-pool/v1`](extension-points.md#warm-poolv1)) |
| `nav` | `ui` only: namespaces trusted to place panes in the UI's main navigation ([`ui-conventions.md`](ui-conventions.md#agent-served-panes)) |
| `mode` | `reconciler` only: `ui` or `repo` ([UI mode and repo mode](#ui-mode-and-repo-mode)). |
| `off` | an optional component may be written `<name>: off`. Bootstrap components (`model`, `runner`, `egress`, `reconciler`) can't. |

Every component holding a `read` grant is marked credential-holding,
and `list` and `exposure` show it so. Naming it here is root's decision
to trust it with root's keys; granting it `read` on one's own is each
owner's.

---

## What applying does

The reconciler applies a spec in these steps, as root:

1. **Validates** the spec: schema, contract versions the kernel knows,
   bootstrap components present, every class mapped to a driver, every
   `reads` path an existing secret.
2. **Diffs** it against the applied version and against `list` of
   `/sys`. A dry run stops here and returns the diff, with a dry-run
   `batch` and a dry-run `sys_plugins` for the charges and refusals.
3. **Plug-ins.** If `drivers`, `classes` or `secrets` changed, calls
   `sys_plugins`; the kernel runs a driver's swap itself.
4. For each changed component, runs the [staged swap](#staged-swap).
   Components that didn't change aren't touched.
5. **One `batch`** for everything else: namespaces, VMs, endpoints,
   the `/sys/egress` grant for each component's `egress`, the `read`
   grants for `reads`, the root grants that make each endpoint
   reachable, and revokes for entries that are gone. Turning an
   optional component off revokes its root grant.
6. **Records** the new version in its history, with its source, and
   emits `sys.applied` through the kernel (the batch's audit record
   carries the version).

If any swap is refused, the whole apply is refused, every swap already
done in it is rolled back, and the applied version doesn't change.

---

## Staged swap

The reconciler replaces a component in four steps
([design, Staged swap](../shibu.md#staged-swap)):

1. **Start** the new implementation alongside the old one.
2. **Conformance.** Run the contract's suite against it
   ([`extension-points.md`, Common to every distribution
   contract](extension-points.md#common-to-every-distribution-contract)).
3. **Re-point** the `/sys/...` endpoint to it, in one `batch`
   (`delete_endpoint`, `create_endpoint`, re-issuing the root grant).
4. **Keep the old one warm** for `rollback_window` (default 30
   minutes). `POST rollback` within the window re-points to it at once,
   without a new conformance run.

If conformance fails, nothing changes, and `sys.swap` reports
`refused` with the suite's report. A bad commit to the config repo, or
a bad edit in the UI, can't take out a bootstrap component.

For a **driver**, the kernel runs the swap inside `sys_plugins`, and
step 3 re-points new creates only. VMs on the old
driver can't migrate, so they drain there (by exiting or being
deleted), and the old driver stays loaded until its last VM is gone.

Each step is a `sys.swap` event.

---

## UI mode and repo mode

- **UI mode**, the default. Root edits the spec in the UI, or with
  `shibu sys apply`, and the reconciler applies it directly. The history
  keeps every version, so any earlier one can be applied again.
- **Repo mode**, opt-in. Root turns the reconciler on, pointing it at a
  repo, a branch and a path
  ([`reconciler/v1`](extension-points.md#reconcilerv1)):

  ```yaml
  reconciler:
    image: shibu/reconciler:1.0.0
    contract: reconciler/v1
    mode: repo
    config: {repo: /sys/git/acme/shibu-config, branch: main, path: sys.yaml}
  ```

  The repo is then the source of truth, and edits in the UI become pull
  requests against it. The kernel never needs git itself, so there is
  no bootstrap chicken-and-egg: the kernel starts first, then the
  reconciler in UI mode, and only then can it reach a repo. Switching
  back to `mode: ui` returns `/sys` to UI mode, from the last spec it
  applied.

---

## The default spec

Install starts the kernel with its default plug-ins (the docker +
granule driver on the install host, the built-in secrets backend),
boots the reconciler from an image baked into the install, and has it
apply a default spec: the standard distribution, with every component
on except the pool and the `agents` service, and the reconciler in UI
mode. It needs no repo, and no network beyond the
image registry
([design, Bootstrap order](../shibu.md#bootstrap-order)).
