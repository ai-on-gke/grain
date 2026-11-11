# Route authorization: role × route matrix, horizontal access, escalation (2026-11-10)

Task 3981, one of the operator's 2026-11-10 set ("non-admin users cannot
escalate their permissions or get access to credentials"; see also
#3965 insider risk, #3873 identity-less access, #3864 admin role edits).

## Summary

| # | Finding | Severity | Status |
|---|---------|----------|--------|
| F1 | `GET /api/inbox` showed a **repository-limited role** (a person whose allowlist role is limited to some repositories) the inbox of **every** repository: task ids, titles, questions and the names of pending secrets. `inboxReadable` narrowed only `gma_` credentials. | Medium (confidentiality across a repository limit) | **Fixed** in this change (`pkg/ui/external_cli.go`): the inbox now narrows through `eventsReadable`, the narrowing the event stream already used for roles and agents. Regression: `TestHorizontalObjectAccess`. |
| F2 | `POST /api/api-scope-sets/delete` of a built-in set (`grain/…`) answered **500** instead of a 4xx. It refuses everybody, including the access code, so nothing was deleted. | Low (wrong status, no access) | **Fixed**: now a `ValidationError` (400). |

Nothing else turned up: no route answered a caller the scope tables
refuse, no limited caller was answered a body holding the access code or
a `gma_` credential, and every escalation attempt below was refused.
Every task-object route refused a caller limited to another repository.

## 1. The matrix: `TestRouteAuthorizationMatrix`

`tests/security/route_matrix_test.go`. It covers every pattern
`Server.Routes()` lists (379 with `PUT /api/config`, which `server.go`
registers outside `Server.route`, gated as `PUT /api/settings`). It
calls each one as each of 28 callers:

- **Every built-in scope set as an allowlist role.** That is the four
  access roles (`grain/operator`, `grain/viewer`, `grain/contributor`,
  `grain/delegated`) and all 15 presets (`grain/task-admin`,
  `grain/ci-read`, `grain/external-read`, …). The list is read from
  `model.BuiltinAPIScopeSets()`, so a new built-in becomes a row as soon
  as it ships.
- **Custom sets:** one scope (`tasks:read`), one scope (`tasks:write`), a
  set limited to `acme/widgets`, and three narrow administrators
  (`capabilities:admin`, `access:write`, `sessions:write`).
- **An agent run** acting for a task that holds `api:tasks:read` and
  `api:tasks:write`. It calls over the sandbox CLI channel, the way
  `gitproxy.forwardToAPI` turns a run's per-attempt proxy token into a
  call.
- **A `gma_` external-agent credential** holding `tasks:read` and
  `ci:read`.
- **No identity**, from the open internet.

What each caller is owed is **computed from the tables the gates read**,
never from what a handler happens to do:

- **People:** `ui.RoleScopeOf`, which is `route_scopes.go`,
  `humanRouteScopes` and `operatorOnlyRoutes` together.
- **Agents and `gma_` credentials:** `ui.AgentAPIRoutes()`, plus
  `ui.IsSandboxDeniedScope` on the sandbox channel.

A route missing from `routeAuthorization` fails the test, as it already
fails `TestRouteAuthorization`. `TestRouteScopesAreTotal` and
`TestRoleGateCoversEveryRoute` in `pkg/ui` keep the scope tables total.

The test asserts three things:

1. A caller the tables refuse is answered 401 or 403 (8,359 probes).
2. A caller the tables allow is not refused, unless the access code is
   refused the same probe, which means the fixture's object decided and
   not the caller (1,777 probes).
3. No caller short of a full role or the code itself is ever answered a
   body holding the access code (`LocalAPIToken`) or the `gma_`
   credential.

`matrixNotDecidedByScope` lists the routes where a rail beyond the scope
refuses some callers the scope allows. It skips only the "allowed" half
on those routes, and gives each one's reason. For example: `submit`
turns on auto-merge; `run-now` approves; agents read only their own
messages; model-only control actions are refused on the sandbox
channel. Every entry refuses more; none answers less.

**Not driven by the matrix, and why:**

- **Git proxy and sandbox `/api/` forwarding.** `ServeAPIHTTP` strips
  `Authorization` and every `X-Grain-*` header, then sets the acting task
  from the proxy token. `pkg/gitproxy/cli_actions_test.go` covers this,
  including a spoofed `X-Grain-Acting-Task`, and
  `tests/security/gitproxy_test.go` covers token binding.
- **The MCP sandbox proxy** (`controllerMCPProxy`). It always sets the
  acting task. `POST /api/mcp` is checked per tool call against the
  task's grants or the caller's role (`mcp_tools_test.go`,
  `TestMCPPerToolRoleChecks`). The matrix's agent row uses the sandbox
  channel. The horizontal and escalation tests also call as an agent
  in this controller form: the acting task alone, with no channel.
- **The hostd websocket and enrollment routes.** These authenticate in
  their own protocol (`hostd_*_test.go`).
- **SSE and terminal routes** (`/api/events`, `/api/console/{id}/output`,
  `/api/console/{id}/input`, `/api/restart-watch`) are ordinary rows of
  the matrix. `serveUntilAnswered` reads the status the stream opened
  with.

## 2. Horizontal access

