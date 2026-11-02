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
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/golang-jwt/jwt/v5"
)

type principalContextKey struct{}
type authMethodContextKey struct{}
type iapConfigContextKey struct{}

// IAPAssertionHeader is the HTTP request header holding the Google Cloud IAP JWT assertion.
const IAPAssertionHeader = "X-Goog-IAP-JWT-Assertion"

// GoogleIAPCertURL is the Google endpoint serving public keys in PEM format.
const GoogleIAPCertURL = "https://www.gstatic.com/iap/verify/public_key"

// GoogleIAPIssuer is the expected issuer claim in Google IAP JWT assertions.
const GoogleIAPIssuer = "https://cloud.google.com/iap"

// PlaintextIdentityHeaders are the unsigned identity headers a proxy or
// grain's own older clients send. Anything able to reach the daemon can
// write them, so ExtractPrincipal believes none of them; a request
// carrying one is logged (logIgnoredIdentityHeader).
var PlaintextIdentityHeaders = []string{
	"X-Goog-Authenticated-User-Email",
	"X-User-LDAP",
	"X-Forwarded-User",
	"X-Goog-Authenticated-User-Id",
	"X-Grain-User",
	"Grain-User",
}

// iapKeysTTL is how long a fetched key set is used before it is fetched
// again; iapKeysRefetchFloor is how soon an unknown key ID may trigger
// another fetch, so a stream of made-up kids cannot hammer the endpoint.
const (
	iapKeysTTL          = time.Hour
	iapKeysRefetchFloor = 10 * time.Second
)

var (
	iapMu            sync.RWMutex
	iapAudience      string
	iapIssuer        string
	iapKeySets       map[string]*iapKeySet
	iapHTTPClient    = &http.Client{Timeout: 5 * time.Second}
	googleIAPCertURL = GoogleIAPCertURL
)

// iapKeySet is one key endpoint's keys, by key ID, and when they were
// fetched. Keys are cached per URL, since a deployment can name its own
// (IAPConfig.KeysURL) and a test names another.
type iapKeySet struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

// IAPConfig holds request-level or server-level IAP configuration options.
type IAPConfig struct {
	// Audience is the audience a signed assertion must carry, or several
	// separated by commas. Empty falls back to $GRAIN_IAP_AUDIENCE; with
	// neither, no assertion verifies at all.
	Audience string
	// Issuer is the issuer a signed assertion must carry. Empty means
	// $GRAIN_IAP_ISSUER, and then GoogleIAPIssuer.
	Issuer string
	// KeysURL is where the assertion's signing keys are published, as
	// IAP's PEM map or a JWK set. Empty means $GRAIN_IAP_KEYS_URL, and
	// then GoogleIAPCertURL.
	KeysURL string
	Mode    bool
	// LocalAPIToken is Config.LocalAPIToken: the secret this host's own
	// CLI presents (presentsLocalAPIToken).
	LocalAPIToken string
	// AccessSessionLifetime is Config.AccessSessionLifetime: how long a
	// single-tenant daemon's access session lasts unused.
	AccessSessionLifetime time.Duration
}

// WithIAPConfig attaches an IAPConfig to the context.
func WithIAPConfig(ctx context.Context, cfg IAPConfig) context.Context {
	return context.WithValue(ctx, iapConfigContextKey{}, cfg)
}

// IAPConfigFromContext extracts IAPConfig from context if present.
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

