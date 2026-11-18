# Persistent Session Workspaces Specification

**Status:** proposed normative contract for a future implementation; not a claim
of current behavior. Motivation, source inspection, backend scope and tradeoffs
are in the [design](../../design/persistent-session-workspaces.design.md).
This specification governs the proposed feature when implemented. Existing
service sandboxes and ephemeral sessions retain their current contracts.

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY have their BCP 14 meanings.

## WS-1 Identity, ownership and authorization

1. A workspace MUST have a stable immutable ID, owner principal, repository,
   initial ref/resolved commit, desired compute state, image/shape, app command,
   relative directory, optional port, volume ID and storage generation.
   Observed compute/app/storage states MUST be separate from desired state.
2. A volume MUST have its own durable record, provider handle, deployment and
   namespace ownership, capacity, generation and lifecycle state. Compute,
   session, task and run records MUST NOT own its deletion lifecycle.
3. A session/task MAY reference one workspace; v1 MUST permit at most one attached
   interactive session per workspace, including idle attachment. Many browser
   clients MAY reconnect to that session. Other tasks and CI MUST NOT mount it.
4. Read/list require Grain reader access and workspace owner/collaborator access;
   app/terminal interaction, attachment and compute mutations additionally require
   writer access. Clear/delete/ownership transfer require owner or administrator
   authority. Administrators MAY recover retained storage. Checks MUST occur on
   server APIs and gateway requests, not just visibility of UI controls.
5. Repository/capability authority MUST be checked independently of workspace
   ownership at create, attach, start, execution and renewal. A session MUST NOT
   acquire the union of another session's cached grants. Unauthorized IDs MUST NOT
   leak metadata through list or error responses.

## WS-2 Durable bytes and backend qualification

1. Initial support MUST be restricted to explicitly qualified ordinary Kubernetes
   sandbox pods and CSI StorageClasses supporting ReadWriteOncePod and verified
   termination/detachment. Container, host, granule and k8s-granule backends MUST
   advertise unsupported until independently implemented and qualified.
2. Unsupported create/start requests MUST fail explicitly without allocating scratch
   storage as a substitute. Backend capability discovery MUST be available to UI/API.
3. One workspace MUST own one PVC outside pod/session ownerReferences. Callers MUST
   NOT select arbitrary provider handles, host paths or another workspace's claim.
   Claims MUST carry immutable deployment/workspace/generation labels.
4. The claim MUST mount at `/workspace`; checkout is `/workspace/repo`, app data
   `/workspace/data`. OS/root disk, `/tmp`, memory and secret mounts are ephemeral.
   Start/rebuild/session expiry MUST preserve the claim and durable bytes.
5. The advertised guarantee MUST be filesystem retention across compute release,
   replacement, session expiry and daemon restart/upgrade, subject to the qualified
   storage provider's durability and crash-consistency contract. Applications remain
   responsible for fsync and database recovery. Process/editor-buffer continuity
   and disaster recovery MUST NOT be promised.
6. Missing or unreadable storage MUST produce unavailable/storage-lost status.
   It MUST NOT cause automatic empty-volume creation or checkout reset.

## WS-3 Checkout, app and credentials

1. Checkout initialization MUST occur only on a new confirmed-empty generation.
   It MUST clone through a temporary directory, atomically publish the checkout,
   persist resolved commit/initialization state and recover interrupted initialization
   without overwriting existing files. Disk markers alone MUST NOT authorize reset.
2. Remote URLs and persistent git configuration MUST be credential-free. Rebuild,
   ref configuration edits and service refresh logic MUST NOT reset, clean, pull
   or reclone an initialized checkout. Ref edits apply on the next explicit clear;
   users MAY run ordinary git operations themselves.
3. The app command MUST execute as a stable unprivileged UID/GID, with a validated
   working directory contained within repo. Symlink escapes MUST be rejected.
   Configured ports MUST be in 1–65535 and reached through backend Dial, without a
   public host-port fallback. No app command/port MUST permit terminal-only use.
4. App command, image, literal env and secret references MUST be configurable.
   Secret values MUST NOT be serialized into workspace configuration or API output.
   Unprivileged images incompatible with the volume permissions MUST fail visibly.
