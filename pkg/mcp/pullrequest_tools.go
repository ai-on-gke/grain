package mcp

// pull_request_status is the one tool that lets a run see what GitHub
// thinks of the work it has pushed: the pull request open for its branch,
// if there is one yet, and every CI check reported against the commit at
// the branch's tip.
//
// It exists because a dispatched run had no way to close the loop on its
// own tests. An agent could commit and `git push origin <branch>` from the
// first turn -- the git proxy authorizes a push to the task's own target
// (gitproxy/authorize.go) and ConfigureGitCredentials leaves a usable
// identity and credential helper behind -- but what CI made of that push
// was invisible until long after the run had finished, when
// orchestrator.SyncPullRequests read the checks and the merge queue sent
// the task back for a whole further attempt over them
// (orchestrator/sync.go's requeueForRepair). Repairing a red build
// therefore always cost a second
// dispatch, starting from a cold sandbox, to fix something the run that
// caused it was still sitting there able to fix. With this, the run can
// push, look, repair and push again inside its own turn budget.
//
// It does not reopen docs/design.md's split surface ("Sandboxes: git
// transport only. No REST, no GraphQL"). This tool is served by the
// `grain mcpserver` process, which runs on the controller alongside the
// daemon and reads GitHub with the controller's own credential -- the
// identical shape the ask_question escape hatch already has, and the same
// reason that one was acceptable: what crosses into the sandbox is a
// rendered answer, never a credential and never a general-purpose API
// call. The sandbox gains no way to reach GitHub it did not have before.
//
// The scope is fixed at process start (PullRequestScope, written by
// pkg/cli/mcpserver.go's flags from the run's own task) and no argument
// can move it, mirroring the "only place it's named" discipline
// agent/claude's own doc comment describes for the sandbox root: a run
// reads CI for its own branch, and for the branch that branch merges
// into (base_branch_status_tool.go), or nothing.

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
)

// PullRequestScope is the one branch pull_request_status reports on --
// this run's own target repo and the branch model.BranchName gave it --
// plus the one other branch base_branch_status may look at, which is the
// branch that work merges into and nothing else.
//
// Owner, Repo and Branch are all required; an incomplete scope is a task
// with no repo attached, which the tools say plainly rather than
// guessing.
type PullRequestScope struct {
	Owner  string
	Repo   string
	Branch string
	// Base is the branch Branch merges into: model.Task's own Base,
	// passed down from orchestrator.RunDispatch through the Framework's
	// forked "mcpserver -pr-base". Empty is not missing -- it is what a
	// task with no base of its own has, and means the repo's default
	// branch, which base_branch_status resolves through
	// PullRequestReader.DefaultBranch exactly as the finish path does
	// (orchestrator.pullRequestBase). Either way the branch is grain's to name
	// and not the agent's, the same rule Branch above is under.
	Base string
	// Repair marks this run as the merge queue's repair of Branch --
	// grain's own reading of the task it dispatched
	// (orchestrator.mergeRepairRun), passed down the same way Base is
	// (the forked "mcpserver -pr-repair").
	//
	// It changes one answer: base_branch_status tells an ordinary run
	// that a check failing on the base too is breakage to report rather
	// than to fix, which is the right advice for work that has to be
	// judged on its own diff and the wrong advice for a run whose whole
	// job is to get this branch merging again. A repair is told to fix
	// it either way -- see renderBaseChecks, and the prompt paragraph
	// this agrees with (orchestrator.repairCISection), which a run
	// reading both must not find in disagreement at the moment it is
	// deciding what to do about a red check.
	//
	// False -- every ordinary run, and every caller that has no view of
	// a task at all -- leaves those answers exactly as they read before
	// this existed.
	Repair bool
	// GitHubCIIgnored says this repository has declared that GitHub's
	// check runs are not a source of verdicts for it: grain CI
	// (.grain/workflows) is its gate, the merge queue neither reads nor
	// waits on GitHub's checks here, and no clock runs on them
	// (model.RepoConfig.IgnoreGitHubCI, carried down as this run's
	// "mcpserver -pr-ignore-github-ci").
	//
	// It changes what both CI reports do rather than only what they say.
	// Neither tool reads GitHub's checks when it is set, because a list
	// of a dozen reds nobody is reading is not a shorter answer than a
	// sentence saying so -- it is a longer one, and every run that got
	// it spent turns and a paragraph of its closing note explaining the
	// reds away to a person who already knew (grain/task-270's own
	// record of that). base_branch_status has a second reason: the
	// comparison it exists to make is between two sets of GitHub checks,
	// and with neither side read there is nothing to compare, so it says
	// what actually judges the branch instead of answering "all red on
	// both sides" every time.
	//
	// False is every repository that has not declared it, and leaves
	// both answers exactly as they read before. Grain's own fact, under
	// the same rule as everything above.
	GitHubCIIgnored bool
	// BaseIncident is the notice about an active base branch incident, if any.
	BaseIncident string
	// PullRequestNumber, when positive, pins an external pull request
	// ("mcpserver -pr-number", derived from model.LinkExternalPR) that
	// this run is reviewing or assisting on when its own Branch has no
	// open pull request yet. Daemon-pinned like Owner, Repo and Branch;
	// no tool argument can set or move it.
	PullRequestNumber int
}

