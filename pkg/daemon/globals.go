package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync/atomic"
	"time"

	"github.com/bwsalmon/grain/pkg/console"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/sysstat"
	"github.com/bwsalmon/grain/pkg/ui"
)

// reconcilerDown reports whether runDaemon has given up entirely --
// returned a non-nil error, including from a recovered panic -- rather
// than a run-level failure the next tick can still recover from. It was
// once also the "not given up yet" side of a per-slot provisioning step
// that retried with backoff; there is no such step now, since a sandbox
// is prepared per run and a failure there finishes that one run rather
// than wedging the deployment (orchestrator's runOne). Set once, from
// run()'s own goroutine, alongside the log line that already reports the
// same failure; never cleared, since -- like orchestrator.
// ChecksUnavailable -- this is a standing fact about *this process*, not
// an event, and only a restart (a fresh process, with a fresh zero
// value) can turn it back to false. GET /api/config surfaces it as
// reconcilerDown so a UI, or an external monitor polling that same
// endpoint, can see reconciliation is dead without an operator having to
// notice and interpret a single log line (bwsalmon/agents#576).
var ReconcilerDown atomic.Bool

// livePullRequests holds the GitHub-backed pull request opener runDaemon
// builds, for the UI/API server that was started before it to reach.
//
// The two halves of this process come up in that order on purpose: the
// UI/API server starts as early as the store allows, precisely so a
// deployment whose reconcile loop never comes up is still visible and
// operable (bwsalmon/agents#576, and run()'s own comment on the
// ordering). But the GitHub client belongs to runDaemon, below it, so
// POST /api/tasks/{id}/pull-request has nothing to call until runDaemon
// gets there. Set once, from runDaemon, and read through pullRequestGate
// -- the same shape reconcilerDown above already uses to let these two
// halves see one fact about each other without either owning the other.
//
// A run's mcpserver reaching this before it is set gets an honest "not
// ready yet" rather than a nil dereference; in practice nothing can,
// since no run exists to ask until the loop that dispatches runs is
// already going.
var LivePullRequests atomic.Pointer[PullRequestOpener]

// liveGitHubCalls holds the transport every REST call this deployment
// makes goes through, for ui.Config.GitHubCalls.
//
// A pointer published here rather than passed, for the reason
// livePullRequests and liveRepoNames next door are: startUIServer runs
// before runDaemon builds the transport, so the UI has to be wired to
// something that can be filled in afterwards.
var liveGitHubCalls atomic.Pointer[github.RealTransport]

// gitHubCallStats is ui.GitHubCallStats over whatever liveGitHubCalls
// holds when a report is asked for.
//
// A daemon that has not built its transport yet -- the window between
// the UI coming up and runDaemon reaching it, and the whole life of a
// `grain ui` with no reconcile loop -- answers the zero Stats, which the
// report renders as a section that has seen nothing rather than as one
// that is unavailable. That is the right way round: the UI *was* handed
// something to ask, and the honest answer is that it has no calls to
// report yet.
type gitHubCallStats struct{}

func (gitHubCallStats) Stats() github.Stats {
	if t := liveGitHubCalls.Load(); t != nil {
		return t.Stats()
	}
	return github.Stats{}
}

// liveRepoNames holds the same GitHub client for ui.Config.RepoNames,
// published by runDaemon for the UI/API server started ahead of it --
// livePullRequests' own reason, and the same shape.
//
// Its own pointer rather than a second reader of livePullRequests
// because that one holds a pullRequestOpener, whose client is a
// github.Client: RepoFullName is deliberately not on that interface (see
// its doc comment), so the concrete client is what has to be published
// here.
var liveRepoNames atomic.Pointer[github.RESTClient]

// repoNameGate is the ui.Config.RepoNames the UI/API server is given:
// whatever liveRepoNames holds by the time an add actually arrives, and
// a refusal until runDaemon has put one there.
//
// The refusal is not a failure. AddTargetRepo treats any error here as
// "this deployment cannot ask" and stores the operator's own spelling,
// so a repo added before the reconcile loop is up is added under the
// name that was typed rather than not added at all.
type repoNameGate struct{}

