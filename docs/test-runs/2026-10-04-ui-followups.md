# 2026-10-04: UI follow-ups from the 10-03 walkthroughs (test plan §9)

Task: grain/task-1835. Plan: `docs/test-plans/2026-10-04.md` §9, "UI behaviour from yesterday's
walkthroughs", and its "Standing environment notes".

## Environment

- Base `candidate` at `ed41e93922bce508fc50a596bcc09cac2f4e7549` (Merge PR #2884, grain/task-1834).
- All fifteen commits under test are on it. `git branch -r --contains <sha>` lists `origin/candidate` (and
  `origin/main`) for each of them: `6da925425`, `3cf19b567`, `7b2868f57`, `77b2fbed5`, `3fc94de56`,
  `8af545853`, `bc44d1b54`, `7f525eb04`, `6733e68a8`, `a2d46bb21`, `bd7db0bf1`, `7a531b8fb`, `97f315b6a`,
  `e758b1046` and `d28f3fe21`.
- Sandbox: Debian, Go 1.26.6, Node v20.19.0, Playwright 1.62.1 with
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
  - Chromium 151.0.7922.34 (headless shell 1234) ran every step.
  - WebKit 26.5 is installed. It was not used, because `playwright.config.js` limits WebKit to the
    phone, keyboard and banner specs.
- Viewports: 1400×900 and 390×844 (phone).
- Browser time zone UTC. The deployment's time zone was set to `America/Los_Angeles` for the executor step.
- Server: the real `grain demo -addr 127.0.0.1:0 -open=false -default-target-repo acme/widgets` on its
  embedded SQLite store, serving the production bundle from `cd ui && npm ci && npm run build`. The e2e
  fixture (`ui/e2e/fixtures.js`) starts one for the spec. The exploratory passes also used a hand-started
  one at `127.0.0.1:18420`.

## Stubs

- **None of grain's own APIs or React.** Every request reached the demo server.
- **GitHub:** not stubbed, and not reachable either. `grain demo` wires no GitHub client, so
  `GET /api/repos/acme/widgets/github-branches` answers 404 and every base-branch check fails open. A
  githubsim-backed demo does not exist yet (see F2). The server-side refusal is covered by
  `pkg/ui/base_existence_test.go`, which uses a fake branch lister.
- **Live model sessions:** none were started. `grain demo` has no orchestrator. The run that ends on
  `nothing_needed` is driven in Go (`pkg/orchestrator/nothing_needed_filed_test.go`), using the
  orchestrator's own test store and its githubsim client (`newSim`).
- **`page.route` on `/api/tasks/{id}`:** holds the request for 2 s, then calls `route.continue()` to the
  real server. This slows the response but does not mock it.

## Steps and outcomes

Everything below is in `ui/e2e/ui-followups-walkthrough.spec.js` (7 tests, all passing:
`npx playwright test e2e/ui-followups-walkthrough.spec.js`, about 35 s) unless it says otherwise.

| # | Step | Outcome |
|---|------|---------|
| 1 | Create a template in the UI with **When the agent finds nothing to do → Leave for acknowledgement**. Create a schedule over it in the UI, then **Run now**. | Pass. The template and schedule both read `nothingNeededAction: "ack"`. Run now files one task, and `GET /api/tasks/{id}` gives `"ack"`. The task pane shows the "When the agent finds nothing to do" setting. |
| 1b | Same with **Close**. | Pass. Template, schedule and filed task all read `"close"`. |
| 1c | A suite over each template, run with `POST /api/suite-runs` (`repo`, `base: main`). | Pass. The suite's task carries the template's value. A suite run with no `base` is refused: "base is required: a suite run has no default branch to fall back to". |
| 1d | End the filed task's run with `nothing_needed`. | Pass, in Go: `TestATemplatesNothingNeededActionDecidesTheEndingOfTheTaskItsScheduleFiles` and `…ItsSuiteFiles`. A template with `ack` → schedule fire / suite compose → `ProcessResult` gives `awaiting_ack`. With `close`, the task is `closed`. |
| 2 | Base branch `no-such-branch` in the New task, template and schedule forms. | Demo cannot answer this (F2). `github-branches` is 404, no warning is shown, and the task is created (201). The template and schedule forms also save it without a warning. That is the documented fail-open, not a fault. |
| 3 | Edit an inline schedule's title, then press Escape, "← Schedules" and browser Back. | Pass. Each one asks "You have unsaved changes here. Leave and discard them?". Dismissing keeps the edit in the field. |
| 3b | Save, then browser Back twice. | Pass. No prompt. |
| 3c | Open the declared schedule, template and suite (`repo:*:acme:widgets:*`) and press Escape. Also open them and use browser Back. | Pass. They close, and no prompt is asked anywhere. |
| 4 | As `dave` (member of `alpha` and `beta`), follow `/teams/alpha/{schedules,templates,suites}/<beta's id>`. | Pass at 1400 px and at 390 px. Each editor opens with Target team "Beta", and the URL is kept. |
| 4b | `/?team=nope` and `/teams/nope/tasks`. | Pass at both widths. An alert says `no team "nope"`, and the page falls back to "All tasks". |
| 4c | `/teams/gamma/settings`, where gamma is a team dave is not in. | Pass at both widths. "You are not a member of team gamma, so its settings are not yours to see…", with no spinner. |
| 5 | Make a task PATCH fail by closing the task from another context first, as the plan suggests. | The PATCH is not refused. `POST /api/tasks/2/close`, then `PATCH /api/tasks/2 {"title":…}`, answers 200, and so does a comment on a closed task. A real refusal came instead from demoting dave to viewer while the editor was open, which gives 403 "you can read team "default"'s tasks but not change them…". |
| 5b | With that refusal: the task pane's title edit, the task pane's reply, and the Inbox reply. | **Failed before the fix (F1).** The task-pane edit and reply were lost. The Inbox reply was kept. Pass after the fix. |
| 6 | Delay `/api/tasks/{id}` by 2 s. Click task 1, wait, Escape, then click task 2 and sample the pane every 100 ms. | Pass. The pane is named "Loading task 2" until task 2 lands, then "Task 2: Fix flaky retry test". Task 1's body never appeared. While a task is loading, its overlay covers the list, so a second click cannot land on another row. |
| 7 | `PUT /api/settings {"timeZone":"America/Los_Angeles"}`, issue an executor token, then open Admin → Executors. | Pass. "Created: 10/4/2026, 1:33:49 PM" is the Los Angeles wall clock for a 20:33 UTC token, in a UTC browser. |
| 8 | `POST /api/teams` with `timeZone: "Mars/Base"`. | Pass. 400 `{"error":"invalid timeZone \"Mars/Base\""}`. |

## Findings

### F1: a refused task edit or task-pane reply was lost once the refresh named the team read-only (fixed)

Reproduction (deterministic, `ui-followups-walkthrough.spec.js` "a task edit and a reply refused with a
403 stay where they were typed"):

1. Sign in as `dave`, a member of `default`. Open `/tasks/1`, click **Edit**, and type a new title.
2. In another context, run `PUT /api/teams/default {"addUsers":["dave:viewer"]}`.
3. Click **Save**. The PATCH answers 403 and the error is shown.
4. Wait for the next `/api/config` poll, which now lists `default` in `readOnlyTeams`. The edit form
   unmounts and the typed title is gone.
5. The task pane's reply box does the same. Type in it, demote, click **Comment** (403), and wait for the
   poll. The box is replaced by the refusal sentence, and `Timeline`'s put-back writes into a ref that no
   longer exists.

Cause: 7a531b8fb keeps a draft when `act()` resolves false, but `DetailOverlay` rendered the editor only
while `editing && !refusal`, and `Timeline` swapped the composer for the refusal. The refusal that arrives
after a 403 removed the very form that was keeping the draft. The Inbox reply was not affected.

Fix: `ui/src/components/DetailOverlay.jsx` keeps an open editor until Cancel. The Edit button is still
offered only with no refusal. `ui/src/components/detail/Timeline.jsx` keeps the reply box, with the
refusal under it, while it holds a draft that has not been confirmed as landed.

Regressions:
- Vitest: `DetailOverlay.readOnly.test.jsx` "keeps the open editor and what was typed in it".
- Vitest: `Timeline.test.jsx` "keeps a refused comment when the refusal that follows names the team
  read-only".
- e2e: the spec above.

All three fail against the old code. The e2e waits for the read-only `/api/config` answer, so it does not
depend on timing.

### F2: base-branch warnings and the one-shot refusal cannot be walked in `grain demo` (filed)

Reproduction: start `grain demo` and type `no-such-branch` as the base in the New task form.
`GET /api/repos/acme/widgets/github-branches` is 404, no warning is shown, and `POST /api/tasks` answers
201. The schedule and template forms likewise show nothing. This is the intended fail-open when GitHub
cannot be read. It means, though, that the warning and the refusal from 77b2fbed5/3fc94de56 can only be
seen in a browser by stubbing grain's own `github-branches` endpoint, which `base-branch-existence.spec.js`
does. That is not allowed here. The fix needs `grain demo` to wire a githubsim-backed GitHub client, which
is larger than this task, so it is filed as a follow-up.

### Observation: closing a task does not make a later edit fail

The plan's step 6 assumes it does. `PATCH /api/tasks/{id}` and `POST /api/tasks/{id}/comments` on a
closed task both answer 200. That looks intended (a closed task's title can still be corrected), so it is
not filed. A role change was used for the refusal instead.

## Fixes in this change

- `fix(ui): keep a task edit or reply when its save is refused with a 403` (F1).
- `test: walk the 10-04 §9 UI follow-ups end to end`:
  - `ui/e2e/ui-followups-walkthrough.spec.js`;
  - `pkg/orchestrator/nothing_needed_filed_test.go`.
- `pkg/orchestrator` is a trust-boundary path, so this change can be held for security review even though
  the file there is test-only.

## Formatting

`npm run format` in `ui/` and `gofmt -l` (clean).

## CI verdict

Full suite: grain CI run `58fe528605704fc0` on `d77018272`, the commit that adds this report, passed.

| Job | Result | Time |
|-----|--------|------|
| frontend | success | 5m38s |
| go | success | 16m2s |
| installer | success | 4m2s |
| lint | success | 24s |
| package cache | success | 2m5s |
| postgres | success | 1m39s |
| terraform | success | 40s |
| ui e2e | success | 11m49s |

Neither known flake (`RepoPage.test.jsx` "shows the inherited global value…",
`team_declarations_ci_walkthrough.spec.js:229`) recurred, so no `retrigger_ci` was needed. The commit that
writes this table is checked again before merging.
