# Inbox desktop notifications, ack reopen and task pane changes — 2026-10-07

This run covers [docs/test-plans/2026-10-06.md](../test-plans/2026-10-06.md) §6. It was task 2384, filed by task 2231.

## Environment

- **Tested commit:** `dafd69778` (`origin/candidate`, "Merge pull request #3156 from bwsalmon/grain/task-2466"). The branch `grain/task-2384` was cut from that commit.
- **Commits in scope:** `git branch -r --contains <sha>` lists `origin/candidate` for each of these:

  | Commit | Change |
  |---|---|
  | `017527a85` | Desktop notifications for new inbox arrivals |
  | `660c5577e` | A person's comment on an `awaiting_ack` task requeues it |
  | `c210c4e98` | A running task's repo and base are read-only; PATCH refuses changing them |
  | `aed6fbdc4` | Merge rules in the task pane; `grain demo -merge-rules` |
  | `043d39d65` | Merge-rules warnings on the Repos page |
  | `a216d12da` | Task pane flicker fix |

- **Sandbox:**
  - Debian, Go 1.26.6 and Node 20.19.0.
  - Playwright browsers came from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, which has `chromium-1234` and `chromium_headless_shell-1234`.
- **Server:**
  - The real `grain demo` ran on its embedded SQLite store, with one server per Playwright worker (`ui/e2e/fixtures.js`).
  - The UI was the production bundle: `cd ui && npm ci && npm run build` into `pkg/ui/static`.

## Commands

```sh
cd ui && npm ci && npm run build
export PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright
npx playwright test e2e/inbox-notifications-walkthrough.spec.js --project=chromium
npx playwright test e2e/task-pane-running-ack-walkthrough.spec.js --project=chromium
npx playwright test e2e/merge-rules-walkthrough.spec.js --project=chromium
npx vitest run src/components/BulkEditOverlay.test.jsx
cd .. && go test ./tests/e2e -run TestAPersonsCommentReopensAnAwaitingAckTask -count=1 -v
go test ./pkg/demo/
# probes against a hand-started demo
go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false
curl -X PATCH $U/api/tasks/3 -d '{"base":"release-1"}'
curl -X POST $U/api/teams/default/broadcast -d '{"message":"code freeze from 17:00"}'
```

## Outcomes

### 1. Desktop notifications (`017527a85`)

The new spec is `ui/e2e/inbox-notifications-walkthrough.spec.js`. Its three tests pass.

**Setup**

- **Recording:** an init script wraps `window.Notification` in a subclass. The subclass calls the real constructor and records `{title, body, tag}` into `window.__notes`.
- **Arrivals:** each arrival goes through grain's own routes:
  - `POST /api/tasks` files a proposal.
  - `POST /api/tasks/{id}/messages` parks a question. This is the endpoint a run's chat tool uses (`mcp.NewChatTools`).
  - `POST /api/tasks/{id}/comments` posts the person's reply.
- **Background tab:** a second tab is opened and brought to the front.

**Results**

| Check | Result |
|---|---|
| Page-load baseline | The seeded inbox (a parked question, a proposal, a failure and others) produced **0** notifications over a 7 s wait. |
| Initial switch state | With permission granted and no stored preference, the button already reads "Turn off notifications". It is on by default, so there is no "Turn on" click to make. |
| Suppression while focused | An arrival while the tab was focused and visible: the row appeared and **0** notifications were shown. |
| One per arrival while blurred | One proposal gave exactly one notification: `{title:"14 was proposed and needs approval", body:<task title>, tag:"task:14:proposed"}`. |
| More than three | Four proposals gave one notification: `"4 new items in your inbox"`, tag `grain-inbox-summary`, with the first three titles as its body. |
| A question asked again | A task asked a question and got one `task:15:reply` notification. A reply sent it back to the queue, and nothing was shown. It asked again and got a second `task:15:reply` notification, titled "15 asked a question". |
| Click-through | Calling the recorded notification's `onclick` navigated to `/tasks/15` and opened the pane. On a summary, `onclick` went to `/inbox`. See the note below on focus. |
| Off switch after a reload | Clicking "Turn off notifications" stored `localStorage["grain.inboxNotifications"] = "off"`. After a reload the button reads "Turn on notifications", and a background arrival showed **0** notifications. |
| Denied permission | The header shows the text "Notifications blocked", not a button. Its tooltip says "Allow notifications for this site in the browser's settings to turn them on." No notifications button is rendered, so nothing can loop. |

