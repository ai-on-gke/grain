package orchestrator

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/github"
)

func commit(message string) github.Commit {
	return github.Commit{SHA: "sha-" + strings.Fields(message + " x")[0], Message: message}
}

// One commit is the whole description: its message, verbatim, with no
// list of other commits bolted onto it.
func TestDescribeCommitsUsesTheOnlyCommitVerbatim(t *testing.T) {
	t.Parallel()
	got := describeCommits([]github.Commit{commit("Add the parser\n\nIt reads the header first,\nthen the body.")})
	want := "Add the parser\n\nIt reads the header first,\nthen the body."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The commit that explains the change leads, even when it is neither the
// first nor the last -- which is the case grain's own push/check/repair
// loop produces every time CI comes back red.
func TestDescribeCommitsLeadsWithTheCommitThatHasABody(t *testing.T) {
	t.Parallel()
	got := describeCommits([]github.Commit{
		commit("wip"),
		commit("Add the parser\n\nWhy it was needed."),
		commit("Fix the vet warning"),
	})
	if !strings.HasPrefix(got, "Add the parser\n\nWhy it was needed.") {
		t.Fatalf("expected the explained commit to lead, got %q", got)
	}
	for _, want := range []string{"Also on this branch:", "- wip", "- Fix the vet warning"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	// Listed by summary line only: a body belongs to the lead commit's
	// account of the change, not to a bullet list.
	if strings.Count(got, "Why it was needed.") != 1 {
		t.Fatalf("expected the body once, got %q", got)
	}
}

// No commit has a body: the first one still leads, and every other is
// still named. Nothing on the branch goes unmentioned.
func TestDescribeCommitsFallsBackToTheFirstCommit(t *testing.T) {
	t.Parallel()
	got := describeCommits([]github.Commit{commit("Add the parser"), commit("Fix the vet warning")})
	if !strings.HasPrefix(got, "Add the parser") || !strings.Contains(got, "- Fix the vet warning") {
		t.Fatalf("got %q", got)
	}
}

// "Merge main into grain/task-1" is a fact about the branch's shape, not
// about the change -- and a run told to merge its base back in when it
// conflicts produces them routinely.
func TestDescribeCommitsDropsMergeCommits(t *testing.T) {
	t.Parallel()
	merge := github.Commit{SHA: "m", Message: "Merge main into grain/task-1", Merge: true}
	got := describeCommits([]github.Commit{commit("Add the parser\n\nWhy."), merge})
	if strings.Contains(got, "Merge main") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "Also on this branch") {
		t.Fatalf("a dropped merge is not another commit to list, got %q", got)
	}
}

// Nothing to say is said as nothing at all, never as an empty
// description -- callers read "" as "leave the description alone".
func TestDescribeCommitsIsEmptyWithoutWrittenCommits(t *testing.T) {
	t.Parallel()
	if got := describeCommits(nil); got != "" {
		t.Fatalf("got %q", got)
	}
	merge := github.Commit{SHA: "m", Message: "Merge main into grain/task-1", Merge: true}
	if got := describeCommits([]github.Commit{merge}); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := describeCommits([]github.Commit{{SHA: "s", Message: "   \n"}}); got != "" {
		t.Fatalf("got %q", got)
	}
}

// GitHub refuses a body over 65536 bytes, and that refusal would land on
// the call that opens the pull request -- stranding a finished run's work
// on a branch nobody was told about. A description too long to send is
// cut instead.
func TestDescribeCommitsCapsAnEnormousDescription(t *testing.T) {
	t.Parallel()
	got := describeCommits([]github.Commit{
		{SHA: "a", Message: "Do the thing\n\n" + strings.Repeat("é ", maxDescriptionBytes)},
	})
	if len(got) > maxDescriptionBytes+100 {
		t.Fatalf("description is %d bytes, want it capped near %d", len(got), maxDescriptionBytes)
	}
	if !strings.Contains(got, "Truncated") {
		t.Fatalf("a cut description says so: %q", got[len(got)-100:])
	}
	if !strings.HasPrefix(got, "Do the thing") {
		t.Fatalf("the cut takes the tail, not the head: %q", got[:40])
	}
	if !utf8.ValidString(got) {
		t.Fatal("the cut landed inside a rune")
	}
}

// The pull request is named after the change, and the name is the summary
// line of the very commit the description leads with -- not the first
// commit on the branch, which on grain's own push/check/repair loop is
// routinely "wip".
func TestTitleFromCommitsIsTheLeadCommitsSummaryLine(t *testing.T) {
	t.Parallel()
	got := titleFromCommits([]github.Commit{
		commit("wip"),
		commit("Fix the off-by-one in the header parser\n\nIt stopped at the first blank line."),
		commit("Fix the vet warning"),
	})
	if got != "Fix the off-by-one in the header parser" {
		t.Fatalf("got %q", got)
	}
}

// No commit explains itself: the first one still names the branch, the
// same fallback describeCommits makes.
func TestTitleFromCommitsFallsBackToTheFirstCommit(t *testing.T) {
	t.Parallel()
	got := titleFromCommits([]github.Commit{commit("Add the parser"), commit("Fix the vet warning")})
	if got != "Add the parser" {
		t.Fatalf("got %q", got)
	}
}

// Nothing to say is said as nothing at all: callers read "" as "fall back
// to the task's own title", never as a pull request called "".
func TestTitleFromCommitsIsEmptyWithoutWrittenCommits(t *testing.T) {
	t.Parallel()
	merge := github.Commit{SHA: "m", Message: "Merge main into grain/task-1", Merge: true}
	for _, commits := range [][]github.Commit{
		nil,
		{merge},
		{{SHA: "s", Message: "   \n"}},
	} {
		if got := titleFromCommits(commits); got != "" {
			t.Fatalf("got %q for %+v", got, commits)
		}
	}
}

// GitHub refuses a title over 256 characters, and that refusal would land
// on the call that opens the pull request -- capDescription's own
// argument, one line up.
func TestTitleFromCommitsCapsAnEnormousSummaryLine(t *testing.T) {
	t.Parallel()
	got := titleFromCommits([]github.Commit{commit(strings.Repeat("é ", maxTitleBytes) + "\n\nWhy.")})
	if len(got) > maxTitleBytes+10 {
		t.Fatalf("title is %d bytes, want it capped near %d", len(got), maxTitleBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("a cut title says so: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("the cut landed inside a rune")
	}
}

func TestPullRequestBodyIsJustTheFooterWithoutADescription(t *testing.T) {
	t.Parallel()
	if got := pullRequestBody("", "t1", 0); got != "Automated change for grain task t1." {
		t.Fatalf("got %q", got)
	}
}

func TestPullRequestBodyIncludesIssueClosingDirective(t *testing.T) {
	t.Parallel()

	// With description
	got := pullRequestBody("Add the parser\n\nWhy it was needed.", "t1", 123)
	want := "Add the parser\n\nWhy it was needed.\n\nFixes #123\n\n---\nAutomated change for grain task t1."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// Without description
	gotBare := pullRequestBody("", "t1", 123)
	wantBare := "Fixes #123\n\n---\nAutomated change for grain task t1."
	if gotBare != wantBare {
		t.Fatalf("got %q, want %q", gotBare, wantBare)
	}
}

func TestPullRequestBodyDoesNotDuplicateExistingClosingDirective(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"Fixes #123\n\nDetailed explanation.",
		"fixes #123",
		"Fixed #123",
		"Fixes: #123",
		"Closes #123",
		"closed #123",
		"Resolves #123",
		"Fixes acme/widgets#123",
	} {
		got := pullRequestBody(text, "t1", 123)
		if strings.Count(got, "#123") != 1 {
			t.Errorf("for input %q, expected exactly one occurrence of #123, got %q", text, got)
		}
		if !strings.HasSuffix(got, "---\nAutomated change for grain task t1.") {
			t.Errorf("for input %q, expected proper footer, got %q", text, got)
		}
	}

	// But a different issue or non-closing reference still gets Fixes #123 appended
	different := pullRequestBody("Fixes #456", "t1", 123)
	if !strings.Contains(different, "Fixes #123") {
		t.Fatalf("expected Fixes #123 when only #456 was closed, got %q", different)
	}

	nonClosing := pullRequestBody("Related to issue #123", "t1", 123)
	if !strings.Contains(nonClosing, "Fixes #123") {
		t.Fatalf("expected Fixes #123 when only non-closing reference existed, got %q", nonClosing)
	}
}

// grainAuthored is what stands between a refresh and a human's own
// writing: grain's footer says grain wrote it, and anything else says a
// reviewer did.
func TestGrainAuthoredRecognisesOnlyGrainsOwnBodies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"a body grain wrote", pullRequestBody("Add the parser", "t1", 0), true},
		{"a body grain wrote with issue", pullRequestBody("Add the parser", "t1", 42), true},
		{"a bare issue body grain wrote", pullRequestBody("", "t1", 42), true},
		{"the bare footer an older grain wrote", descriptionFooter("t1"), true},
		{"an empty body", "  \n", true},
		{"a body a human replaced", "I rewrote this by hand.", false},
		{"grain's body a human appended to", pullRequestBody("Add the parser", "t1", 0) +
			"\n\nReviewer: test this against staging first.", false},
		{"another task's footer", descriptionFooter("t2"), false},
	} {
		if got := grainAuthored(tc.body, "t1"); got != tc.want {
			t.Fatalf("%s: grainAuthored = %v, want %v", tc.name, got, tc.want)
		}
	}
}
