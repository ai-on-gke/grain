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
// instead; one naming none of them, or only repo, is answered exactly as
// before.

// taskListQuery is GET /api/tasks' narrowing.
type taskListQuery struct {
	repo    string
	states  []string
	search  string
	limit   int
	summary bool
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
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			out.limit = n
		}
	}
	if q.Has("fields") {
		narrowed = true
		out.summary = strings.EqualFold(strings.TrimSpace(q.Get("fields")), "summary")
	}
	return out, narrowed
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
	if q.search != "" && !strings.Contains(strings.ToLower(t.Title), strings.ToLower(q.search)) {
		return false
	}
	return true
}

// taskListSummary is one task under fields=summary: what picks a row out
// of a list, and none of what is only worth reading once it is picked.
type taskListSummary struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	State     model.State `json:"state"`
	Repo      string      `json:"repo,omitempty"`
	CreatedAt *time.Time  `json:"createdAt,omitempty"`
	BlockedBy []string    `json:"blockedBy,omitempty"`
	Stall     string      `json:"stall,omitempty"`
}

// handleFilteredTaskList answers a narrowed GET /api/tasks: the matching
// tasks, the newest limit of them by filing time, oldest first.
func (s *Server) handleFilteredTaskList(w http.ResponseWriter, r *http.Request, q taskListQuery) {
	all, err := s.taskList.list(r.Context(), s.tasks)
	if err != nil {
		writeClientError(w, err)
		return
	}
	matched := make([]Task, 0, len(all))
	for _, t := range all {
		if q.matches(t) {
			matched = append(matched, t)
		}
	}
	sortTasksByFiling(matched)
	if q.limit > 0 && len(matched) > q.limit {
		matched = matched[len(matched)-q.limit:]
	}
	w.Header().Set("Cache-Control", "no-cache")
	if !q.summary {
		writeJSON(w, http.StatusOK, matched)
		return
	}
	out := make([]taskListSummary, 0, len(matched))
	for _, t := range matched {
		out = append(out, taskListSummary{
			ID: t.ID, Title: t.Title, State: t.State, Repo: t.Repo,
			CreatedAt: t.CreatedAt, BlockedBy: t.BlockedBy, Stall: t.Stall,
		})
	}
	writeJSON(w, http.StatusOK, out)
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
