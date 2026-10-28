package namespace

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/redact"
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
}

func (s stubDeployProvider) ReadMergeQueue(ctx context.Context) (string, error) {
	return s.mergeQueue, nil
}

func (s stubDeployProvider) ReadMetrics(ctx context.Context, section string) (string, error) {
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

func (s stubDeployProvider) ReadGrainLog(ctx context.Context, source string) (string, error) {
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
