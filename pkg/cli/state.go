// state.go is `grain state`, the operator's own handle on the state
// repository (pkg/staterepo) -- the git repository holding the database
// this deployment runs on.
//
// Like `grain secrets`, it edits files under -data-dir directly rather
// than talking to a daemon, so it only works on the host the server runs
// on. The config repository is always controller-local; this command
// inspects, validates and exports it but never connects it to a remote.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/staterepo/statecheck"
)

const stateUsage = `usage: grain state -data-dir DIR <command> [args]

-data-dir must name the same root a colocated ` + "`grain daemon`" + ` was
started with; it defaults to $GRAIN_DATA_DIR when that is set, which a host
installed by scripts/setup.sh exports already. This edits files on disk, not
a running daemon.

Commands:
  status                    inspect this installation's config repository
  repo [-branch B] [-token-file T] [URL]
                            use an external git config repository in read-only IaC
                            mode (or report the current mode; -none uses internal
                            grain/config)
  sync                      export the database and commit it now (or pull and
                            apply in external config repository mode)
  resets clear              print the settings-reset notice this host is keeping,
                            then forget it -- the terminal's half of the button
                            the State pane has, for a deployment with no browser
  reformat                  say that this deployment's schema changed, so the next
                            start replaces the repository's dump -- written before
                            the change and unreadable by this build -- with this
                            deployment's own database
  restore-snapshot DIR      restore the settings and memory tables from a snapshot
                            directory (written to data-dir/settings-snapshots before
                            an import that changes rows)
  prepare-upgrade           export a store this build cannot open (another schema)
                            for this build's next start to migrate, the way the
                            Upgrade button does, and rehearse that import first.
                            Prints "prepared FROM TO DIR", "current N" or "none".
                            Stop the daemon first; scripts/setup.sh runs it
  check DIR                 load a state repository's files into a throwaway
                            database and report what breaks. Needs no -data-dir
                            and touches nothing; agents use it to validate a
                            proposed grain/config change
  key show                  print this installation's secrets public key
  key path                  print where the private key is read from
  key import [-key-file F]  install a secrets private key this operator holds,
                            reading it from -key-file or stdin
`

func stateCmd(args []string) {
	if err := runState(args); err != nil {
		fmt.Fprintln(os.Stderr, "grain state: "+err.Error())
		os.Exit(1)
	}
}

