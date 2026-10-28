# Test Run: Empty-Repository Bootstrap, Squash/Rebase Merge Fallback, Handover Window Reset, and Package Registry Seams (`docs/test-plans/2026-10-22.md` §7)

- **Date:** 2026-10-23 UTC
- **Task:** `/tasks/218` (`grain/049f1e09/task-218`, targeting `candidate`)
- **Test Plan Reference:** `docs/test-plans/2026-10-22.md` §7
- **Commits Under Test:**
  - Empty-repository first-push default branch initialization: `802ef0f40` / `6304429ab`, `768dc4354` / `12e400c27`
  - `MergePullRequest` squash and rebase fallback: `afe4b7fc6` / `9e575d1bb`
  - Conversation handover window reset & suspended prompt preservation: `f80f0f968` / `2214a74e8`, `abe8837c7` / `c27f8cddb`
  - Antigravity resumed turn with prior step `ERROR`: `495d9c53a` / `33c23efb9`
  - Package registry credential seams & preflight checks: `ca841ce99` / `0a9cb9483`, `bfe65cca7`, `b15cf0a22`, `b5a556acd` / `1cc933f49`, `e7c4a6dfc` / `5961826e7`

---

## 1. Environment Details

- **OS / Kernel:** Debian GNU/Linux sandbox (`linux/amd64`)
- **Repository:** `bwsalmon/grain` at `/home/debian/work`
- **Branch:** `grain/049f1e09/task-218` (based on `candidate`)
- **Automated E2E Walkthrough Suite:** `tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`
- **Unit & Integration Suites Exercised:**
  - `pkg/mcp` (`TestOpenPullRequest*`)
  - `pkg/github` & `pkg/github/githubsim`
  - `pkg/orchestrator`
  - `pkg/agent/antigravity`
  - `pkg/capability/packagepublish`, `pkg/gitproxy`, `google/gitproxy/ggh`

---

## 2. Bug Discovered & Fixed During Walkthrough

### `mcp_grain-sandbox_open_pull_request` reported `"Pull request <repo>#0 is open"` and instructed the agent to poll CI checks after initializing an empty repository's default branch

- **Root Cause:**
  - When an agent working on the first commit of an empty repository pushes its branch and calls `open_pull_request` mid-run, `orchestrator.OpenPullRequestForTask` (`pkg/orchestrator/pullrequest.go`) calls `ensurePullRequest` -> `seedEmptyRepoBase`, which creates `main` (or `task.Base`), sets the repository's default branch to `main`, marks the task completed and closed (`Observation.CompletedAt` and `Observation.ClosedAt`), and returns `PullRequestStatus{Number: 0, ChecksKnown: true, Note: "Initialized <base> on <repo> at <sha> and marked task <id> done."}`.
  - However, `renderPullRequestReport` in `pkg/mcp/open_pull_request_tool.go` unconditionally formatted the first line as `fmt.Sprintf("Pull request %s#%d is open.\n", repo, r.Number)` and, because `len(r.Checks) == 0`, took the `case !r.ChecksKnown || len(r.Checks) == 0:` branch — returning:
    ```text
    Pull request acme/empty-midrun#0 is open.
    Initialized main on acme/empty-midrun at <sha> and marked task 1 done.
    No checks have reported yet -- CI may still be starting; call this tool again in a moment.
    ```
  - Additionally, `storeBackedOpener.OpenForTask` in `tests/e2e/mcpserver_open_pull_request_test.go` omitted `Note: status.Note` when converting `orchestrator.PullRequestStatus` into `daemon.OpenPullRequestResponse`.
- **Fix Applied:**
  - Updated `renderPullRequestReport` in `pkg/mcp/open_pull_request_tool.go` so that when `r.Number == 0 && r.Note != ""`, it returns `strings.TrimRight(r.Note, "\n")` directly without claiming `#0` is open or prompting the agent to poll checks.
  - Updated `storeBackedOpener.OpenForTask` in `tests/e2e/mcpserver_open_pull_request_test.go` to forward `Note: status.Note`.
  - Added `githubsim.CreateEmptyRepository(owner, repo, bareRepo)` in `pkg/github/githubsim/githubsim.go` to create a genuinely empty bare git repository (`0` commits, unborn `HEAD`, `DefaultBranch: ""`) with `http.receivepack=true` and `receive.denyCurrentBranch=ignore`.