func (repoNameGate) RepoFullName(owner, name string) (string, error) {
	client := liveRepoNames.Load()
	if client == nil {
		return "", errors.New(
			"this daemon has no GitHub client yet, so it cannot ask how this repo spells its own name: " +
				"its reconcile loop has not started (or has failed -- check the daemon log)")
	}
	return client.RepoFullName(owner, name)
}

// repoComparisonGate supplies the task UI's internal pull-request view. The
// controller-owned config repositories (grain/config and every team's
// grain/config-<team>) stay on their local comparer; every GitHub-backed
// repo uses the same authenticated REST client the reconcile loop already
// publishes for repository reads. That client appears after the UI server is
// started, which is why this follows repoNameGate's load-at-call-time shape.
type repoComparisonGate struct {
	local ui.RepoComparer
}

func (g repoComparisonGate) Compare(ctx context.Context, repo model.RepoRef, base, head string) (ui.BranchComparison, error) {
	if model.IsConfigRepo(repo) {
		if g.local == nil {
			return ui.BranchComparison{}, errors.New("the config repository is not available")
		}
		return g.local.Compare(ctx, repo, base, head)
	}
	client := liveRepoNames.Load()
	if client == nil {
		return ui.BranchComparison{}, errors.New(
			"this daemon has no GitHub client yet, so it cannot compare this task's branch: " +
				"its reconcile loop has not started (or has failed -- check the daemon log)")
	}
	return GitHubRepoComparer{Client: client}.Compare(ctx, repo, base, head)
}

// repoBranchGate is ui.Config.RepoBranches: the branch pickers' listing
// for every repository GitHub's API does not serve. The controller-local
// config repository (model.IsConfigRepo, the predicate repoComparisonGate
// and grain CI's RequiredFor route it by too) is read from the state
// repository's own checkout and never from GitHub, where grain/config is
// a 404; a repository on a plain git host is read over git with the
// credential the git proxy clones it with. Anything else answers ok false
// and goes to GitHub as before.
type repoBranchGate struct {
	local  configRepoBranchLister
	remote interface {
		GitBranches(owner, repo string) ([]string, bool, error)
	}
}

// configRepoBranchLister is what repoBranchGate needs of the state
// manager (StateManager.Branches).
type configRepoBranchLister interface {
	Branches(ctx context.Context) ([]string, error)
}

func (g repoBranchGate) RepoBranches(ctx context.Context, repo model.RepoRef) ([]string, bool, error) {
	if model.IsConfigRepo(repo) {
		if g.local == nil {
			return nil, true, errors.New("the config repository is not available")
		}
		names, err := g.local.Branches(ctx)
		return names, true, err
	}
	if g.remote == nil {
		return nil, false, nil
	}
	return g.remote.GitBranches(repo.StoredOwner(), repo.Name)
}

// GitHubRepoComparer is the GitHub-backed half of repoComparisonGate: the
// task UI's internal pull-request view drawn from one REST client's
// compare endpoint. It is exported so `grain demo -github-sim` can put the
// same comparison in front of its simulated repo rather than a copy of it.
type GitHubRepoComparer struct {
	Client *github.RESTClient
}

func (c GitHubRepoComparer) Compare(_ context.Context, repo model.RepoRef, base, head string) (ui.BranchComparison, error) {
	client := c.Client
	if base == "" {
		var err error
		base, err = client.DefaultBranch(repo.Owner, repo.Name)
		if err != nil {
			return ui.BranchComparison{}, fmt.Errorf("reading %s's default branch: %w", repo, err)
		}
	}
	// The merge base first, and the diff from *that* commit rather than
	// from base itself -- see forkPointBeforeMerge for why a merged branch
	// otherwise compares to nothing at all.
	comparison, err := client.CompareRefs(repo.Owner, repo.Name, base, head)
	if err != nil {
		return ui.BranchComparison{}, fmt.Errorf("comparing %s and %s in %s: %w", base, head, repo, err)
	}
	merged := comparison.Status == "identical" || comparison.Status == "behind"
	mergeBase := comparison.MergeBase
	if merged {
		if forked := forkPointBeforeMerge(client, repo, base, head, comparison.MergeBase); forked != "" {
			mergeBase = forked
		}
	}
	from := base
	if mergeBase != "" {
		from = mergeBase
	}
	diff, err := client.CompareDiff(repo.Owner, repo.Name, from, head)
	if err != nil {
		return ui.BranchComparison{}, fmt.Errorf("comparing %s and %s in %s: %w", from, head, repo, err)
	}
	const maxDiff = 2 << 20
	if len(diff) > maxDiff {
		diff = diff[:maxDiff] + "\n\n[diff truncated at 2 MiB]\n"
	}
	return ui.BranchComparison{
		Base: base, Head: head, MergeBase: mergeBase, Merged: merged, Diff: diff,
	}, nil
}

