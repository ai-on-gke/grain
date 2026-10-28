# 2026-10-23: Daily exploratory UI testing and defect discovery

Task 4670 (`grain/eu/task-4670`): exploratory browser pass across the Grain web UI, looking for console/network errors, crash screens, layout overflow and broken journeys, and filing a reproduction task for each real defect.

## Environment

- **Code tested**: `candidate` at `6f656699c` (`v1.14.0-rc.1.0.20261022160427-6f656699c8c4`). `candidate` moved to `785adac18` during the run; the three commits in between change no frontend code except one line of `SystemPage.test.jsx`, so the result holds for `785adac18`.
- **Server**: `go build ./cmd/grain` then `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file …` (embedded SQLite throwaway store, seeded demo data), signed in through `POST /api/auth/access-code`.
- **Frontend**: production bundle from `cd ui && npm ci && npm run build` (Vite), served by the demo.
- **Browser**: Playwright 1.62.1 driving Chromium headless shell 151.0.7922.34. Go 1.26.6, Node 20.19.0, Linux 6.1.
- **Viewports**: 1920×1080, 1440×900, 768×1024 and 375×812, in light and dark colour schemes for the overflow sweeps.
- **Staging**: no staging URL is reachable from a task sandbox, so the local `grain demo` stood in for it, as the task allows.
- **Stubs**: none. Every request went to the real demo server. The only fault injection was Playwright's `context.setOffline` and a `page.route("**/api/**")` 500 responder, both used for the resilience checks below.
- **Listeners on every page**: `console` errors and warnings, `pageerror` (uncaught exceptions), every response ≥ 400, `requestfailed` other than aborted navigations, and native `alert`/`confirm` dialogs. Each page was also checked for document scrollWidth larger than the viewport, for elements past the right edge with no clipping ancestor, and for error-boundary text.

### Out of scope for this branch

Teams, per-team model assignments and team quota/isolation are not in the code under test. `main` and `candidate` are single-tenant since the reset (`docs/single-tenant-reset.md`), so there is no team switcher to exercise. The CI view reports "Not available" because `grain demo` runs without grain CI. The merge queue is only visible through task 5's batch state and System → Pipeline.

## Route crawl

There are 62 routes, each visited at all four viewports (248 page loads). They cover:
- the root, inbox, task list and task deep links, including a missing task;
- `/new`;
- repo overview, releases, settings, advanced settings, compare, and a repo that does not exist;
- the four boards and a missing board;
- processes, schedules, templates and suites, including missing ids;
- CI, including a missing run;
- sessions;
- all 16 Settings sections and a bogus section;
- all 8 System tabs;
- malformed query strings: `/?q=%E0%A4%A`, `/tasks?state=bogus&repo=%%%`, `/boards/board-1?window=abc` and `/system/metrics?window=-5`.

| Result | Detail |
|---|---|
| Uncaught page errors / error-boundary screens | None at any viewport. |
| Page-level horizontal overflow | None at any viewport. |
| Failing requests | Expected 404s only: `GET /api/tasks/99999` and `GET /api/ci/runs/run-999` for the deliberately missing ids, and `GET /api/repos/acme/widgets/github-branches` on task and process panes. The last is the demo having no GitHub credential (`errGitHubBranchesUnavailable` → 404); `branchRefs.js` treats it as "no suggestions". |
| Unknown ids | Missing task → `/` with the alert "no task 99999" (`loadTask` → `showError`). Missing schedule → `/processes`. Missing board → `/boards`. Unknown settings section → `/settings/general`. Unknown repo → an empty repo page. |

## Journeys

