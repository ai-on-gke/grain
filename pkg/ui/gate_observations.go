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
	"strconv"
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
	// Apply resource scopes independently of task visibility, including for
	// cached/history evidence collected by a more privileged reader.
	allows := func(scope APIScope, repo string) bool {
		if call, ok := agentCallFromContext(ctx); ok {
			return call.grants.Allows(scope, repo)
		}
		if role := accessRoleFromContext(ctx); role != nil {
			return role.allows(scope, repo)
		}
		return true // existing trusted in-process/loopback/access-code ceiling
	}
	authorizationFence := gates.Digest("trusted internal health read ceiling")
	if call, ok := agentCallFromContext(ctx); ok {
		authorizationFence = gates.Digest(grantListFromSet(call.grants))
	}
	if role := accessRoleFromContext(ctx); role != nil {
		authorizationFence = gates.Digest(struct {
			Grants []string
			Deny   map[APIScope]bool
			Full   bool
		}{role.grants, role.deny, role.Full})
	}
	authorizationRevision, _ := strconv.ParseUint(authorizationFence[7:23], 16, 64)
	authorizationRevision &= (1 << 53) - 1 // canonical JSON-safe opaque scope fence

	resource := func(repo string) bool { return allows("ci:read", repo) && allows("repos:read", repo) }
	access := model.GateHealthAccess{AuthorizationRevision: authorizationRevision, Repository: resource,
		PublicationRepository: func(repo string) bool { return resource(repo) && allows("releases:read", repo) },
		PipelineRepository:    func(repo string) bool { return resource(repo) && allows("tasks:read", repo) },
		DeploymentWide:        allows("deployment:read", "") && allows("tasks:read", "") && allows("ci:read", "") && allows("repos:read", "")}
	access.ScopeLimited = !(access.DeploymentWide && access.PublicationRepository(""))
	// A failed diagnostic never becomes a failed ordinary task read.
	readCtx, cancel := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 250*time.Millisecond)
	defer cancel()
	now := c.now()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	var err error
	if taskReadScopedForContext(ctx) {
		v, err = c.Store.ObserveTaskGatePredictions(readCtx, id, now, access)
	} else {
		v, d, err = c.Store.ObserveTaskGates(readCtx, id, now, access)
	}
	allowed := func(view *gates.ObservationView) bool {
		subject := func(s gates.Subject) bool {
			if s.Kind == "task" {
				return s.TaskID == id
			}
			return readable(s.Repository)
		}
		health := func(typ string) bool {
			return typ == "base_health" || typ == "nightly_health" || typ == "publishing_health" || typ == "pipeline_health"
		}
		for _, b := range view.Bindings {
			if health(b.Type) {
				continue
			} // health resource ceilings are checked by the scoped model reader
			if !subject(b.Subject) {
				return false
			}
		}
		for _, r := range view.Results {
			if health(r.Result.Type) {
				continue
			}
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
	unavailable := gates.UnavailableObservation(id, now)
	v = &unavailable
	// A fresh short budget can retain a failure even when the collection's
	// own 250ms budget expired. Never delay an already canceled request.
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 50*time.Millisecond)
		_ = c.Store.RecordGateObservationFailure(failureCtx, id, now, v, access)
		stop()
	}
	if !allowed(v) {
		return nil, nil
	}
	return v, nil
}
