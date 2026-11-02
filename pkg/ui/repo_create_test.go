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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repocreate"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var repoCreateKeyPEM = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}()

// repoCreateServer is a server whose deployment's git connection is a
// GitHub App ("app") on a simulated GitHub, targeting acme/existing.
func repoCreateServer(t *testing.T) (*ui.Server, *ui.Client, *githubsim.Accounts, *secrets.Store) {
	t.Helper()
	srv, client, creds := testServerWithCreds(t)
	gh := githubsim.NewAccounts()
	if err := creds.SetApp("app", "123", "", repoCreateKeyPEM); err != nil {
		t.Fatal(err)
	}
	creds.AppTransport = gh
	client.Config.GitReach = &gitproxy.Reach{Credentials: creds, API: gh, Host: "github.com"}
	store := secrets.New(t.TempDir())
	client.Config.Secrets = store
	ctx := context.Background()
	if err := client.Store.UpdateConfig(ctx, func(cfg *model.Config) error {
		cfg.GitCredential = "app"
		cfg.TargetRepos = []string{"acme/existing"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	gh.Kinds["acme"] = "Organization"
	gh.Installations["acme"] = &githubsim.AppInstall{ID: 11, Selection: "all",
		Permissions: map[string]string{"administration": "write", "contents": "write"}}
	return srv, client, gh, store
}

func createRepo(t *testing.T, srv *ui.Server, body string) ui.CreateRepoResult {
	t.Helper()
	rec := do(t, srv, http.MethodPost, "/api/repos/create", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/repos/create: %d %s", rec.Code, rec.Body.String())
	}
	var res ui.CreateRepoResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func targetRepos(t *testing.T, client *ui.Client) []string {
	t.Helper()
	cfg, err := client.Store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cfg.TargetRepos
}

func TestCreateRepoConnectsItAndWritesItsRepoConfig(t *testing.T) {
	srv, client, gh, _ := repoCreateServer(t)

	res := createRepo(t, srv, `{"owner":"acme","name":"widgets","visibility":"private","description":"Widgets",
		"ci_mode":"github","default_capabilities":[],"reads":["acme/existing"]}`)

	if !res.Created || !res.Connected || !res.Complete || res.Repo != "acme/widgets" {
		t.Fatalf("result = %+v, want acme/widgets created and connected", res)
	}
	if r := gh.Repo("acme", "widgets"); r == nil || !r.Private || !r.HasBranch {
		t.Fatalf("GitHub repo = %+v, want private and initialised", r)
	}
	if got := targetRepos(t, client); !slices.Contains(got, "acme/widgets") || !slices.Contains(got, "acme/existing") {
		t.Fatalf("targetRepos = %v, want acme/widgets added beside acme/existing", got)
	}
	rc, err := client.Store.GetRepoConfig(context.Background(), model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || rc == nil {
		t.Fatalf("repo_config row: %v %v", rc, err)
	}
	if rc.CIMode != model.CIModeGitHub || !slices.Contains(rc.CICapabilities, model.RepoReadCapability(model.RepoRef{Owner: "acme", Name: "existing"})) {
		t.Fatalf("repo_config = %+v, want ci mode github and a CI read of acme/existing", rc)
	}
	var names []string
	for _, s := range res.Steps {
		names = append(names, s.Name+":"+s.Status)
	}
	want := []string{"create:done", "default-branch:done", "access:done", "connect:done", "repo-config:done"}
	if !slices.Equal(names, want) {
		t.Fatalf("steps = %v, want %v", names, want)
	}
}

func TestCreateRepoThatAlreadyExistsIsConnectOnly(t *testing.T) {
	srv, client, gh, _ := repoCreateServer(t)
	gh.AddRepo("acme", "widgets", true)

	res := createRepo(t, srv, `{"owner":"acme","name":"widgets"}`)

	if res.Created || !res.Existed || !res.Connected {
		t.Fatalf("result = %+v, want connect-only", res)
	}
	if got := targetRepos(t, client); !slices.Contains(got, "acme/widgets") {
		t.Fatalf("targetRepos = %v, want acme/widgets", got)
	}
}

func TestCreateRepoInAnOrgWithoutPermissionConnectsNothing(t *testing.T) {
	srv, client, gh, _ := repoCreateServer(t)
	gh.Installations["acme"].Permissions = map[string]string{"contents": "write"}

	res := createRepo(t, srv, `{"owner":"acme","name":"widgets"}`)

	if res.Created || res.Connected || res.Complete {
		t.Fatalf("result = %+v, want nothing done", res)
	}
	if len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Detail, "Administration: write") || res.Steps[0].Link == "" {
		t.Fatalf("steps = %+v, want one failed create naming the permission and linking its settings", res.Steps)
	}
	if got := targetRepos(t, client); slices.Contains(got, "acme/widgets") {
		t.Fatalf("targetRepos = %v, want acme/widgets left off", got)
	}
}

func TestCreateRepoForAPersonalAccountUsesItsStoredUserToken(t *testing.T) {
	srv, _, gh, store := repoCreateServer(t)
	gh.Kinds["sklodowska-dev"] = "User"
	gh.UserTokens["ghu_marie"] = "sklodowska-dev"
	gh.Installations["sklodowska-dev"] = &githubsim.AppInstall{ID: 22, Selection: "selected"}

	res := createRepo(t, srv, `{"owner":"sklodowska-dev","name":"radium"}`)
	if res.Created || len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Command, "gh repo create sklodowska-dev/radium --private --add-readme") {
		t.Fatalf("without a user token: %+v, want the gh command", res)
	}

	secret, key, _ := strings.Cut(repocreate.UserTokenSecret("sklodowska-dev"), "/")
	if err := store.Set(secret, key, []byte("ghu_marie")); err != nil {
		t.Fatal(err)
	}
	res = createRepo(t, srv, `{"owner":"sklodowska-dev","name":"radium"}`)
	if !res.Created || !res.Complete {
		t.Fatalf("with a user token: %+v, want created and complete", res)
	}
	if ids := gh.Installations["sklodowska-dev"].RepoIDs; len(ids) != 1 {
		t.Fatalf("installation repos = %v, want the new repo added", ids)
	}
}

func TestCreateRepoRefusesAHostWithNoCreator(t *testing.T) {
	srv, _, _, _ := repoCreateServer(t)
	rec := do(t, srv, http.MethodPost, "/api/repos/create", `{"host":"git.example.test","owner":"acme","name":"widgets"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "github.com") {
		t.Fatalf("POST to an unregistered host: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, srv, http.MethodGet, "/api/repo-hosts", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"github.com"`) {
		t.Fatalf("GET /api/repo-hosts: %d %s", rec.Code, rec.Body.String())
	}
}

// An agent creates a repository only on a person's grant of the
// dangerous repos:create scope, and then only with a reason.
func TestCreateRepoNeedsTheDangerousScopeAndAReason(t *testing.T) {
	srv, client, gh, _ := repoCreateServer(t)
	if !model.IsDangerousAPIScope("repos:create") {
		t.Fatal("repos:create is not a dangerous scope")
	}
	seedAPITask(t, client, "1", "acme/existing", "api:repos:write")
	seedAPITask(t, client, "2", "acme/existing", "api:repos:create")
	body := `{"owner":"acme","name":"widgets"}`

	if code, resp := asAgent(t, srv, "1", http.MethodPost, "/api/repos/create", body, "because"); code != http.StatusForbidden ||
		!strings.Contains(resp, "repos:create") {
		t.Errorf("repos:write created a repo: %d %s", code, resp)
	}
	if code, resp := asAgent(t, srv, "2", http.MethodPost, "/api/repos/create", body, ""); code != http.StatusForbidden ||
		!strings.Contains(resp, "reason") {
		t.Errorf("repos:create without a reason was not refused for it: %d %s", code, resp)
	}
	if gh.Repo("acme", "widgets") != nil {
		t.Fatal("a refused call created the repo")
	}
	if code, resp := asAgent(t, srv, "2", http.MethodPost, "/api/repos/create", body, "the operator asked for it in task 2"); code != http.StatusOK {
		t.Errorf("repos:create with a reason was refused: %d %s", code, resp)
	}
	if gh.Repo("acme", "widgets") == nil {
		t.Fatal("the granted call did not create the repo")
	}
}
