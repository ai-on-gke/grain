package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/workflows"
)

// Warming the sandbox guest cache once, for the branch that changed a
// guest input (grain/task-410).
//
// # What this is about
//
// A job that boots a real VM needs a sandbox guest image in front of it,
// ~1GB of Debian with a Go toolchain, a node, a docker and a kind node
// image already in it. Nobody builds one per job: it is
// content-addressed on the files that decide what comes out of the build
// (scripts/granule/guest-inputs.sh), published as `guest:inputs-<key>`,
// and a job that resolves a guest pulls that tag
// (scripts/granule-guest-resolve.sh). On a hit that is a `docker pull`.
// On a miss it is a whole `granule granulefile build` of the
// Granulefile in the CI sandbox -- every apt install, toolchain download
// and `VM PACK` it describes, all of it work an earlier commit already
// did.
//
// Two publishers fill that cache and both of them are about `main`: the
// release run on every merge (.grain/workflows/release.yml), and, with
// releases switched off, the jobs an operator names for grain to
// dispatch on the base branch after every merge
// (model.RepoConfig.PostMergeDispatchJobs, grain/task-409). Between them
// they cover `main` and every branch that changes none of those files --
// nearly all of them, in a Go and TypeScript repository.
//
// What neither can cover is the branch that changes one. Its key does
// not exist in the registry until it lands, so until then *every* job on
// that branch which resolves a guest builds its own, and several jobs on
// several branches can miss the same key at the same time and each start
// a build of the identical image. The only answer before this was an
// operator dispatching the cache job by hand, which is documented in
// .grain/workflows/image.yml and which nobody does until the branch has
// already cost an hour.
//
// # What grain does instead
//
// A job declares the files its guest is built from (`guest-cache:`, see
// Job.GuestCache). The plan turns that into a GuestKey -- the content of
// those files, so two branches with identical guest inputs have the same
// key and a branch that changes one has a key of its own. Before such a
// job takes a sandbox, grain warms that key: one CI run, of the jobs the
// repository's operator has already named for filling its caches, on
// this branch's own commit.
//
// Everything else waits for that one run rather than starting another.
// The single flight is keyed on GuestKey and on nothing else -- not on
// the branch, not on the run -- because the cache it fills is keyed on
// exactly that: two branches whose guest inputs are identical miss the
// same tag and want the same one build, and treating them as two pieces
// of work would be the duplicate build this whole file exists to stop.
//
// # Why a run of its own rather than a build inside the first job
//
// Because a build inside a job is a build nobody else can have. A CI job
// is a whole machine with a docker daemon of its own; an image built in
// job A's daemon is invisible to job B, on the same branch or any other,
// and the only thing that makes one build serve every waiter is that it
// is *pushed* -- which needs the package-publish capability, whose
// material is placed for a release or a `workflow_dispatch` and never
// for a push (pkg/capability/packagepublish.SupportsCIEvent). So the
// warm is a dispatch, the same event and the same narrowing the
// post-merge dispatch uses, and what it leaves behind is a registry tag
// every later job on every branch pulls.
//
// # What this trusts, said plainly
//
// The warm run executes *this branch's* copy of the job it dispatches,
// with that dispatch's publishing material, and publishes an image under
// a key that `main` will pull once the branch lands. That is a real
// widening of what a branch can reach, and it is the one thing here that
// needs an operator's judgement rather than an engineer's: today the
// same act takes a person typing `grain ci run -job ...`, and
// .grain/workflows/image.yml calls that person's dispatch a vouching for
// the branch's guest inputs.
//
// Two things keep it as narrow as it can be while still working:
//
//   - grain warms nothing for a repository whose operator has named no
//     cache-filling job at all (model.RepoConfig.PostMergeDispatchJobs).
//     The setting that says "these jobs are grain's to dispatch, and
//     they fill build inputs rather than shipping releases" is the same
//     setting, read for the same purpose, on a different ref.
//   - the run is narrowed to those jobs, exactly as the post-merge
//     dispatch is. It is never "whatever the dispatch plan would have
//     run".
//
// What it does not do is ask a person per branch, and that is the
// deliberate change: the whole cost this removes is the hour a branch
// spends before anybody notices it should have asked.
//
// # What happens when the declaration is wrong, or the warm fails
//
// Every failure here degrades to what the branch does today, which is
// build the guest inline:
//
//   - a job that declares nothing, a declaration that matches no file, a
//     run whose source could not read the tree: no GuestKey, no warm.
//   - a repository with no cache job named: no warm.
//   - a warm that fails, errors, or is still going when the waiting job
//     has spent its own timeout: the job goes on and resolves its guest
//     the way it always did.
//
// Degrading to an inline build is a cost in minutes and must never be
// more than that. It used to be more: the inline build spent the job's
// whole `timeout-minutes` and the job came back a *failure*, so a cold
// cache was a red verdict on a branch nothing was wrong with
// (grain/task-464). A step that resolves a guest says
// `infrastructure: true` now -- it is off the job's clock, and if it
// cannot get a guest at all the job is ConclusionErrored, which holds the
// merge queue while somebody looks at the deployment and blames no branch
// for it. Nothing in this file is load-bearing for that: the warm makes
// the miss rarer, and Step.Infrastructure makes the miss harmless.
//
// And a declaration that selects *more* files than the build really
// reads is safe in the direction that matters: it warms more often than
// it strictly must, and each unnecessary warm is a run whose job pulls
// the tag that already exists and exits. Selecting fewer is the
// dangerous direction -- grain would believe a key was warmed that
// nothing warmed -- and the defence against it is a test rather than a
// rule: tests/deploy holds the declaration to what
// scripts/granule/guest-inputs.sh prints.
//
// # What it deliberately does not do
//
// It does not ask the registry whether the key is there. Grain has no
// registry client and giving it one would put a publishing credential in
// the controller to answer a question the warm run itself answers for
// free: the cache job's first act is a `docker pull` of that exact tag,
// so a warm of a key that is already published is a minute of one
// sandbox and no build at all. The cost of not knowing is therefore one
// cheap run per distinct set of guest inputs per daemon lifetime, paid
// by the first job to want it, and the saving is the branch that changed
// an input never building its guest more than once.
//
// It keeps no record across a restart either. The flight book below is
// memory, which is enough for what it is for -- every CI run of a
// deployment is driven by one process, so "several jobs at once" and
// "several branches at once" are both in it -- and a daemon that
// restarts mid-warm has lost the run it was waiting for anyway.

