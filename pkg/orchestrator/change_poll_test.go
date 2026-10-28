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
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// pollingCLs is a changelist provider that polls: every task it is
// handed comes back as a failing observation of its primary CL.
type pollingCLs struct{ handed [][]string }

func (p *pollingCLs) Kind() model.LinkKind { return model.LinkChangelist }
func (p *pollingCLs) Observe(context.Context, model.Task, string) (change.Observation, error) {
	return change.Observation{}, nil
}
func (p *pollingCLs) Threads(context.Context, model.Task, string) ([]change.Thread, error) {
	return nil, nil
}
func (p *pollingCLs) Submit(context.Context, model.Task, string, change.Person) (change.Result, error) {
	return change.Result{}, nil
}
func (p *pollingCLs) Sync(context.Context, model.Task, string) (change.SyncResult, error) {
	return change.SyncResult{}, nil
}
func (p *pollingCLs) PollTick(_ context.Context, tasks []model.Task) []change.Observation {
	var ids []string
	var out []change.Observation
	for _, t := range tasks {
		ids = append(ids, t.ID)
		out = append(out, change.Observation{Target: change.Primary(t), Health: change.HealthFailing,
			Presubmit: change.PresubmitFailed, Checks: []change.Check{{Name: "//a:test", State: "failed"}, {Name: "//b:test", State: "passed"}}})
	}
	p.handed = append(p.handed, ids)
	return out
}

type pollStore struct {
	tasks  []model.Task
	closed map[string]bool
}

func (s pollStore) ListTasks(context.Context) ([]model.Task, error) { return s.tasks, nil }
func (s pollStore) GetObservation(_ context.Context, id string) (*model.Observation, error) {
	if s.closed[id] {
		now := time.Now()
		return &model.Observation{TaskID: id, ClosedAt: &now}, nil
	}
	return nil, nil
}

func TestPollChangesHandsLiveCLsToThePollerAndRemembersWhatItSaw(t *testing.T) {
	poller := &pollingCLs{}
	change.Register(poller)
	t.Cleanup(func() {
		change.Unregister(model.LinkChangelist)
		change.Forget("cl/1")
		change.ClearBlocked(string(model.LinkChangelist))
	})
	cl := func(n string) []model.Link { return []model.Link{{Kind: model.LinkChangelist, Target: n}} }
	store := pollStore{
		tasks: []model.Task{
			{ID: "1", Links: cl("cl/1")},
			{ID: "2"},                    // a git task: never handed over
			{ID: "3", Links: cl("cl/3")}, // closed: its CL is not live
		},
		closed: map[string]bool{"3": true},
	}
	if n := orchestrator.PollChangesOnce(context.Background(), store); n != 1 {
		t.Fatalf("recorded %d observations, want 1", n)
	}
	if len(poller.handed) != 1 || len(poller.handed[0]) != 1 || poller.handed[0][0] != "1" {
		t.Fatalf("handed %v, want only task 1", poller.handed)
	}
	obs, ok := change.LastObserved("cl/1")
	if !ok || obs.Presubmit != change.PresubmitFailed {
		t.Fatalf("remembered %+v, %v", obs, ok)
	}
	if failing := obs.Failing(); len(failing) != 1 || failing[0] != "//a:test" {
		t.Fatalf("failing = %v, want only //a:test", failing)
	}

	// Blocked on credentials: nothing is polled until a Check clears it.
	change.MarkBlocked(string(model.LinkChangelist), change.BlockedCredentials, "expired", time.Now())
	if n := orchestrator.PollChangesOnce(context.Background(), store); n != 0 || len(poller.handed) != 1 {
		t.Fatalf("polled while blocked: %d observations, handed %v", n, poller.handed)
	}
}