- **Regression Coverage:**
  - Unit test: `TestOpenPullRequestOnEmptyRepoFirstPushReportsInitializationWithoutZeroPR` in `pkg/mcp/open_pull_request_tool_test.go`
  - E2E test: `TestWalkthroughEmptyRepoBootstrapToNormalPRFlow/open_pull_request_mid_run_initializes_main_and_closes_task` in `tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`

---

## 3. Walkthrough Execution & Verification Results

### Step 1: Empty Repository Bootstrap to Normal PR Flow (`802ef0f40`, `768dc4354`)

- **Test:** `TestWalkthroughEmptyRepoBootstrapToNormalPRFlow` (`tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`)
- **Subtest 1A (`open_pull_request_mid_run_initializes_main_and_closes_task`):**
  1. Created a genuinely empty bare repository via `githubsim.CreateEmptyRepository("acme", "empty-midrun", bare)` and verified `DefaultBranch` returned `""`.
  2. Dispatched task `/tasks/1` through `orchestrator.RunDispatch`:
     - Verified `prepareCheckout` logged that `acme/empty-midrun` is an empty repository and checked out unborn branch `grain/task-1`.
     - Committed `README.md`, pushed `origin HEAD` (which `githubsim` adopted as the initial `DefaultBranch = "grain/task-1"`), and invoked `open_pull_request` over stdio against the `mcpserver` + daemon HTTP server.
  3. Verified `open_pull_request` returned `"Initialized main on acme/empty-midrun at <12-char-sha> and marked task 1 done."` (no `#0`, no check-polling prompt).
  4. Verified 0 pull requests were opened on the remote, `DefaultBranch` on `acme/empty-midrun` was updated to `"main"` at the pushed SHA, task `/tasks/1` transitioned to `closed` (`CompletedAt` and `ClosedAt` set), and the closing comment on `/tasks/1` stated:
     `"Branch `grain/task-1` was the first branch pushed to empty repository acme/empty-midrun, so grain initialized `main` at `<12-char-sha>` and set it as the repository's default branch rather than opening a pull request against itself.\n\ngrain has marked this task done as landed on `main`."`
- **Subtest 1B (`finish_initializes_main_then_task_2_opens_pr_1_and_merges`):**
  1. Created an empty repository `acme/empty-bootstrap` via `githubsim.CreateEmptyRepository` with `PullRequestBase = 1`.
  2. Dispatched task `eb-1` via `orchestrator.RunCycle`: the agent committed `README.md`, pushed `origin HEAD`, and exited without calling `open_pull_request`.
  3. Verified the finish path (`ProcessResult` -> `salvagePushedBranch` -> `ensurePullRequest` -> `seedEmptyRepoBase`) closed `eb-1` without opening a 0-commit PR, created `main` at `eb-1`'s commit SHA, updated `DefaultBranch` to `"main"`, and recorded the initialization closing comment.
  4. Dispatched task `eb-2` against `acme/empty-bootstrap` via `orchestrator.RunCycle`: verified its checkout cloned `main` (containing `README.md`), committed `CONTRIBUTING.md`, pushed `grain/task-eb-2`, and opened PR `#1` against `main`.
  5. Ran a subsequent `orchestrator.RunCycle` after CI checks passed on `grain/task-eb-2`: verified the merge queue merged PR `#1` cleanly into `main`, closed `eb-2`, and left both `README.md` and `CONTRIBUTING.md` on `refs/heads/main`.
- **Outcome:** **PASS**

---

### Step 2: Merge Queue / Orchestrator Squash and Rebase Fallback (`afe4b7fc6`)

