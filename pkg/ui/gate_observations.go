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
	"encoding/json"
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
	readable := taskReadableForContext(ctx)
	if !readable(taskRepo(&task)) || !c.observesGates(id) {
		return nil, nil
	}
	authorizedCtx := model.WithExternalRefReadable(ctx, readable)
	now := c.now()
	history := !taskReadScopedForContext(ctx)
	visible := func(v *gates.ObservationView, d *model.GateObservationDiagnostics) bool {
		if !observationVisible(ctx, v) {
			return false
		}
		if d != nil {
			for _, sample := range d.History {
				if !observationVisible(ctx, &sample.View) {
					return false
				}
			}
		}
		return true
	}
	backoffCtx, stopBackoff := context.WithTimeout(authorizedCtx, 50*time.Millisecond)
	cached, diagnostics, cacheErr := c.Store.GateObservationBackoff(backoffCtx, id, now, history)
	stopBackoff()
	if cacheErr == nil && cached != nil {
		if visible(cached, diagnostics) {
			return cached, diagnostics
		}
		return nil, nil
	}
	// Ordinary domain reads run outside the diagnostic writer transaction.
	sourceCtx, sourceStop := context.WithTimeout(authorizedCtx, 3*time.Second)
	qualifications, qualErr := gateobserve.CollectQualificationObservationsWithCIRead(sourceCtx, c.Store, c.Config.GitHubPullRequests, id, now, func(repo string) bool { return requirePullRequestScopes(ctx, repo, APIScope("ci:read")) == nil })
	fence, checks, err := c.collectGateChecks(sourceCtx, id, now)
	sourceStop()
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
		fence, checks, err = "", nil, nil
	}
	if err == nil {
		err = qualErr
	}
	persistCtx, persistStop := context.WithTimeout(authorizedCtx, 250*time.Millisecond)
	defer persistStop()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	if err == nil {
		if history {
			v, d, err = c.Store.ObserveTaskGatesWithEvidence(persistCtx, id, now, fence, checks, qualifications)
		} else {
			v, err = c.Store.ObserveTaskGatePredictionsWithEvidence(persistCtx, id, now, fence, checks, qualifications)
		}
	}
	if err == nil {
		if visible(v, d) {
			return v, d
		}
		return nil, nil
	}
	if errors.Is(err, model.ErrGateObservationScopeDenied) {
		return nil, nil
	}
	// Rejected projections contain no accepted source evidence or raw errors.
	unavailableChecks := []gates.ChecksFacts{}
	if !checksDenied {
		for _, subject := range model.CheckSubjects(task) {
			if len(unavailableChecks) < 8 {
				unavailableChecks = append(unavailableChecks, gates.ChecksFacts{TaskID: id, Subject: subject})
			}
		}
	}
	unavailable := gates.UnavailableObservationForBindings(id, now, append(gates.BindingsFor(id, unavailableChecks), qualifications.Bindings(id)[2:]...))
	v = &unavailable
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(authorizedCtx, 50*time.Millisecond)
		_ = c.Store.RecordGateQualificationFailure(failureCtx, id, now, v, qualifications)
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
			return len(v.Bindings) > 0 && s.TaskID == v.Bindings[0].Subject.TaskID
		}
		return readable(s.Repository)
	}
	for _, b := range v.Bindings {
		if !subject(b.Subject) {
			return false
		}
		if b.Type == "release_qualification" {
			var config gates.ReleaseQualificationConfig
			if json.Unmarshal(b.Config, &config) != nil {
				return false
			}
			if config.Predicate == "suite" && requirePullRequestScopes(ctx, b.Subject.Repository, APIScope("ci:read")) != nil {
				return false
			}
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
