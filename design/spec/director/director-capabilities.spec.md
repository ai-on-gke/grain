# Director capabilities and session envelope

**Status:** draft for review. Normative.

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
  today. That is a bare capability id (`self-debug`), `api:<scope>`
  (`api:ci:read`), `api:<preset>` (`api:task-admin`), or either api form
  narrowed with `@owner/name`.
- The **canonical** unified-grant spelling of
  [`design/design/unified-grants.design.md`](../../design/unified-grants.design.md)
  §3–§4: `https://grain.dev/capabilities/api#<scope-or-preset>`,
  `…/grain#<method>`, `…/repo#read|write`, `…/skill#skill`, and so on.
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
  - `Capabilities`: `self-debug`, `grain-log`, `deployment-health` and
    `bootstrap-playbooks`, plus every build `DefaultOn` registration
    (`RegisterDefaultConsoleSessionCapability`, e.g. `corp-research` on
    the Google build).
  - `NetworkAccess: NetworkOpen`. This is **not** `none`, which
    `docs/director.md` and the task that filed this spec both say. Its
    doc comment explains why: the host-directory sandbox cannot honour
    `none`.
  - `TasksApproved` is unset (`false`). `Reads` and `Writes` are empty.
- `model.ConsoleSessionGrants` always prepends `durable` (`GrantByGrain`),
  which implies `priority-dispatch`.
- `orchestrator.fileConsoleSession` passes the state repository
  (`Config.settingsRepo()`) as `StateRepo`, so the director task's
  `Reads` always holds `grain/config` (or whatever repository the
  deployment uses). That read does not come from a grant.
- `prepareDirectorStateWithReuse` clones that repository to
  `/home/debian/grain-state` and sets its push URL to `grain-read-only`.
  The checkout is read-only.
- The `self-debug` alias also grants the `api:self-debug` preset
  (`tasks:read`, `deployment:read`).
- `CatalogueKindDirector` is only a kind that `CatalogueKindOf` returns
  and `kindWord` prints. **No `DefaultCapabilityCatalogue` exists** on
  `main`: a deployment without `settings/capabilities.yml` has a nil
  catalogue, which admits everything, and no shipped rule names the
  director kind.
- `propose_config_change` is designed
  (`design/design/config-repo-settings.design.md` §4.3) but **not implemented**.
  No tool or route of that name exists under `pkg/`.

## 2. Workflow-to-capability traceability matrix {#DC-2}

Each row names a surface the director uses, the grant that reaches it
on `main`, its canonical reference, and the tier (§3) the grant belongs
to. *Implicit* means the grant comes with the task rather than from
`Capabilities`.

### 2.1 W1 — Bootstrap and onboarding; W2 — Observability and diagnosis

