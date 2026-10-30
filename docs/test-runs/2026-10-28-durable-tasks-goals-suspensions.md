# Walkthrough: Durable Tasks — `resume_later`, Daemon-Checked Goals, Suspensions, Stop Rule, and Grant-Change Restarts

- **Date:** 2026-10-29
- **Plan:** `docs/test-plans/2026-10-28.md` §2 (*Durable Tasks: `resume_later`, Daemon-Checked Goals, Suspensions (Context, Usage Limit, Model Capacity), Stop Rule, and Grant-Change Restarts*)
- **Task:** [Task #1715](/tasks/1715) (Origin: [Task #1549](/tasks/1549))
- **Branch:** `grain/049f1e09/task-1715` (base `main` at `41de786`)

---

## 1. Environment

- **Repository:** `ai-on-gke/grain` (`/home/debian/work`, branch `grain/049f1e09/task-1715` from `41de786`)
- **Runtime:** Debian 12 sandbox (`linux/amd64`), Go `go1.26.2`, Node `v20.19.0`, SQLite CLI `3.40.1-2+deb12u2` (`sudo apt-get install -y sqlite3`), Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`)
- **Build & Demo Server:**
  - Built frontend (`npm ci --omit=dev && npm run build`) into `pkg/ui/static` and compiled `bin/grain` (`v3.1.0-dev.206+41de786`).
  - Exercised `grain demo -github-sim -fake-ci -open=false` alongside a deterministic SQLite + orchestrator + UI walkthrough harness backed by `/tmp/walkthrough-s2.db`.

---

## 2. Step-by-Step Walkthrough & Observations

### Step 2: `resume_later` (`when.at`, `when.ci`, `when.task` + `until`, `when.message`) and Unpushed-Work Refusal

We exercised `mcp.NewResumeLaterTools` (`pkg/mcp/resume_later_tool.go`), `orchestrator.ProcessResult` (`pkg/orchestrator/finish.go`), `orchestrator.reconcileWake` (`pkg/orchestrator/wake.go`), and `orchestrator.BuildPrompt` (`pkg/orchestrator/run.go`) across all `when` variants:

1. **Refusal with uncommitted/unpushed work vs. `force: true` (`rl-at`)**:
   - Calling `resume_later` (`{"reason": "Hold off 1m for rate window", "when": {"at": "1m"}}`) when `UnpushedWorkReader` reports dirty/unpushed changes was refused with `IsError: true`:
     ```text
     Not stopped: the sandbox does not survive a park, and this would be lost with it:
     2 uncommitted file(s) and 1 unpushed commit(s) on grain/rl-at
     Commit and push it first (git push origin <your branch>), then call again -- or set force: true to park and lose it.
     ```
   - Passing `"force": true` succeeded:
     ```text
     Parked. This task is queued again once 2026-10-29T07:01:00Z has passed; the next run's prompt says this one was suspended, and why. This ends your run: stop here and take no further actions.

     resume_on: {"at":"2026-10-29T07:01:00Z"}
     ```
   - `GET /api/tasks/rl-at` immediately after `ProcessResult`:
     ```json
     {"id":"rl-at","state":"deferred","deferredUntil":"2026-10-29T07:01:00Z","dependsOn":null,"parked":null,"goal":null}
     ```
   - At `T+30s` (`2026-10-29T07:00:30Z`), `reconcileWake` left `rl-at` in `deferred`. At `T+60s` (`2026-10-29T07:01:00Z`), `reconcileWake` logged `orchestrator: task rl-at has reached its wake time and is picked back up` and transitioned `rl-at` to `queued`.
   - Next run prompt opening line (with Bug 1 fixed so the agent's `reason` is preserved alongside `ParkedUntilDetail`):
     ```text
     Your previous run was suspended (requested, parked: until 2026-10-29T07:01:00Z: Hold off 1m for rate window) at 2026-10-29T07:00:10Z and this is a fresh run. Anything you did not push is gone.
     ```
     And `parkedResumeSection` also populated `ResumedUntil = 2026-10-29T07:01:00Z`:
     ```text
     This run resumes from a park: the previous attempt called wait_for_task with resume_at and parked this task until 2026-10-29T07:01:00Z. That time has passed (or a person undeferred the task early), so grain queued it again.
     ```

2. **`when.ci: "my-branch"` (`rl-ci`)**:
   - Calling `resume_later` with `{"reason": "Wait for grain CI on branch before merging", "when": {"ci": "my-branch"}}` deferred `rl-ci` (`state: "deferred"`, `deferredUntil: null`) and recorded `resume_on = {"ci":"my-branch"}`.
   - Posting an unrelated message (`store.WakeOnMessage(ctx, "rl-ci", ...)`) returned `woke = false`—confirming `rl-ci` does not wake on non-matching events.
   - Recording a concluding CI run (`ci-run-901`, `conclusion: "success"`) on `grain/task-rl-ci` caused `store.DueResumeOnCI` and `reconcileWake` to log `orchestrator: task rl-ci has the CI verdict it waited for and is picked back up` and transition `rl-ci` to `queued`.
   - Next run prompt opening line:
     ```text
     Your previous run was suspended (requested, parked: resume_later until grain CI (my-branch) has a verdict: Wait for grain CI on branch before merging) at 2026-10-29T07:00:10Z and this is a fresh run. Anything you did not push is gone.
     ```

3. **`when.task` with `until: "closed"` (`rl-task-closed` -> `dep-c`) and `until: "merged"` (`rl-task-merged` -> `dep-m`)**:
   - Both tasks parked as `state: "queued"`, `parked: true`, with `dependsOn: ["dep-c"]` and `dependsOn: ["dep-m"]`, held from `store.Ready(ctx)` while `dep-c` and `dep-m` were open.
   - Once `dep-c` closed (unmerged) and `dep-m` closed (merged), both `rl-task-closed` and `rl-task-merged` entered `store.Ready(ctx)`.
   - Next run prompt for `rl-task-merged` (with Bug 1 fixed so the agent's `reason` is preserved in `task_run.detail` and `task_suspension.detail`):
     ```text
     Your previous run was suspended (requested, parked: waiting on task(s) dep-m: Wait for dep-m schema migration PR to merge) at 2026-10-29T07:00:10Z and this is a fresh run. Anything you did not push is gone.
     ```
     Followed by `parkedResumeSection` naming `Task dep-m` and `outcome merged`.

4. **`when.message: true` (`rl-msg`)**:
   - Calling `resume_later` with `{"reason": "Waiting for peer agent message with benchmark numbers", "when": {"message": true}}` deferred `rl-msg` (`state: "deferred"`).
   - Posting a comment via `uiClient.AddComment(ctx, "rl-msg", "Benchmark numbers: p99 dropped from 42ms to 19ms.", nil)` immediately woke `rl-msg` back to `state: "queued"`.
   - Next run prompt opening line:
     ```text
     Your previous run was suspended (requested, parked: resume_later until anybody posts to this task: Waiting for peer agent message with benchmark numbers) at 2026-10-29T07:00:10Z and this is a fresh run. Anything you did not push is gone.
     ```

---

### Step 3: Daemon-Checked Goals (`base-green`, `pr-merged`, `tasks-closed`, `person-closes`, `pipeline-healthy`)

We created tasks covering all 5 goal kinds via `SetGoal` (`PUT /api/tasks/{id}/goal`) and drove `reconcileTaskGoals`:

1. **`base-green` with `on_met: "close"` (`goal-bg-close`)**:
   - Spec: `{"repo":"acme/widgets","branch":"main"}`, defaults `maxResumes: 6, maxStall: 12h, onMet: "close", onLimit: "hold"`.
   - After recording `BaseBranchCI` (`Conclusion: "success"`, `Commit: "41de786a001"`, `RunID: "ci-main-green"`), `reconcileTaskGoals` closed the task with `outcome: "goal-met"`:
     ```json
     {"id":"goal-bg-close","state":"closed","goal":{"kind":"base-green","onMet":"close","onLimit":"hold","outcome":"goal-met","metAt":"2026-10-29T07:02:00Z","evidence":"`main` on acme/widgets green at 41de786a001 (grain CI run ci-main-green)"}}
     ```

2. **`pr-merged` with `on_met: "close"` (`goal-pr-close`)**:
   - Spec: `{"task":"pr-source"}`, defaults `maxResumes: 6, maxStall: 24h, onMet: "close", onLimit: "hold"`.
   - When `pr-source` recorded `PrMergedAt`, `reconcileTaskGoals` closed `goal-pr-close` with `outcome: "goal-met"` and evidence `"task pr-source's pull request merged at 2026-10-29T07:02:00Z"`.

3. **`tasks-closed` with `on_met: "rest"` (`goal-tc-rest`)**:
   - Spec: `{"tasks":["sub-1"],"until":"closed"}`, `onMet: "rest"`, `onLimit: "notify"`.
   - When `sub-1` closed, `reconcileTaskGoals` recorded `metAt: "2026-10-29T07:02:00Z"`, `evidence: "all 1 task(s) closed: sub-1"`, posted `"grain: this task's goal (tasks-closed) is met: all 1 task(s) closed: sub-1. The task rests here until a person or a message moves it."`, and transitioned `goal-tc-rest` to `state: "deferred"` (`deferredUntil: null`).

4. **`person-closes` (`goal-pc`) & Console Sessions**:
   - Spec: `{}` (`kind: "person-closes"`), defaults `maxResumes: 0, maxStall: 0, onMet: "close"`.
   - Confirmed `restOnGoal` suspends the task between runs (`ResumeOn{Message: true}`) and `reconcileTaskGoals` never auto-closes it (`evidence: "waiting for a person to close it"`). Also verified that `EnsureDirectorConsoleSession` (`pkg/orchestrator/consolesessions.go`) declares `person-closes` on console sessions and that the `durable` capability (`pkg/model/capability_policy.go`, `pkg/orchestrator/durable.go`) is person-only (`GrantableBy: "person"`) with ceiling (`50` runs / `7d`) and `48h` idle watchdog (`durable-idle`).

5. **`pipeline-healthy` with `on_limit: "notify"` (`goal-ph-notify`) vs. `base-green` with `on_limit: "hold"` (`goal-hold-del`)**:
   - With open pipeline notification `pipeline.base-red:acme/widgets:release` and `release` red at `badc0de111: go unit`, we ran 2 consecutive non-progress runs (`maxResumes: 2`) on both tasks:
     - `goal-ph-notify` (`onLimit: "notify"`): raised warning notification `task.no-progress:goal-ph-notify` and backed off with a wake time (`state: "deferred"`, `deferredUntil: "2026-10-29T07:25:30Z"`, +15m backoff).
     - `goal-hold-del` (`onLimit: "hold"`): raised warning notification `task.no-progress:goal-hold-del` and held the task indefinitely (`state: "deferred"`, `deferredUntil: null`).

6. **`DELETE /api/tasks/{id}/goal` (`ClearGoal`) on `goal-hold-del`**:
   - Calling `ClearGoal` (`DELETE /api/tasks/goal-hold-del/goal`) removed the `task_goal` row, cleared the open `task.no-progress:goal-hold-del` notification (`cleared = 1`), and un-deferred `goal-hold-del` back to `state: "queued"` (with Bug 2 fixed; see §3 below).

```text
=== SQLite: task_goal ===
task_id         kind              on_met  on_limit  outcome   resumes_since_progress  max_resumes  limit_notified
--------------  ----------------  ------  --------  --------  ----------------------  -----------  --------------
goal-bg-close   base-green        close   hold      goal-met  0                       6            0             
goal-pc         person-closes     close   hold                0                       0            0             
goal-ph-notify  pipeline-healthy  rest    notify              2                       2            1             
goal-pr-close   pr-merged         close   hold      goal-met  0                       6            0             
goal-tc-rest    tasks-closed      rest    notify              0                       6            0             

=== SQLite: notification ===
id  subject                                 family             severity  task_id         cleared  cleared_by
--  --------------------------------------  -----------------  --------  --------------  -------  ----------
1   pipeline.base-red:acme/widgets:release  pipeline.base-red  warning                   0                  
2   task.no-progress:goal-hold-del          task.no-progress   warning   goal-hold-del   1        :         
3   task.no-progress:goal-ph-notify         task.no-progress   warning   goal-ph-notify  0                  
```

---

### Step 4: Suspensions (`context`, `usage-limit` across restart & Settings lift, `capacity` model rotation, setup-interrupted)

1. **Context exhaustion (`susp-context`)**:
   - Simulated a run failing with `"claude: API Error: 400 prompt is too long: 210000 tokens > 200000 maximum"`.
   - `classifyRunEnd` classified it as `(outcome: "suspended", suspend: "context")`.
   - `lastTurnForHandover` resumed `sess-ctx-99` for a single handover turn (`maxTurns: 3`), relayed the agent's `write_handover` call as a handover comment on the task (`handover: true`), and `recordRunEnd` cleared `obs.SuspendedSession` (`<nil>`) so the next run starts a fresh session seeded with the handover rather than re-entering the full session.
   - Failure streak remained `count=0`, and the next run's prompt opened with:
     ```text
     Your previous run was suspended (context, the conversation no longer fit the model's context window; the next run starts a fresh session from the conversation and the handover) at 2026-10-29T07:01:00Z and this is a fresh run. Anything you did not push is gone.
     ```

2. **Usage-limit pause (`susp-usage`), daemon restart survival, and Settings lift**:
   - Triggered a usage-limit refusal (`UsageLimitError{ResetAt: 2026-10-29T09:00:00Z, Message: "You've hit your usage limit; resets in 2h"}`) on `conn-anthropic-main` (`model: "claude-sonnet-4-6"`).
   - `Pause.Begin` persisted the pause to `usage_limit_pause` in SQLite and `recordRunEnd` recorded `task_run.outcome = 'paused'`, `task_run.suspend_kind = 'usage-limit'`, and `task_suspension.kind = 'usage-limit'`.
   - Simulated a daemon restart by constructing a fresh `&orchestrator.Pause{}` and calling `restartedGate.Restore(ctx, store, t0+5m)`:
     - Logged: `orchestrator: restored the usage-limit pause on model claude-sonnet-4-6 on connection conn-anthropic-main (team default) until 2026-10-29T09:00:00Z`
     - `restartedGate.ModelPaused("conn-anthropic-main", "claude-sonnet-4-6", "default", t0+5m)` returned `true`.
     - Also verified `go test -v ./pkg/daemon -run TestPausedConnectionsStaySkippedAcrossARestart` (0.02s PASS).
   - Simulated an operator editing `conn-anthropic-main` in Settings (`liftEditedConnectionPauses` -> `Pause.LiftWhere`):
     - Logged: `orchestrator: the usage-limit pause on model claude-sonnet-4-6 on connection conn-anthropic-main (team default) set until 2026-10-29T09:00:00Z ... was lifted by hand; dispatch onto it resumes`
     - `ModelPaused` immediately returned `false` and the `conn-anthropic-main` row was deleted from `usage_limit_pause`.

3. **Model capacity rotation (`susp-capacity`)**:
   - Attempt 1 (`susp-capacity-1`) ran on `claude-opus-4-6` and ended with `SuspendCapacity` (`model_hint = "claude-opus-4-6"`).
   - `store.FailedOverModels(ctx, "susp-capacity")` returned `["claude-opus-4-6"]`, causing attempt 2 (`susp-capacity-2`) to rotate onto `claude-sonnet-4-6` and succeed.

4. **Setup-interrupted run (`susp-setup`)**:
   - Attempt 1 (`susp-setup-1`) ended with `outcome = "interrupted"` and `suspend_kind = "restart"` (`"the daemon driving this run was stopped while the run was still setting up, before any agent started; handed back to the queue without a strike"`).
   - Task returned to `state = "queued"` with `FailureStreak = count=0`.

```text
=== SQLite: task_suspension ===
task_id         run_id            kind         resume_on                                model_hint         detail_short                                                               
--------------  ----------------  -----------  ---------------------------------------  -----------------  ---------------------------------------------------------------------------
susp-setup      susp-setup-1      restart                                                                  the daemon driving this run was stopped while the run was still setting up,
rl-at           rl-at-1           requested    {"at":"2026-10-29T07:01:00Z"}                               parked: until 2026-10-29T07:01:00Z: Hold off 1m for rate window            
rl-ci           rl-ci-1           requested    {"ci":"my-branch"}                                          parked: resume_later until grain CI (my-branch) has a verdict: Wait for gra
rl-msg          rl-msg-1          requested    {"message":true}                                            parked: resume_later until anybody posts to this task: Waiting for peer age
rl-task-closed  rl-task-closed-1  requested    {"tasks":["dep-c"]}                                         parked: waiting on task(s) dep-c: Wait for dep-c investigation to close    
rl-task-merged  rl-task-merged-1  requested    {"tasks":["dep-m"],"until_merged":true}                     parked: waiting on task(s) dep-m: Wait for dep-m schema migration PR to mer
susp-capacity   susp-capacity-1   capacity                                              claude-opus-4-6    529 Overloaded: model capacity exhausted on claude-opus-4-6                
susp-context    susp-context-1    context                                                                  the conversation no longer fit the model's context window; the next run sta
susp-usage      susp-usage-1      usage-limit                                           claude-sonnet-4-6  agent: usage limit reached; You've hit your usage limit; resets in 2h; rese
cap-restart     cap-restart-1     capability                                                               the task's capabilities changed while it ran; restarted with a sandbox buil

=== SQLite: task_run (suspensions & capability restart) ===
id               task_id        attempt  sandbox      model              agent_framework  outcome      suspend_kind
---------------  -------------  -------  -----------  -----------------  ---------------  -----------  ------------
cap-restart-1    cap-restart    1        sbx-cap-old  claude-sonnet-4-6  claude           suspended    capability  
cap-restart-2    cap-restart    2        sbx-cap-new  claude-sonnet-4-6  claude           succeeded                
susp-capacity-1  susp-capacity  1        sbx-cap-1    claude-opus-4-6    claude           capacity     capacity    
susp-capacity-2  susp-capacity  2        sbx-cap-2    claude-sonnet-4-6  claude           succeeded                
susp-context-1   susp-context   1        sbx-ctx-1    claude-sonnet-4-6  claude           suspended    context     
susp-setup-1     susp-setup     1        sbx-setup-1  claude-sonnet-4-6  claude           interrupted  restart     
susp-usage-1     susp-usage     1        sbx-u-1      claude-sonnet-4-6  claude           paused       usage-limit 

=== SQLite: usage_limit_pause ===
scope_key                                  connection_id    model           labels  team     framework  until_at                       reason                                                                      escalated  updated_at                             
-----------------------------------------  ---------------  --------------  ------  -------  ---------  -----------------------------  --------------------------------------------------------------------------  ---------  ---------------------------------------
connection|conn-agy-backup|gemini-2.5-pro  conn-agy-backup  gemini-2.5-pro          default             2026-10-29 09:00:00 +0000 UTC  agent: usage limit reached; Quota exceeded; resets at 2026-10-29T09:00:00Z             2026-10-29 07:54:51.423478762 +0000 UTC
```

---

### Step 5: Stop Rule (`pkg/agent/stoprule.go` & `tests/e2e/stop_rule_walkthrough_test.go`)

1. **`agent.DecideStop` unit verification**:
   - Interactive turn ending with `"I have updated the handler. I'll now run the tests."` and no tool calls:
     `{Block: true, Reason: "You said you would continue. Do it now, or use \`chat\` to tell the person and wait for them."}`
   - Background turn ending with `"I have updated the handler. I'll now run the tests."` and no closing tool call:
     `{Block: true, Reason: "A reply without a tool call ends your run, and this run has not finished: nothing has been pushed and nothing has been reported..."}`
   - Background turn ending with `comment_on_issue`:
     `{Block: false, Reason: ""}`
2. **End-to-end single-resume guarantee (`TestWalkthroughARunThatEndsOnAPromiseIsResumedOnce`)**:
   - Ran `go test -v ./tests/e2e -run TestWalkthroughARunThatEndsOnAPromiseIsResumedOnce` (PASS in 4.09s): confirmed `continueStoppedRun` in `pkg/orchestrator/stoprule.go` resumes the session exactly once within the same run (`sess-promise`) and records `grain:stop-blocked` in telemetry without looping.

---

### Step 6: Grant-Change Restarts & Playwright Verification

1. **Orchestrator capability-change restart (`cap-restart`)**:
   - Started run `cap-restart-1` in sandbox `sbx-cap-old` with grant `["memory"]`.
   - Added sandbox-mounted capability `"gcp-key"` to the live task:
     - First `reconcileCapabilityChanges` tick posted the capability-change addendum (`"grain: a capability of this task changed..."`) and started the 30s grace timer (`capabilityRestartGrace`).
     - Within the grace window (`+29s`), the run remained uncancelled.
     - At `+30s`, `reconcileCapabilityChanges` cancelled the run with `errCapabilityChanged`, recording `outcome = "suspended"`, `suspend_kind = "capability"`.
     - Attempt 2 (`cap-restart-2`) started in a fresh sandbox `sbx-cap-new` with the new capability set.
   - Adding an `api:*` grant (`api:tasks-read`) was applied in-place without restarting the sandbox (`capabilityNeedsSandbox == false`).

2. **Playwright E2E & Visual Layout Verification**:
   - Ran `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/goal-section-layout.spec.js e2e/session-pending-grants.spec.js`:
     - `e2e/goal-section-layout.spec.js`: PASS (2.2s) — verified Goal section and open Goal editor fit inside the 260px detail column at both desktop (`1440x900`) and phone (`390x844`) widths.
     - `e2e/session-pending-grants.spec.js`: PASS at desktop (1.2s) and phone (895ms) — verified adding a capability during a live console session turn displays the `"New capabilities pending: ... Apply now"` banner and clicking `"Apply now"` triggers `POST /api/sessions/{name}/restart-sandbox` and clears the banner.
   - Verified `GoalSection.jsx` with `onLimit: "notify"` and `limitNotifiedAt` set at desktop (`1440x900`) and phone (`390x844`) viewports in both light and dark modes (`fitsInCol=true`, `pageErrors=0`).

---

## 3. Findings & Fixes

### Bug 1: `resume_later` with `when.at` or `when.task` dropped the agent's `reason`
- **Symptom:** Calling `resume_later` requires a `reason` argument whose schema states: *"Why the run is stopping and what the next one is to do: recorded on the run and shown to the next one."* However, when `resume_later` was called with `when.task` or `when.at` (without `ci` or `message`), `requestedSuspension` in `pkg/orchestrator/finish.go` overwrote `susp.Detail` with `model.ParkedDetail(targets)` (`"parked: waiting on task(s) <ids>"`) or `model.ParkedUntilDetail(*when.At)` (`"parked: until <RFC3339>"`), discarding `reason` completely from `task_run.detail`, `task_suspension.detail`, and the next run's opening `SuspensionNote`.
- **Root Cause:** `model.ParkedTargets` and `model.ParkedUntil` in `pkg/model/wait_target.go` parsed `detail` by assuming nothing followed the task IDs or RFC3339 timestamp after the prefix, so `requestedSuspension` omitted `reason` to avoid breaking `ParkedTargets` / `ParkedUntil`.
- **Fix:**
  - Updated `ParkedTargets` and `ParkedUntil` in `pkg/model/wait_target.go` to strip an optional `": <reason>"` suffix (`strings.Cut(rest, ": ")`) before parsing the space-separated task IDs or RFC3339 timestamp.
  - Updated `requestedSuspension` in `pkg/orchestrator/finish.go` to append `": " + reason` (when non-empty) to `ParkedDetail` and `ParkedUntilDetail`.
  - Added regression tests in `pkg/model/requested_suspension_test.go` (`TestResumeOnRoundTrips`) and `pkg/orchestrator/wait_for_task_test.go` (`TestResumeLaterPreservesReasonOnTaskAndTimedParks`).

### Bug 2: `DELETE /api/tasks/{id}/goal` (`ClearGoal`) and `PUT /api/tasks/{id}/goal` (`SetGoal`) left `task.no-progress:<id>` notifications open and goal-deferred tasks stranded
- **Symptom:** When a task's goal hit its progress limit (`applyGoalLimit`), the orchestrator raised operator notification `task.no-progress:<id>` and deferred the task (`SetDeferred`). Similarly, when a goal task rested between runs (`restOnGoal`) or met an `on_met: "rest"` goal (`meetGoal`), the orchestrator deferred the task indefinitely. If a user or operator subsequently removed the goal via `DELETE /api/tasks/{id}/goal` (`ClearGoal`), `ClearGoal` only deleted the `task_goal` row:
  1. Because `reconcileTaskGoals` only iterates over `store.OpenTaskGoals(ctx)`, the open `task.no-progress:<id>` alert was never cleared and remained stuck in the Inbox.
  2. The task remained stranded in `deferred` state with no wake time and no goal reconciler to ever wake or close it.
  3. Similarly, replacing a goal via `PUT /api/tasks/{id}/goal` (`SetGoal`) reset `limit_notified_at = NULL` in SQLite without clearing the open `task.no-progress:<id>` notification or un-deferring a task held on the old goal's limit.
- **Fix:**
  - Updated `SetGoal` and `ClearGoal` in `pkg/ui/taskgoal.go` to clear any open `task.no-progress:<id>` notification (`c.Store.ClearNotification`) and un-defer the task (`c.Store.SetDeferred(ctx, id, nil, nil, now)`) when its deferral came from the replaced/removed goal (`deferredByGoal`).
  - Added regression test `TestClearAndReplaceGoalClearsNoProgressNotificationAndDeferral` in `pkg/ui/wait_task_test.go`.

### Bug 3: `GoalSection.jsx` mislabeled `on_limit: "notify"` goals at their limit as `"Held at its limit: a person has to act."`
- **Symptom:** In `ui/src/components/detail/GoalSection.jsx` (line 160), `{goal.limitNotifiedAt && " Held at its limit: a person has to act."}` was rendered whenever `goal.limitNotifiedAt` was set, even when `goal.onLimit === "notify"` (where the task is not held waiting for a person, but notifies the operator and backs off exponentially between resumes).
- **Fix:**
  - Updated `ui/src/components/detail/GoalSection.jsx` to render `" Limit reached: operator notified, backing off."` when `goal.onLimit === "notify"` and `" Held at its limit: a person has to act."` otherwise.
  - Added unit test `"distinguishes hold vs notify when the goal is at its progress limit"` in `ui/src/components/detail/GoalSection.test.jsx`.

---

## 4. Outcome

All §2 items of the 2026-10-28 test plan were executed and verified against SQLite, the HTTP API, and Playwright. Three bugs uncovered during the walkthrough were fixed with unit and integration regression tests.
