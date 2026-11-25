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
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/jwks"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// AuthMethodOAuth is a verified Google OAuth2 / OIDC ID token or session.
	AuthMethodOAuth AuthMethod = "oauth"

	// GoogleOIDCIssuer is the primary issuer claim in Google OIDC ID tokens.
	GoogleOIDCIssuer = "https://accounts.google.com"
	// GoogleOIDCIssuerAlt is the alternate issuer string Google OIDC tokens may carry.
	GoogleOIDCIssuerAlt = "accounts.google.com"

	// GoogleOIDCKeysURL is Google's OIDC JWKS endpoint.
	GoogleOIDCKeysURL = "https://www.googleapis.com/oauth2/v3/certs"

	// GoogleOAuthAuthURL is Google's OAuth 2.0 authorization endpoint.
	GoogleOAuthAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

	// GoogleOAuthTokenURL is Google's OAuth 2.0 token endpoint.
	GoogleOAuthTokenURL = "https://oauth2.googleapis.com/token"

	// OAuthIDTokenHeader is an optional explicit header for passing a Google OIDC ID token.
	OAuthIDTokenHeader = "X-Grain-OAuth-ID-Token"

	// OAuthSessionCookieName is the browser session cookie issued after an in-app OAuth2 login.
	OAuthSessionCookieName = "grain_oauth_session"

	// OAuthStateCookieName stores the short-lived OAuth2 PKCE + state nonce during browser login.
	OAuthStateCookieName = "grain_oauth_state"

	// DefaultOAuthSessionLifetime is the default duration of an in-app OAuth2 browser session.
	DefaultOAuthSessionLifetime = 24 * time.Hour
)

type oauthConfigContextKey struct{}

// OAuthConfig holds request-level or server-level direct OAuth2 / OIDC configuration.
type OAuthConfig struct {
	// ClientID is the Google Cloud Web Application OAuth 2.0 Client ID.
	ClientID string
	// ClientSecret is the Google Cloud Web Application OAuth 2.0 Client Secret.
	ClientSecret string
	// CLIClientID is the Google Cloud Desktop App OAuth 2.0 Client ID used by `grain login`.
	CLIClientID string
	// CLIClientSecret is the Desktop App OAuth 2.0 Client Secret used by `grain login`.
	CLIClientSecret string
	// ExtraAudiences is a comma-separated list of additional accepted OAuth2 Client IDs / audiences.
	ExtraAudiences string
	// RedirectURL is the explicit callback URL for the browser OAuth2 flow.
	RedirectURL string
	// HostedDomains is a comma-separated list of allowed Google Workspace hd domains.
	HostedDomains string
	// Issuer is the expected OIDC issuer (defaults to GoogleOIDCIssuer).
	Issuer string
	// KeysURL is the JWKS endpoint used to verify OIDC ID tokens (defaults to GoogleOIDCKeysURL).
	KeysURL string
	// AuthURL overrides Google's OAuth 2.0 authorization URL.
	AuthURL string
	// TokenURL overrides Google's OAuth 2.0 token exchange URL.
	TokenURL string
	// SessionLifetime is how long a browser OAuth2 session cookie remains valid.
	SessionLifetime time.Duration
	// SessionKey is the secret used to HMAC-sign OAuth2 session and state cookies.
	SessionKey string
	// RequireOAuth disables the single-tenant access code over the network (preserving unforwarded loopback).
	RequireOAuth bool
	// LocalAPIToken is the fallback key source for signing cookies when SessionKey is unset.
	LocalAPIToken string
}

// WithOAuthConfig attaches an OAuthConfig to ctx.
func WithOAuthConfig(ctx context.Context, cfg OAuthConfig) context.Context {
	return context.WithValue(ctx, oauthConfigContextKey{}, cfg)
}

// OAuthConfigFromContext extracts OAuthConfig from ctx if present.
func OAuthConfigFromContext(ctx context.Context) (OAuthConfig, bool) {
	if ctx == nil {
		return OAuthConfig{}, false
	}
	cfg, ok := ctx.Value(oauthConfigContextKey{}).(OAuthConfig)
	return cfg, ok
}

// OAuthClaims represents the JWT claims in a Google OIDC ID token.
type OAuthClaims struct {
	jwt.RegisteredClaims
	Email           string `json:"email,omitempty"`
	EmailVerified   *bool  `json:"email_verified,omitempty"`
	HD              string `json:"hd,omitempty"`
	AuthorizedParty string `json:"azp,omitempty"`
}

var (
	oauthMu         sync.RWMutex
	oauthClientID   string
	oauthCLIClient  string
	oauthExtraAuds  string
	oauthIssuer     string
	oauthKeysURLVar = GoogleOIDCKeysURL
	oauthDomains    string
)