func runState(args []string) error {
	fs := flag.NewFlagSet("grain state", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, stateUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root directory a colocated `grain daemon` was started with (required; $"+dataDirEnvVar+" supplies the default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("a command is required")
	}
	ctx := context.Background()
	// check is not about this host's own installation: it reads a
	// directory somebody hands it -- a CI runner with a checkout and
	// nothing else, or an operator's own clone of a repository no
	// deployment has been pointed at yet. Neither has a data directory, a
	// store or a daemon, so it is dispatched before -data-dir is insisted
	// on rather than being made to invent one.
	switch rest[0] {
	case "check":
		return stateCheck(ctx, rest[1:])
	}
	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}
	cmd, cmdArgs := rest[0], rest[1:]
	// The three below are handed this host's data directory and nothing
	// else, so a word written after one of them has nowhere to go: it is
	// refused here rather than in each of them, since none has a flag
	// set of its own to hang the check on.
	switch cmd {
	case "status", "sync", "reformat":
		if err := noArgsFor("grain state "+cmd, cmdArgs); err != nil {
			return err
		}
	}
	switch cmd {
	case "status":
		return stateStatus(ctx, *dataDir, os.Stdout)
	case "repo":
		return stateRepoCommand(ctx, *dataDir, os.Stdout, cmdArgs)
	case "sync":
		return stateSync(ctx, *dataDir)
	case "resets":
		return stateResets(ctx, *dataDir, os.Stdout, cmdArgs)
	case "reformat":
		return stateReformat(*dataDir)
	case "restore-snapshot":
		return stateRestoreSnapshot(ctx, *dataDir, os.Stdout, cmdArgs)
	case "prepare-upgrade":
		return statePrepareUpgrade(ctx, *dataDir, os.Stdout, cmdArgs)
	case "key":
		return stateKey(*dataDir, cmdArgs)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func stateStatus(ctx context.Context, dataDir string, out io.Writer) error {
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		return err
	}
	head, _ := repo.Head(ctx)
	if head == "" {
		head = "(nothing committed yet)"
	}
	version, err := staterepo.ReadSchemaVersion(repo.Dir())
	if err != nil {
		return err
	}
	settings, _ := staterepo.LoadSettings(dataDir)
	if settings.Remote != "" {
		fmt.Fprintf(out, "repository: %s (external config repository, read-only IaC mode)\n", settings.Remote)
	} else {
		fmt.Fprintln(out, "repository: grain/config (controller local)")
	}
	reportStateRepo(dataDir, out)
	fmt.Fprintln(out, "backup:     run `grain backup` for Google Drive or GCS; use daemon -backup-interval to schedule it")
	fmt.Fprintf(out, "branch:     %s\n", repo.Branch())
	fmt.Fprintf(out, "working tree: %s\n", repo.Dir())
	fmt.Fprintf(out, "head:       %s\n", head)
	fmt.Fprintf(out, "schema:     %d in the repository, %d in this build\n", version, model.SchemaVersion)
	fmt.Fprintf(out, "secrets:    %s\n", daemon.SecretsConfig(dataDir).File)
	if _, statErr := os.Stat(daemon.SecretsConfig(dataDir).File); statErr == nil {
		store := secrets.Open(daemon.SecretsConfig(dataDir))
		if err := store.Check(); err != nil {
			fmt.Fprintf(out, "            unreadable: %v\n", err)
			if want, err := store.FileRecipient(); err == nil && want != "" {
				fmt.Fprintf(out, "            encrypted to %s -- install that key with `grain state key import`\n", want)
			}
		}
	}
	reportSecretsRepo(dataDir, out)
	reportUndeliveredResets(ctx, repo, out)
	reportLatestSnapshot(dataDir, out)
	reportSecretsKey(dataDir, out)
	return nil
}

func reportUndeliveredResets(ctx context.Context, repo *staterepo.Repo, out io.Writer) {
	kept, ok := repo.UndeliveredResets(ctx)
	if !ok {
		return
	}
	writeUndeliveredResets(kept, out)
	fmt.Fprintf(out, "            Settings -> State says the same and has the button that clears this;\n")
	fmt.Fprintf(out, "            `grain state resets clear` is that button at a terminal.\n")
}

func reportLatestSnapshot(dataDir string, out io.Writer) {
	snapPath, snapAt, err := staterepo.LatestSnapshot(dataDir)
	if err != nil || snapPath == "" {
		return
	}
	if !snapAt.IsZero() {
		fmt.Fprintf(out, "snapshot:   %s (%s)\n", snapPath, snapAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(out, "snapshot:   %s\n", snapPath)
	}
}

// writeUndeliveredResets renders the marker itself: what each merge
// replaced, when, and why nobody was told where they did it.
//
// Its own function because two commands print it -- `status`, above, and
// `resets clear`, which prints what it is about to forget before it
// forgets it -- and the discipline the whole feature keeps is that no
// two surfaces come to say different things about one repository. What
// each caller adds is the sentence about what to do next, which is the
// one part that differs: status points at the two ways to dismiss, and
// the dismissal points at nothing.
func writeUndeliveredResets(kept staterepo.UndeliveredResets, out io.Writer) {
	fmt.Fprintf(out, "resets:     %d merged settings %s replaced a value here and grain had nowhere to "+
		"say so but this host\n", len(kept.Notices), plural(len(kept.Notices), "change", "changes"))
	if kept.Dropped > 0 {
		fmt.Fprintf(out, "            (%d earlier %s dropped to keep this file bounded; %s in this "+
			"deployment's journal)\n", kept.Dropped, plural(kept.Dropped, "one was", "were"),
			plural(kept.Dropped, "it is", "they are"))
	}
	for _, notice := range kept.Notices {
		fmt.Fprintf(out, "            %s, merging %s: %s\n",
			notice.At.UTC().Format(time.RFC3339), shortSHA(notice.Commit), notice.Reason)
		for _, v := range notice.Values {
			fmt.Fprintf(out, "              %s\n", v)
		}
		if notice.MoreValues > 0 {
			fmt.Fprintf(out, "              ... and %d more, in this deployment's journal\n", notice.MoreValues)
		}
	}
	fmt.Fprintf(out, "            those values are gone from the database: merge a settings file that\n")
	fmt.Fprintf(out, "            carries each column with the value it should have to put them back.\n")
}

// stateResets is the terminal's handle on that same marker, which today
// has exactly one thing to do to it.
//
// Split as `resets clear` rather than as a flag on `status` because the
// two are different acts: one reports and the other deletes, and a
// reporting command that deletes something when a flag is passed is one
// an operator can reach for while reading.
func stateResets(ctx context.Context, dataDir string, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: grain state resets clear")
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "clear":
		if len(rest) > 0 {
			return fmt.Errorf("grain state resets clear takes no arguments (got %q)", rest[0])
		}
		return stateResetsClear(ctx, dataDir, out)
	default:
		return fmt.Errorf("unknown resets command %q (usage: grain state resets clear)", cmd)
	}
}

// stateResetsClear is a person at a terminal saying they have read what
// a merge took away: the same delete the State pane's Dismiss button
// does (staterepo.Repo.ClearUndeliveredResets, through
// stateManager.DismissResetNotices), for the deployment the notice
// exists for in the first place -- the one with no browser open on it,
// which until now could read the notice in `grain state status` and
// never clear it, so every later reset piled up underneath it.
//
// It prints the notice before deleting it, which the pane does not have
// to: the pane shows the values and the button together, and a person
// clicking it has them on screen. A terminal cannot do that, so the last
// thing this command can do for the values is put one more copy in the
// scrollback of the person who asked for them to be forgotten. They are
// in this deployment's journal either way -- that is what makes a
// dismissal cost a scroll rather than a value.
//
// Nothing to clear is not an error, as it is not on the pane's route:
// two operators reading the same notice, or one who has already clicked
// Dismiss in a browser, should get "there is nothing here" and an exit
// status of zero rather than a failure over work somebody else did.
func stateResetsClear(ctx context.Context, dataDir string, out io.Writer) error {
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		return err
	}
	kept, ok := repo.UndeliveredResets(ctx)
	if !ok {
		fmt.Fprintf(out, "nothing to clear: this host is keeping no settings-reset notice\n")
		return nil
	}
	writeUndeliveredResets(kept, out)
	if err := repo.ClearUndeliveredResets(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "cleared:    %d %s gone from this host, on the State pane as well as here.\n",
		len(kept.Notices), plural(len(kept.Notices), "notice is", "notices are"))
	fmt.Fprintf(out, "            The values above stay in this deployment's journal, and a later\n")
	fmt.Fprintf(out, "            reset grain cannot report anywhere puts a fresh notice up.\n")
	return nil
}

