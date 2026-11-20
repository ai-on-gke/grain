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

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/gates"
)

// GateHealthAccess is the ordinary caller's current read ceiling. Selecting a
// task for observation grants no additional resource or deployment visibility.
type GateHealthAccess struct {
	ScopeLimited          bool
	AuthorizationRevision uint64
	PublicationRepository func(string) bool
	Repository            func(string) bool
	PipelineRepository    func(string) bool
	DeploymentWide        bool
}

func (a GateHealthAccess) allows(repo string, pipeline bool) bool {
	if pipeline {
		return a.PipelineRepository != nil && a.PipelineRepository(repo)
	}
	return a.Repository != nil && a.Repository(repo)
}
func healthGoal(g *TaskGoal) bool {
	return g != nil && slices.Contains([]GoalKind{GoalBaseGreen, GoalNightlyGreen, GoalPipelineHealthy}, g.Kind)
}
func gateTaskGoal(ctx context.Context, q querier, id string) (*TaskGoal, error) {
	g, err := scanTaskGoal(q.QueryRowContext(ctx, "SELECT "+taskGoalColumns+" FROM `task_goal` WHERE `task_id` = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}
func healthBindings(t Task, g TaskGoal, repos []string) []gates.Binding {
	repo := g.Spec.Repo
	if repo == "" && t.Target != nil {
		repo = t.Target.String()
	}
	if repo == "" && len(repos) > 0 {
		repo = repos[0]
	}
	if repo == "" {
		repo = "unavailable"
	}
	if g.Kind == GoalPipelineHealthy && len(repos) > 0 {
		repo = repos[0]
	}
	ref := g.Spec.Branch
	if ref == "" {
		ref = t.Base
	}
	if ref == "" {
		ref = "main"
	}
	subject := gates.Subject{Kind: "repository", Repository: repo, Ref: ref}
	origin := gates.Origin{Kind: "task", ID: t.ID, Revision: gates.Digest(struct {
		Declaration GoalDecl
		At          time.Time
		By, Build   string
	}{g.Decl(), g.DeclaredAt, g.DeclaredBy, gates.Implementation}), Imported: true}
	out := []gates.Binding{}
	add := func(id, typ string, c any) {
		raw, _ := json.Marshal(c)
		bindingOrigin := origin
		var profile struct{ Semantics string }
		_ = json.Unmarshal(raw, &profile)
		bindingOrigin.Imported = strings.HasPrefix(profile.Semantics, "legacy_")
		out = append(out, gates.Binding{ID: id, Type: typ, Version: 1, Purpose: "monitor", Subject: subject, Config: raw, FreshnessSeconds: 60, Origin: bindingOrigin})
	}
	switch g.Kind {
	case GoalBaseGreen:
		add("base-legacy", "base_health", gates.BaseHealthConfig{Semantics: "legacy_recorded_v1", AllowNonreproduction: true})
		add("base-current", "base_health", gates.BaseHealthConfig{Semantics: "current_tip_v1", AllowNonreproduction: true})
		if g.Spec.Publishing {
			add("publishing-legacy", "publishing_health", gates.PublishingHealthConfig{Semantics: "legacy_no_finding_v1", ExpectedCommit: "current_tip"})
			add("publishing-current", "publishing_health", gates.PublishingHealthConfig{Semantics: "positive_current_v1", ExpectedCommit: "current_tip"})
		}
	case GoalNightlyGreen:
		semantics := "legacy_500_v1"
		name := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(path.Base(g.Spec.Workflow)), ".yml"), ".yaml")
		if g.DeclaredBy == "automation:github-base-repair" || slices.Contains([]string{"tests", "test", "ci"}, name) {
			semantics = "legacy_verified_push_v1"
		}
		c := gates.NightlyHealthConfig{Workflow: string([]rune(g.Spec.Workflow)[:min(256, utf8.RuneCountInString(g.Spec.Workflow))]), Since: g.DeclaredAt.UTC().Format(time.RFC3339Nano), WindowSeconds: 86400, Semantics: semantics}
		add("nightly-legacy", "nightly_health", c)
		c.Semantics = "window_v1"
		add("nightly-current", "nightly_health", c)
	case GoalPipelineHealthy:
		if len(repos) == 0 {
			repos = []string{repo}
		}
		add("pipeline-legacy", "pipeline_health", gates.PipelineHealthConfig{Repos: repos, Semantics: "legacy_quiet_v1"})
		add("pipeline-current", "pipeline_health", gates.PipelineHealthConfig{Repos: repos, Semantics: "scoped_v1"})
	}
	return out
}
func captureGateFacts(ctx context.Context, q querier, id string, now time.Time, access GateHealthAccess, t *Task, o *Observation, snap *gateTaskSnapshot, selected ...gates.ChildrenConfig) (gates.Facts, string, error) {
	var (
		g   *TaskGoal
		err error
	)
	if snap != nil && snap.goalLoaded {
		g = snap.goal
	} else {
		g, err = gateTaskGoal(ctx, q, id)
		if err != nil {
			return gates.Facts{}, "", err
		}
		if snap == nil {
			snap = &gateTaskSnapshot{}
		}
		snap.goalLoaded = true
		snap.goal = g
	}
	if !healthGoal(g) {
		// Rows supplied by the caller were read inside this same authorized
		// acceptance transaction, never taken from the external preflight.
		if t == nil {
			return gates.Facts{}, "", sql.ErrNoRows
		}
		f, state, run, err := captureReportFactsWithSnap(ctx, q, id, now, t, o, snap)
		if err == nil {
			err = captureOutcomeFacts(ctx, q, &f, now, selected, t, o, run, snap)
		}
		if err == nil {
			reserve, _ := ctx.Value(qualificationBindingReserveKey{}).(int)
			reviewCtx := context.WithValue(ctx, qualificationBindingReserveKey{}, reserve+len(f.Bindings)-2)
			observed := o
			if observed == nil {
				observed = &Observation{TaskID: id}
			}
			change, version, readErr := captureChangeGateFacts(reviewCtx, q, *t, observed)
			if readErr != nil {
				return f, state, readErr
			}
			f.Change = change
			if change != nil {
				f.SourceVersion = gates.Digest([]any{f.SourceVersion, version})
			}
		}
		return f, state, err
	}
	t, err = getTask(ctx, q, id)
	if err != nil {
		return gates.Facts{}, "", err
	}
	if t == nil {
		return gates.Facts{}, "", sql.ErrNoRows
	}
	var state string
	if err = q.QueryRowContext(ctx, "SELECT `state` FROM `task_state` WHERE `task_id` = ?", id).Scan(&state); err != nil {
		return gates.Facts{}, "", err
	}
	repos, scopeKnown, err := healthScopeRepos(ctx, q, *t, *g, access)
	if err != nil {
		return gates.Facts{}, state, err
	}
	f := gates.Facts{TaskID: id, Applicable: true, Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), Bindings: healthBindings(*t, *g, repos), Health: map[string]gates.HealthFacts{}}
	// Check the entire binding scope before consulting any resource cache or
	// history. Denied callers receive an unsaved, evidence-free projection.
	for _, b := range f.Bindings {
		allowed := access.allows(b.Subject.Repository, g.Kind == GoalPipelineHealthy)
		if b.Type == "publishing_health" {
			allowed = access.PublicationRepository != nil && access.PublicationRepository(b.Subject.Repository)
		}
		if g.Kind == GoalPipelineHealthy {
			if len(repos) == 0 {
				allowed = false
			}
			for _, r := range repos {
				allowed = allowed && access.allows(r, true)
			}
			// The legacy profile always scans deployment-wide, even with repo filters.
			if b.ID == "pipeline-legacy" {
				allowed = allowed && access.DeploymentWide
			}
		}
		if !allowed {
			f.Health[b.ID] = gates.HealthFacts{Denied: true, Version: gates.Digest("denied"), ObservedAt: now, ValidUntil: f.ValidUntil}
		}
	}
	// A partial permission projection is evaluated transiently, without caches
	// or history from a broader reader.
	if len(f.Health) > 0 {
		f.Complete = false
	}

	for _, b := range f.Bindings {
		if _, denied := f.Health[b.ID]; denied {
			continue
		}
		h := gates.HealthFacts{Complete: true, ObservedAt: now, ValidUntil: f.ValidUntil, AuthorizationRevision: access.AuthorizationRevision}
		if healthRepoScopeOversized(g.Spec.Repos) || g.Kind == GoalNightlyGreen && utf8.RuneCountInString(g.Spec.Workflow) > 256 {
			h.Complete = false
			h.Reason = "The legacy goal scope exceeds canonical observation bounds; no narrower health verdict is inferred."
			h.Version = gates.Digest(h.Reason)
			f.Health[b.ID] = h
			continue
		}

		if b.ID == "pipeline-current" && !scopeKnown {
			h.Complete = false
			h.Reason = "The configured repository set is absent; deployment inventory is not an expected scope."
			h.Version = gates.Digest(h.Reason)
			f.Health[b.ID] = h
			continue
		}
		repo, parseErr := ParseRepo(b.Subject.Repository)
		if parseErr != nil {
			h.Complete = false
			h.Reason = "Configured repository scope is unavailable."
		} else {
			cfg, e := getRepoConfig(ctx, q, repo)
			if e != nil {
				return f, state, e
			}
			h.PolicyRevision = GateHealthPolicyRevision(cfg)
			switch b.Type {
			case "base_health":
				var proof *GateBaseSource
				h.Base, proof, h.ValidUntil, err = captureBaseHealth(ctx, q, repo, b.Subject.Ref, now, h.ValidUntil)
				if proof != nil {
					h.SourceRevisions = append(h.SourceRevisions, proof.PlanRevision, proof.ConfirmationRevision)
				}
				row, e := getBaseBranchCI(ctx, q, repo, b.Subject.Ref)
				if e != nil {
					return f, state, e
				}
				h.SourceRevisions = append(h.SourceRevisions, gates.Digest(row))
			case "publishing_health":
				var row *BaseBranchRelease
				row, err = getBaseBranchRelease(ctx, q, repo, b.Subject.Ref)
				h.SourceRevisions = append(h.SourceRevisions, gates.Digest(row))
				h.Publishing = &gates.PublishingHealthFacts{}
				if row != nil {
					_, finding := BaseBranchPublishingOf(*row, now)
					h.Publishing.Present = true
					h.Publishing.LegacyFinding = finding
					h.Publishing.Commit = row.Commit
					h.Publishing.Conclusion = row.Conclusion
					h.Publishing.InFlight = row.InFlight()
					if row.FailingSince != nil && !finding {
						deadline := row.FailingSince.Add(BaseBranchNotPublishingFor)
						if deadline.After(now) && deadline.Before(h.ValidUntil) {
							h.ValidUntil = deadline
						}
					}
				}
				p, e := readGateBaseSource(ctx, q, repo, b.Subject.Ref)
				if e != nil {
					return f, state, e
				}
				if p != nil && !now.Before(p.At) && now.Before(p.At.Add(2*time.Minute)) {
					h.Publishing.Tip = p.Tip
				}
			// Existing release rows attest workflow success, not target/commit/artifact
			// publication receipts. Keep current positive publication honestly unknown.
			case "nightly_health":
				if b.ID == "nightly-legacy" && strings.Contains(string(b.Config), "legacy_verified_push_v1") {
					met := g.MetAt != nil
					h.LegacyVerifiedDisposition = &met
					h.SourceRevisions = append(h.SourceRevisions, gates.Digest(struct {
						MetAt             *time.Time
						Measure, Evidence string
					}{g.MetAt, g.Measure, g.Evidence}))
				}
				if b.ID == "nightly-legacy" && strings.Contains(string(b.Config), "legacy_500_v1") {
					jobs, e := listGitHubCIJobs(ctx, q, GitHubCIFilter{Repo: repo.String(), Since: g.DeclaredAt, Limit: 500})
					if e != nil {
						return f, state, e
					}
					for _, j := range jobs {
						at := j.RecordedAt
						if j.CompletedAt != nil {
							at = *j.CompletedAt
						}
						h.LegacyJobs = append(h.LegacyJobs, gates.HealthJob{ID: strconv.FormatInt(j.CheckRunID, 10), Name: j.JobName, Workflow: j.Workflow, Ref: j.Ref, Commit: j.Commit, Conclusion: j.Conclusion, Source: j.Source, App: j.App, RunID: j.RunID, Attempt: j.RunAttempt, At: at})
					}
				} else {
					err = each(ctx, q, "SELECT `proof` FROM `gate_health_workflow_source` WHERE `repo` = ? AND `completed_at` >= ? ORDER BY `completed_at` DESC,`run_id` DESC,`attempt` DESC LIMIT 128", []any{repo.String(), g.DeclaredAt}, func(rows *sql.Rows) error {
						var raw string
						if e := rows.Scan(&raw); e != nil {
							return e
						}
						var run gates.WorkflowHealthRun
						if e := json.Unmarshal([]byte(raw), &run); e != nil {
							return e
						}
						h.Runs = append(h.Runs, run)
						return nil
					})
					// Legacy verified-push goals may have authoritative evidence from before
					// diagnostic collection existed. Such evidence remains unknown here; a
					// free-form goal note is not authenticated run/job provenance.
				}
			case "pipeline_health":
				h.Pipeline, err = capturePipelineHealth(ctx, q, repos, g.Spec.Repos, id, b.ID == "pipeline-legacy", now, &h.ValidUntil)
			}
		}
		if err != nil {
			return f, state, err
		}
		// Hash semantic facts, including leases from collectors but not read time.
		semantic := h
		semantic.ObservedAt = time.Time{}
		semantic.ValidUntil = time.Time{}
		h.Version = gates.Digest(struct {
			Facts gates.HealthFacts
			Build string
		}{semantic, gates.Implementation})
		f.Health[b.ID] = h
		if h.ValidUntil.Before(f.ValidUntil) {
			f.ValidUntil = h.ValidUntil
		}
	}
	f.SourceVersion = gates.Digest(struct {
		Bindings []gates.Binding
		Health   map[string]gates.HealthFacts
		State    string
	}{f.Bindings, healthVersions(f.Health), state})
	return f, state, nil
}
func healthVersions(in map[string]gates.HealthFacts) map[string]gates.HealthFacts {
	out := map[string]gates.HealthFacts{}
	for k, h := range in {
		out[k] = gates.HealthFacts{Version: h.Version}
	}
	return out
}
func captureBaseHealth(ctx context.Context, q querier, repo RepoRef, ref string, now, valid time.Time) (*gates.BaseHealthFacts, *GateBaseSource, time.Time, error) {
	row, err := getBaseBranchCI(ctx, q, repo, ref)
	if err != nil {
		return nil, nil, valid, err
	}
	b := &gates.BaseHealthFacts{}
	if row != nil {
		b.Present = true
		b.Commit = row.Commit
		b.RunID = row.RunID
		b.Conclusion = row.Conclusion
		b.Confirmation = row.Confirmation
		b.Broken = row.Broken()
		b.InFlight = row.RunID != "" && row.Conclusion == ""
	}
	proof, err := readGateBaseSource(ctx, q, repo, ref)
	if err != nil {
		return b, nil, valid, err
	}
	cfg, err := getRepoConfig(ctx, q, repo)
	if err != nil {
		return b, nil, valid, err
	}
	if proof != nil && row != nil && proof.RowRevision == GateBaseRowRevision(*row) && proof.PolicyRevision != "" && proof.PolicyRevision == GateHealthPolicyRevision(cfg) && !now.Before(proof.At) && now.Before(proof.At.Add(2*time.Minute)) {
		b.Tip = proof.Tip
		b.CurrentProof = proof.Complete && proof.PlanRevision != ""
		b.NonreproductionProof = proof.Nonreproduction && proof.ConfirmationRevision != ""
		if deadline := proof.At.Add(2 * time.Minute); deadline.Before(valid) {
			valid = deadline
		}
	}
	return b, proof, valid, nil
}

