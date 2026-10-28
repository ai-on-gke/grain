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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/mcp"
)

func TestEndToEndSandboxSSHCommitAndTagSigningViaGitProxy(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	_, wantPub := setupFakeHostSSHSigning(t)

	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	tokenStore := NewSandboxTokenStore(tokenPath)
	sandboxToken, err := tokenStore.EnsureToken("sandbox-sign")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(tokenPath)
	if err != nil {
		t.Fatal(err)
	}

	proxy := &GitProxy{
		Authorizer:          stubAuthorizer{allow: map[[3]string]bool{{"sandbox-sign", "owner", "repo"}: true}},
		Credentials:         c,
		Tokens:              tokens,
		CredentialOverrides: fixedOverride("op"),
	}
	srv := httptest.NewServer(NewHandler(proxy))
	defer srv.Close()

	id, err := c.CredentialGitIdentity("op")
	if err != nil {
		t.Fatal(err)
	}
	if id.SigningKey != wantPub {
		t.Fatalf("CredentialGitIdentity SigningKey = %q, want %q", id.SigningKey, wantPub)
	}

	sandboxHome := t.TempDir()
	remoteURL := srv.URL + "/owner/repo.git"
	if err := mcp.ConfigureGitCredentials(sandboxHome, remoteURL, sandboxToken, mcp.GitIdentity{
		Name:       id.Name,
		Email:      id.Email,
		SigningKey: id.SigningKey,
	}); err != nil {
		t.Fatalf("ConfigureGitCredentials: %v", err)
	}

	// Ensure no private key file exists anywhere in the sandbox directory.
	entries, err := os.ReadDir(sandboxHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(sandboxHome, e.Name()))
		if strings.Contains(string(data), "PRIVATE KEY") {
			t.Fatalf("sandbox file %s contains a private key", e.Name())
		}
	}

	repoDir := filepath.Join(sandboxHome, "work")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + sandboxHome,
			"GIT_CONFIG_NOSYSTEM=1",
			"LC_ALL=C",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	runGit("init", "-q")
	runGit("commit", "--allow-empty", "-m", "test signed commit")

	rawCommit := runGit("cat-file", "commit", "HEAD")
	if !strings.Contains(rawCommit, "gpgsig -----BEGIN SSH SIGNATURE-----") {
		t.Fatalf("commit object lacks gpgsig header:\n%s", rawCommit)
	}

	sigLog := runGit("log", "--show-signature", "-1")
	if !strings.Contains(sigLog, `Good "git" signature`) {
		t.Fatalf("git log --show-signature did not report Good \"git\" signature:\n%s", sigLog)
	}

	// Also verify annotated tag signing (tag.gpgSign = true).
	runGit("tag", "-m", "test signed tag", "v1.0.0")
	rawTag := runGit("cat-file", "tag", "v1.0.0")
	if !strings.Contains(rawTag, "-----BEGIN SSH SIGNATURE-----") {
		t.Fatalf("tag object lacks SSH signature:\n%s", rawTag)
	}

	// Unauthenticated requests to /_grain/git-sign are rejected with 401.
	resp, err := http.Post(srv.URL+"/_grain/git-sign", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /_grain/git-sign status = %d, want 401", resp.StatusCode)
	}

	// Non-ambient credentials are rejected with 403.
	if err := c.SetToken("pat", "ghp_secret"); err != nil {
		t.Fatal(err)
	}
	proxy.CredentialOverrides = fixedOverride("pat")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/_grain/git-sign", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("sandbox", sandboxToken)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("non-ambient /_grain/git-sign status = %d, want 403", resp2.StatusCode)
	}
}