// plural picks the word for a count, so the lines above read as English
// for one notice as well as for several. staterepo has one of its own
// for the journal line it writes; this is cmd/grain's, and neither
// package exports one for the other.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// reportSecretsKey says where the private key is, whether it is there,
// and -- every time, not only when something is wrong -- that it is the
// operator's to back up.
//
// It is reported here because this is the command that describes what
// survives a rebuilt host, and the key is the one part of that which
// nothing else can stand in for: the rest of this output can be cloned
// back from a remote, and the encrypted file beside this key travels
// nowhere at all (secretsConfig -- it is deliberately out of the
// repository a sandbox may clone). A host restored onto a fresh data
// directory therefore needs both halves handed back, and a key nobody
// copied anywhere is a key one disk away from taking every secret with
// it. pkg/secrets refuses a secrets file whose key is missing rather
// than papering over it, so the moment to notice is now, while the key
// still exists to be copied somewhere.
//
// Read straight off disk rather than through secrets.Open, which would
// *mint* a key on a data directory that has none -- a reporting command
// must not create the thing it reports on.
func reportSecretsKey(dataDir string, out io.Writer) {
	path := daemon.SecretsConfig(dataDir).KeyFile
	key, err := secrets.ReadKeyFile(path)
	switch {
	case errors.Is(err, secrets.ErrNoKey):
		fmt.Fprintf(out, "secrets key: %s (none yet -- grain mints one the first time it opens the store)\n", path)
		return
	case err != nil:
		fmt.Fprintf(out, "secrets key: %s (unreadable: %v)\n", path, err)
		return
	}
	fmt.Fprintf(out, "secrets key: %s\n", path)
	fmt.Fprintf(out, "            public key %s\n", key.Public())
	fmt.Fprintf(out, "            back this file up. It is the one thing here a redeploy cannot\n")
	fmt.Fprintf(out, "            rebuild: a host that mints a fresh key cannot read a secrets\n")
	fmt.Fprintf(out, "            file restored beside it. Seed it back with GRAIN_SECRETS_KEY\n")
	fmt.Fprintf(out, "            (scripts/setup.sh) on a deploy, or install one you are holding\n")
	fmt.Fprintf(out, "            with `grain state key import`.\n")
}