| Surface (tool / CLI / route) | Current grant | Canonical reference | Tier |
|---|---|---|---|
| Source checkout, metrics tools, `grain task list\|read\|prompt\|transcript`, `task://`, `src://` | `self-debug` (alias → `api:self-debug` = `tasks:read` + `deployment:read`) | `…/api#self-debug` + `…/grain#source` *(proposed)* | 1 |
| Daemon journal (`grain-log`) | `grain-log` | `…/grain#journal` *(proposed)* | 1 |
| Host / sandbox probes; `GET /api/deployment/*` reads | `deployment-health` | `…/api#deployment:read` + `…/grain#health` *(proposed)* | 1 |
| Bring-up runbooks | `bootstrap-playbooks` | `…/skill#skill` `{dir: bootstrap}` | 1 |
| `request_secret` (write-only intake into `<data-dir>/secrets`) | none (a turn-ending tool that every task holds) | — | 1 |
| `GET /api/tasks`, `GET /api/tasks/{id}`, `GET /api/states` | `api:tasks:read` (via `api:self-debug`) | `…/api#tasks:read` | 1 |
| `GET /api/ci/runs`, `/api/ci/runs/{id}`, `/api/ci/faults`, artifacts; `grain ci list\|log\|test-history`; `ci://` | `api:ci:read` (or preset `api:ci-read`) | `…/api#ci:read` | 1 |
| `grain merge-queue`, base-branch gates | `api:ci:read` | `…/api#ci:read` | 1 |
| Repository settings, branches, declarations | `api:repos:read` | `…/api#repos:read` | 1 |
| Schedules, templates, suites, releases (read) | `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:releases:read` | `…/api#<scope>` | 1 |
| Dispatch pause state | `api:dispatch:read` | `…/api#dispatch:read` | 1 |
| `GET /api/config`, `GET /api/state-repo*` | `api:settings:read` | `…/api#settings:read` | 1 |
| `/home/debian/grain-state` (`tables/`, `settings/`, `memory/`) | implicit `Reads: [<state repo>]` | `…/repo#read` `{repo: grain/config}` | 1 (implicit) |
| Edition defaults (`corp-research` on Google builds) | build `DefaultOn` registration | `…/corp#research` *(proposed)* | 1 (build) |

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
| Detach only (no attach, no replace, never on a standing session) | **`api:capabilities:detach` (proposed new scope)**, not dangerous, reason required | `…/api#capabilities:detach` *(proposed)* | 1 once it exists (§3.1.2) |
| Read deployment config | implicit state-repo read; `api:settings:read` | `…/repo#read` `{repo: grain/config}` | 1 |
| `propose_config_change` (branch, validate, pin `headSha`, in-chat approval card) | **(proposed)**: implicit write to the state repo on `director/*` branches only | `…/repo#write` `{repo: grain/config, refs: [director/*]}` *(proposed)* | 1 once it exists (§3.1.3) |
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
| `Capabilities` | `self-debug`, `grain-log`, `deployment-health`, `bootstrap-playbooks`, `api:task-admin`, `api:capabilities:read`, `api:settings:read`, `api:repos:read`, `api:releases:read`, `api:schedules:read`, `api:templates:read`, `api:suites:read`, `api:dispatch:read`, plus build `DefaultOn` ids | W1–W5 day-to-day. None of these is dangerous, so none needs a person's grant under `IsPersonOnlyGrant`. |
| (implicit) `durable` | always, via `ConsoleSessionGrants` | Priority admission (§3.1.4). |
| `Reads` | `[]` in the row; the state repository is added at filing (`FileConsoleSessionOptions.StateRepo`) | Config is always readable without the operator naming a repo (§3.1.3). |
| `Writes` | `[]` | The director has no target and lands no code. Config writes go through §3.1.3. |
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

#### 3.1.2 Grant detachment (proposed `capabilities:detach`)

W4's grant cleanup needs to remove grants, and on `main` that means
`capabilities:admin`, which is dangerous. This specification proposes a
new scope `capabilities:detach`, **not dangerous** and sandbox-denied
(MCP-only), that admits exactly:

- `POST /api/tasks/{id}/capabilities` with `attach: false` and no
  `replace`;
- `PUT /api/repos/{owner}/{name}/capabilities` and
  `…/ci-capabilities` whose body is a subset of the stored list
  (`storedFieldGrants` already computes that);
- in both cases with a non-empty reason, and never on a task for which
  `IsConsoleSession()` holds (design §3.4: the director does not modify
  another standing session, and not its own either).

Once `capabilities:detach` exists, Tier 1 MUST include
`api:capabilities:detach`. Until then, detachment is Tier 2 through
`api:capabilities:admin`, or is done by proposing a `grain/config`
change.

#### 3.1.3 `grain/config` access

- The director task MUST carry the state repository in `Reads` whenever
  the deployment has one. It MUST be checked out at
  `/home/debian/grain-state` before the first turn, with
  `DirectorState` reporting `ready`, `absent`, `unauthorized` or
  `unavailable` to the prompt. This is `main`'s behaviour and is
  retained.
- The checkout MUST stay push-disabled (`grain-read-only`) until
  `propose_config_change` is implemented. When it is, the director MAY
  push only to branches under `director/`. Its pushes MUST NOT move the
  default branch, and every change MUST land only through
  `pkg/trustboundary.ClassifyState` and a person's approval of the
  pinned `headSha` (§3.3).
