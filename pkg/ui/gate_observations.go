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
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/forge"
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

var trustedInternalHealthFence = sync.OnceValue(func() string {
	return gates.Digest("trusted internal health read ceiling")
})

const maxGateObservationCacheEntries = 64

type gateObservationCacheKey struct {
	taskID      string
	callerKey   string
	scoped      bool
	childrenKey string
}

type gateSplicedSlot struct {
	legacyETag  string
	splicedBody []byte
	splicedETag string
}

type gateObservationCacheEntry struct {
	changeToken string
	evaluatedAt time.Time
	validUntil  time.Time
	localFence  *model.GateObservationLocalFence
	revalMeta   gateCheckRevalMeta
	obs         *gates.ObservationView
	diag        *model.GateObservationDiagnostics
	fieldsJSON  []byte
	spliced     [2]gateSplicedSlot
	nextSlot    int
	used        uint64
}

// gateObservationCache caches already-authorized, already-redacted gate
// observation projections keyed by (task, caller authorization, scope, child
// selection) and guarded by Store.ChangeToken plus the observation's exact
// freshness lease (or persisted retry deadline). Any database commit -- task
// edit, Submit, Accept findings, webhook/CI update, repository move, or direct
// SQL mutation -- moves Store.ChangeToken and invalidates the O(1) fast path;
// unrelated commits can still revalidate a task's transactionally captured
// local fence in one read-only transaction before spending remote forge calls.
type gateObservationCache struct {
	mu      sync.Mutex
	entries map[gateObservationCacheKey]*gateObservationCacheEntry
	clock   uint64
}

func (c *gateObservationCache) hit(key gateObservationCacheKey, token string, now time.Time) (*gates.ObservationView, *model.GateObservationDiagnostics, bool) {
	if token == "" {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.changeToken != token || now.Before(e.evaluatedAt) || !now.Before(e.validUntil) {
		return nil, nil, false
	}
	c.clock++
	e.used = c.clock
	obs, diag := cloneCachedGateEntry(e)
	return obs, diag, true
}

func cloneCachedGateEntry(e *gateObservationCacheEntry) (*gates.ObservationView, *model.GateObservationDiagnostics) {
	var obs *gates.ObservationView
	if e.obs != nil {
		cp := e.obs.Clone()
		obs = &cp
	}
	var diag *model.GateObservationDiagnostics
	if e.diag != nil {
		cp := e.diag.Clone()
		diag = &cp
	}
	return obs, diag
}

func (c *gateObservationCache) candidate(key gateObservationCacheKey, now time.Time) (*gateObservationCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.localFence == nil || now.Before(e.evaluatedAt) || !now.Before(e.validUntil) {
		return nil, false
	}
	return e, true
}

func (c *gateObservationCache) promoteToken(key gateObservationCacheKey, entry *gateObservationCacheEntry, token string) {
	if token == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.entries[key]; ok && cur == entry {
		cur.changeToken = token
		c.clock++
		cur.used = c.clock
	}
}

func (c *gateObservationCache) spliceEntry(key gateObservationCacheKey, e *gateObservationCacheEntry, legacyBody []byte, legacyETag string) ([]byte, string, bool) {
	c.mu.Lock()
	c.clock++
	e.used = c.clock
	for _, slot := range e.spliced {
		if slot.legacyETag == legacyETag && len(slot.splicedBody) > 0 {
			body, etag := slot.splicedBody, slot.splicedETag
			c.mu.Unlock()
			return body, etag, true
		}
	}
	prefix := e.fieldsJSON
	c.mu.Unlock()

	spliced, err := spliceGatePrefixJSON(legacyBody, prefix)
	if err != nil {
		return nil, "", false
	}
	etag := etagOf(spliced)

	c.mu.Lock()
	if cur, ok := c.entries[key]; ok && cur == e {
		cur.spliced[cur.nextSlot] = gateSplicedSlot{
			legacyETag:  legacyETag,
			splicedBody: spliced,
			splicedETag: etag,
		}
		cur.nextSlot = (cur.nextSlot + 1) % len(cur.spliced)
	}
	c.mu.Unlock()
	return spliced, etag, true
}

func (c *gateObservationCache) hitSpliced(key gateObservationCacheKey, token string, now time.Time, legacyBody []byte, legacyETag string) ([]byte, string, bool) {
	if token == "" {
		return nil, "", false
	}
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok || e.changeToken != token || now.Before(e.evaluatedAt) || !now.Before(e.validUntil) {
		c.mu.Unlock()
		return nil, "", false
	}
	c.mu.Unlock()
	return c.spliceEntry(key, e, legacyBody, legacyETag)
}

func (c *gateObservationCache) store(key gateObservationCacheKey, entry *gateObservationCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[gateObservationCacheKey]*gateObservationCacheEntry)
	}
	c.clock++
	entry.used = c.clock
	c.entries[key] = entry
	if len(c.entries) <= maxGateObservationCacheEntries {
		return
	}
	var oldestKey gateObservationCacheKey
	oldest := ^uint64(0)
	for k, v := range c.entries {
		if v.used < oldest {
			oldestKey, oldest = k, v.used
		}
	}
	delete(c.entries, oldestKey)
}