// GuestCacheWarmer is consulted before a job that declares `guest-cache:`
// takes a sandbox, and returns once the guest that job will resolve is in
// the cache, or once trying has stopped being worth it.
//
// An error is not a reason to fail the job: it means the guest was not
// warmed, and a job whose guest is not warmed resolves one for itself,
// which is what it did before any of this existed. The engine logs it
// and starts the job.
//
// wait takes the same Wait a Gate reports, so a job held here is held
// visibly and is bucketed apart from a job queueing for a machine --
// twenty minutes waiting for a guest and twenty minutes waiting for
// memory are the same length and two entirely different problems. It is
// called only when this is actually going to block, which is the
// contract every other waiter in the engine follows.
type GuestCacheWarmer interface {
	Warm(ctx context.Context, req GuestCacheRequest, wait func(workflows.Wait)) error
}

// GuestCacheRequest is one job's ask: warm the guest this tree needs.
type GuestCacheRequest struct {
	// RunID is the run the asking job belongs to. It is here for one
	// reason: a warm run's own jobs must never warm anything, or a
	// repository that declared `guest-cache:` on its cache job would have
	// grain dispatching runs to warm the run that is doing the warming.
	RunID string
	// Repo, Ref and Commit are what a warm run would be started against:
	// this branch's own tree, since that is the tree whose guest inputs
	// are missing from the cache.
	Repo   string
	Ref    string
	Commit string
	// Key identifies the guest itself -- the content of the job's declared
	// guest inputs (PlannedJob.GuestKey). It is the whole of what the
	// single flight is keyed on.
	Key workflows.Key
	// Job is the plan id of the job asking, for the log line. Nothing
	// branches on it.
	Job string
}

