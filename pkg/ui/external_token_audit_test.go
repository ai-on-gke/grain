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
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// docs/test-runs/2026-11-03-operator-attribution.md F6: issuing,
// rotating and revoking an external agent token is logged as its only
// audit trail (a credential has no config_audit row), but the lines did
// not say who did it. Each names the writer as config writes do.
//
// Not parallel: it reads the standard logger's output.
func TestExternalAgentTokenWritesLogWhoMadeThem(t *testing.T) {
	srv, _ := testServer(t)
	ui.SetAccessCodeForTest(srv, testAccessCode)
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	h := map[string]string{"Authorization": "Bearer " + testAccessCode, "Content-Type": "application/json"}
	rec := serveFrom(srv, "127.0.0.1:5555", http.MethodPost, "/api/external-agents/tokens", `{"name":"ext1","scopes":["tasks:read"]}`, h)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/external-agents/tokens: %d %s", rec.Code, rec.Body)
	}
	var issued struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if rec := serveFrom(srv, "127.0.0.1:5555", http.MethodPost, "/api/external-agents/tokens/"+issued.ID+"/revoke", `{}`, h); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{
		"grain external-agent: operator (access code) (access-code) issued credential id=" + issued.ID,
		"grain external-agent: operator (access code) (access-code) revoked credential id=" + issued.ID,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), issued.Token) {
		t.Error("the log carries the token itself")
	}
}
