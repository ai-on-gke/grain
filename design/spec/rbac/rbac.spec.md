# Basic RBAC Specification

**Status:** Superseded by [`docs/design/access-rbac.md`](../../../docs/design/access-rbac.md) (task 2969). Not normative; kept as the record of the proposal. See the note below.

**Specifies:** [`rbac.design.md`](../../design/rbac.design.md) (also superseded)

> **Superseded.** This draft was never implemented as written. Grain implemented roles as API scope sets instead (task 2969, [`docs/design/access-rbac.md`](../../../docs/design/access-rbac.md)), not as a fixed `admin > writer > reader` enum. An allowlist entry's `role` names a scope set: the built-ins `grain/viewer`, `grain/contributor` and `grain/operator`, or a set the deployment declares, with optional repository limits. A person who matches several entries holds the union of their sets' scopes, minus anything one of those sets denies. Each route is checked against its scope in `pkg/ui/route_scopes.go` by the same table that checks agents. There is no `model.Role`, `RoleWriter`, `IsAdminOnlyGrant` or `ui.RequirePermission`. Where this document and `access-rbac.md` or the code disagree, `access-rbac.md` and the code are right, and the nightly docs review should not report differences against this file.
>
> | Section here | Disposition | Replaced by |
> | --- | --- | --- |
> | §1.1 `RB-1` (three ordered roles, no custom roles) | **Superseded** | Roles are scope sets, built-in or declared, and custom roles are the point (`access-rbac.md` §1, §10) |
> | §1.2–§1.4 `RB-2`–`RB-4` (storage, entry syntax, highest role wins) | **Superseded** | `allowed_principal.role` holds a scope set name, defaulting to `grain/operator`, and stays out of the grain/config export. Several matching entries are a union of grants with deny-overrides (`access-rbac.md` §1, §2, §9) |
> | §1.5–§1.6 `RB-5`–`RB-6` (owner floor, break-glass) | **Superseded** | Access-code, loopback and in-process callers are unlimited admins. A verified person may assign a role a person must grant to others when their own role holds all of it, with a reason, never widening their own entries or removing the last full admin (`access-rbac.md` §3, §4) |
> | §2 `RB-7`–`RB-8` (admin-only grants, writer filing and approval) | **Superseded** | Filing, editing and approval by a limited role are held to that role's own scopes and repositories (`filingEscalation`, `roleGrantEscalation`, `roleApprovalWithin`). Approving a stored task, or Run now on a task with no approval yet, needs `tasks:admin` on its repository. A task with `autoMerge`, `keepRunning`, a grain/config or person-approved change target, or `networkAccess` wider than the default needs `grain/operator` (`access-rbac.md` §5) |
> | §3 `RB-9`–`RB-12` (permission matrix) | **Superseded** | Every route's scope in `pkg/ui/route_scopes.go` and `humanRouteScopes`, held total by `TestRoleGateCoversEveryRoute` (`access-rbac.md` §5) |
> | §4 `RB-13`–`RB-15` (agents and delegation) | **Superseded** | A person sending `X-Grain-Acting-Task` is held to both their role and the task's grants, and a `gma_` token inside a verified session is intersected with that session's role. Agents may not change the allowlist (`access-rbac.md` §3, §5) |
> | §5–§6 `RB-16`–`RB-20` (enforcement, audit, migration, UI) | **Superseded** | The role gate in `Server.route`, refusals that name the role, `config_audit` surface `access`, existing rows migrating to `grain/operator`, and `access` on `GET /api/auth/user` and `GET /api/config` (`access-rbac.md` §1, §5, §6, §8) |
> | §7 `RB-21`–`RB-22` (tests) | **Superseded** | The tests named in `access-rbac.md` and `pkg/ui/access_roles_test.go` |

This specification defines role-based access control (`admin`, `writer`, `reader`) for Grain: role storage and resolution on allowlisted principals, the exhaustive permission matrix across HTTP routes, streaming/WebSocket endpoints, UI views, and CLI subcommands, agent delegation and `OnBehalfOf` intersection, single-point enforcement, audit, migration, and required automated tests. Where [`rbac.design.md`](../../design/rbac.design.md) and this specification disagree, this specification wins.

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

---

## Contents