// SetOAuthClientID sets the package-level default OAuth2 Web Client ID.
func SetOAuthClientID(id string) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	oauthClientID = strings.TrimSpace(id)
}

// SetOAuthKeysURL overrides the package-level JWKS URL for Google OIDC verification.
func SetOAuthKeysURL(u string) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	if strings.TrimSpace(u) != "" {
		oauthKeysURLVar = strings.TrimSpace(u)
	} else {
		oauthKeysURLVar = GoogleOIDCKeysURL
	}
}

// SetOAuthHostedDomains sets the package-level allowed Google Workspace hd domains.
func SetOAuthHostedDomains(domains string) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	oauthDomains = strings.TrimSpace(domains)
}

func resetOAuthConfig() {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	oauthClientID = ""
	oauthCLIClient = ""
	oauthExtraAuds = ""
	oauthIssuer = ""
	oauthKeysURLVar = GoogleOIDCKeysURL
	oauthDomains = ""
}

func oauthAudiencesFor(ctx context.Context) []string {
	var raw []string
	if cfg, ok := OAuthConfigFromContext(ctx); ok {
		raw = append(raw, cfg.ClientID, cfg.CLIClientID, cfg.ExtraAudiences)
	}
	oauthMu.RLock()
	raw = append(raw, oauthClientID, oauthCLIClient, oauthExtraAuds)
	oauthMu.RUnlock()
	raw = append(raw,
		os.Getenv("GRAIN_OAUTH_CLIENT_ID"),
		os.Getenv("GRAIN_OAUTH_CLI_CLIENT_ID"),
		os.Getenv("GRAIN_OAUTH_EXTRA_AUDIENCES"),
		os.Getenv("GRAIN_OAUTH_CLIENT_IDS"),
	)
	var out []string
	seen := map[string]bool{}
	for _, r := range raw {
		for _, a := range strings.Split(r, ",") {
			if a = strings.TrimSpace(a); a != "" && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

func oauthIssuerFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Issuer) != "" {
		return strings.TrimSpace(cfg.Issuer)
	}
	oauthMu.RLock()
	iss := oauthIssuer
	oauthMu.RUnlock()
	if iss != "" {
		return iss
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_ISSUER")); env != "" {
		return env
	}
	return GoogleOIDCIssuer
}

func oauthKeysURLFor(ctx context.Context) string {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.KeysURL) != "" {
		return strings.TrimSpace(cfg.KeysURL)
	}
	oauthMu.RLock()
	u := oauthKeysURLVar
	oauthMu.RUnlock()
	if u != GoogleOIDCKeysURL {
		return u
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_KEYS_URL")); env != "" {
		return env
	}
	return u
}

func oauthHostedDomainsFor(ctx context.Context) []string {
	var raw string
	if cfg, ok := OAuthConfigFromContext(ctx); ok && strings.TrimSpace(cfg.HostedDomains) != "" {
		raw = cfg.HostedDomains
	} else {
		oauthMu.RLock()
		raw = oauthDomains
		oauthMu.RUnlock()
		if raw == "" {
			raw = os.Getenv("GRAIN_OAUTH_HOSTED_DOMAINS")
		}
	}
	var out []string
	for _, d := range strings.Split(raw, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func oauthRequireFor(ctx context.Context) bool {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && cfg.RequireOAuth {
		return true
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GRAIN_REQUIRE_OAUTH")))
	return v == "1" || v == "true" || v == "yes"
}

func oauthSessionLifetimeFor(ctx context.Context) time.Duration {
	if cfg, ok := OAuthConfigFromContext(ctx); ok && cfg.SessionLifetime > 0 {
		return cfg.SessionLifetime
	}
	if env := strings.TrimSpace(os.Getenv("GRAIN_OAUTH_SESSION_LIFETIME")); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	return DefaultOAuthSessionLifetime
}

func oauthSessionKeyFor(ctx context.Context) []byte {
	var raw string
	if cfg, ok := OAuthConfigFromContext(ctx); ok {
		raw = strings.TrimSpace(cfg.SessionKey)
		if raw == "" {
			raw = strings.TrimSpace(os.Getenv("GRAIN_OAUTH_SESSION_KEY"))
		}
		if raw == "" && strings.TrimSpace(cfg.ClientSecret) != "" {
			raw = strings.TrimSpace(cfg.ClientSecret)
		}
		if raw == "" && strings.TrimSpace(cfg.LocalAPIToken) != "" {
			raw = canonicalAccessCode(cfg.LocalAPIToken)
		}
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("GRAIN_OAUTH_SESSION_KEY"))
	}
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("GRAIN_OAUTH_CLIENT_SECRET"))
	}
	if raw == "" {
		return nil
	}
	sum := sha256.Sum256([]byte("grain oauth2 session key\x00" + raw))
	return sum[:]
}

