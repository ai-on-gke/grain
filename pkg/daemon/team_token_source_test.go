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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A repository cloned with an SSH key has its REST calls made with the
// token paired to that key, and with no pairing it is git-only: the
// pull request is refused with a sentence saying so, rather than sent
// unauthenticated.
func TestCredentialTokenSourceUsesTheSSHKeysPairedAPICredential(t *testing.T) {
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(block))
	if err := credentials.SetToken("api", "api-token"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetSSHKey("paired", gitproxy.SSHKeyFile{Host: "github.com", PrivateKey: key, APICredential: "api"}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetSSHKey("alone", gitproxy.SSHKeyFile{Host: "github.com", PrivateKey: key}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{"acme/paired": "paired", "acme/alone": "alone"}
	src := credentialTokenSource{credentials: credentials, teamCredential: func(host, owner, repo string) (string, bool) {
		name, ok := owners[owner+"/"+repo]
		return name, ok
	}}
	if tok := src.TokenFor("acme", "paired"); tok == nil || *tok != "api-token" {
		t.Errorf("TokenFor(paired SSH key) = %v, want the paired API token", tok)
	}
	if reason := src.GitOnly("acme", "paired"); reason != "" {
		t.Errorf("GitOnly(paired) = %q", reason)
	}
	if tok := src.TokenFor("acme", "alone"); tok != nil {
		t.Errorf("TokenFor(unpaired SSH key) = %q, want no token", *tok)
	}
	client := &github.RESTClient{Transport: &github.FakeTransport{}, Tokens: src}
	_, err = client.CreatePullRequest("acme", "alone", "grain/task-1", "main", "t", "b")
	if err == nil || !strings.Contains(err.Error(), "covers git only") {
		t.Errorf("opening a pull request on a git-only repository: %v", err)
	}
}

func TestCredentialTokenSourceForTaskSelectsUserPATAndAuthorSignature(t *testing.T) {
	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("default-cred", "tok-default"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("alice-cred", "tok-alice"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetHost("alice-cred", gitproxy.CredentialHost{Host: "github.com", Kind: model.GitHostGitHub, Author: "Alice Developer"}); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("bob-cred", "tok-bob"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetHost("bob-cred", gitproxy.CredentialHost{Host: "github.com", Kind: model.GitHostGitHub, Author: "Bob Engineer"}); err != nil {
		t.Fatal(err)
	}

	taskCreds := map[string]string{
		"42": "alice-cred",
		"99": "bob-cred",
	}
	src := credentialTokenSource{
		credentials: credentials,
		teamCredential: func(host, owner, repo string) (string, bool) {
			return "default-cred", true
		},
		taskCredential: func(taskID, host, owner, repo string) (string, bool) {
			c, ok := taskCreds[taskID]
			return c, ok
		},
	}

	if tok := src.TokenFor("acme", "widgets"); tok == nil || *tok != "tok-default" {
		t.Fatalf("unscoped TokenFor = %v, want tok-default", tok)
	}
	aliceSrc := src.ForTask("42")
	if tok := aliceSrc.TokenFor("acme", "widgets"); tok == nil || *tok != "tok-alice" {
		t.Fatalf("ForTask(42).TokenFor = %v, want tok-alice", tok)
	}
	if sig, ok := aliceSrc.(github.AuthorSignatureSource); !ok || sig.AuthorSignature("acme", "widgets") != "Alice Developer" {
		t.Fatalf("ForTask(42).AuthorSignature = %q, want Alice Developer", sig.AuthorSignature("acme", "widgets"))
	}

	bobSrc := src.ForTask("99")
	if tok := bobSrc.TokenFor("acme", "widgets"); tok == nil || *tok != "tok-bob" {
		t.Fatalf("ForTask(99).TokenFor = %v, want tok-bob", tok)
	}
	if sig, ok := bobSrc.(github.AuthorSignatureSource); !ok || sig.AuthorSignature("acme", "widgets") != "Bob Engineer" {
		t.Fatalf("ForTask(99).AuthorSignature = %q, want Bob Engineer", sig.AuthorSignature("acme", "widgets"))
	}
}

func TestStateRepoPullRequestClientAndReplyForTaskUseTaskPATInsteadOfFirstOnList(t *testing.T) {
	ctx := context.Background()
	store, db, err := OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "first-cred", User: "alice@example.com"},
			{Credential: "second-cred", User: "bob@example.com"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	taskBob := model.Task{
		ID:      "t-bob",
		Branch:  "grain/t-bob",
		Intent:  model.IntentImplement,
		Title:   "Bob PR",
		Owner:   "bob@example.com",
		Creator: "bob@example.com",
		Target:  &repo,
		Binding: model.BindingDirective,
		Links:   []model.Link{{Kind: model.LinkFixes, Target: "acme/widgets#42"}},
	}
	if err := store.PutTask(ctx, taskBob); err != nil {
		t.Fatal(err)
	}

	credentials, err := gitproxy.LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("first-cred", "tok-first"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetHost("first-cred", gitproxy.CredentialHost{Host: "github.com", Kind: model.GitHostGitHub, Author: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetPAT("second-cred", "tok-second"); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetHost("second-cred", gitproxy.CredentialHost{Host: "github.com", Kind: model.GitHostGitHub, Author: "bob"}); err != nil {
		t.Fatal(err)
	}

	src := credentialTokenSource{
		credentials:    credentials,
		teamCredential: repoTeamCredential(store),
		taskCredential: taskGitCredential(store),
	}

	fake := github.NewFakeTransport(
		github.ApiResponse{
			Status: 200,
			Body:   []byte(`[{"number":42,"state":"open","head":{"ref":"grain/t-bob","sha":"abc"},"base":{"ref":"main"}}]`),
		},
		github.ApiResponse{
			Status: 200,
			Body:   []byte(`[{"id":701,"body":"Please fix this","path":"main.go","line":10,"user":{"login":"reviewer"}}]`),
		},
		github.ApiResponse{
			Status: 201,
			Body:   []byte(`{"id":702}`),
		},
	)

	var wrapped github.Client = &StateRepoPullRequestClient{
		Client: &github.RESTClient{Transport: fake, Tokens: src},
		Store:  store,
	}
	wrapped = github.WithCaller(wrapped, "sync")

	opener := &PullRequestOpener{Store: store, Client: wrapped}
	text, refused, err := opener.ReplyForTask(ctx, "t-bob", 701, "Addressed in latest commit.")
	if err != nil || refused {
		t.Fatalf("ReplyForTask = %q, refused=%v, err=%v", text, refused, err)
	}

	var replyCall *github.FakeCall
	for i := range fake.Calls {
		if fake.Calls[i].Method == "POST" && strings.HasSuffix(fake.Calls[i].Path, "/comments/701/replies") {
			replyCall = &fake.Calls[i]
			break
		}
	}
	if replyCall == nil {
		t.Fatalf("expected POST to /comments/701/replies, calls=%+v", fake.Calls)
	}
	if gotAuth := replyCall.Headers["Authorization"]; gotAuth != "token tok-second" {
		t.Errorf("reply Authorization = %q, want %q (used wrong PAT)", gotAuth, "token tok-second")
	}
	if !strings.Contains(string(replyCall.Body), "*Signed by bob (via Grain)*") {
		t.Errorf("reply body = %s, want bob attribution", string(replyCall.Body))
	}
}