grain is single-tenant. What scopes an object to a caller is the
**repository**, and for agents their **own task**. Who filed the object
does not: `Task.Owner` (#3238) is attribution and a list filter, not an
access boundary (`docs/design/access-rbac.md` §8).

| Object | Scoped by | Held by |
|---|---|---|
| Tasks, comments, messages, attachments, attempts and transcripts, capability audit, goal, PR, sandbox recreate (`/api/tasks/{id}/…`) | The task's repository, for roles, agents and `gma_` credentials limited to some repositories | `agentCallRepo` resolves `{id}` to its repository; `TestHorizontalObjectAccess` checks every `/api/tasks/{id}` route (64 today) against a task in another repository |
| Task lists, inbox, `fs://task` | The repository, by narrowing rather than refusing | `GET /api/tasks`, `/api/fs/task/` (fsScope), `GET /api/inbox` (**F1**) |
| An agent's own conversation (`GET /api/tasks/{id}/messages`) | The acting task | `agentRails` (`ownMessagesReadRoute`); `api:task-admin` reads others |
| An agent's own task (close, regrant, retry) | The acting task, against itself | `agentRails` (`selfMutableTaskRoutes`) |
| Standing sessions and consoles | The run that owns the session | `agentRails`: an agent never creates, restarts, cancels or deletes a session, and never types into a console; `TestAgentRunIsHeldToItsOwnTask` |
| Human-review holds | A run asks only as its own task; only a person releases one | `agentRails` |
| Pauses | Whoever paused | `agentRails` (`run-now`, `pause`, `resume`) |
| External-agent credentials | Itself: a credential may revoke itself (`ownCredentialRoute`), and anything else needs `capabilities:admin` | `TestExternalCredentialRevokesOnlyItself` |
| Notifications, CI runs, artifacts and logs | Deployment-wide for persons; repository for CI reads | Route scope; CI routes resolve their repository from the run |
| Schedules, templates, suites, boards, memory, settings, connections, secrets, hosts, scope sets, allowlist | **Deployment-wide by design** | Route scope only |

People, sessions and consoles are deployment-wide: there is one
deployment, and whoever holds `sessions:write` on it may drive its
standing sessions. Nothing here is scoped in intent but not in code,
apart from F1.

## 3. Escalation through the API: `TestNoEscalationThroughTheAPI`

Every attempt below is refused (401/403). Each refusal is checked
against a control the same caller is allowed, so the refusals cannot be
a broken fixture.

- **Minting a `gma_` credential wider than yourself** (`external_agents.go`):
  - A role holding `capabilities:admin`, and an agent task holding
    `api:capabilities:admin`, tried to mint `secrets:read`,
    `access:write`, `settings:write` and `scopeSet: grain/operator`. All
    were refused. The same role minting `tasks:read` is answered.
  - A `gma_` credential and `grain/viewer` cannot mint at all.
  - Separately, `externalTokenOwnerCap` already caps a token at its
    owner's *current* role (`TestExternalTokenCannotExceedItsOwnersCurrentRole`).
- **Editing scope sets or roles** (`access_roles.go`, `api_scope_sets.go`):
  - Widening one's own role's set, and writing a new set wider than
    oneself, were refused for the `capabilities:admin` role and agent.
  - `grain/viewer` cannot write any set.
  - Deleting a built-in set is refused to everybody (F2 fixes its
    status).
- **Changing one's own allowlist entry:**
  - An `access:write` role cannot `PATCH` itself, or anybody else, to
    `grain/operator`.
  - It cannot add a user or a `group:` entry as `grain/operator`.
  - Re-`POST`ing its own entry with a wider role answers the existing
    entry unchanged (`created: false`), and the test checks the stored
    role.
  - `grain/viewer`, the `capabilities:admin` role and the
    `sessions:write` role cannot change their own entry.
  - #3864 covers admin-on-admin edits.
- **Reading the access code or `LocalAPIToken`:**
  - No route's body holds it for any limited caller (matrix assertion
    3).
  - `GET /api/auth/access-code`, `/api/settings`, `/api/deployment` and
    `/api/config` are also checked explicitly for five narrow callers.
- **Starting a console or terminal with a wider identity:**
  - A `sessions:write` role cannot start a session holding
    `api:secrets:write`, `api:grain/operator` or `api:task-admin`.
  - It cannot set `tasksApproved`, write to a repository it does not
    reach, or set `networkAccess: open` on a deployment whose default is
    `none`.
  - `grain/viewer` cannot start a session or type into a console.

## 4. Nightly

The tests live in `tests/security`, which `nightly.yml`'s `go-long`
job already runs (`-race -shuffle=on -count=2`). The entry points for
the planned `security-attacks` job are:

- `TestRouteAuthorizationMatrix`
- `TestHorizontalObjectAccess`
- `TestAgentRunIsHeldToItsOwnTask`
- `TestExternalCredentialRevokesOnlyItself`
- `TestNoEscalationThroughTheAPI`

None of them needs a live sandbox, a metadata server or a real daemon:
they drive `ui.Server` in process, behind a real ES256 identity proxy.

The matrix makes about 10,600 requests. It takes about 30s on its own,
and about 4 minutes under `-race`: the PR's `go model` job runs it
within its 25-minute budget, and nightly `go-long` runs it twice within
its 80-minute budget.

The registry of scope sets is process-wide. A probe that writes or
deletes a set reloads the registry from its own fixture's store
(`Store.LoadAPIScopeSets`), so the matrix stores its custom sets in its
fixture template as well as installing them. Without that, the custom
roles vanished part-way through a run and were refused at random. This
is a test-isolation hazard only: a daemon has one store.

## 5. Follow-ups

None filed. Both confirmed findings were small and are fixed here.
