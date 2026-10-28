package cli

// stateprepare.go is `grain state prepare-upgrade`: the half of the
// Upgrade button's schema migration (schemaupgrade.go) that
// scripts/setup.sh can run, so that re-running setup.sh across a schema
// change carries the deployment's rows the way the button does instead
// of starting an empty store (grain/task-4357).
//
// It is run by the build being installed, against a store written by
// the build before it, with the daemon stopped. It writes the same
// upgrade note the button writes (daemon.PrepareStoreSchemaUpgrade),
// then rehearses the import into a throwaway database -- the check the
// button asks the new image for (`grain state check -migrate`), made
// here in-process because this already is the new build. A rehearsal
// that fails removes the note again, so nothing is armed for a start
// that would only fail the same way.
//
// What it prints on stdout is for setup.sh to branch on, one line:
//
//	prepared FROM TO DIR   the note is written; the next start migrates
//	current N              the store is already at this build's schema
//	none                   there is no stamped store to carry (SQLite or Postgres)

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/staterepo/statecheck"
)

func statePrepareUpgrade(ctx context.Context, dataDir string, out io.Writer, args []string) error {
	if err := noArgsFor("grain state prepare-upgrade", args); err != nil {
		return err
	}
	from, dir, err := daemon.PrepareStoreSchemaUpgrade(ctx, dataDir, "scripts/setup.sh")
	if errors.Is(err, daemon.ErrNoStore) {
		fmt.Fprintln(out, "none")
		return nil
	}
	if err != nil {
		return err
	}
	if dir == "" {
		fmt.Fprintf(out, "current %d\n", from)
		return nil
	}
	if err := rehearseSchemaUpgrade(ctx, dir); err != nil {
		daemon.DiscardSchemaUpgrade(dataDir)
		return fmt.Errorf("this build could not take the rows exported from the store at schema %d, so "+
			"nothing is armed and the store is untouched: %w", from, err)
	}
	fmt.Fprintf(out, "prepared %d %d %s\n", from, model.SchemaVersion, dir)
	return nil
}

// rehearseSchemaUpgrade imports dir into a scratch database at this
// build's schema, reporting what would arrive on stderr -- stdout is
// setup.sh's to parse.
func rehearseSchemaUpgrade(ctx context.Context, dir string) error {
	db, cleanup, err := statecheck.ScratchStore()
	if err != nil {
		return err
	}
	defer cleanup()
	scratch, err := os.MkdirTemp("", "grain-migration-check")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	report, err := staterepo.CheckMigration(ctx, db, dir, scratch, model.SchemaVersion)
	for _, w := range report.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "rehearsed: %d rows across %d tables migrate from schema %d into %d\n",
		report.Total(), len(report.Rows), report.SchemaVersion, model.SchemaVersion)
	return nil
}
