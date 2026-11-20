# User & system settings walkthrough — 2026-11-20 (task 7548)

## Environment

- **Repository**: `ai-on-gke/grain`, branch
  `grain/53549e42/task-7548-walk-through-settings-its`, base `main`.
- **Commit tested**: `2c44ffa0b` (`main` at the start of the run; unversioned
  build, so General → About reads `2c44ffa` and the rail foot
  `grain v0.0.0-20261120080048-2c44ffa0b8af`). Fixes were re-verified on a
  rebuild of the same commit plus this branch's `ui-ng/` changes.
- **Build**: `make frontend` (ui, ui-ng, ui-hub) then `go build ./cmd/grain`.
- **Browser**: Playwright headless Chromium from `ui-ng/node_modules`,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `timezoneId: UTC`.
  Throwaway driver scripts lived in `/var/tmp/walk/` (not committed). They
  signed in through the real access-code form, then drove visible controls
  (section nav, filter box, `/`, `Escape`, pill selects, text/number boxes,
  the section save bar, Discard, Retry, confirm dialogs, Back/Forward,
  reload) and recorded `pageerror`, console errors, failed requests and every
  non-GET `/api/` call. Widths: 1440×900, 1280×600, 820×1180 (tablet),
  390×844 (phone).
- **Gemini**: no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed look-over; layout was checked through DOM geometry,
  `scrollWidth`/`clientWidth` and axe-core.
- **Servers** — real `grain demo` processes over throwaway embedded stores
  (SQLite, `TMPDIR=/var/tmp/walk`), access code from `-access-code-file`:

| Demo | Port | Binary | Flags | Used for |
| --- | --- | --- | --- | --- |
| A | 8420 | base | `-github-sim -fake-ci` | primary walkthrough, bug reproduction |
| B | 8421 | base | `-github-sim -external-config-repo https://github.com/acme/grain-config` | external config repo lock, Config repo viewer failure |
| C | 8430 | fixed | `-github-sim -fake-ci` | re-verification, Access/Memory/Models, classic redirects |
| D | 8431 | fixed | `-github-sim -external-config-repo https://github.com/acme/grain-config` | re-verification under the external lock |

## Stubs

Everything ran against the real server and real compiled bundles. The only
intercepted route in the walkthrough was:

| Route | Stub | Why |
| --- | --- | --- |
| `POST /api/settings` | first call answered `503 {"error":"temporarily unavailable"}`, then passed through | to see a failed save, its header line and Retry (the demo never fails a settings write by itself) |

`-github-sim` stands in for GitHub. Model-provider tests were not stubbed:
`grain demo`'s real `POST /api/agent-keys/{framework}/check` answers 404
("this deployment's UI is not wired to the daemon that holds them"), and
that was the state walked. No identity, upgrade or config-repo boundary was
stubbed in this run (the 2026-11-15 report covers IAP `Access Denied` and the
internal `grain/config` lock with stubs; nothing in those areas changed since).

## Which UI each step ran against

All steps ran on `/next/` except where noted. `/next/` has no page yet for
these, so they were only checked to load on the classic UI at `/`:
Preferences' task-timeline order and pinned-item customisation (classic
`/settings/preferences`), Tasks' Task fields / Default grants (`GrantsPicker`)
/ Backlog limits, and General's Updates card (on `/next/` the upgrade
controls live in Settings → Upgrade).

## Results

### 1. Layout and paths — PASS

- `/next/settings` on desktop opens Models (heading `Models`, URL unchanged);
  on a phone it shows the five groups (`Models & Compute`, `Repositories &
  Code`, `Work & Agents`, `Access & Security`, `Instance & Preferences`) with
  no section open; choosing Audit opens it alone under `← All settings`, which
  returns to the list. No horizontal overflow at 390 px.
- All 20 sections load by URL with their heading. Upgrade, Audit, CLI (and
  Preferences, Config repo) show no `All changes saved` line.
- Back/Forward between sections restores the section; old links land where
  expected: `/admin` → General, `/memory` → `/settings/memory`,
  `/settings/credentials` → `/settings/capabilities#credentials`,
  `/config?path=…` → `/settings/config-repo/<file>`.
- `/` focuses `Filter settings`; `max turns` → Agents, `time zone` →
  General, Preferences; `branch` → Repositories, Tasks, Agents; `zzzz` → `No
  settings sections match "zzzz".`; Escape restores all 20.
- Gaps against the brief (not bugs in what exists): the `/next/` filter
  matches sections, not individual fields, so it does not reveal/outline a
  field; at 820 px the nav stays a list rather than becoming a Section select;
  the rail section label still reads `Registry & Hub`.

### 2. Save, role and lock model — PASS after Bug 1

- `/next/` uses a section save bar: edits mark the section dirty (`Unsaved
  changes`, dirty dot in the nav, `1 unsaved change` / Discard / Plan /
  Save changes), and nothing is written until `Save changes`. This differs
  from the brief's commit-on-change model but is consistent across sections.
- Out-of-range numbers are refused by name before saving (`Max agent turns
  must be a whole number of at least 0.`, `Max unreviewed follow-up depth must
  be a whole number of at least 1.`) and the error clears once restored.
- Leaving a dirty section by the nav, the rail, in-app Back, or a reload asks
  first (`Discard unsaved changes? You have unsaved changes in Agents…`;
  reload raises `beforeunload`); Cancel keeps the draft and the URL; a clean
  section never asks.
- Saved `Max unreviewed follow-up depth` 2 → 3, reloaded, read back 3, and
  restored.
- **Bug 1** (fixed): a failed save left the header at `Unsaved changes`, and
  the banner showed the raw server text — for a refused branch template,
  `taskBranchTemplate branch template must contain {id}` — with only
  Dismiss. Now the header reads `Couldn't save Max unreviewed follow-up
  depth` / `Couldn't save Branch name template`, the banner names the field by
  label (`Branch name template: branch template must contain {id}`) with a
  Retry button, and both clear on a successful save or Discard.