- **Test:** `TestWalkthroughMergePullRequestSquashAndRebaseFallback` (`tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`)
- **Procedure:**
  1. Wrapped `githubsim.Sim` with `mergeMethodPolicyTransport` intercepting `PUT /repos/{owner}/{repo}/pulls/{number}/merge` and recording every `merge_method` attempted.
  2. Filed parent task `/tasks/17` (`WorkOrigin: acme/widgets#99`) and child implementation task `/tasks/42` (`AutoMerge: true`, `LinkProposedBy: 17`, `Approval: alice`).
  3. **Case 2A (`fallback_to_squash_when_merge_commits_disallowed`):**
     - Configured transport to return HTTP 405 `{"message":"Merge commits are not allowed on this repository"}` when `merge_method` is `""` or `"merge"`.
     - Dispatched task `/tasks/42` in cycle 1 (opening PR `#1`) and ran the merge queue in cycle 2.
     - Verified `MergePullRequestWithMessage` attempted `merge_method` `["", "squash"]`, landed PR `#1` on `main`, closed `/tasks/42`, and preserved all lineage/identity trailers (`Origin: acme/widgets#99`, `Filed-by: task 17`, `Approved-by: alice`) in the commit message on `refs/heads/main`.
  4. **Case 2B (`fallback_to_rebase_when_merge_and_squash_disallowed`):**
     - Configured transport to return HTTP 405 for both `"Merge commits are not allowed on this repository"` and `"Squash commits are not allowed on this repository"`.
     - Verified `MergePullRequestWithMessage` attempted `merge_method` `["", "squash", "rebase"]`, landed PR `#1` on `main`, closed `/tasks/42`, and preserved `Origin: acme/widgets#99`, `Filed-by: task 17`, and `Approved-by: alice` on `refs/heads/main`.
- **Outcome:** **PASS**

---

### Step 3: Multi-Attempt Handover Window Reset & Suspended Prompt Preservation (`f80f0f968`, `abe8837c7`)

- **Test:** `TestWalkthroughMultiAttemptHandoverWindowResetAndSuspendedPrompt` (`tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`)
- **Subtest 3A (`multi_attempt_handover_window_reset`):**
  1. Seeded 20 comments (`~80 KiB > 64 KiB`) on `/tasks/1` prior to Attempt 1.
  2. Dispatched Attempt 1 via `orchestrator.RunDispatch` + `orchestrator.ProcessResult`: verified Attempt 1's prompt contained `handoverRequest` (`write_handover`), and had Attempt 1 call `write_handover` with summary `H1` and park on `ask_question`.
  3. Seeded another 20 comments (`~80 KiB > 64 KiB`) after `H1` so post-`H1` comments again exceeded `maxCommentThreadBytes` (`64 KiB`).
  4. Dispatched Attempt 2: verified Attempt 2's prompt included `H1`, `PromptThreadCutNotice`, and `handoverRequest` (since post-`H1` bytes exceeded 64 KiB), and had Attempt 2 call `write_handover` with summary `H2` and park on `ask_question`.
  5. Added a short human reply (`< 1 KiB` after `H2`) and dispatched Attempt 3:
     - Verified `H1` and pre-`H2` comments were omitted from Attempt 3's prompt.
     - Verified `H2`, `PromptThreadCutNotice`, and the short human reply were present in Attempt 3's prompt.
     - Verified `handoverRequest` (`write_handover` prompt instruction) was **omitted** on Attempt 3 because `postHandoverBytes` after `H2` was well under the 64 KiB window.
