# Roles for allowed users, groups and service accounts

Status: implemented with this doc (task 2969).

Until now the database allowlist (`allowed_principal`, Settings → Access,
`grain access allow|deny|list`) admitted people at full operator
privilege or not at all. This doc gives every entry a **role**. A role
is an API scope set, the same object that already names an agent's
grants (`docs/agent-api.md`, "Scope sets"). A verified-identity session
is then checked against its role's scopes by the same route table that
checks an agent. No second permission system is added.

## 1. Model

- `allowed_principal` gains a `role` column (schema 110). It holds a
  scope set name, built-in or declared, and defaults to the new built-in
  `grain/operator`, which resolves to every scope of the vocabulary.
  Every existing row migrates to `grain/operator` through the column
  default, so nobody loses access on upgrade.
- **Roles can carry repository limits, through the set's own `repos`**
  (and through the limits its includes carry). A set
  `{name: "acme/ci", scopes: ["ci:*", "tasks:read"], repos: ["acme/widgets"]}`
  given as a role reaches CI and tasks of `acme/widgets` only. We
  considered a per-entry repo list and rejected it. It would be a second
  place where reach is defined, and the state check could not report it,
  because the allowlist is not exported (§9).
- A role names a set by name, so it is resolved when a request arrives
  (§5). When a set is edited, every entry holding it widens or narrows
  at once, with no restart.
- A role whose set no longer exists still admits the entry, but it
  resolves to no scopes at all. The person can sign in and see their own
  role (`/api/auth/user`), and every scoped route refuses them, naming
  the missing set. This fails closed: deleting a set never widens
  anybody's access.

## 2. Several matching entries

A person can match several entries: a direct `user:` entry, plus every
`group:` entry that Cloud Identity says they belong to. Service accounts
match only their own `serviceAccount:` entry, because groups never admit
a service account (unchanged).

- **Grants are a union.** Each matching role turns into the api grant
  `api:<role>`, and the person's scopes are `APIGrantsOf` of all of them.
  This is the same function that reads a task's grants. Repository limits
  are kept per scope:
  - A scope that any matching role grants on every repository reaches
    every repository.
  - Otherwise, the scope reaches the union of the repositories the roles
    name for it.
- **A `deny` overrides every other grant (deny-overrides).** A set's
  `deny` already removes scopes inside that set's own resolution. For a
  role, the patterns in its `deny` (and in the `deny` of every set it
  includes, transitively) also form a deny list for the person. A scope
  that any matching role denies is withheld, even when another matching
  role grants it. This is the safe reading. Joining a group can then only
  ever narrow what its `deny` names, and an operator who wants "never
  secrets for contractors" writes it once on the contractors' role. If
  you only want a narrower set, list its scopes, as the built-ins below
  do. Do not write a `deny`: in a role, a deny is a veto.
- Admission does not change: any one matching entry admits. Role
  resolution also asks Cloud Identity about every `group:` entry. It
  stops asking as soon as a direct entry has already resolved to
  `grain/operator`, because nothing can widen that. A group lookup that
  fails leaves out that group's role but does not refuse a person who is
  admitted another way. Leaving out a role that only grants can only
  narrow access. A role that denies scopes is different: leaving out its
  veto would widen the person, so a failed lookup of a group whose role
  denies anything refuses the request instead.

## 3. Who administers

- **Access-code sessions are unlimited admins.** The same goes for the
  other callers grain trusts as itself: break-glass over loopback, and
  in-process callers. They hold no role and pass no scope check. They
  can add and remove any entry and assign any role.
- **A verified session whose role holds `access:write`** can add and
  remove entries and assign roles, but only roles no wider than its own.
  A role is no wider when the caller holds every one of its resolved
  scopes on every repository that scope reaches. The caller cannot
  assign a role that is marked "only a person may grant" (§4), and it
  cannot change or remove an entry whose current role is wider than its
  own, is marked that way, or denies scopes (lifting a deny widens
  everybody the entry matches, perhaps the caller too, and a deny grants
  nothing for "no wider" to compare). Only an unlimited admin can do
  those things.
- **Agents and sandboxes may not change the list by any route.**
  `POST`, `PATCH` and `DELETE /api/access/principals` refuse an acting
  task, a console session and an external agent token, whatever api
  grants it holds. The sandbox channel already refused them through
  `access:write` being dangerous. Before this change, an agent with a
  person-granted `api:access:write` could add entries. That is removed:
  who may sign in is decided by people only. Agents keep `access:read`.

## 4. Dangerous scopes and `requiresPerson`

For an agent, a set marked "only a person may grant it" (`Person`,
forced on by any dangerous scope) means exactly that. For a human role,
it means **assigning the role needs an unlimited admin**, and the
assignment must carry a reason (the request's `reason`, or the
`X-Grain-Acting-Reason` header). The reason is recorded in
`config_audit` with the change. This applies to the default as well:
`grain access allow user:x` without `--role` assigns `grain/operator`,
so it needs the access code and a `--reason`. The one-time flag import
is automation and records its source instead.

