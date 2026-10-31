// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/authfix"
	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/bootstrap"
	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/capability/deploymenthealth"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/capability/readissues"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// errTaskClosed is what context.Cause(runCtx) reads once
// watchForTaskClosed has cancelled a run -- RunDispatch checks for it by
// identity (errors.Is) to tell "this run was killed because its task got
// closed" apart from any other reason framework.Run might return an
// error, and to record outcome "cancelled" rather than "failed" for
// exactly that case.
var errTaskClosed = errors.New("orchestrator: task closed while its run was still live")

// errRunTimedOut is context.Cause's own report, by identity, for a run
// RunDispatch cancelled itself because it outran cfg.maxRunRuntime() --
// Config.MaxRunRuntime's own doc comment has the reasoning
// (bwsalmon/agents#575). Checked the same way errTaskClosed is, and
// recorded as outcome "cancelled" for the same reason: the run did not
// fail on its own, RunDispatch ended it.
var errRunTimedOut = errors.New("orchestrator: run exceeded its wall-clock time limit")

// errDaemonStopped is context.Cause's report, by identity, for a run
// cancelled because the daemon driving it is stopping -- an upgrade, a
// restart, a SIGTERM. RunDispatch records that as
// model.InterruptedOutcome: nothing about the task earned it, so it takes
// no strike and the task is simply dispatched again, into the
// conversation the run left behind (earlySession).
//
// It is a cause of its own rather than the daemon context's bare
// context.Canceled for the reason errTaskClosed is: whichever cancel
// reaches a run first is what it is recorded as, and only a cause set on
// the run's own context says which that was. RunDispatch's run context
// is therefore detached from the daemon's and cancelled with this when
// the daemon's is (see where it is made).
var errDaemonStopped = errors.New("orchestrator: the daemon driving this run was stopped")

// InterruptedInSetupDetail is the task_run.detail of a run the daemon
// was stopped under while the run was still setting up: its sandbox
// building, its checkout or its capabilities being prepared, no agent
// CLI started (finishStoppedInSetup).
const InterruptedInSetupDetail = "the daemon driving this run was stopped (an upgrade or a restart) while the run " +
	"was still setting up, before any agent started; handed back to the queue without a strike"

// finishStoppedInSetup hands back a run the daemon was stopped under
// before its agent started -- what runOne's setup guard and
// RunDispatch's own setup paths do when ctx, the daemon's, is done. It
// used to read as the setup failing ("setup-failed"), which took a
// strike and spent the Run now request, over a stop that said nothing
// about the task.
//
// Finished as model.InterruptedOutcome instead, at shutdown rather than
// by the next daemon's startup recovery: the row is no longer live, so
// the task reads queued at once, with no strike and no retry backoff,
// and FinishRun puts back the Run now request it spent with its
// original timestamp (model.RunNowSurvives, settleRunNow), which keeps
// its place ahead of the ready list. Whatever sandbox it had acquired is
// released by runOne's own deferred release, on a context detached from
// the shutdown. Counted with the rest of the shutdown's interruptions
// (InterruptedRuns), and the upgrade check reports it as lost in setup,
// since no framework had been chosen for it (AdoptionReport.withRows).
func finishStoppedInSetup(ctx context.Context, store *model.Store, taskID, runID string, at time.Time) error {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	if err := store.FinishRun(finishCtx, runID, at, model.InterruptedOutcome, InterruptedInSetupDetail); err != nil {
		return err
	}
	interruptedRuns.Add(1)
	noteInterrupted(finishCtx, store, taskID, runID, at)
	return nil
}

// StoppedByDaemon reports whether ctx -- the one a run's framework is
// given, or a child of it -- was cancelled because the daemon driving the
// run is stopping, rather than by the run's deadline, its task closing or
// a usage limit. It is what a daemon sets runhost.Host.Detach to: a run
// host is left running for the next daemon to adopt on exactly this
// cancellation, and destroyed on every other.
func StoppedByDaemon(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errDaemonStopped)
}

// interruptedRuns counts the runs this process has finished as
// model.InterruptedOutcome, for the journal line the daemon writes as it
// shuts down (InterruptedRuns).
var interruptedRuns atomic.Int64

// InterruptedRuns is how many runs this process has finished as
// model.InterruptedOutcome -- in practice, how many its own shutdown
// interrupted, since that is the only thing that ends a run that way.
func InterruptedRuns() int64 { return interruptedRuns.Load() }

// checkTaskClosed reads store.State(taskID) once and calls
// cancel(errTaskClosed) if it reads model.StateClosed, reporting whether
// it did. A store error is treated as "not closed" rather than
// propagated: watchForTaskClosed's own caller retries on the next tick
// regardless, so a transient read failure costs one interval of latency,
// not the ability to ever notice a close for the rest of the run.
func checkTaskClosed(ctx context.Context, store *model.Store, taskID string, cancel context.CancelCauseFunc) bool {
	st, err := store.State(ctx, taskID)
	if err != nil {
		return false
	}
	if st == model.StateClosed {
		cancel(errTaskClosed)
		return true
	}
	return false
}

// watchForTaskClosed polls store.State(taskID) every interval and calls
// cancel(errTaskClosed) the moment it reads model.StateClosed -- the
// store-polled cancellation signal RunDispatch needs because grain daemon
// (running the agent) and grain ui (where a close actually lands) are
// separate processes sharing only the store, per bwsalmon/agents#346.
//
// It does not check immediately on entry: RunDispatch itself already
// calls checkTaskClosed synchronously, before framework.Run is ever
// invoked, which is what makes a task already closed by the time
// RunDispatch runs -- dispatch.Cycle claimed the slot before the close
// write landed, and RunDispatch only got around to running it after --
// stop that run's first tool call from ever reaching a real sandbox,
// deterministically, rather than racing this goroutine's own first tick
// against a real subprocess. See RunDispatch's own doc comment.
//
// queryCtx, not runCtx, bounds the store reads: runCtx is what this func
// is about to cancel, so querying with it would make the very last read
// racy against its own effect. It returns as soon as runCtx.Done() fires
// for any reason, which is what bounds this goroutine's lifetime to the
// run's: RunDispatch cancels runCtx itself the moment framework.Run
// returns.
func watchForTaskClosed(runCtx, queryCtx context.Context, store *model.Store, taskID string, interval time.Duration, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			if checkTaskClosed(queryCtx, store, taskID, cancel) {
				return
			}
		}
	}
}

// frameworkOpensPullRequests asks the Framework about to drive this run
// whether its runs get the open_pull_request tool at all, so BuildPrompt
// can name it only where it exists (agent.PullRequestFramework's own doc
// comment says why the Framework is the only thing that knows).
//
// A Framework that does not implement that interface -- every test fake
// here, and any implementation added later that forks no mcpserver --
// answers no, which is the safe direction: a run that is never told
// about a tool it happens to have loses one convenience, where a run
// told to call a tool it does not have burns turns on an error it cannot
// fix.
func frameworkOpensPullRequests(framework agent.Framework) bool {
	f, ok := framework.(agent.PullRequestFramework)
	return ok && f.CanOpenPullRequest()
}

// BuildPrompt is the prompt a dispatched run receives — deliberately
// plain: the task's own title and body plus the facts that are grain's
// own, never the agent's to guess (which branch to push, which repo it
// lives in, and which other repos it may read), the same "deterministic,
// not self-reported" reasoning model.BranchName's own doc comment gives,
// restated here at the one place those facts reach the prompt.
//
// checkoutDir, when non-empty, is the directory RunDispatch already
// cloned the target into (prepareCheckout's own CheckoutDir) -- said here
// because an agent that is not told cannot know, which is the whole
// failure that made the clone happen up front. Empty is a sandbox left
// bare, and leaves this prompt exactly as it always read -- except when
// task.Target is nil, which is a sandbox that was always going to be
// bare (checkout.go's prepareCheckout skips cloning outright for a task
// with no target) and gets its own sentence explaining why, rather than
// silently reading like a clone that simply failed.
//
// sandboxRoot is where that directory actually is: the sandbox's own
// working directory as an absolute path inside it (checkout.Root, asked
// of the sandbox by sandboxWorkingDir). It is here because the relative
// form above is not the form a file tool is called with, and because the
// environment every agent framework describes to its own run is the
// controller's rather than the sandbox's -- see sandboxDirsSection, which
// is the only thing that reads it, for what a run that has to reconcile
// those two on its own actually does. "" -- a sandbox that could not be
// asked -- leaves the prompt exactly as it read before this existed.
//
// The one thing here that is advice rather than fact is
// proposalSection's follow-on task etiquette, and it is here for the same
// reason the rest is: the facts it stands on (this run's task id, whether
// that task auto-merges) are grain's own, and an agent told neither
// cannot fill in a proposal's depends_on or decide its auto_merge at all.
//
// task.Reads is mentioned but not enforced here: the git proxy already
// allows a fetch against any of them and refuses a push to any but
// task.Target (gitproxy/authorize.go), so this line is purely
// informational -- it tells the agent those repos exist and are safe to
// clone, rather than granting anything itself.
//
// The commit-message paragraph is a fact of the same kind: grain builds
// the pull request's title and description out of this branch's commit
// messages (description.go), which no tool description says and nothing
// in the sandbox reveals. A run that does not know it has no reason to
// write a commit message for anyone but git -- and no way of guessing
// that its own summary line, not the task's title, is what the pull
// request ends up called.
//
// The last paragraphs, for a task that has a target at all, are the
// push/check/repair loop pkg/mcp's pull_request_status exists for, and
// what finishing that loop means: green checks and a branch that still
// merges into its base. They are informational in the same way: nothing
// here grants a second push, the proxy already allowed every push of
// the task's own branch to task.Target -- what they grant is the
// knowledge that the loop is available and that a push alone is not the
// end of it, neither of which a tool description on its own can convey (see the comments at those
// paragraphs).
//
// canOpenPullRequest is the one fact in that paragraph this function
// cannot work out for itself: whether this run's mcpserver actually
// registered open_pull_request. That depends on the Framework driving
// the run having been given a daemon to ask (agent.PullRequestFramework,
// and pkg/cli/mcpserver.go's -server/-task), which is a deployment's
// choice -- a UI/API served at all -- rather than anything visible in a
// task. False leaves the paragraph naming only pull_request_status, so a
// deployment without that route never sends a run after a tool that is
// not on its roster.
//
// It gates one more paragraph, statusSection's, on exactly the same fact:
// update_status is registered by the same -server/-task pair, so a run
// that can open its own pull request can also say what it is doing on its
// task's row, and one that cannot has neither tool. See statusSection for
// why that paragraph is worth its space at all.
//
// maxRuntime is the wall-clock budget RunDispatch will cancel this run
// at (cfg.maxRunRuntime(), the deadline on the very ctx framework.Run
// receives), and it is here for the reason everything else here is: it
// is grain's own fact and there is no way at all to discover it from
// inside the sandbox. A run that does not know a deadline exists has no
// reason to push before reaching for one more refactor and no basis for
// choosing between waiting on CI and finishing -- and salvagePushedBranch
// rescues only what was *pushed* when the clock runs out, so a run that
// guesses wrong loses everything it merely committed. Zero omits the
// paragraph, for a caller with no budget to state rather than for one
// that means "uncapped": no dispatch passes zero (Config.maxRunRuntime
// substitutes DefaultMaxRunRuntime), and a run really is always capped.
//
// The prompt is read once, at turn 1, so it is only half the answer; the
// other half is mcp.Registry.AnnounceDeadline, which puts the time
// remaining on every tool result once the budget runs low. The paragraph
// says so, since a run that expects the reminder can spend its early
// turns working rather than counting.
//
// history is everything that has already happened to this task: the
// attempts before this one, the commits they left on the branch this one
// continues, and the conversation (see History, previousAttemptsSection
// and commentThreadSection). Both of its sections go here, immediately
// after the sentences that say where the checkout is and which branch is
// in it -- the facts they explain -- and ahead of the commit-message, CI
// and budget paragraphs. That is deliberate: a redispatch's first
// question is "what is already on this branch, and what did the attempt
// that put it there run into?", and a section that answered it after two
// paragraphs of push-and-check mechanics would be answering it too late
// to stop the re-diagnosis it exists to prevent. History{} -- a first
// attempt at a task nobody has said anything about -- leaves the prompt
// exactly as it reads without either section.
//
// setup is what the repo's own setup command did in that checkout before
// this run's first turn (SetupResult, nil for the repos that configure
// none), and it goes immediately after the checkout sentence for the
// reason history goes where it does: it is a fact about the directory
// those sentences just pointed at, and a run reading it late has already
// spent turns on the failure it explains. See setupSection.
func BuildPrompt(task model.Task, checkoutDir, sandboxRoot string, canOpenPullRequest bool,
	maxRuntime time.Duration, history History, setup *SetupResult) string {
	branch := task.BranchName()
	prompt := suspensionNoteSection(history)
	if task.ChangeTarget != nil {
		// A change that is not a git branch (a changelist): its kind's
		// own section says where the work is and how it is handed in, in
		// place of the branch and push sentences below.
		prompt += fmt.Sprintf("%s\n\n%s\n\n%s", task.Title, task.Body,
			model.ChangeTargetPromptSection(task, CommitTrailers(task)))
	} else if task.Target == nil {
		prompt += fmt.Sprintf(
			"%s\n\n%s\n\nThis task has no repo attached -- there is no source working branch to push. "+
				"Use any read-only material grain prepared in the sandbox for analysis; "+
				"file repository changes as separate tasks.",
			task.Title, task.Body,
		)
	} else if reviewedID, inPlace := reviewsInPlace(task); inPlace {
		// A review working on the reviewed change's own branch: the branch
		// already exists, somebody else's pull request is open off it, and
		// that task may be re-run while this one is.
		prompt = fmt.Sprintf(
			"%s\n\n%s\n\nWork in %s. Push any fixes to %q, the branch of task %s that you are "+
				"reviewing -- it already exists, and its pull request carries what you push there, so "+
				"open no pull request of your own and never push to the repo's default branch. Before "+
				"pushing, `git fetch origin` and rebase onto `origin/%s`; if the push is still rejected as "+
				"non-fast-forward, fetch, re-read what moved, rebase and retry once, and if that is rejected "+
				"too, ask_question the owner rather than force-pushing.",
			task.Title, task.Body, task.Target, branch, reviewedID, branch,
		)
	} else {
		prompt += fmt.Sprintf(
			"%s\n\n%s\n\nWork in %s. Push your change to a new branch named %q -- "+
				"never to the repo's default branch directly.",
			task.Title, task.Body, task.Target, branch,
		)
	}
	if checkoutDir != "" {
		prompt += fmt.Sprintf(
			"\n\nThat repo is already cloned for you at ./%s, with %q checked out and "+
				"its remote pointing at the only address you can reach it through -- "+
				"work in that directory rather than cloning anything yourself, and "+
				"push with `git push origin %s`.",
			checkoutDir, branch, branch,
		)
	}
	prompt += sandboxDirsSection(checkoutDir, sandboxRoot)
	prompt += sandboxToolingSection(task.Target != nil, grainci.Granted(task.Grants))
	prompt += grainCLISection(task)
	prompt += customFieldsSection(task)
	prompt += timezoneSection(task)
	prompt += issueSection(task)
	prompt += setupSection(setup)
	prompt += sessionSandboxSection(task, history)
	// The two history sections, together and in that order: what the
	// attempts before this one did, then what has been said about the
	// task. Both are the same kind of fact -- something a run would
	// otherwise pay to rediscover -- and both are read before the
	// mechanics below them. See this function's own doc comment on why
	// they sit here rather than at the tail.
	//
	// Both are lists, so both end in a newline of their own; trimmed on
	// the way in rather than shaped differently, so that a section
	// followed by another paragraph is separated from it by the same one
	// blank line every other pair of paragraphs here is.
	if resumed := parkedResumeSection(history); resumed != "" {
		prompt += "\n\n" + strings.TrimRight(resumed, "\n")
	}
	if attempts := previousAttemptsSection(history, branch); attempts != "" {
		prompt += "\n\n" + strings.TrimRight(attempts, "\n")
	}
	if thread := commentThreadSection(history.Comments); thread != "" {
		prompt += "\n\n" + strings.TrimRight(thread, "\n")
	}
	if history.BaseIncident != "" {
		prompt += "\n\n" + strings.TrimRight(history.BaseIncident, "\n")
	}
	if task.Target != nil {
		prompt += pairedBranchesSection(history.PairedBranches)
		prompt += relayedReviewThreadsSection(task)
	}
	// Said out loud because neither half is discoverable from the tools
	// alone. Nothing stops a run pushing repeatedly -- the branch is its
	// own and the proxy authorizes every push to it (gitproxy/refupdates.go)
	// -- but the sentences above read as one final act, and a run that
	// treats them that way has no reason to ever look at CI at all.
	// Leaving that loop implicit is what left a red build to the merge
	// queue's own repair of the branch a whole dispatch later (sync.go's
	// requeueForRepair) even when the run that caused it was still
	// running.
	//
	// wait_for_checks is named first, and pull_request_status only as the
	// thing it saves: a run told to "check CI" reaches for the status read
	// and then has to invent a waiting strategy out of turns it could have
	// spent working -- which is the loop that tool was added to end
	// (mcp/wait_for_checks_tool.go). The status read is still worth
	// naming, since it is the one that answers questions about the pull
	// request itself rather than about the build.
	if task.Target != nil {
		// What the commit messages are for, which is discoverable from
		// nowhere else at all: grain builds the pull request's title and
		// description out of them (description.go), so they are the only
		// place an agent can write for the human who will review the
		// change. A run never told this writes "wip" and "fix tests" --
		// perfectly good git log entries -- and earns a pull request that
		// reads as description-free, which is exactly what grain's own
		// did for as long as this paragraph was missing and the body was
		// one line of metadata.
		//
		// The summary line is called out separately because of what it
		// now decides. The pull request is named after the change on the
		// branch rather than after the task that asked for it
		// (titleFromCommits), so a task worded as a question -- "check
		// the parser for bugs" -- gets a pull request named for the
		// answer only if the agent writes that answer as a summary line.
		// It is also the whole of how a run renames its own pull
		// request: there is no tool for it, and rewriting the commit
		// message is the rename.
		prompt += "\n\nCommit with a message written for a human reviewer: a short summary " +
			"line, a blank line, then a paragraph on what changed and why. grain builds " +
			"this branch's pull request description out of those commit messages, so " +
			"they are where you explain the change -- there is nowhere else for it to " +
			"come from, and a description is what a reviewer reads first. The pull " +
			"request is named after your change too, not after this task: its title is " +
			"the summary line of the first commit whose message has a body, so write " +
			"that line as the name you want the pull request to have -- \"Fix the " +
			"off-by-one in the header parser\" rather than the task's own wording. " +
			"Amending or adding a commit renames it; grain reads the branch again every " +
			"time it touches the pull request."
		// The lineage trailers (worktree.go): grain writes them on the
		// merge commit itself, and asks for them on the agent's own
		// commits too, so `git log --grep 'Origin: …'` finds the work
		// whichever way it landed -- a cherry-pick or a port included.
		if trailers := CommitTrailers(task); len(trailers) > 0 {
			prompt += "\n\nEnd every commit message with these trailers, in its last " +
				"paragraph, so the work can be followed from git alone:\n\n    " +
				strings.Join(trailers, "\n    ")
		}
		if grainci.Granted(task.Grants) {
			prompt += fmt.Sprintf(
				"\n\nPush as often as you like: %q is your branch. After each push, run "+
					"`grain ci wait` (give `run_command` `timeout: 600000`): it explicitly starts "+
					"grain's own CI for the pushed branch (or joins the identical run already in "+
					"progress), waits for its verdict, and prints failing step output. It does not "+
					"read or wait for GitHub checks. If CI is still running when its 9-minute slice "+
					"ends, it says so and names the run: carry on with `grain ci wait --resume "+
					"<run-id>`. Pass `--jobs j1,j2` for fast selective feedback; a selected job "+
					"includes its dependencies but never satisfies the task's required CI verdict. "+
					"Before finishing, run `grain ci wait` without `--jobs` and get a successful "+
					"full-suite result for the exact commit. If grain CI fails, fix it, push again "+
					"and run `grain ci wait` again.", branch)
			prompt += redCIProcedureSection()
		} else {
			prompt += fmt.Sprintf(
				"\n\nPush as often as you like: %q is your branch, and each push reruns CI "+
					"against the new commit. After a push, call `wait_for_checks` and fix "+
					"any failure before finishing.", branch)
		}
		// The other half of reading a failure, and the one a run has no
		// way to work out from inside its sandbox: whether the failure is
		// its own. A suite that was already red on the base looks
		// identical from here to one this change broke, and a run that
		// cannot tell them apart spends its remaining turns rewriting
		// working code -- or, worse, weakening the test that was telling
		// the truth. Named in words for the same reason the loop above
		// is: nothing in a tool roster suggests looking at a branch that
		// is not yours (mcp/base_branch_status_tool.go).
		//
		// A repair run is the exception, and repairSection is where the
		// same paragraph comes out the other way round: the branch it is
		// working on is the one holding the merge queue up, so leaving
		// inherited breakage alone leaves the queue stopped.
		if grainci.Granted(task.Grants) {
			// Grain CI judges this branch directly. It does not run the base,
			// so base_branch_status is a GitHub-check comparison with no role
			// in this CI loop.
		} else if history.Repair || model.FixesBaseBranch(task) {
			prompt += repairCISection()
		} else {
			prompt += "\n\nIf a check fails, find out whether it was already failing before you " +
				"started: `base_branch_status` reports CI on the branch your work merges " +
				"into. A failure that is red there too is not yours to fix -- leave it " +
				"alone, do not weaken the test to get past it, and say in what you report " +
				"that it was already failing. A failure with no counterpart there is yours."
		}
		// The second half of the same loop, for a run that really has the
		// tool (canOpenPullRequest above): pull_request_status reports the
		// checks a push triggered directly, but a repo whose CI only runs
		// on pull requests has none until one is open, and grain does not
		// open it until the run has already exited. A run that opens it
		// itself sees those checks while it still has turns left to fix
		// them -- which is the whole reason `grain pr open` exists, and
		// is exactly as undiscoverable from a tool description as the
		// loop above. It is named as the CLI command, not the
		// open_pull_request tool: the control surface every sandboxed run
		// now gets (pkg/daemon/mcp_surface.go's controlRoster) leaves that
		// tool off the roster, and the command answers through the same
		// daemon route (gitproxy's /cli/pr/open) the tool did.
		if canOpenPullRequest {
			prompt += "\n\nOnce you have pushed, you can run `grain pr open` to open the " +
				"pull request for that branch without waiting for your run to end, and " +
				"see what CI says about it there and then -- then fix what it reports, " +
				"push again, and run it again for the next round. It is the same pull " +
				"request grain opens for you when this run finishes, and running it more " +
				"than once never opens a second one."
		}
		// Where the loop ends, said in words for the same reason the loop
		// itself is: a run that pushes, reads one status and stops has
		// followed every sentence above this one to the letter. Both
		// halves here are ways a push that looked finished is not. An
		// unfinished check carries no verdict at all -- pull_request_status
		// says so on the answer itself, and healthFrom makes the same call
		// at the merge gate -- so stopping on a queued job is stopping on
		// an unknown. And a branch that conflicts with its base never
		// merges however green its checks are (healthFrom reads
		// PrConflicted off the pull request before it looks at a single
		// check), which the run still holding the checkout can fix in a
		// turn, rather than leaving it to the repair the merge queue asks
		// for minutes later in a cold sandbox (sync.go's requeueForRepair).
		base := baseDescription(task)
		prompt += fmt.Sprintf(
			"\n\nYour job is not done at the moment you push: it is done when those "+
				"checks have finished and passed and your branch still merges cleanly "+
				"into %s. A check that has not finished carries no verdict, so wait for "+
				"it rather than reading it as a pass. If your branch conflicts "+
				"with %s, resolving that is part of this task too -- `git fetch origin`, "+
				"merge that branch into %q, resolve the conflicts, commit and push "+
				"again.",
			base, base, branch,
		)
	}
	prompt += nothingNeededSection(task)
	prompt += questionSummarySection
	prompt += statusSection(canOpenPullRequest)
	prompt += chatSection(task, canOpenPullRequest)
	prompt += runtimeSection(task, maxRuntime)
	if names := readOnlyNames(task); len(names) > 0 {
		prompt += fmt.Sprintf(
			"\n\nYou may also read %s for reference -- clone them if you need to, "+
				"but never push to them.",
			strings.Join(names, ", "),
		)
	}
	prompt += proposalSection(task)
	prompt += writeTargetsSection(task)
	return prompt
}

// suspendedCLINote leads the grain CLI reference in a resumed session's
// delta prompt. The session's own history may predate the control surface
// (pkg/daemon/mcp_surface.go), so its earlier turns can name MCP tools --
// wait_for_checks, run_ci_job, open_pull_request -- that are no longer on
// its roster; a resumed run that is not told reaches for them, gets "No
// such tool available", and parks without ever running CI.
const suspendedCLINote = "\n\nYour tools may have changed since this session last ran: " +
	"CI, pull request and namespace work now goes through the `grain` CLI below, so a " +
	"tool an earlier turn called that now answers \"No such tool available\" has a " +
	"`grain` command instead (`wait_for_checks` is `grain ci wait`, `run_ci_job` is " +
	"`grain ci run-job`, `open_pull_request` is `grain pr open`)."

// BuildSuspendedPrompt builds the delta prompt sent after
// agent.SuspendedPreamble when a dispatch resumes a suspended CLI session
// (agent.RunConfig.ResumeSession). Because the CLI's on-disk conversation
// already holds the task description, standing instructions, tool rules, and
// prior conversation turns from the earlier dispatch, repeating the full
// 30-80+ KiB prompt on every resumed dispatch bloats the session context and
// triggers frequent compactions/handoffs.
//
// Instead, this carries the current task description (in case it was edited),
// the fresh sandbox checkout orientation, any new attempt or park history,
// any comments added since the last delivered turn, active base/repair
// notices, the grain CLI reference (suspendedCLINote says why a resumed
// session needs it again), and dynamic per-dispatch sections (placed attachments and capability
// prompt sections). The full prompt in RunConfig.Prompt remains the fallback
// whenever session resume is refused or lost.
func BuildSuspendedPrompt(task model.Task, checkoutDir, sandboxRoot string,
	history History, setup *SetupResult, newComments []model.Comment, extraSections ...string) string {
	branch := task.BranchName()
	prompt := suspensionNoteSection(history)
	if line := model.ChangeTargetSuspendedPromptLine(task); line != "" {
		prompt += fmt.Sprintf("Task: %s (task %s). %s", task.Title, task.ID, line)
	} else if task.Target == nil {
		prompt += fmt.Sprintf(
			"Task: %s (task %s, no repository attached).",
			task.Title, task.ID,
		)
	} else {
		prompt += fmt.Sprintf(
			"Task: %s (task %s). Work in %s on branch %q.",
			task.Title, task.ID, task.Target, branch,
		)
	}
	if checkoutDir != "" {
		if history.SandboxReused {
			prompt += fmt.Sprintf(
				" That repo is already checked out for you at ./%s with %q checked out.",
				checkoutDir, branch,
			)
		} else {
			prompt += fmt.Sprintf(
				" That repo is freshly cloned for you at ./%s with %q checked out.",
				checkoutDir, branch,
			)
		}
	}
	if sandboxRoot != "" {
		if checkoutDir != "" {
			prompt += fmt.Sprintf(
				" Your sandbox working directory is %s (checkout at %s).",
				sandboxRoot, path.Join(sandboxRoot, checkoutDir),
			)
		} else {
			prompt += fmt.Sprintf(
				" Your sandbox working directory is %s.",
				sandboxRoot,
			)
		}
	}
	if body := strings.TrimSpace(task.Body); body != "" {
		prompt += "\n\n" + body
	}
	prompt += customFieldsSection(task)
	prompt += timezoneSection(task)
	prompt += issueSection(task)
	prompt += setupSection(setup)
	prompt += sessionSandboxSection(task, history)
	if resumed := parkedResumeSection(history); resumed != "" {
		prompt += "\n\n" + strings.TrimRight(resumed, "\n")
	}
	if attempts := previousAttemptsSection(history, branch); attempts != "" {
		prompt += "\n\n" + strings.TrimRight(attempts, "\n")
	}
	if thread := commentThreadSection(newComments); thread != "" {
		prompt += "\n\n" + strings.TrimRight(thread, "\n")
	}
	if history.BaseIncident != "" {
		prompt += "\n\n" + strings.TrimRight(history.BaseIncident, "\n")
	}
	if task.Target != nil {
		prompt += relayedReviewThreadsSection(task)
	}
	if task.Target != nil && !grainci.Granted(task.Grants) && (history.Repair || model.FixesBaseBranch(task)) {
		prompt += repairCISection()
	}
	prompt += suspendedCLINote + grainCLISection(task)
	for _, section := range extraSections {
		if trimmed := strings.Trim(section, "\n"); trimmed != "" {
			prompt += "\n\n" + trimmed
		}
	}
	return prompt
}

