// Package staterepo keeps grain's database in a git repository.
//
// The store itself is still the embedded SQLite database pkg/model/sqlite
// opens -- every query in pkg/model is unchanged, and nothing above this
// package knows a repository exists. What changes is where that database
// comes from and where it goes: the repository's working tree holds the
// same rows as text (Export/Import, in dump.go), so the source of truth
// an operator backs up, and an agent reads, is a directory of diffable
// files rather than an opaque binary an agent cannot open.
//
// That is the whole point of it. grain's settings -- templates, suites,
// repo config, prompt extensions -- are the kind of thing a task is
// forever asking to change, and until now the only way to change one was
// a hand edit through the UI, because a row in a SQLite file is not
// something an agent can propose a diff to. As text in a repository they
// go through the mechanism grain already has for changing anything else:
// a branch, a pull request, a review, a merge.
//
// Concurrency is deliberately not modelled. The daemon is the only writer
// (see cmd/grain's daemon.go: the UI and the CLI reach it over REST), so
// a sync cycle is "pull, write what the database says, commit it, push
// it" with no merge to resolve. An agent's change arrives the other way,
// as a merged pull request, and Pull brings it back down: Apply imports
// the settings tables of it into a daemon that is already running, which
// is as much of a wholesale replacement as is safe to do underneath live
// runs, and Load imports the same tables at a start. The whole dump is
// imported in one case only, a clone this host has never loaded, which
// is the restore.
//
// The remote is optional, and that is not a fallback but a supported
// deployment: Config.Remote left empty gives a repository with no origin
// at all -- `git init` in the data directory, commits that go nowhere.
// A local install therefore needs no GitHub account, no credential and
// no answer from the operator, and can be pointed at a real remote later
// (SetRemote, which is what `grain state adopt` and the State pane's own
// Adopt call) without anything being reformatted.
package staterepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitrefs"
)

// DefaultBranch is the branch a state repository lives on when Config
// does not say otherwise. Named here rather than assumed at each call
// site: `git init` picks a branch name from the invoking user's own git
// configuration (init.defaultBranch), which is exactly the sort of thing
// that differs between a developer's laptop and a deployed host, and a
// repository whose branch name depends on who created it is one a push
// cannot find later.
const DefaultBranch = "main"