- Writes to the state repo MUST NOT be expressed as an entry in the
  session's `Writes`. That would let `UngrantedWrites` treat the
  director's proposals that write `grain/config` as already granted,
  so they would no longer wait for a person.

#### 3.1.4 Priority admission

The director MUST hold `durable` (and so `priority-dispatch`) through
`ConsoleSessionGrants`. That admits it outside `max_workers`, up to
`max_durable_runs` (`DefaultMaxDurableRuns = 4`), and past pipeline
pauses and red-base holds. `durable` is a person-only grant
(`IsPersonOnlyGrant`), so it stays implicit and is never written into
`Capabilities`. The director cannot pass it to a proposal: a child
asking for `durable` or `priority-dispatch` is held for a person.

#### 3.1.5 `TasksApproved: true`

The shipped director MUST file its proposals approved
(`QueuesProposals`) unless the operator turns this off, either with
`ConsoleSessionTasksApprovedTag` or in the session row. Rationale:

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
  nothing bounds how much the director can queue. Before the default
  flips, session-queued proposals MUST be held to a fan-out bound. The
  bound is counted per conversation turn, not per task lifetime,
  because the director task is long-lived. Over the bound, a proposal
  is filed for a person with `AutoSubmitLimitNote`, as an auto-submit
  over its limit is today (gap G12).

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
remain `open` on that build.

### 3.2 Tier 2 — Opt-in, person-granted {#DC-4}

An operator MAY attach these to the director in the session pane or in
`grain/config` (the `console_sessions` setting of `grain_config`). The
director MUST NOT be able to obtain any of them itself:
`agentGrantRefusal` refuses every dangerous one, and
`EscalatedCapabilities` holds a proposal that asks for one.

| Grant | Unlocks | Why opt-in |
|---|---|---|
| `api:tasks:admin` | Approve proposed tasks (other than its own, §3.3), recreate sandboxes | Dangerous: approval is the person's half of every proposal gate. |
| `api:dispatch-control` (`dispatch:write`) | Pause/resume all dispatch, `run-now` | Deployment-wide stop, and a queue jump equivalent to `priority-dispatch`. MCP-only. |
| `api:capabilities:admin` | Attach/replace grants, edit capability servers and policy | Dangerous. `agentGrantEscalation` still applies, so the director can only hand out what it holds. |
| `api:release-manager` | Cut, qualify and promote candidates; write schedules and releases | Publishes artefacts to users. |
| `api:self-upgrade` (alias `self-upgrade`) | Start or cancel a drained controller upgrade (`deployment:upgrade`) | Dangerous; restarts the deployment the director is talking from. Honours upgrade holds (`agentRails`). |
| `api:deployment-ops` | Restart, reboot | Dangerous. |
| `api:test-quarantine` (`tests:write`) | Quarantine a test and file its repair | Hides a failing signal from every branch. |
| `api:secrets:read` | List secret, key and token names and the secret audit log | Names and audit trails are reconnaissance. W1 intake is write-only and does not need them. |
| `api:memory` | Write standing instructions every later run is given | Persistent prompt injection surface (`DefaultConsoleSessions` doc comment). |
| `self-repair` | Run commands on grain's host, each confirmed in chat | Can break the deployment it is talking from. |
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
     director kind: the catalogue rule in §5 enforces it.
   - `merge-on-green` writes (`repos:write`) MUST NOT be held by the
     director either.
3. **Approving its own work.**
   - The director MUST NOT approve:
     - a task it proposed, directly or through descendants
       (the `LinkProposedBy` chain);
     - a proposal held for `requires_person`, escalation or
       `HeldForApprovalTag`.
   - This holds even under `api:tasks:admin`. It is a **proposed gate
     rail**: `main` relies on `tasks:admin` being person-granted.
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

4.1. **What `main` does.** A child that `propose_task` files from the
director is judged by `AutoApprovalRefusal(child, director, sessionQueues)`:

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

