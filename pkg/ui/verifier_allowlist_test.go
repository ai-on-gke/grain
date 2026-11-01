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

package ui_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/golang-jwt/jwt/v5"
)

// fakeHeaderVerifier is a test IdentityVerifier that models wrapper verifiers
// (including non-comparable fields such as slices and functions).
type fakeHeaderVerifier struct {
	name           string
	headers        []string
	accessCodeWins bool
	// nonComparable ensures RegisterIdentityVerifier and its unregister closure
	// never compare IdentityVerifier interface values with ==, which would panic
	// on wrapper structs holding slices, maps, or functions.
	nonComparable func()
	verifyFn      func(r *http.Request, raw string) (ui.VerifiedIdentity, error)
}

func (f *fakeHeaderVerifier) Name() string      { return f.name }
func (f *fakeHeaderVerifier) Headers() []string { return f.headers }

func (f *fakeHeaderVerifier) Verify(r *http.Request) (ui.VerifiedIdentity, bool, error) {
	for _, h := range f.headers {
		if raw := strings.TrimSpace(r.Header.Get(h)); raw != "" {
			id, err := f.verifyFn(r, raw)
			if err != nil {
				return ui.VerifiedIdentity{}, false, err
			}
			return id, true, nil
		}
	}
	return ui.VerifiedIdentity{}, false, nil
}

type fakePrecedenceVerifier struct {
	*fakeHeaderVerifier
}

func (f *fakePrecedenceVerifier) AccessCodeWins() bool {
	return f.accessCodeWins
}

func parseSimpleSignedHeader(_ *http.Request, raw string) (ui.VerifiedIdentity, error) {
	if strings.HasPrefix(raw, "bad:") || raw == "forged" {
		return ui.VerifiedIdentity{}, errors.New("signature verification failed")
	}
	email := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "ok:")))
	if email == "" {
		return ui.VerifiedIdentity{}, errors.New("missing email")
	}
	kind := model.PrincipalHuman
	if strings.HasSuffix(email, ".gserviceaccount.com") {
		kind = model.PrincipalService
	}
	return ui.VerifiedIdentity{Email: email, Kind: kind}, nil
}

func TestIdentityVerifierRegistryOrderRefusalAndProxyHeaders(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	v1 := &fakeHeaderVerifier{
		name:          "proxy-one",
		headers:       []string{"X-Proxy-One"},
		nonComparable: func() {},
		verifyFn:      parseSimpleSignedHeader,
	}
	v2 := &fakeHeaderVerifier{
		name:          "proxy-two",
		headers:       []string{"X-Proxy-Two"},
		nonComparable: func() {},
		verifyFn:      parseSimpleSignedHeader,
	}
	unreg1 := ui.RegisterIdentityVerifier(v1)
	t.Cleanup(unreg1)
	unreg2 := ui.RegisterIdentityVerifier(v2)
	t.Cleanup(unreg2)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{"user:one@example.com", "user:two@example.com"}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	// 1. Neither header present -> 401.
	if rec := serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/auth/user", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header: got %d, want 401", rec.Code)
	}

	// 2. Only second verifier's header present -> v1 returns ok=false, v2 verifies.
	rec := serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/auth/user", "", map[string]string{
		"X-Proxy-Two": "ok:two@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("second verifier: got %d, want 200: %s", rec.Code, rec.Body)
	}
	var authResp ui.AuthUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatal(err)
	}
	if !authResp.Authenticated || authResp.User != "two@example.com" || authResp.AuthMethod != "proxy-two" {
		t.Fatalf("second verifier auth/user = %+v, want authenticated two@example.com via proxy-two", authResp)
	}

	// 3. Both headers present and valid -> first registered verifier wins.
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/auth/user", "", map[string]string{
		"X-Proxy-One": "ok:one@example.com",
		"X-Proxy-Two": "ok:two@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("both verifiers: got %d, want 200: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatal(err)
	}
	if authResp.User != "one@example.com" || authResp.AuthMethod != "proxy-one" {
		t.Fatalf("both verifiers auth/user = %+v, want one@example.com via proxy-one", authResp)
	}

	// 4. First verifier's header present but INVALID, alongside valid second
	// verifier header and valid access code -> 401, never falls through!
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/tasks", "", map[string]string{
		"X-Proxy-One":     "bad:forged",
		"X-Proxy-Two":     "ok:two@example.com",
		"Authorization":   "Bearer " + testAccessCode,
		"X-Grain-User":    "two@example.com",
		"X-Forwarded-For": "203.0.113.10",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("failed first verifier fell through: got %d, want 401: %s", rec.Code, rec.Body)
	}

	// 5. Unregistering v1 (idempotent) removes it so v2 handles requests alone.
	unreg1()
	unreg1()
	rec = serveFrom(srv, "203.0.113.10:1234", http.MethodGet, "/api/auth/user", "", map[string]string{
		"X-Proxy-One": "bad:ignored-after-unregister",
		"X-Proxy-Two": "ok:two@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("after unregistering v1: got %d, want 200: %s", rec.Code, rec.Body)
	}

	// 6. Verifier Headers() count as proxy headers (relayedByProxy): a loopback
	// request carrying a registered verifier's header is treated as proxied, not
	// loopback-exempt, for access-code rate limiting.
	unregBreakGlass := ui.RegisterIdentityVerifier(&fakePrecedenceVerifier{
		fakeHeaderVerifier: &fakeHeaderVerifier{
			name:           "loopback-proxy",
			headers:        []string{"X-Custom-Proxy-Sig"},
			accessCodeWins: true,
			verifyFn:       parseSimpleSignedHeader,
		},
	})
	t.Cleanup(unregBreakGlass)

	const wrongCode = "WRNG-ACCESS-CODE-9999"
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		r := serveFrom(srv, "127.0.0.1:4242", http.MethodGet, "/api/tasks", "", map[string]string{
			"X-Custom-Proxy-Sig": "ok:two@example.com",
			"Authorization":      "Bearer " + wrongCode,
		})
		if r.Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatalf("expected verifier header X-Custom-Proxy-Sig on loopback to count as relayedByProxy and trigger rate limit")
	}
}

