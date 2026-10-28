# 2026-10-07 — `main`: grain CI tools and `grain demo -github-sim` (test plan 2026-10-06 §10)

Task 2388, filed by task 2231 (nightly test plan). This walkthrough covers `docs/test-plans/2026-10-06.md` §10.

## Environment

- Branch `grain/task-2388`, cut from **`main` at `7819e615d`** (`Merge pull request #3160 from bwsalmon/grain/task-2468`).
- `git branch -r --contains` confirms that all five commits under test are on `origin/main`: `c5b75c1a3`, `5fa2c6762`, `e0a1b447b`, `5115be820` and `27af676af`. None of them is on `origin/candidate`, so this run targets `main`, as the plan says.
- Debian sandbox, with Go from the image, Node/npm from `/usr/local/bin`, and Playwright browsers from `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH`). The UI was built with `cd ui && npm ci && npm run build`, so the specs ran against the production bundle in `pkg/ui/static`.
- Two notes on the plan as written:
  - There is no `-sandbox-budget` flag. The budget is `model.Budget` (host shape, reserve and overcommit), and the walkthrough sets it directly to a 3 GiB / 8 vCPU host with overcommit 1.
  - The `cmd/grain/ci_stuck_test.go` that the plan lists is now `pkg/daemon/ci_stuck_test.go`, since the daemon moved into packages (§7).

## Commands

```sh
go test ./tests/e2e/ -run TestCIToolsWalkthrough -count=1 -v      # the new walkthrough
go test ./pkg/ci/ ./pkg/ui/ ./pkg/mcp/ -count=1                   # unit regressions
go test ./pkg/model/... ./pkg/ci/... ./pkg/ui/... ./pkg/mcp/... ./pkg/daemon/... \
        ./pkg/orchestrator/... ./tests/security/... ./cmd/grain/   # affected packages, all ok
cd ui && npm ci && npm run build
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  npx playwright test e2e/base-branch-existence.spec.js e2e/base-branch-unreadable.spec.js
```

## The walkthrough: `tests/e2e/ci_tools_walkthrough_test.go`

These parts are real:
- the CI service the daemon builds, `daemon.BuildCIService`, including:
  - the SQLite-stored run history;
  - the directory job cache and artifact store;
  - the Jobs `SharedGate` with budget, ledger and the `Stuck` hook wired through `ciStuckJobs`;
- `ui.Server` over a real SQLite `model.Store`;
- the orchestrator's own `"ci"` reconciler, reading githubsim through a real `github.RESTClient`, with a real pull request opened by `orchestrator.EnsurePullRequest`;
- the agent side: a real `grain mcpserver -grant grain-ci -pr-repo acme/widgets -server … -task …` subprocess, driven over stdio MCP. That subprocess is the "fake agent".

The workflow has three jobs: `a`, `b` and `flaky` (display name `flaky e2e`). `flaky` fails the first time it runs at a commit. A marker file outside the workspace, keyed by repo and commit, records that run. On later runs it waits while a hold file exists. When it fails, it keeps a `trace` artifact.

### §10.1 `retrigger_ci` (`c5b75c1a3`): `TestCIToolsWalkthroughRecheckOnlyTheFailedJob`

