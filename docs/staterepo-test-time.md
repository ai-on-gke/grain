# Where pkg/staterepo's test time goes

**Status: measured, nothing changed** (grain/task-348). This is a
measurement with a date on it: 8 September 2026, at 110fcb48, in a grain
sandbox (3 vCPU, 3GB, go1.26.6, git 2.39.5). It exists because
`pkg/staterepo` costs about three minutes of every CI run and the last
attempt to speed it up could not tell whether it had.

The short answer: **it is not git, and it is not fsync.** On the build CI
actually runs -- `go test -race` -- git is 12% of the package's wall
clock. The rest is Go on one core, and most of that is
`modernc.org/sqlite` parsing SQL text, with the race detector's pointer
checks on top of it. The package is CPU-bound, single-threaded, and
almost exactly as expensive on every run.

## What was asked

grain/task-295 set `synchronous=NORMAL` on the store and measured the two
store-backed packages three times each. `pkg/model/sqlite` moved from
126-132s to 54-65s, tightly and repeatably, because its time really is an
fsync per commit. `pkg/staterepo` gave 174s, 192s and 220s against 107s,
111s, 164s and 178s -- ranges that overlap, and a spread wider than the
effect. So: what is `pkg/staterepo` actually spending, and why can nobody
measure a change to it?

## How this was measured

Three instruments, all on the package alone:

- **Every git invocation timed.** A small Go program that execs
  `/usr/bin/git`, records how long it took and appends the arguments and
  the working directory, put first on `PATH`. It sees the package's own
  `exec.Command` calls and the tests' direct ones alike. Its own process
  startup inflates the run it measures (about 5ms a call), so the *total*
  of a wrapped run means nothing; the per-invocation times it records are
  git's own and are what is quoted below.
- **Per-test wall clock**, from `go test -v`.
- **`-cpuprofile`**, split two ways: the three loop tests, and everything
  else.

## The numbers

|                                  | plain | `-race` |
| -------------------------------- | ----- | ------- |
| package wall clock               | 60s   | 285s / 280s / 282s |
| git invocations                  | 10,400 | 10,400 |
| time inside git                  | 32.2s | 33.7s  |
| git as a share of the package    | 54%   | **12%** |

The git column is the point. The number of subprocesses and what they
cost does not change between the two builds -- git is a separate process
and the race detector cannot reach it -- so as the Go side gets four and
a half times more expensive, git's share collapses. CI builds with
`-race`. The 174-220s CI has recorded is this 280s run on a machine with
two cores and other packages on it.

Where the git calls go, by verb (plain build, whole package):

```
    4695   8.4s  rev-parse       1238   3.2s  add
     275   5.4s  push             896   2.2s  config
    1206   3.3s  status           200   1.8s  fetch
     259   1.7s  commit           278   1.4s  ls-remote
     120   1.4s  clone            234   0.5s  remote
```

Nothing in that list is slow. A git process here costs about 3ms; there
are simply ten thousand of them.

## Where the time actually goes

Two buckets, and they want different fixes.

### Three loop tests: 117s of the 247s (47%)

| test | `-race` | what it loops |
| ---- | ------- | ------------- |
| `TestChurnCommitsOnItsOwnClockAndNotOnEveryTick` | 45.1s | 480 `Sync`s (4h of 30s ticks) |
| `TestSoakTheRepositoryAndTheDatabaseStayInAgreement` | 37.9s | 60 rounds |
| `TestAnUnchangedDatabaseCommitsNothingAcrossAChurnBoundary` | 33.7s | 360 `Sync`s of a database nobody touched |

Their profile is one shape:

```
53.8%  staterepo.sync
43.9%    staterepo.ExportTier
37.7%      staterepo.exportTable
26.1%        modernc sqlite3Prepare -> RunParser -> yy_reduce
15.3%        staterepo.columns          (PRAGMA table_info)
```

A `Sync` exports all 56 tables, and every table costs two queries: a
`PRAGMA table_info` to learn its columns and a `SELECT ... ORDER BY` to
read its rows. That is 112 statements per tick, prepared from text every
time, and a quarter of the whole run's CPU is modernc's SQL parser
reducing that grammar again. The git in one of those ticks is four calls
and about 8ms -- 9% of the tick under `-race`.

