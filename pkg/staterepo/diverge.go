package staterepo

// Divergence: the one shape of trouble this repository can get into that
// nothing else here can talk its way out of.
//
// How a deployment arrives at it is ordinary enough. grain commits its
// export and then pushes it; if that push fails -- an installation token
// that expired between the commit and the network call, a remote that is
// briefly unreachable -- the commit stays on this host. If a pull request
// against the state repository is merged before the next push succeeds,
// the remote now holds a commit on the other side of the same parent, and
// Pull is fast-forward only by design: it refuses rather than resolving a
// conflict in a database dump by guesswork.
//
// That refusal is right, and until now nothing cleared it. Every tick
// logged the same divergence, no merged change was ever applied, and no
// export ever reached the remote again until somebody went to the host
// and fixed the working tree by hand -- which, now that the daemon pulls
// on its own timer rather than only at startup, is a state a deployment
// stays in for as long as it runs.
//
// The way out is narrow on purpose. There is exactly one case where grain
// can resolve this itself without deciding anything on a human's behalf:
// every local commit the remote has not got is grain's own export. Those
// commits carry only what the database already holds, so throwing them
// away loses nothing by construction -- the export that runs immediately
// afterwards writes the same rows straight back out, on top of whatever
// was merged. Anything else -- a commit somebody made by hand in the
// working tree, a remote whose history was rewritten under a commit we
// had already pulled -- stays a loud refusal, because that is a human's
// to resolve and no amount of resetting makes it not be.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrDiverged marks a local branch and a remote branch that have both
// moved past their common parent. Pull wraps it around its refusal so a
// caller can tell that case apart from every other reason a fetch or a
// merge fails -- an unreachable remote, a credential that expired -- and
// offer it to RecoverDiverged, which is the only thing that clears it.
var ErrDiverged = errors.New("the state repository and its remote have diverged")

// exportedPaths is what one of grain's own export commits is allowed to
// have touched, and it is the second half of "is this commit ours" --
// the author being the first.
//
// It is the list of everything the export path writes: the dump itself
// (Export), the schema stamp (WriteSchemaVersion), the README it rewrites
// on every sync and the .gitignore Open ensures (bind.go). SecretsFile is
// on it not because anything writes one -- nothing has since the file
// moved out to <data-dir>/secrets -- but because the commit that *removes*
// the one an older build left behind is grain's own too, and a deployment
// that diverged across that upgrade would otherwise be stuck forever on a
// commit whose only content is a deletion it will happily make again.
//
// WorkflowFile is on it for the same reason and with the same argument
// as the dump: the commit that installs the CI step (installWorkflow,
// format.go) is grain's own, its content is generated, and a sync that
// finds the file gone writes it again -- so a workflow commit that was
// committed but never pushed is one a reset destroys no information by
// throwing away. Left off, a deployment whose workflow push was overtaken
// by a merge would sit diverged until somebody went to the host.
var exportedPaths = []string{
	TablesDir + "/",
	// The three nested settings documents, which are the other half of
	// what Export writes (decl.go).
	SettingsDir + "/",
	SchemaVersionFile,
	ReadmeFile,
	IgnoreFile,
	SecretsFile,
	WorkflowFile,
}

