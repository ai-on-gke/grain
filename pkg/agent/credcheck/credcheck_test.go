package credcheck_test

// What a credential check has to get right is narrow and entirely
// observable from outside: the right call, presented with the right
// header, and a sentence naming the secret to replace when the far end
// says no. Every test here drives a real http.Client against an
// httptest.Server standing in for the vendor, so the request the vendor
// would receive is the request being asserted on.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// vendor stands in for one of the three APIs: it records the request it
// was given and answers with what the test told it to.
type vendor struct {
	status int
	body   string

	path   string
	query  string
	header http.Header
}

func (v *vendor) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.path = r.URL.Path
		v.query = r.URL.RawQuery
		v.header = r.Header.Clone()
		status := v.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(v.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGeminiKeyIsCheckedAgainstTheModelListing(t *testing.T) {
	v := &vendor{body: `{"models":[{"name":"models/gemini-3-pro"},{"name":"models/gemini-3-flash"}]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{GeminiBaseURL: srv.URL}.
		Check(context.Background(), "antigravity", "AIza-live")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if v.path != "/v1beta/models" {
		t.Fatalf("path = %q, want /v1beta/models", v.path)
	}
	// The key travels as this API's own `key=` parameter -- the only way
	// it takes one -- so a check that stopped sending it would still get
	// a 200 from a stub and prove nothing.
	if !strings.Contains(v.query, "key=AIza-live") {
		t.Fatalf("query = %q, want the key in it", v.query)
	}
	if res.Secret != secrets.GeminiAPIKeySecret {
		t.Fatalf("Secret = %q, want %q", res.Secret, secrets.GeminiAPIKeySecret)
	}
	// The evidence half: a count and real names, which is what
	// distinguishes this from the chip that already said "set".
	for _, want := range []string{"2 model(s)", "gemini-3-pro", secrets.GeminiAPIKeySecret} {
		if !strings.Contains(res.Detail, want) {
			t.Fatalf("Detail = %q, want %q in it", res.Detail, want)
		}
	}
}

// The legacy spelling reaches the same key agy really runs on -- the
// same fold every other caller applies, asserted here because a "gemini"
// that checked nothing would look like a working button.
func TestLegacyGeminiSpellingChecksTheAntigravityCredential(t *testing.T) {
	v := &vendor{body: `{"models":[{"name":"models/gemini-3-pro"}]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{GeminiBaseURL: srv.URL}.
		Check(context.Background(), "gemini", "AIza-live")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Framework != "antigravity" {
		t.Fatalf("Framework = %q, want antigravity", res.Framework)
	}
	if res.Secret != secrets.GeminiAPIKeySecret {
		t.Fatalf("Secret = %q, want %q", res.Secret, secrets.GeminiAPIKeySecret)
	}
}

// A Claude Code OAuth token is a different credential to that API than
// an API key is: bearer plus the oauth beta header, which is what the
// live API tells apart ("OAuth access token is invalid" against "API key
// is invalid").
func TestClaudeOAuthTokenIsPresentedAsABearer(t *testing.T) {
	v := &vendor{body: `{"data":[{"id":"claude-opus-4"}]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{AnthropicBaseURL: srv.URL}.
		Check(context.Background(), "claude", "sk-ant-oat01-live")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := v.header.Get("Authorization"); got != "Bearer sk-ant-oat01-live" {
		t.Fatalf("Authorization = %q, want the token as a bearer", got)
	}
	if got := v.header.Get("anthropic-beta"); got == "" {
		t.Fatalf("anthropic-beta header missing: an OAuth token is refused without it")
	}
	if got := v.header.Get("anthropic-version"); got == "" {
		t.Fatalf("anthropic-version header missing: this API requires it on every call")
	}
	if got := v.header.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want it unset for an OAuth token", got)
	}
	if !strings.Contains(res.Detail, "claude-opus-4") {
		t.Fatalf("Detail = %q, want the listed model in it", res.Detail)
	}
}

// An operator with a plain Anthropic API key can paste that into the
// same field and their runs work, so the check must not call that
// deployment broken.
func TestPlainAnthropicKeyIsPresentedAsAnAPIKey(t *testing.T) {
	v := &vendor{body: `{"data":[{"id":"claude-opus-4"}]}`}
	srv := v.start(t)

	if _, err := (credcheck.Checker{AnthropicBaseURL: srv.URL}).
		Check(context.Background(), "claude", "sk-ant-api03-live"); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := v.header.Get("x-api-key"); got != "sk-ant-api03-live" {
		t.Fatalf("x-api-key = %q, want the key", got)
	}
	if got := v.header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want it unset for an API key", got)
	}
}

func TestOpenAIKeyIsPresentedAsABearer(t *testing.T) {
	v := &vendor{body: `{"data":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{OpenAIBaseURL: srv.URL}.
		Check(context.Background(), "codex", "sk-openai-live")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if v.path != "/v1/models" {
		t.Fatalf("path = %q, want /v1/models", v.path)
	}
	if got := v.header.Get("Authorization"); got != "Bearer sk-openai-live" {
		t.Fatalf("Authorization = %q, want the key as a bearer", got)
	}
	if res.Secret != secrets.OpenAIAPIKeySecret {
		t.Fatalf("Secret = %q, want %q", res.Secret, secrets.OpenAIAPIKeySecret)
	}
}

// A refusal is an answer, and the whole of its value is the sentence:
// who said no, what they said, and which secret holds the value to
// replace.
func TestARefusedCredentialNamesTheVendorTheSecretAndTheReason(t *testing.T) {
	v := &vendor{
		status: http.StatusUnauthorized,
		body:   `{"type":"error","error":{"type":"authentication_error","message":"OAuth access token is invalid."}}`,
	}
	srv := v.start(t)

	res, err := credcheck.Checker{AnthropicBaseURL: srv.URL}.
		Check(context.Background(), "claude", "sk-ant-oat01-stale")
	if err == nil {
		t.Fatalf("Check succeeded against a 401, want an error: %+v", res)
	}
	// Reported alongside the refusal, not lost with it: a caller shows
	// the field to paste into whether or not the call worked.
	if res.Secret != secrets.ClaudeOAuthTokenSecret {
		t.Fatalf("Secret = %q, want it filled in even on a refusal", res.Secret)
	}
	for _, want := range []string{"Anthropic", secrets.ClaudeOAuthTokenSecret, "401", "OAuth access token is invalid"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q in it", err, want)
		}
	}
	// The credential itself is never quoted back at anybody.
	if strings.Contains(err.Error(), "sk-ant-oat01-stale") {
		t.Fatalf("error leaked the credential: %q", err)
	}
}

// A body that is not the vendor's own JSON -- a proxy's HTML error page,
// say -- still produces a usable sentence rather than a wall of markup.
func TestARefusalWithAnUnreadableBodyStillNamesTheStatus(t *testing.T) {
	v := &vendor{status: http.StatusForbidden, body: "<html>blocked by policy</html>"}
	srv := v.start(t)

	_, err := credcheck.Checker{OpenAIBaseURL: srv.URL}.
		Check(context.Background(), "codex", "sk-openai-stale")
	if err == nil {
		t.Fatal("Check succeeded against a 403, want an error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q, want the status in it", err)
	}
	if strings.Contains(err.Error(), "<html>") {
		t.Fatalf("error pasted the raw body into a sentence: %q", err)
	}
}

// Nothing to send is answered here rather than by the vendor: the remedy
// is the field beside the button.
func TestAnEmptyCredentialIsRefusedWithoutACall(t *testing.T) {
	v := &vendor{body: `{"data":[]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{OpenAIBaseURL: srv.URL}.
		Check(context.Background(), "codex", "   \n")
	if err == nil {
		t.Fatal("Check succeeded with no credential, want an error")
	}
	if v.path != "" {
		t.Fatalf("a request was made to %q, want none", v.path)
	}
	if res.Secret != secrets.OpenAIAPIKeySecret {
		t.Fatalf("Secret = %q, want it named so a caller can say what to fill in", res.Secret)
	}
}

// A framework grain cannot dispatch with at all is a mistake about
// grain, told apart from every answer above by SecretFor's own bool.
func TestAnUnknownFrameworkIsNotACredentialAnswer(t *testing.T) {
	if _, ok := credcheck.SecretFor("gpt"); ok {
		t.Fatal("SecretFor(gpt) = ok, want not ok")
	}
	if _, err := (credcheck.Checker{}).Check(context.Background(), "gpt", "x"); err == nil {
		t.Fatal("Check succeeded for an unknown framework, want an error")
	}
}

// A vendor that cannot be reached at all must not read as a bad
// credential: the remedy is the network, and rotating a live key over it
// is the mistake this sentence exists to prevent.
func TestAnUnreachableVendorIsNotReportedAsARefusal(t *testing.T) {
	srv := (&vendor{}).start(t)
	unreachable := srv.URL
	srv.Close()

	_, err := credcheck.Checker{GeminiBaseURL: unreachable}.
		Check(context.Background(), "antigravity", "AIza-live")
	if err == nil {
		t.Fatal("Check succeeded against a closed server, want an error")
	}
	if !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("error = %q, want it to read as a network failure", err)
	}
	if strings.Contains(err.Error(), "refused the credential") {
		t.Fatalf("error = %q, want it not to blame the credential", err)
	}
	// The Gemini key travels in the URL's query string, and a transport
	// error renders the URL it failed on: the key must not come with it.
	if strings.Contains(err.Error(), "AIza-live") {
		t.Fatalf("error = %q, want it not to carry the key", err)
	}
}

// An accepted credential that can see nothing is still accepted, and the
// sentence says both halves rather than reading like a failure.
func TestAnEmptyListingStillReportsTheCredentialAccepted(t *testing.T) {
	v := &vendor{body: `{"data":[]}`}
	srv := v.start(t)

	res, err := credcheck.Checker{OpenAIBaseURL: srv.URL}.
		Check(context.Background(), "codex", "sk-openai-live")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(res.Detail, "accepted") || !strings.Contains(res.Detail, "no models") {
		t.Fatalf("Detail = %q, want it to say accepted and empty", res.Detail)
	}
}