// commentsSinceSuspend returns the subset of comments added after the last
// comment delivered to a completed turn (priorDelivered). When no comment ID
// has been recorded as delivered yet (priorDelivered == nil), all comments are
// included so comments posted during or after the initial turn are never
// dropped by a late SuspendedSession.At timestamp.
func commentsSinceSuspend(comments []model.Comment, priorDelivered *int64) []model.Comment {
	if len(comments) == 0 {
		return nil
	}
	if priorDelivered == nil {
		return comments
	}
	var out []model.Comment
	for _, c := range comments {
		if c.ID > *priorDelivered {
			out = append(out, c)
		}
	}
	return out
}

// mergeRepairRun reports whether the run about to be dispatched for task
// is a branch repair rather than ordinary work: a task the queue has sent
// back to fix its own pull request branch and whose repair has not finished
// (Observation.RepairInFlight), a task the queue filed to repair another
// task's branch back when a repair was a separate task (model.OriginReason.Merger),
// or a task repairing the base branch itself (model.FixesBaseBranch).
//
// A nil obs -- a task nothing has ever been observed about, which is
// every first dispatch -- is still a repair when FixesBaseBranch(task) or
// Origin.Reason.Merger() holds.
func mergeRepairRun(task model.Task, obs *model.Observation) bool {
	return task.Origin.Reason.Merger() || obs.RepairInFlight() || model.FixesBaseBranch(task)
}

// redCIProcedureSection is the one way every run with grain CI is told to
// work a red build, and what grain's own repair bodies point back to by
// name (model.RedCIProcedure) instead of saying it again.
//
// It exists because the tools for narrow work were all there and nobody
// used them: over three days to 2026-10-25 runs called wait_for_checks
// 1,142 times and retrigger_ci 242 times, and run_ci_job 7. A run left to
// itself pushes and waits out the whole suite to learn what one `go test
// -run` would have told it in seconds -- and a base-branch repair that
// works that way holds every queued change for the hours it takes.
// Classifying comes before reproducing because an infrastructure failure
// has no local reproduction, and a run that edits tests to get past one
// is weakening the suite for nothing. Kept short: it is in every prompt.
func redCIProcedureSection() string {
	return "\n\nWhen grain CI is red, follow the " + model.RedCIProcedure + " rather than " +
		"pushing and re-running the whole suite:\n\n" +
		"1. Get the failing job and the exact failing test names from `grain ci wait` and " +
		"`grain ci log --job <job> --step <step> --tail N` (a test runner's failure summary is at " +
		"the end). For the full output, read the failing attempt from the namespace rather than " +
		"re-running the suite to find the test: " +
		"`grain read ci://<repo>/<run>/<job>/attempts/<n>/tests/<pkg>/<Test>.log`, or " +
		"`grain grep -- '--- FAIL' ci://<repo>/<run>/<job>/attempts/<n>/gotest.log`.\n" +
		"2. Check `grain ci test-history <test>` for those tests, and the note on whether the same job is " +
		"failing on other branches. If it is inherited or flaky, say so, and do not chase it unless " +
		"repairing that branch is this run's own work.\n" +
		"3. Classify it. A step killed at its time limit (exit 124), out of memory (exit 137), " +
		"Unschedulable, connection timeouts to the Kubernetes API, or a sandbox that failed to " +
		"set up is infrastructure, which no test edit fixes: report it and retrigger at most once.\n" +
		"4. Reproduce it locally with the narrowest command -- `go test -race -run '<TestName>' " +
		"./<pkg>`, or the job's own step command -- before changing anything.\n" +
		"5. Fix it, then re-run that test locally, and any test that checks the file you changed " +
		"(a CI workflow edit means running the tests that read the workflows).\n" +
		"6. Push, then run `grain ci wait --jobs <the jobs that failed>` (or `grain ci run-job " +
		"<job>` / `grain ci retrigger`) for fast feedback.\n" +
		"7. Only once those are green, run `grain ci wait` without `--jobs` for the merge gate."
}

// repairCISection is what a merge queue repair run is told about a red
// check instead of the "leave inherited breakage alone" paragraph every
// other run gets (History.Repair, mergeRepairRun).
//
// The ordinary advice is right for ordinary work: a run that rewrites
// working code chasing a failure it did not cause has wasted its turns,
// and one that weakens the failing test has done damage. It is wrong
// here, and wrong in the one way that matters to the deployment. A
// repair run exists because this branch is the head of a merge queue and
// is not merging (sync.go's requeueForRepair); the queue does not care
// whose commit turned the check red, only that it is red, and a repair
// that reads base_branch_status, finds the same failure on the base and
// reports "already failing, not mine" hands the queue back exactly the
// pull request it could not merge. The next cycle then gives up on it
// and asks a person (escalateToUser), so breakage that reached the base
// -- which is breakage that already got past somebody -- stops the
// pipeline rather than being carried out of it by the next change
// through. Told to fix it either way, the same run lands the fix on a
// branch that is about to merge into the base, which is where the fix
// was needed anyway.
//
// The two things it keeps from the ordinary paragraph are the two that
// were never about whose failure it is: do not weaken the test, and say
// what you found. base_branch_status is still worth calling for exactly
// that -- a failure that reproduces on the base is a different kind of
// diagnosis than one that appeared with this branch, and the person
// reading the pull request afterward wants to know which they got.
func repairCISection() string {
	return "\n\nThis run is the merge queue's repair of a branch that is already stopping " +
		"the queue, so fix every failing check on it, whether or not your own commits " +
		"caused the failure. If a check fails, `base_branch_status` still tells you " +
		"where it came from -- it reports CI on the branch your work merges into -- " +
		"but a failure that is red there too is yours to fix here all the same: this " +
		"pull request cannot merge until it is green, and leaving inherited breakage " +
		"alone leaves the queue stopped. Fix the code the check is failing on rather " +
		"than weakening the test to get past it, and say in what you report which " +
		"failures were already failing on the base and what you did about them."
}

// writeTargetsSection is the paragraph a task with more than one
// writeable repo gets: which the others are, which branch work goes on
// in each, and how a change over there actually gets made -- "" for the
// single-repo task most of them are.
//
// It says the mechanism out loud because nothing else can. The sandbox
// holds one checkout, the proxy refuses a push to anything but the
// task's own target (model.Store.GitScope), and a run that has been told
// "this task covers grain and its state repository" and nothing more
// will spend its turns discovering that the second half of its job is
// impossible -- or, worse, do the work in a directory it cannot push and
// lose it with the sandbox. What is possible is a proposal naming that
// repository (propose_task's `repo`), which grain files as a task of its
// own against it, with its own sandbox, its own branch and its own pull
// request. Told that, the same run does the half it can and leaves the
// other half in a form somebody can dispatch.
//
// It sits after proposalSection deliberately, and qualifies it. That
// paragraph tells an auto-merge task with a base of its own that nothing
// it proposes inherits auto-merge, which stops being the whole truth the
// moment a write target exists: a proposal that lands on one of these
// repositories, on the branch declared with it, does inherit it
// (model.LandsWhereGranted). A run that believed the unqualified version
// would water down its own proposals -- ask for auto_merge: false, or
// say in the body that a human should look -- for a permission it
// actually has.
// readOnlyNames is what the "you may also read" sentence lists: the
// task's read-only repos, minus the one it is working in.
//
// A task can carry its own target among Reads. Somebody stating both
// halves of it is refused now (ui.refuseTargetAmongReads, and the
// read-only picker no longer offers the target either), but several
// paths that write a task's reads straight into the store (a template's
// or a schedule's own Reads, a backport carrying the original's) can
// still produce it without anybody choosing it. Left in, the
// prompt said both things about one repository in the same breath:
// "Work in acme/widgets. Push your change to a new branch..." near the
// top, and "You may also read acme/widgets ... but never push to them"
// a few paragraphs down. An agent that believes the second sentence
// does not push at all, which is the whole run wasted; and there is
// nothing the sentence could usefully be saying about that repository
// anyway, since it is already cloned and already pushable.
//
// Compared with RepoRef.Same rather than by string, so a read spelled
// in another case than the target is the same repository here too.
func readOnlyNames(task model.Task) []string {
	names := make([]string, 0, len(task.Reads))
	for _, r := range task.Reads {
		if task.Target != nil && r.Same(*task.Target) {
			continue
		}
		names = append(names, r.String())
	}
	return names
}

func writeTargetsSection(task model.Task) string {
	if len(task.Writes) == 0 {
		return ""
	}
	names := make([]string, len(task.Writes))
	for i, w := range task.Writes {
		if w.Base != "" {
			names[i] = fmt.Sprintf("%s (on branch `%s`)", w.Repo, w.Base)
			continue
		}
		names[i] = fmt.Sprintf("%s (on its default branch)", w.Repo)
	}
	s := fmt.Sprintf(
		"\n\nThis task may land work in more than one repository or branch: besides the one you "+
			"are working in, it covers %s. You cannot push to those from this sandbox -- "+
			"clone them to read if that helps, but every push here goes to your own "+
			"branch of the repo above. What you do instead, for each of them that needs "+
			"a change, is call `propose_task` with `repo` set to that owner/name (or `branch` "+
			"set to the branch named here): grain files it as a task against that repository, "+
			"on the branch named here, for an agent with its own sandbox and its own pull request. "+
			"File one only where work is actually needed, and say in its body what it has to do and why -- "+
			"that body is all the run picking it up will have.",
		strings.Join(names, ", "),
	)
	if task.AutoMerge {
		s += " A proposal filed that way inherits this task's auto-merge, and is queued " +
			"without waiting for anybody to approve it, exactly as one " +
			"for the repository you are working in would be: the repositories and branches " +
			"named here are what this task was given, so landing on one of them is not " +
			"landing somewhere else."
	}
	return s
}

// runtimeSection is the wall-clock budget paragraph, and the one piece
// of advice that follows from it: work in pushable pieces, because the
// only thing that outlives this run's sandbox is what reached the
// remote.
//
// It is worded as a deadline plus what to do about it rather than as a
// number alone, because a number alone is the half a run cannot act on:
// "you have 2h0m" and "commit and push each piece as it works" are the
// same fact, and only the second one changes what a run does at the turn
// where it has a working change and an idea for a better one.
//
// The wait credit is stated for exactly that reason and no other
// (RunWaitCreditCap). A run told "2h" that silently gets more still
// rations itself as if it had 2h -- and the rationing it does is to skip
// the wait on the push it has just made, which is the one thing the
// credit exists to pay for. The number is the rule the run is actually
// under, so the ceiling is named too: what a budget paragraph must never
// do is leave a run guessing which of two figures it will be cancelled
// at.
//
// It names RunWaitCreditCap rather than a per-dispatch figure because
// the cap is a property of this build, not of one run. What is per
// deployment is whether reports can reach the daemon at all
// (Config.RunWaitCredits): a caller that wires no registry -- a test, a
// one-shot cycle -- grants no credit, and this paragraph overstates by
// the cap for such a run. That is the same standard the sentence below
// it already holds to, promising tool results that count the deadline
// down for callers whose framework announces none.
//
// A task with no target gets the same deadline and a different
// instruction, since it has no branch to push: its work product is the
// closing note comment_on_issue relays (ProcessResult), and a note never
// written is a run that produced nothing at all. It is told to keep time
// back for that note rather than to write it early and add to it --
// ProcessResult relays the *first* such call (firstToolCallArg), so
// "add to it" would be advice grain then drops on the floor.
//
// "" for a zero budget, which BuildPrompt's own doc comment covers --
// except a durable task's, whose zero budget is no deadline at all
// (durableDispatch) and is told so, with the idle watchdog that stands in
// for it (watchIdle).
func runtimeSection(task model.Task, maxRuntime time.Duration) string {
	if maxRuntime <= 0 && task.IsDurable() {
		return durableRuntimeSection(task)
	}
	if maxRuntime <= 0 {
		return ""
	}
	s := fmt.Sprintf(
		"\n\ngrain cancels this run after %s of wall-clock time and destroys the "+
			"sandbox with it, so treat that as your budget rather than as a limit you "+
			"will never reach. Time you spend blocked inside `grain ci wait` is not "+
			"charged against that budget -- up to %s over the whole run, which is "+
			"credited back to your deadline as you spend it, so %s is the longest this "+
			"run can live. Waiting for the verdict on a push you have already made is "+
			"therefore not a reason to skip the wait.",
		humanDuration(maxRuntime), humanDuration(RunWaitCreditCap),
		humanDuration(maxRuntime+RunWaitCreditCap),
	)
	if task.Target != nil {
		s += " Only what you have pushed survives that: a commit you never pushed, and " +
			"an edit you never committed, go with the sandbox. Commit and push each " +
			"piece of the work as it starts working rather than saving one push for " +
			"the end, and if the time runs short, push what already works and say what " +
			"is left in a comment_on_issue note instead of starting something you " +
			"cannot finish."
	} else {
		s += " Nothing in the sandbox survives it, and this task has no branch to push: " +
			"your answer reaches anybody only through the comment_on_issue note that " +
			"closes this run, so keep back enough time to write it. An answer still in " +
			"the sandbox when the clock runs out reaches nobody."
	}
	s += " You will be told how much time is left, on every tool result, once it " +
		"runs low -- until then, work rather than counting."
	return s
}

// durableRuntimeSection is runtimeSection for a durable run, which has no
// deadline. It names the default idle timeout: the setting is a
// deployment's, and the prompt is built without it.
func durableRuntimeSection(task model.Task) string {
	s := fmt.Sprintf("\n\nThis run has no deadline; if it goes idle for %s it is suspended and resumed. "+
		"Idle means no output, no tool call and no wait in progress (`grain ci wait`, `grain task wait`, a "+
		"CI job, a chat wait), so work for as long as the work needs.",
		humanDuration(DefaultDurableIdleTimeout))
	if task.Target != nil {
		s += " A suspended run's sandbox goes with it, so only what you have pushed survives: commit " +
			"and push each piece of the work as it starts working."
	}
	return s
}

// nothingNeededSection tells a run about the nothing_needed tool, in the
// same words the tool's own description uses (mcp.NothingNeededDescription),
// and what calling it will do to this particular task -- which is the
// task's own choice (model.Task.NothingNeededAction), so the run is told
// the one that applies rather than both.
func nothingNeededSection(task model.Task) string {
	what := "closes this task"
	if task.NothingNeededAction.Resolved() == model.NothingNeededAck {
		what = "leaves this task for a human to acknowledge"
	}
	return "\n\nIf the task turns out to need nothing, call `nothing_needed` with your " +
		"reason: " + mcp.NothingNeededDescription + ". It ends your run the way " +
		"comment_on_issue does, posts the reason as the closing comment and " + what +
		". A finding, an answer or a recommendation somebody should read is still a " +
		"comment_on_issue note, and anything you pushed or asked in the same run still " +
		"wins: the pull request is opened, or the question is asked, with the reason " +
		"posted beside it."
}

// questionSummarySection is what every run is told about the one-line
// summary each question it asks carries (model.Comment.Summary): the inbox,
// the task list and notifications show nothing else of it, so a summary
// that says only that there is a question tells a person nothing.
const questionSummarySection = "\n\nWhenever you ask a person something -- ask_question, " +
	"request_secret, chat -- give a `summary` as well: one line, at most 100 characters, " +
	"naming the decision or input you need (\"Keep the v1 API alongside v2, or remove " +
	"it?\"). The inbox, the task list and notifications show only that line, with your " +
	"full question behind an expander, so never write \"I have a question\" or " +
	"\"Need input\"."

// statusSection is the paragraph about update_status: that a run can put
// one line on its own task's row saying what it is doing, and when doing
// so is worth a turn.
//
// It is here rather than left to the tool's own description for the
// reason every other paragraph in this prompt is: a run reads its tools
// when it wants one, and a tool nobody goes looking for is a tool nobody
// finds. Nothing in the sandbox suggests that anybody is watching the
// task at all, so a run has no reason to imagine there is a row to write
// on -- and this is the one tool here whose whole value is to somebody
// outside the run, which makes it exactly the one a run optimising for
// its own task would never reach for unprompted.
//
// What it deliberately does not do is ask for a status per turn. The call
// costs a turn like any other, and a run that narrates every file it
// opens has spent its budget on narration; the paragraph names the
// moments worth marking -- a long build, a wait on CI, a change of
// approach -- and says the quiet stretches are fine.
//
// Which is also why it now leads with what grain writes rather than with
// the tool. Every tool call a run makes puts a line on that row by itself
// (mcp.Registry.AnnounceActivity) -- run_command's own `description`
// where it wrote one, the tool's own name otherwise -- so the row keeps
// up whether or not the agent says anything, and update_status is left
// with what a tool name cannot say: why this file, what went wrong with
// the last approach. Naming the free half first is what stops a run from
// paying a turn to announce the build it is about to start in the same
// words it is about to write into the command's own description.
//
// canReachDaemon is canOpenPullRequest under the name that actually
// applies here. The two tools are registered by the same -server/-task
// pair (pkg/cli/mcpserver.go), so a Framework that can open a pull
// request can also carry a status and one that cannot has neither; the
// caller's bool answers both questions, and a deployment without that
// route never reads about a tool it does not have.
func statusSection(canReachDaemon bool) string {
	if !canReachDaemon {
		return ""
	}
	return "\n\nSomebody may be watching this task's row in grain's UI while your run " +
		"lasts, and one line on it says what you are doing. Most of it writes " +
		"itself: grain puts a line there for every tool call you make, from the " +
		"tool you called -- and for `run_command`, from its own `description` -- so " +
		"give each command a description written for a person who cannot see the " +
		"command (\"running the test suite\", \"pushing the branch\") and the row " +
		"keeps up with you at no cost. `grain status \"<note>\"` is for what the tool you are " +
		"calling cannot say on its own: which package you are reading your way " +
		"around, why you have changed approach, what you are waiting for. Call it " +
		"at those moments and not otherwise -- it costs a turn like any other call, " +
		"and repeating what the command you are about to run already says wastes " +
		"one. Neither reaches anybody as a question: ask_question and " +
		"comment_on_issue are still the only ways to say something a human has to " +
		"answer or read."
}

// chatSection is what a run dispatched for a task somebody is sitting
// with is told about the person on the other end -- and it is the one
// paragraph here that changes what the run is *for*.
//
// A background run is a piece of work with a report at the end of it. An
// interactive one is a conversation: the model has a tool that posts into
// the task's chat and blocks until a person answers (mcp's chat), so the
// cheapest thing it can do about an ambiguity is ask, in the same run,
// with everything it has already read still in front of it. Left to the
// tool description alone, a run reads its task, guesses at the ambiguity
// and delivers -- which is exactly right for every other dispatch and is
// the wrong instinct here.
//
// It corrects the sentence statusSection has just finished on, which
// promises that ask_question and comment_on_issue are the only ways to
// reach a human. For this run they are not, and they are also not the
// ones to reach for: both end the turn and wait for a whole new dispatch,
// which is the cost this tool exists to remove.
//
// canReachDaemon is statusSection's own condition, and the same one: chat
// is registered by the -server/-task pair that registers open_pull_request
// (pkg/cli/mcpserver.go), so a Framework with no daemon to ask has
// neither, and a prompt naming a tool that is not on the roster costs a
// run turns it cannot get back.
func chatSection(task model.Task, canReachDaemon bool) string {
	if !task.Interactive || !canReachDaemon {
		return ""
	}
	return "\n\nThis task is a live chat: somebody filed it to work through with you and " +
		"is watching its conversation now. `chat` is how you talk to them -- it posts " +
		"your message into this task's chat and blocks until they reply, so their " +
		"answer comes back as that call's own result and you keep your sandbox, your " +
		"reading of the code and everything else you already have. Use it early and " +
		"use it often: ask when two readings of what they want would lead to " +
		"different work, show them what you found before acting on it, and tell them " +
		"when something you both expected turns out not to be true. That is cheaper " +
		"here than anywhere else in grain, and guessing is dearer -- there is " +
		"somebody right there to ask. Prefer it to ask_question and " +
		"comment_on_issue, which end your turn and wait for a whole new run to be " +
		"dispatched before anyone hears the answer. If a chat call comes back saying " +
		"nobody replied, they have stepped away: finish or push what you have and " +
		"stop, rather than waiting again -- their next message starts a fresh run " +
		"with this whole conversation in front of it. A reply without a tool call " +
		"ends your turn, and nothing happens after it until they write again: never " +
		"end on \"checking now\" or \"back in a minute\". Use `chat` for interim " +
		"updates, and keep working in the same turn."
}

// resolvePromptExtension is the layers of standing instructions this run
// is given, resolved into the one block of text that reaches its prompt:
// the deployment's (deployment, which RunCycle refreshes out of
// grain_config every cycle), this task's target repo's, and the task's
// own override -- see model.PromptExtensionFor for how they compose, and
// model/prompt_extension.go for why.
//
// The repo layer is itself two: what an operator typed into grain's
// settings for that repository, and what the repository declared for
// itself in `.grain/settings.yml`, appended in that order
// (model.RepoConfig.RepoPromptExtension). A repository can add to its
// own standing instructions and can never discard the operator's, which
// is the whole of what grain/task-340 lets a repository say here.
//
// The repo layer is read here rather than carried on Config beside the
// deployment's, for the reason a task's grants are resolved here too:
// it is keyed by something only the task in hand names, so there is no
// one value a cycle could have refreshed ahead of time. A task with no
// target reads no row at all and gets the deployment's own text, which
// is the only layer that could apply to it.
//
// A store read that fails fails the dispatch, the same as the two reads
// RunDispatch makes just before calling this -- the task's conversation
// and its attachments. All three are the same broken store, and running
// an agent with instructions somebody wrote but grain could not read is
// worse than not running it, silently so: nothing in the prompt would
// say a layer was missing.
func resolvePromptExtension(ctx context.Context, store *model.Store, deployment string, task model.Task) (string, error) {
	var repo string
	if task.Target != nil {
		rc, err := store.GetRepoConfig(ctx, *task.Target)
		if err != nil {
			return "", fmt.Errorf("orchestrator: reading %s's repo config: %w", task.Target, err)
		}
		var declared string
		if rc != nil {
			declared = rc.DeclaredPromptExtension
		}
		var repoPrompt string
		if rc != nil {
			repoPrompt = rc.PromptExtension
		}
		repo = model.JoinTrimmed("\n\n", repoPrompt, declared)
	}
	return model.PromptExtensionFor(deployment, repo, task.PromptExtension), nil
}

// resolveMemory is what grain has learned about working here, resolved
// into the one block of text this run's prompt carries: the
// deployment-wide memory and, for a task with a target, that repo's own
// (model.MemoryFor, and model/memory.go for what memory is and why it is
// not the prompt extension next door).
//
// Both layers are read here, per dispatch, rather than refreshed onto
// Config by the cycle the way the deployment's prompt extension is. The
// repo layer has to be, since only the task in hand names the repo -- and
// the deployment layer joins it because memory is written *by runs*,
// through the memory capability, while other runs are in flight: a value
// cached at the top of a cycle would hand a dispatch what was true before
// last night's memory agent finished, which is the one reading memory
// must not give.
//
// A store read that fails fails the dispatch, exactly as
// resolvePromptExtension's does and for the same reason: a run given
// instructions somebody wrote but grain could not read is worse than no
// run at all, silently so.
//
// The index of what grain keeps in full (model.MemoryDocument) is joined
// on the end of the same block, for the runs that can act on it: one
// line per document in the two layers above, naming it, what it is about
// and how old it is, so a run knows what it could look up without being
// made to read any of it. now is what turns a row's timestamp into that
// age.
//
// Only for a task holding the memory:read scope (via api:memory,
// api:memory:read, or the bare memory alias): telling every other run
// that a note it has no way to open exists would be a line of prompt
// whose only possible effect is to send a run looking for a tool it does
// not have.
func resolveMemory(ctx context.Context, store *model.Store, task model.Task, now time.Time) (string, error) {
	deployment, err := store.GetMemory(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading this deployment's memory: %w", err)
	}
	var repoMemory *model.Memory
	if task.Target != nil {
		repoMemory, err = store.GetMemory(ctx, task.Target)
		if err != nil {
			return "", fmt.Errorf("orchestrator: reading %s's memory: %w", task.Target, err)
		}
	}
	text := model.MemoryFor(memoryText(deployment), memoryText(repoMemory))
	if !hasAPIScope(task, "memory:read") {
		return text, nil
	}
	scopes := []model.MemoryScope{{Repo: nil}}
	if task.Target != nil {
		scopes = append(scopes, model.MemoryScope{Repo: task.Target})
	}
	docs, err := store.ListMemoryDocuments(ctx, scopes...)
	if err != nil {
		return "", fmt.Errorf("orchestrator: listing what grain keeps in full: %w", err)
	}
	return appendSection(text, memoryIndexSection(model.MemoryIndex(docs, now))), nil
}

// memoryText is m.Text for a memory that exists, and "" for the nil
// GetMemory returns when nothing has been written for that scope --
// which is every scope until the memory agent has run once.
//
// A layer a person wrote by hand is prefixed with the line saying so
// (model.MemoryHandNote): every prompt tells a run that memory is an
// earlier run's observations, to be replaced when they stop being true,
// and that reading is wrong about the one an operator typed on the
// Settings pane. Said per layer rather than once for the whole block,
// because the two are written by two different people at two different
// moments and a note that covered both would be wrong about one of them.
func memoryText(m *model.Memory) string {
	if m == nil {
		return ""
	}
	if note := model.MemoryHandNote(m.UpdatedBy); note != "" {
		return note + "\n\n" + m.Text
	}
	return m.Text
}

// resolveCloneURL is the address this run's checkout is cloned from when
// its repository configures one of its own (model.RepoConfig.CloneURL,
// through the team's effective config), and "" -- the git proxy, by way
// of checkoutURL -- otherwise. A store read that fails fails the
// dispatch, for the reason resolveSetupCommand's does.
func resolveCloneURL(ctx context.Context, store *model.Store, task model.Task) (string, error) {
	if task.Target == nil {
		return "", nil
	}
	// grain/config is a virtual repository owned by this controller. It is
	// always reached through Config.GitRemoteBase's authenticated local alias,
	// even when an upgraded database still carries the CloneURL row older
	// config-mirroring builds wrote for it. Treating that legacy value like an
	// ordinary ambient-credential remote sends the checkout away from the
	// controller and defeats both the stable identity and native pull-request
	// lifecycle. An empty configured URL makes checkoutURL select the proxy.
	if model.IsConfigRepo(*task.Target) {
		return "", nil
	}
	eff, err := store.EffectiveRepoConfig(ctx, *task.Target)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading %s's clone URL: %w", task.Target, err)
	}
	return eff.CloneURL, nil
}

// resolveGateIsGrainCI answers whether GitHub's check runs are a source
// of verdicts for this task's repository, composing the deployment-wide
// declaration with the repository's own the one way they compose: either
// saying so is enough, and a repository cannot take itself back out of a
// deployment-wide one (repoIgnoresGitHubCI has why).
//
// What it changes is what this run's own CI tools say. A run told to
// read pull_request_status on a repository whose gate is grain CI would
// otherwise be handed a dozen red checks nobody is reading and spend its
// turns -- and a paragraph of its closing note -- explaining them away.
func resolveGateIsGrainCI(ctx context.Context, store *model.Store, task model.Task) (bool, error) {
	deployment, err := deploymentIgnoresGitHubCI(ctx, store)
	if err != nil {
		return false, err
	}
	if deployment {
		return true, nil
	}
	if task.Target == nil {
		return false, nil
	}
	eff, err := store.EffectiveRepoConfig(ctx, *task.Target)
	if err != nil {
		return false, err
	}
	if eff.CIMode != "" {
		return eff.CIMode == model.CIModeGrain, nil
	}
	return repoIgnoresGitHubCI(ctx, store, *task.Target, false)
}

// resolveSetupCommand is the shell this run's checkout needs before its
// first turn -- model.RepoConfig.SetupScript for the task's target repo,
// and "" for a task with no target or a repo that configures none.
//
// SetupScript rather than the field, because there are two layers of it
// as there are of the prompt extension: an operator's command and the
// one the repository declared for itself, both run, the operator's
// first (grain/task-340).
//
// A second read of the row resolvePromptExtension just read, rather than
// one read threaded through both: they are two independent facts about
// the repo, read at two different moments (the extension composes with
// two other layers before it reaches a prompt; this one is handed
// straight to prepareCheckout), and one sqlite row read twice per
// dispatch costs nothing measurable against the clone that follows it.
//
// A store read that fails fails the dispatch, for the reason
// resolvePromptExtension's does: a run started in a checkout that was
// never set up, because grain could not read the row saying how, is a
// run that fails later for reasons nothing in its prompt explains.
func resolveSetupCommand(ctx context.Context, store *model.Store, task model.Task) (string, error) {
	if task.Target == nil {
		return "", nil
	}
	eff, err := store.EffectiveRepoConfig(ctx, *task.Target)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading %s's setup command: %w", task.Target, err)
	}
	rc, err := store.GetRepoConfig(ctx, *task.Target)
	if err != nil {
		return "", fmt.Errorf("orchestrator: reading %s's repo config: %w", task.Target, err)
	}
	var declared string
	if rc != nil {
		declared = rc.DeclaredSetupCommand
	}
	return model.JoinTrimmed("\n", eff.SetupCommand, declared), nil
}