// GuestWarmer is the GuestCacheWarmer a deployment runs: one dispatched
// run per guest key, and every other job that wants that guest waiting
// for it.
//
// Start and Get are the two things it needs of the CI service that owns
// it, passed as functions rather than as the service itself because the
// service is built on top of this package's Engine and cannot be named
// here (cmd/grain is where the two ends meet).
type GuestWarmer struct {
	// Jobs is what this repository's operator has named as the jobs grain
	// may dispatch to fill its caches
	// (model.RepoConfig.PostMergeDispatchJobs). No jobs, no warm -- see
	// this file's "What this trusts".
	//
	// nil warms nothing at all, which is the right behaviour for a
	// deployment that has not thought about any of this.
	Jobs func(repo string) []string
	// Start starts a CI run and returns before it finishes.
	Start func(ctx context.Context, req workflows.Request) (workflows.Run, error)
	// Get reads a run back by id, reporting false for a run this
	// deployment no longer has.
	Get func(runID string) (workflows.Run, bool)
	// Poll is how often a warm in flight is read back. Zero means
	// DefaultGuestWarmPoll.
	Poll time.Duration
	// Cooldown is how long a *failed* warm stands before another job may
	// start a second one for the same key. Zero means
	// DefaultGuestWarmCooldown.
	//
	// It exists because the expensive failure mode of a retry here is not
	// wasted minutes but a loop: a guest that cannot be built in this
	// deployment (an archive outside CI's egress is the one that has
	// actually happened) would otherwise have every job on every branch
	// starting another 25-minute run to find that out again, and that is
	// a deployment's whole CI capacity. A warm that failed means the jobs
	// after it build their guests inline, which is exactly what they did
	// before.
	Cooldown time.Duration
	// MaxWarmTime bounds one warm run's own life as far as this is
	// concerned -- not the run, which has its own timeouts, but how long
	// this goes on watching it. Zero means DefaultGuestWarmMaxTime.
	MaxWarmTime time.Duration
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Log, when set, is called with one line per warm started and per
	// warm finished. nil discards.
	Log func(format string, args ...any)

	mu sync.Mutex
	// warms is the flight book, keyed on the guest key and nothing else.
	warms map[workflows.Key]*guestWarm
	// started is the runs this warmer itself began, so that a job of one
	// of them never asks for a warm of the run it is already part of.
	started map[string]bool
}

// The defaults, all three of them chosen against the same picture: a
// guest build is ~25 minutes when it works and an hour when the network
// is against it, and the thing waiting for it is a CI job holding
// nothing but a place in a queue.
const (
	// DefaultGuestWarmPoll is how often a warm in flight is read back.
	// Fifteen seconds against a half-hour build is noise, and it is short
	// enough that a warm which turns out to be a cache hit releases its
	// waiters about as soon as it finishes.
	DefaultGuestWarmPoll = 15 * time.Second
	// DefaultGuestWarmCooldown is how long a failed warm stands. Long
	// enough that a branch which cannot build its guest does not spend
	// this deployment's CI on finding that out over and over, short
	// enough that a fix pushed within the hour is tried.
	DefaultGuestWarmCooldown = 30 * time.Minute
	// DefaultGuestWarmMaxTime is how long this watches one warm run
	// before it stops caring. Above .grain/workflows/image.yml's own
	// 90-minute job timeout, because the run's timeout is the run's to
	// enforce and this should be reporting its verdict rather than
	// inventing one.
	DefaultGuestWarmMaxTime = 2 * time.Hour
)

// guestWarm is one guest key's flight: the run warming it, and what
// became of it.
type guestWarm struct {
	// done is closed when this warm has concluded, one way or another.
	// Waiters select on it and on their own context, which is what keeps
	// a job from waiting past its own timeout.
	done chan struct{}
	// err is nil for a warm that published (or found) the guest, and
	// otherwise says what went wrong. Read only after done is closed.
	err error
	// finishedAt is when it concluded, for the cooldown.
	finishedAt time.Time
}

