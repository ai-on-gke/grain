# Settings → Capability servers for host scripts: how the UI was checked

Task 5143 (follow-up to 5087), 2026-10-23. Covers the host-script server's configuration form (one card per script, multi-line `usage` and `script`, `env` as name/value rows), the script list with usage on the server's card, a script's Test with an argv, and granting a script on a task ([host-scripts.md](../../host-scripts.md#3-try-it)).

## What ran

- **Server:** `grain demo` built from the branch, over its throwaway store.
- **Capability server:** a real `grain capability host-script` (the same binary), added as a stdio entry and spawned and attached by the demo's own `servers.Registry`, with `-listen 127.0.0.1:0` and a temporary `-data-dir`. Its scripts ran under bash in the sandbox. Nothing was stubbed.
- **Browser:** Playwright's Chromium against the built frontend. No grain API, React or browser behaviour was mocked.

## Automated

- `ui/e2e/host-script-settings.spec.js`: add the server; define `echo-args` in its configuration form (a form, not the JSON editor; `usage` and `script` are text areas; an `env` row whose placeholder is `$env:DEPLOY_TOKEN`); check the saved config; check the card lists the script with its usage and limits; Test it with the arguments `two words` and `$(echo injected)` and check stdout is `<two words>` / `<$(echo injected)>` (each passed literally), stderr carries the env value, exit 3 and a duration; at 390 px check the row, argument field and output stay inside the card; grant it on a new task through the `with: {script}` select and check the task holds `…#host-script={"script":"echo-args"}`.
- `ui/src/components/CapabilityArgsForm.test.jsx` (vitest): host-script's schema renders without falling back; a string map's values are checked live; text areas and name/value rows edit the value.
- `pkg/capability/intree/hostscript_test.go`: `test_script` runs a script once with its argv exactly, leaves no grant behind, and audits it with `caller: settings-test` and an attempt_id `settings-test-<hex>` that reads as no task; it refuses an unknown script, too many arguments and a NUL byte. `pkg/ui/capability_servers_test.go`: the route calls `test_script` and refuses what it should. `tests/security/routes_test.go`: the route is `trusted`.
- By hand against the demo: `POST /api/capability-servers/test-script` answered `{"stdout":"<two words>\n<$(id)>\n","stderr":"done\n","exit_code":3,"duration_ms":1}`, and `audit.jsonl` held `"caller":"settings-test"`.

## Looked at

`node e2e/screenshot.mjs` (what `make ui-screenshots` runs) against that demo, at 1440×900 and 390×844, light and dark, of `/settings/credentials#capability-servers` with two scripts configured (`pages-contact-sheet.png`). The same widths and themes with a script's Test run (`test-contact-sheet.png`) and with Configure open (`config-contact-sheet.png`).

Gemini could not describe the shots: the run's `~/.gemini-api-key` was refused with `API_KEY_INVALID`. So the layout was measured instead. At both widths nothing in the host-script card runs past its edges, the page does not scroll sideways, and a 70-character argument wraps in its output. The Test button sits at the row's right at desktop width and drops under the description at phone width. The script field is monospace. A person should still look at the sheets.

Left as it was: the method argument table scrolls sideways at phone width (CapabilityMethodTable, as in the 2026-10-23 capability-server-settings report). Switching between desktop and phone width mounts a different Settings layout, so an open Test panel closes on resize.
