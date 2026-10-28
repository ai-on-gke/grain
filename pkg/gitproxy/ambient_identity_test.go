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

package gitproxy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// The commit identity that goes with an ambient credential: the host
// account's own git identity first, then the login GitHub answers /user
// with, and never `grain agent <grain-agent@localhost>` while the host
// has either to offer.

func TestAmbientGitIdentityIsTheHostAccountsOwn(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	writeState(t, state, "git-user-name", "Olive Operator")
	writeState(t, state, "git-user-email", "olive@example.com")
	c, now := ambientSet(t)
	fake := github.NewFakeTransport()
	c.AppTransport = fake
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	id, err := c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive Operator" || id.Email != "olive@example.com" {
		t.Fatalf("identity = %+v, want the host's own git config", id)
	}
	// A host whose git config says who the person is needs no GitHub
	// lookup at all: neither gh nor /user was asked.
	if n := calls(t, state, "gh-calls"); n != 0 || len(fake.Calls) != 0 {
		t.Fatalf("gh ran %d times and /user was asked %d times for an identity git config answered", n, len(fake.Calls))
	}
	if got := id.String(); got != "Olive Operator <olive@example.com>" {
		t.Fatalf("String() = %q", got)
	}

	// Cached: a second ask within the TTL does not run git again, and
	// one past it does.
	before := calls(t, state, "git-calls")
	if _, err := c.AmbientGitIdentity("op"); err != nil {
		t.Fatal(err)
	}
	if n := calls(t, state, "git-calls"); n != before {
		t.Fatalf("git ran again within the TTL (%d -> %d calls)", before, n)
	}
	writeState(t, state, "git-user-name", "Olive O. Operator")
	*now = now.Add(hostGitIdentityTTL + time.Second)
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "Olive O. Operator" {
		t.Fatalf("after the TTL: %+v, %v; want the host's new name", id, err)
	}
}

func TestAmbientGitIdentityFallsBackToTheLoginsNoreplyAddress(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "gh-token", "gho_who")
	// The host names the person but gives no address: the address is
	// the login's noreply one, which GitHub attributes to the account.
	writeState(t, state, "git-user-name", "Olive Operator")
	c, _ := ambientSet(t)
	fake := github.NewFakeTransport(
		github.ApiResponse{Status: 200, Body: []byte(`{"login":"operator","id":12345,"name":"Olive from GitHub"}`), Headers: map[string]string{"X-Oauth-Scopes": "repo"}},
	)
	c.AppTransport = fake
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	id, err := c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive Operator" || id.Email != "12345+operator@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want the host's name and the login's noreply address", id)
	}

	// A host with no git identity at all: both halves come from the
	// login -- its display name, and its noreply address.
	if err := os.Remove(filepath.Join(state, "git-user-name")); err != nil {
		t.Fatal(err)
	}
	c.hostIdentity = nil
	id, err = c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive from GitHub" || id.Email != "12345+operator@users.noreply.github.com" {
		t.Fatalf("identity = %+v, want the login's own name and noreply address", id)
	}
	// Asked /user once for both: the login is cached per token.
	if len(fake.Calls) != 1 {
		t.Fatalf("/user asked %d times", len(fake.Calls))
	}

	// A login with no display name is named by its login.
	fake.Responses = []github.ApiResponse{{Status: 200, Body: []byte(`{"login":"operator","id":12345}`)}}
	c.identities = nil
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "operator" {
		t.Fatalf("identity for a login without a name = %+v, %v", id, err)
	}
}

func TestAmbientGitIdentityOnGHESUsesThatHostsNoreplyDomain(t *testing.T) {
	id := AmbientIdentity{Login: "operator", ID: 7}
	if got := id.NoreplyEmail("ghe.example.com:8443"); got != "7+operator@users.noreply.ghe.example.com" {
		t.Fatalf("NoreplyEmail = %q", got)
	}
	if got := id.NoreplyEmail(""); got != "7+operator@users.noreply.github.com" {
		t.Fatalf("NoreplyEmail for the default host = %q", got)
	}
	if got := (AmbientIdentity{Login: "x-access-token"}).NoreplyEmail("git.example.com"); got != "" {
		t.Fatalf("NoreplyEmail for a plain git host's helper user = %q, want none", got)
	}
}

func TestAmbientGitIdentityWithNothingOnTheHostSaysSo(t *testing.T) {
	state := fakeHostTools(t)
	c, _ := ambientSet(t)
	c.AppTransport = github.NewFakeTransport()
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	// No git identity and gh not logged in: an error naming what to
	// run, for the caller to log and fall back on grain's own default.
	if _, err := c.AmbientGitIdentity("op"); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("identity with nothing on the host = %v", err)
	}
	// With a name but no login: the name alone, no error -- the other
	// half is the sandbox's to default.
	writeState(t, state, "git-user-name", "Olive Operator")
	c.hostIdentity = nil
	if id, err := c.AmbientGitIdentity("op"); err != nil || id.Name != "Olive Operator" || id.Email != "" {
		t.Fatalf("identity with a name only = %+v, %v", id, err)
	}
	// Anything that is not an ambient credential has no host identity.
	if _, err := c.AmbientGitIdentity("nobody"); err == nil {
		t.Fatal("a non-ambient name answered an identity")
	}
}

