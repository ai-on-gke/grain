# Insider-risk review: indirect paths to dangerous grants (2026-11-10)

Task 3965. The question: can somebody who does not hold `capabilities:admin`
(or `secrets:write`) still get the effect of one -- a host script, a widened
scope set, a GCP key on their work -- by going round the route that asks for
it? The direct route stays refused: `PUT /api/capability-servers` and the
rest of the capability-server routes need `capabilities:admin`, a dangerous
scope only a person holds.

Two insider profiles were evaluated throughout:

- **A** -- a signed-in grain user whose role is limited: any scope set
  short of every scope (`accessRole.Full == false`), for example one
  holding `tasks:write` and `settings:write` but not `capabilities:admin`
  or `secrets:*`.
- **B** -- somebody with write access to a repository grain works in (or
  to an external config repository), and no grain access at all.

Severity is for this deployment's single-tenant model, where the owner
holding the access code is an unlimited session.

## Summary

| # | Path | Profile | Status | Severity |
|---|------|---------|--------|----------|
| 1a | Submit / `autoMerge: true` on a grain/config pull request | A | Already closed; regression test added | -- |
| 1b | Approving a session-proposed config change (`config-change/approve`) with `settings:write` alone | A | **Fixed here** | High |
| 1c | Repointing grain/config at an external remote (`POST /api/state-repo/remote`) with `settings:write` | A, then B | **Fixed here** | Critical |
| 1d | Write access to an *external* config repository | B | Open by design; follow-up filed | High |
| 2a | Repo-declared schedule fires approved tasks carrying gcp-key, host-script, github-credential or dangerous `api:*` grants | B | **Fixed here** (firings held for a person) | High |
| 2b | Repo-declared suites, CI workflow `capabilities:`, network/sizing on declarations, no per-declaration acceptance | B | Fixed (task 3971) | Medium |
| 3 | Host-privileged code and release images from a forge-side merge | B | Open; follow-up filed | High (bounded by forge branch protection) |
| 4 | Agent widening its own or its children's grants | agent | Closed (verified) | -- |
| 5 | Attribution gaps (anonymous access code, shared git credential) | A, B | Tracked by #3873, #3239 | Medium |

## 1. The config repository (grain/config)

What merges into grain/config's main is imported on the next tick by
`staterepo.Apply`: `grain_config` (with `mcp_servers` -- host-script
scripts, gcp admins and role ceilings), `capability_policy`,
`default_capabilities`, `api_scope_set` (the roles), templates, schedules,
suites and standing sandboxes. `trustboundary.ClassifyState` puts every
changed path under `StatePrivilege` (`RequiresHuman: true`), and
`NeverAutoMergePaths` keeps the privilege-bearing ones there whatever
`settings/config-policy.yml` says. So the question was what a person's
release of that hold requires.

### 1a. Submit and auto-merge: closed

