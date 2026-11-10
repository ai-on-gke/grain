# Audit: Non-Admin Escalation Through Tasks, Templates, Grants, and Approvals (2026-11-10)

Task 3982. Companion to #3965 (`docs/security/audits/2026-11-10-insider-risk.md`),
#3905 (`docs/security/audits/2026-11-10-skipped-reviews.md`), #3904 (security-review
holds on Submit), and the route-authorization matrix audit.

**Question:** Can a signed-in non-admin user whose role lacks dangerous scopes
(`capabilities:admin`, `secrets:*`, `settings:*`, `merge:force`, `tasks:admin`,
`deployment:*` — or holds only a repository-limited subset of them, so
`accessRole.Full == false`) use grain's task, template, access-request, agent,
merge, schedule, or console-session machinery to indirectly obtain those powers
or the credentials behind them?

**Principle enforced:** A person may only attach, approve, steer, merge, or run
grants and settings within their own role, enforced server-side across REST,
CLI, and MCP (`/mcp` and `/api/mcp`).

## Summary

| # | Path | Surface | Status | Severity |
|---|------|---------|--------|----------|
| 1a | Filing tasks or instantiating templates with `gcp#gcp-key`, `host-script`, dangerous/person-only/unheld `api:*`, `github-credential`, `networkAccess: open`, `keepRunning`, `admission`, `declaredPush`, or unreachable `reads`/`writes` | UI form, `POST /api/tasks`, `POST /api/tasks/from-template`, `grain create`, `/mcp`, `/api/mcp` | Closed; regression tests added | -- |
| 1b | Direct `POST /api/tasks` with `approved: true` / `interactive: true` targeting `grain/config` or a person-held change target by a limited `tasks:admin` role or delegated caller | `pkg/ui/agent_api_gate.go`, `pkg/ui/client.go` | **Fixed here** | High |
| 1c | Omitting `capabilities` on `POST /api/tasks` attaches `DefaultCapabilities` even when vetoed by a role's `deny` list | `pkg/ui/client.go`, `pkg/ui/access_roles.go` | **Fixed here** | Medium |
| 1d | `GET /api/persistent-sandboxes/{name}/logs` (`deployment:read`) serves unredacted output containing bound secrets and ignores repository limits | `pkg/ui/persistent_sandboxes.go`, `pkg/ui/access_roles.go` | **Fixed here** | High |
| 1e | Per-task `sandboxCpus`, `sandboxMemoryMb`, and `sandboxDiskGb` have no upper bound or role/agent ceiling | `pkg/ui/client.go`, `pkg/ui/agent_api_gate.go` | Open; follow-up task filed (`knownGapSandboxShapeCeiling`) | Medium |
| 2a | `PATCH /api/tasks/{id}` adding capabilities, `autoMerge`, elevated `priority`, widened `networkAccess`, or out-of-scope `reads`/`writes`; `approve` and unapproved `run-now` on wider proposals (#3735) | `pkg/ui/access_roles.go`, `pkg/ui/agent_api_gate.go` | Verified closed (#3735, #3759, #3923, #3925); regression tests added | -- |
| 2b | Overwriting `description` (prompt) via `PATCH /api/tasks/{id}`, or calling approved `run-now`, `retry`, `reopen`, `undefer`, `resume`, `comments`, or `reply` on an operator-filed task carrying wider grants | `pkg/ui/access_roles.go`, `pkg/ui/agent_api_gate.go` | **Fixed here** | Critical |
| 2c | Ownerless `gma_` external token holding `tasks:admin` approving a proposal past the token's own grant ceiling | `pkg/ui/agent_api_gate.go` (`externalApprovalRail`) | Closed by #3924 (`externalApprovalWithin`); regression test flipped | -- |
| 3a | Non-admin without `capabilities:admin` (or any agent / delegated caller) granting a `request_access` card | `pkg/ui/access_roles.go`, `pkg/ui/task_access_request.go` | Closed; regression tests added | -- |
| 3b | Limited role holding `capabilities:admin` (e.g. repo-scoped) granting `gcp#gcp-key`, `host-script`, dangerous `api:*`, `network: open`, or out-of-scope repos through `POST /api/tasks/{id}/access-request/grant` | `pkg/ui/task_access_request.go` | **Fixed here** | Critical |
| 4a | Agent `propose_task` auto-approval ceilings, self-mutation blocks, and in-sandbox `grain api` channel (`GitProxy.ServeAPIHTTP` + `SandboxAPIGrantsOf`) | `pkg/ui/client.go`, `pkg/ui/agent_api_gate.go`, `pkg/gitproxy/namespace.go` | Closed; regression tests added | -- |
| 4b | In-grain task agent calling `POST /api/tasks/{id}/comments` or `/reply` clears human-held `PendingQuestionCommentID` / `PendingSecret` / `PendingAccess` | `pkg/ui/client.go` (`addAttributedComment`) | **Fixed here** | High |
| 4c | Agent holding non-dangerous `api:tasks:write` on a repo overwriting `description`/`promptExtension` or steering another task in that repo that holds wider grants | `pkg/ui/agent_api_gate.go`, `pkg/ui/agent_cross_task.go` | Fixed by task 4019 (`agentCrossTaskRail`); `knownGapAgentCrossTaskPromptSteering` flipped | High |
| 5a | Non-admin pressing `Submit` (`POST /api/tasks/{id}/submit`) or `config-change/approve` (#3965) | `pkg/ui/agent_api_gate.go`, `pkg/ui/task_config_change.go` | Closed; regression tests added | -- |
| 5b | Non-admin releasing a `HumanReviewHold` (`POST /api/tasks/{id}/human-review/release`, scope `tasks:write`) to re-arm `AutoMerge: true`, or calling `POST /api/tasks/{id}/force-merge` under a limited role | `pkg/ui/agent_api_gate.go`, `pkg/ui/human_review.go`, `pkg/ui/client.go` | **Fixed here** | Critical |
| 6a | Creating, editing, or running a schedule (`POST /api/schedules`, `PATCH /api/schedules/{id}`, `POST /api/schedules/{id}/run`) without `tasks:write` on repo, targeting `grain/config`, or carrying wider `networkAccess`/grants | `pkg/ui/access_roles.go` | **Fixed here** | High |
| 6b | Limited `sessions:write` role creating a console session that silently inherits Director capabilities, `NetworkOpen`, `ReadsAll`, and `grain/config` reads, or editing/restarting/messaging `director` or attaching to a privileged task's live PTY (`/api/console/{id}/*`) | `pkg/ui/console_sessions.go`, `pkg/ui/access_roles.go`, `pkg/ui/console.go` | **Fixed here** | Critical |

---

## 1. Filing a Task with Grants Beyond Your Role

### What was already closed

Every task-creation entry point — the browser form (`POST /api/tasks`), `grain create`
(`ui.HTTPClient.CreateTask`), `POST /api/tasks/from-template`, `POST /mcp`, and
`POST /api/mcp` (`grain_invoke` via `dispatchSubrequest`) — passes through
`Server.roleGate` -> `authorizeRoleCall` (`pkg/ui/access_roles.go:753-818`).

- **Explicit capability grants (`gcp#gcp-key`, `host-script`, `api:*`, `github-credential`):**
  `roleGrantEscalation` -> `roleGrantRefusal` (`pkg/ui/access_roles.go:822-849`) inspects
  every capability named in `capabilities`. Any non-`api:*` external capability (including
  `gcp#gcp-key`, `host-script#*`, and CI-only `github-credential:<name>`) is rejected by
  `agentGrantRefusal` because human roles resolve only to `api:*` scope sets; any dangerous
  `api:*` scope (`capabilities:admin`, `secrets:*`, `settings:*`, `tasks:admin`,
  `merge:force`, `deployment:upgrade`, `deployment:restart`, `deployment:reboot`,
  `access:write`), any `RequiresPerson` scope set (`grantNamesPersonScopeSet`), any
  unheld `api:*` scope, and any `api:<scope>@<repo>` outside the role's repository limits
  is refused with `403 Forbidden`.
- **Network access & standing task execution:** `filingEscalation` (`pkg/ui/agent_api_gate.go`)
  refuses `networkAccess: "open"` whenever `networkAccessWidens` reports it exceeds the
  deployment default (`cfg.NetworkAccess`), refuses `autoMerge: true` and `keepRunning: true`
  for all limited roles (`!a.Full`), and maps `admission: "durable"` / `"bypass"` to
  `durable` / `priority-dispatch` (refused for limited roles).
- **Repository reach & templates:** `repo`, `reads`, `writes`, `reviewTemplateId`, and
  `templateId` (both stored templates and `.grain/templates/` `declaredPush` templates from
  #3923) are checked against `allowedRepo("tasks:write", ...)` / `allowedRepo("tasks:read", ...)`
  and `checkFilingTemplate`. Hidden `new-task-form.yml` capability defaults live only in
  `GET /api/new-task-form` UI hints; `Client.creationGrants` never auto-attaches hidden
  form capabilities server-side.
- **Persistent sandboxes:** `/staging/{name}/{path...}` is in `operatorOnlyRoutes`
  (`pkg/ui/access_roles.go:108`), and `POST /api/persistent-sandboxes/{name}/{action}`
  requires `deployment:restart` / `deployment:write`.

### Reproductions and fixes landed here

1. **Direct `approved: true` / `interactive: true` filings targeting `grain/config` or
   person-held change targets (`pkg/ui/agent_api_gate.go`, `pkg/ui/client.go`):**
   `ConfigRepoProposalHold` (`grain/config`) and `ChangeTargetNeedsPersonApproval` previously
   cleared `req.Approved` only when `proposedBy != nil` (agent proposals). A limited human
   role holding `tasks:admin` could call `POST /api/tasks` directly with `approved: true`
   and `writes: [{"repo": "grain/config"}]` (if also holding `tasks:write` unscoped) or a
   person-held `changeTarget` and skip the operator gate. `roleApprovedTargetRefusal` now
   refuses `approved: true` / `interactive: true` for limited roles, and `Client.CreateTask`
   forces `catalogueHold, req.Approved = true, false` for limited roles, agents, and
   delegated callers even when `proposedBy == nil`.
2. **Role `deny` vetoes vs. `DefaultCapabilities` (`pkg/ui/client.go`, `pkg/ui/access_roles.go`):**
   When `POST /api/tasks` omitted `capabilities`, `creationGrants` attached
   `Config.DefaultCapabilities` and per-repo `DefaultCapabilities` (`GrantByDefault`)
   without checking `accessRole.deny`. `creationGrants` now strips any default grant whose
   resolved scopes intersect `a.deny` (`grantDeniedByRole`).
3. **Persistent sandbox logs secret redaction and repo scoping (`pkg/ui/persistent_sandboxes.go`,
   `pkg/ui/access_roles.go`):**
   `GET /api/persistent-sandboxes/{name}/logs` maps to `deployment:read` (included in
   `grain/viewer` and `grain/contributor`). Unlike `GET /api/logs/{source}` (`logs.go`),
   `handlePersistentSandboxLogs` wrote `ps.Logs(name)` raw without running `LogRedactor` /
   `grainlog.NewRedactor` and without resolving the persistent sandbox's repository in
   `authorizeRoleCall`. A viewer or contributor could read bound secret values printed by a
   standing preview sandbox, and a repo-limited role could read logs from other repositories'
   persistent sandboxes. `authorizeRoleCall` now resolves `persistentSandboxRepo`, and
   `handlePersistentSandboxLogs` redacts through `Config.LogRedactor` (failing closed on
   redactor error, matching `logs.go`) or `grainlog.NewRedactor(nil)`.

### Follow-up task filed

- **Per-task sandbox resource shape (`sandboxCpus`, `sandboxMemoryMb`, `sandboxDiskGb`):**
  `validateSandboxShape` (`pkg/ui/client.go`) validates only lower bounds (`>= 1` CPU,
  `>= 128` MB RAM, `>= 0` GB disk), and neither `filingEscalation` nor `propose_task`
  enforces an upper bound or role ceiling. Pinned by `TestPath1KnownGapPerTaskSandboxShapeUncapped`
  (`knownGapSandboxShapeCeiling`).

---

## 2. Editing an Existing Task, Running, Retrying, and Approving Wider Grants

### Verification of #3735 and direct `PATCH` rails

We audited #3735 (`pkg/ui/access_roles.go` `roleApprovalWithin`), together with #3759, #3923,
and #3925:
- `PATCH /api/tasks/{id}` modifying `capabilities` requires `capabilities:admin` plus
  `roleGrantRefusal`; modifying `autoMerge: true` requires `grain/operator`; `priority > 5`,
  `fixesBaseBranch`, and `autoApproveProposals` require `tasks:admin`; `reads`, `writes`,
  `networkAccess`, `keepRunning`, `admission`, and `reviewTemplateId` are checked by
  `filingEscalation`.
- `POST /api/tasks/{id}/approve` and `POST /api/tasks/{id}/run-now` on an unapproved task
  invoke `roleApprovalWithin`, requiring `tasks:admin` on the task's repository and checking
  every stored grant, `autoMerge`, `keepRunning`, `declaredPush`, `ConfigRepoProposalHold`,
  `ChangeTargetNeedsPersonApproval`, `networkAccess`, `admission`, `writes`, `reads`, and
  `reviewTemplateId`. Fix #3735 (with its #3923/#3925 extensions) is sound for human roles.
- Note on external `gma_` tokens: as documented in #3905 Finding 2.2 (which already filed the
  remediation task), `externalApprovalRail` checks the token owner's human role when an
  identity verifier is active, while ownerless local tokens do not yet run `roleTaskWithin`
  against the token's own scope/repo ceiling. #3924 closed it (`externalApprovalWithin`), and
  `TestPath2ExternalTokenApprovalCeiling` now asserts the refusal.

### Reproductions and fixes landed here

1. **Prompt overwrite and execution steering on already-approved privileged tasks
   (`pkg/ui/access_roles.go`):**
   `roleStandingTargetEscalation` checked stored task grants only on `approve` and
   *unapproved* `run-now` (`if task.Approval != nil { return nil }`). Once an operator filed
   or approved a task carrying `gcp#gcp-key`, `host-script`, `api:secrets:write`,
   `networkAccess: "open"`, or `autoMerge: true`:
   - Any `grain/contributor` holding `tasks:write` on that repository could `PATCH /api/tasks/{id}`
     with `{"description": "..."}` (overwriting `Task.Body`, the agent's prompt!) or `{"model": ...}`
     because `addedGrantFieldsInBody` saw no capability fields in the PATCH body.
   - Any caller holding `dispatch:write` could trigger `POST /api/tasks/{id}/run-now` on the
     already-approved privileged task; any caller holding `tasks:write` could call
     `retry`, `reopen`, `undefer`, `resume`, `comments`, or `reply` to re-dispatch or inject
     human-attributed instructions into the privileged run.
   `roleStandingTargetEscalation` now runs `Client.roleTaskWithin` across `PATCH /api/tasks/{id}`,
   approved `POST /api/tasks/{id}/run-now`, `retry`, `reopen`, `undefer`, `resume`, `comments`,
   and `reply`. Plain tasks carrying only `GrantByDefault` capabilities remain editable and
   retryable by `grain/contributor` unless vetoed by `a.deny`.
2. **External token + limited human role intersection (`pkg/ui/agent_api_gate.go`):**
   In `authorizeExternalAgentCall`, when an outer limited `accessRole` was present on context,
   `set` was intersected with `outerRole` while `held` (passed into `filingEscalation`) kept
   the raw token grants. `held` is now narrowed via `grantListFromSet(set)`.

---

## 3. Access Requests (`request_access`)

### What was already closed

`POST /api/tasks/{id}/access-request/grant` and `.../deny` carry `routeScopes` `"internal"`
and `humanRouteScopes` `"capabilities:admin"` (`pkg/ui/access_roles.go:75-76`), and
`checkDelegatedPersonOnly` (`pkg/ui/access_roles.go:648-650`) plus `Client.GrantAccessRequest`
(`pkg/ui/task_access_request.go`) refuse non-human and delegated callers. Consequently,
`grain/viewer`, `grain/contributor`, external `gma_` tokens, and a user's own task agent
are all refused `403` when trying to grant an access request — including one raised by the
user's own agent.

### Reproduction and fix landed here

- **Limited role holding `capabilities:admin` bypassing grant, network, and repo ceilings
  (`pkg/ui/task_access_request.go`):**
  Because `POST /api/tasks/{id}/access-request/grant`'s request body is `{"items": [0, 1]}`
  (indexes into `Observation.PendingAccess.Items`, with no capability names or repos in the
  HTTP body), `authorizeRoleCall` saw only that the caller held `capabilities:admin` on the
  task's target repository. `Client.GrantAccessRequest` then mutated the task in Go via
  `c.UpdateTask` and `c.SetCapabilityGrant` without consulting `accessRoleFromContext(ctx)`.
  A limited role holding `capabilities:admin` on `acme/widgets` could grant its own task's
  `request_access` for `gcp#gcp-key`, `host-script`, `api:secrets:write`, `network: open`,
  or `write: other/gadgets`.
  `Client.GrantAccessRequest` now enforces `roleTaskWithin` on the target task and validates
  every selected `AccessRequestItem` against the caller's `accessRole` (`tasks:read` for
  `AccessRead`, `tasks:write` + non-`grain/config` for `AccessWrite`, `!networkAccessWidens`
  for `AccessNetwork`, and `roleGrantRefusal` for `AccessGrant`).

---

## 4. Agent-Mediated Escalation

### What was already closed

- **`propose_task` ceilings and auto-approval:** Both live proposals (`POST /api/tasks` with
  `X-Grain-Acting-Task`) and end-of-run `propose_task` replays enforce `filingEscalation`,
  `AutoApprovalRefusal`, `ConfigRepoProposalHold`, and `ChangeTargetNeedsPersonApproval`:
  an agent cannot attach `autoMerge: true`, cannot self-approve (`approved: true` refused
  on `agentAPIGate`), and any proposal escalated past the parent's grants, repos, or
  network — even from a task with `AutoApproveProposals: true` or the `director` session —
  lands held unapproved (`Approval == nil`).
- **Self-mutation:** `selfMutableTaskRoutes` (`pkg/ui/agent_api_gate.go`) refuses an acting
  task calling `PATCH /api/tasks/{self}` to regrant its own capabilities, reads, writes,
  network, or auto-merge, and `POST /api/tasks/{self}/capabilities` requires dangerous
  `api:capabilities:admin`.
- **In-sandbox `grain api` channel:** `GitProxy.ServeAPIHTTP` (`pkg/gitproxy/namespace.go:193-261`)
  authenticates the per-attempt sandbox token, strips client-supplied `Authorization` and
  `X-Grain-*` headers, and stamps `X-Grain-Acting-Task` plus `X-Grain-Channel: sandbox-cli`.
  `authorizeAgentCall` then resolves grants via `model.SandboxAPIGrantsOf` (stripping all
  dangerous scopes, person-only sets, and model-only scopes `dispatch:write`, `repos:write`,
  `sessions:write`, `teams:broadcast`, and non-read `deployment:*`) and refuses attaching
  `capabilities` over `sandbox-cli`.

### Reproduction and fix landed here

- **In-grain agents un-parking human questions, secret requests, and access requests via
  `/comments` or `/reply` (`pkg/ui/client.go`):**
  `Client.addAttributedComment` (`pkg/ui/client.go:6756-6795`) cleared
  `Observation.PendingQuestionCommentID`, `PendingSecret`, and `PendingAccess` whenever
  *any* caller other than the parked task's own agent posted a comment — including another
  in-grain task's agent calling `POST /api/tasks/{id}/comments` or `/reply` under
  `api:tasks:write`. `addAttributedComment` now skips clearing human-parked
  `PendingQuestionCommentID` / `PendingSecret` / `PendingAccess` when `inGrainAgentTaskID(by.Actor) != ""`
  or `by.Actor.Kind == model.PrincipalAutomation`.

### Fixed by task 4019: cross-task agent prompt and execution steering

- **Cross-task agent prompt and execution steering under `api:tasks:write`:**
  Limited *human* roles were already blocked by `roleTaskWithin` from editing or steering
  higher-privilege tasks (Path 2b), but an *agent* holding non-dangerous `api:tasks:write` on a
  repository could still call `PATCH /api/tasks/{other}` (`description`/`promptExtension`),
  `retry`/`reopen`, or `comments`/`agent-messages` against another task in that repository
  carrying wider grants (`gcp#gcp-key`, `host-script`, `autoMerge: true`).
- **Fix:** `agentRails` now calls `agentCrossTaskRail` (`pkg/ui/agent_cross_task.go`) for every
  steering route (`PATCH /api/tasks/{id}`, `retry`, `reopen`, `undefer`, `resume`,
  `comments`, `reply`, `PUT goal`, `agent-messages`, `notes`). It refuses the call with `403`
  unless the target task is within the calling task's envelope (`agentTaskWithin`, the agent
  counterpart of `roleTaskWithin`): every grant the target carries (capabilities, `api:*`
  scopes, and the secrets and credentials a capability's arguments bind) must be covered by
  the caller's own grants (`agentCoversGrant`, sharing `agentHoldsGrant` with
  `agentGrantRefusal`), every extra repository it writes or reads must be one the caller writes
  or reads, its network access must not be wider than the caller's, and it must not carry
  `autoMerge` the caller lacks. A repository default grant (`GrantByDefault`) is skipped unless
  it is dangerous, as `roleTaskWithin` skips defaults. Exceptions: the caller's own task (still held by the
  self-mutation rails) and tasks in its own proposal chain, which filing and approval already
  held to the existing ceilings. Approved `run-now` stays outside the rail: it changes only
  when a task runs (`api:dispatch-control`), adds nothing to its prompt, and an unapproved
  target is already held by `agentApprovalRail`.
