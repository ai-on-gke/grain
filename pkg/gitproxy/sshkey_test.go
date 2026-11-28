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
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ai-on-gke/grain/pkg/model"
)

func testSSHKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	return string(pem.EncodeToMemory(block)), sshPub
}

func TestSetSSHKeyPinsHostKeysAndStoresPrivately(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := testSSHKey(t, "")
	_, hostKey := testSSHKey(t, "")
	hostLine := "git.example.com " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))

	// Never trust on first use: a host other than github.com needs its
	// key pinned.
	err = c.SetSSHKey("plain", SSHKeyFile{Host: "git.example.com", PrivateKey: key}, model.GitHostGit)
	if err == nil || !strings.Contains(err.Error(), "never trusts a host key on first use") {
		t.Fatalf("SetSSHKey with no known_hosts = %v, want the TOFU refusal", err)
	}
	// An entry for another host pins nothing for this one.
	err = c.SetSSHKey("plain", SSHKeyFile{Host: "git.example.com", Port: 2222, PrivateKey: key, KnownHosts: hostLine}, model.GitHostGit)
	if err == nil || !strings.Contains(err.Error(), "[git.example.com]:2222") {
		t.Fatalf("SetSSHKey with a port-22 entry for a port-2222 host = %v", err)
	}
	if err := c.SetSSHKey("plain", SSHKeyFile{Host: "git.example.com", PrivateKey: key, KnownHosts: hostLine}, model.GitHostGit); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "plain.ssh.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("plain.ssh.json: %v, mode %v; want 0600", err, info.Mode())
	}
	if got := c.AuthType("plain"); got != AuthTypeSSH {
		t.Errorf("AuthType = %q", got)
	}
	if h := c.HostOf("plain"); h.Host != "git.example.com" || h.Kind != model.GitHostGit {
		t.Errorf("HostOf = %+v", h)
	}
	cred, ok := c.Get("plain")
	if !ok || cred.SSH == nil || cred.Token != nil || cred.SSH.Addr != "git.example.com:22" || cred.SSH.User != "git" {
		t.Fatalf("Get = %+v, %v", cred, ok)
	}
	if !strings.Contains(strings.Join(c.Names(), ","), "plain") {
		t.Errorf("Names = %v, want the SSH credential listed", c.Names())
	}

	// A PAT saved over it replaces it, and the SSH file is gone.
	if err := c.SetToken("plain", "ghp_x"); err == nil {
		t.Error("SetToken wrote a token an SSH credential would shadow")
	}
	if err := c.SetPAT("plain", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	if c.IsSSH("plain") || c.AuthType("plain") != AuthTypePAT {
		t.Errorf("after SetPAT: IsSSH=%v AuthType=%q", c.IsSSH("plain"), c.AuthType("plain"))
	}
}

func TestSSHKeyOnGitHubDefaultsToGitHubsHostKeys(t *testing.T) {
	c, _ := LoadCredentialSet(t.TempDir())
	key, _ := testSSHKey(t, "s3cret")
	err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key}, model.GitHostGitHub)
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("an encrypted key with no passphrase = %v", err)
	}
	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key, Passphrase: "s3cret"}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("gh")
	if cred.SSH == nil || len(cred.SSH.hostKeys) != 2 {
		t.Fatalf("github.com SSH credential = %+v, want GitHub's two published host keys pinned", cred.SSH)
	}
	data, _ := os.ReadFile(filepath.Join(c.Dir(), "gh.ssh.json"))
	if !strings.Contains(string(data), "AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl") {
		t.Error("the pinned GitHub key is not written into the credential, where an operator can see it")
	}
}

func TestSSHKeyAPIPairing(t *testing.T) {
	c, _ := LoadCredentialSet(t.TempDir())
	key, _ := testSSHKey(t, "")
	if err := c.SetToken("api", "ghp_api"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key, APICredential: "missing"}, model.GitHostGitHub); err == nil {
		t.Error("paired an SSH key with a credential that does not exist")
	}
	if err := c.SetToken("ghe-api", "ghp_ghe"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHost("ghe-api", CredentialHost{Host: "ghe.example.com", Kind: model.GitHostGitHub}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key, APICredential: "ghe-api"}, model.GitHostGitHub); err == nil ||
		!strings.Contains(err.Error(), "not github.com") {
		t.Errorf("paired with another host's credential: %v", err)
	}

	// Unpaired: git-only, and said so.
	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	cred, _ := c.Get("gh")
	if _, ok := c.APICredential(cred); ok {
		t.Error("an unpaired SSH key reported an API credential")
	}
	if reason := GitOnlyReason(cred, "acme", "widgets"); !strings.Contains(reason, "covers git only") {
		t.Errorf("GitOnlyReason = %q", reason)
	}

	if err := c.SetSSHKey("gh", SSHKeyFile{Host: "github.com", PrivateKey: key, APICredential: "api"}, model.GitHostGitHub); err != nil {
		t.Fatal(err)
	}
	cred, _ = c.Get("gh")
	api, ok := c.APICredential(cred)
	if !ok || api.Token == nil || *api.Token != "ghp_api" {
		t.Errorf("APICredential = %+v, %v; want the paired token", api, ok)
	}
}

