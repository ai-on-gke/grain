# Settings → Capability servers and Capability policy: how the UI was checked

Task 5112 (part B of 5049), 2026-10-23. Covers editing a capability server in place, its configuration form with write-only secret fields, Test, run location, usage warnings on remove and rename, and the Capability policy card ([capability-servers-ui.md](../../capability-servers-ui.md)).

## What ran

- **Server:** `grain demo -secrets-store` built from the branch, over its throwaway store, so typed secrets reach a real encrypted secrets store.
- **Capability server:** the demo's own `https://capabilities.demo.grain.example/cloud` (`pkg/demo/demo_capability_server.go`), a real `protocol.CapabilityServer` over Streamable HTTP attached by a real `servers.Registry`. It now declares `configure` with a pattern-checked `project`, an enum `region` and a `writeOnly` `apiToken`. It is the only stand-in, for an external capability server. Nothing mints in the demo.
- **Browser:** Playwright's Chromium against the built frontend. No grain API, React or browser behaviour was mocked.

## Automated

- `ui/e2e/capability-server-settings.spec.js`: add a second entry for the demo server after testing the unsaved values; configure it, with the server's refusal of `project` shown on the field, then a secret that reaches the store and never the stored config; test the saved entry; edit its headers in place; grant `cloud-viewer` to a task; a deny policy whose preview lists that task, saved with "Save anyway"; remove with the warning listing the task. A second test checks that run location refuses host without grain hostd and says why.
- `ui/src/components/CapabilityServersPanel.test.jsx` and `CapabilityPolicyEditor.test.jsx` (vitest): edit, rename after the usage warning, the config form's secret field and inline errors, Test's verbatim error, the policy preview.
- `pkg/ui/capability_policy_test.go`: usage (an open task, a template and a declared workflow job; a closed task is not usage), the preview, policy validation and save, rename through `renameFrom`. `pkg/model/capability_ref_test.go`: deny rules.

## Looked at

`make ui-screenshots` at 1440×900 and 390×844, light and dark, of `/settings/credentials#capability-servers` and `#capability-policy` (`pages-contact-sheet.png`), and the same widths and themes with the forms open (`forms-contact-sheet.png`): the demo server's card with Configure open (a field error under `project`, the `apiToken` secret chip, a Test result with method tables) and Edit open; a policy rule; the removal dialog.

Found and fixed while looking: a refusal keyed by a field was shown twice, inline and as a toast over the form; it is now inline only. Left as they were: the method argument table scrolls sideways at phone width (CapabilityMethodTable, #5048), and the first button of a card the URL anchors to shows its focus ring.
