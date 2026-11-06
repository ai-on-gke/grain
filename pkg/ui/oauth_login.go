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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	oauthLoginPath     = "/api/auth/oauth/login"
	oauthCallbackPath  = "/api/auth/oauth/callback"
	oauthCLIConfigPath = "/api/auth/oauth/cli-config"

	oauthStateTTL = 10 * time.Minute
)

const errOAuthRequired = "this daemon requires OAuth2 (-ui-require-oauth / GRAIN_REQUIRE_OAUTH): " +
	"the access code is not accepted over the network, so sign in at /api/auth/oauth/login or send a " +
	"Google OIDC ID token in Authorization: Bearer (`grain login`); the access code still works from " +
	"the daemon's own host over loopback (kubectl exec deploy/grain -- grain ...)"

// OAuthCLIConfigResponse is the discovery document returned by GET /api/auth/oauth/cli-config
// so `grain login` can discover the deployment's Desktop OAuth 2.0 Client ID and endpoints.
type OAuthCLIConfigResponse struct {
	Enabled         bool     `json:"enabled"`
	Issuer          string   `json:"issuer,omitempty"`
	AuthURL         string   `json:"authUrl,omitempty"`
	TokenURL        string   `json:"tokenUrl,omitempty"`
	CLIClientID     string   `json:"cliClientId,omitempty"`
	CLIClientSecret string   `json:"cliClientSecret,omitempty"`
	WebClientID     string   `json:"webClientId,omitempty"`
	HostedDomains   []string `json:"hostedDomains,omitempty"`
}

func oauthWebClientIDFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.ClientID) != "" {
		return strings.TrimSpace(cfg.ClientID)
	}
	oauthMu.RLock()
	cid := oauthClientID
	oauthMu.RUnlock()
	if cid != "" {
		return cid
	}
	return strings.TrimSpace(os.Getenv("GRAIN_OAUTH_CLIENT_ID"))
}

func oauthWebClientSecretFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.ClientSecret) != "" {
		return strings.TrimSpace(cfg.ClientSecret)
	}
	return strings.TrimSpace(os.Getenv("GRAIN_OAUTH_CLIENT_SECRET"))
}

func oauthCLIClientIDFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.CLIClientID) != "" {
		return strings.TrimSpace(cfg.CLIClientID)
	}
	oauthMu.RLock()
	cid := oauthCLIClient
	oauthMu.RUnlock()
	if cid != "" {
		return cid
	}
	return strings.TrimSpace(os.Getenv("GRAIN_OAUTH_CLI_CLIENT_ID"))
}

func oauthCLIClientSecretFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.CLIClientSecret) != "" {
		return strings.TrimSpace(cfg.CLIClientSecret)
	}
	return strings.TrimSpace(os.Getenv("GRAIN_OAUTH_CLI_CLIENT_SECRET"))
}

func oauthAuthURLFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.AuthURL) != "" {
		return strings.TrimSpace(cfg.AuthURL)
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_AUTH_URL")); env != "" {
		return env
	}
	return GoogleOAuthAuthURL
}

func oauthTokenURLFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.TokenURL) != "" {
		return strings.TrimSpace(cfg.TokenURL)
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_TOKEN_URL")); env != "" {
		return env
	}
	return GoogleOAuthTokenURL
}

