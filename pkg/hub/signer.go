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

package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// RegistryAssertionIssuer is the fixed `iss` claim in hub assertions.
	// It keeps its "grain-registry" spelling through the Grain Hub rename:
	// every daemon configured with -ui-hub-issuer (or the old
	// -ui-registry-issuer) names it, and changing it would stop every
	// assertion already minted, and every one minted after, verifying.
	RegistryAssertionIssuer = "grain-registry"

	// HubAssertionHeader is the HTTP header carrying the signed hub JWT.
	HubAssertionHeader = "X-Grain-Hub-Assertion"
	// RegistryAssertionHeader is HubAssertionHeader's pre-rename name; the
	// hub sends both for a release.
	RegistryAssertionHeader = "X-Grain-Registry-Assertion"

	// AssertionNotBeforeSkew is subtracted from `iat` to form `nbf` (-30s).
	AssertionNotBeforeSkew = 30 * time.Second
)

// RegistryClaims represents the JWT payload signed by the registry (`ES256`)
// and attached as `X-Grain-Registry-Assertion`.
type RegistryClaims struct {
	Issuer    string           `json:"iss"`
	Audience  string           `json:"aud"`
	Subject   string           `json:"sub"`
	Email     string           `json:"email"`
	Kind      string           `json:"kind"`
	Actor     *DelegationActor `json:"act,omitempty"`
	IssuedAt  int64            `json:"iat"`
	NotBefore int64            `json:"nbf"`
	ExpiresAt int64            `json:"exp"`
	JWTID     string           `json:"jti"`
	RequestID string           `json:"rid,omitempty"`
}

// JSONWebKey represents a single public key in an RFC 7517 JWKS document.
type JSONWebKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JSONWebKeySet represents an RFC 7517 JWKS document served at `/.well-known/jwks.json`.
type JSONWebKeySet struct {
	Keys []JSONWebKey `json:"keys"`
}

// LocalKeyPair holds an ECDSA P-256 key pair and its key ID.
type LocalKeyPair struct {
	Kid        string
	PrivateKey *ecdsa.PrivateKey
	PublicKey  *ecdsa.PublicKey
}

// LocalSigner signs registry assertions using an in-process ECDSA P-256 private key
// and publishes all configured verification keys in its JWKS document.
type LocalSigner struct {
	active LocalKeyPair
	all    []LocalKeyPair
	ttl    time.Duration
	nowFn  func() time.Time
}

// NewLocalSigner creates a LocalSigner from SigningConfig (`key_file`, `key_dir`,
// or an ephemeral P-256 key when neither is set).
func NewLocalSigner(cfg SigningConfig) (*LocalSigner, error) {
	ttl := cfg.JWTTTL
	if ttl <= 0 {
		ttl = DefaultJWTTTL
	}
	var pairs []LocalKeyPair
	switch {
	case strings.TrimSpace(cfg.KeyDir) != "":
		loaded, err := loadKeysFromDir(cfg.KeyDir)
		if err != nil {
			return nil, err
		}
		pairs = loaded
	case strings.TrimSpace(cfg.KeyFile) != "":
		kp, err := loadKeyFromFile(cfg.KeyFile, cfg.KeyID)
		if err != nil {
			return nil, err
		}
		pairs = []LocalKeyPair{kp}
	default:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generating ephemeral P-256 key: %w", err)
		}
		kid := strings.TrimSpace(cfg.KeyID)
		if kid == "" {
			kid = computeKeyID(&priv.PublicKey)
		}
		pairs = []LocalKeyPair{{
			Kid:        kid,
			PrivateKey: priv,
			PublicKey:  &priv.PublicKey,
		}}
	}
	if len(pairs) == 0 {
		return nil, errors.New("local signer: no keys loaded")
	}

	active := pairs[0]
	if wantKid := strings.TrimSpace(cfg.KeyID); wantKid != "" {
		found := false
		for _, kp := range pairs {
			if kp.Kid == wantKid && kp.PrivateKey != nil {
				active = kp
				found = true
				break
			}
		}
		if !found && active.PrivateKey != nil && len(pairs) == 1 {
			active.Kid = wantKid
			pairs[0].Kid = wantKid
		}
	}
	if active.PrivateKey == nil {
		return nil, errors.New("local signer: active key has no private key")
	}
	return &LocalSigner{
		active: active,
		all:    pairs,
		ttl:    ttl,
		nowFn:  time.Now,
	}, nil
}

