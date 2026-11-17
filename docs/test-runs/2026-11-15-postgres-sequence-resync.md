# Walkthrough: PostgreSQL id sequence resync after imports and on startup (2026-11-15)

Plan: [`docs/test-plans/2026-11-15.md`](../test-plans/2026-11-15.md) §3, for PR #1502 ([Task #5592](/tasks/5592), commits `f953f5c5c`, `a9d5912ab`). Filed by [Task #5885](/tasks/5885), walked by task 5915.

**Result: the sequence resync works on every path the plan names. No SQLSTATE 23505 on `HEAD`, and ids always continued above the imported maximum.** It found no bug in the resync. `v3.8.0` reproduces the original bug (23505 after its own rebuild), and `HEAD`'s first start repairs that store. One unrelated `grain/config` import bug turned up along the way (see [Finding](#finding-a-grainconfig-template-without-target-is-refused-with-sqlstate-23502)); it is filed as a follow-up and not fixed here.

## Environment

| | |
|---|---|
| `HEAD` | `c9c78ed5c` (`main`, 2026-11-15 11:06 UTC), `go build -o /home/debian/bin/grain ./cmd/grain` (schema 147) |
| `v3.8.0` | tag `v3.8.0` → commit `2c9fff5d0`, built from a `git worktree` as `/home/debian/bin/grain-v380` (schema 146) |
| PostgreSQL | 15.20 (Debian 15.20-0+deb12u1), `apt-get install postgresql postgresql-client`, superuser role `grain` on `127.0.0.1:5432` |
| SQLite | the embedded store (`modernc.org/sqlite`), for step 2's source store |
| Go | go1.26.10 linux/amd64 |
| Daemon | a real `grain daemon -sandbox-port-base=0 -host-sandboxes -max-workers 1 -max-mergers 0 -as alice@example.com -postgres-url …`, with no repository and no agent, so nothing dispatches. Writes went through its HTTP API with the local access code. |

The UI was not built or used (`make frontend` was skipped). Every step is API and database only, so the embedded placeholder UI was enough.

## Helpers

Everything is under [`2026-11-15-postgres-sequence-resync/`](2026-11-15-postgres-sequence-resync/). `run_all.sh` runs steps 1–6 in order and writes each transcript beside it (`stepN.out.txt`). The transcripts committed here come from that one clean run.

- `env.sh` holds the shared helpers:
  - `start_daemon` / `stop_daemon` start and stop a real daemon on PostgreSQL, or on SQLite with `STORE=sqlite`.
  - `writes LABEL` files a task, comments on it, saves a setting (`config_audit`), creates a template and creates a suite holding it (`suite_item`). It fails on any non-2xx response, `SQLSTATE 23505` or `duplicate key`.
  - `seq_report` / `seq_summary` print `last_value`, `is_called` and `MAX(col)` for every serial column in a schema, flagging `BEHIND` when the next `nextval()` would collide.
  - `nextval_probe` draws one value from every one of the 37 serial sequences and checks each lands above `MAX(col)`.
  - `break_sequences` rewinds every populated table's sequence to 1, the state a pre-resync rebuild leaves.
- `step1-v380-to-head.sh` through `step6-custom-schema.sh` cover the plan's items 1–6.
- `repro-template-without-target.sh` reproduces the unrelated finding.
- `go-test-summary.txt` covers step 7.

Each `seq_summary` table below lists only the populated (or behind) serial columns. Of the 37 serial columns in the schema, the rest are on empty tables.

## Steps and outcomes

### 1. `v3.8.0` → `HEAD` on PostgreSQL, including a schema-change rebuild: **pass** (`step1.out.txt`)

`v3.8.0` filed 5 tasks with comments, 5 settings saves, 5 templates and 5 suites, closed one task (a `task_event` row) and approved another. That left `config_audit` at 42, `task_comment` at 6, `suite_item` at 5 and `task_sequence` at 6, every sequence `ok`.