5. Grain-injected git/model/API credentials, CLI auth state and secret files MUST
   use restricted ephemeral mounts outside the volume. Grain MUST NOT configure
   credential helpers to persist credentials there. Logs/errors MUST redact secrets.
6. Credentials MUST be short-lived and workspace/session/epoch scoped, renewed before
   expiry against current grants, and revoked on stop, detach, takeover or withdrawal.
   Apps unable to reload rotated credentials MUST be restarted.
7. Withdrawal MUST stop execution, close app streams, revoke access and remove
   ephemeral secret material. If termination cannot be proven, the workspace MUST
   be quarantined. Durable bytes MUST remain, but repository withdrawal MUST block
   editor/terminal/file access until rights are resolved. Arbitrary user-copied
   credentials in files/history are outside the non-persistence guarantee.

## WS-4 Operations, fencing and recovery

1. Mutating requests MUST carry an idempotency key and expected workspace version.
   Persist operation ID, target generation/epoch and intent before provider calls.
   A stale version or conflicting operation MUST return conflict (409); retries of
   the same operation MUST return its existing result rather than repeat destruction.
2. Start MUST mount the existing claim. Stop MUST terminate execution/app, revoke
   routes/tokens and release compute only. Restart MUST restart processes on the
   same bytes. Rebuild MUST replace compute/root disk and preserve the same volume,
   checkout and uncommitted changes. Desired stopped state MUST survive restart.
3. Attach MUST be serialized by durable session attachment and fencing epoch.
   A second session MUST receive 409 and the authorized current-session link.
   Explicit detach/takeover MUST cancel/join old execution and verify old compute
   termination/detachment before another writer starts. Database lease expiry alone
   MUST NOT authorize mounting a partitioned writer's volume.
4. A workspace MUST allow only one active compute writer; RWOP and provider/pod
   fencing MUST enforce that in addition to the store lease. App and session
   processes inside that compute share files; no cross-process transactional-write
   guarantee is provided.
5. Startup reconciliation MUST inventory workspace claims/compute before generic
   orphan reaping. It MUST adopt matching verified compute or fence and replace it
   on the existing claim. Unknown tagged resources MUST be quarantined for repair,
   never automatically deleted as ephemeral run orphans.
6. Backend failures MUST preserve handles, quota reservations and pending operations,
   report errors and retry with backoff. Timeout MUST NOT imply successful deletion
   or permission to create a second writer. Permanent loss requires explicit restore
   or clear. Daemon upgrade MAY interrupt processes and connections but MUST preserve
   workspace/attachment records and storage.
7. Default idle behavior MUST keep compute running. An explicitly selected idle-stop
   policy MAY release compute only. Pressure reclamation MUST honor its advertised
   interruption policy and MUST NOT invoke durable data deletion. Normal session
   expiry/completion/deletion MUST only detach or stop compute.
8. Compute status MUST distinguish stopped, provisioning, starting, running, stopping,
   rebuilding, unavailable and failed. App readiness MUST be separate. Storage MUST
   distinguish provisioning, ready, clearing, retained, deleting, lost and failed.
   Pending operations, timestamps and actionable errors MUST be visible.

## WS-5 Clear, delete, retention and accounting

1. Clear MUST be a separate destructive operation, never a flag implicitly enabled
   by rebuild or session expiry. UI confirmation MUST name workspace, generation,
   bytes/data affected and irreversibility, require typing the name, and obtain a
   short-lived single-use server confirmation bound to actor, operation and version.
   API/CLI clients MUST use the same confirmation protocol, not bypass it with a
   generic compute permission.
2. Clear MUST revoke routes, fence execution, delete the old claim, confirm deletion,
   create a new generation and initialize from configured ref. It MUST default to
   stopped. Failure MUST remain explicit and retryable with its operation identity;
   a stale confirmation MUST NOT clear a newer generation.
3. Delete MUST offer retain-storage as default and destroy-storage as an explicit
   confirmed choice. Retained records/tombstones MUST preserve owner, provider
   handle, generation, capacity and recovery linkage in an owner/admin view.
   Restore MUST recover the same workspace ID and claim after access checks.
