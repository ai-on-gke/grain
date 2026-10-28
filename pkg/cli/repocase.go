package cli

// repocase.go is `grain repo-case`, the one-shot reconciliation behind
// pkg/model.Store.PlanRepoCase and ApplyRepoCase (grain/task-34).
//
// grain/task-32 made every repo name *entering* the store take this
// deployment's own spelling and deliberately left what was already
// stored alone, so a deployment that ran before it can hold rows keyed
// under a capitalisation nothing else uses: a task that shows up as a
// repo of its own on the repos page, a repo_config row no task ever
// reads, a qualification plan for a repo that never qualifies anything.
// This is how an operator finds out whether their deployment has any,
// and then how they fix them.
//
// It is a mode of its own rather than `grain repo case`, alongside
// `grain state` and `grain secrets` and for the same reason: those talk
// to the daemon's API over -server, and there is no endpoint for this
// one. Rewriting primary keys wholesale is not something to expose on an
// HTTP surface a browser reaches -- it is a thing an operator does once,
// from a shell on the host, having read what it is about to do.
//
// Reporting is the default and -apply is the flag, not the other way
// around: the evidence this acts on (the target repo allowlist) is
// edited from a browser by whoever runs the deployment, and a rewrite
// that ran unasked would be grain deciding that the newest thing typed
// into that box is what every historical row meant.
//
// The state repository is the other half of -apply, and it is not a
// courtesy. Most of what this rewrites is settings -- repo_config, the
// qualification plan, templates -- and the repository is authoritative
// for those: a start whose repository has moved on imports them
// (staterepo.Load), so a reconciliation is not durable until it has been
// exported, and one that has not been is one the next start takes apart
// down the middle. So -apply refuses to write while the repository holds
// a commit this host has not loaded (repoCaseImportPending), and exports
// what it wrote before it returns (exportRepoCase). Neither is
// staterepo's to fix: the repository is authoritative for settings
// deliberately, and this is the one command that writes them from
// outside it.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const repoCaseUsage = `usage: grain repo-case [-data-dir DIR] [-default-target owner/name] [-apply]

Reports every row in this deployment's store whose repository is spelled in a
case the deployment itself spells another way, and with -apply rewrites those
rows to the deployment's own spelling. GitHub resolves owner/name
case-insensitively; grain's own keys do not, so ` + "`ACME/Widgets`" + ` and
` + "`acme/widgets`" + ` are one repository to clone and two to everything here.

The deployment's spelling is its target repo allowlist first and its default
target second -- the same order ui.parseRepo uses for a repo being stored
today. A repo neither of those names is left exactly as it is: grain has no
opinion about its capitalisation to impose.

-data-dir must name the same root a colocated ` + "`grain daemon`" + ` was started
with; it defaults to $GRAIN_DATA_DIR when that is set, which a host installed
by scripts/setup.sh exports already. This edits the database on disk rather
than talking to a running daemon: stop the daemon before -apply, so no
reconcile cycle is mid-decision about a task whose target is being rewritten.

A deployment with a state repository has one more thing to be careful of, and
-apply takes care of both halves of it itself. Half of what this rewrites --
repo_config, the qualification plan, templates -- lives in the settings
tables, and the repository is authoritative for those: a start whose
repository has moved on imports them, so a rewrite that has not been exported
is a rewrite the next start undoes in those tables and no others. So -apply
refuses to write while the repository holds a commit this host has not loaded
(start the daemon once to take it up, stop it, and run this again), and
exports what it wrote as a commit of its own before it returns. What is left
for an operator to watch is the other order: a settings pull request opened
before the reconciliation and merged after it holds the old spellings in its
files, and merging it puts them back.

Flags:
  -data-dir DIR              root directory a colocated ` + "`grain daemon`" + ` was started with
  -default-target owner/name the daemon's own -default-target-repo, if it runs with
                             one: it is a startup flag rather than stored settings,
                             so this command cannot read it for itself
  -apply                     make the changes rather than only reporting them
