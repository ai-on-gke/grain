package mcp

// base_branch_status is the control group for pull_request_status.
//
// The other two CI tools answer "what does CI say about the commit you
// pushed", which is only half of what a run staring at a red check
// actually needs. "FAILING go (failure)" says the build is broken; it
// does not say whether *this change* broke it. A suite that was already
// failing on main before the run started looks identical from inside the
// sandbox to one the run's own diff broke, and a run that cannot tell
// them apart has only bad moves available: it rewrites working code
// chasing a failure it did not cause, burns its remaining turns on it,
// and -- worst of the three -- eventually "fixes" the symptom by
// weakening the test that was telling the truth. The alternative, giving
// up and reporting "the build is red, possibly not mine", is a guess
// dressed as a finding.
//
// The fact that settles it is one read away and was simply not reachable
// from a sandbox: what CI says about the tip of the branch this work
// merges into. A check failing there is failing without this run's
// change in it. So this tool reports that branch the way
// pull_request_status reports the run's own -- same tally, same check
// lines, same failing-job logs, so the two answers can be read side by
// side and compared line for line rather than in two different
// vocabularies.
//
// It is a separate tool rather than a second half bolted onto
// pull_request_status for two reasons. It costs reads (a branch head, its
// checks, and the failing jobs' logs on top of that), and nearly every
// call of the status read is on a build that is green or still running,
// where there is nothing to compare and nothing to explain. And an answer
// that always carried both branches would be two sets of check lines
// under one heading, which is precisely the confusion this exists to
// prevent -- "FAILING go" twice, once about the run and once not.
// pull_request_status and wait_for_checks point here instead, in the one
// case where the comparison is worth making (baseComparisonPointer).
//
// The branch it reads is grain's, not the agent's:
// PullRequestScope.Base, or the repo's default branch when the task has
// no base of its own. There is no argument, exactly as there is none on
// pull_request_status, so this widens what a run can see by one branch --
// the one its own work is aimed at -- and not by an arbitrary ref.

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/github"
)

// baseComparisonPointer is what pull_request_status and wait_for_checks
// say when they have just named a failing check. It is on those reports
// rather than only in this tool's description because the moment a run
// needs the comparison is the moment it is looking at a failure, and a
// run that has never called this tool has no reason to remember it
// exists -- the same reason BuildPrompt names wait_for_checks in words
// rather than trusting the roster (orchestrator/run.go).
//
// It carries no formatting verb: both callers concatenate it into a
// format string.
const baseComparisonPointer = "If this may have been broken before you started, call " +
	"base_branch_status: it reports CI on the branch you merge into, which tells a " +
	"failure you caused from one you inherited."

func baseBranchStatusTool(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) Tool {
	// The middle sentence is the one a repair run needs the other way
	// round, for the reason PullRequestScope.Repair gives: what it is
	// for is telling breakage apart either way, but what to do about
	// inherited breakage is the opposite for a run holding up a merge
	// queue. Said in the description as well as in the answer so that a
	// repair deciding whether to call this at all is not put off by a
	// tool that advertises "report rather than fix".
	whose := "a check that is failing here is failing without your change in it, so it " +
		"is pre-existing breakage to report rather than to fix, while a check that " +
		"passes here and fails on your branch is yours"
	if scope.Repair {
		whose = "a check that is failing here is failing without your change in it, so it " +
			"is breakage you inherited -- but this run is the merge queue's repair of " +
			"your branch, so fix it here anyway rather than reporting it, since the " +
			"branch does not merge while it is red"
	}
	// Last, and overriding both, because it is not a third opinion about
	// whose failure a red check is -- it is the statement that there are
	// no check verdicts here to have an opinion about
	// (PullRequestScope.GitHubCIIgnored). Said in the description and not
	// only in the answer, because the cost this setting exists to remove
	// is paid before the answer arrives: a run that reads "call this when
	// a check fails and it will tell you whose failure it is" calls it,
	// and spends a turn being told there was nothing to compare.
	if scope.GitHubCIIgnored {
		whose = "this repository's gate is grain CI (`.grain/workflows`) and GitHub's checks " +
			"are not read here, so this tool has no comparison to make and calling it " +
			"will only say so -- grain CI runs against your branch and never against " +
			"the base, which means a grain CI failure is yours either way"
	}
	return Tool{
		Name: "base_branch_status",
		Description: "Show what CI says about the branch your work merges into -- " +
			"the repository's default branch, unless this task was given a " +
			"base of its own -- rather than about your branch. Use it when " +
			"a check fails on your branch and you need to know whose " +
			"failure it is: " + whose + ". It reports the same check " +
			"lines pull_request_status does, plus the end of each failing " +
			"job's own log, so the two answers can be compared directly. " +
			"Which branch this reads is fixed by grain, the same way your " +
			"own branch is; it only reads GitHub and changes nothing.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		ElisionAdvice: elisionAdviceCIReport,
		Handler: func(ctx context.Context, _ map[string]any) Result {
			if client == nil || !scope.complete() {
				return Result{
					Text: "There is no base branch to report on: this run has no GitHub " +
						"repository configured for it. Do the work in the sandbox and " +
						"report what you found instead.",
					IsError: true,
				}
			}
			text, err := baseBranchStatus(ctx, client, grainCI, scope)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: text}
		},
	}
}

