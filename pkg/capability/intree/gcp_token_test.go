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
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const testOrgSA = "org-runner@grain-test-proj.iam.gserviceaccount.com"

// existingGCP is a gcp server configured with a pool set "reader" and a
// set "org" over the existing account testOrgSA, with org's extra fields.
func existingGCP(t *testing.T, org map[string]any) (*protocol.Server, *gcpfake.Cloud) {
	t.Helper()
	cloud := gcpfake.New()
	cloud.DefaultIdentity = "minter@grain-test-proj.iam.gserviceaccount.com"
	cloud.AddAccount("grain-test-proj", testOrgSA, "made by hand")
	s, err := newConfigurableGCP(GCPConfig{Connector: cloud, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]any{"name": "org", "admin": "minter", "service_account": testOrgSA, "roles": []any{"roles/owner"}}
	for k, v := range org {
		set[k] = v
	}
	ps, err := attachConfigured(t, s, map[string]any{
		protocol.DeploymentIDConfig: "049f1e09",
		protocol.InstanceIDConfig:   "049f1e09c0ffee0123456789abcdef01",
		"admins": []any{
			map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/storage.objectViewer"}},
		},
		"admin_credentials": "$secret:gcp-admins",
		"permission_sets": []any{
			map[string]any{"name": "reader", "admin": "minter", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}},
			set,
		},
	}, map[string]string{"gcp-admins": `{"minter": ` + testMinterKey + `}`})
	if err != nil {
		t.Fatal(err)
	}
	return ps, cloud
}

// exchange posts subject to the placed config's token_url, as a client
// library does.
func exchange(t *testing.T, tokenURL, subject string) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(tokenURL, url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":      {subject},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func TestGCPTokenModeGrantPlacesAnExternalAccountAndNoKey(t *testing.T) {
	ps, cloud := existingGCP(t, nil)
	desc := ps.Methods["gcp-key"].Description
	if !strings.Contains(desc, "set org: existing service account "+testOrgSA) || !strings.Contains(desc, "admin minter can impersonate it") {
		t.Errorf("Test report:\n%s", desc)
	}
	resp := mint(t, ps, "gcp-key", "e7-1-0", map[string]any{"permission_set": "org"})
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if cloud.LiveKeys() != 0 || cloud.Calls["CreateKey"] != 0 {
		t.Fatal("a token-mode grant created a key")
	}
	if len(resp.Ports) != 1 || resp.Ports[0].HostPort == 0 {
		t.Fatalf("ports = %+v", resp.Ports)
	}
	var cfg struct {
		Type             string            `json:"type"`
		TokenURL         string            `json:"token_url"`
		CredentialSource map[string]string `json:"credential_source"`
	}
	if err := json.Unmarshal([]byte(placement(t, resp, GCPKeyPath).Content), &cfg); err != nil {
		t.Fatal(err)
	}
	wantURL := "http://127.0.0.1:" + strconv.Itoa(resp.Ports[0].HostPort) + "/runs/e7-1-0/token"
	if cfg.Type != "external_account" || cfg.TokenURL != wantURL {
		t.Fatalf("config = %+v, want an external_account exchanging at %s", cfg, wantURL)
	}
	subjectPath := cfg.CredentialSource["file"]
	subject := placement(t, resp, subjectPath)
	if subject.Mode != "0600" || subject.Content == "" {
		t.Fatalf("subject token placement = %+v", subject)
	}
	if about := placement(t, resp, gcpAboutPath(GCPKeyPath)).Content; !strings.Contains(about, "service_account: "+testOrgSA) || !strings.Contains(about, "no key exists") {
		t.Errorf("about = %s", about)
	}
	if !strings.Contains(placement(t, resp, resp.Setup).Content, "gcloud auth login --cred-file=") {
		t.Errorf("setup does not sign gcloud in with the config")
	}

	// A client library reading the placed config, as ADC would, gets the
	// impersonated account's token from the forwarded listener.
	dir := t.TempDir()
	local := filepath.Join(dir, "subject")
	os.WriteFile(local, []byte(subject.Content), 0o600)
	cfgJSON := strings.Replace(placement(t, resp, GCPKeyPath).Content, subjectPath, local, 1)
	creds, err := google.CredentialsFromJSON(context.Background(), []byte(cfgJSON), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		t.Fatalf("the client library's token exchange: %v", err)
	}
	if !strings.HasSuffix(tok.AccessToken, testOrgSA) || time.Until(tok.Expiry) > time.Hour+time.Minute {
		t.Fatalf("token = %+v", tok)
	}
	// Each refresh is another impersonated token, never anything kept in
	// the sandbox but the per-attempt token.
	code, body := exchange(t, cfg.TokenURL, subject.Content)
	if code != http.StatusOK || body["access_token"] == tok.AccessToken || body["token_type"] != "Bearer" {
		t.Fatalf("refresh = %d %v", code, body)
	}
	if code, _ := exchange(t, cfg.TokenURL, "not-the-token"); code != http.StatusUnauthorized {
		t.Fatalf("a wrong subject token got %d", code)
	}
	otherURL := strings.Replace(cfg.TokenURL, "e7-1-0", "e7-9-0", 1)
	if code, _ := exchange(t, otherURL, subject.Content); code != http.StatusUnauthorized {
		t.Fatalf("another attempt's path got %d", code)
	}

	// The ID-token helper's endpoint.
	idURL := strings.Replace(cfg.TokenURL, "/token", "/id-token", 1) + "?audience=https%3A%2F%2Fsvc.example.com"
	req, _ := http.NewRequest(http.MethodGet, idURL, nil)
	req.Header.Set("Authorization", "Bearer "+subject.Content)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	idTok, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || string(idTok) != "id-https://svc.example.com-"+testOrgSA {
		t.Fatalf("id-token = %d %q", r.StatusCode, idTok)
	}

	// A renewal keeps the placed per-attempt token good.
	if renewed := mint(t, ps, "gcp-key", "e7-1-0", map[string]any{"permission_set": "org"}); placement(t, renewed, subjectPath).Content != subject.Content {
		t.Fatal("a renewal changed the per-attempt token the sandbox already holds")
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e7-1-0"); err != nil {
		t.Fatal(err)
	}
	if code, _ := exchange(t, cfg.TokenURL, subject.Content); code != http.StatusUnauthorized {
		t.Fatalf("after release the exchange got %d", code)
	}
	for _, call := range []string{"CreateServiceAccount", "SetPolicy", "DeleteServiceAccount", "DeleteKey"} {
		if cloud.Calls[call] != 0 {
			t.Errorf("%s called for a set over an existing account", call)
		}
	}
}

