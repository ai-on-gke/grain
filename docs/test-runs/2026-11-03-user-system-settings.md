# 2026-11-03: Settings walkthrough (layout, lock and save model, Preferences, Access, General, Config repo, Models, Agents, Memory, Tasks)

Task 2702 is a browser walkthrough of the Settings view. Its scope is the
same as the 2026-11-02 run (`2026-11-02-user-system-settings.md`, task
2404). It ran against real `grain demo` servers and a real `grain
daemon`, each with its built frontend, and Playwright drove them through
the visible controls. Git, Capabilities, Legacy capabilities, Engine, CI,
Sandboxes, Caches, System, Upgrade and Repositories belong to other
walkthroughs. They are only touched here where the lock model reaches
them.

**Result:** two bugs were found.

1. **Fixed on this branch.** The Config Repo Browser printed "exported
   1/1/1, 12:00:00 AM" until the export loop had finished its first
   cycle.
2. **Filed as a follow-up.** Signing in on the access-code page always
   lands on `/`, so the deep link that led there is lost.

Every other check passed.

## Commit and environment

- **Commit tested:** `0fdeff3b` (`main`, "Merge pull request #540 from
  ai-on-gke/grain/049f1e09/task-2524-route-each-run-s-host-work-to"). The
  fix below was then checked on top of it.
- **Build:** `npm ci && npm run build` in `ui/`. Then Playwright's
  `e2e/global-setup.js` ran `go build` for the `grain` binary, which
  embeds the UI. About reads `v1.2.0 · 0fdeff3-dirty`.
- **Servers.** Each ran over its own throwaway store, on a port the kernel
  picked:
  - **Demo, default** (`e2e/fixtures.js` `demoServer`): layout, filter,
    save model, Preferences, Access, General, Agents, Tasks and Memory.
  - **Demo, `-agent-paused -model-catalog 127.0.0.1:18099
    -model-discovery-interval 5s`:** Models.
  - **Demo, `-external-config-repo
    https://github.com/acme/grain-config.git`:** external mode.
  - **A real `grain daemon`** (`e2e/realdaemon.js` `startDaemon`, with
    `directConfigWrites: false`, `-sandbox-port-base=0` and a fake `agy`)
    for the internal config repo, the settings lock, the Config Repo
    Browser and the held config pull request.
- **Browser:** Chromium from `/var/cache/playwright`.
  - Widths: 1280×860 (1280×600 for the `#card` check), 800×1000 for tablet
    and 390×844 for phone.
  - Time zone: UTC, except America/Los_Angeles for the Preferences checks.
- **Toolchain:** Debian, Go 1.26.10, Node 20.
- **Gemini key:** `/home/debian/.gemini-api-key` is absent from this
  sandbox, so no real model call was made.
- **Scripts:** ad hoc spec files (`ui/e2e/zz-walk-*.spec.js`) running on
  the repository's own fixtures. They were deleted afterwards and are not
  committed, and no screenshot or trace is committed either.

## Stubs and intercepted routes

| Boundary | How | Why |
|---|---|---|
| Model provider (listing) | A loopback Node server on `127.0.0.1:18099` answered `GET /v1/models` with `claude-walk-discovered-1` and `-2`. The Models demo used it through `-model-catalog`, and it served 11 listings. | Discovery needs a provider, and there is no key here. |
| `PUT /api/settings` (Retry only) | `page.route` answered the **first** PUT with `503 stubbed outage`, and let every later one through. | To raise "Couldn't save Max agent turns" with Retry deterministically. |
| `GET /api/settings` (restart chip only) | `page.route` added `pendingRestart: ["environmentName"]` to the real answer. | A demo has no running configuration to differ from. |
| Identity (`GET /api/config`, badge only) | `page.route` added `authMethod: "iap", actor: "walker@example.com"` to the real answer. | The demo signs in with an access code, and a verified identity needs an IAP issuer. |
| Config repo | **Not stubbed.** External mode is the demo's own `-external-config-repo`, which nothing fetches. Internal mode and the lock ran on the real daemon's own `grain/config`. | — |
| Test Connection, GitHub, upgrade | **Not stubbed.** The demo answers "Connection valid" itself, and nothing here reached GitHub or an upgrade service. | — |

## Steps and outcomes

### Layout and paths (default demo)

- **Settings entry:** the header rail has one Settings entry, the gear
  (`button` "Settings", `top-bar-settings`), and it opens
  `/settings/models`. On a phone the bar carries it as `phone-settings`.
  Pass.
- **Tasks:** Tasks in the nav, then a reload, stays on Tasks. Back
  returns to Models and Forward to Tasks. Pass.
- **Scrolling to a card:** at 1280×600, `/settings/tasks#task-fields`
  scrolls Task fields to top=129, just under the section header. Pass.
- **Tablet (800 px):** the nav is a "Section" select. Choosing Models goes
  there, and `scrollWidth` is 800. Pass.
- **Phone, the section list:** a bare `/settings` is the grouped list:
  Models & sandboxes, Secrets & access, This browser, System / Operations
  and Managed in config repo. Pass.
- **Phone, one section:** Agents opens alone under the "All settings" back
  arrow (`scrollWidth` 390), and the arrow returns to the list. Pass.
- **Old links:**
  - `/admin` → general, `/admin/workflow` → tasks.
  - `/admin/upgrade` and `/system/upgrade` → upgrade.
  - `/memory` → memory.
  - `/config?path=tables/grain_config.json` →
    `/settings/config-repo/tables/grain_config.json`.

  Pass.
- **`/admin/executors` and `/system/executors`** now land on **Settings →
  Hostds**, not Models. That is deliberate (`b548f016`, "add Settings →
  Hostds for the hostd registry", with `paths.test.js` updated), so the
  brief is out of date there.

### Filter

- `/` focuses "Filter settings". Pass.
- "turns" lists *max agent turns* under Agents. Choosing it goes to
  `/settings/agents#maxAgentTurns`, focuses the box and outlines it (one
  `.setting-highlight`). Pass.
- Labels and keywords match by section:

  | Query | Matches |
  |---|---|
  | auto-merge | Tasks |
  | time zone | Preferences |
  | theme | appearance (Preferences) |
  | reasoning | default model (Models) |
  | deployment id | Agents |
  | lock | Lock settings in UI (General) |
  | pinned | sidebar (Preferences) |

  Pass.
- "zzqqxx" says "No setting matches 'zzqqxx'.", and Escape restores the
  groups. Pass.

### Save model (default demo)

| Check | Outcome |
|---|---|
| Checkbox (Show closed tasks by default) | Clicking sent one PUT, and the value read back after a reload. It was restored. Pass. |
| Text (Environment name) | Nothing was sent while typing, and one PUT on Enter. The tab title became "walk-env — grain", and that read back after a reload. Clearing it saved on blur, and the title went back to "grain". Pass. |
| Out of range | `-5` in Max agent turns gave "Couldn't save Max agent turns". **No PUT** was sent, and the box went back to 0. Saving 0 cleared it ("All changes saved"). `-1` in Max unreviewed follow-up depth was refused the same way. Pass. |
| Couldn't save + Retry (stubbed 503) | The header read "Couldn't save Max agent turns" with one Retry, and the box rolled back to 0. Retry sent the PUT to the real server, and 12 read back after a reload. Restored to 0. Pass. |
| Read-back of the server's answer (Engine → Poll interval) | `1m` + Enter read back as `1m0s`, also after a reload. Restored to `10s`. Pass. |
| Save card (Agents → Standing instructions) | Blur sent nothing. The header read "Unsaved changes in Standing instructions", and the nav carried one dirty dot. Changing section and clicking the rail's Inbox each asked "You have unsaved changes here. Leave and discard them?", and dismissing stayed put. Discard restored the box. Save sent one PUT, which read back after a reload. Once clean, Tasks and Inbox asked nothing. Pass. |
| Restart needed (stubbed) | The chip shows in the section header. Focus + Enter opens `/system/power`. Pass. |

### Preferences (`/settings/preferences`, browser in America/Los_Angeles)

- **Appearance:** Dark and Light set `html[data-theme]`, and Dark survived
  a reload. Auto followed the browser's light scheme. Pass.
- **Time zone:**
  - With none chosen, the first option is "Browser default
    (America/Los_Angeles)". The list marks "America/Los_Angeles (this
    browser)".
  - `/tasks/1` printed "11/2/2026, 3:36:49 PM".
  - With Asia/Tokyo it printed "11/3/2026, 8:36:49 AM", 17 hours later as
    it should be. The choice survived a reload, and was put back to the
    browser default.

  Pass.
- **Task timeline:** Oldest first survived a reload, and was restored.
  Pass.
- **Customize sidebar:**
  - Pinned through Type → Item → Pin item: "settings → memory", "system →
    logs", a board and the "director" session. Move up, then Save.
  - The rail read "inbox / memory / logs / director / board", the same
    after a reload.
  - The pinned Memory opened `/settings/memory`, and Logs opened
    `/system/logs`.
  - Unpinning everything and reloading emptied the pinned group.

  Pass.
- **Nothing deployment-wide:** no non-GET request was sent during any
  Preferences step. What remains in `localStorage` is `grain.pinned` and
  `grain.storageVersion`. Pass.

### Access

- **Access code:** a fresh browser context was signed in by typing the code
  into "Enter access code" → Continue. The This browser card says "Signed
  in with the access code." There is no email badge in the rail, which is
  right for this sign-in. Log out returned to "Enter access code" on the
  same URL, and a deep link to `/settings/general` afterwards showed the
  prompt too. Pass.
- **Verified identity (stubbed):** the rail badge reads
  `walker@example.com`, and the card says "Signed in through IAP as
  walker@example.com." Pass.
- **Under external config:** Access has exactly one control, Log out,
  and it is enabled. On the daemon with the lock on, Log out stays the
  one enabled control too. Pass.
- **Bug, filed as a follow-up:** after Continue the browser is sent to
  `/`, not to the `/settings/access` deep link it started on (see Bugs).

### General (default demo, and the daemon)

- **Deployment:** covered by the checkbox and text cases above.
- **Settings lock** ("Lock settings in UI (require config repo PRs)"):
  - On the demo it is disabled: "unavailable without a config
    repository: settings are always edited directly here."
  - On the daemon it is enabled, and clicking it saved and read back after
    a reload.
- **Updates:** After an upgrade → Check the upgrade ("File it and queue it
  (default)"), Hold the queue until the check is done, and Automatic
  upgrades with its channel and time of day (24-hour, UTC).
- **About:** "v1.2.0 · 0fdeff3-dirty · 2026-11-03 02:22Z", the same text
  as the build stamp at the foot of the rail. Pass.

### Config repo and lock (real daemon; external demo)

- **Internal panel:**
  - It reads "Internal (grain/config on this host — default) …". The head
    chip reads `grain/config | main | 634932d7`, with the working tree's
    path and Export now.
  - The panel offers the mode switch (Repository URL, Branch, Personal
    access token (optional), Use external config repository) and the
    secrets key card (public key, and the private key's path to back up).
  - The backup note says "No external config repository is configured.
    grain backup protects it …".
  - Pass.
- **Lock on:**
  - Every control in General is disabled except the lock itself, which is
    a bootstrap setting.
  - Agents, Tasks, Memory, Caches and Capabilities have every control
    disabled.
  - Engine leaves Max worker agents editable. That is a bootstrap field by
    design (`SettingsPage.jsx` `configLocked`, and
    `pkg/ui/config_locked_fields_test.go`).
  - Each section leads with "View in config repo → <path>":
    - General, Agents, Engine and Caches name `tables/grain_config.json`.
    - Tasks also names `tables/custom_field.json`.
    - Memory names `memory/deployment.md`.
    - Capabilities also names `tables/api_scope_set.json`.
  - The links point at `/config?path=…`, which opens
    `/settings/config-repo/<file>`.
  - Pass.
- **Locked controls and the keyboard:** on the locked Tasks section, Tab
  stops only on the two "View in config repo" links. No disabled chip or
  select takes focus. It was the same on General and Tasks under the
  external demo. Pass.
- **Config Repo Browser** (`/settings/config-repo/tables/grain_config.json`
  and `/config?path=…`):
  - It lists README.md, schema-version, `settings/*.yml` and
    `tables/*.json` with their sizes, and shows the chosen file in a
    `<pre>`.
  - README.md renders as Markdown, with headings "grain state", "What is
    here", "Changing something" and "Secrets are not here".
  - The stamp read **"showing cd9b0a8, exported 1/1/1, 12:00:00 AM"**.
    That is a bug, fixed on this branch (see below). With the fix it reads
    "showing 76cac44".
- **A refused write offers a held config pull request:**
  - With the lock on, `PUT /api/settings {"maxAgentTurns":7}` answered 409
    with the `tables/grain_config.json` patch.
  - In the UI, Processes → + New → template → Add template opened the
    refusal dialog: "This setting lives in grain/config (main) and is
    changed with a config pull request there …". It carried "Open in
    Config Browser" links and the diff.
  - Create Config PR answered "Opened config pull request #2 as task 2. It
    waits for a person to submit it."
  - Pass.
- **A later save succeeds:**
  - Unlocking from General saved ("All changes saved"). Agents' Max agent
    turns was editable again, and its "View in config repo" link had gone.
  - The Settings header carried no leftover refusal.
  - Pass.
- **External mode (demo):**
  - Every managed section (General, Agents, Tasks, Memory, Engine, Caches,
    Capabilities) has every control disabled, under "Configuration is
    read-only and managed in https://github.com/acme/grain-config.git
    (main). …".
  - There the "Edit tables/grain_config.json in acme/grain-config (main)"
    link goes to the repository's own GitHub blob, because the config
    repository lives there.
  - On Models only Test Connection and Edit stay enabled.
  - `PUT /api/settings` answered 409 with the patch to commit.
  - Pass.
- **Not re-run today:** the failing-sync alert and its Inbox row. Raising
  them takes a five-minute grace on a daemon switched to a broken external
  remote, and this run's time went to the lock checks above. The 11-02
  run (`2026-11-02-user-system-settings.md`) covered them on an external
  remote it renamed away.

### Models (`-agent-paused`, with the provider stub)

- **Paused default:**
  - "paused until 9:38:47 AM (about 5h 59m) — antigravity: usage limit
    reached …".
  - The pane warned "Every enabled connection that serves model
    gemini-2.5-pro is paused. …".
  - Resume sent `DELETE /api/pause?connection=gemini-pro`. The warning
    went, and stayed gone after a reload.

  Pass.
- **Test Connection:** "Connection valid". Pass.
- **Disable and Enable:** Disable warned "No enabled connection serves
  model gemini-2.5-pro. Tasks on the default model wait in the queue until
  one does.", which survived a reload. Enable cleared it. Pass.
- **Edit:** it opens the connection form in the overlay, and "← Models"
  closes it. Pass.
- **Unfinished new connection:**
  - The agent types are antigravity, claude, codex and custom command.
  - Claude → Continue stores a disabled draft.
  - "← Models" asked the general "You have unsaved changes here. Leave and
    discard them?", not a Remove prompt. Accepting sent `DELETE` for the
    draft, and after a reload only the original connection was left.
  - Pass.
- **Finished new connection:** Claude → OAuth token → "Walk Claude". The
  form listed both discovered models. After a reload the card read
  "Advertises: claude-walk-discovered-1, claude-walk-discovered-2 (every
  model its provider lists)". Pass.
- **Pickers:**
  - The session picker (`/session/new`) offered "Deployment default
    (gemini-2.5-pro) | gemini-2.5-pro | gemini-2.5-flash |
    claude-walk-discovered-1 | claude-walk-discovered-2".
  - The Default model picker offered the same models after "None —
    provider default (unset)".
  - New task, New schedule and New template were not re-checked today.
    The 11-02 run found them identical.
- **Default model:** claude-walk-discovered-2 read back after a reload. A
  default nobody advertises (`retired-model-9`, set over the API) is
  warned about by name. It was restored to gemini-2.5-pro. Pass.
- **Reasoning effort:** the options are model default / low / medium /
  high. *high* read back after a reload, and was restored. Pass.
- **Remove:** it asked "Remove the connection Walk Claude? A credential
  stored for it is deleted with it …". Dismissing kept it. Accepting
  removed it, which a reload confirmed. Pass.
- **Where each agent CLI runs:**
  - claude: host (not found on PATH), with the container-on-bare-machine
    explanation.
  - codex: host.
  - agy: `/opt/agy/bin/agy`, set by -agy-run.
  - Custom commands: host.

  Pass.

### Agents

- **Branch name template:** `grain/no-id`, `bad branch {id}` and
  `grain/task-{id}.lock` were each refused with "Couldn't save Branch name
  template". `walk/{deployment}/t-{id}` saved and read back after a
  reload, and was restored to empty. Pass.
- **Deployment ID:** `Bad ID!` was refused. The helper still ends "Leave
  empty for this deployment's random ID, 75032fd8", and the placeholder is
  still `75032fd8`, so the sample ID stays visible. Pass.
- **Commit identity:** its "Settings → Git" link opens `/settings/git`.
  Pass.

### Memory (`/settings/memory`)

- It shows the deployment's note (written by the memory agent) and the
  repository's, with their documents.
- **A correction:** appending "WALK correction 2702." gave "Unsaved
  changes in Memory", and nothing was sent.
  - The rail's Inbox asked the discard question.
  - Back and a reload each raised `beforeunload`. Dismissing kept the edit.
  - Save sent `PUT /api/memory`. Reopening through Inbox → Memory read it
    back.
  - Saving the original text restored it, which a reload confirmed.

  Pass.
- Memory is not in the rail unless pinned (the pinned case is above), and
  `/repos/acme/widgets` does not mention it. Pass.

### Tasks

- **Saved defaults:**
  - Queue new tasks immediately by default: off.
  - Auto-merge new tasks by default: off.
  - Max unreviewed follow-up depth: `-1` was refused by name, and 3 saved.

  All three read back after a reload. Default legacy capabilities lists
  self debug, gcp key and gemini key.
- **New task:** + New task then opened with "Auto-merge" and "Queue
  immediately" both unchecked. The defaults were restored. Pass.
- **Task fields "no longer offers" link:** not re-run today. It needs a
  config-repo commit withdrawing an enum value, which the 11-02 run made
  through an external remote. That run followed it into
  `custom.stage:legacy`.

### Console and network

There were no console errors or failed requests beyond the expected
ones:

- the stubbed 503;
- the 409s from locked or external writes, and from the refused template;
- the 400s from refused values;
- two 404s while `/tasks/1` was open on the demo, as on earlier runs (the
  demo has no GitHub data).

## Bugs

### 1. The Config Repo Browser printed a year-1 export time (fixed)

- **Reproduction:**
  1. Start a fresh daemon, or the e2e `startDaemon`.
  2. Open Settings → Config repo → Show files before the export loop has
     finished its first cycle.
  3. The caption reads "showing cd9b0a8, exported 1/1/1, 12:00:00 AM".
- **Cause:** `ui.StateRepoStatus.SyncedAt` is a `time.Time` tagged
  `json:"syncedAt,omitempty"`. `omitempty` never omits a struct, so the
  zero time went out as `0001-01-01T00:00:00Z`.
  `StateRepoBrowser.jsx` prints any `syncedAt` it is given, while the
  field's own comment says it is "zero on a deployment whose first cycle
  has not finished".
- **Fix:** the tag is now `omitzero`, so before the first cycle the
  caption is just "showing <head>".
- **Regression test:** `TestStateRepoStatusOmitsASyncThatHasNotHappened`
  in `pkg/ui/staterepo_test.go`. It fails without the fix.
- **Checked in the browser:** re-run on the daemon, the caption reads
  "showing 76cac44".

### 2. Signing in with the access code loses the deep link (follow-up)

- **Reproduction:**
  1. Open `/settings/access`, or any deep link, signed out.
  2. Type the access code and press Continue.
  3. The browser lands on `/`.
- **Cause:** `handleAccessCodeLogin` in `pkg/ui/accesscode.go` always
  answers `303 → /`.
- **Why it is not fixed here:** fixing it means carrying a return path
  through the form and redirecting to it, which is open-redirect-sensitive
  code. The file is on the trust-boundary list, so the fix was filed as a
  focused follow-up rather than made in this auto-merged walkthrough.

## Observations (not changed)

- **The brief is out of date in three places:**
  - the old executor links land on Hostds;
  - the gear is a button;
  - external mode links to the external repository's own host, not the
    in-app Config Repo Browser.
- **Max agent turns still has no upper bound** (noted since 10-30).
- **The connection form is not deep-linkable** (as on 11-02).
