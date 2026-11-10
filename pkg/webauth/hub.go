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

package webauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/hubenv"

	"github.com/ai-on-gke/grain/pkg/jwks"
	"github.com/golang-jwt/jwt/v5"
)

// RegistryAssertionHeader is the HTTP request header holding a short-lived
// ES256 JWT assertion minted by grain-registry when proxying a request to a
// Grain instance.
const HubAssertionHeader = "X-Grain-Hub-Assertion"

// RegistryAssertionHeader is HubAssertionHeader's name from before the
// Grain Registry was renamed the Grain Hub. For a release the hub sends
// both and a daemon reads either (HubAssertion), so a daemon and a hub
// either side of the rename still understand each other.
const RegistryAssertionHeader = "X-Grain-Registry-Assertion"

// HubAssertionHeaders are both names the assertion travels under: what a
// verifier reads, and what anything stripping a caller's own identity
// headers must strip.
var HubAssertionHeaders = []string{HubAssertionHeader, RegistryAssertionHeader}

// HubAssertion is the assertion h carries, under its new name or its old.
func HubAssertion(h http.Header) string {
	if v := strings.TrimSpace(h.Get(HubAssertionHeader)); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get(RegistryAssertionHeader))
}

// hubAssertionToken extracts the single unambiguous assertion JWT from h.
// During a rolling upgrade a pre-rename grain-registry strips only
// X-Grain-Registry-Assertion and sets only X-Grain-Registry-Assertion, while a
// post-rename grain-hub strips both and sets both headers to the identical JWT.
// If both headers are non-empty with different values (or either carries
// multiple distinct values), a caller smuggled X-Grain-Hub-Assertion past an
// un-upgraded proxy; fail closed rather than trusting the smuggled header.
func hubAssertionToken(h http.Header) (string, error) {
	hubVal, err := singleAssertionValue(h, HubAssertionHeader)
	if err != nil {
		return "", err
	}
	regVal, err := singleAssertionValue(h, RegistryAssertionHeader)
	if err != nil {
		return "", err
	}
	if hubVal != "" && regVal != "" && hubVal != regVal {
		return "", fmt.Errorf("conflicting %s and %s headers", HubAssertionHeader, RegistryAssertionHeader)
	}
	if hubVal != "" {
		return hubVal, nil
	}
	return regVal, nil
}

func singleAssertionValue(h http.Header, name string) (string, error) {
	var found string
	for _, raw := range h.Values(name) {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if found != "" && found != v {
			return "", fmt.Errorf("multiple %s headers with differing values", name)
		}
		found = v
	}
	return found, nil
}

const (
	hubKeysTTL          = time.Hour
	hubKeysRefetchFloor = time.Minute
	hubClockSkew        = 30 * time.Second
	hubMaxLifetime      = 5 * time.Minute
)

type hubConfigContextKey struct{}

// HubConfig holds request-level or server-level registry JWT verification
// configuration (-ui-hub-issuer, -ui-hub-audience, -ui-hub-keys-url).
type HubConfig struct {
	// Issuer is the exact iss claim a registry assertion must carry
	// (e.g. "https://grain.example.com").
	Issuer string
	// Audience is the single aud claim a registry assertion must carry
	// (e.g. "https://my-grain.grain.example.com").
	Audience string
	// KeysURL is the JWKS endpoint publishing the registry's ES256 public keys
	// (e.g. "https://grain.example.com/.well-known/jwks.json").
	KeysURL string
}

// Enabled reports whether all three registry verification settings are non-empty.
func (c HubConfig) Enabled() bool {
	return strings.TrimSpace(c.Issuer) != "" &&
		strings.TrimSpace(c.Audience) != "" &&
		strings.TrimSpace(c.KeysURL) != ""
}

// AnySet reports whether any of the three registry verification settings is non-empty.
func (c HubConfig) AnySet() bool {
	return strings.TrimSpace(c.Issuer) != "" ||
		strings.TrimSpace(c.Audience) != "" ||
		strings.TrimSpace(c.KeysURL) != ""
}

// WithHubConfig attaches a HubConfig to ctx.
func WithHubConfig(ctx context.Context, cfg HubConfig) context.Context {
	return context.WithValue(ctx, hubConfigContextKey{}, cfg)
}

// HubConfigFromContext extracts HubConfig from ctx if present.
func HubConfigFromContext(ctx context.Context) (HubConfig, bool) {
	if ctx == nil {
		return HubConfig{}, false
	}
	cfg, ok := ctx.Value(hubConfigContextKey{}).(HubConfig)
	return cfg, ok
}

