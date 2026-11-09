# Grain Registry Specification

**Specifies:** [`hub.design.md`](../../design/hub.design.md)

The key words **MUST**, **MUST NOT**, **REQUIRED**, **SHALL**, **SHALL NOT**, **SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **MAY**, and **OPTIONAL** in this document are to be interpreted as described in RFC 2119.

Every normative requirement below is derived from the implementation on `main` and names the automated test(s) that pin it.

---

## Contents

- [1. Provider Seams, Boundary Hygiene, and Configuration](#1-provider-seams-boundary-hygiene-and-configuration)
- [2. Front-Door Authentication, Admission, and Group Resolution](#2-front-door-authentication-admission-and-group-resolution)
- [3. Entry Naming, Subdomains, and 30-Day Tombstone Reservation](#3-entry-naming-subdomains-and-30-day-tombstone-reservation)
- [4. Entry Kinds, Ownership (`owner` + `owner_group`), and Quotas](#4-entry-kinds-ownership-owner--owner_group-and-quotas)
- [5. Two-Step Accountable Ownership Transfer](#5-two-step-accountable-ownership-transfer)
- [6. Catalog Privacy (`listed`), Heartbeats, and Scope Resolution](#6-catalog-privacy-listed-heartbeats-and-scope-resolution)
- [7. Directory Search, Longest-Prefix Route Resolution, and Cache Consistency](#7-directory-search-longest-prefix-route-resolution-and-cache-consistency)
- [8. ES256 Identity Assertions, JWKS Discovery, and Delegated Identity (`act`)](#8-es256-identity-assertions-jwks-discovery-and-delegated-identity-act)
- [9. Reverse Proxy, Header Sanitization, and Outbound WebSocket Tunnel Routing](#9-reverse-proxy-header-sanitization-and-outbound-websocket-tunnel-routing)
- [10. Hosted Instance Lifecycle, Reconciler State Machine, and Cloud SQL / GCS Isolation](#10-hosted-instance-lifecycle-reconciler-state-machine-and-cloud-sql--gcs-isolation)
- [11. Soft Delete, Retention `restore`, and Immediate Hard Purge](#11-soft-delete-retention-restore-and-immediate-hard-purge)
- [12. Break-Glass OS Login SSH Grants](#12-break-glass-os-login-ssh-grants)
- [13. Admin Recovery, Conflict Resolution, and Control-Plane Audit Log](#13-admin-recovery-conflict-resolution-and-control-plane-audit-log)
- [14. CLI (`grain hub` / `grain connect`) and Web UI (`ui-hub`)](#14-cli-grain-registry--grain-connect-and-web-ui-ui-hub)
- [15. Requirement Traceability Summary](#15-requirement-traceability-summary)
- [16. Sources](#16-sources)

---

## 1. Provider Seams, Boundary Hygiene, and Configuration

### 1.1 Provider seams (`pkg/hub/seams.go`) {#REG-1}

1. `pkg/hub` MUST define narrow, provider-agnostic interfaces for external dependencies so the control plane, store, router, and reconciler can be tested with deterministic fakes and deployed across cloud environments:
   - `FrontIdentity` (`pkg/webauth/front.go`): verifies the front-proxy identity header on every incoming request;
   - `GroupResolver`: `IsMember(ctx, groupEmail, memberEmail) (bool, error)` and `NormalizeGroup(raw) (string, error)`;
   - `AssertionSigner`: `SignAssertion(ctx, RegistryClaims) (string, error)` and `JWKS(ctx) ([]byte, error)`;
   - `ComputeProvider`: `EnsureInstance`, `GetInstance`, `DeleteInstance`, `ListManagedInstances`, `GrantBreakGlassSSH`, `RevokeBreakGlassSSH`;
   - `InstanceResources`: `EnsureResources`, `FenceDatabase`, `UnfenceDatabase`, `PurgeResources`;
   - `ConfigRepoProvider`: `EnsureRepo`, `WriteBootstrapMetadata`, `WriteRestoreFlag`, `ClearRestoreFlag`, `ArchiveRepo`;
   - `OrphanProvider`: `ListOrphans() []api.OrphanVMInfo` and `DeleteOrphan(ctx, zone, instanceID) error`.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep11ConfigProviderSeamsAndFakeValidation`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`
- `pkg/hub/providers/gcp/gcp_test.go`: `TestCloudIdentityGroupsNormalizeCachingAndBoundedNegativeCache`, `TestKMSSignerSignAssertionAndJWKS`, `TestGCEComputeEnsureInstanceAndLabels`, `TestGCPInstanceResourcesEnsureFenceUnfenceAndPurge`
- `pkg/hub/providers/github/github_test.go`: `TestConfigReposEnsureBootstrapRestoreFlagAndArchive`

### 1.2 Core edition boundary hygiene {#REG-2}

1. Core registry packages (`pkg/hub/...`, `cmd/grain-hub/...`, `ui-hub/...`) MUST NOT contain Google-internal domain literals, corporate shortlinks, or internal credential identifiers.
2. All organization-specific domains, GCP project IDs, KMS key versions, Cloud SQL instance names, and GitHub organizations MUST be supplied via runtime configuration (`Config`).

**Pinned by:**
- `tests/hygiene/boundary_test.go`: `TestCoreSaysNoGoogleTokens`

### 1.3 Configuration schema and startup validation {#REG-3}

1. `ParseConfig` (`pkg/hub/config.go`) MUST validate:
   - `server.registry_host` and `server.instance_domain` are valid DNS hostnames, and `registry_host` is distinct from `*.<instance_domain>` unless explicitly routed as the control-plane apex;
   - `server.listen_addr` (default `:8080`), `server.public_listen_addr` (default `:8081`), and `server.peer_listen_addr` (default `:8082`) are distinct listener addresses;
   - `database.url` is non-empty;
   - `auth.iap.audience` (when `auth.mode == "iap"`), `auth.allowed_domains` or `auth.admins`, and `kms.key_versions` are valid;
   - `compute.shapes` defines at least one valid VM shape with `quota_units >= 1` and `boot_disk_gb >= 10`, and `compute.default_shape` names a configured shape;
   - `compute.auto_upgrade` is one of `""`, `"off"`, `"patch"`, `"minor"`, `"notify"`, and `compute.maintenance_window` (when set) parses as `HH:MM-HH:MM` UTC.
2. `grain-registry demo` MUST refuse to bind any non-loopback address (`-addr` MUST resolve to `127.0.0.1`, `::1`, or `localhost`).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestParseConfigValidation`, `TestDemoRefusesNonLoopbackAddr`, `TestStep17StopStartAutoUpgradeAndMaintenanceWindowEndToEnd`

---

## 2. Front-Door Authentication, Admission, and Group Resolution

### 2.1 Front-proxy verification and dual listener port separation {#REG-4}

1. The primary listener (`:8080`, `Server.Handler()`) MUST verify every request (except `/healthz` and `/.well-known/jwks.json`) through `webauth.Admitter` using the configured `FrontIdentity` verifier (`X-Goog-IAP-JWT-Assertion` in production). Unverified or missing front-proxy assertions MUST be refused with `401 Unauthorized`.
2. The public tunnel/heartbeat listener (`:8081`, `Server.PublicHandler()`) MUST expose only:
   - `GET /healthz`
   - `GET /.well-known/jwks.json`
   - `POST /api/entries/{name}/heartbeat` (authenticated solely by an active `grc_…` registration credential for `{name}`)
   - `GET /api/tunnel/connect` (authenticated solely by an active `grc_…` registration credential)
   Every other path on `:8081` MUST be refused with `404 Not Found` so `:8081` never exposes catalog, control-plane, or routed instance surfaces without front-proxy verification.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestPublicListenerPortSeparationAndTokenAuth`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`
- `pkg/webauth/webauth_test.go`: `TestAdmitterDomainAndPrincipalAdmission`

### 2.2 Organization admission (`allowed_domains`, `admins`, `creators`, `service_principals`) {#REG-5}

1. `webauth.Admitter` MUST admit a verified human caller only if their email domain is in `auth.allowed_domains`, their principal (`user:<email>`) is listed in `auth.admins`, or they belong to a group (`group:<email>`) listed in `auth.admins`.
2. Verified service accounts (`*.gserviceaccount.com`, `Kind == "service"`) MUST be admitted for read-only catalog discovery (`GET /api/directory`, `GET /api/route`, `GET /api/status`) or heartbeat reporting only when listed in `auth.service_principals`, registered as an active delegating app (`registry.delegating_apps`), or matching an entry's `connected_principal`.
3. `canCallerCreate` MUST permit every admitted human caller to create entries when `auth.creators` is empty, and restrict creation to principals/groups in `auth.creators` (plus registry admins) when `auth.creators` is non-empty.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`, `TestConsentDelegationPolicyAuthorizeOnBehalfOf`
- `pkg/webauth/webauth_test.go`: `TestAdmitterDomainAndPrincipalAdmission`

### 2.3 `GroupResolver` caching and fail-closed `503` degradation {#REG-6}

1. `CloudIdentityGroups` (`pkg/hub/providers/gcp/groups.go`) MUST normalize `group:<email>` identifiers (`NormalizeGroup`), cache positive membership results for `5m`, and cache negative lookups in a bounded LRU (max `10,000` entries, `60s` TTL) to prevent memory exhaustion on unauthenticated or probing principals.
2. When `GroupResolver.IsMember` returns an error (such as a Google Cloud Identity API timeout or outage), any authorization check that depends on group membership (`owner_group`, group-based `admins`, or group-based `creators`) MUST fail closed with HTTP `503 Service Unavailable` and header `Retry-After: 5`. Direct `owner == caller.Email` and direct `user:<email>` admin checks MUST continue to succeed without consulting `GroupResolver`.

**Pinned by:**
- `pkg/hub/providers/gcp/gcp_test.go`: `TestCloudIdentityGroupsNormalizeCachingAndBoundedNegativeCache`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

---

## 3. Entry Naming, Subdomains, and 30-Day Tombstone Reservation

### 3.1 Entry name syntax, reserved names, and random `adjective-noun-xxxx` generation {#REG-7}

1. Entry names (`store.ValidateEntryName`) MUST be lowercase DNS labels matching `^[a-z][a-z0-9-]{1,30}[a-z0-9]$` (`3..32` characters), MUST NOT contain `--`, and MUST NOT match any built-in or operator-configured reserved name (`registry`, `api`, `admin`, `auth`, `login`, `logout`, `status`, `healthz`, `www`, `mail`, `smtp`, `imap`, `pop`, `ftp`, `ssh`, `vpn`, ` tunnel`, `static`, `assets`, `cdn`, `docs`, `help`, `support`, `billing`, `security`, `oauth`, `jwks`, `well-known`, `internal`, `system`, `root`, `default`, `localhost`, `example`, `test`, `demo`, `staging`, `prod`, `production`, `grain`).
2. When `POST /api/entries` omits `name` for `kind = "instance"`, `Store.GenerateRandomName` MUST generate an available `adjective-noun-xxxx` name that does not collide with any active or reserved tombstone entry.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestEntryNameValidationAndTombstoneReservation`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

### 3.2 30-day soft-delete name reservation and disabled-owner release {#REG-8}

1. Soft-deleting an entry (`state = 'deleted'`) MUST set `deleted_at = now` and `reserved_until = now + name_reservation_period` (default `30 days` / `720h`).
2. While `now < reserved_until` and the deleted entry's `owner` is not disabled (`owners.disabled = false`), `CheckNameAvailability` and `CreateEntry` MUST report `status = "reserved_by_you"` (and allow same-owner recreation) when `callerEmail == owner`, and `status = "reserved"` (rejecting creation with `409 Conflict`) for any other caller.
3. Once `now >= reserved_until`, or immediately when an administrator disables the owner (`SetOwnerDisabled(ctx, owner, true)`) or hard-purges the entry with `release_reservation = true`, the tombstone reservation MUST be released (`status = "free"`) so any admitted user may register the name.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestEntryNameValidationAndTombstoneReservation`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

---

## 4. Entry Kinds, Ownership (`owner` + `owner_group`), and Quotas

### 4.1 Entry kinds (`instance` and `connected`) {#REG-9}

1. Every registry entry MUST have `kind` equal to `"instance"` (registry-provisioned GCE VM routed over the VPC) or `"connected"` (self-hosted workstation, Kubernetes pod, or VM routed over an outbound `grain connect` WebSocket tunnel). No `redirect` kind is accepted in v1.
2. Hosted `instance` entries MUST store their upstream address as a full URL with scheme in `backend_url` (e.g., `http://10.128.0.15:8080`) so TLS upstreams can be introduced without schema changes.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStoreMigrationsQuotasAndOptimisticConcurrency`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

### 4.2 Individual accountable `owner` and co-managing `owner_group` {#REG-10}

1. Every entry MUST have a single accountable human `owner` (`entries.owner`, referencing `registry.owners(email)`). Quota (`max_instance_units` and `max_connected`) MUST always be charged to `entries.owner`, regardless of who in `owner_group` creates, resizes, or restores the entry.
2. An entry MAY specify an optional `owner_group` (`group:<email>`). Setting or changing `owner_group` MUST verify via `GroupResolver.IsMember` that the caller (or the target owner during an admin force-transfer) is a member of that group, unless the caller is a registry admin.
3. Verified members of `entries.owner_group` MUST have full co-management authority on the entry: reading entry details and credentials (`GET /api/entries/{name}`), updating catalog metadata and `listed` (`PATCH /api/entries/{name}`), setting declared scopes (`PUT /api/entries/{name}/scopes`), rotating/revoking `grc_…` credentials, upgrading/resizing/stopping/starting/deleting/restoring the entry, and initiating or cancelling an ownership transfer offer (`POST /api/entries/{name}/transfer`).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`
- `pkg/hub/store/store_test.go`: `TestStoreMigrationsQuotasAndOptimisticConcurrency`

### 4.3 Transactional per-owner quota enforcement (`SELECT … FOR UPDATE`) {#REG-11}

1. Before creating an entry, resizing an `instance` entry to a larger shape, restoring a soft-deleted entry, or accepting/forcing an ownership transfer, `Store` MUST lock the accountable owner's row in `registry.owners` (`SELECT disabled FROM registry.owners WHERE email = $1 FOR UPDATE`) inside the database transaction.
2. If `owners.disabled` is `true`, the transaction MUST abort with `ErrConflict` / `403 Forbidden (owner_disabled)`.
3. Effective quota limits MUST come from `registry.quotas` when an override row exists for `owner_email`, or else from the server defaults (`default_max_instance_units = 2` in store / `10` in config, `default_max_connected = 20`).
4. For `kind = "instance"`, the sum of `shapes.quota_units` across all non-deleted `instance` entries owned by `owner_email` (including `creating`, `running`, `replacing`, `stopping`, `stopped`, and `failed`) plus the delta MUST NOT exceed `max_instance_units`. For `kind = "connected"`, the count of non-deleted `connected` entries owned by `owner_email` plus 1 MUST NOT exceed `max_connected`. Exceeding either limit MUST fail with `ErrQuotaExceeded` (`429 Too Many Requests` / `409 Conflict`).

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStoreMigrationsQuotasAndOptimisticConcurrency`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

---

## 5. Two-Step Accountable Ownership Transfer

### 5.1 Transfer offer, 7-day expiry, `owner_group` composition, and cancel {#REG-12}

1. `POST /api/entries/{name}/transfer` (`OfferEntryTransfer`) by `entries.owner`, a verified member of `entries.owner_group`, or a registry admin MUST record `pending_owner = to` and `transfer_expires_at = now + 7 days` (`168h`), leaving `entries.owner` unchanged until acceptance.
2. By default, offering a transfer MUST preserve `entries.owner_group` so intra-team transfers keep team co-ownership intact. If `clear_owner_group: true` is passed, `owner_group` MUST be cleared; if a non-empty `owner_group` is passed, the caller's membership in that group MUST be verified before updating `owner_group`.
3. Passing `cancel: true` to `POST /api/entries/{name}/transfer` MUST clear `pending_owner` and `transfer_expires_at`.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

### 5.2 Recipient-only acceptance, quota re-accounting, and credential/tunnel/SSH revocation {#REG-13}

1. `POST /api/entries/{name}/transfer/accept` (`AcceptEntryTransfer`) MUST verify that the authenticated human caller matches `entries.pending_owner` (case-insensitive). Members of `entries.owner_group` who are not `pending_owner` MUST be refused with `403 Forbidden`.
2. If `transfer_expires_at` is non-null and `now > transfer_expires_at`, `AcceptEntryTransfer` MUST refuse the acceptance with `409 Conflict` (`ErrConflict: transfer offer expired`).
3. Within a single `SELECT … FOR UPDATE` transaction, `AcceptEntryTransfer` MUST:
   - verify `pending_owner` is not disabled and has sufficient quota (`max_instance_units` or `max_connected`) for the entry;
   - update `entries.owner = pending_owner`, clear `pending_owner = NULL` and `transfer_expires_at = NULL`, and clear any active break-glass SSH grant (`ssh_granted_to = NULL`, `ssh_grant_expires_at = NULL`);
   - revoke all active `grc_…` registration credentials for the entry (`UPDATE registry.credentials SET revoked_at = now`);
   - terminate all active tunnel sessions for the entry (`DELETE FROM registry.tunnel_sessions WHERE entry_id = $1`) and emit `NOTIFY registry_tunnel_changed`.
4. Registry admins MAY perform an immediate one-step audited force-transfer via `POST /api/admin/entries/{name}/transfer` (or `POST /api/entries/{name}/transfer` with `force: true`), which charges the new owner's quota under `SELECT … FOR UPDATE` and performs the same credential, tunnel, and SSH grant revocations.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

---

## 6. Catalog Privacy (`listed`), Heartbeats, and Scope Resolution

### 6.1 Unlisted-by-default privacy invariant (`listed = false`) {#REG-14}

1. `POST /api/entries` MUST default `listed` to `false` when omitted from the request body.
2. Unlisted entries (`listed = false`) MUST NOT appear in `CatalogCache.Snapshot()`, `GET /api/directory`, `GET /api/route`, or non-admin `GET /api/status` aggregations (counts, active tunnel counts, or `version_counts`), and MUST NOT participate in primary-prefix scope conflict detection against listed entries.
3. `GET /api/entries` for a non-admin caller MUST return only entries where the caller is `entries.owner`, a verified member of `entries.owner_group`, or `entries.pending_owner`.
4. `GET /api/entries/{name}` for an unlisted entry MUST return `403 Forbidden` to callers who are not `owner`, in `owner_group`, `pending_owner`, or a registry admin. For a listed entry, `GET /api/entries/{name}` MUST redact private control-plane fields (`backend_url`, `credentials`, `connected_principal`, `pending_owner`, `transfer_expires_at`) when the caller is not a manager (`owner`, `owner_group`, or admin).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestUnlistedEntryPrivacyAndAuthorization`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

### 6.2 Dual-mode heartbeats (`:8081` `grc_…` token and `:8080` FrontIdentity + dual-binding) {#REG-15}

1. `POST /api/entries/{name}/heartbeat` on `:8081` MUST authenticate the caller exclusively via an active, non-revoked, non-expired `grc_…` registration credential (`Authorization: Bearer grc_…` or `X-Grain-Registry-Token: grc_…`) bound to `{name}`.
2. `POST /api/entries/{name}/heartbeat` on `:8080` MUST authenticate via `FrontIdentity` and verify:
   - for `kind = "instance"`: the caller is the entry's dedicated per-instance GSA (`entries.service_account`), or when `X-Grain-Registry-Token` is also supplied, both the verified front identity and the `grc_…` token match the entry (dual-binding);
   - for `kind = "connected"`: the caller matches `entries.connected_principal`, `entries.owner`, a verified member of `entries.owner_group`, or a valid `grc_…` token for `{name}`.
3. Each accepted heartbeat MUST update `grain_version`, `grain_commit`, `host_kind`, `health`, `health_detail`, `active_tasks`, `awaiting_reply_tasks`, `queue_depth`, `active_sessions`, `last_heartbeat_at = now`, `last_seen_at = now`, and reconcile self-reported scopes (`source = 'reported'`).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestPublicListenerPortSeparationAndTokenAuth`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`
- `pkg/hub/store/store_test.go`: `TestCredentialsScopesHeartbeatsAndRouteResolution`

### 6.3 Reachability status derivation (`online`, `degraded`, `offline`) {#REG-16}

1. `Store.DeriveStatus` MUST compute an entry's live reachability status from heartbeat age (`now - last_heartbeat_at`) and, for `kind = "connected"`, live tunnel session liveness:
   - A `connected` entry without an active `registry.tunnel_sessions` row on a healthy replica (replica heartbeat within `ReplicaTTL = 15s` and tunnel `last_seen_at` within `HeartbeatOfflineTTL = 90s`) MUST be `offline` even if a recent HTTP heartbeat was posted;
   - Any entry with `state != 'running'`, `last_heartbeat_at == NULL`, `heartbeat_age > 90s` (`HeartbeatOfflineTTL`), or `health == 'unhealthy'` MUST be `offline`;
   - Any running entry with `45s < heartbeat_age <= 90s` (`HeartbeatDegradedTTL`) or `health == 'degraded'` MUST be `degraded`;
   - Otherwise the entry MUST be `online`.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestCredentialsScopesHeartbeatsAndRouteResolution`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

### 6.4 Reported vs declared scope precedence and primary-prefix conflict detection {#REG-17}

1. An entry's scopes (`registry.entry_scopes`) track `source = 'reported'` (from daemon heartbeats) and `source = 'declared'` (set by the owner via `POST /api/entries` or `PUT /api/entries/{name}/scopes`).
2. When an entry has one or more `declared` scopes for a repository `R`, any `reported` scope for repository `R` that is wider than a declared scope on `R` MUST be marked `active = false` with `inactive_reason = 'overridden_by_narrower'`, and declared scopes that narrow or demote a reported scope take precedence.
3. Two path prefixes `A` and `B` on the same repository `R` overlap (` store.PrefixesOverlap(A, B)`) if and only if `A == B`, `A == ""` (repo root), `B == ""`, `A` is a path-segment prefix of `B` (`B` starts with `A + "/"`), or `B` is a path-segment prefix of `A`.
4. Across all non-deleted **listed** (`listed = true`) entries, at most one entry MAY hold an active `effective_role = 'primary'` scope on overlapping prefixes of the same repository `R`:
   - On synchronous control-plane writes (`POST /api/entries` with `listed = true`, `PATCH /api/entries/{name}` setting `listed = true`, or `PUT /api/entries/{name}/scopes` on a listed entry), an overlapping primary claim against an incumbent listed entry MUST be rejected with HTTP `409 Conflict` (`code: "primary_scope_conflict"`) and a structured `ScopeConflict` payload naming the incumbent entry, prefix, and owner.
   - On asynchronous `POST /api/entries/{name}/heartbeat`, a newly reported primary scope that overlaps an incumbent listed primary scope MUST NOT fail the heartbeat; instead, the contender's reported scope MUST be stored with `active = false`, `inactive_reason = 'primary_conflict'`, and `conflict_with = <incumbent>`, surfacing an active conflict banner in the UI and in `GET /api/admin/summary`.
5. When `catalog.scope_owners` configures ownership rules (`repo: <owner>/<name>` or `<prefix>/*`, where an exact repository rule overrides the longest matching `<prefix>/*` rule, rules naming a host-qualified repository (`<host>/<owner>/...`) are consulted before bare `<owner>/...` rules, and a bare rule also governs host-qualified claims of the same path; repositories matching no rule remain self-service), an entry MAY hold a `primary` scope on a restricted repository if and only if `user:<entry.owner>`, `entry.owner_group`, or a `GroupResolver`-verified group of `entry.owner` appears in the rule's `principals`:
   - Unauthorized synchronous `primary` writes and transfers (`POST /api/entries`, `PUT /api/entries/{name}/scopes`, `PATCH /api/entries/{name}`, `POST /api/entries/{name}/transfer`, `POST /api/entries/{name}/transfer/accept`) MUST fail closed with `403 Forbidden` (`code: "scope_owner_forbidden"`), or `503 Service Unavailable` (`Retry-After: 5`) if `GroupResolver` fails during evaluation.
   - Asynchronous heartbeats (`PUT /api/entries/{name}/heartbeat`) and admin force-transfers (`POST /api/admin/entries/{name}/force-transfer`) MUST NOT fail liveness on an unauthorized `primary` scope; instead, unauthorized `primary` claims MUST be demoted to `role = 'secondary'`.
   - Primary claims stored before a rule applied (the policy newly configured or tightened) MUST NOT be grandfathered: on startup the registry re-evaluates every entry's `primary` scopes and demotes those the policy does not authorize to `role = 'secondary'`. A `GroupResolver` error during this sweep leaves the claim unchanged rather than demoting it.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestCredentialsScopesHeartbeatsAndRouteResolution`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`
- `pkg/hub/scope_owners_and_shapes_test.go`: `TestParseConfigScopeOwnersValidationAndMatching`, `TestDefaultScopeClaimsWalkthroughAndSecondaryRoute`, `TestCatalogScopeOwnersEnforcesPrimaryClaimsAndDemotesHeartbeats`
- `pkg/hub/scope_owners_sweep_test.go`: `TestScopeOwnersStartupSweepDemotesPrePolicyPrimaries`

---

## 7. Directory Search, Longest-Prefix Route Resolution, and Cache Consistency

### 7.1 `GET /api/directory` and `GET /api/route` longest-prefix ranking {#REG-18}

1. `GET /api/directory` MUST serve exclusively from the in-memory `CatalogCache` snapshot of non-deleted `listed = true` entries, supporting filters by `q` (substring match on `name`, `display_name`, `purpose`, `owner`, `owner_group`, `tags`, and active `scopes.repo`), `repo`, `tag`, `status`, `kind`, `host_kind`, and `owner`.
2. `GET /api/route?repo=<owner/name>&path=<path>` MUST normalize `repo` and `path`, reject any `path` containing `.` or `..` segments with `400 Bad Request`, and match all non-deleted `listed = true` entries with an active scope on `repo` whose `path_prefix` is empty or matches `path` on a `/` segment boundary.
3. Matching candidates MUST be ranked deterministically:
   1. **Specificity** (`len(path_prefix)`, longest matching prefix first; repo-root `""` has specificity `0`);
   2. **Effective role** (`primary` ahead of `secondary`);
   3. **Live reachability status** (`online` ahead of `degraded` ahead of `offline`), EXCEPT that an `offline` `primary` at equal or higher specificity MUST remain ranked ahead of an `online` `secondary` when `include_offline` is `true` (the default) so callers see that the designated primary owner is offline; when `include_offline=false` is passed, offline entries (including offline primaries) MUST be excluded;
   4. **Tie-breakers**: earlier `claimed_at`, then lexicographical `entry.name`.
4. `RouteResponse.Primary` MUST be populated if and only if the top-ranked candidate's `matched_scope.role == "primary"`; when only `secondary` entries match `(repo, path)`, `Primary` MUST remain `nil` while matching `secondary` entries are returned in `Candidates`.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestCredentialsScopesHeartbeatsAndRouteResolution`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`
- `pkg/hub/scope_owners_and_shapes_test.go`: `TestDefaultScopeClaimsWalkthroughAndSecondaryRoute`, `TestCatalogScopeOwnersEnforcesPrimaryClaimsAndDemotesHeartbeats`

### 7.2 Zero-SQL hot path (`CatalogCache` + PostgreSQL `LISTEN/NOTIFY`) {#REG-19}

1. `CatalogCache` (`pkg/hub/router.go`) MUST hold an immutable in-memory snapshot of listed catalog entries and active scopes, refreshed on PostgreSQL `LISTEN registry_catalog_changed` notifications, local mutations, and a periodic fallback ticker (`5s`).
2. `GET /api/directory` and `GET /api/route` MUST read exclusively from `CatalogCache.Snapshot()` and execute **zero SQL queries** on the request hot path.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

---

## 8. ES256 Identity Assertions, JWKS Discovery, and Delegated Identity (`act`)

### 8.1 ES256 `X-Grain-Hub-Assertion` / `X-Grain-Registry-Assertion` signing, 30s replica cache, and JWKS endpoint {#REG-20}

1. When proxying an authenticated request to a hosted `instance` or `connected` entry, `Router` MUST mint an ES256-signed JWT in headers `X-Grain-Hub-Assertion` and `X-Grain-Registry-Assertion` (after stripping any caller-supplied copies of either header) with claims:
   - `iss`: `https://<registry_host>`
   - `aud`: `https://<entry>.<instance_domain>`
   - `sub`: verified caller email
   - `email`: verified caller email
   - `kind`: `"human"` or `"service"`
   - `jti`: unique token identifier
   - `rid`: request trace ID (`X-Grain-Request-ID`)
   - `iat`, `nbf`, `exp` (`60s` lifetime).
2. `CachingSigner` (`pkg/hub/signer.go`) MUST cache signed assertions per `(sub, act.sub, act.grant, aud)` tuple for `30s` (half the `60s` token lifetime) so KMS signing calls are amortized across bursts while guaranteeing at least `30s` of remaining validity to downstream daemons.
3. `GET /.well-known/jwks.json` on both `:8080` and `:8081` MUST return an RFC 7517 JSON Web Key Set (`{"keys": [...]}`) containing all configured active and verification-only P-256 public keys (`alg: "ES256"`, `use: "sig"`, `kty: "EC"`, `crv: "P-256"`, `kid`).

**Pinned by:**
- `pkg/hub/providers/gcp/gcp_test.go`: `TestKMSSignerSignAssertionAndJWKS`
- `pkg/hub/hub_test.go`: `TestCachingSignerActClaimAndInvalidation`, `TestRouterReverseProxyAndHeaderSanitization`
- `pkg/hub/server/hub_rename_test.go`: `TestSanitizeProxyRequestSendsTheAssertionUnderBothNames`

### 8.2 Downstream `registry` verifier on `grain` daemons {#REG-21}

1. Every `grain` daemon configured with `-ui-hub-issuer`, `-ui-hub-audience`, and `-ui-hub-keys-url` (or their deprecated `-ui-registry-*` / `$GRAIN_REGISTRY_*` aliases) MUST register the `registry` `IdentityVerifier` (`Headers() == ["X-Grain-Hub-Assertion", "X-Grain-Registry-Assertion"]`, `AccessCodeWins() == false`) (`pkg/webauth/hub.go`, `pkg/ui/verifier.go`).
2. The `registry` verifier MUST validate the ES256 signature against the registry's JWKS, enforce exact `iss` and `aud` match, validate `exp`/`iat`/`nbf` with `30s` clock-skew tolerance, reject conflicting `X-Grain-Hub-Assertion` and `X-Grain-Registry-Assertion` headers or multiple distinct values on either header, and reject any forged or expired assertion with `401 Unauthorized` without falling back to an access code or plaintext header.

**Pinned by:**
- `pkg/ui/hub_verifier_test.go`: `TestRegistryVerifierAcceptsValidAssertionAndRefusesTampered`
- `pkg/webauth/hub_rename_test.go`: `TestRegistryVerifierRefusesConflictingHubAndRegistryAssertions`
- `pkg/ui/delegation_test.go`: `TestRegistryVerifierExposesPersonAndDelegatingApp`, `TestRegistryVerifierRefusesMalformedOrNestedAct`

### 8.3 RFC 8693 `act` delegated identity and per-person consent grants {#REG-22}

1. A service principal registered in `registry.delegating_apps` (`disabled_at IS NULL`) MAY request delegated routing on behalf of a human principal `P` by sending `X-Grain-On-Behalf-Of: user:<P>` to `https://<entry>.<instance_domain>/...`.
2. `ConsentDelegationPolicy.AuthorizeOnBehalfOf` MUST refuse the request with `403 Forbidden` and an audited `delegation_refused` event unless:
   - the calling service principal is an active registered delegating app (`reason: "app_not_registered"` or `"app_disabled"`);
   - `P` is an admitted human principal in `auth.allowed_domains` (`reason: "person_not_admitted"`);
   - a live, non-revoked, non-expired consent grant (`registry.delegation_grants`) exists for `(app, P)` covering `<entry>` (or `"*"`) (`reason: "no_grant"`, `"grant_expired"`, `"grant_revoked"`, or `"entry_out_of_reach"`).
3. When authorized, the registry MUST mint `X-Grain-Registry-Assertion` with `sub = P`, `email = P`, `kind = "human"`, and top-level `act = {sub: <app_email>, kind: "service", app: <app_name>, grant: <grant_id>, access: <"read"|"write">}` (single level only; nested `act.act` is forbidden).
4. Revoking or updating a consent grant MUST immediately evict matching cached tokens via `CachingSigner.InvalidateGrant(grantID)`, and disabling a delegating app MUST immediately evict matching cached tokens via `CachingSigner.InvalidateApp(appName)` — both on the serving replica and across peer replicas via PostgreSQL `LISTEN registry_delegations` (`grant:<id>` and `app:<name>`).
5. Delegating apps MUST be managed only by registry admins, through `GET`/`POST /api/admin/delegating-apps` (list, create) and `PATCH /api/admin/delegating-apps/{name}` (`{"disabled": bool}`). Create MUST normalise `principal` through `store.NormalizeAppPrincipal` (a bare email gains `serviceAccount:`), MUST refuse a principal that is not a service account (`400`), and MUST answer a taken name or principal with `409`.
6. Consent grants MUST be managed through `GET`/`POST /api/delegation-grants` and `DELETE /api/delegation-grants/{grant-id|app}`, which admit human callers only and act only for the signed-in person: a create whose `person` names anyone else MUST be refused with `403` (admins included), a list MUST return only the caller's grants, and a revoke of another person's grant MUST answer `404`, indistinguishable from a missing one. A grant MUST NOT exceed the app's `max_access` (`403 exceeds_max_access`) or `max_grant_ttl_seconds` (capped), and a disabled app MUST take no new grants (`403 app_disabled`).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestConsentDelegationPolicyAuthorizeOnBehalfOf`, `TestCachingSignerActClaimAndInvalidation`
- `pkg/hub/delegation_admin_test.go`: `TestDelegatingAppAdminAPIAndPersonGrants`
- `pkg/hub/e2e_test.go`: `TestMultiReplicaDelegationCacheEviction`
- `pkg/hub/store/store_test.go`: `TestNormalizeAppPrincipal`
- `tests/security/hub_delegation_test.go`: `TestRegistryConsentSurfaceRefusesOtherTenantsAndNonAdmins`, `TestRegistryDelegationGrantIsOnlyEverThePersonsOwn`
- `pkg/ui/delegation_test.go`: `TestRegistryVerifierExposesPersonAndDelegatingApp`, `TestRegistryVerifierRefusesMalformedOrNestedAct`, `TestDelegatedRoleIsPersonIntersectDelegate`

---

## 9. Reverse Proxy, Header Sanitization, and Outbound WebSocket Tunnel Routing

### 9.1 Inbound header stripping and subdomain routing (`<entry>.<instance_domain>`) {#REG-23}

1. Before forwarding any request to a hosted `instance` backend, a `connected` tunnel stream, or a peer replica, `Router` MUST strip every inbound `X-Grain-*` header (except internal peer-loop prevention evaluated before stripping) and `X-Goog-IAP-*` assertion header so a client can never smuggle a forged `X-Grain-Registry-Assertion`, `X-Grain-Hostd-Token`, or identity header to a downstream daemon.
2. For `kind = "instance"` with `state == "running"`, `Router` MUST forward HTTP and WebSocket requests over the VPC to `entries.backend_url` with the freshly minted `X-Grain-Registry-Assertion`.
3. When a routed entry is `offline`, `creating`, `replacing`, `stopping`, `stopped` (with `auto_wake = false`), or `deleted`, `Router` MUST return a structured `502`/`503` `OfflineErrorResponse` (or HTML status page for browser `Accept: text/html` requests) without leaking internal VPC IPs.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestRouterReverseProxyAndHeaderSanitization`, `TestStep13TunnelMuxCrossReplicaForwardingAndReconnect`

### 9.2 Outbound WebSocket tunnel (`pkg/tunnel/mux`) and credential binding {#REG-24}

1. A `connected` grain daemon (`grain connect` / `grain daemon -hub-connect`) MUST dial `GET /api/tunnel/connect` on `:8081` authenticating with its `grc_…` registration token, upgrade to a binary multiplexed WebSocket (`grain.tunnel.v1` in `pkg/tunnel/mux`), and service multiplexed HTTP/WebSocket streams (`FrameStreamOpen`, `FrameStreamData`, `FrameStreamClose`, `FrameStreamReset`, `FramePing`, `FramePong`) without opening any inbound firewall port.
2. `Store.AcquireTunnelSessionWithCred` MUST atomically increment `session_epoch`, bind the tunnel session to the authenticating `cred_id` and `replica_id`, and emit `NOTIFY registry_tunnel_changed`.
3. If the bound `cred_id` is subsequently revoked (`RevokeCredential`), or ownership of the entry changes (`AcceptEntryTransfer` / `TransferEntryOwner`), or an administrator disconnects the tunnel (`DELETE /api/admin/tunnels/{name}`), the holding replica MUST immediately close the live WebSocket tunnel session.

**Pinned by:**
- `pkg/tunnel/mux/mux_test.go`: `TestSessionHTTPAndWebSocketMultiplexing`
- `pkg/hub/hub_test.go`: `TestStep13TunnelMuxCrossReplicaForwardingAndReconnect`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

### 9.3 Cross-replica peer forwarding (`:8082` `X-Grain-Tunnel-Hop: 1`) {#REG-25}

1. When replica `A` receives a request for `<entry>.<instance_domain>` whose active tunnel session is held by peer replica `B` (`tunnel_sessions.replica_id != A`), replica `A` MUST forward the request over the internal peer listener (`:8082`) to `B.peer_addr` with header `X-Grain-Tunnel-Hop: 1`.
2. The peer listener (`:8082`, `Server.PeerHandler()`) MUST refuse any request whose `X-Grain-Tunnel-Hop` header is already `"1"` when it does not hold the local tunnel session (`502 Bad Gateway`), preventing multi-hop routing loops across stale cache windows.
3. If forwarding to peer `B` fails or returns `502`, replica `A` MUST invalidate its `TunnelCache` entry for `<entry>` and refresh once from PostgreSQL (`RefreshEntry`).

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep13TunnelMuxCrossReplicaForwardingAndReconnect`

---

## 10. Hosted Instance Lifecycle, Reconciler State Machine, and Cloud SQL / GCS Isolation

### 10.1 Single-leader lease and reconciler state machine (`creating`, `running`, `replacing`, `deleting`, `failed`) {#REG-26}

1. `Reconciler` (`pkg/hub/reconcile/reconciler.go`) MUST acquire and renew the PostgreSQL single-leader lease (`registry.leases` row `lease_name = 'reconciler'`, `30s` TTL) before running reconciliation ticks, orphan sweeps, retention purges, or auto-upgrades.
2. For an entry in `state = 'creating'`, `Reconciler` MUST idempotently:
   1. Allocate a shared Cloud SQL instance (`draining = false`, `used_databases < max_databases`) and per-instance database/role;
   2. Provision per-instance GCP resources (`InstanceResources.EnsureResources`: dedicated GSA `<id>@<project>.iam.gserviceaccount.com`, Cloud SQL database & password in Secret Manager `grain-inst-<id>-…`, and GCS Managed Folder IAM binding);
   3. Provision the per-instance config repository (`ConfigRepoProvider.EnsureRepo` and `WriteBootstrapMetadata`);
   4. Provision the GCE VM (`ComputeProvider.EnsureInstance`) with labels `grain-managed=true`, `grain-entry-id=<id>`, `grain-entry-name=<name>`;
   5. Probe `http://<internal_ip>:8080/healthz` until healthy, then transition the entry to `state = 'running'`.
3. If an entry remains in `creating` or `replacing` past `ProvisionTimeout` (default `10m`), `Reconciler` MUST transition it to `state = 'failed'` with a diagnostic `status_reason`.

**Pinned by:**
- `pkg/hub/reconcile/reconciler_test.go`: `TestReconcilerCreateProbeTimeoutAndLeaderLease`, `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`

### 10.2 Shared Cloud SQL pool placement, prefix Secret Manager IAM, and GCS Managed Folder isolation {#REG-27}

1. `AllocateSQLInstance` MUST select the non-draining shared Cloud SQL instance in `registry.sql_instances` with the lowest current database count below `max_databases`, counting both active and soft-deleted retained databases (`purged_at IS NULL`).
2. `GCPInstanceResources.EnsureResources` (`pkg/hub/providers/gcp/resources.go`) MUST isolate each hosted VM's credentials and storage:
   - Each instance receives its own Google Service Account (`grain-vm-<id8>@...`), never a shared default compute service account;
   - Secret Manager access is bound with an IAM condition restricting the instance GSA to secrets matching prefix `projects/<p>/secrets/grain-inst-<id>-`;
   - GCS workspace backup access is bound exclusively to the instance's Managed Folder `gs://<bucket>/instances/<id>/`.

**Pinned by:**
- `pkg/hub/providers/gcp/gcp_test.go`: `TestGCPInstanceResourcesEnsureFenceUnfenceAndPurge`
- `pkg/hub/store/store_test.go`: `TestStep15InstanceLifecyclePlacementAndPurge`

### 10.3 VM shape resize, version upgrade, and 4-step replacement (`draining` → `deleting_vm` → `creating_vm` → `probing`) {#REG-28}

1. `POST /api/entries/{name}/replace` (`RequestEntryReplace`) and `POST /api/entries/{name}/upgrade` (`RequestEntryUpgrade`) MUST transition a hosted `instance` entry to `state = 'replacing'` with `replace_step = 'draining'` (charging any positive VM quota unit delta under `SELECT … FOR UPDATE` before accepting a resize).
2. `Reconciler` MUST execute replacement through four observable steps (`api.ReplaceStep`):
   - `draining`: wait for active tasks to drain (or drain timeout);
   - `deleting_vm`: delete the old GCE VM while preserving the Cloud SQL database, Secret Manager secrets, GCS Managed Folder, and config repository;
   - `creating_vm`: create the new GCE VM with the target shape and target grain version;
   - `probing`: probe `/healthz` on the new VM and call `CompleteEntryReplace` to transition back to `state = 'running'`.

**Pinned by:**
- `pkg/hub/reconcile/reconciler_test.go`: `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`

---

## 11. Soft Delete, Retention `restore`, and Immediate Hard Purge

### 11.1 Soft delete, database/GSA fencing, and 7-day retention window {#REG-29}

1. `DELETE /api/entries/{name}` MUST require confirmation matching `{name}` (`confirm=<name>`) and transition the entry to `state = 'deleted'` (or `state = 'deleting'` for hosted VMs until `Reconciler` deletes the GCE VM and calls `CompleteDeleteEntry`), setting `deleted_at = now`, `reserved_until = now + 30d`, and `db_purge_after = now + 7d` (`db_retention_period = 168h`).
2. On soft delete of a hosted `instance` entry, `Reconciler` MUST delete the GCE VM, fence the per-instance Cloud SQL role (`NOLOGIN` / `InstanceResources.FenceDatabase`), disable the instance GSA, and write a restore flag to the config repository, while retaining the Cloud SQL database, Secret Manager secrets, GCS Managed Folder, and config repository until `db_purge_after`.
3. Soft-deleting an entry MUST immediately release the owner's VM quota units (`max_instance_units`) or connected entry count (`max_connected`).

**Pinned by:**
- `pkg/hub/reconcile/reconciler_test.go`: `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`
- `pkg/hub/store/store_test.go`: `TestStep15InstanceLifecyclePlacementAndPurge`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`

### 11.2 Retention `restore` (`POST /api/entries/{name}/restore`) {#REG-30}

1. `POST /api/entries/{name}/restore` (`Store.RestoreEntry`) MUST be permitted for `entries.owner`, a verified member of `entries.owner_group`, or a registry admin while `entries.state == 'deleted'`, `entries.purged_at IS NULL`, and `now < entries.db_purge_after` (for hosted instances with retained databases).
2. If `entries.purged_at IS NOT NULL` or `now >= entries.db_purge_after`, `RestoreEntry` MUST reject the request with `409 Conflict`.
3. `RestoreEntry` MUST lock `entries.owner` in `registry.owners` (`SELECT … FOR UPDATE`), verify the owner is not disabled and has sufficient quota to re-charge the entry, clear `deleted_at`, `reserved_until`, and `db_purge_after`, and transition `kind = "instance"` to `state = 'creating'` (so `Reconciler` unfences the retained Cloud SQL database and GSA and boots a replacement VM) or `kind = "connected"` to `state = 'running'`.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

### 11.3 Automated retention purge and admin immediate hard purge (`POST /api/admin/entries/{name}/purge`) {#REG-31}

1. Once `now >= entries.db_purge_after` on a soft-deleted `instance` entry with `purged_at IS NULL`, `Reconciler` MUST call `InstanceResources.PurgeResources` (dropping the retained Cloud SQL database/role, deleting `grain-inst-<id>-*` Secret Manager secrets, deleting the GCS Managed Folder, and deleting the GSA), archive the config repo (`ConfigRepoProvider.ArchiveRepo`), and call `Store.MarkEntryResourcesPurged` (`purged_at = now`).
2. `POST /api/admin/entries/{name}/purge` (`handleAdminPurgeEntry` / `Store.HardPurgeEntry`) MUST allow a registry admin to immediately hard-purge a soft-deleted entry (or force-purge with `force: true`), tearing down retained resources, setting `purged_at = now` and `db_purge_after = now`, and when `release_reservation: true` is set, setting `reserved_until = now` so the entry name is immediately available for reuse.

**Pinned by:**
- `pkg/hub/reconcile/reconciler_test.go`: `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

---

## 12. Break-Glass OS Login SSH Grants

### 12.1 Time-boxed OS Login SSH grant and automatic revocation {#REG-32}

1. Hosted `instance` entries default to `allow_ssh = true`. When `allow_ssh` is `true`, `entries.owner`, a verified member of `entries.owner_group`, or a registry admin MAY request a time-boxed break-glass OS Login SSH grant (`ComputeProvider.GrantBreakGlassSSH`, default TTL `2h`, max `8h`), which binds `roles/compute.osLogin` on the target GCE instance exclusively to `user:<caller_email>` with an IAM expiry condition and records `ssh_granted_to` and `ssh_grant_expires_at` on `registry.entries`.
2. Break-glass SSH grants MUST be automatically revoked when the TTL expires, when `allow_ssh` is flipped to `false`, when the entry is soft-deleted or stopped, or when entry ownership is transferred (`AcceptEntryTransfer` / `TransferEntryOwner`).

**Pinned by:**
- `pkg/hub/providers/gcp/gcp_test.go`: `TestGCEComputeEnsureInstanceAndLabels`
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`

---

## 13. Admin Recovery, Conflict Resolution, and Control-Plane Audit Log

### 13.1 Admin summary (`GET /api/admin/summary`) and admin-only route protection {#REG-33}

1. Every `/api/admin/...` route MUST verify `caller.IsAdmin` and return `403 Forbidden` to non-admin callers.
2. `GET /api/admin/summary` MUST return:
   - `replicas`: all registered control-plane replicas with `healthy` computed against `ReplicaTTL` (`15s`), `peer_addr`, and `active_tunnels`;
   - `tunnel_sessions`: all active outbound tunnel sessions across replicas;
   - `owners`: all owners joined with custom `registry.quotas` overrides and active `used_instance_units` / `used_connected` usage (`ListOwnerQuotaSummaries`);
   - `scope_conflicts`: all active primary-prefix scope conflicts across listed entries (`ListAllScopeConflicts`);
   - `sql_instances`: all shared Cloud SQL pool instances with `used_databases`, `active_databases`, `retained_databases`, `max_databases`, and `draining` status (`ListSQLInstanceStatuses`);
   - `orphans`: unmatched GCE VMs discovered by the reconciler orphan sweep (`OrphanProvider.ListOrphans`);
   - `deleted_entries`: soft-deleted entries currently in their retention or name-reservation window;
   - `version_counts`: fleet `grain_version` distribution across all non-deleted entries (including unlisted entries);
   - `reconciler_lease`: current `reconciler` leader lease holder, `active` boolean, and `lag_seconds` (`GetLeaseStatus`);
   - `audit_log`: recent control-plane audit log records including `actor_email`, `via`, and `grant_id`.

**Pinned by:**
- `pkg/hub/hub_test.go`: `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

### 13.2 Admin quota overrides, owner disablement, SQL pool draining, tunnel disconnect, scope conflict resolution, and orphan VM deletion {#REG-34}

1. **Owner disable/enable** (`POST` / `PATCH /api/admin/owners/{email}`): setting `disabled = true` MUST block the owner from creating or receiving entries, release all 30-day tombstone name reservations held by that owner, and record an `owner_disabled_update` audit event.
2. **Quota overrides** (`PUT` / `POST /api/admin/quotas/{email}` and `DELETE /api/admin/quotas/{email}`): MUST upsert or delete a custom `registry.quotas` row (`max_instance_units`, `max_connected`) and record a `quota_set` or `quota_reset` audit event.
3. **Scope conflict resolution** (`POST /api/admin/conflicts/resolve`): MUST accept `{entry_name, repo, path_prefix, resolution: "demote" | "remove"}`, demote the named scope to `secondary` (or delete it), automatically promote the earliest-claimed waiting primary contender on `{repo, path_prefix}` whose prefix no longer overlaps any active listed primary, refresh `CatalogCache`, and record a `scope_conflict_resolve` audit event.
4. **Shared Cloud SQL draining** (`POST` / `PATCH /api/admin/sql-instances/{name}`): MUST toggle `sql_instances.draining` so draining instances receive no new database placements while continuing to serve existing databases, and record a `sql_instance_draining_update` audit event.
5. **Orphan GCE VM deletion** (`DELETE /api/admin/orphans/{zone}/{instance}`): MUST delete the unmatched GCE VM via `OrphanProvider.DeleteOrphan` (verifying the `grain-managed=true` label in `Reconciler.DeleteOrphan` before calling GCE) and record an `orphan_vm_delete` audit event.
6. **Active tunnel disconnect** (`DELETE /api/admin/tunnels/{name}`): MUST delete the active `registry.tunnel_sessions` row, notify replicas to close the live WebSocket tunnel, invalidate `TunnelCache`, and record a `tunnel_disconnect` audit event.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/reconcile/reconciler_test.go`: `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

### 13.3 Control-plane audit log attribution (`actor_email`, `via`, `grant_id`) {#REG-35}

1. Every mutating control-plane action (entry create/update/delete/restore/hard-purge, scope update, credential mint/rotate/revoke, transfer offer/cancel/accept/force, upgrade/replace/stop/start, delegating app/grant change, and every `/api/admin/...` action) MUST insert an immutable row into `registry.audit_log` recording `actor_email`, `via` (`"owner"`, `"owner_group"`, `"admin_override"`, or delegating app), `grant_id` (when applicable), `action`, `entry_id`, `entry_name`, and structured JSON `detail`.

**Pinned by:**
- `pkg/hub/store/store_test.go`: `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`
- `pkg/hub/hub_test.go`: `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd`

---

## 14. CLI (`grain hub` / `grain connect`) and Web UI (`ui-hub`)

### 14.1 `grain hub` and `grain connect` CLI commands and SSRF / credential-hygiene guards {#REG-36}

1. `api.NewClient` (`pkg/hub/api/client.go`) MUST reject any registry URL that uses plain `http://` to a non-loopback host (unless `AllowInsecureHTTP` is explicitly set in tests), contains userinfo credentials, contains query parameters or fragments, exceeds `2048` bytes, or targets link-local/cloud metadata addresses (`169.254.169.254`, `metadata.google.internal`).
2. `grain hub` (`pkg/cli/hub.go`) MUST provide subcommands for `list`, `directory`, `route`, `get`, `create`, `register`, `update`, `scopes`, `token rotate`, `token revoke`, `transfer`, `accept-transfer`, `upgrade`, `replace`, `restore`, `delete`, `status`, `delegating-apps list|create|disable|enable` (registry admins), and `grants list|create|revoke` (the caller's own consent grants), reading credentials from `--token-file` / `$GRAIN_HUB_TOKEN` without leaking registration tokens in command-line process arguments.
3. `grain connect` (and `grain daemon -hub-connect`) MUST maintain the outbound WebSocket tunnel to `:8081/api/tunnel/connect` and periodic heartbeats to `:8081/api/entries/{name}/heartbeat` with exponential backoff and jitter on disconnect.

**Pinned by:**
- `pkg/cli/hub_test.go`: `TestRegistryCLICommandsEndToEnd`, `TestRegistryClientURLValidationAndSSRFGuard`
- `pkg/cli/hub_delegation_test.go`: `TestRegistryCLIDelegatingAppsAndGrants`

### 14.2 Web UI (`ui-hub`) views, strict CSP, and admin recovery controls {#REG-37}

1. `pkg/hub/server` MUST serve the embedded `ui-hub` single-page application with a strict Content-Security-Policy (`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'`) and `X-Content-Type-Options: nosniff`.
2. `ui-hub` MUST implement:
   - **My Entries (`/`)**: personal quota cards, incoming pending transfer acceptance banner, entry table with status/tasks/conflict badges, and type-to-confirm soft-delete dialog;
   - **Directory (`/directory`)**: searchable organization catalog of listed entries and interactive longest-prefix route tester (`/api/route`);
   - **Create / Register (`/new`)**: live name availability checker, hosted VM shape selector, connected entry registration with copyable `-hub-connect` CLI snippet, and one-time `grc_…` token display;
   - **Apps acting for you (`/grants`, alias `/apps`)**: personal consent grant manager listing active grants (`GET /api/delegation-grants`) with an `Include revoked grants` toggle (`?include_revoked=true`), a consent form (`POST /api/delegation-grants` with `app`, `entries`, `access`, `ttl_seconds`), and per-grant revocation (`DELETE /api/delegation-grants/<id>`);
   - **Entry Detail (`/entries/{name}`)**: runtime & tunnel liveness, 4-step VM replacement progress stepper, catalog & ownership editor, two-step transfer offer (with 7-day expiry display, optional `clear_owner_group`, cancel offer, and recipient accept button), reported vs declared scope editor with `409` conflict banner, `grc_…` credential rotation/revocation, VM resize/upgrade, and soft-delete/restore;
   - **Admin (`/admin`, visible only when `me.is_registry_admin` is true)**: reconciler leader lease card, active outbound tunnels per replica with `Disconnect`, owners & quotas editor (`Save quota override`, `Reset quota`, `Disable/Enable owner`), active scope conflicts resolver (`Demote to secondary`, `Remove scope`), shared Cloud SQL pool (`Drain instance` / `Resume placement`), orphan GCE VMs (`Delete orphan VM`), all entries & retention recovery (`Restore`, `Hard purge`, `Force transfer`), delegating apps registry (`GET/POST /api/admin/delegating-apps` accepting a bare service-account email stored as `serviceAccount:<email>`, and `PATCH /api/admin/delegating-apps/<name>` to disable/enable), and control-plane audit log showing `actor_email` and `via`/`grant_id`;
   - **Status (`/status`)**: service health, active replica heartbeats, fleet version distribution, and VM shape catalog.

**Pinned by:**
- `pkg/hub/server/static_test.go`: `TestStaticHandlerServesSPAWithStrictCSP`
- `pkg/hub/delegation_admin_test.go`: `TestDemoDelegatingAppsAndPersonGrants`
- `ui-hub/src/App.test.jsx`:
  - `renders My entries view with quota summary, pending transfer banner, and delete confirmation modal`
  - `renders Directory view and evaluates interactive route tester`
  - `renders Create / Register view defaulting to connected with -hub-connect snippet and mints token`
  - `renders Entry detail view with tunnel status, last_seen_at, scopes, conflict banner, and credential rotation`
  - `handles 409 primary-scope conflict on catalog update and renders VM replacement progress stepper`
  - `renders Admin and Status views with active outbound tunnels per replica and fleet summary`
  - `hides Admin nav for non-admin callers, blocks non-http entry URLs, and handles malformed routes`
  - `supports two-step ownership transfer offer with expiry and cancel in Entry detail view, and admin recovery actions in Admin view`
  - `lists, creates with a bare service-account email, and disables/enables delegating apps on Admin screen`
  - `lists, grants consent, toggles include_revoked, and revokes grants on Apps acting for you screen`

---

## 15. Requirement Traceability Summary

| ID | Requirement | Status on `main` | Primary tests |
| --- | --- | --- | --- |
| `REG-1` | Provider seams (`FrontIdentity`, `GroupResolver`, `AssertionSigner`, `ComputeProvider`, `InstanceResources`, `ConfigRepoProvider`, `OrphanProvider`) | Implemented | `TestStep11ConfigProviderSeamsAndFakeValidation`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-2` | Core edition boundary hygiene (zero Google-internal tokens) | Implemented | `TestCoreSaysNoGoogleTokens` |
| `REG-3` | Configuration schema validation & `demo` loopback-only guard | Implemented | `TestParseConfigValidation`, `TestDemoRefusesNonLoopbackAddr`, `TestStep17StopStartAutoUpgradeAndMaintenanceWindowEndToEnd` |
| `REG-4` | Front-proxy verification on `:8080` and public tunnel/heartbeat port separation on `:8081` | Implemented | `TestPublicListenerPortSeparationAndTokenAuth`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-5` | Organization admission (`allowed_domains`, `admins`, `creators`, `service_principals`) | Implemented | `TestAdmitterDomainAndPrincipalAdmission`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-6` | `GroupResolver` caching, bounded negative LRU, and fail-closed `503` degradation | Implemented | `TestCloudIdentityGroupsNormalizeCachingAndBoundedNegativeCache`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-7` | Entry name validation, reserved names, and `adjective-noun-xxxx` generation | Implemented | `TestEntryNameValidationAndTombstoneReservation` |
| `REG-8` | 30-day soft-delete tombstone name reservation and disabled-owner release | Implemented | `TestEntryNameValidationAndTombstoneReservation`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery` |
| `REG-9` | Entry kinds (`instance` and `connected`, no `redirect` in v1) and `backend_url` with scheme | Implemented | `TestStoreMigrationsQuotasAndOptimisticConcurrency`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-10` | Individual accountable `owner` + co-managing `owner_group` | Implemented | `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-11` | Transactional per-owner quota enforcement under `SELECT … FOR UPDATE` | Implemented | `TestStoreMigrationsQuotasAndOptimisticConcurrency`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery` |
| `REG-12` | Two-step ownership transfer offer, 7-day expiry, `owner_group` preservation/clearing, and cancel | Implemented | `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-13` | Recipient-only transfer acceptance, quota charge, credential/tunnel/SSH revocation, and admin force-transfer | Implemented | `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-14` | Unlisted-by-default privacy (`listed = false`) across Directory, Route, Status, and Entries APIs | Implemented | `TestUnlistedEntryPrivacyAndAuthorization`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-15` | Dual-mode heartbeats (`:8081` `grc_…` token and `:8080` FrontIdentity + dual-binding) | Implemented | `TestPublicListenerPortSeparationAndTokenAuth`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-16` | Reachability status derivation (`online`, `degraded`, `offline`) requiring live tunnel for `connected` entries | Implemented | `TestCredentialsScopesHeartbeatsAndRouteResolution`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-17` | Reported vs declared scope precedence and synchronous `409` vs async heartbeat primary-prefix conflict detection | Implemented | `TestCredentialsScopesHeartbeatsAndRouteResolution`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-18` | `GET /api/directory` filtering and `GET /api/route` longest-prefix ranking & `..` traversal rejection | Implemented | `TestCredentialsScopesHeartbeatsAndRouteResolution`, `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-19` | Zero-SQL hot path via `CatalogCache` and PostgreSQL `LISTEN/NOTIFY` | Implemented | `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd` |
| `REG-20` | ES256 `X-Grain-Registry-Assertion` signing, `30s` `CachingSigner`, and `/.well-known/jwks.json` | Implemented | `TestKMSSignerSignAssertionAndJWKS`, `TestCachingSignerActClaimAndInvalidation` |
| `REG-21` | Downstream `registry` `IdentityVerifier` on `grain` daemons | Implemented | `TestRegistryVerifierAcceptsValidAssertionAndRefusesTampered` |
| `REG-22` | RFC 8693 `act` delegated identity (`X-Grain-On-Behalf-Of`) and per-person consent grants | Implemented | `TestConsentDelegationPolicyAuthorizeOnBehalfOf`, `TestRegistryVerifierExposesPersonAndDelegatingApp`, `TestDelegatedRoleIsPersonIntersectDelegate`, `TestDelegatingAppAdminAPIAndPersonGrants`, `TestMultiReplicaDelegationCacheEviction`, `TestRegistryDelegationGrantIsOnlyEverThePersonsOwn` |
| `REG-23` | Reverse proxy routing and inbound `X-Grain-*` / `X-Goog-IAP-*` header stripping | Implemented | `TestRouterReverseProxyAndHeaderSanitization` |
| `REG-24` | Outbound WebSocket tunnel (`grain.tunnel.v1` mux) and credential revocation termination | Implemented | `TestSessionHTTPAndWebSocketMultiplexing`, `TestStep13TunnelMuxCrossReplicaForwardingAndReconnect` |
| `REG-25` | Cross-replica peer forwarding on `:8082` with `X-Grain-Tunnel-Hop: 1` loop prevention | Implemented | `TestStep13TunnelMuxCrossReplicaForwardingAndReconnect` |
| `REG-26` | Single-leader reconciler lease and VM provisioning state machine | Implemented | `TestReconcilerCreateProbeTimeoutAndLeaderLease`, `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep` |
| `REG-27` | Shared Cloud SQL pool placement, prefix Secret Manager IAM, and GCS Managed Folder isolation | Implemented | `TestGCPInstanceResourcesEnsureFenceUnfenceAndPurge`, `TestStep15InstanceLifecyclePlacementAndPurge` |
| `REG-28` | VM shape resize, version upgrade, and 4-step replacement (`draining` → `deleting_vm` → `creating_vm` → `probing`) | Implemented | `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep` |
| `REG-29` | Soft delete, database/GSA fencing, quota release, and 7-day retention window | Implemented | `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`, `TestStep15InstanceLifecyclePlacementAndPurge` |
| `REG-30` | Retention `restore` (`POST /api/entries/{name}/restore`) within `db_retention_period` | Implemented | `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-31` | Automated retention purge and admin immediate hard purge (`POST /api/admin/entries/{name}/purge`) | Implemented | `TestReconcilerReplaceUpgradeDeletePurgeAndOrphanSweep`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-32` | Time-boxed break-glass OS Login SSH grants and automatic revocation | Implemented | `TestGCEComputeEnsureInstanceAndLabels`, `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery` |
| `REG-33` | Enriched `GET /api/admin/summary` and admin-only route protection | Implemented | `TestStep12CatalogCRUDGroupAuthHeartbeatsDirectoryAndRouteEndToEnd`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-34` | Admin recovery endpoints (owner disable, quota override/reset, conflict resolve, SQL draining, orphan VM delete, tunnel disconnect) | Implemented | `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-35` | Control-plane audit log attribution (`actor_email`, `via`, `grant_id`) | Implemented | `TestStep16TwoStepTransfer7DayExpiryRestoreHardPurgeAndAdminRecovery`, `TestStep16TwoStepTransferRestoreHardPurgeAndAdminRecoveryEndToEnd` |
| `REG-36` | `grain hub` / `grain connect` CLI commands and SSRF / URL / credential-hygiene guards | Implemented | `TestRegistryCLICommandsEndToEnd`, `TestRegistryClientURLValidationAndSSRFGuard`, `TestRegistryCLIDelegatingAppsAndGrants` |
| `REG-37` | `ui-hub` SPA views, strict CSP, two-step transfer UI, and `/admin` recovery tools | Implemented | `TestStaticHandlerServesSPAWithStrictCSP`, `ui-hub/src/App.test.jsx` |

---

## 16. Sources

- Design document: [`design/design/hub.design.md`](../../design/hub.design.md)
- Companion designs & specs:
  - [`design/design/authentication.design.md`](../../design/authentication.design.md) & [`design/spec/authentication/authentication.spec.md`](../authentication/authentication.spec.md)
  - [`design/design/rbac.design.md`](../../design/rbac.design.md) & [`design/spec/rbac/rbac.spec.md`](../rbac/rbac.spec.md)
  - [`design/design/editions.design.md`](../../design/editions.design.md)
