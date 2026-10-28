# Trust-boundary security hold and capability-escalated proposals — walkthrough report

**Date**: 2026-10-01
**Plan**: `docs/test-plans/2026-10-01.md` §1 (task 1410)
**Branch tested**: `main` at `9e265140f0505ca5bfeea3c761a2f3df5059b4e3`. It contains `25b12a037` (trust-boundary security hold), `e482cb075` (capability-escalated proposals) and the later `35409b738` (hold bound to the classified commit, list widened to `trust-boundary/v2`).
**Environment**: Debian GNU/Linux 12 (bookworm), kernel 6.1.0-53-amd64, Go 1.26.6, Node 20.19.0, git 2.39.5, Playwright 1.62.1 with headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), frontend built into `pkg/ui/static` with `npm ci && npm run build`.
**Stubbed**: GitHub only, as `githubsim` behind a real `httptest` server that also serves real git smart-HTTP over a bare repository. The model vendor is a scripted `antigravity.NewForTest` run, chosen per task from the task id in the run's prompt. A failing-files transport wraps the sim for one scenario. Grain's store (embedded SQLite), `ui.Client`, orchestrator (`RunCycle`), merge queue (`SyncPullRequests`) and `SyncSecurityHolds` reconciler are all real and unmocked.

## A note on the plan and on `grain demo`

- **The plan file**: `docs/test-plans/2026-10-01.md` was not on any ref this sandbox can reach. There is no `grain/task-1353` branch, and `refs/pull/1353` is an unrelated September PR. The scenario list in task 1410's own description was used as the plan, and every item on it is covered below.
- **`grain demo` ran**: `go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false` started and served 12 seeded tasks. None of them is security-held.
- **What `grain demo` cannot do**: it builds a `ui.NewServer` over a seeded store and has no orchestrator, no GitHub and no agent backend. So it cannot run the hold, the review, the reconciler or the merge queue.
- **What was used instead**: the orchestrator scenarios run as a Go e2e test against the real orchestrator. For the UI half, the same real `ui.NewServer` the demo uses was served over the store the orchestrator had just written, and driven with Playwright. That temporary harness is in the appendix.

## Commands

```sh
cd ui && npm ci && npm run build && cd ..                       # pkg/ui/static
go test ./tests/e2e/ -run 'TestSecurityHoldWalkthrough|TestCapabilityEscalatedProposalsWalkthrough' -count=1 -v
go test -race -count=1 -run 'TestSecurityHoldWalkthrough|TestCapabilityEscalatedProposalsWalkthrough|AutoMerge|ListPullRequestFiles|Merge' ./tests/e2e/
go test -count=1 ./pkg/trustboundary/... ./pkg/github/...
go test -count=1 -run 'SecurityHold|Session|Propos|Escalat|AutoSubmit' ./pkg/orchestrator/ ./pkg/model/ ./pkg/ui/
make security                                                   # go vet ./... + tests/security with -race: ok (117.8s)
go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false           # served; seeded data only
# UI half: see the appendix
```

## Scenarios: expected vs observed

All of these are subtests of `tests/e2e/security_hold_walkthrough_test.go`. Each one starts from a fresh bare repository, sim and store. Tasks are filed through `ui.Client.CreateTask` with auto-merge on and approved, then driven only by `orchestrator.RunCycle`. "Mergeable" is set on the sim's pull requests between cycles, standing in for GitHub settling mergeability.

