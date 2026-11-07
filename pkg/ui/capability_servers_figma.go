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

package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	figmaToolTimeout     = time.Minute
	figmaOAuthStateTTL   = 15 * time.Minute
	maxFigmaOAuthPending = 256
)

// TestFigmaAccountRequest is POST /api/capability-servers/figma-test.
type TestFigmaAccountRequest struct {
	ID      string `json:"id"`
	Account string `json:"account"`
}

// FigmaOAuthAuthorizeRequest is POST /api/capability-servers/figma-oauth-authorize.
type FigmaOAuthAuthorizeRequest struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	RedirectURI string `json:"redirectUri"`
}

// FigmaOAuthAuthorizeResponse is returned by POST /api/capability-servers/figma-oauth-authorize.
type FigmaOAuthAuthorizeResponse struct {
	Account      string `json:"account"`
	AuthorizeURL string `json:"authorizeUrl"`
	State        string `json:"state"`
	Scopes       string `json:"scopes"`
}

// FigmaOAuthExchangeRequest is POST /api/capability-servers/figma-oauth-exchange.
type FigmaOAuthExchangeRequest struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Code        string `json:"code"`
	State       string `json:"state,omitempty"`
	RedirectURI string `json:"redirectUri"`
}

// FigmaOAuthExchangeResponse is returned by POST /api/capability-servers/figma-oauth-exchange.
// Neither access_token nor refresh_token is ever returned to the browser.
type FigmaOAuthExchangeResponse struct {
	Account   string `json:"account"`
	UserID    string `json:"userId,omitempty"`
	ExpiresIn int    `json:"expiresIn,omitempty"`
}

type pendingFigmaOAuthState struct {
	serverID    string
	account     string
	redirectURI string
	expiresAt   time.Time
}

var (
	figmaOAuthMu     sync.Mutex
	figmaOAuthStates = map[string]pendingFigmaOAuthState{}
	// FigmaOAuthAPIBase and FigmaOAuthHTTPClient allow unit tests to point code
	// exchange at a local httptest.Server without touching production defaults.
	FigmaOAuthAPIBase    string
	FigmaOAuthHTTPClient *http.Client
)

func issueFigmaOAuthState(serverID, account, redirectURI string) (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	state := hex.EncodeToString(raw[:])
	now := time.Now()

	figmaOAuthMu.Lock()
	defer figmaOAuthMu.Unlock()
	for k, v := range figmaOAuthStates {
		if now.After(v.expiresAt) {
			delete(figmaOAuthStates, k)
		}
	}
	if len(figmaOAuthStates) >= maxFigmaOAuthPending {
		for k := range figmaOAuthStates {
			delete(figmaOAuthStates, k)
			break
		}
	}
	figmaOAuthStates[state] = pendingFigmaOAuthState{
		serverID:    serverID,
		account:     account,
		redirectURI: redirectURI,
		expiresAt:   now.Add(figmaOAuthStateTTL),
	}
	return state, nil
}

func consumeFigmaOAuthState(state, serverID, account, redirectURI string) error {
	figmaOAuthMu.Lock()
	defer figmaOAuthMu.Unlock()
	now := time.Now()
	for k, v := range figmaOAuthStates {
		if now.After(v.expiresAt) {
			delete(figmaOAuthStates, k)
		}
	}
	for k, v := range figmaOAuthStates {
		if subtle.ConstantTimeCompare([]byte(k), []byte(state)) == 1 {
			delete(figmaOAuthStates, k)
			if v.serverID != serverID || v.account != account || v.redirectURI != redirectURI {
				return validationErrorf("OAuth state does not match the target Figma server, account, or redirect URI")
			}
			return nil
		}
	}
	return validationErrorf("OAuth state expired or invalid; start OAuth authorization again")
}

func (c *Client) figmaEntry(ctx context.Context, id string) (protocol.ServerConfig, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return protocol.ServerConfig{}, validationErrorf("a capability_id is required")
	}
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return protocol.ServerConfig{}, err
	}
	var configured map[string]protocol.ServerConfig
	if cfg != nil {
		configured = cfg.MCPServers
	}
	configured, _ = intree.EnsureBuiltins(configured)
	entry, ok := configured[id]
	if !ok || (entry.Builtin != intree.FigmaName && intree.InTreeName(entry) != intree.FigmaName) {
		return protocol.ServerConfig{}, validationErrorf("capability server %q does not run grain's figma server", id)
	}
	return entry, nil
}

func (c *Client) figmaCaller(ctx context.Context, id, what string) (CapabilityServerToolCaller, error) {
	caller, err := c.toolCaller(id, what)
	if err != nil {
		return nil, err
	}
	if _, err := c.figmaEntry(ctx, id); err != nil {
		return nil, err
	}
	return caller, nil
}

