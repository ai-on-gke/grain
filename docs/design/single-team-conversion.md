# Converting a single-team install into a single-tenant Grain

> **Historical.** The Silo hub this design registers with was removed
> from grain on 2026-10-19 ([../hub-removed.md](../hub-removed.md)), and
> docs/design/grain-silo.md with it. The hub sections below describe code
> that no longer exists.

Status: design, for the owner's review. Written for task 4234 (origin
task 4193). No requirement IDs yet: they are added once the owner
approves the design (docs/design-docs.md), and the planner files work
from them after that.

A **Grain** is single-tenant and has one backlog. It is the Policy
Enforcement Point: it holds every task to a local policy that only its
owner can edit (docs/local-policy.md). **Silo** is the multi-tenant hub
and Policy Decision Point (bwsalmon/silo `docs/hub.md`,
`docs/hub-interface.md`; docs/design/grain-silo.md). Task 4175 removes
multi-user accounts, sessions and team RBAC from grain. The target format
in this document is grain as it will be after 4175, together with the
flags and claims from 4176 (`-policy`, `-repo`, `-sandbox-backend`,
`-model-backend`, `grain create -author/-team`, signed claims). The hub
side is tasks 4165 and 4172–4174.

This document designs one thing: how an existing grain install that
serves **one team** becomes a Grain in that format, and how it can then
register with Silo if its owner wants it to. It ends with a staged plan.

## Scope

**In scope:** a well-defined, repeatable, reversible conversion of one
team's install.

