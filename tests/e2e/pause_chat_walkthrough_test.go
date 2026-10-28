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

// This file is docs/test-plans/2026-10-05.md §9 step 3, and the end of
// step 2 (a short pause expiring), driven through real cycles. Step 3: the director session filed by its reconciler, a person's
// messages posted through ui.Client.AddComment, dispatch.Cycle and
// RunDispatch running the session's runs, and ProcessResult and the
// session reconciler deciding what the session does when a run ends.
// The only stand-in is the model: chatFramework answers in text, and on
// its first run a person says something while it is writing that answer
// (docs/test-runs/2026-10-05-model-source-pause-chat-standing.md).
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// chatFramework is the scripted model. Each run records the prompt it
// was given and answers in plain text; the first run calls duringFirst
// before it returns, which is "while it writes its final answer".
type chatFramework struct {
	clock       *burstClock
	duringFirst func()

	mu      sync.Mutex
	prompts []string
}

func (f *chatFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, cfg.Prompt)
	n := len(f.prompts)
	f.mu.Unlock()
	f.clock.Advance(10 * time.Second)
	if n == 1 && f.duringFirst != nil {
		f.duringFirst()
	}
	f.clock.Advance(5 * time.Second)
	return &agent.Result{FinalText: "answer " + strconv.Itoa(n)}, nil
}

