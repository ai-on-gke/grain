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

// What an agent is told when GitHub refuses one of the CI tools' reads
// (readable_github.go).

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
)

// The body GitHub actually sends for a repository this credential may no
// longer read: the two words that matter, three lines deep in an escaped
// document with carriage returns in it.
var notFoundFromGitHub = &github.Error{Status: 404, Body: []byte(
	"{\r\n  \"message\": \"Not Found\",\r\n  \"documentation_url\": " +
		"\"https://docs.github.com/rest/branches/branches#get-a-branch\",\r\n" +
		"  \"status\": \"404\"\r\n}")}

// A refusal reads as GitHub's own sentence, with the tool's own context
// still around it.
//
// This is the answer a run gets when its deployment's credential is
// revoked, narrowed, or pointed at a repository that has been renamed,
// and it is the surface where reading it wrongly costs the most: the
// agent acts on this text unsupervised, and "GitHub API error 404:
// "{\r\n \"message\": ..." is a sentence about JSON rather than about
// access.
func TestACIToolsRefusalReadsAsWhatGitHubSaid(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		client *fakePullRequests
	}{
		{"pull_request_status", &fakePullRequests{headErr: notFoundFromGitHub}},
		{"base_branch_status", &fakePullRequests{headErr: notFoundFromGitHub, defaultBranch: "main"}},
		{"wait_for_checks", &fakePullRequests{headErr: notFoundFromGitHub}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			tool := namedTool(t, NewPullRequestTools(tc.client, nil, testScope), tc.tool)
			res := tool.Handler(context.Background(), map[string]any{})
			if !res.IsError {
				t.Fatalf("a refused read was reported as an answer: %s", res.Text)
			}
			if !strings.Contains(res.Text, "GitHub answered 404: Not Found") {
				t.Errorf("%s does not say what GitHub said: %s", tc.tool, res.Text)
			}
			// The sentence replaces the body, not the tool's own account
			// of which read failed -- an agent told only "GitHub
			// answered 404" cannot tell whose branch was missing.
			if !strings.Contains(res.Text, "branch") {
				t.Errorf("%s stopped saying which read failed: %s", tc.tool, res.Text)
			}
			for _, unwanted := range []string{`\r\n`, "documentation_url", `\"message\"`} {
				if strings.Contains(res.Text, unwanted) {
					t.Errorf("%s carries %s out of the raw body: %s", tc.tool, unwanted, res.Text)
				}
			}
		})
	}
}

// The rendering is a wrapper and not a rewrite, and this is what that
// buys: the two predicates these tools branch on still answer for an
// error that has been through it.
//
// Both branches below are the difference between a useful answer and a
// useless one -- a credential granted Actions but not Checks gets its CI
// reported through the fallback, and one that may not read job logs is
// told so rather than shown a report with a silent hole in it. Reading
// GitHub's refusals through errors.New(github.Message(err)) would have
// turned both off, and neither would have failed loudly.
func TestRenderingARefusalDoesNotStopThePredicatesAnswering(t *testing.T) {
	denied := &github.Error{Status: 403, Body: []byte(
		`{"message":"Resource not accessible by personal access token"}`)}

	// Checks refused, Actions readable: the report is the workflow runs.
	res := call(t, &fakePullRequests{
		head:      &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checksErr: denied,
		workflows: []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("success")}},
	}, testScope)
	if res.IsError {
		t.Fatalf("the fallback to the Actions API stopped happening: %s", res.Text)
	}
	if !strings.Contains(res.Text, "go") {
		t.Errorf("the workflow runs did not reach the answer:\n%s", res.Text)
	}

	// Logs refused: named as a property of the credential rather than as
	// a failure with no output.
	res = call(t, &fakePullRequests{
		head:       &github.BranchHead{SHA: "0123456789abcdef", Message: "wire it up"},
		checks:     []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("failure")}},
		jobLogsErr: denied,
	}, testScope)
	if res.IsError {
		t.Fatalf("IsError = true, want the red build reported anyway: %s", res.Text)
	}
	if !strings.Contains(res.Text, "may not read Actions job logs") {
		t.Errorf("a refused log read stopped being recognized as one:\n%s", res.Text)
	}
}

// The blocking half is registered on its own by callers that do not want
// the whole vocabulary, so it wraps its own client -- and wrapping the
// one NewPullRequestTools already wrapped changes nothing.
func TestTheBlockingHalfRendersRefusalsWhenRegisteredAlone(t *testing.T) {
	client := &fakePullRequests{headErr: notFoundFromGitHub}
	tool := namedTool(t, NewWaitForChecksTools(client, nil, testScope), "wait_for_checks")
	res := tool.Handler(context.Background(), map[string]any{})
	if !strings.Contains(res.Text, "GitHub answered 404: Not Found") {
		t.Errorf("wait_for_checks alone does not say what GitHub said: %s", res.Text)
	}
	if got := readableErrors(readableErrors(client)); got != readableErrors(client) {
		t.Errorf("wrapping an already wrapped client made a second layer: %#v", got)
	}
}

// A nil client is how this package's tools are enumerated without a
// GitHub credential (NewPullRequestTools), and the handlers test for it,
// so the wrapper must not turn one into a non-nil reader that panics on
// the first call.
func TestANilClientIsStillNil(t *testing.T) {
	if got := readableErrors(nil); got != nil {
		t.Fatalf("readableErrors(nil) = %#v, want nil", got)
	}
	res := namedTool(t, NewPullRequestTools(nil, nil, testScope), "pull_request_status").
		Handler(context.Background(), map[string]any{})
	if !res.IsError || !strings.Contains(res.Text, "no GitHub") {
		t.Errorf("a run with no GitHub access is no longer told so: %s", res.Text)
	}
}
