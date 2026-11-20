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
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

// GET /api/tasks narrowed and/or paged in SQL.
//
// Narrowed and paged requests push state, repo, q, limit, cursor, owner,
// externalRef, filed_via, closedSince, closedBefore, fields=summary, and
// caller repository scope into SQL (pkg/model/task_list_query.go) so only
// the returned page is hydrated and enriched.

// taskListQuery is GET /api/tasks' narrowing.
type taskListQuery struct {
	repo     string
	owner    string
	states   []string
	search   string
	limit    int
	limitAll bool
	summary  bool
	// derivedFrom and filedVia are the external-cli spec's provenance
	// filters (#XC-18): ?derived_from=<owner/name#pr or its URL> and
	// ?filed_via=<substring>.
	derivedFrom string
	filedVia    string
	externalRef string
	// list leaves descriptions out, as the live list does (fields=list).
	list bool
	// paged answers a taskListPage, newest-closed first, starting after
	// cursor (empty for the first page).
	paged  bool
	cursor string
	// closedSince and closedBefore keep only closed tasks, closed in
	// [since, before) by closedTime.
	closedSince, closedBefore *time.Time
	// full is ?fields=full with nothing else narrowing it: the cached
	// whole list rather than a filtered one.
	full bool
}

const (
	// taskListPageDefault is a paged GET /api/tasks' size when no limit is named.
	taskListPageDefault = 100
	// personTaskListLiveDefault bounds unnarrowed person GET /api/tasks polls
	// so deployments with thousands of open/failed tasks never serialize
	// megabytes per tick while leaving normal deployments' full open set intact.
	personTaskListLiveDefault = 500
	// personTaskListPageDefault is the default cap for narrowed non-paged
	// person queries (?state=..., ?q=...) when limit is omitted (?limit=0
	// explicitly asks for all matching items).
	personTaskListPageDefault = 200
)

// taskListPage is a paged GET /api/tasks' answer: one page of tasks, the
// cursor that asks for the next (empty on the last), and how many tasks
// match across every page.
type taskListPage[T any] struct {
	Tasks      []T    `json:"tasks"`
	NextCursor string `json:"nextCursor,omitempty"`
	Truncated  bool   `json:"truncated"`
	Total      int    `json:"total"`
	Merged     int    `json:"merged"`
	Sessions   int    `json:"sessions"`
}

// parseTaskListQuery reads q, and reports whether it asks for anything the
// cached whole-list answer does not give.
func parseTaskListQuery(q url.Values) (taskListQuery, bool) {
	var out taskListQuery
	narrowed := false
	out.repo = strings.TrimSpace(q.Get("repo"))
	for _, v := range q["state"] {
		narrowed = true
		for _, st := range strings.Split(v, ",") {
			if st = strings.TrimSpace(st); st != "" {
				out.states = append(out.states, st)
			}
		}
	}
	if q.Has("q") || q.Has("search") {
		narrowed = true
		out.search = strings.TrimSpace(q.Get("search"))
		if out.search == "" {
			out.search = strings.TrimSpace(q.Get("q"))
		}
	}
	if q.Has("limit") {
		narrowed = true
		if text := strings.TrimSpace(q.Get("limit")); text != "" {
			if n, err := strconv.Atoi(text); err == nil {
				if n == 0 {
					out.limitAll = true
				} else if n > 0 {
					out.limit = n
				}
			}
		}
	}
	if q.Has("derived_from") {
		narrowed = true
		out.derivedFrom = strings.TrimSpace(q.Get("derived_from"))
	}
	if q.Has("filed_via") {
		narrowed = true
		out.filedVia = strings.TrimSpace(q.Get("filed_via"))
	}
	if extRef := strings.TrimSpace(q.Get("externalRef")); extRef != "" {
		narrowed = true
		out.externalRef = extRef
	}
	if q.Has("owner") {
		narrowed = true
		out.owner = strings.TrimSpace(q.Get("owner"))
	}
	if q.Has("closedSince") {
		narrowed = true
		out.closedSince = parseListTime(q.Get("closedSince"))
	}
	if q.Has("closedBefore") {
		narrowed = true
		out.closedBefore = parseListTime(q.Get("closedBefore"))
	}
	if q.Has("cursor") {
		narrowed = true
		out.paged = true
		out.cursor = strings.TrimSpace(q.Get("cursor"))
	}
	fields := strings.ToLower(strings.TrimSpace(q.Get("fields")))
	out.summary = fields == "summary"
	out.list = fields == "list"
	out.full = fields == "full"
	if q.Has("fields") && !narrowed && out.full {
		return out, false
	}
	if q.Has("fields") {
		narrowed = true
	}
	return out, narrowed
}

