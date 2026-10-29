# 2026-10-29: Settings walkthrough (layout, save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 1758 (`grain/049f1e09/task-1758`): a browser walkthrough of the Settings
view's layout, filter and save model, the browser's own Preferences, and the
Deployment, Agents and Work groups of sections, Memory among them. It ran
against real `grain demo` servers (and, for Config repo, a real `grain daemon`)
and their built frontend, driven by Playwright through visible controls.
Connections and Infrastructure (Git, Capabilities, Legacy capabilities,
Executors, Engine, CI, Sandboxes, Caches), System, and Settings → Repositories
belong to other walkthroughs and were only touched where a check here needed
them.

## Commit and environment

- **Base tested**: `99581723` (`main`, "Merge pull request #143 from
  ai-on-gke/grain/049f1e09/task-1753"). The fixes below were re-verified
  in the browser against demos rebuilt from this branch.
- **Servers**:
  - `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file …`, the
    main walkthrough server.
  - `grain demo -addr 127.0.0.1:18081 -agent-paused …`, for a real
    usage-limit pause on the demo's connection.
  - `grain daemon -data-dir … -ui-addr 127.0.0.1:18090 -sandbox-port-base=0
    -as alice -agy-path /bin/true -gemini-api-key-file <dummy>
    -github-host 127.0.0.1:9 -github-insecure-http`, for Config repo. The
    demo has no state repository; the daemon hosts a real `grain/config`.

  All three were built with `go build -tags grain_e2e -ldflags "-X
  …/pkg/version.Release=v1.2.0" ./cmd/grain`, over the production bundle
  from `npm run build` (Vite), each on a throwaway embedded store.
- **Browser**: Playwright (`ui/node_modules`) driving Chromium
  (`chromium-1234`) from `/var/cache/playwright`. It signed in by typing the
  access code into the "Enter access code" page. Widths were desktop
  1280×860 (and 1280×600 for the hash-scroll check), tablet 800×1000 and
  phone 390×844, in light and dark. The browser zone was UTC, or
  America/Los_Angeles for the time-zone check.
- **Toolchain**: Debian, Go (`/usr/local/bin/go`), Node `v20.19.0`, Vitest
  `4.1`.
- **Gemini key**: `/home/debian/.gemini-api-key` is absent from this sandbox,
  so no model call was made. Connections were exercised through the real
  server and UI only.
- **Scripts**: the walkthrough scripts were ad hoc Node/Playwright files
  kept outside the repository (`/var/tmp/walk`). The regressions they found
  are pinned in `ui/e2e/settings-models-walkthrough.spec.js` and in unit
  tests (below).

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| `PUT /api/settings` | `page.route` answered the **first** PUT with `503 stubbed outage`, then let every later one through to the real server. | To produce "Couldn't save <field>" with Retry deterministically. Retry then went to the real server. |
| Config repo | **Not stubbed.** A real `grain daemon`, with an external repository that was a local bare git repo (`/var/tmp/rd/external.git`). The "failing sync" was made by renaming that repo away, and undone by renaming it back. | The demo has no state repository. |
| Model provider | **Not stubbed.** Test Connection on the demo answered "Connection valid" from the demo itself, and model discovery reported "This deployment cannot ask this provider for its models". | No key in the sandbox. Discovered models could therefore not be shown live (see Not exercised). |
| GitHub, upgrade | **Not stubbed.** Nothing in these sections reaches them. The demo's `github-branches` 404 on `/tasks/1` is the demo lacking GitHub data, outside this area. | — |

## Steps and outcomes

### Layout, paths and filter

| Check | Steps | Outcome |
|---|---|---|
| Gear | The header rail's only Settings entry is the gear button (`aria-label="Settings"`). On a phone the same gear is in the top bar. | Pass |
| Bare `/settings` | Desktop opened `/settings/general`. Phone showed the grouped section list at `/settings`. | Pass |
| Section in the URL | Moved agents → tasks → models by URL, then reloaded (stayed on models). Back went to tasks and Forward to models. | Pass |
| `#card` | `/settings/tasks#task-fields` at 1280×600 scrolled the Task fields card into view (y≈129), and again after a reload. | Pass |
| Old links | `/admin` → general. `/admin/models` → models. `/admin/workflow` → tasks. `/admin/upgrade` and `/system/upgrade` → `/settings/upgrade`. `/memory` → `/settings/memory`. `/admin/logs` → `/system/logs`. | Pass |
| Tablet | At 800 px the nav became a "Section" select. Choosing Models navigated there. No horizontal overflow. | Pass |
| Phone | The list opened Agents alone, under a ← ("All settings") button that returned to the list. `scrollWidth` = 390. | Pass |
| Filter | `/` focused "Filter settings". "turns" listed *max agent turns, agents*. Choosing it went to `/settings/agents#maxAgentTurns`, focused the box and outlined it (`.setting-highlight`). The helper keyword "auto-merge" listed *auto-merge new tasks by default, tasks*. "zzqqxx" said "No setting matches 'zzqqxx'." Escape restored the groups. | Pass |

### Save model

| Check | Outcome |
|---|---|
| Checkbox commits on change (General → show closed tasks by default) | One PUT on click, read back after a reload, then restored. Pass. |
| Select commits on change (General → Updates check mode) | `approve`, `off`, `auto` each PUT on choice and read back after a reload. Pass. |
| Text/number commits on blur or Enter (Max agent turns) | Enter saved `40`, which read back after a reload. Blurring by clicking elsewhere also saved. Pass. |
| Multi-line card waits for Save (Standing instructions) | Typing and tabbing out sent nothing. The header said "Unsaved changes in Standing instructions" and the nav showed the dirty dot. Discard restored the text to "All changes saved". Save PUT it, a reload read it back, and it was then restored. Pass. |
| Number box alone is not "unsaved" | While typing in Max agent turns the header stayed "All changes saved". This is by design (`useSettingsSection.js`): only Save cards are dirty, because a box commits as focus leaves it. |
| Leave prompts | With a dirty Save card: the section nav, a rail click and Back each asked "You have unsaved changes here. Leave and discard them?". Dismissing stayed put. A reload raised `beforeunload`. A clean section moved to another section and to the Inbox without asking. Pass. |
| Readback of what the server answered | Poll interval `1m` read back as `1m0s` (the server's form; the brief's "60s" example is from an older build), then restored to `10s`. Pass. |
| Out-of-range refused by name, no request | `-5` in Max agent turns: "Couldn't save Max agent turns", no PUT, box kept its saved value. Putting `0` back cleared the error to "All changes saved". Pass. |
| Couldn't save + Retry | With the stubbed 503: "Couldn't save Max agent turns" and a Retry button. Retry saved through the real server, and `12` read back after a reload. Restored to `0`. Pass. |
| Restart needed chip | Not raised. The demo's restart-required settings (`githubHost`, `k8s*`, `sandboxBackend`) all sit in Connections/Infrastructure, and the chip's `onClick` → System → Power is covered by `restart-banner.spec.js`. Not exercised here. |

### Preferences (`/settings/preferences`)

- **Appearance**: Dark, Light and Auto each set `html[data-theme]`. Dark
  survived a reload. Restored to Auto.
- **Time zone**: with none chosen and the browser in Los Angeles, the Inbox
  printed "since 2:28:02 AM" and the task page "10/28/2026, 11:08:02 PM".
  With `Asia/Tokyo` chosen, the same rows read "6:28:02 PM" and
  "10/29/2026, 3:08:02 PM". The build stamp `… 2026-10-29 09:31Z` is UTC by
  its own `Z`, and its tooltip follows the zone. System → Logs prints the
  daemon's lines verbatim. Restored to none.
- **Task timeline**: Oldest first survived a reload. Restored to Newest first.
- **Customize sidebar**, opened from Preferences:
  - Pinned *settings → memory* (a Settings section), *system → logs* (a
    System tab), the *board* board and the *director* session.
  - Unpinned Inbox, moved the last item up, then saved.
  - After a reload the rail read *memory / logs / director / board*.
    `grain.pinned` held the four entries in that order. The pinned Memory
    opened `/settings/memory` and the pinned Logs opened `/system/logs`.
  - Reset to default + Save put back just Inbox.
- **Nothing deployment-wide**: no `PUT /api/settings` or `/api/config`
  request was sent from Preferences.

### Access (`/settings/access`)

"This browser: Signed in with the access code." Log out returned to "Enter
access code", and a reload stayed there. (The button reads "Log out", not
"Sign out".)

