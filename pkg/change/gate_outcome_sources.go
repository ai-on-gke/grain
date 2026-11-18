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

package change

import (
	"context"
	"fmt"
	"github.com/ai-on-gke/grain/pkg/model"
	"time"
)

// RecordOutcomeObservation preserves only exact scoped outcome provenance from
// an ordinary provider read. The shadow evaluator never invokes the provider.
func RecordOutcomeObservation(ctx context.Context, store any, task model.Task, obs Observation, now time.Time) {
	recordOutcomeObservation(ctx, store, task, obs, "", true, now)
}
func recordOutcomeObservation(ctx context.Context, store any, task model.Task, obs Observation, canonical string, outside bool, now time.Time) {
	recorder, ok := store.(interface {
		RecordGateOutcomeSource(context.Context, string, model.GateOutcomeSource)
	})
	if !ok {
		return
	}
	linked := false
	for _, target := range model.TaskChangelists(task) {
		if target == obs.Target {
			linked = true
		}
	}
	if !linked {
		return
	}
	head := ""
	if obs.Snapshot > 0 {
		head = fmt.Sprintf("snapshot/%d", obs.Snapshot)
	}
	state := "open"
	switch obs.Status {
	case StatusSubmitted:
		state = "submitted"
	case StatusAbandoned:
		state = "abandoned"
	case "":
		state = "unknown"
	}
	attribution := "grain-submit"
	if outside {
		attribution = "external-observation"
	}
	if canonical == "" {
		canonical = obs.Target
	}
	recorder.RecordGateOutcomeSource(ctx, task.ID, model.GateOutcomeSource{Resource: obs.Target, Head: head, State: state, Product: canonical, Policy: model.GateOutcomePolicy(task), Attribution: attribution, At: now})
}