- **Comments to wider tasks:** plain comments, replies and `agent-messages` to an unrelated
  wider task are refused, since a comment is delivered to the live run like a person's. Messages
  between parent and child (`agent-messages`, `notes`) stay allowed so a director's children can
  report back, and when the sender is the narrower task the message is prefixed with an
  `[Untrusted agent input: ...]` notice (`untrustedAgentInputNotice`) in the recipient's
  conversation, telling the receiving run to treat it as information, not instructions.
- Pinned by `TestPath4KnownGapAgentCrossTaskPromptSteering` (now asserting the refusal) and
  `pkg/ui/agent_cross_task_test.go`.

---

## 5. Merging as Escalation

Coordinated with #3965 (`docs/security/audits/2026-11-10-insider-risk.md`) and #3904:
- **`Submit` (`POST /api/tasks/{id}/submit`):** Already refused to all limited roles (`!a.Full`)
  and all agents (`filingEscalation`), regardless of repository or PR path. (Note: #3904
  separately tracks an *unlimited* operator's `Submit` overriding an unfinished
  `system:security-review` run.)
- **Session-proposed `grain/config` changes (`POST /api/tasks/{id}/config-change/approve`):**
  Fixed in #3965 (`configChangeScopeRefusal` checks `trustboundary.StateScopes(pending.Files)`
  so `settings:write` alone cannot approve changes conferring `capabilities:admin`,
  `secrets:write`, or `connections:write`); verified in `TestPath5NonAdminMergeSubmitForceMergeAndHumanReviewRelease`.

