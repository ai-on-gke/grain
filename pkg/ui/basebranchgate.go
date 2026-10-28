package ui

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A base branch nothing has judged is a control that is off, and until
// this file nothing anybody looks at said so (grain/task-453).
//
// On 2026-09-19 at 07:22 UTC release workflows were switched off on this
// deployment's own repository with no judging jobs named beside them,
// which at the time meant grain started no base-branch run at all. The
// last run that judged `main` had started at 07:32. Roughly a hundred
// merges landed over the following 33 hours, the merge queue went on
// merging batch after batch onto a branch nothing had tested, and the
// only thing that ever said so was a log line the reconciler wrote once
// a cycle. It was found by a Director session reading `git log` on the
// settings repository.
//
// # That particular way in has since been closed, and this is still the point
//
// grain/task-458 and grain/task-461 took the release switch out of the
// decision entirely: every base branch is now judged by an untrusted
// push of the repository's own push plan, narrowed only if
// model.RepoConfig.BaseBranchCIJobs names jobs, and empty means "the
// whole push plan" rather than "nothing". So the exact condition above
// is no longer reachable, and this file does not look for it -- warning
// about releases being off would now be a false alarm on a repository
// whose gate is perfectly healthy.
//
// What is not fixed by that, and what this reading is for, is the blind
// spot rather than the cause. A branch can stop being judged for reasons
// no settings page shows:
//
//   - the seeding refusal that is left (orchestrator.baseCINotEnabled):
//     an operator ticks "watch the default branch" on a repository grain
//     CI is not enabled for, and nothing is ever enrolled. The
//     reconciler says so once, with sayOnce, and never again;
//   - a run that never concludes. One run judges a branch at a time
//     (grain/task-334), so a row naming a run with no verdict is a branch
//     nothing will start another run for -- and from every other surface
//     it looks exactly like a branch being tested right now;
//   - a merge that has landed and had no run started for it at all,
//     which is what a wedged cycle, a paused pipeline or a daemon that
//     is not running looks like from here;
//   - a run that started, finished, and judged nothing -- a repository
//     none of whose `.grain/workflows` answer `push` has a base-branch
//     run made of no job at all, over in seconds, holding nothing
//     (grain/task-508). It is the same hazard as the incident above
//     wearing a green tick: until grain/task-508 such a run was recorded
//     as a *success*, because ci.PlanRun keeps "this repository declares
//     no workflow" as a pass on purpose, and a gate that passes over
//     every tree whatever is indistinguishable here from one that works.
//     A run grain could not even ask for arrives as the same finding
//     (grain/task-509): a base-branch job list naming jobs that no
//     longer exist is refused by the planner, so there is no run to
//     conclude and, until that refusal was written down, nothing to
//     read -- the reconciler asked, was refused and logged it, once a
//     cycle, for ever.
//
// All four arrive as the sentence the incident above needed and nobody
// had: **no base-branch run has started for `main` since <when>**. That
// is the fact a reader needs before they need the reason, and it does
// not depend on grain having correctly diagnosed itself -- which is the
// property that makes it worth keeping now that the one cause anybody
// had predicted is gone. The data was on the row the merge queue already
// reads the whole time (model.BaseBranchCI.StartedAt, replaced each time
// a run starts there).
//
// It is said where a person is: on the deployment health response GET
// /api/sandboxes carries -- which is also what read_deployment_health
// hands an agent -- and on the repository's own release pane
// (RepoReleaseSettings.BaseBranchGate). The same shape as the sandbox
// guest reading next door (sandbox_guest.go), for the same reason and
// out of the same incident: a fact three readings away from its
// consequence, written out in the sentence that was missing.
//
// # What it does not do
//
// It judges nothing and starts nothing. Every finding here is read off
// state grain has already written, on a poll that was being made anyway,
// and the repair in each case is an operator's: grant the capability, or
// go and look at the run that has not finished.
//
// It also reports nothing about a repository it has no record of. A
// merge into a repository with no grain-ci grant writes no row at all
// (orchestrator.recordPostMergeCI), so if its default branch is not
// watched either there is nothing in the store to walk and no honest
// finding to make. Watching the default branch is what makes that
// repository's silence visible here.

