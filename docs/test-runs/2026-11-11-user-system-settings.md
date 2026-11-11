# User & System Settings Walkthrough — 2026-11-11

Settings' layout, filter, save/role/lock model, Preferences, Access, CLI,
Audit, General, Config repo, Models, Agents, Memory and Tasks, walked in a
real browser against a real `grain demo` server (task 4302).

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-4302-walk-through-settings-its`.
- **Commit tested:** `bff615b79` (`main` at the start of the run; build
  stamp `v0.0.0-20261111152614-bff615b792f6 · bff615b · 2026-11-11 15:26Z`,
  which General → About and the rail footer both print). The fixes below
  were re-verified on the rebuilt working tree (`897954671`).
- **Build:** `make frontend` (Vite bundle embedded in `pkg/ui/static`), then
  `go build ./cmd/grain`. Note that `make frontend` leaves `ui/node_modules`
  without Playwright; `npm ci` in `ui/` puts it back.
- **Servers:** `grain demo -addr 127.0.0.1:7811 -open=false -github-sim
  -fake-ci -secrets-store -access-code-file <file>` (base binary) and the
  same on `127.0.0.1:7812` (fixed binary), each over its own throwaway
  embedded store; signed in through `POST /api/auth/access-code` as
  `alice@example.com` or through the visible "Enter access code" page.
- **Browser:** Playwright headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by scripts
  outside the repository; `timezoneId: UTC`. Viewports 1440×900 and
  1280×600 (desktop), 820×1180 (tablet), 390×844 (phone, dark and light).
- **Model/tooling:** the sandbox has no `/home/debian/.gemini-api-key`, so
  no Gemini-backed tooling was used and `LOOK=1` was unavailable. Layout
  was checked through DOM geometry (bounding boxes, `scrollWidth`) and the
  `make ui-screenshots` contact sheet (not committed).

## Stubs (intercepted with `page.route`)

Everything else ran against the real server and its store.

| Route | Why |
| --- | --- |
| `PUT /api/settings` → one `503` | The demo never fails a save; used to see `Saving…`, `Couldn't save Max agent turns` and `Retry` recover against the real server. |
| `GET /api/settings` + `pendingRestart: ["pollInterval"]` | The demo hot-applies everything, so it never needs a restart; used for the `Restart needed` chip. |
| `GET /api/config`, `GET /api/settings` + `externalConfigRepo: "https://github.com/acme/grain-config"`, `configRepoOnly: true` | The demo has no external config repository and the sandbox no GitHub; used for the external-config-repo lock. |

No model-provider, identity, upgrade or GitHub stub was needed this run
(connection tests were not exercised against a live provider; see "Not
covered").

## Steps & outcomes

### Layout and paths

- The rail carries no Settings link; the header gear
  (`top-bar-settings`) opens `/settings/models`. On a phone the bar's gear
  (`phone-settings`) is the entry.
- Bare `/settings` on a phone lists the five groups (Models & sandboxes,
  Secrets & access, This browser, System / Operations, Managed in config
  repo) with no section open; Access opens under the back arrow, which
  returns to `/settings`. `scrollWidth` 390 at 390 px.
- Every section rides in the URL through reload and Back/Forward
  (`/settings/general` → `/settings/agents`, reload, Back → General,
  Forward → Agents).
- `Upgrade`, `Audit` and `CLI` show no state line; every other section
  walked shows `All changes saved`.
- `#card` deep links at 1280×600 land the card in view with its heading
  visible: `agents#external-agents` (top 269), `models#usage` (553 px
  tall card, top 306, heading 323), `agents#commit-identity`,
  `tasks#task-fields`.
- Old links: `/admin` → General; `/admin/workflow` → Tasks;
  `/admin/executors`, `/system/executors`, `/settings/hosts`,
  `/settings/hostds` → `/settings/hostds`; `/settings/credentials`,
  `/settings/legacy-capabilities`, `/admin/integrations`,
  `/system/integrations` → `/settings/capabilities#credentials`;
  `/settings/secrets` → `/settings/capabilities#other-secrets`;
  `/admin/upgrade`, `/system/upgrade` → `/settings/upgrade`; `/memory` →
  `/settings/memory`; `/config?path=README.md` →
  `/settings/config-repo/README.md`.
- Tablet (820×1180): the nav becomes the `Section` select; with dirty
  Standing instructions, choosing General and cancelling the prompt keeps
  `/settings/agents` and the select on `Agents`; accepting moves to
  General.

### Filter

- `/` focuses `Filter settings`. `house style` lists `Standing
  instructions for every run` under Agents; choosing it pushes
  `/settings/agents#promptExtension`, focuses and outlines the textarea,
  and Back returns to `/settings/models`.
- `time zone` → Preferences; `environment name` from inside
  `/settings/config-repo/README.md` leaves the file view for
  `/settings/general#environmentName` and focuses the box; a miss says
  `No setting matches 'zzzqqq'.`; Escape restores the groups.
- **Bug F1** (below): choosing a select (`Check the upgrade`, or
  `Automatic upgrade time of day` while automatic upgrades are off)
  focused the select's hidden native input.

### Save, role and lock model

- `Environment name` saves on Enter (one `PUT /api/settings`, nothing
  while typing), retitles the tab (`walk-4302 — grain`) and reads back
  after reload; restored to empty afterwards.
- Engine's `Poll interval` typed as `1m` reads back `1m0s` (restored to
  `10s`).