func (s PullRequestScope) complete() bool {
	return s.Owner != "" && s.Repo != "" && (s.Branch != "" || s.PullRequestNumber > 0)
}

// PullRequestReader is the read-only slice of github.Client this tool
// needs. Narrowed to a handful of methods rather than taking the whole interface
// so that what an agent can reach through here is legible at a glance:
// there is no mutation in this set at all, and nothing that names a repo
// the caller did not already pin in PullRequestScope.
type PullRequestReader interface {
	GetBranchHead(owner, repo, branch string) (*github.BranchHead, error)
	// DefaultBranch is how base_branch_status resolves an empty
	// PullRequestScope.Base into a branch name. It reads a property of
	// the repo the caller already pinned, and takes no branch of its
	// own: there is no argument here an agent could aim anywhere.
	DefaultBranch(owner, repo string) (string, error)
	FindOpenPullRequestForBranch(owner, repo, branch string) (*github.PullRequest, error)
	GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error)
	ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error)
	ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error)
	// FailedJobLogs is what turns a named failing check into something a
	// run can act on without leaving its sandbox -- see
	// failing_job_logs.go. Read-only like the rest, and scoped to a
	// commit the caller already had to name.
	FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error)
	// ActionsState is how a report with no log to show says why there is
	// none instead of guessing -- see failing_job_logs.go. Read only
	// after a failure with no readable log, and scoped to the same
	// commit FailedJobLogs was asked about.
	ActionsState(owner, repo, headSHA string) (github.ActionsState, error)
	// ListReviewComments is the inline review comments on a pull request
	// the caller already found for its own branch -- what reviewers said
	// about the diff, which pull_request_status shows under the pull
	// request (pullrequest_review_tools.go).
	ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error)
}

// NewPullRequestTools returns the tools a run gets for watching its own
// pull request: pull_request_status, which answers what CI says now,
// wait_for_checks, which blocks until CI has an actual verdict
// (wait_for_checks_tool.go), and base_branch_status, which answers the
// question the first two cannot -- whether a failing check was already
// failing on the branch this work merges into
// (base_branch_status_tool.go). The three are one vocabulary -- a run
// that has the first and not the second has no way to wait except by
// spending turns on it, and a run without the third has no way to tell
// its own breakage from the base's -- so they are registered together
// rather than left to each call site to remember, which is also how
// agent/claude's and agent/antigravity's allowedTools learn the new name
// without being edited.
//
// grainCI is the fourth thing these answers are composed from, and the
// one that is not GitHub's: the verdict grain's own CI recorded on this
// run's task, which has held the merge queue since grain/task-302 and
// which none of GitHub's check lists can show. nil -- a task that does
// not hold the grain-ci capability, a caller with no daemon to ask --
// leaves every report exactly as it read before, and grain_ci.go is
// where the whole of that is written down.
//
// client may be nil and scope may be incomplete -- a deployment that
// configured no GitHub access, or a task with no repo attached. Every
// tool is registered either way, so tools/list does not change shape
// with a deployment's configuration and an agent that calls one gets a
// sentence explaining why there is nothing to report instead of "unknown
// tool pull_request_status", which reads like a bug in grain rather than
// a fact about this run.
func NewPullRequestTools(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) []Tool {
	// Every GitHub refusal these tools report reads as GitHub's own
	// sentence rather than as the JSON it arrived in -- see
	// readable_github.go, which explains why that is worth more here
	// than anywhere else in this repository.
	client = readableErrors(client)
	// The only writes in this set: answering and resolving review
	// comments on the run's own pull request, registered whether or not
	// the client can make them, for the same reason the reads are
	// (pullrequest_review_tools.go).
	commenter := commenterOf(client)
	tools := []Tool{
		pullRequestStatusTool(client, grainCI, scope),
		baseBranchStatusTool(client, grainCI, scope),
		replyToPullRequestCommentTool(client, commenter, scope),
		resolveReviewThreadTool(client, commenter, scope),
	}
	return append(tools, NewWaitForChecksTools(client, grainCI, scope)...)
}

