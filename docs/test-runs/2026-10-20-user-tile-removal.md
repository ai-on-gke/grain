# 2026-10-20: no user tile, no browser-chosen username (settings-panel stage c)

Stage (c) of `docs/design/settings-panel.md` (§6, and the rest of §4.2
and §4.3): the sidebar's user tile and the browser-chosen username are
gone, and this browser's per-user localStorage keys become
deployment-wide ones. This is the run of
`ui/e2e/user-tile-removal-walkthrough.spec.js` that goes with it, and the
screenshots in `2026-10-20-user-tile-removal/`.

## How it was run

- `grain demo` (the e2e fixture's own server, built by
  `ui/e2e/global-setup.js`, signed in with its access code), against the
  built frontend (`cd ui && npm ci && npm run build`).
- `USER_TILE_SCREENSHOTS=/tmp/shots npx playwright test user-tile-removal-walkthrough`
  in Chromium, at the default 1280×720 viewport and then 390×844 for the
  phone drawer.
- No stubs. Every request reached the demo's real API: the board is made
  with `POST /api/boards`, the Director is the session the demo seeds,
  and the task and the comment are filed through the UI's own New task
  form and Reply box.

## What it covers

1. **Old pins survive, merged.** The browser is set up the way it was
   before the stage:
   - `grain.username = "alice"`;
   - `grain.pinnedNav.alice.default` holding a pinned board;
   - `grain.pinnedNav.bob.default` holding the Director session;
   - `grain.userTimeZone = "Europe/London"`;
   - no `grain.storageVersion`.

   After a reload, `storageMigration.js` has run:
   - the rail pins the board, then the Director;
   - `grain.pinned` holds `[board, session director]` in that order;
   - `grain.timeZone` is `Europe/London`;
   - `grain.storageVersion` is `"1"`;
   - no `grain.pinnedNav*`, `grain.username` or `grain.userTimeZone` key
     is left.
2. **No user tile.** The rail has no `user-tile` and does not show
   "alice". On desktop, the build stamp is the foot of the rail. On a
   phone, the drawer's foot is the System row and then the build stamp.
3. **Attribution is the server's.** A task filed through New task (while
   the browser still remembers `grain.username = "alice"`) and a comment
   posted through Reply send no `creator`, no `author` and no
   `X-Grain-User` header. `GET /api/tasks/<id>` shows the task's `creator`
   and the comment's `author` as `/api/config`'s `actor`.

Result: 2 passed.

The specs this stage changed were run as well, in Chromium (and
`phone.spec.js`'s drawer and footer tests in WebKit):
`processes-walkthrough`, `sidebar-navigation`,
`track6-navigation-merged-dissolve`, `phone`, `host-sandboxes-alert`,
`chat-scrollbar-accessibility`, `grain_ci_grantable_walkthrough`,
`stale-save-two-contexts-walkthrough`, `system-rename-walkthrough`,
`access-code-walkthrough`, `relay-settings-walkthrough` and
`deployment-settings-and-operations-inputs-walkthrough`. All passed.

## Screenshots

| | |
|---|---|
| `sidebar-footer-desktop.png` | the rail on desktop: the migrated pins at the top, the build stamp alone at the foot |
| `sidebar-footer-phone.png` | the phone drawer: System, then the build stamp |
