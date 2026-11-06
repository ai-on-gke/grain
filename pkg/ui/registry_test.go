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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/webauth"
	"github.com/golang-jwt/jwt/v5"
)

func TestRegistrySettingsAndEndpoints(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	secDir := t.TempDir()
	secStore := secrets.Open(secrets.Config{
		File:    filepath.Join(secDir, "secrets.age"),
		KeyFile: filepath.Join(secDir, "secrets.key"),
	})
	c.Config.Secrets = secStore

	var syncCalls atomic.Int32
	c.Config.TriggerRegistrySync = func() { syncCalls.Add(1) }

	srv := ui.NewServerWithClient(c)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatal(err)
	}

	// Unconfigured daemon: GET /api/registry reports disabled, and
	// GET /api/sandboxes omits registry health.
	rec := do(t, srv, http.MethodGet, "/api/registry", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry = %d: %s", rec.Code, rec.Body.String())
	}
	snap := decode[model.RegistrySnapshot](t, rec)
	if snap.Enabled || snap.State != model.RegistryStateDisabled {
		t.Fatalf("initial registry snapshot = %+v, want disabled", snap)
	}
	healthRec := do(t, srv, http.MethodGet, "/api/sandboxes", "")
	if healthRec.Code != http.StatusOK {
		t.Fatalf("GET /api/sandboxes = %d: %s", healthRec.Code, healthRec.Body.String())
	}
	if h := decode[ui.SandboxHealthResponse](t, healthRec); h.Registry != nil {
		t.Fatalf("unconfigured deployment reported non-nil Registry health: %+v", h.Registry)
	}

	// Invalid registry URL (plain HTTP to non-loopback) is rejected by UpdateSettings.
	badReg := model.RegistryConfig{URL: "http://registry.example.com", Entry: "grain-dev"}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &badReg}); err == nil {
		t.Fatal("expected plain HTTP non-loopback registry URL to fail validation")
	}

	// Add two repositories and configure work ownership on one as "off" -> secondary.
	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("AddTargetRepo(acme/widgets): %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/docs"); err != nil {
		t.Fatalf("AddTargetRepo(acme/docs): %v", err)
	}
	if _, err := c.SetRepoWorkDefault(ctx, "acme/docs", "off"); err != nil {
		t.Fatalf("SetRepoWorkDefault(acme/docs, off): %v", err)
	}

	// Set registration token secret via PUT /api/secrets/registry-registration-token/value
	// and verify TriggerRegistrySync fires.
	beforeSync := syncCalls.Load()
	secRec := do(t, srv, http.MethodPut,
		"/api/secrets/"+secrets.RegistryRegistrationTokenSecret+"/"+secrets.AgentCredentialKey,
		`{"value":"grc_supersecrettokenvalue"}`)
	if secRec.Code != http.StatusOK {
		t.Fatalf("PUT registry secret = %d: %s", secRec.Code, secRec.Body.String())
	}
	if syncCalls.Load() <= beforeSync {
		t.Fatal("setting registry-registration-token secret did not trigger registry sync")
	}

	// Configure registry settings with a path-prefix scope override on acme/widgets.
	regCfg := model.RegistryConfig{
		URL:         "https://registry.example.com/",
		Entry:       "grain-dev",
		DisplayName: "Grain Dev",
		Purpose:     "Widgets team controller",
		HostKind:    api.HostKindWorkstation,
		Listed:      true,
		Scopes: []model.RegistryScopeRule{
			{Repo: "acme/widgets", PathPrefix: "/pkg/autoscaler/"},
		},
	}
	saved, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &regCfg})
	if err != nil {
		t.Fatalf("UpdateSettings(Registry): %v", err)
	}
	if saved.Registry.URL != "https://registry.example.com" {
		t.Errorf("saved Registry.URL = %q, want trailing slash trimmed", saved.Registry.URL)
	}
	if !saved.RegistryTokenSet {
		t.Error("saved.RegistryTokenSet = false, want true")
	}

	// Seed one running task so GET /api/registry and GET /api/registry/status
	// report live queue counts.
	now := time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID:         "101",
		Title:      "Fix autoscaler",
		ApprovedAt: &now,
		CreatedAt:  &now,
	}); err != nil {
		t.Fatalf("PutTask: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID:        "run-101",
		TaskID:    "101",
		Attempt:   1,
		StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// GET /api/registry/status returns wire api.InstanceStatusResponse with derived scopes.
	statusRec := do(t, srv, http.MethodGet, "/api/registry/status", "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry/status = %d: %s", statusRec.Code, statusRec.Body.String())
	}
	inst := decode[api.InstanceStatusResponse](t, statusRec)
	if inst.ActiveTasks != 1 {
		t.Errorf("InstanceStatusResponse.ActiveTasks = %d, want 1", inst.ActiveTasks)
	}
	if len(inst.Scopes) != 2 {
		t.Fatalf("InstanceStatusResponse.Scopes = %+v, want 2 scopes", inst.Scopes)
	}
	if inst.Scopes[0].Repo != "acme/docs" || inst.Scopes[0].Role != api.RoleSecondary {
		t.Errorf("Scopes[0] = %+v, want acme/docs secondary", inst.Scopes[0])
	}
	if inst.Scopes[1].Repo != "acme/widgets" || inst.Scopes[1].PathPrefix != "pkg/autoscaler" || inst.Scopes[1].Role != api.RolePrimary {
		t.Errorf("Scopes[1] = %+v, want acme/widgets pkg/autoscaler primary", inst.Scopes[1])
	}

	// Verify LogRedactor redacts secret material in LastError on GET /api/registry
	// and GET /api/sandboxes, and POST /api/registry/heartbeat invokes callback.
	c.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{
			{Label: "secret registry-registration-token", Value: "grc_supersecrettokenvalue"},
		}), nil
	}
	var hbTriggered atomic.Bool
	c.Config.TriggerRegistryHeartbeat = func(_ context.Context) (model.RegistrySnapshot, error) {
		hbTriggered.Store(true)
		return model.RegistrySnapshot{
			Enabled:   true,
			State:     model.RegistryStateError,
			Config:    saved.Registry,
			LastError: "auth failed with bearer grc_supersecrettokenvalue",
		}, nil
	}
	c.Config.RegistryStatus = c.Config.TriggerRegistryHeartbeat

	hbRec := do(t, srv, http.MethodPost, "/api/registry/heartbeat", "")
	if hbRec.Code != http.StatusOK {
		t.Fatalf("POST /api/registry/heartbeat = %d: %s", hbRec.Code, hbRec.Body.String())
	}
	if !hbTriggered.Load() {
		t.Error("POST /api/registry/heartbeat did not invoke TriggerRegistryHeartbeat")
	}
	hbSnap := decode[model.RegistrySnapshot](t, hbRec)
	if strings.Contains(hbSnap.LastError, "grc_supersecrettokenvalue") {
		t.Errorf("LastError leaked secret token: %q", hbSnap.LastError)
	}

	// Verify Client.TriggerRegistryHeartbeat also redacts errors returned
	// directly by c.Config.TriggerRegistryHeartbeat using LogRedactor.
	c.Config.LogRedactor = func() (*grainlog.Redactor, error) {
		return grainlog.NewRedactor([]grainlog.KnownValue{
			{Label: "secret registry-registration-token", Value: "custom-secret-value-xyz987"},
		}), nil
	}
	c.Config.TriggerRegistryHeartbeat = func(_ context.Context) (model.RegistrySnapshot, error) {
		return model.RegistrySnapshot{}, errors.New("upstream failed with custom-secret-value-xyz987")
	}
	if _, hbErr := c.TriggerRegistryHeartbeat(ctx); hbErr == nil || strings.Contains(hbErr.Error(), "custom-secret-value-xyz987") {
		t.Fatalf("TriggerRegistryHeartbeat error leaked LogRedactor secret: %v", hbErr)
	}
}

