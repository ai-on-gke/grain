# `grain/config` grants-only repo access export across versions — 2026-11-15

Walkthrough of [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §6
(PR #1511, `28907dd1c`, [Task #5460](/tasks/5460)), run as [Task #5918](/tasks/5918)
from [Task #5885](/tasks/5885). `2026-11-14.md` §2 (the earlier grant-export walkthrough) has
no report on `main` yet, so this one starts from scratch.

## Summary

| Item | Outcome |
|---|---|
| 1. HEAD export imported into older builds | **v3.8.0 and release/v3.5.1 import it intact.** **release/v3.1.1 loses every plain read and write without saying so** when the import runs; the daemon refuses at startup, but its sync tick and `grain state sync` import it. HEAD's own `SyncExternal` had the same hole for a newer-schema export. **Fixed** (`pkg/staterepo/bind.go`), regression test added, compatibility floor documented. |
| 2. Live export → commit → import → dispatch (SQLite, PostgreSQL) | **Pass.** Tables identical across the round trip on both stores. Tasks filed from the imported templates clone their read repos, are refused pushes to read-only repos, and push to their write targets (including a base-less write). The off-host read is refused at filing with a clear message. |
| 3. First HEAD export after a v3.8.0 one | **Pass.** 0 lines added, 29 deleted, and every deleted line is a duplicate `reads:`/`writes:` entry. |
| 4. `/next/` template and schedule pages after the round trip | **Pass.** They show the same reads and writes as the tables. Screenshots at 1440 and 390 px, light and dark. No classic page was needed. |

## Environment

- Commit walked: `origin/main` `c6743dc5d` (HEAD). Older builds: `release/v3.5.1`
  `4e29f7be9` (schema 136), tag `v3.8.0` `2c9fff5d0` (schema 146), and `release/v3.1.1`
  `0e5f0baf3` (schema 87), added because the plan's "what is not proved" names it. HEAD is
  schema 146.
- `go build -o /home/debian/bin/grain-<v> ./cmd/grain` in a worktree per version; `grain-fix`
  is HEAD plus the fix in this change, rebuilt after `make frontend` for the UI item.
- Real `grain daemon -sandbox-port-base=0 -host-sandboxes` through the 2026-11-13 harness
  (`docs/test-runs/2026-11-13-grain-health-exit-codes/env.sh`, `fake-agy.py`). Its `ghsim` is
  replaced here by `ghsim.go`, which also serves `acme/docs` and `acme/gadgets` over
  git-http-backend and lists them in `/user/repos` for the bot token.
- SQLite (modernc, built in) and PostgreSQL 15.20 (Debian 15.20-0+deb12u1). Chromium 1234
  (Playwright cache `/var/cache/playwright`), Node v20.19.0.
- Helpers, all in `docs/test-runs/2026-11-15-config-grants-export/`:
  - `lib.sh`: seeds the data and dumps the four access tables.
  - `run_versions.sh`: items 1 and 3.
  - `import_old.sh`: imports into one older build.
  - `live_newer_schema.sh`: the bug's live reproduction.
  - `run_live.sh` (`STORE=sqlite|postgres`): item 2.
  - `probe.sh`: run inside each task's sandbox.
  - `pane_text.mjs`: reads the `/next/` detail pane's text.
  - `captures/`: everything quoted below.

### Seeded data (`lib.sh` `seed_access`, through the API)

| Object | reads | writes |
|---|---|---|
| `template-1` walk-plain | `acme/docs` | `acme/gadgets@main` |
| `template-2` walk-edge | `acme/docs`, `ghe.example.com/acme/legacy` | `acme/docs@release`, `acme/gadgets` (no base) |
| `template-3` walk-nobase | `acme/docs` | `acme/gadgets` (no base) |
| `sched-1` Walk schedule | `acme/docs`, `ghe.example.com/acme/legacy` | `acme/gadgets@main`, `acme/docs` (no base) |

**A NULL-base write cannot be stored.** `template_write.base` and `schedule_write.base` are
`TEXT NOT NULL DEFAULT ''` (an `UPDATE … SET base=NULL` fails with `NOT NULL constraint failed:
schedule_write.base`). A write with no base is stored as `''`, and both v3.8.0 and HEAD export it
as `…/repo#write={"base":"","repo":"acme/gadgets"}` under `grants:`. The NULL-base branch of
`repoGrantEntry` can't be reached from a stored row. In practice only the off-host repository
stays under `reads:`/`writes:`. The design note now says so.

## Item 3: v3.8.0 → HEAD upgrade diff

`run_versions.sh` seeds the data on a v3.8.0 daemon over SQLite, then stops it; that export
commit is the "before". Then `grain-head state -data-dir … sync` (`committed dfe9552…`, "on
the first export by an upgraded grain") runs, and the two commits are diffed
(`captures/v3.8.0-to-head-upgrade.diff`):

```
0	15	settings/schedules.yml
0	14	settings/templates.yml
```

Every deleted line is a duplicate. The same rows are already in the entry's `grants:` list.

- `reads:` ×7 and `writes:` ×4 headers.
- `- acme/docs` ×5, `- acme/gadgets` ×2, `- repo: acme/docs` ×1, `- repo: acme/gadgets` ×2.
- `base: main` ×2 and `base: release` ×1.
- `- grain/config` ×5. These are the system schedules' reads: the five
  `repo#read={"repo":"grain/config"}` grants are still there.

`ghe.example.com/acme/legacy` stays under `reads:`. Nothing else churns: no reordering, no
timestamps, no other files.

## Item 1: HEAD's export imported into each older build

`run_versions.sh` clones the HEAD export (`captures/head-export-templates.yml`,
`head-export-sched-1.yml`) to a bare repo. `import_old.sh <v>` then, on a fresh SQLite
data dir:

1. Runs `grain-<v> state repo -branch main file://…/config.git` (read-only IaC mode).
2. Starts the daemon once to create the store.
3. Runs `grain-<v> state sync`.
4. Dumps the tables.

Results are in `captures/import-head-into-<v>.txt`.

| Build | Daemon start on the repo | `grain state sync` | `template_read` / `template_write` / `schedule_read` / `schedule_write` after (excluding system schedules) |
|---|---|---|---|
| v3.8.0 (146) | loads it | `applied`, exit 0 | 4 / 4 / 2 / 2: **identical to the source** |
| release/v3.5.1 (136) | **refuses and exits**: "state repository was written by a newer build of grain: repository is at schema 146, this build knows 136" | `applied`, exit 0 | 4 / 4 / 2 / 2: **identical** (it already projects `grants:` repo entries into the tables) |
| release/v3.1.1 (87) | **refuses and exits**: "… schema 146, this build knows 87" | `applied`, exit 0 | **1 / 0 / 1 / 0**: only the two off-host reads survive |

On v3.1.1 the plain reads and writes become `template_grant`/`schedule_grant` rows
(`captures/import-head-into-v3.1.1-grant-rows.txt`), for example
`https://grain.dev/capabilities/repo#read={"repo":"acme/docs"}`. That build's `repo` capability
server knows only `#write {force, refs}` (push-any-ref) and has nothing that turns these
entries into read or write rows. So a task filed from those templates gets no extra
repository access, and nothing says why.

### What decides the compatibility floor

- Reading `grants:` repo entries back into the read and write tables (`projectedRepoRow`,
  `6f0f15cde`) is in every tag from **v3.5.0**. Earlier builds (v3.1.1 to v3.4.0) have the
  `grants:` key but read these entries as opaque capabilities.
  The PR #1511 commit message names v3.8.0 as the floor, which is too high: release/v3.5.1
  reads a grants-only export fine.
- PR #1511 is what makes v3.1.1 lose the rows. v3.1.1 still reads `reads:`/`writes:`, so a
  v3.8.0 export, which writes each plain row there as well as under `grants:`, keeps them.
  Only a grants-only export leaves v3.1.1 nothing it understands.
- Every older build already refuses a newer-schema repository **at startup**, so an older
  deployment can't *start* on a HEAD export. The exposure is a deployment already running in
  IaC mode that takes a newer push on a later sync, or `grain state sync`.

### Bug: an external sync imported a newer build's dump without checking its schema

Deterministic reproduction (`live_newer_schema.sh`, `captures/newer-schema-live.txt`):

1. Start a HEAD daemon in read-only IaC mode on the schema-146 export above.
2. Push a commit that stamps `schema-version` 147 and drops template-1's `acme/docs` grant,
   the way a newer build's export would differ.
3. Call `POST /api/state-repo/sync`.

| Build | Sync answer | template-1's `acme/docs` read | `GET /api/state-repo` `error` |
|---|---|---|---|
| HEAD `c6743dc5d` | 200 | **gone** | `null` |
| HEAD + fix | 502 `state repository holds changes grain could not apply: state repository was written by a newer build of grain: repository is at schema 147, this build knows 146` | kept | the same message |

Cause: the daemon's IaC tick (`StateManager.cycle`), the State pane's Sync, a remote change
and `grain state sync` all call `staterepo.SyncExternal`. That function pulls and runs
`ImportTables` without ever reading the stamp. `Apply` (internal mode) and the startup load
both refuse `found > version`. The same code is in v3.8.0, release/v3.5.1 and release/v3.1.1,
which is how the v3.1.1 loss above happens on a live deployment.

Fix (`pkg/staterepo/bind.go`): after pulling, `SyncExternal` reads `schema-version` and
refuses a stamp newer than `model.SchemaVersion`. The error is `ErrSchemaTooNew` wrapped in
`ErrNotApplied`, worded the same as `Apply`'s. Nothing is imported, and the loaded-head marker
is left alone, so every tick reports the refusal again. Older stamps still import, since
`ImportTables` runs their data migrations. Regression test:
`TestSyncExternalRefusesANewerBuildsDump` (`pkg/staterepo/syncexternal_schema_test.go`) syncs
this build's export, then a `SchemaVersion+1` export without the read, with and without
`force`. It fails on `main` ("= <nil>, want ErrSchemaTooNew wrapped in ErrNotApplied") and
passes with the fix. `go test ./pkg/staterepo/... ./pkg/cli/...` passes. In `./pkg/daemon/...`
only `TestHostdSelfAssertedLabelsDoNotSelectIt` failed, on a `TempDir RemoveAll … directory
not empty` cleanup race; it passes with `-count=3` on its own and doesn't touch `staterepo`.

**This fixes the problem going forward only.** Builds already released keep the hole until
they are upgraded. Two policy questions for whoever owns the release branches, neither done
here:

- **Backport.** Backporting this fix to `release/v3.1.1` would turn that branch's silent loss
  into a loud refusal. The plan's branch rule is to backport only when an operator asks.
  release/v3.5.1 doesn't need it for this export, because it reads the grants correctly.
- **Duplicate spelling.** Should the duplicate `reads:`/`writes:` spelling come back, behind a
  setting or a version marker, for deployments that share one `grain/config` with pre-v3.5.0
  readers? I didn't add one. Builds before v3.5.0 already refuse this export at startup for
  its schema, and a reader that skips the schema check is now refused on `main`. A setting
  would only help an old running reader that still lacks the check, and the fix for that
  reader is to upgrade it or backport the check.

What changed in the docs:

- `docs/external-config-repo.md` gains "Sharing one repository across grain versions". It
  explains the schema stamp, the old sync hole and the v3.5.0 grants floor, and says to run
  every deployment that reads one config repository on the same release, upgrading readers
  before the writer.
- `design/design/unified-grants.design.md` corrects the floor (v3.5.0, not v3.8.0), records
  that a base-less write is `''` (not NULL) and goes under `grants:`, and records the new
  refusal.

## Item 2: live export → commit → import → dispatch

`STORE=sqlite|postgres run_live.sh`, with `GRAIN=grain-fix` (the frontend-embedded build):

1. **Daemon A**, fresh store, internal `grain/config`: `seed_access` through the API. Each
   write is exported and committed by the daemon itself ("Update grain state after a write by
   the access code"). A is stopped, and its `data/state-repo` is cloned to a bare
   `config.git`.
2. **Daemon B**, fresh store (a new SQLite file, or a dropped and recreated `grain_walk`
   PostgreSQL database): `grain state repo -branch main file://…/config.git`, then start. B
   loads the repository in IaC mode (`{"mode":"external","error":null}`).
3. The four tables on A and B, excluding the system-schedule rows each daemon owns, are
   **identical on both stores** (`captures/live-*/A-B.diff`: "A and B tables identical").
4. One task is filed on B from each template (`POST /api/tasks {"templateId", "approved": true}`).
   The fake agent runs `probe.sh`, which clones `acme/docs` and `acme/gadgets` through the
   task's own git-proxy URL and pushes the task branch to each.

| Template | Filing | `acme/docs` | `acme/gadgets` |
|---|---|---|---|
| template-1 walk-plain | queued, reads `[acme/docs]`, writes `[acme/gadgets@main]` | clone ok. Push **refused**: "acme/docs is read-only for this sandbox: it may fetch it but not push to it" (403) | clone ok. Push **ok**: `grain/…/task-2-walk-plain-access` landed on the bare remote |
| template-2 walk-edge | **refused**: "the deployment has no git connection on ghe.example.com, so it cannot reach ghe.example.com/acme/legacy -- add one in Settings → Git (/settings/git), then file this again." | – | – |
| template-3 walk-nobase (base-less write) | queued, writes `[acme/gadgets]` | clone ok, push refused (read-only) | clone ok, push **ok** (`…/task-3-walk-base-less-write`) |

The off-host read survives the round trip as a row. It is enforced at filing, which refuses
loudly because this sandbox has no `ghe.example.com` connection; nothing was dropped. SQLite
and PostgreSQL gave the same results line for line (`captures/live-sqlite/`,
`captures/live-postgres/`).

A first run showed `acme/gadgets` refusing the push upstream ("GitHub accepted the "bot"
credential and refused it write access (403)"). That came from the harness: the bare repo
lacked `http.receivepack`. The git proxy had already allowed the push, and its message named
the cause correctly.

## Item 4: `/next/` pages after the round trip

On daemon B (SQLite, after the import):

- Screenshots of `/next/processes/templates` with "Walk edge access" selected and
  `/next/processes/schedules` with "Walk schedule" selected (`ui/e2e/screenshot.mjs --url
  --click`, desktop 1440 px and phone 390 px, light and dark).
- Contact sheets: `captures/screenshots/templates-contact-sheet.png`,
  `schedules-contact-sheet.png`.

My file reader shows the PNGs as bytes, and `LOOK=1` has no key here. So
`pane_text.mjs` reads the selected detail pane's rendered text (`captures/next-pane-text.txt`):

| Page → row | reads | writes |
|---|---|---|
| templates → Walk plain access | `acme/docs` | `acme/gadgets (main)` |
| templates → Walk edge access | `acme/docs, ghe.example.com/acme/legacy` | `acme/docs (release), acme/gadgets` |
| templates → Walk base-less write | `acme/docs` | `acme/gadgets` |
| schedules → Walk schedule | `acme/docs, ghe.example.com/acme/legacy` | `acme/docs, acme/gadgets (main)` |

These match the seeded data and the tables on A and B. `/next/` shows repository access for
both kinds, so no classic page was walked. No UI code changed.

## Harness notes

- `env.sh` runs with `set -e`. A failing `grep` or `curl -f` ends a script before its
  `stop_daemon`, leaving a daemon on 4280 for the next run. The scripts here stop their
  daemons in an `EXIT` trap.
- After `go test ./pkg/daemon/...`, a temporary PostgreSQL from the test helpers
  (`/tmp/grain-postgres-test-*`, `-h 127.0.0.1 -p 5432`) was still running and holding
  `127.0.0.1:5432`. The system cluster then bound only `::1`, and the harness's TCP DSN reached
  the leftover server ("role "grain" does not exist"). Kill it before starting PostgreSQL for
  a walkthrough.
- `ui/` needs `npm ci` before `make ui-screenshots` or `node e2e/screenshot.mjs` in a fresh
  sandbox.
