# Walkthrough: CI API token narrowing for `ci_api_repos`, `ci://` slash job names, Stage 1 reads (`2026-11-16`)

Executes §10 of `docs/test-plans/2026-11-16.md` (filed by [Task #6330](/tasks/6330), executed in [Task #6483](/tasks/6483)) against a real `grain daemon -host-sandboxes -sandbox-port-base=0` + GitHub simulator environment, reproducing and fixing every defect found.

## Environment & Commit

- **Base HEAD tested:** `d40aaeebd` (`ai-on-gke/grain`, branch `grain/53549e42/task-6483-walkthrough-ci-api-token`)
- **Features under test:**
  1. **Narrowed CI API tokens** — PR #1556 (`c1f02fc70`, `c8e881cf0`, `5051b0178`): `grain_config.ci_api_repos` token narrowing enforced against real `ui.Server` across every route family and `POST /api/tasks/from-template` grant classes.
  2. **`ci://` slash & percent job names** — PR #1622 (`f4c40de64`): `grain ls`, `grain read`, `grain ci log --job`, `grain ci artifact`, and host `grain fs` on both GitHub Actions and Grain CI jobs containing `/` and `%`.
  3. **Stage 1 job-attempt reads** — PR #1656 (`5b936ac39`): `StateRepoPullRequestClient` forwards `WorkflowRunsOnCommit` and `ListActionsJobAttempts` in production so Stage 1 job-level verdicts, `pipeline.rc-failed` failure lines, and `pipeline.flake-budget` (including across `-discarded` tags) work live.
- **Host / runtime:** Linux (`debian`), Go `1.26.10`, SQLite `3.40.1`, PostgreSQL `15.20`, `grain daemon -host-sandboxes -sandbox-port-base=0`.
- **Harness scripts** (all under `docs/test-runs/2026-11-16-ci-api-narrowing/`):
  - `ghsim.go` (`//go:build ignore`): multi-repo `githubsim` server with `/_sim/seed-actions`, `/_sim/check-runs`, `git http-backend`, and HTTP request logging for `/actions/runs` and `/tags`.
  - `probe.sh`: 131-probe security & functional suite hitting every CI-reachable route family, unauthorized route family, `POST /api/notifications/release-pending`, and 16 `POST /api/tasks/from-template` grant classes.
  - `run_item1.sh`: boots `ghsim` (`acme/widgets`, `acme/secret-other`) + real daemon, verifies pre-allowlist admission refusal, adds `acme/widgets` to `ci_api_repos`, and runs `probe.sh` inside a live CI job holding a real `pkg/capability/ciapi`-minted `GRAIN_API_TOKEN` (and a second job holding `api:release-gate-read@acme/widgets`).
  - `run_item2.sh`: boots `ghsim` + real daemon + `fake-agy.py`, seeds a GitHub Actions run (`gha:acme:widgets:9101`) and runs a real Grain CI run with 6 tricky job names (`pkg/cli stress under load`, decoy `pkg`, `coverage 100% pass`, `pkg/cli 100% pass`, `ratio 50/50`, `ratio 50%2F50`) plus a failing job artifact, dispatches a real fake-agent task to run 35 sandbox CLI probes, and runs 13 host `grain fs` probes.
  - `run_item3.sh`: adapts `docs/test-runs/2026-11-15-release-train-publishing-held/` (`acme/grain` seeded with HEAD's `.grain/` + `.github/workflows/`) and drives 3 live `reconcileReleaseQualification` phases through `StateRepoPullRequestClient`.

---

## Summary of Bugs Found and Fixed (8 total: 3 security, 5 functional)

| # | Item | Severity | Bug | Fix | Regression Tests |
|---|------|----------|-----|-----|------------------|
| 1 | Item 1 | **Security** | `GET /api/schedules?limit=1&q=...` and `?f=repo:any:acme/secret-other` filtered/counted all repos before calling `readableSchedules`, creating a cross-repo existence oracle (`total=1, items=0`). | Filter via `readableSchedules` before `?q=` / `?f=` filtering and pagination in `pkg/ui/schedules.go`. | `pkg/daemon/ciapi_test.go`, `tests/security/ciapi_narrowing_test.go` |
| 2 | Item 1 | **Security** | `GET /api/persistent-sandboxes?limit=1` paginated across all repos before `readablePersistentSandboxes`, leaking a foreign persistent sandbox's name inside base64url `nextCursor` (`o:1:alpha-other-secret-svc`) and creating a `?q=` existence oracle (`total=1`). | Filter via `readablePersistentSandboxes` before `?q=` filtering and pagination in `pkg/ui/persistent_sandboxes.go`. | `pkg/daemon/ciapi_test.go`, `tests/security/ciapi_narrowing_test.go` |
| 3 | Item 1 | **Security** | `GET /api/templates` returned `acme/widgets`-targeted templates carrying cross-repo `reads`/`writes`/`declaredIn`/API grants to a repo-narrowed CI token, disclosing foreign repo names and prompts (`templateAllowedForGrants` was skipped for `accessRole` callers). | Enforce `templateAllowedForGrants(t, role.set)` for non-full `accessRole` callers in `pkg/ui/templates.go`. | `pkg/daemon/ciapi_test.go`, `tests/security/ciapi_narrowing_test.go` |
| 4 | Item 1 | Functional | `pkg/capability/ciapi.narrowPath` treated any route parameter named `{name}` (without `{owner}`), such as `GET /api/ci/runs/{id}/artifacts/{job}/{name}`, as repo `""/<name>`, falsely returning `403: this token reaches acme/widgets only, not /widgets-report` on the repo's own CI artifacts. | Only check `{owner}/{name}` in `narrowPath` when both `owner` and `name` path values are non-empty (`pkg/capability/ciapi/serve.go`). | `pkg/capability/ciapi/ciapi_test.go`, `pkg/daemon/ciapi_test.go` |
| 5 | Item 1 | Functional | `pkg/capability/ciapi.Route` stripped the trailing slash from Go 1.22 wildcard patterns (`/api/fs/task/{path...}` -> `/api/fs/task`), causing Go's `ServeMux` to 307-redirect `/api/fs/ci/` and `/api/fs/task/` in a loop. | Preserve the trailing `/` before `{name...}` wildcards in `Route` (`pkg/capability/ciapi/serve.go`). | `pkg/capability/ciapi/ciapi_test.go`, `pkg/daemon/ciapi_test.go` |
| 6 | Item 2 | Functional | When `"ratio 50/50"` and `"ratio 50%2F50"` coexisted in the same run (both GHA and Grain CI), `escapeCIJobName` listed both identically as `ratio 50%2F50/` and `matchCIJob` checked literal `rest[0]` before `unescapeCIJobName`, causing `grain ci log --job "ratio 50/50"` to return **`"ratio 50%2F50"`'s log** instead! | Make `escapeCIJobName` injective (also escape `%` → `%25` when `unescapeCIJobName` would decode it, while leaving plain `"coverage 100% pass"` unchanged) and check multi-segment slash joins (`n >= 2`), then unescaped `rest[0]`, then literal `rest[0]` in `matchCIJob` (`pkg/namespace/namespace.go`). | `pkg/namespace/namespace_test.go`, `pkg/sandboxcli/actions_slash_job_test.go` |
| 7 | Item 2 | Functional | Stored local Grain CI artifacts (`DirArtifacts` at `<dataDir>/state/ci-artifacts/<run>/<job>.<name>.tar.gz`) were invisible in `grain ls ci://.../<run>` and unreadable via `grain ci artifact` (`not found: artifact "report"`): `pkg/daemon/run.go` gated `cfg.ciArtifacts` on `cfg.ObjectStore != nil`, leaving `nsServer.Artifacts == nil`, and `resolveCI` never hydrated artifacts omitted by `StoredRunStore`. | Initialize `cfg.ciArtifacts` for local `DataDir` deployments in `pkg/daemon/run.go` and hydrate stored artifacts via `s.Artifacts.List` in `resolveCI` (`pkg/namespace/namespace.go`). | `pkg/namespace/namespace_test.go`, `pkg/sandboxcli/actions_slash_job_test.go` |
| 8 | Item 2 | Functional | `grain ci artifact "pkg/cli stress under load" report --run <run>` failed with `not found: artifact "cli stress under load/report"`: `ciArtifact` did not call `ciJobSegment(job)` and `resolveCIArtifacts` took `rem[0]` literally instead of using `matchCIJob`. | Use `ciJobSegment(job)` in `pkg/sandboxcli/sandboxcli.go` and resolve artifact job keys via `matchCIJob` + `escapeCIJobName` in `resolveCIArtifacts` (`pkg/namespace/namespace.go`). | `pkg/namespace/namespace_test.go`, `pkg/sandboxcli/actions_slash_job_test.go` |

---

## Item 1 — Narrowed CI API Tokens for `ci_api_repos`

### Steps

1. Started `ghsim` serving two repositories (`acme/widgets` and `acme/secret-other`) and real `grain daemon -host-sandboxes -sandbox-port-base=0`.
2. Seeded cross-repo fixtures:
   - Tasks: `#3` (`acme/widgets`, `externalRef: WALK-42`), `#4` (`acme/secret-other`, `externalRef: WALK-42`), `#5` (standalone `no_repo`).
   - CI runs & artifacts: Grain CI runs for `acme/widgets` (with stored artifact `own-artifact/widgets-report`) and `acme/secret-other` (with stored artifact `secret-job/secret-artifact`), plus GitHub Actions runs `gha:acme:widgets:9002` and `gha:acme:secret-other:9001`.
   - Schedules: one for `acme/widgets` and one for `acme/secret-other` (`cobalt-zebra-secret`), plus one standalone.
   - Persistent sandboxes: `widgets-svc` (`acme/widgets`) and `alpha-other-secret-svc` (`acme/secret-other`, sorted first alphabetically to expose cursor leaks).
   - Notifications: `release.pending:v9.9.0` on `acme/secret-other`.
   - Templates: 16 repo-declared templates under `acme/widgets/.grain/templates/` covering every grant class (`clean`, `own-ci-read`, `own-grain-ci`, `cross-read`, `cross-write`, `cross-api-grant`, `unscoped-api-grant`, `non-ci-write-grant`, `grain-daemon-grant`, `dangerous-grant`, `open-network`, `admission-bypass`, `high-priority`, `keep-running`, `declared-push`) plus a foreign template on `acme/secret-other`.
3. **Phase 1 (Admission refusal before allowlisting):** Submitted a Grain CI run on `acme/widgets` declaring `capabilities: [grain#ci-api]` before adding `acme/widgets` to `ci_api_repos`. Verified refusal:
   `ci_api_repos` gate rejected the run (`"acme/widgets is not listed in ci_api_repos"`).
4. **Phase 2 (Allowlisted `acme/widgets`):** Updated `PUT /api/settings` with `ciApiRepos: ["acme/widgets"]`, ran `probe.sh` inside a real Grain CI job holding the minted `GRAIN_API_TOKEN` (and a second job holding `api:release-gate-read@acme/widgets` for `POST /api/notifications/release-pending`).

### Pre-Fix Reproduction (HEAD `d40aaeebd`)

Running `run_item1.sh` against unmodified HEAD produced 8 real failures/leaks (plus 2 initial probe-script bugs since fixed in `probe.sh` — URL-encoding `#` in `?pr=` and parsing `after` rather than `since` on `/api/events`):

```text
| GET | /api/fs/task/                                                     | 307 | FAIL | <a href="/api/fs/task/?repo=acme%2Fwidgets">Temporary Redirect</a>. |
| GET | /api/fs/ci/                                                       | 307 | FAIL | <a href="/api/fs/ci/?repo=acme%2Fwidgets">Temporary Redirect</a>. |
| GET | /api/ci/runs/<own>/artifacts/own-artifact/widgets-report          | 403 | FAIL | {"error":"this token reaches acme/widgets only, not /widgets-report"} |
| GET | /api/schedules?limit=1&q=cobalt-zebra-secret                      | 200 | LEAK | total=1 items=0 (cross-repo schedule count/oracle leak!) |
| GET | /api/schedules?limit=1&f=repo:any:acme/secret-other               | 200 | LEAK | total=1 (foreign repo filter leaked count!) |
| GET | /api/persistent-sandboxes?limit=1                                 | 200 | LEAK | total=2 items=0 decoded_cursor=o:1:alpha-other-secret-svc |
| GET | /api/persistent-sandboxes?limit=1&q=alpha-other-secret-svc        | 200 | LEAK | total=1 (cross-repo persistent-sandbox oracle!) |
| GET | /api/templates                                                    | 200 | LEAK | count=16 leaked foreign template containing cobalt-zebra-secret! |
```

### Post-Fix Route → Status Table (131 probes, `failures_or_leaks=0`)

| Method | Route                                                          | Status | Result   | Notes |
|--------|----------------------------------------------------------------|--------|----------|-------|
| GET    | /api/tasks (no token)                                          | 401    | PASS     | unauthorized without bearer token |
| GET    | /api/tasks (bad token)                                         | 401    | PASS     | unauthorized with bogus token |
| GET    | /api/tasks                                                     | 200    | PASS     | count=1 repos=acme/widgets |
| GET    | /api/tasks?repo=acme/secret-other                              | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/tasks/states                                              | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/st |
| GET    | /api/states                                                    | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/states d |
| GET    | /api/tasks/summaries?ids=3,4,5                                 | 200    | PASS     | ids=3 missing= |
| GET    | /api/tasks/summaries?ids=3&repo=acme/secret-other              | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/tasks/wait?ids=3&until=any&timeout=1                      | 200    | PASS     | tasks= |
| GET    | /api/tasks/wait?ids=4&until=any&timeout=1                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/wait?ids=5&until=any&timeout=1                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/wa |
| GET    | /api/tasks/wait?ids=3,4&until=any&timeout=1                    | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/3                                                   | 200    | PASS     | id=3 repo=acme/widgets |
| GET    | /api/tasks/4                                                   | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5                                                   | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/messages                                          | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/messages                                          | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/prompt                                            | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/prompt                                            | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/context                                           | 403    | PASS     | ci:acme/widgets (role ci-api) holds dispatch:read, but not on acme/secret-other: ask an ac |
| GET    | /api/tasks/5/context                                           | 403    | PASS     | ci:acme/widgets (role ci-api) holds dispatch:read only on acme/widgets, and GET /api/tasks |
| GET    | /api/tasks/4/diff-preview                                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/diff-preview                                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/tree                                              | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/tree                                              | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/attempts/1/transcript                             | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/attempts/1/transcript                             | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/4/attempts/1/capability-audit                       | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/tasks/5/attempts/1/capability-audit                       | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read only on acme/widgets, and GET /api/tasks/{i |
| GET    | /api/tasks/3/messages                                          | 200    | PASS     | messages=0 |
| GET    | /api/tasks/3/prompt                                            | 200    | PASS     | prompt ok |
| GET    | /api/tasks/3/context                                           | 200    | PASS     | context ok |
| GET    | /api/inbox                                                     | 200    | PASS     | items=0 |
| GET    | /api/inbox?repo=acme/secret-other                              | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/refs/WALK-42/summary                                      | 200    | PASS     | total=1 ids= |
| GET    | /api/refs/WALK-42/summary?repo=acme/secret-other               | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/fs/task/                                                  | 403    | PASS     | forbidden: task://: that tool belongs to the "https://grain.dev/capabilities/api#tasks:rea |
| GET    | /api/fs/task/3/summary.md                                      | 200    | PASS     | non-json (237B) |
| GET    | /api/fs/task/4/summary.md                                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds tasks:read, but not on acme/secret-other: ask an acces |
| GET    | /api/fs/task/5/summary.md                                      | 403    | PASS     | forbidden: task://5/summary.md: another task's record needs an "https://grain.dev/capabili |
| GET    | /api/fs/ci/                                                    | 200    | PASS     | non-json (13B) |
| GET    | /api/fs/ci/acme/widgets                                        | 200    | PASS     | non-json (58B) |
| GET    | /api/fs/ci/acme/secret-other                                   | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/fs/ci/acme/secret-other/bd39e939b1ca4b2f                  | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/fs/src/acme/widgets                                       | 403    | PASS     | GET /api/fs/src/{path...} needs api:deployment:read, which this job's token does not hold  |
| GET    | /api/fs/config                                                 | 403    | PASS     | GET /api/fs/config/{path...} needs api:settings:read, which this job's token does not hold |
| GET    | /api/ci/runs                                                   | 200    | PASS     | runs=3 |
| GET    | /api/ci/runs?repo=acme/secret-other                            | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/ci/runs/d36604749aaab016                                  | 200    | PASS     | id=d36604749aaab016 repo=acme/widgets |
| GET    | /api/ci/runs/d36604749aaab016/artifacts                        | 200    | PASS     | artifacts=1 |
| GET    | /api/ci/runs/d36604749aaab016/artifacts/own-artifact/widgets-report | 200    | PASS     | non-json (123B) |
| GET    | /api/ci/runs/gha:acme:widgets:9002                             | 200    | PASS     | gha own run ok |
| GET    | /api/ci/runs/gha:acme:widgets:9002/log                         | 200    | PASS     | gha own log ok |
| GET    | /api/ci/runs/bd39e939b1ca4b2f                                  | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/runs/bd39e939b1ca4b2f/log                              | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/runs/bd39e939b1ca4b2f/artifacts                        | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/runs/bd39e939b1ca4b2f/artifacts/secret-job/secret-artifact | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/runs/gha:acme:secret-other:9001                        | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/runs/gha:acme:secret-other:9001/log                    | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/ci/workflows?ref=main                                     | 403    | PASS     | grain CI is not enabled for acme/widgets: set its CI Mode to "Use Grain CI" on the repo's  |
| GET    | /api/ci/workflows?repo=acme/secret-other&ref=main              | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/ci/faults                                                 | 200    | PASS     | faults=0 |
| GET    | /api/ci/faults?repo=acme/secret-other                          | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/merge-batches                                             | 200    | PASS     | batches=0 |
| GET    | /api/merge-batches?repo=acme/secret-other                      | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/base-branch-gates                                         | 200    | PASS     | gates=0 |
| GET    | /api/base-branch-gates?repo=acme/secret-other                  | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/pull-requests/status?pr=acme/secret-other%231             | 403    | PASS     | ci:acme/widgets (role ci-api) holds ci:read, but not on acme/secret-other: ask an access-c |
| GET    | /api/events?since=0&limit=100                                  | 200    | PASS     | events=6 (all repo=acme/widgets) |
| GET    | /api/events?since=0&repo=acme/secret-other                     | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/pipeline/changes                                          | 200    | PASS     | entries=0 |
| GET    | /api/pipeline/changes?repo=acme/secret-other                   | 403    | PASS     | this token reads acme/widgets only, not acme/secret-other |
| GET    | /api/notifications                                             | 200    | PASS     | count=0 |
| GET    | /api/notifications?limit=1&q=cobalt-zebra-secret               | 200    | PASS     | total=0 items=0 |
| GET    | /api/schedules                                                 | 200    | PASS     | count=2 |
| GET    | /api/schedules?limit=1&q=cobalt-zebra-secret                   | 200    | PASS     | total=0 items=0 (no cross-repo oracle) |
| GET    | /api/schedules?limit=1&f=repo:any:acme/secret-other            | 200    | PASS     | total=0 (foreign repo filter yields 0) |
| GET    | /api/schedules/repo:schedule:acme:secret-other:secret-sched/composition | 403    | PASS     | ci:acme/widgets (role ci-api) holds schedules:read only on acme/widgets, and GET /api/sche |
| GET    | /api/persistent-sandboxes                                      | 200    | PASS     | count=1 |
| GET    | /api/system/persistent-sandboxes                               | 200    | PASS     | legacy alias count=1 |
| GET    | /api/persistent-sandboxes?limit=1                              | 200    | PASS     | total=1 items=1 cursor=none |
| GET    | /api/persistent-sandboxes?limit=1&q=alpha-other-secret-svc     | 200    | PASS     | total=0 (no cross-repo oracle) |
| GET    | /api/persistent-sandboxes/alpha-other-secret-svc/logs          | 403    | PASS     | GET /api/persistent-sandboxes/{name}/logs needs api:deployment:read, which this job's toke |
| GET    | /api/templates                                                 | 200    | PASS     | count=12 (no foreign templates) |
| GET    | /api/templates/repo:template:acme:secret-other:secret-tmpl/composition | 403    | PASS     | ci:acme/widgets (role ci-api) holds templates:read only on acme/widgets, and GET /api/temp |
| GET    | /api/suites                                                    | 403    | PASS     | ci:acme/widgets (role ci-api) holds suites:read only on acme/widgets, and GET /api/suites  |
| GET    | /api/suite-runs                                                | 200    | PASS     | count=0 |
| GET    | /api/deployment/reachable-repos                                | 200    | PASS     | repos=acme/widgets |
| GET    | /api/repos/acme/widgets/settings                               | 200    | PASS     | own repo settings ok |
| GET    | /api/repos/acme/secret-other/settings                          | 403    | PASS     | this token reaches acme/widgets only, not acme/secret-other |
| GET    | /api/repos/acme/widgets/releases                               | 200    | PASS     | own repo releases ok |
| GET    | /api/repos/acme/secret-other/releases                          | 403    | PASS     | this token reaches acme/widgets only, not acme/secret-other |
| GET    | /api/repos/acme/widgets/declarations                           | 200    | PASS     | own repo declarations ok |
| GET    | /api/repos/acme/secret-other/declarations                      | 403    | PASS     | this token reaches acme/widgets only, not acme/secret-other |
| GET    | /api/deployment/repos/acme/widgets/config                      | 200    | PASS     | own repo config ok |
| GET    | /api/deployment/repos/acme/secret-other/config                 | 403    | PASS     | this token reaches acme/widgets only, not acme/secret-other |
| GET    | /api/release-approval-key                                      | 403    | PASS     | ci:acme/widgets (role ci-api) holds releases:read only on acme/widgets, and GET /api/relea |
| GET    | /api/git-ambient-sources                                       | 403    | PASS     | ci:acme/widgets (role ci-api) holds repos:read only on acme/widgets, and GET /api/git-ambi |
| GET    | /api/pause                                                     | 403    | PASS     | ci:acme/widgets (role ci-api) holds dispatch:read only on acme/widgets, and GET /api/pause |
| GET    | /api/pipeline-pause                                            | 403    | PASS     | ci:acme/widgets (role ci-api) holds dispatch:read only on acme/widgets, and GET /api/pipel |
| GET    | /api/settings                                                  | 403    | PASS     | GET /api/settings needs api:settings:read, which this job's token does not hold (it holds  |
| GET    | /api/config                                                    | 403    | PASS     | GET /api/config needs api:settings:read, which this job's token does not hold (it holds ci |
| GET    | /api/secrets                                                   | 403    | PASS     | GET /api/secrets needs api:secrets:read, which this job's token does not hold (it holds ci |
| GET    | /api/connections                                               | 403    | PASS     | GET /api/connections is not a route of grain's API a CI job's token can reach |
| GET    | /api/capabilities                                              | 403    | PASS     | GET /api/capabilities is not a route of grain's API a CI job's token can reach |
| GET    | /api/memory                                                    | 403    | PASS     | GET /api/memory needs api:memory:read, which this job's token does not hold (it holds ci:r |
| GET    | /api/sandboxes                                                 | 403    | PASS     | GET /api/sandboxes needs api:deployment:read, which this job's token does not hold (it hol |
| GET    | /api/metrics                                                   | 403    | PASS     | GET /api/metrics needs api:deployment:read, which this job's token does not hold (it holds |
| POST   | /api/tasks                                                     | 403    | PASS     | POST /api/tasks needs api:tasks:write, which this job's token does not hold (it holds ci:r |
| PUT    | /api/settings                                                  | 403    | PASS     | PUT /api/settings needs api:settings:write, which this job's token does not hold (it holds |
| POST   | /api/notifications/release-pending (no release-gate-read)      | 403    | PASS     | POST /api/notifications/release-pending needs https://grain.dev/capabilities/api#release-g |
| POST   | /api/notifications/release-pending (own v9.9.1)                | 200    | PASS     | raised with repo=acme/widgets |
| POST   | /api/notifications/release-pending                             | 403    | PASS     | notification release.pending:v9.9.0 belongs to acme/secret-other, not acme/widgets |
| POST   | /api/tasks/from-template (clean)                               | 201    | PASS     | id=7 state=queued |
| POST   | /api/tasks/from-template (own-ci-read)                         | 201    | PASS     | id=8 state=queued |
| POST   | /api/tasks/from-template (own-grain-ci)                        | 201    | PASS     | id=9 state=queued |
| POST   | /api/tasks/from-template (cross-repo-body)                     | 403    | PASS     | this token files tasks for acme/widgets only, not acme/secret-other |
| POST   | /api/tasks/from-template (cross-read)                          | 403    | PASS     | this CI token reaches acme/widgets only, and template repo:template:acme:widgets:cross-rea |
| POST   | /api/tasks/from-template (cross-write)                         | 403    | PASS     | this CI token reaches acme/widgets only, and template repo:template:acme:widgets:cross-wri |
| POST   | /api/tasks/from-template (cross-api-grant)                     | 403    | PASS     | this CI token reaches acme/widgets only, and template repo:template:acme:widgets:cross-api |
| POST   | /api/tasks/from-template (unscoped-api-grant)                  | 201    | PASS     | id=10 state=proposed |
| POST   | /api/tasks/from-template (non-ci-write-grant)                  | 201    | PASS     | id=11 state=proposed |
| POST   | /api/tasks/from-template (grain-daemon-grant)                  | 201    | PASS     | id=12 state=proposed |
| POST   | /api/tasks/from-template (dangerous-grant)                     | 201    | PASS     | id=13 state=proposed |
| POST   | /api/tasks/from-template (open-network)                        | 201    | PASS     | id=14 state=proposed |
| POST   | /api/tasks/from-template (admission-bypass)                    | 201    | PASS     | id=15 state=proposed |
| POST   | /api/tasks/from-template (high-priority)                       | 201    | PASS     | id=16 state=proposed |
| POST   | /api/tasks/from-template (keep-running)                        | 201    | PASS     | id=17 state=proposed |
| POST   | /api/tasks/from-template (declared-push)                       | 201    | PASS     | id=18 state=proposed |

SUMMARY: failures_or_leaks=0

### Exhaustive `ciAPIScopes` Audit

Audited every route in `pkg/ui/route_scopes.go` reachable under the 10 `ciAPIScopes` (`ci:read`, `dispatch:read`, `releases:read`, `repos:read`, `sandboxes:read`, `schedules:read`, `suites:read`, `tasks:from-template`, `tasks:read`, `templates:read`). Confirmed that every other deployment-wide or cross-repo route (`GET /api/templates/{id}/composition`, `GET /api/suites`, `GET /api/suite-runs/{id}`, `GET /api/git-ambient-sources`, `GET /api/release-approval-key`, `GET /api/pause`, `GET /api/pipeline-pause`, `GET /api/tasks/states`, `GET /api/states`) is refused `403` by `authorizeRoleCall` / `agentCallRepo`, and `GET /api/notifications` already filters via `readableNotifications`.

---

## Item 2 — `ci://` Slash & Percent Job Names (Both CI Modes)

### Steps

1. Started `ghsim` (`acme/widgets`) and real `grain daemon -host-sandboxes -sandbox-port-base=0` with `fake-agy.py`.
2. Seeded GitHub Actions run `gha:acme:widgets:9101` and executed a real Grain CI run (`ci_mode=grain`, `.grain/workflows/slash-names.yml`) each containing six jobs:
   - `"pkg/cli stress under load"` (failing, with `Build` + `Stress` steps and declared artifact `report` on Grain CI)
   - `"pkg"` (decoy matching the prefix before `/`)
   - `"coverage 100% pass"` (plain `%`)
   - `"pkg/cli 100% pass"` (`/` + `%`)
   - `"ratio 50/50"` (slash colliding with `%2F` form)
   - `"ratio 50%2F50"` (literal `%2F` colliding with `"ratio 50/50"`)
3. Dispatched a real fake-agent task (`api:ci:read@acme/widgets`) running 35 sandbox CLI commands (`grain ls`, `grain read`, `grain ci log --job ... --tail 50`, `grain ci artifact`), and ran 13 host CLI `grain fs ls` / `grain fs read` commands against `/api/fs/ci/`.

> Note: `--tail 50` on `grain ci log` / `grain read` counts **bytes** (use `--lines 50` for lines), matching PR #1622.

### Pre-Fix Reproduction (HEAD `d40aaeebd`)

1. **`%2F`/`/` job-name collision (Bug #6):**
   - `grain ls ci://acme/widgets/gha:acme:widgets:9101` listed `ratio 50%2F50/` **twice** (identical entries for `"ratio 50/50"` and `"ratio 50%2F50"`).
   - `grain ci log gha:acme:widgets:9101 --job "ratio 50/50" --tail 50` returned `2026-11-16T15:00:01Z GHA_RATIO_ESCAPED_LOG` (wrong job!).
   - `grain ci log <grain-run> --job "ratio 50/50" --step Run --tail 50` returned `GRAIN_RATIO_ESCAPED_LOG` (wrong job!).
2. **Stored Grain CI artifacts invisible & unreadable via `ci://` (Bug #7):**
   - `GET /api/ci/runs/<run>` showed `pkg_cli_stress` had `{job: "pkg_cli_stress", name: "report", stored: true}` and `<dataDir>/state/ci-artifacts/<run>/pkg_cli_stress.report.tar.gz` existed on disk, yet `grain ls ci://acme/widgets/<run>` omitted `artifacts/`, `grain ls .../artifacts` was empty, and `grain ci artifact pkg_cli_stress report --run <run>` exited `1` with `grain: not found: artifact "report"`.
3. **`grain ci artifact` slash display names broken (Bug #8):**
   - `grain ci artifact "pkg/cli stress under load" report --run <run>` exited `1` with `grain: not found: artifact "cli stress under load/report"`.

### Post-Fix Live Outputs (All 35 Sandbox Probes + 13 Host CLI Probes Passed, `EXIT=0`)

#### Sandbox CLI (`grain ls`, `grain read`, `grain ci log --job ... --tail 50`, `grain ci artifact`)

```text
=== CASE: GHA ls run ===
EXIT=0
pkg%2Fcli stress under load/
pkg/
coverage 100% pass/
pkg%2Fcli 100%25 pass/
ratio 50%2F50/
ratio 50%252F50/

=== CASE: GHA ci log --job slash raw ===
EXIT=0
2026-11-16T15:00:02Z GHA_SLASH_JOB_STRESS_MARKER

=== CASE: GHA ci log --job decoy pkg ===
EXIT=0
2026-11-16T15:00:01Z GHA_DECOY_PKG_LOG

=== CASE: GHA ci log --job pct-only ===
EXIT=0
2026-11-16T15:00:01Z GHA_PCT_ONLY_OK

=== CASE: GHA ci log --job slash+pct ===
EXIT=0
2026-11-16T15:00:01Z GHA_SLASH_AND_PCT_OK

=== CASE: GHA ci log --job ratio 50/50 (collision check) ===
EXIT=0
2026-11-16T15:00:01Z GHA_RATIO_SLASH_LOG

=== CASE: GHA ci log --job ratio 50%252F50 (ls-listed form of ratio 50%2F50) ===
EXIT=0
2026-11-16T15:00:01Z GHA_RATIO_ESCAPED_LOG

=== CASE: Grain ls run ===
EXIT=0
pkg_cli_stress/
coverage_pct/
pkg/
ratio_escaped/
ratio_slash/
slash_and_pct/
artifacts/

=== CASE: Grain ci log --job slash raw --step Stress ===
EXIT=0
GRAIN_STRESS_LINE_1
GRAIN_STRESS_MARKER_SLASH_JOB

=== CASE: Grain ci log --job decoy pkg --step Run ===
EXIT=0
GRAIN_DECOY_PKG_JOB

=== CASE: Grain ci log --job pct-only --step Run ===
EXIT=0
GRAIN_PCT_ONLY_OK

=== CASE: Grain ci log --job slash+pct --step Run ===
EXIT=0
GRAIN_SLASH_AND_PCT_OK

=== CASE: Grain ci log --job ratio 50/50 --step Run (collision check) ===
EXIT=0
GRAIN_RATIO_SLASH_LOG

=== CASE: Grain ci log --job ratio 50%252F50 --step Run (ls-listed form of ratio 50%2F50) ===
EXIT=0
GRAIN_RATIO_ESCAPED_LOG

=== CASE: Grain ls artifacts ===
EXIT=0
pkg_cli_stress/

=== CASE: Grain ls artifacts by slash display name ===
EXIT=0
report

=== CASE: Grain ci artifact by jobId (tar extract) ===
EXIT=0
slash job artifact body

=== CASE: Grain ci artifact by slash display name (tar extract) ===
EXIT=0
slash job artifact body

=== CASE: Grain ci artifact by %2F display name (tar extract) ===
EXIT=0
slash job artifact body
```

#### Host CLI (`grain fs ls` / `grain fs read` via `/api/fs/ci/`)

```text
=== HOST: grain fs ls GHA run ===
pkg%2Fcli stress under load/
pkg/
coverage 100% pass/
pkg%2Fcli 100%25 pass/
ratio 50%2F50/
ratio 50%252F50/

=== HOST: grain fs read GHA ratio 50/50 raw ===
2026-11-16T15:00:01Z GHA_RATIO_SLASH_LOG
=== HOST: grain fs read GHA ratio 50%2F50 (%2F -> ratio 50/50) ===
2026-11-16T15:00:01Z GHA_RATIO_SLASH_LOG
=== HOST: grain fs read GHA ratio 50%252F50 (%252F -> ratio 50%2F50) ===
2026-11-16T15:00:01Z GHA_RATIO_ESCAPED_LOG

=== HOST: grain fs ls Grain run ===
pkg_cli_stress/
coverage_pct/
pkg/
ratio_escaped/
ratio_slash/
slash_and_pct/
artifacts/

=== HOST: grain fs read Grain artifact by slash display name (tar extract) ===
slash job artifact body
```

---

## Item 3 — Stage 1 Job-Attempt Reads & Flake Budget on a Live Release Train

### Steps

1. Seeded `acme/grain` with HEAD's `.grain/` and `.github/workflows/` (plus release tag `v3.8.0` on the base commit), started `ghsim` and real `grain daemon -host-sandboxes -sandbox-port-base=0` (where every reconcile cycle wraps the forge client in `StateRepoPullRequestClient`).
2. Paused task dispatch (`POST /api/pipeline-pause`), set `ci_mode=github`, enabled `declarations=true`, and verified `repo:suite:acme:grain:release-qualification` imported.
3. Exercised three consecutive release-qualification reconcile ticks (`releaseQualificationInterval = 60s`) against live multi-attempt `nightly` runs:
   - **Phase 1 (`v3.9.0-rc.1` + `v3.9.0-rc.1-discarded`, run `9201`):** `"pkg/cli stress under load"` failed attempt 1 and passed attempt 2 (flake retry #1); `"sustained load"` failed attempt 1 and failed attempt 2 (confirmed red); `"stage 1 qualification verdict"` (rollup) failed both attempts.
   - **Phase 2 (`v3.9.0-rc.2`, run `9202`):** `"pkg/cli stress under load"` failed attempt 1 and passed attempt 2 (flake retry #2 within 24h -> hits `FlakeBudgetRetries = 2`); `"sustained load"` passed attempt 1; only rollup `"stage 1 qualification verdict"` failed (run `conclusion: "failure"`).
   - **Phase 3 (`v3.9.0-rc.3`, active, no `-discarded` tag, run `9203`):** matrix shard `"go, shuffled and repeated (1)"` passed attempt 1, `"go, shuffled and repeated (2)"` failed attempt 1 and attempt 2 (confirmed red), rollup failed.

### Outcomes (All Passed, No Bugs Found)

1. **HTTP proof that `StateRepoPullRequestClient` forwarded both `WorkflowRunsOnCommit` and `ListActionsJobAttempts` in production (`ghsim.log`):**
   ```text
   ghsim req: GET /repos/acme/grain/actions/runs?head_sha=818742c46a5a51398ec5e8ae8501ac21172416d9&per_page=100
   ghsim req: GET /repos/acme/grain/actions/runs/9201/jobs?per_page=100&filter=all
   ghsim req: GET /repos/acme/grain/actions/runs?head_sha=efea8819bd1d1174837e09fc730738652a31a8b9&per_page=100
   ghsim req: GET /repos/acme/grain/actions/runs/9202/jobs?per_page=100&filter=all
   ghsim req: GET /repos/acme/grain/actions/runs?head_sha=df5268cd79d5a582c64d15e6741265085be76e4c&per_page=100
   ghsim req: GET /repos/acme/grain/actions/runs/9203/jobs?per_page=100&filter=all
   ```
2. **Phase 1 (`syncDiscardedStage1` on `v3.9.0-rc.1-discarded`):**
   - Raised `pipeline.rc-failed:v3.9.0-rc.1` (`warning`) with detailed job-level Stage 1 failure line (ignoring the rollup job):
     > `Stage 1 (nightly):`
     > `- sustained load: failed again on its retry (failure,failure): confirmed red`
   - Recorded flake retry #1 for `pkg/cli stress under load` via `syncDiscardedStage1` → `noteStage1Flake` without prematurely raising `pipeline.flake-budget`.
3. **Phase 2 (`watchStage1` on `v3.9.0-rc.2`):**
   - Cleared `pipeline.rc-failed:v3.9.0-rc.1` (`clearedAt: 2026-11-16T16:34:24.517706816Z`) when `v3.9.0-rc.2` superseded it.
   - Counted flake retry #2 for `pkg/cli stress under load` across discarded `v3.9.0-rc.1` + active `v3.9.0-rc.2` and raised `pipeline.flake-budget:acme/grain:pkg/cli stress under load` (`warning`):
     > `pkg/cli stress under load failed and was retried 2 times in the last 24h of Stage 1 (nightly), most recently on v3.9.0-rc.2 (attempts failure,success). A flake retry is spent per candidate, so a flaky one makes red candidates out of healthy ones: de-flake it, or quarantine what it tests.`
   - Treated the rollup-only failure on run `9202` as Stage 1 green (`v.passed == true`) and proceeded directly to Stage 2 gating without raising `pipeline.rc-failed:v3.9.0-rc.2` (the PAT fixture correctly logged `not qualifying v3.9.0-rc.2: its forge connection is not a GitHub App grain can name`).
4. **Phase 3 (`watchStage1` on active `v3.9.0-rc.3`, no `-discarded` tag):**
   - Normalized matrix shard `"go, shuffled and repeated (2)"` → `"go, shuffled and repeated"` and raised `pipeline.rc-failed:v3.9.0-rc.3` (`warning`):
     > `Stage 1 (nightly):`
     > `- go, shuffled and repeated: failed again on its retry (failure,failure): confirmed red`
   - Reached `RELEASE_MAX_RC_ATTEMPTS (3)` and raised `pipeline.release-escalated:v3.9.0` (`critical`) aggregating both `v3.9.0-rc.1` and `v3.9.0-rc.3`'s job-level Stage 1 failure lines.
5. **Events & `/next/` release views:**
   - Recorded in `integration_event` (served on `GET /api/events`) and `GET /api/notifications` (consumed by `/next/inbox`):
     | Event ID | Type | Notification ID | Subject | Severity |
     |---|---|---|---|---|
     | 8 | `notification.raised` | 1 | `pipeline.rc-failed:v3.9.0-rc.1` | `warning` |
     | 10 | `notification.cleared` | 1 | `pipeline.rc-failed:v3.9.0-rc.1` | `warning` |
     | 11 | `notification.raised` | 2 | `pipeline.flake-budget:acme/grain:pkg/cli stress under load` | `warning` |
     | 13 | `notification.raised` | 3 | `pipeline.rc-failed:v3.9.0-rc.3` | `warning` |
     | 14 | `notification.raised` | 4 | `pipeline.release-escalated:v3.9.0` | `critical` |
   - Verified `GET /api/repos/acme/grain/release` (`enabled: true, available: true`), `GET /api/repos/acme/grain/releases`, `GET /next/inbox` (`200`), and `GET /next/repos/acme/grain/releases` (`200`).

---

## Verification

- `go test ./pkg/capability/ciapi/...` — PASS (including `TestNarrowPathDoesNotConfuseArtifactNameWithRepoName`, `TestRoutePreservesTrailingSlashOnWildcardFSRoutes`)
- `go test ./pkg/daemon/... -run "TestAListedRepositorysCIAPIToken|TestStateRepoPullRequestClientForwards"` — PASS (including `TestAListedRepositorysCIAPITokenNarrowsPagedListsTemplatesAndFS`)
- `go test ./tests/security/...` — PASS (including `TestCIAPINarrowingPreventsCrossRepoLeaks`)
- `go test ./pkg/namespace/...` — PASS (including `TestCIJobPercentAndSlashCoexistence`, `TestCIArtifactsHydratedFromDirArtifactsAndSlashJobNames`)
- `go test ./pkg/sandboxcli/...` — PASS (including `TestSandboxCLISlashPercentCollisionAndArtifacts`)
- `go test ./tests/hygiene/... ./pkg/ports/...` — PASS
