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
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SQL-level filtering, keyset paging, counting, and page-only enrichment for
// GET /api/tasks and GET /api/inbox.
//
// At 50,000 tasks the old path loaded and hydrated every task in the store and
// ran twenty whole-store enrichment queries before slicing a 50- or 100-row
// page. Every method here narrows and limits in SQL first, using covering and
// partial indexes over task, task_observation, task_run, and task_link, and
// then hydrates and enriches ONLY the winning page's task IDs.

const taskQueryBatchChunk = 400

// taskStateCaseSQL is the task_state view's CASE expression over aliases
// `t` (task), `o` (task_observation) and `r` (open task_run with finished_at IS NULL).
var taskStateCaseSQL = "CASE" +
	" WHEN `o`.`closed_at` IS NOT NULL THEN 'closed'" +
	" WHEN `o`.`completed_at` IS NOT NULL AND `t`.`auto_merge` = FALSE AND EXISTS (" +
	"SELECT 1 FROM `task_link` AS `fl` WHERE `fl`.`task_id` = `t`.`id` AND `fl`.`kind` IN (" + changeLinkKindsSQL + ")" +
	") THEN 'awaiting_submit'" +
	" WHEN `o`.`completed_at` IS NOT NULL AND `t`.`origin_reason` != '" + string(ReasonReview) + "' AND NOT EXISTS (" +
	"SELECT 1 FROM `task_link` AS `al` WHERE `al`.`task_id` = `t`.`id` AND `al`.`kind` IN (" + changeLinkKindsSQL + ")" +
	") THEN 'awaiting_reply'" +
	" WHEN `o`.`completed_at` IS NOT NULL THEN 'completed'" +
	" WHEN `o`.`pending_question_comment_id` IS NOT NULL THEN 'awaiting_reply'" +
	" WHEN `r`.`id` IS NOT NULL THEN 'running'" +
	" WHEN COALESCE(`o`.`streak_count`, 0) >= " + strconv.Itoa(MaxConsecutiveFailures) + " THEN 'failed'" +
	" WHEN `t`.`deferred_at` IS NOT NULL THEN 'deferred'" +
	" WHEN `t`.`approval_actor_kind` IS NULL THEN 'proposed'" +
	" ELSE 'queued'" +
	" END"

// openUncompletedStateCaseSQL is equivalent to taskStateCaseSQL whenever
// `o`.`closed_at` IS NULL AND `o`.`completed_at` IS NULL. Because it omits
// `t`.`auto_merge`, `t`.`origin_reason`, and the `task_link` subqueries, every
// column it references is covered by task_created_desc + task_observation_state
// + task_run_open_task without touching task table heap rows.
var openUncompletedStateCaseSQL = "CASE" +
	" WHEN `o`.`pending_question_comment_id` IS NOT NULL THEN 'awaiting_reply'" +
	" WHEN `r`.`id` IS NOT NULL THEN 'running'" +
	" WHEN COALESCE(`o`.`streak_count`, 0) >= " + strconv.Itoa(MaxConsecutiveFailures) + " THEN 'failed'" +
	" WHEN `t`.`deferred_at` IS NOT NULL THEN 'deferred'" +
	" WHEN `t`.`approval_actor_kind` IS NULL THEN 'proposed'" +
	" ELSE 'queued'" +
	" END"

// TaskListFilter describes SQL-pushable narrowing predicates for task listings.
type TaskListFilter struct {
	States         []State
	OpenOnly       bool
	Repo           string
	UseRepoScope   bool
	AllowedRepos   []RepoRef
	AllowEmptyRepo bool
	Search         string
	FiledVia       string
	ExternalRef    string
	Owner          string
	// DerivedFrom is an already-normalized PR ref ("owner/name#123") used as a
	// SQL pre-filter; callers still apply exact TaskDerivedFrom via PostMatch.
	DerivedFrom  string
	ClosedSince  *time.Time
	ClosedBefore *time.Time
	// PostMatch, when non-nil, verifies predicates SQL only pre-filtered
	// (such as DerivedFrom body-line parsing or non-ASCII case folding).
	PostMatch func(Task) bool
}

// TaskPageKey is a task's position in the newest-closed-first paged order:
// closing/ending/filing timestamp descending, then numeric IDs descending,
// then lexicographic raw ID descending.
type TaskPageKey struct {
	At    time.Time
	ID    int64
	IsNum bool
	RawID string
}

// ZeroTaskPageTime encodes a zero timestamp in a cursor string.
const ZeroTaskPageTime = "z"

// NewTaskPageKey builds a TaskPageKey for (at, rawID).
func NewTaskPageKey(at time.Time, rawID string) TaskPageKey {
	k := TaskPageKey{At: at, RawID: rawID}
	if n, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		k.ID, k.IsNum = n, true
	}
	return k
}

// Before reports whether k sorts before o (later times first; at equal times,
// higher numeric IDs before lower, numeric IDs before non-numeric, and higher
// rawID first).
func (k TaskPageKey) Before(o TaskPageKey) bool {
	if !k.At.Equal(o.At) {
		return k.At.After(o.At)
	}
	if k.IsNum && o.IsNum && k.ID != o.ID {
		return k.ID > o.ID
	}
	if k.IsNum != o.IsNum {
		return k.IsNum
	}
	return k.RawID > o.RawID
}

// String encodes k as an opaque cursor token.
func (k TaskPageKey) String() string {
	if k.At.IsZero() {
		return ZeroTaskPageTime + "." + k.RawID
	}
	return strconv.FormatInt(k.At.UnixNano(), 10) + "." + k.RawID
}

// ParseTaskPageCursor parses a cursor produced by TaskPageKey.String.
func ParseTaskPageCursor(s string) (TaskPageKey, bool) {
	at, rawID, ok := strings.Cut(s, ".")
	if !ok || rawID == "" || strings.ContainsAny(rawID, " \t\r\n") {
		return TaskPageKey{}, false
	}
	k := TaskPageKey{RawID: rawID}
	if at != ZeroTaskPageTime {
		ns, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			return TaskPageKey{}, false
		}
		k.At = time.Unix(0, ns).UTC()
	}
	if n, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		k.ID, k.IsNum = n, true
	}
	return k, true
}

// TaskListCounts holds the aggregate counts reported alongside a paged task list.
type TaskListCounts struct {
	Total    int
	Merged   int
	Sessions int
}

// TaskPageResult is the output of Store.QueryTaskPage.
type TaskPageResult struct {
	Tasks      []Task
	States     map[string]State
	NextCursor string
	Truncated  bool
	Counts     TaskListCounts
}

// TaskSummaryRow is the lightweight projection needed by GET /api/tasks?fields=summary.
type TaskSummaryRow struct {
	ID          string
	Title       string
	State       State
	Target      *RepoRef
	ExternalRef string
	CreatedAt   *time.Time
	BlockedBy   []string
	StateTimes  StateTimes
}

// TaskListEnrichment holds all per-page enrichment maps built from bounded
// PK-indexed queries over only the page's task IDs.
type TaskListEnrichment struct {
	States            map[string]State
	MergeQueueBlocked map[string]time.Time
	Repairing         map[string]bool
	MergeQueueStates  map[string]MergeQueueActivity
	RunNow            map[string]time.Time
	RunNowStale       map[string]bool
	Activity          map[string]RunActivity
	GrantWaits        map[string]GrantWait
	OOMRetrying       map[string]time.Time
	Spans             map[string]TaskSpan
	StateTimes        map[string]StateTimes
	Pauses            map[string]TaskPause
	TurnsStopped             map[string]bool
	PendingQuestions         map[string]PendingQuestion
	PendingQuestionCommentID map[string]int64
	PendingAccess            map[string]bool
	PendingSecrets    map[string]string
	NoteSummaries     map[string]string
	Undelivered       map[string]Principal
	GoalKinds         map[string]GoalKind
	BlockerHolds      map[string]BlockerHold
}