const (
	// baseBranchRunOverdue is how long a base branch may carry a merge
	// nothing has judged before that is worth reporting. The reconciler
	// starts a run on the cycle after a merge moves the branch, so the
	// honest bound is minutes; two hours is slack for a deployment whose
	// cycle is wedged, paused, or waiting behind another branch's run,
	// and is still an order of magnitude below the 33 hours this went
	// unnoticed for.
	baseBranchRunOverdue = 2 * time.Hour
	// baseBranchRunUnfinished is how long a recorded run may go without a
	// conclusion before the row it is on is reported as stuck. A
	// base-branch run is a full push suite -- the best part of an hour on
	// this deployment -- and ci.DefaultJobTimeout cancels a job at 36
	// minutes, so three hours is past every way one of these legitimately
	// takes a long time and is only reached by a run whose conclusion is
	// never coming.
	baseBranchRunUnfinished = 3 * time.Hour
)

// The words BaseBranchGate.Reason is one of. They name the state rather
// than the repair, because two of them have the same repair and a caller
// filtering on "which of these is a settings problem" should not have to
// read prose to find out.
const (
	// baseGateNotEnabled: grain CI is not enabled for this repository, so
	// the default branch it is watching will never be enrolled
	// (orchestrator.baseCINotEnabled, orchestrator.seedWatchedBaseBranch).
	// Since grain/task-461 this is the only settings-shaped way a base
	// branch goes unjudged, and it is only ever reported for a branch
	// grain has no row for: once a row exists the row is itself the
	// opt-in, and the reconciler starts runs for it whatever the
	// deployment's default capability set later says.
	baseGateNotEnabled = "grain-ci-off"
	// baseGateRunUnfinished: the run this branch's row names has recorded
	// no conclusion, and one run judges a branch at a time, so nothing
	// else starts here until it does.
	baseGateRunUnfinished = "run-unfinished"
	// baseGateNoRunStarted: a merge has landed on this branch, nothing is
	// in flight for it, and no run has started in baseBranchRunOverdue.
	// Whatever the reason, the queue is merging onto a commit nothing has
	// judged.
	baseGateNoRunStarted = "no-run-started"
	// baseGateJudgedNothing: a run did start, and finished without
	// judging the tree -- errored, which is grain's word for a verdict
	// about nothing at all (orchestrator.collectBaseBranchCI). grain
	// re-asks a few times at ciRetryDelay apart and then stops, so past
	// baseBranchRunOverdue this is a branch whose gate has run and
	// decided nothing and will not be asked again until somebody merges.
	//
	// This is the finding grain/task-508 went looking for across this
	// deployment's repositories: a gate that runs no test. The commonest
	// way in is a repository with no `.grain/workflows` answering `push`
	// at all, whose base-branch run matches nothing and is over in
	// seconds. The other three are a deployment whose CI cannot get a job
	// onto a machine, a run that keeps being lost with its daemon, and a
	// run grain asked for and was refused -- a job list naming jobs the
	// workflows no longer declare (orchestrator.refuseBaseBranchCI,
	// grain/task-509), which leaves no run at all. All four are "nothing
	// judged this tip", which is what the reader needs before they need
	// which one it was; RunDetail is which one it was.
	baseGateJudgedNothing = "judged-nothing"
)