// TestFakeVerifierContractPinsBothAccessCodePrecedenceModes pins the exact
// seam contract wrapper builds (such as Google's signed ÜberProxy verifier)
// rely on, including both AccessCodeWins()==false and AccessCodeWins()==true.
func TestFakeVerifierContractPinsBothAccessCodePrecedenceModes(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	t.Run("default verifier: verified assertion beats access code", func(t *testing.T) {
		unreg := ui.RegisterIdentityVerifier(&fakeHeaderVerifier{
			name:     "strict-proxy",
			headers:  []string{"X-Strict-Assertion"},
			verifyFn: parseSimpleSignedHeader,
		})
		defer unreg()

		srv, client := testServer(t)
		client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
		ui.SetAccessCodeForTest(srv, testAccessCode)

		// Sign in once without assertion to mint a valid grain_access session cookie.
		cookieVal := mintAccessCookieForTest(t, srv, testAccessCode)

		// Admitted caller carrying BOTH assertion and bearer access code -> assertion wins.
		rec := serveFrom(srv, "198.51.100.5:1234", http.MethodGet, "/api/auth/user", "", map[string]string{
			"X-Strict-Assertion": "ok:alice@example.com",
			"Authorization":      "Bearer " + testAccessCode,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("admitted + bearer code: got %d, want 200: %s", rec.Code, rec.Body)
		}
		var got ui.AuthUserResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if !got.Authenticated || got.User != "alice@example.com" || got.AuthMethod != "strict-proxy" {
			t.Fatalf("admitted + bearer code: got %+v, want alice@example.com via strict-proxy (assertion wins)", got)
		}

		// Unallowed caller carrying BOTH valid assertion and valid bearer/cookie access code -> 403!
		for mode, hdrs := range map[string]map[string]string{
			"bearer": {
				"X-Strict-Assertion": "ok:mallory@example.com",
				"Authorization":      "Bearer " + testAccessCode,
			},
			"cookie": {
				"X-Strict-Assertion": "ok:mallory@example.com",
				"Cookie":             ui.AccessCookieName + "=" + cookieVal,
			},
		} {
			rec := serveFrom(srv, "198.51.100.5:1234", http.MethodGet, "/api/tasks", "", hdrs)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "mallory@example.com") {
				t.Fatalf("unallowed + %s access code: got %d, want 403 naming mallory@example.com: %s", mode, rec.Code, rec.Body)
			}
		}

		// Bad assertion + valid access code -> 401 (no fallback).
		rec = serveFrom(srv, "198.51.100.5:1234", http.MethodGet, "/api/tasks", "", map[string]string{
			"X-Strict-Assertion": "bad:corrupt",
			"Authorization":      "Bearer " + testAccessCode,
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bad assertion + access code: got %d, want 401: %s", rec.Code, rec.Body)
		}
	})

	t.Run("AccessCodeWins verifier: valid access code beats verified identity for break-glass", func(t *testing.T) {
		unreg := ui.RegisterIdentityVerifier(&fakePrecedenceVerifier{
			fakeHeaderVerifier: &fakeHeaderVerifier{
				name:           "uberproxy",
				headers:        []string{"X-UberProxy-Signed-UpTick"},
				accessCodeWins: true,
				verifyFn:       parseSimpleSignedHeader,
			},
		})
		defer unreg()

		srv, client := testServer(t)
		// Configure a group allowlist whose group checker is completely broken,
		// modeling the exact break-glass scenario in the design doc.
		client.Config.AllowedPrincipals = []string{"user:alice@example.com", "group:ops@example.com"}
		client.Config.GroupChecker = groupCheckerFunc(func(ctx context.Context, group, member string) (bool, error) {
			return false, errors.New("cloud identity outage")
		})
		ui.SetAccessCodeForTest(srv, testAccessCode)
		cookieVal := mintAccessCookieForTest(t, srv, testAccessCode)

		// 1. Without the access code: verified identity is used.
		// Direct match (alice@example.com) succeeds without touching broken group lookup.
		rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/auth/user", "", map[string]string{
			"X-UberProxy-Signed-UpTick": "ok:alice@example.com",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("alice without code: got %d, want 200: %s", rec.Code, rec.Body)
		}
		var got ui.AuthUserResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if !got.Authenticated || got.User != "alice@example.com" || got.AuthMethod != "uberproxy" {
			t.Fatalf("alice without code: got %+v, want alice@example.com via uberproxy", got)
		}

		// Caller relying on broken group lookup without access code -> refused 403!
		rec = serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", map[string]string{
			"X-UberProxy-Signed-UpTick": "ok:oncall@example.com",
		})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "oncall@example.com") {
			t.Fatalf("oncall without code during group outage: got %d, want 403 naming oncall@example.com: %s", rec.Code, rec.Body)
		}

		// 2. Break-glass WITH valid access code (both bearer and session cookie):
		// judged by the code alone, succeeds as trusted operator despite group outage!
		for mode, hdrs := range map[string]map[string]string{
			"bearer": {
				"X-UberProxy-Signed-UpTick": "ok:oncall@example.com",
				"Authorization":             "Bearer " + testAccessCode,
			},
			"cookie": {
				"X-UberProxy-Signed-UpTick": "ok:oncall@example.com",
				"Cookie":                    ui.AccessCookieName + "=" + cookieVal,
			},
		} {
			recTasks := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", hdrs)
			if recTasks.Code != http.StatusOK {
				t.Fatalf("break-glass (%s) GET /api/tasks: got %d, want 200: %s", mode, recTasks.Code, recTasks.Body)
			}
			recAuth := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/auth/user", "", hdrs)
			if recAuth.Code != http.StatusOK {
				t.Fatalf("break-glass (%s) GET /api/auth/user: got %d, want 200: %s", mode, recAuth.Code, recAuth.Body)
			}
			var authGot ui.AuthUserResponse
			_ = json.Unmarshal(recAuth.Body.Bytes(), &authGot)
			if authGot.Authenticated || authGot.User != "" || authGot.AuthMethod != ui.AuthMethodTrusted {
				t.Fatalf("break-glass (%s) /api/auth/user = %+v, want trusted unnamed operator", mode, authGot)
			}
		}

		// 3. Wrong bearer access code alongside valid proxy header -> 401 (no silent fallback to alice).
		rec = serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", map[string]string{
			"X-UberProxy-Signed-UpTick": "ok:alice@example.com",
			"Authorization":             "Bearer WRNG-ACCESS-CODE-9999",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong break-glass code: got %d, want 401: %s", rec.Code, rec.Body)
		}

		// 4. Failed proxy signature verification STILL fails closed with 401 and no fallback,
		// even when the caller presents a valid access code or session cookie!
		for mode, hdrs := range map[string]map[string]string{
			"bearer": {
				"X-UberProxy-Signed-UpTick": "bad:tampered",
				"Authorization":             "Bearer " + testAccessCode,
			},
			"cookie": {
				"X-UberProxy-Signed-UpTick": "bad:tampered",
				"Cookie":                    ui.AccessCookieName + "=" + cookieVal,
			},
		} {
			rec := serveFrom(srv, "198.51.100.8:1234", http.MethodGet, "/api/tasks", "", hdrs)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("bad proxy uptick + valid %s access code: got %d, want 401: %s", mode, rec.Code, rec.Body)
			}
		}
	})
}