- **(a) The real upgrade path.** `HEAD`'s `grain state prepare-upgrade` printed `prepared 146 147 …`, the note setup.sh writes. On start, `HEAD` migrated in place (146 → 147: migration 147 is an in-place `Up`) and then brought the state repository across.
  - This is the real `v3.8.0` → `HEAD` upgrade, but it is **not** a store rebuild: `v3.8.0` → `HEAD` has an in-place migration, so no rows are re-imported with explicit ids.
  - Every sequence stayed `ok`. Two rounds of writes got task ids 7 and 8 and `suite-6`/`suite-7`, with no 23505.
- **(b) A forced schema-change rebuild through `staterepo.ImportMigrated`.**
  - To force one, the store was stamped one schema ahead (`grain_schema.version = 148`), which is what a rollback from a newer build looks like. Then `HEAD`'s own `prepare-upgrade` wrote `prepared 148 147`.
  - On start, `HEAD` moved the store to `public_schema148_…`, logged `imported this deployment's rows … into a fresh store at schema 147`, and rebuilt it with explicit ids.
  - Straight after the import, every sequence equalled `MAX(id)` (`config_audit` 58/58, `task_comment` 8/8, `suite_item` 7/7, `template_sequence` 7/7). No startup line was logged because the import had already resynced them.
  - Writes continued at task 9/10, `suite-8`/`suite-9` and `config_audit` up to 74, with no 23505.

### 2. SQLite → PostgreSQL (`UpgradeFromSQLite`): **pass** (`step2.out.txt`)

`HEAD` on SQLite filed 6 rounds of writes and closed one task, reaching `config_audit` 49, `task_comment` 7 and `suite_item` 6. Then `grain db upgrade-postgres -data-dir … -dsn …` ran: `145 tables migrated`, with every PostgreSQL sequence equal to its imported `MAX(id)`. The daemon on PostgreSQL logged no resync (nothing was behind) and two write rounds continued at task 8/9 and `suite-7`/`suite-8`.

Inside a grain sandbox, `grain db …` is the in-sandbox `grain db query` command (`pkg/sandboxcli`). The script runs the host command with `GRAIN_SANDBOX_CLI=0`. That behaviour is by design, not a bug.

### 3. State-repo restore and a settings merge, then writes to every serial table: **pass** (`step3.out.txt`)

- **Restore.**
  - Deployment A was populated (4 templates, 4 suites, 1 board, 4 tasks).
  - A rebuilt host was then simulated: a fresh data directory holding only a `git clone` of A's `grain/config`, over an empty database.
  - `HEAD` logged `loading … as the deployment's state repository; importing its settings` (`staterepo.Load` → `ImportTables`).
  - The imported settings tables came in with their sequences at the imported maximum (`template_sequence` 4/4, `suite_sequence` 4/4, `suite_item` 4/4, `board_sequence` 1/1).
  - Writes continued at `template-5`, `suite-5` and board 2, and `nextval_probe` reported `37 serial tables, 0 would collide`.
- **Settings merge (live, no restart).**
  - A commit to that `grain/config` added `template-50` and `suite-50`, with explicit `template_sequence.number = 50` and `suite_sequence.id = 50`.
  - The running daemon applied it within a tick: it wrote a settings snapshot and merged through `applySettings` → `importInto` with a merge plan.
  - `template_sequence` and `suite_sequence` moved to 50/50. The next suite was `suite-51`, then `suite-52`.
  - `nextval_probe` again reported `0 would collide` across all 37 serial tables.

The first way I tried the restore didn't take this path, and that is not a resync bug. I dropped the database under the same data directory. The store-highwater guard refused it, and when started with its documented `GRAIN_ACCEPT_STORE_BEHIND=1` override, the `task_sequence` seed meant the database was no longer empty. So `Load` took the ordinary "marker equals head" path and imported nothing. A rebuilt host, with a fresh data directory, is the path that restores.

### 4. Startup self-heal of a store a `v3.8.0` rebuild left behind: **pass** (`step4.out.txt`)

