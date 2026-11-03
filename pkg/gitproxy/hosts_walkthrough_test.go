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
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// localGitHTTPBackend serves bare repositories under projectRoot via git http-backend.
func localGitHTTPBackend(projectRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cmd := exec.Command("git", "http-backend")
		cmd.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+projectRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+r.URL.Path,
			"QUERY_STRING="+r.URL.RawQuery,
			"REQUEST_METHOD="+r.Method,
			"CONTENT_TYPE="+r.Header.Get("Content-Type"),
			"HTTP_CONTENT_ENCODING="+r.Header.Get("Content-Encoding"),
			"GATEWAY_INTERFACE=CGI/1.1",
			"SERVER_PROTOCOL=HTTP/1.1",
		)
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, fmt.Sprintf("git backend: %v: %s", err, string(out)), http.StatusInternalServerError)
			return
		}

		parts := strings.SplitN(string(out), "\r\n\r\n", 2)
		if len(parts) != 2 {
			parts = strings.SplitN(string(out), "\n\n", 2)
		}
		if len(parts) == 2 {
			for _, line := range strings.Split(parts[0], "\n") {
				line = strings.TrimRight(line, "\r")
				if kv := strings.SplitN(line, ": ", 2); len(kv) == 2 {
					w.Header().Set(kv[0], kv[1])
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, parts[1])
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(out)
		}
	}
}

