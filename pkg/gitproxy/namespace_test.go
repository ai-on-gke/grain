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

package gitproxy_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/namespace"
	"github.com/ai-on-gke/grain/pkg/redact"
)

type fakeNamespaceScopes struct {
	scopes map[string]model.NamespaceScope
}

func (f fakeNamespaceScopes) SandboxNamespaceScope(_ context.Context, sandbox string) (model.NamespaceScope, bool, error) {
	sc, ok := f.scopes[sandbox]
	return sc, ok, nil
}

func basicAuthHeader(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

func TestGitProxyNamespaceAuthAndGrantScoping(t *testing.T) {
	ciStore := ci.NewRunStore(10)
	started := time.Now().Add(-time.Minute)
	finished := time.Now()
	ciStore.Put(ci.Run{
		ID:         "run-900",
		Repo:       "bwsalmon/grain",
		Conclusion: ci.ConclusionFailure,
		StartedAt:  &started,
		FinishedAt: &finished,
		Jobs: []ci.JobRun{
			{
				Name:       "go",
				Conclusion: ci.ConclusionFailure,
				Steps: []ci.StepRun{
					{
						Name:       "Test",
						Conclusion: ci.ConclusionFailure,
						Output:     "=== RUN   TestWidget\n    widget_test.go:11: boom token=ghp_1234567890abcdefghijklmnopqrstuvwx\n--- FAIL: TestWidget (0.01s)\nFAIL\tgithub.com/ai-on-gke/grain/pkg/widget\t0.020s\n",
					},
				},
			},
		},
	})

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("grain source tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tokenFile := filepath.Join(t.TempDir(), "sandbox-tokens.json")
	rawTokens, _ := json.Marshal(map[string]string{
		"sb-no-ci":   "tok-no-ci",
		"sb-ci":      "tok-ci",
		"sb-debug":   "tok-debug",
		"sb-stopped": "tok-stopped",
	})
	if err := os.WriteFile(tokenFile, rawTokens, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	proxy := &gitproxy.GitProxy{
		Tokens: tokens,
		Namespace: &namespace.Server{
			CIStore:  ciStore,
			SrcDir:   srcDir,
			Redactor: redact.New(nil),
		},
		NamespaceScopes: fakeNamespaceScopes{
			scopes: map[string]model.NamespaceScope{
				"sb-no-ci": {
					TaskID:    "1",
					TaskRepo:  "bwsalmon/grain",
					MayReadCI: false,
				},
				"sb-ci": {
					TaskID:    "2",
					TaskRepo:  "bwsalmon/grain",
					MayReadCI: true,
				},
				"sb-debug": {
					TaskID:       "3",
					TaskRepo:     "bwsalmon/grain",
					MayReadCI:    true,
					MayReadAllCI: true,
					MayReadSrc:   true,
				},
			},
		},
	}

	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer srv.Close()

	// 1. Unauthenticated request -> 401
	resp, err := http.Get(srv.URL + "/fs/ci/bwsalmon/grain/run-900/go/Test.log")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	// 2. Sandbox without ci-read -> 403 on ci://
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/fs/ci/bwsalmon/grain/run-900/go/Test.log", nil)
	req.Header.Set("Authorization", basicAuthHeader("tok-no-ci"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-ci status = %d, want 403", resp.StatusCode)
	}

	// 3. Sandbox with no live task -> 403
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/fs/ci/bwsalmon/grain/run-900/go/Test.log", nil)
	req.Header.Set("Authorization", basicAuthHeader("tok-stopped"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stopped sandbox status = %d, want 403", resp.StatusCode)
	}

	// 4. Sandbox with ci-read -> 200 on ci://, 403 on src://
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/fs/src/README.md", nil)
	req.Header.Set("Authorization", basicAuthHeader("tok-ci"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ci-only on src:// status = %d, want 403", resp.StatusCode)
	}

	// 5. Sandbox with self-debug -> 200 on src://
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/fs/src/README.md", nil)
	req.Header.Set("Authorization", basicAuthHeader("tok-debug"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("self-debug on src:// status = %d, want 200", resp.StatusCode)
	}

	// 6. Verify curl inside a sandbox configured by ConfigureGitCredentials
	// authenticates automatically via ~/.curlrc + ~/.git-credentials.netrc!
	sandboxHome := t.TempDir()
	if err := mcp.ConfigureGitCredentials(sandboxHome, srv.URL, "tok-ci", mcp.GitIdentity{Name: "grain", Email: "grain@localhost"}); err != nil {
		t.Fatalf("ConfigureGitCredentials: %v", err)
	}
	cmd := exec.Command("curl", "-sf", srv.URL+"/fs/ci/bwsalmon/grain/run-900/go/tests/github.com/ai-on-gke/grain/pkg/widget/TestWidget.log")
	cmd.Env = []string{
		"HOME=" + sandboxHome,
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("curl failed: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "--- FAIL: TestWidget") {
		t.Fatalf("curl output missing per-test failure:\n%s", text)
	}
	if strings.Contains(text, "ghp_1234567890") {
		t.Fatalf("curl output leaked secret token:\n%s", text)
	}
}
