package githubsandbox

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bwsalmon/grain/pkg/github"
)

// testKey is a throwaway RSA key for signing/verifying test JWTs -- never
// used against the real GitHub API, so a small, fast-to-generate key is
// fine here even though GitHub itself requires 2048 bits.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	return key
}

func testAppClient(t *testing.T, transport github.Transport, now time.Time) *realAppClient {
	return &realAppClient{
		transport: transport,
		appID:     "app-123",
		key:       testKey(t),
		now:       func() time.Time { return now },
	}
}

// --- expectStatus --------------------------------------------------------

func TestExpectStatusAcceptsAnyWantedStatus(t *testing.T) {
	resp := github.ApiResponse{Status: 201, Body: []byte(`{}`)}
	if err := expectStatus(resp, 200, 201, 204); err != nil {
		t.Errorf("expectStatus(201, 200/201/204) = %v, want nil", err)
	}
}

func TestExpectStatusRejectsAnUnwantedStatus(t *testing.T) {
	resp := github.ApiResponse{Status: 403, Body: []byte(`{"message":"bad credentials"}`)}
	err := expectStatus(resp, 200, 201)
	if err == nil {
		t.Fatal("want an error for a status not in want")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "bad credentials") {
		t.Errorf("expectStatus error = %q, want it to name the status and body", err.Error())
	}
}

// --- signedJWT -------------------------------------------------------

func TestSignedJWTClaimsMatchGitHubsOwnBounds(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := &realAppClient{appID: "app-123", key: key, now: func() time.Time { return now }}

	tokenString, err := c.signedJWT()
	if err != nil {
		t.Fatal(err)
	}

	var claims jwt.RegisteredClaims
	parser := jwt.NewParser(jwt.WithTimeFunc(func() time.Time { return now }))
	token, err := parser.ParseWithClaims(tokenString, &claims, func(tok *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parsing the signed JWT back: %v", err)
	}
	if !token.Valid {
		t.Fatal("signed JWT did not validate against its own public key")
	}
	if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
		t.Errorf("signing method = %q, want RS256", token.Method.Alg())
	}
	if claims.Issuer != "app-123" {
		t.Errorf("Issuer = %q, want the App ID", claims.Issuer)
	}
	wantIssuedAt := now.Add(-60 * time.Second)
	if !claims.IssuedAt.Time.Equal(wantIssuedAt) {
		t.Errorf("IssuedAt = %v, want %v (backdated 60s for clock skew)", claims.IssuedAt.Time, wantIssuedAt)
	}
	wantExpiresAt := now.Add(9 * time.Minute)
	if !claims.ExpiresAt.Time.Equal(wantExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v (under GitHub's 10-minute ceiling)", claims.ExpiresAt.Time, wantExpiresAt)
	}
}

// --- appRequest / request -----------------------------------------------

func TestAppRequestAuthenticatesWithAFreshJWT(t *testing.T) {
	key := testKey(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`[]`)})
	c := &realAppClient{transport: transport, appID: "app-123", key: key, now: func() time.Time { return now }}

	if _, err := c.appRequest("GET", "/app/installations", nil); err != nil {
		t.Fatal(err)
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(transport.Calls))
	}
	call := transport.Calls[0]
	auth := call.Headers["Authorization"]
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization = %q, want a Bearer JWT", auth)
	}
	var claims jwt.RegisteredClaims
	parser := jwt.NewParser(jwt.WithTimeFunc(func() time.Time { return now }))
	if _, err := parser.ParseWithClaims(strings.TrimPrefix(auth, "Bearer "), &claims, func(tok *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}); err != nil {
		t.Errorf("Authorization header did not carry a JWT signed with the App's own key: %v", err)
	}
	if claims.Issuer != "app-123" {
		t.Errorf("JWT issuer = %q, want the App ID", claims.Issuer)
	}
}

func TestRequestSetsGitHubHeaders(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`{}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.request("POST", "/some/path", "Bearer sometoken", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Headers["Authorization"] != "Bearer sometoken" {
		t.Errorf("Authorization = %q, want the given authorization verbatim", call.Headers["Authorization"])
	}
	if call.Headers["Accept"] != "application/vnd.github+json" {
		t.Errorf("Accept = %q", call.Headers["Accept"])
	}
	if call.Headers["X-GitHub-Api-Version"] != apiVersion {
		t.Errorf("X-GitHub-Api-Version = %q, want %q", call.Headers["X-GitHub-Api-Version"], apiVersion)
	}
	if call.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json when a body is given", call.Headers["Content-Type"])
	}
}

func TestRequestOmitsContentTypeWithNoBody(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`{}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.request("GET", "/some/path", "Bearer sometoken", nil); err != nil {
		t.Fatal(err)
	}
	if ct, ok := transport.Calls[0].Headers["Content-Type"]; ok {
		t.Errorf("Content-Type = %q, want it unset with a nil body", ct)
	}
}

