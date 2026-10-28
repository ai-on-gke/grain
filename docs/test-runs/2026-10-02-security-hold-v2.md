# Security hold v2 and proposal auto-submit narrowing — walkthrough report

**Date**: 2026-10-03 (the 2026-10-02 plan's run)
**Plan**: `docs/test-plans/2026-10-02.md` §3 (task 1528). The plan file is on `origin/candidate` (`7e2f08b32`) but not yet on `main`. It was read from there.
**Task**: 1575
**Branch tested**: `main` at `5a63bb2d1`. `git branch -r --contains 35409b738` lists `origin/main`, and so do `216c7368e`, `4f4e38b89`, `528ed8153`, `517dff25d`, `50fd42127` and the 2026-10-01 harness commit `1466373b7`. `trustboundary.Version` is `trust-boundary/v2`.
**Environment**: Debian 12 (bookworm), kernel 6.1.0-53-amd64, Go 1.26.6, Node 20.19.0, npm 10.8.2, git 2.39.5, and Playwright 1.62.1 with headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). The production frontend bundle was built into `pkg/ui/static` with `npm ci && npm run build`.

## Stubs (external boundaries only)

- **GitHub**: `githubsim` behind a real `httptest` server, which also serves real git smart-HTTP over a bare repository. Commit B is pushed over that server.
- **The model vendor**: a scripted `antigravity.NewForTest` run, chosen per task from the task id in the run's prompt. This is the 2026-10-01 harness (`newHoldWorld`), reused unchanged.
- **A counting transport** in front of the sim, for step 4 only. It counts `GET /repos/{o}/{r}/pulls/{n}/files` and forwards every request unchanged.

Nothing of grain's own is mocked. The store (embedded SQLite), `ui.Client`, `orchestrator.RunCycle`, `SyncPullRequests` (the merge queue), `SyncSecurityHolds`, `ui.NewServer` and the React bundle are all real.

## `grain demo`, and what ran where

- **What `grain demo` can't do**: it has no orchestrator, no GitHub and no agent backend. This is the same finding as the 2026-10-01 report, and it is still true. So it can't run a hold, a review or the merge queue.
- **Steps 1–4 and 5 (relayed)**: these ran as Go e2e tests on the 2026-10-01 harness, which the task names as "the same harness".
- **Step 5 (live) and the UI assertion**: these ran against a real `grain demo`. Proposals were filed through its `/api/tasks` endpoint, and Playwright checked them against the production bundle.

## Commands

```sh
git fetch origin && git branch -r --contains 35409b738     # origin/main (and others)
cd ui && npm ci && npm run build && cd ..                  # pkg/ui/static
go test ./tests/e2e/ -run 'TestSecurityHoldV2Walkthrough|TestWidenedProposalsWalkthrough|TestSecurityHoldWalkthrough|TestCapabilityEscalatedProposalsWalkthrough' -count=1 -v
go test -count=1 ./pkg/ui/ ./pkg/model/... ./pkg/trustboundary/
go test -count=1 -run 'Propos|SecurityHold|Relay|AutoSubmit|Ungranted' ./pkg/orchestrator/
go build -o /tmp/grain ./cmd/grain && /tmp/grain demo -addr 127.0.0.1:18080 -open=false
#   POST /api/tasks {"title":"auto-merging parent …","approved":true,"autoMerge":true}   -> task 13
#   POST /api/tasks {"proposedBy":"13","reads":["acme/secret-sauce"],"autoMerge":true}    -> task 14
#   POST /api/tasks {"proposedBy":"13","writes":[{"repo":"grain/config","base":"main"}],"autoMerge":true} -> task 15
node proposed-check.mjs                                    # Playwright, see the appendix
make security                                              # go vet ./... + tests/security with -race: ok (140.7s)
```

## Steps: expected vs observed

Steps 1–5 are the subtests of `tests/e2e/security_hold_v2_walkthrough_test.go`. Step 6 is `tests/e2e/security_hold_walkthrough_test.go`, re-run unchanged.