4. Destroy MUST remain pending until provider deletion is verified. Session removal,
   workspace metadata/config removal, account deletion, TTL and garbage collection
   MUST NOT implicitly destroy durable storage. Ownership/account removal requires
   an explicit administrator disposition.
5. Storage quota MUST reserve provisioned capacity atomically before allocation and
   include stopped, retained and deletion-pending volumes. Compute MUST use existing
   admission/usage accounting with a workspace category, separately from volume
   capacity and ephemeral scratch DiskGB. Quota failure MUST NOT allocate a claim.
6. Initial recommended defaults are 20 GiB per volume, 100 GiB maximum, 5 workspaces
   and 200 GiB per owner; operators MUST configure/enforce deployment ceilings.
   V1 MUST NOT shrink volumes. Full-disk errors MUST NOT trigger user-data cleanup.
   UI MUST show provisioned capacity and measured usage with its measurement time,
   or explicitly unknown usage.
7. Operations and ownership changes MUST be audited with actor, workspace, operation,
   epoch/generation, timestamps and outcome, excluding secret values.
8. Backups/snapshots MUST be labeled optional with policy and recovery point if
   configured. No automatic backup or migration guarantee is included. Restore
   MUST fence compute and introduce a new recorded generation. Delete MUST disclose
   provider-retained disks/external backups and MUST NOT claim certified erasure.

## WS-6 UI and authenticated application access

1. Grain MUST expose discoverable workspace list/create/detail, session-to-workspace
   and workspace-to-session links, state/capacity/owner/errors, logs and authorized
   start/stop/restart/rebuild/attach/detach/clear/delete controls. Retained storage
   MUST remain discoverable. Management links MUST respect deployment path prefixes.
2. Open UI MUST use authenticated per-workspace HTTPS hostnames on a separate
   registrable site from Grain. Without configured isolated access, Open UI MUST
   be unavailable with an explanation; terminal/session management MAY remain.
3. The gateway MUST use a short-lived, audience/workspace/user-bound, single-use
   top-level POST handoff, then its own host-only Secure HttpOnly auth cookie.
   No durable bearer token may appear in URLs. Reserved gateway auth paths/cookie
   names MUST NOT be controlled by the app.
4. The gateway MUST resolve targets from authorized workspace identity and current
   compute epoch, stripping Grain authorization/access-code headers and cookies.
   App cookies MUST be host-only, stripped of Domain and unable to overwrite gateway
   cookies. No ambient Grain API authority or permissive Grain CORS is permitted.
5. Authorization MUST precede every HTTP request and WS upgrade. Established streams
   MUST be revalidated at most every 60 seconds and closed on revocation/expiry;
   revocation notification SHOULD close them immediately. WS Origin MUST match the
   workspace host; CSRF protection MUST cover management/auth mutations.
6. HTTP, nonbuffered SSE and WebSocket upgrades MUST work through the authenticated
   gateway. Serve the app at root so absolute assets, redirects and WS paths stay
   on its isolated origin. Target host/port MUST NOT be user-supplied proxy input.
7. Default Open UI MUST use a new tab with noopener. Optional embeds MUST preserve
   separate origins, restrict frame ancestors and validate postMessage origins.
   The design MUST NOT require third-party cookies for the primary access workflow.
8. Existing `/staging` MAY remain for service previews under its existing auth,
   credential filtering, scoped cookies and opaque-origin CSP. It MUST NOT become
   the default workspace route, lose that CSP to fix app compatibility, or imply
   durable volume support. Prefix stripping alone MUST NOT be advertised as support
   for arbitrary root-relative applications.
9. Existing staging UI restoration belongs to parity audit Task #28; durable workspace
   state and session/storage controls are new feature scope.

## WS-7 Management API contract

These proposed routes MUST use Grain's existing authentication, role checks, error
envelopes and pagination conventions. Responses MUST exclude secret values.

