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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestAgentRepoDefaultRoutesListOnlyTheGrantedRepo pins what lets
// GET /api/templates, /api/memory, /api/memory/documents and
// /api/suite-runs sit in agentRepoDefaultRoutes: the gate admits a task
// granted their scope on one repository alone to each of them without
// ?repo=, by defaulting it to that repository, so the list it is answered
// must hold that repository's rows only -- not another repository's, and
// not the deployment's own. Before the routes took ?repo= at all, such a
// task was refused them outright; the default is only safe for as long as
// each handler actually narrows by it.
func TestAgentRepoDefaultRoutesListOnlyTheGrantedRepo(t *testing.T) {
	srv, client := testServer(t)
	ctx := context.Background()
	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})
	seedAPITask(t, client, "900", "acme/widgets",
		"api:templates:read@acme/widgets", "api:memory:read@acme/widgets", "api:suites:read@acme/widgets")

	widgets := model.RepoRef{Owner: "acme", Name: "widgets"}
	other := model.RepoRef{Owner: "other", Name: "thing"}
	for _, m := range []model.Memory{
		{Repo: &widgets, Text: "WIDGETS-ONLY memory"},
		{Repo: &other, Text: "OTHER-REPO memory"},
		{Text: "DEPLOYMENT-WIDE memory"},
	} {
		if err := client.Store.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []model.MemoryDocument{
		{Repo: &widgets, Name: "widgets-doc", Summary: "WIDGETS-ONLY document", Text: "w"},
		{Repo: &other, Name: "other-doc", Summary: "OTHER-REPO document", Text: "o"},
		{Name: "deployment-doc", Summary: "DEPLOYMENT-WIDE document", Text: "d"},
	} {
		if err := client.Store.PutMemoryDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	var suiteTemplate ui.Template
	for _, req := range []ui.CreateTemplateRequest{
		{Name: "widgets-template", Title: "WIDGETS-ONLY template", Repo: "acme/widgets"},
		{Name: "other-template", Title: "OTHER-REPO template", Repo: "other/thing"},
		{Name: "unbound-template", Title: "DEPLOYMENT-WIDE template"},
	} {
		tmpl, err := client.CreateTemplate(humanCtx, req)
		if err != nil {
			t.Fatalf("creating template %s: %v", req.Name, err)
		}
		if req.Repo == "" {
			suiteTemplate = tmpl
		}
	}
	suite, err := client.CreateSuite(humanCtx, ui.CreateSuiteRequest{
		Name: "qualify", TemplateIDs: []string{suiteTemplate.ID}, Mode: "count", Count: 1,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	for _, repo := range []string{"acme/widgets", "other/thing"} {
		if _, err := client.CreateSuiteRun(humanCtx, ui.CreateSuiteRunRequest{SuiteID: suite.ID, Repo: repo, Base: "main"}); err != nil {
			t.Fatalf("starting a suite run on %s: %v", repo, err)
		}
	}

	for _, tc := range []struct {
		path       string
		want, deny []string
	}{
		{"/api/templates", []string{"WIDGETS-ONLY"}, []string{"OTHER-REPO", "DEPLOYMENT-WIDE"}},
		{"/api/templates?fields=summary", []string{"WIDGETS-ONLY"}, []string{"OTHER-REPO", "DEPLOYMENT-WIDE"}},
		{"/api/memory", []string{"WIDGETS-ONLY"}, []string{"OTHER-REPO", "DEPLOYMENT-WIDE"}},
		{"/api/memory/documents", []string{"WIDGETS-ONLY"}, []string{"OTHER-REPO", "DEPLOYMENT-WIDE"}},
		{"/api/suite-runs", []string{"acme/widgets"}, []string{"other/thing"}},
	} {
		for _, path := range []string{tc.path, withQuery(tc.path, "repo=acme/widgets"), withQuery(tc.path, "repo=ACME/Widgets")} {
			code, body := asAgent(t, srv, "900", http.MethodGet, path, "", "")
			if code != http.StatusOK {
				t.Errorf("GET %s as a task granted acme/widgets alone: %d %s", path, code, body)
				continue
			}
			for _, marker := range tc.want {
				if !strings.Contains(body, marker) {
					t.Errorf("GET %s answered without %q: %s", path, marker, body)
				}
			}
			for _, marker := range tc.deny {
				if strings.Contains(body, marker) {
					t.Errorf("GET %s leaked %q to a task granted acme/widgets alone: %s", path, marker, body)
				}
			}
		}
		// Naming the repository the grant does not reach, or asking for
		// the deployment's own scope by name, is still refused rather
		// than narrowed.
		for _, denied := range []string{"repo=other/thing"} {
			path := withQuery(tc.path, denied)
			if code, body := asAgent(t, srv, "900", http.MethodGet, path, "", ""); code != http.StatusForbidden {
				t.Errorf("GET %s as a task granted acme/widgets alone: %d %s, want 403", path, code, body)
			}
		}
	}
	// The deployment's own memory scope is asked for with an empty repo;
	// a task granted one repository is narrowed to it instead.
	for _, path := range []string{"/api/memory?repo=", "/api/memory/documents?repo="} {
		code, body := asAgent(t, srv, "900", http.MethodGet, path, "", "")
		if code != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, code, body)
			continue
		}
		if strings.Contains(body, "DEPLOYMENT-WIDE") || !strings.Contains(body, "WIDGETS-ONLY") {
			t.Errorf("GET %s as a task granted acme/widgets alone answered the deployment's own scope: %s", path, body)
		}
	}
}

func withQuery(path, query string) string {
	if strings.Contains(path, "?") {
		return path + "&" + query
	}
	return path + "?" + query
}
