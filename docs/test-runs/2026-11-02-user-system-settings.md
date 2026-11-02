# 2026-11-02: Settings walkthrough (layout, save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 2404 is a browser walkthrough of the Settings view, in the same
scope as the 2026-11-01 runs (`2026-11-01-user-system-settings.md`,
tasks 2089 and 2237). It ran against real `grain demo` servers and one
real `grain daemon`, each with its built frontend, and Playwright drove
them through visible controls. Connections and Infrastructure, System
and Settings → Repositories belong to other walkthroughs.

**Result:** every check in the brief passed. No bug was found, so this
branch changes nothing but this report. Since 11-01 this run also
covered two things no earlier run reached:

- the Task fields note on values a field no longer offers, made for
  real through the config repository;
- resuming a pause on a single model, not only on a whole connection.

## Commit and environment

- **Commit tested**: `aaa266ef` (`main`, "Merge pull request #462 from
  ai-on-gke/grain/049f1e09/task-2395-docs-design-move-contributions").
- **Build**: `npm ci && npm run build` in `ui/`, then `go build -tags
  grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0"
  ./cmd/grain`. The binary embeds the built UI.
- **Servers**, each over its own throwaway store:
  - `:18080`: `grain demo -access-code-file …`. Layout, filter, save
    model, Preferences, Access, General, Agents, Tasks, Memory.
  - `:18081`: `grain demo … -model-catalog 127.0.0.1:18099
    -model-discovery-interval 5s -agent-paused`. Models.
  - `:18082`: `grain demo … -external-config-repo
    https://github.com/acme/grain-config.git`. External mode on a demo.
  - `:18090`: a real `grain daemon`, set up by `grain init workstation
    -non-interactive -repo acme/widgets -sandbox-backend host
    -model-backend claude -ui-addr 127.0.0.1:18090 -start-service=false`
    under a throwaway `HOME`. It was started from the `daemon.sh` that
    init wrote, with `-sandbox-port-base=0 -github-host 127.0.0.1:1
    -github-insecure-http`. It ran the Config repo checks (internal mode,
    switching to external and back, the failing sync) and Task fields.
    The daemon ignores `-access-code-file`, so the browser signed in with
    its `local-api-token`.
- **Browser**: Playwright from `ui/node_modules`, driving Chromium from
  `/var/cache/playwright`. It signed in by typing the access code on the
  "Enter access code" page.
  - Widths: 1280×860 (1280×600 for the `#card` check), 800×1000 tablet
    and 390×844 phone.
  - Time zone: UTC, or America/Los_Angeles for the Preferences checks.
- **Toolchain**: Debian, Go 1.26.10, Node v20.19.0.
- **Gemini key**: `/home/debian/.gemini-api-key` is absent from this
  sandbox, so no real model call was made.
- **Scripts**: ad hoc Node/Playwright files in `/var/tmp/walk`
  (`p1…p27.mjs`), outside the repository and not committed.

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| Model provider (model listing) | A loopback Node server on `127.0.0.1:18099` answered `GET /v1/models` with `claude-walk-discovered-1` and `-2`. The `:18081` demo used it through `-model-catalog`; it served 50 listings during the run. | Discovery needs a provider, and there is no key here. |
| `PUT /api/settings` (Retry only) | `page.route` answered the **first** PUT with `503 stubbed outage` and let every later one through. | To raise "Couldn't save <field>" with Retry deterministically. Retry then went to the real server. |
| `GET /api/settings` (restart chip only) | `page.route` fetched the real response and added `pendingRestart: ["environmentName","githubHost"]`. | A demo has no running configuration to differ from. |
| `GET /api/config` and `GET /api/pause` (model pause only) | `page.route` fetched the real response and added `agentPause` with one entry for connection `gemini-pro`, model `gemini-2.5-flash`. The `DELETE /api/pause?…&model=gemini-2.5-flash` that Resume sent went to the real server, and the stub stopped adding the entry once it saw that request. | `-agent-paused` makes only a connection-wide pause. A model-scoped one needs a provider refusing one model. |
| Config repo | **Not stubbed.** The external repository was a local bare repository, `file:///var/tmp/walk/external.git`, cloned from the daemon's own `grain/config`. To make the sync fail it was renamed away; renaming it back restored it. | — |
| Test Connection, GitHub, upgrade | **Not stubbed.** The demo answers "Connection valid" itself, and nothing here reaches GitHub or the upgrade service. | — |

## Steps and outcomes

### Layout, paths and filter (`:18080`)

| Check | Outcome |
|---|---|
| The header rail's gear (a button named "Settings") is the only Settings entry, and it opens `/settings/models`. On a phone the gear is in the top bar. | Pass |
| Agents → Tasks in the nav, then a reload, stays on Tasks. Back gives Models, and Forward gives Tasks. | Pass |
| At 1280×600, `/settings/tasks#task-fields` puts Task fields at top=129. At 390 px it puts the card at top=250, under the bar and the section header, with `scrollWidth` 390. | Pass |
| Old links: `/admin` → general, `/admin/models` → models, `/admin/workflow` → tasks, `/admin/upgrade` and `/system/upgrade` → upgrade, `/memory` → memory, `/admin/state` → config-repo, `/admin/executors` → models. | Pass |
| At 800 px the nav is a "Section" combobox. Choosing Models goes there, `scrollWidth` is 800, and ArrowDown opens the list from the keyboard. | Pass |
| On a phone, `/settings` is the grouped list. Agents opens alone under an "All settings" back button (`scrollWidth` 390), which returns to the list. | Pass |
| `/` focuses "Filter settings". "turns" lists *Max agent turns* under Agents. Choosing it goes to `/settings/agents#maxAgentTurns`, focuses the box, and outlines it with one `.setting-highlight`. | Pass |
| From the keyboard, "auto-merge", then ArrowDown and Enter, goes to `/settings/tasks#autoMergeByDefault` and focuses that checkbox. | Pass |
| Matches by section: auto-merge → Tasks, time zone → Preferences, theme → appearance (Preferences), reasoning → default model (Models), deployment id → Agents, lock → Lock settings in UI (General). | Pass |
| "zzqqxx" says "No setting matches 'zzqqxx'." Escape restores the groups. | Pass |
| The nav entries are reachable with Tab, in order, and open on Enter or Space. | Pass |

### Save model (`:18080`)

| Check | Outcome |
|---|---|
| Checkbox (Show closed tasks by default) | One PUT `{"showClosedByDefault":true}` on click. It read back after a reload and was restored. Pass. |
| Text (Environment name) | Nothing was sent while typing, and one PUT on Enter. The tab title became "walk-env — grain", and the name read back after a reload. Clearing it saved on blur, and the title went back to "grain". Pass. |
| Select (After an upgrade → Check the upgrade) | One PUT `{"upgradeCheckMode":"approve"}` on change, which read back after a reload. The options are don't / file a proposal to approve / file it and queue it (default). It was restored to `auto`. Pass. |
| About | "v1.2.0 · aaa266e · 2026-11-02 08:30Z", identical to the build stamp at the foot of the rail. Pass. |
| Out of range | `-5` showed "Couldn't save Max agent turns" and "Max agent turns must be at least 0; -5 was not saved." with **no PUT**. The box went back to its stored 0, and saving 0 cleared both messages. `2.5` was refused the same way. Max unreviewed follow-up depth `-1` gave "… must be at least 1; -1 was not saved." Pass. |
| Couldn't save + Retry | With the stubbed 503: "Couldn't save Max agent turns", the box rolled back to 0, and a Retry button appeared. Retry sent the same PUT to the real server, the header said "All changes saved", and 12 read back after a reload. Pass. |
| Server read-back (Engine → Poll interval) | `1m` + Enter read back as the server's `1m0s`, also after a reload. The brief's "60s" is the same duration in another spelling. Restored to `10s`. Pass. |
| Save card (Agents → Standing instructions) | Typing and blurring sent nothing. The header read "Unsaved changes in Standing instructions", and the nav entry carried the dirty dot. Changing section and clicking the rail's Inbox each asked "You have unsaved changes here. Leave and discard them?", and dismissing stayed put. A reload raised `beforeunload`. Discard restored the text. Save sent one PUT `{"promptExtension":…}`, which read back after a reload. Once clean, Tasks and then Inbox asked nothing. Pass. |
| Restart needed chip (stubbed `pendingRestart`) | Shown in the section header on General, Agents and Tasks. Click, or focus + Enter (it is a `role=button` chip), opens `/system/power`. Pass. |

### Preferences (`/settings/preferences`, `:18080`, browser in America/Los_Angeles)

- **Appearance**: Dark and Light set `html[data-theme]`, and Dark
  survived a reload. Auto followed the browser's light scheme. Pass.
- **Time zone**: with none chosen, the first option reads "Browser
  default (America/Los_Angeles)", the list marks "America/Los_Angeles
  (this browser)", and `/tasks/1` printed "11/1/2026, 8:52:38 PM".
  - With Asia/Tokyo it printed "11/2/2026, 1:52:38 PM", 17 hours later as
    it should be.
  - The choice survived a reload, and was put back to the browser
    default.
  - With no zone chosen, the build stamp's tooltip printed its 08:30Z
    commit time in the browser's zone ("11/2/2026, 12:30:14 AM"). It was
    not re-read under Tokyo. System → Logs prints raw server log lines
    (`08:32:38+0000 …`), which are the log's own text, not a time the
    page formats. Pass.
- **Task timeline**: Oldest first survived a reload. Restored to Newest
  first. Pass.
- **Customize sidebar**:
  - Pinned, through Type → Item → Pin item: a Settings section
    ("settings → memory"), a System tab ("system → logs"), a board
    ("board") and a session ("director").
  - Move up put the session above the board, then Save.
  - The rail read "inbox / memory / logs / director / board", the same
    after a reload.
  - The pinned Memory opened `/settings/memory`, and Logs opened
    `/system/logs`.
  - Unpinning all four and reloading restored the rail to Inbox alone.
    Pass.
- **Nothing deployment-wide**: no non-GET request was sent during any
  Preferences or pin step. The choices are in `localStorage`
  (`grain.pinned` and the rest). Pass.

### Access (`:18080`)

The This browser card says "Signed in with the access code." Log out
sent `POST /api/auth/logout` and returned to the "Enter access code"
prompt. A deep link to `/settings/general` afterwards showed the prompt
too. Pass.

### General (`:18080`)

- **Deployment**: covered by the checkbox and text cases above.
- **Updates**: covered by the select case above. "Hold the queue until
  the check is done" and Automatic upgrades (channel, time of day) are
  present.
- **About**: matches the build stamp (above).

### Config repo (`:18082` demo and `:18090` daemon)

- **Internal (`:18090`)**: the panel reads "Internal (grain/config on
  this host — default) …", with Export now. A UI write is applied: Max
  agent turns `4` read back after a reload, and was restored to 0.
