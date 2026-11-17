// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ui_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// attemptsActions is fakeActions that also lists every attempt of a
// run's jobs (github.ActionsJobAttemptsReader).
type attemptsActions struct {
	*fakeActions
	attempts map[int64][]github.ActionsRunJob
}

func (f attemptsActions) ListActionsJobAttempts(owner, repo string, runID int64) ([]github.ActionsRunJob, error) {
	f.note("attempts", owner)
	return f.attempts[runID], nil
}

func TestCIRunAttemptsListsEveryAttemptOfAnActionsRunsJobs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	forge := attemptsActions{fakeActions: newFakeActions(now), attempts: map[int64][]github.ActionsRunJob{42: {
		{ID: 700, RunID: 42, RunAttempt: 1, HeadSHA: "abc", Name: "go", Status: "completed", Conclusion: "failure"},
		{ID: 710, RunID: 42, RunAttempt: 2, HeadSHA: "abc", Name: "go", Status: "completed", Conclusion: "success"},
	}}}
	srv, _, _ := actionsServerOver(t, forge, now)

	code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/attempts", "", "")
	if code != http.StatusOK {
		t.Fatalf("attempts: %d %s", code, body)
	}
	got := decodeString[ui.CIRunAttemptsResponse](t, body)
	if got.TotalCount != 2 || len(got.Jobs) != 2 || got.Jobs[0].RunAttempt != 1 || got.Jobs[1].RunAttempt != 2 ||
		got.Jobs[1].Conclusion == nil || *got.Jobs[1].Conclusion != "success" || got.Jobs[0].HeadSHA != "abc" {
		t.Errorf("attempts = %+v", got)
	}
	if code, body := asAgent(t, srv, "reader", http.MethodGet, "/api/ci/runs/run-g1/attempts", "", ""); code != http.StatusBadRequest {
		t.Errorf("a grain CI run's attempts answered %d %s, want 400", code, body)
	}
	if code, body := asAgent(t, srv, "outsider", http.MethodGet, "/api/ci/runs/gha:acme:widgets:42/attempts", "", ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("a task without ci:read on acme/widgets read its attempts: %d %s", code, body)
	}
}