// HubClaims represents the JWT claims contained in an
// X-Grain-Registry-Assertion JWT minted by grain-registry.
type HubClaims struct {
	jwt.RegisteredClaims
	Email         string          `json:"email,omitempty"`
	EmailVerified *bool           `json:"email_verified,omitempty"`
	Kind          PrincipalKind   `json:"kind,omitempty"`
	Act           json.RawMessage `json:"act,omitempty"`
	RequestID     string          `json:"rid,omitempty"`
}

// HubValidator validates a registry JWT assertion token string for a
// target audience.
type HubValidator func(ctx context.Context, tokenStr string, audience string) (*HubClaims, error)

type hubKeySet struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

type hubUnavailableError struct {
	err error
}

func (e hubUnavailableError) Error() string {
	return e.err.Error()
}

func (e hubUnavailableError) Unwrap() error {
	return e.err
}

// Unavailable implements ui.VerifierUnavailableError so an initial JWKS fetch
// failure before any keys are cached returns 503 Service Unavailable with
// Retry-After: 5 rather than 401 Unauthorized.
func (e hubUnavailableError) Unavailable() bool {
	return true
}

var (
	hubMu           sync.RWMutex
	hubIssuer       string
	hubAudience     string
	hubKeysURL      string
	hubKeySets      map[string]*hubKeySet
	hubHTTPClient   = &http.Client{Timeout: 5 * time.Second}
	hubRefetchFloor = hubKeysRefetchFloor
)

// SetHubIssuer sets the expected issuer for registry JWT verification.
func SetHubIssuer(issuer string) {
	hubMu.Lock()
	defer hubMu.Unlock()
	hubIssuer = strings.TrimSpace(issuer)
}

