# Self-debug: read-only Grain source checkout (`grain-src/`)

- **Date:** 2026-10-12
- **Plan:** `docs/test-plans/2026-10-11.md` §8. It covers `5c2beb0cb` (checkout and prompt guidance) and `38307be72` (leading slashes in the deprecated tools). Both are on `origin/candidate` (`git branch -r --contains`).
- **Base:** `origin/candidate` at `c56e11751112a7e7b75f1cbe8bc62b01c6380fc0`.
- **Task:** 3144 (filed by nightly test-plan task 2955).

## Environment

- Debian 12 grain sandbox (task 3144), Go `go1.26.6`, git `2.39.5`.
- **Harness:** `tests/e2e`'s `world`. It runs a real `gitproxy` in front of real bare upstream repos served by `git http-backend`, and a real SQLite store. Each run is a real `orchestrator.RunDispatch` with `Config{GitRemoteBase: <proxy>}`, pkg/mcp's real exec-backed sandbox tools (`mcp.NewSandboxTools`), and a scripted agent (`antigravity.NewForTest`) whose single turn is a `run_command`. The proxy authorizes each clone against the run's own `GitScope`, exactly as in a deployment.
- **Live cross-check:** this sandbox's own git proxy (`http://172.17.0.1:9728`), in front of the real `bwsalmon/grain` on GitHub.
- **Daemon build revision:** `grain version -json` from a full clone of `candidate`:
  ```json
  {"release": "v1.9.0-rc.1.0.20261012010511-c56e11751112",
   "revision": "c56e11751112a7e7b75f1cbe8bc62b01c6380fc0",
   "committedAt": "2026-10-12T01:05:11Z"}
  ```
  `origin/main` (the repository's default branch) was `64349ad583f126b3ea55fd00b4b24b8dd8f03235` at the time.

### Stubs

- **GitHub only.** The e2e upstreams are local bare repositories behind `git http-backend`, standing in for GitHub. The live cross-check used real GitHub through the proxy.
- **The model.** The agent is a scripted `antigravity.NewForTest` run, so what it "sees" is the real output of real shell commands in the real sandbox directory.
- Nothing in grain (orchestrator, proxy, store, MCP tools) was mocked.

## Steps and results

The durable form of steps 1–3 is `tests/e2e/grain_source_checkout_walkthrough_test.go` (`TestGrainSourceCheckoutWalkthrough`). Each subtest builds its own world. The agent's turn runs:

```
ls grain-src; echo head=$(git -C grain-src rev-parse HEAD)
git -C grain-src push origin HEAD:refs/heads/probe; echo push-exit=$?
git -C grain-src push <proxy>/bwsalmon/grain.git HEAD:refs/heads/probe; echo direct-push-exit=$?
```

### Step 1: three tasks

The first table is what `candidate` does before this change, observed with a probe run of the same harness.

| Task | `grain-src/` | HEAD | Push from `grain-src` | Prompt guidance |
|---|---|---|---|---|
| `reads: [bwsalmon/grain]` | present (`README.md`) | upstream `main` tip | refused: `fatal: 'grain-read-only' does not appear to be a git repository`, exit 128. Direct push to the proxy URL refused by the proxy (not the run's target). No `probe` branch upstream. | present: "Grain checked out its own source repository read-only at `<root>/grain-src` … standard sandbox tools … pushing is disabled." |
| `self-debug` only | **absent** | n/a | n/a | **nothing**: the section is empty. Daemon log: `remote: bwsalmon/grain is not in scope for this sandbox` / `403`. |
| neither | absent | n/a | n/a | no mention of `grain-src` (correct) |

**Live cross-check against the real `bwsalmon/grain`, with the checkout script run verbatim through this sandbox's proxy:**

- **Pre-fix script** (`git clone` + `remote set-url --push origin grain-read-only`): exit 0, HEAD `64349ad583f1…`, which is `main`. **It is not the daemon's build revision `c56e11751112…`.**
- **Post-fix script:** `grain-src-head: c56e11751112a7e7b75f1cbe8bc62b01c6380fc0`, which equals `grain version -json`'s `revision`.
- **`git -C grain-src push origin …`:** `fatal: 'grain-read-only' does not appear to be a git repository`, exit 128. `git remote -v` shows `origin http://172.17.0.1:9728/bwsalmon/grain.git (fetch)` and `origin grain-read-only (push)`.
- **Direct push to the proxy URL of another branch:** `! [remote rejected] … a sandbox may push only its own task's branch`. The proxy is the real guard; the push URL is a speed bump. (A task whose *target* is bwsalmon/grain could still push its own branch from `grain-src` by naming the URL. That is the same right it has from `work/`, so it is not a finding.)

### Step 2: Grain source unreachable

Two shapes:

- **Out of the proxy's scope:** the `self-debug`-only task above. The task started (`RunDispatch` returned no error and the agent ran), but before the fix the prompt gave **no reason** for the missing checkout. The self-debug system template still told the run to "inspect the `grain-src/` checkout".
- **In scope, upstream gone:** `bwsalmon/grain.git` renamed away under the harness's upstream, with a task holding both `self-debug` and `reads`. The proxy forwards and gets a 404. The task started, again with no explanation in the prompt before the fix.

After the fix, both prompts carry a sentence saying why (see Fixes), and both cases are asserted in the e2e test.

### Step 3: deprecated tools and leading slashes

`selfdebug.SourceTools` was run against the real `grain-src` checkout (pinned to `c56e1175`):

| Call | Result |
|---|---|
| `read_grain_source("go.mod")` | ok, 2659 bytes, `module github.com/ai-on-gke/grain…` |
| `read_grain_source("/go.mod")` | ok, same 2659 bytes |
| `read_grain_source("//go.mod")` | ok, same |
| `read_grain_source("/../go.mod")` | ok, same (cleaned to `go.mod`, still inside the root) |
| `read_grain_source("../go.mod")` | error: `path "../go.mod" escapes grain's source checkout` |
| `list_grain_source("")` / `("/")` | ok, identical root listings (`.dockerignore`, `.git/`, …) |
| `list_grain_source("/pkg/capability/selfdebug")` / `("pkg/capability/selfdebug/")` | ok, `selfdebug.go`, `selfdebug_test.go` |

Both tool descriptions begin with "Deprecated: inspect grain-src/ directly with standard sandbox tools …". Leading slashes work as `38307be72` intends. The e2e test asserts the slash and no-slash forms and the `../` refusal.

## Findings

1. **The checkout is the default branch, not the build the daemon runs (fixed).** `prepareGrainSource` cloned and stayed on the default branch. On this deployment, `candidate` is what runs and is often days behind `main`, so a self-debug agent read code that is not running: `64349ad5` versus the build's `c56e1175` today. The plan asks for "the daemon's build revision (or documented ref)", and neither held: the prompt did not say which ref it was.
   - *Reproduction:* the live cross-check above, or `TestGrainSourceCheckoutIsPinnedToTheBuildRevision` against the pre-fix code (HEAD is the newer `main` commit).
2. **No explanation when there is no checkout (fixed).** `grainSourceSection` returned `""` for every status other than ready. The `grainSourceUnauthorized` status was declared but never set. Plan step 2 ("says why") failed in both shapes.
   - *Reproduction:* the `self-debug alone` and `source unreachable` subtests of `TestGrainSourceCheckoutWalkthrough`, against the pre-fix code (the prompt does not contain "There is no `").
3. **`self-debug` alone never gets a checkout (filed, not fixed).** The proxy's scope (`model.Store.GitScope`) is the target plus `reads`/`writes`; a grant does not widen it. So the `self-debug` half of the feature cannot work through the proxy, and every self-debug-only run is told by `model.SystemTemplate` to use a directory it does not have.
   - *Reproduction:* `go test ./tests/e2e -run 'TestGrainSourceCheckoutWalkthrough/self-debug_alone' -v`. The log line is `grain source checkout unauthorized: … bwsalmon/grain is not in scope for this sandbox`.
   - Widening the git proxy's authorization is a security-boundary change, so it was proposed as a separate task needing human review rather than landed here unattended. The prompt half (finding 2) now tells such runs what is wrong and to fall back on `read_grain_source` / `list_grain_source`.
4. **Observation, not a bug in this feature:** `go build ./cmd/grain` in a *linked git worktree* (`git worktree add`) produced a binary whose `grain version -json` is `{}` (no vcs stamp), even with `-buildvcs=true`. A full clone stamps correctly. Releases build from full clones, so this does not affect deployments. It only matters to anyone reproducing this walkthrough from a worktree.

## Fixes (this change)

- **`pkg/orchestrator/checkout.go`:**
  - `prepareGrainSource` now passes the binary's own `version.Get().Revision` to the new `prepareGrainSourceAt`.
  - After the clone, the script checks out that commit, detached, when the repository has it (`git cat-file -e <rev>^{commit}`), and prints the resulting HEAD behind a marker. The revision is only put into the shell when it matches `^[0-9a-f]{7,64}$`.
  - A clone failure is classified: `unauthorized` for the proxy's own "is not in scope for this sandbox", `unavailable` for anything else (upstream 404, credential refusal, no proxy). The proxy's `remote:` line, or git's `fatal:` line, is kept in `GrainSourceWhy`.
- **`pkg/orchestrator/run.go`, `grainSourceSection`:**
  - When ready, it names the revision: either "`<rev>`, the revision this grain was built from", or "the tip of its default branch (`<head>`)" plus why that may differ from what is running.
  - When unauthorized or unavailable, it says there is no checkout and why, and points a self-debug run at the deprecated tools. When unauthorized, it also says that adding bwsalmon/grain to the task's reads is the fix for later runs.
  - When absent (neither the grant nor the read), it still says nothing.
- **Regression tests:**
  - `pkg/orchestrator/grain_source_internal_test.go`: pinning to the build revision, the fallback for an unknown revision, a malformed revision never reaching the shell, the unavailable explanation for self-debug versus reads-only runs, and `gitFailureLine`.
  - `tests/e2e/grain_source_checkout_walkthrough_test.go`: the durable walkthrough. Real proxy, real clone and real `RunDispatch` for the reads, self-debug-only, neither and unreachable cases, with the push refusal (push URL and proxy) and the deprecated tools against the real checkout.

## Follow-ups

- **Proposed:** "Let a self-debug run's git proxy scope read bwsalmon/grain, so it actually gets grain-src/" (finding 3). It depends on task 3144, and its auto-merge is off because it changes git-proxy authorization.
  - **Done in task 3204.** `model.Store.GitScope` now adds `model.GrainSourceRepo()` (bwsalmon/grain) to the *reads* of a live run whose task holds `self-debug` in its deployment-wide form (its team is one of `CapabilityTeams.DeploymentWide`'s; a team-scoped holder keeps `read_grain_source` only, since a clone reads every branch), so the `self-debug alone` subtest now expects a checkout and a refused push. `TestGitProxySelfDebugGrantIsAReadOfGrainsSourceOnly` (tests/security) holds the boundary: fetch only, with no push to any ref even alongside `git-push-any-ref`, and no read for runs without the grant.

## Local verification

- `go test ./pkg/orchestrator/... ./pkg/capability/selfdebug/... ./pkg/model/... ./cmd/grain/...`: ok.
- `go test ./tests/e2e/`: ok (187 s), including `TestGrainSourceCheckoutWalkthrough`.

## CI verdict

Grain CI full suite on `b5bc367` (the code in this change; this report-only commit follows it):
- Run `40b4e196c74639cd`: every job passed (`go` in 19m28s) except `ui e2e`. That job failed one Playwright spec unrelated to this change: `task_detail_workflows.spec.js:170`, "quotes an edited description as typed and links related tasks by address", which timed out waiting for navigation to `/tasks/69`. Nothing here touches the UI. That spec is one of the deployment's known-flaky Task Detail walkthroughs, although `query_test_history` had no earlier failure for it on record.
- The uncached `retrigger_ci` recheck of `ui e2e` (`98737f9c3c9ffd19`) passed.
- Combined verdict (`117be3a36255c172`): **success**. frontend, go, installer, lint, package cache, postgres, relay image, terraform and ui e2e all green.
