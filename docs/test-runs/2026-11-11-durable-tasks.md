# Walkthrough: durable tasks — compaction, brief, stale handovers, rollover (2026-11-12)

Plan: [`docs/test-plans/2026-11-11.md`](../test-plans/2026-11-11.md) §1 ("Durable tasks: compaction, the current brief, stale handovers and rollover"), items 1–7 under "What is NOT proved". Task 4514, filed by task 4297.

## Environment

- **Commit:** branch `grain/098073e4/task-4514-walkthrough-durable-tasks` at `e8091b22` (`origin/main` `c5611085` merged in, plus the fixes below). Schema 142 at the time of the walk (the plan was written at 139).
- **Build:** `cd ui && npm ci && npm run build`, `go build -o /home/debian/bin/grain ./cmd/grain` (Go 1.26.10, Node 20.19.0, Playwright 1.62.1 Chromium).
- **Backends:** SQLite 3.40.1 (embedded) and PostgreSQL 15.19 (Debian package, local, `-postgres-url postgres://grain:grain@127.0.0.1:5432/grainwalk`).
- **Daemon:** a real `grain daemon -sandbox-port-base=0 -host-sandboxes` with `-github-host` pointed at the GitHub simulator (`ghsim.go`), a bare `acme/widgets` repository with a `.grain/workflows/ci.yml` that passes, `-agent-framework antigravity -agy-path fake-agy.py`.
- **Stubbed:** only the model provider. `fake-agy.py` stands in for the Antigravity CLI: it reads the real prompt on stdin, records it whole (`runs/prompt-<task>-<n>.txt`), then calls the run's real `grain-sandbox` MCP server (`compact_context`, `resume_later`, `wait_for_task`, `run_command` — so `grain handover write` and `grain pr open` go through the real in-sandbox CLI and git proxy) and emits real `step_update` events. Its `hold` action pauses a run before its tool calls so a person can comment mid-run, and a `no-session` marker makes it name no conversation, so grain hands every run the full prompt instead of resuming the session.
- **UI:** `grain demo -github-sim -fake-ci -reconcile 2s` (its seeded durable task, "Maintain the acme/widgets changelog", task 20) and the PostgreSQL daemon's compacted task.
- **Upgrade:** `61668e58` (schema 131) built from a scratch worktree as `grain-131`.

Harness: [`2026-11-11-durable-tasks/`](2026-11-11-durable-tasks/) — `env.sh` (daemon, ghsim, seeded connections), `common.sh`, `fake-agy.py`, `ghsim.go` (`//go:build ignore`), `item1_compact.sh`, `items2to4.sh`, `item3_runs.sh`, `item5_rollover.sh`, `item6_upgrade.sh`, `ui_probe.mjs`; their outputs are under `out/`.

## Results

| # | Item | SQLite | PostgreSQL |
|---|---|---|---|
| 1 | Agent compaction via `compact_context`, cursor rule, shorter next prompt, deep link | pass | pass |
| 2 | `grain compact` without and with `-handover-file`; same handover again confirms | pass after fixes 1 and 3 | pass after fixes 1 and 3 |
| 3 | Staleness by comments and by runs; badge, prompt note and API agree | pass after fix 1 | pass after fix 1 |
| 4 | Brief edits: 400 on empty reason, 403 without `dispatch:write`, history, original under disclosure, next prompt uses brief | pass after fixes 1 and 2 | pass after fixes 1 and 2 |
| 5 | Rollover by hand and by the reconciler | pass after fix 4 | pass after fix 4 |
| 6 | Upgrade 131 → 142 with a durable task | pass | pass |
| 7 | UI at 1440×900 and 390×844, light and dark | pass | pass |

### 1. Agent compaction (`item1_compact.sh`)

A durable task, 30 comments over three parked runs, then a run that holds, takes a person's comment mid-run, and calls `compact_context`:

- Cursor `compacted_through = 39`, the "picked up now" comment posted before the run started; the mid-run comment is 40 and stays out of the fold (`summarised: 38`, `compacted_by: task 2`, `handover_version: 1`). Same IDs on PostgreSQL.
- The next run's prompt carries `MIDRUN-COMMENT` verbatim, the handover, and the "every message up to comment 39 is summarised by the handover below" note, and none of the 30 round notes: **21,142 → 16,151 bytes** (SQLite), 21,148 → 16,157 (PostgreSQL).
- `/tasks/<id>#comment=<summarised id>` opens the "Summarised by compaction" disclosure, scrolls the comment into view and sets `.comment-highlight` (item 7 probe, all four width/theme pairs on both the demo and the real daemon).

