# Test Run: Interactive Templates, Session Exclusion from Task Views, Named & Sidebar-Pinned Sessions on `/session/new`, and Browser Timezone Fallback (`docs/test-plans/2026-10-26.md` §6)

- **Date:** 2026-10-26
- **Plan Reference:** `docs/test-plans/2026-10-26.md` §6 (Interactive Templates, Session Exclusion from Task Views, Named & Sidebar-Pinned Sessions on `/session/new`, and Browser Timezone Fallback)
- **Target Commits Under Test:**
  - `f229c151b` (`feat(templates): interactive templates`, PR #4652)
  - `f883f12fe` (`feat(ui): interactive switch on templates and UseTemplateOverlay`, PR #4652)
  - `398e56e28` (`refactor(ui): drop interactive-task controls and the Kind filter from task views`, PR #4897)
  - `13ea11e04` (`fix(ui): count interactive tasks the way the task list now shows them`, PR #4897)
  - `5bd1c9b40` (`feat(ui): name a new session on /session/new and optionally pin it to the sidebar`, PR #4845)
  - `996d7e581` (`fix(ui): keep Create session on the same row as Session name on /session/new`, PR #4845)
  - `59298d414` (`feat(ui): fall back to the browser's IANA time zone when the deployment sets none`, PR #4883)
  - `4dc8e9a8a` (`feat(ui): send X-Grain-Time-Zone on UI write requests`, PR #4883)
  - `e1d84c076` (`fix(ui): keep the open settings section mounted across the phone breakpoint`, PR #4814)
- **Environment:**
  - **OS / Arch:** Linux (`debian` bookworm `amd64`)
  - **Server:** `grain demo -addr 127.0.0.1:0 -open=false -github-sim -fake-ci` backed by embedded SQLite (`grain.db`)
  - **CLI:** `grain declarations check` (`grain decl`)
  - **Browser:** Headless Chromium via Playwright (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) at desktop (`1440x900`) and phone (`390x844`) viewports, in both default (`UTC`) and `America/New_York` browser timezone contexts

---

## Executive Summary

All scenarios in `docs/test-plans/2026-10-26.md` §6 were executed end-to-end against a real `grain demo -github-sim -fake-ci` instance, embedded SQLite database, `grain declarations check` CLI, and Playwright Chromium browser.

During the walkthrough and accompanying code inspection, **two bugs were discovered, reproduced deterministically, fixed in this branch, and covered with unit and end-to-end regression tests**:

1. **Bug 1 (Fixed): `UseTemplateOverlay` and `GET /api/templates/{id}/composition` reported `"waits for approval"` (`source: "filing"`, `"chosen here"`) for interactive templates when `approved` was false.**
   - **Symptom:** When opening `"Use…"` (`UseTemplateOverlay.jsx`) on an interactive template (`template.interactive === true`), the form hides the `"Approved"` checkbox and displays `"This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed."` However, `UseTemplateOverlay` passed its local `approved` state (`false` whenever `config.approvedByDefault` is `false` or after unticking `"Approved"` on a non-interactive template before switching the template picker) to `GET /api/templates/{id}/composition` and `POST /api/tasks`, and `Client.TemplateComposition` (`pkg/ui/templates.go`) passed `approved` straight through to `Store.TemplateComposition` with `Sources.Approved = LayerFiling`. As a result, `"What this files"` (`CompositionPanel`) rendered **`Approval: waits for approval — chosen here`** right below the live-chat note (and even when `approved` was `true`, rendered **`chosen here`** despite no checkbox being shown), contradicting both the note and `Client.CreateTask` (`pkg/ui/client.go:2265`), which always approves an interactive template task filed by a person.
   - **Fix:** Updated `Client.TemplateComposition` (`pkg/ui/templates.go`) so that when `tmpl.Interactive` is `true`, `approved` is forced to `true` and `comp.Sources.Approved` is set to `model.LayerTemplate` (`"from the template"`). Updated `UseTemplateOverlay.jsx` to compute `const effectiveApproved = template?.interactive ? true : approved;` for both `previewPath` and `POST /api/tasks`.

2. **Bug 2 (Fixed): `relayProposedTasks` (`pkg/orchestrator/finish.go`) dropped `comp.Interactive` when replaying an end-of-run `propose_task` tool call naming an interactive `template_id`.**
   - **Symptom:** While live `propose_task` calls go through `Client.CreateTask` (`fromTemplate`, which sets `req.Interactive = comp.Interactive` and leaves the proposal unapproved because `proposedBy != nil`), end-of-run proposal replay in `relayProposedTasks` (`pkg/orchestrator/finish.go:1399-1425`) resolved `comp := model.ComposeTemplateTask(*tmpl, ...)` and copied `comp.Title`, `comp.Body`, `comp.Target`, `comp.Base`, and `comp.NetworkAccess`, but omitted `comp.Interactive`. Any `propose_task` naming an interactive `template_id` replayed at the end of a run was therefore stored with `interactive = 0` (`false`).
   - **Fix:** Propagated `interactive = comp.Interactive` in `relayProposedTasks` (`pkg/orchestrator/finish.go`) onto `proposal.Interactive`, while preserving the unapproved proposal rules.

---

## Detailed Walkthrough & Verification Results

### 1. Interactive Templates & Session Exclusion from Task Views (`f229c151b`, `f883f12fe`, `398e56e28`, `13ea11e04`)

#### 1.1 Creating an Interactive Template in `TemplateOverlay.jsx`
1. Navigated to `[/processes?kind=template](/processes?kind=template)` and clicked **`+ New template`**.
2. Verified the **`"Interactive (agent can chat with you)"`** checkbox (`input[name="interactive"]`) is rendered directly beneath **`"Auto-merge once checks pass"`** (`input[name="autoMerge"]`) with no overlap at both desktop (`1440x900`) and phone (`390x844`) viewports:
   - **Desktop (`1440x900`):**
     - `Auto-merge once checks pass`: `{ x: 250, y: 857.38, width: 699, height: 42 }`
     - `Interactive (agent can chat with you)`: `{ x: 250, y: 899.38, width: 699, height: 42 }`
   - **Phone (`390x844`):**
     - `Auto-merge once checks pass`: `{ x: 6, y: 903.28, width: 352, height: 42 }`
     - `Interactive (agent can chat with you)`: `{ x: 6, y: 945.28, width: 352, height: 42 }`
3. Filled in:
   - **Name:** `Interactive incident triage`
   - **Task title:** `Triage active incident`
   - **Description:** `Investigate the active incident and report findings in chat.`
   - **Repo:** `acme/widgets`
   - **Interactive (agent can chat with you):** checked (`true`)
4. Clicked **`Add template`**.
   - **Request (`POST /api/templates`):**
     ```json
     {
       "name": "Interactive incident triage",
       "title": "Triage active incident",
       "description": "Investigate the active incident and report findings in chat.",
       "repo": "acme/widgets",
       "base": "",
       "autoMerge": false,
       "interactive": true,
       "networkAccess": "",
       "nothingNeededAction": "close",
       "reads": [],
       "writes": [],
       "capabilities": [],
       "fields": []
     }
     ```
   - **Response (`201 Created`):** `id: "template-3"`, `interactive: true`.
   - **SQLite (`SELECT id, name, title, interactive FROM template WHERE id = 'template-3'`):**
     ```json
     [{"id": "template-3", "name": "Interactive incident triage", "title": "Triage active incident", "interactive": 1}]
     ```

#### 1.2 Instantiating an Interactive Template in `UseTemplateOverlay.jsx`
1. Opened **`Use…`** on a non-interactive template, unticked **`Approved`**, and then switched the **`Template`** select in `UseTemplateOverlay` to **`Interactive incident triage`**.
2. Verified that:
   - The **`Approved`** checkbox is removed from the DOM (`count === 0`).
   - In its place, `UseTemplateOverlay` renders the explanatory text:
     > `"This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed."`
   - **Before Fix (Bug 1):** `UseTemplateOverlay` requested `GET /api/templates/template-3/composition?repo=&base=&approved=false` and rendered `Approval: waits for approval — chosen here` in `#composition-preview`.
   - **After Fix:** `UseTemplateOverlay` requests `GET /api/templates/template-3/composition?repo=&base=&approved=true` and `Client.TemplateComposition` returns:
     ```json
     {
       "name": "approval",
       "values": ["approved when filed"],
       "source": "template"
     }
     ```
     rendering **`Approval: approved when filed — from the template`** in `#composition-preview`.
3. Clicked **`Create task`**:
   - **Request (`POST /api/tasks`):** `{"templateId":"template-3","fields":{},"repo":"","base":"","approved":true}`
   - **Response (`201 Created`):** `{"id":"13","title":"Triage active incident","state":"queued","approved":true,"interactive":true}`
   - **SQLite (`task` row `13`):** `interactive = 1`, `approval_actor_kind = 'human'`, `approval_actor_id = 'debian'`, `approved_at` populated.
   - **Navigation:** Browser navigated immediately to `[/tasks/13](/tasks/13)` with the live `.task-chat` (`TaskChat`) composer visible.

#### 1.3 Removal of `"Interactive session"` from `NewTaskOverlay.jsx` (`398e56e28`)
1. Opened `[/new](/new)` (`NewTaskOverlay`) and expanded **`Advanced options`**.
2. Verified:
   - `input[name="interactive"]` count is **`0`**.
   - Text matching `/Interactive session/i` count is **`0`**.
   - Ordinary task filing buttons (**`Queue immediately`**) remain visible as expected.

#### 1.4 Agent Proposal (`propose_task` / `POST /api/tasks` with `proposedBy`) Naming an Interactive `templateId`
1. Sent `POST /api/tasks` with `{"repo":"acme/widgets","templateId":"template-3","proposedBy":"1","interactive":true}` (simulating a live `propose_task` call from Task 1).
2. Verified the created task (`id: "14"`):
   - `interactive: true`
   - `state: "proposed"` (`approved` is `false` / omitted, `approval_actor_kind IS NULL` in SQLite).
3. Also verified `relayProposedTasks` (`pkg/orchestrator/finish.go`) via unit test `TestRelayProposedTasksCarriesInteractiveFromTemplate`: an end-of-run `propose_task` tool call naming `"template_id": "template-interactive"` produces a child task with `Interactive: true` and `Approval: nil`.

#### 1.5 Exclusion of Session & Interactive Tasks (`isSessionTask`) from Task Views & Counts (`398e56e28`, `13ea11e04`)
1. Inspected `GET /api/tasks`:
   - Session/interactive tasks present in the store:
     - `[Task #12](/tasks/12)` (`"Director Session"`, `state: "queued"`, `interactive: true`, `consoleSession: "director"`)
     - `[Task #13](/tasks/13)` (`"Triage active incident"`, `state: "queued"`, `interactive: true`)
     - `[Task #14](/tasks/14)` (`"Triage active incident"`, `state: "proposed"`, `interactive: true`)
   - Also filed a non-interactive child task `[Task #15](/tasks/15)` (`"Child task filed from interactive session"`, `proposedBy: "13"`, `approved: true`) under interactive `[Task #13](/tasks/13)`.
   - Total non-session tasks (`!t.consoleSession && !t.interactive`): **15 tasks** across the deployment (`proposed: 1`, `queued: 3`, `running: 2`, `awaiting reply: 2`, `awaiting submit: 1`, `merging: 1`, `deferred: 1`, `closed: 1`, `blocked: 1`; plus 2 merged tasks).
2. Verified **Main Task List (`[/](/f=)`)**:
   - Neither `"Director Session"` (`#12`) nor `"Triage active incident"` (`#13`, `#14`) appears in `TaskList`.
3. Verified **Sidebar State Counts (`Sidebar.jsx`)**:
   - Sidebar displays `tasks 15`, `proposed 1`, `queued 3`, `running 2`, `awaiting reply 2`, `awaiting submit 1`, `merging 1`, `deferred 1`, `closed 1`, `blocked 1` — matching the exact counts of non-session tasks (`#12`, `#13`, and `#14` are excluded from `tasks`, `queued`, and `proposed`).
4. Verified **Repository Task Counts (`[/repos](/repos)`, `state.js` `repoSummary`)**:
   - `acme/widgets` displays `proposed 1`, `queued 3`, `running 2`, `awaiting reply 2`, `awaiting submit 1`, `merging 1`, `deferred 1`, `closed 1`, `blocked 1`, `12 tasks` — excluding `#12`, `#13`, and `#14`.
5. Verified **Kanban, Gantt, and Tree Boards (`[/boards](/boards)`)**:
   - **Kanban board (`kind: "kanban"`):** Excludes `"Director Session"` and `"Triage active incident"` (`kanbanIncludesInteractive === false`).
   - **Gantt board (`kind: "gantt"`):** Excludes `"Director Session"` and `"Triage active incident"` (`ganttIncludesInteractive === false`).
   - **Tree board (`kind: "tree"`, `TaskTreeBoard.jsx` with `keepSessions: true`):** Includes interactive parent `[Task #13](/tasks/13)` (`"Triage active incident"`) and nests its non-interactive follow-up `[Task #15](/tasks/15)` (`"Child task filed from interactive session"`) directly beneath it (`treeIncludesInteractiveParentAndChild === true`).
6. Verified **Sessions View (`[/sessions](/sessions)`)**:
   - Displays `"Director"` standing session (`sessionsPageIncludesDirector === true`).
7. Verified **Legacy Filter URL Stripping (`[/?f=kind:none:interactive](/?f=kind:none:interactive)`)**:
   - Visiting `/?f=kind:none:interactive` strips the legacy `kind:none:interactive` clause via `isLegacyInteractiveClause`, normalizes the URL to `/`, and renders **0** filter chips (`legacyClauseRenderedAsChip === false`).

#### 1.6 Repository Declaration CLI (`grain declarations check`)
1. Created `.grain/templates/live-triage.yml` with `interactive: true`:
   ```yaml
   name: Live incident triage
   title: Triage incident interactively
   body: |
     Investigate the active incident and report findings in chat.
   base: main
   auto-merge: false
   interactive: true
   ```
2. Ran `grain declarations check -repo acme/widgets <dir>`:
   - Exit code `0`; human output printed:
     ```text
     template live-triage  .grain/templates/live-triage.yml
       id:         repo:template:acme:widgets:live-triage
       name:       Live incident triage
       title:      Triage incident interactively
       base:       main
       auto-merge: false
       interactive: true
     ```
   - `grain -json declarations check -repo acme/widgets <dir>` output included `"interactive": true` on the template entry.

---

### 2. Named & Sidebar-Pinned Sessions on `/session/new` (`5bd1c9b40`, `996d7e581`)

#### 2.1 Desktop (`1440x900`) and Phone (`390x844`) Layout on `[/session/new](/session/new)`
1. Opened `[/session/new](/session/new)` at desktop (`1440x900`) and phone (`390x844`) viewports and measured bounding boxes of the **`"Session name"`** `TextField` and **`"Create session"`** `Button`:
   - **Desktop (`1440x900`):**
     - `Session name` field: `{ x: 260, y: 133.03, width: 601.72, height: 63.91 }`
     - `Create session` button: `{ x: 873.72, y: 133.03, width: 122.28, height: 36.50 }`
     - Same horizontal row (`y` aligned at `133.03px`, button to the right of input at `x: 873.72 >= 861.72`).
   - **Phone (`390x844`):**
     - `Session name` field: `{ x: 14.39, y: 186.05, width: 361.22, height: 83.81 }`
     - `Create session` button: `{ x: 14.39, y: 281.86, width: 361.22, height: 36.50 }`
     - Cleanly stacked vertically (`281.86 >= 269.86`) and full-width within the `390px` viewport (`14.39 + 361.22 = 375.61 <= 390`, zero horizontal overflow).

#### 2.2 Creating a Named, Sidebar-Pinned Session Without a First Message
1. Entered **`Incident triage`** in **`"Session name"`**, checked **`"Pin to sidebar"`**, left the chat composer empty, and clicked **`"Create session"`**.
2. Verified:
   - **Request (`POST /api/console-sessions`):** `{"title":"Incident triage","attachments":[]}` (no `message` field sent).
   - **Response (`201 Created`):** `created: "incident-triage"`, session `{ id: "incident-triage", title: "Incident triage", taskId: "16" }`.
   - **Navigation:** Browser navigated to `[/session/incident-triage](/session/incident-triage)`.
   - **Sidebar Pinning:** `"Incident triage"` appeared in the left navigation rail's **Pinned** section (`pinnedBeforeReload: true`), persisted in `localStorage["grain.pinned"]` as `[{"kind":"view","id":"inbox"},{"kind":"session","id":"incident-triage"}]`, and remained visible in the sidebar after a full page reload (`pinnedAfterReload: true`).

#### 2.3 Creating an Unnamed Session With a First Message (Title Fallback)
1. Navigated to `[/session/new](/session/new)`, left **`"Session name"`** blank, typed **`Investigate elevated latency on api-gateway`** into the chat composer, and clicked **`Send`**.
2. Verified:
   - **Request (`POST /api/console-sessions`):** `{"message":"Investigate elevated latency on api-gateway","attachments":[]}` (no `title` field sent).
   - **Response (`201 Created`):** `created: "investigate-elevated-latency-on-api-gateway"`, with `title: "Investigate elevated latency on api-gateway"` derived from the first message.
   - **Navigation:** Browser navigated to `[/session/investigate-elevated-latency-on-api-gateway](/session/investigate-elevated-latency-on-api-gateway)`.

---

### 3. Browser Timezone Fallback (`X-Grain-Time-Zone`, `59298d414`, `4dc8e9a8a`) & Responsive Settings Section Mounting (`e1d84c076`)

#### 3.1 Browser Timezone Fallback & `X-Grain-Time-Zone` Header Propagation
1. Launched a Playwright browser context configured with `timezoneId: "America/New_York"` against the `grain demo` server (where deployment `timeZone` is unset).
2. Navigated to `[/settings/preferences](/settings/preferences)`:
   - Verified `GET /api/config` returned `timeZone: ""` (empty).
   - Verified the **Time zone** card (`#time-zone`) selected option displayed **`"Browser default (America/New_York)"`** and helper text `"Times here follow this browser's own clock until it picks one."`
3. Triggered three UI write requests from this browser context:
   - **Task creation (`POST /api/tasks` via `[/new](/new)`):**
     - Captured request header: `X-Grain-Time-Zone: America/New_York`
     - Created `[Task #18](/tasks/18)` (`"Timezone fallback verification task"`).
   - **Task comment (`POST /api/tasks/13/comments` via `TaskChat`):**
     - Captured request header: `X-Grain-Time-Zone: America/New_York`
   - **Console session creation (`POST /api/console-sessions` via `[/session/new](/session/new)`):**
     - Captured request header: `X-Grain-Time-Zone: America/New_York`
     - Created session backing `[Task #19](/tasks/19)` (`"Session timezone fallback check"`).
4. Queried SQLite (`SELECT id, title, time_zone FROM task WHERE id IN (18, 19) ORDER BY id`):
   ```json
   [
     {"id": "18", "title": "Timezone fallback verification task", "time_zone": "America/New_York"},
     {"id": "19", "title": "Session timezone fallback check", "time_zone": "America/New_York"}
   ]
   ```

#### 3.2 Responsive Settings Section Mounting Across Phone Breakpoint (`e1d84c076`)
1. Opened `[/settings/sandboxes](/settings/sandboxes)` at desktop viewport (`1440x900`).
2. Stored direct object references to the mounted `[data-testid="settings-section"]` container and its `<h1>` heading node on `window`.
3. Resized the viewport across the phone breakpoint to `390x844` (`document.documentElement.dataset.phone === "true"`), then back to `1440x900`:
   - **At `390x844`:**
     - `sameSectionNode: true` (exact same DOM element instance; not unmounted/remounted)
     - `sameHeadingNode: true`
     - `dataSection: "sandboxes"`
     - `backBtnVisible: true` (`[data-testid="settings-back"]` rendered for phone header)
   - **Back at `1440x900`:**
     - `sameSectionNode: true`
     - `sameHeadingNode: true`
     - `dataSection: "sandboxes"`
     - `navVisible: true` (`[data-testid="settings-nav"]` restored alongside the still-mounted section)

---

## Bugs Fixed & Regression Tests Added

| # | Location | Description | Regression Tests Added |
|---|---|---|---|
| 1 | `pkg/ui/templates.go` (`TemplateComposition`), `ui/src/components/UseTemplateOverlay.jsx` | `UseTemplateOverlay` and `GET /api/templates/{id}/composition` used the form's `approved` state (`LayerFiling`) for interactive templates, causing `"What this files"` (`CompositionPanel`) to display `"Approval: waits for approval — chosen here"` when `approved` was `false` (and `"chosen here"` when `true`), contradicting the interactive live-chat note and `Client.CreateTask`. Fixed so interactive templates always preview and submit `approved: true` with `source: "template"` (`"from the template"`). | `TestInteractiveTemplateCompositionReportsApprovedFromTemplate` (`pkg/ui/interactive_template_test.go`), `InteractiveTemplate.test.jsx` (`ui/src/components/InteractiveTemplate.test.jsx`), `processes-walkthrough.spec.js` (`ui/e2e/processes-walkthrough.spec.js`) |
| 2 | `pkg/orchestrator/finish.go` (`relayProposedTasks`) | `relayProposedTasks` did not copy `comp.Interactive` onto `proposal.Interactive` when replaying an end-of-run `propose_task` tool call naming an interactive `template_id`. Fixed to set `Interactive: interactive` from `comp.Interactive` while keeping the proposal unapproved. | `TestRelayProposedTasksCarriesInteractiveFromTemplate` (`pkg/orchestrator/requester_internal_test.go`) |
