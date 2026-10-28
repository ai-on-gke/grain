package ui_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/golang-jwt/jwt/v5"
)

// grain/task-1847: a BeyondCorp port tunnel delivered requests from
// loopback carrying X-Goog-Authenticated-User-Email, and grain took that
// unsigned header as who the caller was. The daemon keeps no accounts
// now, but it still verifies an IAP assertion, to refuse a person a
// proxy vouched for rather than run them as itself
// (singleTenantRefusesIdentity). These tests pin that verification.

const (
	tunnelAudience = "/projects/424242/global/backendServices/777"
	tunnelKid      = "tunnel-key-1"
)

// assertionKeys serves priv's public key as IAP does (a PEM map, or a
// JWK set with jwk) and points the package's IAP verification at it.
func assertionKeys(t *testing.T, priv *ecdsa.PrivateKey, pemStr string, jwk bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if jwk {
			b := func(n *big.Int) string { return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, 32))) }
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "EC", "crv": "P-256", "kid": tunnelKid, "alg": "ES256",
				"x": b(priv.PublicKey.X), "y": b(priv.PublicKey.Y),
			}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{tunnelKid: pemStr})
	}))
	t.Cleanup(srv.Close)
	ui.ResetIAPConfig()
	ui.SetIAPCertURL(srv.URL)
	ui.SetIAPHTTPClient(srv.Client())
	t.Cleanup(ui.ResetIAPConfig)
	return srv.URL
}

// verifiedAs is who r's IAP assertion verifies as, the way the access
// gate asks before refusing it (singleTenantRefusesIdentity).
func verifiedAs(r *http.Request) (model.Principal, bool) {
	email, err := ui.VerifyIAPToken(r.Context(), r.Header.Get(ui.IAPAssertionHeader))
	if err != nil || email == "" {
		return model.Principal{}, false
	}
	return model.Principal{Kind: model.PrincipalHuman, ID: email}, true
}

func assertionRequest(remoteAddr, assertion string, cfg ui.IAPConfig, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.RemoteAddr = remoteAddr
	req = req.WithContext(ui.WithIAPConfig(req.Context(), cfg))
	if assertion != "" {
		req.Header.Set(ui.IAPAssertionHeader, assertion)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestSignedAssertionIsVerifiedByTheDefaultValidator(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	cfg := ui.IAPConfig{Audience: tunnelAudience}
	// The plaintext headers a BeyondCorp proxy sends beside the
	// assertion: none of them may stand in for an assertion that did not
	// verify.
	spoof := map[string]string{
		"X-Goog-Authenticated-User-Email": "accounts.google.com:admin@example.com",
		"X-Grain-User":                    "admin@example.com",
	}

	good := signIAPToken(t, priv, tunnelKid, tunnelAudience, "dharb@google.com", "accounts.google.com:1234", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))
	p, ok := verifiedAs(assertionRequest("127.0.0.1:4000", good, cfg, spoof))
	if !ok || p != (model.Principal{Kind: model.PrincipalHuman, ID: "dharb@google.com"}) {
		t.Fatalf("a valid assertion was not accepted as dharb@google.com: %+v %v", p, ok)
	}

	other, _ := generateECDSATestKey(t)
	noExp := func() string {
		claims := ui.IAPClaims{RegisteredClaims: jwt.RegisteredClaims{
			Issuer: ui.GoogleIAPIssuer, Audience: jwt.ClaimStrings{tunnelAudience}, IssuedAt: jwt.NewNumericDate(time.Now()),
		}, Email: "admin@example.com"}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = tunnelKid
		s, err := tok.SignedString(priv)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}()
	hmacForged := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, ui.IAPClaims{RegisteredClaims: jwt.RegisteredClaims{
			Issuer: ui.GoogleIAPIssuer, Audience: jwt.ClaimStrings{tunnelAudience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}, Email: "admin@example.com"})
		tok.Header["kid"] = tunnelKid
		s, err := tok.SignedString([]byte(pemStr))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}()
	exp := time.Now().Add(10 * time.Minute)
	refused := map[string]string{
		"forged signature":             signIAPToken(t, other, tunnelKid, tunnelAudience, "admin@example.com", "1", ui.GoogleIAPIssuer, exp),
		"unknown key":                  signIAPToken(t, priv, "not-a-published-kid", tunnelAudience, "admin@example.com", "1", ui.GoogleIAPIssuer, exp),
		"expired":                      signIAPToken(t, priv, tunnelKid, tunnelAudience, "admin@example.com", "1", ui.GoogleIAPIssuer, time.Now().Add(-10*time.Minute)),
		"no expiry":                    noExp,
		"wrong audience":               signIAPToken(t, priv, tunnelKid, "/projects/1/global/backendServices/2", "admin@example.com", "1", ui.GoogleIAPIssuer, exp),
		"wrong issuer":                 signIAPToken(t, priv, tunnelKid, tunnelAudience, "admin@example.com", "1", "https://accounts.google.com", exp),
		"no issuer":                    signIAPToken(t, priv, tunnelKid, tunnelAudience, "admin@example.com", "1", "", exp),
		"HS256 over the published key": hmacForged,
		"garbage":                      "not.a.jwt",
	}
	for name, tok := range refused {
		if p, ok := verifiedAs(assertionRequest("127.0.0.1:4000", tok, cfg, spoof)); ok {
			t.Errorf("%s assertion accepted as %+v", name, p)
		}
	}

	// And with no audience configured at all, even a well-formed,
	// Google-signed assertion is refused: it could have been minted for
	// any IAP backend, whoever runs that one could replay it here.
	if p, ok := verifiedAs(assertionRequest("127.0.0.1:4000", good, ui.IAPConfig{}, nil)); ok {
		t.Errorf("assertion accepted with no audience configured: %+v", p)
	}
}

