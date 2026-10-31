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

// Package githubsim replicates v1's own live-test double for the GitHub
// REST API -- tests/test_live_issue_to_pr.py's RealGitHubMock -- as a
// github.Transport, so a Go end-to-end test can wire a real
// github.RESTClient against it exactly the way github.FakeTransport lets
// a unit test wire one against a single scripted response. The
// difference is state: Sim holds a mutable set of issues and pull
// requests across a whole test, so a sequence of calls (add a label, list
// issues, open a PR, list pull requests again) sees the effect of the one
// before it, the same as talking to real GitHub across a real
// orchestration run would.
//
// Sim implements every endpoint github.Client's real implementation
// (github.RESTClient) calls: list/get/create issues, close/reopen/update
// an issue, add/remove a label, default branch, branch existence, the
// commits one branch carries over another, create/find/get/merge/close a
// pull request, rewrite one's body, merging one branch into another,
// the plain comment thread, inline review comments and draft reviews, and
// check runs -- the whole surface
// github.go's Client interface names, so a live end-to-end test never
// has to fall back to github.FakeTransport partway through for an
// endpoint Sim doesn't yet answer. Wired in as a github.Transport,
// github.RESTClient's own logic (path building, pagination via the shape
// of a response's own Link header where it applies, status-code
// handling, JSON field extraction) runs completely unmodified; only the
// network call underneath is swapped.
//
// BranchExists is the one endpoint that would be dishonest as a canned
// answer: whether a branch is "real" is exactly the question a live
// end-to-end test is trying to prove an agent's push settled, so Sim
// answers it by running a real `git show-ref` against a real bare repo
// rather than consulting its own bookkeeping.
package githubsim

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

var (
	issueDirectiveRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+(?:[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+)?#(\d+)\b`)
	issueCommentsRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)/comments$`)
	issueRe          = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)$`)
	// issueCommentRe: one conversation comment, edited in place
	// (github.RESTClient.UpdateComment).
	// commitPullsRe: the pull requests a commit belongs to
	// (github.RESTClient.MergedPullRequestForCommit), answered from
	// MergedPullRequestForCommit.
	commitPullsRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/commits/([^/]+)/pulls$`)
	issueCommentRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/comments/(\d+)$`)
	// repoRe: the target repo's own default branch, read once per
	// dispatch so the PR base comes from the repo itself rather than a
	// single configured base branch.
	repoRe        = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)$`)
	issuesRe      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues$`)
	labelsPostRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)/labels$`)
	labelDeleteRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)/labels/([^/]+)$`)
	branchRe      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/branches/(.+)$`)
	// branchesRe: the repo's whole branch listing
	// (github.RESTClient.ListBranches), which grain's UI checks a typed
	// base branch against (pkg/ui's ListGitHubBranches).
	branchesRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/branches$`)
	// compareRe: the commits one branch carries that another does not
	// (github.RESTClient.CompareCommits), which a pull request's
	// description is built from. Both refs live in one path segment
	// separated by "...", slashes in a branch name and all.
	compareRe      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/compare/(.+?)\.\.\.(.+)$`)
	pullsRe        = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls$`)
	pullRe         = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)$`)
	pullMergeRe    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/merge$`)
	pullCommentsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/comments$`)
	// pullCommentRepliesRe: a reply in an inline review thread
	// (github.RESTClient.ReplyToReviewComment).
	pullCommentRepliesRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/comments/(\d+)/replies$`)
	// pullFilesRe: which files a pull request touches
	// (github.RESTClient.ListPullRequestFiles), which the merge queue
	// reads to tell a startup failure its own branch caused -- a workflow
	// file this pull request made unparseable -- from one belonging to
	// the repository (orchestrator.advanceMergeQueueHead).
	pullFilesRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/files$`)
	// pullCommitsRe: a pull request's own commits
	// (github.RESTClient.ListPullRequestCommits), which the merge queue
	// reads a merged change's port footers from (orchestrator.decidePort).
	pullCommitsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/commits$`)
	pullReviewsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/reviews$`)
	// pullRequestedReviewersRe, branchRulesRe and rulesetRe are what
	// github.RESTClient.MergeRequirements reads to say who has to approve
	// a merge GitHub refused for want of a review.
	pullRequestedReviewersRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)/requested_reviewers$`)
	branchRulesRe            = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/rules/branches/(.+)$`)
	rulesetRe                = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/rulesets/(\d+)$`)
	checkRunsRe              = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/commits/([^/]+)/check-runs$`)
	// The three Actions endpoints github.RESTClient.FailedJobLogs walks
	// from a commit to a log: the commit's runs, one run's jobs, one
	// job's log. actionsRunsRe is also what ListWorkflowRuns reads when a
	// credential cannot reach the Checks API.
	actionsRunsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/runs$`)
	runJobsRe     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/runs/(\d+)/jobs$`)
	jobLogsRe     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/jobs/(\d+)/logs$`)
	// And the fourth read hung off that walk, for a run whose jobs served
	// no log: the check run behind one of them, which is where GitHub
	// writes why a job it never started did not start. Its annotations
	// are the second place the same sentence can appear.
	checkRunRe            = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/check-runs/(\d+)$`)
	checkRunAnnotationsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/check-runs/(\d+)/annotations$`)
	// gitRefsRe/gitRefRe: the git-database endpoints CreateBranch and
	// UpdateBranch call -- release management's own (bwsalmon/agents#398).
	gitRefsRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/refs$`)
	gitRefRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/refs/heads/(.+)$`)
	// gitObjectsRe and gitOtherRefRe: the rest of the git-database API
	// github.RESTClient.PublishFiles writes through -- blobs, a tree and a
	// commit, and a ref outside refs/heads to keep the commit alive.
	gitObjectsRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/(blobs|trees|commits)$`)
	gitOtherRefRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/(refs/.+)$`)
	// mergesRe: the branch-into-branch merge the merge queue makes to
	// bring a stale head up to date with its base
	// (github.RESTClient.MergeBranch, orchestrator.refreshStaleHead).
	mergesRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/merges$`)
	// collaboratorPermissionRe, reactions and check-runs write endpoints for
	// GitHub App bot reviews, reactions, check runs and trust checks.
	collaboratorPermissionRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/collaborators/([^/]+)/permission$`)
	issueCommentReactionsRe  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/comments/(\d+)/reactions$`)
	pullCommentReactionsRe   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/comments/(\d+)/reactions$`)
	createCheckRunsRe        = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/check-runs$`)
	updateCheckRunRe         = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/check-runs/(\d+)$`)
	contentsRe               = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/contents/(.+)$`)
)

// Issue is one seeded or created fake issue -- Sim's own bookkeeping, not
// github.Issue, since Sim needs a mutable Labels set an incoming request
// can add to or remove from.
type Issue struct {
	Title  string
	Body   string
	Author string
	Labels map[string]struct{}
	// State is GitHub's own open/closed field -- defaults to the zero
	// value, which issueJSON below treats as "open", the same fallback
	// github.decodeIssue applies to a real response with no state field.
	State string
}

// Review is one draft review Sim recorded through CreateReview -- kept
// separately from ReviewComments (below), the same way real GitHub keeps
// a pending review's own comments invisible through the review-comments
// endpoint until a human submits it on github.com (github.go's own
// package doc comment on why create_review only ever posts a draft).
type Review struct {
	Number   int
	Event    string
	Body     string
	Comments []github.NewReviewComment
}

// CommentReaction records one reaction posted to an issue or review comment.
type CommentReaction struct {
	CommentID int64
	Content   string
	Review    bool
}

// PullRequest is one pull request Sim recorded through CreatePullRequest.
type PullRequest struct {
	Number  int
	Title   string
	Body    string
	Head    string
	Base    string
	HTMLURL string
	// State is GitHub's own open/closed field. Defaults to "open" at
	// creation; MergePullRequest (below) sets it to "closed" the same way
	// a real merge does, and a test can set it directly to stand in for
	// GitHub's own merge button the way e2e's mergeBranchIntoDefault
	// does for the git side of a merge.
	State string
	// Mergeable stands in for GitHub's own asynchronously computed field
	// -- nil (unknown) unless a test sets it, since Sim has no real merge
	// engine to compute it with.
	Mergeable *bool
	// Merged and MergedAt are GitHub's own fields for the one thing State
	// alone cannot say: whether this PR closed by merging rather than
	// without merging (github.PullRequestDetail.Merged's own doc
	// comment). MergePullRequest sets both; a test standing in for a
	// human closing a PR without merging (setting State to "closed"
	// directly, the same doc comment above already describes) leaves
	// Merged false, matching GitHub's own field for that case.
	Merged   bool
	MergedAt time.Time
	// CreatedAt is GitHub's own field: when the PR was opened, set by
	// CreatePullRequest -- Sim's real wall clock rather than a caller's
	// injected one, since Sim has no clock of its own to inject
	// (unlike orchestrator.SyncPullRequests, which takes now from its
	// caller).
	CreatedAt time.Time
	// Additions, Deletions and ChangedFiles are the diff's size, which
	// real GitHub computes and returns on the pull-request detail
	// endpoint alone (github.PullRequestDetail.Additions). Seeded by a
	// test rather than computed from the bare repo: the figure a test
	// wants to assert on is the one it chose, and a merged pull request
	// whose head branch has since been deleted -- exactly the case that
	// matters here -- has no diff left to measure, while GitHub still
	// reports the size it recorded at merge.
	//
	// All three zero, the default, is a Sim that reports no size, which
	// is what every caller written before these existed sees and what
	// orchestrator reads as "GitHub did not say."
	Additions    int
	Deletions    int
	ChangedFiles int
	// Author is the login GitHub reports as the pull request's user,
	// omitted from the response when empty.
	Author string
	// HeadRepo is the "owner/name" of the fork the head branch lives in,
	// for a test standing in for a pull request from a fork; empty is the
	// base repository itself. MaintainerCanModify is GitHub's flag of the
	// same name, read only alongside a HeadRepo.
	HeadRepo            string
	MaintainerCanModify bool
	// MergeCommitSHA is GitHub's own merge_commit_sha, seeded by a test
	// standing in for a merge done by hand (a squash merge, say), and
	// omitted from the response when empty.
	MergeCommitSHA string
	// Labels are the labels applied through POST .../issues/N/labels,
	// which real GitHub serves for a pull request's number as well as an
	// issue's (orchestrator.labelOrigin's `origin/#N`).
	Labels map[string]struct{}
	// MergeMessage is the commit message PUT .../pulls/N/merge was asked
	// for (commit_title, a blank line, commit_message), "" when it named
	// none and the merge commit carries GitHub's default.
	MergeMessage string
	// mergedCommits is the pull request's own commits as they stood when
	// MergePullRequest merged it: what GitHub goes on answering for a
	// merged pull request once its base contains its head, and after its
	// head branch is deleted.
	mergedCommits []map[string]any
	// mergedDiff is the pull request's own diff as it stood when
	// MergePullRequest merged it, which GitHub goes on serving for it
	// (github.RESTClient.PullRequestDiff) once base...head is empty.
	mergedDiff string
	merged     bool
}

// Call is one request Sim answered, kept for tests that want to assert on
// what was actually asked of it.
type Call struct {
	Method string
	Path   string
}

// WorkflowJob is one GitHub Actions job of one commit's run, and what it
// printed -- the thing the merge queue reads to put a failing build's own
// output into the fix task it files (orchestrator.failingJobLogs).
//
// There is no Status field: a seeded job is one that has run, so Sim
// answers "completed" for all of them. An empty Conclusion reads as
// "success", so a test seeding a job it wants ignored need only leave it
// blank.
type WorkflowJob struct {
	Name       string
	Conclusion string
	// Log is what the logs endpoint serves for this job. Empty means
	// GitHub has none to serve -- answered 410, the way a real one
	// answers for a log past its retention window, which is a case
	// FailedJobLogs has to skip rather than fail on.
	Log string
	// NeverStarted marks a job GitHub created and then declined to run at
	// all -- no Actions minutes, a spending limit reached, no runner for
	// the labels it asked for. Its `steps` array comes back empty, which
	// is the only thing in the API separating it from a job that ran and
	// failed with an unreadable log, and the reason it must not be
	// repaired by pushing at the branch (see github.JobLog's
	// RunJobsNeverStarted).
	//
	// Not a Conclusion of its own: GitHub concludes such a job `failure`,
	// like any other, and a double that let a test seed something more
	// distinguishable than that would be testing an API GitHub does not
	// have.
	NeverStarted bool
	// FailureReason is what the check run behind this job says about it --
	// served as that check run's output title, which is where GitHub
	// writes "The job was not started because...". Empty means GitHub
	// wrote nothing there, which is what every job that really ran looks
	// like.
	FailureReason string
	// FailingStep is the name of the step that failed in this job; defaults
	// to "Run the job" when empty.
	FailingStep string
}

