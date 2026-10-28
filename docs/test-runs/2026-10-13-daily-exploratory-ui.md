# Daily Exploratory UI Testing & Defect Discovery Report (2026-10-13)

## Summary

- **Date:** 2026-10-13 (task 3517)
- **Build under test:** candidate `925d50306`. It reports itself as `v1.11.0-rc.1.0.20261013194736-925d50306127`.
- **Server:** `grain demo -addr 127.0.0.1:8420 -open=false -observability -merge-queue-states -sensitive-hold -mechanical-port -merge-rules -secrets-store -qualification`, on its embedded SQLite store. The frontend came from `cd ui && npm ci && npm run build`.
- **Browsers:** Playwright 1.62 Chromium and WebKit 26.5, both headless.
- **Viewports:** 1920×1080, 1440×900, 768×1024, 375×812.
- **Personas:** all sent as `X-Grain-User` from loopback.
  - `alice`: system admin and `default` admin, with Admin mode on
  - `bob`: `default` member, Admin mode off
  - `carol`: `payments` admin only
  - The demo's `default` team carries the display name "admin".
- **Stubs:** none. Every request went to the real demo server. The CI stub is the one `-observability` itself wires.
- **Verdict:** 3 new defects, all filed as candidate tasks. No crash screens, no uncaught page errors and no horizontal overflow in any engine, viewport or persona.
- **Last run's defect 3:** `grain demo -sensitive-hold -github-sim` exiting at startup is fixed. The demo now starts with both flags.

## Coverage

### Passive sweep

- 47 routes:
  - team-scoped: home, tasks, inbox, boards, sessions, repos, the repo page and its releases, schedules, templates, suites, ci, memory, new, and all 8 Team settings tabs
  - all 17 System tabs (`/admin/<tab>`)
  - `/metrics`, `/settings`, and task detail panes for tasks 4, 6, 18 and 21
- Run passes:
  - Chromium at all 4 viewports as `alice`
  - WebKit at 1440 and 375
  - `bob` at 1440 and 375
  - `carol` on `/t/payments/*`
- Listeners captured console errors and warnings, `pageerror`, failed requests and HTTP ≥ 400, blank and crash text, and `scrollWidth > clientWidth`.
- The only signals were expected:
  - `GET /api/repos/acme/widgets/github-branches` → 404. The demo has no GitHub client.
  - `GET /api/relay-settings` → 501, handled as "no relay".
  - `GET /api/teams/default/audit-logs` → 403 for `bob`. The panel says it was refused (bc0384875).
  - `GET /api/tasks/N` → 404 for `carol` on `default` tasks.

### Journeys

