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
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// reconcileRollover rolls every open durable task whose record is past a
// limit (model.RolloverRunLimit, RolloverCommentLimit, RolloverAgeLimit)
// over to a successor, under grain's name
// (design/design/durable-tasks.design.md#DT-15, "Rollover to a successor
// task").
//
// A task model.RolloverRefusal turns away is left for a later cycle
// without a word: a running task rolls over once its run has ended, and
// a console session rolls over by its own refiling when it is closed, so
// neither is an error to report every cycle.
func reconcileRollover(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	past, err := deps.Store.OpenDurableTasksPastRolloverLimit(ctx, now)
	if err != nil || len(past) == 0 {
		return err
	}
	ids := make([]string, 0, len(past))
	for id := range past {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var errs []error
	for _, id := range ids {
		task, err := deps.Store.GetTask(ctx, id)
		if err != nil || task == nil {
			errs = append(errs, err)
			continue
		}
		state, err := deps.Store.State(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if model.RolloverRefusal(*task, state) != "" {
			continue
		}
		why := "grain rolled it over by itself because it has " + past[id] + "."
		result, err := deps.Store.Rollover(ctx, id, model.Attribution{Actor: model.GrainPrincipal}, why, now)
		if errors.Is(err, model.ErrRolloverRefused) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: rolling over task %s: %w", id, err))
			continue
		}
		log.Printf("orchestrator: rolled durable task %s over to task %s: it has %s", id, result.Successor, past[id])
	}
	return errors.Join(errs...)
}