// WorkflowRun is one seeded GitHub Actions workflow run for Sim.WorkflowRuns.
type WorkflowRun struct {
	ID         int64
	Name       string
	Path       string
	HeadBranch string
	HeadSHA    string
	Event      string
	Status     string
	Conclusion string
	RunAttempt int
	Jobs       []WorkflowJob
}

// Sim is a github.Transport backed by an in-memory set of issues and pull
// requests, plus a real bare git repo for the one endpoint
// (BranchExists/branch lookups) that would be dishonest as a canned
// answer. Owner and Repo are the only repo Sim answers for -- a request
// naming any other repo is an unhandled-request panic, the same as v1's
// own mock, since a test wiring a second repo in in error should fail
// loudly rather than silently return an empty answer.
type Sim struct {
	Owner         string
	Repo          string
	BareRepo      string
	DefaultBranch string

	// PullRequestBase is the number the first pull request opened on this
	// sim gets, the next one base+1 and so on. Zero means 9000. A test
	// package whose tests run in parallel against sims of the same
	// owner/repo sets it from UniquePullRequestBase: grain keeps
	// process-wide memory keyed by pull request ref and head sha (forge's
	// refusal, read-failure and check-sighting records, the merge
	// queue's gather holds), and two sims that both open acme/widgets#9000
	// on identical commits -- empty commits made inside the same second
	// hash the same -- otherwise read and write each other's records.
	PullRequestBase int

	// Reachable is what each token -- the Authorization header's value,
	// without its "token " -- is answered with by the two endpoints that
	// list the repositories a credential can reach: /installation/
	// repositories for an App installation token and /user/repos for a
	// personal access token. A token with no entry is answered 401, as
	// GitHub answers one it does not know.
	Reachable map[string][]ReachableRepo

	Issues       map[int]*Issue
	PullRequests []PullRequest
	Calls        []Call

	// Comments is every posted top-level comment, by issue/PR number --
	// GitHub's own issues-comments endpoint serves both, per Comment's own
	// doc comment in github.go.
	Comments map[int][]github.Comment
	// MergedPullRequestForCommit is the merged pull request a commit
	// landed by, by sha, for a test that wants a merge to have come
	// from one. A commit not in it belongs to none.
	MergedPullRequestForCommit map[string]int
	// CheckRuns is keyed by whatever ref a test seeds it under -- a branch
	// name or a sha. A read for either finds it: BareRepo is a real
	// repository, so the branch a test named and the sha the caller asks
	// by resolve to one commit here the same way they do on GitHub (see
	// checkRunsFor).
	CheckRuns map[string][]github.CheckRun
	// WorkflowJobs is the CI behind those check runs: the GitHub Actions
	// jobs, and their logs, that github.RESTClient.FailedJobLogs reaches
	// through a commit's workflow runs. Keyed like CheckRuns, except that
	// a branch name works even though the endpoints are called with a sha
	// -- see workflowJobsFor, which resolves one to the other against the
	// bare repo, since a test seeding these has no way to know a sha it
	// never wrote down.
	//
	// Seeded, not derived: a Sim that invented a job per seeded check run
	// would decide for the test what CI looked like, and the point of
	// these is a test saying what one particular job printed.
	WorkflowJobs map[string][]WorkflowJob
	// WorkflowRuns is explicit workflow runs seeded for GET .../actions/runs
	// and GET .../actions/runs/{id}/jobs.
	WorkflowRuns []WorkflowRun
	// StartupFailures marks the commits (keyed exactly like WorkflowJobs)
	// whose workflow run GitHub concluded before any job of it started:
	// the run is there, it is `startup_failure`, and its jobs endpoint
	// answers with none. That is what a repository that may not run
	// Actions at all looks like from the API, and it is the one CI shape
	// where the failure is certainly not in the tree -- so grain has to
	// be able to see it, and a test has to be able to seed it.
	//
	// Separate from WorkflowJobs rather than "a commit with an empty job
	// list", because that already means something here: no run at all.
	StartupFailures map[string]bool
	// ReviewComments is every inline review comment on a PR, keyed by PR
	// number -- what ListReviewComments reads back. Seeded directly by a
	// test standing in for a human reviewer's own comments, since
	// CreateReview's own draft reviews never land here (see Review's doc
	// comment above).
	ReviewComments map[int][]github.ReviewComment
	// ResolvedThreads is every review thread resolveReviewThread has
	// marked resolved, by thread ID. The sim's threads are its review
	// comments grouped by the comment that opened each one, and a
	// thread's ID is "thread-" and that comment's ID (reviewThreadsJSON).
	ResolvedThreads map[string]bool
	// Reviews is every draft review CreateReview has recorded, in call
	// order -- Sim's own bookkeeping for a test to assert what an
	// add_review_comment call produced, the same append-only shape
	// PullRequests already uses.
	Reviews []Review
	// CollaboratorPermissions maps username (case-insensitively) to GitHub
	// repository permission ("admin", "maintain", "write", "triage", "read",
	// "none") for GET /repos/{owner}/{repo}/collaborators/{username}/permission.
	CollaboratorPermissions map[string]string
	// CommentReactions records reactions posted via ReactionPoster.
	CommentReactions []CommentReaction
	nextCheckRunID   int64
	// RefuseMerges, when non-empty, is what GitHub answers PUT
	// .../pulls/N/merge with instead of merging: a 405 carrying this
	// message, which is the shape of every refusal that is a rule on
	// GitHub's side rather than a problem with the branch -- a protection
	// wanting an approving review, a required check this deployment does
	// not run, a base that has to be up to date, a credential without
	// write access.
	//
	// A test sets it to stand in for the pull request that reads
	// perfectly clean and will not merge however many times it is asked,
	// which is the one thing Sim could not be made to do: its own 409 for
	// a moved head resolves itself on the next cycle, and there is no
	// branch protection here to violate.
	RefuseMerges string
	// RequireMergeQueue is a ruleset requiring GitHub's merge queue on
	// every branch: PUT .../pulls/N/merge is refused with GitHub's own
	// "Changes must be made through the merge queue", and the GraphQL
	// enqueuePullRequest mutation puts the pull request in MergeQueue
	// instead, where LandMergeQueue merges it -- which a test calls to
	// stand in for GitHub's queue passing its merge group.
	RequireMergeQueue bool
	// MergeQueue is the pull requests in GitHub's merge queue, by number,
	// front first, and Enqueues how many times enqueuePullRequest has been
	// called.
	MergeQueue []int
	Enqueues   int
	// Now is the clock a pull request's opening and merging are stamped
	// with, for a test whose own clock is not the wall clock; nil is
	// time.Now.
	Now func() time.Time
	// RequireApprovals, when positive, is a GitHub rule wanting that many
	// approving reviews (Approvals) before a pull request merges: the
	// merge is answered 405 "Review required" until it has them, and
	// merges once it does -- which is what lets a test show the queue
	// waiting for an approval and merging when it arrives.
	RequireApprovals int
	// RequireConversationResolution, when true, is GitHub's "Require
	// conversation resolution before merging" branch protection / ruleset
	// rule: PUT .../merge answers 405 "Repository rule violations found\n\nAll
	// comments must be resolved." while any review thread on the pull
	// request is unresolved.
	RequireConversationResolution bool
	// RefuseLabels, when non-zero, is the status GitHub answers POST
	// .../issues/N/labels with instead of applying them -- a 403 for a
	// token without the permission, a 422 for a label the repository will
	// not create.
	RefuseLabels int
	// Approvals is who has approved each pull request, keyed by number:
	// what GET .../pulls/N/reviews answers, as APPROVED reviews.
	Approvals map[int][]string
	// RequestedReviewers is each pull request's outstanding review
	// requests, keyed by number: a login, or "org/team" for a team (the
	// code owners GitHub requests on its own).
	RequestedReviewers map[int][]string
	// BranchRules is the ruleset rules GET .../rules/branches/{branch}
	// answers for each branch, as GitHub's own JSON objects, and
	// RulesetNames the name each ruleset id reads back under.
	BranchRules  map[string][]map[string]any
	RulesetNames map[int]string
	// FailReads, when non-nil, is what Sim answers every GET with
	// instead of the answer it has.
	//
	// It stands in for the two ways a read of a pull request can fail
	// that no other knob here can produce, and that the merge queue has
	// to tell apart: GitHub having a bad minute (a 5xx, which resolves by
	// itself and which this deployment meets every few hours), and GitHub
	// refusing outright (a 404 on a repository the credential has lost, a
	// 403 on a permission it does not hold), which resolves only when
	// somebody does something.
	//
	// Every GET rather than only the pull request's own endpoint, because
	// that is the shape of the real thing: a bad minute at GitHub is not
	// confined to one path, and both of the reads forge.Observe makes are
	// observed failing in this deployment's log.
	FailReads *ReadFailure

	nextCommentID int
	nextIssue     int
	nextReviewID  int

	// runKeys is the commits (or branch names -- see WorkflowJobs) Sim
	// has answered a workflow-run id for, in the order it first did.
	// Real GitHub's run ids are its own; here the index in this slice is
	// the id, so that the runs endpoint and the jobs endpoint after it
	// agree about which commit is being asked about without Sim having to
	// keep a second map to invert.
	runKeys []string

	// mu serialises Request, which is every mutation this double makes:
	// the orchestrator dispatches concurrently -- a goroutine per run,
	// and since a run now outlives the cycle that started it (see
	// orchestrator.InFlight), several of them at once against one client
	// -- so two runs really do reach a Sim at the same time. Nothing
	// about the maps and slices above is safe for that on its own, and a
	// double that corrupts its own state under the very concurrency the
	// code it stands in for is built for would fail tests for a reason
	// that has nothing to do with what they assert.
	//
	// One lock for the whole method, rather than something finer: a
	// request here is microseconds of map work plus the occasional git
	// command against BareRepo, which has to be serialised anyway.
	// Fields are still read directly by tests, which do that before or
	// after a cycle rather than during one.
	mu sync.Mutex
}

// New returns a Sim seeded with no issues and no pull requests, answering
// for owner/repo, with defaultBranch as the target repo's own default
// branch and bareRepo as the real git repository BranchExists checks
// against.
// pullRequestBlocks counts the blocks UniquePullRequestBase has handed out.
var pullRequestBlocks atomic.Int64

// UniquePullRequestBase is a Sim.PullRequestBase no other call in this
// process returns: 9000 on the first call, then 19000, 29000 and so on,
// so the sims of tests running side by side never open the same pull
// request number. A block holds 10000 numbers, far more than a test
// opens, and stays clear of the 8000s the sim gives issues.
func UniquePullRequestBase() int {
	return 9000 + 10000*int(pullRequestBlocks.Add(1)-1)
}

func New(owner, repo, bareRepo, defaultBranch string) *Sim {
	return &Sim{
		Owner: owner, Repo: repo, BareRepo: bareRepo, DefaultBranch: defaultBranch,
		Issues:          map[int]*Issue{},
		Comments:        map[int][]github.Comment{},
		CheckRuns:       map[string][]github.CheckRun{},
		WorkflowJobs:    map[string][]WorkflowJob{},
		StartupFailures: map[string]bool{},
		ReviewComments:  map[int][]github.ReviewComment{},
		ResolvedThreads: map[string]bool{},
		nextCommentID:   1000,
		nextIssue:       8000,
		nextReviewID:    5000,
	}
}

// CreateEmptyRepository initializes an unseeded bare git repository at bareRepo
// (with no initial commit, http.receivepack=true, and receive.denyCurrentBranch=ignore)
// and returns a Sim whose first pushed branch becomes its default_branch, matching
// GitHub's behavior on a newly created empty repository.
func CreateEmptyRepository(owner, repo, bareRepo string) (*Sim, error) {
	if err := os.MkdirAll(bareRepo, 0o755); err != nil {
		return nil, err
	}
	for _, args := range [][]string{
		{"init", "--bare", "--initial-branch=main", bareRepo},
		{"--git-dir", bareRepo, "config", "http.receivepack", "true"},
		{"--git-dir", bareRepo, "config", "receive.denyCurrentBranch", "ignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
	}
	return New(owner, repo, bareRepo, "main"), nil
}

func (s *Sim) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// compareRev is what the compare endpoint accepts on either side of its
// "...": a branch name, or -- as on GitHub -- a commit SHA.
func (s *Sim) compareRev(ref string) (string, bool) {
	if s.branchExists(ref) {
		return "refs/heads/" + ref, true
	}
	if len(ref) != 40 {
		return "", false
	}
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "rev-parse", "--verify", "--quiet",
		"--end-of-options", ref+"^{commit}")
	cmd.Env = os.Environ()
	if cmd.Run() != nil {
		return "", false
	}
	return ref, true
}

