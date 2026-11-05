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
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// DefaultTaskOwner is the deployment fallback owner for tasks filed without a
// verified person or configured schedule/trigger owner (attribution.design.md).
const DefaultTaskOwner = "operator"

// PrincipalUnknown marks a backfilled proposer when nothing in a legacy row
// recorded who or what proposed the task.
const PrincipalUnknown PrincipalKind = "unknown"

// TaskEventOwnerChanged records a transfer of task ownership (old owner, new
// owner, and who made the change).
const TaskEventOwnerChanged TaskEventKind = "owner-changed"

// TransitionOwnerChanged is the timeline step for a recorded ownership transfer.
const TransitionOwnerChanged State = "owner_changed"

// TaskProposer records who or what proposed a task at creation time so the
// timeline's Proposed step renders directly from stored fields rather than
// reconstructing provenance later.
type TaskProposer struct {
	Attribution Attribution
	// TaskID and RunID name the proposing task and run when a task run
	// proposed this task (e.g. via propose_task or the agent API).
	TaskID string
	RunID  string
}

// IsZero reports whether p carries no proposer attribution.
func (p TaskProposer) IsZero() bool {
	return p.Attribution.Actor.Kind == "" &&
		strings.TrimSpace(p.Attribution.Actor.ID) == "" &&
		p.TaskID == "" &&
		p.RunID == ""
}

// NormalizeTaskOwner validates and canonicalizes a task owner account string.
// Accepts plain account identifiers (e.g. "alice@example.com", "operator",
// "serviceAccount:bot@project.iam.gserviceaccount.com") as well as "user:<id>"
// or "human:<id>" shorthands, and rejects empty strings, whitespace/control
// characters, and grain/agent/automation principals.
func NormalizeTaskOwner(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("task owner cannot be empty")
	}
	for _, prefix := range []string{"user:", "human:"} {
		if rest, ok := cutPrefixFold(s, prefix); ok {
			s = strings.TrimSpace(rest)
			break
		}
	}
	if s == "" {
		return "", errors.New("task owner cannot be empty")
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("task owner %q may not contain whitespace or control characters", raw)
		}
	}
	lower := strings.ToLower(s)
	if lower == "grain" || lower == "unknown" {
		return "", fmt.Errorf("task owner %q must be an account, not %s", raw, lower)
	}
	for _, forbidden := range []string{"agent:", "external:", "automation:", "run:", "session:", "schedule:", "task:"} {
		if strings.HasPrefix(lower, forbidden) {
			return "", fmt.Errorf("task owner %q must be an account, not an agent or automation principal", raw)
		}
	}
	return s, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

type ownerChangeContextKey struct{}

// WithOwnerChange attaches the actor changing a task's owner to ctx so putTask
// records a TaskEventOwnerChanged in the same transaction.
func WithOwnerChange(ctx context.Context, by Attribution) context.Context {
	return context.WithValue(ctx, ownerChangeContextKey{}, by)
}

func ownerChangeFrom(ctx context.Context) (Attribution, bool) {
	v, ok := ctx.Value(ownerChangeContextKey{}).(Attribution)
	return v, ok
}

// FormatOwnerChangeDetail formats the old and new owners for TaskEvent.Detail.
func FormatOwnerChangeDetail(oldOwner, newOwner string) string {
	return fmt.Sprintf("owner changed from %s to %s", oldOwner, newOwner)
}

// ParseOwnerChangeReason splits a TaskEventOwnerChanged Reason ("old -> new").
func ParseOwnerChangeReason(reason string) (oldOwner, newOwner string, ok bool) {
	oldOwner, newOwner, ok = strings.Cut(reason, " -> ")
	if !ok {
		return "", "", false
	}
	oldOwner, newOwner = strings.TrimSpace(oldOwner), strings.TrimSpace(newOwner)
	return oldOwner, newOwner, oldOwner != "" && newOwner != ""
}

func deploymentOwnerTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) string {
	var kind, id string
	err := q.QueryRowContext(ctx,
		"SELECT `actor_kind`, `actor_id` FROM `grain_config` WHERE `id` = 1").Scan(&kind, &id)
	if err == nil && (PrincipalKind(kind) == PrincipalHuman || PrincipalKind(kind) == PrincipalService) {
		if norm, nerr := NormalizeTaskOwner(id); nerr == nil {
			return norm
		}
	}
	return DefaultTaskOwner
}

