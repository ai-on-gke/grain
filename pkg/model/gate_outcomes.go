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
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gates"
)

const maxOutcomeResources = 64

func readGateSources(ctx context.Context, q querier, id string) (map[string]GateOutcomeSource, bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT `data` FROM `gate_outcome_source` WHERE `task_id` = ? ORDER BY `resource` LIMIT 67", id)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	sources := map[string]GateOutcomeSource{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var source GateOutcomeSource
		if err = json.Unmarshal([]byte(raw), &source); err != nil {
			return nil, false, err
		}
		sources[source.Resource] = source
	}
	return sources, len(sources) <= maxOutcomeResources+2, rows.Err()
}

// captureOutcomeFacts reads one authorized local scope. Relations never widen
// that scope, select spawned children, call a provider or install a wait.
func captureOutcomeFacts(ctx context.Context, q querier, f *gates.Facts, now time.Time, selected []gates.ChildrenConfig, t *Task, o *Observation, run gateReportRun, snap *gateTaskSnapshot) error {
	if o == nil {
		o = &Observation{}
	}
	sources, complete, err := readGateSources(ctx, q, f.TaskID)
	if err != nil {
		return err
	}
	runID, outcome, started, finished, noWork := run.runID, run.outcome, run.started, run.finished, run.noWork
	f.Bindings = gates.Bindings(f.TaskID)
	f.LegacyMet = map[string]bool{}
	f.Resources = map[string][]gates.ResourceFact{}
	f.Children = map[string]gates.ChildFact{}
	rest := sources["goal-rest"]
	goalRest := rest.RunID == runID && rest.State == "resting_on_goal" && rest.Policy == GateOutcomePolicy(*t) && !rest.At.Before(started) && t.DeferredAt != nil && t.DeferredAt.Truncate(time.Microsecond).Equal(rest.At.Truncate(time.Microsecond))
	f.Blocked = o.PendingQuestionCommentID != nil || o.PendingAccess != nil || o.PendingSecret != "" || outcome == ParkedOutcome && !goalRest
	f.Applicable = !NeverCompletes(*t) && !t.Interactive && !t.HasConsole() && !t.KeepRunning
	f.UnsupportedReason = "Interactive or standing controller policy has no finite completion prediction."
	f.ChildAnswer = ClosesOnAnswer(*t)
	f.NothingAck = t.NothingNeededAction.Resolved() == NothingNeededAck
	if outcome == "no_action" || outcome == InterruptedOutcome {
		f.Settled = false
	}
	// Only tasks that await acknowledgment (no change link, not a Grain review),
	// are not review-intent tasks, and did not declare nothing_needed project a
	// settled closing note as a report product.
	isNoWorkRun := outcome == NothingNeededOutcome || (noWork > 0 && AwaitsAck(*t))
	canBeReport := AwaitsAck(*t) && t.Intent != IntentReview && !isNoWorkRun
	if f.Report != nil && f.Settled && canBeReport {
		f.Product = "report"
		f.ProductEvidence = f.Report
	}
	finish, recorded := sources["finish"]
	validFinish := f.Settled && recorded && finish.RunID == runID && finished.Valid && !finish.At.Before(started) && finish.Policy == GateOutcomePolicy(*t) && !f.Blocked
	if validFinish {
		if finish.Product != "" && finish.Product != "report" {
			f.Product = finish.Product
			version := gates.Digest(finish)
			f.ProductEvidence = &gates.Evidence{Kind: "report", ID: finish.Product + "/" + runID, Version: version, Subject: gates.Subject{Kind: "task", TaskID: f.TaskID}, Digest: version}
			if finish.Product == "review" {
				f.ProductEvidence.Kind = "review"
			}
		} else if finish.Product == "report" && canBeReport && f.Report == nil {
			f.Product = "report"
		}
	}
	if f.Product == "report" && f.Report == nil {
		f.ProductUnknown = "The finish record lacks a correlated durable report."
	}
	inPlace, acceptanceRevision, err := captureInPlaceDelivery(ctx, q, *t, now)
	if err != nil {
		return err
	}
	if reason, unknown := acceptanceRevision.(string); unknown && reason == "owner unavailable" {
		f.ProductUnknown = "Owning-change acceptance is unavailable in this read scope."
	}
	if inPlace != nil && f.Settled && !f.Blocked {
		f.Product = "change"
		f.ProductEvidence = inPlace
	}
	// A live/parked run cannot make a prior report its current finished product.
	if !f.Settled {
		f.Product = ""
		f.ProductEvidence = nil
	}
	add := func(id, typ string, config any, subject gates.Subject, imported bool) {
		raw, _ := json.Marshal(config)
		f.Bindings = append(f.Bindings, gates.Binding{ID: id, Type: typ, Version: 1, Purpose: "monitor", Subject: subject, Config: raw, FreshnessSeconds: 60, Origin: gates.Origin{Kind: "task", ID: f.TaskID, Revision: gates.Implementation, Imported: imported}})
	}
	subject := gates.Subject{Kind: "task", TaskID: f.TaskID}
	if t.Intent != IntentAnalyze || !AwaitsAck(*t) || f.ChildAnswer || f.Product == "nothing_needed" || isNoWorkRun {
		for _, product := range []string{"change", "review", "nothing_needed", "any"} {
			add("delivery-"+product, "delivery", gates.DeliveryConfig{Product: product, Semantics: "current_v1"}, subject, false)
		}
		for _, mode := range []string{"legacy_report", "legacy_child_answer", "legacy_nothing_needed_ack"} {
			add(mode, "acknowledgment", gates.AcknowledgmentConfig{Mode: mode}, subject, true)
		}
	}
	for _, kind := range []string{"pr_merged", "cl_submitted"} {
		resources, changeSubject := gateIntegrationResources(*t, sources, complete, kind, "")
		f.Resources[kind] = resources
		if kind == "pr_merged" {
			f.LegacyMet[kind] = o.PrMergedAt != nil
		} else {
			f.LegacyMet[kind] = legacyCLsSubmitted(*t, "")
		}
		if len(resources) > 0 {
			add(kind, "integration", gates.IntegrationConfig{Kind: kind, Semantics: "positive_v1"}, changeSubject, false)
		}
	}
	if f.Settled && !f.Blocked {
		for _, resource := range f.Resources["pr_merged"] {
			if resource.State == "equivalent" && resource.Complete && !resource.ObservedAt.Before(started) && now.Before(resource.ValidUntil) {
				f.Product = "change"
				e := resource.Evidence
				f.ProductEvidence = &e
				f.ProductUnknown = ""
			}
		}
	}
	if f.Settled && !f.Blocked && isNoWorkRun && (!validFinish || finish.Product != "nothing_needed") {
		f.ProductUnknown = "No-work settlement lacks current authenticated finish provenance."
	}
	if f.Settled && !f.Blocked && !canBeReport && f.Product == "" && f.ProductUnknown == "" {
		if t.Filing.Reason == ReasonReview || t.Intent == IntentReview {
			f.ProductUnknown = "Settled review task lacks current authenticated finish provenance."
		} else {
			f.ProductUnknown = "Settled change task lacks current authenticated finish provenance."
		}
	}
	if f.Product == "change" && f.ProductEvidence != nil && inPlace == nil {
		verified := false
		for _, resources := range f.Resources {
			if len(resources) == 0 {
				continue
			}
			known := true
			for _, resource := range resources {
				if !resource.Complete || !now.Before(resource.ValidUntil) || !oneOfOutcome(resource.State, "open", "closed", "abandoned", "merged", "submitted", "equivalent") {
					known = false
				}
			}
			if known {
				verified = true
				evidence := resources[0].Evidence
				evidence.Version = gates.Digest(resources)
				evidence.Digest = evidence.Version
				f.ProductEvidence = &evidence
			}
		}
		if !verified {
			f.ProductUnknown = "The finish selected a change, but its complete exact-head provenance is unavailable."
		}
	}
	// No-work may have no report body at all. Pin its compatibility receipt to
	// the authenticated current finish product, never to an incidental report.
	if f.Product == "nothing_needed" {
		if f.Settled && !f.Blocked && f.ProductEvidence != nil && f.ProductUnknown == "" && o.CompletedAt != nil {
			f.NoWorkReceipt, _, err = captureProductReceipt(ctx, q, f.TaskID, o.ClosedAt, *o.CompletedAt, finish.At, *f.ProductEvidence)
			if err != nil {
				return err
			}
		}
	}
	legacyChildBinding := ""
	var goalPtr *TaskGoal
	if snap != nil && snap.goalLoaded {
		goalPtr = snap.goal
	} else {
		goalPtr, err = gateTaskGoal(ctx, q, f.TaskID)
		if err != nil {
			return err
		}
	}
	var g TaskGoal
	if goalPtr != nil {
		g = *goalPtr
		switch g.Kind {
		case GoalTasksClosed:
			predicate := g.Spec.Until
			if predicate == "" {
				predicate = "closed"
			}
			config := gates.ChildrenConfig{Tasks: g.Spec.Tasks, Predicate: predicate}
			legacyChildBinding = "children-" + predicate + "-" + gates.Digest(config)[7:19]
			selected = append(append([]gates.ChildrenConfig(nil), selected...), config)
		case GoalPRMerged, GoalCLSubmitted:
			resources, changeSubject, legacyMet, err := captureGoalIntegration(ctx, q, *t, g)
			if err != nil {
				return err
			}
			kind := "pr_merged"
			if g.Kind == GoalCLSubmitted {
				kind = "cl_submitted"
			}
			f.Resources["goal-integration"] = resources
			f.LegacyMet["goal-integration"] = legacyMet
			add("goal-integration", "integration", gates.IntegrationConfig{Kind: kind, Semantics: "positive_v1"}, changeSubject, true)
		}
	}
	// At most three read-only explicit predicate selections, plus one legacy
	// tasks-closed mapping, bound both query load and result size.
	if len(selected) > 4 {
		return fmt.Errorf("too many shadow child selections")
	}
	seen := map[string]bool{}
	for _, c := range selected {
		raw, _ := json.Marshal(c)
		if _, err := gates.Validate("children", 1, raw); err != nil {
			return err
		}
		id := "children-" + c.Predicate + "-" + gates.Digest(c)[7:19]
		if seen[id] {
			continue
		}
		seen[id] = true
		add(id, "children", c, subject, id == legacyChildBinding)
		for _, childID := range c.Tasks {
			if len(f.Children) >= 64 {
				if _, ok := f.Children[childID]; !ok {
					return fmt.Errorf("too many selected shadow children")
				}
			}
			if _, ok := f.Children[childID]; ok {
				continue
			}
			child, err := captureChildOutcome(ctx, q, *t, childID, now)
			if err != nil {
				return err
			}
			f.Children[childID] = child
		}
	}
	for _, binding := range f.Bindings {
		if binding.Type != "children" {
			continue
		}
		var config gates.ChildrenConfig
		_ = json.Unmarshal(binding.Config, &config)
		if config.Predicate == "success" {
			continue
		}
		met := true
		for _, id := range config.Tasks {
			child := f.Children[id]
			if !child.Known || config.Predicate == "closed" && !child.Closed || config.Predicate == "merged" && !child.LegacyMerged {
				met = false
			}
		}
		f.LegacyMet[binding.ID] = met
	}
	cycle, relations, err := gateOutcomeCycle(ctx, q, *t, selected)
	if err != nil {
		return err
	}
	f.Cycle = cycle
	f.SourceVersion = gates.Digest(struct {
		Base            string
		Links           []Link
		Policy          string
		Proposer        any
		Filing          any
		Sources         map[string]GateOutcomeSource
		Bindings        []gates.Binding
		Resources       map[string][]gates.ResourceFact
		Children        map[string]gates.ChildFact
		Goal            any
		Relations       any
		Acceptance      any
		Delivered       *int64
		Blocked         bool
		Product         string
		ProductEvidence *gates.Evidence
		NoWorkReceipt   *gates.Evidence
		ProductUnknown  string
		LegacyMet       map[string]bool
		Settled         bool
	}{f.SourceVersion, t.Links, GateOutcomePolicy(*t), t.Proposer, t.Filing, sources, f.Bindings, f.Resources, f.Children, struct {
		Declaration GoalDecl
		At          time.Time
		By          string
	}{g.Decl(), g.DeclaredAt, g.DeclaredBy}, relations, acceptanceRevision, o.DeliveredCommentID, f.Blocked, f.Product, f.ProductEvidence, f.NoWorkReceipt, f.ProductUnknown, f.LegacyMet, f.Settled})
	return nil
}

