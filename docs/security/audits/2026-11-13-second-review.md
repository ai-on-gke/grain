# Security review: merges to `main`, 2026-11-13 02:22 → 13:57 UTC

- **Date:** 2026-11-13 (UTC), second review of the day
- **Auditor:** grain task 5373 (agent), the nightly review of the day's
  merges, in the shape of task 907's 2.4 audit
  (`2026-10-25-2.4-audit.md`)
- **Base audited:** `origin/main` at `e69d34223` (merge of #1478,
  2026-11-13 13:57 UTC)
- **Range:** `4ae6438fd..e69d34223` on `main`'s first-parent line: the
  **19** merges since the most recent report's base `4ae6438fd`
  (`2026-11-13.md`, merge of #1435, 2026-11-13 02:22 UTC). 195 files,
  +13340 −410, of which about 11600 added lines are `docs/test-runs/`
  and `docs/test-plans/` records. Merge times below are UTC committer
  dates (`TZ=UTC git log --date=format-local`; plain `--date=format:`
  prints the committer's own zone, −08:00 here, which is eight hours
  off).
- **Method:** every merge's file list was mapped to the brief's surfaces
  and to the other paths on a trust boundary that the brief does not list
  (`pkg/gitproxy`, `pkg/hostd`, `pkg/agentd`, `pkg/ui/access_*`,
  `pkg/secrets`, `pkg/upgrade`, `pkg/github`, `pkg/daemon`,
  `deploy/kubernetes`, `.github/workflows`, `scripts/`). Every diff
  outside `docs/test-runs/`, `docs/test-plans/`, `ui/` and `ui-ng/` was
  read in full, with the code it calls into on the far side of the
  boundary; the UI merges were read by file list and grepped for new API
  calls. The security and baseline suites and every touched package's
  tests were run (below). No finding was confirmed, so no throwaway test
  was written and no fix task was filed.
- **Result:** **no findings; no fix task filed.** It was a quiet range:
  eleven of the nineteen merges are UI-only or documentation, and the
  code that did land on a boundary is one tightening (#1457 ships the
  `network: none` fence in `grain.yaml`), two attribution and messaging
  changes (#1467, #1385), a reporting-only task field (#1438), an
  informational admission note (#1463), a client-side drain flag for an
  RPC the host agent already served (#1460), and a granule re-vendor that
  makes a delete stricter (#1444). Two observations that are not findings
  are recorded for the next review.

## Commits reviewed

Every merge to `main` in the range, newest first. *Files* is the merge's
own change against its first parent; *surface* is how many of those
files fall under a surface the brief names. Titles are the pull
request's first commit.

| Merge | Merged (UTC) | PR | Title | Files | Surface |
|---|---|---|---|---|---|
| `e69d34223` | 11-13 13:57 | #1478 | docs(test-plans): add the 2026-11-13 afternoon new features test plan | 1 | 0 |
| `40b9807f7` | 11-13 13:02 | #1467 | fix(ui): record local_script.answered for an approval a sweep expired first | 64 | 0 |
| `23005075b` | 11-13 06:16 | #1460 | fix(agentd): let grain-agentd drain by hand with -drain and SIGUSR1 | 12 | 0 |
| `22343d516` | 11-13 05:58 | #1457 | fix(k8s): ship grain-sandbox-network-none in grain.yaml | 16 | 0 |
| `5aa9c8bd3` | 11-13 05:22 | #1463 | fix(orchestrator): tell the proposer what admission changed about its proposals | 21 | 1 |
| `7b196c61e` | 11-13 04:42 | #1459 | fix(ui-ng): keep /next/ dialogs inside a phone's screen | 4 | 0 |
| `6599ed791` | 11-13 04:30 | #1458 | fix(ui-ng): confirm destructive Hosts actions in /next/ Settings | 4 | 0 |
| `5f92071b5` | 11-13 04:17 | #1454 | fix(ui-ng): say Not available on Settings → Upgrade when grain cannot upgrade itself | 4 | 0 |
| `471543551` | 11-13 04:11 | #1453 | fix(ui): show suite runs that filed no tasks without crashing or promising a pass | 8 | 0 |
| `64cc5b4bb` | 11-13 04:07 | #1449 | fix(ui): don't offer Keep on a state counts panel for a sort-only narrowing | 4 | 0 |
| `86f11e7a5` | 11-13 04:03 | #1450 | fix(ui-ng): offer only the CI modes the server accepts on /next/ repo page | 6 | 0 |
| `51844bb2c` | 11-13 03:27 | #1451 | docs(security): nightly review of merges to main, 2026-11-11 16:27 to 2026-11-13 02:22 UTC | 1 | 0 |
| `17f00df68` | 11-13 03:20 | #1232 | chore(templates): point dead-code scan at ui-hub and security review at ai-on-gke/grain | 5 | 0 |
| `fb564ae5e` | 11-13 03:18 | #1445 | fix(ui): say why Create task is disabled, and pluralize batch Close | 6 | 0 |
| `4178ee49a` | 11-13 03:15 | #1444 | fix(granule): re-vendor gke-labs/granule at ccdbc4b and drop the vm delete retry loop | 10 | 8 |
| `3d60d431b` | 11-13 03:12 | #1442 | fix(cli): exit 1 from grain health when a check is FAILING | 6 | 0 |
| `f62de7bdd` | 11-13 03:10 | #1438 | feat(tasks): record which template a task was filed from | 17 | 2 |
| `e475bf878` | 11-13 03:07 | #1385 | fix(hostd): say a hostd stop, not an upgrade drain, ended a Draining exit | 6 | 4 |
| `0619ed329` | 11-13 03:05 | #1448 | docs(test-plans): add the 2026-11-13 nightly new features test plan | 1 | 0 |

## Trust-boundary surfaces touched

Against every surface in the brief, with the merges that touched it:

| Surface | Paths | Touched in range |
|---|---|---|
| Agent API scopes and route authorization | `pkg/ui/agent_api*.go`, `pkg/ui/route_scopes.go`, `.grain/baselines/route-authorization.txt` | **no.** No route was added, removed or re-scoped; the baseline is unchanged. The one `pkg/ui` handler change (#1467, `handleExpireLocalScriptApproval`) keeps its ownership check and changes only when the audit event is recorded. |
| Capability grants and placements | `pkg/capability/...`, `pkg/orchestrator/run.go` (`applyPlacements`), `pkg/mcp/ssh_placements.go` | **no.** Unchanged. `pkg/mcp/mock_tools.go` (#1463) adds wording to `propose_task`'s reply; `pkg/orchestrator/finish.go` (#1463) adds a comment naming what admission refused, after the unchanged refusal. |
| Sandbox backends | `pkg/k8s`, `pkg/orchestrator/k8s_granule_sandboxes.go`, `pkg/granule`, `third_party/granule` | **yes** -- `pkg/granule/granule.go` and `third_party/granule/pkg/dockervm/docker.go` (#1444 re-vendor at `ccdbc4b`); `deploy/kubernetes/grain.yaml` and `networkpolicy.yaml` (#1457, the `network: none` fence). `pkg/k8s` and `k8s_granule_sandboxes.go` are unchanged (#1457 adds tests beside them: `k8s_grant_refusal_test.go`, `k8s_networkpolicy_test.go`). |
| Release and secrets gating | `pkg/ci`, `pkg/ui/ci.go`, `pkg/ui/repo_release_workflows.go`, `pkg/orchestrator/basebranch*.go` | **no.** Unchanged, as are `.github/workflows/*`, `scripts/`, `pkg/upgrade`, `pkg/github`, `pkg/secrets` and `pkg/daemon`. |
| State-repo sync | `pkg/staterepo`, `pkg/orchestrator/sync.go` | **no** (code). #1438 adds the `task_source_template` table to `pkg/staterepo/tables_internal_test.go`'s `notSettings` list, so the sync keeps treating it as task state and not a setting; `sync.go` is unchanged. |
| Trust-boundary classification and the security hold | `pkg/trustboundary`, `pkg/orchestrator/securityhold.go` | **no.** Unchanged. |
| Path containment | `pkg/safepath` and callers | **no.** Unchanged; `.grain/baselines/path-containment.txt` unchanged. |

Other boundary paths changed in the range and read for this review:
`pkg/hostd` (#1385 `protocol.go`, `server_linux.go`, `spool_linux.go`),
`pkg/agentd/agentd.go` (#1460), `pkg/ui/local_script_approvals.go` and
`pkg/ui/server.go` (#1467), `pkg/orchestrator/finish.go` and `port.go`
(#1438, #1463), `pkg/model/{store,schema,task,compose}.go` and
`pkg/model/postgres/adapter.go` (#1438), `pkg/ui/client.go` and
`tasks.go` (#1438), `pkg/cli/health.go` (#1442),
`.grain/templates/*.yml` (#1232), `deploy/kubernetes/*` (#1457).

## Tests run

All in the sandbox at `e69d34223` with Go 1.26.10, no `-race`:

- `go test ./pkg/trustboundary/... ./tests/security/...` -- **pass**
  (`pkg/trustboundary` 4.6s, `tests/security` 99.9s, which includes
  `TestRouteAuthorizationMatrix` over the unchanged route table).
- Touched packages -- **pass**: `pkg/hostd` (51.7s), `pkg/agentd` and
  its sub-packages, `pkg/granule` (16.5s), `pkg/model` and
  `pkg/model/{postgres,sqlite,sqlitetest,v2baseline}`, `pkg/cli`
  (87.5s), `pkg/mcp` (39.5s), `pkg/staterepo` and `statecheck`,
  `tests/deploy` (590s; includes
  `TestTheKubernetesManifestShipsTheObjectsItsHeaderClaims` over #1457),
  `pkg/ui` (190.6s) and `pkg/orchestrator` (189.5s).
- `go test ./tests/hygiene/` with this report staged -- **pass**
  (`TestCoreSaysNoGoogleTokens` scans `docs/security/audits/`).
- `third_party/granule`: `go test ./pkg/dockervm/...` run inside its own
  module -- **one environmental failure**, `TestWaitReady_Sub50msDetection`
  ("took 71ms ... want sub-50ms"), a wall-clock assertion that failed
  while `pkg/ui` and `pkg/orchestrator` were running beside it (load
  average 19.8 on 7 cores) and passed 2 of 3 reruns. `exec_test.go` is
  not in the range (last touched by `b1d801b87`); it is a timing
  result, not a security one. The tests the re-vendor added
  (`TestDelete_RemovalAlreadyInProgressWaitsUntilGone`,
  `TestDelete_RemovalInProgressBoundsWait`) pass.
- Not verifiable from the sandbox: that `third_party/granule` is
  byte-identical to `gke-labs/granule` at `ccdbc4b`. `go.mod` replaces
  the module with the vendored tree and the sandbox cannot reach
  `github.com` (`go mod download` fails on `git ls-remote`), so the
  re-vendor's own claim (`docs/granule-local-patches.md`, "replaced
  wholesale from a `git archive` of `ccdbc4b` with no local patches") is
  recorded here as asserted, not checked. The next review with
  `reads: ["gke-labs/granule"]` can `diff -r` it.
- `postgresql` is not installed in the sandbox, so `pkg/model/postgres`
  (2.3s) ran its Postgres-backed tests as skips; #1438's `task_source_template`
  primary key is exercised on SQLite only here. CI's `Tests / go` job
  installs Postgres and ran it green before merge.

## Findings

None.

## Observations (not findings)

Recorded so the next review knows they were seen; none names a way for
anyone to do what they should not.

1. **`task_source_template` is a second template column on a task
   (#1438).** `model.Task.SourceTemplateID` is written for every task
   that names a template -- including an agent's proposal and a port,
   where `TemplateID` is deliberately left empty because `TemplateID`
   carries a template's declared push set and template-restricted
   catalogue grants. Every reader of the new field was enumerated
   (`grep -rn SourceTemplateID pkg`): `store.go` writes and hydrates
   it, `compose.go`, `finish.go`, `port.go` and `client.go` set it, and
   `pkg/ui/tasks.go` copies it to the API's `templateId` for display.
   Nothing in `templateDeclaredPush`, `catalogueFilingGrants` or
   `GitDeclaredPush` reads it; `hydrate` copies `TemplateID` into
   `SourceTemplateID` and never the reverse; and the filing gate in
   `client.go:3216` still sets `TemplateID` only when the filer is a
   person (`proposedBy == nil && actor.Kind != PrincipalAgent`). The
   risk is a future reader taking `SourceTemplateID` for `TemplateID`;
   the field's doc comment says plainly that it grants nothing. Worth a
   glance whenever a change adds a reader.
2. **Admission notes quote agent-chosen titles under grain's name
   (#1463).** `noteProposalAdmissionChanges` posts a comment authored by
   `PrincipalAutomation "grain"` that includes each proposal's title
   (`%q`-quoted) and id. A run can therefore put a sentence of its
   choosing into a grain-authored comment on its own task. This is the
   same shape as the existing `noteRefusedTuningProposals` and the
   proposal body notes, the title is already the proposal's own
   record, and the comment lands on the proposing task where the same
   run speaks anyway, so it reaches nobody the run could not already
   address. Not a finding.

## Examined and found sound

What was read and why it holds, so the next night can tell what was
looked at.

- **#1467 local-script approval audit events** (`pkg/ui/local_script_approvals.go`,
  `pkg/ui/server.go`). The change moves `local_script.answered` recording
  for sweep-expired approvals into the registry (`SetOnExpire`,
  `flushExpired`, `unreported`) so a task-page read that prunes an
  expired approval still records it. The state machine is untouched:
  `prune` settles the same approvals it did, `Answer` and `Expire` keep
  their pending checks, and `handleExpireLocalScriptApproval` keeps the
  `task %s's run may not expire approvals on task %s` ownership check
  ahead of any write. `flushExpired` is deferred before `mu` is taken,
  so it runs after the unlock (LIFO defers) and the `onExpire`
  callback never holds the registry lock while writing to the store.
  The `server.go` hunk sets the event actor on the approval-callback-token
  path to the task principal that path already authenticates as; it
  changes attribution, not who may call what. `recordLocalScriptEvent`
  on the server falls back to `RequestActor(ctx)` only when a principal
  is present, so an unauthenticated context records no actor rather
  than a wrong one.
- **#1463 admission-change note** (`pkg/orchestrator/finish.go`,
  `pkg/mcp/mock_tools.go`). `relayProposedTasks` still refuses CI-only
  grants (`TaskGrantCIOnlyRefusal`), still turns `api:ci-read@repo` into
  `reads`, and still clamps the sandbox shape to `SandboxShapeLimits`;
  the change records what it did (`admissionChanges`) and says so in
  one comment per run. `ciOnlyProposalGrantsOutcome` in the MCP tool
  computes the same classification for the reply text and files
  nothing itself. `clampProposalSandboxShape` gained a `fromTemplate`
  flag that changes only the note's wording; the clamp and the
  "settings row unreadable means grain's default maximum" rule are as
  the 2026-11-13 review read them (#1435).
- **#1438 source template** (see observation 1). The `task_source_template`
  table is created with `CREATE TABLE IF NOT EXISTS` and no
  `SchemaVersion` bump, for the same reason `task_filed_template` was;
  `putTask` deletes and rewrites it with the task's other child rows;
  `pkg/model/postgres/adapter.go` names its primary key. The UI `Task`
  struct has exactly one `json:"templateId"` field, so the new one does
  not collide (encoding/json would silently drop both). No update route
  reads a request's `templateId` into `TemplateID`: `req.TemplateID` is
  read at filing only, at the gate above.
- **#1385 hostd stop vs drain** (`pkg/hostd`). A new `killStopping`
  kind joins `killDraining`; both set `Exit.Draining`, and
  `killStopping` additionally sets `Exit.Error` to a fixed string. On
  the far side, `pkg/agent/hostd.go:160` tests `exit.Draining` before it
  looks at the error, so the daemon still re-queues such a run without
  a strike (`ErrHostdDraining`); the new error text is informational.
  Nothing about which processes hostd kills, or when, changed.
- **#1460 `grain-agentd -drain` and SIGUSR1** (`pkg/agentd/agentd.go`).
  `-drain` is a client of the hostd socket's existing `Hello`,
  `Cutover` and `Drain` requests, the ones `grain host -drain` already
  sends; `drainOnSignal` calls `srv.Drain()` on SIGUSR1, as `grain host`
  does. Neither adds a request the host agent did not already serve,
  and the server's `Drain` handler is unchanged. Who may drain is who
  could already: a process of the same user (socket in the user's
  runtime dir, signal to the user's own process). `defaultSocketFor`
  falls back from `$XDG_RUNTIME_DIR` to `/run/user/<uid>` only when
  `ownedDir` confirms that directory is owned by the caller's uid,
  then to the pre-existing `/tmp/grain-agentd-<uid>/`; the refusal to
  run as root is before the `-drain` branch, so a root `-drain` is
  refused too.
- **#1457 `grain-sandbox-network-none` in `grain.yaml`** (`deploy/kubernetes`).
  A tightening: a deployment that set `-k8s-network-policy` (needed for
  external capability grants) from `grain.yaml` alone used to run
  `network: none` tasks in pods no policy selected, with reach to the
  metadata server, the API server and every pod (the merge's test-run
  record shows it on kind with Calico). The policy is a verbatim copy
  of `networkpolicy.yaml`'s, kept in step by
  `k8s_networkpolicy_test.go`; it selects `grain.dev/network=none`
  pods, denies all ingress, and allows egress only to pods labelled
  `app.kubernetes.io/name: grain` (the git proxy and model proxies),
  with no port list, which is the pre-existing design. The new
  `k8s_grant_refusal_test.go` pins that an external grant is refused
  without the flag. `tests/deploy` now requires two `NetworkPolicy`
  objects in the manifest.
- **#1444 granule re-vendor** (`pkg/granule/granule.go`,
  `third_party/granule/pkg/dockervm/docker.go`). `pkg/granule.Delete`
  loses the retry loop that returned success after 30 attempts of
  "removal already in progress", and now returns granule's own result:
  granule's `remove` and `stopAndRemove` wait the in-progress removal
  out with `docker inspect` under a one-minute bound and return an
  error if it does not settle. A delete that leaves state behind is
  therefore an error rather than a silent success, which is the
  stricter direction. The vendored `.grain/` additions are granule's
  own PR-review template and trigger (`human: on-findings`,
  `gate: true`), read by grain only when a task runs on that
  repository.
- **#1442 `grain health` exit code** (`pkg/cli/health.go`). Exit 1 on a
  FAILING section; same `GET /api/sandboxes` read, no new route.
- **#1232 templates** (`.grain/templates/nightly-security-review.yml`,
  `weekly-dead-code.yml`, `.grain/baselines/dead-code-allowlist.txt`).
  Retargets `propose_task`'s `repo:` to `ai-on-gke/grain` and the knip
  path to `ui-hub/`; three allowlist lines for a Vite alias. No grants
  or capabilities change.
- **UI-only merges** (#1459, #1458, #1454, #1453, #1449, #1450, #1445,
  and #1467's `ui/` tests). File lists are confined to `ui/` and
  `ui-ng/` plus `docs/test-runs/`; the new API references in them are
  Playwright assertions against existing routes (`PUT
  /api/repos/{owner}/{name}/ci-mode`, `DELETE /api/hostds/{name}`,
  `GET /api/upgrade`), and #1450's "offer only the CI modes the server
  accepts" is presentation: the server's refusal on `ci-mode` is
  unchanged and its e2e spec (`next-repo-ci-mode-bypass.spec.js`) pins
  that a direct `PUT` is still refused.
- **Documentation merges** (#1478, #1448, #1451). Test plans and the
  previous security report; no code.

## Not reviewed

- `docs/test-runs/` and `docs/test-plans/` records (the bulk of the
  range by line count), beyond confirming they are records.
- `grain-plc/` (its own module; not touched in range).
- Byte-identity of `third_party/granule` with upstream `ccdbc4b` (see
  Tests run).
