# Task intake, Inbox and lifecycle walkthrough — 2026-11-18 (task 7015)

- **Date:** 2026-11-18, 03:59–04:50 UTC, one run.
- **Task:** task 7015, branch
  `grain/53549e42/task-7015-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `cb1067558` ("Merge pull request #1826", the
  2026-11-17 walkthrough's merge). Each fix below was checked again on a
  rebuild of that commit with the fix applied.
- **Focus:** what the 2026-11-17 report listed as not covered or only
  partly covered: the `request_access` card (on the task page and in the
  Inbox), a suspended `resume_later` session, Pause / Pause immediately /
  Resume, dependency add and remove, Retry merge and Re-run CI on a
  failed merge, the New task sheet's fork head labels and proposal path,
  image attachments and Mermaid diagrams in the conversation,
  close/reopen at phone width, and the state ovals on Tasks rows.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`; Node.js
    v20.19.0; Playwright from `ui-ng/package-lock.json` with headless
    Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`); timezone
    UTC; viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend`, `npm ci` in `ui-ng/` (`make frontend`
    still drops `@playwright/test` from `ui-ng/node_modules`, as on
    2026-11-16 and 2026-11-17) and `go build ./cmd/grain`. The fixed
    build re-ran `cd ui-ng && npm run build` and `go build`.
  - Servers:
    - `:8450`, base build: `grain demo -addr 127.0.0.1:8450 -open=false
-github-sim -fake-ci -scheduled-workflows -fork-pull-request
-git-proxy -base-branch-ci -merge-rules -merge-queue-states
-stalled-tasks -sensitive-hold -review-questions -human-review
-secrets-store -notifications -qualification -mechanical-port
-config-change -access-code-file <file>`.
    - `:8451`, fixed build: `grain demo -merge-rules`, for the
      screenshots and layout checks.
    - Each had `TMPDIR` set to a fresh scratch directory, so each store
      was a throwaway embedded SQLite file.
  - There was no `/home/debian/.gemini-api-key`, so no model was called
    and `LOOK=1` was not available.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through `POST /api/auth/access-code` (as
  `ui-shared/e2e/accesscode.js` `signIn` does) and then acts only through
  visible controls. Each page logged uncaught page errors, console errors
  and warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), native dialogs, and every non-GET `/api/` request with its
  body. The REST API (bearer access code) read back what each gesture
  persisted.
- **Console baseline:** no uncaught page error on any `/next/` page and no
  failed request. The only console error was the `400` from step 4.4,
  where the server refused a base branch on purpose.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. Two seeds came from the demo's own loopback-only e2e hooks
rather than from a model run, because no model was available:

1. **Pending access requests:** `POST
/api/e2e/tasks/{id}/pending-access` (`pkg/demo/demo_access_request.go`)
   parks a task on a real `model.PendingAccessRequest`. It checks the
   items the way a run's `request_access` call is checked. It was used for
   #22, #20 and #41 on `:8450`, and #26 on `:8451`.