type groupCheckerFunc func(ctx context.Context, groupEmail, memberEmail string) (bool, error)

func (fn groupCheckerFunc) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	return fn(ctx, groupEmail, memberEmail)
}

func mintAccessCookieForTest(t *testing.T, srv *ui.Server, code string) string {
	t.Helper()
	form := url.Values{"code": {code}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/access-code", strings.NewReader(form.Encode()))
	req.RemoteAddr = "127.0.0.1:4242"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /api/auth/access-code: got %d, want 303: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == ui.AccessCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no %s cookie returned", ui.AccessCookieName)
	return ""
}

func TestIAPPrincipalsNormalizationClockSkewAndServiceAccounts(t *testing.T) {
	logs := ui.LogBufferForTest(t)
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)

	srv, client := testServer(t)
	client.Config.AllowedPrincipals = []string{
		"user:alice.smith@example.com",
		"serviceAccount:hostd-runner@my-proj.iam.gserviceaccount.com",
	}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	// 1. Mixed-case human email with accounts.google.com: prefix -> lowercased PrincipalHuman.
	humanTok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "accounts.google.com:Alice.Smith@Example.COM", "sub-1", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	reqCheck := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	reqCheck.Header.Set(ui.IAPAssertionHeader, humanTok)
	p, ok := ui.ExtractPrincipal(reqCheck)
	if !ok || p != (model.Principal{Kind: model.PrincipalHuman, ID: "alice.smith@example.com"}) {
		t.Fatalf("ExtractPrincipal(human) = %+v, %v; want {human alice.smith@example.com}", p, ok)
	}

	// 2. *.gserviceaccount.com -> lowercased PrincipalService, admitted across API routes.
	saTok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "Hostd-Runner@My-Proj.IAM.GServiceAccount.com", "sub-sa", ui.GoogleIAPIssuer, time.Now().Add(5*time.Minute))
	reqSA := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	reqSA.Header.Set(ui.IAPAssertionHeader, saTok)
	pSA, ok := ui.ExtractPrincipal(reqSA)
	if !ok || pSA != (model.Principal{Kind: model.PrincipalService, ID: "hostd-runner@my-proj.iam.gserviceaccount.com"}) {
		t.Fatalf("ExtractPrincipal(serviceAccount) = %+v, %v; want {service hostd-runner@my-proj.iam.gserviceaccount.com}", pSA, ok)
	}
	if !pSA.Kind.Valid() {
		t.Fatalf("PrincipalService.Valid() = false, want true")
	}

	// Service account can call GET /api/auth/user and POST /api/tasks (no cookies -> programmatic client passes CSRF).
	recSAUser := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/auth/user", "", map[string]string{
		ui.IAPAssertionHeader: saTok,
	})
	if recSAUser.Code != http.StatusOK {
		t.Fatalf("SA GET /api/auth/user: got %d: %s", recSAUser.Code, recSAUser.Body)
	}
	var saAuth ui.AuthUserResponse
	_ = json.Unmarshal(recSAUser.Body.Bytes(), &saAuth)
	if !saAuth.Authenticated || saAuth.AuthMethod != ui.AuthMethodIAP || saAuth.User != "hostd-runner@my-proj.iam.gserviceaccount.com" {
		t.Fatalf("SA /api/auth/user = %+v", saAuth)
	}

	recCreate := serveFrom(srv, "203.0.113.20:443", http.MethodPost, "/api/tasks", `{"title":"SA task","body":"created by SA","repo":"acme/widgets"}`, map[string]string{
		ui.IAPAssertionHeader: saTok,
		"Content-Type":        "application/json",
	})
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("SA POST /api/tasks: got %d, want 201: %s", recCreate.Code, recCreate.Body)
	}

	// 3. Clock skew on exp/iat: token expired 10s ago or issued 10s in the future is accepted within 30s leeway;
	// token expired 2m ago is rejected with 401.
	skewTok := func(iat, exp time.Time) string {
		claims := ui.IAPClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    ui.GoogleIAPIssuer,
				Audience:  jwt.ClaimStrings{tunnelAudience},
				IssuedAt:  jwt.NewNumericDate(iat),
				ExpiresAt: jwt.NewNumericDate(exp),
			},
			Email: "alice.smith@example.com",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = tunnelKid
		s, err := tok.SignedString(priv)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	now := time.Now()
	withinSkew := skewTok(now.Add(10*time.Second), now.Add(-10*time.Second))
	if rec := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{ui.IAPAssertionHeader: withinSkew}); rec.Code != http.StatusOK {
		t.Fatalf("assertion within 30s clock skew: got %d, want 200: %s", rec.Code, rec.Body)
	}
	pastSkew := skewTok(now.Add(-5*time.Minute), now.Add(-2*time.Minute))
	if rec := serveFrom(srv, "203.0.113.20:443", http.MethodGet, "/api/tasks", "", map[string]string{
		ui.IAPAssertionHeader: pastSkew,
		"Authorization":       "Bearer " + testAccessCode,
	}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired assertion + access code: got %d, want 401: %s", rec.Code, rec.Body)
	}

	// 4. Plaintext identity headers never authenticate and never override IAP identity.
	spoofHeaders := map[string]string{
		"X-Goog-Authenticated-User-Email": "accounts.google.com:alice.smith@example.com",
		"X-Grain-User":                    "alice.smith@example.com",
		"X-User-LDAP":                     "alice.smith",
		"X-Forwarded-User":                "alice.smith@example.com",
		"Grain-User":                      "alice.smith@example.com",
	}
	if rec := serveFrom(srv, "127.0.0.1:4242", http.MethodGet, "/api/tasks", "", spoofHeaders); rec.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed plaintext headers alone: got %d, want 401: %s", rec.Code, rec.Body)
	}
	logOutput := logs.String()
	if !strings.Contains(logOutput, "refused an X-Goog-IAP-JWT-Assertion") {
		t.Errorf("expected logRefusedAssertion diagnostic in logs, got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "ignored the unsigned X-Goog-Authenticated-User-Email header") {
		t.Errorf("expected logIgnoredIdentityHeader diagnostic in logs, got:\n%s", logOutput)
	}
}