| Route | Meaning |
| --- | --- |
| `GET /api/workspace-capabilities` | Supported backend, limits, isolated UI availability and persistence boundary |
| `GET/POST /api/workspaces` | Authorized paginated list / create intent with repository, ref, shape, image, capacity, app and idle policy |
| `GET/PATCH /api/workspaces/{id}` | Detail / versioned configuration edit; no checkout mutation |
| `POST /api/workspaces/{id}/{action}` | start, stop, restart, rebuild, attach, detach or restore; attach supplies session ID |
| `GET /api/workspaces/{id}/logs` | Authorized redacted app log tail/stream |
| `POST /api/workspaces/{id}/confirmations` | Fresh destructive confirmation bound to clear or delete, retention choice and current generation/version |
| `POST /api/workspaces/{id}/clear` | Confirmed reset; returns operation ID |
| `DELETE /api/workspaces/{id}` | Explicit retention choice; destruction requires confirmation |
| `GET /api/workspace-operations/{id}` | Authorized operation status and retryable error |
| `POST /api/workspaces/{id}/open` | Authorized gateway POST handoff material; no persistent token |

Mutations MUST require idempotency keys and version preconditions as in WS-4.
Long operations MUST return 202 with an operation reference; retries MUST return
the original operation. Unsupported capability MUST return 422 with a machine
readable reason; denied authorization MUST use existing 403/404 conventions.
Session/task detail MUST expose the authorized workspace reference. A retained
storage filter on list MUST include tombstones for owners/admins; compute actions
against retained records MUST require Restore first.

## WS-8 Acceptance scenarios

These are release gates for implementation, not tests performed by this design PR.

| Scenario | Required observable outcome |
| --- | --- |
| Qualified create | Workspace ID returned; one quota reservation/PVC; initialized repo at recorded commit; stable detail link |
| Retry create after provider timeout | Same operation finds its labeled PVC; no duplicate allocation or quota |
| Unsupported backend | Explicit unavailable capability; no scratch substitute, PVC or reservation leak |
| Reconnect after daemon restart | Session/workspace links and dirty file survive; process interruption disclosed |
| Stop/start, restart, rebuild with a marker and dirty checkout | Same volume/generation and exact files; no implicit fetch/reset; root-disk-only changes may disappear |
| Session idle TTL, deletion and pressure reclamation | Compute may stop per policy; marker remains and storage remains charged |
| Two sessions or competing reconcilers attach | One writer; other receives conflict; repeated browser reconnect to current session works |
| Network partition during takeover | No second writer until old pod/mount fenced; unavailable state is visible |
| Crash between each clear/delete provider/store step | Idempotent resume, no wrong-generation deletion, no live old writer, accurate quota |
| Stale or absent clear confirmation | Rejected; rebuild and ordinary expiry never request or perform clear |
| Confirmed clear | Old claim deletion verified; new generation/checkout; prior data absent, stopped by default |
| Delete retaining storage / restore | Owner/admin sees charged retained storage; restore reconnects same data and ID |
| Explicit destroy | Pending until provider confirms deletion; releases quota only then; backup caveat shown |
| Missing PVC, failed mount or full disk | Actionable failure; no fresh empty replacement or automatic data cleanup |
| Grant/repo access revoked while app and agent run | Access and tokens revoked, streams closed, compute stopped/quarantined; bytes preserved but editor access blocked |
| Credential renewal and rebuild | Valid renewed capabilities work; injected old secrets absent from durable files and logs |
| App with absolute assets, cookies, localStorage and WS | Works at isolated root origin through auth; no access to Grain cookies/API or another workspace's storage |
| Malicious app cookies/headers and forged WS Origin | Cannot overwrite auth cookies or gain Grain authority; invalid upgrade denied before dial |
| Proxy deployment and no gateway configuration | Prefix-correct management links; explicit Open UI unavailable, no unsafe host-port fallback |
| Owner quota exhaustion and stopped volumes | Provisioning denied before allocation; all retained provisioned bytes accounted |
| Upgrade/orphan sweep | Tagged workspace claims preserved; matching compute adopted or fenced; ephemeral run cleanup still works |
| Optional snapshot restore | Recovery point/caveat visible, fenced new generation, ownership preserved; no backup guarantee if disabled |