func findFigmaOAuthAccount(sc protocol.ServerConfig, account string) (clientID, scopes string, err error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return "", "", validationErrorf("name the Figma account")
	}
	rawAccounts, _ := sc.Config["accounts"].([]any)
	for _, item := range rawAccounts {
		m, _ := item.(map[string]any)
		name, _ := m["name"].(string)
		if strings.TrimSpace(name) != account {
			continue
		}
		mode, _ := m["auth_mode"].(string)
		if strings.TrimSpace(mode) != intree.FigmaAuthOAuth {
			return "", "", validationErrorf("Figma account %q uses auth_mode %q, not oauth", account, mode)
		}
		cid, _ := m["client_id"].(string)
		cid = strings.TrimSpace(cid)
		if cid == "" {
			return "", "", validationErrorf("Figma account %q has no client_id configured", account)
		}
		scStr, _ := m["scopes"].(string)
		return cid, strings.TrimSpace(scStr), nil
	}
	return "", "", validationErrorf("Figma account %q is not configured on this server (save the account first)", account)
}

func (c *Client) resolveSecretJSONMap(ctx context.Context, fieldVal any) map[string]string {
	out := map[string]string{}
	if c.Config.Secrets == nil {
		return out
	}
	ref, _ := fieldVal.(string)
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "$secret:") {
		return out
	}
	raw, err := c.Config.Secrets.Resolve(ctx, strings.TrimPrefix(ref, "$secret:"))
	if err != nil || len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// TestFigmaAccount calls test_account on an attached figma capability server.
func (c *Client) TestFigmaAccount(ctx context.Context, req TestFigmaAccountRequest) (intree.FigmaTestResult, error) {
	var out intree.FigmaTestResult
	id := strings.TrimSpace(req.ID)
	account := strings.TrimSpace(req.Account)
	if account == "" {
		return out, validationErrorf("name the Figma account to test")
	}
	caller, err := c.figmaCaller(ctx, id, "test a Figma account")
	if err != nil {
		return out, err
	}
	raw, err := caller.CallTool(ctx, id, intree.FigmaTestTool, map[string]any{"account": account}, figmaToolTimeout)
	if err != nil {
		return out, validationErrorf("%s", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("capability server %s: %s answered %q: %w", id, intree.FigmaTestTool, raw, err)
	}
	return out, nil
}

// AuthorizeFigmaOAuth builds a CSRF-protected Figma OAuth 2.0 authorization URL
// for a configured oauth account (working even before its first refresh_token
// has been generated).
func (c *Client) AuthorizeFigmaOAuth(ctx context.Context, req FigmaOAuthAuthorizeRequest) (FigmaOAuthAuthorizeResponse, error) {
	var out FigmaOAuthAuthorizeResponse
	id := strings.TrimSpace(req.ID)
	account := strings.TrimSpace(req.Account)
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if redirectURI == "" {
		return out, validationErrorf("redirectUri is required")
	}
	entry, err := c.figmaEntry(ctx, id)
	if err != nil {
		return out, err
	}
	clientID, scopes, err := findFigmaOAuthAccount(entry, account)
	if err != nil {
		return out, err
	}
	state, err := issueFigmaOAuthState(id, account, redirectURI)
	if err != nil {
		return out, fmt.Errorf("generating OAuth state: %w", err)
	}
	authURL, err := intree.BuildFigmaOAuthAuthorizeURL("", clientID, redirectURI, scopes, state)
	if err != nil {
		return out, validationErrorf("%s", err)
	}
	if scopes == "" {
		scopes = "file_content:read,file_metadata:read,file_comments:read"
	}
	return FigmaOAuthAuthorizeResponse{
		Account:      account,
		AuthorizeURL: authURL,
		State:        state,
		Scopes:       scopes,
	}, nil
}

// ExchangeFigmaOAuth exchanges a Figma OAuth 2.0 authorization code for a
// refresh_token and stores it in Grain's encrypted secrets store for account.
func (c *Client) ExchangeFigmaOAuth(ctx context.Context, req FigmaOAuthExchangeRequest) (FigmaOAuthExchangeResponse, error) {
	var out FigmaOAuthExchangeResponse
	id := strings.TrimSpace(req.ID)
	account := strings.TrimSpace(req.Account)
	code := strings.TrimSpace(req.Code)
	redirectURI := strings.TrimSpace(req.RedirectURI)
	state := strings.TrimSpace(req.State)
	if code == "" {
		return out, validationErrorf("authorization code is required")
	}
	if redirectURI == "" {
		return out, validationErrorf("redirectUri is required")
	}
	if err := c.checkSettingsWritableUnrehearsed(ctx, "grain_config"); err != nil {
		return out, err
	}
	if c.Config.Secrets == nil {
		return out, errSecretsUnavailable
	}
	entry, err := c.figmaEntry(ctx, id)
	if err != nil {
		return out, err
	}
	clientID, _, err := findFigmaOAuthAccount(entry, account)
	if err != nil {
		return out, err
	}
	if state != "" {
		if err := consumeFigmaOAuthState(state, id, account, redirectURI); err != nil {
			return out, err
		}
	}
	clientSecrets := c.resolveSecretJSONMap(ctx, entry.Config["client_secrets"])
	clientSecret := strings.TrimSpace(clientSecrets[account])
	if clientSecret == "" {
		return out, validationErrorf("Figma account %q has no client secret stored in client_secrets yet; save its client secret first", account)
	}

	tok, err := intree.ExchangeFigmaOAuthCode(ctx, FigmaOAuthHTTPClient, FigmaOAuthAPIBase, clientID, clientSecret, code, redirectURI)
	if err != nil {
		return out, validationErrorf("%s", err)
	}
	if strings.TrimSpace(tok.RefreshToken) == "" {
		return out, validationErrorf("Figma OAuth token exchange returned no refresh_token")
	}

	secretRefName := configSecretName(id, "refresh_tokens")
	if existingRef, ok := entry.Config["refresh_tokens"].(string); ok && strings.HasPrefix(existingRef, "$secret:") {
		secretRefName = strings.TrimPrefix(existingRef, "$secret:")
	}
	refreshMap := c.resolveSecretJSONMap(ctx, entry.Config["refresh_tokens"])
	refreshMap[account] = strings.TrimSpace(tok.RefreshToken)
	encoded, err := json.Marshal(refreshMap)
	if err != nil {
		return out, err
	}
	secretName, secretKey, explicit := strings.Cut(secretRefName, "/")
	if !explicit {
		secretKey = "value"
	}
	if err := c.Config.Secrets.Set(secretName, secretKey, encoded); err != nil {
		return out, validationErrorf("%s", err)
	}

	if err := c.updateCapabilityServers(ctx, func(m map[string]protocol.ServerConfig) error {
		sc := m[id]
		if sc.Config == nil {
			sc.Config = map[string]any{}
		}
		sc.Config["refresh_tokens"] = protocol.SecretRef(secretRefName)
		m[id] = sc
		return nil
	}); err != nil {
		return out, err
	}

	by := RequestActor(ctx)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		By:         by.Actor,
		OnBehalfOf: by.OnBehalfOf,
		Surface:    "capability-servers:figma-oauth",
		Summary:    fmt.Sprintf("stored Figma OAuth refresh token for account %q on %s", account, id),
	}); err != nil {
		log.Printf("ui: recording figma-oauth config audit: %v", err)
	}

	return FigmaOAuthExchangeResponse{
		Account:   account,
		UserID:    tok.UserIDString,
		ExpiresIn: tok.ExpiresIn,
	}, nil
}