// Pinned against the existing liveness classifier by boundary tests. These are observation clocks,
// never timers that wake, hold, close or enqueue anything.
const (
	TaskQueuedStallAfter    = 72 * time.Hour
	TaskRunningStallAfter   = 24*time.Hour + 15*time.Minute
	TaskCompletedStallAfter = 12*time.Hour + 15*time.Minute
	TaskDeferredStallAfter  = 15 * time.Minute
)

func TaskHealthStallDeadline(state State, since time.Time) time.Time {
	var d time.Duration
	switch state {
	case StateQueued:
		d = TaskQueuedStallAfter
	case StateRunning:
		d = TaskRunningStallAfter
	case StateCompleted:
		d = TaskCompletedStallAfter
	case StateDeferred:
		d = TaskDeferredStallAfter
	}
	if d == 0 {
		return time.Time{}
	}
	return since.Add(d)
}

// capturePipelineHealth uses one transaction and a bounded complete local scan.
// The legacy profile retains its broad base/task scan and newest-200 notification
// selection, but a truncated quiet scan is unknown, never a healthy verdict.
func capturePipelineHealth(ctx context.Context, q querier, repos, legacyFilter []string, self string, legacy bool, now time.Time, valid *time.Time) (*gates.PipelineHealthFacts, error) {
	p := &gates.PipelineHealthFacts{DeploymentRead: legacy, ScanComplete: true, ExpectedBasesKnown: true}
	versions := []string{}
	defer func() { p.CollectionRevision = gates.Digest(versions) }()
	var bases []BaseBranchCI
	var err error
	if legacy {
		bases, err = listBaseBranchCI(ctx, q, 257)
	} else {
		for _, text := range repos {
			repo, e := ParseRepo(text)
			if e != nil {
				p.ExpectedBasesKnown = false
				continue
			}
			var refs []string
			err = each(ctx, q, "SELECT `branch` FROM `base_branch_ci` WHERE `owner` = ? AND `name` = ? ORDER BY `branch` LIMIT 65", []any{repo.StoredOwner(), repo.Name}, func(rows *sql.Rows) error {
				var ref string
				if e := rows.Scan(&ref); e != nil {
					return e
				}
				refs = append(refs, ref)
				return nil
			})
			if err != nil {
				return p, err
			}
			if len(refs) > 64 {
				p.ExpectedBasesKnown = false
				refs = refs[:64]
			}
			for _, ref := range refs {
				row, e := getBaseBranchCI(ctx, q, repo, ref)
				if e != nil {
					return p, e
				}
				if row != nil {
					bases = append(bases, *row)
					if len(bases) > 256 {
						p.ScanComplete = false
						p.ExpectedBasesKnown = false
						return p, nil
					}
				}
			}
		}
	}
	if err != nil {
		return p, err
	}
	for _, b := range bases {
		versions = append(versions, gates.Digest(b))
	}
	if len(bases) > 256 {
		p.ScanComplete = false
		return p, nil
	}
	if legacy {
		for _, b := range bases {
			p.Bases = append(p.Bases, gates.BaseHealthFacts{Present: true, Broken: b.Broken(), Conclusion: b.Conclusion})
		}
	} else {
		for _, r := range repos {
			repo, e := ParseRepo(r)
			if e != nil {
				p.ExpectedBasesKnown = false
				continue
			}
			cfg, e := getRepoConfig(ctx, q, repo)
			if e != nil {
				return p, e
			}
			versions = append(versions, GateHealthPolicyRevision(cfg))
			refs := []string{}
			proofKnown := false
			for _, row := range bases {
				if row.Repo.Same(repo) {
					_, proof, _, e := captureBaseHealth(ctx, q, repo, row.Branch, now, *valid)
					if e != nil {
						return p, e
					}
					if proof != nil && proof.PolicyRevision == GateHealthPolicyRevision(cfg) && proof.PolicyRevision != "" && !now.Before(proof.At) && now.Before(proof.At.Add(2*time.Minute)) && len(proof.ManagedRefs) > 0 {
						refs = proof.ManagedRefs
						proofKnown = true
						break
					}
				}
			}
			if !proofKnown || len(refs) > 64 {
				p.ExpectedBasesKnown = false
				continue
			}
			for _, ref := range refs {
				b, proof, expiry, e := captureBaseHealth(ctx, q, repo, ref, now, *valid)
				if proof != nil {
					versions = append(versions, proof.PlanRevision, proof.ConfirmationRevision)
				}
				if e != nil {
					return p, e
				}
				p.Bases = append(p.Bases, *b)
				if expiry.Before(*valid) {
					*valid = expiry
				}
			}
		}
	}
	// Preserve old newest-200 ordering before filtering, and detect truncation.
	query := "SELECT `id`,`subject`,`family`,COALESCE(`repo`,'') FROM `notification` WHERE `cleared_at` IS NULL"
	var notificationArgs []any
	if !legacy {
		query += " AND `family` LIKE 'pipeline.%' AND `repo` IN (" + strings.TrimSuffix(strings.Repeat("?,", len(repos)), ",") + ")"
		for _, repo := range repos {
			notificationArgs = append(notificationArgs, repo)
		}
	}
	query += " ORDER BY `id` DESC LIMIT 1001"
	count := 0
	err = each(ctx, q, query, notificationArgs, func(rows *sql.Rows) error {
		var id int64
		var subject, family, repo string
		if e := rows.Scan(&id, &subject, &family, &repo); e != nil {
			return e
		}
		versions = append(versions, gates.Digest(struct {
			ID                    int64
			Subject, Family, Repo string
		}{id, subject, family, repo}))
		count++
		if legacy && count > 200 {
			return nil
		}
		if !strings.HasPrefix(family, "pipeline.") {
			return nil
		}
		filter := repos
		if legacy {
			filter = legacyFilter
		}
		scoped := len(filter) == 0 || repo == "" || slices.Contains(filter, repo)
		// checkGoal first checks repo-filtered notifications, then calls
		// pipelineStewardQuiet, which checks them deployment-wide except
		// for the steward's own regression notification. Preserve both reads.
		steward := legacy && subject != SubjectStewardRegression
		if scoped || steward {
			p.OpenNotifications++
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	if count > 1000 || legacy && count > 200 {
		p.ScanComplete = false
	}
	// Read only the state/clock/queue columns used by this predicate, in one
	// complete bounded query. Full task/grant/link expansion would multiply
	// load per task and spend the diagnostic writer lease on unrelated data.
	taskQuery := "SELECT `s`.`task_id`,`s`.`state`,`t`.`approved_at`,`t`.`undeferred_at`,`t`.`deferred_until`,`o`.`completed_at`,`o`.`retry_requested_at`,`o`.`run_now_at`," +
		"(SELECT MAX(`r`.`finished_at`) FROM `task_run` AS `r` WHERE `r`.`task_id` = `s`.`task_id`)," +
		"(SELECT MIN(`r`.`started_at`) FROM `task_run` AS `r` WHERE `r`.`task_id` = `s`.`task_id` AND `r`.`finished_at` IS NULL)," +
		"`o`.`merge_queue_blocked_at`,`o`.`merge_queue_stalled_at` FROM `task_state` AS `s` JOIN `task` AS `t` ON `t`.`id` = `s`.`task_id` LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `s`.`task_id` WHERE `s`.`state` <> 'closed' AND NOT EXISTS (SELECT 1 FROM `task_pause` AS `p` WHERE `p`.`task_id` = `s`.`task_id`)"
	taskArgs := []any{}
	if legacy {
		// Match pipelineStewardQuiet's complete tagged own-task set exactly.
		// An ordinary untagged pipeline goal is not silently excluded as self.
		taskQuery += " AND NOT EXISTS (SELECT 1 FROM `task_tag` AS `tag` WHERE `tag`.`task_id` = `s`.`task_id` AND `tag`.`tag` = ?)"
		taskArgs = append(taskArgs, ScheduleFiringTag(PipelineStewardTemplateID))
	} else {
		taskQuery += " AND `s`.`task_id` <> ?"
		taskArgs = append(taskArgs, self)
		clauses := []string{}
		for _, text := range repos {
			repo, e := ParseRepo(text)
			if e != nil {
				continue
			}
			clauses = append(clauses, "(`t`.`target_owner` = ? AND `t`.`target_name` = ?)")
			taskArgs = append(taskArgs, repo.StoredOwner(), repo.Name)
		}
		if len(clauses) == 0 {
			p.ScanComplete = false
			return p, nil
		}
		taskQuery += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	taskQuery += " ORDER BY `s`.`task_id` LIMIT 1001"
	taskCount := 0
	err = each(ctx, q, taskQuery, taskArgs, func(rows *sql.Rows) error {
		var id, state string
		var approved, undeferred, until, completed, retried, runNow, blocked, stalled sql.NullTime
		var finishedRaw, liveRaw any
		if e := rows.Scan(&id, &state, &approved, &undeferred, &until, &completed, &retried, &runNow, &finishedRaw, &liveRaw, &blocked, &stalled); e != nil {
			return e
		}
		finished, e := scanSubqueryTime(finishedRaw)
		if e != nil {
			return e
		}
		live, e := scanSubqueryTime(liveRaw)
		if e != nil {
			return e
		}
		taskCount++
		if taskCount > 1000 {
			p.ScanComplete = false
			return nil
		}
		times := StateTimes{ApprovedAt: nullableTime(approved), RequeuedAt: latest(nullableTime(undeferred), nullableTime(retried), nullableTime(runNow)), DeferredUntil: nullableTime(until), CompletedAt: nullableTime(completed), LastRunFinishedAt: finished, LiveRunStartedAt: live}
		versions = append(versions, gates.Digest(struct {
			ID, State        string
			Times            StateTimes
			Blocked, Stalled *time.Time
		}{id, state, times, nullableTime(blocked), nullableTime(stalled)}))
		st := State(state)
		if since := times.Since(st); since != nil {
			deadline := TaskHealthStallDeadline(st, *since)
			if !deadline.IsZero() {
				if !now.Before(deadline) {
					p.StalledTasks++
				} else if deadline.Before(*valid) {
					*valid = deadline
				}
			}
		} else if !legacy && slices.Contains([]State{StateQueued, StateRunning, StateCompleted}, st) {
			p.ScanComplete = false
		}
		if st == StateCompleted && (blocked.Valid || stalled.Valid) {
			p.BlockedQueue++
		}
		return nil
	})
	if err != nil {
		return p, err
	}

	return p, nil
}

// scanSubqueryTime converts a nullable timestamp returned by an aggregate
// scalar subquery (MAX/MIN over DATETIME columns) into a UTC *time.Time on
// both SQLite (which reports expression columns as strings) and PostgreSQL
// (which returns time.Time). Non-nil unparseable values fail closed.
func scanSubqueryTime(v any) (*time.Time, error) {
	if v == nil {
		return nil, nil
	}
	if t := parseScannedTime(v); t != nil {
		return t, nil
	}
	return nil, fmt.Errorf("parse subquery timestamp (%T)", v)
}
