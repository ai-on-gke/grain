# Settings → Capability servers for gchat: how the UI was checked

Task 1162 (follow-up to 316), 2026-10-26. Covers the gchat server's card: the configured spaces with their usage, a space's **Send test message**, and configuring a webhook space and granting `gchat#post` on a task ([gchat.md](../../gchat.md)).

## What ran

- **Server:** `grain demo` built from the branch, over its throwaway store, with `-secrets-store`.
- **Capability server:** grain's own gchat server (`intree.NewGChat`), served by the demo under `https://grain.dev/capabilities/gchat` and attached by its `servers.Registry`. Its only stub is Google Chat: the demo's `fakeChat` answers every webhook post with the message it created, so nothing left the machine.
- **Browser:** Playwright's Chromium against the built frontend. No grain API, React or browser behaviour was mocked.

## Automated

- `ui/e2e/gchat-settings.spec.js`: configure one webhook space (`team-room`, its URL in the write-only `webhook_urls`) through the schema form; check the saved config holds a `$secret:` reference and not the URL; Test shows `space team-room: webhook`; Send test message shows `Posted spaces/DEMO/messages/demo-…` and the row's usage goes to `1 posted` and `1 / 60 this hour`; the button sits right of the space's name inside the row; grant `https://grain.dev/capabilities/gchat#post` with `space: team-room` picked from the enum on a new task and check the task holds `…#post={"space":"team-room"}`.
- `ui/src/components/CapabilityServersPanel.test.jsx` (vitest): the card lists webhook and app spaces with their usage, sends a test message with `{id, space}`, re-reads the usage, and shows Chat's refusal for a space it could not post to.
- `pkg/ui/capability_servers_test.go`: the routes call `test_post` and `usage` with the right arguments, return a refused post as a result, and refuse a bad id, a missing space, an unattached server and a process with no registry. `tests/security/routes_test.go`: both routes are `trusted`.

## Looked at

`node e2e/screenshot.mjs` (what `make ui-screenshots` runs) at 1440×900 and 390×844, light and dark, of `/settings/capabilities#capability-servers` with the gchat tab selected:

- `before-contact-sheet.png`: the base, gchat unconfigured.
- `after-contact-sheet.png`: two webhook spaces configured (`team-room` with a display name, `release-notes` with `per_hour: 10`), after test posts to `team-room`, with Send test message pressed on it.

The same page was also checked in the browser at both widths and themes: nothing overflows the card or the page (`scrollWidth` equals `clientWidth`). On desktop the button sits right of the space name and is vertically centred on it; on phone it stacks below the name. Heading text follows the theme (rgb(36,34,29) on rgb(247,246,243) light, rgb(233,231,225) on rgb(15,16,19) dark). This sandbox had no Gemini key for `LOOK=1`, so the PNGs themselves were not described by a model.
