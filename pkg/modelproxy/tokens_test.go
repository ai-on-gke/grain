package modelproxy

import (
	"strings"
	"testing"
	"time"
)

func TestMintedTokenAuthenticatesAsItsOwner(t *testing.T) {
	tokens := NewTokens()
	id, token, err := tokens.Mint("run-1", testConnection)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if id == "" || token == "" {
		t.Fatalf("Mint returned id %q token %q, want both", id, token)
	}
	// The id is the half a Lease records, so it has to be recoverable
	// from the token and never be the whole of it -- see tokens.go's own
	// note on why a lease must not carry the material.
	if !strings.HasPrefix(token, id+".") {
		t.Fatalf("token %q does not start with its own id %q", token, id)
	}
	if token == id {
		t.Fatal("the token is its own id: a lease recording the id would publish a live credential")
	}
	owner, ok := tokens.Authenticate(token)
	if !ok || owner != "run-1" {
		t.Fatalf("Authenticate(token) = %q, %v, want \"run-1\", true", owner, ok)
	}
}

func TestNothingElseAuthenticates(t *testing.T) {
	tokens := NewTokens()
	id, token, err := tokens.Mint("run-1", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"the id alone", id},
		{"the secret alone", strings.TrimPrefix(token, id+".")},
		{"a token with no separator", strings.ReplaceAll(token, ".", "")},
		{"the right id, the wrong secret", id + ".0000000000"},
		{"an id nobody minted", "deadbeef.0000000000"},
	} {
		if owner, ok := tokens.Authenticate(tc.token); ok {
			t.Errorf("%s: Authenticate(%q) = %q, true -- want refused", tc.name, tc.token, owner)
		}
	}
}

func TestARevokedTokenAuthenticatesNobody(t *testing.T) {
	tokens := NewTokens()
	id, token, err := tokens.Mint("run-1", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	tokens.Revoke(id)
	if _, ok := tokens.Authenticate(token); ok {
		t.Fatal("a revoked token still authenticates: a token copied out of a finished run's sandbox would go on working")
	}
	// Idempotent, which is what a retried cleanup does.
	tokens.Revoke(id)
	tokens.Revoke("never-minted")
	if n := tokens.Len(); n != 0 {
		t.Fatalf("Len after revoking the only token = %d, want 0", n)
	}
}

func TestATokenStopsWorkingAtItsLease(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tokens := NewTokens()
	tokens.Lease = time.Hour
	tokens.Now = func() time.Time { return now }
	_, token, err := tokens.Mint("run-1", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Minute)
	if _, ok := tokens.Authenticate(token); !ok {
		t.Fatal("a token inside its lease was refused")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := tokens.Authenticate(token); ok {
		t.Fatal("a token past its lease still authenticates: nothing else here expires one")
	}
}

// A token minted for a run that states its own end is dead once that run
// could no longer be alive, however much of the flat lease is left --
// the case a Revoke that never came leaves behind (bwsalmon/grain#2418).
func TestATokenMintedUntilADeadlineStopsWorkingThere(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tokens := NewTokens()
	tokens.Now = func() time.Time { return now }
	until := now.Add(3 * time.Hour)
	_, token, err := tokens.MintUntil("run-1", testConnection, until)
	if err != nil {
		t.Fatal(err)
	}
	now = until
	if owner, ok := tokens.Authenticate(token); !ok || owner != "run-1" {
		t.Fatalf("Authenticate at the deadline = %q, %v, want run-1, true", owner, ok)
	}
	now = until.Add(time.Second)
	if _, ok := tokens.Authenticate(token); ok {
		t.Fatalf("a token past its run's deadline still authenticates, with %v of DefaultLease left",
			DefaultLease-3*time.Hour)
	}

	// A deadline further off than the lease leaves the lease in charge,
	// and a zero one is plain Mint.
	now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tokens.Lease = time.Hour
	for _, until := range []time.Time{now.Add(48 * time.Hour), {}} {
		now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
		_, token, err := tokens.MintUntil("run-2", testConnection, until)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(61 * time.Minute)
		if _, ok := tokens.Authenticate(token); ok {
			t.Errorf("MintUntil(%v): a token past its lease still authenticates", until)
		}
	}
}

// The registry is the only place a minted token exists, so the one thing
// that must not grow without bound is the map: a deployment whose runs
// all end without a revoke would otherwise keep every token it ever
// minted for the life of the process.
func TestMintingSweepsExpiredTokens(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tokens := NewTokens()
	tokens.Lease = time.Hour
	tokens.Now = func() time.Time { return now }
	for range 5 {
		if _, _, err := tokens.Mint("run-old", testConnection); err != nil {
			t.Fatal(err)
		}
	}
	if n := tokens.Len(); n != 5 {
		t.Fatalf("Len = %d, want 5", n)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := tokens.Mint("run-new", testConnection); err != nil {
		t.Fatal(err)
	}
	if n := tokens.Len(); n != 1 {
		t.Fatalf("Len after minting past five expired leases = %d, want 1 (the new one)", n)
	}
}

// A nil registry is what a handler wired up without one holds, and a nil
// endpoint is what a capability provider built with no proxy holds. Both
// have to read as "there is no proxy" rather than panic on the first
// request or the first dispatch.
func TestNilRegistryAndEndpointFailClosed(t *testing.T) {
	var tokens *Tokens
	if _, _, err := tokens.Mint("run-1", testConnection); err == nil {
		t.Error("a nil registry minted a token")
	}
	if _, ok := tokens.Authenticate("anything"); ok {
		t.Error("a nil registry authenticated somebody")
	}
	tokens.Revoke("anything")
	if tokens.Len() != 0 {
		t.Error("a nil registry holds tokens")
	}

	var endpoint *Endpoint
	if url := endpoint.URL(); url != "" {
		t.Errorf("a nil endpoint has URL %q", url)
	}
	endpoint.SetURL("http://example.invalid")
	if _, _, err := endpoint.Mint("run-1", testConnection); err == nil {
		t.Error("a nil endpoint minted a token")
	}
	if _, _, ok := endpoint.authenticate("anything"); ok {
		t.Error("a nil endpoint authenticated somebody")
	}
	endpoint.Revoke("anything")
	if credential, err := endpoint.ResolveCredential(t.Context(), testConnection); err != nil || credential != "" {
		t.Errorf("a nil endpoint resolved credential %q, %v", credential, err)
	}
}

func TestAnEndpointPublishesItsAddressAndItsCredential(t *testing.T) {
	endpoint := NewEndpoint()
	if endpoint.URL() != "" {
		t.Fatalf("a fresh endpoint already has URL %q, want none until a server publishes one", endpoint.URL())
	}
	endpoint.SetURL("http://172.17.0.1:41234")
	if got := endpoint.URL(); got != "http://172.17.0.1:41234" {
		t.Fatalf("URL = %q", got)
	}
	// No Credential func is a deployment holding no Claude credential,
	// which is an answer rather than an error: the handler refuses with
	// a sentence and the capability refuses to resolve.
	if credential, err := endpoint.ResolveCredential(t.Context(), testConnection); err != nil || credential != "" {
		t.Fatalf("ResolveCredential with no func = %q, %v, want \"\", nil", credential, err)
	}
	_, token, err := endpoint.Mint("run-1", testConnection)
	if err != nil {
		t.Fatal(err)
	}
	if owner, connection, ok := endpoint.authenticate(token); !ok || owner != "run-1" || connection != testConnection {
		t.Fatalf("authenticate = %q, %v", owner, ok)
	}
}
