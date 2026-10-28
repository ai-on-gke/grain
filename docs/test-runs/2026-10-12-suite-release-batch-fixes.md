# 2026-10-12 suite run with a deleted template, default-branch release, merge batch reset

grain/task-3290 (filed by task 3209). The walkthrough in
[docs/test-plans/2026-10-12.md §5](../test-plans/2026-10-12.md) against `candidate`, covering:

- `e59b9acb4`: a suite run whose template is deleted mid-run fails instead of looping.
- `b38b500cb`: a release whose prod branch is the default branch is refused at provisioning and at
  promotion.
- `f1346e4f7`: a merge batch size of 0 means grain's default.

All three commits are on `origin/candidate` (`git branch -r --contains`). The two orchestrator fixes
behave as described. The settings fix holds at the API, but the pane still showed `3` after a clear
and reload; that is fixed on this branch. A related release hazard the plan pointed at, a release
named after an existing *non-default* branch, is reproduced and filed.

## Environment

| | |
| --- | --- |
| Base walked | `candidate` at `c8a3f5e0c2f58abf31ebd8af1bf855a22842a897` |
| Fix verified at | this branch, `grain/eu/task-3290` |
| Go / Node / npm / git | go1.26.6 linux/amd64 / v20.19.0 / 10.8.2 / 2.39.5 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Frontend | `cd ui && npm ci && npm run build` (into `pkg/ui/static`) |
| Server | `grain demo -addr 127.0.0.1:0 -open=false -github-sim`, built from the tree, on its embedded SQLite store in a throwaway `TMPDIR`. Exploration used a hand-started one (`go build -o /tmp/grain ./cmd/grain`). The committed spec gets its own per worker through `ui/e2e/fixtures.js` (`test.use({ demoArgs: ["-github-sim"] })`). |
| Readiness | `GET /api/sandboxes` → 200 (there is no `/api/health`) |
| Identity | `localStorage["grain.username"] = "alice"` and `X-Grain-Admin-Mode: on`, as the suite's own fixtures do |
| Model / agents | None. The demo runs no agents. |

## Stubs (external boundaries only)

`grain demo` runs **no reconcile loop** and **no agents** (`pkg/demo` never calls
`orchestrator.SyncSuites` or `SyncReleases`). Nothing in steps 1 and 2 moves unless something stands
in for them. `ui/e2e/releasehelper`, the release walkthroughs' existing helper, gains commands that
stand in for exactly those two, and nothing of grain's own is mocked:

| Stub | Stands in for | What it does |
| --- | --- | --- |
| `releasehelper complete-suite-pass` | an agent run finishing each task of a pass | `Store.ObserveField(..., CompletedAt = now)` for each task in the newest active run's current pass |
| `releasehelper sync-suites` | one reconcile cycle's suite step | calls the real `orchestrator.SyncSuites`, and logs a returned error as `reconcile: suites: …` on stderr, which is the "daemon log" this walkthrough greps |
| `releasehelper sync-releases` | one reconcile cycle's release step | calls the real `orchestrator.SyncReleases` with a `github.Client` over a `githubsim.Sim` built on the demo's own bare repo (`<demo dir>/githubsim/acme/widgets.git`), the same repo the demo's `-github-sim` serves the panes from |
| `releasehelper provision-legacy` | a release provisioned before `b38b500cb` | creates `<name>.latest` and `<name>.rc.1` in the bare repo at a new commit on top of `main`, then `MarkReleaseProvisioned` and `MarkCandidateCut` |
| `releasehelper branch-sha <b>` | reading GitHub's branch tip | `git rev-parse refs/heads/<b>` in the simulator's bare repo |
| GitHub | GitHub | `-github-sim` (`githubsim.Sim` over a real bare repo seeded with `main` and `release/1.0`) |

## Steps and results

The committed spec is `ui/e2e/suite-release-batch-walkthrough.spec.js`; all three tests pass on
this branch (`npx playwright test e2e/suite-release-batch-walkthrough.spec.js --project=chromium`,
3 passed in 19s).

### 1. Suite run whose template is deleted after pass 1

1. `/templates` → **+ New template**: `walk-gone-<stamp>`, repo `acme/widgets`, base `main` → **Add
   template**.
