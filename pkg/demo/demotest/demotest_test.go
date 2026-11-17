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

package demotest

import (
	"net/http"
	"testing"
)

// seededCLTask is the seeded changelist task whose run left two replies
// to its reviewer as drafts.
const seededCLTask = "Port the retry budget to the depot"

// waitingDrafts is how many replies the seeded CL task's Publish replies
// button says are waiting.
func waitingDrafts(t *testing.T, s *Server) int {
	t.Helper()
	id := s.Seed.Tasks[seededCLTask]
	if id == "" {
		t.Fatalf("no seeded task titled %q among %v", seededCLTask, s.Seed.Tasks)
	}
	var task struct {
		CLReview *struct {
			Drafts int `json:"drafts"`
		} `json:"clReview"`
	}
	s.Do(t, http.MethodGet, "/api/tasks/"+id, nil).WantOK(t).JSON(t, &task)
	if task.CLReview == nil {
		t.Fatalf("seeded task %s has no CL review", id)
	}
	return task.CLReview.Drafts
}

// The demo's CL state is the process's (pkg/change), but every Start
// after the last demo closed seeds it afresh: the seeded CL task reports
// its two waiting drafts however many demos came before it, and whatever
// they published. Not parallel: demos alive at once share that state.
func TestEveryStartSeedsTheChangelistDraftsAfresh(t *testing.T) {
	first := Start(t, Options{})
	if got := waitingDrafts(t, first); got != 2 {
		t.Fatalf("first demo: %d waiting drafts, want 2", got)
	}
	id := first.Seed.Tasks[seededCLTask]
	first.Do(t, http.MethodPost, "/api/tasks/"+id+"/change-publish", map[string]any{}).WantOK(t)
	if got := waitingDrafts(t, first); got != 0 {
		t.Fatalf("after Publish replies: %d waiting drafts, want 0", got)
	}

	first.Restart()
	if got := waitingDrafts(t, first); got != 2 {
		t.Errorf("after Restart: %d waiting drafts, want 2", got)
	}
	first.stop()

	for i := range 2 {
		next := Start(t, Options{})
		if got := waitingDrafts(t, next); got != 2 {
			t.Errorf("demo %d started after the first closed: %d waiting drafts, want 2", i+2, got)
		}
		next.stop()
	}
}

// A demo started beside a live one shares its CL state rather than
// seeding it again on top: still two drafts, not four, and none left once
// both have closed and another starts.
func TestDemosAliveTogetherShareOneSeed(t *testing.T) {
	a := Start(t, Options{})
	b := Start(t, Options{})
	for name, s := range map[string]*Server{"a": a, "b": b} {
		if got := waitingDrafts(t, s); got != 2 {
			t.Errorf("demo %s: %d waiting drafts, want 2", name, got)
		}
	}
	a.stop()
	if got := waitingDrafts(t, b); got != 2 {
		t.Errorf("demo b after a closed: %d waiting drafts, want 2", got)
	}
	b.stop()
	c := Start(t, Options{})
	if got := waitingDrafts(t, c); got != 2 {
		t.Errorf("demo started after both closed: %d waiting drafts, want 2", got)
	}
}