// gateIsGrainCINotice is what both CI reports print where a check list
// would go, for a repository that has declared GitHub's check runs are
// not a source of verdicts for it (PullRequestScope.GitHubCIIgnored).
//
// It is one sentence of fact and one of consequence, and it is
// deliberately not hedged. A run that is told "GitHub's checks are red
// but probably do not matter" will still spend turns on them -- that is
// what every run on this deployment did while GitHub Actions was blocked
// at the account level and grain went on reporting the reds anyway. The
// honest thing to say is the thing that is true: nobody read them,
// nothing is waiting on them, and here is what does decide.
//
// judged is whether grain CI actually judges this run -- a task holding
// the grain-ci capability, which is the reader pull_request_status was
// built with. False is the case the repository setting has to answer for
// out loud rather than quietly: the gate is grain CI and this run has no
// grain CI, so nothing will report a verdict for its commit and the
// merge queue will wait until it hands the pull request to a person.
// Saying that is the whole reason turning the setting on without grain
// CI is allowed to be a visible dead end rather than a silent green.
func gateIsGrainCINotice(judged bool) string {
	notice := "This repository's gate is grain CI (`.grain/workflows`); GitHub's checks are " +
		"not read here. Nothing above was fetched from them, the merge queue does not " +
		"count them and does not wait on them, and a red one on this commit neither " +
		"holds this pull request nor is yours to fix."
	if judged {
		return notice + " What decides this branch is below."
	}
	return notice + " Nor does anything else: this run does not hold the grain-ci " +
		"capability, so no grain CI verdict will be recorded for the commit you push, " +
		"and the merge queue will wait for one until it gives up and asks a person. " +
		"That is a fact about how this task was filed, not about your change -- say so " +
		"in what you report."
}

// elisionAdviceCIReport is what the three GitHub CI reports say if the
// backstop ever has to cut one -- a commit with more failing checks than
// a screenful, say. None of them takes an argument that would return
// less, so it says the one honest thing: what is kept is the two ends,
// and the way to the rest is not another call.
const elisionAdviceCIReport = "no argument narrows this answer -- its head is the " +
	"branch and its check list, its tail is the end of the last failing job's log. " +
	"Reproduce the failure in your checkout rather than calling this again for the middle"

