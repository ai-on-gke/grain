# Operator attribution

**Status:** draft for review. Normative.

This specification turns
[`operator-attribution.design.md`](../../design/operator-attribution.design.md)
into the actor record, request plumbing, write paths, read surfaces and
tests that make every human-initiated write in grain name the person who
made it. It builds on
[`authentication.design.md`](../../design/authentication.design.md),
which establishes who a request is from. This document only says how
that identity is recorded and shown. Where this specification and the
design disagree, this document wins. Where either disagrees with the code
on `main`, §3 marks the gap.

The key words "MUST", "MUST NOT", "SHOULD", "SHOULD NOT" and "MAY" are
to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and
only when, they appear in all capitals.

## Contents

1. The actor record
2. Request plumbing
3. Write-path inventory
4. Read surfaces
5. `GET /api/whoami`
6. Security
7. Migration and tests
8. The Google layer

## 1. The actor record {#OA-1}

### 1.1 Type

1.1.1. `model.Principal` (`pkg/model/task.go`) gains one field:

```go
type Principal struct {
	Kind   PrincipalKind
	ID     string
	Source AuthSource // how ID was established; "" on rows written before this spec
}
```

`model.Attribution` (`Actor`, `OnBehalfOf *Principal`) is unchanged.
It is the actor record wherever a write has an on-behalf-of. A write with
no on-behalf-of MAY keep storing a bare `Principal`.

1.1.2. **Kinds.** The stored `PrincipalKind` vocabulary stays `human`,
`agent` and `automation`, plus `service`, which `authentication.design.md`
§3 adds. Readers already switch on that column (`LandsQueued`,
`identityTrailers`, `state.js`), so this spec adds no kinds. The
distinctions the design asks for (session, API grant, external agent,
operator fallback) are (kind, source) pairs:

| Who acted | `Kind` | `Source` | `ID` |
|---|---|---|---|
| A person verified by IAP | `human` | `iap` | the `email` claim, lowercased |
| A person verified by a registered verifier | `human` | the verifier's `Name()`, e.g. `example-proxy` | `VerifiedIdentity.Email`, lowercased |
| A person with a per-user API token (future) | `human` | `token` | the token's owner |
| A Google service account behind IAP | `service` | `iap` / verifier name | the SA email |
| The operator fallback (nobody named) | `human` | `access-code`, `loopback` or `in-process` | `Config.Actor.ID` (`operator` by default) |
| A console session (director, …) using its grants | `agent` | `session` | the session's task id |
| A dispatched run using an API grant | `agent` | `sandbox` | the task id (`agentCall.principalID`) |
| An external agent token (`gma_…`) | `agent` | `external-token` | `external:<name>` |
| Grain itself | `automation` | `internal` | the deployment name |

1.1.3. **`AuthSource`** is a new string type in `pkg/model` with the
constants above: `iap`, `token`, `access-code`, `loopback`,
`in-process`, `session`, `sandbox`, `external-token` and `internal`. A
verifier's `Name()` is also a valid source. `Source` MUST NOT be empty
on any row written after this spec ships. A writer that has no source
passes `internal` for automation, and otherwise the request's source
(§2).

1.1.4. **Verified.** `Principal.Verified()` is true when
`Kind ∈ {human, service}` and `Source` is `iap`, `token` or a registered
verifier's name. Only a verified principal may be shown as a person (§4)
or written into a `Requested-by:` / `Approved-by:` trailer (§4.6).

### 1.2 Wire form

1.2.1. The API keeps its flat string fields (`configResponse`'s reasoning
in `pkg/ui/tasks.go`: `model.Principal` carries no wire tags). Each
existing `<p>`/`<p>Kind` pair gains `<p>Source` and `<p>Label`. For
example, `author`, `authorKind`, `authorSource`, `authorLabel` on a task
and a comment, and `actor`, `actorKind`, `actorSource`, `actorLabel` on a
transition. A field for an on-behalf-of (`onBehalfOf` on `Comment`)
gains `onBehalfOfKind` and `onBehalfOfLabel`.

1.2.2. **Label.** `<p>Label` is computed server-side by
`ui.PrincipalLabel(p, behalf)`, so the UI, the CLI and MCP all say the
same thing:

