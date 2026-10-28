package upgrade

// The step that decides whether the build being cut over to can run on
// this deployment's data at all (grain/task-59).
//
// Everything else this package checks is a property of the new build
// alone: it pulled, it runs, it names a sandbox image. That was the
// whole of it until a release changed model.SchemaVersion, and then the
// upgrade did exactly what it was told -- pulled a healthy image, ran
// `grain schema-version` in it (which touches no store, deliberately),
// repointed the ref file and restarted -- into a build whose first act
// was to refuse the store on disk. The UI never bound a port, systemd
// restarted into the identical failure, and the rollback that exists for
// a broken binary never fired, because nothing was broken: the image was
// healthy and only this host's rows were in the way.
//
// So an upgrade now asks the new build for its schema version as well as
// for proof that it runs, and when the answer differs from the running
// build's it does two more things before the ref file is touched:
//
//	Prepare -- the running build writes its whole database out, at its
//	own schema, somewhere the restart cannot take it with it
//	(cmd/grain's writeSchemaUpgrade)
//
//	CheckArgs -- the *new* build is asked to import that dump at its own
//	schema, in a container that touches nothing (`grain state check
//	-migrate`). This is the health check the health check could not be:
//	not "does this build run" but "does this build run on these rows"
//
// A failure in either leaves the deployment exactly where it was, on the
// build that is serving it, with the ref file untouched and the note
// discarded -- and the pane says which rows the new build could not
// take. A success cuts over as usual, and the new build's first start
// finds the note and migrates itself onto those rows
// (pkg/cli/schemaupgrade.go).
//
// Not offered as a choice, and not confirmed: an operator clicking
// Upgrade is asking for the deployment to be running the new build
// afterwards, which is what this makes true. The destructive answer --
// move the store aside, start empty -- is still there for a schema
// change no migration can carry, and it is still `scripts/setup.sh`,
// where an operator is on the host and has said so.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// SchemaConfig, set on Config.Schema, is what an Upgrader needs to
// notice that a build changes this deployment's schema and to carry the
// deployment across it. Left nil -- by `grain demo`, by a test, by any
// deployment with no state worth migrating -- an upgrade behaves exactly
// as it did before this existed: the step is planned and skipped.
type SchemaConfig struct {
	// Version is the schema the running build knows,
	// model.SchemaVersion at the one real call site. Zero switches the
	// whole step off, since there is nothing to compare against.
	Version int
	// VersionArgs asks the new build for its own -- {"schema-version"},
	// the same subcommand HealthCheckArgs runs, whose entire output is
	// the number. Run separately from the health check rather than by
	// reading that step's output: the health check is about a build
	// starting at all, its output is streamed into the log line by line,
	// and a step that needs an answer should ask for one.
	VersionArgs []string
	// CheckArgs, with the directory Prepare returned appended, asks the
	// new build to rehearse the migration -- {"state", "check",
	// "-migrate"}. Empty skips the rehearsal and cuts over on the
	// export alone, which is a deployment saying it would rather find
	// out on the far side.
	CheckArgs []string
	// Prepare writes this deployment's rows down for the new build to
	// come back on, and returns the directory holding them. It runs
	// after everything else has succeeded and immediately before the
	// cut-over, so that the window between the rows being read and the
	// process being restarted is as short as this package can make it.
	// Required when Version is set. branch is the branch being upgraded
	// onto, which nothing reads and which is worth recording where it
	// lands: a directory holding a deployment's rows should say which
	// upgrade put it there.
	//
	// A Prepare that returns an error must leave nothing of its own
	// behind: Discard is not called on that path, deliberately, because
	// this is the one caller that cannot tell a half-written export from
	// an untouched earlier one (grain/task-255, and writeSchemaUpgrade's
	// own comment).
	Prepare func(ctx context.Context, branch string, from, to int) (string, error)
	// Discard undoes Prepare -- a Prepare that succeeded, and an upgrade
	// that then did not go on to cut over, because a note left armed
	// behind a failed upgrade is a migration waiting to happen at some
	// unrelated later restart, onto rows that stopped describing this
	// deployment hours ago. It is not the cleanup for a Prepare that
	// failed; see there.
	Discard func()
}

