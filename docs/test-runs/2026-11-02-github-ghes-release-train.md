# Test run 2026-11-02 — §9 GitHub: GHES GraphQL, the release train's LKG, `-github-sim` workflow runs, review `ci-read`, external PR chip

Plan: [`docs/test-plans/2026-11-02.md`](../test-plans/2026-11-02.md) §9 (PRs #412, #408, #386, #390, #327, #326). Task 2451, filed by task 2399.

## Environment

- Tree: `origin/main` at `cc939397` (PR #438). The binary was built from it with the real UI embedded (`cd ui && npm ci && npm run build`, `go build -o ~/bin/grain ./cmd/grain`): `v0.0.0-20261102044304-cc939397bb35+dirty`. "Dirty" is this branch's own `pkg/mcp` fix, rebuilt in after F1.
- Sandbox: 7 CPUs, Go 1.26.10, Node 20.19, Chromium from `/var/cache/playwright`. No sandbox backend; the git proxy holds 9728.
- **GHES boundary**: [`2026-11-02-github-ghes-release-train/fake_ghes.py`](2026-11-02-github-ghes-release-train/fake_ghes.py) on `127.0.0.1:8443`. It serves REST only under `/api/v3` and GraphQL only at `/api/graphql`. `/graphql`, `/api/v3/graphql` and REST outside `/api/v3` answer 404 and are logged as wrong. It knows `acme/widgets`, with PR #7 (head `feature`), one unresolved review thread and a merge queue. `POST /__mode {"graphql401":true}` makes `/api/graphql` answer 401 while REST keeps working. Every request is logged as a JSON line.
- **What talked to it.** These are the pieces of grain a real deployment uses for a repository on a GHES host:
  - **`grain mcpserver`**, the subprocess `grain daemon` forks for every run (`pkg/daemon/agents.go`), with the flags the daemon gives it: `-data-dir`, `-pr-repo 127.0.0.1:8443/acme/widgets`, `-pr-branch feature` and `-pr-credential ghes`. The credential `ghes` has a host record, `secrets/github/ghes.host.json` = `{"host":"127.0.0.1:8443","kind":"github","base_url":"http://127.0.0.1:8443"}`, which is what a git connection on a GHES writes. The driver is [`mcp_call.py`](2026-11-02-github-ghes-release-train/mcp_call.py), plain stdio JSON-RPC.
  - **The daemon's forge route for a repository off `-github-host`**: `gitproxy.CredentialSet.ForgeTransportOn` (what `pkg/daemon/forgeroute.go` uses for the reconcile loop's PR, merge and merge-queue calls) under `github.RESTClient`. The driver is [`mqdriver/`](2026-11-02-github-ghes-release-train/mqdriver/main.go), a nested module (`replace ../../../..`) so `./...` never builds it.
  - **A real `grain daemon`** (`-data-dir … -ui-addr 127.0.0.1:4390 -disable-native-sandboxes -sandbox-port-base=0 -github-host 127.0.0.1:8443 -github-insecure-http`). It started and served the UI, but sent nothing to the fake GHES while idle. Its reconcile loop reads review threads only for a task with an open PR, which needs a run host and an agent this sandbox has neither of, so the GraphQL calls were driven through the two pieces above. See "Not run".
- **Release train**: `scripts/candidate-gate.sh select-lkg` and `scripts/release-from-trunk.sh` with `DRY_RUN=true`. Input was real `ai-on-gke/grain` workflow runs, read unauthenticated and read-only from `api.github.com` at 05:14 UTC. They're recorded, trimmed to the fields the scripts read, in [`fixtures/ai-on-gke-grain-main-runs-2026-11-02T0514Z.json`](2026-11-02-github-ghes-release-train/fixtures/ai-on-gke-grain-main-runs-2026-11-02T0514Z.json): 412 runs, `tests.yml` 209, `release.yml` 200, `nightly.yml` 3, all on `branch=main`. `release-from-trunk.sh` reads the runs with `GITHUB_TOKEN`, which the sandbox doesn't have, so the same listing was handed to it through `RELEASE_RUNS_JSON`. The counterfactual run (below) ran in a throwaway clone of a local bare repo, so nothing could reach `origin`. Nothing was tagged, pushed or published on GitHub.
- **`-github-sim` loop**: `grain demo -github-sim -fake-ci -scheduled-workflows -reconcile 2s -addr 127.0.0.1:4391`, driven over its API with the access code. Shared environment item 3: the demo runs the orchestrator cycle that files schedule reviews and release triage, which is all this part needs.
- **External PR chip**: `grain demo -github-sim -fork-pull-request` on `127.0.0.1:4392` for screenshots, and the worker-scoped demo of `ui/e2e/fixtures.js` for the specs.

## 1. GHES end to end (What is NOT proved 1)

### Requests the fake GHES logged

`pull_request_status` (real `grain mcpserver`, control surface):
```
GET  /api/v3/repos/acme/widgets/branches/feature                         200
GET  /api/v3/repos/acme/widgets/pulls?state=open&head=acme%3Afeature     200
GET  /api/v3/repos/acme/widgets/pulls/7                                  200
GET  /api/v3/repos/acme/widgets/pulls/7/comments?per_page=100            200
POST /api/graphql                                    200  graphql:reviewThreads
GET  /api/v3/repos/acme/widgets/commits/1111…/check-runs?per_page=100    200
```
It reported `Inline review comments (1 thread(s), 1 unresolved): comment 501 … thread PRRT_fake1, unresolved`.

`resolve_review_thread {"thread_id":"PRRT_fake1"}` then `pull_request_status` (`-mcp-surface full`, [`log-resolve.jsonl`](2026-11-02-github-ghes-release-train/log-resolve.jsonl)):
```
GET  /api/v3/repos/acme/widgets/pulls?state=open&head=acme%3Afeature     200
POST /api/graphql                                    200  graphql:reviewThreads
POST /api/graphql                                    200  graphql:resolveReviewThread
…
POST /api/graphql                                    200  graphql:reviewThreads   -> "1 thread(s), 0 unresolved … resolved"
```

Merge queue, through `ForgeTransportOn` (`mqdriver`):
```
POST /api/graphql  200  graphql:mergeQueue           MergeQueueEntryOf  -> <nil> (not queued)
POST /api/graphql  200  graphql:mergeQueue           EnqueuePullRequest: queue state read
POST /api/graphql  200  graphql:enqueuePullRequest   -> {State:QUEUED Position:1}
POST /api/graphql  200  graphql:mergeQueue           MergeQueueEntryOf  -> {QUEUED 1 AlreadyQueued:true}
POST /api/graphql  200  graphql:reviewThreads        ListReviewThreads  -> [PRRT_fake1]
```
Nothing went to `/graphql` or `/api/v3/graphql`, and no REST call went outside `/api/v3`.

### Forced 401 on `/api/graphql` ([`log-401.jsonl`](2026-11-02-github-ghes-release-train/log-401.jsonl))
```
POST /api/graphql                 401  forced 401
GET  /api/v3/repos/acme/widgets   200      <- graphqlRefusal's REST probe
```
`resolve_review_thread` showed the new note:
> reading pull request #7's review threads: GitHub answered 401: Bad credentials (the GraphQL endpoint (POST http://127.0.0.1:8443/api/graphql) refused the request, though the REST API on the same connection answers for acme/widgets)

`pull_request_status` showed no note at all (**F1**, fixed below).

**Verdict: confirmed.** Review threads, `ResolveReviewThread`, the merge-queue query and the enqueue all post to `/api/graphql` beside `/api/v3`. A 401 names the endpoint and says the REST probe worked. Both hold for a credential with a GHES host record. A GHES given as `-github-host` without one doesn't get this layout at all (**F4**).

## 2. `select-lkg` against real GitHub data (What is NOT proved 2)

```
bash scripts/candidate-gate.sh select-lkg --runs-json fixtures/ai-on-gke-grain-main-runs-2026-11-02T0514Z.json --ref origin/main --max-commits 40
DRY_RUN=true RELEASE_CI_SOURCE=github RELEASE_RUNS_JSON=<same> RELEASE_TASKS_JSON=<[]> RELEASE_IMAGES=off RELEASE_NOTES_TASK=off RELEASE_NOTIFY=off bash scripts/release-from-trunk.sh
```

**The LKG it chose: none.** `select-lkg` printed `LKG none` (exit 1). `release-from-trunk.sh` printed:
```
major 3 (floor minor 0); newest promoted release v3.0.0; target v3.1.0; its notes are from v3.0.0
phase 1: nothing soaking; v3.1.0's rc.1 must be after v3.0.0 (dc8c85627214)
phase 2: no new LKG commit on main since dc8c85627214; nothing to cut
```

**Why.** All three `nightly.yml` runs on main failed:

| Run | Commit | Failed jobs |
|---|---|---|
| 41377312988, 11-02 02:21 | `744a1dbe` | `go, shuffled and repeated (3)`, `(4)` |
| 41264850163, 11-01 | `1d324a89` | `ui e2e, repeated (1)`, `go, shuffled and repeated (3)`, `(4)` |
| 41159548797, 10-31 | `6e0411a9` | `go, shuffled and repeated` |

A nightly can only vouch for a commit when it passed, so no commit qualifies. The commits right after the newest nightly say so in the gate's own words. `6e703d14`, `6cfcdf3d` and `978b4160` have `tests.yml success release.yml success`, but "the newest nightly.yml on its ancestry (744a1dbecd03) is failure". `744a1dbe` itself has its own `nightly.yml failure`. The newest 14 commits are `pending` (the head) or `infra`.

It is right to ignore PR and fork runs. The `branch=main` listing holds only `push`, `schedule` and `workflow_dispatch` runs. To test the filter I added three fakes at the head commit, each claiming success: a `pull_request` run of `tests.yml`, a `push` run of `release.yml` from `someone/grain-fork`, and a fork `pull_request_target` run of `nightly.yml`. The head stayed `tests.yml pending release.yml pending`, so all three were ignored.

**Counterfactual** (same data, the `nightly.yml` at `744a1dbe` flipped to success, `--now` 05:14Z):
- With no task list: `6e703d14: … nightly.yml passed on 744a1dbecd03, but with no task list to show no repair of main is open, only a commit with a nightly.yml of its own qualifies`, then `LKG none`. That is right, per the script's doc.
- With `--tasks-json []`: `6e703d14: tests.yml success release.yml success; nightly.yml passed on its ancestor 744a1dbecd03 2h ago and no repair of main is open; known-good`, then **`LKG 6e703d14f3de`**. That is the newest first-parent commit with its own `tests.yml` and `release.yml` green and a green nightly on its ancestry within 24 h. The 14 commits above it are `pending`, `infra` or `tests.yml failure` (`7fc16563`, `09b383ac`).
- `release-from-trunk.sh` with `DRY_RUN=true` on that input (throwaway clone): `phase 2: cutting v3.1.0-rc.1 on 6e703d14f3de`, `[dry-run] would git push origin refs/tags/v3.1.0-rc.1`, `[dry-run] would git push origin --force 6e703d14…:refs/heads/candidate`. That is right, except the clone kept a local `v3.1.0-rc.1` tag (**F2**).

An observation, not a bug: 125 of the 209 `tests.yml` runs on main and 72 of the 200 `release.yml` runs are `cancelled`. The jobs' `concurrency` groups don't cancel in progress on main (`cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`), but GitHub still cancels a *pending* run when a newer one queues in the same group. So with merges arriving faster than a run takes, most main commits never get a verdict, and only the commits whose runs weren't superseded can ever be the LKG. The gate reads them as `infra`, which is right. Worth knowing when the train finds nothing to cut.

**Verdict: confirmed** for the selection logic on real data. Today's real answer is "nothing to cut" because nightly is red.

## 3. The `-github-sim` loop (What is NOT proved 3)

On the demo I pushed `.grain/schedules/nightly-failures.yaml` (`trigger: github_workflow_run: {workflow: nightly.yml, conclusions: [failure], branches: [main]}`) to the sim's bare repo, then ran `PUT …/ci-mode {"ciMode":"github"}` and `PUT …/declarations {"declarations":true}`. The orchestrator logged `acme/widgets at 16b81c63f600 declares … 1 schedule(s)`.

1. **A failing `nightly.yml` run, `?webhook=1`** (`id 2101`, event `schedule`, job `go-long` failing at `go test -race`). The POST answered `webhook_queued: true`. Within one cycle the schedule filed **task 20, "Review last night's "nightly" workflow run"**, with capabilities `["api:ci-read@acme/widgets"]` (PR #390). grain's comment on it reads "GitHub Actions workflow `nightly` (`.github/workflows/nightly.yml`) on `main` concluded **failure**: run `2101` … Run `grain ci log 2101`", with the failure tail.
2. **The review can read the log.** I ran the real `grain mcpserver -mcp-surface full -server <demo> -task 20 -grant api:ci-read@acme/widgets -pr-repo acme/widgets`, which is the server a sandboxed run's `grain ci log` reaches. It registered `list_ci_runs`, `get_ci_log` and `query_test_history`. `get_ci_log {"run_id":"2101"}` returned the `go test -race` step with `--- FAIL: TestNightlyWalk` and the marker. `list_ci_runs {"repo":"acme/other"}` was refused: "not a repository this run may read CI for". The mcpserver authenticated to the demo as the operator. A run's own agent token couldn't be minted here, so the agent-API gate's check of a *run* token is what PR #390's unit tests cover, not this.
3. **A passing `nightly.yml` run** (`2102`, `?webhook=1`) filed nothing. That's right: the schedule asks only for failures.
4. **Release triage.** It reacts only to release/publish-named workflows (`isGitHubReleaseWorkflow`), so no `nightly.yml` run can open it. The plan's step 3 runs the two together. I used `release.yml` for this step. A failing `release.yml` push run on main (`2201`, `?webhook=1`) → `filed 21 to triage failed release on acme/widgets main at 16b81c63f600 (failure)`. A passing one (`2202`, `?webhook=1`) → task 21 **closed**: "Closed as superseded: GitHub Actions release workflow `release` published `main` on acme/widgets at 16b81c63f600 (run `2202`), so its releases are working again …".

**Verdict: confirmed**, with F3: the review task's description is the grain CI prompt. The walk was driven over the demo's API rather than by clicking. `workflow-schedules-candidate-walkthrough.spec.js` covers the schedule pages in the browser.

## 4. External PR chip (What is NOT proved 4)

- A task filed on `contrib:fix/readme-typo` gets `externalPullRequests: ["acme/widgets#41"]`.
- `make ui-screenshots PAGES=/tasks/20 URL=http://127.0.0.1:4392 CODE=…` shot it before and after the move, at 1440 (desktop) and 390 (phone), light and dark: `ui/screenshots/before-move/`, `ui/screenshots/after-move/`, eight PNGs and two contact sheets. Neither of this sandbox's file readers renders a PNG, and there's no Gemini key for `LOOK=1`. So the pages were examined with [`look-chip.mjs`](2026-11-02-github-ghes-release-train/look-chip.mjs), which measures the rendered DOM in a real Chromium in each mode and saves a JPEG crop of the header line:

| Width / theme | Chip in `.freshness` | Chip box | Line height | Label / background | Contrast | Overflow |
|---|---|---|---|---|---|---|
| 1440 light | yes, "as of just now · acme/widgets#41" | x 350, w 117, h 24 | 24 | rgb(138,106,46) on white | 5.02:1 | 0 |
| 1440 dark | yes | x 350, w 117, h 24 | 24 | rgb(217,164,65) on rgb(20,22,26) | 8.05:1 | 0 |
| 390 light | yes | x 106, w 117, h 24 | 24 | as light | 5.02:1 | 0 |
| 390 dark | yes | x 106, w 117, h 24 | 24 | as dark | 8.05:1 | 0 |

  The `href` is `https://github.com/acme/widgets/pull/41` in every mode.
- **After `PATCH /api/tasks/20 {"repo":"acme/gizmos"}`**, with `acme/gizmos` added to `targetRepos`: `externalPullRequests` and `links` are null, and the chip count is 0 in all four modes. The freshness line drops back to one text row (height 19) reading "as of just now".
- New spec [`ui/e2e/external-pr-chip-walkthrough.spec.js`](../../ui/e2e/external-pr-chip-walkthrough.spec.js) pins both: the dark-mode layout at 1440 and 390 px with bounding boxes, and the chip gone after the move. It restores the shared demo's `targetRepos` in `finally`. It and `external-pr-chip.spec.js` passed 18/18 with `--repeat-each=3`.

**Verdict: confirmed.**

## Findings

### F1 — `pull_request_status` dropped the GraphQL refusal (fixed)

With `/api/graphql` answering 401, `pull_request_status` listed the inline comments as "1 thread(s)" with no resolution state and gave no reason. A run reads that the same as "nothing left open". PR #412's endpoint note reached `resolve_review_thread` but not the status report. The cause was `reviewCommentsSection` (`pkg/mcp/pullrequest_review_tools.go`) throwing away the `ListReviewThreads` error.

- **Reproduction**: run `fake_ghes.py`, then `POST /__mode {"graphql401":true}`, then call `pull_request_status` through `grain mcpserver … -pr-credential ghes` ([`log-401.jsonl`](2026-11-02-github-ghes-release-train/log-401.jsonl)).
- **Fix**: one line under the comments, "Whether each thread is resolved could not be read: GitHub answered 401: Bad credentials (the GraphQL endpoint (POST http://127.0.0.1:8443/api/graphql) refused the request, though the REST API on the same connection answers for acme/widgets)". Rechecked live, [`log-401-after-fix.jsonl`](2026-11-02-github-ghes-release-train/log-401-after-fix.jsonl).
- **Regression test**: `TestPullRequestStatusListsReviewCommentsWithoutThreads` asserts the line appears, and that a reader with no thread view at all still says nothing.

### F2 — `release-from-trunk.sh` `DRY_RUN=true` leaves local tags behind (follow-up)

Phase 2 runs `git tag -a vX.Y.0-rc.N` whatever `DRY_RUN` says; only `push` and `release_images` check it. A second dry run in the same checkout then reads `phase 1: v3.1.0-rc.1 (6e703d14f3de) soak in progress (0h / 24h); nothing to cut while it soaks`, though nothing was pushed. The reproduction is above (two runs in `/tmp/rft`). `scripts/*` is a trust-boundary and release-promotion path, so this is filed as a follow-up held for a person rather than edited here.

### F3 — `github_workflow_run` reviews get the grain CI prompt (follow-up)

Task 20's description is `model.workflowReviewTemplate("nightly")`. It says "grain CI has run this repository's `nightly` workflow", tells the agent to read "`.grain/workflows/nightly.yml`", and says "Nothing in this run was served from grain CI's cache". All three are wrong for a GitHub Actions run of `.github/workflows/nightly.yml`, and they contradict grain's first comment on the same task. pkg/repodecl's `GitHubWorkflowRun` case reuses the grain CI template ID. The reproduction is §3 step 1, then `GET /api/tasks/20`. This needs its own system template, which reaches into pkg/model and pkg/repodecl, so it's filed as a follow-up.

### F4 — A GHES named as `-github-host` gets REST at the root (follow-up)

Without a credential host record, both the daemon's default transport and mcpserver's `repoTransport` use `PathPrefix ""`. The fake GHES logged `GET /repos/acme/widgets/branches/feature 404 (outside /api/v3)` ([`log-github-host-only.jsonl`](2026-11-02-github-ghes-release-train/log-github-host-only.jsonl)). `pull_request_status` then told the run "Branch feature does not exist on acme/widgets yet … `git push origin feature`" about a branch that exists. This is the limitation `github.APIHost`'s comment admits, but the run is told the wrong thing. Filed as a follow-up.

### F5 — Wording (follow-up, one task)

- On the control surface, `pull_request_status` says to use `reply_to_pull_request_comment` / `resolve_review_thread`, which that surface doesn't serve. A sandboxed run's spellings are `grain pr reply` / `grain pr resolve`.
- `get_ci_log` heads a GitHub Actions run "grain CI run 2101".
- The triage close log reads "closing 21 as superseded: Closed as superseded: …".

### Plan corrections

- §9 "What is NOT proved" 3 has a `nightly.yml` run opening and closing release triage. Triage only follows release/publish-named workflows, so it was walked with `release.yml`.
- The plan says `grain demo` has no orchestrator dispatch. That's true, but its cycle *does* fire `github_workflow_run` schedules and release triage, which is all §3 needed.

## Fixes on this branch

- `fix(mcp): say why pull_request_status shows no review-thread state`: `pkg/mcp/pullrequest_review_tools.go` plus its test. It also adds this run's fake GHES, drivers and recorded fixture under `docs/test-runs/2026-11-02-github-ghes-release-train/`.
- `test(ui): pin the external PR chip in dark mode and its removal on a repo move`: `ui/e2e/external-pr-chip-walkthrough.spec.js`.

Local runs: `go test ./pkg/mcp/` ok. The two chip specs passed 18/18 with `--repeat-each=3`. Prettier is clean.

## Follow-ups filed (each depends on task 2451)

1. `release-from-trunk.sh: DRY_RUN=true leaves local rc tags that change the next dry run` (F2). Held for a person: `scripts/*`.
2. `Give github_workflow_run schedule reviews a GitHub Actions prompt, not the grain CI one` (F3).
3. `A GitHub Enterprise Server set as -github-host gets REST at the root and reports branches as unpushed` (F4).
4. `Wording nits from the 11-02 GitHub walkthrough: resolve tool name, get_ci_log header, doubled close reason` (F5).

No follow-up was filed for the red nightly (`go, shuffled and repeated` failing 3 nights running). It's the nightly review's to triage, and it's why the train has nothing to cut.

## Not run

- **The daemon's own reconcile loop against the fake GHES.** A daemon reaches review threads and the merge queue only for a task with an open PR, and that needs a sandbox backend and an agent run. This sandbox has no Docker or Granule run host and no agent credentials. The calls went through the daemon's forked `grain mcpserver` and through `CredentialSet.ForgeTransportOn`, the transport `forgeRouter` uses for a repository off `-github-host`. A forced 401 "in the UI" wasn't seen for the same reason: the UI shows these errors only off a run.
- **The 401 from an ambient/command-backed GHES credential** (`gh api graphql`). There's no `gh` in the sandbox.
- **`release-train.yml` itself**: its `actions: read` grant, and its refusal to run live without `GRAIN_API_URL`, `GRAIN_API_TOKEN` and `RELEASE_STAGING_URL`. Running the workflow would publish, so it wasn't triggered.
- **A run-scoped agent token through the agent-API gate** for `api:ci-read@acme/widgets`. The mcpserver authenticated as the operator; its client-side scope check was exercised.
- **Looking at the PNGs directly.** No renderer and no Gemini key. The DOM and colour measurements above stand in for it.
