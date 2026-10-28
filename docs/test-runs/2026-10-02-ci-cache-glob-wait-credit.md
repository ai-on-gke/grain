# CI cache branch scoping, glob/quarantine fixes and MCP wait credit: walkthrough

**Date**: 2026-10-03 (plan dated 2026-10-02)
**Task**: grain/task-1579
**Plan**: §7 of `docs/test-plans/2026-10-02.md` ("CI and MCP correctness"). The plan file is not on `main` yet. It was read from `3330e60d2` (on `origin/candidate` and `grain/task-1593`).
**Code under test**: `main` at `5a63bb2d1`. `git branch -r --contains c97f1f70d` lists `origin/main` and not `origin/candidate`, so this ran on `main`. The tree contains `c97f1f70d`, `f117b8199`, `3cfff83f1`, `22afa9095`, `9ebb8c7d9`, `14245b220`, `e1bdebe9b`, `105f2c05f`, `7d1f39fac`, `7a7b330e5`, `447e07f18` and `a85e897cf`.

## Summary

| Area | Result |
|---|---|
| Actions cache: task branch saves `k`, `main` restores `k` | **PASS**: miss |
| Actions cache: what a release run sees | **PASS** (as designed): release runs read only release-written entries, so not `main`'s push entries |
| `paths: ['docs/**/*.md']` vs `docs/a.md` and `docs/x/y/a.md` | **PASS**: both re-run the job |
| Quarantined test in a job cancelled from the UI | **PASS**: `cancelled`, not excused |
| Quarantined test in a job past its `timeout-minutes` | **PASS**: `cancelled`, not excused |
| Quarantined test in a **step** past its own `timeout-minutes` | **FAIL → FIXED** (F1): was `success`, "passed (quarantined)" |
| Three `wait_for_checks` calls credited cumulatively | **PASS**: 15 s → 30 s → 45 s |
| `list_ci_runs` with `repo:` set to a `reads` repo | **PASS** functionally; **wording FIXED** (F2) |

## Environment

| | |
|---|---|
| OS | Debian 12.15, kernel 6.1.0-53-amd64, x86_64 |
| Go | go1.26.6 linux/amd64 |
| git | 2.39.5 |
| Store | SQLite (`pkg/model/sqlite`), at `/tmp/w7/data/store` |
| Extra packages | `jq` (already in the image) |

## How the environment was built (and what is stubbed)

Plain `grain demo -addr 127.0.0.1:0 -open=false` has no CI engine, no Actions cache and no run clock to credit. `cmd/grain/demo_observability.go` says so, and the 2026-10-01 CI walkthrough found the same. The real daemon reads workflows by cloning from GitHub, which a sandbox cannot reach. So this run reused and extended that walkthrough's harness: [`harness.go.txt`](2026-10-02-ci-cache-glob-wait-credit/harness.go.txt), built as `_walk7/main.go`. The harness wires the production pieces the daemon wires:

