# 2026-11-04: Settings walkthrough (layout, lock and save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 2963 is a browser walkthrough of the Settings view, with the same scope
as the 11-03 run (`2026-11-03-user-system-settings.md`, task 2702). It ran
against real `grain demo` servers and a real `grain daemon`, each serving its
built frontend, and Playwright drove them through the visible controls. Git,
Capabilities, Legacy capabilities, Engine, CI, Sandboxes, Caches, System,
Upgrade and Repositories belong to other walkthroughs. They are touched here
only where the lock model reaches them.

This run paid particular attention to the change since 11-03 most likely to
regress this area. `733b7d5f` moved Settings' pickers onto SearchSelect: the
Section select, Updates, Time zone, Default model, reasoning effort, run
locations and the connection form.

**Result:** no new bug was found in Settings, and every check below passed.

- **11-03's follow-up is fixed on `main`.** Signing in with the access code
  now returns to the deep link it started from.
- **Not a Settings fault:** `main` at `4460d761` does not compile (see
  *Unrelated*).

## Commit and environment

- **Commits tested:**
  - `a3bcafa9` (`main`, "Merge pull request #693 …"): layout, filter, save
    model, Preferences, Access, General, Agents, Memory, Tasks and Models.
  - `4460d761` (`main`, "Merge pull request #698 …"): the real daemon
    (config repo, lock, browser, held config PR), external mode, the
    failing-sync alert and Task fields.
  - Between the two, the diff in `ui/src` and `pkg/ui` touches nothing in
    Settings except a two-line change to `pkg/ui/accesscode.go`.
- **Local workaround for the daemon run:** `4460d761` does not build. A
  local, uncommitted edit removed the duplicate `"login"` case in
  `pkg/cli/main.go` (see *Unrelated*).
- **Build:** `npm ci && npm run build` in `ui/`. Playwright's
  `e2e/global-setup.js` then built the e2e `grain` binary, which embeds the
  UI. About reads `v1.2.0 · a3bcafa-dirty · 2026-11-04 09:15Z`.
- **Servers.** Each ran over its own throwaway store, on a port the kernel
  picked:
  - **Demo, default** (`e2e/fixtures.js` `demoServer`).
  - **Demo, `-agent-paused -model-catalog 127.0.0.1:18099
    -model-discovery-interval 5s`:** Models.
  - **Demo, `-external-config-repo
    https://github.com/acme/grain-config.git`:** external mode, the sync
    alert and Task fields.
  - **A real `grain daemon`** (`e2e/realdaemon.js` `startDaemon`, with
    `directConfigWrites: false`): its own internal `grain/config`, the lock
    and the held config pull request.
- **Browser:** Chromium from `/var/cache/playwright`.
  - Widths: 1280×860 (1280×600 for the `#card` check), 800×1000 for tablet
    and 390×844 for phone.
  - Time zone: UTC, except America/Los_Angeles for the Preferences file.
- **Toolchain:** Debian, Go 1.26.10, Node 20.
- **Gemini key:** `/home/debian/.gemini-api-key` is absent from this
  sandbox, so no real model call was made.
- **Scripts:** ad hoc spec files (`ui/e2e/zz-walk-*.spec.js`) running on the
  repository's own fixtures. They were deleted afterwards and are not
  committed, and no screenshot or trace is committed either.

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| Model provider (listing) | A loopback Node `http` server on `127.0.0.1:18099` answered `GET /v1/models` with `claude-walk-discovered-1` and `-2`, through the demo's `-model-catalog`. | Discovery needs a provider, and there is no key here. |
| `PUT /api/settings` (Retry only) | `page.route` answered the **first** PUT with `503 stubbed outage`, and let later ones through. | To raise "Couldn't save Max agent turns" with Retry deterministically. |
| `GET /api/settings` (restart chip only) | `page.route` added `pendingRestart: ["environmentName"]` to the real answer. | A demo has no running configuration to differ from. |
| `GET /api/config` (sync alert only) | `page.route` added `stateRepoSyncFailure` (`push rejected (stubbed)`, since 2026-11-04T09:00Z). | A demo has no config repository to fail. |
| `GET /api/config` (Task fields only) | `page.route` removed `medium` from the `priority` field's `options` (default `low`), as a config-repo commit withdrawing the value would. | Field definitions come from the config repo, and the demo's are fixed. |
| Config repo, Test Connection, GitHub, upgrade, identity | **Not stubbed.** External mode is the demo's own flag, internal mode is the daemon's real `grain/config`, and the demo answers Test Connection itself. Nothing reached GitHub or an upgrade service. A verified identity was not re-stubbed today (11-03 covered the badge). | — |

## Steps and outcomes

### Layout and paths (default demo)

- **Settings entry:** the gear opens `/settings/models`, and a bare
  `/settings` also opens Models on desktop. On a phone the bar carries the
  entry as `phone-settings`. Pass.