func (w *GuestWarmer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *GuestWarmer) log(format string, args ...any) {
	if w.Log != nil {
		w.Log(format, args...)
	}
}

func (w *GuestWarmer) poll() time.Duration {
	if w.Poll > 0 {
		return w.Poll
	}
	return DefaultGuestWarmPoll
}

func (w *GuestWarmer) cooldown() time.Duration {
	if w.Cooldown > 0 {
		return w.Cooldown
	}
	return DefaultGuestWarmCooldown
}

func (w *GuestWarmer) maxWarmTime() time.Duration {
	if w.MaxWarmTime > 0 {
		return w.MaxWarmTime
	}
	return DefaultGuestWarmMaxTime
}

// Warm implements GuestCacheWarmer.
func (w *GuestWarmer) Warm(ctx context.Context, req GuestCacheRequest, wait func(workflows.Wait)) error {
	if w == nil || req.Key == "" || w.Start == nil || w.Get == nil {
		return nil
	}
	if w.isOurs(req.RunID) {
		// A job of a warm run asking to be warmed. Nothing to do and
		// nothing to say: the run it belongs to *is* the warm.
		return nil
	}
	var jobs []string
	if w.Jobs != nil {
		jobs = w.Jobs(req.Repo)
	}
	if len(jobs) == 0 {
		// Nobody has said which of this repository's jobs fill its
		// caches, so there is nothing grain is entitled to dispatch. The
		// job resolves its own guest, exactly as it did before.
		return nil
	}

	flight, mine := w.claim(req.Key)
	if mine {
		// Detached from the asking job's context on purpose: the run
		// serves every job that wants this guest, so the first one
		// walking away -- its own timeout, its run cancelled -- must not
		// take the build with it. It is bounded by MaxWarmTime instead.
		go w.drive(req, jobs, flight)
	}
	select {
	case <-flight.done:
		return flight.err
	default:
	}
	if wait != nil {
		// No run id in it, on purpose: the run this job is waiting for is
		// usually not started yet at this moment -- it is being started
		// by the goroutine above -- and a reason that named it would be
		// racing the thing it names for the sake of a word.
		wait(workflows.Wait{
			Cause:  workflows.WaitForGuest,
			Reason: "waiting for the sandbox guest this job resolves to be built and published",
		})
	}
	select {
	case <-flight.done:
		return flight.err
	case <-ctx.Done():
		// The job's own timeout, or the run being cancelled. Either way
		// this job stops waiting; the warm goes on for whoever is next.
		return fmt.Errorf("gave up waiting for the sandbox guest to be warmed: %w", ctx.Err())
	}
}

// claim is the single flight itself: the flight already warming this key,
// or a new one this caller is to drive.
//
// mine is true for exactly one caller per flight, which is what makes
// "several jobs missing the same key at once" one build rather than
// several -- and it is decided under the mutex, before anything is
// started, so two jobs arriving in the same microsecond cannot both think
// they are first.
func (w *GuestWarmer) claim(key workflows.Key) (flight *guestWarm, mine bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.warms == nil {
		w.warms = map[workflows.Key]*guestWarm{}
	}
	if existing, ok := w.warms[key]; ok {
		select {
		case <-existing.done:
			// Concluded. A warm that worked stands for good -- the tag is
			// in the registry and the next branch with these inputs pulls
			// it. A warm that failed stands for the cooldown, and then
			// somebody may try again.
			if existing.err == nil || w.now().Sub(existing.finishedAt) < w.cooldown() {
				return existing, false
			}
		default:
			return existing, false // in flight: wait for it, do not duplicate it
		}
	}
	flight = &guestWarm{done: make(chan struct{})}
	w.warms[key] = flight
	return flight, true
}

