# 2026-10-20: the Settings panel, stage (a)

Task 4882, building stage (a) of
[docs/design/settings-panel.md](../design/settings-panel.md) (SET-1):
one Settings view behind a gear in the top bar, a status-only System
view beside it, Memory in Settings, and the team leftovers gone.

## How it was run

- `grain demo` from this branch, started by the Playwright fixtures
  (`ui/e2e/fixtures.js`) on a throwaway store; the built frontend
  (`make frontend`).
- Playwright 1.62 Chromium headless, desktop 1280x720 unless the test
  says otherwise, 390x844 for the phone case and 768x1024 for the tablet
  case.
- No grain API was mocked or stubbed. Nothing external was stubbed
  either: the walkthrough reaches no external boundary.

## What `ui/e2e/settings-panel-walkthrough.spec.js` covers

| Check | Result |
|---|---|
| The gear opens Settings on General (`/settings/general`), pressed while Settings shows; the System icon opens System on Health (`/system`) | pass |
| The sidebar is flat: no `team-section` box, no Memory entry, no System/Metrics footer | pass |
| One setting edited in each section that has one, then read back after a reload: General (environment name), Agents (max agent turns, and the standing instructions through their own Save), Memory (acme/widgets' memory), Tasks (follow-up depth), Credentials (GCP project), Executors (a join token), Engine (poll interval), CI (jobs per run), Sandboxes (CPU overcommit), Caches (Actions cache size, when the demo runs it), Preferences (Dark) | pass |
| Access: `grain demo` runs no relay client, so the relay card is the notice and has nothing to edit; the "This browser" card says how it is signed in | pass (no edit possible in the demo) |
| Models, Git and Config repo are the existing connection and config-repo panels, moved unchanged; their own walkthroughs (`deployment-connections-models-walkthrough`, `git-connections-multihost-walkthrough`, `provider-connection-lifecycle-walkthrough`) edit and reload them at the new paths | covered there |
| Repositories is read-only: a row per repository, linking to its page | pass |
| Every redirect of §3.5 (`/admin`, `/admin/<tab>`, `/system/<settings tab>`, `/settings`, `/memory`, `/sandboxes`, `/caches`, `/metrics?window=30d`) lands on its new path, and Back returns to the page before rather than to the old path | pass |
| Filtering "overcommit" lists CPU and Memory overcommit; picking CPU overcommit opens Sandboxes, focuses the box and puts `#sandboxCpuOvercommit` in the address | pass |
| A toggle (Show closed tasks by default) autosaves and survives a reload | pass |
| An edited standing instruction marks the section dirty (header line and nav dot), leaving it asks first, and Discard clears it | pass |
| 390x844: the bare `/settings` is the section list; a section opens under a back arrow, which returns to the list | pass |
| 768x1024: the nav folds into a Section select, which opens the picked section | pass |

## Screenshots

Every Settings section and System tab, at 1360x900, light then dark
(from the same demo store, after the walkthrough's edits):

- [Settings, light](2026-10-20-settings-panel/settings-light-sheet.jpg)
- [Settings, dark](2026-10-20-settings-panel/settings-dark-sheet.jpg)
- [System, light](2026-10-20-settings-panel/system-light-sheet.jpg)
- [System, dark](2026-10-20-settings-panel/system-dark-sheet.jpg)

A first look at them found the Models, Git, Tasks, Config repo and
Memory cards repeating the section's own heading over a panel that
already names its parts; those cards carry the name as their accessible
label only now.

## Found and fixed during the run

- The phone bar's gear measured 36px, under the 40px touch-target rule
  `phone.spec.js` holds every control on a phone to; it is a small icon
  button now, which the phone stylesheet lifts to 40px.
- On a phone the desktop top bar is drawn inside the drawer, so its
  System and Settings buttons appeared there twice beside the drawer's
  own System row; App passes them to the desktop bar only.
- The specs that looked for the old overlays' "Settings" and "Metrics"
  headings found nothing: the section nav is now headed "Settings", and
  System → Metrics carries a "Metrics" heading, as the overlays did.
