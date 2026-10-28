# Granting capability server methods with arguments: how the UI was checked

Task 5048 (PR #4455), 2026-10-23. Covers the schema-driven grant form, grant chips, "Add another", the task capabilities API with `with:` arguments, and the Settings method table (docs/capabilities.md, "Granting from the UI").

## What ran

- **Server:** `grain demo` built from the branch (`go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`), over its throwaway store.
- **Capability server:** the demo's own `https://capabilities.demo.grain.example/cloud` (`pkg/demo/demo_capability_server.go`). It is a real `protocol.CapabilityServer` on a loopback port, attached by a real `servers.Registry` over Streamable HTTP. It stands in for an external capability server and is the only stand-in. Its `cloud-key` method has a required enum, a required pattern-checked string, a bounded integer with a default, a boolean, an array of unique strings, and a nested object through `$defs`. Its `cloud-viewer` method takes no arguments. Nothing mints in the demo.
- **Browser:** Playwright's Chromium against the built frontend. No grain API, React or browser behaviour was mocked.

## Automated

- `ui/e2e/capability-grant-arguments.spec.js` covers:
  - granting `cloud-key` on a new task, with the pattern refused as typed and a repeated array item flagged;
  - a second grant with "Add another", and that both reach the task normalized;
  - editing the grant on a queued task, with a bound refused as typed;
  - the server's field error for an out-of-enum value;
  - the Settings method table;
  - a bounding-box check that the method menu sits above the New task overlay at 390 px.
- `ui/src/components/CapabilityArgsForm.test.jsx` (vitest) runs the form generator against a fixture using every supported keyword, plus the JSON fallback.
- `pkg/ui/capability_grants_test.go` covers the API: method listing, attach/detach with arguments, field errors, multiple instances, dedup of equal grants, replace in place, detaching a grant no server offers, the check endpoint's `changed`/`unchecked` flags, and field errors from task creation.

## Looked at

These were shot at 1440×900 and 390×844, in light and dark:

- New task: the picker with the method menu open, and the grant form;
- the task pane's grant chips, and the edit form showing an inline pattern error;
- Settings → Credentials → Capability servers, with the method table.

The first pass showed two problems, both fixed before these sheets were taken:

- The nested-object and array groups' `fieldset` legends broke their borders. They are now plain bordered groups.
- On a phone, the Settings argument table crushed its last column. It now keeps a minimum width and scrolls sideways inside its box.

`contact-sheet.jpg` holds the dialogs and pane states. `pages-contact-sheet.png` is `ui/e2e/screenshot.mjs`'s own sheet of `/tasks/<id>` and `/settings/credentials`.
