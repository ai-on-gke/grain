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

// Package webauth provides shared browser and proxy authentication primitives
// used by both the grain daemon (pkg/ui) and grain-registry: Google Cloud IAP
// and grain-registry signed-header JWT verification, group-membership checking
// with pluggable directory checkers, and Fetch-Metadata / Origin CSRF checks.
//
// It intentionally has no dependency on pkg/model or pkg/ui so standalone
// binaries such as cmd/grain-registry can import it without pulling in daemon
// state packages.
package webauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/jwks"
	"github.com/golang-jwt/jwt/v5"
)

// PrincipalKind classifies an authenticated caller without importing pkg/model.
// Its string values match model.PrincipalHuman ("human") and
// model.PrincipalService ("service").
type PrincipalKind string

const (
	// PrincipalHuman identifies an interactive human user.
	PrincipalHuman PrincipalKind = "human"
	// PrincipalService identifies a non-human service account.
	PrincipalService PrincipalKind = "service"
)

// DelegatingApp describes the service principal that forwarded a request on
// behalf of a person through the registry (`act` claim in a registry JWT,
// design/design/authentication.design.md §6, AU-22).
type DelegatingApp struct {
	Email     string
	Name      string
	Grant     string
	Access    string
	JTI       string
	RequestID string
}

// VerifiedIdentity is an authenticated caller extracted from a signed proxy
// assertion header.
type VerifiedIdentity struct {
	Email string
	Sub   string
	Kind  PrincipalKind
	Via   *DelegatingApp
}

// IAPAssertionHeader is the HTTP request header holding the Google Cloud IAP JWT assertion.
const IAPAssertionHeader = "X-Goog-IAP-JWT-Assertion"

// GoogleIAPCertURL is the Google endpoint serving public keys in PEM format.
const GoogleIAPCertURL = "https://www.gstatic.com/iap/verify/public_key"

// GoogleIAPIssuer is the expected issuer claim in Google IAP JWT assertions.
const GoogleIAPIssuer = "https://cloud.google.com/iap"

const (
	iapKeysTTL          = time.Hour
	iapKeysRefetchFloor = 10 * time.Second
	iapClockSkew        = 30 * time.Second
)

type iapConfigContextKey struct{}

// IAPConfig holds request-level or server-level IAP JWT verification overrides.
type IAPConfig struct {
	// Audience is the audience a signed assertion must carry, or several
	// separated by commas. Empty falls back to package/environment defaults;
	// when still empty, no assertion verifies.
	Audience string
	// Issuer is the issuer a signed assertion must carry. Empty falls back to
	// $GRAIN_IAP_ISSUER and then GoogleIAPIssuer.
	Issuer string
	// KeysURL is where signing keys are published, as IAP's PEM map or a JWK
	// set. Empty falls back to $GRAIN_IAP_KEYS_URL and then GoogleIAPCertURL.
	KeysURL string
}

// WithIAPConfig attaches an IAPConfig to ctx.
func WithIAPConfig(ctx context.Context, cfg IAPConfig) context.Context {
	return context.WithValue(ctx, iapConfigContextKey{}, cfg)
}

// IAPConfigFromContext extracts IAPConfig from ctx if present.
func IAPConfigFromContext(ctx context.Context) (IAPConfig, bool) {
	if ctx == nil {
		return IAPConfig{}, false
	}
	cfg, ok := ctx.Value(iapConfigContextKey{}).(IAPConfig)
	return cfg, ok
}

// IAPClaims represents the JWT claims contained in a Google Cloud IAP JWT assertion.
type IAPClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email,omitempty"`
	HD    string `json:"hd,omitempty"`
}

// IAPValidator validates a Google Cloud IAP JWT assertion token string for a target audience.
type IAPValidator func(ctx context.Context, tokenStr string, audience string) (*IAPClaims, error)

type iapKeySet struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

var (
	iapMu            sync.RWMutex
	iapAudience      string
	iapIssuer        string
	iapKeySets       map[string]*iapKeySet
	iapHTTPClient    = &http.Client{Timeout: 5 * time.Second}
	googleIAPCertURL = GoogleIAPCertURL
)

// SetIAPAudience sets the expected audience for Google Cloud IAP JWT verification.
func SetIAPAudience(aud string) {
	iapMu.Lock()
	defer iapMu.Unlock()
	iapAudience = strings.TrimSpace(aud)
}

// GetIAPAudience returns the configured audience for Google Cloud IAP JWT verification.
func GetIAPAudience() string {
	iapMu.RLock()
	defer iapMu.RUnlock()
	if iapAudience != "" {
		return iapAudience
	}
	if aud := os.Getenv("GRAIN_IAP_AUDIENCE"); aud != "" {
		return strings.TrimSpace(aud)
	}
	if aud := os.Getenv("GRAIN_AUDIENCE"); aud != "" {
		return strings.TrimSpace(aud)
	}
	return strings.TrimSpace(os.Getenv("IAP_AUDIENCE"))
}