// revSHA is the commit a compareRev answer names, or "" where it names
// none.
func (s *Sim) revSHA(rev string) string {
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "rev-parse", "--verify", "--quiet",
		"--end-of-options", rev+"^{commit}")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s *Sim) branchExists(branch string) bool {
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

// branchHead resolves branch's own tip commit for real, against BareRepo
// -- what backs GetBranchHead the same honest way branchExists backs
// BranchExists (this file's own doc comment): a caller cutting a release
// candidate from it needs the real sha CreateBranch then pins, not a
// canned one a test would have to keep in sync with whatever pushBranch
// last committed.
func (s *Sim) branchHead(branch string) (sha, message string, ok bool) {
	if !s.branchExists(branch) {
		return "", "", false
	}
	shaOut, err := exec.Command("git", "--git-dir", s.BareRepo, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return "", "", false
	}
	msgOut, err := exec.Command("git", "--git-dir", s.BareRepo, "log", "-1", "--format=%B", "refs/heads/"+branch).Output()
	if err != nil {
		return "", "", false
	}
	return strings.TrimSpace(string(shaOut)), strings.TrimRight(string(msgOut), "\n"), true
}

// branchNames lists BareRepo's real branches, sorted, for the same
// reason branchExists runs git: which branches a repo has is what a
// caller of the listing is asking, so it comes from the repo itself.
func (s *Sim) branchNames() ([]string, error) {
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Sim) setHEAD(branch string) {
	if s.BareRepo == "" || branch == "" {
		return
	}
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	cmd.Env = os.Environ()
	_ = cmd.Run()
}

// defaultBranch is the repository's default branch as GitHub reports it:
// DefaultBranch when that ref exists (or when no BareRepo is wired in), and
// the repository's first pushed branch when an initially empty BareRepo had a
// branch pushed to it before DefaultBranch ever existed -- matching GitHub's
// own rule that the first branch pushed to an empty repository becomes its
// default_branch.
func (s *Sim) defaultBranch() string {
	if s.BareRepo != "" && (s.DefaultBranch == "" || !s.branchExists(s.DefaultBranch)) {
		if names, err := s.branchNames(); err == nil && len(names) > 0 {
			s.DefaultBranch = names[0]
			s.setHEAD(s.DefaultBranch)
		}
	}
	return s.DefaultBranch
}

// commitsBetween is the real `git log base..head` against BareRepo, in
// GitHub's own compare order (oldest first) and shape (sha, message,
// parents) -- answered honestly for the same reason branchHead is: what
// a pull request's description gets built from is exactly the commits an
// agent really pushed, and a canned list would let a test pass on a
// description assembled from commits nobody wrote.
//
// ok is false when either ref is missing, which is real GitHub's own 404
// here.
func (s *Sim) commitsBetween(base, head string) ([]map[string]any, bool) {
	baseRev, okBase := s.compareRev(base)
	headRev, okHead := s.compareRev(head)
	if !okBase || !okHead {
		return nil, false
	}
	// %x00 between the fields and %x1e between commits: a commit message
	// is multi-line by design, so nothing line-oriented can frame it.
	out, err := exec.Command("git", "--git-dir", s.BareRepo, "log", "--reverse",
		"--format=%H%x00%P%x00%B%x1e", "--end-of-options", baseRev+".."+headRev).Output()
	if err != nil {
		return nil, false
	}
	commits := []map[string]any{}
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.SplitN(record, "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		parents := []map[string]any{}
		for _, p := range strings.Fields(fields[1]) {
			parents = append(parents, map[string]any{"sha": p})
		}
		commits = append(commits, map[string]any{
			"sha":     fields[0],
			"commit":  map[string]any{"message": strings.TrimRight(fields[2], "\n")},
			"parents": parents,
		})
	}
	return commits, true
}

// comparisonStanding is the rest of what GitHub's compare endpoint says
// about two branches: the commit they last had in common, and its own
// word for how head stands relative to base -- "identical", "behind"
// (base already contains head, which is what a merged branch looks like),
// "ahead" or "diverged".
//
// Derived from the real bare repository for commitsBetween's reason: the
// one reader of these two fields is grain's pull-request view working out
// where a branch forked from its base, and a canned answer would let that
// pass here while going blank against real GitHub.
//
// Either side may be a commit SHA rather than a branch, as on GitHub
// (compareRev): the merge queue's overlap re-check compares a pull
// request's head commit with its branch's tip, and the tip with the
// commit the two forked from.
func (s *Sim) comparisonStanding(base, head string) (string, string) {
	baseRev, okBase := s.compareRev(base)
	headRev, okHead := s.compareRev(head)
	if !okBase || !okHead {
		return "", ""
	}
	out, err := exec.Command("git", "--git-dir", s.BareRepo, "merge-base",
		"--end-of-options", baseRev, headRev).Output()
	if err != nil {
		return "", ""
	}
	mergeBase := strings.TrimSpace(string(out))
	baseSHA, headSHA := s.revSHA(baseRev), s.revSHA(headRev)
	if baseSHA == "" || headSHA == "" {
		return mergeBase, ""
	}
	switch {
	case mergeBase == baseSHA && mergeBase == headSHA:
		return mergeBase, "identical"
	case mergeBase == headSHA:
		return mergeBase, "behind"
	case mergeBase == baseSHA:
		return mergeBase, "ahead"
	default:
		return mergeBase, "diverged"
	}
}

// comparedFiles is the compare endpoint's own "files" list: the real
// `git diff --raw base...head` against BareRepo, each path with the blob
// head leaves there, in GitHub's shape (filename, status, sha). Renames
// are not detected, so a rename reads as a removal and an addition --
// a shape GitHub itself produces too, and one every reader of the list
// has to handle anyway.
func (s *Sim) comparedFiles(base, head string) []map[string]any {
	files := []map[string]any{}
	baseRev, okBase := s.compareRev(base)
	headRev, okHead := s.compareRev(head)
	if !okBase || !okHead {
		return files
	}
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "diff", "--raw", "--no-abbrev",
		"--no-renames", "-z", "--end-of-options", baseRev+"..."+headRev)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return files
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		// ":100644 100644 <old> <new> M"
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 {
			continue
		}
		files = append(files, map[string]any{
			"filename": fields[i+1],
			"status":   fileStatus(meta[4]),
			"sha":      meta[3],
		})
	}
	return files
}

// diffMediaType is the Accept header asking GitHub for a raw diff rather
// than JSON (github.RESTClient.CompareDiff, PullRequestDiff).
const diffMediaType = "application/vnd.github.diff"

// diffBetween is the real `git diff base...head` against BareRepo: the
// raw diff GitHub serves for a comparison, or for a pull request's own
// changes, when asked for diffMediaType.
func (s *Sim) diffBetween(base, head string) (string, bool) {
	baseRev, okBase := s.compareRev(base)
	headRev, okHead := s.compareRev(head)
	if !okBase || !okHead {
		return "", false
	}
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "diff", "--full-index", "-M",
		"--end-of-options", baseRev+"..."+headRev)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (s *Sim) readFileAt(ref, filePath string) ([]byte, bool) {
	if s.BareRepo == "" {
		return nil, false
	}
	clean := strings.TrimPrefix(strings.TrimSpace(filePath), "/")
	if clean == "" {
		return nil, false
	}
	rev, ok := s.compareRev(ref)
	if !ok {
		return nil, false
	}
	spec := rev + ":" + clean
	typeCmd := exec.Command("git", "--git-dir", s.BareRepo, "cat-file", "-t", "--end-of-options", spec)
	typeCmd.Env = os.Environ()
	typeOut, err := typeCmd.Output()
	if err != nil || strings.TrimSpace(string(typeOut)) != "blob" {
		return nil, false
	}
	cmd := exec.Command("git", "--git-dir", s.BareRepo, "cat-file", "blob", "--end-of-options", spec)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// filesChangedIn is the real `git diff --name-status -M base...head`
// against BareRepo: the paths pr touches, in GitHub's own shape for the
// files endpoint (a bare array of objects with a filename and status each,
// and a renamed file's old path in previous_filename).
//
// Derived rather than seeded, for commitsBetween's reason above: which
// files a pull request touches is a fact about the branch an agent
// really pushed, and BareRepo has that branch. A seeded list would let a
// merge-queue test assert that grain repaired a workflow file nobody
// had edited -- which is precisely the distinction the caller of this
// endpoint exists to make, so a test making it against canned data would
// be asserting nothing.
//
// Three dots, not two: GitHub lists a pull request's files against the
// merge base, so a head that is merely behind its base does not read as
// touching everything that landed on the base meanwhile.
//
// ok is false when either branch is missing, which is real GitHub's own
// 404 here.
func (s *Sim) filesChangedIn(pr PullRequest) ([]map[string]any, bool) {
	if !s.branchExists(pr.Base) || !s.branchExists(pr.Head) {
		return nil, false
	}
	out, err := exec.Command("git", "--git-dir", s.BareRepo, "diff", "--name-status", "-M", "-z",
		"refs/heads/"+pr.Base+"...refs/heads/"+pr.Head).Output()
	if err != nil {
		return nil, false
	}
	files := []map[string]any{}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status := fields[i]
		file := map[string]any{"filename": fields[i+1], "status": fileStatus(status)}
		// A rename (or copy) names its old path first and its new one
		// second, and GitHub lists it under the new path with the old in
		// previous_filename -- the field the security hold reads so that
		// moving a file out of a sensitive directory still counts.
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && i+2 < len(fields) {
			file["previous_filename"] = fields[i+1]
			file["filename"] = fields[i+2]
			i++
		}
		files = append(files, file)
	}
	return files, true
}

// fileStatus is git's --name-status letter in GitHub's own words for the
// files endpoint's status field.
func fileStatus(status string) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return "added"
	case strings.HasPrefix(status, "D"):
		return "removed"
	case strings.HasPrefix(status, "R"):
		return "renamed"
	case strings.HasPrefix(status, "C"):
		return "copied"
	default:
		return "modified"
	}
}

// branchSHA is branchHead's first half on its own: branch's tip, or ""
// if there is no such branch. Separate because the pull-request detail
// endpoint needs the sha on every read and nothing else -- branchHead
// would fork git three times (an existence check, rev-parse, and a log
// for a commit message with no reader) where `rev-parse --verify` on its
// own answers both questions in one, and a load test reading a pull
// request in a loop pays that difference.
func (s *Sim) branchSHA(branch string) string {
	out, err := exec.Command("git", "--git-dir", s.BareRepo,
		"rev-parse", "-q", "--verify", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refSHA is what ref -- a full ref name, inside refs/heads or not --
// points at, or "" when there is no such ref.
func (s *Sim) refSHA(ref string) string {
	sha, err := s.gitOutput(nil, nil, "rev-parse", "-q", "--verify", "--end-of-options", ref)
	if err != nil {
		return ""
	}
	return sha
}

// gitOutput runs git against BareRepo with stdin and env added to this
// process's environment, and returns what it printed, trimmed.
func (s *Sim) gitOutput(stdin []byte, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--git-dir", s.BareRepo}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("githubsim: git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// createGitObject is the git-database API's POST to blobs, trees or
// commits, written into BareRepo for real so that a test can read back
// exactly what was published: hash-object, mktree and commit-tree, the
// plumbing GitHub's own endpoints stand for.
func (s *Sim) createGitObject(kind string, body []byte) (string, error) {
	switch kind {
	case "blobs":
		var payload struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", err
		}
		content := []byte(payload.Content)
		if payload.Encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(payload.Content)
			if err != nil {
				return "", err
			}
			content = decoded
		}
		return s.gitOutput(content, nil, "hash-object", "-w", "--stdin")
	case "trees":
		var payload struct {
			Tree []struct {
				Path, Mode, Type, SHA string
			} `json:"tree"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", err
		}
		var lines strings.Builder
		for _, e := range payload.Tree {
			fmt.Fprintf(&lines, "%s %s %s\t%s\n", e.Mode, e.Type, e.SHA, e.Path)
		}
		return s.gitOutput([]byte(lines.String()), nil, "mktree")
	default:
		var payload struct {
			Message string   `json:"message"`
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
			Author  struct {
				Name, Email, Date string
			} `json:"author"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", err
		}
		args := []string{"commit-tree", payload.Tree, "-m", payload.Message}
		for _, parent := range payload.Parents {
			args = append(args, "-p", parent)
		}
		a := payload.Author
		return s.gitOutput(nil, []string{
			"GIT_AUTHOR_NAME=" + a.Name, "GIT_AUTHOR_EMAIL=" + a.Email, "GIT_AUTHOR_DATE=" + a.Date,
			"GIT_COMMITTER_NAME=" + a.Name, "GIT_COMMITTER_EMAIL=" + a.Email, "GIT_COMMITTER_DATE=" + a.Date,
		}, args...)
	}
}

// setBranch points branch at sha -- update-ref both creates a ref that
// doesn't yet exist and moves one that does, which is what lets Sim
// answer CreateBranch and UpdateBranch with the same underlying command;
// real GitHub's own git-database API is the two different endpoints
// github.go's own doc comments on those methods explain.
func (s *Sim) setBranch(branch, sha string) error {
	return runGit(s.BareRepo, "git", "--git-dir", s.BareRepo, "update-ref", "refs/heads/"+branch, sha)
}

// deleteBranch removes a ref, the other half of setBranch: real GitHub's
// DELETE on the git-database endpoint, which is what the merge queue does
// to its own throwaway ref when it drops a batch part-way through merging.
func (s *Sim) deleteBranch(branch string) error {
	return runGit(s.BareRepo, "git", "--git-dir", s.BareRepo, "update-ref", "-d", "refs/heads/"+branch)
}

// mergeIntoBase performs pr's merge for real, at the git level, against
// BareRepo -- real GitHub's own PUT .../merge endpoint moves commits into
// the base branch as a side effect, not just github.PullRequestDetail's
// State; a Sim that only flipped State to "closed" would let a caller
// believe a merge landed when the base branch never actually moved, which
// is exactly the gap an end-to-end test asserting on the base branch's
// own git log (e2e's own harness already does this for a human's
// merge via mergeBranchIntoDefault) would otherwise never catch for the
// auto-merge path, where nothing else performs the git side of the merge
// on grain's behalf.
func (s *Sim) mergeIntoBase(pr PullRequest, message string) error {
	dir, err := os.MkdirTemp("", "githubsim-merge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	wd := filepath.Join(dir, "work")
	if err := runGit(dir, "git", "clone", "-q", s.BareRepo, wd); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"git", "config", "user.email", "github@example.com"},
		{"git", "config", "user.name", "github (simulated merge)"},
		{"git", "fetch", "-q", "origin", pr.Head},
		{"git", "checkout", "-q", pr.Base},
		{"git", "merge", "--no-ff", "origin/" + pr.Head, "-m", message},
		{"git", "push", "-q", "origin", pr.Base},
	} {
		if err := runGit(wd, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sim) closeLinkedIssues(pr PullRequest) {
	matches := issueDirectiveRe.FindAllStringSubmatch(pr.Body, -1)
	for _, m := range matches {
		if len(m) > 1 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				if issue, ok := s.Issues[n]; ok {
					issue.State = "closed"
				}
			}
		}
	}
}

