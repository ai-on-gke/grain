# Test Run Report: Release-from-Trunk Pending-Release Approval (CLI, Inbox, Alerts) (`docs/test-plans/2026-10-28.md` §6)

- **Date:** 2026-10-28 (executed 2026-10-29 UTC)
- **Task:** [Task #1719](/tasks/1719) (`Origin: task 1549`)
- **Repository:** `ai-on-gke/grain`
- **Branch:** `grain/049f1e09/task-1719` (base `main` at `3ae644f`)
- **Test Plan Reference:** `bwsalmon/grain` `docs/test-plans/2026-10-28.md` §6 — *Walkthrough: release-from-trunk pending-release approval (CLI, Inbox, Alerts)*

---

## 1. Environment

| Property | Value |
|---|---|
| Host OS / Kernel | Debian GNU/Linux 12 (bookworm), `linux/amd64` |
| Go Toolchain | `go1.25.8` |
| Node / Playwright | Node `v20.19.0`, Playwright `1.64.1` (Chromium Headless Shell `151.0.7922.34`) |
| Target Checkout | `/home/debian/work` (`ai-on-gke/grain`, base commit `3ae644f`) |
| Retired Source Reference | `bwsalmon/grain` (inspected via read-only clone at `eb8eb59`) |

---

## 2. Post-Cutover Inventory (`bwsalmon/grain` → `ai-on-gke/grain`)

`bwsalmon/grain`'s GitHub Actions release workflows were disabled at the 2026-10-28 cutover (`d539d92eb`). Before executing the walkthrough, we audited every component listed in §6 of the 2026-10-28 test plan against `ai-on-gke/grain` at `3ae644f`:

| `bwsalmon/grain` Commit | Feature / Component | Path(s) in `ai-on-gke/grain` | Status Post-Move |
|---|---|---|---|
| `07b5ba4b2`, `86834fedb` | Cut and promote from trunk after a staging soak; candidate gate (`select-lkg`, `check-soak`) and release script (`train`, `approve`, `reject`, `--dry-run`) | `scripts/release-from-trunk.sh`, `scripts/release-from-trunk_test.sh`, `scripts/candidate-gate.sh`, `scripts/candidate-gate_test.sh`, `scripts/release-images.sh`, `scripts/release-notes.sh`, `scripts/release.conf` | **Present** |
| `cca9a7b4b`, `7c3129acd` | Train stops at `vX.Y.0-pending` for a person; person-only `promote-release.yml` (`approve`, `promote`, `reject` jobs) and `release-promotion` path trigger (`human: required`, `sensitive: true`) | `.grain/workflows/promote-release.yml`, `.grain/workflows/promote.yml`, `.grain/triggers/release-promotion.yml`, `pkg/trustboundary/trustboundary.go`, `tests/deploy/release_promotion_test.go` | **Present** (updated at cutover to `repo-write:ai-on-gke/grain`) |
| `fab2f44fa` | `grain release approve\|reject [-repo owner/name] vX.Y.0`, `POST /api/repos/{owner}/{name}/pending-releases/{version}/approve\|reject`, Inbox and Alerts controls, seeded pending release in `grain demo` | `pkg/cli/release.go`, `pkg/ui/release_approval.go`, `pkg/ui/release_approval_test.go`, `ui/src/notifications.js`, `ui/src/components/InboxPage.jsx`, `ui/src/components/AlertsButton.jsx`, `pkg/demo/demo.go` | **Present** |
| `dcb2ca8e4` | Scheduled daily train (`.grain/workflows/release-train.yml`, `15 4 * * *`) and `.grain/templates/release-notes.yml` (`auto_merge: true`) | `.grain/workflows/release-train.yml`, `.grain/templates/release-notes.yml`, `pkg/orchestrator/scheduledworkflow.go`, `tests/deploy/grain_workflows_test.go` | **Present** (updated at cutover to `repo-write:ai-on-gke/grain`) |
| `8fffeca72`, `a799e32c9`, `9976d2f55`, `d35119f1c` | `release-gate-read` capability (`GET /api/ci/runs`, `GET /api/persistent-sandboxes`, `GET /api/tasks`, `POST /api/tasks/from-template`, `POST /api/notifications/release-pending`), CI release ref rules, and `EventRelease` promotion trigger | `pkg/capability/ciapi/ciapi.go`, `pkg/capability/ciapi/ciapi_test.go` (absorbed `pkg/capability/releasegate`), `pkg/gitproxy/ciref.go`, `pkg/gitproxy/gitproxy_test.go` | **Present** (`pkg/capability/releasegate` merged into `pkg/capability/ciapi`) |
| `4a79f75be` | Credential-less train stops before tagging (`require_publish_credentials` checks `GRAIN_RELEASE_GATE_URL`/`GRAIN_RELEASE_GATE_TOKEN` and `GHCR_TOKEN`/`GITHUB_TOKEN` before any tag or ref is written) | `scripts/release-from-trunk.sh` (`require_publish_credentials`), `scripts/release-from-trunk_test.sh` (`no gate` cases) | **Present** |
| `c55e04b16` | Qualification runs (`schedule`, `workflow_dispatch`) may push and delete `refs/heads/qualification/*` | `pkg/gitproxy/ciref.go` (`isQualificationBranch`), `.grain/workflows/deep-rc-qualification.yml` | **Present** |

**Summary of Post-Move Inventory:** Nothing was omitted during the repository move; all workflows, scripts, CLI commands, API routes, capability endpoints, ref rules, and UI controls exist in `ai-on-gke/grain`. However, several defaults still reference `bwsalmon/grain` (`model.GrainSourceRepo()`, script fallbacks) or `v2.x` (`scripts/release.conf`), documented in Findings F1–F3 below.

---

## 3. Walkthrough Steps & Observations

### Step 1: `grain demo -github-sim -fake-ci` — Approve & Reject via CLI, Inbox, and Alerts; Check API State; Verify Agent Token Refusal

#### 1.1 Initial Reproduction of F1 (Before Fix)

Running `grain demo -github-sim -fake-ci` seeds three operator notifications in `seedDemoNotifications` (`pkg/demo/demo.go:1537–1549`), including `release.pending:v2.4.0` on `model.GrainSourceRepo()` (`bwsalmon/grain`):

```json
{
  "id": 2,
  "subject": "release.pending:v2.4.0",
  "family": "release.pending",
  "severity": "warning",
  "title": "v2.4.0 is ready to promote (v2.4.0-rc.2 passed its soak)",
  "repo": "bwsalmon/grain",
  "raisedBy": "system:release-gate",
  "actions": ["approve-release", "reject-release"]
}
```

On the unmodified tree, running `grain release approve v2.4.0` (or clicking **Approve** / **Reject** in the Inbox or Alerts bell) immediately failed with HTTP `403 Forbidden`:

```
$ ./bin/grain -token "$CODE" release approve v2.4.0
grain: grain server: grain CI is not enabled for bwsalmon/grain: set its CI Mode to "Use Grain CI" on the repo's page (Settings, Repos), or PUT /api/repos/bwsalmon/grain/ci-mode with {"ciMode": "grain"}
```

- **Why:** `configureFakeCIDemo` (`pkg/demo/demo_fakeci.go:160–170`) only configured `acme/widgets` (leaving `acme/widgets`'s `CIMode` unset so `ui/e2e/release-run-ref-checks-walkthrough.spec.js` can test turning it on) and never set `CIMode: model.CIModeGrain` on `model.GrainSourceRepo()` (`bwsalmon/grain`). Worse, `bwsalmon/grain` is not in the demo's `TargetRepos` (`["acme/widgets"]`), so it does not appear on `/settings/repos` in the UI.
- Furthermore, once `PUT /api/repos/bwsalmon/grain/ci-mode` was called manually, `grain release approve v2.4.0` printed trailing empty parentheses `()` after the job name because `demoFakeCI.Start` left `JobRun.Workflow` empty and `printer.ciStarted` (`pkg/cli/cicmd_print.go:267`) unconditionally formatted `  %s (%s)\n`:
  ```
  approved v2.4.0: promote-release is tagging it and moving latest onto it
  started run fake-ci-1: bwsalmon/grain at main
    .grain/workflows/promote-release.yml:promote ()
  ```

#### 1.2 Verification After Fixing F1

With `configureFakeCIDemo` enabling `CIMode: model.CIModeGrain` on `model.GrainSourceRepo()` (when distinct from the primary demo target repo `acme/widgets`), `demoFakeCI.Start` recording `WorkflowPath: req.Workflow`, and `printer.ciStarted` omitting ` (%s)` when `job.Workflow == ""`:

1. **CLI Approval (`grain release approve v2.4.0`):**
   ```
   $ ./bin/grain -token "$CODE" release approve v2.4.0
   approved v2.4.0: promote-release is tagging it and moving latest onto it
   started run fake-ci-1: bwsalmon/grain at main
     .grain/workflows/promote-release.yml:promote

   follow it with "grain ci watch fake-ci-1", read it with "grain ci show fake-ci-1",
   or cancel it with "grain ci cancel fake-ci-1"
   ```
   - `GET /api/ci/runs` shows `fake-ci-1` recorded: `repo: "bwsalmon/grain"`, `ref: "main"`, `event: "workflow_dispatch"`, `requestedBy: "operator"`, `jobs: [{"jobId": ".grain/workflows/promote-release.yml:promote", "workflowPath": ".grain/workflows/promote-release.yml"}]`.
   - `GET /api/notifications` shows `release.pending:v2.4.0` cleared (`clearedAt` populated, `clearedBy: "human:operator"`).

2. **Inbox Approval (`/inbox`):**
   - Navigating to `/inbox`, clicking **Approve** on the pending release row (`v2.4.0 is ready to promote`), and accepting the confirmation dialog (`Approve v2.4.0? This tags v2.4.0, moves latest onto it and publishes its images.`) calls `POST /api/repos/bwsalmon/grain/pending-releases/v2.4.0/approve` (`202 Accepted`).
   - The notification row immediately disappears from `/inbox` and the Alerts bell, `fake-ci-1` (`promote`) is recorded on `bwsalmon/grain` `main`, and `release.pending:v2.4.0` has `clearedBy: "human:..."`.

3. **Alerts Bell Approval (`/`):**
   - Raising `release.pending:v2.5.0`, opening the Alerts bell (`[data-testid="alerts-button"]`), clicking **Approve** on `v2.5.0 is ready to promote`, and accepting `Approve v2.5.0?` calls `POST /api/repos/bwsalmon/grain/pending-releases/v2.5.0/approve` (`202 Accepted`).
   - The alert row disappears from `[data-testid="alerts-panel"]`, `fake-ci-2` (`promote`) is recorded on `bwsalmon/grain` `main`, and `release.pending:v2.5.0` has `clearedBy: "human:..."`.

4. **Pending-Release Rejection (CLI `grain release reject` & Inbox `Reject` button):**
   - Raising `release.pending:v2.6.0`, clicking **Reject** in `/inbox`, and accepting `Reject v2.6.0? Its release candidate is dropped, and the next release train run cuts the next one.` calls `POST /api/repos/bwsalmon/grain/pending-releases/v2.6.0/reject` (`202 Accepted`).
   - Running `./bin/grain -token "$CODE" release reject v2.4.0` prints:
     ```
     rejected v2.4.0: promote-release is dropping it; the next train run cuts its next rc
     started run fake-ci-2: bwsalmon/grain at main
       .grain/workflows/promote-release.yml:reject
     ```
   - `GET /api/ci/runs` records `.grain/workflows/promote-release.yml:reject` (`event: "workflow_dispatch"`, `repo: "bwsalmon/grain"`, `ref: "main"`), and `release.pending:v2.6.0` is cleared (`clearedBy: "human:..."`).

5. **Agent Token Refusal (`person-only` check):**
   - **Task agent without `api:releases:write`** (`X-Grain-Acting-Task: 1`):
     - `POST /api/repos/bwsalmon/grain/pending-releases/v2.7.0/approve` → `403 Forbidden`:
       `{"error":"task 1 does not hold api:releases:write (or a preset that includes it): attach it on the task page or pass it when proposing the task"}`
   - **Task agent with `api:releases:write` attached** (`POST /api/tasks/1/capabilities` with `{"id":"api:releases:write","attach":true}`, then `X-Grain-Acting-Task: 1`):
     - `POST /api/repos/bwsalmon/grain/pending-releases/v2.7.0/approve` → `403 Forbidden`:
       `{"error":"only a person may approve a release: \"1\" is a agent, and a release is made official by a person, never by an agent or a schedule"}`
     - `POST /api/repos/bwsalmon/grain/pending-releases/v2.7.0/reject` → `403 Forbidden`:
       `{"error":"only a person may reject a release: \"1\" is a agent, and a release is made official by a person, never by an agent or a schedule"}`
   - **`release-gate-read` CI bearer token (`ciapi.Endpoint`):**
     - `POST /api/repos/bwsalmon/grain/pending-releases/v2.7.0/approve` → `403 Forbidden`:
       `{"error":"release-gate-read does not offer POST /api/repos/bwsalmon/grain/pending-releases/v2.7.0/approve: it may read GET /api/ci/runs, GET /api/persistent-sandboxes and GET /api/tasks, file the release-notes template with POST /api/tasks/from-template, and raise a pending release with POST /api/notifications/release-pending"}`
   - No CI runs are started by any of the refused calls.

---

### Step 2: Scratch Git Repository — Run `scripts/release-from-trunk.sh` With No Credentials

In an isolated temporary git repository (`/tmp/tmp.*`) configured with a bare `origin` remote and `scripts/release.conf` (`RELEASE_MAJOR=2`, `RELEASE_FLOOR_MINOR=3`), we ran `scripts/release-from-trunk.sh` with all credential environment variables unset (`env -i PATH="$PATH" HOME="$HOME"`):

1. **Train mode with no soaking RC (needs to cut first RC):**
   ```
   $ env -i PATH="$PATH" HOME="$HOME" bash /home/debian/work/scripts/release-from-trunk.sh
   release-from-trunk: newest promoted release (none); target v2.3.0; its notes are from the beginning of history
   release-from-trunk: no soaking release candidate
   release-from-trunk: cutting v2.3.0 requires the release-gate-read capability (GRAIN_RELEASE_GATE_URL and GRAIN_RELEASE_GATE_TOKEN): attach release-gate-read to this workflow job, or pass --runs-json for an offline dry-run
   exit=1
   ```
2. **Train mode with a soaking RC (`v2.3.0-rc.1` on `refs/heads/candidate`):**
   ```
   $ env -i PATH="$PATH" HOME="$HOME" bash /home/debian/work/scripts/release-from-trunk.sh
   release-from-trunk: newest promoted release (none); target v2.3.0; its notes are from the beginning of history
   release-from-trunk: soaking candidate is v2.3.0-rc.1 (9b793ba)
   release-from-trunk: soaking v2.3.0-rc.1 requires the release-gate-read capability (GRAIN_RELEASE_GATE_URL and GRAIN_RELEASE_GATE_TOKEN): attach release-gate-read to this workflow job, or pass --staging-json for an offline dry-run
   exit=1
   ```
3. **Train mode with offline `--runs-json` provided but no `GHCR_TOKEN`/`GITHUB_TOKEN` (`require_publish_credentials`):**
   ```
   $ env -i PATH="$PATH" HOME="$HOME" bash /home/debian/work/scripts/release-from-trunk.sh --runs-json "$RUNS_JSON"
   release-from-trunk: newest promoted release (none); target v2.3.0; its notes are from the beginning of history
   release-from-trunk: no soaking release candidate
   candidate-gate: 9b793ba qualifies: all 3 required suite(s) green on this commit (go, lint, ui-unit, e2e, guest-e2e, container-build, k8s-sandbox-e2e)
   release-from-trunk: cutting v2.3.0-rc.1 requires GHCR_TOKEN or GITHUB_TOKEN (the package-publish capability) before any tag or branch is pushed: attach package-publish to this workflow job, or pass --dry-run
   exit=1
   ```
4. **Approve / Reject modes without `GRAIN_CI_REQUESTED_BY_KIND=human`:**
   ```
   $ env -i PATH="$PATH" HOME="$HOME" bash /home/debian/work/scripts/release-from-trunk.sh approve v2.3.0
   release-from-trunk: approve v2.3.0 is for a person, not '' (GRAIN_CI_REQUESTED_BY_KIND=''): approve the release in the Inbox or Alerts, or run 'grain release approve v2.3.0'
   exit=1
   ```
5. **Ref Verification (`git show-ref` on both working tree and bare `origin`):**
   - Before and after every credential-less invocation, `git show-ref` on the bare remote and scratch clone contained only `refs/heads/main` (and `refs/heads/candidate` when pre-seeded for case 2). Zero `v2.3.0*` tags or branch updates were written locally or pushed to `origin`.

---

### Step 3: Grain CI Planner Dry-Run, Pending Stage, `release-gate-read`, and `CIRefUpdates` Ref Rules

#### 3.1 Workflow Loading & Planner (`ci.LoadDir` + `ci.BuildPlan`)

All three release workflows in `.grain/workflows/` parse cleanly under `ci.LoadDir` and plan as expected under `ci.BuildPlan`:

| Workflow | Event & Ref | Planned Stages & Jobs | Job Capabilities | `CIRefUpdates` Attached |
|---|---|---|---|---|
| `.grain/workflows/release-train.yml` (`Release Train`) | `schedule` (`15 4 * * *`) or `workflow_dispatch` on `main` | 1 stage: `[train]` (`cache: false`) | `repo-write:ai-on-gke/grain`, `release-gate-read`, `package-publish`; optional `[image-signing]` | Yes (`CIRefUpdates(event)`) |
| `.grain/workflows/release-train.yml` | `push`, `pull_request`, `release` on `main` | 0 stages (not matched) | — | — |
| `.grain/workflows/promote-release.yml` (`Promote Pending Release`) | `workflow_dispatch` on `main`, `Jobs: ["promote"]` | 2 stages: `[approve]` → `[promote]` (`cache: false`) | `approve`: `repo-write:ai-on-gke/grain`, `package-publish`; `promote`: `release-publish`, `package-publish`, optional `[image-signing]` | Yes (`CIRefUpdates(workflow_dispatch)`) |
| `.grain/workflows/promote-release.yml` | `workflow_dispatch` on `main`, `Jobs: ["reject"]` | 1 stage: `[reject]` (`cache: false`) | `reject`: `repo-write:ai-on-gke/grain`, `package-publish` | Yes (`CIRefUpdates(workflow_dispatch)`) |
| `.grain/workflows/promote-release.yml` | `push`, `pull_request`, `schedule`, `release` on `main` or `candidate` | 0 stages (dispatch-only) | — | — |
| `.grain/workflows/promote.yml` (`Promote Release`) | `release` on `v*.*.*` | 1 stage: `[promote]` | `release-publish`, `package-publish`, optional `[image-signing]` | Yes (`CIRefUpdates(release)`) |

#### 3.2 `daemon.ResolveCICapabilities` & `release-gate-read`

Testing `daemon.ResolveCICapabilities` on `.grain/workflows/release-train.yml:train`:
- On `bwsalmon/grain` (`model.GrainSourceRepo()`):
  - `EventWorkflowDispatch` or `EventSchedule` with `TrustedSchedule: true` (grain's own `orchestrator/scheduledworkflow.go` reconciler) → grants `repo-write:bwsalmon/grain`, `release-gate-read`, and `package-publish` (`held: nil`, `err: nil`).
  - `EventSchedule` with `TrustedSchedule: false` (an untrusted external `POST /api/ci/runs` with `event: "schedule"`) → grants `repo-write:bwsalmon/grain` and `release-gate-read`, and withholds `package-publish` in `held: ["package-publish"]`.
- On `ai-on-gke/grain` (Finding F2):
  - Because `model.GrainSourceRepo()` (`pkg/model/task.go:807`) still returns `bwsalmon/grain` and `ciapi.NewReleaseGateRead(nil)` defaults its allowed repo list to `[]model.RepoRef{model.GrainSourceRepo()}`, `daemon.ResolveCICapabilities` refuses `release-train.yml:train` on `ai-on-gke/grain` with:
    `release-gate-read is not offered to ai-on-gke/grain's jobs: it reads deployment-wide state, and grain's API is offered to CI for bwsalmon/grain only`

#### 3.3 `gitproxy.CIRefUpdates` Ref Rules

Verified `gitproxy.CIRefUpdates(event).Check(ref, oldSHA, newSHA)` across all CI events (`pkg/gitproxy/ciref.go`, `go test -v ./pkg/gitproxy -run TestCIRepoWrite`):

| Target Ref | `push` / `pull_request` | `schedule` / `workflow_dispatch` | `release` |
|---|---|---|---|
| `refs/heads/main` | Refused | Refused | Refused |
| `refs/heads/candidate` (create / fast-forward / force-move) | Refused | **Allowed** | **Allowed** |
| `refs/heads/candidate` (delete `0000...`) | Refused | Refused (`deleting a release branch is not a release operation`) | Refused |
| `refs/heads/latest` (create / move) | Refused | **Allowed** | **Allowed** |
| `refs/heads/latest` (delete `0000...`) | Refused | Refused | Refused |
| `refs/heads/qualification/*` (create / update / delete) | Refused | **Allowed** (`isQualificationBranch`) | Refused |
| `refs/tags/vX.Y.Z-rc.N` (create) | Refused | **Allowed** | Refused |
| `refs/tags/vX.Y.Z-rc.N` (move or delete) | Refused (`already exists; release tags are not moved or deleted`) | Refused | Refused |
| `refs/tags/vX.Y.Z-rc.N-discarded[-reason]` (create) | Refused | **Allowed** | Refused |
| `refs/tags/vX.Y.0-pending` (create **and** delete) | Refused | **Allowed** (`isPendingReleaseTag` allows deletion once approved or rejected) | **Allowed** |
| `refs/tags/vX.Y.0` (official release tag create) | Refused | **Allowed** (`workflow_dispatch` only; refused on `schedule`) | **Allowed** |
| `refs/tags/vX.Y.0` (move or delete) | Refused | Refused | Refused |

#### 3.4 End-to-End Pending-Release Stage & Shell Suites

Ran the full shell and deployment test suites (`go test -v ./tests/deploy -run 'TestReleaseTrain|TestPromoteRelease|TestTheRelease|TestDeepRCQualification'` and `go test -v ./pkg/capability/ciapi`):
- `scripts/candidate-gate_test.sh`: **34/34 passed** (`select-lkg` and `check-soak`).
- `scripts/release-from-trunk_test.sh`: **98/98 passed** (first cut, soaking, `never-promote`, placeholder minor skip, soak passes → tags `v2.3.0-pending`, files `release-notes` template via `POST /api/tasks/from-template`, raises `release.pending:v2.3.0` via `POST /api/notifications/release-pending`, cuts `v2.4.0-rc.1` on `candidate`, leaves `latest` alone; next day waits on pending `v2.3.0`; `approve` by agent refused; `approve` by human tags `v2.3.0`, moves `latest`, deletes `v2.3.0-pending`, retags `:v2.3.0-pending` images; `reject` by human deletes `v2.3.0-pending`, marks `rc.1` discarded, drops `v2.4.0-rc.1`, and next train run cuts `v2.3.0-rc.2`; `--dry-run` pushes no tag and moves no branch).

---

## 4. Findings

### Finding F1 (Fixed in this PR): Seeded pending release in `grain demo -github-sim -fake-ci` fails with HTTP 403 out of the box, and `grain release approve|reject` prints empty workflow parentheses `()`

- **Severity:** Medium (breaks §6 Step 1 walkthrough out of the box on `grain demo -github-sim -fake-ci`).
- **Root Cause:**
  1. `seedDemoNotifications` (`pkg/demo/demo.go:1537–1549`) seeds `release.pending:v2.4.0` on `model.GrainSourceRepo()` (`bwsalmon/grain`), whereas `configureFakeCIDemo` (`pkg/demo/demo_fakeci.go:160–170`) only configured `acme/widgets`. Because `bwsalmon/grain` had no `RepoConfig` row with `CIMode: model.CIModeGrain` (and is not in `TargetRepos`), `DecideRelease` (`pkg/ui/release_approval.go:89–95`) rejected every CLI, Inbox, and Alerts approval or rejection with `403 Forbidden: grain CI is not enabled for bwsalmon/grain`.
  2. `demoFakeCI.Start` (`pkg/demo/demo_fakeci.go:83–85`) left `WorkflowPath` and `Workflow` empty on created `ci.JobRun`s, and `printer.ciStarted` (`pkg/cli/cicmd_print.go:266–268`) printed `  %s (%s)\n` even when `job.Workflow == ""`, producing trailing ` ()`.
- **Fix (in this PR):**
  - `pkg/demo/demo_fakeci.go`: In `configureFakeCIDemo`, set `CIMode: model.CIModeGrain` on `model.GrainSourceRepo()` when `!repo.Same(grain)` (keeping `acme/widgets`'s `CIMode` unset for `ui/e2e/release-run-ref-checks-walkthrough.spec.js`), and populate `WorkflowPath: req.Workflow` on `demoFakeCI`'s `JobRun`s.
  - `pkg/cli/cicmd_print.go`: Omit ` (%s)` in `printer.ciStarted` when `job.Workflow == ""`.
  - Added unit test `TestFakeCIDemoPendingReleaseApproval` in `pkg/demo/demo_test.go` and Playwright E2E spec `ui/e2e/release-from-trunk-approval-walkthrough.spec.js`.

### Finding F2 (Proposed as Follow-Up Task via `propose_task`): Post-cutover `bwsalmon/grain` defaults in `model.GrainSourceRepo()`, `scripts/release-*.sh`, `pkg/cli/release.go`, and `ui/src/notifications.js`

- **Severity:** Medium (blocks `release-gate-read` capability resolution on `ai-on-gke/grain`).
- **Root Cause:**
  - `.grain/workflows/release-train.yml` and `.grain/workflows/promote-release.yml` were updated at cutover to `repo-write:ai-on-gke/grain`, but `model.GrainSourceRepo()` (`pkg/model/task.go:807`) still returns `bwsalmon/grain`.
  - `pkg/capability/ciapi/ciapi.go:331–333` (`NewReleaseGateRead`) defaults `release-gate-read`'s allowed repos to `[]model.RepoRef{model.GrainSourceRepo()}`, causing `daemon.ResolveCICapabilities` to refuse `release-train.yml:train` on `ai-on-gke/grain` with:
    `release-gate-read is not offered to ai-on-gke/grain's jobs: it reads deployment-wide state, and grain's API is offered to CI for bwsalmon/grain only`
  - Default repo fallbacks in `scripts/release-from-trunk.sh:521`, `scripts/release-notes.sh:58`, `scripts/release-images.sh:407`, `pkg/cli/release.go:34`, and `ui/src/notifications.js:111` also still name `bwsalmon/grain`.
- **Disposition:** Because `scripts/release-from-trunk.sh` and `scripts/release-images.sh` are on `.grain/triggers/release-promotion.yml` (`human: required`) and `pkg/capability/` is on the `capabilities-secrets` trust boundary, filed as a follow-up task via `propose_task` (`Update GrainSourceRepo and release-from-trunk defaults from bwsalmon/grain to ai-on-gke/grain`).

### Finding F3 (Proposed as Follow-Up Task via `propose_task`): `scripts/release-from-trunk.sh` moves major version backwards from `v3.0.0` to `v2.3.0` on `ai-on-gke/grain`

- **Severity:** Medium (would cut `v2.3.0-rc.1` in a repository that already has `v3.0.0`).
- **Root Cause:**
  - `ai-on-gke/grain` carries annotated tag `v3.0.0` on `main`, while `scripts/release.conf` still sets `RELEASE_MAJOR=2` and `RELEASE_FLOOR_MINOR=3`.
  - Unlike `scripts/build-version.sh:171–182` (which compares `want_major` against the highest `v*` tag's `major` and never moves backwards below `v3.0.0`), `scripts/release-from-trunk.sh:179–196` (`compute_target_version`) unconditionally uses `RELEASE_MAJOR` from `scripts/release.conf`, outputting:
    `release-from-trunk: newest promoted release v3.0.0; target v2.3.0; its notes are from v3.0.0`
- **Disposition:** Because `scripts/release-from-trunk.sh` is guarded by `.grain/triggers/release-promotion.yml` (`human: required`), filed as a follow-up task via `propose_task` (`Keep scripts/release-from-trunk.sh from moving major version backwards below highest v* release tag`).

---

## 5. Verification & Outcome

- **Go Unit & Integration Tests:**
  - `go test -v ./pkg/demo -run 'TestFakeCIDemoPendingReleaseApproval|TestSeedDemo'` — PASS
  - `go test ./pkg/cli ./pkg/ui ./pkg/gitproxy ./pkg/capability/ciapi` — PASS
  - `go test -v ./tests/deploy -run 'TestReleaseTrain|TestPromoteRelease|TestTheRelease|TestDeepRCQualification'` — PASS (including 34/34 in `scripts/candidate-gate_test.sh` and 98/98 in `scripts/release-from-trunk_test.sh`)
- **Playwright E2E Tests:**
  - `npx playwright test --project=chromium e2e/release-from-trunk-approval-walkthrough.spec.js e2e/inbox-notifications-layout.spec.js e2e/release-run-ref-checks-walkthrough.spec.js e2e/ci-retry.spec.js e2e/grain_ci_grantable_walkthrough.spec.js e2e/merge-queue-lanes-walkthrough.spec.js e2e/port-settings-walkthrough.spec.js` — 10/10 PASS
- **Outcome:** Walkthrough complete; F1 fixed with unit and Playwright E2E regression tests; F2 and F3 proposed as follow-up tasks.