// NewEphemeralLocalSigner creates a LocalSigner with a freshly generated P-256 key.
func NewEphemeralLocalSigner(kid string) (*LocalSigner, error) {
	return NewLocalSigner(SigningConfig{
		Provider: "local",
		KeyID:    kid,
		JWTTTL:   DefaultJWTTTL,
	})
}

func loadKeysFromDir(dir string) ([]LocalKeyPair, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading signing key_dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".pem") || strings.HasSuffix(e.Name(), ".key") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var pairs []LocalKeyPair
	for _, name := range names {
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".pem"), ".key")
		kp, err := loadKeyFromFile(filepath.Join(dir, name), base)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, kp)
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no .pem or .key files found in %s", dir)
	}
	return pairs, nil
}

func loadKeyFromFile(path, defaultKid string) (LocalKeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LocalKeyPair{}, fmt.Errorf("reading key file %s: %w", path, err)
	}
	priv, pub, err := ParseECDSAP256PEM(data)
	if err != nil {
		return LocalKeyPair{}, fmt.Errorf("parsing key file %s: %w", path, err)
	}
	kid := strings.TrimSpace(defaultKid)
	if kid == "" {
		kid = computeKeyID(pub)
	}
	return LocalKeyPair{
		Kid:        kid,
		PrivateKey: priv,
		PublicKey:  pub,
	}, nil
}

// ParseECDSAP256PEM parses an ECDSA P-256 private or public key from PEM bytes.
func ParseECDSAP256PEM(pemBytes []byte) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, nil, errors.New("no PEM block found")
	}
	if priv, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		if priv.Curve != elliptic.P256() {
			return nil, nil, fmt.Errorf("expected P-256 curve, got %s", priv.Curve.Params().Name)
		}
		return priv, &priv.PublicKey, nil
	}
	if pkcs8Key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		priv, ok := pkcs8Key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("expected ECDSA private key in PKCS#8, got %T", pkcs8Key)
		}
		if priv.Curve != elliptic.P256() {
			return nil, nil, fmt.Errorf("expected P-256 curve, got %s", priv.Curve.Params().Name)
		}
		return priv, &priv.PublicKey, nil
	}
	if pubAny, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		pub, ok := pubAny.(*ecdsa.PublicKey)
		if !ok {
			return nil, nil, fmt.Errorf("expected ECDSA public key, got %T", pubAny)
		}
		if pub.Curve != elliptic.P256() {
			return nil, nil, fmt.Errorf("expected P-256 curve, got %s", pub.Curve.Params().Name)
		}
		return nil, pub, nil
	}
	return nil, nil, errors.New("unsupported PEM key format (expected EC private key, PKCS#8 private key, or PKIX public key)")
}

func computeKeyID(pub *ecdsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "local-1"
	}
	sum := sha256.Sum256(der)
	return "local-" + hex.EncodeToString(sum[:6])
}

// PublicKeyToJWK converts an ECDSA P-256 public key into an RFC 7517 JSONWebKey.
func PublicKeyToJWK(kid string, pub *ecdsa.PublicKey) (JSONWebKey, error) {
	if pub == nil || pub.Curve != elliptic.P256() {
		return JSONWebKey{}, errors.New("PublicKeyToJWK requires a non-nil P-256 ECDSA public key")
	}
	xBytes := make([]byte, 32)
	yBytes := make([]byte, 32)
	pub.X.FillBytes(xBytes)
	pub.Y.FillBytes(yBytes)
	return JSONWebKey{
		Kty: "EC",
		Crv: "P-256",
		Kid: kid,
		Use: "sig",
		Alg: "ES256",
		X:   base64.RawURLEncoding.EncodeToString(xBytes),
		Y:   base64.RawURLEncoding.EncodeToString(yBytes),
	}, nil
}

