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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

func TestHubRotateCredentialTokenFileOmitsTokenFromJSONStdout(t *testing.T) {
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

	prevClient := hubHTTPClient
	prevOIDC := hubOIDCToken
	hubHTTPClient = regSrv.Client()
	hubOIDCToken = func(ctx context.Context, hubURL, audience string) (string, error) {
		return "oidc-test-token", nil
	}
	t.Cleanup(func() {
		hubHTTPClient = prevClient
		hubOIDCToken = prevOIDC
	})

	tokPath := filepath.Join(t.TempDir(), "rotated.token")
	stdout := captureStdout(t, func() {
		err := cmdHub(ctx, nil, &printer{json: true}, []string{
			"rotate-credential",
			"-hub-url", regSrv.URL,
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

func TestHubTargetFlagsRejectInsecureURLBeforeOIDCMint(t *testing.T) {
	ctx := context.Background()
	prevOIDC := hubOIDCToken
	oidcCalled := false
	hubOIDCToken = func(ctx context.Context, hubURL, audience string) (string, error) {
		oidcCalled = true
		return "should-not-be-minted", nil
	}
	t.Cleanup(func() {
		hubOIDCToken = prevOIDC
	})

	for _, args := range [][]string{
		{"list", "-hub-url", "http://registry.example.com"},
		{"unregister", "-standalone", "-hub-url", "http://registry.example.com", "workstation-1"},
	} {
		err := cmdHub(ctx, nil, &printer{}, args)
		if err == nil || !strings.Contains(err.Error(), "insecure http://") {
			t.Fatalf("cmdHub(%v) = %v, want insecure http:// refusal", args, err)
		}
		if oidcCalled {
			t.Fatalf("hubOIDCToken was called before validating -hub-url for %v", args)
		}
	}
}

func TestPrintConnectedConfigSnippetQuotesShellMetacharacters(t *testing.T) {
	out := captureStdout(t, func() {
		printConnectedConfigSnippet("https://registry.example.com/$(id)", "bad;rm -rf /'x", "", "")
	})
	if !strings.Contains(out, "'https://registry.example.com/$(id)'") {
		t.Fatalf("snippet did not single-quote URL metacharacters: %s", out)
	}
	if !strings.Contains(out, `'bad;rm -rf /'\''x'`) {
		t.Fatalf("snippet did not single-quote entry metacharacters: %s", out)
	}
}

// TestPrintConnectedConfigSnippetCarriesVerifierFlags pins the 2026-11-07
// walkthrough finding: the printed snippet omitted -ui-hub-issuer,
// -ui-hub-audience and -ui-hub-keys-url (and the token file), so
// pasting it failed with "-hub-connect requires -ui-hub-issuer,
// -ui-hub-audience, and -ui-hub-keys-url".
func TestPrintConnectedConfigSnippetCarriesVerifierFlags(t *testing.T) {
	out := captureStdout(t, func() {
		printConnectedConfigSnippet("https://registry.example.com/", "ws-1", "https://ws-1.grain-user.example", "/etc/grain/registry.token")
	})
	for _, want := range []string{
		"-hub-url https://registry.example.com/",
		"-hub-entry ws-1",
		"-hub-token-file /etc/grain/registry.token",
		"-hub-connect",
		"-ui-require-verifier=registry",
		"-ui-hub-issuer=grain-registry",
		"-ui-hub-audience=https://ws-1.grain-user.example",
		"-ui-hub-keys-url=https://registry.example.com/.well-known/jwks.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q:\n%s", want, out)
		}
	}
}

func TestHubRegisterPreservesPrivateEntryMetadataAndSurfacesScopeConflicts(t *testing.T) {
	ctx := context.Background()
	var (
		mu             sync.Mutex
		patchCount     int
		setScopesCount int
		entry          = api.Entry{
			Name:        "private-ws",
			Kind:        api.KindConnected,
			HostKind:    api.HostKindKubernetes,
			DisplayName: "Private Sec Cluster",
			Purpose:     "Confidential audit workloads",
			Owner:       "alice@example.com",
			OwnerGroup:  "group:sec-team@example.com",
			Listed:      false,
			Tags:        []string{"sec", "private"},
		}
	)
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/entries/private-ws":
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/entries/private-ws":
			patchCount++
			var req api.UpdateEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Listed != nil {
				entry.Listed = *req.Listed
			}
			if req.OwnerGroup != nil {
				entry.OwnerGroup = *req.OwnerGroup
			}
			if req.DisplayName != nil {
				entry.DisplayName = *req.DisplayName
			}
			if req.Purpose != nil {
				entry.Purpose = *req.Purpose
			}
			if req.HostKind != nil {
				entry.HostKind = *req.HostKind
			}
			if req.Tags != nil {
				entry.Tags = slices.Clone(*req.Tags)
			}
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == http.MethodPut && r.URL.Path == "/api/entries/private-ws/scopes":
			setScopesCount++
			var req api.SetScopesRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			for _, sc := range req.Scopes {
				if sc.Repo == "conflict/repo" {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": "primary scope conflict",
						"code":  "primary_scope_conflict",
					})
					return
				}
			}
			_ = json.NewEncoder(w).Encode(api.SetScopesResponse{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/private-ws/credentials/rotate":
			_ = json.NewEncoder(w).Encode(api.RotateCredentialResponse{
				RegistrationToken: "grc_private_ws_token",
				Credential:        api.CredentialMeta{CredentialID: "cred-priv-1"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/entries/private-ws/heartbeat":
			_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{Status: api.StatusOnline, NextIntervalSeconds: 30})
		default:
			http.NotFound(w, r)
		}
	}))
	defer regSrv.Close()

	prevClient := hubHTTPClient
	prevOIDC := hubOIDCToken
	hubHTTPClient = regSrv.Client()
	hubOIDCToken = func(ctx context.Context, hubURL, audience string) (string, error) {
		return "oidc-creator-jwt", nil
	}
	t.Cleanup(func() {
		hubHTTPClient = prevClient
		hubOIDCToken = prevOIDC
	})

	// 1. Standalone register on an existing unlisted entry without metadata flags must NOT flip
	// Listed to true or clear OwnerGroup / DisplayName / Purpose / Tags.
	tokPath := filepath.Join(t.TempDir(), "priv.token")
	_ = captureStdout(t, func() {
		err := cmdHub(ctx, nil, &printer{json: true}, []string{
			"register",
			"-standalone",
			"-rotate-token",
			"-hub-url", regSrv.URL,
			"-token-file", tokPath,
			"private-ws",
		})
		if err != nil {
			t.Fatalf("standalone register: %v", err)
		}
	})
	mu.Lock()
	if patchCount != 0 {
		t.Fatalf("standalone register without metadata flags sent %d PATCH requests, want 0", patchCount)
	}
	if entry.Listed || entry.OwnerGroup != "group:sec-team@example.com" || entry.DisplayName != "Private Sec Cluster" {
		t.Fatalf("existing private entry metadata clobbered: %+v", entry)
	}
	mu.Unlock()

	// 2. Explicit conflicting -scope on an existing entry must surface the 409 error instead of exiting 0.
	err := cmdHub(ctx, nil, &printer{json: true}, []string{
		"register",
		"-standalone",
		"-hub-url", regSrv.URL,
		"-scope", "conflict/repo::primary",
		"private-ws",
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("register with conflicting -scope = %v, want primary scope conflict error", err)
	}

	// 3. Attaching a fresh daemon with target repos (but no -scope or -listed flags) to an existing
	// unlisted entry must preserve Listed=false and OwnerGroup in daemon settings and must NOT promote
	// target repos to declared scopes via PUT /scopes.
	tmp := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(tmp, "data")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	secStore := secrets.Open(secrets.Config{
		File:    filepath.Join(tmp, "secrets.age"),
		KeyFile: filepath.Join(tmp, "secrets.key"),
	})
	mgr := daemon.NewHubManager(daemon.HubManagerOptions{
		Store:   store,
		Secrets: secStore,
		LiveConfig: func() daemon.Config {
			mc, _ := store.GetConfig(ctx)
			if mc == nil {
				return daemon.Config{HostSandboxes: true}
			}
			return daemon.Config{HostSandboxes: true, Hub: mc.Hub}
		},
		Health:         func() (api.HealthState, string) { return api.HealthOK, "" },
		Version:        func() version.Info { return version.Info{Release: "v1.4.0", Revision: "abc1234"} },
		HTTPClient:     regSrv.Client(),
		JitterFraction: -1,
	})
	daemonSrv := httptest.NewServer(ui.NewServer(withAccessCode(ui.Config{
		Actor:               ui.DefaultActor("operator"),
		Capabilities:        ui.OfferedCapabilities(),
		Secrets:             secStore,
		HubStatus:           mgr.Snapshot,
		TriggerHubHeartbeat: mgr.TriggerHeartbeat,
		TriggerHubSync:      mgr.Trigger,
	}), store))
	defer daemonSrv.Close()

	c := accessCodeClient(daemonSrv.URL)
	if _, err := c.UpdateSettings(ctx, *settingsRequest("30s", 2, "github.example")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/internal-sec"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	setScopesBefore := setScopesCount
	mu.Unlock()

	_ = captureStdout(t, func() {
		if err := cmdHub(ctx, c, &printer{}, []string{"register", "-hub-url", regSrv.URL, "private-ws"}); err != nil {
			t.Fatalf("daemon attach register: %v", err)
		}
	})

	mu.Lock()
	if setScopesCount != setScopesBefore {
		t.Fatalf("daemon register without -scope called PUT /scopes %d times, want 0", setScopesCount-setScopesBefore)
	}
	if entry.Listed {
		t.Fatalf("daemon attach flipped private entry Listed to true: %+v", entry)
	}
	mu.Unlock()

	storedCfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if storedCfg.Hub.Listed {
		t.Fatalf("stored daemon Registry.Listed = true, want false inherited from remote private entry")
	}
	if storedCfg.Hub.OwnerGroup != "group:sec-team@example.com" {
		t.Fatalf("stored daemon Registry.OwnerGroup = %q, want group:sec-team@example.com", storedCfg.Hub.OwnerGroup)
	}
}