- `ui.NewServer` with the real `/api/ci/*` handlers (start, read, cancel with `DELETE /api/ci/runs/{id}`, the call the UI's Cancel makes) and the real `POST /api/tasks/{id}/wait-credit`;
- `ci.Service` with `ci.NewStoredRunStore`, `ci.Engine` and `ci.OpenDirCache`; jobs run in `orchestrator.NewHostSandboxes` (real `bash` on the host, checkout of the run's commit);
- the real Actions cache: `actionscache.NewCache` + `actionscache.NewServer` + `actionscache.Scopes` on its own listener (`127.0.0.1:18082`). Every CI job's sandbox is granted a token for `Scope{Repo, Ref, Trusted: Event == release}`, which is `cmd/grain`'s `ciActionsCacheScope` field for field. The token is revoked on Release, and the URL is handed to steps as `ACTIONS_CACHE_URL`, as `act --env` does;
- for the MCP parts: a real `mcp.Registry`, with `AnnounceDeadline`, `AnnounceWaitCredit(orchestrator.RunWaitCreditCap, …)` and the real `NewGrainCIWaitTools` registered on it, with `CIRepoScope{Repo: acme/widgets, Reads: [acme/gadgets]}`. Tools are called as JSON-RPC `tools/call` lines through `Registry.Handle`, as an agent's MCP client sends them.

**Stubs (external boundaries only):**

1. *GitHub as workflow source.* A `ci.TreeSource` that shallow-clones a branch of a local bare repository (`/tmp/w7/widgets.git` = `acme/widgets`, `/tmp/w7/gadgets.git` = `acme/gadgets`), loads `.grain/workflows` with `ci.LoadDir`, and builds the `ci.Tree` with `git ls-tree -r -z` exactly as `cmd/grain`'s `ciTree` does.
2. *The `actions/cache` action.* The action cannot be fetched without github.com. `ci/cache.sh` in the fixture makes the same Actions-cache HTTP calls with `curl` that the action's toolkit makes: `GET …/cache?keys=&version=`, `POST …/caches`, `PATCH` with `Content-Range`, `POST …/caches/{id}`, and `GET archiveLocation`.
3. *Release trigger.* A release run can only be started by grain itself (`ci.Request.TrustedRelease`), so `/walk/release?ref=` calls `ci.Service.Start` with `Event: release, TrustedRelease: true`. Push runs go through the real `POST /api/ci/runs`.
4. *The daemon's run clock.* `orchestrator.RunWaitCredits.register` is unexported and only a dispatched agent run calls it. The harness's `stubCredits` is `runClock.credit` line for line: `max(seen, reported)`, capped at `RunWaitCreditCap`, deadline = base + credited. It has no cancel timer, and it records every report it receives. The MCP-side reporter is `cmd/grain/mcpserver.go`'s `daemonWaitCredit` verbatim, over `ui.HTTPClient.CreditWait`.
5. *GitHub client (`7d1f39fac`, `7a7b330e5`, `447e07f18`).* There is no GitHub here, so these were not walked through. Their `pkg/github` unit tests ran in grain CI's `go` job.

Commands:

```sh
sh docs/test-runs/2026-10-02-ci-cache-glob-wait-credit/setup.sh        # fixture repos and branches
mkdir _walk7 && cp docs/test-runs/2026-10-02-ci-cache-glob-wait-credit/harness.go.txt _walk7/main.go
go build -o /tmp/walk7 ./_walk7
setsid nohup /tmp/walk7 >> /tmp/w7-serve.log 2>&1 < /dev/null &        # 127.0.0.1:18081, cache on :18082
R=docs/test-runs/2026-10-02-ci-cache-glob-wait-credit/run.sh             # start a run, wait, print jobs and steps
sh $R walk/cache; sh $R main; sh $R main release; ...                    # §1
/tmp/walk7 agent -task walk-wait -waits 3 -budget 2h                     # §4
/tmp/walk7 agent -task walk-wait -calls '[{"name":"list_ci_runs","arguments":{"repo":"acme/gadgets"}}]'   # §5
```

## Results

### 1. Actions cache scoped by branch and release (`c97f1f70d`, `f117b8199`)

Every branch runs `.grain/workflows/cache.yml` (`on: [push, release]`, `cache: false`). It restores `k`, then saves `k` with a body naming the ref and event that wrote it. Runs in order:

| # | Run | Restore of `k` | Expected | |
|---|---|---|---|---|
| 1 | `walk/cache` push (×3) | 1st miss, then `HIT -> written by ref=walk/cache event=push` | own branch only | PASS |
| 2 | `main` push | `MISS (find answered 204)` | **must miss** the task branch's entries | PASS |
| 3 | `main` push | `HIT -> written by ref=main event=push` | own ref | PASS |
| 4 | `main` **release** | `MISS (find answered 204)`, though `main` push entries exist | a release reads only trusted entries | PASS |
| 5 | `walk/cache2` push (new branch) | `HIT -> written by ref=main event=release` | falls back on release-written entries, never on `walk/cache` or `main` push | PASS |
| 6 | `walk/cache` push | `HIT -> written by ref=walk/cache` | own ref first | PASS |
| 7 | `main` release | `HIT -> written by ref=main event=release` | own trusted entries | PASS |
| 8 | `main` push | `HIT -> written by ref=main event=push` (own ref beats the release entry) | preference 2 over 1 | PASS |

On disk (`/tmp/w7/data/cache/actions/<id>/meta.json`), entries 1–10 record `ref` (`walk/cache`, `main`, `walk/cache2`) and `trusted: true` only on entries 6 and 9, the two release saves. A request with a token nobody granted got `403 {"message":"this cache token is not one of this deployment's live CI jobs"}`. Every job got its own token (25 grants logged), revoked on release.

Release design note: a release run does **not** see `main`'s push-written cache. `docs/actions-cache.md` and the `c97f1f70d` message say this is intended, so a push can never poison a release. The cost is that a release builds cold until a release has saved the key.

### 2. Path keys with `docs/**/*.md` (`3cfff83f1`)

Grain refuses `paths:` on a trigger by name. It takes them on the *job*, where they decide the job's cache key. The `walk/paths` job declares `paths: ['docs/**/*.md']`. The tree has `docs/a.md`, `docs/x/y/a.md` and `docs/notes.txt`.

| # | Commit | Job | Expected | |
|---|---|---|---|---|
| A | initial | ran | ran | PASS |
| A′ | same commit again | `cached=true` | hit | PASS |
| B | `README` changed | `cached=true` | hit (not a declared file) | PASS |
| C | `docs/notes.txt` changed | `cached=true` | hit (not `*.md`) | PASS |
| D | **`docs/a.md`** changed | **ran** (`docs job ran at 71c1b3e…`) | must run. Before `3cfff83f1`, `**/` needed a directory, so `docs/a.md` was outside the key and this was a stale hit | PASS |
| E | **`docs/x/y/a.md`** changed | **ran** (`… at b2b954c…`) | must run | PASS |
| — | `walk/paths-trigger`: `on: push: paths: [...]` | run `failure`: `push: "paths" is not a filter grain applies -- an event takes branches and branches-ignore` | refused, not silently ignored | as designed |

### 3. Quarantine with cancelled, timed-out and errored steps (`22afa9095`)

Each branch has `.grain/quarantine.yml` quarantining `TestFoo`. Its job prints `--- FAIL: TestFoo (0.00s)` and then:

| Branch | After the FAIL line | Verdict | Expected | |
|---|---|---|---|---|
| `walk/q-control` | `exit 1` | `success`, `passed (quarantined)` | excused (positive control) | PASS |
| `walk/q-cancel` | `sleep 300`, run cancelled with `DELETE /api/ci/runs/{id}` (the UI's Cancel) | `cancelled`, step `cancelled`, not quarantined | red | PASS |
| `walk/q-jobtimeout` | `sleep 300`, job `timeout-minutes: 1` | `cancelled`, `this job ran past its 1m0s bound`, not quarantined | red | PASS |
| `walk/q-steptimeout` | `sleep 300`, **step** `timeout-minutes: 1` | **`success`, `passed (quarantined)`, step exit -1** | red | **FAIL → F1** |
| `walk/q-steptimeout`, fixed build | same | `failure`, step error `this step ran past its own 1m0s bound (timeout-minutes)`, not quarantined | red | PASS |

The errored (infrastructure step) case is covered by `TestEngineQuarantineDoesNotExcuseErroredStep`, which passes. No fixture was built for it.

### 4. Cumulative wait credit (`e1bdebe9b`)

A scripted agent run on task `walk-wait` had a 2 h budget, so the deadline was `03:38:44Z` at start. It made three `wait_for_checks` calls and pushed a new commit between calls, as an agent does. Each run has a 12 s uncached job.

| After | Blocked in this call | Reports the daemon received (cumulative) | Credited | Deadline |
|---|---|---|---|---|
| start | — | — | 0 s | 03:38:44.315Z |
| wait 1 | 15.08 s | 0 s, 15.014 s | **15.014 s** | 03:38:59.329Z |
| wait 2 | 15.09 s | …, 15.015 s, 30.032 s | **30.032 s** | 03:39:14.347Z |
| wait 3 | 15.09 s | …, 30.032 s, 45.05 s | **45.05 s** | 03:39:29.365Z |

The credit is the sum of the three waits, not the longest one. Before `e1bdebe9b` each call reported only its own ~15 s, the granter kept `max`, and the run would have been credited 15 s. Each call's first report carries the run's total so far (15.015 s, 30.032 s), as `startWaitCredit` intends.

On a first attempt the fixture's `walk/wait` had dropped `main`'s `cache.yml`. All three runs were refused by the base-branch gate in 0 s and earned 0 s and 1 ms, which is correct: a wait that does not block earns nothing. The fixture now keeps `cache.yml`.

### 5. `list_ci_runs` with a `reads` repo (`105f2c05f`)

Scope `{Repo: acme/widgets, Reads: [acme/gadgets]}`, with one run on `acme/gadgets`:

| Call | Answer | |
|---|---|---|
| `repo: acme/gadgets` | the gadgets run, only it (1 of 28) | PASS (was the widgets runs before `105f2c05f`) |
| `repo: ACME/Gadgets` | the same | PASS |
| no `repo` | acme/widgets runs only | PASS |
| `repo: acme/secret` | refused | PASS |
| Heading and refusal wording | heading read `repo acme/gadgets (this run's own, and the only one it may read)`. The refusal said `use acme/widgets, its own repository` and never named acme/gadgets | **F2, fixed** |

After the fix: `Filter: repo acme/gadgets (one this run may read, besides its own).`, `Filter: repo acme/widgets (this run's own; it may also read acme/gadgets).`, and `use acme/widgets, its own repository, acme/gadgets, which it may read, or leave "repo" out.`

## Findings

### F1 — Quarantine excused a step stopped by its own `timeout-minutes` (fixed)

**Reproduction (deterministic):** use a job step with `timeout-minutes: 1` that prints `--- FAIL: TestFoo` for a quarantined `TestFoo` and then sleeps past the minute. The sandbox enforces the step's bound through `Command.Timeout` and kills the shell. The step comes back as an ordinary non-zero exit (`-1` on the host backend) with no error. `stepCtx` (the job's bound) has not expired, so the step concludes `failure`. That made it eligible for quarantine, and the job went green as "passed (quarantined)". `22afa9095` closed this for the job's bound and for cancellation, but not for a step's own bound.

**Fix** (`pkg/ci/engine.go`, `runSteps`): the step's elapsed time is measured against its own bound with the engine clock. The measurement doesn't read a context, because the guest-side bound is the one that ends the command and the call is deliberately left running past it. A step that ran to its own bound stays `failure` and gets the error `this step ran past its own <d> bound (timeout-minutes)`. Quarantine does not apply to it. A step failing well inside its bound is still excused. Regression test: `TestEngineQuarantineDoesNotExcuseAStepPastItsOwnTimeout`, with both sub-cases. It fails on the unfixed tree with `run.Conclusion = success, want failure`.

### F2 — The CI scope wording ignored the task's `reads` repositories (fixed)

**Reproduction:** with `CIRepoScope{Repo: "acme/widgets", Reads: ["acme/gadgets"]}`, call `list_ci_runs {"repo":"acme/gadgets"}`. It returns the right runs, under the heading "this run's own, and the only one it may read". The refusal for any other repo, the tool and `get_ci_log` descriptions, and `refuseRun` likewise all named only the run's own repo, so an agent was told the wrong boundary. **Fix** (`pkg/mcp/ci_repo_scope.go`): each of those sentences names the read repos when there are any. A scope with no reads reads exactly as before. Regression test: `TestListCIRunsSaysWhichRepositoriesTheTaskReads`, which fails 7 ways on the unfixed tree.

Both fixes are in `pkg/ci` and `pkg/mcp` (trust boundary v2), so they will be held for security review, as the task expects.

## Not covered

- GitHub client changes (`7d1f39fac`, `7a7b330e5`, `447e07f18`), `9ebb8c7d9`, `14245b220` and `a85e897cf` have no walkable surface without GitHub or the real daemon. They are covered only by their unit tests in grain CI.
- The UI's Cancel button was not clicked in a browser. Its request (`DELETE /api/ci/runs/{id}`) was sent directly.
- Wait credit was measured against the stub run clock, not a dispatched agent run's real `runClock` timer (stub 4).

## Grain CI on this branch

The first full run (`f32b06967ab90408`, at `564bc00`) failed only on two known flakes that are red on other branches at the same time. Neither touches anything this branch changes:

- `go`: `pkg/console` `TestSessionGivesItsProcessAControllingTerminal` ("nothing after 30s"; fix queued as task 1556). `pkg/ci`, `pkg/mcp` and every other package passed.
- `ui e2e`: `suites_workflows.spec.js`, where `POST /api/suite-runs` answered 400 for `acme/innovate-app`. This is the suites flake the plan itself notes; `main` passed and failed it on neighbouring runs.

Grain CI caches failures, so a new commit was pushed for a fresh verdict.

## Notes for whoever runs this next

- Don't write `jq … gsub("^"; …)`: it loops forever on multi-line strings. Here it ran the VM out of memory, and the OOM killer took the sandbox's exec agent with it, so every command died with "the guest closed the connection". `run.sh` uses `split("\n") | map(...)` instead.
- A fixture branch must keep every job `main` requires, or the base-branch gate refuses its runs in 0 s.