// gateIntegrationResources captures the complete authoritative task selector:
// current PR (resolved from every link), or every scoped CL. An explicit goal
// resource is an exact single-resource selector, never an agent-narrowed set.
func gateIntegrationResources(t Task, sources map[string]GateOutcomeSource, complete bool, kind, explicit string) ([]gates.ResourceFact, gates.Subject) {
	targets := []string{}
	changeSubject := gates.Subject{Kind: "change", Authority: "unresolved", Repository: "unresolved", ChangeKind: "cl", ID: "task_changes"}
	if t.ChangeTarget != nil {
		changeSubject.Authority = t.ChangeTarget.Kind
		changeSubject.Repository = "workspace/" + gates.Digest(t.ChangeTarget)[7:23]
	}
	if kind == "pr_merged" {
		changeSubject.ChangeKind = "pr"
		if t.Target != nil {
			changeSubject.Authority = t.Target.GitHost()
			changeSubject.Repository = t.Target.String()
		}
		if pr := TaskPullRequest(t); pr != nil {
			targets = append(targets, pr.String())
			changeSubject.Authority = pr.Repo.GitHost()
			changeSubject.Repository = pr.Repo.String()
			changeSubject.ID = strconv.Itoa(pr.Number)
		}
		if len(targets) == 0 {
			if _, ok := sources["equivalent"]; ok {
				targets = []string{"equivalent"}
			}
		}
	} else {
		targets = TaskChangelists(t)
	}
	if explicit != "" {
		targets = []string{explicit}
		changeSubject.ID = explicit
		if kind == "pr_merged" {
			if ref, err := ParsePullRequestRef(explicit); err == nil {
				changeSubject.Authority = ref.Repo.GitHost()
				changeSubject.Repository = ref.Repo.String()
				changeSubject.ID = strconv.Itoa(ref.Number)
			}
		}
	}
	if len(targets) > maxOutcomeResources {
		complete = false
		targets = targets[:maxOutcomeResources]
	}
	resources := make([]gates.ResourceFact, 0, len(targets))
	for _, target := range targets {
		s, ok := sources[target]
		if equivalent, exists := sources["equivalent:"+target]; exists && equivalent.Head == s.Head && equivalent.Policy == GateOutcomePolicy(t) && equivalent.Proof != "" {
			s, ok = equivalent, true
		}
		canonical := s.Product
		if canonical == "" {
			canonical = target
		}
		mappingOK := true
		if kind == "cl_submitted" && canonical != target {
			mappingOK = false
			for _, link := range t.Links {
				if link.Kind == LinkChangelistSubmitted && link.Target == canonical+" (was "+target+")" {
					mappingOK = true
				}
			}
		}
		version := gates.Digest(struct {
			Source GateOutcomeSource
			Links  []Link
			Policy string
		}{s, t.Links, GateOutcomePolicy(t)})
		var head *string
		if s.Head != "" {
			value := s.Head
			head = &value
		}
		resourceSubject := changeSubject
		if kind == "cl_submitted" {
			resourceSubject.ID = canonical
		}
		identity := target
		if canonical != target {
			identity += "=>" + canonical
		}
		evidenceID := identity + " (" + s.Attribution + ")"
		// Evidence IDs are opaque wire references. Keep normal attribution
		// readable, reserving room for the dependency's "change/" prefix.
		// Oversized metadata retains its exact tuple in the identity digest.
		if len([]rune(evidenceID)) > 240 {
			attribution := []rune(s.Attribution)
			if len(attribution) > 100 {
				attribution = append(attribution[:100], '…')
			}
			evidenceID = "resource/" + gates.Digest([]string{target, canonical, s.Attribution}) + " (" + string(attribution) + ")"
		}
		evidence := gates.Evidence{Kind: "integration", ID: evidenceID, Version: version, Subject: resourceSubject, Head: head, Digest: version}
		until := s.At.Add(time.Minute)
		if s.State == "merged" || s.State == "submitted" || s.State == "equivalent" {
			until = s.At.Add(72 * time.Hour)
		}
		resources = append(resources, gates.ResourceFact{Evidence: evidence, State: s.State, Complete: complete && ok && mappingOK && changeSubject.Authority != "unresolved" && changeSubject.Repository != "unresolved" && s.Head != "" && s.Policy == GateOutcomePolicy(t) && (s.State != "equivalent" || s.Proof != "" && s.BaseHead != ""), ObservedAt: s.At, ValidUntil: until})
	}
	return resources, changeSubject
}