func TestRegistryDefaultShapeRedactionAndHealthDetail(t *testing.T) {
	t.Parallel()
	c, _, _ := testClient(t)
	// Leave c.Config.LogRedactor nil to verify shape-based redaction still
	// cuts grc_ tokens, X-Grain-Registry-Token headers, and git URL tokens
	// from both LastError and HealthDetail.
	c.Config.RegistryStatus = func(_ context.Context) (model.RegistrySnapshot, error) {
		return model.RegistrySnapshot{
			Enabled:      true,
			State:        model.RegistryStateError,
			Config:       model.RegistryConfig{URL: "https://registry.example.com", Entry: "grain-dev"},
			LastError:    "X-Grain-Registry-Token: grc_shorttok\nfailed with grc_AbCdEfGhIjKlMnOpQrStUvWxYz",
			Health:       api.HealthDegraded,
			HealthDetail: "sync failed against https://oauth2:ghp_abcdefghijklmnopqrstuvwxyz1234@github.com/acme/state.git",
		}, nil
	}
	srv := ui.NewServerWithClient(c)

	regRec := do(t, srv, http.MethodGet, "/api/registry", "")
	if regRec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry = %d: %s", regRec.Code, regRec.Body.String())
	}
	for _, secret := range []string{"grc_shorttok", "grc_AbCdEfGh", "ghp_abcdefgh"} {
		if strings.Contains(regRec.Body.String(), secret) {
			t.Errorf("GET /api/registry leaked %q: %s", secret, regRec.Body.String())
		}
	}

	statusRec := do(t, srv, http.MethodGet, "/api/registry/status", "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /api/registry/status = %d: %s", statusRec.Code, statusRec.Body.String())
	}
	if strings.Contains(statusRec.Body.String(), "ghp_abcdefgh") {
		t.Errorf("GET /api/registry/status leaked git credential in HealthDetail: %s", statusRec.Body.String())
	}
}