// parseListTime is an RFC 3339 time, or nil for anything else.
func parseListTime(s string) *time.Time {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return &at
}

// openOnly reports whether q can match no closed task -- it names
// states and closed is not one of them.
func (q taskListQuery) openOnly() bool {
	if len(q.states) == 0 || q.closedSince != nil || q.closedBefore != nil {
		return false
	}
	for _, st := range q.states {
		if strings.EqualFold(st, string(model.StateClosed)) {
			return false
		}
	}
	return true
}

// canIncludeRunning reports whether q's response could contain a running task
// whose live Activity field might change between structural writes.
func (q taskListQuery) canIncludeRunning() bool {
	if q.summary || q.closedSince != nil || q.closedBefore != nil {
		return false
	}
	if len(q.states) == 0 {
		return true
	}
	for _, st := range q.states {
		if strings.EqualFold(st, string(model.StateRunning)) {
			return true
		}
	}
	return false
}

// TaskDerivedFrom reports whether t records that its work was derived
// from the pull request ref names (owner/name#number or its URL): a
// model.LinkDerivedFrom link, or a "Derived from: <ref>" line in its
// description, which is how a task filed by hand says the same thing.
func TaskDerivedFrom(t Task, ref string) bool {
	want, ok := NormalizePullRequestRef(ref)
	if !ok {
		want = strings.TrimSpace(ref)
	}
	for _, d := range t.DerivedFrom {
		if strings.EqualFold(d, want) {
			return true
		}
	}
	return descriptionDerivedFrom(t.Description, want)
}

func modelTaskDerivedFrom(t model.Task, want string) bool {
	for _, l := range t.Links {
		if l.Kind == model.LinkDerivedFrom && strings.EqualFold(l.Target, want) {
			return true
		}
	}
	return descriptionDerivedFrom(t.Body, want)
}

func descriptionDerivedFrom(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "Derived from:")
		if !found {
			continue
		}
		for _, word := range strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
			word = strings.Trim(word, "()[]<>`*_.")
			if got, ok := NormalizePullRequestRef(word); ok && strings.EqualFold(got, want) {
				return true
			}
		}
	}
	return false
}

// taskListSummary is one task under fields=summary: what picks a row out
// of a list, and none of what is only worth reading once it is picked.
type taskListSummary struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	State       model.State `json:"state"`
	Repo        string      `json:"repo,omitempty"`
	ExternalRef string      `json:"externalRef,omitempty"`
	CreatedAt   *time.Time  `json:"createdAt,omitempty"`
	BlockedBy   []string    `json:"blockedBy,omitempty"`
	Stall       string      `json:"stall,omitempty"`
}

// resolveTaskRepoScope translates s.taskReadable(r) into a SQL-pushable
// repository scope and deterministic cache key component.
func (s *Server) resolveTaskRepoScope(r *http.Request, requestedRepo string) (useScope bool, allowed []model.RepoRef, allowEmpty bool, scopeKey string, empty bool, err error) {
	if !taskReadScoped(r) {
		return false, nil, false, "all", false, nil
	}
	readable := s.taskReadable(r)
	if requestedRepo != "" {
		if !readable(requestedRepo) {
			return true, nil, false, "none", true, nil
		}
		return false, nil, false, "repo:" + strings.ToLower(requestedRepo), false, nil
	}
	distinct, hasEmpty, err := s.tasks.Store.DistinctTaskRepos(r.Context())
	if err != nil {
		return false, nil, false, "", false, err
	}
	if hasEmpty && readable("") {
		allowEmpty = true
	}
	var names []string
	for _, repo := range distinct {
		name := repo.String()
		if readable(name) {
			allowed = append(allowed, repo)
			names = append(names, strings.ToLower(name))
		}
	}
	if len(allowed) == 0 && !allowEmpty {
		return true, nil, false, "none", true, nil
	}
	sort.Strings(names)
	var b strings.Builder
	if allowEmpty {
		b.WriteString("empty,")
	}
	b.WriteString(strings.Join(names, ","))
	return true, allowed, allowEmpty, b.String(), false, nil
}

