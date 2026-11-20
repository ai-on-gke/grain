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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
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
	// Apply resource scopes independently of task visibility, including for
	// cached/history evidence collected by a more privileged reader.
	allows, authorizationRevision := gateObservationScope(ctx)

	resource := func(repo string) bool { return allows("ci:read", repo) && allows("repos:read", repo) }
	access := model.GateHealthAccess{AuthorizationRevision: authorizationRevision, Repository: resource,
		PublicationRepository: func(repo string) bool { return resource(repo) && allows("releases:read", repo) },
		PipelineRepository:    func(repo string) bool { return resource(repo) && allows("tasks:read", repo) },
		DeploymentWide:        allows("deployment:read", "") && allows("tasks:read", "") && allows("ci:read", "") && allows("repos:read", "")}
	access.ScopeLimited = !(access.DeploymentWide && access.PublicationRepository(""))
	// A failed diagnostic never becomes a failed ordinary task read.
	collectCtx, cancel := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 3*time.Second)
	defer cancel()
	taskScopes := map[string]bool{id: true}
	canReadTask := func(relatedID string) bool {
		if permitted, ok := taskScopes[relatedID]; ok {
			return permitted
		}
		related, err := c.Store.GetTask(collectCtx, relatedID)
		permitted := err == nil && related != nil && gates.Digest(related.Target) == gates.Digest(task.Target) && related.ChangeTarget.Equal(task.ChangeTarget) && readable(taskRepo(related))
		taskScopes[relatedID] = permitted
		return permitted
	}
	now := c.now()
	needQual := task.Target != nil
	needChecks := len(model.CheckSubjects(task)) > 0
	var (
		healthProjection bool
		qualifications   gates.QualificationCollection
		fence            string
		checks           []gates.ChecksFacts
		checksDenied     bool
		err              error
	)
	if needQual || needChecks {
		goalCtx, stopGoal := context.WithTimeout(collectCtx, 250*time.Millisecond)
		var goal *model.TaskGoal
		goal, err = c.Store.TaskGoal(goalCtx, id)
		stopGoal()
		healthProjection = goal != nil && slices.Contains([]model.GoalKind{model.GoalBaseGreen, model.GoalNightlyGreen, model.GoalPipelineHealthy}, goal.Kind)
	}
	if err == nil && !healthProjection && (needQual || needChecks) {
		backoffCtx, stopBackoff := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 250*time.Millisecond)
		cached, history, cacheErr := c.Store.GateObservationRetry(backoffCtx, id, now, !taskReadScopedForContext(ctx), access)
		stopBackoff()
		if cacheErr == nil && cached != nil {
			c.redactGateHistoryScopes(collectCtx, id, cached, history)
			if !observationVisible(ctx, cached, canReadTask) {
				return nil, nil
			}
			if history != nil {
				for _, sample := range history.History {
					if !observationVisible(ctx, &sample.View, canReadTask) {
						return nil, nil
					}
				}
			}
			return cached, history
		}
		// Domain reads happen before acquiring the diagnostic writer lock.
		if needQual {
			qualifications, err = gateobserve.CollectQualificationObservationsForTask(collectCtx, c.Store, c.Config.GitHubPullRequests, task, now, func(repo string) bool {
				return allows("ci:read", repo) && requirePullRequestScopes(ctx, repo, APIScope("ci:read")) == nil
			})
		}
		if err == nil && needChecks {
			fence, checks, err = c.collectGateChecks(collectCtx, id, now)
		}
		if err == nil {
			for _, f := range checks {
				for _, source := range f.Sources {
					if source.Error {
						err = fmt.Errorf("CI observation source unavailable")
					}
				}
			}
		}
		checksDenied = errors.Is(err, errGateCheckDenied)
		if checksDenied {
			// CI read authority is independent of the already authorized local
			// report/review projection. Do not read or display any CI source.
			fence, checks, err = "", nil, nil
		}
	}
	readCtx, stopRead := context.WithTimeout(model.WithGateHealthAccess(model.WithExternalRefReadable(ctx, readable), access), 250*time.Millisecond)
	defer stopRead()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	if err == nil {
		if taskReadScopedForContext(ctx) {
			v, err = c.Store.ObserveTaskGatePredictionsWithEvidence(readCtx, id, now, fence, checks, qualifications, c.Config.GateObservationChildren[id]...)
		} else {
			v, d, err = c.Store.ObserveTaskGatesWithEvidence(readCtx, id, now, fence, checks, qualifications, c.Config.GateObservationChildren[id]...)
		}
	}
	if err == nil {
		c.redactGateHistoryScopes(readCtx, id, v, d)
		if !observationVisible(ctx, v, canReadTask) {
			return nil, nil
		}
		if d != nil {
			for _, sample := range d.History {
				if !observationVisible(ctx, &sample.View, canReadTask) {
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
	if !checksDenied && !healthProjection {
		for _, subject := range model.CheckSubjects(task) {
			if len(unavailableChecks) < 8 {
				unavailableChecks = append(unavailableChecks, gates.ChecksFacts{TaskID: id, Subject: subject})
			}
		}
	}
	unavailable := gates.UnavailableObservationForBindings(id, now, append(gates.BindingsFor(id, unavailableChecks), qualifications.Bindings(id)[2:]...))
	v = &unavailable
	// A fresh short budget can retain a failure even when the collection's
	// own 250ms budget expired. Never delay an already canceled request.
	if ctx.Err() == nil {
		failureCtx, stop := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 50*time.Millisecond)
		_ = c.Store.RecordGateQualificationFailure(failureCtx, id, now, v, qualifications, access)
		stop()
	}
	if !observationVisible(ctx, v, canReadTask) {
		return nil, nil
	}
	return v, nil
}

// gateObservationScope intersects every authorization restriction on ctx
// (acting-task or external-agent call grants, direct external-agent token,
// limited verified-identity RBAC role, CI API call, and fail-closed unscoped
// PrincipalAgent) and hashes the combined intersection into a canonical
// JSON-safe opaque authorizationRevision.
func gateObservationScope(ctx context.Context) (func(APIScope, string) bool, uint64) {
	var effective APIGrantSet
	restricted := false
	intersect := func(next APIGrantSet) {
		if !restricted {
			effective = next
			restricted = true
			return
		}
		effective = effective.Intersect(next)
	}
	if call, ok := agentCallFromContext(ctx); ok {
		intersect(call.grants)
	}
	if tok := externalTokenFromContext(ctx); tok != nil {
		_, tokSet, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
		if err != nil {
			intersect(APIGrantSet{})
		} else {
			intersect(tokSet)
		}
	}
	if role := accessRoleFromContext(ctx); role != nil && !role.Full {
		intersect(role.set.Without(role.deny))
	}
	if ciCall, ok := model.CIAPICallFromContext(ctx); ok {
		intersect(ciAPICallGrantSet(ciCall))
	}
	if !restricted {
		if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
			intersect(APIGrantSet{})
		}
	}

	authorizationFence := gates.Digest("trusted internal health read ceiling")
	allows := func(APIScope, string) bool { return true }
	if restricted {
		allows = effective.Allows
		authorizationFence = gates.Digest(struct {
			Restricted bool
			Grants     []string
		}{true, grantListFromSet(effective)})
	}
	authorizationRevision, _ := strconv.ParseUint(authorizationFence[7:23], 16, 64)
	authorizationRevision &= (1 << 53) - 1 // canonical JSON-safe opaque scope fence
	return allows, authorizationRevision
}

func ciAPICallGrantSet(ciCall model.CIAPICall) APIGrantSet {
	if ciCall.Repo.Owner == "" || ciCall.Repo.Name == "" || len(ciCall.Scopes) == 0 {
		return APIGrantSet{}
	}
	repo := ciCall.Repo.String()
	grants := make([]string, 0, len(ciCall.Scopes))
	for _, sc := range ciCall.Scopes {
		if model.CIAPIScopeRefusal(sc) != "" {
			continue
		}
		grants = append(grants, APIGrantPrefix+string(sc)+"@"+repo)
	}
	return APIGrantsOf(grants)
}

// Recheck every persisted resource's display scope, including retained history.
// A task's links and a viewer's grants may have changed since collection.
func observationVisible(ctx context.Context, v *gates.ObservationView, relatedReadable ...func(string) bool) bool {
	if v == nil {
		return true
	}
	readable := taskReadableForContext(ctx)
	allows, _ := gateObservationScope(ctx)
	ciReadable := func(repo string) bool {
		return allows("ci:read", repo) && requirePullRequestScopes(ctx, repo, APIScope("ci:read")) == nil
	}
	subject := func(s gates.Subject) bool {
		if s.Kind == "task" {
			if len(v.Bindings) > 0 && s.TaskID == v.Bindings[0].Subject.TaskID {
				return true
			}
			return len(relatedReadable) > 0 && relatedReadable[0](s.TaskID)
		}
		return readable(s.Repository)
	}
	for _, b := range v.Bindings {
		if isHealthObservationType(b.Type) {
			continue
		}
		if !subject(b.Subject) {
			return false
		}
		if b.Type == "release_qualification" {
			var config gates.ReleaseQualificationConfig
			if json.Unmarshal(b.Config, &config) != nil {
				return false
			}
			if config.Predicate == "suite" && !ciReadable(b.Subject.Repository) {
				return false
			}
		}
		if b.Type == "checks" && !ciReadable(b.Subject.Repository) {
			return false
		}
	}
	for _, r := range v.Results {
		if isHealthObservationType(r.Result.Type) {
			continue
		}
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

// ObservationChildSelection configures comparisons, never requirements or waits.
// The task allowlist independently controls whether anything is collected.
func ObservationChildSelection(raw string) (map[string][]gates.ChildrenConfig, error) {
	out := map[string][]gates.ChildrenConfig{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if len(raw) > 64*1024 {
		return nil, fmt.Errorf("shadow child selection exceeds 64 KiB")
	}
	entries := map[string][]json.RawMessage{}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("child selections must be an object")
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		id, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("invalid parent ID")
		}
		if _, exists := entries[id]; exists {
			return nil, fmt.Errorf("duplicate parent selection")
		}
		var configs []json.RawMessage
		if err = decoder.Decode(&configs); err != nil {
			return nil, err
		}
		entries[id] = configs
		if len(entries) > 32 {
			return nil, fmt.Errorf("too many parents")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing child selection data")
	}
	if len(entries) > 32 {
		return nil, fmt.Errorf("at most 32 shadow parent selections")
	}
	for id, configs := range entries {
		if !isValidTaskSummaryID(id) || len(configs) > 3 {
			return nil, fmt.Errorf("invalid parent or too many child predicates")
		}
		children := map[string]bool{}
		for _, raw := range configs {
			canonical, err := gates.Validate("children", 1, raw)
			if err != nil {
				return nil, err
			}
			var config gates.ChildrenConfig
			_ = json.Unmarshal(canonical, &config)
			for _, child := range config.Tasks {
				children[child] = true
			}
			if len(children) > 64 {
				return nil, fmt.Errorf("at most 64 distinct selected children")
			}
			out[id] = append(out[id], config)
		}
	}
	return out, nil
}
func observationChildrenEnvironment() map[string][]gates.ChildrenConfig {
	selections, err := ObservationChildSelection(os.Getenv("GRAIN_GATE_OBSERVATION_CHILDREN"))
	if err != nil {
		log.Printf("gate child comparisons disabled: %v", err)
		return nil
	}
	return selections
}

// A child's permissions can change after a history sample was retained. Apply
// current task-read scope to every historical sidecar, not just today's result.
func (c *Client) redactGateHistoryScopes(ctx context.Context, parentID string, v *gates.ObservationView, d *model.GateObservationDiagnostics) {
	var (
		parentLoaded bool
		parent       *model.Task
	)
	readable := taskReadableForContext(ctx)
	allowed := map[string]bool{parentID: true}
	canRead := func(id string) bool {
		if permitted, ok := allowed[id]; ok {
			return permitted
		}
		if !parentLoaded {
			parentLoaded = true
			p, err := c.Store.GetTask(ctx, parentID)
			if err == nil {
				parent = p
			}
		}
		if parent == nil {
			return true
		}
		task, err := c.Store.GetTask(ctx, id)
		permitted := err == nil && task != nil && gates.Digest(task.Target) == gates.Digest(parent.Target) && task.ChangeTarget.Equal(parent.ChangeTarget) && readable(taskRepo(task))
		allowed[id] = permitted
		return permitted
	}
	redact := func(view *gates.ObservationView) {
		if view == nil {
			return
		}
		for i := range view.Results {
			result := &view.Results[i].Result
			hidden := false
			for _, e := range result.Evidence {
				if e.Subject.Kind == "task" && !canRead(e.Subject.TaskID) {
					hidden = true
				}
			}
			if hidden {
				result.Status = "unknown"
				result.Summary = "Related evidence is unavailable in the current read scope."
				result.Evidence = []gates.Evidence{}
				result.Dependencies = []gates.Dependency{}
				result.Waits = []gates.Wait{}
				result.HumanText = nil
				result.AgentText = nil
			}
		}
	}
	redact(v)
	if d != nil {
		for i := range d.History {
			redact(&d.History[i].View)
		}
	}
}

func isHealthObservationType(typ string) bool {
	return typ == "base_health" || typ == "nightly_health" || typ == "publishing_health" || typ == "pipeline_health"
}