// sandboxDirsSection says where a run's tools actually land, and that
// the environment its own framework described to it is somewhere else.
//
// Every framework grain drives opens its run with a preamble grain does
// not write, describing the process that framework is running in.
// Claude Code's is an "Environment" block, and on this deployment it
// reads "Primary working directory: /" and "Is a git repository: false".
// Both are true of the controller, which is where the CLI runs and must
// run -- the agent's credential never enters the sandbox, so the sandbox
// is reached only over MCP (see pkg/agent/claude's own package comment).
// Neither is true of anything the run can touch: every tool call lands in
// a sandbox elsewhere, where run_command starts in the directory
// sandboxWorkingDir found and prepareCheckout has left a clone of the
// task's repo, on the task's branch, under it.
//
// A run reconciling those two invents the same path every time: an
// absolute /work/..., out of the "./work" the sentence above this one in
// the prompt gives it and the "/" its framework claims to be sitting in.
// Over the 185 runs this deployment had recorded when this was written,
// 105 read_file and edit_file calls failed on such a path, across 84
// distinct runs -- 45% of every run ever dispatched had lost at least one
// turn to it, and five had gone on to try /work/work/... trying to
// reconcile the absolute guess with the relative form they were given.
// The rate was not falling: 18 on one day, 73 on the next.
//
// So both forms are named here, absolute first, since the absolute one
// is the form a file tool is actually called with. mcp's file tools
// recover the guess when it is made anyway and say that they did
// (mcp.workspaceRetry); this is the half that stops it being made.
//
// Silent when sandboxRoot is "" -- a sandbox that could not be asked
// where it starts. A prompt with no path in it is what every run got
// before this, and a path invented here rather than measured would be
// the same wrong sentence with grain's name on it.
func sandboxDirsSection(checkoutDir, sandboxRoot string) string {
	if sandboxRoot == "" {
		return ""
	}
	s := fmt.Sprintf(
		"\n\nWhatever your agent framework has told you about its own environment -- a "+
			"working directory, whether it sits in a git repository -- describes the "+
			"machine that framework runs on, not your sandbox. Your tools all reach the "+
			"sandbox instead, where run_command starts in %s.",
		sandboxRoot)
	if checkoutDir != "" {
		s += fmt.Sprintf(
			" The checkout is %s: name files by that absolute path in read_file, "+
				"edit_file and write_file, or relative to the directory those commands "+
				"start in (%s/...). /%s/... is not a path that exists in the sandbox at all.",
			path.Join(sandboxRoot, checkoutDir), checkoutDir, checkoutDir)
	}
	return s
}

// sandboxToolingSection says what a run will not find on the machine
// sandboxDirsSection has just pointed it at, and what to do about it.
//
// A dispatched run discovers the sandbox's toolchain by running things
// in it, and the discovery is not free. grain/task-215 counted 67
// `command not found` failures across 183 runs of one deployment; 42 of
// them were procps' own binaries, which is why procps, file and
// shellcheck are in the Granulefile's package list today.
// That fixes the image and not the problem: the guest a run lands in is
// whatever its deployment last pulled, not whatever this tree installs
// -- on 2026-09-08 this deployment's own guest was three days behind
// main and still had none of those three -- and the next tool a run
// reaches for is one nobody has counted yet. So this says the general
// thing rather than enumerating a list that is wrong for some sandbox
// somewhere the day it is written.
//
// Three sentences, and each of them is a turn somebody has already
// spent:
//
//   - `gh`, which is the one measured absence that is deliberate and
//     permanent. Ten runs reached for it after a CI answer told them a
//     failing check had no readable log; the sandbox has no GitHub
//     credential for it to use and the MCP tools answer from the
//     controller's, which is docs/design.md's "The sandbox image has no
//     `gh`". Said here as well as in that answer (pkg/mcp's
//     failingCheckPages) because a run reads this before it has failed a
//     check, and the reach for `gh` happens the first time it wants to
//     know what CI said.
//   - apt, because the image carries no package lists: `apt-get install`
//     alone fails on a sandbox with a perfectly good mirror in front of
//     it, and the update that fixes that costs about ten seconds. The
//     nudge to report it is what turns a per-run workaround into a
//     deployment's own sandbox image (model.Config.SandboxImage),
//     which is where a package every run wants belongs.
//   - exit 127, which is the failure that costs more than the missing
//     binary does. `command not found` is a non-zero exit like any
//     other, so a probe at the head of an `&&` chain takes the rest of
//     the chain with it -- in one run, the `git commit` it ended with,
//     which is a whole turn's work lost to a `pgrep` that was only ever
//     asking a question.
//
// The GitHub sentence is skipped for a task with no repo attached: there
// is no branch, no pull request and no CI for those tools to report on,
// so it would be three tool names of noise. The other two hold for every
// run in every sandbox.
func sandboxToolingSection(hasTarget, grainCI bool) string {
	s := "\n\n"
	if hasTarget && grainCI {
		s += "A sandbox has no GitHub API access and no credential for one, so read CI with " +
			"`grain ci wait`, `grain pr status` and `grain pr base-status` rather than " +
			"reaching for `gh`, which the stock sandbox image does not carry. "
	} else if hasTarget {
		s += "A sandbox has no GitHub API access and no credential for one, so read CI with " +
			"`wait_for_checks`, `pull_request_status` and `base_branch_status` rather than " +
			"reaching for `gh`, which the stock sandbox image does not carry. "
	}
	s += "Anything else you find missing is usually a Debian package away -- the image keeps no " +
		"apt package lists, so it is `sudo apt-get update && sudo apt-get install -y <package>`, " +
		"about ten seconds -- and worth naming in your closing note if it is something every run " +
		"here would want, since a deployment can bake it into its own sandbox image instead. " +
		"Check with `command -v` before you chain, though: a command that is not installed exits " +
		"127, and one at the head of an `&&` chain takes the rest of the chain with it, including " +
		"the `git commit` at the end of it."
	return s
}

// grainCLISection is the short reference to the `grain` CLI every run with
// a sandbox has (docs/design/sandbox-first-agent-surface.md §4): the MCP
// roster is the control set alone, so this is the only place the rest of
// what grain offers a run -- the namespace, CI, the pull request, the
// collaboration helpers -- is named at all. `grain help` and `grain <cmd>
// --help` carry the detail, which is why this stays a page.
func grainCLISection(task model.Task) string {
	repo := "<owner>/<repo>"
	if task.Target != nil && task.Target.Owner != "" && task.Target.Name != "" {
		repo = task.Target.Owner + "/" + task.Target.Name
	}
	s := "\n\n### Grain CLI (`grain`)\n\n" +
		"Inside your sandbox, the `grain` CLI (run it with `run_command`) reaches this deployment's " +
		"namespace, CI, pull request, merge queue and the capabilities granted to this task, with no " +
		"tool-result size cap -- pipe it to `grep`, `jq` or a file:\n\n" +
		"- **Namespace** (`ci://`, `src://`, `task://`, `config://`, `memory://`, `deploy://`): " +
		"`grain ls <uri>`, `grain read <uri> [--tail N]`, `grain grep <re> <uri>`. For example " +
		"`grain ls ci://" + repo + "`.\n"
	if task.Target != nil {
		s += "- **CI & pull request** (this task's branch): `grain ci wait [--jobs j1,j2]` (give " +
			"`run_command` `timeout: 600000`; time blocked is credited back to your deadline), " +
			"`grain ci run-job <job>`, `grain ci retrigger`, `grain ci list`, `grain ci log [<run-id>] " +
			"[--job <j>] [--step <s>] [--tail N]`, `grain ci test-history <test>`, `grain pr status`, " +
			"`grain pr base-status`, `grain pr open`, `grain pr reply`, `grain pr resolve`, " +
			"`grain pr review-comment`. Another task's CI in a repository you may read: " +
			"`grain ci list --task <id>`, `grain pr status --task <id>` (or `--ref <branch>`), " +
			"`grain ci log <run-id>`.\n"
	}
	s += "- **Task & collaboration:** `grain status \"<note>\"`, `grain handover write --file <path>`, " +
		"`grain task wait <id>`, `grain task message <id> \"<body>\"`, `grain notify --subject <s> " +
		"--title <t>`, `grain git revert --task <id> --reason \"<why>\"`, `grain merge-queue`.\n" +
		grainCLIGrantBullets(task.Grants) +
		"- **Turn-ending, parking and proposals stay tool calls:** use `comment_on_issue`, " +
		"`nothing_needed`, `ask_question`, `request_secret`, `wait_for_task` (`park: true`), " +
		"`resume_later` and `propose_task` directly, not as shell commands.\n\n" +
		"Run `grain help` or `grain <command> --help` for every flag."
	return s
}

// grainCLIGrants is the `grain` commands each of grain's own capability
// grants unlocks, in the order grainCLIGrantBullets lists them. A grant
// whose tools stay model tools (db-write) is not here: the
// tool roster already names those.
var grainCLIGrants = []struct{ grant, commands string }{
	{model.SelfDebugCapability, "`grain ls/read/grep src://...`, `grain task list`, `grain task read <id>`, " +
		"`grain task transcript <id>`, `grain metrics [<section>] [--window 7d | --since <t> --until <t>] [--repo <r>]`, `grain read deploy://pipeline-changes`"},
	{"grain-log", "`grain log [--source daemon|dmesg] [--since <t>] [--until <t>] [--priority err] [--contains <s>] [--max-lines N]`"},
	{"deployment-health", "`grain health`, `grain read deploy://state`"},
	{"bootstrap-playbooks", "`grain playbooks [<name>]`"},
	{"memory", "`grain memory list`, `grain memory read <key>`, `grain memory write [--repo owner/name] --file <f>`, " +
		"`grain memory doc-write <name> --summary <s> --file <f> [--repo owner/name]`"},
	{"release-notes", "`grain release-notes stats [--period <p>]`, `grain release-notes publish --title <t> --file <f> [--period <p>]`"},
	{"read-issues", "`grain issue list`, `grain issue read <n>`"},
	{"test-quarantine", "`grain quarantine <pkg> <test> --reason <why>`"},
	{"db-read", "`grain db query <sql>`"},
}

// grainCLIGrantBullets is one bullet per grant the task holds naming the
// `grain` commands it unlocks, plus one for any api grant.
func grainCLIGrantBullets(grants []model.Grant) string {
	held := map[string]bool{}
	names := make([]string, 0, len(grants))
	api := false
	for _, g := range grants {
		held[g.Capability] = true
		names = append(names, g.Capability)
		if strings.HasPrefix(g.Capability, model.APIGrantPrefix) {
			api = true
		}
	}
	apiSet := model.APIGrantsOf(names)
	if apiSet.Has("memory:write") {
		held["memory"] = true
	}
	if apiSet.Has("release-notes:read") || apiSet.Has("release-notes:write") {
		held["release-notes"] = true
	}
	var b strings.Builder
	for _, c := range grainCLIGrants {
		if held[c.grant] {
			fmt.Fprintf(&b, "- **%s:** %s.\n", c.grant, c.commands)
		}
	}
	if api {
		b.WriteString("- **api grants:** `grain api routes` lists the agent API routes they reach; " +
			"`grain api <METHOD> <path> [--body '<json>']` calls one. Routes a sandbox process may " +
			"not call stay `api_*` tool calls.\n")
	}
	return b.String()
}

// timezoneSection tells the run the current time in the task's own time
// zone, UTC when it names none or one this host cannot load.
func timezoneSection(task model.Task) string {
	return timezoneSectionAt(task, time.Now())
}

// withDeploymentTimeZone is task with the deployment's own zone in place
// of none: a task nobody filed from a browser (a schedule, a proposal, the
// CLI) tells its run the time on the deployment's clock rather than in
// UTC. The copy is the run's alone; nothing writes it back to the store.
func withDeploymentTimeZone(task model.Task, deployment string) model.Task {
	if strings.TrimSpace(task.TimeZone) == "" {
		task.TimeZone = strings.TrimSpace(deployment)
	}
	return task
}

func timezoneSectionAt(task model.Task, now time.Time) string {
	tzName := strings.TrimSpace(task.TimeZone)
	loc := time.UTC
	resolvedName := "UTC"
	if tzName != "" {
		if l, err := time.LoadLocation(tzName); err == nil {
			loc = l
			resolvedName = tzName
		}
	}
	tInLoc := now.In(loc)
	timeFormatted := tInLoc.Format("2006-01-02 15:04 MST")

	isDirect := task.Interactive || task.Origin.Reason == model.ReasonDirect || task.Origin.Reason == model.ReasonConsoleSession
	if isDirect {
		return fmt.Sprintf("\n\nUser timezone: %s (current time: %s)", resolvedName, timeFormatted)
	}
	return fmt.Sprintf("\n\nTeam timezone: %s (current time: %s)", resolvedName, timeFormatted)
}

