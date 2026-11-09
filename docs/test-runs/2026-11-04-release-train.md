# Test run — 2026-11-04 — the release train, rebuilt (§1)

This walks §1 of [`docs/test-plans/2026-11-04.md`](../test-plans/2026-11-04.md), "The release train, rebuilt": objective qualification, the continuous train, the task train's tag grant, `grain/rc-hold` and red-RC notifications. It covers the section's "What is NOT proved" items 1–6. Task 3067.

## Summary

| # | Item | Outcome |
|---|---|---|
| 1 | One rc through the whole loop over githubsim | Passes after fixes. Walked: v3.2.0-rc.1 cut → Stage 1 dispatched on the tag → Stage 2 filed off the green Stage 1 → items closed with footers → `grain/release-qualification` posted as grain's App → `v3.2.0-pending` staged. A same-named check run from `github-actions` was ignored. Before the fixes, githubsim could not carry this loop (finding 3). |
| 2 | The objective judge on real item tasks | Passes. A PR, a follow-up on `main`, a leftover `qualification/<id>` branch and a footer naming another commit each failed their item. An `INFRA` footer was re-run, not failed. |
| 3 | Red rc → hold → rc.2, escalation at rc.3 | **Bug, fixed (finding 1).** The train never dropped a red rc: it waited for a "retry" of grain's Stage 2 verdict, which nobody makes. After the fix the whole path walks: `pipeline.rc-failed` with the right range, `[rc-blocker …]` holds `grain/rc-hold`, `rc_held`, the hold lifts, rc.2 is cut, rc.3 converges on `release/v3.3.0`, and `pipeline.release-escalated` is raised with no rc.4. **Design gap** in the hold window (O2, filed). |
| 4 | The task train's grant against the live proxy | **Bug, fixed (finding 2).** Every tick of the real template failed at its first `grain ci list`: its `grain-ci` grant is stripped on a GitHub-CI repository. After the fix, the rc tag, `candidate`, `-pending`, `-discarded`, `vM.Y.0` and `latest` go through the proxy. Patch tags, retractions, `main`, moved tags and deletes are refused. A PR widening `push:` is held for a person. The plan's "refused `vX.Y.Z` and `latest`" is stale (see item 4). |
| 5 | `grain ci checks` / `ci list --format github\|github-jobs` fidelity | Passes. No field differs from githubsim's own REST answers for a multi-attempt run. A run grain knows only from its job record is counted in `grain_note`, not invented. |
| 6 | `release-train-actions.sh` against githubsim | Passes. Two first failures produced one combined re-run dispatch (`inputs.jobs=loadtest,tunnel-stress`) and none while it was queued. Dropping rc.1 cancelled only the rc's own run, not `main`'s run on the same commit. |

Code changes on this branch (each with a regression test):

- `scripts/candidate-gate.sh`: grain's own completed `grain/release-qualification` failure is confirmed red at once (finding 1).
- `.grain/templates/release-train.yml`: the template now also grants `api:ci-read@ai-on-gke/grain` (finding 2).
- `github.RESTClient.ListTags` reads every page (finding 4).
- `pkg/github/githubsim`: check runs, tags, dispatch and cancel, workflow-run and job shapes, and `Sim.Handler` (finding 3).

The `candidate-gate.sh` and template edits are release machinery: both commits say so and expect the `release-machinery` security review. The template edit is also person-only (`release-promotion`). So this branch will not merge unattended, whatever the task's auto-merge says.

## Environment