func stateSync(ctx context.Context, dataDir string) error {
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		return err
	}
	// The store the daemon on this data dir runs on, which is not SQLite
	// when it was started on Postgres: syncing a SQLite store opened here
	// for the first time would commit an empty deployment over the
	// repository's real one.
	driver, dsn, err := daemon.ResolveDatabase(dataDir)
	if err != nil {
		return err
	}
	if (driver == "" || driver == "sqlite") && dsn == "" && !daemon.HasSQLiteStore(dataDir) {
		return fmt.Errorf("%s has no task store yet (no store/grain.db, and no %s naming another database); "+
			"if its daemon runs on Postgres, set $GRAIN_DB_DRIVER=postgres and $GRAIN_DB_DSN, "+
			"or start the daemon on it once so it records its database", dataDir, postgres.DatabaseConfigFile)
	}
	_, db, err := daemon.OpenStore(driver, dsn, dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		return err
	}
	if settings.Remote != "" {
		if _, err := staterepo.SyncExternal(ctx, repo, db, true); err != nil {
			return err
		}
		head, _ := repo.Head(ctx)
		fmt.Printf("applied %s from %s\n", head, settings.Remote)
		return nil
	}
	// SyncAll, not Sync: a human typing this is asking for everything the
	// repository carries to be written out now. The two are the same call
	// today -- there is no second, slower clock left for an explicit sync
	// to overrule -- and this stays the one that says "all of it".
	changed, err := staterepo.SyncAll(ctx, repo, db, model.SchemaVersion)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Println("already up to date, nothing to commit")
		return nil
	}
	head, _ := repo.Head(ctx)
	fmt.Printf("committed %s\n", head)
	return nil
}