The result: a director proposal to a repository whose
`default_capabilities` include a reference, such as
`…/host-script#host-script` `{script: …}` or a `…/gcp#gcp-key`
permission set, is held unapproved. It is held only because the
director is `no_repo` and holds no repo defaults, even though a person
filing the same task by hand would get the same grant without a
prompt.

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
holds them.

## 5. Gap analysis and implementation checklist {#DC-7}

| # | Area | `main` today | This spec | Change |
|---|---|---|---|---|
| G1 | `DefaultConsoleSessions().Capabilities` | 4 ids + build `DefaultOn` | Tier 1 list (§3.1) | Append the api grants in `pkg/model/console_session.go`, and update its doc comment and `docs/director.md`. Existing deployments keep their saved row: surface the new defaults as a one-click "adopt shipped defaults" in the session pane, not a silent rewrite (grain/task-2126 keeps operator removals sticky). |
| G2 | `TasksApproved` | `false` | `true` | Set in `DefaultConsoleSessions`. `ConsoleSessionTasksApprovedTag` / its opposite keep an operator's override. Add tests in `pkg/model`. |
| G3 | `NetworkAccess` | `NetworkOpen` | `NetworkNone` with the `ConsoleSessionNetworkAccess` fallback | Change the default. First verify on granule that the state-repo clone, `grain` CLI and `api_*` tools work under `none`, and that no build `DefaultOn` capability needs sandbox egress. |
| G4 | State repo read | implicit via `StateRepo`, read-only checkout | Unchanged | None; pin the behaviour with a test that the director task's `Reads` contains the state repo. |
| G5 | `propose_config_change` | not implemented | Push to `director/*` on the state repo, approval via `trustboundary` | Implement per `config-repo-settings.md` §4.3; not part of the session's `Writes` (§3.1.3). |
| G6 | `capabilities:detach` | does not exist | New non-dangerous, MCP-only scope (§3.1.2) | Add it to `model.apiScopes`, `IsSandboxDeniedAPIScope`, `route_scopes.go` (a detach-body check like `tasksAdminTaskPatchBodyAllowed`), `docs/agent-api.md`; then add it to G1. |
| G7 | Default catalogue for the director kind | none (`DefaultCapabilityCatalogue` absent; nil catalogue admits all) | Shipped rule set denying the director kind `merge:force`, `settings:write`, `secrets:write`, `connections:write`, `repos:write` | Add a built-in restriction layer applied under any `settings/capabilities.yml`, keyed on `CatalogueKindDirector` (operator files may narrow, not widen). |
| G8 | Self-approval | relies on `tasks:admin` being person-granted | Gate refuses `POST /api/tasks/{id}/approve` on a task in the actor's proposal subtree or held for a person | Add to `agentRails` in `pkg/ui/agent_api_gate.go`, with tests. |
| G9 | Seeded repo defaults on director proposals | held as "seeded" escalations | Admitted under §4.2 | Change `EscalatedCapabilities` / `AutoApprovalRefusal` (`pkg/model/auto_submit.go`, `hold_for_approval.go`) to take the seeding source; tests for each excluded case. |
| G10 | `DirectorSessionBody` | — | Tells the director to use `expect` on stop/close, `hold_for_approval` for destructive proposals, and give reasons | Update the reviewed body text. |
| G11 | Docs | `docs/director.md` lists 4 capabilities and claims `none` | Matches §3 | Update with G1–G3. |
| G12 | Capacity bound on session-queued proposals | session-queued proposals skip `AutoSubmitLimits` | Per-turn fan-out bound; over it, filed for a person (§3.1.5) | Add the check beside `ExceededAutoSubmitLimit` in `ui.Client.CreateTask` and `orchestrator.relayProposedTasks`, with tests. |

Ordering: G4 and G10 are independent. G6 precedes the detach part of
G1, and G3's verification precedes its default change. G7, G8 and G12
MUST land before G2, so that the auto-queueing director never runs
on a deployment without the Tier 3 rails.