### Reproductions and fixes landed here

1. **Releasing `HumanReviewHold` with `tasks:write` (`pkg/ui/agent_api_gate.go`,
   `pkg/ui/human_review.go`):**
   `POST /api/tasks/{id}/human-review/release` had `routeScopes` `"tasks:write"` with no
   limited-role guard in `filingEscalation` or `Client.ReleaseHumanReviewHold`. When a PR
   originally carrying `AutoMerge: true` hit a trust-boundary (`human: required`,
   `NeverAutoMergePaths`, `StatePrivilege`) or security-review hold, grain parked auto-merge
   behind `LinkAutoMergeAwaitsPerson` + `LinkHumanReviewHold`. Any `grain/contributor`
   holding `tasks:write` could call `POST /api/tasks/{id}/human-review/release`, which ran
   `restoreAfterHolds` and **automatically restored `AutoMerge: true`**, merging the
   trust-boundary PR without an operator! Both `filingEscalation` and
   `Client.ReleaseHumanReviewHold` now require an unlimited role (`grain/operator`).
2. **Force-merging with a limited role (`pkg/ui/agent_api_gate.go`, `pkg/ui/client.go`):**
   `POST /api/tasks/{id}/force-merge` checked only scope `merge:force`. A custom non-operator
   role holding `merge:force` on a single repo could bypass `Submit`, unfinished security
   reviews, and `NeverAutoMergePaths` holds even though `Submit` itself requires
   `grain/operator`. Both `filingEscalation` and `Client.ForceMergeWithGates` (which `ForceMerge` and `grain pr submit --override-gates` both reach) now require an unlimited
   role (`grain/operator`).