// customFieldsSection tells the run what this deployment has recorded
// about the task on its own fields (model.Task.CustomFields) -- "" for
// a task carrying none, which is most of them.
//
// It is here because a field nobody tells the agent about is a field
// that changes nothing: "priority: urgent" or "customer: acme" is a
// fact about the work that whoever filed the task would otherwise have
// had to repeat in the body, in prose, task after task. grain itself
// still reads none of it -- it dispatches, orders and blocks on exactly
// what it did before -- so what a field *means* for the work is left to
// the words in it and to whatever standing instructions this deployment
// gives (model.Config.PromptExtension), which is where "urgent means
// stop and ask" belongs.
//
// Names, not labels: a run gets the task, and the labels live on the
// declarations in the store, which nothing here reads. The name is what
// a field is called everywhere else an agent could see it -- the state
// repository's own tables/custom_field.json included -- so it is the
// spelling that lets a run join the two up.
//
// Sorted by name, like every other list in this prompt: two runs of one
// task should be handed the same text.
func customFieldsSection(task model.Task) string {
	if len(task.CustomFields) == 0 {
		return ""
	}
	names := make([]string, 0, len(task.CustomFields))
	for name := range task.CustomFields {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString("\n\nThis deployment records fields of its own on a task, and this task carries:\n")
	for _, name := range names {
		// A field declared `multiple` carries a set of values
		// (model.CustomField.Multiple), written as the one comma-separated
		// line a reader of this prompt would write themselves. There is
		// nothing here that needs them apart, and a bullet per value would
		// bury the field's own name.
		fmt.Fprintf(&b, "- %s: %s\n", name, strings.Join(task.CustomFields[name], ", "))
	}
	// Said here rather than only in propose_task's own description,
	// because this is where the names are: a run that has read this list
	// knows what its deployment calls a customer or a team, and a
	// proposal filed without them is a piece of the same job that has
	// dropped off whatever the humans here sort and filter by
	// (grain/task-105). Only the carrying-forward is suggested -- what a
	// value should be for work that is not simply a piece of this task
	// is not something a run can know from one task's fields.
	//
	// The note about the list is worth its line because the two shapes
	// differ: a field holding a set is *read* above as one comma-joined
	// line, and a run that copied that line back verbatim would be
	// asking for a single value with a comma in it, which is what the
	// declaration would then dutifully store.
	b.WriteString("\nWork you propose (propose_task) can carry these too -- its `custom_fields`, " +
		"keyed by the same names. State the ones that are still true of the work you are " +
		"proposing; leave out the ones that are not. A field listed above with several " +
		"values holds a set: give those as a list of strings rather than as the one line " +
		"they are written on here.")
	return strings.TrimRight(b.String(), "\n")
}

// issueSection tells the agent which GitHub issue this task is associated
// with, so the agent knows the task resolves that issue and writes clean
// commit messages attributing it.
func issueSection(task model.Task) string {
	if task.Issue <= 0 {
		return ""
	}
	return fmt.Sprintf("\n\nThis task is associated with GitHub issue #%d. Reference or attribute it in commit messages where appropriate.", task.Issue)
}

// setupSection tells the run what the repo's setup command did in the
// checkout it is about to work in -- "" for a repo that configures none,
// which is most of them.
//
// A *failed* setup is the case this exists for. The alternative is a run
// handed a working directory that does not build, with nothing saying
// so: the first `go test` or `npm test` fails on a missing dependency,
// and the run spends its turns debugging the repo's toolchain -- or
// worse, "fixing" the code to match a broken tree. Told plainly, the
// same run can install the missing thing itself in one command, or say
// what is wrong in its closing note.
//
// A setup that *succeeded* is worth a line too, though a much shorter
// one: it tells a run that `make deps` has already happened, so it does
// not run it again, and names the command so that a run which needs it
// again after touching a manifest knows exactly what to re-run.
func setupSection(r *SetupResult) string {
	if r == nil {
		return ""
	}
	if !r.failed() {
		return fmt.Sprintf(
			"\n\ngrain ran this repo's own setup command in that checkout before your "+
				"first turn, and it succeeded (exit 0):\n\n%s\n\nSo whatever it installs "+
				"or generates is already in place -- you do not need to run it again "+
				"unless you change something it depends on.",
			r.Command)
	}
	return fmt.Sprintf(
		"\n\ngrain ran this repo's own setup command in that checkout before your first "+
			"turn, and it FAILED (exit %d):\n\n%s\n\nThe last of what it printed:\n\n%s\n\n"+
			"So the checkout may be missing dependencies or generated files, and a build "+
			"or test failing for that reason is this, not your change. Deal with it first "+
			"-- fix it or work around it if you can, and if you cannot, say so plainly in "+
			"what you report rather than reading it as a failure of the task.",
		r.ExitCode, r.Command, r.Output)
}

// stateRepoSection is what a run working in a grain state repository
// (pkg/staterepo) is told about the thing it has just been handed: not
// source code but this deployment's editable configuration and memory,
// written out of grain's database as text.
//
// What it deliberately does not say is whose database that is -- the
// checkout cannot answer that, and settingsRepoSection below is where
// this deployment's own answer to it goes.
//
// Every fact here is one the checkout does not volunteer. The files are
// JSON, so their shape is discoverable, but nothing in them says that
// grain rewrites all of them from its database on a timer -- which is
// what makes formatting a matter of correctness rather than taste -- and
// nothing in them says which tables are missing and why. Only the
// settings tables and the memory ones are exported, by
// staterepo.ExportConfig: grain's own record of what it did stays in
// SQLite, so a run
// sent here for a task's history finds no file for it, and a run that
// writes one -- to "fix" a failed attempt, or to leave a note where it
// thinks grain keeps them -- has its file deleted by the next export
// rather than imported. Both halves are said out loud, because the
// absence of a file is the one thing a directory listing cannot explain.
//
// The state repository's own README.md says some of this, and a careful
// run would find it. This says it before the first turn instead, for the
// same reason BuildPrompt names the branch rather than leaving it to be
// deduced: a fact stated in the prompt is one nobody spends a turn
// rediscovering, and one nobody rediscovers wrongly.
//
// The check at the end is the whole reason `grain state check` exists
// (pkg/cli/state.go): staterepo.Import is the only real answer to "is
// this loadable", and a run that never runs it finds out through a
// deployment that will not start after the merge.
//
// It is spelled as a `docker run` rather than as `grain state check .`,
// which is what this paragraph asked for until grain/task-277, because
// the shorter command was not true of any sandbox grain builds: there is
// no `grain` binary in one (the repository's Granulefile installs a
// toolchain, not this deployment), so every run that followed the
// instruction spent a turn on `grain: command not found` and then went
// looking for what to do instead. Docker is there, and grain publishes
// one image per build and no bare binaries, so the image *is* how grain
// runs in a sandbox -- and it is already the shape of Grain CI's built-in
// config-repository check: a checkout and this one command.
//
// Shipping a grain binary in the guest image would have made the
// shorter command true and answered about the wrong build: a guest is
// built from a tree and a deployment runs whatever it has been upgraded
// to, and `grain state check` refuses a dump stamped with a schema it
// does not know ("repository is at schema N, this build knows M"). That
// failure reads exactly like a broken change, on a run whose change is
// fine. The image named here is Config.CheckImage -- what grain passes
// to its built-in config-repository CI step, and so the one build whose answer
// means anything -- which costs an image pull of a minute or so on the
// first check of a run, and seconds on every one after it.
//
// The settings tables are named from staterepo.SettingsTables rather
// than written out here, and that is not only to save keeping two lists
// in step. That list is the one grain acts on -- it decides what Apply
// imports into a running daemon -- so naming it here means the prompt
// cannot tell a run that a table is settings when grain will not treat
// it as such. A rename in pkg/model would otherwise leave this
// paragraph confidently naming tables that no longer exist.
func stateRepoSection(prepared checkout, image string) string {
	if !prepared.StateRepo {
		return ""
	}
	return "\n\nThis repo is grain's own state: its editable configuration and memory, " +
		"exported as text. " +
		"`tables/<name>.json` is one file per table, holding a JSON array with one " +
		"object per row and that table's columns in their declared order, rows sorted " +
		"by primary key. grain rewrites every one of those files from its database on " +
		"a timer, so keep that shape exactly -- a file that comes back in a different " +
		"shape is a diff against grain's next export rather than a change to it. " +
		"`schema-version` stamps the schema the dump was written by; leave it alone, " +
		"and never touch `secrets.enc`, which is grain's encrypted secret store and " +
		"nothing a task has business editing.\n\n" +
		"The tables that are settings -- the ones a task is normally asked to change, " +
		"and the only ones a merge applies to a running grain -- are exactly these: " +
		strings.Join(staterepo.SettingsTables, ", ") + ". They are templates, suites, " +
		"schedules, per-repo configuration, qualification plans and the deployment's " +
		"own config row (`grain_config`, which carries the prompt extension every run " +
		"is given), together with the `_read`, `_grant` and `_sequence` tables that " +
		"belong to them.\n\n" +
		"Beside them are `tables/memory.json` and `tables/memory_document.json`, what " +
		"grain's memory agent has written. That is the whole export. grain's own record " +
		"of what it has already done -- `task`, `task_run`, `task_comment`, " +
		"`task_observation`, `task_attachment`, `lease`, `branch`, `release`, " +
		"`qualification_run`, the `suite_run` tables and their like -- is not exported " +
		"at all: those rows stay in grain's database on the server, so there is no " +
		"`tables/task.json` or `tables/task_run.json` here to read, and a file you write " +
		"for one is deleted by grain's next export rather than imported. If you need to " +
		"know what this deployment has actually run, that is the self-debug capability's " +
		"task tools (`list_grain_tasks`, `read_grain_task`); without them, say the " +
		"history is not available to you rather than inferring it from the settings.\n\n" +
		"Check what you propose before it merges. There is no `grain` binary in your " +
		"sandbox -- grain publishes one container image per build and no bare binaries " +
		"-- so the check is the one `docker run` Grain CI also runs:" +
		"\n\n" + stateCheckCommand(prepared, image) + "\n\n" +
		"That image is the build this deployment is running, and it has to be: `grain " +
		"state check` refuses a dump stamped with a schema it does not know. The first " +
		"check you run pulls the image and takes a minute or so; every one after it takes " +
		"seconds. It loads the whole " +
		"directory into a throwaway database and reports what breaks. A malformed file, " +
		"or a row missing a column the schema requires, otherwise fails when grain next " +
		"reads the repository, which is the worst place to find out. Grain CI runs that " +
		"same check for every pushed config branch, outside this repository so the branch " +
		"cannot remove the check that judges it, and its successful verdict is required " +
		"before the change can merge. A red verdict means grain would not have loaded what " +
		"you wrote. A merged change to " +
		"a settings table is pulled in and applied within the minute, without a restart; " +
		"everything else waits for one. Either way the import is a replacement, so a row " +
		"you delete is a row that is gone."
}

// stateCheckCommand is that one command, written out so that a run can
// paste it rather than assemble it: the checkout mounted read-only at
// /state, and grain's own image asked about what is there.
//
// Word for word the command in Grain CI's built-in config-repository
// workflow, which is deliberate: the whole value of checking before the
// merge is that a green answer here means
// a green tick there, and two commands that differ in any argument are
// two checks a run would have to reason about the difference between.
//
// The mount is spelled absolutely whenever the sandbox has said where
// its working directory is (checkout.Root), because docker resolves a
// bind mount against the daemon's filesystem rather than against a
// shell's idea of "." -- so a relative path is either a silent mount of
// the wrong directory or an error, depending on the version. A sandbox
// that could not be asked (Root is "", which is a slot exposing no
// run_command tool at all -- see sandboxWorkingDir) gets the `cd` form
// instead, which needs no absolute path and is the same command the
// workflow runs in a checkout of its own.
func stateCheckCommand(prepared checkout, image string) string {
	if image == "" {
		image = staterepo.DefaultCheckImage
	}
	run := func(mount string) string {
		return "docker run --rm -v " + mount + " " + image + " state check /state"
	}
	if prepared.Root == "" {
		return "cd " + prepared.Dir + " && " + run(`"$PWD:/state:ro"`)
	}
	return run(path.Join(prepared.Root, prepared.Dir) + ":/state:ro")
}

func directorStateSection(prepared checkout, task model.Task) string {
	id, ok := model.ConsoleSessionIDOf(task)
	if !ok || id != model.DirectorSessionID {
		return ""
	}
	path := DirectorStateDir
	if prepared.Root != "" {
		path = strings.TrimRight(prepared.Root, "/") + "/" + DirectorStateDir
	}
	switch prepared.DirectorState {
	case directorStateReady:
		return fmt.Sprintf("\n\nGrain checked out this deployment's state repository read-only at `%s`. Read the JSON files under `tables/` there to inspect this deployment's configuration and memory -- that export holds no tasks, runs or comments, so what this deployment has actually run is read with the self-debug task tools if you hold them, and reported as unavailable if you do not. Its `origin` uses grain's scoped Git proxy, and pushing is disabled. For any other repository listed in this task's read scope, clone `%s/OWNER/REPO.git`; credentials are supplied by the sandbox and must not be copied into commands or output.", path, prepared.ProxyBase)
	case directorStateAbsent:
		return "\n\nThis deployment has no configured state repository, so no state checkout is available. Start normally and say that plainly when this deployment's own configuration is needed."
	case directorStateUnauthorized:
		return fmt.Sprintf("\n\nThis deployment has a state repository configured (%s), but this Director session was not granted read access to it. No state checkout is available; start normally and say that plainly rather than trying to work around the scope.", prepared.DirectorStateRepo)
	case directorStateUnavailable:
		if prepared.ProxyBase != "" {
			return fmt.Sprintf("\n\nThe configured state repository could not be checked out through grain's scoped Git proxy, so no state checkout is available for this run. Start normally and say that plainly. Other repositories in this task's read scope use `%s/OWNER/REPO.git`; credentials are supplied by the sandbox and must not be copied into commands or output.", prepared.ProxyBase)
		}
		return "\n\nThe configured state repository could not be checked out because grain's scoped Git proxy is unavailable. No state checkout is available for this run; start normally and say that plainly."
	default:
		return ""
	}
}

// grainSourceSection tells a self-debug run about grain-src/ -- including
// when it does not have one, since the self-debug guidance
// (model.SystemTemplate) sends every self-debug run to that directory,
// and an agent sent to a directory that is not there with no word why
// concludes something about grain that is not true.
func grainSourceSection(prepared checkout, task model.Task) string {
	path := GrainSourceDir
	if prepared.Root != "" {
		path = strings.TrimRight(prepared.Root, "/") + "/" + GrainSourceDir
	}
	switch prepared.GrainSource {
	case grainSourceReady:
	case grainSourceNotBaked:
		return fmt.Sprintf("\n\nThere is no `%s` in this run: this deployment has no baked-in copy of grain's source, so there is nothing to place there. Say so if what you were asked depends on it.", path)
	case grainSourceUnavailable:
		return fmt.Sprintf("\n\nThere is no `%s` in this run: placing this deployment's baked-in copy of grain's source there failed (%s). Say so if what you were asked depends on it.", path, prepared.GrainSourceWhy)
	default:
		return ""
	}
	at := "a revision this grain build does not record"
	if prepared.GrainSourceRevision != "" {
		at = fmt.Sprintf("`%s`, the revision this grain was built from", prepared.GrainSourceRevision)
	}
	history := "It carries no git history: this deployment was built without any, so `git log` and `git blame` do not work there."
	if prepared.GrainSourceCommits > 0 {
		history = fmt.Sprintf("It carries a shallow git history of the last %d commits, so `git log`, `git blame` and `git show` work back that far and no further; nothing can be fetched into it or pushed from it.", prepared.GrainSourceCommits)
	}
	return fmt.Sprintf("\n\nGrain placed this deployment's own baked-in copy of its source read-only at `%s`, at %s -- the code that is actually running. %s Use your standard sandbox tools (`run_command` with grep, find and git, or `read_file`) to inspect grain's behavior and implementation directly in that directory.", path, at, history)
}

// settingsRepoSection answers the question stateRepoSection above cannot:
// not "what is this tree" but "whose settings are these" -- which
// repository the deployment that dispatched this run keeps its own
// configuration in, and whether the checkout in front of the agent is it.
//
// The two are genuinely different facts, and the checkout only carries
// one of them. checkout.StateRepo is read out of the cloned tree (a
// tables/ directory beside a schema-version stamp), which is the same
// shape whether the repository is this deployment's own state, another
// installation's, or a copy somebody is editing offline -- its own doc
// comment says as much, and that is the right answer for a section whose
// subject is the layout. It is the wrong answer for a run that has been
// asked to change what this grain runs on: an agent that cannot tell
// this deployment's settings from some other grain's dump opens its pull
// request against a repository that changes nothing here, and one
// working in an ordinary repo has no way to discover where the settings
// it wants changed even live. Neither is discoverable from inside the
// sandbox: the git proxy serves every repo from the same address, and
// nothing in a checkout says which grain reads it.
//
// So this is the deployment's own answer, from Config.StateRepo, stated
// before the first turn for the reason every other fact in this prompt
// is (BuildPrompt's own doc comment): a fact stated in the prompt is one
// nobody spends a turn rediscovering, and one nobody rediscovers
// wrongly.
//
// Three shapes, and the third is the one every ordinary dispatch gets:
//
//   - the checkout is this deployment's settings, so a merge here is a
//     change to the grain running this task;
//   - it is not, and the settings are named, so a run that wants one
//     changed knows where they are and knows this is not it -- with the
//     extra warning for a checkout that does hold a dump, which is
//     exactly the tree an agent is most likely to mistake for this
//     deployment's own;
//   - the deployment named none, and nothing is said at all.
//
// That last case is silence rather than a hedge, and it covers two
// deployments that cannot be told apart from here: one whose state is
// local-only (no repository exists to name, so its settings are not
// something any pull request can reach) and one whose caller wired no
// answer in -- `grain demo`, a test, any embedding of this package that
// has no state repository of its own. Naming nothing is right for both;
// a paragraph saying grain does not know where its settings are would be
// prompt spent on a fact no run can act on.
//
// Comparison is case-insensitive on both halves, the same way every
// other repo comparison in grain is (gitproxy's canonicalMatches): a
// task targeting Acme/Grain-State and a remote spelling it
// acme/grain-state are the same repository to GitHub, and telling a run
// working in this deployment's own settings that its settings are
// somewhere else would be worse than saying nothing.
func settingsRepoSection(prepared checkout, target *model.RepoRef, settings model.RepoRef) string {
	if settings == (model.RepoRef{}) {
		return ""
	}
	if sameRepo(target, settings) {
		return "\n\nThe settings this deployment runs on are in this very repo: " +
			settings.String() + " is where the grain that dispatched you reads its own " +
			"configuration from. A change merged here is a change to the grain running this " +
			"task, not to some other installation's copy of it."
	}
	// "not in this repo" for the ordinary dispatch, which has one, and
	// the same fact without that clause for a task with no target at all
	// -- BuildPrompt has just told such a run there is nothing checked
	// out, and a sentence contrasting the settings with a repo it does
	// not have would read as one it was supposed to find.
	s := "\n\nThis deployment's own settings live in a repository of their own. The grain that " +
		"dispatched you reads its configuration"
	if target != nil {
		s = "\n\nThis deployment's own settings are not in this repo. The grain that dispatched " +
			"you reads its configuration"
	}
	s += " -- templates, suites, schedules, per-repo configuration " +
		"and its own config row -- out of " + settings.String() + ", its database exported as " +
		"text, and a change to any of that is a pull request against that repository rather " +
		"than anything you can edit from here. If the work in front of you turns out to need " +
		"one, say so in what you report."
	if prepared.StateRepo {
		s += " Note that what you have been handed does look like a grain state repository, " +
			"but it is not this deployment's: nothing merged here reaches the grain running " +
			"this task."
	}
	return s
}

// sameRepo reports whether target is settings, for settingsRepoSection's
// one comparison. A task with no target at all is nobody's settings
// repository, which is what the nil check answers.
func sameRepo(target *model.RepoRef, settings model.RepoRef) bool {
	return target != nil && target.Same(settings)
}

// promptExtensionSection is how that text is handed to the agent: named
// as standing instructions, so a run can tell something somebody chose
// for work here in general apart from the task it was filed under and
// from the facts grain states about the branch and the repo.
//
// The heading says nothing about which of the three layers wrote it.
// Whoever configured the deployment, the repo, or this one task all mean
// the same thing by it -- "this is how work is done here" -- and a
// heading that named the layer would be a fact about grain's own
// settings model, which is no use to the run reading it.
//
// "" for no extension at all, which is every deployment that has not
// written one and so the overwhelmingly common case -- an empty heading
// with nothing under it would be a paragraph of prompt saying there is
// nothing to say.
func promptExtensionSection(text string) string {
	if text == "" {
		return ""
	}
	return "Standing instructions for work on this deployment. They are not part of the " +
		"task above, and they do not replace anything grain has told you about the " +
		"branch, the repo or the checks -- follow them alongside all of it:\n\n" + text
}

// memorySection is how what grain has learned reaches the agent
// (model.Memory, resolveMemory): named as notes from earlier runs rather
// than as instructions, because that is what they are and because the
// difference decides what a run should do when a note turns out to be
// wrong. A standing instruction is a person's decision and is obeyed; a
// memory is a previous run's observation and can be out of date, in
// which case the run reading it should trust the tree in front of it and
// say so in what it reports.
//
// It sits beside the prompt extension rather than inside it, under its
// own heading, for the reason model/memory.go gives for keeping the two
// apart at all: they have different authors, and a run that cannot tell
// "somebody decided this" from "a run last week noticed this" has no way
// to weigh them against each other.
//
// "" for a deployment whose memory is empty, which is every deployment
// until the memory agent has run -- an empty heading with nothing under
// it would be a paragraph of prompt saying there is nothing to say.
func memorySection(text string) string {
	if text == "" {
		return ""
	}
	return "What grain has learned about working here, from earlier runs. These are " +
		"notes, not instructions: they were written by an agent reading what this " +
		"deployment has been doing, so trust them where they hold and trust the tree " +
		"in front of you where they do not -- and say so in what you report if one of " +
		"them is out of date:\n\n" + text
}

// memoryIndexSection frames the index of what grain keeps in full
// (model.MemoryIndex, resolveMemory): the notes that are deliberately
// not in this prompt, one line each.
//
// It sits inside the memory block rather than beside it -- memorySection
// wraps the two together -- because it is the same thing said at a
// different length, and a heading of its own would invite a run to weigh
// "notes" against "documents" as if the difference were about what they
// are rather than about how much of it there was room for.
//
// What it has to earn is the tool call it asks for, which is why it says
// what a summary is: a line here is all the prompt could afford, and a
// run that reads it as the whole of what is known will confidently do
// the thing the document exists to warn it about.
//
// "" for a run with nothing to look up, which is every dispatch until
// something has written a document.
func memoryIndexSection(index string) string {
	if index == "" {
		return ""
	}
	return "Longer notes are kept out of this prompt so it stays short. These exist, one line " +
		"each -- the line is a summary of the note, not the note. Read one in full with " +
		"read_memory_document when what it names touches what you are doing, and replace it " +
		"with update_memory if the tree says it has gone stale:\n\n" + index
}

// baseDescription names the branch a run's own branch has to keep
// merging into, for the sentence above -- task.Base when the task fixes
// one (directives.go's `/base`, and the stacked fix tasks the merge
// queue used to file, whose base was the branch they repaired), and
// otherwise the repo's default branch, which is whatever prepareCheckout's clone left
// at origin/HEAD. Unnamed rather than guessed in that second case:
// grain does not know the repo's default branch here, and naming the
// wrong one would send a run merging a branch that does not exist.
func baseDescription(task model.Task) string {
	if task.Base != "" {
		return "`" + task.Base + "`"
	}
	return "its base branch"
}

// proposalSection is the follow-on task etiquette every dispatch is told,
// and the two facts an agent cannot work out for itself that it needs to
// follow it: which task it is running as, and whether that task is an
// auto-merge job.
//
// Both are grain's own facts, the same reason BuildPrompt names the
// branch rather than letting an agent pick one. Without the task id, an
// agent splitting a piece out of the work it is doing has nothing to put
// in that proposal's depends_on -- it would have to reverse the id out of
// the branch name -- and relayProposedTasks resolves depends_on against
// real task ids, so a proposal that names nothing is filed unblocked and
// can be approved and dispatched beside the task it was meant to follow.
// The exception is the task that never finishes. "Task N itself" is the
// right advice for a run that lands a change and ends, and it is exactly
// wrong for a standing chat session (model.NeverCompletes): that task is
// never completed, a depends-on link is satisfied only by its target
// closing, and so every proposal the director filed naming its own
// session sat queued and undispatchable -- eight of them in one day,
// with nothing anywhere saying why (grain/task-487). Such a run is told
// the other half of the same paragraph, which is the half that applies
// to it, and told outright not to name itself. The filing path refuses
// the link either way (orchestrator.proposedDependencies,
// ui.Client.dependsOnLinks); this is the sentence that stops a run
// asking for it and losing the ordering it meant to state.
//
// Without knowing its own task auto-merges, an agent has no way to tell
// whether propose_task's auto_merge is even open to it: proposedAutoMerge
// caps a proposal at the proposing task's own setting, so the sentence is
// omitted, rather than negated, for a task that is not one -- there is
// nothing an agent could usefully do with "you may not ask for this".
//
// That sentence carries a second fact for the same reason: a proposal
// such a task makes is filed approved as well (model.AutoSubmits), so
// nobody stands between it and the queue. What a run does with that is
// depends_on -- the paragraph's opening advice stops being etiquette and
// becomes the only thing keeping a proposal behind the work it has to
// follow, since "unblocked when it is queued" is now "unblocked".
//
// Which is why the opening sentence says "as soon as it is queued"
// rather than "the moment a human approves it". Auto-merge is not the
// only permission that queues a proposal without a click -- a standing
// console session's deployment may have granted it the same
// (model.ConsoleSessionQueuesProposals) -- and this function cannot tell
// from the task alone, since that answer is a settings row away. A
// sentence true whoever is reading it is worth more here than a
// confident one that is wrong for the director: grain/task-311 is what
// a prompt asserting a gate the filing path does not apply costs.
//
// And it carries the bound on that fact, because the fact is not
// unconditional: past model.AutoSubmitLimits a proposal is filed for a
// person after all. The sentence says the shape of the bound rather than
// where this run stands in it -- nothing here can tell a run that
// without a store read this function does not make, and it would not
// change what a run should do anyway. What it does change is what a
// proposal's body is written for: a run told its proposals are never
// read writes them for nobody.
//
// An auto-merge task working against a base branch of its own is told
// the opposite, and here the negation earns its place: relayProposedTasks
// files a proposal against the repo's default branch, and auto-merge does
// not cross from one branch to another (model.SameBranch), so a run told
// only "this task is an auto-merge job" would split work out believing it
// lands the same way the whole of it would have. What it can do with
// that is real -- say in the proposal's body that it was meant to merge
// unattended, so whoever approves it can decide.
//
// Both of those sentences are about the repository this run is working
// in, which is why they now say so. A task with extra write targets has
// more repositories than that, and a proposal for one of them is judged
// by a rule of its own (model.LandsWhereGranted) -- writeTargetsSection
// follows this paragraph and says which.
//
// Which repository a proposal lands in is said here too, for a task that
// has one at all, because the default is a fact about grain rather than
// about the work: relayProposedTasks files a proposal against the
// proposing task's own repo unless the call names another
// (proposedTarget). A run that does not know that has no reason to name
// one, and the failure is silent and expensive -- a run that read this
// deployment's history in its state repository and proposed eight
// changes to grain's own code had all eight filed against the state
// repository, where a Go change cannot land, leaving a human to retarget
// seven of them by hand (grain/task-144). The `repo` argument's own
// description says the rest; this is the sentence that gets a run to
// read it.
func proposalSection(task model.Task) string {
	waitOn := fmt.Sprintf("task %s itself, when the follow-up only makes sense once "+
		"this task's own change has landed, and the id you gave an earlier proposal "+
		"in this same run that it builds on", task.ID)
	if model.NeverCompletes(task) {
		waitOn = "the id you gave an earlier proposal in this same run that it builds on"
	}
	s := fmt.Sprintf(
		"\n\nYou are running as task %s. Anything you split out with propose_task "+
			"should say what it has to wait on in depends_on: %s. "+
			"A proposal that names nothing is unblocked as soon as it is queued -- "+
			"which for most proposals is the moment a human approves them, and for "+
			"some is at once -- and can be dispatched beside work it was supposed to "+
			"follow.",
		task.ID, waitOn,
	)
	if model.NeverCompletes(task) {
		s += fmt.Sprintf(" Never name task %s itself: this is a standing chat session, a task "+
			"that by design never completes, and a depends-on link is satisfied only by its "+
			"target closing -- so a proposal waiting on this one would wait forever. grain "+
			"refuses such a link and says so in the proposal's body, but a proposal that asks "+
			"for it is one whose ordering nobody recorded.", task.ID)
	}
	if task.Target != nil {
		s += fmt.Sprintf(" A proposal is filed against %s, the repository you are "+
			"working in, unless you set propose_task's `repo` to another one -- do "+
			"set it for work that belongs elsewhere, because a task filed against a "+
			"repository its change cannot land in is one a human has to retarget "+
			"before it can run.", task.Target)
	}
	switch {
	case task.AutoMerge && task.Base != "":
		s += fmt.Sprintf(" This task is an auto-merge job: its pull request merges on its "+
			"own once its checks pass, with no human review. Nothing you propose for this "+
			"repository inherits that, though, because this task lands on %s and such a "+
			"proposal is filed against the repository's default branch -- auto-merge is a "+
			"permission for one branch, "+
			"not for the run holding it. Say in a proposal's body if it was meant to merge "+
			"unattended, and whoever approves it decides.", baseDescription(task))
	case task.AutoMerge:
		s += " This task is an auto-merge job: its pull request merges on its own " +
			"once its checks pass, with no human review. A proposal that is a piece " +
			"of this same task inherits that -- pass auto_merge: false on one that " +
			"is separate work and deserves a human's own review. Such a proposal is " +
			"also filed approved rather than waiting for somebody to accept it, so " +
			"it is dispatched in its turn whether or not anybody has looked at it: " +
			"depends_on is the only thing holding it behind work it has to follow, " +
			"and a proposal you file with none can start beside the task you are " +
			"running now. That reaches only so far -- a few levels of such work " +
			"below the last person who approved anything, and a few proposals from " +
			"any one task -- and past either bound a proposal is filed for a human " +
			"with a note saying why, so write each one's body for somebody who may " +
			"have to read it. Pass hold_for_approval on a proposal a human should " +
			"look at before it runs, and it is filed unapproved whatever this " +
			"permission covers."
	}
	s += " Any link to a grain UI route (`/tasks/<id>`, `/settings/...`, `/system/...`, etc.) " +
		"in your comments, questions, chat messages, or task descriptions must always be a " +
		"relative path (such as `[Task #31](/tasks/31)` or `[Settings → CI](/settings/ci)`) " +
		"and never an absolute `http://127.0.0.1:...` or `http://localhost:...` URL, so links " +
		"work when viewed through UberProxy or any reverse proxy."
	return s
}

// History is what has already happened to a task, as the run about to be
// dispatched for it is told: the attempts before this one, the commits
// those attempts left on the branch this one continues, and the
// conversation. RunDispatch assembles it (from store.Runs,
// checkoutCommits and store.Comments) and BuildPrompt renders it.
//
// The zero value is a first attempt at a task nobody has commented on,
// which is what every one of this package's own prompt tests passes and
// what a task's first dispatch really is.
type History struct {
	// Attempt is this run's own attempt number (dispatch.Dispatch.Attempt),
	// stated so a run can place itself in the list below rather than
	// counting it -- a list bounded to the last few attempts is one a run
	// cannot count its way to the end of.
	Attempt int
	// Attempts is the runs before this one, oldest first and this run's
	// own row excluded. model.Run rather than a narrower struct because
	// the two fields that matter here -- Outcome and Detail -- are
	// exactly the pair the store already carries per run, and since
	// outcomeOf a succeeded run fills Detail in too.
	Attempts []model.Run
	// Commits is what those attempts left on the branch: `git log
	// <base>..HEAD` in the checkout prepareCheckout just made, newest
	// first, one "<abbrev> <subject>" per entry (checkoutCommits). Empty
	// when nothing was ever pushed, when there is no checkout to read, or
	// when the read failed -- none of which is worth failing a dispatch
	// over.
	Commits []string
	// Comments is the task's conversation so far, oldest first, exactly
	// as store.Comments returns it -- see commentThreadSection.
	Comments []model.Comment
	// Repair is true when this dispatch is the merge queue's own repair
	// rather than ordinary work on the task: the queue has sent the task
	// back to an agent to fix the pull request branch it is already open
	// from (Observation.MergeQueueRepairAt), or the task was filed to
	// repair somebody else's branch back when a repair was a separate
	// task (model.ReasonFix). Both halves are mergeRepairRun, which is
	// model.mergerTaskSQL's own definition of a merger run read in Go.
	//
	// It belongs here for the reason the rest of History does: it is
	// something that has already happened to this task, which the run
	// cannot see from inside its sandbox and would otherwise have to
	// infer from a comment. What it changes is one paragraph --
	// repairCISection replaces the "inherited breakage is not yours"
	// advice with its opposite -- and nothing else about the prompt.
	Repair bool
	// BaseIncident is the notice about an active base branch incident, if any.
	BaseIncident string
	// PairedBranches is the branches the task's repository ports between
	// (model.RepoConfig.PairedBranches), or nil for a repository that
	// pairs fewer than two. See pairedBranchesSection.
	PairedBranches []string
	// Resumed is the tasks the previous attempt parked this one on
	// through wait_for_task, as they stand now -- set only when that
	// attempt ended model.ParkedOutcome. See parkedResumeSection.
	Resumed []model.WaitTarget
	// ResumedUntil is the time the previous attempt parked this task
	// until through wait_for_task's resume_at -- zero unless that attempt
	// ended model.ParkedOutcome that way. See parkedResumeSection.
	ResumedUntil time.Time
	// SandboxReused is true when a session task's previous warm sandbox was
	// reused across turns rather than torn down and rebuilt.
	SandboxReused bool
	// SandboxReclaimed is true when a session task had a warm sandbox that was
	// reclaimed (TTL expiry or capacity eviction) before this turn started.
	SandboxReclaimed bool
}

func sessionSandboxSection(task model.Task, h History) string {
	if h.SandboxReused {
		return "\n\nThe sandbox is the same one as your previous turn: your filesystem, installed " +
			"packages, clones and uncommitted changes are still in place, so do not redo setup, " +
			"re-clone or reinstall packages that are already there."
	}
	if h.SandboxReclaimed || ((task.Interactive || task.KeepRunning) && len(h.Attempts) > 0) {
		return "\n\nThe sandbox is not the same one: your previous session sandbox was reclaimed, " +
			"so this run has a new sandbox with a fresh clone and only what you committed and " +
			"pushed still exists."
	}
	return ""
}

// parkedResumeSection tells a run that its task is back from a park: which
// tasks the previous attempt waited on, and how each of them ended. A
// park ends on closure whatever the closure was, so the run has to be
// told the outcome rather than assume the one it was waiting for.
func parkedResumeSection(h History) string {
	if !h.ResumedUntil.IsZero() {
		return "This run resumes from a park: the previous attempt called wait_for_task with " +
			"resume_at and parked this task until " + h.ResumedUntil.UTC().Format(time.RFC3339) +
			". That time has passed (or a person undeferred the task early), so grain queued it " +
			"again. The sandbox did not survive the park -- what that attempt pushed is on your " +
			"branch, and nothing else of it is. Pick up what it was waiting to do: if it was " +
			"waiting out a hold on CI, run CI again now with `grain ci retrigger` and `grain ci wait`.\n"
	}
	if len(h.Resumed) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("This run resumes from a park: the previous attempt called wait_for_task, " +
		"parked this task on the task(s) below, and ended. They have all closed (or been removed " +
		"from this task's dependencies), so grain queued it again. The sandbox did not survive " +
		"the park -- what that attempt pushed is on your branch, and nothing else of it is. " +
		"Check how each one ended before carrying on: a task closed without merging is not " +
		"the change you were waiting for.\n")
	for _, t := range h.Resumed {
		b.WriteString(t.Describe())
	}
	return b.String()
}

// pairedBranchesSection tells a run in a repository that pairs branches
// never to merge one of them into the other. A change reaches the other
// branch as a port (port.go), filed when it merges and reviewed as a
// port. A run that needs a fix only the other branch has can reach it
// without one: grain/task-1869 merged origin/candidate into a branch cut
// from main, and its pull request carried 38 candidate commits onto main.
// That included candidate's ports of main's own commits, so main then
// held each of those changes twice. It also hid the run's own change from
// the port decision, and anything candidate-only marked `Port: no` would
// have landed on main unreviewed. The port task's own body already says
// this (portBody); this section says it to every other run as well.
//
// "" for fewer than two branches, which is every repository that pairs
// none.
func pairedBranchesSection(branches []string) string {
	if len(branches) < 2 {
		return ""
	}
	quoted := make([]string, len(branches))
	for i, b := range branches {
		quoted[i] = "`" + b + "`"
	}
	names := strings.Join(quoted, " and ")
	if len(quoted) > 2 {
		names = strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
	}
	return "\n\n" + names + " are paired: do not merge one into the other, or into a branch " +
		"cut from the other. grain carries each change merged onto one of them across as a " +
		"port, and a merge would bypass that mechanism. It would bring the other branch's " +
		"ports along as duplicates, and changes that were never meant to cross would go with " +
		"them. If you need a change that is only on the other branch, ask_question, or " +
		"propose_task a port of it, instead."
}

// previousAttempts is the runs before this one at taskID, oldest first --
// store.Runs with this run's own row, and anything numbered at or beyond
// it, left out.
//
// The filter is not optional: dispatch.Cycle writes the task_run row
// (store.StartRun) before RunDispatch ever sees the Dispatch, so store.Runs
// always returns this very run alongside its predecessors, and a prompt
// that listed it would be telling a run about itself -- with an outcome
// column that is empty precisely because it has not happened yet.
func previousAttempts(ctx context.Context, store *model.Store, taskID string, attempt int) ([]model.Run, error) {
	runs, err := store.Runs(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: reading %s's earlier attempts: %w", taskID, err)
	}
	var out []model.Run
	for _, r := range runs {
		if r.Attempt < attempt {
			out = append(out, r)
		}
	}
	return out, nil
}

// suspensionNoteSection is the line a prompt opens with when the run
// before this one was suspended (model.SuspensionNote,
// docs/design/durable-tasks.md#DT-2), followed by the blank line that
// separates it from the rest, and "" otherwise. Only the most recent
// attempt is read: a suspension further back has been followed by a run
// that was not one, and that run's ending is what the attempts section
// reports.
func suspensionNoteSection(h History) string {
	if len(h.Attempts) == 0 {
		return ""
	}
	last := h.Attempts[len(h.Attempts)-1]
	if last.SuspendKind == "" || last.FinishedAt == nil {
		return ""
	}
	detail, _, _ := strings.Cut(strings.TrimSpace(last.Detail), "\n")
	if len(detail) > maxSuspensionNoteDetail {
		detail = strings.ToValidUTF8(detail[:maxSuspensionNoteDetail], "") + "…"
	}
	return model.SuspensionNote(last.SuspendKind, detail, *last.FinishedAt) + "\n\n"
}

// maxSuspensionNoteDetail bounds the detail suspensionNoteSection quotes,
// which is one line and not the place for an agent's whole error text.
const maxSuspensionNoteDetail = 200

// maxPreviousAttempts is how many earlier attempts previousAttemptsSection
// describes, newest kept: a task that has failed nine times has nothing
// more to teach its tenth attempt than its last few endings do, and the
// older ones are the ones whose commits are already folded into the
// branch it is about to read anyway.
const maxPreviousAttempts = 3

// maxAttemptDetail bounds one attempt's task_run.detail in that section.
// Detail is short by construction (model.Run.Detail, outcomeOf's own tool
// census, agent.TrimLimitMessage) but not bounded anywhere: a framework's
// own error text is whatever the CLI printed, and a prompt is not the
// place to find out how long that can get.
const maxAttemptDetail = 240

// maxBranchCommits is how many of the branch's commits are listed. Enough
// to see the shape of what an earlier attempt built, short of being the
// git log a run can read for itself in one tool call -- this section's job
// is to tell it there is something there to read.
const maxBranchCommits = 10

// previousAttemptsSection tells a redispatched run what the attempts
// before it did and how they ended, or "" for a first attempt -- which
// is every task's first dispatch, and every caller that has no history to
// pass.
//
// Without it, attempt 2 opens on a branch carrying commits it did not
// make, with no account of what they were for or why the attempt that
// made them stopped: grain hands it the task, the conversation, the
// attachments and a checkout continuing the previous attempt's branch
// (prepareCheckout, deliberately), and says nothing at all about the
// attempt itself. The store has had all of it the whole time -- every
// task_run row carries outcome and detail, and since outcomeOf that
// detail includes the tool census for a run that succeeded as well as
// one that failed. The cheapest thing that costs is re-doing the
// diagnosis attempt 1 already paid for; the dearest is re-attempting
// precisely the thing that hit the wall clock -- an ending a branch
// cannot reveal and the detail says outright ("the run exceeded its 2h0m0s
// wall-clock limit" -- model.RuntimeCapDetail, recorded on RunDispatch's
// errRunTimedOut arm, and read back by model.EndingOf).
//
// Bounded on purpose, by maxPreviousAttempts, maxAttemptDetail and
// maxBranchCommits: this is orientation, not a transcript store. The
// transcript stays exactly where it is (Store.SetRunTranscript, and
// pkg/ui's own pane over it) -- it is prose, per-framework and unbounded,
// and none of that belongs in a prompt.
func previousAttemptsSection(h History, branch string) string {
	if len(h.Attempts) == 0 {
		return ""
	}
	shown := h.Attempts
	if len(shown) > maxPreviousAttempts {
		shown = shown[len(shown)-maxPreviousAttempts:]
	}

	var b strings.Builder
	b.WriteString("This task has been attempted before")
	if h.Attempt > 0 {
		fmt.Fprintf(&b, " -- you are attempt %d", h.Attempt)
	}
	b.WriteString(". Read this before you start: it is the diagnosis those attempts " +
		"already paid for, and re-doing it, or re-attempting whatever the last one " +
		"ended on, is the commonest way a redispatch spends its budget twice.\n")
	if len(shown) < len(h.Attempts) {
		fmt.Fprintf(&b, "The %d most recent, oldest first (%d earlier one(s) not listed):\n",
			len(shown), len(h.Attempts)-len(shown))
	} else {
		b.WriteString("Each of them, oldest first:\n")
	}
	for _, r := range shown {
		fmt.Fprintf(&b, "- attempt %d ended %s%s\n", r.Attempt,
			attemptOutcome(r), attemptDetail(r))
	}
	if len(h.Commits) > 0 {
		commits := h.Commits
		if len(commits) > maxBranchCommits {
			commits = commits[:maxBranchCommits]
		}
		fmt.Fprintf(&b, "Commits those attempts already pushed to %q, newest first -- "+
			"they are on the branch you have checked out, so read them and the diff "+
			"rather than writing them again:\n", branch)
		for _, c := range commits {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		// No count on the overflow line, because there is no honest one
		// to give: RunDispatch asks checkoutCommits for one commit more
		// than this list holds, precisely so that "there is more" can be
		// said without a second, unbounded read whose only use would be
		// to make this sentence a number.
		if len(h.Commits) > len(commits) {
			b.WriteString("- ... and older ones still; `git log` in the checkout has all of them.\n")
		}
	}
	return b.String()
}

// attemptOutcome renders one earlier run's task_run.outcome for that
// list. "" is a run whose row was never finished -- a daemon that died
// mid-run, before recover.go's own sweep reached it -- and reads as the
// unknown it is rather than as a blank where an outcome should be.
func attemptOutcome(r model.Run) string {
	if r.Outcome == "" {
		return "with no outcome recorded (grain lost the run)"
	}
	return strconv.Quote(r.Outcome)
}

// attemptDetail renders one earlier run's task_run.detail, trimmed to one
// line and to maxAttemptDetail -- the same shape agent.TrimLimitMessage
// gives the details it writes, applied here to every other writer of that
// column too, since a prompt has no more room for a pasted stack trace
// than a run listing does.
func attemptDetail(r model.Run) string {
	s := strings.Join(strings.Fields(r.Detail), " ")
	if s == "" {
		return ""
	}
	if len(s) > maxAttemptDetail {
		s = s[:maxAttemptDetail] + "..."
	}
	return ": " + s
}

// attributionLabel renders who said a comment, from the redispatched
// run's own point of view -- "you" for grain relaying this same task's
// own earlier ask_question or comment_on_issue call (Attribution.OnBehalfOf
// naming a PrincipalAgent, exactly relayComment's own attribution in
// finish.go), so a run recognizes its own prior words rather than reading
// them as some third party's, and "a human" for a direct reply, which is
// the answer a parked ask_question was waiting on. taskID is the task
// the comment is on: one relayed on behalf of a different task's run --
// a team broadcast (ui.Client.BroadcastTeamMessage) -- is that task's,
// not "you".
func attributionLabel(a model.Attribution, taskID string) string {
	if a.OnBehalfOf != nil {
		switch a.OnBehalfOf.Kind {
		case model.PrincipalAgent:
			if other := otherTaskRun(a, taskID); other != "" {
				return other
			}
			return "you, in an earlier attempt at this task"
		case model.PrincipalHuman:
			if a.OnBehalfOf.ID != "" {
				return fmt.Sprintf("a human (%s)", a.OnBehalfOf.ID)
			}
			return "a human"
		}
	}
	switch a.Actor.Kind {
	case model.PrincipalHuman:
		if a.Actor.ID != "" {
			return fmt.Sprintf("a human (%s)", a.Actor.ID)
		}
		return "a human"
	case model.PrincipalAgent:
		if other := otherTaskRun(a, taskID); other != "" {
			return other
		}
		return "an earlier attempt at this task"
	default:
		return "grain"
	}
}

// otherTaskRun labels a comment written by, or relayed on behalf of, a
// run of some task other than taskID -- a team broadcast, or a message
// one run sent another's agent (POST /api/tasks/{id}/agent-messages,
// which signs it with the sender's own agent principal) -- or is empty
// for any other comment.
func otherTaskRun(a model.Attribution, taskID string) string {
	run := a.OnBehalfOf
	if run == nil {
		run = &a.Actor
	}
	if run.Kind != model.PrincipalAgent || run.ID == "" || taskID == "" || run.ID == taskID {
		return ""
	}
	return fmt.Sprintf("the run of another task (task %s), not a human", run.ID)
}

// addendumText renders one just-arrived comment for a Framework to fold
// into its conversation mid-run -- present tense, unlike
// attributionLabel's own labels ("an earlier attempt at this task"),
// which are phrased for a redispatched run looking back at its whole
// history rather than for something that landed seconds ago in the run
// currently reading it.
func addendumText(c model.Comment) string {
	who := "a human"
	if other := otherTaskRun(c.Author, c.TaskID); other != "" {
		who = other
	} else if c.Author.OnBehalfOf != nil && c.Author.OnBehalfOf.Kind == model.PrincipalHuman {
		if c.Author.OnBehalfOf.ID != "" {
			who = fmt.Sprintf("a human (%s)", c.Author.OnBehalfOf.ID)
		}
	} else if c.Author.Actor.Kind == model.PrincipalHuman {
		if c.Author.Actor.ID != "" {
			who = fmt.Sprintf("a human (%s)", c.Author.Actor.ID)
		}
	} else if c.Author.OnBehalfOf == nil && c.Author.Actor.Kind != model.PrincipalHuman {
		who = "grain"
	}
	return fmt.Sprintf(
		"%s just added this to the task while you're already working on it -- read it and factor it in:\n\n%s",
		who, c.Body,
	)
}

// lastCommentID is the highest comment id in seen, or zero for a task
// whose conversation is empty.
//
// It is the cursor both halves of "a comment posted mid-run reaches the
// run" start from: addendaPoller's own seed below, and
// agent.RunConfig.AddendaAfter, which carries the same number into the
// run's forked mcpserver. The max rather than the last element because
// nothing here promises Comments answers in id order, and a cursor that
// trusted the ordering would replay the whole thread to a run the first
// time one arrived out of it.
func lastCommentID(seen []model.Comment) int64 {
	var last int64
	for _, c := range seen {
		if c.ID > last {
			last = c.ID
		}
	}
	return last
}

// addendaPoller returns an agent.RunConfig.Addenda function bound to
// store and taskID: a comment posted while this run is still live,
// rather than only seen folded into the task's next dispatch
// (commentThreadSection).
//
// No Framework calls it -- each needs a turn boundary to poll at and
// none has one, which that field's own doc comment says in full. What
// actually carries a mid-run comment to a run is AddendaAfter beside it,
// through the run's forked mcpserver (pkg/mcp's addenda.go); this is
// kept because it belongs to the interface rather than to this moment's
// frameworks, on the same footing RunConfig.Tools is.
//
// seen is the same slice RunDispatch already read to build this run's
// own prompt -- its highest comment id seeds the cursor here, so the
// first poll only ever returns what arrived after dispatch, never
// something already sitting in the prompt this run started with.
func addendaPoller(store *model.Store, taskID string, seen []model.Comment) func(context.Context) ([]string, error) {
	lastID := lastCommentID(seen)
	return func(ctx context.Context) ([]string, error) {
		comments, err := store.Comments(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("orchestrator: polling %s's conversation for addenda: %w", taskID, err)
		}
		var addenda []string
		for _, c := range comments {
			if c.ID <= lastID {
				continue
			}
			lastID = c.ID
			addenda = append(addenda, addendumText(c))
		}
		return addenda, nil
	}
}

// RunDispatch drives one dispatch.Dispatch to completion: resolve and
// materialize its task's capabilities (writing any SideSandbox placements
// through placer, or into sandboxRoot when the sandbox offers no placer --
// both may be empty when the task has nothing to place),
// run the agent against tools (whatever this run's own Sandbox.Tools
// produced -- see the package doc comment on the local-directory-vs-
// real-VM choice that makes), revoke whatever was materialized, and
// record the run's outcome. Every path here finishes the run, even a
// failing one -- ported from pkg/orchestrate's own runDispatch
// (bwsalmon/agents#254) when that package merged into this one: an
// unfinished run would hold its share of the concurrency limit forever. It does not touch
// task_observation or GitHub at all -- see ProcessResult for that half,
// kept separate the same way e2e's own runDispatch and its caller are,
// since deciding what a run produced is a different question from
// deciding what to do about it.
//
// sandboxRoot and granuleVM both also travel straight into the
// agent.RunConfig framework.Run is given, for a Framework with no
// in-process route to tools to reach the same sandbox by itself -- see
// RunConfig's own doc comment. Either may be empty; runOne computes
// whichever this run's own Sandbox can actually offer. placer is the
// third of those, and unlike the other two it is used here rather than
// forwarded: it is how a sandbox with no local directory receives a
// placement at all (see SandboxPlacer), and nil for one that has no
// route of its own.
//
// While the agent runs, a background watchForTaskClosed goroutine polls
// store for this task being closed and cancels the ctx the agent itself
// (and every tool call it makes) was given the moment it sees that --
// bwsalmon/agents#346's "actually terminate a running task's sandbox
// process on cancel": a task closed through `grain ui`'s Cancel button
// reaches this run, in whatever separate grain daemon process is
// actually running it, only because both share the one store. A run
// killed this way finishes with outcome "cancelled", distinct from
// "failed", and returns a non-nil error wrapping errTaskClosed.
//
// A run whose framework reports the agent's own usage limit
// (agent.UsageLimitError) ends a third way: outcome model.PausedOutcome,
// cfg.Pause closed behind it so that nothing more is dispatched onto the
// credential that met it -- the model connection owning its key
// (PauseScope) -- until the provider's window resets, and every other
// run in flight on it cancelled with errUsageLimit -- which lands those
// runs on this same path, with the same outcome and a detail saying
// whose limit it was. A run spending another credential -- another
// team's, or another connection's of the same team -- is not touched.
// See Pause.
//
// A run whose framework reports the provider having no capacity for it
// (agent.CapacityError) ends a fourth way: outcome model.CapacityOutcome,
// which no failure streak counts, and nothing paused at all. An overload
// is usually over in seconds, so the response is to keep this attempt off
// the task's record and let dispatch's own backoff space the next one --
// not to stop the deployment. See model.CapacityOutcome.
// baseIncidentNote is the line a run's prompt gets while its base branch
// is red: somebody else is repairing it, so leave the shared fault alone.
// Not for a repair of that branch -- grain's own, whichever commit it was
// filed for, or a task a person marked as fixing it (model.FixesBaseBranch)
// -- whose whole job is that fault: a marked task running alongside
// grain's repair was being told to keep its hands off the very thing it
// was marked to fix.
func baseIncidentNote(ctx context.Context, store *model.Store, task model.Task) string {
	if task.Target == nil {
		return ""
	}
	baseBranch := task.Base
	if baseBranch == "" {
		baseBranch = "main"
	}
	row, err := store.GetBaseBranchCI(ctx, *task.Target, baseBranch)
	if err != nil || row == nil || !row.Broken() || isBaseRepair(task, *task.Target, baseBranch, row) {
		return ""
	}
	return fmt.Sprintf("NOTE: Base branch `%s` currently has an active incident / failing CI (task %s is repairing it). Do not attempt to fix shared base faults locally; focus only on your change.", row.Branch, row.RepairTaskID)
}

func RunDispatch(ctx context.Context, store *model.Store, framework agent.Framework,
	cfg Config, task model.Task, d dispatch.Dispatch, tools []mcp.Tool, sandboxRoot, granuleVM string,
	placer SandboxPlacer, at time.Time, containers ...string) (*agent.Result, error) {

	var container string
	if len(containers) > 0 {
		container = containers[0]
	}
	if placer == nil && framework != nil {
		if hp, ok := framework.(interface{ Host() agent.Host }); ok {
			if p, ok := hp.Host().(SandboxPlacer); ok {
				placer = p
			}
		}
	}
	if hp, ok := framework.(agent.HostProvider); ok && hp != nil {
		if gph, ok := hp.Host().(interface{ GitProxyURL() string }); ok {
			if u := gph.GitProxyURL(); u != "" {
				cfg.GitRemoteBase = u
			}
		}
	}
	task = withDeploymentTimeZone(task, cfg.TimeZone)
	run := model.Run{ID: d.RunID, TaskID: d.TaskID, Sandbox: d.RunID, Attempt: d.Attempt, StartedAt: at}
	cc := model.CapabilityContext{Task: task, Run: run, Now: at, Workdir: sandboxRoot, Credentials: cfg.Credentials,
		Deadline: capabilityDeadline(at, cfg.maxRunRuntime())}
	// The connection the run's own agent spends, as its usage limits are
	// keyed (runPauseScope), so a model proxy token minted below spends
	// the same one.
	if connection, ok := framework.(CredentialConnection); ok {
		cc.Connection, _ = connection.CredentialConnection()
	}

	// Which model and framework this run is on, recorded before anything
	// else can end it, so every attempt -- a setup failure's too -- says
	// which of the task's rotation it was given (Store.SetRunModel). Logged
	// and no more on failure, as SetRunAgentStarted is below.
	if err := store.SetRunModel(ctx, d.RunID, runModelFor(framework, task),
		runFrameworkFor(framework, task, cfg)); err != nil {
		log.Printf("orchestrator: run %s: recording its model: %v", d.RunID, err)
	}

	// materialized is every capability this run holds once
	// materializeCapabilities below has run: the leases admission reserved
	// (cfg.PreparedCapabilities) and whatever it minted on top of them.
	var materialized []model.Materialized

	// A panic anywhere below is recoverRunOne's to turn into an error and
	// to finish the row for (finishPanickedRun), but neither of those knows
	// what this run holds -- runOne handed its reservation over before
	// calling here, and everything materialized since lives only in this
	// frame. Unrevoked, a scarce lease stayed spent until its own expiry,
	// which for a pool of a few keys is every other task that needs one
	// waiting on a run that is already over. So the leases go back here,
	// on the way out, and the panic carries on to the recover that owns it.
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		revokeAll(cleanupCtx, store, cc, withUnconsumed(materialized, cfg.PreparedCapabilities))
		panic(rec)
	}()

	// unprepared is every return below that comes before this run has
	// anything of its own to finish -- the store reads the prompt is
	// assembled from. runOne handed this run's row over before calling
	// here (its setup guard stands down once RunDispatch is called), along
	// with whatever scarce capability admission reserved for it, so a
	// plain return from one of those reads left the row live -- reading
	// 'running', holding a worker, until a daemon restart -- and the
	// reservation's lease held with it. It finishes the row "setup-failed",
	// the word runOne's own guard uses for a run no agent ever reached,
	// and gives the reservation back, on a context detached from ctx's
	// cancellation for that guard's reason.
	unprepared := func(err error) (*agent.Result, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
		defer cancel()
		revokeAll(cleanupCtx, store, cc, cfg.PreparedCapabilities)
		if ctx.Err() != nil {
			// The daemon stopping, not the store failing: handed back.
			if ferr := finishStoppedInSetup(ctx, store, d.TaskID, d.RunID, cfg.now()); ferr != nil {
				err = errors.Join(err, fmt.Errorf("orchestrator: finishing run %s stopped in setup: %w", d.RunID, ferr))
			}
			return nil, fmt.Errorf("orchestrator: run %s: %w", d.RunID, errors.Join(errDaemonStopped, err))
		}
		if ferr := finishSetupFailed(cleanupCtx, store, d.TaskID, d.RunID, cfg.now(),
			"this run could not be set up: "+err.Error()); ferr != nil {
			err = errors.Join(err, fmt.Errorf("orchestrator: finishing run %s after its setup failed: %w", d.RunID, ferr))
		}
		return nil, err
	}

	// A change target (a registered kind) is checked again here, as it was at
	// filing: the task may have been edited since, and an edition's
	// validation may have tightened. A task missing the grant its target
	// implies -- the CitC workspace it works in -- cannot do its work, and
	// attaching one now would skip the approval that grant is subject to.
	if err := dispatchChangeTargetRefusal(task); err != nil {
		return unprepared(err)
	}

	comments, err := store.Comments(ctx, task.ID)
	if err != nil {
		return unprepared(fmt.Errorf("orchestrator: reading %s's conversation: %w", task.ID, err))
	}
	// Read the observation to know which comments had already been delivered
	// to a prior completed turn, as well as whether this dispatch is a merge
	// queue repair (repairCISection, mcp.PullRequestScope.Repair).
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		return unprepared(fmt.Errorf("orchestrator: reading %s's observation: %w", task.ID, err))
	}
	var priorDelivered *int64
	if obs != nil && obs.DeliveredCommentID != nil {
		id := *obs.DeliveredCommentID
		priorDelivered = &id
	}
	attachments, err := store.Attachments(ctx, task.ID)
	if err != nil {
		return unprepared(fmt.Errorf("orchestrator: reading %s's attachments: %w", task.ID, err))
	}
	// What the attempts before this one did, for the prompt section that
	// tells this one (previousAttemptsSection). Read from the same store
	// as the two reads above, and fatal for the same reason they are: a
	// store that cannot answer this is one that cannot record how this
	// run ends either.
	previous, err := previousAttempts(ctx, store, task.ID, d.Attempt)
	if err != nil {
		return unprepared(err)
	}
	repair := mergeRepairRun(task, obs)
	promptExtension, err := resolvePromptExtension(ctx, store, cfg.PromptExtension, task)
	if err != nil {
		return unprepared(err)
	}
	memory, err := resolveMemory(ctx, store, task, cfg.now())
	if err != nil {
		return unprepared(err)
	}

	// Before the agent's first turn: a run whose sandbox never got its repo
	// has nothing to do. Capability placements are applied just below before
	// this clone, because an explicit remote may need one to authenticate.
	//
	// Skipped for a task already closed by the time its dispatch got
	// here -- the same race the synchronous checkTaskClosed below exists
	// for (bwsalmon/agents#346), whose rule is that such a run never
	// reaches its sandbox at all. A clone is the run's own first touch of
	// that sandbox, so it has to observe the rule too; the closed case
	// then falls through to the cancellation path below and finishes
	// "cancelled" exactly as it did before. A read error here is treated
	// as "not closed": the cancellation path re-reads it anyway, so the
	// cost of being wrong is one clone, not a run that ignores a close.
	setupCommand, err := resolveSetupCommand(ctx, store, task)
	if err != nil {
		return unprepared(err)
	}
	cloneURL, err := resolveCloneURL(ctx, store, task)
	if err != nil {
		return unprepared(err)
	}
	// Whether GitHub's check runs are a source of verdicts in this run's
	// repository at all, read here for the same reason the two above are:
	// it is a fact about the repository, settled when the run is
	// dispatched, and the run's own CI tools have to agree with the merge
	// queue about it (mcp.PullRequestScope.GitHubCIIgnored).
	gateIsGrainCI, err := resolveGateIsGrainCI(ctx, store, task)
	if err != nil {
		return unprepared(err)
	}
	// The rest of this run's setup, narrated on the task's own row for
	// whoever is watching it -- runOne has already said what it was doing
	// while the sandbox was being built, and this picks the story up from
	// the clone. See setupNotes.
	notes := setupNotes{store: store, taskID: task.ID, now: cfg.now}

	// A custom remote may authenticate through a repo-scoped capability, so
	// its sandbox placements have to exist before the first clone. Keep the
	// resulting materialization for prompt sections, recreation and the one
	// eventual revoke; never materialize the same grant a second time.
	var prepErr error
	if len(task.Grants) > 0 {
		notes.say(ctx, capabilityCredentialsNote)
	}
	materialized, prepErr = materializeCapabilities(ctx, cfg.Capabilities, cc,
		cfg.PreparedCapabilities, sandboxRoot, placer)
	cfg.SandboxRecreations.setMaterialized(d.TaskID, materialized)
	recordMaterializedLeases(ctx, store, d.RunID, materialized)
	// Before the clone, so the CLI every prompt names is there whatever
	// the clone does. After the grants' own placements, so nothing a
	// grant installs in the same directory is placed over it.
	if !cfg.sandboxReused {
		if err := placeSandboxGrain(ctx, cfg.SandboxGrainBinary, sandboxRoot, placer); err != nil {
			log.Printf("orchestrator: task %s: the sandbox keeps its image's grain: %v", task.ID, err)
		}
	}

	var prepared checkout
	var checkoutErr error
	if closed, err := taskClosed(ctx, store, task.ID); prepErr == nil && (err != nil || !closed) {
		// Named, because on a deployment with several repos "cloning" on
		// its own says nothing the row did not already say. Only where
		// there is actually a clone to make: prepareCheckout returns a
		// zero checkout and does nothing at all for a task with no target
		// or a deployment running no git proxy.
		if (cfg.GitRemoteBase != "" || cloneURL != "") && task.Target != nil && !cfg.sandboxReused {
			notes.say(ctx, cloningNote(*task.Target))
		}
		if task.Target == nil && hasGrant(task, model.SelfDebugCapability) {
			var srcOut checkout
			var srcWG sync.WaitGroup
			srcWG.Add(1)
			go func() {
				defer srcWG.Done()
				placeGrainSource(ctx, task, cfg.GrainSourceDir, sandboxRoot, placer, tools, &srcOut, cfg.sandboxReused)
			}()
			prepared, checkoutErr = prepareCheckoutWithReuse(ctx, tools, cfg.GitRemoteBase, cloneURL, task, setupCommand, cfg.sandboxReused, notes, cfg.settingsRepo())
			srcWG.Wait()
			if checkoutErr == nil {
				prepared.GrainSource = srcOut.GrainSource
				prepared.GrainSourceWhy = srcOut.GrainSourceWhy
				prepared.GrainSourceRevision = srcOut.GrainSourceRevision
				prepared.GrainSourceCommits = srcOut.GrainSourceCommits
			}
		} else {
			prepared, checkoutErr = prepareCheckoutWithReuse(ctx, tools, cfg.GitRemoteBase, cloneURL, task, setupCommand, cfg.sandboxReused, notes, cfg.settingsRepo())
			if checkoutErr == nil {
				placeGrainSource(ctx, task, cfg.GrainSourceDir, sandboxRoot, placer, tools, &prepared, cfg.sandboxReused)
			}
		}
	}

	// The commits those earlier attempts pushed, read out of the checkout
	// they are in -- the one place they can be read at all
	// (checkoutCommits). Only for a redispatch, and only where there is a
	// checkout: a first attempt's branch has nothing on it that is not
	// the base's, so this would be a tool call spent proving that.
	history := History{
		Attempt:          d.Attempt,
		Attempts:         previous,
		Comments:         comments,
		Repair:           repair,
		SandboxReused:    cfg.sandboxReused,
		SandboxReclaimed: cfg.sandboxReclaimed,
	}
	history.BaseIncident = baseIncidentNote(ctx, store, task)
	history.Resumed = ResumedFrom(ctx, store, previous)
	history.ResumedUntil = ResumedUntil(previous)
	if task.Target != nil {
		// Advisory, like BaseIncident above: a row grain cannot read
		// costs the run one paragraph rather than the dispatch.
		if rc, err := store.GetRepoConfig(ctx, *task.Target); err == nil && rc != nil {
			if paired := rc.PairedBranches(); len(paired) >= 2 {
				history.PairedBranches = paired
			}
		}
	}
	if len(previous) > 0 && checkoutErr == nil && prepared.Dir != "" {
		history.Commits = checkoutCommits(ctx, tools, task, maxBranchCommits+1)
	}

	var prompt string
	var dynamicSections []string
	if checkoutErr == nil && prepErr == nil {
		prompt, dynamicSections, prepErr = prepareCapabilityPrompt(ctx, cc, materialized, tools, history,
			attachments, prepared, cfg.settingsRepo(), cfg.checkImage(),
			frameworkOpensPullRequests(framework), promptExtension, memory, cfg.maxRunRuntime())
	}
	// Each capability grant's prompt_section, as a block naming the grant,
	// its attempt_id and its MCP server, and saying a capability server
	// wrote it rather than the task's author (docs/capabilities-spec.md
	// §5.4.2).
	if section := cfg.capabilityPromptSections; section != "" && prompt != "" {
		prompt += "\n\n" + section
		dynamicSections = append(dynamicSections, section)
	}

	var result *agent.Result
	var runErr error
	outcome, detail := "failed", ""
	// transcriptPath names a file cfg.TranscriptDir's own doc comment says
	// a Framework may mirror this run's transcript-in-progress into live;
	// "" (cfg.TranscriptDir unset) leaves agent.RunConfig.TranscriptPath
	// empty, which every Framework already treats as "no caller wants
	// this". Computed before the switch below because the cleanup after
	// it (once the store already has this run's own final story) needs it
	// too, whether or not framework.Run ever actually ran.
	var transcriptPath string
	// How long this run spent blocked on CI and what that earned it back,
	// set once the run's own clock is done with (RunWaitCreditCap) and
	// recorded with the rest of its census. nil for a run that never
	// reached its agent, and for one that never waited on a check.
	var waitCredit *model.RunWaitCredit
	// Whether this run ever reached its agent, and the conversation that
	// agent left behind for the task's next run to be resumed into
	// (model.SuspendedSession, off agent.Result.ResumableSession).
	//
	// The flag is what keeps a *setup* failure from touching the record
	// at all. A run whose clone failed has said nothing to any model and
	// holds no opinion about the session an earlier run left parked, and
	// a clearing write from it would throw away hours of an earlier
	// run's thinking over a transient git error.
	agentRan := false
	resumableSession := ""
	// The same conversation, recorded as soon as the framework named it
	// rather than when it returned (earlySession), so a run the daemon
	// is stopped in the middle of still leaves one behind. nil for a run
	// that never reached its agent.
	var early *earlySession
	var recordedEarly string
	// The tail of what this run said when it failed for a reason no
	// marker of grain's could name -- neither a usage limit nor a
	// capacity refusal. Empty for every other ending, including the
	// setup failures below, which are grain's own and not a provider's.
	// See the arm that fills it, and model.UnmatchedFailure.
	var unmatched string
	// The earlier runs of a refusal burst this run completed, recorded
	// as paused once this one's own outcome is written, and what to
	// record on them. Empty for every other ending. See refusalburst.go.
	var burstRuns []string
	var burstDetail string
	// suspend and modelHint are runEnding's own.
	var suspend model.SuspensionKind
	var modelHint string
	if cfg.TranscriptDir != "" {
		transcriptPath = filepath.Join(cfg.TranscriptDir, d.RunID)
	}
	switch {
	case (checkoutErr != nil || prepErr != nil) && ctx.Err() != nil:
		// The daemon was stopped under the clone or the capabilities,
		// before any agent started: handed back without a strike, as
		// finishStoppedInSetup does for the paths that finish the row
		// themselves.
		outcome, detail = model.InterruptedOutcome, InterruptedInSetupDetail
		interruptedRuns.Add(1)
		noteInterrupted(context.WithoutCancel(ctx), store, task.ID, d.RunID, cfg.now())
		runErr = fmt.Errorf("orchestrator: run %s: %w", d.RunID,
			errors.Join(errDaemonStopped, errors.Join(checkoutErr, prepErr)))
	case checkoutErr != nil:
		runErr = fmt.Errorf("orchestrator: preparing %s: %w", d.RunID, checkoutErr)
		detail = checkoutErr.Error()
		outcome, detail = setupEnding(checkoutErr, outcome, detail)
	case prepErr != nil:
		runErr = fmt.Errorf("orchestrator: preparing %s: %w", d.RunID, prepErr)
		detail = prepErr.Error()
		outcome, detail = setupEnding(prepErr, outcome, detail)
	default:
		// runCtx, not ctx, is what watchForTaskClosed cancels the instant
		// it sees this task closed, which is what makes cancelling this
		// run from outside the process running it possible at all -- see
		// that func's own doc comment. cancelRun(nil) once framework.Run
		// returns is what stops the watcher goroutine either way, whether
		// or not it was the one that ended the run.
		//
		// agentCtx, not runCtx, is what framework.Run actually gets: a
		// child of runCtx carrying its own deadline, cfg.maxRunRuntime()
		// out from now, so a run that outlives it gets cancelled the same
		// way a task-closed run does (Config.MaxRunRuntime's own doc
		// comment, bwsalmon/agents#575) without the deadline itself ever
		// touching runCtx or the watcher goroutine reading it. Its cause,
		// once framework.Run returns, tells the two forms of cancellation
		// apart from each other and from any other error framework.Run
		// might return, and from a runCtx cancellation propagated down to
		// it: context.Cause walks up to whichever ancestor was cancelled
		// first.
		//
		// A runClock rather than context.WithTimeoutCause, because that
		// deadline moves: time this run spends blocked on CI is credited
		// back to it while it is still blocked, up to RunWaitCreditCap,
		// and a context deadline cannot be moved once it is set. The
		// clock keeps every property that call had -- see newRunClock --
		// and registering it is what lets the run's own wait_for_checks
		// reach it from the sandbox (RunWaitCredits).
		//
		// runCtx is detached from ctx and cancelled by hand when ctx is,
		// so that a daemon shutdown reaches the run as errDaemonStopped
		// rather than as a cancellation with no cause of its own -- which
		// is what lets it be told apart from the run's other endings
		// (see errDaemonStopped). Its values are ctx's all the same.
		runCtx, cancelRun := context.WithCancelCause(context.WithoutCancel(ctx))
		stopDaemonWatch := context.AfterFunc(ctx, func() { cancelRun(errDaemonStopped) })
		defer stopDaemonWatch()
		agentCtx, clock := newRunClock(runCtx, cfg.maxRunRuntime(), cfg.runWaitCreditCap())
		stopIdle := func() {}
		if cfg.durable {
			stopIdle = watchIdle(runCtx, idleWatch{store: store, taskID: task.ID, runID: d.RunID,
				transcript: transcriptPath, timeout: cfg.durableIdleTimeout(), cancel: cancelRun,
				renew: cfg.renewLeases})
			defer stopIdle()
		}
		stopCrediting := cfg.RunWaitCredits.register(d.TaskID, clock)
		stopCancelling := cfg.TaskCancellers.register(d.TaskID, cancelRun)
		defer stopCancelling()
		checkTaskClosed(ctx, store, task.ID, cancelRun)
		watcherDone := make(chan struct{})
		go func() {
			defer close(watcherDone)
			watchForTaskClosed(runCtx, ctx, store, task.ID, cfg.cancelPollInterval(), cancelRun)
		}()

		// The third thing that can end this run from outside it: another
		// run meeting the usage limit of the same credential, which
		// cancels every run in flight spending it rather
		// than letting each spend an agent's worth of wall-clock time
		// discovering the same refusal (Pause). Same runCtx the watcher
		// cancels, so a run stopped this way lands in exactly the same
		// place a task-closed one does, with its own cause to tell them
		// apart.
		//
		// Registered under the credential this run spends -- the model
		// connection that owns its key, or its team and framework when it
		// is on no connection (runPauseScope) -- since that is what
		// decides which pauses reach it: a run on one team's connection
		// has no business being cancelled by another key's refusal.
		//
		// Unregistered the moment framework.Run returns, below, so a
		// pause begun by *this* run's own limit never cancels the
		// already-finished context it is about to read the cause of.
		if task.AgentFramework == "" {
			task.AgentFramework = runFrameworkFor(framework, task, cfg)
		}
		runScope := runPauseScope(framework, task, cfg)
		stopPause := cfg.Pause.register(d.RunID, runScope, cfg.now(), cancelRun)

		// Repo/Branch are the same pair BuildPrompt names in the prompt,
		// passed structurally as well so a Framework can scope its
		// forked mcpserver's pull_request_status to exactly this run's
		// branch. Empty for a task with no target, which
		// agent.RunConfig.Repo's own doc comment covers.
		var repo string
		if task.Target != nil {
			repo = task.Target.String()
		}

		// And the credential that branch is pushed with, by name, so the
		// forked mcpserver's own GitHub reads -- pull_request_status,
		// base_branch_status, the issue tools -- are made as the same
		// account rather than off a deployment ladder that, on a
		// deployment whose credentials are all team connections, names
		// nothing (agent.RunConfig.GitCredential). Resolved here for the
		// reason proposeRepos below is: this is the last point that
		// holds a store handle. A task with no target has no credential
		// to name, and a ladder that ends empty is not a failure here --
		// heldForGitCredential keeps such a task from dispatching at
		// all, and a run already prepared is better served reading as
		// it always did than not running.
		gitCredential := runGitCredential(ctx, store, task)

		// Which tool-granting capabilities this run's task holds, and
		// where grain's own source is if self-debug is among them -- what
		// turns those grants' read-only tools on in the forked mcpserver a
		// subprocess Framework runs (agent.RunConfig.Grants,
		// agent.GrantArgs).
		//
		// Read off the task's raw Grants, exactly as runOne reads them
		// for Config.GrantTools, and for the same reason: both of these
		// are model.ProvisionGrant capabilities whose Resolve always
		// honours, so there is no "granted but refused" case a resolved
		// GrantResolution would catch and this would miss.
		//
		// Not gated on Interactive, which GrantTools is: that gate is
		// about selfrepair's tools needing a human watching the chat to
		// answer a confirmation, and nothing here asks anyone anything.
		// A task granted self-debug is a task somebody wants to be able
		// to debug grain, attended or not, and a playbook is a file to
		// read whether or not anyone is watching it being read.
		grants := runGrants(task)
		grainSourceDir := ""
		if slices.Contains(grants, selfdebug.CapabilityName) {
			grainSourceDir = cfg.GrainSourceDir
		}
		// Told to the run's forked mcpserver only alongside the grant it
		// belongs to, exactly as the source checkout above is -- and here
		// it matters more, because what this directory holds is this
		// deployment's credentials. Nothing is served out of it: the
		// process told about it is on the controller, where those files
		// already are, and what it does with them is recognize their
		// values in a journal line and cut them out before the line is
		// answered with (pkg/capability/grainlog, agent.RunConfig's
		// GrainSecretsDir).
		grainSecretsDir := ""
		if slices.Contains(grants, grainlog.CapabilityName) || slices.Contains(grants, deploymenthealth.CapabilityName) {
			grainSecretsDir = cfg.GrainSecretsDir
		}
		grainStateDir := ""
		if slices.Contains(grants, deploymenthealth.CapabilityName) {
			grainStateDir = cfg.GrainStateDir
		}
		// The local-scripts declarations this task's capabilities grant,
		// and the credentials their scripts are delivered. The grants
		// were resolved before the sandbox was built, which is where an
		// untrusted run was refused; this says it again rather than
		// trusting that it was (localScriptsFor).
		localScripts, localScriptsEnvFile, err := localScriptsFor(ctx, cfg, task, d.RunID)
		if err != nil {
			return unprepared(fmt.Errorf("serving local scripts: %w", err))
		}
		if localScriptsEnvFile != "" {
			defer os.Remove(localScriptsEnvFile)
		}

		// The repositories a propose_task call from this run may name,
		// which the forked mcpserver folds into that tool's own `repo`
		// argument (agent.RunConfig.ProposeRepos). Read here because
		// this is the last point that holds a store handle: the process
		// serving the tools has none, so a description that could
		// otherwise only state the rule gets to state the answer.
		proposeRepos := proposalRepos(ctx, store, d.TaskID, model.DefaultTeamID)

		// Setup is over and the agent's own time starts here -- the one
		// moment inside a run nothing else records, and the line
		// pkg/metrics splits SandboxSetup from AgentWork at. Everything
		// above (a sandbox built, a repo cloned, capabilities minted and
		// placed) is on the near side of it; everything framework.Run
		// does is on the far side.
		//
		// A failure to record it is logged and no more. The measurement
		// is worth taking on every run, and worth nothing at all if
		// taking it can cost one: a task must not fail because a
		// bookkeeping write did (Store.SetRunAgentStarted).
		agentStartedAt := cfg.now()
		if cfg.Now == nil && agentStartedAt.Before(at) {
			agentStartedAt = at
		}
		if err := store.SetRunAgentStarted(ctx, d.RunID, agentStartedAt); err != nil {
			log.Printf("orchestrator: run %s: recording when its agent started: %v", d.RunID, err)
		}

		// The other half of that line: the row stops being grain's to
		// narrate here, so grain's last setup phrase comes off it rather
		// than standing over the agent's first quiet half-hour as if it
		// were still true. From here only update_status writes it. See
		// setupNotes.handOver.
		notes.handOver(ctx)

		// The prompt itself, recorded here rather than reconstructed
		// later: it is assembled once, out of a task that may since be
		// edited and a conversation that has since grown, so this is the
		// only moment "what was this run actually told?" has an answer
		// (Store.SetRunPrompt, and pkg/ui's own /prompt route over it).
		// Written before framework.Run rather than after, so a run still
		// in flight can be asked the question too. Logged and no more on
		// failure, for the same reason the write above is: a task must
		// not fail because a bookkeeping write did.
		if err := store.SetRunPrompt(ctx, d.RunID, prompt); err != nil {
			log.Printf("orchestrator: run %s: recording the prompt it was given: %v", d.RunID, err)
		}

		// And how big it came out. The conversation a prompt carries
		// grows monotonically with the task -- every message replayed
		// into every later attempt -- and the ceiling it grows towards is
		// the model's context window, which fails quietly: the run does
		// not stop at dispatch, it works worse than the same run did on
		// the same task a week ago, with its actual instructions crowded
		// into something enormous, and nothing in the record names the
		// reason (grain/task-268, conversation.go).
		//
		// So the number is said out loud once it is worth saying. Not a
		// refusal and not a hold: a dispatch is never stopped by this,
		// because a prompt that is large is not yet a prompt that is
		// broken, and an operator told before it breaks can split the
		// task, answer it, or close it. The section that actually grows
		// is bounded a layer up (maxConversationBytes), and this is what
		// catches everything else -- a description somebody pasted a log
		// into, an attempt history, a deployment whose standing prompt
		// extension has been edited for a year.
		//
		// Logged rather than commented on the task: a comment about the
		// conversation being too long would be one more message in the
		// conversation, paid for by every attempt after it.
		if size := model.MeasurePrompt(prompt); size.Large() {
			log.Printf("level=warning condition=prompt_size task=%q run=%q attempt=%d "+
				"bytes=%d tokens=%d share=%.2f message=%q",
				d.TaskID, d.RunID, d.Attempt, size.Bytes, size.EstimatedTokens, size.Share(),
				"this task's assembled prompt is "+size.String())
		}

		early = &earlySession{
			store: store, taskID: task.ID, runID: d.RunID, framework: frameworkFor(task, cfg),
			now: cfg.now, ctx: context.WithoutCancel(ctx),
		}
		// task.AgentFramework is the framework this run was built on
		// (runFrameworkFor, above), so a session another framework left is
		// not resumed here; an interactive task is told why.
		noteFrameworkSwitch(ctx, store, task, obs, framework, frameworkFor(task, cfg), cfg.now())
		resumeSession := model.ResumeTokenFor(obs, frameworkFor(task, cfg))
		var suspendedPrompt string
		if resumeSession != "" {
			newComments := commentsSinceSuspend(comments, priorDelivered)
			suspendedPrompt = BuildSuspendedPrompt(task, prepared.Dir, prepared.Root, history, prepared.Setup, newComments, dynamicSections...)
		}
		// Which run this is, for a Framework whose Host launches it into
		// a run host of its own (runhost.Host): the run host's labels
		// and its self-enforced ceiling come from here. The budget plus
		// the most wait credit it can earn is the latest this run can
		// legitimately end; runhost.Host adds its adoption allowance.
		hostCtx := runhost.WithRun(agentCtx, runhost.Run{Task: task.ID, Run: d.RunID,
			Deadline: runHostDeadline(cfg)})
		var firstTurnOnce sync.Once
		noteFirstTurn := func() {
			firstTurnOnce.Do(func() {
				if store != nil {
					firstTurnAt := cfg.now()
					if cfg.Now == nil && firstTurnAt.Before(agentStartedAt) {
						firstTurnAt = agentStartedAt
					}
					if ferr := store.SetRunFirstTurn(context.WithoutCancel(ctx), d.RunID, firstTurnAt); ferr != nil {
						log.Printf("orchestrator: run %s: recording first turn: %v", d.RunID, ferr)
					}
				}
			})
		}
		runTools := make([]mcp.Tool, len(tools))
		for i, t := range tools {
			orig := t.Handler
			if orig != nil {
				t.Handler = func(tctx context.Context, args map[string]any) mcp.Result {
					noteFirstTurn()
					return orig(tctx, args)
				}
			}
			runTools[i] = t
		}
		runCfg := agent.RunConfig{
			Prompt: prompt, SuspendedPrompt: suspendedPrompt, Tools: runTools, SandboxRoot: sandboxRoot, GranuleVM: granuleVM, Container: container, K8sPod: cfg.sandboxPod,
			SandboxEnvFile: cfg.sandboxEnvFile,
			SandboxHide:    cfg.sandboxHide,
			// The MCP servers this run's capability grants expose, each
			// registered beside grain-sandbox (§5.2.4).
			CapabilityMCPServers: cfg.capabilityMCPServers,
			Repo:                 repo, Branch: task.BranchName(),
			// The branch that branch merges into, which is what
			// base_branch_status reads CI for. Passed as the task wrote
			// it, empty included: an empty Base is a task that merges
			// into the repo's default branch, and resolving it here
			// would spend a GitHub read on every dispatch for a tool the
			// run may never call (mcp.PullRequestScope.Base).
			Base: task.Base,
			// Whether this dispatch is the merge queue's repair of that
			// branch, which is what makes base_branch_status tell the run
			// to fix breakage it inherited rather than to leave it alone
			// -- the same thing repairCISection has already said in the
			// prompt, said again by the tool it will be reading when the
			// question actually comes up.
			Repair:            repair,
			BaseIncident:      history.BaseIncident,
			PullRequestNumber: externalPRNumberForTarget(task),
			// Whether this repository has declared GitHub's check runs
			// are not a source of verdicts for it, which is what makes
			// pull_request_status and base_branch_status say so instead
			// of listing checks the merge queue is not reading
			// (mcp.PullRequestScope.GitHubCIIgnored).
			GitHubCIIgnored: gateIsGrainCI,
			// The name of the credential the branch is pushed with, so
			// that subprocess reads GitHub as the same account
			// (runGitCredential above).
			GitCredential: gitCredential,
			// TaskID is what lets a Framework's own forked mcpserver ask
			// the daemon to act for this run rather than only on its
			// sandbox -- open_pull_request, today (see RunConfig.TaskID).
			TaskID: task.ID,
			// Whether somebody is sitting with this task, which is what
			// puts the chat tool on the run's roster: a message posted
			// into the task's own conversation and answered inside the
			// same run, rather than a dispatch per thing said
			// (mcp.NewChatTools). Read off the task exactly as it was
			// filed -- interactiveDispatch reads the same field for the
			// longer clock such a run gets.
			Interactive: task.Interactive,
			// The wall-clock deadline this run is under before any wait
			// credit -- clock.base, which is fixed for the run's whole
			// life and is what the agent's own tools count down from and
			// credit upwards (agent.RunConfig.Deadline). Not base plus
			// the credit granted so far, which moves: the process being
			// told is a fork that outlives no part of this one, and telling it a
			// number that will be stale within the minute is how the two
			// halves of one deadline drift apart.
			Deadline: clock.base,
			// A durable judgement task's wake runs under the per-wake
			// tool budget (model.UsesToolBudget, DT-13).
			ToolBudget: model.UsesToolBudget(task),
			Grants:     grants, GrainSourceDir: grainSourceDir, GrainSecretsDir: grainSecretsDir, GrainStateDir: grainStateDir,
			LocalScripts: localScripts, LocalScriptsEnvFile: localScriptsEnvFile,
			// Which repositories a proposal this run makes may be filed
			// against -- the half of propose_task's `repo` argument only
			// the store can answer (proposalRepos).
			ProposeRepos: proposeRepos,
			MaxTurns:     cfg.MaxAgentTurns, TranscriptPath: transcriptPath, RunID: d.RunID,
			Addenda: addendaPoller(store, task.ID, comments),
			// Where that same conversation stops in this run's prompt,
			// for the route a comment posted mid-run actually takes:
			// the run's own forked mcpserver polls the daemon from this
			// cursor and appends whatever is new to the next tool
			// result (agent.RunConfig.AddendaAfter, mcp's addenda.go).
			// Seeded off the same slice Addenda above is, so the two
			// halves cannot disagree about what the prompt already
			// says.
			AddendaAfter: lastCommentID(comments),
			// The conversation a previous run of this task left
			// suspended, if it left one this run's framework can reopen
			// (model.ResumeTokenFor). The agent picks that conversation
			// back up instead of working the task out from nothing --
			// which is what makes a run cut off by the wall clock, or
			// parked half a day waiting for an answer, carry on rather
			// than begin again. Empty for a first dispatch and for every
			// framework whose sessions do not outlive a run, which
			// leaves the dispatch exactly as it has always been.
			ResumeSession: resumeSession,
			SandboxReused: cfg.sandboxReused,
			// And the conversation this run is in, written down the
			// moment the framework names it (earlySession).
			OnResumableSession: early.record,
		}
		result, runErr = framework.Run(hostCtx, runCfg)
		if runErr == nil || (result != nil && (len(result.ToolCalls) > 0 || strings.TrimSpace(result.FinalText) != "")) {
			noteFirstTurn()
		}
		// The stop rule, for a framework whose CLI cannot apply it from
		// inside the run: a turn that ended promising more work, or a
		// background run that ended with no closing move, is resumed
		// once in the same session with the nudge (continueStoppedRun).
		if !taskKeepRunning(context.WithoutCancel(ctx), store, task) {
			result, runErr = continueStoppedRun(hostCtx, framework, runCfg, result, runErr)
		}
		result, runErr, comments = continueSessionTurns(hostCtx, ctx, store, cfg, framework, runCfg, task, d.RunID, clock.deadline, comments, result, runErr)
		agentRan = true
		if result != nil {
			resumableSession = result.ResumableSession
		}
		// Unregistered before the clock is stopped, so that the last
		// possible credit report finds either a live clock or no run at
		// all, never a stopped clock it would be told was still live.
		stopIdle()
		stopCrediting()
		clock.stop()
		stopPause()
		cancelRun(nil)
		<-watcherDone

		// What the wait credit came to, for the record that lets the cap
		// be re-priced against real runs rather than re-argued
		// (model.RunWaitCredit). Read here, from the one thing that
		// holds it, and folded into the census written below.
		if blocked, credited := clock.ledger(); blocked > 0 {
			waitCredit = &model.RunWaitCredit{Blocked: blocked, Credited: credited}
		}

		// A run left running in its run host as the daemon stops
		// (runhost.Host.Detach): it has not ended, so nothing here may
		// finish it. Its row stays live, its leases stay held, its
		// transcript file stays where the next daemon appends to it, and
		// runOne leaves its sandbox standing -- all of it for the daemon
		// that adopts the run (RecoverRunHosts, ResumeAdoptedRun).
		if errors.Is(runErr, agent.ErrDetached) {
			log.Printf("orchestrator: run %s: left running in its run host for the next daemon to adopt", d.RunID)
			return nil, fmt.Errorf("orchestrator: run %s: %w", d.RunID, runErr)
		}

		// Whatever was recorded early stops before any handover turn or
		// recordRunEnd write, so nothing lands on top of the answer below.
		recordedEarly = early.finish()
		_, credited := clock.ledger()
		end := classifyRunEnd(ctx, store, cfg, task, d.RunID, result, runErr, context.Cause(agentCtx),
			credited, runScope, agentStartedAt)
		end = applySandboxOOM(ctx, store, cfg, task, d.RunID, end)
		if end.suspend == model.SuspendContext {
			session := resumableSession
			if session == "" {
				session = recordedEarly
			}
			if session == "" {
				session = runCfg.ResumeSession
			}
			lastTurnForHandover(ctx, store, cfg, framework, runCfg, task, session)
		}
		outcome, detail, runErr = end.outcome, end.detail, end.runErr
		unmatched, burstRuns, burstDetail = end.unmatched, end.burstRuns, end.burstDetail
		suspend, modelHint = end.suspend, end.modelHint
	}

	// cfg.now(), not at: at is the moment RunCycle began this whole
	// cycle, recorded as this run's StartedAt above, and passing it here
	// as well stamped finished_at with the start time -- so every run
	// ever recorded, however long the agent actually worked, read back
	// as having taken zero seconds. That reading is what `grain get` and
	// the UI's own attempt timeline show, and it hid the difference
	// between a run that failed on its first turn and one that worked
	// for an hour before it did -- the first question anyone asks of a
	// failed run. See Config.Now.
	//
	// Detached from ctx's cancellation: when the daemon is shutting down
	// (ctx.Err() != nil), this row still needs to be finished durably so
	// that interrupted status and checkpointed sessions are preserved --
	// and so does everything written about the run below it (transcript,
	// census, the leases it held). Written on ctx, a shut-down daemon's
	// interrupted run lost its transcript and tool census and returned
	// "finishing run: context canceled" for a row it had finished
	// (docs/test-runs/2026-10-12-upgrade-drain-interrupted.md).
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancelFinish()
	if outcome == "succeeded" {
		// Mark the comments included in this dispatch's prompt as delivered
		// once the run completes its turn, before recordRunEnd checks
		// interactiveMessageWaiting for any comment that arrived mid-run
		// after this prompt was assembled. Keeping this on succeeded turns
		// ensures a dispatch that fails during setup or execution does not
		// advance DeliveredCommentID past comments the agent never answered
		// (undeliveredHumanComment's started-time rule still prevents a
		// failed interactive run from looping).
		if err := store.AdvanceDelivered(finishCtx, task.ID, lastCommentID(comments), cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: recording the conversation it was dispatched for: %v", d.RunID, err)
		}
	}
	finishErr := recordRunEnd(ctx, finishCtx, store, cfg, task, d.RunID, obs, result,
		runEnding{outcome: outcome, detail: detail, unmatched: unmatched, burstRuns: burstRuns, burstDetail: burstDetail,
			suspend: suspend, modelHint: modelHint},
		waitCredit, agentRan, resumableSession, recordedEarly)
	// A grant this task holds that no run of it can ever be given, said
	// in the task's own conversation rather than only on the run that
	// broke on it. Written here, after the outcome above, and logged
	// rather than surfaced on failure: the run has already failed, and a
	// comment that could not be written must not turn that into a second
	// kind of failure. See noteRefusedGrant.
	var refused *refusedGrantError
	if errors.As(prepErr, &refused) {
		if err := noteRefusedGrant(ctx, store, task, refused, cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: explaining its refused capability on the task: %v", d.RunID, err)
		}
	}
	// So do expired credentials on the host: no attempt starts the tool
	// until somebody renews them. Parked on the sentence
	// that says so, and left off the streak (model.EnvironmentOutcome).
	if outcome == model.EnvironmentOutcome {
		if err := stopOnExpiredHostCredentials(ctx, store, task, detail, cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: parking it on expired host credentials: %v", d.RunID, err)
		}
	}
	// A base branch that is gone stays gone: stop here, at the first
	// attempt, rather than let dispatch offer the task again until
	// MaxConsecutiveFailures retires it. See stopOnMissingBase.
	var gone *baseGoneError
	if errors.As(checkoutErr, &gone) {
		if err := stopOnMissingBase(ctx, store, task, gone, cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: stopping on its missing base branch: %v", d.RunID, err)
		}
	}
	// So does a repository the team's credential cannot reach: access is
	// granted on GitHub or in Team settings, and no later attempt gets it
	// by trying again. See stopOnAccessRefused.
	var refusedAccess *accessRefusedError
	if errors.As(checkoutErr, &refusedAccess) {
		if err := stopOnAccessRefused(ctx, store, task, refusedAccess, cfg.now()); err != nil {
			log.Printf("orchestrator: run %s: stopping on its refused clone: %v", d.RunID, err)
		}
	}
	// Only now, with the store already carrying whatever final story this
	// run has to tell (FinishRun and SetRunTranscript, just above), does
	// the live file cfg.TranscriptDir named stop mattering: a caller
	// still polling AttemptTranscript sees FinishedAt set and reads the
	// store from here on, never this file again, so removing it earlier
	// (before either write above landed) would have opened a window where
	// a still-"running" attempt suddenly read back as empty. Best-effort:
	// a Framework never given a TranscriptPath (or one that errored before
	// opening it) leaves nothing here to remove, and os.Remove's own
	// error for that is ignored the same way any other remove failure
	// would be -- there is nothing left to do differently about a stray
	// file at this point beyond leaving it for an operator to notice.
	if transcriptPath != "" {
		os.Remove(transcriptPath)
	}
	revokeAll(finishCtx, store, cc, withUnconsumed(materialized, cfg.PreparedCapabilities))
	if finishErr != nil {
		// result, not nil, for the reason the runErr return below gives:
		// the agent may have pushed before this write failed, and the
		// caller can only salvage a branch it is handed a result for. A
		// row that failed to finish is still live, so nothing else would
		// find that branch until a restart's RecoverOrphanedRuns.
		err := fmt.Errorf("orchestrator: finishing run %s: %w", d.RunID, finishErr)
		if runErr != nil {
			err = errors.Join(runErr, err)
		}
		return result, err
	}
	if runErr != nil {
		// result, not nil: a failed run that pushed a branch before it
		// broke still left work on GitHub, and the caller needs the
		// result to find it. See agent.Framework.
		return result, runErr
	}
	return result, nil
}

