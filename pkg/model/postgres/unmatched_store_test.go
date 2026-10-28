package postgres_test

// What a run said when it failed for a reason grain could not name,
// against a real database: it has to survive the round trip whatever a
// provider put in it, a window has to bound what comes back, and the
// newest has to come first -- because the question this table answers is
// "has something changed lately?".

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestUnmatchedFailureRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")

	// A provider's error body, braces, quotes and all: this column holds
	// somebody else's words verbatim, so the round trip is the test.
	text := `claude: api error: 529 {"type":"error","error":{"type":"overloaded_error"}}`
	want := model.UnmatchedFailure{
		RunID: "r1", TaskID: "a1b2", Framework: "claude",
		At: now.UTC(), Text: text, Suspect: true,
	}
	if err := store.RecordUnmatchedFailure(ctx, want); err != nil {
		t.Fatalf("RecordUnmatchedFailure: %v", err)
	}

	got, err := store.UnmatchedFailures(ctx, now.Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d rows, want 1: %+v", len(got), got)
	}
	if !got[0].At.Equal(want.At) {
		t.Errorf("At = %s, want %s", got[0].At, want.At)
	}
	got[0].At = want.At
	if got[0] != want {
		t.Errorf("read back %+v, want %+v", got[0], want)
	}
}

// Recording twice for one run leaves one row. The write happens once per
// run today, and a recovery path that ran it again must not double a log
// an operator reads by counting.
func TestRecordingAnUnmatchedFailureTwiceReplacesIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	seedFinishedRun(t, store, ctx, "r1", "a1b2")

	for _, text := range []string{"first", "second"} {
		if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
			RunID: "r1", TaskID: "a1b2", Framework: "claude", At: now, Text: text,
		}); err != nil {
			t.Fatalf("RecordUnmatchedFailure(%q): %v", text, err)
		}
	}
	got, err := store.UnmatchedFailures(ctx, now.Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	if len(got) != 1 || got[0].Text != "second" {
		t.Fatalf("read %+v, want exactly one row saying \"second\"", got)
	}
}

// The window is what makes this readable at all -- "the last week", not
// "everything this deployment has ever failed at" -- and newest first is
// what puts a rewording at the top of the page it announces itself on.
func TestUnmatchedFailuresAreWindowedNewestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for i, age := range []time.Duration{72 * time.Hour, 2 * time.Hour, time.Hour} {
		runID := string(rune('a' + i))
		seedFinishedRun(t, store, ctx, runID, "t"+runID)
		if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
			RunID: runID, TaskID: "t" + runID, Framework: "claude",
			At: now.Add(-age), Text: runID,
		}); err != nil {
			t.Fatalf("RecordUnmatchedFailure(%s): %v", runID, err)
		}
	}

	got, err := store.UnmatchedFailures(ctx, now.Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	var texts []string
	for _, f := range got {
		texts = append(texts, f.Text)
	}
	if want := "c b"; strings.Join(texts, " ") != want {
		t.Errorf("read %v, want %q: newest first, and the three-day-old one outside the window",
			texts, want)
	}
}

// A limit bounds the answer, because the number of failures a week holds
// is the deployment's business and not the reader's.
func TestUnmatchedFailuresHonoursALimit(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for i := 0; i < 5; i++ {
		runID := string(rune('a' + i))
		seedFinishedRun(t, store, ctx, runID, "t"+runID)
		if err := store.RecordUnmatchedFailure(ctx, model.UnmatchedFailure{
			RunID: runID, TaskID: "t" + runID, Framework: "codex",
			At: now.Add(-time.Duration(i) * time.Minute), Text: runID,
		}); err != nil {
			t.Fatalf("RecordUnmatchedFailure(%s): %v", runID, err)
		}
	}
	got, err := store.UnmatchedFailures(ctx, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatalf("UnmatchedFailures: %v", err)
	}
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Errorf("read %+v, want the two newest", got)
	}
}
