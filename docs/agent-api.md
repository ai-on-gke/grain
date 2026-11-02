# The agent API

**Anything a person can do in grain, an agent can do, if it is given the
permission.**

Agents used to get a hand-picked set of MCP tools, and each new ability
was new plumbing: a capability package that re-wrapped a few `ui.Client`
methods as tools. The agent API replaces that with one mechanism. Every
route of grain's own REST API has a permission **scope**. A task granted a
scope is served **one MCP tool per route under it**. Each tool calls the
same route the UI calls, through the same handler, so validation, side
effects and audit are the same.

A new UI action is agent-callable as soon as its route has a scope. There
is nothing else to write.

## How it fits together

| Piece | Where |
|---|---|
| Scope vocabulary and presets | `pkg/model/agent_api.go` |
| Route → scope table | `pkg/ui/route_scopes.go`, beside the route table in `pkg/ui/server.go` |
| Totality test | `pkg/ui/agent_api_internal_test.go` (`TestRouteScopesAreTotal`) |
| Enforcement, rails, attribution | `pkg/ui/agent_api_gate.go` (`agentAPIGate`, wrapping every route) |
| Generated tools | `pkg/ui/agent_api_tools.go` (`AgentAPITools`), served by `grain mcpserver` |

A generated tool sends its request to the daemon with
`X-Grain-Acting-Task: <task id>` (and `X-Grain-Acting-Reason` when it has
one). The daemon's gate then:

1. looks up the acting task's api grants;
2. refuses the call unless they grant the route's scope, on the route's
   repository;
3. refuses a destructive call that has no reason;
4. applies the self-protection rails and the escalation check (below);
5. runs the handler as the principal `{agent, <task id>}`, so everything
   the handler records is attributed to that run;
6. writes a journal line for every call, and, for a change to another
   task, a note in that task's conversation, signed by grain on behalf of
   the run.

A repository off the default git host is `host/owner/name`. In a
`/api/repos/{owner}/{name}/...` path its `{owner}` is `host/owner`
escaped into one segment, as the UI writes it:
`/api/repos/git.forge.example%2Fexperimental/grain/releases`. The
unescaped `/api/repos/<host>/<owner>/<name>/...` is read as that same
route when no other matches (a first segment holding a `.` or a `:` is
a host, as `model.ParseRepo` reads one). The gate reads every
repository a call names -- in its path, a `repo` in its body, or a
`?repo=` -- the way the deployment spells it, so an SSH remote, a URL
or another capitalisation matches the grant naming that repository.
Text that is not a repository is refused with 400.

The header can only narrow a request. A request without it is whatever
the access gate already let in, so anyone able to send the header could
also have left it off.

## Granting scopes

A grant is one capability name per scope or preset:

| Grant | Grants |
|---|---|
| `api:tasks:write` | one scope, on every repository |
| `api:task-admin` | a preset's scopes |
| `api:ci:write@bwsalmon/grain` | a scope or preset on one repository only |
| `api:ops/triage` | a [scope set](#scope-sets)'s resolved scopes |

A template, schedule or task can also write the grant as the `api`
capability with arguments, which is the same thing spelled for a
declaration:

```yaml
capabilities:
  - {capability: api, with: {set: ops/triage}}
  - {capability: api, with: {set: [grain/ci-read, ops/triage], repo: bwsalmon/grain}}
  - {capability: api, with: {scopes: ["ci:read", "tasks:*"]}}
```

Each entry becomes the grant names above (`api:ops/triage`,
`api:grain/ci-read@bwsalmon/grain`, `api:ci:read`, `api:tasks:read`,
...) when it is read, so admission, policy and the proposal gate see
nothing new. A `set` naming no set is refused there and then.

Grant several names for several scopes, or several repositories. A task
whose only grant for a scope is repo-limited reaches just those
repositories. It is refused any route whose repository can't be
determined, such as reordering the whole queue.

The repository of a call is taken from the first of these that applies:

1. the route's `{owner}/{name}`;
2. the repository of the task named in the route's `{id}`;
3. the `repo` or `taskId` in the request body;
4. the `?repo=` query parameter, for a read.

The body and query are only consulted for a route whose path names no
other resource. A route like `DELETE /api/ci/runs/{id}` or
`PATCH /api/schedules/{id}` acts on that resource's repository whatever
the body or query claim, so its repository counts as unknown and only a
grant for every repository reaches it. A body `repo` that differs from
the call's repository (moving a task with `PATCH /api/tasks/{id}`, say)
must be reached by the scope too.

The grants are ordinary capability names. The task, New task, template,
schedule and session capability pickers show them as one "Grain API"
group of their own: the presets first, each with the scopes it expands
to, then the scopes by area, the dangerous ones flagged. Pick a
repository in the group to have a tick write `api:<scope>@owner/name`.
GET /api/config's capability rows carry `preset`, `scopes`, `dangerous`
and `summary` for that group to draw from. The grants are compared by
name in the proposal trust gate, and can be matched by capability policy
rules, the same as any other capability.

## The scope vocabulary

Reads take an `<area>:read` scope. A scope marked **dangerous** can only
be granted by a person, and every call under it needs a `reason`. So does
every `DELETE`.

| Scope | Lets a run | Dangerous |
|---|---|---|
| `tasks:read` | read tasks, conversations, transcripts, trees | |
| `tasks:write` | file, edit, comment on, reorder, defer, pause, resume, retry, stop, close and reopen tasks | |
| `tasks:admin` | approve proposed tasks, recreate sandboxes, edit numeric task `priority` (`PATCH /api/tasks/{id}` with `{"priority": <int>}` only) | yes |
| `tasks:from-template` | file a task from a repository's own declared template (`POST /api/tasks/from-template`); an agent's call also needs `tasks:write`, since it is served as `POST /api/tasks` | |
| `merge:write` | open pull requests for tasks, retry their merges | |
| `merge:force` | force-merge past the gate | yes |
| `ci:read` | read CI runs (job logs included), artifacts, merge batches, base-branch gates | |
| `ci:write` | start, re-run and cancel CI runs, base-branch and release runs | |
| `tests:write` | quarantine flaky or failing tests (`POST /api/tests/quarantine` and `grain quarantine`) and file held follow-up repair tasks | |
| `notifications:write` | raise and clear operator notifications (`POST /api/notifications`, `/{id}/clear`); acknowledging and Investigate are a person's | no |
| `teams:broadcast` | post a comment to every active task (`POST /api/deployment/broadcast`), un-parking the ones awaiting a reply; signed as the broadcasting task, and model-only (never on the sandbox channel) | |
| `dispatch:read` | read whether dispatch is paused | |
| `dispatch:write` | pause and resume dispatch, run a task now | |
| `schedules:read` | read schedules and what they compose | |
| `schedules:write` | create, edit, delete and **Run now** schedules | |
| `templates:read` | read templates | |
| `templates:write` | create, edit and delete templates | |
| `suites:read` | read suites and suite runs | |
| `suites:write` | create, edit and delete suites, start suite runs | |
| `releases:read` | read releases, candidates, qualification | |
| `releases:write` | create releases, cut and promote candidates, approve qualification, request release merges | |
| `repos:read` | read repository settings, branches, declarations | |
| `repos:write` | add and remove repositories, change their settings, create branches | |
| `boards:read` | read boards | |
| `boards:write` | create, edit and delete boards | |
| `memory:read` | read memory documents | |
| `memory:write` | write memory documents | |
| `release-notes:read` | read release notes and weekly cluster stats | |
| `release-notes:write` | write release notes | |
| `sessions:read` | read standing sessions and live consoles | |
| `sessions:write` | edit standing sessions (subject to the rails) | |
| `caches:read` | read the CI, package and actions caches | |
| `caches:write` | flush them | |
| `settings:read` | read deployment settings and configuration | |
| `settings:write` | change deployment settings, the database, the state repository | yes |
| `connections:read` | read git connections and model backends | |
| `connections:write` | change them | yes |
| `capabilities:read` | read capability servers and policy, check grants | |
| `capabilities:admin` | change capability servers, policy, and any task's or repository's grants | yes |
| `secrets:read` | list secrets, keys and tokens by name, never their values | |
| `secrets:write` | set and delete secrets, keys and tokens | yes |
| `deployment:read` | read host, sandbox, log, metric and upgrade status | |
| `sandboxes:read` | read the persistent sandboxes' status (`GET /api/persistent-sandboxes`); `deployment:read` implies it | |
| `deployment:upgrade` | upgrade grain and its database, or cancel a scheduled upgrade | yes |
| `deployment:restart` | restart grain, act on persistent sandboxes, run sandbox probes | yes |
| `deployment:reboot` | reboot or shut down the host | yes |

`internal` marks grain's own plumbing, which no agent can ever reach.
This covers the access-code login, executor bootstrap, and the endpoints
the mcpserver's hand-written tools already call (delivery, wait credit,
local-script approvals, the database tools). These routes are in the
table only so the table stays total.

