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

	"github.com/ai-on-gke/grain/pkg/gateobserve"
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
	authorizedCtx := model.WithExternalRefReadable(ctx, readable)
	readCtx, cancel := context.WithTimeout(authorizedCtx, 250*time.Millisecond)
	defer cancel()
	allowed := func(view *gates.ObservationView) bool {
		subject := func(s gates.Subject) bool {
			if s.Kind == "task" {
				return s.TaskID == id
			}
			return readable(s.Repository)
		}
		for _, b := range view.Bindings {
			if !subject(b.Subject) {
				return false
			}
		}
		for _, r := range view.Results {
			for _, e := range r.Result.Evidence {
				if !subject(e.Subject) {
					return false
				}
			}
			for _, dep := range r.Result.Dependencies {
				if !subject(dep.Subject) {
					return false
				}
			}
		}
		return true
	}
	now := c.now()
	history := !taskReadScopedForContext(ctx)
	if cached, diagnostics, cacheErr := c.Store.GateObservationBackoff(readCtx, id, now, history); cacheErr == nil && cached != nil {
		if allowed(cached) {
			return cached, diagnostics
		}
		return nil, nil
	}
	cancel()
	sourceCtx, sourceStop := context.WithTimeout(authorizedCtx, 3*time.Second)
	qualifications, err := gateobserve.CollectQualificationObservations(sourceCtx, c.Store, c.Config.GitHubPullRequests, id, now)
	sourceStop()
	persistCtx, persistStop := context.WithTimeout(authorizedCtx, 250*time.Millisecond)
	defer persistStop()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	if err == nil {
		if history {
			v, d, err = c.Store.ObserveTaskGatesWithQualifications(persistCtx, id, now, qualifications)
		} else {
			v, err = c.Store.ObserveTaskGatePredictionsWithQualifications(persistCtx, id, now, qualifications)
		}
	}
	if err == nil {
		if !allowed(v) {
			return nil, nil
		}
		return v, d
	}
	if errors.Is(err, model.ErrGateObservationScopeDenied) {
		return nil, nil
	}
	// No raw database errors, comment bodies, principals or credentials escape.
	unavailable := gates.UnavailableQualifications(id, now, qualifications)
	v = &unavailable
	// A fresh short budget can retain a failure even when the collection's
	// own 250ms budget expired. Never delay an already canceled request.
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
		_ = c.Store.RecordGateQualificationFailure(failureCtx, id, now, v, qualifications)
		stop()
	}
	if !allowed(v) {
		return nil, nil
	}
	return v, nil
}
