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

import (
	"context"
	"fmt"
	"strings"
)

// CheckReport is one CI check on a pull request's head, as
// open_pull_request reports it back to the agent: GitHub's own name,
// status ("queued", "in_progress", "completed") and, once completed, its
// conclusion ("success", "failure", ...) verbatim. Nothing here narrows
// that vocabulary -- an agent reading "failure" against a check it
// recognizes knows more about what to do next than this package could
// encode.
type CheckReport struct {
	Name       string
	Status     string
	Conclusion string
}

// PullRequestReport is what one open_pull_request call learned: the pull
// request now open for this run's own branch, and what the repo's CI has
// said about it so far.
type PullRequestReport struct {
	Repo   string
	Number int
	URL    string
	Checks []CheckReport
	// ChecksAvailable is false when the deployment cannot read checks at
	// all, as opposed to a head nothing has reported on yet -- the two are
	// the same empty Checks otherwise, and only one of them is worth
	// waiting on.
	ChecksAvailable bool
	// ChecksError is a failure to read the checks of a pull request that
	// was nonetheless opened. The pull request above is real either way.
	ChecksError string
	// Note is grain's own word on part of the branch having already
	// landed on its base, said ahead of the checks when there is one.
	Note string
}

// PullRequestOpener opens (or finds) the pull request for the one task
// this server's run belongs to, and reports its current checks.
//
// It is an interface here, and a narrow one, because this package must
// not know how that actually happens. pull_request_status, next door in
// pullrequest_tools.go, reads GitHub from the mcpserver process itself --
// a read, inside a scope fixed at process start. Opening one is a write,
// and which branch is opened against which base has always been grain's
// decision, so the real implementation -- pkg/cli/mcpserver.go's own --
// asks the running daemon over its REST API and lets the daemon read the
// repo and the branch out of the task's own record. Nothing an agent can
// put in a tool call changes any of it: this call takes no arguments at
// all.
type PullRequestOpener interface {
	OpenPullRequest(ctx context.Context) (PullRequestReport, error)
}

// ChangeOpener is a PullRequestOpener that can also carry a change
// description: what open_pull_request on a changelist task sends, since a
// CL has no commits to build one from.
type ChangeOpener interface {
	OpenChange(ctx context.Context, summary, body string) (PullRequestReport, error)
}

// NewOpenPullRequestTools returns the one tool a run gets when its
// dispatch can open its own pull request: open_pull_request.
//
// Unlike NewMockTools' four escape hatches, this one is not deferred
// until the run ends and then applied by the controller -- it happens
// while the agent waits, because the entire point is what comes back.
// grain has always opened a pull request for a run's branch, but only
// after the run had already exited, which meant the agent never saw what
// the pull request's own checks said: a change that compiles locally and
// fails the repo's own build was a fact nobody learned until a human read
// the pull request. A run that opens it early can read the checks, fix
// what they say, push again, and call this tool again to see the next
// round.
//
// pull_request_status (NewPullRequestTools) answers the neighbouring
// question -- what CI says about a pushed branch -- without opening
// anything, and a run that only wants to watch its own build wants that
// one. This is for a run that has decided the work is done.
//
// opener nil returns the tool anyway, refusing every call: that is what
// lets a caller enumerate the tool names this package registers (each
// agent framework's own allowedTools does exactly that) without holding a
// live opener, and a server that genuinely has none should simply not
// register these tools at all.
func NewOpenPullRequestTools(opener PullRequestOpener) []Tool {
	return []Tool{openPullRequestTool(opener)}
}

