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

package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const fsTestSecret = "fs-api-secret-value-7788"

// fsTestTasks is task:// over two tasks, one in each repository.
type fsTestTasks struct{}

func (fsTestTasks) ListTaskIDs(context.Context) ([]string, error) { return []string{"7", "8"}, nil }

func (fsTestTasks) ReadTaskView(_ context.Context, id string) (*namespace.TaskView, error) {
	switch id {
	case "7":
		return &namespace.TaskView{ID: "7", Repo: "acme/widgets", Record: "task 7 in widgets\n"}, nil
	case "8":
		return &namespace.TaskView{ID: "8", Repo: "acme/secret", Record: "task 8 in secret\n"}, nil
	}
	return nil, namespace.ErrNotFound
}

// fsTestDeploy is deploy://, whose merge queue names both repositories.
type fsTestDeploy struct{}

func (fsTestDeploy) ReadMergeQueue(context.Context) (string, error) {
	return "acme/widgets #1\nacme/secret #2\n", nil
}

func (fsTestDeploy) ReadMergeQueueFor(_ context.Context, permits func(string) bool) (string, error) {
	var b strings.Builder
	for _, repo := range []string{"acme/widgets", "acme/secret"} {
		if permits(repo) {
			b.WriteString(repo + " queued\n")
		}
	}
	return b.String(), nil
}

func (fsTestDeploy) ReadMetrics(context.Context, string, namespace.MetricsQuery) (string, error) {
	return "metrics\n", nil
}
func (fsTestDeploy) ReadPipelineChanges(context.Context, string, int) (string, error) {
	return "changes\n", nil
}
func (fsTestDeploy) ReadHealth(context.Context) (string, error)            { return "healthy\n", nil }
func (fsTestDeploy) ReadState(context.Context, string) (string, error)     { return "state\n", nil }
func (fsTestDeploy) ListStateTables(context.Context) ([]string, error)     { return nil, nil }
func (fsTestDeploy) ListGrainLogSources(context.Context) ([]string, error) { return nil, nil }
func (fsTestDeploy) ReadGrainLog(context.Context, string, namespace.GrainLogQuery) (string, error) {
	return "log\n", nil
}