// BaseBranchGate is what is judging one repository's base branch -- or,
// when there is a row here at all, what is not.
//
// One per (repo, branch) grain is watching, and only for the branches
// with something wrong: a deployment whose gates are all working reports
// none of these, which is what keeps the health response and the
// repository pane quiet on the ordinary day. A reader who wants to know
// which jobs judge a healthy branch reads
// RepoReleaseSettings.BaseBranchJobs, which says so whether or not
// anything is wrong.
type BaseBranchGate struct {
	// Repo is the repository, as `owner/name`, and Branch the base branch
	// this is about -- empty where grain is watching a repository's
	// default branch but has never resolved it, which is the one state
	// with no branch name to give.
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	// Judged is whether grain will start a base-branch run here at all.
	// False is the state this file exists for: not a run that failed, but
	// no run, and a merge queue going on merging onto the result.
	Judged bool `json:"judged"`
	// Reason is which state this is, in one of the words above.
	Reason string `json:"reason"`
	// LastRunAt is when a base-branch run last *started* for Branch
	// (model.BaseBranchCI.StartedAt), and LastRunAgeMS how long ago that
	// is as of this reading. Both absent for a branch grain has never
	// started one for -- a seeded row, or a first merge's -- which the
	// detail says in those words rather than as a zero timestamp.
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
	LastRunAgeMS int64      `json:"lastRunAgeMS,omitempty"`
	// RunInFlight is whether the row names a run that has not concluded,
	// and MergesWaiting whether a merge has landed that no run has judged
	// yet (model.BaseBranchCI.PendingTaskID, which empties as a run takes
	// it up). Read together they are the difference between a branch
	// nobody has pushed to and a branch the queue is piling onto.
	RunInFlight   bool `json:"runInFlight"`
	MergesWaiting bool `json:"mergesWaiting"`
	// RunID is the run this finding is about, for the reader to go and
	// look at: the one that has not finished, or the one that finished
	// having judged nothing. Empty on the findings that are about there
	// being no run -- including a judged-nothing row where the run was
	// never started at all, because grain asked for one and was refused
	// (orchestrator.refuseBaseBranchCI).
	RunID string `json:"runID,omitempty"`
	// RunDetail is what grain recorded that run as
	// (model.BaseBranchCI.Detail), carried because on a
	// baseGateJudgedNothing row it is the whole diagnosis -- "grain CI
	// ran no job at all" is a repository with no suite, "could not run N
	// job(s)" is a deployment with nowhere to run one, and "could not be
	// asked for a run" is a request grain CI refused, which names the
	// setting that refused it. Empty on every other finding, where the
	// reason already says it.
	RunDetail string `json:"runDetail,omitempty"`
	// Detail is the finding in the words a person needs, because this is
	// a fact three readings away from its consequence and nobody
	// assembled it from the fields alone the first time -- the same
	// contract SandboxGuestPublishing.Detail keeps next door.
	Detail string `json:"detail"`
}

// baseBranchGates is the reading GET /api/sandboxes carries, and through
// it read_deployment_health: every base branch of every watched
// repository that nothing is judging, or that nothing has judged for
// longer than it should have been.
//
// nil on a UI with no store behind it and on a reading that could not be
// taken, which are both "no line in the pane" rather than a claim that
// every gate is working -- the nil-means-unavailable contract the rest of
// that response keeps.
func (c *Client) baseBranchGates(ctx context.Context) []BaseBranchGate {
	if c.Store == nil {
		return nil
	}
	rows, err := c.Store.ListBaseBranchCI(ctx)
	if err != nil {
		log.Printf("ui: listing watched base branches for the base-branch gate reading: %v", err)
		return nil
	}
	configs, err := c.Store.ListRepoConfigs(ctx)
	if err != nil {
		log.Printf("ui: reading repository settings for the base-branch gate reading: %v", err)
		return nil
	}
	byRepo := map[string]model.RepoConfig{}
	for _, cfg := range configs {
		byRepo[cfg.Repo.String()] = cfg
	}
	now := c.now()
	var out []BaseBranchGate
	watched := map[string]bool{}
	for i := range rows {
		row := rows[i]
		watched[row.Repo.String()] = true
		cfg := byRepo[row.Repo.String()]
		if baseCheckSwitchedOff(&cfg, row.Repo) {
			// Unjudged on purpose (model.BaseBranchCIItem, grain/task-3332):
			// an operator said so, and an alarm about it would be the
			// false alarm this reading is careful not to raise.
			continue
		}
		if gate, ok := baseBranchGateOf(row.Repo, &row,
			grainci.RepoUsesGrainCI(&cfg, row.Repo), now); ok {
			out = append(out, gate)
		}
	}
	// A repository whose default branch grain has been told to watch but
	// has never enrolled has no row to read, and the reason it has none
	// may be the very refusal this reading is about
	// (orchestrator.seedWatchedBaseBranch, which asks the same policy
	// before it enrols anything). Reported with no branch name, since
	// resolving one is what has not happened.
	for _, cfg := range configs {
		if !cfg.WatchDefaultBranch || watched[cfg.Repo.String()] || baseCheckSwitchedOff(&cfg, cfg.Repo) {
			continue
		}
		if gate, ok := baseBranchGateOf(cfg.Repo, nil,
			grainci.RepoUsesGrainCI(&cfg, cfg.Repo), now); ok {
			out = append(out, gate)
		}
	}
	// A stable order, so a pane redrawing on every poll does not shuffle
	// its own alerts under the reader's cursor.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Branch < out[j].Branch
	})
	return out
}

// baseCheckSwitchedOff reports whether this deployment has said it does
// not run repo's base-branch check -- the orchestrator's baseBranchCIOff,
// asked of a row already read: the item, and never for the deployment's
// own configuration repository, which is judged whatever it says.
func baseCheckSwitchedOff(cfg *model.RepoConfig, repo model.RepoRef) bool {
	return !grainci.RequiredFor(repo) && !model.RunsWorkItem(cfg, model.BaseBranchCIItem)
}