- **History:** Tasks in the section nav, then a reload, stays on
  `/settings/tasks`. Back returns to Models and Forward to Tasks. Pass.
- **`#card`:** at 1280×600, `/settings/tasks#task-fields` puts Task fields at
  top=129, just under the section header. Pass.
- **Tablet (800 px):** the nav is the "Section" SearchSelect. Choosing
  Agents opens `/settings/agents`, and `scrollWidth` is 800. Pass.
- **Phone (390 px):** a bare `/settings` stays on the grouped list. Agents
  opens alone (`scrollWidth` 390) under the "All settings" back arrow, which
  returns to `/settings`. Pass.
- **Old links:**
  - `/admin` → general, `/admin/workflow` → tasks.
  - `/admin/executors` and `/system/executors` → hostds.
  - `/admin/upgrade` and `/system/upgrade` → upgrade.
  - `/memory` → memory.
  - `/config?path=tables/grain_config.json` →
    `/settings/config-repo/tables/grain_config.json`.

  Pass.
- **Console and network:** no console error and no failed request
  throughout. Pass.

### Filter

- `/` focuses "Filter settings". Pass.
- Labels and keywords match by section:

  | Query | Matches |
  |---|---|
  | turns | Agents → max agent turns |
  | auto-merge | Tasks |
  | time zone | Preferences |
  | theme | Preferences → appearance |
  | reasoning | Models → default model |
  | deployment id | Agents |
  | lock | General → Lock settings in UI |
  | pinned | Preferences → sidebar |
  | channel | Upgrade, and General → automatic upgrade channel |

  Pass.
- Choosing *max agent turns* goes to `/settings/agents#maxAgentTurns`,
  focuses the box and outlines it (one `.setting-highlight`). Pass.
- "zzqqxx" says "No setting matches 'zzqqxx'.", and Escape restores the
  groups. Pass.

### Save model (default demo)

| Check | Outcome |
|---|---|
| Checkbox (Show closed tasks by default) | One PUT, and the header reads "All changes saved". Read back after a reload, then restored. Pass. |
| Text (Environment name) | Nothing sent while typing, one PUT on Enter, tab title "walk-env — grain". Read back after a reload. Clearing saved on blur, and the title went back to "grain". Pass. |
| SearchSelects (General → After an upgrade, Automatic upgrades) | They render as comboboxes with their current choice ("File it and queue it (default)", "Off (manual only)"), and are enabled on an unlocked demo. Pass. |
| Out of range (`-5` in Max agent turns) | **No PUT.** The header read "Couldn't save Max agent turns", the banner read "Max agent turns must be at least 0; -5 was not saved.", and the box reverted to 0. Putting 0 back cleared it to "All changes saved". Pass. |
| Couldn't save + Retry (stubbed 503) | The header read "Couldn't save Max agent turns" with one Retry, and the box rolled back. Retry saved 12, which read back after a reload. Restored. Pass. |
| Read-back of the server's answer (Engine → Poll interval) | `1m` + Enter read back as `1m0s`, at once and after a reload. Restored. Pass. |
| Save card (Agents → Standing instructions) | Blur sent nothing. The header read "Unsaved changes in Standing instructions", with one dirty dot in the nav. Changing section, and the rail's Inbox, each asked "You have unsaved changes here. Leave and discard them?", and dismissing stayed put. Discard restored the box. Save sent one PUT, which read back after a reload. Once clean, Tasks and Inbox asked nothing. Pass. |
| Restart needed (stubbed) | The chip is a focusable button in the header. Focus + Enter opens `/system/power`. Pass. |

### Preferences (browser in America/Los_Angeles)

- **Appearance:** Dark and Light set `html[data-theme]`, and Dark survived a
  reload. Auto followed the browser's light scheme. Pass.
- **Time zone:**
  - With none chosen the picker reads "Browser default
    (America/Los_Angeles)", and `/tasks/1` printed "11/3/2026, 9:52:54 PM".
  - Typing "Tokyo" and choosing Asia/Tokyo made it print "11/4/2026,
    2:52:54 PM", 17 h later, as it should be. The choice survived a reload,
    and was put back to the browser default.

  Pass.
- **Task timeline:** Oldest first survived a reload, and was restored.
  Pass.
- **Customize sidebar:**
  - Pinned through Type → Item → Pin item: Settings → Memory, System →
    Logs, the "Board" board and the "Director" session. Move up, then Save.
  - The rail read "inbox / memory / logs / board / director", the same after
    a reload.
  - The pinned Memory opened `/settings/memory`, and Logs opened
    `/system/logs`.
  - Unpinning all five and reloading emptied the pinned group.

  Pass.
