# Walkthrough Report: `/next/` Task Operations — Batch Actions, Holds, Defer Until, Fleet Waits, Processes (2026-11-16)

Plan: [`docs/test-plans/2026-11-16.md`](../test-plans/2026-11-16.md) §7. Walked by [Task #6480](/tasks/6480), filed by [Task #6330](/tasks/6330).

## Result

One bug was found and fixed (F1). Everything else in "What is NOT proved" items 1–5 held on a real daemon, over SQLite and PostgreSQL.

| # | Item | SQLite | PostgreSQL | Notes |
|---|---|---|---|---|
| 1a | Run selected now on 20 rows during a deployment pause; Stop mid-batch | pass | pass | All 20 queued rows showed "Paused — waits for Run now". The confirm said "Start 20 tasks now… The deployment is paused: these start, nothing else does." Peak concurrency was **4** in-flight `POST /run-now`. Stop gave "Stopped after 12 of 20; 8 left unchanged". The 8 unsent rows stayed ticked. On the server, exactly 12 ran under the pause (fake agent, `nothing_needed`) and 8 stayed `queued` with no run. |
| 1b | Submit and Retry merge on rows in the merge queue (GitHub sim) | pass | pass | The held `.grain/` change (`awaiting_submit`, PR open, `mergeQueueState: waiting_ci`) offered Submit and Retry merge. Submit asked first ("Each pull request goes to the merge queue; review gates still apply."). Its answer was `200` and the `approval` gate went `needed → passed`. Retry merge on a deferred CI-waiting PR row went straight through, as one row does (`confirmRetryMerge` asks only for several): `200`, "Put #5 back on the merge queue". |
| 1c | Bulk edit across a role that can write only some rows | **fail → fixed (F1)** | pass (after F1) | The writer (`acme/widgets-writer`: `tasks:read/write` on acme/widgets only) never sees no-repo rows, so no mixed selection can be made; the server answers `403` for one anyway. Bulk edit was offered on the writer's own rows, but priority 9 was sent and **both rows were refused** ("cannot set priority above 5 without tasks:admin"). After F1 the form refuses 9 beside the field, and 4 lands on both rows. |
| 1d | Capability attach/detach stores canonical grant strings | pass | pass | Attach sent `{"id":"api:task-admin","attach":true}`. Both tasks then held `https://grain.dev/capabilities/api#task-admin`, and detach (after its confirm) removed it from both. |
| 1e | Reopen on the closed tab (390 px) | pass | pass | Bar: "2 selected Reopen Clear". Both rows went `closed → proposed`. |
| 2 | Holds from a real `.grain/triggers/*.yml` with `human: required` | pass | pass | `walk-declarations.yml` (the shape of this repo's `grain-declarations.yml`) was committed to acme/widgets with declarations on. `/next/task/<id>` draws "Held by this repository's gating triggers · human: required", names the trigger file, and links "Review task #N", which opens `/next/task/N`. After Submit the owed approval reads "approved by alice@example.com". The card stayed inside the viewport at 1440 and 390 px, and the writer sees the same card. |
| 3 | Defer until across the display zone; real wake; picker at 390 px | pass | pass | With `timeZone: America/Los_Angeles`, picking 2026-11-20 showed "Wakes Fri, Nov 20, 2026, 12:00 AM PST" and stored `deferredUntil: 2026-11-20T08:00:00Z`, the start of that day in the zone. Under UTC it stored `T00:00:00Z`. The picker and its button fit 390 px. The page then said "Picks itself back up on Fri, Nov 20, 2026, 12:00 AM PST…". Setting the stored time 3 s ahead woke the task on the live daemon (`-poll-interval 500ms`) in 2.6–3.3 s, back to `proposed`. |
| 4a | A real 1h+ `resume_later(ci: my-branch)` wait on `/next/fleet` | pass | pass | A real run pushed, opened its PR and called `resume_later(ci: my-branch)`. Its run, deferral, suspension and comment were then moved 65 min back. The "Waiting on CI" card listed it ("#37 1h my-branch acme/widgets … github") at 390 and 1440 px. When the sim reported a green run on the branch, the task woke in about 0.3 s, and the card dropped it without a reload. |
| 4b | A real release failure gates publishing | pass | — | `2026-11-15-release-train-publishing-held` harness: three failed `release.yml` runs on main. `/next/fleet` showed "Publishing held · acme/grain@main: …" at 1440 (light/dark) and 390 px, with a link to triage task #2 (`/next/task/2`). The harness then closed the triage task, and the open page switched to "Publishing nothing · …" in under 1 s. `grain health` printed `WARNING: base branches publishing nothing: acme/grain main (held by release triage task 2): …`. |
| 5 | `/next/processes` custom order in two browsers; declared triggers | pass | — | Over acme/grain seeded with this checkout's own `.grain/`. The five trigger rows match `.grain/triggers/*.yml` one for one. Alt+ArrowDown said "Moved to position 2 of 28." and survived a reload. A second browser context (bob) sees the default order: the order lives in that browser's `localStorage` (`grain.list-order.processes`), as on classic (O2). `?sort=name` sorts. The New ▾ menu offers Schedule, Template and Suite. 390 px has no horizontal overflow. |
| 5 | `/next/agents` inspector, keyboard only | pass | — | With a real run going: Tab reaches the card, Enter opens `?task=38`, and Escape closes to the full-width cards with focus back on the card. Tab reaches the bar's Close inspector button, Enter closes, and focus lands on the card. |

## Environment

- **Commit walked:** `ad8ebd4fb` (`main`, "Merge pull request #1747"). The fix was re-walked on the same tree plus F1.
- **Build:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`.
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes -allow-unisolated-host-sandboxes -max-workers 4 -poll-interval 500ms` with the fake agy CLI and the GitHub sim. It ran over SQLite 3.40.1 (`127.0.0.1:4280`/`:4284`) and PostgreSQL 15.20 (`127.0.0.1:4286`/`:4287`).
- **Second daemon:** the publishing-held harness on `127.0.0.1:4290`/`:4294` (SQLite).
- **People:** the IAP stand-in `tests/iap/iapsim.go` (issuer `127.0.0.1:4302`, `-ui-iap-*` flags) turns each browser context into a named person whose role the daemon enforces. Without it, an access-code session on loopback is an operator whatever email it names. The allowlist held:
  - `user:alice@example.com` as `grain/operator`;
  - `user:wendy@example.com` as `acme/widgets-writer` (`{scopes: [tasks:read, tasks:write, ci:read, sandboxes:read], repos: [acme/widgets]}`).
- **Browser:** Chromium 151.0.7922.34 (Playwright), at 1440×900 and 390×844, light and dark.
- **Seed:** `seed.sh` makes 37 tasks:
  - a `.grain/` change held by the trigger, with its security/targeted review;
  - a CI-waiting PR;
  - 20 queued rows under a pause, 4 proposed, 3 no-repo and 2 closed;
  - later, more CI waits and a long run for the Agents walk.

### Helpers (`docs/test-runs/2026-11-16-next-task-operations/`)

| File | What it does |
|---|---|
| `env.sh` | Sources `2026-11-15-resume-later-release/env.sh`, which in turn sources the 2026-11-13 harness. Adds a `human: required` trigger to acme/widgets, the IAP stand-in, `seed_roles` and `park_and_age` (a real CI wait moved past an hour). |
| `up.sh`, `seed.sh` | Start the environment; seed the tasks (ids land in `$WORK_DIR/seeded.env`). |
| `item1-batch.mjs` | Item 1 (`STEPS=1a,…,1e`). |
| `item2-4-holds-defer-fleet.mjs` | Items 2, 3, 4a (`STORE=postgres` makes its store edits through psql). |
| `item4b-publishing.mjs` | Item 4b, over `2026-11-15-release-train-publishing-held/run_walkthrough.sh` with `PAUSE_FOR_SCREENSHOTS=1`. |
| `item5-processes-agents.mjs` | Item 5 (`PART=processes` over the publishing-held daemon, `PART=agents` over this one). |
| `shoot-bulk-edit.mjs`, `contact-sheet.mjs` | F1's before/after screenshots and their contact sheets. |
| `results/{sqlite,postgres}/*.results.txt` | Every PASS/FAIL/INFO line of the final runs. `item2-4-first-run.results.txt` is the first SQLite run, kept for the harness races noted below. |

## Steps

1. `STORE=sqlite ./up.sh /var/tmp/walk-ops`, then:
   - `seed_roles`;
   - `PUT /api/repos/acme/widgets/declarations {"declarations":true}`;
   - `./seed.sh`.

   The PostgreSQL pass is the same with `STORE=postgres DAEMON_PORT=4286 GHSIM_PORT=4287`.
2. `node item1-batch.mjs`. 1a holds each `run-now` answer 400 ms through `page.route`, so a person-speed Stop lands mid-batch. The requests themselves reach the daemon unchanged.
3. `node item2-4-holds-defer-fleet.mjs`, with `STEPS=4a CIWAIT_ID=$(park_and_age …)` for a fresh aged wait.
4. Start `2026-11-15-release-train-publishing-held/run_walkthrough.sh` with `PAUSE_FOR_SCREENSHOTS=1`, then run `NOIAP=1 DAEMON=http://127.0.0.1:4290 WORK_DIR=/var/tmp/walk-rt node item4b-publishing.mjs`. It touches `continue`, so the harness closes the triage task while the page is open.
5. `PART=processes NOIAP=1 … node item5-processes-agents.mjs` (two contexts), then `PART=agents node item5-processes-agents.mjs`.

## F1 — Bulk edit sent a priority the caller's role could not set (fixed)

**Reproduction (deterministic).**

1. Sign in with a role holding `tasks:write` but not `tasks:admin` (wendy above).
2. On `/next/tasks?tab=backlog`, tick two rows, then choose More → Bulk edit…, Task priority `9`, Apply.
3. Apply is enabled with no warning. Both `PATCH /api/tasks/{id}` answer `403`: "wendy@example.com (role acme/widgets-writer) cannot set priority above 5 without tasks:admin". The bar says "2 of 2 tasks failed: …", and nothing changes.

The server rule is `pkg/ui/agent_api_gate.go` (above `model.DefaultTaskPriority` needs `tasks:admin` on the task's repository). The form did not know it.

**Fix.**

- `tasks-page.component.ts` passes `maxPriority: 5` to the Bulk edit dialog when the caller lacks `tasks:admin` on any selected row's repository (`AccessService.canScope`).
- The dialog's hint adds "your role sets at most 5".
- A value above the cap is refused beside the field ("Above 5 needs tasks:admin on every selected task's repository.", `aria-invalid`), and Apply stays off.
- A full role, or one holding `tasks:admin` on every row, is not capped.
- `DEFAULT_TASK_PRIORITY` names the 5 the page already used in `priorityOf`.

**Regression tests.**

- `ui-ng/e2e/next-tasks-bulk-edit-priority-cap.spec.js` (desktop and `@phone`):
  - capped role: 9 refused beside the field, inside the dialog, and axe-clean; 2.5 still reads "Use a whole number"; 3 lands on both rows;
  - admin: 9 lands uncapped.
- `ui-ng/src/app/tasks/batch-edit-dialog.component.spec.ts` (unit).
- Re-walked on the real daemon: 1c passes over SQLite and PostgreSQL.

**Screenshots.** `screenshots/bulk-edit-writer-before.png` and `screenshots/bulk-edit-writer-after.png` are contact sheets: the writer's Bulk edit with 9 typed, at 1440 and 390 px, light and dark. Their DOM state is asserted above: Apply enabled with no error before; disabled, with the error under the field, after.

## Observations (not changed here)

- **O1 — the writer's Fleet.** `GET /api/sandboxes` needs `deployment:read`, so a repo-scoped writer's `/next/fleet` has no Waiting on CI card even for their own repository's waits. That is by scope; a role that should see them includes `grain/viewer`.
- **O2 — processes order is per browser.** The plan asks for "between two browsers". The custom order lives in `localStorage`, the same key classic uses, so it is kept across reloads in one browser and not shared between browsers or people. That is the documented parity, not a regression. A shared order would be a feature (server-side preference).
- **O3 — `?q=` on processes matches names the row does not print.** `?q=release` lists `UI walkthrough`, whose schedule runs suite `release-ui-walkthrough`, and one other row matched the same way. This is by design (`processes-page.component.ts` searches the template and suite names), but the match is invisible on the row.
- **Harness races in the first SQLite run** (`results/sqlite/item2-4-first-run.results.txt`), fixed in the scripts and not product bugs:
  - The Fleet card was read before the first `/api/sandboxes` answer.
  - The display zone was set with a wrong key (`displayTimeZone`; the setting is `timeZone`).
  - The phone check ran after desktop had already cleared the wait.
- **Not walked:** the Inbox's CL Submit and stuck-row controls (PR #1556, PR #1452). A changelist needs a downstream change backend this core harness does not have, so they are left to a downstream walkthrough.
- **Review mandate:** this run could not start the design book's three independent reviews (Design, Functional, Adversarial) itself. The approver should have them done on the pull request before merging.