- External config repo (B/D): General, Agents, Tasks and Memory render the
  `Managed in config repo` banner with `View in Config repo` →
  `/next/settings/config-repo?path=tables%2Fgrain_config.json`
  (`memory%2Fdeployment.md` for Memory) and their fields disabled; Models
  connection fields are disabled (4/30 controls enabled: credentials and
  test); Preferences, Access, CLI and Audit stay fully usable.

### 3. Preferences — PASS

- Choosing `Asia/Tokyo (JST)` in `Display timezone` changes Access added-at
  stamps (`Added Nov 18, 2026, 09:41:45 PM GMT+9`) and Audit entries, and the
  same choice shows in General; no `/api/` write was made.
- General's own `Display timezone` sat in the "Deployment identity & UI lock"
  card with nothing saying it is per-browser. **Bug 3** (fixed): it now says
  `This browser only, like Preferences: …never saved for the deployment.`

### 4. Access — PASS

- This browser: `Signed in with the access code. Changes made here are
  recorded as alice@example.com (access code).`, `Your role: operator`.
- Grant access: `bob@example.com` refused (`must start with user:, group:,
  serviceAccount: or delegate:`), `serviceAccount:bot@other.com` refused
  (`*.gserviceaccount.com`), a valid service account without a reason refused
  (`give a reason to admit a service account`). `user:bob@example.com` added
  (`201`), survived reload, Remove asked `Remove user:bob@example.com?` and
  deleted it (`200`). Removing your own entry asks `Remove your own access
  entry?` with `Confirm self-removal`.
- Log out from `/next/settings/access` returns to the access-code prompt with
  the URL kept.

### 5. CLI and Audit — PASS

- CLI: `curl -fsSL <origin>/cli/install.sh | sh`, `grain login <origin> --name
  127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d`, the
  warning that external agents are off with an in-app link to
  `/next/settings/agents#external-agents`, and four `grain skill install`
  commands (`--jetski`, `--claude`, `--codex`, `--gemini`) each with Copy.
- Audit lists configuration changes with time, surface, actor and summary;
  filter/category/window controls work; no overflow at phone width. Both
  stay usable under the external config repo.

### 6. General — PASS (Updates is on Settings → Upgrade in `/next/`)

- Environment name, Deployment ID, Show closed tasks by default, Settings
  lock disabled with `Unavailable without a config repository`, Broadcast,
  and About (`2c44ffa`, the commit in the rail's build stamp).

### 7. Config repo — PASS after Bug 2

- **Bug 2** (fixed): the file viewer swallowed a failing
  `GET /api/state-repo/files` — `501` with no state repository (A), `500`
  "names a repository and nothing more" under `-external-config-repo` (B) —
  and kept saying `Select a config file to inspect…` with nothing to select,
  so the lock banner's `View in Config repo` link led to a blank viewer. It
  now says `Couldn't list the config repository's files: <server reason>`.

### 8. Models — PASS (with a note)

- Default model `gemini-2.5-pro`, reasoning effort, fallback models, the
  connection row (`Gemini 2.5 Pro`, pinned models), Token budget card.
- Remove asks first (`Remove connection gemini-pro?`); Cancel keeps it.
- Test connection on the demo answers 404; the row shows the generic `Could
  not test the saved connection. Check your access…`. Showing the server's
  text was tried and reverted: `next-settings-connection-tests.spec.js`
  deliberately keeps error bodies off the page so a credential echoed in one
  cannot leak.

### 9. Agents, Memory and Tasks — PASS

- Agents: Max agent turns validation, Standing instructions joins the save
  bar (no card-level Save), Commit identity links in-app to
  `/next/settings/git` and the dirty-leave prompt fires before it.
- Memory: deployment memory edit saved (`PUT /api/memory 200`), read back
  after reload, reverted and read back again; repository notes for
  `acme/widgets` and its documents are listed; Memory is not in the rail.
- Tasks: approval/auto-merge switches, follow-up depth/fan-out, branch
  template (server-side `{id}` validation, see Bug 1), quality control.

### Console and network

Only expected noise: the `401` for the signed-out first load, the
`501`/`500` file list (now explained on the page, Bug 2) and the demo's
`404` connection test. No uncaught page errors.

## Bugs found and fixed

| # | Repro | Fix | Coverage |
| --- | --- | --- | --- |
| 1 | `/next/settings/tasks`, set Branch name template to `no-placeholder`, Save changes → header `Unsaved changes`, banner `taskBranchTemplate branch template must contain {id}` + Dismiss only | `SettingsStateService.failedSave`, `failedSaveLabels`, `readableSaveError`; header `Couldn't save <field>`; banner Retry | `ui-ng/e2e/next-settings-save-failure.spec.ts`, `settings-state.service.spec.ts` |
| 2 | `/next/settings/config-repo` on a demo without a state repo, or with `-external-config-repo` → empty viewer saying "Select a config file" | `configFilesError` shown in the viewer | `next-settings-save-failure.spec.ts` |
| 3 | `/next/settings/general` → `Display timezone` in the deployment card, no hint it is per-browser | hint text | `next-settings-save-failure.spec.ts` |

Classic UI: none of these were broken in the classic pages in a way users hit
today, so no classic change was made.

## Tests run

- `npx vitest run src/app/settings` (ui-ng): 80 passed.
- `npx playwright test e2e/next-settings-save-failure.spec.ts
  e2e/next-user-system-settings.spec.ts e2e/next-settings-connection-tests.spec.js
  e2e/next-settings-models.spec.js --project=desktop`: all passed on the
  final bundle.