func TestAllowedPrincipalsDefaultsAndCloudIdentityGroups(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, false)
	ui.SetIAPAudience(tunnelAudience)

	mint := func(email string) string {
		return signIAPToken(t, priv, tunnelKid, tunnelAudience, email, "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))
	}

	t.Run("default allowlist admits owner and provisioned service accounts only", func(t *testing.T) {
		srv, client := testServer(t)
		client.Config.Actor = ui.DefaultActor("owner@example.com")
		client.Config.ProvisionedServiceAccounts = []string{"extra-sa@my-proj.iam.gserviceaccount.com"}
		storeSettingsRow(t, client)
		ctx := context.Background()
		mc, err := client.Store.GetConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mc.GCPProject = "my-proj"
		mc.GCPServiceAccountEmail = "grain-agent@my-proj.iam.gserviceaccount.com"
		mc.GCPServiceAccountCount = 2
		if err := client.Store.PutConfig(ctx, *mc); err != nil {
			t.Fatal(err)
		}

		// Record any Cloud Identity call: default allowlist has no group: entries, so 0 calls may occur.
		var groupCalls atomic.Int32
		client.Config.GroupChecker = groupCheckerFunc(func(ctx context.Context, g, m string) (bool, error) {
			groupCalls.Add(1)
			return true, nil
		})

		for _, admitted := range []string{
			"owner@example.com",
			"extra-sa@my-proj.iam.gserviceaccount.com",
			"grain-daemon@my-proj.iam.gserviceaccount.com",
			"grain-agent-0@my-proj.iam.gserviceaccount.com",
			"grain-agent-1@my-proj.iam.gserviceaccount.com",
		} {
			rec := serveFrom(srv, "203.0.113.9:443", http.MethodGet, "/api/tasks", "", map[string]string{
				ui.IAPAssertionHeader: mint(admitted),
			})
			if rec.Code != http.StatusOK {
				t.Errorf("default allowlist refused %q: got %d: %s", admitted, rec.Code, rec.Body)
			}
		}

		for _, denied := range []string{
			"stranger@example.com",
			"grain-agent-2@my-proj.iam.gserviceaccount.com",
		} {
			rec := serveFrom(srv, "203.0.113.9:443", http.MethodGet, "/api/tasks", "", map[string]string{
				ui.IAPAssertionHeader: mint(denied),
			})
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), denied) {
				t.Errorf("default allowlist for %q: got %d %q, want 403 naming %q", denied, rec.Code, rec.Body.String(), denied)
			}
		}

		if got := groupCalls.Load(); got != 0 {
			t.Fatalf("Cloud Identity called %d times with no group: entries configured, want 0", got)
		}
	})

	t.Run("domain, user, serviceAccount and Cloud Identity group lookup with caching and optionality", func(t *testing.T) {
		var lookupCalls atomic.Int32
		var membershipCalls atomic.Int32
		var failAPI atomic.Bool

		ciSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failAPI.Load() {
				http.Error(w, `{"error":"backend unavailable"}`, http.StatusInternalServerError)
				return
			}
			switch {
			case r.URL.Path == "/v1/groups:lookup":
				lookupCalls.Add(1)
				if r.URL.Query().Get("groupKey.id") == "grain-admins@example.com" {
					_ = json.NewEncoder(w).Encode(map[string]string{"name": "groups/01abcxyz"})
					return
				}
				http.NotFound(w, r)
			case r.URL.Path == "/v1/groups/01abcxyz/memberships:checkTransitiveMembership":
				membershipCalls.Add(1)
				q := r.URL.Query().Get("query")
				isMember := strings.Contains(q, "'groupmember@other.org'")
				_ = json.NewEncoder(w).Encode(map[string]bool{"hasMembership": isMember})
			default:
				http.NotFound(w, r)
			}
		}))
		defer ciSrv.Close()

		fakeNow := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		checker := ui.NewCloudIdentityGroupChecker(
			ui.WithCloudIdentityEndpoint(ciSrv.URL),
			ui.WithCloudIdentityHTTPClient(ciSrv.Client()),
			ui.WithCloudIdentityCacheTTL(5*time.Minute),
			ui.WithCloudIdentityClock(func() time.Time { return fakeNow }),
		)

		srv, client := testServer(t)
		client.Config.AllowedPrincipals = []string{
			"user:direct@outside.org",
			"serviceAccount:bot@proj.iam.gserviceaccount.com",
			"domain:corp.example.com",
			"group:grain-admins@example.com",
		}
		client.Config.GroupChecker = checker

		// 1. Direct entries (user:, serviceAccount:, domain:) succeed even while Cloud Identity is failing (500),
		// and make ZERO Cloud Identity HTTP calls!
		failAPI.Store(true)
		for _, direct := range []string{
			"direct@outside.org",
			"bot@proj.iam.gserviceaccount.com",
			"engineer@corp.example.com",
		} {
			rec := serveFrom(srv, "203.0.113.11:443", http.MethodGet, "/api/tasks", "", map[string]string{
				ui.IAPAssertionHeader: mint(direct),
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("direct match %q failed during Cloud Identity outage: %d %s", direct, rec.Code, rec.Body)
			}
		}
		if lookupCalls.Load() != 0 || membershipCalls.Load() != 0 {
			t.Fatalf("Cloud Identity called (%d, %d) for direct matches, want (0, 0)", lookupCalls.Load(), membershipCalls.Load())
		}

		// 2. While Cloud Identity is failing (500), a caller depending on group: is refused with 403 naming email.
		recFail := serveFrom(srv, "203.0.113.11:443", http.MethodGet, "/api/tasks", "", map[string]string{
			ui.IAPAssertionHeader: mint("groupmember@other.org"),
		})
		if recFail.Code != http.StatusForbidden || !strings.Contains(recFail.Body.String(), "groupmember@other.org") {
			t.Fatalf("group outage refusal: got %d %s, want 403 naming groupmember@other.org", recFail.Code, recFail.Body)
		}

		// 3. Recovery: failed lookup was NOT cached, so when Cloud Identity recovers, the very next call succeeds!
		failAPI.Store(false)
		lookupCalls.Store(0)
		membershipCalls.Store(0)

		for i := 0; i < 3; i++ {
			rec := serveFrom(srv, "203.0.113.11:443", http.MethodGet, "/api/tasks", "", map[string]string{
				ui.IAPAssertionHeader: mint("groupmember@other.org"),
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("group member attempt %d: got %d: %s", i, rec.Code, rec.Body)
			}
		}
		// Cached across the 3 calls!
		if lookupCalls.Load() != 1 || membershipCalls.Load() != 1 {
			t.Fatalf("expected 1 lookup + 1 membership call across 3 cached requests, got (%d, %d)", lookupCalls.Load(), membershipCalls.Load())
		}

		// Non-member is refused with 403 naming email, and negative membership is also cached.
		for i := 0; i < 2; i++ {
			rec := serveFrom(srv, "203.0.113.11:443", http.MethodGet, "/api/tasks", "", map[string]string{
				ui.IAPAssertionHeader: mint("intruder@other.org"),
			})
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "intruder@other.org") {
				t.Fatalf("non-member attempt %d: got %d: %s", i, rec.Code, rec.Body)
			}
		}
		if membershipCalls.Load() != 2 {
			t.Fatalf("expected negative membership to be cached (total 2 membership calls), got %d", membershipCalls.Load())
		}

		// Advance clock past 5m TTL -> cache expires and re-queries Cloud Identity!
		fakeNow = fakeNow.Add(6 * time.Minute)
		rec := serveFrom(srv, "203.0.113.11:443", http.MethodGet, "/api/tasks", "", map[string]string{
			ui.IAPAssertionHeader: mint("groupmember@other.org"),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("after TTL expiry: got %d: %s", rec.Code, rec.Body)
		}
		if membershipCalls.Load() != 3 {
			t.Fatalf("expected re-query after TTL expiry (total 3 membership calls), got %d", membershipCalls.Load())
		}
	})

	t.Run("ParseAllowedPrincipals rejects malformed entries", func(t *testing.T) {
		for _, bad := range []string{
			"alice@example.com",
			"unknown:alice@example.com",
			"user:",
			"user:not-an-email",
			"group:not-an-email",
			"serviceAccount:not-an-email",
			"domain:user@example.com",
		} {
			if _, err := ui.ParseAllowedPrincipals([]string{bad}); err == nil {
				t.Errorf("ParseAllowedPrincipals(%q) succeeded, want error", bad)
			}
		}
	})
}