2. `/suites` → **+ New suite**, **Run mode** → *Run a fixed number of times*, count 2, **Templates**
   → the new template → **Add suite**. The row reads `run 2×`.
3. Row **Run…** → repo `acme/widgets`, base `main` → **Run**. The runs list row reads `Active`,
   `pass 1`.
4. `complete-suite-pass` completes pass 1's only task.
5. Through the UI, the suite row → **Delete** (confirm accepted), then the template row → **Delete**.
   Both go. Neither refuses, since a run that already started is not a reference.
6. `sync-suites` twice (two reconcile cycles).

| Check | Result |
| --- | --- |
| Runs list row after cycle 1 | **Failed**, `pass 2 could not be filed: template template-3 no longer exists` |
| Run pane (`/suites/runs/<id>`) | **Failed** chip and the same reason |
| `GET /api/suite-runs/<id>` | `status: "failed"`, `error` as above |
| "Daemon log" (stderr of both cycles) | no `no longer exists` line in either. The run is no longer active after cycle 1, so cycle 2 never reads it. |

**Pre-fix reproduction (deterministic).** Reverse-apply `e59b9acb4`'s `pkg/orchestrator/suites.go`
hunk (`git show e59b9acb4 -- pkg/orchestrator/suites.go | git apply -R`) and rerun the spec. The
first cycle logs `reconcile: suites: orchestrator: advancing suite run 1: template template-3 no
longer exists`, and so does every later cycle. The run stays Active.

### 2. Release named after the default branch

1. `/repos/acme/widgets/releases`. `branch-sha main` is recorded (`d6b011d…` in the hand run; the
   spec compares against whatever its own demo seeded).
2. **New release name** `main` → **Create release**. The pane shows `latest: main.latest, prod:
   main`. Creation is accepted; the refusal is made at provisioning, as designed.
3. `sync-releases` twice. The release line reads `(release "main" would use the default branch
   "main" as its prod branch, and promoting it would force-move "main" to the candidate; nothing is
   done for it -- delete it and create the release under another name)`. `GET …/releases/main` gives
   `status: provisioning` and the same `error`. No `reconcile: releases` error is logged, and
   `main.latest` is never created. `main`'s SHA is unchanged.
4. **Promotion attempt** on a release provisioned before the check: `provision-legacy` puts
   `main.latest` and `main.rc.1` at a commit `main` doesn't have. On reload **Promote current RC** is
   enabled. Clicking it (confirm accepted) shows `main.rc.1 -- promoting`. `sync-releases` twice.
   `main` is still at the recorded SHA and `main.rc.1` is still at its own different commit. After a
   reload the current candidate reads `main.rc.1 -- promoting` with the same refusal as its error.
5. **Delete release** (confirm accepted) → it leaves the pane, and `GET …/releases/main` → 404.
   **Rename** is not offered anywhere in the pane. A new name is a new release: `walk-2` is created,
   and one sync provisions `walk-2.latest` at `main`'s tip. `main` is still unchanged.

**Pre-fix reproduction.** Reverse-apply `b38b500cb`'s `pkg/orchestrator/releases.go` hunks and
rerun the spec. The refusal never appears: the release provisions normally. The force-move of `main`
on promotion itself is reproduced by `b38b500cb`'s own `releases_test.go` against the simulator.

### 3. Merge batch size (System → Engine & Concurrency)

The box saves on blur or Enter, which is the pane's "save".

| Action | PUT | `GET /api/settings` `mergeBatchSize` | Box after reload, on `candidate` | On this branch |
| --- | --- | --- | --- | --- |
| type `5`, Enter | 200 `{"mergeBatchSize":5}` | 5 | `5` | `5` |
| clear, Enter | 200 `{"mergeBatchSize":0}` | 3 | **`3`** | blank, placeholder `3` |
| `-1`, Enter (from 5) | 400 `mergeBatchSize must be at least 1 (1 turns batching off), or 0 for grain's default`, shown as an alert | 5 | `5` | `5` |
| `abc` via the API | 400 `json: cannot unmarshal string into Go struct field UpdateSettingsRequest.mergeBatchSize of type int` | 5 | -- | -- |
| `abc` typed into the box (Chromium) | see finding 3 | | | |

## Findings

