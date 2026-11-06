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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/golang-jwt/jwt/v5"
)

func TestCLIOAuthLoginPKCEFlowAndLogout(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const (
		cliClientID = "987654-cli.apps.googleusercontent.com"
		kid         = "cli-test-rsa-key-1"
	)
	n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.PublicKey.E)).Bytes())

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   n,
				"e":   e,
			}},
		})
	}))
	defer jwksSrv.Close()

	var gotVerifier string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotVerifier = r.FormValue("code_verifier")
		verified := true
		claims := ui.OAuthClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    ui.GoogleOIDCIssuer,
				Subject:   "sub-alice",
				Audience:  jwt.ClaimStrings{cliClientID},
				IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			Email:         "alice@example.com",
			EmailVerified: &verified,
			HD:            "example.com",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = kid
		signed, err := tok.SignedString(priv)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "test-cli-access-token",
			"refresh_token": "1//cli-refresh",
			"id_token":      signed,
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()

	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_ = store.PutConfig(context.Background(), model.Config{PollInterval: model.DefaultPollInterval})

	grainSrv := httptest.NewServer(ui.NewServer(ui.Config{
		SourceDB:           db,
		Actor:              ui.DefaultActor("operator"),
		OAuthCLIClientID:   cliClientID,
		OAuthKeysURL:       jwksSrv.URL,
		OAuthAuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
		OAuthTokenURL:      tokenSrv.URL,
		OAuthHostedDomains: "example.com",
		RequireOAuth:       true,
		AllowedPrincipals:  []string{"user:alice@example.com=developer"},
	}, store))
	defer grainSrv.Close()

	credFile := filepath.Join(t.TempDir(), "oauth-credentials.json")
	t.Setenv(ui.OAuthCredentialsFileEnv, credFile)
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, "")

	origBrowser := oauthLoginOpenBrowser
	origHook := oauthLoginCallbackHook
	t.Cleanup(func() {
		oauthLoginOpenBrowser = origBrowser
		oauthLoginCallbackHook = origHook
	})
	oauthLoginOpenBrowser = func(string) error { return nil }
	oauthLoginCallbackHook = func(authURL string) {
		u, err := url.Parse(authURL)
		if err != nil {
			t.Errorf("invalid authURL: %v", err)
			return
		}
		redirectURI := u.Query().Get("redirect_uri")
		state := u.Query().Get("state")
		go func() {
			resp, err := http.Get(redirectURI + "?code=test-auth-code&state=" + url.QueryEscape(state))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}

	hc := ui.NewHTTPClient(grainSrv.URL)
	if err := cmdOAuthLogin(context.Background(), hc, &printer{json: true}, []string{"-no-browser"}); err != nil {
		t.Fatalf("cmdOAuthLogin: %v", err)
	}
	if gotVerifier == "" {
		t.Fatalf("expected PKCE code_verifier to be sent during token exchange")
	}

	// Verify credentials were saved to credFile and work automatically for subsequent HTTPClient calls.
	loaded, err := ui.LoadOAuthCredentials(credFile)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := loaded.Servers[grainSrv.URL]
	if !ok || stored.Email != "alice@example.com" || stored.RefreshToken != "1//cli-refresh" {
		t.Fatalf("stored credential = %+v, ok=%v", stored, ok)
	}

	freshClient := ui.NewHTTPClient(grainSrv.URL)
	if _, err := freshClient.ListTasks(context.Background()); err != nil {
		t.Fatalf("ListTasks using stored CLI OAuth credential: %v", err)
	}

	// Logout removes the stored credential.
	if err := cmdOAuthLogout(context.Background(), freshClient, &printer{json: true}, nil); err != nil {
		t.Fatalf("cmdOAuthLogout: %v", err)
	}
	loadedAfter, err := ui.LoadOAuthCredentials(credFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := loadedAfter.Servers[grainSrv.URL]; exists {
		t.Fatalf("expected credential for %s to be removed after logout", grainSrv.URL)
	}
}

func TestCheckConfigRefusesRequireOAuthWithoutClientID(t *testing.T) {
	t.Setenv("GRAIN_OAUTH_CLIENT_ID", "")
	t.Setenv("GRAIN_OAUTH_CLI_CLIENT_ID", "")
	t.Setenv("GRAIN_OAUTH_EXTRA_AUDIENCES", "")
	t.Setenv("GRAIN_REQUIRE_OAUTH", "true")

	start := startable()
	err := checkConfig(start)
	if err == nil || !strings.Contains(err.Error(), "GRAIN_REQUIRE_OAUTH needs an OAuth 2.0 Client ID") {
		t.Fatalf("checkConfig(GRAIN_REQUIRE_OAUTH=true, no client ID) = %v, want refusal naming missing Client ID", err)
	}

	t.Setenv("GRAIN_OAUTH_CLIENT_ID", "12345-web.apps.googleusercontent.com")
	if err := checkConfig(start); err != nil {
		t.Fatalf("checkConfig with GRAIN_OAUTH_CLIENT_ID set: %v", err)
	}
}

func TestAccessCodeNotesAServerThatRequiresOAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/access-code" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"authenticated":false,"requireOAuth":true,"accessCodeAccepted":true}`))
	}))
	defer srv.Close()
	t.Setenv(serverEnvVar, srv.URL)
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	if _, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir)); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "grain login") {
		t.Fatalf("stderr = %q, want note mentioning `grain login`", errOut.String())
	}
}