// drive starts the warm run and watches it to its conclusion.
func (w *GuestWarmer) drive(req GuestCacheRequest, jobs []string, flight *guestWarm) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), w.maxWarmTime())
	defer cancel()

	run, err := w.Start(ctx, workflows.Request{
		Repo: req.Repo, Ref: req.Ref, Commit: req.Commit,
		// `workflow_dispatch`, for RepoConfig.PostMergeDispatchJobs' own
		// reason: a job that fills a registry cache pushes to a registry,
		// and that capability's material is admitted to a dispatch and a
		// release and never to a push. Untrusted -- TrustedRelease is a
		// release run's property and this is not one.
		//
		// And no release secrets: the ref is whichever branch first
		// missed this guest, which may be an untrusted task branch, and
		// a dispatch would otherwise hand every one of the repository's
		// release secrets to its jobs (issue #2389). Warming a cache
		// never needs one.
		Event: workflows.EventManual, Jobs: jobs, WithholdReleaseSecrets: true,
		RequestedBy: "guest-cache-warm:" + req.Repo + ":" + req.Ref,
	})
	if err != nil {
		w.finish(flight, fmt.Errorf("starting a run to warm the sandbox guest cache: %w", err))
		w.log("ci: warming the sandbox guest for %s %s (%s): could not start %v: %v",
			req.Repo, req.Ref, shortKey(req.Key), jobs, err)
		return
	}
	w.remember(run.ID)
	w.log("ci: %s %s at %s needs a sandbox guest nothing has published: dispatching %v as run %s, "+
		"and every job waiting on this guest waits for that one run rather than building its own",
		req.Repo, req.Ref, shortSHA(req.Commit), jobs, run.ID)

	err = w.watch(ctx, run.ID)
	w.finish(flight, err)
	if err != nil {
		w.log("ci: warming the sandbox guest for %s %s (%s) did not work: %v -- the jobs that need it "+
			"resolve their own, which is what they did before anything warmed anything",
			req.Repo, req.Ref, shortKey(req.Key), err)
		return
	}
	w.log("ci: the sandbox guest for %s %s (%s) is warm: run %s",
		req.Repo, req.Ref, shortKey(req.Key), run.ID)
}

// watch reads the warm run back until it concludes, and says what that
// conclusion means for the jobs waiting on it.
func (w *GuestWarmer) watch(ctx context.Context, runID string) error {
	ticker := time.NewTicker(w.poll())
	defer ticker.Stop()
	for {
		run, ok := w.Get(runID)
		switch {
		case !ok:
			// Pruned, or lost with the daemon that was running it.
			// collectBaseBranchDispatch treats the same silence the same
			// way: it is not a success, and leaving it unconcluded would
			// leave every waiter waiting for a verdict that cannot come.
			return errors.New("the run warming this guest is no longer known to this deployment")
		case run.Interrupted:
			return errors.New("the run warming this guest was interrupted")
		case run.Conclusion.Done():
			if run.Conclusion == workflows.ConclusionSuccess {
				return nil
			}
			return fmt.Errorf("the run warming this guest ended %s", run.Conclusion)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("the run warming this guest had not finished after %s: %w",
				w.maxWarmTime(), ctx.Err())
		}
	}
}

// finish records how a flight ended and releases everybody waiting on it.
func (w *GuestWarmer) finish(flight *guestWarm, err error) {
	w.mu.Lock()
	flight.err = err
	flight.finishedAt = w.now()
	w.mu.Unlock()
	close(flight.done)
}

// remember records a run this warmer started, so that the jobs of that
// run never ask it for a warm.
func (w *GuestWarmer) remember(runID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started == nil {
		w.started = map[string]bool{}
	}
	w.started[runID] = true
}

func (w *GuestWarmer) isOurs(runID string) bool {
	if runID == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started[runID]
}

// shortKey is a guest key as a log line carries one. The keys are
// "guest:" and sixty-four hex digits, and what a reader does with one is
// tell it from the other keys in the same log.
func shortKey(key workflows.Key) string {
	s := string(key)
	if len(s) > 18 {
		return s[:18]
	}
	return s
}

// shortSHA is a commit as this package's log lines print one.
func shortSHA(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
