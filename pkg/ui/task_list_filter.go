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
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// GET /api/tasks narrowed for a caller that is not the UI.
//
// The UI polls the whole list and is answered from taskListCache, ETag
// and all. An agent calling the route through `grain api` wants a few
// rows, and was handed every task the deployment had ever filed with its
// full description -- 9.6 MB on a deployment with two thousand tasks --
// whatever ?state= or ?limit= it asked for (grain task 1945). A request
// naming any of state, limit, q (or search) or fields takes this path
// instead; one naming none of them, or only repo, is answered from the
// cache.
//
// What that cached answer holds changed in grain task 2983: it is the
// live list -- every task that is not closed, without descriptions -- and
// ?fields=full (alone, or with repo) is the whole list with every field,
// as the route answered before. Closed tasks are paged in on demand with
// ?state=closed&cursor= (and limit, search, closedSince, closedBefore),
// newest-closed first.

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

// taskListPageDefault is a page's size when the caller names no limit.
const taskListPageDefault = 100

// taskListPage is a paged GET /api/tasks' answer: one page of tasks, the
// cursor that asks for the next (empty on the last), and how many tasks
// match across every page. The totals are what let a reader holding only
// the first pages -- the UI's rail, counting closed work -- say how much
// there is without reading it all: Total every matching task, Sessions
// the console and interactive sessions' own (which the rail counts under
// Sessions), and Merged the rest whose pull request merged (PrMergedAt;
// the rail's Merged entry rather than Closed).
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