An access-code admin is no longer anonymous. The change is recorded
under the name the code's session signed in with, `access-code:<name>`
([access-code.md](../access-code.md#who-the-code-is-recorded-as)), and
every other mutation the code makes goes into the same access history
under the surface `access-code`. In the code's **recovery** mode
(`grain access-code mode recovery`), managing access and settings is
all a code session may do once a person with a full role can sign in
with a verified identity. So the code stays the way back in for role
changes, but it stops being an everyday admin credential.

Holding a dangerous scope through a role does not make a person give a
reason for each call. Per-call reasons stay an agent rule.

## 5. Enforcement

Admission happens in `singleTenantGate` and is unchanged. When it admits
a verified identity, it also resolves the role (§2). It does this per
request, from the database and the scope sets in effect, so a role
change takes effect on the next request. The result is stored on the
request context as the caller's **role grants**.

`Server.route` wraps every route in a **role gate** ahead of
`agentAPIGate`. Requests with no role grants pass straight through:
access code, loopback, in-process callers, and agents (whose own gate
follows). Requests with role grants are checked as follows:

| Route class | How a limited role is gated |
|---|---|
| JSON API routes with a grantable scope (`route_scopes.go`) | The route's scope is checked on the route's repository, which is found the way an agent call's is (`agentCallRepo`: path, body `repo`/`taskId`, `?repo=`, a CI run's repo). A role limited to some repositories that calls a collection without `?repo=` gets the single repository it reaches, or is refused when it reaches several. |
| Task, template, schedule, suite and session filings and edits (`tasks:write`, `tasks:from-template`, `capabilities:admin`, `templates:write`, `schedules:write`, `suites:write`, `sessions:write`) | Held to the role's own scopes and repository limits by the shared grant- and filing-escalation pass (`filingEscalation`, `roleGrantEscalation`). A limited role may grant only non-dangerous capabilities its own role covers (never a dangerous grant or a `Person` scope set), may not turn on `autoMerge` or `submit`, needs `tasks:admin` on the repository for `approved`/`interactive`/`priority` above default/`fixesBaseBranch`/`autoApproveProposals`, needs `capabilities:admin` on the repository to edit a task's grants (`PATCH /api/tasks/{id}`, `POST /api/tasks/{id}/capabilities`), and may set `writes`/`reads` only to repositories its `tasks:write`/`tasks:read` reach. Refusals name the caller's role and the first refused key. |
| Approving a stored task (`POST /api/tasks/{id}/approve`, and `POST /api/tasks/{id}/run-now` on a task with no approval yet, since Run now approves it first) | Held to what the role could have filed approved itself (`roleApprovalWithin`): `tasks:admin` on the task's repository (on every repository for a task with none), and a task carrying `autoMerge`, `keepRunning`, a `grain/config` or person-approved change target, or `networkAccess` wider than the deployment's default needs an unlimited role (`grain/operator`). Every grant and the task's `admission` must be one the role may grant, its `writes`/`reads` must be repositories its `tasks:write`/`tasks:read` reach, and a review template other than the repository's default must itself be within the role. Running an already approved task now needs only `dispatch:write`. A `gma_` token presented inside a verified session is intersected with that session's role first, so a wider token cannot approve for it. |
| `internal` routes a person uses (sign-in, whoami, approving a config change, granting an access request, acknowledging a notification, answering a local-script approval) | Each one has an entry in `humanRouteScopes`, either a grantable scope or `signed-in` (any admitted caller). An `internal` route with no entry there is refused. |
| HTML pages and the SPA's static files (the `/` handler) | Any admitted caller. They hold no data; every datum comes from a scoped API route. |
| SSE and event streams, log tails (`/api/console/{id}/output`, `/api/restart-watch`, `/api/logs/...`) | These are ordinary routes in the table and are gated by their scope like any JSON route. |
| Terminal and console input (`/api/console/...`) | `sessions:read` to watch, `sessions:write` to type. Already in the table. |
| File downloads (`/api/fs/...`, CI artifacts and logs) | Their table scope, on the repository the path names. |
| `/api/mcp`, `/mcp` | Open to any signed-in role at the outer route (`humanRouteScopes`: `signed-in`), and checked per tool call against the caller's role on the request context (`accessRoleFromContext`) with the same `authorizeRoleCall` check as the route the tool maps to. Discovery (`grain_explore_scope` on `/api/mcp`, `tools/list` on `/mcp`) and `grain_filing_context` expose only scopes and repositories the role covers; invoking an unauthorized tool is refused naming the caller's role and the missing scope. |
| `/staging/{name}/...` (persistent sandboxes' running services) | Refused to any role that does not resolve to every scope. |
| hostd data plane (`/api/hostd/connect`, `/hostd/install`, `/api/hostd/install.sh`, `/api/hostd/join*`, `/api/hostd/renew`) | Authenticate in their own protocol with the host's credential and Ed25519 proof-of-possession signature (or verified enrolling identity on `/api/hostd/join`). Viewing/managing enrollment codes and accepting/rejecting pending join requests require the deployment owner (`IsDeploymentOwner`); host registry lifecycle actions require `capabilities:admin`. |
| CI data plane (`/cli/ci/...`, sandbox proxy) | Served by the sandbox proxy to run tokens, never to a verified session. Not affected. |
| `PUT /api/config` (registered outside `route`) | Gated as `PUT /api/settings` (`settings:write`). |

**Fail closed.** A route with no scope in the table, or an `internal`
route with no `humanRouteScopes` entry, is refused to every limited
role. `TestRoleGateCoversEveryRoute` holds the classification total.
Every registered pattern must have a grantable scope or a
`humanRouteScopes` entry, or appear in the short `operatorOnlyRoutes`
list.

A person who sends `X-Grain-Acting-Task` is held to both their role
**and** the task's grants. The role gate runs first, so the header can
never widen a person.

## 6. UI

`GET /api/auth/user` and `GET /api/config` report the caller's access:

```json
"access": {"role": "grain/viewer", "roles": ["grain/viewer"], "full": false,
           "scopes": ["tasks:read", ...], "repoLimits": {"ci:read": ["acme/widgets"]}}
```

`access` is absent for an unlimited admin. The UI reads it through
`useAccess()`/`can(scope)`. It shows the caller's role in Settings →
Access, and it hides or disables actions the caller's scopes don't
cover: adding, removing and re-roling entries without `access:write`.
The server stays the enforcement point. Hiding a button is a courtesy,
not a check.

## 7. Service accounts

Task 2942 admits `serviceAccount:` entries at full operator. That stays
the default (`grain/operator`). An operator can give a service account
entry a narrow role instead, for example a CI-only set
(`grain access allow serviceAccount:ci@p.iam.gserviceaccount.com
--role acme/ci-bot --reason "release pipeline"`). Two things stay true
whatever role is set:

- gcp-key pool accounts, and every account whose key grain hands to
  sandboxes, are refused before the role is ever read. This is
  unchanged.
- Daemon-provisioned accounts (`grain-daemon@<project>`) are grain's own
  and keep resolving to `grain/operator`.

## 8. Attribution and audit

A refusal is a 403 in the same shape as an agent refusal:

> `alice@example.com (role grain/viewer) holds no scope tasks:write, which POST /api/tasks needs: ask an access-code admin to assign a role that includes it (docs/access-code.md)`

A repository refusal names the repository instead. Refusals are logged
as `grain api: refused {human, alice@example.com} ...`. Every role
assignment, at add time or by `PATCH`, is recorded in `config_audit`
under surface `access`, with the old and new roles and the reason, for
example `role user:bob@x.com grain/operator -> grain/viewer (rotation)`.

Tasks created by a verified session (`user:` or `serviceAccount:`) record
that identity as both `Task.Proposer` (shown on the timeline's **Proposed**
step) and the default `Task.Owner` (unless an explicit `owner` is given on
`POST /api/tasks` or `grain create -owner`). Any caller whose role grants
`tasks:write` on the task's repository (other than an agent or external-agent
token, which are refused on `owner`) may reassign `owner` via
`PATCH /api/tasks/{id}`, `grain update -owner`, or the task detail page,
recording an `owner_changed` audit step with the old owner, new owner, and
actor. `GET /api/tasks?owner=<account>` and `grain list -owner <account>`
accept `me` as a shorthand for the caller's own account.

## 9. State repository

The entries and their roles stay out of the export, as `allowed_principal`
already is (`pkg/staterepo` excluded tables). The reason is unchanged: a
bad or compromised pull request against grain/config must not be able to
grant sign-in or lock operators out. What a role *means*, its scope set,
is exported already as `tables/api_scope_set.json`. The state check's
existing scope-set diff (scopes added or removed, repository reach
moved, the person flag) is therefore also the report of how every role
built on that set changes. `docs/agent-api.md` says so, so a reviewer
knows that a set change also widens or narrows people who hold it as a
role.

## 10. Built-in roles

These are shipped alongside the preset built-ins and are read-only:

| Role | Resolves to | Person |
|---|---|---|
| `grain/operator` | Every scope of the vocabulary (each area's `:*`), never `internal`. | yes |
| `grain/viewer` | Every `*:read` scope (`tasks:read`, `ci:read`, `settings:read`, `access:read`, `secrets:read` (names only), `deployment:read`, `sandboxes:read`, ...). Nothing dangerous. | no |
| `grain/contributor` | `grain/viewer` plus `tasks:write` and `ci:write`. | no |

Deployments compose their own roles from these (`includes:
[grain/viewer]`, `repos: [...]`).

## Not done here

- Hiding every action across every UI page. The capability helper is in
  place and the Access page uses it. Other pages adopt it as they are
  touched, and the server refuses regardless.
