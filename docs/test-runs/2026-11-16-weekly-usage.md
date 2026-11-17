# Walkthrough: Weekly Usage Horizon and Paused-Model Reset Times (2026-11-16 §5)

- **Date:** 2026-11-17, 00:45–02:15 UTC (baseline run on unmodified `HEAD`, fixes, and full re-run on both stores).
- **Plan:** `docs/test-plans/2026-11-16.md` §5 (filed by [Task #6330](/tasks/6330)), executed by [Task #6478](/tasks/6478).
- **Branch:** `grain/53549e42/task-6478-walkthrough-weekly-usage`.
- **Base commit tested:** `a771a9973` (unmodified `HEAD` built into `/home/debian/bin/grain-base` before any edits). All fixes were rebuilt into `/home/debian/bin/grain` (`make frontend && go build`) and re-walked end-to-end on both SQLite and PostgreSQL.
- **Outcome:** All four "What is NOT proved" items in §5 were executed end-to-end against real `grain daemon -sandbox-port-base=0` instances on both SQLite and PostgreSQL (`GRAIN_REQUIRE_POSTGRES=1`) plus Playwright Chromium at 1440×900 and 390×844 in light and dark themes. Six real bugs were reproduced live on `a771a9973` (F1–F6), fixed, covered with Go, Vitest, and `/next/` Playwright regression tests, and verified fixed in a full live re-walk on both SQLite and PostgreSQL.

## Environment

| Piece | What ran |
| --- | --- |
| Sandbox | Debian 12 (`linux/amd64`), Go `go1.26.10`, Node `v20.19.0`, `sqlite3` `3.40.1`, `socat`. |
| Stores | **SQLite** (default embedded store) and **PostgreSQL 15.20** (`127.0.0.1:5432`, `GRAIN_REQUIRE_POSTGRES=1`, fresh database per run). |
| Daemon | Real `grain daemon -sandbox-port-base=0 -listen 127.0.0.1:<port>` with local-process sandbox backend (`-sandbox local`), real `grain usage`, `grain usage --json`, `grain state sync`, and `grain state check`. |
| Frontend | `make frontend` (Angular `/next/` in `pkg/ui/static-next` and classic `/` in `pkg/ui/static`), embedded in the tested `grain` binary. |
| Browser | Playwright `1.62.1` / Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at `1440×900` (desktop) and `390×844` (phone), light & dark themes, in `America/Los_Angeles` and `Asia/Kolkata` contexts. |
| Visual review | No `GEMINI_API_KEY` was available in the sandbox (`LOOK=1` unavailable); layout, horizontal overflow (`scrollWidth - clientWidth === 0`), timezone text, and axe-core accessibility were verified programmatically, and both before and after `contact-sheet.png` composites are attached to [Task #6478](/tasks/6478). |

## Walkthrough Harness and Stubs

The reproducible harness lives under `docs/test-runs/2026-11-16-weekly-usage/`:

- `fake-claude.py` & `fake-agy.py`: Extended from `docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py`. Both speak real `grain-sandbox` MCP over the daemon's sandbox proxy, report realistic token usage, and emit configurable weekly (`You've hit your weekly limit · resets <Mon DD>, <h:mm><am|pm> (UTC)`) or 5-hour interval (`You've hit your 5-hour limit · resets <h:mm><am|pm> (UTC)`) refusals on demand.
- `env.sh`: Starts and stops a real `grain daemon -sandbox-port-base=0` backed by either SQLite or PostgreSQL, configures two connections (`claude-primary` → `claude-opus-4-6`, `gemini-fallback` → `gemini-3.1-pro`), sets default fallback order `claude-opus-4-6,gemini-3.1-pro`, and seeds a local bare git repository for `acme/widgets`.
- `run_walkthrough.sh`: Drives Items 1–4 end-to-end on either `STORE=sqlite` or `STORE=postgres`, capturing `GET /api/usage`, `GET /api/config`, `grain usage`, `grain usage --json`, `usage_capacity` / `usage_limit_pause` / `task` SQL rows, interactive fallback notices, and `grain state sync` / `grain state check` outputs at every step.
- `pw_walkthrough.mjs`: Drives Playwright contexts with `timezoneId: "America/Los_Angeles"` and `"Asia/Kolkata"` to file tasks from `/next/tasks` (recording outgoing `X-Grain-Time-Zone`), inspect `/next/telemetry`, `/next/settings/models`, and classic `/settings` UsageCard across 1440×900 and 390×844 light/dark, verify DST transitions in Chromium's `Intl`, and compose `contact-sheet.png`.

Only the model CLIs (`claude` and `agy`) and the bare local git remote were faked; the daemon, orchestrator, usage governor, persistence layer, HTTP API, CLI, state-repo sync, and both UIs ran live.

## Steps and Outcomes

### Item 1 — Live Weekly Pause and Fallback-Model Dispatch

1. **Seed and age runs >5h (`step1a`):** Ran 2 standard tasks and 4 interactive chats on `claude-opus-4-6` (`1.93M` total billable tokens), then aged `task_run_usage.finished_at` back by 6 hours so the 5-hour interval read `0` while the 7-day window held `1.93M`.
2. **Trigger live weekly refusal (`step1b`):** Configured `fake-claude.py` to emit `You've hit your weekly limit · resets Nov 20, 1:50am (UTC)` (+72h). Filed a default-model task and a `claude-opus-4-6`-pinned task.
   - **Baseline `a771a9973` (SQLite & PostgreSQL):** ❌ **F2** — `pkg/orchestrator/pause.go` clamped the 72h weekly reset to `maxUsageLimitPause = 24h` (`2026-11-18T01:22:37Z` instead of `2026-11-20T01:22:00Z`), corrupting `usage_limit_pause.until_at`, `usage_capacity.reset_at`, `GET /api/usage`, `grain usage`, and UI reset pills (`resets Nov 17, 5:22 PM PST (in 23h 59m)` right beside the verbatim provider text `resets Nov 20, 1:22am (UTC)`).
   - **After fix (SQLite & PostgreSQL):** ✅ Both stores recorded `until_at = 2026-11-20 01:50:00Z` (+72h), `usage_capacity` row `claude-primary | #weekly | 1930000 | 604800000 | reset_at=2026-11-20 01:50:00Z`, `pauseHorizon: "weekly"`, `weeklyLimit: 1930000`, `weeklyLimitSource: "learned"`, `weeklyPausedUntil: "2026-11-20T01:50:00Z"`, and `used: 0`. The default-model task automatically fell back to `gemini-3.1-pro` and closed; the pinned `claude-opus-4-6` task stayed queued waiting for the weekly reset.
   - **Revision after review ([task 6580](/tasks/6580)):** the live captures below were taken with the first F2 fix (8-day dispatch ceiling), which has since been withdrawn. With the final code, `usage_limit_pause.until_at` and `weeklyPausedUntil` are again held to +24h (dispatch re-probes daily), while the `#weekly` `usage_capacity.reset_at` still records the provider-named `2026-11-20 01:50:00Z` (`TestLearnFromWeeklyPauseUsesTheProviderNamedReset`). See F2 below.

Captured `grain usage` table after fix (SQLite `step1b`):

```text
CONNECTION               MODEL                       USED     LIMIT      LEFT RESET                 WEEK  WEEK LIM WEEK RESET          BURN/H SUSTAINS
Claude Primary           (all models)                   0         ?    paused 11-20 01:50 UTC       1.9M     spent 11-20 01:50 UTC          0 0
Claude Primary           claude-opus-4-6                0         ?    paused 11-20 01:50 UTC       1.9M     spent 11-20 01:50 UTC          0 0
Gemini Fallback          gemini-3.1-pro              105k         ?         ? 11-17 06:50 UTC       105k         ? 11-24 01:50 UTC       105k -

warning: default model claude-opus-4-6 on Claude Primary hit its weekly limit and is paused until Nov 20 01:50 UTC; work falls back to gemini-3.1-pro
```

Captured `GET /api/usage` & `grain usage --json` key scope fields (identical between API and CLI aside from rolling `windowStart` milliseconds, on both SQLite and PostgreSQL):

```json
{
  "connection": "claude-primary",
  "used": 0,
  "pausedUntil": "2026-11-20T01:50:00Z",
  "pauseHorizon": "weekly",
  "resetAt": "2026-11-20T01:50:00Z",
  "weeklyUsed": 1930000,
  "weeklyLimit": 1930000,
  "weeklyLimitSource": "learned",
  "weeklyResetAt": "2026-11-20T01:50:00Z",
  "weeklyPausedUntil": "2026-11-20T01:50:00Z",
  "sustainableWorkers": 0
}
```

### Item 2 — Interval Pause in the Same Week and Auto-Mode Worker Cap

1. **Interval pause while `#weekly` exists (`step2a`):** Lifted the weekly pause (`DELETE /api/pause`), set the `#weekly` reset to `+12h` (late in the same week), ran a mid-week task (`400k` billable tokens on `claude-opus-4-6`), then triggered a 5-hour interval refusal (`You've hit your 5-hour limit · resets 4:06am (UTC)`).
   - **SQLite & PostgreSQL:** ✅ `usage_capacity` held **both** rows side by side without clobbering `#weekly`:
     - `claude-primary | "" | 400000 | 18000000 | reset_at=2026-11-17 04:06:00Z`
     - `claude-primary | #weekly | 1930000 | 604800000 | reset_at=2026-11-17 13:51:42Z`
   - `GET /api/usage` and `grain usage --json` reported `pauseHorizon: "interval"`, `used: 400000`, `limit: 400000`, `limitSource: "learned"`, `weeklyUsed: 2330000`, `weeklyLimit: 1930000`, `weeklyResetAt: "2026-11-17T13:51:42Z"`, and warning `default model claude-opus-4-6 on Claude Primary hit its 5h interval limit and is paused until 04:06 UTC; work falls back to gemini-3.1-pro`.
2. **Auto-mode worker cap late in the week (`step2b`):** Lifted the interval pause, set `mode: "auto"` (`max_workers: 4`), set `claude-primary#weekly` capacity to `2.53M` (`200k` remaining with 12h left in the week) and a configured budget on `gemini-fallback/gemini-3.1-pro`.
   - **Baseline `a771a9973`:** ❌ **F5** — `effectiveWorkers` dropped from `4` to `1` (`sustainableWorkers: 0`), and `advisories` warned `at this rate Claude Primary weekly limit runs out in ~30m`, but `warnings` emitted **no** default-model run-out warning for `claude-opus-4-6` because Claude's account-wide (`model == ""`) scope was ignored by `pkg/usage/usage.go` `messages()`.
   - **After fix (SQLite & PostgreSQL):** ✅ `mode: "auto"`, `maxWorkers: 4`, `effectiveWorkers: 1`, `sustainableWorkers: 0`, advisory `at this rate Claude Primary weekly limit runs out in ~30m; sustainable ≈ 0 workers`, and default-model warnings for **both** `claude-opus-4-6 on Claude Primary` and `gemini-3.1-pro on Gemini Fallback`.

### Item 3 — Persistence Across Restart and `grain/config` Sync

1. **Daemon restart (`step3a` → `step3b`):** Stopped the real daemon and restarted it against the same store while the weekly pause was active.
   - **SQLite & PostgreSQL:** ✅ `usage_capacity` (`claude-primary | #weekly | 1930000 | 2026-11-20T01:50:00Z`), `usage_limit_pause`, `GET /api/usage`, `grain usage --json`, and the queued pinned task all survived restart identically.
2. **`grain/config` sync and `grain state check` (`step3c`):** Ran `grain state -data-dir ... sync` (with `GRAIN_DB_DRIVER=postgres GRAIN_DB_DSN=...` on Postgres) and `grain state check <state-repo>`.
   - **SQLite & PostgreSQL:** ✅ Both committed cleanly (`committed <sha>`) and passed `grain state check` (`ok: grain can load this`). Per `pkg/staterepo/bind.go`, `usage_capacity` is intentionally kept out of `SettingsTables` (so config `Apply` does not overwrite live learned capacities), while full `Export`/`Import` and `Sync`+`Load` carry `usage_capacity` rows (verified in `pkg/staterepo/weekly_usage_test.go`). The `#weekly` row in `usage_capacity` remained untouched before and after sync on both stores.

### Item 4 — Reset Times Across Time Zones, Fallback Notices, Both UIs, and DST

1. **Interactive fallback notices across 4 time-zone paths (`step1c`):** Seeded 4 interactive chats on `claude-opus-4-6` before the weekly pause:
   - Chat 1: filed with `X-Grain-Time-Zone: America/Los_Angeles`
   - Chat 2: filed with `X-Grain-Time-Zone: Asia/Kolkata`
   - Chat 3: no task time zone; deployment `TimeZone = America/New_York`
   - Chat 4: no task time zone; deployment `TimeZone = ""` (UTC fallback)
   After the 72h weekly pause took effect, posted a follow-up message to each chat to trigger live fallback onto `gemini-3.1-pro`:
   - **Baseline `a771a9973`:** ❌ **F3** — Every notice omitted the calendar date even though the reset was 3 days away (`paused until 5:22 PM PST`, `6:52 AM IST`, `8:22 PM EST`, `1:22 AM UTC`).
   - **After fix (SQLite & PostgreSQL):** ✅ Every notice named both the calendar date and local zone:
     - `America/Los_Angeles`: `because claude-opus-4-6 is paused until Nov 19, 5:50 PM PST.`
     - `Asia/Kolkata`: `because claude-opus-4-6 is paused until Nov 20, 7:20 AM IST.`
     - Deployment fallback (`America/New_York`): `because claude-opus-4-6 is paused until Nov 19, 8:50 PM EST.`
     - UTC fallback (`""`): `because claude-opus-4-6 is paused until Nov 20, 1:50 AM UTC.`
2. **Playwright filing from `/next/tasks` (`step4-playwright.json`):**
   - **Baseline `a771a9973`:** ❌ **F1** — Filing from `/next/tasks` in `America/Los_Angeles` and `Asia/Kolkata` contexts sent `requestHeader: null` (`X-Grain-Time-Zone` missing) and persisted `task.time_zone = ""`.
   - **After fix (SQLite & PostgreSQL):** ✅ Outgoing `POST /api/tasks` carried `X-Grain-Time-Zone: America/Los_Angeles` and `X-Grain-Time-Zone: Asia/Calcutta` (Chromium's canonical alias for `Asia/Kolkata`, accepted by Go's `model.ValidTimeZone` and rendered as `IST`), and `SELECT id, title, time_zone FROM task` confirmed both persisted identically on SQLite and PostgreSQL.
3. **`/next/telemetry` and `/next/settings/models` at 1440×900 & 390×844, light & dark:**
   - **Baseline `a771a9973`:** ❌ **F2 + F4** — Banners said `"Model quota exceeded — Claude Primary development paused"` (omitting `"Weekly"`) and reset pills showed clamped `Nov 17, 5:22 PM PST (in 23h 59m)`.
   - **After fix (SQLite & PostgreSQL):** ✅ All 8 viewport/theme/timezone combinations rendered:
     - Headline: `Weekly model quota exceeded — Claude Primary development paused`
     - `America/Los_Angeles` reset pill & banners: `resets Nov 19, 5:50 PM PST (in 71h 58m)` (`Paused · weekly limit`)
     - `Asia/Kolkata` reset pill & banners: `resets Nov 20, 7:20 AM GMT+5:30 (in 71h 58m)` (`Paused · weekly limit`)
     - Horizontal overflow: `sideways === 0` on all 8 combinations across both pages.
4. **Classic `/settings` UsageCard:**
   - **Baseline `a771a9973`:** ❌ **F6** — Reset and Week reset columns formatted with `formatMoment` (`11/17/2026, 5:22:37 PM` — seconds included, timezone omitted).
   - **After fix:** ✅ Formatted with `formatResetMoment`: `11/19/2026, 5:53 PM PST` (`America/Los_Angeles`) and `11/20/2026, 7:23 AM GMT+5:30` (`Asia/Kolkata`) — named zone, no seconds.
5. **DST transitions:** Verified in Go (`pkg/daemon/model_fallback_test.go`), Vitest (`ui-ng/src/app/shared/dispatch-status.spec.ts`), and Chromium `Intl` (`pw_walkthrough.mjs`):
   - `2026-03-08T09:30:00Z` → `Mar 8, 1:30 AM PST` / `2026-03-08T10:30:00Z` → `Mar 8, 3:30 AM PDT` (spring forward)
   - `2026-11-01T08:30:00Z` → `Nov 1, 1:30 AM PDT` / `2026-11-01T09:30:00Z` → `Nov 1, 1:30 AM PST` (fall back)
   - `2026-11-17T19:00:00Z` (`Asia/Kolkata`) → `Nov 18, 12:30 AM GMT+5:30` (midnight rollover)

## Findings, Reproductions, and Fixes

### F1 (Critical) — `/next/` `GrainApi` never sent `X-Grain-Time-Zone` on mutations

- **Reproduction:** Launch a Playwright context with `timezoneId: "America/Los_Angeles"` (or `"Asia/Kolkata"`), open `/next/tasks`, and submit a new task. On `a771a9973`, `POST /api/tasks` omitted `X-Grain-Time-Zone` and `task.time_zone` was stored as `""`, so fallback notices for tasks filed in `/next/` could never use the requester's time zone (only classic `ui/src/api.js` sent the header).
- **Fix:** Added `requesterTimeZone()` in `ui-ng/src/app/api/grain-api.service.ts` (preferring `localStorage["grain.timeZone"]` when set and not `"local"`, else `Intl.DateTimeFormat().resolvedOptions().timeZone`) and attached `X-Grain-Time-Zone` in `GrainApi.send()` on every mutation (`POST`, `PUT`, `PATCH`, `DELETE`).
- **Regression tests:** `ui-ng/src/app/api/grain-api.service.spec.ts` (`GrainApi X-Grain-Time-Zone header`) and `ui-ng/e2e/next-task-timezone.spec.ts`.

### F2 (Critical) — Weekly usage-limit pauses clamped to 24 hours by `maxUsageLimitPause`

- **Reproduction:** Emit a live provider refusal `You've hit your weekly limit · resets Nov 20, 1:22am (UTC)` (+72h). On `a771a9973`, `pkg/orchestrator/pause.go` `resumeAt()` unconditionally clamped `limit.ResetAt` to `now + 24h` (`maxUsageLimitPause`), truncating a 3-day weekly pause to 24h in `usage_limit_pause.until_at` and `usage_capacity.reset_at`.
- **Fix (revised after security review [task 6580](/tasks/6580)):** The first fix raised the dispatch ceiling to 8 days (`maxWeeklyUsageLimitPause`) for weekly refusals. Review pointed out that the refusal text can be an agent-written final answer, so a forged "weekly limit" could idle a connection for ~8 days instead of 24h. That ceiling is withdrawn: `resumeAt()` again holds dispatch for at most 24h for every refusal (a real weekly limit costs one more refused run per day). The provider-named reset is kept where it matters for the weekly horizon: `usage.ProviderResetAt` reads the `resets at <RFC3339>` that `agent.UsageLimitError.Error()` puts in the pause reason, believes it only within one weekly cycle plus a day of `now`, and `usage.LearnFromPause` records it as the `#weekly` `usage_capacity.reset_at`. `usage.IsWeeklyReason` stays exported for the horizon classification.
- **Regression tests:** `pkg/orchestrator/pause_test.go` (`TestResumeAtBoundsWhatTheProviderSaid`: weekly 3-day and absurd resets are both held to `maxUsageLimitPause`) and `pkg/usage/store_test.go` (`TestLearnFromWeeklyPauseUsesTheProviderNamedReset`: in-cycle, beyond-cycle and past resets).

### F7 (Medium, found by review [task 6600](/tasks/6600)) — Weekly headline matched any scope's weekly pause

- **Reproduction:** `summarizeDispatchStatus` with an interval pause on `gemini-key · gemini-pro` and an unrelated weekly-paused `claude-primary` scope headlined `"Weekly model quota exceeded on Gemini key · gemini-pro"`, because `apWeekly` was true when *any* usage scope was weekly.
- **Fix:** `ui-ng/src/app/shared/dispatch-status.ts` calls the headline weekly only when a paused entry's own reason is weekly or a weekly scope covers that entry (same connection; an account-wide scope or the same model). With no per-entry pauses it falls back to the aggregate reason and scopes as before.
- **Regression test:** `ui-ng/src/app/shared/dispatch-status.spec.ts` ("does not call an interval pause weekly because another connection's scope met its weekly limit"), which fails without the fix.

### F3 (High) — Multi-day fallback notices omitted the calendar date (and `formatNoticeText` missed dated 12h UTC with comma)

- **Reproduction:** Trigger a live fallback on an interactive chat while `claude-opus-4-6` is paused until +72h. On `a771a9973`, `pkg/daemon/agents.go` `pauseClock()` always formatted `"3:04 PM MST"`, producing `paused until 5:22 PM PST` with no date. In addition, `ui-ng/src/app/shared/dispatch-status.ts` `formatNoticeText` did not match an optional comma after the day (`Nov 20, 1:22 AM UTC`).
- **Fix:** Updated `pauseClock(now, at, loc)` in `pkg/daemon/agents.go` to format `"Jan 2, 3:04 PM MST"` whenever `at.In(loc)` falls on a different calendar date than `now.In(loc)`, and `"3:04 PM MST"` on the same day. Updated `formatNoticeText` in `ui-ng/src/app/shared/dispatch-status.ts` to accept optional comma after the day.
- **Regression tests:** `pkg/daemon/model_fallback_test.go` (`TestPauseClockNamesTheLocalZone` multi-day, midnight rollover, and spring/fall DST cases) and `ui-ng/src/app/shared/dispatch-status.spec.ts`.

### F4 (High) — `/next/` dispatch banner omitted "Weekly" when `agentPause` was active, and Fleet pause-lift left stale weekly cache fields

- **Reproduction:** On `a771a9973`, a live weekly pause sets both `config.agentPause` and `usage.scopes[].pauseHorizon = "weekly"`. In `ui-ng/src/app/shared/dispatch-status.ts`, the `agentPause` branch fired first and hardcoded `"Model quota exceeded — Claude Primary development paused"`. Also, `ui-ng/src/app/fleet/fleet-page.component.ts` cleared `pausedUntil` on optimistic pause lift without clearing `weeklyPausedUntil` or `pauseHorizon`.
- **Fix:** Updated `summarizeDispatchStatus` to detect weekly pauses (from pause reasons or `usage.scopes`) and emit `"Weekly model quota exceeded — ..."`, and updated Fleet's optimistic pause-lift patch to also clear `weeklyPausedUntil` and `pauseHorizon`.
- **Regression tests:** `ui-ng/src/app/shared/dispatch-status.spec.ts`, `ui-ng/src/app/fleet/fleet-page.component.spec.ts`, and `ui-ng/e2e/next-task-timezone.spec.ts`.

### F5 (High) — Account-wide weekly/interval limits skipped default-model run-out warnings; `LearnFromPause` did not advance recurring weekly cycles >7d old

- **Reproduction:** In `step2b`, Claude's account-wide (`model == ""`) weekly limit (`2.33M / 2.53M`, running out in ~30m) emitted an advisory on `Claude Primary` but **zero** warning for default model `claude-opus-4-6`. Separately, in `pkg/usage/store.go`, `LearnFromPause` computed `start = c.ResetAt - 7d` without advancing `c.ResetAt` forward by multiples of `WeeklyWindow` when the previous anchor was more than 7 days in the past.
- **Fix:** Extracted shared `weeklyCycleBounds(resetAt, now)` in `pkg/usage/weekly.go`, used it in both `computeScope` (`pkg/usage/usage.go`) and `LearnFromPause` (`pkg/usage/store.go`, also filtering `r.FinishedAt.Before(start)`), and updated `messages()` in `pkg/usage/usage.go` to check connection-wide (`model == ""`) scopes for default-model run-out warnings.
- **Regression tests:** `pkg/usage/weekly_test.go` (`TestAccountWideWeeklyLimitWarnsOnDefaultModel`), `pkg/usage/store_test.go` (`TestLearnFromWeeklyPauseAdvancesRecurringResetCycle`), and `pkg/staterepo/weekly_usage_test.go` (`TestWeeklyUsageCapacitySurvivesExportImportAndConfigSync`).

### F6 (Medium) — Classic `UsageCard` printed reset columns without timezone name and with seconds

- **Reproduction:** Open classic `/settings` → Models UsageCard during a weekly pause. On `a771a9973`, Reset and Week reset cells rendered `11/17/2026, 5:22:37 PM` via `formatMoment` instead of PR #1606's `formatResetMoment`.
- **Fix:** Switched `s.resetAt` and `s.weeklyResetAt` in `ui/src/components/settings/UsageCard.jsx` from `formatMoment` to `formatResetMoment`.
- **Regression tests:** `ui/src/components/settings/UsageCard.test.jsx`.

## Verification

| Check | Command / Scope | Result |
| --- | --- | --- |
| Live walkthrough (SQLite) | `GRAIN=/home/debian/bin/grain STORE=sqlite RUN_PW=1 SHOTS_DIR=/tmp/shots-after-sqlite bash docs/test-runs/2026-11-16-weekly-usage/run_walkthrough.sh` | ✅ Passed (all 4 items + 16 screenshots + `contact-sheet.png`) |
| Live walkthrough (PostgreSQL) | `GRAIN=/home/debian/bin/grain STORE=postgres GRAIN_REQUIRE_POSTGRES=1 RUN_PW=1 SHOTS_DIR=/tmp/shots-after-postgres bash docs/test-runs/2026-11-16-weekly-usage/run_walkthrough.sh` | ✅ Passed (all 4 items + `grain state sync` & `check`) |
| Go unit, Postgres, hygiene & ports | `GRAIN_REQUIRE_POSTGRES=1 go test ./pkg/usage/... ./pkg/orchestrator/... ./pkg/daemon/... ./pkg/staterepo/... ./pkg/ui/... ./tests/hygiene/... ./pkg/ports/...` | ✅ All 8 packages passed |
| Angular `/next/` unit & lint | `cd ui-ng && npm test -- --run && npm run lint && npx tsc -p tsconfig.app.json --noEmit` | ✅ 70 files / 693 tests passed, 0 lint/tsc errors |
| Classic UI unit | `cd ui && npm test -- --run src/components/settings/UsageCard.test.jsx` | ✅ 4/4 passed |
| Frontend formatting & build | `make fmt-frontend && make frontend` | ✅ Clean |
| Playwright `/next/` e2e | `next-task-timezone.spec.ts` (8/8 across desktop, `@phone`, `@dark`), `next-telemetry-weekly-usage.spec.js` (4/4), `next-paused-model-reset-local.spec.js` (4/4), `next-full-system.spec.js` (5/5) | ✅ 21/21 passed, 0 axe violations |
