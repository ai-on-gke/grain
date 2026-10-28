# 2026-10-23: User settings, System settings configuration and Memory walkthrough

Task 275 (`grain/049f1e09/task-275`), attempt 2: a browser walkthrough of
the reader's own settings, every deployment configuration section, the
Upgrade tab and the Memory pane. It ran against a real `grain demo` server
and its built frontend, driven by Playwright through visible controls.
Attempt 1 was cancelled before it produced anything, when the credential
it shared reached its usage limit.

## Commit and environment

- **Base tested**: `66b743a5d` (`candidate`, "Merge pull request #4528 from
  bwsalmon/grain/049f1e09/task-270"). Each fix below was then re-verified
  in the browser against a demo rebuilt from this branch.
- **Server**: `grain demo -addr 127.0.0.1:18080 -open=false
  -access-code-file …`, built with `go build -tags grain_e2e
  ./google/cmd/grain` over the production bundle from `npm run build`
  (Vite). It used a throwaway SQLite store under `/var/tmp`. Secrets were
  exercised on a second start with `-secrets-store`.
- **Browser**: Playwright `1.62.1` driving Chromium (`chromium-1234`) from
  `/var/cache/playwright`, signed in with the access code via `POST
  /api/auth/access-code`. Desktop was `1280×860` and `1440×900`, tablet
  `768×1024`, phone `390×844`, each in light and dark.
- **Toolchain**: Debian, Linux `6.1.0-54-amd64`, Go `1.26.9`, Node
  `v20.19.0`, Vitest `4.1`.
- **Gemini key**: `/home/debian/.gemini-api-key` is absent from this
  sandbox. The Gemini (Antigravity) connection, the default model and
  per-model effort were exercised through the real server and UI only.
  No model call was made, and `make ui-screenshots LOOK=1` was not
  available.
- **Scripts**: the walkthrough scripts were ad hoc Node/Playwright files
  kept outside the repository. The regressions they found are pinned in
  `ui/e2e/user-system-settings-walkthrough.spec.js`.

## Where things live in this build

The brief describes an older layout: a user tile at the foot of the rail,
an `/admin/<tab>` System pane holding configuration, a standalone
`/memory` page, and a System administrators card. Since
`docs/design/settings-panel.md` (SET-1..3), configuration lives in
**Settings** (`/settings/<section>`, from the top-bar gear) and operations
live in **System** (`/system/<tab>`, from the top-bar System icon and the
footer build stamp). The walkthrough mapped the brief onto that layout:

| Brief | This build | Observed |
|---|---|---|
| User settings from the user tile (`/settings`) | No user tile (SET-3). Settings → Preferences (`/settings/preferences`) and Settings → Access (`/settings/access`). `/settings` opens `/settings/general` on desktop and the section list on a phone. | As designed |
| `/admin`, and `/system` opening General | `/admin` → `/settings/general`. `/admin/models` → `/settings/models`, `/admin/workflow` → `/settings/tasks`, `/admin/upgrade` → `/system/upgrade`. `/system` opens System → Health, the operations pane (`systemDestination("")`), not General. | As designed (`settingsSections.js`) |
| General & Deployment, Agents & Task Branches | Settings → General, Settings → Agents | Covered |
| Engine & Concurrency (CI sections) | Settings → Engine, Settings → CI | Covered |
| Models & connections | Settings → Models | Covered |
| Git connections | Settings → Git | Covered. There is no agent git name/email (commit identity now comes from the credential), no Edit on a git connection row, no write-only GitHub credential with Keep existing / Remove, and no copy-clone-command action in this build. |
| Workflow & Tasks | Settings → Tasks | Covered |
| Sandbox & Infrastructure, Executors, Integrations & Auth | Settings → Sandboxes, Caches, Executors, Credentials | Covered |
| Access: sign-in card and system administrators | Settings → Access holds the "This browser" card (Log out). The System administrators card is gone with SET-3. | Covered |
| Config repo and Upgrade | Settings → Config repo, System → Upgrade | Covered (stubbed, see below) |
| Memory page from the rail (`/memory`) | `/memory` → Settings → Memory. Memory is not in the rail by default but can be pinned there. | Covered |

## Steps and outcomes

### User settings (Preferences and Access)

| Check | Steps | Outcome |
|---|---|---|
| Appearance | Chose Light, then Dark. `html[data-theme]` and `localStorage["grain.themeMode"]` followed. Reloaded and Dark was still checked. Restored Auto. | Pass |
| Time zone | Chose `Asia/Tokyo`. After a reload it was still selected. **No time on any page moved** (bug 4). After the fix, the Inbox's config-repo row printed in Tokyo time, then in UTC once UTC was picked. Restored the default. | Fixed |
| Task timeline order | Chose Oldest first, reloaded, still chosen. Restored Newest first. | Pass |
| Customize pinned items | Opened Customize from Preferences and from the rail. Pinned *Settings → Memory*, the *timeline* board and the *director* session, moved Director up and unpinned Inbox, then saved. The rail showed Director, Memory and timeline, in that order, and still did after a reload (`grain.pinned`). The pinned Memory link opened `/settings/memory`. Cancel discarded a draft unpin. Reset to default + Save put back just Inbox. | Pass |
| Nothing deployment-wide in Preferences | Preferences holds only appearance, time zone, timeline order and pinned navigation, and sends no `PUT /api/settings`. | Pass |
| Version display | The footer stamp `v1.2.0 · google · 66b743a · …` opens `/system/upgrade`. Settings → General → About shows the same stamp. | Pass |
| Log out | Settings → Access → Log out, then a reload showed "Enter access code", and `GET /api/config` answered 401. | Pass |
| Keyboard | Tabbing reaches the settings filter, every section in the nav, the appearance radio group (one stop), the time zone select, the timeline group and Customize. | Pass |

### Navigation, URLs and narrow windows

- Clicking every Settings section in the nav moved the URL through
  `/settings/access` … `/settings/preferences`. Back and Forward walked that
  history, and a reload kept the open section.
- System tabs (`health, logs, sandboxes, caches, top, pipeline, metrics,
  upgrade, power`) each set `/system/<tab>`, and Back returned to the
  previous tab.
- **Phone (390 px)**: Settings and System are drill-down lists with a ←
  back control, not horizontal tab strips. `document.scrollWidth` equalled
  the viewport on `/settings`, `/settings/sandboxes`, `/settings/git` and
  `/system/logs`, so nothing overflowed. Tablet `/settings/models` also had
  no overflow.

### General, Agents, Engine, CI, Tasks, Sandboxes, Caches, Credentials: validate, save, reload

Each box was given a value, tabbed out of and reloaded, then the original
value was put back:

| Box | Bad value → result | Good value → result |
|---|---|---|
| Environment name | — | `walk-test` saved, reloaded, cleared |
| Max agent turns | `-5` → "Couldn't save Max agent turns: maxAgentTurns cannot be negative", box reverts | `40` saved and reloaded, then `0` |
| Branch name template | `grain/no-id` → refused, reverts | `grain/{deployment}/task-{id}` saved and reloaded, then cleared |
| Deployment ID | `Bad ID!` → refused ("lowercase letters, digits…") | `walk-dep` saved, reloaded, cleared (placeholder shows the random ID) |
| Poll interval | `abc`, `0s` → refused | — |
| Max worker agents | `-1` → "must be at least 1" | `100000` accepted (no upper bound) |
| Merge batch size | `0` → blank, which uses grain's default (placeholder `3`) | — |
| Merge gather window | `-1m` → stored `-1m0s`, which turns the wait off as documented. `0s` → blank (default `5m0s`). | — |
| CI runs kept, CI queue age boost, job cache, artifacts | `nope` → refused. `-1` accepted for runs kept, as documented ("keeps every run"). | `123` and `999999` saved, then blanked back to their placeholders |
| Max unreviewed follow-up depth / per task | `-1`, `0` → refused, **named by API key** (bug 1) | — |
| Proxy cache max size | `-3` → "must be 0 (unset) or at least 1" | Actions cache `0` → unset |
| Sandbox vCPUs / memory | memory `-1` → "0 (unset) or at least 128" | — |
| Disk floor share, CPU overcommit | `2`, `-1` → refused, **named by API key** (bug 1) | — |
| GitHub host | `https://github.com` → refused ("not a URL") | — |
| Sandbox setup script | — | `echo walk-setup` saved with Save, reloaded, then cleared |

The section header's status line read "Unsaved changes in …" while a box
was dirty, "All changes saved" after a save, and "Couldn't save <label>"
with the server's reason after a refused save. A refused box reverts to
the stored value. The restart-required chip ("needs restart") and its
hint were shown on GitHub host, plain HTTP and the Kubernetes boxes. No
unrunnable value was stored in the demo, so that warning was not shown.

### Settings → Tasks: defaults reach the new-task form

Turned off "Queue new tasks immediately by default" and "Auto-merge new
tasks by default", then reloaded: both were still off. The New task form
then opened with *queue immediately* and *auto-merge once checks pass*
unchecked. Both were turned back on. The default capabilities (`self
debug`, `gcp key`, `gemini key`) and the build-default note ("Corp research
is on by default for this build…") are listed. The task fields card lists
the six demo fields with their kinds and defaults. Ticking **"grain CI is
the gate everywhere"** reported "All changes saved", but it was unticked
again after a reload (bug 3).

### Settings → Models (provider connections, default model, effort)

- **Test connection** on the seeded *Gemini 2.5 Pro* (Antigravity) answered
  "Connection valid", which the demo provides itself.
- **New connection** → Agent type *Antigravity* → Continue →
  Authentication *Gemini API key* (a fake value, set write-only) → Name
  `walk-gemini` → *Pin to a fixed list instead* → Advertised model
  `gemini-walk-model` → Save connection. The card appeared with "Advertises:
  gemini-walk-model".
- **Pickers**: the Default model options became `gemini-2.5-pro`,
  `gemini-2.5-flash` and `gemini-walk-model`. Setting `gemini-walk-model`
  as the default survived a reload. New task's model picker listed
  "Deployment default (gemini-walk-model)" and every advertised model.
- **Disable**: the card showed *disabled*, and Models showed "No enabled
  connection serves model gemini-walk-model. Tasks on the default model wait
  in the queue until one does." The warning survived a reload. New task
  then listed "gemini-walk-model (disabled connections only)". **Enable**
  cleared the warning. The default was set back to `gemini-2.5-pro`.
- **Remove** deleted the connection and its stored credential on one click
  with no confirmation (bug 2). After the fix, declining the prompt keeps
  the connection.
- Leaving the New-connection form after Continue, by closing the tab or
  reloading, left a disabled connection that advertises nothing ("no models
  — set one"). In-app leaving discards the draft (`ModelBackendPage`
  unmount). This is filed as a follow-up.

### Settings → Git

- Added a throwaway **plain git** connection (`git.walk.invalid`, PAT) and
  a **GitHub** connection scoped to `walk-org` (fake PAT). Both rows were
  there after a reload. The acme/widgets repository picker offered
  "walk-org on github.com (pat)".
- **Test** on the GitHub row reached api.github.com and reported "GitHub
  rejected the token (bad credentials)", which is right for a fake token.
  **Test** on the plain git row asked `https://git.walk.invalid/rate_limit`
  and reported "could not reach GitHub". The check probes the GitHub API
  whatever the connection's kind, which is filed as a follow-up.
- **Remove** had no confirmation (bug 2). Both throwaway rows were removed
  and the seeded `github.com` default was left in place.
- The commit-author card names each connection's identity ("grain agent
  <grain-agent@localhost> (asking github.com who credential "default" is:
  HTTP 401)").

### Settings → Credentials, Executors, Sandboxes, Caches

- On the `-secrets-store` demo, the Other secrets form set
  `walk-throwaway/token`. The value box is `type=password` and is cleared
  after Set. The row shows only the name and key, `GET /api/secrets`
  returns `{"name":"walk-throwaway","keys":["token"]}` and no value, and
  the request bodies were never logged. Replacing the value kept the one
  key. Delete secret asks first: dismissing kept it, and accepting removed
  it, confirmed after a reload. Without `-secrets-store` the pane says
  secrets are not available.
- The 26 capability sub-tabs list their configured state ("26 of 26
  configured"). The GCP project, service account and count boxes save on
  blur.
- Executors: Issue token `walk-throwaway`. The token is shown once with a
  Copy button and the `grain executor --token …` hint. Revoke asks
  ("Are you sure you want to revoke token walk-throwaway (Prefix: …)?"),
  and the row then reads Revoked, also after a reload.
- Sandboxes shows the budget (vCPU, memory, disk, overcommit, reserve,
  disk floor), the network select, the Kubernetes boxes with their
  needs-restart chips, and the setup script. Caches holds the proxy and
  Actions cache toggles and size boxes, and points at System → Caches.

### Settings → Config repo and System → Upgrade

- With no stub, the demo's Config repo says "not running inside a daemon
  that owns a config repository", and Upgrade says not available (neither
  `-upgrade-image` nor `-upgrade-src-dir`).
- With the state-repo stubs below, the alerts button read "Alerts: 1
  active, critical". The Inbox showed a *Settings are not being exported*
  group with "Settings have not been exported to grain/config since … :
  push rejected …", and its **Config repo** button opened
  `/settings/config-repo`. That pane showed the repository, branch,
  short head, working tree, the failure text and Export now. The
  sentence ignored the chosen time zone (bug 4).
- With the upgrade stubs below, the pane showed "Now running
  ghcr.io/bwsalmon/grain/grain:main", the branch box (latest / candidate /
  main), "wait until this deployment is idle", the Database backend card
  and the pointer to Settings → General → Updates. Typing `candidate` and
  pressing Upgrade sent `{"branch":"candidate","afterTasksFinish":false}`.
  That POST was intercepted and never reached the server. With no runs in
  flight there is no confirmation, by design (`UpgradePanel.jsx`).
  `/api/upgrade/release-notes` returns 404 on the demo, so no release
  notes were shown.

### Memory (`/memory` → Settings → Memory)

- The pane shows *This deployment* (memory agent, 271 characters, document
  `release-checklist`) and *acme/widgets* ("edited here", document
  `flaky-e2e`), plus a picker for *another repo*. Opening a document shows
  its summary and text.
- A correction was appended to the deployment's note. The header read
  "Unsaved changes in Memory". Then:
  - a rail click asked "You have unsaved changes here. Leave and discard
    them?", and dismissing stayed on the page;
  - Settings' own nav, the top-bar System button and a filter jump all
    asked the same;
  - Back and a reload raised the browser's `beforeunload` prompt, and
    dismissing kept the text.
- Save → "All changes saved". After leaving to General and coming back,
  the note read the correction back and named its writer "edited here".
  The original text was saved again and confirmed after a reload.
- Memory appears neither on `/repos/acme/widgets` nor anywhere in System.

### Console and network

Across the run the only failures were:

- `GET /api/repos/acme/widgets/github-branches` 404 on a task page (the
  demo has no GitHub);
- `GET /api/upgrade/targets` and `/api/upgrade/release-notes` 404 (no
  upgrader on the demo);
- `PUT /api/settings` 400 for each deliberately invalid value;
- `/api/tasks` requests aborted by navigation.

There were no uncaught page errors.

## Bugs found and fixed

### 1. A refused save named twenty-odd boxes by their API key

- **Reproduction**: on `/settings/tasks`, type `0` in *Max unreviewed
  follow-up depth* and press Tab. The header read "Couldn't save
  maxAutoSubmitDepth". The same happened for `maxAutoSubmitFanOut`,
  `sandboxDiskFloorShare`, `sandboxCpuOvercommit`, every CI retention box
  and others.
- **Cause**: `settingsPlan.js`'s `SETTING_LABELS` had no entry for boxes
  added since it was written.
- **Fix**: added each box's on-screen label.
- **Coverage**: `settingsPlan.test.js` reads every `data-setting-id` in
  `components/settings/*.jsx` and fails on one with no label. It fails on
  the base, listing the 22 missing keys. The e2e spec covers the Tasks
  case.

### 2. Remove on a provider or git connection deleted it, and its credential, on one click

- **Reproduction**: on `/settings/models` or `/settings/git`, press Remove
  on any connection. It was deleted at once, with no prompt.
- **Fix**: both now `window.confirm` with the connection's name, and do
  nothing if declined. This matches the Secrets panel and the executor
  token revoke.
- **Coverage**: unit tests for the declined and accepted paths
  (`ModelsSection.test.jsx`, `GitConnections.connections.test.jsx`), and
  the e2e spec (dismissing keeps both rows across a reload).
  `git-connections-multihost-walkthrough.spec.js` now accepts the prompt.

### 3. Settings → Tasks' "grain CI is the gate everywhere" saved nothing

- **Reproduction**: tick it. The request was `PUT /api/settings
  {"ignoreGitHubCI":true}`, the header read "All changes saved", `GET
  /api/settings` had no such field, and a reload showed the box unticked.
- **Cause**: the deployment-wide `Config.IgnoreGitHubCI` was removed in
  `a5c329900` in favour of each repository's CI mode, and `PUT
  /api/settings` silently ignores unknown fields.
- **Fix**: the box, its settings-filter entry and its label are removed.
  The card now says the gate is each repository's CI mode, set under
  Settings → Repositories or on the repository's page.
- **Audit**: every other `data-setting-id`, `saveChoice({…})` key and blur
  handler key in the settings sections was checked against
  `pkg/ui/settings.go`'s JSON fields, and none other is missing.
- **Coverage**: `TasksSection.test.jsx` and the e2e spec.

### 4. Preferences' time zone changed no time anywhere

- **Reproduction**: choose `Asia/Tokyo` in Settings → Preferences, then
  open the Inbox with a config-repo sync failure (stubbed). The time was
  still printed on the browser's clock.
- **Cause**: `TimeZoneProvider` was fed only `config.timeZone`, and nothing
  but the picker read `usePreferences().timeZone`. `docs/design/settings-panel.md`
  §6.2 names `TimeZoneContext` as its consumer, and SET-3 left that wiring
  out. The sync sentence also used a bare `toLocaleString()`.
- **Fix**: `AppContent` feeds `preferredTimeZone || config.timeZone` to
  `TimeZoneProvider` and `collectAlerts`. `stateSyncSentence` formats on
  that zone. The picker's unset option names the deployment's zone when it
  has one ("Deployment default (UTC)"), and its help no longer claims to
  move schedules, which keep their own zone.
- **Coverage**: `alerts.test.js`, `PreferencesSection.test.jsx` and the
  e2e spec (Tokyo, then UTC).

## Follow-ups proposed (not fixed here)

- **The plain-git connection Test probes the GitHub API.**
  `CheckTeamGitConnection` always calls `checkGitHubCredentialForHost`
  (`/rate_limit`), so a plain git host is reported as "could not reach
  GitHub". This needs a server-side reachability check for plain git
  hosts.
- **`PUT /api/settings` ignores unknown fields.** That is how bug 3 hid
  for a month. The server should report a field it does not know.
- **Abandoned New-connection drafts stay behind.** Closing or reloading the
  tab after Continue leaves a disabled connection that advertises nothing.

## Stubs and intercepted routes

Everything else was the real `grain demo` server and the built UI.

| Route | Why | Stub |
|---|---|---|
| `GET /api/config` (Config repo and time-zone checks only) | The demo has no state repository that could fail to export | Real response, plus `stateRepoSyncFailure: {repo: "grain/config", since: "2026-10-23T09:00:00Z", error: "push rejected …"}` (and `timeZone: ""` in the e2e spec) |
| `GET /api/state-repo` | Same | `{available: true, mode: "internal", repository: "grain/config", branch: "main", head: …, error: …}` |
| `GET /api/upgrade`, `GET /api/upgrade/targets` | The demo wires no upgrader | Idle image-mode upgrader on `ghcr.io/bwsalmon/grain/grain:main`; targets latest, candidate, main |
| `POST /api/upgrade` | Never actually upgrade | Intercepted in the browser and answered `202 {phase: "pulling", detail: "walk stub…"}`, so nothing reached the server |

The model-provider and git-host boundaries were not stubbed. The demo
answers its own provider tests. The GitHub test reached api.github.com
with a fake token and got 401. The plain-git test failed DNS on a
`.invalid` host. No real credential was stored or sent.

## Tests run

- `npx vitest run src/components/settings src/settingsPlan.test.js
  src/settingsIndex.test.jsx src/alerts.test.js src/components/SettingsPage.test.jsx
  src/components/InboxPage*`: all passed.
- `npx playwright test e2e/user-system-settings-walkthrough.spec.js
  e2e/provider-connection-lifecycle-walkthrough.spec.js
  e2e/git-connections-multihost-walkthrough.spec.js --project=chromium`:
  all passed.
- `e2e/screenshot.mjs` before (base `66b743a5d`) and after on
  `/settings/tasks`, `/settings/preferences`, `/settings/models` and
  `/settings/git`, at desktop and phone in light and dark. Bounding-box
  checks found the new Tasks note and the time zone select inside their
  cards at both widths and in both themes, with no horizontal overflow.