// SetIAPIssuer sets the expected issuer for Google Cloud IAP JWT verification.
func SetIAPIssuer(issuer string) {
	iapMu.Lock()
	defer iapMu.Unlock()
	iapIssuer = strings.TrimSpace(issuer)
}

// GetIAPIssuer returns the issuer a signed assertion must carry.
func GetIAPIssuer() string {
	iapMu.RLock()
	defer iapMu.RUnlock()
	if iapIssuer != "" {
		return iapIssuer
	}
	if iss := strings.TrimSpace(os.Getenv("GRAIN_IAP_ISSUER")); iss != "" {
		return iss
	}
	return GoogleIAPIssuer
}

// SetIAPHTTPClient sets a custom http.Client for fetching IAP public keys.
func SetIAPHTTPClient(client *http.Client) {
	iapMu.Lock()
	defer iapMu.Unlock()
	if client != nil {
		iapHTTPClient = client
	} else {
		iapHTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
}

// SetIAPCertURL overrides the URL used to fetch Google IAP public keys.
func SetIAPCertURL(u string) {
	iapMu.Lock()
	defer iapMu.Unlock()
	if u != "" {
		googleIAPCertURL = u
	} else {
		googleIAPCertURL = GoogleIAPCertURL
	}
}

// ResetIAPConfig resets all IAP package-level configurations and key caches to defaults.
func ResetIAPConfig() {
	iapMu.Lock()
	iapAudience = ""
	iapIssuer = ""
	iapKeySets = nil
	iapHTTPClient = &http.Client{Timeout: 5 * time.Second}
	googleIAPCertURL = GoogleIAPCertURL
	iapMu.Unlock()
	ResetRegistryConfig()
}

// IAPKeysURL returns the public-key endpoint URL active for ctx.
func IAPKeysURL(ctx context.Context) string {
	if cfg, ok := IAPConfigFromContext(ctx); ok && strings.TrimSpace(cfg.KeysURL) != "" {
		return strings.TrimSpace(cfg.KeysURL)
	}
	iapMu.RLock()
	u := googleIAPCertURL
	iapMu.RUnlock()
	if u == GoogleIAPCertURL {
		if env := strings.TrimSpace(os.Getenv("GRAIN_IAP_KEYS_URL")); env != "" {
			return env
		}
	}
	return u
}

// IAPAudienceFor returns the audience (or comma-separated audiences) active for ctx.
func IAPAudienceFor(ctx context.Context) string {
	if cfg, ok := IAPConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Audience) != "" {
		return strings.TrimSpace(cfg.Audience)
	}
	return GetIAPAudience()
}

// IAPIssuerFor returns the expected JWT issuer active for ctx.
func IAPIssuerFor(ctx context.Context) string {
	if cfg, ok := IAPConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Issuer) != "" {
		return strings.TrimSpace(cfg.Issuer)
	}
	return GetIAPIssuer()
}

