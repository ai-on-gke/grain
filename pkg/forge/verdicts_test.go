package forge

import (
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// fakeVerdicts is a Verdicts that answers whatever a test says, and
// records whether it was asked.
type fakeVerdicts struct {
	checks   []github.CheckRun
	complete bool
	err      error
	asked    int
}

func (f *fakeVerdicts) For(Ref, string) ([]github.CheckRun, bool, error) {
	f.asked++
	return f.checks, f.complete, f.err
}

func conclusion(s string) *string { return &s }

func testRef() Ref { return Ref{Owner: "acme", Repo: "widgets", Number: 7} }

// A complete answer is used, and GitHub is not asked at all -- which is
// the entire point: the request that is not made is the saving.
func TestACompleteSourceAnswerSavesTheGitHubRead(t *testing.T) {
	src := &fakeVerdicts{
		checks:   []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("success")}},
		complete: true,
	}
	transport := github.NewFakeTransport()
	client := github.NewClient(transport, nil)

	checks, known, err := CheckRuns(client, src, testRef(), "topic", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !known || len(checks) != 1 || checks[0].Name != "go" {
		t.Fatalf("got %v known=%v, want the source's answer", checks, known)
	}
	if len(transport.Calls) != 0 {
		t.Errorf("GitHub was called %d times despite a complete answer: %v",
			len(transport.Calls), transport.Calls)
	}
}

// An incomplete answer is not an answer. GitHub is asked, exactly as it
// was before this seam existed, and what the source said is discarded
// rather than merged in -- merging would be how three green checks out
// of twelve become a pass.
func TestAnIncompleteSourceAnswerFallsThroughToGitHub(t *testing.T) {
	src := &fakeVerdicts{
		checks:   []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("success")}},
		complete: false,
	}
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 200,
		Body:   []byte(`{"check_runs":[{"name":"from github","status":"in_progress"}]}`),
	})
	client := github.NewClient(transport, nil)

	checks, known, err := CheckRuns(client, src, testRef(), "topic", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Fatal("the fallback did not answer")
	}
	if len(checks) != 1 || checks[0].Name != "from github" {
		t.Fatalf("got %v, want GitHub's own answer and not the partial one", checks)
	}
	if len(transport.Calls) == 0 {
		t.Error("GitHub was not asked, so an incomplete answer was acted on")
	}
}

// A source that is down must not stop the queue. The failure mode of the
// whole mechanism is "grain goes back to polling", which is merely
// expensive.
func TestASourceThatFailsFallsThroughToGitHub(t *testing.T) {
	src := &fakeVerdicts{err: errors.New("connection refused")}
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 200,
		Body:   []byte(`{"check_runs":[{"name":"from github","status":"completed","conclusion":"success"}]}`),
	})
	client := github.NewClient(transport, nil)

	checks, known, err := CheckRuns(client, src, testRef(), "topic", "deadbeef")
	if err != nil {
		t.Fatalf("a source that failed became the caller's error: %v", err)
	}
	if !known || len(checks) != 1 || checks[0].Name != "from github" {
		t.Fatalf("got %v known=%v, want GitHub's answer", checks, known)
	}
}

// No source at all is exactly what this package did before the seam
// existed, which is what every deployment without a reporting service
// gets.
func TestNoSourceIsTheOldBehaviour(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 200,
		Body:   []byte(`{"check_runs":[{"name":"from github","status":"completed","conclusion":"success"}]}`),
	})
	client := github.NewClient(transport, nil)

	checks, known, err := CheckRuns(client, nil, testRef(), "topic", "deadbeef")
	if err != nil || !known || len(checks) != 1 {
		t.Fatalf("got %v known=%v err=%v", checks, known, err)
	}
}

// A verdict is about a tree, and the branch-name fallback resolves to
// whatever that branch points at now -- so a commit nobody has named is
// a commit the source has nothing to say about, and it is not asked.
func TestASourceIsNotAskedAboutACommitNobodyNamed(t *testing.T) {
	src := &fakeVerdicts{complete: true}
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 200, Body: []byte(`{"check_runs":[]}`),
	})
	client := github.NewClient(transport, nil)

	if _, _, err := CheckRuns(client, src, testRef(), "topic", ""); err != nil {
		t.Fatal(err)
	}
	if src.asked != 0 {
		t.Errorf("the source was asked about a branch rather than a commit")
	}
}

// Observe passes its Input's source through, which is the only route a
// deployment's reporting service actually reaches this package by.
func TestObservePassesItsSourceThrough(t *testing.T) {
	src := &fakeVerdicts{
		checks:   []github.CheckRun{{Name: "go", Status: "completed", Conclusion: conclusion("success")}},
		complete: true,
	}
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 200,
		Body: []byte(`{"state":"open","mergeable":true,"head":{"ref":"topic","sha":"deadbeef"},` +
			`"base":{"ref":"main"}}`),
	})
	client := github.NewClient(transport, nil)

	seen, err := Observe(client, testRef(), Input{Verdicts: src}, time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if src.asked != 1 {
		t.Fatalf("the source was asked %d times, want once", src.asked)
	}
	if len(seen.Checks) != 1 || seen.Checks[0].Name != "go" {
		t.Errorf("Observe used %v, want the source's answer", seen.Checks)
	}
}
