// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package staterepo

// Reformatting: what a schema change does to a repository that has a
// remote.
//
// Load refuses a dump stamped with a schema this build does not know
// (bind.go), and that refusal is right in every ordinary case: importing
// it is impossible and exporting over it would replace somebody's rows
// with a dump written to a shape they never agreed to. But a schema
// change is a case where the refusal has nowhere to go on its own.
// scripts/setup.sh moves the store aside on a schema bump so the daemon
// starts on a fresh database, and moves the working tree aside beside it
// so the daemon re-seeds the repository -- which works exactly as long as
// the repository is local-only. With a remote, the tree the script
// archived comes straight back down on the next start, still holding the
// old dump and the old stamp, and the daemon exits on it; systemd
// restarts it into the same failure, forever. Nothing in the loop can
// break it, because every part of it is behaving correctly.
//
// So the schema bump has to say what it did. It is the step that decided
// the old rows were not worth keeping -- it moved the store holding them
// aside -- and this is the other half of that same decision, applied to
// the same rows in their other form. The instruction travels as a file in
// the data directory that survives both the store and the working tree
// (cmd/grain's own stateRepoReformatStamp), and Reformat below is what
// carries it out: the database this build actually has, written over the
// dump it cannot read, in one commit that says which schema it left and
// which it arrived at.
//
// What is not lost: the old dump is a commit in this history like any
// other, so `git show` on the commit before this one gives it back whole.
// What is: the tip of the branch stops being the deployment's old state,
// which is the same thing the archived store means on the host. A schema
// change is destructive here on purpose (README.md), and this is the
// shape that destruction takes when there is a remote to tell about it.
//
// Deliberately not inferred from anything Load can see. "An old dump and
// an empty database" is also exactly what a restore looks like -- clone
// the repository onto a new machine, point grain at it -- and a build
// that re-seeded on that would delete the only copy of a deployment
// whose real answer is "run the build that wrote it". The difference
// between the two is not in the repository, it is in whether somebody
// asked for a reformat, so that is what is asked.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Reformat replaces whatever the repository holds with this database,
// exported at this build's schema, and pushes it.
//
// It is the deliberate, destructive answer to a dump this build cannot
// read, and it decides nothing: a caller that calls this has already
// decided that the rows in the repository are not the ones to keep. See
// this file's own comment above for who decides and how they say so.
//
// The bool reports whether a commit was made, and it is separate from
// the error on purpose. A push that fails after the commit -- the remote
// went away between the pull and the push -- leaves this host's working
// tree correctly reformatted and the remote not yet told, which is a
// state the ordinary sync loop resolves with its next push. A caller
// clearing the instruction that got it here should clear it on the
// commit, not on the push, or the next start will do all of this again
// against a repository that no longer needs it.
func Reformat(ctx context.Context, r *Repo, db *sql.DB, version int) (bool, error) {
	return rewrite(ctx, r, db, version, reformatCommitMessage)
}

// rewrite is the whole of Reformat's mechanics, and of Migrated's
// (migrate.go): pull, export this database over whatever is there, stamp
// it, commit, push.
//
// The two differ in one thing only, and it is the one thing a reader of
// the history cannot work out from the diff -- whether the rows in the
// commit before this one came across or were left behind -- so the
// message is what is passed in and everything else is shared.
func rewrite(ctx context.Context, r *Repo, db *sql.DB, version int,
	message func(found, version int, build string, now time.Time) string) (bool, error) {
	// The same fetch Load makes, and for the same reason: this commits on
	// top of whatever the remote holds, so it has to be on top of the
	// current tip or the push behind it cannot fast-forward. An
	// unreachable remote is carried rather than fatal, exactly as it is
	// in Load -- a host with a working tree and no network still has a
	// repository to reformat, and the push at the end is where that is
	// felt.
	var unreachable error
	if _, err := r.Pull(ctx); err != nil {
		if !errors.Is(err, ErrUnreachable) {
			return false, err
		}
		unreachable = err
	}
	// Read before it is overwritten: the stamp that was in the way is
	// half of what the commit message has to say, and a message that
	// named only where it arrived would leave a reader to work out what
	// this replaced from the diff of an entire dump.
	found := 0
	if HasDump(r.Dir()) {
		// An unreadable stamp is not worth refusing over here. It is one
		// of the shapes "this build cannot read what is there" takes, and
		// the commit message says "an unknown schema" rather than a
		// number.
		found, _ = ReadSchemaVersion(r.Dir())
	}
	if err := writeReadme(r.Dir(), deploymentName(ctx, db)); err != nil {
		return false, err
	}
	// The same export a tick of the sync loop makes, and it has to be the
	// same one: this writes into the state repository's working tree and
	// pushes it, so anything it wrote that a tick does not would be
	// published to the remote and then deleted again by the very next
	// sync. It was a whole-database Export until now -- which predates
	// grain/task-395 narrowing the repository to the configuration, and
	// was how a schema change committed every task, run and CI step this
	// deployment had ever recorded into a repository that agents are
	// dispatched to clone.
	//
	// What that Export was here for is still wanted and ExportConfig
	// still does it: every file the repository carries is replaced rather
	// than updated, and a file left behind by a table this build no
	// longer exports is removed rather than inherited.
	if err := ExportConfig(ctx, db, r.Dir()); err != nil {
		return false, err
	}
	if err := WriteSchemaVersion(r.Dir(), version); err != nil {
		return false, err
	}
	now := r.now()
	committed, err := r.Commit(ctx, message(found, version, r.cfg.Build, now))
	if err != nil {
		return false, err
	}
	// Recorded as this host's own, exactly as a seed does: the commit
	// just made is one this host wrote, so the next start must not read
	// it as a repository that moved underneath it and import the dump it
	// has just exported.
	if err := r.recordLoadedHead(ctx); err != nil {
		return committed, err
	}
	if err := r.recordExportedBuild(ctx); err != nil {
		return committed, err
	}
	if err := r.Push(ctx); err != nil {
		if unreachable != nil {
			return committed, fmt.Errorf("%w (the new dump is committed in %s and will go out "+
				"with the next push that works: %v)", unreachable, r.Dir(), err)
		}
		return committed, err
	}
	return committed, nil
}

// reformatCommitMessage says what this commit is, which is the one thing
// in this repository's history a reader cannot get from the diff.
//
// The diff is every row in the deployment at once, which is what an
// export by a new build looks like too (exportCommitMessage) -- and the
// difference between those two commits matters more than either: one is
// the same state written another way, and this one is a different state.
// So it says both schemas, and it says plainly that the rows the commit
// before it holds are not coming back.
func reformatCommitMessage(found, version int, build string, now time.Time) string {
	was := "an unknown schema"
	if found > 0 {
		was = "schema " + strconv.Itoa(found)
	}
	msg := "Reformat grain state for schema " + strconv.Itoa(version) + "\n\n" +
		"This deployment's schema changed, from " + was + " to schema " +
		strconv.Itoa(version) + ".\n" +
		"grain cannot migrate a dump across a schema change any more than it can\n" +
		"migrate the database it came from, so the store holding those rows was\n" +
		"moved aside on this host and this commit is the other half of that: the\n" +
		"database this deployment actually runs on now, written out at the schema\n" +
		"this build knows.\n\n" +
		"The rows this replaces are not gone, they are the commit before this one.\n" +
		"Nothing after this point carries them, and nothing will bring them back:\n" +
		"a restore from here restores the deployment as it is now.\n\n" +
		"Written by grain at " + now.UTC().Format(time.RFC3339) + "."
	if build != "" {
		msg += "\n\nThe build that wrote it: " + build + "."
	}
	return msg
}