// BaseBranchGates is GET /api/base-branch-gates' whole answer: the same
// findings the health response carries, on an endpoint the app itself
// can afford to ask for.
//
// It exists because the inbox needs them (grain/task-470) and the
// reading they already had is not one the app may poll. GET
// /api/sandboxes probes every live guest to answer, which is why nothing
// outside the System pane asks for it; this is three store reads and no
// probe of anything, so the rail can carry the count in every view.
type BaseBranchGates struct {
	// Gates is every watched base branch with something wrong with it,
	// in baseBranchGates' own order. Never null, so a caller can iterate
	// it without checking -- and empty on a deployment whose gates are
	// all working, which is the ordinary day.
	Gates []BaseBranchGate `json:"gates"`
}

// handleListBaseBranchGates answers GET /api/base-branch-gates.
//
// A reading that could not be taken answers as an empty list rather than
// as an error, which is the contract the same reading keeps on the
// health response: baseBranchGates has already logged whatever went
// wrong, and a poll that turned a store hiccup into a banner every three
// seconds would be worse than one line missing from the inbox until the
// next tick.
func (s *Server) handleListBaseBranchGates(w http.ResponseWriter, r *http.Request) {
	gates := s.tasks.baseBranchGates(r.Context())
	if gates == nil {
		gates = []BaseBranchGate{}
	}
	writeJSON(w, http.StatusOK, BaseBranchGates{Gates: gates})
}

// baseBranchGate is the same reading for one repository, for its own
// release pane to put beside the switch that is usually the cause. nil
// when there is nothing wrong with that repository's gate, which is one
// alert fewer rather than a reassurance.
//
// Where a repository has several watched base branches the worst one is
// reported: an unjudged branch before a late one, and the longest wait
// among equals. The pane is a paragraph beside a switch rather than a
// table, and the branch a reader has to act on first is the one it should
// be about.
func (c *Client) baseBranchGate(ctx context.Context, repo model.RepoRef) *BaseBranchGate {
	var worst *BaseBranchGate
	for _, gate := range c.baseBranchGates(ctx) {
		if gate.Repo != repo.String() {
			continue
		}
		if worst == nil || worseBaseBranchGate(gate, *worst) {
			found := gate
			worst = &found
		}
	}
	return worst
}

// worseBaseBranchGate is which of two of one repository's findings a
// reader has to act on first: a branch nothing judges at all before one
// that is merely late, and among two of a kind the one that has been
// waiting longer.
func worseBaseBranchGate(a, b BaseBranchGate) bool {
	if a.Judged != b.Judged {
		return !a.Judged
	}
	return a.LastRunAgeMS > b.LastRunAgeMS
}