The broken store was made by `v3.8.0` itself, with no hand edits:

1. A `HEAD` store (schema 147, 4 rounds of writes) was rolled back with `v3.8.0`'s own `grain state prepare-upgrade` (`prepared 147 146`).
2. `v3.8.0`'s start archived it and rebuilt a fresh store through its `ImportMigrated`, which predates the resync. That left 8 sequences `BEHIND`: `config_audit` 1/33, `integration_event` 1/12, `suite_item` 1/4, `suite_sequence` 1/4, `task_comment` 1/5, `task_event` 1/1, `task_sequence` 1/5, `template_sequence` 1/4.
3. `v3.8.0`'s next writes on that store failed with HTTP 500. Its log shows the original bug: `allocating a task id: … duplicate key value violates unique constraint "task_sequence_pkey" (SQLSTATE 23505)`, along with `config_audit_pkey` and `template_sequence_pkey`.

`HEAD`'s first start (after `prepare-upgrade`, `prepared 146 147`) migrated in place and logged one line per sequence, naming each:

```
grain: PostgreSQL sequence public.config_audit_id_seq for config_audit.id was behind (next=6, max=33); resynced to 33
grain: PostgreSQL sequence public.integration_event_id_seq for integration_event.id was behind (next=1, max=12); resynced to 12
grain: PostgreSQL sequence public.suite_item_id_seq for suite_item.id was behind (next=1, max=4); resynced to 4
grain: PostgreSQL sequence public.suite_sequence_id_seq for suite_sequence.id was behind (next=1, max=4); resynced to 4
grain: PostgreSQL sequence public.task_comment_id_seq for task_comment.id was behind (next=1, max=5); resynced to 5
grain: PostgreSQL sequence public.task_event_id_seq for task_event.id was behind (next=1, max=1); resynced to 1
grain: PostgreSQL sequence public.task_sequence_number_seq for task_sequence.number was behind (next=3, max=5); resynced to 5
grain: PostgreSQL sequence public.template_sequence_number_seq for template_sequence.number was behind (next=2, max=4); resynced to 4
```

