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

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/systemlog"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type stage2Scopes map[string]model.NamespaceScope

func (s stage2Scopes) SandboxNamespaceScope(_ context.Context, sb string) (model.NamespaceScope, bool, error) {
	sc, ok := s[sb]
	return sc, ok, nil
}

// stage2Proxy is the sandbox proxy as StartGitProxy builds it -- the
// namespace and /cli/ wired by wireSandboxNamespace onto grain's own API
// over a real store -- serving the sandboxes scopes names, each with the
// token of its own name.
func stage2Proxy(t *testing.T, scopes stage2Scopes) (*httptest.Server, *model.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	api := &inProcessAPI{}
	api.Set(ui.NewServer(ui.Config{Actor: ui.DefaultActor("deployment"), SourceDB: db, DBDriver: "sqlite"}, store))
	// Redacted as StartGitProxy redacts it: with this deployment's own
	// secrets, read afresh for each request.
	ns := &namespace.Server{NewRedactor: func() *redact.Redactor { return buildNamespaceRedactor(dataDir) }}
	cli := wireSandboxNamespace(ns, sandboxNamespaceConfig{DataDir: dataDir, Store: store, API: api})

	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	tokens := map[string]string{}
	for sb := range scopes {
		tokens[sb] = sb
	}
	raw, _ := json.Marshal(tokens)
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gitproxy.NewHandler(&gitproxy.GitProxy{
		Tokens: loaded, Namespace: ns, NamespaceScopes: scopes, CLI: cli,
	}))
	t.Cleanup(srv.Close)
	return srv, store, dataDir
}