- `Max agent turns` = `-5` is refused by name (`Couldn't save Max agent
  turns`) with no request sent; putting the old value back clears it.
- A stubbed `503`: `Saving…` while in flight, then `Couldn't save Max agent
  turns` with `Retry`, which saved `77` against the real server (restored).
- Server refusals (`Deployment ID` = `not hex!`, a bad branch template)
  answer 400; the state line names the box and the field goes back to the
  stored value with the sample ID `14b7a176` still the placeholder.
- Dirty Standing instructions: `Unsaved changes in Standing instructions`,
  one nav dirty dot; the section nav, the Commit identity `Settings → Git`
  link, a rail link and a reload each ask first (`You have unsaved changes
  here. Leave and discard them?` / `beforeunload`); Discard restores the
  text; a clean leave never asks.
- `Restart needed` chip (stubbed `pendingRestart`) opens `/system/power`.
- External config repo (stubbed): General, Agents, Tasks, Memory and
  Models show the read-only banner with an edit link into the repo
  (`tables/grain_config.json`, `memory/`), every control is `:disabled`,
  and no `aria-disabled` control is left in the tab order. Access, CLI,
  Audit and Preferences have no banner and stay usable.

### Preferences

- Appearance Dark persists through reload (body `rgb(15, 16, 19)`);
  Auto restores it. Time zone `Asia/Tokyo` and timeline order are kept in
  `localStorage` (`grain.timeZone`, `grain.timelineOrder`); **no
  deployment-wide write** was sent from the section.
- With Tokyo chosen, Audit (`11/11/2026, 4:10:27 PM` → `11/12/2026,
  1:10:27 AM`) and Access added-at times (`11/9/2026, 3:58:58 PM` →
  `11/10/2026, 12:58:58 AM`) follow the zone; with none chosen they print
  in the browser's (UTC).
- Customize sidebar pinned `Settings → Memory`, `System → Power`, the
  `Board` board and the `Director` session, reordered and saved; the
  pinned `Power` opens `/system/power`. No API write was made.

### Access

- This browser: `Signed in with the access code. Changes made here are
  recorded as alice@example.com (access code), not as a person.`, matching
  `GET /api/whoami` (`label: "alice@example.com (access code)"`) and the
  rail's user tile. An access-code session has no `Your role` chip (it can
  make any change).
- Add allowed principal: `alice2@example.com` → `Entry must start with
  user:, group: or serviceAccount:`; `user:bob` → `user: entry must be a
  valid email address.`; `serviceAccount:bot@example.com` → must be
  `*.gserviceaccount.com`; a valid service account without a reason →
  `Give a reason to admit a service account.`; `user:carol@example.com` at
  the default `grain/operator` → `Give a reason: grain/operator holds
  dangerous scopes`. `grain/viewer` adds without one. Removing an entry
  (your own `user:alice@example.com` included) opens the pending-change
  summary, which refuses to apply a dangerous role's removal without a
  reason (`Give a reason: this change is recorded in the access history
  with why.`); the access history records the service account added with
  its reason. A per-row role change was not driven to completion this
  run.
- Log out returns to the `Enter access code` page. **Bug F2** (below):
  signing in from `/settings/agents#external-agents` came back without the
  `#external-agents` card.

### CLI and Audit