// containsBranch reports whether base's own history already contains
// head's tip -- the question real GitHub's merges endpoint answers 204
// for, asked here the honest way (`git merge-base --is-ancestor` against
// the bare repo) rather than from bookkeeping, since "was this branch
// actually behind" is precisely what a caller of that endpoint is
// relying on it for.
func (s *Sim) containsBranch(base, head string) bool {
	return exec.Command("git", "--git-dir", s.BareRepo, "merge-base", "--is-ancestor",
		"refs/heads/"+head, "refs/heads/"+base).Run() == nil
}

// mergeBranches merges head into base for real, against BareRepo, the
// same way mergeIntoBase performs a pull request's own merge -- real
// GitHub's POST /merges writes a merge commit onto base, so a Sim that
// only said it had would let the merge queue believe it refreshed a
// branch that never moved, which is the whole thing the caller then waits
// a cycle for fresh CI on.
//
// The second return is a conflict rather than an error: once both
// branches are known to exist (the caller 404s first) and base is known
// not to contain head already, a conflict is the only way this merge can
// fail, and it is an answer -- real GitHub's own 409 -- not a fault. That
// is the case a test most wants to be real, since it is the one the merge
// queue cannot repair by itself.
func (s *Sim) mergeBranches(base, head, message string) (sha string, conflicted bool, conflictingFiles []string, err error) {
	dir, err := os.MkdirTemp("", "githubsim-merges-*")
	if err != nil {
		return "", false, nil, err
	}
	defer os.RemoveAll(dir)
	wd := filepath.Join(dir, "work")
	if err := runGit(dir, "git", "clone", "-q", s.BareRepo, wd); err != nil {
		return "", false, nil, err
	}
	for _, args := range [][]string{
		{"git", "config", "user.email", "github@example.com"},
		{"git", "config", "user.name", "github (simulated merge)"},
		{"git", "fetch", "-q", "origin", head},
		{"git", "checkout", "-q", base},
	} {
		if err := runGit(wd, args[0], args[1:]...); err != nil {
			return "", false, nil, err
		}
	}
	if err := runGit(wd, "git", "merge", "--no-ff", "origin/"+head, "-m", message); err != nil {
		out, _ := exec.Command("git", "-C", wd, "diff", "--name-only", "--diff-filter=U").Output()
		var files []string
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f != "" {
				files = append(files, f)
			}
		}
		return "", true, files, nil
	}
	if err := runGit(wd, "git", "push", "-q", "origin", base); err != nil {
		return "", false, nil, err
	}
	return s.branchSHA(base), false, nil, nil
}

