# Settings imports are a three-way merge

Incident: grain-eu-host (Postgres store), 2026-10-06/07.

1. Settings sync failed for about 36 hours on a SQLite-only query, and nobody was alerted.
2. A restart then imported the stale grain/config export wholesale over the database.
3. That rewound 70 rows across 7 settings files, and the database had no history to recover them from.

## What an import does now

`staterepo.applySettings` (pkg/staterepo/resets.go, merge.go):

- **Base.** It merges against the commit this host last agreed with the repository at. That is the `loadedHead` marker: the last export it wrote, or the last commit it imported. After a divergence recovery it uses the merge base of that commit and HEAD instead.
- **Rows the arriving commit did not touch** keep the database's version. This includes rows the database added or deleted since the last export.
- **Rows the arriving commit edited** are merged column by column. Columns it changed take the arriving value, and every other column keeps the database's value.
- **Rows the arriving commit added or deleted** take the arriving version.
- **Conflicts.** Where both sides changed the same column, the arriving value wins, and the journal names it.
- **Refusals.** If grain cannot work out what an import would do, it refuses the import (`ErrNotApplied`) and keeps the database. That covers an unreadable base commit (`ErrNoMergeBase`) and a failure while reading what the import would replace or reset. It no longer logs "applying them anyway".

The marker commit already records which export the database last matched, so no separate settings revision counter is stored. Comparing the database with that commit's dump is how grain detects changes that were never exported.

## When the sync fails

After 5 minutes of failed cycles, `StateManager` reports `ui.StateRepoSyncFailure` in three places:

- a standing banner in the UI header, for everyone;
- an inbox item for deployment admins;
- the sandbox-health response, which is what `read_deployment_health` returns.

Readiness is unchanged, so the UI stays up to show the problem.

## Already on main before this change

- `908bc0a66` fixed the `"id" IS $2` withheld-columns query, which caused the "keeping grain_config's withheld columns" error. It is on main but was not on candidate when this change was written.
- The `''` reset query ("reading what applying grain_config would overwrite") was still broken on main, and is fixed here.
- Nothing on main covered the stale-export rollback, the refusal, the loud sync failure, or backups.

## Backups

`pkg/backup` is opt-in (`BackupInterval` 0 means off), and it snapshots with SQLite's `VACUUM INTO`. So a Postgres deployment gets no automatic snapshot. Pre-import snapshots of the settings tables are filed as a follow-up task.
