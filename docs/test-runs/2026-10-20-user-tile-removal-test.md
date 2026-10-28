# 2026-10-20: settings-panel SET-3 test pass (user tile removal, storage migration)

The test task for stage (c) of `docs/design/settings-panel.md` (SET-3,
Design-Rev e4761b54). Stage (c) itself was built by task 4884 (90ef49a44,
ffb1ac1b5), whose run is in `2026-10-20-user-tile-removal.md`. This run
checks the stage's tests against the section's test bullets, fills the one
gap it found, and runs the walkthrough again on `main` at 7ea12f4e9.

## How it was run

- `cd ui && npm ci && npm run build`, then
  `USER_TILE_SCREENSHOTS=/tmp/shots npx playwright test user-tile-removal-walkthrough --project=chromium`.
  Each worker runs its own real `grain demo` (`ui/e2e/fixtures.js`),
  signed in with its access code.
- No stubs and no mocked grain APIs. The board comes from
  `POST /api/boards`, the Director is the session the demo seeds, and the
  task and comment go through the UI's New task form and Reply box.
- `npx vitest run src/storageMigration.test.js src/PreferencesContext.test.jsx`.

## Walkthrough: the section's bullets

| Bullet | Where in `user-tile-removal-walkthrough.spec.js` |
|---|---|
| seed `grain.username = "alice"`, `grain.pinnedNav.alice.default` with a pinned board, `grain.pinnedNav.bob.default` with a session | first test: the `page.evaluate` before the reload (it also clears `grain.storageVersion`, so the migration runs again) |
| load, and both are pinned in that order | the board's and the Director's rail buttons are visible, the board above; `grain.pinned` is `[board, session director]` |
| every old key gone, `grain.storageVersion` is `"1"` | no key matches `grain.pinnedNav*`, `grain.username` or `grain.userTimeZone`; the version is `"1"` |
| a task and a comment carry the deployment's actor | second test: no POST sends `creator`, `author` or `X-Grain-User`, and `GET /api/tasks/<id>` shows `creator` and the comment's `author` as `/api/config`'s `actor` |

Result: **2 passed** (Chromium). The screenshots it took match the ones
already in `2026-10-20-user-tile-removal/`, so they are not added again.

## Unit tests: the section's bullets

`storageMigration.test.js`:

| Bullet | Test |
|---|---|
| each candidate order of §6.3 step 1 | `pinnedCandidates` "orders the old lists as §6.3 step 1 does", "with no remembered name…", and **new**: `migrateStorage` "puts … list first when it is the best one stored", one case per tier |
| merge of two users' lists, no duplicates, order kept | "merges two names' lists…", `mergePinnedLists` "appends only what is not already there…" |
| the time-zone move | "keeps the remembered name's pins and preferences", "does not overwrite a time zone already under the new key" |
| removing every old key | "removes every old key and records the version" |
| idempotence | "is idempotent" |
| a throwing `localStorage` | "treats unavailable storage as no error", "leaves the version unwritten when a step throws…" |
| an already-migrated store left alone | "leaves an already-migrated store alone" |

The gap: the order was only tested through `pinnedCandidates`'s key
list, so nothing checked that `migrateStorage` itself puts each tier's
list first. The new table-driven test seeds one tier and every tier below
it, and checks that the tier's items lead `grain.pinned` in order and that
only `grain.pinned` and `grain.storageVersion` are left. When the pre-team
`grain.pinnedNav` key's rank was moved below "another team's", five of
its cases failed.

`PreferencesContext.test.jsx` exists: 4 tests (the deployment-wide time
zone key, update and clear, the fallback outside a provider, and no
username).

Result: **28 passed** (24 and 4).

## Route authorization

No change, as the section says. The server already ignores `X-Grain-User`
and the `creator`/`author` bodies.
