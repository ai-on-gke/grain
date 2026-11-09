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
	// Its note leads with the new thread, and lists the first one -- never
	// resolved -- beneath it as still open: the merge waits on both.
	bodies = commentBodies(t, ctx, store, task.ID)
	note = bodies[len(bodies)-1]
	newPart, oldPart, split := strings.Cut(note, "from before")
	if !split || !strings.Contains(newPart, "1 new review thread") || !strings.Contains(newPart, "Rename this.") ||
		strings.Contains(newPart, "never ends") || !strings.Contains(oldPart, "never ends") ||
		!strings.Contains(oldPart, "thread `thread-501`") {
		t.Errorf("the second run's note should lead with the new thread and list the open older one after it:\n%s", note)
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

// Five comments in one bot review are five threads, and the run is told
// about all five -- including one GitHub calls outdated (REST gives it no
// line), which GitHub's conversation-resolution rule still counts. That
// one used to be recorded and dropped, so the task fixed four and the
// merge was refused over the fifth (task 3016, comment 234726).
func TestAReviewOfFiveBotCommentsRelaysAllFive(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := completedTask(t, ctx, store, sim, client, "t1", repo, true, "")
	number := sim.PullRequests[0].Number
	line := func(n int) *int { return &n }
	sim.ReviewComments = map[int][]github.ReviewComment{number: {
		{ID: 234726, User: "code-review[bot]", Body: "This runs in a subshell, so the exit is lost.", Path: "scripts/run.sh", ReviewID: 9},
		{ID: 234727, User: "code-review[bot]", Body: "In conjunction with fixing the subshell issue, quote this.", Path: "scripts/run.sh", Line: line(14), ReviewID: 9},
		{ID: 234728, User: "code-review[bot]", Body: "Unused variable.", Path: "scripts/run.sh", Line: line(20), ReviewID: 9},
		{ID: 234729, User: "code-review[bot]", Body: "Missing error check.", Path: "pkg/a/a.go", Line: line(3), ReviewID: 9},
		{ID: 234730, User: "code-review[bot]", Body: "Typo in comment.", Path: "pkg/a/a.go", Line: line(8), ReviewID: 9},
	}}
	if err := orchestrator.SyncReviewThreads(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	note := bodies[len(bodies)-1]
	if !strings.Contains(note, "5 new review threads") {
		t.Errorf("the note does not count five threads:\n%s", note)
	}
	for _, id := range []int{234726, 234727, 234728, 234729, 234730} {
		if !strings.Contains(note, "comment "+strconv.Itoa(id)) || !strings.Contains(note, "thread `thread-"+strconv.Itoa(id)+"`") {
			t.Errorf("the note lacks comment %d with its thread ID:\n%s", id, note)
		}
	}
	if !strings.Contains(note, "scripts/run.sh (outdated)") || !strings.Contains(note, "subshell, so the exit is lost") {
		t.Errorf("the outdated thread is not listed with its path and body:\n%s", note)
	}
}