| Principal | Label |
|---|---|
| verified human or service | `alice@example.com` |
| operator fallback, `access-code` | `operator (access code)` |
| operator fallback, `loopback` / `in-process` | `operator (local)` |
| legacy row: `human`, empty source, ID = `Config.Actor.ID` | `operator (access code)` |
| legacy row: `human`, empty source, any other ID | `<id> (unverified)` |
| agent, `session` | the session title, e.g. `director` |
| agent, `sandbox` | `task <id>` |
| agent, `external-token` | `external agent <name>` |
| automation | `grain` |
| any of the above with a verified on-behalf-of | `<label> for <on-behalf-of label>` |

A display-name map (design, *Open questions*) would only change this
function.

### 1.3 Storage

1.3.1. Every table that stores a principal as a `<p>_kind`/`<p>_id` pair
gains `<p>_source TEXT NOT NULL DEFAULT ''`. On `main` today these are
`task` (`origin_actor_*`, `origin_behalf_*`, `approval_actor_*`,
`approval_behalf_*`), `task_comment` (`author_*`, `author_behalf_*`),
`task_pause` (`paused_by_*`), `pipeline_pause` and `scheduled_upgrade`
(`actor_*`), `memory`, `memory_document` and `release_note`
(`updated_by_*`), and the auto-submit store's actor. `task_event` (task
1986, PR #242) gains `actor_source`, `behalf_kind`, `behalf_id` and
`behalf_source`. The implementation MUST find the full set by grepping
`pkg/model` for `_kind` columns, not from this list.

1.3.2. Columns that hold a principal as one free string (`Task.Creator`,
`base_branch_ci.run_requested_by`, `ci_run.cancelled_by`, the
`principal` of `SecretAuditEntry` and `TeamAuditEvent`, and the `user`
arguments to the `*WithUser` team and key methods) keep their shape. They
store the principal's `ID` and nothing else. A verified person's email
replaces `operator` there.

1.3.3. Adding a defaulted column needs a `SchemaVersion` bump and a
migration rung for the tables that already exist, in both the SQLite and
the Postgres adapters. It also needs the `pkg/staterepo` table and
fidelity lists, since the state repo exports these rows as text.

1.3.4. **No rewrite.** Existing rows are never updated. They read back
with `Source == ""` and render as §1.2.2 says. In particular, every
`{human, operator}` row renders as "operator (access code)".

## 2. Request plumbing {#OA-2}

2.1. **One place sets the principal.** `Server.ServeHTTP`
(`pkg/ui/server.go`) already attaches a principal and an `AuthMethod` to
the request context through `WithPrincipal` and `withAuthMethod`, after
`singleTenantGate`. It MUST attach a principal to every request it lets
through, with its `Source` set:

1. external token (`gma_…`) → `{agent, external:<name>, external-token}`;
2. agent API gate (`agentAPIGate`, `pkg/ui/agent_api_gate.go`) →
   `{agent, <task>, sandbox}`, or `session` when the acting task is a
   console session (`ReasonConsoleSession`);
3. a verified identity from `identifyCaller` (IAP, or the first
   registered `IdentityVerifier` whose header is present) →
   `{human|service, <email>, <verifier name>}`;
4. otherwise, a request `singleTenantTrusted` admits →
   `Config.Actor` with `Source` `access-code` (it presented the code or
   an access session), `loopback` (break-glass over loopback,
   `-ui-require-iap`), or `in-process` (no `RemoteAddr`).

`extractPrincipal` MUST stop dropping verified humans
(`authentication.design.md` rollout step 2). This spec depends on that
step.

