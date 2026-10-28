package gitproxy

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

func testAppKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pkcs1PEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestParseAppPrivateKeyAcceptsPKCS1(t *testing.T) {
	key := testAppKey(t)
	parsed, err := ParseAppPrivateKey(pkcs1PEM(key))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(key) {
		t.Error("parsed key does not match the original")
	}
}

func TestParseAppPrivateKeyAcceptsPKCS8(t *testing.T) {
	key := testAppKey(t)
	parsed, err := ParseAppPrivateKey(pkcs8PEM(t, key))
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(key) {
		t.Error("parsed key does not match the original")
	}
}

func TestParseAppPrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := ParseAppPrivateKey([]byte("not a key")); err == nil {
		t.Error("expected an error for input with no PEM block")
	}
	garbage := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not actually DER")})
	if _, err := ParseAppPrivateKey(garbage); err == nil {
		t.Error("expected an error for a PEM block that isn't a valid key")
	}
}

// appJWT's own claims are checked by decoding and verifying the token
// exactly the way a real recipient (GitHub) would, rather than asserting
// on appJWT's internals -- the point of a JWT is that it's independently
// verifiable, so that's the property worth testing.
func TestAppJWTVerifiesAndCarriesTheExpectedClaims(t *testing.T) {
	key := testAppKey(t)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	token, err := appJWT("12345", key, now)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(header) != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("unexpected header: %s", header)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	hashed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sig); err != nil {
		t.Errorf("signature does not verify against the signing key's own public half: %v", err)
	}

	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "12345" {
		t.Errorf("iss = %q, want %q", claims.Iss, "12345")
	}
	if want := now.Add(-time.Minute).Unix(); claims.Iat != want {
		t.Errorf("iat = %d, want %d (now backdated a minute)", claims.Iat, want)
	}
	if want := now.Add(9 * time.Minute).Unix(); claims.Exp != want {
		t.Errorf("exp = %d, want %d (nine minutes out)", claims.Exp, want)
	}
	if claims.Exp-claims.Iat > 10*60 {
		t.Error("exp is more than ten minutes past iat, past GitHub's own ceiling")
	}
}

func TestMintInstallationTokenPostsToTheAccessTokensEndpoint(t *testing.T) {
	key := testAppKey(t)
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 201,
		Body:   []byte(`{"token":"ghs_minted","expires_at":"2026-08-30T13:00:00Z"}`),
	})
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	token, expiresAt, err := mintInstallationToken(transport, AppCredential{
		AppID: "12345", InstallationID: "67890", PrivateKey: key,
	}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if token != "ghs_minted" {
		t.Errorf("token = %q, want %q", token, "ghs_minted")
	}
	if !expiresAt.Equal(time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("expiresAt = %v, want 13:00 UTC", expiresAt)
	}

	if len(transport.Calls) != 1 {
		t.Fatalf("expected one call, got %d", len(transport.Calls))
	}
	call := transport.Calls[0]
	if call.Method != "POST" {
		t.Errorf("method = %q, want POST", call.Method)
	}
	if call.Path != "/app/installations/67890/access_tokens" {
		t.Errorf("path = %q", call.Path)
	}
	if !strings.HasPrefix(call.Headers["Authorization"], "Bearer ") {
		t.Errorf("Authorization header = %q, want a Bearer JWT", call.Headers["Authorization"])
	}
	if len(strings.Split(call.Headers["Authorization"], ".")) != 3 {
		t.Error("expected the bearer credential to be a 3-part JWT")
	}
}

func TestMintInstallationTokenRaisesOnANon201(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 403, Body: []byte(`{"message":"nope"}`)})
	_, _, err := mintInstallationToken(transport, AppCredential{
		AppID: "1", InstallationID: "2", PrivateKey: testAppKey(t),
	}, time.Now(), "")
	if err == nil {
		t.Fatal("expected an error for a non-201 response")
	}
}

func TestMintInstallationTokenRaisesOnAnUnparsableExpiry(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{
		Status: 201, Body: []byte(`{"token":"ghs_x","expires_at":"not-a-time"}`),
	})
	_, _, err := mintInstallationToken(transport, AppCredential{
		AppID: "1", InstallationID: "2", PrivateKey: testAppKey(t),
	}, time.Now(), "")
	if err == nil {
		t.Fatal("expected an error for an unparsable expires_at")
	}
}

func TestDynamicInstallationResolutionPerRepositoryAndBotLogin(t *testing.T) {
	key := testAppKey(t)
	dir := t.TempDir()
	creds, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	creds.Now = func() time.Time { return now }

	if err := creds.SetAppWithWebhookSecret("grain-bot", "999", "", string(pkcs1PEM(key)), "whsec_test"); err != nil {
		t.Fatalf("SetAppWithWebhookSecret: %v", err)
	}
	if secret, ok := creds.AppWebhookSecret("grain-bot"); !ok || secret != "whsec_test" {
		t.Fatalf("AppWebhookSecret = (%q, %v), want (whsec_test, true)", secret, ok)
	}

	transport := github.NewFakeTransport(
		// GET /repos/acme/widgets/installation
		github.ApiResponse{Status: 200, Body: []byte(`{"id":4242}`)},
		// POST /app/installations/4242/access_tokens
		github.ApiResponse{Status: 201, Body: []byte(`{"token":"ghs_acme_widgets","expires_at":"2026-08-30T13:00:00Z"}`)},
		// GET /repos/beta/tools/installation
		github.ApiResponse{Status: 200, Body: []byte(`{"id":7777}`)},
		// POST /app/installations/7777/access_tokens
		github.ApiResponse{Status: 201, Body: []byte(`{"token":"ghs_beta_tools","expires_at":"2026-08-30T13:00:00Z"}`)},
		// GET /app (for AppBotLogin)
		github.ApiResponse{Status: 200, Body: []byte(`{"slug":"grain-bot"}`)},
	)
	creds.AppTransport = transport
	creds.SetConnections(func(owner, repo string) (string, bool) {
		return "grain-bot", true
	})

	c1, ok := creds.GetForRepo("grain-bot", "acme", "widgets")
	if !ok || c1.Token == nil || *c1.Token != "ghs_acme_widgets" {
		t.Fatalf("GetForRepo(acme/widgets) = (%+v, %v)", c1, ok)
	}
	c2, ok := creds.GetForRepo("grain-bot", "beta", "tools")
	if !ok || c2.Token == nil || *c2.Token != "ghs_beta_tools" {
		t.Fatalf("GetForRepo(beta/tools) = (%+v, %v)", c2, ok)
	}

	botLogin, isApp, err := creds.AppBotLogin("grain-bot")
	if err != nil || !isApp || botLogin != "grain-bot[bot]" {
		t.Fatalf("AppBotLogin = (%q, %v, %v), want (grain-bot[bot], true, nil)", botLogin, isApp, err)
	}
	// Cached second lookup does not issue another GET /app.
	botLogin2, isApp2, err2 := creds.AppBotLogin("grain-bot")
	if err2 != nil || !isApp2 || botLogin2 != "grain-bot[bot]" {
		t.Fatalf("second AppBotLogin = (%q, %v, %v)", botLogin2, isApp2, err2)
	}
	if len(transport.Calls) != 5 {
		t.Fatalf("transport calls = %d, want 5", len(transport.Calls))
	}
}
