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
	registryKeysTTL          = time.Hour
	registryKeysRefetchFloor = time.Minute
	registryClockSkew        = 30 * time.Second
	registryMaxLifetime      = 5 * time.Minute
)

type registryConfigContextKey struct{}

// RegistryConfig holds request-level or server-level registry JWT verification
// configuration (-ui-hub-issuer, -ui-hub-audience, -ui-hub-keys-url).
type RegistryConfig struct {
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
func (c RegistryConfig) Enabled() bool {
	return strings.TrimSpace(c.Issuer) != "" &&
		strings.TrimSpace(c.Audience) != "" &&
		strings.TrimSpace(c.KeysURL) != ""
}

// AnySet reports whether any of the three registry verification settings is non-empty.
func (c RegistryConfig) AnySet() bool {
	return strings.TrimSpace(c.Issuer) != "" ||
		strings.TrimSpace(c.Audience) != "" ||
		strings.TrimSpace(c.KeysURL) != ""
}

// WithRegistryConfig attaches a RegistryConfig to ctx.
func WithRegistryConfig(ctx context.Context, cfg RegistryConfig) context.Context {
	return context.WithValue(ctx, registryConfigContextKey{}, cfg)
}

// RegistryConfigFromContext extracts RegistryConfig from ctx if present.
func RegistryConfigFromContext(ctx context.Context) (RegistryConfig, bool) {
	if ctx == nil {
		return RegistryConfig{}, false
	}
	cfg, ok := ctx.Value(registryConfigContextKey{}).(RegistryConfig)
	return cfg, ok
}

// RegistryClaims represents the JWT claims contained in an
// X-Grain-Registry-Assertion JWT minted by grain-registry.
type RegistryClaims struct {
	jwt.RegisteredClaims
	Email         string          `json:"email,omitempty"`
	EmailVerified *bool           `json:"email_verified,omitempty"`
	Kind          PrincipalKind   `json:"kind,omitempty"`
	Act           json.RawMessage `json:"act,omitempty"`
	RequestID     string          `json:"rid,omitempty"`
}

// RegistryValidator validates a registry JWT assertion token string for a
// target audience.
type RegistryValidator func(ctx context.Context, tokenStr string, audience string) (*RegistryClaims, error)

type registryKeySet struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

type registryUnavailableError struct {
	err error
}

func (e registryUnavailableError) Error() string {
	return e.err.Error()
}

func (e registryUnavailableError) Unwrap() error {
	return e.err
}

// Unavailable implements ui.VerifierUnavailableError so an initial JWKS fetch
// failure before any keys are cached returns 503 Service Unavailable with
// Retry-After: 5 rather than 401 Unauthorized.
func (e registryUnavailableError) Unavailable() bool {
	return true
}

var (
	registryMu           sync.RWMutex
	registryIssuer       string
	registryAudience     string
	registryKeysURL      string
	registryKeySets      map[string]*registryKeySet
	registryHTTPClient   = &http.Client{Timeout: 5 * time.Second}
	registryRefetchFloor = registryKeysRefetchFloor
)

// SetRegistryIssuer sets the expected issuer for registry JWT verification.
func SetRegistryIssuer(issuer string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registryIssuer = strings.TrimSpace(issuer)
}

// GetRegistryIssuer returns the configured issuer for registry JWT verification.
func GetRegistryIssuer() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if registryIssuer != "" {
		return registryIssuer
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_ISSUER"))
}

// SetRegistryAudience sets the expected audience for registry JWT verification.
func SetRegistryAudience(aud string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registryAudience = strings.TrimSpace(aud)
}

// GetRegistryAudience returns the configured audience for registry JWT verification.
func GetRegistryAudience() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if registryAudience != "" {
		return registryAudience
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_AUDIENCE"))
}

// SetRegistryKeysURL sets the JWKS endpoint URL for registry JWT verification.
func SetRegistryKeysURL(u string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registryKeysURL = strings.TrimSpace(u)
}

// GetRegistryKeysURL returns the configured JWKS endpoint URL for registry JWT verification.
func GetRegistryKeysURL() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if registryKeysURL != "" {
		return registryKeysURL
	}
	return strings.TrimSpace(hubenv.Get("GRAIN_HUB_KEYS_URL"))
}

