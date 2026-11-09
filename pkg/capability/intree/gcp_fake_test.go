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

package intree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// `grain capability gcp -fake-cloud <fixture>` is the gcp server a
// walkthrough runs under a real daemon with no GCP: every admin connects
// to the fixture's in-memory cloud, and its clock_file moves the server
// past a grant's deadline. A token-mode grant then exchanges, refreshes
// until the deadline and not after, and stops at release, as on GCP.
func TestGCPFakeCloudFlagServesTokenModeWithAMovableClock(t *testing.T) {
	dir := t.TempDir()
	clock := filepath.Join(dir, "clock")
	fixture, _ := json.Marshal(map[string]any{
		"default_identity": "walk-admin@walk-proj.iam.gserviceaccount.com",
		"accounts":         []any{map[string]any{"project": "walk-proj", "email": "deployer@walk-proj.iam.gserviceaccount.com"}},
		"clock_file":       clock,
	})
	path := filepath.Join(dir, "gcpfake.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := builders["gcp"].Build(context.Background(), []string{"-fake-cloud", path, "-listen", "127.0.0.1:0"}, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	ps, err := attachConfigured(t, s, map[string]any{"permission_sets": []any{
		map[string]any{"name": "ext-deploy", "service_account": "deployer@walk-proj.iam.gserviceaccount.com"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if desc := ps.Methods["gcp-key"].Description; !strings.Contains(desc, "admin default (ambient) is walk-admin@walk-proj.iam.gserviceaccount.com") {
		t.Errorf("Test report does not name the fixture's admin:\n%s", desc)
	}
	resp := mint(t, ps, "gcp-key", "w1-1-0", map[string]any{"permission_set": "ext-deploy"})
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	var cfg struct {
		TokenURL         string            `json:"token_url"`
		CredentialSource map[string]string `json:"credential_source"`
	}
	if err := json.Unmarshal([]byte(placement(t, resp, GCPKeyPath).Content), &cfg); err != nil {
		t.Fatal(err)
	}
	subject := placement(t, resp, cfg.CredentialSource["file"]).Content
	if code, body := exchange(t, cfg.TokenURL, subject); code != 200 || !strings.HasSuffix(body["access_token"].(string), "deployer@walk-proj.iam.gserviceaccount.com") {
		t.Fatalf("exchange = %d %v", code, body)
	}
	// mint's deadline is an hour away: 59m on, a token lives a minute.
	os.WriteFile(clock, []byte("59m"), 0o600)
	if code, body := exchange(t, cfg.TokenURL, subject); code != 200 || body["expires_in"].(float64) > 61 {
		t.Fatalf("exchange a minute before the deadline = %d %v", code, body)
	}
	os.WriteFile(clock, []byte("61m"), 0o600)
	if code, body := exchange(t, cfg.TokenURL, subject); code != 401 || !strings.Contains(body["error_description"].(string), "deadline") {
		t.Fatalf("exchange past the deadline = %d %v, want 401 naming the deadline", code, body)
	}
	os.Remove(clock)
	if _, err := ps.Release(context.Background(), "gcp-key", "w1-1-0"); err != nil {
		t.Fatal(err)
	}
	if code, body := exchange(t, cfg.TokenURL, subject); code != 401 || !strings.Contains(body["error_description"].(string), "released") {
		t.Fatalf("exchange after release = %d %v, want 401", code, body)
	}
}

// A fixture the server cannot read stops it starting, rather than
// leaving a gcp server that would fall through to Google.
func TestGCPFakeCloudFlagRefusesAFixtureItCannotRead(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"default_identity": "a@b", "acounts": []}`), 0o600)
	for _, path := range []string{bad, filepath.Join(dir, "missing.json")} {
		if _, err := builders["gcp"].Build(context.Background(), []string{"-fake-cloud", path, "-listen", "127.0.0.1:0"}, func(string) string { return "" }, nil); err == nil || !strings.Contains(err.Error(), "-fake-cloud") {
			t.Errorf("%s: Build = %v, want a -fake-cloud error", filepath.Base(path), err)
		}
	}
}
