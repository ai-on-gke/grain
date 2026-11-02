# One Settings panel, a status-only System, Processes, and no user tile

Status: design, for the owner's review. Written for task 4881 (brief
filed by task 4530; replaces task 4876). Every choice the owner has to
confirm is marked **Decision:** with a recommended answer. Submitting
this doc accepts every recommendation that is not changed in review.

The three stages in §7 carry requirement IDs (`{#SET-1}` to `{#SET-3}`,
docs/design-docs.md). Once this doc merges, the design planner files one
implementation task for each of them, and those tasks merge without
asking again. Every other section is the specification the stages build
to. Those sections are not requirements of their own and carry no ID.

Baseline: `main` at e1efc3e (10-20). The doc designs against the tree
as it will be once the single-tenant reset has finished landing
(#4862–#4864 drop the team table and the team columns,
docs/single-tenant-reset.md). #4880 removes the remaining admin gates
(`ui/src/admin.js`, the `isAdmin` props). Everything in this doc assumes
both have happened:

- there are no teams;
- there are no users;
- the access code is the only credential, and whoever holds it is the
  owner;
- every control is always shown.

Where the tree at e1efc3e still has a team or admin remnant that this
design deletes, §1.5 and §6 say so.

**Scope.** The frontend only (`ui/`). No API endpoint, model field or
schema changes. The exceptions are the deletions this doc lists. None of
them removes an endpoint; at most the UI stops calling one. So no stage
moves a route in `pkg/ui/server.go`, and none changes
`tests/security/routes_test.go` or
`.grain/baselines/route-authorization.txt` (§7). Every new path
(`/settings/...`, `/system/...`, `/processes/...`) is a client-side
route. The daemon already serves `index.html` for any non-`/api` path,
so the server needs nothing new for them.

---

## 1. Inventory

Kinds:

- **D**: deployment-wide setting;
- **P**: preference of this browser;
- **R**: per-repository setting;
- **S**: status or operation.

"Where now" names the component and, for System, the `ADMIN_TABS` id
(`AdminOverlay.jsx`). The route is the one that opens it today: both
`/admin/<tab>` and `/system/<tab>` parse to the same overlay
(`paths.js` `parseSegments`), and `buildPath` writes `/admin/<tab>`.

### 1.1 System overlay, settings tabs (`AdminOverlay.jsx`)

| Setting or group | Where now | Route | API | Kind |
|---|---|---|---|---|
| Environment name | General & Deployment (`general`) | `/admin` | `GET`/`PUT /api/settings` (`environmentName`) | D |
| Show closed tasks by default | `general` | `/admin` | `/api/settings` (`showClosedByDefault`) | D |
| System Administrators (`AdminAccountsSection`) | `general` | `/admin` | `/api/settings` (`adminAccounts`) | D, **deleted** by #4880 (landed) |
| Max agent turns | Agents & Task Branches (`agents`) | `/admin/agents` | `/api/settings` (`maxTurns`) | D |
| Standing instructions for every run | `agents`, "Prompt extension" | `/admin/agents` | `/api/settings` (`promptExtension`) | D |
| Branch name template, Deployment ID | `agents`, "Task branches" | `/admin/agents` | `/api/settings` (`branchTemplate`, `deploymentId`) | D |
| Commit identity (pointer text only) | `agents`, "Commit identity" | `/admin/agents` | none | D |
| Provider connections | Models & connections (`models`), `DeploymentConnectionsPanel` → `ModelBackendPage` | `/admin/models` | `GET`/`PUT /api/deployment`, `PUT`/`DELETE /api/deployment/agent-backends/{id}`, `PUT`/`DELETE /api/agent-backends/{id}/credential`, `/api/agent-backends/{id}/codex-auth`, `GET /api/agent-models` | D |
| Default model, per-model settings and pauses, reasoning effort | `models`, `TeamModelsPanel` | `/admin/models` | `PUT /api/deployment` (`defaultModel`, `modelSettings`), `/api/settings` (`geminiEfforts`, `codexEfforts`), `/api/pause` | D |
| Git connections, git identity | Git connections (`git`), `TeamGitPanel` | `/admin/git` | `GET`/`POST /api/deployment/git-connections`, `DELETE /api/deployment/git-connections/{cred}`, `POST .../check`, `GET /api/git-ambient-sources`, `PUT /api/deployment/repos/{owner}/{name}/git-connection` | D |
| Task defaults (approved by default, default capabilities, newest first, follow-up depth and count) | Workflow & Tasks (`workflow`) | `/admin/workflow` | `/api/settings` | D |
| Task fields (custom fields) | `workflow`, `CustomFieldsSection` | `/admin/workflow` | `PUT /api/deployment` (`customFields`) | D |
| Relay (URL, audiences, token) | Access (`access`), `RelayPanel` | `/admin/access` | `GET`/`PUT /api/relay-settings`, `POST /api/relay-settings/test` | D |
| Poll interval | Engine & Concurrency (`engine`) | `/admin/engine` | `/api/settings` (`pollInterval`) | D |
| Merge queue: max merges per repo branch | `engine` | `/admin/engine` | `/api/settings` | D |
| CI queue: jobs per run, age boost, reserve after | `engine`, "CI queue" | `/admin/engine` | `/api/settings` | D |
| CI history: runs kept, step output kept/age, output per run | `engine`, "CI history" | `/admin/engine` | `/api/settings` | D |
| CI job cache: size, entries, age | `engine`, "CI job cache" | `/admin/engine` | `/api/settings` | D |
| CI artifacts: kept, age, per run, largest | `engine`, "CI artifacts" | `/admin/engine` | `/api/settings` | D |
| Sandbox backend, vCPUs, memory, disk, network | Sandbox & Infrastructure (`sandbox`) | `/admin/sandbox` | `/api/settings` | D |
| Sandbox budget: CPU/memory overcommit, host memory reserve, disk floor share and minimum | `sandbox`, "Sandbox budget" | `/admin/sandbox` | `/api/settings` | D |
| Custom sandbox image | `sandbox` | `/admin/sandbox` | `/api/settings` (`sandboxImage`) | D |
| Proxy cache max size | `sandbox`, "Proxy cache" | `/admin/sandbox` | `/api/settings` | D |
| Actions cache max size | `sandbox`, "Actions cache" | `/admin/sandbox` | `/api/settings` | D |
| Remote executors: connected list, join tokens | Executors (`executors`), `ConnectedExecutors`, `ExecutorsPanel` | `/admin/executors` | `GET /api/executors`, `GET`/`POST /api/executors/tokens`, `POST .../{id}/revoke`, `DELETE .../{id}` | D (tokens) and S (connected list) |
| GitHub host | Integrations & Auth (`integrations`) | `/admin/integrations` | `/api/settings` (`githubHost`) | D |
| GCP project, service account email, account count | `integrations`, "GCP" | `/admin/integrations` | `/api/settings` | D |
| Capabilities and their credentials (gcp-key, gemini-key, GitHub tokens, agent keys) | `integrations`, `CapabilitiesPanel` | `/admin/integrations` | `POST /api/capabilities/{id}/check`, `/api/gcp-keys*`, `/api/github-tokens*`, `/api/agent-keys*`, `/api/codex-auth` | D |
| Secrets | `integrations`, `SecretsPanel` | `/admin/integrations` | `GET /api/secrets`, `PUT`/`DELETE /api/secrets/{secret}[/{key}]` | D |
| Config repo (state repo, secrets repo and key, sync, reset notice) | Config repo (`state`), `StateRepoPanel` | `/admin/state` | `GET /api/state-repo`, `POST /api/state-repo/{sync,secrets-key,secrets-repo,reset-notice}`, `GET /api/state-repo/file[s]` | D |
| Upgrade check mode, "upgrade check pauses the pipeline" | Upgrade (`upgrade`), `UpgradePanel` | `/admin/upgrade` | `/api/settings` (`upgradeCheckMode`, `upgradeCheckPausesPipeline`) | D |

### 1.2 System overlay, status and operations tabs

| View | Where now | Route | API | Kind |
|---|---|---|---|---|
| Logs | `logs`, `LogsPage` | `/admin/logs` | `GET /api/logs`, `GET /api/logs/{source}` | S |
| Sandboxes (health, persistent sandboxes, lifetimes, storage) | `sandboxes`, `SandboxHealthPage` (+ `PersistentSandboxesPanel`, `SandboxLifetimePanel`, `SandboxStoragePanel`) | `/admin/sandboxes`, and the `/sandboxes` view | `GET /api/sandboxes`, `GET /api/persistent-sandboxes`, `.../{name}/logs`, `POST .../{name}/{action}` | S |
| Caches (proxy and Actions caches: contents, flush) | `caches`, `CachesPage` (+ `PackageCachePanel`) | `/admin/caches`, and the `/caches` view | `GET /api/package-cache`, `POST /api/package-cache/flush`, `GET /api/actions-cache`, `POST /api/actions-cache/flush` | S |
| Top | `top`, `TopPage` | `/admin/top` | `GET /api/host/top` | S |
| Pipeline pause | `pipeline`, `PipelineControl` | `/admin/pipeline` | `GET`/`POST`/`DELETE /api/pipeline-pause` | S |
| Power: restart, reboot, shut down | `power` | `/admin/power` | `POST /api/host/{restart,reboot,shutdown}` | S |
| Upgrade: targets, release notes, run, schedule | `upgrade`, `UpgradePanel` | `/admin/upgrade` | `GET`/`POST /api/upgrade`, `GET /api/upgrade/{targets,release-notes}`, `DELETE /api/upgrade/scheduled`, `GET /api/restart-watch` | S |
| Database (SQLite → Postgres) | `upgrade`, `UpgradePanel` "Database" | `/admin/upgrade` | `GET /api/database`, `POST /api/database/{test,upgrade}`, `POST /api/upgrade/postgres` | S |
| Metrics, release notes | `MetricsOverlay` (+ `ReleaseNotesPanel`), sidebar footer | `/metrics` | `GET /api/metrics`, `/api/release-notes*` | S |
| Health (standing alerts) | `AlertsButton` in the page header rail (`alerts.js` `collectAlerts`) | none | `GET /api/config` and what it carries | S |
| Merge re-checks, CI | the CI view | `/ci`, `/ci/<run>` | `/api/ci/*`, `/api/merge-batches` | S, unchanged by this design |

