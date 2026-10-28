package daemon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/model"
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