func gateObservationCallerKey(ctx context.Context) (callerKey string, scoped bool) {
	call, isAgent := agentCallFromContext(ctx)
	tok := externalTokenFromContext(ctx)
	role := accessRoleFromContext(ctx)
	ciCall, isCIAPI := model.CIAPICallFromContext(ctx)
	p, hasP := PrincipalFromContext(ctx)
	agentPrincipal := hasP && p.Kind == model.PrincipalAgent
	limitedRole := role != nil && !role.Full
	scoped = isAgent || tok != nil || limitedRole || isCIAPI || agentPrincipal
	if !isAgent && tok == nil && role == nil && !isCIAPI && !agentPrincipal {
		return "trusted", false
	}
	var agentGrants []string
	if isAgent {
		agentGrants = grantListFromSet(call.grants)
	}
	type tokKey struct {
		ScopeSet string
		Scopes   []string
		Repos    []string
	}
	var tk *tokKey
	if tok != nil {
		tk = &tokKey{ScopeSet: tok.ScopeSet, Scopes: tok.Scopes, Repos: tok.Repos}
	}
	type roleKey struct {
		Grants []string
		Deny   map[APIScope]bool
		Full   bool
	}
	var rk *roleKey
	if role != nil {
		rk = &roleKey{Grants: role.grants, Deny: role.deny, Full: role.Full}
	}
	ciRepo := ""
	var ciScopes []model.APIScope
	if isCIAPI {
		ciRepo = ciCall.Repo.String()
		ciScopes = ciCall.Scopes
	}
	return gates.Digest(struct {
		Agent          bool
		AgentGrants    []string
		Token          *tokKey
		Role           *roleKey
		CIAPI          bool
		CIRepo         string
		CIScopes       []model.APIScope
		AgentPrincipal bool
		Scoped         bool
	}{isAgent, agentGrants, tk, rk, isCIAPI, ciRepo, ciScopes, agentPrincipal, scoped}), scoped
}

func (c *Client) gateObservationKey(ctx context.Context, id string) gateObservationCacheKey {
	callerKey, scoped := gateObservationCallerKey(ctx)
	childrenKey := ""
	if children := c.Config.GateObservationChildren[id]; len(children) > 0 {
		childrenKey = gates.Digest(children)
	}
	return gateObservationCacheKey{
		taskID:      id,
		callerKey:   callerKey,
		scoped:      scoped,
		childrenKey: childrenKey,
	}
}

