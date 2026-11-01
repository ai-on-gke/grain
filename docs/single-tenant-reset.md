# Single-tenant reset

This is the plan for removing multi-tenant mode from grain (task 4626). It
covers what goes, what the team-scoped features turn into, the order the work
lands in, and how the one existing multi-tenant deployment is kept off the
result. Each stage is its own task and its own pull request. Each must be
green on grain CI and leave grain working. The stages work from this
document and update it as they land. The last stage deletes it once
`docs/data-model.md` describes the new table set.

The owner's decisions (2026-10-17) are the input:

- Grain is single-tenant only. These all go: password and Google accounts,
  sessions, teams, team membership and RBAC, admin mode, access lists,
  cross-team approval, team pipeline pauses, team grants, the team switcher
  and the `-tenancy` flag. The access-code sign-in and the loopback /
  CLI-token trust in `pkg/ui/single_tenant.go` stay, because they are how
  single-tenant grain is reached.
- The connections built for teams become the only implementation. They are
  keyed to the deployment instead of a team:
  - git connections: PAT, App, ambient gh (or a registered source's tool), and
    host-qualified hosts;
  - model connections: live discovery, per-model settings and per-model
    pauses.
- The older paths are removed, not kept beside the connections:
  - the `secrets/github/credentials.json` repository ladder;
  - `-github-host`-only token plumbing;
  - the deployment-wide legacy agent settings;
  - every other way of resolving a credential or a model.

  One file format survives, as an import/seed format only
  (`grain connections import <file>`). Grain never reads it at runtime.
- There is no upgrade path for a multi-tenant deployment. The eu host is
  retired, not migrated, and must not upgrade onto a stripped build.
- The database is reset to one clean single-tenant baseline schema, for
  SQLite and Postgres alike.

## Keeping the eu deployment off a stripped build

Every removal stage depends on this, not only the schema stages. A build
without teams cannot serve the eu host's teams, even before its schema
changes.

### How that deployment upgrades

This is what the tree shows at 0324dfc44:

- **Nothing upgrades a deployment on a timer.** An upgrade starts only from
  `POST /api/upgrade`. Three things call it:
  - the Upgrade pane's button;
  - a scheduled upgrade (`scheduled_upgrade`,
    `orchestrator.reconcileScheduledUpgrade`), which waits for the
    deployment to go idle and then starts the upgrade;
  - a task holding the `self-upgrade` capability
    (`pkg/capability/selfupgrade`).

  The upgrade *check* (`upgrade_check_mode`) only files a task after an
  upgrade has happened. It never starts one.
- **The target defaults to the deployment's channel**, which is read off the
  image reference it runs (`upgrade.ChannelForRef`). The eu host runs
  `candidate`, so "upgrade" means "the newest candidate". The daily cut
  (`scripts/release-candidate-cut.sh`) moves `candidate` to `main`'s head. A
  removal merged to `main` therefore reaches the eu host on the first
  upgrade after the next cut.
- **A restart alone pulls nothing new.** The ref file and the Kubernetes
  Deployment both record `repo:tag@sha256:...`, so a restart brings up the
  same digest.
- **The schema step can stop an upgrade.** It (`pkg/upgrade/schema.go`)
  imports a dump of the running deployment into the new build
  (`grain state check -migrate`) before cutting over. If that fails, the
  deployment is left untouched. The step only runs when
  `model.SchemaVersion` differs between the two builds.

### What the owner has to do before stage 2 merges

None of this is in this repository, so a grain pull request cannot do it:

1. **Pin the eu host to an immutable build.** Upgrade it to the `sha-<short>`
   tag of the candidate it runs now, or rewrite its ref file or Deployment
   image to that tag. `ChannelForRef` returns no channel for a `sha-` tag, so
   the pane and a scheduled upgrade have no "newer" build to offer. Reaching
   a stripped build then takes somebody typing a branch name on purpose.
2. **Remove `self-upgrade` from grain/config.** Take it off every template,
   schedule and team grant, in a pull request against grain/config, so no
   agent on the eu host can start an upgrade.
3. **Cancel any pending scheduled upgrade** on the eu host, from the Upgrade
   pane or with `DELETE /api/upgrade/scheduled`.

### What grain does about it itself (stage 5)

The startup guard refuses a data dir from before the reset. It runs in the
store's `Init` and also in `grain state check -migrate`, which is the check
the upgrade path already makes. An upgrade onto a stripped build therefore
fails at the pre-cutover import, and the eu host stays on the build it runs.

Stage 5 also bumps `model.SchemaVersion`. With the version unchanged the
schema step is skipped, and the guard would only fire after the cutover, in
a restart loop.

The guard trips on any of these:

- a schema version from before the reset (stage 7: below
  `model.BaselineSchemaVersion`);
- any row in `user`, `access_accounts` or `team_user`;
- more than one row in `team`;
- any access list, cross-team approval or team grant.

Its message says this build is single-tenant only and needs a fresh data
dir: seed it with `grain connections import` and add its repositories again.
The guard never drops or reinterprets a row.

The daemon has no multi-tenant mode left to start in: `-tenancy`,
`<data-dir>/tenancy` and `GRAIN_ALLOW_MULTI_TENANT` went in stage 3
(3e-iii-c4), and an older unit still passing `-tenancy` gets it named among
the flags the daemon ignores.

Until 6-ii-b4, a store (`Store.SingleTenant`) also refused to write a team
besides the default one, or a member of one (`model.ErrSingleTenantWrite`,
a 409 from the API), and `TestASingleTenantDaemonWritesNothingItsGuardRefuses`
in `tests/security` checked that. Without the refusal, the team routes that
served until stage 3 removed them (`POST /api/teams`) let a running daemon
write a data dir it would refuse on its next start (task 4756). 6-ii-b4
(task 4863) dropped the `team` table, and with it the refusal and the
test: no table is left that can hold a second team or a member. The
post-reset qualification (`scripts/post-reset-e2e.sh`, row I3) restarts
a daemon on the dir it has just written to and checks that the guard
accepts it.

## What the team-scoped features become

| Feature | Becomes | Why |
|---|---|---|
| Git connections (`team.git_connections`, `team.git_credential`) | Deployment git connections: one list on the settings row, with the same scope rules (`model.TeamGitConnection` without the team) | The owner's decision. The host-default, owner and repository scopes already express everything the ladder did |
| Model connections (`grain_config.agent_backends`, `AgentBackend.Team`) | The same records with no team | They are already stored on the settings row. Only `Team` and the team filter (`TeamAgentBackends`, `BelongsTo`) go |
| `team.default_model`, `team.model_settings`, per-model pauses | A deployment default model and per-model settings on the settings row | One deployment means one set of choices |
| Per-team GCP keys (`team_gcp_key`, docs/team-gcp-keys.md) | Deployment GCP keys, `gcp-key:<name>` | The key mechanism is unchanged. Its owner is now the deployment |
| Boards and custom fields (`board.team`, `custom_field.team`) | Deployment-level | There is nothing left to scope them to |
| Team repo config (`team_repo_config`) | Folded into `repo_config` | Repositories are configured once |
| Team pipeline pause | Gone; `pipeline_pause` remains | There is one queue |
| `Task.Team` | Removed (task 4844) | It was kept as inert metadata only while Silo's attestation signed it. Task 4826 removed the Silo hub connection, so the field, its readers and writers, `CreateTaskRequest.Team`, `UpdateTaskRequest.Team`, `POST /api/tasks/{id}/move` and `grain create -team` are gone. The `task.team` column, left unread and unwritten, went with the other team columns at 6-ii-b5 (task 4864) |
| `/api/config`'s `teams` and `tenancy`; `team` on schedule and suite requests | Removed (task 4924) | `/api/config` carries the deployment as one `deployment` object, the shape `GET /api/deployment` answers, and the frontend reads that instead of `teams[0]`. `tenancy` had no value but "single"; `/api/auth/user` still sends it for the workstation installer. Create and update requests for schedules and suites that still name `team` are refused with a 400 naming the field. The deployment's `name` is its `EnvironmentName` (empty when unnamed), not the default team's "admin". `adminAccounts`/`isAdmin` stay: they are the deployment's admins |
| Team-scoped residue in errors and tools | Removed (task 4932) | The connection checks (`AgentBackend.BelongsTo`, `TeamID`, `TeamAgentBackends`, `requireTeamAgentBackend` and Settings' cross-team borrow refusal), the dead config-repo submit check, the team-scoped metrics report (`MetricsRequest.Team` / `?team=`, `MetricsScope.Team`, `metrics.Scope.Teams`, `mcp.NewTeamMetricsTools`, `MetricsDeploymentWideSections`) and the broadcast's team (`mcp.TaskScope`, `grain mcpserver -caller-team`, the tool's `team` argument, `BroadcastTeamResponse.Team`) are gone. The tool keeps the name `broadcast_team_message` and the grant `team-broadcast`, which the tool allowlists name. Git-connection, target-repo and approver errors name the deployment. New git connections' credentials are `grain-git-c-<hex>`; names minted before (`grain-team-<base64 team>-c-<hex>`) are stored selectors and stay as they are, both read as the deployment's (`model.DeploymentOwnsGitCredential`). A secret's team restriction went with task 4933 (next row) |
| A secret's team restriction (`PUT /api/secrets/{secret}/teams`) | Removed (task 4933) | The caller's team is always `default`, so a secret restricted to any other team could be resolved by nothing. The route, the list's `?team=` filter, the `teams` field on `PUT /api/secrets/{secret}/{key}` and `SecretInfo.Teams` are gone, and so are `secrets.SetTeams`, `SetWithTeams`, `Teams`, `ListForTeam`, `ResolveForTeam`, `CopyCredentials`, `WithTeam`/`TeamFromContext`, `ErrSecretRestricted` and `AuditEntry.TeamID`, which nothing has read since schema 25 dropped the audit's team column. A secrets file that already carries `teams` is read and the field ignored, so every secret resolves; the next write leaves it out |
| `Task.Creator`, `Task.Approval` | Unchanged pass-through | Grain is the PEP and trusts Silo's claims about who asked and who approved |

**Open point for the owner.** The brief lists "the `AgentBackends` pool in
`pkg/model/config.go`" among the legacy settings. But `AgentBackends` is
where the model connections themselves are stored
(`model.Connection = AgentBackend`). The plan reads the brief as removing
the pool's *legacy* parts and keeping the connection records. The legacy
parts are:

- the single-framework fields: `AgentFramework`, `GeminiModel`,
  `GeminiEffort`, `ClaudeModel`, `CodexModel` and `CodexEffort`;
- the per-connection `model` and `effort` that `MigrateModelSelection`
  rewrites.

If that reading is wrong, say so on task 4626.

## Stages

Each stage is a task filed from task 4626. They are chained so that none
starts before the one it builds on has merged.

1. **Deployment connections.** Re-key git and model connections to the
   deployment. Route every credential and model lookup through them:
   - the git proxy (`gitproxy/core.go`);
   - CI sandboxes (`daemon/ci.go`);
   - the state repository (`daemon/staterepo.go`);
   - git identity (`daemon/git_identity.go`);
   - capabilities (`daemon/capabilities.go`);
   - the MCP server (`daemon/mcpserver.go`);
   - merge-queue repairs, scheduled runs, releases and server-side git.

   Before stage 1 these called `Credentials.SelectForRepo` or
   `SelectForRepoOn` directly, from 11 call sites.

   **Landed (task 4680).** Nothing resolves a credential or a model
   through the team dimension or the credentials.json ladder any more.
   [git-hosts.md](git-hosts.md#deployment-git-connections-single-tenant-reset-stage-1)
   and
   [connections-and-models.md](connections-and-models.md#deployment-connections-single-tenant-reset-stage-1)
   describe the result. What stage 1 settled:

   - **Git connections are stored on the default team's row**, which
     single-tenant grain already treated as the deployment, rather than
     on a new `grain_config` column. This is the "hidden default team"
     the brief allowed when it is much simpler. A new column needs its
     own migration, state-repo export and settings API, plus an editor
     to replace Team settings → Git connections. Stage 3 removes that
     editor anyway, and stages 6 and 7 reset the schema with no upgrade
     path, so all of that would be written twice. The team dimension is
     gone from every *lookup*. `Store.GitConnectionOn(host, owner,
     repo)` has no team parameter. `TeamGitCredentialOn`,
     `TaskGitCredentialOn` and the proxy's team path all answer with
     it, whatever team they are given. Stage 6 or 7 moves the list onto
     the settings row when the `team` table goes.
   - **Model connections are every stored `agent_backends` record.**
     Dispatch, the model proxies, pins and model resolution read them
     through `model.DeploymentAgentBackends`. `AgentBackend.Team`,
     `BelongsTo` and `TeamAgentBackends` now only say which team's
     editor lists a connection. Stage 3 deletes them with that editor.
     The deployment's default model and per-model settings are the
     default team's `default_model` and `model_settings`.
   - **The ladder is imported, not read.** `Store.ImportGitLadder` runs
     on every start. It records each credentials.json entry not yet
     imported as a deployment connection
     (`.git-ladder-imported.json`). Stage 2 deletes the ladder's
     resolution code (`Select`, `SelectForRepo`, `SelectForRepoOn`),
     which nothing in the git, CI, REST, identity, MCP or state-repo
     paths calls any more. Two callers remain for stage 2:
     `CredentialSet.PackageCredential` (package publishing) and the
     package cache's registry pulls (`pkg/pkgcache/docker.go`). Both
     name packages rather than repositories.
   - **Tests.** Tests whose subject was per-team credential or
     model-connection isolation were rewritten to the deployment
     behaviour where that was short. The remaining multi-team
     walkthroughs (in `tests/e2e`, `cmd/grain` and `ui/e2e`) were
     skipped with a reason naming this stage, then rewritten (task
     4700) as walkthroughs of the deployment behaviour: their fixtures
     set the connections on the default team's row, and they keep what
     else they covered (audit attribution, commit identity, reach
     checks, usage-limit pauses, host routing).
2. **Legacy paths and the import command.** Delete:
   - the credentials.json ladder (the repository selection in
     `gitproxy/credentials.go`);
   - `-github-host` token plumbing that bypasses connections;
   - the legacy agent settings and `MigrateModelSelection`;
   - `team_git_credential_migration.go` and
     `team_agent_backend_migration.go`.

   Add `grain connections import <file>`, and seed installs through it.

   **Landed (task 4681).** What stage 2 settled:

   - **The ladder is gone, not imported.** `credentials.json` is never
     read; `Store.ImportGitLadder`, `MigrateLegacyGitCredentials`,
     `team_git_credential_migration.go`, the -repo ladder copy and the
     pattern editor (`/api/github-credential-patterns`) are deleted.
     `CredentialSet` holds credential material only, and its `Select`
     (package publishing, the package cache's registry pulls) asks the
     deployment's git connections. Every stored credential is a
     nameable capability, the host default's included.
   - **The legacy agent settings are gone.** `model.Config` has no
     `AgentFramework`, `GeminiModel`, `GeminiEffort`, `ClaudeModel`,
     `CodexModel` or `CodexEffort`, and `AgentBackend` no `Model` or
     `Effort`; their columns stay for stage 6 to drop. The daemon's
     `-agent-framework` and `-*-model` flags, the settings API and CLI
     fields, `MigrateModelSelection` and
     `team_agent_backend_migration.go` are deleted. A run's model and
     effort are only what was chosen for it on a connection.
   - **The `AgentBackends` pool** keeps the connection records, as this
     plan read the brief: only its legacy parts went.
   - **`grain connections import <file>`** (`cmd/grain/connections.go`)
     takes YAML with `git:`, `models:` and `default_model:`, and is
     idempotent. `scripts/setup.sh`, `grain init` and `grain init
     workstation` seed through it; [git-hosts.md](git-hosts.md) and
     [connections-and-models.md](connections-and-models.md) document the
     format.
   - **Left for later stages.** `Team`'s own per-framework fields
     (`Team.EffectiveModels`) and the team editors go with stage 3.
     `-github-host` still names the default git host and the REST host;
     it no longer chooses a credential.
3. **Accounts, sessions, RBAC, admin and the multi UI.** Remove:
   - in `pkg/ui`: `auth.go`, `access.go`, `users.go`, `team_scope.go`,
     `teams.go`, `teampipelinepause.go` and `team_events.go`;
   - in `pkg/model`: `user*.go`, `access_account_store.go`,
     `admin_mode.go`, `cross_team_approval.go` and `tenancy.go`;
   - in `cmd/grain`: `user.go`, `useradmin.go`, `team.go` and `access.go`;
   - the `-tenancy` flag, the team switcher and the team-in-URL UI.

   This stage rewrites `tests/security/routes_test.go` and
   `.grain/baselines/route-authorization.txt` for the single-tenant API,
   which needs the owner's Submit.

   **Landing in slices.** Stage 3 touches about 85 Go test files and
   130 Playwright specs, more than one run can rewrite and keep green, so
   it lands as a chain of pull requests, each green on its own, in this
   order. Multi-tenant mode kept working between them, minus what each
   removed, until 3e-iii-c4 took it out.

   - **3a: access lists and team pipeline pauses (task 4682, landed).**
     The access-account allowlist is gone: `/api/access/accounts`,
     `grain access`, `model/access_account_store.go`, `pkg/access`'s
     store (its identifier parser stays, for team members), the System
     settings Access panel, and the allowlist's place in
     `Store.IsUserAllowed`, the IAP reconciler's desired members and the
     state-repo settings import. Team pipeline pauses are gone:
     `/api/teams/{id}/pipeline-pause`, `model/teampipelinepause.go`,
     `pkg/ui/teampipelinepause.go`, dispatch's per-team gate, the team
     pause control and its alert. `pipeline_pause` is the one pause.
     pipeline-control now pauses the deployment only for a task that
     holds it deployment-wide, which every single-tenant task does; a
     team-scoped grant is refused rather than widened. Both tables stay
     for stage 6, written by hand in the state-repo fidelity fixture
     so the round trip keeps covering them.
   - **3b: cross-team approval and team events (task 4733, landed).**
     Cross-team dependency approval is gone: `model/cross_team_approval.go`,
     `Team.RequireCrossTeamApproval`, dispatch's approval hold, the
     unused `pending_cross_team_approval` state and its liveness row,
     `/api/tasks/{id}/approve-cross-team`, `/cross-team-approvals` and
     `/withdraw-cross-team-approval`, the task detail's approval fields,
     the task pane's hold, approvals and dependents panes, and the
     Audit / Security tab's policy switch. A dependency on another
     team's task simply waits for it. Team events are gone:
     `pkg/ui/team_events.go` (`/api/teams/{id}/events`,
     `/tasks/events`, `/listener`) and `HTTPClient`'s `TeamEvents`,
     `ListenTeamEvents` and `ListenTeamEventsWait`; nothing in the UI
     called them. `cross_team_dependency_approval` and
     `team.require_cross_team_approval` stay for stage 6, written by hand
     in the state-repo fidelity fixture.
   - **3c: admin mode (task 4734, landed).** A configured system admin
     is an admin on every request again. Gone: `model/admin_mode.go`
     (`WithoutSystemAdmin`, `SystemAdminSuppressed`, `Config.IsAdminIn`;
     `Store.IsAdmin` now answers what `IsConfiguredAdmin` did, and that
     name is gone), the `X-Grain-Admin-Mode` header (`AdminModeHeader`,
     `adminModeRequested`, the server's suppression of a person's admin
     rights without it, and the CLI client's copy of it), the "turn on
     Admin mode" refusal hints (`adminModeHint`, `adminModeHintFor`),
     the `adminMode` fields of `/api/auth/user` and `/api/config` and the
     latter's `isSystemAdmin`, the sidebar's switch and chip, the
     `grain.adminMode` sessionStorage marker and `api()`'s copy of it,
     and the Playwright suite's default header with every spec's
     `extraHTTPHeaders: {}` override. The installers' readiness reports
     and the docs no longer send a new admin to the switch. Tests whose
     subject was admin mode went with it; `tests/security` keeps every
     check that is not about the marker.
   - **3d: sign-in accounts (task 4735, landed).** grain keeps no
     password or Google accounts and signs no sessions. Gone:
     `model/user.go` and `user_store.go` (`User`, `PutUser`, `GetUser`,
     `ListUsers`, `DeleteUser`, `SetUserPassword` and the first-user admin
     bootstrap), bcrypt, `pkg/ui/users.go` (`/api/users`), the password,
     registration, change-password and Google OAuth routes of
     `pkg/ui/auth.go` (which keeps sign-out, `/api/auth/user` and
     `/api/auth/status`), the `grain_session` cookie and its JWTs
     (`CreateSessionToken`, `SessionCookieName`, `SetSessionSecret`,
     `AuthMethodSession`, `Password` and `Google`), `grain user` and
     `grain user bootstrap-admin` (`pkg/cli/user.go`, `useradmin.go`; the
     plan's `cmd/grain` paths), `Config.PasswordAuthEnabled`,
     `OpenRegistration` and the `GoogleOAuth*` settings with the System
     settings Authentication section and All Users tab (Access & Accounts
     is now Access), and the frontend's login, registration and
     change-password forms: the username dialog only picks the name a
     browser sends as `X-Grain-User`. `/api/config` loses
     `passwordAuthEnabled`, `registrationOpen`, `googleOAuthEnabled`,
     `noPasswordAccounts` and `hasPassword`. Nothing reads or writes the
     `user` table: `Store.IsUserAllowed` counts team members and system
     admins, `ResolveTenancy` no longer resolves `auto` to multi for
     accounts or Google sign-in, the loopback-trust warning and
     `CheckApprover` stop counting accounts, and the state repository's
     note about members with no account is gone.
     `pkg/ui/single_tenant.go` keeps the access-code gate and the loopback
     and CLI-token trust; `singleTenantRefusesRoute` went with the routes
     it refused. `scripts/setup.sh` and `grain init` make the installing
     person a system admin through the settings API and write no password
     file. The `user` table and the `grain_config` sign-in columns stay for
     stage 6, written by hand in the state-repo fidelity and Postgres
     fixtures, and the Google client secret column is still withheld from
     the state repository. `tests/security` signs people in with verified
     IAP assertions where it used session tokens.
   - **3e:** teams, membership and team RBAC (`team_scope.go`,
     `teams.go`, `cmd/grain/team.go`), the `-tenancy` flag and
     `model/tenancy.go`, the team switcher and team-in-URL UI; the
     rewrite of `tests/security/routes_test.go` and the route baseline
     for the single-tenant API; the security docs and threat model;
     `docs/multi-tenancy-design.md` and the other multi-tenant docs.
     Too large for one run (forcing single-tenant fails about 109
     `pkg/ui` tests on its own), so task 4736 split it into a chain:
     - **3e-i: the `grain team` CLI (task 4736, landed).** `pkg/cli/
       team.go` (the plan's `cmd/grain/team.go`) is gone.
       `broadcast` and `move` went with it; the rest name the
       deployment and no team: `grain git-ambient`, `grain
       git-connections`, `grain default-model` and `grain gcp-key`
       (`pkg/cli/deployment.go`), which still reach the default team's
       row through `/api/teams/default/...` until 3e-iv. The workstation
       installer calls them and `GRAIN_WRAPPER_TEAM` is gone; its
       seeded model is filed on `default`.
     - **3e-ii: the multi-tenant UI (task 4777, landed).** The
       frontend names no team. Gone: `TeamContext.jsx` (`useTeam`,
       `TeamProvider`, `SelectedTeamRowContext`, `firstTeam`, the
       `grain.selectedTeam` marker), team-in-URL routing (`paths.js`
       keeps every other path; `/teams/<team>/...`, `/team-settings`
       and the team home's bare `/t/<team>` now land on the task list
       like any unknown path, and `/t/<team>/<rest>` is read as
       `/<rest>` and rewritten to it, so a saved "File a task with
       these fields" link, `/t/<team>/new?field.…`, still opens the
       prefilled form -- task 5090), the top bar's team
       switcher, team home and team settings buttons (it reads
       "grain / <page>"), every list, count and config narrowed to the
       selected team (App's and the rail's `visible*` filters and
       team-scoped `config`), `TeamSettingsOverlay` with its Team,
       Members, Capacity and Audit / Security tabs (`TeamProfilePanel`,
       `TeamMembersPanel`, `TeamMembersBulkAdd`, `bulkMembers.js`,
       `TeamCapacityPanel`, `TeamAuditPanel`, `TeamBroadcastDialog`),
       the All Teams pane (`TeamsPanel`), the New team dialog and the
       rail's New team item, `TeamHomePage` (the setup alert counts the
       deployment's steps left and opens the Settings tab that fixes the
       first), `NoTeamPage`, `TeamEmptyNote`,
       `InTeamNote`, the executor-token dialog's team picker (a token
       is still filed on `default` until 3e-iv), `capabilityAllowedForTeam`
       and `capabilityTeamHint`, and the repo page's team layer
       (`selectedTeam` is always null there; its dead branches go with
       the team repo-config routes in 3e-iv). Settings gains Models &
       connections, Git connections and Workflow & Tasks tabs
       (`DeploymentConnectionsPanel.jsx`, reusing `TeamModelsPanel` and
       `TeamGitPanel`), and its Executors tab lists the connected
       executors (`ConnectedExecutors.jsx`). They read and write the
       default team's endpoints, where stage 1 keeps the deployment's
       connections: `/api/teams/default/git-connections`, `PUT
       /api/teams/default` for `defaultModel` / `modelSettings`, and
       `/api/teams/default/agent-backends/{id}`. Pins keep the key the
       default team's list had (`grain.pinnedNav.<user>.default`). The
       schedule, template, suite and new-task dialogs still carry a
       Target team picker, which shows only on a deployment with more
       than one team and goes with team roles in 3e-iii. Playwright
       walkthroughs whose subject was team switching, membership, team
       roles or team settings are deleted; the connections, models and
       credential-reach walkthroughs run against Settings.
     - **3e-iii (landed):** single-tenant only: the `-tenancy` flag,
       `model/tenancy.go` and every `SingleTenant` switch with the
       multi-mode branches behind it; team RBAC (`team_scope.go`,
       `requireSystemAdmin` / `checkSystemAdmin` as role checks); the
       rewrite of `tests/security/routes_test.go` and the route
       baseline. Still too large for one run (task 4778 measured 108
       failing `pkg/ui` tests and 148 more in `pkg/cli`, `pkg/daemon`,
       `pkg/demo`, `tests/e2e` and `tests/security` with single-tenant
       forced), so it lands as a chain, each piece green:
       - **3e-iii-a (task 4778, landed):** the `pkg/ui` tests that
         reach a server over a real listener present its access code
         (`accessCodeClient`, `serverAccessClient`), as the grain CLI
         does, so they pass whichever mode the daemon is in. No
         security expectation changes. 66 `pkg/ui` tests still fail
         single-tenant; their subject is team roles or multi-tenant
         trust, and they go with the code they test.
       - **3e-iii-b (task 4791, landed):** the same preparation in
         `pkg/cli`, `pkg/daemon`, `pkg/demo` and `tests/e2e`: each
         package's `access_code_test.go` gives the servers its tests
         reach a test access code and has their clients present it
         (`-token`, the HTTP client's token, the token file, or a
         daemon's seeded `local-api-token`). In `tests/security`,
         `TestSecretResolutionAuditTrailLogging` is split from the
         team-admin check on its route
         (`TestSecretAuditTrailIsTeamAdminOnly`). What still fails
         single-tenant there is about team roles or multi-tenant
         trust and goes with 3e-iii-c: the 16 other `tests/security`
         tests, the team walkthroughs in `tests/e2e`
         (`team_*_walkthrough_test.go`,
         `secret_audit_cross_team_walkthrough_test.go`), and
         `TestLiveDeclaredRepoAccessAndPinnedCommitWalkthrough` in
         `pkg/cli`.
       - **3e-iii-c (landed, tasks 4792, 4803-4805, 4817, 4820 and
         4821):** the flip itself: `-tenancy`,
         `model/tenancy.go` and every `SingleTenant` switch go, the
         single-tenant branch stays, and the tests of multi-tenant
         trust and team roles go; `routes_test.go` and the baseline
         are rewritten (owner's Submit). Task 4792 found the
         Playwright suite unprepared for it (every spec drives a
         multi-tenant `grain demo` and names its caller in
         `X-Grain-User`), so it lands in four:
         - **3e-iii-c1 (task 4792, landed):** `tests/security`.
           `TestRouteAuthorization` sweeps a single-tenant daemon: a
           route is public or trusted, every trusted route refuses a
           caller from loopback without the access code (claiming to be
           the system admin by session token, `X-Grain-User` and
           `?user=`) and answers the access code's session. The
           member, team-admin and system-admin levels are gone, and the
           baseline is empty. The tests of multi-tenant trust and team
           roles are deleted: the loopback, plaintext-header and IAP
           sign-in tests, the forged-claims admin check, the claimed-user
           team edits, both team-admin audit tests and the route file's
           loopback-header and secret-RBAC tests.
         - **3e-iii-c2 (task 4803, landed):** the Playwright suite. Every
           worker's `grain demo` is started with an access code
           (`-access-code-file` in its temp directory, so it is
           single-tenant already) and signs in once through `POST
           /api/auth/access-code`; `ui/e2e/fixtures.js` overrides
           Playwright's `storageState`, so every page, `request` and
           context a spec opens carries the session cookie whatever its
           `extraHTTPHeaders`, and `accessCode` is there for a caller
           outside the browser (a bearer token, the CLI's `-token`).
           Measured that way, 24 tests failed. Deleted, as about team
           roles or multi-tenant trust: `rbac-writes-walkthrough`,
           `console-sessions-team-scoping`,
           `suite-run-team-config-refusal`,
           `system-settings-scoped-access-walkthrough` and
           `people.js`; the viewer, member and outsider steps of the
           base-branch-gate, repo-tabs, release-deletion,
           repo-ownership, ui-followups and work-tree specs; shell's
           username prompt; and the people and second team of
           `main-tasks-sessions-proposers-walkthrough`. Rewritten without
           a second team or a named person: closed-task sorting (its team
           filter step goes), the not_before walkthrough, release runs (as
           the deployment's owner), candidate-keyboard's and the
           recurring walkthrough's default model (the deployment's), the
           phone footer, repos-autosave, the security perimeter's raw
           requests, and insecure-origin's cookie for its own host.
           `path-triggers-sensitive-hold-walkthrough` lost its
           named-approver test (bob refused, alice releasing), a team
           role; task 4811 then made `grain demo -sensitive-hold` seed no
           team or people besides the owner when single-tenant (its
           trigger names the owner as approver) and brought the spec
           back for the owner's Submit releasing the hold and the
           repository page's failed-declaration-read line. The specs that start a multi-tenant daemon of
           their own with `GRAIN_ALLOW_MULTI_TENANT` are left to 3e-iii-c4,
           and specs that still send an `X-Grain-User` the single-tenant
           demo ignores pass as they are.
         - **3e-iii-c3 (task 4804, landed):** `pkg/ui` single-tenant only.
           `ui.Config.SingleTenant`, `TenancyRecovery`,
           `IAPConfig.SingleTenant` and `TrustIdentityHeaders`,
           `Server.singleTenant()` and `singleTenantRequest` are gone:
           `serveHTTP` always runs the access gate, an untrusted caller
           always hears `singleTenantRefusal`, and the `IsUserAllowed`
           check for a person goes, since no person gets that far. In
           `principal.go`, `isTrustedSource` is `singleTenantTrusted`,
           `trustedNetwork` and `trustsEdge` are gone, `extractPrincipal`
           always drops a person, and `identifyCaller` only verifies an IAP
           assertion (for `singleTenantRefusesIdentity`) and logs an
           ignored identity header. `tenancy` stays on `/api/config` and
           `/api/auth/user`, always "single" (`/api/config`'s went in task
           4924); `tenancyRecovery` is gone; the
           access-code handlers answer on every server. `grain demo` is
           always single-tenant, its code beside the throwaway store (and
           logged) without `-access-code-file`. Deleted as about team roles
           or multi-tenant trust: the 66 `pkg/ui` tests (whole files where
           that was their subject: `ci_team_scope`, `team_*_scope`,
           `unauthenticated_access`, `config_stranger`,
           `admin_team_squat`, `basebranchgate_scope`, `console_test`,
           `secrets_test`, `team_agent_backends`, the proposer refusal
           table; rewritten where the subject survives, as the
           deployment's own admin or with the access code: the connection
           turn-limit and CLI-path checks, the deployment routes, the
           ambient-git and git-connection tests, the staging proxy, the
           auth-method report and the IAP verification tests), the
           `tests/e2e` team walkthroughs, `pkg/cli`'s
           `TestLiveDeclaredRepoAccessAndPinnedCommitWalkthrough` and the
           multi-tenant controls in `TestASingleTenantDaemon*`. The route
           sweep and its baseline are unchanged. The Playwright specs that
           start a server of their own (`realdaemon.js`, the Director,
           console and defaults fixture servers, and the specs that spawn
           a daemon or demo) now serve one access code made for the whole
           run (`ui/e2e/accesscode.js`, `GRAIN_E2E_ACCESS_CODE`), so the
           session a worker signs in for holds on all of them;
           `ignored-identity-header` and `loopback-trust-banner` are
           deleted, and so are the member half of `host-sandboxes-alert` and
           `model-picker-search`'s move to a second team. Left to 3e-iii-c4 with the
           daemon's flags: `ui.Config`'s `TrustIdentityHeaders`,
           `TrustedCIDRs`, `TrustTunneled` and `DistrustLoopback`, which
           nothing in `pkg/ui` reads any more, and `daemon.TenancyRecovery`.
         - **3e-iii-c4 (task 4805, landed):** the `-tenancy` flag and
           `model/tenancy.go` are gone, and the daemon is always
           single-tenant: `daemon.TenancyPath` and the kept
           `<data-dir>/tenancy`, `resolveTenancy`, `Config.Tenancy` /
           `SingleTenant()`, `GRAIN_ALLOW_MULTI_TENANT` and `grain
           access-code`'s multi-tenant warning go, and so do the
           multi-tenant branches behind them (the loopback trust warning,
           the host-sandbox refusal and warning about a trusted loopback, a
           team's git reach parking tasks at filing). An older unit still
           passing `-tenancy` gets it named among the flags the daemon
           ignores; `grain init -tenancy` is accepted and ignored. hostinit's
           tenancy step goes and its admin steps are always skipped;
           `scripts/setup.sh` and `wrapper-installer` lose
           `GRAIN_TENANCY` (setup.sh still sets up a v1.9.0 image, which
           predates the single-tenant engine, as multi-tenant), and the
           Kubernetes manifests their `-tenancy=multi`. The Playwright
           specs that started a multi-tenant daemon now start the default
           one, and `team-config-repo.spec.js`, whose subject is the
           configuration repositories of teams besides `default`, is
           deleted. The gitproxy, orchestrator and store `SingleTenant`
           switches stay, always set by the daemon, because their false
           branch is what the team tests still exercise: they go with
           team RBAC in 3e-iii-d. So do the trust options nothing reads
           any more (`ui.Config`'s `TrustedCIDRs`, `TrustTunneled`,
           `TrustIdentityHeaders`, `DistrustLoopback`, their daemon and
           installer flags, `LoopbackTrustWarning`) and hostinit's and the
           installers' admin steps, filed as 3e-iii-c5.
         - **3e-iii-c5 (task 4817, landed):** the trust options are gone.
           `ui.Config` loses `TrustedCIDRs`, `TrustTunneled`,
           `TrustIdentityHeaders`, `DistrustLoopback` and
           `LoopbackTrustWarning` (and `IAPConfig` its copies of the first
           three), `daemon.Config` its `UiTrustedCIDRs`, `UiTrustRelay`,
           `UiTrustIdentityHeaders` and `UiDistrustLoopback`, and the daemon
           the flags `-ui-trusted-cidrs`, `-ui-trust-relay` /
           `-ui-trust-tunnel`, `-ui-trust-loopback` and
           `-ui-trust-identity-headers`; a unit that still passes them gets
           them named among the flags the daemon ignores. The startup
           warnings about them go, and so do `ui.LoopbackTrustWarning`,
           `/api/config`'s `loopbackTrustWarning` and its alert, and
           `relaysettings.Status.TrustRelay` with `TrustWarning` and the
           relay view's `warning` (and the Relay panel's alert for it).
           `scripts/setup.sh` loses `GRAIN_UI_TRUST_LOOPBACK`, which a v1.9.0
           image setup.sh still installs could have used: such a daemon now
           always trusts its loopback, as v1.9.0's default did. `grain init
           -ui-trust-loopback` is accepted and ignored, hostinit stops
           carrying `-ui-trust-loopback=false` across a unit rewrite, the
           workstation launcher stops passing it, and `grain init` always
           says how to reach the daemon with the local API token. The
           admin steps and the `.grain/templates` text that c5 was also
           filed for are split off: hostinit's `ensureAdminAccount` /
           `ensureAdminUser`, `-admin-account` / `-admin-user` and
           wrapper-installer's `make_admin` as 3e-iii-c6, and the two
           install-walkthrough templates' `GRAIN_TENANCY` as 3e-iii-c7
           (an edit under `.grain/`, so it waits for a human's Submit).
         - **3e-iii-c6 (task 4820, landed):** hostinit's admin steps are gone:
           `ensureAdminAccount` and its `adminAccountNotAdmin` probe,
           `ensureAdminUser` (`adminuser.go`), `skipAdminSteps` and
           `Options.AdminAccount` / `AdminUser`, with their tests.
           `keepStoredAdminActor` stays: the daemon's admin routes still
           check that its actor is a stored admin until 3e-iii-d, so a
           `-data-dir` a root daemon seeded still needs the unit's `-as`
           rewrite; it just no longer retargets `-admin-account`. `grain
           init -admin-account` / `-admin-user` are accepted and ignored,
           and `defaultAdminAccount` / `defaultAdminUser` go.
           `wrapper-installer` loses `make_admin` and its summary's
           Admin line; `--no-admin` and `GRAIN_MAKE_ADMIN` are accepted
           and ignored (and the latter is still kept out of setup.sh's
           environment). `scripts/setup.sh`'s `ensure_admin_user` stays
           while v1.9.0, which predates the single-tenant daemon
           (`DAEMON_PREDATES_TENANCY`), is the last release.
         - **3e-iii-c7 (task 4821, landed):** the install-walkthrough templates
           stop setting `GRAIN_TENANCY`. The single-tenant walkthrough
           runs a plain `sudo ./scripts/setup.sh` and checks the unit for
           no `-tenancy` instead of a `tenancy: single` log line nothing
           prints any more. The shared-host walkthrough stays, under its
           old name so its schedule and reports line up, because its
           re-run, upgrade, channel switch, rollback, host mode and
           `grain init` flows are covered nowhere else; it loses the
           admin account, second user and team, the tenancy line and the
           `GRAIN_TENANCY=auto` re-run, and its old-style-unit step now
           checks a unit still passing `-tenancy` starts with it ignored.
           `docs/test-plans/install-walkthroughs.md` follows.
       - **3e-iii-d (task 4793, landed):** team RBAC, unreachable after
         3e-iii-c: every caller `pkg/ui` serves is the deployment's own
         actor, so each check now passes it as it already did.
         `team_scope.go` and `ci_scope.go` are gone (the read, write,
         propose and admin scopes, `authorizeTask*`, `authorizeTeam*`,
         `authorizeStanding*`, `proposerMayEdit`, `adminReposOf`, the CI
         run and metrics bounds), and with them the per-task route
         wrapper in `Server.route`, the team filters on every list (tasks,
         task states, the work tree, schedules, templates, suites, suite
         runs, secrets, console sessions, consoles, base-branch gates,
         pauses, local script approvals, teams), `readableTemplate` /
         `readableSuite` (callers read the store), the standing-work
         pre-authorization (`requireAdminOfSomeTeam`,
         `authorizeStandingCreate`; an edit of an unknown id is still a
         404 before its body is read), `CreateTask`'s creator-role and
         proposer-only filing, `UpdateTask`'s proposer narrowing, the
         console viewer scope, and `/api/config`'s `readOnlyTeams`,
         `proposerTeams`, `adminTeams`, `adminScoped` and `adminRepos`.
         `system_admin.go` (`checkSystemAdmin`, `requireSystemAdmin`) is
         gone with every call, as are `requireSystemAdminOr`,
         `requireSystemAdminToName`, `requireSystemAdminForGCPKeys` and
         the role half of `requireSystemAdminForAmbient` (now
         `requireAmbientAvailable`). In `pkg/model`, `Team.EffectiveRole`,
         `CanRead`, `CanSubmit`, `CanApprove`, `CanPropose`, `IsProposer`,
         `IsMember`, `IsViewer`, `CanConfigure`, `TeamRole.CanSubmit` /
         `CanPropose` and `ProposerMayEdit` are gone; `Store.IsApprover`
         reads a `team-admins:` approver as `team:` (any member), and the
         hub asks `UserInTeam` of an approver. `requireLiveTeam` stays (it
         is validation, not a role). Left to 3e-iv with `teams.go` and
         team membership: `TeamRole` and its constants (the stored member
         role), `Team.UserRole` / `HasAdmin` / `IsAdmin` and
         `Store.UserRoleInTeam` as membership reads, and the team-admin
         checks inside `teams.go` (`checkTeamAdmin`,
         `authorizeTeamCredentialChange`), `checkForceMergeAdmin` and the
         repository-admin rule (`authorizeRepo`) that still read them;
         `Store.StatesPageInTeams` is unused and goes with the schema.
         With the admin routes no longer asking whether the daemon's
         actor is a stored admin, hostinit's `keepStoredAdminActor`
         (3e-iii-c6) has nothing left to keep working.
     - **3e-iv (landed):** `teams.go` and the
       `/api/teams*` routes, team
       membership and team grants; deployment routes for what the
       deployment still configures there. Task 4779 found 3e-iii not
       yet landed (team RBAC and multi mode still in the tree), so it
       lands in two pieces:
       - **3e-iv-a (task 4779, landed):** the deployment routes,
         additive (`pkg/ui/deployment_routes.go`): `GET`/`PUT
         /api/deployment` (the default team's row; the PUT carries only
         `defaultModel` and `modelSettings`, so it writes no member),
         `/api/deployment/git-connections` (list, create, `DELETE
         .../{cred}`, `POST .../check`), `/api/deployment/agent-backends/
         {backendId}`, `GET /api/deployment/reachable-repos`, and
         `/api/deployment/repos/{owner}/{name}/config` and
         `.../git-connection` (the config route's `DELETE` went with
         `team_repo_config` in 6-ii-b3). Each is the default team's route with
         the team fixed, so it authorizes as that one does until team
         roles go; `tests/security/routes_test.go` holds them there, and
         `TestASingleTenantDaemonServesItsDeploymentRoutesOnlyToItsOwner`
         holds each open to the access code and refused without it.
         GCP keys were already `/api/gcp-keys` (4e). `grain git-ambient`,
         `git-connections` and `default-model` call them
         (`HTTPClient.GetDeployment`, `UpdateDeployment`,
         `AddDeploymentGitConnection`), as do Settings' Models &
         connections and Git connections editors and the repo list's
         and New task's reachable-repos reads.
       - **3e-iv-b (landed):** the removal and the
         repointing ahead of it. Task
         4798 found 3e-iii-c4 and d not yet landed, so it lands in two:
         - **3e-iv-b1 (task 4798, landed):** the callers that need no
           team route. `grain init` (`ensureTeamTargetRepos`,
           `ensureTeamGitCredential`, `ensureK8sTeamGitHubCredential`,
           `getTeamSettings`) reads `GET /api/deployment`, writes
           `-github-repo` with `PUT /api/deployment`, which now carries
           `targetRepos` (still no team field), and adds its connection
           with `POST /api/deployment/git-connections`. The repository
           page's dead team layer (`selectedTeam`, the team repo-config
           reads and writes, the Team context banner) and
           `teamRepoApiRoot` are gone. The Playwright specs that set the
           deployment up through `/api/teams/default` use the deployment
           routes.
         - **3e-iv-b2 (landed):** after 3e-iii-c4 and
           d, the removal. Task 4814
           found it too large for one run and landed it in three:
           - **3e-iv-b2a (task 4814, landed):** the routes. Every
             `/api/teams*` route is gone (create, list, get, update,
             archive, unarchive, delete, the retired github-credential
             routes and their checks, copy-credentials, broadcast,
             secret-audit and audit-logs), and so are `HTTPClient`'s
             team methods. The deployment routes have handlers of their
             own in `deployment_routes.go` instead of `onDeployment`
             over the team handlers: each fixes the default team's row
             and asks nothing more of its caller than `serveHTTP`'s
             access gate already did, so it is open to a trusted caller
             and refused to an untrusted one. Three moved there rather
             than going: `POST /api/deployment/broadcast` (the
             team-broadcast tool's hop, `DaemonTasks.BroadcastTeamMessage`,
             now `HTTPClient.BroadcastDeploymentMessage`), and `GET
             /api/deployment/secret-audit` and `.../audit-logs`, the only
             reads of the audit trail. `tests/container`'s
             `giveDefaultTeamACredential` adds a placeholder git
             connection with `POST /api/deployment/git-connections` (once
             per daemon) instead of a `PUT` of `gitCredential`. The hub
             round trip writes its submitter and approver into the
             default team in the store before it is offered work. The
             Playwright specs: the running-ack walkthrough broadcasts on
             the deployment route, `port-settings-walkthrough` loses its
             team-role half, `ui-followups` its invalid-time-zone team,
             `task_inputs_verification` reads `/api/config`'s teams, and
             the board-views spec its team cleanup. The `pkg/ui` tests
             whose subject was a team route are deleted (`teams_test`'s
             create, update, delete and legacy-credential cases,
             `cross_team_walkthrough`, `non_admin_team_setup`,
             `team_onboarding`, `team_git_connections_ui`,
             `team_github_credential_check`,
             `team_multi_repo_concurrent`); the rest call the deployment
             routes, or `ui.Client` for a second team. The route sweep
             loses the team routes; the baseline held none of them.
           - **3e-iv-b2b (task 4833, landed):** the `ui/src` team
             helpers. `teamMembers.js` and `teamSetup.js` are gone, and
             nothing in `ui/src` asks about a team role or membership:
             every reader the UI serves is the deployment's owner. The
             setup alert counts the deployment's own required steps (a
             git connection, a model connection, a repository) with
             `alerts.js`'s `deploymentSetupStepsLeft`, for whoever reads
             it; its optional members and default-model steps and their
             "Not needed" note (`onboardingDismissed`, written with the
             gone `PUT /api/teams/{id}`) went with the checklist. The
             schedule, template and suite dialogs start a new item on
             the deployment and offer every live team; New task offers
             every live team; the repo list always shows Add repo; and
             the task filters' people menus offer the names the tasks in
             view carry, with no team member list folded in
             (`teamPeople`).
           - **3e-iv-b2c (landed):** team
             membership and `teams.go` itself. Task 4834 found it too
             large for one run and split it in three:
             - **3e-iv-b2c-i (task 4834, landed):** `ui.Client`'s team
               methods nothing serves any more and the team-admin checks.
               `CreateTeam`, `ArchiveTeam*`, `UnarchiveTeam*`,
               `DeleteTeam*`, `CopyCredentials` (and `POST
               /api/secrets/copy` and `.../copy-credentials`, the copy
               route nothing in `ui/src` called), the team and per-repo
               GitHub credential setters, deleters, checks and status
               reads, `checkTeamAdmin`, `authorizeTeamCredentialChange`,
               `checkForceMergeAdmin` and the team-admin rule in
               `authorizeRepo` are gone, as is `UpdateTeam`'s admin
               check and `Store.StatesPageInTeams`. What took a team
               through `authorizeTeamCredentialChange` looks it up with
               `existingTeam`; `teamRepoTarget` keeps
               `authorizeTeamRepoAccess`'s target-repo boundary (which a
               system admin passes) without its member checks. Adding
               or removing a repo, a person's broadcast and a forced
               merge ask nothing of a team role: every caller the daemon
               serves is its own actor. A repository's settings are a
               system admin's or one of its own admins'
               (`RepoConfig.Admins`). Tests that made a second team
               through `CreateTeam` write it with `Store.PutTeam` (pkg/ui's
               `putTeam` / `createTeam` helpers); those whose subject was
               a team admin's rights are deleted
               (`team_permission_errors_test`, the track2 role-governance
               walkthrough, `TestForceMergeRequiresTeamOrRepoAdmin`,
               `TestRepoMultiTeamAdminAuthorization`, and teams_test's
               archive, delete, copy and verified-principal cases), and
               the CI and release authorization tests' team admin is a
               repository admin instead.
             - **3e-iv-b2c-ii (task 4840, landed):** the two places
               membership still decided anything. The hub's check that a
               hub task's submitter and approver are on the team it files
               into went with the hub itself (commit 6f5d809e4), which landed first.
               `Store.IsApprover` reads a `team:` or `team-admins:` token
               as the deployment's admin accounts (`system-admins`), so
               the default approver of a sensitive trigger with no
               `approvers`, `team-admins:<team>`, is who it always was on
               a single-tenant grain; a team the deployment does not have
               still names nobody, and `CheckApprover` still refuses one
               at sync. `DescribeApprover` says "a system admin" for all
               three, and `DescribeApprovers` says each description once.
               `TestSubmitOnASensitiveHoldWaitsForAnApproverOfEachRule`
               refuses a team member's Submit and takes an admin's, and
               the path-triggers walkthrough's admin account is `owner`,
               whose Submit releases `team:payments` and path-triggers
               where carol's (in team payments) and alice's no longer do.
               Nothing outside `pkg/model`'s membership code calls
               `UserInTeam` any more.
             - **3e-iv-b2c-iii (task 4841, landed):** team membership
               itself. `model.Team` has no `Members`, `Users` or `Admins`;
               `TeamMember`, `TeamRole` and its constants,
               `ValidateTeamRole`, `ValidateTeamUser` and the member methods
               (`UserRole`, `HasUser`, `HasAdmin`, `IsAdmin`, `AddUser`,
               `AddAdmin`, `AddMember`, `SetMemberRole`, `RemoveUser`,
               `RemoveAdmin`) are gone, as are `Team.Validate`'s member
               rules and `ErrTeamUserRequired` / `ErrTeamAdminRequired` /
               `ErrUserNotInTeam`. In `pkg/ui`, `ui.TeamMember`,
               `ui.Team`'s `users`/`admins`/`members` and its `HasUser` /
               `HasAdmin` / `IsAdmin`, and `UpdateTeamRequest`'s member
               fields and "at least one user/admin" rules are gone. The
               store neither reads nor writes `team_user`: `putTeam` writes
               no members and seeds none from `admin_accounts`,
               `EnsureDefaultTeam` seeds and repairs none, `deleteTeamTx`
               deletes none, `Store.UserRoleInTeam`, `UserInTeam` and
               `UserTeams` are gone, and `AllowedUsers` and
               `IsUserAllowed` are the deployment's admin accounts (so a
               trigger's `approvers:` may name only them, as
               `CheckApprover` reads it: the default team's members a
               single-tenant grain had were those accounts, seeded). The
               table and its indexes stay for stage 6; the single-tenant
               guard still counts other teams' rows in it as multi-tenant
               evidence, and the state-repo fidelity fixtures write a row
               there by hand. The IAP reconciler grants UI access to the
               admin accounts (and the agent's service account) only.
               The one membership rule left, `requireRepoWriter`'s "a
               member of a team that targets the repository" for asking
               for a branch or a base-branch run, is `requireRepoAdmin`
               (the default team's members were the admin accounts,
               seeded). In `ui/src`, `canManageTeamTokens` -- a team-admin
               gate the server dropped in 3e-iv-b2a -- is gone. No team
               grants were left to remove. The tests that build teams
               with members lose them, and the ones whose subject was
               membership are deleted (`team_test`'s user and admin
               methods and roles, the sqlite `team_benchmark_test`,
               Postgres's member promotion, the member-of-another-team
               branch request).
     - **3e-v (task 4780, landed):** the multi-tenant docs and the
       threat model. `docs/multi-tenancy-design.md` is deleted, and the
       docs that linked to it or walked team settings, team roles,
       membership or the team switcher point at Settings and this plan
       instead (`user-journeys.md` retires journeys 4 and 11 and rewrites
       5 for deployment GCP keys; `quickstart.md`, `access-code.md`,
       `connections-and-models.md`, `ui-walkthrough.md`, `shibu.md` and
       `google/docs`). [design.md](design.md#threat-model),
       [local-policy.md](local-policy.md)
       describe the single-tenant trust model: every caller the daemon lets
       in presents the access code -- the CLI's local API token, the access
       session cookie, a bearer code from anywhere -- or is in-process, and
       is the deployment's own actor; anybody else is refused, wherever
       they come from (loopback, a network address, the relay and the hub
       grant nothing, and `-ui-trusted-cidrs` went in 3e-iii-c5); and who
       asked for a task comes from `-author` or Silo's signed claims, never
       from the caller. Dated reports under `docs/test-runs` and
       `docs/test-plans` are left as they were. The two Playwright
       walkthrough templates of the retired team UI
       (`ui-walkthrough-teams.yml`, `ui-walkthrough-team-settings.yml`)
       are under `.grain/` and went in a change of their own (task
       4836): both are deleted, and what of Team settings is still
       there -- the connections tabs, now System settings', and the
       Memory page -- is walked by `ui-walkthrough-settings.yml`.
     - **3f: the last of system admin (task 4880).** The owner, on
       10-20: admin mode should no longer be relevant; you are always in
       admin mode. Whoever holds the access code is the owner
       ([access-code.md](access-code.md)), so nobody is "not an admin",
       and every admin concept left only ever hid controls from them.
       Gone:
       - `grain_config.admin_accounts` (schema 26, a pure drop: an older
         dump's column is passed over on import, and `grain state check`
         warns that it would be ignored), `Config.AdminAccounts` and
         `Config.IsAdmin`, `Store.IsAdmin`, `AllowedUsers` and
         `IsUserAllowed`, `RepoConfig.IsAdmin`,
         `model.AdminOnlyTeamIDs`;
       - `adminAccounts` from `GET`/`PUT /api/settings`, and
         `adminAccounts`/`isAdmin` from `/api/config` (whose `allowed`
         is now always true, and `accessDenied` gone);
         `AuthUserResponse.IsSystemAdmin`; the daemon's seeding of its
         own actor as the one admin; `grain settings -add-admin`
         (accepted and ignored) and `grain sync`'s admin-accounts diff;
       - hostinit's `keepStoredAdminActor` step (a unit an earlier init
         wrote with `-as <stored admin>` is still recognized as its own);
       - in `ui/src`, `admin.js` (`isSystemAdmin`), Settings → Access's
         system administrators card and its search entry, and
         `CachesPage`'s and `PackageCachePanel`'s `isAdmin` props: every
         flush button is enabled.

       Checks that told an admin from another person are deleted;
       checks that tell a person from an agent stay, renamed to say so:
       - `checkRepoAdmin` / `requireRepoAdmin` / `checkRepoAdminListEditor`
         are `checkRepoPerson` / `requireRepoPerson`: any person passes,
         an agent (a run's MCP token, a sandbox, an executor) is refused
         as before, and a store with no settings row refuses nobody, as
         before. The repo admin list (`RepoConfig.Admins`) granted
         nothing after this, and went in a follow-up (task 4929):
         `repo_config.admins` (schema 27, a pure drop, as 26 was),
         `GET`/`PUT /api/repos/{owner}/{name}/admins`, `SetRepoAdmins`,
         `SetRepoSettingsRequest.Admins`, the repo defaults response's
         `admins`, the declschema `admins` key and `repo_case.go`'s
         folding of the column. An older client's `admins` in a settings
         `PUT` is accepted and ignored.
       - `checkReleaseRef`: a person may name any ref and pin a commit;
         anybody else is held to reviewed branches.
       - `withholdReleaseSecrets`: a by-hand run started by anything but
         a person places no release secrets.
       - `memoryWriter`: a person writes memory; an agent still needs the
         memory capability.
       - `teamRepoTarget`: a person may name a repository outside the
         target repos.
       - `Store.IsApprover`: every person approves every rule
         (`ApproverSystemAdmins` is `ApproverOwner`, still spelt
         `system-admins` for the holds already recorded); a caller that
         is not a person approves none. `CheckApprover` accepts any user
         name, and still refuses a team the deployment does not have.

       The IAP reconciler read the admin list as the people to grant UI
       access to, and replaced the binding with them. With no list,
       that would revoke everybody, so it now adds what grain needs (the
       agent's service account) and keeps every member already bound,
       removing only broad principals
       (`iapreconciler.WithKeepCurrentMembers`).

       `scripts/setup.sh`'s `ensure_admin_user` stays, for the v1.9.0
       image that predates the single-tenant daemon.
       `tests/security/owner_test.go` holds every former admin-only route
       to the access code, and the person-only ones to refusing an agent.

   **Stage 3 landed** (tasks 4682, 4733, 4734, 4735, 4736, 4777, 4778,
   4791, 4792, 4793, 4798, 4803-4805, 4814, 4817, 4820, 4821, 4780,
   4833, 4834, 4840 and 4841). The slices, each with its task:

   | Slice | Task | What it removed |
   |---|---|---|
   | 3a | 4682 | access lists, team pipeline pauses |
   | 3b | 4733 | cross-team approval, team events |
   | 3c | 4734 | Admin mode |
   | 3d | 4735 | sign-in accounts and sessions |
   | 3e-i | 4736 | the `grain team` CLI |
   | 3e-ii | 4777 | the multi-tenant UI |
   | 3e-iii-a | 4778 | `pkg/ui` tests present the access code |
   | 3e-iii-b | 4791 | the other packages' tests present it |
   | 3e-iii-c1 | 4792 | `tests/security` single-tenant only |
   | 3e-iii-c2 | 4803 | the Playwright suite single-tenant only |
   | 3e-iii-c3 | 4804 | `pkg/ui` single-tenant only |
   | 3e-iii-c4 | 4805 | `-tenancy` and `model/tenancy.go` |
   | 3e-iii-c5 | 4817 | the trust options (`-ui-trusted-cidrs` and the rest) |
   | 3e-iii-c6 | 4820 | hostinit's admin steps |
   | 3e-iii-c7 | 4821 | `GRAIN_TENANCY` in the install-walkthrough templates |
   | 3e-iii-d | 4793 | team RBAC |
   | 3e-iv-a | 4779 | (added) the deployment routes |
   | 3e-iv-b1 | 4798 | callers repointed at the deployment routes |
   | 3e-iv-b2a | 4814 | every `/api/teams*` route |
   | 3e-iv-b2b | 4833 | the `ui/src` team helpers |
   | 3e-iv-b2c-i | 4834 | `ui.Client`'s dead team methods; team-admin checks |
   | 3e-iv-b2c-ii | 4840 | the hub's and `IsApprover`'s membership checks |
   | 3e-iv-b2c-iii | 4841 | team membership |
   | 3e-v | 4780 | the multi-tenant docs; the threat model |
   | 3f | 4880 | `admin_accounts` and the last admin checks |

4. **Team plumbing in the orchestrator and dispatch.** This covers team
   limits, team capability scoping (`model/capability_teams.go`,
   `AdminTeamsOr`), proposal inheritance across teams and per-team CI
   identity.

   **Landing in slices**, like stage 3, because each part reaches from
   `pkg/model` through dispatch to the UI. `Task.Team` stayed throughout
   as inert metadata while Silo's attestation signed it; it was removed
   once the Silo hub connection went (task 4844). `Task.Creator` and `Task.Approval`
   stay as pass-through.

   - **4a: team capability scoping (task 4683, landed).** No capability
     is restricted to a team any more. Gone: `model/capability_teams.go`
     (`CapabilityTeams`, `AdminCapabilityNames`,
     `TeamCapabilityAuthorized`), `CapabilitySpec.AuthorizedTeams`,
     `SupportsTeam`, `DefaultAdminTeams`, `AdminTeamsOr`,
     `TeamUnauthorizedRefusal`, every provider's `AuthorizedTeams`
     setting, `Config.CapabilityTeams` and the settings API's
     `capabilityTeams` (an old client sending it is ignored), the
     Settings > Capabilities "Also authorize teams" editor, the
     `admincaps.IDs`/`Widenable` list, the schedule-firing and
     task-filing refusals, the multi-owner CI refusal, and the grant
     drop on a team move. self-debug, pipeline-control and memory are
     deployment-wide for every holder (`TaskDetail.DeploymentWide`
     still lists them, so the MCP server's checks are unchanged); a team
     memory agent still writes only its own team's layer until memory
     becomes deployment-level. `grain_config.capability_teams` stays for
     stage 6 to drop, and nothing reads it. A team GCP key
     (`gcp-key:<name>`) a team has not configured is still refused at
     filing (`MissingGCPKeyRefusal`) until 4e moves the keys to the
     deployment. The frontend's `capabilityAllowedForTeam` was inert
     (no row carries `authorizedTeams`) and went with stage 3e-ii's team
     UI.
   - **4b: team limits (task 4742, landed).** No team can be capped
     below the deployment any more. Gone: `Team.MaxWorkers`,
     `MaxMergers` and `MaxCIJobs` with `Team.Limits` and `HasLimits`,
     the per-team admission in `Store.StartRun` and `dispatch.Cycle`
     (an archived team's tasks are still skipped), `model.Limits.
     SingleTenant`, `ci.SharedGate.TeamLimit` with its per-team tally
     and wait message, `ci.GateRequest.Team`, `daemon.CiTeamLimits`
     and its cache, the team argument to `ci.Engine.ConcurrencyLimit`,
     the sandbox commitment's `teamSlotTotals`, `grain init`'s
     default-team worker step (`-team-max-workers` is accepted and
     ignored), and the team editor's limit fields in the API, the
     Teams pane, the New team dialog, Team settings > Capacity (which
     keeps the merge batch size) and the team home page. An old client
     sending `maxWorkers`, `maxMergers` or `maxCIJobs` is ignored. The
     deployment's own counts are now the only ones: the sandbox
     commitment and the hub's default slot count read
     `grain_config.max_workers`, `max_mergers` and `max_ci_jobs`, as
     the daemon's journal already did. Dispatch and the CI gate still
     schedule on none of those three (grain/task-28): the sandbox
     budget is what holds the work. `team.max_workers`, `max_mergers`
     and `max_ci_jobs` stay for stage 6 to drop; `PutTeam` writes them
     zero and `scanTeam` reads them into discarded variables.
   - **4c: proposal inheritance, CI identity and executor/proxy teams
     (task 4743, landed).** Nothing is attributed to, inherited from or
     checked against a team on these three paths any more.
     - **Proposals.** A proposal's team is no longer its proposer's, and
       the proposer's team is no longer checked: gone are the live path's
       `proposedBy.Team` default and its "agent from team X cannot submit
       tasks for team Y" refusal (`ui.Client.CreateTask`), the "only
       agents from team X / the admin team" refusals for configuration
       repositories, and the run-end filer's team checks and
       `WorksInRepoForTeam(task.Team, …)` (`orchestrator.proposedTarget`).
       A proposal files into the team whose configuration repository it
       targets and otherwise the default team (`proposalTeam`), and its
       repository is checked against the deployment's allowlist (the
       default team's `TargetRepos`). `AutoSubmits` and
       `EscalatedCapabilities` never read a team and are unchanged.
     - **CI.** Gone: `ci.Request.Team`, the plan key's `Team`
       (`planKeyTeam`), `ci.Run.Team`, `ci.SandboxRequest.Team`,
       `ci.RunFilter.Team` and the team argument of `RunFilter.Visible`,
       `Service.TeamCapabilities`/`TeamReleaseSecrets`, `CiJobTeams`, the
       `teams` argument of `daemon.ResolveCICapabilities`, the `RepoTeams`
       hooks of the CI sandbox, grant and workflow-source wiring, the
       manual-run API's `team` (an old client sending it is ignored),
       the scheduled-workflow team lookup, `list_ci_runs`'s `team`, the
       CI page's team narrowing and Team filter, and `model.CIRun.Team`,
       `CIRunQuery.Team` and `CIRunStamp.Team`. A job reads the
       deployment's configuration of its repository (the default team's
       layer over `repo_config`, found without regard to case), resolves
       its capabilities in a context naming no team, and is registered
       with the git proxy as the default team's. CI runs are read by
       repository only, and a shared fault outside the caller's
       repositories is dropped whole. The plan key changes only for runs
       a custom team was attributed: those re-plan once under a new key;
       job fingerprints are unchanged. `ci_run.team` stays for stage 6,
       written as the default team and read into a discarded variable.
     - **Executors and model proxies.** Gone: `modelexec.Daemon.Team`,
       the hello frame's `team`, `Status.Team`, `RemoteHost.Team`, the
       per-team host pools and waiters (`Host` and `ConnectedHost` take
       only the binary), the archived-team refusal of executor tokens
       (`unusableTeam`), and the team half of the instance-claim
       principal (the token alone now). `grain executor -team` is
       accepted and ignored. `modelproxy.Connection.Team`, the resolver's
       and `ConnectionFor`'s team argument, `Entry.Team` and the token
       file's `team` (ignored when read) are gone; a proxy's usage-limit
       pause is keyed by the connection's own `model.ConnectionTeam`, as
       agent runs key theirs, and its refusals are recorded in the
       default team's audit trail. `executor_token.team` stays for stage
       6, and the executor-token dialog's team picker went with stage
       3e-ii's team UI.
     - **Left for later stages.** `team_repo_config` and team-restricted
       secrets no longer reach CI (stage 6 folds the team layer into
       `repo_config`); a custom team's `gcp-key:<name>` in CI waits for
       4e.
   - **4d: memory, boards and custom fields (task 4744, landed).**
     None of the three has a team any more.
     - **Memory.** Two layers, the deployment's and each repo's
       (`model.MemoryFor(deployment, repo)`, headed "What grain has
       learned across this deployment" and "About this repository").
       `Memory.Team`, `MemoryDocument.Team` and `MemoryScope.Team` are
       gone, as are the store's and the API's team arguments and the
       `team` of `/api/memory` and `/api/memory/documents` (an old
       client's `team` or `?team=` is ignored). Every reader is shown
       every scope; a person writes any of it only as a system admin,
       and an agent only from a task holding the memory capability,
       with no team memory agent narrowed to its own layer
       (`requireMemoryGrant`, `daemonMemory`, `deploymentWideGrants`).
       Rows keep the `team` column for stage 6: the deployment row is
       written with `''` and a repo's with the default team, which is
       what single-tenant data already held. `Store.Init` folded every
       other row in (`foldTeamMemory`) until stage 7 removed that fold
       with the other migrations: no store from before the baseline
       opens, so no row under another key can reach it.
     - **The memory agent.** `TeamMemoryAgentTemplateID`,
       `TeamMemoryAgentTeam`, `IsTeamMemoryAgentTask` and the team
       template are gone. `system:memory-agent`, retired in grain/task-3544
       for the per-team agents, is seeded again and writes both layers;
       every `system:memory-agent:<team>` schedule is retired
       (`model.RetiredSystemScheduleID`, `retireSystemSchedules`) and its
       id resolves to the deployment's agent. The new row takes the
       default team agent's cadence, overlap, switch and model when it
       is first seeded, so a deployment's answers carry over.
     - **Boards.** `Board.Team` is gone from the model, the API and the
       frontend (`normalizeBoard`, the rail's and the sidebar's team
       filter, a pinned board's team). Every board is listed for
       everybody; creating or editing one is asked of the default team
       until 3e drops team roles, and boards no longer hold a team's
       deletion up (`ErrTeamHasBoards`).
     - **Custom fields.** Declarations are keyed by name:
       `CustomField.Team`, `ListCustomFieldsByTeam`,
       `GetCustomFieldByTeam` (now `GetCustomField`),
       `DeleteCustomFieldByTeam` (now `DeleteCustomField`),
       `FiringCustomFieldsForTeam` and `CustomFieldDeclaration.Team`
       are gone, and every filing path reads the one list. Rows are
       written under the default team and only the default team's are
       read.
     - `board.team`, `custom_field.team`, `memory.team` and
       `memory_document.team` stay for stage 6 to drop.
   - **4e: GCP keys (task 4745, landed).** Per-team GCP keys are the
     deployment's named GCP keys, `gcp-key:<name>`
     ([team-gcp-keys.md](team-gcp-keys.md)); the key mechanism (pools,
     minters, leases, the reaper) is unchanged.
     - **Lookup.** `gcpkey.TeamKeyProvider` is `gcpkey.KeyProvider`: its
       Resolve, Materialize, PromptSection, Revoke, Reap and
       CheckCredential read the deployment's key of that name
       (`Store.GetGCPKey`), never `cc.Task.Team`, so a task of any team,
       and every CI job, mints with the same key. `MissingGCPKeyRefusal`
       takes no team and says the deployment has no such key; it is
       still what filing (`ui.Client`) and Resolve refuse with.
     - **Model.** `model.TeamGCPKey` is `model.GCPKey`, with no `TeamID`.
       `GetGCPKey`, `ListGCPKeys`, `PutGCPKey` and `DeleteGCPKey` replace
       the team-keyed store methods (`ListAllTeamGCPKeys` and
       `DeleteAllTeamGCPKeys` are gone). Rows are written under the
       default team and only the default team's are read, as stage 1 did
       for git connections. A new key's default minter is
       `gcp-key-minter-<name>` (`GCPKeyMinterCredential`), which is what
       a row with no minter already fell back to; stored rows keep the
       minter they name.
     - **API and CLI.** `/api/teams/{id}/gcp-keys` is `/api/gcp-keys`:
       listing answers any member, and reading one key, saving and
       deleting are a system admin's (`tests/security/routes_test.go`).
       The team-admin minter rule (`requireTeamOwnedMinter`,
       `minterOwnedBy`) and the copy of GCP keys by
       `POST /api/teams/{id}/copy-credentials` (`CopiedGCPKeys`) went
       with the team admin's right to save one. `grain gcp-key` (stage
       3e-i's command) now calls `/api/gcp-keys` (`pkg/cli/gcpkey.go`).
     - **Config repositories.** `gcp_keys.yaml` is the default team's
       configuration repository's, the deployment's own: another team's
       repository neither exports nor applies one (it is logged, and
       `CheckTeamConfig` warns). A key in the file may name only
       `gcp-key-minter-<name>`, and not when another key in the
       database already mints through that secret (a key named
       `default-deploy` would otherwise claim the
       `gcp-key-minter-default-deploy` the `deploy` key was given while
       keys were per team), unless it is unchanged from the database.
     - `team_gcp_key` (and its `team_id`) stays for stage 6.

   **Stage 4 is landed** (slices 4a-4e, tasks 4683, 4742, 4743, 4744 and
   4745). `Task.Team` has since been removed (task 4844).
5. **The startup guard**, described above, with the `SchemaVersion` bump.
   It must merge before stages 6 and 7, and the owner's pin above must
   already be in place.

   **Landed (task 4684).** `model.Store.CheckSingleTenant`
   (`pkg/model/single_tenant_guard.go`) refuses with
   `ErrMultiTenantData` and only reads. `SchemaVersion` is 18, which
   changes no table. The guard runs in four places:

   - `Store.Init`, before any migration;
   - `staterepo.CheckMigration`, which is `grain state check -migrate`,
     the Upgrade button's rehearsal and `scripts/setup.sh`'s
     `grain state prepare-upgrade`;
   - the daemon's import of an upgrade's export
     (`importSchemaUpgrade`), because `Init` ran on the empty store
     before those rows arrived;
   - the daemon's start after the state repository is loaded, for the
     same reason.

   What it counts:

   - any `user` or `access_accounts` row;
   - more than one `team`;
   - a `cross_team_dependency_approval` row, or a team with
     `require_cross_team_approval` set;
   - a non-empty `grain_config.capability_teams`, which is the team
     grant;
   - any `team_user` row on a team other than the default one. The
     default team's members are not counted, because grain writes them
     itself on a single-tenant daemon: `putTeam` seeds them from
     `admin_accounts` whenever it writes the default team, the
     deployment's own actor ends up there, and a state-repository
     restore brings them back (task 4553). Counting them refused
     ordinary single-tenant data dirs, including the ones the e2e
     suite's daemons write.

   It does not count `task_read`, `task_write`, `task_grant` or their
   template and schedule counterparts. Those are a task's read and write
   targets and its capability grants, and every single-tenant deployment
   has them (see the inventory below). `pkg/model/guardtest` runs the
   guard's cases on SQLite and Postgres, through `Init` and through the
   migration rehearsal. The daemon's start after an upgrade is covered
   in `pkg/cli` (`TestAnUpgradeFromAMultiTenantDeploymentDoesNotStart`).

   `scripts/setup.sh` now defaults to `GRAIN_TENANCY=single`. Its old
   `multi` default bootstrapped a password account, which is a `user`
   row, so every fresh install would have refused to start on its first
   restart. The installer suite expects a single-tenant install and no
   `admin-password`. The owner confirmed on task 4684 (2026-10-18) that
   the eu host is pinned as described above.
6. **Drop the multi-tenant and dead schema.**

   **Landing in two parts.** Part one (task 4685, landed) drops what
   nothing reads any more:

   - the tables `access_accounts`, `team_pipeline_pause` and
     `cross_team_dependency_approval`;
   - `team.require_cross_team_approval`, and `team.max_workers`,
     `max_mergers` and `max_ci_jobs`;
   - from `grain_config`: `agent_framework`, `gemini_model`,
     `gemini_effort`, `claude_model`, `codex_model`, `codex_effort`
     and `capability_teams`;
   - the Postgres adapter's `boolColumns` entry `abandoned`, which
     named no column.

   The sweep found nothing else in the schema that `pkg/model`, its
   drivers or `pkg/staterepo` do not read. `github_host` stays, because
   `Config.GitHubHost` still names the default git host and the REST
   host. `task_suite_sequence` stays in `autoIncrementTables`, because
   Init still renames a store's old table of that name.

   `model.RetiredTables` and `RetiredColumns` (`retired_schema.go`)
   listed what was dropped, and Init dropped it from an older store once
   the startup guard had passed. Stage 7 deleted both, along with
   staterepo's `retired.go`, which refused a dump carrying
   multi-tenant rows in the retired schema. The baseline version check
   refuses every such store and dump now. `SchemaVersion` stayed at 18
   until stage 7.

   Part two drops the rest of the inventory below once the code that
   reads it has gone: `user`, `team`, `team_user`, `team_sequence`,
   `team_repo_config`, `team_gcp_key`, and the team columns on other
   tables. That code goes in stages 3d, 3e and 4c-4e. Part two also
   covers `hub_filing.team`, which the inventory missed. When `team`
   goes, the deployment's git connections, default model and model
   settings move to the settings row.

   Stage 7 landed first, so part two has no `RetiredTables` to add to:
   each table it drops is a schema change after the baseline, and a
   store at the version before is refused as old (`ErrSchemaTooOld`)
   and comes across through the state repository, whose import passes
   over a table this build lacks. By hand that is
   `grain state -data-dir DIR prepare-upgrade` with the new build and the
   daemon stopped (`scripts/setup.sh` runs it on a deploy), then a start:
   the start keeps the old store -- `store.schemaN-<time>` beside a
   SQLite store, a `<schema>_schemaN_<time>` schema beside a Postgres
   one (`postgres.ArchiveStore`) -- rebuilds the store from the export
   and rewrites the state repository's dump. The refusal names that
   command; moving the store aside and running `grain state reformat`
   instead starts an empty deployment (task 4851, where the refusal's
   older wording advised just that and Postgres had no way across at
   all). A `grain export` bundle from the build before takes the same
   route: `grain import` restores its store, finds it a schema behind,
   writes and rehearses the same note itself, and the next start
   migrates it (task 4853). A bundle from a Postgres deployment
   carries `db-config.json` and not the rows, so `grain import` reads
   the schema of the database that file names and refuses one a schema
   behind, naming `prepare-upgrade`, rather than arming it while the
   old daemon may still be writing to it (task 4870). `pkg/staterepo/retired.go` is back to
   ask a dump the guard's questions about a dropped table, so an old
   multi-tenant export is still refused rather than imported with its
   rows left behind. It lands in slices:

   - **6-ii-a: `user` (task 4768, landed).** Nothing read it after 3d.
     `SchemaVersion` is 20. The guard's `user` count is gone from
     `CheckSingleTenant` (a store at 20 has no such table) and is a dump
     check instead: `importInto` refuses a dump holding a `user` row with
     `ErrMultiTenantData`, whichever path it arrives by
     (`TestADumpWithASignInAccountIsRefused`). The golden dump stays at
     19, so `TestTheGoldenDumpMigratesIntoThisBuildsSchema` now covers a
     real 19-to-20 migration.
   - **6-ii-b: the rest.** `team`, `team_user`, `team_sequence`,
     `team_repo_config`, `team_gcp_key` and the team columns, in
     slices of its own:
     - **6-ii-b1: `team_user` and `team_sequence` (task 4839, landed).**
       `SchemaVersion` is 21. Nothing read or wrote a team member after
       3e-iv-b2c-iii but the guard, and nothing allocated a team ID from
       `team_sequence` but `Store.NewTeamID`, which no caller used and
       is gone. The guard's count of another team's members moves from
       `CheckSingleTenant` to `retiredMultiTenantChecks`, which now
       counts only the rows a check says count: a `team_user` row on
       the default team, which every build before 3e-iv-b2c-iii seeded
       from `admin_accounts`, is passed over, and one on any other team
       refuses the dump (`TestADumpWithAnotherTeamsMemberIsRefused`).
       The second-team count stays in `CheckSingleTenant` until `team`
       goes. The golden dump stays at 19.
     - **6-ii-b2: the default team's settings move to `grain_config`
       (task 4861, landed).** `SchemaVersion` is 22. `git_connections`,
       `git_credential`, `agent_backend_id` and `model_settings` left
       `team` for `grain_config` columns of the same names
       (`Config.GitConnections`, `GitCredential`, `AgentBackendID`,
       `ModelSettings`), and `team.default_model` folded into the
       existing `grain_config.default_model`, so the deployment has one
       default model and dispatch runs on it. Where a dump held both, the
       team's wins: it was the one dispatch ran on. `model.Team` lost the
       five fields, so the compiler found every reader; the git
       credential ladder (`Store.GitConnectionOn`), dispatch
       (`AgentPool.resolve`), the git connection editor, `PUT
       /api/deployment`, `grain connections import` and the composition
       preview read and write `Config` now (`Store.UpdateConfig` is the
       atomic read-modify-write). One `dumpFixups` entry, keyed 21
       (`staterepo.moveDefaultTeamSettings`), copies the default team
       row's values onto the `grain_config` row of a dump stamped 21 or
       below (`TestADumpAt21CarriesTheDefaultTeamsSettingsOntoGrainConfig`);
       the golden dump stays at 19. A rollback across 22 is refused, as
       every fixup's is. `grain connections import` still records git
       connections before a daemon's first start: into a row holding
       nothing else (`Config.ConnectionsOnly`), which that start seeds
       from its flags, keeping them.

       What the default team's row still holds, and who reads it, for
       b4: `agent_framework` and the legacy `claude_model`,
       `codex_model`, `gemini_model`, `codex_effort` and `gemini_effort`
       (dispatch, through `Config.WithLegacyTeamModels`, and `PUT
       /api/deployment`'s team shape); `agent_git_name` and
       `agent_git_email` (the commit identity a run is given,
       `orchestrator/git_identity.go`); `target_repos` (`PUT
       /api/deployment`'s `targetRepos`, `grain init -github-repo`, the
       repo allowlist in `model/repo_scope.go` and `schedule_fire.go`,
       `daemon/local_settings.go`, the git connection check and the
       UI's repo lists); `repair_template_id` (base-branch repair,
       `orchestrator/basebranchci.go`) and `merge_batch_size` (the merge
       queue, `orchestrator/mergebatch.go`), each with a `grain_config`
       column of the same name already beside it; `archived`, read only
       for a team other than the default one, which the guard refuses;
       and `time_zone`, `name`, `description`, `personal`,
       `anyone_may_propose` and `onboarding_dismissed`, which only the
       API's team shape shows and nothing acts on. Each read needs a
       home on `grain_config` (or, for the dead ones, nothing) before b4
       can drop the table.
     - **6-ii-b3: `team_repo_config` and `team_gcp_key` (task 4862,
       landed).** `SchemaVersion` is 23. `team_repo_config` is folded
       into `repo_config`, which gained the one column it lacked,
       `git_credential` (`RepoConfig.GitCredential`, the first rung of
       `Store.GitConnectionOn` ahead of the deployment's connections).
       `team_gcp_key`'s rows are a deployment `gcp_key` table keyed by
       `name`. `model.TeamRepoConfig` and its store are gone, so the
       compiler found every reader: dispatch's default grants and
       prompt extension, the clone URL, setup command and grain CI
       gate (`EffectiveRepoConfig`, now over `repo_config` alone), CI's
       capabilities and release secrets, the repair template ladder,
       the git connection editor and `GET /api/config` (which drops
       `teamRepoDefaultCapabilities`; `repoDefaultCapabilities` carries
       them now, and the UI's `defaultCapabilitiesFor` finds nothing in
       the team layer it still reads until the UI's team plumbing goes). `GET`/`PUT /api/deployment/repos/{owner}/{name}/config`
       read and write `repo_config` (`DeploymentRepoConfigResponse`,
       `SetDeploymentRepoConfigRequest`); its `DELETE`, which removed
       the team's layer, is gone with the layer. A team configuration
       repository (`grain/config-<team>`, only ever opened for a team
       besides the default one, which the guard refuses) no longer
       reads or writes `repos/` or `gcp_keys.yaml`, and
       `repodecl.LoadTeamRepoConfigs` is gone. One `dumpFixups` entry,
       keyed 22 (`staterepo.foldTeamRepoSettings`), carries a dump
       stamped 22 or below across: each default-team `team_repo_config`
       row lands on its repository's `repo_config` row (made when there
       is none), the team's value winning where both say something, as
       it did at dispatch, and `default_capabilities` unioned, as dispatch
       unioned them; each default-team `team_gcp_key` row becomes a
       `gcp_key` row (`TestADumpAt22FoldsTheDefaultTeamsRepoSettingsAndGCPKeys`).
       Another team's rows are left for `retiredMultiTenantChecks`,
       which refuses them (`TestADumpWithAnotherTeamsRepoSettingsOrGCPKeyIsRefused`).
       The golden dump stays at 19.
     - **6-ii-b4: `team` (task 4863, landed).** `SchemaVersion` is 24.
       b2's note above was right that the row was not yet empty, so b4
       moved what was left: `agent_framework`, `agent_git_name`,
       `agent_git_email` and `target_repos` are `grain_config` columns
       of the same names (`Config.AgentFramework`, `AgentGitName`,
       `AgentGitEmail`, `TargetRepos`; `Store.TargetRepos` and
       `SetTargetRepos`), and the team's `repair_template_id` and
       `merge_batch_size` fold into the `grain_config` columns already
       beside them. The legacy `claude_model`, `codex_model`,
       `gemini_model`, `codex_effort` and `gemini_effort` are folded
       into `default_model` and `model_settings` once, as dispatch
       folded them on every run (`Config.WithLegacyFrameworkModels`).
       `time_zone`, `name`, `description`, `personal`,
       `anyone_may_propose`, `onboarding_dismissed`, `archived` and
       `created_at` are dropped: nothing acted on them. One
       `dumpFixups` entry, keyed 23 (`staterepo.foldDefaultTeamRow`),
       carries a dump stamped 23 or below across, the team's value
       winning where both say something
       (`TestADumpAt23FoldsTheDefaultTeamRowOntoGrainConfig`); the
       golden dump stays at 19. `model.Team` and its store
       (`team_store.go`), `refuseTeamWrite` and `ErrSingleTenantWrite`
       are gone, so the compiler found every reader: the repo allowlist
       (`repo_scope.go`, `schedule_fire.go`, declaration access,
       `orchestrator/targetrepos.go`, `declarations.go`, the UI's repo
       lists and `canonicalRepos`), the commit identity, dispatch's
       framework, the repair-template ladder and the merge queue. `GET`
       and `PUT /api/deployment` keep the team shape the frontend reads,
       built from `grain_config`; any team id but `default` is not
       found. `CheckSingleTenant` now only refuses a store from before
       the baseline: its count of teams besides the default one is a
       `retiredMultiTenantChecks` entry counting `team` rows whose id is
       not `default` (`TestADumpWithASecondTeamIsRefused`, guardtest's
       "a dump holding a second team", and
       `TestAnUpgradeFromAMultiTenantDeploymentDoesNotStart`). With no
       team besides the default one, a team configuration repository
       (`grain/config-<team>`) is never synced or served; its remaining
       plumbing goes with the UI's.
     - **6-ii-b5: the team columns (task 4864, landed).**
       `SchemaVersion` is 25. Dropped: `team` on `board`, `ci_run`,
       `custom_field`, `executor_token`, `memory`, `memory_document`,
       `schedule`, `suite`, `suite_run`, `task` and `template`, and
       `team_id` on `credential_access_audit` and
       `secret_resolution_audit`, with the indexes over them
       (`task_team`, `task_team_order`, and the audit tables'
       `*_team_time`, now `*_time` on the timestamp alone). Task
       4839's brief said `task.team` stays, but that predates task
       4844, which removed `Task.Team` and left the column unread for
       this slice to drop, as the inventory below says; it went, with
       its two indexes. `hub_filing` had no column left to drop: the
       table went with the hub connection (task 4826). `memory`,
       `memory_document` and `custom_field` are keyed without the
       column (Postgres's `tablePrimaryKeys` with them), and a
       repository's memory notes export under `memory/repos/` rather
       than `memory/teams/default/`. A pure drop: the importer passes
       over a column this build lacks, so there is no `dumpFixups`
       entry, and the golden dump stays at 19. `Template.Team`,
       `Schedule.Team`, `Suite.Team`, `SuiteRun.Team`,
       `ExecutorToken.Team`, `Composition.Team` (and its source),
       `CIJobTiming.Team` and the audit entries' `TeamID` are gone, so
       the compiler found every reader: composition, the schedule fire
       path's target-repo check, suite and qualification runs (which
       now take one order key for the run), the base-branch repair
       templates, the git and model proxies' audit, the audit trail
       (`ListSecretAudit`, `ListCredentialAccess` and
       `ListTeamAuditEvents` take no team), the API's template,
       schedule, suite and suite-run shapes, which no longer carry a
       `team`, and executor tokens. An old client's `team` on a
       create or update is still read and anything but the default
       team refused, as before. A `.grain` declaration's `team:` is
       still accepted when it names the default team, and a file
       naming any other is refused at parse time
       (`repodecl.checkTeam`) instead of at sync; with no team to
       check, the sync's own check is only that the declaring
       repository is in the target repos (`checkDeclaredTargets`).
       The team configuration repository's plumbing reads every row
       as the default team's, so another team's repository carries
       none of them; it goes with the UI's.
     - **6-ii-b6: the team configuration repositories (task 4918,
       landed).** `grain/config` is the only configuration repository.
       No schema change: no table had a column naming one. Gone:
       `model.TeamConfigRepo` and `IsTeamConfigRepo` (`IsConfigRepo` is
       `grain/config` alone, so `RepoAllowed` no longer admits a
       `grain/config-<team>` name off the allowlist); the
       `team-state-repos` directory handling (`TeamStateReposDir`,
       `TeamStateRepoDir`); the `StateManager`'s `teams` map and
       `teamRepo`; `CiWorkflowSource.TeamConfigReposDir` and
       `StateCheckSandboxes.TeamConfigReposDir`; the team mode of the
       built-in config check and of `grain state check` (`-team`,
       `-taken`, `statecheck.Options.Team` and `Taken`,
       `staterepo.CheckTeamConfig`, `TeamTakenIDs`,
       `ExportTeamConfig`, `staterepo.Config.TeamConfig` and the
       apply and sync branches it chose); the git proxy's
       `LocalRepository.ResolveDir`; the per-team target checks in
       task, schedule and suite filing; and the UI's
       `grain/config-default` (the deployment row's `configRepo` is
       `grain/config`, and the boards repo, repo list, target pickers
       and `pullRequestUrl` know no other). The git proxy answers any
       `grain/config-<anything>` "not found: only grain/config exists"
       to every authenticated sandbox, whatever its scope, and CI
       workflow sourcing refuses the name the same way before it runs
       git, so neither forwards it to a forge's `grain/` owner
       (`TestGitProxyRefusesTeamConfigRepos`,
       `TestCiWorkflowSourceRefusesTeamConfigRepoNames`). A data
       directory that still holds `team-state-repos/` starts: the
       daemon logs once that nothing reads it and leaves it in place
       for the operator to delete
       (`TestAStrayTeamStateReposDirectoryDoesNotBreakStartup`).
       `grain export` writes no team bundles, and `grain import` of an
       older bundle that lists some (`teamStateRepos`,
       `state/teams/<team>.bundle`) prints a note and restores nothing
       for them (`TestExportImportIgnoreTeamStateRepos`).
7. **Squash to one baseline schema** for SQLite and Postgres. This stage
   rewrites `docs/data-model.md` to the new table set, and the state-repo
   table format follows it.

   **Landed (task 4686), ahead of stages 3e, 4e and 6's part two.**
   The schema in `pkg/model/schema.go` is the baseline,
   `model.BaselineSchemaVersion` = 19 (`SchemaVersion` is the baseline).
   [data-model.md](data-model.md#the-schema) lists its tables. What
   stage 7 settled:

   - **No migrations.** `Store.Init` creates the schema into an empty
     store and stamps it, and does nothing else. These are gone:
     - every `ensure*`, `migrate*`, `rename*`, `populate*` and
       `backfill*` step in `pkg/model/store.go`, including the
       `UPDATE ... SET team = 'default'` backfills;
     - `repairWorkOrigins` and the `origin_ref` backfill;
     - stage 4d's `foldTeamMemory`;
     - `ensureIndexes`, and `applySchema`'s skip of an index on a
       missing column;
     - `model.DialectMigrator` and `pkg/model/postgres/migrate.go`
       (the int4 widening and the boolean conversion);
     - stage 6's `retired_schema.go` with Init's drop of it, and
       staterepo's `retired.go`.

     The seven CI cache and artifact bounds on `grain_config`
     (`ci_cache_*`, `ci_artifact_*`) were only ever added by a
     migration step, so they are now in the DDL. A fresh SQLite store
     has the same tables, columns, defaults and indexes as the full
     migration chain produced for a fresh store; this was checked
     table by table before the steps went.
   - **The guard refuses a pre-reset version.** `CheckSingleTenant`
     reads the stamp first, and `model.CheckSchemaBaseline` refuses
     any version from 1 up to the baseline with `ErrMultiTenantData`,
     before it counts a row. Its checks of the retired schema
     (`access_accounts`, cross-team approval, `capability_teams`) are
     gone: only a pre-reset store can have that schema, and the version
     refuses it first. In staterepo, `MinMigratableSchema` is gone.
     `Migratable` refuses a dump stamped below the baseline with both
     `ErrNotMigratable` and the guard's error, and `importInto` refuses
     one the same way, whatever path it arrives by.
   - **The state repository.** The dump format is unchanged: one JSON
     file per table, and the settings documents. A dump carries the
     baseline's columns, because Export reads the store. The golden
     dump is regenerated at the baseline
     (`pkg/staterepo/testdata/dump-schema-19`). The tests that aged it
     into pre-reset shapes are gone, along with every test of a
     migration step. The container upgrade suite's floor case now raises
     the baseline rather than `MinMigratableSchema`.
   - **`pkg/model/postgres/upgrade.go` stays.** It holds no schema
     migration. It is the database config file and
     `UpgradeFromSQLite`, which moves a deployment from SQLite to
     Postgres (the Database pane and `grain upgrade-postgres`) by
     exporting and re-importing at the same schema.
   - **Postgres CI.** The `postgres` job already creates every store it
     tests from scratch. `TestTheBaselineIsCreatedWithTheDialectsOwnTypes`
     now checks that a fresh baseline has no int4 column and has its
     flags as `BOOLEAN`, because no migration fixes either afterwards.
   - **Left for later.** Stage 6's part two (the `team`, `user` and
     team-column residue) lands after the baseline. It is the first
     schema change, so it bumps `SchemaVersion` to 20. This document is
     deleted once 3e, 4e and that change have landed.

## Schema inventory

At 0324dfc44 the schema (`pkg/model/schema.go`, `SchemaVersion = 17`) has
103 tables. `pkg/model/store.go` carries 91 `ensure*`, `migrate*` and
`backfill*` shims.

**Multi-tenant tables to drop in stage 6:**

- `user`, `access_accounts`, `team`, `team_user`, `team_sequence` and
  `team_pipeline_pause`;
- `team_repo_config`, after it is folded into `repo_config`;
- `team_gcp_key`, after its keys move to deployment keys;
- `cross_team_dependency_approval`;
- `task_grant`, `task_grant_wait`, `template_grant` and `schedule_grant`;
- the `*_read` and `*_write` access-list tables (`task_read`, `task_write`,
  `template_read`, `template_write`, `schedule_read` and `schedule_write`),
  once stage 3 confirms that no single-tenant code reads them.

  Stage 5 found that these last two items are not multi-tenant.
  `task_grant`, `template_grant` and `schedule_grant` are the
  capabilities a task, template or schedule holds, and `task_grant_wait`
  is admission's wait for one. `task_read`/`task_write` and their
  counterparts are read and write targets. Single-tenant grain reads all
  of them, so stage 6 should keep them.

**Team columns to drop:**

- `board.team`, `ci_run.team`, `custom_field.team`, `executor_token.team`,
  `memory.team`, `memory_document.team`, `schedule.team`, `suite.team`,
  `suite_run.team`, `task.task_team`, `task.team` and `template.team`;
- `credential_access_audit.team_id` and `secret_resolution_audit.team_id`.

`task.team` was unread and unwritten since `Task.Team` was removed (task
4844), and went with the rest (6-ii-b5, schema 25).

**Legacy settings columns to drop from `grain_config`:**

- `agent_framework`, `gemini_model`, `gemini_effort`, `codex_effort` and
  the other single-framework model columns;
- `github_host`, once connections own the host;
- any column that stores the credentials ladder.

The dead-schema sweep is also part of stage 6. It looks for tables and
columns that no code reads, left behind by features removed earlier. Grep
these for every table and column:

- `pkg/model`, `pkg/model/sqlite` and `pkg/model/postgres`, including the
  Postgres adapter's `tablePrimaryKeys` and `boolColumns`;
- `pkg/staterepo`.

The stage 6 pull request lists what it drops and why.
