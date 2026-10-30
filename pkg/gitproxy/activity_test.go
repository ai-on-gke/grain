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

package gitproxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

type recordedToolCall struct {
	runID, tool string
	bytes       int64
	errored     bool
}

type fakeActivity struct {
	mu       sync.Mutex
	notes    []string
	toolRows []recordedToolCall
}

func (f *fakeActivity) SetTaskActivity(_ context.Context, taskID, note string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, taskID+":"+note)
	return true, nil
}

func (f *fakeActivity) RecordProxyToolCall(_ context.Context, runID, tool string, n int64, errored bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toolRows = append(f.toolRows, recordedToolCall{runID, tool, n, errored})
	return nil
}

func TestNamespaceRouteActivityMatchesMCPToolNames(t *testing.T) {
	for _, tc := range []struct {
		path, query, tool string
	}{
		{"/fs/ci/bwsalmon/grain/run-1/go/gotest.log", "", "get_ci_log"},
		{"/fs/ci/git.forge.example/experimental/grain/run-1/go/gotest.log", "", "get_ci_log"},
		{"/fs/ci/bwsalmon/grain/run-1/go/artifacts/cover.out", "", "get_ci_artifact"},
		{"/fs/ci/bwsalmon/grain", "list", "grain_ls"},
		{"/fs/ci/bwsalmon/grain/run-1/go/gotest.log", "grep=FAIL", "grain_grep"},
		{"/fs/src/pkg/ci/engine.go", "", "grain_read"},
		{"/fs/src/playbooks", "", "list_bootstrap_playbooks"},
		{"/fs/src/playbooks/gke.md", "", "read_bootstrap_playbook"},
		{"/fs/task", "", "list_grain_tasks"},
		{"/fs/task/42", "", "read_grain_task"},
		{"/fs/task/42/prompt.md", "", "read_grain_task_prompt"},
		{"/fs/task/42/attempts/2/transcript.log", "", "read_grain_task_transcript"},
		{"/fs/memory/global", "list", "list_memory_documents"},
		{"/fs/memory/global/docs/ci.md", "", "read_memory_document"},
		{"/fs/config/grain.yml", "", "grain_read"},
		{"/fs/deploy/grain.log", "", "read_grain_log"},
		{"/fs/deploy/health", "", "read_deployment_health"},
		{"/fs/deploy/state/task", "", "read_deployment_state"},
		{"/fs/deploy/metrics/tools", "", "read_grain_metrics"},
		{"/fs/deploy/merge-queue", "", "query_merge_queue"},
		{"/fs/deploy/pipeline-changes", "", "read_pipeline_changes"},
	} {
		q, _ := url.ParseQuery(tc.query)
		got := gitproxy.NamespaceRouteActivity(tc.path, q)
		if got.Tool != tc.tool {
			t.Errorf("%s?%s: tool = %q, want %q", tc.path, tc.query, got.Tool, tc.tool)
		}
		if want := mcp.ToolActivityNote(tc.tool); got.Phrase != want {
			t.Errorf("%s: phrase = %q, want the MCP tool's own %q", tc.path, got.Phrase, want)
		}
		if got.Phrase == "calling "+tc.tool {
			t.Errorf("%s: %s has no phrase in pkg/mcp's toolActivityNotes", tc.path, tc.tool)
		}
		if tc.path == "/fs/ci/git.forge.example/experimental/grain/run-1/go/gotest.log" {
			if len(got.Targets) != 1 || got.Targets[0] != "run-1" {
				t.Errorf("%s: targets = %v, want [run-1]", tc.path, got.Targets)
			}
		}
	}
}

func TestNamespaceRequestsNarrateAndRecordToolCalls(t *testing.T) {
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("grain source tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb-debug": "tok-debug", "sb-plain": "tok-plain"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	act := &fakeActivity{}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{SrcDir: srcDir, Redactor: redact.New(nil)},
		NamespaceScopes: fakeNamespaceScopes{scopes: map[string]model.NamespaceScope{
			"sb-debug": {TaskID: "7", RunID: "run-a", TaskRepo: "bwsalmon/grain", MayReadSrc: true},
			"sb-plain": {TaskID: "8", RunID: "run-b", TaskRepo: "bwsalmon/grain"},
		}},
		Activity: act,
	}
	h := gitproxy.NewHandler(proxy)
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", basicAuthHeader(token))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("tok-debug", "/fs/src/README.md"); rec.Code != http.StatusOK {
		t.Fatalf("read src: %d %s", rec.Code, rec.Body)
	}
	// A repeat inside the window writes the row once.
	get("tok-debug", "/fs/src/README.md")
	if rec := get("tok-plain", "/fs/src/README.md"); rec.Code != http.StatusForbidden {
		t.Fatalf("read src without self-debug: %d %s", rec.Code, rec.Body)
	}

	act.mu.Lock()
	defer act.mu.Unlock()
	wantNote := mcp.ToolActivityNote("grain_read")
	if len(act.notes) != 2 || act.notes[0] != "7:"+wantNote || act.notes[1] != "8:"+wantNote {
		t.Errorf("activity notes = %q, want one per task of %q", act.notes, wantNote)
	}
	if len(act.toolRows) != 3 {
		t.Fatalf("tool rows = %+v, want 3", act.toolRows)
	}
	ok := act.toolRows[0]
	if ok.runID != "run-a" || ok.tool != "grain_read" || ok.bytes != int64(len("grain source tree\n")) || ok.errored {
		t.Errorf("served read recorded as %+v", ok)
	}
	refused := act.toolRows[2]
	if refused.runID != "run-b" || refused.tool != "grain_read" || !refused.errored {
		t.Errorf("refused read recorded as %+v", refused)
	}
}