func observationCacheWindow(v *gates.ObservationView, now time.Time) (evaluatedAt, validUntil time.Time, ok bool) {
	if v == nil || len(v.Results) == 0 {
		return time.Time{}, time.Time{}, false
	}
	if v.Dirty {
		// Never cache an unpersisted storage outage (RetryAt == nil);
		// persisted error backoff is reusable until RetryAt.
		retryAt := v.Results[0].RetryAt
		if retryAt == nil || !now.Before(*retryAt) {
			return time.Time{}, time.Time{}, false
		}
		return v.ComparedAt, *retryAt, true
	}
	until := v.ComparedAt.Add(time.Minute)
	for _, entry := range v.Results {
		if entry.Result.ValidUntil.IsZero() {
			return time.Time{}, time.Time{}, false
		}
		if entry.Result.ValidUntil.Before(until) {
			until = entry.Result.ValidUntil
		}
	}
	if !now.Before(until) {
		return time.Time{}, time.Time{}, false
	}
	return v.ComparedAt, until, true
}

func observationWasRaceInvalidated(v *gates.ObservationView, checks []gates.ChecksFacts) bool {
	for _, f := range checks {
		if f.Gap == "Head or base changed during CI collection." {
			return true
		}
	}
	if v == nil {
		return false
	}
	for _, entry := range v.Results {
		s := entry.Result.Summary
		if strings.Contains(s, "changed during collection") ||
			strings.Contains(s, "changed before diagnostic persistence") ||
			strings.Contains(s, "changed after this older collection started") {
			return true
		}
	}
	return false
}

func (c *Client) gateRevalMetaMatches(meta gateCheckRevalMeta, now time.Time) bool {
	for _, p := range meta.pending {
		known, stalled, deadline := forge.InspectPendingChecks(p.ref, p.head, p.stallTimeout, now)
		if known != p.known || stalled != p.stalled || !deadline.Equal(p.deadline) {
			return false
		}
	}
	for _, g := range meta.grainCI {
		if c.Config.CI == nil {
			if g.digest != "" {
				return false
			}
			continue
		}
		run, ok := c.Config.CI.Get(g.runID)
		cur := ""
		if ok {
			cur = gates.Digest(run)
		}
		if cur != g.digest {
			return false
		}
	}
	for _, r := range meta.required {
		cur := ""
		if c.Config.GateRequiredCheck != nil {
			cur = c.Config.GateRequiredCheck(r.repo)
		}
		if cur != r.required {
			return false
		}
	}
	return true
}

func (c *Client) revalidateGateCacheEntry(ctx context.Context, key gateObservationCacheKey, id string, readable func(string) bool, storeToken string, now time.Time) (*gateObservationCacheEntry, bool) {
	cand, ok := c.gateObsCache.candidate(key, now)
	if !ok || c.Store == nil || !c.gateRevalMetaMatches(cand.revalMeta, now) {
		return nil, false
	}
	revalCtx, stopReval := context.WithTimeout(model.WithExternalRefReadable(ctx, readable), 250*time.Millisecond)
	matched, err := c.Store.VerifyGateObservationLocalFence(revalCtx, id, now, cand.localFence)
	stopReval()
	if err != nil || !matched {
		return nil, false
	}
	if storeToken != "" {
		if tokenAfter, err := c.Store.ChangeToken(ctx); err == nil && tokenAfter == storeToken {
			c.gateObsCache.promoteToken(key, cand, storeToken)
		}
	}
	return cand, true
}

