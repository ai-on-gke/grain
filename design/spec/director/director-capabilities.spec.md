# Director capabilities and session envelope

**Status:** draft for review. Normative. Ground truth (§1.4) and the gap
table (§5) checked against `main` on 2026-11-06; remaining implementation
gaps are filed as follow-up tasks from [Task #3274](/tasks/3274).

**Specifies:** [`director.design.md`](../../design/director.design.md);
the capability model it builds on is
[`capabilities.design.md`](../../design/capabilities.design.md).

This specification turns the workflows (`W1`–`W5`) and trust boundaries of
[`director.design.md`](../../design/director.design.md) into the exact
capabilities and session envelope the director
(`model.DirectorSessionID = "director"`) ships with. It also lists the
capabilities that stay opt-in operator grants and the actions that are
permanently out of reach. Where this specification and the design
disagree, this document wins. Where either disagrees with the code on
`main`, §5 records the gap.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT" and "MAY" are
to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and
only when, they appear in all capitals.

## Contents

1. Terms and ground truth
2. Workflow-to-capability traceability matrix
3. Normative classification (Tier 1 / Tier 2 / Tier 3)
4. Proposal and grant inheritance for a `no_repo` director
5. Gap analysis and implementation checklist

## 1. Terms and ground truth {#DC-1}

1.1. **Grant strings.** Grants are written in two spellings:

- The **current** spelling, which `Task.Grants`,
  `ConsoleSession.Capabilities` and `settings/capabilities.yml` store
  today. That is a bare capability id (`github-sandbox`), `api:<scope>`
  (`api:ci:read`), `api:<preset>` (`api:task-admin`), or either api form
  narrowed with `@owner/name`.
- The **canonical** unified-grant spelling of
  [`design/design/unified-grants.design.md`](../../design/unified-grants.design.md)
  §3–§4: `https://grain.dev/capabilities/api#<scope-or-preset>`,
  `…/grain#<method>`, `…/repo#read|write`, `…/config-map#config-map`, and so on.
  Below, `…/` abbreviates `https://grain.dev/capabilities/`. Canonical
  references for in-process capabilities that §4 of that design has not
  yet converted are marked *(proposed)*, as that design marks them.

1.2. **Scopes and presets.** Every `api:` scope named here is in
`model.apiScopes` (`pkg/model/agent_api.go`, documented in
`docs/agent-api.md`), and every preset is in `model.apiPresets`. A scope
or route that does not exist on `main` is marked **(proposed new
scope)** or **(proposed)**. The scopes marked `Dangerous` on `main` are
`tasks:admin`, `merge:force`, `settings:write`, `connections:write`,
`capabilities:admin`, `secrets:write`, `deployment:upgrade`,
`deployment:restart` and `deployment:reboot`.

1.3. **Channels.** An api grant reaches grain over two channels:

- **MCP** (`api_*` tools served by the controller, model-side).
- **sandbox-cli** (the `grain` CLI over the git proxy credential,
  `X-Grain-Channel: sandbox-cli`).

`model.IsSandboxDeniedAPIScope` refuses every dangerous scope on
sandbox-cli. It also refuses `dispatch:write`, `repos:write`,
`sessions:write`, `teams:broadcast` and every `deployment:*` other than
`deployment:read`. Those scopes are therefore **MCP-only**: a command the
agent runs in its sandbox, or text in a repository it reads, cannot
spend them. Every call under a dangerous scope, and every `DELETE`,
MUST carry `X-Grain-Acting-Reason` (`ActingReasonHeader`) and is
refused without it. The reason is written to the controller log
(`grain api: {agent, …} … reason=…`).

1.4. **What `main` seeds today.** These are the facts §5 is diffed against.

- `model.DefaultConsoleSessions()` returns one session:
  - `ID: "director"`, `Title: "Director"`, `Team: DefaultTeamID`.
  - `Capabilities`: `api:self-debug` and `…/grain#source={}` (formerly
    `self-debug`), `…/grain#journal={}` (formerly `grain-log`),
    `api:deployment:read` and `…/grain#health={}` (together formerly
    `deployment-health`), `…/config-map#config-map={"config_maps":["bootstrap"]}`
    (formerly `bootstrap-playbooks`), `api:task-admin`,
    `api:capabilities:read`, `api:capabilities:detach`, `api:settings:read`,
    `api:repos:read`, `api:releases:read`, `api:schedules:read`,
    `api:templates:read`, `api:suites:read`, and `api:dispatch:read`,
    plus every build `DefaultOn` registration
    (`RegisterDefaultConsoleSessionCapability`, which `capability.Register`
    calls for a `DefaultOn` method, e.g. `…/corp#research={}` on the
    wrapper distribution's build, formerly `corp-research`).
  - `NetworkAccess: NetworkNone` (G3, done). `model.ConsoleSessionNetworkAccess`
    resolves it to `NetworkOpen` when the session's task is filed or
    synced on a backend that cannot isolate a sandbox's network
    (`HostSandboxes`, `ContainerSandboxes`, plain `K8sSandboxes`), which
    would otherwise refuse every exchange. A build that registers a
    `DefaultOn` capability with `SandboxEgress` set ships it
    `NetworkOpen` (§3.1.6).
  - `TasksApproved: true` (G2, done); a deployment that saved its own
    `ConsoleSessions` row keeps what it saved (`ConsoleSessionsOr`), and
    `ConsoleSessionTasksApprovedTag` / `ConsoleSessionTasksUnapprovedTag`
    preserve an operator's override on the active task. `Reads` is empty
    and `Writes`
    is `[{Repo: grain/config}]` (`model.ConfigRepo`), which offers
    `propose_config_change` (G5); `ReadsAll` is unset (`nil`, meaning `true`: `model.ConsoleSessionAutoReads`
    adds every active working repository in `repo_config` and `target_repos`).
- `model.ConsoleSessionDefaultGrants` adds `api:ci-read` to every console
  session unless `DeclinedDefaults` names it.
- `model.FileConsoleSessionTask` files every console session task with
  `Admission: AdmissionDurable` (the task setting that replaced the
  `durable` and `priority-dispatch` grants, unified-grants.design.md
  §4.1), which includes `bypass`. `model.ConsoleSessionGrants` drops the
  retired names from a session's stored `Capabilities`.
- `orchestrator.fileConsoleSession` passes the state repository
  (`Config.settingsRepo()`) as `StateRepo`, so the director task's
  `Reads` always holds `grain/config` (or whatever repository the
  deployment uses). That read does not come from a grant.
- `prepareDirectorStateWithReuse` clones that repository to
  `/home/debian/grain-state` (`pkg/orchestrator/checkout.go`). Its push
  URL is set to `grain-read-only` unless the session holds the state
  repository in `Writes`, in which case the push URL points at the
  per-attempt git proxy and `AuthorizeRefUpdates` admits fast-forward
  pushes only to `refs/heads/grain/config/<task-id>/*`
  (`model.ConfigProposalBranchPrefix`), never `main`.
  `DefaultConsoleSessions()` ships `Writes: [grain/config]`, so the
  shipped director's checkout pushes candidate branches; a deployment
  that saved its own session list keeps whatever `Writes` it saved.
- `self-debug` is retired (schema migration 115): the director holds
  what it became, the `api:self-debug` preset (`tasks:read`,
  `deployment:read`) and `…/grain#source={}`. The bare name was an
  alias for one release and is no longer read as either.
- `CatalogueKindDirector` carries a **built-in Tier 3 floor**
  (`pkg/model/director_floor.go`, G7). `model.DirectorFloorRefusal`
  refuses any grant reaching `merge:force`, `settings:write`,
  `secrets:write`, `connections:write` or `repos:write`
  (`model.DirectorForbiddenAPIScopes`) to a run of that kind, in every
  spelling `model.GrantAPIScopes` reads: `api:<scope>`, `api:<preset>`
  or a scope set expanding to one (`api:grain/operator`), either narrowed
  with `@owner/name`, a registered alias, and the canonical
  `…/api#<scope-or-preset>` reference. `CapabilityCatalogue.ValidateGrants`
  and `Refusals` apply it before the operator's
  `settings/capabilities.yml` rules and on a nil catalogue too, so an
  operator file may narrow the director but never widen it past the
  floor. It is enforced:
  - at dispatch, by `orchestrator.catalogueHold`, which on a deployment
    with no catalogue judges the director alone against the floor alone;
  - on a grant attached to the director's task
    (`ui.catalogueEditRefusal`, `ui.CheckAccessRequest`), whether or not
    the deployment has a catalogue;
  - on saving the director's session row
    (`model.ValidateConsoleSessions`), and in a `grain/config` candidate's
    liveness report (`staterepo.CheckDispatchLiveness`);
  - at the agent API gate and MCP endpoint, where
    `model.WithinDirectorFloor` strips those scopes from the director's
    grant set, so a scope set redefined after admission still reaches
    none of their routes.
- `propose_config_change` is implemented (`pkg/mcp/config_change_tool.go`,
  `pkg/ui/task_config_change.go`, `pkg/model/config_change.go`): offered
  on repo-less runs holding the state repository in `Writes`, it validates
  the candidate `grain/config/<task-id>/*` branch with
  `staterepo.ValidateCandidateConfig`, classifies the diff with
  `trustboundary.ClassifyState`, pins `headSha` on the in-chat approval
  card, and lands only on a person's approval. `main` keys the tool on
  `Writes: [<state repo>]`, which `UngrantedWrites` would also admit as
  a write a child `propose_task` call may inherit; so
  `model.ConfigRepoProposalHold` (asked first by `AutoApprovalRefusal`,
  and as a grain hold by both `ui.Client.CreateTask` and
  `orchestrator.relayProposedTasks`) files any child whose `Target` or
  `Writes` names `IsConfigRepo` for a person, whatever the parent holds
  (G5, done).

## 2. Workflow-to-capability traceability matrix {#DC-2}

Each row names a surface the director uses, the grant that reaches it
on `main`, its canonical reference, and the tier (§3) the grant belongs
to. *Implicit* means the grant comes with the task rather than from
`Capabilities`.

### 2.1 W1 — Bootstrap and onboarding; W2 — Observability and diagnosis

| Surface (tool / CLI / route) | Current grant | Canonical reference | Tier |
|---|---|---|---|
| Source checkout, metrics tools, `grain task list\|read\|prompt\|transcript`, `task://`, `src://` | `api:self-debug` (`tasks:read` + `deployment:read`) + `…/grain#source` (formerly `self-debug`, retired in schema migration 115) | `…/api#self-debug` + `…/grain#source` | 1 |
| Daemon journal (formerly `grain-log`) | `…/grain#journal` | `…/grain#journal` | 1 |
| Host / sandbox probes; `GET /api/deployment/*` reads | `api:deployment:read` + `…/grain#health` (formerly `deployment-health`) | `…/api#deployment:read` + `…/grain#health` | 1 |
| Bring-up runbooks | `…/config-map#config-map={"config_maps":["bootstrap"]}` (formerly `bootstrap-playbooks`) | `…/config-map#config-map` `{"config_maps":["bootstrap"]}` | 1 |
| `request_secret` (write-only intake into `<data-dir>/secrets`) | none (a turn-ending tool that every task holds) | — | 1 |
| `GET /api/tasks`, `GET /api/tasks/{id}`, `GET /api/states` | `api:tasks:read` (via `api:self-debug`) | `…/api#tasks:read` | 1 |
| `GET /api/ci/runs`, `/api/ci/runs/{id}`, `/api/ci/faults`, artifacts; `grain ci list\|log\|test-history`; `ci://` | `api:ci:read` (or preset `api:ci-read`) | `…/api#ci:read` | 1 |
| `grain merge-queue`, base-branch gates | `api:ci:read` | `…/api#ci:read` | 1 |
| Repository settings, branches, declarations | `api:repos:read` | `…/api#repos:read` | 1 |
| Schedules, templates, suites, releases (read) | `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:releases:read` | `…/api#<scope>` | 1 |
| Dispatch pause state | `api:dispatch:read` | `…/api#dispatch:read` | 1 |
| `GET /api/config`, `GET /api/state-repo*` | `api:settings:read` | `…/api#settings:read` | 1 |
| `/home/debian/grain-state` (`tables/`, `settings/`, `memory/`) | implicit `Reads: [<state repo>]` | `…/repo#read` `{repo: grain/config}` | 1 (implicit) |
| Edition defaults (`…/corp#research` on wrapper distribution builds, formerly `corp-research`) | build `DefaultOn` registration (`capability.Register`) | `…/corp#research` | 1 (build) |

### 2.2 W3 — Backlog and lifecycle management

| Surface | Current grant | Canonical reference | Tier |
|---|---|---|---|
| `propose_task` (with `repo`, `depends_on`, `supersedes`, `hold_for_approval`) | none (every task holds it) | — | 1 |
| `PATCH /api/tasks/{id}` (priority, body, deps), `POST /api/tasks/{id}/pause\|resume\|stop\|retry\|close\|resume-later`, `PUT\|DELETE /api/tasks/{id}/goal`, `POST /api/tasks/{id}/comments` | `api:tasks:write` | `…/api#tasks:write` | 1 |
| `POST /api/tasks/{id}/agent-messages` (`api_message_task_agent`, guidance to a running agent) | `api:tasks:write` | `…/api#tasks:write` | 1 |
| Operator notifications | `api:notifications:write` | `…/api#notifications:write` | 1 |
| `POST /api/tasks/{id}/approve`, `POST /api/tasks/{id}/sandbox/recreate` | `api:tasks:admin` (**dangerous**) | `…/api#tasks:admin` | 2 |
| `PATCH /api/tasks/{id}` editing priority only, without `tasks:write` | `api:tasks:admin` (`tasksAdminTaskPatchBodyAllowed`) | `…/api#tasks:admin` | n/a (Tier 1 already holds `tasks:write`) |
| `DELETE /api/pause`, pause dispatch, `POST /api/tasks/{id}/run-now` | `api:dispatch:write` (MCP-only), preset `api:dispatch-control` | `…/api#dispatch-control` | 2 |

Note: the task that filed this spec placed reordering, pausing, stopping,
retrying, goal editing and closing under `api:tasks:admin`. On `main`
all of them are `tasks:write`. `tasks:admin` is approval and sandbox
recreation only.

### 2.3 W4 — Configuration and capability hygiene

| Surface | Current grant | Canonical reference | Tier |
|---|---|---|---|
| `GET /api/capability-servers`, `/api/capability-methods`, `POST /api/capability-grants/check`, `GET /api/repos/{o}/{n}/capabilities\|ci-capabilities` | `api:capabilities:read` | `…/api#capabilities:read` | 1 |
| Detach a grant: `POST /api/tasks/{id}/capabilities` `{attach: false}`; narrow `PUT /api/repos/{o}/{n}/capabilities` | `api:capabilities:admin` (**dangerous**) | `…/api#capabilities:admin` | 2 |
| Detach only (no attach, no replace, never on a standing session) | `api:capabilities:detach`, not dangerous, sandbox-denied, reason required | `…/api#capabilities:detach` | 1 (§3.1.2) |
| Read deployment config | implicit state-repo read; `api:settings:read` | `…/repo#read` `{repo: grain/config}` | 1 |
| `propose_config_change` (branch, validate, pin `headSha`, in-chat approval card) | `Writes: [<state repo>]` on a repo-less session (`grain/config/<task-id>/*` branches only; shipped on the director) | `…/repo#write` `{repo: grain/config, refs: [refs/heads/grain/config/<task-id>/*]}` *(proposed)* | 1 (§3.1.3) |
| `grain/config` PR via a filed task | `propose_task` with `writes: [grain/config]` (held for a person by `UngrantedWrites`) | `…/repo#write` | 1 (the hold is the gate) |
| `POST /api/state-repo/sync\|pull-requests\|remote`, `PUT /api/settings` | `api:settings:write` (**dangerous**) | `…/api#settings:write` | 3 for the director (§3.3) |

### 2.4 W5 — CI, merge queue, release and upgrade operations

| Surface | Current grant | Canonical reference | Tier |
|---|---|---|---|
| `POST /api/ci/runs` (re-run), `DELETE /api/ci/runs/{id}` (cancel a wedged run) | `api:ci:write` | `…/api#ci:write` | 1 |
| `POST /api/tasks/{id}/retry-merge` (`grain retry-merge`), `POST /api/tasks/{id}/pull-request` | `api:merge:write` | `…/api#merge:write` | 1 |
| Quarantine a flaky test | `api:tests:write` (preset `api:test-quarantine`) | `…/api#tests:write` | 2 |
| Cut / qualify / promote candidates, run schedules | `api:release-manager` | `…/api#release-manager` | 2 |
| Start or cancel a controller upgrade | `api:self-upgrade` (alias `self-upgrade`; `deployment:upgrade` is **dangerous**) | `…/api#self-upgrade` | 2 |
| Restart / reboot | `api:deployment-ops` (**dangerous**) | `…/api#deployment-ops` | 2 |
| `POST /api/tasks/{id}/submit`, `autoMerge: true` in any body, auto-merge templates | refused for every agent (`errAgentAutoMerge`) | — | 3 |
| `POST /api/tasks/{id}/force-merge` | `api:merge:force` (**dangerous**) | `…/api#merge:force` | 3 for the director |

The preset `api:task-admin` is exactly `tasks:read`, `tasks:write`,
`merge:write`, `ci:read`, `ci:write` and `notifications:write`. None of
those is dangerous or sandbox-denied, and together they are the W3/W5
day-to-day set above. Tier 1 grants it as one preset rather than six
scopes.

## 3. Normative classification

### 3.1 Tier 1 — Shipped defaults and session envelope {#DC-3}

`model.DefaultConsoleSessions()` MUST return the director with the
envelope below. A deployment that has saved its own console-session
list keeps it (`ConsoleSessionsOr`). Tier 1 changes the shipped
default, and §5 says how existing deployments are offered it.

| Field | Value | Rationale |
|---|---|---|
| `Capabilities` | `api:self-debug`, `…/grain#source`, `…/grain#journal`, `api:deployment:read`, `…/grain#health`, `…/config-map#config-map={"config_maps":["bootstrap"]}`, `api:task-admin`, `api:capabilities:read`, `api:capabilities:detach`, `api:settings:read`, `api:repos:read`, `api:releases:read`, `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:dispatch:read`, plus build `DefaultOn` ids | W1–W5 day-to-day. None of these is dangerous, so none needs a person's grant under `IsPersonOnlyGrant`. |
| `Admission` | `durable`, always, via `FileConsoleSessionTask` | Priority admission (§3.1.4). |
| `Reads` | `[]` in the row; the state repository is added at filing (`FileConsoleSessionOptions.StateRepo`) | Config is always readable without the operator naming a repo (§3.1.3). |
| `Writes` | `[grain/config]` | The director has no target and lands no code. The one write is the state repository, for `propose_config_change` (§3.1.3); it is never passed on to a proposal. |
| `TasksApproved` | `true` | §3.1.5. |
| `NetworkAccess` | `none`, resolved by `ConsoleSessionNetworkAccess` (falls back to `open` on a backend that cannot isolate) | §3.1.6. |

#### 3.1.1 Why each api grant is safe in Tier 1

- **Read scopes** expose deployment state the operator already sees
  through the director. `secrets:read` is excluded even though it lists
  only names (§3.2). `settings:read` exposes configuration, in which
  secrets appear by `$secret:` reference rather than by value.
- **`tasks:write`** can stop, close or retry another task, and can
  message its agent. It cannot act on the director's own task
  (`selfMutableTaskRoutes` admits only comments and messages), cannot
  approve anything (approval is `tasks:admin`), and cannot raise grants
  (`agentGrantEscalation`, `agentFilingEscalation`). Every `DELETE`
  needs a reason; this spec RECOMMENDS the session body ask for one on
  stop and close too.
  Stop/close with an `expect` state precondition is RECOMMENDED in the
  session body (`DirectorSessionBody`).
- **`ci:write`** re-runs and cancels CI. It cannot change what CI
  gates on, and a cancelled required check blocks a merge rather than
  admitting one.
- **`merge:write`** opens pull requests and retries merges through the
  merge queue and its gates. It cannot skip them: `merge:force` is
  dangerous, and `submit` / `autoMerge` are refused outright.

Dangerous scopes MUST NOT be in Tier 1. The "MCP-only channel +
`--reason` audit + human PR gate" combination is what makes a
**person's** grant of a dangerous scope reviewable after the fact. It
does not replace the person, because a dangerous scope is defined as
one only a person may grant (`docs/agent-api.md` §Policy). Shipping one
by default would be grain granting it on everyone's behalf.

#### 3.1.2 Grant detachment (`capabilities:detach`)

W4's grant cleanup needs to remove grants, which used to mean
`capabilities:admin`, which is dangerous. The scope
`capabilities:detach` (G6) is **not dangerous** and sandbox-denied
(MCP-only), and admits exactly:

- `POST /api/tasks/{id}/capabilities` with `attach: false` and no
  `replace`;
- `PUT /api/repos/{owner}/{name}/capabilities` and
  `…/ci-capabilities` whose body names only that list, and only entries
  already on the stored list (`storedFieldGrants`);
- in both cases with a non-empty reason, and never on a task for which
  `IsConsoleSession()` holds (design §3.4: the director does not modify
  another standing session, and not its own either).

The gate (`capabilitiesDetachRoutes` in `pkg/ui/route_scopes.go`,
`Server.capabilitiesDetachAllowed` in `pkg/ui/agent_api_gate.go`)
applies these checks only when the caller does not hold
`capabilities:admin` for the repository. The routes keep
`capabilities:admin` as their scope for attach, replace and widen.

Tier 1 MUST include `api:capabilities:detach` (the detach part of G1).
Until G1 lands, a deployment grants it to the director by hand;
otherwise detachment is Tier 2 through `api:capabilities:admin`, or is
done by proposing a `grain/config` change.

#### 3.1.3 `grain/config` access

- The director task MUST carry the state repository in `Reads` whenever
  the deployment has one. It MUST be checked out at
  `/home/debian/grain-state` before the first turn, with
  `DirectorState` reporting `ready`, `absent`, `unauthorized` or
  `unavailable` to the prompt. This is `main`'s behaviour and is
  retained.
- The checkout is push-disabled (`grain-read-only`) unless
  `propose_config_change` is enabled for the session. When enabled, the
  director MAY push only to candidate branches under
  `refs/heads/grain/config/<task-id>/*` (`model.ConfigProposalBranchPrefix`).
  Its pushes MUST NOT move the default branch, and every change MUST pass
  `staterepo.ValidateCandidateConfig` and land only through
  `pkg/trustboundary.ClassifyState` and a person's approval of the
  pinned `headSha` (§3.3).
- Enabling `propose_config_change` on the director (whether keyed on
  `Writes: [<state repo>]` as on `main` today or on a dedicated ref-scoped
  grant) MUST NOT let `AutoApprovalRefusal` auto-approve a child
  `propose_task` whose `Target` or `Writes` names `IsConfigRepo`: child
  tasks targeting or writing the state repository MUST always wait for a
  person.

#### 3.1.4 Priority admission

The director's task MUST have admission `durable` (which includes
`bypass`), set by construction in `FileConsoleSessionTask`. That admits
it outside `max_workers`, up to `max_durable_runs`
(`DefaultMaxDurableRuns = 4`), and past pipeline pauses and red-base
holds. Admission is a task setting, not a grant, and only a person may
raise it above `standard`, so the director cannot pass it to a
proposal: a child asking for `bypass` or `durable` admission (or for the
retired `durable` or `priority-dispatch` names) is held for a person.

#### 3.1.5 `TasksApproved: true`

The shipped director MUST file its proposals approved
(`QueuesProposals`) unless the operator turns this off, either with
`ConsoleSessionTasksUnapprovedTag` (paired with `ConsoleSessionTasksApprovedTag`)
or in the session row. Rationale:

- The director's job is filing work (design §1), and in a single-tenant
  deployment its proposals are ones the operator asked for in the rail.
  Under `false`, the operator approves each one a second time.
- What auto-queueing can do is bounded:
  - `AutoApprovalRefusal` still holds any proposal that asks for a
    grant, read or write the director lacks (§4), or any person-only
    grant.
  - `requires_person` catalogue rules still apply.
  - The proposal's pull request still meets its repository's review
    and merge gates, because `autoMerge` is never granted.
- `hold_for_approval: true` remains available, and the session body
  MUST tell the director to use it for anything destructive or
  surprising.
- **Precondition: a capacity bound.** `docs/director.md` gives the
  reason `main` ships `false`: "a chat is a fast way to ask for a lot of
  work". That risk is real. On `main`, a session-queued proposal is
  exempt from `AutoSubmitLimits` (`ui.Client.CreateTask` only checks
  `ExceededAutoSubmitLimit` when `autoSubmit && !sessionQueued`), so
  nothing bounded how much the director could queue. Before the default
  flips, session-queued proposals MUST be held to a fan-out bound. The
  bound is counted per conversation turn, not per task lifetime,
  because the director task is long-lived. Over the bound, a proposal
  is filed for a person with `AutoSubmitLimitNote`, as an auto-submit
  over its limit is (gap G12, done): `AutoSubmitLimits.TurnFanOut`
  (`DefaultConsoleSessionTurnFanOut`, 10), measured by
  `Store.ExceededSessionTurnLimit` as the proposals queued on
  `ConsoleSessionPrincipal` since the latest comment a person wrote on
  the session's task, in both `ui.Client.CreateTask` and
  `orchestrator.relayProposedTasks`.

#### 3.1.6 `NetworkAccess: none`

The director reads untrusted text (repository bodies, CI logs,
transcripts). Everything it needs is reached through grain itself:
`api_*` tools (model-side), the git proxy, and the namespace CLI. Under
`none`, the granule firewall still admits the segment gateway that
grain's git and model proxies are bound on, and the git proxy
(`granuleIsolationRuleset`), so
egress is not needed and would be an exfiltration path. On a backend
that cannot isolate (host-directory), `ConsoleSessionNetworkAccess`
already resolves `none` to `open`. That fallback is the honest answer
`main`'s `NetworkOpen` default was written to give, and it no longer
requires defaulting everyone to `open`. A build `DefaultOn` capability
that needs egress from the sandbox (rather than from the controller)
MUST say so in its registration, and the director's default then MUST
remain `open` on that build. The registration field is
`capability.Method.SandboxEgress`,
which registers the grant through
`model.RegisterDefaultConsoleSessionEgressCapability`. While one is
registered, `DefaultConsoleSessions` ships `NetworkOpen`.

What the verification behind G3 found, path by path:

- **State repository.** `prepareDirectorStateWithReuse` runs `git clone`
  inside the sandbox against `CloneURL(proxyBase, repo)`, the per-attempt
  git proxy. The `NetworkNone` chain accepts the proxy's address
  (`granuleIsolationRuleset`'s `allow(gitProxy)`) and the gateway's
  published service ports (`daemon.SandboxServicePorts`: every endpoint
  the fabric serves, the git proxy among them).
- **`ReadsAll` working repositories.** They are task `Reads` cloned the
  same way as any `NetworkNone` worker's, through the same proxy.
- **`grain` CLI.** `sandboxcli.detectSandboxProxy` takes its base URL
  and token from `GRAIN_SANDBOX_PROXY` or the sandbox's git credentials.
  Both name the git proxy, so the CLI needs nothing the chain refuses.
- **`api_*` tools.** They are served by `grain mcpserver` to the agent
  framework on grain's side of the boundary, so the sandbox's network
  does not enter into them.
- **Build `DefaultOn` capabilities.** Core registers none. The wrapper
  distribution's `…/corp#research={}` is a run grant whose tools run on
  the session host. Capability servers a sandbox does reach are published
  on the fabric and are therefore among the gateway's service ports.
- **Off-cluster `K8sGranuleSandboxes`.** The guest reaches controller
  ports through `granule forward` tunnels (`EndpointPorts`), not the
  network.

`TestGranuleSandboxNetworkNoneIsolationOutsideGuestVM` (nightly "real
KVM sandboxes") is the end-to-end proof that a `NetworkNone` guest still
reaches a service port at the gateway while the internet, the host's
other addresses and neighbouring containers stay unreachable.

A local policy (`grain_policy.yml`) whose `network` allows only `open`
holds a `none` director on granule. `grain init workstation` therefore
writes `network: [open, none]` for its default `-network open`.
`docs/local-policy.md` tells the owner of an older policy what to add.

### 3.2 Tier 2 — Opt-in, person-granted {#DC-4}

An operator MAY attach these to the director in the session pane or in
`grain/config` (the `console_sessions` setting of `grain_config`). The
director MUST NOT be able to obtain any of them itself:
`agentGrantRefusal` refuses every dangerous one, and
`EscalatedCapabilities` holds a proposal that asks for one.

| Grant | Unlocks | Why opt-in |
|---|---|---|
| `api:tasks:admin` | Approve proposed tasks (other than its own, §3.3), recreate sandboxes | Dangerous: approval is the person's half of every proposal gate. |
| `api:dispatch-control` (`dispatch:write`) | Pause/resume all dispatch, `run-now` (approving unapproved tasks subject to §3.3) | Deployment-wide stop, and a queue jump equivalent to `bypass` admission. MCP-only. |
| `api:capabilities:admin` | Attach/replace grants, edit capability servers and policy | Dangerous. `agentGrantEscalation` still applies, so the director can only hand out what it holds. |
| `api:release-manager` | Cut, qualify and promote candidates; write schedules and releases | Publishes artefacts to users. |
| `api:self-upgrade` (alias `self-upgrade`) | Start or cancel a drained controller upgrade (`deployment:upgrade`) | Dangerous; restarts the deployment the director is talking from. Honours upgrade holds (`agentRails`). |
| `api:deployment-ops` | Restart, reboot | Dangerous. |
| `api:test-quarantine` (`tests:write`) | Quarantine a test and file its repair | Hides a failing signal from every branch. |
| `api:secrets:read` | List secret, key and token names and the secret audit log | Names and audit trails are reconnaissance. W1 intake is write-only and does not need them. |
| `api:memory` | Write standing instructions every later run is given | Persistent prompt injection surface (`DefaultConsoleSessions` doc comment). |
| `…/grain#host-exec` (formerly `self-repair`) | Run commands on grain's host, each confirmed in chat | Can break the deployment it is talking from. Person-only (`protocol.RequirePersonGrant`), and only added to an Interactive task's run. |
| `api:teams:broadcast` | Comment on every active task | MCP-only; un-parks every waiting task. |
| `api:sessions:write` | Edit or restart other standing sessions | Conflicts with design §3.4 unless the operator decides otherwise. |

### 3.3 Tier 3 — Permanently forbidden {#DC-5}

These hold whatever the director is granted. Where a grant could
otherwise reach the action, the director kind MUST be refused at the
gate, not merely left ungranted.

1. **Secret values.** No route, tool or namespace exposes plaintext
   from `<data-dir>/secrets`, and no scope reads it. `secrets:read`
   lists names only, so "never reads secret material" is an
   architectural fact, not a grant decision. `secrets:write` and every
   `/api/state-repo/*-key` route MUST NOT be held by the director. W1
   intake is `request_secret`, whose bytes never reach the agent.
2. **Unreviewed landing.**
   - `POST /api/tasks/{id}/submit` and any `autoMerge: true` are refused
     for every agent (`errAgentAutoMerge`).
   - `merge:force` and `api:merge:force` MUST NOT be granted to the
     director kind: the built-in floor (§1.4, §5 G7) enforces it.
   - `merge-on-green` writes (`repos:write`) MUST NOT be held by the
     director either.
3. **Approving its own work.**
   - The director MUST NOT approve (`POST /api/tasks/{id}/approve`,
     `approved: true` at filing, or `POST /api/tasks/{id}/run-now` on an
     unapproved task):
     - a task it proposed or filed, directly or through descendants
       (the `LinkProposedBy` and `Origin` lineage);
     - a proposal held for a person (`requires_person`, person-only
       grants, `ConfigRepoProposalHold`, escalation, or
       `HeldForApprovalTag`).
   - This holds even under `api:tasks:admin` and `api:dispatch-control`
     (`dispatch:write`). It is a **gate rail** (`agentApprovalRail` and
     `agentFilingEscalation`, called from `agentRails` / `agentAPIGate` in
     `pkg/ui/agent_api_gate.go`), not a grant decision: it refuses every
     agent, the director included, whatever it holds. `run-now` on an
     already-approved task is not an approval and remains allowed under
     `dispatch:write`.
4. **Config approval.**
   - The director MUST NOT approve, merge or push to the default branch
     of the state repository.
   - It MUST NOT hold `settings:write`, which covers
     `POST /api/state-repo/sync|pull-requests|remote` and
     `PUT /api/settings`.
   - It MUST NOT bypass `settings_locked` or the
     `ClassifyState(...).RequiresHuman` gate on a `grain/config` PR or
     a `propose_config_change`.
5. **Self-escalation.** It MUST NOT:
   - change its own task's grants (`selfMutableTaskRoutes`);
   - change its own session row (`sessions:write` on itself);
   - grant anything it does not hold (`agentGrantEscalation`);
   - grant any person-only grant (`IsPersonOnlyGrant`).
6. **Connections and credentials.** `connections:write` and external
   agent token issuance or rotation with dangerous scopes are not
   director actions.

## 4. Proposals and grant inheritance for a `no_repo` director {#DC-6}

4.1. **What `main` did before G9.** A child that `propose_task` files from the
director is judged by `AutoApprovalRefusal(child, director, sessionQueues, seeding)`:

- `EscalatedCapabilities` returns:
  - every `GrantByLabel` capability the director does not hold, or
    that is person-only;
  - every **capability reference** (`https://…`) on the child that the
    director's grants do not cover (`UncoveredCapabilityRefs`). That
    includes references the child carries only as a repository's or
    the deployment's `default_capabilities` (`GrantByDefault`), which
    `SeededEscalations` reports as "seeded".
- A named (non-reference) default such as `grain-ci` is never an
  escalation.
- `UngrantedWrites` / `UngrantedReads` hold any extra write or read
  target the director cannot reach. The child's own `Target` is not
  counted.

The result was that a director proposal to a repository whose
`default_capabilities` include a reference, such as
`…/host-script#host-script` `{script: …}` or a `…/gcp#gcp-key`
permission set, was held unapproved. It was held only because the
director is `no_repo` and holds no repo defaults, even though a person
filing the same task by hand would get the same grant without a
prompt. G9 (§5) closes this: both filing paths (`ui.Client.CreateTask`
and `orchestrator.relayProposedTasks`) now pass the seeding source
(`model.ProposalSeeding`: the target's and the deployment's
`default_capabilities` as resolved at filing time, and the catalogue
with the child's `GrantContext`) to `model.ProposalEscalations` and
`AutoApprovalRefusal`, which apply §4.2.

4.2. **Rule.** For a proposal whose parent is the director (and only
when `QueuesProposals` holds), a grant on the child MUST NOT count as
an escalation when all of the following are true:

- it is `Via: GrantByDefault`;
- it was seeded from the child's own target repository's
  `default_capabilities`, or from the deployment's
  `Config.DefaultCapabilities`, at filing time, unchanged in its
  `with:` arguments;
- it is not person-only (`IsPersonOnlyGrant`) and no catalogue rule
  makes it `requires_person` for the child's `GrantContext`;
- the catalogue admits it for the child's repository, template and kind
  (`ValidateGrants` returns no refusal).

Every other case stays an escalation and keeps the proposal held:

- a grant the director typed (`GrantByLabel`);
- a default whose arguments the proposal changed;
- a default of a repository other than the child's target;
- a person-only or `requires_person` grant.

Writes and reads beyond the target stay held by `UngrantedWrites` /
`UngrantedReads`.

4.3. **Why this is not escalation.** The repository's
`default_capabilities` are a person's standing decision about **every**
task on that repository. The director choosing which repository to file
against does not widen that decision. An ordinary task filed by hand
gets the same grants. What the director cannot do is pick grants: it
has no route to edit `default_capabilities` (`capabilities:admin`), and
the rule never admits a grant the director wrote.

4.4. **What the proposal says.** A child admitted under §4.2 SHOULD
carry a note naming the defaults it received and where they came from,
in the voice of `CapabilityEscalationNote`, so the row shows why it
holds them. `model.SeededDefaultsNote` is that note: it names each admitted
default and whether it came from the target repository's or the
deployment's `default_capabilities`.

## 5. Gap analysis and implementation checklist {#DC-7}

| # | Area | `main` today | This spec | Change |
|---|---|---|---|---|
| G1 | `DefaultConsoleSessions().Capabilities` | **done**: `model.DefaultConsoleSessions` (`pkg/model/console_session.go`) ships all 16 Tier 1 grants plus build `DefaultOn`; saved rows stay sticky on existing deployments (`ConsoleSessionsOr`, [Task #2126](/tasks/2126)), with `model.MissingShippedConsoleSessionCapabilities` / `ui.ConsoleSessionSetting.MissingShippedDefaults` surfacing a one-click "Adopt shipped defaults" banner in `ui/src/components/ConsoleSessionPage.jsx` (`pkg/ui/console_sessions.go`); pinned by `pkg/model/console_session_test.go`, `pkg/ui/console_sessions_test.go` and `ui/src/components/ConsoleSessionPage.test.jsx` | Tier 1 list (§3.1) | None. |
| G2 | `TasksApproved` | **done**: `model.DefaultConsoleSessions` (`pkg/model/console_session.go`) ships `TasksApproved: true` on `director`; saved `ConsoleSessions` rows (`ConsoleSessionsOr`) and active-task overrides (`ConsoleSessionTasksApprovedTag` / `ConsoleSessionTasksUnapprovedTag`) stay sticky across reconciles and active-session edits; pinned by `pkg/model/console_session_test.go` and `pkg/orchestrator/consolesessions_test.go` | `true` (§3.1.5) | None. |
| G3 | `NetworkAccess` | **done**: `NetworkNone` with the `ConsoleSessionNetworkAccess` fallback, and `NetworkOpen` on a build registering a `SandboxEgress` `DefaultOn` capability | `NetworkNone` with the `ConsoleSessionNetworkAccess` fallback | None. The verification is recorded in §3.1.6. |
| G4 | State repo read | **done**: implicit via `StateRepo`; the checkout is read-only for a session without `grain/config` in `Writes`, pinned by `consolesessions_test.go` and `director_state_internal_test.go` | Unchanged | None. |
| G5 | `propose_config_change` & config-repo proposal rail | **done**: `model.ConfigRepoProposalHold` holds every child whose `Target` or `Writes` names `IsConfigRepo` (first in `AutoApprovalRefusal`, and as a grain hold in `ui.Client.CreateTask` and `orchestrator.relayProposedTasks`); `DefaultConsoleSessions` ships the director with `Writes: [grain/config]` | Shipped on the director, while child `propose_task` calls targeting or writing `IsConfigRepo` always wait for a person (§3.1.3) | None. Pinned by the `director_proposal_rails` tests in `pkg/model`, `pkg/ui` and `pkg/orchestrator`. |
| G6 | `capabilities:detach` | **done**: in `model.apiScopes` (not dangerous) and `IsSandboxDeniedAPIScope`; admitted by `capabilitiesDetachRoutes` (`route_scopes.go`) and `capabilitiesDetachAllowed` / `capabilitiesDetachBodyAllowed` (`agent_api_gate.go`); shipped in `DefaultConsoleSessions` (G1); pinned by `pkg/ui/capabilities_detach_test.go`; documented in `docs/agent-api.md` | New non-dangerous, MCP-only scope (§3.1.2) | None. |
| G7 | Default catalogue for the director kind | **done**: built-in floor `model.DirectorFloorRefusal` (`pkg/model/director_floor.go`) applied by `ValidateGrants`/`Refusals` under any `settings/capabilities.yml` and on a nil catalogue; enforced at dispatch (`catalogueHold`), on grant edits (`catalogueEditRefusal`), on session saves (`ValidateConsoleSessions`) and at the API gate (`WithinDirectorFloor`); tests in `pkg/model`, `pkg/ui`, `pkg/orchestrator` | Shipped rule set denying the director kind `merge:force`, `settings:write`, `secrets:write`, `connections:write`, `repos:write` | None. Operator files may narrow the director further, never widen it past the floor. |
| G8 | Self-approval | **done**: `agentFilingEscalation` refuses `approved: true` for every in-grain task agent, and `agentRails` (`agentApprovalRail` in `pkg/ui/agent_api_gate.go`) refuses an agent's `POST /api/tasks/{id}/approve` (and `POST /api/tasks/{id}/run-now` on an unapproved task) on a task in its own proposal or `Origin` lineage or held for a person (`HeldForApprovalTag`, `ConfigRepoProposalHold`, `ChangeTargetNeedsPersonApproval`, declared `push:` sets, ungranted `AutoMerge`, catalogue `requires_person`, person-only grants, `KeepRunning`, and `EscalatedCapabilities` / `UngrantedWrites` / `UngrantedReads` / `PersonOnlyProposalSettings` against the task's proposer), pinned by `pkg/ui/agent_approval_rail_test.go` | Unchanged | None. |
| G9 | Seeded repo defaults on director proposals | **done**: admitted under §4.2 by `model.ProposalEscalations` (`pkg/model/proposal_seeding.go`), which `AutoApprovalRefusal` (`hold_for_approval.go`), `ui.Client.CreateTask` and `orchestrator.relayProposedTasks` take the seeding source (`model.ProposalSeeding`) for; §4.4 note is `model.SeededDefaultsNote`; each excluded case pinned by `proposal_seeding_test.go`, `director_seeded_defaults_test.go` and `consolesessions_test.go` | Admitted under §4.2 | None. |
| G10 | `DirectorSessionBody` | **done**: `model.DirectorSessionBody` (`pkg/model/console_session.go`) instructs passing `expect` state preconditions and explicit reasons on stop/close calls, and passing `hold_for_approval: true` on destructive, irreversible, or surprising `propose_task` calls; pinned by `pkg/model/console_session_test.go` | Tells the director to use `expect` on stop/close, `hold_for_approval` for destructive proposals, and give reasons | None. |
| G11 | Docs | **done**: retired capability names updated in [Task #3185](/tasks/3185); `NetworkAccess` row of `docs/director.md`'s envelope table and `docs/local-policy.md` updated in [Task #3312](/tasks/3312) (G3); `Capabilities` row updated in [Task #3320](/tasks/3320) (G1); `TasksApproved` row and prose in `docs/director.md` and §2 W3 table in `design/design/director.design.md` updated in [Task #3323](/tasks/3323) (G2) | Matches §3 | None. |
| G12 | Capacity bound on session-queued proposals | **done**: `AutoSubmitLimits.TurnFanOut` (default 10), checked by `Store.ExceededSessionTurnLimit` beside `ExceededAutoSubmitLimit` in `ui.Client.CreateTask` and `orchestrator.relayProposedTasks`; over it, filed for a person with `AutoSubmitLimitNote` | Per-turn fan-out bound; over it, filed for a person (§3.1.5) | None. The bound has no settings knob yet. |

Ordering: G1–G12 are all done (G3's verification is recorded in §3.1.6). G2
landed last, after the Tier 3 rails (G5's `IsConfigRepo` proposal hold, G7's
built-in floor, G8's approval rail and G12's fan-out bound) and G1/G10
(`DirectorSessionBody` guidance) were in place.