func captureGoalIntegration(ctx context.Context, q querier, parent Task, g TaskGoal) ([]gates.ResourceFact, gates.Subject, bool, error) {
	t := &parent
	explicit := ""
	kind := "pr_merged"
	if g.Kind == GoalCLSubmitted {
		kind = "cl_submitted"
		if g.Spec.CL > 0 {
			explicit = fmt.Sprintf("cl/%d", g.Spec.CL)
		}
	}
	targetID := g.Spec.Task
	if targetID == "" && g.Kind == GoalPRMerged {
		explicit = fmt.Sprintf("%s#%d", g.Spec.Repo, g.Spec.Number)
		err := q.QueryRowContext(ctx, "SELECT `task_id` FROM `task_link` WHERE `kind` = ? AND `target` = ? ORDER BY `task_id` LIMIT 1", LinkFixes, explicit).Scan(&targetID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, gates.Subject{}, false, err
		}
	}
	if targetID != "" {
		var err error
		t, err = getTask(ctx, q, targetID)
		if err != nil {
			return nil, gates.Subject{}, false, err
		}
	}
	unresolved := gates.Subject{Kind: "change", Authority: "unresolved", Repository: "unresolved", ChangeKind: "pr", ID: "unresolved"}
	if t == nil || !sameGateReadScope(parent, *t) {
		return nil, unresolved, false, nil
	}
	sources, complete, err := readGateSources(ctx, q, t.ID)
	if err != nil {
		return nil, unresolved, false, err
	}
	resources, subject := gateIntegrationResources(*t, sources, complete, kind, explicit)
	legacyMet := legacyCLsSubmitted(*t, explicit)
	if kind == "pr_merged" {
		o, err := getObservation(ctx, q, t.ID)
		if err != nil {
			return nil, unresolved, false, err
		}
		legacyMet = o != nil && o.PrMergedAt != nil
	}
	return resources, subject, legacyMet, nil
}