// usageLimitDetail is what a run stopped by the agent's own usage limit
// records in task_run.detail: what the provider said, and when this
// deployment will try again. Both halves matter to the person reading
// the attempt -- the first says the run was not at fault, the second
// says nothing is stuck and roughly how long the queue is standing
// still.
//
// A zero until is a deployment with no Pause wired (Config.Pause): the
// limit was still real and still worth recording, there is simply
// nothing paused to name an end for.
//
// What is paused is named as well as when it lifts, since a pause covers
// the credential that met the limit and not the rest of the deployment
// (Pause): "dispatch onto connection claude-a (team alpha) is paused
// until ..." is the difference between an operator reading this attempt
// and concluding grain has stopped, and reading it and knowing their
// other connections' tasks are still moving.
func usageLimitDetail(limit *agent.UsageLimitError, scope PauseScope, until time.Time) string {
	what := "the agent's usage limit was reached"
	if limit != nil {
		what = limit.Error()
	}
	if until.IsZero() {
		return what
	}
	return fmt.Sprintf("%s; dispatch onto %s is paused until %s", what, scope.normalized().describe(), until.UTC().Format(time.RFC3339))
}

// pauseReachNote is what a run's detail adds about the reach of the
// pause its limit opened on a connection: that the connection's other
// models keep dispatching, or why every model on it is paused.
func pauseReachNote(held model.UsageLimitPause) string {
	switch {
	case held.Until.IsZero() || held.Connection == "":
		return ""
	case held.Model != "":
		return " (model-scoped: the connection's other models keep dispatching)"
	case held.Escalated != "":
		return " (escalated to every model on the connection: " + held.Escalated + ")"
	}
	return ""
}