- **Subtest 3B (`suspended_prompt_preserves_updated_body_attachment_and_capability`):**
  1. Dispatched Turn 1 on `/tasks/10` (`INITIAL-TASK-BODY-BEFORE-EDIT`, initial comment, standing `PromptExtension`), recording suspended session `sess-walkthrough-10`.
  2. Between turns, edited the task body to `UPDATED-TASK-BODY-AFTER-OPERATOR-EDIT`, added a follow-up comment with image attachment `architecture.png`, and granted capability `walkthrough-cap` (which materializes a placement and emits dynamic prompt section `WALKTHROUGH-CAPABILITY-DYNAMIC-PROMPT-SECTION`).
  3. Dispatched Turn 2 (`Attempt: 2`): verified `RunConfig.ResumeSession == "sess-walkthrough-10"` and verified `RunConfig.SuspendedPrompt` contained the updated task body (`UPDATED-TASK-BODY-AFTER-OPERATOR-EDIT`), the new comment (`FOLLOW-UP-COMMENT-FOR-RESUMED-TURN`), the placed attachment path (`task-attachments/1-architecture.png`), and the capability's dynamic prompt section (`WALKTHROUGH-CAPABILITY-DYNAMIC-PROMPT-SECTION`), while omitting the already-delivered Turn 1 comment and standing `PromptExtension`.
- **Outcome:** **PASS**

---

### Step 4: Antigravity Resumed Turn with Prior Step `ERROR` (`495d9c53a`)