// baseBranchStatus renders the whole answer: which branch is the base and
// why, where its tip is, and what CI made of that commit. ctx is for the
// wait a missing log gets, exactly as in pullRequestStatus.
func baseBranchStatus(ctx context.Context, client PullRequestReader, grainCI GrainCIReader,
	scope PullRequestScope) (string, error) {

	base, defaulted, err := resolveBase(client, scope)
	if err != nil {
		return "", err
	}

	// The whole of this tool is a comparison between GitHub's checks on
	// two branches, so a repository that does not read GitHub's checks
	// has no comparison for it to make -- and every read it would spend
	// making one is a read spent on an answer that decides nothing. It
	// says that, with the branch named so the run can still see which
	// branch it is aimed at, and stops (PullRequestScope.GitHubCIIgnored).
	//
	// Before the branch-head read rather than after: an unreachable base
	// is worth reporting when the base's checks are the answer, and is
	// one more thing to explain away when they are not.
	if scope.GitHubCIIgnored {
		return fmt.Sprintf(
			"%s is %s, and your branch %s merges into it.\n\n%s\n\nSo there is no comparison "+
				"for this tool to make: it exists to tell a failure you caused from one you "+
				"inherited by setting GitHub's checks on the two branches side by side, and "+
				"neither side is read here. What judges %s is grain CI, which runs against "+
				"task branches and never against %s -- so a grain CI failure is yours to fix "+
				"whether or not %s would have had it too. Call wait_for_checks for that "+
				"verdict; pull_request_status reports it.",
			base, baseProvenance(defaulted, scope), scope.Branch,
			gateIsGrainCINotice(grainCI != nil), scope.Branch, base, base), nil
	}

	head, err := client.GetBranchHead(scope.Owner, scope.Repo, base)
	if err != nil {
		return "", fmt.Errorf("reading %s/%s's %s branch: %w", scope.Owner, scope.Repo, base, err)
	}
	if head == nil {
		// A base that does not exist is a real state and not this tool's
		// to repair: the finish path retargets a vanished base onto the
		// default branch when it opens the pull request
		// (orchestrator.pullRequestBase), and prepareCheckout refuses to
		// start a run whose base is gone. Saying so plainly beats an
		// empty check list, which would read as "the base is fine".
		return fmt.Sprintf(
			"Branch %s does not exist on %s/%s, so there is nothing to compare your branch's "+
				"checks against. That is about this task's base, not your change -- say so "+
				"in what you report.",
			base, scope.Owner, scope.Repo), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s is %s, and your branch %s merges into it.\n",
		base, baseProvenance(defaulted, scope), scope.Branch)
	fmt.Fprintf(&b, "Its tip is %s (%s).\n", shortSHA(head.SHA), firstLine(head.Message))

	checks, err := checkRunsForCommit(client, scope, head.SHA)
	if err != nil {
		return "", err
	}
	b.WriteString("\n")
	b.WriteString(renderBaseChecks(checks, base, shortSHA(head.SHA), scope.Branch, scope.Repair))
	// The one thing this comparison cannot cover, said before the logs
	// for the reason pull_request_status says its own there
	// (grainCIBaseCaveat). No read is made for it: what it says is true
	// of every grain CI verdict there could be, so the reader's presence
	// -- this run's branch is judged by grain CI at all -- is the whole
	// of the condition.
	if grainCI != nil {
		b.WriteString(grainCIBaseCaveat(base, scope.Branch))
	}
	// The logs, on the same terms pull_request_status reads them: only
	// once something has failed (failing_job_logs.go). Here they earn
	// their three reads twice over -- the strongest evidence that a
	// failure is not this run's is the base failing in the same job with
	// the same output.
	if anyFailed(checks) {
		b.WriteString(failingJobLogs(ctx, client, scope, head.SHA, checks))
	}
	return b.String(), nil
}

// resolveBase answers which branch the run's work merges into, and
// whether that answer came from the repo rather than from the task.
//
// An empty scope.Base is not a missing configuration -- it is what every
// ordinary task has -- so it is resolved here the way the finish path
// resolves it when it opens the pull request: ask the repo for its
// default branch. Doing it at call time rather than at process start
// keeps a deployment whose credential cannot read the repo from losing
// the other two CI tools over a branch name only this one needs.
func resolveBase(client PullRequestReader, scope PullRequestScope) (base string, defaulted bool, err error) {
	if scope.Base != "" {
		return scope.Base, false, nil
	}
	def, err := client.DefaultBranch(scope.Owner, scope.Repo)
	if err != nil {
		return "", false, fmt.Errorf(
			"reading %s/%s's default branch, which is the base this task has: %w",
			scope.Owner, scope.Repo, err)
	}
	if def == "" {
		return "", false, fmt.Errorf(
			"%s/%s reports no default branch, so there is no base branch to read CI for",
			scope.Owner, scope.Repo)
	}
	return def, true, nil
}

