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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// IntegrationEventType names one kind of event emitted on GET /api/events
// (design/design/integration-events.design.md §3.2).
type IntegrationEventType string

const (
	IntegrationEventTaskState    IntegrationEventType = "task.state"
	IntegrationEventTaskQuestion IntegrationEventType = "task.question"
	IntegrationEventTaskComment  IntegrationEventType = "task.comment"
	IntegrationEventTaskCI       IntegrationEventType = "task.ci"

	// The notification events: a notification raised (new, or raised
	// again at a higher severity, which re-surfaces it), acknowledged, or
	// cleared. TaskID and Repo are the notification's own, when it has
	// them, and readers are filtered by Repo as for task events.
	IntegrationEventNotificationRaised  IntegrationEventType = "notification.raised"
	IntegrationEventNotificationAcked   IntegrationEventType = "notification.acked"
	IntegrationEventNotificationCleared IntegrationEventType = "notification.cleared"

	// The host-script approval events: a host-script call waiting for a
	// person, and that call answered (approved, denied, or expired).
	IntegrationEventLocalScriptPending  IntegrationEventType = "local_script.pending"
	IntegrationEventLocalScriptAnswered IntegrationEventType = "local_script.answered"
)

// ValidIntegrationEventTypes lists every event type GET /api/events serves.
var ValidIntegrationEventTypes = []IntegrationEventType{
	IntegrationEventTaskState,
	IntegrationEventTaskQuestion,
	IntegrationEventTaskComment,
	IntegrationEventTaskCI,
	IntegrationEventNotificationRaised,
	IntegrationEventNotificationAcked,
	IntegrationEventNotificationCleared,
	IntegrationEventLocalScriptPending,
	IntegrationEventLocalScriptAnswered,
}

// Valid reports whether t is a recognized integration event type.
func (t IntegrationEventType) Valid() bool {
	return slices.Contains(ValidIntegrationEventTypes, t)
}

// MaxRetainedIntegrationEvents bounds how many rows integration_event keeps
// locally so a busy deployment's event table cannot grow without bound while
// still giving disconnected integrations a generous replay window.
const MaxRetainedIntegrationEvents = 10000

// IntegrationEvent is one ordered, resumable task lifecycle, comment/question,
// or CI verdict record stored in integration_event. It carries identifiers,
// one-line summaries, external references, and actor attribution only -- never
// secret values, raw comment bodies, prompts, transcripts, or CI logs.
type IntegrationEvent struct {
	ID              int64
	Type            IntegrationEventType
	At              time.Time
	TaskID          string
	Repo            string
	PreviousState   State
	State           State
	QuestionSummary string
	CommentID       int64
	ExternalRef     string
	Origin          string
	PullRequest     string
	FiledVia        string
	CIProvider      string
	CIRunID         string
	CICommit        string
	CIConclusion    string
	CIDetail        string
	// NotificationID, Subject and Severity identify a notification.*
	// event's notification; Summary is its one-line title, or a
	// local_script.* event's script call.
	NotificationID int64
	Subject        string
	Severity       string
	Summary        string
	// ApprovalID and Script identify a local_script.* event's call, and
	// Decision is how a local_script.answered one was answered.
	ApprovalID string
	Script     string
	Decision   string
	Actor      Attribution
}

// IntegrationEventFilter narrows a read of integration_event.
type IntegrationEventFilter struct {
	AfterID int64
	Repo    string
	Types   []IntegrationEventType
	Limit   int
}

type eventActorContextKey struct{}

// WithEventActor attaches the caller attribution that initiated a Store write
// so state transitions recorded inside the transaction carry who asked for
// them when the Store method itself takes no Attribution parameter.
func WithEventActor(ctx context.Context, a Attribution) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if a.Actor.Kind == "" && a.Actor.ID == "" {
		return ctx
	}
	return context.WithValue(ctx, eventActorContextKey{}, a)
}

