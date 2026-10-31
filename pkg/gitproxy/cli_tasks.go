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

package gitproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/namespace"
)

// TaskListNoteHeader carries /cli/tasks' count and elision ("12 task(s)
// shown of 40 matching ...") out of band, so that the body is the task
// lines and nothing else: `grain task list | wc -l` counts tasks, and the
// CLI prints the note on stderr.
const TaskListNoteHeader = "Grain-Task-List-Note"

// serveTasks answers /cli/tasks (list_grain_tasks): the tasks this run may
// read, narrowed by state, repo, search, kind, changed_since and stalled,
// the newest limit of them, one line each -- or, with format=json, as
// mcp.TaskListEntry values.
//
// A scope that may read every task (self-debug, api:tasks:read) lists
// them all; any other lists its own task, as task:// itself does.
func (p *GitProxy) serveTasks(w http.ResponseWriter, r *http.Request, scope namespace.Scope) {
	own := strings.TrimSpace(scope.TaskIDStr)
	if !scope.MayReadAllTasks && own == "" {
		cliError(w, namespace.RefuseCapability("grain task list", "self-debug"))
		return
	}
	query, err := taskListQuery(r.URL.Query(), time.Now())
	if err != nil {
		cliError(w, err)
		return
	}
	var lister mcp.TaskSummaryLister
	if p.Namespace != nil {
		lister, _ = p.Namespace.Tasks.(mcp.TaskSummaryLister)
	}
	if lister == nil {
		cliError(w, fmt.Errorf("%w: grain task list is not served by this proxy", namespace.ErrNotFound))
		return
	}
	all, err := lister.ListTaskSummaries(r.Context())
	if err != nil {
		cliError(w, err)
		return
	}
	visible := all
	if !scope.MayReadAllTasks {
		visible = nil
		for _, t := range all {
			if scope.PermitsTask(t.ID) {
				visible = append(visible, t)
			}
		}
	}
	page := mcp.ListTaskPage(visible, query)
	w.Header().Set(TaskListNoteHeader, page.Note())
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tasks":   page.Entries(),
			"shown":   len(page.Tasks),
			"matched": page.Matched,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, page.Lines())
}

// taskListQuery reads /cli/tasks' query: state may be repeated or
// comma-separated, and search may be given as q.
func taskListQuery(q url.Values, now time.Time) (mcp.TaskListQuery, error) {
	var out mcp.TaskListQuery
	for _, v := range q["state"] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out.States = append(out.States, s)
			}
		}
	}
	out.Repo = strings.TrimSpace(q.Get("repo"))
	out.Search = strings.TrimSpace(q.Get("search"))
	if out.Search == "" {
		out.Search = strings.TrimSpace(q.Get("q"))
	}
	out.Kind = strings.TrimSpace(q.Get("kind"))
	if err := mcp.CheckTaskKind(out.Kind); err != nil {
		return out, fmt.Errorf("%w: %v", namespace.ErrBadRequest, err)
	}
	if since := strings.TrimSpace(q.Get("changed_since")); since != "" {
		at, err := ci.ParseSince(since, now)
		if err != nil {
			return out, fmt.Errorf("%w: %v", namespace.ErrBadRequest, err)
		}
		out.ChangedSince = at
	}
	switch v := q.Get("stalled"); v {
	case "", "0", "false":
	default:
		out.Stalled = true
	}
	if v := q.Get("limit"); v != "" {
		out.Limit = cliLimit(q, "limit", 0, 1<<20)
		if out.Limit == 0 {
			return out, fmt.Errorf("%w: limit %q is not a positive number", namespace.ErrBadRequest, v)
		}
	}
	return out, nil
}