func openPullRequestTool(opener PullRequestOpener) Tool {
	return Tool{
		Name: "open_pull_request",
		Description: "Open the pull request for the branch you were told to push, " +
			"without ending your turn -- and report the state of its CI checks. " +
			"Use it once you have pushed a change you believe is finished, so " +
			"you can see what the repo's own checks make of the pull request " +
			"while you still have turns left to fix them. (To read CI for your " +
			"branch without opening anything, use pull_request_status instead.) " +
			"This is the same pull " +
			"request grain opens for you when your run ends, so calling it does " +
			"not commit you to anything and not calling it loses you nothing but " +
			"the checks. Call it again after pushing more commits, or after " +
			"waiting for checks that were still running, to see the current " +
			"state -- it never opens a second pull request. Its title and " +
			"description are built from your branch's own commit messages and " +
			"rebuilt every time you call this, so opening one early costs the " +
			"finished change nothing: write the commit messages and both follow. " +
			"The title is the summary line of the first commit whose message has " +
			"a body, which is how you name the pull request after what you did " +
			"rather than after what the task asked for -- rewrite that line and " +
			"call this again to rename it. Nothing you pass chooses where: which repo, " +
			"branch and base are grain's to decide, not yours. " +
			"On a changelist task (a workspace target, no git branch) it creates the task's " +
			"CL from the workspace's pending files on the first call and updates it " +
			"and uploads a new snapshot after, returning cl/N and its presubmit " +
			"state; it never mails. There, pass summary and body: the CL " +
			"description, written as a commit message (BUG= is the only tag-like " +
			"line most CLs need; R=, CC=, TBR= and auto-submit or presubmit-bypass " +
			"tags are refused).",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "Changelist tasks only: the CL description's one-line summary."},
				"body":    map[string]any{"type": "string", "description": "Changelist tasks only: the CL description's body."},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if opener == nil {
				return Result{
					Text: "This run cannot open its own pull request (no route to grain's " +
						"GitHub client). Finish your work and push your branch: a pull " +
						"request is opened for it when your run ends.",
					IsError: true,
				}
			}
			summary, _ := args["summary"].(string)
			body, _ := args["body"].(string)
			var report PullRequestReport
			var err error
			if co, ok := opener.(ChangeOpener); ok && (summary != "" || body != "") {
				report, err = co.OpenChange(ctx, summary, body)
			} else {
				report, err = opener.OpenPullRequest(ctx)
			}
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: renderPullRequestReport(report)}
		},
	}
}

// renderPullRequestReport is what the agent actually reads back. Plain
// lines rather than JSON, matching every other tool in this package, and
// explicit about the difference between "no checks have reported yet"
// (wait and call again) and "checks cannot be read here" (waiting will
// never help) -- an agent that cannot tell those apart will either poll
// forever or dismiss a real failure it never saw.
func renderPullRequestReport(r PullRequestReport) string {
	if r.Number == 0 && r.Note != "" {
		return strings.TrimRight(r.Note, "\n")
	}
	if r.Repo != "" && !strings.Contains(r.Repo, "/") {
		// A CL: its note already says what was uploaded and how presubmit stands.
		return strings.TrimRight(fmt.Sprintf("cl/%d: %s\n%s", r.Number, r.URL, r.Note), "\n")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request %s#%d is open", r.Repo, r.Number)
	if r.URL != "" {
		fmt.Fprintf(&b, ": %s", r.URL)
	}
	b.WriteString("\n")
	if r.Note != "" {
		b.WriteString(r.Note + "\n")
	}

	switch {
	case r.ChecksError != "":
		fmt.Fprintf(&b, "Its checks could not be read: %s\n", r.ChecksError)
	case !r.ChecksAvailable:
		b.WriteString("This deployment's GitHub credential cannot read checks, so there is " +
			"nothing to wait for here -- calling again will not show you CI.\n")
	case len(r.Checks) == 0:
		b.WriteString("No checks have reported on this commit yet. If this repo has CI, " +
			"give it a minute and call this tool again.\n")
	default:
		b.WriteString("Checks on its head commit right now:\n")
		for _, c := range r.Checks {
			if c.Conclusion != "" {
				fmt.Fprintf(&b, "- %s: %s (%s)\n", c.Name, c.Status, c.Conclusion)
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Status)
		}
		b.WriteString("A check that is not \"completed\" yet is still running: call this " +
			"tool again later to see how it ended.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
