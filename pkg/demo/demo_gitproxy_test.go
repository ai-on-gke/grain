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

package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestDemoGitProxyServesNamespaceAndSandboxCLI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the simulated GitHub needs git")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := withAccessCode(ui.Config{
		DataDir:  dir,
		SourceDB: db,
		DBDriver: "sqlite",
		CI:       &demoFakeCI{},
	})
	var bare string
	cfg, bare, err = configureGitHubSimDemo(dir, cfg, repo)
	if err != nil {
		t.Fatalf("configureGitHubSimDemo: %v", err)
	}
	cfg, err = configureSecretsDemo(dir, cfg)
	if err != nil {
		t.Fatalf("configureSecretsDemo: %v", err)
	}
	gp, err := configureGitProxyDemo(dir, store, repo, bare, "demo-gh-token", false)
	if err != nil {
		t.Fatalf("configureGitProxyDemo: %v", err)
	}
	client := ui.NewClient(cfg, store)
	srv := gitProxyDemoHandler(ui.NewServerWithClient(client), gp)
	gp.wireServer(srv, client, cfg)

	now := time.Now().UTC()
	task := model.Task{
		ID:        "99",
		Title:     "Walkthrough sandbox CLI test",
		Body:      "Verify demo git proxy routes",
		Target:    &repo,
		CreatedAt: &now,
		Grants: []model.Grant{
			{Capability: model.SelfDebugCapability, Via: model.GrantByLabel},
			{Capability: "grain-ci", Via: model.GrantByGrain},
			{Capability: "deployment-health", Via: model.GrantByLabel},
			{Capability: "grain-log", Via: model.GrantByLabel},
			{Capability: "read-issues", Via: model.GrantByLabel},
			{Capability: "db-read", Via: model.GrantByLabel},
		},
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("PutTask: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/e2e/git-proxy/runs", bytes.NewBufferString(`{"taskId":"99"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, presentAccessCode(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/e2e/git-proxy/runs: %d %s", rec.Code, rec.Body.String())
	}
	var run demoGitProxyRun
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("decoding run: %v", err)
	}

	getProxy := func(path string) (int, string) {
		r, err := http.NewRequest(http.MethodGet, gp.url+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s): %v", path, err)
		}
		r.SetBasicAuth("x-access-token", run.Token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/fs/config/settings/settings.yaml", want: "default_model: gemini-2.5-pro"},
		{path: "/fs/deploy/health", want: `"sandboxCount"`},
		{path: "/fs/deploy/grain.log", want: "started daemon log"},
		{path: "/cli/issues?repo=acme/widgets", want: "Support sandbox CLI subcommands in demo mode"},
		{path: "/cli/db/query?sql=SELECT+id,title+FROM+task+WHERE+id='99'", want: "Walkthrough sandbox CLI test"},
	} {
		code, body := getProxy(tc.path)
		if code != http.StatusOK || !strings.Contains(body, tc.want) {
			t.Errorf("GET %s = %d %q, want 200 containing %q", tc.path, code, body, tc.want)
		}
	}
}