**Out of scope:** keeping multi-team support, a mode flag for it, and a
graceful migration of a multi-team deployment. A multi-team install
(this deployment's grain/config, with eleven teams, is one) is converted
one team at a time, or by hand. Each team becomes its own Grain:
`grain convert -team X` runs against a copy of the data directory for
each team, keeps that team's settings, repositories and open work, and
reports the rest as dropped. Every Grain after the first gets a state
repository of its own. Nothing in this design tries to keep the teams
in one process, or to share a merge queue between the Grains that come
out. For the people involved, Silo becomes the place where the teams
meet.

---

## 1. What a single-team install holds today

"Single-team" means that once archived teams and teams with no
settings, tasks or members are left out, exactly one live team remains.
That team is usually `default`. Sometimes it is a team created in the
UI next to the seeded `default`, which is left empty. `pkg/model` and
this deployment's export (grain/config `tables/`, schema version 17) show
where each piece lives.

| Data | Where it lives | Team-scoped by |
|---|---|---|
| Sign-in accounts | `user` (password hashes) | — |
| Sessions | none at rest: signed JWTs (`ui.CreatePasswordSessionToken`), bound to the account's password hash, in a cookie | the account |
| Access list | `access_accounts` (grain/config has one `user` entry) | — |
| Deployment admins and sign-in settings | `grain_config.admin_accounts`, `password_auth_enabled`, `open_registration`, `google_oauth_*`, `iap_backend_service`, `iap_resource` | — |
| Team rows | `team`: limits (`max_workers`, `max_mergers`, `max_ci_jobs`, `merge_batch_size`), model choice (`default_model`, `model_settings`, the legacy per-framework columns), `agent_framework`, `agent_backend_id`, `time_zone`, `git_credential` / `git_connections`, `agent_git_name` / `agent_git_email`, `target_repos`, `repair_template_id`, `anyone_may_propose`, `require_cross_team_approval`, `personal`, `archived`, `onboarding_dismissed` | its own `id` |
| Team RBAC | `team_user(team_id, user, role, admin)`: `admin`, `member`, `proposer`, `viewer` | `team_id` |
| Per-team repository settings | `team_repo_config` (clone URL, capabilities, CI capabilities, prompt extension, setup command, CI mode, git credential, repair template, release secrets) | `team_id` |
| Per-team GCP keys | `team_gcp_key` | `team_id` |
| Connections | `grain_config.agent_backends[]` (`team` on each entry); team git connections name secrets in `<data-dir>/secrets` as `grain-team-<base64(team)>[-…]` (`model.TeamGitCredentialName`) | `team` / the secret's name |
| Capability scoping | `grain_config.capability_teams` (grain/config: `{"gcp-key":["bouncer"]}`) | team list |
| Pipeline pauses | `team_pipeline_pause` next to the deployment-wide `pipeline_pause` | `team_id` |
| Repositories | `repo_config`, `repo_declaration*`, `repo_path_trigger`, `repo_merge_rules`, `repo_workflow` | none: keyed by `owner/name` |
| Schedules, templates, suites | `schedule`, `template`, `suite` and their `_read`, `_write`, `_grant` and `_custom_field` tables; in the state repository, `settings/*.yml` (every schedule here says `team: default`) | `team` column |
| Boards, custom fields | `board`, `board_column`…, `custom_field` | `team` column |
| Memory | `memory`, `memory_document`; in the state repository, `memory/teams/<team>/…` | `team` column, plus a repository |
| Tasks | `task` (`team`, `creator`, approval), `task_*`, `task_run*`, `task_comment`, `lease`, `work_origin*` | `team` column (a claim, see §2.1) |
| Pull requests and branches | `task.branch` (stored since schema 17), `branch`, `release*` | the repository |
| Merge queue | `merge_batch`, `merge_*_history`, `merge_queue_contention`, `base_branch_ci`, `base_branch_dispatch`, `base_branch_release`, `force_merge_history` | the repository |
| CI | `ci_run` (`team`), `ci_job`, `ci_step*`, caches | `team` (history) |
| v2 executors | `executor_token` (`team`) | `team` |
| Audit | `secret_resolution_audit`, `credential_access_audit` (`team_id`) | `team_id` |
| Cross-team approvals | `cross_team_dependency_approval` | `target_team` |
| State repository | `grain/config`-style: `settings/`, `tables/` (including `team.json`, `team_user.json`, `team_repo_config.json`, `team_gcp_key.json`, `access_accounts.json`), `memory/`, `release-notes/`, `schema-version` | per file |

What single-tenant grain already does differently on `main`
(`-tenancy=single`; aab05ab03, 4e944ea7d, 3ef8746dc) matters for the
conversion:

- Every task's credential, commit identity and provider connections
  resolve as the **`default` team's**, and fall back to the daemon's own
  credential ladder.
- Team limits (`Team.MaxWorkers` and the rest) are **ignored**. Only the
  deployment-wide limits in `grain_config` hold work back.
- Nobody signs in. The access code (`<data-dir>/local-api-token`) is the
  one credential, and the account routes answer 404.
- `Task.Team`, `Task.Creator` and `Task.Approval` are left as they are,
  and still reach the commit trailers and the policy check.
- `-tenancy=auto` resolves to `multi` on any store that has accounts,
  team members, access-list entries, a team besides `default`, a system
  admin besides the daemon's own actor, or Google sign-in
  (`Store.ResolveTenancy`).

So a team's settings only take effect in the new format if they sit
where single mode reads them: the `default` team row and `grain_config`.

## 2. The conversion

The one team is called **T** below. `default` is the seeded team.

### 2.1 Kept as it is

- **Repositories:** `repo_config`, the declaration tables, path
  triggers, merge rules and repository workflows. They have no team key.
- **Tasks, with their history:** every `task` row and its children,
  including `task.team`, `creator` and the approval. In the new format
  these fields are claims (docs/local-policy.md, "Metadata carried
  through"), not access control. Rewriting them would falsify the
  `Requested-by:`/`Approved-by:` trailers and the derived-task walk
  (`model.Task.DerivedFrom`). Tasks of other teams (archived ones, or a
  dropped second team's) are kept as read-only history, and the report
  counts them.
- **Branches and open pull requests:** `task.branch` is stored, so an
  open pull request keeps its `grain/<team>/task-N` name, and
  `task_branch_template` (`grain/eu/task-{id}` here) carries on for new
  tasks.
- **The merge queue:** lanes and batches are keyed by repository, so
  they carry over unchanged. In-flight batches are dealt with in §3.4.
- **CI history and caches, release notes, audit tables, standing
  sandboxes, qualifications.**
- **`executor_token`:** v2 executors still connect until phase 6 of
  grain-silo.md. Their `team` is rewritten to `default` (§2.2) so that
  they keep dispatching.

### 2.2 Collapsed: T's settings become the Grain's

When T is not `default`, the conversion folds T into `default` and
deletes T's row. `default` stays as the one settings row, because that
is what single mode reads (§6 asks 4175 to keep it). Field by field:

| From | To | Rule |
|---|---|---|
| T's model choice, `agent_framework`, `agent_backend_id`, `time_zone`, `agent_git_*`, `repair_template_id`, `target_repos` | `default` | T wins wherever `default` is empty. Where both are set and they differ, T still wins, and the report lists each such field. |
| T's `git_connections` / `git_credential` | `default` | The secrets are copied to `default`-prefixed names (`TeamGitCredentialName("default")…`) and the connections are repointed. The `grain-team-<T>` files are kept, and only removed when the next conversion runs with `-prune`. Otherwise `TeamOwnsGitCredential` would read a connection that names T's secret as one only an admin may point at. |
| T's `max_workers`, `max_mergers`, `max_ci_jobs`, `merge_batch_size` | `grain_config` | Single mode ignores team limits, so a limit T set would be lost without warning. The tighter of T's and the deployment's value is kept, and the report says so. |
| `team_repo_config` rows for T | `team_repo_config` for `default` | Rekeyed. A collision with an existing `default` row is resolved by the rule in the first row. |
| `team_gcp_key` for T | `team_gcp_key` for `default` | Rekeyed. |
| `team_pipeline_pause` for T | `pipeline_pause` | A paused team becomes a paused Grain. |
| `agent_backends[].team == T` | `team: ""` | The connection becomes deployment-wide. Entries naming any other team are dropped, and the report lists them. |
| `capability_teams` entries | removed | A capability whose list includes T becomes plainly available. One whose list does not include T is denied in the starter policy (§2.5). |
| `team` column on `schedule`, `template`, `suite`, `suite_run`, `board`, `custom_field`, `memory`, `memory_document`, `ci_run`, `executor_token` | `default` | Rows that already say `default` are left as they are. A memory row present under both keys keeps both texts, joined with a separator, and the report lists it. |
| `onboarding_dismissed`, `description`, `name` | `default` | The team's name becomes the Grain's display name. |

When T is `default`, only the limits, the `capability_teams` entries, the
agent backends and the pause rows need any work.

### 2.3 Dropped

- `user`: every sign-in account and its password hash. Every session
  token dies with it: none is stored, and each one is bound to a hash
  that no longer exists.
- `team_user`, `access_accounts`.
- `grain_config` sign-in settings: `password_auth_enabled`,
  `open_registration`, `google_oauth_*`, `iap_backend_service` and
  `iap_resource`. The Google OAuth client secret is deleted from the
  secrets directory. `admin_accounts` is gone (schema 26, task 4880):
  whoever holds the access code is the owner.
- T's `anyone_may_propose`, `require_cross_team_approval` and `personal`.
  In the new format these become decisions for Silo (who may file) and
  for the local policy (whose tasks need approval).
- `cross_team_dependency_approval` rows: there is no other team left.
- Other teams' rows and their settings, when the install had more than
  one team (the multi-team paragraph above).

Before anything is dropped it is written to the backup (§3.3), and the
membership becomes the hub manifest (§2.4). Nothing is lost that could
not be put back.

After the conversion, `ResolveTenancy(auto)` on the converted store
returns `single`. That is the conversion's postcondition, and §5 tests
it.

### 2.4 To the hub, if the Grain registers

Silo's directory (hub.md §5.3) holds `teams(id, name)`,
`team_members(team, principal, role: member | admin)` and
`team_repos(repo, team, history)`. The conversion writes
`<data-dir>/convert/hub-directory.json`:

```json
{
  "team": {"id": "eu", "name": "grain"},
  "members": [
    {"principal": "user:alice@example.com", "role": "admin"},
    {"principal": "user:bob@example.com", "role": "member"}
  ],
  "unmapped_members": [
    {"principal": "user:carol@example.com", "grain_role": "proposer"}
  ],
  "anyone_may_propose": false,
  "repos": ["bwsalmon/grain", "gke-labs/bouncer"],
  "node": {"id": "<-hub-node-id>", "repos": ["bwsalmon/grain", "gke-labs/bouncer"]}
}
```

It also writes the directory calls that would apply the manifest
(`POST /v1/teams`, `PUT /v1/teams/{team}/members/{principal}` and
`PUT /v1/teams/{team}/repos/{owner}/{name}`), so the right person can
run them.

**The conversion does not call Silo.** Creating a team and assigning a
repository are hub-admin operations (SA1-M2), and the Grain's owner is
not usually a hub admin. A tool on the enforcement side that wrote the
decision point's data would also put the decision on the wrong side of
the trust boundary (grain-silo.md §5.4). The manifest is a request. A
hub admin applies the team and repository rows. A team admin applies the
membership. Applying the manifest is an operation on Silo, and it can be
repeated.

Mapping details:

- `admin` maps to `admin`, and `member` maps to `member`. Silo has no
  `proposer` or `viewer` role today. Those members go in
  `unmapped_members`, and the report says they lose access until Silo
  can express their role. `anyone_may_propose` is carried as a flag in
  the manifest for the same reason. This is open question 1.
- The repositories come from T's `target_repos`, plus the `repo_config`
  and `team_repo_config` repositories that T's tasks, schedules or
  templates targeted. They are the policy's `repos` with the globs left
  out (§2.5). Silo admits a node only if it may serve **every** repository
  it registers (hub.md §5.3), so the manifest's `node.repos` list is
  exactly what has to be assigned before the Grain first connects.
- Registration itself is unchanged: `grain hub login`, then
  `grain daemon -hub-url … -policy …`. The repositories the node
  registers default to the policy's repositories (`-repo`, 4176).

### 2.5 A starter local policy

The conversion writes `<data-dir>/grain_policy.yml`. It never overwrites
an existing file: it writes `grain_policy.converted.yml` beside it and
reports the difference. Each key is derived as follows:

| Key | Derived from |
|---|---|
| `repos` | T's `target_repos`; every repository a non-terminal task of T's, or any schedule or template, targets, reads or writes (`task_read`, `task_write` and the `schedule_*` and `template_*` tables); `team_repo_config` for T; every `repo-read:`/`repo-write:` grant they hold. These are exact names, and the conversion never writes a glob. |
| `capabilities.allow` | The union of `repo_config.default_capabilities` and `ci_capabilities` for those repositories, the grants on T's schedules and templates, `grain_config.default_capabilities`, and every grant held by T's tasks over the last 30 days (`task_grant`). `capability_teams` entries that list T are included. |
| `capabilities.deny` | `capability_teams` entries that do not list T. Nothing else. |
| `network` | The network accesses T's tasks and `grain_config.network_access` actually used. |
| `local_requesters` | The converting operator (`-owner`, required), plus T's admins if `-local-requesters=admins` is passed. |
| `approvers` | T's `admin` and `member` principals: the people whose approval counted on T. |
| `silo_issuer`, `silo_keys_url` | Written commented out. The owner turns them on at registration. |

Every line of the file carries a comment saying where it came from, so
an owner can narrow it with confidence. The dry run (§3.2) lists the
queued tasks that this policy would hold back with `waiting: local
policy …`, so a wrong derivation shows up before anything has changed.

## 3. The mechanism

### 3.1 Recommendation: an explicit `grain convert`, built on the schema-upgrade pipeline

There are two candidates:

- **A migration that runs on upgrade**, so that the post-4175 build
  converts whatever store it finds. This is rejected. Conversion
  removes people's access and makes judgements (which team is T, whose
  approvals count, what the policy allows), and the owner has to see
  those first. Upgrades also happen unattended: through the UI's
  Upgrade button, `scripts/setup.sh` and scheduled upgrades. An install
  that turned out to have two live teams, or work in flight, would then
  convert halfway with nobody watching.
- **`grain convert`, a command the operator runs with the daemon
  stopped.** This is recommended.

`grain convert` reuses the machinery that already moves a store across
a schema change (`cmd/grain/schemaupgrade.go`,
`grain state prepare-upgrade`). That pipeline exports the whole database
as a staterepo dump, rehearses the import against the new schema
(`grain state check -migrate`), moves the old store aside under a
timestamped name without deleting it, and imports the dump into a fresh
store. Conversion is one more transform between the export and the
import. That gives the backup, the rehearsal and the atomic swap for
free, plus a way for the same build to carry the state repository
across (§4).

The post-4175 daemon refuses to start on a store that still holds
multi-tenant data, using the same tests `ResolveTenancy` uses today. It
tells the operator to run `grain convert`, and does not start empty.
The refusal is what lets upgrades stay unattended: an unconverted
install stops at a clear message instead of starting with nobody able
to sign in.

```
grain convert -data-dir D [-team T] [-owner EMAIL] [-json]       # dry run: the default
grain convert -data-dir D -team T -owner EMAIL -apply             # convert
grain convert -data-dir D -rollback                               # put the pre-conversion store back
```

### 3.2 The dry-run report

A dry run is the default, and it reads without writing. `-json` gives
the same report as JSON for tests and for the UI. It shows:

- the tenancy the store resolves to now, and why;
- every team, which one is T and why (or, with no `-team` flag, why
  none could be chosen);
- for each table, how many rows are kept, rekeyed, merged or dropped;
- every settings collision from §2.2, with both values and the one that
  would win;
- the secrets that would be copied, and agent backends and capability
  entries that would be dropped;
- the accounts that would be dropped, with their roles;
- the starter policy, and each queued or parked task it would hold back;
- the hub manifest;
- **blockers:** work in flight (§3.4), a second live team without
  `-team`, open pull requests against the state repository, and a store
  at a schema this build cannot read.

`-apply` runs the same planning code. It refuses while any blocker is
left, and prints the report before it changes anything.

### 3.3 Backup, idempotency and rollback

- **Backup first.** `-apply` refuses to run while the daemon holds the
  store (it takes the data directory's lock). It then writes the
  schema-upgrade export into `<data-dir>/convert/<timestamp>/` along
  with the dry-run report, the hub manifest and a copy of the secrets
  it is about to touch. Only then does it move the SQLite store aside
  (`store.pre-convert-<timestamp>`). A `grain backup` to GCS or Drive
  first is recommended in the report, but not required.
- **The swap is atomic.** The converted store is built in a temporary
  directory and renamed into place. If conversion fails at any step, the
  original store is left in place and was never written.
- **PostgreSQL** (docs/cloudsql-postgres.md): there is no store to move
  aside, so the export is the backup. The conversion then runs as a
  single transaction against the live database, and `grain backup` is
  required (not just recommended) before it.
- **Idempotency.** A converted store records
  `converted: {team, at, from_schema, grain_version}` on its
  `grain_instance` row. Running `grain convert` again on a converted
  store reports "already converted", writes nothing and exits 0. A store
  with no legacy rows and no marker (a new install) gets "nothing to
  convert". Because of the atomic swap, a failure in the middle leaves
  nothing half converted to resume from. Re-running starts over from
  the untouched original.
- **Rollback.** `grain convert -rollback` moves the converted store
  aside and the newest `pre-convert` store back, and puts back the
  secrets it copied. For the state repository, it reverts the
  conversion commit (§4). Rollback is only possible with the
  pre-conversion grain build, because the post-4175 build refuses an
  unconverted store, and the report says so. Work done on the converted
  Grain between conversion and rollback (new tasks, merged pull
  requests) is not carried back. The rollback report lists it from the
  converted store before that store is set aside.

### 3.4 Work in flight

The conversion runs with the daemon stopped. Run hosts survive a daemon
restart (`runhost` adoption), but adopting a run across a store that was
rebuilt from an export is not something grain does, and this design does
not add it. So the procedure is to drain work first:

1. `grain pause -set` closes the pipeline gate (`pipeline_pause`), so
   the deployment picks up no new work.
2. **Running tasks** finish, or are cancelled. The dry run blocks on any
   `task_run` that has not ended, and names each one.
3. **Merge-queue lanes** finish the batch they are testing or merging.
   The dry run blocks on any `merge_batch` that is not terminal. A
   gathered batch that has not started yet is not a blocker: it is
   dropped, and its pull requests return to the queue, which gathers them
   again after the conversion.
4. **CI runs** finish or are cancelled. A cancelled CI run is reported,
   and the merge queue re-runs it after the conversion.
5. The operator stops the daemon, runs `grain convert -apply`, and
   starts the daemon with `-tenancy=single -policy <data-dir>/grain_policy.yml`
   (or `-tenancy=auto`, which now resolves to single). The pause carries
   over, and the owner lifts it (`grain pause -resume`) once the first
   cycle looks right.

The rest carries over as it is: queued and parked tasks, open pull
requests (each with its stored branch), proposals waiting for approval
(which T's former members can no longer approve, since nobody signs in,
so the owner approves them or Silo does through claims), schedules and
their next-run times, and base-branch repair holds.

## 4. The state repository

The install's export repository (grain/config here) stays the Grain's
state repository. Its contents change once, in one commit:

- `tables/team.json` keeps one row, `default`. `team_user.json` and
  `access_accounts.json` are gone. `team_repo_config.json`,
  `team_gcp_key.json` and `grain_config.json` are rewritten as in §2.2.
- `settings/*.yml` keep their entries, each with `team: default`.
- `memory/teams/T/…` moves to `memory/teams/default/…`. Git sees the
  move as renames.
- `schema-version` changes if the conversion build's schema differs.

The danger is the daemon's first start. Grain imports a merged state
repository over its database ("a deleted row is a deleted row"). A
converted store that first pulled the old export would bring T's rows,
the accounts and the members straight back. The conversion therefore
leaves the same kind of note the schema upgrade leaves. On first start
the daemon does not import. It exports the converted database over the
old dump and pushes the result in one commit
(`staterepo.Migrated` / `migrateStateRepo`, with a subject line like
"Convert to a single-tenant Grain: fold team T into default"). It tags
the commit before that one `pre-convert-<timestamp>`. The tag is how
rollback finds the commit to revert, and it is a ref on the install's
own repository.

Pull requests open against the state repository when the conversion
runs would be written against rows that no longer exist. The dry run
blocks on them. The operator merges or closes them first, because a
pull request merged afterwards would bring the old rows back.

The things the state repository already does not hold (tasks, runs, CI
runs) stay in the database, and the backup in §3.3 covers them.

For a multi-team install converted team by team, the original
repository goes with one team's Grain. Every other Grain starts a fresh
state repository from its own converted export.

## 5. Testing

**A conversion test over a fixture database** (`pkg/convert`,
table-driven, under `-shuffle=on`):

- The fixture is built through the store's own API at schema 17 in multi
  mode. Team `acme` is live and `default` is the seeded team, left
  empty. It has password accounts, `admin`, `member`, `proposer` and
  `viewer` members, an access-list entry, an `acme` git connection with
  its secret, `team_repo_config` and `team_gcp_key` rows, a team pause,
  an agent backend scoped to `acme`, `capability_teams` naming `acme`
  and another team, and schedules, templates, suites, boards, custom
  fields and memory under `acme`. Its tasks are queued, parked, done,
  holding a pull request, a proposal, and one derived from a hub task.
  It also has a terminal and a gathered merge batch, and the CI history.
- **Dry run:** compares the JSON report against a golden file, and checks
  that the store is byte-identical afterwards.
- **Apply:** checks every row in §2. Tasks keep their team, creator and
  approval. Settings rows read `default`. Limits are the tighter value.
  The secrets are copied, and the gathered batch is dropped and its pull
  requests requeued. Then `ResolveTenancy(auto)` returns `single`, and
  the converted store opens under the post-conversion build.
- **Starter policy and hub manifest:** golden files, plus a test that
  the derived policy refuses no queued task the fixture marks as
  expected to run.
- **Idempotency:** a second `-apply` writes nothing. A run on a new
  install reports "nothing to convert".
- **Failure and rollback:** a fault injected after the export (in the
  same style as `pkg/silo/facade` handler overrides, saved and restored
  in `t.Cleanup`) leaves the original store in place. `-rollback`
  restores a store whose export is byte-identical to the one taken
  before.
- **Refusals:** two live teams without `-team`, a running `task_run`, a
  merge batch that is testing, an open state-repository pull request,
  and the daemon's lock being held.
- **The state repository:** a local bare repository stands in for the
  remote. The first start after conversion pushes exactly one commit
  and imports nothing. The tag is in place, and `-rollback` reverts the
  commit.
- **PostgreSQL:** the same test runs on the `pkg/model/postgres`
  harness.

**An e2e check that a converted install runs a task** (in `cmd/grain`,
next to `daemon_true_e2e_test.go`, run in grain CI):

1. Build the fixture, convert it, and start
   `grain daemon -tenancy=auto -policy <generated>` against the
   fake forge and agent that the existing e2e harness uses. Check that
   the daemon logs `tenancy: single`.
2. With the access code, file a task against `acme`'s repository. It
   dispatches, clones through the git proxy with what was `acme`'s
   credential, pushes its branch, opens a pull request, and the merge
   queue merges it.
3. A task that was queued before the conversion dispatches as well. A
   task against a repository outside the policy waits with
   `waiting: local policy`.
4. A pre-conversion session cookie gets a 401.
5. **The hub:** against the fake hub that `cmd/grain/hub_test.go` and
   `pkg/hubnode` already use, the converted Grain's hello registers
   exactly the manifest's `node.repos`.

## 6. What 4175 must preserve

The conversion is only possible if 4175, which removes RBAC, accounts
and sessions, keeps the following. 4175 is queued behind 4156. Either
these points go into 4175's own scope, or 4175 waits for stage 1 below.

1. **No legacy table is dropped, and no legacy column is removed, on
   open.** `user`, `team_user`, `access_accounts`, `team` (every column
   above), `team_repo_config`, `team_gcp_key`, `team_pipeline_pause`,
   `cross_team_dependency_approval` and the `grain_config` sign-in
   columns have to stay on disk, as written, until the conversion has
   read them. 4175 may delete the Go code that uses them. The conversion
   reads them with its own row types (`pkg/convert/legacy`), not through
   `model.User` or `model.Team`. The tables can be dropped one release
   after the conversion has shipped.
2. **The daemon refuses to start on an unconverted store.** The checks
   `ResolveTenancy` makes today stay, as a start-up refusal that points
   at `grain convert`. 4175 must not turn an install with accounts into
   a single-tenant daemon silently, and must not start empty.
3. **A schema bump goes through the schema-upgrade pipeline**
   (`prepare-upgrade`, then the move-aside-and-import), and never
   through the `GRAIN_ALLOW_STORE_RESET` path. The pipeline's export has
   to include the legacy tables, and its import at the new schema has to
   keep them, even though nothing reads them any more.
4. **The `default` team row stays** as where the Grain's settings live,
   and so do the places single mode reads it: credential resolution,
   provider connections and commit identity. Alternatively, 4175 moves
   those settings into `grain_config`, and the conversion targets that
   instead. Either works, but 4175 has to choose one and write the choice
   down. The fold in §2.2 is written against "the `default` row".
5. **Secret names stay stable.** `TeamGitCredentialName` and
   `TeamRepoGitCredentialName` keep their encoding, so the conversion can
   find a team's secrets and copy them.
6. **The existing migrations stay callable.** `MigrateLegacyGitCredentials`
   and `MigrateModelSelection` run before the fold, so that an old
   install's `git_credential` and per-framework model columns are
   normalised first.
7. **`Task.Team`, `Creator` and `Approval` stay columns and claims**, as
   they already are in single mode. Removing RBAC must not remove the
   data.
8. **The state repository's import is held back** when a conversion (or
   schema-upgrade) note is present. That path exists today, and 4175 must
   not reroute first-start state handling around it.

## 7. Staged plan

Each stage ships on its own and has an exit test.

1. **What 4175 preserves.** Fold the eight points of §6 into 4175's
   scope, or land them first: keep the legacy tables, refuse to start
   on an unconverted store, and keep the schema-upgrade export complete.
   Exit: on a post-4175 build, an unconverted fixture store refuses to
   start with the `grain convert` message, and its export still holds
   every legacy row.
2. **`grain convert`, dry run only.** Choosing T, the per-table plan,
   collisions and blockers, as text and JSON. It is read-only, so it can
   ship before 4175, and owners can see now what conversion would do to
   them. Exit: the dry-run golden test and the byte-identical-store test.
3. **`-apply` and `-rollback` on SQLite.** The backup, the fold, the
   drops, the atomic swap, the `grain_instance` marker and rollback. It
   ships with or after 4175. Exit: the apply, idempotency, failure and
   rollback tests, and `ResolveTenancy(auto) == single`.
4. **The starter policy.** Derivation, provenance comments, the
   no-overwrite rule, and the dry run's list of tasks the policy would
   hold back. Exit: the policy golden test and the e2e steps 1–3.
5. **The state repository.** The conversion note, the single export
   commit on first start, the tag, the block on open pull requests, and
   rollback's revert. Exit: the bare-repository test.
6. **The hub manifest.** `hub-directory.json`, the directory calls, the
   role mapping and `unmapped_members`, and the default node repositories.
   Exit: the manifest golden test and e2e step 5.
7. **PostgreSQL.** The transactional apply and the required backup. Exit:
   the conversion test on the postgres harness.
8. **The full e2e and the docs.** The converted-install e2e in grain CI,
   and an upgrade note (docs/quickstart.md, the release notes) telling an
   owner to run `grain pause -set`, a dry run, `-apply` and start the daemon.
   Exit: the e2e is green in grain CI.

Stages 2–6 depend on stage 1 only where they need the post-4175 schema.
Stage 2 does not.

## 8. Open questions

1. **Proposers and viewers.** Silo's directory has `member` and `admin`
   only, but grain-silo.md §4.4.1 assumes `proposer` and `viewer` roles
   and an "anyone may propose" setting held by Silo. Until Silo has
   them, a converted team's proposers and viewers lose their access.
   Should the conversion wait for Silo, or ship and report them?
2. **Who counts as the owner.** `-owner` is the person whose machine and
   credentials the Grain runs on. On a shared VM with no single owner
   (grain-silo.md §7, item 2) it is probably a role account. Should
   `local_requesters` default to the team's admins in that case?
3. **The tighter-limit rule.** Keeping the lower of T's and the
   deployment's limits is the safe choice, but it could make a Grain
   that used to run alone slower. The other option is to take T's value
   and report it. Which does the owner want?
4. **History after a transfer.** If the repositories are assigned to a
   new Silo team, the Grain's existing tasks are local only. Silo never
   saw them. Should the hub manifest ask for `history: true`? Today that
   flag only concerns tasks Silo already holds, so it changes nothing
   for a newly registered Grain.
