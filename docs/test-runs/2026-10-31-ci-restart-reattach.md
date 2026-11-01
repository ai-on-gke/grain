# CI across a daemon restart: step reattach, channel-lost re-runs, 503 while CI starts

- **Plan:** `docs/test-plans/2026-10-30.md` §4 (PRs #128, #155 `4b741c74`, #162 `891415d8`, #190 `eab9750f`/`7b27ecf0`/`f32fa609`)
- **Date:** 2026-10-31, 22:15–23:00 UTC
- **Task:** `grain/049f1e09/task-2013` on `ai-on-gke/grain`, base `main` at `70036e3f` (origin: [Task #1899](/tasks/1899))
- **Environment:**
  - Debian 12 sandbox (`granule` backend), `linux/amd64`, 7 vCPU / 15.6 GiB, 29.4 GiB disk
  - Go `go1.26.10`; `cd ui && npm ci && npm run build`, then `go build -o /home/debian/bin/grain ./cmd/grain` (rebuilt after the fix)
  - `sqlite3` and `bc` from apt; Chromium from `/var/cache/playwright` via the `playwright` package in `ui/node_modules`
  - **Harness reused from the 2026-10-29 §9 walk** (`docs/test-runs/2026-10-29-grit-backend-ci-shutdown.md`): a ~30-line Go wrapper around `git http-backend` on `127.0.0.1:4250` serving a bare repository as the deployment's GitHub, `setsid`-started processes stopped by pid, and the same `curl` calls to add the repo, switch it to grain CI and set a token. Its shutdown-under-load measurements (10 jobs, 0.06 s exit, adoption of every job) are not repeated here.

The repository is served as **`ai-on-gke/grain`**, because an api grant is honoured only for grain's own repository: `ciapi.Config{}` is always empty in `pkg/daemon/capabilities.go`, so `model.GrainSourceRepo()` is the only repository whose jobs may hold one. Its `.grain/workflows/ci.yml`:

- `on:` `push`, `pull_request`, `workflow_dispatch`
- `slow`: `capabilities: []`, `cache: false`, one step `echo start; sleep 60; echo done`
- `gate`: `capabilities: [release-gate-read]`, `cache: false`, one step that sources `/home/debian/.grain-api/env` and curls `$GRAIN_API_URL/api/ci/runs?ref=main&limit=5`

Daemon (always an absolute `-data-dir`, which avoids the walker's F6):

```sh
grain daemon -data-dir /home/debian/wt/d -sandbox-dir /home/debian/wt/sbx -ui-addr 127.0.0.1:7811 \
  -sandbox-port-base=19712 -sandbox-backend host -github-host 127.0.0.1:4250 -github-insecure-http
```

`stop.sh` sends `SIGTERM` to the daemon's pid and polls `kill -0` every 10 ms to time the exit. `poll.sh` (every 100 ms) and `fastpoll.sh` (back-to-back) record `GET /api/ci/runs/<id>` status codes with timestamps. The store is `d/store/grain.db`, not `d/grain.db`.

## Outcome

| §4 "not proved" item | Result |
|---|---|
| 1a. SIGTERM mid-step on the default backend, restart | **Pass.** SIGTERM 25 s into `slow`'s `sleep 60`; the daemon exited in **0.118 s** (`shutdown interrupted 0 run(s)`); the `sleep` kept running. Restart: `ci: adoption: 1 job(s) adopted across 1 run(s), 0 lost (0 run(s) marked interrupted)`, `job …:slow: resumed in sandbox c9137f7-1`, then `success in 1m0s` with the step's whole output (`start\ndone`). `ci_step_attachment` was **empty** before, during and after: the table (schema 64) is written only by the dial-out runner, so the host backend went through the old readoption path, which still works after `RecoverUnattachable`. |
| 1b. Killed before any sandbox exists | **Pass.** With free disk under the 5 GiB floor (an 18.5 GB `fallocate` file), run `61fa658f3f495ed1` was held (`job …/gate is waiting -- for room in this deployment's sandbox budget -- disk: 4.7 GiB is free`). SIGTERM: exit in **0.025 s**. After the restart (disk freed) the run read `conclusion: cancelled`, `interrupted: true`, `this run was interrupted: the grain daemon running it stopped before it finished, so its jobs were never concluded`. In the same restart a concurrent `slow` run was adopted and finished `success`. It was a `main` dispatch, not a task branch with a pull request, so there was nothing for the reconciler to re-run. |
| 2. Waiter across the restart (`grain ci wait --resume` through the git proxy, `GET /api/ci/runs/<id>`) | **Pass; never a 404.** Through a real task sandbox's proxy credential (see step 3): a `grain ci wait --resume 7cde2d4afbe4e849` blocked at SIGTERM returned **6 ms after the signal** with `CI run 7cde2d4afbe4e849 is still running after 3s … To keep waiting in this run: grain ci wait --resume 7cde2d4afbe4e849` (exit 0). The daemon exited in 0.059 s. After the restart the same `--resume` returned the real verdict (`grain CI failed on 86916e2434a3 (run 7cde2d4afbe4e849, failure …) Failing: gate (failure) Passed: slow`). `GET /api/ci/runs/<id>` across three restarts: only `200` and connection refused (`000`). **No 404 and no 503 was ever observed**: on this store, `LiveCI` is published in the same second the UI listener starts (`serving the UI/API` and `grain CI is on` are both logged at `22:24:16`), and 1,042 back-to-back GETs did not land in the gap. |
| 3. `/ci` and a task's CI panel while CI is not up | **Fail → fixed (F1).** `GET /api/ci/runs` (the list `/ci` reads) answered **200 `{"enabled":true,"runs":[],…}`** while CI was not up, while `GET /api/ci/runs/{id}` beside it answered 503. In Chromium a fresh `/ci` load showed **"No CI runs yet. Start a run above…"**, and an already-open `/ci` replaced its table with the same message at its next 5 s poll. After the fix the list answers the same 503. A fresh load now shows *"CI runs could not be listed (this daemon's CI service is not up yet: its reconcile loop has not started (or has failed -- check the daemon log)). This pane tries again every few seconds."*, and an open page keeps its rows under *"The latest read of CI runs failed (…not up yet…), so the list below is from the last one that succeeded."*. The task's CI panel does not read the list and is unaffected (but see U2). |
| 4. api-grant job on a daemon with `-sandbox-port-base` | **Pass (grant resolves); host-backend path finding U1.** The CI API listener bound **127.0.0.1:19716** (base 19712 + offset 4). A `workflow_dispatch` run's `gate` job (`capabilities: [release-gate-read]`) got `GRAIN_API_URL=http://127.0.0.1:19716` and a token, and `GET $GRAIN_API_URL/api/ci/runs?ref=main&limit=5` answered **200**. The documented `. /home/debian/.grain-api/env` failed on the host backend, though (`No such file or directory`): the placement is written under the run's root, `<root>/home/debian/.grain-api/env`, and the step had to `find` it (U1). |
| 5. Dial-out on a real cluster | **Not walked.** kind v0.32.0, kubectl and a working docker engine are in the sandbox. But `-k8s-granule-access=dialout` refuses to start outside a pod, so grain has to run in-cluster as `deploy/kubernetes/grain-granule.yaml`, whose image is the retired `ghcr.io/bwsalmon/grain/grain:main`. `-k8s-granule-sandboxes` also requires `-k8s-granule-image`, the granule wrapper image built from `gke-labs/granule`, which this sandbox cannot read (no grant; `git ls-remote` asks for credentials). Dial-out stays covered by `pkg/sandboxaccess/restart_test.go` over a real acceptor. |

Shutdown timings: 0.119 s (idle), 0.118 s (a step running), 0.059 s (a waiter blocked in `/cli/ci/wait` and an agent run in flight), 0.130 s and 0.143 s (later restarts), 0.025 s (a held run, no sandbox). Each one sits within the 10 s bound, matching the §9 walk's 0.06 s.

## Steps and observations

### 1. Restart mid-step (host backend)

1. Repo added, `ciMode: grain`, token set; the token wanted a restart (`needsRestart: true`), so the first, idle restart was also timed: 0.119 s.
2. `POST /api/ci/runs {"event":"workflow_dispatch","recheck":true}` on `main` gave run `9137f73977ee9aca` with `slow` and `gate` (gate: U1).
3. SIGTERM at 22:18:43.235, 25 s into the sleep; exit after 0.118 s; one `sleep 60` still running. `select * from ci_step_attachment` returned no rows (the schema is present).
4. Restart at 22:18:52: `adoption: 1 job(s) adopted … resumed in sandbox c9137f7-1`; `slow: success in 1m0s` at 22:19:18; run `failure` because of `gate`.
5. `poll.sh` over that window: 42 × `200`, 83 × `000` (9.5 s, nothing listening), 109 × `200`.

### 2. The waiter, through a real task sandbox

grain's `custom` agent framework stands in for an agent (`docs/custom-agent-backends.md`). `fake-agent` reads the request, records `working_directory`, commits a line and `git push`es through the sandbox's proxy, then stays alive so the run and its proxy token stay live. `PUT /api/settings {"agentBackends":[{"id":"fake","framework":"custom","command":"/home/debian/wt/fake-agent","models":["fake-model"]}]}`, then a task for `ai-on-gke/grain`, approved.

1. Task 2 dispatched into `sbx/runs/2-1` (`.git-credentials` → `http://sandbox:…@127.0.0.1%3a19712`) and pushed `grain/52b8750c/task-2-walk-ci-wait`.
2. With `HOME=<sandbox>`: `grain ci wait --timeout 15s` started run `7cde2d4afbe4e849` and answered `still running … grain ci wait --resume 7cde2d4afbe4e849`.
3. `grain ci wait --resume 7cde2d4afbe4e849` in the background; SIGTERM 3 s later (22:22:22.809). The waiter answered `running` with the resume id at 22:22:22.815, and the daemon was gone 0.059 s after the signal.
4. A second waiter started 0.5 s after the exit answered `forbidden: grain ci: that tool belongs to the "grain-ci" capability`. That is a **harness artefact, not a grain fault**. On the host backend the shutdown finished the agent run as interrupted and removed `sbx/runs/2-1`, credentials and all. With that `HOME` gone, `pkg/sandboxcli` fell back to `/home/debian/.git-credentials`, which is *this* task's own sandbox proxy. The real deployment answered it, and this task no longer holds grain-ci ([Task #1933](/tasks/1933) removed it). Reproduced deterministically (daemon down, same `HOME`: same answer in 28 ms).
5. Restart at 22:22:28: `adoption: 1 job(s) adopted`, `slow: success in 1m0s`; `--resume` then returned the verdict (table above). The new daemon re-dispatched task 2 (attempts 2–4, each `interrupted` at the next restart, `none counts against its task`): on the host backend an agent run does not outlive the daemon, as before this range.
6. A wait with no restart in it (attempt 4's sandbox, run `dc48d12a932f9726`) answered `grain CI failed on 0868cab436bf … waited 1m5s`.

### 3. The 503 window and the UI

1. Starting order (`d5.log`): `serving the UI/API` → `git proxy is serving` → three `ci:` lines → `grain CI is on`, all in 22:24:16. `fastpoll.sh` on `/api/ci/runs/7cde…` and `/api/ci/runs?repo=…` over that restart: 103 × `000`, then 939 and 921 × `200`. No `503`, no `404`.
2. The window is real but under a millisecond-scale poll on a store this size ("a minute or more" on a restart that adopts a lot, per `ci.ErrNotUp`'s comment). What `/api/ci/runs` answers inside it comes from `CiGate.Find`, which returns an empty `ci.RunPage` while `LiveCI` is nil. `TestCIRunListIsUnavailableNotEmptyWhileCIIsNotUp` drives `handleListCIRuns` with the package's `notUpRunner`. Before the fix it fails: `GET /api/ci/runs status = 200, want 503: {"enabled":true,"runs":[],"matched":0,"stored":0}`.
3. Chromium against the real daemon (bearer access code; desktop 1440×900 and phone 390×844). Only `GET /api/ci/runs` was intercepted, answered first with the unfixed daemon's not-up body above and then with the fixed daemon's 503 `{"error":"this daemon's CI service is not up yet: …"}`. Every other request went to the daemon. Pages: `/ci` and `/tasks/2`, plus an open `/ci` whose answers switch to not-up and are polled for 12 s. The alert texts are in the table above; there are no `ui/` changes, so there are no layout changes to screenshot.
4. Task 2's panel read **"grain CI is running against this task's branch (0868cab436bf)"** for run `dc48d12a…`, which had concluded `failure`, and likewise for `7cde…` before it (U2).

### 4. The api-grant job

1. `ss -ltnp`: grain holds 19712 (git proxy), 19713 (package cache), 19714 (Actions cache), 19715 (capability ports), **19716 (CI API)**, 19728–19730 (model proxies).
2. Run `0ff4ea313874190a` (`workflow_dispatch`, `gate` only), whose step falls back to `find` for the env file: `HOME=/home/debian/wt/sbx/runs/c0ff4ea-0`, `env file: /home/debian/wt/sbx/runs/c0ff4ea-0/home/debian/.grain-api/env`, `url=http://127.0.0.1:19716`, `200`; job `success`.
3. On a push run of the task branch, `gate` failed, as the release-only event gate intends.

## Findings, fixes and follow-ups

| # | Finding | Status |
|---|---|---|
| F1 | `GET /api/ci/runs` answered 200 with an empty list while the daemon's CI service was not up, so `/ci` said "No CI runs yet" and blanked an open table across every restart; `GET /api/ci/runs/{id}` already answered 503. | **Fixed** (`pkg/ui/ci.go`, `handleListCIRuns`): `!ci.IsUp(runner)` answers `writeCINotUp`. Test `TestCIRunListIsUnavailableNotEmptyWhileCIIsNotUp` (`pkg/ui/ci_notup_test.go`). |
| U1 | On the host sandbox backend a capability placement lands at `<run root>` + its path (`orchestrator.applyPlacements`), and a step's `HOME` is the run root. So `/home/debian/.grain-api/env`, the path `docs/ci-capabilities.md` and `ciapi.Dir` give, does not exist for a host-backend CI job. The same holds for every other placement path (`/home/debian/.gemini-api-key`, …). | Follow-up (filed). |
| U2 | A task's recorded CI verdict (`task.ci`) gets its conclusion only from `orchestrator.reconcileTaskCI`, which runs for tasks with an open pull request (`OpenPullRequestLinks`). A run a task starts before it has one (`grain ci wait` in a fresh run) stays "grain CI is running" on the task page after it has concluded. This is independent of restarts. | Follow-up (filed). |
| U3 | The 503 window was too short to land in on this store, so the UI's not-up state was rendered with the list answer intercepted, not caught live. | Noted. A live catch needs a restart that adopts many runs. |
| U4 | Dial-out on a cluster not walked (item 5). | Needs a cluster with granule images, as above. |