2. **Tasks created for a step:** one `POST /api/tasks` (#41 on `:8450`)
   made a task to park, and it was closed afterwards. Everything else was
   filed through the New task sheet.

## Which UI each area ran against

| Area                                | UI                                              |
| ----------------------------------- | ----------------------------------------------- |
| Inbox, access card                  | `/next/inbox`                                   |
| Task detail, access card, lifecycle | `/next/task/<id>`                               |
| Tasks rows and tabs                 | `/next/tasks`                                   |
| New task sheet                      | `/next/` sheet (rail **New**)                   |
| Failed-merge row (comparison only)  | classic `/tasks?filter=all`, read and not acted |
| System → Pipeline manual mode       | no `/next/` page; not walked                    |
| `/new?field.<name>=` links          | no `/next/` route (`**` → Inbox); not walked    |
| Boards                              | classic only; not walked                        |

## Steps and outcomes

### 1. Inbox (`/next/inbox`, base build)

| #   | Step                                                  | Outcome                                                                                                                                                                                                                                                                                                                                                   |
| --- | ----------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1.1 | Open `/next/tasks`, then the rail's **Inbox**.        | Lands on `/next/inbox`. Headline "28 decisions waiting on you"; rail "Inbox (28 waiting)". Groups: release 1, reply 4, review decision 1, submit 10, unblock a merge 1, approve on GitHub 1, stuck 5, proposals 3, alerts 3. The **All** chip reads 29 (O1). No console error.                                                                            |
| 1.2 | Park #22 on a three-item access request, then reload. | A new group "Asked: grant access (1)"; #22 leaves "Look at a stuck task" (5 → 4). The rail reads 29.                                                                                                                                                                                                                                                      |
| 1.3 | Select #22's row.                                     | The card lists `read acme/billing` (unticked, disabled, with the target-list refusal), `network access: open` (ticked) and `api#ci-read={"repo":"acme/widgets"}`, which #22 holds already, marked **held**. **Bug 1:** the held item was ticked and enabled, so it was counted towards the button's "Grant all" / "Grant M of N" and sent with the grant. |

### 2. Task detail (`/next/task/<id>`, base build)

| #    | Step                                                                                      | Outcome                                                                                                                                                                                                                                                                                                                                                                                    |
| ---- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 2.1  | `/next/task/22` (the same request).                                                       | "Waiting for an access grant: …", the reason, the three items with the refusal and "already held", **Re-check items**, **Grant all**, **Deny…**. Only `network: open` is ticked. **Bug 1:** the button reads "Grant all" though it grants one of the three items; unticking that item makes it "Grant selected (0)".                                                                       |
| 2.2  | **Deny…** with an empty reason.                                                           | The reason box appears; **Deny request** stays disabled until it has text. **Cancel** restores the card. Nothing was sent.                                                                                                                                                                                                                                                                 |
| 2.3  | #3 (running) → **Pause**.                                                                 | `POST /api/tasks/3/pause {"immediate":false}`, with no confirm (pause after this turn).                                                                                                                                                                                                                                                                                                    |
| 2.4  | #8 (running) → **More ▾** → **Pause immediately**.                                        | More holds Pause immediately, Compact context… and Recreate sandbox. The confirm reads "Pause #8 now? Its run is stopped at once, without the grace period to push its work and write its handover; whatever it has not pushed is lost. …". **Cancel** sent nothing; #8 stays `running`.                                                                                                   |
| 2.5  | #25 (queued) → **Pause**, reload → **Resume**.                                            | `pause` → `queued/operator_paused`, control Resume. After the reload the header offers **Resume**. Resume → `queued/ready`, still `queued`, in the same place in the backlog.                                                                                                                                                                                                              |
| 2.6  | #25 → Details → type `2` → **Add**, then **Remove dependency #2**.                        | `POST …/depends-on {"id":"2","attach":true}` → `dependsOn ["2"]`, `queued/blocked_on_task`. The chip's hover reads "#2 Fix flaky retry test — Waiting". Remove → `attach:false`; `dependsOn` gone, `queued/ready`.                                                                                                                                                                         |
| 2.7  | #23 (the `resume_later` CI wait) → Runs.                                                  | Run 1 "parked", with its detail "parked: resume_later until CI (my-branch) has a verdict: pushed the Go 1.27 bump; waiting for CI on it". The park reason is kept. The header reads "Deferred" with **Bring back** (O2).                                                                                                                                                                   |
| 2.8  | #29 (merge failed) → header.                                                              | **Bug 2:** the oval reads "Merging", in the neutral tone. Classic's row for the same task reads "Merge failed … merge blocked merge failed". The header has no **Retry merge** (O3).                                                                                                                                                                                                       |
| 2.9  | #29 → Changes and CI → **Retry merge**.                                                   | `POST …/29/retry-merge`, with no confirm. The page follows without a reload: `completed/on_merge_queue`, back to "Merging".                                                                                                                                                                                                                                                                |
| 2.10 | #29 → **Re-run CI**.                                                                      | `POST /api/ci/runs {"taskId":"29"}`, with no confirm.                                                                                                                                                                                                                                                                                                                                      |
| 2.11 | #2 → composer: two PNGs, one valid and one invalid ` ```mermaid ` fence → **Send to #2**. | One `POST …/comments` carrying both attachments. Two thumbnails; each is a link to `/api/tasks/2/attachments/<n>` with `target="_blank"`, so clicking opens a new tab (no viewer; F1). The valid fence draws an SVG with a **Diagram / Source** toggle. The invalid one reads "Diagram could not be rendered (Parse error on line 2:). Showing the Mermaid source." Both survive a reload. |
| 2.12 | 390×844: #35 → **Close**, then **Reopen**.                                                | Close acts at once (by design: 2026-11-16 O3, 2026-11-17 step 2.8). The transition is recorded as by "alice@example.com (access code)". Reopen → `queued`, with a `reopened` transition by the same principal. No sideways overflow.                                                                                                                                                       |

### 3. Tasks list (`/next/tasks`, base build)

| #   | Step                                                     | Outcome                                                                                                                                                                                                                                                             |
| --- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 3.1 | Tabs.                                                    | Backlog 6, Needs approval 4, Running 3, Asked 6, Submit & land 15, Deferred 3, Closed, All 37.                                                                                                                                                                      |
| 3.2 | Rows on Needs approval, Submit & land, Backlog, Running. | #11 reads "Waits on a dependency". Merging changes (#5) read "Merging"; CL #14 reads "Ready to submit · No repository". **Bug 2:** #29, whose merge failed, also reads "Merging". #12, imported from `acme/widgets#42`, still has no `imported` chip (a known gap). |
| 3.3 | Search `retry` → reload → Back.                          | `?q=retry` is written to the URL with `replaceUrl`, and a reload keeps it. Back leaves the app, because the search adds no history entry.                                                                                                                           |

### 4. New task sheet (`/next/`, base build)

| #   | Step                                                                       | Outcome                                                                                                                                                                                                                                                 |
| --- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 4.1 | Rail **New** → untick nothing (Create a new branch starts unticked).       | The sheet opens with an empty title.                                                                                                                                                                                                                    |
| 4.2 | Base `octo:feature/x`.                                                     | Help line: "GitHub fork head: branch feature/x on octo's fork. grain files this task against the open pull request on acme/widgets it heads." **Create** is enabled.                                                                                    |
| 4.3 | Base `octo:`, `:x`, `o/c:x`.                                               | Each is refused in the server's words ("… needs both an owner and a branch, as <owner>:<branch>"; "… "o/c" is not a GitHub login"), and **Create** is disabled.                                                                                         |
| 4.4 | Base `octo cat:x` → **Create and start**.                                  | Nothing is said before sending: the value is not shaped like a fork head, so it is treated as a plain branch name. The server refuses it with `400` ("base "octo cat:x" is not a usable branch name: …"), and the sheet shows that and stays open (O4). |
| 4.5 | Tick **Create a new branch**, add reviewer `bob@example.com`, Approve off. | The button reads **Save as proposal**. Pressing it with no branch name refuses locally: "Name the new branch, or untick the new-branch box." The field is marked `aria-invalid`.                                                                        |
| 4.6 | Fresh sheet → title → Approve off → **Save as proposal**.                  | `POST /api/tasks` with `approved:false` → #42 `proposed`. The toast reads "Saved #42 as a proposal in the Inbox", with Open and Discard.                                                                                                                |

## Bugs found and fixed

### Bug 1 — the access card's Grant count was wrong on both surfaces

**Reproduction:** park an `acme/widgets` task on
`[{kind: "read", repo: "acme/billing"}, {kind: "network", access: "open"},
{kind: "grant", capability: "https://grain.dev/capabilities/grain#ci"}]`
through `/api/e2e/tasks/{id}/pending-access`. The first item is refused,
because it is off the target list, and the third is held already.

- **Task page:** the button reads **Grant all**, though Grant sends only
  item 1. Unticking that item gives "Grant selected (0)".
- **Inbox:** the held item is ticked and enabled, and is counted and sent.
  An item refused with a substitute grant could not be ticked at all
  (the Inbox disabled any item with an `error`, unlike classic and the
  task page).

**Fix:** a new `ui-ng/src/app/shared/access-items.ts` is shared by both
cards:

- `accessItemGrantable`: an item is grantable if it is not held and is
  either clean or carries a substitute.
- `chosenAccessItems`.
- `grantAccessLabel`: "Grant all" only when every item asked for is
  going, otherwise "Grant M of N", as classic's `PendingAccess.jsx`
  words it.

**Coverage:** `ui-ng/e2e/next-access-grant-label.spec.js` drives both
cards. It checks "Grant 1 of 3", the tick state of each box, "Grant 0 of
3" disabled once unticked, and that Grant sends `{items: [1]}`, leaves
the Inbox and sets `networkAccess: open`. It fails on the base code.

### Bug 2 — a failed merge read "Merging"

**Reproduction:** `grain demo -merge-rules` seeds "Raise the API pod
memory limit": `completed`, merge-queue state `merge_failed`, reason
`completed/merge_failed` with control Retry merge. On `/next/tasks` →
Submit & land, on its `/next/task/<id>` header and on its workstream
node, its oval read "Merging" in the neutral tone. Classic's row reads
"Merge failed", and `ui/src/state.js` says exactly this state must not
be "dressed up as progress".

**Fix:** `taskStateLabel` and `mergeStuck` in
`ui-ng/src/app/tasks/task-view.ts`. A completed task in `merge_failed`
or `unreadable` gets classic's words ("Merge failed", "Can't read the
pull request") in the error tone on Tasks rows (list, backlog and
timeline templates), the task page header and workstream nodes. Every
other task keeps `stateLabel`. A closed task with a stale observation
is not relabelled.