2.2. **One helper reads it.** `ui.RequestActor(ctx) model.Attribution`
replaces `Client.actor(ctx)` (`pkg/ui/client.go`). It returns the context's
principal and, for an agent, the on-behalf-of of §2.4. With nothing on
the context (a non-HTTP caller such as `cmd/grain`'s in-process CLI) it
returns `Config.Actor` with `Source: in-process`. Every Client method that
writes a principal MUST take it from `RequestActor`. The following MUST NOT
be read anywhere else in `pkg/ui`: `c.Config.Actor`, `requestUser`,
`actingUser` and `teamActingUser`. Those become thin wrappers that return
`RequestActor(ctx).Actor.ID`, and a vet-style test (§7.3) enforces it.

2.3. **Request bodies never name a human.** A body field that names an
author (`CreateTaskRequest.Author` / `Creator`, `CommentRequest.Author`,
`BroadcastTeamRequest.Author`, `requestPrincipal`'s kind and id) MAY
still name an *agent* through `withAgentRequester`'s rules. It MUST be
ignored, or refused with a 400, when it names a human or a service. A
caller that wants to file "as" a person has to be that person. The one
exception is the in-process CLI (`grain create -author`, no
`RemoteAddr`). It MAY set `Task.Creator`, which renders as unverified.

2.4. **On-behalf-of** is set by grain, never by the caller:

- *Console session.* When the acting task is a console session, the
  on-behalf-of is the verified human who wrote the most recent human
  message in that session's conversation (`task_comment`, author kind
  `human`) before the turn that made the call. The session's turn
  bookkeeping is the source; no header or body field is read. If that
  message's author is not verified, there is no on-behalf-of.
- *Dispatched run.* The on-behalf-of is the verified human who approved
  the task (`Task.Approval.Actor`). If grain approved it, there is none.
  This is what a run's writes are "for".
- *External agent token.* The on-behalf-of is the verified human who
  minted the token, recorded on the token row when it is created
  (`CreateExternalAgentToken`).

2.5. **No verifier configured.** With no IAP audience and no registered
verifier, step 3 of §2.1 never fires. Every human write is
`Config.Actor` with source `access-code`, `loopback` or `in-process`,
exactly as today plus the source. `GET /api/whoami` reports it (§5).
Nothing else changes.

## 3. Write-path inventory {#OA-3}

This is every Client method a person reaches through the UI, the API or
the CLI that writes state, grouped by surface. "Today" says what it
records on `main` at this spec's base: **actor** means it already calls
`Client.actor` (so it will record the person once §2 lands), **coarse**
means it records `Config.Actor` or a string derived from it, and
**none** means it records no actor. Every row MUST end up recording
`RequestActor(ctx)`. The **Target** column says where, when "none"
needs a new field or row.

### 3.1 Tasks (`pkg/ui/client.go` unless noted)

| Action | Function | Today | Target |
|---|---|---|---|
| File a task | `CreateTask` → `Task.Origin` (`origin_actor_*`), `Creator` | actor (origin); coarse (`Creator` falls back to `Config.Actor.ID`, ~L2300) | `origin_actor_*`; `Creator` = verified ID |
| Edit title / description | `UpdateTask` → `noteEdit` | actor (the edit note's author) | — |
| Edit other fields (priority, grants, base, goal …) | `UpdateTask` | none | an edit note, as `noteEdit` writes |
| Attach / detach a capability | `SetCapability`, `SetCapabilityGrant` (`capability_grants.go`) | none (refusal checks only, `catalogueEditRefusal`) | a task note naming the grant and actor |
| Depends-on | `SetDependency` | none | a task note |
| Reorder | `Reorder` | none | a note per task moved, or one pipeline event |
| Approve | `Approve` → `approval_actor_*` | actor | — |
| Withdraw approval | `POST /api/tasks/{id}/withdraw-approval` | none | a note |
| Submit to merge queue | `Submit` | none | a note; `Approved-by`/submitter |
| Retry | `Retry` | none | a note |
| Run now | `RunNow` | actor | — |
| Stop | `Stop` | actor | — |
| Pause / resume | `Pause`, `Resume` (`taskpause.go`) | actor | — |
| Defer / undefer / park-until / wait-on | the handlers for those routes | none | a note |
| Close / reopen | `Close`, `Reopen` | none on `main`; `TaskEvent.By` (coarse) on PR #242 | `task_event` with source and on-behalf-of |
| Goal set / clear | `SetGoal`, `ClearGoal` (`taskgoal.go`) | actor | — |
| Pending secret | `SetPendingSecret` (`task_secret.go`) | none | a note (value never logged) |
| Retry merge / force merge | `RetryMerge`, `ForceMerge` | actor | — |
| Change review: publish / mail | `PublishReplies`, `MailChange` (`change_review.go`) | actor, plus a `by` string | — |
| Change submit | `personActor` (`change_submit.go`) | actor | requester is §4.6 |

### 3.2 Conversation

| Action | Function | Today | Target |
|---|---|---|---|
| Comment / reply on a task | `commentWithAuthor` | actor (`author_*`) | — |
| Message a session (chat) | `POST /api/tasks/{id}/messages` → `commentWithAuthor` | actor | — (it is §2.4's source) |
| Agent messages | `POST /api/tasks/{id}/agent-messages` | agent | add the on-behalf-of |
| Notification raise / ack / clear / investigate | `RaiseNotification`, `AckNotification`, `ClearNotification`, `InvestigateNotification` (`notifications.go`) | actor | — |
| Team broadcast | `BroadcastTeamRequest` (`teams.go`) | coarse (`Author` or the actor ID) | §2.3 |

### 3.3 Releases, CI and the merge queue

| Action | Function | Today | Target |
|---|---|---|---|
| Release approve / reject | `DecideRelease` (`release_approval.go`) | actor | — |
| Cut / promote a candidate | `CutCandidate`, `PromoteCandidate` (`releases.go`) | none | a release-event record |
| Delete a release | `DeleteRelease` | none | the same |
| Start a release workflow | `StartRepoRelease` (`repo_release_workflows.go`) | actor | — |
| Approve a qualification run | `ApproveQualificationRun` (`qualifications.go`) | actor | — |
| Base-branch CI run | `RequestBaseBranchRun` (`base_branch_runs.go`) → `run_requested_by` | actor (as a string) | — |
| CI run / re-run / cancel | `POST /api/ci/runs`, the cancel route → `ci_run.cancelled_by` | coarse (`"operator"`, `CancelByOperator`) | the principal's ID |
| Pipeline pause | `pipelinepause.go` | actor | — |
| Scheduled upgrade | `upgrade.go` | actor | — |

### 3.4 Settings, configuration and grants

| Action | Function | Today | Target |
|---|---|---|---|
| Deployment settings | `UpdateSettings` (`settings.go`) | none | a config-audit row (§4.4) |
| Config PRs | `OpenConfigPullRequest` (`config_pull_requests.go`) | actor | `Requested-by:` on that PR |
| Repo settings | `SetRepo*` (`repos.go`, `repo_release_workflows.go`, `repo_work_ownership.go`, `repo_workflows.go`) | none (`checkRepoPerson` reads the actor for a refusal only) | config-audit row |
| Release secrets | `SetRepoReleaseSecret`, `DeleteRepoReleaseSecret` | none | config-audit row |
| Schedules | `UpdateSchedule`, `DeleteSchedule`, `RunScheduleNow` (`schedules.go`) | none | config-audit row; the run's task `origin` gets the actor as on-behalf-of |
| Templates, suites | `UpdateTemplate`, `DeleteTemplate` (`templates.go`); `UpdateSuite`, `DeleteSuite` (`suites.go`) | none | config-audit row |
| Boards | `UpdateBoard`, `DeleteBoard` (`boards.go`) | none | config-audit row |
| Capability policy, servers | `SetCapabilityPolicy`, `SetCapabilityServer`, `SetCapabilityServerDisabled` | none | config-audit row |
| API scope sets | `DeleteAPIScopeSet` and its setter (`api_scope_sets.go`) | none | config-audit row |
| CLI-run and CLI-path settings | `UpdateCLIRunSettings` → `auditCLIRunChanges`; `auditCLIPathChanges` | actor (logged) | config-audit row |
| Memory, release notes | `memoryAuthorOrActor` (`memory.go`), `releasenotes.go` | actor (`updated_by_*`) | — |
| Team, git connections, agent backends, GCP keys | `UpdateTeam`, `SetDeploymentRepoConfigWithUser` (`teams.go`), `team_git_connections.go`, `team_agent_backends.go`, `gcp_keys.go` | coarse (`user` string from `actingUser`, falling back to `Config.Actor.ID`) | the principal's ID |
| External agent tokens | `CreateExternalAgentToken`, `RotateExternalAgentToken`, `Revoke*`, `DeleteExternalAgentToken` (`external_agents.go`) | actor on create/rotate; none on revoke/delete | the token row's `created_by` (§2.4); config-audit row |
| Executor / hostd tokens | `RevokeExecutorToken` (`executors.go`) | none | config-audit row |

### 3.5 Sessions

| Action | Function | Today | Target |
|---|---|---|---|
| Create | `createConsoleSession` (`console_sessions.go`) | actor | — |
| Update / delete | `UpdateConsoleSession`, `DeleteConsoleSession` | none | a note on the session task, or a config-audit row for a deleted one |
| Rename | `RenameConsoleSessionOfTask` (`session_title.go`) | none | a note |

### 3.6 Agent API

Every call through `agentAPIGate` is already attributed to the acting
task (`{agent, <task>}`, `agentCall.principalID`). It MUST also carry the
on-behalf-of of §2.4. That reaches every write in §3.1–§3.5 a grant
allows, through `RequestActor`. The MCP tools in `pkg/mcp` that call the
daemon over HTTP need no change: their principal is set by the gate, not
by them.

## 4. Read surfaces {#OA-4}

4.1. **`GET /api/tasks/{id}`.** `author*` (§1.2), each comment's
`author*` and `onBehalfOf*`, `approval`'s actor, and every entry of
`transitions` that has an actor (task 1986's close and reopen events)
carry `Source` and `Label`. The UI renders `Label` and nothing else.
That covers "Filed by" (`Declared.jsx`, `taskAttributes.js`), the comment
header, and the `Timeline` steps.

4.2. **`/api/pipeline/changes`** (`pipeline_changes.go`). Every change
that has an actor (task 1986 adds close and reopen) carries `actor`,
`actorKind`, `actorSource` and `actorLabel`.

4.3. **`grain task read`** (`pkg/cli/taskprint.go`) and the MCP task
views (`pkg/mcp/task_views.go`) print `Label`, for example
`Filed by: alice@example.com`, `Closed by: operator (access code)` and
`By: director for alice@example.com`. They never print the raw
`{human, operator}` tuple.

4.4. **Audit log.** The config-audit rows that §3.4 requires go in a new
`config_audit` table: `at`, `actor_kind`/`id`/`source`, `behalf_*`,
`surface` (`settings`, `repo:<owner/name>`, `schedule:<id>`, …) and a
`summary` of what changed, with no secret values. They are shown on
Settings → Audit, next to the existing team audit
(`ListTeamAuditEvents`) and secret audit (`ListSecretAudit`), whose
`principal` strings become the principal's ID (§1.3.2). The daemon log
line for each write (`auditCLIRunChanges` and the like) names
`Label`.

4.5. **Notifications.** A notification raised by a person
(`RaiseNotification`) names its actor's label as the sender. The inbox
shows "acknowledged by" and "cleared by" from the same record.

4.6. **Commits and changes.** `identityTrailers`
(`pkg/orchestrator/worktree.go`) writes `Requested-by: <Creator>` and
`Approved-by: <approval actor ID>`. Both MUST be written only for a
verified principal (§1.1.4), and the value MUST be the email. For the
operator fallback, the trailer keeps today's `operator`, so this
change does not stop the trailers being written. The grainfile
`requestor` field of an exported task takes the same value.
`attribution.design.md` may use it as a `Co-authored-by:` candidate,
but that is its decision. A change backend that has a requester of its
own (a CL in the Google layer, through `pkg/change`)
takes it from `Task.Creator` when that is verified.

## 5. `GET /api/whoami` {#OA-5}

5.1. Request: any method the access gate admits. No body.

5.2. Response, `200`:

```json
{
  "actor": {"kind": "human", "id": "alice@example.com", "source": "iap",
            "label": "alice@example.com"},
  "onBehalfOf": null,
  "source": "iap",
  "verified": true,
  "allowed": true
}
```

- `actor` is `RequestActor(ctx).Actor` in §1.2's form, and `onBehalfOf`
  is the same for its on-behalf-of.
- `source` duplicates `actor.source` for the UI's convenience.
- `verified` is §1.1.4.
- `allowed` is whether the caller passed the allowlist
  (`authentication.design.md` §2). It is always `true` in a `200`,
  because a caller that is not allowed gets the gate's `403` first. It is
  in the body so that a future read-only role can report `false` here
  rather than change the shape.

A request the gate refuses gets the gate's own 401/403, not this body.

5.3. `GET /api/auth/user` (`pkg/ui/auth.go`) stays for the installer and
older frontends. Its `user` and `authMethod` are derived from the same
`RequestActor` call.

5.4. **UI.** A user tile returns to the sidebar footer (the one the
current `Sidebar.test.jsx` asserts is absent). It shows `actor.label`
and, for an unverified source, a muted "(access code)" or "(local)".
Clicking it opens Settings → Access, which (`AccessSection.jsx`) reads
`whoami` instead of `config.actor`. The free-text username dialog stays
gone, and `grain.username` stays retired (`storageMigration.js`). The
frontend MUST NOT send `X-Grain-User` (`api.test.js` already asserts
this).

## 6. Security {#OA-6}

6.1. **No unsigned identity.** Nothing in `PlaintextIdentityHeaders`, no
request-body author field (§2.3), and no cookie other than one the gate
itself verified MAY produce a `human` or `service` principal. They are
logged as `logIgnoredIdentityHeader` already does.

6.2. **Agents cannot become people.** A request admitted as an agent
(the agent API gate, the sandbox proxy at `127.0.0.1:9728`, `grain
mcpserver` with `X-Grain-Acting-Task`, an external token) MUST NOT
yield a human `Actor`, whatever headers it carries. The sandbox acceptor
MUST NOT consult the identity verifiers at all
(`authentication.design.md`, *In-sandbox calls do not change*). An
agent's on-behalf-of comes only from §2.4's records, and a body or header
naming one is refused (`withAgentRequester`'s rule, extended to
`OnBehalfOf`).

6.3. **The fallback stays visible.** The access code is shared, so an
access-code write MUST NOT be attributed to a person, even when the
deployment has exactly one. If a request carries both a verified
assertion and the access code, the verified principal wins
(`authentication.design.md` §1).

6.4. **Logging.** Every refused or downgraded identity is logged once
per header, as today. Every write logs `Label` and the source. Email
addresses are personal data: they appear in the DB, in the API to
callers the gate admits, and in commit trailers on repositories this
deployment writes to. They MUST NOT appear in anything grain publishes
outside those, such as public CI logs or notification bodies sent to
another system.

## 7. Migration and tests {#OA-7}

7.1. **Migration.** One schema bump adds the `*_source` columns (§1.3)
and `config_audit`. No row is rewritten. The state repo's export gains
the columns, and an old export (with no source columns) imports with
empty sources.

7.2. **The test matrix.** For each write path in §3, a table-driven test
in `pkg/ui` issues the same request five ways and checks the recorded
principal:

| Request | Expected `Actor` | Expected on-behalf-of |
|---|---|---|
| a valid IAP assertion for `alice@example.com` | `{human, alice@example.com, iap}` | — |
| a fake `IdentityVerifier` (`Name() = "fake"`) for `bob@example.com` | `{human, bob@example.com, fake}` | — |
| the access code, no assertion | `{human, operator, access-code}` | — |
| the agent API gate, a console session whose last message is alice's | `{agent, <session>, session}` | `{human, alice@example.com, iap}` |
| the access code plus `X-Goog-Authenticated-User-Email: mallory@…` and `X-Grain-User: mallory` | `{human, operator, access-code}` | — |

The fake verifier test also pins the contract the Google layer relies
on (`authentication.design.md`, *IdentityVerifier*).

7.3. **Nothing can forget.** `TestEveryMutatingRouteIsAttributed` walks
the server's route table. Every non-GET route MUST be either covered by
§7.2's matrix or on an explicit, commented exemption list (for example,
`/api/auth/logout`). A new route that is on neither fails. A
source-level test fails on any reference to `Config.Actor`,
`requestUser`, `actingUser` or `teamActingUser` outside
`RequestActor`'s file.

7.4. **Rendering.** `PrincipalLabel` has one table test for every row of
§1.2.2, legacy rows included. UI unit tests cover the user tile, Filed
by, a comment, a timeline close and a pipeline change, each in a verified
case and an operator case. A Playwright spec over `grain demo` checks
that the sidebar tile shows the `whoami` label.

7.5. **Trailers.** `identityTrailers` tests cover a verified creator and
approver (email in the trailers), the operator fallback (`operator`, as
today), and an unverified `grain create -author` creator (no
`Requested-by:`).

## 8. The Google layer {#OA-8}

8.1. The Google layer MUST register its signed-proxy verifier with
`ui.RegisterIdentityVerifier` at start-up. Its `Name()` is the source
this spec records (written `example-proxy` in this tree's fixtures), and
its `Headers()` name the proxy's signed header. `Verify` returns
`VerifiedIdentity{Email: "<username>@<domain>", Kind: PrincipalHuman}`
only after checking that header's signature. Extracting the username is
another task's work. This spec only consumes it.

8.2. Nothing else in the Google layer has to change for attribution to
flow. §2 records the principal and §4 renders it. Its change backend
(`pkg/change` registration) SHOULD take the CL requester from a verified
`Task.Creator` (§4.6), and its design doc's "Attribution" section
SHOULD point at this spec.

8.3. This spec needs two things from the Google layer: the stable email
form above (lowercased, the full email, never the bare username), so that §1's
IDs compare equal across the IAP and signed-proxy paths; and a test in
that tree that drives one request through its registered verifier and
sees `{human, <username>@<domain>, <verifier name>}` recorded, mirroring
§7.2's fake-verifier row.