// One team, an SSH key on a plain git host and a GitHub App on a GHE
// instance: each connection resolves on its own host only, and each
// credential keeps its own type and host.
func TestTeamMixesSSHOnAGitHostWithAnAppOnGHE(t *testing.T) {
	c, _ := LoadCredentialSet(t.TempDir())
	key, _ := testSSHKey(t, "")
	_, hostKey := testSSHKey(t, "")
	if err := c.SetSSHKey("team-git", SSHKeyFile{Host: "git.example.com", PrivateKey: key,
		KnownHosts: "git.example.com " + string(ssh.MarshalAuthorizedKey(hostKey))}, model.GitHostGit); err != nil {
		t.Fatal(err)
	}
	if err := c.SetApp("team-ghe", "12", "34", string(pkcs1PEM(testAppKey(t)))); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHost("team-ghe", CredentialHost{Host: "ghe.example.com", Kind: model.GitHostGitHub}); err != nil {
		t.Fatal(err)
	}
	conns := []model.TeamGitConnection{
		{Credential: "team-git", Host: "git.example.com"},
		{Credential: "team-ghe", Host: "ghe.example.com"},
	}
	for host, want := range map[string]string{"git.example.com": "team-git", "ghe.example.com": "team-ghe"} {
		conn, ok := model.MatchTeamGitConnection(conns, host, model.DefaultGitHost, "acme", "widgets")
		if !ok || conn.Credential != want {
			t.Errorf("connection on %s = %+v, %v; want %s", host, conn, ok, want)
		}
	}
	if _, ok := model.MatchTeamGitConnection(conns, model.DefaultGitHost, model.DefaultGitHost, "acme", "widgets"); ok {
		t.Error("a github.com repo matched a connection on another host")
	}
	if c.AuthType("team-git") != AuthTypeSSH || c.AuthType("team-ghe") != AuthTypeApp {
		t.Errorf("auth types: %q, %q", c.AuthType("team-git"), c.AuthType("team-ghe"))
	}
	if h := c.HostOf("team-ghe"); h.Host != "ghe.example.com" {
		t.Errorf("App host = %+v", h)
	}
	// The App on GHE is never handed to the SSH host, and vice versa:
	// a set forwarding to the GHE host refuses the SSH credential.
	c.ForwardHost = "ghe.example.com"
	c.forget("team-git")
	if cred, _ := c.Get("team-git"); cred.WrongHost == "" || cred.SSH != nil {
		t.Errorf("SSH credential for git.example.com served on ghe.example.com: %+v", cred)
	}
}

func TestSSHKeyPreservesPortCarriedInHost(t *testing.T) {
	c, err := LoadCredentialSet(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := testSSHKey(t, "")
	_, hostKey := testSSHKey(t, "")
	pubText := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey)))

	// Host carrying a non-22 port with Port==0 dials that port and matches
	// the bracketed [host]:port known_hosts entry.
	err = c.SetSSHKey("local-ssh", SSHKeyFile{
		Host:       "127.0.0.1:4295",
		PrivateKey: key,
		KnownHosts: "[127.0.0.1]:4295 " + pubText,
	}, model.GitHostGit)
	if err != nil {
		t.Fatalf("SetSSHKey with host:port = %v", err)
	}
	cred, ok := c.Get("local-ssh")
	if !ok || cred.SSH == nil {
		t.Fatalf("Get(local-ssh) = %+v, %v", cred, ok)
	}
	if cred.SSH.Addr != "127.0.0.1:4295" {
		t.Errorf("Addr = %q, want 127.0.0.1:4295", cred.SSH.Addr)
	}

	// Explicit Port overrides any port carried in Host.
	err = c.SetSSHKey("override-port", SSHKeyFile{
		Host:       "127.0.0.1:4295",
		Port:       2222,
		PrivateKey: key,
		KnownHosts: "[127.0.0.1]:2222 " + pubText,
	}, model.GitHostGit)
	if err != nil {
		t.Fatalf("SetSSHKey with explicit Port override = %v", err)
	}
	if cred, _ := c.Get("override-port"); cred.SSH == nil || cred.SSH.Addr != "127.0.0.1:2222" {
		t.Errorf("explicit Port override Addr = %+v, want 127.0.0.1:2222", cred.SSH)
	}

	// IPv6 host:port with Port==0 keeps its port.
	if got := sshAddr("[::1]:2222", 0); got != "[::1]:2222" {
		t.Errorf("sshAddr([::1]:2222, 0) = %q, want [::1]:2222", got)
	}

	// A non-22 port on github.com must not silently pin port-22 GitHub host keys.
	err = c.SetSSHKey("gh-alt", SSHKeyFile{
		Host:       "github.com:2222",
		PrivateKey: key,
	}, model.GitHostGitHub)
	if err == nil || !strings.Contains(err.Error(), "[github.com]:2222") {
		t.Fatalf("SetSSHKey on github.com:2222 with empty known_hosts = %v, want [github.com]:2222 refusal", err)
	}

	// An out-of-range port carried in Host is refused cleanly.
	err = c.SetSSHKey("bad-port", SSHKeyFile{
		Host:       "git.example.com:70000",
		PrivateKey: key,
		KnownHosts: "[git.example.com]:70000 " + pubText,
	}, model.GitHostGit)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("SetSSHKey with port 70000 in host = %v, want out of range error", err)
	}
}
