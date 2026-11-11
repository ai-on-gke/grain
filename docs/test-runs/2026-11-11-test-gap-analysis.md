# Test-gap analysis of grain core — 2026-11-11

Task 4376. This covers `ai-on-gke/grain` `main` at `ec371e80b`
(2026-11-11). It measures where grain core's tests are missing, ranks
the packages by risk and splits the work into sub-tasks T1–T15 (plus E1
for the edition). The sub-tasks write the tests. This document writes
none.

Why now: two bugs reached v3.5.0 untested.

- **#4225:** `grain_submit_task` declared an array with no `items`, and
  Gemini rejected the whole tool list.
- **#4257:** adding a repo dropped its host. Every `pkg/ui` test built
  `ui.Config` with `RepoNames` nil, so the real lookup never ran.

Separately, the live real-model suites had stopped running without
anyone noticing (#4344).

The companion report `docs/reviews/2026-11-complexity-review.md`
(#4352) ranks what to *simplify*. This one ranks what to *test*, and
most of its "tests first" items appear below with an owner.

## Method

1. **Line coverage per package.** `go test -coverprofile` over
   `./pkg/... ./cmd/...` in a sandbox carrying the CI guest packages
   (`bubblewrap expect zstd socat psmisc sqlite3 postgresql`). Each
   package is measured by **its own tests only**, which is what a
   per-package floor (T15) can check cheaply. Packages exercised mostly
   from elsewhere read low on this basis. The two biggest cases were
   re-measured with `-coverpkg`:
   - `pkg/model` is 49.3% from its own tests and **67.8%** with
     `pkg/model/sqlite`'s tests added;
   - `pkg/change` stays at 19.1% even with `pkg/orchestrator`'s tests
     added.

   Every package passed, including `pkg/model/postgres` against a real
   embedded PostgreSQL 15. Coverage of `tests/e2e`, `tests/deploy` and
   `ui/e2e` was not measured.
2. **Churn and fix history.** Using `git log --since=6.weeks --no-merges
   --numstat` over non-test Go files, a commit counts as a fix when its
   subject starts with `fix` or `revert`.
3. **Trust boundary.** A package counts as trust-boundary if
   `pkg/trustboundary/trustboundary.go` (v28) classifies the package or
   any file in it.
4. **Risk** is `(fix commits + 1) × (1 + churn / 10,000 lines) × (2 if
   trust boundary)`. It is a ranking aid, not a measure. Appendix A
   gives the inputs for every package.
5. **Uncovered functions.** `go tool cover -func` was intersected with
   files that had ≥ 4 fix commits or ≥ 15 commits in the window
   (Appendix B).
6. **Structural scans** (scripts in the task transcript):
   - every `ui.Config`, `orchestrator.Config` and `orchestrator.Deps`
     hook was counted by how many test files set it against how many
     `== nil` guards read it;
   - every agent API route in `pkg/ui/agent_api_tools.go` was matched
     against `pkg/ui`, `tests/e2e` and `ui/e2e` tests, and checked for a
     refusal (403) case;
   - every data-rewriting migration past v27 was matched against a test
     that names its version;
   - zero-coverage `cmd*` entry points were listed for `pkg/cli`,
     `pkg/sandboxcli` and `pkg/mcp`.

   These scans are heuristics, so each sub-task confirms a gap before
   filling it.

## Summary table by package

These are the 40 riskiest packages. Appendix A lists all 160.

| Package | Cov. | 6-wk fixes / commits | TB | Top gaps | Owner |
|---|---|---|---|---|---|
| `pkg/ui` | 78.1% | 612 / 1,977 | yes | 9 `Config` hooks never set by any test (incl. `WaitCredit`, `StoreSwitch`, `HostLoad`, `ArchivedTranscripts`, `SettingsHistory`); 41 more set by exactly one test file; route permission matrix (43 routes never named literally in a test, 8 with no refusal case); `sandbox_cli_actions.go` 20/23 funcs at 0%; `console.go`, `notifications.go` handlers at 0% | T4, T5 |
| `pkg/orchestrator` | 80.4% | 334 / 1,106 | yes | `taskgoals.go` 63.8% (`checkGoal` 41%, `openPipelineNotifications` 0%, `applyGoalLimit` 42%); `reviewquestions.go` 62%; `securityhold_inplace/submit` ~70%; `takeover_adopt.go` CI-green check 15–43%; `Deps.DispatchHalted`, `ApprovalCallback`, `MintSandboxGrant`, `DiskSpace` never set by a test | T1, T2, T3 |
| `pkg/model` | 49.3% (67.8% w/ sqlite) | 272 / 1,332 | yes | `resume_on.go` 34%, `task_goal.go` 27%, `human_review_hold.go` 9%, `suspended_session.go` 0%; 16 of 43 data-rewriting migrations have no version-named upgrade test; `store.go` 143 of 264 funcs at 0% in its own package | T2, T3, T6 |
| `pkg/daemon` | 61.4% | 228 / 888 | yes | `sandbox_cli.go` 31/35 funcs at 0% (the server behind the in-sandbox `grain` CLI); `mcpserver.go` 30/94 at 0% (`OpenPullRequest`, `RecreateSandbox`, `RequestHumanReview`...); `statepullrequests.go` 56/92; `ci.go` 34/107; `statemanager.go` 20/48 | T10 |
| `pkg/cli` | 71.1% | 146 / 640 | | 22 command entry points at 0% (`cmdMCP`, `cmdFS`, `cmdForceMerge`, `cmdCompact`/`Brief`/`Rollover`, `cmdHostRekey`/`Codes`/`RotateCode`/`RevokeCode`, `cmdCIArtifact`, `cmdDemo`, `runDaemonCommand`, ...) | T11 |
| `pkg/mcp` | 85.2% | 55 / 338 | yes | `resume_later` tool constructor and handler 0%; `notify_operator`/`message_task`, `request_human_review`, `query_merge_queue`, `wait_for_task` constructors 0%; `CapabilityForTool`, `AgyQualifiedToolName` 0%; hand-kept tool-role lists disagree (`wait_for_task`) | T11 |
| `pkg/gitproxy` | 75.7% | 62 / 221 | yes | `cli.go` `serveIssues`, `serveDBQuery` 0%; `namespace.go` `authenticateSandbox`, `handleNamespaceRequest` 0%; `cli_actions.go` `serveAPIRoutes` 0% | T8 |
| `pkg/upgrade` | 86.7% | 34 / 128 | yes | `CheckAllowedBranches`, `ValidBranchName` 0%; daemon side `daemon/upgrade.go` 5/15 funcs at 0% | T12 |
| `pkg/hostd` | 75.5% | 45 / 138 | yes | `remote.go` `WaitConnected`, `ReplayCache`, `RenameSession`, `SharedTokenOwner` 0%; `carry.go` error type 0% | T9 |
| `pkg/staterepo` | 78.7% | 31 / 149 | yes | `decl.go` overlap-mapping decode 0%; `ConfigureMirror`, `SetSSHKeyFile` 0% | T6 |
| `pkg/ci` | 75.4% | 25 / 114 | yes | `external.go` (GHA run lookup) 4/4 funcs at 0% | T12 |
| `pkg/demo` | 60.9% | 49 / 189 | | `demo.go` 14/49 funcs at 0% | T10 |
| `pkg/capability/intree` | 78.4% | 14 / 79 | yes | `NewConfigurableGCP`, gcp-token `held` 0% | T7 |
| `pkg/sandboxcli` | 79.4% | 48 / 84 | | `Main`, `IsCommand`, `Unknown` 0%; `parseNotBeforeFlag` 0%; `help.go` 17 fixes in 34 commits | T11 |
| `pkg/trustboundary` | 90.5% | 23 / 37 | yes | no test that every file under a sensitive dir is classified or exempt (10 "classify X" catch-ups, v17→v28) | T3 |
| `pkg/github` | 71.5% | 26 / 74 | | 17 of 131 funcs at 0% (`RerunFailedJobs`, `JobLogTail`, `DeleteBranch`, `ConflictFiles`, `PullRequestDiff`, ...) and no check that `githubsim` answers in GitHub's shapes | T8 |
| `pkg/capability/protocol` | 84.0% | 15 / 76 | yes | capability-server protocol has no versioned conformance fixture | T7 |
| `pkg/workflows` | 32.0% | 12 / 73 | yes | most of the package untested | T12 |
| `pkg/namespace` | 69.9% | 30 / 51 | | `resolveExternalCI` / `externalStep` / `attemptCombinedLog` (GHA in `ci://`) 0%; `PermitsSrc` 0% | T8 |
| `pkg/hostinit` | 79.6% | 8 / 43 | yes | — (adequate) | T9 |
| `pkg/repodecl` | 90.7% | 10 / 50 | yes | `TemplatePush` 0% | T8 |
| `pkg/github/githubsim` | 66.3% | 18 / 30 | | merge-queue land/drop, GraphQL review threads 0% | T8 |
| `pkg/secrets` | 79.7% | 9 / 26 | yes | `GrainHeld`, `AgentBackendCredentialSecret`, `KeySource` 0% | T13 |
| `pkg/policy` | 85.6% | 9 / 18 | yes | — | T7 |
| `pkg/agent` | 65.7% | 11 / 92 | | `IsControlTool`, `MCPServerArgs` 0% | T11 |
| `pkg/grit/engine` | 46.5% | 7 / 20 | yes | 13 of 42 funcs at 0% (`Resume`, cache lookup, flight claims) | T12 |
| `pkg/agent/codex` | 78.8% | 10 / 57 | | `Run`, host-auth wiring 0% | T11 |
| `pkg/model/postgres` | 73.5% | 10 / 57 | | `adapter.go` prepared-statement/tx paths 0% | T6 |
| `pkg/webauth` | 70.7% | 4 / 17 | yes | refusal paths | T13 |
| `pkg/hub/server` | 57.7% | 5 / 12 (+ `pkg/registry` history) | yes | low coverage on young code | T14 |
| `pkg/redact` | 70.2% | 4 / 9 | yes | `RedactHostEnrollmentCodes`, `KnownCount` 0% | T13 |
| `pkg/hub` | 52.4% | 4 / 9 (+32 fixes as `pkg/registry`) | yes | low coverage | T14 |
| `pkg/runhost` | 77.7% | 3 / 35 | yes | — | T9 |
| `pkg/capability/grainserver` | 75.0% | 3 / 19 | yes | 12 grant predicates at 0% (`IsSQLWriteGrant`, `IsHostExecGrant`, ...) | T7 |
| `pkg/ci/cli` | 27.0% | 3 / 7 | yes | most untested | T11 |
| `pkg/change` | 19.1% | 6 / 24 | | seam barely tested even with orchestrator's tests | T8 |
| `pkg/capability/grainci` | 58.3% | 2 / 6 | yes | (coordinate with #4361/#4364) | T7 |
| `pkg/capability/release` | 30.4% | 2 / 3 | yes | most untested | T7 |
| `pkg/agentd` | 63.9% | 4 / 15 | | allowlist / first-allow paths | T9 |
| `pkg/registryauth` | 53.1% | — | | refusal paths | T13 |

Packages at 0% that hold logic: `cmd/grain`, `cmd/grain-hub`,
`pkg/cli/skill`, `pkg/hubenv` and `pkg/gritapi` (5.7%). Test helpers
(`*test`, `kmsfake`, `gcpfake`, `v2baseline`) are excluded from the gaps.

## Structural gaps

### S1. Optional dependencies left nil in every test (the #4257 shape)

**`ui.Config`** (`pkg/ui/labels.go`) has 93 hook-like fields.

- **Never set by any test anywhere**, though production code guards on
  them: `WaitCredit`, `StoreSwitch`, `HostLoad`, `ArchivedTranscripts`,
  `SettingsHistory`, `CLIRunCheck`, `SandboxImageMirrorFor`,
  `TriggerIAPSync` and `CheckClusterImageConnectivity`.
  `AccessSessionLifetime` and `AccessCodeMode` are never set either, so
  only their zero values run. → **T5**
- **Set by exactly one `pkg/ui` test file** (41 hooks): `RepoComparer`,
  `IssueImporter`, `GitHubPullRequests`, `GitHubPullRequestFiles`,
  `GitHubForkPullRequests`, `GitHubCredentialAvailable`,
  `ForbiddenRepos`, `AutoMergeDegraded`, `DispatchConditions`,
  `StateRepoSyncFailure`, `Overcommit`, `PackageCache`, `ActionsCache`,
  `AgentKeyChecks`, `HubStatus`, `TriggerHubSync`, `Consoles`,
  `HostExecutor`, `Restart`, `Shutdown`, `Reboot`, `SandboxRecreate` and
  others. → **T5**

**`orchestrator.Deps`** (`cycle.go`):

- never set by a test: `DispatchHalted`, `ApprovalCallback`,
  `MintSandboxGrant` (3 guards) and `DiskSpace` (2 guards);
- set by one test file: `GitReach`, `ModelAvailability`, `PolicyAudit`,
  `UsageCap`, `RunHost`, `CycleTimes` and `Refs`.

→ **T1**

**`orchestrator.Config`:**

- never set by a test: `ControllerMCP`, `TranscriptSink` and
  `StateRepoCheck`;
- set by one test file: `CIPreempter`, `CheckImage`, `RestartGap`,
  `UpgradeInterrupts` and `LatestRelease`.

→ **T1** (T12 for `LatestRelease`/`UpgradeInterrupts`)

### S2. Error and refusal paths with no test

- **`pkg/ui/access_roles.go`:** `grantDeniedByRole`,
  `roleConsoleSessionWithin`, `isUnconditionalPersonOnlyRoute`,
  `delegatedRefusalError` and `persistentSandboxRepo` are at 0%.
  → **T4**
- **`pkg/ui/agent_api_tools.go`:** `emptyRouteAnswer` and
  `getTaskAsExternal`, the external-credential read path, are at 0%.
  → **T4**
- **`pkg/gitproxy`:** `authenticateSandbox` and `handleNamespaceRequest`
  (`namespace.go`) are at 0%, so the sandbox-credential refusal on
  `ci://`, `src://` and the other namespace roots is untested at the
  proxy. → **T8**
- **`pkg/model`:** `CatalogueRequiresPersonNote`, `UngrantedWritesNote`,
  `UngrantedReadsNote`, `MissingGCPKeyRefusal`, `HeldForApprovalNote`
  and `CheckCapabilityGrantSyntax` are at 0% in their own package.
  → **T7**
- **`pkg/cli/login.go`:** `loginRefused` is at 0%. → **T11**

### S3. Agent API routes without a route-level allowed and refused test

`pkg/ui/agent_api_tools.go` declares **277 agent routes**.

- **43** never appear as a literal path in any test. This counts paths
  built by concatenation as missing, so it is an upper bound:
  `DELETE /api/console/{id}`, `POST /api/console/{id}/input`,
  `POST /api/console/{id}/resize`, `GET /api/console`,
  `PUT|DELETE /api/tasks/{id}/goal`, `POST /api/tasks/{id}/rollover`,
  `/compact`, `/brief`, `/pause`, `/resume`, `/undefer`,
  `/withdraw-approval`, `/retry-merge`, `/human-review`,
  `/human-review/release`, `/pull-request`, `/sandbox/recreate`,
  `/depends-on`, `GET /api/tasks/{id}/context`,
  `POST /api/capability-servers/{enable,test,test-script,rotate-managed,dismiss-notice,figma-*,gchat-test}`,
  `POST /api/capability-policy/preview`,
  `POST /api/external-agents/tokens/revoke-all`,
  `POST /api/hostds/delete-revoked`,
  `POST /api/notifications/{id}/clear`,
  `POST /api/package-cache/flush`,
  `POST /api/repos/{owner}/{name}/candidates/{id}/qualification/approve`,
  `PUT /api/settings/cli-run`, `PUT /api/usage/settings`,
  `GET /api/deployment/audit-logs`, `GET /api/memory/documents/{name}`,
  `GET /api/release-approval-key` and
  `GET /api/templates/{id}/composition`.
- **4** are never referenced at all, even loosely:
  `GET /api/capability-servers/gchat-usage`,
  `GET /api/capability-servers/host-entries`, `GET /api/console` and
  `GET /api/deployment/audit-logs`.
- **8** are named only in test files with no refusal case:
  `GET /api/api-scope-sets/usage`, `GET /api/capability-servers/usage`,
  `GET /api/ci/faults`, `GET /api/console/{id}/output`,
  `GET /api/host/top`, `GET /api/merge-batches`,
  `GET /api/release-notes/versions` and `GET /api/states`.

Existing tests iterate `AgentAPIRoutes()` for attribution, naming and
search (`agent_api_internal_test.go`, `mutating_routes_test.go`), but
none asserts **for every route** that a caller with the route's scope is
let through and a caller without it is refused. → **T4**

### S4. State transitions with no test

File-level coverage of the lifecycle code (orchestrator's own tests; for
`pkg/model`, model + sqlite tests):

| File | Cov. | Untested transitions | Owner |
|---|---|---|---|
| `model/human_review_hold.go` | 8.9% | hold placed, released, inherited by a successor | T3 |
| `model/task_goal.go` | 26.7% | goal kinds other than the common ones; limit reached → hold vs notify; `on_met: rest` | T2 |
| `model/resume_on.go` | 34.0% | `resume_later` CI wait on `ci_mode: github` (GHA verdict) vs grain CI; a run id that never reports; `message: true`; task-closed wake | T2 |
| `model/suspend.go` / `suspension.go` / `suspended_session.go` | 51–54% / 0% | park → wake → `resume_at` released (the `4e47815f8` bug); suspended interactive session | T2 |
| `orchestrator/taskgoals.go` | 63.8% | `pipeline-healthy` goal (`openPipelineNotifications` 0%), `applyGoalLimit` | T2 |
| `orchestrator/reviewquestions.go` | 62.3% | `dropReviewQuestions` 0% | T3 |
| `orchestrator/securityhold_inplace.go` / `_submit.go` | 68% / 72% | in-place hold on an Actions repo (`d69f5e6d3`), Submit during hold | T3 |
| `orchestrator/takeover_adopt.go` | 72.5% | `latestCIRun` / `takeoverCIGreen` (15% / 43%) | T1 |
| `orchestrator/mergegates.go` / `mergerecheck.go` | 74% / 74% | gate refusal reasons, recheck after base moved | T3 |
| `orchestrator/pathtrigger.go` | 74.2% | `putFiledTask` 36%: two triggers on one head file one review (#4093) | T3 |
| `orchestrator/wake.go` | 75.0% | CI-verdict wake under both CI modes | T2 |
| `orchestrator/rollover.go` / `durable.go` | 73% / 90% | rollover refused / carried grants | T2 |

None of these has a model-level property test. The complexity review
(area 8) asks for one: random sequences of operations with an
invariant check after each step. → **T2**. Delivery of comments on a
reopened task stays with **#4278**.

### S5. Migrations without an up-from-old-data test

107 migrations follow the v27 baseline (now `SchemaVersion = 139`). Of
them, 43 rewrite data (an `Up` func, `UPDATE`, `INSERT` or `DELETE`),
and **16 have no test naming their version**:

| Version | What it rewrites |
|---|---|
| 45 | suspensions and the dispatch standing a run started with |
| 50 | `resume_on_deadline` and parks as requested suspensions |
| 68, 69, 71, 72, 75, 76 | dispatch-control, pipeline-control, memory, release-notes, self-upgrade and test-quarantine grants to `api:*` |
| 81, 84, 85 | deployment-health, self-repair (incl. `repo_config.ci_capabilities`) and git-push-any-ref grants |
| 95 | `max_merges_per_repo` default 1 → 5, stored values kept |
| 101 | image-signing, registry-push and package-publish grants, incl. `repo_config.ci_capabilities` |
| 104 | custom command connections disabled and kept |
| 113 | `task.owner` and `task.proposer_*` backfilled, non-empty owner enforced |
| 139 | `auto_upgrade_min_interval_hours` and `auto_upgrade_release.succeeded_at` |

Some of these may be covered by behaviour tests that don't name the
version (`legacy_grant_migration_test.go` covers several grant
rewrites). Each one needs a seeded-old-row test on **both SQLite and
Postgres**. `ApplyDataMigrations` and `raiseMaxMergesPerRepoDefault` are
at 0% in their own package. → **T6**

### S6. CLI commands without help or behaviour tests

`TestHostCLISpecCoversEveryCommandAndFlag` and the generated
`design/spec/cli/*.spec.md` already guarantee that **help exists** for
every command. These **behaviour** entry points are at 0%:

- `pkg/cli`: `cmdMCP`, `cmdFS`, `cmdForceMerge`, `cmdCompact`,
  `cmdBrief`, `cmdRollover`, `cmdHostCodes`, `cmdHostRotateCode`,
  `cmdHostRevokeCode`, `cmdHostRekey`, `cmdHostUpdate`,
  `cmdCIArtifact`, `cmdCapabilityServersAdd`/`Bind`,
  `cmdRepoCICapabilities`, `cmdGitAmbient`, `cmdSandbox`, `cmdDemo`,
  `runDaemonCommand`, `runHostCmd` and `runCodexDetached`;
- `pkg/cli/init_workstation.go` (4 funcs) and `pkg/cli/sync.go`
  `syncCmd`;
- `pkg/sandboxcli`: `Main` and `parseNotBeforeFlag`;
- `pkg/ci/cli` is at 27%, and `cmd/grain` and `pkg/cli/skill` are at 0%.

→ **T11**

### S7. Contract surfaces without a conformance test

| Surface | State | Owner |
|---|---|---|
| Model-provider tool schemas (Gemini/Claude/OpenAI) across every MCP surface | partial lints (`pkg/cli/mcp_array_items_test.go`, `pkg/ui/mcp_endpoint_test.go`) | **#4344** (do not duplicate); T11 adds the tool-*role*-list consistency test only |
| GitHub REST/GraphQL as simulated by `githubsim` | no fixture check that `githubsim` answers in GitHub's documented shapes; 17 `pkg/github` client funcs at 0% | T8 |
| Non-GitHub forges (command forge, GitLab-like hosts) | `commandforge_*_test.go` exist; e2e is **#4345** | T8 (unit), #4345 (e2e) |
| CI verdicts: grain CI vs GitHub Actions | `forge.Verdicts` tests use fakes only; no suite running both backends through the same cases | T2 (wake/resume), T12 (`ci/external.go`) |
| hostd ↔ daemon protocol | `pkg/hostd/contract_test.go` and `tests/agentdcompat` (built from released tags) exist; reconnect/replay (`WaitConnected`, `ReplayCache`) untested | T9 |
| Capability-server protocol | `pkg/capability/protocol` 84%, no versioned golden fixture | T7 |
| Hub API (`pkg/hub/api`) | 56.4%, no wire-compat fixture for stored JSON | T14 |
| In-sandbox `grain` CLI ↔ daemon (`daemon/sandbox_cli.go`, `ui/sandbox_cli_actions.go`) | server side 0% in-package (31/35 and 20/23 funcs) | T10 |

## Sub-tasks

Each sub-task's body repeats its gaps from this document and stands on
its own. Every one carries the same rules:

- Write the missing tests. Fix bugs they expose if the fix is small;
  otherwise file a task naming the failing test.
- No product refactors beyond what a fix needs.
- Keep new tests fast enough for the per-PR GitHub Actions run, or tag
  them for nightly and say why.
- Tests must exercise real stores and real code paths, with fakes only
  at process or network edges.

| ID | Area | Packages | Key gaps | Coordinates with |
|---|---|---|---|---|
| T1 | Orchestrator dispatch and run lifecycle | `pkg/orchestrator` (run, cycle, finish, adopt/takeover, capability_pass, session_sandboxes), `pkg/dispatch`, `pkg/lifecycle` | nil `Deps`/`Config` hooks; `relayProposedTasks` vs `CreateTask` parity; takeover CI-green | #4278, #4249 |
| T2 | Goals, deferral, suspension and resume | `pkg/orchestrator` (taskgoals, wake, waitcredit, rollover, durable), `pkg/model` (task_goal, resume_on, suspend*) | S4 rows; `resume_later` CI waits on both CI modes; property test | #4361/#4364 |
| T3 | Merge queue, gates, security hold, reviews and triggers | `pkg/orchestrator` (merge*, securityhold*, pathtrigger*, review*, humanreviewhold, basehold), `pkg/model/human_review_hold.go`, `pkg/trustboundary` | hold transitions; one review per head; classification hygiene test | — |
| T4 | API gate: route permission matrix | `pkg/ui` (agent_api_gate, agent_api_tools, access_roles, route_scopes, external_agents) | S3; S2 refusal funcs; gate-vs-handler body parity | — |
| T5 | `ui.Config` hooks and the `ui.Client` paths behind them | `pkg/ui` (client, settings, tasks, notifications, console, httpclient, pull_requests_api, hostds) | S1 hooks; 0% handlers | #4249, #4278 |
| T6 | Model store and migrations | `pkg/model`, `pkg/model/sqlite`, `pkg/model/postgres`, `pkg/staterepo` | S5 (16 migrations, both dialects); postgres adapter; store funcs | — |
| T7 | Capabilities, grants and admission | `pkg/capability/...`, `pkg/model` (canonical_grant, capability_*, agent_api, auto_submit, hold_for_approval), `pkg/policy`, `pkg/sandboxaccess` | `release` 30%, `githubtoken` 52%, `grainci` 58%, `grainserver` predicates; protocol fixture | #4361/#4364 |
| T8 | gitproxy, forge, GitHub and multi-host | `pkg/gitproxy`, `pkg/forge`, `pkg/change`, `pkg/github`, `githubsim`, `pkg/namespace`, `pkg/repodecl`, `pkg/ghwebhook` | off-default-host cases; `githubsim` shape check; namespace auth | #4345, #4249 |
| T9 | hostd, agentd, runhost and tunnels | `pkg/hostd`, `pkg/agentd`, `pkg/runhost`, `pkg/tunnel/...`, `pkg/hostinit`, `pkg/k8s`, `pkg/sandboximage` | reconnect/replay; agentd allowlist; wsupgrade 65% | #4360 |
| T10 | Daemon wiring and sandbox-facing servers | `pkg/daemon`, `pkg/demo` | `sandbox_cli.go`, `mcpserver.go`, `statepullrequests.go`, `ci.go`, `statemanager.go` | — |
| T11 | CLI, sandboxcli and MCP tool surfaces | `pkg/cli`, `pkg/sandboxcli`, `pkg/ci/cli`, `pkg/mcp`, `pkg/agent/...`, `cmd/grain` | S6; MCP tool constructors at 0%; tool-role list consistency | #4344, #4366 |
| T12 | Upgrade, release train, CI engine and workflows | `pkg/upgrade`, `pkg/orchestrator/release*`, `pkg/ci`, `pkg/grit/...`, `pkg/gritapi`, `pkg/workflows`, `pkg/releaseapproval` | `workflows` 32%, `grit/engine` 47%, `ci/external.go` 0% | — |
| T13 | Secrets, redaction and auth | `pkg/secrets`, `pkg/secretsrepo`, `pkg/redact`, `pkg/webauth`, `pkg/jwks`, `pkg/registryauth`, `pkg/iapreconciler` | 0% funcs; refusal paths | — |
| T14 | Hub | `pkg/hub/...`, `pkg/hubenv`, `cmd/grain-hub` | 52–61% across the tree; API wire fixture | #4360 |
| T15 | Per-package coverage ratchet in CI | `scripts/`, `.github/workflows/tests.yml` | floors from Appendix A, ratchet up only | — |
| E1 | Same analysis for the edition | the edition repository | uitrust, corpwebauth, command forge, capability servers, installer | — |

The closing note on task 4376 records the grain task number of each
sub-task.

## Not covered here

- **#4352 / #4353**, the complexity and review studies, are reports
  only. This analysis cites #4352 where its "tests first" items match.
- **#4366** (legacy-name lint) and **#4360** (hub tunnel flake) are
  referenced and not duplicated.
- UI (React/Angular) unit and Playwright coverage was not measured. It
  is not in "grain core's Go code" as scoped.

## Appendix A: every package

Coverage is from the package's own tests (`go test -coverprofile`). The
churn, commit and fix counts cover non-test Go files from 2026-09-30 to
2026-11-11. The `pkg/hub` history before the rename is under
`pkg/registry` (80 commits, 32 fixes) and is not folded in. Packages
with no tests show 0.0%.

| Package | Line coverage | Churn (lines) | Commits | Fix commits | Trust boundary | Risk | Owner |
|---|---|---|---|---|---|---|---|
| `pkg/ui` | 78.1% | 119,396 | 1977 | 612 | yes | 15864 | T4/T5 |
| `pkg/orchestrator` | 80.4% | 100,883 | 1106 | 334 | yes | 7429 | T1/T2/T3 |
| `pkg/model` | 49.3% | 102,960 | 1332 | 272 | yes | 6168 | T6 |
| `pkg/daemon` | 61.4% | 54,123 | 888 | 228 | yes | 2937 | T10 |
| `pkg/cli` | 71.1% | 44,903 | 640 | 146 |  | 807 | T11 |
| `pkg/mcp` | 85.2% | 28,391 | 338 | 55 | yes | 430 | T11 |
| `pkg/gitproxy` | 75.7% | 19,544 | 221 | 62 | yes | 372 | T8 |
| `pkg/upgrade` | 86.7% | 16,915 | 128 | 34 | yes | 188 | T12 |
| `pkg/hostd` | 75.5% | 9,388 | 138 | 45 | yes | 178 | T9 |
| `pkg/staterepo` | 78.7% | 17,320 | 149 | 31 | yes | 175 | T6 |
| `pkg/ci` | 75.4% | 13,164 | 114 | 25 | yes | 120 | T12 |
| `pkg/demo` | 60.9% | 9,589 | 189 | 49 |  | 98 | T10 |
| `pkg/capability/intree` | 78.4% | 10,868 | 79 | 14 | yes | 63 | T7 |
| `pkg/sandboxcli` | 79.4% | 1,515 | 84 | 48 |  | 56 | T11 |
| `pkg/trustboundary` | 90.5% | 1,611 | 37 | 23 | yes | 56 | T13 |
| `pkg/github` | 71.5% | 9,506 | 74 | 26 |  | 53 | T8 |
| `pkg/capability/protocol` | 84.0% | 6,243 | 76 | 15 | yes | 52 | T7 |
| `pkg/workflows` | 32.0% | 7,901 | 73 | 12 | yes | 47 | T12 |
| `pkg/namespace` | 69.9% | 4,587 | 51 | 30 |  | 45 | T8 |
| `pkg/hostinit` | 79.6% | 7,890 | 43 | 8 | yes | 32 | T9 |
| `pkg/repodecl` | 90.7% | 3,091 | 50 | 10 | yes | 29 | T8 |
| `pkg/github/githubsim` | 66.3% | 4,536 | 30 | 18 |  | 28 | T8 |
| `pkg/secrets` | 79.7% | 3,593 | 26 | 9 | yes | 27 | T13 |
| `pkg/policy` | 85.6% | 1,122 | 18 | 9 | yes | 22 | T7 |
| `pkg/agent` | 65.7% | 4,922 | 92 | 11 |  | 18 | T11 |
| `pkg/grit/engine` | 46.5% | 976 | 20 | 7 | yes | 18 | T12 |
| `pkg/agent/codex` | 78.8% | 4,825 | 57 | 10 |  | 16 | T11 |
| `pkg/model/postgres` | 73.5% | 3,615 | 57 | 10 |  | 15 | T6 |
| `pkg/pkgcache` | 80.9% | 7,414 | 42 | 3 | yes | 14 | T9 |
| `pkg/webauth` | 70.7% | 2,941 | 17 | 4 | yes | 13 | T13 |
| `pkg/hub/server` | 57.7% | 133 | 12 | 5 | yes | 12 | T14 |
| `pkg/agent/antigravity` | 86.9% | 7,021 | 63 | 6 |  | 12 | T11 |
| `pkg/capability/servers` | 88.3% | 1,302 | 15 | 4 | yes | 11 | T7 |
| `pkg/capability/localscripts` | 90.2% | 1,180 | 11 | 4 | yes | 11 | T7 |
| `pkg/capability/gcpsets` | 74.5% | 3,909 | 34 | 3 | yes | 11 | T7 |
| `pkg/redact` | 70.2% | 353 | 9 | 4 | yes | 10 | T13 |
| `pkg/hub` | 52.4% | 145 | 9 | 4 | yes | 10 | T14 |
| `pkg/runhost` | 77.7% | 2,400 | 35 | 3 | yes | 10 | T9 |
| `pkg/capability/grainserver` | 75.0% | 1,143 | 19 | 3 | yes | 9 | T7 |
| `pkg/staterepo/statecheck` | 78.6% | 950 | 16 | 3 | yes | 9 | T6 |
| `pkg/ci/cli` | 27.0% | 858 | 7 | 3 | yes | 9 | T11 |
| `pkg/change` | 19.1% | 1,580 | 24 | 6 |  | 8 | T8 |
| `pkg/k8s` | 68.8% | 2,770 | 13 | 2 | yes | 8 | T9 |
| `cmd/ghcr-prune` | 84.6% | 2,080 | 19 | 5 |  | 7 | T12 |
| `pkg/capability/packagepublish` | 91.9% | 591 | 7 | 2 | yes | 6 | T7 |
| `pkg/forge` | 71.5% | 2,686 | 31 | 4 |  | 6 | T8 |
| `pkg/capability/modelproxy` | 89.0% | 495 | 6 | 2 | yes | 6 | T7 |
| `pkg/capability/grainci` | 58.3% | 237 | 6 | 2 | yes | 6 | T7 |
| `pkg/capability/release` | 30.4% | 207 | 3 | 2 | yes | 6 | T7 |
| `pkg/hub/api` | 56.4% | 113 | 6 | 2 | yes | 6 | T14 |
| `pkg/hub/providers/gcp` | 72.6% | 46 | 4 | 2 | yes | 6 | T14 |
| `pkg/sandboxaccess` | 67.3% | 1,867 | 20 | 4 |  | 6 | T7 |
| `pkg/agentd` | 63.9% | 1,791 | 15 | 4 |  | 6 | T9 |
| `pkg/reviewedmerge` | 82.9% | 539 | 7 | 4 |  | 5 | T8 |
| `pkg/grit` | 64.7% | 2,911 | 21 | 1 | yes | 5 | T12 |
| `pkg/dispatch` | 79.5% | 1,589 | 14 | 3 |  | 5 | T1 |
| `pkg/lifecycle` | 85.5% | 1,482 | 10 | 1 | yes | 5 | T1 |
| `pkg/metrics` | 93.8% | 5,169 | 37 | 2 |  | 5 | T12 |
| `pkg/sandboximage` | 64.8% | 1,357 | 12 | 1 | yes | 5 | T9 |
| `pkg/granule` | 82.0% | 1,253 | 7 | 1 | yes | 5 | T9 |
| `pkg/tunnel/mux` | 82.9% | 1,237 | 7 | 1 | yes | 4 | T9 |
| `pkg/capability/githubsandbox` | 84.2% | 1,088 | 8 | 1 | yes | 4 | T7 |
| `pkg/grit/artifacts` | 60.7% | 1,042 | 5 | 1 | yes | 4 | T12 |
| `pkg/capability/ciapi` | 88.1% | 1,030 | 11 | 1 | yes | 4 | T7 |
| `pkg/capability/portforward` | 70.3% | 734 | 9 | 1 | yes | 4 | T7 |
| `pkg/installtrack` | 64.4% | 704 | 8 | 1 | yes | 4 | T12 |
| `pkg/capability/dbaccess` | 86.0% | 671 | 7 | 1 | yes | 4 | T7 |
| `pkg/capability` | 92.5% | 646 | 12 | 1 | yes | 4 | T7 |
| `cmd/grit` | 75.3% | 628 | 15 | 1 | yes | 4 | T12 |
| `pkg/capability/gcpsets/gcpfake` | 0.0% | 520 | 6 | 1 | yes | 4 | T7 |
| `pkg/grit/steps` | 63.5% | 455 | 11 | 1 | yes | 4 | T12 |
| `pkg/releaseapproval` | 79.3% | 391 | 5 | 1 | yes | 4 | T12 |
| `pkg/registryauth` | 53.1% | 259 | 3 | 3 |  | 4 | T13 |
| `pkg/agent/claude` | 84.6% | 3,593 | 41 | 2 |  | 4 | T11 |
| `pkg/hub/reconcile` | 72.9% | 40 | 3 | 1 | yes | 4 | T14 |
| `pkg/hub/store` | 60.7% | 16 | 2 | 1 | yes | 4 | T14 |
| `pkg/declschema` | 100.0% | 1,356 | 22 | 2 |  | 3 | T12 |
| `pkg/objectstore` | 68.7% | 1,315 | 7 | 2 |  | 3 | T9 |
| `pkg/agent/sessionstore` | 67.4% | 1,108 | 6 | 2 |  | 3 | T11 |
| `pkg/cli/skill` | 0.0% | 496 | 4 | 2 |  | 3 | T11 |
| `pkg/forkhead` | 84.4% | 200 | 3 | 2 |  | 3 | T8 |
| `cmd/grain-sandbox` | 86.7% | 108 | 7 | 2 |  | 3 | T9 |
| `pkg/modelproxy` | 89.4% | 2,657 | 14 | 0 | yes | 3 | T6 |
| `pkg/capability/githubtoken` | 52.1% | 1,781 | 12 | 0 | yes | 2 | T7 |
| `pkg/localexec` | 82.6% | 1,535 | 14 | 0 | yes | 2 | T9 |
| `pkg/sysstat` | 88.7% | 1,440 | 17 | 1 |  | 2 | T9 |
| `pkg/ghwebhook` | 62.8% | 1,372 | 13 | 1 |  | 2 | T8 |
| `pkg/capability/gcpkey` | 81.3% | 1,354 | 12 | 0 | yes | 2 | T7 |
| `pkg/grit/cache` | 76.3% | 1,328 | 6 | 0 | yes | 2 | T12 |
| `pkg/checkpost` | 84.2% | 1,276 | 12 | 0 | yes | 2 | T8 |
| `pkg/console` | 84.5% | 1,004 | 15 | 1 |  | 2 | T9 |
| `pkg/secretsrepo` | 72.2% | 918 | 5 | 0 | yes | 2 | T13 |
| `pkg/capability/geminikey` | 74.4% | 887 | 4 | 0 | yes | 2 | T7 |
| `pkg/agent/antigravity/hook` | 85.4% | 806 | 6 | 1 |  | 2 | T11 |
| `pkg/capability/grainlog` | 89.0% | 777 | 10 | 0 | yes | 2 | T7 |
| `pkg/usage` | 87.2% | 762 | 4 | 1 |  | 2 | T12 |
| `pkg/iapreconciler` | 81.6% | 669 | 6 | 0 | yes | 2 | T13 |
| `pkg/gcpsetup` | 61.4% | 638 | 4 | 0 | yes | 2 | T13 |
| `pkg/agent/stoprule` | 0.0% | 617 | 3 | 1 |  | 2 | T11 |
| `pkg/tunnel/wsupgrade` | 64.8% | 514 | 3 | 0 | yes | 2 | T9 |
| `pkg/capability/selfrepair` | 84.6% | 409 | 6 | 0 | yes | 2 | T7 |
| `pkg/agent/hookpolicy` | 94.6% | 242 | 2 | 1 |  | 2 | T11 |
| `cmd/checkpost` | 50.0% | 239 | 4 | 0 | yes | 2 | T8 |
| `pkg/grit/actions` | 85.7% | 236 | 5 | 0 | yes | 2 | T12 |
| `pkg/jwks` | 70.8% | 211 | 3 | 0 | yes | 2 | T13 |
| `pkg/capability/deploymenthealth` | 87.7% | 198 | 4 | 0 | yes | 2 | T7 |
| `pkg/capability/admincaps` | 0.0% | 171 | 13 | 0 | yes | 2 | T7 |
| `pkg/capability/capabilitytest` | 0.0% | 167 | 5 | 0 | yes | 2 | T7 |
| `pkg/capability/imagesigning` | 94.3% | 165 | 3 | 0 | yes | 2 | T7 |
| `pkg/tunnel/hostport` | 96.4% | 157 | 2 | 0 | yes | 2 | T9 |
| `pkg/apierror` | 79.4% | 156 | 3 | 1 |  | 2 | T4 |
| `pkg/secrets/kmsfake` | 0.0% | 150 | 1 | 0 | yes | 2 | T13 |
| `pkg/access` | 95.7% | 125 | 2 | 0 | yes | 2 | T4 |
| `pkg/capability/registrypush` | 96.7% | 125 | 3 | 0 | yes | 2 | T7 |
| `pkg/gitproxy/gitproxytest` | 0.0% | 116 | 4 | 0 | yes | 2 | T8 |
| `pkg/safepath` | 90.3% | 101 | 2 | 0 | yes | 2 | T13 |
| `pkg/ports` | 100.0% | 93 | 5 | 1 |  | 2 | T9 |
| `pkg/installtrack/installtracktest` | 0.0% | 73 | 3 | 0 | yes | 2 | T12 |
| `pkg/hubenv` | 0.0% | 67 | 1 | 0 | yes | 2 | T14 |
| `cmd/grain-registry` | 0.0% | 44 | 4 | 1 |  | 2 | T14 |
| `cmd/grain` | 0.0% | 24 | 3 | 0 | yes | 2 | T11 |
| `pkg/hub/providers/github` | 69.8% | 0 | 0 | 0 | yes | 2 | T14 |
| `pkg/statemachine` | 79.8% | 1,516 | 10 | 0 |  | 1 | T1 |
| `pkg/actionscache` | 88.7% | 1,279 | 6 | 0 |  | 1 | T9 |
| `pkg/gritapi` | 5.7% | 1,173 | 8 | 0 |  | 1 | T12 |
| `pkg/portpick` | 72.7% | 699 | 4 | 0 |  | 1 | T9 |
| `pkg/systemlog` | 88.1% | 661 | 4 | 0 |  | 1 | T9 |
| `cmd/image-vulns` | 88.7% | 621 | 1 | 0 |  | 1 | T12 |
| `pkg/schedulefilter` | 88.1% | 576 | 2 | 0 |  | 1 | T1 |
| `pkg/attest` | 81.7% | 536 | 5 | 0 |  | 1 | T12 |
| `pkg/backup` | 73.0% | 528 | 7 | 0 |  | 1 | T6 |
| `pkg/model/v2baseline` | 0.0% | 525 | 5 | 0 |  | 1 | T6 |
| `pkg/sandboxaddr` | 92.5% | 519 | 2 | 0 |  | 1 | T9 |
| `pkg/cifault` | 80.6% | 472 | 6 | 0 |  | 1 | T12 |
| `pkg/agent/credcheck` | 89.9% | 469 | 3 | 0 |  | 1 | T11 |
| `pkg/hostscriptcli` | 78.7% | 458 | 6 | 0 |  | 1 | T9 |
| `pkg/designdoc` | 95.7% | 439 | 4 | 0 |  | 1 | T12 |
| `pkg/nsclient` | 21.6% | 417 | 2 | 0 |  | 1 | T8 |
| `pkg/releasenotes` | 90.7% | 348 | 3 | 0 |  | 1 | T12 |
| `pkg/hosttop` | 87.5% | 332 | 8 | 0 |  | 1 | T9 |
| `pkg/agent/claude/stophook` | 84.5% | 322 | 2 | 0 |  | 1 | T11 |
| `pkg/kubectlgate` | 89.8% | 315 | 2 | 0 |  | 1 | T7 |
| `pkg/dispatchcondition` | 93.9% | 310 | 3 | 0 |  | 1 | T1 |
| `pkg/treeid` | 81.9% | 257 | 3 | 0 |  | 1 | T9 |
| `pkg/authfix` | 92.9% | 254 | 2 | 0 |  | 1 | T13 |
| `pkg/model/postgres/postgrestest` | 0.0% | 243 | 3 | 0 |  | 1 | T6 |
| `pkg/version` | 100.0% | 240 | 3 | 0 |  | 1 | T12 |
| `pkg/procgroup` | 73.7% | 220 | 10 | 0 |  | 1 | T9 |
| `pkg/model/guardtest` | 0.0% | 218 | 3 | 0 |  | 1 | T6 |
| `pkg/gitrefs` | 73.2% | 196 | 2 | 0 |  | 1 | T8 |
| `pkg/model/sqlite` | 68.0% | 180 | 2 | 0 |  | 1 | T6 |
| `cmd/github-webhook` | 21.7% | 139 | 4 | 0 |  | 1 | T8 |
| `pkg/whererun` | 90.9% | 131 | 3 | 0 |  | 1 | T12 |
| `pkg/model/sqlite/sqlitetest` | 54.9% | 128 | 3 | 0 |  | 1 | T6 |
| `cmd/grain-connect` | 85.4% | 122 | 2 | 0 |  | 1 | T9 |
| `cmd/reviewed-merge` | 91.2% | 94 | 1 | 0 |  | 1 | T8 |
| `pkg/authfix/authfixtest` | 0.0% | 78 | 3 | 0 |  | 1 | T13 |
| `cmd/grain-agentd` | 0.0% | 34 | 1 | 0 |  | 1 | T9 |
| `cmd/grain-hub` | 0.0% | 26 | 1 | 0 |  | 1 | T14 |

## Appendix B: uncovered functions in churned or fix-heavy files

These are files with ≥ 4 fix commits or ≥ 15 commits in the window,
listing the functions their own package's tests never enter. The list
is cut at 14 per file.

- `pkg/ui/client.go` (81 fixes / 141 commits; 1 of 167 funcs at 0%): Error
- `pkg/ui/agent_api_gate.go` (52 fixes / 103 commits; 2 of 107 funcs at 0%): Flush, Unwrap
- `pkg/orchestrator/finish.go` (30 fixes / 57 commits; 2 of 102 funcs at 0%): branchUnmovedSince, resolveContextCommitSHA
- `pkg/orchestrator/run.go` (26 fixes / 63 commits; 2 of 103 funcs at 0%): hasGrant, ApplyCapabilityPlacements
- `pkg/model/store.go` (25 fixes / 80 commits; 143 of 264 funcs at 0%): ID, CommitCount, InstanceID, DeploymentID, Changed, BranchForNewTask, CommentAdded, Comment, FirstHumanComment, CountCommentsBefore, CommentsCreatedAfter, LatestNonAgentCommentAtBefore, AddAttachment, addAttachment ...
- `pkg/ui/access_roles.go` (22 fixes / 36 commits; 5 of 42 funcs at 0%): persistentSandboxRepo, grantDeniedByRole, roleConsoleSessionWithin, isUnconditionalPersonOnlyRoute, delegatedRefusalError
- `pkg/daemon/mcpserver.go` (22 fixes / 63 commits; 30 of 94 funcs at 0%): OpenPullRequest, OpenChange, ReplyToChangeThread, ResolveChangeThread, Metrics, ExecDB, DeploymentHealth, errorString, RecreateSandbox, ProposeConfigChange, CheckAccessRequest, ReportStatus, RenameSession, RequestHumanReview ...
- `pkg/orchestrator/cycle.go` (21 fixes / 55 commits; 2 of 60 funcs at 0%): SyncSandboxLedger, taskBaseTarget
- `pkg/ui/agent_api_tools.go` (20 fixes / 84 commits; 2 of 23 funcs at 0%): emptyRouteAnswer, getTaskAsExternal
- `pkg/demo/demo.go` (20 fixes / 72 commits; 14 of 49 funcs at 0%): Run, publish, Threads, ChecksStatus, WaitChecks, GrainCIRefusal, demoChecksReport, Reply, Resolve, PublishRun, PublishReplies, Mail, memoryScopeName, removeOnSignal
- `pkg/daemon/ui_server.go` (20 fixes / 56 commits; 4 of 15 funcs at 0%): CheckUIRequireVerifier, CheckUIRequireIAP, lastRestart, restartAdoption
- `pkg/ui/console_sessions.go` (17 fixes / 34 commits; 1 of 31 funcs at 0%): consoleSessionGrainCapabilities
- `pkg/github/github.go` (17 fixes / 30 commits; 17 of 131 funcs at 0%): ConflictFiles, IsNoCommitsBetween, AnyJobLogRead, ResolveCommitSHA, PullRequestDiff, ReadFile, DeleteBranch, ListPullRequestCommits, RerequestCheckSuite, RerequestCheckRun, FailedJobsNote, JobLogTail, RerunFailedJobs, CompletedBranchWorkflowRuns ...
- `pkg/sandboxcli/sandboxcli.go` (17 fixes / 29 commits; 5 of 46 funcs at 0%): osSandboxEnv, Error, Main, IsCommand, Unknown
- `pkg/ui/capability_servers.go` (16 fixes / 30 commits; 1 of 64 funcs at 0%): DismissCapabilityServerNotice
- `pkg/daemon/ci.go` (16 fixes / 34 commits; 34 of 107 funcs at 0%): WorkflowOptions, List, Find, Cancel, MarkFlake, EvictBaseTransitionFailures, CacheStats, ClearCache, ClearArtifacts, ArtifactStats, RunArtifacts, OpenArtifact, WithArtifacts, CiActionsCacheScope ...
- `pkg/namespace/namespace.go` (16 fixes / 29 commits; 7 of 66 funcs at 0%): PermitsSrc, resolveExternalCI, externalCINode, externalStep, sanitizeStepName, attemptCombinedLog, WalkFiles
- `pkg/model/task.go` (15 fixes / 53 commits; 13 of 67 funcs at 0%): Valid, Verified, String, IsDeclaration, OtherBranchQueueNote, LandingBranch, GrainSourceRepo, CapabilityRepos, Settled, NeverCompletes, NeverEndingDependencyNote, RepairInFlight, RepairsSpent
- `pkg/model/capability_catalogue.go` (14 fixes / 26 commits; 6 of 57 funcs at 0%): IsZero, TaskGrantContext, CatalogueRequiresPersonNote, JoinCapabilities, SplitCapabilities, EffectivePreset
- `pkg/orchestrator/capability_pass.go` (14 fixes / 32 commits; 4 of 46 funcs at 0%): NoteCapabilityServerDropped, NoteCapabilityServerDroppedOnHostd, RetryCapabilityReleases, Error
- `pkg/orchestrator/sync.go` (14 fixes / 24 commits; 3 of 109 funcs at 0%): notFound, runsPronoun, allFinishedPhrase
- `pkg/github/githubsim/githubsim.go` (14 fixes / 24 commits; 21 of 79 funcs at 0%): UniquePullRequestBase, CreateEmptyRepository, forkByOwner, readFileAt, reachableRepos, LandMergeQueue, DropFromMergeQueue, mergeQueuePosition, graphql, reviewThreadExists, hasUnresolvedReviewThreads, reviewThreadsJSON, workflowJobsFor, runIDFor ...
- `pkg/gitproxy/cli_actions.go` (14 fixes / 26 commits; 1 of 36 funcs at 0%): serveAPIRoutes
- `pkg/ui/httpclient.go` (13 fixes / 45 commits; 66 of 148 funcs at 0%): GoogleIDToken, adcIDToken, runGcloud, RoundTrip, ResponseError, SandboxHealth, SetDependency, Defer, Pause, Resume, Undefer, Comment, RetryMerge, ForceMerge ...
- `pkg/cli/daemon.go` (13 fixes / 52 commits; 1 of 2 funcs at 0%): runDaemonCommand
- `pkg/ui/external_agents.go` (12 fixes / 24 commits; 1 of 30 funcs at 0%): ExternalAgentAPITools
- `pkg/model/agent_api.go` (11 fixes / 49 commits; 27 of 65 funcs at 0%): LegacyModelProxyCapability, ModelProxyGrantVendor, IsGrainJournalGrant, HasGrainJournal, IsGrainSourceGrant, HasGrainSource, IsGrainHealthGrant, HasGrainHealth, HasGrainHostExec, IsGitHubReadIssuesGrant, HasGitHubReadIssues, IsGitHubSandboxRepoGrant, HasGitHubSandboxRepo, ParseGrainSQLGrant ...
- `pkg/model/migrations.go` (11 fixes / 88 commits; 2 of 8 funcs at 0%): raiseMaxMergesPerRepoDefault, ApplyDataMigrations
- `pkg/gitproxy/cli.go` (11 fixes / 22 commits; 3 of 26 funcs at 0%): serveIssues, serveDBQuery, writeToolResult
- `pkg/mcp/mock_tools.go` (10 fixes / 23 commits; 2 of 34 funcs at 0%): SetAttachmentFiles, RecordProposedTask
- `pkg/repodecl/parse.go` (10 fixes / 29 commits; 1 of 26 funcs at 0%): TemplatePush
- `pkg/ui/messages.go` (9 fixes / 19 commits; 1 of 12 funcs at 0%): handleRecordDelivered
- `pkg/model/capability_ref.go` (9 fixes / 24 commits; 3 of 43 funcs at 0%): CheckCapabilityGrantSyntax, CapabilityGrants, HasCapabilityRef
- `pkg/orchestrator/consolesessions.go` (9 fixes / 24 commits; 2 of 28 funcs at 0%): carryConsoleSessionHandover, consoleSessionGrants
- `pkg/sandboxcli/task.go` (9 fixes / 12 commits; 1 of 17 funcs at 0%): parseNotBeforeFlag
- `pkg/ui/repos.go` (8 fixes / 26 commits; 2 of 64 funcs at 0%): handleGetRepoDeclarations, handleGetRepoWatchDefaultBranch
- `pkg/ui/capability_policy.go` (8 fixes / 15 commits; 1 of 20 funcs at 0%): CapabilityPolicy
- `pkg/ui/list_page.go` (8 fixes / 9 commits; 1 of 17 funcs at 0%): ParseListCursor
- `pkg/model/postgres/adapter.go` (8 fixes / 37 commits; 11 of 47 funcs at 0%): Open, Prepare, PrepareContext, Begin, adaptDriverValues, Close, NumInput, Exec, Query, ExecContext, QueryContext
- `pkg/hostd/remote.go` (8 fixes / 17 commits; 4 of 29 funcs at 0%): SharedTokenOwner, RenameSession, ReplayCache, WaitConnected
- `pkg/daemon/hostexec.go` (8 fixes / 21 commits; 2 of 32 funcs at 0%): HostdClient, hostExecutorStatus
- `pkg/ui/ci_actions.go` (7 fixes / 13 commits; 4 of 42 funcs at 0%): ServesRepo, ExternalJobAttempts, ExternalCheckRuns, ExternalJobLog
- `pkg/ui/server.go` (7 fixes / 76 commits; 1 of 14 funcs at 0%): ServeHTTP
- `pkg/daemon/capability_servers_hostd.go` (7 fixes / 11 commits; 4 of 29 funcs at 0%): hostAgentsOnly, rotateManaged, HostAgentsOnly, RotateManaged
- `pkg/daemon/config.go` (7 fixes / 47 commits; 3 of 23 funcs at 0%): ciRetention, ciStorageBounds, ciRetention
- `pkg/daemon/sandbox_image.go` (7 fixes / 16 commits; 2 of 35 funcs at 0%): checkClusterSandboxImageConnectivity, logCustomSandboxImage
- `pkg/ui/sandbox_cli_actions.go` (6 fixes / 17 commits; 20 of 23 funcs at 0%): cliActionError, StartTaskCI, CIRun, Ready, OpenPullRequest, renderPullRequestStatus, reviewActions, ReplyToPullRequestComment, ResolveReviewThread, ListReviewThreads, RevertInfo, sameTarget, NotifyOperator, MessageTask ...
- `pkg/ui/notifications.go` (6 fixes / 13 commits; 6 of 26 funcs at 0%): notificationID, handleRaiseNotification, handleAckNotification, handleClearNotification, handleInvestigateNotification, handleAgentNotify
- `pkg/ui/access_principals.go` (6 fixes / 14 commits; 5 of 33 funcs at 0%): ListAllowedPrincipals, AllowPrincipal, SetPrincipalRole, DenyPrincipal, DenyPrincipalWith
- `pkg/ui/pull_requests_api.go` (6 fixes / 8 commits; 4 of 18 funcs at 0%): AdoptPullRequest, ReviewPullRequest, PullRequestStatus, SubmitPullRequest
- `pkg/model/notification.go` (6 fixes / 14 commits; 14 of 26 funcs at 0%): Rank, Validate, Open, NotificationSubject, BranchKey, getNotification, GetNotification, OpenNotification, ClearNotification, ClearNotificationByID, AckNotification, SetNotificationInvestigation, SetNotificationDelivered, ListNotifications
- `pkg/model/compose.go` (6 fixes / 22 commits; 4 of 25 funcs at 0%): ModelSourceOf, ScheduleComposition, SuiteComposition, TemplateComposition
- `pkg/staterepo/decl.go` (6 fixes / 20 commits; 3 of 58 funcs at 0%): isBase64Mapping, sortedKeys, decodeOverlapMapping
- `pkg/model/auto_submit.go` (6 fixes / 13 commits; 2 of 15 funcs at 0%): UngrantedWritesNote, UngrantedReadsNote
- `pkg/orchestrator/orchestrator.go` (6 fixes / 20 commits; 2 of 30 funcs at 0%): AtLeast, WithEnv
- `pkg/daemon/sandbox_namespace.go` (6 fixes / 17 commits; 10 of 36 funcs at 0%): ServeHTTP, ReadMergeQueueQuery, ReadMetrics, ListMetricsSections, ReadHealth, ListGrainLogSources, ListMemoryRepos, ListConfigRepos, SetIssues, IssueTools
- `pkg/daemon/sandbox_backends.go` (6 fixes / 14 commits; 3 of 36 funcs at 0%): K8sSandboxServiceHost, hasStartupSandboxPlumbing, AvailableSandboxBackends
- `pkg/secrets/secrets.go` (6 fixes / 14 commits; 3 of 42 funcs at 0%): GrainHeld, AgentBackendCredentialSecret, KeySource
- `pkg/upgrade/upgrade.go` (6 fixes / 17 commits; 3 of 48 funcs at 0%): CheckAllowedBranches, ValidBranchName, headRevision
- `pkg/daemon/ci_capability_pass.go` (6 fixes / 16 commits; 2 of 15 funcs at 0%): WithCICapabilityPorts, WithCICapabilityServers
- `pkg/gitproxy/namespace.go` (6 fixes / 13 commits; 2 of 17 funcs at 0%): authenticateSandbox, handleNamespaceRequest
- `pkg/demo/demo_githubsim.go` (6 fixes / 12 commits; 1 of 21 funcs at 0%): BranchExists
- `pkg/hostd/client.go` (6 fixes / 10 commits; 1 of 18 funcs at 0%): String
- `pkg/ui/external_cli.go` (5 fixes / 10 commits; 6 of 23 funcs at 0%): pullRequestWebURL, Reply, Inbox, InboxPage, WaitTasks, GetTaskCommentsSince
- `pkg/ui/hostds.go` (5 fixes / 16 commits; 5 of 26 funcs at 0%): ListHostds, CreateHostd, HostdInstall, UpdateHostd, RotateHostdToken
- `pkg/ui/console.go` (5 fixes / 9 commits; 4 of 8 funcs at 0%): handleListConsoles, handleConsoleInput, handleConsoleResize, handleCloseConsole
- `pkg/model/capability.go` (5 fixes / 16 commits; 10 of 35 funcs at 0%): Unwrap, WithRunConnection, RunConnection, WithMintRunID, MintRunID, WithMintCredentials, MintCredentials, SupportsCI, RegisterCIDynamic, MissingGCPKeyRefusal
- `pkg/model/hold_for_approval.go` (5 fixes / 10 commits; 3 of 6 funcs at 0%): HeldForApproval, HoldRequested, HeldForApprovalNote
- `pkg/model/task_auto_merge.go` (5 fixes / 7 commits; 2 of 14 funcs at 0%): AllowAutoMerge, WithAutoMergeChange
- `pkg/daemon/sandbox_cli.go` (5 fixes / 14 commits; 31 of 35 funcs at 0%): actions, StartTaskCI, CIRun, Ready, OpenPullRequest, ReplyToPullRequestComment, ResolveReviewThread, ListReviewThreads, RevertInfo, NotifyOperator, MessageTask, CheckWaitTargets, ProposeTask, ListAPIRoutes ...
- `pkg/grit/engine/engine.go` (5 fixes / 15 commits; 13 of 42 funcs at 0%): WithDetach, claimFlight, releaseFlight, cache, Resume, matchJobRun, skippedJob, neverRan, lookupCache, conclusionFor, SortJobRuns, JobCapabilities, joinJobWarning
- `pkg/daemon/upgrade.go` (5 fixes / 17 commits; 5 of 15 funcs at 0%): ValidateManagementURL, CheckManagementURL, SplitBranchPatterns, kubeUpgradeSchemaConfig, readTrimmed
- `pkg/cli/init_workstation.go` (5 fixes / 11 commits; 4 of 21 funcs at 0%): hostWorkstationEnv, initWorkstationCmd, writeWorkstationGitHubToken, setWorkstationNetwork
- `pkg/cli/login.go` (5 fixes / 7 commits; 3 of 15 funcs at 0%): loginRefused, warnAccessCode, readSecretLine
- `pkg/ui/change_review.go` (4 fixes / 8 commits; 4 of 13 funcs at 0%): handleChangeReview, handlePublishReplies, handleMailChange, ChangeReview
- `pkg/ui/task_config_change.go` (4 fixes / 8 commits; 2 of 14 funcs at 0%): handleProposeConfigChange, handleRejectConfigChange
- `pkg/ui/api_scope_sets.go` (4 fixes / 9 commits; 1 of 9 funcs at 0%): handleListAPIScopeSets
- `pkg/ui/hostd_install.go` (4 fixes / 10 commits; 1 of 28 funcs at 0%): HasFeature
- `pkg/ui/k8s_images.go` (4 fixes / 11 commits; 1 of 27 funcs at 0%): isHexString
- `pkg/model/repo_declaration_store.go` (4 fixes / 9 commits; 9 of 21 funcs at 0%): GetRepoDeclaration, ListRepoDeclarations, RepoDeclarationsOf, GetRepoDeclarationSync, ListRepoDeclarationSyncs, RecordDeclarationCheck, ForgetRepo, ForgottenRepoTables, DeclaringRepos
- `pkg/staterepo/staterepo.go` (4 fixes / 9 commits; 5 of 47 funcs at 0%): Branch, SetSSHKeyFile, ConfigureMirror, LoadedHead, shellQuote
- `pkg/orchestrator/capability_materialize.go` (4 fixes / 13 commits; 4 of 20 funcs at 0%): Error, placeAllWithPlacer, reportCapabilitySetupFailure, capabilityService
- `pkg/model/pull_request_adopt.go` (4 fixes / 6 commits; 1 of 4 funcs at 0%): ParseTakeoverMode
- `pkg/model/pullrequest.go` (4 fixes / 7 commits; 1 of 8 funcs at 0%): WithoutOffTargetPullRequests
- `pkg/staterepo/check.go` (4 fixes / 10 commits; 1 of 16 funcs at 0%): Tables
- `pkg/daemon/statepullrequests.go` (4 fixes / 14 commits; 56 of 92 funcs at 0%): taskForBranch, pushesReviewedBranch, localCommits, localComparedFiles, UpdateBranch, FindOpenPullRequestForHead, UpdatePullRequestBody, UpdatePullRequestTitle, MergeBranch, ClosePullRequest, EmptyChecksAreSettled, ListWorkflowRuns, RerequestCheckSuite, RerequestCheckRun ...
- `pkg/daemon/servers.go` (4 fixes / 16 commits; 7 of 12 funcs at 0%): store, Put, Update, Get, List, Find, Live
- `pkg/hostd/carry.go` (4 fixes / 7 commits; 6 of 25 funcs at 0%): Error, Is, Carry, Addr, Network, String
- `pkg/agent/codex/codex.go` (4 fixes / 14 commits; 5 of 37 funcs at 0%): Run, WithHostAuthFile, WithHost, Host, HostdEnv
- `pkg/ci/external.go` (4 fixes / 5 commits; 4 of 4 funcs at 0%): ExternalCIFor, FindExternalRunsCounted, ActionsRunID, ParseActionsRunID
- `pkg/capability/servers/servers.go` (4 fixes / 12 commits; 3 of 31 funcs at 0%): CallTool, Write, Rotate
- `pkg/daemon/transcripts.go` (4 fixes / 9 commits; 2 of 25 funcs at 0%): LiveTranscriptDir, CheckObjectStoreURI
- `pkg/redact/redact.go` (4 fixes / 9 commits; 2 of 7 funcs at 0%): KnownCount, RedactHostEnrollmentCodes
- `pkg/agent/toolroster.go` (4 fixes / 21 commits; 1 of 8 funcs at 0%): IsControlTool
- `pkg/capability/intree/gcp.go` (4 fixes / 14 commits; 1 of 22 funcs at 0%): NewConfigurableGCP
- `pkg/capability/intree/gcp_token.go` (4 fixes / 6 commits; 1 of 19 funcs at 0%): held
- `pkg/cli/demo.go` (4 fixes / 25 commits; 1 of 2 funcs at 0%): cmdDemo
- `pkg/cli/hostds.go` (4 fixes / 11 commits; 1 of 9 funcs at 0%): cmdHostUpdate
- `pkg/cli/mcp_external.go` (4 fixes / 6 commits; 1 of 18 funcs at 0%): cmdMCP
- `pkg/mcp/task_tools.go` (4 fixes / 14 commits; 1 of 32 funcs at 0%): renderTaskList
- `pkg/ui/teams.go` (3 fixes / 18 commits; 2 of 22 funcs at 0%): Error, ListTeamAuditLogsWithUser
- `pkg/model/task_event.go` (3 fixes / 17 commits; 4 of 11 funcs at 0%): RecordTaskEvent, TaskEventsLatest, TaskEventsSince, RunSpansSince
- `pkg/model/console_session.go` (3 fixes / 27 commits; 3 of 37 funcs at 0%): DirectorNetworkAccess, DirectorPolicyCapabilities, CarryConsoleSessionHandover
- `pkg/daemon/statemanager.go` (3 fixes / 15 commits; 20 of 48 funcs at 0%): Nudge, resetNoticePending, sync, Compare, ResolveRev, localMergeBase, localForkPoint, checkImage, SettingsChanges, Tree, File, Status, DismissResetNotices, resetNotices ...
- `pkg/capability/grainserver/grainserver.go` (3 fixes / 19 commits; 12 of 51 funcs at 0%): ParseRepoCredentialGrant, RepoServerForCredentials, RepoStatusForCredentials, defaultConfigMapServer, IsJournalGrant, IsHealthGrant, HasHealth, IsSQLGrant, IsSQLWriteGrant, HasSQL, HasSQLWrite, IsHostExecGrant
- `pkg/daemon/agents.go` (3 fixes / 16 commits; 10 of 63 funcs at 0%): AgentFrameworks, addHostdBlocked, CredentialConnection, Run, ParseResult, ParseAttempts, Unwrap, CanOpenPullRequest, TerminalCommand, errDetachedRemote
- `pkg/agent/antigravity/antigravity.go` (3 fixes / 16 commits; 9 of 84 funcs at 0%): Run, WithMaxTurns, TurnLimits, Model, Host, ServeHook, writeAgyHome, refreshAgyHome, IsWithheldNativeTool
- `pkg/agent/agent.go` (3 fixes / 17 commits; 1 of 11 funcs at 0%): MCPServerArgs
- `pkg/model/config.go` (2 fixes / 29 commits; 18 of 41 funcs at 0%): ParseAutoUpgradeTime, UpgradeCheckModes, ValidUpgradeCheckMode, NewDeploymentConfig, SessionSandboxIdleTTLOr, BaseCIRedStreakOr, MergeBatchSizeOr, HostdTargets, AgentCLIBinary, DeploymentAgentBackends, AdvertisesDiscovered, NoLongerDiscovers, ConnectionsOnly, WithConnectionsOf ...
- `pkg/daemon/live_config.go` (2 fixes / 16 commits; 3 of 22 funcs at 0%): actionsCacheUsable, GitHubCredential, PackageCredential
- `pkg/mcp/toolnames.go` (2 fixes / 16 commits; 2 of 5 funcs at 0%): AgyQualifiedToolName, CapabilityForTool
- `pkg/cli/sync.go` (2 fixes / 19 commits; 1 of 8 funcs at 0%): syncCmd
- `pkg/ui/principal.go` (1 fixes / 15 commits; 15 of 38 funcs at 0%): WithHubConfig, HubConfigFromContext, GetIAPAudience, SetIAPIssuer, GetIAPIssuer, SetHubAudience, GetHubAudience, SetHubIssuer, GetHubIssuer, SetHubKeysURL, GetHubKeysURL, SetHubHTTPClient, SetHubRefetchFloor, ResetHubConfig ...
- `pkg/daemon/capabilities.go` (1 fixes / 35 commits; 3 of 25 funcs at 0%): GitBranches, GitRefSHA, TokenRefused