### 2. `grain compact` (`items2to4.sh`)

- `grain compact <id>` sets the standing request (`compaction requested by human alice@example.com …: the next run compacts first`). **Before fix 1** the next run — which resumes its session, as a durable task that parks does on most runs — was not told: its prompt carried the original description and neither the brief nor the request. After the fix the resumed prompt carries both; the run compacts, `handover_version` 1 → 2, and the request clears.
- `grain compact -handover-file f` compacts at once (version 3, cursor at the newest comment). **Before fix 3**, running it again with the same file bumped the version to 4 and posted a second identical handover. After: version stays 3, cursor moves to the newer comment, `handover_confirmed_at` and the age reset, one copy on the task.

### 3. Staleness (`items2to4.sh`, `item3_runs.sh`)

- Comments: 29 comments → `handover_stale: false`; the 30th → `true`; `grain brief` says `-- stale`; the UI shows the `stale` badge (`data-testid="handover-stale"`) exactly when the API says so (true on the demo's seeded task, false on the confirmed PostgreSQL task). The next (resumed) prompt says `[grain] This task's handover is stale: 1 run(s) and 31 comment(s) …` — before fix 1 a resumed prompt had no such note. Writing the same handover again with `grain handover write` confirms it (version unchanged, age back to 0).
- Runs: after 9 runs `stale: false`, after 10 `stale: true`; the 11th run's prompt carries the note. The prompt's count includes the run being prompted (11 vs. the API's 10 before it started), which reads correctly from the run's side.
- Observation (follow-up B): every parked run adds two or three of grain's own notices ("Asked for this task to be picked up now", "this task's run parked it until …"), and `TaskHandoverAge` counts them as comments, so a durable task that parks reaches `HandoverStaleComments` (30) after ~10 runs even if no person or agent said anything.

### 4. Brief edits (`items2to4.sh`)

- `POST /api/tasks/{id}/brief` with `"reason": ""` → **400** `say why the brief changed (reason)`.
- An external-agent token holding only `tasks:read` + `dispatch:read` on `acme/widgets` → **403** `holds no api grant for scope dispatch:write` on `/brief`, `/compact` and `/rollover`; `GET /context` → 200.
- History lists who and why. **Before fix 2** an edit by the access code / local API token was recorded as `by: "human"` (and the CLI said "current brief (by human, …)", "compaction requested by human"); now `human alice@example.com`, as a comment from the same caller is attributed.
- `original_description` keeps the description; the page shows it under "Original description"; the next prompt (fresh and, after fix 1, resumed) carries the brief and not the description.

### 5. Rollover (`item5_rollover.sh`)

A durable task with an open pull request (opened by its run through `grain pr open` against ghsim), a `person-closes` goal, a `durable.walk` notification subscription, a handover written with `grain handover write`, a brief, a dependent task and a task parked on it with `wait_for_task`:

- `grain rollover P` → `task P rolled over to task S`, `now waiting on task S instead: D, W`.
- Predecessor: closed, `rolledOverTo: S`, no pull request, no goal; `task_goal` and `task_notification_subscription` rows 0 for P, 1 for S.
- Successor: durable, `rolledOverFrom: P`, holds `acme/widgets#9000`, the goal; its description is the brief, the handover and "Rolled over from [task P](/tasks/P)". The parked task's `resume_on` changed from `{"tasks":["P"]}` to `{"tasks":["S"]}`; the dependent waits on S. Its first prompt carries the brief and handover and not the original description.
- Refusals: rolling P again → `it was already rolled over, to task S`; a non-durable task → `it is not a durable task`.
- Reconciler: S held mid-run, `created_at` set 400 days back in SQL → after 20 s still `running`, not rolled; once the run ended, the next cycle rolled it over to a new successor.
- **Before fix 4**, `POST /api/tasks/P/reopen` on the rolled-over predecessor returned 200 and put it back in the queue (deferred, would run again on its old branch) beside its successor. Now 400: `task P was rolled over to task S, which carries its work on; reopen or comment on task S instead`. The task page still offers "Reopen" on any closed task (`closed/closed` class "awaiting a person" in `livenessreasons.go`); it calls the same route, so it now gets that refusal (not clicked through in this walk).

### 6. Upgrade 131 → 142 (`item6_upgrade.sh`)

`grain-131` seeds the store and runs a durable task with 25 comments; this checkout's `grain` then starts on the same data directory:

- SQLite in place, SQLite through `grain state prepare-upgrade` (`prepared 131 142 …/schema-upgrade`, i.e. the export/import round trip the Upgrade button uses), and PostgreSQL in place: `migrated the store in place from schema 131 to 142`; the task keeps its admission, state, 26 comments and attempt.
- `GET /api/tasks/{id}/context`: `current_brief: null`, no handover, no cursor, `original_description` the task's description; `grain brief` says `current brief: none (prompts carry the original description)`. Setting a brief afterwards works. The task page answers 200.
- `grain state sync` commits a schema-142 dump; `grain state check` on it says `ok: grain can load this`. (`task_context`/`task_brief_edit` are operational tables and not part of the config dump by design — `pkg/staterepo/tables_internal_test.go`.)

### 7. UI (`ui_probe.mjs`, `make ui-screenshots`-equivalent shots)

The sandbox's file reader shows PNGs as bytes and has no Gemini key, so the shots (`e2e/screenshot.mjs --pages /tasks/20`, attached as `contact-sheet.png` in the closing note) are backed by a probe that measures the page in Chromium at 1440×900 and 390×844, light and dark, on the demo's durable task and on the PostgreSQL daemon's compacted task:

- no horizontal overflow (`scrollWidth - innerWidth = 0`, no element past the right edge), no page errors;
- Edit brief, "Original description" disclosure, Context panel (`context-compaction`), Compact and Write handover… present; the `stale` badge matches `handover_stale`;
- collapsed "Summarised by compaction" disclosures present (4 on the demo, 18 on the real task's first page of comments — the rest are behind §2's "Show earlier");
- the summarised-comment deep link expands, lands in view and highlights.

No `ui/` change was made.

## Findings and fixes

1. **Resumed durable runs lost the brief and all context upkeep** (`pkg/orchestrator/run.go`). `BuildSuspendedPrompt`, which prompts every run that resumes a CLI session, never applied `applyTaskContext`, `summarisedSection` or `contextUpkeepSection`. A durable task that parks resumes on most runs, so in practice the brief, a `grain compact` request, the stale-handover warning and the compaction note never reached it. Fixed: the resumed prompt now carries the brief in place of the description, the upkeep section, and the compaction note when the handover it points at is among the comments being delivered. Regression: `TestSuspendedPromptCarriesTheBriefAndContextUpkeep`. Reproduced on the pre-fix build (`2ab9a53a`, this task's first attempt): after `grain brief` and `grain compact`, the next resumed prompt had `brief=0 original=1 request=0`; `items2to4.sh` prints the same counters.
2. **Brief edits and compaction requests by the access code were attributed to "human"** (`pkg/ui/task_context.go`). Now the deployment's own person, as `requestPersonOrOperator` does for comments. Regression: `TestBriefAndCompactionNameTheOperatorForAnAnonymousCaller`.
3. **Compacting to the same handover was a new version** (`pkg/model/task_context.go`). `Store.CompactTask` always posted a handover comment and bumped `handover_version`, against the design's "writing the same handover again is a confirmation". Now the same body moves the cursor and confirms. Regression: `TestCompactingToTheSameHandoverConfirmsIt`.
4. **A rolled-over task could be reopened** (`pkg/ui/client.go`). Reopen and "Reopen and send" refuse it now, before writing the comment. Regression: `TestRolledOverTaskIsNotReopened`.

## Follow-ups filed

- **A.** The durable-task prompts (`context_compaction.go`, `context_handover.go`, `conversation.go`) and `pkg/mcp/tool_budget.go` tell runs to call `write_handover`, but no live MCP registry offers that tool any more (the fake agent's call answered `unknown tool write_handover`; only `mock_tools.go` defines it). Runs write handovers with `grain handover write`, which does relay and confirm correctly. Changing the wording touches several tests that pin it, so it is left to its own task.
- **B.** `TaskHandoverAge` counts grain's own park and run-now notices as comments, so a parking durable task goes stale by comment count with nobody having said anything.