`TestAnUnchangedDatabaseCommitsNothingAcrossAChurnBoundary` is the
clearest case: 360 full exports of a database that never changes, to
prove that HEAD does not move.

### The other 149 tests: 130s, about 0.87s each

None of them is slow and there is no tail to chase; they cost what a test
in this package costs to set up. Priced with a probe that does nothing
else:

| step | plain | `-race` |
| ---- | ----- | ------- |
| `t.TempDir` | 0.03ms | -- |
| `sqlite.Open` on a new file | 20ms | 53ms |
| `sqlite.Open` + `model.Store.Init` | 67ms | **456ms** |
| `bareRemote` (`git init --bare` + 2 configs) | 10ms | -- |
| `staterepo.Open` (init/clone + configure) | 55ms | -- |
| ... + `Load` (export, add, commit, push) | 306ms | -- |
| ... + `Apply` | 329ms | -- |

And the profile of those 149 tests agrees: **`openDB` is 32% of all CPU
in them**, nearly all of it `Store.Init` -> `applySchema` (30%), which
executes the 93 statements `model.Statements()` produces against a fresh
database. `ExportTier` is another 15%.

`applySchema` is already batched into one transaction (see its doc
comment -- that removed ~82 fsyncs and took Init from ~3s to ~120ms).
What is left is not I/O at all: it is SQLite parsing 93 DDL statements,
in pure Go, under `-race` and `-d=checkptr`. `runtime.checkptrBase` and
`checkptrAlignment` together are 20% of the tail's CPU, all of it from
modernc's emulated libc doing pointer arithmetic.

## Why nobody could measure the fsync change

Three reasons, and they compound:

1. **The effect was small.** Under `-race`, git is 12% and fsync-bearing
   commits are a fraction of what is left. `pkg/model/sqlite` is an fsync
   benchmark wearing a test suite; `pkg/staterepo` is not.
2. **The package is CPU-bound and single-threaded.** Both profiles report
   about 0.9-1.1 cores busy for their whole duration. On a two-core
   runner `go test ./...` runs other packages beside it, so the wall
   clock GitHub reports for `pkg/staterepo` includes however long it
   spent descheduled -- which depends on what else was running, not on
   anything in the package.
3. **On a machine of its own it is not noisy at all.** Three `-race` runs
   in an idle sandbox: 284.7s, 279.7s and 281.9s -- a spread of 1.8%,
   against the 26% the same package spans on CI. What grain/task-295 was
   reading is the runner, not the test.

So a change to this package can be measured -- but only by running it
alone (`go test -race ./pkg/staterepo/`, twice), never by reading the
package line out of a full CI run.

## Candidates, priced

Written down rather than done, in the order the numbers put them:

1. **Stop applying the schema 138 times.** Apply it once into a template
   database file and copy the file per test. Measured against the real
   thing: `copy + sqlite.Open` is 45ms under `-race` where `sqlite.Open +
   Init` is 456ms -- about 410ms for each of the 138 `openDB` call sites
   here, so roughly **55s of this package's 280s**, and
   `Store.Init`-per-test is how nearly every
   store-backed package in this repo opens a database, so the same helper
   pays out well beyond `pkg/staterepo`. This is the one worth doing
   first.
2. **Make the loop tests loop less, or export less per loop.** 480 + 360
   syncs is 117s. Two hours of ticks proves what four proves about a
   clock; and an export that did not re-`PRAGMA table_info` all 56 tables
   on every tick would take a quarter of the run's CPU with it.
   (`staterepo.columns` is called per table per export, and the answer
   cannot change while the process is running.)
3. **Memoise `rev-parse --absolute-git-dir`.** 2,987 of the 4,695
   `rev-parse` calls are this one question, asked by `gitDirFile` for
   every loaded-head and churn marker read. A `Repo`'s git directory does
   not move. Worth about 5s of git time -- 2% under `-race`, 9% without
   it, and cheap.

Not a candidate: `growth_test.go`, which the report on this package keeps
naming. It is skipped unless `GRAIN_GROWTH_TICKS` is set and costs CI
nothing.
