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
	"time"
)

// reconcileWake picks back up every task whose wake time has arrived --
// a deferral that named a date (model.Task.DeferredUntil) rather than
// the indefinite kind, which nothing here touches.
//
// This is the other half of "let a task say when it may start". A date
// in a task's own prose is a note to a human and holds nothing: the
// reading of the nightly suite's first week was filed asking not to
// start before a date and was dispatched a week early twice
// (docs/test-runs/2026-09-08-nightly-week-one-still-early.md), because
// there was nothing to set. Deferring with a wake time is the thing to
// set, and this is what makes it come back without anybody remembering.
//
// Undeferring is the whole of it: the task returns to whichever of
// 'proposed' and 'queued' its own approval implies, exactly as Undefer's
// button does, and it is the dispatcher that decides whether a queued
// one runs. So a wake time is a floor -- "not before" -- and never a
// promise that anything starts at that instant.
//
// It runs immediately after "schedule" and before "dispatch", which is
// the same latency preference the reconcilers around it are ordered by
// (Reconcilers' own doc comment): a task woken this cycle is dispatchable
// on this same tick rather than on the next one. Nothing depends on that
// ordering -- a wake this pass misses is a wake the next pass makes,
// since the condition it reads is a timestamp in the past and stays one.
//
// One task failing to wake does not stop the others, for the reason
// reconcileSchedule keeps firing schedules past one that failed.
func reconcileWake(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil {
		return nil
	}
	due, err := deps.Store.DueDeferrals(ctx, now)
	if err != nil {
		return fmt.Errorf("orchestrator: listing due deferrals: %w", err)
	}
	var errs []error
	for _, id := range due {
		if err := deps.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: waking task %s: %w", id, err))
			continue
		}
		log.Printf("orchestrator: task %s has reached its wake time and is picked back up", id)
	}
	// A resume_later that waits on grain CI is a deferral too, with its
	// verdict for a wake time (model.Store.DueResumeOnCI).
	onCI, err := deps.Store.DueResumeOnCI(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("orchestrator: listing tasks waiting on CI: %w", err))
	}
	for _, id := range onCI {
		if err := deps.Store.SetDeferred(ctx, id, nil, nil, now); err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: waking task %s on its CI verdict: %w", id, err))
			continue
		}
		log.Printf("orchestrator: task %s has the CI verdict it waited for and is picked back up", id)
	}
	return errors.Join(errs...)
}