// runGit runs a git command in dir, folding its combined output into the
// returned error so a merge conflict or missing branch -- the only ways
// this can fail -- is diagnosable from the *github.Error mergeIntoBase's
// caller turns it into, the same as a real 405 response body would be.
func runGit(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("githubsim: %s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// unescapeRef undoes whatever percent-encoding a client applied to a
// branch name in a path, leaving it alone when it decodes to nothing
// valid -- the same tolerance every other ref-taking endpoint here shows.
func unescapeRef(ref string) string {
	decoded, err := url.PathUnescape(ref)
	if err != nil {
		return ref
	}
	return decoded
}

func splitPathQuery(path string) (string, url.Values) {
	p, rawQuery, _ := strings.Cut(path, "?")
	qs, _ := url.ParseQuery(rawQuery)
	return p, qs
}

func issueJSON(number int, owner, repo string, issue *Issue) map[string]any {
	labels := make([]string, 0, len(issue.Labels))
	for l := range issue.Labels {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	labelObjs := make([]map[string]string, len(labels))
	for i, l := range labels {
		labelObjs[i] = map[string]string{"name": l}
	}
	state := issue.State
	if state == "" {
		state = "open"
	}
	return map[string]any{
		"number": number, "title": issue.Title, "body": issue.Body,
		"html_url": fmt.Sprintf("https://github.example/%s/%s/issues/%d", owner, repo, number),
		"labels":   labelObjs, "state": state,
		"user": map[string]string{"login": issue.Author},
	}
}

// ReachableRepo is one repository a token can reach (Sim.Reachable), and
// whether it may push there.
type ReachableRepo struct {
	FullName string
	Push     bool
}

// reachableRepos answers /installation/repositories (an object wrapping
// the list) and /user/repos (the bare list), one page of them at a time.
func (s *Sim) reachableRepos(path string, qs url.Values, headers map[string]string) github.ApiResponse {
	token := strings.TrimPrefix(headers["Authorization"], "token ")
	repos, ok := s.Reachable[token]
	if !ok {
		return jsonResponse(401, map[string]string{"message": "Bad credentials"})
	}
	perPage, page := 30, 1
	if n, err := strconv.Atoi(qs.Get("per_page")); err == nil && n > 0 {
		perPage = n
	}
	if n, err := strconv.Atoi(qs.Get("page")); err == nil && n > 0 {
		page = n
	}
	list := []map[string]any{}
	for i := (page - 1) * perPage; i < len(repos) && i < page*perPage; i++ {
		list = append(list, map[string]any{
			"full_name":   repos[i].FullName,
			"permissions": map[string]bool{"pull": true, "push": repos[i].Push},
		})
	}
	if path == "/installation/repositories" {
		return jsonResponse(200, map[string]any{"total_count": len(repos), "repositories": list})
	}
	return jsonResponse(200, list)
}

// ReadFailure is one answer Sim gives instead of reading: a status and
// GitHub's own message under it. See Sim.FailReads.
type ReadFailure struct {
	Status  int
	Message string
}

func jsonResponse(status int, v any) github.ApiResponse {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return github.ApiResponse{Status: status, Body: body}
}

// Request implements github.Transport. It panics on any request shape it
// doesn't recognise, the same as v1's own RealGitHubMock raising
// AssertionError -- a test exercising an endpoint this double doesn't yet
// answer for should fail loudly, not silently get back a default "[]".
func (s *Sim) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Calls = append(s.Calls, Call{Method: method, Path: path})
	p, qs := splitPathQuery(path)

	if method == "GET" && s.FailReads != nil {
		// Counted as a call, above, because it is one: what a test
		// asserting on retries needs to see is that grain really did ask
		// again, not that it gave up quietly.
		return jsonResponse(s.FailReads.Status, map[string]string{"message": s.FailReads.Message}), nil
	}
	if method == "GET" && (p == "/installation/repositories" || p == "/user/repos") {
		return s.reachableRepos(p, qs, headers), nil
	}
	if method == "GET" {
		if m := pullReviewsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var reviews []map[string]any
			for _, login := range s.Approvals[mustAtoi(m[3])] {
				reviews = append(reviews, map[string]any{"user": map[string]string{"login": login}, "state": "APPROVED"})
			}
			return jsonResponse(200, append([]map[string]any{}, reviews...)), nil
		}
		if m := collaboratorPermissionRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			user, err := url.PathUnescape(m[3])
			if err != nil {
				user = m[3]
			}
			perm := "none"
			for k, v := range s.CollaboratorPermissions {
				if strings.EqualFold(k, user) {
					perm = v
					break
				}
			}
			return jsonResponse(200, map[string]any{"permission": perm}), nil
		}
		if m := pullRequestedReviewersRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			users, teams := []map[string]string{}, []map[string]string{}
			for _, who := range s.RequestedReviewers[mustAtoi(m[3])] {
				if _, team, ok := strings.Cut(who, "/"); ok {
					teams = append(teams, map[string]string{"slug": team})
				} else {
					users = append(users, map[string]string{"login": who})
				}
			}
			return jsonResponse(200, map[string]any{"users": users, "teams": teams}), nil
		}
		if m := branchRulesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			branch, err := url.PathUnescape(m[3])
			if err != nil {
				branch = m[3]
			}
			return jsonResponse(200, append([]map[string]any{}, s.BranchRules[branch]...)), nil
		}
		if m := rulesetRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			name, ok := s.RulesetNames[mustAtoi(m[3])]
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			return jsonResponse(200, map[string]any{"id": mustAtoi(m[3]), "name": name}), nil
		}
		if m := issueCommentsRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			return jsonResponse(200, commentsJSON(s.Comments[number])), nil
		}
		if m := commitPullsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			if n, ok := s.MergedPullRequestForCommit[m[3]]; ok {
				return jsonResponse(200, []map[string]any{{"number": n, "merged_at": "2026-01-01T00:00:00Z"}}), nil
			}
			return jsonResponse(200, []map[string]any{}), nil
		}
		if m := pullsRe.FindStringSubmatch(p); m != nil && qs.Get("head") != "" {
			s.mustOwn(m[1], m[2])
			return jsonResponse(200, s.findOpenPullRequestsForHead(m[1], qs.Get("head"))), nil
		}
		if m := pullRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			number := mustAtoi(m[3])
			pr, ok := s.pullRequest(number)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			if headers["Accept"] == diffMediaType {
				diff, ok := pr.mergedDiff, pr.merged
				if !ok {
					diff, ok = s.diffBetween(pr.Base, pr.Head)
				}
				if !ok {
					return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
				}
				return github.ApiResponse{Status: 200, Body: []byte(diff)}, nil
			}
			return jsonResponse(200, pullRequestDetailJSON(pr, s.branchSHA(pr.Head))), nil
		}
		if m := pullCommentsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			number := mustAtoi(m[3])
			return jsonResponse(200, reviewCommentsJSON(s.ReviewComments[number])), nil
		}
		if m := pullFilesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			pr, ok := s.pullRequest(mustAtoi(m[3]))
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			files, ok := s.filesChangedIn(pr)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			return jsonResponse(200, files), nil
		}
		if m := pullCommitsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			pr, ok := s.pullRequest(mustAtoi(m[3]))
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			if pr.mergedCommits != nil {
				return jsonResponse(200, pr.mergedCommits), nil
			}
			commits, ok := s.commitsBetween(pr.Base, pr.Head)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			return jsonResponse(200, commits), nil
		}
		if m := checkRunsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			ref, err := url.PathUnescape(m[3])
			if err != nil {
				ref = m[3]
			}
			return jsonResponse(200, checkRunsJSON(s.checkRunsFor(ref))), nil
		}
		if m := actionsRunsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			key := qs.Get("head_sha")
			// No commit to scope to is the repository-wide read
			// (github.RESTClient.RecentRunStarts / CompletedWorkflowRuns):
			// everything this repository has run lately, rather than one commit's runs.
			if key == "" {
				payload := s.repoRunsJSON(m[1], m[2])
				rawBytes, _ := json.Marshal(payload)
				var h uint64 = 14695981039346656037
				for _, b := range rawBytes {
					h ^= uint64(b)
					h *= 1099511628211
				}
				etag := fmt.Sprintf(`W/"%x-%016x"`, len(rawBytes), h)
				if headers["If-None-Match"] != "" && headers["If-None-Match"] == etag {
					return github.ApiResponse{Status: 304, Headers: map[string]string{"ETag": etag}}, nil
				}
				resp := jsonResponse(200, payload)
				resp.Headers = map[string]string{"ETag": etag}
				return resp, nil
			}
			if s.startupFailureFor(key) {
				return jsonResponse(200, startupFailureRunJSON(m[1], m[2], s.runIDFor(key))), nil
			}
			jobs := s.workflowJobsFor(key)
			return jsonResponse(200, workflowRunsJSON(m[1], m[2], s.runIDFor(key), jobs)), nil
		}
		if m := runJobsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			runID := mustAtoi(m[3])
			if wr, ok := s.workflowRunByID(int64(runID)); ok {
				return jsonResponse(200, jobsJSON(m[1], m[2], runID, wr.Jobs)), nil
			}
			key, ok := s.runKeyFor(runID)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			if s.startupFailureFor(key) {
				// A run that never started a job has none to list, which
				// is the whole shape of this case: GitHub answers with an
				// empty array rather than an error.
				return jsonResponse(200, jobsJSON(m[1], m[2], runID, nil)), nil
			}
			return jsonResponse(200, jobsJSON(m[1], m[2], runID, s.workflowJobsFor(key))), nil
		}
		if m := jobLogsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			id := mustAtoi(m[3])
			runID, index := id/1000, id%1000
			if wr, ok := s.workflowRunByID(int64(runID)); ok {
				if index >= len(wr.Jobs) || wr.Jobs[index].Log == "" {
					return github.ApiResponse{Status: 410, Body: []byte("{}")}, nil
				}
				return github.ApiResponse{Status: 200, Body: []byte(wr.Jobs[index].Log)}, nil
			}
			key, ok := s.runKeyFor(runID)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			jobs := s.workflowJobsFor(key)
			if index >= len(jobs) || jobs[index].Log == "" {
				// 410, not 404: this is the shape of a real log past its
				// retention window, and the one FailedJobLogs has to skip
				// rather than fail on.
				return github.ApiResponse{Status: 410, Body: []byte("{}")}, nil
			}
			// Plain text, like the real endpoint's own answer after the
			// redirect github's storage serves it from.
			return github.ApiResponse{Status: 200, Body: []byte(jobs[index].Log)}, nil
		}
		if m := checkRunAnnotationsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			// Nothing seeds annotations: a job's reason is seeded on the
			// check run's output (WorkflowJob.FailureReason), and this
			// endpoint exists so that the fallback read for a check run
			// with no output gets GitHub's own empty array rather than an
			// unhandled-request panic.
			return jsonResponse(200, []map[string]any{}), nil
		}
		if m := checkRunRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			job, ok := s.jobByID(mustAtoi(m[3]))
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			return jsonResponse(200, checkRunJSON(job)), nil
		}
		if m := issueRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			issue, ok := s.Issues[number]
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			return jsonResponse(200, issueJSON(number, s.Owner, s.Repo, issue)), nil
		}
		if m := repoRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			return jsonResponse(200, map[string]string{"default_branch": s.defaultBranch()}), nil
		}
		if m := issuesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			label := qs.Get("labels")
			numbers := make([]int, 0, len(s.Issues))
			for n := range s.Issues {
				numbers = append(numbers, n)
			}
			sort.Ints(numbers)
			items := make([]map[string]any, 0, len(numbers))
			for _, n := range numbers {
				issue := s.Issues[n]
				if label != "" {
					if _, ok := issue.Labels[label]; !ok {
						continue
					}
				}
				items = append(items, issueJSON(n, s.Owner, s.Repo, issue))
			}
			return jsonResponse(200, items), nil
		}
		if m := compareRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			base, head := unescapeRef(m[3]), unescapeRef(m[4])
			if headers["Accept"] == diffMediaType {
				diff, ok := s.diffBetween(base, head)
				if !ok {
					return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
				}
				return github.ApiResponse{Status: 200, Body: []byte(diff)}, nil
			}
			commits, ok := s.commitsBetween(base, head)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			mergeBase, status := s.comparisonStanding(base, head)
			return jsonResponse(200, map[string]any{
				"commits":           commits,
				"files":             s.comparedFiles(base, head),
				"status":            status,
				"merge_base_commit": map[string]any{"sha": mergeBase},
			}), nil
		}
		if m := branchesRe.FindStringSubmatch(p); m != nil {
			if m[1] != s.Owner || m[2] != s.Repo {
				// A repo this double does not stand for is one GitHub
				// would not show this credential: its own 404.
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			names, err := s.branchNames()
			if err != nil {
				return github.ApiResponse{}, err
			}
			branches := []map[string]any{}
			for _, name := range names {
				sha, _, _ := s.branchHead(name)
				branches = append(branches, map[string]any{"name": name, "commit": map[string]string{"sha": sha}})
			}
			return jsonResponse(200, branches), nil
		}
		if m := branchRe.FindStringSubmatch(p); m != nil {
			branch, err := url.PathUnescape(m[3])
			if err != nil {
				branch = m[3]
			}
			sha, message, ok := s.branchHead(branch)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			return jsonResponse(200, map[string]any{
				"commit": map[string]any{
					"sha":    sha,
					"commit": map[string]string{"message": message},
				},
			}), nil
		}
		if m := contentsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			filePath := unescapeRef(m[3])
			ref := qs.Get("ref")
			if ref == "" {
				ref = s.defaultBranch()
			}
			data, ok := s.readFileAt(ref, filePath)
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			if strings.Contains(headers["Accept"], "raw") {
				return github.ApiResponse{Status: 200, Body: data}, nil
			}
			return jsonResponse(200, map[string]any{
				"type":     "file",
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString(data),
			}), nil
		}
	}

	if method == "POST" && p == "/graphql" {
		return s.graphql(body)
	}
	if method == "POST" {
		if m := pullCommentRepliesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			number, to := mustAtoi(m[3]), mustAtoi(m[4])
			var root *github.ReviewComment
			for i, c := range s.ReviewComments[number] {
				if c.ID == to {
					root = &s.ReviewComments[number][i]
				}
			}
			if root == nil {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			rootID := root.ID
			if root.InReplyTo != 0 {
				rootID = root.InReplyTo
			}
			s.nextCommentID++
			reply := github.ReviewComment{ID: s.nextCommentID, User: "grain-agent", Body: payload.Body,
				Path: root.Path, Line: root.Line, InReplyTo: rootID}
			s.ReviewComments[number] = append(s.ReviewComments[number], reply)
			return jsonResponse(201, map[string]any{"id": reply.ID, "body": reply.Body}), nil
		}
		if m := issueCommentsRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			s.nextCommentID++
			comment := github.Comment{ID: s.nextCommentID, User: "grain-agent", Body: payload.Body, AuthorAssociation: "NONE"}
			s.Comments[number] = append(s.Comments[number], comment)
			return jsonResponse(201, map[string]any{"id": comment.ID, "body": comment.Body}), nil
		}
		if m := issuesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Title  string   `json:"title"`
				Body   string   `json:"body"`
				Labels []string `json:"labels"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			s.nextIssue++
			number := s.nextIssue
			issue := &Issue{Title: payload.Title, Body: payload.Body, Labels: labelSetFrom(payload.Labels)}
			s.Issues[number] = issue
			return jsonResponse(201, issueJSON(number, s.Owner, s.Repo, issue)), nil
		}
		if m := labelsPostRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			var payload struct {
				Labels []string `json:"labels"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if s.RefuseLabels != 0 {
				return jsonResponse(s.RefuseLabels, map[string]string{"message": "Resource not accessible by integration"}), nil
			}
			var labels map[string]struct{}
			if issue := s.Issues[number]; issue != nil {
				if issue.Labels == nil {
					issue.Labels = map[string]struct{}{}
				}
				labels = issue.Labels
			} else {
				for i := range s.PullRequests {
					if s.PullRequests[i].Number == number {
						if s.PullRequests[i].Labels == nil {
							s.PullRequests[i].Labels = map[string]struct{}{}
						}
						labels = s.PullRequests[i].Labels
					}
				}
			}
			if labels == nil {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			for _, l := range payload.Labels {
				labels[l] = struct{}{}
			}
			return github.ApiResponse{Status: 200, Body: []byte("[]")}, nil
		}
		if m := pullsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Title string `json:"title"`
				Head  string `json:"head"`
				Base  string `json:"base"`
				Body  string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			// Real GitHub refuses a base that is not a branch, every
			// time, with exactly this 422 -- and that refusal is the
			// whole reason orchestrator.pullRequestBase exists: a task
			// whose base merged and was deleted could never be finished,
			// because the one call that turns its pushed branch into a
			// pull request was declined on every attempt. A sim that
			// accepted any base at all was why nothing here ever saw it.
			if !s.branchExists(payload.Base) {
				return jsonResponse(422, map[string]any{
					"message": "Validation Failed",
					"errors": []map[string]any{{
						"resource": "PullRequest", "field": "base", "code": "invalid",
					}},
				}), nil
			}
			// The sibling refusal, just as permanent and just as
			// unhandled until now: real GitHub declines a head that
			// carries nothing its base does not already have -- a branch
			// whose run reverted its own work, committed only what the
			// base had, or never committed at all. A sim that opened a
			// pull request for such a branch is why nothing here ever saw
			// the loop that produced (orchestrator.noteEmptyBranch has
			// the account of it). The message is GitHub's own, and it is
			// the whole of what distinguishes this 422 from the one above
			// -- github.IsNoCommitsBetween reads exactly this text.
			if s.containsBranch(payload.Base, payload.Head) {
				return jsonResponse(422, map[string]any{
					"message": "Validation Failed",
					"errors": []map[string]any{{
						"resource": "PullRequest", "code": "custom",
						"message": fmt.Sprintf("No commits between %s and %s", payload.Base, payload.Head),
					}},
				}), nil
			}
			base := s.PullRequestBase
			if base == 0 {
				base = 9000
			}
			number := base + len(s.PullRequests)
			pr := PullRequest{
				Number: number, Title: payload.Title, Body: payload.Body,
				Head: payload.Head, Base: payload.Base, State: "open",
				HTMLURL:   fmt.Sprintf("https://github.example/%s/%s/pull/%d", s.Owner, s.Repo, number),
				CreatedAt: s.now(),
			}
			s.PullRequests = append(s.PullRequests, pr)
			return jsonResponse(201, map[string]any{
				"number": pr.Number, "title": pr.Title, "body": pr.Body,
				"head": pr.Head, "base": pr.Base, "html_url": pr.HTMLURL,
			}), nil
		}
		if m := pullReviewsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			number := mustAtoi(m[3])
			var payload struct {
				Event    string `json:"event"`
				Body     string `json:"body"`
				Comments []struct {
					Path string `json:"path"`
					Line int    `json:"line"`
					Body string `json:"body"`
				} `json:"comments"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			comments := make([]github.NewReviewComment, len(payload.Comments))
			for i, c := range payload.Comments {
				comments[i] = github.NewReviewComment{Path: c.Path, Line: c.Line, Body: c.Body}
			}
			s.nextReviewID++
			s.Reviews = append(s.Reviews, Review{Number: number, Event: payload.Event, Body: payload.Body, Comments: comments})
			if (payload.Event != "" || s.conversationResolutionRequiredForPR(number)) && len(comments) > 0 {
				if s.ReviewComments == nil {
					s.ReviewComments = map[int][]github.ReviewComment{}
				}
				for _, c := range comments {
					s.nextCommentID++
					line := c.Line
					s.ReviewComments[number] = append(s.ReviewComments[number], github.ReviewComment{
						ID:       s.nextCommentID,
						User:     "grain-agent",
						ReviewID: s.nextReviewID,
						Path:     c.Path,
						Line:     &line,
						Body:     c.Body,
					})
				}
			}
			return jsonResponse(200, map[string]any{"id": s.nextReviewID}), nil
		}
		if m := issueCommentReactionsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			s.CommentReactions = append(s.CommentReactions, CommentReaction{
				CommentID: int64(mustAtoi(m[3])),
				Content:   payload.Content,
			})
			return jsonResponse(201, map[string]any{"id": len(s.CommentReactions), "content": payload.Content}), nil
		}
		if m := pullCommentReactionsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			s.CommentReactions = append(s.CommentReactions, CommentReaction{
				CommentID: int64(mustAtoi(m[3])),
				Content:   payload.Content,
				Review:    true,
			})
			return jsonResponse(201, map[string]any{"id": len(s.CommentReactions), "content": payload.Content}), nil
		}
		if m := createCheckRunsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Name       string `json:"name"`
				HeadSHA    string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				DetailsURL string `json:"details_url"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			s.nextCheckRunID++
			id := 8000 + s.nextCheckRunID
			if s.CheckRuns == nil {
				s.CheckRuns = map[string][]github.CheckRun{}
			}
			cr := github.CheckRun{
				Name:    payload.Name,
				Status:  payload.Status,
				HTMLURL: payload.DetailsURL,
			}
			if cr.Status == "" {
				cr.Status = "queued"
			}
			if payload.Conclusion != "" {
				conc := payload.Conclusion
				cr.Conclusion = &conc
			}
			s.CheckRuns[payload.HeadSHA] = append(s.CheckRuns[payload.HeadSHA], cr)
			return jsonResponse(201, map[string]any{"id": id}), nil
		}
		if m := mergesRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Base          string `json:"base"`
				Head          string `json:"head"`
				CommitMessage string `json:"commit_message"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			// A branch that is not in this repository is real GitHub's own
			// 404 here -- what a fork pull request's head reads as, and
			// what the caller has to treat as "cannot refresh this one".
			if !s.branchExists(payload.Base) || !s.branchExists(payload.Head) {
				return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
			}
			if s.containsBranch(payload.Base, payload.Head) {
				return github.ApiResponse{Status: 204, Body: nil}, nil
			}
			message := payload.CommitMessage
			if message == "" {
				message = fmt.Sprintf("Merge %s into %s", payload.Head, payload.Base)
			}
			sha, conflicted, files, err := s.mergeBranches(payload.Base, payload.Head, message)
			if err != nil {
				return github.ApiResponse{Status: 500, Body: []byte(err.Error())}, nil
			}
			if conflicted {
				respBody, _ := json.Marshal(map[string]any{
					"message": "Merge conflict",
					"files":   files,
				})
				return github.ApiResponse{Status: 409, Body: respBody}, nil
			}
			return jsonResponse(201, map[string]any{"sha": sha}), nil
		}
		if m := gitObjectsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			sha, err := s.createGitObject(m[3], body)
			if err != nil {
				return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
			}
			return jsonResponse(201, map[string]string{"sha": sha}), nil
		}
		if m := gitRefsRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if !strings.HasPrefix(payload.Ref, "refs/heads/") {
				if s.refSHA(payload.Ref) != "" {
					return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Reference already exists"}`)}, nil
				}
				if err := runGit(s.BareRepo, "git", "--git-dir", s.BareRepo, "update-ref", payload.Ref, payload.SHA); err != nil {
					return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
				}
				return jsonResponse(201, map[string]string{"ref": payload.Ref, "object.sha": payload.SHA}), nil
			}
			branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
			if s.branchExists(branch) {
				return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Reference already exists"}`)}, nil
			}
			if err := s.setBranch(branch, payload.SHA); err != nil {
				return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
			}
			return jsonResponse(201, map[string]string{"ref": payload.Ref, "object.sha": payload.SHA}), nil
		}
	}

	if method == "PATCH" {
		if m := updateCheckRunRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				DetailsURL string `json:"details_url"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			for sha, runs := range s.CheckRuns {
				for i := range runs {
					if payload.Name == "" || runs[i].Name == payload.Name {
						if payload.Status != "" {
							s.CheckRuns[sha][i].Status = payload.Status
						}
						if payload.Conclusion != "" {
							conc := payload.Conclusion
							s.CheckRuns[sha][i].Conclusion = &conc
						}
						if payload.DetailsURL != "" {
							s.CheckRuns[sha][i].HTMLURL = payload.DetailsURL
						}
					}
				}
			}
			return jsonResponse(200, map[string]any{"id": mustAtoi(m[3])}), nil
		}
		if m := issueCommentRe.FindStringSubmatch(p); m != nil {
			id := mustAtoi(m[3])
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			for number, comments := range s.Comments {
				for i := range comments {
					if comments[i].ID == id {
						s.Comments[number][i].Body = payload.Body
						return jsonResponse(200, map[string]any{"id": id, "body": payload.Body}), nil
					}
				}
			}
			return github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)}, nil
		}
		// The three fields of a pull request anything here edits: its
		// body (github.RESTClient.UpdatePullRequestBody, rewriting a
		// description for a branch that has moved), its title
		// (github.RESTClient.UpdatePullRequestTitle, renaming one for
		// the same reason) and its state
		// (github.RESTClient.ClosePullRequest, closing one without
		// merging it). A PATCH carrying none of them falls through to the
		// unhandled-request panic below rather than being silently
		// accepted and ignored, which is the whole value of that panic.
		if m := pullRe.FindStringSubmatch(p); m != nil {
			var payload struct {
				Title *string `json:"title"`
				Body  *string `json:"body"`
				State *string `json:"state"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if payload.Title != nil || payload.Body != nil || payload.State != nil {
				s.mustOwn(m[1], m[2])
				number := mustAtoi(m[3])
				for i := range s.PullRequests {
					if s.PullRequests[i].Number != number {
						continue
					}
					if payload.Title != nil {
						s.PullRequests[i].Title = *payload.Title
					}
					if payload.Body != nil {
						s.PullRequests[i].Body = *payload.Body
					}
					// State moves and nothing else does: closing a pull
					// request on GitHub leaves the branch, the commits
					// and Merged exactly as they were -- which is the
					// whole reason a human is offered the choice at all,
					// and the thing a test asserting "the work is still
					// there" has to be able to see.
					if payload.State != nil {
						s.PullRequests[i].State = *payload.State
					}
					return jsonResponse(200, pullRequestDetailJSON(
						s.PullRequests[i], s.branchSHA(s.PullRequests[i].Head))), nil
				}
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
		}
		if m := issueRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			var payload struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			issue, ok := s.Issues[number]
			if !ok {
				return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
			}
			issue.State = payload.State
			return jsonResponse(200, issueJSON(number, s.Owner, s.Repo, issue)), nil
		}
		if m := repoRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				DefaultBranch *string `json:"default_branch"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if payload.DefaultBranch != nil {
				branch := strings.TrimSpace(*payload.DefaultBranch)
				if branch == "" || (s.BareRepo != "" && !s.branchExists(branch)) {
					return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Validation Failed"}`)}, nil
				}
				s.DefaultBranch = branch
				s.setHEAD(branch)
				return jsonResponse(200, map[string]string{"default_branch": s.DefaultBranch}), nil
			}
		}
		if m := gitRefRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			branch, err := url.PathUnescape(m[3])
			if err != nil {
				branch = m[3]
			}
			var payload struct {
				SHA   string `json:"sha"`
				Force bool   `json:"force"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if !s.branchExists(branch) {
				return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Reference does not exist"}`)}, nil
			}
			if err := s.setBranch(branch, payload.SHA); err != nil {
				return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
			}
			return jsonResponse(200, map[string]string{"ref": "refs/heads/" + branch, "object.sha": payload.SHA}), nil
		}
		if m := gitOtherRefRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			var payload struct {
				SHA string `json:"sha"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				return github.ApiResponse{}, err
			}
			if s.refSHA(m[3]) == "" {
				return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Reference does not exist"}`)}, nil
			}
			if err := runGit(s.BareRepo, "git", "--git-dir", s.BareRepo, "update-ref", m[3], payload.SHA); err != nil {
				return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
			}
			return jsonResponse(200, map[string]string{"ref": m[3], "object.sha": payload.SHA}), nil
		}
	}

	if method == "PUT" {
		if m := pullMergeRe.FindStringSubmatch(p); m != nil {
			s.mustOwn(m[1], m[2])
			number := mustAtoi(m[3])
			var payload struct {
				SHA           string `json:"sha"`
				CommitTitle   string `json:"commit_title"`
				CommitMessage string `json:"commit_message"`
			}
			if len(body) > 0 {
				if err := json.Unmarshal(body, &payload); err != nil {
					return github.ApiResponse{}, err
				}
			}
			for i := range s.PullRequests {
				if s.PullRequests[i].Number == number {
					// GitHub's own `sha` parameter: "SHA that pull
					// request head must match to allow merge", answered
					// 409 when it does not. A caller that merges on a
					// verdict it computed from one commit's CI sends it
					// (orchestrator.syncEntry), and a branch that moved
					// since then has to be refused here rather than
					// merged, or a test can never tell the two apart.
					if payload.SHA != "" && payload.SHA != s.branchSHA(s.PullRequests[i].Head) {
						return github.ApiResponse{Status: 409, Body: []byte(
							`{"message":"Head branch was modified. Review and try the merge again."}`)}, nil
					}
					// A refusal GitHub makes before it looks at the git at
					// all: the rules said no (RefuseMerges).
					if s.RefuseMerges != "" {
						return jsonResponse(405, map[string]string{"message": s.RefuseMerges}), nil
					}
					if s.RequireMergeQueue {
						return jsonResponse(405, map[string]string{"message": "Repository rule violations found\n\n" +
							"Changes must be made through the merge queue"}), nil
					}
					if s.RequireApprovals > 0 && len(s.Approvals[number]) < s.RequireApprovals {
						return jsonResponse(405, map[string]string{"message": fmt.Sprintf(
							"Repository rule violations found\n\nReview required: at least %d approving review is required by reviewers with write access.",
							s.RequireApprovals)}), nil
					}
					if s.conversationResolutionRequired(s.PullRequests[i].Base) && s.hasUnresolvedReviewThreads(number) {
						return jsonResponse(405, map[string]string{"message": "Repository rule violations found\n\n" +
							"All comments must be resolved."}), nil
					}
					// GitHub's own message: commit_title (its default
					// when empty), a blank line, then commit_message.
					title := payload.CommitTitle
					if title == "" {
						title = fmt.Sprintf("Merge pull request #%d from %s", number, s.PullRequests[i].Head)
					}
					message := title
					if payload.CommitMessage != "" {
						message += "\n\n" + payload.CommitMessage
					}
					if payload.CommitTitle != "" || payload.CommitMessage != "" {
						s.PullRequests[i].MergeMessage = message
					}
					if err := s.land(i, message); err != nil {
						return github.ApiResponse{Status: 405, Body: []byte(err.Error())}, nil
					}
					return github.ApiResponse{Status: 200, Body: []byte("{}")}, nil
				}
			}
			return github.ApiResponse{Status: 404, Body: []byte("{}")}, nil
		}
	}

	if method == "DELETE" {
		if m := labelDeleteRe.FindStringSubmatch(p); m != nil {
			number := mustAtoi(m[3])
			label, err := url.PathUnescape(m[4])
			if err != nil {
				label = m[4]
			}
			delete(s.Issues[number].Labels, label)
			return github.ApiResponse{Status: 200, Body: []byte("{}")}, nil
		}
		if m := gitRefRe.FindStringSubmatch(p); m != nil {
			// github.RESTClient.DeleteBranch: the merge queue destroying
			// its own throwaway batch ref. Done for real against BareRepo,
			// like every other ref write here, so a test can assert the ref
			// is gone rather than that grain said it would be.
			s.mustOwn(m[1], m[2])
			branch, err := url.PathUnescape(m[3])
			if err != nil {
				branch = m[3]
			}
			if !s.branchExists(branch) {
				return github.ApiResponse{Status: 422, Body: []byte(`{"message":"Reference does not exist"}`)}, nil
			}
			if err := s.deleteBranch(branch); err != nil {
				return github.ApiResponse{Status: 422, Body: []byte(err.Error())}, nil
			}
			return github.ApiResponse{Status: 204, Body: nil}, nil
		}
	}

	panic(fmt.Sprintf("githubsim: unhandled request %s %s", method, path))
}