func (s *Server) handleTestFigmaAccount(w http.ResponseWriter, r *http.Request) {
	var req TestFigmaAccountRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.TestFigmaAccount(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAuthorizeFigmaOAuth(w http.ResponseWriter, r *http.Request) {
	var req FigmaOAuthAuthorizeRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.AuthorizeFigmaOAuth(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleExchangeFigmaOAuth(w http.ResponseWriter, r *http.Request) {
	var req FigmaOAuthExchangeRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := s.tasks.ExchangeFigmaOAuth(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// TestFigmaAccount is POST /api/capability-servers/figma-test.
func (c *HTTPClient) TestFigmaAccount(ctx context.Context, req TestFigmaAccountRequest) (intree.FigmaTestResult, error) {
	var out intree.FigmaTestResult
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/figma-test", req, &out)
	return out, err
}

// AuthorizeFigmaOAuth is POST /api/capability-servers/figma-oauth-authorize.
func (c *HTTPClient) AuthorizeFigmaOAuth(ctx context.Context, req FigmaOAuthAuthorizeRequest) (FigmaOAuthAuthorizeResponse, error) {
	var out FigmaOAuthAuthorizeResponse
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/figma-oauth-authorize", req, &out)
	return out, err
}

// ExchangeFigmaOAuth is POST /api/capability-servers/figma-oauth-exchange.
func (c *HTTPClient) ExchangeFigmaOAuth(ctx context.Context, req FigmaOAuthExchangeRequest) (FigmaOAuthExchangeResponse, error) {
	var out FigmaOAuthExchangeResponse
	err := c.do(ctx, http.MethodPost, "/api/capability-servers/figma-oauth-exchange", req, &out)
	return out, err
}