## Presets

| Preset | Scopes |
|---|---|
| `task-admin` | `tasks:read`, `tasks:write`, `merge:write`, `ci:read`, `ci:write`, `notifications:write` |
| `ci-read` | `ci:read` |
| `dispatch-control` | `tasks:read`, `tasks:write`, `dispatch:read`, `dispatch:write` |
| `deployment-ops` | `deployment:read`, `deployment:upgrade`, `deployment:restart`, `deployment:reboot` |
| `release-manager` | `tasks:read`, `tasks:write`, `merge:write`, `ci:read`, `ci:write`, `schedules:read`, `schedules:write`, `releases:read`, `releases:write`, `repos:read` |
| `self-debug` | `tasks:read`, `deployment:read` |
| `self-upgrade` | `deployment:read`, `deployment:upgrade` |
| `pipeline-control` | `dispatch:read`, `dispatch:write` |
| `team-broadcast` | `teams:broadcast` |
| `memory` | `memory:read`, `memory:write` |
| `release-notes` | `release-notes:read`, `release-notes:write` |
| `test-quarantine` | `tests:write` |
| `release-gate-read` | `ci:read`, `tasks:read`, `sandboxes:read`, `tasks:from-template` limited to the `release-notes` template |

The `self-debug` capability is now an alias. A task holding it is also
served its preset's routes, and keeps the tools it always had under
their old names (`list_grain_tasks`, `read_grain_metrics`, ...).

`team-broadcast`, `dispatch-control`, `pipeline-control`, `memory`,
`release-notes`, `self-upgrade` and `test-quarantine` are aliases
too, but their
in-process capabilities are retired (docs/design/unified-grants.md §4):
they have no tools of their own any longer, and a run holding one is
served its preset's `api_*` tools and `grain` CLI subcommands instead (`api_deployment_broadcast`
for `broadcast_team_message`; the pipeline-pause, run-now and defer
routes for `pause_pipeline`, `resume_pipeline`, `run_task_now` and
`defer_task`; for pipeline-control, the pipeline-pause routes, which an
agent calls as itself, so it lifts only a pause it took and never an
upgrade's hold; for `memory` and `release-notes`, the `/api/memory*` and
`/api/release-notes*` routes and `grain memory` / `grain release-notes`;
for `self-upgrade`, `api_start_upgrade` and `api_cancel_scheduled_upgrade`
plus `GET /api/upgrade` via `grain api`; for `test-quarantine`,
`POST /api/tests/quarantine` (`api_quarantine_test`) and `grain quarantine`). Every place a grant is recorded writes it as
`api:<preset>` (`model.CanonicalLegacyGrant`), importing a grain/config
does the same, and schema migrations 67, 68, 69, 71, 72, 75 and 76 rewrote the grants
already stored -- tasks, templates, schedules, the default capability
lists and the standing sessions. The bare name keeps granting the preset
wherever it is still written, such as a repository's own `.grain`
declarations.

The per-ability capabilities that were queued separately are presets over
this mechanism rather than new tool plumbing:

| Queued task | Capability | Preset |
|---|---|---|
| 399 | dispatch-control | `dispatch-control` |
| 420 | task-admin | `task-admin` |
| 421 | deployment-ops | `deployment-ops` |
| 422 | ci-read | `ci-read` |
| 425 | release-manager | `release-manager` (the standing `system:pipeline-steward` job holds `task-admin`, `dispatch-control` and `ci-read` instead -- see `docs/pipeline-steward.md`) |

### Pausing and resuming one task

`POST /api/tasks/{id}/pause` and `POST /api/tasks/{id}/resume` are under
`tasks:write`, so the `dispatch-control` preset (and `task-admin`) grants
them. The body of a pause is optional: `{"immediate": true}` skips a
running task's grace period. Both answer with the task, which carries a
`paused` object (`at`, `by`, `fromState`, `immediate`, `graceUntil`)
while it is paused. Pausing a closed task is refused (400); pausing a
paused task, or resuming one that is not paused, is a no-op. The
self-protection rails below apply: a run cannot pause its own task.
[The data model](data-model.md#pausing-a-task) says what a pause does
in each state.

## Scope sets

The presets above are grain's **built-in scope sets**. They are also
named `grain/task-admin`, `grain/ci-read`, `grain/dispatch-control`,
`grain/deployment-ops`, `grain/release-manager`, `grain/self-debug`,
`grain/self-upgrade` and `grain/pipeline-control`; the bare names keep
working. The `grain/` prefix is reserved: a deployment can't declare a
set under it, or under a preset's bare name, so a built-in can't be
shadowed or edited. It can be composed.

A deployment declares its own sets in its config repository, in
`tables/api_scope_set.json` (the `api_scope_set` settings table,
`model.APIScopeSet`). It's a settings table like `custom_field`, chosen
over a new declarations directory for the same reasons that one is:

- the sets round-trip through `pkg/staterepo` the way every other
  setting does;
- a merged edit is imported on the next sync (`staterepo.Apply`);
- a UI edit is the same row, exported back;
- with an external config repository, a UI write is refused with the
  patch to commit instead (`docs/external-config-repo.md`).

A set is a row:

| Column | Meaning |
|---|---|
| `name` | lower-case words, `/`-separated (`ops/triage`). Not `grain/...`, not a preset's name. |
| `description` | what the set is for |
| `scopes` | JSON list. Scopes of the vocabulary, or `<area>:*`: every scope this build's vocabulary has in that area (`tasks:*` is `tasks:read`, `tasks:write`, `tasks:admin`; `deployment:*` is all four `deployment:` scopes). A scope grain adds to an area later joins every set naming its wildcard. A bare `*` and `internal` are refused. |
| `includes` | JSON list of other sets, built-in or declared. Their resolved scopes are added, each keeping the repository limits it had. A missing set or a cycle (`a -> b -> a`, or a set including itself) is an error naming the path. |
| `repos` | JSON list of `owner/name`. Limits every scope of the set, included ones too: a scope already limited by an included set keeps only the repositories in both, and one left with none is dropped. Empty means no limit of the set's own. |
| `deny` | JSON list in the `scopes` syntax. Removed after composition. |
| `requires_person` | 1 means only a person may grant the set. **Forced on** when the resolved set holds any dangerous scope (`capabilities:admin`, `secrets:write`, `settings:write`, `connections:write`, `merge:force`, `tasks:admin`, `deployment:*` except `deployment:read`). A set can turn it on, never off. |
| `version` | the author's revision number, shown in the check and in Settings |
| `deprecated` | 1 to flag the set in Settings and the pickers. It still resolves and still grants. |

`declschema.APIScopeSetSchema` pins these columns to the model and the
table, and `model.ResolveAPIScopeSets` is the validation.

The task's own example, `ops/triage`, as a row of
`tables/api_scope_set.json`:

```json
{"name": "ops/triage", "description": "Triage the queue and its CI",
 "scopes": "[]", "includes": "[\"grain/ci-read\",\"grain/task-admin\"]",
 "repos": "[\"bwsalmon/grain\"]", "deny": "[\"tasks:admin\"]",
 "requires_person": 0, "version": 1, "deprecated": 0}
```

It resolves to `ci:read`, `ci:write`, `merge:write`, `tasks:read` and
`tasks:write`, each on `bwsalmon/grain` only, and doesn't require a
person.

**Validation is all or nothing.** The daemon resolves every declared set
together (`model.ResolveAPIScopeSets`) at startup and after every state
sync, and installs the result only if all of them resolve. If any is
wrong, the sets already in effect stay. Settings → Capabilities → API
scope sets shows why, and so does the journal. The store refuses a UI
or API write that would leave any set unresolvable, such as removing a
set another includes.

**Authoring by pull request.** An agent that can read the config
repository proposes a set by opening a pull request against
`tables/api_scope_set.json`. That pull request's `grain state check`
(`statecheck.APIScopeSets`) fails if the file doesn't resolve. Otherwise
it prints, for every declared set:

- the resolved scopes, with their repository limits;
- whether it requires a person;
- the effective scopes added and removed against the base branch's
  version of the file, and every scope whose repositories moved (a
  `repos` limit dropped or changed widens a grant as surely as a new
  scope does).

`api_scope_set` is a state-privileged table (`pkg/trustboundary`).
Merging a set grants it to nobody. But a set is granted by name and
resolved when it's used, so editing a set that's already granted
changes every grant of it. A change to the file is therefore merged by
a person.

**Granting.** Merging grants nothing. A set reaches a task only through
an `api:<name>` grant (or `{capability: api, with: {set: <name>}}`),
which goes through admission and capability policy like any other
grant. A grant `api:<name>@<repo>` naming a repository none of a
repo-limited set's scopes reach would grant nothing, so it is refused
when it is granted. A set that requires a person makes its grant dangerous, so an
agent can never pass it on. The pickers' Grain API group lists declared
sets after the presets, each with its resolved scopes, so the operator
sees what a grant hands out before granting it. `grain mcpserver`, which
can't read the deployment's sets, is started with a declared set's
grant written out as the scope grants it resolves to
(`model.APIScopeSetGrantNames`).

**Routes.** `GET /api/api-scope-sets` (`capabilities:read`) lists every
set in effect with its source, resolved scopes and requires-person flag.
`PUT /api/api-scope-sets` and `POST /api/api-scope-sets/delete`
(`capabilities:admin`) write a declared set. `GET
/api/api-scope-sets/usage` (`capabilities:read`) lists, for each set in
effect, every open task, template, schedule and Settings workflow job
holding `api:<name>` or `api:<name>@<repo>`; Settings shows that list
and asks for confirmation before it removes a set, since every one of
those grants then grants nothing. Because a set is granted by
name and resolved when it is used, writing one is granting what it
resolves to to every task holding it: an agent's `PUT` is held to the
same rules as any other grant (see Policy below) -- every scope the set
resolves to, on every repository it reaches, must be one the acting task
holds, and none may be dangerous. An agent's write is refused while the
declared sets are not the ones in effect (they failed to resolve when
last loaded).

## CI jobs

A workflow job can hold the same grants, in its `capabilities:`:

```yaml
jobs:
  cut-or-promote:
    capabilities: ["api:ci:read", "api:tasks:read", release-gate-read]
```

A job has no agent to serve tools to, so its api grants are worth one
bearer token instead (`pkg/capability/ciapi`). It is minted when the
job's sandbox is acquired, revoked when the sandbox is released, and
placed in `/home/debian/.grain-api/`: `url`, `token`, and `env`, which
exports `GRAIN_API_URL` and `GRAIN_API_TOKEN`. However many api grants
the job declares, it holds one token worth all of them.

```sh
. /home/debian/.grain-api/env
curl -fsS -H "Authorization: Bearer $GRAIN_API_TOKEN" "$GRAIN_API_URL/api/ci/runs?ref=main&limit=100"
```

The token works only against grain's CI API listener, which answers
with grain's own API handler, in process. A request is matched against
the same route table this document describes, and is refused unless the
token holds that route's scope. Every request is narrowed to the job's
own repository:

- A `repo=` naming another repository is refused. A request naming none
  is given the job's.
- A route whose path names another repository
  (`/api/repos/{owner}/{name}/...`) is refused.
- Whatever the answer holds of another repository's is dropped. An
  object whose `repo` is another's answers 404, and list elements whose
  `repo` is another's are removed (`GET /api/tasks`, which takes no
  repository filter, is held to the job's repository this way).
- `POST /api/tasks/from-template` files for the job's repository only,
  from the templates the grant allows, and files the task approved.

A job may hold only the scopes whose answers narrow to one repository:
`ci:read`, `tasks:read`, `tasks:from-template`, `sandboxes:read`,
`templates:read`, `suites:read`, `releases:read`, `repos:read`,
`schedules:read` and `dispatch:read`. Every dangerous scope is refused
outright, and so is every other write and every deployment-wide read
(`settings:*`, `secrets:*`, `connections:*`, `capabilities:*`,
`sessions:*`, `memory:*`, `deployment:*`). A preset holding any of them
(`task-admin`, `release-manager`, ...) is refused whole, as is a scope
set only a person may grant, and a grant limited to another repository
(`api:ci:read@acme/widgets` on a job of `bwsalmon/grain`).

The trust rules are those of `repo-write`. A job is given its api grants
only on a post-merge `release`, an operator's `workflow_dispatch`, or a
cron grain fired itself, never on a pull request's or an ordinary push's
run. They are honoured only for the repositories the deployment offers
grain's API to in CI, which is grain's own (`bwsalmon/grain`). Like
repository access, a job is granted them by declaring them: they are
not taken from the repository's CI capabilities list.

`release-gate-read`, the release train's capability
(`docs/design/release-from-trunk.md`, RFT-3), is the built-in preset
`api:release-gate-read`. A job may declare it under either name.

## GitHub Actions runs

For a repository whose CI is GitHub Actions (`ci_mode: github`, or any
repository whose GitHub CI is not ignored), the `ci:read` routes serve
its Actions workflow runs beside grain CI's. The reads go through the
daemon's own forge connection, so no credential reaches a sandbox: the
GitHub App or token for github.com, and the host's own tool
(`ForgeViaCommand`, `ggh` for `depot.code.corp.goog`) for a GitHub
Enterprise host, whose REST API is under `/api/v3`. The rules for which
repositories a task may read are the same as for grain CI runs.

| Route | Answers |
|---|---|
| `GET /api/ci/runs?repo=owner/name` | Grain CI runs and Actions workflow runs merged, newest first. Each row has `provider` (`grain` or `github-actions`). An Actions row's `actions` object holds `runId`, `runAttempt`, `workflow` (the file), `workflowName`, GitHub's `status` and `conclusion`, `url` and `failedJobs`. `ref`, `conclusion`, `since`, `until` and `limit` filter both kinds of row. `provider=grain` or `provider=github-actions` keeps one kind. |
| `GET /api/ci/runs/{id}` | With an Actions id (`gha:<owner>:<name>:<run id>`, for example `gha:ai-on-gke:grain:123`): the run with its jobs and their steps, each with its conclusion. Logs are not included. |
| `GET /api/ci/runs/{id}/log?job=<name or id>&tail=<lines>` | One Actions job's log. With no `job`, the first job that failed, or the only job. It is capped at 256 KiB (`ci.MaxStepOutput`, the same cap as a grain CI step's output), and the end is kept. The answer gives `job`, `jobId`, `url`, the full size in `bytes`, and `truncated`. |
| `GET /api/ci/runs/{id}/artifacts` | The Actions run's artifacts, names and sizes only. Downloading one (`.../artifacts/{job}/{name}`) answers 501. An Actions artifact is a zip behind a redirect, sometimes gigabytes, and grain does not fetch it for you; get it from the run's page. |