func sameGateReadScope(parent, child Task) bool {
	// Repository scopes are the ordinary tasks:read authorization boundary.
	// Workspace-only tasks also require the exact same registered target.
	return gates.Digest(parent.Target) == gates.Digest(child.Target) && parent.ChangeTarget.Equal(child.ChangeTarget)
}

func captureChildOutcome(ctx context.Context, q querier, parent Task, id string, now time.Time) (gates.ChildFact, error) {
	child := gates.ChildFact{}
	t, err := getTask(ctx, q, id)
	if err != nil || t == nil {
		return child, err
	}
	if !sameGateReadScope(parent, *t) {
		return child, nil
	}
	o, err := getObservation(ctx, q, id)
	if err != nil {
		return child, err
	}
	if o == nil {
		o = &Observation{}
	}
	var outcome, runID string
	var runStarted time.Time
	var finished sql.NullTime
	err = q.QueryRowContext(ctx, "SELECT `id`,COALESCE(`outcome`,''),`started_at`,`finished_at` FROM `task_run` WHERE `task_id` = ? ORDER BY `attempt` DESC LIMIT 1", id).Scan(&runID, &outcome, &runStarted, &finished)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return child, err
	}
	var event, comment int64
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(`id`),0) FROM `task_event` WHERE `task_id` = ?", id).Scan(&event); err != nil {
		return child, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(MAX(`id`),0) FROM `task_comment` WHERE `task_id` = ?", id).Scan(&comment); err != nil {
		return child, err
	}
	sources, complete, err := readGateSources(ctx, q, id)
	if err != nil {
		return child, err
	}
	var reopened time.Time
	reopenErr := q.QueryRowContext(ctx, "SELECT `at` FROM `task_event` WHERE `task_id` = ? AND `kind` = 'reopened' ORDER BY `id` DESC LIMIT 1", id).Scan(&reopened)
	if reopenErr != nil && !errors.Is(reopenErr, sql.ErrNoRows) {
		return child, reopenErr
	}
	child.Known = true
	child.Closed = o.ClosedAt != nil
	child.LegacyMerged = o.PrMergedAt != nil
	child.Failed = outcome != "" && outcome != "succeeded" && outcome != NothingNeededOutcome && outcome != ParkedOutcome
	child.SuccessKnown = child.Failed || o.PrClosedAt != nil
	child.MergedKnown = o.PrClosedAt != nil || o.PrMergedAt == nil
	for _, kind := range []string{"pr_merged", "cl_submitted"} {
		resources, _ := gateIntegrationResources(*t, sources, complete, kind, "")
		if len(resources) == 0 {
			continue
		}
		integrated := true
		for _, r := range resources {
			if r.ObservedAt.Before(runStarted) || (!reopened.IsZero() && !r.ObservedAt.After(reopened)) || !r.Complete || !now.Before(r.ValidUntil) || (r.State != "merged" && r.State != "submitted" && r.State != "equivalent") {
				integrated = false
			}
		}
		if !integrated && !reopened.IsZero() {
			child.MergedKnown = false
		}
		if integrated {
			child.Merged = child.Closed
			child.MergedKnown = true
			child.SuccessKnown = true
		}
	}
	// A successful legacy run alone never proves success. Positive integration
	// or a recorded report-only child answer/no-work is required, and a reopened
	// child cannot inherit its prior closed outcome.
	child.Success = child.Closed && child.Merged && !child.Failed
	finish := sources["finish"]
	var reportVersion string
	// Reopening preserves delivery under legacy policy, but closes after that
	// transition need new outcome proof. In particular, a manual cancellation
	// must not reuse a no-work receipt after a reply cleared completion.
	currentFinish := o.CompletedAt != nil && !o.CompletedAt.Before(runStarted) &&
		finish.State == "settled" && !finish.At.Before(runStarted) && !finish.At.After(now) &&
		(reopened.IsZero() || finish.At.After(reopened))
	if child.Closed && !child.Failed && finished.Valid && currentFinish && finish.RunID == runID && finish.Policy == GateOutcomePolicy(*t) && now.Before(finish.At.Add(72*time.Hour)) {
		if outcome == NothingNeededOutcome && finish.Product == "nothing_needed" {
			child.Success = true
			child.SuccessKnown = true
		}
		if outcome == "succeeded" && ClosesOnAnswer(*t) && finish.Product == "report" {
			report, _, _, err := captureReportFacts(ctx, q, id, now, t, o)
			if err != nil {
				return child, err
			}
			reportVersion = report.SourceVersion
			if report.Complete && report.Report != nil && report.Settled {
				child.Success = true
				child.SuccessKnown = true
			}
		}
	}
	if child.Success {
		var goals int
		if err = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM `task_goal` WHERE `task_id` = ?", id).Scan(&goals); err != nil {
			return child, err
		}
		var humanID int64
		var humanAt sql.NullTime
		err = q.QueryRowContext(ctx, "SELECT `id`,`created_at` FROM `task_comment` WHERE `task_id` = ? AND `author_kind` = 'human' ORDER BY `id` DESC LIMIT 1", id).Scan(&humanID, &humanAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return child, err
		}
		unseen := o.DeliveredCommentID != nil && humanID > *o.DeliveredCommentID
		if o.DeliveredCommentID == nil && humanAt.Valid && o.CompletedAt != nil && humanAt.Time.After(*o.CompletedAt) {
			unseen = true
		}
		if goals > 0 || unseen || o.PendingQuestionCommentID != nil {
			child.Success = false
			child.SuccessKnown = false
		}
	}
	version := gates.Digest(struct {
		Links             []Link
		Policy            string
		Closed, Completed *time.Time
		Outcome, Run      string
		Event, Comment    int64
		Filing            any
		Proposer          any
		Sources           map[string]GateOutcomeSource
		Delivered         *int64
		ReportVersion     string
	}{t.Links, GateOutcomePolicy(*t), o.ClosedAt, o.CompletedAt, outcome, runID, event, comment, t.Filing, t.Proposer, sources, o.DeliveredCommentID, reportVersion})
	child.Evidence = gates.Evidence{Kind: "child_outcome", ID: id, Version: version, Subject: gates.Subject{Kind: "task", TaskID: id}, Digest: version}
	return child, nil
}