`POST /api/tasks/{id}/submit` maps to `tasks:write`, but
`authorizeRoleCall` → `filingEscalation` refuses it outright for any
limited role ("letting a change merge unreviewed requires an unlimited
role"), and refuses `autoMerge: true` in `PATCH /api/tasks/{id}` the same
way. The MCP endpoints route every tool call through the same
`authorizeRoleCall`. A profile-A person cannot Submit any change, a
grain/config one adding a host script included.

There was no test pinning this, so one was added:
`pkg/ui/config_change_scopes_internal_test.go`
`TestALimitedRoleCannotSubmitAConfigChange`.

### 1b. Config-change approval: fixed

A session's `propose_config_change` parks its task on a pending change, and
`POST /api/tasks/{id}/config-change/approve` merges it into grain/config
and applies it at once (`ApproveConfigChange` → `MergeConfigChange` →
`StateRepo.Sync`). For a role that route needed `settings:write`
(`humanRouteScopes`) and nothing more, whatever the change did.

**Reproduction (before):** a role holding `settings:write` only; a session
with grain/config in its writes proposes a branch editing
`tables/grain_config.json` to add a host-script script to `mcp_servers`;
the person approves → merged and applied → the script exists, with no
`capabilities:admin` anywhere in the chain.

**Fix:** `trustboundary.StateScopes(paths)` says what a change to each
grain/config path confers once applied, as the API scopes the direct route
needs:

- config row, repo config, scope sets, capability catalogue, config
  policy, templates, schedules, suites, sessions, sandboxes, extracted
  `scripts/**`, `workflows/**`, `.github/**`, `.grain/**`, the schema
  version, and any unknown path outside `settings/` and `tables/`
  → `capabilities:admin`;
- git credentials, sandbox secret/env bindings and GCP keys →
  `secrets:write`;
- agent backends → `connections:write`.

`pendingConfigChangeFor(…, "approve")` now refuses (403) a limited role
missing any of them and names the scopes. The mapping is by path, so it is
deliberately coarse: an edit to an unrelated field of
`tables/grain_config.json` also needs `capabilities:admin`. Narrowing that
to the columns that changed is a follow-up.

**Test:** `TestApprovingAConfigChangeNeedsTheScopesItConfers` -- a
`settings:write` role is refused a host-script / scope-set / template /
script change, a `capabilities:admin` role is refused a credential change
without `secrets:write`, and plain settings and unlimited sessions pass.

**Override:** none yet. An explicit, recorded override belongs to #3904's
design and should route through there.

### 1c. Repointing the config repository: fixed

`POST /api/state-repo/remote` (`settings:write`) set grain/config's remote,
branch and token. With a non-mirror remote, `StateManager` pulls from it
(`Repo.PullExternal`) and `staterepo.Apply` imports whatever is on that
branch -- no grain pull request, no Submit. So a profile-A person with
`settings:write` could point the deployment at a repository they (or a
profile-B accomplice) control and push host scripts, scope sets and
secret bindings into it.

**Fix:** the route is in `operatorOnlyRoutes`, so only an unlimited role
reaches it. **Test:** `TestRepointingTheConfigRepoNeedsAnUnlimitedRole`.

### 1d. Write access to an external config repository: open (follow-up)

Where an operator has deliberately configured an external config
repository, anybody with write access to its branch there (profile B) is
a full administrator of the deployment: grain applies what lands, and a
forge-side merge never passes through grain's Submit. This is by design
(docs/external-config-repo.md), but it is not said anywhere an operator
would read it. Grain also does not check the applied commit against
anything -- signed commits, a required review, or a merge grain itself
made. Filed as a follow-up: document it, and offer a `require_verified`
mode that applies only commits grain merged or commits whose forge review
it can verify.

## 2. Repo-declared grants (.grain/templates, .grain/schedules)

A repository's `.grain/` declarations are read from its default branch by
the declaration sync (`model.SyncRepoDeclarations`) once an operator has
turned declarations on for that repository. Template and schedule
`capabilities:` / `grants:`, `reads:`, `writes:` and `push:` are written to
their rows unchanged. `parseCapabilities` checks the syntax and that the
repositories are allowed, but not whether a grant is one only a person may
give.

What a profile-B writer can do with that:

- **`push:`**: held (`TemplatePush`, `RequiresHuman`) when the change
  comes through a grain pull request. A forge-side merge skips that hold,
  but the push set is applied by the git proxy only to that template's
  tasks, which still have to be filed.
- **Templates filed by a person**: the filing form shows the template's
  grants, and the person filing is the one granting them.
- **Templates named by an agent**: `TemplateWithinTask` refuses any grant
  the agent does not hold, or that only a person may give.
- **Reviews**: `ReviewGrants` drops person-only grants.
- **Schedules -- the gap.** `ComposeScheduleTask` files every firing with
  `Approved: true`, carrying the schedule's (or its template's) grants. The
  dispatch-time capability catalogue (`catalogueHold`) holds a task only
  for a grant the catalogue *refuses*, not for one it says needs a person,
  and with no catalogue configured everything passes. So a schedule file
  merged on the forge with `gcp#gcp-key {"permission_set":"..."}`, a
  `host-script#…` grant or `api:capabilities:admin` fired as approved work
  holding that grant. The downstream edition's image-publishing schedule,
  which declares a `gcp#gcp-key` grant with an artifact-registry writer
  permission set, is the live example of the shape.