// DistinctTaskRepos lists every distinct target repository referenced by at
// least one task row, plus whether any task has no target repository.
func (s *Store) DistinctTaskRepos(ctx context.Context) (repos []RepoRef, hasEmpty bool, err error) {
	err = each(ctx, s.db, "SELECT DISTINCT `target_owner`, `target_name` FROM `task`", nil, func(rows *sql.Rows) error {
		var owner, name sql.NullString
		if err := rows.Scan(&owner, &name); err != nil {
			return err
		}
		if !owner.Valid {
			hasEmpty = true
			return nil
		}
		repos = append(repos, RepoFromStored(owner.String, name.String))
		return nil
	})
	return repos, hasEmpty, err
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (f TaskListFilter) impossibleScope() bool {
	return f.UseRepoScope && len(f.AllowedRepos) == 0 && !f.AllowEmptyRepo
}

func (f TaskListFilter) normalizedStates() []State {
	if len(f.States) == 0 {
		return nil
	}
	out := make([]State, 0, len(f.States))
	for _, st := range f.States {
		norm := State(strings.ToLower(strings.TrimSpace(string(st))))
		if norm != "" && !slices.Contains(out, norm) {
			out = append(out, norm)
		}
	}
	return out
}

func (f TaskListFilter) allowsState(st State) bool {
	if f.ClosedSince != nil || f.ClosedBefore != nil {
		if st != StateClosed {
			return false
		}
	}
	if f.OpenOnly && st == StateClosed {
		return false
	}
	states := f.normalizedStates()
	if len(states) == 0 {
		return true
	}
	return slices.Contains(states, st)
}

func (f TaskListFilter) allowsOnlyClosed() bool {
	if f.ClosedSince != nil || f.ClosedBefore != nil {
		return f.allowsState(StateClosed)
	}
	states := f.normalizedStates()
	return len(states) == 1 && states[0] == StateClosed && !f.OpenOnly
}

// onlyOpenUncompletedStates reports whether every state admitted by f implies
// o.closed_at IS NULL AND o.completed_at IS NULL, so openUncompletedStateCaseSQL
// is exact without reading task.auto_merge, task.origin_reason, or task_link.
func (f TaskListFilter) onlyOpenUncompletedStates() bool {
	if f.ClosedSince != nil || f.ClosedBefore != nil {
		return false
	}
	states := f.normalizedStates()
	if len(states) == 0 {
		return false
	}
	for _, st := range states {
		switch st {
		case StateRunning, StateFailed, StateDeferred, StateProposed, StateQueued:
		default:
			return false
		}
	}
	return true
}

func (f TaskListFilter) allowsAnyState() bool {
	if f.impossibleScope() {
		return false
	}
	if (f.ClosedSince != nil || f.ClosedBefore != nil) && !f.allowsState(StateClosed) {
		return false
	}
	states := f.normalizedStates()
	if len(states) > 0 {
		for _, st := range states {
			if f.allowsState(st) {
				return true
			}
		}
		return false
	}
	return true
}

// smallIndexedStateCandidates builds a CTE UNION query for filters whose
// requested states all belong to directly indexed small sets (no closed, no
// queued, no failed). Returns ("", nil, false) otherwise.
func (f TaskListFilter) smallIndexedStateCandidates() (cte string, args []any, ok bool) {
	if f.ClosedSince != nil || f.ClosedBefore != nil {
		return "", nil, false
	}
	states := f.normalizedStates()
	if len(states) == 0 {
		return "", nil, false
	}
	var parts []string
	hasCompletedOpen := false
	addCompletedOpen := func() {
		if !hasCompletedOpen {
			hasCompletedOpen = true
			parts = append(parts, "SELECT `task_id` AS `id` FROM `task_observation` WHERE `closed_at` IS NULL AND `completed_at` IS NOT NULL")
		}
	}
	for _, st := range states {
		switch st {
		case StateRunning:
			parts = append(parts, "SELECT `task_id` AS `id` FROM `task_run` WHERE `finished_at` IS NULL")
		case StateAwaitingReply:
			parts = append(parts, "SELECT `task_id` AS `id` FROM `task_observation` WHERE `closed_at` IS NULL AND `pending_question_comment_id` IS NOT NULL")
			addCompletedOpen()
		case StateAwaitingSubmit, StateCompleted:
			addCompletedOpen()
		case StateProposed:
			parts = append(parts, "SELECT `id` FROM `task` WHERE `approval_actor_kind` IS NULL")
		case StateDeferred:
			parts = append(parts, "SELECT `id` FROM `task` WHERE `deferred_at` IS NOT NULL")
		default:
			return "", nil, false
		}
	}
	if len(parts) == 0 {
		return "", nil, false
	}
	return "WITH `cands`(`id`) AS (" + strings.Join(parts, " UNION ") + ") ", args, true
}

// appendTaskOnlyFilterClauses appends predicates that reference only alias `t`
// (and correlated `task_link` for DerivedFrom), never `o` or `r`.
func appendTaskOnlyFilterClauses(conds []string, args []any, f TaskListFilter) ([]string, []any) {
	if f.impossibleScope() {
		return append(conds, "1 = 0"), args
	}
	if f.Repo != "" {
		if parsed, err := ParseRepo(f.Repo); err == nil {
			conds = append(conds, "LOWER(`t`.`target_owner`) = ? AND LOWER(`t`.`target_name`) = ?")
			args = append(args, strings.ToLower(parsed.StoredOwner()), strings.ToLower(parsed.Name))
		} else {
			conds = append(conds, "`t`.`target_owner` IS NOT NULL AND LOWER(`t`.`target_owner` || '/' || `t`.`target_name`) = ?")
			args = append(args, strings.ToLower(strings.TrimSpace(f.Repo)))
		}
	}
	if f.UseRepoScope {
		var parts []string
		for _, r := range f.AllowedRepos {
			parts = append(parts, "(LOWER(`t`.`target_owner`) = ? AND LOWER(`t`.`target_name`) = ?)")
			args = append(args, strings.ToLower(r.StoredOwner()), strings.ToLower(r.Name))
		}
		if f.AllowEmptyRepo {
			parts = append(parts, "`t`.`target_owner` IS NULL")
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	if f.ExternalRef != "" {
		conds = append(conds, "`t`.`external_ref` = ?")
		args = append(args, f.ExternalRef)
	}
	if f.FiledVia != "" && isASCIIString(f.FiledVia) {
		conds = append(conds, "instr(LOWER(`t`.`filed_via`), ?) > 0")
		args = append(args, strings.ToLower(f.FiledVia))
	}
	if f.Owner != "" {
		conds = append(conds, "LOWER(CASE WHEN TRIM(`t`.`owner`) <> '' THEN TRIM(`t`.`owner`) WHEN TRIM(`t`.`creator`) <> '' THEN TRIM(`t`.`creator`) ELSE ? END) = ?")
		args = append(args, DefaultTaskOwner, strings.ToLower(f.Owner))
	}
	if f.Search != "" && isASCIIString(f.Search) {
		needle := strings.ToLower(f.Search)
		idNeedle := strings.TrimPrefix(needle, "#")
		conds = append(conds,
			"(instr(LOWER(`t`.`title`), ?) > 0"+
				" OR instr(LOWER(`t`.`id`), ?) > 0"+
				" OR instr(LOWER(`t`.`external_ref`), ?) > 0"+
				" OR instr(LOWER(CASE WHEN `t`.`branch` <> '' THEN `t`.`branch` WHEN `t`.`target_owner` IS NOT NULL THEN 'grain/task-' || `t`.`id` ELSE '' END), ?) > 0"+
				" OR instr(LOWER(`t`.`body`), ?) > 0)")
		args = append(args, needle, idNeedle, needle, needle, needle)
	}
	if f.DerivedFrom != "" {
		conds = append(conds,
			"(EXISTS (SELECT 1 FROM `task_link` AS `dl` WHERE `dl`.`task_id` = `t`.`id` AND `dl`.`kind` = ? AND LOWER(`dl`.`target`) = ?)"+
				" OR instr(LOWER(`t`.`body`), 'derived from:') > 0)")
		args = append(args, string(LinkDerivedFrom), strings.ToLower(f.DerivedFrom))
	}
	return conds, args
}

// appendTaskFilterClauses appends WHERE clauses for f over aliases `t` (task),
// `o` (task_observation), and `r` (open task_run, optional when skipState is true).
func appendTaskFilterClauses(conds []string, args []any, f TaskListFilter, includeState bool) ([]string, []any) {
	conds, args = appendTaskOnlyFilterClauses(conds, args, f)
	if f.impossibleScope() {
		return conds, args
	}
	if f.ClosedSince != nil {
		conds = append(conds, "`o`.`closed_at` IS NOT NULL AND `o`.`closed_at` >= ?")
		args = append(args, f.ClosedSince.UTC())
	}
	if f.ClosedBefore != nil {
		conds = append(conds, "`o`.`closed_at` IS NOT NULL AND `o`.`closed_at` < ?")
		args = append(args, f.ClosedBefore.UTC())
	}
	if includeState {
		conds, args = appendStateFilterClauses(conds, args, f)
	}
	return conds, args
}

func appendStateFilterClauses(conds []string, args []any, f TaskListFilter) ([]string, []any) {
	states := f.normalizedStates()
	if f.OpenOnly {
		conds = append(conds, "`o`.`closed_at` IS NULL")
	}
	if len(states) == 0 {
		return conds, args
	}
	if len(states) == 1 {
		switch states[0] {
		case StateClosed:
			return append(conds, "`o`.`closed_at` IS NOT NULL"), args
		case StateQueued:
			return append(conds,
				"`o`.`closed_at` IS NULL",
				"`o`.`completed_at` IS NULL",
				"`o`.`pending_question_comment_id` IS NULL",
				"COALESCE(`o`.`streak_count`, 0) < "+strconv.Itoa(MaxConsecutiveFailures),
				"`t`.`deferred_at` IS NULL",
				"`t`.`approval_actor_kind` IS NOT NULL",
				"`r`.`id` IS NULL",
			), args
		case StateProposed:
			return append(conds,
				"`o`.`closed_at` IS NULL",
				"`o`.`completed_at` IS NULL",
				"`o`.`pending_question_comment_id` IS NULL",
				"COALESCE(`o`.`streak_count`, 0) < "+strconv.Itoa(MaxConsecutiveFailures),
				"`t`.`deferred_at` IS NULL",
				"`t`.`approval_actor_kind` IS NULL",
				"`r`.`id` IS NULL",
			), args
		case StateRunning:
			return append(conds,
				"`o`.`closed_at` IS NULL",
				"`o`.`completed_at` IS NULL",
				"`o`.`pending_question_comment_id` IS NULL",
				"`r`.`id` IS NOT NULL",
			), args
		case StateFailed:
			return append(conds,
				"`o`.`closed_at` IS NULL",
				"`o`.`completed_at` IS NULL",
				"`o`.`pending_question_comment_id` IS NULL",
				"`r`.`id` IS NULL",
				"COALESCE(`o`.`streak_count`, 0) >= "+strconv.Itoa(MaxConsecutiveFailures),
			), args
		case StateDeferred:
			return append(conds,
				"`o`.`closed_at` IS NULL",
				"`o`.`completed_at` IS NULL",
				"`o`.`pending_question_comment_id` IS NULL",
				"`r`.`id` IS NULL",
				"COALESCE(`o`.`streak_count`, 0) < "+strconv.Itoa(MaxConsecutiveFailures),
				"`t`.`deferred_at` IS NOT NULL",
			), args
		}
	}
	if !slices.Contains(states, StateClosed) {
		conds = append(conds, "`o`.`closed_at` IS NULL")
	}
	stateCase := taskStateCaseSQL
	if f.onlyOpenUncompletedStates() {
		conds = append(conds, "`o`.`completed_at` IS NULL")
		stateCase = openUncompletedStateCaseSQL
	}
	ph := make([]string, len(states))
	for i, st := range states {
		ph[i] = "?"
		args = append(args, string(st))
	}
	conds = append(conds, stateCase+" IN ("+strings.Join(ph, ",")+")")
	return conds, args
}

func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

func inPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringArgs(ids []string) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// fetchTasksByIDs reads and hydrates ONLY the named task IDs, preserving the
// exact slice order of ids.
func (s *Store) fetchTasksByIDs(ctx context.Context, ids []string) ([]Task, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	byID := make(map[string]Task, len(ids))
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		rows, err := s.db.QueryContext(ctx,
			"SELECT "+taskColumns+" FROM `task` WHERE `id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("fetching task page: %w", err)
		}
		for rows.Next() {
			t, err := scanTask(rows.Scan)
			if err != nil {
				rows.Close()
				return nil, err
			}
			byID[t.ID] = t
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	ptrs := make([]*Task, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	if err := hydrateTasksByIDs(ctx, s.db, ptrs); err != nil {
		return nil, err
	}
	return out, nil
}

// hydrateTasksByIDs fills child rows for ONLY the given tasks using chunked
// `task_id IN (...)` primary-key lookups instead of full-table scans.
func hydrateTasksByIDs(ctx context.Context, q querier, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}
	byID := make(map[string]*Task, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
		ids = append(ids, t.ID)
	}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		in := " WHERE `task_id` IN (" + inPlaceholders(len(chunk)) + ")"
		args := stringArgs(chunk)

		if err := each(ctx, q,
			"SELECT `task_id`,`owner`,`name` FROM `task_read`"+in+" ORDER BY `owner`,`name`",
			args, func(rows *sql.Rows) error {
				var id, owner, name string
				if err := rows.Scan(&id, &owner, &name); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					t.Reads = append(t.Reads, RepoFromStored(owner, name))
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`owner`,`name`,`base` FROM `task_write`"+in+" ORDER BY `owner`,`name`,`base`",
			args, func(rows *sql.Rows) error {
				var w WriteTarget
				var id, owner, name string
				if err := rows.Scan(&id, &owner, &name, &w.Base); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					w.Repo = RepoFromStored(owner, name)
					t.Writes = append(t.Writes, w)
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`template_id` FROM `task_filed_template`"+in,
			args, func(rows *sql.Rows) error {
				var id, template string
				if err := rows.Scan(&id, &template); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					t.TemplateID = template
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`template_id` FROM `task_source_template`"+in,
			args, func(rows *sql.Rows) error {
				var id, template string
				if err := rows.Scan(&id, &template); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					t.SourceTemplateID = template
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`capability`,`via`,`folder` FROM `task_grant`"+in+" ORDER BY `capability`",
			args, func(rows *sql.Rows) error {
				var g Grant
				var id, via string
				var folder sql.NullString
				if err := rows.Scan(&id, &g.Capability, &via, &folder); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					g.Via, g.Folder = GrantSource(via), ParseFolder(folder.String)
					t.Grants = append(t.Grants, g)
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`kind`,`target` FROM `task_link`"+in+" ORDER BY `kind`,`target`",
			args, func(rows *sql.Rows) error {
				var l Link
				var id, kind string
				if err := rows.Scan(&id, &kind, &l.Target); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					l.Kind = LinkKind(kind)
					t.Links = append(t.Links, l)
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`tag` FROM `task_tag`"+in+" ORDER BY `tag`",
			args, func(rows *sql.Rows) error {
				var id, tag string
				if err := rows.Scan(&id, &tag); err != nil {
					return err
				}
				if t := byID[id]; t != nil {
					t.Tags = append(t.Tags, tag)
				}
				return nil
			}); err != nil {
			return err
		}
		if err := each(ctx, q,
			"SELECT `task_id`,`name`,`value` FROM `task_custom_field`"+in+" ORDER BY `name`,`value`",
			args, func(rows *sql.Rows) error {
				var id, name, value string
				if err := rows.Scan(&id, &name, &value); err != nil {
					return err
				}
				t := byID[id]
				if t == nil {
					return nil
				}
				if t.CustomFields == nil {
					t.CustomFields = map[string][]string{}
				}
				t.CustomFields[name] = append(t.CustomFields[name], value)
				return nil
			}); err != nil {
			return err
		}
	}
	for _, t := range tasks {
		t.SourceTemplateID = cmp.Or(t.SourceTemplateID, t.TemplateID)
	}
	return nil
}

// StatesFor reads task states for only ids using PK-indexed lookups on task_state.
func (s *Store) StatesFor(ctx context.Context, ids []string) (map[string]State, error) {
	out := make(map[string]State, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		err := each(ctx, s.db,
			"SELECT `task_id`,`state` FROM `task_state` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var id, st string
				if err := rows.Scan(&id, &st); err != nil {
					return err
				}
				out[id] = State(st)
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// QueryLiveTasks loads up to limit open (non-closed) tasks matching filter in
// canonical dispatch order (taskQueueOrderSQL). When more than limit open tasks
// exist, active non-failed tasks (live runs and inbox-attention tasks first,
// then queued/proposed/deferred) are prioritized ahead of failed tasks so large
// historical failure backlogs cannot crowd out active work.
func (s *Store) QueryLiveTasks(ctx context.Context, filter TaskListFilter, limit int) ([]Task, map[string]State, bool, error) {
	if !filter.allowsAnyState() {
		return nil, map[string]State{}, false, nil
	}
	filter.OpenOnly = true

	// Base-repair tags are rare; load the small set once via task_tag_tag.
	baseRepair := map[string]bool{}
	if err := each(ctx, s.db,
		"SELECT DISTINCT `task_id` FROM `task_tag` WHERE `tag` LIKE ?",
		[]any{baseRepairBranchTagPrefix + "%"}, func(rows *sql.Rows) error {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			baseRepair[id] = true
			return nil
		}); err != nil {
		return nil, nil, false, err
	}

	type liveCand struct {
		id         string
		priority   int
		baseRepair bool
		orderKey   float64
		approvedAt *time.Time
		createdAt  *time.Time
		tier       int // 0 = live/attention, 1 = active backlog, 2 = failed
	}

	conds, args := appendTaskFilterClauses(nil, nil, filter, true)
	query := "SELECT `t`.`id`, `t`.`priority`, `t`.`order_key`, `t`.`approved_at`, `t`.`created_at`," +
		" `o`.`completed_at`, `o`.`pending_question_comment_id`, COALESCE(`o`.`streak_count`, 0), `r`.`id`" +
		" FROM `task` AS `t`" +
		" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
		whereClause(conds)

	var cands []liveCand
	if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var c liveCand
		var approved, created, completed sql.NullTime
		var pendingQ sql.NullInt64
		var streak int
		var runID sql.NullString
		if err := rows.Scan(&c.id, &c.priority, &c.orderKey, &approved, &created, &completed, &pendingQ, &streak, &runID); err != nil {
			return err
		}
		c.baseRepair = baseRepair[c.id]
		c.approvedAt = timePtr(approved)
		c.createdAt = timePtr(created)
		switch {
		case runID.Valid || pendingQ.Valid || completed.Valid:
			c.tier = 0
		case streak >= MaxConsecutiveFailures:
			c.tier = 2
		default:
			c.tier = 1
		}
		cands = append(cands, c)
		return nil
	}); err != nil {
		return nil, nil, false, fmt.Errorf("listing open tasks: %w", err)
	}
	if len(cands) == 0 {
		return nil, map[string]State{}, false, nil
	}

	compareQueue := func(a, b liveCand) int {
		if a.priority != b.priority {
			if a.priority > b.priority {
				return -1
			}
			return 1
		}
		if a.baseRepair != b.baseRepair {
			if a.baseRepair {
				return -1
			}
			return 1
		}
		if a.orderKey != b.orderKey {
			if a.orderKey < b.orderKey {
				return -1
			}
			return 1
		}
		if d := compareNullableTimeAsc(coalesceTime(a.approvedAt, a.createdAt), coalesceTime(b.approvedAt, b.createdAt)); d != 0 {
			return d
		}
		if d := compareNullableTimeAsc(a.createdAt, b.createdAt); d != 0 {
			return d
		}
		return strings.Compare(a.id, b.id)
	}

	truncated := false
	if limit > 0 && len(cands) > limit {
		truncated = true
		slices.SortFunc(cands, func(a, b liveCand) int {
			if a.tier != b.tier {
				return cmp.Compare(a.tier, b.tier)
			}
			return compareQueue(a, b)
		})
		cands = cands[:limit]
	}
	// Preserve canonical queue order across the returned open rows.
	slices.SortFunc(cands, compareQueue)

	ids := make([]string, len(cands))
	for i, c := range cands {
		ids[i] = c.id
	}
	tasks, err := s.fetchTasksByIDs(ctx, ids)
	if err != nil {
		return nil, nil, false, err
	}
	states, err := s.StatesFor(ctx, ids)
	if err != nil {
		return nil, nil, false, err
	}
	return tasks, states, truncated, nil
}

func coalesceTime(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

// compareNullableTimeAsc matches SQLite's ASC order for nullable DATETIME
// (NULL sorts before non-NULL values, matching taskQueueOrderSQL).
func compareNullableTimeAsc(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	default:
		return 0
	}
}

type pagedKeyEntry struct {
	key   TaskPageKey
	state State
}

// closedTimeOf returns closed_at, else completed_at, else created_at, else zero.
func closedTimeOf(closed, completed, created sql.NullTime) time.Time {
	if closed.Valid {
		return closed.Time.UTC()
	}
	if completed.Valid {
		return completed.Time.UTC()
	}
	if created.Valid {
		return created.Time.UTC()
	}
	return time.Time{}
}

// QueryTaskPage answers a keyset-paged task query in newest-closed-first order,
// reading, hydrating, and enriching ONLY the winning page's rows.
func (s *Store) QueryTaskPage(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, limit int, limitAll bool, includeCounts bool) (TaskPageResult, error) {
	if !filter.allowsAnyState() {
		return TaskPageResult{States: map[string]State{}}, nil
	}
	if filter.PostMatch != nil {
		return s.queryTaskPageWithPostMatch(ctx, filter, cursor, limit, limitAll, includeCounts)
	}

	entries, hasMore, err := s.queryPagedKeys(ctx, filter, cursor, limit, limitAll)
	if err != nil {
		return TaskPageResult{}, err
	}

	var res TaskPageResult
	res.States = make(map[string]State, len(entries))
	if hasMore && len(entries) > 0 {
		res.Truncated = true
		res.NextCursor = entries[len(entries)-1].key.String()
	}

	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.key.RawID
		res.States[e.key.RawID] = e.state
	}
	if len(ids) > 0 {
		res.Tasks, err = s.fetchTasksByIDs(ctx, ids)
		if err != nil {
			return TaskPageResult{}, err
		}
	}
	if includeCounts {
		res.Counts, err = s.TaskCounts(ctx, filter)
		if err != nil {
			return TaskPageResult{}, err
		}
	}
	return res, nil
}

// QueryTaskPageSummaries answers a keyset-paged GET /api/tasks?fields=summary
// without reading task.body or hydrating unrelated child tables.
func (s *Store) QueryTaskPageSummaries(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, limit int, limitAll bool, includeCounts bool, needStall bool) ([]TaskSummaryRow, string, bool, TaskListCounts, error) {
	if !filter.allowsAnyState() {
		return nil, "", false, TaskListCounts{}, nil
	}
	if filter.PostMatch != nil {
		res, err := s.queryTaskPageWithPostMatch(ctx, filter, cursor, limit, limitAll, includeCounts)
		if err != nil {
			return nil, "", false, TaskListCounts{}, err
		}
		rows, err := s.summariesFromTasks(ctx, res.Tasks, res.States, needStall)
		return rows, res.NextCursor, res.Truncated, res.Counts, err
	}

	entries, hasMore, err := s.queryPagedKeys(ctx, filter, cursor, limit, limitAll)
	if err != nil {
		return nil, "", false, TaskListCounts{}, err
	}
	nextCursor := ""
	if hasMore && len(entries) > 0 {
		nextCursor = entries[len(entries)-1].key.String()
	}
	ids := make([]string, len(entries))
	states := make(map[string]State, len(entries))
	for i, e := range entries {
		ids[i] = e.key.RawID
		states[e.key.RawID] = e.state
	}
	rows, err := s.fetchTaskSummaryRows(ctx, ids, states, needStall)
	if err != nil {
		return nil, "", false, TaskListCounts{}, err
	}
	var counts TaskListCounts
	if includeCounts {
		counts, err = s.TaskCounts(ctx, filter)
		if err != nil {
			return nil, "", false, TaskListCounts{}, err
		}
	}
	return rows, nextCursor, hasMore, counts, nil
}

func (s *Store) queryTaskPageWithPostMatch(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, limit int, limitAll bool, includeCounts bool) (TaskPageResult, error) {
	// SQL pre-filter narrows the candidate IDs (e.g. derived-from links or body marker),
	// then Go verifies PostMatch and pages cleanly.
	ids, states, times, err := s.queryAllMatchingKeys(ctx, filter)
	if err != nil {
		return TaskPageResult{}, err
	}
	tasks, err := s.fetchTasksByIDs(ctx, ids)
	if err != nil {
		return TaskPageResult{}, err
	}
	var matched []Task
	var entries []pagedKeyEntry
	for _, t := range tasks {
		if !filter.PostMatch(t) {
			continue
		}
		matched = append(matched, t)
		entries = append(entries, pagedKeyEntry{
			key:   NewTaskPageKey(times[t.ID], t.ID),
			state: states[t.ID],
		})
	}
	slices.SortFunc(entries, func(a, b pagedKeyEntry) int {
		if a.key.Before(b.key) {
			return -1
		}
		if b.key.Before(a.key) {
			return 1
		}
		return 0
	})
	var counts TaskListCounts
	if includeCounts && len(matched) > 0 {
		counts, err = s.countsForTaskSlice(ctx, matched)
		if err != nil {
			return TaskPageResult{}, err
		}
	}
	start := 0
	if cursor != nil {
		start = sort.Search(len(entries), func(i int) bool {
			return cursor.Before(entries[i].key)
		})
	}
	end := len(entries)
	if !limitAll && limit > 0 {
		end = min(start+limit, len(entries))
	}
	byID := make(map[string]Task, len(matched))
	for _, t := range matched {
		byID[t.ID] = t
	}
	res := TaskPageResult{
		Tasks:  make([]Task, 0, end-start),
		States: make(map[string]State, end-start),
		Counts: counts,
	}
	for _, e := range entries[start:end] {
		res.Tasks = append(res.Tasks, byID[e.key.RawID])
		res.States[e.key.RawID] = e.state
	}
	if end < len(entries) && end > start {
		res.Truncated = true
		res.NextCursor = entries[end-1].key.String()
	}
	return res, nil
}

func (s *Store) countsForTaskSlice(ctx context.Context, tasks []Task) (TaskListCounts, error) {
	counts := TaskListCounts{Total: len(tasks)}
	nonSessionIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if sid, _ := ConsoleSessionIDOf(t); sid != "" || t.Interactive {
			counts.Sessions++
		} else {
			nonSessionIDs = append(nonSessionIDs, t.ID)
		}
	}
	for start := 0; start < len(nonSessionIDs); start += taskQueryBatchChunk {
		chunk := nonSessionIDs[start:min(start+taskQueryBatchChunk, len(nonSessionIDs))]
		var n int
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `task_observation` WHERE `pr_merged_at` IS NOT NULL AND `task_id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk)...).Scan(&n); err != nil {
			return TaskListCounts{}, err
		}
		counts.Merged += n
	}
	return counts, nil
}

func (s *Store) queryAllMatchingKeys(ctx context.Context, filter TaskListFilter) ([]string, map[string]State, map[string]time.Time, error) {
	cte, cteArgs, useCTE := filter.smallIndexedStateCandidates()
	conds, args := appendTaskFilterClauses(nil, cteArgs, filter, true)
	from := " FROM `task` AS `t`"
	if useCTE {
		from = " FROM `cands` JOIN `task` AS `t` ON `t`.`id` = `cands`.`id`"
	}
	query := cte +
		"SELECT `t`.`id`, `o`.`closed_at`, `o`.`completed_at`, `t`.`created_at`, " + taskStateCaseSQL +
		from +
		" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
		whereClause(conds)

	var ids []string
	states := map[string]State{}
	times := map[string]time.Time{}
	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var id, st string
		var closed, completed, created sql.NullTime
		if err := rows.Scan(&id, &closed, &completed, &created, &st); err != nil {
			return err
		}
		ids = append(ids, id)
		states[id] = State(st)
		times[id] = closedTimeOf(closed, completed, created)
		return nil
	})
	return ids, states, times, err
}

// queryPagedKeys selects up to limit winning (TaskPageKey, State) entries in
// exact newest-closed-first order using indexed timestamp range scans.
func (s *Store) queryPagedKeys(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, limit int, limitAll bool) ([]pagedKeyEntry, bool, error) {
	// Fast path 1: small directly-indexed state sets (e.g. running, awaiting_reply, proposed).
	if cte, cteArgs, ok := filter.smallIndexedStateCandidates(); ok {
		conds, args := appendTaskFilterClauses(nil, cteArgs, filter, true)
		query := cte +
			"SELECT `t`.`id`, `o`.`closed_at`, `o`.`completed_at`, `t`.`created_at`, " + taskStateCaseSQL +
			" FROM `cands` JOIN `task` AS `t` ON `t`.`id` = `cands`.`id`" +
			" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
			" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
			whereClause(conds)
		var entries []pagedKeyEntry
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id, st string
			var closed, completed, created sql.NullTime
			if err := rows.Scan(&id, &closed, &completed, &created, &st); err != nil {
				return err
			}
			key := NewTaskPageKey(closedTimeOf(closed, completed, created), id)
			if cursor != nil && !cursor.Before(key) {
				return nil
			}
			entries = append(entries, pagedKeyEntry{key: key, state: State(st)})
			return nil
		}); err != nil {
			return nil, false, err
		}
		return finishPagedEntries(entries, limit, limitAll), !limitAll && limit > 0 && len(entries) > limit, nil
	}

	// Fast path 1b: substring search scans task.body once sequentially rather
	// than running random B-tree lookups across multiple disjoint time sets.
	if filter.Search != "" && isASCIIString(filter.Search) {
		return s.queryPagedKeysBySearch(ctx, filter, cursor, limit, limitAll)
	}

	// Fast path 2: limit=0 (all matching rows).
	if limitAll || limit <= 0 {
		ids, states, times, err := s.queryAllMatchingKeys(ctx, filter)
		if err != nil {
			return nil, false, err
		}
		entries := make([]pagedKeyEntry, 0, len(ids))
		for _, id := range ids {
			key := NewTaskPageKey(times[id], id)
			if cursor != nil && !cursor.Before(key) {
				continue
			}
			entries = append(entries, pagedKeyEntry{key: key, state: states[id]})
		}
		slices.SortFunc(entries, comparePagedKeyEntry)
		return entries, false, nil
	}

	// General O(limit) path: partition COALESCE(o.closed_at, o.completed_at, t.created_at)
	// into its three disjoint indexed sets and merge their top (limit+1) entries.
	want := limit + 1
	var combined []pagedKeyEntry
	seen := map[string]bool{}
	addEntries := func(list []pagedKeyEntry) {
		for _, e := range list {
			if !seen[e.key.RawID] {
				seen[e.key.RawID] = true
				combined = append(combined, e)
			}
		}
	}

	// Set A: closed tasks (o.closed_at IS NOT NULL) -> task_observation_closed_at.
	var minTS *time.Time
	updateFloor := func() {
		if len(combined) < want {
			return
		}
		slices.SortFunc(combined, comparePagedKeyEntry)
		floorAt := combined[want-1].key.At
		if floorAt.IsZero() {
			return
		}
		// Subtract 1 second to tolerate SQLite/Postgres timestamp formatting
		// differences; Go's exact TaskPageKey comparator trims the final page.
		t := floorAt.UTC().Add(-time.Second)
		if minTS == nil || t.After(*minTS) {
			minTS = &t
		}
	}

	if filter.allowsState(StateClosed) && (cursor == nil || !cursor.At.IsZero()) {
		list, err := s.queryTopFromClosedSet(ctx, filter, cursor, want)
		if err != nil {
			return nil, false, err
		}
		addEntries(list)
		updateFloor()
	}

	// Set B: open completed tasks (o.closed_at IS NULL AND o.completed_at IS NOT NULL) -> task_observation_completed_open.
	if filter.ClosedSince == nil && filter.ClosedBefore == nil &&
		(cursor == nil || !cursor.At.IsZero()) &&
		(filter.allowsState(StateAwaitingSubmit) || filter.allowsState(StateAwaitingReply) || filter.allowsState(StateCompleted)) {
		list, err := s.queryTopFromOpenCompletedSet(ctx, filter, cursor, minTS, want)
		if err != nil {
			return nil, false, err
		}
		addEntries(list)
		updateFloor()
	}

	// Set C: open uncompleted tasks (o.closed_at IS NULL AND o.completed_at IS NULL) -> task_created_desc.
	if filter.ClosedSince == nil && filter.ClosedBefore == nil &&
		(filter.allowsState(StateAwaitingReply) || filter.allowsState(StateRunning) || filter.allowsState(StateFailed) ||
			filter.allowsState(StateDeferred) || filter.allowsState(StateProposed) || filter.allowsState(StateQueued)) {
		list, err := s.queryTopFromOpenUncompletedSet(ctx, filter, cursor, minTS, want)
		if err != nil {
			return nil, false, err
		}
		addEntries(list)
	}

	slices.SortFunc(combined, comparePagedKeyEntry)
	hasMore := len(combined) > limit
	if hasMore {
		combined = combined[:limit]
	}
	return combined, hasMore, nil
}

func comparePagedKeyEntry(a, b pagedKeyEntry) int {
	if a.key.Before(b.key) {
		return -1
	}
	if b.key.Before(a.key) {
		return 1
	}
	return 0
}

func finishPagedEntries(entries []pagedKeyEntry, limit int, limitAll bool) []pagedKeyEntry {
	slices.SortFunc(entries, comparePagedKeyEntry)
	if !limitAll && limit > 0 && len(entries) > limit {
		return entries[:limit]
	}
	return entries
}

// queryTopFromTimeIndexedSet executes an O(want) descending index scan on
// tsCol with a ±1s tie window at the cursor and at the page boundary so Go's
// exact TaskPageKey comparator settles sub-second driver differences and
// numeric-vs-lexicographic ID ties with zero full-table sorts.
func (s *Store) queryTopFromTimeIndexedSet(
	ctx context.Context,
	baseSelect string,
	fromClause string,
	baseConds []string,
	baseArgs []any,
	tsCol string,
	idCol string,
	allowNullTS bool,
	cursor *TaskPageKey,
	minTS *time.Time,
	want int,
) ([]pagedKeyEntry, error) {
	seen := map[string]bool{}
	var out []pagedKeyEntry

	scanQuery := func(extraConds []string, extraArgs []any, orderAndLimit string) (int, error) {
		conds := make([]string, 0, len(baseConds)+len(extraConds))
		conds = append(conds, baseConds...)
		conds = append(conds, extraConds...)
		args := make([]any, 0, len(baseArgs)+len(extraArgs))
		args = append(args, baseArgs...)
		args = append(args, extraArgs...)

		q := baseSelect + fromClause + whereClause(conds) + orderAndLimit
		n := 0
		err := each(ctx, s.db, q, args, func(rows *sql.Rows) error {
			var id, st string
			var ts sql.NullTime
			if err := rows.Scan(&id, &ts, &st); err != nil {
				return err
			}
			n++
			var at time.Time
			if ts.Valid {
				at = ts.Time.UTC()
			}
			key := NewTaskPageKey(at, id)
			if cursor != nil && !cursor.Before(key) {
				return nil
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, pagedKeyEntry{key: key, state: State(st)})
			}
			return nil
		})
		return n, err
	}

	// Cursor at zero timestamp: only NULL timestamps can follow.
	if cursor != nil && cursor.At.IsZero() {
		if !allowNullTS || minTS != nil {
			return nil, nil
		}
		if _, err := scanQuery([]string{tsCol + " IS NULL"}, nil, ""); err != nil {
			return nil, err
		}
		slices.SortFunc(out, comparePagedKeyEntry)
		if len(out) > want {
			out = out[:want]
		}
		return out, nil
	}

	// Step 1: When resuming from a cursor, fetch the ±1s window around cursor.At
	// so every same-second tie (regardless of numeric vs string ID order or
	// sub-second text formatting) is resolved in Go.
	var strictlyBefore time.Time
	if cursor != nil {
		lo := cursor.At.UTC().Add(-time.Second)
		hi := cursor.At.UTC().Add(time.Second)
		strictlyBefore = lo
		if minTS == nil || !hi.Before(*minTS) {
			if minTS != nil && lo.Before(*minTS) {
				lo = *minTS
			}
			if _, err := scanQuery(
				[]string{tsCol + " IS NOT NULL", tsCol + " >= ?", tsCol + " <= ?"},
				[]any{lo, hi},
				"",
			); err != nil {
				return nil, err
			}
		}
	}

	// Step 2: Walk the DESC index strictly below the cursor window (or from the top),
	// stopping immediately at minTS when another disjoint set already fills want rows.
	need := want - len(out)
	var fetchedMain int
	if need > 0 && (cursor == nil || minTS == nil || strictlyBefore.After(*minTS)) {
		var extraConds []string
		var extraArgs []any
		if cursor != nil {
			extraConds = []string{tsCol + " IS NOT NULL", tsCol + " < ?"}
			extraArgs = []any{strictlyBefore}
		} else {
			extraConds = []string{tsCol + " IS NOT NULL"}
		}
		if minTS != nil {
			extraConds = append(extraConds, tsCol+" >= ?")
			extraArgs = append(extraArgs, *minTS)
		}
		extraArgs = append(extraArgs, need)
		var err error
		fetchedMain, err = scanQuery(extraConds, extraArgs, " ORDER BY "+tsCol+" DESC, "+idCol+" DESC LIMIT ?")
		if err != nil {
			return nil, err
		}
	}

	// Step 3: If the LIMIT cut off mid-scan, check whether the cut boundary
	// might split a same-second tie group and widen by ±1s only when needed.
	if fetchedMain == need && len(out) >= want {
		slices.SortFunc(out, comparePagedKeyEntry)
		tCut := out[want-2].key.At
		tLast := out[want-1].key.At
		if !tCut.IsZero() && !tLast.IsZero() && tCut.Sub(tLast) <= 2*time.Second {
			lo := tLast.UTC().Add(-time.Second)
			hi := tCut.UTC().Add(time.Second)
			if minTS != nil && lo.Before(*minTS) {
				lo = *minTS
			}
			if _, err := scanQuery(
				[]string{tsCol + " IS NOT NULL", tsCol + " >= ?", tsCol + " <= ?"},
				[]any{lo, hi},
				"",
			); err != nil {
				return nil, err
			}
		}
	}

	// Step 4: If fewer than want non-null rows exist and NULL timestamps are
	// possible in this set (and no non-zero floor already fills want rows),
	// pull NULL-timestamp rows (they sort last).
	if allowNullTS && minTS == nil && len(out) < want {
		if _, err := scanQuery([]string{tsCol + " IS NULL"}, nil, ""); err != nil {
			return nil, err
		}
	}

	slices.SortFunc(out, comparePagedKeyEntry)
	if len(out) > want {
		out = out[:want]
	}
	return out, nil
}

func (s *Store) queryTopFromClosedSet(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, want int) ([]pagedKeyEntry, error) {
	// Every row in Set A has state = 'closed' and o.closed_at IS NOT NULL.
	f := filter
	f.States = nil
	f.OpenOnly = false
	baseConds := []string{"`o`.`closed_at` IS NOT NULL"}
	baseConds, baseArgs := appendTaskFilterClauses(baseConds, nil, f, false)

	// Start from task_observation so SQLite/Postgres walk task_observation_closed_at directly.
	from := " FROM `task_observation` AS `o` JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id`"
	return s.queryTopFromTimeIndexedSet(
		ctx,
		"SELECT `o`.`task_id`, `o`.`closed_at`, 'closed'",
		from,
		baseConds,
		baseArgs,
		"`o`.`closed_at`",
		"`o`.`task_id`",
		false,
		cursor,
		nil,
		want,
	)
}

func (s *Store) queryTopFromOpenCompletedSet(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, minTS *time.Time, want int) ([]pagedKeyEntry, error) {
	baseConds := []string{"`o`.`closed_at` IS NULL", "`o`.`completed_at` IS NOT NULL"}
	baseConds, baseArgs := appendTaskFilterClauses(baseConds, nil, filter, true)
	from := " FROM `task_observation` AS `o`" +
		" JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL"
	return s.queryTopFromTimeIndexedSet(
		ctx,
		"SELECT `o`.`task_id`, `o`.`completed_at`, "+taskStateCaseSQL,
		from,
		baseConds,
		baseArgs,
		"`o`.`completed_at`",
		"`o`.`task_id`",
		false,
		cursor,
		minTS,
		want,
	)
}

func (s *Store) queryTopFromOpenUncompletedSet(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, minTS *time.Time, want int) ([]pagedKeyEntry, error) {
	baseConds := []string{"`o`.`closed_at` IS NULL", "`o`.`completed_at` IS NULL"}
	states := filter.normalizedStates()
	// Because o.closed_at IS NULL AND o.completed_at IS NULL is enforced above,
	// openUncompletedStateCaseSQL is exact even when filter includes closed/completed
	// states (e.g. awaiting_reply alongside closed) -- avoiding heap lookups for
	// task.auto_merge and task.origin_reason.
	f := filter
	f.States = nil
	f.OpenOnly = false
	baseConds, baseArgs := appendTaskFilterClauses(baseConds, nil, f, false)
	if len(states) > 0 {
		// Keep only states reachable when closed_at IS NULL AND completed_at IS NULL.
		var reachable []State
		for _, st := range states {
			switch st {
			case StateAwaitingReply, StateRunning, StateFailed, StateDeferred, StateProposed, StateQueued:
				reachable = append(reachable, st)
			}
		}
		if len(reachable) == 0 {
			return nil, nil
		}
		if len(reachable) == 1 && reachable[0] != StateAwaitingReply {
			rf := TaskListFilter{States: reachable}
			baseConds, baseArgs = appendStateFilterClauses(baseConds, baseArgs, rf)
		} else {
			ph := make([]string, len(reachable))
			for i, st := range reachable {
				ph[i] = "?"
				baseArgs = append(baseArgs, string(st))
			}
			baseConds = append(baseConds, openUncompletedStateCaseSQL+" IN ("+strings.Join(ph, ",")+")")
		}
	}
	from := " FROM `task` AS `t`" +
		" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL"
	return s.queryTopFromTimeIndexedSet(
		ctx,
		"SELECT `t`.`id`, `t`.`created_at`, "+openUncompletedStateCaseSQL,
		from,
		baseConds,
		baseArgs,
		"`t`.`created_at`",
		"`t`.`id`",
		true,
		cursor,
		minTS,
		want,
	)
}

// TaskCounts computes Total, Merged, and Sessions for filter using covering
// and partial indexes (never reading task.body unless Search or DerivedFrom
// requires it).
func (s *Store) TaskCounts(ctx context.Context, filter TaskListFilter) (TaskListCounts, error) {
	if !filter.allowsAnyState() {
		return TaskListCounts{}, nil
	}

	// Ultra-fast index-only path when no row-attribute filter is set (the common
	// case for GET /api/tasks?state=closed&cursor= and GET /api/tasks?limit=100&cursor=).
	noAttrFilter := filter.Repo == "" && !filter.UseRepoScope && filter.Search == "" &&
		filter.FiledVia == "" && filter.ExternalRef == "" && filter.Owner == "" &&
		filter.DerivedFrom == "" && filter.ClosedSince == nil && filter.ClosedBefore == nil
	if noAttrFilter && !filter.OpenOnly && len(filter.States) == 0 {
		return s.fastUnfilteredCounts(ctx, false)
	}
	if noAttrFilter && filter.allowsOnlyClosed() {
		return s.fastUnfilteredCounts(ctx, true)
	}

	cte, cteArgs, useCTE := filter.smallIndexedStateCandidates()
	conds, args := appendTaskFilterClauses(nil, cteArgs, filter, true)
	from := " FROM `task` AS `t`"
	if useCTE {
		from = " FROM `cands` JOIN `task` AS `t` ON `t`.`id` = `cands`.`id`"
	} else if filter.allowsOnlyClosed() {
		from = " FROM `task_observation` AS `o` JOIN `task` AS `t` ON `t`.`id` = `o`.`task_id`"
	}
	joins := " LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL"
	if !useCTE && filter.allowsOnlyClosed() {
		joins = ""
	}

	sessionExpr := "(`t`.`interactive` = 1 OR EXISTS (SELECT 1 FROM `task_tag` AS `ct` WHERE `ct`.`task_id` = `t`.`id` AND `ct`.`tag` LIKE '" + ConsoleSessionTagPrefix + "%' AND `ct`.`tag` <> '" + ConsoleSessionTagPrefix + "'))"
	query := cte +
		"SELECT COUNT(*)," +
		" COALESCE(SUM(CASE WHEN " + sessionExpr + " THEN 1 ELSE 0 END), 0)," +
		" COALESCE(SUM(CASE WHEN NOT " + sessionExpr + " AND `o`.`pr_merged_at` IS NOT NULL THEN 1 ELSE 0 END), 0)" +
		from + joins + whereClause(conds)

	var c TaskListCounts
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&c.Total, &c.Sessions, &c.Merged); err != nil {
		return TaskListCounts{}, fmt.Errorf("counting tasks: %w", err)
	}
	return c, nil
}

func (s *Store) fastUnfilteredCounts(ctx context.Context, closedOnly bool) (TaskListCounts, error) {
	var c TaskListCounts
	if closedOnly {
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `task_observation` WHERE `closed_at` IS NOT NULL").Scan(&c.Total); err != nil {
			return TaskListCounts{}, fmt.Errorf("counting closed tasks: %w", err)
		}
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `task_observation` WHERE `closed_at` IS NOT NULL AND `pr_merged_at` IS NOT NULL").Scan(&c.Merged); err != nil {
			return TaskListCounts{}, fmt.Errorf("counting closed merged tasks: %w", err)
		}
	} else {
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `task`").Scan(&c.Total); err != nil {
			return TaskListCounts{}, fmt.Errorf("counting tasks: %w", err)
		}
		if err := s.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM `task_observation` WHERE `pr_merged_at` IS NOT NULL").Scan(&c.Merged); err != nil {
			return TaskListCounts{}, fmt.Errorf("counting merged tasks: %w", err)
		}
	}

	// Collect the tiny set of session tasks via partial index task_interactive + task_tag_tag.
	sessionQuery := "WITH `sess`(`id`) AS (" +
		"SELECT `id` FROM `task` WHERE `interactive` = 1" +
		" UNION " +
		"SELECT `task_id` FROM `task_tag` WHERE `tag` LIKE ? AND `tag` <> ?" +
		") SELECT COUNT(*), COALESCE(SUM(CASE WHEN `o`.`pr_merged_at` IS NOT NULL THEN 1 ELSE 0 END), 0)" +
		" FROM `sess` LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `sess`.`id`"
	if closedOnly {
		sessionQuery += " WHERE `o`.`closed_at` IS NOT NULL"
	}
	var mergedSessions int
	if err := s.db.QueryRowContext(ctx, sessionQuery, ConsoleSessionTagPrefix+"%", ConsoleSessionTagPrefix).Scan(&c.Sessions, &mergedSessions); err != nil {
		return TaskListCounts{}, fmt.Errorf("counting session tasks: %w", err)
	}
	c.Merged = max(0, c.Merged-mergedSessions)
	return c, nil
}

// filingKey orders tasks oldest-first by CreatedAt (nil last), then numeric ID
// ascending, then rawID ascending -- matching ui's sortTasksByFiling.
type filingEntry struct {
	id        string
	createdAt *time.Time
	numID     int
	isNum     bool
	state     State
}

func makeFilingEntry(id string, created sql.NullTime, st State) filingEntry {
	e := filingEntry{id: id, createdAt: timePtr(created), state: st}
	if n, err := strconv.Atoi(id); err == nil {
		e.numID, e.isNum = n, true
	}
	return e
}

// compareFilingAsc sorts oldest first, nil CreatedAt last.
func compareFilingAsc(a, b filingEntry) int {
	switch {
	case a.createdAt != nil && b.createdAt != nil:
		if a.createdAt.Before(*b.createdAt) {
			return -1
		}
		if a.createdAt.After(*b.createdAt) {
			return 1
		}
	case a.createdAt != nil:
		return -1
	case b.createdAt != nil:
		return 1
	}
	if a.isNum && b.isNum && a.numID != b.numID {
		return cmp.Compare(a.numID, b.numID)
	}
	return strings.Compare(a.id, b.id)
}

// QueryTasksByFiling returns the newest limit tasks matching filter (or all
// when limit <= 0), ordered oldest-first by filing time.
func (s *Store) QueryTasksByFiling(ctx context.Context, filter TaskListFilter, limit int) ([]Task, map[string]State, error) {
	if !filter.allowsAnyState() {
		return nil, map[string]State{}, nil
	}
	if filter.PostMatch != nil {
		return s.queryFilingWithPostMatch(ctx, filter, limit)
	}
	entries, err := s.queryNewestFilingEntries(ctx, filter, limit)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, map[string]State{}, nil
	}
	ids := make([]string, len(entries))
	states := make(map[string]State, len(entries))
	for i, e := range entries {
		ids[i] = e.id
		states[e.id] = e.state
	}
	tasks, err := s.fetchTasksByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	return tasks, states, nil
}

// QueryTaskSummariesByFiling returns lightweight summary rows for the newest
// limit matching tasks in oldest-first filing order without reading task.body
// or hydrating non-blocking child tables.
func (s *Store) QueryTaskSummariesByFiling(ctx context.Context, filter TaskListFilter, limit int, needStall bool) ([]TaskSummaryRow, error) {
	if !filter.allowsAnyState() {
		return nil, nil
	}
	if filter.PostMatch != nil {
		tasks, states, err := s.queryFilingWithPostMatch(ctx, filter, limit)
		if err != nil {
			return nil, err
		}
		return s.summariesFromTasks(ctx, tasks, states, needStall)
	}
	entries, err := s.queryNewestFilingEntries(ctx, filter, limit)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	ids := make([]string, len(entries))
	states := make(map[string]State, len(entries))
	for i, e := range entries {
		ids[i] = e.id
		states[e.id] = e.state
	}
	return s.fetchTaskSummaryRows(ctx, ids, states, needStall)
}

func (s *Store) queryFilingWithPostMatch(ctx context.Context, filter TaskListFilter, limit int) ([]Task, map[string]State, error) {
	entries, err := s.queryNewestFilingEntries(ctx, filter, 0)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(entries))
	states := make(map[string]State, len(entries))
	for i, e := range entries {
		ids[i] = e.id
		states[e.id] = e.state
	}
	tasks, err := s.fetchTasksByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	matched := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if filter.PostMatch(t) {
			matched = append(matched, t)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	outStates := make(map[string]State, len(matched))
	for _, t := range matched {
		outStates[t.ID] = states[t.ID]
	}
	return matched, outStates, nil
}

// queryNewestFilingEntries returns the newest limit matching entries (or all
// when limit <= 0), sorted oldest-first (compareFilingAsc).
func (s *Store) queryNewestFilingEntries(ctx context.Context, filter TaskListFilter, limit int) ([]filingEntry, error) {
	// Fast path 1: small directly-indexed state sets.
	if cte, cteArgs, ok := filter.smallIndexedStateCandidates(); ok {
		conds, args := appendTaskFilterClauses(nil, cteArgs, filter, true)
		query := cte +
			"SELECT `t`.`id`, `t`.`created_at`, " + taskStateCaseSQL +
			" FROM `cands` JOIN `task` AS `t` ON `t`.`id` = `cands`.`id`" +
			" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
			" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
			whereClause(conds)
		var out []filingEntry
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id, st string
			var created sql.NullTime
			if err := rows.Scan(&id, &created, &st); err != nil {
				return err
			}
			out = append(out, makeFilingEntry(id, created, State(st)))
			return nil
		}); err != nil {
			return nil, err
		}
		slices.SortFunc(out, compareFilingAsc)
		if limit > 0 && len(out) > limit {
			out = out[len(out)-limit:]
		}
		return out, nil
	}

	// Fast path 1b: substring search scans task sequentially once without
	// forcing an ORDER BY task_created_desc random-lookup loop or joining
	// task_observation/task_run on non-matching rows.
	if filter.Search != "" && isASCIIString(filter.Search) {
		return s.queryFilingEntriesBySearch(ctx, filter, limit)
	}

	baseConds, baseArgs := appendTaskFilterClauses(nil, nil, filter, true)
	from := " FROM `task` AS `t`" +
		" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL"
	stateExpr := taskStateCaseSQL
	if states := filter.normalizedStates(); len(states) == 1 {
		switch states[0] {
		case StateClosed, StateQueued, StateProposed, StateRunning, StateFailed, StateDeferred:
			stateExpr = "'" + string(states[0]) + "'"
		}
	} else if filter.onlyOpenUncompletedStates() {
		stateExpr = openUncompletedStateCaseSQL
	}
	baseSelect := "SELECT `t`.`id`, `t`.`created_at`, " + stateExpr

	// Unbounded filing query (limit <= 0).
	if limit <= 0 {
		var out []filingEntry
		if err := each(ctx, s.db, baseSelect+from+whereClause(baseConds), baseArgs, func(rows *sql.Rows) error {
			var id, st string
			var created sql.NullTime
			if err := rows.Scan(&id, &created, &st); err != nil {
				return err
			}
			out = append(out, makeFilingEntry(id, created, State(st)))
			return nil
		}); err != nil {
			return nil, err
		}
		slices.SortFunc(out, compareFilingAsc)
		return out, nil
	}

	// Bounded O(limit) path: in compareFilingAsc, nil CreatedAt sorts LAST (i.e.
	// among the "newest" tail). Pull NULL CreatedAt rows first, then walk
	// task_created_desc backwards for the remaining slots with a ±1s boundary
	// check for exact tie-breaking on numeric IDs.
	seen := map[string]bool{}
	var out []filingEntry
	scan := func(extraConds []string, extraArgs []any, suffix string) (int, error) {
		conds := make([]string, 0, len(baseConds)+len(extraConds))
		conds = append(conds, baseConds...)
		conds = append(conds, extraConds...)
		args := make([]any, 0, len(baseArgs)+len(extraArgs))
		args = append(args, baseArgs...)
		args = append(args, extraArgs...)
		n := 0
		err := each(ctx, s.db, baseSelect+from+whereClause(conds)+suffix, args, func(rows *sql.Rows) error {
			var id, st string
			var created sql.NullTime
			if err := rows.Scan(&id, &created, &st); err != nil {
				return err
			}
			n++
			if !seen[id] {
				seen[id] = true
				out = append(out, makeFilingEntry(id, created, State(st)))
			}
			return nil
		})
		return n, err
	}

	if _, err := scan([]string{"`t`.`created_at` IS NULL"}, nil, ""); err != nil {
		return nil, err
	}
	need := limit - len(out)
	if need > 0 {
		fetched, err := scan(
			[]string{"`t`.`created_at` IS NOT NULL"},
			[]any{need + 1},
			" ORDER BY `t`.`created_at` DESC, `t`.`id` DESC LIMIT ?",
		)
		if err != nil {
			return nil, err
		}
		if fetched == need+1 {
			slices.SortFunc(out, compareFilingAsc)
			// Newest limit+1 are at the end; check the boundary between the
			// oldest kept item (len-limit) and the item just outside (len-limit-1).
			if len(out) > limit {
				keptOldest := out[len(out)-limit].createdAt
				droppedNewest := out[len(out)-limit-1].createdAt
				if keptOldest != nil && droppedNewest != nil && keptOldest.Sub(*droppedNewest) <= 2*time.Second {
					lo := droppedNewest.UTC().Add(-time.Second)
					hi := keptOldest.UTC().Add(time.Second)
					if _, err := scan(
						[]string{"`t`.`created_at` IS NOT NULL", "`t`.`created_at` >= ?", "`t`.`created_at` <= ?"},
						[]any{lo, hi},
						"",
					); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	slices.SortFunc(out, compareFilingAsc)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// searchTaskMatches runs a single sequential scan over `task` (zero joins to
// task_observation or task_run, no ORDER BY index-to-heap bounce) returning
// every matching task ID and its created_at.
func (s *Store) searchTaskMatches(ctx context.Context, filter TaskListFilter) ([]filingEntry, error) {
	conds, args := appendTaskOnlyFilterClauses(nil, nil, filter)
	var out []filingEntry
	err := each(ctx, s.db, "SELECT `t`.`id`, `t`.`created_at` FROM `task` AS `t`"+whereClause(conds), args, func(rows *sql.Rows) error {
		var id string
		var created sql.NullTime
		if err := rows.Scan(&id, &created); err != nil {
			return err
		}
		out = append(out, makeFilingEntry(id, created, ""))
		return nil
	})
	return out, err
}

func (s *Store) queryFilingEntriesBySearch(ctx context.Context, filter TaskListFilter, limit int) ([]filingEntry, error) {
	matched, err := s.searchTaskMatches(ctx, filter)
	if err != nil || len(matched) == 0 {
		return nil, err
	}
	needsObsFilter := len(filter.normalizedStates()) > 0 || filter.OpenOnly || filter.ClosedSince != nil || filter.ClosedBefore != nil
	if !needsObsFilter {
		slices.SortFunc(matched, compareFilingAsc)
		if limit > 0 && len(matched) > limit {
			matched = matched[len(matched)-limit:]
		}
		ids := make([]string, len(matched))
		for i, e := range matched {
			ids[i] = e.id
		}
		states, err := s.StatesFor(ctx, ids)
		if err != nil {
			return nil, err
		}
		for i := range matched {
			matched[i].state = states[matched[i].id]
		}
		return matched, nil
	}
	// Apply state/closed filters over only the matched task IDs (or fall back
	// to a single joined scan if a broad search term matched thousands of rows).
	if len(matched) > 4000 {
		ids, states, _, err := s.queryAllMatchingKeys(ctx, filter)
		if err != nil {
			return nil, err
		}
		createdByID := make(map[string]*time.Time, len(matched))
		for _, e := range matched {
			createdByID[e.id] = e.createdAt
		}
		out := make([]filingEntry, 0, len(ids))
		for _, id := range ids {
			var nt sql.NullTime
			if ct := createdByID[id]; ct != nil {
				nt = sql.NullTime{Time: *ct, Valid: true}
			}
			out = append(out, makeFilingEntry(id, nt, states[id]))
		}
		slices.SortFunc(out, compareFilingAsc)
		if limit > 0 && len(out) > limit {
			out = out[len(out)-limit:]
		}
		return out, nil
	}
	obsFilter := TaskListFilter{
		States:       filter.States,
		OpenOnly:     filter.OpenOnly,
		ClosedSince:  filter.ClosedSince,
		ClosedBefore: filter.ClosedBefore,
	}
	obsConds, obsArgs := appendTaskFilterClauses(nil, nil, obsFilter, true)
	var out []filingEntry
	for start := 0; start < len(matched); start += taskQueryBatchChunk {
		chunk := matched[start:min(start+taskQueryBatchChunk, len(matched))]
		chunkIDs := make([]string, len(chunk))
		for i, e := range chunk {
			chunkIDs[i] = e.id
		}
		conds := append([]string{"`t`.`id` IN (" + inPlaceholders(len(chunkIDs)) + ")"}, obsConds...)
		args := append(stringArgs(chunkIDs), obsArgs...)
		query := "SELECT `t`.`id`, `t`.`created_at`, " + taskStateCaseSQL +
			" FROM `task` AS `t`" +
			" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
			" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
			whereClause(conds)
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id, st string
			var created sql.NullTime
			if err := rows.Scan(&id, &created, &st); err != nil {
				return err
			}
			out = append(out, makeFilingEntry(id, created, State(st)))
			return nil
		}); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, compareFilingAsc)
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *Store) queryPagedKeysBySearch(ctx context.Context, filter TaskListFilter, cursor *TaskPageKey, limit int, limitAll bool) ([]pagedKeyEntry, bool, error) {
	matched, err := s.searchTaskMatches(ctx, filter)
	if err != nil || len(matched) == 0 {
		return nil, false, err
	}
	if len(matched) > 4000 {
		ids, states, times, err := s.queryAllMatchingKeys(ctx, filter)
		if err != nil {
			return nil, false, err
		}
		entries := make([]pagedKeyEntry, 0, len(ids))
		for _, id := range ids {
			key := NewTaskPageKey(times[id], id)
			if cursor != nil && !cursor.Before(key) {
				continue
			}
			entries = append(entries, pagedKeyEntry{key: key, state: states[id]})
		}
		hasMore := !limitAll && limit > 0 && len(entries) > limit
		return finishPagedEntries(entries, limit, limitAll), hasMore, nil
	}
	obsFilter := TaskListFilter{
		States:       filter.States,
		OpenOnly:     filter.OpenOnly,
		ClosedSince:  filter.ClosedSince,
		ClosedBefore: filter.ClosedBefore,
	}
	obsConds, obsArgs := appendTaskFilterClauses(nil, nil, obsFilter, true)
	var entries []pagedKeyEntry
	for start := 0; start < len(matched); start += taskQueryBatchChunk {
		chunk := matched[start:min(start+taskQueryBatchChunk, len(matched))]
		chunkIDs := make([]string, len(chunk))
		for i, e := range chunk {
			chunkIDs[i] = e.id
		}
		conds := append([]string{"`t`.`id` IN (" + inPlaceholders(len(chunkIDs)) + ")"}, obsConds...)
		args := append(stringArgs(chunkIDs), obsArgs...)
		query := "SELECT `t`.`id`, `o`.`closed_at`, `o`.`completed_at`, `t`.`created_at`, " + taskStateCaseSQL +
			" FROM `task` AS `t`" +
			" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
			" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
			whereClause(conds)
		if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
			var id, st string
			var closed, completed, created sql.NullTime
			if err := rows.Scan(&id, &closed, &completed, &created, &st); err != nil {
				return err
			}
			key := NewTaskPageKey(closedTimeOf(closed, completed, created), id)
			if cursor != nil && !cursor.Before(key) {
				return nil
			}
			entries = append(entries, pagedKeyEntry{key: key, state: State(st)})
			return nil
		}); err != nil {
			return nil, false, err
		}
	}
	hasMore := !limitAll && limit > 0 && len(entries) > limit
	return finishPagedEntries(entries, limit, limitAll), hasMore, nil
}

// fetchTaskSummaryRows reads only the light task columns, blocking links,
// blocker states, and (optionally) state times for the exact ids in order.
func (s *Store) fetchTaskSummaryRows(ctx context.Context, ids []string, states map[string]State, needStall bool) ([]TaskSummaryRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	type rawRow struct {
		id            string
		title         string
		owner, name   sql.NullString
		externalRef   string
		createdAt     *time.Time
		approvedAt    *time.Time
		undeferredAt  *time.Time
		deferredUntil *time.Time
	}
	byID := make(map[string]rawRow, len(ids))
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		err := each(ctx, s.db,
			"SELECT `id`,`title`,`target_owner`,`target_name`,`external_ref`,`created_at`,`approved_at`,`undeferred_at`,`deferred_until`"+
				" FROM `task` WHERE `id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var r rawRow
				var created, approved, undeferred, deferredUntil sql.NullTime
				if err := rows.Scan(&r.id, &r.title, &r.owner, &r.name, &r.externalRef, &created, &approved, &undeferred, &deferredUntil); err != nil {
					return err
				}
				r.createdAt = timePtr(created)
				r.approvedAt = timePtr(approved)
				r.undeferredAt = timePtr(undeferred)
				r.deferredUntil = timePtr(deferredUntil)
				byID[r.id] = r
				return nil
			})
		if err != nil {
			return nil, err
		}
	}

	// Read only blocking links for these ids.
	blockLinks := make(map[string][]string)
	targetSet := map[string]struct{}{}
	var targetIDs []string
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		err := each(ctx, s.db,
			"SELECT `task_id`,`target` FROM `task_link` WHERE `blocks` = 1 AND `task_id` IN ("+inPlaceholders(len(chunk))+") ORDER BY `kind`,`target`",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var id, target string
				if err := rows.Scan(&id, &target); err != nil {
					return err
				}
				if !slices.Contains(blockLinks[id], target) {
					blockLinks[id] = append(blockLinks[id], target)
				}
				if _, ok := targetSet[target]; !ok {
					targetSet[target] = struct{}{}
					targetIDs = append(targetIDs, target)
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	targetStates, err := s.StatesFor(ctx, targetIDs)
	if err != nil {
		return nil, err
	}

	var stateTimes map[string]StateTimes
	if needStall {
		stateTimes, err = s.stateTimesForIDs(ctx, ids, func(id string) (approved, undeferred, deferredUntil *time.Time) {
			r := byID[id]
			return r.approvedAt, r.undeferredAt, r.deferredUntil
		})
		if err != nil {
			return nil, err
		}
	}

	out := make([]TaskSummaryRow, 0, len(ids))
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue
		}
		row := TaskSummaryRow{
			ID:          r.id,
			Title:       r.title,
			State:       states[r.id],
			ExternalRef: r.externalRef,
			CreatedAt:   r.createdAt,
			StateTimes:  stateTimes[r.id],
		}
		if r.owner.Valid {
			target := RepoFromStored(r.owner.String, r.name.String)
			row.Target = &target
		}
		for _, target := range blockLinks[r.id] {
			if targetStates[target] != StateClosed {
				row.BlockedBy = append(row.BlockedBy, target)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Store) summariesFromTasks(ctx context.Context, tasks []Task, states map[string]State, needStall bool) ([]TaskSummaryRow, error) {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return s.fetchTaskSummaryRows(ctx, ids, states, needStall)
}

func (s *Store) stateTimesForIDs(ctx context.Context, ids []string, taskCols func(string) (approved, undeferred, deferredUntil *time.Time)) (map[string]StateTimes, error) {
	out := make(map[string]StateTimes, len(ids))
	for _, id := range ids {
		approved, undeferred, deferredUntil := taskCols(id)
		out[id] = StateTimes{
			ApprovedAt:    approved,
			RequeuedAt:    undeferred,
			DeferredUntil: deferredUntil,
		}
	}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		in := inPlaceholders(len(chunk))
		args := stringArgs(chunk)

		if err := each(ctx, s.db,
			"SELECT `task_id`,`completed_at`,`retry_requested_at`,`run_now_at` FROM `task_observation` WHERE `task_id` IN ("+in+")",
			args, func(rows *sql.Rows) error {
				var id string
				var completed, retried, runNow sql.NullTime
				if err := rows.Scan(&id, &completed, &retried, &runNow); err != nil {
					return err
				}
				st := out[id]
				st.CompletedAt = timePtr(completed)
				st.RequeuedAt = latest(timePtr(retried), timePtr(runNow), st.RequeuedAt)
				out[id] = st
				return nil
			}); err != nil {
			return nil, err
		}
		if err := each(ctx, s.db,
			"SELECT `task_id`,`started_at`,`finished_at` FROM `task_run` WHERE `task_id` IN ("+in+")",
			args, func(rows *sql.Rows) error {
				var id string
				var started time.Time
				var finished sql.NullTime
				if err := rows.Scan(&id, &started, &finished); err != nil {
					return err
				}
				st := out[id]
				if !finished.Valid {
					sCopy := started
					st.LiveRunStartedAt = &sCopy
				} else {
					f := finished.Time
					if st.LastRunFinishedAt == nil || f.After(*st.LastRunFinishedAt) {
						st.LastRunFinishedAt = &f
					}
				}
				out[id] = st
				return nil
			}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaskEnrichmentFor loads every list-row annotation needed to project tasks
// into ui.Task using bounded PK-indexed queries over ONLY tasks (and their
// direct blocking-link targets).
func (s *Store) TaskEnrichmentFor(ctx context.Context, tasks []Task, states map[string]State, needStall bool) (*TaskListEnrichment, error) {
	e := &TaskListEnrichment{
		States:            make(map[string]State, len(tasks)),
		MergeQueueBlocked: map[string]time.Time{},
		Repairing:         map[string]bool{},
		MergeQueueStates:  map[string]MergeQueueActivity{},
		RunNow:            map[string]time.Time{},
		RunNowStale:       map[string]bool{},
		Activity:          map[string]RunActivity{},
		GrantWaits:        map[string]GrantWait{},
		OOMRetrying:       map[string]time.Time{},
		Spans:             map[string]TaskSpan{},
		StateTimes:        map[string]StateTimes{},
		Pauses:            map[string]TaskPause{},
		TurnsStopped:             map[string]bool{},
		PendingQuestions:         map[string]PendingQuestion{},
		PendingQuestionCommentID: map[string]int64{},
		PendingAccess:            map[string]bool{},
		PendingSecrets:    map[string]string{},
		NoteSummaries:     map[string]string{},
		Undelivered:       map[string]Principal{},
		GoalKinds:         map[string]GoalKind{},
		BlockerHolds:      map[string]BlockerHold{},
	}
	if len(tasks) == 0 {
		return e, nil
	}

	ids := make([]string, len(tasks))
	taskByID := make(map[string]Task, len(tasks))
	var missingStates []string
	for i, t := range tasks {
		ids[i] = t.ID
		taskByID[t.ID] = t
		if st, ok := states[t.ID]; ok {
			e.States[t.ID] = st
		} else {
			missingStates = append(missingStates, t.ID)
		}
	}
	if len(missingStates) > 0 {
		fetched, err := s.StatesFor(ctx, missingStates)
		if err != nil {
			return nil, err
		}
		for id, st := range fetched {
			e.States[id] = st
		}
	}

	// Collect distinct blocking-link targets whose states aren't already known.
	var targetIDs []string
	targetSeen := map[string]bool{}
	var queuedIDs, awaitingReplyIDs []string
	for _, t := range tasks {
		st := e.States[t.ID]
		if st == StateQueued {
			queuedIDs = append(queuedIDs, t.ID)
		}
		if st == StateAwaitingReply {
			awaitingReplyIDs = append(awaitingReplyIDs, t.ID)
		}
		for _, l := range t.Links {
			if !l.Kind.Blocks() {
				continue
			}
			if _, ok := e.States[l.Target]; ok || targetSeen[l.Target] {
				continue
			}
			targetSeen[l.Target] = true
			targetIDs = append(targetIDs, l.Target)
		}
	}
	if len(targetIDs) > 0 {
		tStates, err := s.StatesFor(ctx, targetIDs)
		if err != nil {
			return nil, err
		}
		for id, st := range tStates {
			e.States[id] = st
		}
	}

	// Initialize StateTimes from task columns when stall notes are enabled.
	if needStall {
		for _, t := range tasks {
			e.StateTimes[t.ID] = StateTimes{
				ApprovedAt:    t.ApprovedAt,
				RequeuedAt:    t.UndeferredAt,
				DeferredUntil: t.DeferredUntil,
			}
		}
	}

	// 1. Single batched read of task_observation for all page IDs.
	pendingQCommentToTasks := map[int64][]string{}
	var pendingQCommentIDs []int64
	deliveredByTask := map[string]struct {
		delivered int64
		pendingQ  *int64
	}{}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		err := each(ctx, s.db,
			"SELECT `task_id`,`closed_at`,`completed_at`,`pr_merged_at`,`pr_closed_at`,"+
				"`merge_queue_blocked_at`,`merge_queue_repair_at`,`merge_queue_state`,`merge_queue_note`,"+
				"`merge_queue_state_at`,`merge_queue_stalled_at`,`merge_queue_reason`,"+
				"`run_now_at`,`retry_requested_at`,`pending_question_comment_id`,"+
				"`pending_secret`,`pending_access_request`,`delivered_comment_id`"+
				" FROM `task_observation` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var id string
				var closed, completed, prMerged, prClosed sql.NullTime
				var mqBlocked, mqRepair, mqStateAt, mqStalled sql.NullTime
				var mqState, mqNote, mqReason sql.NullString
				var runNow, retryAt sql.NullTime
				var pendingQ, delivered sql.NullInt64
				var pendingSecret, pendingAccess sql.NullString
				if err := rows.Scan(
					&id, &closed, &completed, &prMerged, &prClosed,
					&mqBlocked, &mqRepair, &mqState, &mqNote,
					&mqStateAt, &mqStalled, &mqReason,
					&runNow, &retryAt, &pendingQ,
					&pendingSecret, &pendingAccess, &delivered,
				); err != nil {
					return err
				}
				span := TaskSpan{
					EndedAt:    earlier(timePtr(completed), timePtr(closed)),
					ClosedAt:   timePtr(closed),
					PrMergedAt: timePtr(prMerged),
					PrClosedAt: timePtr(prClosed),
				}
				if span.EndedAt != nil || span.ClosedAt != nil || span.PrMergedAt != nil || span.PrClosedAt != nil {
					e.Spans[id] = span
				}
				if needStall {
					st := e.StateTimes[id]
					st.CompletedAt = timePtr(completed)
					st.RequeuedAt = latest(timePtr(retryAt), timePtr(runNow), st.RequeuedAt)
					e.StateTimes[id] = st
				}
				if mqBlocked.Valid {
					e.MergeQueueBlocked[id] = mqBlocked.Time
				}
				if mqRepair.Valid && !completed.Valid && !closed.Valid {
					e.Repairing[id] = true
				}
				if mqState.Valid && mqState.String != "" {
					e.MergeQueueStates[id] = MergeQueueActivity{
						State:     mqState.String,
						Note:      mqNote.String,
						At:        timePtr(mqStateAt),
						StalledAt: timePtr(mqStalled),
						Reason:    mqReason.String,
					}
				}
				if !closed.Valid {
					if runNow.Valid {
						e.RunNow[id] = runNow.Time
					}
					if pendingQ.Valid {
						e.PendingQuestionCommentID[id] = pendingQ.Int64
						if _, ok := pendingQCommentToTasks[pendingQ.Int64]; !ok {
							pendingQCommentIDs = append(pendingQCommentIDs, pendingQ.Int64)
						}
						pendingQCommentToTasks[pendingQ.Int64] = append(pendingQCommentToTasks[pendingQ.Int64], id)
						if pendingAccess.Valid && pendingAccess.String != "" {
							e.PendingAccess[id] = true
						}
					}
					if pendingSecret.Valid && pendingSecret.String != "" {
						e.PendingSecrets[id] = pendingSecret.String
					}
					if delivered.Valid && e.States[id] == StateAwaitingReply {
						var pq *int64
						if pendingQ.Valid {
							v := pendingQ.Int64
							pq = &v
						}
						deliveredByTask[id] = struct {
							delivered int64
							pendingQ  *int64
						}{delivered: delivered.Int64, pendingQ: pq}
					}
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}

	// 2. Single batched read of task_run for all page IDs -> StartedAt,
	// StateTimes (LastRunFinishedAt, LiveRunStartedAt), TaskActivity,
	// RunNowStale, and OOMRetrying.
	type latestRunInfo struct {
		startedAt  time.Time
		hasOOM     bool
		oomEndedAt time.Time
	}
	latestRun := map[string]latestRunInfo{}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		err := each(ctx, s.db,
			"SELECT `task_id`,`started_at`,`finished_at`,`activity`,`activity_at`,`agent_started_at`,`outcome`"+
				" FROM `task_run` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var id string
				var started time.Time
				var finished, activityAt, agentStarted sql.NullTime
				var activity, outcome sql.NullString
				if err := rows.Scan(&id, &started, &finished, &activity, &activityAt, &agentStarted, &outcome); err != nil {
					return err
				}
				span := e.Spans[id]
				if span.StartedAt == nil || started.Before(*span.StartedAt) {
					sCopy := started
					span.StartedAt = &sCopy
					e.Spans[id] = span
				}
				if needStall {
					st := e.StateTimes[id]
					if !finished.Valid {
						sCopy := started
						st.LiveRunStartedAt = &sCopy
					} else if st.LastRunFinishedAt == nil || finished.Time.After(*st.LastRunFinishedAt) {
						fCopy := finished.Time
						st.LastRunFinishedAt = &fCopy
					}
					e.StateTimes[id] = st
				}
				if !finished.Valid && activity.Valid {
					e.Activity[id] = RunActivity{
						Note:    activity.String,
						At:      timePtr(activityAt),
						BySetup: !agentStarted.Valid,
					}
				}
				if rn, ok := e.RunNow[id]; ok && !rn.After(started) {
					e.RunNowStale[id] = true
				}
				isOOM := finished.Valid && outcome.Valid && outcome.String == OOMOutcome
				cur, hasCur := latestRun[id]
				switch {
				case !hasCur || started.After(cur.startedAt):
					info := latestRunInfo{startedAt: started, hasOOM: isOOM}
					if isOOM {
						info.oomEndedAt = finished.Time
					}
					latestRun[id] = info
				case started.Equal(cur.startedAt) && isOOM:
					cur.hasOOM = true
					cur.oomEndedAt = finished.Time
					latestRun[id] = cur
				}
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	for id, info := range latestRun {
		if info.hasOOM {
			e.OOMRetrying[id] = info.oomEndedAt
		}
	}

	// 3. Pending question comments & TurnsStopped (PK lookup by comment id).
	if len(pendingQCommentIDs) > 0 {
		for start := 0; start < len(pendingQCommentIDs); start += taskQueryBatchChunk {
			chunk := pendingQCommentIDs[start:min(start+taskQueryBatchChunk, len(pendingQCommentIDs))]
			args := make([]any, len(chunk))
			for i, cid := range chunk {
				args[i] = cid
			}
			err := each(ctx, s.db,
				"SELECT `id`,`summary`,`body` FROM `task_comment` WHERE `id` IN ("+inPlaceholders(len(chunk))+")",
				args, func(rows *sql.Rows) error {
					var cid int64
					var summary, body string
					if err := rows.Scan(&cid, &summary, &body); err != nil {
						return err
					}
					stopped := body == TurnStoppedComment
					if stopped {
						summary = ""
					} else if summary == "" {
						summary = DeriveQuestionSummary(body)
					}
					for _, tid := range pendingQCommentToTasks[cid] {
						if stopped {
							e.TurnsStopped[tid] = true
						}
						e.PendingQuestions[tid] = PendingQuestion{CommentID: cid, Summary: summary}
					}
					return nil
				})
			if err != nil {
				return nil, err
			}
		}
	}

	// 4. NoteSummaries and UndeliveredPersonComments ONLY for awaiting_reply tasks on this page.
	if len(awaitingReplyIDs) > 0 {
		for start := 0; start < len(awaitingReplyIDs); start += taskQueryBatchChunk {
			chunk := awaitingReplyIDs[start:min(start+taskQueryBatchChunk, len(awaitingReplyIDs))]
			err := each(ctx, s.db,
				"SELECT `c`.`task_id`, `c`.`summary` FROM `task_comment` AS `c`"+
					" WHERE `c`.`task_id` IN ("+inPlaceholders(len(chunk))+") AND "+noteSummaryWhere,
				stringArgs(chunk), func(rows *sql.Rows) error {
					var id, summary string
					if err := rows.Scan(&id, &summary); err != nil {
						return err
					}
					e.NoteSummaries[id] = summary
					return nil
				})
			if err != nil {
				return nil, err
			}
		}
	}
	if len(deliveredByTask) > 0 {
		delivIDs := make([]string, 0, len(deliveredByTask))
		for id := range deliveredByTask {
			delivIDs = append(delivIDs, id)
		}
		for start := 0; start < len(delivIDs); start += taskQueryBatchChunk {
			chunk := delivIDs[start:min(start+taskQueryBatchChunk, len(delivIDs))]
			args := append([]any{string(PrincipalHuman), string(PrincipalService), string(PrincipalHuman)}, stringArgs(chunk)...)
			err := each(ctx, s.db,
				"SELECT `c`.`task_id`, `c`.`id`, `c`.`author_kind`, `c`.`author_id`, `c`.`author_behalf_kind`, `c`.`author_behalf_id`"+
					" FROM `task_comment` AS `c`"+
					" WHERE (`c`.`author_kind` = ? OR (`c`.`author_kind` = ? AND `c`.`author_behalf_kind` = ?))"+
					" AND `c`.`task_id` IN ("+inPlaceholders(len(chunk))+") ORDER BY `c`.`id`",
				args, func(rows *sql.Rows) error {
					var tid, kind, actor string
					var cid int64
					var behalfKind, behalfID sql.NullString
					if err := rows.Scan(&tid, &cid, &kind, &actor, &behalfKind, &behalfID); err != nil {
						return err
					}
					lim := deliveredByTask[tid]
					if cid <= lim.delivered {
						return nil
					}
					if lim.pendingQ != nil && cid <= *lim.pendingQ {
						return nil
					}
					a := Attribution{Actor: Principal{Kind: PrincipalKind(kind), ID: actor}}
					if behalfKind.Valid {
						a.OnBehalfOf = &Principal{Kind: PrincipalKind(behalfKind.String), ID: behalfID.String}
					}
					if p := PersonOf(a); p != nil {
						e.Undelivered[tid] = *p
					}
					return nil
				})
			if err != nil {
				return nil, err
			}
		}
	}

	// 5. Scoped GrantWaits (queued tasks only), Pauses, and GoalKinds.
	if len(queuedIDs) > 0 {
		for start := 0; start < len(queuedIDs); start += taskQueryBatchChunk {
			chunk := queuedIDs[start:min(start+taskQueryBatchChunk, len(queuedIDs))]
			err := each(ctx, s.db,
				"SELECT `task_id`,`capability`,`resource`,`waiting_at` FROM `task_grant_wait` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
				stringArgs(chunk), func(rows *sql.Rows) error {
					var w GrantWait
					if err := rows.Scan(&w.TaskID, &w.Capability, &w.Resource, &w.WaitingAt); err != nil {
						return err
					}
					e.GrantWaits[w.TaskID] = w
					return nil
				})
			if err != nil {
				return nil, err
			}
		}
	}
	for start := 0; start < len(ids); start += taskQueryBatchChunk {
		chunk := ids[start:min(start+taskQueryBatchChunk, len(ids))]
		in := inPlaceholders(len(chunk))
		args := stringArgs(chunk)
		if err := each(ctx, s.db,
			"SELECT "+pauseColumns+" FROM `task_pause` WHERE `task_id` IN ("+in+")",
			args, func(rows *sql.Rows) error {
				p, err := scanPause(rows)
				if err != nil {
					return err
				}
				e.Pauses[p.TaskID] = p
				return nil
			}); err != nil {
			return nil, err
		}
		if err := each(ctx, s.db,
			"SELECT `task_id`,`kind` FROM `task_goal` WHERE `task_id` IN ("+in+")",
			args, func(rows *sql.Rows) error {
				var id, kind string
				if err := rows.Scan(&id, &kind); err != nil {
					return err
				}
				e.GoalKinds[id] = GoalKind(kind)
				return nil
			}); err != nil {
			return nil, err
		}
	}

	// 6. Batched BlockerHolds for open dependencies of queued tasks on this page.
	if len(queuedIDs) > 0 {
		blockerSet := map[string]bool{}
		var blockerIDs []string
		for _, qid := range queuedIDs {
			t := taskByID[qid]
			for _, l := range t.Links {
				if l.Kind.Blocks() && e.States[l.Target] != StateClosed && !blockerSet[l.Target] {
					blockerSet[l.Target] = true
					blockerIDs = append(blockerIDs, l.Target)
				}
			}
		}
		if len(blockerIDs) > 0 {
			holds, err := s.blockerHoldsBatch(ctx, blockerIDs, taskByID, e.States, e.Pauses, e.GoalKinds)
			if err != nil {
				return nil, err
			}
			e.BlockerHolds = holds
		}
	}

	return e, nil
}

// blockerHoldsBatch evaluates BlockerHoldOf for blockerIDs in batched queries
// instead of per-blocker GetTask/State/GetPause round-trips.
func (s *Store) blockerHoldsBatch(
	ctx context.Context,
	blockerIDs []string,
	knownTasks map[string]Task,
	states map[string]State,
	knownPauses map[string]TaskPause,
	knownGoals map[string]GoalKind,
) (map[string]BlockerHold, error) {
	// Fetch light Task fields for blockers not already on the page.
	blockers := make(map[string]Task, len(blockerIDs))
	var fetchIDs []string
	for _, id := range blockerIDs {
		if t, ok := knownTasks[id]; ok {
			blockers[id] = t
		} else {
			fetchIDs = append(fetchIDs, id)
		}
	}
	for start := 0; start < len(fetchIDs); start += taskQueryBatchChunk {
		chunk := fetchIDs[start:min(start+taskQueryBatchChunk, len(fetchIDs))]
		err := each(ctx, s.db,
			"SELECT `id`,`deferred_until` FROM `task` WHERE `id` IN ("+inPlaceholders(len(chunk))+")",
			stringArgs(chunk), func(rows *sql.Rows) error {
				var id string
				var deferredUntil sql.NullTime
				if err := rows.Scan(&id, &deferredUntil); err != nil {
					return err
				}
				blockers[id] = Task{ID: id, DeferredUntil: timePtr(deferredUntil)}
				return nil
			})
		if err != nil {
			return nil, err
		}
		// NeverCompletes inspects console-session:* tags on blockers.
		args := append([]any{ConsoleSessionTagPrefix + "%"}, stringArgs(chunk)...)
		if err := each(ctx, s.db,
			"SELECT `task_id`,`tag` FROM `task_tag` WHERE `tag` LIKE ? AND `task_id` IN ("+inPlaceholders(len(chunk))+")",
			args, func(rows *sql.Rows) error {
				var id, tag string
				if err := rows.Scan(&id, &tag); err != nil {
					return err
				}
				if b, ok := blockers[id]; ok {
					b.Tags = append(b.Tags, tag)
					blockers[id] = b
				}
				return nil
			}); err != nil {
			return nil, err
		}
	}

	paused := make(map[string]bool, len(blockerIDs))
	for _, id := range blockerIDs {
		if _, ok := knownPauses[id]; ok {
			paused[id] = true
		}
	}
	if len(fetchIDs) > 0 {
		for start := 0; start < len(fetchIDs); start += taskQueryBatchChunk {
			chunk := fetchIDs[start:min(start+taskQueryBatchChunk, len(fetchIDs))]
			if err := each(ctx, s.db,
				"SELECT `task_id` FROM `task_pause` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
				stringArgs(chunk), func(rows *sql.Rows) error {
					var id string
					if err := rows.Scan(&id); err != nil {
						return err
					}
					paused[id] = true
					return nil
				}); err != nil {
				return nil, err
			}
		}
	}

	hasGoal := make(map[string]bool)
	var needGoalAndResume []string
	for _, id := range blockerIDs {
		b, ok := blockers[id]
		if !ok {
			continue
		}
		if states[id] == StateDeferred && b.DeferredUntil == nil {
			if _, ok := knownGoals[id]; ok {
				hasGoal[id] = true
			} else {
				needGoalAndResume = append(needGoalAndResume, id)
			}
		}
	}
	var resumeHeld map[string]bool
	if len(needGoalAndResume) > 0 {
		for start := 0; start < len(needGoalAndResume); start += taskQueryBatchChunk {
			chunk := needGoalAndResume[start:min(start+taskQueryBatchChunk, len(needGoalAndResume))]
			if err := each(ctx, s.db,
				"SELECT `task_id` FROM `task_goal` WHERE `task_id` IN ("+inPlaceholders(len(chunk))+")",
				stringArgs(chunk), func(rows *sql.Rows) error {
					var id string
					if err := rows.Scan(&id); err != nil {
						return err
					}
					hasGoal[id] = true
					return nil
				}); err != nil {
				return nil, err
			}
		}
	}

	out := map[string]BlockerHold{}
	for _, id := range blockerIDs {
		b, ok := blockers[id]
		if !ok {
			continue
		}
		st := states[id]
		if st == StateClosed {
			continue
		}
		if st == StateDeferred && b.DeferredUntil == nil && resumeHeld == nil {
			var err error
			if resumeHeld, err = s.ResumeHeldTasks(ctx); err != nil {
				return nil, err
			}
		}
		if hold, ok := BlockerHoldOf(b, st, paused[id], hasGoal[id], resumeHeld[id]); ok {
			out[id] = hold
		}
	}
	return out, nil
}

// ListInboxTasks queries ONLY the indexed sets that can produce GET /api/inbox
// task items (awaiting_reply, proposed, awaiting_submit, and merge-held open
// tasks), avoiding a whole-store task list scan.
func (s *Store) ListInboxTasks(ctx context.Context, filter TaskListFilter, kind string) ([]Task, *TaskListEnrichment, error) {
	if !filter.allowsAnyState() || kind == "notification" {
		return nil, &TaskListEnrichment{States: map[string]State{}}, nil
	}

	var parts []string
	hasCompletedOpen := false
	addCompletedOpen := func() {
		if !hasCompletedOpen {
			hasCompletedOpen = true
			parts = append(parts, "SELECT `task_id` AS `id` FROM `task_observation` WHERE `closed_at` IS NULL AND `completed_at` IS NOT NULL")
		}
	}
	if kind == "" || kind == "question" || kind == "secret" {
		parts = append(parts, "SELECT `task_id` AS `id` FROM `task_observation` WHERE `closed_at` IS NULL AND `pending_question_comment_id` IS NOT NULL")
		addCompletedOpen()
	}
	if kind == "" || kind == "proposal" {
		parts = append(parts, "SELECT `id` FROM `task` WHERE `approval_actor_kind` IS NULL")
	}
	if kind == "" || kind == "merge-held" {
		parts = append(parts,
			"SELECT `task_id` AS `id` FROM `task_observation` WHERE `closed_at` IS NULL AND (`merge_queue_blocked_at` IS NOT NULL OR `merge_queue_stalled_at` IS NOT NULL)",
			"SELECT `task_id` AS `id` FROM `task_link` WHERE `kind` IN ('"+string(LinkHumanReviewHold)+"','"+string(LinkSecurityHold)+"','"+string(LinkPathTriggerHold)+"')",
		)
		addCompletedOpen()
	}
	if len(parts) == 0 {
		return nil, &TaskListEnrichment{States: map[string]State{}}, nil
	}

	f := filter
	f.OpenOnly = true
	f.States = nil
	conds, args := appendTaskFilterClauses(nil, nil, f, true)

	query := "WITH `cands`(`id`) AS (" + strings.Join(parts, " UNION ") + ") " +
		"SELECT `t`.`id`, `t`.`created_at`, " + taskStateCaseSQL +
		" FROM `cands`" +
		" JOIN `task` AS `t` ON `t`.`id` = `cands`.`id`" +
		" LEFT JOIN `task_observation` AS `o` ON `o`.`task_id` = `t`.`id`" +
		" LEFT JOIN `task_run` AS `r` ON `r`.`task_id` = `t`.`id` AND `r`.`finished_at` IS NULL" +
		whereClause(conds)

	var entries []filingEntry
	if err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var id, st string
		var created sql.NullTime
		if err := rows.Scan(&id, &created, &st); err != nil {
			return err
		}
		entries = append(entries, makeFilingEntry(id, created, State(st)))
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("listing inbox tasks: %w", err)
	}
	if len(entries) == 0 {
		return nil, &TaskListEnrichment{States: map[string]State{}}, nil
	}
	slices.SortFunc(entries, compareFilingAsc)

	ids := make([]string, len(entries))
	states := make(map[string]State, len(entries))
	for i, e := range entries {
		ids[i] = e.id
		states[e.id] = e.state
	}
	tasks, err := s.fetchTasksByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	enrich, err := s.TaskEnrichmentFor(ctx, tasks, states, false)
	if err != nil {
		return nil, nil, err
	}
	return tasks, enrich, nil
}