func splitAudiences(aud string) []string {
	var out []string
	for _, a := range strings.Split(aud, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func getIAPPublicKey(ctx context.Context, client *http.Client, certURL, kid string) (crypto.PublicKey, error) {
	if certURL == "" {
		certURL = GoogleIAPCertURL
	}
	iapMu.RLock()
	set := iapKeySets[certURL]
	defaultClient := iapHTTPClient
	iapMu.RUnlock()
	if set != nil {
		if key, ok := set.keys[kid]; ok && time.Since(set.fetched) < iapKeysTTL {
			return key, nil
		}
	}

	iapMu.Lock()
	defer iapMu.Unlock()

	set = iapKeySets[certURL]
	if set != nil {
		key, ok := set.keys[kid]
		if ok && time.Since(set.fetched) < iapKeysTTL {
			return key, nil
		}
		if time.Since(set.fetched) < iapKeysRefetchFloor {
			if ok {
				return key, nil
			}
			return nil, fmt.Errorf("unknown IAP key ID %q", kid)
		}
	}

	hc := client
	if hc == nil {
		hc = iapHTTPClient
		if hc == nil {
			hc = defaultClient
		}
	}
	newKeys, err := jwks.Fetch(ctx, hc, certURL, "IAP")
	if err != nil {
		if set != nil {
			if key, ok := set.keys[kid]; ok {
				return key, nil
			}
		}
		return nil, err
	}
	if iapKeySets == nil {
		iapKeySets = map[string]*iapKeySet{}
	}
	iapKeySets[certURL] = &iapKeySet{keys: newKeys, fetched: time.Now()}

	k, exists := newKeys[kid]
	if !exists {
		return nil, fmt.Errorf("unknown IAP key ID %q", kid)
	}
	return k, nil
}

// ValidateIAPToken verifies an IAP JWT assertion string against expectedAudience
// (one audience or comma-separated audiences) and returns its verified claims.
//
// Signature verification enforces ES256 for EC keys and RS256 for RSA keys
// based on the published key type (never trusting alg alone), requires exp,
// checks iat/nbf with 30s clock skew leeway, and verifies iss and aud.
func ValidateIAPToken(ctx context.Context, tokenStr string, expectedAudience string) (*IAPClaims, error) {
	return validateIAPTokenWithClient(ctx, nil, tokenStr, expectedAudience)
}

func validateIAPTokenWithClient(ctx context.Context, client *http.Client, tokenStr string, expectedAudience string) (*IAPClaims, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return nil, errors.New("empty IAP assertion token")
	}
	audiences := splitAudiences(expectedAudience)
	if len(audiences) == 0 {
		return nil, errors.New("no IAP audience is configured (GRAIN_IAP_AUDIENCE / -ui-iap-audience), so no assertion can be verified")
	}
	issuer := IAPIssuerFor(ctx)
	certURL := IAPKeysURL(ctx)

	claims := &IAPClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || strings.TrimSpace(kid) == "" {
			return nil, errors.New("missing kid in IAP JWT header")
		}
		key, err := getIAPPublicKey(ctx, client, certURL, kid)
		if err != nil {
			return nil, err
		}
		// The algorithm is the key's, never the token's to choose.
		switch key.(type) {
		case *ecdsa.PublicKey:
			if t.Method.Alg() != jwt.SigningMethodES256.Alg() {
				return nil, fmt.Errorf("unexpected IAP signing algorithm %v for an EC key", t.Header["alg"])
			}
		case *rsa.PublicKey:
			if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
				return nil, fmt.Errorf("unexpected IAP signing algorithm %v for an RSA key", t.Header["alg"])
			}
		default:
			return nil, errors.New("unsupported IAP key type")
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg(), jwt.SigningMethodRS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(iapClockSkew),
		jwt.WithIssuer(issuer),
	)
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid IAP JWT assertion: %w", err)
	}

	matched := false
	for _, want := range audiences {
		if slices.Contains(claims.Audience, want) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, fmt.Errorf("IAP audience mismatch: expected %q, got %v", strings.Join(audiences, ","), claims.Audience)
	}

	return claims, nil
}

// CleanIdentity strips Google's accounts.google.com: prefix and trims surrounding whitespace.
func CleanIdentity(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	val = strings.TrimPrefix(val, "https://accounts.google.com:")
	val = strings.TrimPrefix(val, "accounts.google.com:")
	return strings.TrimSpace(val)
}

// NormalizeIdentityEmail strips Google's accounts prefix, trims whitespace,
// and lowercases the identity so emails are canonicalized in full.
func NormalizeIdentityEmail(val string) string {
	return strings.ToLower(CleanIdentity(val))
}

// PrincipalKindForEmail classifies *.gserviceaccount.com addresses as
// PrincipalService and everything else as PrincipalHuman.
func PrincipalKindForEmail(email string) PrincipalKind {
	email = strings.ToLower(strings.TrimSpace(email))
	if local, domain, ok := strings.Cut(email, "@"); ok && local != "" && strings.HasSuffix(domain, ".gserviceaccount.com") && len(domain) > len(".gserviceaccount.com") {
		return PrincipalService
	}
	return PrincipalHuman
}

// IdentityFromIAPClaims extracts and normalizes the VerifiedIdentity from verified IAPClaims.
func IdentityFromIAPClaims(claims *IAPClaims) (VerifiedIdentity, error) {
	if claims == nil {
		return VerifiedIdentity{}, errors.New("nil IAP claims")
	}
	email := strings.TrimSpace(claims.Email)
	if email == "" {
		email = strings.TrimSpace(claims.Subject)
	}
	email = NormalizeIdentityEmail(email)
	if email == "" {
		return VerifiedIdentity{}, errors.New("no email or subject found in verified IAP token claims")
	}
	return VerifiedIdentity{
		Email: email,
		Kind:  PrincipalKindForEmail(email),
	}, nil
}

// VerifyIAPToken validates an IAP JWT assertion against the audience active on
// ctx and returns the normalized authenticated email.
func VerifyIAPToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty IAP assertion token")
	}
	claims, err := ValidateIAPToken(ctx, tokenStr, IAPAudienceFor(ctx))
	if err != nil {
		return "", err
	}
	id, err := IdentityFromIAPClaims(claims)
	if err != nil {
		return "", err
	}
	return id.Email, nil
}

// IAPVerifierOption configures an IAPVerifier created by NewIAPVerifier.
type IAPVerifierOption func(*IAPVerifier)