// JWKToPublicKey converts an RFC 7517 JSONWebKey back into an ECDSA P-256 public key.
func JWKToPublicKey(jwk JSONWebKey) (*ecdsa.PublicKey, error) {
	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported JWK kty=%q crv=%q", jwk.Kty, jwk.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("decoding JWK x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("decoding JWK y: %w", err)
	}
	if len(xBytes) != 32 || len(yBytes) != 32 {
		return nil, fmt.Errorf("invalid P-256 coordinate lengths (%d, %d)", len(xBytes), len(yBytes))
	}
	curve := elliptic.P256()
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !curve.IsOnCurve(x, y) {
		return nil, errors.New("JWK coordinates are not on P-256 curve")
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

// CompleteClaims populates default `iss`, `iat`, `nbf`, `exp`, `kind`, and `jti`
// fields on RegistryClaims before signing.
func CompleteClaims(claims RegistryClaims, now time.Time, ttl time.Duration) RegistryClaims {
	if ttl <= 0 {
		ttl = DefaultJWTTTL
	}
	if claims.Issuer == "" {
		claims.Issuer = RegistryAssertionIssuer
	}
	if claims.Kind == "" {
		claims.Kind = "human"
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = now.Unix()
	}
	if claims.NotBefore == 0 {
		claims.NotBefore = now.Add(-AssertionNotBeforeSkew).Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = now.Add(ttl).Unix()
	}
	if claims.JWTID == "" {
		claims.JWTID = randomJTI()
	}
	return claims
}

func randomJTI() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// EncodeUnsignedJWT constructs the `base64url(header) + "." + base64url(payload)`
// signing input for ES256 and returns both the signing input and its SHA-256 digest.
func EncodeUnsignedJWT(kid string, claims RegistryClaims) (signingInput string, digest [32]byte, err error) {
	hdr := struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}{
		Alg: "ES256",
		Typ: "JWT",
		Kid: kid,
	}
	hdrJSON, err := json.Marshal(hdr)
	if err != nil {
		return "", [32]byte{}, err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", [32]byte{}, err
	}
	signingInput = base64.RawURLEncoding.EncodeToString(hdrJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	digest = sha256.Sum256([]byte(signingInput))
	return signingInput, digest, nil
}

// SignAssertion signs RegistryClaims with the active local P-256 private key using JWS ES256 (`R || S`).
func (s *LocalSigner) SignAssertion(_ context.Context, claims RegistryClaims) (string, error) {
	now := time.Now()
	if s.nowFn != nil {
		now = s.nowFn()
	}
	claims = CompleteClaims(claims, now, s.ttl)
	signingInput, digest, err := EncodeUnsignedJWT(s.active.Kid, claims)
	if err != nil {
		return "", err
	}
	rInt, sInt, err := ecdsa.Sign(rand.Reader, s.active.PrivateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("ecdsa sign: %w", err)
	}
	sig := make([]byte, 64)
	rInt.FillBytes(sig[:32])
	sInt.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// JWKS returns the RFC 7517 JSONWebKeySet containing all configured local verification keys.
func (s *LocalSigner) JWKS(_ context.Context) (*JSONWebKeySet, error) {
	keys := make([]JSONWebKey, 0, len(s.all))
	for _, kp := range s.all {
		jwk, err := PublicKeyToJWK(kp.Kid, kp.PublicKey)
		if err != nil {
			return nil, err
		}
		keys = append(keys, jwk)
	}
	return &JSONWebKeySet{Keys: keys}, nil
}

// CachingSigner wraps a Signer with a short-lived per-(email, audience) cache
// (default 30s cache TTL for 60s JWT TTL) so per-request signing never blocks
// on KMS round-trips or burns KMS quota.
type CachingSigner struct {
	inner    Signer
	cacheTTL time.Duration
	nowFn    func() time.Time

	mu      sync.Mutex
	entries map[signerCacheKey]cachedAssertion
}

// signerCacheKey holds every claim a caller chooses, so a cached assertion
// is only ever reused for a request asking for exactly the same claims.
// Only the time claims (iat/nbf/exp) and jti, which the signer fills in,
// are left out; RequestID/JWTID bypass the cache altogether.
type signerCacheKey struct {
	issuer      string
	email       string
	sub         string
	audience    string
	kind        string
	hasActor    bool
	actorSub    string
	actorApp    string
	actorGrant  string
	actorAccess string
}

type cachedAssertion struct {
	token     string
	expiresAt time.Time
}

const maxSignerCacheEntries = 4096

// CacheTTLForJWTTTL derives a safe CachingSigner TTL that never exceeds half
// of the signed assertion's lifetime (`jwtTTL / 2`) or DefaultJWTCacheTTL.
func CacheTTLForJWTTTL(jwtTTL time.Duration) time.Duration {
	if jwtTTL <= 0 {
		jwtTTL = DefaultJWTTTL
	}
	half := jwtTTL / 2
	if half < DefaultJWTCacheTTL {
		return half
	}
	return DefaultJWTCacheTTL
}

// NewCachingSigner wraps inner with a per-(email, audience) assertion cache.
func NewCachingSigner(inner Signer, cacheTTL time.Duration) *CachingSigner {
	if cacheTTL <= 0 {
		cacheTTL = DefaultJWTCacheTTL
	}
	return &CachingSigner{
		inner:    inner,
		cacheTTL: cacheTTL,
		nowFn:    time.Now,
		entries:  make(map[signerCacheKey]cachedAssertion),
	}
}

// SignAssertion returns a cached token if one is still valid for the given caller
// and target entry audience, or signs and caches a new one.
//
// When RequestID or JWTID is explicitly populated on claims, caching is bypassed
// so caller-supplied per-request correlation IDs are never aliased across requests.
func (c *CachingSigner) SignAssertion(ctx context.Context, claims RegistryClaims) (string, error) {
	if claims.RequestID != "" || claims.JWTID != "" || claims.ExpiresAt != 0 {
		return c.inner.SignAssertion(ctx, claims)
	}
	now := time.Now()
	if c.nowFn != nil {
		now = c.nowFn()
	}
	var actorSub, actorApp, actorGrant, actorAccess string
	if claims.Actor != nil {
		actorSub = claims.Actor.Sub
		actorApp = claims.Actor.App
		actorGrant = claims.Actor.Grant
		actorAccess = claims.Actor.Access
	}
	key := signerCacheKey{
		issuer:      claims.Issuer,
		hasActor:    claims.Actor != nil,
		email:       strings.ToLower(strings.TrimSpace(claims.Email)),
		sub:         claims.Subject,
		audience:    claims.Audience,
		kind:        claims.Kind,
		actorSub:    actorSub,
		actorApp:    actorApp,
		actorGrant:  actorGrant,
		actorAccess: actorAccess,
	}

	c.mu.Lock()
	if hit, ok := c.entries[key]; ok && now.Before(hit.expiresAt) {
		c.mu.Unlock()
		return hit.token, nil
	}
	c.mu.Unlock()

	tok, err := c.inner.SignAssertion(ctx, claims)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	if len(c.entries) >= maxSignerCacheEntries {
		for k, v := range c.entries {
			if !now.Before(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxSignerCacheEntries {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}
	c.entries[key] = cachedAssertion{
		token:     tok,
		expiresAt: now.Add(c.cacheTTL),
	}
	c.mu.Unlock()
	return tok, nil
}

// JWKS delegates to the underlying Signer.
func (c *CachingSigner) JWKS(ctx context.Context) (*JSONWebKeySet, error) {
	return c.inner.JWKS(ctx)
}

// InvalidateGrant removes all cached assertions issued under grantID.
func (c *CachingSigner) InvalidateGrant(grantID string) {
	grantID = strings.TrimSpace(grantID)
	if c == nil || grantID == "" {
		return
	}
	c.mu.Lock()
	for k := range c.entries {
		if k.hasActor && k.actorGrant == grantID {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// InvalidateApp removes all cached assertions issued for appName (`act.app`).
func (c *CachingSigner) InvalidateApp(appName string) {
	appName = strings.ToLower(strings.TrimSpace(appName))
	if c == nil || appName == "" {
		return
	}
	c.mu.Lock()
	for k := range c.entries {
		if k.hasActor && strings.EqualFold(k.actorApp, appName) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// InvalidateActor removes all cached assertions issued for actorSub.
func (c *CachingSigner) InvalidateActor(actorSub string) {
	actorSub = strings.ToLower(strings.TrimSpace(actorSub))
	if c == nil || actorSub == "" {
		return
	}
	c.mu.Lock()
	for k := range c.entries {
		if k.hasActor && strings.EqualFold(k.actorSub, actorSub) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}

// VerifyRegistryAssertion verifies a JWS ES256 registry assertion against a JWKS
// and validates its issuer, audience, and time bounds (`nbf` and `exp`).
func VerifyRegistryAssertion(token string, jwks *JSONWebKeySet, expectedAud string, now time.Time) (*RegistryClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jwt: expected 3 dot-separated segments")
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("jwt: decode header: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return nil, fmt.Errorf("jwt: unmarshal header: %w", err)
	}
	if hdr.Alg != "ES256" {
		return nil, fmt.Errorf("jwt: unexpected alg %q (want ES256)", hdr.Alg)
	}
	if jwks == nil || len(jwks.Keys) == 0 {
		return nil, errors.New("jwt: empty JWKS")
	}
	var matchedKey *JSONWebKey
	for i := range jwks.Keys {
		if jwks.Keys[i].Kid == hdr.Kid {
			matchedKey = &jwks.Keys[i]
			break
		}
	}
	if matchedKey == nil {
		return nil, fmt.Errorf("jwt: kid %q not found in JWKS", hdr.Kid)
	}
	pub, err := JWKToPublicKey(*matchedKey)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("jwt: decode signature: %w", err)
	}
	if len(sig) != 64 {
		return nil, fmt.Errorf("jwt: invalid ES256 signature length %d", len(sig))
	}
	rInt := new(big.Int).SetBytes(sig[:32])
	sInt := new(big.Int).SetBytes(sig[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], rInt, sInt) {
		return nil, errors.New("jwt: ES256 signature verification failed")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("jwt: decode payload: %w", err)
	}
	var claims RegistryClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("jwt: unmarshal claims: %w", err)
	}
	if claims.Issuer != RegistryAssertionIssuer {
		return nil, fmt.Errorf("jwt: unexpected iss %q", claims.Issuer)
	}
	if expectedAud != "" && claims.Audience != expectedAud {
		return nil, fmt.Errorf("jwt: unexpected aud %q (want %q)", claims.Audience, expectedAud)
	}
	unixNow := now.Unix()
	if claims.NotBefore != 0 && unixNow < claims.NotBefore {
		return nil, fmt.Errorf("jwt: token not valid before %d (now %d)", claims.NotBefore, unixNow)
	}
	if claims.ExpiresAt == 0 || unixNow >= claims.ExpiresAt {
		return nil, fmt.Errorf("jwt: token expired at %d (now %d)", claims.ExpiresAt, unixNow)
	}
	return &claims, nil
}

func init() {
	RegisterSignerProvider("local", func(_ context.Context, cfg SigningConfig) (Signer, error) {
		ls, err := NewLocalSigner(cfg)
		if err != nil {
			return nil, err
		}
		cacheTTL := CacheTTLForJWTTTL(cfg.JWTTTL)
		if cacheTTL <= 0 {
			return ls, nil
		}
		return NewCachingSigner(ls, cacheTTL), nil
	})
}