### General

- **Deployment**: Environment name `walk-env` saved on Enter, read back after
  a reload, and showed in the tab title ("walk-env — grain"). It was then
  cleared. Show closed tasks: see Save model.
- **Updates**: the check mode (don't / file a proposal to approve / file it
  and queue it) and "hold the queue until the check is done" each committed
  and read back. The hold box is disabled under "don't". Restored.
- **About**: `v1.2.0 · 9958172 · 2026-10-29 09:31Z`, identical to the build
  stamp at the foot of the rail.

### Config repo (real daemon)

- **Internal**: the panel shows the `grain/config` · `main` · head chips, the
  working tree, and Export now. Export now ran without error.
- **External**: Repository URL = a local bare clone, then "Use external
  config repository". The panel switched to the read-only IaC wording, with
  Pull now and "Switch to internal config repository".
  - General, Models, Agents, Memory and Tasks each showed
    `external-config-repo-banner`, with every card's fieldset disabled and
    no enabled control in the section.
  - A write sent anyway (`PUT /api/settings {"maxAgentTurns":7}` from the
    page) was refused `409 config_managed_externally`. The refusal names
    `/var/tmp/rd/external.git@main`, the file `tables/grain_config.json`
    and the exact diff to commit, and ends "Open a pull request there;
    grain applies it on its next sync."