| # | Scenario | Expected | Observed |
|---|----------|----------|----------|
| 1 | Change to `pkg/gitproxy/proxy.go` | `AutoMerge` withdrawn; `security-hold` link = list version; `security-review-task` link; comment listing the matched paths; review stacked on `grain/task-N` titled "Security review: …" with the paths in its body; state `awaiting_submit` | As expected. The hold link reads **`trust-boundary/v2`**, not v1, because `35409b738` bumped the list after the plan was written. |
| 2 | Clean review (no findings, no commits) | `SyncSecurityHolds` restores auto-merge; the queue merges the PR; a comment says so | As expected. The PR merged into `main` within 6 cycles, with the comment "…finished without findings, so it merges automatically again…". |
| 3 | Held change, review parked (asks a question), PR mergeable for 4 cycles | Not batched or merged; auto-merge stays off | As expected. The branch never reached `main`. |
| 4 | Review leaves an `add_review_comment` finding | Hold dropped; auto-merge off; waits for a human | As expected. |
| 5 | Review pushes a commit to its own stacked branch | Hold dropped; auto-merge off; comment "pushed commits of its own" | As expected. |
| 6 | Human Submit (`ui.Client.Submit`) while the review is still pending | Merges | As expected. Also done through the UI with Playwright (below). |
| 7 | `.grain/baselines/lint.json` only | Held with no releasable `security-hold` link; a review is filed; after the review finishes clean, still not auto-merged | As expected. The review reached `closed` and the change stayed `awaiting_submit` with auto-merge off. The comment says "changes to **ci-suppressions** never merge automatically". |
| 8 | `docs/guide.md` only (control) | No review; merges | As expected. |
| 9 | `git mv pkg/relay/relay.go internal/relay.go` | Held | **Failed at first (finding 1).** The PR read as touching only `internal/relay.go`, was not held, and completed with auto-merge on. Passes after the githubsim fix. |
| 10 | PR file listing returns 502 | Held as if it touched everything, not releasable; the review is told the list was unreadable | As expected. |
| 11 | `propose_task` × 2 from an auto-merging parent with no capabilities: one with `capabilities: ["gcp-key"]`, one with none | The gcp-key proposal is `proposed`, holds `gcp-key`, and its body carries `CapabilityEscalationNote`; the plain one is `queued` with no note | As expected. |
| 12 | Same two proposals filed live (`CreateTask` with `ProposedBy`) from a console session with `TasksApproved: true` | Same as 11 | As expected. |
| 13 | Same two proposals from that session's own run, relayed at the end of the run | Same as 11 | As expected. The session reconciler adopts the tagged task and parks it `awaiting_reply`; a human message (`ui.Client.AddComment`) is what dispatches it. That is expected session behaviour, not a bug. |

### UI (Playwright against the real server and the built frontend)

**Store contents**: task 1 held on `pkg/gitproxy/proxy.go`, with its review (task 3) parked, and task 4 held on `.grain/baselines/lint.json`, with its review (task 5) finished clean.

- **Held task page, `/tasks/1`**:
  - Reads "Awaiting submit" and shows the **submit**, "auto-merge once checks pass", **force merge**, **run now** and **close** controls.
  - The timeline shows the review comment: the trust-boundary category, the matched path, "Filed task 3 to review it…", and how the hold ends.
  - No indicator says the task is on a security hold. It looks like any other awaiting-submit task.
  - "task 3" is plain text, not a link. The page's only links are to the PR (finding 3).
- **Review page, `/tasks/3`**:
  - Shows the full `system:security-review` instructions, "You are reviewing task 1 …", the path list, and "generated from 1".
  - Its question ("is this ref meant to be writable?") is in the timeline.
- **Baselines task page, `/tasks/4`**: the comment says changes to `ci-suppressions` never merge automatically.
- **Override**: Playwright clicked **submit** on `/tasks/1` → `POST /api/tasks/1/submit` 200. The orchestrator's next cycles merged the PR, and after a reload the page reads "PR merged" / "Closed".
- **Console**: the only console error was one 404 for a resource the stripped-down config does not serve. There were no page errors.
- **Account modal**: with no session, the first-run account modal makes the page inert. Setting `localStorage["grain.username"]` the way `ui/e2e/pageerrors.js` does gets past it.

## Findings

1. **githubsim did not report renames (fixed).**
   - **What was wrong**: `githubsim.filesChangedIn` used `git diff --name-only`, which prints only the new path of a detected rename. Real GitHub returns `status: "renamed"` with `previous_filename`, and `github.RESTClient.ListPullRequestFiles` (since `25b12a037`) reads that field. So against the simulator a rename out of `pkg/relay/` read as touching nothing on the trust boundary and kept auto-merge.
   - **Scope**: test infrastructure only. The production client is correct, and `pkg/github`'s own unit test covers `previous_filename`. But no test that runs on githubsim could exercise the rename rule.
   - **Repro**: scenario 9 above, or `TestSimListPullRequestFilesListsARenameUnderBothPaths`. Both fail with the fix reverted (checked by stashing it).
   - **Fix**: the sim now runs `git diff --name-status -M -z` and returns `filename`, `status` and `previous_filename`.
2. **The hold comment goes on the grain task, not the GitHub PR.**
   - **Expected vs observed**: the plan expects "a PR comment listing the matched paths". What exists is a grain task comment (`reviewComment` → `store.AddComment`). That comment is shown in the task's timeline, and nothing is posted to GitHub.
   - **Why it matters**: a reviewer who only looks at GitHub sees a green PR that is not merging and no reason why.
   - **Not fixed**: whether grain should post on the PR is a product decision. It is filed as a follow-up.