- [1. Role Model, Storage, and Resolution](#1-role-model-storage-and-resolution)
- [2. Admin-Only Grants and Task Escalation Rules](#2-admin-only-grants-and-task-escalation-rules)
- [3. Exhaustive Permission Matrix](#3-exhaustive-permission-matrix)
- [4. Agents, Sessions, Tokens, and Delegation](#4-agents-sessions-tokens-and-delegation)
- [5. Enforcement, Wire Contracts, Audit, and Read Scoping](#5-enforcement-wire-contracts-audit-and-read-scoping)
- [6. Migration and UI Surfaces](#6-migration-and-ui-surfaces)
- [7. Verification and Automated Test Suite](#7-verification-and-automated-test-suite)
- [8. Requirement Traceability Summary](#8-requirement-traceability-summary)
- [9. Sources](#9-sources)

---

## 1. Role Model, Storage, and Resolution

### 1.1 Roles and total ordering {#RB-1}

1. `pkg/model` MUST define `Role` with three valid role constants and an empty zero value representing an unadmitted caller:
   ```go
   type Role string

   const (
       RoleNone   Role = ""
       RoleReader Role = "reader"
       RoleWriter Role = "writer"
       RoleAdmin  Role = "admin"
   )
   ```
2. Roles MUST form a strict total order `RoleAdmin > RoleWriter > RoleReader > RoleNone`:
   - `Role.Valid()` MUST return `true` if and only if `r` is `RoleReader`, `RoleWriter`, or `RoleAdmin`.
   - `Role.Covers(required Role) bool` MUST return `true` when `required == RoleNone` or both `r.Valid()` and `required.Valid()` hold with `rank(r) >= rank(required)`.
   - `MaxRole(a, b Role) Role` MUST return whichever of `a` and `b` has the higher rank.
3. No custom roles or per-person raw `api:*` scope assignments MAY be stored in `allowed_principal`. Fine-grained `api:*` scopes remain the narrowing vocabulary for agents and scoped tokens (§4).

### 1.2 Database schema and config-repository exclusion {#RB-2}

1. Role bindings MUST be stored in a SQL table `allowed_principal` created in both SQLite (`pkg/model/sqlite`) and PostgreSQL (`pkg/model/postgres`) migrations:
   ```sql
   CREATE TABLE allowed_principal (
       principal   TEXT PRIMARY KEY,               -- canonical "<prefix>:<lowercased-value>"
       role        TEXT NOT NULL,                  -- "admin", "writer", or "reader"
       created_at  TEXT NOT NULL,                  -- RFC 3339 UTC timestamp
       created_by  TEXT NOT NULL DEFAULT '',       -- actor label or ID
       updated_at  TEXT NOT NULL,                  -- RFC 3339 UTC timestamp
       updated_by  TEXT NOT NULL DEFAULT ''        -- actor label or ID
   );
   ```
2. `allowed_principal` MUST NOT be included in `staterepo.SettingsTables` (`pkg/staterepo`). Merging a pull request into `grain/config` MUST never add, promote, demote, or delete `allowed_principal` entries.
3. `allowed_principal` MUST be included in `staterepo.BackupOnlyTables` so cold disaster-recovery exports (`grain backup` / `grain export`) preserve role bindings alongside `hostd` and `external_agent_token` metadata.
4. Mutations to `allowed_principal` via `/api/access/principals` MUST remain writable by a human `admin` even when `grain_config.settings_locked` is `true` (external config repository mode) or `grain_config.config_mode` is `"simple"` ([`simple-config.design.md`](../../design/simple-config.design.md) §4.3).

### 1.3 Principal entry syntax, validation, and default role {#RB-3}

1. Every `allowed_principal.principal` entry MUST validate under `ui.ParseAllowedPrincipals` (`pkg/ui/allowlist.go`, [`authentication.spec.md`](../authentication/authentication.spec.md) `AU-6`), supporting the four canonical prefixes:
   - `user:<email>` — matches `model.PrincipalHuman` case-insensitively (rejecting `.gserviceaccount.com` addresses);
   - `serviceAccount:<email>` — matches `model.PrincipalService` case-insensitively (requiring `.gserviceaccount.com`);
   - `domain:<domain>` — matches `model.PrincipalHuman` whose exact domain after `@` equals `<domain>` (never matching subdomains or `model.PrincipalService`);
   - `group:<group-email>` — matches `model.PrincipalHuman` or `model.PrincipalService` principals that are transitive members of `<group-email>` in `GroupMemberChecker`.
2. Daemon flag `-ui-allowed-principals` and `$GRAIN_ALLOWED_PRINCIPALS` MUST accept an OPTIONAL `=<role>` suffix on each comma-separated item:
   ```
   <prefix>:<value>[=<admin|writer|reader>]
   ```
   Role names in flag items MUST be normalized to lower-case and trimmed; any `=<role>` value outside `admin`, `writer`, or `reader` MUST fail `ui.ParseAllowedPrincipals` and daemon startup validation.
3. **Default role when omitted:**
   - On `POST /api/access/principals`, omitting `role` (or passing `""`) MUST default to `RoleReader` (`"reader"`).
   - On a fresh deployment (no pre-existing `grain_config` row prior to RBAC startup), `-ui-allowed-principals` items without an explicit `=<role>` suffix MUST seed `allowed_principal` with `RoleReader`, EXCEPT an entry matching the deployment's bootstrap owner (§1.5), which always resolves to `RoleAdmin`.
   - On one-time migration of an existing pre-RBAC deployment (§6.1 `RB-19`), `-ui-allowed-principals` items without `=<role>` MUST seed `allowed_principal` with `RoleAdmin` so existing operators lose no access on upgrade.
4. **Sandbox `gcp-key` pool exclusion (`AU-8` preserved):** Any `serviceAccount:` entry naming an account in `GCPKeyPoolEmails` MUST be rejected at daemon startup (`CheckUIAllowedPrincipals`) and refused with `422 Unprocessable Entity` on `POST /api/access/principals`. At request time, a verified principal in `GCPKeyPoolEmails` MUST resolve to `RoleNone` (`403 Forbidden`) regardless of any `group:` membership or role.

### 1.4 Highest-role-wins resolution and Cloud Identity group short-circuit {#RB-4}

1. Given a verified caller `id` (`VerifiedIdentity{Email, Kind}`), `Server.resolvePrincipalRole(ctx, id) (Role, error)` MUST evaluate the effective allowlist (the union of database `allowed_principal` rows and the in-memory bootstrap floor of §1.5) and return the **highest matching role** (`MaxRole` over all matching entries).
2. Evaluation MUST proceed in two phases so Cloud Identity latency or outages never degrade higher or equal direct grants:
   - **Phase 1 (in-memory direct match):** Evaluate the immutable bootstrap floor (§1.5) plus all `user:`, `serviceAccount:`, and `domain:` entries without network I/O, computing `directRole`. If `directRole == RoleAdmin`, `resolvePrincipalRole` MUST return `(RoleAdmin, nil)` immediately without calling `GroupMemberChecker`.
   - **Phase 2 (selective `group:` lookup):** Collect only `group:` entries whose `Role` is strictly greater than `directRole` (`entry.Role > directRole`), ordered from highest role (`admin`) down to `reader`. As soon as a `group:` entry matches with `RoleAdmin` (or the highest remaining candidate role), remaining `group:` entries MUST NOT be queried.
3. If `GroupMemberChecker.IsMember` returns an error:
   - When `directRole != RoleNone`, `resolvePrincipalRole` MUST log the group-lookup error (deduplicated) and return `(directRole, nil)`, preserving the caller's verified direct access while failing closed on the unverified higher group role;
   - When `directRole == RoleNone` and no other `group:` entry matched without error, `resolvePrincipalRole` MUST fail closed and refuse admission with `403 Forbidden` (`AU-9`).

### 1.5 Bootstrap `admin` floor and lockout-prevention invariants {#RB-5}

1. `grain daemon` MUST support `-deployment-owner=<email>` (`$GRAIN_DEPLOYMENT_OWNER`, validated as a human email address when non-empty). When `-deployment-owner` is unset and `Config.Actor.ID` (`-as`) is a valid non-service-account email address containing `@`, that address acts as the fallback deployment owner for bootstrap admission when `allowed_principal` is empty.
2. Whenever `deploymentOwner` (`-deployment-owner`) is non-empty, the server MUST merge `{Principal: "user:" + lower(deploymentOwner), Role: RoleAdmin}` in memory as an **immutable floor** on every request, even if `allowed_principal` is empty or holds a lower role for that email.
3. Every daemon-provisioned service account in `Config.ProvisionedServiceAccounts` (for example `grain-daemon@<gcp-project>.iam.gserviceaccount.com`, excluding `GCPKeyPoolEmails`) MUST likewise carry an in-memory `serviceAccount:<sa>=admin` floor.
4. Mutations on `/api/access/principals` (`POST`, `PATCH`, `DELETE`) MUST enforce three lockout-prevention invariants, returning `422 Unprocessable Entity` (`code: "lockout_prevented"`) if violated:
   - **Pinned owner protection:** A request MUST NOT delete `user:<deploymentOwner>` or set its role below `admin` when `-deployment-owner` is set.
   - **At least one human admin:** When `-deployment-owner` is empty, the resulting `allowed_principal` table MUST retain at least one `user:<email>` row with `role = "admin"`.
   - **No self-lockout:** A human `admin` caller MUST NOT delete or demote the entry that grants their own `admin` role unless `-deployment-owner` equals their email or another `user:<caller-email>` row with `role = "admin"` remains.
5. On a registry-managed instance ([`hub.design.md`](../../design/hub.design.md)), `grain-registry` passes `entries.owner` as `-deployment-owner`; transferring `entries.owner` updates `-deployment-owner` on VM replacement, automatically transferring the immutable `admin` floor to the new owner.

### 1.6 Break-glass access code and loopback callers {#RB-6}

1. Any request authenticated without a verified person via:
   - the deployment access code (`local-api-token` bearer header or `grain_access` cookie, when permitted by `singleTenantGate` and `AccessCodePrecedence`),
   - unproxied loopback break-glass (`127.0.0.1` / `::1` without proxy headers, such as `kubectl exec … -- grain …` or an SSH port-forward under `-ui-require-iap`), or
   - an in-process controller call (`SourceInProcess`),
   MUST resolve to `RoleAdmin`.
2. Per [`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md) `OA-1` and `OA-6`, such calls MUST record `Actor` as `{Kind: PrincipalHuman, ID: Config.Actor.ID, Source: "access-code" | "loopback" | "in-process"}` (`operator (access code)` or `operator (local)`) and MUST NEVER attribute mutations to a named person.

---

## 2. Admin-Only Grants and Task Escalation Rules

### 2.1 Admin-only grant classification (`model.IsAdminOnlyGrant`) {#RB-7}

1. `pkg/model` MUST define `IsAdminOnlyGrant(grant string) bool`, returning `true` if and only if any of the following holds:
   - `model.IsPersonOnlyGrant(grant)` is `true` (`priority-dispatch`, `durable`, any dangerous `api:*` scope—`tasks:admin`, `merge:force`, `settings:write`, `connections:write`, `capabilities:admin`, `secrets:write`, `deployment:upgrade`, `deployment:restart`, `deployment:reboot`—or any built-in/declared scope set with `requires_person: true`);
   - `grant` is an `api:*` grant (scope, preset, or declared scope set, with or without `@owner/name`) whose resolved `APIScope` set contains any scope whose route requirement is `RoleAdmin` (`repos:write`, `schedules:write`, `templates:write`, `suites:write`, `boards:write`, `memory:write`, `sessions:write`, `caches:write`, `teams:broadcast`, `secrets:read`, `connections:read`); or
   - `grant` names a host-execution capability marked `AdminOnly` in the capability registry (`https://grain.dev/capabilities/grain#host-exec`, formerly `self-repair`, retired in schema migration 84; and `…/host-script#host-script`, formerly `local-scripts:<name>`, retired in tasks 2366 and 3077).
2. Repository-seeded default capabilities (`repo_config.default_capabilities`, configured by an `admin`) that do not satisfy `IsAdminOnlyGrant` are `writer`-grantable; if an `admin` configures an `IsAdminOnlyGrant` capability in `repo_config.default_capabilities`, tasks inheriting it strictly via `GrantByDefault` (`director-capabilities.spec.md` §4.2) without caller-supplied argument overrides MAY be filed by a `writer` without holding for `admin` approval, because an `admin` authored that repository default.

### 2.2 Writer task filing, editing, capability attachment, and proposal approval {#RB-8}

1. **Filing (`POST /api/tasks`, `POST /api/tasks/from-template`):**
   - A `writer` MAY file tasks (approved or proposed) whose explicit `capabilities` / template grants contain no `IsAdminOnlyGrant` entry (or only `GrantByDefault` per ``RB-7`.2`), provided the target repository is not the deployment's `state_repo` (`grain/config`).
   - If a `writer` (or an agent acting `OnBehalfOf` a `writer`) submits `POST /api/tasks` carrying any explicit `IsAdminOnlyGrant` capability (or `priorityDispatch: true`):
     - When `approved` is `false` or omitted (proposal mode) or `holdForApproval: true`, Grain MUST accept the filing as an unapproved proposal (`state: "proposed"`) with an escalation note (`CapabilityEscalationNote`) recording that **`admin` approval is required** for the named grant(s);
     - When `approved: true` is explicitly requested by a `writer`, the server MUST refuse with `403 Forbidden` (`code: "insufficient_role"`, `requiredRole: "admin"`), naming the admin-only grant(s).
2. **Editing (`PATCH /api/tasks/{id}`) and direct grant changes (`POST /api/tasks/{id}/capabilities`):**
   - A `writer` MAY edit task metadata (including numeric `priority` `>= 1`, `title`, `description`, `base`, `dependsOn`, `autoMerge`, `keepRunning`, etc.) and MAY attach or detach non-`IsAdminOnlyGrant` capabilities via `POST /api/tasks/{id}/capabilities`.
   - Setting `priorityDispatch: true` on `PATCH /api/tasks/{id}`, attaching any `IsAdminOnlyGrant` capability on `POST /api/tasks/{id}/capabilities`, or retargeting an approved task's `repo`/`writes` to `grain/config` (`state_repo`) MUST require `RoleAdmin` (`403` for `writer`).
3. **Approving proposals (`POST /api/tasks/{id}/approve`):**
   - A `writer` MAY approve a proposed task (`POST /api/tasks/{id}/approve`) when the task holds no `IsAdminOnlyGrant` (other than `GrantByDefault` per ``RB-7`.2`) and does not write to `grain/config` (`state_repo`).
   - If the proposed task carries any non-default `IsAdminOnlyGrant` capability, `priorityDispatch`, or `writes` targeting `grain/config` (`state_repo`), `POST /api/tasks/{id}/approve` MUST require `RoleAdmin`.
4. **`Submit` (`POST /api/tasks/{id}/submit`) and config changes:**
   - Pressing `Submit` on an ordinary repository task is `RoleWriter` (and strictly human-only; agents remain barred by `agentAPIGate`).
   - Pressing `Submit` on a task targeting `grain/config` (`state_repo`), or calling `POST /api/tasks/{id}/config-change/approve` or `/reject`, MUST require `RoleAdmin`.

---

## 3. Exhaustive Permission Matrix

### 3.1 Hard-case classification rules {#RB-9}

The normative decisions for the eleven operational boundaries in [`rbac.design.md`](../../design/rbac.design.md) §2 are:

1. **Proposals & person-only grants:** `POST /api/tasks/{id}/approve` is `writer` for ordinary proposals and `admin` when the task carries `IsAdminOnlyGrant` or writes `grain/config` (`RB-8`).
2. **`Submit` vs. `force-merge` vs. `retry-merge`:** `POST /api/tasks/{id}/submit` is `writer` (human-only; `admin` for `grain/config` tasks); `POST /api/tasks/{id}/retry-merge` and `POST /api/tasks/{id}/pull-request` are `writer`; `POST /api/tasks/{id}/force-merge` is `admin`.
3. **Task capability grants:** `POST /api/tasks/{id}/capabilities` is `writer` for non-`IsAdminOnlyGrant` grants and `admin` for any `IsAdminOnlyGrant` grant (`RB-7`, `RB-8`).
4. **Director & console sessions:** Reading `/api/console-sessions`, `/api/console`, and `/api/console/{id}/output` is `reader`; messaging a session (`POST /api/tasks/{id}/comments`), restarting/cancelling a session turn (`POST /api/console-sessions/{id}/restart`, `/cancel`), and interacting with a task PTY (`POST /api/console/{id}/input`, `/resize`, `DELETE /api/console/{id}`, unless the target task holds `IsAdminOnlyGrant`, which requires `admin`) are `writer`; creating, updating, or deleting standing session definitions (`POST /api/console-sessions`, `PUT /api/console-sessions/{id}`, `DELETE /api/console-sessions/{id}`) is `admin`.
5. **`gma_` external-agent tokens vs. `hostd` tokens:** All `/api/hostds*` routes are `admin`. When `externalAgentApi` is enabled (`PUT /api/settings`, `admin`), `writer`s MAY list, mint, rotate, revoke, and delete **their own** (`owner == caller`) `gma_` tokens on `/api/external-agents/tokens*` provided every resolved scope of the token is covered by `RoleWriter` (§4.2 `RB-14`); minting `admin`-scoped tokens, viewing/acting on another user's tokens, and `POST /api/external-agents/tokens/revoke-all` require `admin`. `reader`s MUST NOT mint or manage `gma_` tokens.
6. **Releases:** Reading releases and release notes is `reader`; operational release actions (`POST /api/repos/{owner}/{name}/releases`, `DELETE .../releases/{release}`, `POST .../releases/{release}/merge`, `POST .../candidates`, `POST .../candidates/promote`, `POST .../pending-releases/{version}/approve`, `POST .../pending-releases/{version}/reject`, `POST .../candidates/{id}/qualification/approve`, `PUT /api/release-notes`) are `writer`; release configuration and credentials (`PUT .../release`, `PUT .../qualification-plan`, `PUT/DELETE .../release-secrets/{secret}`) are `admin`.
7. **Schedules, templates, suites, boards, and memory:** `POST /api/schedules/{id}/run` and `POST /api/suite-runs` are `writer`; all create/update/delete routes on `/api/schedules*`, `/api/templates*`, `/api/suites*`, `/api/boards*`, and `PUT /api/memory*` are `admin`; all `GET` and draft `composition` preview routes (`POST /api/schedules/composition`, `POST /api/suites/composition`) are `reader`.
8. **Repository import:** Onboarding, removing, or configuring repositories (`POST /api/repos`, `DELETE /api/repos/{owner}/{name}`, `GET /api/deployment/reachable-repos`, `PUT /api/repos/{owner}/{name}/*`, `POST .../branches`, `POST .../lifecycle/refresh`) is `admin`; importing GitHub issues into tasks on an existing repository (`POST /api/repos/{owner}/{name}/import-issues`) is `writer`.
9. **Secrets:** Plaintext secret values MUST NEVER be returned on any route for any role (`nobody`). Metadata/audit reads (`GET /api/secrets`, `secret-audit`, `audit-logs`, `agent-keys`, `gcp-keys*`, `github-tokens`, `codex-auth`, `POST /api/agent-keys/{framework}/check`) and all write/bind/delete secret routes (including `PUT /api/tasks/{id}/secret` and `POST /api/capability-servers/bind-secret`) are `admin`.
10. **Deployment health and logs:** Queue pause status, sandbox capacity (`GET /api/sandboxes`, `GET /api/persistent-sandboxes`), metrics, unmatched failures, upgrade status/targets/release-notes, cache sizes, `GET /api/settings`, `GET /api/settings/cli-run`, and `GET /api/restart-watch` are `reader`; raw controller/host logs (`GET /api/logs`, `GET /api/logs/{source}`, `GET /api/persistent-sandboxes/{name}/logs`) and `GET /api/host/top` are `admin`. Task transcripts (`GET /api/tasks/{id}/attempts/{number}/transcript`) and CI job logs (`GET /api/ci/runs/{id}/log`) are `reader`.
11. **Inbox and notifications:** `GET /api/notifications` is `reader`; `POST /api/notifications`, `/ack`, `/clear`, and `/investigate` are `writer`.

### 3.2 Complete HTTP API route permission table {#RB-10}

Every pattern registered on `ui.Server` (`pkg/ui/server.go`, `pkg/ui/route_scopes.go` — 327 existing patterns plus 5 new RBAC/`whoami` patterns = **332 total patterns**) MUST enforce the minimum role declared below. Legend for the **Role** column:
- `public`: unauthenticated/pre-auth route (`singleTenantOpenRoute`), open before role resolution;
- `protocol`: authenticates with its own protocol token (`hostd` `ghd_…` + owner check; `mcp` `gma_…` + per-tool route/role check);
- `daemon`: internal `grain mcpserver` loopback plumbing (`APIScopeInternal`); requires daemon `local-api-token` (`admin`) and is refused to browser/CLI `reader` and `writer` callers;
- `reader`, `writer`, `admin`: minimum `model.Role` checked by `ui.RequirePermission`.

#### A. Frontend, Authentication, Identity, and RBAC (`/` + 13 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `/` | *(SPA)* | `public` | Serves static assets / `index.html` (or HTML login/refusal pages when gated by `singleTenantGate`). |
| `GET /api/config` | `settings:read` | `public` | Pre-login bootstrap metadata; MUST NOT expose secrets or `allowed_principal` list. |
| `GET /api/auth/access-code` | `internal` | `public` | Access-code sign-in page/metadata. |
| `POST /api/auth/access-code` | `internal` | `public` | Exchanges valid access code for `grain_access` session (`admin` break-glass). |
| `GET /api/auth/access-code/login.js` | `internal` | `public` | Access-code login script. |
| `POST /api/auth/logout` | `internal` | `public` | Clears `grain_access` / returns IAP logout redirect. |
| `GET /api/auth/logout` | `internal` | `public` | Browser logout redirect. |
| `GET /api/auth/user` | `internal` | `public` | Legacy caller status; includes `role` when authenticated. |
| `GET /api/auth/status` | `internal` | `public` | Auth status check. |
| `GET /api/whoami` *(new, `OA-5`)* | `internal` | `reader` | Returns `{actor, onBehalfOf, source, verified, allowed, role, deploymentOwner}` (§6.2). |
| `GET /api/access/principals` *(new)* | `capabilities:read` | `admin` | Lists `allowed_principal` rows plus immutable `-deployment-owner` floor (§6.2). |
| `POST /api/access/principals` *(new)* | `capabilities:admin` | `admin` | Adds an `allowed_principal` entry (human `admin` only; `role` defaults to `"reader"`). |
| `PATCH /api/access/principals` *(new)* | `capabilities:admin` | `admin` | Updates `role` on an `allowed_principal` entry (human `admin` only; enforces `RB-5`). |
| `DELETE /api/access/principals` *(new)* | `capabilities:admin` | `admin` | Removes an `allowed_principal` entry by `principal` (human `admin` only; enforces `RB-5`). |

#### B. Tasks, Conversations, Approvals, Merges, and Notifications (70 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/tasks` | `tasks:read` | `reader` | Deployment-wide task list (§5.2). |
| `GET /api/tasks/states` | `tasks:read` | `reader` | Task state counts. |
| `GET /api/states` | `tasks:read` | `reader` | Task state vocabulary. |
| `GET /api/tasks/{id}` | `tasks:read` | `reader` | Full task detail and conversation. |
| `GET /api/tasks/{id}/attachments/{attachmentId}` | `tasks:read` | `reader` | Task/comment attachment bytes. |
| `GET /api/tasks/{id}/messages` | `tasks:read` | `reader` | Task message feed. |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | `tasks:read` | `reader` | Attempt transcript log. |
| `GET /api/tasks/{id}/attempts/{number}/capability-audit` | `tasks:read` | `reader` | Per-attempt capability invocation audit. |
| `GET /api/tasks/{id}/prompt` | `tasks:read` | `reader` | Rendered task prompt. |
| `GET /api/tasks/{id}/tree` | `tasks:read` | `reader` | Task parent/child dependency & proposal tree. |
| `GET /api/pipeline/changes` | `tasks:read` | `reader` | Merge queue & active PR changes. |
| `GET /api/notifications` | `tasks:read` | `reader` | Operator/team inbox notifications. |
| `POST /api/tasks` | `tasks:write` | `writer` | `admin` required if `approved: true` with `IsAdminOnlyGrant`, `priorityDispatch`, or `grain/config` target (`RB-8`). |
| `POST /api/tasks/from-template` | `tasks:from-template` | `writer` | Same `IsAdminOnlyGrant` check as `POST /api/tasks`. |
| `POST /api/tasks/reorder` | `tasks:write` | `writer` | Reorders queued tasks. |
| `PATCH /api/tasks/{id}` | `tasks:write` | `writer` | `admin` required if setting `priorityDispatch: true` or retargeting an approved task to `grain/config`. |
| `POST /api/tasks/{id}/capabilities` | `capabilities:admin` | `writer` | `writer` for non-`IsAdminOnlyGrant` grants; **`admin`** if attaching any `IsAdminOnlyGrant` (`RB-7`, `RB-8`). |
| `POST /api/tasks/{id}/depends-on` | `tasks:write` | `writer` | Edits task dependencies. |
| `POST /api/tasks/{id}/approve` | `tasks:admin` | `writer` | `writer` for ordinary proposals; **`admin`** if task holds non-default `IsAdminOnlyGrant` or targets `grain/config` (`RB-8`). |
| `POST /api/tasks/{id}/withdraw-approval` | `tasks:write` | `writer` | Returns queued task to `proposed`. |
| `POST /api/tasks/{id}/park-until` | `tasks:write` | `writer` | Parks task until timestamp/condition. |
| `POST /api/tasks/{id}/resume-later` | `tasks:write` | `writer` | Suspends and re-queues task. |
| `POST /api/tasks/{id}/defer` | `tasks:write` | `writer` | Defers task dispatch. |
| `POST /api/tasks/{id}/undefer` | `tasks:write` | `writer` | Lifts task deferral. |
| `POST /api/tasks/{id}/submit` | `tasks:write` | `writer` | Human-only; **`admin`** if task targets `grain/config` (`state_repo`) (`RB-8`). |
| `POST /api/tasks/{id}/comments` | `tasks:write` | `writer` | Posts comment or sends message to `director`/session. |
| `POST /api/tasks/{id}/close` | `tasks:write` | `writer` | Closes task. |
| `POST /api/tasks/{id}/stop` | `tasks:write` | `writer` | Cancels in-flight run. |
| `POST /api/tasks/{id}/pause` | `tasks:write` | `writer` | Pauses task. |
| `POST /api/tasks/{id}/resume` | `tasks:write` | `writer` | Resumes paused task. |
| `PUT /api/tasks/{id}/goal` | `tasks:write` | `writer` | Sets task completion goal. |
| `DELETE /api/tasks/{id}/goal` | `tasks:write` | `writer` | Clears task completion goal. |
| `PUT /api/tasks/{id}/hostd` | `tasks:write` | `writer` | Refused (400): a task has no `hostd`; it is set on the model connection or capability server (schema 111). |
| `POST /api/tasks/{id}/reopen` | `tasks:write` | `writer` | Reopens closed task. |
| `POST /api/tasks/{id}/retry` | `tasks:write` | `writer` | Retries failed/stopped task. |
| `POST /api/tasks/{id}/retry-merge` | `merge:write` | `writer` | Re-queues PR into normal merge checks. |
| `POST /api/tasks/{id}/pull-request` | `merge:write` | `writer` | Opens/refreshes task PR. |
| `POST /api/tasks/{id}/run-now` | `dispatch:write` | `writer` | Immediately dispatches an already-approved task. |
| `POST /api/tasks/{id}/messages` | `tasks:write` | `writer` | Own-task chat message (agent rail applies). |
| `POST /api/tasks/{id}/notes` | `tasks:write` | `writer` | Posts non-parking note on task/session. |
| `POST /api/tasks/{id}/agent-messages` | `tasks:write` | `writer` | Sends addendum message to running agent. |
| `POST /api/tasks/{id}/change-publish` | `tasks:write` | `writer` | Publishes change via `pkg/change` backend. |
| `POST /api/tasks/{id}/change-mail` | `tasks:write` | `writer` | Mails change for review via `pkg/change`. |
| `POST /api/tasks/{id}/change-sync` | `tasks:write` | `writer` | Syncs change status via `pkg/change`. |
| `POST /api/tasks/{id}/wait-on` | `tasks:write` | `writer` | Sets/waits on task dependency. |
| `POST /api/notifications` | `notifications:write` | `writer` | Raises notification in `/inbox`. |
| `POST /api/notifications/{id}/ack` | `internal` | `writer` | Human acknowledgement of inbox notification. |
| `POST /api/notifications/{id}/clear` | `notifications:write` | `writer` | Clears notification. |
| `POST /api/notifications/{id}/investigate` | `internal` | `writer` | Files investigation task from notification. |
| `PUT /api/tasks/{id}/secret` | `secrets:write` | `admin` | Writes secret value requested by `request_secret` into secret store. |
| `POST /api/tasks/{id}/force-merge` | `merge:force` | `admin` | Bypasses CI/review merge gates. |
| `POST /api/tasks/{id}/sandbox/recreate` | `tasks:admin` | `admin` | Destroys and rebuilds sandbox VM/pod (`admin` or own-task `mcpserver`). |
| `POST /api/tasks/{id}/config-change/approve` | `internal` | `admin` | Human `admin` approval of proposed `grain/config` change. |
| `POST /api/tasks/{id}/config-change/reject` | `internal` | `admin` | Human `admin` rejection of proposed `grain/config` change. |
| `GET /api/local-script-approvals` | `internal` | `admin` | Lists pending host-script execution requests. |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/answer` | `internal` | `admin` | Approves or denies host-script execution. |
| `POST /api/tasks/{id}/config-change` | `internal` | `daemon` | `grain mcpserver` `propose_config_change` plumbing (`admin` token). |
| `POST /api/tasks/{id}/activity` | `internal` | `daemon` | `grain mcpserver` live status line (`admin` token). |
| `PUT /api/tasks/{id}/session-title` | `internal` | `daemon` | `grain mcpserver` session title update (`admin` token). |
| `POST /api/tasks/{id}/staged-attachments` | `internal` | `daemon` | `grain mcpserver` attachment staging (`admin` token). |
| `POST /api/tasks/{id}/db/query` | `internal` | `daemon` | `grain mcpserver` `self-debug` read-only SQL query (`admin` token). |
| `POST /api/tasks/{id}/db/execute` | `internal` | `daemon` | `grain mcpserver` `…/grain#sql` `{write: true}` SQL execution (formerly `db-write`, retired in schema migration 82; `admin` token). |
| `POST /api/tasks/{id}/notify` | `internal` | `daemon` | `grain mcpserver` notification relay (`admin` token). |
| `POST /api/tasks/{id}/delivered` | `internal` | `daemon` | `grain mcpserver` message delivery cursor (`admin` token). |
| `POST /api/tasks/{id}/wait-credit` | `internal` | `daemon` | `grain mcpserver` CI/task wait deadline credit (`admin` token). |
| `POST /api/tasks/{id}/change-checks` | `internal` | `daemon` | `grain mcpserver` `pkg/change` check query (`admin` token). |
| `POST /api/tasks/{id}/change-review` | `internal` | `daemon` | `grain mcpserver` `pkg/change` review query (`admin` token). |
| `POST /api/tasks/{id}/local-script-approvals` | `internal` | `daemon` | `grain mcpserver` host-script approval request (`admin` token). |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/wait` | `internal` | `daemon` | `grain mcpserver` host-script wait (`admin` token). |
| `POST /api/tasks/{id}/local-script-approvals/{approval}/expire` | `internal` | `daemon` | `grain mcpserver` host-script expiry (`admin` token). |


#### C. CI, Tests, Merge Batches, and Dispatch Pause (19 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/ci/workflows` | `ci:read` | `reader` | Lists CI workflow definitions. |
| `GET /api/ci/runs` | `ci:read` | `reader` | Lists Grain CI and GitHub Actions runs. |
| `GET /api/ci/faults` | `ci:read` | `reader` | Lists shared CI fault digests. |
| `GET /api/ci/runs/{id}` | `ci:read` | `reader` | CI run detail (`grain` or `gha:*`). |
| `GET /api/ci/runs/{id}/log` | `ci:read` | `reader` | CI job/step log output. |
| `GET /api/ci/runs/{id}/artifacts` | `ci:read` | `reader` | Lists CI run artifacts. |
| `GET /api/ci/runs/{id}/artifacts/{job}/{name}` | `ci:read` | `reader` | Downloads Grain CI artifact. |
| `GET /api/merge-batches` | `ci:read` | `reader` | Merge queue history/batches. |
| `GET /api/base-branch-gates` | `ci:read` | `reader` | Base-branch CI health state. |
| `GET /api/pause` | `dispatch:read` | `reader` | Legacy dispatch pause state. |
| `GET /api/pipeline-pause` | `dispatch:read` | `reader` | Pipeline pause & upgrade-hold status. |
| `POST /api/ci/runs` | `ci:write` | `writer` | Starts or re-runs a CI run. |
| `DELETE /api/ci/runs/{id}` | `ci:write` | `writer` | Cancels an active CI run. |
| `POST /api/repos/{owner}/{name}/base-branch-runs` | `ci:write` | `writer` | Triggers base-branch CI run. |
| `POST /api/repos/{owner}/{name}/release-runs` | `ci:write` | `writer` | Triggers release CI run. |
| `POST /api/tests/quarantine` | `tests:write` | `writer` | Quarantines test and files held repair task. |
| `POST /api/pipeline-pause` | `dispatch:write` | `writer` | Pauses task dispatch across pipeline. |
| `DELETE /api/pipeline-pause` | `dispatch:write` | `writer` | Resumes dispatch (`admin` required if lifting an `admin` scheduled-upgrade hold). |
| `DELETE /api/pause` | `dispatch:write` | `writer` | Legacy dispatch resume (`admin` required if lifting an upgrade hold). |

#### D. Repositories, Releases, and Release Notes (66 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/deployment/repos/{owner}/{name}/config` | `repos:read` | `reader` | Per-repo configuration readback (no secrets). |
| `GET /api/repos/{owner}/{name}/work-ownership` | `repos:read` | `reader` | Repo work-ownership mode. |
| `GET /api/repos/{owner}/{name}/prompt-extension` | `repos:read` | `reader` | Repo prompt extension text. |
| `GET /api/repos/{owner}/{name}/setup-command` | `repos:read` | `reader` | Repo sandbox setup command. |
| `GET /api/repos/{owner}/{name}/declarations` | `repos:read` | `reader` | `.grain/` declarations status. |
| `GET /api/repos/{owner}/{name}/require-branch-up-to-date` | `repos:read` | `reader` | Merge up-to-date setting. |
| `GET /api/repos/{owner}/{name}/merge-on-green` | `repos:read` | `reader` | Merge-on-green setting. |
| `GET /api/repos/{owner}/{name}/base-repair-bypasses-human-gates` | `repos:read` | `reader` | Base-repair gate setting. |
| `GET /api/repos/{owner}/{name}/ignore-github-ci` | `repos:read` | `reader` | Ignore-GitHub-CI setting. |
| `GET /api/repos/{owner}/{name}/ci-mode` | `repos:read` | `reader` | Repo CI mode (`github`, `grain`, `none`). |
| `GET /api/repos/{owner}/{name}/settings` | `repos:read` | `reader` | Combined per-repo settings view. |
| `GET /api/repos/{owner}/{name}/watch-default-branch` | `repos:read` | `reader` | Trunk watch setting. |
| `GET /api/repos/{owner}/{name}/unfixable-checks` | `repos:read` | `reader` | Unfixable check patterns. |
| `GET /api/repos/{owner}/{name}/workflows` | `repos:read` | `reader` | Repo CI workflows. |
| `GET /api/repos/{owner}/{name}/lifecycle` | `repos:read` | `reader` | Repo lifecycle state. |
| `GET /api/repos/{owner}/{name}/branches` | `repos:read` | `reader` | Tracked branches. |
| `GET /api/repos/{owner}/{name}/github-branches` | `repos:read` | `reader` | Forge branch listing for PR/base display. |
| `GET /api/repos/{owner}/{name}/compare` | `repos:read` | `reader` | Pull-request / ref diff view (`/repos/<o>/<n>/compare/...`). |
| `GET /api/repos/{owner}/{name}/capabilities` | `capabilities:read` | `reader` | Repo default task capabilities. |
| `GET /api/repos/{owner}/{name}/ci-capabilities` | `capabilities:read` | `reader` | Repo CI job capabilities. |
| `GET /api/repos/{owner}/{name}/release` | `releases:read` | `reader` | Repo release configuration readback (secret names only). |
| `GET /api/repos/{owner}/{name}/releases` | `releases:read` | `reader` | Lists releases. |
| `GET /api/repos/{owner}/{name}/release-creation` | `releases:read` | `reader` | Release creation preflight/options. |
| `GET /api/repos/{owner}/{name}/releases/{release}` | `releases:read` | `reader` | Release detail. |
| `GET /api/repos/{owner}/{name}/releases/{release}/candidates` | `releases:read` | `reader` | Release candidates list. |
| `GET /api/repos/{owner}/{name}/qualification-plan` | `releases:read` | `reader` | Qualification plan readback. |
| `GET /api/repos/{owner}/{name}/candidates/{id}/qualification` | `releases:read` | `reader` | Candidate qualification run status. |
| `GET /api/release-notes` | `release-notes:read` | `reader` | Weekly/version release notes list. |
| `GET /api/release-notes/stats` | `release-notes:read` | `reader` | Cluster/task stats for release notes. |
| `GET /api/release-notes/{period}` | `release-notes:read` | `reader` | Period release note. |
| `GET /api/release-notes/versions` | `release-notes:read` | `reader` | Versioned release notes list. |
| `GET /api/release-notes/versions/{version}` | `release-notes:read` | `reader` | Specific version release note. |
| `POST /api/repos/{owner}/{name}/import-issues` | `tasks:write` | `writer` | Imports GitHub issues as tasks (``RB-9`.8`). |
| `POST /api/repos/{owner}/{name}/releases` | `releases:write` | `writer` | Starts/cuts a release entry. |
| `DELETE /api/repos/{owner}/{name}/releases/{release}` | `releases:write` | `writer` | Deletes an unpromoted/draft release record. |
| `POST /api/repos/{owner}/{name}/releases/{release}/merge` | `releases:write` | `writer` | Requests release branch merge. |
| `POST /api/repos/{owner}/{name}/releases/{release}/candidates` | `releases:write` | `writer` | Cuts release candidate. |
| `POST /api/repos/{owner}/{name}/releases/{release}/candidates/promote` | `releases:write` | `writer` | Promotes release candidate. |
| `POST /api/repos/{owner}/{name}/pending-releases/{version}/approve` | `releases:write` | `writer` | Approves pending release checkpoint (``RB-9`.6`). |
| `POST /api/repos/{owner}/{name}/pending-releases/{version}/reject` | `releases:write` | `writer` | Rejects pending release checkpoint (``RB-9`.6`). |
| `POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve` | `releases:write` | `writer` | Approves candidate qualification run. |
| `PUT /api/release-notes` | `release-notes:write` | `writer` | Updates release notes document. |
| `GET /api/deployment/reachable-repos` | `repos:read` | `admin` | Enumerates all forge repos visible to configured git credentials (``RB-9`.8`). |
| `GET /api/git-ambient-sources` | `repos:read` | `admin` | Discovers host ambient git credentials for Settings → Git. |
| `POST /api/repos` | `repos:write` | `admin` | Onboards repository to deployment. |
| `DELETE /api/repos/{owner}/{name}` | `repos:write` | `admin` | Removes repository from deployment. |
| `PUT /api/deployment/repos/{owner}/{name}/config` | `repos:write` | `admin` | Updates per-repo config. |
| `PUT /api/repos/{owner}/{name}/work-ownership` | `repos:write` | `admin` | Updates repo work-ownership mode. |
| `PUT /api/repos/{owner}/{name}/prompt-extension` | `repos:write` | `admin` | Updates repo prompt extension. |
| `PUT /api/repos/{owner}/{name}/setup-command` | `repos:write` | `admin` | Updates repo sandbox setup command. |
| `PUT /api/repos/{owner}/{name}/declarations` | `repos:write` | `admin` | Updates `.grain/` declaration sync settings. |
| `PUT /api/repos/{owner}/{name}/require-branch-up-to-date` | `repos:write` | `admin` | Updates merge up-to-date rule. |
| `PUT /api/repos/{owner}/{name}/merge-on-green` | `repos:write` | `admin` | Updates merge-on-green rule. |
| `PUT /api/repos/{owner}/{name}/base-repair-bypasses-human-gates` | `repos:write` | `admin` | Updates base-repair gate rule. |
| `PUT /api/repos/{owner}/{name}/ignore-github-ci` | `repos:write` | `admin` | Updates GitHub CI ignore toggle. |
| `PUT /api/repos/{owner}/{name}/ci-mode` | `repos:write` | `admin` | Updates repo CI mode. |
| `PUT /api/repos/{owner}/{name}/settings` | `repos:write` | `admin` | Updates per-repo settings. |
| `PUT /api/repos/{owner}/{name}/watch-default-branch` | `repos:write` | `admin` | Updates default-branch watcher toggle. |
| `PUT /api/repos/{owner}/{name}/unfixable-checks` | `repos:write` | `admin` | Updates unfixable check patterns. |
| `PUT /api/repos/{owner}/{name}/workflows` | `repos:write` | `admin` | Updates repo CI workflow configuration. |
| `POST /api/repos/{owner}/{name}/lifecycle/refresh` | `repos:write` | `admin` | Refreshes repo lifecycle state. |
| `POST /api/repos/{owner}/{name}/branches` | `repos:write` | `admin` | Creates upstream branch on repository. |
| `PUT /api/repos/{owner}/{name}/capabilities` | `capabilities:admin` | `admin` | Updates repo default capabilities. |
| `PUT /api/repos/{owner}/{name}/ci-capabilities` | `capabilities:admin` | `admin` | Updates repo CI job capabilities. |
| `PUT /api/repos/{owner}/{name}/release` | `releases:write` | `admin` | Configures repo release pipeline (``RB-9`.6`). |
| `PUT /api/repos/{owner}/{name}/qualification-plan` | `releases:write` | `admin` | Configures repo qualification plan (``RB-9`.6`). |

#### E. Schedules, Templates, Suites, Boards, Memory, Console Sessions, and Staging (42 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/schedules` | `schedules:read` | `reader` | Lists schedules. |
| `GET /api/schedules/{id}/composition` | `schedules:read` | `reader` | Shows resolved schedule composition. |
| `POST /api/schedules/composition` | `schedules:read` | `reader` | Read-only composition preview of unsaved schedule draft (`server.go`). |
| `GET /api/templates` | `templates:read` | `reader` | Lists task templates. |
| `GET /api/templates/{id}/composition` | `templates:read` | `reader` | Shows resolved template composition. |
| `GET /api/suites` | `suites:read` | `reader` | Lists qualification/test suites. |
| `GET /api/suites/{id}/composition` | `suites:read` | `reader` | Shows resolved suite composition. |
| `POST /api/suites/composition` | `suites:read` | `reader` | Read-only composition preview of unsaved suite draft. |
| `GET /api/suite-runs` | `suites:read` | `reader` | Lists suite runs. |
| `GET /api/suite-runs/{id}` | `suites:read` | `reader` | Suite run detail. |
| `GET /api/boards` | `boards:read` | `reader` | Lists saved boards/dashboards. |
| `GET /api/memory` | `memory:read` | `reader` | Deployment memory overview. |
| `GET /api/memory/documents` | `memory:read` | `reader` | Lists memory documents. |
| `GET /api/memory/documents/{name}` | `memory:read` | `reader` | Reads a memory document. |
| `GET /api/console-sessions` | `sessions:read` | `reader` | Lists standing console sessions (`director`, etc.). |
| `GET /api/console` | `sessions:read` | `reader` | Lists active terminal consoles. |
| `GET /api/console/{id}/output` | `sessions:read` | `reader` | Reads live console terminal output. |
| `POST /api/schedules/{id}/run` | `schedules:write` | `writer` | Triggers an existing schedule to fire immediately (``RB-9`.7`). |
| `POST /api/suite-runs` | `suites:write` | `writer` | Starts a suite run from an existing suite (``RB-9`.7`). |
| `POST /api/console-sessions/{id}/restart` | `sessions:write` | `writer` | Restarts/resets a console session conversation turn (``RB-9`.4`). |
| `POST /api/console-sessions/{id}/cancel` | `sessions:write` | `writer` | Cancels an in-flight console session turn (``RB-9`.4`). |
| `POST /api/console/{id}/input` | `sessions:write` | `writer` | Sends terminal input (`admin` required if target task holds `IsAdminOnlyGrant`). |
| `POST /api/console/{id}/resize` | `sessions:write` | `writer` | Resizes terminal PTY (`admin` if target task holds `IsAdminOnlyGrant`). |
| `DELETE /api/console/{id}` | `sessions:write` | `writer` | Closes terminal PTY (`admin` if target task holds `IsAdminOnlyGrant`). |
| `/staging/{name}/{path...}` | `internal` | `writer` | Reverse proxy (HTTP, SSE, WebSocket) to persistent staging sandbox (`RB-11`). |
| `POST /api/schedules` | `schedules:write` | `admin` | Creates schedule configuration. |
| `PATCH /api/schedules/{id}` | `schedules:write` | `admin` | Updates schedule configuration (`owner`, cron, grants). |
| `DELETE /api/schedules/{id}` | `schedules:write` | `admin` | Deletes schedule configuration. |
| `POST /api/templates` | `templates:write` | `admin` | Creates task template. |
| `PATCH /api/templates/{id}` | `templates:write` | `admin` | Updates task template. |
| `DELETE /api/templates/{id}` | `templates:write` | `admin` | Deletes task template. |
| `POST /api/suites` | `suites:write` | `admin` | Creates suite definition. |
| `PATCH /api/suites/{id}` | `suites:write` | `admin` | Updates suite definition. |
| `DELETE /api/suites/{id}` | `suites:write` | `admin` | Deletes suite definition. |
| `POST /api/boards` | `boards:write` | `admin` | Creates shared board in `grain/config`. |
| `PATCH /api/boards/{id}` | `boards:write` | `admin` | Updates shared board. |
| `DELETE /api/boards/{id}` | `boards:write` | `admin` | Deletes shared board. |
| `PUT /api/memory` | `memory:write` | `admin` | Edits deployment-wide prompt memory. |
| `PUT /api/memory/documents` | `memory:write` | `admin` | Creates/updates deployment memory document. |
| `POST /api/console-sessions` | `sessions:write` | `admin` | Creates standing console session definition (``RB-9`.4`). |
| `PUT /api/console-sessions/{id}` | `sessions:write` | `admin` | Updates standing console session capabilities/settings (``RB-9`.4`). |
| `DELETE /api/console-sessions/{id}` | `sessions:write` | `admin` | Deletes standing console session definition (``RB-9`.4`). |

#### F. Capabilities, Scope Sets, External-Agent Tokens, Hostds, and MCP (37 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/capability-servers` | `capabilities:read` | `reader` | Lists capability catalog/status for task & picker UI (no secrets). |
| `GET /api/capability-methods` | `capabilities:read` | `reader` | Lists capability methods for picker UI. |
| `POST /api/capability-grants/check` | `capabilities:read` | `reader` | Read-only validation of candidate grants against policy. |
| `GET /api/capability-policy` | `capabilities:read` | `reader` | Reads capability policy rules. |
| `POST /api/capability-policy/preview` | `capabilities:read` | `reader` | Read-only preview of policy evaluation. |
| `GET /api/api-scope-sets` | `capabilities:read` | `reader` | Lists built-in presets and declared API scope sets. |
| `GET /api/external-agents/tokens` | `capabilities:read` | `writer` | `writer` returns **only tokens where `owner == caller`**; `admin` returns all tokens (`RB-14`). |
| `POST /api/external-agents/tokens` | `capabilities:admin` | `writer` | `writer` MAY mint only when `externalAgentApi` is enabled, `owner == caller`, and all scopes are `writer`-covered; `admin` required otherwise (`RB-14`). |
| `POST /api/external-agents/tokens/{id}/rotate` | `capabilities:admin` | `writer` | `writer` MAY rotate only own `writer`-covered token; `admin` may rotate any (`RB-14`). |
| `POST /api/external-agents/tokens/{id}/revoke` | `capabilities:admin` | `writer` | `writer` MAY revoke only own token; `admin` may revoke any (`RB-14`). |
| `DELETE /api/external-agents/tokens/{id}` | `capabilities:admin` | `writer` | `writer` MAY delete only own token; `admin` may delete any (`RB-14`). |
| `POST /api/external-agents/tokens/revoke-all` | `capabilities:admin` | `admin` | Emergency deployment-wide kill switch (`admin` only). |
| `GET /api/capability-servers/gchat-usage` | `capabilities:read` | `admin` | Admin capability-server usage inspection. |
| `GET /api/capability-servers/host-entries` | `capabilities:read` | `admin` | Lists host-script entries on host. |
| `GET /api/capability-servers/usage` | `capabilities:read` | `admin` | Lists cross-deployment capability server usage. |
| `GET /api/api-scope-sets/usage` | `capabilities:read` | `admin` | Lists cross-deployment scope-set usage before deletion. |
| `PUT /api/capability-servers` | `capabilities:admin` | `admin` | Adds/updates capability server. |
| `POST /api/capability-servers/remove` | `capabilities:admin` | `admin` | Removes capability server. |
| `POST /api/capability-servers/enable` | `capabilities:admin` | `admin` | Enables capability server. |
| `POST /api/capability-servers/disable` | `capabilities:admin` | `admin` | Disables capability server. |
| `POST /api/capability-servers/dismiss-notice` | `capabilities:admin` | `admin` | Dismisses capability server notice. |
| `POST /api/capability-servers/rotate-managed` | `capabilities:admin` | `admin` | Rotates managed capability credential. |
| `POST /api/capability-servers/bind-secret` | `capabilities:admin` | `admin` | Binds write-only secret to capability server (``RB-9`.9`). |
| `POST /api/capability-servers/test` | `capabilities:admin` | `admin` | Tests capability server connection. |
| `POST /api/capability-servers/test-script` | `capabilities:admin` | `admin` | Tests host script execution. |
| `POST /api/capability-servers/gchat-test` | `capabilities:admin` | `admin` | Sends test chat message. |
| `PUT /api/capability-policy` | `capabilities:admin` | `admin` | Updates capability policy. |
| `PUT /api/api-scope-sets` | `capabilities:admin` | `admin` | Creates/updates declared API scope set. |
| `POST /api/api-scope-sets/delete` | `capabilities:admin` | `admin` | Deletes declared API scope set. |
| `GET /api/hostds` | `capabilities:read` | `admin` | Lists registered `grain host` instances. |
| `POST /api/hostds` | `capabilities:admin` | `admin` | Registers `hostd` and mints `ghd_…` token (``RB-9`.4`, `RB-14`). |
| `PATCH /api/hostds/{name}` | `capabilities:admin` | `admin` | Updates `hostd` owner/metadata. |
| `GET /api/hostds/{name}/install` | `capabilities:read` | `admin` | Shows a registered `hostd`'s install instructions again (`ui.RegisterHostdInstall`); never its token. |
| `POST /api/hostds/{name}/rotate` | `capabilities:admin` | `admin` | Rotates `ghd_…` token. |
| `POST /api/hostds/{name}/revoke` | `capabilities:admin` | `admin` | Revokes `ghd_…` token. |
| `DELETE /api/hostds/{name}` | `capabilities:admin` | `admin` | Deletes a revoked `hostd` and its `ghd_…` token rows; refuses a live one, `local`, and one a model connection or capability server still names. |
| `POST /api/hostds/delete-revoked` | `capabilities:admin` | `admin` | Deletes every revoked `hostd` nothing still names; lists those skipped. |
| `GET /api/hostd/connect` | `internal` | `protocol` | `hostd` dial-in upgrade; enforces `X-Grain-Hostd-Token` + verified owner (`AU-17`, `AU-18`). |
| `POST /mcp` | `internal` | `protocol` | Streamable HTTP MCP; authenticates `gma_…` and enforces per-tool route role + `OnBehalfOf` intersection (§4). |

*(Note: `POST /api/tasks/{id}/capabilities`, `GET/PUT /api/repos/{owner}/{name}/capabilities`, and `GET/PUT /api/repos/{owner}/{name}/ci-capabilities` are listed in Sections B and D respectively.)*

#### G. Settings, Connections, Secrets, State Repo, Database, Caches, Host, and Upgrades (84 routes)

| Route pattern | `routeScopes` | Role | Conditional rules & notes |
|---|---|---|---|
| `GET /api/settings` | `settings:read` | `reader` | Non-secret operational settings (`System → Health` & UI state). |
| `GET /api/settings/cli-run` | `settings:read` | `reader` | CLI run placement status (`System → Health`). |
| `GET /api/agent-models` | `settings:read` | `reader` | Configured model names/availability for task & New Task display. |
| `GET /api/restart-watch` | `deployment:read` | `reader` | Long-poll notifying open browser tabs of daemon restart (`RB-11`). |
| `GET /api/sandboxes` | `deployment:read` | `reader` | Active/idle sandbox pool counts and status (``RB-9`.10`). |
| `GET /api/persistent-sandboxes` | `sandboxes:read` | `reader` | Persistent sandboxes status list. |
| `GET /api/metrics` | `deployment:read` | `reader` | Deployment throughput/latency/quota metrics (`/metrics`). |
| `GET /api/unmatched-failures` | `deployment:read` | `reader` | Unmatched CI/run failure summary. |
| `GET /api/usage` | `deployment:read` | `reader` | Token usage, budgets and the sustainable-concurrency estimate per connection and model. |
| `GET /api/upgrade` | `deployment:read` | `reader` | Current build version and scheduled-upgrade status. |
| `GET /api/upgrade/targets` | `deployment:read` | `reader` | Upgrade channel/target status. |
| `GET /api/upgrade/release-notes` | `deployment:read` | `reader` | Upgrade release notes. |
| `GET /api/ci/cache` | `caches:read` | `reader` | CI cache usage summary. |
| `GET /api/package-cache` | `caches:read` | `reader` | Package cache usage summary. |
| `GET /api/actions-cache` | `caches:read` | `reader` | Actions cache usage summary. |
| `GET /api/logs` | `deployment:read` | `admin` | Raw controller/daemon journal logs (``RB-9`.10`). |
| `GET /api/logs/{source}` | `deployment:read` | `admin` | Per-source daemon/hostd logs (``RB-9`.10`). |
| `GET /api/persistent-sandboxes/{name}/logs` | `deployment:read` | `admin` | Persistent sandbox service logs (``RB-9`.10`). |
| `GET /api/host/top` | `deployment:read` | `admin` | Host OS process table (``RB-9`.10`). |
| `GET /api/deployment` | `settings:read` | `admin` | Full deployment backend & connection config (`Settings → Models / Git`). |
| `PUT /api/deployment` | `settings:write` | `admin` | Updates deployment backends/connections. |
| `PUT /api/settings` | `settings:write` | `admin` | Updates `grain_config` (`configMode` requires human `admin`, `SC-2`). |
| `PUT /api/usage/settings` | `settings:write` | `admin` | Replaces the token usage settings: concurrency mode, automatic cap floor, retention and connection budgets. |
| `POST /api/settings` | `settings:write` | `admin` | Legacy settings update alias (`admin`). |
| `POST /api/settings/plan` | `settings:read` | `admin` | Previews `grain_config` mutation plan. |
| `PUT /api/settings/cli-run` | `settings:write` | `admin` | Updates CLI run placement settings. |
| `POST /api/deployment/broadcast` | `teams:broadcast` | `admin` | Broadcasts message to every active task across deployment. |
| `GET /api/deployment/git-connections` | `connections:read` | `admin` | Lists git forge connections metadata. |
| `POST /api/deployment/git-connections` | `connections:write` | `admin` | Adds/updates git forge connection. |
| `DELETE /api/deployment/git-connections/{cred}` | `connections:write` | `admin` | Deletes git forge connection. |
| `POST /api/deployment/git-connections/check` | `connections:read` | `admin` | Probes git forge connection credential. |
| `PUT /api/deployment/agent-backends/{backendId}` | `connections:write` | `admin` | Configures model backend. |
| `DELETE /api/deployment/agent-backends/{backendId}` | `connections:write` | `admin` | Removes model backend. |
| `POST /api/deployment/agent-backends/{backendId}/discover` | `connections:write` | `admin` | Refreshes the models the backend's provider advertises (writes only the discovered-models cache). |
| `PUT /api/deployment/repos/{owner}/{name}/git-connection` | `connections:write` | `admin` | Binds git connection to repository. |
| `GET /api/deployment/secret-audit` | `secrets:read` | `admin` | Reads secret access/write audit log. |
| `GET /api/deployment/audit-logs` | `secrets:read` | `admin` | Reads deployment security/config audit logs. |
| `GET /api/agent-keys` | `secrets:read` | `admin` | Lists configured model API key statuses (never values). |
| `PUT /api/agent-keys/{framework}` | `secrets:write` | `admin` | Sets model framework API key. |
| `DELETE /api/agent-keys/{framework}` | `secrets:write` | `admin` | Deletes model framework API key. |
| `POST /api/agent-keys/{framework}/check` | `secrets:read` | `admin` | Probes model framework API key against vendor. |
| `PUT /api/agent-backends/{id}/credential` | `secrets:write` | `admin` | Sets model backend credential. |
| `DELETE /api/agent-backends/{id}/credential` | `secrets:write` | `admin` | Deletes model backend credential. |
| `GET /api/agent-backends/{id}/codex-auth` | `secrets:read` | `admin` | Codex auth status (never secret bytes). |
| `PUT /api/agent-backends/{id}/codex-auth` | `secrets:write` | `admin` | Uploads Codex auth credential. |
| `DELETE /api/agent-backends/{id}/codex-auth` | `secrets:write` | `admin` | Deletes Codex auth credential. |
| `PUT /api/codex-auth` | `secrets:write` | `admin` | Uploads default Codex auth credential. |
| `DELETE /api/codex-auth` | `secrets:write` | `admin` | Deletes default Codex auth credential. |
| `GET /api/gcp-keys` | `secrets:read` | `admin` | Lists `gcp-key` pool metadata (never private keys). |
| `POST /api/gcp-keys` | `secrets:write` | `admin` | Creates `gcp-key` credential entry. |
| `GET /api/gcp-keys/{name}` | `secrets:read` | `admin` | Reads `gcp-key` metadata (never private key). |
| `PUT /api/gcp-keys/{name}` | `secrets:write` | `admin` | Uploads `gcp-key` service-account key. |
| `DELETE /api/gcp-keys/{name}` | `secrets:write` | `admin` | Deletes `gcp-key` entry. |
| `GET /api/github-tokens` | `secrets:read` | `admin` | Lists named GitHub tokens (never token bytes). |
| `PUT /api/github-tokens/{name}` | `secrets:write` | `admin` | Sets named GitHub token. |
| `DELETE /api/github-tokens/{name}` | `secrets:write` | `admin` | Deletes named GitHub token. |
| `PUT /api/repos/{owner}/{name}/release-secrets/{secret}` | `secrets:write` | `admin` | Sets per-repo release secret. |
| `DELETE /api/repos/{owner}/{name}/release-secrets/{secret}` | `secrets:write` | `admin` | Deletes per-repo release secret. |
| `GET /api/secrets` | `secrets:read` | `admin` | Lists secret and key names only (never values, `RB-17`). |
| `PUT /api/secrets/{secret}/{key}` | `secrets:write` | `admin` | Writes encrypted secret key value. |
| `DELETE /api/secrets/{secret}/{key}` | `secrets:write` | `admin` | Deletes secret key. |
| `DELETE /api/secrets/{secret}` | `secrets:write` | `admin` | Deletes secret bundle. |
| `GET /api/state-repo` | `settings:read` | `admin` | Reads `grain/config` state-repo sync status. |
| `GET /api/state-repo/files` | `settings:read` | `admin` | Lists `grain/config` files (`Settings → Config repo`). |
| `GET /api/state-repo/file` | `settings:read` | `admin` | Reads a file from `grain/config`. |
| `POST /api/state-repo/sync` | `settings:write` | `admin` | Forces `grain/config` sync. |
| `POST /api/state-repo/pull-requests` | `settings:write` | `admin` | Opens PR against external `grain/config` repo. |
| `POST /api/state-repo/remote` | `settings:write` | `admin` | Configures `grain/config` remote URL. |
| `POST /api/state-repo/secrets-key` | `secrets:write` | `admin` | Generates/rotates state-repo secrets encryption key. |
| `POST /api/state-repo/import-key` | `secrets:write` | `admin` | Imports state-repo secrets encryption key. |
| `POST /api/state-repo/secrets-key/wrap` | `secrets:write` | `admin` | Wraps the secrets key with a Cloud KMS key, in the daemon (`grain state key wrap`). |
| `POST /api/state-repo/secrets-key/unwrap` | `secrets:write` | `admin` | Puts the secrets key back in plaintext form (`grain state key unwrap`). |
| `POST /api/state-repo/secrets-repo` | `secrets:write` | `admin` | Configures encrypted secrets backup repository. |
| `POST /api/state-repo/reset-notice` | `settings:write` | `admin` | Clears state-repo warning notice. |
| `GET /api/database` | `settings:read` | `admin` | Reads which database backend is in use. No route migrates it: store moves are offline only (docs/database.md). |
| `DELETE /api/ci/cache` | `caches:write` | `admin` | Flushes CI cache. |
| `POST /api/package-cache/flush` | `caches:write` | `admin` | Flushes package cache. |
| `POST /api/actions-cache/flush` | `caches:write` | `admin` | Flushes GitHub Actions cache. |
| `POST /api/upgrade` | `deployment:upgrade` | `admin` | Starts or schedules Grain controller upgrade. |
| `DELETE /api/upgrade/current` | `deployment:upgrade` | `admin` | Cancels an in-progress upgrade before cut-over. |
| `DELETE /api/upgrade/scheduled` | `deployment:upgrade` | `admin` | Cancels scheduled upgrade. |
| `POST /api/host/restart` | `deployment:restart` | `admin` | Restarts Grain controller process. |
| `POST /api/persistent-sandboxes/{name}/{action}` | `deployment:restart` | `admin` | Starts/stops/restarts persistent sandbox. |
| `POST /api/sandbox-probe` | `deployment:restart` | `admin` | Runs live sandbox backend health probe. |
| `POST /api/host/reboot` | `deployment:reboot` | `admin` | Reboots controller host. |
| `POST /api/host/shutdown` | `deployment:reboot` | `admin` | Shuts down controller host. |

### 3.3 WebSocket, SSE, streaming, long-poll, and MCP endpoints {#RB-11}

1. Connection-upgraded, streaming (`http.Flusher`), and long-polling endpoints MUST run `ui.RequirePermission` **before** writing response headers (`200 OK` or `101 Switching Protocols`) or hijacking the socket (`http.Hijacker`):

| Endpoint | Transport | Minimum role / gate | Notes |
|---|---|---|---|
| `GET /api/hostd/connect` | HTTP `Hijack` (framed tunnel) | `protocol` (`admin`-minted `ghd_…` + `AU-18` owner match) | `hostd` tokens can only be created/rotated by an `admin`; remote `hostd` also verifies caller identity == `hostd.Owner`. |
| `/staging/{name}/{path...}` | HTTP, SSE (`text/event-stream`), WebSocket (`101 Switching Protocols`) | `writer` | Enforced before `httputil.ReverseProxy` dials the sandbox; `reader` receives `403` before upgrade. |
| `POST /mcp` | Streamable HTTP JSON-RPC 2.0 | `protocol` (`gma_…` + per-call `RequirePermission`) | `tools/list` filters out tools whose route role exceeds `OnBehalfOf`'s current role; `tools/call` re-checks `RequirePermission` on every invocation. |
| `GET /api/restart-watch` | Long-poll HTTP | `reader` | Used by every open UI tab to detect controller restarts. |
| `GET /api/console/{id}/output` | Polled/streamed console output | `reader` | Read-only terminal output; `POST .../input` requires `writer` (or `admin` if task holds `IsAdminOnlyGrant`). |
| `GET /api/tasks/{id}/attempts/{number}/transcript` | Streamed/chunked transcript | `reader` | Task run transcript. |
| `GET /api/ci/runs/{id}/log` | Streamed CI job log | `reader` | Grain CI and GitHub Actions job logs. |
| `GET /api/logs`, `GET /api/logs/{source}`, `GET /api/persistent-sandboxes/{name}/logs` | Polled/streamed controller & service logs | `admin` | Raw daemon/hostd/service journals (``RB-9`.10`). |

2. The in-sandbox listeners (`127.0.0.1:9728` git/namespace proxy `/fs/<scheme>/...` and `grain mcpserver` over stdio) are never reachable over the UI/API ingress (`AU-21`) and authenticate solely via per-attempt sandbox tokens.

### 3.4 Browser UI views and `grain` CLI subcommands {#RB-12}

1. **Browser UI routes (`ui/src/paths.js`):** The static SPA shell (`/`) loads for any admitted user; data comes from `/api/*`. UI navigation and controls MUST follow `whoami.role`:
   - **All roles (`reader`, `writer`, `admin`):** `/` and `/tasks`, `/tasks/<id>`, `/inbox` (read-only for `reader`), `/director` and `/sessions/<id>` (read-only transcript for `reader`), `/boards` and `/boards/<id>`, `/repos` and `/repos/<owner>/<name>` (including `/releases` and `/compare/<base>...<head>`), `/processes` (`/schedules/<id>`, `/templates/<id>`, `/suites/<id>`, `/suites/runs/<id>`), `/ci/<runId>`, `/metrics` and `/system` (`Health`, `Sandboxes`, `Caches`, `Pipeline`, `Metrics` read-only tabs), and **`/settings/preferences`** (browser-local `localStorage` theme/timezone).
   - **`writer` and `admin`:** `/new` (New Task overlay), task/PR action buttons (`Approve`, `Comment`, `Submit`, `Retry`, `Retry merge`, `Close`, `Reopen`, `Pause`, `Run now`), `director` composer, schedule `Run now`, suite `Start run`, release candidate/approval buttons, and **Settings → Personal API tokens** (managing own `gma_` tokens when `externalAgentApi` is enabled).
   - **`admin` only:** **Settings → Users** (`/settings/users`), all other `/settings/*` sections (`models`, `sandboxes`, `credentials`, `access`, `hostds`, `upgrade`, `caches`, `engine`, `config-repo` / `/config`, `general`, `git`, `repositories`, `tasks`, `agents`, `capabilities`, `ci`, `memory`), `/repos/<owner>/<name>/settings/*` mutation controls, `/system/logs`, `/system/top`, `/system/power`, `Force merge`, and `request_secret` value submission boxes.
   - Navigating directly to an `admin`-only Settings URL as a `reader` or `writer` MUST render an inline `"Admin role required"` empty state naming `whoami.role` rather than blanking or crashing the page.

2. **`grain` CLI subcommands (`pkg/cli/main.go`):**
   - **HTTP-backed task/API commands** authenticate as the caller (IAP OIDC, `gma_…` token, or local `local-api-token` break-glass) and inherit the permission of the underlying HTTP routes:
     - `reader`: `grain whoami`, `grain list`, `grain get`, `grain config`, `grain metrics`, `grain failures`, `grain health`, `grain sandbox-budget`, `grain ci list|show|watch|log|artifact|faults|cache` (read), `grain declarations check` (local file lint), `grain release list|get`, `grain release-notes` (read), `grain host list` (`admin` on server).
     - `writer`: `grain create` (`admin` if `-approve` with `IsAdminOnlyGrant`), `grain update`, `grain approve` (`admin` if task holds `IsAdminOnlyGrant`), `grain withdraw`, `grain defer`, `grain undefer`, `grain capability` (`admin` for `IsAdminOnlyGrant`), `grain comment`, `grain close` / `delete`, `grain reopen`, `grain retry`, `grain retry-merge`, `grain run-now`, `grain pause`, `grain resume`, `grain ci run|cancel`, `grain quarantine`, `grain notify`, `grain release` (operational cut/promote/approve), `grain login --issue-token` (own `writer`-bounded `gma_` token).
     - `admin`: `grain users list|add|set-role|remove` *(new)*, `grain force-merge`, `grain settings`, `grain default-model`, `grain repo`, `grain git-connections`, `grain git-ambient`, `grain gcp-key`, `grain capability-servers`, `grain host register|set-owner|rotate|revoke`, `grain sandbox` (recreate/probe), `grain broadcast`.
   - **Host-filesystem / process commands** (`grain daemon`, `grain controller`, `grain setup`, `grain init`, `grain host`, `grain runhost`, `grain mcpserver`, `grain secrets`, `grain connections`, `grain backup`, `grain export`, `grain import`, `grain state`, `grain sync`, `grain db`, `grain upgrade-postgres`, `grain access-code`) run directly against local host files/databases; accessing them requires OS/container shell access (`kubectl exec` / root), which is break-glass `admin` (`RB-6`).

---

## 4. Agents, Sessions, Tokens, and Delegation

### 4.1 `OnBehalfOf` server-side role intersection {#RB-13}

1. An agent (`PrincipalAgent`) MUST NEVER exercise higher RBAC authority than the person on whose behalf it acts.
2. When `agentAPIGate` (`pkg/ui/agent_api_gate.go`) or `handleMCP` (`pkg/ui/external_agents.go`) evaluates a request from `Actor.Kind == PrincipalAgent`:
   - It MUST read `attr := ui.RequestActor(ctx)` (`attr.Actor` and `attr.OnBehalfOf`, derived strictly from server-side records per [`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md) `OA-2` and `OA-6`, never from caller headers or request bodies).
   - When `attr.OnBehalfOf != nil` and `attr.OnBehalfOf.Verified()`:
     - The server MUST resolve `delegatorRole, err := s.resolvePrincipalRole(ctx, VerifiedIdentity{Email: attr.OnBehalfOf.ID, Kind: attr.OnBehalfOf.Kind})` against the **current** allowlist and bootstrap floor.
     - If `err != nil` or `!delegatorRole.Covers(routeRequiredRole)`, the server MUST refuse the call with `403 Forbidden` (`code: "insufficient_role"`, `requiredRole`, `actorRole: delegatorRole`, plus `delegatedFrom: attr.OnBehalfOf.ID`), **even if** the agent or `gma_` token holds the route's `api:*` scope.
   - Both checks MUST pass (`agentGrants.Allows(routeScope, repo) && delegatorRole.Covers(routeRequiredRole)`).
3. Consequently, demoting `alice@example.com` from `writer` to `reader`—or deleting `user:alice@example.com` from `allowed_principal`—MUST take effect on the very next HTTP or MCP call made by any `gma_` token or console session turn acting on Alice's behalf, without restarting the daemon or revoking tokens manually.

### 4.2 Standing console sessions (`director`), `gma_` tokens, and `hostd` tokens {#RB-14}

1. **Director and standing console sessions:**
   - Posting a message to a standing console session (`POST /api/tasks/{id}/comments`) requires `RoleWriter`. A `reader` viewing `/director` can read `GET /api/tasks/{id}` and `GET /api/console-sessions` but MUST be refused on `POST /api/tasks/{id}/comments` with `403`.
   - Each session turn sets `OnBehalfOf` to the verified human who sent the message that woke the turn (`operator-attribution.spec.md` §2.4; if a turn was woken by an access-code caller, `OnBehalfOf` is `operator` → `RoleAdmin`).
   - During that turn, every `api_*` MCP tool call and `grain` CLI call made by the session passes through `RB-13`:
     - When a `writer` messages the `director`, the `director` can inspect tasks/CI (`reader`) and file/triage tasks (`writer`), cannot approve or queue proposals with `IsAdminOnlyGrant` (`RB-8`), and cannot call `admin` routes even if an `admin` previously attached opt-in `admin` scopes to the `director`.
     - When an `admin` messages the `director`, `delegatorRole == RoleAdmin`, so the `director` may exercise its full configured `ConsoleSession.Capabilities` (still bounded by `director.design.md` §3's permanent trust boundaries: no secret reads, no `autoMerge`/`Submit`/`merge:force`).
2. **`gma_` external-agent tokens (`pkg/ui/external_agents.go`, [`external-cli.design.md`](../../design/external-cli.design.md)):**
   - Every `ExternalAgentToken` row minted by a verified person MUST record `Owner` (`model.Principal` of the creator, immutable after creation). Tokens minted via break-glass access code record `Owner` as `{human, operator, access-code}` (`RoleAdmin`).
   - **Minting and lifecycle by `writer`:** When `grain_config.external_agent_api` is `true`, a `writer` MAY call:
     - `GET /api/external-agents/tokens`: response MUST filter rows to `row.Owner.ID == caller.ID` (case-insensitive);
     - `POST /api/external-agents/tokens`: `Owner` is forced to `caller`; every resolved `APIScope` in `scopeSet` / `scopes` MUST be permitted to `RoleWriter` (specifically excluding all `IsAdminOnlyGrant` scopes: dangerous scopes, `requires_person` sets, and `admin`-only scopes listed in ``RB-7`.1`). Requesting any `admin`-only scope (for example `repos:write`, `schedules:write`, `settings:write`, or `tasks:admin`) MUST fail with `403 Forbidden` (`code: "insufficient_role"`, `requiredRole: "admin"`);
     - `POST /api/external-agents/tokens/{id}/rotate`, `/revoke`, and `DELETE /api/external-agents/tokens/{id}`: permitted to a `writer` if and only if `token.Owner.ID == caller.ID` (and on `rotate`, the token's stored scopes remain `writer`-covered); accessing another user's token ID MUST return `404 Not Found` (avoiding enumeration of other users' token IDs) or `403 Forbidden`.
   - **Minting and lifecycle by `admin`:** An `admin` MAY view, mint (up to `admin` scopes, subject to `AU-11` requiring human/explicit SA for dangerous scopes), rotate, revoke, or delete any `gma_` token, and MAY call `POST /api/external-agents/tokens/revoke-all`.
   - **Runtime execution (`POST /mcp` and `/api/*` with `Bearer gma_…`):** `RequestActor(ctx)` sets `Actor = {agent, "external:" + tok.Name, external-token}` and `OnBehalfOf = &tok.Owner`. Every request enforces `RB-13` against `tok.Owner`'s current RBAC role, and `tools/list` on `POST /mcp` omits tools whose route requires a role above `tok.Owner`'s current role.
3. **`grain host` tokens (`/api/hostds*`):**
   - Only `RoleAdmin` MAY list, register (`POST /api/hostds`), update owner (`PATCH /api/hostds/{name}`), rotate, or revoke `ghd_…` tokens.
   - Note that `hostd.Owner` (`AU-18`) is the identity that the remote `grain host` process itself authenticates as at `GET /api/hostd/connect` (often a dedicated `serviceAccount:` or workstation owner); that `Owner` MUST pass admission (`resolvePrincipalRole != RoleNone`, so a `reader` or `writer` workstation owner or `serviceAccount:...=reader` can connect their admin-registered `hostd` tunnel without needing `RoleAdmin` across the UI/API!).

### 4.3 Autonomous runs, schedules, and `oncall` {#RB-15}

1. Autonomous tasks not initiated by a live HTTP caller—scheduled runs, repository triggers, `base-branch-ci` repairs, and `system:*` jobs—derive their owner per [`attribution.design.md`](../../design/attribution.design.md):
   - A task fired by a schedule resolves `schedule.Owner` at firing time (`grain_config.oncall` when `schedule.Owner == "oncall"` or empty, falling back to `operator` when `oncall` is unset).
   - Child tasks proposed by an autonomous task inherit their parent task's `Owner`.
2. **Grant validity vs. dynamic `OnBehalfOf` calls:**
   - Because creating or editing schedules (`POST`/`PATCH /api/schedules*`), repository triggers/configs, and `grain_config.oncall` all require `RoleAdmin` (`§3.2`), any `Capabilities` attached directly to a schedule or system job (`system:pipeline-steward`, `system:memory-agent`, `self-upgrade`) were explicitly authorized by an `admin` (`GrantByGrain` / schedule config) and execute as configured regardless of who is currently `oncall`.
   - However, if a schedule's configured `Owner` is a specific verified person (not `oncall` / `operator`) and the task spawns proposals or makes dynamic calls attributed `OnBehalfOf` that person, `RB-8` holds any newly proposed `IsAdminOnlyGrant` child task for `admin` approval unless covered by `AutoSubmits` on non-admin grants.

---

## 5. Enforcement, Wire Contracts, Audit, and Read Scoping

### 5.1 Single check point (`ui.RequirePermission`) and `403` refusal contract {#RB-16}

1. `pkg/ui` MUST expose a single authorization entry point used by `Server.route` (and inside handlers with payload-dependent elevation such as `POST /api/tasks/{id}/approve`, `POST /api/tasks/{id}/capabilities`, `POST /api/tasks/{id}/submit`, and `/api/external-agents/tokens*`):
   ```go
   func (s *Server) RequirePermission(ctx context.Context, required model.Role) error
   ```
2. **Request pipeline ordering in `Server.serveHTTP` (`pkg/ui/server.go`):**
   1. `checkCrossOrigin` (`AU-14`);
   2. `verifyCallerNow` (`401` on invalid assertion, `AU-2`);
   3. `authenticateExternalAgent` (if `Bearer gma_…` is present);
   4. `singleTenantGate` (`401` unauthenticated / `403` `"Not allowed on this grain"` when `resolvePrincipalRole == RoleNone`, `AU-6`, `AU-15`) + `checkVerifiedIdentityCSRF` (`AU-14`);
   5. Attach `Principal`, `OnBehalfOf`, and resolved `Role` (`callerRoleFromContext(ctx)`) to `r.Context()`;
   6. **Route-level `RequirePermission(r.Context(), routePermissions[pattern])`** (for human/service callers and `OnBehalfOf` delegation ceiling) **ahead of** `config_refusal.go` (`settings_locked` / `config_mode: simple` checks, [`simple-config.design.md`](../../design/simple-config.design.md) §4.3) and handler execution;
   7. `agentAPIGate` scope, repo, escalation, and self-protection rails (`pkg/ui/agent_api_gate.go`).
3. **`403` JSON refusal payload:** Whenever `RequirePermission` denies a request on `/api/*`, `POST /mcp`, or any non-`GET`/`HEAD` request, the server MUST respond with HTTP `403 Forbidden` (`Content-Type: application/json`) naming both the required role and the caller's effective role:
   ```json
   {
     "error": "admin role required for POST /api/repos (signed in as bob@example.com with role writer)",
     "code": "insufficient_role",
     "requiredRole": "admin",
     "actorRole": "writer"
   }
   ```
   When denial comes from `OnBehalfOf` delegation (`RB-13`), the JSON object MUST also include `"delegatedFrom": "carol@example.com"` and state in `"error"` that the agent acts on behalf of `carol@example.com` (`role reader`).
4. **Browser navigation refusal:** A `GET`/`HEAD` request outside `/api/` is `/`, which serves the SPA (`public`) once `singleTenantGate` admits the user (`Role != RoleNone`), so admitted `reader`s and `writer`s always receive the SPA rather than a raw HTTP error page. Unadmitted principals (`RoleNone`) continue to receive `writePrincipalRefusedPage` (`403 "Not allowed on this grain"`, `AU-15`).

### 5.2 Deployment-wide read scoping and secret non-readability {#RB-17}

1. In basic RBAC, `RoleReader` grants read visibility across **all** repositories (`target_repos`) and tasks on the deployment. List and detail handlers (`GET /api/tasks*`, `GET /api/ci/runs*`, `GET /api/repos*`) MUST NOT filter tasks or repositories by human role (whereas scoped agents and CI tokens continue to enforce per-repository `api:<scope>@owner/name` narrowing in `agentAPIGate`).
2. **Secret values are unreadable by every role:** No route, CLI command over HTTP, or MCP tool MAY return plaintext secret bytes from `<data-dir>/secrets`, Secret Manager, `gcp-key` private keys, `github-tokens`, model API keys, or Codex auth files to any caller—including `RoleAdmin` and break-glass `access-code`. Admins can only list secret names/metadata (`secrets:read`), write/overwrite/delete secrets (`secrets:write`), and bind secret references (`$secret:<name>`) to capability servers.

### 5.3 Role-change audit (`config_audit`) and bounded denial logging {#RB-18}

1. Every successful `POST`, `PATCH`, or `DELETE` on `/api/access/principals` MUST:
   - Append a row to `config_audit` ([`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md) §4.4) with `actor` / `on_behalf_of` from `ui.RequestActor(ctx)`, `surface = "access:principals"`, and a human-readable `summary` naming the target `principal` and role transition (for example `"added user:bob@example.com as writer"`, `"changed user:bob@example.com role from writer to reader"`, `"removed domain:contractor.example.com (was reader)"`);
   - Emit a controller log line: `grain rbac: <actorLabel> <summary>`.
2. Every `403` refusal from `RequirePermission` MUST be logged to the controller journal as:
   ```
   grain rbac: refused <actorLabel> (role=<actorRole>) on <METHOD> <pattern>: requires <requiredRole>
   ```
   To prevent log flooding from polling tabs or misconfigured scripts, identical `(actorKey, pattern, requiredRole, actorRole)` tuples MUST be deduplicated within a bounded window/map (up to 256 keys per `5m` window, matching `AU-19`'s bounded diagnostic discipline).

---

## 6. Migration and UI Surfaces

### 6.1 Existing-deployment migration and role narrowing {#RB-19}

1. **Zero-breakage upgrade rule:** When a deployment upgrades across the schema migration that introduces `allowed_principal`—or starts up with `allowed_principal` empty while an existing `grain_config` row is already present in the database—Grain MUST seed every valid entry from `-ui-allowed-principals` (`$GRAIN_ALLOWED_PRINCIPALS`) into `allowed_principal`:
   - Entries carrying an explicit `=<role>` suffix keep that explicit role;
   - Entries with **no `=<role>` suffix** MUST be seeded with `role = "admin"` (`created_by = "grain (migration)"`), and Grain MUST log a startup notice listing how many principals were migrated as `admin` and pointing to `/settings/users`.
2. Once `allowed_principal` contains at least one row, subsequent restarts MUST treat the database `allowed_principal` table (plus the in-memory `-deployment-owner` and `ProvisionedServiceAccounts` floors of `RB-5`) as authoritative rather than re-seeding removed entries from `-ui-allowed-principals` (`seedOnly` semantics, matching [`settings.spec.md`](../settings/settings.spec.md) §10.1 and [`simple-config.design.md`](../../design/simple-config.design.md) §4.3).
3. **Narrowing workflow for operators:** After upgrading, an `admin` opens **Settings → Users** (`/settings/users`), adds a group or domain rule (for example `group:eng@example.com` → `writer`, `domain:example.com` → `reader`), and either demotes individual `user:` rows from `admin` to `writer`/`reader` or deletes redundant `user:` rows covered by the group rule while lockout protection (``RB-5`.4`) prevents removing the last `admin`.

### 6.2 `GET /api/whoami`, user role badge, and Settings → Users (`/settings/users`) {#RB-20}

1. **`GET /api/whoami` wire contract** (extending [`operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md) `OA-5`):
   ```json
   {
     "actor": {
       "kind": "human",
       "id": "alice@example.com",
       "source": "iap",
       "label": "alice@example.com"
     },
     "onBehalfOf": null,
     "source": "iap",
     "verified": true,
     "allowed": true,
     "role": "writer",
     "deploymentOwner": "owner@example.com"
   }
   ```
   - `role` MUST be the effective `model.Role` (`"admin"`, `"writer"`, or `"reader"`; for an agent with `OnBehalfOf`, the effective `delegatorRole` ceiling).
   - `deploymentOwner` MUST be the normalized `-deployment-owner` email when configured, or `""` otherwise.
2. **`/api/access/principals` wire contract:**
   - `GET /api/access/principals` (`admin`) returns `200 OK`:
     ```json
     {
       "deploymentOwner": "owner@example.com",
       "principals": [
         {
           "principal": "user:owner@example.com",
           "role": "admin",
           "pinned": true,
           "source": "deployment-owner"
         },
         {
           "principal": "group:eng@example.com",
           "role": "writer",
           "pinned": false,
           "source": "database",
           "createdAt": "2026-11-03T08:00:00Z",
           "createdBy": "owner@example.com",
           "updatedAt": "2026-11-03T08:00:00Z",
           "updatedBy": "owner@example.com"
         }
       ]
     }
     ```
   - `POST /api/access/principals` accepts `{"principal": "user:bob@example.com", "role": "writer"}` (`role` optional, defaults to `"reader"`), returning `201 Created` with the created item (`409 Conflict` if `principal` already exists; `400` on invalid syntax; `422` on `gcp-key` pool account).
   - `PATCH /api/access/principals` accepts `{"principal": "user:bob@example.com", "role": "reader"}`, returning `200 OK` (`404` if absent; `422` if violating ``RB-5`.4`).
   - `DELETE /api/access/principals?principal=user%3Abob%40example.com` (or JSON body `{"principal": "..."}`) deletes the row and returns `204 No Content` (`404` if absent; `422` if violating ``RB-5`.4`).
3. **UI user tile and role badge:**
   - Desktop `PageHeader.jsx` (`top-bar-user`) and phone drawer footer `Sidebar.jsx` MUST render the signed-in caller's `whoami.actor.label` alongside a role badge (`admin`, `writer`, or `reader`; muted `admin (access code)` / `admin (local)` on break-glass sessions).
   - Clicking the user tile opens `/settings/users` for an `admin` and `/settings/preferences` (which also displays a read-only `"Your access"` card naming `actor.label`, `source`, and `role`) for `writer` and `reader` users.
4. **Settings → Users (`/settings/users`):**
   - Added under `Secrets & access` in `SETTINGS_GROUPS` (`ui/src/settingsSections.js`, visible in both `full` and `simple` configuration modes to `admin`s, hidden from `writer`s and `reader`s).
   - Displays all principal entries (`user:`, `group:`, `domain:`, `serviceAccount:`), an inline role selector (`admin`, `writer`, `reader`), a lock badge with disabled role/delete controls on `pinned: true` entries and on the sole remaining human `admin`, an `"Add user, group, or domain"` form defaulting the role picker to `reader`, and recent `access:principals` `config_audit` entries.

---

## 7. Verification and Automated Test Suite

### 7.1 Route-coverage totality and `role × route` matrix tests {#RB-21}

1. **Route-coverage totality (`TestEveryRouteHasDeclaredPermission` in `pkg/ui`):**
   - Mirroring `TestEveryMutatingRouteIsAttributed` (`pkg/ui/mutating_routes_test.go`) and `TestRouteScopesAreTotal` (`pkg/ui/agent_api_internal_test.go`), `TestEveryRouteHasDeclaredPermission` MUST walk `Server.Routes()` and fail if:
     - any registered route pattern is missing from `routePermissions`;
     - `routePermissions` contains any pattern not registered on `Server`; or
     - any entry has an unknown permission class outside `{public, protocol, daemon, reader, writer, admin}`.
2. **Table-driven `role × route` permission matrix (`TestRBACRouteMatrix` in `pkg/ui` / `tests/security`):**
   - A table-driven test MUST exercise every `reader`, `writer`, `admin`, and `daemon` route in `Server.Routes()` across at least four authenticated callers (`reader` human via fake `IdentityVerifier`, `writer` human, `admin` human, and break-glass `access-code`), asserting:
     - `reader` succeeds (`!= 401`, `!= 403`) on `public` and `reader` routes and receives `403` (`code: "insufficient_role"`, `requiredRole` ∈ `{"writer", "admin"}`, `actorRole: "reader"`) on every `writer`, `admin`, and `daemon` route;
     - `writer` succeeds on `public`, `reader`, and `writer` baseline routes and receives `403` (`code: "insufficient_role"`, `requiredRole: "admin"`, `actorRole: "writer"`) on every `admin` and `daemon` route;
     - `admin` and break-glass `access-code` pass `RequirePermission` on all routes.
   - Payload-conditional elevation cases MUST have dedicated subtests:
     - `writer` calling `POST /api/tasks` (`approved: true`), `POST /api/tasks/{id}/approve`, or `POST /api/tasks/{id}/capabilities` with each class of `IsAdminOnlyGrant` (`api:settings:write`, `api:repos:write`, `priority-dispatch`, `durable`, `…/grain#host-exec`) → `403` (`requiredRole: "admin"`), while `POST /api/tasks` (`approved: false`) holds in `proposed` with `CapabilityEscalationNote`;
     - `writer` calling `POST /api/tasks/{id}/submit` on an ordinary repo → `200`, vs. on `grain/config` (`state_repo`) → `403` (`requiredRole: "admin"`);
     - `writer` calling `/staging/web/ws` (`Upgrade: websocket`) → allowed, vs. `reader` calling `/staging/web/ws` → `403` before `101` upgrade.

### 7.2 Delegation, break-glass, lockout, migration, and UI tests {#RB-22}

1. **Agent delegation & dynamic demotion (`TestRBACAgentDelegationIntersection`):**
   - A console session (`director`) holding `api:tasks:write` and `api:settings:write` woken by a `reader` (if forced in store) cannot execute `tasks:write` or `settings:write` (`403`, `delegatedFrom`); woken by a `writer`, it can execute `tasks:write` (`POST /api/tasks` without admin grants) and is refused `PUT /api/settings` and `POST /api/schedules` (`403`, `requiredRole: "admin"`, `delegatedFrom`); woken by an `admin`, both scopes work.
   - A `writer` mints a `gma_` token (`grain/external-dev`), verifies `POST /mcp` `tools/call` `api_create_task` succeeds, an `admin` patches that user's role to `reader` via `PATCH /api/access/principals`, and the **next** `POST /mcp` `api_create_task` call with the same `gma_` token fails with `403 insufficient_role` while `tools/list` drops write tools.
   - A `writer` attempting to mint a `gma_` token with `api:settings:write` or `api:repos:write`, or attempting to rotate/revoke/delete another user's `gma_` token or call `revoke-all`, is refused (`403`/`404`).
2. **Resolution, Cloud Identity fallback, break-glass, and lockout (`TestRBACRoleResolutionAndLockout`):**
   - Multi-match precedence (`domain:example.com=reader` + `group:eng@example.com=writer` + `user:alice@example.com=admin` → `admin`; `domain:example.com=admin` + `user:bob@example.com=reader` → `admin`);
   - Direct `admin` skips `GroupMemberChecker`; `GroupMemberChecker` timeout when `domain:example.com=reader` matched falls back to `reader` and denies `writer`/`admin`;
   - Break-glass `access-code` and loopback under `-ui-require-iap` resolve to `admin` and record `operator (access code)` / `operator (local)`;
   - `gcp-key` sandbox pool account matched by `group:eng@example.com=admin` still resolves to `RoleNone` (`403`);
   - `/api/access/principals` refuses (`422`) deleting/demoting `-deployment-owner`, removing the last human `admin`, or self-demoting an `admin` not covered by `-deployment-owner`.
3. **Migration & audit (`TestRBACLegacyAllowlistMigrationAndAudit`):**
   - Upgrading a populated store with `-ui-allowed-principals=user:alice@example.com,group:eng@example.com` seeds both as `admin`, whereas a brand-new store seeds bare entries without `=<role>` as `reader` (while `-deployment-owner` stays `admin`);
   - Every `/api/access/principals` mutation writes a `config_audit` row, and repeated `403 insufficient_role` denials log once per deduplication key.
4. **Frontend unit & Playwright E2E tests:**
   - Vitest component tests for `PageHeader.jsx`, `Sidebar.jsx`, `UsersSection.jsx` (`/settings/users`), and role-disabled task/PR/director/settings controls;
   - Playwright spec over `grain demo` verifying `admin`, `writer`, and `reader` views at desktop and `390 px` phone widths in light and dark themes.

---

## 8. Requirement Traceability Summary

| ID | Requirement | Status | Planned verification |
|---|---|---|---|
| `RB-1` | `model.Role` (`admin > writer > reader`) and `Covers` ordering | Superseded | `pkg/model/rbac_test.go` |
| `RB-2` | `allowed_principal` SQL table; excluded from `SettingsTables`, in `BackupOnlyTables` | Superseded | `pkg/model/sqlite`, `pkg/model/postgres`, `pkg/staterepo` tests |
| `RB-3` | Entry syntax (`=<role>`), `reader` default on new entries, `gcp-key` exclusion | Superseded | `pkg/ui/allowlist_test.go`, `pkg/daemon` startup tests |
| `RB-4` | Highest-role-wins resolution & selective `GroupMemberChecker` short-circuit | Superseded | `TestRBACRoleResolutionAndLockout` |
| `RB-5` | Immutable `-deployment-owner` floor & 3 lockout-prevention invariants (`422`) | Superseded | `TestRBACRoleResolutionAndLockout` |
| `RB-6` | Break-glass `access-code`, `loopback`, and `in-process` resolve to `admin` (`operator`) | Superseded | `TestRBACRoleResolutionAndLockout`, `TestRBACRouteMatrix` |
| `RB-7` | `model.IsAdminOnlyGrant` classification | Superseded | `pkg/model/rbac_test.go` |
| `RB-8` | Writer task filing/approval hold-for-approval & `grain/config` `Submit` guard | Superseded | `TestRBACRouteMatrix`, `pkg/ui/rbac_tasks_test.go` |
| `RB-9` | Normative resolution of the 11 hard-case boundaries | Superseded | `TestRBACRouteMatrix` |
| `RB-10` | Exhaustive 332-route HTTP permission matrix | Superseded | `TestEveryRouteHasDeclaredPermission`, `TestRBACRouteMatrix` |
| `RB-11` | Pre-upgrade enforcement on WebSocket, SSE, streaming, long-poll, and `/mcp` | Superseded | `TestRBACRouteMatrix`, `pkg/ui/persistent_sandboxes_test.go` |
| `RB-12` | Browser UI route gating (`/settings/preferences` open) & CLI verb mapping | Superseded | Vitest, Playwright, `pkg/cli` tests |
| `RB-13` | Server-side `OnBehalfOf` role intersection with immediate demotion effect | Superseded | `TestRBACAgentDelegationIntersection` |
| `RB-14` | Director turn scoping, `writer` self-service `gma_` tokens, `admin`-only `hostds` | Superseded | `TestRBACAgentDelegationIntersection`, `pkg/ui/external_agents_test.go` |
| `RB-15` | Autonomous schedule/trigger owner & `oncall` role rules | Superseded | `pkg/orchestrator/schedule_rbac_test.go` |
| `RB-16` | `ui.RequirePermission` single check point & structured `403` JSON payload | Superseded | `TestEveryRouteHasDeclaredPermission`, `TestRBACRouteMatrix` |
| `RB-17` | Deployment-wide read scoping for `reader` & universal non-readability of secret values | Superseded | `TestRBACRouteMatrix`, `tests/security/secret_audit_and_scoping_test.go` |
| `RB-18` | `config_audit` recording on role changes & bounded deduplicated denial logs | Superseded | `TestRBACLegacyAllowlistMigrationAndAudit` |
| `RB-19` | Existing-deployment `admin` migration & `seedOnly` database authority | Superseded | `TestRBACLegacyAllowlistMigrationAndAudit` |
| `RB-20` | `GET /api/whoami`, `/api/access/principals`, role badge, and `/settings/users` UI | Superseded | `pkg/ui/whoami_test.go`, Vitest, Playwright |
| `RB-21` | `TestEveryRouteHasDeclaredPermission` & `role × route` matrix test | Superseded | `pkg/ui/rbac_routes_test.go` |
| `RB-22` | End-to-end delegation, lockout, break-glass, migration, and UI tests | Superseded | `pkg/ui`, `tests/security`, `ui/e2e` |

---

## 9. Sources

- Design document: [`design/design/rbac.design.md`](../../design/rbac.design.md)
- Companion designs & specs:
  - [`design/design/authentication.design.md`](../../design/authentication.design.md) and [`design/spec/authentication/authentication.spec.md`](../authentication/authentication.spec.md)
  - [`design/design/operator-attribution.design.md`](../../design/operator-attribution.design.md) and [`design/spec/operator-attribution/operator-attribution.spec.md`](../operator-attribution/operator-attribution.spec.md)
  - [`design/design/attribution.design.md`](../../design/attribution.design.md)
  - [`design/design/simple-config.design.md`](../../design/simple-config.design.md)
  - [`design/design/hub.design.md`](../../design/hub.design.md)
  - [`design/design/director.design.md`](../../design/director.design.md) and [`design/spec/director/director-capabilities.spec.md`](../director/director-capabilities.spec.md)
  - [`design/design/external-cli.design.md`](../../design/external-cli.design.md) and [`docs/agent-api.md`](../../../docs/agent-api.md)
- Route and policy tables inspected on `main`: `pkg/ui/server.go`, `pkg/ui/route_scopes.go`, `pkg/ui/agent_api_gate.go`, `pkg/ui/agent_api_tools.go`, `pkg/ui/allowlist.go`, `pkg/ui/accesscode.go`, `pkg/ui/request_actor.go`, `pkg/ui/external_agents.go`, `pkg/model/agent_api.go`, `pkg/model/auto_submit.go`, `pkg/model/priority_dispatch.go`, `ui/src/paths.js`, `ui/src/settingsSections.js`, `pkg/cli/main.go`.