func pullRequestStatusTool(client PullRequestReader, grainCI GrainCIReader, scope PullRequestScope) Tool {
	// Named in the description as well as in the answer, and only for a
	// run grain CI actually judges: a run deciding whether this tool
	// tells it the whole story about its pull request is deciding it
	// from the roster, before it has ever seen an answer -- and for
	// these runs the answer is a second CI system it has heard nothing
	// about (grain_ci.go).
	// A repository whose gate is grain CI is a different tool from the
	// roster's point of view: what it answers is not "every CI check
	// reported against your commit" but "nothing here reads those, and
	// this is what does". Saying so in the description is what stops a
	// run reaching for it as a GitHub check report and then having to
	// reconcile the answer with what it expected
	// (PullRequestScope.GitHubCIIgnored).
	if scope.GitHubCIIgnored {
		return Tool{
			Name: "pull_request_status",
			Description: "Show where the branch you are pushing to stands: its tip " +
				"commit and the pull request open for it, if there is one yet. " +
				"This repository's gate is grain CI (.grain/workflows) and " +
				"GitHub's check runs are not read here, so this reports none of " +
				"them -- they hold nothing, nobody waits on them, and a red one " +
				"is not yours to fix. Use wait_for_checks for the verdict that " +
				"does decide whether this branch can merge. This only reads; it " +
				"pushes nothing and changes nothing.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
			ElisionAdvice: elisionAdviceCIReport,
			Handler: func(ctx context.Context, _ map[string]any) Result {
				if client == nil || !scope.complete() {
					return Result{
						Text: "There is no pull request to report on: this run has no GitHub " +
							"repository configured for it. Do the work in the sandbox and " +
							"report what you found instead.",
						IsError: true,
					}
				}
				text, err := pullRequestStatus(ctx, client, grainCI, scope)
				if err != nil {
					return Result{Text: err.Error(), IsError: true}
				}
				return Result{Text: text}
			},
		}
	}
	grainCIClause := ""
	if grainCI != nil {
		grainCIClause = " It also reports what grain's own CI " +
			"(.grain/workflows) makes of the same commit, which is not a " +
			"GitHub check and appears nowhere in the list: a red one keeps " +
			"this pull request out of grain's merge queue exactly as a red " +
			"check does."
	}
	return Tool{
		Name: "pull_request_status",
		Description: "Show what GitHub currently says about the branch you are " +
			"pushing to: the pull request open for it, if there is one " +
			"yet, and every CI check reported against your latest pushed " +
			"commit, with the failing ones named and the end of each " +
			"failing job's own log quoted under it. Use this to find out " +
			"whether the tests actually pass -- commit, `git push` your " +
			"branch, then call this to read CI's verdict on that commit, " +
			"and repeat until it is green. Checks are reported per commit, " +
			"so a result only reflects work you have already pushed, and a " +
			"check that has not finished carries no verdict at all." +
			grainCIClause + " This " +
			"only reads; it pushes nothing and changes nothing.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		ElisionAdvice: elisionAdviceCIReport,
		Handler: func(ctx context.Context, _ map[string]any) Result {
			if client == nil || !scope.complete() {
				return Result{
					Text: "There is no pull request to report on: this run has no GitHub " +
						"repository configured for it. Do the work in the sandbox and " +
						"report what you found instead.",
					IsError: true,
				}
			}
			text, err := pullRequestStatus(ctx, client, grainCI, scope)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: text}
		},
	}
}