// pauseHeldScope is scope as the pause Begin left in force describes it:
// the model it named, or none once it escalated to the whole connection.
// scope itself where nothing was held (a deployment with no Pause).
func pauseHeldScope(scope PauseScope, held model.UsageLimitPause) PauseScope {
	if held.Until.IsZero() || scope.Connection == "" {
		return scope
	}
	scope.Model = held.Model
	return scope
}

// capacityDetail is what a run the provider had no capacity for records
// in task_run.detail: the provider's own words, and the sentence that
// says what happens next.
//
// The second half is there because the first is easy to misread. An
// operator who finds an attempt saying only "503 Service Unavailable"
// has no way to tell whether the task is now stuck, retiring, or fine --
// which is the question this ending exists to answer, and the reason it
// is not simply "failed". model.EndingOf reads the outcome rather than
// this sentence, so nothing depends on its wording.
func capacityDetail(capacity *agent.CapacityError) string {
	what := "the agent's provider had no capacity for this run"
	if capacity != nil {
		what = capacity.Error()
	}
	return what + "; the task is not counted as failed and will be retried"
}

// setupEnding is the outcome and detail of a run whose setup failed with
// err: unchanged, unless what failed was a host tool the kernel would not
// start for want of host credentials -- an ambient source's tool minting
// the clone's credential, say -- which is the host's doing and is recorded
// as model.EnvironmentOutcome, in the words that say how to renew them. A setup
// step's error may have crossed a process (the git proxy answering a
// clone), so the refusal is also recognised by its text.
func setupEnding(err error, outcome, detail string) (string, string) {
	if expired, ok := authfix.As(authfix.Wrap(err, "", "")); ok {
		return model.EnvironmentOutcome, expired.Error()
	}
	return outcome, detail
}

// suspectedRefusalSuffix is what a failed run's own detail says when its
// text reads like the provider refusing it, and grain could not name the
// refusal -- nothing at all for the ordinary failure, which is most of
// them.
//
// It exists because the person reading one failed task is not the person
// reading a week of `grain failures`, and they are the ones with the
// context to recognise a provider's new wording. The run's detail
// already carries the provider's own words (the arm above leads with
// them); this only says what they may be, and says it as a maybe.
//
// Hedged deliberately, and cheap to be wrong about. agent.
// ResemblesRefusal is the loose read by design -- it matches words a
// tight marker would be reckless to match (its own doc comment on why
// that is safe here) -- so a share of these sentences will sit on runs
// that failed perfectly ordinarily. Nothing reads this sentence:
// model.EndingOf judges the outcome, the streak counts the outcome, and
// the outcome is unchanged. Recognising a refusal properly is
// capacity.go's job, and the fix for a real one is a marker there, not a
// looser test here.
func suspectedRefusalSuffix(text string) string {
	if text == "" || !agent.ResemblesRefusal(text) {
		return ""
	}
	return "; this reads like the provider refusing the run rather than the run failing, " +
		"but it matched none of grain's known refusals -- see `grain failures`"
}

// partialWorkSuffix names what a failed run got done before it failed, or
// nothing at all when the framework returned no result to say. Kept to
// names and counts, like noActionDetail: this lands in a stored outcome
// column that `grain get` prints, not a transcript.
//
// The one way a run can now fail with a result behind it uses this: a
// framework that returned an error (above). An erroring tool call used to
// be a second way, and used to record only the failing call's name, so a
// run that opened its pull request and then tripped over something
// unrelated read back as never having opened one -- a wrong answer to the
// only question task_run.detail can be asked about tool use, not merely a
// thinner one. It is not an ending at all any more (outcomeOf), which
// leaves this to the framework's own failures.
func partialWorkSuffix(result *agent.Result) string {
	if result == nil || len(result.ToolCalls) == 0 {
		return ""
	}
	return fmt.Sprintf("; the run made %d tool call(s)%s first",
		len(result.ToolCalls), toolCallSummary(result))
}

