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
| 3c. A sandbox blocked in `grain ci wait` resumes with `resume_run_id` | **Not walked live.** Needs a dispatched task run and its sandbox proxy; covered only by `pkg/gitproxy/cli_actions_test.go` (follow-up). |

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
| F6 | A relative `-data-dir` broke every embedded CI run (workflow clone run from another directory). | **Follow-up.** A fix (`absDataDir` at the top of `daemon.Run`, with a test) was walked live here but reverted on this branch: with it the `installer` job's `TestGoogleTrackRunsAgyThroughHostdAndMigratesFromHostMode` hung past the 15m bound twice while passing elsewhere, and there was no time left to tell cause from cluster load. Workaround: pass an absolute `-data-dir`. |
| U1 | `-grit-url` cannot run a repository's workflows: no tree resolver (only `.grain-tree-meta` is uploaded), no `.grain/workflows` executor in grit serve, no per-request secrets resolver. | Follow-up (held for a decision: GS-3 is deferred, so either finish it or mark `-grit-url` experimental/refuse it). |
| U2 | No grain process serves `/v1/session`, so reverse-tunnel mode cannot work end to end. | Same follow-up. |
| U3 | Attestation keys are fetched from grit's own `/v1/info`; there is no way to pin them on the daemon. | Same follow-up. |
| U4 | A submission made while grit is unreachable returns `400` but is delivered later from the outbox. | Same follow-up. |
| U5 | Shutdown waiter (`grain ci wait` / `run-job` returning `running` + `resume_run_id`) not walked live. | Follow-up walkthrough. |
