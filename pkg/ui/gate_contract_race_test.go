// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGateObservationContractChangeDuringCIRequiresFreshCollection(t *testing.T) {
	srv, c := testServer(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{Title: "Observe a new scoped Submit requirement"})
	must(err)
	must(c.Store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}))
	c.Config.GateObservationTaskIDs = []string{task.ID}
	forge := newShadowForge()
	c.Config.GitHubPullRequests = forge
	acceptShadowRules(t, c, forge)
	now := baseTime.Add(time.Minute)
	c.Now = func() time.Time { return now }
	read := func(want string) ui.TaskDetail {
		t.Helper()
		code, body := serve(t, srv, http.MethodGet, "/api/tasks/"+task.ID, "")
		if code != http.StatusOK {
			t.Fatalf("task GET %d %s", code, body)
		}
		var detail ui.TaskDetail
		must(json.Unmarshal([]byte(body), &detail))
		if detail.GateObservations == nil {
			t.Fatal("missing sidecar")
		}
		found := false
		for i, b := range detail.GateObservations.Bindings {
			if b.Type == "checks" && !b.Origin.Imported {
				found = true
				if got := detail.GateObservations.Results[i].Result.Status; got != want {
					t.Fatalf("current CI got %s want %s", got, want)
				}
			}
		}
		if !found {
			t.Fatal("current CI monitor missing")
		}
		return detail
	}
	before := read("met")
	var written *model.Task
	var observed *model.Observation
	var wake <-chan struct{}
	var commits uint64
	forge.onChecks = func() {
		forge.onChecks = nil
		// The external read is still in progress. This legacy writer establishes a
		// new contract that transactional acceptance must see, even when ordinary
		// source rows are reused inside that transaction.
		must(c.Store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
			task.Links = append(task.Links, model.ApprovalRequiredLink("trigger:new-path-policy", []string{"alice"}))
			return nil
		}))
		written, err = c.Store.GetTask(ctx, task.ID)
		must(err)
		observed, err = c.Store.GetObservation(ctx, task.ID)
		must(err)
		wake, commits = c.Store.Changed(), c.Store.CommitCount()
	}
	now = now.Add(time.Second)
	invalidated := read("unknown")
	if invalidated.GateObservations.SourceRevision == before.GateObservations.SourceRevision {
		t.Fatal("changed contract did not invalidate the collection")
	}
	after, err := c.Store.GetTask(ctx, task.ID)
	must(err)
	afterObserved, err := c.Store.GetObservation(ctx, task.ID)
	must(err)
	if !reflect.DeepEqual(written, after) || !reflect.DeepEqual(observed, afterObserved) || c.Store.CommitCount() != commits {
		t.Fatal("shadow modified the new legacy contract")
	}
	select {
	case <-wake:
		t.Fatal("shadow dispatched after the contract writer")
	default:
	}
	now = now.Add(time.Second)
	fresh := read("met")
	after, err = c.Store.GetTask(ctx, task.ID)
	must(err)
	if len(model.PendingApprovals(*after)) != 1 {
		t.Fatal("passing recollected CI cleared the scoped Submit requirement")
	}
	for i, b := range fresh.GateObservations.Bindings {
		if b.Type == "approval" && fresh.GateObservations.Results[i].Result.Status == "met" {
			t.Fatal("CI success became approval proof")
		}
	}
}