func TestSignedAssertionIssuerAudiencesAndKeysAreConfigurable(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	keysURL := assertionKeys(t, priv, pemStr, true)
	// The package default points somewhere else entirely: only the
	// per-server KeysURL can find the key.
	ui.SetIAPCertURL("http://127.0.0.1:1/nothing-here")
	const issuer = "https://iap-tunnel.example.com/tunnel"
	cfg := ui.IAPConfig{
		Audience: "/projects/1/apps/elsewhere, " + tunnelAudience,
		Issuer:   issuer,
		KeysURL:  keysURL,
	}
	tok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "dharb@google.com", "x", issuer, time.Now().Add(5*time.Minute))
	if p, ok := verifiedAs(assertionRequest("203.0.113.5:1", tok, cfg, nil)); !ok || p.ID != "dharb@google.com" {
		t.Fatalf("assertion from a configured issuer, audience and JWK set refused: %+v %v", p, ok)
	}
	googleIssued := signIAPToken(t, priv, tunnelKid, tunnelAudience, "dharb@google.com", "x", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	if p, ok := verifiedAs(assertionRequest("203.0.113.5:1", googleIssued, cfg, nil)); ok {
		t.Fatalf("assertion from an issuer other than the configured one accepted: %+v", p)
	}
}

// Cloud Run's own IAP names the service rather than a backend service in
// its assertions: /projects/<number>/locations/<region>/services/<name>.
// The audience is matched as an opaque string, so it verifies like any
// other -- and a Cloud Run service's assertion is no good for another
// service, region or project, nor for a load balancer's backend.
func TestCloudRunServiceAudienceIsVerified(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	const cloudRun = "/projects/123456789012/locations/europe-west4/services/grain-ui"
	exp := time.Now().Add(10 * time.Minute)

	tok := signIAPToken(t, priv, tunnelKid, cloudRun, "dharb@google.com", "accounts.google.com:1234", ui.GoogleIAPIssuer, exp)
	for _, aud := range []string{cloudRun, tunnelAudience + "," + cloudRun} {
		p, ok := verifiedAs(assertionRequest("203.0.113.5:1", tok, ui.IAPConfig{Audience: aud}, nil))
		if !ok || p != (model.Principal{Kind: model.PrincipalHuman, ID: "dharb@google.com"}) {
			t.Errorf("Cloud Run IAP assertion refused with audience %q: %+v %v", aud, p, ok)
		}
	}

	for _, other := range []string{
		"/projects/123456789012/locations/europe-west4/services/another-service",
		"/projects/123456789012/locations/us-central1/services/grain-ui",
		"/projects/999999999999/locations/europe-west4/services/grain-ui",
		tunnelAudience,
	} {
		wrong := signIAPToken(t, priv, tunnelKid, other, "admin@example.com", "1", ui.GoogleIAPIssuer, exp)
		if p, ok := verifiedAs(assertionRequest("203.0.113.5:1", wrong, ui.IAPConfig{Audience: cloudRun}, nil)); ok {
			t.Errorf("assertion for %q accepted against %q as %+v", other, cloudRun, p)
		}
	}
}

func TestRSASignedAssertionFromAJWKSet(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "rsa-1",
			"n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes()),
		}}})
	}))
	defer srv.Close()
	ui.ResetIAPConfig()
	defer ui.ResetIAPConfig()
	ui.SetIAPHTTPClient(srv.Client())
	cfg := ui.IAPConfig{Audience: tunnelAudience, KeysURL: srv.URL}

	sign := func(m jwt.SigningMethod, key any) string {
		tok := jwt.NewWithClaims(m, ui.IAPClaims{RegisteredClaims: jwt.RegisteredClaims{
			Issuer: ui.GoogleIAPIssuer, Audience: jwt.ClaimStrings{tunnelAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now()),
		}, Email: "rsa@example.com"})
		tok.Header["kid"] = "rsa-1"
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if p, ok := verifiedAs(assertionRequest("203.0.113.5:1", sign(jwt.SigningMethodRS256, priv), cfg, nil)); !ok || p.ID != "rsa@example.com" {
		t.Fatalf("RS256 assertion over a published RSA key refused: %+v %v", p, ok)
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if p, ok := verifiedAs(assertionRequest("203.0.113.5:1", sign(jwt.SigningMethodES256, ec), cfg, nil)); ok {
		t.Fatalf("ES256 assertion naming an RSA key accepted: %+v", p)
	}
}