// --- FindInstallation ----------------------------------------------------

func TestFindInstallationReturnsTheOneInstallation(t *testing.T) {
	body := `[{"id":42,"account":{"login":"grain-bot"}}]`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(body)})
	c := testAppClient(t, transport, time.Now())

	inst, err := c.FindInstallation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inst.ID != 42 || inst.Account != "grain-bot" {
		t.Errorf("FindInstallation() = %+v, want {42 grain-bot}", inst)
	}
	if transport.Calls[0].Method != "GET" || transport.Calls[0].Path != "/app/installations?per_page=100" {
		t.Errorf("call = %s %s, want GET /app/installations?per_page=100", transport.Calls[0].Method, transport.Calls[0].Path)
	}
}

func TestFindInstallationErrorsOnZeroInstallations(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`[]`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.FindInstallation(t.Context()); err == nil {
		t.Fatal("want an error when the App has no installations")
	}
}

func TestFindInstallationErrorsOnMultipleInstallations(t *testing.T) {
	body := `[{"id":1,"account":{"login":"org-a"}},{"id":2,"account":{"login":"org-b"}}]`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(body)})
	c := testAppClient(t, transport, time.Now())

	_, err := c.FindInstallation(t.Context())
	if err == nil {
		t.Fatal("want an error when the App has more than one installation")
	}
	if !strings.Contains(err.Error(), "org-a") || !strings.Contains(err.Error(), "org-b") {
		t.Errorf("error = %q, want it to name both ambiguous accounts", err.Error())
	}
}

func TestFindInstallationErrorsOnNonOKStatus(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 401, Body: []byte(`{"message":"Bad credentials"}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.FindInstallation(t.Context()); err == nil {
		t.Fatal("want an error on a non-200 status")
	}
}

func TestFindInstallationErrorsOnUnparsableBody(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`not json`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.FindInstallation(t.Context()); err == nil {
		t.Fatal("want an error on a malformed response body")
	}
}

// --- MintToken -------------------------------------------------------

func TestMintTokenScopedToRepos(t *testing.T) {
	respBody := `{"token":"ghs_abc","expires_at":"2026-01-01T01:00:00Z"}`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 201, Body: []byte(respBody)})
	c := testAppClient(t, transport, time.Now())

	tok, err := c.MintToken(t.Context(), 42, []string{"repo-a"}, map[string]string{"contents": "write"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Token != "ghs_abc" {
		t.Errorf("Token = %q, want ghs_abc", tok.Token)
	}
	wantExpiry := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	if !tok.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, wantExpiry)
	}

	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/app/installations/42/access_tokens" {
		t.Errorf("call = %s %s, want POST /app/installations/42/access_tokens", call.Method, call.Path)
	}
	var sentBody struct {
		Permissions  map[string]string `json:"permissions"`
		Repositories []string          `json:"repositories"`
	}
	if err := json.Unmarshal(call.Body, &sentBody); err != nil {
		t.Fatal(err)
	}
	if len(sentBody.Repositories) != 1 || sentBody.Repositories[0] != "repo-a" {
		t.Errorf("sent repositories = %v, want [repo-a]", sentBody.Repositories)
	}
	if sentBody.Permissions["contents"] != "write" {
		t.Errorf("sent permissions = %v, want contents:write", sentBody.Permissions)
	}
}

func TestMintTokenOmitsRepositoriesKeyWhenInstallationWide(t *testing.T) {
	respBody := `{"token":"ghs_abc","expires_at":"2026-01-01T01:00:00Z"}`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 201, Body: []byte(respBody)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.MintToken(t.Context(), 42, nil, map[string]string{"contents": "write"}); err != nil {
		t.Fatal(err)
	}
	var sentBody map[string]any
	if err := json.Unmarshal(transport.Calls[0].Body, &sentBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := sentBody["repositories"]; ok {
		t.Errorf(`sent body has a "repositories" key with no repos given, want it omitted (installation-wide): %v`, sentBody)
	}
}

func TestMintTokenErrorsOnNonCreatedStatus(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 403, Body: []byte(`{"message":"forbidden"}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.MintToken(t.Context(), 42, nil, nil); err == nil {
		t.Fatal("want an error on a non-201 status")
	}
}

func TestMintTokenErrorsOnUnparsableBody(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 201, Body: []byte(`not json`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.MintToken(t.Context(), 42, nil, nil); err == nil {
		t.Fatal("want an error on a malformed response body")
	}
}

// --- CreateRepo ------------------------------------------------------

func TestCreateRepoSendsAPrivateRepoRequest(t *testing.T) {
	respBody := `{"name":"my-repo","full_name":"grain-bot/my-repo"}`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 201, Body: []byte(respBody)})
	c := testAppClient(t, transport, time.Now())

	repo, err := c.CreateRepo(t.Context(), "sometoken", "grain-bot", "my-repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "my-repo" || repo.FullName != "grain-bot/my-repo" {
		t.Errorf("CreateRepo() = %+v, want {my-repo grain-bot/my-repo}", repo)
	}

	call := transport.Calls[0]
	if call.Method != "POST" || call.Path != "/orgs/grain-bot/repos" {
		t.Errorf("call = %s %s, want POST /orgs/grain-bot/repos", call.Method, call.Path)
	}
	if call.Headers["Authorization"] != "Bearer sometoken" {
		t.Errorf("Authorization = %q, want Bearer sometoken", call.Headers["Authorization"])
	}
	var sentBody struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.Unmarshal(call.Body, &sentBody); err != nil {
		t.Fatal(err)
	}
	if sentBody.Name != "my-repo" || !sentBody.Private {
		t.Errorf("sent body = %+v, want {my-repo true}", sentBody)
	}
}