// outcomeOf reads agent.Result.ToolCalls -- the only record of what
// happened inside the run (mcp/mock_tools.go's own sink is internal and
// discarded when Run returns) -- and turns it into a run outcome and a
// short reason for it: a run that made no tool call at all failed, since
// an agent that never touched run_command did not do the work, and every
// other run that got as far as its tools reads "succeeded" here until
// ProcessResult has checked whether those calls amounted to anything
// (Store.SetRunOutcome's own doc comment on that division of labour).
// Ported from pkg/orchestrate's own runAgent (bwsalmon/agents#254),
// extended with detail for bwsalmon/agents#403's own "a human should see
// why, not just that".
//
// An errored tool call is deliberately not a failure any more. It used to
// be -- the first ToolCall with IsError set ended this function -- and
// that read a normal turn of the agent's own loop as a broken run. IsError
// is how a tool reports an ordinary result the agent is expected to read
// and work around: pkg/mcp's run_command sets it for any non-zero exit
// status, so a grep that matched nothing, a test suite that failed before
// the agent fixed it, or a `git diff --quiet` that found changes all
// marked the whole run failed; read_file sets it for a file that is not
// there, and edit_file for an old_string that did not match or matched
// twice, which is the search-and-refine loop working exactly as intended.
// Almost every real run trips one of those, so almost every real run --
// including the ones that committed, pushed and opened a pull request --
// was recorded "failed".
//
// What that cost was never cosmetic. task_streak (schema.go) counts every
// finished run whose outcome is not "succeeded", so those bogus failures
// drive dispatch.retryEligible's exponential backoff on a task that is
// working fine, and take it to model.MaxConsecutiveFailures -- state
// 'failed', dispatched no more -- on the strength of a grep. It also had
// to be papered over twice downstream, once in model.Transitions and once
// in ui.Client.Task, both of which suppress a failure streak on a task
// that plainly completed (bwsalmon/agents#502 and #514); those guards are
// treating this symptom rather than the cause.
//
// A run really broken by its tools does not need this heuristic. A tool
// framework that failed -- the agent CLI dying, its MCP connection
// dropping, a sandbox that stopped answering -- comes back as a non-nil
// error from framework.Run and is recorded "failed" by RunDispatch above,
// with the framework's own diagnosis. A run whose tools worked but which
// achieved nothing is corrected to "no_action" by ProcessResult once a
// push, a question and a closing comment have all actually been ruled
// out. This function's job is only the guess in between.
//
// Every ending that had a run behind it at all carries toolCallSummary,
// including the successful one, which used to record nothing. That is not
// symmetry for its own sake: which tools a run reached for is the only
// evidence there is for whether a tool grain went to the trouble of
// building and naming in the prompt is actually being used, and until now
// it survived a successful run nowhere. agent.Result is never persisted;
// noActionDetail and partialWorkSuffix wrote the summary only for runs
// that failed or achieved nothing; and Result.Transcript is prose, is
// best-effort per framework (agent.Result's own doc comment), and renders
// a call as a "> name(args)" line that any tool's own *output* can
// contain verbatim -- so counting calls out of it is both framework-
// specific and unsound. A run that pushed, checked CI, repaired and
// pushed again is exactly the run that succeeds, so the successful path
// was precisely the one where the question "did it call the tool?" could
// not be answered.
//
// Names and counts only, into a stored column a task listing prints --
// the same bound noActionDetail's own doc comment sets, and the reason
// this is not a transcript store.
func outcomeOf(result *agent.Result) (outcome, detail string) {
	if len(result.ToolCalls) == 0 {
		return "failed", "the agent made no tool calls at all"
	}
	return "succeeded", fmt.Sprintf("the run made %d tool call(s)%s%s",
		len(result.ToolCalls), toolCallSummary(result), erroredCallSuffix(result))
}

// textOnlyAnswerDetail is what a run that answered a conversation in
// plain text, without calling a tool, is recorded with.
const textOnlyAnswerDetail = "the agent answered in text without calling a tool"

// outcomeFor is outcomeOf for the task the run belonged to.
//
// The one difference is an interactive task's run that called no tool
// but did say something. On a background task that is a run that never
// attempted the work. In a conversation it is an answer -- somebody asked
// a question the agent could answer from what was already in front of it
// -- and its text is relayed into the chat like any other
// (ProcessResult). Recording it "failed" put grain's "That did not get
// an answer" line under a real answer and counted a failure against the
// session (grain/task-1774). A run that produced neither a tool call nor
// any text is still a failure, interactive or not.
func outcomeFor(task model.Task, result *agent.Result) (outcome, detail string) {
	if (task.Interactive || task.KeepRunning) && result != nil && len(result.ToolCalls) == 0 &&
		strings.TrimSpace(result.FinalText) != "" {
		return "succeeded", textOnlyAnswerDetail
	}
	if task.KeepRunning && result != nil && len(result.ToolCalls) == 0 {
		return "succeeded", "the run exited cleanly"
	}
	return outcomeOf(result)
}

// erroredCallSuffix says how many of a run's tool calls came back as
// errors, or nothing at all when none did.
//
// Recorded even though it no longer decides the outcome (see outcomeOf):
// toolCallSummary already marks which tools errored and how often, but it
// marks them per tool, and the one number a human scanning `grain get`
// wants is how much of the run was spent on calls that did not land. A
// handful is the ordinary shape of agentic work; nearly all of them is
// the shape of a sandbox that stopped answering, and that is worth being
// able to see on a run whose outcome is otherwise unremarkable.
func erroredCallSuffix(result *agent.Result) string {
	errored := 0
	for _, c := range result.ToolCalls {
		if c.IsError {
			errored++
		}
	}
	if errored == 0 {
		return ""
	}
	return fmt.Sprintf("; %d of them returned an error", errored)
}

// prepareCapabilities resolves and materializes cc.Task's capability
// grants against reg and assembles the prompt they, the task itself,
// history (what has already happened to it -- its earlier attempts and
// its conversation, both rendered by BuildPrompt) and attachments (every
// file the task or its conversation carries -- see placeAttachments) all
// contribute, applying every SideSandbox placement
// under sandboxRoot and every attachment under AttachmentsDir on the way.
// A nil registry, or a task with no Grants, skips capability resolution
// and returns the rest of the prompt unchanged -- a deployment or test
// that grants no capabilities needs to configure none of this. A non-nil
// error means preparation itself failed (or a grant was refused) and the
// caller must not run the agent at all -- the same "a half-materialized
// capability is never described to the agent as present" rule
// model.MaterializeGrants's own doc comment holds to, one level up: an
// agent whose capability request was refused must not run at all, since
// the task it would work almost always depends on it. Ported from
// pkg/orchestrate's own prepare (bwsalmon/agents#254).
//
// That rule holds for every grant, including one a task was filed with
// because the deployment attaches it to everything, or because the repo
// it targets adds it (model.Config.DefaultCapabilities,
// model.RepoConfig.DefaultCapabilities, model.GrantByDefault). There is no
// "degrade rather than fail" tier here, and it is not an oversight: v1
// needed one because it minted a GCP key per dispatch for every sandbox,
// with no task holding the request and nowhere to record that it had
// failed, so swallowing the error was the only way a broken minter did
// not stop the deployment. A default here is instead seeded onto the
// task at creation, so a failed mint fails one task, says so on it, and
// is fixed either by fixing the capability or by detaching it -- from
// that task, or from the default set, whichever the operator meant.
// Silently running an agent without a capability its task is recorded as
// holding would trade that for a run that quietly does the wrong work.
//
// history, prepared, canOpenPullRequest and maxRuntime are passed
// straight through to BuildPrompt -- prepared as the three things it
// carries, the checkout's directory, the sandbox directory that hangs
// off, and what the repo's setup command did in it -- which is where all
// of them are explained: nothing here
// reads any of them, this being the one path that assembles a dispatch's
// prompt.
//
// settingsRepo is the repository this deployment keeps its own settings
// in (Config.StateRepo, resolved by the caller), and is read here rather
// than passed on: it is half of a fact only this path holds both halves
// of -- the other being what prepareCheckout found in the tree -- and
// settingsRepoSection is where the two are turned into words. The zero
// RepoRef says nothing, which is what a local-only deployment and a
// caller that wired no answer both get.
//
// checkImage travels beside it and for the same reason -- it is the
// deployment's own answer rather than the checkout's -- but it is only
// ever read when the checkout is a dump: it is the image
// stateRepoSection tells such a run to check its change with, resolved
// by the caller (Config.checkImage) so that "" here still renders a
// command that runs.
//
// memory is what grain has learned about working here (resolveMemory),
// and it travels with promptExtension below because it lands in the same
// place for the same reason: both are text about work here in general
// rather than about this run, so both read as an aside anywhere earlier.
// Memory goes first of the two -- what is known before what is asked --
// and "" (every deployment whose memory agent has not run yet) leaves
// the prompt exactly as it was.
//
// promptExtension is the standing instructions for this
// run, already resolved across the three layers that can carry them
// (resolvePromptExtension). It goes last, after the capability sections
// and everything else: it is the one part of this prompt a human wrote
// for runs in general rather than for this run in particular, and
// wedging it between the conversation and a capability's own "your key
// is at /path" is where it would read as an aside. Last, it reads as the
// standing rule it is. "" -- no deployment, repo or task has written one
// -- leaves the prompt exactly as it was before any of this existed.
func prepareCapabilities(ctx context.Context, reg *model.CapabilityRegistry,
	cc model.CapabilityContext, preparedMaterial []model.Materialized, sandboxRoot string, placer SandboxPlacer, tools []mcp.Tool, history History,
	attachments []model.Attachment, prepared checkout, settingsRepo model.RepoRef, checkImage string,
	canOpenPullRequest bool, promptExtension, memory string, maxRuntime time.Duration) (materialized []model.Materialized, prompt string, err error) {

	materialized, err = materializeCapabilities(ctx, reg, cc, preparedMaterial, sandboxRoot, placer)
	if err != nil {
		return materialized, "", err
	}
	prompt, _, err = prepareCapabilityPrompt(ctx, cc, materialized, tools, history, attachments, prepared,
		settingsRepo, checkImage, canOpenPullRequest, promptExtension, memory, maxRuntime)
	return materialized, prompt, err
}

// refusedGrantError is one grant this task holds that its provider will
// not honour: the failure materializeCapabilities has always produced
// for a refused resolution, with the two halves kept apart so the task's
// own conversation can be written from them (noteRefusedGrant) rather
// than from a string that has to be parsed back.
//
// Error() is word for word what this failure read as before it was a
// type, because it is what task_run.detail carries and what `grain get`
// shows: the shape of the record did not change, only what else can be
// done with it.
type refusedGrantError struct {
	Capability string
	Reason     string
}

func (e *refusedGrantError) Error() string {
	return fmt.Sprintf("capability %q refused: %s", e.Capability, e.Reason)
}

// noteRefusedGrant is what a task gets instead of a run when one of its
// capabilities is refused: the provider's own explanation in the task's
// own conversation, and a stop -- noteEmptyBranch's two halves, for a
// failure of exactly the same shape.
//
// The shape is "this will fail identically every time, and nothing the
// run does can change it". A refusal is a resolution, not an error: the
// provider has looked at this task and said no, and it will say the same
// no to the next attempt, so the attempts after the first buy nothing.
// Failing the dispatch stays the right answer to that (a refused grant
// must never be quietly dropped, or a run would proceed without material
// it was told it had) -- what was missing is anybody being told. The
// failure was recorded on the run, in task_run.detail, which is the one
// place a person sitting in an interactive task's chat does not look:
// grain/task-382's own operator ticked package-publish on such a task
// and got two failed runs and no sentence, because every message they
// sent started another run that died before an agent existed to answer
// them.
//
// So the reason travels into the thread, where it can be read and
// replied to, and the task is parked on it the way every other park
// works -- Observation.PendingQuestionCommentID, cleared by any reply,
// which takes the task out of task_ready until somebody has seen this.
// A reply after the grant is taken off dispatches a run that works; a
// reply with the grant still on it parks again on the next attempt,
// which is the same refusal said once more rather than four more runs
// spent on it.
//
// Attributed to grain itself rather than on behalf of the agent, for
// noteBaseRetarget's reason: no run said any of this -- none got far
// enough to say anything.
func noteRefusedGrant(ctx context.Context, store *model.Store, task model.Task,
	refused *refusedGrantError, now time.Time) error {

	log.Printf("orchestrator: task %s holds capability %q, which its provider refuses (%s); "+
		"parking it on grain's own comment rather than retrying a dispatch that cannot work",
		task.ID, refused.Capability, refused.Reason)

	body := fmt.Sprintf(
		"This task could not be dispatched: it holds the capability `%s`, and grain refuses it.\n\n"+
			"> %s\n\n"+
			"No run can start while that is true, and every attempt fails in the same place -- before "+
			"the agent exists, so nothing here can answer you. Grain has stopped rather than repeating it.\n\n"+
			"Take `%s` off this task (its Capabilities, on the task's own pane), or close whatever gap "+
			"the refusal names, and reply here to have it dispatched again.",
		refused.Capability, refused.Reason, refused.Capability,
	)
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}},
		Body:      body,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: recording that %s's capability %q was refused: %w",
			task.ID, refused.Capability, err)
	}
	return observeField(ctx, store, task.ID, now, func(o *model.Observation) {
		o.PendingQuestionCommentID = &commentID
	})
}

// materializeCapabilities resolves, materializes and applies a task's
// capabilities before anything tries to clone its repository. A configured
// non-GitHub remote may depend on exactly such an ambient placement (an SSH
// key or Git credential helper), so doing this after prepareCheckout makes the
// credential usable after recreation but not on the first checkout.
func materializeCapabilities(ctx context.Context, reg *model.CapabilityRegistry,
	cc model.CapabilityContext, preparedMaterial []model.Materialized, sandboxRoot string,
	placer SandboxPlacer) ([]model.Materialized, error) {

	if reg == nil || len(cc.Task.Grants) == 0 {
		return nil, nil
	}
	resolved, err := model.ResolveGrants(ctx, reg, cc)
	if err != nil {
		return nil, fmt.Errorf("resolving capabilities: %w", err)
	}
	for _, gr := range resolved {
		if gr.Resolution.Refused {
			return nil, &refusedGrantError{Capability: gr.Grant.Capability, Reason: gr.Resolution.Reason}
		}
	}
	materialized, err := model.CompleteMaterializeGrants(ctx, reg, cc, resolved, preparedMaterial)
	if err != nil {
		return materialized, fmt.Errorf("materializing capabilities: %w", err)
	}
	if err := applyPlacements(ctx, sandboxRoot, placer, materialized); err != nil {
		return materialized, fmt.Errorf("applying placements: %w", err)
	}
	return materialized, nil
}

// prepareCapabilityPrompt performs the checkout-dependent half of capability
// setup. Materialization is intentionally absent here: RunDispatch already
// applied these exact placements before cloning, and recreation retains this
// slice so it can re-place rather than re-mint them.
func prepareCapabilityPrompt(ctx context.Context, cc model.CapabilityContext,
	materialized []model.Materialized, tools []mcp.Tool, history History,
	attachments []model.Attachment, prepared checkout, settingsRepo model.RepoRef,
	checkImage string, canOpenPullRequest bool, promptExtension, memory string,
	maxRuntime time.Duration) (string, []string, error) {

	extension := appendSection(memorySection(memory), promptExtensionSection(promptExtension))
	prompt := BuildPrompt(cc.Task, prepared.Dir, prepared.Root, canOpenPullRequest, maxRuntime, history, prepared.Setup)
	// Right after the prompt that describes the checkout, and before
	// anything about capabilities or attachments: it is a fact about the
	// directory those sentences just pointed at, the same placement
	// setupSection has inside BuildPrompt for the same reason. Not inside
	// BuildPrompt itself because what it turns on is not a fact about the
	// task at all -- it is what prepareCheckout found in the tree, which
	// only this path has.
	prompt += stateRepoSection(prepared, checkImage)
	// Immediately after it, and on the same terms: which grain's settings
	// those files are (or, for the overwhelmingly common checkout that is
	// not a dump at all, where this deployment's settings live instead).
	// Neither half is in the checkout, so neither can be in BuildPrompt;
	// see settingsRepoSection.
	prompt += settingsRepoSection(prepared, cc.Task.Target, settingsRepo)
	prompt += directorStateSection(prepared, cc.Task)
	prompt += grainSourceSection(prepared, cc.Task)
	attachmentsSection, err := placeAttachments(ctx, tools, attachments)
	if err != nil {
		return "", nil, err
	}
	prompt += attachmentsSection
	var dynamicSections []string
	if attachmentsSection != "" {
		dynamicSections = append(dynamicSections, attachmentsSection)
	}
	sections, err := model.PromptSections(ctx, cc, materialized)
	if err != nil {
		return prompt, dynamicSections, fmt.Errorf("building prompt sections: %w", err)
	}
	for _, section := range sections {
		prompt += "\n\n" + section
		dynamicSections = append(dynamicSections, section)
	}
	return appendSection(prompt, extension), dynamicSections, nil
}

// toolGrants are the capability grants whose whole effect is which tools
// a run gets, and which a forked "grain mcpserver" can therefore serve
// for itself once it is told the run holds them (pkg/cli/mcpserver.go's
// -grant). self-debug, grain-log and bootstrap-playbooks are read-only;
// memory writes only through the daemon's own REST API
// (pkg/capability/memory), and grain-ci replaces wait_for_checks with the
// task-scoped daemon implementation. deployment-health, read-issues,
// db-read and db-write likewise reach the daemon over its REST API
// rather than holding anything of their own. None of them puts a
// credential in the subprocess.
//
// selfrepair's own grant is absent because its one tool blocks on a
// human's reply in the task's chat, and that needs a store handle the
// mcpserver process deliberately does not have.
//
// A fixed order, so that a run's own arguments read the same way twice
// for the same task rather than following whatever order the grants were
// attached in. A capability registered from outside core with RunGrant
// set (capability.RegisterProvider) travels too, after these: one that
// serves no MCP tool at all may be read by the run's agent framework
// instead (agent/antigravity's host-tool grants).
var toolGrants = []string{
	selfdebug.CapabilityName, grainlog.CapabilityName, bootstrap.CapabilityName,
	grainci.CapabilityName, deploymenthealth.CapabilityName,
	readissues.CapabilityName,
	dbaccess.CapabilityDBRead, dbaccess.CapabilityDBWrite,
}

// runGrants is the subset of toolGrants task holds, which is what a
// Framework passes on to the mcpserver it forks
// (agent.RunConfig.Grants). Nothing outside that list travels: a run's
// tools are the only thing this is for, and a capability that mints
// credentials has no business being named in a subprocess's arguments.
// localScriptsFor is the local-scripts declarations task's
// local-scripts:<name> capabilities grant this run (cfg.LocalScripts),
// and a 0600 file of the secrets their scripts name, resolved as the
// task's team -- "" when they name none. An untrusted task
// (localscripts.Untrusted) is served none, whatever it holds.
func localScriptsFor(ctx context.Context, cfg Config, task model.Task, runID string) ([]agent.LocalScripts, string, error) {
	names := localscripts.Granted(task)
	if len(names) == 0 {
		return nil, "", nil
	}
	if why := localscripts.Untrusted(task); why != "" {
		log.Printf("orchestrator: task %s: not serving local scripts: %s", task.ID, why)
		return nil, "", nil
	}
	var out []agent.LocalScripts
	var paths []string
	for _, name := range names {
		path, err := cfg.LocalScripts.Path(name)
		if err != nil {
			return nil, "", err
		}
		out = append(out, agent.LocalScripts{Name: name, Path: path})
		paths = append(paths, path)
	}
	ctx = secrets.WithRun(secrets.WithTask(ctx, task.ID), runID)
	creds, err := localscripts.ResolveCredentials(ctx, cfg.Credentials, paths)
	if err != nil {
		return nil, "", err
	}
	if len(creds) == 0 {
		return out, "", nil
	}
	file, err := localscripts.WriteCredentialsFile("", creds)
	if err != nil {
		return nil, "", fmt.Errorf("writing the local-scripts credentials file: %w", err)
	}
	return out, file, nil
}

func runGrants(task model.Task) []string {
	var grants []string
	for _, grant := range append(slices.Clone(toolGrants), capability.RunGrants()...) {
		if hasGrant(task, grant) {
			grants = append(grants, grant)
		}
	}
	return grants
}

// maxProposalRepos caps the list of repositories a run is told it may
// file a proposal against (proposalRepos).
//
// A cap because the list is bound for a tool description, read on every
// turn by a model that has a task to do: a deployment with forty
// repositories would spend a paragraph naming all of them, and the
// fortieth is no likelier to be the right home for a proposal than the
// task's own repo was. A dozen because the useful subset is where work
// is actually going on -- model.Store.WorkRepos returns the most
// recently worked in first, so this is the head of that list -- and a
// dozen owner/names is a sentence rather than a page.
//
// Nothing is lost by the cap that was not already lost: a repository off
// the end may still be named, judged by the same gate
// (model.Store.WorksInRepo), and the `repo` argument still says what
// happens to a repository this deployment has never worked in. What the
// list buys is the run that would not have known to name one at all.
const maxProposalRepos = 12

// proposalRepos is what a run is told it may set propose_task's `repo`
// to: the repositories this deployment works in (scoped to the task's team),
// most recently worked in first, capped at maxProposalRepos and spelled as the
// store holds them (agent.RunConfig.ProposeRepos, pkg/cli/mcpserver.go's -propose-repo).
//
// A read failure is logged and nothing else. The list is a convenience
// -- a run given none proposes exactly as it did before this existed,
// against its own repository -- and no task should fail its dispatch
// because one extra SELECT did.
func proposalRepos(ctx context.Context, store *model.Store, taskID, teamID string) []string {
	repos, err := store.WorkReposForTeam(ctx, teamID, maxProposalRepos)
	if err != nil {
		log.Printf("orchestrator: task %s: listing the repos a proposal may name: %v", taskID, err)
		return nil
	}
	var named []string
	for _, repo := range repos {
		named = append(named, repo.String())
	}
	return named
}

// runGitCredential is the name of the credential task's branch is pushed
// with -- model.Store.TaskGitCredentialOn's ladder for the task and its
// target: the task's own github-credential grant, else its team's
// connection for that repository on that host -- which a run's forked
// mcpserver is told so its own GitHub reads are made as the same account
// (agent.RunConfig.GitCredential, pkg/cli/mcpserver.go's
// -pr-credential). The proxy resolves the same ladder for the push itself
// (model.Store.GitCredentialOverrideOn), so the two cannot name different
// accounts.
//
// "" for a task with no target, for one targeting a repository the
// controller serves itself (a configuration repository, which needs no
// GitHub credential and has no GitHub to read), and for a ladder that
// ends empty or cannot be read: the first two have nothing to name, the
// last is logged and no more. A dispatch is never stopped here over it --
// a team with no credential is held before its task is ever dispatched
// (heldForGitCredential), and anything that slips past that is better
// off running with unauthenticated reads than not running at all.
func runGitCredential(ctx context.Context, store *model.Store, task model.Task) string {
	if store == nil || task.Target == nil || task.Target.Owner == "" || task.Target.Name == "" {
		return ""
	}
	if model.IsConfigRepo(*task.Target) {
		return ""
	}
	name, err := store.TaskGitCredentialOn(ctx, task.ID, task.Target.Host, task.Target.Owner, task.Target.Name)
	if err != nil {
		log.Printf("orchestrator: task %s: naming the credential %s is pushed with for its run's GitHub reads: %v",
			task.ID, task.Target, err)
		return ""
	}
	return name
}

// hasGrant reports whether task carries a grant for capability, by name
// alone: model.Grant.Via records how the grant was come by, never what it
// lets a task do, so a self-debug grant a human ticked and one a repo's
// defaults attached are the same grant here (model.GrantsSubsetOf draws
// the same line for the same reason).
func hasGrant(task model.Task, capability string) bool {
	for _, g := range task.Grants {
		if g.Capability == capability {
			return true
		}
	}
	return false
}

func hasAPIScope(task model.Task, scope model.APIScope) bool {
	names := make([]string, 0, len(task.Grants))
	for _, g := range task.Grants {
		names = append(names, g.Capability)
	}
	return model.APIGrantsOf(names).Has(scope)
}

// appendSection joins one more section onto a prompt, blank-line
// separated the way every other section here is joined, and leaves the
// prompt untouched when there is no section to add -- so "no prompt
// extension" is a prompt with nothing appended rather than one ending in
// two blank lines.
func appendSection(prompt, section string) string {
	if section == "" {
		return prompt
	}
	return prompt + "\n\n" + section
}

// applyPlacements delivers every SideSandbox placement Materialize
// returned into the sandbox, by whichever of the two routes that sandbox
// has: placer, when it can write into itself over its own transport (a
// granule VM, over the same SSH/docker-exec runner its tool calls use --
// see SandboxPlacer), or root, the local directory standing in for the
// sandbox otherwise -- see the package doc comment.
//
// placer wins where both exist, because it writes into the sandbox
// itself: a sandbox that offers one is telling this function where its
// filesystem actually is, and a root alongside it (no current Sandbox has
// both) could only be a staging copy of the same material on the
// controller's own disk -- a second copy of a credential is exactly what
// a placement should not leave behind.
//
// A placement's Path is always the absolute path it would land at inside
// a real sandbox (gcpkey.SandboxKeyPath, geminikey.KeyPath), which placer
// takes as given. root plays the same role a chroot's own root plays for
// such a path, which is why it is joined after stripping the leading
// separator rather than passed through mcp.resolvePath -- that function
// confines an agent's own tool arguments, a different question from where
// a controller-applied placement lands.
//
// A SideController placement is skipped, not written anywhere: no
// current provider returns one (gcpkey and geminikey both mint straight
// into the sandbox), and nothing in v2 has decided what a controller-side
// destination for one even is yet. Ported from pkg/orchestrate
// (bwsalmon/agents#254).
//
// Both root and placer are empty for a sandbox that offers neither. That
// is only a problem for a grant that actually materializes a SideSandbox
// placement (gcpkey, geminikey, githubsandbox): most grants, self-debug
// and self-repair included, materialize none and so never reach the loop
// body below at all (bwsalmon/agents#643).
func applyPlacements(ctx context.Context, root string, placer SandboxPlacer, materialized []model.Materialized) error {
	for _, m := range materialized {
		for _, p := range m.Materialization.Placements {
			if p.Side != model.SideSandbox {
				continue
			}
			// Parsed before either branch so an unusable mode is
			// rejected the same way whichever route the placement
			// takes -- placer is handed the octal string itself,
			// since a remote `install -m` is what applies it there.
			mode, err := strconv.ParseUint(p.EffectiveMode(), 8, 32)
			if err != nil {
				return fmt.Errorf("placement %s: invalid mode %q: %w", p.Path, p.Mode, err)
			}
			if placer != nil {
				if err := placer.PlaceFile(ctx, p.Path, p.Content, p.EffectiveMode()); err != nil {
					return fmt.Errorf("capability %q placing %s in the sandbox: %w", m.Grant.Capability, p.Path, err)
				}
				continue
			}
			if root == "" {
				return fmt.Errorf("capability %q placed %s in the sandbox, but this sandbox has no local directory to place it in and no way to place it remotely", m.Grant.Capability, p.Path)
			}
			full := filepath.Join(root, strings.TrimPrefix(p.Path, "/"))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", filepath.Dir(full), err)
			}
			if err := os.WriteFile(full, []byte(p.Content), os.FileMode(mode)); err != nil {
				return fmt.Errorf("writing %s: %w", full, err)
			}
			if err := os.Chmod(full, os.FileMode(mode)); err != nil {
				return fmt.Errorf("setting mode %s on %s: %w", p.EffectiveMode(), full, err)
			}
		}
	}
	return nil
}

// revokeAll calls Revoke on every capability materialized carried a Lease
// for, then drops it from the store -- the mirror image of
// prepareCapabilities' MaterializeGrants call, run unconditionally
// (success or failure) so a failed run never strands a minted credential
// the way it would if revocation only ran on the happy path. A revoke or
// DropLease failure is logged rather than propagated: the run itself has
// already been finished by the time this runs, and there is nothing left
// to do differently beyond leaving the lease live until an operator or
// model.Reaper notices. Ported from pkg/orchestrate's own revoke
// (bwsalmon/agents#254).
func revokeAll(ctx context.Context, store *model.Store, cc model.CapabilityContext, materialized []model.Materialized) {
	for _, m := range materialized {
		lease := m.Materialization.Lease
		if lease == nil {
			continue
		}
		if err := m.Provider.Revoke(ctx, cc, *lease); err != nil {
			log.Printf("orchestrator: task %s: revoking capability %q: %v", cc.Task.ID, m.Grant.Capability, err)
			continue
		}
		if err := store.DropLease(ctx, cc.Run.ID, lease.Capability, lease.Resource); err != nil {
			log.Printf("orchestrator: task %s: dropping lease for capability %q: %v", cc.Task.ID, m.Grant.Capability, err)
		}
	}
}

// recordMaterializedLeases writes what materialized minted against the
// run's row, beside the reservations StartRun already wrote, so a daemon
// that adopts the run from a run host can revoke them at its end
// (revokeRecordedLeases) the way revokeAll does here. Logged rather than
// returned: a run must not fail on a bookkeeping write, and a lease left
// unrecorded is still the reaper's to collect (daemon.reapCapabilities).
func recordMaterializedLeases(ctx context.Context, store *model.Store, runID string, materialized []model.Materialized) {
	var leases []model.Lease
	for _, m := range materialized {
		if l := m.Materialization.Lease; l != nil {
			leases = append(leases, *l)
		}
	}
	if err := store.RecordLeases(ctx, runID, leases); err != nil {
		log.Printf("orchestrator: run %s: recording the leases it holds: %v", runID, err)
	}
}

// revokeRecordedLeases is revokeAll for a run whose materialized
// capabilities are not in hand -- one this daemon adopted from the daemon
// that started it (ResumeAdoptedRun): every lease recorded against the
// run (StartRun, recordMaterializedLeases) is revoked through its
// capability's provider in reg and then dropped. A lease whose capability
// this daemon no longer registers, or whose revoke fails, is logged and
// left for the reaper, as revokeAll leaves one.
func revokeRecordedLeases(ctx context.Context, store *model.Store, reg *model.CapabilityRegistry, cc model.CapabilityContext) {
	leases, err := store.RunLeases(ctx, cc.Run.ID)
	if err != nil {
		log.Printf("orchestrator: task %s: reading the leases of run %s: %v", cc.Task.ID, cc.Run.ID, err)
		return
	}
	for _, lease := range leases {
		provider, ok := reg.Lookup(lease.Capability)
		if !ok {
			log.Printf("orchestrator: task %s: no provider for capability %q to revoke run %s's lease on %s with",
				cc.Task.ID, lease.Capability, cc.Run.ID, lease.Resource)
			continue
		}
		if err := provider.Revoke(ctx, cc, lease); err != nil {
			log.Printf("orchestrator: task %s: revoking capability %q: %v", cc.Task.ID, lease.Capability, err)
			continue
		}
		if err := store.DropLease(ctx, cc.Run.ID, lease.Capability, lease.Resource); err != nil {
			log.Printf("orchestrator: task %s: dropping lease for capability %q: %v", cc.Task.ID, lease.Capability, err)
		}
	}
}

