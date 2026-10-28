package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
)

// PlaceFileOverSSH is what actually delivers a capability's credential
// into a sandbox this process cannot reach with os.WriteFile, so the
// three things worth proving are that the file arrives, that its content
// is exact, and that its mode is the placement's own -- not the login
// user's umask. localExecRunner (ssh_tools_test.go) runs the same
// coreutils a real runner would, against a temp directory.
func TestPlaceFileOverSSHWritesTheFileWithItsPlacementMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "home", "debian", ".gcp-service-account.json")
	const key = `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\n"}`

	if err := PlaceFileOverSSH(context.Background(), localExecRunner{dir: dir}, target, key, "600"); err != nil {
		t.Fatalf("PlaceFileOverSSH: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the placement never arrived: %v", err)
	}
	if string(got) != key {
		t.Errorf("placed content = %q, want %q", got, key)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("placed mode = %04o, want 0600 -- a credential must not land under the login user's umask", perm)
	}
}

// A placement's parent directory does not have to exist yet: gcpkey's own
// path lives in a home directory a fresh guest has, but nothing promises
// that for every capability, and the local path applyPlacements takes
// calls os.MkdirAll for exactly this reason.
func TestPlaceFileOverSSHCreatesMissingParentDirectories(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deep", "nested", "never", "created", "key.json")

	if err := PlaceFileOverSSH(context.Background(), localExecRunner{dir: dir}, target, "material", "640"); err != nil {
		t.Fatalf("PlaceFileOverSSH: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("the placement never arrived: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("placed mode = %04o, want 0640 -- Placement.Mode, not the default", perm)
	}
}

// Re-placing over a file left wider by something else must not inherit
// that width: `install -m` is what makes the mode a property of the
// placement rather than of whatever was there before.
func TestPlaceFileOverSSHNarrowsAFileLeftWiderThanThePlacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "key.json")
	if err := os.WriteFile(target, []byte("stale, world-readable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := PlaceFileOverSSH(context.Background(), localExecRunner{dir: dir}, target, "fresh", "600"); err != nil {
		t.Fatalf("PlaceFileOverSSH: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after re-placing = %04o, want 0600", perm)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh" {
		t.Errorf("content after re-placing = %q, want the new material with no remnant of the old", got)
	}
}

// A failure has to come back as an error naming the path, not be
// swallowed: applyPlacements' whole contract is that a half-materialized
// capability is never described to the agent as present.
func TestPlaceFileOverSSHReportsAFailedWrite(t *testing.T) {
	dir := t.TempDir()
	// A directory is not something `install` can overwrite with a file.
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	err := PlaceFileOverSSH(context.Background(), localExecRunner{dir: dir}, target, "material", "600")
	if err == nil {
		t.Fatal("PlaceFileOverSSH reported success for a placement that cannot have landed")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("error = %v, want it to name %s", err, target)
	}
}

func TestPlaceFileOverSSHRetriesASessionCutOffPartWay(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "key.json")
	runner := &flakyRunner{inner: localExecRunner{dir: dir}, failures: 1}
	if err := PlaceFileOverSSH(context.Background(), runner, target, "material", "600"); err != nil {
		t.Fatalf("one cut-off session failed the placement: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "material" {
		t.Errorf("%s = %q, want the placed content", target, got)
	}
}
