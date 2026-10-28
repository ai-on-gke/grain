package gitproxy

import (
	"strings"
	"testing"
)

// tokenForwarder answers 200 to the one token GitHub currently accepts
// and 401 to any other, and records every token it was sent.
type tokenForwarder struct {
	accept string
	sent   []string
}

func (f *tokenForwarder) Forward(method, path, query string, headers map[string]string, body []byte, token *string) (UpstreamResponse, error) {
	sent := ""
	if token != nil {
		sent = *token
	}
	f.sent = append(f.sent, sent)
	if sent != f.accept {
		return UpstreamResponse{Status: 401, Headers: upstreamHeaders, Body: []byte("Invalid username or token.\n")}, nil
	}
	return UpstreamResponse{Status: 200, Headers: upstreamHeaders, Body: []byte("ok")}, nil
}

func ambientProxy(t *testing.T) (*GitProxy, *tokenForwarder, *RecordingAuditLog, string) {
	t.Helper()
	state := fakeHostTools(t)
	c, _ := ambientSet(t)
	if err := c.SetAmbient("op", "", "gh"); err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadSandboxTokens(writeTokensFile(t, t.TempDir(), map[string]string{"sandbox-0": "tok0"}))
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &tokenForwarder{}
	audit := &RecordingAuditLog{}
	p := &GitProxy{
		Audit:               audit,
		Authorizer:          stubAuthorizer{allow: map[[3]string]bool{{"sandbox-0", "owner", "repo"}: true}},
		Credentials:         c,
		TeamCredentials:     ladderConnections{creds: c},
		CredentialOverrides: fixedOverride("op"),
		Tokens:              tokens,
		Forwarder:           forwarder,
	}
	return p, forwarder, audit, state
}

// The walkthrough in docs/test-runs/2026-10-15-ambient-git-credentials.md:
// the host's gh login rotated inside the 5-minute cache window, and the
// first clone after it was refused with "that login is no longer valid"
// although gh already printed a good token. The proxy now asks gh again
// on that 401 and sends the request once more with the new token.
func TestAmbient401AfterRotationIsRetriedWithTheFreshToken(t *testing.T) {
	p, forwarder, audit, state := ambientProxy(t)
	writeState(t, state, "gh-token", "gho_old")
	forwarder.accept = "gho_old"
	if resp := fetchWith(t, p); resp.Status != 200 {
		t.Fatalf("first fetch = %d %s", resp.Status, resp.Body)
	}

	writeState(t, state, "gh-token", "gho_new")
	forwarder.accept = "gho_new"
	ghBefore := calls(t, state, "gh-calls")
	resp := fetchWith(t, p)
	if resp.Status != 200 {
		t.Fatalf("fetch after rotation = %d %s, want it retried with the new token", resp.Status, resp.Body)
	}
	if got := strings.Join(forwarder.sent, ","); got != "gho_old,gho_old,gho_new" {
		t.Errorf("tokens sent = %s, want the stale one once and then the fresh one", got)
	}
	if got := calls(t, state, "gh-calls") - ghBefore; got != 1 {
		t.Errorf("gh ran %d times for the retry, want 1", got)
	}
	var outcomes []string
	for _, e := range audit.Entries {
		outcomes = append(outcomes, e.Outcome)
	}
	joined := strings.Join(outcomes, "\n")
	if !strings.Contains(joined, "forwarded: 401 (ambient: this host's gh login); asked the host's login again and retried") {
		t.Errorf("audit trail does not record the refused first attempt:\n%s", joined)
	}
	if strings.Contains(joined, "gho_") {
		t.Errorf("audit trail carries a token:\n%s", joined)
	}
}

// A login that really is gone keeps its refusal: gh hands back the same
// token, so there is nothing to retry with and the sandbox is told what
// to run.
func TestAmbient401WithNoNewTokenIsNotRetried(t *testing.T) {
	p, forwarder, _, state := ambientProxy(t)
	writeState(t, state, "gh-token", "gho_revoked")
	forwarder.accept = "gho_other"
	resp := fetchWith(t, p)
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "gh auth login") {
		t.Fatalf("fetch with a revoked login = %d %s, want the refusal naming gh auth login", resp.Status, resp.Body)
	}
	if got := strings.Join(forwarder.sent, ","); got != "gho_revoked" {
		t.Errorf("tokens sent = %s, want the original request once and nothing more", got)
	}
}
