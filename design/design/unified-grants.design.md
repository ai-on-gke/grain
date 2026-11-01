# Unified grants: one grant model, one Grants picker

Status: design, with slice 1 (UI only) landed alongside it (task 1060).
Operator direction of 2026-10-26: everything a task is given should be
the same kind of grant, edited in one place in the UI.

## Where we are

A task carries four kinds of access today. Each has its own storage, its
own checks and its own form control:

| Kind | Spelling today | Stored in | Edited by (before slice 1) |
|---|---|---|---|
| Grain's own API | `api:<scope>`, `api:<preset>`, either `@owner/repo`-limited ([agent-api.md](../../docs/agent-api.md)) | `task_grant.capability` (the capabilities list) | "Grain API" accordion, `ApiGrantPicker` |
| Capability server methods | `https://…#method` plus `with:`, stored as `<ref>=<normalized json>` ([capabilities.md](../../docs/capabilities.md), [capabilities-spec.md](../spec/capabilities/capabilities.spec.md)) | `task_grant.capability` | "Capability server methods", `CapabilityMethodGrants` |
| In-process capabilities | bare names: `gcp-key`, `self-debug`, `<vendor>-proxy`, … (`pkg/capability/`) | `task_grant.capability` | the "Capabilities" checklist |
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
| bare `gcp-key`, `self-debug`, … | unchanged while they exist (they are read as legacy, see §4), then converted, one by one, to a reference |
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
| **Sandbox pass** (`MaterializeGrants`) | Nothing placed; `api_*` tools are served to the run (`agent.RunConfig.Grants`). | `setup_root`/`setup`, `mcp_endpoint` wiring, as today. | `#target` cloned to `/repo`. `#read`/`#write` reachable through the proxy for clone. Nothing else placed. |
| **Prompt** | "grain api" tool section, as today. | Method's own prompt text, as today. | The repository section and `writeTargetsSection` (run.go), generated from the grants rather than from three fields. |
| **Git proxy ref policy** (`AuthorizeRefUpdates`) | none | `git#git-repo` grants, as today | Push to the task's own branch of `#target`, and nothing else, as today. `refs` widen it **only** when read from the declaration on the repository's default branch (`Store.GitDeclaredPush`), never from a copy on the task row. A `refs` value a person or agent types onto a task is refused at write time. `git-push-any-ref` becomes `#write` with `refs: ["refs/heads/*", "refs/tags/*"]`, which `ValidateRefPushPattern` today refuses for declarations, so it is admin-granted only (Q3). |
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
- **Slice 3 (write one).** Writers switch to references. This is the
  first change an older binary cannot read, so it waits a release behind
  slice 2 (the `main`/`candidate` pairing gives that for free).
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
- **State-repo export (grain/config).** The exported YAML gains
  `grants:` lists. `capabilities:`, `reads:` and `writes:` keep being
  read (`declschema`), and for a release keep being *written* beside
  `grants:`, so a grain/config shared by two versions round-trips. The
  import merge
  treats the two spellings as one key.
- **Repository declarations.** `.grain/templates/*.yml` and
  `.grain/schedules/*.yml` accept `grants:` beside `capabilities:`,
  `reads:`, `writes:` and `push:` indefinitely. These live in other
  people's repositories, so nothing is migrated there, and `push:` stays
  the way a declaration widens refs.
- **API presets** need nothing. They become methods of `…/api`, and the
  alias table (`apiAliasCapabilities`) keeps `self-upgrade` and
  `pipeline-control` meaning their presets until §4 retires them.

## 4. Retiring the in-process capabilities

Operator decision (2026-10-26): the in-process capabilities stay **out**
of the unified picker, in their own checklist labelled **Legacy
capabilities**. They are converted, not merged as they are.
[capabilities.md](../../docs/capabilities.md) already says the providers under
`pkg/capability/` "stay in use until runs are moved over". This is the
per-capability list, with what each one becomes:

| In-process id | Becomes (proposed) | Kind of change |
|---|---|---|
| `gcp-key` | `https://grain.dev/capabilities/gcp#gcp-key` `{permission_set}` | existing server method ([gcp-key-permission-sets.md](../../docs/gcp-key-permission-sets.md)) |
| `gemini-key` | `…/model-proxy#model-proxy` `{vendor: gemini}`, or a `gemini-key` method on `…/gcp` | existing server; Q6 decides which |
| `<vendor>-proxy` (model proxies) | `…/model-proxy#model-proxy` `{vendor}` | existing server method |
| `github-credential:<name>` | `…/git#git-repo` `{credential: <name>, repo}` | existing server, gains an argument |
| `github-sandbox` | `…/github#sandbox-repo` | new method on existing `github` server |
| `git-push-any-ref` | `…/repo#write` `{repo, refs: […], force: true}` | repo grant (§1, Q3) |
| `local-scripts:<name>` | `…/host-script#host-script` `{script}` | existing server method |
| `repo-read:<repo>`, `repo-write:<repo>` (CI) | `…/repo#read` / `…/repo#write` `{repo}` | repo grant |
| `self-debug` | `…/api#self-debug` preset (already an alias) plus a new `…/grain#source` method for the source checkout and metrics tools | api scope + new in-process server `…/grain` |
| `grain-log` | `…/grain#journal` | **done** (task 2037): provider removed, `https://grain.dev/capabilities/grain#journal` added on the in-process `…/grain` server (redaction kept, deliberately in no api preset), stored grants rewritten to `https://grain.dev/capabilities/grain#journal={}` by schema migration 80 |
| `deployment-health` | `…/api#deployment:read` plus `…/grain#health` for the host/sandbox probes | **done** (task 2037): provider removed, `https://grain.dev/capabilities/grain#health` added on the in-process `…/grain` server alongside `api:deployment:read`, stored grants rewritten to `api:deployment:read` + `https://grain.dev/capabilities/grain#health={}` by schema migration 81 |
| `self-repair` | `…/grain#host-exec` (each command still needs a live chat reply) | new method, dangerous (Q7) |
| `self-upgrade` | `…/api#self-upgrade` preset | **done** (task 2036): provider and its three tools removed, stored grants rewritten to `api:self-upgrade` by schema migration 75 |
| `pipeline-control` | `…/api#pipeline-control` preset | **done** (task 2004): provider and its two tools removed, stored grants rewritten to `api:pipeline-control` by schema migration 69 |
| `dispatch-control` | `…/api#dispatch-control` preset | **done** (task 2004): provider and its four tools removed, stored grants rewritten to `api:dispatch-control` by schema migration 68 |
| `test-quarantine` | `…/api#tests:write` (new scope), preset `test-quarantine` | **done** (task 2036): provider and `quarantine_test` removed, `POST /api/tests/quarantine` added under `tests:write` and `grain quarantine` gated on it, stored grants rewritten to `api:test-quarantine` by schema migration 76 |
| `read-issues` | `…/github#read-issues` | new method on `github` server |
| `release-notes` | `…/api#release-notes` preset (`release-notes:read` + `release-notes:write`) | **done** (task 2035): provider and its tools removed, `/api/release-notes*` routes moved to `release-notes:read`/`release-notes:write`, stored grants rewritten to `api:release-notes` by schema migration 72 |
| `memory` | `…/api#memory` preset (`memory:read` + `memory:write`) | **done** (task 2035): provider and its tools removed, stored grants rewritten to `api:memory` by schema migration 71 |
| `team-broadcast` | `…/api#teams:broadcast` (new scope), preset `team-broadcast` | **done** (task 2004): provider and `broadcast_team_message` removed, stored grants rewritten to `api:team-broadcast` by schema migration 67 |
| `bootstrap-playbooks` | `…/skill#skill` `{dir: bootstrap}` | existing server method |
| `console` | stays a task setting, not a grant (it changes how the run is hosted, not what it may reach) | moved out of grants (Q8) |
| `db-read`, `db-write` | `…/grain#sql` `{write: bool}` | **done** (task 2037): providers removed, `https://grain.dev/capabilities/grain#sql` (`{write: bool}`, with `write: true` dangerous) added on the in-process `…/grain` server, stored grants rewritten to `https://grain.dev/capabilities/grain#sql={"write":false}` / `{"write":true}` by schema migration 82 |
| `grain-ci` | `…/grain#ci` | new method |
| `image-signing`, `registry-push`, `package-publish` (CI) | `…/release#image-signing`, `…/release#registry-push`, `…/release#package-publish` | new in-process server `…/release` |
| `corp-research` (google build) | a `…/corp#research` method registered by the google build through the same `capability.Register` seam | contributed server method |

How `self-debug`, `grain-log` and `deployment-health` style grants fit:
they are grain reading itself. Each becomes either api scopes (anything
that is a REST route already) or methods of one in-process `…/grain`
server for the parts that are not routes: the source checkout, the
journal and host probes. That server is answered by the daemon, exactly
like `…/api` and `…/repo`. `grain-log` stays separate from `self-debug`
for the reason its description gives: the journal is a different
exposure. Converting one capability is one task. It adds the
method, makes `CanonicalGrant` map the bare id to it, migrates its rows,
and deletes the provider. Until then, the bare id keeps working and
stays in the legacy checklist.

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
  The API repository limit is the "Grain API grants apply to" select
  until api grants carry `with: {repo}`. Then it becomes their argument
  form like any other. The menu re-reads `GET /api/capability-methods`
  every time it opens (task 1056).
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
   read-only on template-filed tasks.
6. **Per-capability conversions** from §4, one task each.

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
  Recommended: yes.
- **Q6.** `gemini-key`: model-proxy (`{vendor: gemini}`) or a `gcp`
  server method?
- **Q7.** Is `…/grain#host-exec` (today's `self-repair`) a grant at all,
  or should it stay an admin-only setting outside the picker?
- **Q8.** `console` is how a run is hosted, not access. Should it move out
  of grants into a task setting?
- **Q9.** Retirement order for §4. Proposed: the api-alias ones first
  (no new code), then the model proxies and `gcp-key` (servers exist),
  then the new `…/grain` server.