func (q taskListQuery) matches(t Task) bool {
	if q.repo != "" && !strings.EqualFold(t.Repo, q.repo) {
		return false
	}
	if len(q.states) > 0 {
		found := false
		for _, st := range q.states {
			if strings.EqualFold(string(t.State), st) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q.closedSince != nil || q.closedBefore != nil {
		if t.State != model.StateClosed {
			return false
		}
		at := closedTime(t)
		if q.closedSince != nil && at.Before(*q.closedSince) {
			return false
		}
		if q.closedBefore != nil && !at.Before(*q.closedBefore) {
			return false
		}
	}
	if q.search != "" {
		// The title, the id, the external reference and the description:
		// the live list no longer carries descriptions, so this is where a
		// search over them is answered. The external reference ("CA-204")
		// is what somebody pasting a ticket's name into the search box is
		// reaching for, and the UI's own search box matches it on the
		// tasks it holds (ui/src/taskFilters.js's matchesFilters).
		needle := strings.ToLower(q.search)
		idNeedle := strings.TrimPrefix(needle, "#")
		if !strings.Contains(strings.ToLower(t.Title), needle) &&
			!strings.Contains(strings.ToLower(t.Description), needle) &&
			!strings.Contains(strings.ToLower(t.Branch), needle) &&
			!strings.Contains(strings.ToLower(t.ExternalRef), needle) &&
			!strings.Contains(strings.ToLower(t.ID), idNeedle) {
			return false
		}
	}
	if q.derivedFrom != "" && !TaskDerivedFrom(t, q.derivedFrom) {
		return false
	}
	if q.filedVia != "" && !strings.Contains(strings.ToLower(t.FiledVia), strings.ToLower(q.filedVia)) {
		return false
	}
	if q.externalRef != "" && t.ExternalRef != q.externalRef {
		return false
	}
	if q.owner != "" && !strings.EqualFold(t.Owner, q.owner) {
		return false
	}
	return true
}

// TaskDerivedFrom reports whether t records that its work was derived
// from the pull request ref names (owner/name#number or its URL): a
// model.LinkDerivedFrom link, or a "Derived from: <ref>" line in its
// description, which is how a task filed by hand says the same thing.
// `grain list -filter derived_from:eq:<ref>` asks it of every task.
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
	for _, line := range strings.Split(t.Description, "\n") {
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

// handleFilteredTaskList answers a narrowed GET /api/tasks: the matching
// tasks, the newest limit of them by filing time, oldest first.
func (s *Server) handleFilteredTaskList(w http.ResponseWriter, r *http.Request, q taskListQuery) {
	all, err := s.taskList.list(r.Context(), s.tasks)
	if err != nil {
		writeClientError(w, err)
		return
	}
	readable := s.taskReadable(r)
	matched := make([]Task, 0, len(all))
	for _, t := range all {
		if readable(t.Repo) && q.matches(t) {
			matched = append(matched, t)
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	if q.paged {
		page := pageTasks(matched, q)
		if q.summary {
			out := taskListPage[taskListSummary]{
				Tasks:      make([]taskListSummary, 0, len(page.Tasks)),
				NextCursor: page.NextCursor,
				Truncated:  page.Truncated,
				Total:      page.Total,
				Merged:     page.Merged,
				Sessions:   page.Sessions,
			}
			for _, t := range page.Tasks {
				out.Tasks = append(out.Tasks, taskListSummary{
					ID: t.ID, Title: t.Title, State: t.State, Repo: t.Repo, ExternalRef: t.ExternalRef,
					CreatedAt: t.CreatedAt, BlockedBy: t.BlockedBy, Stall: t.Stall,
				})
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	sortTasksByFiling(matched)
	if q.limit > 0 && len(matched) > q.limit {
		matched = matched[len(matched)-q.limit:]
	}
	if q.list {
		for i := range matched {
			matched[i] = listRow(matched[i])
		}
	}
	if !q.summary {
		writeJSON(w, http.StatusOK, matched)
		return
	}
	out := make([]taskListSummary, 0, len(matched))
	for _, t := range matched {
		out = append(out, taskListSummary{
			ID: t.ID, Title: t.Title, State: t.State, Repo: t.Repo, ExternalRef: t.ExternalRef,
			CreatedAt: t.CreatedAt, BlockedBy: t.BlockedBy, Stall: t.Stall,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// pageTasks is the page of matched that q's cursor and limit ask for,
// newest-closed first. A task's place in that order is its closing time
// (closedTime: its ending or filing time when it has none) and then its id, which is what the
// cursor names: a cursor is the place of the last task a page held, so
// the next page starts after it even when that task has since reopened
// or been deleted.
func pageTasks(matched []Task, q taskListQuery) taskListPage[Task] {
	keys := make(map[string]taskPageKey, len(matched))
	for _, t := range matched {
		keys[t.ID] = pageKeyOf(t)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return keys[matched[i].ID].before(keys[matched[j].ID])
	})
	start := 0
	if after, ok := parsePageCursor(q.cursor); ok {
		start = sort.Search(len(matched), func(i int) bool {
			return after.before(keys[matched[i].ID])
		})
	}
	end := len(matched)
	if !q.limitAll {
		limit := q.limit
		if limit <= 0 {
			limit = taskListPageDefault
		}
		end = min(start+limit, len(matched))
	}
	page := taskListPage[Task]{Tasks: make([]Task, 0, end-start), Total: len(matched)}
	for _, t := range matched {
		switch {
		case t.ConsoleSession != "" || t.Interactive:
			page.Sessions++
		case t.PrMergedAt != nil:
			page.Merged++
		}
	}
	for _, t := range matched[start:end] {
		if !q.summary && !q.full {
			t = listRow(t)
			if n := len(t.Reads); n > taskPageRowReads {
				t.Reads = append([]string(nil), t.Reads[:taskPageRowReads]...)
				t.ReadCount, t.ReadsTruncated = n, true
			}
		}
		page.Tasks = append(page.Tasks, t)
	}
	if end < len(matched) && end > start {
		page.Truncated = true
		page.NextCursor = keys[matched[end-1].ID].String()
	}
	return page
}

// taskPageRowReads is how many of a task's Reads a paged list row
// carries (Task.ReadsTruncated); a director reading 260 repositories made
// one row 7.8 KB and its page 14 KB.
const taskPageRowReads = 10

// taskPageKey is a task's place in a page's order.
type taskPageKey struct {
	at    time.Time
	id    int64
	isNum bool
	rawID string
}

func pageKeyOf(t Task) taskPageKey {
	k := taskPageKey{at: closedTime(t), rawID: t.ID}
	if n, err := strconv.ParseInt(t.ID, 10, 64); err == nil {
		k.id, k.isNum = n, true
	}
	return k
}

// before reports whether k comes before o: later times first, and at the
// same time the higher numeric id (or lexicographically higher rawID) first.
func (k taskPageKey) before(o taskPageKey) bool {
	if !k.at.Equal(o.at) {
		return k.at.After(o.at)
	}
	if k.isNum && o.isNum && k.id != o.id {
		return k.id > o.id
	}
	if k.isNum != o.isNum {
		return k.isNum
	}
	return k.rawID > o.rawID
}

// zeroPageTime is a cursor's time for a task with no closing or filing
// time. time.Time{}.UnixNano() is outside int64's range (it answers
// -6795364578871345152, which reads back as 1754), so such a cursor read
// back as a later time than the task it named and the next page started
// at the same task again: a walk of the list never ended.
const zeroPageTime = "z"

func (k taskPageKey) String() string {
	if k.at.IsZero() {
		return zeroPageTime + "." + k.rawID
	}
	return strconv.FormatInt(k.at.UnixNano(), 10) + "." + k.rawID
}

func parsePageCursor(s string) (taskPageKey, bool) {
	at, rawID, ok := strings.Cut(s, ".")
	if !ok || rawID == "" || strings.ContainsAny(rawID, " \t\r\n") {
		return taskPageKey{}, false
	}
	k := taskPageKey{rawID: rawID}
	if at != zeroPageTime {
		ns, err := strconv.ParseInt(at, 10, 64)
		if err != nil {
			return taskPageKey{}, false
		}
		k.at = time.Unix(0, ns)
	}
	if n, err := strconv.ParseInt(rawID, 10, 64); err == nil {
		k.id, k.isNum = n, true
	}
	return k, true
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

// list is every task, from the cache when the store says nothing has been
// committed since it was built and from the store otherwise. The copy is
// the caller's to reorder.
func (c *taskListCache) list(ctx context.Context, client *Client) ([]Task, error) {
	token := ""
	if client.Store != nil {
		if t, err := client.Store.ChangeToken(ctx); err == nil {
			token = t
		}
	}
	if token != "" {
		c.mu.Lock()
		cached := c.token == token && c.tasks != nil
		tasks := c.tasks
		c.mu.Unlock()
		if cached {
			return append([]Task(nil), tasks...), nil
		}
	}
	c.builds.Add(1)
	tasks, err := client.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	if token != "" {
		c.mu.Lock()
		if c.token != token {
			c.token, c.tasks, c.bodies = token, tasks, map[string]taskListBody{}
		}
		c.mu.Unlock()
	}
	return append([]Task(nil), tasks...), nil
}