// SetRegistryHTTPClient sets a custom http.Client for fetching registry JWKS keys.
func SetRegistryHTTPClient(client *http.Client) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if client != nil {
		registryHTTPClient = client
	} else {
		registryHTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
}

// SetRegistryRefetchFloor overrides the minimum interval between JWKS refetches
// triggered by an unknown key ID. A non-positive duration restores the 1-minute default.
func SetRegistryRefetchFloor(d time.Duration) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if d > 0 {
		registryRefetchFloor = d
	} else {
		registryRefetchFloor = registryKeysRefetchFloor
	}
}

// ResetRegistryConfig resets all registry package-level configurations and key caches to defaults.
func ResetRegistryConfig() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registryIssuer = ""
	registryAudience = ""
	registryKeysURL = ""
	registryKeySets = nil
	registryHTTPClient = &http.Client{Timeout: 5 * time.Second}
	registryRefetchFloor = registryKeysRefetchFloor
}

// RegistryIssuerFor returns the expected registry JWT issuer active for ctx.
func RegistryIssuerFor(ctx context.Context) string {
	if cfg, ok := RegistryConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Issuer) != "" {
		return strings.TrimSpace(cfg.Issuer)
	}
	return GetRegistryIssuer()
}

// RegistryAudienceFor returns the expected registry JWT audience active for ctx.
func RegistryAudienceFor(ctx context.Context) string {
	if cfg, ok := RegistryConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Audience) != "" {
		return strings.TrimSpace(cfg.Audience)
	}
	return GetRegistryAudience()
}

// RegistryKeysURLFor returns the registry JWKS URL active for ctx.
func RegistryKeysURLFor(ctx context.Context) string {
	if cfg, ok := RegistryConfigFromContext(ctx); ok && strings.TrimSpace(cfg.KeysURL) != "" {
		return strings.TrimSpace(cfg.KeysURL)
	}
	return GetRegistryKeysURL()
}

// EffectiveRegistryConfig returns the resolved RegistryConfig for ctx.
func EffectiveRegistryConfig(ctx context.Context) RegistryConfig {
	return RegistryConfig{
		Issuer:   RegistryIssuerFor(ctx),
		Audience: RegistryAudienceFor(ctx),
		KeysURL:  RegistryKeysURLFor(ctx),
	}
}

