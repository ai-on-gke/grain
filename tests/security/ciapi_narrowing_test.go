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

package security

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/ciapi"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type securityStubCI struct {
	ci.NopRunner
	runs      map[string]ci.Run
	artifacts map[ci.ArtifactRef]string
}

func (r *securityStubCI) Get(id string) (ci.Run, bool) {
	run, ok := r.runs[id]
	return run, ok
}

func (r *securityStubCI) List() []ci.Run {
	out := make([]ci.Run, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run)
	}
	return out
}

func (r *securityStubCI) RunArtifacts(_ context.Context, runID string) ([]ci.Artifact, bool, error) {
	var out []ci.Artifact
	for ref, body := range r.artifacts {
		if ref.Run == runID {
			out = append(out, ci.Artifact{Run: ref.Run, Job: ref.Job, Name: ref.Name, Stored: true, Bytes: int64(len(body))})
		}
	}
	return out, true, nil
}

func (r *securityStubCI) OpenArtifact(_ context.Context, ref ci.ArtifactRef) (io.ReadCloser, ci.Artifact, error) {
	body, ok := r.artifacts[ref]
	if !ok {
		return nil, ci.Artifact{}, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(body)), ci.Artifact{Run: ref.Run, Job: ref.Job, Name: ref.Name, Stored: true, Bytes: int64(len(body))}, nil
}

type securityStubPersistentSandboxes struct {
	list []model.PersistentSandboxStatus
}

func (s *securityStubPersistentSandboxes) List(context.Context) ([]model.PersistentSandboxStatus, error) {
	return append([]model.PersistentSandboxStatus(nil), s.list...), nil
}
func (*securityStubPersistentSandboxes) Logs(string) (string, bool) { return "", false }
func (*securityStubPersistentSandboxes) Control(context.Context, string, string) error {
	return errors.New("unused")
}
func (*securityStubPersistentSandboxes) Dial(context.Context, string) (net.Conn, error) {
	return nil, errors.New("unused")
}
func (*securityStubPersistentSandboxes) StripPrefix(string) (bool, bool) { return false, false }

type securityCancelOnlyCtx struct{ context.Context }

func (securityCancelOnlyCtx) Value(any) any { return nil }

type securityInProcessAPI struct {
	h http.Handler
}

func (a *securityInProcessAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(securityCancelOnlyCtx{req.Context()})
	clone.RemoteAddr = ""
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, clone)
	return w.Result(), nil
}

