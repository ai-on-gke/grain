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

package repocreate_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/repocreate"
)

// testKeyPEM is one RSA key for every test here: generating 2048-bit
// keys per test is the slow part of the suite otherwise.
var testKeyPEM = func() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}()

// world is one GitHub (githubsim.Accounts) and a deployment whose git
// connection is a GitHub App credential named "app".
type world struct {
	gh      *githubsim.Accounts
	creds   *gitproxy.CredentialSet
	secrets map[string]string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	gh := githubsim.NewAccounts()
	creds, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetApp("app", "123", "", testKeyPEM); err != nil {
		t.Fatal(err)
	}
	creds.AppTransport = gh
	return &world{gh: gh, creds: creds, secrets: map[string]string{}}
}

func (w *world) env() repocreate.Env {
	return repocreate.Env{
		Credentials: w.creds, Credential: "app", API: w.gh,
		Secret: func(_ context.Context, name string) (string, bool) {
			v, ok := w.secrets[name]
			return v, ok
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func (w *world) create(t *testing.T, req repocreate.Request) repocreate.Outcome {
	t.Helper()
	c, ok := repocreate.For("github.com")
	if !ok {
		t.Fatal("no creator registered for github.com")
	}
	out, err := c.Create(context.Background(), w.env(), req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return out
}

func stepNamed(t *testing.T, out repocreate.Outcome, name string) repocreate.Step {
	t.Helper()
	for _, s := range out.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %q step in %+v", name, out.Steps)
	return repocreate.Step{}
}

func TestCreatesARepoInAnOrgWithTheAppInstallation(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["acme"] = "Organization"
	w.gh.Installations["acme"] = &githubsim.AppInstall{ID: 11, Selection: "selected",
		Permissions: map[string]string{"administration": "write", "contents": "write"}}
	w.gh.BranchAfter = 2

	out := w.create(t, repocreate.Request{Owner: "acme", Name: "widgets", Private: true, Init: true, Description: "Widgets"})

	if !out.Created || out.Existed || out.Repo != "acme/widgets" || out.DefaultBranch != "main" {
		t.Fatalf("outcome = %+v, want acme/widgets created on main", out)
	}
	r := w.gh.Repo("acme", "widgets")
	if r == nil || !r.Private || !r.HasBranch || r.Description != "Widgets" {
		t.Fatalf("repo on GitHub = %+v, want a private, initialised repo", r)
	}
	for _, name := range []string{"create", "default-branch", "access"} {
		if s := stepNamed(t, out, name); s.Status != repocreate.StepDone {
			t.Errorf("%s step = %+v, want done", name, s)
		}
	}
}

func TestAnOrgWhoseInstallationLacksAdministrationIsAHelpfulError(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["acme"] = "Organization"
	w.gh.Installations["acme"] = &githubsim.AppInstall{ID: 11, Selection: "all",
		Permissions: map[string]string{"contents": "write"}}

	out := w.create(t, repocreate.Request{Owner: "acme", Name: "widgets", Private: true, Init: true})

	if out.Connectable() {
		t.Fatalf("outcome = %+v, want nothing created", out)
	}
	s := stepNamed(t, out, "create")
	if s.Status != repocreate.StepFailed || !strings.Contains(s.Detail, "Administration: write") {
		t.Fatalf("create step = %+v, want it to name the missing permission", s)
	}
	if s.Link != "https://github.com/organizations/acme/settings/installations/11" {
		t.Errorf("link = %q, want the installation's settings page", s.Link)
	}
	if w.gh.Repo("acme", "widgets") != nil {
		t.Error("a repo was created anyway")
	}
}

func TestAnOrgWithoutTheAppInstalledLinksToTheInstallPage(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["acme"] = "Organization"

	out := w.create(t, repocreate.Request{Owner: "acme", Name: "widgets", Private: true, Init: true})

	s := stepNamed(t, out, "create")
	if s.Status != repocreate.StepFailed || !strings.Contains(s.Detail, "not installed") {
		t.Fatalf("create step = %+v, want it to say the App is not installed", s)
	}
	if s.Link != "https://github.com/apps/grain-test/installations/new" {
		t.Errorf("link = %q, want the App's install page", s.Link)
	}
}

func TestAPersonalAccountIsCreatedWithItsStoredUserToken(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["sklodowska-dev"] = "User"
	w.gh.UserTokens["ghu_marie"] = "sklodowska-dev"
	w.gh.Installations["sklodowska-dev"] = &githubsim.AppInstall{ID: 22, Selection: "selected",
		Permissions: map[string]string{"contents": "write"}}
	w.secrets[repocreate.UserTokenSecret("sklodowska-dev")] = "ghu_marie"

	out := w.create(t, repocreate.Request{Owner: "sklodowska-dev", Name: "radium", Private: true, Init: true})

	if !out.Created {
		t.Fatalf("outcome = %+v, want created", out)
	}
	if s := stepNamed(t, out, "create"); !strings.Contains(s.Detail, "github-user-token/sklodowska-dev") {
		t.Errorf("create step = %+v, want it to say which token made it", s)
	}
	// The installation is limited to selected repositories, so the user
	// token adds the new one to it.
	access := stepNamed(t, out, "access")
	r := w.gh.Repo("sklodowska-dev", "radium")
	inst := w.gh.Installations["sklodowska-dev"]
	if access.Status != repocreate.StepDone || len(inst.RepoIDs) != 1 || inst.RepoIDs[0] != r.ID {
		t.Fatalf("access step = %+v, installation repos = %v, want the repo added", access, inst.RepoIDs)
	}
}

func TestAPersonalAccountWithoutAUserTokenShowsTheGhCommand(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["sklodowska-dev"] = "User"
	w.gh.Installations["sklodowska-dev"] = &githubsim.AppInstall{ID: 22, Selection: "all",
		Permissions: map[string]string{"administration": "write"}}

	out := w.create(t, repocreate.Request{Owner: "sklodowska-dev", Name: "radium", Private: true, Init: true})

	if out.Connectable() {
		t.Fatalf("outcome = %+v, want nothing created", out)
	}
	s := stepNamed(t, out, "create")
	if s.Command != "gh repo create sklodowska-dev/radium --private --add-readme" {
		t.Errorf("command = %q", s.Command)
	}
	for _, want := range []string{"personal account", "installation token cannot", "no user token"} {
		if !strings.Contains(s.Detail, want) {
			t.Errorf("detail %q does not say %q", s.Detail, want)
		}
	}
	for _, want := range []string{"github-user-token/sklodowska-dev", "Add repo"} {
		if !strings.Contains(s.Fix, want) {
			t.Errorf("fix %q does not say %q", s.Fix, want)
		}
	}
}

func TestARepoThatAlreadyExistsIsConnectOnly(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["acme"] = "Organization"
	w.gh.Installations["acme"] = &githubsim.AppInstall{ID: 11, Selection: "all",
		Permissions: map[string]string{"administration": "write"}}
	w.gh.AddRepo("acme", "widgets", true)

	out := w.create(t, repocreate.Request{Owner: "acme", Name: "widgets", Private: true, Init: true})

	if out.Created || !out.Existed || !out.Connectable() {
		t.Fatalf("outcome = %+v, want connect-only", out)
	}
	if s := stepNamed(t, out, "create"); s.Status != repocreate.StepSkipped || !strings.Contains(s.Detail, "already exists") {
		t.Fatalf("create step = %+v", s)
	}
	for _, c := range w.gh.Calls {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/repos") {
			t.Errorf("created anyway: %s %s", c.Method, c.Path)
		}
	}
}

func TestATemplateRepoIsGeneratedFromIt(t *testing.T) {
	w := newWorld(t)
	w.gh.Kinds["acme"] = "Organization"
	w.gh.Installations["acme"] = &githubsim.AppInstall{ID: 11, Selection: "all",
		Permissions: map[string]string{"administration": "write"}}
	w.gh.AddRepo("acme", "template", false)

	out := w.create(t, repocreate.Request{Owner: "acme", Name: "widgets", Private: true, Template: "acme/template"})

	if r := w.gh.Repo("acme", "widgets"); !out.Created || r == nil || r.Template != "acme/template" {
		t.Fatalf("outcome = %+v, repo = %+v, want it generated from acme/template", out, r)
	}
}

func TestRegisterAddsAHostPattern(t *testing.T) {
	unregister := repocreate.Register("*.example.test", repocreate.GitHub{})
	t.Cleanup(unregister)
	if _, ok := repocreate.For("forge.example.test"); !ok {
		t.Fatal("no creator for forge.example.test")
	}
	got := repocreate.Hosts([]string{"forge.example.test", "elsewhere.test"})
	if strings.Join(got, ",") != "forge.example.test,github.com" {
		t.Fatalf("Hosts = %v", got)
	}
	unregister()
	if _, ok := repocreate.For("forge.example.test"); ok {
		t.Fatal("creator still registered after unregister")
	}
}
