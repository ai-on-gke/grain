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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// `grain capability gcp -test-endpoints` with GRAIN_GCP_TEST_ENDPOINTS
// naming gcpfake's HTTP stub is the real gcp server -- the Google
// clients, the admin key's token fetch -- against no GCP: a managed
// token set's account is created and bound on its Artifact Registry
// scope, a grant exchanges for a token minted through the stub's IAM
// Credentials, and delete_orphans unbinds and deletes it again.
func TestGCPTestEndpointsRunManagedTokenSetsAgainstTheHTTPStub(t *testing.T) {
	cloud := gcpfake.New()
	cloud.RequireTokenCreator = true
	stub := gcpfake.NewServer(cloud)
	ts := httptest.NewServer(stub)
	defer ts.Close()
	env := func(k string) string {
		if k == gcpsets.TestEndpointsEnv {
			return gcpfake.Endpoints(ts.URL).String()
		}
		return ""
	}
	s, err := builders["gcp"].Build(context.Background(), []string{"-test-endpoints", "-listen", "127.0.0.1:0"}, env, nil)
	if err != nil {
		t.Fatal(err)
	}

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	adminEmail := "walk-admin@grain-test-proj.iam.gserviceaccount.com"
	key, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": adminEmail, "private_key_id": "k1",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	creds, _ := json.Marshal(map[string]json.RawMessage{"walk": key})
	config := func(sets []any, extra map[string]any) map[string]any {
		c := map[string]any{
			protocol.DeploymentIDConfig: "049f1e09",
			protocol.InstanceIDConfig:   "049f1e09c0ffee0123456789abcdef01",
			"admins": []any{map[string]any{
				"name": "walk", "credential_source": "configured", "projects": []any{"grain-test-proj"},
				"role_ceiling": []any{"roles/artifactregistry.writer", "roles/logging.viewer"},
			}},
			"admin_credentials": "$secret:walk-admin",
			"permission_sets":   sets,
		}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	arSet := map[string]any{
		"name": "ar-writer", "admin": "walk", "project": "grain-test-proj", "credential": "token",
		"roles": []any{"roles/artifactregistry.writer"}, "resource_scope": "repositories/us-central1/images",
	}
	ps, err := attachConfigured(t, s, config([]any{arSet}, nil), map[string]string{"walk-admin": string(creds)})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	wantSA := "grain-049f1e09-ar-writer@grain-test-proj.iam.gserviceaccount.com"
	arRes := "projects/grain-test-proj/locations/us-central1/repositories/images"
	if got := cloud.Members(arRes, "roles/artifactregistry.writer"); len(got) != 1 || got[0] != "serviceAccount:"+wantSA {
		t.Fatalf("AR bindings = %v", got)
	}
	if got := cloud.Members("projects/grain-test-proj/serviceAccounts/"+wantSA, "roles/iam.serviceAccountTokenCreator"); len(got) != 1 || got[0] != "serviceAccount:"+adminEmail {
		t.Fatalf("tokenCreator = %v", got)
	}

	resp := mint(t, ps, "gcp-key", "e1-1-0", map[string]any{"permission_set": "ar-writer"})
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
	if code, body := exchange(t, cfg.TokenURL, subject); code != http.StatusOK || !strings.HasSuffix(body["access_token"].(string), wantSA) {
		t.Fatalf("exchange = %d %v", code, body)
	}
	if !strings.Contains(strings.Join(stub.Requests(), "\n"), "POST /iamcredentials/v1/projects/-/serviceAccounts/"+wantSA+":generateAccessToken") {
		t.Fatalf("the token was not minted through the stub: %v", stub.Requests())
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e1-1-0"); err != nil {
		t.Fatal(err)
	}

	other := map[string]any{"name": "log-reader", "admin": "walk", "project": "grain-test-proj", "credential": "token", "roles": []any{"roles/logging.viewer"}}
	// Configure sends config as given, with no secret resolved.
	reconfig := config([]any{other}, map[string]any{"delete_orphans": []any{"ar-writer"}})
	reconfig["admin_credentials"] = string(creds)
	if err := ps.Configure(context.Background(), reconfig); err != nil {
		t.Fatalf("reconfigure with delete_orphans: %v", err)
	}
	if got := cloud.Members(arRes, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Errorf("AR binding survived delete_orphans: %v", got)
	}
	if _, ok := cloud.Accounts["grain-test-proj"][wantSA]; ok {
		t.Errorf("account %s survived delete_orphans", wantSA)
	}
}

// The endpoints variable on its own is refused, not obeyed or ignored;
// the flag without it, or beside -fake-cloud, and an off-machine stub
// are refused too.
func TestGCPTestEndpointsAreRefusedWithoutTheFlag(t *testing.T) {
	spec := gcpfake.Endpoints("http://127.0.0.1:1").String()
	envOf := func(v string) Env {
		return func(k string) string {
			if k == gcpsets.TestEndpointsEnv {
				return v
			}
			return ""
		}
	}
	for name, tc := range map[string]struct {
		args []string
		env  string
		want string
	}{
		"env without the flag": {nil, spec, "was not started with -test-endpoints"},
		"flag without the env": {[]string{"-test-endpoints"}, "", "needs " + gcpsets.TestEndpointsEnv},
		"beside -fake-cloud":   {[]string{"-test-endpoints", "-fake-cloud", "x.json"}, spec, "give one"},
		"off-machine stub":     {[]string{"-test-endpoints"}, strings.Replace(spec, "127.0.0.1", "192.0.2.1", 1), "not on a loopback address"},
	} {
		args := append(tc.args, "-listen", "127.0.0.1:0")
		s, err := builders["gcp"].Build(context.Background(), args, envOf(tc.env), nil)
		if s != nil {
			s.Close(context.Background())
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Build = %v, want %q", name, err, tc.want)
		}
	}
}

// Neither -test-endpoints nor -fake-cloud stands in for apikeys.googleapis.com,
// so gemini_key refuses rather than reaching Google behind a flag that promised
// nothing reaches Google.
func TestGCPTestEndpointsAndFakeCloudRefuseGeminiKey(t *testing.T) {
	ts := httptest.NewServer(gcpfake.NewServer(gcpfake.New()))
	defer ts.Close()
	spec := gcpfake.Endpoints(ts.URL).String()
	fixture := filepath.Join(t.TempDir(), "gcpfake.json")
	if err := os.WriteFile(fixture, []byte(`{"default_identity":"walk@grain-test-proj.iam.gserviceaccount.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	key, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "walk@grain-test-proj.iam.gserviceaccount.com", "private_key_id": "k1",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	creds, _ := json.Marshal(map[string]json.RawMessage{"walk": key})
	cfg := map[string]any{
		"admins":            []any{map[string]any{"name": "walk", "credential_source": "configured", "projects": []any{"grain-test-proj"}}},
		"admin_credentials": "$secret:walk-admin",
		"gemini_key":        map[string]any{"project": "grain-test-proj", "admin": "walk"},
	}

	for name, tc := range map[string]struct {
		args []string
		env  Env
	}{
		"-test-endpoints": {[]string{"-test-endpoints", "-listen", "127.0.0.1:0"}, func(k string) string {
			if k == gcpsets.TestEndpointsEnv {
				return spec
			}
			return ""
		}},
		"-fake-cloud": {[]string{"-fake-cloud", fixture, "-listen", "127.0.0.1:0"}, func(string) string { return "" }},
	} {
		s, err := builders["gcp"].Build(context.Background(), tc.args, tc.env, nil)
		if err != nil {
			t.Fatalf("%s: Build: %v", name, err)
		}
		t.Cleanup(func() { s.Close(context.Background()) })
		ps, err := attachConfigured(t, s, cfg, map[string]string{"walk-admin": string(creds)})
		if err != nil {
			t.Fatalf("%s: attach: %v", name, err)
		}
		_, err = ps.Mint(context.Background(), GeminiKeyMethod, "g1-1-0", time.Now().Add(time.Hour), nil)
		if err == nil || !strings.Contains(err.Error(), "stands in for permission sets only") {
			t.Errorf("%s: Mint(gemini-key) = %v, want refused before reaching Google", name, err)
		}
	}
}