func TestHostGitIdentityIsOnlyReadOnAHost(t *testing.T) {
	state := fakeHostTools(t)
	writeState(t, state, "git-user-name", "Olive Operator")
	t.Setenv("GRAIN_IN_CONTAINER", "1")
	c, _ := ambientSet(t)
	if id := c.HostGitIdentity(); !id.Empty() {
		t.Fatalf("HostGitIdentity inside the container = %+v, want nothing", id)
	}
	if n := calls(t, state, "git-calls"); n != 0 {
		t.Fatalf("git ran %d times inside the container", n)
	}
}

func setupFakeHostSSHSigning(t *testing.T) (homeDir, pubKeyLine string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	homeDir = t.TempDir()
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(sshDir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath, "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pubBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pubKeyLine = strings.TrimSpace(string(pubBytes))
	gitconfig := "[user]\n\tname = Olive Signer\n\temail = olive@example.com\n\tsigningkey = ~/.ssh/id_ed25519.pub\n[gpg]\n\tformat = ssh\n[commit]\n\tgpgsign = true\n"
	if err := os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAIN_TEST_AMBIENT_HOME", homeDir)
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	return homeDir, pubKeyLine
}

func TestHostGitIdentityAndSignGitPayloadWithSSHSigningKey(t *testing.T) {
	homeDir, wantPub := setupFakeHostSSHSigning(t)
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	id, err := c.AmbientGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "Olive Signer" || id.Email != "olive@example.com" || id.SigningKey != wantPub {
		t.Fatalf("AmbientGitIdentity = %+v, want SigningKey=%q", id, wantPub)
	}

	credID, err := c.CredentialGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if credID.SigningKey != wantPub {
		t.Fatalf("CredentialGitIdentity SigningKey = %q, want %q", credID.SigningKey, wantPub)
	}

	payload := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Olive Signer <olive@example.com> 1700000000 +0000\ncommitter Olive Signer <olive@example.com> 1700000000 +0000\n\ntest commit\n")
	sig, err := c.SignGitPayload(context.Background(), "op", "git", payload)
	if err != nil {
		t.Fatalf("SignGitPayload: %v", err)
	}
	if !strings.Contains(string(sig), "-----BEGIN SSH SIGNATURE-----") {
		t.Fatalf("SignGitPayload output = %q, want SSH signature block", sig)
	}

	// Verify the signature with ssh-keygen -Y verify.
	allowedFile := filepath.Join(homeDir, "allowed_signers")
	if err := os.WriteFile(allowedFile, []byte("olive@example.com "+wantPub+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sigFile := filepath.Join(homeDir, "payload.sig")
	if err := os.WriteFile(sigFile, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	vcmd := exec.Command("ssh-keygen", "-Y", "verify", "-n", "git", "-f", allowedFile, "-I", "olive@example.com", "-s", sigFile)
	vcmd.Stdin = strings.NewReader(string(payload))
	if out, err := vcmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y verify failed: %v\n%s", err, out)
	}

	// Also verify resolving user.signingkey when it points to the private key or key:: literal.
	gitconfigPriv := "[user]\n\tname = Olive Signer\n\temail = olive@example.com\n\tsigningkey = ~/.ssh/id_ed25519\n[gpg]\n\tformat = ssh\n"
	if err := os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte(gitconfigPriv), 0o600); err != nil {
		t.Fatal(err)
	}
	c.hostIdentity = nil
	if got := c.HostGitIdentity(); got.SigningKey != wantPub {
		t.Fatalf("HostGitIdentity with private key path = %q, want %q", got.SigningKey, wantPub)
	}

	gitconfigInline := "[user]\n\tname = Olive Signer\n\temail = olive@example.com\n\tsigningkey = key::" + wantPub + "\n[gpg]\n\tformat = ssh\n"
	if err := os.WriteFile(filepath.Join(homeDir, ".gitconfig"), []byte(gitconfigInline), 0o600); err != nil {
		t.Fatal(err)
	}
	c.hostIdentity = nil
	if got := c.HostGitIdentity(); got.SigningKey != wantPub {
		t.Fatalf("HostGitIdentity with key:: literal = %q, want %q", got.SigningKey, wantPub)
	}

	// Reject non-"git" namespace and non-ambient credential.
	if _, err := c.SignGitPayload(context.Background(), "op", "file", payload); err == nil {
		t.Fatal("SignGitPayload accepted namespace != git")
	}
	if _, err := c.SignGitPayload(context.Background(), "not-ambient", "git", payload); err == nil {
		t.Fatal("SignGitPayload accepted non-ambient credential")
	}
}