// forkPointBeforeMerge is where head forked from base, for a head base
// already contains -- a branch that has been merged, which is every task
// branch grain has finished with.
//
// It is needed because a merge makes the ordinary answer useless. The
// merge base of two refs where one contains the other is that one's own
// tip, so the moment a branch lands its comparison against base is empty,
// and grain's own pull-request view goes blank exactly when somebody
// wants to read what a finished task actually did (grain/task-375).
//
// The fork point is still there to be found, in the merge commit: base
// carries a commit whose parents are base as it stood and head's tip, and
// the merge base of that first parent with head is where the branch began
// -- which is precisely the comparison GitHub itself goes on drawing on a
// merged pull request. headSHA is head's tip, which for a contained head
// is the merge base the caller has already read.
//
// Everything here degrades to "" rather than to an error: a branch merged
// more than 250 commits ago (CompareRefs' own cap), one squashed or
// rebased rather than merged, one fast-forwarded with no merge commit at
// all. The caller falls back to the plain comparison, which is what this
// view showed before.
func forkPointBeforeMerge(client *github.RESTClient, repo model.RepoRef, base, head, headSHA string) string {
	if headSHA == "" {
		return ""
	}
	// head...base: the commits base carries that head does not, oldest
	// first, which is where the merge that brought head in must be.
	landed, err := client.CompareRefs(repo.Owner, repo.Name, head, base)
	if err != nil {
		return ""
	}
	for _, commit := range landed.Commits {
		if len(commit.Parents) < 2 || !slices.Contains(commit.Parents, headSHA) {
			continue
		}
		for _, parent := range commit.Parents {
			if parent == headSHA {
				continue
			}
			forked, err := client.CompareRefs(repo.Owner, repo.Name, parent, head)
			if err != nil {
				return ""
			}
			return forked.MergeBase
		}
	}
	return ""
}

// cycleTimes is this process's record of its own RunCycle ticks -- how
// long each took, and how far into each one the dispatch decision was
// reached (orchestrator.CycleTimes). GET /api/metrics serves it as the
// "cycles" section beside the runs one, which is where it answers the
// question that report otherwise only raises: a queue wait looks
// identical whether the deployment was at max_concurrent or whether
// there was room all along and the tick was slow to get to dispatch.
//
// Package-level for the same reason reconcilerDown and livePullRequests
// above are: the UI/API server starts before runDaemon builds its Deps,
// so the two halves need one thing they can both name. Unlike those two
// it needs no gate -- it is a value, allocated at process start, and
// reading it before the reconcile loop has ticked reports no ticks,
// which is exactly true of a deployment whose loop has not started (or
// has died: see reconcilerDown).
var cycleTimes = orchestrator.NewCycleTimes(orchestrator.DefaultCycleHistory)

// hostLoad is this process's record of the machine it is running on: one
// reading a minute, folded into hourly rollups in a bounded ring
// (sysstat.History). GET /api/metrics serves it as the "host" section,
// which is what every duration in that report has to be read against -- a
// CI job that took twice as long did not get slower, it got less of a
// machine, and no measurement of the job can say which.
//
// Package-level for cycleTimes' reason exactly: the sampler and the
// UI/API server are started in different places and need one thing they
// can both name. It costs nothing to allocate and reports an empty history
// until the sampler has run, which is true of a deployment whose sampler
// has not started.
var hostLoad = sysstat.NewHistory(sysstat.DefaultHistoryHours)