// Config says where the state repository lives and, optionally, which
// remote it is a clone of.
type Config struct {
	// Dir is the working tree. It need not exist yet.
	Dir string
	// DataDir is the root data directory of the deployment. If empty,
	// defaults to filepath.Dir(Dir).
	DataDir string
	// Remote is the URL of the repository this one tracks, or "" for a
	// local-only install. An https URL is what Token authenticates
	// against; an ssh URL authenticates however the host's own ssh does,
	// and Token is not consulted.
	Remote string
	// Branch is the branch state lives on; DefaultBranch if empty.
	Branch string
	// AuthorName and AuthorEmail identify grain's own commits. Both are
	// given to git explicitly rather than left to the host's git config,
	// which on a deployed machine is usually not set at all -- and an
	// unset user.email is a commit that fails, not one that is merely
	// anonymous.
	AuthorName  string
	AuthorEmail string
	// Token, if set, is called for the credential to push and fetch an
	// https remote with. It is called per operation rather than once, so
	// a token that expires (a GitHub App installation token lasts an
	// hour) is re-read rather than cached until it fails.
	Token func(ctx context.Context) (string, error)
	// CheckImage is the container image the CI workflow grain installs
	// runs `grain state check` from; DefaultCheckImage when empty. The
	// check refuses a dump stamped with any other schema, so what belongs
	// here is the build this deployment is running -- which is what
	// cmd/grain passes, from the reference stamped into its own image
	// (pkg/cli/grainimage.go), unless an operator has named one in
	// state-repo.json.
	//
	// It is not only written into a workflow that is missing: an
	// installed one that is still grain's own rendering -- this build's
	// or an earlier grain's -- is repointed here as well, so an upgraded
	// deployment's check follows it. See StaleWorkflow.
	CheckImage string
	// Build is how this deployment names the build it is running -- the
	// image reference cmd/grain has stamped into itself, and the commit
	// that image was built from (pkg/cli/grainimage.go). It is an
	// opaque string to this package: the only thing done with it is to
	// compare it against the one recorded when this working tree was last
	// exported to, so that the first export after an upgrade says in its
	// commit message which build wrote the one before it and which build
	// wrote this one. See exportedBuildFile and exportCommitMessage.
	//
	// Empty means the caller does not know which build it is -- an
	// unstamped binary, a test -- and every commit message is then the
	// plain one, exactly as it was before there was a field here.
	// Deliberately not defaulted to anything: a made-up build identity
	// would be an upgrade reported where none happened.
	Build string
	// NoWorkflow stops grain from installing that workflow at all: the
	// opt-out for a deployment whose state repository is checked
	// somewhere else, or by something other than GitHub Actions. It is a
	// switch rather than "delete the file", because deleting a file grain
	// writes when it is missing is not a decision that stays made -- see
	// installWorkflow in format.go, which also leaves a workflow somebody
	// has edited exactly as they left it.
	NoWorkflow bool
	// AnnounceResets, if set, is called when a merged settings change has
	// just been applied and something was lost to it: commit is the
	// commit that arrived, and resets is what it replaced, exactly as the
	// journal line beside it names them (resets.go).
	//
	// It is the seam between the fact and the surfaces that carry it. The
	// journal line is written here, unconditionally, because it is this
	// package's own record and costs nothing; anything that has to reach
	// somebody -- the pull request the merge came in on, which is where
	// whoever caused the reset is looking -- needs a GitHub credential
	// and a REST client, and this package has neither and should not
	// grow them to say one sentence. cmd/grain has both, so cmd/grain
	// says it (stateresets.go).
	//
	// Called with the import already committed, and its failures are its
	// own: a notice nobody could deliver must not turn a settings change
	// that is already live into an error, for the same reason
	// applySettings does not fail over a reset it could not work out.
	// Called on the applying goroutine, so a slow surface is a slow tick
	// -- worth knowing, and cheap in practice, since nothing calls this
	// on a tick that reset nothing, which is all but a handful of them.
	//
	// The error it returns is not a failure to act on: nothing here
	// retries and nothing here stops. It is the callback saying the fact
	// did not get out, and its message is the sentence a reader is shown
	// in its place -- announceResets keeps it in a marker on this host
	// (UndeliveredResets), which is what the State pane and `grain state
	// status` carry for the deployments the callback can never reach.
	// So it should read as one human sentence naming *why*, in the words
	// the callback's own journal line uses; the callback keeps that line,
	// and this package writes none of its own over the top of it.
	//
	// ErrNowhereToAnnounce, wrapped, is how a callback says the fact did
	// not get out because this deployment has nowhere to say it -- which
	// is recorded exactly the same way and is not a fault. See its own
	// doc comment.
	AnnounceResets func(ctx context.Context, commit string, resets []Reset) error
	// AnnounceSystemScheduleEdits, if set, is called when a merged
	// settings change has just edited the content of one of grain's own
	// schedules -- a change the next reconcile cycle undoes, because
	// those columns are a cache of a template that lives in code
	// (systemschedules.go). commit is the commit that arrived, and edits
	// names the schedules and what of them changed, exactly as the
	// journal line beside it names them.
	//
	// The same seam AnnounceResets is, wired in the same place
	// (pkg/cli/stateresets.go) to the same pull request, and for the
	// same reason: the person who needs telling that their merge is
	// about to be reverted is the one who merged it, and this package
	// has no credential to reach them with. Its failures are its own,
	// nothing here retries, and nothing about the import turns on it.
	//
	// Unlike AnnounceResets, what does not get out is not kept in a
	// marker on this host: the text this is about is in the pull
	// request's own diff and in the state repository's history, so
	// there is nothing here for a person to recover. See
	// announceSystemScheduleEdits.
	AnnounceSystemScheduleEdits func(ctx context.Context, commit string, edits []SystemScheduleEdit) error
	// WorkflowRuns, if set, is called on a sync to ask how the check
	// grain installed has actually been running: file is WorkflowFile,
	// the repository-relative path of it, and what comes back is that
	// workflow's most recent runs, newest first.
	//
	// It is the same seam AnnounceResets is, for the same reason -- the
	// answer lives behind a GitHub credential and a REST client, and this
	// package has neither -- and cmd/grain wires it (statechecks.go).
	// Left nil, grain never asks, which is every caller that has no
	// GitHub behind it: a test, a local-only deployment, an operator's
	// clone.
	//
	// What the answer is for is workflowhealth.go's one question: whether
	// every finished run of this check was concluded before a job of it
	// started, which is a repository that cannot run Actions at all
	// rather than a settings change that does not load. Its failures are
	// its own -- a deployment must not stop exporting because it could
	// not find out whether anything was watching -- and nothing here
	// retries.
	//
	// Called on the syncing goroutine, at most once an hour
	// (workflowHealthInterval), so a GitHub that accepts a connection and
	// then says nothing is a stalled sync: the callback owns its own
	// deadline.
	WorkflowRuns func(ctx context.Context, file string) ([]WorkflowRun, error)
	// PostCheck says whether this deployment runs the check itself, on
	// this host, against every open pull request on the state repository,
	// and posts what it finds as a check run (postcheck.go).
	//
	// It is Settings.PostCheck, and it is off unless an operator turned
	// it on, because it needs a credential most deployments do not have:
	// only a GitHub App installation may create a check run at all. It is
	// deliberately not the same switch as NoWorkflow -- the two are
	// independent, a repository can be checked by both, by either or by
	// neither, and all four states mean something
	// (docs/state-repo-check-posted-by-grain.md, section 5).
	PostCheck bool
	// OpenPullRequests, if set, lists the open pull requests on Remote,
	// each with the commit at its head. The same seam WorkflowRuns is,
	// wired in the same place (cmd/grain/statecheckpost.go) for the same
	// reason: what it answers lives behind a GitHub credential this
	// package does not have.
	//
	// Every open pull request, not only the ones grain's own tasks
	// opened: a person's settings change is checked on the same terms,
	// and is the one most likely to have been written by hand.
	OpenPullRequests func(ctx context.Context) ([]PullRequestHead, error)
	// PostCheckRun creates a check run when post.ID is zero -- answering
	// with the id GitHub gave it -- and updates that run when it is not.
	//
	// Two jobs through one seam because they are one conversation, and
	// the order of it is the whole design: the run is created
	// "in_progress" before the tree is checked, and concluded after, so
	// that a deployment which dies half way through leaves a pull request
	// wearing a check that never finishes rather than one wearing no
	// check at all. A missing check is not a blocked pull request -- it
	// is a clean one (pkg/orchestrator's healthFrom), which is the whole
	// hazard this design is built around.
	PostCheckRun func(ctx context.Context, post CheckRunPost) (int64, error)
	// CheckTree, if set, replaces running the check as a subprocess: it
	// is handed a checked-out tree and answers with what a reader of the
	// check run should be shown, or an error whose message is that.
	//
	// Only tests set it. What a deployment does instead is exec its own
	// binary (runCheckSubprocess) -- see postChecks on why an unmerged
	// pull request's tree is not something to import inside the daemon's
	// own process.
	CheckTree func(ctx context.Context, dir string) (string, error)
	// Now is this repository's clock -- the timestamp an export commit
	// carries, and what the hourly work inside a sync is measured
	// against (the workflow health check, git's own maintenance). nil
	// means time.Now. It exists for the measurements in growth_test.go
	// and cigrowth_test.go, which drive a simulated day through this
	// package in a few minutes of wall clock and would otherwise be
	// measuring their own runtime rather than the cadence they set out
	// to.
	Now func() time.Time
}