func TestRegistryTargetRequiresSecretsWriteRole(t *testing.T) {
	h := newRoleHarness(t)
	installScopeSets(t,
		model.APIScopeSet{
			Name:   "team/settings-only",
			Scopes: []string{"settings:read", "settings:write"},
		},
		model.APIScopeSet{
			Name:   "team/settings-and-secrets",
			Scopes: []string{"settings:read", "settings:write", "secrets:write"},
		},
	)
	h.allow(t, "user:sam@example.com", "team/settings-only")
	h.allow(t, "user:ops@example.com", "team/settings-and-secrets")

	if _, err := h.client.UpdateSettings(context.Background(), firstSettings()); err != nil {
		t.Fatal(err)
	}

	body := `{"registry":{"url":"https://registry.example.com","entry":"grain-dev"}}`
	if code, resp := h.call("sam@example.com", http.MethodPut, "/api/settings", body); code != http.StatusForbidden {
		t.Errorf("settings-only role PUT /api/settings(registry.url): %d %s, want 403", code, resp)
	}
	if code, resp := h.call("ops@example.com", http.MethodPut, "/api/settings", body); code != http.StatusOK {
		t.Fatalf("settings+secrets role PUT /api/settings(registry.url): %d %s, want 200", code, resp)
	}
}

func TestRegistryTunnelHandlerRequiresAssertionAndRejectsShortcuts(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	c, _, ctx := testClient(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	byteLen := (ecKey.PublicKey.Curve.Params().BitSize + 7) / 8
	xBytes := ecKey.PublicKey.X.FillBytes(make([]byte, byteLen))
	yBytes := ecKey.PublicKey.Y.FillBytes(make([]byte, byteLen))
	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{
					"kty": "EC",
					"crv": "P-256",
					"alg": "ES256",
					"use": "sig",
					"kid": "reg-key-1",
					"x":   base64.RawURLEncoding.EncodeToString(xBytes),
					"y":   base64.RawURLEncoding.EncodeToString(yBytes),
				},
			},
		})
	}))
	t.Cleanup(jwksSrv.Close)

	const (
		issuer     = "https://grain.example.com"
		audience   = "https://my-grain.grain.example.com"
		accessCode = "local-access-code-secret"
	)

	c.Config.LocalAPIToken = accessCode
	c.Config.RegistryIssuer = issuer
	c.Config.RegistryAudience = audience
	c.Config.RegistryKeysURL = jwksSrv.URL
	c.Config.AllowedPrincipals = []string{"user:alice@example.com"}
	c.Config.StateRepoRef = func() model.RepoRef {
		return model.RepoRef{Owner: "acme", Name: "grain-config"}
	}
	// Intentionally leave RequireVerifier empty on c.Config to simulate a
	// workstation grain whose local listener accepts the access code while
	// -registry-connect serves accepted mux streams behind RegistryTunnelHandler.

	srv := ui.NewServerWithClient(c)
	ui.SetAccessCodeForTest(srv, accessCode)
	if _, err := c.UpdateSettings(ctx, firstSettings()); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/widgets"); err != nil {
		t.Fatalf("AddTargetRepo(acme/widgets): %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, "acme/grain-config"); err != nil {
		t.Fatalf("AddTargetRepo(acme/grain-config): %v", err)
	}
	if _, err := c.AddTargetRepo(ctx, model.ConfigRepo().String()); err != nil {
		t.Fatalf("AddTargetRepo(%s): %v", model.ConfigRepo(), err)
	}

	// 1. On the local listener (srv), local access code is accepted and
	// HTTPClient.GetRegistryInstanceStatus returns scopes excluding grain/config and stateRepo.
	localHTTP := httptest.NewServer(srv)
	t.Cleanup(localHTTP.Close)
	hc := ui.NewHTTPClient(localHTTP.URL)
	hc.Token = accessCode
	instStatus, err := hc.GetRegistryInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("GetRegistryInstanceStatus on local listener: %v", err)
	}
	if len(instStatus.Scopes) != 1 || instStatus.Scopes[0].Repo != "acme/widgets" {
		t.Fatalf("GetRegistryInstanceStatus scopes = %+v, want [acme/widgets] (excluding grain/config)", instStatus.Scopes)
	}

	// 2. On RegistryTunnelHandler(), every route unconditionally requires a
	// valid X-Grain-Registry-Assertion and rejects loopback/access-code shortcuts.
	tunnelHandler := srv.RegistryTunnelHandler()
	mintAssertion := func(aud string, exp time.Time) string {
		now := time.Now()
		claims := webauth.RegistryClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Audience:  jwt.ClaimStrings{aud},
				Subject:   "alice@example.com",
				IssuedAt:  jwt.NewNumericDate(now.Add(-5 * time.Second)),
				NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
				ExpiresAt: jwt.NewNumericDate(exp),
				ID:        "jti-tunnel-1",
			},
			Email:     "alice@example.com",
			Kind:      webauth.PrincipalHuman,
			RequestID: "req_tunnel_1",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "reg-key-1"
		signed, err := tok.SignedString(ecKey)
		if err != nil {
			t.Fatalf("SignedString: %v", err)
		}
		return signed
	}
	validAssertion := mintAssertion(audience, time.Now().Add(55*time.Second))

	for _, path := range []string{"/", "/api/config", "/api/auth/whoami", "/api/hostd/connect", "/api/registry/status"} {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		tunnelHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("tunnel GET %s without assertion = %d, want 401", path, rec.Code)
		}
	}

	// Reject wrong audience and expired assertions.
	for name, tok := range map[string]string{
		"wrong-audience": mintAssertion("https://other.grain.example.com", time.Now().Add(55*time.Second)),
		"expired":        mintAssertion(audience, time.Now().Add(-5*time.Minute)),
	} {
		req := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/registry/status", nil)
		req.Header.Set(ui.RegistryAssertionHeader, tok)
		rec := httptest.NewRecorder()
		tunnelHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("tunnel GET with %s assertion = %d, want 401", name, rec.Code)
		}
	}

	// Reject access-code headers, cookies, and non-external-agent bearer tokens
	// even when accompanied by a valid X-Grain-Registry-Assertion and loopback RemoteAddr.
	shortcutCases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "X-Grain-Access-Code header",
			mutate: func(r *http.Request) {
				r.Header.Set("X-Grain-Access-Code", accessCode)
			},
		},
		{
			name: "grain_access cookie",
			mutate: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "grain_access", Value: accessCode})
			},
		},
		{
			name: "Authorization Bearer access code",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+accessCode)
			},
		},
		{
			name: "Authorization Bearer callback token (ghsa_)",
			mutate: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer ghsa_localscriptcallbacktoken")
			},
		},
	}
	for _, sc := range shortcutCases {
		req := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/registry/status", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set(ui.RegistryAssertionHeader, validAssertion)
		sc.mutate(req)
		rec := httptest.NewRecorder()
		tunnelHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("tunnel request with %s = %d (%s), want 401", sc.name, rec.Code, rec.Body.String())
		}
	}

	// Valid X-Grain-Registry-Assertion without shortcuts succeeds on tunnel stream.
	okReq := httptest.NewRequest(http.MethodGet, "https://my-grain.grain.example.com/api/registry/status", nil)
	okReq.RemoteAddr = "127.0.0.1:54321"
	okReq.Header.Set(ui.RegistryAssertionHeader, validAssertion)
	okRec := httptest.NewRecorder()
	tunnelHandler.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("tunnel GET /api/registry/status with valid assertion = %d: %s", okRec.Code, okRec.Body.String())
	}
	gotStatus := decode[api.InstanceStatusResponse](t, okRec)
	if len(gotStatus.Scopes) != 1 || gotStatus.Scopes[0].Repo != "acme/widgets" {
		t.Errorf("tunnel InstanceStatusResponse.Scopes = %+v, want [acme/widgets]", gotStatus.Scopes)
	}
}