- CLI cards: Install `curl -fsSL http://127.0.0.1:7811/cli/install.sh | sh`
  (the demo is unversioned); Sign in `grain login http://127.0.0.1:7811
  --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires
  30d` with the "external agent API is off" warning and its link to
  `/settings/agents#external-agents` (gone once `Accept external agents`
  is on); Agent skills for Jetski, Claude Code, Codex and Gemini CLI. Each
  Copy button copies exactly its own command.
- Audit: `Configuration changes` names the actor (`alice@example.com
  (access code)`), surface and summary (`PUT /api/settings`), no secret
  values; at 390 px it stacks into blocks (no `<table>`, no overflow).

### General, Config repo

- Deployment card, Settings lock disabled with `unavailable without a
  config repository`, Updates (`Check the upgrade`: don't / file a
  proposal / file it and queue it (default); `Upgrade automatically`: off /
  on each release (recommended) / once a day; `Hold the queue until the
  check is done`), About matching the rail's build stamp.
- Config repo panel renders on the demo; the Config Repo Browser shows
  `README.md` (also through `/config?path=README.md`).

### Models

- New connection then Escape cleans the draft up without a Remove prompt
  (1 Remove button before and after). Remove asks first (`Remove the
  connection Gemini 2.5 Pro? A credential stored for it is deleted…`);
  dismissing keeps it. Disable / Enable persist through reload. New task's
  model picker offers `Deployment default (gemini-2.5-pro)`,
  `gemini-2.5-pro`, `gemini-2.5-flash`.

### Agents, Tasks, Memory

- Agents: Max agent turns, Standing instructions (Save/Discard), Task
  branches validation (above), `Accept external agents` round trip.
- Tasks: flipping `Auto-merge new tasks by default` persists and New
  task's `Auto-merge once checks pass` starts the same way (restored).
- Memory: a correction to the first deployment note shows `Unsaved changes
  in Memory`; Back asks first; Save, leave and reopen reads it back;
  restored afterwards. Memory is not in the rail unless pinned and
  `/repos/acme/widgets` does not mention it.

### Console and network

No console errors other than the deliberately stubbed `503` and the
`400`s of the refused Deployment ID / branch template, and the `401`s of
the signed-out login page. `ERR_ABORTED` `/api/tasks` long-polls were
navigation, not failures.

## Findings

### F1 (fixed): a filtered select focused its hidden native input

- **Reproduction:** `/settings/models`, Filter settings → `check the
  upgrade` → choose it. `document.activeElement` is `<input
  name="upgradeCheckMode" aria-hidden="true" tabindex="-1"
  class="MuiSelect-nativeInput">` — nothing visibly focused, and Tab starts
  over. Same for `upgrade time of day` while automatic upgrades are off
  (the card's first control is that select).
- **Cause:** `SettingsPage`'s reveal focused the first
  `input:not([type=hidden])`, which for a MUI Select is its hidden native
  input.
- **Fix:** focus the first control a keyboard reaches (skipping
  `aria-hidden` / `tabindex="-1"` inputs, taking `[role=combobox]`).
  After: the focused element is `DIV role=combobox`.

### F2 (fixed): signing in dropped a deep link's `#card`

- **Reproduction:** signed out, open `/settings/agents#external-agents`,
  sign in with the access code. Lands on `/settings/agents` at the top
  (card at y 1239 of a 600 px window).
- **Cause:** the fragment never reaches the server, and
  `localReturnPath` (rightly) refuses `#` in `next`.
- **Fix:** the login script appends the address bar's fragment to the
  form's `action`; a 303 whose `Location` has no fragment keeps the
  request's, so the browser lands on `/settings/agents#external-agents`
  with the card in view (y 269), a wrong code first included.

**Regression coverage:**
`ui/e2e/user-system-settings-focus-and-deep-link.spec.js` (both).
Passing alongside `user-system-settings-walkthrough.spec.js` and
`access-code-walkthrough.spec.js` (17/17); `go test ./pkg/ui -run Access`
and `SettingsPage.test.jsx` (43/43) pass.

## Not covered this run

- A controller-local config repository (`grain daemon`) — Settings lock
  on, held config-repo pull requests, sync-failure alert and Inbox row,
  reset notices, secrets backup — and identity sign-in / `Access Denied`;
  the 2026-11-09 run covers those against `grain daemon` and they were not
  re-walked here.
- Model connection Test against a live provider, host placement for
  `ambient` credentials, paused-model resume, and Token usage budgets.
- The `custom.<name>:legacy` link: the demo's task fields carry no
  stranded value, so the link was not offered.
