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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// networkWideningFixture is a UI server over a real store whose
// deployment-wide networkAccess is deployment, holding an acting task
// ("901", api:tasks:write on acme/widgets, no networkAccess of its own) and
// an approved, queued sibling ("902") it may edit -- numbered past what the
// store hands a filing, so a filing the gate wrongly let through cannot
// land on either.
func networkWideningFixture(t *testing.T, deployment model.NetworkAccess) (*ui.Server, *model.Store) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConfig(ctx, model.Config{
		PollInterval:     model.DefaultPollInterval,
		NetworkAccess:    deployment,
		TargetRepos:      []string{"acme/widgets"},
		ExternalAgentAPI: true,
	}); err != nil {
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	person := model.Principal{Kind: model.PrincipalHuman, ID: "operator"}
	for _, task := range []model.Task{
		{ID: "901", Intent: model.IntentImplement, Title: "acting", Target: &repo,
			Grants: []model.Grant{{Capability: "api:tasks:write@acme/widgets", Via: model.GrantByLabel}}},
		{ID: "902", Intent: model.IntentImplement, Title: "sibling", Target: &repo,
			Approval: &model.Attribution{Actor: person}},
	} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	return ui.NewServer(ui.Config{Actor: ui.DefaultActor("operator"), DefaultTarget: &repo, LocalAPIToken: testAccessCode}, store), store
}

func serveNetworkWidening(t *testing.T, srv *ui.Server, method, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// The access code, as the operator's browser and the git proxy's
	// in-process API present it; an external agent's own bearer replaces it.
	req.Header.Set("Authorization", "Bearer "+testAccessCode)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func siblingNetworkAccess(t *testing.T, store *model.Store) model.NetworkAccess {
	t.Helper()
	task, err := store.GetTask(context.Background(), "902")
	if err != nil || task == nil {
		t.Fatalf("reading task 902: %v", err)
	}
	return task.NetworkAccess
}

// An agent whose task takes the deployment's networkAccess of "none" has
// no network of its own, so it may not hand "open" to work it edits or
// files -- through its run's API tools or through an external agent
// credential. The gate used to compare only the acting task's own field,
// so a task with no override (the usual case) on a deployment whose
// default is none could set a queued sibling to open, and an external
// agent was never checked at all.
func TestAgentCannotWidenNetworkAccessPastAnInheritedNone(t *testing.T) {
	agent := map[string]string{ui.ActingTaskHeader: "901"}

	t.Run("in-grain agent", func(t *testing.T) {
		srv, store := networkWideningFixture(t, model.NetworkNone)
		for _, body := range []string{`{"networkAccess":"open"}`, `{"NetworkAccess":"open"}`} {
			if code, resp := serveNetworkWidening(t, srv, http.MethodPatch, "/api/tasks/902", body, agent); code != http.StatusForbidden {
				t.Errorf("PATCH %s: %d %s, want 403", body, code, resp)
			}
		}
		if got := siblingNetworkAccess(t, store); got != "" {
			t.Errorf("sibling networkAccess = %q after refused edits, want unchanged", got)
		}
		if code, resp := serveNetworkWidening(t, srv, http.MethodPost, "/api/tasks",
			`{"title":"wide","repo":"acme/widgets","networkAccess":"open"}`, agent); code != http.StatusForbidden {
			t.Errorf("filing with networkAccess open: %d %s, want 403", code, resp)
		}
		// Narrowing, and asking for the default (none), stay allowed.
		for _, body := range []string{`{"networkAccess":"none"}`, `{"networkAccess":""}`} {
			if code, resp := serveNetworkWidening(t, srv, http.MethodPatch, "/api/tasks/902", body, agent); code != http.StatusOK {
				t.Errorf("PATCH %s: %d %s, want 200", body, code, resp)
			}
		}
	})

	t.Run("in-grain agent on an open deployment", func(t *testing.T) {
		srv, store := networkWideningFixture(t, model.NetworkOpen)
		if code, resp := serveNetworkWidening(t, srv, http.MethodPatch, "/api/tasks/902", `{"networkAccess":"open"}`, agent); code != http.StatusOK {
			t.Fatalf("an agent with the deployment's open network was refused open: %d %s", code, resp)
		}
		if got := siblingNetworkAccess(t, store); got != model.NetworkOpen {
			t.Errorf("sibling networkAccess = %q, want open", got)
		}
	})

	for _, deployment := range []model.NetworkAccess{model.NetworkNone, model.NetworkOpen} {
		t.Run("external agent on "+string(deployment), func(t *testing.T) {
			srv, store := networkWideningFixture(t, deployment)
			code, resp := serveNetworkWidening(t, srv, http.MethodPost, "/api/external-agents/tokens",
				`{"name":"writer","scopes":["tasks:write"],"repos":["acme/widgets"]}`, nil)
			if code != http.StatusOK && code != http.StatusCreated {
				t.Fatalf("issuing an external credential: %d %s", code, resp)
			}
			var issued struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal([]byte(resp), &issued); err != nil || !strings.HasPrefix(issued.Token, "gma_") {
				t.Fatalf("issued credential %s: %v", resp, err)
			}
			bearer := map[string]string{"Authorization": "Bearer " + issued.Token}
			code, resp = serveNetworkWidening(t, srv, http.MethodPatch, "/api/tasks/902", `{"networkAccess":"open"}`, bearer)
			want := http.StatusForbidden
			if deployment == model.NetworkOpen {
				want = http.StatusOK
			}
			if code != want {
				t.Fatalf("external PATCH networkAccess open on a %s deployment: %d %s, want %d", deployment, code, resp, want)
			}
			if deployment == model.NetworkNone {
				if got := siblingNetworkAccess(t, store); got != "" {
					t.Errorf("sibling networkAccess = %q after a refused external edit, want unchanged", got)
				}
				if code, resp := serveNetworkWidening(t, srv, http.MethodPatch, "/api/tasks/902", `{"networkAccess":"none"}`, bearer); code != http.StatusOK {
					t.Errorf("external PATCH networkAccess none: %d %s, want 200", code, resp)
				}
			}
		})
	}
}
