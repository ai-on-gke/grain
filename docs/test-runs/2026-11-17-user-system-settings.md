# User & system settings walkthrough — 2026-11-17 (task 6592)

- **Date:** 2026-11-17, 23:05–01:00 UTC, one run.
- **Task:** task 6592, branch
  `grain/53549e42/task-6592-walk-through-settings-its`, base `main`.
- **Commit tested:** `07e43408c` ("Merge pull request #1803"), then the
  fixes below on top of it, rebuilt and walked again.
- **UI walked:** the new UI at `/next/settings/...` for every step. Settings
  is in `ui-ng/src/app/app.routes.ts` and `ui-ng/src/app/shell/nav.ts`, so no
  step needed a classic page. Classic `/settings` was not walked or changed:
  none of the bugs below happens there (classic already validates Grant
  access, has Copy buttons, and disables the Settings lock without a config
  repo).
- **Environment:** Debian sandbox, `linux/amd64`, Go 1.26.10, Node.js
  v20.19.0. Playwright from `ui-ng/package-lock.json`, headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC.
  Viewports 1440×900, 820×1180 and 390×844. Built with `make frontend`
  (then `npm run build` in `ui-ng/` after each fix). The ui-ng e2e harness
  (`ui-ng/e2e/fixtures.ts`) builds `grain` with `-tags grain_e2e` and signs
  in through the access code as `alice@example.com`.
- **No `/home/debian/.gemini-api-key`** in this sandbox, so no model was
  called, and `LOOK=1` could not describe screenshots. Layout was checked
  with bounding boxes and `scrollWidth`.
- **Servers:** `grain demo -addr 127.0.0.1:0 -open=false -access-code-file
<file>` over a throwaway store, one per Playwright worker, and the same
  with `-external-config-repo https://github.com/example/walk-config` for
  the lock model.
- **How it was driven:** throwaway Playwright scripts (not committed) acting
  only through visible controls: the gear, the nav, `/`, typing, `Escape`,
  `Tab`, `Enter`, clicks, reloads. Every page logged page errors,
  console errors and warnings, failed requests, each `/api/` answer of 400
  or more with its body, every non-GET `/api/` request, and every native
  dialog.

## Stubs

None. Every request reached the real server. The GitHub, identity, upgrade,
model-provider and config-repo boundaries were not reached by the steps
walked (see "Not covered"), so nothing had to be intercepted.

## Steps and outcomes

### 1. Layout and paths — PASS