func isASCIIFilter(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func buildModelTaskFilter(q taskListQuery, useScope bool, allowed []model.RepoRef, allowEmpty bool) model.TaskListFilter {
	f := model.TaskListFilter{
		OpenOnly:       q.openOnly(),
		Repo:           q.repo,
		UseRepoScope:   useScope,
		AllowedRepos:   allowed,
		AllowEmptyRepo: allowEmpty,
		Search:         q.search,
		FiledVia:       q.filedVia,
		ExternalRef:    q.externalRef,
		Owner:          q.owner,
		ClosedSince:    q.closedSince,
		ClosedBefore:   q.closedBefore,
	}
	for _, st := range q.states {
		f.States = append(f.States, model.State(strings.ToLower(st)))
	}
	var wantDerived string
	if q.derivedFrom != "" {
		if norm, ok := NormalizePullRequestRef(q.derivedFrom); ok {
			wantDerived = norm
		} else {
			wantDerived = strings.TrimSpace(q.derivedFrom)
		}
		f.DerivedFrom = wantDerived
	}
	needNonASCIISearch := q.search != "" && !isASCIIFilter(q.search)
	needNonASCIIFiledVia := q.filedVia != "" && !isASCIIFilter(q.filedVia)
	if wantDerived != "" || needNonASCIISearch || needNonASCIIFiledVia {
		needle := strings.ToLower(q.search)
		idNeedle := strings.TrimPrefix(needle, "#")
		filedVia := strings.ToLower(q.filedVia)
		f.PostMatch = func(t model.Task) bool {
			if wantDerived != "" && !modelTaskDerivedFrom(t, wantDerived) {
				return false
			}
			if needNonASCIIFiledVia && !strings.Contains(strings.ToLower(t.FiledVia), filedVia) {
				return false
			}
			if needNonASCIISearch {
				branch := ""
				if t.Target != nil {
					branch = t.BranchName()
				}
				if !strings.Contains(strings.ToLower(t.Title), needle) &&
					!strings.Contains(strings.ToLower(t.Body), needle) &&
					!strings.Contains(strings.ToLower(branch), needle) &&
					!strings.Contains(strings.ToLower(t.ExternalRef), needle) &&
					!strings.Contains(strings.ToLower(t.ID), idNeedle) {
					return false
				}
			}
			return true
		}
	}
	return f
}

func (q taskListQuery) filterCacheKey(scopeKey string) string {
	states := make([]string, len(q.states))
	for i, s := range q.states {
		states[i] = strings.ToLower(s)
	}
	slices.Sort(states)
	var b strings.Builder
	b.WriteString("scope=")
	b.WriteString(scopeKey)
	b.WriteString("|repo=")
	b.WriteString(strings.ToLower(q.repo))
	b.WriteString("|states=")
	b.WriteString(strings.Join(states, ","))
	b.WriteString("|q=")
	b.WriteString(q.search)
	b.WriteString("|owner=")
	b.WriteString(strings.ToLower(q.owner))
	b.WriteString("|ext=")
	b.WriteString(q.externalRef)
	b.WriteString("|via=")
	b.WriteString(strings.ToLower(q.filedVia))
	b.WriteString("|derived=")
	b.WriteString(strings.ToLower(q.derivedFrom))
	if q.closedSince != nil {
		b.WriteString("|since=")
		b.WriteString(strconv.FormatInt(q.closedSince.UnixNano(), 10))
	}
	if q.closedBefore != nil {
		b.WriteString("|before=")
		b.WriteString(strconv.FormatInt(q.closedBefore.UnixNano(), 10))
	}
	return b.String()
}

func (q taskListQuery) responseCacheKey(scopeKey string) string {
	var b strings.Builder
	b.WriteString(q.filterCacheKey(scopeKey))
	b.WriteString("|paged=")
	b.WriteString(strconv.FormatBool(q.paged))
	b.WriteString("|limit=")
	b.WriteString(strconv.Itoa(q.limit))
	b.WriteString("|all=")
	b.WriteString(strconv.FormatBool(q.limitAll))
	b.WriteString("|cur=")
	b.WriteString(q.cursor)
	b.WriteString("|sum=")
	b.WriteString(strconv.FormatBool(q.summary))
	b.WriteString("|list=")
	b.WriteString(strconv.FormatBool(q.list))
	b.WriteString("|full=")
	b.WriteString(strconv.FormatBool(q.full))
	return b.String()
}

type pagedRenderMeta struct {
	nextCursor string
	truncated  bool
	counts     model.TaskListCounts
}

func trimPagedRow(t Task, full bool) Task {
	if full {
		return t
	}
	t = listRow(t)
	if n := len(t.Reads); n > taskPageRowReads {
		t.Reads = append([]string(nil), t.Reads[:taskPageRowReads]...)
		t.ReadCount, t.ReadsTruncated = n, true
	}
	return t
}

// handleFilteredTaskList answers a narrowed or paged GET /api/tasks directly
// from SQL, loading and enriching only the returned page.
func (s *Server) handleFilteredTaskList(w http.ResponseWriter, r *http.Request, q taskListQuery) {
	useScope, allowed, allowEmpty, scopeKey, emptyScope, err := s.resolveTaskRepoScope(r, q.repo)
	if err != nil {
		writeClientError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if emptyScope {
		if q.paged {
			if q.summary {
				writeJSON(w, http.StatusOK, taskListPage[taskListSummary]{Tasks: []taskListSummary{}})
				return
			}
			writeJSON(w, http.StatusOK, taskListPage[Task]{Tasks: []Task{}})
			return
		}
		if q.summary {
			writeJSON(w, http.StatusOK, []taskListSummary{})
			return
		}
		writeJSON(w, http.StatusOK, []Task{})
		return
	}

	filter := buildModelTaskFilter(q, useScope, allowed, allowEmpty)
	filterKey := q.filterCacheKey(scopeKey)
	respKey := q.responseCacheKey(scopeKey)
	needsActivity := q.canIncludeRunning()

	var cursor *model.TaskPageKey
	if q.paged && q.cursor != "" {
		if parsed, ok := model.ParseTaskPageCursor(q.cursor); ok {
			cursor = &parsed
		}
	}

	pageLimit := q.limit
	if q.paged && !q.limitAll && pageLimit <= 0 {
		pageLimit = taskListPageDefault
	}

	body, err := s.taskList.filteredResponse(
		r.Context(),
		s.tasks,
		respKey,
		needsActivity,
		func() ([]Task, any, []byte, error) {
			if q.paged {
				if q.summary {
					if filter.PostMatch != nil {
						rows, nextCur, trunc, counts, err := s.tasks.QueryTaskPageSummaries(r.Context(), filter, cursor, pageLimit, q.limitAll, true)
						if err != nil {
							return nil, nil, nil, err
						}
						if rows == nil {
							rows = []taskListSummary{}
						}
						b, err := json.Marshal(taskListPage[taskListSummary]{
							Tasks: rows, NextCursor: nextCur, Truncated: trunc,
							Total: counts.Total, Merged: counts.Merged, Sessions: counts.Sessions,
						})
						return nil, nil, b, err
					}
					rows, nextCur, trunc, _, err := s.tasks.QueryTaskPageSummaries(r.Context(), filter, cursor, pageLimit, q.limitAll, false)
					if err != nil {
						return nil, nil, nil, err
					}
					counts, err := s.taskList.cachedCounts(s.tasks, filterKey, func() (model.TaskListCounts, error) {
						return s.tasks.Store.TaskCounts(r.Context(), filter)
					})
					if err != nil {
						return nil, nil, nil, err
					}
					if rows == nil {
						rows = []taskListSummary{}
					}
					b, err := json.Marshal(taskListPage[taskListSummary]{
						Tasks: rows, NextCursor: nextCur, Truncated: trunc,
						Total: counts.Total, Merged: counts.Merged, Sessions: counts.Sessions,
					})
					return nil, nil, b, err
				}

				if filter.PostMatch != nil {
					tasks, nextCur, trunc, counts, err := s.tasks.QueryTaskPage(r.Context(), filter, cursor, pageLimit, q.limitAll, true)
					if err != nil {
						return nil, nil, nil, err
					}
					for i := range tasks {
						tasks[i] = trimPagedRow(tasks[i], q.full)
					}
					if tasks == nil {
						tasks = []Task{}
					}
					meta := pagedRenderMeta{nextCursor: nextCur, truncated: trunc, counts: counts}
					b, err := json.Marshal(taskListPage[Task]{
						Tasks: tasks, NextCursor: nextCur, Truncated: trunc,
						Total: counts.Total, Merged: counts.Merged, Sessions: counts.Sessions,
					})
					return tasks, meta, b, err
				}
				tasks, nextCur, trunc, _, err := s.tasks.QueryTaskPage(r.Context(), filter, cursor, pageLimit, q.limitAll, false)
				if err != nil {
					return nil, nil, nil, err
				}
				counts, err := s.taskList.cachedCounts(s.tasks, filterKey, func() (model.TaskListCounts, error) {
					return s.tasks.Store.TaskCounts(r.Context(), filter)
				})
				if err != nil {
					return nil, nil, nil, err
				}
				for i := range tasks {
					tasks[i] = trimPagedRow(tasks[i], q.full)
				}
				if tasks == nil {
					tasks = []Task{}
				}
				meta := pagedRenderMeta{nextCursor: nextCur, truncated: trunc, counts: counts}
				b, err := json.Marshal(taskListPage[Task]{
					Tasks: tasks, NextCursor: nextCur, Truncated: trunc,
					Total: counts.Total, Merged: counts.Merged, Sessions: counts.Sessions,
				})
				return tasks, meta, b, err
			}

			// Non-paged narrowed query: newest limit tasks in oldest-first filing order.
			filingLimit := q.limit
			if q.limitAll {
				filingLimit = 0
			}
			if q.summary {
				rows, err := s.tasks.QueryTaskSummariesByFiling(r.Context(), filter, filingLimit)
				if err != nil {
					return nil, nil, nil, err
				}
				if rows == nil {
					rows = []taskListSummary{}
				}
				b, err := json.Marshal(rows)
				return nil, nil, b, err
			}
			tasks, err := s.tasks.QueryTasksByFiling(r.Context(), filter, filingLimit)
			if err != nil {
				return nil, nil, nil, err
			}
			if q.list {
				for i := range tasks {
					tasks[i] = listRow(tasks[i])
				}
			}
			if tasks == nil {
				tasks = []Task{}
			}
			b, err := json.Marshal(tasks)
			return tasks, nil, b, err
		},
		func(tasks []Task, meta any) ([]byte, error) {
			if m, ok := meta.(pagedRenderMeta); ok {
				return json.Marshal(taskListPage[Task]{
					Tasks:      tasks,
					NextCursor: m.nextCursor,
					Truncated:  m.truncated,
					Total:      m.counts.Total,
					Merged:     m.counts.Merged,
					Sessions:   m.counts.Sessions,
				})
			}
			return json.Marshal(tasks)
		},
	)
	if err != nil {
		writeClientError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// taskPageRowReads is how many of a task's Reads a paged list row
// carries (Task.ReadsTruncated); a director reading 260 repositories made
// one row 7.8 KB and its page 14 KB.
const taskPageRowReads = 10

func parsePageCursor(s string) (model.TaskPageKey, bool) {
	return model.ParseTaskPageCursor(s)
}

// sortTasksByFiling orders tasks oldest first by CreatedAt, then by the
// number their id ends in -- never as strings, which puts 999 after 1000.
// A task with no filing time sorts after every task that has one.
func sortTasksByFiling(tasks []Task) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		switch {
		case a.CreatedAt != nil && b.CreatedAt != nil:
			if !a.CreatedAt.Equal(*b.CreatedAt) {
				return a.CreatedAt.Before(*b.CreatedAt)
			}
		case a.CreatedAt != nil:
			return true
		case b.CreatedAt != nil:
			return false
		}
		na, erra := strconv.Atoi(a.ID)
		nb, errb := strconv.Atoi(b.ID)
		if erra == nil && errb == nil && na != nb {
			return na < nb
		}
		return a.ID < b.ID
	})
}
