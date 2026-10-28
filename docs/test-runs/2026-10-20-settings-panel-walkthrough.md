# 2026-10-20: the Settings panel walkthrough, checked against SET-1

Task 4895, the test task for stage (a) of
[docs/design/settings-panel.md](../design/settings-panel.md) (SET-1, at
design rev e4761b5). Task 4882 wrote
`ui/e2e/settings-panel-walkthrough.spec.js` and recorded its first run in
[2026-10-20-settings-panel.md](2026-10-20-settings-panel.md). This run
reviews that spec and the stage's unit tests against the section's
test list, fills the gaps, and runs the walkthrough again.

## How it was run

- Base: `main` at 4d70c3c, plus this branch's changes.
- `grain demo` from this tree, started per worker by the Playwright
  fixtures (`ui/e2e/fixtures.js`, the same `go run ./cmd/grain demo
  -addr 127.0.0.1:0 -open=false` server) on a throwaway store; the
  frontend built with `npm ci && npm run build`.
- Playwright 1.62.1, Chromium headless:
  `npx playwright test e2e/settings-panel-walkthrough.spec.js --project=chromium`.
- No grain API was mocked or stubbed. Nothing external was stubbed
  either: the walkthrough reaches no external boundary.

## The section's list, check by check

| SET-1 bullet | Test | Result |
|---|---|---|
| The gear opens Settings, and the System icon opens System → Health | "the gear opens Settings and the System icon opens Health" | pass |
| Every `/admin/<tab>`, `/system/<settings-tab>`, `/settings`, `/memory` and `/metrics?window=30d` redirect lands where §3.5 says, and Back does not return to the old path | "every old path lands where §3.5 says, and Back skips it": 41 paths, one or more per row of the §3.5 table | pass |
| Filtering "overcommit" finds and focuses CPU overcommit | "filtering 'overcommit' finds CPU overcommit and focuses it" | pass |
| A toggle autosaves and survives reload | "a toggle autosaves and survives a reload" (Show closed tasks by default) | pass |
| An edited standing instruction marks the section dirty, the leave guard asks, and Discard clears it | "an edited standing instruction asks before leaving, and Discard clears it" | pass |
| 390×844: the section list, opening a section, back to the list | "on a phone › lists the sections, opens one, and goes back to the list" | pass |
| 768×1024: the section select | "on a tablet › picks a section from the Section select" | pass |

The spec's other test, "edits one setting in each section, and every one
survives a reload", is unchanged from 4882's run and still passes.

All 8 tests passed (53.5s).

## Gaps found in the spec, and filled

- **Redirects.** The spec checked 21 of the old paths. It now walks
  every row of §3.5: `/admin/general`, `/admin/accounts`, the remaining
  `/admin/{sandboxes,top,pipeline,power}` System tabs, an unknown
  `/admin/<tab>` (→ `/settings/general`), `/system/<id>` for every
  settings tab id of the table (→ that tab's Settings section), an
  unknown `/system/<tab>` (→ `/system`), and `/cache` and
  `/actions-cache` (→ `/system/caches`). Each one is opened from `/`,
  and Back has to land on `/`.
- **The leave guard.** The test dismissed a dialog without checking that
  one came up. The page is still on Agents before any navigation lands,
  so a guard that never asked could still have passed. The test now
  records the dialog and asserts it was a `confirm`.

## Unit tests

- `ui/src/settingsIndex.test.jsx` (§3.3). The completeness check was
  already there: every section that draws fields is rendered with a
  fixture `/api/settings`, and its `data-setting-id`s have to equal the
  index's entries for that section. Added: every index entry of those
  sections has to point at an anchor the section really draws. It
  failed on `card:credentials:capabilities`, whose anchor `capabilities`
  no section draws, so picking "Capabilities" in the filter scrolled to
  nothing. The entry now points at the Credentials card, which holds the
  Capabilities panel. That index fix is the only change outside tests.
- `ui/src/useSettingsSection.test.jsx` (§3.4). Saving, dirty, error and
  retry, rollback and the leave guard were already covered. Added: the
  guard stops asking once Settings unmounts, and a section with several
  dirty Save cards names all of them, with a failed save's error taking
  the status over them.
- `ui/src/paths.test.js` (§3.5). The `buildPath(parsePath(old))` table
  now has every row of the redirect table. Added `/system/<id>` for
  `general`, `models`, `git`, `access`, `accounts` and `executors`, plus
  `/cache` and `/actions-cache`. The bare `/settings` is checked as
  Settings' own entry: it stays `/settings`, the phone's section list,
  and SettingsPage opens General from it on wider screens, which the
  walkthrough checks.

## Not changed

- Matching ignores case in the tab segment (`/admin/Logs`,
  `/system/STATE`), as §3.5 says it should "as `adminTabFor` does now".
  The first segment is still matched exactly, so `/Admin` and `/Memory`
  open the task list, the same as any other mis-cased top-level path.
  §3.5 does not ask for more, so no test or code was changed for it.