Notes:

- **Who sees Actions rows by default.** A call from an agent gets the
  Actions rows without asking. A person's call needs `provider` to get
  them. The CI pane is a person's call: it polls every few seconds and
  shows grain CI only.
- **Where the rows come from.** A listing reads the forge's
  `actions/runs`, one page of at most 100, and adds in the jobs grain
  has recorded for each run (`github_ci_job`, filled by the merge
  queue's polls and by webhooks).
  - If there is no forge connection, the hour's API budget is low
    (`github.BudgetLow`), or the forge read fails, the listing is built
    from grain's record alone.
  - When that happens, `actionsNote` says why.
- **Rate-limit budget.** Every forge call is charged to the `ci-read`
  caller in the budget ledger (`GET /api/system/github`). Job logs and
  artifact lists have their own kinds, `job logs` and `artifacts`.
- **Caching.**
  - A listing is cached for 30 seconds.
  - A finished run, its jobs, and a finished job's log are cached for
    10 minutes.
  - A run or job that is still going is cached for 15 seconds.
- **Log redirects.** A log comes back as a redirect to a blob host.
  - On github.com, net/http follows it and drops the credential.
  - Over `ggh`, the tool follows it itself. If it hands the redirect
    back, the full URL is sent through the tool again.

In a sandbox the same runs are read with the `grain` CLI and under
`ci://`:

```sh
grain ci list --repo ai-on-gke/grain --ref main --provider github-actions
grain ci show gha:ai-on-gke:grain:123
grain ci log gha:ai-on-gke:grain:123 --job publish --lines 80
grain read ci://ai-on-gke/grain/gha:ai-on-gke:grain:123/publish/job.log --tail 4000
```

`list_ci_runs` takes `provider` as well, and `get_ci_log` reads an
Actions job's log when given a `gha:` run id. `grain ci wait` and
`wait_for_checks` already wait on the GitHub checks that gate a
`ci_mode: github` task's branch.

## Policy

- **Dangerous scopes are a person's to grant.** An agent can't grant one
  to anything:
  - a request body that writes one through any route is refused;
  - a `propose_task` asking for one is filed for a person to approve,
    even when the proposer holds it (`model.EscalatedCapabilities`).

  The `priority-dispatch` capability (`docs/capabilities.md`) is held to
  the same three rules (`model.IsPersonOnlyGrant`), because it lets a
  task jump the dispatch queue.
- **No escalation through the API.** A run can't use any route to grant
  a capability its own task doesn't hold. Any body field named like
  `capabilities`, `defaultCapabilities`, `ciCapabilities` or `grants`
  must list only capabilities the acting task holds. Routes that change
  grants directly are `capabilities:admin`, which is dangerous, and
  holding it doesn't lift this rule: `POST /api/tasks/{id}/capabilities`
  is read by its own request type, so its `id`, `capability` (with its
  `with`), `grant` and `replace` are all held to it as well. Names are
  compared as grants rather than as strings: an api grant by its scopes
  and repository (`api:tasks:read` covers `api:tasks:read@acme/widgets`),
  and a capability reference by whether a held one covers it.
- **No filing around the proposal gate.** Work an agent files or edits
  through any route (a task, schedule or template body) can't carry
  more than `propose_task` would let it: `writes` and `reads` may name
  only repositories the acting task itself pushes to or reads;
  `autoMerge` can't be turned on, and `POST /api/tasks/{id}/submit` is
  refused, because letting a change merge unreviewed is a person's
  decision; `autoApproveProposals` may be turned on only by a task that
  has it; `approved` needs `tasks:admin`; `author` and
  `proposedBy` can't be set; and a `templateId` is refused when the
  template grants, writes or reads beyond the acting task, is auto-merge,
  or files against a repository the scope does not reach. A
  `reviewTemplateId` is held to the same grants, writes and reads
  (the review it files is approved and auto-merge, on the branch the
  agent wrote), except the repository's own default review, which is
  always allowed; `propose_task`'s `review_template_id` falls back to
  that default, with a note on the proposal, instead. Whatever template
  a review comes from, it never carries a dangerous api grant the
  reviewed task did not hold itself.
- **Task priority is 1 or more, everywhere.** `priority` on
  `POST`/`PATCH /api/tasks`, `grain create -priority` and
  `grain update -priority`, `grain task propose --priority`,
  `propose_task`'s `priority` and the Edit form all refuse 0 and below
  with the same message. No value means "the default": leave the field
  out to file at 5 (or at the template's priority), and set 5 to put an
  edited task back on it. A `propose_task` priority above both the
  proposer's own and 5 is filed for a person to approve.
- **Follow-ups do not inherit their parent's priority.** A proposal
  that names no `priority` files at 5, or at its template's priority,
  whatever the proposing task's priority is
  (`pkg/orchestrator/finish.go`). A follow-up from a priority-9 task
  that should run as urgently has to ask for 9 itself, which it may do
  without a person's approval because 9 is not above its parent's.
- **Edits to grants go through the existing policy.** Approval rules,
  `capability_policy.go` and the proposal trust gate apply to api grants
  exactly as they apply to any other capability.

## Self-protection rails

- **Its own run.** A run can't end, orphan or regrant its own task. On
  its own task it may only comment and send messages: no stop, close,
  retry, defer, edit or approval changes.
- **Its own chat.** `POST /api/tasks/{id}/messages` (`api_post_task_message`)
  is the chat tool's write half. It says something to the person watching
  the caller's *own* task and parks that task on their reply. It is
  refused on any other task: called on another task, it would speak as
  that task's run and park it on the operator. Give `summary` with
  `body`: one line of at most 100 characters naming the decision you
  need, which is all the inbox and the task list show of it
  ([question-summaries.md](question-summaries.md)).
- **Standing sessions.** A run can't act on a standing session it doesn't
  own. It can never create, restart, cancel or delete a session, and
  can't type into a live console. The one exception is a note
  (`POST /api/tasks/{id}/notes`, message_task) from the session's own
  parent or child: a director's child may report back to it. A note never
  parks the session or acts on its run. It reaches the session the same
  way whether it is sent through the agent API, `grain task message` or
  `grain mcpserver`'s `message_task`.
- **Upgrade holds.** A run honours them. It can't lift a dispatch pause
  someone else took. It can't upgrade, restart or reboot while an upgrade
  check holds dispatch for another task.

## Tools

Each tool is named after the route's handler, with the `api_` prefix, for
example:

| Tool | Route |
|---|---|
| `api_reorder` | `POST /api/tasks/reorder` |
| `api_start_ci_run` | `POST /api/ci/runs` |
| `api_get_ci_run` | `GET /api/ci/runs/{id}` |
| `api_get_ci_run_log` | `GET /api/ci/runs/{id}/log` |
| `api_run_schedule` | `POST /api/schedules/{id}/run` |
| `api_cut_candidate` | `POST /api/repos/{owner}/{name}/releases/{release}/candidates` |

Its arguments are:

- each path parameter;
- `reason` (required for a destructive call);
- `query`, an object of query parameters, naming and describing the
  ones the route's handler reads (`routeQueries`);
- `body`, for a route that reads one: the JSON request body, described
  field by field from the request type declared in `routeBodies`.

A route that changes one task by its id (`/api/tasks/{id}...`, any method
but GET) also takes `expect`: `{state, title_prefix, kind}`, what the caller
believes about that task. The tool reads the task first -- as `GET /api/tasks/{id}` made by
the acting task, so it needs `tasks:read` on that task's repository like
any other read -- and refuses,
changing nothing, when any field given disagrees with the record, naming
both versions. `kind` is one of the kind tags `port`, `repair`, `session`,
`proposal`, `durable` (`ui.TaskKindTags`), or `none`
(docs/design/durable-tasks.md#DT-13).

Descriptions come from `routeDocs`, or, for a read with none, from the
handler's name. `TestEveryScopedRouteIsDescribed` (in
`pkg/ui/agent_api_internal_test.go`) keeps the three tables total: every
scoped route that changes something has a sentence, and the body type
and query parameters each route declares are the ones its handler's
source reads, so a new UI action is fully described to agents as soon
as its route is scoped. Every generated name is published to the
frameworks' allowed-tool lists (`agent.RegisterPublishedToolNames`).

## Messaging another task's agent

To tell another task's running agent something, use
`api_message_task_agent` (`POST /api/tasks/{id}/agent-messages`, scope
`tasks:write`). It takes a `body` and optional `attachments`.

- **Who it's from.** The message is signed as the calling task. It is
  written with that task's own agent principal and starts "Message from
  task N (title)".
- **Who gets it.** If the target has a live run, that run is handed the
  message as an addendum on its next tool result, the way a person's
  comment is (`GET /api/tasks/{id}/messages?agents=true`). If it has no
  live run, its next run reads the message in the conversation in its
  prompt.
- **What it leaves alone.** It never parks the target, never lifts a
  question the target is parked on, and never queues a dispatch. The
  target's state is the same afterwards.

It is refused on the caller's own task.

Two other routes look like they do this, but don't:

| Route | What it does |
|---|---|
| `POST /api/tasks/{id}/messages` | The caller's own chat with the person watching its own task; parks that task. Refused on any other task. |
| `POST /api/tasks/{id}/comments` | A note on the target's conversation. It lifts any question the target is parked on, as a reply does, and leaves a second, audit note naming the call. |

## External agents

An external agent -- one not running as a grain task in a grain sandbox
(for example, a person's own Claude, Gemini, or Codex CLI session, an
external orchestrator, or an automation bot) -- can drive grain through
the same generated `api_*` MCP tools over Streamable HTTP at `POST /mcp`,
using a per-agent scoped credential (`model.ExternalAgentToken`,
`pkg/ui/external_agents.go`).

### Enabling the endpoint

`POST /mcp` is **off by default**. Enable it in Settings
(`PUT /api/settings` with `{"externalAgentApi": true}`, or the
`external_agent_api` parameter in `grain_config`). When disabled, every
external agent request is refused with `403 Forbidden` even if it
presents a valid token.

### Per-agent credentials

Each external agent is issued its own scoped bearer token (`gma_<hex>`):

- **Name**: a unique identifier (`[a-zA-Z0-9][a-zA-Z0-9._-]*`, up to 64
  characters) used for attribution (`external:<name>`).
- **Grants**: a `scopeSet` (either a built-in preset such as
  `grain/task-admin` or a declarative scope set from
  `tables/api_scope_set.json`) and/or an explicit `scopes` list
  (`<area>:<level>` or `<area>:*`).
- **Repository restriction**: an optional `repos` list (`owner/name`).
  When set, every scope granted to the credential is restricted to those
  repositories; when omitted, scopes apply across all repositories. A
  single scope can be held to one repository on its own instead, as
  `<area>:<level>@owner/name` in `scopes` (`tasks:write@acme/widgets`).
  Each level is its own scope: a credential holding only `tasks:write`
  can file and edit tasks but is offered no `tasks:read` tool to list or
  read them, and mutating task routes answer with `{id, state}` (plus any
  fields that `PATCH /api/tasks/{id}` itself just replaced; empty `PATCH`
  bodies are refused) rather than echoing the full task record unless the
  caller also holds `tasks:read` on that repository. A plain `POST
  /api/tasks` still answers with the whole new task, since it holds only
  what the caller sent; one naming a `templateId` or `supersedes` does not.
- **Expiry and limits**: an optional `expiresAt` timestamp, a per-credential
  request rate limit (`rateLimit`, requests per minute, default `60`), and
  an in-flight concurrency cap (`maxConcurrent`, default `8`).

Only the SHA-256 hash of the token is stored (`external_agent_token`
table). The raw `gma_...` secret is returned once when the credential is
created or rotated and is never shown again.

Issuing or rotating a credential that resolves to any **dangerous scope**
or a declarative scope set with `requires_person: true` requires a human
principal (`PrincipalHuman`), matching the admission policy for task
grants.

Credentials are managed through:

| Route | Scope | Description |
|---|---|---|
| `GET /api/external-agents/tokens` | `capabilities:read` | List external agent credentials (metadata only; never returns secret tokens). |
| `POST /api/external-agents/tokens` | `capabilities:admin` | Issue a new external agent credential and return its raw `gma_...` token once. |
| `POST /api/external-agents/tokens/{id}/rotate` | `capabilities:admin` | Replace a credential's secret token immediately and return the new token once. |
| `POST /api/external-agents/tokens/{id}/revoke` | `capabilities:admin` | Revoke a credential immediately (`204 No Content`). |
| `DELETE /api/external-agents/tokens/{id}` | `capabilities:admin` | Delete a credential record (`204 No Content`). |
| `POST /api/external-agents/tokens/revoke-all` | `capabilities:admin` | Emergency kill switch: revoke every active external agent credential immediately. |

A credential's view (listed, created or rotated) carries
`resolvedScopes`: every scope it holds after expanding scope sets and
implied scopes. A scope held on named repositories only is listed once
per repository as `scope@owner/name` (a credential issued with
`"scopes": ["tasks:write@acme/widgets"]` lists `tasks:write@acme/widgets`);
a bare scope is held on every repository.

### Attribution

Every call made with an external agent credential executes as
`Principal{Kind: PrincipalAgent, ID: "external:<name>"}` and passes
through `agentAPIGate`:

- **Daemon journal**: every MCP request and every underlying API route
  call is logged with `{agent, external:<name>}`, its route pattern,
  scope, repository, reason, and HTTP status.
- **Task conversations**: any non-GET call that modifies a task (including
  `/api/tasks/{id}/*`, `POST /api/tasks/reorder`, and `POST /api/ci/runs`
  with `taskId`) records an audit comment on the affected task's
  conversation signed by `grain` on behalf of
  `{agent, external:<name>}`.

### Routes without calling-task context

Because an external agent is not a running grain task and has no sandbox
or console session of its own, routes and rails that assume "the calling
task" behave as follows:

| Route / Feature | External agent handling |
|---|---|
| `POST /api/tasks/{id}/messages` (`api_post_task_message`) | Excluded from `tools/list` and refused by `agentAPIGate`. It speaks as the calling task's own run to its operator and parks that task; external agents use `POST /api/tasks/{id}/comments` (`api_add_comment`) or `POST /api/tasks/{id}/agent-messages` (`api_message_task_agent`) instead. |
| `PUT /api/tasks/{id}/session-title` (`api_set_task_session_title`) | Excluded from `tools/list` and refused by `agentAPIGate` (renames the calling run's own standing chat session). |
| `/api/console/*` and `/api/console-sessions*` | Excluded from `tools/list` and refused by `agentAPIGate` (bound to in-grain sandbox consoles and session tasks). |
| `POST /api/tasks` (`api_create_task`) | Requires an explicit target in the request body (`repo`, a `templateId` with a target, or `noRepo: true`) because there is no calling task from which to inherit a default repository. |
| Self-protection on `/api/tasks/{id}/*` | No task ID matches `external:<name>`, so an external agent with the required scope and repository grant can stop, close, retry, pause, or reorder any task in its allowed repositories while still honouring standing-session ownership, pause ownership, and upgrade holds. |
| Single-repo collection reads (`GET /api/tasks`, `GET /api/ci/runs`, `GET /api/ci/workflows`, `GET /api/merge-batches`, `GET /api/pipeline/changes`, `GET /api/memory/documents/{name}`) | When a credential is restricted to a single repository and omits `?repo=`, the query filter defaults automatically to that repository, and the answer holds nothing outside it. A credential restricted to several repositories names one in `?repo=` on each read. |
| External configuration repository (`409 config_managed_externally`) | Works unchanged: when the deployment's configuration lives in an external git repository, configuration-writing tools return `409 Conflict` (`code: "config_managed_externally"`) with the rehearsed git patch to commit. |

### Worked example

1. **Enable the external agent API and issue a credential** (as an
   operator):

   ```sh
   export GRAIN_URL="https://grain.example.com"

   # Enable POST /mcp in Settings:
   curl -fsS -X PUT "$GRAIN_URL/api/settings" \
     -H "Authorization: Bearer $GRAIN_ACCESS_CODE" \
     -H "Content-Type: application/json" \
     -d '{"externalAgentApi": true}'

   # Issue credential "ops-laptop" with preset grain/task-admin on ai-on-gke/grain:
   curl -fsS -X POST "$GRAIN_URL/api/external-agents/tokens" \
     -H "Authorization: Bearer $GRAIN_ACCESS_CODE" \
     -H "Content-Type: application/json" \
     -d '{
       "name": "ops-laptop",
       "scopeSet": "grain/task-admin",
       "repos": ["ai-on-gke/grain"],
       "rateLimit": 60,
       "maxConcurrent": 8
     }'
   ```

   Save the returned `"token": "gma_..."` value as `GRAIN_MCP_TOKEN`.

2. **Configure an external MCP client** (such as Claude Code, Gemini CLI,
   or Codex) to connect over Streamable HTTP:

   ```json
   {
     "mcpServers": {
       "grain": {
         "type": "http",
         "url": "https://grain.example.com/mcp",
         "headers": {
           "Authorization": "Bearer gma_..."
         }
       }
     }
   }
   ```

3. **List tools, move a task to the front, and re-run CI on a PR**:

   ```sh
   # List the tools granted to ops-laptop:
   curl -fsS -X POST "$GRAIN_URL/mcp" \
     -H "Authorization: Bearer $GRAIN_MCP_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'

   # Move task 42 ahead of task 10 at the front of the queue:
   curl -fsS -X POST "$GRAIN_URL/mcp" \
     -H "Authorization: Bearer $GRAIN_MCP_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "jsonrpc": "2.0",
       "id": 2,
       "method": "tools/call",
       "params": {
         "name": "api_reorder",
         "arguments": {
           "reason": "prioritize release blocker",
           "body": {"ids": ["42"], "beforeId": "10"}
         }
       }
     }'

   # Re-run CI on task 42's pull request:
   curl -fsS -X POST "$GRAIN_URL/mcp" \
     -H "Authorization: Bearer $GRAIN_MCP_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{
       "jsonrpc": "2.0",
       "id": 3,
       "method": "tools/call",
       "params": {
         "name": "api_start_ci_run",
         "arguments": {
           "reason": "re-run CI on PR",
           "body": {"taskId": "42"}
         }
       }
     }'
   ```

   Both calls are logged in the daemon's log (the journal) as
   `grain api: {agent, external:ops-laptop} <route> scope=... repo=...
   reason=... -> <status>`, and each one that succeeds is recorded on
   task 42's conversation as a comment by `grain` on behalf of
   `external:ops-laptop`. Both tasks must be in a repository the
   credential reaches (`ai-on-gke/grain` here): a task id that names no
   task is refused as a call that "does not name a repository it
   reaches". Moving a standing console session (a director chat) is
   refused, since an external agent owns none, and `api_start_ci_run`
   with a `taskId` runs only for a task that holds the `grain-ci`
   capability -- one filed on a repository whose CI Mode is "Use Grain
   CI" (Settings -> Repos) -- answering `task 42 does not hold the
   "grain-ci" capability` otherwise.

   The plain `curl` calls above work without an `initialize` handshake:
   the endpoint keeps no session, so every request stands on its own.
   An MCP client (the `mcpServers` entry in step 2, or
   `npx @modelcontextprotocol/inspector --cli "$GRAIN_URL/mcp"
   --transport http --header "Authorization: Bearer $GRAIN_MCP_TOKEN"
   --method tools/list`) sends `initialize` first, which is answered as
   well.

### Threat model and safety controls

- **Off by default**: `POST /mcp` and `gma_` bearer authentication are
  refused until an operator explicitly enables `externalAgentApi` in
  Settings.
- **Hashed at rest, shown once**: raw `gma_` tokens are 128-bit random
  secrets hashed with SHA-256 before storage; a database read or
  `GET /api/external-agents/tokens` response never reveals a usable token.
- **Least privilege by scope and repository**: `tools/list` exposes only
  the tools whose route scope the credential holds, and `agentAPIGate`
  enforces both the route scope and the repository restriction on every
  invocation.
- **No privilege escalation or unreviewed merge**: external agents are
  subject to the same `agentGrantEscalation` and `externalAgentFilingEscalation`
  checks as in-grain task agents -- they cannot mint or grant capabilities
  they do not hold, cannot issue or rotate dangerous-scope credentials,
  and cannot enable `autoMerge` or submit a PR to merge without human
  review.
- **Rate limits, concurrency caps, and body limits**: each credential has
  an independent token-bucket rate limiter (`rateLimit` req/min) and
  in-flight concurrency cap (`maxConcurrent`), returning
  `429 Too Many Requests` when exceeded -- with `Retry-After` set to when
  the bucket next holds a request (20 seconds at `rateLimit: 3`), or 1
  second for a full concurrency cap -- and `/mcp` bounds request bodies
  to `4 MiB` (`413 Request Entity Too Large`).
- **Immediate revocation and kill switch**: revoking or rotating a
  credential takes effect on the next request without restarting the
  daemon, and `POST /api/external-agents/tokens/revoke-all` revokes every
  active external credential at once.

## Example: auditing CI runs

`GET /api/ci/runs` (`api_list_ci_runs`) carries every job's steps and the
whole of every shared fault with each page, which runs to about 16 KB a
run -- more than a tool result holds once a page has more than a few runs
on it. For an audit, ask for `view=compact` instead. Each run is then only
its id, repo, ref, commit, base commit, event, requester,
start and finish times and conclusion. Each job that did not succeed adds
one entry: its name and conclusion, the step that failed, the first
failing test where the output named one, and the first line of its error.
Steps and fault run lists are left out. A shared fault is listed once,
under `faults`, and a job that is part of one names it by `digest`
(`GET /api/ci/faults` has the rest of it).

The filters work in either view. `ref` matches any part of the branch name,
so `grain-merge-queue/` finds the merge-queue runs. `conclusion` takes
several values. `since` and `until` each take a span back from now (`7d`)
or a date, so together they bound a window that is already over:

| Step | Tool | Arguments |
|---|---|---|
| One week of red merge-queue runs | `api_list_ci_runs` | `query: {view: "compact", ref: "grain-merge-queue/", conclusion: "failure,errored", since: "14d", until: "7d", limit: "50"}` |
| One run in full | `api_get_ci_run` | `id` of the run |

The same listing from a terminal is
`grain ci list -compact -ref grain-merge-queue/ -since 14d -until 7d`.
Add `-json` to get the route's own body.

## Example: the release-candidate cut

A director session granted the following can do the whole cut through
generated tools:

```
api:tasks:write  api:merge:write  api:ci:read  api:ci:write
api:schedules:write  api:releases:write
```

Or grant `api:release-manager`, which also adds the reads.

| Step | Tool | Arguments |
|---|---|---|
| Move a task to the front of the queue | `api_reorder` | `body: {ids: ["441"], beforeId: "<first queued id>"}` |
| Re-run CI on a PR | `api_start_ci_run` | `body: {taskId: "441", recheck: true, recheckFailed: true}` |
| Read a failing job's log | `api_list_ci_runs` | `query: {ref: "grain/…/task-441"}` |
| | `api_get_ci_run` | `id` of the run; job steps include their output |
| Run a schedule now | `api_run_schedule` | `id` of the schedule |
| Cut a release candidate | `api_cut_candidate` | `owner`, `name`, `release` |
| Promote it | `api_promote_candidate` | `owner`, `name`, `release` |

Each call appears in the daemon's journal as
`grain api: {agent, <session task>} POST /api/... scope=... -> 201`. A
call that changed another task is also noted in that task's
conversation.