- Debian sandbox. Go 1.26.10, git 2.39.5, jq 1.6, curl 7.88.1, Node 20.19.0, shellcheck 0.9.0 (from apt).
- Shared step 1: `cd ui && npm ci && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain`. The binary embeds the UI. The sandbox-mode `grain ci` runs in item 4/5 are this binary.
- Base: `origin/main` at `c6004bc9`. The real remote's tags are `v3.0.0`, `v3.0.1`, `v3.0.2`, `v3.1.0-rc.1`, `v3.1.0`, `v3.1.1`, `v3.2.0-rc.1` and `latest`. `latest` is on **v3.1.0**, not v3.1.1. The walks seed only the release tags (no `-rc`), so they own v3.2.0 from its first rc.
- **Backend: neither `grain demo` nor a daemon.** `grain demo -reconcile` runs no Stage 2 reconciler and no dispatch. `ui/e2e/consolerestserver` has no githubsim forge. A real daemon needs a run host plus a GitHub. So the walk is a Go driver in its own module, [`walk/`](2026-11-04-release-train/walk/) (`replace ../../../..`, never built by `./...`). It wires grain's real parts together the way the daemon does:
  - **githubsim** stands in for github.com: a bare repository seeded with this checkout's `origin/main` and release tags. It is served over HTTP to the scripts (`githubsim.Sim.Handler`, new) and through a real `github.RESTClient` to grain. Its installation token belongs to the App `grain-walkthrough` (`Sim.CheckRunApp`, new).
  - `scripts/release-from-trunk.sh` (train mode, `RELEASE_PUSH_GRANT=train`, `RELEASE_CI_SOURCE=github`, `RELEASE_GITHUB_API_URL` = the sim) and `scripts/release-train-actions.sh` run **unmodified in a scratch clone** of that bare repository. They are never run in this checkout. Each walk ends by checking the clone's `.git/config`: no `user.*` key.
  - grain's Stage 2 (`orchestrator.SyncReleaseQualification`) runs on a **real sqlite store**. The release-qualification suite and its four templates are loaded from the checkout's `.grain` by the real `declarations` reconciler.
  - Items 4/5 (`walk -proxy`): the real `release-train` schedule files the task. Its run's only way out is `gitproxy.BuildProxy`, as the daemon builds it: git goes through it to a `git http-backend` over the same bare repository, and `grain ci` goes through its `/cli` handlers to a `ui.Client` whose forge is githubsim.
- **Stubs**, all at boundaries a sandbox does not have:
  - GitHub itself: githubsim.
  - Actions executing `nightly.yml`, `tests.yml` and `release.yml`: the driver concludes the runs `release-train-actions.sh` dispatched, and seeds `tests`/`release` green on known-good commits.
  - The agent inside each qualification task: the driver writes what a run's finish writes. That is the closing note as grain's relay for the run (`automation:grain` on behalf of the task), the PR and its `LinkFixes` link, the proposals, and the scratch branch. Real agent backends need credentials the sandbox does not have.
  - Images, notes and notifications: `RELEASE_IMAGES=off`, `RELEASE_NOTES_TASK=off`, `RELEASE_NOTIFY=off`.
- Reproduce with [`run.sh`](2026-11-04-release-train/run.sh) (`bash run.sh <checkout> <scratch>`). Each run takes about 30 s. The outputs are [`walk.log`](2026-11-04-release-train/walk.log) (32 OK, 0 FAIL) and [`proxy-walk.log`](2026-11-04-release-train/proxy-walk.log) (21 OK, 0 FAIL), with `/home/debian` shortened to `~`. The check runs, notifications and `grain ci`-vs-githubsim answers are dumped as JSON in [`json/`](2026-11-04-release-train/json/) and [`json-proxy/`](2026-11-04-release-train/json-proxy/).

## Item 1 — one rc through the loop

[`walk.log`](2026-11-04-release-train/walk.log), sections 1–13. Each step reads the previous step's output from the sim or the bare repository. Nothing is canned JSON.

| Step | What ran | Observed |
|---|---|---|
| 1 | seed | `tests.yml` and `release.yml` green on `main@c6004bc9`. `nightly.yml` (schedule) last ran on `main~1`. |
| 2 | tick 1, `FORCE=true` | `phase 2: cutting v3.2.0-rc.1 on c6004bc95553` → `rc_tag=v3.2.0-rc.1`. `candidate` moved onto it. |
| 3 | `release-train-actions.sh` | `dispatching nightly.yml on it` → githubsim: `workflow_dispatch nightly.yml ref=v3.2.0-rc.1 inputs=map[]`, queued run 20003. |
| 4 | reconcile | No qualification task while Stage 1 is queued. No check run. |
| 5 | tick 2 | `PENDING ... still running`. Nothing cut. |
| 6–7 | Stage 1 red twice (item 6) | See item 6. |
| 8 | re-run passes, reconcile | Three gate items filed at priority 8 (`qualification-mergequeue`, `-ci-diagnostics`, `-base-triage`; `-system-smoke` waits). Check run `#8001 grain/release-qualification app=grain-walkthrough status=in_progress external_id=release-qualification:ai-on-gke/grain@v3.2.0-rc.1`. |
| 9 | foreign check run | `#7001 grain/release-qualification app=github-actions completed success` added. A dry run printed `IGNORED grain/release-qualification: 1 from github-actions, not grain-walkthrough; only grain's own app is trusted` and stayed PENDING. |
| 10 | agent closes gates `PASS`, reconcile, final item `PASS`, reconcile | `#8001 ... completed success`, summary `qualification-mergequeue: pass (task 1, attempt 1)` … `qualification-system-smoke: pass (task 4, attempt 1)`. |
| 11 | tick 6 (dry run, defaults) | `Qualification: PASS v3.2.0-rc.1`. But see the note below this table. |
| 12 | tick 7, `RELEASE_AUTO_PROMOTE=false` | `pending_version=v3.2.0`. `v3.2.0` was not tagged and `latest` did not move. |
| 13 | `approve v3.2.0` (person, outside the grant) | Needed `RELEASE_ALLOW_BREAKING=v3.2.0`. Then `v3.2.0` and `latest` landed on the rc's commit. |