// withUnconsumed is materialized plus every lease in prepared that it does
// not already carry. materializeCapabilities folds the reservation
// admission made into what it returns, but only once it gets that far: a
// grant that failed to resolve, a refusal, or a provider that failed
// part-way through hands back less than it was given, and the reservations
// it never reached would otherwise be revoked by nobody.
func withUnconsumed(materialized, prepared []model.Materialized) []model.Materialized {
	type key struct{ capability, resource string }
	held := make(map[key]bool, len(materialized))
	for _, m := range materialized {
		if l := m.Materialization.Lease; l != nil {
			held[key{l.Capability, l.Resource}] = true
		}
	}
	out := materialized
	for _, m := range prepared {
		l := m.Materialization.Lease
		if l == nil || held[key{l.Capability, l.Resource}] {
			continue
		}
		out = append(out[:len(out):len(out)], m)
	}
	return out
}

// ApplyCapabilityPlacements delivers capability material using the same transport
// and permissions as agent runs, for non-agent callers such as CI jobs.
func ApplyCapabilityPlacements(ctx context.Context, root string, placer SandboxPlacer, materialized []model.Materialized) error {
	return applyPlacements(ctx, root, placer, materialized)
}

// runSetupAllowance is how long a run's setup -- the clone and the
// repository's own setup command (setupCommandTimeout) between its
// capabilities materializing and its clock starting -- is allowed for in
// capabilityDeadline. Generous on purpose: setup running past it costs a
// run its capabilities' material in the last stretch of its life, which
// is a far worse outcome than material outliving a run by the difference.
const runSetupAllowance = time.Hour

// capabilityDeadline is model.CapabilityContext.Deadline for a run
// dispatched at: the latest it can still be alive. The run's own clock
// starts once its setup is done rather than at dispatch, and runs for
// maxRuntime plus up to RunWaitCreditCap of credit (newRunClock) -- so
// this is dispatch, the setup allowed for, and the longest that clock
// can ever run.
//
// A maxRuntime of 0 is a durable run's (durableDispatch), which has no
// deadline: its leases are minted for durableLeaseTerm and renewed by its
// idle watchdog before they run out (watchIdle), so a run that keeps
// working keeps its material however long it lasts.
func capabilityDeadline(at time.Time, maxRuntime time.Duration) time.Time {
	if maxRuntime <= 0 {
		return at.Add(durableLeaseTerm)
	}
	return at.Add(runSetupAllowance + maxRuntime + RunWaitCreditCap)
}

// runEnding is what an agent run's ending is recorded as: classifyRunEnd's
// answer, from what the framework returned and what cancelled the run.
type runEnding struct {
	outcome, detail string
	runErr          error
	// unmatched, burstRuns and burstDetail are RunDispatch's of the same
	// names: what the run said when no marker could name its failure, and
	// the earlier runs of a refusal burst it completed.
	unmatched   string
	burstRuns   []string
	burstDetail string
	// suspend is the suspension kind a run recorded under
	// model.SuspendedOutcome stopped with (Store.Suspend), "" for every
	// other ending: the kinds with an outcome of their own are read off
	// it (model.SuspensionKindOfOutcome).
	suspend model.SuspensionKind
	// modelHint is the model the run was on (PauseScope.Model), kept on
	// a failover suspension (model.SuspensionKind.IsFailover) so the
	// task's next run is offered the rest of its rotation first.
	modelHint string
}

// suspensionKind is the kind e suspends its run with, or false.
func (e runEnding) suspensionKind() (model.SuspensionKind, bool) {
	if e.suspend != "" {
		return e.suspend, true
	}
	return model.SuspensionKindOfOutcome(e.outcome)
}

// classifyRunEnd decides how a run that reached its agent ended: result
// and runErr are what the framework returned, cause is the cancellation
// cause of the context it ran under (nil when nothing cancelled it),
// credited is the wait credit its clock granted, scope is the credential
// it spent and agentStartedAt is when its agent's time began.
//
// It is RunDispatch's, split out so that a daemon adopting a run another
// daemon started (ResumeAdoptedRun) reads its ending by exactly the same
// rules: a usage limit pauses the same credential, a timeout names the
// same limit, and an unrecognised failure is kept the same way.
func classifyRunEnd(ctx context.Context, store *model.Store, cfg Config, task model.Task, runID string,
	result *agent.Result, runErr, cause error, credited time.Duration, scope PauseScope,
	agentStartedAt time.Time) runEnding {
	end := classifyRunEndArms(ctx, store, cfg, task, runID, result, runErr, cause, credited, scope, agentStartedAt)
	end.modelHint = scope.Model
	return end
}

// classifyRunEndArms is classifyRunEnd's decision, before the model the
// run was on is noted beside it.
func classifyRunEndArms(ctx context.Context, store *model.Store, cfg Config, task model.Task, runID string,
	result *agent.Result, runErr, cause error, credited time.Duration, scope PauseScope,
	agentStartedAt time.Time) runEnding {
	outcome, detail := "failed", ""
	var (
		unmatched   string
		burstRuns   []string
		burstDetail string
	)
	// Read once, before the switch, because it decides the first two
	// branches between them: a run that met the limit itself is what
	// begins the pause, and a run cancelled by somebody else's limit
	// is what the pause did.
	limit, hitLimit := agent.UsageLimit(runErr)
	incompat, hitIncompatibility := agent.ModelIncompatibility(runErr)
	if hitIncompatibility {
		// Compatibility refusals have no provider reset time. Reuse the
		// bounded deployment pause so configuration changes are picked up
		// on a later dispatch without spending the task's failure streak.
		limit = &agent.UsageLimitError{Framework: incompat.Framework, Message: incompat.Error()}
		log.Printf("level=warning condition=model_incompatibility framework=%q model=%q run=%q message=%q",
			incompat.Framework, incompat.Model, runID, incompat.Message)
	}
	// The other refusal that is the provider's doing and not this
	// run's, read here for the arm below the cancellations: a
	// provider with no capacity right now (agent.CapacityError).
	capacity, hitCapacity := agent.Capacity(runErr)
	// And the failure that is neither the provider's nor the task's but
	// the host's: a CLI the kernel would not start because the account's
	// host credentials have expired (authfix.Error).
	expired, credsExpired := authfix.As(runErr)

	switch {
	case runErr != nil && errors.Is(cause, errRunIdle):
		// A durable run its idle watchdog stopped (watchIdle): not
		// judged, suspended, and resumed on the next cycle.
		outcome = model.SuspendedOutcome
		detail = idleDetail(cfg.durableIdleTimeout()) + partialWorkSuffix(result)
		return runEnding{outcome: outcome, detail: detail, runErr: fmt.Errorf("orchestrator: run %s: %w", runID, errRunIdle),
			suspend: model.SuspendIdle}
	case hitLimit || hitIncompatibility:
		// This run is the one that found the wall. Pausing is what
		// stops the rest of the queue walking into it -- see Pause --
		// and cancels whatever else is in flight on the same
		// credential right now.
		//
		// Recorded as model.PausedOutcome rather than "failed": the
		// agent did not fail, the deployment ran out of budget, and
		// nothing about this task earned a place in its own failure
		// streak for it (that constant's own doc comment).
		held := cfg.Pause.BeginWindow(cfg.now(), scope, limit)
		until := held.Until
		if hitIncompatibility {
			detail = incompat.Error() + "; dispatch paused until " + until.UTC().Format(time.RFC3339) + partialWorkSuffix(result)
		} else {
			detail = usageLimitDetail(limit, pauseHeldScope(scope, held), until) + pauseReachNote(held) + partialWorkSuffix(result)
		}
		outcome = model.PausedOutcome
		runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
	case runErr != nil && errors.Is(cause, errUsageLimit):
		// Somebody else's limit, cancelled mid-run. Same outcome for
		// the same reason, and the detail says whose doing it was so
		// that a human reading this attempt is not left looking for a
		// fault in a run that had none. "The same credential" because
		// that is the only limit that can reach this run: a pause
		// covers the runs spending the credential that met it (Pause).
		outcome = model.PausedOutcome
		detail = "another run on the same credential (" + scope.describe() + ") reached its usage limit; " +
			"this one was cancelled until that window resets" +
			partialWorkSuffix(result)
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, errUsageLimit)
	case runErr != nil && errors.Is(cause, errTaskClosed):
		outcome = "cancelled"
		// Both cancellations record "cancelled", and the sentence
		// beside it is the only thing that says which -- so both
		// sentences are model's (RunEnding), which is what reads
		// them back.
		detail = model.TaskClosedDetail
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, errTaskClosed)
	case runErr != nil && errors.Is(cause, ErrTurnStopped):
		outcome = "cancelled"
		detail = model.TurnStoppedDetail
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, ErrTurnStopped)
	case runErr != nil && errors.Is(cause, errRunTimedOut):
		outcome = "cancelled"
		// The limit as this run actually had it: the budget plus
		// whatever wait credit it earned. A run cancelled at 2h20m
		// recorded as having exceeded "2h" would read to a human as
		// twenty minutes grain failed to enforce.
		detail = model.RuntimeCapDetail(cfg.maxRunRuntime()+credited) + partialWorkSuffix(result)
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, errRunTimedOut)
		if resumeOnDeadline(ctx, store, task) {
			// A task that asked for it is resumed instead, a bounded number
			// of times in a row (model.MaxDeadlineResumes).
			return runEnding{outcome: model.SuspendedOutcome, detail: detail, runErr: runErr,
				suspend: model.SuspendDeadline}
		}
	case errors.Is(cause, ErrOperatorPaused):
		// An operator paused the task (TaskPause, reconcilePauses):
		// interrupted, as a pre-emption is -- a person's decision about
		// the deployment's work earns the task no strike, and its
		// conversation is kept for the run that follows a resume.
		outcome = model.InterruptedOutcome
		detail = model.OperatorPausedDetail + partialWorkSuffix(result)
		if runErr != nil {
			runErr = fmt.Errorf("orchestrator: run %s: %w", runID, ErrOperatorPaused)
		}
	case runErr != nil && errors.Is(cause, errPreempted):
		// Stopped to make room for a base-branch repair (preempter):
		// interrupted, as a daemon restart is, and for the same reason
		// -- nothing about this task earned a strike. preempter has
		// already said so on the task, with the repair named.
		outcome = model.InterruptedOutcome
		detail = PreemptedDetail + partialWorkSuffix(result)
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, errPreempted)
	case runErr != nil && errors.Is(cause, errCapabilityChanged):
		// Restarted to pick up a grant change (reconcileCapabilityChanges):
		// the task's doing only in that a person changed what it holds,
		// so a suspension, and the next run's sandbox is built for the
		// grants as they are now.
		return runEnding{outcome: model.SuspendedOutcome, suspend: model.SuspendCapability,
			detail: CapabilityChangedDetail + partialWorkSuffix(result),
			runErr: fmt.Errorf("orchestrator: run %s: %w", runID, errCapabilityChanged)}
	case runErr != nil && errors.Is(cause, errDaemonStopped):
		// The daemon itself is shutting down (an upgrade or a
		// restart). Not a failure of the task or its agent, so it is
		// recorded as interrupted: no strike against the streak, no
		// backoff, and the conversation it was in is already on
		// record for the next dispatch to resume (earlySession). The
		// note on the task says why it went back to the queue.
		outcome = model.InterruptedOutcome
		detail = model.InterruptedDetail + partialWorkSuffix(result)
		interruptedRuns.Add(1)
		noteInterrupted(context.WithoutCancel(ctx), store, task.ID, runID, cfg.now())
		runErr = fmt.Errorf("orchestrator: run %s: %w", runID, errDaemonStopped)
	case runErr != nil && errors.Is(runErr, agent.ErrHostdDraining):
		// grain hostd refused to start the CLI because it is draining
		// for its own upgrade: nothing ran, and nothing about the task
		// earned a strike. Interrupted, as a daemon restart is, so the
		// task is queued again and its next attempt starts on the hostd
		// that replaces this one.
		outcome = model.InterruptedOutcome
		detail = runErr.Error()
		runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
	case hitCapacity:
		// The provider had no capacity for this run: overloaded,
		// unavailable, a gateway that gave up. Recorded as
		// model.CapacityOutcome rather than "failed" for the reason
		// that constant gives -- nothing about this task earned a
		// place in its own failure streak for a provider's bad
		// minute, and five such minutes would otherwise retire it.
		//
		// Below the three cancellations above rather than beside
		// hitLimit at the top: a run the deployment itself stopped
		// is described by what stopped it, whatever the CLI managed
		// to say on its way out. Nothing is paused here, and that is
		// the whole difference from hitLimit -- an overload is
		// usually seconds long, and stopping the deployment (and
		// cancelling other frameworks' live runs) would cost far
		// more than the wait it saves. dispatch's own backoff
		// carries the retry.
		outcome = model.CapacityOutcome
		detail = capacityDetail(capacity) + partialWorkSuffix(result)
		runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
	case runErr != nil && agent.ContextExhausted(runErr):
		// The conversation no longer fits the model's window: nothing
		// about the task, and nothing another try in the same session
		// would fix. A `context` suspension, resumed in a fresh session
		// from the conversation and the handover the run is given one
		// last turn to write (lastTurnForHandover).
		return runEnding{outcome: model.SuspendedOutcome, suspend: model.SuspendContext,
			detail: ContextExhaustedDetail + partialWorkSuffix(result),
			runErr: fmt.Errorf("orchestrator: running %s: %w", runID, runErr)}
	case credsExpired:
		// Ahead of the ordinary failure below, which would count it
		// against this task's streak and keep its words as a refusal
		// grain could not name -- and, three times in five minutes,
		// pause the framework as a refusal burst. None of that fits:
		// every run on this host fails this way until somebody renews
		// its credentials, whatever the task. Recorded as model.EnvironmentOutcome,
		// which no streak counts; RunDispatch parks the task on the same
		// sentence (stopOnExpiredHostCredentials).
		outcome = model.EnvironmentOutcome
		detail = expired.Error() + partialWorkSuffix(result)
		runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
	case runErr != nil:
		// runErr's own text, not the wrapped form below: that form
		// repeats runID, which is already this row's own id, and
		// buries the one thing worth a human's own read (gemini's
		// "exceeded max turns (20) without a final answer", a tool
		// framework's own connection error) under two layers of
		// "orchestrator: running ...: ".
		//
		// A framework may hand back what the run managed to do before
		// it broke (see agent.Framework), and that half answers the
		// question the error itself raises. "exceeded max turns (100)
		// without a final answer" says only that the budget ran out;
		// followed by the tools it spent that budget on, it says
		// whether the run was working or spinning.
		//
		// This is also the arm where a provider refusal grain has
		// stopped recognising ends up, and the only place that can
		// be noticed from. Every marker the two arms above match on
		// is a phrase a provider writes and may reword, and a
		// reworded refusal arrives here silently: read as an
		// ordinary failure, counted against the task's streak, and
		// contradicted by no test, because a test can only assert
		// the wording somebody wrote down. So what the run said is
		// kept -- bounded, since it is model output
		// (agent.UnmatchedFailureTail) -- for the report that reads
		// a window of them at once (Store.UnmatchedFailures, GET
		// /api/unmatched-failures, `grain failures`). Written after
		// FinishRun below, with the rest of what this run leaves
		// behind.
		unmatched = agent.UnmatchedFailureTail(runErr.Error())
		// The safety net for exactly that rewording: the third run
		// on this framework in five minutes to fail this fast, this
		// early and in these same words is read as the provider
		// refusing every run, and the framework is paused as if it
		// had said so (refusalburst.go). Its words stay in the
		// unmatched log above -- that is where a marker for them
		// comes from.
		if isFastFailure(result, cfg.now().Sub(agentStartedAt)) {
			framework := frameworkFor(task, cfg)
			if burst := cfg.Pause.observeFastFailure(cfg.now(), scope, runID, unmatched); burst != nil {
				limit := refusalBurstLimit(framework, unmatched, len(burst))
				until := cfg.Pause.Begin(cfg.now(), scope, limit)
				log.Printf("level=error condition=unrecognised_refusal_burst framework=%q runs=%q paused_until=%s message=%q "+
					"-- %s failed the same way %d times in a row; this is almost certainly a provider refusal grain has no marker for",
					framework, burst, until.UTC().Format(time.RFC3339), unmatched, framework, len(burst))
				outcome = model.PausedOutcome
				detail = usageLimitDetail(limit, scope, until) + partialWorkSuffix(result)
				burstRuns = burst[:len(burst)-1]
				burstDetail = usageLimitDetail(limit, scope, until)
				runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
				break
			}
		}
		// And said on the run itself, where somebody looking at one
		// failed task rather than at a week of logs is: a hedge, not
		// a verdict, since ResemblesRefusal is the loose read by
		// design and nothing branches on it.
		detail = runErr.Error() + partialWorkSuffix(result) + suspectedRefusalSuffix(unmatched)
		runErr = fmt.Errorf("orchestrator: running %s: %w", runID, runErr)
	default:
		task.KeepRunning = taskKeepRunning(ctx, store, task)
		outcome, detail = outcomeFor(task, result)
	}
	// A run that stopped by itself during a graceful pause's grace period
	// -- the agent did as it was asked, pushed, wrote its handover and
	// stopped -- is not a failure either, whatever it left behind. A
	// success stands: whatever it delivered is real work. So does an
	// ending already classified as earning no strike (a usage limit, a
	// capacity or environment fault, an OOM kill): what it says, and what
	// reads it back -- the OOM retry, the limit's own detail -- is still
	// true of the run, and the pause has nothing to add.
	if outcome != "succeeded" && !noStrikeOutcome(outcome) && taskPaused(ctx, store, task.ID) {
		outcome = model.InterruptedOutcome
		detail = model.OperatorPausedDetail + partialWorkSuffix(result)
	}
	return runEnding{outcome: outcome, detail: detail, runErr: runErr,
		unmatched: unmatched, burstRuns: burstRuns, burstDetail: burstDetail}
}

// recordRunEnd writes what a finished agent run leaves in the store: its
// outcome (FinishRun), the burst runs it re-records, its transcript, its
// census, the session it leaves suspended and what it said when nothing
// could name its failure. It returns FinishRun's (or the transcript
// write's) error; every other write is logged rather than surfaced, for
// the reasons given beside each.
//
// It is RunDispatch's, split out for ResumeAdoptedRun, which finishes a
// run another daemon started the same way. agentRan, resumableSession and
// recordedEarly are RunDispatch's of the same names (recordedEarly being
// what earlySession wrote before the run returned).
func recordRunEnd(ctx, finishCtx context.Context, store *model.Store, cfg Config, task model.Task, runID string,
	obs *model.Observation, result *agent.Result, end runEnding, waitCredit *model.RunWaitCredit,
	agentRan bool, resumableSession, recordedEarly string) error {
	outcome, detail := end.outcome, end.detail
	burstRuns, burstDetail, unmatched := end.burstRuns, end.burstDetail, end.unmatched
	var finishErr error
	kind, _ := end.suspensionKind()
	failover := kind.IsFailover() && end.modelHint != ""
	if end.suspend != "" || failover {
		susp := model.Suspension{RunID: runID, Kind: kind, Detail: detail, CreatedAt: cfg.now()}
		if failover {
			susp.ModelHint = end.modelHint
		}
		finishErr = store.Suspend(finishCtx, susp)
	} else {
		finishErr = store.FinishRun(finishCtx, runID, cfg.now(), outcome, detail)
	}
	if finishErr == nil && failover {
		noteFailedOverModels(finishCtx, store, task, cfg.now())
	}
	// The earlier runs of the burst this one completed were recorded as
	// failed, because one of them alone was indistinguishable from an
	// ordinary failure. Now that the burst says otherwise, they are
	// recorded as what they were, which takes them back out of their
	// tasks' failure streaks (Store.SetRunOutcome). Logged rather than
	// surfaced: this run's own outcome is already written.
	for _, runID := range burstRuns {
		if err := store.SetRunOutcome(finishCtx, runID, model.PausedOutcome, burstDetail); err != nil {
			log.Printf("orchestrator: run %s: re-recording it as paused by a refusal burst: %v", runID, err)
		}
	}
	if finishErr == nil && result != nil && result.Transcript != "" {
		// A separate write, after FinishRun's own -- see
		// Store.SetRunTranscript's own doc comment on why (bwsalmon/
		// agents#446). Skipped once finishErr is already non-nil: a run
		// whose own outcome failed to record is not worth a second write
		// attempt on top of it.
		//
		// Redacted first, of the values this run's sandbox carried from
		// its operator's environment and of every recognizable token
		// shape: run_command already cut them from what it returned, but
		// the transcript also holds what the agent wrote itself and what
		// every other tool returned, and this is the copy that outlives
		// the run (GSA1-12).
		transcript, _ := mcp.SandboxEnvRedactor(cfg.sandboxEnv).Text(result.Transcript)
		finishErr = store.SetRunTranscript(finishCtx, runID, transcript)
	}
	// The run's own census, from the one place it exists: agent.Result,
	// which is about to be discarded (census.go). A separate write again,
	// after the outcome this run is judged on, and logged rather than
	// surfaced on failure -- a measurement that cannot be taken is not a
	// reason to fail the run being measured, the same rule
	// Store.SetRunAgentStarted's own doc comment states.
	telemetry := runTelemetryAt(result, cfg.now())
	telemetry.WaitCredit = waitCredit
	if !telemetry.Empty() {
		if err := store.RecordRunTelemetry(finishCtx, runID, telemetry); err != nil {
			log.Printf("orchestrator: run %s: recording what it did with its tools: %v", runID, err)
		}
	}
	// Tool calls the cluster's exec tunnel failed after this run reached
	// its agent: recorded so the deployment-wide alert sees a tunnel
	// that fails only mid-run, which no setup failure would show
	// (model.ExecTransportFailure). Logged rather than surfaced, as the
	// census above.
	if n := execTransportFailures(result); n > 0 {
		at := cfg.now()
		if err := store.RecordExecTransportFailure(finishCtx, model.ExecTransportFailure{
			RunID: runID, TaskID: task.ID, At: at, Calls: n,
		}); err != nil {
			log.Printf("orchestrator: run %s: recording its exec-tunnel failures: %v", runID, err)
		} else {
			noteExecTunnelRate(finishCtx, store, at)
		}
	}
	// The conversation this run leaves parked for the task's next one,
	// or the clearing of whatever an earlier run parked
	// (model.SuspendedSession). Written after the outcome it is read
	// against, since the ending is half of what decides it, and logged
	// rather than surfaced on failure for the reason the census above
	// gives: an instrument must not be able to fail the work it
	// observes. Only for a run that actually reached its agent -- see
	// agentRan.
	//
	// The record is always this run's own answer rather than a merge
	// with the last one's: a task's suspended session is the
	// conversation its most recent run ended in, and a token an earlier
	// run left is one this run's framework has since declined to name.
	// A run the task was closed under is the one ending that parks
	// nothing at all -- there is no next run to resume it.
	//
	// The write is skipped entirely where there is nothing to say and
	// nothing recorded, which is every run on a framework that keeps no
	// resumable session: this is an observation like any other, so a
	// write of it stamps the row and lands in the state repository's
	// diff, and a deployment that will never suspend anything should not
	// pay a commit per run to be told so again.
	if agentRan {
		// Whatever was recorded early stops here, so nothing lands on top
		// of the answer below. A framework that came back with no Result
		// at all said nothing to contradict what its stream named, so
		// that is still the conversation this run is in; one that came
		// back with a Result has had its say in ResumableSession, ""
		// included.
		if result == nil && resumableSession == "" {
			resumableSession = recordedEarly
		}
		// A suspension is not an ending: the next run joins this one's
		// conversation (docs/design/durable-tasks.md#DT-2, Continuity),
		// so a suspended run whose framework named no session of its own
		// leaves the one it was in -- what its stream named, or failing
		// that the one it was resumed from -- rather than clearing it.
		kind, suspended := end.suspensionKind()
		if kind == model.SuspendContext {
			// The session is what is full: the next run starts a fresh
			// one from the conversation and the handover (DT-2,
			// Continuity), so none is left for it to join.
			resumableSession, recordedEarly = "", ""
		} else if suspended && resumableSession == "" {
			resumableSession = recordedEarly
			if resumableSession == "" {
				resumableSession = model.ResumeTokenFor(obs, frameworkFor(task, cfg))
			}
		}
		var session *model.SuspendedSession
		if resumableSession != "" && model.EndingOf(outcome, detail) != model.EndingTaskClosed {
			session = &model.SuspendedSession{
				Framework: frameworkFor(task, cfg),
				Token:     resumableSession,
				RunID:     runID,
				At:        cfg.now(),
			}
		}
		if session != nil || (obs != nil && obs.SuspendedSession != nil) || recordedEarly != "" {
			if err := store.SetSuspendedSession(finishCtx, task.ID, session, cfg.now()); err != nil {
				log.Printf("orchestrator: run %s: recording the session it leaves suspended: %v", runID, err)
			}
		}
	}
	// What this run said when grain could not tell whether the provider
	// had refused it -- kept so that a marker which has stopped matching
	// can be found by reading a week of these rather than by watching
	// tasks retire (model.UnmatchedFailure). Another write of its own
	// after the outcome, and logged rather than surfaced on failure, for
	// the reason the census above gives: an instrument must not be able
	// to fail the work it observes.
	if unmatched != "" {
		if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
			RunID: runID, TaskID: task.ID, Framework: frameworkFor(task, cfg),
			At: cfg.now(), Text: unmatched, Suspect: agent.ResemblesRefusal(unmatched),
		}); err != nil {
			log.Printf("orchestrator: run %s: recording what it said when it failed: %v", runID, err)
		}
	}
	return finishErr
}

// resumeOnDeadline reports whether a run of task that ran out of wall
// clock is a deadline suspension: the task asked for it
// (model.Task.ResumeOnDeadline) and has not already been resumed
// model.MaxDeadlineResumes times in a row. A history that cannot be read
// is a failure, as the deadline was before the flag existed.
func resumeOnDeadline(ctx context.Context, store *model.Store, task model.Task) bool {
	if !task.ResumeOnDeadline {
		return false
	}
	runs, err := store.Runs(context.WithoutCancel(ctx), task.ID)
	if err != nil {
		log.Printf("orchestrator: task %s: reading its runs to count deadline resumes: %v", task.ID, err)
		return false
	}
	return model.DeadlineResumes(runs) < model.MaxDeadlineResumes
}

// ResumedUntil is the time the last of previous parked until, or zero
// when it did not end parked on a time.
func ResumedUntil(previous []model.Run) time.Time {
	if len(previous) == 0 {
		return time.Time{}
	}
	last := previous[len(previous)-1]
	if last.Outcome != model.ParkedOutcome {
		return time.Time{}
	}
	until, _ := model.ParkedUntil(last.Detail)
	return until
}

// ResumedFrom is the tasks the last of previous parked on, as they stand
// now, or nil when it did not end parked. Advisory, like BaseIncident: a
// target grain cannot read is left out rather than failing the dispatch.
func ResumedFrom(ctx context.Context, store *model.Store, previous []model.Run) []model.WaitTarget {
	if len(previous) == 0 {
		return nil
	}
	last := previous[len(previous)-1]
	if last.Outcome != model.ParkedOutcome {
		return nil
	}
	var out []model.WaitTarget
	for _, id := range model.ParkedTargets(last.Detail) {
		target, err := store.WaitTargetOf(ctx, id)
		if err != nil || target == nil {
			continue
		}
		out = append(out, *target)
	}
	return out
}

// dispatchChangeTargetRefusal is why task, which carries a change target,
// cannot be dispatched as it stands, nil when it can (or carries none).
func dispatchChangeTargetRefusal(task model.Task) error {
	if task.ChangeTarget == nil {
		return nil
	}
	checked := task
	checked.Grants = slices.Clone(task.Grants)
	if err := model.ValidateTaskChangeTarget(&checked); err != nil {
		return fmt.Errorf("orchestrator: task %s's %s target: %w", task.ID, task.ChangeTarget.Kind, err)
	}
	if len(checked.Grants) != len(task.Grants) {
		return fmt.Errorf("orchestrator: task %s's %s target needs a grant it does not hold (%s); grant it on the task and run it again",
			task.ID, task.ChangeTarget.Kind, model.CapabilityGrantRef(checked.Grants[len(checked.Grants)-1].Capability))
	}
	return nil
}