// TestGitHostsPhase1ProxyMigrationAndGuard carries out Step 1 of §3 of docs/test-plans/2026-10-07-evening.md:
//  1. Build the proxy the way the daemon does (BuildProxy), forwarding to an httptest TLS server
//     playing a GHE host. Use an httptest handler standing in for GHE (minting App tokens at /api/v3
//     and serving smart HTTP git at root). Give it a temp secrets/github/ with one PAT and one App
//     credential, and no host records.
//  2. Clone and push through the proxy. Assert both .host.json files are written with the GHE host,
//     and forwarded Authorization is the PAT or the App token minted from /api/v3.
//  3. Restart with ForwardHost set to a second httptest server. Assert clone is refused with
//     the WrongHost sentence naming both hosts, and second server received no request carrying either credential.
//  4. Restart once more on github.com defaults with a fresh store. Assert no .host.json files are written.
func TestGitHostsPhase1ProxyMigrationAndGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Set up temporary directories
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets", "github")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. Provision PAT credential "my-pat"
	patToken := "ghp_walkthrough_pat_12345"
	if err := os.WriteFile(filepath.Join(secretsDir, "my-pat.token"), []byte(patToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Provision GitHub App credential "my-app"
	// Generate valid RSA key for App JWT signing
	appData := map[string]string{
		"app_id":          "12345",
		"installation_id": "67890",
		"private_key":     string(pkcs1PEM(testAppKey(t))),
	}
	appJSON, err := json.Marshal(appData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "my-app.app.json"), appJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	// Initial credentials.json naming both credentials
	patterns := map[string]string{
		"acme/app-repo": "my-app",
		"acme/pat-repo": "my-pat",
		"*":             "my-pat",
	}
	credsJSON, err := json.Marshal(patterns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "credentials.json"), credsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	// Upstream bare repositories
	upstreamDir := t.TempDir()
	initBareRepoWithCommit(t, upstreamDir, "acme", "pat-repo")
	initBareRepoWithCommit(t, upstreamDir, "acme", "app-repo")

	// Combined upstream handler for GHE 1: smart HTTP git backend + GHE /api/v3 App minting
	var receivedHeaders sync.Map
	appTokenValue := "ghs_walkthrough_app_token_minted_999"

	ghe1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record Authorization header seen
		if auth := r.Header.Get("Authorization"); auth != "" {
			receivedHeaders.Store(r.URL.Path, auth)
		}

		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(fmt.Sprintf(`{"token":%q,"expires_at":"2099-01-01T00:00:00Z"}`, appTokenValue)))
			return
		}

		// Git smart HTTP backend
		localGitHTTPBackend(upstreamDir)(w, r)
	})

	ghe1Server := httptest.NewTLSServer(ghe1Handler)
	defer ghe1Server.Close()

	// Trust test server cert in defaultForwardClient
	origTransport := defaultForwardClient.Transport
	defer func() { defaultForwardClient.Transport = origTransport }()
	tClone := origTransport.(*http.Transport).Clone()
	tClone.TLSClientConfig = ghe1Server.Client().Transport.(*http.Transport).TLSClientConfig
	defaultForwardClient.Transport = tClone

	ghe1Host := ghe1Server.Listener.Addr().String()

	// Configure HTTP client for proxy tests
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// Build the real proxy forwarding to ghe1Host
	readScopes := NewReadScopes()
	readScopes.Grant("sandbox-pat", model.RepoRef{Owner: "acme", Name: "pat-repo"})
	readScopes.Grant("sandbox-app", model.RepoRef{Owner: "acme", Name: "app-repo"})
	writeScopes := NewWriteScopes()
	writeScopes.Grant("sandbox-pat", model.RepoRef{Owner: "acme", Name: "pat-repo"})
	writeScopes.Grant("sandbox-app", model.RepoRef{Owner: "acme", Name: "app-repo"})

	proxy1, err := BuildProxy(BuildConfig{
		DataDir:     dataDir,
		ForwardHost: ghe1Host,
		ForwardTLS:  true,
		ReadScopes:  readScopes,
		WriteScopes: writeScopes,
	})
	if err != nil {
		t.Fatalf("BuildProxy: %v", err)
	}
	proxy1.TeamCredentials = ladderConnections{proxy: proxy1}

	proxyServer1 := httptest.NewServer(NewHandler(proxy1))
	defer proxyServer1.Close()

	// Assert that MigrateCredentialHosts wrote .host.json for both credentials
	patHostFile := filepath.Join(secretsDir, "my-pat.host.json")
	appHostFile := filepath.Join(secretsDir, "my-app.host.json")

	patHostData, err := os.ReadFile(patHostFile)
	if err != nil {
		t.Fatalf("reading %s: %v", patHostFile, err)
	}
	var patHostRecord CredentialHost
	if err := json.Unmarshal(patHostData, &patHostRecord); err != nil {
		t.Fatalf("unmarshal pat host: %v", err)
	}
	if patHostRecord.Host != ghe1Host {
		t.Errorf("my-pat host = %q, want %q", patHostRecord.Host, ghe1Host)
	}

	appHostData, err := os.ReadFile(appHostFile)
	if err != nil {
		t.Fatalf("reading %s: %v", appHostFile, err)
	}
	var appHostRecord CredentialHost
	if err := json.Unmarshal(appHostData, &appHostRecord); err != nil {
		t.Fatalf("unmarshal app host: %v", err)
	}
	if appHostRecord.Host != ghe1Host {
		t.Errorf("my-app host = %q, want %q", appHostRecord.Host, ghe1Host)
	}

	// Issue a clone/fetch through proxy1 for pat-repo and app-repo
	// Register sandbox tokens for sandbox-pat and sandbox-app
	tokenStore := NewSandboxTokenStore(filepath.Join(dataDir, "secrets", "sandbox-tokens.json"))
	sbPatToken, err := tokenStore.EnsureToken("sandbox-pat")
	if err != nil {
		t.Fatal(err)
	}
	sbAppToken, err := tokenStore.EnsureToken("sandbox-app")
	if err != nil {
		t.Fatal(err)
	}

	// Git clone pat-repo through proxy1
	cloneDir1 := t.TempDir()
	gitClone(t, proxyServer1.URL, "acme", "pat-repo", sbPatToken, cloneDir1)

	// Check Authorization received at upstream for pat-repo
	authPAT, ok := receivedHeaders.Load("/acme/pat-repo.git/info/refs")
	if !ok {
		t.Fatalf("no Authorization header seen for pat-repo git request")
	}
	wantPATAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(patToken+":"))
	if authPAT != wantPATAuth {
		t.Errorf("PAT Authorization = %q, want %q", authPAT, wantPATAuth)
	}

	// Git clone app-repo through proxy1
	cloneDir2 := t.TempDir()
	gitClone(t, proxyServer1.URL, "acme", "app-repo", sbAppToken, cloneDir2)

	// Check Authorization received at upstream for app-repo
	authApp, ok := receivedHeaders.Load("/acme/app-repo.git/info/refs")
	if !ok {
		t.Fatalf("no Authorization header seen for app-repo git request")
	}
	wantAppAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(appTokenValue+":"))
	if authApp != wantAppAuth {
		t.Errorf("App Authorization = %q, want %q", authApp, wantAppAuth)
	}

	// =========================================================================
	// Step 2: Restart proxy with ForwardHost set to a second httptest server.
	// Assert the clone is refused with WrongHost sentence naming both hosts,
	// and second server received no request carrying either credential.
	// =========================================================================
	var ghe2Received sync.Map
	ghe2Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			ghe2Received.Store(r.URL.Path, auth)
		}
		http.Error(w, "should not be reached", http.StatusInternalServerError)
	})
	ghe2Server := httptest.NewTLSServer(ghe2Handler)
	defer ghe2Server.Close()
	ghe2Host := ghe2Server.Listener.Addr().String()

	proxy2, err := BuildProxy(BuildConfig{
		DataDir:     dataDir, // reuse existing dataDir containing the written .host.json
		ForwardHost: ghe2Host,
		ForwardTLS:  true,
		ReadScopes:  readScopes,
		WriteScopes: writeScopes,
	})
	if err != nil {
		t.Fatalf("BuildProxy 2: %v", err)
	}
	proxy2.TeamCredentials = ladderConnections{proxy: proxy2}
	proxyServer2 := httptest.NewServer(NewHandler(proxy2))
	defer proxyServer2.Close()

	// Attempt git clone through proxyServer2
	cloneDirFail := t.TempDir()
	cloneOutput, cloneErr := gitCloneOutput(proxyServer2.URL, "acme", "pat-repo", sbPatToken, cloneDirFail)
	if cloneErr == nil {
		t.Fatalf("expected git clone through proxy 2 to fail, but succeeded")
	}

	// Check WrongHost sentence:
	// "credential \"my-pat\" is for git host <ghe1Host>, and this request goes to <ghe2Host> -- grain never sends one host's credential to another; add a git connection for <ghe2Host>"
	wantSubstr := fmt.Sprintf(`credential "my-pat" is for git host %s, and this request goes to %s -- grain never sends one host's credential to another; add a git connection for %s`,
		ghe1Host, ghe2Host, ghe2Host)
	if !strings.Contains(cloneOutput, wantSubstr) {
		t.Errorf("clone output:\n%s\nwant it to contain:\n%q", cloneOutput, wantSubstr)
	}

	// Assert that ghe2Server received no request carrying either credential
	ghe2Received.Range(func(key, value any) bool {
		authStr := fmt.Sprintf("%v", value)
		if strings.Contains(authStr, patToken) || strings.Contains(authStr, appTokenValue) {
			t.Errorf("ghe2Server received credential leak on path %v: %v", key, value)
		}
		return true
	})

	// =========================================================================
	// Step 3: Restart once more on github.com defaults with a fresh store.
	// Assert no .host.json files are written.
	// =========================================================================
	freshDataDir := t.TempDir()
	freshSecretsDir := filepath.Join(freshDataDir, "secrets", "github")
	if err := os.MkdirAll(freshSecretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(freshSecretsDir, "dotcom-pat.token"), []byte("ghp_dotcom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dotcomCredsJSON, _ := json.Marshal(map[string]string{
		"*": "dotcom-pat",
	})
	if err := os.WriteFile(filepath.Join(freshSecretsDir, "credentials.json"), dotcomCredsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = BuildProxy(BuildConfig{
		DataDir:     freshDataDir,
		ForwardHost: "github.com",
		ForwardTLS:  true,
	})
	if err != nil {
		t.Fatalf("BuildProxy 3: %v", err)
	}

	dotcomHostFile := filepath.Join(freshSecretsDir, "dotcom-pat.host.json")
	if _, err := os.Stat(dotcomHostFile); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist on github.com defaults, but it exists", dotcomHostFile)
	}
}