---

## 6. Schedules, Standing Console Sessions, and Live PTY Consoles

### Reproductions and fixes landed here

1. **Schedules (`pkg/ui/access_roles.go`):**
   - `POST /api/schedules` previously checked `tasks:write` on `repo` only when `templateId`
     was set, allowing a role with `schedules:write` on `acme/widgets` and `tasks:write` only
     on `other/gadgets` to create an inline schedule firing approved tasks in `acme/widgets`,
     and did not check `ConfigRepoProposalHold` (`grain/config`). Both are now enforced
     (without requiring `tasks:admin`, preserving the `standing-writer` and `api:release-manager`
     contracts).
   - `PATCH /api/schedules/{id}` and `POST /api/schedules/{id}/run` now also enforce
     `ConfigRepoProposalHold` and `networkAccessWidens` against the stored schedule.
2. **Standing console sessions (`pkg/ui/console_sessions.go`, `pkg/ui/access_roles.go`):**
   - `POST /api/console-sessions` (`sessions:write`): when `req.Capabilities` was `nil`,
     `Client.createConsoleSession` previously copied the `director`'s Tier 1 capability list
     (`grain#source`, `grain#journal`, `grain#health`, `api:self-debug`, `api:task-admin`,
     `api:capabilities:detach`, `api:settings:read`, etc.) plus `cfg.DefaultCapabilities`,
     defaulted `ReadsAll` to `true` (every repo), attached `grain/config` (`stateRepo`) to
     `task.Reads`, and resolved `NetworkAccess` to `NetworkOpen` on non-isolating backends.
     For limited roles (`role != nil && !role.Full`), `createConsoleSession` now filters
     implicit defaults through `roleGrantRefusal`, declines `ConsoleSessionDefaultGrants`
     (`api#ci-read`) that exceed the role, defaults `ReadsAll` to `false` unless the role
     holds unscoped `tasks:read`, defaults `NetworkAccess` to `NetworkNone` when `"open"`
     would widen past the deployment default, and omits `stateRepo` from `task.Reads` unless
     the role holds unscoped `settings:read` or `tasks:read` on `stateRepo`.
   - `PUT /api/console-sessions/{id}`, `POST /api/console-sessions/{id}/restart`,
     `POST /api/console-sessions/{id}/cancel`, and `DELETE /api/console-sessions/{id}` now
     enforce `Client.roleConsoleSessionWithin` so a limited `sessions:write` caller cannot
     reconfigure, reset, or hijack `director` or any session exceeding their role.
   - Messaging a standing console session via `POST /api/tasks/{id}/comments` requires
     `sessions:write` and checks `roleTaskWithin` against the session's envelope (for
     verified delegated requests via an authorized `grain/delegated` app, `AU-26`, the
     envelope is checked against the verified human speaker's own `personRole`, preserving
     operator Slack-to-Director workflows while blocking limited users both directly and
     through delegates).