// splitAudiences is an audience setting's audiences: one, or several
// separated by commas, for a deployment reached through more than one
// signing proxy.
func splitAudiences(aud string) []string {
	var out []string
	for _, a := range strings.Split(aud, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
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

// SetIAPCertURL overrides the URL used to fetch Google IAP public keys
// (an operator's -ui-iap-keys-url, or a test's own server).
func SetIAPCertURL(u string) {
	iapMu.Lock()
	defer iapMu.Unlock()
	if u != "" {
		googleIAPCertURL = u
	} else {
		googleIAPCertURL = GoogleIAPCertURL
	}
}

// iapKeysURL is where the keys for a request's assertions come from.
func iapKeysURL(ctx context.Context) string {
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

// iapAudienceFor is the audience (or comma-separated audiences) a request's
// IAP assertions must carry.
func iapAudienceFor(ctx context.Context) string {
	if cfg, ok := IAPConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Audience) != "" {
		return strings.TrimSpace(cfg.Audience)
	}
	return GetIAPAudience()
}

// iapIssuerFor is the issuer a request's assertions must carry.
func iapIssuerFor(ctx context.Context) string {
	if cfg, ok := IAPConfigFromContext(ctx); ok && strings.TrimSpace(cfg.Issuer) != "" {
		return strings.TrimSpace(cfg.Issuer)
	}
	return GetIAPIssuer()
}

// ResetIAPConfig resets all IAP package-level configurations to defaults.
func ResetIAPConfig() {
	iapMu.Lock()
	defer iapMu.Unlock()
	iapAudience = ""
	iapIssuer = ""
	iapKeySets = nil
	iapHTTPClient = &http.Client{Timeout: 5 * time.Second}
	googleIAPCertURL = GoogleIAPCertURL
	ignoredIdentityHeadersMu.Lock()
	clear(ignoredIdentityHeadersLogged)
	ignoredIdentityHeadersMu.Unlock()
	refusedAssertionsMu.Lock()
	clear(refusedAssertionsLogged)
	refusedAssertionsMu.Unlock()
	refusedVerifiersMu.Lock()
	clear(refusedVerifiersLogged)
	refusedVerifiersMu.Unlock()
}

// fetchGoogleIAPKeys fetches the signing keys published at certURL. Two
// shapes are understood: IAP's own map of key ID to PEM public key
// (https://www.gstatic.com/iap/verify/public_key), and a standard JWK set
// ({"keys": [...]}, as IAP's public_key-jwk and most other signing
// proxies publish). EC P-256 and RSA keys are kept; anything else is
// skipped.
func fetchGoogleIAPKeys(ctx context.Context, client *http.Client, certURL string) (map[string]crypto.PublicKey, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if certURL == "" {
		certURL = GoogleIAPCertURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching IAP public keys: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching IAP public keys: unexpected status %d", resp.StatusCode)
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding IAP public keys: %w", err)
	}

	parsed := map[string]crypto.PublicKey{}
	if set, ok := raw["keys"]; ok {
		var jwks []jsonWebKey
		if err := json.Unmarshal(set, &jwks); err != nil {
			return nil, fmt.Errorf("decoding JWK set: %w", err)
		}
		for _, k := range jwks {
			if pub, err := k.publicKey(); err == nil && k.Kid != "" {
				parsed[k.Kid] = pub
			}
		}
	} else {
		for kid, msg := range raw {
			var pemStr string
			if json.Unmarshal(msg, &pemStr) != nil {
				continue
			}
			block, _ := pem.Decode([]byte(pemStr))
			if block == nil {
				continue
			}
			pub, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				continue
			}
			switch pub.(type) {
			case *ecdsa.PublicKey, *rsa.PublicKey:
				parsed[kid] = pub
			}
		}
	}
	if len(parsed) == 0 {
		return nil, errors.New("no usable public keys found at the IAP key endpoint")
	}
	return parsed, nil
}

// jsonWebKey is the part of an RFC 7517 key fetchGoogleIAPKeys reads.
type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (k jsonWebKey) publicKey() (crypto.PublicKey, error) {
	dec := base64.RawURLEncoding.DecodeString
	switch k.Kty {
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := dec(k.X)
		if err != nil {
			return nil, err
		}
		y, err := dec(k.Y)
		if err != nil {
			return nil, err
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("EC key is not on its curve")
		}
		return pub, nil
	case "RSA":
		n, err := dec(k.N)
		if err != nil {
			return nil, err
		}
		e, err := dec(k.E)
		if err != nil {
			return nil, err
		}
		exp := new(big.Int).SetBytes(e)
		if !exp.IsInt64() || exp.Int64() < 3 || exp.Int64() > 1<<31 {
			return nil, errors.New("RSA key has an unusable exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}

// getIAPPublicKey is key kid from the key set published at certURL,
// fetched at most once an hour, and again early (at most every
// iapKeysRefetchFloor) for a kid the cached set does not hold, which is
// how a rotated key is picked up. A failed fetch keeps serving the keys
// already held.
func getIAPPublicKey(ctx context.Context, certURL, kid string) (crypto.PublicKey, error) {
	iapMu.RLock()
	set := iapKeySets[certURL]
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

	newKeys, err := fetchGoogleIAPKeys(ctx, iapHTTPClient, certURL)
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

// iapClockSkew is how far an assertion's exp, iat and nbf may be off
// this host's clock and still be taken.
const iapClockSkew = 30 * time.Second

// defaultIAPValidator verifies an assertion as IAP documents it must be
// verified (https://cloud.google.com/iap/docs/signed-headers-howto):
// signed (ES256, or RS256 for a JWK-publishing proxy) by a key the
// configured endpoint publishes, issued by exactly the configured
// issuer, for one of the configured audiences, and current -- an
// expiry is required, not merely checked when present.
//
// An empty expectedAudience verifies nothing: an assertion is only
// worth anything bound to this deployment, and one Google signed for
// some other backend is exactly what whoever runs that backend could
// replay here.
func defaultIAPValidator(ctx context.Context, tokenStr string, expectedAudience string) (*IAPClaims, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return nil, errors.New("empty IAP assertion token")
	}
	audiences := splitAudiences(expectedAudience)
	if len(audiences) == 0 {
		return nil, errors.New("no IAP audience is configured (GRAIN_IAP_AUDIENCE / -ui-iap-audience), so no assertion can be verified")
	}
	issuer := iapIssuerFor(ctx)
	certURL := iapKeysURL(ctx)

	claims := &IAPClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		kid, ok := t.Header["kid"].(string)
		if !ok || strings.TrimSpace(kid) == "" {
			return nil, errors.New("missing kid in IAP JWT header")
		}
		key, err := getIAPPublicKey(ctx, certURL, kid)
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

// VerifyIAPToken validates an IAP JWT assertion and returns the authenticated user email/identity.
func VerifyIAPToken(ctx context.Context, tokenStr string) (string, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return "", errors.New("empty IAP assertion token")
	}

	var validator IAPValidator
	aud := iapAudienceFor(ctx)

	for _, v := range registeredVerifiersSnapshot() {
		if iv, ok := v.(*iapVerifier); ok {
			validator = iv.validator
			if iv.audience != "" {
				aud = iv.audience
			}
			if iv.issuer != "" || iv.keysURL != "" {
				cfg, _ := IAPConfigFromContext(ctx)
				if iv.issuer != "" {
					cfg.Issuer = iv.issuer
				}
				if iv.keysURL != "" {
					cfg.KeysURL = iv.keysURL
				}
				ctx = WithIAPConfig(ctx, cfg)
			}
			break
		}
	}

	var claims *IAPClaims
	var err error
	if validator != nil {
		claims, err = validator(ctx, tokenStr, aud)
	} else {
		claims, err = defaultIAPValidator(ctx, tokenStr, aud)
	}
	if err != nil {
		return "", err
	}
	if claims == nil {
		return "", errors.New("nil IAP claims")
	}

	email := strings.TrimSpace(claims.Email)
	if email == "" {
		email = strings.TrimSpace(claims.Subject)
	}
	email = normalizeIdentityEmail(email)
	if email == "" {
		return "", errors.New("no email or subject found in verified IAP token claims")
	}
	return email, nil
}

// testSourceNets are networks singleTenantTrusted trusts as it does an
// in-process request in this package's own tests and nowhere else: export_test.go
// is all that sets it, to the TEST-NET-1 range httptest.NewRequest gives
// every request it builds. Production code used to trust that range
// outright for the tests' sake (grain#2370).
var testSourceNets []*net.IPNet

// proxyIdentityHeaders are headers a signing or identity-aware proxy
// adds to the requests it relays; forwardingHeaders are the ones any
// reverse proxy adds. Grain's own CLI and frontend send neither.
var (
	proxyIdentityHeaders = []string{
		IAPAssertionHeader,
		"X-Goog-Authenticated-User-Email",
		"X-Goog-Authenticated-User-Id",
	}
	forwardingHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"}
)

// relayedByProxy reports whether r shows signs of having been relayed
// on somebody else's behalf, which makes an in-process-looking request no
// longer the daemon's own (singleTenantTrusted). Every registered
// IdentityVerifier's Headers() also count as proxy headers.
func relayedByProxy(r *http.Request) bool {
	for _, h := range proxyIdentityHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	for _, h := range forwardingHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	for _, v := range registeredVerifiersSnapshot() {
		for _, h := range v.Headers() {
			if r.Header.Get(h) != "" {
				return true
			}
		}
	}
	return false
}

// WithPrincipal returns a copy of parent context with the authenticated principal attached.
func WithPrincipal(ctx context.Context, p model.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext extracts the authenticated principal from ctx, if any.
func PrincipalFromContext(ctx context.Context) (model.Principal, bool) {
	if ctx == nil {
		return model.Principal{}, false
	}
	p, ok := ctx.Value(principalContextKey{}).(model.Principal)
	if !ok || p == (model.Principal{}) {
		return model.Principal{}, false
	}
	return p, true
}

// AuthMethod is how a request's principal was established, which the
// frontend reads to decide what it offers a signed-in person
// (grain/task-2142).
type AuthMethod string

const (
	// AuthMethodNone is a request ExtractPrincipal identified nobody in.
	AuthMethodNone AuthMethod = ""
	// AuthMethodIAP is a verified IAP (or other signing proxy's)
	// assertion.
	AuthMethodIAP AuthMethod = "iap"
	// AuthMethodTrusted is nobody signed in, from a source this daemon
	// trusts as itself: the access code, or an in-process request.
	AuthMethodTrusted AuthMethod = "trusted"
)

// ExtractPrincipal returns who r is authenticated as through a registered
// IdentityVerifier (such as Google Cloud IAP). Plaintext identity headers
// never authenticate anybody. If the deciding verifier declares
// AccessCodeWins() == true and r also carries a valid access code, the
// request is judged by the access code alone and ExtractPrincipal returns
// false.
func ExtractPrincipal(r *http.Request) (model.Principal, bool) {
	p, _, ok := extractPrincipal(r)
	return p, ok
}

// extractPrincipal is ExtractPrincipal, also saying which source
// identified the caller.
func extractPrincipal(r *http.Request) (model.Principal, AuthMethod, bool) {
	if r == nil {
		return model.Principal{}, AuthMethodNone, false
	}
	cv := cachedOrVerifyCaller(r)
	if !cv.present || cv.err != nil {
		return model.Principal{}, AuthMethodNone, false
	}
	if cv.accessCodeWins {
		cfg, _ := IAPConfigFromContext(r.Context())
		if presentsAccessCode(r, cfg) {
			return model.Principal{}, AuthMethodNone, false
		}
		if _, ok := accessSessionIssued(r, cfg); ok {
			return model.Principal{}, AuthMethodNone, false
		}
	}
	return model.Principal{Kind: cv.id.Kind, ID: cv.id.Email, Source: model.AuthSource(cv.method)}, cv.method, true
}

// identifyCaller asks each registered IdentityVerifier in turn (with IAP
// registered first by default when its audience is set). The first verifier
// whose header is present decides the request; its error refuses the request
// and never falls through to another verifier. Plaintext identity headers
// name nobody and are only logged (logIgnoredIdentityHeader).
func identifyCaller(r *http.Request) (model.Principal, AuthMethod, bool) {
	if r == nil {
		return model.Principal{}, AuthMethodNone, false
	}
	cv := cachedOrVerifyCaller(r)
	if !cv.present || cv.err != nil {
		return model.Principal{}, AuthMethodNone, false
	}
	return model.Principal{Kind: cv.id.Kind, ID: cv.id.Email, Source: model.AuthSource(cv.method)}, cv.method, true
}

// withAuthMethod records how the request's principal was established
// (extractPrincipal), for authMethodFromContext to read back.
func withAuthMethod(ctx context.Context, m AuthMethod) context.Context {
	return context.WithValue(ctx, authMethodContextKey{}, m)
}

// authMethodFromContext is the AuthMethod withAuthMethod recorded, and
// AuthMethodNone where nothing did.
func authMethodFromContext(ctx context.Context) AuthMethod {
	if ctx == nil {
		return AuthMethodNone
	}
	m, _ := ctx.Value(authMethodContextKey{}).(AuthMethod)
	return m
}

// unverifiedIdentity describes the identity r claims that ExtractPrincipal
// did not accept -- a refused assertion, or a plaintext header nobody
// opted in to -- for the refusal a caller sees, or "" when it claims
// none. It names what an operator has to configure for that identity to
// count.
//
// With proxyOnly it leaves out the headers grain's own clients send
// (ownClientIdentityHeaders): a signed-out browser names its remembered
// user in X-Grain-User on every request, and that is no proxy for an
// operator to configure.
func unverifiedIdentity(r *http.Request, proxyOnly bool) string {
	for _, v := range activeVerifiers(r.Context()) {
		if h := firstPresentHeader(r, v.Headers()); h != "" {
			if v.Name() == string(AuthMethodIAP) {
				return fmt.Sprintf("its %s did not verify against this deployment's IAP audience, issuer and keys "+
					"(-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url)", h)
			}
			return fmt.Sprintf("its %s header did not verify (%s)", h, v.Name())
		}
	}
	if strings.TrimSpace(r.Header.Get(IAPAssertionHeader)) != "" {
		return fmt.Sprintf("its %s did not verify against this deployment's IAP audience, issuer and keys "+
			"(-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url)", IAPAssertionHeader)
	}
	if h, id, why, _ := ignoredIdentityHeader(r, proxyOnly); h != "" {
		return fmt.Sprintf("it names %q only in the unsigned %s header, %s", id, h, why)
	}
	return ""
}

// ignoredIdentityHeader is the first plaintext identity header r carries
// (ExtractPrincipal believes none), the identity it names, and why it
// was not believed, phrased to follow "which ...", and what the operator
// would do about it, for the log -- or four empty strings when r carries
// none.
// It is what both the refusal a caller sees and the daemon's log line
// (logIgnoredIdentityHeader) say; proxyOnly skips
// ownClientIdentityHeaders.
func ignoredIdentityHeader(r *http.Request, proxyOnly bool) (header, id, why, fix string) {
	for _, h := range PlaintextIdentityHeaders {
		if proxyOnly && slices.Contains(ownClientIdentityHeaders, h) {
			continue
		}
		if v := cleanIdentity(r.Header.Get(h)); v != "" {
			header, id = h, v
			break
		}
	}
	if header == "" {
		return "", "", "", ""
	}
	why = "which a single-tenant daemon never takes as anybody: it keeps no accounts"
	fix = "present the access code (`grain access-code` prints it) instead"
	return header, id, why, fix
}

// ownClientIdentityHeaders are the PlaintextIdentityHeaders grain's own
// clients send -- the frontend X-Grain-User (ui/src/api.js), the Go
// client X-Forwarded-User (HTTPClient.User) -- rather than a proxy in
// front of the daemon.
var ownClientIdentityHeaders = []string{"X-Grain-User", "Grain-User", "X-Forwarded-User"}

// ignoredIdentityHeadersLogged is which plaintext identity headers have
// had an ignored request logged already. Anybody can send one, so it is
// logged once per header name for the life of the process, and
// PlaintextIdentityHeaders bounds how many lines that can ever be.
var (
	ignoredIdentityHeadersMu     sync.Mutex
	ignoredIdentityHeadersLogged = map[string]bool{}
)

// logIgnoredIdentityHeader logs, once per header name, a request whose
// plaintext identity header ExtractPrincipal ignored, naming the header,
// the identity and what the operator would set for it to count. It is
// how a deployment whose BeyondCorp tunnel sends only that header finds
// out why everybody is anonymous (the identity-aware proxy's own docs), as
// logRefusedAssertion is for a signed assertion.
func logIgnoredIdentityHeader(r *http.Request) {
	h, id, why, fix := ignoredIdentityHeader(r, true)
	if h == "" {
		return
	}
	ignoredIdentityHeadersMu.Lock()
	seen := ignoredIdentityHeadersLogged[h]
	ignoredIdentityHeadersLogged[h] = true
	ignoredIdentityHeadersMu.Unlock()
	if seen {
		return
	}
	log.Printf("ui: ignored the unsigned %s header naming %q on a request from %q, %s -- %s "+
		"(logged once per header; later ones are ignored silently)",
		h, id, r.RemoteAddr, why, fix)
}

func cleanIdentity(val string) string {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}
	val = strings.TrimPrefix(val, "https://accounts.google.com:")
	val = strings.TrimPrefix(val, "accounts.google.com:")
	return strings.TrimSpace(val)
}

// withAgentRequester is requestPrincipal for a write an agent makes on
// its own behalf through the daemon's hop rather than the agent API (whose
// gate makes the acting task the actor itself) -- returning ctx with
// that agent as the actor, so the pause row and the task's conversation
// name {agent, <task id>} rather than this deployment's own operator.
// Only an agent may be named: a request naming a person or an automation
// would be a caller claiming to be somebody it is not, where naming the
// task a run serves is only being specific about which agent it is.
// Naming nobody leaves ctx as it was.
func withAgentRequester(ctx context.Context, what, id, kind string) (context.Context, error) {
	by, err := requestPrincipal(what, id, kind)
	if err != nil {
		return ctx, err
	}
	if by == (model.Principal{}) {
		return ctx, nil
	}
	if by.Kind != model.PrincipalAgent {
		return ctx, validationErrorf("%s may only name an agent, got %s %q", what, by.Kind, by.ID)
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		if !by.Same(p) {
			return ctx, forbiddenErrorf("agent %q may not act as %s %q", p.ID, by.Kind, by.ID)
		}
		return ctx, nil
	}
	by.Source = model.SourceSandbox
	return WithPrincipal(ctx, by), nil
}

// requestPrincipal is the principal a request names, and the zero
// Principal for one that names nobody -- which every caller of this
// reads as "record this deployment's own actor".
//
// It is a claim, not a proof: the API is unauthenticated, so a caller
// that says it is somebody is believed. Worth stating plainly rather
// than assuming otherwise, and it is the footing every other attribution
// in this store already stands on -- a caller that can reach this port
// at all can already do the thing it is attributing.
//
// A kind and an id are given together or not at all: a kind grain does
// not know would land an unreadable word in a column every reader
// switches on, and an id with no kind would be an author nothing could
// say the nature of. Both are refused with a sentence rather than
// quietly defaulted, since a caller that went to the trouble of naming a
// principal has an opinion worth failing over.
//
// what names the thing being attributed ("a memory's author"), so the
// sentence a 400 carries says which field of which request was wrong.
func requestPrincipal(what, id, kind string) (model.Principal, error) {
	id, kind = strings.TrimSpace(id), strings.TrimSpace(kind)
	if id == "" && kind == "" {
		return model.Principal{}, nil
	}
	if id == "" || kind == "" {
		return model.Principal{}, validationErrorf(
			"%s is a kind and an id together, got kind %q and id %q", what, kind, id)
	}
	if !model.PrincipalKind(kind).Valid() {
		return model.Principal{}, validationErrorf(
			"%q is not a kind of principal grain knows (%s, %s, %s or %s)",
			kind, model.PrincipalHuman, model.PrincipalService, model.PrincipalAgent, model.PrincipalAutomation)
	}
	return model.Principal{Kind: model.PrincipalKind(kind), ID: id}, nil
}