- **Failing sync**: with the external repository renamed away, Pull now
  answered 502, and the panel's warning carried git's error after a
  reload. The Inbox row and alert appear only once the sync has failed
  for `syncFailureGrace` (5 minutes, `pkg/daemon/statemanager.go`).
  After that window:
  - `/api/config` carried `stateRepoSyncFailure`.
  - The Inbox showed a row whose **Config repo** button opened
    `/settings/config-repo`.
  - The alerts panel listed it.

  With the repository restored, Pull now cleared the failure, Switch to
  internal returned to internal mode with no banner, and the Inbox row was
  gone.

### Models

- **Connections**:
  - Test Connection → "Connection valid".
  - Disable → chip *disabled*. A reload kept it, and Enable restored it.
    While the only connection was disabled the section warned "No enabled
    connection serves model gemini-2.5-pro. Tasks on the default model
    wait in the queue until one does."
  - Edit opened the connection page (authentication, name, concurrency,
    CLI path, advanced timeouts, advertised models).
  - Remove asked "Remove the connection Gemini 2.5 Pro? …". Accepting it
    left "This deployment has no provider connection, so its tasks wait.
    Create one to authenticate agent models.", which survived a reload.
- **New connection**: choosing an agent type and Continue stores a disabled
  draft. Leaving with ← Models (after the leave prompt) deleted it with no
  Remove prompt. **Leaving by reload did not delete it** (bug 2).
- **Default model and effort**:
  - The pickers offered the two advertised models.
  - New task's Model offered "Deployment default (gemini-2.5-pro)",
    gemini-2.5-pro and gemini-2.5-flash.
  - Reasoning effort *high* on gemini-2.5-pro survived a reload.
  - With no connection advertising the default, the picker read
    "gemini-2.5-pro (no connection advertises it)".
  - A free-typed unknown default (`no-such-model-xyz` + Enter) was not
    taken; the picker kept its value.
- **Pause** (`-agent-paused` demo): the connection was chipped *Paused*, and
  every model read "(paused)" in the served-by list. **There was no Resume
  on the connection's row** (bug 1), although `/api/config` said
  `canLift: true`.
