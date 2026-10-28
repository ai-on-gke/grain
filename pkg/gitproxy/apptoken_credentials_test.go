package gitproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/github"
)

// writeAppCredential writes name.app.json under dir, the *.app.json
// counterpart writeCredentialSet's tokens map writes for *.token
// credentials (credentials_test.go).
func writeAppCredential(t *testing.T, dir, name string) {
	t.Helper()
	key := testAppKey(t)
	data, err := json.Marshal(map[string]string{
		"app_id":          "12345",
		"installation_id": "67890",
		"private_key":     string(pkcs1PEM(key)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".app.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mintedTokenResponse(token, expiresAt string) github.ApiResponse {
	body, _ := json.Marshal(map[string]string{"token": token, "expires_at": expiresAt})
	return github.ApiResponse{Status: 201, Body: body}
}

func TestSelectResolvesAnAppBackedCredential(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"acme/widgets": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(mintedTokenResponse("ghs_minted", "2026-08-30T13:00:00Z"))

	cred, ok := useTestPatterns(set).Select("acme", "widgets")
	if !ok || cred.Name != "ci-app" || cred.Token == nil || *cred.Token != "ghs_minted" {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

func TestGetRecognizesAnAppJSONCredentialWithNoTokenFile(t *testing.T) {
	dir := writeCredentialSet(t, nil, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(mintedTokenResponse("ghs_minted", "2026-08-30T13:00:00Z"))

	cred, ok := set.Get("ci-app")
	if !ok || cred.Token == nil || *cred.Token != "ghs_minted" {
		t.Fatalf("got %+v, %v", cred, ok)
	}
}

func TestLoadCachesAnAppTokenUntilItNearsExpiry(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	transport := github.NewFakeTransport(
		mintedTokenResponse("ghs_first", "2026-08-30T13:00:00Z"),
		mintedTokenResponse("ghs_second", "2026-08-30T14:00:00Z"),
	)
	set.AppTransport = transport

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	set.Now = func() time.Time { return now }

	cred, _ := useTestPatterns(set).Select("acme", "widgets")
	if *cred.Token != "ghs_first" {
		t.Fatalf("first call: got token %q", *cred.Token)
	}

	// Well before expiry (minus the refresh skew): still cached, no
	// second mint.
	now = now.Add(30 * time.Minute)
	cred, _ = useTestPatterns(set).Select("acme", "widgets")
	if *cred.Token != "ghs_first" {
		t.Fatalf("within lifetime: got token %q, want the cached one", *cred.Token)
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("expected exactly one mint call so far, got %d", len(transport.Calls))
	}

	// Inside appTokenRefreshSkew of the first token's own expiry: load
	// re-mints rather than serving a token GitHub is about to invalidate.
	now = now.Add(29 * time.Minute)
	cred, _ = useTestPatterns(set).Select("acme", "widgets")
	if *cred.Token != "ghs_second" {
		t.Fatalf("near expiry: got token %q, want a freshly minted one", *cred.Token)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("expected a second mint call once the first token neared expiry, got %d", len(transport.Calls))
	}
}

func TestLoadFallsBackToAnonymousWhenMintingFailsAndRetriesAfterADelay(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	transport := github.NewFakeTransport(
		github.ApiResponse{Status: 403, Body: []byte(`{"message":"bad key"}`)},
		mintedTokenResponse("ghs_recovered", "2026-08-30T14:00:00Z"),
	)
	set.AppTransport = transport

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	set.Now = func() time.Time { return now }

	cred, ok := useTestPatterns(set).Select("acme", "widgets")
	if !ok || cred.Token != nil {
		t.Fatalf("expected a failed mint to serve as anonymous, got %+v, %v", cred, ok)
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("expected one mint attempt, got %d", len(transport.Calls))
	}

	// Immediately retrying does not hammer GitHub's token endpoint again.
	cred, _ = useTestPatterns(set).Select("acme", "widgets")
	if cred.Token != nil {
		t.Fatal("expected the cached failure to still read as anonymous")
	}
	if len(transport.Calls) != 1 {
		t.Fatalf("expected no retry before appMintFailureRetryDelay elapsed, got %d calls", len(transport.Calls))
	}

	now = now.Add(appMintFailureRetryDelay + time.Second)
	cred, ok = useTestPatterns(set).Select("acme", "widgets")
	if !ok || cred.Token == nil || *cred.Token != "ghs_recovered" {
		t.Fatalf("expected a retry to succeed once the key was fixed, got %+v, %v", cred, ok)
	}
	if len(transport.Calls) != 2 {
		t.Fatalf("expected a second mint attempt after the retry delay, got %d", len(transport.Calls))
	}
}

func TestAFailedMintIsCarriedOnTheCredentialWithGitHubsOwnReason(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(github.ApiResponse{
		Status: 403, Body: []byte(`{"message":"This installation has been suspended","documentation_url":"https://docs.github.com/rest"}`),
	})
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	set.Now = func() time.Time { return now }

	cred, _ := useTestPatterns(set).Select("acme", "widgets")
	if cred.Token != nil {
		t.Fatalf("expected the failed mint to still serve anonymously, got %+v", cred)
	}
	if cred.MintFailure == nil {
		t.Fatal("expected the failed mint to be carried on the credential, so something other than " +
			"the daemon's log can report it")
	}
	if cred.MintFailure.Status != 403 {
		t.Errorf("status: got %d, want GitHub's own 403", cred.MintFailure.Status)
	}
	if cred.MintFailure.Message != "This installation has been suspended" {
		t.Errorf("message: got %q, want GitHub's own sentence", cred.MintFailure.Message)
	}
	if !cred.MintFailure.Time.Equal(now) {
		t.Errorf("time: got %v, want the set's own clock %v", cred.MintFailure.Time, now)
	}
	if want := `GitHub answered 403 to the token exchange: "This installation has been suspended"`; cred.MintFailure.Describe() != want {
		t.Errorf("Describe: got %q, want %q", cred.MintFailure.Describe(), want)
	}
}

func TestASuccessfulMintCarriesNoFailure(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(mintedTokenResponse("ghs_minted", "2026-08-30T13:00:00Z"))
	set.Now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }

	cred, _ := useTestPatterns(set).Select("acme", "widgets")
	if cred.MintFailure != nil {
		t.Fatalf("a minted credential reports a failure: %+v", cred.MintFailure)
	}
	if _, failed := set.AppMintFailure("ci-app"); failed {
		t.Fatal("AppMintFailure reports a failure for a credential that minted fine")
	}
}

func TestAppMintFailureAnswersForACredentialNothingHasAskedForYet(t *testing.T) {
	dir := writeCredentialSet(t, nil, map[string]string{"bot": "ghp_static"})
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	set.AppTransport = github.NewFakeTransport(github.ApiResponse{
		Status: 404, Body: []byte(`{"message":"Integration not found"}`),
	})

	// Nothing has resolved this credential in this process, which is the
	// state a settings pane opens in: the answer has to be minted, not
	// remembered.
	failure, failed := set.AppMintFailure("ci-app")
	if !failed {
		t.Fatal("expected AppMintFailure to mint and report the refusal")
	}
	if failure.Status != 404 || failure.Message != "Integration not found" {
		t.Errorf("got %+v, want GitHub's own 404 and message", failure)
	}

	// A *.token credential has nothing to mint, and asking about one
	// must not turn into a token exchange.
	if _, failed := set.AppMintFailure("bot"); failed {
		t.Error("a *.token credential reports a mint failure")
	}
}

func TestAMintFailureWithNoUsableMessageStillDescribesItself(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "ci-app"}, nil)
	writeAppCredential(t, dir, "ci-app")
	set, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	// An HTML error page from something in front of GitHub: not a
	// sentence, and not repeated into a git "remote:" line or a pane.
	set.AppTransport = github.NewFakeTransport(github.ApiResponse{
		Status: 502, Body: []byte("<html><body>Bad gateway</body></html>"),
	})

	cred, _ := useTestPatterns(set).Select("acme", "widgets")
	if cred.MintFailure == nil {
		t.Fatal("expected a failure to be recorded")
	}
	if strings.Contains(cred.MintFailure.Describe(), "<html>") {
		t.Errorf("HTML was repeated verbatim: %q", cred.MintFailure.Describe())
	}
	if !strings.Contains(cred.MintFailure.Describe(), "502") {
		t.Errorf("Describe drops the status it does know: %q", cred.MintFailure.Describe())
	}
}