| # | Step | Expected | Observed |
|---|------|----------|----------|
| 1 | Auto-merge task's run commits `docs/guide.md` only (commit A) and finishes | Not held. `trust-boundary-cleared` = `trust-boundary/v2@<A>`. Not merged before the PR reads mergeable | As expected. |
| 2 | Commit B (new `pkg/mcp/sandbox_tools.go`) pushed to `grain/task-N` over the git server, outside any run. PR then reads clean for 4 reconciler and merge-queue cycles. The review parks on a question | No merge. `clearedForHead` reclassifies at B. Then a `security-hold` = `trust-boundary/v2` link, a review stacked on the branch naming the path and `exec-sandbox`, and exactly one PR comment | As expected. Auto-merge was withdrawn, and `trust-boundary-cleared` moved to `v2@<B>`. One "**grain security hold**" comment over 4 cycles, naming `pkg/mcp/sandbox_tools.go`. The branch never reached `main`. |
| 3 | The same, with B = `pkg/trustboundary/trustboundary.go`. The review finishes clean, with no findings and no commits | Held for a human. No releasable `security-hold` link. Still not auto-merged after the review completes | As expected. The review reached `completed`/`closed`. Over 6 cycles auto-merge stayed off and nothing merged. The single PR comment says changes to **hold-policy** "never merge automatically". |
| 4 | No B. The cleared, unmoved head merges | Exactly one file-list read (at the finish), none from the merge queue | As expected. 1 `pulls/{n}/files` request in total. The PR merged within 4 cycles, with no hold comments. |
| 5a | Relayed at the end of the run: `propose_task` with `reads: [acme/secret-sauce]`, and separately `writes: [{acme/gadgets, main}]`, from an auto-merge parent | Both proposed and unapproved. The body says why | As expected. Both are `proposed` with `Approval == nil`, and each carries the ungranted-reads or ungranted-writes note. The writes proposal has `AutoMerge=false`. The reads proposal keeps `AutoMerge=true`; see finding 2. |
| 5b | The same two filed live (`ui.CreateTask` with `ProposedBy`, which is how an agent's `propose_task` arrives over the API) | Same as 5a | **Failed on `main` (finding 1).** The reads proposal was **queued** and approved by `auto-submit`, and neither proposal carried a note. Passes after the fix. |
| 5c | `grain demo` + Playwright: tasks 14 (reads) and 15 (`writes: grain/config`) | Both shown under Proposed | With the fix: both rows carry `.badge-proposed` in the task list and appear in the Inbox with an **Approve** button. `/tasks/14` shows the "asks to read `acme/secret-sauce`" note. No page errors. On a `main` build of `grain demo`, the same reads request came back `state: queued, autoMerge: true` with no note (the reproduction for finding 1). |
| 6 | Re-run the 2026-10-01 security-hold walkthrough unchanged | Passes against v2 | **Pass**. All 9 `TestSecurityHoldWalkthrough` and 3 `TestCapabilityEscalatedProposalsWalkthrough` subtests pass. The file was not modified. |

## Findings

1. **Live proposals bypassed the reads/writes narrowing (fixed).**
   - **What was wrong**:
     - `517dff25d` and `50fd42127` added the `UngrantedReads` and `UngrantedWrites` gates. Both changed `orchestrator.relayProposedTasks` (the end-of-run relay) but not `ui.CreateTask`, which is the live path.
     - `ui.CreateTask` built the child it passes to `model.AutoSubmits` without `Reads`, so the reads gate never fired.
     - It held back a `TasksApproved` console session's approval only for escalated capabilities, never for ungranted reads or writes.
     - It never added the notes saying why.
   - **Effect on `main`**:
     - From an auto-merge parent, a live proposal with `reads: [owner/other-repo]` was queued by `auto-submit`. Its sandbox could then clone a repository the parent could not reach. This is the minting #1437 closed for the relay.
     - From a trusted console session, proposals with ungranted reads or ungranted writes were both queued.
   - **Deterministic reproduction**, any one of:
     - `TestLiveProposalWideningReadsOrWritesWaitsForAPerson`. With the fix stashed it fails three ways: auto-merge parent + reads → queued, session + reads → queued, session + writes → queued.
     - `TestWidenedProposalsWalkthrough/filed_live…`.
     - The `grain demo` curl above.
   - **Fix**:
     - `ui.CreateTask` now checks the child with its `Reads`, and holds the session's approval for ungranted reads or writes.
     - It appends the same notes the relay does. They moved to `model.UngrantedWritesNote` and `model.UngrantedReadsNote` so both paths word them the same way.
     - Auto-merge is still decided without `Reads`, as `model.AutoSubmits` documents.
2. **The plan expects `AutoMerge=false` on the reads proposal, but the code deliberately keeps it (plan vs design; not changed).**
   - **What the code does**: `model.AutoSubmits` and `docs/data-model.md` say "Reads gate the queueing only, not the child's auto-merge". A human approving the wider read approves the proposal as asked. The relay has always kept a same-branch reads proposal's inherited auto-merge.
   - **What this walkthrough asserts**: the documented behaviour. The fix keeps the live path consistent with it.
   - **Open question**: if the plan's stricter expectation is what is wanted, that is a product decision, not something to change here.
3. **The 2026-10-01 findings, revisited.**
   - **Finding 2 (no PR comment) is resolved.** `securityHoldNote` now posts on the pull request too, once per state change. Steps 2 and 3 count exactly one.
   - **Finding 3 (the UI can't show a hold) is still open.** `ui.TaskDetail` still has no `links`, so a held task still looks like any other awaiting-submit task.
4. **"The 2026-10-01 security-hold Playwright spec" is a Go e2e test.** `1466373b7` added `tests/e2e/security_hold_walkthrough_test.go`. The UI half of that run was a temporary harness that was never committed. Step 6 therefore re-ran the Go spec.

No defect was found in the hold itself:
- reclassification at a moved head;
- the human-only rule for `hold-policy`;
- one comment per settled state;
- no second file-list read for an unmoved head.

## Fixes made in this task

- `fix(ui)`: finding 1, with the regression test `TestLiveProposalWideningReadsOrWritesWaitsForAPerson` in `pkg/ui/live_proposal_test.go`. The fix touches `pkg/orchestrator/finish.go`, but only to call the moved note helpers. That puts this change on the `exec-sandbox` trust boundary, so it is reviewed by the security hold before it merges. `securityhold.go` and `pkg/trustboundary/` were not touched.
- `test(e2e)`: `tests/e2e/security_hold_v2_walkthrough_test.go` (steps 1–5).

## Not run, and why

- **`grain demo` as the orchestrator driver**: it has no orchestrator, GitHub or agent (see above).
- **Pushing B through grain's `pkg/gitproxy`**: B is pushed over the sim's git smart-HTTP server instead. What the hold must catch is a head that moved without a run finishing, and that is the same whichever proxy carried the push.
- **The relayed `writes: grain/config` variant**: not run end to end. On `grain demo` (live), `grain/config` is accepted and filed proposed with `AutoMerge=false`. `acme/gadgets` was refused there as not on the demo's target-repo list, which is the expected validation.

## Appendix: Playwright check (temporary, not committed)

```js
import { chromium } from "@playwright/test";
const base = "http://127.0.0.1:18080";
const titles = ["walkthrough: follow-up reading acme/secret-sauce", "walkthrough: follow-up landing in grain/config"];
const browser = await chromium.launch();
const page = await browser.newPage();
await page.addInitScript(() => localStorage.setItem("grain.username", "operator"));
await page.goto(base + "/");
for (const t of titles) {
  const row = page.locator(".task-row", { hasText: t });
  await row.waitFor();
  console.log(t, await row.locator(".badge-proposed").count());          // 1, 1
}
await page.getByRole("button", { name: /^Inbox/ }).click();
for (const t of titles) await page.locator(".inbox-item", { hasText: t }).waitFor();  // both present, each with Approve
await page.goto(base + "/tasks/14");
await page.getByText("asks to read").first().waitFor();                 // the ungranted-reads note
```
