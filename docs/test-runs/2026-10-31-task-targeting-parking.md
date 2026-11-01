# Test run — 2026-10-31 — task targeting and parking (plan 2026-10-30 §7)

Walkthrough of §7 of `docs/test-plans/2026-10-30.md`: fork head labels
(`owner:branch`) as a task's branch, off-target pull request links pruned on
retarget, `resume_later` park reasons and goal clean-up, and infra-review
proposals that target source repositories (PRs #178, #149, #137, #197).

Run by task 2016 (attempt 5; attempts 1–4 were paused on usage limits before
pushing anything).

## Environment

- `main` at `7c652762`; UI built with `npm ci && npm run build`, `grain` with
  `go build ./cmd/grain`, so the demo embeds the real UI.
- `grain demo -github-sim -fake-ci -git-proxy -fork-pull-request -reconcile 2s -notifications -open=false -addr 127.0.0.1:4391`
  (later `:4392` on the fixed build). Over plain HTTP on loopback; the access
  code is presented as a bearer token to the API and as the
  `grain_access` session cookie to Chromium.
- Chromium through Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`),
  at 1440×900 and 390×844, in light and dark (`colorScheme`), using ad-hoc
  scripts outside the committed suite.
- `sqlite3` installed with apt, so the store's `task_link`, `task_goal` and
  `notification` rows could be read (and, where noted, written).

### Test-only demo seed: `-fork-pull-request`

The simulator already models a fork's pull request (`githubsim.PullRequest.HeadRepo`,
and the owner-aware `GET /pulls?head=` lookup), but nothing in the demo seeded
one. The new `grain demo -github-sim -fork-pull-request` flag
(`pkg/demo/demo_fork_pull_request.go`) seeds two pull requests from the fork
`contrib/widgets`:

| Label | PR | State |
|---|---|---|
| `contrib:fix/readme-typo` | acme/widgets#41 | open |
| `contrib:feat/old-idea` | acme/widgets#42 | closed |

Each one's commit sits at `refs/pull/N/head` in the sim's bare repo, which is
where GitHub keeps a fork PR's head. That's the ref a run is told to fetch.
`PullRequestBase` is shifted so grain's own first pull request is still #9000.
The flag is opt-in and changes nothing for any other demo
(`TestForkPullRequestSeedResolvesTheOpenOneOnly`).

## Results

### Gap 1: fork label end to end over `-github-sim`: **passes, with one copy bug (fixed, F1) and one gap (F2)**

On the New task form at all four sizes (1440/390 × light/dark):

- `a:b:c` in *Base branch* sets `aria-invalid="true"` and shows
  `"a:b:c" has more than one ':': a fork head is <owner>:<branch>`.
- `contrib:feat/old-idea` (closed PR) shows the inline help, and *Create task*
  is refused with 400. Before the fix the refusal read
  `… but no open pull request on acme/widgets has it as its head: no open pull request heads it`
  (**F1**). After it, the message ends at "… has it as its head".
- `contrib:fix/readme-typo` shows `fork-head-help`:
  "GitHub fork head: branch fix/readme-typo on contrib's fork. grain files
  this task against the open pull request on acme/widgets it heads." Its box
  sits under the field and inside the field's width at both widths (1440:
  field x=904 w=512, help x=918 w=484; 390: field x=24 w=342, help x=38
  w=314). *Create task* gives 201 (tasks 22–25).
- The filed task has base `main` (the PR's base) and branch `grain/task-22`,
  and the store holds `external-pr acme/widgets#41`. Its description appends
  the guidance: filed against `contrib:fix/readme-typo` from the fork
  contrib/widgets, linked to acme/widgets#41, "grain cannot push to a fork's
  branch", `git fetch origin refs/pull/41/head` + `git reset --hard FETCH_HEAD`,
  and open a follow-up PR that credits @contrib.
- **The run's push.** I played the run through the demo's real git proxy:
  `POST /api/e2e/git-proxy/runs {"taskId":"22"}` minted the sandbox token.
  - `git fetch origin refs/pull/41/head` through the proxy got the fork
    commit `68a1500 Fix a typo in the README`.
  - A commit on top pushed to `grain/task-22` in acme/widgets. A push to any
    other ref was refused: "a sandbox may push only its own task's branch".
  - `POST /api/tasks/22/pull-request` opened the follow-up acme/widgets#9000
    from `grain/task-22`, now linked `fixes acme/widgets#9000` beside
    `external-pr acme/widgets#41`.
- **F2:** `GET /api/tasks/<id>` has no field for the `external-pr` link, and
  the task page shows no chip for it. The only place a person sees #41 is
  the appended description. The plan's "read links with `GET /api/tasks/<id>`"
  can't be done for this link kind; I read `task_link` with sqlite3 instead.
  Filed as a follow-up.

### Gap 2: retarget in the UI: **passes for what the demo can show (F3 filed)**

- Task 23, also filed against `contrib:fix/readme-typo`, was given an open PR:
  I pushed `grain/task-23` straight to the sim's bare repo (the proxy path was
  already shown on task 22, which was now *running*, and the Repos section is
  read-only while a task runs), then `POST /api/tasks/23/pull-request` opened
  acme/widgets#9001. A `security-hold` link pinned to the head was written to
  the store by hand, standing in for a trust-boundary review.