| Area | What was exercised | Outcome |
|---|---|---|
| Search & filters, URL sync | Typed a search on `/tasks` (URL gained `?q=pagination&f=state:none:deferred\|closed&f=kind:none:interactive`), reloaded (box and rows kept), changed state from the sidebar (Queued, then Running), then Back, Back, Forward. The URL, rows and search box stayed in step at every step. Opening a task and pressing Back returned to the filtered list. Direct URLs `f=state:any:queued\|running`, `f=repo:any:acme/widgets&f=state:any:queued` and `sort=title:desc` matched the expected rows. | Pass |
| Malformed filter URLs | `f=state:bogus:x`, `f=::`, `sort=nope:sideways` and `q=%25` were ignored or normalized without a crash (`sort=nope:asc`). Custom-field filters need the `field.` prefix; a bare `f=priority:any:high` is dropped as an unknown attribute. | Pass (noted) |
| New task modal | Empty form → Create disabled. Filled in title, description, repo, estimate `-3`, and a malformed due date (the browser's date input rejects it). Created task 13 as queued with the default capabilities. A negative estimate is accepted because the demo's `estimate` field declares no minimum. | Pass |
| Bulk operations | Selected tasks 1 (proposed) and 13 (queued). The bulk bar offers Approve/Submit/Defer/Undefer/Close/Reopen/Bulk edit/capability attach. Bulk edit marks mixed fields "(mixed across the selection)" and keeps Apply disabled until a field is ticked. Bulk Defer → both deferred. Bulk Undefer → 1 back to proposed and 13 back to queued, as `SetDeferred` documents. | Pass |
| Task detail | Commented (body cleared, comment shown). Edit with an empty title → Save disabled. Pressing Escape on an unsaved edit asks "You have unsaved changes here. Leave and discard them?"; discarding left the title unchanged. Defer/Undefer from the pane works. | Pass |
| Deferral history | Undefer replaces the `deferred` transition: `proposed → deferred` becomes `proposed → proposed`. This is a documented limitation (`model.Transitions`: one `deferred_at` column, so an ended deferral is not on the timeline), not filed. | Known |
| Schedules | Edited `sched-2`. Changing the time zone to `America/New_York` relabels the time box "Time (America/New_York, EDT)". The Repeat menu offers every N hours, daily, weekly and monthly. Cancel discards edits. Pause sends `PATCH {"enabled":false}` and Resume `{"enabled":true}`; both close the pane. Run now asks a native confirm, then `POST /api/schedules/sched-2/run` → 201 and task 15 filed. A new every-6-hours schedule saved as `sched-3`; `0` hours is blocked by the input's `min=1`. | Pass |
| Schedule API validation | `everyNHours: 0`, `timeOfDay: "25:99"`, `weekday: "funday"`, empty title and `timeZone: "Mars/Olympus"` each returned 400 with a specific message. | Pass |
| Suites | New suite: Max passes `0` is blocked by `min=1`. Created "Probe suite" (`count` mode, 3 passes, one template) → `suite-2`. | Pass |
| Settings → Engine | Bad values (`banana`, `-5s`, `0s` poll interval; `-1` workers; `forever` gather window) each got a 400 and "Couldn't save <field>" with the server's message. Valid edits autosave. **Defect:** putting the box back to its saved value leaves the error status and alert up until reload (filed, see below). | Fail (filed) |
| Settings → Executors | Issued a token: the dialog shows the secret once, plus a `grain executor --token … --server <server-addr:port>` command. Revoke asks a confirmation dialog, then the token shows "revoked" with a timestamp. "2 connected · see System → Sandboxes" links through. | Pass |
| Inbox triage | Answered task 4's question from the inbox → comment posted, task back to queued, inbox count updated. Approve/Defer/Decline and Submit sections render with their bulk buttons. | Pass |
| System → Pipeline | Dispatch group shows the running state, with a reason box and "Pause pipeline". | Pass (not paused) |
| Network interruption | Went offline for 15s → "This deployment cannot be reached … retrying automatically" banner, and the New Task dialog still opens. Back online → banner gone within one poll, page intact. Every `/api/**` returned 500 for 12s → no banner and no alert, stale data kept, `console.warn("grain: poll failed")`. That is deliberate (`App.jsx`: "Deliberately quiet"; `isUnreachable` covers only network failures and 502/503/504). Recovered on its own. | Pass (noted) |
| Hostile content | A 240-character unbroken title, a description with `<img onerror>` and a `javascript:` markdown link, and a long-URL comment. No injected `<img>`, no `javascript:` hrefs, and no page overflow on the list, detail, boards or inbox at 375, 768 or 1440. | Pass |
| Overlays at phone/tablet, light and dark | New task, task detail (×2), schedule, suite, template, System → Sandboxes, Settings → Executors and Models, Timeline and Report boards, repo page. No overflow. | Pass |
| Tree board at ≤390px | Rows with a status pill and a PR link scroll sideways inside `.task-tree`. This is the intended behaviour from ea3ec549a (`overflow-x: auto`, `min-width: 6rem` on the title) so pills never clip; not filed. | By design |

## Defects filed

1. **UI: Settings autosave – "Couldn't save <field>" status sticks after the field is corrected back to its saved value.**
   - Cause: in `ui/src/useSettingsSection.js`, the blur handlers skip a save whose value equals the stored one, and only a successful save clears `error`. So after a rejected value is replaced by the original, the status line and alert keep reporting a failure until reload.
   - Affects every autosaved text and number box in Settings.
   - Same code on `main` and `candidate`. Filed via `propose_task` against `candidate` with full repro and evidence.

No other defect reproduced.