3. **The UI cannot show the hold or link to the review.**
   - **What is missing**: `ui.TaskDetail` (and so `/api/tasks/{id}`) has no `links` field, so the frontend cannot see `security-hold`, `security-review-task` or `trust-boundary-cleared`. A held task looks like any other awaiting-submit task, and the only way to the review is to read the comment and type the task id into the URL.
   - **Not fixed**: this needs an API field and a frontend badge or link, which is larger than this task. Filed as a follow-up.
4. **The security review stays open after a Submit override merges the change.** Task 3 was still `awaiting_reply`, asking its question, after task 1 had merged and closed. It does no harm, since a finding can no longer change anything, but it leaves a stale review parked in the inbox. Noted in the UI follow-up for a decision, not fixed.

## Fixes made in this task

- `fix(githubsim)`: rename reporting, as in finding 1, with the regression test `TestSimListPullRequestFilesListsARenameUnderBothPaths`.
- `test(e2e)`: `tests/e2e/security_hold_walkthrough_test.go`. Its `TestSecurityHoldWalkthrough` and `TestCapabilityEscalatedProposalsWalkthrough` are scenarios 1–13 as regression tests.

## Not run, and why

- **The literal plan text**: it could not be read (see above), so the scenario list in task 1410's description was used.
- **`grain demo` as the orchestrator driver**: the demo has no orchestrator, GitHub or agent, so the orchestrator half ran in the e2e harness and the UI half on a real `ui.NewServer` over that store. The UI harness was not committed, because it is an env-gated serve-and-wait test; it is reproduced in the appendix.
- **The 3000-file listing cap**: not exercised end to end. Generating a PR with 3000 files was out of proportion here, and `pkg/orchestrator`'s unit tests cover the unreadable-listing path that the cap shares.

## Appendix: UI harness

A temporary `tests/e2e/zz_hold_ui_serve_test.go`, removed before committing, that reuses the walkthrough's helpers:

```go
func TestServeHeldChangeForUI(t *testing.T) {
	out := os.Getenv("HOLD_UI_URL_FILE")
	if out == "" { t.Skip("HOLD_UI_URL_FILE unset") }
	w := newHoldWorld(t, false)
	held := w.create("Tighten the git proxy's ref policy", true, nil)
	w.workThenReview(held.ID, "echo '// tweak' >> pkg/gitproxy/proxy.go",
		func(model.Task) []antigravity.Step { return askScript("is this ref meant to be writable?") })
	reviewID := w.heldOn(held, "pkg/gitproxy/proxy.go", true)
	w.notMergedWhileHeld(held, 2)
	base := w.create("Refresh the lint baseline", true, nil)
	w.workThenReview(base.ID, "echo '{\"x\":1}' > .grain/baselines/lint.json", nil)
	w.heldOn(base, ".grain/baselines/lint.json", false)
	w.notMergedWhileHeld(base, 3)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go http.Serve(ln, ui.NewServer(ui.Config{Actor: ui.DefaultActor("operator"),
		Capabilities: ui.OfferedCapabilities()}, w.store))
	os.WriteFile(out, []byte("http://"+ln.Addr().String()+" "+held.ID+" "+reviewID+" "+base.ID+"\n"), 0o644)
	// then: poll until the UI's Submit turns AutoMerge on, run w.cycles(4, merged?),
	// write merged=<bool> state=<state> to out+".result", and wait for out+".done".
}
```

```sh
HOLD_UI_URL_FILE=/tmp/hold-ui go test ./tests/e2e/ -run TestServeHeldChangeForUI -count=1 -timeout 15m &
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node hold-submit.cjs
```

`hold-submit.cjs`, run from `ui/` so it resolves `playwright`:

```js
const { chromium } = require('playwright');
(async () => {
  const [url, held] = require('fs').readFileSync('/tmp/hold-ui','utf8').trim().split(' ');
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
  await ctx.addInitScript(() => window.localStorage.setItem('grain.username', 'operator'));
  const page = await ctx.newPage();
  await page.goto(`${url}/tasks/${held}`);
  await page.waitForLoadState('networkidle');
  const resp = page.waitForResponse(r => r.request().method() === 'POST');
  await page.locator('button', { hasText: /^submit$/i }).first().click();
  console.log((await resp).url(), (await resp).status());   // .../api/tasks/1/submit 200
  await page.waitForTimeout(6000);
  await page.reload();                                       // timeline: "PR merged", "Closed"
  await browser.close();
})();
```

Result: `merged=true state=closed`.
