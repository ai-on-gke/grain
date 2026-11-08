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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

func TestRegistryCLIRegisterStatusUnregister(t *testing.T) {
	ctx := context.Background()
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

	// Fake grain registry server tracking entries, minted tokens, and heartbeats.
	var (
		mu             sync.Mutex
		entries        = map[string]api.Entry{}
		activeTokens   = map[string]string{}
		lastHeartbeat  api.HeartbeatRequest
		heartbeatCount int
		rotateCount    int
	)
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			e, ok := entries[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries":
			if auth != "oidc-creator-jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "creator token required"})
				return
			}
			var req api.CreateEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			listed := true
			if req.Listed != nil {
				listed = *req.Listed
			}
			e := api.Entry{
				Name:        req.Name,
				Kind:        req.Kind,
				HostKind:    req.HostKind,
				DisplayName: req.DisplayName,
				Purpose:     req.Purpose,
				Listed:      listed,
				Status:      api.StatusOffline,
			}
			entries[req.Name] = e
			tok := "grc_minted_initial_token"
			activeTokens[req.Name] = tok
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(api.CreateEntryResponse{Entry: e, RegistrationToken: tok})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			e := entries[name]
			var req api.UpdateEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.DisplayName != nil {
				e.DisplayName = *req.DisplayName
			}
			if req.Purpose != nil {
				e.Purpose = *req.Purpose
			}
			if req.HostKind != nil {
				e.HostKind = *req.HostKind
			}
			if req.Listed != nil {
				e.Listed = *req.Listed
			}
			entries[name] = e
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/credentials/rotate"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/credentials/rotate")
			rotateCount++
			tok := "grc_rotated_token"
			activeTokens[name] = tok
			_ = json.NewEncoder(w).Encode(api.RotateCredentialResponse{RegistrationToken: tok})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/heartbeat")
			if want := activeTokens[name]; want != "" && auth != want {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid registration credential"})
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&lastHeartbeat)
			heartbeatCount++
			_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{
				Status:              api.StatusOnline,
				NextIntervalSeconds: 15,
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			if auth != "oidc-creator-jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "creator token required"})
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			delete(entries, name)
			delete(activeTokens, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer regSrv.Close()

	prevHTTP := registryHTTPClient
	prevOIDC := registryOIDCToken
	registryHTTPClient = regSrv.Client()
	registryOIDCToken = func(_ context.Context, _, _ string) (string, error) {
		return "oidc-creator-jwt", nil
	}
	defer func() {
		registryHTTPClient = prevHTTP
		registryOIDCToken = prevOIDC
	}()

	mgr := daemon.NewRegistryManager(daemon.RegistryManagerOptions{
		Store:   store,
		Secrets: secStore,
		LiveConfig: func() daemon.Config {
			mc, _ := store.GetConfig(ctx)
			if mc == nil {
				return daemon.Config{HostSandboxes: true}
			}
			return daemon.Config{HostSandboxes: true, Registry: mc.Registry}
		},
		Health:         func() (api.HealthState, string) { return api.HealthOK, "" },
		Version:        func() version.Info { return version.Info{Release: "v1.4.0", Revision: "abc1234"} },
		HTTPClient:     regSrv.Client(),
		JitterFraction: -1,
	})

	uiCfg := withAccessCode(ui.Config{
		Actor:                    ui.DefaultActor("operator"),
		Capabilities:             ui.OfferedCapabilities(),
		Secrets:                  secStore,
		RegistryStatus:           mgr.Snapshot,
		TriggerRegistryHeartbeat: mgr.TriggerHeartbeat,
		TriggerRegistrySync:      mgr.Trigger,
	})
	daemonSrv := httptest.NewServer(ui.NewServer(uiCfg, store))
	defer daemonSrv.Close()

	c := accessCodeClient(daemonSrv.URL)
	if _, err := c.UpdateSettings(ctx, *settingsRequest("30s", 2, "github.example")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatal(err)
	}

	// 1. Initial status is disabled.
	outText := captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{}, []string{"status"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(outText, "Registry: disabled") {
		t.Fatalf("initial status = %q, want disabled", outText)
	}

	// 2. Register creates the connected entry, stores grc_minted_initial_token in secrets,
	// updates Settings, and runs an immediate heartbeat.
	regText := captureStdout(t, func() {
		err := cmdRegistry(ctx, c, &printer{}, []string{
			"register",
			"-registry-url", regSrv.URL,
			"-display-name", "Widgets Grain",
			"-purpose", "Widgets CI and tasks",
			"widgets-dev",
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(regText, "State:       connected") || !strings.Contains(regText, "acme/widgets") {
		t.Fatalf("register output = %q, want connected state and acme/widgets scope", regText)
	}

	storedTok, err := secStore.Resolve(ctx, secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey)
	if err != nil || storedTok != "grc_minted_initial_token" {
		t.Fatalf("stored secret = %q, %v; want grc_minted_initial_token", storedTok, err)
	}

	mu.Lock()
	hbCount := heartbeatCount
	hb := lastHeartbeat
	mu.Unlock()
	if hbCount != 1 || hb.GrainVersion != "v1.4.0" || hb.GrainCommit != "abc1234" || len(hb.Scopes) != 1 || hb.Scopes[0].Repo != "acme/widgets" {
		t.Fatalf("heartbeat (%d) = %+v", hbCount, hb)
	}

	// 3. Status JSON reports connected snapshot.
	statusJSON := captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{json: true}, nil); err != nil {
			t.Fatal(err)
		}
	})
	var snap model.RegistrySnapshot
	if err := json.Unmarshal([]byte(statusJSON), &snap); err != nil {
		t.Fatalf("unmarshal status json: %v (%s)", err, statusJSON)
	}
	if !snap.Enabled || snap.State != model.RegistryStateConnected || snap.Config.Entry != "widgets-dev" {
		t.Fatalf("status snapshot = %+v", snap)
	}

	// 4. Register with -rotate-token rotates credential and heartbeats with the new token.
	_ = captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{}, []string{"register", "-rotate-token"}); err != nil {
			t.Fatal(err)
		}
	})
	rotatedTok, err := secStore.Resolve(ctx, secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey)
	if err != nil || rotatedTok != "grc_rotated_token" {
		t.Fatalf("rotated secret = %q, %v; want grc_rotated_token", rotatedTok, err)
	}
	mu.Lock()
	if rotateCount != 1 || heartbeatCount != 2 {
		t.Fatalf("rotateCount=%d heartbeatCount=%d, want 1 and 2", rotateCount, heartbeatCount)
	}
	mu.Unlock()

	// 5. Unregister deletes remote entry, clears settings, and removes secret.
	unregText := captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{}, []string{"unregister"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(unregText, "unregistered widgets-dev") {
		t.Fatalf("unregister output = %q", unregText)
	}
	mu.Lock()
	_, stillOnRegistry := entries["widgets-dev"]
	mu.Unlock()
	if stillOnRegistry {
		t.Fatal("expected widgets-dev to be deleted from fake registry")
	}
	hasSec, _ := secStore.Has(secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey)
	if hasSec {
		t.Fatal("expected registry-registration-token secret to be deleted")
	}
	mcAfter, _ := store.GetConfig(ctx)
	if mcAfter.Registry.Enabled() {
		t.Fatalf("expected stored registry config to be cleared, got %+v", mcAfter.Registry)
	}

	// 6. Standalone register writes 0600 token file without contacting a daemon.
	tokFile := filepath.Join(tmp, "standalone.token")
	_ = captureStdout(t, func() {
		err := cmdRegistry(ctx, nil, &printer{}, []string{
			"register",
			"-standalone",
			"-registry-url", regSrv.URL,
			"-token-file", tokFile,
			"standalone-grain",
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	rawTok, err := os.ReadFile(tokFile)
	if err != nil || strings.TrimSpace(string(rawTok)) != "grc_minted_initial_token" {
		t.Fatalf("standalone token file = %q, %v", rawTok, err)
	}
	if fi, err := os.Stat(tokFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("standalone token file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
}

func TestGrainRegistryRegisterRetargetingNeverSendsCrossHostToken(t *testing.T) {
	ctx := context.Background()
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

	const (
		tokOld = "grc_old_registry_token_1111111111"
		tokNew = "grc_new_registry_token_2222222222"
	)

	var leakMu sync.Mutex
	var crossHostLeaks []string

	newFakeReg := func(label, ownTok, otherTok string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotRegTok := r.Header.Get(api.HeaderRegistryToken)
			if gotRegTok == "" {
				if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer grc_") {
					gotRegTok = strings.TrimPrefix(authz, "Bearer ")
				}
			}
			if gotRegTok == otherTok {
				leakMu.Lock()
				crossHostLeaks = append(crossHostLeaks, fmt.Sprintf("%s received other registry's token on %s %s", label, r.Method, r.URL.Path))
				leakMu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/entries/widgets-dev":
				_ = json.NewEncoder(w).Encode(api.Entry{Name: "widgets-dev", Kind: api.KindConnected, HostKind: api.HostKindWorkstation, Listed: true})
			case r.Method == http.MethodPatch && r.URL.Path == "/api/entries/widgets-dev":
				_ = json.NewEncoder(w).Encode(api.Entry{Name: "widgets-dev", Kind: api.KindConnected, HostKind: api.HostKindWorkstation, Listed: true})
			case r.Method == http.MethodPost && r.URL.Path == "/api/entries/widgets-dev/credentials/rotate":
				_ = json.NewEncoder(w).Encode(api.RotateCredentialResponse{RegistrationToken: ownTok})
			case r.Method == http.MethodPut && r.URL.Path == "/api/entries/widgets-dev/heartbeat":
				if gotRegTok != ownTok {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"unexpected token"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(api.HeartbeatResponse{Status: api.StatusOnline, NextIntervalSeconds: 60})
			default:
				http.NotFound(w, r)
			}
		}))
	}

	regOld := newFakeReg("regOld", tokOld, tokNew)
	defer regOld.Close()
	regNew := newFakeReg("regNew", tokNew, tokOld)
	defer regNew.Close()

	prevHTTP := registryHTTPClient
	prevOIDC := registryOIDCToken
	registryHTTPClient = regOld.Client()
	registryOIDCToken = func(_ context.Context, _, _ string) (string, error) {
		return "oidc-creator-jwt", nil
	}
	defer func() {
		registryHTTPClient = prevHTTP
		registryOIDCToken = prevOIDC
	}()

	mgr := daemon.NewRegistryManager(daemon.RegistryManagerOptions{
		Store:   store,
		Secrets: secStore,
		LiveConfig: func() daemon.Config {
			mc, _ := store.GetConfig(ctx)
			if mc == nil {
				return daemon.Config{HostSandboxes: true}
			}
			return daemon.Config{HostSandboxes: true, Registry: mc.Registry}
		},
		Health:         func() (api.HealthState, string) { return api.HealthOK, "" },
		Version:        func() version.Info { return version.Info{Release: "v1.4.0", Revision: "abc1234"} },
		HTTPClient:     regOld.Client(),
		BaseInterval:   time.Hour,
		JitterFraction: -1,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(runCtx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	uiCfg := withAccessCode(ui.Config{
		Actor:                    ui.DefaultActor("operator"),
		Capabilities:             ui.OfferedCapabilities(),
		Secrets:                  secStore,
		RegistryStatus:           mgr.Snapshot,
		TriggerRegistryHeartbeat: mgr.TriggerHeartbeat,
		TriggerRegistrySync: func() {
			// Run an immediate synchronous step whenever settings or secrets
			// change, maximizing race sensitivity for cross-host token leaks.
			_, _ = mgr.TriggerHeartbeat(ctx)
		},
	})
	daemonSrv := httptest.NewServer(ui.NewServer(uiCfg, store))
	defer daemonSrv.Close()

	c := accessCodeClient(daemonSrv.URL)
	if _, err := c.UpdateSettings(ctx, *settingsRequest("30s", 2, "github.example")); err != nil {
		t.Fatal(err)
	}

	// Register initially against regOld.
	_ = captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{}, []string{"register", "-registry-url", regOld.URL, "widgets-dev"}); err != nil {
			t.Fatalf("register regOld: %v", err)
		}
	})

	// Retarget live daemon from regOld to regNew where widgets-dev already exists.
	_ = captureStdout(t, func() {
		if err := cmdRegistry(ctx, c, &printer{}, []string{"register", "-registry-url", regNew.URL, "widgets-dev"}); err != nil {
			t.Fatalf("retarget regNew: %v", err)
		}
	})

	leakMu.Lock()
	leaks := append([]string(nil), crossHostLeaks...)
	leakMu.Unlock()
	if len(leaks) > 0 {
		t.Fatalf("cross-host registry token leak detected:\n%s", strings.Join(leaks, "\n"))
	}
	gotTok, err := secStore.Resolve(ctx, secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey)
	if err != nil || gotTok != tokNew {
		t.Fatalf("stored secret after retarget = %q, %v; want %q", gotTok, err, tokNew)
	}
}

func TestDaemonRegistryFlags(t *testing.T) {
	t.Setenv(daemon.RegistryURLEnv, "")
	t.Setenv(daemon.RegistryEntryEnv, "")

	cfg, _, unknown, err := parseDaemonArgs([]string{
		"-data-dir", "/tmp/grain-data",
		"-disable-native-sandboxes",
		"-registry-url", "https://registry.example.com",
		"-registry-entry", "team-alpha",
		"-registry-auth-mode", "oidc",
		"-registry-oidc-audience", "https://registry.example.com",
		"-registry-display-name", "Team Alpha",
		"-registry-purpose", "Core backend services",
		"-registry-host-kind", "kubernetes",
		"-registry-listed=false",
		"-registry-token-file", "/etc/grain/reg.token",
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("parseDaemonArgs = %v, unknown=%v", err, unknown)
	}
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig(valid registry flags) = %v", err)
	}
	if cfg.Registry.URL != "https://registry.example.com" ||
		cfg.Registry.Entry != "team-alpha" ||
		cfg.Registry.AuthMode != model.RegistryAuthOIDC ||
		cfg.Registry.HostKind != api.HostKindKubernetes ||
		cfg.Registry.Listed != false ||
		cfg.RegistryTokenFile != "/etc/grain/reg.token" {
		t.Fatalf("parsed Registry = %+v (tokenFile=%q)", cfg.Registry, cfg.RegistryTokenFile)
	}

	// Environment variable fallback when flags are omitted.
	t.Setenv(daemon.RegistryURLEnv, "https://env-registry.example.com")
	t.Setenv(daemon.RegistryEntryEnv, "env-grain")
	envCfg, _, _, err := parseDaemonArgs([]string{"-data-dir", "/tmp/grain-data", "-disable-native-sandboxes"})
	if err != nil {
		t.Fatal(err)
	}
	if envCfg.Registry.URL != "https://env-registry.example.com" || envCfg.Registry.Entry != "env-grain" {
		t.Fatalf("env fallback Registry = %+v", envCfg.Registry)
	}

	// Partial configuration (URL without entry) is rejected by checkConfig.
	bad := cfg
	bad.Registry.Entry = ""
	if err := checkConfig(bad); err == nil || !strings.Contains(err.Error(), "-registry-url and -registry-entry") {
		t.Fatalf("checkConfig(missing entry) = %v, want paired flag error", err)
	}

	// Non-HTTPS non-loopback URL is rejected by checkConfig.
	badURL := cfg
	badURL.Registry.URL = "http://registry.example.com"
	if err := checkConfig(badURL); err == nil || !strings.Contains(err.Error(), "insecure http://") {
		t.Fatalf("checkConfig(http url) = %v, want insecure http:// error", err)
	}

	// -registry-connect without registry verifier flags fails closed in checkConfig.
	t.Setenv(daemon.RegistryConnectEnv, "")
	t.Setenv("GRAIN_REGISTRY_ISSUER", "")
	t.Setenv("GRAIN_REGISTRY_AUDIENCE", "")
	t.Setenv("GRAIN_REGISTRY_KEYS_URL", "")
	connCfg, _, _, err := parseDaemonArgs([]string{
		"-data-dir", "/tmp/grain-data",
		"-disable-native-sandboxes",
		"-registry-connect",
	})
	if err != nil {
		t.Fatalf("parseDaemonArgs(-registry-connect): %v", err)
	}
	if !connCfg.RegistryConnect {
		t.Fatal("connCfg.RegistryConnect = false, want true")
	}
	if err := checkConfig(connCfg); err == nil || !strings.Contains(err.Error(), "-ui-registry-issuer") {
		t.Fatalf("checkConfig(-registry-connect without verifier flags) = %v, want missing -ui-registry-issuer error", err)
	}

	// $GRAIN_REGISTRY_CONNECT=true + verifier flags passes checkConfig.
	t.Setenv(daemon.RegistryConnectEnv, "true")
	connEnvCfg, _, _, err := parseDaemonArgs([]string{
		"-data-dir", "/tmp/grain-data",
		"-disable-native-sandboxes",
		"-ui-registry-issuer", "https://grain.example.com",
		"-ui-registry-audience", "https://my-grain.grain.example.com",
		"-ui-registry-keys-url", "https://grain.example.com/.well-known/jwks.json",
	})
	if err != nil {
		t.Fatalf("parseDaemonArgs(GRAIN_REGISTRY_CONNECT=true): %v", err)
	}
	if !connEnvCfg.RegistryConnect {
		t.Fatal("connEnvCfg.RegistryConnect = false, want true")
	}
	if err := checkConfig(connEnvCfg); err != nil {
		t.Fatalf("checkConfig(-registry-connect with verifier flags) = %v, want nil", err)
	}
}

func TestRegistryCLISubcommands(t *testing.T) {
	ctx := context.Background()

	var (
		mu      sync.Mutex
		entries = map[string]api.Entry{}
	)
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/entries":
			var list []api.Entry
			for _, e := range entries {
				list = append(list, e)
			}
			_ = json.NewEncoder(w).Encode(api.ListEntriesResponse{Entries: list})
		case r.Method == http.MethodGet && r.URL.Path == "/api/directory":
			var dir []api.DirectoryEntry
			for _, e := range entries {
				if !e.Listed {
					continue
				}
				dir = append(dir, api.DirectoryEntry{
					Name:        e.Name,
					URL:         "https://" + e.Name + ".grain.example.com",
					Kind:        e.Kind,
					Status:      e.Status,
					HostKind:    e.HostKind,
					DisplayName: e.DisplayName,
					Owner:       e.Owner,
				})
			}
			_ = json.NewEncoder(w).Encode(api.DirectoryResponse{Entries: dir})
		case r.Method == http.MethodGet && r.URL.Path == "/api/route":
			repo := r.URL.Query().Get("repo")
			path := r.URL.Query().Get("path")
			cand := api.RouteCandidate{
				Name:   "platform-vm",
				URL:    "https://platform-vm.grain.example.com",
				Kind:   api.KindInstance,
				Status: api.StatusOnline,
				Owner:  "alice@example.com",
				MatchedScope: api.MatchedScope{
					Repo:        repo,
					PathPrefix:  path,
					Role:        api.RolePrimary,
					Source:      api.SourceDeclared,
					Specificity: len(path),
				},
			}
			_ = json.NewEncoder(w).Encode(api.RouteResponse{
				Repo:       repo,
				Path:       path,
				Primary:    &cand,
				Candidates: []api.RouteCandidate{cand},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries":
			var req api.CreateEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			listed := false
			if req.Listed != nil {
				listed = *req.Listed
			}
			e := api.Entry{
				Name:        req.Name,
				URL:         "https://" + req.Name + ".grain.example.com",
				Kind:        req.Kind,
				State:       api.StateCreating,
				Status:      api.StatusOffline,
				HostKind:    api.HostKindRegistryVM,
				DisplayName: req.DisplayName,
				Purpose:     req.Purpose,
				Owner:       "alice@example.com",
				OwnerGroup:  req.OwnerGroup,
				Listed:      listed,
				Shape:       req.Shape,
				Tags:        req.Tags,
			}
			entries[req.Name] = e
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(api.CreateEntryResponse{Entry: e})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			e, ok := entries[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
				return
			}
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			e := entries[name]
			var req api.UpdateEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.DisplayName != nil {
				e.DisplayName = *req.DisplayName
			}
			if req.Listed != nil {
				e.Listed = *req.Listed
			}
			entries[name] = e
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/scopes"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/scopes")
			e := entries[name]
			var req api.SetScopesRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			e.Scopes = nil
			for _, sc := range req.Scopes {
				e.Scopes = append(e.Scopes, api.Scope{
					Repo:       sc.Repo,
					PathPrefix: sc.PathPrefix,
					Role:       sc.Role,
					Source:     api.SourceDeclared,
					Active:     true,
				})
			}
			entries[name] = e
			_ = json.NewEncoder(w).Encode(api.SetScopesResponse{Scopes: e.Scopes})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/credentials/rotate"):
			_ = json.NewEncoder(w).Encode(api.RotateCredentialResponse{
				RegistrationToken: "grc_rotated_subcmd_token",
				Credential:        api.CredentialMeta{CredentialID: "cred-123"},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upgrade"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/upgrade")
			e := entries[name]
			var req api.UpgradeEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			e.TargetVersion = req.TargetVersion
			entries[name] = e
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/replace"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/replace")
			e := entries[name]
			var req api.ReplaceEntryRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			e.Shape = req.Shape
			e.State = api.StateReplacing
			e.ReplaceStep = api.ReplaceStepDraining
			entries[name] = e
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transfer"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/transfer")
			e := entries[name]
			var req api.TransferRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			e.PendingOwner = req.To
			entries[name] = e
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/transfer/accept"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/transfer/accept")
			e := entries[name]
			e.Owner = e.PendingOwner
			e.PendingOwner = ""
			entries[name] = e
			_ = json.NewEncoder(w).Encode(e)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/entries/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/entries/")
			e := entries[name]
			e.State = api.StateDeleted
			entries[name] = e
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "deleted"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restore"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/entries/"), "/restore")
			e := entries[name]
			e.State = api.StateRunning
			entries[name] = e
			_ = json.NewEncoder(w).Encode(e)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer regSrv.Close()

	prevClient := registryHTTPClient
	prevOIDC := registryOIDCToken
	registryHTTPClient = regSrv.Client()
	registryOIDCToken = func(ctx context.Context, registryURL, audience string) (string, error) {
		return "oidc-test-token", nil
	}
	defer func() {
		registryHTTPClient = prevClient
		registryOIDCToken = prevOIDC
	}()

	out := &printer{json: true}
	runSub := func(args ...string) {
		t.Helper()
		full := append([]string{args[0], "-registry-url", regSrv.URL}, args[1:]...)
		if err := cmdRegistry(ctx, nil, out, full); err != nil {
			t.Fatalf("cmdRegistry(%v) = %v", full, err)
		}
	}

	runSub("create", "-shape", "medium", "-display-name", "Platform VM", "-listed", "-scope", "acme/platform:pkg/api:primary", "platform-vm")
	runSub("list", "-all")
	runSub("directory", "-q", "platform")
	runSub("route", "-repo", "acme/platform", "-path", "pkg/api/types.go")
	runSub("show", "platform-vm")
	runSub("update", "-display-name", "Platform Core VM", "-listed", "platform-vm")
	runSub("scopes", "-set", "acme/platform:pkg/registry:primary", "platform-vm")
	runSub("scopes", "platform-vm")
	runSub("rotate-credential", "-label", "ci-rotation", "-revoke-previous", "platform-vm")
	runSub("upgrade", "-version", "v0.14.2", "platform-vm")
	runSub("resize", "-shape", "large", "platform-vm")
	runSub("transfer", "-to", "bob@example.com", "platform-vm")
	runSub("accept-transfer", "platform-vm")
	runSub("delete", "-confirm", "platform-vm", "platform-vm")
	runSub("restore", "platform-vm")
}

// TestRegistryCLIStopStart drives `grain registry stop` / `start` against a
// fake registry: each posts to its own endpoint, prints the new lifecycle
// state, and refuses a missing or extra entry name before any request.
func TestRegistryCLIStopStart(t *testing.T) {
	ctx := context.Background()

	var (
		mu    sync.Mutex
		calls []string
		entry = api.Entry{Name: "idle-vm", Kind: api.KindInstance, State: api.StateRunning}
	)
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/idle-vm/stop":
			entry.State = api.StateStopping
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/idle-vm/start":
			entry.State = api.StateCreating
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(entry)
		case r.Method == http.MethodPost && r.URL.Path == "/api/entries/edge-box/stop":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":  "invalid_entry_kind",
				"error": `only kind="instance" entries can be stopped`,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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

	run := func(args ...string) (string, error) {
		t.Helper()
		full := append([]string{args[0], "-registry-url", regSrv.URL}, args[1:]...)
		var err error
		stdout := captureStdout(t, func() {
			err = cmdRegistry(ctx, nil, &printer{}, full)
		})
		return stdout, err
	}

	got, err := run("stop", "idle-vm")
	if err != nil {
		t.Fatalf("registry stop: %v", err)
	}
	if want := "requested stop for idle-vm (state=stopping)"; !strings.Contains(got, want) {
		t.Fatalf("registry stop printed %q, want %q", got, want)
	}

	got, err = run("start", "idle-vm")
	if err != nil {
		t.Fatalf("registry start: %v", err)
	}
	if want := "requested start for idle-vm (state=creating)"; !strings.Contains(got, want) {
		t.Fatalf("registry start printed %q, want %q", got, want)
	}

	if _, err := run("stop", "edge-box"); err == nil || !strings.Contains(err.Error(), "can be stopped") {
		t.Fatalf("registry stop on a connected entry = %v, want the registry's refusal", err)
	}

	mu.Lock()
	before := len(calls)
	mu.Unlock()
	for _, args := range [][]string{{"stop"}, {"start"}, {"stop", "a", "b"}} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "usage: grain registry "+args[0]) {
			t.Fatalf("registry %v = %v, want a usage error", args, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != before {
		t.Fatalf("usage errors reached the registry: %v", calls[before:])
	}
	want := []string{
		"POST /api/entries/idle-vm/stop",
		"POST /api/entries/idle-vm/start",
		"POST /api/entries/edge-box/stop",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("registry calls = %v, want %v", calls, want)
	}
}