// stateReformat is the operator's half of a schema change: it records
// that this deployment's schema moved, so that the next start replaces
// the repository's dump instead of refusing to run on it.
//
// It writes a file and stops there, deliberately. The reformat itself is
// an export of the database this deployment runs on, and the process
// that has that database open is the daemon -- so this says what is to
// happen and the next start is what does it (reformatStateRepo in
// staterepo.go, and pkg/staterepo/reformat.go for what it costs). That
// also means this command needs neither the store, which on a deployment
// in this state is often one this build cannot open either, nor the
// network.
//
// scripts/setup.sh writes the same file itself on a schema bump, so a
// host updated the ordinary way never needs this. What needs it is every
// other way a build's schema can change under a deployment -- the UI's
// in-place Upgrade button, a binary swapped by hand -- where nothing has
// moved a store aside and nobody has said anything to the next start.
func stateReformat(dataDir string) error {
	dir := daemon.StateRepoDir(dataDir)
	// A repository already at this build's schema is not one to authorise
	// anything against. The stamp is read once and cleared, but a stamp
	// written where nothing needed it would sit in the data directory
	// until some later schema change found it and took it for consent.
	if staterepo.HasDump(dir) {
		if found, err := staterepo.ReadSchemaVersion(dir); err == nil && found == model.SchemaVersion {
			fmt.Printf("%s already holds a dump at schema %d, which is what this build knows -- "+
				"nothing to reformat, and nothing written\n", dir, model.SchemaVersion)
			return nil
		}
	}
	stamp := daemon.StateRepoReformatStamp(dataDir)
	note := fmt.Sprintf("Written by `grain state reformat` at %s.\n\n"+
		"It says that this deployment's schema changed to %d, and that the dump in\n"+
		"%s -- written before that change -- is to be replaced by this\n"+
		"deployment's own database rather than refused. grain reads this file at its\n"+
		"next start, does that once, and removes it.\n\n"+
		"Delete it to call the whole thing off.\n",
		time.Now().UTC().Format(time.RFC3339), model.SchemaVersion, dir)
	if err := os.WriteFile(stamp, []byte(note), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", stamp, err)
	}
	fmt.Printf("wrote %s\n\n", stamp)
	fmt.Printf("The next start replaces the dump in %s with this deployment's\n"+
		"database, exported at schema %d, in one commit that says so.\n"+
		"The rows it holds now stay in the history and stop being the tip: a restore\n"+
		"from that point on gives back the deployment as it is now.\n\n"+
		"If the store is at the old schema too, this is not enough on its own -- grain\n"+
		"cannot open that store any more than it can read that dump. Move %s\n"+
		"aside as well (or re-run scripts/setup.sh, which does both) before starting.\n",
		dir, model.SchemaVersion, filepath.Join(dataDir, "store"))
	return nil
}

// stateCheck loads a directory of state files into a database it throws
// away, and says what broke.
//
// The point is where it runs: not here, on the deployment, but in the
// state repository's own CI, against a pull request nobody has merged
// yet. staterepo.Import is already the validator -- one transaction,
// foreign keys deferred, rolled back whole on any inconsistency -- and
// until this command existed the only thing that ever ran it was a
// daemon starting up. A dump that was malformed, or a row missing a NOT
// NULL column, therefore failed at import time on the next start: after
// the merge, in the deployment, to whoever was on call rather than to
// whoever wrote the diff.
//
// It needs no -data-dir and reads nothing but the directory named, so a
// CI runner needs this binary and a checkout and nothing else:
//
//	grain state check .
func stateCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grain state check", flag.ContinueOnError)
	// -migrate is what an upgrade runs, inside the image it is about to
	// cut over to, against the dump the running build has just exported
	// (pkg/upgrade's schema step, and pkg/cli/schemaupgrade.go for the
	// whole sequence). Without it a dump at another schema is refused
	// here, which is right for the CI step this command was written for:
	// a pull request against a repository at a schema this build does not
	// share is not one it can vouch for. With it, the question asked is
	// the other one -- would the migration land these rows in this
	// build's schema -- and it is asked while the deployment is still
	// running the build that wrote them, which is the only moment at
	// which the answer is still worth having.
	migrate := fs.Bool("migrate", false,
		"rehearse a migration: accept a dump written at another schema and report what would arrive")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: grain state check [-migrate] DIR\n\n"+
			"DIR is the root of a state repository -- the directory holding tables/ and\n"+
			"schema-version, which for a CI step run at the top of the checkout is \".\".\n\n"+
			"-migrate accepts a dump stamped with a different schema and imports it the way\n"+
			"an upgrade would, so an upgrade can find out whether this build can take a\n"+
			"deployment's rows before it takes over from the build that wrote them.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	switch fs.NArg() {
	case 0:
	case 1:
		dir = fs.Arg(0)
	default:
		fs.Usage()
		return errors.New("check takes one directory")
	}

	return statecheck.Run(ctx, os.Stdout, dir, statecheck.Options{Migrate: *migrate})
}

