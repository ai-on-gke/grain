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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Replacing the deployment's git connection -- rotating the token it
// pushes with -- keeps the merge credential the connection named: a
// silent drop would put every later merge back under the pushing
// person's login, which is what the merge credential is there to stop
// (task 4386).
func TestReplacingAGitConnectionKeepsItsMergeCredential(t *testing.T) {
	r := newLifecycleRig(t)
	if err := r.store.UpdateConfig(r.ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{{Credential: "old-push", MergeCredential: "grain-merge"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.creds.SetToken("old-push", "old-token"); err != nil {
		t.Fatal(err)
	}
	code, out := r.call(http.MethodPost, "/api/deployment/git-connections",
		`{"type":"pat","host":"github.com","kind":"github","value":"ghp_rotated","replace":true}`)
	if code != http.StatusOK {
		t.Fatalf("replace: %d %s", code, out)
	}
	settings, err := r.store.GetConfig(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.GitConnections) != 1 {
		t.Fatalf("connections after replace = %+v, want one", settings.GitConnections)
	}
	got := settings.GitConnections[0]
	if got.Credential == "old-push" {
		t.Fatalf("connection still pushes with the replaced credential: %+v", got)
	}
	if got.MergeCredential != "grain-merge" {
		t.Errorf("merge credential after replace = %q, want grain-merge kept", got.MergeCredential)
	}
}

// GET /api/deployment/git-connections says which credential each
// connection merges with. The 2026-11-13 merge-and-reviews walkthrough
// (docs/test-runs/2026-11-13-merge-and-reviews.md, F1) found the route
// dropped it: a merge credential `grain connections import` recorded was
// in the store and used for every merge, and nothing an operator could
// read said so.
func TestGitConnectionsListSaysWhichCredentialMerges(t *testing.T) {
	r := newLifecycleRig(t)
	if err := r.store.UpdateConfig(r.ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "push-bot", MergeCredential: "grain-merge"},
			{Credential: "other", Scope: "acme/other"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, out := r.call(http.MethodGet, "/api/deployment/git-connections", "")
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, out)
	}
	var got []struct {
		Credential      string `json:"credential"`
		MergeCredential string `json:"mergeCredential"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding %s: %v", out, err)
	}
	merges := map[string]string{}
	for _, c := range got {
		merges[c.Credential] = c.MergeCredential
	}
	if merges["push-bot"] != "grain-merge" {
		t.Errorf("push-bot's mergeCredential = %q, want grain-merge (%s)", merges["push-bot"], out)
	}
	if v, ok := merges["other"]; !ok || v != "" {
		t.Errorf("other's mergeCredential = %q (listed %v), want empty", v, ok)
	}
}