`

func repoCaseCmd(args []string) {
	if err := runRepoCase(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain repo-case: "+err.Error())
		os.Exit(1)
	}
}

func runRepoCase(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain repo-case", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, repoCaseUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root directory a colocated `grain daemon` was started with (required; $"+dataDirEnvVar+" supplies the default)")
	defaultTarget := fs.String("default-target", "", "the daemon's own -default-target-repo, if it runs with one")
	apply := fs.Bool("apply", false, "rewrite the rows rather than only reporting them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}

	ctx := context.Background()
	store, db, err := daemon.OpenStore("sqlite", "", *dataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	canonical, err := canonicalRepos(ctx, store, *defaultTarget)
	if err != nil {
		return err
	}
	if len(canonical) == 0 {
		return errors.New("this deployment names no repo at all -- neither a target repo allowlist " +
			"nor a -default-target, so there is no spelling to reconcile anything to")
	}
	return reportRepoCase(ctx, *dataDir, store, db, canonical, *apply, out)
}

// canonicalRepos is the deployment's own spellings, in
// ui.(*Client).canonicalRepo's order: the stored target repo allowlist,
// then the default target.
//
// The default target has to be passed in. It is a `grain daemon` startup
// flag (-default-target-repo) rather than a row in grain_config, so a
// command reading only the database cannot see it -- and a deployment
// that runs with one and forgets to say so here simply gets a smaller
// canonical set, which under-reports rather than rewriting anything
// wrongly.
//
// An allowlist entry that does not parse is skipped rather than
// rejected, exactly as canonicalRepo and targetAllowed already skip it:
// it names no repo grain could act on either way.
func canonicalRepos(ctx context.Context, store *model.Store, defaultTarget string) ([]model.RepoRef, error) {
	targets, err := store.TargetRepos(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.RepoRef
	for _, entry := range targets {
		repo, err := model.ParseRepo(entry)
		if err != nil {
			continue
		}
		out = append(out, repo)
	}
	if defaultTarget != "" {
		repo, err := model.ParseRepo(defaultTarget)
		if err != nil {
			return nil, fmt.Errorf("-default-target: %w", err)
		}
		out = append(out, repo)
	}
	return out, nil
}

func reportRepoCase(ctx context.Context, dataDir string, store *model.Store, db *sql.DB, canonical []model.RepoRef, apply bool, out io.Writer) error {
	plan, err := store.PlanRepoCase(ctx, canonical)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "this deployment spells %d repo(s):\n", len(canonical))
	for _, repo := range canonical {
		fmt.Fprintf(out, "  %s\n", repo)
	}
	fmt.Fprintln(out)

	if plan.Empty() {
		fmt.Fprintln(out, "every repo in this store is already spelled that way: nothing to do.")
		return nil
	}

	if len(plan.Changes) > 0 {
		fmt.Fprintf(out, "%d row(s) are stored under another spelling:\n", plan.Rows())
		for _, c := range plan.Changes {
			fmt.Fprintf(out, "  %s\n", c)
		}
		fmt.Fprintln(out)
	}
	if len(plan.Conflicts) > 0 {
		fmt.Fprintf(out, "%d row(s) cannot be merged without deciding something grain has no basis to decide:\n",
			len(plan.Conflicts))
		for _, c := range plan.Conflicts {
			fmt.Fprintf(out, "  %s\n", c)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "nothing has been written. Resolve those by hand -- in the UI, under whichever")
		fmt.Fprintln(out, "spelling is the one you meant -- and run this again.")
		return errors.New("this store holds rows in two spellings that cannot be merged mechanically")
	}
	// The state repository, opened before anything is written, because
	// half of what this rewrites belongs to it: what a start imports is
	// the settings tables, and this plan is mostly settings tables.
	repo, err := repoCaseStateRepo(ctx, dataDir, apply, out)
	if err != nil {
		return err
	}
	if !apply {
		pending := false
		if repo != nil {
			pending = reportRepoCasePending(ctx, repo, out)
		}
		fmt.Fprintln(out, "nothing has been written. Re-run with -apply to make these changes,")
		fmt.Fprintln(out, "with the daemon stopped.")
		// Not said to an operator who has just been told -apply will
		// refuse: what that run does after it writes is not the thing in
		// front of them.
		if repo != nil && !pending {
			// Said here because this is where an operator decides to run it:
			// -apply writes to the state repository as well as to the
			// database, which is a thing to know before it happens rather
			// than after.
			fmt.Fprintln(out, "That also exports the result to this deployment's state repository, as a commit")
			fmt.Fprintln(out, "of its own: most of what this rewrites is settings, and the repository is")
			fmt.Fprintln(out, "authoritative for those, so a rewrite it has not been told about is one the")
			fmt.Fprintln(out, "next start puts back.")
		}
		return nil
	}
	// The refusal this command exists to make instead of a rewrite that
	// is undone twenty minutes later: see repoCaseImportPending.
	if repo != nil {
		if err := repoCaseImportPending(ctx, repo, out); err != nil {
			return err
		}
	}

	applied, err := store.ApplyRepoCase(ctx, canonical)
	if err != nil {
		// A conflict here and not above means the store changed underneath
		// this command -- the daemon it was meant to be run without.
		if errors.Is(err, model.ErrRepoCaseConflict) {
			for _, c := range applied.Conflicts {
				fmt.Fprintf(out, "  %s\n", c)
			}
			return errors.New("this store holds rows in two spellings that cannot be merged mechanically")
		}
		return err
	}
	fmt.Fprintf(out, "rewrote %d row(s).\n", applied.Rows())
	if repo == nil {
		return nil
	}
	return exportRepoCase(ctx, repo, db, applied.Rows(), out)
}

// repoCaseStateRepo is this deployment's state repository, or nil when it
// has none for this command to worry about.
//
// "None" is a deployment that has neither a remote configured nor a dump
// on disk: a fresh install nobody has pointed anywhere, and the store
// this command edits is the only copy of anything. Asked in that order
// and without opening anything, because opening is not free -- a
// configured remote with no working tree is cloned by the call below --
// and a command that reports by default must not go to the network to
// find out that there is nothing to say.
//
// A repository that will not open is fatal to -apply and not to a report.
// The question an -apply has to answer is "will a start undo this", and
// an unanswered question is not a no: the write is refused, exactly as
// the git proxy refuses a repository whose history it cannot read
// (forbiddenRepos). A report writes nothing, so it says what happened and
// goes on reporting.
func repoCaseStateRepo(ctx context.Context, dataDir string, apply bool, out io.Writer) (*staterepo.Repo, error) {
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		return nil, err
	}
	if settings.Remote == "" && !staterepo.HasDump(daemon.StateRepoDir(dataDir)) {
		return nil, nil
	}
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		if apply {
			return nil, fmt.Errorf("opening this deployment's state repository, which is authoritative "+
				"for the settings this would rewrite: %w", err)
		}
		fmt.Fprintf(out, "this deployment's state repository could not be opened (%v), so nothing below\n"+
			"knows whether a start would import settings over these changes.\n\n", err)
		return nil, nil
	}
	return repo, nil
}

// repoCaseImportPending is the refusal, and the reason -apply asks the
// state repository anything at all.
//
// A start whose repository has moved since this host last loaded it
// imports the settings tables out of the repository's dump
// (staterepo.Load), and that dump is whatever the last export wrote --
// which, run after a reconciliation that has not been exported, is the
// old spelling of every settings table. The rewrite then comes apart down
// the middle: task, branch, release and schedule are not settings tables
// and stay rewritten, while repo_config, the qualification plan and the
// templates go back. Worse than a revert, because a template that came
// back bound to the old spelling goes on filing tasks under it.
//
// So the write is refused while anything is waiting, rather than made and
// then lost. The way out is one line long and does not need this command:
// start the daemon, let it take the change up the way it takes any merge
// up, stop it, and run this again -- at which point the repository and
// this host agree and there is nothing left to import.
func repoCaseImportPending(ctx context.Context, repo *staterepo.Repo, out io.Writer) error {
	pending, err := repo.PendingImport(ctx)
	if err != nil {
		return fmt.Errorf("asking whether this deployment's state repository holds a change it has "+
			"not loaded: %w", err)
	}
	if !pending.Pending {
		// An unreachable remote is not a refusal -- nothing is known, and a
		// deployment whose network is down is still one an operator may
		// reconcile -- but it is the one case where the check below has not
		// actually cleared anything, so it says so.
		if pending.RemoteUnknown {
			fmt.Fprintln(out, "this deployment's state repository could not be reached, so whether a settings")
			fmt.Fprintln(out, "change is waiting on it is not known. What is on this host holds nothing back.")
			fmt.Fprintln(out)
		}
		return nil
	}
	where := "its working tree"
	if pending.OnRemote {
		where = "its remote"
	}
	fmt.Fprintf(out, "this deployment's state repository holds a commit this host has not loaded,\non %s:\n", where)
	if pending.Commit != "" {
		fmt.Fprintf(out, "  %s\n", pending.Commit)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The repository is authoritative for the settings tables -- repo_config, the")
	fmt.Fprintln(out, "qualification plan, templates -- so the next start would import that commit's")
	fmt.Fprintln(out, "version of them over these changes, leaving half of this rewrite in place and")
	fmt.Fprintln(out, "putting the other half back. Nothing has been written.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Start the daemon once so it takes that commit up, stop it again, and run this")
	fmt.Fprintln(out, "with -apply.")
	return errors.New("this deployment's state repository holds a change this host has not loaded")
}

// reportRepoCasePending is the same question asked by a run that is only
// reporting: worth saying, because it is what -apply will refuse over,
// and an operator who learns it now does not have to learn it from a
// refusal.
//
// It reports whether there is something waiting, which is the one thing
// the closing lines after it need to know.
//
// Its failures are printed rather than returned for the same reason the
// report path tolerates a repository that will not open: a report changes
// nothing, so nothing here can go wrong in a way that costs anything.
func reportRepoCasePending(ctx context.Context, repo *staterepo.Repo, out io.Writer) bool {
	pending, err := repo.PendingImport(ctx)
	if err != nil {
		fmt.Fprintf(out, "whether this deployment's state repository holds a change this host has not\n"+
			"loaded could not be established (%v).\n\n", err)
		return false
	}
	if !pending.Pending {
		return false
	}
	fmt.Fprintln(out, "this deployment's state repository holds a commit this host has not loaded, and")
	fmt.Fprintln(out, "-apply will refuse over it: a start imports the settings tables out of the")
	fmt.Fprintln(out, "repository, which would put back the half of this rewrite that lives in them.")
	fmt.Fprintln(out, "Start the daemon once so it takes that commit up, and stop it again first.")
	fmt.Fprintln(out)
	return true
}

// exportRepoCase is the other half of the same problem: a reconciliation
// is not durable until the repository holds it.
//
// -apply does the export itself rather than telling an operator to run
// `grain state sync` next, because the window between the two is the
// whole of the hazard -- a settings change merged in it is imported at
// the next start over rows this command has just rewritten -- and because
// the process that has just rewritten every settings table is in a better
// position to write them out than the operator is to remember to.
//
// A failure here is reported and returned, and it does not undo anything:
// the rewrite is in the database, which is what the deployment runs on,
// and the sentence below is what says the durable half has not happened
// yet. ErrRemoteAhead is the one worth naming, because it is this
// command's own hazard arriving a minute late -- somebody merged
// something between the check above and this export -- and the way out of
// it is the same one: let the daemon take that change up, then run this
// again.
func exportRepoCase(ctx context.Context, repo *staterepo.Repo, db *sql.DB, rows int, out io.Writer) error {
	changed, err := staterepo.SyncNoted(ctx, repo, db, model.SchemaVersion, repoCaseCommitMessage(rows))
	if err != nil {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "the rewrite is in the database, and exporting it to the state repository failed:\n  %v\n", err)
		fmt.Fprintln(out)
		if errors.Is(err, staterepo.ErrRemoteAhead) {
			fmt.Fprintln(out, "Something was merged into the repository while this ran. Start the daemon so it")
			fmt.Fprintln(out, "takes that change up -- which will put back the settings tables this rewrote --")
			fmt.Fprintln(out, "stop it again, and run this with -apply once more.")
			return err
		}
		fmt.Fprintln(out, "Until it is exported, the next start imports the repository's own settings tables")
		fmt.Fprintln(out, "back over the ones this rewrote. Fix what is above and run `grain state sync`, or")
		fmt.Fprintln(out, "run this with -apply again, before starting the daemon.")
		return err
	}
	if !changed {
		// Nothing to commit after a rewrite of hundreds of rows means the
		// repository already held them: a re-run, or a rewrite of rows no
		// dump carries. Either way it is durable, which is what this is
		// about.
		fmt.Fprintln(out, "the state repository already held this: nothing to export.")
		return nil
	}
	head, _ := repo.Head(ctx)
	fmt.Fprintf(out, "exported to the state repository as %s.\n", head)
	// The one order neither the refusal nor this export can do anything
	// about, said where somebody is looking: a pull request against the
	// settings that was opened before this ran holds the old spellings in
	// its own files, and merging it writes them back over these rows
	// exactly as any merged settings change is written -- correctly, as
	// far as anything here can tell. It is worth a sentence rather than a
	// mechanism, because the mechanism would be grain second-guessing a
	// merge somebody made on purpose.
	fmt.Fprintln(out)
	fmt.Fprintln(out, "One thing is left to watch: a settings pull request opened before this ran holds")
	fmt.Fprintln(out, "the old spellings in its files, and merging it now puts them back. Rebase or")
	fmt.Fprintln(out, "re-file any that are open against the commit above.")
	return nil
}

// repoCaseCommitMessage is what that commit says for itself: the subject
// a reviewer meets a diff of several hundred rewritten rows under, and
// the sentence that keeps them from reading it as a deployment that went
// mad overnight.
func repoCaseCommitMessage(rows int) string {
	return fmt.Sprintf("Reconcile %d row(s) onto this deployment's own spelling of its repos\n\n"+
		"`grain repo-case -apply` was run on this host with the daemon stopped:\n"+
		"every row keyed on a repository this deployment spells another way has\n"+
		"been rewritten to the deployment's own spelling, and rows that met an\n"+
		"existing row under it were merged into it. GitHub resolves owner/name\n"+
		"case-insensitively and grain's own keys do not, so those rows were one\n"+
		"repository to clone and two to everything here.\n\n"+
		"The diff is one line per row and larger than any half minute of this\n"+
		"deployment's activity would be. Nothing here was decided by grain: the\n"+
		"spelling is this deployment's target repo allowlist, and a repository it\n"+
		"names no spelling for was left exactly as it was.", rows)
}