func stage2Get(t *testing.T, srv *httptest.Server, token, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("x-access-token", token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestSandboxProxyServesTheStage2Roots reads task://, memory://, config://
// and deploy:// over the proxy, as the in-sandbox grain CLI does, and
// finds in them what the store holds -- rather than "not configured".
func TestSandboxProxyServesTheStage2Roots(t *testing.T) {
	repo := "acme/widgets"
	srv, store, dataDir := stage2Proxy(t, stage2Scopes{
		"own": {TaskID: "t-7", RunID: "run-t-7-2", TaskRepo: repo},
		"debug": {TaskID: "t-9", RunID: "run-9", TaskRepo: repo, MayReadAllTasks: true, MayReadFullConfig: true,
			MayReadAllMemory: true, MayReadHealth: true},
	})
	ctx := context.Background()
	ref, _ := model.ParseRepo(repo)
	base := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"t-7", "t-8"} {
		if err := store.PutTask(ctx, model.Task{ID: id, Title: "Widget task " + id, Body: "Make the widget spin",
			Target: &ref}); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 1; attempt <= 2; attempt++ {
		runID := "run-t-7-" + string(rune('0'+attempt))
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: "t-7", Sandbox: "own", Attempt: attempt,
			StartedAt: base.Add(time.Duration(attempt) * time.Hour)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRunPrompt(ctx, runID, "prompt of attempt "+string(rune('0'+attempt))); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRunTranscript(ctx, runID, "transcript of attempt "+string(rune('0'+attempt))); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			if err := store.FinishRun(ctx, runID, base.Add(90*time.Minute), "failed", "the build broke"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "t-7", Body: "please also polish it", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "t-7", Body: "Settled: spin clockwise.", CreatedAt: base.Add(time.Minute),
		Handover: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(ctx, model.Memory{Text: "deployment-wide lesson"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemory(ctx, model.Memory{Repo: &ref, Text: "widgets lesson"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutMemoryDocument(ctx, model.MemoryDocument{Repo: &ref, Name: "flakes", Summary: "known flakes",
		Text: "TestSpin is flaky"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: ref, CloneURL: "https://bot:hunter2@git.example/acme/widgets.git",
		DefaultCapabilities: []string{"grain-ci"}}); err != nil {
		t.Fatal(err)
	}
	state := StateRepoDir(dataDir)
	for name, body := range map[string]string{
		"README.md":           "grain's configuration\n",
		"tables/task.json":    `[{"id":"t-7","note":"s3cret-value"}]`,
		"secrets.enc":         "ENCRYPTED",
		"secrets/secrets.key": "AGE-SECRET-KEY",
		"notes/private.txt":   "not grain's",
	} {
		p := filepath.Join(state, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(state, "secrets", "secrets.key"), filepath.Join(state, "tables", "key.json")); err != nil {
		t.Fatal(err)
	}

	// With no secret store to say what to cut out, health and state
	// are refused rather than served unredacted -- read_deployment_*'s
	// own rule.
	if status, body := stage2Get(t, srv, "debug", "/fs/deploy/state/tables/task.json"); status == 200 ||
		!strings.Contains(body, "refused") {
		t.Errorf("state with no secret store = %d %s, want it refused", status, body)
	}
	if err := secrets.Open(SecretsConfig(dataDir)).Set("widget-api", "token", []byte("s3cret-value")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		token, path string
		status      int
		want        []string
		not         []string
	}{
		// task://: a run reads its own task, in read_grain_task's words.
		{token: "own", path: "/fs/task/t-7/record.md", status: 200,
			want: []string{"Task t-7: Widget task t-7", "Make the widget spin", "#1", "outcome failed", "please also polish it"}},
		{token: "own", path: "/fs/task/t-7/summary.md", status: 200, want: []string{"Widget task t-7"}},
		{token: "own", path: "/fs/task/t-7/conversation.md", status: 200, want: []string{"please also polish it", "Settled: spin clockwise."}},
		{token: "own", path: "/fs/task/t-7/handover.md", status: 200, want: []string{"Settled: spin clockwise."}, not: []string{"polish"}},
		{token: "own", path: "/fs/task/t-7/prompt.md", status: 200, want: []string{"prompt of attempt 2"}},
		{token: "own", path: "/fs/task/t-7/transcript.md", status: 200, want: []string{"transcript of attempt 2"}},
		{token: "own", path: "/fs/task/t-7/attempts/1/prompt.md", status: 200, want: []string{"prompt of attempt 1"}},
		{token: "own", path: "/fs/task/t-7/attempts/1/transcript.md", status: 200, want: []string{"transcript of attempt 1"}},
		{token: "own", path: "/fs/task/t-7/attempts?list", status: 200, want: []string{"1/", "2/"}},
		// A sibling task in the run's own repository is not, without
		// self-debug or an api:tasks:read grant naming the repository.
		{token: "own", path: "/fs/task/t-8/record.md", status: 403, want: []string{"api:tasks:read@"}, not: []string{"Widget task t-8"}},
		{token: "debug", path: "/fs/task/t-8/record.md", status: 200, want: []string{"Widget task t-8"}},
		{token: "debug", path: "/fs/task/?list", status: 200, want: []string{"t-7/", "t-8/"}},
		{token: "debug", path: "/fs/task/t-404/record.md", status: 404},

		// memory://
		{token: "own", path: "/fs/memory/global/MEMORY.md", status: 200, want: []string{"deployment-wide lesson"}},
		{token: "own", path: "/fs/memory/repos/acme/widgets/MEMORY.md", status: 200, want: []string{"widgets lesson"}},
		{token: "own", path: "/fs/memory/repos/acme/widgets/flakes.md", status: 200, want: []string{"TestSpin is flaky"}},
		{token: "own", path: "/fs/memory/repos/acme/widgets?list", status: 200, want: []string{"MEMORY.md", "flakes.md"}},

		// config://: the run's own repository's row, with no credential
		// in it, and the state repository held to what grain exports.
		{token: "own", path: "/fs/config/repos/acme/widgets", status: 200, want: []string{"grain-ci", "git.example/acme/widgets.git"},
			not: []string{"hunter2"}},
		{token: "debug", path: "/fs/config/?list", status: 200, want: []string{"README.md", "tables/", "repos/"},
			not: []string{"secrets", "notes"}},
		{token: "debug", path: "/fs/config/tables/task.json", status: 200, want: []string{`"t-7"`, "[redacted:"}, not: []string{"s3cret-value"}},
		{token: "debug", path: "/fs/config/tables?list", status: 200, want: []string{"task.json"}, not: []string{"key.json"}},
		{token: "debug", path: "/fs/config/tables/key.json", status: 403},
		{token: "debug", path: "/fs/config/secrets.enc", status: 403},
		{token: "debug", path: "/fs/config/secrets/secrets.key", status: 403},
		{token: "debug", path: "/fs/config/notes/private.txt", status: 403},
		{token: "debug", path: "/fs/config/.git/config", status: 403},

		// deploy://
		{token: "own", path: "/fs/deploy/merge-queue", status: 200, want: []string{"Merge Queue Status"}},
		{token: "debug", path: "/fs/deploy/state/tables?list", status: 200, want: []string{"task.json"}},
		{token: "debug", path: "/fs/deploy/state/tables/task.json", status: 200, want: []string{`"t-7"`, "[redacted:"}, not: []string{"s3cret-value"}},
		{token: "own", path: "/fs/deploy/state/tables/task.json", status: 403},
		{token: "debug", path: "/fs/deploy/state/tables?list", status: 200, not: []string{"key.json"}},
		{token: "debug", path: "/fs/deploy/state/tables/key.json", status: 404, not: []string{"AGE-SECRET-KEY"}},
	} {
		status, body := stage2Get(t, srv, tc.token, tc.path)
		if status != tc.status {
			t.Errorf("%s GET %s = %d, want %d: %s", tc.token, tc.path, status, tc.status, body)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s GET %s: missing %q in:\n%s", tc.token, tc.path, w, body)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(body, n) {
				t.Errorf("%s GET %s: should not contain %q:\n%s", tc.token, tc.path, n, body)
			}
		}
	}
}

// TestSandboxProxyServesDBQueriesReadOnly is /cli/db/query answered by
// query_grain_db's backend: rows for a read, a refusal for a write even
// from a run whose task holds db-write, and the grant's refusal without
// it.
func TestSandboxProxyServesDBQueriesReadOnly(t *testing.T) {
	srv, store, _ := stage2Proxy(t, stage2Scopes{
		"db":      {TaskID: "t-db", RunID: "run-t-db", MayReadDB: true},
		"nogrant": {TaskID: "t-db", RunID: "run-t-db"},
	})
	ctx := context.Background()
	if err := store.PutTask(ctx, model.Task{ID: "t-db", Title: "DB task", Grants: []model.Grant{
		{Capability: dbaccess.CapabilityDBRead, Via: model.GrantByLabel},
		{Capability: dbaccess.CapabilityDBWrite, Via: model.GrantByLabel},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "run-t-db", TaskID: "t-db", Sandbox: "db", Attempt: 1,
		StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	status, body := stage2Get(t, srv, "db", "/cli/db/query?sql="+urlQuery("SELECT id, title FROM task WHERE id = 't-db'"))
	if status != 200 || !strings.Contains(body, "Query returned 1 row(s)") || !strings.Contains(body, "DB task") {
		t.Errorf("read query = %d %s", status, body)
	}
	status, body = stage2Get(t, srv, "db", "/cli/db/query?sql="+urlQuery("DELETE FROM task"))
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Query rejected") {
		t.Errorf("write query = %d %s, want it rejected as not read-only", status, body)
	}
	if task, err := store.GetTask(ctx, "t-db"); err != nil || task == nil {
		t.Errorf("the task is gone after a refused DELETE: %v", err)
	}
	status, body = stage2Get(t, srv, "nogrant", "/cli/db/query?sql="+urlQuery("SELECT 1"))
	if status != http.StatusForbidden || !strings.Contains(body, "db-read") {
		t.Errorf("without the grant = %d %s", status, body)
	}
}

func urlQuery(s string) string {
	r := strings.NewReplacer(" ", "%20", "'", "%27", "=", "%3D", ",", "%2C")
	return r.Replace(s)
}

// deploy://grain.log's query reaches read_grain_log as that tool's own
// arguments, and a field left empty is left out so the tool's default
// stands (the 200-line tail every read used to be stuck on).
func TestGrainLogArgsCarryTheQuery(t *testing.T) {
	got := grainLogArgs("", namespace.GrainLogQuery{})
	if len(got) != 1 || got["source"] != "daemon" {
		t.Errorf("grainLogArgs(empty) = %v, want only source=daemon", got)
	}
	got = grainLogArgs("upgrade", namespace.GrainLogQuery{Since: "2026-10-29T00:00:00Z", Until: "2026-10-29T06:00:00Z",
		MinPriority: "err", Contains: "dispatch", MaxLines: 900})
	want := map[string]any{"source": "upgrade", "since": "2026-10-29T00:00:00Z", "until": "2026-10-29T06:00:00Z",
		"min_priority": "err", "contains": "dispatch", "max_lines": 900}
	if len(got) != len(want) {
		t.Fatalf("grainLogArgs = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("grainLogArgs[%q] = %v, want %v", k, got[k], v)
		}
	}
}

// A `grain log` read through deploy://grain.log is audited as the task
// whose sandbox made it. A walkthrough found every such read logged as
// "task (no task)" (docs/test-runs/2026-10-31-sandbox-grain-cli.md, F2):
// the namespace path built read_grain_log without its TaskID.
func TestSandboxGrainLogReadIsAuditedAsItsTask(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	dataDir := t.TempDir()
	if err := secrets.Open(SecretsConfig(dataDir)).Set("widget-api", "token", []byte("s3cret-value")); err != nil {
		t.Fatal(err)
	}
	d := sandboxDeploy{secretsDir: SecretsDir(dataDir), logSources: []grainlog.Source{{Name: "daemon", What: "test journal",
		Read: func(context.Context, systemlog.Query) ([]string, error) { return []string{"a line"}, nil }}}}
	if _, err := d.ReadGrainLog(context.Background(), "", namespace.GrainLogQuery{MaxLines: 5, TaskID: "42"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "read_grain_log: task 42 read daemon") {
		t.Errorf("audit line = %q, want it to name task 42", got)
	}
}

// TestSandboxProxyPagesPipelineChanges reads deploy://pipeline-changes a
// page at a time, as `grain read --cursor` does: each page ends with the
// command that reads the next, and running it returns the changes after
// the first page's rather than the first page again.
func TestSandboxProxyPagesPipelineChanges(t *testing.T) {
	srv, store, _ := stage2Proxy(t, stage2Scopes{
		"steward": {TaskID: "t-1", RunID: "run-1", MayReadPipeline: true},
	})
	ctx := context.Background()
	// A base-branch run that started and finished within the feed's
	// lookback: two changes, so one to a page is two pages.
	started := time.Now().Add(-2 * time.Hour)
	finished := started.Add(time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: model.RepoRef{Owner: "acme", Name: "widgets"},
		Branch: "main", RunID: "run-7", Commit: "abc123", StartedAt: started, FinishedAt: &finished,
		Conclusion: "failure", Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	status, all := stage2Get(t, srv, "steward", "/fs/deploy/pipeline-changes")
	if status != http.StatusOK {
		t.Fatalf("deploy://pipeline-changes: status=%d body=%q", status, all)
	}
	entries := func(body string) []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if !strings.HasPrefix(line, "next: ") && !strings.HasPrefix(line, "(") {
				out = append(out, line)
			}
		}
		return out
	}
	everything := entries(all)
	if len(everything) < 2 {
		t.Fatalf("want at least two changes to page through, got:\n%s", all)
	}

	status, first := stage2Get(t, srv, "steward", "/fs/deploy/pipeline-changes?page=1")
	if status != http.StatusOK {
		t.Fatalf("?page=1: status=%d body=%q", status, first)
	}
	if got := entries(first); len(got) != 1 || got[0] != everything[0] {
		t.Fatalf("first page = %q, want only %q", got, everything[0])
	}
	if !strings.Contains(first, "(more changes follow") {
		t.Errorf("first page does not say more follow:\n%s", first)
	}
	_, nextLine, _ := strings.Cut(first, "next: ")
	nextLine = strings.TrimSpace(nextLine)
	cursor, ok := strings.CutPrefix(nextLine, "grain read deploy://pipeline-changes --cursor ")
	if !ok || !strings.HasSuffix(cursor, " --page 1") {
		t.Fatalf("next line = %q, want the grain read command for the next page", nextLine)
	}
	cursor = strings.TrimSuffix(cursor, " --page 1")

	for _, param := range []string{"cursor", "since"} {
		status, second := stage2Get(t, srv, "steward", "/fs/deploy/pipeline-changes?page=1&"+param+"="+url.QueryEscape(cursor))
		if status != http.StatusOK {
			t.Fatalf("?%s=: status=%d body=%q", param, status, second)
		}
		if got := entries(second); len(got) != 1 || got[0] != everything[1] {
			t.Errorf("?%s= second page = %q, want only %q", param, got, everything[1])
		}
	}
	if status, body := stage2Get(t, srv, "steward", "/fs/deploy/pipeline-changes?page=lots"); status != http.StatusBadRequest {
		t.Errorf("?page=lots: status=%d, want 400; body=%q", status, body)
	}
}
