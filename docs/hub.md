# The Grain Hub

> **Renamed from the Grain Registry.** The Grain Hub was called the Grain
> Registry until this release. Every name below changed; the old one is
> still accepted for **one release**, logging a one-time deprecation
> warning, and the new one wins when both are set.
>
> | Old | New |
> | --- | --- |
> | `cmd/grain-registry`, binary `grain-registry` | `cmd/grain-hub`, binary `grain-hub` (the image keeps a `grain-registry` symlink) |
> | `Dockerfile.registry` | `Dockerfile.hub` (`Dockerfile.registry` is a verbatim copy for now) |
> | image `docker.io/grainai/registry` | `docker.io/grainai/hub` (releases publish both, same tags and signatures) |
> | `make image-registry`, `IMAGE_REGISTRY`, `MAIN_grain-registry` | `make image-hub`, `IMAGE_HUB`, `MAIN_grain-hub` |
> | `ui-registry/`, `pkg/registry/...` | `ui-hub/`, `pkg/hub/...` |
> | `grain registry <subcommand>` | `grain hub <subcommand>` |
> | daemon `-registry-url`, `-registry-entry`, `-registry-auth-mode`, `-registry-oidc-audience`, `-registry-token-file`, `-registry-display-name`, `-registry-purpose`, `-registry-host-kind`, `-registry-listed`, `-registry-connect` | `-hub-url`, `-hub-entry`, `-hub-auth-mode`, `-hub-oidc-audience`, `-hub-token-file`, `-hub-display-name`, `-hub-purpose`, `-hub-host-kind`, `-hub-listed`, `-hub-connect` |
> | `-ui-registry-issuer`, `-ui-registry-audience`, `-ui-registry-keys-url` (daemon and `grain demo`) | `-ui-hub-issuer`, `-ui-hub-audience`, `-ui-hub-keys-url` |
> | `grain hub` subcommands' `-registry-url`, `-registry-token` | `-hub-url`, `-hub-token` |
> | `$GRAIN_REGISTRY_URL`, `_ENTRY`, `_TOKEN`, `_CONNECT`, `_ISSUER`, `_AUDIENCE`, `_KEYS_URL`, `_CONFIG` | `$GRAIN_HUB_URL`, `_ENTRY`, `_TOKEN`, `_CONNECT`, `_ISSUER`, `_AUDIENCE`, `_KEYS_URL`, `_CONFIG` |
> | hub config `compute.registry_id` | `compute.hub_id` |
> | settings API key `registry` / `registryTokenSet` | `hub` / `hubTokenSet` (both are served and accepted) |
> | header `X-Grain-Registry-Assertion` | `X-Grain-Hub-Assertion` (the hub sends both; a daemon reads either) |
> | header `X-Grain-Registry-Token` | `X-Grain-Hub-Token` (a daemon sends both; the hub reads either) |
> | metrics `grain_registry_*` | `grain_hub_*` (both are exported) |
>
> Deliberately **unchanged**, because existing deployments and resources
> depend on them: the assertion issuer `grain-registry` (assertions
> already minted, and every daemon's `-ui-hub-issuer`, name it), the
> `grain-registry` label on hub-managed VMs and disks, the
> `registry-registration-token` secret name, the `registry` identity
> verifier name (`-ui-require-verifier=registry`), the daemon's
> `/api/registry` endpoints, and the hub's database tables. `pkg/registryauth`
> keeps its name too: it authenticates to container image registries
> (Artifact Registry, gcr.io), not to the Grain Hub.

A grain deployment is single-tenant ([`docs/access-code.md`](access-code.md)).
Across an engineering organization, running many single-tenant grains
raises two operational problems:

1. **Provisioning and reaching each grain.** Standing up a new grain by
   hand takes a machine, a domain and TLS certificate, an Identity-Aware
   Proxy (IAP) backend or access code, a database, and an upgrade loop.
   Grains running on developer workstations or inside inbound-blocked
   development environments also have no stable public address for
   teammates, web hooks or tools to reach.
2. **Discovering and routing work to the right grain.** Tools and people
   sitting above individual grains -- planning boards, chat commands,
   issue routers and cross-team handoffs -- need a fast, authoritative
   way to ask *which* grain owns a repository or subdirectory, whether
   that grain is online, and which person and team are accountable for
   it.

The **Grain Hub** (`cmd/grain-hub`, container image
`docker.io/grainai/hub`,
[`design/design/hub.design.md`](../design/design/hub.design.md))
solves both in one service:

- **As a host and reverse proxy**, the hub serves its own UI and
  control-plane API at `https://<registry-host>/` (for example
  `https://grain.example.com/`) and routes each registered entry at
  `https://<name>.<instance-domain>/` (for example
  `https://calm-otter-7f3a.grain-user.com/`) under a wildcard
  certificate and a single IAP backend:
  - **Hosted instances (`kind = instance`)** are stateless, replaceable
    single-tenant GCE VMs whose durable state lives in a shared Cloud
    SQL for PostgreSQL pool, Google Cloud Secret Manager, a per-instance
    Git configuration repository, and Cloud Storage. The hub routes
    to each hosted VM directly over the private VPC (`backend_url`,
    `http://<internal-ip>:8080`).
  - **Connected entries (`kind = connected`)** are self-registered
    grains that the hub did not provision -- running on developer
    workstations, in inbound-blocked dev environments, or on Kubernetes
    clusters -- that push periodic heartbeats and dial an outbound
    multiplexed WebSocket tunnel
    (`wss://<registry-host>/api/entries/<name>/connect`) with
    `-hub-connect`.
- **As a catalog and work router**, every entry (`instance` and
  `connected`) carries an accountable human `owner`, an optional
  co-managing `owner_group`, human-readable purpose and host metadata,
  opt-in directory visibility (`listed`), and repository + path-prefix
  **scopes** (`primary` or `secondary`). `GET /api/directory` and
  `GET /api/route` (and `grain hub directory` /
  `grain hub route`) let admitted users and authorized service
  principals discover listed grains and resolve the best grain for any
  `(repo, path)` in milliseconds.

---

## Contents

- [1. Host Isolation and Shared Authentication](#1-host-isolation-and-shared-authentication)
- [2. Hosted Instances (`kind = instance`)](#2-hosted-instances-kind--instance)
- [3. Connected Grains and the Outbound Tunnel (`kind = connected`)](#3-connected-grains-and-the-outbound-tunnel-kind--connected)
- [4. Catalog Metadata and Repository Scopes](#4-catalog-metadata-and-repository-scopes)
- [5. Accountable Ownership, `owner_group`, Quotas, and Transfers](#5-accountable-ownership-owner_group-quotas-and-transfers)
- [6. Catalog Discovery and Work Routing (`/api/directory` and `/api/route`)](#6-catalog-discovery-and-work-routing-apidirectory-and-apiroute)
- [7. CLI Reference (`grain hub`)](#7-cli-reference-grain-registry)
- [8. Running and Configuring `grain-registry`](#8-running-and-configuring-grain-registry)

---

## 1. Host Isolation and Shared Authentication

### Separate registrable domains

The hub's control plane (`host`, e.g. `grain.example.com`) and
entry subdomains (`*.<instance-domain>`, e.g. `*.grain-user.com`) live
on separate registrable domains. Putting entries on `<instance-domain>`
prevents a grain subdomain from sharing origin cookies with
`<registry-host>` or corporate domains, and `grain-registry` refuses to
start if `host` equals or is a subdomain of `instance_domain`.

### Edge verification and the `X-Grain-Registry-Assertion` JWT

Both `instance` VMs and outbound-tunnelled `connected` grains share the
exact same reverse-proxy authentication pipeline:

1. **Edge caller verification (`FrontIdentity`).** The hub verifies
   the caller's edge assertion (such as `X-Goog-IAP-JWT-Assertion` behind
   IAP), normalizes the caller's email and principal kind (`human` vs
   `service`), and checks that the caller is admitted by
   `auth.allowed_domains` (or `auth.service_principals` for catalog and
   service calls) and is not a disabled owner.
2. **Pre-strip CSRF check.** Before stripping cookies, the hub
   enforces same-origin CSRF verification (`Sec-Fetch-Site` / `Origin`
   against the target host) on state-changing requests (`POST`, `PUT`,
   `PATCH`, `DELETE`).
3. **Credential stripping.** The reverse proxy strips edge-only cookies
   and headers before forwarding to any backend or peer replica:
   - `X-Goog-IAP-JWT-Assertion`, `X-Goog-Authenticated-User-*`;
   - incoming `X-Grain-Registry-Assertion`, `X-Grain-Registry-Token`,
     `X-Grain-Auth`, `X-Registry-Peer-Auth`, `Proxy-Authorization`;
   - `Forwarded`, `X-Forwarded-*`, `X-Real-Ip`;
   - `__Host-GCP_IAP_AUTH_TOKEN_*` and `GCP_IAAP_AUTH_TOKEN_*` cookies;
   - `Authorization: Bearer <jwt>` when `<jwt>` is an OIDC token minted
     for the hub's edge/IAP audience or `https://<registry-host>`
     (leaving grain-scoped bearer credentials such as external-agent
     `gma_…` tokens intact).
   On responses from the grain, the proxy strips `Domain` attributes from
   `Set-Cookie` headers so every cookie set by an entry stays host-only
   on `<name>.<instance-domain>`.
4. **Short-lived, single-audience ES256 assertion.** The hub signs
   an ES256 JWT (`X-Grain-Registry-Assertion`) with a **60-second TTL**
   (cached per `(caller, entry)` on the serving replica for **30
   seconds**) and public verification keys published at
   `https://<registry-host>/.well-known/jwks.json`:
   - `iss`: the fixed string `grain-registry` (not the hub's URL)
   - `aud`: `["https://<name>.<instance-domain>"]` (strictly one
     entry's origin, so a token captured on `alice.<instance-domain>` is
     rejected by `bob.<instance-domain>`)
   - `sub` / `email`: normalized caller email
   - `kind`: `"human"` or `"service"`
   - `jti` and `rid`: token and request identifiers for audit correlation
5. **Grain-side verification (`-ui-require-verifier=registry`).** The
   target grain verifies `X-Grain-Registry-Assertion` using the built-in
   `registry` `IdentityVerifier`
   ([`docs/access-code.md`](access-code.md#three-modes)) configured with:
   ```text
   -ui-hub-issuer=grain-registry
   -ui-hub-audience=https://<name>.grain-user.com
   -ui-hub-keys-url=https://grain.example.com/.well-known/jwks.json
   -ui-require-verifier=registry
   ```
   The hub only identifies *who* is calling and routes the request
   to the entry; the grain's own **Settings → Access** allowlist
   (`allowed_principal`) and RBAC roles decide whether that caller is
   admitted to the grain and what they may do there.

### Delegated access: delegating apps and consent grants

With `auth.delegation_policy: consent`, a registered **delegating app** — a
service account such as a planning board or an issue triager — may call
`https://<name>.<instance-domain>/…` on behalf of a person by sending
`X-Grain-On-Behalf-Of: user:<email>`, but only while that person holds a
live **consent grant** to the app covering `<name>` (or `"*"`). The
hub still checks the person's own access to the entry, so a grant
never reaches further than the person could; a `read` grant also refuses
anything but `GET`/`HEAD`. (The default `static` policy refuses every
on-behalf-of request.)

**Registering an app (hub admins).** Only callers in `auth.admins`
can create, list, disable, or enable apps (from the Hub UI's
**Admin → Delegating Apps** section at `/admin#delegating-apps`, the API,
or the CLI):

| Route | CLI | Effect |
| --- | --- | --- |
| `GET /api/admin/delegating-apps` | `grain hub delegating-apps list` | List every app, enabled or not |
| `POST /api/admin/delegating-apps` | `grain hub delegating-apps create -principal <sa-email> [flags] <name>` | Register an app (`201`; `409` if the name or principal is taken) |
| `PATCH /api/admin/delegating-apps/<name>` `{"disabled": true\|false}` | `grain hub delegating-apps disable\|enable <name>` | Stop (or resume) the app acting for anyone; its grants are kept |

`principal` is the service account the app calls as. Give the bare email
or `serviceAccount:<email>`: the hub stores and matches the
`serviceAccount:` form either way, and refuses anything that is not a
`*.gserviceaccount.com` account (a person's email could never present as
a machine caller, so such an app would only ever read as
`app_not_registered`). Other fields: `display_name` (defaults to the
name), `description`, `homepage`, `owner` (a person; defaults to the
calling admin), `owner_group`, `max_access` (`read` or `write`, default
`write`), `max_grant_ttl_seconds` (default 90 days; `-max-grant-ttl` takes
a Go duration), `require_subject_token`, and `redirect_uris`.

```text
grain hub delegating-apps create -principal planner@my-proj.iam.gserviceaccount.com \
    -display-name "Planning board" -max-access read -max-grant-ttl 720h planner
```

**Granting consent (any person).** These routes act only for the signed-in
person (from the Hub UI's **Apps acting for you** screen at `/grants`,
the API, or the CLI); service accounts are refused.

| Route | CLI | Effect |
| --- | --- | --- |
| `GET /api/delegation-grants[?include_revoked=true]` | `grain hub grants list [-include-revoked]` | Your own grants, newest first |
| `POST /api/delegation-grants` | `grain hub grants create -entry <name\|*>... [-access read\|write] [-ttl <dur>] <app>` | Grant (or replace your live grant to) `<app>` |
| `DELETE /api/delegation-grants/<grant-id\|app>` | `grain hub grants revoke <grant-id\|app>` | Revoke one of your grants by its `dlg_…` ID or by app name |

A grant body is `{"app", "entries", "access", "ttl_seconds"}`. `entries`
is required: entry names, or `"*"` for every entry you can reach.
`access` defaults to the app's `max_access` and may not exceed it (`403
exceeds_max_access`); `ttl_seconds` defaults to 30 days and is capped at
the app's `max_grant_ttl_seconds`. A disabled app takes no new grants
(`403 app_disabled`). Grants are only ever your own: a body whose
`person` names anyone else is refused with `403` — hub admins
included — and somebody else's grant ID answers `404`, exactly like one
that does not exist. Every create, disable, enable, grant, and revoke is
recorded in `registry.audit_log`.

---

## 2. Hosted Instances (`kind = instance`)

A hosted `instance` entry is a single-tenant grain controller VM managed
by the hub reconciler in a dedicated GCP instance project and VPC.

### Creating a hosted instance

Any admitted human user in `auth.allowed_domains` (or matching
`auth.creators` when configured) may create a hosted instance from the
hub UI (`/new`), the API (`POST /api/entries`), or the CLI:

```bash
grain hub create \
  -hub-url https://grain.example.com \
  -shape standard \
  -display-name "Autoscaler" \
  -purpose "Autoscaler control-loop heuristics and scale-down simulations" \
  -owner-group group:autoscaler-eng@example.com \
  -scope acme/autoscaler::primary \
  -listed
```

- **Random names by default.** When `<name>` (or `-name`) is omitted,
  creation defaults to a random `<adjective>-<noun>-<4hex>` label (or
  `grain-<6hex>` from the CLI when generated client-side), avoiding
  naming friction and squatting on common words. You can also pass a
  custom DNS label (`[a-z][a-z0-9-]{1,30}[a-z0-9]`, no `--`, not in the
  reserved name list).
- **The UI defaults to a hosted VM.** `/new` opens on the Hosted VM
  form (`/new?kind=connected` opens the connected-entry form instead),
  with the name field pre-filled with a random `grain-<6hex>` name that
  you can type over or regenerate; availability is checked as you type.
  When the hub has no VM shapes configured, the form falls back to
  registering a connected entry.
- **Weighted shapes and custom boot disks.** Each shape (`small`,
  `standard`, `large`, etc.) specifies machine type, default boot disk size,
  and an integer quota weight (`quota_units`) charged against the owner's
  `max_instance_units` (default `2`). Pass `-disk-gb <gb>` (`boot_disk_gb`,
  `20`–`4000` GB) on `grain hub create` or `grain hub resize` to
  override the shape's default boot disk size. GCE instances boot with nested
  virtualization enabled (`enableNestedVirtualization: true`) for Firecracker
  sandboxes, using `hyperdisk-balanced` and `GVNIC` on `c4-*` machine types
  and `pd-ssd` on other families.
- **Break-glass SSH and fleet logs.** OS Login break-glass SSH for the
  instance owner is enabled by default (`compute.ssh_break_glass: true`,
  or `-allow-ssh` on `grain hub create` / `update`; see
  [`docs/access-code.md`](access-code.md#break-glass-ssh-on-registry-hosted-instances)).
  Fleet log shipping is off by default (`observability.fleet_logs: false`,
  or `-ship-logs` per entry).

### Replaceable VMs and externalised state

Hosted VMs are disposable compute: each VM runs from an immutable
`grain-release` boot image with an ephemeral `auto_delete: true` scratch
disk, no external IP address, and `-disable-self-upgrade`. Deleting a VM
and booting a replacement from a newer release or a larger shape loses
no state because all persistent data lives in four isolated backing
services:

| State | Backing service | Per-instance isolation |
| --- | --- | --- |
| Tasks, runs, comments, attachments, CI verdicts, merge queue, settings and memory tables, webhook replay cursor | **Shared Cloud SQL for PostgreSQL pool** (`instance_sql.sql_instances`) | Dedicated database `grain_<name_with_underscores>` and IAM database role `gi-<slug>@<project>.iam` with `PUBLIC` access revoked and per-role connection limits |
| Model keys, Git credentials, capability secrets, `request_secret` values, config-repo deploy key, break-glass `local-api-token`, and the database password when `instance_sql.cloud_sql_admin: false` (`grain-inst-<name>-db-password`) | **Google Cloud Secret Manager** (`secrets.secret_prefix`, default `grain-inst-`) | Secrets stored under `grain-inst-<name>-<item>`. The VM's service account holds conditional IAM prefix bindings (`resource.name.startsWith(".../secrets/grain-inst-<name>-")`) with `secrets.list` omitted so no VM can enumerate sibling secret names |
| Declarative `grain/config` repository (`settings/*.yml`, `tables/*.json`, `memory/**`, Git history and open config PRs) | **Per-instance configuration repository** (`config_repos.provider: github` or `none`) | Dedicated private repository (`<github_org>/<repo_prefix><name>`) accessed via a repository-scoped `ed25519` SSH deploy key stored in Secret Manager (`grain-inst-<name>-config-repo-key`) |
| Agent transcripts, CI artifacts, archived durable session bundles | **Google Cloud Storage** (`instance_state.gcs_bucket`) | Object prefix `gs://<bucket>/instances/<name>/` isolated by GCS Managed Folder / prefix IAM bindings for `gi-<slug>@` |

### Direct VPC routing and `backend_url`

The hub reverse-proxies traffic for `kind = instance` directly over
the `grain-instances` VPC to the entry's stored `backend_url`
(`http://<internal-ip>:8080` in the first version). Storing
`backend_url` as a full scheme-qualified URL and keeping caller
authentication strictly on the `X-Grain-Registry-Assertion` JWT keeps
direct VPC routing stateless across hub replicas (1 hop from any
hub pod, zero hub credentials stored on the VM) while allowing
per-instance TLS (`https://…`) to be enabled later without changing the
data model or authentication contract.

### Instance lifecycle, upgrades, resize, stop/start, and restore

Hosted instances move through a narrow state machine:

- **`creating` → `running` (or `failed`).** `POST /api/entries` records
  the entry in `creating` and returns `202 Accepted`. The leader
  reconciler provisions the per-instance service account, Cloud SQL
  database, Secret Manager and GCS bindings, config repository, and GCE
  VM, then polls `GET <backend_url>/healthz` and
  `GET <backend_url>/api/registry/status` until the daemon is healthy,
  switching the entry to `running`. While `creating`, visits to
  `https://<name>.<instance-domain>/` receive a self-contained `503`
  provisioning status page (`Retry-After: 5`).
- **Periodic status and scope polling (`15 s`).** While `running`, the
  reconciler polls `GET <backend_url>/api/registry/status` every
  `compute.status_poll_interval` (default `15 s`) to refresh
  `grain_version`, `grain_commit`, `health_state`, `active_tasks`,
  `awaiting_reply_tasks`, `last_heartbeat_at`, and self-reported
  repository scopes.
- **Upgrades, shape or disk changes, and VM recovery (`replacing`).** Upgrading
  to a new release (`grain hub upgrade [-version <vX.Y.Z>] <name>`
  or `POST /api/entries/<name>/upgrade`), changing VM shape or boot disk size
  (`grain hub resize [-shape <shape>] [-disk-gb <gb>] [-version <vX.Y.Z>] <name>`), or recovering a
  broken VM (`POST /api/entries/<name>/replace`) all execute the same
  replacement sequence (`replace_step`):
  1. `draining`: replicas serve a `503` updating page (`Retry-After: 5`)
     while the reconciler stops the old VM cleanly, giving `grain daemon`
     up to `compute.drain_timeout` to flush transcripts, artifacts and
     config syncs and release its PostgreSQL advisory lock.
  2. `deleting_vm`: the old VM and its ephemeral disks are deleted,
     preserving single-writer database safety.
  3. `creating_vm`: a new VM with the target image and shape boots under
     the same `vm_name` and service account, reattaching to the existing
     Cloud SQL database, Secret Manager prefix, GCS prefix, and config
     repo.
  4. `probing` → `running`: once `/healthz` and `/api/registry/status`
     respond on the new `backend_url`, the entry returns to `running`.

  Hosted `instance` VMs run with `-disable-self-upgrade`, so this external VM
  replacement does not use the daemon's in-place `pkg/upgrade` pipeline and
  cannot be cancelled with `DELETE /api/upgrade/current` or
  `grain upgrade cancel` (run `grain hub upgrade -version <previous> <name>`
  afterwards to move back). By contrast, `connected` entries run their own
  `grain daemon` and support per-instance upgrade cancellation, grace-window
  countdown, and one-click rollback via the Hub's proxied
  `[Settings → Upgrade](/settings/upgrade)` pane, `DELETE /api/upgrade/current`,
  or `grain -context <name> upgrade cancel`.
- **Stop and start (`stopping` → `stopped`).** An entry manager can
  park an idle instance with `grain hub stop <name>` (or
  `POST /api/entries/<name>/stop`, or **Stop** on the entry's page and
  in **My entries**). The reconciler waits up to `compute.drain_timeout`
  for active tasks to finish, then halts and deletes the VM while keeping
  the database, secrets, GCS prefix, and config repo, and the entry
  shows as `stopped` (status `offline`). `grain hub start <name>`
  (`POST /api/entries/<name>/start`, or **Start**) boots a replacement
  VM onto the same state; with `auto_wake` on (the default), a visit to
  `https://<name>.<instance-domain>/` wakes it too. Both are human-only
  and refused for `connected` entries.
- **Deletion, 7-day fenced database retention, and 30-day name hold.**
  `grain hub delete -confirm <name> <name>` (or
  `DELETE /api/entries/<name>`) transitions the instance through
  `deleting` to `deleted`:
  - The VM and scratch disks are deleted immediately.
  - Unless `-delete-disk` is requested, the reconciler **fences** the
    instance's Cloud SQL database (`ALLOW_CONNECTIONS false`, active
    backends terminated, service-account access revoked) and retains the
    database, secrets, GCS objects, and config repo until `purge_after`
    (`instance_state.db_retention_period`, default **7 days** / `168h`).
    During that window, an entry manager or admin can run
    `grain hub restore <name>` (`POST /api/entries/<name>/restore`)
    to un-fence the database and boot a replacement VM with all prior
    state intact.
  - Once `purge_after` passes, the reconciler drops the database and
    role, deletes the Secret Manager secrets, GCS prefix, config repo,
    and service account, and sets `purged_at`.
  - The entry name remains reserved for its former `owner` until
    `reserved_until` (`quotas.reservation_period`, default **30 days** /
    `720h`). During that 30-day hold, the former owner can reuse the
    name, and no other user can claim it until both `reserved_until` has
    expired and the instance has been purged.
  - Deleted entries drop out of **My entries** in the hub UI as
    soon as they are deleted. Hub admins still see them on the
    Admin page, where they can restore or purge them.

---

## 3. Connected Grains and the Outbound Tunnel (`kind = connected`)

A `connected` entry is any grain controller that the hub did not
provision -- running on a developer workstation behind a corporate
proxy, inside an inbound-blocked development environment or capsule, on
a host whose IP address changes, or on a Kubernetes cluster -- that
registers itself in the catalog, reports health and repository scopes via
heartbeats, and dials an outbound multiplexed tunnel to the hub so
`https://<name>.<instance-domain>/` routes to it with zero inbound
network ports open.

> **No redirect entries in v1.** The first version of the hub supports
> `kind = "instance"` (direct VPC proxy) and `kind = "connected"`
> (outbound tunnel). External deployments that want a `*.<instance-domain>`
> address and catalog listing connect via `-hub-connect`. (`kind`
> remains an explicit column and routing dispatches per `kind` so static
> redirect or link-only entries can be added later without changing
> ownership, reservations, or the catalog model.)

### Authenticating a `connected` grain to the hub

A `connected` grain authenticates both its periodic heartbeats
(`PUT /api/entries/<name>/heartbeat`) and its outbound tunnel
(`GET /api/entries/<name>/connect`) using one of two credential modes
(`-hub-auth-mode`):

1. **Per-entry registration token (`-hub-auth-mode=token`, default
   when a token is minted).** When a `connected` entry is created or its
   credential is rotated, the hub mints a `grc_<256-bit-base64url>`
   token, returns the plaintext once, and stores only `SHA-256(token)`
   and a 4-character hint in `entry_credentials`.
   - On the grain, the token lives in the secret store under
     `registry-registration-token` (or is read from
     `-hub-token-file` or `$GRAIN_HUB_TOKEN`; never stored in
     `grain/config`).
   - Heartbeat and tunnel requests send `X-Grain-Auth: token` and
     `X-Grain-Registry-Token: grc_…`, which the Gateway routes to the
     `grain-registry-public` listener (`:8081`) outside IAP so machines
     outside GCP need no cloud OIDC credential.
   - Each `grc_…` token is scoped strictly to `<name>`'s heartbeat and
     `<name>`'s `/connect` endpoint; it cannot mutate ownership, change
     `listed`, or read `/api/directory` or other entries.
   - Rotate a token at any time with an optional grace window:
     ```bash
     grain hub rotate-credential \
       -hub-url https://grain.example.com \
       -overlap 1h \
       my-workstation
     ```
     Passing `-revoke-previous` (or revoking a credential ID via
     `DELETE /api/entries/<name>/credentials/<id>`) immediately closes
     any active tunnel session that authenticated with the revoked
     credential.
2. **OIDC / `FrontIdentity` (`-hub-auth-mode=oidc`).** When the
   grain daemon runs under a workload identity or user credential that
   traverses the hub's `FrontIdentity` edge -- matching the entry's
   `connected_principal` (for example
   `serviceAccount:grain@team-proj.iam.gserviceaccount.com`), `owner`,
   or a verified member of `owner_group` -- it mints OIDC ID tokens for
   `-hub-oidc-audience` (defaulting to `-hub-url`) and
   connects with zero stored `grc_…` secret.

### Registering a `connected` grain

From a machine running a local `grain daemon`, `grain hub register`
reserves the entry on the hub, stores the minted `grc_…` token in
the local daemon's secret store (`registry-registration-token`), updates
the daemon's stored **Settings → Hub** configuration, and triggers
an immediate heartbeat:

```bash
grain hub register my-workstation \
  -hub-url https://grain.example.com \
  -display-name "Alice's Workstation" \
  -purpose "Kernel driver experiments and local hardware tests" \
  -host-kind workstation \
  -owner-group group:kernel-team@example.com \
  -listed
```

To reserve an entry and mint a token *without* contacting a local daemon
(for example, before deploying a container or Kubernetes pod), pass
`-standalone` (and `-token-file` to write the token directly to a `0600`
file):

```bash
grain hub register my-k8s-grain \
  -hub-url https://grain.example.com \
  -host-kind kubernetes \
  -connected-principal serviceAccount:grain@team-proj.iam.gserviceaccount.com \
  -mint-token \
  -token-file /etc/grain/registry-token \
  -standalone
```

Each `connected` entry counts against the owner's `max_connected` cap
(default `20`), consuming zero VM instance quota units. To remove the
registration, run `grain hub unregister [<entry>]` (pass
`-keep-remote` to clear the local daemon's settings without deleting the
hub entry).

**Metadata and `host_kind` precedence.** When `grain hub register`
connects a local daemon to an existing entry (for example, one reserved
earlier with `-standalone`), catalog fields follow a strict precedence
order:

1. **Explicit CLI flags** passed on that `grain hub register` or
   `grain hub update` invocation (`-display-name`, `-purpose`,
   `-host-kind`, `-owner-group`, `-listed`, `-tags`, `-scope`).
2. **Explicitly configured local daemon `Registry` settings** already set
   for the same `(registry_url, entry)`.
3. **Existing remote hub entry values** (`GET /api/entries/<name>`),
   which are preserved and adopted into the local daemon's settings
   whenever the corresponding CLI flags are omitted.
4. **Daemon runtime host inference** (`workstation`, `kubernetes`, or
   `other`), used only as a fallback when creating a brand-new entry with
   no explicit `-host-kind`. Periodic heartbeats and OIDC metadata syncs
   send `host_kind` only when explicitly configured on the daemon, so a
   CLI-declared `-host-kind` is never overwritten by background heartbeats.

### Outbound tunnel (`-hub-connect`)

To route `https://<name>.<instance-domain>/` to a `connected` grain,
start `grain daemon` with `-hub-connect` (or
`GRAIN_HUB_CONNECT=true`) alongside the three `-ui-hub-*`
verifier flags:

```bash
grain daemon \
  -hub-url https://grain.example.com \
  -hub-entry my-workstation \
  -hub-connect \
  -hub-token-file /etc/grain/registry.token \
  -ui-hub-issuer grain-registry \
  -ui-hub-audience https://my-workstation.grain-user.com \
  -ui-hub-keys-url https://grain.example.com/.well-known/jwks.json \
  -ui-require-verifier=registry
```

How the outbound tunnel operates:

1. **WebSocket upgrade and stream multiplexing.** The daemon's
   `HubTunnelSupervisor` dials
   `wss://<registry-host>/api/entries/<name>/connect` with
   `Sec-WebSocket-Protocol: grain.registry.connect.v1` and
   `X-Grain-Entry: <name>` (`pkg/tunnel/wsupgrade`), then runs a
   multiplexed stream session (`pkg/tunnel/mux`) with 15-second
   keepalive pings and per-stream 1 MiB receive windows.
2. **One-way stream initiation (`MaxPeerInitiatedStreams = 0`).** Only
   the hub opens streams (odd stream IDs) to forward incoming HTTP,
   SSE, and upgraded WebSocket or `/api/hostd/connect` requests to the
   grain. Any stream frame initiated by the grain (even stream ID) is
   immediately reset with `FrameReset`, so a compromised connected host
   can never open connections back into the hub pod, Cloud SQL, or
   the `grain-instances` VPC.
3. **In-process `MuxListener` and mandatory verifier gate.** On the
   grain, accepted `mux` streams are served in-process over a
   `MuxListener` whose `RemoteAddr()` is `stream-<id>-remote` (network
   `"mux"`, never `127.0.0.1`), wrapped in `HubTunnelHandler`.
   Every request arriving over the tunnel **must** carry a valid
   `X-Grain-Registry-Assertion` JWT; `grain_access` cookies,
   `X-Grain-Access-Code`, and bearer access codes are unconditionally
   rejected on tunnel streams. A developer workstation can therefore
   bind its local TCP listener to `127.0.0.1:4242` for local CLI use
   while guaranteeing that traffic arriving from the hub tunnel is
   always authenticated by a verified hub JWT. Starting
   `-hub-connect` without `-ui-hub-issuer`,
   `-ui-hub-audience`, and `-ui-hub-keys-url` fails closed at
   startup.
4. **Multi-replica HA and `:8082` peer forwarding.** When a tunnel terminates
   on hub replica `B`, replica `B` records a monotonically
   increasing fencing token (`tunnel_sessions.session_id`) and its
   cluster-internal peer address (`<pod-ip>:8082`) in PostgreSQL and
   broadcasts `NOTIFY registry_tunnels`. If a visitor request for
   `<name>.<instance-domain>` lands on replica `A`, replica `A` performs
   edge verification, CSRF checks, header stripping, and
   `X-Grain-Registry-Assertion` signing first, then forwards the request
   over cluster-internal HTTP (`http://<peer-ip>:8082/internal/tunnel/<name>`)
   with a short-lived `X-Registry-Peer-Auth` token
   (`aud = "urn:grain:registry:peer"`) and `X-Registry-Hop: 1`.
5. **Graceful drains, `2 s` reconnect grace, and 23-hour rotation.**
   During a rolling hub restart (`SIGTERM`), draining pods mark
   `/healthz` not-ready and close idle tunnels staggered over `0..5 s`
   so connected grains redial onto healthy pods within `250 ms ± 25%`.
   Incoming requests arriving within `5 s` of a disconnect wait up to
   `tunnel.reconnect_grace` (default `2 s`, up to 32 waiters per entry)
   for the redial to complete before returning `503`. Connected grains
   also proactively dial a replacement tunnel at `23 h ± 30 m` and drain
   the old session (`2 m`) to avoid 24-hour load-balancer connection
   drops.
6. **Offline `503` page.** Whenever a `connected` grain has no active
   tunnel session, its derived catalog status is immediately `offline`
   and `https://<name>.<instance-domain>/` answers `503 Service
   Unavailable` (`Retry-After: 10`) naming the entry's `owner`,
   `owner_group`, and `last_seen_at`.

### Periodic heartbeats (`PUT /api/entries/<name>/heartbeat`)

Alongside the tunnel, `HubManager` pushes a JSON heartbeat every
`catalog.heartbeat_interval` (default `15 s`, and immediately whenever
configured repositories or `WorkOwnership` settings change):

```json
{
  "grain_version": "v1.4.0",
  "grain_commit": "215a1150f3c9",
  "host_kind": "workstation",
  "health": "ok",
  "active_tasks": 3,
  "awaiting_reply_tasks": 1,
  "scopes": [
    {"repo": "acme/autoscaler", "path_prefix": "", "role": "primary"},
    {"repo": "acme/cluster-api", "path_prefix": "", "role": "secondary"}
  ]
}
```

The hub updates the entry's health, task counts, build stamps, and
`reported` scopes, and responds with
`{status, next_heartbeat_seconds, scope_conflicts, environment_name}`
-- the last being the name the grain shows itself under by default
([What an instance is called](#what-an-instance-is-called)). Check a grain's
live registration state, last heartbeat timestamp, and any scope
conflicts from the grain itself with:

```bash
grain hub status
```

### Derived liveness `status`

Both hosted `instance` entries (via 15 s reconciler status polls) and
`connected` entries (via 15 s heartbeats plus live `tunnel_sessions`
state) expose the same three-value derived `status`:

| Condition | Derived `status` |
| --- | --- |
| `state == "running"`, `health == "ok"`, heartbeat age `<= 45 s` (`degraded_after`), and (for `connected`) an active tunnel session | `online` |
| `state == "running"` (with active tunnel for `connected`), and either `health == "degraded"` or heartbeat age between `45 s` and `90 s` (`offline_after`) | `degraded` |
| `state` is `creating` or `replacing` | `degraded` |
| Heartbeat age `> 90 s`, `last_heartbeat_at` is `NULL`, `connected` tunnel is disconnected, or `state` is `failed`, `deleting`, or `deleted` | `offline` |

---

## 4. Catalog Metadata and Repository Scopes

Every entry (`instance` and `connected`) carries catalog metadata and
repository scopes used by the directory and work router.

### Catalog fields

- **`display_name`** (up to 120 characters; falls back to `name` when
  empty): human-readable title shown in directory listings.
- **`purpose`** (up to 500 characters): one or two plain sentences
  describing what work belongs on this grain (for example,
  *"Autoscaler control-loop heuristics and scale-down simulations"*).
- **`host_kind`**: `"registry-vm"` (pinned automatically for
  `kind = "instance"` and forbidden on `connected` entries),
  `"workstation"`, `"kubernetes"`, or `"other"`.
- **`tags`**: up to 16 normalized lowercase `[a-z0-9][a-z0-9-]{0,31}`
  labels for filtering in `/api/directory`.
- **`listed`**: controls catalog visibility. Newly created hosted
  instances default to `listed = false` so personal and experimental
  grains stay visible **only** to their `owner`, verified `owner_group`
  members, and hub `admins`. Setting `listed = true` publishes the
  entry into `GET /api/directory` and `GET /api/route`.

### What an instance is called

A grain the hub knows about names itself after its entry: its
**environment name** -- the browser tab title and the sidebar badge in
its own UI, and the name in Settings -- defaults to the entry's
`display_name`, or to its `name` when the entry has no display name.
That makes it obvious, with several hub grains open in tabs, which one
you are looking at.

- **Hosted instances.** The reconciler puts the name in the VM's
  `grain-config` metadata as `environment_name`;
  `scripts/grain-instance-init.sh` writes it to `daemon.env` as
  `GRAIN_DEFAULT_ENVIRONMENT_NAME`, which the daemon reads as
  `-default-environment-name`.
- **Connected grains.** The heartbeat reply carries it as
  `environment_name`, and the daemon applies it on every heartbeat.

It is a default only. The daemon records the name the hub gave it
(`grain_config.hub_environment_name`), and adopts a new one from the hub
only while its environment name is empty or still that hub-given name. A
name the operator sets in **Settings** (or in the state repository's
`tables/grain_config.json`) is anything else, and the hub never
overrides it. Clearing the name in Settings hands it back to the hub,
which fills it in again.

Changing the entry's `display_name` therefore
renames the grain unless its operator has named it themselves:

- a **connected** grain picks the new name up on its next heartbeat;
- a **hosted** instance picks it up the next time the hub builds its VM
  (an upgrade, resize or replace). `grain-config` is written only when a
  VM is created, so a running VM keeps the name it booted with until
  then.

The name is cut down to what the daemon's Settings accept rather than
failing provisioning. Line breaks, tabs and other control characters
become spaces, runs of spaces collapse, and the result is cut to 32
characters. For hosted instances, the characters a `daemon.env` line
cannot carry unchanged to both systemd and docker (`"`, `'`, `\`, `$`
and `` ` ``) are dropped.

### Repository scopes (`repo`, `path_prefix`, `role`)

A scope claims responsibility for repository `<owner>/<name>` (normalized
to lowercase) under a normalized repo-relative directory prefix
`path_prefix`:

- `path_prefix: ""` covers the entire repository (displayed as `/` in CLI
  tables).
- A non-empty `path_prefix` (for example `"pkg/autoscaler"` or
  `"services/billing"`, with no leading or trailing `/` and no `..`
  segments) covers that directory subtree. Matching operates strictly on
  **path-segment boundaries**: `"pkg/auth"` matches `"pkg/auth"` and
  `"pkg/auth/jwt.go"`, never `"pkg/autoscaler/loop.go"`.
- `role` is either:
  - **`primary`**: the grain that owns standing and default routed work
    for `(repo, path_prefix)`.
  - **`secondary`**: a grain that works on `(repo, path_prefix)` for
    manual, experimental, or fallback tasks while another deployment
    owns standing work.

### How self-reported (`reported`) and registry-declared (`declared`) scopes combine

To prevent stale directory entries and avoid maintaining repository lists
in two places, the hub combines scopes self-reported by the running
grain (`source = "reported"`) with optional overrides declared in the
hub (`source = "declared"`):

1. **What a grain self-reports (`source = "reported"`).** For every
   repository configured on the grain **excluding** its own
   `grain/config` state repository, the grain reports
   `(repo, path_prefix: "", role)`:
   - `role = "primary"` when the repository's work ownership default
     (`RepoConfig.Work.Default`) is unset (`""`) or `"on"`;
   - `role = "secondary"` when `RepoConfig.Work.Default` is `"off"`.
2. **Before the grain's first report (`has_reported_scopes = false`).**
   On a newly created entry before its first heartbeat or reconciler
   status poll, any `declared` scopes set on the entry are used as its
   effective scopes.
3. **Once the grain has reported (`has_reported_scopes = true`).** The
   grain's `reported` set is authoritative for **which repositories the
   grain serves** and **the maximum `role` (`primary` > `secondary`) on
   each repository**:
   - If an entry has no `declared` rows for a reported `repo`, the
     reported whole-repo scope is effective verbatim.
   - If an entry manager has configured `declared` rows for a reported
     `repo` -- for example, narrowing whole-repo `""` to
     `"services/billing"` in a shared monorepo, or demoting a repository
     from `primary` to `secondary` -- those `declared` rows replace the
     broader `reported` row (`inactive_reason = "overridden_by_narrower"`),
     with each row's effective role capped at
     `min(reported.role, declared.role)`. Declared overrides can
     **narrow** `path_prefix` or **demote** `primary` → `secondary`,
     never promote a repository that the grain reports as `secondary`
     (`Work.Default = "off"`) to `primary`.
   - Any `declared` row naming a repository that is not in the grain's
     latest report is marked `active = false`
     (`inactive_reason = "not_reported_by_instance"`) and omitted from
     `/api/directory` and `/api/route` until that repository is
     configured on the grain.

Inspect or set `declared` scope overrides with `grain hub scopes`:

```bash
# Inspect effective, reported, and declared scopes (plus any conflicts):
grain hub scopes -hub-url https://grain.example.com autoscaler

# Narrow a monorepo to two subdirectories and mark a second repo secondary:
grain hub scopes \
  -hub-url https://grain.example.com \
  -set acme/monorepo:services/autoscaler:primary \
  -set acme/monorepo:pkg/scale:primary \
  -set acme/cluster-api::secondary \
  autoscaler

# Clear declared overrides and revert to the grain's self-reported scopes:
grain hub scopes -hub-url https://grain.example.com -clear autoscaler
```

### Primary-prefix overlap and conflict resolution

Across all non-deleted entries with **`listed = true`**, two `primary`
scopes in the same `repo` **overlap** whenever one `path_prefix` equals
or is a path-segment ancestor of the other (`a == b`, `a == ""`,
`b == ""`, `b` starts with `a + "/"`, or `a` starts with `b + "/"`).

- Disallowing ancestor/descendant `primary` overlap prevents a listed
  entry from silently hijacking a subdirectory of a repository whose
  whole-repo (`""`) primary scope already belongs to another listed
  grain.
- In a monorepo shared across teams, give each team's listed grain a
  disjoint `primary` prefix (`"services/billing"`, `"services/auth"`)
  and give any repo-wide catch-all grain `path_prefix: ""` with
  `role: "secondary"`.
- Unlisted entries (`listed = false`) and `secondary` scopes never
  reserve or conflict with primary prefixes.

Overlap is enforced in two ways:

1. **Synchronous hub API/CLI writes (`409 Conflict`).** Creating a
   listed entry (`POST /api/entries` with `listed = true`), flipping an
   entry from unlisted to listed (`PATCH /api/entries/<name>`), or
   updating declared scopes (`PUT /api/entries/<name>/scopes`) on a
   listed entry fails with `409 Conflict` when an active `primary` scope
   overlaps another listed entry's active `primary` scope. The error
   response names the conflicting entry, its `owner` / `owner_group`,
   and the overlapping `path_prefix`.
2. **Asynchronous heartbeats and status polls (first-registered wins).**
   Background heartbeats (`PUT /api/entries/<name>/heartbeat`) and
   reconciler polls never fail on a scope collision (`200 OK`) so
   liveness tracking is never disrupted. Instead, the incumbent holder
   (earliest `claimed_at`) keeps `active = true`, and the newcomer's
   colliding scope is recorded with `active = false`,
   `inactive_reason = "primary_conflict"`, and
   `conflict_with = "<incumbent-name>"`. The conflict is returned in
   `HeartbeatResponse.scope_conflicts`, surfaced in
   `grain hub status` and `grain hub show`, and excluded from
   `/api/directory` and `/api/route`. When the incumbent deletes,
   unlists, or drops that primary scope, the hub automatically
   promotes the earliest non-overlapping contender.

### Who may claim a `primary` scope (`catalog.scope_owners`)

Overlap detection prevents two listed entries from claiming overlapping
`primary` prefixes, and unlisted entries (`listed = false`) never appear in
`/api/directory` or `/api/route` (if an unlisted entry stages a whole-repo
`primary` scope while another listed entry already holds a subdirectory on
that repository, flipping the unlisted entry to `listed = true` fails with
`409 Conflict`). However, overlap detection alone does not decide *which*
admitted users or teams are allowed to claim an unclaimed repository or a
disjoint `primary` prefix (for example claiming `acme/app` or
`acme/app:services/web` while another team's entry holds
`acme/app:services/api`, which would otherwise route paths outside
`services/api` to the second entry).

`grain-registry` supports both single-team self-service fleets and
multi-team / multi-domain fleets via `catalog.scope_owners`:

1. **Default self-service mode (`catalog.scope_owners` omitted).** In a
   single-team or single-organization hub where every admitted user in
   `auth.allowed_domains` is trusted to register team grains without manual
   approval, any entry manager can claim non-overlapping `primary` scopes
   (first-registered wins across listed entries, with admin override via
   `POST /api/admin/conflicts/resolve`). This preserves zero-ceremony
   self-registration from `RepoConfig` heartbeats without requiring GitHub
   admin tokens on the registry.
2. **Per-repository / per-organization ownership verification
   (`catalog.scope_owners`).** Multi-team or multi-domain hubs can
   bind repository patterns (`<owner>/<repo>` or `<prefix>/*`) to allowed
   `user:<email>` and `group:<email>` principals. When multiple rules could
   match a repository, an **exact `<owner>/<repo>` rule overrides the
   longest matching `<prefix>/*` wildcard rule**; repositories matching no
   rule remain self-service. A rule written in the bare `<owner>/...` form
   also governs host-qualified claims of the same path (`acme/*` covers
   `github.com/acme/billing` as well as `acme/billing`), since scopes and
   `/api/route` queries may spell a repository either way; a rule naming
   the host-qualified form (`github.com/acme/*`) is consulted first for that
   host.
   - An entry is authorized to hold a `primary` scope (`declared` or
     `reported`) on a restricted repository if and only if:
     1. `user:<entry.owner>` is listed in the matching rule's `principals`,
     2. `entry.owner_group` is non-empty and listed in `principals`, or
     3. `entry.owner` is a verified member of a `group:<email>` listed in
        `principals` via `GroupResolver`.
   - **Synchronous API writes and transfers fail closed:** unauthorized
     `primary` claims on `POST /api/entries`,
     `PUT /api/entries/<name>/scopes`, `PATCH /api/entries/<name>` (when
     changing `owner_group` or setting `listed = true`),
     `POST /api/entries/<name>/transfer`, and
     `POST /api/entries/<name>/transfer/accept` are rejected with
     `403 Forbidden` (`code: "scope_owner_forbidden"`). If `GroupResolver`
     is temporarily unavailable while checking a `group:` rule, synchronous
     writes fail closed with `503 Service Unavailable` (`Retry-After: 5`).
     `secondary` scopes remain allowed for any admitted entry manager.
   - **Heartbeats and admin force-transfers demote rather than fail:**
     asynchronous `PUT /api/entries/<name>/heartbeat` reports never fail
     liveness on an unauthorized `primary` scope (`200 OK`); instead,
     unauthorized reported `primary` claims are stored as `secondary`
     (`role = "secondary"`). Likewise, an admin force-transfer
     (`POST /api/admin/entries/<name>/force-transfer`) to a recipient
     outside a repo's `scope_owners` demotes affected declared `primary`
     scopes on that entry to `secondary`.
   - **Existing claims are re-checked at startup:** primaries stored before
     a rule applied (the policy newly added, or a rule tightened) are not
     grandfathered. Each hub replica re-evaluates every entry's
     `primary` scopes when it starts and demotes the unauthorized ones to
     `secondary` (audited as `entry_scope_owner_demote`). A `GroupResolver`
     error during that sweep leaves the claim as it is rather than demoting
     it on a transient outage.

---

## 5. Accountable Ownership, `owner_group`, Quotas, and Transfers

### Individual `owner` and team `owner_group`

Every entry has a single accountable human **`owner`** (`user:<email>`)
and an optional **`owner_group`** (`group:<email>`), resolved through
 the hub's `GroupResolver` (Cloud Identity or static group config,
cached for 5 minutes; if the group directory is unreachable, group
checks fail closed with `503` + `Retry-After: 5` while direct `owner`
and `admin` checks continue working).

- **Co-management by `owner_group` (`entry manager`).** Verified members
  of `owner_group` can manage the entry just like `owner`:
  - view the entry (including when `listed = false`) in
    `GET /api/entries`, `GET /api/entries/<name>`, `grain hub list`,
    and `grain hub show`, as well as detailed `502`/`503` recovery
    pages on `<name>.<instance-domain>`;
  - update catalog metadata (`display_name`, `purpose`, `host_kind`,
    `tags`, `listed`) and `declared` scopes;
  - trigger `upgrade`, `resize`, `replace`, `delete`, and `restore`
    during the retention window;
  - edit `connected_principal`, rotate or revoke `grc_…` registration
    credentials, and dial `/api/entries/<name>/connect`;
  - initiate a two-step ownership transfer.
- **Who may set or change `owner_group`.** Attaching or changing
  `owner_group` requires `owner` (who must belong to the target group,
  unless an admin), an existing `owner_group` member who is also a
  member of the new target group, or a hub admin. This prevents a
  user from attaching an unrelated team's group to an entry.

### Personal quota attribution

Even when `owner_group` is set, quota is **always charged solely to the
individual `entries.owner`**, never pooled across or divided among
`owner_group`:

- **`max_instance_units`** (default `2` weighted VM units per owner):
  sum of `shape.quota_units` across the owner's non-deleted `instance`
  entries (during a resize to a larger shape, the positive delta is
  reserved up front; during a shrink or delete, units return once the
  transition completes or the instance enters `deleted`).
- **`max_connected`** (default `20` connected entries per owner): count
  of the owner's non-deleted `connected` entries.

When an `owner_group` co-manager deletes or shrinks an entry, the freed
quota returns to `entries.owner`. When an `owner_group` co-manager
resizes an instance upward or restores a deleted instance, the hub
checks `entries.owner`'s available quota and records both `actor` and
`quota_owner` in `audit_log`. If `entries.owner` has no free quota, a
group peer can transfer `owner` to themselves first. Hub admins can
configure per-user or per-group quota overrides in `quotas.overrides` or
via the admin API.

### Two-step ownership transfer

Either `owner` **or** a verified member of `owner_group` can offer an
ownership transfer to another user (`POST /api/entries/<name>/transfer`),
so a team is never blocked when an accountable owner is away:

```bash
# Offer transfer to bob@example.com (keeping owner_group unless -clear-owner-group is set):
grain hub transfer \
  -hub-url https://grain.example.com \
  -to bob@example.com \
  autoscaler
```

**Only the named human recipient** (`bob@example.com`) -- never a group
peer acting on their behalf -- can accept the transfer within 7 days
(`POST /api/entries/<name>/transfer/accept`), because accepting charges
the entry against the recipient's personal quota and makes them the
accountable `owner`:

```bash
# Run as bob@example.com:
grain hub accept-transfer \
  -hub-url https://grain.example.com \
  autoscaler
```

Across an intra-team transfer, `owner_group` is preserved by default
unless `-clear-owner-group` or a new `-owner-group` is passed. Accepting
a transfer atomically moves the quota charge from the old owner to the
new owner, updates break-glass SSH IAM bindings on hosted VMs, and closes
any active outbound tunnel authenticated under the previous owner's
identity so the connected grain re-authenticates cleanly.

---

## 6. Catalog Discovery and Work Routing (`/api/directory` and `/api/route`)

Both catalog endpoints are served on `https://<registry-host>/api/` from
each replica's in-memory `catalogCache` (kept fresh via PostgreSQL
`LISTEN registry_catalog` with a 3-second fallback poll) and admit:

- any verified human caller in `auth.allowed_domains` (not disabled), and
- any verified service principal (`Kind == "service"`) permitted by
  `DelegationPolicy.MayReadCatalog` (configured in
  `auth.service_principals`, such as a planning board, issue triager, or
  chat bot service account). Service principals hold **catalog-read
  access only**; control-plane endpoints (`GET /api/entries`,
  mutations, admin routes) reject them with `403 Forbidden`.

Only entries with **`listed = true` and `state != "deleted"`** appear in
`/api/directory` and `/api/route`.

### `GET /api/directory`

`GET /api/directory` searches listed grains across the organization. All
supplied filters are combined with `AND`:

| Query parameter | CLI flag (`grain hub directory`) | Matching rule |
| --- | --- | --- |
| `q` | `-q <text>` (or positional `<query>`) | Case-insensitive substring/token match across `name`, `display_name`, `purpose`, `owner`, `owner_group`, `tags`, and active `scopes` (`repo` and `path_prefix`) |
| `repo` | `-repo <owner/name>` | Entries with at least one `active = true` scope for normalized `<owner>/<name>` |
| `tag` | `-tag <tag>` | Entries whose `tags` include every requested tag (repeatable or comma-separated) |
| `status` | `-status <online\|degraded\|offline>` | Filter by derived liveness status (comma-separated for multiple) |
| `kind` | `-kind <instance\|connected>` | Filter by entry kind |
| `host_kind` | `-host-kind <registry-vm\|workstation\|kubernetes\|other>` | Filter by host kind |
| `owner` | `-owner <email>` | Matches `owner` or `owner_group` |
| `limit` | `-limit <n>` | Maximum number of entries to return |

Results sort by derived `status` (`online` first, then `degraded`, then
`offline`) and then `name ASC`. Each entry in the response includes only
public catalog fields (`name`, `display_name`, `url`, `kind`,
`host_kind`, `owner`, `owner_group`, `purpose`, `tags`, active `scopes`,
`status`, `state`, `heartbeat_age_seconds`, `grain_version`,
`grain_commit`, `active_tasks`, `awaiting_reply_tasks`) -- never internal
infrastructure columns (`vm_name`, `backend_url`, `sql_instance`,
`db_name`, `service_account`, `failure_reason`).

Example CLI and HTTP usage:

```bash
grain hub directory \
  -hub-url https://grain.example.com \
  -repo acme/autoscaler \
  -status online

grain --json registry directory -hub-url https://grain.example.com -q autoscaler
```

### `GET /api/route`

`GET /api/route?repo=<owner/name>&path=<path>&include_offline=<bool>`
answers *"which grain should receive work for this repository and
path?"*:

| Query parameter | CLI flag (`grain hub route`) | Description |
| --- | --- | --- |
| `repo` *(required)* | `-repo <owner/name>` (or 1st positional arg) | Target repository `<owner>/<name>`, normalized to lowercase (`400` if missing or invalid) |
| `path` *(optional)* | `-path <subpath>` (or 2nd positional arg) | Repo-relative directory or file path (default `""`; leading/trailing `/` trimmed; `400` if it contains `..` traversal segments) |
| `include_offline` *(optional)* | `-include-offline` | Include offline secondary candidates when inspecting all matching grains |

How `/api/route` matches and ranks candidates:

1. **Path-segment boundary match.** Select every listed, non-deleted
   entry with at least one `active = true` (unconflicted) scope for
   `repo` where `path_prefix == ""` or `path == path_prefix` or `path`
   starts with `path_prefix + "/"`. If one entry has multiple matching
   scopes in `repo`, keep its strongest match (`primary` over
   `secondary`, then longest `path_prefix`).
2. **Candidate ranking order:**
   1. **Role:** `primary` candidates always rank ahead of `secondary`
      candidates.
   2. **Specificity:** longest matching `path_prefix` first
      (`len(path_prefix) DESC`, where whole-repo `""` has length `0`).
   3. **Liveness and load tie-breaker:** `online` > `degraded` >
      `offline`, then lowest `active_tasks`, then `name ASC`.
3. **Offline primaries stay visible in rank 1, and `primary` is strictly
   role-gated.** Because active `primary` prefixes across listed entries
   are mutually disjoint, at most one `primary` entry matches any
   `(repo, path_prefix)` specificity level. If that primary grain is
   currently `offline` or `degraded` (for example, a VM mid-upgrade or a
   disconnected workstation), it remains in rank 1 with
   `status: "offline"` / `"degraded"` and its `heartbeat_age_seconds`,
   `owner`, `owner_group`, and `url` ahead of any `online` `secondary`
   candidates. A caller can therefore tell the user *"primary grain
   `autoscaler` (`owner: alice@example.com`) is offline; wait or route to
   secondary `core-fallback` (`online`)"* rather than silently dispatching
   standing work to the wrong grain. The top-level `primary` field in
   `RouteResponse` is populated **only** when a `role: "primary"` candidate
   matches; when only `secondary` entries match `(repo, path)`, `primary`
   is `null` (omitted) and those entries appear only in `candidates`.

#### Route consumer authorization

`GET /api/route` is a **catalog discovery endpoint, not an authorization
grant**. Route consumers MUST pair `/api/route` with caller/target
authorization appropriate to how they dispatch work:

- **Interactive users and delegated apps (`X-Grain-On-Behalf-Of`):** the
  hub refuses to mint a delegated `act` assertion unless the signed-in
  person holds an active consent grant (`delegation_grants.entries`)
  covering the specific target entry (`403 no_grant`), and the target grain
  independently enforces its own `allowed_principal` and `delegate:` rules.
  An untrusted listed entry therefore cannot silently receive a person's
  delegated credentials.
- **Automated non-delegated service principals (`serviceAccount:…`):**
  planners, webhook routers, or bots that dispatch work directly as a
  service principal or external agent token without per-entry human consent
  MUST NOT blindly send sensitive repository work or secrets to an
  arbitrary route result unless **either** (a) the hub enforces
  `catalog.scope_owners` for those repositories, **or** (b) the caller
  checks `matched_scope.role == "primary"` and verifies the candidate's
  `owner` / `owner_group` against its own trusted allowlist before
  dispatching.

Example CLI and HTTP usage:

```bash
grain hub route \
  -hub-url https://grain.example.com \
  acme/monorepo services/autoscaler/loop.go

grain --json registry route \
  -hub-url https://grain.example.com \
  -repo acme/monorepo \
  -path services/autoscaler/loop.go \
  -include-offline
```

Example JSON response from `GET /api/route?repo=acme/monorepo&path=services/autoscaler/loop.go`:

```json
{
  "repo": "acme/monorepo",
  "path": "services/autoscaler/loop.go",
  "primary": {
    "name": "autoscaler",
    "display_name": "Autoscaler",
    "url": "https://autoscaler.grain-user.com",
    "kind": "instance",
    "host_kind": "registry-vm",
    "owner": "alice@example.com",
    "owner_group": "group:autoscaler-eng@example.com",
    "purpose": "Autoscaler control-loop heuristics and scale-down simulations",
    "status": "online",
    "state": "running",
    "heartbeat_age_seconds": 6,
    "active_tasks": 2,
    "awaiting_reply_tasks": 0,
    "matched_scope": {
      "repo": "acme/monorepo",
      "path_prefix": "services/autoscaler",
      "role": "primary",
      "source": "declared"
    }
  },
  "candidates": [
    {
      "name": "autoscaler",
      "display_name": "Autoscaler",
      "url": "https://autoscaler.grain-user.com",
      "kind": "instance",
      "host_kind": "registry-vm",
      "owner": "alice@example.com",
      "owner_group": "group:autoscaler-eng@example.com",
      "purpose": "Autoscaler control-loop heuristics and scale-down simulations",
      "status": "online",
      "state": "running",
      "heartbeat_age_seconds": 6,
      "active_tasks": 2,
      "awaiting_reply_tasks": 0,
      "matched_scope": {
        "repo": "acme/monorepo",
        "path_prefix": "services/autoscaler",
        "role": "primary",
        "source": "declared"
      }
    },
    {
      "name": "monorepo-triage",
      "display_name": "Monorepo Catch-All",
      "url": "https://monorepo-triage.grain-user.com",
      "kind": "connected",
      "host_kind": "kubernetes",
      "owner": "bob@example.com",
      "owner_group": "group:platform-eng@example.com",
      "purpose": "Cross-cutting triage and fallback tasks for acme/monorepo",
      "status": "online",
      "state": "running",
      "heartbeat_age_seconds": 11,
      "active_tasks": 1,
      "awaiting_reply_tasks": 0,
      "matched_scope": {
        "repo": "acme/monorepo",
        "path_prefix": "",
        "role": "secondary",
        "source": "reported"
      }
    }
  ]
}
```

---

## 7. CLI Reference (`grain hub`)

`grain hub` authenticates to `-hub-url` (or
`$GRAIN_HUB_URL`, falling back to the local daemon's configured
`Registry.URL`) using `-hub-token` (`$GRAIN_HUB_TOKEN`) or an
OIDC ID token minted for `-oidc-audience` (defaulting to
`-hub-url`). Pass `--json` before `hub` (`grain --json hub …`)
for machine-readable JSON output on every subcommand.

| Command | Purpose |
| --- | --- |
| `grain hub status` | Show the local daemon's connected-entry registration state, hub URL, last heartbeat, active/awaiting task counts, advertised scopes, and any `scope_conflicts` |
| `grain hub directory [flags] [<query>]` | Search listed grains in `GET /api/directory` (`-q`, `-repo`, `-tag`, `-status`, `-kind`, `-host-kind`, `-owner`, `-limit`) |
| `grain hub route [flags] [<repo> [<path>]]` | Resolve ranked candidate grains for a repository and path via `GET /api/route` (`-repo`, `-path`, `-include-offline`) |
| `grain hub list [flags]` | List entries owned by you or your `owner_group` (`-all` for admins; `-owner`, `-owner-group`, `-kind`, `-status`, `-listed`, `-conflict`, `-include-deleted`) |
| `grain hub show <name>` | Show full entry details, tunnel connection state, version, `reported` and `declared` scopes, `scope_conflicts`, and credential metadata |
| `grain hub create [flags] [<name>]` | Provision a hosted `instance` VM (`-name`, `-shape`, `-disk-gb`, `-zone`, `-display-name`, `-purpose`, `-owner-group`, `-listed`, `-tags`, `-allow-ssh`, `-ship-logs`, `-scope`) |
| `grain hub register [flags] [<entry>]` | Reserve or update a `connected` entry, configure the local daemon (unless `-standalone`), and optionally mint/rotate a `grc_…` token (`-mint-token`, `-rotate-token`, `-oidc`, `-token-file`) |
| `grain hub unregister [flags] [<entry>]` | Unregister a `connected` entry from the hub (unless `-keep-remote`) and clear the local daemon's hub settings and stored token |
| `grain hub update [flags] <name>` | Edit catalog metadata, `owner_group`, `listed`/`unlisted`, `tags`, `host_kind`, `connected_principal`, `shape`, `allow_ssh`, or `ship_logs` |
| `grain hub scopes [-set <repo[:prefix[:role]]>]... [-clear] <name>` | View or replace `declared` repository scope overrides on `<name>` |
| `grain hub rotate-credential [flags] <name>` | Mint a replacement `grc_…` token for a `connected` entry (`-overlap`, `-revoke-previous`, `-cred-id`, `-label`, `-token-file`) |
| `grain hub upgrade [-version <vX.Y.Z>] <name>` | Replace a hosted `instance` VM onto the target (or current default) release image (`connected` entries upgrade in place via `grain -context <name> upgrade` and cancel via `grain -context <name> upgrade cancel`) |
| `grain hub resize [-shape <shape>] [-disk-gb <gb>] [-version <vX.Y.Z>] <name>` | Replace a hosted `instance` VM onto a new shape and/or boot disk size (`20`–`4000` GB) |
| `grain hub delete [-confirm <name>\|-yes] [-delete-disk] <name>` | Delete an entry (fencing a hosted instance's database for the 7-day restore window unless `-delete-disk` is set, and holding the name for 30 days) |
| `grain hub stop <name>` | Stop a hosted `instance`'s VM while keeping its state (`stopping` → `stopped`) |
| `grain hub start <name>` | Start a stopped hosted `instance` again |
| `grain hub restore <name>` | Restore a soft-deleted hosted `instance` whose retention window (`purge_after`) has not expired |
| `grain hub transfer -to <email> [-owner-group <group>] [-clear-owner-group] <name>` | Offer a two-step accountable `owner` transfer to `<email>` |
| `grain hub transfer -cancel <name>` | Withdraw the entry's pending transfer offer (before it is accepted or expires) |
| `grain hub accept-transfer <name>` | Accept a pending ownership transfer offered to you |
| `grain hub delegating-apps list\|create\|disable\|enable` | Hub admins: manage the service accounts allowed to act on people's behalf ([Delegated access](#delegated-access-delegating-apps-and-consent-grants)) |
| `grain hub grants list\|create\|revoke` | Grant, list, or revoke your own consent for a delegating app to act for you |

---

## 8. Running and Configuring `grain-registry`

### Binary, listeners, and container image

The hub ships as `cmd/grain-hub` (published to
`docker.io/grainai/hub`):

```bash
# Validate a configuration file without starting listeners:
grain-registry validate -config /etc/grain-registry/config.yaml

# Run the registry server and reconciler:
grain-registry serve -config /etc/grain-registry/config.yaml

# Print build version:
grain-registry version --json
```

Each `grain-registry` replica runs three HTTP listeners:

- **`listen` (default `:8080`, `grain-registry` Service behind IAP):**
  serves the hub UI (`/`), JSON API (`/api/…`), OIDC-authenticated
  `/api/entries/{name}/heartbeat` and `/api/entries/{name}/connect`, and
  wildcard subdomain routing (`Host: <name>.<instance-domain>`).
- **`jwks_listen` (default `:8081`, `grain-registry-public` Service
  outside IAP):** exposes strictly `GET /.well-known/jwks.json`,
  `GET /healthz`, and token-authenticated (`X-Grain-Auth: token` +
  `X-Grain-Registry-Token: grc_…`) `PUT /api/entries/{name}/heartbeat`
  and `GET /api/entries/{name>/connect`, returning `404` for everything
  else.
- **`tunnel.peer_listen` (default `:8082`, cluster-internal pod-to-pod
  only):** forwards proxied HTTP, SSE, and upgraded WebSocket streams to
  the replica holding an entry's active outbound tunnel, authenticated by
  `X-Registry-Peer-Auth` (`aud = "urn:grain:registry:peer"`).

### Reference configuration (`config.yaml`)

```yaml
listen: ":8080"
jwks_listen: ":8081"
host: grain.example.com
instance_domain: grain-user.com

auth:
  provider: iap
  iap_audience: /projects/123456789/global/backendServices/987654321
  allowed_domains:
    - example.com
  admins:
    - user:admin@example.com
    - group:grain-registry-admins@example.com
  # Optional: omit creators so every admitted user in allowed_domains can create entries.
  # creators:
  #   - group:grain-creators@example.com
  service_principals:
    - serviceAccount:lifecycle-router@example-prod.iam.gserviceaccount.com

groups:
  provider: cloud-identity   # or "static" with static_members
  cache_ttl: 5m

signing:
  provider: kms
  kms_key_name: projects/grain-registry/locations/global/keyRings/registry/cryptoKeys/jwt/cryptoKeyVersions/1
  jwt_ttl: 60s

catalog:
  heartbeat_interval: 15s
  degraded_after: 45s
  offline_after: 90s
  cache_ttl: 3s
  # Optional: restrict which owners / owner_groups may hold primary scopes on specific repos or orgs.
  # Exact <owner>/<repo> rules override the longest matching <prefix>/* wildcard rule.
  # scope_owners:
  #   - repo: acme/*
  #     principals:
  #       - group:acme-eng@example.com
  #   - repo: acme/billing
  #     principals:
  #       - group:billing-owners@example.com
  #       - user:alice@example.com

tunnel:
  transport: ws-mux
  peer_listen: ":8082"
  replica_heartbeat: 5s
  replica_ttl: 15s
  reconnect_grace: 2s
  drain_timeout: 30s
  max_concurrent_streams: 256
  max_buffered_bytes: 16777216

database:
  url: postgres://registry@127.0.0.1:5432/registry?sslmode=disable

compute:
  provider: gce
  project: grain-instances-prod
  region: us-central1
  zones:
    - us-central1-a
    - us-central1-b
    - us-central1-c
  network: projects/grain-instances-prod/global/networks/grain-instances
  subnetwork: projects/grain-instances-prod/regions/us-central1/subnetworks/instances
  boot_image: projects/grain-images/global/images/family/grain-release
  # Optional GCE bootstrap script (mutually exclusive) and project-IAM toggle:
  # startup_script_file: /etc/grain-registry/instance-startup.sh
  # startup_script: "#!/bin/sh\n..."
  # bind_project_iam: true
  # The hub grants itself roles/iam.serviceAccountUser on each gi-*
  # account it creates so it can attach it to the VM (see "IAM the hub
  # needs" below). Turn off when actAs is granted out of band; set
  # self_principal when the hub's own principal cannot be detected.
  # grant_self_act_as: true
  # self_principal: serviceAccount:grain-registry@grain-instances-prod.iam.gserviceaccount.com
  vm_prefix: grain-
  sa_prefix: gi-
  ssh_break_glass: true
  default_shape: standard
  drain_timeout: 2m
  health_timeout: 10m
  status_poll_interval: 15s
  # Hosted VMs run with nested virtualization (/dev/kvm for Granule
  # microVMs), so shapes must use a machine type that supports it, such as
  # N2 or C4. E2 machine types (e2-standard-*, e2-small, ...) do not, and
  # the gce provider refuses them.
  shapes:
    - name: small
      machine_type: n2-standard-4
      boot_disk_gb: 100
      quota_units: 1
      description: "4 vCPU, 16 GB RAM"
    - name: standard
      machine_type: n2-standard-8
      boot_disk_gb: 200
      quota_units: 2
      description: "8 vCPU, 32 GB RAM"
    - name: large
      machine_type: n2-standard-16
      boot_disk_gb: 500
      quota_units: 4
      description: "16 vCPU, 64 GB RAM"

instance_sql:
  # Default true (Cloud SQL IAM database auth + Admin API); set false for
  # direct TCP over private_ip when Cloud SQL Admin API is unavailable.
  # With false, each instance's role is a built-in password user: the
  # registry generates a fresh password on every provision, stores it in
  # Secret Manager as `<secret_prefix><name>-db-password` (never in the
  # VM's `grain-config` metadata, which anyone with compute.instances.get
  # can read), and the VM fetches it at boot with its own service account
  # and connects with `sslmode=require`. When `compute.bind_project_iam` is
  # false, the registry grants the VM's service account secretAccessor on
  # that one secret. grain-config is only written when a VM is created, so
  # a hosted instance provisioned by an older registry still carries its
  # password (and an `sslmode=disable` DSN) in metadata until it is
  # replaced: `grain hub upgrade <name>` (or
  # `POST /api/entries/<name>/replace`) re-provisions it, which rotates the
  # password and moves it into Secret Manager.
  # cloud_sql_admin: true
  sql_instances:
    - name: grain-inst-sql-0
      connection_name: grain-instances-prod:us-central1:grain-inst-sql-0
      private_ip: 10.10.16.2
      max_databases: 200

instance_state:
  db_retention_period: 168h
  gcs_bucket: grain-instances-prod-state

secrets:
  project: grain-instances-prod
  secret_prefix: grain-inst-

observability:
  fleet_logs: false

config_repos:
  provider: github
  github_org: acme-grain-configs
  repo_prefix: main-
  app_id: 112233
  installation_id: 445566
  private_key_file: /var/run/secrets/grain-registry/github-app-key.pem

quotas:
  default_instance_units: 2
  default_connected: 20
  reservation_period: 720h
```

### IAM the hub needs

For each hosted instance, the `gce` provider creates a service account
`<sa_prefix><name>` (`gi-<name>@<compute.project>.iam.gserviceaccount.com`)
and then creates the VM running as that account. GCE accepts an
`instances.insert` that attaches a service account only when the caller
holds `iam.serviceAccounts.actAs` on that account, so between those two
steps the hub grants its own principal `roles/iam.serviceAccountUser`
on the new account. It makes the grant through the account's own IAM
policy (`projects/<p>/serviceAccounts/<email>:getIamPolicy` and then
`:setIamPolicy`), not the project's. That way the hub can act as the
accounts it created and as no other.

The service-account part of the hub's IAM in `compute.project` is:

| Need | Grant |
|------|-------|
| Create, look up and delete the `gi-*` accounts | `roles/iam.serviceAccountCreator`, `roles/iam.serviceAccountViewer`, `roles/iam.serviceAccountDeleter` |
| Grant itself actAs on each new account | `iam.serviceAccounts.getIamPolicy` and `iam.serviceAccounts.setIamPolicy` (the custom role below) |
| Create, stop and delete the VMs | `roles/compute.instanceAdmin.v1` |

It does **not** need `roles/iam.serviceAccountUser` on the project. No
predefined role holds the two policy permissions without far more besides
(`roles/iam.serviceAccountAdmin` holds them, along with everything the
creator, viewer and deleter roles hold), so grant them through a custom role:

```bash
PROJECT=grain-instances-prod   # compute.project
PRINCIPAL='serviceAccount:grain-registry@grain-hub-prod.iam.gserviceaccount.com'  # see "The registry's principal"

gcloud iam roles create grainRegistryInstanceSAPolicy --project="${PROJECT}" \
  --title="grain-registry: instance service account policies" \
  --description="Lets grain-registry grant itself actAs on the per-instance service accounts it creates" \
  --permissions=iam.serviceAccounts.get,iam.serviceAccounts.getIamPolicy,iam.serviceAccounts.setIamPolicy \
  --stage=GA

for role in roles/iam.serviceAccountCreator roles/iam.serviceAccountViewer roles/iam.serviceAccountDeleter \
    "projects/${PROJECT}/roles/grainRegistryInstanceSAPolicy"; do
  gcloud projects add-iam-policy-binding "${PROJECT}" \
    --member="${PRINCIPAL}" --role="${role}" --condition=None
done
```

**Scope of that grant: there is no way to limit it to `gi-*`.** An IAM
condition on the resource name (for example
`resource.name.startsWith("projects/<p>/serviceAccounts/gi-")`) looks like
it should do that, but service accounts are not among the resources IAM
evaluates resource attributes for. The
[resource attributes reference](https://cloud.google.com/iam/docs/conditions-resource-attributes)
lists the services whose resources "recognize" `resource.name`,
`resource.type` and `resource.service`, and `iam.googleapis.com` is not one
of them. "Other services and resource types do not recognize resource
attributes." That explains why a project-level
`roles/iam.serviceAccountUser` binding conditioned on `gi-*` never let the
registry attach an instance account: the condition never matches, whatever
form the resource name takes (`projects/-/serviceAccounts/<uniqueId>` or
otherwise). For the same reason the docs give no support for conditioning
`setIamPolicy` that way, so this page does not recommend it. Two
consequences follow:

- A project-level grant of `iam.serviceAccounts.setIamPolicy` covers every
  service account in the project. Holding `setIamPolicy` on an account is
  as strong as holding `actAs` on it, because the holder can grant itself
  `roles/iam.serviceAccountUser` there. That is exactly what the registry
  does for the `gi-*` accounts. The custom role keeps the registry from
  acting as other accounts *implicitly*, and it leaves an audit-log entry
  (`SetIamPolicy` on the account) for every account the registry gives
  itself, but it is not a hard boundary.
- The boundary that does hold is the project. Put hosted VMs and their
  `gi-*` accounts in a project of their own (`compute.project`; see
  `deploy/test/hub-sandbox`'s `--hosted-project`) that contains no
  service account you would not let the registry act as: no admin accounts
  and no Terraform deployers. The project's default Compute Engine account
  counts too, so disable it or strip its roles. With that layout, the
  project-level grant above reaches only accounts the registry owns anyway.

#### Turning the grant off

Set `compute.grant_self_act_as: false` when something else grants the
registry `iam.serviceAccounts.actAs` on each instance account. The registry
then makes no `getIamPolicy` or `setIamPolicy` calls and does not need the
custom role. If actAs is missing, instance creation retries for two minutes
and then fails with GCE's `SERVICE_ACCOUNT_ACCESS_DENIED`.

#### The registry's principal

The binding names the identity the registry's Application Default
Credentials authenticate as. Unless `compute.self_principal` is set, the
registry works it out on first use:

| Where the registry runs | Detected principal |
|---|---|
| Service account key file, or ADC impersonating a service account | `serviceAccount:<client email / impersonated email>` |
| GCE VM, or GKE Workload Identity with the Kubernetes service account bound to a Google service account | `serviceAccount:<metadata server's default account>` |
| GKE Workload Identity without a Google service account (the metadata server reports `<project>.svc.id.goog`) | `principal://iam.googleapis.com/projects/<project-number>/locations/global/workloadIdentityPools/<project>.svc.id.goog/subject/ns/<namespace>/sa/<ksa>`, read from the metadata server's project number and the `sub` claim of the pod's mounted service account token |
| Anything else (for example `gcloud auth application-default login`) | the email `oauth2.googleapis.com/tokeninfo` reports for the access token (`user:` or `serviceAccount:`) |

When detection cannot work (for example, the pod's service account token is
not mounted), provisioning fails with a status_reason that points at
`compute.self_principal`. Set it to the principal's IAM form, as you would
in `gcloud ... --member`:

```yaml
compute:
  self_principal: principal://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/grain-hub-prod.svc.id.goog/subject/ns/grain-hub/sa/grain-registry
```

A bare `<name>@<project>.iam.gserviceaccount.com` is accepted as shorthand
for `serviceAccount:<email>`. GCE's own error names the principal it saw:
`User: 'grain-hub/grain-registry (grain-hub-prod.svc.id.goog)'` is the
Workload Identity principal for namespace `grain-hub` and Kubernetes service
account `grain-registry`.

#### What an instance's status_reason says

- **While a new account or binding propagates.** GCP can take a minute or
  so before Compute sees a new account (`EXTERNAL_RESOURCE_NOT_FOUND`) or a
  new binding on it (`SERVICE_ACCOUNT_ACCESS_DENIED`). The entry stays
  `creating` with
  `waiting for service account <sa>, and the registry's permission to attach it, to propagate ...`,
  and the reconciler retries the insert with backoff (5s, 10s, 20s, then
  every 30s) for up to two minutes before failing the entry.
- **When IAM refuses the grant.** Provisioning fails at once with
  `provisioning instance resources failed: gcp iam: the registry (<principal>) is missing iam.serviceAccounts.setIamPolicy on service account <sa> ...`
  (or `getIamPolicy`), which names the custom role above as the fix. The
  entry does not go on to GCE's less specific error.

#### On delete

The binding is part of the instance account's own IAM policy, so it is
deleted along with the account at final purge. Nothing else needs cleaning
up. Fence-on-delete (the retention window) keeps the account, and the binding
with it.
