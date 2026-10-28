package orchestrator_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// New review threads on a grain-owned pull request -- a bot's or a
// person's -- send one run to fix or answer them, once per new batch.
func TestNewReviewThreadsOnAPullRequestSendOneRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := completedTask(t, ctx, store, sim, client, "t1", repo, true, "")
	number := sim.PullRequests[0].Number
	line := 12

	// grain's own draft review (a review task's findings) is not a thread
	// for a run to fix: those are the owner's questions.
	if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkDraftReview, Target: "77"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sim.ReviewComments = map[int][]github.ReviewComment{number: {
		{ID: 501, User: "gemini-code-assist[bot]", Body: "This loop never ends.", Path: "run.go", Line: &line},
		{ID: 502, User: "grain", Body: "draft finding", Path: "run.go", Line: &line, ReviewID: 77},
	}}

	sync := func(at time.Time) {
		t.Helper()
		if err := orchestrator.SyncReviewThreads(ctx, store, client, at); err != nil {
			t.Fatal(err)
		}
	}
	sync(baseTime.Add(time.Minute))
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q -- a new thread sends a run", s, model.StateQueued)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	note := bodies[len(bodies)-1]
	for _, want := range []string{"1 new review thread", "gemini-code-assist[bot]", "run.go:12", "This loop never ends.", "resolve_review_thread"} {
		if !strings.Contains(note, want) {
			t.Errorf("the run's note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "draft finding") {
		t.Errorf("grain's own draft review was counted as a thread:\n%s", note)
	}

	// The run finishes, having replied in the thread without resolving it,
	// and the bot answers back. Neither is a new thread: no second run.
	finish := func(at time.Time) {
		t.Helper()
		if err := store.ObserveField(ctx, task.ID, at, func(o *model.Observation) { o.CompletedAt = &at }); err != nil {
			t.Fatal(err)
		}
	}
	finish(baseTime.Add(time.Hour))
	sim.ReviewComments[number] = append(sim.ReviewComments[number],
		github.ReviewComment{ID: 503, User: "grain", Body: "Fixed in abc123.", Path: "run.go", Line: &line, InReplyTo: 501},
		github.ReviewComment{ID: 504, User: "gemini-code-assist[bot]", Body: "Thanks!", Path: "run.go", Line: &line, InReplyTo: 501},
	)
	sync(baseTime.Add(time.Hour + time.Minute))
	if s := stateOf(t, ctx, store, task.ID); s != model.StateCompleted {
		t.Fatalf("state = %q, want %q -- replies in a handled thread send no run", s, model.StateCompleted)
	}

	// A person opens a new one: one more run, for that one only.
	sim.ReviewComments[number] = append(sim.ReviewComments[number],
		github.ReviewComment{ID: 505, User: "bsalmon", Body: "Rename this.", Path: "api.go", Line: &line})
	sync(baseTime.Add(2 * time.Hour))
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	bodies = commentBodies(t, ctx, store, task.ID)
	if note := bodies[len(bodies)-1]; !strings.Contains(note, "Rename this.") || strings.Contains(note, "never ends") {
		t.Errorf("the second run's note is not about the new thread alone:\n%s", note)
	}

	// Every thread is recorded, so none is sent twice.
	recorded := map[string]bool{}
	for _, l := range reloaded(t, ctx, store, task.ID).Links {
		if l.Kind == model.LinkReviewThread {
			recorded[l.Target] = true
		}
	}
	for _, id := range []int{501, 505} {
		if !recorded[strconv.Itoa(id)] {
			t.Errorf("thread %d is not recorded: %v", id, recorded)
		}
	}
}

// A thread already resolved when grain first sees it sends no run.
func TestAResolvedReviewThreadSendsNoRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := completedTask(t, ctx, store, sim, client, "t1", repo, true, "")
	number := sim.PullRequests[0].Number
	line := 3
	sim.ReviewComments = map[int][]github.ReviewComment{number: {
		{ID: 601, User: "gemini-code-assist[bot]", Body: "nit", Path: "a.go", Line: &line},
	}}
	sim.ResolvedThreads = map[string]bool{"thread-601": true}
	if err := orchestrator.SyncReviewThreads(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateCompleted {
		t.Fatalf("state = %q, want %q", s, model.StateCompleted)
	}
}