// schemaStep runs the whole of the above, and reports what to add to the
// upgrade's Detail line.
//
// build is how to run the new build: the image path runs the pulled ref
// in a container, the source path runs the binary it has just installed.
func (u *Upgrader) schemaStep(ctx context.Context, p *progress, branch string, build schemaRunner) (string, error) {
	cfg := u.cfg.Schema
	if cfg == nil || cfg.Version == 0 || len(cfg.VersionArgs) == 0 {
		p.skip(stepSchema, "this deployment does not check schema changes")
		return "", nil
	}
	p.start(stepSchema)
	to, err := u.newBuildSchema(ctx, p, build)
	if err != nil {
		return "", err
	}
	if to == cfg.Version {
		p.done(stepSchema, fmt.Sprintf("unchanged: both builds are at schema %d", to))
		return "", nil
	}
	if cfg.Prepare == nil {
		return "", fmt.Errorf("the build being upgraded to is at schema %d and this one at %d, and this "+
			"deployment has no way to carry its rows across -- upgrading would leave a store this build "+
			"cannot open; re-run scripts/setup.sh on the host instead", to, cfg.Version)
	}
	p.logf("%s: this build is at schema %d and the one being upgraded to is at %d -- writing this "+
		"deployment's rows out for it", stepSchema, cfg.Version, to)
	dir, err := cfg.Prepare(ctx, branch, cfg.Version, to)
	if err != nil {
		// No discardSchemaUpgrade here, unlike every failure below this
		// line. Discard removes the whole of what Prepare writes, and
		// this is the one point at which that is not this upgrade's to
		// remove: a Prepare that failed before it cleared the ground
		// leaves an *earlier* upgrade's note, which may still be the one
		// this deployment is waiting to restart onto, and nothing here
		// can tell that case from a half-written export of its own. The
		// one thing that can is Prepare, which cleans up what it wrote
		// (see its doc comment) -- so this step does not throw away what
		// it did not write.
		return "", fmt.Errorf("writing this deployment's rows out to migrate them from schema %d to %d: %w",
			cfg.Version, to, err)
	}
	if len(cfg.CheckArgs) > 0 {
		argv := append(append([]string{}, cfg.CheckArgs...), dir)
		if err := build.rehearse(ctx, p, dir, argv); err != nil {
			// Nothing has cut over: the ref file (or the installed
			// binary's health check, on the source path) is still what
			// it was, so this deployment goes on running the build that
			// wrote these rows -- which is the only build that can.
			u.discardSchemaUpgrade()
			return "", fmt.Errorf("the build being upgraded to is at schema %d, this deployment is at %d, "+
				"and that build could not take this deployment's rows -- so nothing has changed and it is "+
				"still running the build that can: %w", to, cfg.Version, err)
		}
	} else {
		p.logf("%s: no migration check configured -- cutting over on the export alone", stepSchema)
	}
	p.done(stepSchema, fmt.Sprintf("schema %d -> %d: this deployment's rows are in %s, and the new build "+
		"imports them at its first start", cfg.Version, to, dir))
	return fmt.Sprintf(", migrating this deployment from schema %d to %d", cfg.Version, to), nil
}

// schemaRunner is how this step reaches the build being upgraded to. The
// two pipelines answer it differently -- image.go runs the pulled ref in
// a container, upgrade.go runs the binary it has just installed -- and
// the difference between asking and rehearsing is not cosmetic: one
// wants the answer back, and the other wants a whole import's worth of
// output streamed into the pane and a directory on this host visible to
// whatever is doing the importing.
type schemaRunner interface {
	// ask runs args and hands back what the build printed on stdout,
	// logging what it printed on stderr under the step as it goes -- so
	// that a question this upgrade cannot get an answer to says why,
	// rather than only that it failed.
	ask(ctx context.Context, p *progress, args []string) (string, error)
	// rehearse runs args with dir readable by the build, logging what it
	// prints under the schema step as it goes.
	rehearse(ctx context.Context, p *progress, dir string, args []string) error
}

// newBuildSchema asks the build being upgraded to which schema it knows.
//
// An answer that is not a number is a failure rather than a shrug. The
// health check has already run this very subcommand successfully in this
// very build, so "it printed something else" means this upgrade cannot
// reason about what it is about to do -- and cutting over on that is how
// the failure this whole file exists for happened in the first place.
func (u *Upgrader) newBuildSchema(ctx context.Context, p *progress, build schemaRunner) (int, error) {
	printed, err := build.ask(ctx, p, u.cfg.Schema.VersionArgs)
	if err != nil {
		return 0, fmt.Errorf("asking the build being upgraded to which schema it knows: %w", err)
	}
	text := strings.TrimSpace(printed)
	// The first line, whatever else follows it, the same allowance
	// sandboxImage makes: the subcommand prints exactly the number, and
	// anything after it -- a trailing newline, a parting line on the way
	// out -- is not part of the answer. Note that this is only an
	// allowance for what comes *after*: a build that logged something
	// on stdout before answering would fail the Atoi below, which is the
	// safe way for it to go wrong.
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	version, err := strconv.Atoi(text)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("the build being upgraded to answered %q when asked which schema it knows, "+
			"which is not a schema version -- refusing to cut over to a build this upgrade cannot "+
			"reason about", strings.TrimSpace(printed))
	}
	p.logf("%s: the build being upgraded to knows schema %d", stepSchema, version)
	return version, nil
}
