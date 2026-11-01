# grit backend (`-grit-url`) and CI during daemon shutdown

- **Plan:** `docs/test-plans/2026-10-29.md` §9 (PRs #93, #89, #96, #94)
- **Date:** 2026-10-29, 14:06–15:00 UTC
- **Task:** `grain/049f1e09/task-1803` on `ai-on-gke/grain`, base `main` at `d71aa7e4` (origin: [Task #1753](/tasks/1753))
- **Environment:**
  - Debian 12 sandbox (`kubernetes-granule`), kernel `6.1.0-54-amd64`, `linux/amd64`, 7 vCPU / 15 GiB
  - Go `go1.26.10`, git 2.39.5, Python 3.11.2
  - `go build -o /home/debian/bin/grain ./cmd/grain` and `go build -o /home/debian/bin/grit ./cmd/grit` from this branch, rebuilt after each fix
  - Working directory `/home/debian/wt`; every process started with `setsid` and stopped by pid (helper `ctl.sh`)
  - A local repository `acme/widgets` (`git init`, `.grain/workflows/ci.yml`), served as the deployment's GitHub by a 30-line Go wrapper around `git http-backend` on `127.0.0.1:4250`
  - `tamper_proxy.py`: a Python reverse proxy on `127.0.0.1:4245` in front of `grit serve` with modes `none`, `sig` (flip a byte of the DSSE signature), `payload` (rewrite the signed statement's conclusions to `success`), `status` (rewrite only the unsigned `/v1/runs` and `/v1/events` bodies' `errored`/`failure` to `success`), `noatt` (503 on `/attestation`) and `hold` (503 on `GET /v1/runs*` and `/v1/events`)

Exact commands:

```sh
# grit and its client token
grit client add -data-dir grit-data -id grain -scope 'submit:*,read:*,cancel:*' > grit.token
grit serve -data-dir grit-data -listen 127.0.0.1:4244
python3 tamper_proxy.py 4245 4244 <mode>

# daemon over grit (part 1)
grain daemon -data-dir d1 -sandbox-dir /home/debian/wt/sbx -ui-addr 127.0.0.1:7811 -sandbox-port-base=0 \
  -sandbox-backend host -grit-url http://127.0.0.1:4245 -grit-client-token "$(cat grit.token)"

# daemon with embedded CI (part 2)
grain daemon -data-dir d2 -sandbox-dir /home/debian/wt/sbx2 -ui-addr 127.0.0.1:7811 -sandbox-port-base=0 \
  -sandbox-backend host -github-host 127.0.0.1:4250 -github-insecure-http

# both: add the repo, switch it to grain CI, start runs, read them back
curl -XPOST  $GRAIN_SERVER/api/repos -d '{"repo":"acme/widgets"}'
curl -XPUT   $GRAIN_SERVER/api/repos/acme/widgets/ci-mode -d '{"ciMode":"grain"}'
curl -XPUT   $GRAIN_SERVER/api/github-tokens/default -d '{"value":"ghp_..."}'     # part 2 only
curl -XPOST  $GRAIN_SERVER/api/ci/runs -d '{"repo":"acme/widgets","ref":"main","commit":"<sha>","recheck":true}'
grain ci list; grain ci show <run>
```

(All `curl` calls carry `Authorization: Bearer $(cat <data-dir>/local-api-token)`.)

## Outcome

| Step | Result |
|---|---|
| 1. `grit serve` + `grain daemon -grit-url` running a repo's two-job workflow, verdict in `/api/ci/runs` (what `/ci` reads) and `grain ci` | **Fail → fixed to fail closed (F1); real execution is a follow-up.** A workflow whose `test` job runs `exit 1` came back **success** ("1 jobs, all green") from a placeholder job named `default`: the daemon uploads a metadata-only tree and grit serve's executor fabricated success for any tree without `.grain/grit-job.sh`. After F1 the run is recorded **errored** with "job default ... was not run". The workflow is still not run over `-grit-url` (follow-up). |
| 2a. Tampered attestation (signature byte, signed payload) | **Pass.** Both recorded `errored: attestation verification failed: attest: signature by "85fedc…" does not verify`. |
| 2b. Unsigned run status rewritten to success, attestation untouched | **Fail → fixed (F2).** Recorded **success** before the fix; now `errored: attestation verification failed: job default: run status reports "success", attestation signs "errored"`, and no job row keeps the unverified "success". |
| 2c. `grit` unreachable / restarting | **Pass after fix (F3)**, one follow-up. A submission made while grit was down stayed in the durable outbox and ran when grit came back (but the API answered `400` at the time — follow-up). A 503 on the attestation fetch used to record the run errored for good (F3); it now stays in flight and verifies after grit returns. |
| 2d. `grit` state reset mid-run (new epoch) and cursor resync | **Fail → fixed (F4).** The cursor resynced to the new epoch (`{"epoch":"e-487551c00a0d","afterSeq":0}`) but the in-flight run grit no longer knew stayed `running` for ever. It is now concluded `errored: grit no longer has run r-2c8697379167 (its state was reset): run it again`, on resync and on daemon start. |
| 2e. Reverse tunnel (`grit serve -grain-url`) | **Not available (follow-up), grit side fixed (F5).** No grain process mounts `/v1/session` (`gritapi.SessionHub` is only used in tests): the daemon answers 404/401. grit redialed every 100 ms in silence; it now logs the refusal once and backs off to 30 s. |
| 3a. `SIGTERM` with 10 running CI jobs (two runs, one job retrying) | **Pass.** The daemon exited **0.06 s** after `SIGTERM` (first try: same second, < 1 s), well inside the 10 s bound; `grain daemon: shutdown interrupted 0 run(s)`. Host-sandbox steps kept running detached. |
| 3b. Next daemon re-adopts and finishes the work | **Pass (with F6 applied locally; F6 is a follow-up).** `ci: adoption: 10 job(s) adopted across 2 run(s), 0 lost`; jobs `resumed in sandbox …`; the `flaky` job whose first attempt failed while no daemon was running had its automatic retry run by the new daemon; both runs concluded `failure` (flaky) with the four 60 s jobs `success`. Embedded CI did not run at all with a relative `-data-dir` before F6. |
| 3c. A sandbox blocked in `grain ci wait` resumes with `resume_run_id` | **Walked 2026-11-01 (addendum): pass after fixes F7, F8.** Both waiters answered `running` with the run to resume 4–6 ms after `SIGTERM`, and the restarted daemon's `--resume` waits reported the verdict. |

## Steps and observations

### 1. Two real processes: `grit serve` and `grain daemon -grit-url`

1. `grit serve` printed `grit serve listening on 127.0.0.1:4244 (epoch e-d89e7686f1a6, key 85fedc303dd04323e93fd094b1cb54e5)`; `/v1/info` with the `Grit-Client` token listed that key.
2. The daemon logged `grain daemon: grain CI is wired to remote grit server at http://127.0.0.1:4244`. `acme/widgets` was added and set to `ciMode: grain`.
3. The workflow had two jobs, `build` (`sleep 20`) and `test` (`exit 1`). `POST /api/ci/runs` with the commit returned `202` with run `r-2248c1956b84` and a single job `default`; 30 ms later the run was `success`, its one step's output `job default completed on acme/widgets@45bf7088…`. `grain ci list`: `r-2248c1956b84   success … 1 jobs, all green`. Nothing of the repository reached grit: the daemon builds no tree resolver, so `BackendRunner.resolveTree` uploads a tar holding only `.grain-tree-meta` (repo/ref/commit), and grit serve's local executor does not read `.grain/workflows` (GS-3, deferred by `docs/design/grit-split.md`). Note also that the daemon accepted a commit that exists in no repository it can reach. → **F1**.
4. After F1 the same request gives `r-0eac94811e29 errored … 1 jobs: 1 errored`.

### 2. Attestations, grit restarts, and the reverse tunnel

1. Through the proxy, `sig` and `payload` modes both gave `errored` with `attestation verification failed: attest: signature by "85fedc…" does not verify`. Pass.
2. `status` mode (unsigned `GET /v1/runs/{id}` and `/v1/events` bodies rewritten, envelope untouched) gave `r-b77636840339 success` — the runner checked the attestation's signature and tree/commit subject but recorded the conclusions from the unsigned status. → **F2**. After the fix: `r-63497a626e4c errored attestation verification failed: job default: run status reports "success", attestation signs "errored"`.
3. Attestation keys come from grit's own `/v1/info` over the same connection (the daemon exposes no flag for `BackendRunnerConfig.VerifyKeys`), so anything able to rewrite responses could also substitute the key. Follow-up.
4. grit stopped, run submitted: `POST /api/ci/runs` → `400 {"error":"Post \"http://127.0.0.1:4245/v1/trees:negotiate\": EOF"}`, yet `d1/state/grit-outbox.json` held the `start` entry and, once grit was back, the consumer flushed it and run `r-1b46fa3afe0c` was recorded. The caller is told the run failed to start when it is only pending, and a caller that retries a `recheck` gets a second run. Follow-up.
5. `noatt` mode: `r-94bed83e37e2 errored attestation verification failed: fetching attestation for run r-94bed83e37e2: grit api (503 http_error)` — a transient error recorded as the verdict. → **F3**. After the fix the run stays in flight while the 503s last and is recorded with its real verdict once they stop (`r-c2dd8617263d`).
6. `hold` mode, run `r-2c8697379167` submitted (tracked, not concluded); grit stopped, its data directory moved aside (key kept), a new client token minted, grit restarted (`epoch e-487551c00a0d`), daemon restarted with the new token. The cursor moved to the new epoch; `GET /v1/runs/r-2c8697379167` is 404 on the new grit; the run stayed `running … 1 jobs: 1 waiting` indefinitely. → **F4**. After the fix it is concluded at the daemon's next start: `errored grit no longer has run r-2c8697379167 (its state was reset): run it again`.
7. `grit serve -grain-url http://127.0.0.1:7811 -client-token …`: the daemon has no `/v1/session` route (`curl …/v1/session` → 404 with a valid bearer token; grit's own dial, carrying `Grit-Client`, is turned away at the access-code wall with 401). grit printed nothing and redialed every 100 ms. → **F5** (grit side). Now: `grit serve: reverse session to http://127.0.0.1:7811: grit api (401 http_error): <!doctype html> … (retrying, backing off to 30s)`, once.

### 3. Embedded CI and `SIGTERM` under load

1. First start with `-data-dir d2` (relative): every run failed `ci: reading acme/widgets's workflows at main: resolving acme/widgets at main: git rev-parse: chdir d2/state/grain-ci-workflows-2728195952/repo: no such file or directory`. → **F6**.
2. With the fix applied locally (later reverted on this branch, see F6), a workflow with an `on:` and five jobs (`a`–`d`: `sleep 60`; `flaky`: `sleep 5; exit 1` with `retries: 3`) ran in host sandboxes after a git token was set (`this deployment has no git connection covering acme/widgets` before that). `flaky` failed, retried once in a fresh sandbox, failed definitively.
3. `SIGTERM` with run `4d4b44c1cea6cb80` holding four running jobs: exit within the same second, `shutdown interrupted 0 run(s)`, the four `sleep 60` steps still running. Restart: `ci: adoption: 4 job(s) adopted across 1 run(s), 0 lost (0 run(s) marked interrupted)`, `job …:a: resumed in sandbox c4d4b44-0` (×4), all four `success in 1m0s`, run `failure` (flaky).
4. Two runs (`main`, `b2`), 10 jobs running, `SIGTERM` 4.3 s in, just before `flaky`'s first attempt failed: **daemon exited 0.06 s after SIGTERM**. Restart ~2 min later: `ci: adoption: 10 job(s) adopted across 2 run(s), 0 lost`; `flaky: initial attempt failed …, automatically retrying once in fresh sandbox` was logged by the *new* daemon, which ran the retry; both runs finished `5 jobs: 1 failure, 4 success`. A second `POST` for an identical request during the first run was answered `already running as 4d4b44c1cea6cb80`, so no run was ever purely queued in this walk; every job was admitted at once on this 7-vCPU host.
5. The `grain ci wait` / `run-job` waiter was not walked: it needs a dispatched task's sandbox and its proxy token, which this walk did not set up.

## Findings, fixes and follow-ups

| # | Finding | Status |
|---|---|---|
| F1 | `grit serve`'s default executor reported **success** (and signed it) for every job of a tree without `.grain/grit-job.sh`; with the daemon's metadata-only tree, every `-grit-url` run was green whatever its workflows said. | **Fixed** (`pkg/grit/local.go`): such a job concludes `errored` with a `no_executor` fault and a log line saying it was not run. Tests `TestDefaultExecutorNeverPassesAJobItDidNotRun`, `TestDefaultExecutorRunsGritJobScript`. |
| F2 | `BackendRunner` recorded conclusions from the unsigned run status, not the verified attestation. | **Fixed** (`pkg/ci/backend_runner.go`, `checkAttestedVerdict`): run id, job set and every job conclusion must match the signed statement; on any verification failure no job row keeps a "success". Tests `TestBackendRunnerRecordsOnlyTheAttestedVerdict`, `TestCheckAttestedVerdict`. |
| F3 | A transient failure fetching the attestation or keys (503, connection refused) was recorded as the run's permanent `errored` verdict. | **Fixed**: transport errors, 429 and 5xx leave the run in flight for the next sync. Test `TestBackendRunnerRetriesATransientAttestationFetch`. |
| F4 | A run grit lost (state reset, 404) stayed `running` in grain for ever after the epoch resync. | **Fixed**: concluded errored on resync and in a one-off pass at consumer start. Tests `TestBackendRunnerConcludesARunGritLost`, `TestBackendRunnerConcludesARunLostWhileNoDaemonWatched`. |
| F5 | `grit serve -grain-url` redialed every 100 ms and logged nothing when grain refused the session. | **Fixed** (`cmd/grit/serve.go`, `dialGrainLoop`): logs once per distinct failure, on one line, backs off 100 ms → 30 s. Test `TestDialGrainLoopLogsAndBacksOff`. |
| F6 | A relative `-data-dir` broke every embedded CI run (workflow clone run from another directory). | **Follow-up.** A fix (`absDataDir` at the top of `daemon.Run`, with a test) was walked live here but reverted on this branch: with it the `installer` job's `TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode` hung past the 15m bound twice while passing elsewhere. The same test then hung the same way on this branch *without* the fix (run `c5e43992379df447`), and `installer` failed or timed out on several other branches and the merge queue that afternoon, so the fix was most likely not the cause; re-landing it (commit `6896829e`) is the follow-up. Workaround until then: pass an absolute `-data-dir`. |
| U1 | `-grit-url` cannot run a repository's workflows: no tree resolver (only `.grain-tree-meta` is uploaded), no `.grain/workflows` executor in grit serve, no per-request secrets resolver. | Follow-up (held for a decision: GS-3 is deferred, so either finish it or mark `-grit-url` experimental/refuse it). |
| U2 | No grain process serves `/v1/session`, so reverse-tunnel mode cannot work end to end. | Same follow-up. |
| U3 | Attestation keys are fetched from grit's own `/v1/info`; there is no way to pin them on the daemon. | Same follow-up. |
| U4 | A submission made while grit is unreachable returns `400` but is delivered later from the outbox. | Same follow-up. |
| U5 | Shutdown waiter (`grain ci wait` / `run-job` returning `running` + `resume_run_id`) not walked live. | **Walked** 2026-11-01, see the addendum (F7, F8 fixed; U6 follow-up). |

## Addendum (2026-11-01): the shutdown waiter, walked live (U5)

- **Task:** `grain/049f1e09/task-1830` on `ai-on-gke/grain`, base `main` at `8688c0cf` (origin: [Task #1753](/tasks/1753), filed by [Task #1803](/tasks/1803))
- **Environment:** Debian 12 sandbox (`granule`), 7 vCPU, Go `go1.26.10`, git 2.39.5; `go build -o ~/bin/grain ./cmd/grain` from this branch, rebuilt after each fix; working directory `~/wt`.
- **Rig:**
  - `acme/widgets` is a bare repository served by a 20-line Go wrapper (`net/http/cgi` around `git http-backend`, `http.receivepack true`) on `127.0.0.1:4250`. Its `.grain/workflows/ci.yml` has two jobs, `a` and `b`, each `sleep 60`.
  - The stub agent follows `newStubAgent` (`tests/e2e/upgrade_drain_interrupted_test.go`) and is passed to the daemon as `-claude-path`. Its model connection is `grain connections import` of a `claude` / `claude-sonnet-5` entry.
  - On its first start, the stub pushes an empty commit to the task's branch, then runs `grain ci run-job b` and, 3 s later, `grain ci wait`. On a later start it runs `grain ci wait --resume <id>` and `grain ci run-job b --resume <id>` with the ids those printed. Each writes timestamps and output to a file.

Exact commands (all `curl` calls carry `Authorization: Bearer $(cat d/local-api-token)`):

```sh
githost 127.0.0.1:4250 ~/wt/git
grain daemon -data-dir ~/wt/d -sandbox-dir ~/wt/sbx -ui-addr 127.0.0.1:7811 -sandbox-port-base=0 \
  -sandbox-backend host -github-host 127.0.0.1:4250 -github-insecure-http \
  -claude-path ~/wt/agent/claude -max-workers 2
grain connections -data-dir ~/wt/d import conn.yaml          # daemon stopped; after its first start
curl -XPOST $GRAIN_SERVER/api/repos -d '{"repo":"acme/widgets"}'
curl -XPUT  $GRAIN_SERVER/api/repos/acme/widgets/ci-mode -d '{"ciMode":"grain"}'
curl -XPUT  $GRAIN_SERVER/api/github-tokens/default -d '{"value":"ghp_stub"}'   # then restart: needsRestart
curl -XPOST $GRAIN_SERVER/api/tasks -d '{"title":"wait across a restart","description":"…","repo":"acme/widgets","approved":true}'
kill -TERM <daemon pid>      # ~13 s into the waits; timed until the pid is gone
```

Notes on the rig:

- `grain-ci` cannot be attached to a task by hand (`POST /api/tasks` with `capabilities: ["grain-ci"]` is refused). It comes from the repository's CI mode.
- With host sandboxes the agent CLI runs in the daemon's working directory with the operator's `HOME`, so the stub sets `HOME` to its run's sandbox root (`sbx/runs/<run>/`, where that run's `.git-credentials` names its own proxy) before calling `grain`.
  - The first try did not, and its `grain` reached the git proxy of the sandbox this walk ran in. That proxy refused one call and answered the other "branch does not exist".
  - The e2e test below fails closed instead.
- The daemon stops an interrupted run's whole process tree, and with host sandboxes the stub's `grain ci wait` is in it. In the first try it was killed before it could print anything.
  - On a VM sandbox `run_command` runs in the VM, which the daemon's stop does not reach.
  - So the stub starts each waiter with `setsid`, standing in for that, and every result below is from runs that did.

### Results

| Check | Result |
|---|---|
| (1) The waiter gets `running` with `resume_run_id` at once, not after the 10 s server bound | **Pass.** `SIGTERM` at `…367.0769`; `grain ci wait` printed `CI run 569dfc4ba0453cc4 is still running after 13s (jobs running: a, b …)` / `grain ci wait --resume 569dfc4ba0453cc4` at `…367.0824`, **5.4 ms** later. The daemon exited in 0.07 s (`shutdown interrupted 1 run(s): 1 finished as interrupted`). With `run-job` and `wait` both blocked (after F7): **4.2 ms** and **5.6 ms**, both naming `47a516bc22e49de8`. |
| (2) After restart a new `grain ci wait` with that id resumes and reports the verdict | **Pass.** The restart re-ran the interrupted task (attempt 2) and logged `ci: adoption: 2 job(s) adopted across 1 run(s), 0 lost`. `grain ci wait --resume 569dfc4ba0453cc4` blocked 35 s and printed `grain CI passed on af92d7bc90c8 (run 569dfc4ba0453cc4, waited 35s; wait credit used: 35s / 45m0s): a, b`. After F7, `grain ci run-job b --resume 47a516bc22e49de8` printed `grain CI passed … : b` and the selective-run reminder. |
| `grain ci run-job b`, then `grain ci wait` | **Fail → fixed (F7).** The full run the wait started superseded the run-job's selective run (`cancelled: superseded by run 20323a65b120b54b, started for the same task`, by design). The run-job printed `grain CI failed on a7de7871a3f6 (run 049f5944d672293a, cancelled, waited 5s …)` / `Failing: b (cancelled)` 5 s in, while `b` went on to run and pass in the full run. |
| (3) Wait credit is not double-counted | **Fail → fixed (F8).** Sequential waits were fine. Run `2-1` credited 13 s for its one 13 s wait, and its `--resume` in run `2-2` credited 35 s for 35 s. Waits blocked *side by side* were each credited, though: `run-job` and `wait` both blocked for the same 35 s, and the resumed run reported `wait credit used: 1m10s` and `1m5s`. After F8 the same walk credited **16 s** before the stop (waits of 16 s and 13 s, overlapping) and **45 s** after it (both waited 45 s). The store holds `task_run_wait_credit` `2-1 | 15528 | 15528` for the interrupted run, and `task_run_check_wait` holds `2-1 timed_out 12528/15528` and `2-2 passed 45001` ×2. |

### Findings

| # | Finding | Status |
|---|---|---|
| F7 | The proxy waiter behind `grain ci wait` / `run-job` (`pkg/gitproxy/cli_actions.go`) reported a run grain had cancelled to make way for a newer one for the same task as the verdict. `wait_for_checks` already follows such a run (`pkg/mcp`'s `followSupersede`). | **Fixed.** `followSuperseded` follows chains of superseding runs, staying on the same commit and the task's own branch. A selective wait follows only into a run that runs every job it asked for, and is then answered by those jobs alone (`waitSettled`): it passes or fails as soon as they finish, whatever the rest of the run does. Test `TestCLIWaitFollowsTheRunThatSupersededIt`. |
| F8 | The per-run CLI wait ledger summed every wait's blocked intervals. Overlapping waits were each credited for the same wall-clock time, so a run that backgrounded *n* waits earned *n*× credit, up to the 45 m cap. | **Fixed.** `cliWaitLedger.add` credits only the part of each reported interval past the latest instant the run's total covers. The total is therefore the time at least one wait was blocked. Tests `TestCLIWaitCreditCountsOverlappingWaitsOnce` (2.0 s credited for 1.0 s before the fix) and `TestCLIWaitCreditIsCountedOnceAcrossAResume`. |
| U6 | Found by reading the code, not walked: walking it needs a run that survives the restart (`-claude-run detached` through hostd). A run the next daemon **adopts** gets a fresh clock (`newAdoptedRunClock`) that holds only the restart gap. The proxy's CLI wait ledger is in memory too, so CLI wait credit earned before the restart is dropped. A run that spent most of its budget in `grain ci wait` before an upgrade could be cancelled as timed out at adoption. | Follow-up: persist the per-source ledger for runs left for adoption. |
| O1 | A waiter answered during shutdown can omit `wait credit used`: by then the run's clock may already be cancelled (`Live: false`). It was omitted in one of the three walks. The credit is still recorded (`task_run_wait_credit` above). | Cosmetic; not changed. |

The walk is kept as `tests/e2e/ci_wait_shutdown_walkthrough_test.go` (`TestACIWaitAcrossADaemonRestartResumesToTheVerdict`, ~32 s, two `sleep 20` jobs). It uses the same rig in-process: `daemon.Run` with `Ci` and `HostSandboxes`, a cancelled ctx for `SIGTERM`, and the real `grain` CLI. It checks (1)–(3) and fails on the credit check with F8 reverted.