**Coverage:** a `taskStateLabel` unit test in `task-view.spec.ts`, and
`ui-ng/e2e/next-merge-failed-state.spec.js` (row and header text and
tone; a change still landing keeps "Merging"). The e2e spec fails on
the base code.

## Tests run

- `npx vitest run src/app/tasks src/app/task src/app/workstreams
src/app/inbox` in `ui-ng/`: 16 files, 225 tests pass. `npx tsc -p
tsconfig.app.json --noEmit` is clean, and Prettier is clean on every
  touched file.
- Each new e2e spec was run on the base frontend (fails, as quoted above)
  and on the fixed one (passes).
- `npx playwright test` over `next-full-system`, every `next-inbox*`,
  `next-tasks*` and `next-task-*` spec, `next-changelist-diff-preview`,
  `next-intake-*`, `next-suites-and-task-actions` and the two new specs
  (desktop, phone and dark projects): 82 passed.
- The whole `ui-ng` e2e suite was started on the fixed build; its
  result is in the closing note.

## Rendering

On the fixed build (`:8451`), `ui/e2e/screenshot.mjs` (with `URL`/`CODE`
pointed at that server) shot `/next/task/25` (the failed merge),
`/next/tasks?tab=merging` and `/next/task/26` (the mixed access request).
Each was shot at desktop and phone width, in light and dark. The file
reader here cannot show PNGs and there was no Gemini key, so the layout
was checked by bounding boxes:

- **Header oval:** "Merge failed", 90×24 at the header's left. Light mode
  is `rgb(65,14,11)` on `rgb(249,222,220)`; dark is `rgb(249,222,220)`
  on `rgb(140,29,24)`.
- **Row oval:** 106×24, with no internal overflow (`scrollWidth −
clientWidth` 0) at 1400 and 390.
- **Grant button:** "Grant 1 of 3", 100×40.
- `scrollWidth − clientWidth` was 0 on every page at both widths.

None of the shots were committed.

## Observations (not treated as defects)

- **O1:** the Inbox's **All** chip counts 29 while the headline and rail
  say 28. The difference is the seeded alert #3, which is already
  acknowledged. The store leaves acknowledged alerts out of
  `decisions()` on purpose (as classic's bell does), but the chip counts
  every row shown.
- **O2:** #23, parked on `resume_later` until CI, reads "Deferred" with
  **Bring back**, and its reason is `deferred/no_wake_time` ("awaiting a
  person", control Undefer). The CI wait shows only on its Runs tab and
  in `/api/sandboxes`' `ciWaits`.
- **O3:** a merge-failed task's header offers Pause and Run now but not
  **Retry merge**, which its reason names as its control. Retry merge is
  only on the Changes and CI tab. A follow-up is proposed.
- **O4:** a base with a ':' that is not shaped like a fork head (such as
  `octo cat:x`) is not refused before sending. The server refuses it, and
  the sheet shows its words. A ':' can never be part of a branch name, so
  the sheet could refuse it locally, as it does `o/c:x`.
- **O5:** Mermaid diagrams in the conversation have Diagram / Source but
  no zoom or fullscreen control. Image thumbnails open in a new tab; there
  is still no full-screen viewer (F1 of earlier reports).

## Gaps on `/next/` (no page or control yet)

Rechecked against `ui-ng/src/app` at `cb1067558`:

- **Inbox:** sections cannot be collapsed (the only `aria-expanded` is
  the split pane's), and Alerts starts open.
- **Tasks:** there are no State/Admission/Human review/External ref/Origin
  filters, no `owner:any:me`, no server-side closed search, no
  `imported`, external-PR or "held for approval by" chip, and no Blocked
  tab or rail count.
- **Task detail:** there is no image viewer, and no Mermaid zoom or
  fullscreen. Edit's priority is a fixed-level chip, with no free Task
  priority, and Edit has no Owner, External ref, Reviewers or Keep
  running.
- **New task:** no Task priority, Owner, Keep running, Admission or
  Hosting control.
- **Classic-only pages:** System → Pipeline, `/new?field.<name>=` links
  and the `settings/new-task-form.yml` notice, and Boards.

## Not covered

- Host-script approvals, the pending-secret box, cross-team approvals,
  the capability audit's contents, the live transcript, the goal card's
  `on_limit` behaviour, and the config-change card's Approve & Apply.
  Either no seed reaches them without a model run, or there was no time
  in this run.
- Force merge: the CI-wait task (#23) has no pull request, so the Changes
  tab offers no Force merge. No seeded task combines a stuck CI wait with
  a pull request.
- Batch bar, template draft priority and the read-only role: walked on
  2026-11-17, and their specs still pass in the run above.
