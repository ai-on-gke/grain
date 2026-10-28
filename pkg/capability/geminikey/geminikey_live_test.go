package geminikey

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestLiveMintAndRevoke exercises the real API Keys API, the same way
// v1's tests/test_gcp_live.py gates a live-credential test on an
// environment variable and skips otherwise:
//
//	GEMINI_KEY_LIVE_PROJECT=my-project \
//	GEMINI_KEY_LIVE_CREDENTIALS_FILE=/path/to/minter-key.json \
//	go test ./capability/geminikey/... -run TestLiveMintAndRevoke -v
//
// The credential must belong to an identity that actually holds
// apikeys.keys.{create,get,delete,list} on the project -- unlike a
// gcp-key capability's own service-account keys, an API key carries no
// IAM binding of its own to grant that permission narrowly, so whichever
// account this names has it project-wide. That is a real difference from
// grain/automation/gemini_keys.py's minter, which impersonates a
// separate agent account per call; nothing here does that (see
// Capability's own doc comment on Credential).
func TestLiveMintAndRevoke(t *testing.T) {
	projectID := os.Getenv("GEMINI_KEY_LIVE_PROJECT")
	credentialsFile := os.Getenv("GEMINI_KEY_LIVE_CREDENTIALS_FILE")
	if projectID == "" || credentialsFile == "" {
		t.Skip("GEMINI_KEY_LIVE_PROJECT and GEMINI_KEY_LIVE_CREDENTIALS_FILE not set; skipping live API Keys integration test")
	}
	material, err := os.ReadFile(credentialsFile)
	if err != nil {
		t.Fatalf("reading %s: %v", credentialsFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const credentialName = "live-test-credential"
	c := New(projectID, model.CredentialRef{Name: credentialName})
	cc := model.CapabilityContext{
		Task:        model.Task{ID: "live-test-task"},
		Run:         model.Run{ID: "live-" + time.Now().UTC().Format("20060102-150405"), TaskID: "live-test-task"},
		Now:         time.Now().UTC(),
		Credentials: fakeResolver{credentialName: string(material)},
	}

	res, err := c.Resolve(ctx, cc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused {
		t.Fatalf("Resolve refused: %s", res.Reason)
	}

	m, err := c.Materialize(ctx, cc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if m.Lease == nil || m.Lease.Resource == "" {
		t.Fatalf("expected a Lease naming the minted key, got %+v", m.Lease)
	}
	if len(m.Placements) != 1 || m.Placements[0].Content == "" {
		t.Fatalf("expected one placement carrying a real key string, got %+v", m.Placements)
	}
	if !strings.HasPrefix(m.Placements[0].Content, "AIza") {
		t.Errorf("minted key string %q does not look like a Google API key", m.Placements[0].Content)
	}
	t.Logf("minted %s", m.Lease.Resource)

	// However the assertions above turn out, the key must not outlive
	// this test.
	defer func() {
		if err := c.Revoke(ctx, cc, *m.Lease); err != nil {
			t.Errorf("Revoke: %v", err)
		}
	}()
}

// TestLiveDeleteExpiredReapsOnlyOld mints one grain-prefixed key and
// confirms DeleteExpired, called with a cutoff older than the key, does
// NOT reap it -- the same "never call with a short TTL against a live
// project" caution tests/test_gcp_live.py's own docstring gives, since a
// real deployment could have other tasks' keys live in the same project.
func TestLiveDeleteExpiredReapsOnlyOld(t *testing.T) {
	projectID := os.Getenv("GEMINI_KEY_LIVE_PROJECT")
	credentialsFile := os.Getenv("GEMINI_KEY_LIVE_CREDENTIALS_FILE")
	if projectID == "" || credentialsFile == "" {
		t.Skip("GEMINI_KEY_LIVE_PROJECT and GEMINI_KEY_LIVE_CREDENTIALS_FILE not set; skipping live API Keys integration test")
	}
	material, err := os.ReadFile(credentialsFile)
	if err != nil {
		t.Fatalf("reading %s: %v", credentialsFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const credentialName = "live-test-credential"
	c := New(projectID, model.CredentialRef{Name: credentialName})
	cc := model.CapabilityContext{
		Task:        model.Task{ID: "live-test-task"},
		Run:         model.Run{ID: "live-reap-" + time.Now().UTC().Format("20060102-150405"), TaskID: "live-test-task"},
		Now:         time.Now().UTC(),
		Credentials: fakeResolver{credentialName: string(material)},
	}

	m, err := c.Materialize(ctx, cc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	defer func() {
		if err := c.Revoke(ctx, cc, *m.Lease); err != nil {
			t.Errorf("cleanup Revoke: %v", err)
		}
	}()

	deleted, err := DeleteExpired(ctx, fakeResolver{credentialName: string(material)}, credentialName, projectID, time.Now().UTC(), 24*time.Hour)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	for _, name := range deleted {
		if name == m.Lease.Resource {
			t.Fatalf("DeleteExpired reaped a key minted seconds ago against a 24h cutoff")
		}
	}
}