| Claim | Outcome |
|---|---|
| Push, then `wait_for_checks` returns red on `flaky` only | ✅ |
| The reconciler concludes the red verdict (`flake_recheck: false` merge policy, so the recheck under test is the agent's own) | ✅ `failure: checks/flaky e2e` |
| `retrigger_ci {}` reruns only `flaky` (answer: `Jobs queued: flaky e2e`, one new sandbox; `a` and `b` ran once, in the first run) | ✅ |
| The task's verdict becomes the recheck, with `RecheckOf` set to the red run, while the recheck runs | ✅ |
| A concurrent full `wait_for_checks` (second mcpserver process) starts no run, cancels nothing, and returns the recheck's verdict | ✅ The note was missing until the fix (finding 2) |
| The recheck passes, and the reconciler records the verdict as green with `RecheckOf` = the red run | ✅ `recheck … of grain CI run …: success … failed once and passed an uncached recheck` |
| A later full `wait_for_checks` is green with all three jobs `Cached`, and the merge gate reads green | ✅ |
| After a fresh red (new push), a selective `jobs: ["a"]`, then `retrigger_ci {}`: `flaky` is still the one rerun, **at the new commit** | ❌ before the fix: reran at the *first* commit (finding 1); ✅ after |
| `retrigger_ci {jobs: ["nope"]}` is refused with the real names | ✅ `retriggering grain CI: ci: selected job "nope" does not exist in the matching workflows; the jobs there are: a, b, flaky` |
| `retrigger_ci {jobs: ["flaky e2e"]}` (a display name) is accepted | ✅ |

### §10.2 `get_ci_artifact` (`5fa2c6762`): subtest of the above

| Claim | Outcome |
|---|---|
| With defaults (newest run, its failed job, its one artifact), the gzipped tar lands at the workspace root, byte-identical to `Service.OpenArtifact`, and `out/trace.txt` holds the commit's trace | ✅ |
| The tool result is a one-line "Wrote artifact …" and does not inline the bytes | ✅ |
| With explicit `run_id`, `job: "flaky e2e"`, `name: "trace"` and `path: "evidence/trace.tgz"`, the bytes match | ✅ |
| Another repo's run (`acme/other`) is refused | ✅ `grain CI run … is acme/other's, and this run may only read CI for acme/widgets -- so this is a refusal, not a run that does not exist. …` |

The plan's "real Playwright trace from a real run" was not attempted. The artifact here is a small tar from the local runner. The collect, store, download and untar path is the same.

### §10.3 Deadlock (`e0a1b447b`, `5115be820`): `TestCIToolsWalkthroughStuckGateAdmitsTheJobItsOwnSandboxBlocks`

The budget is a 3 GiB host. Task 9 has a live `model.Run` `9-1`, charged to the ledger as a 2 GiB `PoolAgent` sandbox under that run's id. Task 9's agent calls `wait_for_checks`, and the run's one job asks for `memory-mb: 2048`.

| Claim | Outcome |
|---|---|
| The job is admitted on its first gate attempt (queued 1ms), and the run is green | ✅ |
| The log names the `Stuck` admission | ✅ `ci: this job is waiting on capacity that cannot be released -- every sandbox this deployment is holding right now is a task's own agent run waiting for CI run <id> to answer it, and job-2 is one of that run's jobs -- no release is coming from any of them until it runs -- running it anyway rather than waiting for a release that is not coming` |
| An unrelated live sandbox (`other-1`, 512 MiB) plus a new push: the job waits (not admitted across 2 s of gate cycles), and its refusal names what holds the room | ✅ `for room in this deployment's sandbox budget -- memory: 2.5 GiB is committed by this deployment's live sandboxes of its 3.0 GiB sandbox budget (3.0 GiB host, 0.0 GiB reserved for the host itself), and this one wants 2.0 GiB -- held by 3 vCPU / 2.5 GiB (3 vCPU / 2.5 GiB in 2 agent sandboxes)` |
| Once the unrelated sandbox is released, the job is admitted through `Stuck` again, and the waiting `wait_for_checks` returns green | ✅ |

On the "names both holders" claim: the refusal names the holders by **pool and count** ("in 2 agent sandboxes"), not by run or task. That is what `5115be820` says it does (`Ledger.Describe`), so it is not a bug. Naming the runs would make "is it my own sandbox?" answerable from the message, and this is filed as a follow-up below.

### §10.4 `-github-sim` (Playwright, `27af676af`)

- The schedule and template forms' warnings were already walked on `main` by `ui/e2e/base-branch-existence.spec.js`, the `-github-sim` sibling of `base-branch-unreadable.spec.js`. Both pass here.
- **Added** to `base-branch-existence.spec.js`:
  - `POST /api/tasks` with `repo: "acme/widgets", base: "nope"` gets `400`, with the message: `base "nope" does not exist on acme/widgets: push it first, or have the task depend on the one that creates it`.
  - The same request with `base: "release/1.0"` gets `201`.
- 5/5 specs pass against `grain demo -addr 127.0.0.1:0 -open=false -github-sim` (the fixtures' `demoArgs`) with the built UI.

## Findings

### 1. `ci.RunFilter`'s zero value is oldest-first, but its doc and its callers assume newest-first (fixed)

**Defect.** `RunFilter`'s doc said: "The zero value is … started, newest first." `SortRuns` sorts `Descending == false` ascending, and `HTTPClient.FindCIRuns` sends `order=asc` for it. Every caller that built `RunFilter{Repo: …}` and took the first run as the latest was reading the **oldest** run:

- `ui.failedTaskJobs`, which is `retrigger_ci {}`'s default. After a second push, it reran the failed jobs at the branch's *first* commit.
- The `run_id` default of `get_ci_log` and `get_ci_artifact`. "Latest" was the repository's oldest remembered run.
- `query_test_history` and `wait_for_checks`' red-run attribution read the oldest N runs.

**Reproduction.**
- The walkthrough's subtest "a selective run does not hide the failed job". Before the fix it failed with `recheck after the selective run reran flaky at 036ed4c…, want flaky at 8f1c344…`. The stored store listed the branch's runs oldest first.
- Unit: `pkg/ui` `TestTaskScopedRecheckOfTheFailedJobsIsAtTheNewestCommit` and `pkg/mcp` `TestGetCILogDefaultsToTheNewestRun`. Both fail without the fix.

**Fix.**
- The four call sites ask for `Descending: true`.
- The doc now says what the zero value does.
- `SortRuns` and the HTTP API are unchanged. Every UI and CLI path already set `Descending: true` explicitly.

### 2. `Run.Recheck` is lost once a recheck concludes (fixed)

**Defect.**
- `Engine.Run` builds the finished run without `Recheck`.
- `Service.Start` replaces the record with that run when it concludes.
- Independently, the stored run store had no column for it.

As a result, `wait_for_checks`' `recheckRunNote` never fires. A full-suite wait handed the task's live recheck (`ui.liveRecheck`) is answered with "Grain CI run X … is success. Job flaky e2e: success", with nothing to say that this lists only the rechecked job and that the full verdict is combined. The verdict itself was right (`RecheckOf` is on the task). Only the explanation was lost.

**Reproduction.**
- The walkthrough asserts the note on the joined wait. Before the fix, `GET /api/ci/runs/<recheck>` returned `recheck` false.
- Unit: `pkg/ci` `TestAStoredRecheckIsReadBackAsARecheck` (fails without the store fix) and the new assertion in `TestEngineRunsARecheckedJobWithoutReadingTheCache`.

**Fix.**
- `Engine.Run` copies `plan.Request.Recheck`.
- `model.CIRun.Recheck` is a new `ci_run.recheck` column: in the schema, plus an `ensureColumn` migration that defaults it to 0, so old rows read back as ordinary runs, as they already did.
- `storedRun` and `runOf` carry it.

### 3. The `get_ci_artifact` / `get_ci_log` default is the newest run of the *repository*, not of the branch (follow-up)

Both descriptions say `run_id` "defaults to latest on branch". The default asks `FindGrainCIRuns(RunFilter{Repo: scope.Repo, …}, 1)`, which is the newest run of any branch of the repo. On a busy repo, that is usually another task's run. The scope knows the repository but not the branch, so the fix needs the branch to be wired into the tool. A follow-up is proposed rather than folded in here.

### 4. A budget refusal counts holders by pool but does not name them (follow-up, enhancement)

See §10.3. The text is exactly what `5115be820` intended. The proposal is to list the holder ids (agent run / task), so that a reader can tell whether the sandbox in the way is their own.

### Observations (not bugs)

- The CI reconciler collects a verdict only for a task that is completed or awaiting submit with a pull request open (`OpenPullRequestLinks`). While the agent's run is live, `retrigger_ci` cannot make the recheck the verdict's second sample, because no concluded red exists yet, so `recordTaskRecheck` correctly does nothing. The documented route still works: the agent's final full `wait_for_checks` reads the recheck's cached pass and is green.
- `wait_for_checks` enforces a 30 s minimum on `timeout_seconds` ("Asked for 3s; 30s is the shortest wait allowed"), so a walkthrough that needs a short wait has to run it in the background.

## Stubs (external boundaries only)

- **The job's machine.** `localCISandboxes` runs each step with `sh -c` in a temporary directory, as a local runner, in place of `daemon.CiSandboxes`' git-proxy checkout on a host or granule sandbox. The workflow's steps need nothing from the checkout. Artifact collection runs the engine's own script through the same `Exec`.
- **Where workflows come from.** `bareRepoCISource` reads `.grain/workflows/*.yml` with git from the bare repository githubsim serves, in place of `daemon.CiWorkflowSource` cloning from GitHub.
- **GitHub.** githubsim behind the real `github.RESTClient` (Go), and `grain demo -github-sim` (Playwright).
- **The agent sandbox's ledger charge.** The deadlock walkthrough charges `PoolAgent` directly under the id of a real live `model.Run`, which is the id the dispatcher charges it under, instead of dispatching a real agent run.
- No grain API, React or store was mocked.

## Fixes in this change

- `pkg/ci/filter.go`: the doc comment. `pkg/ui/ci.go`, `pkg/mcp/grain_ci_wait_tool.go` and `pkg/mcp/ci_artifact_tool.go`: `Descending: true`.
- `pkg/ci/engine.go`, `pkg/ci/runstore_stored.go`, `pkg/model/{ci_run.go,ci_run_store.go,schema.go,store.go}`: `Recheck` persisted.
- `pkg/model/postgres/adapter.go`: `recheck` added to `boolColumns`. The first CI run of this branch went red on the `postgres` job. Without the entry, the column was created as `BIGINT` on postgres, and every CI run write failed with `unable to encode false into binary format for int8`. The `postgres` job passed once the entry was added.
- Tests: `tests/e2e/ci_tools_walkthrough_test.go` (new) and `ui/e2e/base-branch-existence.spec.js` (the API case), plus the unit regressions named above.

`pkg/ci` and `pkg/mcp` are trust-boundary paths, so this change is expected to be held for security review before it auto-merges.

## CI verdict

Recorded in the closing note on task 2388, together with the full-suite `wait_for_checks` result for the pushed commit.
