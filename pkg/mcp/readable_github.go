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

package mcp

// readable_github.go decides how a GitHub refusal reads when one of the
// CI tools has to hand it back.
//
// Everything these three tools return travels the same last inch: the
// handler turns whatever came back into Result{Text: err.Error()}, the
// agent reads that text, and a human reads it again in the transcript
// afterwards. What arrives there for a credential GitHub has stopped
// answering for is *github.Error's rendering, which quotes the response
// body whole and escaped:
//
//	reading acme/grain's main branch: GitHub API error 404: "{\r\n
//	\"message\": \"Not Found\",\r\n  \"documentation_url\": ...
//
// The two words that say what happened are three lines into a blob, and
// this is the one surface in the repository whose reader then acts on
// them unsupervised: an agent that cannot tell "your branch is not
// pushed" from "this deployment's credential lost its access" spends
// turns on the wrong one, and a run that reads the second as a bug in
// its own change goes looking for it in the diff.
//
// So every read these tools make is rendered through github.Readable
// (github.Message as an error), and the sentence above becomes `reading
// acme/grain's main branch: GitHub answered 404: Not Found`.
//
// It is done here, once, around the client, rather than at each call
// site. There are ten of those across pullrequest_tools.go,
// base_branch_status_tool.go, wait_for_checks_tool.go and
// failing_job_logs.go -- effectively every method of PullRequestReader
// -- so "the reads this tool surface makes render GitHub's own sentence"
// is a property of the surface rather than a habit each call site has to
// remember, and an eleventh call site added later gets it without being
// told. Readable keeps the *github.Error underneath, so
// github.IsPermissionDenied and github.IsNotFound go on answering for
// these errors: checkRunsForCommit's fallback to the Actions API and
// logsUnreadable's sentence about job logs both depend on that, and both
// are read through this wrapper.

import "github.com/ai-on-gke/grain/pkg/github"

// readableErrors returns client with every GitHub refusal it reports
// rendered as GitHub's own sentence.
//
// nil in, nil out: a nil client is not a misconfiguration here but the
// ordinary way this package's tools are enumerated without a GitHub
// credential (see NewPullRequestTools), and the handlers test for it, so
// wrapping one in a non-nil decorator would turn "there is no repository
// configured for this run" into a nil dereference. Wrapping an already
// wrapped client is likewise a no-op, since NewPullRequestTools passes
// its own client on to NewWaitForChecksTools and both wrap.
func readableErrors(client PullRequestReader) PullRequestReader {
	if client == nil {
		return nil
	}
	if r, ok := client.(readableReader); ok {
		return r
	}
	return readableReader{client: client}
}

// readableReader is PullRequestReader with github.Readable over each
// answer. The methods are written out rather than embedded so that
// adding one to the interface is a compile error here: a read that
// quietly kept *Error's rendering would show up only in a transcript,
// long after anybody was looking.
type readableReader struct {
	client PullRequestReader
}

func (r readableReader) GetBranchHead(owner, repo, branch string) (*github.BranchHead, error) {
	head, err := r.client.GetBranchHead(owner, repo, branch)
	return head, github.Readable(err)
}

func (r readableReader) DefaultBranch(owner, repo string) (string, error) {
	branch, err := r.client.DefaultBranch(owner, repo)
	return branch, github.Readable(err)
}

func (r readableReader) FindOpenPullRequestForBranch(owner, repo, branch string) (*github.PullRequest, error) {
	pr, err := r.client.FindOpenPullRequestForBranch(owner, repo, branch)
	return pr, github.Readable(err)
}

func (r readableReader) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	detail, err := r.client.GetPullRequest(owner, repo, number)
	return detail, github.Readable(err)
}

func (r readableReader) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	checks, err := r.client.ListCheckRuns(owner, repo, ref)
	return checks, github.Readable(err)
}

func (r readableReader) ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error) {
	runs, err := r.client.ListWorkflowRuns(owner, repo, headSHA)
	return runs, github.Readable(err)
}

func (r readableReader) FailedJobLogs(owner, repo, headSHA string) ([]github.JobLog, error) {
	logs, err := r.client.FailedJobLogs(owner, repo, headSHA)
	return logs, github.Readable(err)
}

func (r readableReader) ActionsState(owner, repo, headSHA string) (github.ActionsState, error) {
	state, err := r.client.ActionsState(owner, repo, headSHA)
	return state, github.Readable(err)
}

func (r readableReader) ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error) {
	comments, err := r.client.ListReviewComments(owner, repo, number)
	return comments, github.Readable(err)
}
