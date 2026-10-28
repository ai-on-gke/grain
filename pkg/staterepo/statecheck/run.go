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

package statecheck

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Options is what `grain state check` is asked beyond the directory.
type Options struct {
	// Migrate accepts a dump stamped with another schema and imports it
	// the way an upgrade would (`-migrate`).
	Migrate bool
	// PreviousAPIScopeSets is the previous version of
	// tables/api_scope_set.json -- the base the change is against -- for
	// the effective-scope diff APIScopeSets prints. Nil prints no diff;
	// empty is a base that declared no sets.
	PreviousAPIScopeSets []byte
}

// Run is the whole of `grain state check`: it loads dir into a database
// it throws away, runs every check this package has against what
// arrived, and writes to out what a CI log should say about it -- the
// warnings, the schema, the rows, and "ok: grain can load this" -- in
// the same words whichever caller asked. An error is a dump grain could
// not load or would refuse; out has already been told whatever led to
// it.
//
// It is here rather than in cmd/grain because there are two callers: the
// command, run in the state repository's own CI, and the daemon, which
// runs it in-process as the grain CI check of its own configuration
// repositories (pkg/daemon's ConfigRepoCISource) so that the check needs
// no image of the build it is running.
func Run(ctx context.Context, out io.Writer, dir string, opts Options) error {
	db, cleanup, err := ScratchStore()
	if err != nil {
		return err
	}
	defer cleanup()

	check := staterepo.Check
	if opts.Migrate {
		scratch, err := os.MkdirTemp("", "grain-migration-check")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		check = func(ctx context.Context, db *sql.DB, dir string, version int) (staterepo.CheckReport, error) {
			return staterepo.CheckMigration(ctx, db, dir, scratch, version)
		}
	}
	report, err := check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		// Whatever the import objected to, printed before the error, so a
		// CI log says which schema the dump claimed as well as why it did
		// not load.
		for _, w := range report.Warnings {
			fmt.Fprintf(out, "warning: %s\n", w)
		}
		return err
	}
	for _, w := range report.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	if report.SchemaVersion == model.SchemaVersion {
		fmt.Fprintf(out, "schema:  %d, which is what this build of grain knows\n", report.SchemaVersion)
	} else {
		fmt.Fprintf(out, "schema:  %d, migrated into the %d this build of grain knows\n",
			report.SchemaVersion, model.SchemaVersion)
	}
	fmt.Fprintf(out, "loaded:  %d rows across %d tables\n", report.Total(), len(report.Rows))
	for _, t := range report.Tables() {
		fmt.Fprintf(out, "  %-28s %d\n", t, report.Rows[t])
	}
	if err := CustomFields(ctx, db); err != nil {
		return err
	}
	unusable, err := UnusableCustomFieldValues(ctx, db)
	if err != nil {
		return err
	}
	for _, w := range unusable {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	stranded, err := StrandedCustomFieldValues(ctx, db)
	if err != nil {
		return err
	}
	for _, w := range stranded {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	unrunnable, err := UnrunnableSettings(ctx, db)
	if err != nil {
		return err
	}
	for _, w := range unrunnable {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	if err := APIScopeSets(ctx, db, out, opts.PreviousAPIScopeSets); err != nil {
		return err
	}
	fmt.Fprintln(out, "ok: grain can load this")
	return nil
}
