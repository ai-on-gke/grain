# Unified grants: one grant model, one Grants picker

Status: design, with slice 1 (UI only) landed alongside it (task 1060),
slice 2 (read both) landed from task 3304, and slice 3 (write canonical
references) implemented in [Task #3317](/tasks/3317) (held to merge
after slice 2 ships in a release): `model.Grant` is
`{Capability, With, Via, Folder}`, `model.CanonicalGrant` reads every
spelling, writers across `pkg/model`, `pkg/ui`, `pkg/cli`,
`pkg/repodecl` and `pkg/orchestrator` record canonical `…/api#<what>`
(with `{repo}` when repository-limited) and `…/repo#read`/`#write`
references, `GrantsPicker.jsx` edits `with: {repo}` inline on `…/api`
grants, and the fidelity fixture is `pkg/model/canonical_grant_test.go`.
Slice 4 (migrate once) is implemented in [Task #3321](/tasks/3321):
schema migration 132, grain/config's `grants:` read as one key with
`reads:`/`writes:`, `grants:` in `.grain/` templates and schedules, and
the `release-gate-read` provider retired (§3's "As built" notes say what
it does and what it left for later). Slice 5 (repository grants in the
picker) is implemented in [Task #3322](/tasks/3322); see "Slice 5 as
built" under Slices. Reads and writes stay stored in their own tables.
[Task #3882](/tasks/3882) decided to keep them there, with a grant
projection and a deny-only policy check (§3, slice 4), and
slice 6, the per-capability conversions of §4, is mostly done; §4's
table says which, and the remaining slices and conversions are filed from
[Task #3274](/tasks/3274). The overview of capabilities as a whole is
[capabilities.design.md](capabilities.design.md).
Operator direction of 2026-10-26: everything a task is given should be
the same kind of grant, edited in one place in the UI.

## Where we are

A task carries four kinds of access today. Each has its own storage, its
own checks and its own form control:

| Kind | Spelling today | Stored in | Edited by (before slice 1) |
|---|---|---|---|
| Grain's own API | `api:<scope>`, `api:<preset>`, either `@owner/repo`-limited ([agent-api.md](../../docs/agent-api.md)) | `task_grant.capability` (the capabilities list) | "Grain API" accordion, `ApiGrantPicker` |
| Capability server methods | `https://…#method` plus `with:`, stored as `<ref>=<normalized json>` ([capabilities.md](../../docs/capabilities.md), [capabilities-spec.md](../spec/capabilities/capabilities.spec.md)) | `task_grant.capability` | "Capability server methods", `CapabilityMethodGrants` |
| In-process capabilities | bare names: `github-sandbox`, `read-issues`, … (`pkg/capability/`) | `task_grant.capability` | the "Capabilities" checklist |
| Repository access | `Target`/`Base`, `Reads` (`task_read`), `Writes` (`task_write`, repo + base), a declared template's `push:` refs (`model.RefPush`), CI's `repo-read:<repo>` / `repo-write:<repo>` | their own columns and tables | Repo and Base branch fields, `ReadOnlyReposField`, `WriteReposField` |

The first three already share one list and one table. `model.Grant` is
`{Capability, Via, Folder}`, and a server method's arguments are folded
into the `Capability` string. Repository access is the odd one out.
It has no `Grant` at all, and nothing that checks grants reads it.

## 1. One grant shape

```go
// model.Grant, after slice 2.
type Grant struct {
	Capability string          // a canonical reference, always https://…#method
	With       json.RawMessage // normalized arguments; nil for none
	Via        GrantSource     // provenance, unchanged
	Folder     *FolderRef      // unchanged
}
```

On the wire, in the API and in every declaration file, a grant is
`{capability: <reference>, with: {...}}`, which is the shape server
methods already use (`localGrantHandlers`, `POST /api/tasks/{id}/capabilities`).
Its string form, used for keys, chips, `task_grant.capability` and audit
lines, stays `<reference>` or `<reference>=<normalized json>`
(`model.EncodeCapabilityGrant`). Two grants are equal when their references are
canonically equal and their normalized `with:` are deep-equal (spec
§2.3.2). That rule already holds for server methods.

Every reference is an `https://` URL under `https://grain.dev/capabilities/`
for anything grain itself provides. That prefix is already canonical for
the built-in servers (`…/gcp#gcp-key`), and `model.IsCapabilityRef`
already tells references from bare names by it. A `grain:` scheme is
shorter, but it would give the codebase a third spelling to recognise.
(Open question Q1.)

### How each kind maps

| Today | Unified grant |
|---|---|
| `api:ci:write` | `{capability: "https://grain.dev/capabilities/api#ci:write"}` |
| `api:ci:write@acme/widgets` | `{capability: "…/api#ci:write", with: {repo: "acme/widgets"}}` |
| `api:task-admin` (preset) | `{capability: "…/api#task-admin"}`. A preset is a method whose description lists the scopes it expands to, as today (`model.APIGrantsOf`). |
| `https://…/gcp#gcp-key` `with: {permission_set: ci}` | unchanged |
| bare `github-sandbox`, `read-issues`, … | unchanged while they exist (they are read as legacy, see §4), then converted, one by one, to a reference |
| task `Target` + `Base` | `{capability: "…/repo#target", with: {repo, base}}`, exactly one per task with a repo. It is a projection: storage stays `task.target_*`/`task.base` (Q2). |
| `Reads` entry | `{capability: "…/repo#read", with: {repo}}` |
| `Writes` entry | `{capability: "…/repo#write", with: {repo, base}}` |
| a declared template's `push:` | `{capability: "…/repo#write", with: {repo, base, refs: [{ref, force?, delete?}]}}`, honoured only when `Via` is a declaration (see "git proxy" below) |
| CI `repo-read:<repo>`, `repo-write:<repo>` | `…/repo#read` / `…/repo#write` with `{repo}` in a job's `capabilities:` |

`…/api` and `…/repo` are not separate processes. They are **in-process
capability servers**: the same `Method` listing, `args` schema,
normalization and `POST /api/capability-grants/check` that an MCP server
gets, answered by grain itself. That way `GET /api/capability-methods`
lists them, `CapabilityArgsForm` draws their forms, and the picker has no
special case.

### What each consumer does with a grant

The table describes the end state. Every rule in it is today's rule,
moved to read the reference rather than the old spelling.

| Consumer | api | server method | repo |
|---|---|---|---|
| **Admission** (`ResolveGrants`, `task_grant_wait`) | Resolves the scope set (`APIGrantsOf`). No resource, never waits. | `attach` / lease as today, including `needs-config` and finite-resource waits. | `#target`/`#write`: the filing team's git credential must reach the repo (today's dispatch check). `#read`: the fetch check `mayRead`. |
| **Sandbox pass** (`MaterializeGrants`) | Nothing placed; `api_*` tools are served to the run (`agent.RunConfig.Grants`). | `setup_root`/`setup`, `mcp_endpoint` wiring, as today. | `#target` cloned to `work/` under `/home/debian`. `#read`/`#write` reachable through the proxy for clone. Nothing else placed. |
| **Prompt** | "grain api" tool section, as today. | Method's own prompt text, as today. | The repository section and `writeTargetsSection` (run.go), generated from the grants rather than from three fields. |
| **Git proxy ref policy** (`AuthorizeRefUpdates`) | none | `git#git-repo` grants, as today | Push to the task's own branch of `#target`, and nothing else, as today. `refs` widen it **only** when read from the declaration on the repository's default branch (`Store.GitDeclaredPush`), never from a copy on the task row. A `refs` value a person or agent types onto a task is refused at write time. `git-push-any-ref` becomes `#write` with `refs: ["refs/heads/*", "refs/tags/*"]`, which `ValidateRefPushPattern` today refuses for declarations; the migration keeps it a team grant, as `git-push-any-ref` was, and whether to make it admin-only is still Q3. |
| **Approval / danger** | §2 | §2 | §2 |
| **Audit** | `api_call` rows, with reason for dangerous scopes, as today. | grant/mint/release events, as today. | `grant` events (attach/detach), with the `with:` (Q5). |

## 2. Which grants a person must approve

There are three rules today. They carry over unchanged, and only the
input changes from three fields to one list.

1. **Dangerous API scopes.** `model.IsDangerousGrant` (`capabilities:admin`,
   `secrets:write`, `settings:write`, `connections:write`, `merge:force`,
   `tasks:admin`, `deployment:*`, and the bare aliases standing for a
   preset that contains one) becomes a predicate on `…/api#<scope>`.
   Only a person may grant one. An agent's `propose_task`, the agent API
   and `TemplateWithinTask` refuse it whether or not the agent holds it.
   `ReviewGrants` drops it. Nothing else about the rule moves.
2. **Grants the proposer does not hold.** `model.GrantsSubsetOf` already
   compares references by canonical capability plus deep-equal normalized
   arguments (`UncoveredCapabilityRefs`). Once every grant is a reference,
   that one comparison covers api grants too. Equality stays strict:
   `…/api#ci:write {repo: x}` is not covered by an unlimited
   `…/api#ci:write`, which matches today, where the two names differ.
   Whether an unlimited grant should cover a limited one is Q4. A
   proposal that is not a subset is filed unapproved, as today.
3. **Writes outside the task's declared repos.** `UngrantedWrites` and
   `LandsWhereGranted` become functions of the `…/repo#write` grants
   (plus `#target`) rather than `Writes`/`Target`. A child's `#write`
   that is not among the parent's write grants, repo and base alike,
   lands nowhere granted and inherits no auto-merge. The config
   repository (`IsConfigRepo`) is never reached through the widening.
   Both rules stay exactly where they are.

`#read` grants keep today's rule (`TemplateWithinTask`'s `mayRead`):
granting a read of a repo the acting task cannot fetch is refused, and
nothing else is asked of them. The capability policy
([local-policy.md](../../docs/local-policy.md)) matches on the reference, so a rule
can name `…/repo#write` or `…/api#merge:force` like any other method.

## 3. Migration

**Recommendation: read both shapes for a while, write one, then migrate
once with a fidelity test.**

- **Slice 2 (read both).** Add `model.CanonicalGrant(string) Grant`. It
  turns `api:x[@r]` into `…/api#x {repo}`, and turns
  `repo-read:`/`repo-write:` likewise. Bare in-process names pass through
  unchanged. Every reader goes through it: admission, approval rules,
  policy, audit and the UI. Every writer keeps writing today's strings,
  so an older binary reading a newer store sees nothing new. The API
  accepts `grants: [{capability, with}]` beside `capabilities`, `reads`
  and `writes`. A request that sends both for one kind is refused rather
  than merged.
  *As built (task 3304):* readers compare in the spelling slice 2
  stores (`model.StoredGrantSpelling`, CanonicalGrant then back), so a
  legacy-only store gives byte-for-byte today's answers. `grants:` is
  accepted on task create and edit (`CreateTaskRequest`,
  `UpdateTaskRequest`); a list naming only repository access leaves
  capabilities alone. §2.1.3's method grammar gained `:` for `…/api`.
  The React grant helpers (`ui/src/apiGrants.js`) read both spellings as
  of task 3607 while still writing the `api:` spelling that this slice
  stores.
- **Slice 3 (write one).** Writers switch to references. This is the
  first change an older binary cannot read, so it lands in the release
  after slice 2 ships.
  *As built ([Task #3317](/tasks/3317)):* `NormalizeCapabilityNames` and
  `NormalizeCapabilityGrants` canonicalize `api:<what>[@repo]` and
  `…/api#<what>[=...]` to `https://grain.dev/capabilities/api#<what>`
  (omitting empty `with:` and encoding `={"repo":"<owner/name>"}` when
  repository-limited) and rewrite `repo-read:<repo>` /
  `repo-write:<repo>` to `…/repo#read` / `…/repo#write`. Writers across
  `pkg/model` (defaults, console sessions, system templates, external
  agent tokens, `{capability: api, with: ...}` and scope-set helpers,
  proposal seeding, `SplitRequestGrants`), `pkg/repodecl`, `pkg/ui`,
  `pkg/cli` (brace-aware comma splitting for `-default-capabilities` and
  `grain repo capabilities -set`), and `pkg/orchestrator` emit canonical
  references, while `model.CanonicalGrant` continues reading legacy
  `api:`, `repo-read:`, and `repo-write:` strings from unmigrated store
  rows and `.grain/` declarations. `protocol.ValidMethod` gained `.` and
  `/` segments (`len <= 63`) and `grainserver` lists `APIScopeSetsInEffect()`
  live on `https://grain.dev/capabilities/api` so slash-named scope sets
  (`ops/triage`, `grain/task-admin`) normalize and list as methods.
  `GrantsPicker.jsx` drops the top-level "Grain API grants apply to"
  dropdown and edits `with: {repo}` inline on `…/api#<what>` sections
  while still rendering and removing unmigrated `api:` lines.
- **Slice 4 (migrate once).** One store migration rewrites
  `task_grant`, `schedule_grant`, `template_grant`, repo config's
  `default_capabilities` / `ci_capabilities`, console sessions'
  defaults, and the deployment's `default_capabilities`. It also projects
  `task_read`/`task_write` (and the schedule/template equivalents) into
  grant rows. **Fidelity test:** for every row in a fixture store and in
  a snapshot of this deployment's grain/config, old → `CanonicalGrant` →
  old again must be byte-identical, and the approval predicates above
  must return the same answer on both forms for every (proposer, child)
  pair in the fixture.
  *As built ([Task #3321](/tasks/3321)):* schema migration 132
  (`model.MigrateCanonicalGrants`, `canonical_grant_migration.go`) reads
  every grant string off the store, as migration 88 did, and rewrites
  each one `CanonicalStoredGrant` changes -- `api:<what>[@repo]`, an api
  reference in a non-canonical spelling (`={}`, an unprinted repo), and
  any `repo-read:`/`repo-write:` a state-repository import brought in
  after 88 -- in `task_grant`, `template_grant`, `schedule_grant`,
  `task_grant_wait`, both `default_capabilities`, `ci_capabilities`,
  `capability_policy`'s catalogue defaults, and the console sessions'
  capabilities and `declinedDefaults`. A row whose
  reference its owner already holds is dropped. Bare in-process names
  (`release-gate-read` among them) and every other reference are left as
  they are. A grain/config import at the current schema runs the same
  rewrite, so a mixed-version or hand-edited file's legacy spellings land
  as one key with their references. The fidelity test is
  `pkg/model/sqlite/canonical_grant_migration_test.go`: every migrated row
  is its reference, old → `CanonicalGrant` → old is byte-identical, the
  approval predicates (`GrantsSubsetOf`, `EscalatedCapabilities`,
  `LandsWhereGranted`, `ReviewGrants`, `TemplateWithinTask`, dangerous and
  person-only, the capability policy, the api scope set) answer every
  (proposer, child) pair alike before and after, and a second run is a
  no-op; the earlier migration tests read their expectations through
  `CanonicalStoredGrant` since every store now ends at 132. Not done here:
  `task_read`/`task_write` (and the template and schedule tables) are
  **not** projected into grant rows. A `…/repo#read` grant row outside
  CI is refused by the capability policy (`CIOnlyRefusal`), and the
  `task_ready` view, dispatch, liveness and metrics join those tables
  directly. Slice 5 ([Task #3322](/tasks/3322)) kept that storage too,
  editing reads and writes in the picker as lists, so moving it is a
  follow-up of its own; until then `TaskRepoGrants` is the projection,
  as for `#target` (Q2).
  *Storage decision ([Task #3882](/tasks/3882)):* the read and write
  tables (`task_read`/`task_write`, `template_*`, `schedule_*`) **stay
  the source of truth, and are not mirrored** into the grant tables.
  `Target`/`Base` stay columns (Q2) for the same reason. About thirty SQL
  sites join the read/write tables directly: the `task_ready` view,
  `pkg/dispatch`, `pkg/orchestrator` (liveness, targetrepos, cycle),
  `pkg/metrics`, the postgres adapter, `pkg/trustboundary`, the repo-case
  renames, and the grain/config import. Moving them all is one large
  change with nothing to gain at its end. A mirror would be a second copy
  every one of those writers had to keep in step. It would also be a
  `#read` row in `task_grant` that every grant reader (admission,
  prompts, the catalogue, the export) had to learn to skip. The grant form
  is a pair of functions at the model boundary instead.
  `model.TaskRepoGrants` projects Reads and Writes to `…/repo#read {repo}`
  and `…/repo#write {repo, base}`. `model.TaskRepoAccessOf` is its
  inverse: it reads grant rows back into Reads and Writes and leaves every
  other grant (`#target`, a CI job's `{repo}`-only `#write`, a `refs`
  widening) where it was. A future move of storage would use the same two
  functions at Store load and save. The fidelity test
  (`TestRepoAccessReadBackFromGrantRowsKeepsItsAnswers`, beside migration
  132's) stores each fixture's access as those grant rows in `task_grant`,
  `template_grant` and `schedule_grant`, and reads it back through
  `TaskRepoAccessOf`. The result must be the same access, and
  `UngrantedWrites`, `UngrantedReads`, `LandsWhereGranted`, `SameBranch`,
  `TemplateWithinTask` (its `mayRead` and `WriteTargetFor`) and the
  policy must give the same answer for every (proposer, child) pair.
  *Policy:* a task's own access goes through
  `CapabilityPolicy.PermitsTaskRepoGrant`. That is `Permits` without the
  CI-only refusal, and it applies deny rules only: who may read or push a
  repository is still for `mayRead`/`UngrantedWrites` to decide, so the
  policy can narrow access but never has to admit it. An allow rule
  written for CI jobs' `{repo}` grants therefore refuses no task. What
  sets a task's access apart from a CI job's `#read {repo}`, which has
  the same shape, is where it comes from: Reads, never `task_grant`.
  `Permits` still refuses a `#read` in a task's capabilities as a CI
  grant pasted onto a task. Admission (`checkTaskGrantsPolicy`) applies
  the check to every task that reads or writes beyond its target, so a
  deny rule on `…/repo#write` for `runKinds: [task]` now holds such a
  task back.
- **State-repo export (grain/config).** The exported YAML gains
  `grants:` lists. `capabilities:`, `reads:` and `writes:` keep being
  read (`declschema`), and for a release keep being *written* beside
  `grants:`, so a grain/config shared by two versions round-trips. The
  import merge
  treats the two spellings as one key.
  *As built ([Task #3321](/tasks/3321)):* a template's or schedule's
  `grants:` (template_grant/schedule_grant) is exported in canonical
  references, and on import an entry that is a task-level repository
  grant -- `…/repo#read {repo}` or `…/repo#write {repo, base}`, as a
  string or `{capability, with}` -- is a row of the read or write table,
  and one `reads:`/`writes:` already names is held once
  (`pkg/staterepo/decl.go`, `projectedRepoRow`). The export keeps
  repository access under `reads:` and `writes:` only for now: the
  previous release reads every `grants:` entry as a grant row
  (`decodeCompanion`), so writing `#read`/`#write` there would hand its
  template tasks a CI-only grant. Writing them into `grants:` as well
  lands with that follow-up, a release after this reader.
  *As built ([Task #4063](/tasks/4063)):* the reader (merge `29192f3f8`)
  shipped in `v3.5.0`. The export now writes each read row as
  `…/repo#read {repo}` and each write row as `…/repo#write {repo, base}`
  under `grants:`, after the entry's own grant rows
  (`repoGrantEntries`), and still writes `reads:`/`writes:`. A row whose
  repository is not a plain `owner/name` on the default host goes under
  `reads:`/`writes:` only. On import the two spellings collapse into one
  row, so export → import → export is stable
  (`TestRepoAccessExportedAsGrantsRoundTrips`). Dropping `reads:`/`writes:`
  from the export, a release after this, is not done yet.
- **Repository declarations.** `.grain/templates/*.yml` and
  `.grain/schedules/*.yml` accept `grants:` beside `capabilities:`,
  `reads:`, `writes:` and `push:` indefinitely. These live in other
  people's repositories, so nothing is migrated there, and `push:` stays
  the way a declaration widens refs.
  *As built ([Task #3321](/tasks/3321)):* `grants:` is accepted in a
  repository (`declschema`, `repodecl.declaredGrants`) as the unified
  spelling of `capabilities:`, read as one list with it and held to its
  rule; `reads:` and `writes:` stay refused there, as they always were.
- **API presets** need nothing. They become methods of `…/api`, and the
  alias table (`apiAliasCapabilities`) keeps `self-upgrade` and
  `pipeline-control` meaning their presets until §4 retires them.

## 4. Retiring the in-process capabilities

Operator decision (2026-10-26): the in-process capabilities stay **out**
of the unified picker, in their own checklist labelled **Legacy
capabilities**. They are converted, not merged as they are.
This is the per-capability list, with what each one becomes. The
providers still registered on `main` (`daemon.CapabilityProviders`) are
the rows not marked **done**:

**Status (task 2876):** every row that minted a credential is done. The
`capability.RegisterProvider` seam and the Settings → Legacy capabilities
tab are removed; the tab's GitHub host, GCP settings and secrets are the
Deployment credentials card on Settings → Capabilities. The bare names
remain one-release aliases (no promoted release contains every
conversion yet). The task forms' empty Legacy capabilities checklist
is removed too (PR #985).

| In-process id | Becomes (proposed) | Kind of change |
|---|---|---|
| `gcp-key` | `https://grain.dev/capabilities/gcp#gcp-key` `{permission_set}` | **done** (task 2430): in-process `gcp-key` and `gcp-key:<name>` providers removed; schema migration 97 seeds `sandbox-agent` and `<name>` permission sets on `https://grain.dev/capabilities/gcp` from `grain_config` and `gcp_key` rows (keeping `/home/debian/.gcp-service-account-<name>.json` key placement) and rewrites stored grants across tasks, templates, schedules, default/CI capabilities, console sessions, catalogue, and policy (`gcp-key` and `gcp-key:<name>` remain one-release aliases; [gcp-key-permission-sets.md](../../docs/gcp-key-permission-sets.md)) |
| `gemini-key` | `https://grain.dev/capabilities/gcp#gemini-key` (Q6) | **done** (tasks 2431, 2847): `…/gcp` offers `#gemini-key` when configured with `gemini_key: {project, admin?}`, minting with that admin's credential and placing `/home/debian/.gemini-api-key` with the in-process prompt, and reaping unreleased keys older than 24h. The in-process provider is removed; schema migration 103 seeds `gemini_key: {project}` from `grain_config.gcp_project`, minting with the `gcp-key-minter` secret (an existing admin holding it, the top-level admin on an unconfigured entry, or a new `GRAIN_GCP_MINTER_*` admin beside the old one kept as `default`), and rewrites stored grants, `ci_capabilities` and the policy to `…#gemini-key={}`; bare `gemini-key` remains a one-release alias |
| `<vendor>-proxy` (model proxies) | `https://grain.dev/capabilities/model-proxy#model-proxy` `{vendor}` | **done** (task 2431): the in-process `claude-proxy`, `openai-proxy` and `gemini-proxy` providers removed; `…/model-proxy` is now an in-process server (`pkg/capability/modelproxy.Method`, registered by the daemon over its own proxy endpoints), not the `grain capability model-proxy` built-in, so each mint still forwards with the run's own team connection (the run's agent connection, handed in the mint's context, `model.WithRunConnection`) with no deployment-wide key. Same token registry, same allowlist, same `/home/debian/.<vendor>-proxy-{url,token}` files and the same prompt. Stored grants rewritten to `…#model-proxy={"vendor":"<vendor>"}` by schema migration 100; the bare names remain one-release aliases |
| `github-credential:<name>` | `…/repo#credential` `{credential: <name>}` | **done** (task 3307): `githubtoken` provider wrapper removed from `daemon.CapabilityProviders`; `https://grain.dev/capabilities/repo#credential` `{credential}` added on the in-process `…/repo` server (`grainserver.RepoServer`), validating `githubtoken.NamePattern` (excluding `"anonymous"`), enumerating configured names under `<secrets>/github`, and refusing tasks holding more than one `#credential` grant with `model.ErrConflictingGitCredentials`. Overrides which named `secrets/github/<name>` credential `pkg/gitproxy`, `-pr-credential` and `git_identity` select on the live task (`TaskGitCredentialOn`, `GitCredentialOverrideOn`), with no `repo` argument yet (like `git-push-any-ref`); `pkg/ui/github_tokens.go` refuses deleting a named credential held via either `repo#credential` or the legacy prefix. Stored grants across tasks, templates, schedules, default/CI capabilities, console sessions, catalogue, and policy rewritten to `https://grain.dev/capabilities/repo#credential={"credential":"<name>"}` (policy rules to `…/repo#credential`) by schema migration 120; `github-credential:<name>` remains a one-release alias |
| `github-sandbox` | `…/github#sandbox-repo` | **done** (task 2433): `githubsandbox` provider removed from `daemon.CapabilityProviders`; `https://grain.dev/capabilities/github#sandbox-repo` added on `grainserver.GitHubServer` (`pkg/capability/githubsandbox.MethodSpec`), provisioning a single-use `grain-sandbox-<attempt>` repository (named for the attempt ID, which carries the deployment, so deployments sharing one App installation cannot collide), scoped GitHub App installation token, `/home/debian/.github-sandbox-{token,repo}` placements and prompt for both tasks (`model.WithMintRunID`, `model.WithMintCredentials`) and CI jobs (`model.WithCIMint`), declaring `model.CIGrantRule{RepoGranted: true, TaskGrantable: true}` (no event gate; an event-gated rule is always CI-only, and `CIGrantRuleFor`/`CanonicalCIGrant` bind its bare, case-varied and methodless spellings), deleting on release through the one `grainserver.GitHubServer()` (task 3538), and reaping stray `grain-sandbox-*` repositories via `githubsandbox.Current()`, with or without a provider registry. Stored grants across tasks, templates, schedules, default/CI capabilities, console sessions, catalogue, and policy rewritten to `https://grain.dev/capabilities/github#sandbox-repo={}` (policy rules to `…/github#sandbox-repo`) by schema migration 124; bare `github-sandbox` remains a one-release alias |
| `git-push-any-ref` | `…/repo#write` `{refs: ["refs/heads/*", "refs/tags/*"], force: true}` | **done** (task 1063): provider removed, `https://grain.dev/capabilities/repo#write` added on a new in-process `…/repo` server (`grainserver.RepoServer`); its schema validates only that one `refs`/`force` value (beside `{repo}`, a CI job's write access, since task 2365), the one the git proxy honours (`model.IsGitPushAnyRefGrant`), so a narrower task-level `refs` is refused rather than recorded as doing something (Q3). It stays a team grant, not dangerous, as `git-push-any-ref` was. `repo` is not an argument yet: like the bare grant it applies to every repository the task may write. Stored grants rewritten to `https://grain.dev/capabilities/repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}` by schema migration 85 |
| `local-scripts:<name>` | `…/host-script#host-script` `{script}`, one grant per script of the declaration | **done** (tasks 2366, 3077): at each start the daemon converts every declaration it can read (`localscripts.ConvertToHostScripts`, including `approve: true` scripts) into scripts `<declaration>-<script>` on the in-daemon host-script server (`https://grain.dev/capabilities/host-script`), binding secrets as `$env:GRAIN_LOCAL_SCRIPTS_<SECRET>` → `$secret:<name>`, rewriting stored grants (`Store.RewriteRetiredGrants`), and keeping `local-scripts:<name>` as a runtime alias (`model.SetRuntimeGrantExpansions`) whose expanded grants remain withheld from scheduled runs and refused to CI jobs (`localscripts.MigratedFrom`). `host-script` supports optional stdin (`grain host-script --stdin <name>`) and per-call `approve: true` via a per-task callback URL + scoped `ghsa_` token handed over at mint (`localexec.HTTPApprover`). `localscripts.DynamicProvider` and `grain mcpserver`'s local-scripts MCP serving are removed |
| `repo-read:<repo>`, `repo-write:<repo>` (CI) | `…/repo#read` / `…/repo#write` `{repo}` | **done** (task 2365): `reporead` / `repowrite` providers removed; `https://grain.dev/capabilities/repo#read` `{repo}` added and `#write` widened to take `{repo}` beside the any-ref value (`model.IsGitPushAnyRefGrant` still means only the latter). What the providers decided rides on the grant (`model.IsRepoAccessGrant`): it is CI-only (a task holding one is refused with `CIOnlyRefusal`), and `model.RepoAccessAdmitsCIEvent` is write's event gate (release, workflow_dispatch, grain's own trusted schedule). `daemon/ci.go` widens the proxy's scopes from the job's declared ids through that gate. The bare `repo-read:`/`repo-write:` are read as the same grants indefinitely (a repository's own `.grain/workflows`), and the local policy matches either spelling. Stored grants, `repo_config.ci_capabilities` included, rewritten by schema migration 88 |
| `self-debug` | `…/api#self-debug` preset (already an alias) plus a new `…/grain#source` method for the source checkout | **done** (task 3146): provider (`pkg/capability/selfdebug`) and its `admincaps.Providers` entry removed; `https://grain.dev/capabilities/grain#source` added on the in-process `…/grain` server for what is not a REST route: `grain-src/` in the sandbox (`placeGrainSource`), `src://` and `src://core/...`, and -- held deployment-wide -- every repository's CI and the whole of `config://`. It stays team-scoped: `admincaps.TeamScoped` now names `grain#source` (`IsTeamScoped` matches any spelling of it, the bare alias included), so `TaskDetail.DeploymentWide` and the mcpserver's CI scope read it as they read `self-debug`. Everything `self-debug` served over a REST route is the `api:self-debug` preset (`tasks:read` + `deployment:read`): the task readers, `read_pipeline_changes`, `read_grain_metrics`, `task://`, `deploy://metrics` and `deploy://pipeline-changes`; the orchestrator forwards `api:self-debug` to the forked mcpserver beside `grain#source` so those tools stay on the roster, and `model.DefaultConsoleSessions()` seeds the director with both. `CanonicalLegacyGrants` maps the bare name to both (`model.SelfDebugGrants`), the catalogue's `in_process.self-debug` binds both, and schema migration 115 rewrites stored grants -- the grant tables, default capabilities, console sessions, `repo_config.ci_capabilities` and the capability policy's catalogue defaults -- to `api:self-debug` + `https://grain.dev/capabilities/grain#source={}`, keeping each once where either was already held (a capability policy rule naming `self-debug` names `…/grain#source`). The bare name remains a one-release alias (`model.IsGrainSourceGrant` and `apiAliasCapabilities` still read it) |
| `grain-log` | `…/grain#journal` | **done** (task 2037): provider removed, `https://grain.dev/capabilities/grain#journal` added on the in-process `…/grain` server (redaction kept, deliberately in no api preset), stored grants rewritten to `https://grain.dev/capabilities/grain#journal={}` by schema migration 80 |
| `deployment-health` | `…/api#deployment:read` plus `…/grain#health` for the host/sandbox probes | **done** (task 2037): provider removed, `https://grain.dev/capabilities/grain#health` added on the in-process `…/grain` server alongside `api:deployment:read`, stored grants rewritten to `api:deployment:read` + `https://grain.dev/capabilities/grain#health={}` by schema migration 81 |
| `self-repair` | `…/grain#host-exec` (each command still needs a live chat reply) | **done** (task 2252): provider removed, `https://grain.dev/capabilities/grain#host-exec` added on the in-process `…/grain` server and marked person-only (`protocol.RequirePersonGrant`), `run_host_command` still built by pkg/capability/selfrepair, still confirmed by a live chat reply (`selfrepair.Confirm`) and still added only to an Interactive task's run; stored grants rewritten to `https://grain.dev/capabilities/grain#host-exec={}` by schema migration 84 (repo_config.ci_capabilities included). Q7 answered: a grant |
| `self-upgrade` | `…/api#self-upgrade` preset | **done** (task 2036): provider and its three tools removed, stored grants rewritten to `api:self-upgrade` by schema migration 75 |
| `pipeline-control` | `…/api#pipeline-control` preset | **done** (task 2004): provider and its two tools removed, stored grants rewritten to `api:pipeline-control` by schema migration 69 |
| `dispatch-control` | `…/api#dispatch-control` preset | **done** (task 2004): provider and its four tools removed, stored grants rewritten to `api:dispatch-control` by schema migration 68 |
| `test-quarantine` | `…/api#tests:write` (new scope), preset `test-quarantine` | **done** (task 2036): provider and `quarantine_test` removed, `POST /api/tests/quarantine` added under `tests:write` and `grain quarantine` gated on it, stored grants rewritten to `api:test-quarantine` by schema migration 76 |
| `read-issues` | `…/github#read-issues` | **done** (task 3147): provider removed, `https://grain.dev/capabilities/github#read-issues` added on a new in-process `…/github` server (`grainserver.GitHubServer`), gated with the same reach as the provider (`grain issue` and `list_issues`/`read_issue` over the repositories the run may read via its team git credential). Stored grants, `repo_config.ci_capabilities` and `capability_policy` rewritten to `https://grain.dev/capabilities/github#read-issues={}` by schema migration 121; bare `read-issues` remains an input-only alias for one release |
| `release-notes` | `…/api#release-notes` preset (`release-notes:read` + `release-notes:write`) | **done** (task 2035): provider and its tools removed, `/api/release-notes*` routes moved to `release-notes:read`/`release-notes:write`, stored grants rewritten to `api:release-notes` by schema migration 72 |
| `memory` | `…/api#memory` preset (`memory:read` + `memory:write`) | **done** (task 2035): provider and its tools removed, stored grants rewritten to `api:memory` by schema migration 71 |
| `team-broadcast` | `…/api#teams:broadcast` (new scope), preset `team-broadcast` | **done** (task 2004): provider and `broadcast_team_message` removed, stored grants rewritten to `api:team-broadcast` by schema migration 67 |
| `bootstrap-playbooks` | `…/config-map#config-map` `{"config_maps":["bootstrap"]}` | **done** (task 3148): embedded markdown moved to `pkg/playbooks`, `pkg/capability/bootstrap` and its two MCP tools deleted, and `bootstrap.New()` removed from `daemon.CapabilityProviders`. The built-in `…/config-map` server always serves a reserved `bootstrap` map placing `/home/debian/.config-map/bootstrap/*.md` (`0644`, never included in an implicit empty-args mint) with the live-approval `run_host_command` prompt guidance (`grain capability skill -dir bootstrap` serves the same embedded directory); `src://playbooks` and `grain playbooks` remain enabled by the grant. Stored grants and `repo_config.ci_capabilities` rewritten to `https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}` (policy rules to `…/config-map#config-map`) by schema migration 118; bare `bootstrap-playbooks` remains a one-release input alias. Task 3274 had recorded `…/grain#playbooks` as the target; task 3148's brief named the config-map server instead, and the concerns behind `#playbooks` are met here: the embedded map needs no operator configuration (the built-in server serves it even with no config-map directory), an embedded-only grant (`model.IsEmbeddedBootstrapOnlyGrant`) is minted and placed on the unisolated `host` backend while operator config maps stay refused there, and `DefaultConsoleSessions()` seeds `model.BootstrapPlaybooksGrant` |
| `console` | task setting `hosting: unattended \| console` (§4.1) | **done** (task 3145): a `hosting` column (`unattended \| console`) on tasks, templates and schedules, with `default_hosting` on repository and deployment settings and `hosting` on console sessions; `Task.HasConsole` is `NormalizeHosting(Hosting) == HostingConsole` (`pkg/orchestrator/console.go`), separate from `Interactive` and with no person-only rule, and each change is a `hosting-changed` task event with old value, new value and actor. Schema migration 123 moved stored `console` grants from tasks, templates, schedules, default/CI capability lists and console sessions into the column and dropped the catalogue and policy entries (Q10); `pkg/capability/console` and its `daemon.CapabilityProviders` entry are removed, the task pane has a **Hosting** select (`Unattended` / `Interactive console`), as do the template and schedule editors (`HostingField`, task 3319; a console session is always `console`), and the bare name is a one-release input alias mapped to `console` with a deprecation warning |
| `priority-dispatch` | task setting `admission: standard \| bypass` (§4.1) | **done** (task 3144): an `admission` column (`standard \| bypass \| durable`) on tasks, templates and schedules, with `default_admission` on repository and deployment settings; `Task.HasPriorityDispatch` is `Admission >= bypass` and `priorityDispatchTaskSQL` the column predicate. Only a person may raise it above `standard` (agent-filed tasks and proposals asking for more wait for a person; the pipeline steward template and console sessions set it by construction), and each change is an `admission-changed` task event with old value, new value and actor. Schema migration 122 moved stored grants from tasks, templates, schedules, default capability lists and console sessions into the column and dropped the catalogue and policy entries (Q10); `pkg/capability/prioritydispatch` and its `daemon.CapabilityProviders` entry are removed, the task pane's toggle is an **Admission** select, and the bare name is a one-release input alias mapped to `bypass` with a deprecation warning |
| `durable` | task setting `admission: durable` (§4.1) | **done** (task 3144): with `priority-dispatch` above; `Task.IsDurable` is `Admission == durable` and `durableTaskSQL` the column predicate, a row holding both grants migrated to `durable`, `pkg/capability/durable` and its `daemon.CapabilityProviders` entry removed, and the bare name a one-release input alias mapped to `durable` with a deprecation warning |
| `release-gate-read` (CI) | `…/api#release-gate-read` preset | **done** (task 3321, slice 4): `ciapi.NewAlias` is no longer registered in `daemon.CapabilityProviders`, which now registers nothing. A CI job's `release-gate-read`, `api:release-gate-read` and `…/api#release-gate-read` all resolve through `ciapi.DynamicProvider` (the bare name via `ciapi.Aliases`), so `.grain/workflows` keep declaring it, and it stays a bare string in stored CI capability lists |
| `db-read`, `db-write` | `…/grain#sql` `{write: bool}` | **done** (task 2037): providers removed, `https://grain.dev/capabilities/grain#sql` (`{write: bool}`, with `write: true` dangerous) added on the in-process `…/grain` server, stored grants rewritten to `https://grain.dev/capabilities/grain#sql={"write":false}` / `{"write":true}` by schema migration 82 |
| `grain-ci` | `…/grain#ci` | **done** (task 2364): provider removed, `https://grain.dev/capabilities/grain#ci` added on the in-process `…/grain` server (still granted solely from a repository's CI Mode and refused when attached by hand), stored grants rewritten to `https://grain.dev/capabilities/grain#ci={}` by schema migration 96 (repo_config.ci_capabilities included) |
| `image-signing`, `registry-push`, `package-publish` (CI) | `…/release#image-signing`, `…/release#registry-push`, `…/release#package-publish` | **done** (task 2432): the three providers removed from the daemon's registry; `…/release` is an in-process server (`pkg/capability/release`) whose mints are the retired providers' own Resolve and Materialize for the CI job in the mint's context (`model.WithCIMint`). The CI-only, event (image-signing: `release`; the others: `release`, `workflow_dispatch`, and package-publish grain's trusted schedule) and repository-list rules are declared by the server (`capability.Method.CI`, `model.CIGrantRule`) and enforced ahead of `capability_policy`, which can only narrow them. Stored grants rewritten to `…#<name>={}` by schema migration 101; the bare names remain one-release aliases |
| `corp-research` (wrapper distribution build) | a `…/corp#research` method registered by the wrapper distribution's build through the same `capability.Register` seam | **done** (task 2377): core's `capability.Register` takes an in-process server ID, a `protocol.MethodSpec`, `RunGrant`, `DefaultOn`, display name, description and legacy bare `Aliases`; `grainserver.Lookup`/`IsInProcessGrant`/`Statuses` (and so admission, `GET /api/capability-methods` and the mcpserver allowlist) resolve the server built from registered methods, `capability.RunGrants` forwards the method's ref, grant and aliases to the run, aliases go through `model.RegisterLegacyGrantAlias` into `CanonicalLegacyGrant(s)`, and antigravity matches a host-tool grant by method. Stored grants are rewritten to `https://grain.dev/capabilities/corp#research={}` by schema migration 86 (repo_config.ci_capabilities included). The wrapper distribution's build now registers the method (`capability.Register(Method())` in its `capability/corpresearch`) with `corp-research` as its alias, and its `corpresearch` provider is gone |

How the `self-debug`, `grain-log` and `deployment-health` style grants
fit (all three now converted): they are grain reading itself. Each
became either api scopes (anything that is a REST route already) or
methods of one in-process `…/grain` server for the parts that are not
routes: the source checkout (`#source`), the journal (`#journal`) and
host probes (`#health`). That server is answered by the daemon, exactly
like `…/api` and `…/repo`. `grain#journal` stays separate from
`grain#source` for the reason its description gives: the journal is a
different exposure. Converting one capability is one task. It adds the
method, makes `CanonicalLegacyGrant(s)` map the bare id to it, migrates
its rows, and deletes the provider. Until then, the bare id keeps working
and stays in the legacy checklist.

### 4.1 Admission and hosting are task settings, not grants

`console`, `priority-dispatch` and `durable` are grants in name only.
None of them mints, places or reaches anything: each provider is a bare
`Spec` over `model.BaseCapability`, and `pkg/capability/durable` says it
"exists so the grant is offered, listed and audited like any other
capability". What they really change is how the task is scheduled and
hosted, and the grant table is only where that is stored:
`Task.HasPriorityDispatch` and `Task.IsDurable` loop over `t.Grants`, and
dispatch asks `priorityDispatchTaskSQL`, an `EXISTS` over `task_grant`, in
`StartRun`'s own transaction. They move out of grants into two task
settings (operator decision, 2026-11-04, Q8).

**`admission: standard | bypass | durable`** replaces `priority-dispatch`
and `durable`. The values are ordered, and each one includes the one
before it, which is how the grants already behave (`durable` implies
`priority-dispatch` in two hand-kept places, `HasPriorityDispatch` and
the SQL's `IN (?, ?)`):

| Value | What a run of it gets | Today |
|---|---|---|
| `standard` | ordinary admission: worker and merger counts, pipeline pause, base-repair parking, per-connection concurrency limit, CI preemption | neither grant |
| `bypass` | admitted outside the worker and merger counts, ordered ahead of mergers and ordinary work, passes a pipeline pause other than an upgrade's own hold, never parked behind a red base branch's repair, not held by a connection's concurrency limit, never preempted by CI; still charged to the sandbox budget under the priority policy | `priority-dispatch` |
| `durable` | `bypass`, plus no wall-clock deadline and no wait-credit cap, suspended by the idle watchdog instead, counted under `Limits.Durable` rather than `max_workers`, setup failures suspended rather than counted | `durable` |

The name is chosen to avoid "priority" on purpose. The numeric
`task.priority` column (schema migration 73) is a different thing and is
unchanged: it orders the queue (`taskQueueOrderSQL`) and nothing else.
It goes on ordering tasks within an admission level. In the UI the
setting reads **Admission: Standard / Bypass queue / Durable**.

**`hosting: unattended | console`** replaces `console`: whether dispatch
starts the agent CLI on a pseudo-terminal for somebody to type into
(`pkg/console`), or hands it a prompt and reads the answer back. It
stays separate from `admission` because it says who drives the run, not
when it is let in. Every console session is `admission: durable` by
construction (`model.ConsoleSessionGrants` adds `durable` today), and it
is `hosting: console` only if it holds `console` today. `hosting` is
also still not `Interactive`, which marks a task filed as a live chat
and changes nothing about how the run is driven.

What carries over:

- **Only a person may raise `admission`** above `standard`, as
  `IsPersonOnlyGrant` does for the two grants today. A task an agent
  files, or a proposal, that asks for more waits for a person the way
  `EscalatedCapabilities` makes a grant wait today. Two places set it
  by construction and stay exempt: `pipelineStewardTemplate` (`bypass`,
  and in fact `durable`, which includes it: the steward held both grants,
  and a row holding both migrates to `durable`, DT-6)
  and `model.ConsoleSessionGrants` (`durable`). `hosting` has
  no such rule, because `console` has none today.
- **Same places as today.** Both settings are columns on tasks,
  templates and schedules. Repositories and the deployment can set
  defaults for them, which replaces naming `console` (or either
  admission grant) in `DefaultCapabilities`.
- **Audit.** A change to either setting is recorded as a task event
  with the old value, the new value and the actor, the way grant attach
  and detach are recorded today.
- **One predicate.** `HasPriorityDispatch` becomes `t.Admission >=
  AdmissionBypass`, `IsDurable` becomes `t.Admission == AdmissionDurable`,
  and `priorityDispatchTaskSQL` / `durableTaskSQL` become
  `t.admission IN ('bypass', 'durable')` / `t.admission = 'durable'`: a
  column read in place of a subquery.
- **Who may hold it.** `catalogueEditRefusal` checks
  `priority-dispatch` against the catalogue's `CatalogueInProcess`
  restrictions (repos, templates, kinds) today. No deployment narrows
  them (Q10), so nothing replaces that check. The person-only rule and
  `Limits.Durable` are the only gates.

Migration, like the other conversions:

1. One schema migration moves `console`, `priority-dispatch` and
   `durable` rows into the new columns, from wherever grants are stored:
   tasks, templates, schedules, deployment and repository default
   capability lists, console sessions, the catalogue and the policy. It
   then deletes the rows. A task holding both admission grants becomes
   `durable`. Catalogue and policy entries for the three names are
   dropped, not converted (Q10).
2. For one release the bare names are still accepted on input (API,
   `-grant`, declarations, grain/config import) and turned into the
   setting with a deprecation warning. They are never written.
3. `pkg/capability/console`, `pkg/capability/durable` and
   `pkg/capability/prioritydispatch` are deleted, along with their
   entries in `daemon.CapabilityProviders`. The task pane's
   `PriorityDispatch` toggle, which today adds and removes the grant,
   becomes the Admission select. All three leave the Legacy
   capabilities checklist.
4. The docs that describe them as grants are updated:
   [durable-tasks.design.md](durable-tasks.design.md) (DT-1, DT-7, and the sessions
   section's "a task with the `durable` grant") and
   [capabilities.md](../../docs/capabilities.md).

This is two tasks, not three. `admission` is one task because
`priority-dispatch` and `durable` share every predicate. `hosting` is
the other.

## 5. The UI

One **Grants** section on the new task overlay, the task pane, the
template editor and the schedule editor (`GrantsPicker.jsx`).

- **One list of sections**, everything held, in held order. A grant
  with nothing to set is one line: API grants read `api:ci:write` or
  `api:ci:write on acme/widgets` with their summary; a method without
  `with:` reads its server and method. A method with arguments is a
  section with its `CapabilityArgsForm` inline -- its options always
  visible and edited in place -- and a header naming the method, its
  ref and description, "Add another" on the method's last section, and
  ×. Repo access will read `acme/widgets · main · write`. Each grant has
  the warning states method grants have: *unknown* (no server offers
  it, or no longer offered), *no longer validates*, *normalizes
  differently*. Dangerous API grants are badged. × removes a grant.
- **Auto-save, no dialog.** A section saves itself like the rest of
  the task pane: an edit that passes `validateValue` is sent as
  `onSubmit({capability, with}, replacing)` 500 ms after the last
  change, or at once on blur, one save at a time per section, each
  replacing the grant the previous one recorded. An edit that fails the
  live check, or that the server refuses (`err.fields`), is marked
  against its field and not saved.
- **One "Add grant…" search** over every kind, grouped: Grain API presets,
  Grain API scopes by area, capability server methods, and (slice 5)
  repository access. A method is granted at once when its defaults
  (`initialValue`) already pass; one with a required argument still to
  fill in is a draft section in the list until it passes, then granted.
  The API repository limit is edited inline via `with: {repo}` on each
  `https://grain.dev/capabilities/api#<what>` section alongside
  capability server methods (the former "Grain API grants apply to"
  select was retired in slice 3, [Task #3317](/tasks/3317)). The menu
  re-reads `GET /api/capability-methods` every time it opens (task 1056).
- **Repo access** becomes a grant whose form picks repository, branch
  and read/write. The target repository stays its own field at the top
  of the form (it decides the branch, the clone and the pull request),
  and shows as a non-removable `#target` line (Q2).
- **Legacy capabilities** remain a separate checklist under it, labelled
  as legacy, until §4 empties it.

Task 885 (a dedicated Capabilities tab in Settings) owns the Settings
side: servers, policy and the deployment/repo default lists. This design
covers the per-task editors only. Settings' default-capabilities lists
should adopt `GrantsPicker` once 885 lands, which is a follow-up there.

## Slices

1. **UI only (this change).** One Grants section on the four editors.
   API scopes and server methods are in one menu and one chip list, the
   in-process checklist is relabelled "Legacy capabilities", and every
   kind is written to the field it is stored in today. Repo reads/writes
   are not moved yet. They are separate controls in the same form,
   placed beside the repository they qualify.
2. **Backend read-both.** `model.Grant.With`, `CanonicalGrant`, in-process
   `…/api` and `…/repo` method listings, the approval predicates over
   references, the `grants:` request field and the fidelity test
   fixture.
3. **Write one.** References everywhere, a release after 2.
4. **Migrate once,** with the fidelity test, including grain/config export.
5. **Repo grants in the picker.** `…/repo#read`/`#write` chips and form,
   `ReadOnlyReposField`/`WriteReposField` retired, `push:` shown
   read-only on template-filed tasks. Implemented in
   [Task #3322](/tasks/3322); see below.
6. **Per-capability conversions** from §4, one task each.

### Slice 5 as built

- **Still stored as reads and writes.** `GrantsPicker` takes a `repos`
  prop (`ui/src/components/RepoGrants.jsx`) with the form's target,
  base, reads and writes, and hands every edit back as the whole next
  reads or writes list, the way the retired fields did. Nothing about
  `POST /api/tasks`, `PATCH /api/tasks/{id}` or the template and
  schedule bodies changed; `model.TaskRepoGrants` is still how a reader
  that takes grants sees them.
- **Lines.** The task's own repo and base is a `…/repo#target` line
  (`<owner>/<repo> · <base> · target`) with no remove button: the repo
  and base branch fields stay at the top of the form (Q2). Each read is
  `<owner>/<repo> · read` with ×; each write is `<owner>/<repo> · write`
  with ×, its base branch edited in place under it, and named
  `<owner>/<repo> · <base> · write`. A template-filed task's declared
  `push:` refs (`declaredPush` on the task) are read-only
  `<repo> <ref> · push` lines in the task pane.
- **Add grant….** A "Repository access" group, first in the menu,
  offers "Read a repository" and "Write a repository". Either opens a
  repo picker in the list (it does not offer the target, or a repo
  already read, to read; or a repo already written, to write -- the
  target on another branch is a write of its own). The listed
  `…/repo#read` method is a CI job's grant and is left out of the
  menu where the form edits reads; `…/repo#write` stays listed for
  `{refs, force}` widenings (Q3).
- **Where.** The new task overlay (its `reads` and `writes` form fields
  keep their ids, visibility and defaults in
  `settings/new-task-form.yml` but draw nothing of their own; a hidden
  one is left out of the list), the task pane's Access section (the
  Repos section keeps only the target and base), the template editor,
  the schedule editor (where the form edits reads and writes at all),
  and the director's console session page (no target line). Settings'
  default-capabilities lists already use `GrantsPicker` and carry no
  repository access, so they take no `repos`.
- **Audit (Q5).** A `PATCH /api/tasks/{id}` that changes reads or writes
  records one `grant-attached` or `grant-detached` task event per grant
  it adds or removes, its detail the grant's canonical string
  (`Grant.Key`: `https://grain.dev/capabilities/repo#write={"base":"publish","repo":"acme/docs"}`).
  Capability edits through `POST /api/tasks/{id}/capabilities` record
  the same two kinds.

## Open questions for the operator

- **Q1.** Reference spelling: `https://grain.dev/capabilities/api#ci:write`
  (recommended: one recogniser, matches the built-in servers) or
  `grain:api/ci:write`?
- **Q2.** Should the task's target repo and base become a stored grant
  (`…/repo#target`), or stay their own columns and only be *shown* as a
  grant? Recommended: stay columns. Too much keys off `Target` (branch
  naming, clone, PR, merge queue, auto-merge) to route it through a grant
  list.
- **Q3.** `git-push-any-ref` as a `#write` grant with wide `refs`: should a
  task-level (non-declared) `refs` ever be honoured, if granted by an
  admin? Recommended: yes, but only as an admin-only dangerous grant, so
  declarations stay the only non-admin route.
- **Q4.** Should an unlimited api grant cover a repo-limited one in the
  subset check (`…/api#ci:write` covers `…/api#ci:write {repo: x}`)?
  Today it does not. Recommended: yes, since it is strictly narrower,
  but as its own change after migration so the fidelity test can pin
  today's behaviour first.
- **Q5.** Should repo reads/writes edits produce the same grant
  attach/detach audit events as capabilities once they are grants?
  Recommended: yes. **Done in slice 5 ([Task #3322](/tasks/3322)):**
  `grant-attached` / `grant-detached` task events, for both.
- **Q6.** `gemini-key`: model-proxy (`{vendor: gemini}`) or a `gcp`
  server method?
  **Answered (task 1061, operator): a `gcp` server method.** Folding it
  into model-proxy would hand the run a proxy token instead of a key,
  which is a different placement.
- **Q7.** Is `…/grain#host-exec` (today's `self-repair`) a grant at all,
  or should it stay an admin-only setting outside the picker?
  **Answered (task 2252, operator): a grant.** It is a dangerous method
  only a person may give; the per-command live confirmation and the
  Interactive-only rule are what keep it safe, and both stay.
- **Q8.** `console` is how a run is hosted, not access. Should it move out
  of grants into a task setting?
  **Answered (2026-11-04, operator): yes, and `priority-dispatch` and
  `durable` with it.** None of the three reaches anything. They become
  the `hosting` and `admission` task settings (§4.1).
- **Q9.** Retirement order for §4. Proposed: the api-alias ones first
  (no new code), then the model proxies and `gcp-key` (servers exist),
  then the new `…/grain` server.
- **Q10.** Does `admission` need its own "who may set it" restriction?
  Today the catalogue can limit `priority-dispatch` and `durable` to
  certain repos, templates and kinds (`CatalogueInProcess`, checked by
  `catalogueEditRefusal`).
  **Answered (2026-11-04, operator): no.** No deployment sets anything
  but the default for these. The person-only rule and `Limits.Durable`
  are enough, no `admission:` allowlist is built, and the migration
  drops the catalogue and policy entries for the three names instead of
  converting them.
