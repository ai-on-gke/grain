# 2026-10-23: a refused settings box put back reads saved again

The fix for the stuck "Couldn't save <field>" status found in the
2026-10-23 exploratory pass (`2026-10-23-exploratory-ui-defect-discovery.md`).
A box the server refused (Poll interval `banana`) and then put back to
the value the server still holds kept the header on "Couldn't save
Poll interval", the Retry button and the error banner up. It now goes
back to "All changes saved" and the banner is dismissed, with no
request sent. The screenshots are in `2026-10-23-settings-error-cleared/`.

## How it was run

- `grain demo` over a throwaway store, built from this branch and from
  its base, each embedding its own production frontend
  (`cd ui && npm run build`).
- `main`'s `ui/e2e/screenshot.mjs` (not on `candidate` yet), with one
  local addition run before each shot: fill Poll interval with
  `banana`, Tab, wait for "Couldn't save Poll interval", fill the stored
  value back, Tab, wait 1.5s.
- `/settings/engine` at desktop (1440×900) and phone (390×844), light
  and dark. Chromium headless shell 151.
- No stubs. The 400 comes from the demo's real `PUT /api/settings`.

## What it shows

- `before-contact-sheet.png`: every shot has the header on "Couldn't
  save Poll interval" with Retry, and the banner reading
  `pollInterval: time: invalid duration "banana"`.
- `after-contact-sheet.png`: every shot reads "All changes saved" with
  no Retry and no banner. Nothing else on the page differs.

The same check, without the screenshots, is the "a refused box put back
to its saved value reads saved again" case in
`ui/e2e/settings-panel-walkthrough.spec.js`. It also asserts that no
PUT is sent.