func TestVerifiedIdentityCSRFLogoutAndUnchangedNoIAPPath(t *testing.T) {
	t.Run("CSRF check on verified identity browser requests", func(t *testing.T) {
		priv, pemStr := generateECDSATestKey(t)
		assertionKeys(t, priv, pemStr, false)
		ui.SetIAPAudience(tunnelAudience)

		srv, client := testServer(t)
		client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
		tok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))

		// GET with browser cookie and no Origin/Sec-Fetch-Site -> allowed (200).
		if rec := serveFrom(srv, "203.0.113.30:443", http.MethodGet, "/api/tasks", "", map[string]string{
			ui.IAPAssertionHeader: tok,
			"Cookie":              "GCP_IAPLOGIN=session123",
		}); rec.Code != http.StatusOK {
			t.Fatalf("GET with cookie: got %d, want 200: %s", rec.Code, rec.Body)
		}

		// POST with browser cookie and NO Origin or Sec-Fetch-Site -> refused 403!
		if rec := serveFrom(srv, "203.0.113.30:443", http.MethodPost, "/api/tasks", `{"title":"csrf","repo":"a/b"}`, map[string]string{
			ui.IAPAssertionHeader: tok,
			"Cookie":              "GCP_IAPLOGIN=session123",
			"Content-Type":        "application/json",
		}); rec.Code != http.StatusForbidden {
			t.Fatalf("POST with cookie and neither Sec-Fetch-Site nor Origin: got %d, want 403: %s", rec.Code, rec.Body)
		}

		// POST with Sec-Fetch-Site: cross-site -> refused 403!
		if rec := serveFrom(srv, "203.0.113.30:443", http.MethodPost, "/api/tasks", `{"title":"csrf","repo":"a/b"}`, map[string]string{
			ui.IAPAssertionHeader: tok,
			"Cookie":              "GCP_IAPLOGIN=session123",
			"Sec-Fetch-Site":      "cross-site",
			"Content-Type":        "application/json",
		}); rec.Code != http.StatusForbidden {
			t.Fatalf("POST cross-site: got %d, want 403: %s", rec.Code, rec.Body)
		}

		// POST with mismatched Origin -> refused 403!
		reqBadOrigin := httptest.NewRequest(http.MethodPost, "https://grain.example.com/api/tasks", strings.NewReader(`{"title":"csrf","repo":"a/b"}`))
		reqBadOrigin.RemoteAddr = "203.0.113.30:443"
		reqBadOrigin.Host = "grain.example.com"
		reqBadOrigin.Header.Set(ui.IAPAssertionHeader, tok)
		reqBadOrigin.Header.Set("Cookie", "GCP_IAPLOGIN=session123")
		reqBadOrigin.Header.Set("Origin", "https://evil.example.com")
		reqBadOrigin.Header.Set("Content-Type", "application/json")
		recBadOrigin := httptest.NewRecorder()
		srv.ServeHTTP(recBadOrigin, reqBadOrigin)
		if recBadOrigin.Code != http.StatusForbidden {
			t.Fatalf("POST mismatched Origin: got %d, want 403: %s", recBadOrigin.Code, recBadOrigin.Body)
		}

		// POST with matching Origin against Host -> 201!
		reqGoodOrigin := httptest.NewRequest(http.MethodPost, "https://grain.example.com/api/tasks", strings.NewReader(`{"title":"good origin","repo":"a/b"}`))
		reqGoodOrigin.RemoteAddr = "203.0.113.30:443"
		reqGoodOrigin.Host = "grain.example.com"
		reqGoodOrigin.Header.Set(ui.IAPAssertionHeader, tok)
		reqGoodOrigin.Header.Set("Cookie", "GCP_IAPLOGIN=session123")
		reqGoodOrigin.Header.Set("Origin", "https://grain.example.com")
		reqGoodOrigin.Header.Set("Content-Type", "application/json")
		recGoodOrigin := httptest.NewRecorder()
		srv.ServeHTTP(recGoodOrigin, reqGoodOrigin)
		if recGoodOrigin.Code != http.StatusCreated {
			t.Fatalf("POST matching Origin: got %d, want 201: %s", recGoodOrigin.Code, recGoodOrigin.Body)
		}

		// POST with Sec-Fetch-Site: same-origin -> 201!
		if rec := serveFrom(srv, "203.0.113.30:443", http.MethodPost, "/api/tasks", `{"title":"same-origin","repo":"a/b"}`, map[string]string{
			ui.IAPAssertionHeader: tok,
			"Cookie":              "GCP_IAPLOGIN=session123",
			"Sec-Fetch-Site":      "same-origin",
			"Content-Type":        "application/json",
		}); rec.Code != http.StatusCreated {
			t.Fatalf("POST same-origin: got %d, want 201: %s", rec.Code, rec.Body)
		}
	})

	t.Run("IAP logout redirects to /_gcp_iap/clear_login_cookie", func(t *testing.T) {
		priv, pemStr := generateECDSATestKey(t)
		assertionKeys(t, priv, pemStr, false)
		ui.SetIAPAudience(tunnelAudience)

		srv, client := testServer(t)
		client.Config.AllowedPrincipals = []string{"user:alice@example.com"}
		tok := signIAPToken(t, priv, tunnelKid, tunnelAudience, "alice@example.com", "sub", ui.GoogleIAPIssuer, time.Now().Add(10*time.Minute))

		// GET /api/auth/logout -> 302 to /_gcp_iap/clear_login_cookie
		recGet := serveFrom(srv, "203.0.113.30:443", http.MethodGet, "/api/auth/logout", "", map[string]string{
			ui.IAPAssertionHeader: tok,
		})
		if recGet.Code != http.StatusFound || recGet.Header().Get("Location") != ui.IAPClearLoginCookiePath {
			t.Fatalf("IAP GET /api/auth/logout: got %d Location=%q, want 302 %q", recGet.Code, recGet.Header().Get("Location"), ui.IAPClearLoginCookiePath)
		}

		// POST /api/auth/logout -> JSON with redirect: "/_gcp_iap/clear_login_cookie"
		recPost := serveFrom(srv, "203.0.113.30:443", http.MethodPost, "/api/auth/logout", "", map[string]string{
			ui.IAPAssertionHeader: tok,
			"Sec-Fetch-Site":      "same-origin",
		})
		if recPost.Code != http.StatusOK {
			t.Fatalf("IAP POST /api/auth/logout: got %d: %s", recPost.Code, recPost.Body)
		}
		var out struct {
			LoggedOut bool   `json:"loggedOut"`
			Redirect  string `json:"redirect"`
		}
		if err := json.Unmarshal(recPost.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.LoggedOut || out.Redirect != ui.IAPClearLoginCookiePath {
			t.Fatalf("IAP POST /api/auth/logout = %+v, want redirect %q", out, ui.IAPClearLoginCookiePath)
		}
	})

	t.Run("no IAP configured keeps access code, login page and grain_access unchanged", func(t *testing.T) {
		ui.ResetIAPConfig()
		t.Cleanup(ui.ResetIAPConfig)

		srv, _ := testServer(t)
		ui.SetAccessCodeForTest(srv, testAccessCode)

		// Browser GET / without cookie -> 401 access-code login page.
		recRoot := serveFrom(srv, "203.0.113.40:443", http.MethodGet, "/", "", nil)
		if recRoot.Code != http.StatusUnauthorized || !strings.Contains(recRoot.Body.String(), "Enter access code") {
			t.Fatalf("no-IAP GET /: got %d, want 401 login page", recRoot.Code)
		}

		// GET /api/tasks without code -> 401.
		if rec := serveFrom(srv, "203.0.113.40:443", http.MethodGet, "/api/tasks", "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("no-IAP GET /api/tasks: got %d, want 401", rec.Code)
		}

		// Sign in via /api/auth/access-code -> grain_access cookie admits browser.
		cookieVal := mintAccessCookieForTest(t, srv, testAccessCode)
		if rec := serveFrom(srv, "203.0.113.40:443", http.MethodGet, "/api/tasks", "", map[string]string{
			"Cookie": ui.AccessCookieName + "=" + cookieVal,
		}); rec.Code != http.StatusOK {
			t.Fatalf("no-IAP GET /api/tasks with grain_access cookie: got %d, want 200", rec.Code)
		}

		// Logout without IAP redirects to "/" rather than IAP clear_login_cookie.
		recLogout := serveFrom(srv, "203.0.113.40:443", http.MethodGet, "/api/auth/logout", "", map[string]string{
			"Cookie": ui.AccessCookieName + "=" + cookieVal,
		})
		if recLogout.Code != http.StatusFound || recLogout.Header().Get("Location") != "/" {
			t.Fatalf("no-IAP GET /api/auth/logout: got %d Location=%q, want 302 /", recLogout.Code, recLogout.Header().Get("Location"))
		}
	})
}
