package staterepo

// postcheck.go is grain checking its own settings changes, on this host,
// and saying so on the pull request as a check run.
//
// installWorkflow (format.go) asks GitHub Actions to do it: a workflow
// file in the state repository that runs `grain state check` from this
// deployment's own image on every pull request. That is the right answer
// for a repository whose Actions run. For one whose Actions cannot start
// -- a private repository on an account out of minutes or at its
// spending limit, which is what workflowhealth.go exists to notice --
// the check is concluded by GitHub before a job of it begins, and every
// settings pull request the deployment will ever make sits behind a red
// X that no commit clears. The operator's remedies are all off-host:
// pay, add a runner, or give the validation up ("noWorkflow": true).
//
// The deployment holds everything the check needs, though -- the binary,
// the schema, and a credential -- so it can run the import itself and
// post the answer through the Checks API, which is what this does. See
// docs/state-repo-check-posted-by-grain.md for the whole argument; three
// things about it are load-bearing enough to repeat where the code is.
//
// **The gate has to stay fail-closed, and the naive shape opens it.** A
// missing check is not a blocked pull request, it is a clean one:
// pkg/orchestrator's healthFrom reads an empty check list, once its
// registration window settles, as PrClean and merges. So a deployment
// that posted a check only when it managed to run one would have
// converted a gate that fails closed into one that fails open -- by
// default rather than by accident. Two rules close that, and both are
// here rather than argued about elsewhere: the check run is created
// *in_progress before the tree is checked*, so a deployment that dies
// half way through leaves a pull request wearing a check that never
// finishes (which blocks, and escalates to a person on the stall
// deadline) rather than one wearing nothing; and the merge queue is told
// this one repository requires this one check by name, so a pull request
// this deployment never saw at all is not clean either
// (Config.StateRepoCheck in pkg/orchestrator).
//
// Both of those are rules about grain's own queue, which is what merges
// settings here. Requiring the same name in the repository's branch
// protection -- so that GitHub's merge button knows about it too -- is an
// operator's own move and deliberately not grain's: administration
// rights on that repository are not on the list of permissions this
// credential asks for. The State pane and `grain state status` say so
// wherever they name the posted check, because a rule nobody is told
// about is a rule that gets set once and then wondered at.
//
// **The image and the host are the same binary.** What the Actions run
// buys is the trigger, the venue and the record, and only the trigger is
// load-bearing: installWorkflow already pins that workflow at this
// deployment's own image, because `grain state check` refuses a dump
// stamped with a schema it does not know, so a check run anywhere else
// answers a question nobody asked. The venue is if anything the wrong
// way round -- the host that will actually import this tree is the more
// faithful place to ask whether it can -- and the record survives, since
// a check run carries an output body and this puts the import's own
// report in it.
//
// **An unmerged pull request's tree is not something to import in
// process.** The check runs as a subprocess of the binary already on the
// box, with a deadline (runCheckSubprocess), rather than by calling
// Check() here. The exposure either way is small -- a JSON read and a
// transaction of inserts into a throwaway database -- but "small" is not
// a thing to find out about from inside the daemon's own process, and
// the whole cost of bounding it is one exec.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// PostedCheckName is what the check run grain posts is called.
//
// The same name on every deployment, and named here rather than
// configured, because two halves have to agree on it: this package
// writes it, and the merge queue refuses to call a pull request against
// the state repository clean without it (pkg/orchestrator's
// requiredCheckMissing). cmd/grain, which can see both, is what carries
// this string from one to the other.
//
// It reads as the command an operator would run to reproduce it, which
// is exactly what it is.
const PostedCheckName = "grain state check"

// PullRequestHead is one open pull request on the state repository, as a
// Config.OpenPullRequests callback hands it over.
//
// HeadSHA is the commit the check run is keyed to, and it is the head
// ref rather than GitHub's merge ref: refs/pull/N/merge is a commit
// GitHub composed, which no check run can be posted against and which
// nobody reviewing the pull request is looking at.
type PullRequestHead struct {
	Number  int
	Title   string
	HeadSHA string
}

// CheckRunPost is one create-or-conclude of the check run, as a
// Config.PostCheckRun callback receives it. ID is zero to create and the
// id GitHub gave the run to conclude it; Status is "in_progress" or
// "completed", and Conclusion is set only with the second.
type CheckRunPost struct {
	ID         int64
	HeadSHA    string
	Name       string
	Status     string
	Conclusion string
	Title      string
	Summary    string
	Text       string
}