func inheritedOwnerTx(ctx context.Context, tx *sql.Tx, startID string) string {
	seen := map[string]bool{}
	cur := strings.TrimSpace(startID)
	for cur != "" && !seen[cur] && len(seen) < 64 {
		seen[cur] = true
		var owner, creator, oaKind, oaID, obKind, obID, originRef string
		var obKindNull, obIDNull sql.NullString
		err := tx.QueryRowContext(ctx,
			"SELECT `owner`, `creator`, `origin_actor_kind`, `origin_actor_id`, `origin_behalf_kind`, `origin_behalf_id`, `origin_ref` "+
				"FROM `task` WHERE `id` = ?", cur).
			Scan(&owner, &creator, &oaKind, &oaID, &obKindNull, &obIDNull, &originRef)
		if err != nil {
			break
		}
		obKind, obID = obKindNull.String, obIDNull.String
		if norm, nerr := NormalizeTaskOwner(owner); nerr == nil {
			return norm
		}
		if norm, nerr := NormalizeTaskOwner(creator); nerr == nil {
			return norm
		}
		next := nextDerivedFromTx(ctx, tx, cur, originRef)
		if next == "" {
			if norm := ownerFromPrincipalFields(obKind, obID, oaKind, oaID); norm != "" {
				return norm
			}
		}
		cur = next
	}
	return ""
}

func nextDerivedFromTx(ctx context.Context, tx *sql.Tx, taskID, originRef string) string {
	for _, kind := range []LinkKind{LinkProposedBy, LinkPortOf} {
		var target string
		err := tx.QueryRowContext(ctx,
			"SELECT `target` FROM `task_link` WHERE `task_id` = ? AND `kind` = ? LIMIT 1",
			taskID, string(kind)).Scan(&target)
		if err == nil && strings.TrimSpace(target) != "" {
			return strings.TrimSpace(target)
		}
	}
	if orig := WorkOrigin(originRef).Task(); orig != "" && orig != taskID {
		return orig
	}
	return ""
}

func ownerFromPrincipalFields(obKind, obID, oaKind, oaID string) string {
	if PrincipalKind(obKind) == PrincipalHuman || PrincipalKind(obKind) == PrincipalService {
		if norm, err := NormalizeTaskOwner(obID); err == nil {
			return norm
		}
	}
	if PrincipalKind(oaKind) == PrincipalHuman || PrincipalKind(oaKind) == PrincipalService {
		if norm, err := NormalizeTaskOwner(oaID); err == nil {
			return norm
		}
	}
	return ""
}

func defaultOwnerForTaskTx(ctx context.Context, tx *sql.Tx, t Task) string {
	if parentID := t.DerivedFrom(); parentID != "" {
		if inherited := inheritedOwnerTx(ctx, tx, parentID); inherited != "" {
			return inherited
		}
	}
	if norm, err := NormalizeTaskOwner(t.Creator); err == nil {
		return norm
	}
	var obKind, obID string
	if t.Origin.Attribution.OnBehalfOf != nil {
		obKind = string(t.Origin.Attribution.OnBehalfOf.Kind)
		obID = t.Origin.Attribution.OnBehalfOf.ID
	}
	if norm := ownerFromPrincipalFields(obKind, obID, string(t.Origin.Attribution.Actor.Kind), t.Origin.Attribution.Actor.ID); norm != "" {
		return norm
	}
	return deploymentOwnerTx(ctx, tx)
}