- **Where each agent CLI runs**: claude and codex report "host (not found on
  PATH)". agy reports "host (/opt/agy/bin/agy) set by -agy-run". Custom
  commands run on the host. On this bare-machine daemon, claude reports
  container set by `-claude-run` as unavailable ("runs on the host until
  this is set to host or default").

### Agents

- Max agent turns: see Save model.
- Standing instructions: Save and Discard, see Save model.
- Branch name template:
  - `grain/no-id` (no `{id}`) and `grain/{id}-{slug}-x` (`{slug}` not
    last) were each refused, "Couldn't save Branch name template", with
    the box reverting to its stored value.
  - `walk/{deployment}/t-{id}` saved and read back after a reload, then
    was restored.
- Deployment ID: empty, with the random ID `d0e534a4` as placeholder.
  `Bad ID!` was refused.
- Commit identity: the card points to Settings → Git in prose. It has no
  link.

### Memory (`/settings/memory`)

- Shows *this deployment* (written by the memory agent, 271 of 8192
  characters) with its document *release-checklist*, and *acme/widgets*
  ("edited here") with its documents.
- Appending "WALK correction." to the deployment note:
  - The header read "Unsaved changes in Memory".
  - The rail and Back each asked first; a reload raised `beforeunload`.
  - Save, then reopening `/settings/memory`, read the correction back.
  - Saving the original text restored it, and a reload confirmed it.
- Memory is not in the rail by default. Once pinned it opens Settings →
  Memory. `/repos/acme/widgets` shows no Memory.

### Tasks

- Turned off "Queue new tasks immediately by default" and "Auto-merge new
  tasks by default", and set Max unreviewed follow-up depth to 3. All three
  read back after a reload.
- **+ New task** then opened with both "queue" and "auto-merge" unticked.
  All three were restored.
- Default legacy capabilities (self debug, gcp key, gemini key) and default
  grants are listed. GitHub-CI gating is a per-repository note.
- Task fields lists the six declared fields (priority, area, requested-by,
  due-date, estimate, customer-facing) read-only, declared in
  `tables/custom_field.json`. The "no longer offered" note is on a task's
  own form (`CustomFields.jsx`), not here. The demo holds no value outside
  a declaration, so it was not shown.

### Console and network

No console errors or failed requests in this area, except the expected
ones:

- 401 on the first load before signing in, and after Log out.
- The intentional 400s/409 from refused values.
- The stubbed 503.
- The 502 from the broken Pull now.

## Bugs found and fixed

1. **A paused connection could not be resumed from Settings → Models.**
   - Repro: `grain demo -agent-paused`, open `/settings/models`. The row
     says "paused until … (about 5h 59m)" and `/api/config`'s pause says
     `canLift: true`, but the row has no Resume. A paused *model* of a
     connection had one; only the alerts panel could lift a whole
     connection.
   - Fix: `ModelsSection.jsx` shows Resume beside the pause's sentence
     when `canLift`. It sends `DELETE /api/pause?connection=…`, the same
     call the alerts panel makes. Laid out with `useFlexGap`, so that
     when it wraps under a long sentence it is flush with the sentence's
     left edge (it was first indented 8 px by Stack's margin spacing).
2. **A new connection left unfinished by a reload or a closed tab was never
   cleaned up.**
   - Repro: Models → New connection → Antigravity → Continue, then reload
     and accept the leave prompt. A second row named
     `backend-<uuid>` · *disabled* · "Advertises: no models — set one"
     remains. The cleanup (`onDiscard`) ran only on ← Models and on
     unmount, and a page unload is neither.
   - Fix: `ModelBackendPage.jsx` deletes the draft on `pagehide`, with a
     `keepalive` DELETE (`ModelsSection.deleteBackend(id, { keepalive })`)
     that outlives the page.
3. **"Advertise all models" with nothing listed by the provider advertised
   none.**
   - Repro: Edit the demo's connection. Discovery says "This deployment
     cannot ask this provider for its models". Press "Advertise all
     models", then Save connection. The row reads "Advertises: no models
     — set one", and the default model "(no connection advertises it)".
   - Fix: `ConnectionModelsField.jsx` disables the button while the
     provider has listed nothing, with a line saying the connection keeps
     the models listed.

Regression coverage:

- `ui/e2e/settings-models-walkthrough.spec.js` (real demo, `-agent-paused`)
  covers all three, including the Resume button's bounding box against the
  pause sentence.
- Unit tests in `ModelsSection.test.jsx` (Resume, and no Resume without
  `canLift`), `ModelBackendPage.test.jsx` (pagehide deletes an unfinished
  draft once, never a finished one) and the new
  `ConnectionModelsField.test.jsx`.

Screens shot after the fix:

- `/settings/models` (paused row) and the connection Edit page (the
  disabled Advertise all and its line).
- 1280×860 and 390×844, light and dark.
- Bounding boxes checked: Resume inside the viewport and flush with the
  sentence when wrapped; no horizontal overflow; the hint inside the
  viewport.

## Not exercised

- Discovered models feeding the pickers: no provider could be asked without
  a key. The pickers were checked with the advertised list only.
- Model pickers on sessions, schedules and templates. New task's was
  checked. The other three (`NewSessionPage`, `ScheduleOverlay`,
  `TemplateOverlay`) share `ModelPicker`, which `model-picker-search.spec.js`
  covers.
- The Restart needed chip (see Save model).
- The "no longer offered" task-field note (needs a declaration change via
  the config repo).