func oauthRedirectURLFor(r *http.Request) string {
	ctx := r.Context()
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.RedirectURL) != "" {
		return strings.TrimSpace(cfg.RedirectURL)
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_REDIRECT_URL")); env != "" {
		return env
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if i := strings.IndexByte(host, ','); i >= 0 {
		host = strings.TrimSpace(host[:i])
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host + oauthCallbackPath
}

func oauthBrowserLoginEnabled(ctx context.Context) bool {
	return oauthWebClientIDFor(ctx) != "" && oauthWebClientSecretFor(ctx) != ""
}

type oauthStatePayload struct {
	State        string `json:"s"`
	CodeVerifier string `json:"v"`
	Next         string `json:"n,omitempty"`
	RedirectURI  string `json:"r,omitempty"`
	ExpiresAt    int64  `json:"exp"`
}

func randomBase64URL(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceS256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sanitizeNextRedirect(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, `/\`) {
		return "/"
	}
	return next
}

func setOAuthSessionCookie(w http.ResponseWriter, r *http.Request, token string, lifetime time.Duration) {
	if lifetime <= 0 {
		lifetime = DefaultOAuthSessionLifetime
	}
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(lifetime / time.Second),
		Expires:  time.Now().Add(lifetime),
	})
}

func clearOAuthSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func clearOAuthStateCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthStateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// handleOAuthLogin initiates the browser OAuth 2.0 Authorization Code + PKCE flow.
func (s *Server) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clientID := oauthWebClientIDFor(ctx)
	clientSecret := oauthWebClientSecretFor(ctx)
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusNotFound, errors.New("browser OAuth2 login is not configured on this daemon (set GRAIN_OAUTH_CLIENT_ID and GRAIN_OAUTH_CLIENT_SECRET)"))
		return
	}
	key := oauthSessionKeyFor(ctx)
	if len(key) == 0 {
		writeError(w, http.StatusInternalServerError, errors.New("no OAuth2 session signing key available"))
		return
	}
	state, err := randomBase64URL(18)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	verifier, err := randomBase64URL(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	redirectURI := oauthRedirectURLFor(r)
	next := sanitizeNextRedirect(r.URL.Query().Get("next"))
	payload := oauthStatePayload{
		State:        state,
		CodeVerifier: verifier,
		Next:         next,
		RedirectURI:  redirectURI,
		ExpiresAt:    time.Now().Add(oauthStateTTL).Unix(),
	}
	rawState, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	signedState := signOAuthCookiePayload("st1", rawState, key)
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthStateCookieName,
		Value:    signedState,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthStateTTL / time.Second),
		Expires:  time.Now().Add(oauthStateTTL),
	})

	authURL, err := url.Parse(oauthAuthURLFor(ctx))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("invalid OAuth2 authorization URL: %w", err))
		return
	}
	q := authURL.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("code_challenge", pkceS256Challenge(verifier))
	q.Set("code_challenge_method", "S256")
	if domains := oauthHostedDomainsFor(ctx); len(domains) == 1 {
		q.Set("hd", domains[0])
	}
	authURL.RawQuery = q.Encode()

	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL.String(), http.StatusFound)
}

// handleOAuthCallback handles the OAuth 2.0 callback, exchanges the authorization
// code + PKCE verifier for an ID token, verifies it, checks the principal allowlist,
// and sets the grain_oauth_session cookie.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clearOAuthStateCookie(w, r)

	clientID := oauthWebClientIDFor(ctx)
	clientSecret := oauthWebClientSecretFor(ctx)
	if clientID == "" || clientSecret == "" {
		writeError(w, http.StatusNotFound, errors.New("browser OAuth2 login is not configured on this daemon"))
		return
	}
	if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
		desc := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if desc != "" {
			writeAssertionRefused(w, r, fmt.Errorf("OAuth2 provider returned %s: %s", oauthErr, desc))
		} else {
			writeAssertionRefused(w, r, fmt.Errorf("OAuth2 provider returned %s", oauthErr))
		}
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	stateParam := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || stateParam == "" {
		writeAssertionRefused(w, r, errors.New("missing code or state parameter in OAuth2 callback"))
		return
	}

	stateCookie, err := r.Cookie(OAuthStateCookieName)
	if err != nil || stateCookie == nil || strings.TrimSpace(stateCookie.Value) == "" {
		writeAssertionRefused(w, r, errors.New("missing OAuth2 state cookie; start sign-in again at /api/auth/oauth/login"))
		return
	}
	key := oauthSessionKeyFor(ctx)
	rawState, ok := verifyOAuthCookiePayload(stateCookie.Value, "st1", key)
	if !ok {
		writeAssertionRefused(w, r, errors.New("invalid OAuth2 state cookie signature"))
		return
	}
	var st oauthStatePayload
	if err := json.Unmarshal(rawState, &st); err != nil {
		writeAssertionRefused(w, r, errors.New("malformed OAuth2 state cookie"))
		return
	}
	if time.Now().Unix() > st.ExpiresAt {
		writeAssertionRefused(w, r, errors.New("OAuth2 login state expired; start sign-in again at /api/auth/oauth/login"))
		return
	}
	if st.State == "" || st.State != stateParam {
		writeAssertionRefused(w, r, errors.New("OAuth2 state mismatch"))
		return
	}

	redirectURI := st.RedirectURI
	if redirectURI == "" {
		redirectURI = oauthRedirectURLFor(r)
	}
	idToken, err := exchangeOAuthCodeForIDToken(ctx, oauthTokenURLFor(ctx), clientID, clientSecret, code, st.CodeVerifier, redirectURI)
	if err != nil {
		writeAssertionRefused(w, r, err)
		return
	}

	claims, err := validateOAuthIDToken(ctx, idToken)
	if err != nil {
		logRefusedAssertion(idToken, "", err)
		writeAssertionRefused(w, r, err)
		return
	}
	email := normalizeIdentityEmail(claims.Email)
	id := VerifiedIdentity{
		Email: email,
		Kind:  principalKindForEmail(email),
	}
	if err := s.checkAllowedPrincipal(ctx, id); err != nil {
		clearOAuthSessionCookie(w, r)
		cv := callerVerification{
			present:  true,
			verifier: defaultOAuthVerifier,
			id:       id,
			method:   AuthMethodOAuth,
		}
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeError(w, http.StatusForbidden, err)
			return
		}
		s.writePrincipalRefusedPage(w, r, cv, err)
		return
	}

	lifetime := oauthSessionLifetimeFor(ctx)
	now := time.Now()
	sessToken, err := mintOAuthSessionToken(oauthSessionPayload{
		Email:     id.Email,
		Kind:      id.Kind,
		ClientID:  clientID,
		HD:        strings.ToLower(strings.TrimSpace(claims.HD)),
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(lifetime).Unix(),
	}, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	setOAuthSessionCookie(w, r, sessToken, lifetime)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, sanitizeNextRedirect(st.Next), http.StatusFound)
}

func exchangeOAuthCodeForIDToken(ctx context.Context, tokenURL, clientID, clientSecret, code, codeVerifier, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	hc := http.DefaultClient

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging OAuth2 authorization code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading OAuth2 token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OAuth2 token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decoding OAuth2 token response: %w", err)
	}
	if strings.TrimSpace(parsed.IDToken) == "" {
		return "", errors.New("OAuth2 token response did not include an id_token")
	}
	return strings.TrimSpace(parsed.IDToken), nil
}

// handleOAuthCLIConfig serves GET /api/auth/oauth/cli-config so `grain login`
// can discover the deployment's Desktop OAuth 2.0 Client ID and endpoints.
func (s *Server) handleOAuthCLIConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	audiences := oauthAudiencesFor(ctx)
	cliID := oauthCLIClientIDFor(ctx)
	webID := oauthWebClientIDFor(ctx)
	enabled := len(audiences) > 0
	resp := OAuthCLIConfigResponse{
		Enabled:       enabled,
		HostedDomains: oauthHostedDomainsFor(ctx),
	}
	if enabled {
		resp.Issuer = oauthIssuerFor(ctx)
		resp.AuthURL = oauthAuthURLFor(ctx)
		resp.TokenURL = oauthTokenURLFor(ctx)
		resp.CLIClientID = cliID
		resp.CLIClientSecret = oauthCLIClientSecretFor(ctx)
		resp.WebClientID = webID
	}
	writeJSON(w, http.StatusOK, resp)
}

var oauthRequiredPage = template.Must(template.New("oauth-required").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>grain: sign in with Google</title>
<style>
body { font-family: system-ui, sans-serif; display: flex; min-height: 100vh; margin: 0; align-items: center; justify-content: center; background: #f5f5f5; }
main { background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,.15); max-width: 28rem; width: 100%; box-sizing: border-box; }
h1 { font-size: 1.25rem; margin: 0 0 1rem; }
p { color: #444; font-size: .9rem; line-height: 1.45; }
a.button { display: inline-block; background: #1a73e8; color: #fff; text-decoration: none; padding: .6rem 1.2rem; border-radius: 4px; font-weight: 500; margin-top: .5rem; }
</style>
</head>
<body>
<main>
<h1>Sign in with Google</h1>
<p>This grain requires OAuth2 authentication and does not accept an access code over the network.</p>
{{if .LoginURL}}<p><a class="button" href="{{.LoginURL}}">Sign in with Google</a></p>{{else}}<p>Authenticate with <code>grain login</code> from the CLI or configure <code>GRAIN_OAUTH_CLIENT_ID</code> and <code>GRAIN_OAUTH_CLIENT_SECRET</code> for browser sign-in.</p>{{end}}
</main>
</body>
</html>
`))

func writeOAuthRequiredPage(w http.ResponseWriter, r *http.Request) {
	var data struct {
		LoginURL string
	}
	if oauthBrowserLoginEnabled(r.Context()) {
		next := sanitizeNextRedirect(r.URL.RequestURI())
		u := oauthLoginPath
		if next != "/" && next != "" {
			u += "?next=" + url.QueryEscape(next)
		}
		data.LoginURL = u
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	if err := oauthRequiredPage.Execute(w, data); err != nil {
		fmt.Fprintln(w, "sign in with Google")
	}
}