(`next=6`, `next=3` and `next=2` are the values `v3.8.0`'s failed writes had already drawn.) Every sequence was then `ok`, and writes succeeded (task 7, `suite-5`).

A second `HEAD` start logged **0** resync lines. Comparing the report taken just before it with the one just after, the only change was `secret_resolution_audit` moving forward 6 → 9, from that start's own audit inserts. No sequence was rewound, and later writes continued (task 8, `suite-6`).

### 5. Two daemons and a lost single-writer lease: **pass** (`step5.out.txt`)

- **Setup.** Daemon A ran on the database and populated it. Two sequences its API writes don't touch (`task_event`, `board_sequence`) were then put `BEHIND` by hand with `setval`, so any resync by a non-holder would show.
- **B waits.** Daemon B (same database, its own data directory and port) logged that the lock was `held by pid=… app="grain-daemon-writer-lock" …; waiting up to 30s for drain`.
  - B waits on the advisory lock before it opens the store (`acquireDaemonWriterLock` comes before `openStoreForSchemaUpgrade` in `pkg/daemon/run.go`), so its startup resync can't run.
  - After 12 s, both hand-broken sequences were still `BEHIND`, and A kept allocating (task 5, `suite-4`).
- **A loses its lease.** A's lock connection was killed with `pg_terminate_backend`.
  - A logged `writer lock session interrupted … attempting to re-acquire`, then `single-writer advisory lock lost: … another grain daemon holds the single-writer advisory lock`, and exited.
  - B logged `acquired PostgreSQL single-writer advisory lock`. Only then did B resync, naming exactly the two hand-broken sequences (`board_sequence` next=1, max=2; `task_event` next=1, max=1), and its writes succeeded (task 6, `suite-5`).

A stale pool that lost its lease mid-resync (the `setval` fence of `a9d5912ab`) can't be reached from outside a process. `TestSyncSequencesRespectsWriteFence` covers it and passed (step 7).

The startup resync in `openStore` runs before `postgres.FenceWrites` is installed (`run.go` `openStoreForSchemaUpgrade` → `openStore` → `syncBehindPostgresSequences`, then `FenceWrites`). In the daemon it is guarded by the advisory lock alone, which this step shows is enough. CLI commands that open the store (`grain connections import`, and so on) run the same resync without either. That only ever moves a behind sequence forward to `MAX(col)`, which is the repair itself, so I'm not counting it as a bug.

### 6. A non-default schema via `search_path`: **pass** (`step6.out.txt`)

One database held two stores with every sequence put `BEHIND` (`break_sequences`): `public` (3 write rounds) and `walk` (5 write rounds, DSN `…&search_path=walk`). A daemon on `search_path=walk`:

- acquired the lock with `schema=walk` and its own key;
- logged 8 resync lines, all `walk.*_seq`, after which every `walk` sequence was `ok` and writes succeeded;
- left `public` byte-for-byte unchanged (every `public` sequence still `BEHIND`; `diff` of the before and after reports was empty).

### 7. Postgres-backed package tests: **ran, all pass** (`go-test-summary.txt`)

`GRAIN_REQUIRE_POSTGRES=1 go test -count=1 -v ./pkg/daemon ./pkg/model/postgres ./pkg/cli ./pkg/staterepo` with PostgreSQL 15.20 installed:

```
ok  	github.com/ai-on-gke/grain/pkg/daemon	251.182s
ok  	github.com/ai-on-gke/grain/pkg/model/postgres	156.644s
ok  	github.com/ai-on-gke/grain/pkg/cli	94.795s
ok  	github.com/ai-on-gke/grain/pkg/staterepo	38.451s
```

PR #1502's tests ran and passed rather than skipping: `TestPostgresSchemaUpgradeResyncsSequences`, `TestPostgresOpenStoreDetectsAndResyncsBehindSequences`, `TestStateRepoImportResyncsPostgresSequences` and `TestSyncSequencesRespectsWriteFence`. So did `TestPrepareUpgradeCarriesAPostgresStoreAcrossASchemaChange`, `TestUpgradePostgresWithEmbeddedPostgres`, `TestPostgresSequencesAllocation` and `TestTheTaskIDSequenceNeverGoesBackwards`. 28 tests skipped, none of them for want of PostgreSQL: they are live kind/docker/VM, GitHub, packet-interception and soak harnesses, plus three retired state-repo SQL tests. Their reasons are listed in the summary.

## Finding: a `grain/config` template without `target:` is refused with SQLSTATE 23502

This is not part of §3's sequence code; I hit it while building step 3's merge commit.

`pkg/staterepo/decl.go`'s format rule is that a key a settings file leaves out "lands on the schema's default when it is imported". But `template.target_owner`, `template.target_name` and `template.auto_merge` are `NOT NULL` with **no** default in `pkg/model/schema.go`. So a hand-written template that leaves out `target:` (as a repository's `.grain/templates/<name>.yml` may) fails to import with:

```
state sync failed: state repository holds changes grain could not apply (unapplied commits …: … ("add a minimal template")): staterepo: inserting template row 0: ERROR: null value in column "target_owner" of relation "template" violates not-null constraint (SQLSTATE 23502)
```

The daemon then retries the unapplied commit on every sync and API settings write.

- **Deterministic reproduction:** `repro-template-without-target.sh`, a fresh `HEAD` deployment on PostgreSQL plus one commit adding a 4-key template (`id`, `name`, `title`, `body`). Transcript: `repro-template-without-target.out.txt`.
- **Why it is not fixed here:** the fix is a schema default or an import-time default in `pkg/staterepo` (a trust-boundary path). It is outside §3's scope, so it is filed as a follow-up task with this reproduction rather than mixed into this walkthrough.

## Fixes

None needed for §3: no bug found in the sequence resync.