// AutomationProposerID names the automation actor for an automation-filed task.
func AutomationProposerID(origin Origin, workOrigin WorkOrigin, tags []string) string {
	actorID := strings.TrimSpace(origin.Attribution.Actor.ID)
	if actorID != "" && actorID != "grain" {
		return actorID
	}
	switch origin.Reason {
	case ReasonSchedule:
		return "schedule"
	case ReasonReview:
		return "review"
	case ReasonFix:
		return "base-repair"
	case ReasonQualification:
		return "qualification"
	case ReasonSuite:
		return "suite"
	case ReasonUpgradeCheck:
		return "upgrade-check"
	case ReasonConsoleSession:
		return "console-session"
	case ReasonImported:
		return "issue-import"
	}
	reason := strings.TrimSpace(string(origin.Reason))
	switch reason {
	case "port":
		return "port"
	case "trigger", "path-trigger", "github-event":
		return "trigger"
	case "auto-submit":
		return "auto-submit"
	case "takeover":
		return "takeover"
	}
	for _, tag := range tags {
		switch {
		case strings.HasPrefix(tag, "schedule:"):
			return "schedule"
		case strings.HasPrefix(tag, "base-ci-repair:"):
			return "base-repair"
		case strings.HasPrefix(tag, "path-trigger:"):
			return "trigger"
		}
	}
	if reason != "" && origin.Reason != ReasonDirect && origin.Reason != ReasonProposal && reason != "proposed" {
		return reason
	}
	if strings.HasPrefix(string(workOrigin), "github:issue:") {
		return "issue-import"
	}
	if strings.HasPrefix(string(workOrigin), "github:pr:") {
		return "trigger"
	}
	return "grain"
}

// InferTaskProposer computes a TaskProposer from a Task's fields at creation
// or migration backfill time. Returns PrincipalUnknown only when nothing in t
// records who or what proposed it.
func InferTaskProposer(t Task) TaskProposer {
	var proposedByTask string
	for _, l := range t.Links {
		if l.Kind == LinkProposedBy && strings.TrimSpace(l.Target) != "" {
			proposedByTask = strings.TrimSpace(l.Target)
			break
		}
	}
	actor := t.Origin.Attribution.Actor
	behalf := t.Origin.Attribution.OnBehalfOf
	reason := strings.TrimSpace(string(t.Origin.Reason))

	// Run-proposed task: LinkProposedBy with ReasonProposal or an agent actor/on-behalf-of
	// (distinct from automation reviews/triggers that also attach LinkProposedBy).
	isRunProposal := proposedByTask != "" && (t.Origin.Reason == ReasonProposal || reason == "proposed" ||
		(actor.Kind == PrincipalAgent && actor.Source != SourceExternalToken) ||
		(behalf != nil && behalf.Kind == PrincipalAgent))
	if !isRunProposal && proposedByTask == "" && actor.Kind == PrincipalAgent && actor.Source == SourceSandbox && strings.TrimSpace(actor.ID) != "" {
		proposedByTask = strings.TrimSpace(actor.ID)
		isRunProposal = true
	}
	if isRunProposal {
		src := actor.Source
		if src == "" || src == SourceInternal {
			src = SourceSandbox
		}
		var ownerBehalf *Principal
		if owner := strings.TrimSpace(t.Owner); owner != "" {
			ownerBehalf = &Principal{Kind: PrincipalHuman, ID: owner}
			if behalf != nil && (behalf.Kind == PrincipalHuman || behalf.Kind == PrincipalService) && strings.EqualFold(behalf.ID, owner) {
				cp := *behalf
				ownerBehalf = &cp
			}
		}
		return TaskProposer{
			Attribution: Attribution{
				Actor:      Principal{Kind: PrincipalAgent, ID: proposedByTask, Source: src},
				OnBehalfOf: ownerBehalf,
			},
			TaskID: proposedByTask,
		}
	}

	if actor.Kind == PrincipalHuman || actor.Kind == PrincipalService {
		if strings.TrimSpace(actor.ID) != "" {
			var bCopy *Principal
			if behalf != nil {
				cp := *behalf
				bCopy = &cp
			}
			return TaskProposer{
				Attribution: Attribution{Actor: actor, OnBehalfOf: bCopy},
			}
		}
	}

	if actor.Kind == PrincipalAgent && strings.TrimSpace(actor.ID) != "" {
		var bCopy *Principal
		if behalf != nil {
			cp := *behalf
			bCopy = &cp
		} else if owner := strings.TrimSpace(t.Owner); owner != "" {
			bCopy = &Principal{Kind: PrincipalHuman, ID: owner}
		}
		return TaskProposer{
			Attribution: Attribution{Actor: actor, OnBehalfOf: bCopy},
		}
	}

	if actor.Kind == PrincipalAutomation || (reason != "" && t.Origin.Reason != ReasonDirect) {
		autoID := AutomationProposerID(t.Origin, t.WorkOrigin, t.Tags)
		src := actor.Source
		if src == "" {
			src = SourceInternal
		}
		return TaskProposer{
			Attribution: Attribution{
				Actor: Principal{Kind: PrincipalAutomation, ID: autoID, Source: src},
			},
		}
	}

	if creator := strings.TrimSpace(t.Creator); creator != "" {
		return TaskProposer{
			Attribution: Attribution{
				Actor: Principal{Kind: PrincipalHuman, ID: creator},
			},
		}
	}

	if proposedByTask != "" {
		var ownerBehalf *Principal
		if owner := strings.TrimSpace(t.Owner); owner != "" {
			ownerBehalf = &Principal{Kind: PrincipalHuman, ID: owner}
		}
		return TaskProposer{
			Attribution: Attribution{
				Actor:      Principal{Kind: PrincipalAgent, ID: proposedByTask, Source: SourceSandbox},
				OnBehalfOf: ownerBehalf,
			},
			TaskID: proposedByTask,
		}
	}

	if origTask := t.WorkOrigin.Task(); origTask != "" && origTask != t.ID {
		var ownerBehalf *Principal
		if owner := strings.TrimSpace(t.Owner); owner != "" {
			ownerBehalf = &Principal{Kind: PrincipalHuman, ID: owner}
		}
		return TaskProposer{
			Attribution: Attribution{
				Actor:      Principal{Kind: PrincipalAgent, ID: origTask, Source: SourceSandbox},
				OnBehalfOf: ownerBehalf,
			},
			TaskID: origTask,
		}
	}

	if strings.HasPrefix(string(t.WorkOrigin), "github:") {
		return TaskProposer{
			Attribution: Attribution{
				Actor: Principal{Kind: PrincipalAutomation, ID: AutomationProposerID(t.Origin, t.WorkOrigin, t.Tags), Source: SourceInternal},
			},
		}
	}

	return TaskProposer{
		Attribution: Attribution{
			Actor: Principal{Kind: PrincipalUnknown, ID: "unknown"},
		},
	}
}

