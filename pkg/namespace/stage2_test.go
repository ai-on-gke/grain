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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	gotMetrics  *MetricsQuery
	gotLog      *GrainLogQuery
	gotPipeline *pipelineRead
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

func (s stubDeployProvider) ReadPipelineChanges(ctx context.Context, cursor string, limit int) (string, error) {
	if s.gotPipeline != nil {
		*s.gotPipeline = pipelineRead{cursor, limit}
	}
	return s.pipelineChanges, nil
}

// pipelineRead is what a deploy://pipeline-changes read asked its provider for.
type pipelineRead struct {
	cursor string
	limit  int
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

	// 1. task:// baseline allows own task, refuses other tasks naming api:tasks:read.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/100/record.md", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Task 100") {
		t.Fatalf("own task record: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/100/attempts/1/transcript.md", nil); rec.Code != http.StatusOK || rec.Body.String() != "transcript 1\n" {
		t.Fatalf("own task attempt transcript: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/task/200/record.md", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/api#tasks:read"`) {
		t.Fatalf("other task without api:tasks:read: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 2. config:// baseline allows repos/<task-repo> and workflows/, refuses grain.yml and other repos.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/repos/bwsalmon/grain.yml", nil); rec.Code != http.StatusOK {
		t.Fatalf("own repo config: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/workflows/default.yml", nil); rec.Code != http.StatusOK {
		t.Fatalf("workflows config: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/config/grain.yml", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/grain#source"`) {
		t.Fatalf("grain.yml without grain#source: status=%d body=%q", rec.Code, rec.Body.String())
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
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/memory/repos/acme/private/MEMORY.md", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/api#memory"`) {
		t.Fatalf("other repo memory without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// 4. deploy:// baseline allows merge-queue, refuses metrics/health/state/grain.log with capability names.
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/merge-queue", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Merge queue") {
		t.Fatalf("deploy://merge-queue: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/metrics", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/api#deployment:read"`) {
		t.Fatalf("deploy://metrics without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/health", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/grain#health"`) {
		t.Fatalf("deploy://health without grant: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(srv, baseline, http.MethodGet, "/fs/deploy/grain.log", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"https://grain.dev/capabilities/grain#journal"`) {
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

// deploy://pipeline-changes passes ?cursor= (or ?since=) and ?page= on, so
// a read can page past the feed's first page.
func TestPipelineChangesReadForwardsItsCursor(t *testing.T) {
	t.Parallel()
	var got pipelineRead
	srv := &Server{Deploy: stubDeployProvider{pipelineChanges: "a change\n", gotPipeline: &got}}
	scope := Scope{TaskID: 1, TaskIDStr: "1", MayReadPipeline: true}
	for _, tc := range []struct {
		query string
		want  pipelineRead
	}{
		{"", pipelineRead{}},
		{"?cursor=2026-10-29T11:50:43Z~1&page=20", pipelineRead{"2026-10-29T11:50:43Z~1", 20}},
		{"?since=2026-10-29T11:50:43Z", pipelineRead{"2026-10-29T11:50:43Z", 0}},
	} {
		got = pipelineRead{}
		rec := serveRequest(srv, scope, http.MethodGet, "/fs/deploy/pipeline-changes"+tc.query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status=%d body=%q", tc.query, rec.Code, rec.Body.String())
		}
		if got != tc.want {
			t.Errorf("%q: provider asked for %+v, want %+v", tc.query, got, tc.want)
		}
	}
	for _, q := range []string{"?page=0", "?page=lots"} {
		if rec := serveRequest(srv, scope, http.MethodGet, "/fs/deploy/pipeline-changes"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400; body=%q", q, rec.Code, rec.Body.String())
		}
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

// queryMergeQueueDeploy answers deploy://merge-queue reads that carry a
// query, recording what each was asked.
type queryMergeQueueDeploy struct {
	stubDeployProvider
	got *MergeQueueQuery
}

func (d queryMergeQueueDeploy) ReadMergeQueueQuery(_ context.Context, q MergeQueueQuery) (string, error) {
	*d.got = q
	if q.JSON {
		return `{"repo":"` + q.Repo + `"}` + "\n", nil
	}
	return "queue of " + q.Repo + "\n", nil
}

// deploy://merge-queue?repo=&format=json reaches a provider that answers
// queries with the repository, the format and -- for a caller reaching
// the queue under ci:read alone -- the scope's own repository limit; a
// provider that cannot answer one refuses it rather than ignoring it.
func TestMergeQueueQueryCarriesRepoFormatAndCIRepoLimit(t *testing.T) {
	t.Parallel()
	var got MergeQueueQuery
	srv := &Server{Deploy: queryMergeQueueDeploy{stubDeployProvider: stubDeployProvider{mergeQueue: "whole queue\n"}, got: &got}}
	byCI := Scope{MayReadCI: true, AllowedRepos: map[string]bool{"acme/widgets": true}, MergeQueueByCI: true}

	rec := serveRequest(srv, byCI, http.MethodGet, "/fs/deploy/merge-queue?repo=acme/widgets&format=json", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"repo":"acme/widgets"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got.Repo != "acme/widgets" || !got.JSON || got.Permits == nil {
		t.Fatalf("query = %+v", got)
	}
	if got.Permits("acme/secret") || !got.Permits("acme/widgets") {
		t.Error("a ci:read caller's query should carry its repository limit")
	}

	got = MergeQueueQuery{}
	rec = serveRequest(srv, Scope{MayReadAllCI: true}, http.MethodGet, "/fs/deploy/merge-queue?repo=acme/widgets", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "queue of acme/widgets\n" || got.JSON || got.Permits != nil {
		t.Fatalf("status=%d body=%q query=%+v", rec.Code, rec.Body.String(), got)
	}

	plain := &Server{Deploy: stubDeployProvider{mergeQueue: "whole queue\n"}}
	if rec := serveRequest(plain, Scope{MayReadAllCI: true}, http.MethodGet, "/fs/deploy/merge-queue", nil); rec.Code != http.StatusOK ||
		rec.Body.String() != "whole queue\n" {
		t.Fatalf("an unqueried read: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serveRequest(plain, Scope{MayReadAllCI: true}, http.MethodGet, "/fs/deploy/merge-queue?repo=acme/widgets", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a provider that cannot narrow the queue should refuse ?repo=: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

type countingMemoryProvider struct {
	repos      []string
	standing   map[string]string
	docs       map[string]map[string]MemoryDocView
	ordered    map[string][]MemoryDocView
	layerReads atomic.Int64
	docReads   atomic.Int64
	textReads  atomic.Int64
}

func newCountingMemoryProvider(repo string, docCount int) *countingMemoryProvider {
	p := &countingMemoryProvider{
		repos: []string{repo},
		standing: map[string]string{
			"global": "global standing lessons\n",
			repo:     "widgets standing lessons\n",
		},
		docs: map[string]map[string]MemoryDocView{
			"global": {
				"global-note": {Slug: "global-note", Summary: "Global note", Text: "global note body\n"},
			},
			repo: make(map[string]MemoryDocView, docCount),
		},
		ordered: map[string][]MemoryDocView{
			"global": {{Slug: "global-note", Summary: "Global note", Size: int64(len("global note body\n"))}},
		},
	}
	list := make([]MemoryDocView, 0, docCount)
	for i := 0; i < docCount; i++ {
		slug := fmt.Sprintf("doc-%04d", i)
		body := fmt.Sprintf("# Document %d\nObservation marker-%04d for %s.\n", i, i, repo)
		p.docs[repo][slug] = MemoryDocView{
			Slug:    slug,
			Summary: fmt.Sprintf("Summary %d", i),
			Text:    body,
			Size:    int64(len(body)),
		}
		list = append(list, MemoryDocView{
			Slug:    slug,
			Summary: fmt.Sprintf("Summary %d", i),
			Size:    int64(len(body)),
		})
	}
	p.ordered[repo] = list
	return p
}

func (p *countingMemoryProvider) ListMemoryRepos(context.Context) ([]string, error) {
	return p.repos, nil
}

func (p *countingMemoryProvider) ReadMemoryLayer(_ context.Context, scope string) (*MemoryLayerView, error) {
	p.layerReads.Add(1)
	text, hasText := p.standing[scope]
	docs := p.ordered[scope]
	if !hasText && len(docs) == 0 && scope != "global" {
		return nil, ErrNotFound
	}
	return &MemoryLayerView{Scope: scope, Text: text, Docs: docs}, nil
}

func (p *countingMemoryProvider) ReadMemoryDocument(_ context.Context, scope, slug string) (*MemoryDocView, error) {
	p.docReads.Add(1)
	bySlug := p.docs[scope]
	if bySlug == nil {
		return nil, ErrNotFound
	}
	doc, ok := bySlug[slug]
	if !ok {
		return nil, ErrNotFound
	}
	return &doc, nil
}

func (p *countingMemoryProvider) ReadMemoryText(_ context.Context, scope string) (string, error) {
	p.textReads.Add(1)
	text, hasText := p.standing[scope]
	if !hasText && len(p.ordered[scope]) == 0 && scope != "global" {
		return "", ErrNotFound
	}
	return text, nil
}

func (p *countingMemoryProvider) resetCounts() {
	p.layerReads.Store(0)
	p.docReads.Store(0)
	p.textReads.Store(0)
}

// TestMemorySingleDocumentAndLargeWalkAvoidQuadraticLayerFetches pins that
// resolving single memory:// documents uses O(1) single-document reads (0
// full-layer listings) and that listing, globbing, and grepping a 2,050-doc
// memory:// layer perform O(1) layer listings and O(N) single-doc reads well
// under 1 second.
func TestMemorySingleDocumentAndLargeWalkAvoidQuadraticLayerFetches(t *testing.T) {
	t.Parallel()

	const docCount = 2050
	const repo = "acme/widgets"
	mem := newCountingMemoryProvider(repo, docCount)
	srv := &Server{Memory: mem, Redactor: redact.New(nil)}
	scope := Scope{TaskID: 1, TaskIDStr: "1", TaskRepo: repo}

	// 1. Single-document reads on global/ and repos/<owner>/<repo>/ fetch only
	// that document without listing the layer.
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/fs/memory/global/global-note.md", "global note body\n"},
		{"/fs/memory/global/docs/global-note.md", "global note body\n"},
		{"/fs/memory/repos/acme/widgets/doc-0042.md", "# Document 42\nObservation marker-0042 for acme/widgets.\n"},
		{"/fs/memory/repos/acme/widgets/doc-0042", "# Document 42\nObservation marker-0042 for acme/widgets.\n"},
		{"/fs/memory/repos/acme/widgets/docs/doc-2049.md", "# Document 2049\nObservation marker-2049 for acme/widgets.\n"},
	} {
		mem.resetCounts()
		rec := serveRequest(srv, scope, http.MethodGet, tc.path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
			t.Fatalf("GET %s: status=%d body=%q, want %q", tc.path, rec.Code, rec.Body.String(), tc.want)
		}
		if got := mem.layerReads.Load(); got != 0 {
			t.Fatalf("GET %s called ReadMemoryLayer %d times, want 0", tc.path, got)
		}
		if got := mem.docReads.Load(); got != 1 {
			t.Fatalf("GET %s called ReadMemoryDocument %d times, want 1", tc.path, got)
		}
	}

	// Missing single document returns 404 in a single document lookup.
	mem.resetCounts()
	if rec := serveRequest(srv, scope, http.MethodGet, "/fs/memory/repos/acme/widgets/no-such-doc.md", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing doc status=%d, want 404", rec.Code)
	}
	if mem.layerReads.Load() != 0 || mem.docReads.Load() != 1 {
		t.Fatalf("missing doc counts: layer=%d doc=%d, want 0 and 1", mem.layerReads.Load(), mem.docReads.Load())
	}

	// Standing MEMORY.md reads do not list documents either.
	for _, p := range []string{
		"/fs/memory/global.md",
		"/fs/memory/global/MEMORY.md",
		"/fs/memory/repos/acme/widgets.md",
		"/fs/memory/repos/acme/widgets/MEMORY.md",
	} {
		mem.resetCounts()
		rec := serveRequest(srv, scope, http.MethodGet, p, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%q", p, rec.Code, rec.Body.String())
		}
		if mem.layerReads.Load() != 0 || mem.docReads.Load() != 0 || mem.textReads.Load() != 1 {
			t.Fatalf("GET %s counts: layer=%d doc=%d text=%d, want 0, 0, 1",
				p, mem.layerReads.Load(), mem.docReads.Load(), mem.textReads.Load())
		}
	}

	// 2. Directory listing (?list with JSON sizes) and globbing over 2,050 docs
	// perform 1 layer listing and 0 document body reads.
	mem.resetCounts()
	start := time.Now()
	listRec := serveRequest(srv, scope, http.MethodGet, "/fs/memory/repos/acme/widgets?list", map[string]string{"Accept": "application/json"})
	if elapsed := time.Since(start); elapsed > walkBudget {
		t.Fatalf("?list over %d docs took %v, want < %v", docCount, elapsed, walkBudget)
	}
	if listRec.Code != http.StatusOK {
		t.Fatalf("?list status=%d body=%q", listRec.Code, listRec.Body.String())
	}
	var listPayload struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("unmarshal ?list JSON: %v", err)
	}
	if len(listPayload.Entries) != docCount+1 {
		t.Fatalf("?list entries=%d, want %d", len(listPayload.Entries), docCount+1)
	}
	for _, e := range listPayload.Entries {
		if e.Size <= 0 {
			t.Fatalf("entry %q has non-positive size %d; lazy listing must preserve byte size", e.Name, e.Size)
		}
	}
	if mem.layerReads.Load() != 1 || mem.docReads.Load() != 0 {
		t.Fatalf("?list counts: layer=%d doc=%d, want 1 and 0", mem.layerReads.Load(), mem.docReads.Load())
	}

	mem.resetCounts()
	start = time.Now()
	globRes, err := srv.GlobEntries(context.Background(), scope, "memory", "", "repos/**/*.md")
	if err != nil {
		t.Fatalf("GlobEntries: %v", err)
	}
	if elapsed := time.Since(start); elapsed > walkBudget {
		t.Fatalf("GlobEntries over %d docs took %v, want < %v", docCount, elapsed, walkBudget)
	}
	if len(globRes.Matches) == 0 {
		t.Fatalf("GlobEntries returned no matches")
	}
	if mem.layerReads.Load() != 1 || mem.docReads.Load() != 0 {
		t.Fatalf("GlobEntries counts: layer=%d doc=%d, want 1 and 0", mem.layerReads.Load(), mem.docReads.Load())
	}

	// 3. Grep (with and without --glob) over 2,050 docs performs 1 layer listing
	// and O(N) single-document reads (bounded by maxWalkFiles) in well under 1s.
	mem.resetCounts()
	start = time.Now()
	grepRes, err := srv.Grep(context.Background(), scope, "memory", "repos/acme/widgets", GrepOptions{
		Pattern:    "marker-1500",
		GlobFilter: "*.md",
	})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if elapsed := time.Since(start); elapsed > walkBudget {
		t.Fatalf("Grep over %d docs took %v, want < %v", docCount, elapsed, walkBudget)
	}
	if grepRes.Matches != 1 || !strings.Contains(grepRes.Output, "doc-1500.md:2:Observation marker-1500") {
		t.Fatalf("Grep result = %+v, want 1 match for marker-1500", grepRes)
	}
	if !grepRes.WalkTruncated {
		t.Fatalf("Grep over %d docs should report WalkTruncated at maxWalkFiles=%d", docCount, maxWalkFiles)
	}
	if got := mem.layerReads.Load(); got != 1 {
		t.Fatalf("Grep called ReadMemoryLayer %d times, want 1", got)
	}
	if got := mem.docReads.Load(); got < 1500 || got > maxWalkFiles {
		t.Fatalf("Grep called ReadMemoryDocument %d times, want O(N) bounded by %d", got, maxWalkFiles)
	}

	// 4. Even a plain MemoryProvider without MemoryDocumentReader (which JSON-decodes
	// all 2,050 documents inside ReadMemoryLayer) decodes the layer only once across
	// a Grep walk rather than N+1 times.
	var legacyLayerDecodes atomic.Int64
	rawLayerJSON, err := json.Marshal(MemoryLayerView{
		Scope: repo,
		Text:  "widgets standing lessons\n",
		Docs: func() []MemoryDocView {
			out := make([]MemoryDocView, 0, docCount)
			for i := 0; i < docCount; i++ {
				out = append(out, mem.docs[repo][fmt.Sprintf("doc-%04d", i)])
			}
			return out
		}(),
	})
	if err != nil {
		t.Fatalf("marshal legacy layer: %v", err)
	}
	legacySrv := &Server{
		Memory: legacyDecodingMemoryProvider{
			repos:   []string{repo},
			payload: map[string][]byte{repo: rawLayerJSON},
			decodes: &legacyLayerDecodes,
		},
		Redactor: redact.New(nil),
	}
	start = time.Now()
	legacyGrep, err := legacySrv.Grep(context.Background(), scope, "memory", "repos/acme/widgets", GrepOptions{
		Pattern: "marker-1500",
	})
	if err != nil {
		t.Fatalf("legacy Grep: %v", err)
	}
	if elapsed := time.Since(start); elapsed > walkBudget {
		t.Fatalf("legacy Grep over %d docs took %v, want < %v", docCount, elapsed, walkBudget)
	}
	if legacyGrep.Matches != 1 || legacyLayerDecodes.Load() != 1 {
		t.Fatalf("legacy Grep matches=%d layerDecodes=%d, want 1 match and 1 layer decode",
			legacyGrep.Matches, legacyLayerDecodes.Load())
	}
}

type legacyDecodingMemoryProvider struct {
	repos   []string
	payload map[string][]byte
	decodes *atomic.Int64
}

func (p legacyDecodingMemoryProvider) ListMemoryRepos(context.Context) ([]string, error) {
	return p.repos, nil
}

func (p legacyDecodingMemoryProvider) ReadMemoryLayer(_ context.Context, scope string) (*MemoryLayerView, error) {
	p.decodes.Add(1)
	raw, ok := p.payload[scope]
	if !ok {
		return nil, ErrNotFound
	}
	var layer MemoryLayerView
	if err := json.Unmarshal(raw, &layer); err != nil {
		return nil, err
	}
	return &layer, nil
}

func BenchmarkMemoryGrepLargeLayer(b *testing.B) {
	const repo = "acme/widgets"
	mem := newCountingMemoryProvider(repo, 2050)
	srv := &Server{Memory: mem, Redactor: redact.New(nil)}
	scope := Scope{TaskID: 1, TaskIDStr: "1", TaskRepo: repo}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := srv.Grep(ctx, scope, "memory", "repos/acme/widgets", GrepOptions{
			Pattern: "marker-1500",
		})
		if err != nil || res.Matches != 1 {
			b.Fatalf("Grep = %+v, %v", res, err)
		}
	}
}