func TestGCPKeyModeGrantOverAnExistingAccount(t *testing.T) {
	ps, cloud := existingGCP(t, map[string]any{"credential": "key"})
	resp := mint(t, ps, "gcp-key", "e8-1-0", map[string]any{"permission_set": "org"})
	if resp.Status != protocol.StatusOK || len(resp.Ports) != 0 {
		t.Fatalf("mint = %+v", resp)
	}
	if !strings.Contains(placement(t, resp, GCPKeyPath).Content, testOrgSA) || cloud.KeyCount(testOrgSA) != 1 {
		t.Fatal("no key for the existing account")
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e8-1-0"); err != nil {
		t.Fatal(err)
	}
	if cloud.KeyCount(testOrgSA) != 0 {
		t.Fatal("release left the key")
	}
	for _, call := range []string{"CreateServiceAccount", "SetPolicy", "DeleteServiceAccount"} {
		if cloud.Calls[call] != 0 {
			t.Errorf("%s called for a set over an existing account", call)
		}
	}
}

// A set over an existing account is a person's to grant: an agent that
// holds it cannot pass it on, while the pool set stays covered as before.
func TestGCPExistingAccountSetsArePersonGranted(t *testing.T) {
	ps, _ := existingGCP(t, nil)
	ref := protocol.Ref{CapabilityID: ps.ID, Method: "gcp-key"}
	org := protocol.Grant{Ref: ref, Args: validated(t, ps, "gcp-key", map[string]any{"permission_set": "org"})}
	reader := protocol.Grant{Ref: ref, Args: validated(t, ps, "gcp-key", map[string]any{"permission_set": "reader"})}
	if protocol.GrantKindOf(org) != protocol.GrantByPerson {
		t.Error("the existing-account set is not person-granted")
	}
	if protocol.Covers([]protocol.Grant{org}, org) {
		t.Error("an agent holding the existing-account set could pass it on")
	}
	if protocol.GrantKindOf(reader) != protocol.GrantCovered || !protocol.Covers([]protocol.Grant{reader}, reader) {
		t.Error("the pool set is no longer covered by its own grant")
	}

	// person_granted: false opts out.
	ps2, _ := existingGCP(t, map[string]any{"person_granted": false})
	org2 := protocol.Grant{Ref: protocol.Ref{CapabilityID: ps2.ID, Method: "gcp-key"}, Args: validated(t, ps2, "gcp-key", map[string]any{"permission_set": "org"})}
	if protocol.GrantKindOf(org2) != protocol.GrantCovered {
		t.Error("person_granted: false still reserves the set")
	}
}
