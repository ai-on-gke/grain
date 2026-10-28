package attest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPAEMatchesTheDSSESpecExample(t *testing.T) {
	got := string(PAE("http://example.com/HelloWorld", []byte("hello world")))
	if want := "DSSEv1 29 http://example.com/HelloWorld 11 hello world"; got != want {
		t.Fatalf("PAE = %q, want %q", got, want)
	}
}

func testSigner(t *testing.T) *FileSigner {
	t.Helper()
	s, err := LoadOrCreateFileSigner(filepath.Join(t.TempDir(), "attest.key"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	tree  = Tree{SHA256: strings.Repeat("a", 64), GitTree: strings.Repeat("b", 40)}
	other = Tree{SHA256: strings.Repeat("c", 64), GitTree: strings.Repeat("d", 40)}
)

func statement() Statement {
	return NewStatement(tree, "0123456789abcdef0123456789abcdef01234567", RunPredicate{
		Grit:     GritInfo{Version: "grit/v0.1.0"},
		Client:   ClientInfo{ID: "grain-test", Labels: map[string]string{"taskId": "1330"}},
		Request:  RequestInfo{RequestID: "grain:1", Run: "r-1", Repo: "bwsalmon/grain", BaseTree: &other},
		Jobs:     []Job{{ID: "tests.yml/go", Result: Result{Conclusion: "success", Attempts: 1}, Secrets: []SecretRef{{Name: "gemini-key", Version: "cfg-12"}}}},
		IssuedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
	})
}

func TestAnAttestationVerifiesForItsTreeAndFailsForAnyOther(t *testing.T) {
	signer := testSigner(t)
	env, err := Sign(context.Background(), signer, statement())
	if err != nil {
		t.Fatal(err)
	}
	// Through JSON, as it crosses the wire and is stored.
	b, _ := json.Marshal(env)
	var back Envelope
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	keys := KeySet{signer.KeyID(): {Public: signer.Public(), Status: KeyActive}}
	s, err := Verify(&back, keys)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := s.CheckSubject(tree, "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Errorf("CheckSubject(its own tree) = %v", err)
	}
	if err := s.CheckSubject(other, "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Error("CheckSubject(another tree) = nil, want a refusal")
	}
	if err := s.CheckSubject(Tree{SHA256: tree.SHA256, GitTree: other.GitTree}, "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Error("CheckSubject(same archive digest, other git tree) = nil, want a refusal")
	}
	if err := s.CheckSubject(tree, "ffff"); err == nil {
		t.Error("CheckSubject(another commit) = nil, want a refusal")
	}
	if got := s.Predicate.Jobs[0].Secrets[0]; got.Name != "gemini-key" || got.Version != "cfg-12" {
		t.Errorf("secret ref = %+v", got)
	}
}

func TestVerifyRefusesTamperingAndUntrustedKeys(t *testing.T) {
	signer := testSigner(t)
	env, err := Sign(context.Background(), signer, statement())
	if err != nil {
		t.Fatal(err)
	}
	keys := KeySet{signer.KeyID(): {Public: signer.Public(), Status: KeyActive}}

	tampered := *env
	tampered.Payload = []byte(strings.Replace(string(env.Payload), `"success"`, `"failure"`, 1))
	if _, err := Verify(&tampered, keys); err == nil {
		t.Error("Verify(tampered payload) = nil")
	}

	stranger := testSigner(t)
	if _, err := Verify(env, KeySet{stranger.KeyID(): {Public: stranger.Public(), Status: KeyActive}}); err == nil {
		t.Error("Verify(untrusted key) = nil")
	}
	// A signature under a trusted kid but by another key.
	if _, err := Verify(env, KeySet{signer.KeyID(): {Public: stranger.Public(), Status: KeyActive}}); err == nil {
		t.Error("Verify(wrong key under the kid) = nil")
	}

	wrongType := *env
	wrongType.PayloadType = "text/plain"
	if _, err := Verify(&wrongType, keys); err == nil {
		t.Error("Verify(wrong payload type) = nil")
	}
	if _, err := Verify(&Envelope{PayloadType: PayloadType, Payload: env.Payload}, keys); err == nil {
		t.Error("Verify(no signatures) = nil")
	}
}

func TestARetiredKeyStillVerifiesAndARevokedOneOnlyWhatCameBefore(t *testing.T) {
	signer := testSigner(t)
	env, err := Sign(context.Background(), signer, statement())
	if err != nil {
		t.Fatal(err)
	}
	issued := statement().Predicate.IssuedAt
	for _, c := range []struct {
		key  Key
		want bool
	}{
		{Key{Public: signer.Public(), Status: KeyRetired}, true},
		{Key{Public: signer.Public(), Status: KeyRevoked, RevokedAt: issued.Add(time.Hour)}, true},
		{Key{Public: signer.Public(), Status: KeyRevoked, RevokedAt: issued}, false},
		{Key{Public: signer.Public(), Status: KeyRevoked, RevokedAt: issued.Add(-time.Hour)}, false},
	} {
		_, err := Verify(env, KeySet{signer.KeyID(): c.key})
		if (err == nil) != c.want {
			t.Errorf("%s revoked at %v: Verify err = %v, want ok=%v", c.key.Status, c.key.RevokedAt, err, c.want)
		}
	}
}

func TestTheFileKeyIsReusedAndItsPublicHalfRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attest.key")
	a, err := LoadOrCreateFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, %v; want 0600", fi.Mode(), err)
	}
	b, err := LoadOrCreateFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID() != b.KeyID() {
		t.Errorf("reloaded key id %s, want %s", b.KeyID(), a.KeyID())
	}
	pemBytes, err := MarshalPublicKey(a.Public())
	if err != nil {
		t.Fatal(err)
	}
	kid, pub, err := ParsePublicKey(pemBytes)
	if err != nil || kid != a.KeyID() || !pub.Equal(a.Public()) {
		t.Errorf("ParsePublicKey = %s, %v, %v", kid, pub, err)
	}
}

func TestACachedJobNamesTheOriginalEnvelope(t *testing.T) {
	signer := testSigner(t)
	orig, err := Sign(context.Background(), signer, statement())
	if err != nil {
		t.Fatal(err)
	}
	d, err := Digest(orig)
	if err != nil || !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Fatalf("Digest = %q, %v", d, err)
	}
	s := statement()
	s.Subject[0].Digest["sha256"] = other.SHA256
	s.Predicate.Jobs[0] = Job{ID: "tests.yml/go", Fingerprint: "fp", Result: Result{Conclusion: "success"}, Cached: &CachedRef{Attestation: d, Run: "r-1", Fingerprint: "fp"}}
	env, err := Sign(context.Background(), signer, s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(env, KeySet{signer.KeyID(): {Public: signer.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Predicate.Jobs[0].Cached; c == nil || c.Attestation != d || len(got.Predicate.Jobs[0].Steps) != 0 {
		t.Errorf("cached job = %+v", got.Predicate.Jobs[0])
	}
}

func TestNewKeySetTakesOnlyP256Keys(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if set, err := NewKeySet(map[string]crypto.PublicKey{"k": &ec.PublicKey}); err != nil || set["k"].Status != KeyActive {
		t.Errorf("NewKeySet(P-256) = %v, %v", set, err)
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := NewKeySet(map[string]crypto.PublicKey{"k": &p384.PublicKey}); err == nil {
		t.Error("NewKeySet(P-384) = nil error")
	}
}