- **Switch to external**: Repository URL
  `file:///var/tmp/walk/external.git` → Use external config repository
  sent `POST /api/state-repo/remote`.
  - The panel reads "External repository (Infrastructure as Code) … never
    exports or pushes daemon-generated commits to it, and UI settings are
    read-only".
  - Agents' Max agent turns is disabled under "Configuration is
    read-only and managed in file:///var/tmp/walk/external.git (main).
    Switch to the internal config repository in Config Repo settings to
    edit settings in the UI."
- **Every managed control inert**: in external mode, every control on
  General, Tasks, Agents and Models matches `:disabled`, through the
  card's disabled fieldset. On the `:18082` demo, `PUT /api/deployment`
  (reasoning effort), `PUT /api/settings/cli-run` and `PUT /api/settings`
  were each refused 409 by the server too, so the UI and the API agree.
- **Write refused**: `PUT /api/settings {"maxAgentTurns":5}` from the
  signed-in page answered 409 `config_managed_externally`, naming the
  repository and `tables/grain_config.json`. It says "Make this change in
  tables/grain_config.json instead … Open a pull request there; grain
  applies it on its next sync.", with the patch to commit.
- **Failing sync**:
  - With the remote renamed away, Pull now answered "the state
    repository's remote could not be reached: fetching main: … fatal:
    '/var/tmp/walk/external.git' does not appear to be a git repository
    …". It showed in the panel's status alert and in a snackbar.
  - After the five-minute grace, the Inbox had a "configuration is not
    being pulled" row: "Changes merged in walk/external since then are
    not applied here until the sync is fixed." The Alerts menu carried
    the same alert.
  - The row's Config repo button opened `/settings/config-repo`.
