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

package namespace

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/redact"
)

type stubTaskProvider struct {
	tasks map[string]*TaskView
}

func (s stubTaskProvider) ListTaskIDs(ctx context.Context) ([]string, error) {
	var ids []string
	for id := range s.tasks {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s stubTaskProvider) ReadTaskView(ctx context.Context, id string) (*TaskView, error) {
	tv, ok := s.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	return tv, nil
}

type stubMemoryProvider struct {
	repos  []string
	layers map[string]*MemoryLayerView
}

func (s stubMemoryProvider) ListMemoryRepos(ctx context.Context) ([]string, error) {
	return s.repos, nil
}

func (s stubMemoryProvider) ReadMemoryLayer(ctx context.Context, scope string) (*MemoryLayerView, error) {
	l, ok := s.layers[scope]
	if !ok {
		return nil, ErrNotFound
	}
	return l, nil
}

type stubDeployProvider struct {
	mergeQueue      string
	metrics         map[string]string
	pipelineChanges string
	health          string
	state           map[string]string
	grainLog        string
	// gotMetrics and gotLog, when set, record the query each read was
	// asked with.
	gotMetrics *MetricsQuery
	gotLog     *GrainLogQuery
}

func (s stubDeployProvider) ReadMergeQueue(ctx context.Context) (string, error) {
	return s.mergeQueue, nil
}

func (s stubDeployProvider) ReadMetrics(ctx context.Context, section string, q MetricsQuery) (string, error) {
	if s.gotMetrics != nil {
		*s.gotMetrics = q
	}
	if v, ok := s.metrics[section]; ok {
		return v, nil
	}
	return s.metrics["all"], nil
}

func (s stubDeployProvider) ReadPipelineChanges(ctx context.Context) (string, error) {
	return s.pipelineChanges, nil
}

func (s stubDeployProvider) ReadHealth(ctx context.Context) (string, error) {
	return s.health, nil
}

func (s stubDeployProvider) ReadState(ctx context.Context, table string) (string, error) {
	if v, ok := s.state[table]; ok {
		return v, nil
	}
	return "", ErrNotFound
}

func (s stubDeployProvider) ListStateTables(ctx context.Context) ([]string, error) {
	var out []string
	for k := range s.state {
		if k != "" {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s stubDeployProvider) ReadGrainLog(ctx context.Context, source string, q GrainLogQuery) (string, error) {
	if s.gotLog != nil {
		*s.gotLog = q
	}
	return s.grainLog, nil
}

func (s stubDeployProvider) ListGrainLogSources(ctx context.Context) ([]string, error) {
	return []string{"daemon"}, nil
}

func TestStage2NamespaceRootsAndScoping(t *testing.T) {
	t.Parallel()

	cfgDir := t.TempDir()
	for rel, content := range map[string]string{
		"grain.yml":                "max_workers: 4\n",
		"repos/bwsalmon/grain.yml": "ci_mode: grain\n",
		"repos/acme/private.yml":   "ci_mode: github\n",
		"workflows/default.yml":    "name: default\n",
		"secrets.enc":              "top-secret-encrypted-blob",
	} {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	srv := &Server{
		ConfigDir: cfgDir,
		Redactor:  redact.New([]redact.KnownValue{{Value: "super-secret-token-value", Label: "token"}}),
		Tasks: stubTaskProvider{
			tasks: map[string]*TaskView{
				"100": {
					ID:           "100",
					Record:       "Task 100: Own task\nState: running\n",
					Conversation: "comment #1: hello\n",
					Handover:     "standing summary for 100\n",
					Prompt:       "assembled prompt for 100\n",
					Attempts: []TaskAttemptView{
						{Number: 1, RunID: "100-1", Prompt: "prompt 1\n", Transcript: "transcript 1\n"},
					},
				},
				"200": {
					ID:     "200",
					Record: "Task 200: Other task\nState: queued\n",
				},
			},
		},
		Memory: stubMemoryProvider{
			repos: []string{"bwsalmon/grain", "acme/private"},
			layers: map[string]*MemoryLayerView{
				"global": {
					Scope: "global",
					Text:  "global standing memory\n",
					Docs:  []MemoryDocView{{Slug: "ci-debugging", Text: "how to debug CI\n"}},
				},
				"bwsalmon/grain": {
					Scope: "bwsalmon/grain",
					Text:  "grain repo memory\n",
				},
				"acme/private": {
					Scope: "acme/private",
					Text:  "private repo memory\n",
				},
			},
		},
		Deploy: stubDeployProvider{
			mergeQueue:      "Merge queue: 0 in flight\n",
			metrics:         map[string]string{"all": "All metrics\n", "tools": "Tools census\n"},
			pipelineChanges: "Recent pipeline changes\n",
			health:          `{"healthy":true}`,
			state:           map[string]string{"": "State summary\n", "task": `[{"id":"100"}]`},
			grainLog:        "2026-10-27T12:00:00Z clone https://x-access-token:ghs_secret123@github.com/bwsalmon/grain.git token=super-secret-token-value\n",
		},
	}

	baseline := Scope{
		TaskID:    100,
		TaskIDStr: "100",
		TaskRepo:  "bwsalmon/grain",
	}

	// 1. task:// baseline allows own task, refuses other tasks with self-debug message.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/100/record.md", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Task 100") {
		t.Fatalf("own task record: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/100/attempts/1/transcript.md", nil); rec.Code != http.StatusOK || rec.Body.String() != "transcript 1\n" {
		t.Fatalf("own task attempt transcript: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/200/record.md", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"self-debug"`) {
		t.Fatalf("other task without self-debug: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 2. config:// baseline allows repos/<task-repo> and workflows/, refuses grain.yml and other repos.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/repos/bwsalmon/grain.yml", nil); rec.Code != http.StatusOK {
		t.Fatalf("own repo config: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/workflows/default.yml", nil); rec.Code != http.StatusOK {
		t.Fatalf("workflows config: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/grain.yml", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"self-debug"`) {
		t.Fatalf("grain.yml without self-debug: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/repos/acme/private.yml", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("other repo config without self-debug: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 3. memory:// baseline allows global and repos/<task-repo>, refuses other repos without memory grant.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/memory/global/MEMORY.md", nil); rec.Code != http.StatusOK || rec.Body.String() != "global standing memory\n" {
		t.Fatalf("global memory: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/memory/global/ci-debugging.md", nil); rec.Code != http.StatusOK || rec.Body.String() != "how to debug CI\n" {
		t.Fatalf("global memory doc: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/memory/repos/bwsalmon/grain/MEMORY.md", nil); rec.Code != http.StatusOK {
		t.Fatalf("own repo memory: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/memory/repos/acme/private/MEMORY.md", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"memory"`) {
		t.Fatalf("other repo memory without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 4. deploy:// baseline allows merge-queue, refuses metrics/health/state/grain.log with capability names.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/merge-queue", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Merge queue") {
		t.Fatalf("deploy://merge-queue: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/metrics", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"self-debug"`) {
		t.Fatalf("deploy://metrics without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/health", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"deployment-health"`) {
		t.Fatalf("deploy://health without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/grain.log", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"grain-log"`) {
		t.Fatalf("deploy://grain.log without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 5. Expanded scope unlocks everything and redacts deploy://grain.log.
	expanded := baseline
	expanded.MayReadAllTasks = true
	expanded.MayReadFullConfig = true
	expanded.MayReadAllMemory = true
	expanded.MayReadMetrics = true
	expanded.MayReadPipeline = true
	expanded.MayReadHealth = true
	expanded.MayReadGrainLog = true

	if rec := serveRequest(srv, expanded, http.MethodGet, "/fs/task/200/record.md", nil); rec.Code != http.StatusOK {
		t.Fatalf("other task with self-debug: status=%d", rec.Code)
	}
	if rec := serveRequest(srv, expanded, http.MethodGet, "/fs/config/grain.yml", nil); rec.Code != http.StatusOK {
		t.Fatalf("grain.yml with self-debug: status=%d", rec.Code)
	}
	if rec := serveRequest(srv, expanded, http.MethodGet, "/fs/config/secrets.enc", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("secrets.enc must always be forbidden: status=%d", rec.Code)
	}
	if rec := serveRequest(srv, expanded, http.MethodGet, "/fs/deploy/metrics/tools.txt", nil); rec.Code != http.StatusOK || rec.Body.String() != "Tools census\n" {
		t.Fatalf("deploy://metrics/tools.txt: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, expanded, http.MethodGet, "/fs/deploy/state/tables/task.json", nil); rec.Code != http.StatusOK || rec.Body.String() != `[{"id":"100"}]` {
		t.Fatalf("deploy://state/tables/task.json: status=%d body=%q", rec.Code, rec.Body.String())
	}
	logRec := serveRequest(srv, expanded, http.MethodGet, "/fs/deploy/grain.log", nil)
	if logRec.Code != http.StatusOK {
		t.Fatalf("deploy://grain.log with grant: status=%d body=%q", logRec.Code, logRec.Body.String())
	}
	if strings.Contains(logRec.Body.String(), "ghs_secret123") || strings.Contains(logRec.Body.String(), "super-secret-token-value") {
		t.Fatalf("deploy://grain.log leaked secret: %q", logRec.Body.String())
	}
	if !strings.Contains(strings.ToLower(logRec.Body.String()), "[redacted") {
		t.Fatalf("deploy://grain.log expected redacted marker: %q", logRec.Body.String())
	}
}

// deploy://grain.log and deploy://metrics pass a read's window and filters
// on to their provider, so `grain log --since ... --contains ...` and
// `grain metrics --repo ...` reach past the default tail and report
// rather than only ever reading it.
func TestDeployReadsForwardTheirQuery(t *testing.T) {
	t.Parallel()
	var gotLog GrainLogQuery
	var gotMetrics MetricsQuery
	srv := &Server{Deploy: stubDeployProvider{
		metrics: map[string]string{"all": "All metrics\n"}, grainLog: "a line\n",
		gotLog: &gotLog, gotMetrics: &gotMetrics,
	}}
	scope := Scope{TaskID: 1, TaskIDStr: "1", MayReadGrainLog: true, MayReadMetrics: true}

	rec := serveRequest(srv, scope, http.MethodGet, "/fs/deploy/grain.log/daemon.log?since=2026-10-29T00:00:00Z"+
		"&until=2026-10-29T06:00:00Z&min_priority=err&contains=dispatch+failed&max_lines=900", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy://grain.log: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if want := (GrainLogQuery{Since: "2026-10-29T00:00:00Z", Until: "2026-10-29T06:00:00Z",
		MinPriority: "err", Contains: "dispatch failed", MaxLines: 900, TaskID: "1"}); gotLog != want {
		t.Errorf("grain.log query = %+v, want %+v", gotLog, want)
	}
	if rec := serveRequest(srv, scope, http.MethodGet, "/fs/deploy/grain.log?lines=50", nil); rec.Code != http.StatusOK || gotLog.MaxLines != 50 {
		t.Errorf("?lines=50: status=%d, max lines %d, want 50", rec.Code, gotLog.MaxLines)
	}
	if rec := serveRequest(srv, scope, http.MethodGet, "/fs/deploy/grain.log?max_lines=lots", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("?max_lines=lots: status=%d, want 400; body=%q", rec.Code, rec.Body.String())
	}

	rec = serveRequest(srv, scope, http.MethodGet, "/fs/deploy/metrics/summary.txt?window=7d&since=2026-10-01"+
		"&until=2026-10-08&repo=acme/a,acme/b&repo=acme/c&author=task", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy://metrics: status=%d body=%q", rec.Code, rec.Body.String())
	}
	want := MetricsQuery{Window: "7d", Since: "2026-10-01", Until: "2026-10-08",
		Repos: []string{"acme/a", "acme/b", "acme/c"}, Authors: []string{"task"}}
	if !reflect.DeepEqual(gotMetrics, want) {
		t.Errorf("metrics query = %+v, want %+v", gotMetrics, want)
	}
}

type stubConfigProvider struct {
	repos   []string
	configs map[string]string
}

func (s stubConfigProvider) ListConfigRepos(ctx context.Context) ([]string, error) {
	return s.repos, nil
}

func (s stubConfigProvider) ReadRepoConfig(ctx context.Context, repo string) (string, error) {
	for k, v := range s.configs {
		if strings.EqualFold(k, repo) {
			return v, nil
		}
	}
	return "", ErrNotFound
}

func TestThreeSegmentHostQualifiedRepoConfigAndMemory(t *testing.T) {
	t.Parallel()

	const forgeRepo = "git.forge.example/experimental/grain"
	scope := Scope{
		TaskID:    1920,
		TaskIDStr: "1920",
		TaskRepo:  forgeRepo,
	}

	// 1. Direct unit checks on PermitsConfig and PermitsMemory for 3-segment TaskRepo.
	for _, allowed := range []string{
		"",
		"repos",
		"repos/git.forge.example",
		"repos/git.forge.example/experimental",
		"repos/git.forge.example/experimental/grain",
		"repos/git.forge.example/experimental/grain.json",
		"repos/git.forge.example/experimental/grain.yml",
		"repos/git.forge.example/experimental/grain.yaml",
		"repos/git.forge.example/experimental/grain/sub",
	} {
		if !scope.PermitsConfig(allowed) {
			t.Errorf("PermitsConfig(%q) = false, want true", allowed)
		}
	}
	for _, denied := range []string{
		"grain.yml",
		"repos/git.forge.example/experimental/other",
		"repos/git.forge.example/experimental/other.yml",
		"repos/git.forge.example/other",
		"repos/git.forge.example/other/grain",
		"repos/ai-on-gke/grain",
	} {
		if scope.PermitsConfig(denied) {
			t.Errorf("PermitsConfig(%q) = true, want false", denied)
		}
	}

	for _, allowed := range []string{
		"",
		"global",
		"global.md",
		"global/MEMORY.md",
		"repos",
		"repos/git.forge.example",
		"repos/git.forge.example/experimental",
		"repos/git.forge.example/experimental/grain",
		"repos/git.forge.example/experimental/grain.md",
		"repos/git.forge.example/experimental/grain/MEMORY.md",
		"repos/git.forge.example/experimental/grain/docs/ci.md",
	} {
		if !scope.PermitsMemory(allowed) {
			t.Errorf("PermitsMemory(%q) = false, want true", allowed)
		}
	}
	for _, denied := range []string{
		"repos/git.forge.example/experimental/other",
		"repos/git.forge.example/experimental/other.md",
		"repos/git.forge.example/other",
		"repos/git.forge.example/other/grain",
		"repos/ai-on-gke/grain",
	} {
		if scope.PermitsMemory(denied) {
			t.Errorf("PermitsMemory(%q) = true, want false", denied)
		}
	}

	// 2. Virtual ConfigProvider and MemoryProvider with both 2-segment and 3-segment repos.
	srv := &Server{
		Config: stubConfigProvider{
			repos: []string{
				"ai-on-gke/grain",
				forgeRepo,
				"git.forge.example/experimental/other",
			},
			configs: map[string]string{
				"ai-on-gke/grain":                      `{"repo":"ai-on-gke/grain"}` + "\n",
				forgeRepo:                              `{"repo":"git.forge.example/experimental/grain"}` + "\n",
				"git.forge.example/experimental/other": `{"repo":"other"}` + "\n",
			},
		},
		Memory: stubMemoryProvider{
			repos: []string{
				"ai-on-gke/grain",
				forgeRepo,
				"git.forge.example/experimental/other",
			},
			layers: map[string]*MemoryLayerView{
				forgeRepo: {
					Scope: forgeRepo,
					Text:  "forge grain standing memory\n",
					Docs:  []MemoryDocView{{Slug: "runbook", Text: "forge runbook doc\n"}},
				},
				"git.forge.example/experimental/other": {
					Scope: "git.forge.example/experimental/other",
					Text:  "other memory\n",
				},
			},
		},
	}

	for _, tc := range []struct {
		url  string
		want string
	}{
		{"/fs/config/repos?list", "git.forge.example/\n"},
		{"/fs/config/repos/git.forge.example?list", "experimental/\n"},
		{"/fs/config/repos/git.forge.example/experimental?list", "grain\n"},
		{"/fs/config/repos/git.forge.example/experimental/grain", `{"repo":"git.forge.example/experimental/grain"}` + "\n"},
		{"/fs/config/repos/git.forge.example/experimental/grain.json", `{"repo":"git.forge.example/experimental/grain"}` + "\n"},
		{"/fs/config/repos/git.forge.example/experimental/grain.yml", `{"repo":"git.forge.example/experimental/grain"}` + "\n"},
		{"/fs/config/repos/git.forge.example/experimental/grain.yaml", `{"repo":"git.forge.example/experimental/grain"}` + "\n"},
		{"/fs/memory/repos?list", "git.forge.example/\n"},
		{"/fs/memory/repos/git.forge.example?list", "experimental/\n"},
		{"/fs/memory/repos/git.forge.example/experimental?list", "grain/\n"},
		{"/fs/memory/repos/git.forge.example/experimental/grain?list", "MEMORY.md\nrunbook.md\n"},
		{"/fs/memory/repos/git.forge.example/experimental/grain.md", "forge grain standing memory\n"},
		{"/fs/memory/repos/git.forge.example/experimental/grain/MEMORY.md", "forge grain standing memory\n"},
		{"/fs/memory/repos/git.forge.example/experimental/grain/runbook.md", "forge runbook doc\n"},
		{"/fs/memory/repos/git.forge.example/experimental/grain/docs/runbook.md", "forge runbook doc\n"},
	} {
		rec := serveRequest(srv, scope, http.MethodGet, tc.url, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", tc.url, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.url, got, tc.want)
		}
	}

	if rec := serveRequest(srv, scope, http.MethodGet, "/fs/config/repos/git.forge.example/experimental/other", nil); rec.Code != http.StatusForbidden {
		t.Errorf("other 3-segment config status = %d, want 403", rec.Code)
	}
	if rec := serveRequest(srv, scope, http.MethodGet, "/fs/memory/repos/git.forge.example/experimental/other/MEMORY.md", nil); rec.Code != http.StatusForbidden {
		t.Errorf("other 3-segment memory status = %d, want 403", rec.Code)
	}
}
