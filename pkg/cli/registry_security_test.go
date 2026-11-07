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

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/registry/api"
)

func TestRegistryRotateCredentialTokenFileOmitsTokenFromJSONStdout(t *testing.T) {
	ctx := context.Background()
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/entries/workstation-1/credentials/rotate" {
			_ = json.NewEncoder(w).Encode(api.RotateCredentialResponse{
				RegistrationToken: "grc_super_secret_rotated_value",
				Credential: api.CredentialMeta{
					CredentialID: "cred-rot-1",
					Label:        "ci",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer regSrv.Close()

	prevClient := registryHTTPClient
	prevOIDC := registryOIDCToken
	registryHTTPClient = regSrv.Client()
	registryOIDCToken = func(ctx context.Context, registryURL, audience string) (string, error) {
		return "oidc-test-token", nil
	}
	t.Cleanup(func() {
		registryHTTPClient = prevClient
		registryOIDCToken = prevOIDC
	})

	tokPath := filepath.Join(t.TempDir(), "rotated.token")
	stdout := captureStdout(t, func() {
		err := cmdRegistry(ctx, nil, &printer{json: true}, []string{
			"rotate-credential",
			"-registry-url", regSrv.URL,
			"-token-file", tokPath,
			"workstation-1",
		})
		if err != nil {
			t.Fatalf("rotate-credential: %v", err)
		}
	})
	if strings.Contains(stdout, "grc_super_secret_rotated_value") {
		t.Fatalf("rotate-credential -token-file --json leaked secret token to stdout: %s", stdout)
	}
	info, err := os.Stat(tokPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", tokPath, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token file mode = %o, want 0600", mode)
	}
	fileBytes, err := os.ReadFile(tokPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", tokPath, err)
	}
	if strings.TrimSpace(string(fileBytes)) != "grc_super_secret_rotated_value" {
		t.Fatalf("token file content = %q, want grc_super_secret_rotated_value", string(fileBytes))
	}
}

func TestRegistryTargetFlagsRejectInsecureURLBeforeOIDCMint(t *testing.T) {
	ctx := context.Background()
	prevOIDC := registryOIDCToken
	oidcCalled := false
	registryOIDCToken = func(ctx context.Context, registryURL, audience string) (string, error) {
		oidcCalled = true
		return "should-not-be-minted", nil
	}
	t.Cleanup(func() {
		registryOIDCToken = prevOIDC
	})

	err := cmdRegistry(ctx, nil, &printer{}, []string{
		"list",
		"-registry-url", "http://registry.example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "insecure http://") {
		t.Fatalf("cmdRegistry(list -registry-url http://...) = %v, want insecure http:// refusal", err)
	}
	if oidcCalled {
		t.Fatalf("registryOIDCToken was called before validating -registry-url")
	}
}