// ErrCheckNotRun marks the difference between the two ways a check can
// fail to be green, which have to be told apart because they are acted
// on differently: a tree that does not load is a verdict to post, and a
// check that could not be run at all -- an exec that would not start, a
// deadline, a fetch that failed -- is nothing to conclude from.
//
// The check run then stays in_progress and the next tick tries again,
// which is deliberately the blocking answer: a deployment that cannot
// run its own check must not thereby let settings through unchecked, and
// a condition that persists is one the merge queue's stall deadline
// eventually takes to a person.
var ErrCheckNotRun = errors.New("grain could not run the state check")

// postedChecksFile records the check runs this host has posted, keyed by
// the commit each was posted against.
//
// In the git directory, beside grain-workflow-health and for the same
// reason: it is this host's own record of what it did with its own
// credential, and committing it would push one deployment's bookkeeping
// at every clone of the repository. Losing it costs one duplicate check
// run per open pull request, which is why nothing here treats a missing
// or unreadable marker as anything worse than "post again".
const postedChecksFile = "grain-posted-checks"

// postedChecksKept bounds that file. A state repository with more than a
// few dozen pull requests behind it is not a thing this deployment has
// ever seen, and the entries that matter are the open ones -- an entry
// for a merged pull request is only ever read to be skipped.
const postedChecksKept = 50

// checkTimeout bounds one run of the check.
//
// The import is a JSON read and a transaction of inserts, and this
// deployment's own start does the same work in seconds. What the
// deadline is really for is the case where it does not come back at all:
// this runs on the sync tick, under the lock that keeps an adopt and an
// import apart, so a check that hung would hold up the export of every
// settings change on the deployment rather than only its own reporting.
const checkTimeout = 2 * time.Minute

