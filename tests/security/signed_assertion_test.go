package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A local stand-in for IAP: it signs real ES256 assertions with a key
// generated here and publishes it as a JWK set on a local server, for
// the tests that still need a proxy to vouch for somebody
// (identityProxy) -- now to show that a single-tenant daemon does not
// run that person as anybody. Only Google's half is stubbed.

const (
	assertionIssuer   = "https://iap.test.example"
	assertionAudience = "/projects/123/global/backendServices/456"
	assertionKid      = "local-es256"
	assertionUser     = "alice@example.com"
)

// assertionSigner is a local stand-in for IAP: an ES256 key and the JWK
// set it publishes.
type assertionSigner struct {
	key     *ecdsa.PrivateKey
	keysURL string
}

// startAssertionSigner is newAssertionSigner without a test to clean up
// after: the caller closes the key endpoint.
func startAssertionSigner() (*assertionSigner, func(), error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	pad := func(b []byte) []byte { return append(make([]byte, 32-len(b)), b...) }
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": assertionKid,
		"x": b64(pad(key.PublicKey.X.Bytes())), "y": b64(pad(key.PublicKey.Y.Bytes())),
	}}})
	if err != nil {
		return nil, nil, err
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwks)
	}))
	return &assertionSigner{key: key, keysURL: srv.URL}, srv.Close, nil
}

type assertionClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email,omitempty"`
}

// validAssertionClaims are what a correct assertion for this deployment
// carries; each negative case changes one thing about them.
func validAssertionClaims() assertionClaims {
	now := time.Now()
	return assertionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    assertionIssuer,
			Audience:  jwt.ClaimStrings{assertionAudience},
			Subject:   "accounts.google.com:1234567890",
			IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		},
		Email: "accounts.google.com:" + assertionUser,
	}
}

func (s *assertionSigner) sign(t *testing.T, c assertionClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = assertionKid
	out, err := tok.SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