**Fix (2a):** `model.DeclaredFiringHold` -- for a firing from a
repo-declared schedule or template, every grant that is dangerous or
person-only (`IsDangerousGrant`, `IsPersonOnlyGrant`), or that names the
`gcp`, `host-script` or `github-credential` server
(`DeclaredGrantNeedsPerson`), and any admission above standard -- makes
`composeScheduleTask` file the task unapproved, with a note in its body
saying which grant is waiting for a person. Operator-written schedules are
unchanged: they are a person's standing approval already. None of this
repository's own `.grain/` declarations grant anything the rule holds
(their grants are `api:ci-read`, `api:self-debug`, `api:*:read` and
`grain-ci`), so their nightly firings still land queued.

**Tests:** `pkg/model/declared_grant_hold_test.go` -- a declared schedule
granting a GCP key, a host script or `api:capabilities:admin` files
unapproved and says why; a declared `api:ci-read` and an operator's GCP-key
schedule still file approved; `TestDeclaredGrantNeedsPerson`.

**Fix (2b, task 3971):** per-declaration acceptance replaces holding every
firing. `model.DeclarationAcceptance` (table `declaration_acceptance`)
records, per declaration, what it last asked for that only a person may
give (`DeclaredCompositionHold`: each `DeclaredGrantNeedsPerson` grant, an
admission above standard, `network_access: open`) and what a person last
accepted: who, the scopes the set needs (`DeclaredItemScopes`), and the
declaration's commit. A firing whose items are all within the accepted set
lands approved; one that widened files unapproved, says it widened, and is
listed as awaiting acceptance on Settings → Capabilities → Repository
declarations (`GET /api/declarations`, `POST /api/declarations/accept`).
Accepting takes a person acting directly (no agent, no delegating app)
whose role holds `capabilities:admin` for a capability server's grant and
the scope itself for a dangerous `api:*` grant (`capabilities:admin` does
not stand in for `secrets:write`). The accept call carries the set the
person was shown, and a set that moved in the meantime is refused.

- **Schedules and templates:** keyed by the declared schedule's id, or by
  its declared template's when an operator's schedule fires one.
- **Suites:** a declared suite with `require_approval: false` is accepted as
  one set: every item's held items plus `require_approval: false` itself.
  Until it is accepted, its tasks file unapproved, as if
  `require_approval` were true. A declared template in an operator's suite
  is accepted under the template. `require_approval: false` with nothing
  else held is not held: it is what every schedule firing already is.
- **CI jobs:** `ciDeclarationRefusal` (`pkg/daemon/ci_policy.go`, called
  from `CiGrantReservations.Acquire`) refuses a job whose `capabilities:`
  name a held grant nobody accepted for that job
  (`CIJobDeclarationID`: repository plus job name), before anything is
  minted, and records the request. grain's builtin jobs are not asked.
  Workflows written in grain/config (`repo_workflow`) are asked too, since
  a job carries nothing saying which file it came from.
- **Catalogue `RequiresPerson`:** `catalogueHold` now also holds a queued
  task whose catalogue verdict names grants as a person's decision when
  nobody stands behind its approval (`Store.PersonStandsBehind`: a person
  approved it, an operator's schedule fired it, or a declared schedule or
  template fired it within its accepted set). The approval is withdrawn,
  so the task goes back to the proposals for a person's Approve, with the
  reason on its row.

**Decided, not held:**

- **Sandbox sizing:** templates and schedules cannot declare it (only a
  filing or a proposal can), and a CI job's `sandbox:` size is bounded by
  host capacity like every other job's. Nothing to hold.
- **Standing-sandbox secrets:** standing sandboxes are defined only in
  grain/config, never in a repository's `.grain/`, and a config change
  touching `standing_sandbox_secret` already needs `secrets:write` (1b).
- **`auto_approve_proposals`:** unchanged, for the reason given under 2a.

**Coordination with #3904:** an acceptance is a recorded, attributed grant
that names the scopes its acceptor held, which is the shape #3904's
explicit-override record should take. If that design adds a general
override record, acceptance should be written as one.

