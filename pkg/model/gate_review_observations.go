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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// Bound fanout independently of history retention and the 250ms read budget.
// An overflow is explicit incomplete evidence, never a smaller passing set.
const maxShadowChangeScopes = 31

// captureChangeGateFacts reuses domain task/link/observation and base-declaration
// records in the same transaction. It never calls a reconciler, provider, action
// service or global States/PathTriggers scan. In particular, local clearance and
// CI stamps do NOT prove a current provider head or complete forge collection.
func captureChangeGateFacts(ctx context.Context, q querier, t Task, o *Observation) (*gates.ChangeFacts, string, error) {
	pr := TaskPullRequest(t)
	if pr == nil {
		return nil, "", nil
	}
	ref := *pr
	c := &gates.ChangeFacts{Subject: gates.Subject{Kind: "change", Authority: ref.Repo.GitHost(), Repository: ref.Repo.String(), ChangeKind: "pr", ID: strconv.Itoa(ref.Number)},
		Complete: true, Reviews: map[string]gates.ReviewFacts{}, Approvals: map[string]gates.ApprovalFacts{}}
	// There is no durable authenticated current-head/provider findings collection
	// or rendered Submit/policy/role receipt in the legacy model. Leave those pins
	// absent. Do not relabel a CI commit, carried stamp or closed task as that proof.
	c.IncompleteReason = "Scoped local records cannot prove the current provider head, complete diff/findings or current authorization receipts."
	resources := 0
	for _, link := range t.Links {
		if link.Kind.IsChange() {
			resources++
		}
	}
	if resources > 1 {
		c.Complete = false
		c.IncompleteReason = "Multiple change resources require separate complete provider reads; this primary-resource projection is incomplete."
	}
	if !boundedShadowLinks(t.Links) {
		c.Complete = false
		c.IncompleteReason = "Linked metadata exceeds the 512-link/65536-byte reader bound; exact source evidence is unavailable."
		c.Reviews["collection"] = gates.ReviewFacts{Category: "ordinary", LegacyStatus: "unknown", LegacyDetail: c.IncompleteReason}
		c.Approvals["domain:submit"] = gates.ApprovalFacts{LegacyDetail: c.IncompleteReason}
		return c, gates.Digest([]any{t.ID, len(t.Links), "oversized-linked-metadata", gates.Implementation}), nil
	}
	var source []any
	policySource := []any{t.ReviewTemplateID, trustboundary.Version}
	for _, link := range t.Links {
		if link.Kind == LinkApprovalRequired {
			policySource = append(policySource, link.Target)
		}
	}
	source = append(source, gates.Implementation, t.ReviewTemplateID, t.AutoMerge, t.Filing, t.Links)
	if o != nil {
		source = append(source, o.CI, o.PrMergedAt, o.ForceMergeHeadSHA, o.ForceMergeSkipped, o.ForceMergeAt, o.ForceMergeBy, o.ForceMergeReason)
	}
	type assignment struct{ scope, category, task string }
	assignments := []assignment{}
	add := func(scope, category, task string) {
		scope = shadowRequirementID(scope)
		if _, exists := c.Reviews[scope]; exists {
			return
		}
		if len(c.Reviews)+len(c.Approvals) >= maxShadowChangeScopes {
			c.Complete = false
			c.IncompleteReason = "Review/approval scope exceeds the 31-scope reader bound; the displayed subset is incomplete."
			return
		}
		c.Reviews[scope] = gates.ReviewFacts{AssignedReviewID: task, Category: category, AssignmentReadable: true, AssignmentPresent: task != "",
			AssignmentVersion: gates.Digest([]any{scope, task, t.ReviewTemplateID}),
			LegacyStatus:      "unknown", LegacyDetail: "No corresponding legacy clearance is recorded.", Evidence: []gates.Evidence{}}
		assignments = append(assignments, assignment{scope, category, task})
	}
	addApproval := func(id, detail string) {
		id = shadowRequirementID(id)
		if _, exists := c.Approvals[id]; exists {
			return
		}
		if len(c.Reviews)+len(c.Approvals) >= maxShadowChangeScopes {
			c.Complete = false
			c.IncompleteReason = "Review/approval scope exceeds the 31-scope reader bound; the displayed subset is incomplete."
			return
		}
		c.Approvals[id] = gates.ApprovalFacts{LegacyDetail: detail, Evidence: []gates.Evidence{}}
	}
	for _, l := range t.Links {
		switch l.Kind {
		case LinkReviewTask:
			add("ordinary/task:"+l.Target, "ordinary", l.Target)
		case LinkSecurityReviewTask:
			add("security/task:"+l.Target, "security", l.Target)
		case LinkPathTriggerTask:
			name, id, ok := strings.Cut(l.Target, "=")
			if ok {
				add("trigger:"+name+"/task:"+id, "path", id)
			} else {
				c.Complete = false
			}
		case LinkTakeoverReview:
			id, _, ok := strings.Cut(l.Target, "@")
			if ok {
				add("takeover/task:"+id, "takeover", id)
			} else {
				c.Complete = false
			}
		}
	}
	for _, finding := range ReviewFindings(t) {
		category, prefix := "ordinary", "ordinary"
		if finding.Gate == GateSecurityReview {
			category, prefix = "security", "security"
		}
		if strings.HasPrefix(finding.Gate, "trigger:") {
			category, prefix = "path", finding.Gate
		}
		if finding.Gate == GateQA {
			continue
		}
		scope := prefix + "/task:" + finding.ReviewTask
		if finding.ReviewTask == "" {
			scope = prefix + "/unattributed"
		}
		add(scope, category, finding.ReviewTask)
	}
	for _, reason := range HumanReviewReasons(t) {
		if reason.Source == HumanReviewSecurityFindings {
			add("security/task:"+reason.ReviewTask, "security", reason.ReviewTask)
		}
	}
	if questions, ok := ReviewQuestionsOf(t); ok && !slices.ContainsFunc(assignments, func(a assignment) bool { return a.task == questions.ReviewTaskID }) {
		add("ordinary/task:"+questions.ReviewTaskID, "ordinary", questions.ReviewTaskID)
	}
	for _, link := range t.Links {
		switch link.Kind {
		case LinkTakeover:
			if !slices.ContainsFunc(assignments, func(a assignment) bool { return a.category == "takeover" }) {
				add("takeover/assignment", "takeover", "")
			}
		case LinkTargetedReviewTrigger:
			if !slices.ContainsFunc(assignments, func(a assignment) bool { return strings.HasPrefix(a.scope, "trigger:"+link.Target+"/") }) {
				add("trigger:"+link.Target, "path", "")
			}
		case LinkHumanOnlyHold:
			addApproval("builtin:human-only", "Legacy human-only hold needs an eligible person's decision; a reviewer cannot self-clear it.")
		case LinkBrokenRulesHold:
			addApproval("builtin:broken-rules", "Legacy broken base rules remain held for a person; unknown policy is never an approval.")
		case LinkPathTriggerHold:
			if link.Target == string(TriggerHumanRequired) || link.Target == string(TriggerHumanOnFindings) {
				addApproval("hold:path:"+link.Target, "Legacy path human "+link.Target+" hold remains owned by the existing review and Submit services.")
			}
		}
	}
	if t.ReviewTemplateID != "" && !slices.ContainsFunc(assignments, func(a assignment) bool { return a.category == "ordinary" }) {
		add("ordinary/template:"+t.ReviewTemplateID, "ordinary", "")
	}
	// A local no-hold stamp is only a legacy classification; an unavailable or
	// truncated complete diff must never become a new no-match pass.
	if !slices.ContainsFunc(assignments, func(a assignment) bool { return a.category == "security" }) {
		add("security/classification", "security", "")
	}
	addApproval("domain:submit", "AutoMerge is queue intent; legacy Submit captures the click-time head, without a rendered-head/current-policy receipt.")
	for _, req := range ApprovalRequirements(t) {
		detail := "Named approval is outstanding in the legacy service."
		if req.ApprovedBy != "" {
			detail = "Legacy named approval marker names " + req.ApprovedBy + ", without rendered-head, policy and role-version pins."
		}
		tokens := req.Approvers
		if len(tokens) > 8 {
			tokens = tokens[:8]
		}
		detail += " Recorded approver tokens: " + strings.Join(tokens, ", ") + "."
		if len(req.Approvers) > 8 {
			detail += fmt.Sprintf(" %d additional tokens are shown by the existing approval service.", len(req.Approvers)-8)
		}
		addApproval(req.Rule, detail)
	}
	for _, hold := range HumanReviewReasons(t) {
		if hold.Source == HumanReviewSecurityFindings {
			continue
		} // acceptance is separate
		addApproval("hold:"+string(hold.Source)+"/"+gates.Digest([]any{hold.ReviewTask, hold.Run, hold.Reason})[7:],
			"Legacy human-only/requested hold remains owned by the existing Submit/release service.")
	}
	if t.Target != nil && t.Target.Same(ref.Repo) {
		var syncCommit, syncError string
		err := q.QueryRowContext(ctx, "SELECT `commit`,SUBSTR(`last_error`,1,8192) FROM `repo_declaration_sync` WHERE `owner` = ? AND `name` = ?", ref.Repo.StoredOwner(), ref.Repo.Name).Scan(&syncCommit, &syncError)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
		source = append(source, []any{syncCommit, syncError})
		policySource = append(policySource, []any{syncCommit, syncError})
		if syncError != "" {
			c.Complete = false
			c.IncompleteReason = "Base-owned declaration synchronization is unavailable or broken."
		}
		// Read only this repository's current base-owned declarations. Empty rows
		// are a complete local set, not proof of a complete current diff or policy
		// synchronization at the provider.
		rows, err := q.QueryContext(ctx, "SELECT `commit`,SUBSTR(`rule`,1,65536),LENGTH(`rule`) FROM `repo_path_trigger` WHERE `owner` = ? AND `name` = ? ORDER BY `trigger` LIMIT 65", ref.Repo.StoredOwner(), ref.Repo.Name)
		if err != nil {
			return nil, "", err
		}
		n := 0
		for rows.Next() {
			var commit, raw string
			var size int
			if err = rows.Scan(&commit, &raw, &size); err != nil {
				rows.Close()
				return nil, "", err
			}
			n++
			source = append(source, []any{commit, raw, size})
			policySource = append(policySource, []any{commit, raw, size})
			if n > 64 || size > 65536 {
				c.Complete = false
				c.IncompleteReason = "Base declaration collection exceeds its 64-row/65536-character reader bound."
				continue
			}
			var rule PathTrigger
			if json.Unmarshal([]byte(raw), &rule) != nil {
				c.Complete = false
				c.IncompleteReason = "Base-owned path declaration is malformed; applicability is unknown."
				continue
			}
			if rule.On != TriggerOnPullRequest || !rule.Gate {
				continue
			}
			// Even an as-yet unclassified/new rule must be visible as unknown.
			prefix := "trigger:" + rule.Name + "/task:"
			if !slices.ContainsFunc(assignments, func(a assignment) bool { return strings.HasPrefix(a.scope, prefix) }) {
				add("trigger:"+rule.Name, "path", "")
			}
			if rule.Human == TriggerHumanRequired {
				addApproval("trigger:"+rule.Name, "Legacy human: required needs a person's Submit; a clean automated review cannot release it.")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, "", err
		}
		var commit, raw string
		var size int
		err = q.QueryRowContext(ctx, "SELECT `commit`,SUBSTR(`rules`,1,65536),LENGTH(`rules`) FROM `repo_merge_rules` WHERE `owner` = ? AND `name` = ?", ref.Repo.StoredOwner(), ref.Repo.Name).Scan(&commit, &raw, &size)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, "", err
		}
		source = append(source, []any{commit, raw, size})
		policySource = append(policySource, []any{commit, raw, size})
		if size > 65536 {
			c.Complete = false
			c.IncompleteReason = "Base merge policy exceeds the 65536-character reader bound."
		}
	} else {
		c.Complete = false
		c.IncompleteReason = "Change policy is outside the authorized task repository scope."
	}
	c.PolicyVersion = gates.Digest(policySource)
	// Read each related task at most once. Only tasks in the subject's already
	// authorized repository are inspected, even for unrestricted operators. This
	// makes persisted history safe for later restricted readers of this task.
	states := map[string]State{}
	type related struct {
		task      *Task
		obs       *Observation
		state     State
		version   string
		readable  bool
		failedRun bool
	}
	relatedCache := map[string]related{}
	relatedReads := 0
	readRelated := func(id string) (related, error) {
		if v, ok := relatedCache[id]; ok {
			return v, nil
		}
		if relatedReads >= 64 {
			c.Complete = false
			c.IncompleteReason = "Related review/fix collection exceeds the 64-task reader bound."
			return related{}, nil
		}
		relatedReads++
		var owner, name string
		err := q.QueryRowContext(ctx, "SELECT COALESCE(`target_owner`,''),COALESCE(`target_name`,'') FROM `task` WHERE `id` = ?", id).Scan(&owner, &name)
		if errors.Is(err, sql.ErrNoRows) {
			v := related{readable: true}
			relatedCache[id] = v
			return v, nil
		}
		if err != nil {
			return related{}, err
		}
		repo := RepoFromStored(owner, name)
		if t.Target == nil || !repo.Same(*t.Target) {
			v := related{}
			relatedCache[id] = v
			return v, nil
		}
		rt, err := getTask(ctx, q, id)
		if err != nil {
			return related{}, err
		}
		if !boundedShadowLinks(rt.Links) {
			c.Complete = false
			c.IncompleteReason = "Related task metadata exceeds the 512-link/65536-byte reader bound."
			v := related{version: gates.Digest([]any{id, len(rt.Links), "oversized-linked-metadata"})}
			relatedCache[id] = v
			return v, nil
		}
		ro, err := getObservation(ctx, q, id)
		if err != nil {
			return related{}, err
		}
		var state string
		if err = q.QueryRowContext(ctx, "SELECT `state` FROM `task_state` WHERE `task_id` = ?", id).Scan(&state); err != nil {
			return related{}, err
		}
		var lastComment, lastEvent int64
		if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(`id`),0) FROM `task_comment` WHERE `task_id` = ?", id).Scan(&lastComment); err != nil {
			return related{}, err
		}
		if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(`id`),0) FROM `task_event` WHERE `task_id` = ?", id).Scan(&lastEvent); err != nil {
			return related{}, err
		}
		var runID, outcome string
		var runStarted time.Time
		var runFinished sql.NullTime
		err = q.QueryRowContext(ctx, "SELECT `id`,`started_at`,`finished_at`,COALESCE(`outcome`,'') FROM `task_run` WHERE `task_id` = ? ORDER BY `attempt` DESC LIMIT 1", id).Scan(&runID, &runStarted, &runFinished, &outcome)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return related{}, err
		}
		var progress any
		if ro != nil {
			progress = []any{ro.CompletedAt, ro.ClosedAt, ro.PendingQuestionCommentID, ro.PrMergedAt, ro.CI}
		}
		version := gates.Digest([]any{rt.Links, rt.Tags, rt.Filing, rt.Approval, rt.DeferredAt, progress, state, runID, runStarted, runFinished, outcome, lastComment, lastEvent})
		v := related{task: rt, obs: ro, state: State(state), version: version, readable: true, failedRun: runFinished.Valid && (outcome == "failed" || outcome == "errored")}
		relatedCache[id] = v
		return v, nil
	}
	for _, a := range assignments {
		r := c.Reviews[a.scope]
		if a.task != "" {
			v, err := readRelated(a.task)
			if err != nil {
				return nil, "", err
			}
			r.AssignmentReadable = v.readable
			r.AssignmentPresent = v.task != nil
			source = append(source, []any{a.scope, v.version, v.readable, v.task != nil})
			if v.task != nil {
				states[a.task] = v.state
				for _, link := range v.task.Links {
					if link.Kind != LinkFixTask && link.Kind != LinkDependsOn {
						continue
					}
					dep, err := readRelated(link.Target)
					if err != nil {
						return nil, "", err
					}
					source = append(source, []any{a.scope, link.Kind, link.Target, dep.version, dep.readable})
					if !dep.readable || dep.task == nil {
						r.FixesComplete = false
						continue
					}
					if dep.obs != nil && dep.obs.PrMergedAt != nil {
						continue
					}
					if dep.state == StateClosed || dep.state == StateFailed {
						r.FixesFailed++
					} else {
						r.FixesPending++
					}
				}
				r.AssignmentVersion = gates.Digest([]any{r.AssignmentVersion, v.version})
				r.Running = v.state == StateRunning
				r.Failed = v.state == StateFailed || v.failedRun
				r.Evidence = append(r.Evidence, gates.Evidence{Kind: "review", ID: shadowRequirementID("legacy-task:" + a.task), Version: v.version, Subject: c.Subject, Digest: v.version})
				if a.category == "ordinary" {
					if v.state == StateClosed || (v.state == StateCompleted && TaskPullRequest(*v.task) == nil) {
						r.LegacyStatus = "met"
						r.LegacyDetail = "Ordinary review is closed or completed empty-handed; findings and exact-head provenance are separate."
					} else {
						r.LegacyStatus = "pending"
						r.LegacyDetail = "Ordinary review has not closed or completed empty-handed."
					}
				}
				if TaskPullRequest(*v.task) != nil && (v.obs == nil || v.obs.PrMergedAt == nil) {
					if v.state == StateClosed {
						r.FixesFailed++
					} else {
						r.FixesPending++
					}
				}
			} else if a.category == "ordinary" && v.readable {
				r.LegacyStatus = "met"
				r.LegacyDetail = "Legacy reviewStateOf treats a missing referenced task as finished."
			}

		} else if a.category == "ordinary" {
			r.LegacyStatus = "pending"
			r.LegacyDetail = "Configured ordinary review is still owed."
		}
		for _, finding := range ReviewFindings(t) {
			if finding.ReviewTask == a.task && a.task != "" {
				r.Findings++
			}
		}
		if questions, ok := ReviewQuestionsOf(t); ok && questions.ReviewTaskID == a.task && a.task != "" {
			r.Questions++
		}
		for _, reason := range HumanReviewReasons(t) {
			if reason.Source == HumanReviewSecurityFindings && reason.ReviewTask == a.task && a.task != "" {
				r.Findings++
			}
		}
		r.FindingsDigest = gates.Digest([]any{a.scope, ReviewFindings(t), t.Links})
		// Legacy acceptance records deliberately do not construct a new disposition:
		// they omit review output/current policy/role revisions. Comments never do.
		c.Reviews[a.scope] = r
	}
	legacyGates := ComputeMergeGates(t, o, states)
	legacyContext := ""
	if override, ok := LatestGateOverride(t); ok {
		legacyContext += fmt.Sprintf(" Named legacy force override scopes %v at recorded head %s remain authoritative; no shadow waiver is issued.", override.Gates, override.HeadSHA)
	}
	for _, l := range t.Links {
		if l.Kind == LinkBaseRepairBypass {
			legacyContext += " Legacy base-repair exception is recorded; own CI and domain merge readiness remain separate."
			break
		}
	}
	if o != nil && o.PrMergedAt != nil {
		legacyContext += " Already merged: legacy post-merge review ownership is unchanged."
	}
	for id, r := range c.Reviews {
		if r.Category == "ordinary" {
			r.LegacyDetail += legacyContext
			c.Reviews[id] = r
			continue
		}
		gate := GateSecurityReview
		if r.Category == "path" {
			gate = GateReview
			for _, l := range t.Links {
				if l.Kind == LinkTargetedReviewTrigger && strings.HasPrefix(id, "trigger:"+l.Target+"/") {
					gate = GateTargetedReview
				}
			}
		}
		if r.Category == "takeover" {
			r.LegacyStatus = "pending"
			r.LegacyDetail = "Legacy takeover owns its review hold until its takeover-cleared marker is recorded."
			for _, l := range t.Links {
				if l.Kind == LinkTakeoverCleared && l.Target != "" {
					r.LegacyStatus = "met"
					r.LegacyDetail = "Legacy takeover-cleared marker is recorded; complete exact-scope provenance is not persisted."
				}
			}
			r.LegacyDetail += legacyContext
			c.Reviews[id] = r
			continue
		}
		for _, g := range legacyGates {
			if g.ID != gate {
				continue
			}
			switch g.State {
			case GateStatePassed, GateStateOverridden:
				r.LegacyStatus = "met"
			case GateStateRunning, GateStateNeeded:
				r.LegacyStatus = "pending"
			default:
				r.LegacyStatus = "unmet"
			}
			r.LegacyDetail = fmt.Sprintf("Legacy aggregate %s gate is %s; it is not proof for this individual assignment.", gate, g.State)
		}
		r.LegacyDetail += legacyContext
		c.Reviews[id] = r
	}
	// One informational hashed local evidence record per approval scope. It says
	// nothing about exact rendered context, identity eligibility or role revocation.
	for id, a := range c.Approvals {
		version := gates.Digest([]any{id, t.Links, c.PolicyVersion})
		a.Evidence = append(a.Evidence, gates.Evidence{Kind: "approval", ID: "legacy-scope:" + id, Version: version, Subject: c.Subject, Digest: version})
		c.Approvals[id] = a
	}
	if !c.Complete && c.IncompleteReason == "" {
		c.IncompleteReason = "Policy or assignment collection is incomplete or malformed."
	}
	source = append(source, c)
	return c, gates.Digest(source), nil
}

func shadowRequirementID(id string) string {
	if len(id) <= 200 && strings.TrimSpace(id) != "" {
		return id
	}
	return "scope:" + gates.Digest(id)[7:]
}

func observationBindings(id string, f gates.Facts) []gates.Binding {
	b := gates.Bindings(id)
	if f.Change != nil {
		b = append(b, gates.ChangeBindings(id, *f.Change)...)
	}
	return b
}

// Hash work is bounded even when a legacy task has accumulated many links.
// A partial/truncated source is diagnostic unknown, never an empty passing set.
func boundedShadowLinks(links []Link) bool {
	if len(links) > 512 {
		return false
	}
	size := 0
	for _, link := range links {
		size += len(link.Kind) + len(link.Target)
		if size > 65536 {
			return false
		}
	}
	return true
}
