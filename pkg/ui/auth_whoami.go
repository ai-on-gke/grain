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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/version"
)

// GET /api/auth/whoami is what `grain login` asks a deployment before it
// has a credential for it, and what `grain whoami` asks with one
// (design/spec/external-cli/external-cli.spec.md#XC-8). Unlike GET
// /api/whoami (whoami.go), which the gate answers only for a caller it
// already admitted, it answers anybody: a caller with no credential is
// told how it could sign in, and nothing else.

// AuthWhoamiResponse is GET /api/auth/whoami.
type AuthWhoamiResponse struct {
	// AcceptedAuth is how this daemon lets a caller in: "access-code",
	// "external-token", "iap", "proxy".
	AcceptedAuth     []string `json:"acceptedAuth"`
	ExternalAgentAPI bool     `json:"externalAgentApi"`
	Version          string   `json:"version,omitempty"`

	Authenticated bool `json:"authenticated"`
	// Principal is "human:<id>", "agent:external:<name>" or "operator".
	Principal  string `json:"principal,omitempty"`
	Actor      string `json:"actor,omitempty"`
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
	Via        string `json:"via,omitempty"`
	// HostAccount is true for a person known by the uid that connected
	// to the daemon's API socket (hostaccount.go), not by a credential.
	HostAccount bool `json:"hostAccount,omitempty"`
	// The rest describe an external agent credential (gma_...).
	TokenID        string     `json:"tokenId,omitempty"`
	Owner          string     `json:"owner,omitempty"`
	ScopeSet       string     `json:"scopeSet,omitempty"`
	ResolvedScopes []string   `json:"resolvedScopes,omitempty"`
	Repos          []string   `json:"repos,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

// TokenExpiredCode is the code a 401 for an expired external agent
// credential carries, beside its expiredAt.
const TokenExpiredCode = "token_expired"

// tokenExpiredResponse is the body of that 401.
type tokenExpiredResponse struct {
	Error     string    `json:"error"`
	Code      string    `json:"code"`
	ExpiredAt time.Time `json:"expiredAt"`
}

func writeTokenExpired(w http.ResponseWriter, at time.Time) {
	writeJSON(w, http.StatusUnauthorized, tokenExpiredResponse{
		Error: "external agent bearer token has expired", Code: TokenExpiredCode, ExpiredAt: at.UTC(),
	})
}

func (s *Server) handleAuthWhoami(w http.ResponseWriter, r *http.Request) {
	resp := AuthWhoamiResponse{Version: version.Get().Release}
	if s.tasks != nil && s.tasks.Store != nil {
		if cfg, err := s.tasks.Store.GetConfig(r.Context()); err == nil && cfg != nil {
			resp.ExternalAgentAPI = cfg.ExternalAgentAPI
		}
	}
	iap, _ := IAPConfigFromContext(r.Context())
	if accessCodeAccepted(r, iap) {
		resp.AcceptedAuth = append(resp.AcceptedAuth, "access-code")
	}
	if resp.ExternalAgentAPI {
		resp.AcceptedAuth = append(resp.AcceptedAuth, "external-token")
	}
	if iap.Audience != "" || requiredVerifier(iap) == string(AuthMethodIAP) {
		resp.AcceptedAuth = append(resp.AcceptedAuth, "iap")
	}
	if len(registeredVerifiersSnapshot()) > 0 {
		resp.AcceptedAuth = append(resp.AcceptedAuth, "proxy")
	}
	if resp.AcceptedAuth == nil {
		resp.AcceptedAuth = []string{}
	}

	if tok := externalTokenFromContext(r.Context()); tok != nil {
		view := externalAgentTokenViewOf(*tok, s.tasks.now())
		resp.Authenticated = true
		resp.Principal = string(model.PrincipalAgent) + ":" + view.Principal
		resp.TokenID, resp.Owner, resp.ScopeSet = view.ID, view.Owner, view.ScopeSet
		resp.ResolvedScopes, resp.Repos, resp.ExpiresAt = view.ResolvedScopes, view.Repos, view.ExpiresAt
	} else if p, ok := PrincipalFromContext(r.Context()); ok && p.Kind == model.PrincipalHuman && p.ID != "" {
		resp.Authenticated, resp.Principal = true, "human:"+p.ID
		resp.HostAccount = p.Source == model.SourceHostAccount
		if del := delegatedIdentityFromContext(r.Context()); del != nil {
			resp.Actor = "service:" + del.AppEmail
			resp.OnBehalfOf = "user:" + del.PersonEmail
			resp.Via = del.ViaLabel
		}
	} else if singleTenantTrusted(r) {
		resp.Authenticated, resp.Principal = true, "operator"
	}
	writeJSON(w, http.StatusOK, resp)
}

// TokenExpiredError is GET /api/auth/whoami's answer to an external
// agent credential that has expired.
type TokenExpiredError struct{ ExpiredAt time.Time }

func (e *TokenExpiredError) Error() string {
	return "external agent bearer token expired on " + e.ExpiredAt.UTC().Format("2006-01-02")
}

// AuthWhoami is GET /api/auth/whoami. An expired credential is a
// TokenExpiredError; any other refusal is the usual error for it.
func (c *HTTPClient) AuthWhoami(ctx context.Context) (AuthWhoamiResponse, error) {
	var out AuthWhoamiResponse
	resp, err := c.send(ctx, http.MethodGet, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/auth/whoami", nil)
	})
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if err := signInResponse(c.BaseURL, resp); err != nil {
		return out, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var expired tokenExpiredResponse
		if json.Unmarshal(body, &expired) == nil && expired.Code == TokenExpiredCode {
			return out, &TokenExpiredError{ExpiredAt: expired.ExpiredAt}
		}
		return out, &StatusError{StatusCode: resp.StatusCode, Status: resp.Status, Message: errorMessage(body, resp.Status)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, c.httpError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decoding response from GET /api/auth/whoami: %w", err)
	}
	return out, nil
}

func errorMessage(body []byte, fallback string) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return fallback
}

// CreateExternalAgentToken is POST /api/external-agents/tokens.
func (c *HTTPClient) CreateExternalAgentToken(ctx context.Context, req CreateExternalAgentTokenRequest) (IssuedExternalAgentToken, error) {
	var out IssuedExternalAgentToken
	err := c.do(ctx, http.MethodPost, "/api/external-agents/tokens", req, &out)
	return out, err
}

// RotateExternalAgentToken is POST /api/external-agents/tokens/{id}/rotate.
func (c *HTTPClient) RotateExternalAgentToken(ctx context.Context, id string) (IssuedExternalAgentToken, error) {
	var out IssuedExternalAgentToken
	err := c.do(ctx, http.MethodPost, "/api/external-agents/tokens/"+url.PathEscape(id)+"/rotate", nil, &out)
	return out, err
}

// RevokeExternalAgentToken is POST /api/external-agents/tokens/{id}/revoke.
func (c *HTTPClient) RevokeExternalAgentToken(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/external-agents/tokens/"+url.PathEscape(id)+"/revoke", nil, nil)
}