// baseBranchGateOf reads one watched branch's gate. ok is false when
// there is nothing to report about it, which is every branch on a
// deployment whose merge queue is merging onto tested trees.
//
// row is nil for a repository whose default branch is watched and has
// never been enrolled; ciEnabled is grain CI's capability resolved for
// this repository, which only decides anything in that case -- once a row
// exists the row is the opt-in, and the reconciler goes on judging the
// branch whatever the default capability set later says
// (orchestrator.reconcileOneBaseBranchCI's own note on that).
//
// The repository's own configuration is deliberately not a parameter.
// Everything this used to read off it was the release switch, and since
// grain/task-461 that switch says nothing about whether a base branch is
// judged -- a finding drawn from it would be a warning about a healthy
// repository.
func baseBranchGateOf(repo model.RepoRef, row *model.BaseBranchCI,
	ciEnabled bool, now time.Time) (BaseBranchGate, bool) {

	gate := BaseBranchGate{Repo: repo.String(), Judged: true}
	var age time.Duration
	// judgedNothing: nothing about the tree was decided here. Read off the
	// conclusion rather than off the run, because the run is long gone by
	// the time anybody looks -- grain records a base-branch run that
	// decided nothing as errored, including one made of no job at all
	// (orchestrator.collectBaseBranchCI).
	//
	// A row carrying that conclusion and no run id at all is the same
	// finding rather than a different one: grain asked for a run and was
	// refused, so there was nothing to conclude
	// (orchestrator.refuseBaseBranchCI, grain/task-509). The reader needs
	// "nothing judged this tip" before they need which of the ways it
	// was, and RunDetail below is what tells them.
	judgedNothing := false
	if row != nil {
		gate.Branch = row.Branch
		gate.RunInFlight = row.RunID != "" && row.Conclusion == ""
		gate.MergesWaiting = row.PendingTaskID != ""
		judgedNothing = row.Conclusion == string(ci.ConclusionErrored)
		if row.RunID != "" && !row.StartedAt.IsZero() {
			// Only a row that names a run has a run to date. The seed a
			// first merge writes carries the moment it was written and no
			// run at all (orchestrator.noteBaseBranchMerge), and dating a
			// run off that would report a branch as judged at a moment
			// nothing judged it.
			at := row.StartedAt
			gate.LastRunAt = &at
			age = max(now.Sub(at), 0)
			gate.LastRunAgeMS = age.Milliseconds()
		} else if !row.StartedAt.IsZero() {
			// No run has ever started here, so the age that matters is
			// how long the branch has been enrolled and unjudged.
			age = max(now.Sub(row.StartedAt), 0)
		}
	}
	// The one settings-shaped way a base branch goes unjudged since
	// grain/task-461, and it only reaches a branch that has never been
	// enrolled. Nothing here reads the release switch: it decides whether
	// a merge also publishes, and has had nothing to do with whether the
	// branch is judged since grain/task-458.
	//
	// The deployment's own configuration repository is exempt because
	// grain judges it without anybody opting it in (grainci.RequiredFor),
	// so there is no capability here to have been left off.
	if row == nil && !ciEnabled && !grainci.RequiredFor(repo) {
		gate.Judged, gate.Reason = false, baseGateNotEnabled
	}
	if gate.Judged {
		switch {
		case gate.RunInFlight && age > baseBranchRunUnfinished:
			gate.Reason = baseGateRunUnfinished
		case judgedNothing && age > baseBranchRunOverdue:
			// Judged is false rather than merely late: a run that decided
			// nothing leaves the branch exactly as unjudged as no run at
			// all, and a reader sorting these (worseBaseBranchGate) has to
			// reach it before a branch that is only overdue.
			//
			// Waited out rather than reported at once because grain
			// re-asks an errored run a few times before it gives up
			// (orchestrator.baseCIRetryDue, four attempts five minutes
			// apart), so under baseBranchRunOverdue this is a deployment
			// having another go rather than a gate that has stopped
			// working. Past it, either the re-asking is finished or it is
			// erroring every time, and both are a branch nothing tested.
			gate.Judged, gate.Reason = false, baseGateJudgedNothing
		case !gate.RunInFlight && gate.MergesWaiting && age > baseBranchRunOverdue:
			gate.Reason = baseGateNoRunStarted
		default:
			// Judged, and nothing has been waiting longer than it should:
			// the ordinary state of a watched branch, and no row here.
			return BaseBranchGate{}, false
		}
	}
	if row == nil && gate.Judged {
		// A repository whose default branch is watched, enabled, and not
		// yet enrolled is a repository the seeder gets to on one of the
		// next few cycles (it enrols one branch per cycle), not a fault.
		return BaseBranchGate{}, false
	}
	if row != nil && (gate.Reason == baseGateRunUnfinished || gate.Reason == baseGateJudgedNothing) {
		gate.RunID = row.RunID
	}
	if row != nil && gate.Reason == baseGateJudgedNothing {
		// The run's own account of why it decided nothing, which is the
		// difference between "this repository has no suite" and "this
		// deployment could not get a job onto a machine" -- two findings
		// with the same shape and completely different repairs.
		gate.RunDetail = row.Detail
	}
	gate.Detail = baseBranchGateDetail(gate)
	return gate, true
}

