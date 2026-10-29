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

package hostinit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The regression from task 1710: checkSandboxBudget printed
// `curl -s -X PUT .../api/settings ...` with no access code, which a
// single-tenant daemon answers 401, so `grain init` exited 10 naming a
// fix that could not work. Every API command a step prints is run here,
// verbatim, through a shell and against a server that wants the access
// code the way the daemon does, and has to get a 200.
func TestPrintedAPICommandsCarryTheAccessCode(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	const code = "the-access-code"
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, localAPITokenFileName), []byte(code+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The command reads the token under sudo; a stub stands in for it.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(localAPITokenFileEnv, "")

	var mu sync.Mutex
	var served []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+code {
			http.Error(w, `{"error":"this daemon is single-tenant and every caller shows its access code"}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		served = append(served, req.Method+" "+req.URL.Path+" "+string(body))
		mu.Unlock()
	}))
	defer srv.Close()

	opts := Options{
		DataDir:              dataDir,
		UIAddr:               strings.TrimPrefix(srv.URL, "http://"),
		GitHubRepos:          []string{"o/r"},
		GitHubToken:          "ghp_x",
		GitHubCredentialName: "github",
	}.withDefaults()
	base := "http://" + opts.UIAddr

	// Each step's own request is refused, so each prints its command.
	var r Result
	sys := newFakeSystem()
	sys.onHTTPRequest("GET", base+"/api/settings", 200, []byte(smallHostSettingsJSON), nil)
	sys.onHTTPRequest("GET", base+"/api/deployment", 200, []byte(`{}`), nil)
	sys.onHTTPRequest("PUT", base+"/api/deployment", 500, nil, nil)
	sys.onHTTPRequest("POST", base+"/api/deployment/git-connections", 500, nil, nil)
	ctx := context.Background()
	checkSandboxBudget(ctx, sys, opts, &r, true)
	ensureTeamTargetRepos(ctx, sys, opts, &r, true)
	ensureTeamGitCredential(ctx, sys, opts, &r, true)

	// And with the reads refused, the GETs they print instead.
	failing := newFakeSystem()
	failing.onHTTPRequest("GET", base+"/api/settings", 500, nil, nil)
	failing.onHTTPRequest("GET", base+"/api/deployment", 500, nil, nil)
	checkSandboxBudget(ctx, failing, opts, &r, true)
	ensureTeamTargetRepos(ctx, failing, opts, &r, true)
	ensureTeamGitCredential(ctx, failing, opts, &r, true)

	if len(r.Steps) != 6 {
		t.Fatalf("Steps = %+v, want six", r.Steps)
	}
	for _, s := range r.Steps {
		if s.Status != StepManual || s.Command == "" {
			t.Errorf("step %q = %s with command %q, want a manual step with a command", s.Name, s.Status, s.Command)
			continue
		}
		out, err := exec.Command("sh", "-c", s.Command+" -o /dev/null -w '%{http_code}'").CombinedOutput()
		if err != nil || string(out) != "200" {
			t.Errorf("step %q: %s\nanswered %q (%v), want 200", s.Name, s.Command, out, err)
		}
	}
	want := []string{
		`PUT /api/settings {"sandboxMemoryMb":6144,"sandboxMemoryReserveMb":1792}`,
		`PUT /api/deployment {"targetRepos":["o/r"]}`,
		`POST /api/deployment/git-connections {"credential":"github","host":"github.com"}`,
		"GET /api/settings ",
		"GET /api/deployment ",
		"GET /api/deployment ",
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(served, "\n") != strings.Join(want, "\n") {
		t.Errorf("served:\n%s\nwant:\n%s", strings.Join(served, "\n"), strings.Join(want, "\n"))
	}
}

// On the Kubernetes platforms the token file is on the pod's volume, not
// this machine's -data-dir, so the command asks the pod for the code,
// in the cluster and namespace init itself used.
func TestAPICurlCommandOnKubernetesAsksThePod(t *testing.T) {
	opts := Options{Platform: PlatformK8sGranule, K8s: K8sOptions{Context: "kind-x"},
		K8sGranule: K8sGranuleOptions{Namespace: "ns", Deployment: "d"}}.withDefaults()
	got := apiCurlCommand(opts, http.MethodGet, "http://127.0.0.1:4242/api/settings", nil)
	want := `curl -s http://127.0.0.1:4242/api/settings ` +
		`-H "Authorization: Bearer $(kubectl --context=kind-x -n ns exec deploy/d -- grain access-code -data-dir=/data)"`
	if got != want {
		t.Errorf("command = %q\nwant      %q", got, want)
	}
}