**Note on click-through focus**

After `onclick`, `document.hasFocus()` was `false`. The script's `window.focus()` was not a user activation, because the test called `onclick` itself and the browser's own notification click was not involved. So the spec records the tab coming forward but does not assert it. A headless browser cannot click a real OS notification.

### 2. Ack reopen (`660c5577e`)

**Go e2e:** the new test is `tests/e2e/ack_reopen_walkthrough_test.go`. It passes in 8 s.

- It uses a real `orchestrator.RunCycle`, a scripted agent whose only step is `comment_on_issue`, githubsim and a bare git repo.
- The task ends in `awaiting_ack` after one run.
- **Agent comment:** an agent-principal comment, then a cycle. The task is still `awaiting_ack` and there is no second run.
- **Team broadcast:** `BroadcastTeamMessage` to the default team reached the task (it is in `TaskIDs`). After a cycle the task is still `awaiting_ack` and there is no second run.
- **Person's comment:** the comment "thanks -- please also do X" puts the task in `queued`. The next cycle ran it a second time, and **the second run's prompt contains the comment**. That prompt also carries the earlier broadcast, which had been delivered but not acted on.
- After the second run answers, the task is back in `awaiting_ack` and a further cycle starts nothing.
- One harness detail: the world's orchestrator also files and runs the director console session as task 1. The recorder therefore counts prompts per `RunConfig.TaskID`.

**In the browser:** `task-pane-running-ack-walkthrough.spec.js` covers demo's seeded `awaiting_ack` task 7.

- `POST /api/teams/default/broadcast` delivered to 8 tasks, including task 7. The broadcast appears in the pane, and the task stays `awaiting_ack`.
- The comment box shows "Sending a comment reopens this task for another run."
- Typing "please also do X" and pressing Comment moves the task to `queued`, and the hint goes away.

### 3. Running task (`c210c4e98`)

Demo's seeded running task is "Bump the Go toolchain to 1.24" (task 3).

- **Pane:** Repos shows plain rows, "Repo acme/widgets" and "Base branch the repo's default". There is no control labelled Repo or Base branch.
  - The "Another repo this task may change" box is still editable. That is intended: its hint says a repo added there counts for a proposal the run files.
- **PATCH changing the base:** `{"base":"release-1"}` returned **400** `{"error":"3 is running, so its base branch cannot be changed until its run ends"}`.
- **PATCH changing the repo:** `{"repo":"acme/gadgets"}` returned **400** "…its repo cannot be changed until its run ends".
- **PATCH restating both:** `{"repo":"acme/widgets","base":""}` returned **200**.
- **Bulk edit of the base across task 3 (running) and task 2 (queued):**
  - Result: the error bar reads "1 of 2 task(s) failed: 3 is running, so its base branch cannot be changed until its run ends".
  - Task 2's base became `release-1` and task 3's did not change.
  - The selection stays ("2 selected"), so the refused row can be retried.
  - Before Apply, the form's notice was wrong. See finding F1.

### 4. Merge-rules warnings on the Repos page (`aed6fbdc4`, `043d39d65`)

- `grain demo -merge-rules` now also seeds a leftover `.grain/sensitive.yml` beside `.grain/merge-rules.yml`. Both are loaded through `repodecl.LoadMergeRules`, as the sync loads them.
- `GET /api/repos/acme/widgets/declarations` returns `mergeRules: ".grain/merge-rules.yml"` and three warnings.
- `/repos/acme/widgets` shows "…, merge rules in .grain/merge-rules.yml" and these three lines:
  - `Warning: .grain/merge-rules.yml and .grain/sensitive.yml both exist; .grain/merge-rules.yml is read and .grain/sensitive.yml is ignored -- delete it`
  - `Warning: .grain/merge-rules.yml: rule docs: ci.grain names "lint", which no job or workflow in .grain/workflows declares`
  - `Warning: .grain/merge-rules.yml: rule ui: ci.grain names "ui e2e", which no job or workflow in .grain/workflows declares`
- The two existing task-pane tests in `merge-rules-walkthrough.spec.js` still pass with the extra file. The task-pane policy, the list row and the timeline comment are unchanged.

### 5. Layout shift while opening and closing tasks (`a216d12da`)