### 1.3 Personal preferences (`SettingsOverlay.jsx`)

| Setting | Where now | Route | Storage | Kind |
|---|---|---|---|---|
| Username | "User identity" | `/settings` | `localStorage["grain.username"]`, sent as `X-Grain-User` | P, **deleted** (§6) |
| Preferred time zone | "Preferred time zone" | `/settings` | `localStorage["grain.userTimeZone"]` (`UserContext`) | P |
| Appearance (auto, light, dark) | "Appearance" | `/settings` | `localStorage["grain.themeMode"]` | P |
| Task timeline order | "Task timeline" | `/settings` | `localStorage["grain.timelineOrder"]` | P |
| Pinned navigation | "Sidebar" → `CustomizeSidebarOverlay` | `/settings` (and the sidebar's "Customize") | `localStorage["grain.pinnedNav.<user>.default"]` | P |
| Hidden sidebar sections | "Sidebar" → `CustomizeSidebarOverlay` | `/settings` (and the sidebar's "Customize") | `localStorage["grain.sidebar.hidden"]` | P |
| Log out / "Signed in through IAP as" | "Account" | `/settings` | `POST /api/auth/logout` | S |
| Version | "Version" | `/settings` | `GET /api/config` (`version`) | S |

### 1.4 Memory

| View | Where now | Route | API | Kind |
|---|---|---|---|---|
| Deployment memory and each repo's memory (layers and documents) | `MemoryPage` → `MemoryPanel`, team-section sidebar entry (`Sidebar.jsx` ~1270) | `/memory` | `GET`/`PUT /api/memory`, `GET`/`PUT /api/memory/documents`, `GET /api/memory/documents/{name}` | D (each repo's layer is R content edited from the same page) |

### 1.5 Per-repository settings (`RepoPage.jsx` tabs)

All of these are kind R and live under `/repos/<owner>/<name>` (tab
state is local; see §2.3).

| Setting | RepoPage tab | API |
|---|---|---|
| Repo config: base branch, targets, defaults, git connection | Overview / Advanced | `GET`/`PUT`/`DELETE /api/deployment/repos/{owner}/{name}/config`, `PUT .../git-connection`, `GET`/`PUT /api/repos/{owner}/{name}/settings` |
| CI mode, ignore GitHub CI, CI capabilities, unfixable checks, workflows | CI | `/api/repos/{o}/{n}/{ci-mode,ignore-github-ci,ci-capabilities,unfixable-checks,workflows}` |
| Require branch up to date, merge rules | Merge queue | `/api/repos/{o}/{n}/require-branch-up-to-date` |
| Default capabilities, prompt extension, setup command, work ownership | Agents | `/api/repos/{o}/{n}/{capabilities,prompt-extension,setup-command,work-ownership}` |
| Watch default branch, branches, ports | Branches | `/api/repos/{o}/{n}/{watch-default-branch,branches}` |
| Release workflow, release secrets, qualification plan, lifecycle (`RepoReleaseWorkflowSettings`, `RepoReleases`) | Releases | `/api/repos/{o}/{n}/{release,release-creation,release-secrets/*,qualification-plan,lifecycle}` |
| Declarations (read `.grain/` on/off, status) | Advanced | `GET`/`PUT /api/repos/{o}/{n}/declarations` |
| Settings preview | (all) | `POST /api/settings/plan` |

### 1.6 Team and admin leftovers on `main` at e1efc3e

| Leftover | Where | Fate |
|---|---|---|
| Bordered "Team-scoped section" box | `Sidebar.jsx` ~1032, `data-testid="team-section"` | deleted; its entries join the flat list (§4) |
| `team-selector`, `team-settings-button` | `PhoneNav.jsx` `keepsDrawerOpen` | deleted (nothing renders them any more) |
| `#team-selector` | `style.css` ~987 | deleted |
| `fallbackTeam: "default"` and the legacy-pin migration call | `App.jsx` ~297–320 | replaced by the §6.3 migration |
| "switching teams" comment and logic | `App.jsx` ~1623 | the comment and any branch on team go |
| `team: team \|\| "default"` on `POST /api/console-sessions` | `App.jsx` ~1706 (`createStandingSession`) | the field is dropped from the body |
| `TeamGitPanel`, `TeamModelsPanel` | used by `DeploymentConnectionsPanel` | renamed and moved (§7 stage a) |
| `DEPLOYMENT_TEAM`, `teamPath`, `teamData`, `reloadTeam`, `saveTeamChoice` | `DeploymentConnectionsPanel.jsx` | renamed to deployment terms |
| `pinnedItemTeam`, `migrateLegacyPinnedItems`'s per-team split, `PIN_SCOPE` | `pinnedNav.js`, `PinnedNavContext.jsx` | replaced (§6.3) |
| `team` on boards, sessions, schedules, templates and suites in UI code | various | read nowhere; deleted where still read |
| `AdminAccountsSection`, `adminRemovalWarning`, `actsAsSystemAdmin`, `isAdmin` props, `visibleTabs` filter | `AdminOverlay.jsx`, `admin.js`, `alerts.js`, `memory.js`, `InboxPage.jsx`, `CachesPage.jsx`, `App.jsx` | gone with #4880 (`AdminOverlay.jsx` and `alerts.js`, `memory.js` and `InboxPage.jsx`'s checks had already gone before it) |

---

## 2. The split

### 2.1 System: status and operations only

System keeps every row of §1.2, plus Health and Metrics, and nothing
else. Its tabs, in this order:

| Tab | Path | Content |
|---|---|---|
| Health | `/system` (default), `/system/health` | **New view, no new API:** the alerts `collectAlerts` already computes (the bell's list), each with the link it already carries, plus the config-repo backing dot (`stateDot`) and the "Saved, but not applied yet" / "Stored, but not runnable" lists that today sit at the top of the settings tabs |
| Logs | `/system/logs` | `LogsPage` |
| Sandboxes | `/system/sandboxes` | `SandboxHealthPage`, and the connected-executors list (`ConnectedExecutors`) |
| Caches | `/system/caches` | `CachesPage` (contents and flush) |
| Top | `/system/top` | `TopPage` |
| Pipeline | `/system/pipeline` | `PipelineControl` |
| Metrics | `/system/metrics` | `MetricsOverlay`'s body, with its query (`?window=30d&f=...`) |
| Power | `/system/power` | restart, reboot, shut down |

**Decision:** Health is System's default tab. *Recommended: yes.* It is
the one question somebody opening System is asking ("is anything
wrong?"), and it needs no new endpoint.

**Decision:** Metrics moves into System as a tab, and the sidebar's
Metrics footer entry goes. *Recommended: yes.* `/metrics` and
`/metrics?<query>` redirect to `/system/metrics?<query>`, so a saved
report link keeps its scope and window.

**Decision:** Connected executors (status) go to System → Sandboxes, and
executor join tokens (a setting) go to Settings → Executors. *Recommended:
yes.* Settings → Executors keeps a one-line count with a link
("3 connected · see System → Sandboxes").

**Decision:** The upgrade-check mode and "pauses the pipeline" toggle
move to Settings → General → Updates. The upgrade itself, its targets and
the Postgres database migration stay in their own pane, with a link to
the mode. *Recommended: yes.* That pane was System → Upgrade, and is
Settings → Upgrade (`/settings/upgrade`) now: the deployment's own
version reads as part of its configuration, beside General and Config
repo, rather than as one of System's status tabs. `/system/upgrade` and
`/admin/upgrade` redirect there (§3.5). It stays an action rather than a
setting -- outside the save model, and needing `deployment:upgrade` on
the daemon exactly as before.

**Decision:** System stops being an overlay and becomes a view
(`view: "system"`) in the main pane, beside the sidebar, with the tab
strip in its header as today. *Recommended: yes*, for the same reason as
Settings (§3.1). The standalone `/sandboxes` and `/caches` views and
their pinnable entries become aliases of `/system/sandboxes` and
`/system/caches`. Existing pins keep working through the §5.6 migration.

### 2.2 Settings: every deployment setting, the preferences, and Memory

Settings holds every row of §1.1 (less the admin accounts), §1.3 (less
the username, plus "Sign out" and the version), and §1.4. The section
list is §3.2.

### 2.3 Per-repository settings

**Decision:** Per-repo settings stay on the repo page. Settings gets a
**Repositories** section that is an index only: one row per configured
repository, showing:

- the base branch;
- whether declarations are read;
- the CI mode;
- the git connection;
- a link into that repository's settings on its own page.

*Recommended: this.* The reasons:

- The repo page's tabs mix settings with the status they govern (the CI
  tab shows runs beside the CI mode; Releases shows the release beside
  its workflow). Splitting them would put each setting a click away from
  what it explains.
- `RepoPage` already has its own unsaved-edits guard
  (`repo-page-unsaved-edits.spec.js`) and autosave
  (`repos-autosave-walkthrough.spec.js`).
- The Repositories section answers the "where do I configure a repo"
  question without moving anything.

The repo page's tab becomes part of its path so the index can link to a
tab: `/repos/<owner>/<name>/settings/<tab>`, where `<tab>` is one of
RepoPage's existing tab ids (`overview`, `ci`, `merge-queue`, `agents`,
`branches`, `releases`, `advanced`). `paths.js` parses it into a new
`repoTab` field. The existing `/repos/<o>/<n>`,
`/repos/<o>/<n>/releases[/<r>]` and `/compare/...` paths keep their
meaning. No API change.

---

## 3. Settings layout

### 3.1 Shape

Settings is a view (`view: "settings"`) in the main pane, not an
overlay. It has two columns:

- a 232px section nav on the left, with a filter box at its top and the
  groups below;
- the open section on the right, as one scrolling column of cards, at
  most 760px wide.

**Decision:** Settings is a view, not an `Overlay pane`. *Recommended:
yes.* An overlay pane has no room for a second nav column, and its back
button already behaves like a page's: it is a destination
(`Overlay.jsx`'s own comment). As a view it gets the sidebar, the top
bar and Back for free, and the gear shows it as selected.

The section's own header row holds:

- the section title;
- the save-state line (§3.4);
- on the right, a "Restart needed" chip when a setting in this section is
  pending a restart (`settings.pendingRestart`). It links to System →
  Power.

Within a section, each former sub-heading (`subtitle2` today: "CI
queue", "Sandbox budget", ...) becomes a card with a title and an anchor
id. `/settings/engine#ci-queue` scrolls to it.

### 3.2 Groups and sections, in order

| Group | Section (path) | Holds | From |
|---|---|---|---|
| **Deployment** | General (`general`) | Environment name; show closed tasks by default; **Updates** card (upgrade check mode, pauses pipeline); **About** card (version, build stamp) | `general`, `upgrade` (settings part), `SettingsOverlay` version |
| | Access (`access`) | **This browser** card ("Signed in with the access code", or "through IAP as \<actor\>", and Sign out); **Relay** card (`RelayPanel`) | `access`, `SettingsOverlay` account |
| | Config repo (`config-repo`) | `StateRepoPanel` | `state` |
| | Upgrade (`upgrade`) | `UpgradePanel` without the two upgrade-check settings, and Database. An action, not a setting: no save state, never read-only under an external config repo | System's `upgrade` tab |
| **Agents** | Models (`models`) | Provider connections; default model; per-model settings, pauses and effort | `models` |
| | Agents (`agents`) | Max agent turns; standing instructions; task branches (template, deployment ID); commit identity pointer | `agents` |
| | Memory (`memory`) | `MemoryPanel` (deployment layer, each repo's layer, documents) | `/memory` |
| **Work** | Tasks (`tasks`) | Task defaults; backlog and follow-up limits; task fields | `workflow` |
| | Repositories (`repositories`) | The index of §2.3 | new, read-only |
| **Connections** | Git (`git`) | Git connections; git identity | `git` |
| | Credentials (`credentials`) | GitHub host; GCP; capabilities; secrets | `integrations` |
| | Executors (`executors`) | Join tokens, with a link to System → Sandboxes for the connected list | `executors` |
| **Infrastructure** | Engine (`engine`) | Poll interval; **Merge queue** card | `engine` (first half) |
| | CI (`ci`) | CI queue; CI history; CI job cache; CI artifacts | `engine` (second half) |
| | Sandboxes (`sandboxes`) | Backend, size and network; budget; disk floor; custom sandbox image | `sandbox` |
| | Caches (`caches`) | Proxy cache max size; Actions cache max size | `sandbox` (cache cards) |
| **This browser** | Preferences (`preferences`) | Appearance; time zone; task timeline order; sidebar sections and pinned navigation (opens `CustomizeSidebarOverlay`) | `SettingsOverlay` |

**Decision:** The groups and order above. *Recommended: as listed.* The
reasoning:

- Deployment identity first, because it is what a new owner sets first.
- What runs do (Agents) before where work goes (Work), then where it
  connects (Connections), then the machinery tuning nobody should need
  often (Infrastructure).
- The browser's own preferences last, apart from the rest, because they
  are not the deployment's.

**Decision:** Engine & Concurrency is split into Engine (poll interval,
merge queue) and CI (the four CI cards). *Recommended: yes.* Today's tab
is thirteen boxes long, and the CI half is what an operator tuning disk
is looking for.

**Decision:** "Integrations & Auth" is renamed **Credentials**, and
"Workflow & Tasks" is renamed **Tasks**. *Recommended: yes.* After the
reset neither holds auth or workflows.

**Decision:** Memory sits under Agents. *Recommended: yes.* It is what
runs are told (`MemoryPage.jsx`'s own header comment), beside the
standing instructions.

**Decision:** Bare `/settings` opens General on desktop and the section
list on a phone (§3.6). *Recommended: yes.* It does not reopen the last
section visited: a link should say where it goes.

### 3.3 Search and filter

A filter box at the top of the section nav ("Filter settings", `/` focuses
it while Settings is open):

- **Index.** A new `ui/src/settingsIndex.js` exports one entry per field
  and card: `{ id, section, anchor, label, keywords }`. `label` is the
  field's visible label (reusing `settingsPlan.js`'s `SETTING_LABELS`
  where the key is a `/api/settings` key). `keywords` are a few words of
  its helper text ("disk", "overcommit", "IAP").
- **Matching.** `fuzzy.js` (already used by the model picker) ranks
  entries by label, then keywords.
- **Results.** While the box holds text, the nav shows a flat list of
  matches ("Max merges per repo branch, Engine"), grouped by section, in place of
  the groups. Choosing one navigates to `/settings/<section>#<anchor>`,
  scrolls the field into view, focuses it and outlines it for two
  seconds. Escape clears the filter and restores the groups.
- **No match:** "No setting matches '\<text\>'."
- **Completeness test.** Every field the Settings sections render carries
  `data-setting-id`. A unit test renders each section with a fixture
  `/api/settings` and asserts the set of `data-setting-id`s equals the
  index for that section. A field added without an index entry fails CI.

**Decision:** The filter searches labels and helper keywords only, not
current values. *Recommended: yes.* Values include secrets' presence and
connection names, and matching on them would surface noise.

### 3.4 One save and dirty-state model

Today there are three models: `AdminOverlay`'s save-on-blur/on-change
with rollback (`saveChoice`), `MemoryPage`'s explicit per-box Save, and
the forms' `unsavedEdits.js` guard. Settings uses **one** model, with one
rule for which gesture commits:

1. **Each control commits on its natural gesture:**
   - checkboxes, radios and selects on change;
   - single-line text and number boxes on blur or Enter;
   - **multi-line text and composite forms** (standing instructions,
     a memory document, a git or provider connection,
     a secret, a relay token) on an explicit **Save** button at the
     foot of their own card, with **Discard** beside it.

   The `saveChoice` mechanics (`AdminOverlay.jsx`) are kept: optimistic,
   rolled back on failure, number boxes read through `numberBox.js`.
2. **One state per section.** A new `useSettingsSection()` hook holds
   it: `saving` (a request is in flight), `dirty` (a Save-button card
   holds an edit, read through `unsavedEdits.js`'s `formSnapshot` the
   way forms are today), `error` (the last save failed), or `saved`.
   The section header shows it as "Saving…", "Unsaved changes in
   *\<card\>*", "Couldn't save *\<field\>*: \<error\>" with **Retry**, or
   "All changes saved".
3. **The section nav marks dirty sections** with a dot. Leaving Settings,
   or leaving a dirty section for another one, asks first through the
   same `guardLeaving` App already reads for forms. A clean section
   never asks.
4. **The server's account wins.** After a save, the field shows what
   `PUT /api/settings` answered, not what was typed ("1m" reads back as
   "60s"), as today.
5. **Restart-pending and not-runnable states** (`pendingRestart`,
   `unrunnable`) show on the field itself as helper text, on the section
   header as the chip, and as one line on System → Health.

**Decision:** Commit-on-gesture with explicit Save only for multi-line
and composite cards, rather than one Save button per section.
*Recommended: commit-on-gesture.* It is what eleven of the twelve
settings tabs already do. A per-section Save would ask the owner to
remember to press it for a toggle. And a partial `PUT /api/settings` is
already what the daemon is built for.

### 3.5 Paths and redirects

New paths:

- `/settings`
- `/settings/<section>`, with an optional `#<anchor>`
- `/system`
- `/system/<tab>`
- `/repos/<o>/<n>/settings/<tab>` (§2.3)
- `/processes`, `/processes/<kind>/<id>`, `/processes/suites/runs/<id>` (§5)

Redirects are done in `paths.js` `parseSegments`: the old path parses to
the new state, and App's existing `history.replaceState` pass writes the
new path, so Back never lands on the old one. Matching ignores case, as
`adminTabFor` does now.

| Old path | New path |
|---|---|
| `/admin`, `/admin/general` | `/settings/general` |
| `/admin/agents` | `/settings/agents` |
| `/admin/models` | `/settings/models` |
| `/admin/git` | `/settings/git` |
| `/admin/workflow` | `/settings/tasks` |
| `/admin/access`, `/admin/accounts` | `/settings/access` |
| `/admin/engine` | `/settings/engine` |
| `/admin/sandbox` | `/settings/sandboxes` |
| `/admin/executors` | `/settings/executors` |
| `/admin/integrations` | `/settings/credentials` |
| `/admin/state` | `/settings/config-repo` |
| `/admin/upgrade`, `/system/upgrade` | `/settings/upgrade` |
| `/admin/{logs,sandboxes,caches,top,pipeline,power}` | `/system/<same>` |
| `/admin/<anything else>` | `/settings/general` |
| `/system/<a settings tab id above>` | the same target as `/admin/<id>` |
| `/system/<anything else>` | `/system` (Health) |
| `/settings` (the old `SettingsOverlay`) | `/settings/general`. The old overlay's contents now live in `/settings/preferences` and `/settings/access`; the bare path keeps working as Settings' own entry |
| `/memory` | `/settings/memory` |
| `/metrics[?query]` | `/system/metrics[?query]` |
| `/sandboxes`, `/caches`, `/cache`, `/actions-cache` | `/system/sandboxes`, `/system/caches` |
| `/schedules[/<id>]`, `/templates[/<id>]`, `/suites[/<id>]`, `/suites/runs/<id>` | §5.4 |

Every in-app link that names an old path is rewritten to the new one:

- `alerts.js`'s links;
- the `commit-identity-pointer` text;
- the hints that say "on the Repos pane" or "Engine & Concurrency";
- `adminTabReferences.test.jsx`, which already checks that every
  "System → X" reference in the UI names a tab that exists. It is
  extended to `settingsSectionReferences` and to the System tab list.

**Decision:** Redirects are kept indefinitely; they are a few lines of
`parseSegments`. *Recommended: yes.*

### 3.6 Narrow screens and phones

| Width | Behaviour |
|---|---|
| ≥ 900px | Two columns as in §3.1 |
| 600–899px | The section nav collapses into a full-width **Section** select at the top of the content ("Settings / [Engine ▾]"), grouped with `ListSubheader`s; the filter box sits beside it and opens the results as the select's menu |
| Phone (`PHONE_QUERY`, `phone.js`) | Master-detail. `/settings` shows the filter box and the grouped section list as a full-screen list (40px rows, `phone.js`'s touch-target rule). `/settings/<section>` shows the section alone, under a back arrow that returns to `/settings`. Cards stack full width; Save and Discard stretch full width at the foot of a dirty card |

System behaves the same way: on a tablet its tab strip already scrolls
(`variant="scrollable"`); on a phone it shows the same master-detail
list as Settings.

### 3.7 Wireframes

Desktop, `/settings/engine`:

```
┌──────────────┬───────────────────────────────────────────────────────────────┐
│ + New task ▾ │ settings / engine                         (!)  [▣]  [⚙]       │  page header rail
│ Pinned  Cust.├──────────────────┬────────────────────────────────────────────┤
│  ▸ Inbox     │ [Filter settings]│ Engine                 All changes saved   │
│ ──────────── │                  │                                            │
│  Tasks    41 │ DEPLOYMENT       │ ┌ Polling ─────────────────────────────┐   │
│   Merged  12 │   General        │ │ Poll interval   [ 30s          ]     │   │
│   Blocked  3 │   Access         │ │ how often grain looks for work ...   │   │
│  Boards      │   Config repo  ● │ └──────────────────────────────────────┘   │
│  Sessions    │ AGENTS           │ ┌ Merge queue ─────────────────────────┐   │
│  Processes   │   Models         │ │ Max merges per repo branch [ 3  ]    │   │
│  CI          │   Agents         │ └──────────────────────────────────────┘   │
│  Repos     6 │   Memory         │                                            │
│              │ WORK             │                                            │
│              │   Tasks          │                                            │
│              │   Repositories   │                                            │
│              │ CONNECTIONS      │                                            │
│              │   Git            │                                            │
│              │   Credentials    │                                            │
│              │   Executors      │                                            │
│              │ INFRASTRUCTURE   │                                            │
│              │ ▌ Engine         │                                            │
│              │   CI             │                                            │
│              │   Sandboxes      │                                            │
│              │   Caches         │                                            │
│ ──────────── │ THIS BROWSER     │                                            │
│ v1.14 · 3f2a │   Preferences    │                                            │
└──────────────┴──────────────────┴────────────────────────────────────────────┘
  (!) alerts bell   [▣] System   [⚙] Settings (selected)   ● = unbacked config repo dot
```

Desktop, a filter in progress:

```
│ [overcommit   ×]  │
│ SANDBOXES         │
│   CPU overcommit  │
│   Memory overcom. │
```

Desktop, a dirty multi-line card:

```
│ Agents        Unsaved changes in Standing instructions              │
│ ┌ Standing instructions ──────────────────────────────────────────┐ │
│ │ ┌─────────────────────────────────────────────────────────────┐ │ │
│ │ │ Use conventional commits. Run `make test` before pushing.▌  │ │ │
│ │ └─────────────────────────────────────────────────────────────┘ │ │
│ │                                           [Discard]  [ Save ]   │ │
│ └─────────────────────────────────────────────────────────────────┘ │
```

Phone, `/settings` then `/settings/preferences`:

```
┌──────────────────────────┐      ┌──────────────────────────┐
│ ☰  ◉ grain   (!) [⚙] [+] │      │ ☰  ◉ grain   (!) [⚙] [+] │
├──────────────────────────┤      ├──────────────────────────┤
│ [Filter settings       ] │      │ ← Preferences            │
│ DEPLOYMENT               │      │ ┌ Appearance ──────────┐ │
│  General               › │      │ │ ◉ Auto ○ Light ○ Dark│ │
│  Access                › │      │ └──────────────────────┘ │
│  Config repo         ● › │      │ ┌ Time zone ───────────┐ │
│ AGENTS                   │      │ │ [ Europe/London   ▾] │ │
│  Models                › │      │ └──────────────────────┘ │
│  ...                     │      │ ┌ Task timeline ───────┐ │
└──────────────────────────┘      └──────────────────────────┘
```

System, desktop, `/system`:

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ grain / System / Health                                 (!)  [▣]  [⚙]        │
├──────────────┬───────────────────────────────────────────────────────────────┤
│  (sidebar)   │ System                                                        │
│              │ Health  Logs  Sandboxes  Caches  Top  Pipeline  Metrics  Upgr │
│              │ ───────                                                       │
│              │ ⚠ Config repo is not backed: nothing has synced since 09:12  › │
│              │ ⚠ 2 sandboxes are past their lifetime                        › │
│              │ ⓘ Saved, not applied until restart: Poll interval    [Power] › │
└──────────────┴───────────────────────────────────────────────────────────────┘
```

---

## 4. Top bar and sidebar

### 4.1 The page header rail (`PageHeader.jsx`)

There is no global top bar any more (grain/task-4915). It used to be a
row of its own over the sidebar and the page, holding "grain / <page>"
and the cluster below, with each page's own header under it: two rails.
Now every page in the main pane has one header rail, drawn by `App.jsx`
at the top of the pane (beside the sidebar, which runs the full height
of the window) and outside the page's `ErrorBoundary`, so a page that
threw, or one with no header of its own, still has it. It is one row of
one height (77px, with `.content-header`'s `1rem 1.75rem` padding and
`1rem` heading typography), and holds, left to right:

1. **The section glyph and title**, as crumbs: the section's 20px
   `ItemGlyph` (`.header-icon`) followed by what the old top bar's page
   segment said,
   without "grain" -- `tasks`, `repositories / acme/widgets / releases`,
   `settings / models`, `system / logs`, `boards / <name>`,
   `sessions / <name>`, `task 123`. The last crumb carries
   `aria-current="page"` and `data-testid="top-bar-page"`.
2. **The page's own header row**: its count, buttons, search, filters
   and toolbar. Pages put them there with `PageHeaderActions`, a portal
   into the rail's slot, so they keep their handlers, classes and test
   ids; `ListHeader` and `ListToolbar` do it for every list page. The
   page's heading stays in its own body, inside its main landmark,
   visually hidden while the rail's title says it on screen. The slot
   wraps but is clipped to one line; when anything is clipped a **more**
   button (`page-header-more`) opens the slot under the rail as a menu
   with every row of it, rather than the rail growing a second row.
   The repo and board pages' summary blocks (chips, description, gate
   warnings) are not header rows and stay in the page.

   The slot's rules (grain/task-4980):
   1. **One line, centred on the rail.** The first line fills the slot's
      height whatever is on it, so a count, a badge or a button is level
      with the title on every page. Nothing in the slot may be taller
      than that line (40px): a control with a caption under it (Repos'
      discovery status) puts the caption in the page body while the rail
      shows.
   2. **Left, the page's state and narrowing**, in the page's order:
      its count, search, filters, sort, tabs. The search box is the item
      that grows, and in the rail its basis is 9rem, so it gives up width
      before anything after it wraps.
   3. **Right, the page's own actions** ("+ New", "Add repo", "New
      board", "Refresh"), marked with `PageHeaderPageActions`
      (`page-header-page-actions`; `ListHeader` wraps its `action` in
      it). Whatever order the page portals them in, they are drawn after
      everything else and pushed to the end of the slot, just before the
      global buttons. A later list's header (Processes' "Suite runs")
      follows them as one group, with its own action.
   4. **Counts** (`.count`, here and everywhere a label carries one) are
      one badge: 0.75rem, tabular numerals, centred on the label's line.
      The rail's gap is the slot's (0.75rem), so the first count is as
      far from the title as a later list's count is from its label.

   `page-header-rail-walkthrough.spec.js` checks the count and action
   rules on every page that has a count or an action.
3. **The global buttons** (`page-header-globals`), on every page:
   1. **Alerts bell** (`AlertsButton`), unchanged. Its "see all" goes to
      System → Health.
   2. **System**: an `IconButton` with the "system" glyph (`ItemGlyph
      kind="system"`). It has `aria-label="System"` and
      `data-testid="top-bar-system"`, and carries the `UnbackedStateDot`
      badge. Selected while `view === "system"`.
   3. **Settings**: an `IconButton` with the gear glyph (`ItemGlyph
      kind="settings"`). It has `aria-label="Settings"` and
      `data-testid="top-bar-settings"`. Selected while
      `view === "settings"`. This is the **only** entry point to
      Settings.

DOM order is title, page actions, globals, so Tab reaches the page's
own controls before the buttons every page shares.

On a phone there is no rail: `PhoneNav`'s bar is the rail there, with
the bell and the gear between the environment chip and "+", and System
at the foot of the drawer (§4.3). Each page keeps its own header row in
its body, as it always has, so nothing is drawn twice. The drawer holds
the sidebar alone; it no longer carries a copy of the top bar.

Overlays that open as panes (`Overlay` with `pane`) are headed by the
same component (`data-testid="pane-header"`), with the way back where a
page has its title and no global buttons: a pane is a step inside the
page under it, whose rail already has them. Anything inside an overlay
is cut off from the page's rail (`PageHeaderScope`), so a list header
in a pane stays in the pane.

**Decision:** System's entry point is an icon beside the gear on the
page header rail on desktop, and the drawer's foot on a phone.
*Recommended: yes.* Status
belongs next to the alerts bell that points into it. A 390px bar has room
for one more 40px button, not two, and the gear is the one the owner
asked for.

No keyboard shortcuts are added. App has no navigation shortcuts to
match today, and both buttons are reachable by Tab in DOM order, after
the title and the page's own controls, as the bell is.

### 4.2 Sidebar (`Sidebar.jsx`) after all three stages

From top to bottom:

```
+ New task ▾
Pinned                      Customize
  (pinned items)
──────────
Tasks            41
  Merged         12
  Blocked         3
Boards
  (boards)
Sessions
  (sessions)
Processes        17
CI
Repos             6
──────────
v1.14.0 · 3f2a1c9            ← build stamp, as today
```

- The `team-section` box is gone. Its children are the flat list, in the
  order shown, with no border or background.
- Memory is gone from the sidebar (Settings → Memory).
- Schedules, Templates and Suites are replaced by one **Processes** entry,
  with a count of every row Processes lists. It uses a new "processes"
  glyph (§5.7).
- The footer `List` with System and Metrics is gone (System is in the top
  bar, Metrics is in System).
- The user tile is gone (§6).
- **Customize** (`CustomizeSidebarOverlay`, the one customize control,
  in the Pinned row expanded and as a tune icon on the folded rail) sets
  both what is pinned and which sections show. Each of Pinned, Tasks,
  Boards, Sessions, Processes, CI, Repos and Staging
  (`sidebarSections.js`) can be hidden in this browser, kept as an array
  of ids in `localStorage["grain.sidebar.hidden"]`; nothing stored means
  everything shows, and an id the build does not know is ignored, so a
  section added later is never hidden by an old list. A hidden section
  loses its row and its rail count, folded or not, but its route still
  opens by URL and a pin of it still shows under Pinned. The Customize
  control itself stays when Pinned is hidden, so the panel can always be
  put back; Reset to default there shows everything again.

### 4.3 What the footer holds instead

On desktop the footer holds only the build stamp. It is the line that
sits under the user tile today (`buildStampLabel`, title
`buildStampTitle`). It becomes a link to `/settings/upgrade`, so "which
build is this, and is there a newer one" is one click.

On a phone (`pinFooter`), the drawer's foot holds a **System** row with
its unbacked-state dot, then the build stamp. (Settings is on the bar.)

**Decision:** The footer is the build stamp alone, linked to System →
Upgrade. *Recommended: yes.*

---

## 5. The Processes page

### 5.1 One table

`/processes` is one list (a new `ProcessesList.jsx`). It reads only what
the four lists read today:

- `GET /api/schedules`;
- `GET /api/templates`;
- `GET /api/suites`;
- `GET /api/suite-runs`;
- for triggers, each declaring repository's `GET
  /api/repos/{owner}/{name}/declarations`.

Its toolbar is the existing `ListToolbar`:

- `ListSearchField`, matching name, repo and declared-in path as the
  lists do now;
- `ListSortSelect`: Next run (the default), Last run, Name (A–Z), Kind,
  Custom order;
- the kind filter, a row of toggle chips: **All · Schedules · Workflow
  schedules · Templates · Suites · Triggers**, each with its count. The
  selection rides in the query as `?kind=schedule,suite`, so a filtered
  view is a link, the same rule `paths.js` applies to task narrowing.

| Column | Schedule | Workflow schedule | Template | Suite | Trigger |
|---|---|---|---|---|---|
| Kind chip | Schedule | Workflow | Template | Suite | Trigger |
| Name | `title` | `title` | `name` | `name` | the trigger file's path, without `.grain/triggers/` |
| Repo | `repo` | `repo` | `repo`, or "any" when unset | the repos of its items, or "several" | the declaring repo |
| Last run | `lastRunAt` | `lastWorkflowRun` (time and verdict, `LastWorkflowRun`) | the newest `lastRunAt` among schedules whose `templateId` is it, else "—" | the newest `/api/suite-runs` entry for it | "—" |
| Next run | `nextRunAt` (or the suppression note) | `nextRunAt` | the soonest `nextRunAt` among enabled schedules that fire it, else "on demand" | the same, through `suiteId` | "on change" |
| Enabled | `enabled` | `enabled` | "—" | "—" | "—" |

Every row also shows the existing `DeclaredChip` when `declaredIn` is set.
A **workflow schedule** is a schedule whose `workflowReview` is non-empty
(set only on schedules that run a workflow, per `pkg/ui/schedules.go`).
A system schedule (`system: true`) keeps the lock note SchedulesList
draws today.

**Decision:** Triggers are listed read-only, by path, with no last or
next run. *Recommended: yes.* The API reports a trigger only as its file
path (`DeclarationsStatus.Triggers`). Showing more would need an API
change, which is out of scope. A trigger row opens the file on its
repository's Advanced tab. If the owner would rather not show triggers
until the API reports more, the Triggers chip and rows are dropped and
nothing else changes.

**Decision:** Templates and suites show derived last and next runs (from
the schedules that fire them and from suite runs), not "—". *Recommended:
derived.* "When does this next run" is the question the page is opened
to answer, and the data is already loaded.

### 5.2 Opening a row

A row opens the overlay it opens today, unchanged:

- a schedule → `ScheduleOverlay`;
- a template → `TemplateOverlay`, with "Use" → `UseTemplateOverlay`;
- a suite → `SuiteOverlay`, with "Run" → `SuiteRunOverlay`;
- a suite run → `SuiteRunPane`.

`SchedulesList`, `TemplatesList` and `SuitesList` are deleted. What
they did besides drawing rows moves into `ProcessesList`:

- their custom order, which moves to one `listOrder.js` key, migrated in
  §5.6;
- the "open item not found" handling;
- the reorder affordance.

### 5.3 Create flow

The header's **New** button opens a small menu (`data-testid=
"new-process-kind"`): **Schedule · Template · Suite**. Picking one opens
the existing create overlay for that kind (`ScheduleOverlay`,
`TemplateOverlay`, `SuiteOverlay` with no item) exactly as the old lists'
"New" buttons do.

When the kind filter is narrowed to one creatable kind, **New** skips the
menu and opens that kind directly.

Workflow schedules and triggers are not offered. They are declared in a
repository's `.grain/` (`docs/declarations.md`), and the menu ends with
a hint line that says so and links to the doc.

The sidebar's "+ New task ▾" split button keeps its own entries. If it
lists "New schedule", "New template" or "New suite" by the time of stage
(b), they now open `/processes` with that overlay on top.

### 5.4 Paths and redirects

| Path | Shows |
|---|---|
| `/processes` | the list (query: `?kind=`, `?q=`, `?sort=`) |
| `/processes/schedules/<id>` | the list with `ScheduleOverlay` open on `<id>` |
| `/processes/templates/<id>` | the list with `TemplateOverlay` |
| `/processes/suites/<id>` | the list with `SuiteOverlay` |
| `/processes/suites/runs/<id>` | the list with `SuiteRunPane` |
| `/processes/triggers/<owner>/<name>/<path>` | the trigger's repo, Advanced tab |

The `<kind>` segment is plural, matching the old top-level paths, so
each redirect is a prefix swap. Workflow schedules use
`/processes/schedules/<id>`, because they are schedules. Item ids are
decoded with `itemId()` as today.

| Old | New |
|---|---|
| `/schedules` | `/processes?kind=schedule` |
| `/schedules/<id>` | `/processes/schedules/<id>` |
| `/templates` | `/processes?kind=template` |
| `/templates/<id>` | `/processes/templates/<id>` |
| `/suites` | `/processes?kind=suite` |
| `/suites/<id>` | `/processes/suites/<id>` |
| `/suites/runs/<id>` | `/processes/suites/runs/<id>` |

In App, the `view` values `schedules`, `templates` and `suites` are
replaced by `processes`, with `processKind` and `processId` beside the
existing `scheduleId`, `templateId`, `suiteId` and `suiteRunId`. Those
four stay as the "which overlay is open" state, so the overlays need no
change. Links elsewhere in the UI that build `/schedules/<id>` (a task's
"filed by schedule" hourglass, `SuitesList`'s run rows, `CompositionPanel`)
switch to a `processPath(kind, id)` helper in `paths.js`.

### 5.5 What does not change

- No model, API or `.grain/` declaration change.
- The overlays' forms, validation and composition previews.
- `scheduleSuppression.js`, `templateLabels.js`.

### 5.6 Pinned-nav migration

Pinned items are `{ kind: "view", id }` in the pinned list (§6.3 says
where that list lives after stage c). On load, `pinnedNav.js`'s
`cleanPinnedItem` maps:

| Pinned `view` id | Becomes |
|---|---|
| `schedules`, `templates`, `suites` | one `{ kind: "view", id: "processes" }`, at the position of the first of them; the others are dropped, keeping the user's colour and icon from that first one |
| `memory` (if pinned) | `{ kind: "view", id: "settings", section: "memory" }` |
| `sandboxes`, `caches` | `{ kind: "view", id: "system", tab: "sandboxes" \| "caches" }` (stage a) |

`AVAILABLE_VIEWS` loses Schedules, Templates, Suites, Sandboxes and
Caches. It gains:

- **Processes**;
- **System**, which can be pinned to a tab;
- **Settings**, which can be pinned to a section.

The mapping is idempotent, and it rewrites the stored list once (write
back on the first load that changed anything). A unit test feeds every
old view id through `loadPinnedItems` and checks the result and that a
second load is a no-op.

The custom row orders `SchedulesList`, `TemplatesList` and `SuitesList`
kept in `listOrder.js` (one key each) are merged into the Processes key:

- schedules first, then templates, then suites, each in its saved order;
- items with no saved position follow, by the default sort.

The three old keys are then removed.

### 5.7 Glyph

**Decision:** Processes gets a new Chladni figure ("a loop through three
nodes"), drawn to the existing `ItemGlyph` grid and listed in
`CHLADNI_ICONS`. *Recommended: yes.* The alternative, reusing the
Schedules figure (two posts), reads as "time", and a template or a
trigger is not.

---

## 6. The user tile and username

### 6.1 Every use of the browser-chosen username (e1efc3e)

`username` comes from `useUser()` (`UserContext.jsx`,
`localStorage["grain.username"]`). The server already ignores it
everywhere:

- `X-Grain-User` is in `PlaintextIdentityHeaders` and is never believed
  (`pkg/ui/principal.go`);
- a task's creator is the request's principal or the deployment's actor,
  whatever the body says (`pkg/ui/tasks.go`, `client.go` `CreateTask`);
- a comment's author is `actingUser(r, ...)`, which ignores what the
  body claims (`pkg/ui/teams.go`).

| Use | Where | Replacement |
|---|---|---|
| `X-Grain-User` header on every request | `api.js` | **nothing**: the header is no longer sent |
| `creator: username` on `POST /api/tasks` | `NewTaskOverlay.jsx` ~671 | **nothing**: the field is omitted; the server records its actor |
| `creator: username` on use-template | `UseTemplateOverlay.jsx` ~140 | **nothing** |
| `author: username` on a comment | `TaskChat.jsx` ~296, `detail/Timeline.jsx` ~532, `InboxPage.jsx` ~798 | **nothing** |
| `useUser()` read but not used for the request | `DetailOverlay.jsx` ~185, `CachesPage.jsx` | the import is deleted |
| The user tile's name | `Sidebar.jsx` ~1347–1410 | **nothing**: the tile is deleted |
| `actingUser` | `Sidebar.jsx` ~375, `AdminOverlay.jsx` ~246 | **nothing** (#4880 removes the admin checks it fed) |
| `actsAsSystemAdmin(username, config)` | `App.jsx` ~3022, `InboxPage.jsx` ~570, `alerts.js` ~346, `memory.js` `canWriteMemory` | **nothing** (#4880; every control is shown) |
| `username` prop to `MemoryPage` | `App.jsx` ~3153 | **nothing** |
| `collectAlerts(config, { username })` | `App.jsx` ~2811 | `collectAlerts(config)` |
| The "Username" box and "Signed in through IAP as {actor \|\| username}" | `SettingsOverlay.jsx` | **server-reported actor**: Settings → Access shows `config.actor` (with its `user:` prefix stripped as the tile does today), or "the access code" when `config.actor` names the deployment's own actor |
| `UsernameDialog` (first visit, signed out, "change username") and `onOpenChangeUsername` | `App.jsx` ~323–345, ~2729, ~2782, ~3332 | **nothing** for the first visit. A signed-out browser (`config.authenticated === false && config.actor === ""`) gets `AccessDenied`, which points at the access-code sign-in (`/api/auth/access-code`). `UsernameDialog.jsx` and its test are deleted |
| Key segment of the pinned list | `PinnedNavContext.jsx`, `pinnedNav.js` | a deployment-wide key (§6.3) |
| `localStorage.setItem("grain.username", "alice")` | `setupTests.js` | removed; tests that asserted on `X-Grain-User` or a creator are updated |
| "Literal text can include a username, for example alice/grain/task-{id}" | branch template helper text | unchanged: this is about the branch name, not the browser user |

**Decision:** The UI never shows who it is acting as, except on Settings
→ Access ("Signed in with the access code", or the IAP actor when the
deployment sits behind IAP). *Recommended: yes.* With one credential and
one owner, a name on every screen says nothing.

**Decision:** The task creator and comment author are left to the server,
which already records the deployment's actor. *Recommended: yes.* No API
change.

### 6.2 What `UserContext` becomes

`UserContext.jsx` is renamed `PreferencesContext.jsx`. It keeps only
`timeZone` / `setTimeZone`. `useUser()` is replaced by `usePreferences()`
at the remaining call sites (`TimeZoneContext.jsx` and the time-zone
picker).

### 6.3 Moving per-user keys to deployment-wide keys

Today's keys:

| Key | Scope today |
|---|---|
| `grain.username` | per browser |
| `grain.userTimeZone` | per browser |
| `grain.themeMode` | per browser, no user segment: **already deployment-wide in this sense; unchanged** |
| `grain.timelineOrder` | per browser, no user segment: **unchanged** |
| `grain.pinnedNav.<user>.<team>`, e.g. `grain.pinnedNav.alice.default` | per browser, per user, per team |
| `grain.pinnedNav.<user>` and `grain.pinnedNav` (pre-team legacy) | per browser, per user |
| `grain.list-order.<list>` (`listOrder.js`) | per browser, no user segment: **unchanged** (stage b merges three of them, §5.6) |

After stage (c):

| New key | Holds |
|---|---|
| `grain.pinned` | the pinned list |
| `grain.sidebar.hidden` | the sidebar sections this browser hides (§4.2), added later |
| `grain.timeZone` | the preferred time zone |
| `grain.themeMode`, `grain.timelineOrder` | unchanged |

"Deployment-wide" here means one value per browser for this deployment,
with no user segment. localStorage is already per origin, so it is per
deployment.

A one-time migration runs before the first render (in `main.jsx`, before
`createRoot`). It is a new `ui/src/storageMigration.js` and records that
it has run under `grain.storageVersion = "1"`:

1. **Pins.** Candidate lists, in order of preference:
   1. `grain.pinnedNav.<current grain.username>.default`;
   2. `grain.pinnedNav..default` (empty user);
   3. any other `grain.pinnedNav.<user>.default`, the most recently
      written first. localStorage keeps no timestamps, so this falls back
      to the alphabetically first user;
   4. `grain.pinnedNav.<grain.username>` and `grain.pinnedNav` (pre-team
      legacy);
   5. any other `grain.pinnedNav.<user>.<team>`.

   The first that parses becomes `grain.pinned`, passed through §5.6's
   `cleanPinnedItem` mapping. Any other non-empty list is **merged** in
   after it: items whose `{kind, id}` are not already present are
   appended in their own order. That way no pinned board or session a
   second name had pinned is lost. Every `grain.pinnedNav*` key is then
   removed.
2. **Time zone.** `grain.userTimeZone` → `grain.timeZone` if the latter
   is unset; the old key is removed.
3. **Username.** `grain.username` is removed last, after step 1 has read
   it.
4. Every step is wrapped as `UserContext` wraps storage today
   (unavailable storage is not an error). If a step throws, the version
   key is not written and the next load retries. Each step is idempotent,
   so a retry is safe.

`migrateLegacyPinnedItems`, `pinnedItemTeam`, `legacyPinnedStorageKey`,
`pinnedStorageKey`'s user and team parameters, `PIN_SCOPE`, and App's
`pinsMigratedRef` effect are deleted. A team-era (pre-3724) list is
covered by step 1.iv.

**Decision:** Merge rather than pick one when several names had pins in
one browser. *Recommended: merge.* Nothing anyone pinned disappears, and
the owner can unpin.

---

## 7. Implementation plan

Three pull requests, landed in order. Each leaves `main` working and
green. Each includes its unit tests and its Playwright specs, and updates
the docs that describe the UI it changes (`docs/quickstart.md`,
`docs/schedules.md`, `docs/declarations.md` where they name a sidebar
entry or `/admin/...` path).

**Route-authorization baseline impact, all three stages: none.**

- No stage adds, removes or changes a `pkg/ui/server.go` route or an
  authorization check.
- `tests/security/routes_test.go` and
  `.grain/baselines/route-authorization.txt` are untouched.
- The new paths are client-side, served by the existing SPA fallback.
- If any stage finds it needs a server change, it stops and asks rather
  than widening its scope.

**Decision:** The stages run (a) → (b) → (c), each depending on the
last. *Recommended: yes.* (b) edits the sidebar and the pinned list (a)
reshaped, and (c) migrates the pinned list (b) last changed. Running them
in parallel would conflict in `Sidebar.jsx`, `paths.js` and `pinnedNav.js`.

**Decision:** Stage (a) does not start until #4880 (admin gates) and
#4862–#4864 (team columns) have merged to `main`. *Recommended: yes.*
Otherwise (a) would carry `isAdmin` plumbing it immediately deletes.

### Stage (a): the Settings panel, Memory and the team leftovers {#SET-1}

**Builds:** §2, §3, §4.1, the System half of §4.2 and §4.3, §1.6's team
rows. Settings and System become views at the paths and redirects of
§3.5. Memory moves into Settings. The team leftovers go.

**New components and modules:**

- `components/SettingsPage.jsx`: the shell, with the section nav, filter,
  header state and narrow/phone behaviour.
- `components/settings/*.jsx`: one file per §3.2 section
  (`GeneralSection`, `AccessSection`, `ConfigRepoSection`,
  `ModelsSection`, `AgentsSection`, `MemorySection`, `TasksSection`,
  `RepositoriesSection`, `GitSection`, `CredentialsSection`,
  `ExecutorsSection`, `EngineSection`, `CISection`, `SandboxesSection`,
  `CachesSection`, `PreferencesSection`).
- `components/SystemPage.jsx` and `components/system/HealthTab.jsx`.
- `settingsIndex.js` and `useSettingsSection.js`.

**Moved or merged:**

- `AdminOverlay.jsx`:
  - the settings tabs' bodies are split into the section files;
  - `saveChoice`, `onTextBlur`, `onIntBlur` and `onFloatBlur` move into
    `useSettingsSection.js`;
  - `ADMIN_TABS` becomes `SETTINGS_SECTIONS` (in `SettingsPage.jsx`) and
    `SYSTEM_TABS` (in `SystemPage.jsx`);
  - `POWER_ACTIONS` moves to `SystemPage.jsx`;
  - `adminTabFor` becomes `settingsSectionFor` and `systemTabFor`.
- `DeploymentConnectionsPanel.jsx` is split into `ModelsSection`,
  `GitSection` and `TasksSection`:
  - `TeamModelsPanel.jsx` → `components/settings/ModelDefaults.jsx`;
  - `TeamGitPanel.jsx` → `components/settings/GitConnections.jsx`;
  - every `team*` identifier is renamed to deployment terms
    (`teamData` → `deployment`, `saveTeamChoice` → `saveDeployment`);
  - `DEPLOYMENT_TEAM` is deleted.
- `SettingsOverlay.jsx` → `PreferencesSection` (appearance, time zone,
  timeline, pinned) and `AccessSection` (sign-out, IAP actor). The
  username box is kept until stage (c).
- `MemoryPage.jsx` → `MemorySection` (drops `username` from its props
  only if #4880 has already made `canWriteMemory` unconditional).
- `UpgradePanel.jsx`: the upgrade-check controls move to
  `GeneralSection`'s Updates card.
- `MetricsOverlay.jsx`'s body → System's Metrics tab (the component is
  kept and rendered inside `SystemPage`; the overlay wrapper goes).
- `ConnectedExecutors` → System → Sandboxes.

**Deleted:**

- `SystemOverlay.jsx`, and `AdminOverlay.jsx` once empty.
- The Memory sidebar entry, the `team-section` box, and the sidebar footer
  `List` (System, Metrics).
- `PhoneNav.jsx`'s `team-selector` and `team-settings-button` checks.
- `#team-selector` in `style.css`.
- `fallbackTeam` and the switching-teams comment in `App.jsx`.
- `team:` in `createStandingSession`'s body.
- The `showSettings`, `showAdmin`, `showSystem` and `showMetrics` overlay
  state in `App.jsx` and `paths.js` (replaced by `view: "settings" |
  "system"` with `settingsSection` / `systemTab`).

**Top bar:** `TopBar.jsx` gains the System and Settings buttons (since
folded into each page's header rail, `PageHeader.jsx`, §4.1), and
`PhoneNav.jsx` gains the gear and the drawer-foot System row. The user
tile stays until stage (c), but stops opening Settings and is
display-only.

**Pinned items:** the `sandboxes`, `caches` and `memory` rows of §5.6.

**Unit tests, changed or added:**

- **Rewritten** for the new modules: `AdminOverlay.test.jsx` (→
  `SettingsPage.test.jsx` and one test file per section where a section
  has logic of its own), `SystemOverlay.test.jsx` (→
  `SystemPage.test.jsx`), `SettingsOverlay.test.jsx` (→
  `PreferencesSection.test.jsx`, `AccessSection.test.jsx`),
  `DeploymentConnectionsPanel.test.jsx`, `TeamGitPanel*.test.jsx` (→
  `GitConnections*.test.jsx`), `TeamModelsPanel.test.jsx` (→
  `ModelDefaults.test.jsx`), `MemoryPage.test.jsx` (→
  `MemorySection.test.jsx`), `MetricsOverlay.test.jsx`.
- **Updated** for the new paths and layout: `adminTabReferences.test.jsx`
  (→ `settingsReferences.test.jsx`, covering sections and System tabs),
  `paths.test.js` (every row of §3.5's redirect table, round-tripped
  through `buildPath`), `App.overlays.test.jsx`, `App.test.jsx`,
  `Sidebar.test.jsx` (no `team-section`, no Memory, no footer list),
  `TopBar.test.jsx` (the two buttons, `aria-label`s, selected state),
  `PhoneNav.test.jsx` (the gear, the drawer-foot System row),
  `alerts.test.js` (links point at `/system/...` and `/settings/...`),
  `pinnedNav.test.js` (the §5.6 sandboxes, caches and memory rows),
  `UpgradePanel.test.jsx`.
- **New:** `settingsIndex.test.js` (the completeness check of §3.3) and
  `useSettingsSection.test.jsx` (saving, dirty, error and retry,
  rollback, the leave guard).

**Playwright, changed:**

- `system-overlay-tabs.spec.js` → `system-tabs.spec.js`.
- `system_workflows.spec.js`.
- `system-rename-walkthrough.spec.js`.
- `deployment-operations-*.spec.js` (admin-pointers, escape-power,
  failed-reads, identity-sandbox-errors, logs-tail, metrics-window,
  navigation, number-boxes), `deployment_operations_walkthrough.spec.js`
  and `deployment-settings-and-operations-inputs-walkthrough.spec.js`:
  paths and selectors.
- `config.spec.js`, `deployment-connections-models-walkthrough.spec.js`,
  `provider-connection-lifecycle-walkthrough.spec.js`,
  `git-connections-multihost-walkthrough.spec.js`,
  `relay-settings-walkthrough.spec.js`, `port-settings-walkthrough.spec.js`,
  `deployment-id-walkthrough.spec.js`, `model-picker-search.spec.js`,
  `model-source-pause-walkthrough.spec.js`,
  `credential-reach-walkthrough.spec.js`,
  `stale-save-two-contexts-walkthrough.spec.js`.
- `memory.spec.js` and the `repo*-release-memory*` walkthroughs where
  they open `/memory`.
- `sidebar-navigation.spec.js`, `responsive-navigation-walkthrough.spec.js`,
  `phone.spec.js`, `narrow.spec.js`, `paths.spec.js`,
  `track6-navigation-merged-dissolve.spec.js`,
  `ui-navigation-split-button-upgrade.spec.js`, `upgrade.spec.js`,
  `host-sandboxes-alert.spec.js`, `observability.spec.js`,
  `unsaved-edits-guard.spec.js`.

**Playwright, new:** `settings-panel-walkthrough.spec.js`, against the
real `grain demo` server with no mocked grain APIs. It covers:

- the gear opens Settings, and the System icon opens System → Health;
- every `/admin/<tab>`, `/system/<settings-tab>`, `/settings`, `/memory`
  and `/metrics?window=30d` redirect lands where §3.5 says, with Back
  not returning to the old path;
- filtering "overcommit" finds and focuses CPU overcommit;
- a toggle autosaves and survives reload;
- an edited standing instruction marks the section dirty, the leave guard
  asks, and Discard clears it;
- at 390×844: the section list, opening a section, back to the list;
- at 768×1024: the section select.

A `docs/test-runs/` report records the run, as the UI walkthrough
convention requires.

**Route-authorization baseline impact:** none.

### Stage (b): Processes {#SET-2}

**Builds:** §5, and the Processes entry of §4.2.

**New:**

- `components/ProcessesList.jsx`;
- `processes.js` (row normalisation across kinds, the derived last and
  next runs, the kind filter's query encoding);
- `processPath()` in `paths.js`;
- the Processes glyph in `ItemGlyph.jsx` and `CHLADNI_ICONS`.

**Moved or merged:** the toolbar, search, sort, custom order and
not-found handling of `SchedulesList`, `TemplatesList` and `SuitesList`
merge into `ProcessesList`. The overlays (`ScheduleOverlay`,
`TemplateOverlay`, `UseTemplateOverlay`, `TemplateFields`,
`SuiteOverlay`, `SuiteRunOverlay`, `SuiteRunPane`) are reused unchanged
apart from their close and open paths.

**Deleted:**

- `SchedulesList.jsx`, `TemplatesList.jsx`, `SuitesList.jsx`, and their
  tests;
- the Schedules, Templates and Suites sidebar entries;
- the `schedules`, `templates` and `suites` VIEWS and `AVAILABLE_VIEWS`
  entries (replaced by `processes`);
- their three `listOrder.js` keys, after the §5.6 merge.

**Unit tests, changed or added:**

- **New:** `ProcessesList.test.jsx`, covering:
  - one row per item of each kind, the kind chips and counts;
  - the `?kind=` round trip;
  - the derived last and next runs for a template fired by two
    schedules;
  - the workflow-schedule classification;
  - a system schedule's lock;
  - the create menu, and how it skips when one kind is filtered;
  - a declared row's chip.
- **New:** `processes.test.js`.
- **Changed:** `App.schedules.test.jsx`, `paths.test.js` (the §5.4
  table), `pinnedNav.test.js` (the §5.6 schedules, templates and suites
  rows, idempotence), `listOrder.test.js` (the merge), `Sidebar.test.jsx`,
  `CustomizePinnedOverlay.test.jsx`, `UseTemplateOverlay.test.jsx`,
  `SuiteRunPane.test.jsx`, `TemplateFields.test.jsx` (paths only),
  `ItemGlyph.test.jsx`.

**Playwright, changed:**

- `schedules_workflows.spec.js`, `templates_workflows.spec.js` and
  `suites_workflows.spec.js` are merged into `processes_workflows.spec.js`;
- `suite-run-dialog-order.spec.js`, `suite-release-batch-walkthrough.spec.js`,
  `recurring-composed-walkthrough.spec.js`,
  `workflow-schedules-candidate-walkthrough.spec.js`,
  `standing-work-switch-walkthrough.spec.js`,
  `inputs-automation-walkthrough.spec.js`, `keyboard-open-rows.spec.js`,
  `paths.spec.js` and `sidebar-navigation.spec.js` are updated.

**Playwright, new:** `processes-walkthrough.spec.js` against `grain
demo`. It covers:

- `/schedules/<id>`, `/templates`, `/suites/runs/<id>` redirecting;
- filtering to Suites;
- creating a template through New → Template;
- a pinned "Schedules" item from before the stage showing as Processes.

A `docs/test-runs/` report accompanies it.

**Route-authorization baseline impact:** none.

### Stage (c): the user tile and username {#SET-3}

**Builds:** §6, and the rest of §4.2 and §4.3.

**New:** `storageMigration.js` (with its call in `main.jsx`) and
`PreferencesContext.jsx`.

**Moved or merged:** `UserContext.jsx` → `PreferencesContext.jsx`
(time zone only), and `PinnedNavContext.jsx` reads and writes
`grain.pinned`.

**Deleted:**

- the user tile (`user-tile`, `user-settings-button`) and its
  `onOpenUserSettings`, `onOpenSettings` and `onOpenChangeUsername` props;
- `UsernameDialog.jsx` and `UsernameDialog.test.jsx`;
- the username box in `AccessSection`;
- `X-Grain-User` in `api.js`;
- every `creator: username` and `author: username` in §6.1;
- `migrateLegacyPinnedItems`, `pinnedItemTeam`, `legacyPinnedStorageKey`,
  `PIN_SCOPE`, `pinsMigratedRef`;
- `grain.username` in `setupTests.js`.

**Unit tests, changed or added:**

- **New:** `storageMigration.test.js`, covering:
  - each candidate order of §6.3 step 1;
  - the merge of two users' lists, with no duplicates and order kept;
  - the time-zone move;
  - removing every old key;
  - idempotence;
  - a throwing `localStorage`;
  - an already-migrated store left alone.
- **New:** `PreferencesContext.test.jsx`.
- **Changed:** `UserContext.test.jsx` (removed), `PinnedNavContext.test.jsx`,
  `pinnedNav.test.js`, `api.test.js` (no `X-Grain-User`),
  `NewTaskOverlay*.test.jsx`, `UseTemplateOverlay.test.jsx`,
  `TaskChat.test.jsx`, `InboxPage.test.jsx`, `DetailOverlay.test.jsx`
  (no creator or author in the bodies), `Sidebar.test.jsx` (no
  `user-tile`; the build stamp links to `/settings/upgrade`),
  `AccessSection.test.jsx` (shows the actor, no username box),
  `App.test.jsx` (a signed-out config shows `AccessDenied`, not a dialog).

**Playwright, changed:**

- `access-code-walkthrough.spec.js` and
  `security-perimeter-walkthrough.spec.js` (signed-out → access code);
- `fixtures.js` and `accesscode.js`, if they seed `grain.username`;
- `sidebar-navigation.spec.js`, `phone.spec.js` and
  `responsive-navigation-walkthrough.spec.js` (no user tile);
- `ui-followups-walkthrough.spec.js`.

**Playwright, new:** `user-tile-removal-walkthrough.spec.js` against
`grain demo`. It covers:

- seed `grain.username = "alice"` and `grain.pinnedNav.alice.default`
  holding a pinned board, plus `grain.pinnedNav.bob.default` holding a
  session;
- load and assert both are pinned, in that order;
- assert every old key is gone and `grain.storageVersion` is `"1"`;
- file a task and a comment, and check the stored creator and author are
  the deployment's actor.

A `docs/test-runs/` report accompanies it.

**Route-authorization baseline impact:** none. The server already
ignores `X-Grain-User` and the `creator` and `author` bodies (§6.1). This
stage only stops sending them, so no security test changes.

---

## Decisions, in one place

Each is restated from where it appears above, with the recommended
answer.

1. Health is System's default tab: **yes** (§2.1).
2. Metrics moves into System, `/metrics` redirects: **yes** (§2.1).
3. Connected executors → System → Sandboxes; join tokens → Settings →
   Executors: **yes** (§2.1).
4. Upgrade-check settings → Settings → General → Updates; the upgrade and
   database stay in System: **yes** (§2.1).
5. System becomes a view, and `/sandboxes` and `/caches` alias its tabs:
   **yes** (§2.1).
6. Per-repo settings stay on the repo page; Settings → Repositories is an
   index; the repo tab joins the path: **yes** (§2.3).
7. Settings is a view, not an overlay: **yes** (§3.1).
8. The groups and section order of §3.2: **as listed**.
9. Engine & Concurrency splits into Engine and CI: **yes** (§3.2).
10. "Integrations & Auth" → Credentials, and "Workflow & Tasks" → Tasks:
    **yes** (§3.2).
11. Memory sits under Agents: **yes** (§3.2).
12. Bare `/settings` opens General (the list on a phone): **yes** (§3.2).
13. The filter searches labels and helper keywords, not values: **yes**
    (§3.3).
14. Commit-on-gesture, with Save only on multi-line and composite cards:
    **yes** (§3.4).
15. Redirects are kept indefinitely: **yes** (§3.5).
16. System's entry is a top-bar icon beside the gear, and the drawer foot
    on a phone: **yes** (§4.1).
17. The footer is the build stamp alone, linked to Settings → Upgrade:
    **yes** (§4.3).
18. Triggers are listed read-only, by path: **yes** (§5.1).
19. Templates and suites show derived last and next runs: **yes** (§5.1).
20. Processes gets a new glyph: **yes** (§5.7).
21. The UI names nobody except on Settings → Access: **yes** (§6.1).
22. Creator and author are left to the server: **yes** (§6.1).
23. Several users' pins in one browser are merged: **yes** (§6.3).
24. Stages run (a) → (b) → (c) in sequence: **yes** (§7).
25. Stage (a) waits for #4880 and #4862–#4864: **yes** (§7).
