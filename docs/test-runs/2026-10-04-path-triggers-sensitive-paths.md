# Path triggers, sensitive paths and the design-doc planner: walkthrough (2026-10-04)

Executes §3 of `docs/test-plans/2026-10-04.md`. Task 1829, branch `grain/task-1829`.

## Environment

- **Base.** `candidate` at `5ab9aa1ae` (Merge pull request #2850).
- **Commits under test.** All three are on `candidate`, confirmed with `git branch -r --contains`.
  Each is listed in `origin/candidate`, `origin/main` and both merge-queue branches.
  - `dec6c7202`: path triggers.
  - `84d05077c`: sensitive paths and named approvers.
  - `0152c4cea`: design-doc planner.
- **Sandbox.** Debian sandbox, Go toolchain from the image, Node/npm from the image.
- **Playwright.** Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **UI.** Built with `cd ui && npm ci && npm run build` into `pkg/ui/static`, then rebuilt after the
  `RepoPage.jsx` fix.
- **Go harness.** `tests/e2e`, using `holdWorld` from `security_hold_walkthrough_test.go`:
  - a real orchestrator: `orchestrator.RunCycle`, covering dispatch, finish, the merge queue,
    `reconcileDeclarations` and `reconcilePathTriggers`;
  - a real git server over a bare repository, plus githubsim;
  - a SQLite store.
- **Declarations in the harness.** These are read by the real declaration sync. A
  `DeclarationSource` (`bareDeclarations`) clones the git server's default branch and runs
  `pkg/repodecl.Load` on it, which is `cmd/grain`'s `repoDeclarationSource` without the credential
  ladder. Nothing is put in the store by hand.
- **Browser.** The real `grain demo` server, one per Playwright worker (`ui/e2e/fixtures.js`),
  started with the new opt-in `-sensitive-hold` seed. Users are switched with the `X-Grain-User`
  header, which demo believes.

## Stubs

| Boundary | Stand-in | Where |
|---|---|---|
| GitHub REST (PRs, files, comments, compare, merge) | `githubsim` behind `syncedSim` | `tests/e2e` |
| Model vendor (agent runs) | scripted `antigravity` runs: the subject task clones, edits, commits and pushes; reviews answer "no findings" | `tests/e2e` |
| Clone credentials for the declaration read | none needed: `bareDeclarations` clones the local bare repository directly | `tests/e2e/path_triggers_walkthrough_test.go` |
| Orchestrator-written hold state, in the browser walk only | `grain demo -sensitive-hold` (`cmd/grain/demo_sensitive.go`) writes the links the harness walk asserts the orchestrator writes: `path-trigger-hold`, `path-trigger-task` and `approval-required`. It also writes a failed declaration read via `Store.RecordDeclarationFailure`. | `ui/e2e` |
| Sign-in | `X-Grain-User` header (demo's trusted header) | `ui/e2e` |

Grain's own APIs and React were not mocked or intercepted. Every browser request was answered by
the demo's real handlers.

## Commands

```sh
go run ./cmd/grain declarations check
go test ./tests/e2e -run 'TestPathTriggersWalkthrough|TestSecurityHold' -count=3
go test ./pkg/orchestrator ./pkg/ui ./cmd/grain -count=1
cd ui && npx vitest run src/components/RepoPage.test.jsx
cd ui && npm run build && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  npx playwright test e2e/path-triggers-sensitive-hold-walkthrough.spec.js
gofmt -l cmd pkg tests; (cd ui && npm run format); make security
```

The new tests are `tests/e2e/path_triggers_walkthrough_test.go` (`TestPathTriggersWalkthrough`, five
subtests) and `ui/e2e/path-triggers-sensitive-hold-walkthrough.spec.js` (two tests).

## Outcomes, by procedure step

1. **Gated sensitive PR (harness): pass.**
   - `.grain/triggers/payments.yml` is pushed to `main`:
     `on: pull_request, paths: [src/payments/**], gate: true, sensitive: true,
     approvers: [team:payments, alice], human: required`.
   - The declaration sync stores it.
   - A task with auto-merge on edits `src/payments/x.go`. After its run:
     - it is `awaiting_submit` with auto-merge withdrawn;
     - `path-trigger-hold=required` is recorded;
     - one `path-trigger-task` link points to a review stacked on the task's branch. It is grain's
       security review, because no template was named, and it names `src/payments/x.go`;
     - there is exactly one approval-required rule:
       `trigger:payments=team:payments,alice`.
   - The change does not merge over three cycles while its PR reads clean.

2. **Who may release.**
   - **Harness, through `ui.Client`: pass.**
     - bob is a member of the task's team. His Submit returns `ForbiddenError` with the text "bob is
       not one of the people who may release it… `trigger:payments`: a member of team payments or
       alice".
     - bob's `UpdateTask{autoMerge:true}` returns `ForbiddenError`.
     - alice's Submit releases the change and records `approved-by trigger:payments=alice`.
     - A push to the branch by someone outside the run is treated as a new head. Two cycles later:
       auto-merge is off, the approval is gone, `trigger:payments` is pending again, and nothing has
       merged.
     - The plan lists "an approver given as `team:x` where x is not the held task's team" as a gap.
       carol, who is in team `payments` and not in the task's team, then submits, and that releases
       it. Pass.
   - **Browser: pass.**
     - As bob, the task pane shows "Held by this repository's gating triggers (human: required).",
       the `.grain/triggers/payments.yml` review link, and "trigger:payments
       (.grain/triggers/payments.yml): waiting for a member of team payments or alice".
     - Clicking Submit gets a 403. The page shows the server's message, which names the approvers.
     - Ticking "Auto-merge once checks pass" gets a 403. The page shows "…auto-merge cannot be turned
       on by editing it". After a reload the box is still unticked.
     - As alice, Submit returns 200, the box is ticked and the hold alert is gone. The API reports
       `approvedBy: alice`.
     - "New head clears approvals" needs a git server and is covered in the harness, not the browser.

3. **Burst and restart (harness): pass, with one note (N1).**
   - The `docs` merge trigger is `paths: [docs/x/**]`, `per: folder`, with a repository template.
   - Three tasks touch `docs/x/a/one.md`, `docs/x/a/two.md` and `docs/x/b/one.md`. All three run to
     open PRs first. Once their PRs read clean, they merge through the real merge queue.
   - Result: two open triggered tasks, one each for `docs/x/a` and `docs/x/b`.
     - Each task's `path-trigger-from` is the head of `main` before that folder's first change
       landed.
     - The second `docs/x/a` firing superseded the first. The surviving task lists both files and
       keeps the earliest `from`.
   - Restart: a new store handle and Deps over the same SQLite directory, then a cycle five minutes
     later. Nothing new was filed and the cursor did not move.
   - A fourth change, `docs/x/a/three.md` pushed to `main`, produced a task that `supersedes`
     `docs/x/a`'s task. It keeps the same earliest `from` and lists all three files. `docs/x/b`'s
     task was untouched.

4. **A PR editing its own trigger (harness): pass after fix F1.**
   - The PR rewrites `.grain/triggers/payments.yml` without `gate` and also touches
     `src/payments/x.go`.
   - The rule in force is still the base's (`Gate` true in the store), and the change is held.
   - The trust-boundary security review names `path-triggers`, and so does the hold note on the
     task.
   - Pending approvals are `builtin:path-triggers=team-admins:default,system-admins` and
     `trigger:payments=team:payments,alice`.
   - alice's Submit approves `trigger:payments` only, so the change stays held.
   - bob's Submit is refused with a 403.
   - `sysadmin`, the system admin, submits and that releases it.
   - Before F1, the gate recorded no `path-trigger-hold` and posted "It was not merging
     automatically anyway, so nothing was withdrawn".

5. **Malformed trigger.**
   - **Harness: pass.**
     - `.grain/triggers/broken.yml` (`paths: [docs/**`) is pushed to `main`. The sync fails and
       records "reading acme/widgets's declarations: .grain/triggers/broken.yml: yaml: line 1: did
       not find expected ',' or ']'".
     - The recorded commit stays at the last good one, and only the good `docs` trigger is stored.
     - A later merge touching `docs/x/a/one.md` still files that trigger's task.
   - **Browser: pass after fix F2.** The repository page (`/repos/acme/widgets`, Declarations)
     shows: "Last read failed: reading acme/widgets's declarations: .grain/triggers/broken.yml: yaml:
     line 2: did not find expected ',' or ']' — still running what 0f1e2d3c4b5a declared."

6. **Design-doc planner.**
   - `grain declarations check` on this repository is well-formed. It lists `trigger design-docs
     .grain/triggers/design-docs.yml`:
     - on merge, paths `docs/design/**`;
     - ignore `**/plan.yml, **/*.plan.yml`;
     - files `repo:template:bwsalmon:grain:design-planner`, one task per folder;
     - each task waits for approval.

     Pass.
   - **Harness: pass.** This repository's own `.grain/triggers/design-docs.yml` and
     `.grain/templates/design-planner.yml` are copied onto the harness repository's `main`, and the
     sync reads them cleanly.
     - One merge touches `docs/design/sample/README.md` and `docs/design/other/README.md`. It files
       exactly two tasks, "Plan design doc changes: docs/design/sample" and "…: docs/design/other".
       Both bodies name the `from..to` range, and neither title nor body contains `{{`.
     - A merge touching only `docs/design/sample/plan.yml` files nothing.

## Findings

### F1: a gated trigger's hold is not recorded when a human-only trust-boundary hold already stopped the change (fixed)

- **Reproduction.** Deterministic: `go test ./pkg/orchestrator -run
  TestAPullRequestEditingItsTriggerIsStillJudgedByTheBasesCopy`, or the e2e subtest "a pull request
  editing the trigger…".
  - A change has auto-merge on and touches both a gating trigger's paths and `.grain/triggers/**`.
  - `finishWithPullRequest` runs `holdForTrustBoundary` first. `path-triggers` requires a human,
    so the hold withdraws auto-merge and records no releasable `security-hold` link.
  - `applyPathTriggerGate` then sees a change with auto-merge off and no hold of either kind. It
    records no `path-trigger-hold`, so the task API's `pathTriggerHold` is empty.
  - It posts on the task and the PR: "It was not merging automatically anyway, so nothing was
    withdrawn: it waits for you as before." That is false: the change was merging itself until
    that moment.
  - Without the fix, the regression assertion fails with that note.
- **Fix.** `pkg/orchestrator/pathtriggerhold.go`: `judgePullRequestTriggers` passes the caller's copy
  of auto-merge to `applyPathTriggerGate`. That copy is read before this head's trust-boundary
  classification. The gate treats a change that was merging itself before that classification as
  one it holds.
- **Regression test.** `TestAPullRequestEditingItsTriggerIsStillJudgedByTheBasesCopy` now asserts
  that the `path-trigger-hold` link is recorded and that the note does not say "not merging
  automatically anyway". The e2e walkthrough asserts the same for a sensitive trigger.

### F2: the repository page leaves path triggers out of the declarations status (fixed)

- **Reproduction.** Deterministic.
  - `GET /api/repos/{owner}/{name}/declarations` for a repository whose only declaration is a
    trigger returns `declarationsStatus` with no trigger field.
  - The page reads "Read at …: 0 template(s), 0 suite(s), 0 schedule(s)". That is
    indistinguishable from a repository that declares nothing. bwsalmon/grain is in this state:
    its `.grain/triggers/design-docs.yml` would not show up.
  - When the read fails, the page says only "Last read failed: …". It does not say that the last
    good commit's rules keep running, which is the behaviour step 5 asks a person to be able to
    see.
- **Fix.**
  - `pkg/ui/repos.go`: `RepoDeclarationsStatus.Triggers` is filled from `Store.PathTriggersOf`.
  - `ui/src/components/RepoPage.jsx`: counts `trigger(s)`, adds "— still running what <commit>
    declared." to a failed read, and names `.grain/triggers` in the switch's copy.
- **Regression tests.**
  - `pkg/ui` `TestRepoSettingsCarryTheDeclarationsSwitchAndItsStatus` asserts `Triggers`.
  - Two `RepoPage.test.jsx` cases cover the count and the failed-read sentence.
  - The Playwright spec asserts the sentence on the real server.

### N1: the real merge queue lands queued changes a cycle apart (observation, not a bug)

Three PRs that all read clean at once merged in three separate cycles, giving three commits on
`main`. So a merge burst "within one cycle" does not reach `reconcilePathTriggers` as one diff
through the real queue. Coalescing still happens through supersession: an undispatched task for the
same key is superseded by the next one and keeps the earliest `from`. The walkthrough asserts that
path. The single-diff path is covered by `pkg/orchestrator/pathtrigger_test.go`.

### N2: harness note on team roles (not a product finding)

The harness's `UpdateTeam(AddUser…)` on the default team gave `alice` the admin role, though
`AddUser` adds members. That made her a `team-admins:default` approver and hid the step 4 check
until the walkthrough named an explicit admin (`owner`) first. The default team in a fresh store
apparently promotes a member when it has no admin. The test now asserts that alice and bob are
plain members before it relies on that. It is worth knowing for anyone writing an approver test.
It is not filed: it is team bootstrap behaviour, not part of §3.

## Fixes in this change

- `fix(orchestrator)`: F1, plus `tests/e2e/path_triggers_walkthrough_test.go`. `holdWorld` gains a
  `dbDir` field so a restart can reopen the same store.
- `fix(ui)`: F2.
- `test(ui)`: `grain demo -sensitive-hold` and `ui/e2e/path-triggers-sensitive-hold-walkthrough.spec.js`.

F1 touches `pkg/orchestrator`, a trust-boundary path, so this change is expected to be held for
security review before it merges.

## Formatting and security

- `gofmt -l cmd pkg tests` is clean, and `npm run format` left nothing to change.
- `make security` passed: `go vet ./...` and `go test -race ./tests/security/...` (111s). No
  `.grain/baselines/` file was touched.

## CI verdict

The full grain CI suite passed on `048bb9b`, the code-complete commit of this change: run
`55143774f736d4c6`. Every job was green: frontend, go, installer, lint, package cache, postgres,
terraform and ui e2e. The commit that adds this paragraph changes only this report. Its own
full-suite run is the merge gate.