func (f *chatFramework) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func TestWalkthroughAMessagePostedWhileTheRunAnswersIsDispatchedFor(t *testing.T) {
	clock := &burstClock{now: baseTime}
	fw := &chatFramework{clock: clock}
	w := newWorld(t)
	w.newRepo("acme", "grain-home")
	deps := orchestrator.Deps{
		Store: w.store, Client: simClient(w, "acme", "grain-home"), Sandboxes: worldSandboxes{w}, MaxWorkers: 1,
		Framework: func(context.Context, orchestrator.AgentChoice) (agent.Framework, error) { return fw, nil },
		Config: orchestrator.Config{
			Pause:     &orchestrator.Pause{},
			Now:       clock.Now,
			StateRepo: func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "grain-home"} },
		},
	}
	alice := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice"), Capabilities: ui.OfferedCapabilities()}, w.store)
	alice.Now = clock.Now

	// The first cycle files the director and parks it on its opening.
	cycle(t, w, deps, clock.Now())
	var director model.Task
	tasks, err := w.store.ListTasks(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Origin.Reason == model.ReasonConsoleSession {
			director = task
		}
	}
	if director.ID == "" || !director.Interactive {
		t.Fatalf("no interactive director session task filed: %+v", tasks)
	}
	assertState(w, director.ID, model.StateAwaitingReply, false)

	const second = "and while you are at it, what is queued?"
	fw.duringFirst = func() {
		if err := alice.AddComment(w.ctx, director.ID, second, nil); err != nil {
			t.Errorf("posting during the run: %v", err)
		}
	}
	clock.Advance(time.Minute)
	if err := alice.AddComment(w.ctx, director.ID, "what is this deployment doing?", nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	cycle(t, w, deps, clock.Now())
	if got := len(fw.Prompts()); got != 1 {
		t.Fatalf("the first exchange ran %d times, want 1", got)
	}
	if strings.Contains(fw.Prompts()[0], second) {
		t.Fatal("the first run's prompt already held the message posted during it")
	}

	// The run ended with a message it was never shown: the session is
	// requeued for it, not parked on "Waiting for you.", and the last
	// thing in the conversation is the person's, not grain's.
	assertState(w, director.ID, model.StateQueued, false)
	obs, err := w.store.GetObservation(w.ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	comments, err := w.store.Comments(w.ctx, director.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.DeliveredCommentID == nil {
		t.Fatal("no delivered mark recorded for the first run")
	}
	var secondID int64
	for _, c := range comments {
		if strings.Contains(c.Body, "Waiting for you") && c.ID > *obs.DeliveredCommentID {
			t.Errorf("session parked on %q after the run", c.Body)
		}
		if c.Body == second {
			secondID = c.ID
		}
	}
	if secondID == 0 || *obs.DeliveredCommentID >= secondID {
		t.Fatalf("delivered mark = %v, want it before the unseen comment %d", obs.DeliveredCommentID, secondID)
	}

	// The next cycle dispatches the session again, with the message in
	// its prompt, and that run's ending parks it on the ordinary line.
	clock.Advance(time.Minute)
	cycle(t, w, deps, clock.Now())
	prompts := fw.Prompts()
	if len(prompts) != 2 {
		t.Fatalf("the session ran %d times, want a second run for the unseen message", len(prompts))
	}
	if !strings.Contains(prompts[1], second) {
		t.Errorf("the second run's prompt does not carry %q:\n%s", second, prompts[1])
	}
	clock.Advance(time.Minute)
	cycle(t, w, deps, clock.Now())
	assertState(w, director.ID, model.StateAwaitingReply, false)
	if got := len(fw.Prompts()); got != 2 {
		t.Errorf("the session ran %d times, want no third run once everything was answered", got)
	}

	// POST /delivered refuses a comment that is not on the task.
	other := fileIssue(w, "chat-other", human("erin"), model.RepoRef{Owner: "acme", Name: "grain-home"})
	if err := alice.AddComment(w.ctx, other.ID, "a comment elsewhere", nil); err != nil {
		t.Fatal(err)
	}
	otherComments, err := w.store.Comments(w.ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := otherComments[len(otherComments)-1].ID
	srv := httptest.NewServer(accessCodeServer(alice))
	defer srv.Close()
	for _, tc := range []struct {
		through int64
		want    int
	}{
		{foreign, http.StatusBadRequest},
		{secondID, http.StatusOK},
	} {
		body := strings.NewReader(`{"through":` + strconv.FormatInt(tc.through, 10) + `}`)
		resp, err := accessPost(srv.URL+"/api/tasks/"+director.ID+"/delivered", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("POST /delivered through=%d: %d, want %d", tc.through, resp.StatusCode, tc.want)
		}
	}
}

// limitedFramework meets a provider's usage limit on its first run, with
// the provider's reset resetAfter away on the shared clock, and succeeds
// in text on every run after it.
type limitedFramework struct {
	clock      *burstClock
	resetAfter time.Duration

	mu   sync.Mutex
	runs int
}

func (f *limitedFramework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.mu.Lock()
	f.runs++
	n := f.runs
	f.mu.Unlock()
	f.clock.Advance(5 * time.Second)
	if n == 1 {
		return &agent.Result{}, &agent.UsageLimitError{
			Message: "walk quota exhausted",
			ResetAt: f.clock.Now().Add(f.resetAfter),
		}
	}
	return &agent.Result{FinalText: "done"}, nil
}

func (f *limitedFramework) Runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// §9 step 2's ending: a short usage-limit pause, begun by a real run,
// reads as paused on GET /api/pause and /api/config, holds a queued task
// a person asked to run now, and once its instant passes reads as clear
// and the next cycle dispatches that task.
func TestWalkthroughAShortPauseExpiresAndTheQueuedTaskDispatches(t *testing.T) {
	clock := &burstClock{now: baseTime}
	fw := &limitedFramework{clock: clock, resetAfter: 90 * time.Second}
	w, deps, repo := burstWorld(t, fw, clock, 1)
	pause := deps.Config.Pause
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
		AgentPause:   pause,
	}, w.store)
	client.Now = clock.Now
	srv := httptest.NewServer(accessCodeServer(client))
	defer srv.Close()

	fileIssue(w, "sp-1", human("erin"), repo)
	cycle(t, w, deps, clock.Now())
	if fw.Runs() != 1 {
		t.Fatalf("first cycle ran %d times, want 1", fw.Runs())
	}
	type pauseBody struct {
		Paused bool   `json:"paused"`
		Reason string `json:"reason"`
	}
	read := func(path string) pauseBody {
		t.Helper()
		resp, err := accessGet(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Pause      *pauseBody `json:"pause"`
			AgentPause *pauseBody `json:"agentPause"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		switch {
		case body.Pause != nil:
			return *body.Pause
		case body.AgentPause != nil:
			return *body.AgentPause
		}
		return pauseBody{}
	}
	for _, path := range []string{"/api/pause", "/api/config"} {
		if got := read(path); !got.Paused || !strings.Contains(got.Reason, "walk quota exhausted") {
			t.Fatalf("GET %s = %+v, want a pause with the provider's reason", path, got)
		}
	}

	// Run now records the request and starts nothing while the pause lasts.
	resp, err := accessPost(srv.URL+"/api/tasks/sp-1/run-now", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("POST run-now: %d", resp.StatusCode)
	}
	clock.Advance(30 * time.Second)
	cycle(t, w, deps, clock.Now())
	if fw.Runs() != 1 {
		t.Fatalf("a cycle inside the pause ran the framework (%d runs)", fw.Runs())
	}
	assertState(w, "sp-1", model.StateQueued, false)

	// Past the reset: every read clears and the next cycle dispatches.
	clock.Advance(90 * time.Second)
	for _, path := range []string{"/api/pause", "/api/config"} {
		if got := read(path); got.Paused {
			t.Errorf("GET %s = %+v after the reset, want it clear", path, got)
		}
	}
	cycle(t, w, deps, clock.Now())
	if fw.Runs() != 2 {
		t.Fatalf("after the pause the framework has run %d times, want the queued task dispatched", fw.Runs())
	}
	if len(pause.Pauses(clock.Now())) != 0 {
		t.Error("still paused after the reset")
	}
}