// WithIAPValidator overrides JWT validation on an IAPVerifier.
func WithIAPValidator(v IAPValidator) IAPVerifierOption {
	return func(iv *IAPVerifier) {
		iv.validator = v
	}
}

// WithIAPAudience pins an expected audience (or comma-separated audiences) on
// an IAPVerifier, taking precedence over request/package defaults.
func WithIAPAudience(audience string) IAPVerifierOption {
	return func(iv *IAPVerifier) {
		iv.audience = strings.TrimSpace(audience)
	}
}

// WithIAPIssuer pins the expected JWT issuer on an IAPVerifier.
func WithIAPIssuer(issuer string) IAPVerifierOption {
	return func(iv *IAPVerifier) {
		iv.issuer = strings.TrimSpace(issuer)
	}
}

// WithIAPKeysURL pins the public-key endpoint URL on an IAPVerifier.
func WithIAPKeysURL(keysURL string) IAPVerifierOption {
	return func(iv *IAPVerifier) {
		iv.keysURL = strings.TrimSpace(keysURL)
	}
}

// WithIAPVerifierHTTPClient pins an HTTP client for fetching JWKS/PEM keys on an IAPVerifier.
func WithIAPVerifierHTTPClient(client *http.Client) IAPVerifierOption {
	return func(iv *IAPVerifier) {
		iv.httpClient = client
	}
}

// IAPVerifier verifies X-Goog-IAP-JWT-Assertion headers on incoming HTTP requests.
type IAPVerifier struct {
	validator  IAPValidator
	audience   string
	issuer     string
	keysURL    string
	httpClient *http.Client
}

// NewIAPVerifier constructs an IAPVerifier with the supplied options.
func NewIAPVerifier(opts ...IAPVerifierOption) *IAPVerifier {
	iv := &IAPVerifier{}
	for _, opt := range opts {
		if opt != nil {
			opt(iv)
		}
	}
	return iv
}

// Name returns the canonical verifier name ("iap").
func (v *IAPVerifier) Name() string { return "iap" }

// Headers returns the HTTP header names inspected by IAPVerifier.
func (v *IAPVerifier) Headers() []string { return []string{IAPAssertionHeader} }

// Validator returns the custom IAPValidator configured on v, if any.
func (v *IAPVerifier) Validator() IAPValidator { return v.validator }

// Audience returns the expected audience for ctx, preferring v's pinned audience.
func (v *IAPVerifier) Audience(ctx context.Context) string {
	if v != nil && v.audience != "" {
		return v.audience
	}
	return IAPAudienceFor(ctx)
}

// ContextWithOverrides applies v's pinned Issuer and KeysURL overrides onto ctx.
func (v *IAPVerifier) ContextWithOverrides(ctx context.Context) context.Context {
	if v == nil || (v.issuer == "" && v.keysURL == "") {
		return ctx
	}
	cfg, _ := IAPConfigFromContext(ctx)
	if v.issuer != "" {
		cfg.Issuer = v.issuer
	}
	if v.keysURL != "" {
		cfg.KeysURL = v.keysURL
	}
	return WithIAPConfig(ctx, cfg)
}

// VerifyToken validates tokenStr using v's configuration and ctx, returning the
// verified claims and the effective expected audience string.
func (v *IAPVerifier) VerifyToken(ctx context.Context, tokenStr string) (*IAPClaims, string, error) {
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
	claims, err := validateIAPTokenWithClient(ctx, hc, tokenStr, aud)
	return claims, aud, err
}

// VerifyRequest checks r's X-Goog-IAP-JWT-Assertion header. It returns ok=false
// with a nil error when the header is absent, and an error when the header is
// present but fails verification.
func (v *IAPVerifier) VerifyRequest(r *http.Request) (VerifiedIdentity, bool, error) {
	if r == nil {
		return VerifiedIdentity{}, false, nil
	}
	tokenStr := strings.TrimSpace(r.Header.Get(IAPAssertionHeader))
	if tokenStr == "" {
		return VerifiedIdentity{}, false, nil
	}
	claims, _, err := v.VerifyToken(r.Context(), tokenStr)
	if err != nil {
		return VerifiedIdentity{}, false, err
	}
	id, err := IdentityFromIAPClaims(claims)
	if err != nil {
		return VerifiedIdentity{}, false, err
	}
	return id, true, nil
}

// Verify authenticates r via X-Goog-IAP-JWT-Assertion, returning an error if
// the header is missing or invalid.
func (v *IAPVerifier) Verify(r *http.Request) (VerifiedIdentity, error) {
	id, ok, err := v.VerifyRequest(r)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	if !ok {
		return VerifiedIdentity{}, fmt.Errorf("missing %s header", IAPAssertionHeader)
	}
	return id, nil
}
