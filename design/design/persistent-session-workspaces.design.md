# Persistent session workspaces

**Status:** proposed, design only (Task #29). No backend support is implied by
this document. The [proposed normative specification](../spec/workspaces/workspaces.spec.md)
defines the implementation contract; existing code continues to define current behavior.

## Recommendation

Introduce a user-owned Workspace with an independently owned durable Volume and
replaceable sandbox compute. Store desired state, ownership, attachment leases,
and volume identity in the model store. Reconcile compute from that state rather
than making a running supervisor goroutine the owner of data. A session references
the workspace; neither a session nor a task owns its volume.

Ship first for ordinary Kubernetes sandbox pods with a qualified CSI StorageClass
and ReadWriteOncePod support. Give each workspace one PVC, mounted at
`/workspace`, independent of pod ownership. Fail closed on other backends.
Provide an authenticated workspace gateway on a separate browser site, opening
the workspace app at its own root origin from Grain's workspace detail page.
Default to one attached interactive session with one serialized execution stream.

This costs a PVC and a gateway configuration rather than extending today's warm
pool. In exchange rebuild, expiry, daemon replacement, and data deletion have
separate meanings. The UI remains an integrated entry point without giving
arbitrary development servers Grain's browser authority.

## What exists and what can be reused

The following is an inspection of the checkout, not a statement of workspace support.

| Existing surface | Current behavior / reuse | Required change |
| --- | --- | --- |
| `pkg/model/persistent_sandbox.go` | Config-defined repository/ref, command/dir/env, shape, port, staging subpath, refresh policy | Separate user workspace identity, ownership, volume, session attachment and desired compute state; do not overload service definitions |
| `pkg/orchestrator/persistent_sandboxes.go`, `persistent_refresh.go` | Supervision, backoff, events, logs, metered acquisition; rebuild explicitly creates a fresh VM and clone; checkout can fetch/reset to selected ref | Reuse supervision patterns, not checkout/release semantics. Workspace reconciliation must preserve dirty checkout and volume, and never auto-refresh code |
| `standing_sandboxes.go`, `model.StandingSandbox` | Config-driven image-serving instances, liveness probes, restarts on definition/secret changes | Reuse probes and accounting; image lifetime does not establish durable storage |
| `session_sandboxes.go`, `reconcile_sandboxes.go` | Warm interactive sandbox keyed by task/config/grants, default idle TTL 30 minutes, oldest-first pressure reclamation, release on changed grants | Keep ephemeral sessions working unchanged; workspace-backed dispatch must bypass destructive warm release, use durable attachment state, and stop only compute |
| `pkg/ui/persistent_sandboxes.go` | Auth-gated HTTP/SSE/WebSocket reverse proxy using backend Dial, credential stripping, cookie scoping, CSP sandbox | Reuse transport/security tests and routing mechanics; new per-workspace authorization and browser isolation required |
| `ui/src/components/PersistentSandboxesPanel.jsx`, `ui/src/staging.js` | Service state, logs/restart/rebuild controls, rail links and host-port/proxy warnings | New workspace list/detail/create and session association; do not relabel staging services as durable workspaces |
| `pkg/orchestrator/orchestrator.go` | Sandboxes acquisition, Shape and Release describe compute; no independent durable-volume contract | Add optional backend capability and volume/attachment operations; never make ordinary Release delete workspace storage |
| `container_sandboxes.go`, `granule_sandboxes.go`, `orchestrator.go` (HostSandboxes), `k8s_granule_sandboxes.go` | Container/host scratch and VM disks are compute resources, not a portable user-owned volume API | Unsupported initially; local bind mounts or guest disks need separate durability/fencing designs |
| `k8s_sandboxes.go`, `pkg/k8s/k8s.go` | Pod setup uses emptyDir, secret mounts and host device mounts; ClaimInfo can inspect PVCs | Add PVC provisioning, pod claim mounts, detach verification and storage accounting; ClaimInfo alone is insufficient |

The daemon's own `grain-data` PVC in
[Grain on GKE](grain-on-gke.design.md) stores deployment data; it is not a
per-session volume and must not become a shared writable workspace root.
[Architecture](architecture.design.md) describes the existing ephemeral boundary.
[Non-disruptive upgrade](non-disruptive-upgrade.design.md) and
[agentd stable contract](agentd-stable-contract.design.md) address execution
continuity; this proposal requires filesystem continuity, not surviving processes.
[Hub](hub.design.md) explicitly treats sandbox disks as scratch. Store durability
and volume durability must be configured together before advertising workspaces.
[RBAC](../spec/rbac/rbac.spec.md) currently gives writers staging access; a
workspace additionally needs object ownership checks.

## Object and execution model

A Workspace has an immutable ID, owner principal, authorized collaborators,
repository identity, initial ref/resolved commit, desired compute state,
image/shape, command, relative working directory, optional served port,
volume ID, storage generation, resource limits, timestamps and an operation log.
Literal secret values are excluded. Actual state is observed separately.

A Volume record owns an opaque provider handle, deployment/namespace ownership
labels, capacity, backend/StorageClass, generation, readiness and deletion state.
One live workspace owns one volume; no user-supplied PVC handle or cross-workspace
mount in v1. A compute instance carries workspace ID, volume generation and a
monotonic fencing epoch. A Session/Task stores an optional workspace ID. Runs
store the epoch and compute ID, so recovery can distinguish old from current work.

A user may reconnect many browser clients to the same session. V1 allows one
attached interactive session per workspace, including while that session is idle.
Another session receives an attachment conflict with a link to the current session.
Detach/takeover first cancels and joins all old execution, revokes its credentials,
and proves the old mount fenced. A database lease alone cannot fence a partitioned
pod. RWOP and verified termination/detachment provide the second boundary.
There is no simultaneous agent writer, CI run, or separate task sandbox on the
same volume. The served app and the attached agent necessarily share a filesystem;
applications must coordinate their own file/database writes. This is not a
transactional filesystem or a promise that an editor and agent cannot conflict.

## User workflows

1. **Create:** Workspaces → Create. Choose repository/ref, name, capacity,
   compute shape, image and optional app command/port. Show persistence boundary,
   cost and backend availability. Reserve quota, persist intent, provision a PVC,
   mount it, initialize a checkout, then start the configured app. Creation can
   also leave compute stopped. Return a stable workspace detail link immediately.
2. **Attach/reconnect:** Choose Attach session on the detail page or select a
   workspace when creating an interactive session. Check repo/grants/ownership,
   acquire the exclusive attachment, then start compute if needed. A reconnect
   restores the session UI and workspace link even after daemon restart. Resume
   the conversation from its stored transcript; do not claim a killed CLI process
   or unsaved editor buffer survived.
3. **Open:** Open workspace UI becomes available after the app readiness probe
   passes. The detail page also shows app logs and connection errors. Opening
   creates a short-lived gateway login handoff and launches a new tab; an embed is
   optional only after the separate-origin contract is met.
4. **Stop/start/restart/rebuild:** Stop pauses the app and session execution and
   releases compute. Start mounts the same volume. Restart terminates and starts
   processes on preserved data. Rebuild replaces the compute image/root disk,
   remounts the same volume and runs the configured startup command. No operation
   pulls/reset/cleans the user's checkout. Warn about interruption and unsaved
   process memory, but do not ask to delete data.
5. **Clear/reset:** Explicit Clear durable data shows name, volume generation,
   capacity, irreversible local edits/app-data loss and backup caveat. Require
   typing the workspace name and a fresh confirmation token. Fence compute,
   delete the old PVC and verify deletion, allocate a new generation, then
   initialize from the configured ref. Default remains stopped until Start.
   A failed clear remains visibly clearing/failed; do not quietly restore old
   contents or allocate a usable replacement while the old writer exists.
6. **Delete:** Offer Delete workspace, retain storage (default) or Delete workspace
   and storage (explicit destructive confirmation). Retained storage is listed in
   an owner/admin recovery view, still charged, with a Restore action to recreate
   the workspace record with its same ID. Data deletion remains pending until
   provider deletion is confirmed. Ordinary session deletion only detaches.

## Storage, checkout and secrets

Mount the claim at `/workspace`; use `/workspace/repo` for checkout,
`/workspace/data` for app data, and a Grain-managed metadata directory inaccessible
to the unprivileged app where the backend permits. The authoritative initialization
marker and provider handle live in the store; treat disk metadata as untrusted.
`/tmp`, OS packages outside the volume, process memory, and secret mounts are
ephemeral. Show this boundary beside command configuration.

Clone into a temporary directory on the volume using credential-free remote URLs,
then atomically rename into repo and commit initialization state. Recovery after a
partial clone recognizes only its own incomplete generation and never overwrites
an existing checkout. The initial ref is resolved once. An app command executes
as an unprivileged stable UID/GID in repo (or a validated relative subdirectory);
it can run an editor, notebook, development server or another user-selected tool.
No command and port means terminal/session only. Configure a port in 1–65535 and
bind to the sandbox interface reachable by backend Dial; do not expose host ports.
Image dependencies are baked into the selected image or installed by an explicit
startup command; installs outside the volume are lost on rebuild.

Grain-managed credentials, CLI auth homes, provider tokens, git helpers and secret
files live in restricted tmpfs/ephemeral mounts outside the volume. Git remotes,
config, logs and clone errors must not contain credentials. Disable git credential
storage on durable paths. Keep app env as literals plus secret references; reject
secret values in persisted config where identified by secret inputs. Re-resolve
references on every start, rotate credentials before expiry, and restart processes
that cannot reload them. Credentials carry workspace/session/epoch scope and
short expiry; proxies enforce current grants rather than trusting cached files.

Revocation closes gateway connections and cancels execution immediately, revokes
proxy tokens and stops compute, destroying ephemeral secret mounts. If fencing is
uncertain, quarantine rather than attach elsewhere. Keep durable user data but
block access until remaining repository and workspace rights are evaluated.
Repository content is durable data too: withdrawing repo authorization must block
checkout access, including through an editor UI, rather than merely deny git fetch.
An administrator can explicitly authorize recovery/export or destruction.
No mechanism can erase a credential a user deliberately copied into arbitrary
files or git history. Grain guarantees it does not persist its own injected secret
material, not that arbitrary workspace contents are free of secrets.

## Lifecycle and failure handling

Persist operations with idempotency keys and version preconditions before provider
calls. A single reconciler per workspace holds a renewable lease and fencing epoch.
At daemon startup, inventory workspace-tagged pods/claims before generic orphan
reaping. Adopt verified matching compute or fence it and recreate on the same
claim. Never infer a missing claim means empty storage should be provisioned.

Upgrades may interrupt sessions, apps and WebSockets. Desired state, attachment
identity and volume survive; clients reconnect and new compute resumes on the
same data. On backend timeout, retain handles/reservations, report unavailable,
retry with bounded backoff and reconcile by labels/idempotency key. Do not launch
a second writer during uncertain detach. On permanent disk loss, report
storage-lost and require explicit restore or clear; availability is not durability.

Default idle policy is keep running until Stop. Operators may offer an explicitly
selected idle-stop policy (recommended optional preset: 24 hours without session
execution or active app connections). Idle-stop releases compute only. Pressure
reclamation may stop workspace compute after warning/cancellation according to
its advertised policy, never route it through warm sandbox data cleanup.
Stopped volumes have no automatic expiration. Ownership changes and account
deletion require admin retention disposition, not garbage collection of bytes.

The guarantee is retention of committed filesystem writes on the qualified CSI
volume across compute replacement, ordinary session expiry and daemon restart.
Crash consistency follows the filesystem/CSI provider: applications must fsync
and use their own database recovery. It is not process checkpointing, regional
disaster tolerance or guaranteed survival of a provider volume loss.

## UI gateway and the staging question

Use a dedicated workspace gateway site on a different registrable domain from
Grain. Each workspace gets its own HTTPS hostname so root-relative assets,
cookies, localStorage, service workers and WebSockets work naturally. Wildcard
DNS/TLS and ingress are future operator prerequisites, not changes in this PR.
Do not use a sibling hostname while Grain has domain-wide cookies.

Grain issues an audience/workspace/user-bound, single-use handoff through a
top-level POST; never place durable bearer tokens in a URL. The gateway redeems
it with Grain and sets its own host-only Secure HttpOnly cookie. Its auth path
and cookie namespace are reserved; the app cannot intercept them or set gateway
cookies. Scope cookies to this workspace, strip Domain on app responses and
reject reserved cookie names. Forward neither Grain cookies nor authorization
headers. Current authorization is checked before each HTTP request/WS upgrade;
bounded revalidation closes established streams on expiry or revocation.
Mutating API calls use CSRF protection; WS upgrades validate Origin against the
specific workspace host. Do not accept arbitrary target hosts/ports from requests.
App-to-Grain API calls receive no ambient authentication or permissive CORS.

Support HTTP, streaming SSE and WebSocket upgrades without response buffering.
Resolve target from authorized workspace ID and current epoch; revoke routes
before compute changes. New-tab links use noopener. A later iframe must restrict
frame ancestors to Grain, keep app content cross-origin, and avoid wildcard
postMessage trust. Browsers and proxy deployments that block third-party cookies
are why new-tab is the default.

Existing `/staging/{name}/` is suitable for trusted service previews with its
current restrictions, not the default workspace UI. It already strips Grain
credentials and scopes cookies, and adds CSP `sandbox` without
`allow-same-origin`. That opaque origin deliberately limits script storage and
can break editors and notebooks. Prefix stripping does not rewrite absolute asset
URLs, redirects or app WebSocket URLs. Never relax this CSP on Grain's origin to
make a workspace work. If isolated gateway configuration is absent, show terminal
and workspace management with Open UI unavailable; do not silently fall back to
a host port or unsafe staging route. The dedicated origin serves apps at `/`;
a deployment-wide Grain path prefix must still be respected by management links
and auth return links.

Workspace list/detail shows owner, repo/commit, attached session, desired/observed
compute state, app readiness, storage state/capacity/usage age, idle policy,
last error and pending operation. Session pages show a persistent Workspace link.
Controls are permission-aware and explain blocked readiness/fencing/quota.
Keep Staging service navigation separate.

Task #28 audits UI parity. Existing staging rail/panel/logs and controls are
parity surfaces; workspace create/list/detail, durable volume controls and session
association are new behavior, not restoration of an old feature. Its task record
was not readable under this task's grants. Implementers should reconcile component
placement with that audit, without making durability contingent on restoring a
legacy page or changing this storage contract.

## Operations and scope

Operator choices needed before rollout (recommended choices):

| Decision | Recommendation |
| --- | --- |
| Supported storage | Opt in ordinary k8s pods only, CSI class verified for RWOP, encryption and pod reattachment; no implicit default-class fallback |
| Limits | 20 GiB default, 100 GiB maximum per workspace; 5 workspaces / 200 GiB per owner initially, configurable; reserve provisioned bytes, including stopped/retained/deleting volumes |
| Compute | Existing shape/admission ledger plus a workspace category; running compute consumes capacity, stopped compute does not; charge storage separately from scratch DiskGB |
| Retention | Indefinite until explicit delete; alert owners/admins on retained storage and capacity pressure |
| Browser access | Dedicated gateway site with per-workspace HTTPS hostnames; disable Open UI until available |
| Backups | No automatic backup promise in first release; display policy and last recovery point if an operator supplies CSI snapshots |

Provision with namespace-scoped service-account rights, immutable deployment and
workspace labels and no ownerReference to a pod/session. Fix UID/GID ownership
once at initialization, use fsGroup where needed, no world-writable host mounts
and no user-selected arbitrary mount paths. Reject incompatible images instead
of granting privileged access. Enforce admission transactionally before allocation;
keep storage charged until deletion confirmation. Capacity-full errors must not
trigger cleanup of user files. V1 capacity is fixed; expansion can follow only
with driver support and separate admission. Do not shrink.

Snapshots/backups are optional operator facilities, not substitutes for volume
retention. Crash-consistent snapshots require app quiescence for stronger claims.
Restores create a new volume generation with compute fenced. Metadata backups
must include opaque volume IDs, ownership and generations without secret values.
No automatic cross-backend migration, shared RWX volumes, live process migration,
cross-region replication, arbitrary port tunneling, public sharing, or restoration
from git of uncommitted data. Provider-retained disks and external backups can
outlive a delete; UI must disclose that delete is not certified media erasure.
Migration from a staging/warm sandbox is explicit export/import while stopped,
never an automatic claim of durability for existing scratch disks.

## Implementation sequence

1. **Model/API contract:** migrations for workspace/volume/attachment/operations,
   owner authorization, quotas, tombstones and audit events; optional backend
   capability advertisement. No UI Create before a qualified backend exists.
2. **Storage adapter (depends on 1):** CSI PVC create/inspect/delete, RWOP mount,
   idempotency, ownership validation, detach/fence checks, UID/GID initialization,
   orphan inventory. Integration tests prove pod replacement preserves a marker.
3. **Reconciliation and sessions (depends on 1–2):** durable desired state,
   startup recovery exclusions from reapers, separate compute release, serialized
   attachment/dispatch, ephemeral auth injection/rotation/revocation and checkout
   initialization. Failure injection around every provider/store boundary.
4. **Gateway (depends on 1–3):** per-workspace TLS routing, handoff authentication,
   authorization/stream revocation, HTTP/SSE/WS, cookie/origin tests. Publish operator
   prerequisites in a later deployment/config change.
5. **UI (depends on 1–4):** new list/detail/create, session links, app open and
   destructive confirmations; coordinate existing component placement with Task #28.
   Terminal-only management may ship before gateway with UI unavailable explicitly.
6. **Rollout (depends on 1–5):** end-to-end acceptance from the specification,
   operator capability qualification and recovery runbook. Unsupported backends
   remain disabled; future backend adapters must pass the same contract.

This change supplies only the design and proposed normative spec. It does not
provision storage, modify deployment configuration or implement these stages.