- Moving it to `acme/gizmos` in the Repos picker ("Other…") was first refused
  with 400: "acme/gizmos is not on this deployment's target repo list …". That
  is correct. After `POST /api/repos {"repo":"acme/gizmos"}` the same edit
  gave `PATCH /api/tasks/23` 200.
- After the move, the header's `· acme/widgets#9001` chip is gone at 1440 and
  390, light and dark ("as of just now" alone). In the store, `fixes
  acme/widgets#9001` and the `security-hold` are both gone.
- **Not shown in the demo:** "the next PR links cleanly" and "closing the old
  PR does not close the task". The sim serves only acme/widgets, so no PR can
  open on acme/gizmos, and `grain demo` runs no pull-request sync. Both are
  covered by `TestSyncPullRequestsDropsAPullRequestInTheRepoATaskWasMovedOff`
  and `TestSyncPullRequestsDoesNotCloseATaskWhenThePullRequestItWasMovedOffCloses`
  (`pkg/orchestrator/sync_retarget_test.go`), which pass on this branch.
- **F3:** the move keeps `external-pr acme/widgets#41` and the fork guidance
  in the description. The next run on acme/gizmos would be told to
  `git fetch origin refs/pull/41/head` from a repo that has no PR #41. Filed
  as a follow-up.

### Gap 3: park reason in a real run: **passes (Go test)**

`grain demo` never runs an agent, so no run ends through `ProcessResult`
there. New test `TestResumeLaterReasonIsQuotedInTheConversationAndTheNextPrompt`
(`pkg/orchestrator/task_targeting_parking_test.go`) ends a run through the
real `orchestrator.ProcessResult` with `resume_later {reason, when.task}`. It
then reads the task as `GET /api/tasks/{id}` serves it (`ui.Client.GetTask`):
the run's attempt entry, which the conversation timeline draws, quotes the
reason. Once the awaited task closes, `BuildPrompt` for attempt 2 quotes the
reason under "resumes from a park" and names the task.

### Gap 4: goal clean-up in the UI: **passes**

The demo doesn't run the goal reconciler that raises `task.no-progress`, so
the raised state was staged on task 24:

- a real `PUT /api/tasks/24/goal` (`tasks-closed` on #25, `on_limit: hold`);
- then, written to the store: `task_goal.limit_notified_at`, `task.deferred_at`,
  and a `task.no-progress:24` notification row.

The task then showed as `deferred/awaiting-goal/tasks-closed`, and the bell
listed "Task 24 has made no progress". The Inbox does not list this family.

- **Replace** (1440 light): *Edit goal* → *Until* "merged" → *Save goal* (PUT
  200). The notification is cleared by `human:debian`, `limitNotifiedAt` is
  null, and the task is back to `queued/ready`.
- **Delete** (1440 light and 390 dark, re-staged each time): *Remove goal*
  (DELETE 200). The bell's entry goes from 1 to 0, the goal is null, and the
  task is `queued/ready`.

### Gap 5: infra-review relay: **passes (Go test)**

New test `TestInfraReviewRunFilesOnlyItsSourceRepoProposal` ends a
`system:infra-review` run (`TemplateID == model.InfraReviewTemplateID`)
through `ProcessResult` with three `propose_task` calls:

- (a) a fix on acme/widgets, a repo the deployment targets: **filed** against
  acme/widgets;
- (b) `unknown/elsewhere`: refused;
- (c) grain/config with no parameter: refused.

The run's conversation names both refused proposals and says they name no
`grain_config` parameter. `TestRelayProposedTasksAllowsInfraReviewCodeProposalsForSourceRepo`
already covered `relayProposedTasks` itself. This test covers the run's end.

## Findings

| # | Finding | Outcome |
|---|---|---|
| F1 | A closed or absent PR's fork label is refused with "… has it as its head: no open pull request heads it": `%w` appended the sentinel's own text to a message shown verbatim on the form. Reproduce: `grain demo -github-sim -fork-pull-request`, file with base `contrib:feat/old-idea`. | **Fixed** (`pkg/forkhead`): an error type that matches `ErrNoPullRequest` under `errors.Is` without repeating it. Regression check in `TestResolveNamesALabelNoPullRequestHeads`, which fails before the fix. Before/after shot at 1440 light; re-checked at all four sizes. |
| F2 | The `external-pr` link a fork label adopts is not in `GET /api/tasks/<id>` or on the task page. | Follow-up filed (UI and API change, needs its own screenshots). |
| F3 | Retargeting a fork-label task keeps the off-target `external-pr` link and the fork guidance in its description. | Follow-up filed. |

## Not done here

- Two steps need a pull-request sync the demo doesn't run: a new PR on the
  task's new repo, and closing the old PR on the sim. Both rely on the
  existing `sync_retarget_test.go` tests (above).
- The `security-hold` link and the goal's raised state were written to the
  store by hand, standing in for the trust-boundary review and the goal
  reconciler. Everything that clears them is the real code.
