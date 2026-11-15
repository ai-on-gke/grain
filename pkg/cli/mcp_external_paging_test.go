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

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mcpToolBudget is what an MCP client shows of a tool result before it
// elides the rest (pkg/ui/list_page.go): each default page of a `grain
// mcp` list tool has to fit in it.
const mcpToolBudget = 10 * 1024

// TestGrainMCPListToolsPageUnderTheToolBudget is the §6 walkthrough of
// docs/test-plans/2026-11-10.md (task 3953) as a test: against a daemon
// holding several pages of tasks, inbox items and CI runs,
// list_grain_tasks, grain_inbox and ci_runs each answered ~20 KB with no
// arguments and were cut mid-row by pkg/mcp's backstop. Each default
// answer now fits the budget, says total and truncated, and walking its
// nextCursor reaches every row exactly once.
func TestGrainMCPListToolsPageUnderTheToolBudget(t *testing.T) {
	ctx := context.Background()
	useConfigHome(t)
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetRepos(ctx, []string{"acme/widgets", "acme/gadgets"}); err != nil {
		t.Fatal(err)
	}

	// 520 tasks over two repositories, every other one an unapproved
	// proposal (which the inbox lists), with titles and bodies of the
	// length real ones run to.
	const tasks = 520
	who := model.Principal{Kind: model.PrincipalHuman, ID: "jane"}
	repoTasks := map[string]int{}
	proposals := 0
	for i := range tasks {
		repoText := "acme/widgets"
		if i%4 == 3 {
			repoText = "acme/gadgets"
		}
		repo, err := model.ParseRepo(repoText)
		if err != nil {
			t.Fatal(err)
		}
		tk := model.Task{
			ID:     fmt.Sprintf("task-%04d", i),
			Intent: model.IntentImplement,
			Title:  fmt.Sprintf("fix(cli): page the workstation MCP server's list tools so result %d fits the budget", i),
			Body: strings.Repeat(fmt.Sprintf("Seeded row %d of the paging walkthrough, long enough to read like a real description. ", i), 3) +
				"\n\nDerived from nothing in particular.",
			Origin:  model.Origin{Attribution: model.Attribution{Actor: who}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective,
			Target:  &repo,
		}
		if i%2 == 0 {
			tk.Approval = &model.Attribution{Actor: who}
		} else {
			proposals++
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		repoTasks[repoText]++
	}

	// 260 finished CI runs on acme/widgets, half of them red with a
	// failing job, the shape a week of merge-queue runs has.
	const runs = 260
	runStore := ci.NewRunStore(1000)
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i := range runs {
		started := base.Add(time.Duration(i) * time.Minute)
		finished := started.Add(4 * time.Minute)
		conclusion := ci.ConclusionSuccess
		if i%2 == 1 {
			conclusion = ci.ConclusionFailure
		}
		jobs := []ci.JobRun{}
		for _, name := range []string{"go unit", "go e2e", "frontend", "ui e2e"} {
			job := ci.JobRun{Workflow: "Tests", WorkflowPath: ".github/workflows/tests.yml", JobID: strings.ReplaceAll(name, " ", "-"), Name: name,
				StartedAt: &started, FinishedAt: &finished, Conclusion: ci.ConclusionSuccess}
			if conclusion == ci.ConclusionFailure && name == "go e2e" {
				job.Conclusion = ci.ConclusionFailure
				job.Error = "TestAPinnedHostdIsWaitedForAcrossADisconnect: timed out after 30s"
			}
			jobs = append(jobs, job)
		}
		runStore.Put(ci.Run{
			ID:          fmt.Sprintf("run-%04d", i),
			Repo:        "acme/widgets",
			Ref:         fmt.Sprintf("grain/098073e4/task-%04d-fix-cli-page-grain-mcp-s-list", i),
			Commit:      fmt.Sprintf("%040d", i),
			Event:       ci.EventPush,
			RequestedBy: "grain",
			StartedAt:   &started,
			FinishedAt:  &finished,
			Conclusion:  conclusion,
			Jobs:        jobs,
		})
	}

	srv := ui.NewServer(withAccessCode(ui.Config{CI: &ci.Service{Runs: runStore}}), store)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c := accessCodeClient(ts.URL)

	type page struct {
		Total      int    `json:"total"`
		Truncated  bool   `json:"truncated"`
		NextCursor string `json:"nextCursor"`
	}
	// walk calls tool with args until nextCursor runs out, checking every
	// answer fits the budget, and returns every row's id across the pages
	// and the total the first page claimed.
	walk := func(t *testing.T, tool mcp.Tool, args map[string]any, rows func([]byte) []string) ([]string, int) {
		t.Helper()
		var ids []string
		total := -1
		cursor := ""
		for calls := 0; ; calls++ {
			if calls > 200 {
				t.Fatalf("%s: still paging after %d calls", tool.Name, calls)
			}
			call := map[string]any{}
			for k, v := range args {
				call[k] = v
			}
			if cursor != "" {
				call["cursor"] = cursor
			}
			res := tool.Handler(ctx, call)
			if res.IsError {
				t.Fatalf("%s %v: %s", tool.Name, call, res.Text)
			}
			if calls == 0 {
				t.Logf("%s %v: first page %d bytes", tool.Name, args, len(res.Text))
			}
			if len(res.Text) > mcpToolBudget {
				t.Errorf("%s %v answered %d bytes, over the %d-byte tool budget", tool.Name, call, len(res.Text), mcpToolBudget)
			}
			var p page
			if err := json.Unmarshal([]byte(res.Text), &p); err != nil {
				t.Fatalf("%s: decoding %q: %v", tool.Name, res.Text, err)
			}
			got := rows([]byte(res.Text))
			if calls == 0 {
				total = p.Total
				if !p.Truncated || p.NextCursor == "" {
					t.Errorf("%s %v: first page of %d rows says truncated=%v nextCursor=%q, want more pages", tool.Name, args, p.Total, p.Truncated, p.NextCursor)
				}
			}
			if p.Truncated != (p.NextCursor != "") {
				t.Errorf("%s: truncated=%v but nextCursor=%q", tool.Name, p.Truncated, p.NextCursor)
			}
			ids = append(ids, got...)
			if p.NextCursor == "" {
				return ids, total
			}
			cursor = p.NextCursor
		}
	}
	// distinct checks ids names want rows, none of them twice.
	distinct := func(t *testing.T, name string, ids []string, total, want int) {
		t.Helper()
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Errorf("%s: %s listed on two pages", name, id)
			}
			seen[id] = true
		}
		if total != want || len(seen) != want {
			t.Errorf("%s: total %d, walked %d distinct rows; want %d", name, total, len(seen), want)
		}
	}
	idsOf := func(field string) func([]byte) []string {
		return func(b []byte) []string {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatal(err)
			}
			var items []struct {
				ID   string `json:"id"`
				Task string `json:"task"`
			}
			if err := json.Unmarshal(raw[field], &items); err != nil {
				t.Fatalf("decoding %s: %v", field, err)
			}
			out := make([]string, 0, len(items))
			for _, it := range items {
				if it.ID != "" {
					out = append(out, it.ID)
				} else {
					out = append(out, it.Task)
				}
			}
			return out
		}
	}

	t.Run("list_grain_tasks", func(t *testing.T) {
		tool := externalListTasksTool(c)
		ids, total := walk(t, tool, map[string]any{}, idsOf("tasks"))
		distinct(t, "list_grain_tasks {}", ids, total, tasks)
		ids, total = walk(t, tool, map[string]any{"repo": "acme/widgets"}, idsOf("tasks"))
		distinct(t, "list_grain_tasks {repo}", ids, total, repoTasks["acme/widgets"])
		ids, total = walk(t, tool, map[string]any{"fields": "full"}, idsOf("tasks"))
		distinct(t, "list_grain_tasks {fields: full}", ids, total, tasks)

		// q narrows as search does.
		res := tool.Handler(ctx, map[string]any{"q": "result 417 fits"})
		var one struct {
			Tasks []struct {
				ID string `json:"id"`
			} `json:"tasks"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(res.Text), &one); err != nil || one.Total != 1 || len(one.Tasks) != 1 || one.Tasks[0].ID != "task-0417" {
			t.Errorf("list_grain_tasks {q} = %s (err %v), want task-0417 alone", res.Text, err)
		}
		if res := tool.Handler(ctx, map[string]any{"cursor": "not-a-cursor"}); !res.IsError {
			t.Errorf("list_grain_tasks took a cursor it never handed out: %s", res.Text)
		}
	})

	t.Run("grain_inbox", func(t *testing.T) {
		tool := externalInboxTool(c)
		ids, total := walk(t, tool, map[string]any{}, idsOf("items"))
		distinct(t, "grain_inbox {}", ids, total, proposals)
	})

	t.Run("ci_runs", func(t *testing.T) {
		tool := externalCIRunsTool(c)
		ids, total := walk(t, tool, map[string]any{"repo": "acme/widgets"}, idsOf("runs"))
		distinct(t, "ci_runs {repo}", ids, total, runs)
		ids, total = walk(t, tool, map[string]any{"repo": "acme/widgets", "compact": false}, idsOf("runs"))
		distinct(t, "ci_runs {repo, compact: false}", ids, total, runs)
		ids, total = walk(t, tool, map[string]any{"repo": "acme/widgets", "q": "task-01"}, idsOf("runs"))
		distinct(t, "ci_runs {repo, q}", ids, total, 100)
	})
}