- A `PerformanceObserver({type:"layout-shift", buffered:true})` was installed by an init script.
- The test opened and closed ten tasks: click a `.task-row`, wait for the pane, press Escape, wait for the pane to close.
- Over those ten cycles the observer recorded **0 layout-shift entries, for a cumulative layout shift of 0.0000**. That includes shifts with `hadRecentInput` set.
- The spec asserts a total below 0.1.
- The pre-fix build was not measured for comparison.

## Findings

### F1 (fixed): Bulk edit's notice contradicted the server

**Reproduction:**

1. Start `grain demo`.
2. Tick task 3 ("Bump the Go toolchain to 1.24", running) and task 2 in the list.
3. Click Bulk edit… and tick Base branch.

**What happens:** the info alert read:

> 1 of these is running now. A repo or base change reaches each task's next run -- a retry, or a re-dispatch after a failure -- and never the sandbox already cloned, which keeps the repo it was dispatched against and pushes there.

Since `c210c4e98`, the PATCH for the running task is refused, so nothing reaches its next run. Apply then reports "1 of 2 task(s) failed".

**Fix:** `ui/src/components/BulkEditOverlay.jsx` now says:

> 1 of these is running now. A repo or base change will be refused for it until its run ends, and the rest are changed.

The code comment above it was corrected too.

**Regression tests:**

- `BulkEditOverlay.test.jsx` asserts the new sentence and the absence of the old one.
- `task-pane-running-ack-walkthrough.spec.js` asserts "will be refused" in a real browser, and asserts the refusal Apply reports afterwards.

`origin/main` still has the old text. The port will carry this fix across.

### F2 (environment, not grain): headless Chromium cannot show notifications by default

Playwright's default Chromium is `chromium_headless_shell`. It has two problems for this test:

- It answers `Notification.permission` with `"denied"` even when the context was granted `notifications`. So the inbox can only ever show "Notifications blocked".
- Playwright's CDP focus emulation reports `document.hasFocus() === true` on every tab, including one behind another tab.

**Reproduction:** run `chromium.launch()`, `newContext({permissions:["notifications"]})`, `goto(http://…)`, then evaluate `Notification.permission`. It returns `"denied"`. With `channel:"chromium"` it returns `"granted"`.

**What the spec does:** it uses `test.use({ channel: "chromium" })`, the new headless mode from the same `chromium-1234` install. It also sends `Emulation.setFocusEmulationEnabled({enabled:false})` before backgrounding the tab.

This needs no follow-up. It is recorded so the next person writing a notification spec does not read "Notifications blocked" as a grain bug.

### Observations (not filed)

- A team broadcast also lands on `awaiting_reply` tasks; demo task 4 was among the 8 it reached. `teams.go` documents un-parking those tasks as intended, so this is not a finding.
- The summary notification's body lists titles of the form "N was proposed and needs approval". A question's notification carries the task title as its body, not the question text. Both behave as `inboxNotifications.js` documents.

## Stubs and instrumentation

- `window.Notification` is wrapped to record calls. It still constructs the real notification. This is instrumentation of a browser API.
- In the denied-permission test, `Notification.permission` is overridden to return `"denied"`, because Playwright cannot set a denied permission. This is also a browser API, not grain.
- The test sends `Emulation.setFocusEmulationEnabled(false)` over CDP, so the browser reports its real focus.
- Go e2e: githubsim stands in for GitHub, and the agent is the scripted `antigravity.NewForTest` (`comment_on_issue`). Both are this package's existing harness.
- Nothing of grain's (APIs, store, React) is mocked.

## Changes on this branch

| Change | Files |
|---|---|
| New notifications spec | `ui/e2e/inbox-notifications-walkthrough.spec.js` |
| New task-pane spec: running task, Bulk edit, ack reopen in the browser, layout shift | `ui/e2e/task-pane-running-ack-walkthrough.spec.js` |
| New ack-reopen test through a real dispatch | `tests/e2e/ack_reopen_walkthrough_test.go` |
| Repos-page warnings test | `ui/e2e/merge-rules-walkthrough.spec.js` (extended) |
| Demo seeds the leftover `sensitive.yml` | `pkg/demo/demo_merge_rules.go` |
| Fix F1, with its regression test | `ui/src/components/BulkEditOverlay.jsx`, `ui/src/components/BulkEditOverlay.test.jsx` |

## CI verdict

Grain CI run `fd71f3aff8ffbc4b` on `a271891` (the branch with every change above and this report before this line) passed in full: frontend, go, installer, lint, package cache, postgres, terraform and ui e2e. No known flake recurred.