func latestRunForProposerTx(ctx context.Context, tx *sql.Tx, proposingTaskID string, before *time.Time) string {
	if proposingTaskID == "" {
		return ""
	}
	var runID string
	if before != nil {
		err := tx.QueryRowContext(ctx,
			"SELECT `id` FROM `task_run` WHERE `task_id` = ? AND `started_at` <= ? ORDER BY `started_at` DESC, `attempt` DESC LIMIT 1",
			proposingTaskID, before.UTC()).Scan(&runID)
		if err == nil && runID != "" {
			return runID
		}
	}
	err := tx.QueryRowContext(ctx,
		"SELECT `id` FROM `task_run` WHERE `task_id` = ? ORDER BY `started_at` DESC, `attempt` DESC LIMIT 1",
		proposingTaskID).Scan(&runID)
	if err == nil {
		return runID
	}
	return ""
}

// guardTaskOwnerAndProposer ensures t.Owner is populated and valid, preserves
// the original proposer across updates, and returns an audit TaskEvent when an
// existing task's owner changes.
func guardTaskOwnerAndProposer(ctx context.Context, tx *sql.Tx, existing int, t *Task) (*TaskEvent, error) {
	var prevOwner string
	var prevProposer TaskProposer
	if existing > 0 {
		var paKind, paID, paSource, pbSource, pTaskID, pRunID string
		var pbKind, pbID sql.NullString
		err := tx.QueryRowContext(ctx,
			"SELECT `owner`, `proposer_actor_kind`, `proposer_actor_id`, `proposer_actor_source`, "+
				"`proposer_behalf_kind`, `proposer_behalf_id`, `proposer_behalf_source`, "+
				"`proposer_task_id`, `proposer_run_id` FROM `task` WHERE `id` = ?", t.ID).
			Scan(&prevOwner, &paKind, &paID, &paSource, &pbKind, &pbID, &pbSource, &pTaskID, &pRunID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		prevProposer = TaskProposer{
			Attribution: Attribution{
				Actor:      Principal{Kind: PrincipalKind(paKind), ID: paID, Source: AuthSource(paSource)},
				OnBehalfOf: principalFrom(pbKind, pbID, pbSource),
			},
			TaskID: pTaskID,
			RunID:  pRunID,
		}
	}

	if t.Owner == "" {
		if existing > 0 && strings.TrimSpace(prevOwner) != "" {
			t.Owner = strings.TrimSpace(prevOwner)
		} else {
			t.Owner = defaultOwnerForTaskTx(ctx, tx, *t)
		}
	} else {
		norm, err := NormalizeTaskOwner(t.Owner)
		if err != nil {
			return nil, err
		}
		t.Owner = norm
	}
	if strings.TrimSpace(t.Owner) == "" {
		return nil, errors.New("task owner cannot be empty")
	}

	if existing > 0 && t.Proposer.IsZero() && !prevProposer.IsZero() {
		t.Proposer = prevProposer
	}
	if t.Proposer.IsZero() {
		t.Proposer = InferTaskProposer(*t)
	}
	if t.Proposer.TaskID != "" {
		if t.Proposer.RunID == "" && existing == 0 {
			t.Proposer.RunID = latestRunForProposerTx(ctx, tx, t.Proposer.TaskID, t.CreatedAt)
		}
		if t.Proposer.Attribution.OnBehalfOf == nil && t.Owner != "" {
			t.Proposer.Attribution.OnBehalfOf = &Principal{Kind: PrincipalHuman, ID: t.Owner}
		}
	}

	if existing > 0 && prevOwner != "" && prevOwner != t.Owner {
		who, ok := ownerChangeFrom(ctx)
		if !ok {
			depOwner := deploymentOwnerTx(ctx, tx)
			who = Attribution{Actor: Principal{Kind: PrincipalHuman, ID: depOwner, Source: SourceInProcess}}
		}
		now := time.Now().UTC()
		return &TaskEvent{
			TaskID:     t.ID,
			Kind:       TaskEventOwnerChanged,
			At:         now,
			By:         who.Actor,
			OnBehalfOf: who.OnBehalfOf,
			Reason:     prevOwner + " -> " + t.Owner,
			Detail:     FormatOwnerChangeDetail(prevOwner, t.Owner),
		}, nil
	}
	return nil, nil
}

// MigrateTaskOwnerAndProposer is schema migration 113's data step: it backfills
// task.owner and task.proposer_* for every existing row and then enforces that
// no ownerless task row can be stored.
func MigrateTaskOwnerAndProposer(ctx context.Context, tx *sql.Tx) error {
	depOwner := deploymentOwnerTx(ctx, tx)

	type rawTask struct {
		id         string
		creator    string
		oaKind     string
		oaID       string
		oaSource   string
		obKind     sql.NullString
		obID       sql.NullString
		obSource   string
		oReason    string
		originRef  string
		createdAt  sql.NullTime
		owner      string
		propKind   string
		propID     string
		propTaskID string
	}

	rows, err := tx.QueryContext(ctx,
		"SELECT `id`, `creator`, `origin_actor_kind`, `origin_actor_id`, `origin_actor_source`, "+
			"`origin_behalf_kind`, `origin_behalf_id`, `origin_behalf_source`, `origin_reason`, `origin_ref`, `created_at`, "+
			"`owner`, `proposer_actor_kind`, `proposer_actor_id`, `proposer_task_id` FROM `task`")
	if err != nil {
		return fmt.Errorf("reading tasks for owner backfill: %w", err)
	}
	byID := map[string]*rawTask{}
	var order []string
	for rows.Next() {
		var r rawTask
		if err := rows.Scan(
			&r.id, &r.creator, &r.oaKind, &r.oaID, &r.oaSource,
			&r.obKind, &r.obID, &r.obSource, &r.oReason, &r.originRef, &r.createdAt,
			&r.owner, &r.propKind, &r.propID, &r.propTaskID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		byID[r.id] = &r
		order = append(order, r.id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	proposedBy := map[string]string{}
	portOf := map[string]string{}
	linkRows, err := tx.QueryContext(ctx,
		"SELECT `task_id`, `kind`, `target` FROM `task_link` WHERE `kind` IN ('proposed-by', 'port-of')")
	if err != nil {
		return fmt.Errorf("reading task_link for owner backfill: %w", err)
	}
	for linkRows.Next() {
		var taskID, kind, target string
		if err := linkRows.Scan(&taskID, &kind, &target); err != nil {
			_ = linkRows.Close()
			return err
		}
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		switch LinkKind(kind) {
		case LinkProposedBy:
			if proposedBy[taskID] == "" {
				proposedBy[taskID] = target
			}
		case LinkPortOf:
			if portOf[taskID] == "" {
				portOf[taskID] = target
			}
		}
	}
	if err := linkRows.Close(); err != nil {
		return err
	}
	if err := linkRows.Err(); err != nil {
		return err
	}

	tagsByTask := map[string][]string{}
	tagRows, err := tx.QueryContext(ctx, "SELECT `task_id`, `tag` FROM `task_tag`")
	if err != nil {
		return fmt.Errorf("reading task_tag for proposer backfill: %w", err)
	}
	for tagRows.Next() {
		var taskID, tag string
		if err := tagRows.Scan(&taskID, &tag); err != nil {
			_ = tagRows.Close()
			return err
		}
		tagsByTask[taskID] = append(tagsByTask[taskID], tag)
	}
	if err := tagRows.Close(); err != nil {
		return err
	}
	if err := tagRows.Err(); err != nil {
		return err
	}

	derivedParent := func(id string) string {
		if p := proposedBy[id]; p != "" {
			return p
		}
		if p := portOf[id]; p != "" {
			return p
		}
		if r := byID[id]; r != nil {
			if orig := WorkOrigin(r.originRef).Task(); orig != "" && orig != id {
				return orig
			}
		}
		return ""
	}

	resolvedOwner := map[string]string{}
	var resolveOwner func(id string, visiting map[string]bool) string
	resolveOwner = func(id string, visiting map[string]bool) string {
		if v := resolvedOwner[id]; v != "" {
			return v
		}
		r := byID[id]
		if r == nil {
			return depOwner
		}
		if norm, nerr := NormalizeTaskOwner(r.owner); nerr == nil {
			resolvedOwner[id] = norm
			return norm
		}
		if norm, nerr := NormalizeTaskOwner(r.creator); nerr == nil {
			resolvedOwner[id] = norm
			return norm
		}
		if visiting[id] {
			return depOwner
		}
		visiting[id] = true
		if parent := derivedParent(id); parent != "" && byID[parent] != nil {
			own := resolveOwner(parent, visiting)
			resolvedOwner[id] = own
			return own
		}
		if norm := ownerFromPrincipalFields(r.obKind.String, r.obID.String, r.oaKind, r.oaID); norm != "" {
			resolvedOwner[id] = norm
			return norm
		}
		resolvedOwner[id] = depOwner
		return depOwner
	}

	for _, id := range order {
		r := byID[id]
		own := resolveOwner(id, map[string]bool{})

		var prop TaskProposer
		if r.propKind != "" || r.propID != "" || r.propTaskID != "" {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task` SET `owner` = ? WHERE `id` = ?", own, id); err != nil {
				return fmt.Errorf("updating backfilled owner for task %s: %w", id, err)
			}
			continue
		}

		var links []Link
		if p := proposedBy[id]; p != "" {
			links = append(links, Link{Kind: LinkProposedBy, Target: p})
		}
		if p := portOf[id]; p != "" {
			links = append(links, Link{Kind: LinkPortOf, Target: p})
		}
		synth := Task{
			ID:      r.id,
			Owner:   own,
			Creator: r.creator,
			Origin: Origin{
				Attribution: Attribution{
					Actor:      Principal{Kind: PrincipalKind(r.oaKind), ID: r.oaID, Source: AuthSource(r.oaSource)},
					OnBehalfOf: principalFrom(r.obKind, r.obID, r.obSource),
				},
				Reason: OriginReason(r.oReason),
			},
			WorkOrigin: WorkOrigin(r.originRef),
			Links:      links,
			Tags:       tagsByTask[id],
		}
		if r.createdAt.Valid {
			t := r.createdAt.Time
			synth.CreatedAt = &t
		}
		prop = InferTaskProposer(synth)
		if prop.TaskID != "" && prop.RunID == "" {
			prop.RunID = latestRunForProposerTx(ctx, tx, prop.TaskID, synth.CreatedAt)
		}

		var pbKind, pbID, pbSource string
		if prop.Attribution.OnBehalfOf != nil {
			pbKind = string(prop.Attribution.OnBehalfOf.Kind)
			pbID = prop.Attribution.OnBehalfOf.ID
			pbSource = string(prop.Attribution.OnBehalfOf.Source)
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task` SET `owner` = ?, "+
				"`proposer_actor_kind` = ?, `proposer_actor_id` = ?, `proposer_actor_source` = ?, "+
				"`proposer_behalf_kind` = ?, `proposer_behalf_id` = ?, `proposer_behalf_source` = ?, "+
				"`proposer_task_id` = ?, `proposer_run_id` = ? "+
				"WHERE `id` = ?",
			own,
			string(prop.Attribution.Actor.Kind), prop.Attribution.Actor.ID, string(prop.Attribution.Actor.Source),
			pbKind, pbID, pbSource,
			prop.TaskID, prop.RunID,
			id,
		); err != nil {
			return fmt.Errorf("backfilling owner and proposer for task %s: %w", id, err)
		}
	}

	return enforceNonEmptyTaskOwnerConstraint(ctx, tx)
}

func enforceNonEmptyTaskOwnerConstraint(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx,
		"SELECT `sql` FROM `sqlite_master` WHERE `type` = 'table' AND `name` = 'task'").Scan(&ddl); err != nil {
		return fmt.Errorf("reading task table definition: %w", err)
	}
	if ddl == "" {
		// Postgres: drop any empty default left by ALTER TABLE ADD COLUMN and
		// add a table CHECK constraint under a savepoint so re-running data
		// migrations is idempotent.
		if _, err := tx.ExecContext(ctx, "ALTER TABLE `task` ALTER COLUMN `owner` DROP DEFAULT"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "SAVEPOINT grain_task_owner_check"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE `task` ADD CONSTRAINT `task_owner_nonempty` CHECK (`owner` <> '')"); err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "already exists") {
				return err
			}
			if _, rerr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT grain_task_owner_check"); rerr != nil {
				return rerr
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT grain_task_owner_check")
		return err
	}
	// SQLite: fresh and migrated tables enforce the invariant via BEFORE
	// INSERT / UPDATE triggers.
	for _, stmt := range []string{
		"CREATE TRIGGER IF NOT EXISTS `task_owner_nonempty_insert` BEFORE INSERT ON `task` FOR EACH ROW WHEN NEW.`owner` = '' BEGIN SELECT RAISE(ABORT, 'task.owner must not be empty'); END",
		"CREATE TRIGGER IF NOT EXISTS `task_owner_nonempty_update` BEFORE UPDATE OF `owner` ON `task` FOR EACH ROW WHEN NEW.`owner` = '' BEGIN SELECT RAISE(ABORT, 'task.owner must not be empty'); END",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// SuspendTaskOwnerConstraintForImport temporarily relaxes the non-empty
// task.owner constraint inside tx while staterepo imports a pre-113 dump whose
// task rows omit the owner column; ApplyDataMigrations restores it via
// MigrateTaskOwnerAndProposer before tx commits.
func SuspendTaskOwnerConstraintForImport(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx,
		"SELECT `sql` FROM `sqlite_master` WHERE `type` = 'table' AND `name` = 'task'").Scan(&ddl); err != nil {
		return fmt.Errorf("reading task table definition: %w", err)
	}
	if ddl == "" {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE `task` DROP CONSTRAINT IF EXISTS `task_owner_nonempty`"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "ALTER TABLE `task` ALTER COLUMN `owner` SET DEFAULT ''")
		return err
	}
	for _, stmt := range []string{
		"DROP TRIGGER IF EXISTS `task_owner_nonempty_insert`",
		"DROP TRIGGER IF EXISTS `task_owner_nonempty_update`",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