type oauthVerifier struct{}

var defaultOAuthVerifier IdentityVerifier = &oauthVerifier{}

func (v *oauthVerifier) Name() string {
	return string(AuthMethodOAuth)
}

// Headers returns nil because oauthVerifier inspects Bearer JWT tokens and
// grain_oauth_session cookies selectively rather than a dedicated proxy header.
func (v *oauthVerifier) Headers() []string {
	return nil
}

// extractOAuthBearerJWT extracts a compact 3-segment JWT from X-Grain-OAuth-ID-Token,
// Proxy-Authorization: Bearer, or Authorization: Bearer (excluding gma_ external
// agent tokens and single-tenant access codes).
func extractOAuthBearerJWT(r *http.Request) (tokenStr string, headerName string, present bool) {
	if r == nil {
		return "", "", false
	}
	if raw := strings.TrimSpace(r.Header.Get(OAuthIDTokenHeader)); raw != "" {
		return raw, OAuthIDTokenHeader, true
	}
	if pa := strings.TrimSpace(r.Header.Get("Proxy-Authorization")); strings.HasPrefix(pa, "Bearer ") {
		tok := strings.TrimSpace(strings.TrimPrefix(pa, "Bearer "))
		if looksLikeCompactJWT(tok) {
			return tok, "Proxy-Authorization", true
		}
	}
	if tok, ok := bearerToken(r); ok && looksLikeCompactJWT(tok) {
		return tok, "Authorization", true
	}
	return "", "", false
}

func looksLikeCompactJWT(tok string) bool {
	if tok == "" || strings.HasPrefix(tok, model.ExternalAgentTokenPrefix) {
		return false
	}
	return strings.Count(tok, ".") == 2
}

func (v *oauthVerifier) Verify(r *http.Request) (VerifiedIdentity, bool, error) {
	if r == nil {
		return VerifiedIdentity{}, false, nil
	}
	ctx := r.Context()
	if tok, _, present := extractOAuthBearerJWT(r); present {
		claims, err := validateOAuthIDToken(ctx, tok)
		if err != nil {
			logRefusedAssertion(tok, "", err)
			return VerifiedIdentity{}, false, err
		}
		email := normalizeIdentityEmail(claims.Email)
		return VerifiedIdentity{
			Email: email,
			Kind:  principalKindForEmail(email),
		}, true, nil
	}

	// Check browser OAuth2 session cookie if present.
	c, err := r.Cookie(OAuthSessionCookieName)
	if err == nil && c != nil && strings.TrimSpace(c.Value) != "" {
		sess, ok := verifyOAuthSession(c.Value, oauthSessionKeyFor(ctx), time.Now())
		if !ok {
			// Expired or invalid session cookie: return ok=false, err=nil so
			// singleTenantGate clears the stale cookie and prompts sign-in.
			return VerifiedIdentity{}, false, nil
		}
		email := normalizeIdentityEmail(sess.Email)
		if email == "" {
			return VerifiedIdentity{}, false, nil
		}
		kind := sess.Kind
		if kind == "" {
			kind = principalKindForEmail(email)
		}
		if err := checkOAuthHostedDomain(ctx, email, kind, sess.HD); err != nil {
			return VerifiedIdentity{}, false, nil
		}
		return VerifiedIdentity{
			Email: email,
			Kind:  kind,
		}, true, nil
	}

	return VerifiedIdentity{}, false, nil
}

func checkOAuthHostedDomain(ctx context.Context, email string, kind model.PrincipalKind, hd string) error {
	if kind == model.PrincipalService {
		return nil
	}
	domains := oauthHostedDomainsFor(ctx)
	if len(domains) == 0 {
		return nil
	}
	hd = strings.ToLower(strings.TrimSpace(hd))
	if hd == "" {
		_, emailDomain, _ := strings.Cut(email, "@")
		hd = strings.ToLower(strings.TrimSpace(emailDomain))
	}
	if !slices.Contains(domains, hd) {
		return fmt.Errorf("OAuth hosted domain %q is not in allowed domains (%s)", hd, strings.Join(domains, ","))
	}
	return nil
}

const oauthClockSkew = 30 * time.Second

func getOAuthPublicKey(ctx context.Context, certURL, kid string) (crypto.PublicKey, error) {
	if certURL == "" {
		certURL = GoogleOIDCKeysURL
	}
	keys, err := jwks.Fetch(ctx, http.DefaultClient, certURL, "OAuth")
	if err != nil {
		return nil, err
	}
	k, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("unknown OAuth key ID %q", kid)
	}
	return k, nil
}