// stateKey is the operator's view of the secrets key: the public half,
// which is safe to print, and where the private half is read from, which
// is what they have to back up. It never prints the private key --
// reading it is `cat` on a file they already own, and a command that
// prints one invites it into a terminal's scrollback.
func stateKey(dataDir string, args []string) error {
	store := daemon.OpenSecrets(dataDir)
	if len(args) == 0 {
		return errors.New("usage: grain state key show|path|import")
	}
	switch args[0] {
	case "show", "path":
		if err := noArgsFor("grain state key "+args[0], args[1:]); err != nil {
			return err
		}
	}
	switch args[0] {
	case "show":
		pub, err := store.PublicKey()
		if err != nil {
			return err
		}
		fmt.Println(pub)
		return nil
	case "path":
		fmt.Println(store.KeyFile())
		return nil
	case "import":
		return stateKeyImport(store, args[1:])
	default:
		return fmt.Errorf("unknown key command %q", args[0])
	}
}

// stateKeyImport installs a key the operator already holds, which is
// what makes "put this deployment on a new machine" a restore rather
// than a fresh install with an unreadable file in it.
//
// The key is read from a file or from stdin and never from a flag: a
// private key on a command line is readable by anyone else on the host
// through `ps` and lands in the shell's history besides. That is the same
// reason `grain secrets set` takes no value on its own command line.
func stateKeyImport(store *secrets.Store, args []string) error {
	fs := flag.NewFlagSet("grain state key import", flag.ContinueOnError)
	keyFile := fs.String("key-file", "", "file holding the private key; defaults to reading stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	var data []byte
	var err error
	if *keyFile != "" {
		data, err = os.ReadFile(*keyFile)
	} else {
		data, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}
	key, err := secrets.ParseKey(string(data))
	if err != nil {
		return err
	}
	if err := store.ImportKey(key); err != nil {
		return err
	}
	fmt.Printf("installed %s at %s\n", key.Public(), store.KeyFile())
	return nil
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// stateRestoreSnapshot restores the settings and memory tables into the running
// or offline database from a snapshot directory.
func stateRestoreSnapshot(ctx context.Context, dataDir string, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("grain state restore-snapshot", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: grain state restore-snapshot DIR")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("a snapshot directory is required")
	}
	snapshotDir := fs.Arg(0)
	tablesDir := filepath.Join(snapshotDir, staterepo.TablesDir)
	if _, err := os.Stat(tablesDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot directory %s has no %s subdirectory", snapshotDir, staterepo.TablesDir)
		}
		return err
	}

	db, err := daemon.OpenRawDB(dataDir)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	tables := append(append([]string{}, staterepo.SettingsTables...), "memory", "memory_document", "release_note")
	if err := staterepo.ImportTables(ctx, db, snapshotDir, tables); err != nil {
		return fmt.Errorf("restoring snapshot from %s: %w", snapshotDir, err)
	}

	var headInfo string
	if manifestData, err := os.ReadFile(filepath.Join(snapshotDir, staterepo.SnapshotManifestFile)); err == nil {
		var manifest staterepo.SnapshotManifest
		if err := json.Unmarshal(manifestData, &manifest); err == nil && manifest.Head != "" {
			headInfo = fmt.Sprintf(" (taken before applying %s)", manifest.Head)
		}
	}

	fmt.Fprintf(out, "restored settings and memory tables from %s%s\n", snapshotDir, headInfo)
	return nil
}