func getRegistryPublicKey(ctx context.Context, client *http.Client, keysURL, kid string) (crypto.PublicKey, error) {
	keysURL = strings.TrimSpace(keysURL)
	if keysURL == "" {
		return nil, errors.New("no registry keys URL is configured (GRAIN_REGISTRY_KEYS_URL / -ui-hub-keys-url), so no assertion can be verified")
	}
	registryMu.RLock()
	set := registryKeySets[keysURL]
	defaultClient := registryHTTPClient
	refetchFloor := registryRefetchFloor
	registryMu.RUnlock()
	if set != nil {
		if key, ok := set.keys[kid]; ok && time.Since(set.fetched) < registryKeysTTL {
			return key, nil
		}
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	set = registryKeySets[keysURL]
	refetchFloor = registryRefetchFloor
	if set != nil {
		key, ok := set.keys[kid]
		if ok && time.Since(set.fetched) < registryKeysTTL {
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
		hc = registryHTTPClient
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
		return nil, registryUnavailableError{err: err}
	}
	if registryKeySets == nil {
		registryKeySets = map[string]*registryKeySet{}
	}
	registryKeySets[keysURL] = &registryKeySet{keys: newKeys, fetched: time.Now()}

	k, exists := newKeys[kid]
	if !exists {
		return nil, fmt.Errorf("unknown registry key ID %q", kid)
	}
	return k, nil
}

// ValidateRegistryToken verifies a registry JWT assertion string against
// expectedAudience (a single URL, https://<name>.<instance-domain>) and returns
// its verified claims.
//
// Signature verification pins the algorithm to ES256 on an ECDSA P-256 key
// (rejecting RS256, HS256, and none), requires exp and iat with 30s clock-skew
// leeway on exp/nbf/iat, enforces 0 < exp - iat <= 5m, checks exact iss and
// single-audience equality, and rejects any token carrying the reserved act
// claim until delegated-identity support is enabled.
func ValidateRegistryToken(ctx context.Context, tokenStr string, expectedAudience string) (*RegistryClaims, error) {
	return validateRegistryTokenWithClient(ctx, nil, tokenStr, expectedAudience)
}

func validateRegistryTokenWithClient(ctx context.Context, client *http.Client, tokenStr string, expectedAudience string) (*RegistryClaims, error) {
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
	issuer := RegistryIssuerFor(ctx)
	if issuer == "" {
		return nil, errors.New("no registry issuer is configured (GRAIN_REGISTRY_ISSUER / -ui-hub-issuer), so no assertion can be verified")
	}
	if strings.Contains(issuer, ",") {
		return nil, errors.New("registry issuer must be a single issuer, not a comma-separated list")
	}
	keysURL := RegistryKeysURLFor(ctx)
	if keysURL == "" {
		return nil, errors.New("no registry keys URL is configured (GRAIN_REGISTRY_KEYS_URL / -ui-hub-keys-url), so no assertion can be verified")
	}

	claims := &RegistryClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if t.Method == nil || t.Method.Alg() != jwt.SigningMethodES256.Alg() {
			return nil, fmt.Errorf("unexpected registry signing algorithm %v (want ES256)", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok || strings.TrimSpace(kid) == "" {
			return nil, errors.New("missing kid in registry JWT header")
		}
		key, err := getRegistryPublicKey(ctx, client, keysURL, kid)
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
		jwt.WithLeeway(registryClockSkew),
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
	if claims.IssuedAt.Time.After(time.Now().Add(registryClockSkew)) {
		return nil, errors.New("registry JWT iat is in the future")
	}
	lifetime := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	if lifetime <= 0 || lifetime > registryMaxLifetime {
		return nil, fmt.Errorf("invalid registry JWT lifetime %s (must be in (0, %s])", lifetime, registryMaxLifetime)
	}
	if _, err := IdentityFromRegistryClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

type registryActClaim struct {
	Sub    string `json:"sub"`
	Kind   string `json:"kind"`
	App    string `json:"app,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Access string `json:"access"`
}

func parseRegistryActClaim(raw json.RawMessage, rawTopKind PrincipalKind, topEmail, topSub, jti string) (*DelegatingApp, error) {
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
	var act registryActClaim
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

// IdentityFromRegistryClaims extracts and normalizes the VerifiedIdentity from
// verified RegistryClaims.
func IdentityFromRegistryClaims(claims *RegistryClaims) (VerifiedIdentity, error) {
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
	via, err := parseRegistryActClaim(claims.Act, rawKind, email, sub, claims.ID)
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

// VerifyRegistryToken validates a registry JWT assertion against the audience
// active on ctx and returns the normalized authenticated email.
func VerifyRegistryToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty registry assertion token")
	}
	claims, err := ValidateRegistryToken(ctx, tokenStr, RegistryAudienceFor(ctx))
	if err != nil {
		return "", err
	}
	id, err := IdentityFromRegistryClaims(claims)
	if err != nil {
		return "", err
	}
	return id.Email, nil
}

// RegistryVerifierOption configures a RegistryVerifier created by NewRegistryVerifier.
type RegistryVerifierOption func(*RegistryVerifier)

// WithRegistryValidator overrides JWT validation on a RegistryVerifier.
func WithRegistryValidator(v RegistryValidator) RegistryVerifierOption {
	return func(rv *RegistryVerifier) {
		rv.validator = v
	}
}

// WithRegistryAudience pins an expected single-URL audience on a
// RegistryVerifier, taking precedence over request/package defaults.
func WithRegistryAudience(audience string) RegistryVerifierOption {
	return func(rv *RegistryVerifier) {
		rv.audience = strings.TrimSpace(audience)
	}
}

// WithRegistryIssuer pins the expected JWT issuer on a RegistryVerifier.
func WithRegistryIssuer(issuer string) RegistryVerifierOption {
	return func(rv *RegistryVerifier) {
		rv.issuer = strings.TrimSpace(issuer)
	}
}

// WithRegistryKeysURL pins the JWKS endpoint URL on a RegistryVerifier.
func WithRegistryKeysURL(keysURL string) RegistryVerifierOption {
	return func(rv *RegistryVerifier) {
		rv.keysURL = strings.TrimSpace(keysURL)
	}
}

// WithRegistryVerifierHTTPClient pins an HTTP client for fetching JWKS keys on
// a RegistryVerifier.
func WithRegistryVerifierHTTPClient(client *http.Client) RegistryVerifierOption {
	return func(rv *RegistryVerifier) {
		rv.httpClient = client
	}
}

// RegistryVerifier verifies X-Grain-Registry-Assertion headers on incoming HTTP requests.
type RegistryVerifier struct {
	validator  RegistryValidator
	audience   string
	issuer     string
	keysURL    string
	httpClient *http.Client
}

// NewRegistryVerifier constructs a RegistryVerifier with the supplied options.
func NewRegistryVerifier(opts ...RegistryVerifierOption) *RegistryVerifier {
	rv := &RegistryVerifier{}
	for _, opt := range opts {
		if opt != nil {
			opt(rv)
		}
	}
	return rv
}

// Name returns the canonical verifier name ("registry").
func (v *RegistryVerifier) Name() string { return "registry" }

// Headers returns the HTTP header names inspected by RegistryVerifier.
func (v *RegistryVerifier) Headers() []string { return HubAssertionHeaders }

// AccessCodeWins returns false: a valid registry assertion takes precedence
// over an access code on the same request.
func (v *RegistryVerifier) AccessCodeWins() bool { return false }

// Validator returns the custom RegistryValidator configured on v, if any.
func (v *RegistryVerifier) Validator() RegistryValidator { return v.validator }

// Audience returns the expected audience for ctx, preferring v's pinned audience.
func (v *RegistryVerifier) Audience(ctx context.Context) string {
	if v != nil && v.audience != "" {
		return v.audience
	}
	return RegistryAudienceFor(ctx)
}

// Issuer returns the expected issuer for ctx, preferring v's pinned issuer.
func (v *RegistryVerifier) Issuer(ctx context.Context) string {
	if v != nil && v.issuer != "" {
		return v.issuer
	}
	return RegistryIssuerFor(ctx)
}

// KeysURL returns the JWKS URL for ctx, preferring v's pinned keysURL.
func (v *RegistryVerifier) KeysURL(ctx context.Context) string {
	if v != nil && v.keysURL != "" {
		return v.keysURL
	}
	return RegistryKeysURLFor(ctx)
}

// Enabled reports whether v is configured to verify requests in ctx (either via
// a custom validator or with non-empty issuer, audience, and keys URL).
func (v *RegistryVerifier) Enabled(ctx context.Context) bool {
	if v != nil && v.validator != nil {
		return true
	}
	return strings.TrimSpace(v.Issuer(ctx)) != "" &&
		strings.TrimSpace(v.Audience(ctx)) != "" &&
		strings.TrimSpace(v.KeysURL(ctx)) != ""
}

// ContextWithOverrides applies v's pinned Issuer, Audience, and KeysURL overrides onto ctx.
func (v *RegistryVerifier) ContextWithOverrides(ctx context.Context) context.Context {
	if v == nil || (v.issuer == "" && v.audience == "" && v.keysURL == "") {
		return ctx
	}
	cfg, _ := RegistryConfigFromContext(ctx)
	if v.issuer != "" {
		cfg.Issuer = v.issuer
	}
	if v.audience != "" {
		cfg.Audience = v.audience
	}
	if v.keysURL != "" {
		cfg.KeysURL = v.keysURL
	}
	return WithRegistryConfig(ctx, cfg)
}

// VerifyToken validates tokenStr using v's configuration and ctx, returning the
// verified claims and the effective expected audience string.
func (v *RegistryVerifier) VerifyToken(ctx context.Context, tokenStr string) (*RegistryClaims, string, error) {
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
	claims, err := validateRegistryTokenWithClient(ctx, hc, tokenStr, aud)
	return claims, aud, err
}

// VerifyRequest checks r's X-Grain-Registry-Assertion header. It returns
// ok=false with a nil error when the header is absent, and an error when the
// header is present but fails verification.
func (v *RegistryVerifier) VerifyRequest(r *http.Request) (VerifiedIdentity, bool, error) {
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
	id, err := IdentityFromRegistryClaims(claims)
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
func (v *RegistryVerifier) Verify(r *http.Request) (VerifiedIdentity, error) {
	id, ok, err := v.VerifyRequest(r)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	if !ok {
		return VerifiedIdentity{}, fmt.Errorf("missing %s header", HubAssertionHeader)
	}
	return id, nil
}