- **Nothing deployment-wide:** no non-GET request was sent during any
  Preferences step. `localStorage` holds `grain.pinned` and
  `grain.storageVersion`. Pass.

### Access

- **This browser card:** "Signed in with the access code. Changes made here
  are recorded as operator (access code), not as a person." The rail shows
  `operator (access code)`. Pass.
- **A fresh context:**
  - `/settings/access` showed "Enter access code". Continue landed back on
    **`/settings/access`**: the 11-03 deep-link follow-up is fixed.
  - Log out returned to the access-code prompt on the same URL.

  Pass.
- **Usable when locked or external:**
  - Under the daemon's lock, Access keeps Log out and the allowed-principals
    controls enabled.
  - Under the external demo, Log out, each principal's Remove and Add
    allowed principal stay enabled.

  Pass.

### General

- **Settings lock:** disabled on the demo ("unavailable without a config
  repository: settings are always edited directly here."). Enabled on the
  daemon, where it saved and read back after a reload. Pass.
- **Updates:** After an upgrade → Check the upgrade, Hold the queue until
  the check is done, and Automatic upgrades ("Off (manual only)"; the
  channel and time of day were not switched on today). Pass.
- **About:** "v1.2.0 · a3bcafa-dirty · 2026-11-04 09:15Z", the same text as
  the build stamp at the foot of the rail. Pass.

### Config repo and lock (real daemon; external demo)

- **Internal panel:**
  - "Internal (grain/config on this host — default) …". The head chip reads
    `grain/config | main | 6be8cd1b`, with the working tree's path.
  - Buttons: Export now, Use external config repository (with Repository
    URL and Branch), Show files, Back up secrets here and Import key.
  - The backup note reads "No external config repository is configured.
    grain backup protects it …".

  Pass.
- **Config Repo Browser:**
  - Before the first export cycle the caption is "showing 6be8cd1", with no
    year-1 stamp (11-03's fix holds). At that point `tables/grain_config.json`
    is `[]`.
  - After a cycle the caption reads "showing 8b012a0, exported 11/4/2026,
    12:36:41 PM", and the file previews its row (`id`, `poll_interval_ms`,
    `max_workers` …).
  - `/config?path=README.md` opens `/settings/config-repo/README.md`, which
    renders as Markdown ("grain state", "What is here", "Changing
    something", "Secrets are not here").

  Pass.
- **Lock on (through General's checkbox).** Counting every control in the
  section that is not `:disabled`:

  | Section | Enabled controls | "View in config repo →" links |
  |---|---|---|
  | General | the lock itself (a bootstrap setting) | `tables/grain_config.json` |
  | Agents | none | `tables/grain_config.json` |
  | Tasks | none | `tables/grain_config.json`, `tables/custom_field.json` |
  | Memory | none | `memory/deployment.md` |
  | Engine | Max worker agents (bootstrap, by design) | `tables/grain_config.json` |
  | Caches | none | `tables/grain_config.json` |
  | Legacy capabilities | only the per-capability row buttons, which open each capability's details and change nothing | `tables/grain_config.json` |

  Each link points at `/config?path=…`. Agents' link opens
  `/settings/config-repo/tables/grain_config.json`. Pass.
- **Keyboard:** on locked Tasks, Tab stops only on the two "View in config
  repo" links. On General it stops on the link and the lock. No disabled
  SearchSelect or chip takes focus. Pass.
- **A refused write offers a held config pull request:**
  - Processes → + New → template → Add template opened the refusal dialog:
    "This setting lives in grain/config (main) and is changed with a config
    pull request there … Open in Config Browser → tables/template_sequence.json
    …", with the diff.
  - Create Config PR answered "Opened config pull request #2 as task 2. It
    waits for a person to submit it."
  - The only failed request was the expected `409 POST /api/templates`.

  Pass.
- **Unlocking:** it saved ("All changes saved"). Agents' Max agent turns was
  editable again, its "View in config repo" link had gone, and no refusal
  banner was left. Pass.
- **External mode (demo):**
  - General, Agents, Tasks and Memory have no enabled control, under
    "Configuration is read-only and managed in
    https://github.com/acme/grain-config.git (main). …".
  - On Models only Test Connection and Edit stay enabled.
  - `PUT /api/settings` answered 409.

  Pass.
- **Failing-sync alert (stubbed `/api/config`):**
  - The Inbox carries "Settings have not been exported to grain/config since
    11/4/2026, 9:00:00 AM: push rejected (stubbed)".
  - Its "config repo" link opens `/settings/config-repo`.

  Pass.

### Models (`-agent-paused`, with the provider stub)

- **Paused default:**
  - The row reads "paused until 3:57:02 PM (about 5h 59m) — antigravity:
    usage limit reached …".
  - The pane warns "Every enabled connection that serves model
    gemini-2.5-pro is paused. …".
  - Resume cleared both, and they stayed clear after a reload.

  Pass.
- **Test Connection:** "Connection valid". Pass.
- **Disable:**
  - It warns "Every provider connection is disabled, so no run has anything
    to run on …".
  - It also warns "No enabled connection serves model gemini-2.5-pro. Tasks
    on the default model wait in the queue until one does.", which survived
    a reload.
  - Enable cleared both.

  Pass.
- **Unfinished new connection:**
  - The agent types are antigravity, claude and codex (custom command was
    removed by `b30e5c7b`).
  - Claude → Continue, then "← Models", asked only the general discard
    question. Afterwards only Gemini 2.5 Pro remained.

  Pass.
- **Finished new connection:**
  - Claude → Authentication method "Claude Code OAuth token" (the other
    choice is Google Cloud Vertex AI) → token → Name "Walk Claude".
  - "Discovered models" listed both stub models, and Save connection stored
    it.
  - After a reload the card read "Advertises: claude-walk-discovered-1,
    claude-walk-discovered-2 (every model its provider lists)".

  Pass.
- **Pickers:** Default model, the session picker (`/session/new`) and New
  task's Model each offered the same list: deployment default,
  gemini-2.5-pro, gemini-2.5-flash, claude-walk-discovered-1 and -2.
  Schedules and templates use the same `ModelPicker` (`ScheduleOverlay.jsx`,
  `TemplateOverlay.jsx`) and were not opened today. Pass.
- **Unadvertised default:** a default no connection advertises
  (`retired-model-9`, set over the API) is warned about by name. It was
  restored. Pass.
- **Reasoning effort:**
  - Options: model default / low / medium / high. *high* read back after a
    reload, and was restored.
  - The Claude models say "Claude takes no reasoning effort."

  Pass.
- **Where each agent CLI runs:** claude, codex and agy each report a run
  location, with the hostd explanation. Pass.
- **Remove:** it asked "Remove the connection Walk Claude? A credential
  stored for it is deleted with it …". Accepting removed it. Pass.
- **Network:** no failed request. Pass.

### Agents

- **Branch name template:** `grain/no-id`, `bad branch {id}`,
  `grain/task-{id}.lock` and `a..b/{id}` were each refused with "Couldn't
  save Branch name template" and the reason ("must contain {id}" or
  "produces invalid branch name …"), and the box reverted.
  `walk/{deployment}/t-{id}` saved and read back after a reload, and was
  restored to empty. Pass.
- **Deployment ID:** `Bad ID!` was refused. The placeholder stayed
  `2d6860ab`, and the helper still ends "Leave empty for this deployment's
  random ID, 2d6860ab." Pass.
- **Commit identity:** its Settings → Git link opens `/settings/git`. Pass.

### Memory

- The page shows the deployment's note, its documents ("what a release of
  this deployment actually needs …") and acme/widgets' note, each with who
  wrote it and when.
- **A correction:** appending "WALK correction 2963." gave "Unsaved changes
  in Memory", and nothing was sent.
  - The rail's Inbox asked the discard question.
  - Back and a reload each raised `beforeunload`. Dismissing kept the edit.
  - Save sent `PUT /api/memory`. Inbox → `/settings/memory` read it back.
  - The original was saved again and confirmed after a reload.

  Pass.
- Memory is not in the rail unless pinned, and `/repos/acme/widgets` does
  not mention it. Pass.

### Tasks

- **Saved defaults:** queue-immediately and auto-merge off. `-1` in Max
  unreviewed follow-up depth was refused by name, then 3 saved. All three
  read back after a reload.
- **Task defaults card:** Default legacy capabilities is a SearchSelect.
  Default grants reads "None granted / Add grant…".
- **New task:** + New task opened with Auto-merge and Queue immediately
  unchecked. Every default was restored.
- **Task fields (stubbed withdrawal of `medium`):**
  - Priority reads "2 tasks still carry a value this field no longer
    offers".
  - The link opens `/?f=custom.priority:legacy:`, which shows the "priority
    is a legacy value" filter and the two `priority medium` rows.

  Pass.

## Bugs

None found in Settings on this run.

## Unrelated

- **`main` at `4460d761` does not compile:**
  - `pkg/cli/main.go:684: duplicate case "login"`, against line 610.
  - `#698` and `#512` both added a `login` case to `dispatchCLI`.
  - This is a merge clash for the base-branch repair, so nothing was changed
    for it here. It was worked around locally, uncommitted, only to build
    the daemon.

## Observations (not changed)

- **The brief is out of date in three places:**
  - the old executor links land on **Hostds**, by design since `b548f016`;
  - the agent types no longer include custom command (`b30e5c7b`);
  - external mode has no in-app "View in config repo" link, because the
    configuration lives in the external repository.
- **Max agent turns still has no upper bound.**
- **The connection form is not deep-linkable.**
