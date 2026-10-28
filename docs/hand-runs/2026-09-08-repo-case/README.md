# The harness behind 2026-09-08-repo-case.md

Four throwaway files, kept because the numbers in that write-up are
worth nothing if nobody can produce them again -- and because the run it
belongs to is not finished: whoever gets a copy of a real deployment's
data directory can skip straight to `drive.sh` and read a real report
with it. Nothing here builds or runs as part of grain: the Go file
carries a `.txt` suffix so `go build ./...` and `go vet` never see it,
and the shell scripts are not on any path CI walks.

Everything assumes `grain` and `handrun` are on `$PATH` and that a
store lives at `/home/debian/store-base`; the scripts write their copies
under `/home/debian/cases/<name>`.

1. `seed-store.go.txt` -- the store, and the census. Copy it to
   `handrun/main.go` in a checkout and `go build ./handrun`. Modes:

   * `-mode seed -dir DIR [-tasks 6000] [-runs 7500] [-transcript-kb 38]
     [-pinned 0.45]` writes a deployment a year old: 6,000 tasks, 7,500
     runs with real transcript columns, branches, releases, release
     trains, schedules, templates, per-repo configuration, memories and
     a qualification plan, with 240 tasks under `bwsalmon/Grain`, 68
     under `BWSalmon/kontur` and one repository the deployment names no
     spelling for. 299 MB, about 30 seconds. Everything goes through
     `pkg/model`'s own API -- not `ui.Client`, whose `canonicalRepo` is
     exactly what stops a divergent spelling being stored today.
   * `-mode census -dir DIR` counts every repo-keyed table by spelling,
     walking `model.RepoCaseTables()` itself, so a table the
     reconciliation gains is a table this counts without being edited.
     This is what checks the report's numbers.
   * `-mode conflict-release|conflict-prefix|conflict-base|conflict-memory
     -dir DIR` adds one row to a seeded store that the reconciliation
     cannot decide: a release of one name under both spellings, a
     release train of one prefix under both, a task writing both with
     different base branches, a repo memory under both with different
     text.
   * `-mode one-run -dir DIR` makes exactly one `task_run` row divergent
     in an otherwise clean store, which is how section 2's "one changed
     line in a 302 MB file" figure was taken.

2. `drive.sh <case>` -- the procedure `docs/test-plans/2026-09-06.md`
   section 5 asks for, against a copy: copy the data directory, `grain
   state adopt` into a local bare repository, report, `-apply`, and then
   print the commit the export left and what it cost. Every timing in
   section 0 comes from here.

3. `refusals.sh <case>` -- the same up to the report, with all four
   conflicts seeded at once: the report names them, `-apply` exits 1,
   and the census afterwards shows nothing was written.

4. `durability.sh` -- the settings-import half. Adopts a repository,
   merges a settings change into it behind the deployment's back, shows
   `-apply` refusing over the un-imported commit, takes the documented
   way out (start the daemon, stop it, run it again), and checks that
   the merged setting and the reconciliation both survive. It ends with
   the one-row export measurement.

A `grain daemon` started by these scripts wants `-sandbox-dir` and, on
a machine with no agent CLI installed, will report that its default
agent framework is not ready. That is expected: nothing here needs a run
to actually start, only a start to happen.