| Area | What was exercised | Result |
| --- | --- | --- |
| New task: "this fixes a red <base>" (6d3587d75) | The label follows the typed base (`release/1.0`, padded `  candidate  ` shows trimmed). It hides on "No repo". Ticking it and then switching to No repo files `noRepo:true, base:""` with no mark. | Pass |
| Task pane: fixes-red mark | Ticking it on waiting task 14 sends `PATCH {"fixesBaseBranch":true}`, which persists. Its link to task 13 stays: the demo's seeded repair task 13 carries no repair tag, so `DropBaseRepairWaits` finds nothing to drop, while 14's merge-queue note calls 13 the repair. This is a demo seed inconsistency, not a product bug. | Pass (seed note) |
| Depends-on loop refusal (81487896c) | Making 13 depend on 14 (14 → 13) gives 400 "task 14 already waits on task 13 (14 → 13), so … a loop neither could leave". The message shows inline and as a toast. | Pass |
| Task picker | Typing `14` + Enter in "Add a dependency…" attached **task 21**. | **Defect 2** |
| Repo standing work switch (3ea563982) | As `alice`: default Off → every item shows "Not run", which survives a reload. Release On while the default is Off → Release alone "Runs". Back to "say nothing" restores everything. With the default off, the Schedules list shows "not firing: skipped: this deployment doesn't run acme/widgets's standing work", and the release pane shows "automatic releases off". | Pass |
| Repo page as a team member | `bob` (member) gets every repo setting enabled: the standing-work buttons, the release toggle, CI-mode radios, Remove, workflows, and dispatch jobs. Each click → 403 toast. | **Defect 1** |
| Repo page unsaved-edit guard (30e3d59ff) | A dirty dispatch-jobs box asks before leaving via ← repos, the rail (schedules, inbox, tasks), browser Back and reload (`beforeunload`). | Pass |
| Models & connections merge (3dbf603ce) | The old `/settings/agents` and `/settings/connections` links land on the merged tab. Team default model → `PUT /api/teams/default {"defaultModel":…}`. Per-model effort high → `modelSettings`. Both persist across reload, and the schedule composition preview then reads "the team's default model (gemini-2.5-flash)". | Pass, except defect 3 |
| Memory page | `/t/default/memory` renders with a main landmark, and `/memory/xyz` falls back to it. | Pass |
| Team home counts (eaf91470b) | Running (2) and Queued (5) match the rail's counts. | Pass |
| Teams | Created a team from System › All Teams. Required-field, negative-number, bad-id and bad-role validation works (client and server). The new team gets a "3 steps left" checklist. `bob` switches into it from the phone drawer (375×812) and lands on the team home, which shows 0 tasks. A team with no target repos may file against any repo (`model.RepoAllowed`, by design). | Pass |
| Executors | Team › Executors: Issue token → shown once with the connect command → listed → Revoke asks for confirmation. System › Executors lists tokens. | Pass |
| Schedules / suites / templates | A schedule with base `  release/1.0  ` is saved trimmed. Pause, Resume and Run now each close the pane (by design). Run now confirms, then files task 24. A suite `count`/`maxPasses` of 101 → 400 "at most 100", and the form caps it at max=100. Template names may repeat (ids are the key). | Pass |
| CI board | Start a run (load workflows → observability workflow) deep-links to `/t/default/ci/<id>` with live job state. Cancel run confirms, and the run is then "cancelled". | Pass |
| Inbox | Approving the proposal clears the section, and the rail count drops 8 → 7. The `-merge-rules` seed leaves task 16 under "submit for merge … on no queue" while its row reads "Waiting for CI". The seed sets that pairing on purpose (auto-merge withdrawn by `auto_merge: never`). | Pass (seed note) |
| Task filters | `state` + `q`, `custom.priority`, open-task-then-Back, Back/Forward/Reload: the URL and chips are stable. A bare `priority` is an "unknown condition" chip, as before. Bulk select shows "2 selected" at 1440 and 375. | Pass |
| Resilience | Unknown and malformed URLs for settings tabs, admin tabs, schedules, suites, templates, boards, sessions, CI runs, releases and filters, plus `<script>` in the query. Each falls back or shows a "no X" notice, with no crash. Going offline for 9 seconds in WebKit at 375 shows the "cannot be reached … retrying" banner, which clears once back online. | Pass |

## Defects filed

1. **UI: Repo page, as a team member.**
   - Settings controls stay enabled for a member, and every click returns 403 "does not have permission to modify repository".
   - `RepoPage.jsx` draws `readOnly` from `gateWriteRefusal` (viewers and proposers only).
   - The server's `authorizeRepo` needs repo admin, and `repoAdministered(config, repo)` already exists.
2. **UI: Task picker.**
   - Typing an exact task id and pressing Enter picks a newer task whose title contains those digits (`14` → task 21, "…#140…").
   - `TaskPicker.jsx` sorts matches newest-first with no exact-id priority.
3. **UI: Team settings name the team by its id.**
   - "Showing settings for team: default", 'model team "default" runs on', and 'Provider connections for team "default"'.
   - Every other surface uses the display name ("admin") via `teamDisplayName`.

## Notes for the next run

- `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt npx playwright install chromium webkit` installs the browsers, then `sudo npx playwright install-deps chromium webkit` installs their libraries.
- Build the frontend before `go build`. The binary embeds `pkg/ui/static`, so a binary built while `vite build` is still running serves the old UI.
- The schedule pane's Pause, Resume and Run now use native `confirm()` dialogs. Playwright dismisses those unless a `page.on("dialog")` handler accepts them.
- Repo-page controls are named by `aria-label` (`"the repository default: off"`, `"Release: on"`). Rail entries are links whose names start with the section name.