// hostLoadInterval is how often that sampler takes a reading. A minute is
// inside the shortest thing worth correlating with -- a load average is
// itself a one-minute mean -- and is as close to free as a measurement
// gets, sixty times an hour: on Linux, one read of three files in /proc
// plus the 100ms sysstat.Read spends sampling CPU utilization; on macOS, a
// few sysctls plus the one second its iostat sample takes, because macOS
// iostat counts its interval in whole seconds and the cumulative per-state
// CPU ticks are not reachable without cgo. A second a minute is still
// under two hundredths of one core. The host is the thing being measured
// here, so a sampler that cost anything measurable would be defeating its
// own purpose.
const hostLoadInterval = time.Minute

// sampleHostLoad folds a reading of this machine into hostLoad every
// hostLoadInterval until ctx is done.
//
// A failed reading is dropped rather than logged: on a platform sysstat
// cannot read at all -- anything but Linux and macOS -- every reading
// fails, and a line a minute about it forever would be worse than the
// missing series, which the report already reports as a history that does
// not reach back. The first failure is worth one line, though, because a
// deployment whose host readings have stopped is a deployment whose whole
// tuning story is about to go quiet.
func sampleHostLoad(ctx context.Context, into *sysstat.History) {
	ticker := time.NewTicker(hostLoadInterval)
	defer ticker.Stop()
	logged := false
	for {
		if snap, err := sysstat.Read(); err == nil {
			into.Add(time.Now(), snap)
		} else if !logged {
			logged = true
			log.Printf("grain daemon: reading this host's own load: %v -- "+
				"the metrics report's host series will be empty on this machine", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cycleWakes is how the UI/API server asks the reconcile loop to run a
// cycle *now* rather than at its next tick -- one buffered slot, written
// without blocking and drained by reconcile below.
//
// It exists for the one thing on this deployment somebody sits in front
// of. A reply to a parked task un-parks it (ui.Client.AddComment) and is
// then dispatched by whichever cycle next reaches the dispatch decision,
// so the whole of a reply's wait is the remainder of -poll-interval:
// 29.6-29.9s of the 29.9s a measured director exchange took at the
// shipped 30s, against 0.04-0.44s for everything grain itself does
// between the dispatch decision and the agent's first turn
// (docs/hand-runs/2026-09-08-director-latency.md). It is the largest
// single term in the wait and the only one that buys nothing: the tick
// is a poll for work nobody has announced, and a reply is an
// announcement.
//
// One slot, and a send that gives up when it is full, is the whole of
// the coalescing: what a wake asks for is "a cycle, soon", and two
// replies arriving between two cycles want one cycle, not two. reconcile
// spaces them out further (minWakeInterval), so a burst of replies
// cannot turn the loop into a spin.
//
// Package-level for the reason cycleTimes above is: the UI/API server
// starts before runDaemon builds its Deps, so the two halves need one
// thing they can both name. It needs no gate either -- a wake written
// before the loop has started, or after it has stopped, sits in the
// buffer or is dropped, and neither is a failure. Nothing is lost by a
// dropped wake beyond the promptness it was asking for: the ticker is
// still what guarantees the work happens at all, which is what keeps
// this an optimization rather than a second dispatch path.
var cycleWakes = make(chan struct{}, 1)

// wakeCycle is cycleWakes as ui.Config.Wake takes it: a non-blocking
// send, safe to call from any request handler.
func wakeCycle() {
	select {
	case cycleWakes <- struct{}{}:
	default:
	}
}

// sandboxRecreations is the set of live runs that can be asked to throw
// their sandbox away and start again in a clean one -- the daemon side
// of pkg/mcp's recreate_sandbox, reached over
// POST /api/tasks/{id}/sandbox/recreate.
//
// Package-level for the reason cycleTimes above is, and with the same
// consequence: the UI/API server starts before runDaemon builds its
// Deps, so the two halves need one object they can both name. It needs
// no gate the way livePullRequests does, because it is not a client to
// be built later but a registry that is empty until runs put themselves
// in it -- and a request arriving before the reconcile loop has started
// finds no live run, which is exactly true of a deployment that has
// dispatched nothing.
var sandboxRecreations = orchestrator.NewSandboxRecreations()

// standingSandboxes supervises this deployment's standing sandboxes (the
// grain/config standing_sandbox tables): the reconcile loop's
// "standing-sandboxes" step launches and restarts them through it, and the
// UI reports what it holds on GET /api/sandboxes.
//
// Package-level for sandboxRecreations' reason -- the two halves are
// built in different functions and must name one supervisor, or the pane
// would report a supervisor nothing reconciles. A request before the
// first reconcile finds nothing tracked, which is true.
var standingSandboxes = orchestrator.NewStandingSandboxSupervisor(nil)

// standingSandboxStatuses is ui.Config.StandingSandboxes over
// standingSandboxes, converted field for field for sandboxRecreateAdapter's
// reason. It copies the name and image out of each definition and nothing
// else of it, so neither its env nor the secrets it names reach the UI.
func standingSandboxStatuses() []ui.StandingSandboxStatus {
	active := standingSandboxes.List()
	if len(active) == 0 {
		return nil
	}
	out := make([]ui.StandingSandboxStatus, 0, len(active))
	for _, sb := range active {
		status := ui.StandingSandboxStatus{
			Name:      sb.Def.Name,
			Image:     sb.Def.Image,
			Sandbox:   sb.Sandbox,
			StartedAt: sb.StartedAt,
			Restarts:  sb.Restarts,
			Failed:    sb.Failed,
			LastError: sb.LastError,
			Failures:  sb.Failures,
		}
		if !sb.NextAttempt.IsZero() {
			next := sb.NextAttempt
			status.NextAttempt = &next
		}
		out = append(out, status)
	}
	return out
}

// runWaitCredits is the set of live runs whose wall-clock deadline can
// still be moved out by the time they spend blocked on CI -- the daemon
// side of pkg/mcp's wait_for_checks credit, reached over
// POST /api/tasks/{id}/wait-credit (orchestrator.RunWaitCredits).
//
// Package-level for sandboxRecreations' reason, and unwired the same way
// for the same one: a report that arrives before the reconcile loop has
// started, or after a run has finished, finds no live run to credit,
// which is the honest answer rather than a failure.
var runWaitCredits = orchestrator.NewRunWaitCredits()

// taskCancellers is the set of live runs that can be cancelled when a user
// clicks Stop in the UI (orchestrator.TaskCancellers).
var taskCancellers = orchestrator.NewTaskCancellers()

// consoleSessions is this deployment's live interactive terminals: where
// a console task's run registers the agent CLI it started on a
// pseudo-terminal, and where the UI's own /api/console routes find the
// one somebody is asking to type into (pkg/console).
//
// Package-level for sandboxRecreations' reason -- the reconcile loop
// writes it and the UI server reads it, and the two are built in
// different functions here -- and unwired the same way for the same one:
// a request that arrives before any console task has been dispatched
// finds no session, which is exactly true.
var consoleSessions = console.NewManager()

// agentPause is the gate an agent's own usage limit closes, one window
// per credential -- per model connection owning a key: while one is
// shut, nothing is dispatched onto that credential and every run in
// flight on it has been cancelled, because each of them would spend the
// same exhausted key (orchestrator.Pause). Other connections, and other
// teams, keep running.
//
// Package-level for the reason cycleTimes and sandboxRecreations above
// are: the UI/API server starts before runDaemon builds its Deps, and
// both halves need to name the same gate -- the loop to consult and
// close it, the server to report it on GET /api/config and
// GET /api/pause and to lift it on DELETE /api/pause. It needs no gate
// of its own like livePullRequests: the zero value is a usable, unpaused
// Pause, and a UI reading it before the reconcile loop exists reports
// nothing paused, which is exactly true of a deployment that has
// dispatched nothing.
var agentPause = &orchestrator.Pause{}