- The gear in the header rail is the one Settings link; it opens
  `/next/settings` on **Models**. Five groups: Models & Compute,
  Repositories & Code, Work & Agents, Access & Security, Instance &
  Preferences (the `/next/` grouping, not classic's).
- Each of the 20 sections loads at `/next/settings/<id>` with its heading,
  no save bar while clean, and `scrollWidth` 1440.
- `/next/settings/hosts` → Hosts, `/settings/legacy-capabilities` →
  `capabilities#credentials`, `/system/upgrade` → Upgrade,
  `/config?path=tables/grain_config.json` and
  `/settings/config-repo/README.md` → the Config repo file viewer
  (`?path=`). The `/admin/...` and `/system/...` redirects are covered by
  `next-settings-walkthrough.spec.js` (passes).
- `/next/memory` opens **Context**, the `/next/` rail page, not Settings →
  Memory (unchanged; noted in the 11-14 report too).
- 820 px: the side nav stays a list (no Section select), no overflow.
- 390 px: bare `/settings` lists every section; a section opens alone under
  `settings-mobile-back`, which returns to the list. `scrollWidth` 390.

### 2. Filter — FAIL, fixed (Bug 1)

- `/` focuses **Filter settings**; `zzzqqq` says `No settings sections
match "zzzqqq".`; Escape clears it and restores all five groups.
- **Bug 1:** typing a heading or field label the page shows found nothing
  for most of them. Every card heading and `.s-label` on ten sections was
  typed in; misses included Models' "Fallback models", "Per-model reasoning
  effort", "Connection ID"; Tasks' "Branch name template", "PR rollout (%)";
  Agents' "Default capabilities (comma-separated)"; Access's "Grant access",
  "Token name"; General's "Broadcast message to active tasks"; and every
  Config repo field. The matcher only read section keywords, and kept
  punctuation, so "(comma-separated)" could never match.

### 3. Save, role and lock model — PASS with fixes (Bugs 2, 3)

- `/next/` stages edits in a section draft with a **Discard / Plan /
  Preview changes / Save changes** bar ("1 unsaved change"). The draft
  survives moving to another section and back. In-app navigation does not
  ask (the draft lives in a root service); a reload does (`beforeunload`,
  `next-settings-walkthrough.spec.js`).
- Engine poll interval `1m` was sent as `{"pollInterval":"1m"}` and reads
  back `1m0s`, after Save and after a reload. Restored to `10s`.
- **Bug 2:** Tasks → Branch name template `bad..ref~{id}` went to the
  server, which answered `400 taskBranchTemplate branch template produces
invalid branch name "bad..ref~123"`, and the banner showed the JSON key.
  It now reads "Branch name template: branch template produces invalid
  branch name "bad..ref~123"", with the sample ID still visible.
- External config repo: General, Agents, Tasks, Memory and Caches show the
  banner and a "View in config repo" link (`tables/grain_config.json`,
  `memory/deployment.md`), and their fields are disabled; disabled pill
  selects are `disabled` buttons, so off the keyboard. Access, CLI and
  Audit stay usable.
- **Bug 3:** General's **Lock settings in UI** switch was enabled with no
  config repo at all (the plain demo), and under the external config repo.
  Turning it on in the plain demo makes every config-managed section
  read-only with nowhere to send a pull request. Classic disables it
  (`noConfigRepo`, `allowInExternalMode`). Now it is disabled in both
  cases, and its hint says why.

### 4. Preferences — PASS

- Light / Dark / System write `grain.themeMode` to `localStorage` and the
  document's `data-theme`; no non-GET `/api/` request was made.
- The display time zone select lives on General (Browser local time, UTC,
  Los Angeles, New York, London); per-browser only.

### 5. Access — FAIL, fixed (Bug 4)

- "Your role: operator"; three allowlist rows with per-row Role selects.
- **Bug 4:** Grant access took anything: `user:not-an-email`, `group:`,
  `serviceAccount:foo@bar.com` (not a service account address) all left
  **Grant access** enabled, and a service account needed no reason. Now
  the entry is checked as classic `validatePrincipalEntry` does: an inline
  error by name, `aria-invalid`, Grant disabled; a bare address is filed as
  `user:` (as the server does); a service account asks for a reason before
  anything is sent. Granted `Walk-Carol@Example.com` → row
  `user:walk-carol@example.com`, then Remove → confirm → gone.

### 6. CLI and Audit — PASS with fix (Bug 5)

- CLI shows `curl -fsSL <origin>/cli/install.sh | sh`, `grain login <origin>
--name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires
30d`, and `grain skill install --jetski # or --claude, --codex, --gemini`.
- **Bug 5:** none of these had a Copy button (classic has one per card).
  Now Copy install / sign-in / skill command buttons copy each line (the
  skill one without its comment), with a snackbar.
- Audit: one table of config, secret and access entries, times in the
  display zone (`Nov 17, 2026, 11:28:15 PM UTC`), actor
  `access-code:alice@example.com`, no secret values (secret rows name the
  path). 390 px: `scrollWidth` 390.

### 7. General — PASS (partial port)

- Environment name (saved `walk-env`, read back after reload, restored),
  Deployment ID, Display timezone, Settings lock (Bug 3), Broadcast.
  Updates and About are not on `/next/` General; the running version
  `v1.2.0` is on Upgrade and matches the rail's foot.

### 8. Config repo — PASS (demo has no controller-local repo)

- The panel, remote/KMS fields and file viewer render. The demo answers
  `GET /api/state-repo/files` with `501 this deployment's UI does not
manage a state repository`, logged in the console each time the section
  opens. Sync-failure alerts are covered by
  `next-config-repo-sync-error.spec.ts`.

### 9. Models — PASS (read, layout, lock)

- Default model `gemini-2.5-pro`, fallback models, per-model effort, the
  Gemini 2.5 Pro connection with Test connection / Credential / Edit /
  Remove, the Add form and the token budget card render. Under the external
  config repo the default-model pickers are disabled and the connection
  credential and Test buttons stay enabled. `next-settings-models.spec.js`
  passes. No connection test reached a provider (no key).

### 10. Agents, Tasks, Memory — PASS (partial port)

- Agents: Max agent turns, Default capabilities, Standing console sessions
  (Director). Tasks: approval and auto-merge defaults, Show closed tasks,
  follow-up limits, Branch name template (Bug 2), quality control.
  Memory: Standing instructions, deployment memory, two documents.
  `next-settings-walkthrough.spec.js` covers the Memory save round trip.

### Console and network

Each sign-in logs the expected first-load `401`. Config repo logs the demo's
`501`. The only other 4xx was the Branch name template refusal (Bug 2). No
uncaught page error and no 5xx.

## Not on `/next/` yet (no change)

Still missing from the `/next/` Settings, as in the 11-14 report: the
per-field commit-on-change model with the header state line and nav dirty
dot; the tablet Section select; field-level filter results that focus the
field; the Restart needed chip; Customize pinned items; Access's This
browser card (sign-in method, Log out) and People & groups / Service
accounts tables with added-at times; General → Updates and About; Tasks →
Task fields and Default grants; Agents → Task branches and External agents
(`#external-agents` lands on Access's token card); Models' Enable/Disable,
Credentials stored/ambient, Run on host with fallback hosts, paused-model
resume and the Where each agent CLI runs card; Memory's per-repository
notes; and Audit's three separate logs.

## Not covered

- No model key: no provider connection test or model call.
- Identity/IAP sign-in, `Access Denied` and Log out (no `/next/` control).
- A controller-local config repo and held config-repo pull requests (need
  `grain daemon` with `grain/config`).

## Bugs

All fixed in `ui-ng/` only (classic does each of these right already).

1. **Filter settings missed field labels.** `filterSettingsGroups` now also
   reads each section's headings and field labels
   (`SECTION_FIELD_LABELS`) and ignores punctuation.
2. **A refusal named the JSON key.** `labelSettingError` names a refused
   field by its label in the error banner.
3. **Settings lock was live with no config repo.** Disabled without one or
   under an external one, with the reason in its hint.
4. **Grant access sent malformed entries.** `checkPrincipalEntry` refuses
   them inline; a service account needs a reason.
5. **CLI commands had no Copy buttons.** One per line.

## Tests run

- New: `ui-ng/e2e/next-settings-filter-access-cli.spec.js` (every heading
  and label finds its section; Grant access refusals and the service
  account reason; CLI copies; Branch name template label; Settings lock
  without a config repo) and
  `ui-ng/e2e/next-settings-external-config-repo.spec.js` (Settings lock held,
  Access/CLI/Audit usable). New unit tests in
  `ui-ng/src/app/settings/settings-filter-access.spec.ts`.
- `npx playwright test` on those two plus
  `next-settings-walkthrough.spec.js` (16 passed) and
  `next-full-system.spec.js next-qa-settings.spec.js
next-settings-models.spec.js` (17 passed), `--project=desktop`.
- `npm run lint`, `npx vitest run` (one existing expectation updated: Grant
  access now sends the canonical `user:bob@example.com`), `npx prettier
--check src e2e`: all pass.
- Screenshots: `ui/e2e/screenshot.mjs` for `/next/settings/access`, `cli`
  and `general` at desktop and phone, light and dark. Bounding boxes: the
  Copy buttons sit inside the CLI card and wrap one per line at 390 px; the
  entry error is left-aligned under its input; no horizontal overflow at
  1440 or 390.
