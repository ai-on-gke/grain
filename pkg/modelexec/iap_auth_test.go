package modelexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

func createTestJWT(t *testing.T, exp time.Time, subject string) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Subject:   subject,
		ExpiresAt: jwt.NewNumericDate(exp),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenStr, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("failed to create test JWT: %v", err)
	}
	return tokenStr
}

func TestIAPTokenProviderFromMetadataServer(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	var tokenExp = time.Now().Add(1 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)

		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor header", http.StatusBadRequest)
			return
		}

		aud := r.URL.Query().Get("audience")
		if aud != "expected-audience-123" {
			http.Error(w, "unexpected audience", http.StatusBadRequest)
			return
		}

		jwtToken := createTestJWT(t, tokenExp, "sa@example.iam.gserviceaccount.com")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jwtToken))
	}))
	defer server.Close()

	provider := NewIAPTokenProvider("expected-audience-123")
	provider.MetadataURL = server.URL + "/computeMetadata/v1/instance/service-accounts/default/identity?audience=expected-audience-123&format=full"
	provider.HTTPClient = server.Client()

	ctx := context.Background()

	// First fetch should query metadata server
	tok1, err := provider.Token(ctx)
	if err != nil {
		t.Fatalf("Token() failed: %v", err)
	}
	if tok1 == "" {
		t.Fatal("expected non-empty token")
	}
	if requestCount.Load() != 1 {
		t.Errorf("expected 1 request to metadata server, got %d", requestCount.Load())
	}

	// Second fetch should return cached token without contacting metadata server
	tok2, err := provider.Token(ctx)
	if err != nil {
		t.Fatalf("second Token() failed: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("cached token mismatch: got %q, want %q", tok2, tok1)
	}
	if requestCount.Load() != 1 {
		t.Errorf("expected cached token to not query server, requestCount = %d", requestCount.Load())
	}
}

func TestIAPTokenProviderRefreshesNearExpiry(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		// Return token expiring in 30 seconds (within 1 min refresh window)
		exp := time.Now().Add(30 * time.Second)
		if count > 1 {
			// Second response expires in 1 hour
			exp = time.Now().Add(1 * time.Hour)
		}
		jwtToken := createTestJWT(t, exp, fmt.Sprintf("call-%d", count))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(jwtToken))
	}))
	defer server.Close()

	provider := NewIAPTokenProvider("refresh-audience")
	provider.MetadataURL = server.URL + "/computeMetadata/v1/instance/service-accounts/default/identity?audience=refresh-audience&format=full"
	provider.HTTPClient = server.Client()

	ctx := context.Background()

	tok1, err := provider.Token(ctx)
	if err != nil {
		t.Fatalf("first Token() failed: %v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount.Load())
	}

	// Because tok1 expires within 30s (< 1 min buffer), second call should refresh
	tok2, err := provider.Token(ctx)
	if err != nil {
		t.Fatalf("second Token() failed: %v", err)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected token refresh (requestCount 2), got %d", requestCount.Load())
	}
	if tok1 == tok2 {
		t.Errorf("expected new refreshed token, got identical token")
	}
}

type staticTokenSource struct {
	token *oauth2.Token
	err   error
}

func (s *staticTokenSource) Token() (*oauth2.Token, error) {
	return s.token, s.err
}

func TestIAPTokenProviderFallbackToADC(t *testing.T) {
	t.Parallel()

	// Server returning 404 (simulating non-GCE environment)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not on GCE", http.StatusNotFound)
	}))
	defer server.Close()

	expectedToken := "adc-oidc-id-token-abc"
	mockSource := &staticTokenSource{
		token: &oauth2.Token{
			AccessToken: expectedToken,
			Expiry:      time.Now().Add(30 * time.Minute),
		},
	}

	provider := NewIAPTokenProvider("adc-audience")
	provider.MetadataURL = server.URL + "/computeMetadata/v1/instance/service-accounts/default/identity?audience=adc-audience&format=full"
	provider.HTTPClient = server.Client()
	provider.FallbackSource = mockSource

	ctx := context.Background()
	tok, err := provider.Token(ctx)
	if err != nil {
		t.Fatalf("Token() with fallback failed: %v", err)
	}
	if tok != expectedToken {
		t.Errorf("got token %q, want %q", tok, expectedToken)
	}
}

func TestIAPTokenProviderEmptyAudience(t *testing.T) {
	t.Parallel()

	provider := NewIAPTokenProvider("")
	_, err := provider.Token(context.Background())
	if err == nil {
		t.Fatal("expected error with empty audience, got nil")
	}
}

func TestParseTokenExpiry(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	jwtStr := createTestJWT(t, future, "sub1")

	exp := parseTokenExpiry(jwtStr)
	if exp.Unix() != future.Unix() {
		t.Errorf("got expiry %v, want %v", exp, future)
	}

	// Malformed JWT should return default ~50 minute expiry without panic
	fallbackExp := parseTokenExpiry("not-a-valid-jwt-token")
	if time.Until(fallbackExp) < 40*time.Minute || time.Until(fallbackExp) > 55*time.Minute {
		t.Errorf("unexpected fallback expiry: %v", fallbackExp)
	}
}
