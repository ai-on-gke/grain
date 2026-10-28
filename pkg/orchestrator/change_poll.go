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

package orchestrator

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ChangePollInterval is how often every live non-PR change is handed to
// its provider's change.Poller. The provider decides which of them are
// due (a CL's own 2/10/30-minute intervals and its rate limit); this is
// only how often it is asked.
const ChangePollInterval = time.Minute

// ChangePollStore is what the change poll reads: every task, and whether
// one has closed.
type ChangePollStore interface {
	ListTasks(ctx context.Context) ([]model.Task, error)
	GetObservation(ctx context.Context, taskID string) (*model.Observation, error)
}

// PollChanges runs PollChangesOnce every ChangePollInterval until ctx
// ends (changelists.md §3, G3-3).
func PollChanges(ctx context.Context, store ChangePollStore) {
	t := time.NewTicker(ChangePollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			PollChangesOnce(ctx, store)
		}
	}
}

// PollChangesOnce hands every open task with a changelist link to each
// registered change.Poller whose kind is not blocked on expired
// credentials, and records what each answered (change.Remember), where the
// task view and inbox read their presubmit badge. It reports how many
// observations were recorded.
func PollChangesOnce(ctx context.Context, store ChangePollStore) int {
	var pollers []change.Poller
	for _, kind := range change.Kinds() {
		p, _ := change.Lookup(kind)
		poller, ok := p.(change.Poller)
		if !ok {
			continue
		}
		if change.HealthOf(change.HealthKind(p)).Blocked != "" {
			continue
		}
		pollers = append(pollers, poller)
	}
	if len(pollers) == 0 {
		return 0
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		log.Printf("grain: polling changes: listing tasks: %v", err)
		return 0
	}
	var live []model.Task
	for _, task := range tasks {
		if change.Primary(task) == "" {
			continue
		}
		if obs, err := store.GetObservation(ctx, task.ID); err == nil && obs != nil && obs.ClosedAt != nil {
			continue
		}
		live = append(live, task)
	}
	if len(live) == 0 {
		return 0
	}
	n := 0
	for _, poller := range pollers {
		for _, obs := range poller.PollTick(ctx, live) {
			change.Remember(obs)
			n++
		}
	}
	return n
}