Note on step 11: **today's `main` cannot release v3.2.0 on its own.** `refactor(models)!: remove the custom command connection type (#645)` is a breaking change since v3.1.1, so the default train stages `v3.2.0-pending` with `promotion_blocked=it carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for`. The real next release needs a person to approve it with `RELEASE_ALLOW_BREAKING=v3.2.0`, or a `RELEASE_MAJOR` bump. This is behaving as designed, but an operator should know it.

The `-pending` tag the train staged:

```
Release v3.2.0 pending approval

From v3.2.0-rc.1, which passed Stage 1 (nightly.yml) and Stage 2 (grain/release-qualification).

A person approves it (grain release approve v3.2.0) or rejects it (grain release reject v3.2.0).

Qualification: PASS
```

## Item 2 — the objective judge on real item tasks

Sections 15–16 and 21. The tasks are the suite run's own: filed by `SyncReleaseQualification` from the declared templates, with the rc's tag and commit in their fields. The scripted agent ended each attempt as follows. The verdicts are grain's own check-run summary lines.

| rc / item | Attempt 1 | Attempt 2 | grain's verdict line |
|---|---|---|---|
| rc.1 mergequeue | task 5 pushed `grain/5`, grain opened `ai-on-gke/grain#9000`, `PASS` footer | task 9: same (`#9001`) | `fail (task 9, attempt 2) -- task 9 opened a pull request` |
| rc.1 ci-diagnostics | task 6 proposed task 8 on `main`, `PASS` footer | task 10: same (task 12) | `fail (task 10, attempt 2) -- task 10 proposed a follow-up task` |
| rc.1 base-triage | task 7: `Qualification-Verdict: INFRA` | task 11: `PASS` | `pass (task 11, attempt 2)`: the INFRA attempt was re-run, not counted as a failure |
| rc.2 mergequeue | task 14: footer `Qualified-Commit:` v3.2.0's commit | task 17: same | `fail (task 17, attempt 2) -- task 17 qualified commit c6004bc95553…, not 89a784148550…` |
| rc.2 ci-diagnostics | task 15 left `refs/heads/qualification/15` on the remote, `PASS` | task 18: same | `fail (task 18, attempt 2) -- task 18 left refs/heads/qualification/18 behind` |
| rc.2 base-triage | task 16: `PASS` | — | `pass (task 16, attempt 1)` |