// checkRunsFor answers GET .../commits/{ref}/check-runs for a ref that
// may be a branch name or a commit sha, whichever CheckRuns happens to be
// keyed by -- real GitHub resolves both to one commit, and BareRepo is
// where the same resolution lives here.
//
// It matters because the two ends disagree on purpose. A test seeds
// checks against a branch, which is the readable thing to write; the
// orchestrator asks by head sha, because a branch-scoped read answers for
// whatever the branch points at now rather than for the commit whose
// health it is deciding (orchestrator.checkRunsFor). Resolving here keeps
// those tests saying what they mean, while a test that seeds by sha --
// one about a branch that moves mid-cycle -- still gets an answer that is
// strictly about the commit it named, since a moved branch resolves to a
// different sha and finds nothing.
func (s *Sim) checkRunsFor(ref string) []github.CheckRun {
	if runs, ok := s.CheckRuns[ref]; ok {
		return runs
	}
	// ref is a branch whose checks were seeded by sha...
	if sha := s.branchSHA(ref); sha != "" {
		return s.CheckRuns[sha]
	}
	// ...or a sha whose checks were seeded by branch name. Keys in sorted
	// order so two branches sitting on one commit answer the same way
	// every run rather than however the map felt like iterating.
	keys := make([]string, 0, len(s.CheckRuns))
	for key := range s.CheckRuns {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if s.branchSHA(key) == ref {
			return s.CheckRuns[key]
		}
	}
	return nil
}

