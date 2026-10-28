# Configurable Auto-Approval of Task Proposals Walkthrough (2026-09-22)

## Build under test

- Repository/branch: `bwsalmon/grain`, `grain/task-319`
- Commit: `2d57408521192123e1bd9948dffb4fd0c4c4b5f9` (`Merge pull request #1824 from bwsalmon/grain/task-311`)
- Environment: Debian 12 amd64 sandbox; Go 1.26.6; Node 20.19.0; SQLite 3.40.1.
- Target plan: `docs/test-plans/2026-09-22.md` §2 ("Configurable auto-approval of task proposals from schedules and suites").

## Walkthrough Procedure and Assertions

A comprehensive end-to-end walkthrough test suite was executed in `tests/e2e/schedule_auto_approve_proposals_test.go` (`TestScheduleAutoApproveProposalsWalkthrough`) verifying the complete lifecycle of configurable auto-approval of proposals, proposal propagation across generations, boundary caps (depth and fan-out limits), and suite integration:

1. **Schedule with `auto_approve_proposals: true`**:
   - Defined and filed schedule `sched-auto-approve` with `auto_approve_proposals: true` and `auto_merge: false`.
   - Executed daemon cycle `orchestrator.RunCycle` to fire the scheduled run, verifying parent task instantiation with `AutoApproveProposals: true` and pre-approved attribution.
   - Dispatched the parent task via `dispatch.Cycle` and simulated agent execution issuing two `propose_task` tool calls (`fix-1` and `fix-2`).
   - Completed the task via `orchestrator.ProcessResult`.
   - Verified that both proposed tasks were immediately marked approved with `Actor: model.AutoSubmitPrincipal` and transitioned to `state: queued` (`StateQueued`) in SQLite and the UI client API (`client.Task`).
   - Verified that neither proposal lingered in `StateProposed` or appeared in the Inbox proposals list.

2. **Multi-generation depth limits (`DefaultAutoSubmitDepth = 2`)**:
   - Tested self-approval propagation depth bounds starting from root task (Level 0) with `AutoApproveProposals: true` and `AutoMerge: true`.
   - Level 0 proposed Child 1 (Level 1) -> verified Child 1 was auto-approved with `AutoSubmitPrincipal` (depth 1 <= 2).
   - Dispatched Child 1; agent execution proposed Child 2 (Level 2) -> verified Child 2 was auto-approved with `AutoSubmitPrincipal` (depth 2 <= 2).
   - Dispatched Child 2; agent execution proposed Child 3 (Level 3) -> verified Child 3 exceeded `DefaultAutoSubmitDepth = 2`.
   - Asserted that Child 3 was safely quarantined in `StateProposed` with `Approval: nil` (appearing in Inbox proposals for manual operator review), and its body was automatically appended with explanatory notice: `this would be 3 levels of self-approved work below the last person to approve anything, and 2 is the limit`.

3. **Single-run fan-out limits (`DefaultAutoSubmitFanOut = 5`)**:
   - Triggered an auto-approving scheduled task that called `propose_task` for 6 distinct follow-up tasks in a single agent run.
   - Asserted that tasks 1 through 5 were automatically approved with `AutoSubmitPrincipal` and placed in `StateQueued` ready for dispatch.
   - Asserted that task 6 exceeded the fan-out limit, remaining in `StateProposed` (`Approval: nil`) with notice appended: `task <ID>, which proposed this, is an auto-merge job, so this would normally have been queued without anybody approving it. It is waiting for you instead because task <ID> has already had 5 proposals queued that way, which is the limit`.

4. **Schedule without `auto_approve_proposals` (`auto_approve_proposals: false`)**:
   - Defined schedule `sched-no-auto-approve` with `auto_approve_proposals: false`.
   - Fired schedule and ran agent calling `propose_task`.
   - Asserted that the proposed task remained unapproved (`Approval: nil`), was stored in `StateProposed`, and correctly matched the Inbox proposals filter (`state === "proposed"`).

5. **Suite with `auto_approve_proposals: true`**:
   - Created test template `tmpl-audit` and composed suite `suite-auto-audit` with `AutoApproveProposals: true`.
   - Started suite run via `store.CreateSuiteRun`, verifying the instantiated suite task inherited `AutoApproveProposals: true`.
   - Dispatched suite task; agent execution called `propose_task`.
   - Asserted that the suite follow-up task was automatically approved with `AutoSubmitPrincipal` and queued in `StateQueued`.

## Test Results and Verification

- End-to-end walkthrough test (`go test -v ./tests/e2e -run TestScheduleAutoApproveProposalsWalkthrough`): **PASS** (2.16s).
- Full Model package test suite (`go test ./pkg/model/...`): **PASS** (82.98s).
- SQLite model store test suite (`go test ./pkg/model/sqlite/...`): **PASS** (110.81s).
- Orchestrator package test suite (`go test ./pkg/orchestrator/...`): **PASS** (206.15s).
- UI package test suite (`go test ./pkg/ui/...`): **PASS** (169.97s).
- Full E2E test suite (`go test ./tests/e2e/...`): **PASS** (53.98s).