// baseBranchGateDetail writes the finding out in the sentence that was
// missing -- what is true, what it costs, and what ends it.
//
// It says the branch is not being tested and that the merge queue is
// merging onto it anyway, in those words, because the state it describes
// read for a day and a half as an ordinary setting on a settings pane.
func baseBranchGateDetail(g BaseBranchGate) string {
	branch := g.Branch
	if branch == "" {
		branch = "its default branch"
	}
	var b []string
	switch g.Reason {
	case baseGateNotEnabled:
		// "acme/widgets's base branch main", but "acme/widgets's default
		// branch" where no name was ever resolved, rather than "base
		// branch its default branch".
		subject := g.Repo + "'s base branch " + g.Branch
		if g.Branch == "" {
			subject = g.Repo + "'s default branch"
		}
		b = append(b, "Nothing is testing "+subject+
			", and the merge queue is merging onto it anyway. grain CI is not enabled"+
			" for this repository, so the branch grain has been told to watch is never"+
			" enrolled and no base-branch run is ever started for it -- which leaves"+
			" this repository without the one control that catches two branches that"+
			" are green apart and broken together (docs/merge-queue-behind.md).")
	case baseGateRunUnfinished:
		b = append(b, "The base-branch run judging "+g.Repo+" "+branch+
			" has recorded no verdict. One run judges a branch at a time, so nothing"+
			" else starts here until this one concludes -- the branch's row cannot be"+
			" refreshed, and the merge queue is reading an answer about a tree the"+
			" branch has moved past.")
	case baseGateJudgedNothing:
		if g.RunID == "" {
			// No run to have finished: grain asked for one and was refused
			// (orchestrator.refuseBaseBranchCI). The same finding, and the
			// sentence has to say which of the two it is, because "the run
			// finished without judging the tree" would send a reader
			// looking for a run that does not exist.
			b = append(b, "grain could not start a base-branch run for "+g.Repo+" "+branch+
				" at all, and the merge queue is merging onto it anyway. The request"+
				" itself was refused, so there is no run and no verdict -- which holds"+
				" no merge, because only a failure does.")
		} else {
			b = append(b, "The base-branch run for "+g.Repo+" "+branch+
				" finished without judging the tree, and the merge queue is merging onto"+
				" it anyway. A run that decides nothing holds no merge -- only a failure"+
				" does -- so this branch's gate is running, concluding, and passing over"+
				" whatever is on the branch.")
		}
	case baseGateNoRunStarted:
		b = append(b, "A merge has landed on "+g.Repo+" "+branch+
			" and no base-branch run has been started for it. grain starts one on the"+
			" cycle after a merge moves a watched branch, so a gap this long means"+
			" nothing is starting one -- and whatever the reason, the queue is merging"+
			" onto a commit nothing has judged.")
	default:
		b = append(b, "grain is not judging "+g.Repo+"'s base branch "+branch+".")
	}
	b = append(b, baseBranchGateSince(g))
	switch g.Reason {
	case baseGateNotEnabled:
		// The CI Mode, and not the capability: grain-ci cannot be attached
		// by hand (grainci.Refusal), which is the advice ciNotEnabledMessage
		// stopped giving for the same reason.
		b = append(b, "Setting this repository's CI Mode to \"Use Grain CI\" on its"+
			" page is what enrols the branch; the "+grainci.CapabilityName+" capability"+
			" cannot be attached by hand.")
	case baseGateRunUnfinished:
		if g.RunID != "" {
			b = append(b, "Run "+g.RunID+" is the one to look at; cancelling it lets the"+
				" next cycle start a run against the tip as it now stands.")
		}
	case baseGateJudgedNothing:
		if g.RunDetail != "" {
			b = append(b, "grain recorded it as: "+g.RunDetail+".")
		}
		b = append(b, "A repository whose `.grain/workflows` answer no `push` has a"+
			" base-branch run made of no job at all, which is the commonest way in;"+
			" so is a base-branch job list naming jobs that no longer exist. Both are"+
			" repaired in the repository being judged rather than here.")
	}
	return joinSentences(b)
}

// baseBranchGateSince is the sentence that would have caught this on the
// first day: when a run last started for this branch, and how long ago
// that is.
func baseBranchGateSince(g BaseBranchGate) string {
	branch := g.Branch
	if branch == "" {
		branch = "its default branch"
	}
	if g.LastRunAt == nil {
		return "No base-branch run has ever been started for " + branch + "."
	}
	when := g.LastRunAt.UTC().Format("2006-01-02 15:04 MST")
	since := "The last base-branch run for " + branch + " started " + when +
		", " + baseBranchGateAge(time.Duration(g.LastRunAgeMS)*time.Millisecond) + " ago"
	if g.RunInFlight {
		return since + ", and has not finished."
	}
	if g.MergesWaiting {
		return since + ", and merges have landed since with nothing judging them."
	}
	return since + "."
}

// baseBranchGateAge is a span in the words somebody reading an incident
// uses: hours while it is hours, because "33h" is the whole finding and
// "1d 9h" makes a reader do the arithmetic back again.
func baseBranchGateAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}

// joinSentences puts the pieces of a detail together with the one space
// between them that a paragraph wants, dropping any that came back empty.
func joinSentences(parts []string) string {
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += part
	}
	return out
}
