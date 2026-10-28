package gitproxy

// What a sandbox is told when GitHub answers 404 to a request that was
// authorized and forwarded. GitHub's own answer becomes "remote:
// Repository not found", which reads as a fact about the repository and
// is at least as often a fact about the credential the request went out
// under -- the failure an operator reports as "grain says my state repo
// does not exist".

import (
	"context"
	"strings"
	"testing"
)

func TestAnUpstream404NamesTheCredentialItWasRefusedUnder(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	forwarder.Response = UpstreamResponse{
		Status:  404,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{"message":"Not Found"}`),
	}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 404 {
		t.Fatalf("status = %d, want GitHub's own 404 kept", resp.Status)
	}
	body := string(resp.Body)
	for _, want := range []string{"owner/repo", `"bot"`, "Git connections"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want it to mention %s", body, want)
		}
	}
	// git only prints a body it is told is text (Denial's own doc
	// comment), so GitHub's JSON content type must not survive.
	if ct := resp.Headers["Content-Type"]; !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	// Still one forwarded request, audited as what it was: this
	// substitutes the explanation, not the outcome.
	if len(forwarder.Calls) != 1 {
		t.Errorf("forwarded %d times, want 1", len(forwarder.Calls))
	}
	if audit.Entries[0].Outcome != "forwarded: 404" {
		t.Errorf("outcome = %q, want the forwarded status recorded as it was", audit.Entries[0].Outcome)
	}
}

// The ladder's other credential shape: a repo that resolves to
// "anonymous" is one no token was sent for at all, and saying "the
// \"anonymous\" credential cannot see it" would send an operator looking
// for a token that does not exist.
func TestAnUpstream404UnderNoCredentialSaysSo(t *testing.T) {
	dir := writeCredentialSet(t, map[string]string{"*": "anonymous"}, nil)
	credentials, err := LoadCredentialSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, forwarder, _ := newTestProxy(t)
	p.Credentials = credentials
	forwarder.Response = UpstreamResponse{Status: 404, Body: []byte("Not Found")}
	headers := merge(gitHeaders, map[string]string{"Authorization": basicAuthHeader("tok0")})
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/info/refs",
		"service=git-upload-pack", headers, nil)
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "no credential at all") {
		t.Errorf("body = %q, want it to say the request went out unauthenticated", resp.Body)
	}
}

// A 404 the proxy answers on its own -- an unmatched path -- is not a
// repository the credential could not see, and must not grow a paragraph
// about credentials.json.
func TestTheProxysOwnNotFoundIsUntouched(t *testing.T) {
	p, _, _ := newTestProxy(t)
	resp := p.Handle(context.Background(), "GET", "/owner/repo.git/HEAD", "", gitHeaders, nil)
	if resp.Status != 404 || string(resp.Body) != "not found" {
		t.Errorf("resp = %+v, want the bare not-found this path always answered", resp)
	}
}
