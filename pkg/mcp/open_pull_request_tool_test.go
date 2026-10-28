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
	"errors"
	"strings"
	"testing"
)

type fakeOpener struct {
	report PullRequestReport
	err    error
	calls  int
}

func (f *fakeOpener) OpenPullRequest(context.Context) (PullRequestReport, error) {
	f.calls++
	return f.report, f.err
}

// openPullRequest calls the tool the way a client would: by name, off the
// registry, with whatever arguments the model sent.
func openPullRequest(t *testing.T, opener PullRequestOpener, args map[string]any) Result {
	t.Helper()
	tools := NewOpenPullRequestTools(opener)
	if len(tools) != 1 || tools[0].Name != "open_pull_request" {
		t.Fatalf("NewOpenPullRequestTools returned %+v, want one open_pull_request tool", tools)
	}
	return tools[0].Handler(context.Background(), args)
}

func TestOpenPullRequestReportsTheRequestAndItsChecks(t *testing.T) {
	opener := &fakeOpener{report: PullRequestReport{
		Repo: "acme/widgets", Number: 7, URL: "https://github.com/acme/widgets/pull/7",
		ChecksAvailable: true,
		Checks: []CheckReport{
			{Name: "lint", Status: "completed", Conclusion: "failure"},
			{Name: "tests", Status: "in_progress"},
		},
	}}

	res := openPullRequest(t, opener, nil)
	if res.IsError {
		t.Fatalf("open_pull_request reported an error: %s", res.Text)
	}
	if opener.calls != 1 {
		t.Errorf("opener called %d times, want 1", opener.calls)
	}
	for _, want := range []string{
		"acme/widgets#7",
		"https://github.com/acme/widgets/pull/7",
		"lint: completed (failure)",
		"tests: in_progress",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// "Nothing has reported yet" and "checks cannot be read here" are the
// same empty list, and an agent that cannot tell them apart either polls
// forever or dismisses a failure it never saw -- so the text has to.
func TestOpenPullRequestDistinguishesNoChecksYetFromNoChecksEver(t *testing.T) {
	pending := openPullRequest(t, &fakeOpener{report: PullRequestReport{
		Repo: "acme/widgets", Number: 7, ChecksAvailable: true,
	}}, nil)
	if pending.IsError {
		t.Fatalf("unexpected error: %s", pending.Text)
	}
	if !strings.Contains(pending.Text, "call this tool again") {
		t.Errorf("text = %q, want it to say waiting and calling again is worth it", pending.Text)
	}

	blind := openPullRequest(t, &fakeOpener{report: PullRequestReport{
		Repo: "acme/widgets", Number: 7,
	}}, nil)
	if blind.IsError {
		t.Fatalf("unexpected error: %s", blind.Text)
	}
	if !strings.Contains(blind.Text, "cannot read checks") {
		t.Errorf("text = %q, want it to say checks cannot be read here at all", blind.Text)
	}

	failed := openPullRequest(t, &fakeOpener{report: PullRequestReport{
		Repo: "acme/widgets", Number: 7, ChecksError: "github said 500",
	}}, nil)
	if failed.IsError {
		t.Fatalf("a pull request that was opened is not an error: %s", failed.Text)
	}
	if !strings.Contains(failed.Text, "acme/widgets#7") || !strings.Contains(failed.Text, "github said 500") {
		t.Errorf("text = %q, want both the pull request and why its checks are missing", failed.Text)
	}
}

func TestOpenPullRequestReportsAFailureToOpenOne(t *testing.T) {
	res := openPullRequest(t, &fakeOpener{err: errors.New("grain/task-t1 is not on acme/widgets yet")}, nil)
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	if !strings.Contains(res.Text, "not on acme/widgets yet") {
		t.Errorf("text = %q, want the reason the pull request was not opened", res.Text)
	}
}

func TestOpenPullRequestReportsMergeConflict(t *testing.T) {
	conflictErr := errors.New(`orchestrator: branch "grain/task-t1" conflicts with "main" on acme/widgets in file(s): SHARED.md; resolve the merge conflict locally and push before opening a pull request`)
	res := openPullRequest(t, &fakeOpener{err: conflictErr}, nil)
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	for _, want := range []string{
		`branch "grain/task-t1" conflicts with "main"`,
		"in file(s): SHARED.md",
		"resolve the merge conflict locally",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// A nil opener is how a framework's allowedTools enumerates this tool's
// name without holding a live one (agent/claude's own allowedTools), so
// building the tool must work -- and calling it must refuse rather than
// panic.
func TestOpenPullRequestWithNoOpenerRefuses(t *testing.T) {
	res := openPullRequest(t, nil, nil)
	if !res.IsError {
		t.Fatal("expected a nil opener to refuse the call")
	}
	if !strings.Contains(res.Text, "when your run ends") {
		t.Errorf("text = %q, want it to say the pull request still gets opened at the end", res.Text)
	}
}

// The tool takes no arguments at all: which repo, branch and base a run
// pushes to are grain's to decide, and a schema that admitted any of them
// would invite an agent to try.
func TestOpenPullRequestTakesNoArguments(t *testing.T) {
	tool := NewOpenPullRequestTools(nil)[0]
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema = %+v, want a properties object", tool.InputSchema)
	}
	if len(props) != 0 {
		t.Errorf("properties = %+v, want none", props)
	}
	if tool.InputSchema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", tool.InputSchema["additionalProperties"])
	}
}

// Registered alongside the rest, it is reachable by name over the same
// JSON-RPC surface every other tool is -- which is all a real client
// (claude's --mcp-config fork) ever gets.
func TestOpenPullRequestIsCallableThroughTheRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewOpenPullRequestTools(&fakeOpener{report: PullRequestReport{
		Repo: "acme/widgets", Number: 7, ChecksAvailable: true,
	}})...)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "open_pull_request" {
		t.Fatalf("ListTools = %+v, want just open_pull_request", tools)
	}
	res, err := client.CallTool(context.Background(), "open_pull_request", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("open_pull_request reported an error: %s", res.Text())
	}
	if !strings.Contains(res.Text(), "acme/widgets#7") {
		t.Errorf("text = %q, want the pull request it opened", res.Text())
	}
}

// When the first branch pushed to an empty repository initializes the default
// branch directly, OpenPullRequestForTask returns Number == 0 with a Note
// explaining that the default branch was initialized and the task marked done.
// renderPullRequestReport must return that note cleanly without claiming a
// non-existent "#0" pull request is open or asking the agent to poll checks.
func TestOpenPullRequestOnEmptyRepoFirstPushReportsInitializationWithoutZeroPR(t *testing.T) {
	note := "Initialized main on acme/empty at 1234567 and marked task 1 done."
	res := openPullRequest(t, &fakeOpener{report: PullRequestReport{
		Repo:            "acme/empty",
		Number:          0,
		ChecksAvailable: true,
		Note:            note,
	}}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if res.Text != note {
		t.Errorf("text = %q, want %q", res.Text, note)
	}
	for _, unwanted := range []string{"#0", "call this tool again"} {
		if strings.Contains(res.Text, unwanted) {
			t.Errorf("text = %q, must not contain %q", res.Text, unwanted)
		}
	}
}