- **Recovery**: with the remote back, plus one new commit declaring a
  Task field, Pull now succeeded. The panel's alert went, the head chip
  read the new commit `70de3a6e`, the Inbox row went, and the field
  appeared under Tasks → Task fields (below).
- **Back to internal**: "Switch to internal config repo" (`POST
  /api/state-repo/remote {"none":true}`) returned the panel to Internal,
  and Max agent turns was editable again.

### Models (`:18081`, `-agent-paused`)

- **Paused default**: the only connection, "gemini 2.5 pro"
  (antigravity), reads "paused until 2:52:38 PM (about 5h 39m) —
  antigravity: usage limit reached …" with Resume. The Models pane
  warns: "Every enabled connection that serves model gemini-2.5-pro is
  paused. Tasks on the default model wait in the queue until a pause
  ends or is resumed above." Resume sent `DELETE
  /api/pause?connection=gemini-pro`, the warning went, and the pause
  stayed lifted after a reload. Pass.
- **Paused model** (stubbed, above): the connection's row reads
  "Advertises: gemini-2.5-pro, gemini-2.5-flash (paused)" and "paused
  until … — stubbed: model quota reached" with Resume. Resume sent
  `DELETE /api/pause?connection=gemini-pro&model=gemini-2.5-flash`, and
  the pause was gone after a reload. Pass.
- **Test Connection**: "Connection valid". Pass.
- **Disable / Enable**: Disable sent the connection with
  `"disabled":true`, and the pane warned "No enabled connection serves
  model gemini-2.5-pro. Tasks on the default model wait in the queue
  until one does." That survived a reload, and Enable cleared it. Pass.
- **Edit** opens the connection's form (agent type, authentication,
  name, concurrency, CLI path, discovered and advertised models) in the
  overlay. "← Models" closes it.
- **New connection, unfinished**:
  - The agent types are antigravity, claude, codex and custom command.
  - Claude → Continue stores a disabled draft.
  - "← Models" asks the general "You have unsaved changes here. Leave and
    discard them?", not a Remove prompt. Accepting sent `DELETE` for the
    draft, and it was gone.
  - Pass.
- **New connection, finished**:
  - Claude → "Claude Code OAuth token". The other methods were "Google
    Cloud Vertex AI", plus "Reuse authentication from <id>" for a draft
    left behind (see Observations).
  - Set credential answered 404 on this demo, which has no secrets store
    (as on 11-01).
  - The form listed claude-walk-discovered-1 and -2 under both
    Discovered and Advertised models.
  - "Walk Claude" saved. After a reload it reads "Advertises:
    claude-walk-discovered-1, claude-walk-discovered-2 (every model its
    provider lists)", and the model list gained both.
  - Pass.
- **Pickers fed by discovered models**: New task, New session
  (`/session/new`), New schedule and New template (Processes → + New)
  each offered "Deployment default (gemini-2.5-pro) | gemini-2.5-pro |
  gemini-2.5-flash | claude-walk-discovered-1 |
  claude-walk-discovered-2". Pass.
- **Default model**:
  - Choosing claude-walk-discovered-2 sent `PUT /api/deployment
    {"defaultModel":…}` and read back after a reload.
  - Typing `no-such-model-xyz` + Enter sent nothing, and the default
    stayed.
  - A default no connection advertises (`retired-model-9`, set over the
    API as a stand-in for a retired model) reads "retired-model-9 (no
    connection advertises it)". It warned "No enabled connection serves
    model retired-model-9 …", and its row said "No connection advertises
    this model".
  - Restored to gemini-2.5-pro.
  - Pass.
- **Reasoning effort**: the options are model default / low / medium /
  high. *high* sent `{"modelSettings":[{"model":"gemini-2.5-pro","effort":"high"}]}`
  and read back after a reload. It was restored to Model default
  (`{"modelSettings":[]}`). Pass.
- **Remove**: asked "Remove the connection Walk Claude? A credential
  stored for it is deleted with it and cannot be read back, so adding it
  again means pasting that credential again." Dismissing kept it and
  sent nothing. Accepting removed it, which a reload confirmed. Pass.
- **No connections**: with the last one removed, the pane said "This
  deployment has no provider connection, so its tasks wait. Create one
  to authenticate agent models." The default read "gemini-2.5-pro (no
  connection advertises it)". The demo's connection was then put back
  over the API. Pass.
- **Where each agent CLI runs**:
  - claude: "container set by -claude-run, but a daemon on the bare
    machine has no container of its own …", and it runs on the host.
  - codex: host (not found on PATH).
  - agy: host (`/opt/agy/bin/agy`), set by -agy-run.
  - Custom commands: host (each connection's own command).
  - The choices offered are default and host. Host → Save run locations
    (`PUT /api/settings/cli-run`) read back as Host after a reload.
    Default → Save put the flag's setting back.
  - Pass.

### Agents (`:18080`)

- **Max agent turns** and **Standing instructions**: see Save model.
- **Branch name template**: refused, each with "Couldn't save Branch
  name template": `grain/no-id`, `grain/{slug}-{id}`, `bad branch {id}`,
  `grain/task-{id}.lock` and `grain/{id}-{slug}-x`.
  `walk/{deployment}/t-{id}` saved, the refusal cleared, and it read
  back after a reload. Restored to empty. Pass.
- **Deployment ID**: `Bad ID!` was refused, and `walk-dep` saved, read
  back, and was cleared again. Pass.
- **Commit identity**: the pointer's "Settings → Git" link opens
  `/settings/git`. Pass.

### Memory (`/settings/memory`, `:18080`)

- It shows this deployment's memory with its document
  (release-checklist), and acme/widgets' memory with its document
  (flaky-e2e), each saying who last wrote it. It also offers "another
  repo" to start a memory for.
- **A reversible correction**: appending "WALK correction 2404." gave
  "Unsaved changes in Memory", and nothing was sent.
  - The rail's Inbox, Back and a section change each asked "You have
    unsaved changes here. Leave and discard them?", and dismissing kept
    the edit.
  - A reload raised `beforeunload`.
  - Save sent `PUT /api/memory`. Reopening through Inbox → Settings →
    Memory read it back, now "written today · edited here".
  - Revert put back the saved text. Saving the original text restored
    it, which a reload confirmed.
  - Pass.
- **Delete a document** asks "Delete the memory document
  "release-checklist"? Nothing else keeps a copy of it." Dismissing kept
  it, which a reload confirmed. Pass.
- Memory is not in the rail unless pinned (the pinned case is above).
  `/repos/acme/widgets` does not mention memory. Pass.

### Tasks (`:18080`, and `:18090` for Task fields)

- Turned off "Queue new tasks immediately by default" and "Auto-merge
  new tasks by default". Each sent one PUT on click.
- Removed "gcp key" from Default legacy capabilities, which sent `PUT
  {"defaultCapabilities":["self-debug","gemini-key"]}`.
- `-1` in Max unreviewed follow-up depth was refused by name. 3 saved,
  and the error cleared.
- After a reload all four read back.
- **+ New task** then opened with "Auto-merge once checks pass" and
  "Queue immediately" both unchecked, and only self debug and gemini key
  attached. Once the defaults were restored it opened with both checked
  and all three capabilities. Pass.
- **Task fields, the "no longer offers" note** (`:18090`, through the
  external config repo):
  1. A commit to `tables/custom_field.json` declared *Stage*, an enum of
     alpha, beta and ga. After Pull now, Task fields listed "Stage stage
     · enum: alpha, beta, ga".
  2. + New task offered Stage (none / alpha / beta / ga). A task filed
     with ga stored `{"stage":"ga"}`.
  3. A second commit withdrew ga. After Pull now the card read "Stage
     stage · enum: alpha, beta" and "1 task still carries a value this
     field no longer offers".
  4. That note is a button. It opened the task list filtered to
     `custom.stage:legacy`, showing "stage is a legacy value" and the
     task.

  Pass. The demo's server refuses an undeclared value on filing
  ("custom field "Area": "mobile" is not one of frontend, backend,
  infra, docs"), so a changed declaration is the only way to get one.

### Console and network

There were no console errors or failed requests beyond the expected
ones:

- the 401s before signing in and after Log out;
- the 400s from refused values;
- the stubbed 503;
- the 409s from external-mode writes;
- the 502 from Pull now with the remote gone;
- the demo's `GET /api/repos/acme/widgets/github-branches` 404 on New
  task (the demo has no GitHub data);
- the demo's credential 404 (no secrets store);
- `net::ERR_ABORTED` on `GET /api/tasks` polls cut off by a navigation,
  and on connection `DELETE`s whose 204 the browser had already received
  (as traced on 11-01).

## Observations (not changed)

- **A draft can be left behind when its browser dies.** A new
  connection's draft is cleaned up when its page closes or is left
  normally. A script that closed the browser process while the form was
  open left nameless disabled drafts behind, as on 11-01. They are named
  by id in the list and in "Reuse authentication from", Remove clears
  them, and nothing dispatches to a disabled connection.
- **The connection form is not deep-linkable.** Edit and New connection
  open in an overlay while the URL stays `/settings/models`, so a reload
  leaves the form.
- **A failed Pull now says the same thing twice:** in the panel's sync
  status and in a snackbar.
- **Max agent turns still has no upper bound** (noted since 10-30).
- **The brief is out of date in four places:**
  - a bare `/settings` opens Models (`DEFAULT_SETTINGS_SECTION`), not
    General;
  - the gear is a button, not a link;
  - "ignore GitHub CI" is per repository now (Tasks says so and points
    to Settings → Repositories);
  - the server writes "1m" back as `1m0s`, not "60s".
