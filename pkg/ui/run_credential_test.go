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
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

var testRunCredentialKey = bytes.Repeat([]byte{7}, 32)

// runCredentialServer is a server issuing run credentials, with the
// access code set and task 2 (api:tasks:read) running as run r-2.
func runCredentialServer(t *testing.T) (*ui.Server, *ui.Client) {
	t.Helper()
	client, _, _ := testClient(t)
	client.Config.RunCredentialKey = testRunCredentialKey
	srv := ui.NewServerWithClient(client)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	seedAPITask(t, client, "1", "acme/widgets")
	seedAPITask(t, client, "2", "acme/widgets", "api:tasks:read")
	if err := client.Store.StartRun(context.Background(), model.Run{ID: "r-2", TaskID: "2", Attempt: 1, StartedAt: time.Now()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	return srv, client
}

func mintRunCredential(t *testing.T, taskID, runID string) string {
	t.Helper()
	tok, err := ui.MintRunCredential(testRunCredentialKey, taskID, runID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// The access code says nothing about which task its holder is, so once
// runs carry their own credential an acting-task header beside the code
// alone is refused, on the REST gate and on POST /api/mcp alike.
func TestAnActingTaskHeaderWithOnlyTheAccessCodeIsRefused(t *testing.T) {
	srv, _ := runCredentialServer(t)
	hdr := map[string]string{"Authorization": "Bearer " + testAccessCode, ui.ActingTaskHeader: "2"}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", hdr); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "run credential") {
		t.Errorf("GET /api/tasks acting as task 2 with the access code: %d %s, want 401 asking for the run credential", rec.Code, rec.Body)
	}
	hdr["Content-Type"] = "application/json"
	init := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + ui.ToolGrainListTasks + `","arguments":{}}}`
	if rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/mcp", init, hdr); !strings.Contains(rec.Body.String(), "run credential") {
		t.Errorf("POST /api/mcp acting as task 2 with the access code: %d %s, want a refusal asking for the run credential", rec.Code, rec.Body)
	}
	// Without the header the code is the operator, as it always was.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + testAccessCode}); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks with the access code: %d %s", rec.Code, rec.Body)
	}
}

// A run's credential acts for its own task, held to that task's grants,
// and for no other.
func TestARunCredentialActsForItsOwnTaskOnly(t *testing.T) {
	srv, _ := runCredentialServer(t)
	tok := mintRunCredential(t, "2", "r-2")
	auth := "Bearer " + tok

	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": auth, ui.ActingTaskHeader: "2"}); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks acting as task 2 with its run's credential: %d %s", rec.Code, rec.Body)
	}
	// Held to task 2's grants: api:tasks:read does not comment.
	if rec := serveFrom(srv, networkAddr, http.MethodPost, "/api/tasks/1/comments", `{"body":"hi"}`,
		map[string]string{"Authorization": auth, ui.ActingTaskHeader: "2", "Content-Type": "application/json"}); rec.Code != http.StatusForbidden {
		t.Errorf("task 2's run commented without tasks:write: %d %s", rec.Code, rec.Body)
	}
	// Naming another task, or another run, is refused.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": auth, ui.ActingTaskHeader: "1"}); rec.Code != http.StatusForbidden {
		t.Errorf("task 2's run credential acting as task 1: %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": auth, ui.ActingTaskHeader: "2", ui.ActingRunHeader: "r-9"}); rec.Code != http.StatusForbidden {
		t.Errorf("task 2's run credential naming run r-9: %d %s, want 403", rec.Code, rec.Body)
	}
	// A forged or foreign-keyed credential is refused.
	other, _ := ui.MintRunCredential(bytes.Repeat([]byte{9}, 32), "2", "r-2")
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + other, ui.ActingTaskHeader: "2"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a credential signed with another key: %d %s, want 401", rec.Code, rec.Body)
	}
	// The run's own plumbing, naming no task, is let in as before.
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks/2", "", map[string]string{"Authorization": auth}); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks/2 with the run credential and no acting task: %d %s", rec.Code, rec.Body)
	}
}

// A credential is good while its run is live and no longer.
func TestARunCredentialEndsWithItsRun(t *testing.T) {
	srv, client := runCredentialServer(t)
	tok := mintRunCredential(t, "2", "r-2")
	if err := client.Store.FinishRun(context.Background(), "r-2", time.Now(), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + tok}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a finished run's credential: %d %s, want 401", rec.Code, rec.Body)
	}
	unknown := mintRunCredential(t, "2", "r-never")
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + unknown}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a credential for a run that never started: %d %s, want 401", rec.Code, rec.Body)
	}
}

// Off refuses the code on every request, a run's acting-task requests
// included, while the run's own credential still reaches the API.
func TestOffModeRefusesTheCodeOnEveryRequest(t *testing.T) {
	srv, _ := runCredentialServer(t)
	ui.SetAccessCodeModeForTest(srv, ui.AccessCodeOff)
	code := "Bearer " + testAccessCode
	for _, hdr := range []map[string]string{
		{"Authorization": code},
		{"Authorization": code, ui.ActingTaskHeader: "2"},
		{"Authorization": code, ui.ActingTaskHeader: "2", ui.ActingRunHeader: "r-2"},
		{ui.AccessCodeHeader: testAccessCode, ui.ActingTaskHeader: "2"},
	} {
		if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", hdr); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/tasks with the code switched off and %v: %d %s, want 401", hdr, rec.Code, rec.Body)
		}
	}
	tok := mintRunCredential(t, "2", "r-2")
	if rec := serveFrom(srv, networkAddr, http.MethodGet, "/api/tasks", "", map[string]string{"Authorization": "Bearer " + tok, ui.ActingTaskHeader: "2"}); rec.Code != http.StatusOK {
		t.Errorf("GET /api/tasks with the run's credential and the code switched off: %d %s", rec.Code, rec.Body)
	}
}

func TestMintRunCredentialNeedsATaskARunAndAKey(t *testing.T) {
	if _, err := ui.MintRunCredential(nil, "1", "r"); err == nil {
		t.Error("minted with no key")
	}
	if _, err := ui.MintRunCredential(testRunCredentialKey, "", "r"); err == nil {
		t.Error("minted for no task")
	}
	if _, err := ui.MintRunCredential(testRunCredentialKey, "1", ""); err == nil {
		t.Error("minted for no run")
	}
}

func TestEnsureRunCredentialKeyWritesOnceAndKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), ui.RunCredentialKeyFileName)
	first, err := ui.EnsureRunCredentialKey(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ui.EnsureRunCredentialKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) || len(first) != 32 {
		t.Fatalf("key changed or wrong size: %x then %x", first, again)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v, want 0600", fi.Mode(), err)
	}
}
