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
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestExternalAgentSkillMetadataEndpointsEnforceRepoAndSettingsBoundaries(t *testing.T) {
	origLog := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(origLog) })

	srv, client, store := releaseServer(t, nil)
	ctx := context.Background()

	cfg := model.DefaultConfig()
	cfg.EnvironmentName = "Acme Prod"
	cfg.PromptExtension = "SECRET_GLOBAL_PROMPT_EXTENSION"
	cfg.TargetRepos = []string{"acme/widgets", "acme/secret"}
	cfg.ExternalAgentAPI = true
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	client.Config.DefaultTarget = &model.RepoRef{Owner: "acme", Name: "secret"}
	client.Config.StateRepoRef = func() model.RepoRef { return model.RepoRef{Owner: "acme", Name: "config"} }

	secretRepo := model.RepoRef{Owner: "acme", Name: "secret"}
	widgetsRepo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:             secretRepo,
		PromptExtension:  "secret-repo-prompt",
		SetupCommand:     "make secret-setup",
		DefaultAdmission: model.AdmissionDurable,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoWorkflows(ctx, secretRepo, []model.RepoWorkflow{{
		Repo: secretRepo,
		Path: ".grain/workflows/ci.yml",
		Text: "name: ci\n",
	}}); err != nil {
		t.Fatal(err)
	}

	declaredUnboundID := model.RepoDeclaredID(model.DeclaredTemplate, secretRepo, "secret-unbound")
	if _, err := store.SyncRepoDeclarations(ctx, secretRepo, "deadbeef", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path: model.TemplateDeclarationDir + "/secret-unbound.yml",
			Template: model.Template{
				ID:    declaredUnboundID,
				Name:  "Secret Declared Unbound",
				Title: "Declared in secret repo without target",
				Body:  "TOP_SECRET_DECLARED_BODY",
			},
		}},
	}, time.Now(), time.UTC); err != nil {
		t.Fatal(err)
	}

	// Templates across allowed, out-of-scope target, out-of-scope DeclaredIn,
	// out-of-scope Reads/Writes, and unbound deployment-wide scope.
	for _, tmpl := range []model.Template{
		{
			ID:     "widgets-tmpl",
			Name:   "Widgets Template",
			Target: &widgetsRepo,
			Title:  "Widgets work",
			Body:   "Allowed repo template",
		},
		{
			ID:     "secret-target-tmpl",
			Name:   "Secret Target",
			Target: &secretRepo,
			Title:  "Secret work",
			Body:   "TOP_SECRET_TARGET_BODY",
		},
		{
			ID:     "secret-reads-tmpl",
			Name:   "Cross-repo Reads",
			Target: &widgetsRepo,
			Reads:  []model.RepoRef{secretRepo},
			Title:  "Reads secret repo",
			Body:   "TOP_SECRET_READS_BODY",
		},
		{
			ID:     "secret-writes-tmpl",
			Name:   "Cross-repo Writes",
			Target: &widgetsRepo,
			Writes: []model.WriteTarget{{Repo: secretRepo, Base: "main"}},
			Title:  "Writes secret repo",
			Body:   "TOP_SECRET_WRITES_BODY",
		},
		{
			ID:    "global-unbound-tmpl",
			Name:  "Global Unbound",
			Title: "Unbound template",
			Body:  "General template",
		},
	} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}

	humanCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"})

	// Token 1: ScopeSet grain/external-dev narrowed via tok.Repos = ["acme/widgets"].
	tokByRepos, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:     "by-repos",
		ScopeSet: "grain/external-dev",
		Repos:    []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Token 2: tok.Repos empty, narrowed via per-scope @repo grants!
	tokByScopeQualifier, err := client.CreateExternalAgentToken(humanCtx, ui.CreateExternalAgentTokenRequest{
		Name:   "by-scope-qualifier",
		Scopes: []string{"deployment:read@acme/widgets", "templates:read@acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"tok-repos-list", tokByRepos.Token},
		{"per-scope-repo-qualifier", tokByScopeQualifier.Token},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 1. GET /api/templates must exclude every template tied to acme/secret.
			tmplReq := httptest.NewRequest(http.MethodGet, "/api/templates", nil)
			tmplReq.Header.Set("Authorization", "Bearer "+tc.token)
			tmplRec := httptest.NewRecorder()
			srv.ServeHTTP(tmplRec, tmplReq)
			if tmplRec.Code != http.StatusOK {
				t.Fatalf("GET /api/templates status = %d: %s", tmplRec.Code, tmplRec.Body.String())
			}
			var gotTemplates []ui.Template
			if err := json.Unmarshal(tmplRec.Body.Bytes(), &gotTemplates); err != nil {
				t.Fatalf("unmarshal templates: %v", err)
			}
			var ids []string
			for _, tmpl := range gotTemplates {
				ids = append(ids, tmpl.ID)
			}
			for _, forbiddenID := range []string{
				"secret-target-tmpl",
				declaredUnboundID,
				"secret-reads-tmpl",
				"secret-writes-tmpl",
			} {
				if slices.Contains(ids, forbiddenID) {
					t.Errorf("GET /api/templates leaked unauthorized template %q in %v", forbiddenID, ids)
				}
			}
			if strings.Contains(tmplRec.Body.String(), "TOP_SECRET_") {
				t.Errorf("GET /api/templates response leaked secret template body: %s", tmplRec.Body.String())
			}
			if !slices.Contains(ids, "widgets-tmpl") || !slices.Contains(ids, "global-unbound-tmpl") {
				t.Errorf("GET /api/templates ids = %v, want widgets-tmpl and global-unbound-tmpl", ids)
			}

			// 2. GET /api/config must redact settings:read fields (Deployment, PromptExtension)
			// and filter all repository lists/maps to acme/widgets only.
			cfgReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			cfgReq.Header.Set("Authorization", "Bearer "+tc.token)
			cfgRec := httptest.NewRecorder()
			srv.ServeHTTP(cfgRec, cfgReq)
			if cfgRec.Code != http.StatusOK {
				t.Fatalf("GET /api/config status = %d: %s", cfgRec.Code, cfgRec.Body.String())
			}
			rawBody := cfgRec.Body.String()
			for _, leaked := range []string{
				"acme/secret",
				"acme/config",
				"SECRET_GLOBAL_PROMPT_EXTENSION",
			} {
				if strings.Contains(rawBody, leaked) {
					t.Errorf("GET /api/config leaked %q to repo-scoped external token: %s", leaked, rawBody)
				}
			}
			var parsed map[string]any
			if err := json.Unmarshal(cfgRec.Body.Bytes(), &parsed); err != nil {
				t.Fatalf("unmarshal /api/config: %v", err)
			}
			if parsed["deployment"] != nil {
				t.Errorf("GET /api/config exposed deployment object without settings:read: %v", parsed["deployment"])
			}
			targetRepos, _ := parsed["targetRepos"].([]any)
			if len(targetRepos) != 1 || targetRepos[0] != "acme/widgets" {
				t.Errorf("GET /api/config targetRepos = %v, want [acme/widgets]", targetRepos)
			}
		})
	}
}