- **Test:** `TestWalkthroughAntigravityResumedTurnWithPriorStepError` (`tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`)
- **Procedure:**
  1. Pre-seeded `/tasks/77` with `SuspendedSession{Framework: "antigravity", Token: "conv-resumed-err-77"}`.
  2. Built a stub `agy` binary that records its CLI arguments and emits a resumed conversation `stream-json` transcript containing:
     - `init` event advertising the full eager MCP tool roster
     - `step_update` index `0` (`agent_response`, `state: "ERROR"`) from the earlier interrupted turn
     - `step_update` index `1` (`tool`, `mcp_grain-sandbox_comment_on_issue`, `state: "ACTIVE"` -> `"DONE"`)
     - `step_update` index `2` (`agent_response`, `state: "ACTIVE"` with `text_delta` -> `state: "DONE"`)
     - terminal `result` event with `status: "ERROR"` (referencing step 0's error) and non-empty `response`, then exiting with status `1`.
  3. Executed `orchestrator.RunDispatch` + `orchestrator.ProcessResult` on Attempt 1 using the real `antigravity.Framework`.
  4. Verified `agy` was invoked **exactly once** (with `--conversation conv-resumed-err-77` and no retry loop), `RunDispatch` succeeded on Attempt 1 (`runs[0].Outcome == "succeeded"`), `Observation.CompletedAt` was stamped, and the `comment_on_issue` closing report was recorded on `/tasks/77`.
- **Outcome:** **PASS**

---

### Step 5: Package Registry Credential Checks (`b5a556acd`, `bfe65cca7`, `b15cf0a22`, `ca841ce99`, `e7c4a6dfc`)

- **Test:** `TestWalkthroughPackageRegistryCredentialChecks` (`tests/e2e/empty_repo_merge_fallback_and_handover_walkthrough_test.go`)
- **Subtest 5A (`package_publish_capability_and_ggh_ambient_registry_env`):**
  1. Verified `package-publish` (`pkg/capability/packagepublish`) refuses a fine-grained PAT (`github_pat_...`) for `ghcr.io` during capability resolution with a clear message explaining that GitHub Container Registry only supports classic PATs (`write:packages`) or GitHub App installation tokens.
  2. Verified `package-publish` honours a classic PAT (`ghp_...`) and materializes `/home/debian/.package-publish-registry` (`ghcr.io`), `/home/debian/.package-publish-username` (`x-access-token`), and `/home/debian/.package-publish-token`.
  3. Verified `google/gitproxy/ggh` ambient `gcloud` credential source refuses `PackageCredential("", ...)` when `GRAIN_GOOGLE_PACKAGE_REGISTRY` is unset, and mints `oauth2accesstoken` against the configured Artifact Registry host (`us-central1-docker.pkg.dev`) when `GRAIN_GOOGLE_PACKAGE_REGISTRY=us-central1-docker.pkg.dev` is set.
- **Subtest 5B (`release_images_pat_scope_check_and_docker_config_json`):**
  1. Ran `scripts/release-images.sh` against a local mock GitHub API server (`GHCR_SCOPES_URL`) and stub `docker` binary that records whether `docker build` is invoked:
     - **Classic PAT with `X-OAuth-Scopes: repo, workflow` (missing `write:packages`):** Verified `scripts/release-images.sh` exited non-zero **before** `docker build` was invoked, reporting that the classic PAT has scopes `'repo, workflow'` and is missing `'write:packages'` (with remediation steps pointing to `/settings/git-connections`).
     - **Fine-grained PAT (`github_pat_...`):** Verified `scripts/release-images.sh` exited non-zero **before** `docker build` was invoked.
     - **Classic PAT with `X-OAuth-Scopes: repo, workflow, write:packages`:** Verified `scripts/release-images.sh` passed the preflight check, wrote `~/.docker/config.json` with `auths["ghcr.io"].auth == base64("x-access-token:<token>")` without invoking `docker login`, and proceeded to `docker build` and `docker push`.
- **Outcome:** **PASS**

---

## 4. Test Execution Summary

```text
$ go test -v ./pkg/mcp -run TestOpenPullRequest
=== RUN   TestOpenPullRequestFormatsTheServerReport
--- PASS: TestOpenPullRequestFormatsTheServerReport (0.00s)
=== RUN   TestOpenPullRequestOnEmptyRepoFirstPushReportsInitializationWithoutZeroPR
--- PASS: TestOpenPullRequestOnEmptyRepoFirstPushReportsInitializationWithoutZeroPR (0.00s)
=== RUN   TestOpenPullRequestPropagatesOrchestratorErrorsAsToolErrors
--- PASS: TestOpenPullRequestPropagatesOrchestratorErrorsAsToolErrors (0.00s)
PASS
ok  	github.com/bwsalmon/grain/pkg/mcp	0.025s

$ go test -v ./tests/e2e -run "TestWalkthrough(EmptyRepo|MergePullRequest|MultiAttempt|AntigravityResumed|PackageRegistry)|TestMCPServerOpensAPullRequestOverStdio"
--- PASS: TestWalkthroughPackageRegistryCredentialChecks (2.11s)
    --- PASS: TestWalkthroughPackageRegistryCredentialChecks/package_publish_capability_and_ggh_ambient_registry_env (0.00s)
    --- PASS: TestWalkthroughPackageRegistryCredentialChecks/release_images_pat_scope_check_and_docker_config_json (2.10s)
--- PASS: TestWalkthroughMultiAttemptHandoverWindowResetAndSuspendedPrompt (0.00s)
    --- PASS: TestWalkthroughMultiAttemptHandoverWindowResetAndSuspendedPrompt/suspended_prompt_preserves_updated_body_attachment_and_capability (0.14s)
    --- PASS: TestWalkthroughMultiAttemptHandoverWindowResetAndSuspendedPrompt/multi_attempt_handover_window_reset (0.18s)
--- PASS: TestWalkthroughMergePullRequestSquashAndRebaseFallback (0.00s)
    --- PASS: TestWalkthroughMergePullRequestSquashAndRebaseFallback/fallback_to_rebase_when_merge_and_squash_disallowed (0.50s)
    --- PASS: TestWalkthroughMergePullRequestSquashAndRebaseFallback/fallback_to_squash_when_merge_commits_disallowed (0.51s)
--- PASS: TestWalkthroughAntigravityResumedTurnWithPriorStepError (3.15s)
--- PASS: TestWalkthroughEmptyRepoBootstrapToNormalPRFlow (0.00s)
    --- PASS: TestWalkthroughEmptyRepoBootstrapToNormalPRFlow/finish_initializes_main_then_task_2_opens_pr_1_and_merges (0.56s)
    --- PASS: TestWalkthroughEmptyRepoBootstrapToNormalPRFlow/open_pull_request_mid_run_initializes_main_and_closes_task (3.30s)
--- PASS: TestMCPServerOpensAPullRequestOverStdio (3.57s)
PASS
ok  	github.com/bwsalmon/grain/tests/e2e	5.717s
```