// pullRequest returns the pull request numbered number, if Sim has one.
// land merges s.PullRequests[i] into its base with message, as GitHub
// does on a merge it has agreed to.
func (s *Sim) land(i int, message string) error {
	own, _ := s.commitsBetween(s.PullRequests[i].Base, s.PullRequests[i].Head)
	diff, _ := s.diffBetween(s.PullRequests[i].Base, s.PullRequests[i].Head)
	if err := s.mergeIntoBase(s.PullRequests[i], message); err != nil {
		return err
	}
	s.PullRequests[i].mergedCommits = own
	s.PullRequests[i].mergedDiff, s.PullRequests[i].merged = diff, true
	s.PullRequests[i].State = "closed"
	s.PullRequests[i].Merged = true
	s.PullRequests[i].MergedAt = s.now()
	s.closeLinkedIssues(s.PullRequests[i])
	return nil
}

// LandMergeQueue is GitHub's merge queue passing every merge group in it:
// each queued pull request is merged, front first, and the queue emptied.
func (s *Sim) LandMergeQueue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := s.MergeQueue
	s.MergeQueue = nil
	for _, number := range queue {
		for i := range s.PullRequests {
			if s.PullRequests[i].Number == number {
				if err := s.land(i, fmt.Sprintf("Merge pull request #%d from %s", number, s.PullRequests[i].Head)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// DropFromMergeQueue is GitHub's merge queue taking a pull request out
// without merging it, as it does when the merge group's checks fail.
func (s *Sim) DropFromMergeQueue(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.MergeQueue[:0]
	for _, n := range s.MergeQueue {
		if n != number {
			kept = append(kept, n)
		}
	}
	s.MergeQueue = kept
}

func (s *Sim) mergeQueuePosition(number int) int {
	for i, n := range s.MergeQueue {
		if n == number {
			return i + 1
		}
	}
	return 0
}

func (s *Sim) pullRequest(number int) (PullRequest, bool) {
	for _, pr := range s.PullRequests {
		if pr.Number == number {
			return pr, true
		}
	}
	return PullRequest{}, false
}

// findOpenPullRequestsForHead answers GET .../pulls?head=owner:branch --
// github.RESTClient.FindOpenPullRequestForBranch's own request shape.
// GitHub allows at most one open PR per head branch, but this returns a
// list either way, matching the real endpoint's own shape (a filtered
// list, never a single object).
//
// The label's owner is matched the way GitHub matches it: against the
// owner of the fork a pull request's head lives in (HeadRepo), or repo's
// own owner for a head branch in repo itself.
func (s *Sim) findOpenPullRequestsForHead(owner, head string) []map[string]any {
	headOwner, branch, ok := strings.Cut(head, ":")
	if !ok {
		headOwner, branch = owner, head
	}
	var out []map[string]any
	for _, pr := range s.PullRequests {
		prOwner := owner
		if fork, _, ok := strings.Cut(pr.HeadRepo, "/"); ok {
			prOwner = fork
		}
		if pr.Head == branch && pr.State == "open" && strings.EqualFold(prOwner, headOwner) {
			out = append(out, map[string]any{
				"number": pr.Number, "title": pr.Title, "body": pr.Body,
				"head": pr.Head, "base": pr.Base, "html_url": pr.HTMLURL,
			})
		}
	}
	return out
}

// pullRequestDetailJSON is the shape github.RESTClient.GetPullRequest
// decodes -- head/base as nested {"ref": ...} objects, not the bare
// strings CreatePullRequest's own response uses, matching GitHub's own
// (inconsistent, but real) shape between the two endpoints.
//
// headSHA is the head branch's current tip, read out of the bare repo by
// the caller (there is no other record of it here: this sim stores a pull
// request's head as a branch name, and git is where that name resolves).
// Empty when the branch is gone -- a merged pull request whose head was
// deleted -- which is the same empty GitHub's own response can carry, and
// which orchestrator.checkRunsFor already reads as "no commit to scope
// to."
func pullRequestDetailJSON(pr PullRequest, headSHA string) map[string]any {
	state := pr.State
	if state == "" {
		state = "open"
	}
	out := map[string]any{
		"number": pr.Number, "title": pr.Title, "body": pr.Body, "html_url": pr.HTMLURL,
		"state":     state,
		"head":      map[string]string{"ref": pr.Head, "sha": headSHA},
		"base":      map[string]string{"ref": pr.Base},
		"mergeable": pr.Mergeable,
		"merged":    pr.Merged,
		// Always present, zeroes included: GitHub's own response carries
		// all three on every read of this endpoint, whatever they are.
		"additions":     pr.Additions,
		"deletions":     pr.Deletions,
		"changed_files": pr.ChangedFiles,
	}
	if pr.Author != "" {
		out["user"] = map[string]string{"login": pr.Author}
	}
	if pr.HeadRepo != "" {
		out["head"] = map[string]any{"ref": pr.Head, "sha": headSHA,
			"repo": map[string]string{"full_name": pr.HeadRepo}}
		out["maintainer_can_modify"] = pr.MaintainerCanModify
	}
	if !pr.CreatedAt.IsZero() {
		out["created_at"] = pr.CreatedAt.UTC().Format(time.RFC3339)
	}
	if pr.Merged && !pr.MergedAt.IsZero() {
		out["merged_at"] = pr.MergedAt.UTC().Format(time.RFC3339)
	}
	if pr.MergeCommitSHA != "" {
		out["merge_commit_sha"] = pr.MergeCommitSHA
	}
	return out
}

// commentsJSON is the shape github.RESTClient.ListComments decodes.
func commentsJSON(comments []github.Comment) []map[string]any {
	out := make([]map[string]any, len(comments))
	for i, c := range comments {
		out[i] = map[string]any{
			"id": c.ID, "body": c.Body, "author_association": c.AuthorAssociation,
			"user": map[string]string{"login": c.User},
		}
	}
	return out
}

// reviewCommentsJSON is the shape github.RESTClient.ListReviewComments
// decodes -- id/user.login/body/path/line, the inline (diff-attached)
// shape distinct from commentsJSON's plain conversation-thread one.
func reviewCommentsJSON(comments []github.ReviewComment) []map[string]any {
	out := make([]map[string]any, len(comments))
	for i, c := range comments {
		out[i] = map[string]any{
			"id": c.ID, "body": c.Body, "path": c.Path, "line": c.Line,
			"user": map[string]string{"login": c.User},
		}
		if c.InReplyTo != 0 {
			out[i]["in_reply_to_id"] = c.InReplyTo
		}
		if c.ReviewID != 0 {
			out[i]["pull_request_review_id"] = c.ReviewID
		}
	}
	return out
}

// graphql answers the GraphQL documents github.RESTClient sends -- a
// pull request's review threads (ListReviewThreads), resolving one
// (ResolveReviewThread), and reading and joining a branch's merge queue
// (MergeQueueEntryOf, EnqueuePullRequest) -- told apart by the field each
// selects. Any
// other document is an unhandled request, as an unknown REST path is.
func (s *Sim) graphql(body []byte) (github.ApiResponse, error) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return github.ApiResponse{}, err
	}
	switch {
	case strings.Contains(req.Query, "enqueuePullRequest"):
		id, _ := req.Variables["id"].(string)
		number, err := strconv.Atoi(strings.TrimPrefix(id, "PR_"))
		if err != nil {
			return jsonResponse(200, map[string]any{"errors": []map[string]string{{
				"type": "NOT_FOUND", "message": fmt.Sprintf("Could not resolve to a node with the global id of '%s'", id)}}}), nil
		}
		s.Enqueues++
		if s.mergeQueuePosition(number) == 0 {
			s.MergeQueue = append(s.MergeQueue, number)
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{"enqueuePullRequest": map[string]any{
			"mergeQueueEntry": map[string]any{"state": "QUEUED", "position": s.mergeQueuePosition(number)}}}}), nil
	case strings.Contains(req.Query, "isInMergeQueue"):
		s.mustOwn(fmt.Sprint(req.Variables["owner"]), fmt.Sprint(req.Variables["repo"]))
		number, _ := req.Variables["number"].(float64)
		pr, ok := s.pullRequest(int(number))
		if !ok {
			return jsonResponse(200, map[string]any{"data": map[string]any{
				"repository": map[string]any{"pullRequest": nil}}}), nil
		}
		position := s.mergeQueuePosition(pr.Number)
		var entry any
		if position > 0 {
			entry = map[string]any{"state": "QUEUED", "position": position}
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{
			"repository": map[string]any{"pullRequest": map[string]any{
				"id": fmt.Sprintf("PR_%d", pr.Number), "headRefOid": s.branchSHA(pr.Head), "merged": pr.Merged,
				"isInMergeQueue": position > 0, "mergeQueueEntry": entry,
			}}}}), nil
	case strings.Contains(req.Query, "resolveReviewThread"):
		id, _ := req.Variables["threadId"].(string)
		if !s.reviewThreadExists(id) {
			return jsonResponse(200, map[string]any{"errors": []map[string]string{{
				"type": "NOT_FOUND", "message": fmt.Sprintf("Could not resolve to a node with the global id of '%s'", id)}}}), nil
		}
		s.ResolvedThreads[id] = true
		return jsonResponse(200, map[string]any{"data": map[string]any{
			"resolveReviewThread": map[string]any{"thread": map[string]any{"id": id, "isResolved": true}}}}), nil
	case strings.Contains(req.Query, "reviewThreads"):
		s.mustOwn(fmt.Sprint(req.Variables["owner"]), fmt.Sprint(req.Variables["repo"]))
		number, _ := req.Variables["number"].(float64)
		if _, ok := s.pullRequest(int(number)); !ok {
			return jsonResponse(200, map[string]any{"data": map[string]any{
				"repository": map[string]any{"pullRequest": nil}}}), nil
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{
			"repository": map[string]any{"pullRequest": map[string]any{
				"reviewThreads": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
					"nodes":    s.reviewThreadsJSON(int(number)),
				}}}}}), nil
	}
	panic(fmt.Sprintf("githubsim: unhandled GraphQL document %q", req.Query))
}

func (s *Sim) reviewThreadExists(id string) bool {
	for _, comments := range s.ReviewComments {
		for _, c := range comments {
			if c.InReplyTo == 0 && id == fmt.Sprintf("thread-%d", c.ID) {
				return true
			}
		}
	}
	return false
}

func (s *Sim) conversationResolutionRequiredForPR(number int) bool {
	if s.RequireConversationResolution {
		return true
	}
	pr, ok := s.pullRequest(number)
	if !ok {
		return false
	}
	return s.conversationResolutionRequired(pr.Base)
}

func (s *Sim) conversationResolutionRequired(base string) bool {
	if s.RequireConversationResolution {
		return true
	}
	for _, rule := range s.BranchRules[base] {
		if rule["type"] != "pull_request" {
			continue
		}
		if params, ok := rule["parameters"].(map[string]any); ok {
			if req, _ := params["required_review_thread_resolution"].(bool); req {
				return true
			}
		}
	}
	return false
}

func (s *Sim) hasUnresolvedReviewThreads(number int) bool {
	for _, c := range s.ReviewComments[number] {
		if c.InReplyTo == 0 && !s.ResolvedThreads[fmt.Sprintf("thread-%d", c.ID)] {
			return true
		}
	}
	return false
}

// reviewThreadsJSON is pull request number's review comments grouped
// into GraphQL reviewThreads nodes, one per comment that opened a thread.
func (s *Sim) reviewThreadsJSON(number int) []map[string]any {
	var nodes []map[string]any
	for _, root := range s.ReviewComments[number] {
		if root.InReplyTo != 0 {
			continue
		}
		ids := []map[string]any{{"databaseId": root.ID}}
		for _, c := range s.ReviewComments[number] {
			if c.InReplyTo == root.ID {
				ids = append(ids, map[string]any{"databaseId": c.ID})
			}
		}
		id := fmt.Sprintf("thread-%d", root.ID)
		nodes = append(nodes, map[string]any{
			"id": id, "isResolved": s.ResolvedThreads[id], "isOutdated": root.Line == nil,
			"path": root.Path, "comments": map[string]any{"nodes": ids},
		})
	}
	return nodes
}

