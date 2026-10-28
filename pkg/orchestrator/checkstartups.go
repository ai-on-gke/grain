package orchestrator

// checkstartups.go answers, at the one moment grain has a reason to ask,
// the question the merge queue's per-pull-request handling of a startup
// failure cannot reach: is this the repository rather than the change.
//
// GitHub concludes a workflow run `startup_failure` when it fails before
// any job of it begins, and a job it creates and then refuses to start
// leaves a run concluded plain `failure` (github.JobLog.NoJobStarted).
// Every cause of either is a fact about the repository rather than about
// the tree: a workflow file GitHub cannot parse, Actions disabled or
// restricted, no runner the repository may use, an account out of
// minutes. escalateStartupFailure already recognises that shape on one
// pull request and hands it to a person rather than spending the pull
// request's one automatic repair proving that no commit clears it -- but
// it recognises it *on one pull request*, so a repository in this state
// produces one "this pull request is failing" comment per task and
// nowhere the sentence an operator actually needs: nothing here has run
// for anybody, and one setting clears all of them.
//
// So grain asks. github.RESTClient.RecentRunStarts reads what the
// repository as a whole has been running lately and reports whether any
// of it got a job started; the answer goes into the escalation this
// package was about to write anyway, and into the journal. Both
// directions of it are worth saying, which is why the read is worth
// making at all: "none of the last twenty runs here started a job" tells
// a reader to stop looking at branches, and "other runs here do start
// jobs" rules out the two causes that are about the account and points
// at the workflow file itself.
//
// # Why nothing is remembered between cycles, and what is remembered
// within one
//
// The obvious shape for this is a record: count the escalations per
// repository, keep them somewhere, report the total. grain/task-238 built
// exactly that and the shape is what was wrong with it -- an in-process
// map re-reported on every restart, or a marker file per repository
// grain would then be keeping opinions in about repositories nobody said
// it owned, with no pane and no settings file of its own to point the
// reader at. Both are bookkeeping in place of an answer.
//
// There is nothing here to remember *across* cycles. The verdict is a
// fact about the repository right now, GitHub holds it, and it costs one
// API read -- so it is asked rather than accumulated, it cannot go stale,
// it cannot survive the condition it describes, and nothing has to be
// dismissed when somebody turns Actions back on. The count of pull
// requests it has cost is not kept either: it is not what a reader needs
// (the verdict is true of every pull request against the repository,
// named or not), and where somebody does want it, the queue itself
// already holds it -- those tasks are blocked, in one repository, each
// carrying this comment.
//
// What was wrong was narrower than that, and is what repoFinder fixes:
// the answer was asked for *per pull request*, at the end of a diagnosis
// each entry had to make for itself, and thrown away in between. So four
// entries in one repository each spent a base merge, an agent run and a
// full CI suite arriving at a conclusion grain had already reached and
// written down in the first one's comment thread (grain/task-291, and
// the gke-labs/kontur queue of 2026-09-18 that produced it). repoFinder
// is that conclusion held for the length of one reconciler cycle: asked
// once per repository, read by every entry in it, and gone by the next
// cycle, which asks again. That is enough for both things the record
// was wanted for -- no entry rediscovers it, and every entry's row can
// say it -- with none of what a persisted finding would owe: no
// staleness, no dismissal, no settings pane for a fact nobody chose.
//
// docs/repository-checks-never-started.md is the whole argument, with
// what was deliberately not built.