func (c *Client) spliceGateObservationsCached(ctx context.Context, task model.Task, legacyBody []byte, legacyETag, storeToken string) ([]byte, string, error) {
	id := task.ID
	readable := taskReadableForContext(ctx)
	if !readable(taskRepo(&task)) || !c.observesGates(id) {
		return legacyBody, legacyETag, nil
	}
	now := c.now()
	key := c.gateObservationKey(ctx, id)
	if body, etag, ok := c.gateObsCache.hitSpliced(key, storeToken, now, legacyBody, legacyETag); ok {
		return body, etag, nil
	}
	if cand, ok := c.revalidateGateCacheEntry(ctx, key, id, readable, storeToken, now); ok {
		if body, etag, ok := c.gateObsCache.spliceEntry(key, cand, legacyBody, legacyETag); ok {
			return body, etag, nil
		}
	}
	obs, diag, localFence, revalMeta, cacheable := c.gateObservationsSlow(ctx, task, now)
	if obs == nil && diag == nil {
		return legacyBody, legacyETag, nil
	}
	prefix, err := json.Marshal(gateObservationFields{
		GateObservations:           obs,
		GateObservationDiagnostics: diag,
	})
	if err != nil {
		return nil, "", err
	}
	spliced, err := spliceGatePrefixJSON(legacyBody, prefix)
	if err != nil {
		return nil, "", err
	}
	splicedETag := etagOf(spliced)
	if cacheable && storeToken != "" && c.Store != nil {
		if evaluatedAt, validUntil, ok := observationCacheWindow(obs, now); ok {
			if tokenAfter, err := c.Store.ChangeToken(ctx); err == nil && tokenAfter == storeToken {
				var cachedObs *gates.ObservationView
				if obs != nil {
					cp := obs.Clone()
					cachedObs = &cp
				}
				var cachedDiag *model.GateObservationDiagnostics
				if diag != nil {
					cp := diag.Clone()
					cachedDiag = &cp
				}
				entry := &gateObservationCacheEntry{
					changeToken: storeToken,
					evaluatedAt: evaluatedAt,
					validUntil:  validUntil,
					localFence:  localFence,
					revalMeta:   revalMeta,
					obs:         cachedObs,
					diag:        cachedDiag,
					fieldsJSON:  prefix,
					nextSlot:    1,
				}
				entry.spliced[0] = gateSplicedSlot{
					legacyETag:  legacyETag,
					splicedBody: spliced,
					splicedETag: splicedETag,
				}
				c.gateObsCache.store(key, entry)
			}
		}
	}
	return spliced, splicedETag, nil
}

func (c *Client) gateObservations(ctx context.Context, task model.Task) (*gates.ObservationView, *model.GateObservationDiagnostics) {
	storeToken := ""
	if c.Store != nil && c.observesGates(task.ID) {
		if t, err := c.Store.ChangeToken(ctx); err == nil {
			storeToken = t
		}
	}
	return c.gateObservationsWithToken(ctx, task, storeToken)
}

func (c *Client) gateObservationsWithToken(ctx context.Context, task model.Task, storeToken string) (*gates.ObservationView, *model.GateObservationDiagnostics) {
	id := task.ID
	readable := taskReadableForContext(ctx)
	if !readable(taskRepo(&task)) || !c.observesGates(id) {
		return nil, nil
	}
	now := c.now()
	key := c.gateObservationKey(ctx, id)
	if obs, diag, ok := c.gateObsCache.hit(key, storeToken, now); ok {
		return obs, diag
	}
	if cand, ok := c.revalidateGateCacheEntry(ctx, key, id, readable, storeToken, now); ok {
		return cloneCachedGateEntry(cand)
	}
	obs, diag, localFence, revalMeta, cacheable := c.gateObservationsSlow(ctx, task, now)
	if cacheable && storeToken != "" && c.Store != nil && (obs != nil || diag != nil) {
		if evaluatedAt, validUntil, ok := observationCacheWindow(obs, now); ok {
			if tokenAfter, err := c.Store.ChangeToken(ctx); err == nil && tokenAfter == storeToken {
				if prefix, err := json.Marshal(gateObservationFields{
					GateObservations:           obs,
					GateObservationDiagnostics: diag,
				}); err == nil {
					var cachedObs *gates.ObservationView
					if obs != nil {
						cp := obs.Clone()
						cachedObs = &cp
					}
					var cachedDiag *model.GateObservationDiagnostics
					if diag != nil {
						cp := diag.Clone()
						cachedDiag = &cp
					}
					c.gateObsCache.store(key, &gateObservationCacheEntry{
						changeToken: storeToken,
						evaluatedAt: evaluatedAt,
						validUntil:  validUntil,
						localFence:  localFence,
						revalMeta:   revalMeta,
						obs:         cachedObs,
						diag:        cachedDiag,
						fieldsJSON:  prefix,
					})
				}
			}
		}
	}
	return obs, diag
}