func TestCreateRepoEscapesTheOrgInThePath(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 201, Body: []byte(`{"name":"r","full_name":"o/r"}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.CreateRepo(t.Context(), "tok", "org/with slash", "r"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(transport.Calls[0].Path, "org/with slash") {
		t.Errorf("path = %q, want the org path-escaped", transport.Calls[0].Path)
	}
}

func TestCreateRepoErrorsOnNonCreatedStatus(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 422, Body: []byte(`{"message":"name already exists"}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.CreateRepo(t.Context(), "tok", "grain-bot", "my-repo"); err == nil {
		t.Fatal("want an error on a non-201 status")
	}
}

// --- DeleteRepo ------------------------------------------------------

func TestDeleteRepoSucceedsOn204(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 204})
	c := testAppClient(t, transport, time.Now())

	if err := c.DeleteRepo(t.Context(), "tok", "grain-bot", "my-repo"); err != nil {
		t.Fatal(err)
	}
	call := transport.Calls[0]
	if call.Method != "DELETE" || call.Path != "/repos/grain-bot/my-repo" {
		t.Errorf("call = %s %s, want DELETE /repos/grain-bot/my-repo", call.Method, call.Path)
	}
}

func TestDeleteRepoTreatsAlreadyGoneAsSuccess(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 404, Body: []byte(`{"message":"Not Found"}`)})
	c := testAppClient(t, transport, time.Now())

	if err := c.DeleteRepo(t.Context(), "tok", "grain-bot", "my-repo"); err != nil {
		t.Errorf("DeleteRepo of an already-gone repo = %v, want nil (idempotent)", err)
	}
}

func TestDeleteRepoErrorsOnOtherNonSuccessStatus(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 403, Body: []byte(`{"message":"forbidden"}`)})
	c := testAppClient(t, transport, time.Now())

	if err := c.DeleteRepo(t.Context(), "tok", "grain-bot", "my-repo"); err == nil {
		t.Fatal("want an error on a non-204, non-404 status")
	}
}

func TestDeleteRepoEscapesOwnerAndName(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 204})
	c := testAppClient(t, transport, time.Now())

	if err := c.DeleteRepo(t.Context(), "tok", "owner", "repo name"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(transport.Calls[0].Path, "repo name") {
		t.Errorf("path = %q, want the repo name path-escaped", transport.Calls[0].Path)
	}
}

// --- ListRepos -------------------------------------------------------

func TestListReposParsesTheRepoList(t *testing.T) {
	body := `[{"name":"a","created_at":"2026-01-01T00:00:00Z"},{"name":"b","created_at":"2026-01-02T00:00:00Z"}]`
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(body)})
	c := testAppClient(t, transport, time.Now())

	repos, err := c.ListRepos(t.Context(), "tok", "grain-bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Name != "a" || repos[1].Name != "b" {
		t.Errorf("ListRepos() = %+v, want [a b]", repos)
	}
	call := transport.Calls[0]
	if call.Method != "GET" || call.Path != "/orgs/grain-bot/repos?type=private&per_page=100" {
		t.Errorf("call = %s %s, want GET /orgs/grain-bot/repos?type=private&per_page=100", call.Method, call.Path)
	}
}

func TestListReposErrorsOnNonOKStatus(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 500, Body: []byte(`{"message":"server error"}`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.ListRepos(t.Context(), "tok", "grain-bot"); err == nil {
		t.Fatal("want an error on a non-200 status")
	}
}

func TestListReposErrorsOnUnparsableBody(t *testing.T) {
	transport := github.NewFakeTransport(github.ApiResponse{Status: 200, Body: []byte(`not json`)})
	c := testAppClient(t, transport, time.Now())

	if _, err := c.ListRepos(t.Context(), "tok", "grain-bot"); err == nil {
		t.Fatal("want an error on a malformed response body")
	}
}