**Tests:** `pkg/model/declared_grant_hold_test.go`. An accepted declared
schedule fires approved, and widening it (a host script beside the GCP key)
holds it again as awaiting. A declared template is accepted under its own
id. A declared auto-approved suite is held until accepted. A CI job's GCP
key needs acceptance. `PersonStandsBehind` behaves as described. Also
`TestCatalogueHoldHoldsARequiresPersonGrantNobodyApproved` (orchestrator),
`TestAcceptingADeclarationNeedsTheScopesItsGrantsNeed` (ui) and
`TestACIJobsUnacceptedDeclarationRefusesIt` (daemon).

## 3. Host-privileged code from repositories

`trustboundary.Categories` classifies `scripts/setup.sh`, `pkg/runhost`,
`Dockerfile*` / `Granulefile*`, `.github/workflows/**`, and the release and
promotion scripts. A grain-made pull request touching them is held for a
security review, or (`release-promotion`, `grain-declarations`) for a
person. Two gaps remain, both profile B:

- **Forge-side merges.** All of grain's holds act on grain's own
  pull-request tasks. A writer who merges on the forge (or pushes to main
  where branch protection allows it) lands host-privileged code that the
  next release workflow builds, signs with the release key and publishes.
  The upgrade path pins digests and, where `VerifyCmd` is configured
  (`cosign verify`), checks the signature. But a signature only shows that
  CI built the image, not that a reviewed commit went into it, and with no
  `VerifyCmd` the check only warns. So "only images built from reviewed,
  merged commits" depends entirely on the forge's branch protection.
  Follow-up filed: have release builds refuse a commit that did not reach
  main through a reviewed merge (grain's merge marker, or the forge's
  required-review record), and record that in the image's provenance for
  the upgrade to check.
- **A person's Submit skips the review** until #3904 lands. #3905 audits
  the six such merges; nothing more is added here.

## 4. Self-modification by agents: closed

- `PATCH /api/tasks/{id}` touching grants needs `capabilities:admin` on
  the acting task and a reason (`agentAPIGate`), and
  `agentGrantEscalationFrom` refuses any grant the task does not hold. A
  running task cannot widen its own capabilities under `tasks:write`.
- `propose_task` capabilities pass the same escalation check. A proposal
  is auto-approved only when its grants are covered by the parent's and
  none is person-only (`IsPersonOnlyGrant`, `protocol.Covers`).
- An agent's grain/config change waits for a person's approval
  (`pendingConfigChangeFor` refuses non-person callers), and the approver
  now needs the scopes the change confers (1b).
- An agent's `.grain/` change goes through a grain pull request, which the
  `grain-declarations` trigger holds for a person. A declaration it gets
  merged that way is now also held at firing time (2a).

## 5. Attribution gaps

- **Anonymous access-code actions (#3873).** Every access-code session is
  "the owner": `IsApprover` admits any person, and a Submit or
  config-change approval records the actor as whatever the session says.
  Until #3873 ties access codes to identities, an approval under the access
  code cannot be traced to a person.
- **Shared deployment git credential (#3239).** Pushes grain makes on a
  task's behalf share one credential, so the forge's audit log names the
  deployment, not the task or the person who submitted it.
- **What is recorded:** a Submit records `LinkHumanSubmitted` and a
  `TaskEventSubmitted` note with the request actor. An approval records
  `LinkApprovedBy` per rule. A config-change approval posts "Approved and
  applied config change (sha)" as a task comment attributed to the request
  actor. None of these record *which scopes* the approver held. The
  refusal added in 1b is logged, but the grant is not. That is worth
  adding beside #3904's override record.

## Coordination

- #3864 (admin role edits): `tables/api_scope_set.json` in a config change
  now needs `capabilities:admin`, which matches `PUT /api/api-scope-sets`.
  If #3864 moves role edits to an access-admin scope, `stateScopePaths`
  should follow it.
- #3873 (identity-less access): see section 5.
- #3904 (review gate and explicit override): the 1b refusal has no
  override. The explicit-override design should cover it.