// Diagnostic DFS follows only explicit outcome-goal edges. Its bounded graph
// revision invalidates parents on relation edits, without changing actual waits.
func gateOutcomeCycle(ctx context.Context, q querier, parent Task, selections []gates.ChildrenConfig) (bool, map[string][]string, error) {
	graph := map[string][]string{}
	for _, s := range selections {
		graph[parent.ID] = append(graph[parent.ID], s.Tasks...)
	}
	active, done := map[string]bool{}, map[string]bool{}
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if active[id] {
			return true, nil
		}
		if done[id] {
			return false, nil
		}
		if len(done)+len(active) > 64 {
			return true, nil
		}
		active[id] = true
		if id != parent.ID {
			t, err := getTask(ctx, q, id)
			if err != nil {
				return false, err
			}
			if t == nil || !sameGateReadScope(parent, *t) {
				delete(active, id)
				done[id] = true
				return false, nil
			}
			g, err := scanTaskGoal(q.QueryRowContext(ctx, "SELECT "+taskGoalColumns+" FROM `task_goal` WHERE `task_id` = ?", id).Scan)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
			if err == nil && g.Kind == GoalTasksClosed {
				graph[id] = g.Spec.Tasks
			}
		}
		for _, next := range graph[id] {
			cycle, err := visit(next)
			if cycle || err != nil {
				return cycle, err
			}
		}
		delete(active, id)
		done[id] = true
		return false, nil
	}
	cycle, err := visit(parent.ID)
	return cycle, graph, err
}