// Repo is an opened state repository. Build one with Open.
type Repo struct {
	cfg    Config
	branch string

	gitDirMu stdsync.Mutex
	gitDir   string
}

// Dir returns the working tree.
func (r *Repo) Dir() string { return r.cfg.Dir }

// DataDir returns the root data directory, or filepath.Dir(Dir) if empty.
func (r *Repo) DataDir() string {
	if r.cfg.DataDir != "" {
		return r.cfg.DataDir
	}
	return filepath.Dir(r.cfg.Dir)
}

// Branch returns the branch state lives on.
func (r *Repo) Branch() string { return r.branch }

// Remote returns the tracked remote, or "" for a local-only repository.
func (r *Repo) Remote() string { return r.cfg.Remote }

// now is the clock Config.Now names.
func (r *Repo) now() time.Time { return r.cfg.Now() }

// Open prepares the state repository, creating it if it is not there.
//
// Three cases, and only three: a working tree that is already a git
// repository is used as it is (its remote reconciled to Config.Remote,
// so changing the remote in the bootstrap does not mean re-cloning);
// a Config with a Remote and no repository yet is cloned; a Config with
// neither is `git init`ed and left with no origin.
//
// A clone of a remote that exists but is empty -- which is what "create
// a repository on GitHub and point grain at it" produces -- is not an
// error and not a special case an operator has to know about: git clone
// of an empty repository succeeds and leaves a working tree with no
// commits, which Ensure then seeds exactly as it seeds a local one.
func Open(ctx context.Context, cfg Config) (*Repo, error) {
	if cfg.Dir == "" {
		return nil, errors.New("staterepo: no directory configured")
	}
	branch := strings.TrimSpace(cfg.Branch)
	if branch == "" {
		branch = DefaultBranch
	}
	if err := ValidateBranch(branch); err != nil {
		return nil, err
	}
	if cfg.AuthorName == "" {
		cfg.AuthorName = "grain"
	}
	if cfg.AuthorEmail == "" {
		cfg.AuthorEmail = "grain@localhost"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Repo{cfg: cfg, branch: branch}

	exists, err := isRepo(cfg.Dir)
	if err != nil {
		return nil, err
	}
	// A repository in a ref format this git cannot read (reftable, made
	// by a newer host git) fails every command below with git's bare
	// "unknown repository extension"; say what it is and how to convert
	// it instead.
	if exists {
		if err := gitrefs.Check(ctx, cfg.Dir); err != nil {
			return nil, fmt.Errorf("staterepo: %w", err)
		}
	}
	if !exists {
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return nil, fmt.Errorf("staterepo: preparing %s: %w", cfg.Dir, err)
		}
	}
	// The tree holds the deployment's whole configuration, so nobody but
	// the daemon's own user has any business reading it. MkdirAll leaves
	// a directory that is already there as it found it -- one an operator
	// made by hand, an older build made 0755, or setup.sh made under a
	// loose umask -- so the mode is put right on every Open, not only the
	// one that creates it.
	if err := restrictDir(cfg.Dir); err != nil {
		return nil, err
	}
	if !exists {
		empty, err := isEmptyDir(cfg.Dir)
		if err != nil {
			return nil, err
		}
		switch {
		case cfg.Remote != "" && empty:
			if err := r.clone(ctx); err != nil {
				return nil, err
			}
		default:
			// A remote with files already in the directory inits instead
			// of cloning, because `git clone` refuses a destination that
			// is not empty ("destination path already exists and is not
			// an empty directory") -- and that directory is the ordinary
			// case on a deployed host, not a corner one: scripts/setup.sh
			// writes the encrypted secrets file into this tree before the
			// daemon has ever started (its minter-key seed), and carries
			// it across by hand when a schema bump moves the old tree
			// aside. Refusing there would leave a deployment that cannot
			// start over a file grain itself put there.
			//
			// Nothing is lost by not cloning: configure adds origin
			// below, and Load's first Pull fetches the branch and resets
			// a repository with no commits onto it -- which brings the
			// remote's own copy of every file down over whatever was
			// sitting here, exactly as a clone would have.
			if err := r.init(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := r.configure(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// isRepo reports whether dir already holds a git repository. A directory
// that exists but holds no .git is not one -- Open clones or inits into
// it, which git is happy to do as long as it is empty of anything but
// dotfiles it does not mind.
func isRepo(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil {
		return info.IsDir() || info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("staterepo: inspecting %s: %w", dir, err)
}

// restrictDir takes group and other access off dir, leaving its owner's
// bits as they are. A directory that is already private is not touched.
func restrictDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("staterepo: inspecting %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, info.Mode().Perm()&0o700); err != nil {
		return fmt.Errorf("staterepo: restricting %s to its owner: %w", dir, err)
	}
	return nil
}

// isEmptyDir reports whether dir holds nothing at all, which is the
// question `git clone` asks of a destination it is given.
func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("staterepo: inspecting %s: %w", dir, err)
	}
	return len(entries) == 0, nil
}

func (r *Repo) init(ctx context.Context) error {
	if _, err := r.git(ctx, "init", "--initial-branch="+r.branch, "--", r.cfg.Dir); err != nil {
		return err
	}
	return nil
}

func (r *Repo) clone(ctx context.Context) error {
	// Cloned into the directory itself rather than under it, and with
	// --branch left off: an empty remote has no branch to ask for, and
	// asking for one that does not exist is a clone that fails.
	_, err := r.gitAuthed(ctx, "clone", "--quiet", "--", r.cfg.Remote, r.cfg.Dir)
	if err != nil {
		return fmt.Errorf("staterepo: cloning %s: %w", r.cfg.Remote, err)
	}
	// A clone of an empty remote lands on whatever branch the remote's
	// HEAD advertises, which for a fresh GitHub repository is main but
	// need not be. Naming it ourselves keeps the local branch the one
	// Config asked for either way.
	if head, err := r.currentBranch(ctx); err == nil && head != r.branch {
		if empty, _ := r.isEmpty(ctx); empty {
			_, _ = r.git(ctx, "checkout", "-q", "-b", r.branch)
		}
	}
	return nil
}

// configure writes the identity and the remote into the repository's own
// config, so every later command inherits them and nothing has to pass
// -c on each invocation.
func (r *Repo) configure(ctx context.Context) error {
	for _, kv := range [][2]string{
		{"user.name", r.cfg.AuthorName},
		{"user.email", r.cfg.AuthorEmail},
		// grain's commits are its own; signing is the operator's business
		// and a host with commit.gpgsign set globally would otherwise
		// break every automated commit with a passphrase prompt.
		{"commit.gpgsign", "false"},
		// How many loose objects `gc --auto` (maintain) will tolerate
		// before packing them. git's own default is 6700, which is tuned
		// for a repository whose objects are source files a person wrote:
		// this one's are whole database dumps, tens of megabytes each, so
		// 6700 of them is hundreds of gigabytes of disk sitting unpacked.
		// A few dozen commits' worth is the right order here, and packing
		// that often is cheap because it is exactly what git is good at.
		{"gc.auto", "128"},
		// Synchronously, not in a background process that outlives the
		// call. The daemon runs in a container that can be stopped between
		// one sync and the next, and a detached gc killed halfway is a
		// repository nobody was watching over. Run inline it is bounded by
		// the same timeout every other git command here has.
		//
		// Both keys, because since git v2.47 the process that housekeeping
		// runs in is `git maintenance run --auto`, spawned by fetch, push
		// and commit alike, and it is maintenance.autoDetach that says
		// whether *it* forks. git falls back to gc.autoDetach for a
		// repository that does not name it -- its own source calls that
		// "somewhat weird" and keeps it only to preserve the behaviour
		// from when this was git-gc -- so the line above still holds
		// today, and naming the key that actually decides means it goes on
		// holding when that compatibility is dropped.
		{"gc.autoDetach", "false"},
		{"maintenance.autoDetach", "false"},
	} {
		if _, err := r.git(ctx, "config", "--", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return r.setRemote(ctx, r.cfg.Remote)
}

// setRemote reconciles origin with url: added, updated or removed so the
// repository tracks exactly what Config says. Removing it is what
// converting a remote install back to a local one means, and it leaves
// every commit already made in place.
func (r *Repo) setRemote(ctx context.Context, url string) error {
	current, err := r.git(ctx, "remote")
	if err != nil {
		return err
	}
	has := false
	for _, name := range strings.Fields(current) {
		if name == "origin" {
			has = true
		}
	}
	switch {
	case url == "" && has:
		_, err = r.git(ctx, "remote", "remove", "origin")
	case url != "" && has:
		_, err = r.git(ctx, "remote", "set-url", "--", "origin", url)
	case url != "":
		_, err = r.git(ctx, "remote", "add", "--", "origin", url)
	}
	if err != nil {
		return err
	}
	r.cfg.Remote = url
	return nil
}

// SetRemote points an already-opened repository at a different remote (or
// at none). This is the bootstrap's "adopt a repository" step: the
// working tree, its history and its contents are untouched, only where
// they push to changes.
func (r *Repo) SetRemote(ctx context.Context, url string) error { return r.setRemote(ctx, url) }

// ValidateBranch checks that branch is a valid git branch name. An empty or
// whitespace-only string is allowed (meaning DefaultBranch).
func ValidateBranch(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == DefaultBranch {
		return nil
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("staterepo: invalid branch name %q", branch)
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != branch {
		return fmt.Errorf("staterepo: invalid branch name %q", branch)
	}
	return nil
}

// ConfigureMirror updates an already-opened repository's remote URL, tracked
// branch, and credential callback to match the current state-repo.json
// settings without reopening the repository.
func (r *Repo) ConfigureMirror(ctx context.Context, remote, branch string, token func(context.Context) (string, error)) error {
	remote = strings.TrimSpace(remote)
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = DefaultBranch
	}
	if err := ValidateBranch(branch); err != nil {
		return err
	}
	r.cfg.Token = token
	if branch != r.branch {
		empty, err := r.isEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			if _, err := r.git(ctx, "checkout", "-q", "-b", branch); err != nil {
				return err
			}
		} else {
			if _, err := r.git(ctx, "branch", "-M", "--", branch); err != nil {
				return err
			}
		}
		r.branch = branch
		r.cfg.Branch = branch
	}
	return r.setRemote(ctx, remote)
}

// isEmpty reports whether the repository has no commits yet.
func (r *Repo) isEmpty(ctx context.Context) (bool, error) {
	_, err := r.git(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return true, nil
	}
	return false, nil
}

// currentBranch is the branch HEAD is on, including the unborn one a
// clone of an empty repository lands on.
//
// `git branch --show-current` rather than `rev-parse --abbrev-ref HEAD`,
// which is what this used to run: rev-parse resolves HEAD to a commit
// first, so on a branch with no commits yet it fails outright ("unknown
// revision"). That is exactly the case clone's caller has to answer --
// adopting an *empty* remote -- and the failure there was silent, since
// the rename to Config.Branch is skipped on any error. A remote whose
// HEAD advertised "master" (a `git init --bare` with no
// init.defaultBranch, or an older repository) therefore left every
// commit on master while every push named main, and adopting it failed
// with git's own "src refspec main does not match any" (found by hand,
// task 244).
func (r *Repo) currentBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Commit stages everything in the working tree and commits it, reporting
// whether there was anything to commit. A no-op sync is the common case
// -- the daemon exports on a timer -- so "nothing changed" is a false and
// a nil error rather than something a caller has to recognise in an
// error string.
func (r *Repo) Commit(ctx context.Context, message string) (bool, error) {
	if _, err := r.git(ctx, "add", "--all", "."); err != nil {
		return false, err
	}
	status, err := r.git(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	if _, err := r.git(ctx, "commit", "--quiet", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

// Push sends the branch to origin. A repository with no remote is not an
// error: a local-only install calls the same sync path as a remote one,
// and this is where that stops.
func (r *Repo) Push(ctx context.Context) error {
	if r.cfg.Remote == "" {
		return nil
	}
	if empty, _ := r.isEmpty(ctx); empty {
		return nil
	}
	if _, err := r.gitAuthed(ctx, "push", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		return fmt.Errorf("staterepo: pushing %s: %w", r.branch, err)
	}
	return nil
}

// ErrUnreachable marks the one failure in this package that says nothing
// at all about what the repository holds: the fetch itself did not
// complete.
//
// A network blip, an installation token that expired an hour ago, a
// repository renamed on GitHub -- git reports them differently and they
// mean the same thing here, which is that grain knows exactly what it
// knew before it asked. That is a different fact from "the repository
// holds something this build must not overwrite" (ErrSchemaTooNew and
// its neighbours in bind.go), and only the second is worth refusing to
// start over; the sentinel exists so that the difference is something a
// caller keys off rather than something it matches an error string for.
// Load, and cmd/grain's own run(), are where that decision is written
// down.
var ErrUnreachable = errors.New("the state repository's remote could not be reached")

// Pull fast-forwards the working tree to the remote's branch, reporting
// whether anything arrived.
//
// Fast-forward only, on purpose. The daemon is the only writer of these
// files, so a remote that is ahead of us means one thing -- a pull
// request was merged -- and there is nothing to merge. Divergence means
// something happened that this package's model does not describe, and
// failing loudly with the two commits named beats resolving a conflict
// in a database dump by guesswork.
//
// That refusal is marked with ErrDiverged rather than left as whatever
// git said, because it is the one failure here a caller can do something
// about: RecoverDiverged (diverge.go) clears the case where the only
// commits in the way are grain's own exports, which is what a push that
// failed before a merge landed leaves behind.
//
// A fetch that never got off the ground is marked too, with
// ErrUnreachable, and for the opposite reason: there is nothing to do
// about it and nothing to act on, so a caller that has a working tree
// already can carry on with it and ask again on the next tick.
func (r *Repo) Pull(ctx context.Context) (bool, error) {
	if r.cfg.Remote == "" {
		return false, nil
	}
	if _, err := r.gitAuthed(ctx, "fetch", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		// A remote with no such branch yet (a repository created empty and
		// never pushed to) is not a failure to pull, it is nothing to pull.
		if r.remoteBranchMissing(ctx) {
			return false, nil
		}
		// Marked, because a fetch that did not happen is the one failure
		// here a caller can carry on from: see ErrUnreachable. Everything
		// below this point is local git against a tree we did reach, and
		// keeps its own words.
		return false, fmt.Errorf("%w: fetching %s: %w", ErrUnreachable, r.branch, err)
	}
	before, _ := r.git(ctx, "rev-parse", "HEAD")
	if empty, _ := r.isEmpty(ctx); empty {
		if _, err := r.git(ctx, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := r.git(ctx, "merge", "--ff-only", "--quiet", "FETCH_HEAD"); err != nil {
		// A merge that will not fast-forward is usually divergence and is
		// not always: a working tree with local modifications refuses the
		// same way. Asked rather than assumed, so that the sentinel a
		// caller keys recovery off means what it says.
		if ahead, aerr := r.commitsIn(ctx, "FETCH_HEAD..HEAD"); aerr == nil && len(ahead) > 0 {
			return false, fmt.Errorf("%w: %s and origin/%s have both moved past their common "+
				"parent; grain will not merge a database dump by guesswork: %w",
				ErrDiverged, r.branch, r.branch, err)
		}
		return false, fmt.Errorf("staterepo: fast-forwarding %s to origin/%s: %w", r.branch, r.branch, err)
	}
	after, _ := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(before) != strings.TrimSpace(after), nil
}

// PullExternal fetches origin's branch and resets the working tree onto it,
// reporting whether HEAD moved. Used in external config repository (IaC) mode,
// where the external repository is the sole source of truth and local history
// (including a prior internal grain/config history) is replaced rather than
// merged or pushed.
func (r *Repo) PullExternal(ctx context.Context) (bool, error) {
	if r.cfg.Remote == "" {
		return false, nil
	}
	if _, err := r.gitAuthed(ctx, "fetch", "--quiet", "--end-of-options", "origin", r.branch); err != nil {
		if r.remoteBranchMissing(ctx) {
			return false, nil
		}
		return false, fmt.Errorf("%w: fetching %s: %w", ErrUnreachable, r.branch, err)
	}
	before, _ := r.git(ctx, "rev-parse", "HEAD")
	if _, err := r.git(ctx, "checkout", "-f", "-B", r.branch, "FETCH_HEAD"); err != nil {
		return false, fmt.Errorf("staterepo: checking out origin/%s: %w", r.branch, err)
	}
	if _, err := r.git(ctx, "clean", "-fd"); err != nil {
		return false, fmt.Errorf("staterepo: cleaning working tree for origin/%s: %w", r.branch, err)
	}
	after, _ := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(before) != strings.TrimSpace(after), nil
}

// remoteState is where origin's branch sits relative to this working
// tree, and it is both of the questions a sync cycle has to ask about
// the remote rather than one, because one ls-remote answers them both.
//
// ahead means origin holds commits this working tree does not -- which,
// for this repository, means exactly one thing: a pull request against
// grain's own state was merged. behind means the opposite: this host
// holds commits origin has not got, which is what an export that was
// committed and could not be pushed leaves behind. Neither is the
// ordinary tick, where the two agree.
//
// known is false when there was nothing to ask -- a local-only install
// with no remote -- and when the asking itself failed: a network blip, a
// credential that expired. Nothing at all is known then, so a caller
// carries on exactly as it would have without this check, and the push
// reports the network in its own words. That is also why an unreachable
// remote is not reported as an error here: no answer and "no, neither"
// lead to the same place, and the pull half of the cycle is where an
// operator is told the remote has gone (Apply, and ErrUnreachable
// above).
// head is origin's own commit, when there was one to read: the answer
// the same ls-remote already carried, kept so a caller that has to name
// what is waiting there (PendingImport) does not ask a second time.
type remoteState struct {
	known  bool
	ahead  bool
	behind bool
	head   string
}

// remoteState asks origin where its branch is, in one ls-remote and
// without fetching an object.
//
// The ahead half is asked before every export (see sync) because of what
// would otherwise happen. The daemon would commit its own dump on top of
// a history the remote has moved past, the push would be rejected as a
// non-fast-forward on that tick and on every tick after it, and the next
// start would find the two diverged and refuse to load at all -- a grain
// that will not come up because somebody merged the change it asked
// them to. Noticing first costs one ls-remote and turns all of that into
// a sentence an operator can act on.
//
// The behind half is what that same answer says about the other
// direction, for free, and sync uses it to decide whether to push: see
// the note there on why "did this cycle commit something" is the wrong
// question to hang a push on.
func (r *Repo) remoteState(ctx context.Context) remoteState {
	if r.cfg.Remote == "" {
		return remoteState{}
	}
	out, err := r.lsRemoteBranch(ctx)
	if err != nil {
		return remoteState{}
	}
	empty, _ := r.isEmpty(ctx)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		// No such branch on the remote yet: an empty repository, which is
		// behind us by every commit we have, and behind us by nothing at
		// all if we have none either.
		return remoteState{known: true, behind: !empty}
	}
	head := fields[0]
	// A commit this clone has never fetched cannot be one of ours, and a
	// working tree with no commits at all has nothing it could be.
	if empty {
		return remoteState{known: true, ahead: true, head: head}
	}
	if _, err := r.git(ctx, "cat-file", "-e", "--end-of-options", head+"^{commit}"); err != nil {
		return remoteState{known: true, ahead: true, head: head}
	}
	// We have it: ahead only if it is not already in our history.
	if _, err := r.git(ctx, "merge-base", "--is-ancestor", "--end-of-options", head, "HEAD"); err != nil {
		return remoteState{known: true, ahead: true, head: head}
	}
	// It is in our history, so we are level with it or ahead of it: the
	// second exactly when it is not where we are standing.
	ours, err := r.Head(ctx)
	if err != nil {
		return remoteState{known: true, head: head}
	}
	return remoteState{known: true, behind: ours != head, head: head}
}

func (r *Repo) remoteBranchMissing(ctx context.Context) bool {
	out, err := r.lsRemoteBranch(ctx)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == ""
}

// lsRemoteBranch asks origin about Config.Branch and nothing else.
//
// The full ref rather than the branch name, which is what this used to
// pass and is the whole of the fix. `git ls-remote` matches a pattern
// against the *tail* of a ref, so "main" answers with refs/heads/x/main
// as readily as with refs/heads/main -- and a state repository is
// precisely the repository other branches live in, since a pull request
// against grain's own settings is a branch pushed to this remote and
// sits there for as long as it is open. The answers are then sorted by
// ref name, so the first line is not even reliably grain's: "agent/main"
// sorts before "main".
//
// Both readers of this were wrong in their own direction on that line.
// remoteState took the first commit as origin's copy of the branch, and
// a commit off somebody's proposal is one this working tree has never
// fetched -- so the remote looked ahead, and sync refused to export for
// as long as the branch was open (ErrRemoteAhead), which is a deployment
// whose settings stop reaching its remote because somebody asked it to
// change one. remoteBranchMissing answered "the branch is there" for a
// remote that only held the other one, turning the seed of a freshly
// adopted repository into a failure to reach the remote at all
// (ErrNoLocalCopy, and a daemon that will not start).
func (r *Repo) lsRemoteBranch(ctx context.Context) (string, error) {
	return r.gitAuthed(ctx, "ls-remote", "--heads", "origin", "refs/heads/"+r.branch)
}

// Head returns the commit the working tree is on, or "" if there are no
// commits yet. The UI reports it, so an operator can see at a glance
// whether what grain is running matches what is on the remote.
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// HasSecrets reports whether this repository holds, or has ever held,
// the encrypted secrets file.
//
// "Has ever" is the whole point, and it is why this asks git rather than
// the filesystem. A sandbox that may clone a repository may read every
// object in it: a `git log -p` reaches a file deleted three commits ago
// exactly as easily as one still in the tree. So a state repository an
// earlier build wrote secrets.enc into stays a repository grain must not
// let a sandbox near, however long ago the file was removed, and the
// only remedy is a repository that never held one.
//
// A directory that is not a repository, or a repository with no commits,
// answers on the working tree alone -- there is no history to ask.
func (r *Repo) HasSecrets(ctx context.Context) (bool, error) {
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, SecretsFile)); err == nil {
		return true, nil
	}
	if empty, _ := r.isEmpty(ctx); empty {
		return false, nil
	}
	// --all rather than the current branch: a repository whose secrets
	// file only ever existed on a branch somebody else pushed is one a
	// sandbox can still fetch it from.
	out, err := r.git(ctx, "log", "--all", "--max-count=1", "--format=%H", "--", SecretsFile)
	if err != nil {
		return false, fmt.Errorf("staterepo: looking for %s in %s's history: %w", SecretsFile, r.cfg.Dir, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// loadedHeadFile is where the commit this host last loaded or wrote is
// recorded: inside the git directory, so it is neither part of the
// working tree nor something a commit, a clone or a push can carry. That
// placement is the point -- the marker answers "has this repository moved
// under *this* host", which is a question about the host and not about
// the repository, and a clone onto a new machine must arrive without one.
const loadedHeadFile = "grain-loaded-head"

// loadedHead reads the marker, reporting "" when there is none.
func (r *Repo) loadedHead(ctx context.Context) (string, error) {
	path, err := r.loadedHeadPath(ctx)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("staterepo: reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (r *Repo) setLoadedHead(ctx context.Context, commit string) error {
	path, err := r.loadedHeadPath(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(commit+"\n"), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// recordLoadedHead records wherever the working tree is now.
//
// Nowhere is not an answer it will write. Head reports "" for a
// repository with no commits and for a rev-parse that failed for any
// other reason (its own doc comment), and every caller here has just
// made a commit -- so "" means the second, and an empty marker is not a
// smaller version of the truth. It is read as "this host has never
// loaded this repository", which is the one thing Load answers with a
// whole-database Import: every task, comment and run written since the
// last export replaced by a dump that is older than they are. Better to
// fail the cycle and let the next one record it properly.
func (r *Repo) recordLoadedHead(ctx context.Context) error {
	head, err := r.Head(ctx)
	if err != nil {
		return err
	}
	if head == "" {
		return fmt.Errorf("staterepo: %s has commits but git would not say which one HEAD is on, "+
			"so grain cannot record what this host has loaded", r.cfg.Dir)
	}
	return r.setLoadedHead(ctx, head)
}

// loadedHeadPath asks git where the git directory is rather than
// assuming <dir>/.git: that is a file, not a directory, in a worktree,
// and writing into it would corrupt the repository.
func (r *Repo) loadedHeadPath(ctx context.Context) (string, error) {
	return r.gitDirFile(ctx, loadedHeadFile)
}

func (r *Repo) gitDirFile(ctx context.Context, name string) (string, error) {
	r.gitDirMu.Lock()
	defer r.gitDirMu.Unlock()
	if r.gitDir != "" {
		return filepath.Join(r.gitDir, name), nil
	}
	out, err := r.git(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	r.gitDir = strings.TrimSpace(out)
	return filepath.Join(r.gitDir, name), nil
}

// exportedBuildFile records which build of grain last wrote an export
// into this working tree -- Config.Build, verbatim.
//
// Beside loadedHead, inside the git directory, for the reason that one
// is: it is a fact about this host and the tree on
// it, not about the repository, so it must not be committed and must not
// travel to a remote. A clone that arrives without one has nothing to
// compare against and is not an upgrade: it is a host that has never
// exported here, and the message it gets is the plain one.
const exportedBuildFile = "grain-exported-build"

// exportedBuild reads that marker, reporting "" when there is none.
//
// Every failure is "" as well, and deliberately: what this decides is
// the wording of a commit message, and a marker that could not be read
// is not a reason to fail a sync -- the export, the commit and the push
// are all still exactly right, they just say less.
func (r *Repo) exportedBuild(ctx context.Context) string {
	path, err := r.gitDirFile(ctx, exportedBuildFile)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// recordExportedBuild writes Config.Build into that marker. A caller
// that does not know its own build records nothing rather than recording
// emptiness: the marker's absence already means "unknown", and blanking
// one an earlier stamped build wrote would throw away the only thing an
// upgrade could be noticed against.
func (r *Repo) recordExportedBuild(ctx context.Context) error {
	if r.cfg.Build == "" {
		return nil
	}
	path, err := r.gitDirFile(ctx, exportedBuildFile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(r.cfg.Build+"\n"), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// maintain lets git pack what has accumulated.
//
// Every commit here writes a whole new blob for each file it touched --
// git has no concept of appending to one -- so a repository that is
// committed to on a timer accumulates loose objects, each a complete
// zlib copy of a dump that is mostly identical to the one before it.
// Packing turns that pile into deltas, and it is dramatic rather than
// marginal: a measured day of a busy deployment is gigabytes loose and
// tens of megabytes packed (see growth_test.go).
//
// git's own `gc --auto` is what does it, at the threshold configure()
// sets, so the policy is git's and the only thing this adds is a place
// to call it from. Errors are deliberately dropped: housekeeping that
// could not run is a repository that is larger than it needs to be, and
// failing a sync -- and so a push of real state -- over that would be
// the worse outcome by far.
func (r *Repo) maintain(ctx context.Context) {
	_, _ = r.git(ctx, "gc", "--auto", "--quiet")
}

// git runs one git command in the working tree.
func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, nil, args...)
}

// gitAuthed runs a git command that talks to the remote, with whatever
// credential Config.Token yields.
//
// The token goes into a temporary askpass script rather than into the
// URL or onto the command line: anything in argv is readable by any
// other process on the host through `ps`, and a token baked into a
// remote URL is worse still -- it persists in .git/config, which is a
// file inside the very repository this package pushes to a remote.
func (r *Repo) gitAuthed(ctx context.Context, args ...string) (string, error) {
	if r.cfg.Token == nil || !strings.HasPrefix(r.cfg.Remote, "http") {
		return r.run(ctx, nil, args...)
	}
	token, err := r.cfg.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("staterepo: no credential for %s: %w", r.cfg.Remote, err)
	}
	if token == "" {
		return r.run(ctx, nil, args...)
	}
	dir, err := os.MkdirTemp("", "grain-staterepo-askpass-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "askpass.sh")
	// git asks twice: once for the username, once for the password. The
	// prompt it passes as $1 is what tells them apart.
	body := "#!/bin/sh\ncase \"$1\" in\n*[Uu]sername*) printf '%s' 'x-access-token' ;;\n*) printf '%s' " +
		shellQuote(token) + " ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		return "", err
	}
	env := []string{
		"GIT_ASKPASS=" + script,
		// Without this, a git that cannot use the askpass falls through to
		// a terminal prompt and hangs the daemon forever instead of failing.
		"GIT_TERMINAL_PROMPT=0",
	}
	return r.run(ctx, env, args...)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *Repo) run(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	// A git operation against a remote can hang on a network that
	// half-answers; every call here gets a ceiling so a sync cycle cannot
	// wedge the daemon's timer.
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	// clone names its own destination and must not run inside a directory
	// that is not a repository yet.
	if args[0] != "clone" && args[0] != "init" {
		cmd.Dir = r.cfg.Dir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	if args[0] == "clone" || args[0] == "init" {
		// The files ref backend whatever git this is and whatever its
		// config defaults to: a reftable repository made by a newer git
		// is one an older one cannot open at all (task 410). Older gits
		// ignore the variable and only ever make files repositories.
		cmd.Env = append(cmd.Env, "GIT_DEFAULT_REF_FORMAT=files")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s",
			strings.Join(redact(args), " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// redact keeps a remote URL's userinfo out of an error message, for the
// case where an operator pasted a URL with a token in it during the
// bootstrap. gitAuthed never builds one, but a human can.
func redact(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if at := strings.Index(a, "@"); at > 0 && strings.Contains(a[:at], "://") {
			scheme := a[:strings.Index(a, "://")+3]
			out[i] = scheme + "***@" + a[at+1:]
			continue
		}
		out[i] = a
	}
	return out
}

const gitTimeout = 2 * time.Minute
