# 2026-11-01: Settings walkthrough (layout, save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 2089 (`grain/049f1e09/task-2089`) is a browser walkthrough of the
Settings view, in the same scope as the 2026-10-29 and 2026-10-30 runs
(`2026-10-30-user-system-settings.md`). It ran against real `grain demo`
servers with their built frontend, and Playwright drove them through
visible controls. Connections and Infrastructure, System, and Settings →
Repositories belong to other walkthroughs.

Since 10-30 the Settings layout has changed (3d26e211, 17fcb581):

- The nav groups are now Models & sandboxes, Secrets & access, This
  browser, System / Operations, and Managed in config repo.
- A bare `/settings` opens **Models** on desktop, not General as this
  task's brief says. That is by design: `DEFAULT_SETTINGS_SECTION` is
  `models`, so the brief is out of date. The stale "General elsewhere"
  comment in `SettingsPage.jsx` is corrected on this branch.
- General has a new card, Settings lock ("Lock settings in UI"), which
  this run also exercised.

## Commit and environment

- **Commit tested**: `a71723af` (`main`, "Merge pull request #336 from
  ai-on-gke/grain/049f1e09/task-2086"). The fix below was checked again in
  the browser against a server rebuilt from this branch.
- **Build**: `npm ci && npm run build` in `ui/`, then `go build -tags
  grain_e2e -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`.
- **Servers**, each `grain demo` over its own throwaway embedded store
  with `-access-code-file`:
  - `:18080`: plain demo. Layout, filter, save model, Preferences,
    Access, General, Agents, Tasks, Memory.
  - `:18081`: `-model-catalog 127.0.0.1:18099
    -model-discovery-interval 5s -agent-paused`. Models.
  - `:18082`: `-external-config-repo
    https://github.com/acme/grain-config.git`. Config repo in external
    mode.
- **Browser**: Playwright (`ui/node_modules`) driving Chromium from
  `/var/cache/playwright`, signing in by typing the access code on the
  "Enter access code" page.
  - Widths: 1280×860 (1280×600 for the `#card` check), 800×1000 tablet
    and 390×844 phone.
  - Time zone: UTC, or America/Los_Angeles for the Preferences time-zone
    check.
- **Toolchain**: Debian, Go 1.26.10, Node v20.19.0.
- **Gemini key**: `/home/debian/.gemini-api-key` is absent from this
  sandbox, so no real model call was made.
- **Scripts**: ad hoc Node/Playwright files outside the repository
  (`/var/tmp/walk/p1…p6.mjs`), not committed. The regression is pinned
  by the unit test named under "Bug found and fixed".

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| Model provider (model listing) | A loopback Node server on `127.0.0.1:18099` answered `GET /v1/models` with `claude-walk-discovered-1` and `-2`. The `:18081` demo used it through `-model-catalog`. | Discovery needs a provider, and there is no key here. |
| `PUT /api/settings` | `page.route` answered the **first** PUT with `503 stubbed outage`, then let every later one through. | To raise "Couldn't save <field>" with Retry deterministically. Retry then went to the real server. |
| `GET /api/settings` (restart chip only) | `page.route` fetched the real response and added `pendingRestart: ["githubHost"]`. | A demo has no running configuration to differ from. The 10-30 run made a real pending restart on a daemon. |
| Config repo | **Not stubbed.** The demo's own `-external-config-repo` mode was used; that URL is never fetched in the demo. | — |
| Test Connection, GitHub, upgrade | **Not stubbed.** The demo answers "Connection valid" itself, and nothing here reaches GitHub or the upgrade service. The `404 GET /api/repos/acme/widgets/github-branches` on `/tasks/1` and the repository page is the demo lacking GitHub data, outside this area. | — |

## Steps and outcomes

### Layout, paths and filter (`:18080`)

| Check | Outcome |
|---|---|
| The header rail's gear (`aria-label="Settings"`) is the only Settings entry. It opens `/settings/models`. On a phone the gear is in the top bar. | Pass |
| Agents → Tasks in the nav, then `/settings/models` + reload, stays on Models. Back gives Tasks and Forward gives Models. | Pass |
| At 1280×600, `/settings/tasks#task-fields` puts Task fields at top=129. | Pass |
| Old links: `/admin` → general, `/admin/models` → models, `/admin/workflow` → tasks, `/admin/upgrade` and `/system/upgrade` → upgrade, `/memory` → memory, `/admin/state` → config-repo. | Pass |
| At 800 px the nav is a "Section" combobox. Choosing Models navigates there, and `scrollWidth` is 800. | Pass |
| Phone: `/settings` is the grouped list. Agents opens alone under "All settings" (`scrollWidth` 390), which returns to the list. | Pass |
| `/` focuses "Filter settings". "turns" lists *Max agent turns*. Choosing it goes to `/settings/agents#maxAgentTurns`, focuses the box, and outlines it with one `.setting-highlight`. | Pass |
| Matches by section: auto-merge → Tasks, time zone → Preferences, theme → appearance (Preferences), reasoning → default model (Models), deployment id → Agents, lock → Lock settings in UI (General). | Pass |
| "zzqqxx" says "No setting matches". Escape clears the box and restores the groups. | Pass |

### Save model (`:18080`)

| Check | Outcome |
|---|---|
| Checkbox (Show closed tasks by default) | One PUT `{"showClosedByDefault":true}` on click. It read back after a reload and was restored. Pass. |
| Text (Environment name) | Nothing was sent while typing, and one PUT on Enter. It read back, and the tab title became "walk-env — grain". Clearing it saved on blur. Pass. |
| Select (After an upgrade → Check the upgrade) | One PUT `{"upgradeCheckMode":"approve"}` on change, which read back after a reload. Restored to the original `auto`. Pass. |
| About | "v1.2.0 · a71723a · 2026-11-01 03:38Z", identical to the build stamp at the foot of the rail. Pass. |
| Out of range | `-5`: "Couldn't save Max agent turns" and "Max agent turns must be at least 0", with **no PUT**. Putting `0` back cleared both. `2.5` was refused the same way. Pass. |
| No upper bound | `99999999999999` saved (also observed 10-30). |
| Couldn't save + Retry | With the stubbed 503: "Couldn't save Max agent turns", the box rolled back to 0, and a Retry button appeared. Retry went to the real server, and 12 read back after a reload. Pass. |
| Server read-back (Engine → Poll interval) | `1m` + Enter read back as the server's `1m0s`, also after a reload. Restored to `10s`. Pass. |
| Save card (Agents → Standing instructions) | Typing and blurring sent nothing. The header read "Unsaved changes in Standing instructions", with the nav's dirty dot. Changing section and clicking the rail's Inbox each asked "You have unsaved changes here. Leave and discard them?", and dismissing stayed put. A reload and `goto` raised `beforeunload`. Discard restored the text. Save sent one PUT `{"promptExtension":…}`, which read back. Once clean, moving to Tasks and then Inbox asked nothing. Pass. |
| Restart needed chip (stubbed `pendingRestart`) | Shown on General. Focus + Enter opens `/system/power`. Pass. |

### Preferences (`/settings/preferences`, `:18080`, browser in America/Los_Angeles)

- **Appearance**: Dark and Light set `html[data-theme]`. Dark survived a
  reload. Auto followed the browser's light scheme. Pass.
- **Time zone**: with none chosen, the first option reads "Browser
  default (America/Los_Angeles)", and `/tasks/1` printed "10/31/2026,
  4:40:10 PM". With Asia/Tokyo it printed "11/1/2026, 8:40:10 AM", 16
  hours later as it should be. The choice survived a reload and was put
  back to the browser default. Pass.
- **Task timeline**: Oldest first survived a reload. Restored to Newest
  first. Pass.
- **Customize sidebar**:
  - Pinned, through Type/Item → Pin item: a Settings section (Memory), a
    System tab (Logs), a board and a session ("director").
  - Move up put the session above the board, then Save.
  - The rail read "inbox / memory / logs / director / board", the same
    after a reload.
  - The pinned Memory opened `/settings/memory`, and Logs opened
    `/system/logs`.
  - Unpinning them and reloading restored the rail. Pass.
- **Nothing deployment-wide**: no non-GET request was sent during any
  Preferences or pin step. Pass.

### Access (`:18080`)

The This browser card says "Signed in with the access code." Log out
returned to the access-code prompt (401 on reload), and a deep link to
`/settings/general` afterwards showed the prompt too. Pass.

### General

- **Deployment**: covered by the checkbox and text cases above.
- **Settings lock**: an accidental click in the walkthrough sent `PUT
  {"settingsLocked":true}` on change, as a checkbox should. It was
  unticked again in the browser and confirmed `false` over the API.
- **Updates**: the select case above. "Hold the queue until the check is
  done" and Automatic upgrades (channel, time of day) are present.
- **About**: matches the build stamp (above).

### Config repo

- **Plain demo (`:18080`)**: "This UI is not running inside a daemon
  that owns a config repository, so there is nothing to configure here."
  Settings stay editable, and a `PUT /api/settings` is applied.
- **External (`:18082`)**: the panel reads "External repository
  (Infrastructure as Code) … reads configuration directly from
  https://github.com/acme/grain-config.git on branch main, never exports
  …, and UI settings are read-only", with chips acme/grain-config, the
  URL and main, and Pull now.
  - Agents' Max agent turns is disabled, under "read-only and managed in
    https://github.com/acme/grain-config.git (main). Switch to the
    internal config repository in Config Repo settings to edit settings
    in the UI."
  - A write (`PUT /api/settings {"maxAgentTurns":5}`, from the signed-in
    page) is refused 409 with the exact `tables/grain_config.json` patch
    to commit there instead. Pass.
- **Not re-run this time**: the failing-sync alert and Inbox row, and
  switching between internal and external. Each needs a real daemon with
  a state repository, which this run did not start for time. Both were
  exercised end to end on 10-30, where Bugs 1 and 2 were fixed and
  pinned by `TestAFailedSyncNowSaysWhy`,
  `TestStateSyncFailureSaysWhenTheRepositoryIsExternal` and
  `InboxPage.stateSync.test.jsx`.

### Models (`:18081`, `-agent-paused`)

- **Paused default**: the only connection, "gemini 2.5 pro"
  (antigravity), reads "paused until 9:40:10 AM … usage limit reached"
  with Resume.
  - Before the fix, the Models pane said **nothing** about the default
    model's state (**bug**, below).
  - After the fix it says "Every enabled connection that serves model
    gemini-2.5-pro is paused. Tasks on the default model wait in the
    queue until a pause ends or is resumed above."
  - Resume cleared the pause and the warning, and the pause stayed
    lifted after a reload. Pass.
- **Test Connection**: "Connection valid". Pass.
- **Disable / Enable**: Disable showed "No enabled connection serves
  model gemini-2.5-pro …", and Enable cleared it. Enabling also lifted
  the connection's usage-limit pause. That is by design
  (`pkg/ui/connection_pause.go`: editing a connection, re-enabling
  included, lifts its own pauses).
- **New connection, unfinished**: agent type Claude → Continue → ←
  Models. There was no Remove prompt and no draft row after a reload.
  Pass.
- **New connection, finished**: the agent types are antigravity, claude,
  codex and custom command. Claude → authentication method "Claude Code
  OAuth token" (others: Google Cloud Vertex AI) → dummy token. The form
  listed **claude-walk-discovered-1 / -2** from the stub, and the
  connection saved. Pass.
- **Pickers fed by discovered models**: New session's and New template's
  Model both offered "Deployment default (gemini-2.5-pro) |
  gemini-2.5-pro | gemini-2.5-flash | claude-walk-discovered-1 |
  claude-walk-discovered-2". Pass.
  - New task's offered the same list on 10-30. On this run's second pass
    the connection had already been removed, so it was not re-observed.
  - Schedules' picker was not reached: the script looked for New
    schedule on the wrong page. It is the shared `ModelPicker`, covered
    by `model-picker-search.spec.js`.
- **Default model**: choosing `claude-walk-discovered-2` sent `PUT
  /api/deployment {"defaultModel":…}` and read back after a reload.
  Typing `no-such-model-xyz` + Enter sent nothing. Restored to
  gemini-2.5-pro. Pass.
- **Reasoning effort**: the options are Model default / low / medium /
  high. *high* on gemini-2.5-pro sent `PUT /api/deployment
  {"modelSettings":[…"effort":"high"]}` and read back after a reload,
  then was restored to Model default. Pass.
- **Remove**: asked "Remove the connection backend-…? A credential
  stored for it is deleted with it …". Dismissing kept it; accepting
  removed it, which a reload confirmed. Pass.
- **Where each agent CLI runs**: claude is host (not found on PATH), with
  the "set to container, but a daemon on the bare machine has no
  container …" note. codex is host (not found on PATH). agy is host
  (`/opt/agy/bin/agy`), set by -agy-run. Custom commands is host. Pass.

### Agents (`:18080`)

- **Max agent turns** and **Standing instructions**: see Save model.
- **Branch name template**: refused, each with "Couldn't save Branch name
  template": `grain/no-id`, `grain/{slug}-{id}`, `bad branch {id}`,
  `grain/task-{id}.lock`, and a 256-character name.
  `walk/{deployment}/t-{id}` saved, the refusal's banner cleared, and it
  read back. Restored to empty. Pass.
- **Deployment ID**: `Bad ID!` was refused, and `walk-dep` saved and was
  cleared again. Pass.
- **Commit identity**: still a prose pointer to "Settings → Git" with no
  link (Observation, as on 10-30).

### Memory (`/settings/memory`, `:18080`)

- Shows this deployment's memory, with its documents and the
  per-repository scopes.
- **A reversible correction**: appending "WALK correction 2089." gave
  "Unsaved changes in Memory".
  - The rail asked first.
  - A reload and Back each raised `beforeunload`.
  - Save sent `PUT /api/memory`, and reopening through Inbox → Settings →
    Memory read it back.
  - Saving the original text restored it, which a reload confirmed.
    Pass.
- Memory is not in the rail unless pinned (the pinned case is above).
  `/repos/acme/widgets` does not mention memory. Pass.

### Tasks (`:18080`)

- Turned off "Queue new tasks immediately by default" and "Auto-merge new
  tasks by default".
- `-1` in Max unreviewed follow-up depth was refused by name. 3 saved and
  the error cleared.
- After a reload all three read back.
- **+ New task** opened with queue immediately and auto-merge both
  unchecked.
- Everything was restored (depth back to the default 2). Pass.
- **Task fields**: the card lists the declared fields and says they are
  declared in `tables/custom_field.json`. The "no longer offered" note
  needs a value outside the declarations, which the demo does not hold
  (not exercised, as on 10-30).

### Console and network

There were no console errors or failed requests beyond the expected
ones:

- the 401 before signing in and after Log out;
- the 400s from refused values;
- the stubbed 503;
- the demo's `github-branches` 404 described above;
- `net::ERR_ABORTED` from navigations cancelled at a `beforeunload`
  prompt.

## Bug found and fixed

**Settings → Models said nothing when the default model's only
connections were paused.**

- Repro: `grain demo -agent-paused`, then open Settings → Models.
- Before: the connection row says "paused until …", but the Models pane
  under it showed no warning. `ModelDefaults` warned only when no
  *enabled* connection served the default, and a paused connection is
  still enabled. Tasks on the default model wait in the queue all the
  same.
- Fix: `ModelDefaults.jsx` also warns when every enabled connection
  serving a default model is paused, using the `connectionPause` it
  already uses for the "Served by" chips: "Every enabled connection that
  serves model <m> is paused. Tasks on the default model wait in the
  queue until a pause ends or is resumed above." The disabled-only
  warning is unchanged and takes precedence.
- Test: `ModelDefaults.test.jsx` "warns of a default model only paused
  connections serve". It warns with one paused connection, and stops
  warning once a second, unpaused connection serves the model.
- Checked again in the browser against a rebuilt `:18081`: the warning
  shows, and Resume clears it.

## Observations (not changed)

- The brief's "bare `/settings` opens General on desktop" is out of
  date: it opens Models (`DEFAULT_SETTINGS_SECTION`).
- Max agent turns still has no upper bound.
- Commit identity still names "Settings → Git" in prose, without a link.
