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

// Adding and removing repositories and connections concurrently with
// the work that reads them, and across a restart (grain/task-4288). Run
// under -race by every Go job; the nightly go-long suite runs it again
// shuffled and twice over.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// openLifecycleRigIn is newLifecycleRig over a database, credential set
// and secrets store that live in dirs, so a second call over the same
// dirs is the same deployment after a restart. Each call opens the
// schema again (Store.Init), the step a daemon start and an upgrade both
// take. close stops this instance, as the daemon exiting does.
func openLifecycleRigIn(t *testing.T, dbDir, credDir, secretDir string) (r *lifecycleRig, close func()) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dbDir))
	if err != nil {
		t.Fatal(err)
	}
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	creds, err := gitproxy.LoadCredentialSet(credDir)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		Credentials:   creds,
		Secrets:       secrets.New(secretDir),
	}, store)
	if cfg, err := store.GetConfig(ctx); err != nil || cfg == nil {
		if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	r = &lifecycleRig{t: t, srv: ui.NewServerWithClient(client), client: client, store: store, creds: creds, ctx: ctx}
	var once sync.Once
	close = func() { once.Do(func() { db.Close() }) }
	t.Cleanup(close)
	return r, close
}

// serve is lifecycleRig.call for a goroutine: it reports rather than
// failing the test, which only the test's own goroutine may do.
func (r *lifecycleRig) serve(method, path, body string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Grain-User", "alice")
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestAddingAndRemovingRepositoriesWhileTasksAreFiledAgainstThem(t *testing.T) {
	ui.LogToTest(t)
	r, _ := openLifecycleRigIn(t, t.TempDir(), t.TempDir(), t.TempDir())
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/gadgets"}`)
	cred := r.addConnection("github.com", "acme", "ghp_race")

	const rounds = 20
	var wg sync.WaitGroup
	errs := make(chan string, 8*rounds)
	report := func(format string, args ...any) { errs <- fmt.Sprintf(format, args...) }
	okOr400 := func(what string, code int, out string) {
		if code != http.StatusOK && code != http.StatusBadRequest {
			report("%s: %d %s", what, code, out)
		}
	}
	var filed sync.Map
	wg.Add(4)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			code, out := r.serve(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
			okOr400("add", code, out)
			code, out = r.serve(http.MethodPut, "/api/repos/acme/widgets/setup-command", fmt.Sprintf(`{"setupCommand":"make %d"}`, i))
			okOr400("setup command", code, out)
			code, out = r.serve(http.MethodDelete, "/api/repos/acme/widgets", "")
			okOr400("remove", code, out)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			task, err := r.client.CreateTask(r.ctx, ui.CreateTaskRequest{Title: fmt.Sprintf("race %d", i), Repo: "acme/widgets"})
			if err != nil {
				report("filing task %d: %v", i, err)
				continue
			}
			filed.Store(task.ID, true)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			code, out := r.serve(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)
			okOr400("pin", code, out)
			code, out = r.serve(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":""}`)
			okOr400("unpin", code, out)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			for _, path := range []string{"/api/deployment/git-connections", "/api/repos/acme/widgets/settings"} {
				code, out := r.serve(http.MethodGet, path, "")
				okOr400("GET "+path, code, out)
			}
			code, out := r.serve(http.MethodPost, "/api/repos/plan", `{"targetRepos":["acme/gadgets"]}`)
			okOr400("plan", code, out)
		}
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	// Settled: the last remove won, so acme/widgets is gone with all of its
	// configuration, the neighbour and the connection are intact, and
	// every task filed in the race is still there.
	if got := r.targets(); !slices.Equal(got, []string{"acme/gadgets"}) {
		t.Fatalf("targets after the race = %v", got)
	}
	if rc, err := r.store.GetRepoConfig(r.ctx, model.RepoRef{Owner: "acme", Name: "widgets"}); err != nil || rc != nil {
		t.Fatalf("repo_config after the race = %+v, %v; want none", rc, err)
	}
	if views := r.connections(); !slices.ContainsFunc(views, func(v ui.TeamGitConnectionView) bool { return v.Credential == cred && v.Scope == "acme" }) {
		t.Fatalf("the owner connection is gone after the race: %+v", views)
	}
	if got := r.pins(); len(got) > 1 {
		t.Fatalf("pins after the race = %v, want at most one", got)
	}
	n := 0
	filed.Range(func(id, _ any) bool {
		n++
		if _, err := r.client.GetTask(r.ctx, id.(string)); err != nil {
			t.Errorf("task %s filed during the race is gone: %v", id, err)
		}
		return true
	})
	if n != rounds {
		t.Fatalf("filed %d tasks, want %d", n, rounds)
	}
}

// Adds and removes made before a restart are what the restarted daemon
// sees: nothing removed comes back, nothing added is lost, and the schema
// pass a start (or an upgrade) makes over the store reverts no
// repo_config change (grain/task-2731's failure, here across a restart
// rather than an import).
func TestRepositoryAndConnectionChangesSurviveARestart(t *testing.T) {
	ui.LogToTest(t)
	dbDir, credDir, secretDir := t.TempDir(), t.TempDir(), t.TempDir()
	r, stop := openLifecycleRigIn(t, dbDir, credDir, secretDir)
	cred := r.addConnection("github.com", "acme", "ghp_restart")
	gone := r.addConnection("ghe.example.com", "", "ghp_gone")
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/widgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"acme/gadgets"}`)
	r.must(http.MethodPost, "/api/repos", `{"repo":"ghe.example.com/acme/widgets"}`)
	r.must(http.MethodPut, "/api/repos/acme/widgets/setup-command", `{"setupCommand":"make deps"}`)
	r.must(http.MethodPut, "/api/repos/acme/gadgets/setup-command", `{"setupCommand":"make old"}`)
	r.must(http.MethodPut, "/api/deployment/repos/acme/widgets/git-connection", `{"credential":"`+cred+`"}`)
	r.must(http.MethodDelete, "/api/repos/ghe.example.com%2Facme/widgets", "")
	r.must(http.MethodDelete, "/api/deployment/git-connections/"+gone, "")
	// The edit an upgrade once reverted: a repo_config change made after
	// the row was first written.
	r.must(http.MethodPut, "/api/repos/acme/gadgets/setup-command", `{"setupCommand":"make new"}`)
	stop()

	for restart := 1; restart <= 2; restart++ {
		r, stop = openLifecycleRigIn(t, dbDir, credDir, secretDir)
		if got := r.targets(); !slices.Equal(got, []string{"acme/widgets", "acme/gadgets"}) {
			t.Fatalf("restart %d: targets = %v", restart, got)
		}
		for repo, want := range map[string]string{"widgets": "make deps", "gadgets": "make new"} {
			rc, err := r.store.GetRepoConfig(r.ctx, model.RepoRef{Owner: "acme", Name: repo})
			if err != nil || rc == nil || rc.SetupCommand != want {
				t.Fatalf("restart %d: acme/%s repo_config = %+v, %v; want setup %q", restart, repo, rc, err, want)
			}
		}
		if got := r.pins(); !slices.Equal(got, []string{" acme/widgets=" + cred}) {
			t.Fatalf("restart %d: pins = %v", restart, got)
		}
		for _, v := range r.connections() {
			if v.Credential == gone {
				t.Fatalf("restart %d: the removed connection %s came back: %+v", restart, gone, v)
			}
		}
		if r.credentialExists(gone) {
			t.Fatalf("restart %d: the removed connection's credential came back", restart)
		}
		if !r.credentialExists(cred) {
			t.Fatalf("restart %d: the kept connection's credential was lost", restart)
		}
		stop()
	}
}