// baseProvenance says where the branch name came from, because the two
// cases mean different things to a run reading the answer: the default
// branch is the repo's own trunk, while a base the task carries is
// usually another change this one is stacked on, and a failure there is
// worth reporting to a different person.
func baseProvenance(defaulted bool, scope PullRequestScope) string {
	if defaulted {
		return fmt.Sprintf("%s/%s's default branch", scope.Owner, scope.Repo)
	}
	return "the base branch this task was given"
}

// renderBaseChecks lists the base's checks and says what they mean for
// the run reading them -- which is a different sentence in each case, and
// the only reason this is not renderChecks: every closing line there
// tells the run to go and fix something, and here the useful conclusion
// is often the opposite.
//
// Often, but not for a repair (repair, PullRequestScope.Repair): a run
// the merge queue sent to unbreak the branch it is holding has to fix an
// inherited failure too, so that case gets the opposite conclusion back
// again. Only the failing arm differs -- a green or unfinished base says
// the same thing to both kinds of run.
func renderBaseChecks(checks []github.CheckRun, base, sha, branch string, repair bool) string {
	if len(checks) == 0 {
		return fmt.Sprintf(
			"GitHub reports no checks at all against %s, the tip of %s -- most likely this "+
				"repo runs CI on pull requests rather than on pushes to %s. There is no "+
				"comparison to make from here, so judge a failure on %s on its own: nobody "+
				"looked at %s.",
			sha, base, base, branch, base)
	}

	tally := tallyChecks(checks)
	var b strings.Builder
	fmt.Fprintf(&b, "Checks against %s, the tip of %s:\n", sha, base)
	b.WriteString(tally.lines)
	fmt.Fprintf(&b, "\n%s\n", tally.counts())

	switch {
	case tally.failing > 0 && repair:
		// The one answer a repair needs turned around. Everything above
		// this line is the same report; what differs is the conclusion,
		// because "leave it alone" spends a repair run's whole purpose --
		// see PullRequestScope.Repair.
		fmt.Fprintf(&b, "Those failures are on %s itself, without your change in them -- breakage "+
			"you inherited. Fix it here anyway: this run is the merge queue's repair of %s, "+
			"which does not merge while any check on it is red, whoever turned it red. Fix "+
			"the code rather than weakening the test, and say in what you report that it "+
			"was already failing on %s and what you did. A check failing on %s that is not "+
			"listed above is yours in the ordinary way.\n",
			base, branch, base, branch)
	case tally.failing > 0:
		fmt.Fprintf(&b, "Those failures are on %s itself, without your change in them -- breakage "+
			"you inherited, so leave it alone, do not weaken the test, and say in what you "+
			"report that it was already failing on %s. A check failing on %s that is not "+
			"listed above is still yours to fix.\n",
			base, base, branch)
	case tally.pending > 0:
		fmt.Fprintf(&b, "Nothing has failed on %s, but %d check(s) there have not finished, and "+
			"an unfinished check has not passed. Treat a failure on %s with no counterpart "+
			"here as yours until one of those says otherwise.\n",
			base, tally.pending, branch)
		if tally.cancelled > 0 {
			fmt.Fprintf(&b, "The %d cancelled check(s) above never reported and never will: only "+
				"a re-run of %s would say what they would have.\n", tally.cancelled, base)
		}
	case tally.cancelled > 0:
		// The same missing verdict cancelledHold describes on the run's
		// own branch, and a different consequence: nobody is merging this
		// branch, so there is nothing here for the run to unblock and
		// nothing for it to push. What it changes is the comparison,
		// which is all this tool is for -- a check cancelled on the base
		// is one the base was never judged on, so it can neither
		// exonerate the run's change nor convict it.
		fmt.Fprintf(&b, "Nothing has failed on %s, but %d check(s) there were cancelled before "+
			"they reported, so %s has no verdict from them at all and only a re-run would "+
			"get one. If one of those is what is failing on %s, this comparison cannot say "+
			"whose failure it is: treat it as yours, and report that the base never ran it. "+
			"Every other check here passed, so any other failure on %s is yours.\n",
			base, tally.cancelled, base, branch, branch)
	default:
		fmt.Fprintf(&b, "Every check against %s finished and none failed, so anything failing on "+
			"%s is a failure your own change introduced. Fix it rather than reporting it as "+
			"pre-existing.\n", base, branch)
	}

	// The caveat that makes the comparison honest. The base moves while a
	// run works, so this is the base as it is now rather than the commit
	// the branch was cut from, and the two can disagree in both
	// directions.
	fmt.Fprintf(&b, "\nThis is %s as it stands now, which may be ahead of the commit you branched "+
		"from: a failure that landed on %s since then shows up here and not in your "+
		"checkout, and one fixed there can still be failing on your branch until you merge "+
		"%s in.", base, base, base)
	return strings.TrimRight(b.String(), "\n")
}