// EventActorFromContext returns the caller attribution WithEventActor recorded.
func EventActorFromContext(ctx context.Context) (Attribution, bool) {
	if ctx == nil {
		return Attribution{}, false
	}
	a, ok := ctx.Value(eventActorContextKey{}).(Attribution)
	if !ok || (a.Actor.Kind == "" && a.Actor.ID == "") {
		return Attribution{}, false
	}
	return a, true
}

func eventActorOr(ctx context.Context, fallback Attribution) Attribution {
	if fallback.Actor.Kind != "" || fallback.Actor.ID != "" {
		return fallback
	}
	if a, ok := EventActorFromContext(ctx); ok {
		return a
	}
	return Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}}
}

func eventActorOrGrain(ctx context.Context) Attribution {
	if a, ok := EventActorFromContext(ctx); ok {
		return a
	}
	return Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}}
}

type taskEventMeta struct {
	repo        string
	state       State
	externalRef string
	origin      string
	pullRequest string
	filedVia    string
}

func taskStateTx(ctx context.Context, q querier, taskID string) (State, bool, error) {
	var raw string
	err := q.QueryRowContext(ctx,
		"SELECT `state` FROM `task_state` WHERE `task_id` = ?", taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return State(raw), true, nil
}

func taskEventMetaTx(ctx context.Context, q querier, taskID string) (taskEventMeta, bool, error) {
	var targetOwner, targetName sql.NullString
	var issue int
	var originRef, filedVia, taskExternalRef, stateText string
	err := q.QueryRowContext(ctx,
		"SELECT `t`.`target_owner`, `t`.`target_name`, `t`.`issue`, `t`.`origin_ref`, `t`.`filed_via`, `t`.`external_ref`, `s`.`state` "+
			"FROM `task` AS `t` JOIN `task_state` AS `s` ON `s`.`task_id` = `t`.`id` "+
			"WHERE `t`.`id` = ?",
		taskID,
	).Scan(&targetOwner, &targetName, &issue, &originRef, &filedVia, &taskExternalRef, &stateText)
	if errors.Is(err, sql.ErrNoRows) {
		return taskEventMeta{}, false, nil
	}
	if err != nil {
		return taskEventMeta{}, false, err
	}

	var meta taskEventMeta
	var targetRepo *RepoRef
	if targetOwner.Valid && targetName.Valid && targetOwner.String != "" && targetName.String != "" {
		r := RepoFromStored(targetOwner.String, targetName.String)
		targetRepo = &r
		meta.repo = r.String()
	}
	meta.state = State(stateText)
	meta.origin = strings.TrimSpace(originRef)
	meta.filedVia = strings.TrimSpace(filedVia)

	rows, err := q.QueryContext(ctx,
		"SELECT `kind`, `target` FROM `task_link` WHERE `task_id` = ? ORDER BY `kind`, `target`", taskID)
	if err != nil {
		return taskEventMeta{}, false, err
	}
	var chatRef, fixesPR, anyFixesPR, changelistRef, externalPR, derivedFrom string
	for rows.Next() {
		var kindText, target string
		if err := rows.Scan(&kindText, &target); err != nil {
			rows.Close()
			return taskEventMeta{}, false, err
		}
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		switch LinkKind(kindText) {
		case LinkGoogleChat:
			if chatRef == "" {
				chatRef = target
			}
		case LinkFixes:
			if anyFixesPR == "" {
				anyFixesPR = target
			}
			if fixesPR == "" && (targetRepo == nil || !LinkOffTarget(Task{Target: targetRepo}, Link{Kind: LinkFixes, Target: target})) {
				fixesPR = target
			}
		case LinkChangelist:
			if changelistRef == "" {
				changelistRef = target
			}
		case LinkExternalPR:
			if externalPR == "" {
				externalPR = target
			}
		case LinkDerivedFrom:
			if derivedFrom == "" {
				derivedFrom = target
			}
		}
	}
	if err := rows.Close(); err != nil {
		return taskEventMeta{}, false, err
	}

	switch {
	case fixesPR != "":
		meta.pullRequest = fixesPR
	case anyFixesPR != "":
		meta.pullRequest = anyFixesPR
	case changelistRef != "":
		meta.pullRequest = changelistRef
	}

	var customExternalRef string
	cfRows, err := q.QueryContext(ctx,
		"SELECT `name`, `value` FROM `task_custom_field` WHERE `task_id` = ? ORDER BY `name`, `value`", taskID)
	if err != nil {
		return taskEventMeta{}, false, err
	}
	for cfRows.Next() {
		var name, val string
		if err := cfRows.Scan(&name, &val); err != nil {
			cfRows.Close()
			return taskEventMeta{}, false, err
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "external_ref", "externalref", "external-ref":
			if customExternalRef == "" && strings.TrimSpace(val) != "" {
				customExternalRef = strings.TrimSpace(val)
			}
		}
	}
	if err := cfRows.Close(); err != nil {
		return taskEventMeta{}, false, err
	}

	var issueRef string
	if issue > 0 {
		if meta.repo != "" {
			issueRef = fmt.Sprintf("%s#%d", meta.repo, issue)
		} else {
			issueRef = fmt.Sprintf("#%d", issue)
		}
	}
	var externalOrigin string
	if meta.origin != "" && !strings.HasPrefix(meta.origin, "task:") {
		externalOrigin = meta.origin
	}

	for _, candidate := range []string{
		strings.TrimSpace(taskExternalRef),
		chatRef,
		customExternalRef,
		issueRef,
		externalPR,
		externalOrigin,
		derivedFrom,
	} {
		if candidate != "" {
			meta.externalRef = candidate
			break
		}
	}
	return meta, true, nil
}

func awaitingReplySummaryTx(ctx context.Context, q querier, taskID string) (string, int64) {
	var pending sql.NullInt64
	var pendingSecret sql.NullString
	err := q.QueryRowContext(ctx,
		"SELECT `pending_question_comment_id`, `pending_secret` FROM `task_observation` WHERE `task_id` = ?",
		taskID,
	).Scan(&pending, &pendingSecret)
	if err != nil {
		return "", 0
	}
	var commentID int64
	if pending.Valid && pending.Int64 > 0 {
		commentID = pending.Int64
		var summary, body string
		if err := q.QueryRowContext(ctx,
			"SELECT `summary`, `body` FROM `task_comment` WHERE `id` = ?", commentID,
		).Scan(&summary, &body); err == nil && body != TurnStoppedComment {
			if s := strings.TrimSpace(summary); s != "" {
				return clampSingleLine(s, MaxQuestionSummaryLen), commentID
			}
			if derived := DeriveQuestionSummary(body); derived != "" {
				return derived, commentID
			}
		}
	}
	if sec := strings.TrimSpace(pendingSecret.String); sec != "" {
		return clampSingleLine("set "+sec+" in the encrypted secret input", MaxQuestionSummaryLen), commentID
	}
	// Parked on nothing: a task resting on a closing note is announced by
	// the summary its run gave that note (Store.NoteSummary), and never by
	// a line derived from the note's body.
	if commentID == 0 {
		var summary string
		if err := q.QueryRowContext(ctx,
			"SELECT `c`.`summary` FROM `task_comment` `c` WHERE `c`.`task_id` = ? AND "+noteSummaryWhere, taskID,
		).Scan(&summary); err == nil {
			return clampSingleLine(summary, MaxQuestionSummaryLen), 0
		}
	}
	return "", commentID
}

func clampSingleLine(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func nonZeroUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func insertIntegrationEventTx(ctx context.Context, tx *sql.Tx, e IntegrationEvent) (int64, error) {
	at := nonZeroUTC(e.At)
	actor := e.Actor.Actor
	behalf := e.Actor.OnBehalfOf
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `integration_event` ("+
			"`type`,`at`,`task_id`,`repo`,`previous_state`,`state`,`question_summary`,`comment_id`,"+
			"`external_ref`,`origin`,`pull_request`,`filed_via`,"+
			"`ci_provider`,`ci_run_id`,`ci_commit`,`ci_conclusion`,`ci_detail`,"+
			"`actor_kind`,`actor_id`,`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source`,"+
			"`notification_id`,`subject`,`severity`,`summary`,`approval_id`,`script`,`decision`"+
			") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		string(e.Type), at, e.TaskID, e.Repo, string(e.PreviousState), string(e.State),
		e.QuestionSummary, e.CommentID,
		e.ExternalRef, e.Origin, e.PullRequest, e.FiledVia,
		e.CIProvider, e.CIRunID, e.CICommit, e.CIConclusion, e.CIDetail,
		string(actor.Kind), actor.ID, string(actor.Source),
		behalfColumn(behalf, func(p Principal) string { return string(p.Kind) }),
		behalfColumn(behalf, func(p Principal) string { return p.ID }),
		sourceOf(behalf),
		e.NotificationID, e.Subject, e.Severity, clampSingleLine(e.Summary, MaxQuestionSummaryLen),
		e.ApprovalID, e.Script, e.Decision,
	)
	if err != nil {
		return 0, fmt.Errorf("recording integration event %s on task %s: %w", e.Type, e.TaskID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("reading integration event id on task %s: %w", e.TaskID, err)
	}
	if cutoff := id - MaxRetainedIntegrationEvents; cutoff > 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM `integration_event` WHERE `id` <= ?", cutoff); err != nil {
			return 0, fmt.Errorf("pruning integration events through %d: %w", cutoff, err)
		}
	}
	return id, nil
}

func recordTaskStateChangeTx(ctx context.Context, tx *sql.Tx, taskID string, prevState State, at time.Time, actor Attribution) error {
	meta, ok, err := taskEventMetaTx(ctx, tx, taskID)
	if err != nil || !ok || meta.state == prevState {
		return err
	}
	ev := IntegrationEvent{
		Type:          IntegrationEventTaskState,
		At:            at,
		TaskID:        taskID,
		Repo:          meta.repo,
		PreviousState: prevState,
		State:         meta.state,
		ExternalRef:   meta.externalRef,
		Origin:        meta.origin,
		PullRequest:   meta.pullRequest,
		FiledVia:      meta.filedVia,
		Actor:         actor,
	}
	if meta.state == StateAwaitingReply {
		ev.QuestionSummary, ev.CommentID = awaitingReplySummaryTx(ctx, tx, taskID)
	}
	_, err = insertIntegrationEventTx(ctx, tx, ev)
	return err
}

func recordCommentEventTx(ctx context.Context, tx *sql.Tx, c Comment, commentID int64) error {
	if c.Handover || c.Body == TurnStoppedComment {
		return nil
	}
	meta, ok, err := taskEventMetaTx(ctx, tx, c.TaskID)
	if err != nil || !ok {
		return err
	}
	// A summary marks a question -- unless the comment is a run's
	// comment_on_issue note, whose summary names a report nobody was
	// asked to answer (Comment.Note): that is an ordinary task.comment.
	// The note's summary still announces an awaiting_reply task resting
	// on it (awaitingReplySummaryTx).
	evType := IntegrationEventTaskComment
	summary := clampSingleLine(c.Summary, MaxQuestionSummaryLen)
	if c.Note {
		summary = ""
	}
	if summary != "" {
		evType = IntegrationEventTaskQuestion
	}
	filedVia := strings.TrimSpace(c.FiledVia)
	if filedVia == "" {
		filedVia = meta.filedVia
	}
	_, err = insertIntegrationEventTx(ctx, tx, IntegrationEvent{
		Type:            evType,
		At:              c.CreatedAt,
		TaskID:          c.TaskID,
		Repo:            meta.repo,
		State:           meta.state,
		QuestionSummary: summary,
		CommentID:       commentID,
		ExternalRef:     meta.externalRef,
		Origin:          meta.origin,
		PullRequest:     meta.pullRequest,
		FiledVia:        filedVia,
		Actor:           eventActorOr(ctx, c.Author),
	})
	return err
}

func recordObservationEventsTx(ctx context.Context, tx *sql.Tx, prevState State, hasPrev bool, prevObs *Observation, next Observation, actor Attribution) error {
	meta, ok, err := taskEventMetaTx(ctx, tx, next.TaskID)
	if err != nil || !ok {
		return err
	}
	var at time.Time
	if next.ObservedAt != nil {
		at = *next.ObservedAt
	}

	if hasPrev && meta.state != prevState {
		ev := IntegrationEvent{
			Type:          IntegrationEventTaskState,
			At:            at,
			TaskID:        next.TaskID,
			Repo:          meta.repo,
			PreviousState: prevState,
			State:         meta.state,
			ExternalRef:   meta.externalRef,
			Origin:        meta.origin,
			PullRequest:   meta.pullRequest,
			FiledVia:      meta.filedVia,
			Actor:         actor,
		}
		if meta.state == StateAwaitingReply {
			ev.QuestionSummary, ev.CommentID = awaitingReplySummaryTx(ctx, tx, next.TaskID)
		}
		if _, err := insertIntegrationEventTx(ctx, tx, ev); err != nil {
			return err
		}
	} else if meta.state == StateAwaitingReply && next.PendingQuestionCommentID != nil &&
		(prevObs == nil || prevObs.PendingQuestionCommentID == nil || *prevObs.PendingQuestionCommentID != *next.PendingQuestionCommentID) {
		var cSummary, cBody string
		cid := *next.PendingQuestionCommentID
		if err := tx.QueryRowContext(ctx,
			"SELECT `summary`, `body` FROM `task_comment` WHERE `id` = ?", cid,
		).Scan(&cSummary, &cBody); err == nil && strings.TrimSpace(cSummary) == "" && cBody != TurnStoppedComment {
			qSummary := DeriveQuestionSummary(cBody)
			if qSummary == "" && strings.TrimSpace(next.PendingSecret) != "" {
				qSummary = clampSingleLine("set "+strings.TrimSpace(next.PendingSecret)+" in the encrypted secret input", MaxQuestionSummaryLen)
			}
			if qSummary != "" {
				if _, err := insertIntegrationEventTx(ctx, tx, IntegrationEvent{
					Type:            IntegrationEventTaskQuestion,
					At:              at,
					TaskID:          next.TaskID,
					Repo:            meta.repo,
					State:           meta.state,
					QuestionSummary: qSummary,
					CommentID:       cid,
					ExternalRef:     meta.externalRef,
					Origin:          meta.origin,
					PullRequest:     meta.pullRequest,
					FiledVia:        meta.filedVia,
					Actor:           actor,
				}); err != nil {
					return err
				}
			}
		}
	}

	if next.CI != nil && next.CI.Conclusion != "" &&
		(prevObs == nil || prevObs.CI == nil ||
			prevObs.CI.RunID != next.CI.RunID ||
			prevObs.CI.Commit != next.CI.Commit ||
			prevObs.CI.Conclusion != next.CI.Conclusion) {
		ciAt := at
		if next.CI.FinishedAt != nil && !next.CI.FinishedAt.IsZero() {
			ciAt = *next.CI.FinishedAt
		}
		if _, err := insertIntegrationEventTx(ctx, tx, IntegrationEvent{
			Type:         IntegrationEventTaskCI,
			At:           ciAt,
			TaskID:       next.TaskID,
			Repo:         meta.repo,
			State:        meta.state,
			ExternalRef:  meta.externalRef,
			Origin:       meta.origin,
			PullRequest:  meta.pullRequest,
			FiledVia:     meta.filedVia,
			CIProvider:   "grain",
			CIRunID:      strings.TrimSpace(next.CI.RunID),
			CICommit:     strings.TrimSpace(next.CI.Commit),
			CIConclusion: string(next.CI.Conclusion),
			CIDetail:     clampSingleLine(next.CI.Detail, 200),
			Actor:        Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func recordGitHubCIEventTx(ctx context.Context, tx *sql.Tx, prev *GitHubCIJob, merged GitHubCIJob) error {
	concl := strings.TrimSpace(merged.Conclusion)
	if concl == "" {
		return nil
	}
	if prev != nil && strings.TrimSpace(prev.Conclusion) == concl && prev.RunAttempt == merged.RunAttempt {
		return nil
	}
	branch := strings.TrimPrefix(strings.TrimSpace(merged.Ref), "refs/heads/")
	if branch == "" || strings.TrimSpace(merged.Repo) == "" {
		return nil
	}
	repoRef, err := ParseRepo(merged.Repo)
	if err != nil {
		return nil
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT `t`.`id` FROM `task` AS `t` "+
			"LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id` "+
			"WHERE LOWER(`t`.`target_owner`) = LOWER(?) AND LOWER(`t`.`target_name`) = LOWER(?) "+
			"AND `t`.`branch` = ? AND `o`.`closed_at` IS NULL ORDER BY `t`.`id`",
		repoRef.StoredOwner(), repoRef.Name, branch,
	)
	if err != nil {
		return err
	}
	var taskIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		taskIDs = append(taskIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(taskIDs) == 0 {
		return nil
	}

	runID := strconv.FormatInt(merged.CheckRunID, 10)
	if merged.RunID > 0 {
		runID = fmt.Sprintf("gha:%s:%d", repoRef.String(), merged.RunID)
	}
	detail := strings.TrimSpace(merged.JobName)
	if wf := strings.TrimSpace(merged.Workflow); wf != "" && detail != "" && !strings.EqualFold(wf, detail) {
		detail = wf + " / " + detail
	} else if detail == "" {
		detail = wf
	}
	at := merged.UpdatedAt
	if merged.CompletedAt != nil && !merged.CompletedAt.IsZero() {
		at = *merged.CompletedAt
	}

	for _, taskID := range taskIDs {
		meta, ok, err := taskEventMetaTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := insertIntegrationEventTx(ctx, tx, IntegrationEvent{
			Type:         IntegrationEventTaskCI,
			At:           at,
			TaskID:       taskID,
			Repo:         meta.repo,
			State:        meta.state,
			ExternalRef:  meta.externalRef,
			Origin:       meta.origin,
			PullRequest:  meta.pullRequest,
			FiledVia:     meta.filedVia,
			CIProvider:   "github",
			CIRunID:      runID,
			CICommit:     strings.TrimSpace(merged.Commit),
			CIConclusion: concl,
			CIDetail:     clampSingleLine(detail, 200),
			Actor:        Attribution{Actor: Principal{Kind: PrincipalAutomation, ID: "grain"}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// LatestIntegrationEventID returns the highest recorded integration_event id,
// or 0 when no events have been recorded yet.
func (s *Store) LatestIntegrationEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(`id`), 0) FROM `integration_event`").Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("reading latest integration event id: %w", err)
	}
	return id, nil
}

// ListIntegrationEvents returns integration events strictly after filter.AfterID
// in ascending id order.
func (s *Store) ListIntegrationEvents(ctx context.Context, filter IntegrationEventFilter) ([]IntegrationEvent, error) {
	var conds []string
	var args []any
	conds = append(conds, "`id` > ?")
	args = append(args, filter.AfterID)

	if repo := strings.TrimSpace(filter.Repo); repo != "" {
		if parsed, err := ParseRepo(repo); err == nil {
			repo = parsed.String()
		}
		conds = append(conds, "LOWER(`repo`) = LOWER(?)")
		args = append(args, repo)
	}
	if len(filter.Types) > 0 {
		placeholders := make([]string, len(filter.Types))
		for i, t := range filter.Types {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		conds = append(conds, "`type` IN ("+strings.Join(placeholders, ",")+")")
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query := "SELECT " +
		"`id`,`type`,`at`,`task_id`,`repo`,`previous_state`,`state`,`question_summary`,`comment_id`," +
		"`external_ref`,`origin`,`pull_request`,`filed_via`," +
		"`ci_provider`,`ci_run_id`,`ci_commit`,`ci_conclusion`,`ci_detail`," +
		"`actor_kind`,`actor_id`,`actor_source`,`behalf_kind`,`behalf_id`,`behalf_source`," +
		"`notification_id`,`subject`,`severity`,`summary`,`approval_id`,`script`,`decision` " +
		"FROM `integration_event` WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY `id` ASC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing integration events: %w", err)
	}
	defer rows.Close()

	var out []IntegrationEvent
	for rows.Next() {
		var e IntegrationEvent
		var evType, prevState, state string
		var actorKind, actorID, actorSource string
		var behalfKind, behalfID, behalfSource string
		if err := rows.Scan(
			&e.ID, &evType, &e.At, &e.TaskID, &e.Repo, &prevState, &state, &e.QuestionSummary, &e.CommentID,
			&e.ExternalRef, &e.Origin, &e.PullRequest, &e.FiledVia,
			&e.CIProvider, &e.CIRunID, &e.CICommit, &e.CIConclusion, &e.CIDetail,
			&actorKind, &actorID, &actorSource, &behalfKind, &behalfID, &behalfSource,
			&e.NotificationID, &e.Subject, &e.Severity, &e.Summary, &e.ApprovalID, &e.Script, &e.Decision,
		); err != nil {
			return nil, fmt.Errorf("scanning integration event: %w", err)
		}
		e.Type = IntegrationEventType(evType)
		e.PreviousState = State(prevState)
		e.State = State(state)
		e.At = e.At.UTC()
		e.Actor = Attribution{
			Actor: Principal{
				Kind:   PrincipalKind(actorKind),
				ID:     actorID,
				Source: AuthSource(actorSource),
			},
		}
		if behalfKind != "" || behalfID != "" {
			e.Actor.OnBehalfOf = &Principal{
				Kind:   PrincipalKind(behalfKind),
				ID:     behalfID,
				Source: AuthSource(behalfSource),
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecordIntegrationEvent records e outside any other write: the
// local_script.* events, whose approvals live in the daemon's memory
// rather than the store. A TaskID with no Repo takes the task's own.
func (s *Store) RecordIntegrationEvent(ctx context.Context, e IntegrationEvent) error {
	return s.write(ctx, "record integration event "+string(e.Type), func(tx *sql.Tx) error {
		return recordIntegrationEventTx(ctx, tx, e)
	})
}

// recordIntegrationEventTx inserts e, filling its Repo from its task
// when it names one and has none, and its actor from ctx when unset.
func recordIntegrationEventTx(ctx context.Context, tx *sql.Tx, e IntegrationEvent) error {
	if e.TaskID != "" && e.Repo == "" {
		meta, ok, err := taskEventMetaTx(ctx, tx, e.TaskID)
		if err != nil {
			return err
		}
		if ok {
			e.Repo = meta.repo
		}
	}
	e.Actor = eventActorOr(ctx, e.Actor)
	_, err := insertIntegrationEventTx(ctx, tx, e)
	return err
}

// notificationEventTx records a notification.* event for n.
func notificationEventTx(ctx context.Context, tx *sql.Tx, typ IntegrationEventType, n Notification, at time.Time) error {
	return recordIntegrationEventTx(ctx, tx, IntegrationEvent{
		Type:           typ,
		At:             at,
		TaskID:         n.TaskID,
		Repo:           n.Repo,
		NotificationID: n.ID,
		Subject:        n.Subject,
		Severity:       string(n.Severity),
		Summary:        n.Title,
	})
}