1. **Fixed: a cleared merge batch size read `3` again after a reload.** `GET /api/settings` answers
   `mergeBatchSize` as the effective size (`MergeBatchSizeOr`), and the box was filled from it. So
   once cleared, the box came back holding the default as though it had been typed, and the helper
   text's "Blank uses grain's default" never held. Reproduction: on `candidate`, set 5, clear, press
   Enter, reload `/admin/engine`, and the box reads `3`. The spec's third test fails there with
   `Expected: "" Received: "3"`.
   *Fix (`21624079a`):* `Settings` also carries `mergeBatchSizeChosen`, the stored size (0 when none
   is chosen). The box shows that, with the effective size as its placeholder, and `onIntBlur` reads
   it (a new `readFrom` option) but still sends `mergeBatchSize`. `mergeBatchSize` itself is
   unchanged, so `GET` still gives 3 for a cleared size. Regression tests:
   `TestSettingsTakeAZeroMergeBatchSizeAsTheDefault` (`pkg/ui/merge_batching_test.go`) now checks
   `mergeBatchSizeChosen` after setting 7 and after clearing; the e2e spec's third test checks the
   box after the reload.
2. **Filed: a release named after an existing non-default branch force-moves that branch on
   promotion.** `b38b500cb` guards only the default branch. Reproduction against the hand-started
   `-github-sim` demo:
   1. In the bare repo, create `stable` at a commit `3901efe` (a child of `main`) that no other
      branch has.
   2. `POST /api/repos/acme/widgets/releases {"name":"stable"}` → 201, then `sync-releases`. The
      release goes active with no error.
   3. `POST …/releases/stable/candidates/promote` → 200, then `sync-releases`. `stable.rc.1` is
      promoted with no error.
   4. `stable` now points at `main`'s tip (`d6b011d`), and `git branch --contains 3901efe` lists
      nothing: the commit is gone.

   Release names can't contain `/`, so `release/1.0` (the plan's example) is refused as a name. But
   `candidate`, `stable` or `prod` are all valid names, and `candidate` is a live branch in grain's
   own repo. A fix has to choose between refusing a release whose prod (or latest) branch exists at
   provisioning and the cases where that branch legitimately exists: a reused release name, or a
   retried provisioning. That is a judgement call, so it is filed as a follow-up rather than fixed
   here.
3. **Not a bug, noted: `abc` cannot be typed into the box in Chromium.** The box is
   `type="number"`. With its text selected, typing `abc` leaves it visibly empty (`value ""`,
   `validity.badInput false`), so Enter saves what the box shows, a cleared size (0, the default),
   with no error. What a person sees matches what is saved. A half-typed number (`1e`) is refused
   with `Merge batch size: what was typed is not a number, so it was not saved.`, as
   `deployment-operations-number-boxes.spec.js` already covers. The API refuses `"abc"` with a 400,
   and the spec checks that.
4. **Noted: the plan's "rename" doesn't exist.** The Releases pane offers only **Delete release**.
   Deleting and creating under another name works (step 2.5).
5. **Noted: the demo can't run these walkthroughs without a stand-in reconciler.** Steps 1 and 2
   depend on reconcile cycles, which `grain demo` doesn't run, and the plan's "within two reconcile
   cycles" and "the daemon log" assume one. The helper commands above run the real reconcilers on
   demand. A demo flag that runs them on a timer is filed as a follow-up.

## Fixes and tests on this branch

| Commit | What |
| --- | --- |
| `21624079a` | `fix(ui)`: `mergeBatchSizeChosen` in `Settings`; the System pane's box shows it, with the default as placeholder; Go regression test extended |
| `73cb072e7` | `test(e2e)`: `ui/e2e/suite-release-batch-walkthrough.spec.js` (all three steps); `releasehelper` commands `complete-suite-pass`, `sync-suites`, `sync-releases`, `provision-legacy`, `branch-sha` |

Also checked locally: `go test ./pkg/ui -run MergeBatch`, `npx vitest run
src/components/AdminOverlay` (90 passed), `make fmt`, `make fmt-frontend`.

The CI verdict is grain CI's full suite on the pushed branch, read with `wait_for_checks`. It is
reported on the task, not here, since this file is part of the commit CI runs on.
