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

package sandboxcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

// listTasks is a namespace.TaskProvider that can also list its tasks, as
// mcp.NamespaceTasks does.
type listTasks []mcp.TaskSummary

func (l listTasks) ListTaskIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(l))
	for _, t := range l {
		ids = append(ids, t.ID)
	}
	return ids, nil
}

func (l listTasks) ReadTaskView(_ context.Context, id string) (*namespace.TaskView, error) {
	return nil, namespace.ErrNotFound
}

func (l listTasks) ListTaskSummaries(context.Context) ([]mcp.TaskSummary, error) {
	return append([]mcp.TaskSummary(nil), l...), nil
}

// TestSandboxTaskListFiltersAndBounds runs grain task list end to end
// against the real proxy handler: before, it was a listing of task:// --
// every id there was, sorted as strings, whatever flags it was given.
func TestSandboxTaskListFiltersAndBounds(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var tasks listTasks
	for i := 1; i <= 120; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		state, repo := "queued", "acme/widgets"
		if i%2 == 0 {
			state = "proposed"
		}
		if i%3 == 0 {
			repo = "acme/gadgets"
		}
		tasks = append(tasks, mcp.TaskSummary{ID: fmt.Sprint(i), State: state, Repo: repo,
			Title: fmt.Sprintf("Fix thing %d", i), CreatedAt: &at, ChangedAt: &at})
	}
	tasks[99].Title = "Flaky go-long test"
	tasks[104].Kinds = []string{"session"}
	tasks[110].Stall = "queued for 4h without a worker"
	recentChange := base.Add(200 * time.Minute)
	tasks[115].ChangedAt = &recentChange

	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(map[string]string{"debug": "tok-debug", "plain": "tok-plain", "repo": "tok-repo"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{Tasks: tasks, Redactor: redact.New(nil)},
		NamespaceScopes: cliTestScopes{
			"debug": {TaskID: "7", RunID: "r", TaskRepo: "acme/widgets", MayReadAllTasks: true},
			"plain": {TaskID: "7", RunID: "r2", TaskRepo: "acme/widgets"},
			"repo":  {TaskID: "7", RunID: "r3", TaskRepo: "acme/widgets", TaskReadRepos: map[string]bool{"acme/gadgets": true}},
		},
	}
	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer srv.Close()

	run := func(token string, args ...string) (lines []string, errOut string, code int) {
		t.Helper()
		env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": token}, "")
		var out, stderr bytes.Buffer
		code, handled := maybeRunSandboxCLI(args, env, &out, &stderr)
		if !handled {
			t.Fatalf("%q not handled in sandbox mode", args)
		}
		if s := strings.TrimSuffix(out.String(), "\n"); s != "" {
			lines = strings.Split(s, "\n")
		}
		return lines, stderr.String(), code
	}
	ids := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			id, _, _ := strings.Cut(l, " ")
			out = append(out, id)
		}
		return out
	}

	// Bounded by default, newest kept, in filing (numeric) order.
	lines, errOut, code := run("tok-debug", "task", "list")
	if code != 0 || len(lines) != mcp.DefaultTaskListLimit {
		t.Fatalf("task list: exit %d, %d lines (want %d): %s", code, len(lines), mcp.DefaultTaskListLimit, errOut)
	}
	if got := ids(lines); got[0] != "71" || got[len(got)-1] != "120" {
		t.Errorf("task list kept %s..%s, want 71..120", got[0], got[len(got)-1])
	}
	if !strings.Contains(errOut, "50 task(s) shown of 120 matching") || !strings.Contains(errOut, "70 older") {
		t.Errorf("task list's note on stderr = %q", errOut)
	}

	// The acceptance case: --state and --limit reach the endpoint.
	lines, errOut, code = run("tok-debug", "task", "list", "--state", "proposed", "--limit", "30")
	if code != 0 || len(lines) != 30 {
		t.Fatalf("--state proposed --limit 30: exit %d, %d lines: %s", code, len(lines), errOut)
	}
	if lines[29] != "120  proposed  acme/gadgets  Fix thing 120" {
		t.Errorf("last line = %q", lines[29])
	}
	for _, l := range lines {
		if !strings.Contains(l, "  proposed  ") {
			t.Errorf("--state proposed listed %q", l)
		}
	}

	// Repeated and comma-separated states, repo and search.
	lines, _, _ = run("tok-debug", "task", "list", "--state", "queued", "--state", "proposed", "--repo", "acme/widgets", "--limit", "3")
	if got := strings.Join(ids(lines), ","); got != "116,118,119" {
		t.Errorf("--state queued --state proposed --repo acme/widgets --limit 3 = %s", got)
	}
	lines, _, _ = run("tok-debug", "task", "list", "--state", "queued,proposed", "--search", "GO-LONG")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "100  proposed  acme/widgets  Flaky go-long test") {
		t.Errorf("--search GO-LONG = %q", lines)
	}

	// --kind, --stalled, and --changed-since filters reach the endpoint.
	lines, errOut, code = run("tok-debug", "task", "list", "--kind", "session")
	if code != 0 || len(lines) != 1 || !strings.HasPrefix(lines[0], "105 ") {
		t.Errorf("--kind session: exit %d, lines=%q, err=%q", code, lines, errOut)
	}
	lines, errOut, code = run("tok-debug", "task", "list", "--stalled")
	if code != 0 || len(lines) != 1 || !strings.HasPrefix(lines[0], "111 ") {
		t.Errorf("--stalled: exit %d, lines=%q, err=%q", code, lines, errOut)
	}
	lines, errOut, code = run("tok-debug", "task", "list", "--changed-since", base.Add(150*time.Minute).Format(time.RFC3339))
	if code != 0 || len(lines) != 1 || !strings.HasPrefix(lines[0], "116 ") {
		t.Errorf("--changed-since: exit %d, lines=%q, err=%q", code, lines, errOut)
	}

	// --json.
	env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": "tok-debug"}, "")
	var out, stderr bytes.Buffer
	if code, _ := maybeRunSandboxCLI([]string{"task", "list", "--json", "--limit", "2"}, env, &out, &stderr); code != 0 {
		t.Fatalf("--json: exit %d: %s", code, stderr.String())
	}
	var page struct {
		Tasks   []mcp.TaskListEntry `json:"tasks"`
		Matched int                 `json:"matched"`
	}
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Tasks) != 2 || page.Matched != 120 || page.Tasks[1].ID != "120" {
		t.Errorf("--json = %s (%v)", out.String(), err)
	}

	// An unknown flag, or a kind outside the vocabulary, is refused rather
	// than ignored.
	if _, errOut, code := run("tok-debug", "task", "list", "--bogus", "x"); code != 2 || !strings.Contains(errOut, "unknown flag --bogus") {
		t.Errorf("--bogus: exit %d %q", code, errOut)
	}
	if _, errOut, code := run("tok-debug", "task", "list", "--kind", "nope"); code != 1 || !strings.Contains(errOut, `kind "nope"`) {
		t.Errorf("--kind nope: exit %d %q", code, errOut)
	}
	if _, errOut, code := run("tok-debug", "task", "list", "--limit", "0"); code != 1 || !strings.Contains(errOut, "--limit") {
		t.Errorf("--limit 0: exit %d %q", code, errOut)
	}

	// A run without self-debug sees its own task and nothing else.
	lines, errOut, code = run("tok-plain", "task", "list")
	if code != 0 || len(lines) != 1 || lines[0] != "7  queued  acme/widgets  Fix thing 7" {
		t.Errorf("task list without self-debug: exit %d %q %s", code, lines, errOut)
	}
	if lines, _, _ := run("tok-plain", "task", "list", "--state", "proposed"); len(lines) != 0 {
		t.Errorf("task list --state proposed without self-debug = %q", lines)
	}

	// An api:tasks:read@acme/gadgets grant lists what task:// serves it:
	// its own task and every acme/gadgets task, and no other acme/widgets
	// task. Before, the list stopped at its own task, so a sibling it may
	// read was only found by guessing its id.
	lines, errOut, code = run("tok-repo", "task", "list", "--limit", "200")
	if code != 0 || len(lines) != 41 {
		t.Fatalf("task list with api:tasks:read@acme/gadgets: exit %d, %d lines (want 41): %s", code, len(lines), errOut)
	}
	for _, l := range lines {
		if !strings.Contains(l, "acme/gadgets") && !strings.HasPrefix(l, "7 ") {
			t.Errorf("task list with api:tasks:read@acme/gadgets listed %q", l)
		}
	}
}