func initBareRepoWithCommit(t *testing.T, parentDir, owner, repo string) string {
	t.Helper()
	repoDir := filepath.Join(parentDir, owner, repo+".git")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runCmd(t, "", "git", "init", "--bare", repoDir)
	// Enable receivepack
	runCmd(t, "", "git", "-C", repoDir, "config", "http.receivepack", "true")

	// Create a temporary clone to commit a file
	tmp := t.TempDir()
	runCmd(t, "", "git", "clone", repoDir, filepath.Join(tmp, "work"))
	work := filepath.Join(tmp, "work")
	runCmd(t, work, "git", "config", "user.email", "test@example.com")
	runCmd(t, work, "git", "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, work, "git", "add", "README.md")
	runCmd(t, work, "git", "commit", "-m", "init")
	runCmd(t, work, "git", "push", "origin", "HEAD:main")
	return repoDir
}

func gitClone(t *testing.T, proxyBaseURL, owner, repo, token, targetDir string) {
	t.Helper()
	out, err := gitCloneOutput(proxyBaseURL, owner, repo, token, targetDir)
	if err != nil {
		t.Fatalf("git clone failed: %v\noutput:\n%s", err, out)
	}
}

func gitCloneOutput(proxyBaseURL, owner, repo, token, targetDir string) (string, error) {
	// construct URL with token: http://x-access-token:<token>@<proxyHost>/<owner>/<repo>.git
	u := strings.TrimPrefix(proxyBaseURL, "http://")
	cloneURL := fmt.Sprintf("http://x-access-token:%s@%s/%s/%s.git", token, u, owner, repo)

	cmd := exec.Command("git", "clone", cloneURL, targetDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\noutput: %s", name, args, err, string(out))
	}
}