3. **Live PTY terminal consoles (`pkg/ui/console.go`, `pkg/ui/access_roles.go`):**
   `GET /api/console/{id}/output` (`sessions:read`), `POST /api/console/{id}/input`,
   `POST /api/console/{id}/resize`, and `DELETE /api/console/{id}` (`sessions:write`)
   previously resolved neither the target task's repository nor its grants, allowing a
   `grain/viewer` to read terminal output from — or a `sessions:write` user to type shell
   commands directly into — a live interactive task holding `gcp#gcp-key` or `host-script`.
   `authorizeRoleCall` now resolves the target task's repository on `/api/console/{id}/*`,
   and `Server.console` enforces `roleTaskWithin` across all four endpoints.

---

## Regression Tests & Nightly Coverage

All regression tests live in `tests/security/task_grant_escalation_test.go` (package
`security`), which runs on every PR in `.github/workflows/tests.yml` and nightly under
`-race -shuffle=on -count=2` in `.github/workflows/nightly.yml` (`go-long`, shard 3), and
is ready to be wired into the consolidated nightly `security-attacks` job alongside #3965,
#3981, #3983, and #3984:

- `TestPath1NonAdminTaskFilingAndTemplateGrantCeilings`
- `TestPath1KnownGapPerTaskSandboxShapeUncapped` (`knownGapSandboxShapeCeiling`)
- `TestPath2NonAdminTaskEditApproveRunNowAndRetryCeilings`
- `TestPath2ExternalTokenApprovalCeiling` (was `knownGapExternalTokenApprovalCeiling`, #3905; closed by #3924)
- `TestPath3NonAdminAccessRequestGrantCeilings`
- `TestPath4AgentMediatedEscalationAndSandboxCLIBoundaries` (includes live `gitproxy.BuildProxy` + `sandboxcli.Main` `grain api` execution)
- `TestPath4KnownGapAgentCrossTaskPromptSteering` (was `knownGapAgentCrossTaskPromptSteering`; closed by task 4019)
- `TestPath5NonAdminMergeSubmitForceMergeAndHumanReviewRelease`
- `TestPath6NonAdminScheduleConsoleSessionAndLivePTYCeilings`
