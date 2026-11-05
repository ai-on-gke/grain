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
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestQuarantineTestRouteAuthorizationAndProposal(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	seedAPITask(t, client, "10", "acme/widgets")
	seedAPITask(t, client, "11", "acme/widgets", "api:tests:write@acme/widgets")
	seedAPITask(t, client, "12", "acme/widgets", "api:test-quarantine")
	seedAPITask(t, client, "13", "acme/widgets", "test-quarantine")

	// Missing grant is refused with 403 naming tests:write.
	if code, body := asAgent(t, srv, "10", http.MethodPost, "/api/tests/quarantine",
		`{"testName":"pkg/foo.TestFlaky","reason":"timing flake"}`, ""); code != http.StatusForbidden || !strings.Contains(body, "tests:write") {
		t.Fatalf("missing grant: got %d %s, want 403 naming tests:write", code, body)
	}

	// Repo-scoped grant succeeds on own repo (defaulted from acting task) and refuses another repo.
	if code, body := asAgent(t, srv, "11", http.MethodPost, "/api/tests/quarantine",
		`{"repo":"acme/gadgets","testName":"pkg/foo.TestFlaky","reason":"timing flake"}`, ""); code != http.StatusForbidden {
		t.Fatalf("cross-repo call: got %d %s, want 403", code, body)
	}
	code, body := asAgent(t, srv, "11", http.MethodPost, "/api/tests/quarantine",
		`{"testName":"pkg/foo.TestFlaky","reason":"timing flake","ttl":"14d"}`, "")
	if code != http.StatusOK {
		t.Fatalf("repo-scoped own-repo call: got %d %s, want 200", code, body)
	}
	var resp ui.QuarantineTestResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decoding response: %v (%s)", err, body)
	}
	if resp.TaskID != "11" || resp.FollowUpTaskID == "" || resp.TTL != "14d" {
		t.Fatalf("unexpected response fields: %+v", resp)
	}
	cfg, err := ci.LoadQuarantine([]byte(resp.QuarantineYAML))
	if err != nil || cfg == nil || len(cfg.Tests) != 1 || !cfg.Tests[0].IsActive(time.Now().UTC()) {
		t.Fatalf("invalid rendered quarantineYaml: err=%v cfg=%+v\n%s", err, cfg, resp.QuarantineYAML)
	}
	followUp, err := client.GetTask(context.Background(), resp.FollowUpTaskID)
	if err != nil {
		t.Fatalf("loading follow-up task %s: %v", resp.FollowUpTaskID, err)
	}
	if followUp.State != model.StateProposed || followUp.AutoMerge {
		t.Errorf("follow-up task = (state=%s, autoMerge=%v), want held proposal", followUp.State, followUp.AutoMerge)
	}
	if followUp.Repo != "acme/widgets" {
		t.Errorf("follow-up repo = %q, want acme/widgets", followUp.Repo)
	}
	if !strings.Contains(followUp.Description, "[Task #11](/tasks/11)") {
		t.Errorf("follow-up description missing root-relative link: %q", followUp.Description)
	}

	// Preset and bare legacy alias both authorize, including on the sandbox-cli channel, and fileFollowUp=false skips filing.
	for _, id := range []string{"12", "13"} {
		rec := doWithHeaders(t, srv, http.MethodPost, "/api/tests/quarantine",
			`{"testName":"pkg/bar.TestOther","reason":"contended","fileFollowUp":false}`,
			map[string]string{
				ui.ActingTaskHeader: id,
				ui.ChannelHeader:    ui.SandboxCLIChannel,
				"Content-Type":      "application/json",
			})
		if rec.Code != http.StatusOK {
			t.Fatalf("task %s over sandbox-cli: got %d %s, want 200", id, rec.Code, rec.Body.String())
		}
		var r2 ui.QuarantineTestResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &r2); err != nil {
			t.Fatal(err)
		}
		if r2.FollowUpTaskID != "" {
			t.Errorf("fileFollowUp=false still filed task %q", r2.FollowUpTaskID)
		}
	}

	// Validation: missing fields and invalid TTL return 400.
	for _, bad := range []string{
		`{"reason":"no test"}`,
		`{"testName":"pkg/foo.TestFlaky"}`,
		`{"testName":"pkg/foo.TestFlaky","reason":"bad ttl","ttl":"not-a-duration"}`,
	} {
		if code, _ := asAgent(t, srv, "12", http.MethodPost, "/api/tests/quarantine", bad, ""); code != http.StatusBadRequest {
			t.Errorf("payload %s: got %d, want 400", bad, code)
		}
	}
}