func writeFSFile(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fsTestNamespace is a namespace over every scheme, with a CI run in each
// of acme/widgets and acme/secret whose log carries fsTestSecret.
func fsTestNamespace(t *testing.T) *namespace.Server {
	t.Helper()
	runs := ci.NewRunStore(10)
	for _, r := range []struct{ id, repo string }{{"run-w", "acme/widgets"}, {"run-s", "acme/secret"}} {
		runs.Put(ci.Run{ID: r.id, Repo: r.repo, Conclusion: ci.ConclusionFailure, Jobs: []ci.JobRun{{
			JobID: "go", Name: "go", Conclusion: ci.ConclusionFailure,
			Steps: []ci.StepRun{{Name: "Test", Conclusion: ci.ConclusionFailure,
				Output: "line one\nusing token " + fsTestSecret + "\nFAIL: TestThing\nline four\n"}},
		}}})
	}
	src, memory, config := t.TempDir(), t.TempDir(), t.TempDir()
	writeFSFile(t, src, "README.md", "grain source\n")
	writeFSFile(t, memory, "global.md", "global memory\n")
	writeFSFile(t, memory, "repos/acme/widgets.md", "widgets memory\n")
	writeFSFile(t, memory, "repos/acme/secret.md", "secret memory\n")
	writeFSFile(t, config, "README.md", "state repository\n")
	return &namespace.Server{
		CIStore: runs, SrcDir: src, MemoryDir: memory, ConfigDir: config,
		Tasks: fsTestTasks{}, Deploy: fsTestDeploy{},
		NewRedactor: func() *redact.Redactor {
			return redact.New([]redact.KnownValue{{Label: "secret fs-test", Value: fsTestSecret}})
		},
	}
}

// fsTestServer is a server serving fsTestNamespace at /api/fs/, with
// tasks 7 (acme/widgets) and 8 (acme/secret) in its store and external
// agent tokens enabled.
func fsTestServer(t *testing.T) (*ui.Server, *ui.Client, *namespace.Server) {
	t.Helper()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ctx := context.Background()
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ExternalAgentAPI = true
	if err := client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	seedAPITask(t, client, "7", "acme/widgets")
	seedAPITask(t, client, "8", "acme/secret")
	ns := fsTestNamespace(t)
	srv.SetNamespace(func() *namespace.Server { return ns })
	return srv, client, ns
}

func issueFSToken(t *testing.T, client *ui.Client, name string, scopes []string, repos ...string) string {
	t.Helper()
	person := ui.WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	issued, err := client.CreateExternalAgentToken(person, ui.CreateExternalAgentTokenRequest{
		Name: name, Scopes: scopes, Repos: repos, RateLimit: 100000, MaxConcurrent: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Token
}

func fsGet(t *testing.T, srv *ui.Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHeaders(t, srv, http.MethodGet, path, "", headers)
}

func TestAPIFsPerSchemeScopesRepoLimitsAndExternalSrcRefusal(t *testing.T) {
	ui.LogToTest(t)
	srv, client, _ := fsTestServer(t)
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

	type probe struct {
		path     string
		want     int
		contains string
		absent   string
	}
	check := func(t *testing.T, who string, headers map[string]string, probes []probe) {
		t.Helper()
		for _, p := range probes {
			rec := fsGet(t, srv, p.path, headers)
			body := rec.Body.String()
			if rec.Code != p.want {
				t.Errorf("%s GET %s: status %d, want %d: %s", who, p.path, rec.Code, p.want, strings.TrimSpace(body))
				continue
			}
			if p.contains != "" && !strings.Contains(body, p.contains) {
				t.Errorf("%s GET %s: body %q does not say %q", who, p.path, body, p.contains)
			}
			if p.absent != "" && strings.Contains(body, p.absent) {
				t.Errorf("%s GET %s: body %q names %q, which it may not read", who, p.path, body, p.absent)
			}
		}
	}

	// ci:read on acme/widgets alone: that repository's CI, the merge
	// queue narrowed to it, and nothing of any other scheme.
	ciWidgets := issueFSToken(t, client, "ci-widgets", []string{"ci:read"}, "acme/widgets")
	check(t, "ci:read@acme/widgets", bearer(ciWidgets), []probe{
		{path: "/api/fs/ci/acme/widgets/run-w/go/Test.log", want: http.StatusOK, contains: "FAIL: TestThing"},
		{path: "/api/fs/ci/acme/secret/run-s/go/Test.log", want: http.StatusForbidden, contains: "acme/secret"},
		{path: "/api/fs/ci/?list", want: http.StatusOK, absent: "secret"},
		{path: "/api/fs/deploy/merge-queue", want: http.StatusOK, contains: "acme/widgets queued", absent: "acme/secret"},
		{path: "/api/fs/deploy/merge-queue.md", want: http.StatusOK, absent: "acme/secret"},
		{path: "/api/fs/deploy/health", want: http.StatusForbidden, contains: "deployment:read"},
		{path: "/api/fs/task/7/record.md", want: http.StatusForbidden, contains: "tasks:read"},
		{path: "/api/fs/config/README.md", want: http.StatusForbidden, contains: "settings:read"},
		{path: "/api/fs/memory/global.md", want: http.StatusForbidden, contains: "memory:read"},
		{path: "/api/fs/src/README.md", want: http.StatusForbidden, contains: "deployment:read"},
	})

	// tasks:read and memory:read on acme/widgets: its task and its
	// memory, not acme/secret's.
	readWidgets := issueFSToken(t, client, "read-widgets", []string{"tasks:read", "memory:read"}, "acme/widgets")
	check(t, "tasks+memory:read@acme/widgets", bearer(readWidgets), []probe{
		{path: "/api/fs/task/7/record.md", want: http.StatusOK, contains: "task 7 in widgets"},
		{path: "/api/fs/task/8/record.md", want: http.StatusForbidden},
		{path: "/api/fs/memory/repos/acme/widgets.md", want: http.StatusOK, contains: "widgets memory"},
		{path: "/api/fs/memory/repos/acme/secret.md", want: http.StatusForbidden},
		{path: "/api/fs/ci/acme/widgets/run-w/go/Test.log", want: http.StatusForbidden, contains: "ci:read"},
	})

	// deployment:read and settings:read everywhere: deploy:// and
	// config:// whole, the whole merge queue -- and still no src://,
	// which an external token reads only once it is granted
	// deployment:src.
	deploy := issueFSToken(t, client, "deploy", []string{"deployment:read", "settings:read"})
	check(t, "deployment:read", bearer(deploy), []probe{
		{path: "/api/fs/deploy/health", want: http.StatusOK, contains: "healthy"},
		{path: "/api/fs/deploy/merge-queue", want: http.StatusOK, contains: "acme/secret #2"},
		{path: "/api/fs/config/README.md", want: http.StatusOK, contains: "state repository"},
		{path: "/api/fs/src/README.md", want: http.StatusForbidden, contains: "deployment:src"},
	})
	src := issueFSToken(t, client, "src", []string{"deployment:read", "deployment:src"})
	check(t, "deployment:read+deployment:src", bearer(src), []probe{
		{path: "/api/fs/src/README.md", want: http.StatusOK, contains: "grain source"},
	})

	// A task's own run, through the agent API with its api grants: held
	// to them the same way, and src:// is its under deployment:read.
	seedAPITask(t, client, "9", "acme/widgets", "api:ci:read@acme/widgets", "api:deployment:read")
	acting := map[string]string{ui.ActingTaskHeader: "9"}
	check(t, "task 9", acting, []probe{
		{path: "/api/fs/ci/acme/widgets/run-w/go/Test.log", want: http.StatusOK, contains: "FAIL: TestThing"},
		{path: "/api/fs/ci/acme/secret/run-s/go/Test.log", want: http.StatusForbidden},
		{path: "/api/fs/src/README.md", want: http.StatusOK, contains: "grain source"},
		{path: "/api/fs/task/8/record.md", want: http.StatusForbidden},
	})

	// The person at the UI -- the deployment's owner -- reads all of it.
	check(t, "the operator", nil, []probe{
		{path: "/api/fs/ci/acme/secret/run-s/go/Test.log", want: http.StatusOK, contains: "FAIL: TestThing"},
		{path: "/api/fs/task/8/record.md", want: http.StatusOK, contains: "task 8 in secret"},
		{path: "/api/fs/memory/repos/acme/secret.md", want: http.StatusOK, contains: "secret memory"},
		{path: "/api/fs/src/README.md", want: http.StatusOK, contains: "grain source"},
		{path: "/api/fs/deploy/merge-queue", want: http.StatusOK, contains: "acme/secret #2"},
	})
}

// TestAPIFsSecretRedactionRangeAndQueryParityWithSandboxProxy holds
// /api/fs/ to the sandbox proxy's /fs/: both are namespace.Server.ServeHTTP
// (pkg/gitproxy/namespace.go hands the proxy's request to it as it came),
// so every read here is asked of both and must be answered alike --
// redacted, ranged, tailed, grepped, globbed, listed and capped.
func TestAPIFsSecretRedactionRangeAndQueryParityWithSandboxProxy(t *testing.T) {
	ui.LogToTest(t)
	srv, _, ns := fsTestServer(t)
	ns.MaxResponseBytes = 1 << 20
	proxyScope := namespace.Scope{MayReadCI: true, MayReadAllCI: true}

	sandbox := func(path string, header http.Header) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range header {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		ns.ServeHTTP(rec, req, proxyScope)
		return rec
	}
	const log = "ci/acme/widgets/run-w/go/Test.log"
	for _, tc := range []struct {
		name, query string
		header      http.Header
		wantStatus  int
	}{
		{name: "whole file", wantStatus: http.StatusOK},
		{name: "range", header: http.Header{"Range": {"bytes=0-11"}}, wantStatus: http.StatusPartialContent},
		{name: "tail", query: "?tail=10", wantStatus: http.StatusOK},
		{name: "grep", query: "?grep=FAIL&n=1", wantStatus: http.StatusOK},
		{name: "grep for the secret", query: "?grep=token&n=1", wantStatus: http.StatusOK},
		{name: "offset and limit", query: "?offset=5&limit=8", wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := fsGet(t, srv, "/api/fs/"+log+tc.query, headerMap(tc.header))
			proxy := sandbox("/fs/"+log+tc.query, tc.header)
			if api.Code != tc.wantStatus || proxy.Code != tc.wantStatus {
				t.Fatalf("status: /api/fs/ %d, /fs/ %d, want %d (%s)", api.Code, proxy.Code, tc.wantStatus, api.Body.String())
			}
			if api.Body.String() != proxy.Body.String() {
				t.Errorf("/api/fs/ answered %q, /fs/ %q", api.Body.String(), proxy.Body.String())
			}
			if strings.Contains(api.Body.String(), fsTestSecret) {
				t.Errorf("/api/fs/ leaked the secret: %q", api.Body.String())
			}
			for _, h := range []string{"Content-Range", "X-Grain-Total-Size", "X-Grain-Offset", "X-Grain-Matches", "Accept-Ranges"} {
				if api.Header().Get(h) != proxy.Header().Get(h) {
					t.Errorf("%s: /api/fs/ %q, /fs/ %q", h, api.Header().Get(h), proxy.Header().Get(h))
				}
			}
		})
	}
	whole := fsGet(t, srv, "/api/fs/"+log, nil).Body.String()
	if !strings.Contains(whole, "[redacted:") {
		t.Errorf("the secret was not redacted where it stood: %q", whole)
	}

	for _, tc := range []struct{ name, path string }{
		{"list", "ci/acme/widgets/run-w?list"},
		{"glob", "ci/acme/widgets?glob=**/*.log"},
		{"grep a subtree", "ci/acme/widgets?grep=FAIL&n=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := fsGet(t, srv, "/api/fs/"+tc.path, nil)
			proxy := sandbox("/fs/"+tc.path, nil)
			if api.Code != http.StatusOK || api.Code != proxy.Code || api.Body.String() != proxy.Body.String() {
				t.Errorf("/api/fs/%s: %d %q; /fs/: %d %q", tc.path, api.Code, api.Body.String(), proxy.Code, proxy.Body.String())
			}
		})
	}

	// The response cap is the namespace's, on both.
	ns.MaxResponseBytes = 16
	api := fsGet(t, srv, "/api/fs/"+log, nil)
	proxy := sandbox("/fs/"+log, nil)
	if api.Code != proxy.Code || api.Body.String() != proxy.Body.String() {
		t.Errorf("capped: /api/fs/ %d %q, /fs/ %d %q", api.Code, api.Body.String(), proxy.Code, proxy.Body.String())
	}
	if strings.Contains(api.Body.String(), "line four") {
		t.Errorf("a 16-byte cap answered the whole file: %q", api.Body.String())
	}
}

func headerMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := map[string]string{}
	for k := range h {
		out[k] = h.Get(k)
	}
	return out
}