// RecoverDiverged clears a divergence that is grain's own to clear, by
// resetting the working tree onto the remote's branch, and reports
// whether it did.
//
// It resets only when every commit this host has that the remote does not
// is one of grain's own exports (ownExport, below). That is the whole of
// the safety argument and it is worth stating plainly: an export commit
// holds a dump of the database, the database is still right here, and the
// caller's next export writes it back out over whatever arrived from the
// remote. So the reset destroys a commit and destroys no information.
// A commit that is not an export might hold something that exists nowhere
// else, so it is never reset over -- the divergence is reported instead,
// naming the commit that blocks it, and an operator resolves it.
//
// Callers are expected to Apply and export straight afterwards: this
// function only moves the working tree, and a tree sitting on the
// remote's branch with a database that has not taken it up is exactly
// the state Apply exists to resolve. The daemon does both -- see
// pkg/cli/statemanager.go.
//
// (false, nil) is "there was nothing here to recover": no remote, no such
// branch on it yet, or a history that is not actually diverged. Being
// merely ahead of the remote is that last case, and it is the ordinary
// state of a deployment between its commit and its push -- a push is what
// resolves it, not a reset that would throw the commit away.
func (r *Repo) RecoverDiverged(ctx context.Context) (bool, error) {
	if r.cfg.Remote == "" {
		return false, nil
	}
	if _, err := r.gitAuthed(ctx, "fetch", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		if r.remoteBranchMissing(ctx) {
			return false, nil
		}
		return false, fmt.Errorf("staterepo: fetching %s: %w", r.branch, err)
	}
	if empty, _ := r.isEmpty(ctx); empty {
		// No commits at all on this side: Pull resets onto the remote by
		// itself, and there is nothing here that could have diverged.
		return false, nil
	}
	ours, err := r.commitsIn(ctx, "FETCH_HEAD..HEAD")
	if err != nil {
		return false, err
	}
	theirs, err := r.commitsIn(ctx, "HEAD..FETCH_HEAD")
	if err != nil {
		return false, err
	}
	if len(ours) == 0 || len(theirs) == 0 {
		// One side contains the other: a fast-forward in whichever
		// direction, which is not this function's business.
		return false, nil
	}
	for _, commit := range ours {
		why, err := r.ownExport(ctx, commit)
		if err != nil {
			return false, err
		}
		if why != "" {
			return false, fmt.Errorf("%w: %s has %d commit(s) origin/%s has not, and %s "+
				"is not one of grain's own exports (%s) -- grain will not reset over it; "+
				"resolve it in %s by hand",
				ErrDiverged, r.branch, len(ours), r.branch, short(commit), why, r.cfg.Dir)
		}
	}
	// --hard, so an export that wrote files but never got as far as
	// committing them goes with the commits: those files are the same
	// dump, and the export that follows this writes them again.
	if _, err := r.git(ctx, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return false, fmt.Errorf("staterepo: resetting %s onto origin/%s: %w", r.branch, r.branch, err)
	}
	// The loaded-head marker is deliberately left pointing at the commit
	// that has just been reset away. It is now a commit HEAD is not on,
	// which is precisely what tells Apply and Load that the working tree
	// holds something the database has not taken up -- so the merge that
	// caused all this is imported on the very next call rather than a
	// restart later.
	//
	// What that marker must not cause is an import of the state tier. The
	// dump on the other side of this reset is *older* than the database by
	// every export that could not be pushed, so replacing task, comment,
	// branch and release from it would delete the very rows this
	// function's safety argument calls safe because the database still
	// holds them. It does not: Load imports the settings alone whenever
	// its marker disagrees with HEAD, here and after an ordinary merge
	// alike, since the database is ahead of the dump in both cases and
	// only the size of the window differs (bind.go). This used to need a
	// second marker recording what the reset landed on, so that one case
	// could import less than the other; the two answers are the same one
	// now, and the marker went with the difference.
	return true, nil
}

// ownExport reports why commit is not one of grain's own exports, or ""
// when it is one.
//
// A string rather than a bool because the answer is going into an error
// an operator reads: "grain will not reset over abc1234" is not much use
// without "because bwsalmon wrote it" or "because it changes NOTES.md".
//
// Two questions, both of which have to answer yes. Did grain write it --
// the author, which configure() sets on this repository from Config, so
// it is grain's own identity and not the host's git config. And does it
// touch only what an export writes: a commit authored by grain that also
// edits something else was made by a hand that set the author, and is not
// this function's to throw away.
//
// The author's email is matched on the part before the @ only. grain's
// own is grain@<hostname> (pkg/cli/staterepo.go), and a hostname is
// exactly the sort of thing that changes underneath a deployment -- a
// container gets a new one on every redeploy -- so a repository whose
// unpushed commits were made by the previous container must still be
// recoverable by this one.
func (r *Repo) ownExport(ctx context.Context, commit string) (string, error) {
	out, err := r.git(ctx, "show", "--quiet", "--format=%an%x1f%ae%x1f%P", "--end-of-options", commit)
	if err != nil {
		return "", err
	}
	fields := strings.Split(strings.TrimSpace(out), "\x1f")
	if len(fields) < 3 {
		return "", fmt.Errorf("staterepo: reading %s: unexpected output %q", short(commit), out)
	}
	name, email, parents := fields[0], fields[1], strings.Fields(fields[2])
	if name != r.cfg.AuthorName || localPart(email) != localPart(r.cfg.AuthorEmail) {
		return fmt.Sprintf("it was authored by %s <%s>", name, email), nil
	}
	if len(parents) > 1 {
		// grain never merges -- Pull is fast-forward only -- so a merge
		// commit in this history is somebody else's resolution of an
		// earlier divergence, whoever it says wrote it.
		return "it is a merge commit, which grain never makes", nil
	}
	files, err := r.git(ctx, "show", "--name-only", "--format=", "--end-of-options", commit)
	if err != nil {
		return "", err
	}
	for _, path := range strings.Fields(files) {
		if !isExportedPath(path) {
			return fmt.Sprintf("it changes %s, which grain's export does not write", path), nil
		}
	}
	return "", nil
}

// commitsIn lists the commits in a range, newest first.
func (r *Repo) commitsIn(ctx context.Context, spec string) ([]string, error) {
	out, err := r.git(ctx, "rev-list", "--end-of-options", spec)
	if err != nil {
		return nil, fmt.Errorf("staterepo: listing %s: %w", spec, err)
	}
	return strings.Fields(out), nil
}

func isExportedPath(path string) bool {
	for _, allowed := range exportedPaths {
		if path == allowed || (strings.HasSuffix(allowed, "/") && strings.HasPrefix(path, allowed)) {
			return true
		}
	}
	return false
}

func localPart(email string) string {
	if at := strings.Index(email, "@"); at >= 0 {
		return email[:at]
	}
	return email
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