import (
	"fmt"
	"log"

	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// repoFinder is one reconciler cycle's memory of what it has concluded
// about a repository's CI: whether anything pushed to a branch there can
// reach the checks at all.
//
// One instance per SyncPullRequests cycle, created beside the merge walk
// and handed to every entry. Its whole job is that the read below
// happens once per repository rather than once per pull request -- see
// the section above on why it is a cycle and not a record.
//
// Reads are lazy: a repository whose entries are all green never has the
// question asked about it, because nothing about it is being decided.
// The answer is memoised either way, negative included, so a repository
// with an ordinary broken branch pays one read for a cycle and not one
// per broken branch.
type repoFinder struct {
	client github.Client
	seen   map[model.RepoRef]github.RunStarts
}

func newRepoFinder(client github.Client) *repoFinder {
	return &repoFinder{client: client, seen: map[model.RepoRef]github.RunStarts{}}
}

// starts is what repo's own recent Actions history says about whether
// anything there gets a job started, read at most once per cycle.
//
// A nil finder answers the zero RunStarts, which every reader renders as
// grain having nothing to say. That is the right answer for the one
// caller that has no finder -- a test driving syncEntry directly -- and
// it keeps this a question that can always be asked rather than one
// every call site has to guard.
func (f *repoFinder) starts(repo model.RepoRef) github.RunStarts {
	if f == nil {
		return github.RunStarts{}
	}
	if starts, asked := f.seen[repo]; asked {
		return starts
	}
	starts := repositoryRunStarts(f.client, repo)
	f.seen[repo] = starts
	return starts
}

// unfixable is the same finding in the form the merge queue's decisions
// are made on (forge.Unfixable): which of this repository's checks
// nothing pushed to a branch turns green, and why in words.
//
// declared is the repository's own list of checks that never pass here
// whatever is pushed -- model.RepoConfig.UnfixableChecks, a CLA bot and
// its kind -- which needs no read and is true whether or not Actions is
// working. The two compose: an operator's standing declaration and
// grain's own reading of what the repository has been running lately are
// different facts, and either one on its own is enough to make a repair
// pointless.
func (f *repoFinder) unfixable(repo model.RepoRef, declared []string) forge.Unfixable {
	u := forge.Unfixable{Checks: declared}
	starts := f.starts(repo)
	if starts.ChecksNeverStarted() {
		u.ChecksNeverStart = true
		u.Why = fmt.Sprintf("of the %s in %s, not one got a job as far as running a step",
			recentRunCount(starts.Finished), repo)
	}
	return u
}

// repositoryRunStarts reads what repo's own recent Actions runs did, for
// the escalation about to be written about one of them.
//
// Best effort, and silent about most of what can go wrong: the caller is
// in the middle of blocking a merge-queue head and telling a person why,
// and none of that changes with this answer. A credential that may not
// read Actions, a repository whose CI is not Actions at all, a GitHub
// that is briefly unreachable -- each of them lands here as the zero
// RunStarts, which every reader below renders as saying nothing rather
// than as a claim about the repository.
//
// The error is logged rather than swallowed, because one of its causes
// is a lasting one worth a line in the journal: a token whose Actions
// scope is missing makes every escalation from now on a little less
// useful than it could be, and nothing else would ever say so.
func repositoryRunStarts(client github.Client, repo model.RepoRef) github.RunStarts {
	starts, err := client.RecentRunStarts(repo.Owner, repo.Name, github.RunStartsRead)
	if err != nil {
		log.Printf("orchestrator: could not read what %s has been running lately (%v), so the "+
			"escalation about to be written cannot say whether this failure is the "+
			"repository's or this branch's. Reading it needs Actions read access to %s.",
			repo, err, repo)
		return github.RunStarts{}
	}
	return starts
}

// repositoryStartupNote is the paragraph escalateStartupFailure adds
// about the repository, or "" when grain learned nothing worth saying.
//
// Two things are worth saying and they are opposites, which is the point
// of making the read at all:
//
//   - Nothing in this repository has got a job started. Then the reader
//     should stop reading branches: every pull request open here is
//     failing the same way, and one setting change clears all of them.
//   - Other runs here do get jobs started. Then Actions is on, the
//     account has minutes and a runner exists, so two of the four causes
//     the comment has just listed are ruled out -- which is worth more
//     than the list, because the list is what a reader has to work
//     through by hand.
//
// Silence otherwise, and silence is the common case for a repository
// whose CI is not Actions, a credential that cannot read runs, and a
// repository with no finished runs at all. None of those is evidence of
// anything, and a comment that guessed from them would be sending
// somebody to look at settings on grain's word alone.
func repositoryStartupNote(repo model.RepoRef, starts github.RunStarts) string {
	if starts.ChecksNeverStarted() {
		// The run page is deliberately not repeated here. This pull
		// request's own run is already linked above, GitHub writes the
		// same repository-level reason on every one of them, and a
		// second link to a second run of the same failure is one more
		// thing for a reader to open before they can act.
		return fmt.Sprintf("\n\nThis is not a fact about this pull request, and grain has "+
			"checked rather than guessed: of the %s in %s, not one got a job as far as "+
			"running a step. Every pull request open against this repository is failing "+
			"this way and none of them can be repaired from a branch -- so this comment "+
			"is on this one only because it happened to reach the front of the queue. "+
			"Whoever changes the setting behind it clears all of them at once, with no "+
			"push: each merges as soon as it reads clean.",
			recentRunCount(starts.Finished), repo)
	}
	if starts.Started > 0 {
		evidence := fmt.Sprintf("%d of the %s in %s did get a job started",
			starts.Started, recentRunCount(starts.Finished), repo)
		if starts.Finished == 1 {
			evidence = fmt.Sprintf("the one finished Actions run in %s did get a job started", repo)
		}
		return fmt.Sprintf("\n\nTwo more of those causes are ruled out, and grain has "+
			"checked rather than guessed: %s, so Actions is not switched off for this "+
			"repository and the account behind it has minutes to spend. What is left is "+
			"the workflow file this run comes from -- as it stands on the base branch, "+
			"not in this pull request -- or a runner that particular workflow asks for "+
			"and cannot have.", evidence)
	}
	return ""
}

// recentRunCount names how much history the verdict was read from, in a
// clause that fits both the sentences above and the journal line below --
// "the last 20 finished Actions runs", or "the one finished Actions run"
// for a repository that has run exactly one thing, where "the last 1" is
// the sort of sentence that makes a reader doubt everything around it.
func recentRunCount(finished int) string {
	if finished == 1 {
		return "one finished Actions run"
	}
	return fmt.Sprintf("last %d finished Actions runs", finished)
}

// noteRepositoryChecksNeverStarted puts the verdict in the journal as
// well as on the pull request.
//
// The pull request is where the person who filed this task is reading,
// and the journal is where the person running the deployment is: a
// repository in this state blocks every task grain has against it, which
// is a deployment-level event however locally it is discovered. One line
// per escalation, which is one line per pull request the condition has
// cost -- rare by construction, since each of those pull requests is
// escalated exactly once.
//
// Called where the escalation is written rather than where the read is
// made, and that distinction is newer than this comment: repoFinder asks
// the question for every red entry in a repository now, once per cycle,
// and a journal line per cycle for a condition that lasts days would
// bury everything else in it. The escalations are still one apiece.
//
// Nothing is logged when grain has no verdict, or when the repository's
// runs do start jobs: that is the ordinary state of every repository, and
// a per-escalation line saying so would be noise in the one place a
// person goes looking for the sentence above.
func noteRepositoryChecksNeverStarted(repo model.RepoRef, starts github.RunStarts) {
	if !starts.ChecksNeverStarted() {
		return
	}
	log.Printf("orchestrator: of the %s in %s, not one got a job as far as running a step, so "+
		"every pull request grain has open there is blocked on the repository rather than on "+
		"its own tree, and no commit anybody pushes clears it. Actions disabled or restricted "+
		"for the repository, a runner it may not use, a workflow file GitHub cannot parse, or "+
		"an account out of Actions minutes -- GitHub's own reason is on %s. Every pull request "+
		"there merges on its own once the setting changes.",
		recentRunCount(starts.Finished), repo, startupRunPage(starts))
}

// startupRunPage is the run page to send a reader to, or a plain sentence
// where there is none: a link is the only thing that reaches GitHub's own
// wording, and "" in the middle of a log line reads as a bug.
func startupRunPage(starts github.RunStarts) string {
	if starts.RunURL == "" {
		return "one of those runs' own pages on github.com"
	}
	return starts.RunURL
}