// pullRequestStatus renders the whole answer: where the branch is, which
// pull request (if any) carries it, and what CI made of its tip commit.
//
// The branch is read first and on its own, because "you have not pushed
// anything yet" is a completely different message from "your tests are
// failing" and the two are otherwise easy to confuse -- an empty check
// list looks the same either way.
// ctx is here for two things: a report with no log to show waits a little
// for one to turn up (failing_job_logs.go), and a cancelled run should
// not be left sitting in that wait -- and grain CI's verdict is read
// under it too, since that read is a hop to the daemon (grain_ci.go).
func pullRequestStatus(ctx context.Context, client PullRequestReader, grainCI GrainCIReader,
	scope PullRequestScope) (string, error) {

	var head *github.BranchHead
	if scope.Branch != "" {
		var err error
		head, err = client.GetBranchHead(scope.Owner, scope.Repo, scope.Branch)
		if err != nil {
			return "", fmt.Errorf("reading %s/%s's %s branch: %w", scope.Owner, scope.Repo, scope.Branch, err)
		}
	}
	if head == nil {
		if scope.PullRequestNumber > 0 {
			return externalPullRequestStatus(ctx, client, grainCI, scope)
		}
		return fmt.Sprintf(
			"Branch %s does not exist on %s/%s yet, so there is no pull request and no CI "+
				"to report. Commit and `git push origin %s`, then call this again.",
			scope.Branch, scope.Owner, scope.Repo, scope.Branch), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Branch %s is at %s (%s).\n", scope.Branch, shortSHA(head.SHA), firstLine(head.Message))

	pr, err := client.FindOpenPullRequestForBranch(scope.Owner, scope.Repo, scope.Branch)
	if err != nil {
		return "", fmt.Errorf("looking for an open pull request for %s: %w", scope.Branch, err)
	}
	if pr == nil {
		b.WriteString("No pull request is open for it yet -- grain opens one when this run " +
			"finishes. Any checks below came from your push directly.\n")
		if scope.PullRequestNumber > 0 {
			detail, err := client.GetPullRequest(scope.Owner, scope.Repo, scope.PullRequestNumber)
			if err == nil {
				fmt.Fprintf(&b, "Associated pull request #%d (%s) is %s%s.\n",
					detail.Number, detail.HTMLURL, detail.State, mergeableClause(detail))
				b.WriteString(reviewCommentsSection(client, commenterOf(client), scope, detail.Number))
			}
		}
	} else {
		detail, err := client.GetPullRequest(scope.Owner, scope.Repo, pr.Number)
		if err != nil {
			return "", fmt.Errorf("reading pull request #%d: %w", pr.Number, err)
		}
		// The link comes off `pr`, the same lookup the number does.
		// Taking one field from each read split a single reference
		// across two responses, and wherever the detail read came back
		// without html_url this line rendered "Pull request #42 () is
		// open" -- an empty pair of parentheses where the link belongs.
		// Both reads carry html_url, so the copy already in hand was
		// always enough.
		//
		// The converse is deliberately left alone: a caller whose `pr`
		// is the one missing html_url gets those same empty parentheses
		// rather than a fallback to detail.HTMLURL. A fallback would put
		// the line back on two reads to cover a shape no live GitHub
		// response produces, and the empty parentheses are what made the
		// original blank visible enough to fix -- quietly printing the
		// number alone would not have been. Only the source is pinned
		// (pullrequest_tools_test.go's
		// TestPullRequestStatusTakesTheLinkOffTheLookupThatNamedTheNumber);
		// nothing pins the blank rendering, so a caller that really does
		// arrive holding only the detail's copy can add the fallback.
		fmt.Fprintf(&b, "Pull request #%d (%s) is %s%s.\n",
			pr.Number, pr.HTMLURL, detail.State, mergeableClause(detail))
		b.WriteString(reviewCommentsSection(client, commenterOf(client), scope, pr.Number))
	}

	// A repository whose gate is grain CI gets no check list, and no
	// read to build one from: what GitHub says here is not a verdict
	// about this branch, and printing it would be handing the run a
	// dozen reds to explain away before it could get to the one answer
	// that decides anything (PullRequestScope.GitHubCIIgnored).
	if scope.GitHubCIIgnored {
		b.WriteString("\n")
		b.WriteString(gateIsGrainCINotice(grainCI != nil))
		b.WriteString(grainCIReport(ctx, grainCI, head.SHA, scope.Branch))
		if scope.BaseIncident != "" {
			b.WriteString("\n\n" + scope.BaseIncident)
		}
		return b.String(), nil
	}

	checks, err := checkRunsForCommit(client, scope, head.SHA)
	if err != nil {
		return "", err
	}
	attributions := annotateChecksWithAttribution(client, scope, checks)
	b.WriteString("\n")
	b.WriteString(renderChecks(checks, scope.Branch, shortSHA(head.SHA), attributions))
	// Before the logs rather than after them. A failing job's log runs to
	// a screenful of somebody else's output, and a paragraph on the far
	// side of it saying "the thing actually holding this pull request is
	// grain CI" is a paragraph a run reads last if at all.
	b.WriteString(grainCIReport(ctx, grainCI, head.SHA, scope.Branch))
	// Only once something has failed: there are no logs to read on a
	// green commit, and the three reads it costs are not worth making to
	// find that out (failing_job_logs.go).
	if anyFailed(checks) {
		b.WriteString(failingJobLogs(ctx, client, scope, head.SHA, checks, attributions))
	}
	if scope.BaseIncident != "" {
		b.WriteString("\n\n" + scope.BaseIncident)
	}
	return b.String(), nil
}

func externalPullRequestStatus(ctx context.Context, client PullRequestReader, grainCI GrainCIReader,
	scope PullRequestScope) (string, error) {

	detail, err := client.GetPullRequest(scope.Owner, scope.Repo, scope.PullRequestNumber)
	if err != nil {
		return "", fmt.Errorf("reading pull request #%d: %w", scope.PullRequestNumber, err)
	}
	var b strings.Builder
	if scope.Branch != "" {
		fmt.Fprintf(&b, "Branch %s has not been pushed yet; reporting on pull request #%d.\n",
			scope.Branch, detail.Number)
	}
	fmt.Fprintf(&b, "Pull request #%d (%s) is %s%s.\n",
		detail.Number, detail.HTMLURL, detail.State, mergeableClause(detail))
	b.WriteString(reviewCommentsSection(client, commenterOf(client), scope, detail.Number))

	if detail.HeadSHA == "" {
		return b.String(), nil
	}
	branchLabel := detail.HeadRef
	if branchLabel == "" {
		branchLabel = fmt.Sprintf("PR #%d", detail.Number)
	}
	if scope.GitHubCIIgnored {
		b.WriteString("\n")
		b.WriteString(gateIsGrainCINotice(grainCI != nil))
		b.WriteString(grainCIReport(ctx, grainCI, detail.HeadSHA, branchLabel))
		if scope.BaseIncident != "" {
			b.WriteString("\n\n" + scope.BaseIncident)
		}
		return b.String(), nil
	}
	checks, err := checkRunsForCommit(client, scope, detail.HeadSHA)
	if err != nil {
		return "", err
	}
	attributions := annotateChecksWithAttribution(client, scope, checks)
	b.WriteString("\n")
	b.WriteString(renderChecks(checks, branchLabel, shortSHA(detail.HeadSHA), attributions))
	b.WriteString(grainCIReport(ctx, grainCI, detail.HeadSHA, branchLabel))
	if anyFailed(checks) {
		b.WriteString(failingJobLogs(ctx, client, scope, detail.HeadSHA, checks, attributions))
	}
	if scope.BaseIncident != "" {
		b.WriteString("\n\n" + scope.BaseIncident)
	}
	return b.String(), nil
}

// checkRunsForCommit reads the checks against sha, falling back to the
// Actions API exactly the way orchestrator.checkRunsFor does and for the
// same reason: a fine-grained token granted "Actions" read but not
// "Checks" can see workflow runs and nothing else, and a deployment on
// one would otherwise get a permanent permission error here rather than
// the CI signal it does have.
//
// Unlike that function, a credential that can read neither is an error
// rather than a silent "unknown". This has one caller, answering a
// question an agent asked out loud, and telling it "no checks" when the
// truth is "I am not allowed to look" would send it off to merge-ready
// confidence on a build it never saw.
func checkRunsForCommit(client PullRequestReader, scope PullRequestScope, sha string) ([]github.CheckRun, error) {
	checks, err := client.ListCheckRuns(scope.Owner, scope.Repo, sha)
	if err == nil {
		return checks, nil
	}
	if !github.IsPermissionDenied(err) {
		return nil, fmt.Errorf("reading check runs for %s: %w", shortSHA(sha), err)
	}

	runs, fallbackErr := client.ListWorkflowRuns(scope.Owner, scope.Repo, sha)
	if fallbackErr == nil {
		return runs, nil
	}
	if !github.IsPermissionDenied(fallbackErr) {
		return nil, fmt.Errorf("reading workflow runs for %s: %w", shortSHA(sha), fallbackErr)
	}
	return nil, fmt.Errorf(
		"this grain deployment's GitHub credential may read neither check runs nor Actions "+
			"workflow runs, so CI is invisible from here -- an operator has to grant it "+
			"before this tool can tell you anything. GitHub said: %v", err)
}

// checkFailed reports whether a completed check counts as broken.
//
// Deliberately the same three conclusions orchestrator.failingChecks
// treats as failing, deliberately not shared with it: that one gates a
// merge and this one writes a sentence to an agent, and the two are free
// to diverge. "cancelled" is where they do, and it is still not a
// failure here -- nothing broke, and telling a run to reproduce the
// failure would send it hunting a bug that does not exist. It is not a
// pass either, on either side: since 2026-09-08 the merge gate holds the
// pull request PENDING on one until the check reports or a person is
// told (orchestrator.healthFrom's "cancelled is neither"), and since
// this change the report says so in its own words rather than counting
// the check under "otherwise done" and leaving the run to find out two
// hours later (checkCancelled, cancelledHold). github.CheckRun's own doc
// comment is where the decision not to narrow GitHub's enum in that
// package is recorded.
func checkFailed(c github.CheckRun) bool {
	if c.Status != "completed" || c.Conclusion == nil {
		return false
	}
	switch *c.Conclusion {
	case "failure", "timed_out", "startup_failure":
		return true
	}
	return false
}

// checkCancelled reports whether a completed check is one GitHub
// concluded "cancelled" -- somebody pressed cancel, or a job that failed
// took its siblings down with it. Exactly orchestrator.cancelledChecks's
// test, and separate from checkFailed for the reason its doc comment
// gives: this is a check that never delivered a verdict, which is
// neither a failure to reproduce nor a pass to finish on.
func checkCancelled(c github.CheckRun) bool {
	return c.Status == "completed" && c.Conclusion != nil && *c.Conclusion == "cancelled"
}

// anyFailed reports whether any of these checks is one the report will
// call FAILING -- the one question worth asking before spending reads on
// the failing jobs' logs.
func anyFailed(checks []github.CheckRun) bool {
	for _, c := range checks {
		if checkFailed(c) {
			return true
		}
	}
	return false
}

// renderChecks lists every check against the pushed commit and says what
// to do about them -- and, wherever the honest answer is "not yet",
// points at wait_for_checks rather than at calling this again on a timer.
// Both of those cases are a snapshot of something still in motion, which
// is precisely what the blocking tool exists to spare an agent a turn on
// (wait_for_checks_tool.go's own doc comment).
func renderChecks(checks []github.CheckRun, branch, sha string, attributions ...map[string]ci.JobAttribution) string {
	if len(checks) == 0 {
		return fmt.Sprintf(
			"GitHub reports no checks at all against %s: either CI has not registered them yet "+
				"(usually a minute or two after a push) or this repo has no CI. "+
				"wait_for_checks blocks until they appear and tells the two apart.",
			sha)
	}

	tally := tallyChecks(checks, attributions...)
	var b strings.Builder
	fmt.Fprintf(&b, "Checks against %s:\n", sha)
	b.WriteString(tally.lines)

	fmt.Fprintf(&b, "\n%s\n", tally.counts())
	switch {
	case tally.failing > 0:
		// No word about the cancellations here even when there are some:
		// the advice is already "fix it and push", and the push that
		// carries the fix reruns every check on the commit, cancelled
		// ones included.
		fmt.Fprintf(&b, "Fix those failures, commit, `git push origin %s` -- each push reruns CI "+
			"-- and call this again to see whether the fix took. "+
			baseComparisonPointer, branch)
	case tally.pending > 0:
		b.WriteString("Nothing has failed, but the unfinished checks carry no verdict yet: " +
			"call wait_for_checks rather than polling this, since it blocks for the " +
			"verdict and costs one turn instead of several.")
		if tally.cancelled > 0 {
			// Said now as well as later. The unfinished checks may all
			// go green, and then this is the only thing standing between
			// the run and a merge -- a run that reads "wait" here and
			// nothing else can spend its wait believing the wait is all
			// that is left.
			b.WriteString("\n\n" + cancelledHold(tally.cancelled, branch))
		}
	case tally.cancelled > 0:
		b.WriteString("Nothing has failed, but " + cancelledHold(tally.cancelled, branch))
	default:
		fmt.Fprintf(&b, "Every check that reported against %s is done and none of them failed.", sha)
	}
	return b.String()
}

// cancelledHold is what a run is told about a check GitHub cancelled: the
// one thing the check lines above cannot say, which is what a
// cancellation costs this pull request.
//
// It is the agent-facing half of orchestrator.healthFrom's "cancelled is
// neither". The merge gate reads a cancelled check as a verdict this
// commit never got and holds the pull request PENDING on it; the report
// this paragraph goes into used to count that check under "otherwise
// done" and close with "none of them failed", which is true and which a
// run reasonably finishes on. It then waits out the queue's stall
// deadline and wakes a person, for want of one more push.
//
// The deadline is deliberately not quoted as a number:
// defaultCheckStallDeadline lives in orchestrator, which cannot be
// imported here (it imports this package), so a figure copied into this
// string would be a figure nothing keeps honest. "Hours" is the part that
// matters to the decision anyway -- the run is choosing between pushing
// now and finishing on a pull request that will not merge.
//
// Only pull_request_status and wait_for_checks say it. base_branch_status
// reports a branch nobody is merging, where a cancellation means
// something else entirely (renderBaseChecks).
//
// It opens on the count rather than on "nothing has failed", so that it
// reads as a paragraph after a report that has already said so and takes
// that lead-in from its caller where it has not.
func cancelledHold(cancelled int, branch string) string {
	return fmt.Sprintf(
		"%d check(s) above were cancelled before they reported, and grain's merge queue does "+
			"not read a cancellation as a pass: it holds this pull request pending for a "+
			"verdict this commit will never get, then gives up after hours and wakes a "+
			"person. Only a re-run says more, so `git push origin %s` to rerun CI (an empty "+
			"commit will do), or ask whoever cancelled it to re-run.",
		cancelled, branch)
}

// checkTally is one commit's checks counted by verdict, with the line
// each of them gets. Shared by pull_request_status, wait_for_checks and
// base_branch_status so that a check can never be described one way by
// the tool that polls and another by the tool that waits -- the counts
// are the same numbers all of them then reason about.
type checkTally struct {
	lines     string
	failing   int
	pending   int
	cancelled int
	passed    int
}

// checkLabelWidth is the label column the check lines are laid out in.
//
// It is nine wide because "cancelled" is nine characters, and that is a
// deliberate purchase rather than a measurement. The cheap alternatives
// were to leave a cancelled check labelled "ok" -- the understatement
// this whole change exists to undo, and the worst of them, since the
// label is what a run skimming the list actually reads -- or to invent a
// shorter word for it, which puts a label on the line that is not the
// one GitHub or the merge queue uses for the same thing. What it costs is
// a column shift on every other line of three tools' output
// (pull_request_status, wait_for_checks, base_branch_status), which is
// why it is spelled out here: the label column is those tools' shared
// shape, and widening it again is the same decision made again.
const checkLabelWidth = 9

// tallyChecks classifies each check and renders its line. GitHub's own
// status and conclusion words are carried verbatim, since narrowing them
// is exactly how an agent ends up told that a "skipped" job passed.
func tallyChecks(checks []github.CheckRun, attributions ...map[string]ci.JobAttribution) checkTally {
	var tally checkTally
	var b strings.Builder
	for _, c := range checks {
		name := c.Name
		if name == "" {
			name = "(unnamed check)"
		}
		switch {
		case c.Status != "completed":
			tally.pending++
			fmt.Fprintf(&b, "  %-*s %s (%s)\n", checkLabelWidth, "running", name, c.Status)
		case checkFailed(c):
			tally.failing++
			fmt.Fprintf(&b, "  %-*s %s (%s)", checkLabelWidth, "FAILING", name, *c.Conclusion)
			if len(attributions) > 0 && attributions[0] != nil {
				if attr, ok := attributions[0][name]; ok && attr.Summary() != "" {
					fmt.Fprintf(&b, " -- Attribution: %s", attr.Summary())
				}
			}
			b.WriteString("\n")
		case checkCancelled(c):
			// Counted apart from `passed` rather than inside it: a run
			// reading "1 otherwise done" reads a check that is done with,
			// and this one is done without having said anything. The
			// conclusion is printed even though the label repeats it, so
			// that every line in the list has the same three parts.
			tally.cancelled++
			fmt.Fprintf(&b, "  %-*s %s (%s)\n", checkLabelWidth, "cancelled", name, *c.Conclusion)
		default:
			tally.passed++
			conclusion := "no conclusion reported"
			if c.Conclusion != nil {
				conclusion = *c.Conclusion
			}
			fmt.Fprintf(&b, "  %-*s %s (%s)\n", checkLabelWidth, "ok", name, conclusion)
		}
	}
	tally.lines = b.String()
	return tally
}

// counts is the arithmetic line under the check lines, shared by all
// three tools that render a tally so that the numbers a run compares
// across them are counted and worded the same way.
//
// The cancelled count only appears when there is one. It is a rare shape
// -- most commits have none -- and a standing "0 cancelled" in every
// report would spend a column of every reader's attention on the case
// that is not happening, where the point of the number is to be noticed
// when it is not zero.
func (t checkTally) counts() string {
	if t.cancelled == 0 {
		return fmt.Sprintf("%d failing, %d not finished, %d otherwise done.",
			t.failing, t.pending, t.passed)
	}
	return fmt.Sprintf("%d failing, %d not finished, %d cancelled, %d otherwise done.",
		t.failing, t.pending, t.cancelled, t.passed)
}

// mergeableClause renders GitHub's tri-state Mergeable as a clause to
// hang off the pull request's state, including the nil case: GitHub
// computes mergeability asynchronously, so a read seconds after a push
// legitimately does not know yet (github.PullRequestDetail.Mergeable's
// own doc comment), and saying so beats implying either answer.
func mergeableClause(detail github.PullRequestDetail) string {
	switch {
	case detail.Merged:
		return " and merged"
	case detail.Mergeable == nil:
		return ", and GitHub has not finished working out whether it merges cleanly"
	case *detail.Mergeable:
		return " and merges cleanly into " + detail.BaseRef
	default:
		return " and conflicts with " + detail.BaseRef
	}
}

// shortSHA abbreviates a commit for reading, leaving anything already
// short (or empty) alone rather than slicing a string that has no seven
// characters to give.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// firstLine is a commit message's subject, quoted, bounded so a message
// with a novel in it cannot flood the answer.
func firstLine(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	if len(line) > 72 {
		line = line[:72] + "..."
	}
	if line == "" {
		return "no commit message"
	}
	return `"` + line + `"`
}