// postedCheck is one entry of that marker.
type postedCheck struct {
	Number    int       `json:"number"`
	HeadSHA   string    `json:"headSha"`
	ID        int64     `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	// Concluded is what makes an entry final. An entry that is not is a
	// check this host created and never finished -- a deployment
	// restarted mid-check, a fetch that failed -- and the next tick picks
	// that same check run up by its id and finishes it rather than
	// posting a second one.
	Concluded bool `json:"concluded"`
	Passed    bool `json:"passed,omitempty"`
}

type postedChecks struct {
	Checks []postedCheck `json:"checks"`
}

// postCheckRefused keeps the "this credential may not post checks" line
// to one a process, the way checkRunsFor's own permission line is kept
// to one: it is a fact about the configuration, it will repeat on every
// tick forever, and an operator needs to read it once. An atomic rather
// than a sync.Once because this package has a function called sync
// (bind.go) and so cannot import that one.
var postCheckRefused atomic.Bool

// postChecks checks the open pull requests on the state repository and
// posts what it finds, at most one import a tick.
//
// One a tick rather than all of them, because the tick is thirty seconds
// and the lock this holds is the one an export needs: three open
// settings pull requests are all checked inside two minutes, and no tick
// is ever held for longer than one check. Every failure here is best
// effort in the same sense observeWorkflowHealth's are -- a deployment
// must not stop exporting its settings because it could not check
// somebody else's -- with the one asymmetry the design turns on: a
// failure leaves a check run *in progress*, never a check run absent.
//
// Pull requests only, never the state branch itself. The installed
// workflow makes the same choice for the reason its own header gives:
// grain commits to this repository on a timer, and checking its own
// export would spend a run every time a task changed state.
func (r *Repo) postChecks(ctx context.Context) {
	if !r.cfg.PostCheck || r.cfg.Remote == "" ||
		r.cfg.OpenPullRequests == nil || r.cfg.PostCheckRun == nil {
		return
	}
	prs, err := r.cfg.OpenPullRequests(ctx)
	if err != nil {
		log.Printf("staterepo: could not list the open pull requests on %s, so grain could not "+
			"check them itself this tick: %v", r.cfg.Remote, err)
		return
	}
	posted := r.readPostedChecks(ctx)
	for _, pr := range prs {
		// A pull request GitHub has not filled a head commit in for yet.
		// There is nothing to key a check run to, and the next tick will
		// have one.
		if pr.HeadSHA == "" {
			continue
		}
		if entry, ok := posted.find(pr.HeadSHA); ok && entry.Concluded {
			continue
		}
		if r.postCheckFor(ctx, &posted, pr) {
			// One import a tick. A pull request this tick could not even
			// start on (a fetch that failed) does not count as that one:
			// it must not be able to starve the pull request behind it.
			break
		}
	}
	posted.prune()
	if err := r.writePostedChecks(ctx, posted); err != nil {
		log.Printf("staterepo: could not record which check runs grain has posted on %s (%v), so "+
			"it may post one of them again", r.cfg.Remote, err)
	}
}

// postCheckFor takes one pull request from "nothing said about it" to a
// concluded check run, and reports whether the check itself actually
// ran.
//
// The order is the fail-closed rule, and it is why this is not one call:
// the run is created before the tree is fetched, so every way of dying
// between here and the verdict leaves a pull request that blocks.
func (r *Repo) postCheckFor(ctx context.Context, posted *postedChecks, pr PullRequestHead) bool {
	entry, ok := posted.find(pr.HeadSHA)
	if !ok {
		id, err := r.cfg.PostCheckRun(ctx, CheckRunPost{
			HeadSHA: pr.HeadSHA,
			Name:    PostedCheckName,
			Status:  "in_progress",
			Title:   "Checking",
			Summary: checkAuthor + " is importing this tree into a throwaway database, the same " +
				"way it imports the state repository at start.",
		})
		if err != nil {
			r.reportPostFailure(err)
			return false
		}
		entry = postedCheck{Number: pr.Number, HeadSHA: pr.HeadSHA, ID: id, StartedAt: r.now()}
		posted.put(entry)
		// Written before the check runs, not after it: the marker is what
		// stops the next tick posting a second check run for the same
		// commit, and the check is the slow part in between.
		if err := r.writePostedChecks(ctx, *posted); err != nil {
			log.Printf("staterepo: could not record the check run grain just posted on %s#%d: %v",
				r.cfg.Remote, pr.Number, err)
		}
	}
	report, err := r.checkPullRequest(ctx, pr)
	if errors.Is(err, ErrCheckNotRun) {
		log.Printf("staterepo: grain could not check %s#%d this tick, so the check it posted there "+
			"stays unfinished and that pull request stays blocked: %v", r.cfg.Remote, pr.Number, err)
		return false
	}
	post := CheckRunPost{
		ID: entry.ID, HeadSHA: pr.HeadSHA, Name: PostedCheckName,
		Status: "completed", Text: report,
	}
	if err == nil {
		post.Conclusion, post.Title = "success", "This tree loads"
		post.Summary = checkAuthor + " imported this tree into a throwaway database, at the schema " +
			"this build knows."
	} else {
		post.Conclusion, post.Title = "failure", "This tree does not load"
		post.Summary = fmt.Sprintf("%s could not import this tree, so merging it would leave this "+
			"deployment holding settings it cannot read:\n\n%s", checkAuthor, err)
		log.Printf("staterepo: %s#%d does not load on this deployment: %v",
			r.cfg.Remote, pr.Number, err)
	}
	if _, perr := r.cfg.PostCheckRun(ctx, post); perr != nil {
		r.reportPostFailure(perr)
		return true
	}
	entry.Concluded, entry.Passed = true, err == nil
	posted.put(entry)
	return true
}

// checkPullRequest fetches the pull request's head into the clone grain
// already keeps, checks it out into a worktree of its own, and runs the
// check over it.
//
// A worktree rather than the working tree itself: the working tree is
// the deployment's live settings, the thing every export writes and
// every sync commits, and checking somebody's proposal by checking it
// out over that is how a tick ends up committing a stranger's tree as
// this deployment's own state.
func (r *Repo) checkPullRequest(ctx context.Context, pr PullRequestHead) (string, error) {
	ref := fmt.Sprintf("refs/pull/%d/head", pr.Number)
	if _, err := r.gitAuthed(ctx, "fetch", "--quiet", "--end-of-options", "origin", ref); err != nil {
		return "", fmt.Errorf("%w: fetching %s from %s: %v", ErrCheckNotRun, ref, r.cfg.Remote, err)
	}
	// The commit the check run was posted against, not whatever that ref
	// points at now. A push landing between the listing and this fetch
	// would otherwise have grain check one tree and report the answer
	// against another commit's name.
	if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", pr.HeadSHA+"^{commit}"); err != nil {
		return "", fmt.Errorf("%w: %s is not in this clone after fetching %s: %v",
			ErrCheckNotRun, pr.HeadSHA, ref, err)
	}
	dir, err := os.MkdirTemp("", "grain-state-check-")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCheckNotRun, err)
	}
	tree := filepath.Join(dir, "tree")
	defer func() {
		// --force, because the check ran in there and a check that left
		// something behind must not leave a worktree registered for the
		// life of the deployment; prune tidies the registration up
		// whatever happened to the directory.
		_, _ = r.git(ctx, "worktree", "remove", "--force", "--", tree)
		_ = os.RemoveAll(dir)
		_, _ = r.git(ctx, "worktree", "prune")
	}()
	if _, err := r.git(ctx, "worktree", "add", "--detach", "--quiet", "--", tree, pr.HeadSHA); err != nil {
		return "", fmt.Errorf("%w: checking %s out: %v", ErrCheckNotRun, pr.HeadSHA, err)
	}
	check := r.cfg.CheckTree
	if check == nil {
		check = runCheckSubprocess
	}
	return check(ctx, tree)
}

// runCheckSubprocess runs `grain state check` over a tree, as a process
// of its own with a deadline.
//
// This binary, found through os.Executable rather than by name on PATH:
// the check has to be run by the build that would import the tree for
// real, since it refuses a dump stamped with a schema it does not know,
// and "whatever `grain` resolves to on this host" is not that promise.
//
// Its combined output is the answer either way -- the check's own report
// is what a reader of a red check run needs, and there is no job log
// anywhere else to go and read.
func runCheckSubprocess(ctx context.Context, dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: grain could not find its own binary: %v", ErrCheckNotRun, err)
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "state", "check", dir)
	out, err := cmd.CombinedOutput()
	report := strings.TrimSpace(string(out))
	if ctx.Err() != nil {
		return report, fmt.Errorf("%w: it did not finish within %s", ErrCheckNotRun, checkTimeout)
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return report, nil
	case errors.As(err, &exit):
		// A non-zero exit is the check's verdict, which is the one thing
		// here that is not a failure to run it.
		if report == "" {
			report = fmt.Sprintf("`grain state check` exited %d and said nothing.", exit.ExitCode())
		}
		return report, errors.New(firstLine(report))
	default:
		return report, fmt.Errorf("%w: running `grain state check`: %v", ErrCheckNotRun, err)
	}
}

// firstLine is what a check run's summary says about a failure: the
// first line of the report, since the whole of it is already in the body
// underneath and a summary that repeated it would be a wall of text
// above a wall of text.
func firstLine(s string) string {
	if at := strings.IndexByte(s, '\n'); at >= 0 {
		return strings.TrimSpace(s[:at])
	}
	return s
}

// checkAuthor is how the check run refers to whoever posted it. Not a
// hostname: what a reader of the pull request needs told is that this
// verdict came from the deployment that will have to read these settings
// rather than from a GitHub runner, and that is the same sentence on
// every host.
const checkAuthor = "The grain deployment that runs on these settings"

// reportPostFailure says what a refusal from the Checks API means, once.
//
// A 403 here is the configuration rather than a fault to retry: only a
// GitHub App installation may create a check run, and a deployment on a
// personal access token -- fine-grained or classic -- will be refused on
// every tick for the life of the process.
func (r *Repo) reportPostFailure(err error) {
	if postCheckRefused.Swap(true) {
		return
	}
	log.Printf("staterepo: grain could not post its own check run on %s (%v). Only a GitHub App "+
		"installation may create check runs -- a personal access token cannot, whether "+
		"fine-grained or classic -- so a deployment with \"postCheck\": true in %s needs an "+
		"App credential with Checks write on that repository. Until it has one, pull requests "+
		"there are checked by whatever GitHub Actions can run and by nothing else.",
		r.cfg.Remote, err, SettingsFileName)
}

func (p *postedChecks) find(headSHA string) (postedCheck, bool) {
	for _, c := range p.Checks {
		if c.HeadSHA == headSHA {
			return c, true
		}
	}
	return postedCheck{}, false
}

func (p *postedChecks) put(entry postedCheck) {
	for i, c := range p.Checks {
		if c.HeadSHA == entry.HeadSHA {
			p.Checks[i] = entry
			return
		}
	}
	p.Checks = append(p.Checks, entry)
}

// prune keeps the newest postedChecksKept entries. Oldest first is the
// order they were appended in, so the tail is what survives.
func (p *postedChecks) prune() {
	if len(p.Checks) > postedChecksKept {
		p.Checks = p.Checks[len(p.Checks)-postedChecksKept:]
	}
}

func (r *Repo) readPostedChecks(ctx context.Context) postedChecks {
	path, err := r.gitDirFile(ctx, postedChecksFile)
	if err != nil {
		return postedChecks{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return postedChecks{}
	}
	var posted postedChecks
	if err := json.Unmarshal(data, &posted); err != nil {
		return postedChecks{}
	}
	return posted
}

func (r *Repo) writePostedChecks(ctx context.Context, posted postedChecks) error {
	path, err := r.gitDirFile(ctx, postedChecksFile)
	if err != nil {
		return err
	}
	data, err := json.Marshal(posted)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}
