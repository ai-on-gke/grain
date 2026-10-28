package mcp

// What the two CI reports say for a repository that has declared
// GitHub's check runs are not a source of verdicts for it
// (PullRequestScope.GitHubCIIgnored). The point of the setting is that a
// run stops being handed a dozen reds to explain away, so what is pinned
// here is as much what the answers no longer contain, and what reads
// they no longer make, as what they say instead.

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
)

func gatedScope() PullRequestScope {
	s := testScope
	s.Base = "main"
	s.GitHubCIIgnored = true
	return s
}

// redEverywhere is this repository as it has actually stood since 17
// September: every check on the branch red, every check on the base red
// for the same reason, and neither of them a verdict about anything.
func redEverywhere() *fakePullRequests {
	return &fakePullRequests{
		heads: map[string]*github.BranchHead{
			"grain/task-9": {SHA: testHeadSHA, Message: "the change"},
			"main":         {SHA: "fedcba9876543210", Message: "somebody else's merge"},
		},
		pr:     &github.PullRequest{Number: 42, HTMLURL: "https://github.example/pr/42"},
		detail: github.PullRequestDetail{State: "open", Mergeable: boolPtr(true), BaseRef: "main"},
		checks: []github.CheckRun{done("go", "failure"), done("lint", "failure")},
		jobLogs: []github.JobLog{{
			Name: "go", URL: "https://github.example/runs/1",
			Log: "The job was not started because an Actions budget is preventing further use",
		}},
	}
}

func gatedStatus(t *testing.T, client PullRequestReader, grainCI GrainCIReader) string {
	t.Helper()
	res := namedTool(t, NewPullRequestTools(client, grainCI, gatedScope()), "pull_request_status").
		Handler(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatalf("pull_request_status failed: %s", res.Text)
	}
	return res.Text
}

func gatedBaseStatus(t *testing.T, client PullRequestReader, grainCI GrainCIReader) string {
	t.Helper()
	res := namedTool(t, NewPullRequestTools(client, grainCI, gatedScope()), "base_branch_status").
		Handler(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatalf("base_branch_status failed: %s", res.Text)
	}
	return res.Text
}

func TestPullRequestStatusExplainsTheGateInsteadOfListingChecks(t *testing.T) {
	client := redEverywhere()
	text := gatedStatus(t, client, grainCIAt(testHeadSHA, "success", ""))

	for _, want := range []string{
		"gate is grain CI",
		"GitHub's checks are not read here",
		// Still the two facts the report is actually for: which commit
		// is at the branch's tip, and which pull request carries it.
		"grain/task-9 is at",
		"Pull request #42",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not say %q:\n%s", want, text)
		}
	}
	// The reds themselves, and the paragraph of somebody else's log
	// under them, are the cost this exists to remove.
	for _, unwanted := range []string{"FAILING", "lint", "Actions budget"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("answer still puts %q in front of the run:\n%s", unwanted, text)
		}
	}
	if client.logReads != 0 {
		t.Errorf("the failing jobs' logs were read %d times for checks nobody reads", client.logReads)
	}
}

// Not read, rather than read and then left out. The reads cost money and
// time, and one of them failing used to be an error the run had to
// interpret.
func TestPullRequestStatusMakesNoCheckReadWhereTheGateIsGrainCI(t *testing.T) {
	client := redEverywhere()
	gatedStatus(t, client, grainCIAt(testHeadSHA, "success", ""))
	for _, ref := range client.refs {
		if ref == testHeadSHA {
			t.Errorf("a CI read was made against %s anyway: %v", testHeadSHA, client.refs)
		}
	}
}

// The verdict that does decide is still reported, in the same words it
// always was -- the setting removes a provider, it does not make the
// report quieter about the one that is left.
func TestPullRequestStatusStillReportsGrainCIUnderTheGate(t *testing.T) {
	text := gatedStatus(t, redEverywhere(), grainCIAt(testHeadSHA, "failure", "job \"unit\" failed"))
	for _, want := range []string{".grain/workflows", "unit"} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not say %q:\n%s", want, text)
		}
	}
}

// base_branch_status's whole job is a comparison between GitHub's checks
// on two branches. With neither side read there is nothing to compare,
// and answering "all red on both sides" -- which is what it did for a
// week -- costs a call and settles nothing.
func TestBaseBranchStatusSaysThereIsNothingToCompare(t *testing.T) {
	client := redEverywhere()
	text := gatedBaseStatus(t, client, grainCIAt(testHeadSHA, "success", ""))

	for _, want := range []string{
		"main",
		"gate is grain CI",
		"GitHub's checks are not read here",
		"no comparison",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("answer does not say %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{
		"FAILING",
		"breakage you inherited",
		"already failing on main",
	} {
		if strings.Contains(text, unwanted) {
			t.Errorf("answer still reasons about inherited checks (%q):\n%s", unwanted, text)
		}
	}
	if client.logReads != 0 {
		t.Errorf("the base's failing job logs were read %d times", client.logReads)
	}
}

// And says it in the description too, since the turn a run spends
// calling a tool is spent before it ever sees the answer.
func TestBothDescriptionsSayTheGateIsGrainCI(t *testing.T) {
	tools := NewPullRequestTools(redEverywhere(), grainCIAt(testHeadSHA, "success", ""), gatedScope())
	for _, name := range []string{"pull_request_status", "base_branch_status"} {
		got := namedTool(t, tools, name).Description
		if !strings.Contains(got, "grain CI") || !strings.Contains(got, "not read here") {
			t.Errorf("%s's description does not say the gate is grain CI:\n%s", name, got)
		}
	}
}

// The repository that turned this on and has no grain CI. It is allowed
// -- see model.RepoConfig.IgnoreGitHubCI -- and what makes it allowed
// rather than a silent way to merge ungated is that both reports say so
// to the run that lands in it, in words, rather than reading as a green
// branch nobody has any checks on.
func TestARunWithNoGrainCIUnderTheGateIsToldNothingJudgesIt(t *testing.T) {
	for _, text := range []string{
		gatedStatus(t, redEverywhere(), nil),
		gatedBaseStatus(t, redEverywhere(), nil),
	} {
		for _, want := range []string{"does not hold the grain-ci capability", "gives up and asks a person"} {
			if !strings.Contains(text, want) {
				t.Errorf("answer does not say %q:\n%s", want, text)
			}
		}
	}
}

// The control for every case above: with the declaration off, both tools
// read GitHub and answer exactly as they always have.
func TestWithoutTheDeclarationBothReportsStillListChecks(t *testing.T) {
	scope := testScope
	scope.Base = "main"
	tools := NewPullRequestTools(redEverywhere(), grainCIAt(testHeadSHA, "success", ""), scope)

	for _, name := range []string{"pull_request_status", "base_branch_status"} {
		res := namedTool(t, tools, name).Handler(context.Background(), map[string]any{})
		if res.IsError {
			t.Fatalf("%s failed: %s", name, res.Text)
		}
		if !strings.Contains(res.Text, "FAILING") {
			t.Errorf("%s no longer lists GitHub's checks for an ordinary repository:\n%s", name, res.Text)
		}
		if strings.Contains(res.Text, "not read here") {
			t.Errorf("%s says the gate is grain CI for a repository that never declared it:\n%s", name, res.Text)
		}
	}
}
