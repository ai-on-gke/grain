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

package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ObservationSelection is a deployment-local allowlist, not a task contract or
// policy. Empty disables collection and display. No elapsed timer changes it.
func ObservationSelection(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	ids := strings.Split(raw, ",")
	if len(ids) > 32 {
		return nil, fmt.Errorf("at most 32 observation task IDs")
	}
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if !isValidTaskSummaryID(id) || slices.Contains(ids[:i], id) {
			return nil, fmt.Errorf("invalid or duplicate observation task ID")
		}
		ids[i] = id
	}
	return ids, nil
}
func observationEnvironment() []string {
	ids, err := ObservationSelection(os.Getenv("GRAIN_GATE_OBSERVATION_TASKS"))
	if err != nil {
		log.Printf("gate observations disabled: %v", err)
		return nil
	}
	return ids
}
func (c *Client) observesGates(id string) bool {
	return slices.Contains(c.Config.GateObservationTaskIDs, id) && len(c.Config.GateObservationTaskIDs) <= 32
}
func (c *Client) gateObservations(ctx context.Context, task model.Task) (*gates.ObservationView, *model.GateObservationDiagnostics) {
	id := task.ID
	// History can retain linked evidence after the current change link is
	// removed. Authorize the whole optional sidecar, not just today's bindings,
	// before reading or replaying any shared diagnostic cache.
	readable := taskReadableForContext(ctx)
	if !readable(taskRepo(&task)) {
		return nil, nil
	}
	if !c.observesGates(id) {
		return nil, nil
	}
	// A failed diagnostic never becomes a failed ordinary task read.
	collectCtx, cancel := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 3*time.Second)
	defer cancel()
	now := c.now()
	backoffCtx, stopBackoff := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 50*time.Millisecond)
	cached, history, cacheErr := c.Store.GateObservationRetry(backoffCtx, id, now, !taskReadScopedForContext(ctx))
	stopBackoff()
	if cacheErr == nil && cached != nil {
		if !observationVisible(ctx, cached) {
			return nil, nil
		}
		if history != nil {
			for _, sample := range history.History {
				if !observationVisible(ctx, &sample.View) {
					return nil, nil
				}
			}
		}
		return cached, history
	}
	// Domain reads happen before acquiring the diagnostic writer lock.
	fence, checks, err := c.collectGateChecks(collectCtx, id, now)
	if err == nil {
		for _, f := range checks {
			for _, source := range f.Sources {
				if source.Error {
					err = fmt.Errorf("CI observation source unavailable")
				}
			}
		}
	}
	checksDenied := errors.Is(err, errGateCheckDenied)
	if checksDenied {
		// CI read authority is independent of the already authorized local
		// report/review projection. Do not read or display any CI source.
		fence, checks, err = "", nil, nil
	}
	readCtx, stopRead := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 250*time.Millisecond)
	defer stopRead()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	if err == nil {
		if taskReadScopedForContext(ctx) {
			v, err = c.Store.ObserveTaskGatePredictionsWithChecks(readCtx, id, now, fence, checks)
		} else {
			v, d, err = c.Store.ObserveTaskGatesWithChecks(readCtx, id, now, fence, checks)
		}
	}
	if err == nil {
		if !observationVisible(ctx, v) {
			return nil, nil
		}
		if d != nil {
			for _, sample := range d.History {
				if !observationVisible(ctx, &sample.View) {
					return nil, nil
				}
			}
		}
		return v, d
	}
	if errors.Is(err, model.ErrGateObservationScopeDenied) {
		return nil, nil
	}
	// No raw database errors, comment bodies, principals or credentials escape.
	unavailableChecks := []gates.ChecksFacts{}
	if !checksDenied {
		for _, subject := range model.CheckSubjects(task) {
			if len(unavailableChecks) < 8 {
				unavailableChecks = append(unavailableChecks, gates.ChecksFacts{TaskID: id, Subject: subject})
			}
		}
	}
	unavailable := gates.UnavailableObservation(id, now, unavailableChecks...)
	v = &unavailable
	// A fresh short budget can retain a failure even when the collection's
	// own 250ms budget expired. Never delay an already canceled request.
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 50*time.Millisecond)
		_ = c.Store.RecordGateObservationFailure(failureCtx, id, now, v)
		stop()
	}
	if !observationVisible(ctx, v) {
		return nil, nil
	}
	return v, nil
}

// Recheck every persisted resource's display scope, including retained history.
// A task's links and a viewer's grants may have changed since collection.
func observationVisible(ctx context.Context, v *gates.ObservationView) bool {
	if v == nil {
		return true
	}
	readable := taskReadableForContext(ctx)
	subject := func(s gates.Subject) bool {
		if s.Kind == "task" {
			return true
		}
		return readable(s.Repository)
	}
	for _, b := range v.Bindings {
		if !subject(b.Subject) {
			return false
		}
		if b.Type == "checks" && requirePullRequestScopes(ctx, b.Subject.Repository, APIScope("ci:read")) != nil {
			return false
		}
	}
	for _, r := range v.Results {
		for _, e := range r.Result.Evidence {
			if !subject(e.Subject) {
				return false
			}
		}
		for _, d := range r.Result.Dependencies {
			if !subject(d.Subject) {
				return false
			}
		}
	}
	return true
}
