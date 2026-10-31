# 2026-10-30: Settings walkthrough (layout, save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 1904 (`grain/049f1e09/task-1904`) is a browser walkthrough of the
Settings view, in the same scope as the 2026-10-29 run
(`2026-10-29-user-system-settings.md`). It covers the view's layout,
filter and save model, the browser's own Preferences, and the
Deployment, Agents and Work groups of sections, Memory among them.

It ran against real `grain demo` servers and a real `grain daemon`, with
their built frontend, and Playwright drove them through visible
controls. This run also covered three things the earlier one could not:

- discovered models, through a stubbed Anthropic model listing;
- the Restart needed chip, through a real pending restart;
- the failing-sync Inbox row, after the daemon's five-minute grace.

Connections and Infrastructure, System, and Settings → Repositories
belong to other walkthroughs. This run touched them only where a check
here needed them: Legacy capabilities' GitHub host for the restart chip,
and System → Power as the chip's target.

## Commit and environment

- **Base tested**: `e1b148a4` (`main`, "Merge pull request #212 from
  ai-on-gke/grain/049f1e09/task-1916"). The fixes below were checked
  again in the browser against servers rebuilt from this branch.
- **Servers**, each on a throwaway embedded store:
  - `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file …`:
    the main walkthrough server.
  - `grain demo -addr 127.0.0.1:18081 … -model-catalog 127.0.0.1:18099
    -model-discovery-interval 5s -agent-paused`: Models. It has a real
    usage-limit pause, and Claude connections discover their models from
    the stub below.
  - `grain daemon -data-dir … -sandbox-dir … -ui-addr 127.0.0.1:18090
    -sandbox-port-base=0 -as alice -agy-path /bin/true
    -gemini-api-key-file <dummy> -github-host 127.0.0.1:9
    -github-insecure-http`: Config repo and the Restart needed chip. The
    demo has no state repository and no running configuration to
    differ from.
- **Build**: `npm run build` (Vite), then `go build -tags grain_e2e
  -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`.
- **Browser**: Playwright (`ui/node_modules`) driving Chromium from
  `/var/cache/playwright`, signing in by typing the access code on the
  "Enter access code" page.
  - Widths: 1280×860 (1280×600 for the `#card` check), 800×1000 tablet
    and 390×844 phone.
  - Time zone: browser in UTC, or America/Los_Angeles for the time-zone
    check.
- **Toolchain**: Debian, Go (`/usr/local/bin/go`), Node v20.19.0.
- **Gemini key**: `/home/debian/.gemini-api-key` is absent from this
  sandbox, so no real model call was made.
- **Scripts**: the walkthrough scripts were ad hoc Node/Playwright files
  outside the repository (`/var/tmp/walk`). The regressions they found
  are pinned by the tests named under "Bugs found and fixed".

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| Model provider (model listing) | A loopback Node server on `127.0.0.1:18099` answered `GET /v1/models?limit=1000` with two models, `claude-walk-discovered-1` and `-2`. The second demo was pointed at it with `-model-catalog`. | Discovery needs a provider to ask, and this sandbox has no key. Everything else is the real server and UI. |
| `PUT /api/settings` | `page.route` answered the **first** PUT with `503 stubbed outage`, then let every later one through. | To raise "Couldn't save <field>" with Retry deterministically. Retry then went to the real server. |
| Config repo | **Not stubbed.** A real daemon, with an external repository that is a local bare clone of its own `grain/config` (`file:///var/tmp/rd/external.git`). A failing sync was made by renaming that directory away, and undone by renaming it back. A pending restart was made by committing a `github_host` change to the daemon's internal `grain/config`, which it applied within half a minute, then committing it back. | The demo has neither. |
| Test Connection | **Not stubbed.** The demo answers "Connection valid" itself. | — |
| GitHub, upgrade | **Not stubbed.** Nothing in these sections reaches them. The demo's `GET /api/repos/acme/widgets/github-branches` 404 on `/tasks/1` is the demo lacking GitHub data, outside this area. | — |

## Steps and outcomes

### Layout, paths and filter

| Check | Outcome |
|---|---|
| The gear at the right of the header rail is the only Settings entry (one match), and on a phone the same gear is in the top bar. | Pass |
| On desktop, `/settings` opens `/settings/general`. On a phone it shows the grouped list at `/settings`. | Pass |
| Moving agents → tasks → models, then reloading, stays on models. Back goes to tasks and Forward to models. | Pass |
| At 1280×600, `/settings/tasks#task-fields` scrolls Task fields to y≈129. | Pass |
| Old links: `/admin` → general, `/admin/models` → models, `/admin/workflow` → tasks, `/admin/upgrade` and `/system/upgrade` → `/settings/upgrade`, `/memory` → `/settings/memory`, `/admin/state` → `/settings/config-repo`. | Pass |
| At 800 px the nav is a "Section" select. Choosing Models navigates there, and `scrollWidth` is 800. | Pass |
| On a phone, Agents opens alone under ← ("All settings"), which returns to the list. `scrollWidth` is 390. | Pass |
| `/` focuses "Filter settings". | Pass |
| "turns" lists *Agents: max agent turns*. Choosing it goes to `/settings/agents#maxAgentTurns`, focuses `maxAgentTurns` and outlines it (one `.setting-highlight`). | Pass |
| These queries list their matches by section: "auto-merge" (helper keyword), "merge" (Tasks, then three Engine fields), "time zone", "theme" (→ appearance), "reasoning" (→ default model) and "deployment id". | Pass |
| "zzqqxx" says "No setting matches 'zzqqxx'.", and Escape clears the box and restores the groups. | Pass |

### Save model

| Check | Outcome |
|---|---|
| Checkbox (General → Show closed tasks by default) | One PUT, `{"showClosedByDefault":true}`, on click. It read back after a reload and was restored. Pass. |
| Text box, Enter (General → Environment name) | `walk-env` saved and read back. The tab title became "walk-env — grain" and the name showed beside the mark. Clearing it on blur restored "grain". Pass. |
| Select (General → Updates) | The check-mode select and the "hold the queue" checkbox are there. The select's commit-on-change was exercised on 10-29 and not repeated. |
| Number, Enter (Agents → Max agent turns) | `40` read back after a reload, then was restored to `0`. Pass. |
| Out of range | `-5`: "Couldn't save Max agent turns", banner "Max agent turns must be at least 0; -5 was not saved.", and **no PUT**. Putting `0` back cleared both. `2.5` was refused the same way. Pass. |
| Number with no upper bound | `99999999999999` saved and read back. There is no max on this field; see Observations. |
| Couldn't save + Retry | With the stubbed 503: "Couldn't save Max agent turns", the box rolled back to `0`, and a Retry button appeared. Retry went to the real server, and `12` read back after a reload. Pass. |
| Save card (Agents → Standing instructions) | Typing and tabbing out sent nothing. The status read "Unsaved changes in Standing instructions" and the nav showed the dirty dot (`settings-dirty-dot`, aria-label "unsaved changes"). Discard restored the text. Save sent a PUT and the text read back after a reload. Pass. |
| Leave prompts | With a dirty card, the section nav and a rail click (Inbox) each asked "You have unsaved changes here. Leave and discard them?". Dismissing stayed put. Back across a document and a reload raised `beforeunload`. A clean section moved to Tasks and Inbox with no prompt. Pass. |
| Restart needed chip | A real pending restart on the daemon (`pendingRestart: ["githubHost"]`, from a merged `grain/config` change). The chip shows on General, Access, Agents, Tasks and Legacy capabilities, and not on Models, Memory, Preferences or Config repo (`READS_SETTINGS`). It is `role=button tabindex=0`; Enter on it opens `/system/power` ("Restart grain"). Once the change was reverted it cleared. Pass. |

### Preferences (`/settings/preferences`)

- **Appearance**: Dark, Light and Auto each set `html[data-theme]`
  (dark/light, and Auto follows the browser's light scheme). Dark
  survived a reload.
- **Time zone**: with none chosen, the first option reads "Browser
  default (America/Los_Angeles)", and `/tasks/1` printed "10/30/2026,
  1:00:30 AM". With `Asia/Tokyo` chosen it printed "10/30/2026, 5:00:30
  PM", 16 hours later as it should be. The choice survived a reload and
  was restored to none.
- **Task timeline**: Oldest first survived a reload. Restored to Newest
  first.
- **Customize sidebar**:
  - Pinned *settings → memory* (a Settings section), *system → logs* (a
    System tab), a board ("board") and a session ("director").
  - Move up put Director above Board.
  - Save. The rail read "inbox / memory / logs / director / board",
    the same after a reload.
  - The pinned Memory opened `/settings/memory`, and Logs opened
    `/system/logs`.
  - Unpinning all four and reloading restored the rail.
- **Nothing deployment-wide**: no non-GET request was sent during any of
  the Preferences steps.

### Access

The This browser card says "Signed in with the access code." and has a
Log out button. Log out returning to the access-code prompt was
confirmed on 10-29 (`access-code-walkthrough.spec.js` covers it) and not
repeated here, because it would have ended the walkthrough's session.

### General

- **Deployment**: Environment name and Show closed tasks by default
  (above).
- **Updates**: the check mode ("File it and queue it (default)") and
  "hold the queue until the check is done".
- **About**: "v1.2.0 · e1b148a · 2026-10-30 11:31Z", identical to the
  build stamp at the foot of the rail.

### Config repo (real daemon)

- **Internal**: "Internal (grain/config on this host — default)", with
  chips grain/config / main / head, the working tree and the last
  settings snapshot.
- **Writes in internal mode**: Max agent turns and GitHub host were each
  refused with 409 and the rehearsed patch ("this setting is not written
  directly: configuration is managed in this deployment's config
  repository grain/config (main) … Commit this change to
  tables/grain_config.json instead: diff …"). This is
  `docs/design/config-repo-settings.md` §4.1.1, as designed: the UI's
  editors stay until Slice 4.
- **Switching to external** (Repository URL
  `file:///var/tmp/rd/external.git` → Use external config repository):
  - The panel reads "External repository (Infrastructure as Code)".
  - Fields are disabled, under "Configuration is read-only and managed in
    file:///var/tmp/rd/external.git (main). Switch to the internal config
    repository … Edit tables/grain_config.json in rd/external (main),
    then open a pull request there".
- **Failing sync**:
  - Pull now with the remote gone answered only "an upstream service
    returned an error (ref …)" (**bug 1**).
  - After the next background tick, the panel showed git's error.
  - Five minutes in, the Inbox row and the critical alert appeared. Each
    has a Config repo button that opens `/settings/config-repo`. Both
    said "Settings are not being exported … to rd/external", which is
    wrong for a repository grain only reads (**bug 2**).
- **Recovery**: with the remote restored, Pull now succeeded. "Switch to
  internal config repository" returned to internal mode.

### Models (second demo)

- **Resume**: the paused connection's Resume lifted the pause, which
  stayed lifted after a reload.
- **Test Connection**: "Connection valid".
- **Disable / Enable**: Disable gave the chip *disabled* and the warning
  "No enabled connection serves model gemini-2.5-pro. Tasks on the
  default model wait in the queue until one does." Enable cleared both.
- **New connection, unfinished**: Claude → Continue, then ← Models. Only
  the leave prompt was asked (no Remove prompt), and no draft row
  remained.
- **New connection, finished**: Claude → Continue → Claude Code OAuth
  token (dummy).
  - The form listed **Discovered models: claude-walk-discovered-1,
    claude-walk-discovered-2** from the stub, advertised as "Every model
    the provider lists".
  - After Save connection the row read "Advertises:
    claude-walk-discovered-1, claude-walk-discovered-2 (every model its
    provider lists)".
- **Pickers fed by discovered models**: New task's Model and New
  session's Model both offered "Deployment default (gemini-2.5-pro)",
  gemini-2.5-pro, gemini-2.5-flash, claude-walk-discovered-1 and
  claude-walk-discovered-2. Schedules and templates use the same
  `ModelPicker`, which `model-picker-search.spec.js` covers.
- **Default model**:
  - Choosing `claude-walk-discovered-2` read back after a reload.
  - Typing `no-such-model-xyz` + Enter sent nothing and reverted on
    blur.
  - Restored to gemini-2.5-pro.
- **Reasoning effort**: *high* on gemini-2.5-pro
  (`PUT /api/deployment {"modelSettings":[{"model":"gemini-2.5-pro","effort":"high"}]}`)
  survived a reload, then was put back to Model default.
- **Remove**: asked "Remove the connection backend-…? A credential
  stored for it is deleted with it …". Dismissing kept it; accepting
  removed it, which a reload confirmed.
- **Where each agent CLI runs**:
  - claude: host (not found on PATH), with "set to container, but a
    daemon on the bare machine has no container of its own …".
  - codex: host (not found on PATH).
  - agy: host (/opt/agy/bin/agy), set by -agy-run.
  - Custom commands: host.

### Agents

- **Max agent turns** and **Standing instructions**: see Save model.
- **Branch name template**:
  - Refused, each with "Couldn't save Branch name template" and the box
    reverting: `grain/no-id`, `grain/{id}-{slug}-x`, `grain/{slug}-{id}`,
    `grain/{id}/{deployment}-{slug}` and `bad branch {id}`.
  - `grain/task-{id}.lock` **was saved** (**bug 3**).
  - After `bad branch {id}` was refused and a good template saved, the
    refusal's banner stayed up under "All changes saved" (**bug 4**).
  - `walk/{deployment}/t-{id}` saved and read back, then was restored.
  - The "use it" suggestion for the default template is shown while the
    template is empty.
- **Deployment ID**: the placeholder is the random ID. `Bad ID!` was
  refused, and `walk-dep` saved and was cleared again.
- **Commit identity**: a prose pointer to Settings → Git, without a link
  (see Observations).

### Memory (`/settings/memory`)

- Shows *this deployment* ("written by the memory agent", 271 of 8192
  characters) and its documents.
- **A reversible correction**: appending "WALK correction 1904." gave
  "Unsaved changes in Memory".
  - The rail asked first.
  - Back (across a document) and a reload raised `beforeunload`.
  - Save, then reopening via Inbox → `/settings/memory`, read it back.
  - Saving the original text restored it, which a reload confirmed.
- Memory is not in the rail unless pinned (pinned above, it opens
  Settings → Memory). `/repos/acme/widgets` has the tabs overview, ci,
  merge queue, agents, branches, releases and advanced, and says nothing
  about memory.

### Tasks

- Turned off "Queue new tasks immediately by default" and "Auto-merge
  new tasks by default". Set Max unreviewed follow-up depth to 3 (Enter)
  and per task to 2 (Tab). All four read back after a reload.
- **+ New task** then opened with `approved=false` and
  `autoMerge=false`. Everything was restored, and a reload confirmed it.
- `-1` in the follow-up depth gave "Couldn't save Max unreviewed
  follow-up depth", which cleared when the value was put back.
- Default legacy capabilities (self debug, gcp key, gemini key) and
  Default grants ("None granted") are listed.
- GitHub-CI gating is one line pointing to each repository's CI mode
  (Settings → Repositories).
- Task fields lists the six declared fields read-only, declared in
  `tables/custom_field.json`. The "no longer offered" note is on a
  task's own form (`CustomFields.jsx`). The demo holds no value outside
  a declaration, so the note was not shown (see Not exercised).

### Console and network

There were no console errors or failed requests in this area beyond the
expected ones:

- the 401 before signing in;
- the 400s and 409s from refused values;
- the stubbed 503;
- the 502 from Pull now against the missing remote;
- `net::ERR_ABORTED` on `/api/tasks` polls cut off by navigation.

## Bugs found and fixed

1. **Pull now did not say why it failed.**
   - Repro: real daemon in external mode. Rename the external repository
     away, then press Settings → Config repo → Pull now.
   - Before: "an upstream service returned an error (ref 8a05e900018b)",
     with the panel unchanged until the next background tick. The daemon
     log held the reason.
   - Fix: `handleSyncStateRepo` (`pkg/ui/staterepo.go`) returns the
     cycle's error with `apierror.Expose`. The caller holds
     settings:write, and `GET /api/state-repo` already shows the same
     text to settings:read as `status.error`.
   - After: the toast says "the state repository's remote could not be
     reached: fetching main: … does not appear to be a git repository …".
   - Test: `TestAFailedSyncNowSaysWhy`.
2. **An external repository's failing sync was reported as an export
   failure.**
   - Before: the alert and the Inbox row said "Settings are not being
     exported" / "Settings have not been exported to rd/external since
     …" / "Changes made in the UI since then exist only in this
     deployment's database". In external mode grain never exports, and
     the UI's settings are read-only. The alert's sentence also ended
     "exists.." (git's full stop, plus one more).
   - Fix: `ui.StateRepoSyncFailure` carries `external`, set by
     `StateManager.noteOutcome`. `alerts.js` (`stateSyncTitle`,
     `stateSyncImpact`, `stateSyncSentence`) and `InboxPage.jsx` say
     "Configuration is not being pulled" / "Configuration has not been
     pulled from <repo> since …" / "Changes merged in <repo> since then
     are not applied here …". The sentence gets a full stop only when it
     lacks one.
   - Tests: `TestStateSyncFailureSaysWhenTheRepositoryIsExternal` and two
     cases in `InboxPage.stateSync.test.jsx`.
3. **A branch name template git cannot push was saved.**
   - Repro: Settings → Agents → Branch name template
     `grain/task-{id}.lock` + Enter → "All changes saved".
   - Fix: `model.ValidBranchName` also refuses, as `git
     check-ref-format` does within its charset: a component starting
     with "." or ending in ".lock", and a trailing ".".
   - Tests: `TestValidBranchNameFollowsGitRefRules`, new cases in
     `TestFormatBranchNameRefusesUnsafeOrAmbiguousTemplates`, and
     `e2e/settings-agents-branch-template.spec.js`.
4. **A refusal's banner outlived the next good save.**
   - Repro: Branch name template `bad branch {id}` (refused), then
     `walk/{deployment}/t-{id}`. The header said "All changes saved"
     while the banner still said "… produces invalid branch name "bad
     branch 123"".
   - Fix: `useSettingsSection.save` calls `dismissError` for the error
     it clears on success.
   - Tests: a hook test in `useSettingsSection.test.jsx`, and the same
     e2e spec.

All four were checked again in the browser against servers rebuilt from
this branch:

- Pull now's toast names git's error.
- `grain/task-{id}.lock` and `grain/.x/task-{id}` are refused, and the
  banner clears on the good save.
- After the fix, with the remote gone since 12:55:40 (the row
  appeared at 13:01), the Inbox row reads "Configuration is not being
  pulled" / "Configuration has not been pulled from rd/external since
  10/30/2026, 12:55:40 PM: the state repository's remote could not be
  reached: … the repository exists." The alert adds "Changes merged in
  rd/external since then are not applied here until the sync is fixed."
  and ends with a single full stop.

## Observations (not changed)

- **Max agent turns has no upper bound**: `99999999999999` saved. That is
  harmless as a turn cap, but probably not intended.
- **Commit identity** names "Settings → Git" in prose, with no link.
- **Internal-mode copy**: the internal-mode panel still says grain
  "exports UI settings changes into" `grain/config`, while non-bootstrap
  writes are refused with a patch (§4.1.1). The refusal also tells a
  person to `POST /api/state-repo/pull-requests` rather than offering a
  button. Slice 4 of the design replaces these editors.
- **Unfinished drafts after a browser crash**: a new-connection draft
  is left behind when the browser is closed outright (Playwright's
  `browser.close()`, which fires no `pagehide`). It then appears as a
  "Reuse authentication from claude" choice and as a `backend-<uuid>`
  row until removed. A real tab close fires `pagehide`, which the
  10-29 fix handles.

## Not exercised

- **Log out**: covered by `access-code-walkthrough.spec.js`. Not
  repeated, because it ends the session.
- **The "no longer offered" task-field note**: it needs a declaration
  change in the config repository.
- **Schedules' and templates' model pickers**: these are the shared
  `ModelPicker`. New task's and New session's were checked live.
- **A screenshot contact sheet**: not taken. The only UI change is
  copy in an existing Inbox row and alert, plus a banner being
  dismissed. The pages were checked in the browser at 1280 px.
