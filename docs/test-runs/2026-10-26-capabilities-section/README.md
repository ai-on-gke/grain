# Settings → Capabilities and Legacy capabilities: how the UI was checked

Task 885, 2026-10-26. Covers the new Capabilities section (`/settings/capabilities`): the Capability servers card with one tab per server and the Capability policy card. Also covers the Credentials section, renamed Legacy capabilities (`/settings/credentials`), which keeps the legacy capability readiness panel, the deployment-wide GitHub and GCP fields, and secrets.

## What ran

- **Server:** `grain demo` built from the branch, over its throwaway store (`make ui-screenshots`, and the e2e suite's per-worker demo server).
- **Browser:** Playwright's Chromium against the built frontend. Nothing was mocked.

## Automated

- `ui/e2e/capability-server-settings.spec.js` drives the server tabs end to end. It adds a server from the `+ Add server` tab, which selects the new server's tab. It configures, tests, edits, grants and deny-policies that server, then removes it, and the strip falls back to its first tab. Each built-in tab carries a status dot and a built-in badge, and selecting it shows only that server's card. At 1440 wide, the gcp badge sits in the card's right-hand chip row. At 390 wide, neither the card nor the tab strip overflows the viewport. Main's config-map editor test runs inside its tab.
- `ui/e2e/settings-panel-walkthrough.spec.js` edits GCP project on the section now labelled Legacy capabilities, and it survives a reload.
- `ui/src/components/CapabilityServersPanel.test.jsx` (vitest) covers switching tabs, built-in and custom tabs, adding and removing. `settingsIndex.test.jsx` and `paths.test.js` cover the new section, its search entries and the aliases: `/settings/capabilities` and `/settings/capability-servers` open Capabilities, and `/settings/legacy-capabilities` opens Legacy capabilities.

## Looked at

`make ui-screenshots PAGES=/settings/capabilities,/settings/credentials` was shot at 1440×900 and 390×844, light and dark (`pages-contact-sheet.png`). The images themselves were not inspected by eye in the sandbox: `read_file` returns a PNG's bytes, and no Gemini key was available for `LOOK=1`. Layout was checked with the bounding-box assertions listed above instead. A reviewer should look at the contact sheet.