// TestCIAPINarrowingPreventsCrossRepoLeaks pins the repository isolation
// boundary for a ci_api_repos job token against the real ui.Server,
// ciapi.Endpoint, and namespace.Server, including the three cross-repo leaks
// found during the 2026-11-16 walkthrough (paged /api/schedules search/filter
// oracle, paged /api/persistent-sandboxes cursor disclosure, and
// /api/templates cross-repo reads/writes/grants filtering) as well as foreign
// tasks, no-repo tasks, /api/fs/*, CI runs/artifacts, and from-template holds.
func TestCIAPINarrowingPreventsCrossRepoLeaks(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "acme", Name: "secret-other"}
	now := time.Now().UTC()

	runner := &securityStubCI{
		runs: map[string]ci.Run{
			"run-widgets": {ID: "run-widgets", Repo: widgets.String(), Conclusion: ci.ConclusionSuccess, StartedAt: &now},
			"run-other":   {ID: "run-other", Repo: other.String(), Conclusion: ci.ConclusionSuccess, StartedAt: &now},
		},
		artifacts: map[ci.ArtifactRef]string{
			{Run: "run-widgets", Job: "build", Name: "widgets-report"}: "widgets ok",
			{Run: "run-other", Job: "build", Name: "secret-report"}:    "secret other",
		},
	}
	sandboxes := &securityStubPersistentSandboxes{
		list: []model.PersistentSandboxStatus{
			{Name: "alpha-secret-other-svc", Repo: other.String(), State: "running"},
			{Name: "zulu-widgets-svc", Repo: widgets.String(), State: "running"},
		},
	}

	ciStore := ci.NewRunStore(16)
	for _, run := range runner.runs {
		ciStore.Put(run)
	}

	api := &securityInProcessAPI{}
	ns := &namespace.Server{
		CIStore: ciStore,
	}
	daemon.WireSandboxNamespace(ns, daemon.SandboxNamespaceConfig{
		DataDir:      dataDir,
		Store:        store,
		InsecureHTTP: true,
		API:          api,
	})
	client := ui.NewClient(ui.Config{
		DataDir:             dataDir,
		Actor:               ui.DefaultActor("operator"),
		Capabilities:        ui.OfferedCapabilities(),
		PullRequests:        daemon.PullRequestGate{},
		CI:                  runner,
		PersistentSandboxes: sandboxes,
	}, store)
	uiSrv := ui.NewServerWithClient(client)
	uiSrv.SetNamespace(func() *namespace.Server { return ns })
	api.h = uiSrv

	ownTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Widgets visible task",
		Description: "widgets body",
		Repo:        widgets.String(),
	})
	if err != nil {
		t.Fatalf("creating own task: %v", err)
	}
	otherTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Cobalt zebra foreign task",
		Description: "secret other body",
		Repo:        other.String(),
	})
	if err != nil {
		t.Fatalf("creating foreign task: %v", err)
	}
	noRepoTask, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       "Cobalt zebra standalone task",
		Description: "deployment standalone body",
		NoRepo:      true,
	})
	if err != nil {
		t.Fatalf("creating no-repo task: %v", err)
	}

	if _, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title:       "Widgets daily",
		Description: "own",
		Repo:        widgets.String(),
		Base:        "main",
		Recurrence:  ui.Recurrence{Kind: "daily", TimeOfDay: "09:00"},
	}); err != nil {
		t.Fatalf("creating own schedule: %v", err)
	}
	if _, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title:       "Cobalt zebra secret schedule",
		Description: "foreign",
		Repo:        other.String(),
		Base:        "main",
		Recurrence:  ui.Recurrence{Kind: "daily", TimeOfDay: "09:00"},
	}); err != nil {
		t.Fatalf("creating foreign schedule: %v", err)
	}

	if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:        "widgets-ok",
		Title:       "Widgets clean",
		Description: "safe",
		Repo:        widgets.String(),
	}); err != nil {
		t.Fatalf("creating clean template: %v", err)
	}
	if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:        "widgets-reads-other",
		Title:       "Cobalt zebra cross-read",
		Description: "cross-repo read",
		Repo:        widgets.String(),
		Reads:       []string{other.String()},
	}); err != nil {
		t.Fatalf("creating cross-read template: %v", err)
	}
	if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:        "widgets-writes-other",
		Title:       "Cobalt zebra cross-write",
		Description: "cross-repo write",
		Repo:        widgets.String(),
		Writes:      []ui.WriteTarget{{Repo: other.String()}},
	}); err != nil {
		t.Fatalf("creating cross-write template: %v", err)
	}
	if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name:         "widgets-api-other",
		Title:        "Cobalt zebra cross-api",
		Description:  "cross-repo api grant",
		Repo:         widgets.String(),
		Capabilities: []string{"api:tasks:read@" + other.String()},
	}); err != nil {
		t.Fatalf("creating cross-api template: %v", err)
	}

	gate := ciapi.NewEndpoint()
	gate.Log = func(string, ...any) {}
	gate.SetURL("http://192.0.2.1:9000")
	gate.SetUpstream(uiSrv)
	_, token, err := gate.Tokens.Mint(ciapi.Grant{
		Repo:        widgets,
		RunID:       "ci-sec-test",
		Expires:     now.Add(time.Hour),
		AnyTemplate: true,
		Scopes: []model.APIScope{
			"ci:read", "tasks:read", "dispatch:read", "repos:read",
			"releases:read", "schedules:read", "sandboxes:read",
			"templates:read", "suites:read", "tasks:from-template",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, r)
		req.RemoteAddr = "10.0.0.5:4242"
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, req)
		return w
	}

	// 1. Paged/filtered schedules must not reveal foreign schedules or counts.
	for _, path := range []string{
		"/api/schedules",
		"/api/schedules?limit=1",
		"/api/schedules?limit=1&q=cobalt-zebra",
		"/api/schedules?limit=1&f=repo:any:acme/secret-other",
	} {
		w := do(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "secret-other") || strings.Contains(w.Body.String(), "Cobalt zebra") {
			t.Errorf("GET %s leaked foreign schedule data: %s", path, w.Body)
		}
		if strings.Contains(path, "cobalt-zebra") || strings.Contains(path, "secret-other") {
			var resp struct {
				Items []ui.Schedule `json:"items"`
				Total int           `json:"total"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding %s: %v", path, err)
			}
			if resp.Total != 0 || len(resp.Items) != 0 {
				t.Errorf("GET %s leaked foreign count/oracle: total=%d items=%d", path, resp.Total, len(resp.Items))
			}
		}
	}

	// 2. Paged persistent sandboxes must not leak foreign names via nextCursor, items, or total.
	{
		w := do(http.MethodGet, "/api/persistent-sandboxes?limit=1", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/persistent-sandboxes?limit=1 = %d %s", w.Code, w.Body)
		}
		var resp struct {
			Items      []model.PersistentSandboxStatus `json:"items"`
			Total      int                             `json:"total"`
			NextCursor string                          `json:"nextCursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].Name != "zulu-widgets-svc" {
			t.Errorf("GET /api/persistent-sandboxes?limit=1 = total=%d items=%+v, want only zulu-widgets-svc", resp.Total, resp.Items)
		}
		if resp.NextCursor != "" {
			decoded, _ := base64.RawURLEncoding.DecodeString(resp.NextCursor)
			t.Errorf("GET /api/persistent-sandboxes?limit=1 leaked nextCursor %q (%q)", resp.NextCursor, string(decoded))
		}
	}
	{
		w := do(http.MethodGet, "/api/persistent-sandboxes?limit=1&q=alpha-secret-other-svc", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/persistent-sandboxes?q=foreign = %d %s", w.Code, w.Body)
		}
		var resp struct {
			Items []model.PersistentSandboxStatus `json:"items"`
			Total int                             `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Total != 0 || len(resp.Items) != 0 {
			t.Errorf("GET /api/persistent-sandboxes?q=foreign leaked total=%d items=%d", resp.Total, len(resp.Items))
		}
	}

	// 3. GET /api/templates must hide templates targeting own repo that reference another repo.
	{
		w := do(http.MethodGet, "/api/templates", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/templates = %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "secret-other") || strings.Contains(w.Body.String(), "Cobalt zebra") {
			t.Errorf("GET /api/templates leaked cross-repo template: %s", w.Body)
		}
		if !strings.Contains(w.Body.String(), "widgets-ok") {
			t.Errorf("GET /api/templates dropped clean own template: %s", w.Body)
		}
	}

	// 4. Foreign and no-repo task routes, fs, CI runs/artifacts, and deployment-wide routes stay refused.
	for _, refused := range []string{
		"/api/tasks/" + otherTask.ID,
		"/api/tasks/" + noRepoTask.ID,
		"/api/tasks/" + otherTask.ID + "/messages",
		"/api/tasks/" + noRepoTask.ID + "/prompt",
		"/api/tasks/" + otherTask.ID + "/context",
		"/api/fs/task/",
		"/api/fs/task/" + otherTask.ID + "/summary.md",
		"/api/fs/task/" + noRepoTask.ID + "/summary.md",
		"/api/fs/ci/" + other.String(),
		"/api/ci/runs/run-other",
		"/api/ci/runs/run-other/artifacts/build/secret-report",
		"/api/repos/" + other.String() + "/settings",
		"/api/settings",
		"/api/pause",
		"/api/pipeline-pause",
		"/api/release-approval-key",
		"/api/git-ambient-sources",
	} {
		w := do(http.MethodGet, refused, "")
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d %s, want 403", refused, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "Cobalt zebra") || strings.Contains(w.Body.String(), "secret other") {
			t.Errorf("GET %s leaked foreign payload in error body: %s", refused, w.Body)
		}
	}

	// Own-repo task, artifact, and /api/fs/ci/ continue working cleanly.
	if w := do(http.MethodGet, "/api/tasks/"+ownTask.ID, ""); w.Code != http.StatusOK {
		t.Errorf("GET own task = %d %s, want 200", w.Code, w.Body)
	}
	if w := do(http.MethodGet, "/api/ci/runs/run-widgets/artifacts/build/widgets-report", ""); w.Code != http.StatusOK || w.Body.String() != "widgets ok" {
		t.Errorf("GET own artifact = %d %q, want 200 widgets ok", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/api/fs/ci/", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), other.String()) {
		t.Errorf("GET /api/fs/ci/ = %d %q, want 200 without %s", w.Code, w.Body.String(), other)
	}
}