// workflowJobsFor is the jobs seeded for a commit, found by the sha the
// Actions endpoints are called with or by the name of a branch whose tip
// is that sha.
//
// The second half is what makes these seedable at all: a pull request's
// head sha comes out of the bare repo (pullRequestDetailJSON reads it
// with branchSHA), so a test that seeded CheckRuns under a branch name
// has no sha to seed these under. Resolving the key rather than the
// request keeps one seeding convention for both maps.
func (s *Sim) workflowJobsFor(sha string) []WorkflowJob {
	if sha == "" {
		return nil
	}
	if jobs, ok := s.WorkflowJobs[sha]; ok {
		return jobs
	}
	for key, jobs := range s.WorkflowJobs {
		if s.branchSHA(key) == sha {
			return jobs
		}
	}
	return nil
}

// startupFailureFor reports whether a commit's run is one GitHub failed
// before it started a job. Resolves a branch name to its sha the way
// workflowJobsFor does, and for the same reason.
func (s *Sim) startupFailureFor(sha string) bool {
	if sha == "" {
		return false
	}
	if s.StartupFailures[sha] {
		return true
	}
	for key, failed := range s.StartupFailures {
		if failed && s.branchSHA(key) == sha {
			return true
		}
	}
	return false
}

// runIDBase keeps Sim's fake workflow-run ids clear of the small integers
// everything else here counts from, so a run id in a failing test's
// output is recognisable as one.
const runIDBase = 9000

// runIDFor is the workflow run id Sim answers for a commit -- stable
// across calls, so the jobs endpoint can be asked about a run the runs
// endpoint named earlier.
func (s *Sim) runIDFor(key string) int {
	for i, k := range s.runKeys {
		if k == key {
			return runIDBase + i
		}
	}
	s.runKeys = append(s.runKeys, key)
	return runIDBase + len(s.runKeys) - 1
}

// WorkflowJobID is the id the Actions API gives the index'th job seeded
// in WorkflowJobs under key -- the same id its check run and its log are
// reached by -- registering key's run if nothing has listed it yet.
func (s *Sim) WorkflowJobID(key string, index int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(jobID(s.runIDFor(key), index))
}

// runKeyFor inverts runIDFor.
func (s *Sim) runKeyFor(runID int) (string, bool) {
	i := runID - runIDBase
	if i < 0 || i >= len(s.runKeys) {
		return "", false
	}
	return s.runKeys[i], true
}

// jobID numbers a job within its run, so one integer identifies both --
// the logs endpoint is given nothing else to find a job by.
func jobID(runID, index int) int { return runID*1000 + index }

// jobByID inverts jobID, for the endpoints given a job id and nothing
// else. The check-run endpoints are among them: Sim gives a job's check
// run the job's own id (jobsJSON), since a second id space would be one
// more thing to invert and GitHub's real ids are opaque to a test either
// way.
func (s *Sim) jobByID(id int) (WorkflowJob, bool) {
	key, ok := s.runKeyFor(id / 1000)
	if !ok {
		return WorkflowJob{}, false
	}
	jobs := s.workflowJobsFor(key)
	index := id % 1000
	if index >= len(jobs) {
		return WorkflowJob{}, false
	}
	return jobs[index], true
}

// jobConclusion is GitHub's own field, with Sim's default for a job a
// test seeded without one.
func jobConclusion(job WorkflowJob) string {
	if job.Conclusion == "" {
		return "success"
	}
	return job.Conclusion
}

// workflowRunsJSON is the shape both github.RESTClient.ListWorkflowRuns
// and its FailedJobLogs decode: one run per commit here, since Sim models
// a commit's CI as a flat set of jobs rather than as several workflows.
// Its conclusion follows its jobs -- a run whose jobs all passed is not a
// failed run, and FailedJobLogs never asks it for jobs.
func workflowRunsJSON(owner, repo string, runID int, jobs []WorkflowJob) map[string]any {
	if len(jobs) == 0 {
		return map[string]any{"total_count": 0, "workflow_runs": []map[string]any{}}
	}
	conclusion := "success"
	for _, job := range jobs {
		switch jobConclusion(job) {
		case "failure", "timed_out", "startup_failure":
			conclusion = "failure"
		}
	}
	return map[string]any{
		"total_count":   1,
		"workflow_runs": []map[string]any{runJSON(owner, repo, runID, conclusion)},
	}
}

// repoRunsJSON is the runs endpoint's answer when nothing scopes it to a
// commit: every run Sim has, newest-seeded last, which is what a caller
// asking "does anything in this repository ever get a job started" reads
// (github.RESTClient.RecentRunStarts).
//
// Sim models a commit's CI as one run, so this is one run per seeded
// commit -- a startup failure for a commit in StartupFailures, and
// otherwise the run its jobs imply. Run ids come from runIDFor, the same
// as every other answer here, so the jobs endpoint can be asked about
// anything named in this list.
func (s *Sim) workflowRunByID(id int64) (WorkflowRun, bool) {
	for i, r := range s.WorkflowRuns {
		runID := r.ID
		if runID <= 0 {
			runID = int64(runIDBase + 500 + i)
		}
		if runID == id {
			r.ID = runID
			return r, true
		}
	}
	return WorkflowRun{}, false
}

func (s *Sim) repoRunsJSON(owner, repo string) map[string]any {
	runs := []map[string]any{}
	for i, wr := range s.WorkflowRuns {
		runID := wr.ID
		if runID <= 0 {
			runID = int64(runIDBase + 500 + i)
		}
		status := wr.Status
		if status == "" {
			status = "completed"
		}
		conclusion := wr.Conclusion
		if conclusion == "" {
			conclusion = "success"
			for _, job := range wr.Jobs {
				switch jobConclusion(job) {
				case "failure", "timed_out", "startup_failure":
					conclusion = "failure"
				}
			}
		}
		attempt := wr.RunAttempt
		if attempt <= 0 {
			attempt = 1
		}
		name := wr.Name
		if name == "" {
			name = "tests"
		}
		headSHA := wr.HeadSHA
		if headSHA == "" && wr.HeadBranch != "" {
			headSHA = s.branchSHA(wr.HeadBranch)
		}
		runs = append(runs, map[string]any{
			"id":          runID,
			"name":        name,
			"path":        wr.Path,
			"head_branch": wr.HeadBranch,
			"head_sha":    headSHA,
			"event":       wr.Event,
			"status":      status,
			"conclusion":  conclusion,
			"run_attempt": attempt,
			"html_url":    fmt.Sprintf("https://github.example/%s/%s/actions/runs/%d", owner, repo, runID),
		})
	}
	for _, key := range s.runCommits() {
		id := s.runIDFor(key)
		if s.startupFailureFor(key) {
			runs = append(runs, runJSON(owner, repo, id, "startup_failure"))
			continue
		}
		jobs := s.workflowJobsFor(key)
		if len(jobs) == 0 {
			continue
		}
		conclusion := "success"
		for _, job := range jobs {
			switch jobConclusion(job) {
			case "failure", "timed_out", "startup_failure":
				conclusion = "failure"
			}
		}
		runs = append(runs, runJSON(owner, repo, id, conclusion))
	}
	return map[string]any{"total_count": len(runs), "workflow_runs": runs}
}

// runCommits is every commit (or branch name -- see WorkflowJobs) Sim has
// CI seeded for, in a stable order so that two reads of the endpoint above
// agree with each other and a test's expectations do not depend on map
// iteration.
func (s *Sim) runCommits() []string {
	seen := map[string]bool{}
	var keys []string
	for key := range s.WorkflowJobs {
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	for key, failed := range s.StartupFailures {
		if failed && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// startupFailureRunJSON is the same endpoint's answer for a commit
// seeded in StartupFailures: the run is there and it failed, and the
// jobs endpoint will have none to show for it.
func startupFailureRunJSON(owner, repo string, runID int) map[string]any {
	return map[string]any{
		"total_count":   1,
		"workflow_runs": []map[string]any{runJSON(owner, repo, runID, "startup_failure")},
	}
}

// runJSON is one workflow run as both the runs endpoint's readers decode
// it. html_url is here because a run reported in place of the jobs it
// has no log from is reported *as* its page -- that link is the only
// thing left pointing at GitHub's own reason for the failure.
func runJSON(owner, repo string, runID int, conclusion string) map[string]any {
	return map[string]any{
		"id": runID, "name": "tests", "status": "completed", "conclusion": conclusion,
		"html_url": fmt.Sprintf("https://github.example/%s/%s/actions/runs/%d",
			owner, repo, runID),
	}
}

// jobsJSON is the shape github.RESTClient.FailedJobLogs decodes for one
// run's jobs.
//
// The steps carry the weight here even though nothing reads their names:
// a job that ran has them and a job GitHub never started has an empty
// array, and that difference is how a reader tells a build the tree broke
// from a build the repository broke. check_run_url is the other half of
// the same answer -- the reason for the second kind is written on the
// check run and nowhere in the Actions API -- and it uses the job's own
// id, so that the check-run endpoints below can find their way back to
// the job the same way the logs endpoint does.
func jobsJSON(owner, repo string, runID int, jobs []WorkflowJob) map[string]any {
	items := make([]map[string]any, len(jobs))
	for i, job := range jobs {
		id := jobID(runID, i)
		stepName := job.FailingStep
		if stepName == "" {
			stepName = "Run the job"
		}
		steps := []map[string]any{
			{"name": stepName, "status": "completed", "conclusion": jobConclusion(job)},
		}
		if job.NeverStarted {
			steps = []map[string]any{}
		}
		items[i] = map[string]any{
			"id": id, "name": job.Name, "status": "completed",
			"conclusion": jobConclusion(job),
			"html_url": fmt.Sprintf("https://github.example/%s/%s/actions/runs/%d/job/%d",
				owner, repo, runID, id),
			"check_run_url": fmt.Sprintf("https://api.github.example/repos/%s/%s/check-runs/%d",
				owner, repo, id),
			"steps": steps,
		}
	}
	return map[string]any{"total_count": len(items), "jobs": items}
}

// checkRunJSON is what GET /repos/{owner}/{repo}/check-runs/{id} answers
// for one seeded job: its output, which is where GitHub's own reason for
// a job it never started lives. A job with nothing seeded gets the empty
// output a check run for an ordinary job has, since output.title and
// output.summary are null far more often than they are set.
func checkRunJSON(job WorkflowJob) map[string]any {
	output := map[string]any{"title": nil, "summary": nil}
	if job.FailureReason != "" {
		output = map[string]any{"title": job.FailureReason, "summary": nil}
	}
	return map[string]any{
		"name": job.Name, "status": "completed",
		"conclusion": jobConclusion(job), "output": output,
	}
}

// checkRunsJSON is the shape github.RESTClient.ListCheckRuns decodes --
// GitHub's own "not a bare array" response for this one endpoint.
func checkRunsJSON(runs []github.CheckRun) map[string]any {
	items := make([]map[string]any, len(runs))
	for i, r := range runs {
		item := map[string]any{"name": r.Name, "status": r.Status, "conclusion": r.Conclusion, "html_url": r.HTMLURL}
		if r.ID != 0 {
			item["id"] = r.ID
		}
		if r.HeadSHA != "" {
			item["head_sha"] = r.HeadSHA
		}
		if r.App != "" {
			item["app"] = map[string]any{"slug": r.App}
		}
		if r.StartedAt != nil {
			item["started_at"] = r.StartedAt
		}
		if r.CompletedAt != nil {
			item["completed_at"] = r.CompletedAt
		}
		if r.FailureReason != "" || r.Output != "" {
			var title, summary any
			if r.FailureReason != "" {
				title = r.FailureReason
			}
			if r.Output != "" {
				summary = r.Output
			}
			item["output"] = map[string]any{"title": title, "summary": summary}
		}
		if r.StepsExecuted > 0 {
			steps := make([]map[string]any, r.StepsExecuted)
			for j := 0; j < r.StepsExecuted; j++ {
				steps[j] = map[string]any{"name": fmt.Sprintf("step %d", j+1)}
			}
			item["steps"] = steps
		}
		items[i] = item
	}
	return map[string]any{"total_count": len(items), "check_runs": items}
}

func labelSetFrom(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func (s *Sim) mustOwn(owner, repo string) {
	if owner != s.Owner || repo != s.Repo {
		panic(fmt.Sprintf("githubsim: configured for %s/%s, got %s/%s", s.Owner, s.Repo, owner, repo))
	}
}

func mustAtoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