After the first attempts, each of the three items had exactly one re-run filed and the final item was not started. Two flake retries of one item within 24 h also raised `pipeline.flake-budget:ai-on-gke/grain:<item>` (section 23's notification list).

## Item 3 — red rc → hold → rc.2 → rc.3 → escalation

Sections 17–23.

1. **rc.1 red.** grain posted `#8002 grain/release-qualification completed failure` and raised `pipeline.rc-failed:v3.3.0-rc.1` (warning). Its body: ``Commits to triage: `c6004bc95553…..55cf8c54caec…` (everything since v3.2.0)``. This is the right `<floor>..<rc_sha>` range. The body also gives the item lines and the `[rc-blocker v3.3.0-rc.1]` instruction.
2. **Before the fix**, the next train run printed `PENDING grain/release-qualification: failed for the first time (failure); it gets one retry on v3.3.0-rc.1` and `qualification_rerun=qualification:grain/release-qualification:flake`. `release-train-actions.sh` answered `...is the deployment's to re-run ...; nothing to do here`. Nothing ever dropped rc.1 (finding 1).
3. **After the fix:** a fix landed on `main`. A task titled `[rc-blocker v3.3.0-rc.1] fix the merge queue` was filed, and the reconcile posted `#8003 grain/rc-hold in_progress`, title `Holding v3.3.0-rc.2`. Tick 9 then printed `FAIL grain/release-qualification: failed (failure): grain concludes failure only once an item has failed its own retry, so it is confirmed red`, emitted `discarded_rc=v3.3.0-rc.1`, and then `phase 2: v3.3.0-rc.2 is held by grain/rc-hold on 55cf8c54caec: ... task 13.` → `rc_held=v3.3.0-rc.2`. No rc.2. Tick 10: still held.
4. **The blocker closes.** The hold became `completed success`, `Blockers landed`. Tick 11 emitted `rc_tag=v3.3.0-rc.2` on the fix commit. The next reconcile cleared `pipeline.rc-failed:v3.3.0-rc.1`.
5. **rc.2 red, no blocker.** Tick 12 dropped it and made `release/v3.3.0` from rc.2's commit (`convergence_branch=release/v3.3.0`). A fix cherry-picked onto the branch went green on `release.yml`, and tick 13 cut `v3.3.0-rc.3` from the branch.
6. **rc.3 red.** grain raised `pipeline.rc-failed:v3.3.0-rc.3` ("This was the version's last attempt") and `pipeline.release-escalated:v3.3.0` (critical). A new known-good commit landed on `main`. Tick 14 printed `v3.3.0 has had 3 release candidates dropped, RELEASE_MAX_RC_ATTEMPTS (3); the train cuts no more of it until a person resets or advances it`, emitted `escalated_version=v3.3.0`, and exited 1. No `v3.3.0-rc.4`. `release-train-actions.sh` printed the `::error::` line and dispatched nothing.

## Item 4 — the task train's grant through the scoped proxy

[`proxy-walk.log`](2026-11-04-release-train/proxy-walk.log). The real `release-train` schedule filed task 1, "Run the release train (check, release, cut)", from `repo:template:ai-on-gke:grain:release-train`. Its run on `sandbox-train` holds the template's declared push list, as the proxy reads it: `candidate` (force), `release/v*`, `latest` (force), `v*-discarded`, `v*-pending`, `v*-rc.*`, and `v?.?.0` … `v??.???.0`. The sandbox cloned through the proxy and ran the template body's one command, `bash ~/work/scripts/release-train-task.sh`.

- **Before finding 2's fix**, every tick ended like this:
  ```
  grain: forbidden: grain ci: that tool belongs to the "https://grain.dev/capabilities/grain#ci" capability, and this run does not hold it ...
  release-train-task: grain ci list could not read main's GitHub Actions runs
  RELEASE_TRAIN_RESULT=failed
  ```
- After it, tick A cut `v3.2.0-rc.1` and pushed it and `candidate` through the proxy (`RELEASE_TRAIN_RESULT=acted`). Tick B read Stage 1 and Stage 2 through `grain ci` and pushed `v3.2.0-pending`. That release carries #645's breaking change, so the train staged it rather than releasing it.

Pushes by hand from the task's sandbox, with the proxy's refusals:

| Push | Result | The proxy said |
|---|---|---|
| `refs/tags/v3.1.2` (patch) | refused | `refs/tags/v3.1.2 is not declared. This task may push its own branch, refs/heads/grain/task-1, and what push: in .grain/templates/release-train.yml declares (as read from ai-on-gke/grain's default branch at ffa4d36ac54b); a run cannot widen that by editing the file on its own branch -- the change has to merge first` |
| delete `latest` | refused | `refs/tags/latest may be pushed but not deleted (its entry does not say delete: true)` |
| delete `candidate` | refused | `refs/heads/candidate may be pushed but not deleted ...` |
| delete `v3.2.0-pending` | refused | `... may be pushed but not deleted ...` |
| move `v3.1.0` | refused | `refs/tags/v3.1.0 already exists, and moving a tag is a force-push its entry does not allow (force: true)` |
| rewind `main` | refused | `refs/heads/main is not declared ...` |
| `v3.1.0-retracted` | refused | `... is not declared ...` |
| move `v3.2.0-rc.1` | refused | `... already exists, and moving a tag is a force-push ...` |
| `v3.3.0-rc.1` on a commit only the sandbox has | refused | `could not show cc7cb3c50184 is already on the default branch or a declared branch (... a commit this push carries, new to the repository), and a declared push only moves refs onto commits the repository has` |
| `v3.2.0-rc.1-discarded` | allowed | |
| `v3.2.0` on the rc's commit | allowed | |
| `latest`, force-moved onto it | allowed | |

**The plan's expectation is stale.** The plan says the grant refuses `vX.Y.Z` and `latest`. Since 2026-11-04 the train releases a qualified rc itself (playbook §1), and the template grants `vM.Y.0` (create once) and `latest` (force) on purpose. `TestTheReleaseTrainsGrantReachesOnlyItsOwnRefs` covers this. The proxy still refuses patch tags, moves and deletes of releases, retractions, deleting `latest`, and anything off `main`'s history. `release-from-trunk.sh` narrows `vM.Y.0`/`latest` further, to the qualified rc's commit in the same run (`check_push`, covered by `release-from-trunk_test.sh`). So a run that misbehaves by hand can tag `v3.2.0` on any commit on `main` without the script, and only a person can narrow that.

**A PR editing `push:`.** Task 2 widened the list with `refs/tags/v*` and pushed. `orchestrator.ProcessResult` opened `ai-on-gke/grain#9000` and withdrew auto-merge: `touches the trust boundary (release-promotion, template-push)`, with a human-only hold `trust-boundary/v19` and security review task 3. After review 3 completed clean, `SyncSecurityHolds` still left auto-merge off: "changes to **release-promotion** and **template-push** never merge automatically: press Submit once you have read the review." A control PR that only reworded the body was held too, as `release-promotion` alone, because the template file itself is person-only.

## Item 5 — `grain ci` fidelity against githubsim

Same sandbox, after tick B. The sim held a `nightly.yml` run (31337) on the rc with `run_attempt` 2: `loadtest` failed on attempt 1 and passed on 2, and the other 13 jobs ran on attempt 1. It also held grain's and `github-actions`' `grain/release-qualification` check runs, and a `GitHubCIJob` record for run 4242, which GitHub no longer lists. The driver compared each answer field by field with the sim's own REST answer ([`json-proxy/`](2026-11-04-release-train/json-proxy/)):

| Command | Compared with | Result |
|---|---|---|
| `grain ci checks <sha> --name grain/release-qualification --format github` | `GET .../commits/<sha>/check-runs?check_name=…&filter=all` | 2 = 2. `id`, `name`, `head_sha`, `status`, `conclusion`, `external_id`, `started_at`, `completed_at` and `app` identical. `grain_app: grain-walkthrough`. |
| `grain ci list --ref main --format github` | `GET .../actions/runs?branch=main` | 3 = 3. `name`, `path`, `head_branch`, `head_sha`, `event`, `status`, `conclusion`, `run_attempt`, `run_started_at`, `updated_at` and `repository.full_name` identical. `grain_note: 1 run(s) left out: grain has them only from its record of their jobs, not as the forge's workflow runs`. Run 4242 is not listed. |
| `grain ci list --ref v3.2.0-rc.1 --workflow nightly.yml --format github-jobs` | `GET .../actions/runs/31337/jobs?filter=all` | 15 = 15 (14 + loadtest's first attempt). `id`, `run_id`, `run_attempt`, `workflow_name`, `head_sha`, `name`, `status` and `conclusion` identical. |

The text form of `grain ci checks` marks grain's own App with `(grain)`:

```
success      grain/release-qualification              grain-walkthrough (grain)
failure      grain/release-qualification              github-actions
```

The train read these answers and judged `PASS loadtest: passed on its one retry (failure,success): a flake`. It printed `IGNORED grain/release-qualification: 1 from github-actions`.

## Item 6 — `release-train-actions.sh`

- The rc.1 cut (`rc_tag=`, no `RELEASE_TAGS_FIRE`) produced one `nightly.yml` dispatch on the tag with no `inputs.jobs`.
- Stage 1 then finished with `loadtest` and `tunnel-stress` failing. The train emitted `qualification_rerun=nightly:loadtest:flake,nightly:tunnel-stress:flake`. The helper printed `re-running loadtest,tunnel-stress` and sent **one** dispatch, `inputs.jobs=loadtest,tunnel-stress`.
- A second wake while that run was queued printed `nightly.yml is still running on it (20004); re-running loadtest,tunnel-stress waits for it` and dispatched nothing.
- Before rc.1 was dropped, run 20008 (`workflow_dispatch` on `v3.3.0-rc.1`) and run 20009 (`schedule` on `main`) were both in progress on the same commit. After `discarded_rc=v3.3.0-rc.1`: `cancelling its nightly.yml run 20008`. githubsim's `CancelledRuns` was `[20008]`, so `main`'s run was untouched.

## Findings

### 1. Bug (fixed): a red rc was never dropped, so the red-rc path could not run

`candidate-gate.sh check-qualification` judged `grain/release-qualification` with the Stage 1 job rule: the first failure is a flake that "gets one retry". But grain concludes `failure` only once an item has failed its own retry (`model.DecideReleaseQualification`), and it never looks at a candidate again once its check run is completed (`qualifyCandidate`). Nobody retries. Every red rc therefore stayed in qualification forever. The train never tagged `-discarded` and never cut rc.N+1, and `grain/rc-hold`, rc.3 convergence and escalation were unreachable in practice. The unit tests hid this: `candidate-gate_test.sh` asserted "Stage 2's first failure gets one retry", and `release-from-trunk_test.sh` only modelled a red rc as a Stage 1 job failing twice.

- **Repro:** the walk before the fix (section 19), or `scripts/release-from-trunk_test.sh`'s new `stage 2 red` case against the old `candidate-gate.sh`, which fails 3 of its 5 checks.
- **Fix:** grain's newest completed attempt is the check run's verdict. `failure` is confirmed red (`FAIL grain/release-qualification: failed (failure): grain concludes failure only once an item has failed its own retry, so it is confirmed red`), and success after a failure is a pass. Neutral (INFRA) is unchanged (see O1).
- **Tests:** `candidate-gate_test.sh` ("Stage 2's failure is confirmed red at once", "asks for no re-run", "newest attempt is its verdict") and `release-from-trunk_test.sh` ("stage 2 red": drops the rc, `qualification=fail`, no `qualification_rerun`).

### 2. Bug (fixed): the task train could never read its verdicts

`.grain/templates/release-train.yml` declared `capabilities: [grain-ci]`. ai-on-gke/grain's CI is GitHub Actions (`ci_mode: github`), and composing a task for a repository grain CI does not judge strips `grain#ci` and its `grain-ci` alias (`model.applyGrainCIGrant`). The task the schedule files therefore holds no grant at all, and the proxy refuses its first `grain ci list`. Every 20-minute tick ends `RELEASE_TRAIN_RESULT=failed`. The same strip had already been worked around for workflow-run reviews (9ef73cc4).

- **Repro:** `walk -proxy` before the fix (item 4), or `TestTheReleaseTrainTaskMayReadItsRepositorysCI/github` against the old template (`the release train's run may not read CI (grants [])`).
- **Fix:** the template also declares `api:ci-read@ai-on-gke/grain`. `grain-ci` stays for a deployment that runs grain CI on the repository.
- **Test:** `pkg/gitproxy`'s `TestTheReleaseTrainTaskMayReadItsRepositorysCI` fires the real schedule under both CI modes and checks the run's namespace scope reads the repository's CI.

### 3. Sim gap (fixed): githubsim could not carry the train's GitHub traffic

Walking items 1, 3 and 6 over githubsim, as the plan asks, hit these:

- **Check runs.** `POST .../check-runs` recorded no App and no `head_sha`. So grain's own `grain/release-qualification` was never found again (`latestCheckRun` matches on the App), and every reconcile would post another. `PATCH .../check-runs/{id}` **ignored the id** and rewrote every check run of the same name on every commit. An rc.2 verdict overwrote rc.1's, and a hold on one rc lifted another's. `started_at`, `completed_at` and `output` were dropped. `check_name` was ignored.
- **Endpoints.** There was no `GET .../tags` (the Stage 2 reconciler's `ListTags` panicked), no `workflow_dispatch`, and no run cancel.
- **Workflow runs** had no `repository` and no `updated_at`. `candidate-gate.sh`'s `JQ_GITHUB_RUNS` drops a run whose `head_repository` differs from `repository`, so no commit was ever known-good. A queued run was served `conclusion: "success"`.
- **Jobs** had no `run_id`, `run_attempt`, `head_sha` or `workflow_name`, and no notion of attempts or `?filter=all`.

**Fix:** all of the above, plus `Sim.Handler` to serve the sim over HTTP to non-Go clients. **Tests:** `pkg/github/githubsim/release_train_test.go`. The existing `pkg/orchestrator`, `pkg/ui`, `pkg/demo`, `pkg/gitproxy`, `pkg/daemon` and `pkg/github` suites pass on the changed sim.

### 4. Latent bug (fixed): `ListTags` read one page

`github.RESTClient.ListTags` read `GET .../tags?per_page=100` once, "deliberately: the caller only ever wants the newest candidate". GitHub does not promise newest-version-first, and a name order puts `v3.9.0`'s tags ahead of `v3.10.0-rc.1`'s. The train makes three to six tags per version (`-rc.N`, `-discarded`, `-pending`, the release), and today's 8 tags pass 100 within a few dozen versions. Past that, the Stage 2 reconciler could miss the newest rc, or the `-discarded` marker that drops one, and qualify the wrong candidate. **Fix:** follow the `Link` header. **Test:** `TestTagsAreListedPeeledAndPagedAndTheClientReadsEveryPage` (120 tags; `v3.0.0-rc.1` is on page 2).

### Observations (filed or noted)

- **O1 — a neutral (infra-stalled) Stage 2 has the same deadlock as finding 1, unfixed.** grain posts `completed neutral` and never re-runs a completed candidate. The gate says `RERUN qualification ... infra`, and `release-train-actions.sh` says it is "the deployment's to re-run". The rc stays in qualification until a person tags `-discarded` by hand, and nothing tells them to. Whether grain should re-qualify after repair or the train should drop the rc is a design decision. Filed with `propose_task` (held for approval).
- **O2 — `grain/rc-hold` only holds if the blocker exists before the next tick.** grain posts the hold only once an `[rc-blocker rc.N]` task exists, and the train treats an absent hold as "not held". The train's next tick (at most 20 minutes after `pipeline.rc-failed`) drops rc.N and, in the same run, cuts rc.N+1 off the newest known-good commit. Unless the steward files the blocker inside that window, rc.N+1 is cut off a tree that still has the defect and spends one of the version's three attempts. The walk filed the blocker first, so the hold worked as designed. Filed with `propose_task` (held for approval).
- **O3 — two different views of Stage 1.** The Stage 2 reconciler's `stage1Green` counts the newest `nightly` run on the rc's **commit**, from any ref (main's own schedule included). The GitHub-sourced train (`RELEASE_CI_SOURCE=github`) also reads `nightly.yml` runs by `head_sha`. But `release-train-task.sh`, the grain-task train, reads `grain ci list --ref <rc>`: only runs on the rc's tag. On a commit `main`'s nightly already passed, Stage 2 can start before the rc's own Stage 1 has run, and the task train still waits for it. The two eventually agree, so this is noted, not filed.
- **O4 — the plan's §1 "What changed" is partly superseded.** `release-train.yml` on GitHub Actions is retired as a train and always runs dry. The re-runs and cancels moved to `rc-qualification.yml`, and the train is the grain task. The task grant now reaches `vM.Y.0` and `latest` (item 4).
- **O5 — the real remote's `latest` is on v3.1.0, not v3.1.1.** A patch release from `release/v3.1.1` did not move `latest`. Noted only. Nothing in §1 says it should.

## Commands

```
bash docs/test-runs/2026-11-04-release-train/run.sh . /tmp/rt     # both walks
bash scripts/candidate-gate_test.sh                               # 57/57
bash scripts/release-from-trunk_test.sh                           # 421/421
go test ./pkg/github/... ./pkg/gitproxy/ -run 'TestTheReleaseTrain|TestCheckRuns|TestTags|TestWorkflowDispatch|TestRunJobs|TestHandler'
go test ./pkg/github/... ./pkg/orchestrator/ ./pkg/ui/ ./pkg/demo/ ./pkg/gitproxy/ ./pkg/daemon/ ./pkg/model/ ./tests/deploy/ ./pkg/trustboundary/
shellcheck scripts/candidate-gate.sh scripts/candidate-gate_test.sh scripts/release-from-trunk_test.sh docs/test-runs/2026-11-04-release-train/run.sh
```