// GetHubIssuer returns the configured issuer for registry JWT verification.
func GetHubIssuer() string {
	hubMu.RLock()
	defer hubMu.RUnlock()
	if hubIssuer != "" {
		return hubIssuer
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_ISSUER"))
}

// SetHubAudience sets the expected audience for registry JWT verification.
func SetHubAudience(aud string) {
	hubMu.Lock()
	defer hubMu.Unlock()
	hubAudience = strings.TrimSpace(aud)
}

// GetHubAudience returns the configured audience for registry JWT verification.
func GetHubAudience() string {
	hubMu.RLock()
	defer hubMu.RUnlock()
	if hubAudience != "" {
		return hubAudience
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_AUDIENCE"))
}

// SetHubKeysURL sets the JWKS endpoint URL for registry JWT verification.
func SetHubKeysURL(u string) {
	hubMu.Lock()
	defer hubMu.Unlock()
	hubKeysURL = strings.TrimSpace(u)
}

// GetHubKeysURL returns the configured JWKS endpoint URL for registry JWT verification.
func GetHubKeysURL() string {
	hubMu.RLock()
	defer hubMu.RUnlock()
	if hubKeysURL != "" {
		return hubKeysURL
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_KEYS_URL"))
}

// SetHubHTTPClient sets a custom http.Client for fetching registry JWKS keys.
func SetHubHTTPClient(client *http.Client) {
	hubMu.Lock()
	defer hubMu.Unlock()
	if client != nil {
		hubHTTPClient = client
	} else {
		hubHTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
}

// SetHubRefetchFloor overrides the minimum interval between JWKS refetches
// triggered by an unknown key ID. A non-positive duration restores the 1-minute default.
func SetHubRefetchFloor(d time.Duration) {
	hubMu.Lock()
	defer hubMu.Unlock()
	if d > 0 {
		hubRefetchFloor = d
	} else {
		hubRefetchFloor = hubKeysRefetchFloor
	}
}

// ResetHubConfig resets all registry package-level configurations and key caches to defaults.
func ResetHubConfig() {
	hubMu.Lock()
	defer hubMu.Unlock()
	hubIssuer = ""
	hubAudience = ""
	hubKeysURL = ""
	hubKeySets = nil
	hubHTTPClient = &http.Client{Timeout: 5 * time.Second}
	hubRefetchFloor = hubKeysRefetchFloor
}

// HubIssuerFor returns the expected registry JWT issuer active for ctx.
func HubIssuerFor(ctx context.Context) string {
	if cfg, ok := HubConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Issuer) != "" {
		return strings.TrimSpace(cfg.Issuer)
	}
	return GetHubIssuer()
}

// HubAudienceFor returns the expected registry JWT audience active for ctx.
func HubAudienceFor(ctx context.Context) string {
	if cfg, ok := HubConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Audience) != "" {
		return strings.TrimSpace(cfg.Audience)
	}
	return GetHubAudience()
}

// HubKeysURLFor returns the registry JWKS URL active for ctx.
func HubKeysURLFor(ctx context.Context) string {
	if cfg, ok := HubConfigFromContext(ctx); ok && strings.TrimSpace(cfg.KeysURL) != "" {
		return strings.TrimSpace(cfg.KeysURL)
	}
	return GetHubKeysURL()
}

// EffectiveHubConfig returns the resolved HubConfig for ctx.
func EffectiveHubConfig(ctx context.Context) HubConfig {
	return HubConfig{
		Issuer:   HubIssuerFor(ctx),
		Audience: HubAudienceFor(ctx),
		KeysURL:  HubKeysURLFor(ctx),
	}
}

func getHubPublicKey(ctx context.Context, client *http.Client, keysURL, kid string) (crypto.PublicKey, error) {
	keysURL = strings.TrimSpace(keysURL)
	if keysURL == "" {
		return nil, errors.New("no registry keys URL is configured (GRAIN_REGISTRY_KEYS_URL / -ui-hub-keys-url), so no assertion can be verified")
	}
	hubMu.RLock()
	set := hubKeySets[keysURL]
	defaultClient := hubHTTPClient
	refetchFloor := hubRefetchFloor
	hubMu.RUnlock()
	if set != nil {
		if key, ok := set.keys[kid]; ok && time.Since(set.fetched) < hubKeysTTL {
			return key, nil
		}
	}

	hubMu.Lock()
	defer hubMu.Unlock()

	set = hubKeySets[keysURL]
	refetchFloor = hubRefetchFloor
	if set != nil {
		key, ok := set.keys[kid]
		if ok && time.Since(set.fetched) < hubKeysTTL {
			return key, nil
		}
		if time.Since(set.fetched) < refetchFloor {
			if ok {
				return key, nil
			}
			return nil, fmt.Errorf("unknown registry key ID %q", kid)
		}
	}

	hc := client
	if hc == nil {
		hc = hubHTTPClient
		if hc == nil {
			hc = defaultClient
		}
	}
	newKeys, err := jwks.Fetch(ctx, hc, keysURL, "registry")
	if err != nil {
		if set != nil {
			if key, ok := set.keys[kid]; ok {
				return key, nil
			}
			return nil, err
		}
		return nil, hubUnavailableError{err: err}
	}
	if hubKeySets == nil {
		hubKeySets = map[string]*hubKeySet{}
	}
	hubKeySets[keysURL] = &hubKeySet{keys: newKeys, fetched: time.Now()}

	k, exists := newKeys[kid]
	if !exists {
		return nil, fmt.Errorf("unknown registry key ID %q", kid)
	}
	return k, nil
}

// ValidateHubToken verifies a registry JWT assertion string against
// expectedAudience (a single URL, https://<name>.<instance-domain>) and returns
// its verified claims.
//
// Signature verification pins the algorithm to ES256 on an ECDSA P-256 key
// (rejecting RS256, HS256, and none), requires exp and iat with 30s clock-skew
// leeway on exp/nbf/iat, enforces 0 < exp - iat <= 5m, checks exact iss and
// single-audience equality, and rejects any token carrying the reserved act
// claim until delegated-identity support is enabled.
func ValidateHubToken(ctx context.Context, tokenStr string, expectedAudience string) (*HubClaims, error) {
	return validateHubTokenWithClient(ctx, nil, tokenStr, expectedAudience)
}

func validateHubTokenWithClient(ctx context.Context, client *http.Client, tokenStr string, expectedAudience string) (*HubClaims, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return nil, errors.New("empty registry assertion token")
	}
	expectedAudience = strings.TrimSpace(expectedAudience)
	if expectedAudience == "" {
		return nil, errors.New("no registry audience is configured (GRAIN_REGISTRY_AUDIENCE / -ui-hub-audience), so no assertion can be verified")
	}
	if strings.Contains(expectedAudience, ",") {
		return nil, errors.New("registry audience must be a single URL, not a comma-separated list")
	}
	issuer := HubIssuerFor(ctx)
	if issuer == "" {
		return nil, errors.New("no registry issuer is configured (GRAIN_REGISTRY_ISSUER / -ui-hub-issuer), so no assertion can be verified")
	}
	if strings.Contains(issuer, ",") {
		return nil, errors.New("registry issuer must be a single issuer, not a comma-separated list")
	}
	keysURL := HubKeysURLFor(ctx)
	if keysURL == "" {
		return nil, errors.New("no registry keys URL is configured (GRAIN_REGISTRY_KEYS_URL / -ui-hub-keys-url), so no assertion can be verified")
	}

	claims := &HubClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if t.Method == nil || t.Method.Alg() != jwt.SigningMethodES256.Alg() {
			return nil, fmt.Errorf("unexpected registry signing algorithm %v (want ES256)", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok || strings.TrimSpace(kid) == "" {
			return nil, errors.New("missing kid in registry JWT header")
		}
		key, err := getHubPublicKey(ctx, client, keysURL, kid)
		if err != nil {
			return nil, err
		}
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok || ecKey == nil || ecKey.Curve != elliptic.P256() {
			return nil, errors.New("unexpected registry key type (expected ECDSA P-256)")
		}
		return ecKey, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(hubClockSkew),
		jwt.WithIssuer(issuer),
	)
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid registry JWT assertion: %w", err)
	}

	if claims.Issuer != issuer {
		return nil, fmt.Errorf("registry issuer mismatch: expected %q, got %q", issuer, claims.Issuer)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != expectedAudience {
		return nil, fmt.Errorf("registry audience mismatch: expected single audience %q, got %v", expectedAudience, claims.Audience)
	}
	if claims.ExpiresAt == nil {
		return nil, errors.New("missing exp in registry JWT claims")
	}
	if claims.IssuedAt == nil {
		return nil, errors.New("missing iat in registry JWT claims")
	}
	if claims.IssuedAt.Time.After(time.Now().Add(hubClockSkew)) {
		return nil, errors.New("registry JWT iat is in the future")
	}
	lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	if lifetime <= 0 || lifetime > hubMaxLifetime {
		return nil, fmt.Errorf("invalid registry JWT lifetime %s (must be in (0, %s])", lifetime, hubMaxLifetime)
	}
	if _, err := IdentityFromHubClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

type hubActClaim struct {
	Sub    string `json:"sub"`
	Kind   string `json:"kind"`
	App    string `json:"app,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Access string `json:"access"`
}

func parseHubActClaim(raw json.RawMessage, rawTopKind PrincipalKind, topEmail, topSub, jti string) (*DelegatingApp, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return nil, errors.New("registry JWT act claim must be a JSON object")
	}
	if _, hasNested := obj["act"]; hasNested {
		return nil, errors.New("registry JWT carries nested act.act claim: chained delegation is not supported")
	}
	var act hubActClaim
	if err := json.Unmarshal(trimmed, &act); err != nil {
		return nil, fmt.Errorf("malformed registry JWT act claim: %w", err)
	}
	if strings.TrimSpace(act.Kind) != string(PrincipalService) {
		return nil, fmt.Errorf("registry JWT act claim: act.kind %q must be %q", act.Kind, PrincipalService)
	}
	actEmail := NormalizeIdentityEmail(act.Sub)
	if actEmail == "" || !strings.Contains(actEmail, "@") || PrincipalKindForEmail(actEmail) != PrincipalService {
		return nil, fmt.Errorf("registry JWT act claim: act.sub %q must be a service account email", act.Sub)
	}
	access := strings.TrimSpace(act.Access)
	if access != "" && access != "read" && access != "write" {
		return nil, fmt.Errorf("registry JWT act claim: act.access %q must be \"read\" or \"write\"", act.Access)
	}
	if rawTopKind != PrincipalHuman {
		return nil, fmt.Errorf("registry JWT carrying act claim requires top-level kind %q, got %q", PrincipalHuman, rawTopKind)
	}
	if PrincipalKindForEmail(topEmail) != PrincipalHuman || (topSub != "" && PrincipalKindForEmail(topSub) != PrincipalHuman) {
		return nil, fmt.Errorf("registry JWT carrying act claim requires human top-level subject, got %q", topEmail)
	}
	return &DelegatingApp{
		Email:  actEmail,
		Name:   strings.TrimSpace(act.App),
		Grant:  strings.TrimSpace(act.Grant),
		Access: access,
		JTI:    strings.TrimSpace(jti),
	}, nil
}

// IdentityFromHubClaims extracts and normalizes the VerifiedIdentity from
// verified HubClaims.
func IdentityFromHubClaims(claims *HubClaims) (VerifiedIdentity, error) {
	if claims == nil {
		return VerifiedIdentity{}, errors.New("nil registry claims")
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return VerifiedIdentity{}, errors.New("registry token email_verified is false")
	}
	sub := NormalizeIdentityEmail(claims.Subject)
	email := NormalizeIdentityEmail(claims.Email)
	if sub != "" && email != "" && sub != email {
		return VerifiedIdentity{}, fmt.Errorf("registry token sub %q and email %q do not match", sub, email)
	}
	if email == "" {
		email = sub
	}
	if sub == "" {
		sub = email
	}
	if email == "" {
		return VerifiedIdentity{}, errors.New("no email or subject found in verified registry token claims")
	}
	inferredKind := PrincipalKindForEmail(email)
	rawKind := PrincipalKind(strings.ToLower(strings.TrimSpace(string(claims.Kind))))
	var kind PrincipalKind
	switch rawKind {
	case "":
		kind = inferredKind
	case PrincipalHuman:
		if inferredKind == PrincipalService {
			return VerifiedIdentity{}, fmt.Errorf("service account email %q cannot claim principal kind %q", email, PrincipalHuman)
		}
		kind = PrincipalHuman
	case PrincipalService:
		kind = PrincipalService
	default:
		return VerifiedIdentity{}, fmt.Errorf("unsupported registry principal kind %q", claims.Kind)
	}
	via, err := parseHubActClaim(claims.Act, rawKind, email, sub, claims.ID)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	if via != nil && via.RequestID == "" {
		via.RequestID = strings.TrimSpace(claims.RequestID)
	}
	return VerifiedIdentity{
		Email: email,
		Sub:   sub,
		Kind:  kind,
		Via:   via,
	}, nil
}

// VerifyHubToken validates a registry JWT assertion against the audience
// active on ctx and returns the normalized authenticated email.
func VerifyHubToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty registry assertion token")
	}
	claims, err := ValidateHubToken(ctx, tokenStr, HubAudienceFor(ctx))
	if err != nil {
		return "", err
	}
	id, err := IdentityFromHubClaims(claims)
	if err != nil {
		return "", err
	}
	return id.Email, nil
}

// HubVerifierOption configures a HubVerifier created by NewHubVerifier.
type HubVerifierOption func(*HubVerifier)

// WithHubValidator overrides JWT validation on a HubVerifier.
func WithHubValidator(v HubValidator) HubVerifierOption {
	return func(rv *HubVerifier) {
		rv.validator = v
	}
}

// WithHubAudience pins an expected single-URL audience on a
// HubVerifier, taking precedence over request/package defaults.
func WithHubAudience(audience string) HubVerifierOption {
	return func(rv *HubVerifier) {
		rv.audience = strings.TrimSpace(audience)
	}
}

// WithHubIssuer pins the expected JWT issuer on a HubVerifier.
func WithHubIssuer(issuer string) HubVerifierOption {
	return func(rv *HubVerifier) {
		rv.issuer = strings.TrimSpace(issuer)
	}
}

// WithHubKeysURL pins the JWKS endpoint URL on a HubVerifier.
func WithHubKeysURL(keysURL string) HubVerifierOption {
	return func(rv *HubVerifier) {
		rv.keysURL = strings.TrimSpace(keysURL)
	}
}

// WithHubVerifierHTTPClient pins an HTTP client for fetching JWKS keys on
// a HubVerifier.
func WithHubVerifierHTTPClient(client *http.Client) HubVerifierOption {
	return func(rv *HubVerifier) {
		rv.httpClient = client
	}
}

// HubVerifier verifies X-Grain-Registry-Assertion headers on incoming HTTP requests.
type HubVerifier struct {
	validator  HubValidator
	audience   string
	issuer     string
	keysURL    string
	httpClient *http.Client
}

// NewHubVerifier constructs a HubVerifier with the supplied options.
func NewHubVerifier(opts ...HubVerifierOption) *HubVerifier {
	rv := &HubVerifier{}
	for _, opt := range opts {
		if opt != nil {
			opt(rv)
		}
	}
	return rv
}

// Name returns the canonical verifier name ("registry").
func (v *HubVerifier) Name() string { return "registry" }

// Headers returns the HTTP header names inspected by HubVerifier.
func (v *HubVerifier) Headers() []string { return HubAssertionHeaders }

// AccessCodeWins returns false: a valid registry assertion takes precedence
// over an access code on the same request.
func (v *HubVerifier) AccessCodeWins() bool { return false }

// Validator returns the custom HubValidator configured on v, if any.
func (v *HubVerifier) Validator() HubValidator { return v.validator }

// Audience returns the expected audience for ctx, preferring v's pinned audience.
func (v *HubVerifier) Audience(ctx context.Context) string {
	if v != nil && v.audience != "" {
		return v.audience
	}
	return HubAudienceFor(ctx)
}

// Issuer returns the expected issuer for ctx, preferring v's pinned issuer.
func (v *HubVerifier) Issuer(ctx context.Context) string {
	if v != nil && v.issuer != "" {
		return v.issuer
	}
	return HubIssuerFor(ctx)
}

// KeysURL returns the JWKS URL for ctx, preferring v's pinned keysURL.
func (v *HubVerifier) KeysURL(ctx context.Context) string {
	if v != nil && v.keysURL != "" {
		return v.keysURL
	}
	return HubKeysURLFor(ctx)
}

// Enabled reports whether v is configured to verify requests in ctx (either via
// a custom validator or with non-empty issuer, audience, and keys URL).
func (v *HubVerifier) Enabled(ctx context.Context) bool {
	if v != nil && v.validator != nil {
		return true
	}
	return strings.TrimSpace(v.Issuer(ctx)) != "" &&
		strings.TrimSpace(v.Audience(ctx)) != "" &&
		strings.TrimSpace(v.KeysURL(ctx)) != ""
}

// ContextWithOverrides applies v's pinned Issuer, Audience, and KeysURL overrides onto ctx.
func (v *HubVerifier) ContextWithOverrides(ctx context.Context) context.Context {
	if v == nil || (v.issuer == "" && v.audience == "" && v.keysURL == "") {
		return ctx
	}
	cfg, _ := HubConfigFromContext(ctx)
	if v.issuer != "" {
		cfg.Issuer = v.issuer
	}
	if v.audience != "" {
		cfg.Audience = v.audience
	}
	if v.keysURL != "" {
		cfg.KeysURL = v.keysURL
	}
	return WithHubConfig(ctx, cfg)
}

// VerifyToken validates tokenStr using v's configuration and ctx, returning the
// verified claims and the effective expected audience string.
func (v *HubVerifier) VerifyToken(ctx context.Context, tokenStr string) (*HubClaims, string, error) {
	ctx = v.ContextWithOverrides(ctx)
	aud := v.Audience(ctx)
	if v != nil && v.validator != nil {
		claims, err := v.validator(ctx, tokenStr, aud)
		return claims, aud, err
	}
	var hc *http.Client
	if v != nil {
		hc = v.httpClient
	}
	claims, err := validateHubTokenWithClient(ctx, hc, tokenStr, aud)
	return claims, aud, err
}

// VerifyRequest checks r's X-Grain-Registry-Assertion header. It returns
// ok=false with a nil error when the header is absent, and an error when the
// header is present but fails verification.
func (v *HubVerifier) VerifyRequest(r *http.Request) (VerifiedIdentity, bool, error) {
	if r == nil {
		return VerifiedIdentity{}, false, nil
	}
	tokenStr, err := hubAssertionToken(r.Header)
	if err != nil {
		return VerifiedIdentity{}, false, err
	}
	if tokenStr == "" {
		return VerifiedIdentity{}, false, nil
	}
	claims, _, err := v.VerifyToken(r.Context(), tokenStr)
	if err != nil {
		return VerifiedIdentity{}, false, err
	}
	id, err := IdentityFromHubClaims(claims)
	if err != nil {
		return VerifiedIdentity{}, false, err
	}
	if id.Via != nil {
		if hdrRID := strings.TrimSpace(r.Header.Get("X-Grain-Request-Id")); hdrRID != "" {
			id.Via.RequestID = hdrRID
		}
	}
	return id, true, nil
}

// Verify authenticates r via X-Grain-Registry-Assertion, returning an error if
// the header is missing or invalid.
func (v *HubVerifier) Verify(r *http.Request) (VerifiedIdentity, error) {
	id, ok, err := v.VerifyRequest(r)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	if !ok {
		return VerifiedIdentity{}, fmt.Errorf("missing %s header", HubAssertionHeader)
	}
	return id, nil
}