// In-place delivery requires the owning change's current accepted head, beyond
// the review's own claim that it pushed. This is diagnostic controller policy.
func captureInPlaceDelivery(ctx context.Context, q querier, review Task, now time.Time) (*gates.Evidence, any, error) {
	ownerID, head, reviewedRef := "", "", ""
	for _, l := range review.Links {
		switch l.Kind {
		case LinkPushesReviewedBranch:
			ownerID = l.Target
		case LinkReviewerHead:
			head = l.Target
		case LinkReviewsPullRequest:
			reviewedRef = l.Target
		}
	}
	if ownerID == "" || head == "" {
		return nil, nil, nil
	}
	owner, err := getTask(ctx, q, ownerID)
	if err != nil {
		return nil, nil, err
	}
	if owner == nil || !sameGateReadScope(review, *owner) {
		return nil, "owner unavailable", nil
	}
	revision := struct {
		Links  []Link
		Policy string
	}{owner.Links, GateOutcomePolicy(*owner)}
	pr := TaskPullRequest(*owner)
	if pr == nil || pr.String() != reviewedRef {
		return nil, revision, nil
	}
	accepted := false
	for _, l := range owner.Links {
		if l.Kind == LinkTrustBoundaryCleared && strings.HasSuffix(l.Target, "@"+head) {
			accepted = true
		}
	}
	sources, complete, err := readGateSources(ctx, q, ownerID)
	if err != nil {
		return nil, revision, err
	}
	source, ok := sources[pr.String()]
	if !accepted || !complete || !ok || source.Head != head || source.Policy != GateOutcomePolicy(*owner) || !now.Before(source.At.Add(time.Minute)) {
		return nil, revision, nil
	}
	version := gates.Digest(struct {
		Owner  any
		Source GateOutcomeSource
		Review []Link
	}{revision, source, review.Links})
	return &gates.Evidence{Kind: "review", ID: pr.String(), Version: version, Subject: gates.Subject{Kind: "task", TaskID: ownerID}, Head: &head, Digest: version}, revision, nil
}

func oneOfOutcome(s string, values ...string) bool {
	for _, value := range values {
		if s == value {
			return true
		}
	}
	return false
}

func legacyCLsSubmitted(t Task, explicit string) bool {
	targets := TaskChangelists(t)
	if explicit != "" {
		targets = []string{explicit}
	}
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		found := false
		for _, link := range t.Links {
			if link.Kind == LinkChangelistSubmitted && (link.Target == target || strings.HasSuffix(link.Target, " (was "+target+")")) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
