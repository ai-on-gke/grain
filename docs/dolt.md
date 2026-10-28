# Would Dolt improve grain's state syncing, or only add complexity?

**Status: evaluated, and the answer is no** (grain/task-347). Nothing in
the tree changes but this document and two pointers to it from the
README. The recommendation is to keep the store as it is -- embedded
SQLite (`pkg/model/sqlite`) materialised into a git repository of JSON
dumps (`pkg/staterepo`) -- and to stop treating Dolt as the thing grain
would reach for if it ever wanted history, a watch, or a cheaper churn
tier. It is not: three cheaper answers to those are named at the bottom,
and none of them costs a storage engine.

This is worth writing down rather than merely deciding, because grain ran
on Dolt once (v1, up to bwsalmon/agents#366) and the README twice names
something Dolt would have given for free and grain now has to build:
the commit history in "Grain no longer keeps a commit history", and the
watch in "Reconcilers, not a pipeline". Both are accurate about Dolt and
neither is an argument for going back, which is not obvious from either
sentence on its own. Both now point here.

The question is asked now, and not later, for a real reason: **there are
two deployments and one operator, so a format change is nearly free
today and will not be.** That cuts both ways, and it is the reason this
document ends with what would change the answer. What it does not do is
make the answer a bet on cost. The case against Dolt below is
structural -- it is about what the state repository is *for* -- and a
structural reason does not get better or worse with the number of
deployments.

## What grain has today, precisely

- The store is a single embedded SQLite file, opened by one process. 74
  tables, 3 views, `model.SchemaVersion` 16. `pkg/model` is 21,505
  non-test lines and knows nothing about a repository existing.
- Concurrency control is SQLite's own write lock, taken at `BEGIN`
  (`_txlock=immediate`), with `Store.write` retrying a loser up to five
  times and then returning `model.ErrConflict`. There is no per-row
  version and no merge (README, "Locking, not merging").
- `pkg/staterepo` (6,916 non-test lines, 11,443 test lines) exports every
  table to `tables/<name>.json` beside a `schema-version` stamp, commits
  and pushes on a 30s timer, and pulls the same repository back down on
  the same timer. Divergence is refused rather than resolved; a pull is
  fast-forward only.
- The export is **schema-driven, not hand-written**. `dump.go` reads the
  table list and each table's columns out of the database itself, so a
  new table or column costs nothing there. What a new table does cost is
  a *classification*: settings, which the repository carries and which
  may be replaced under a running daemon (`SettingsTables` in
  `bind.go`), or not, in which case the table lives in SQLite and the
  repository never sees it (`ExportConfig`).
  `tables_internal_test.go` fails the build when a name in either list is
  not a table the schema creates.
- The repository is a **GitHub** repository. An agent is dispatched at
  it, clones it through the same git proxy every other dispatch uses,
  branches, and opens an ordinary pull request that a human reviews and
  GitHub merges; `.github/workflows/grain-state-check.yml`, which grain
  installs into the repository itself, runs `grain state check` on that
  pull request so a change that would not import is red before the merge
  rather than after.

That last bullet is not a detail of the implementation. It is the whole
feature. The database was made into a repository so that grain's own
configuration could be changed by the one mechanism grain already has for
changing anything -- a branch, a pull request, a review, a merge -- and
everything else in `pkg/staterepo` exists to serve it.

## What Dolt costs, measured today rather than remembered

Everything in this section was measured in a sandbox on 2026-09-08
against `github.com/dolthub/driver` v1.88.1 (released 2026-05-07), which
pulls `github.com/dolthub/dolt/go`
v0.40.5-0.20260507221239-14b38e279fc6. The program measured is a
`main()` that does nothing but `sql.Open("dolt", ...)` and print the
result -- so these are the floor, not grain's cost.

| | grain today | hello-world importing only the Dolt driver |
|---|---:|---:|
| modules in the build list | 89 | 525 |
| `go.sum` lines | 133 | 1,394 |
| binary | 41.6 MB, static | 151.5 MB, dynamic |
| `CGO_ENABLED=0` | builds | **fails, both ways** |
| build needs | a Go toolchain | a Go toolchain and a C toolchain; ICU headers unless built with `-tags gms_pure_go` |
| links against | nothing | `libc` with that tag; `libicui18n.so.72`, `libicuuc.so.72`, `libicudata.so.72`, `libstdc++`, `libgcc_s`, `libm` besides, without it |

**One half of the 2025 objection has genuinely expired.**
`go-mysql-server` now ships a pure-Go regex implementation behind a
`gms_pure_go` build tag, so the ICU dependency -- headers to build
against, and a versioned `libicui18n.so.72` SONAME to die at exec time
on a host with a different major -- is a build flag away from gone.
Built with that tag and cgo on, the probe links against `libc` and
nothing else. That is a far weaker objection than the one the Makefile
and the README were written against, and it should be said plainly
rather than left as an old grievance.

**The other half has not.** `CGO_ENABLED=0` still fails with the tag,
because `dolthub/gozstd` has no pure-Go build at all -- its non-cgo files
reference types only its cgo half defines -- and it is imported by
`store/nbs` (`archive_reader.go`, `archive_writer.go`,
`archive_chunker.go`), which is Dolt's storage layer rather than an
optional command. So grain would stop being a static binary. That is the
coupling `CGO_ENABLED=0` exists for and which ICU used to cause one layer
up: not ICU specifically, but a binary that needs the build host's glibc
to agree with the controller's, carried to a machine chosen by
`terraform/gcp` and to a guest built from the repository's `Granulefile`.

The alternative -- `dolt sql-server` in a container beside the daemon,
spoken to over the MySQL wire with a pure-Go driver -- keeps the binary
static and reintroduces the second service that bwsalmon/agents#363
removed and `scripts/setup.sh` still explains the absence of, plus a
network hop on every store read in a process that currently makes them
in-process against a file.

One thing has genuinely stopped mattering. Embedded Dolt was
single-writer, and that used to be disqualifying because the daemon and a
separate UI process both wrote. They do not: the daemon serves the UI and
the CLI over REST and is the only process that opens the store. Whatever
else is wrong with embedding Dolt now, "only one process may hold it" is
not.

## What Dolt would actually buy

Taking the strongest version of the case, there are four real wins and
they should not be waved away.

1. **The dump goes away.** No `Export`/`Import`, no round-trip fidelity
   suite, no base64 encoding of blob columns, no `schema-version` stamp
   to refuse, no `grain state check`. The versioned thing would be the
   database rather than a text rendering of it, so a whole class of "the
   repository could give the database back wrong" cannot exist. That
   class is real: `fidelity_test.go`, `addedcolumns_test.go`,
   `migrate_test.go` and the `testdata/dump-schema-16` fixtures are all
   paying for it.
2. **Churn stops being a storage problem.** The hourly churn tier exists
   because git stores a whole new blob for every version of
   `task_run.json`, and that file is the largest in the dump by an order
   of magnitude. Dolt's storage is a content-addressed prolly tree, so a
   commit costs the rows that changed. The measured accepted cost today
   -- a restore is up to an hour behind on runs and CI -- would go away.
3. **Schema changes stop being destructive.** Today a schema bump moves
   the working tree aside and re-seeds, or reformats the repository over
   itself with a commit that says so (`state-repo.reformat`,
   `staterepo.Reformat`). Dolt keeps schema in the database with the
   data, and migrates rather than re-seeds.
4. **A watch and a `resourceVersion` become nearly free**, which the
   README's "Reconcilers, not a pipeline" already says: a commit hash is
   a version, and `dolt_diff` is a change feed with history. Row-level
   audit ("what did this task look like an hour ago") comes with it.

## What Dolt would not buy, which is the part that decides it

**A Dolt repository is not reviewable on GitHub, and review is the
point.**

Dolt gained git remotes since grain last looked -- `git+file`,
`git+http`, `git+https` and `git+ssh` are registered schemes in
`dbfactory`, in files carrying a 2026 copyright -- and it is worth being
precise about what they are, because the name invites exactly the wrong
conclusion. `git_remote.go`
and `store/blobstore/git_blobstore.go` use a git repository as a
*blobstore*: NBS chunks and a manifest, written as git objects under
`refs/dolt/data`, with a local bare repository as a cache. GitHub can
hold that. GitHub cannot show a diff of it, a pull request cannot target
a hidden non-branch ref, a reviewer cannot read it, a sandbox that clones
it through the git proxy gets chunk files it has no way to open, and
`grain state check` has nothing to run against. Git-as-blobstore is not
git-as-review, and grain wants the second one.

So adopting Dolt means the settings-change workflow moves to whatever
reviews Dolt branches -- DoltHub or DoltLab -- or is rebuilt by hand
inside grain. Both are worse than the thing being replaced, and the
second is a great deal more code than the part of `pkg/staterepo` it
would delete.
It also deletes the property that made this cheap in the first place: an
agent already knows how to work in a GitHub repository, so a settings
change needed no new agent surface at all.

**The classification is policy, and Dolt does not decide it either.**
The single most load-bearing decision in `pkg/staterepo` is which tables
a merged change may replace underneath a running daemon
(`SettingsTables`) and which are grain's own record of what it did. That
question is about what the rows *mean*, not about how they are stored, so
Dolt inherits it whole -- and inherits with it every commit in this
package's history that got the direction of travel wrong: "Never import a
state repository over a database that is ahead of it", "Import the
settings alone at a start, not grain's own record", "Stop the daemon from
committing over a settings change somebody merged", "Classify
pipeline_pause as a table a merged change must not replace live". None of
those was a serialization bug. Every one of them would have been
reachable on Dolt.

**Merging is the feature grain last had to disable.** Dolt merges
concurrent writers cell by cell and reports a conflict only where two of
them disagree about the same cell, which silently unioned two writers'
delete-and-reinsert of the same tag set. grain's answer was `grain_write`,
a shared row rewritten with a fresh random token on every transaction for
no purpose but to force an overlap to look like a disagreement -- and the
token had to be random rather than a counter, because two writers that
both wrote N+1 would have agreed. That whole mechanism is gone, replaced
by SQLite admitting one writer at a time. `pkg/staterepo` refuses to
merge for the same reason one level up: there is one writer, so
divergence means something the model does not describe, and failing
loudly beats guessing at a database dump. Dolt's headline capability is
the one grain paid to defeat and then deleted.

**Portability is not the obstacle, and that is worth saying** because it
would be the natural place to look for one. `pkg/model` writes fairly
plain SQL: no `INSERT OR REPLACE`, no `ON CONFLICT`, twelve `PRAGMA`
references and two `sqlite_master` reads in the whole tree, all of them
in `pkg/model/sqlite` and `pkg/staterepo` rather than in the queries. The
DDL would need work (`AUTOINCREMENT` and the type spellings), and the
concurrency model would need rewriting to whatever Dolt does at `COMMIT`,
but "every query would have to be rewritten" is not true and should not
be used as an argument. The case against Dolt does not need it.

## The hybrids, and why each one fails

- **Dolt as the store, keeping the JSON dump for review.** Pays every
  cost in the table above, keeps every line of `pkg/staterepo`, and gains
  two histories that can disagree. The only thing it buys over today is
  audit and a watch, at another ~110 MB of binary, a C toolchain, and the
  end of the static build.
- **Dolt as the state repository's format, keeping SQLite as the store.**
  Loses review, which is the point, and adds a second storage engine to
  the deployment image to do it.
- **Dolt for the churn tables only, JSON for the rest.** The most
  coherent of the three: it buys back the hour of restore lag on
  `task_run` and CI without touching the reviewable half. It still means
  cgo, a dynamically linked daemon, a ~150 MB binary and a second storage
  engine, to make a backup of grain's own commentary on itself an hour
  fresher, on two deployments that have never wanted it fresher. No.

## Recommendation

Keep SQLite and `pkg/staterepo`. The experience the question is asked
from is 128 commits between 2026-09-03 and 2026-09-07 -- a great deal of
iteration and very little calendar -- and what it bought was spent where
the value is: not on the dump format, which has been schema-driven and
largely untouched since it was written, but on the direction-of-travel
rules, the divergence and restore cases, and the GitHub surface around
the check grain installs (`docs/state-repo-check-startup-failures.md`,
`docs/state-pane-reset-notice.md`). Dolt replaces the part that has been
cheap and inherits or destroys the parts that have been expensive.

The caveat that comes with five days is worth stating rather than
hiding: the costs this design has *not* been made to pay yet are the
long-horizon ones. A repository that has been committed to every 30
seconds for a year, a schema migration on a deployment somebody else
operates, a restore performed in anger rather than in a test. Those are
the cases where a real versioned database earns its keep, and all three
of them are still ahead. They do not change the recommendation, because
the objection below the costs is about review and not about durability,
but they are the honest reason this document ends with premises to
re-check rather than with a decision nobody should reopen.

The things Dolt is remembered for being wanted for each have a cheaper
answer:

- **Audit trail.** The state repository already is one -- every settings
  change within 30 seconds, every task and comment likewise, churn within
  the hour, in `git log` on GitHub, readable by anything that reads text.
  What it is not is row-level (a task's value between two exports is not
  recoverable) and it is not queryable from inside grain. If that is
  wanted, it is an events table, which is what the README already says.
- **A watch, and a `resourceVersion`.** A change table and a sequence
  column, in a process that already holds the only handle on the store.
  Note the README's own ordering: a watch is a latency optimization over
  level-triggered reconciliation, never a replacement for it, and the
  reconcilers it would wake are not yet independent enough to want it.
- **The hour of churn lag.** If it ever bites, the cheap move is a
  shorter `ChurnInterval` and a look at whether `git gc --auto`'s
  threshold still holds, both of which are already tuned by measurement
  in `growth_test.go`.

## What would change the answer

Written down so a later reader can check the premises rather than
re-derive the argument.

1. **Grain's configuration stops being reviewed on GitHub.** If settings
   changes were ever proposed and merged through a first-party UI instead
   of a pull request, the decisive objection disappears and the whole
   question reopens.
2. **Two deployments have to share one live database.** Dolt's merge is
   built for multi-master, and nothing here is. Today the two instances
   each own their own state repository and share nothing; if they ever
   had to converge on one set of rows, home-grown syncing would be the
   wrong tool and this document would be wrong with it.
3. **Dolt builds with `CGO_ENABLED=0`.** It is now one dependency away:
   `-tags gms_pure_go` already removes ICU, and `dolthub/gozstd` --
   imported by `store/nbs` for archive reads and writes -- is the only
   thing left holding cgo on. A pure-Go zstd there (there are several to
   pick from) would make embedded Dolt a static-binary dependency, which
   removes the largest measured cost outright. It does not remove the
   review objection, but it changes the shape of every hybrid above, and
   it is one `go build` to re-check.
4. **Row-level history becomes a product requirement rather than a
   nice-to-have** -- a compliance answer, or agents that need to read the
   deployment's own past. An events table is the cheaper answer only for
   as long as "which columns do we log" has an easy answer.

## Reproducing the measurements

```sh
mkdir /tmp/doltprobe && cd /tmp/doltprobe && go mod init doltprobe
cat > main.go <<'EOF'
package main

import (
	"database/sql"
	"fmt"

	_ "github.com/dolthub/driver"
)

func main() { fmt.Println(sql.Open("dolt", "file:///tmp/doltdb?database=grain")) }
EOF
go get github.com/dolthub/driver@latest
go list -m all | wc -l                        # 525 on 2026-09-08
CGO_ENABLED=0 go build .                      # fails: go-icu-regex, gozstd
CGO_ENABLED=0 go build -tags gms_pure_go .    # fails: gozstd alone
CGO_ENABLED=1 go build .                      # needs libicu-dev; ldd shows libicu
CGO_ENABLED=1 go build -tags gms_pure_go .    # 151.5 MB; ldd shows libc alone
```

Against grain's own tree, for the other column: `go list -m all | wc -l`
(89), `make build && ls -l bin/grain` (41.6 MB, and `ldd` reports it is
not a dynamic executable).