func (c *Client) gateObservationsSlow(ctx context.Context, task model.Task, now time.Time) (*gates.ObservationView, *model.GateObservationDiagnostics, *model.GateObservationLocalFence, gateCheckRevalMeta, bool) {
	id := task.ID
	// History can retain linked evidence after the current change link is
	// removed. Authorize the whole optional sidecar, not just today's bindings,
	// before reading or replaying any shared diagnostic cache.
	readable := taskReadableForContext(ctx)
	if !readable(taskRepo(&task)) {
		return nil, nil, nil, gateCheckRevalMeta{}, false
	}
	if !c.observesGates(id) {
		return nil, nil, nil, gateCheckRevalMeta{}, false
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
	needQual := task.Target != nil
	needChecks := len(model.CheckSubjects(task)) > 0
	var (
		healthProjection bool
		qualifications   gates.QualificationCollection
		fence            string
		checks           []gates.ChecksFacts
		revalMeta        gateCheckRevalMeta
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
				return nil, nil, nil, gateCheckRevalMeta{}, false
			}
			if history != nil {
				for _, sample := range history.History {
					if !observationVisible(ctx, &sample.View, canReadTask) {
						return nil, nil, nil, gateCheckRevalMeta{}, false
					}
				}
			}
			return cached, history, nil, gateCheckRevalMeta{}, true
		}
		// Domain reads happen before acquiring the diagnostic writer lock.
		if needQual {
			qualifications, err = gateobserve.CollectQualificationObservationsForTask(collectCtx, c.Store, c.Config.GitHubPullRequests, task, now, func(repo string) bool {
				return allows("ci:read", repo) && requirePullRequestScopes(ctx, repo, APIScope("ci:read")) == nil
			})
		}
		if err == nil && needChecks {
			fence, checks, revalMeta, err = c.collectGateChecks(collectCtx, id, now)
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
			fence, checks, revalMeta, err = "", nil, gateCheckRevalMeta{}, nil
		}
	}
	readCtx, stopRead := context.WithTimeout(model.WithGateHealthAccess(model.WithExternalRefReadable(ctx, readable), access), 250*time.Millisecond)
	defer stopRead()
	var v *gates.ObservationView
	var d *model.GateObservationDiagnostics
	var localFence *model.GateObservationLocalFence
	if err == nil {
		includeHistory := !taskReadScopedForContext(ctx)
		v, d, localFence, err = c.Store.ObserveTaskGatesForRead(readCtx, id, now, includeHistory, needQual && !healthProjection, fence, checks, qualifications, c.Config.GateObservationChildren[id]...)
	}
	if err == nil {
		c.redactGateHistoryScopes(readCtx, id, v, d)
		if !observationVisible(ctx, v, canReadTask) {
			return nil, nil, nil, gateCheckRevalMeta{}, false
		}
		if d != nil {
			for _, sample := range d.History {
				if !observationVisible(ctx, &sample.View, canReadTask) {
					return nil, nil, nil, gateCheckRevalMeta{}, false
				}
			}
		}
		cacheable := !observationWasRaceInvalidated(v, checks)
		if !cacheable {
			localFence = nil
		}
		return v, d, localFence, revalMeta, cacheable
	}
	if errors.Is(err, model.ErrGateObservationScopeDenied) {
		return nil, nil, nil, gateCheckRevalMeta{}, false
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
		return nil, nil, nil, gateCheckRevalMeta{}, false
	}
	return v, nil, nil, gateCheckRevalMeta{}, true
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

	authorizationFence := trustedInternalHealthFence()
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