// validateOAuthIDToken verifies a Google OIDC ID token against the configured
// JWKS endpoint, issuer, client ID audiences, expiry, email_verified, and
// optional Workspace hosted domain.
func validateOAuthIDToken(ctx context.Context, tokenStr string) (*OAuthClaims, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return nil, errors.New("empty OAuth ID token")
	}
	audiences := oauthAudiencesFor(ctx)
	if len(audiences) == 0 {
		return nil, errors.New("no OAuth2 client ID is configured (GRAIN_OAUTH_CLIENT_ID / -ui-oauth-client-id), so no OIDC ID token can be verified")
	}
	expectedIssuer := oauthIssuerFor(ctx)
	certURL := oauthKeysURLFor(ctx)

	claims := &OAuthClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || strings.TrimSpace(kid) == "" {
			return nil, errors.New("missing kid in OAuth ID token header")
		}
		key, err := getOAuthPublicKey(ctx, certURL, kid)
		if err != nil {
			return nil, err
		}
		switch key.(type) {
		case *ecdsa.PublicKey:
			if t.Method.Alg() != jwt.SigningMethodES256.Alg() {
				return nil, fmt.Errorf("unexpected OAuth signing algorithm %v for an EC key", t.Header["alg"])
			}
		case *rsa.PublicKey:
			if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
				return nil, fmt.Errorf("unexpected OAuth signing algorithm %v for an RSA key", t.Header["alg"])
			}
		default:
			return nil, errors.New("unsupported OAuth key type")
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg(), jwt.SigningMethodES256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(oauthClockSkew),
	)
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid OAuth ID token: %w", err)
	}

	gotIssuer := strings.TrimSpace(claims.Issuer)
	if expectedIssuer == GoogleOIDCIssuer || expectedIssuer == GoogleOIDCIssuerAlt {
		if gotIssuer != GoogleOIDCIssuer && gotIssuer != GoogleOIDCIssuerAlt {
			return nil, fmt.Errorf("OAuth issuer mismatch: expected %q, got %q", GoogleOIDCIssuer, gotIssuer)
		}
	} else if gotIssuer != expectedIssuer {
		return nil, fmt.Errorf("OAuth issuer mismatch: expected %q, got %q", expectedIssuer, gotIssuer)
	}

	matched := false
	for _, want := range audiences {
		if slices.Contains(claims.Audience, want) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf("OAuth audience mismatch: expected one of [%s], got %v", strings.Join(audiences, ", "), claims.Audience)
	}

	email := normalizeIdentityEmail(claims.Email)
	if email == "" {
		return nil, errors.New("no email claim found in verified OAuth ID token")
	}
	kind := principalKindForEmail(email)
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return nil, fmt.Errorf("OAuth ID token for %q has email_verified=false", email)
	}
	if kind == model.PrincipalHuman && claims.EmailVerified == nil {
		return nil, fmt.Errorf("OAuth ID token for human principal %q is missing email_verified claim", email)
	}
	if err := checkOAuthHostedDomain(ctx, email, kind, claims.HD); err != nil {
		return nil, err
	}

	return claims, nil
}

type oauthSessionPayload struct {
	Email     string              `json:"email"`
	Kind      model.PrincipalKind `json:"kind"`
	ClientID  string              `json:"cid,omitempty"`
	HD        string              `json:"hd,omitempty"`
	IssuedAt  int64               `json:"iat"`
	ExpiresAt int64               `json:"exp"`
}

func signOAuthCookiePayload(prefix string, rawJSON, key []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString(rawJSON)
	body := prefix + "." + encoded
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig
}

func verifyOAuthCookiePayload(token, prefix string, key []byte) ([]byte, bool) {
	if len(key) == 0 || token == "" {
		return nil, false
	}
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return nil, false
	}
	body, sig := token[:i], token[i+1:]
	encoded, ok := strings.CutPrefix(body, prefix+".")
	if !ok || encoded == "" {
		return nil, false
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	if !hmac.Equal(gotSig, mac.Sum(nil)) {
		return nil, false
	}
	rawJSON, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	return rawJSON, true
}

func mintOAuthSessionToken(p oauthSessionPayload, key []byte) (string, error) {
	if len(key) == 0 {
		return "", errors.New("missing OAuth session signing key")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return signOAuthCookiePayload("v1", raw, key), nil
}

func verifyOAuthSession(token string, key []byte, now time.Time) (oauthSessionPayload, bool) {
	raw, ok := verifyOAuthCookiePayload(token, "v1", key)
	if !ok {
		return oauthSessionPayload{}, false
	}
	var p oauthSessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return oauthSessionPayload{}, false
	}
	if p.Email == "" || p.ExpiresAt <= 0 {
		return oauthSessionPayload{}, false
	}
	if now.Unix() > p.ExpiresAt {
		return oauthSessionPayload{}, false
	}
	if p.IssuedAt > now.Add(5*time.Minute).Unix() {
		return oauthSessionPayload{}, false
	}
	return p, true
}
