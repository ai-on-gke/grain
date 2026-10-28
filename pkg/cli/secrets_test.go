package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/secrets"
)

// An operating key already in place must survive a mint-gemini-key run
// untouched: config-sync re-runs setup.sh on every convergence pass, so
// a mint that overwrote would issue a fresh key (and orphan the old one
// in GCP, which the reaper deliberately never cleans up) on every
// deploy. The store here holds no minter credential at all, so a run
// that got as far as minting would fail rather than pass quietly.
func TestMintGeminiKeyLeavesAnExistingKeyAlone(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "secrets", "gemini-api-key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("the-key-an-operator-placed"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := secrets.New(filepath.Join(dataDir, "secrets"))
	err := secretsMintGeminiKey(store, dataDir, []string{"-project", "test-project"})
	if err != nil {
		t.Fatalf("mint-gemini-key over an existing key: %v", err)
	}

	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the-key-an-operator-placed" {
		t.Errorf("key file = %q, want it left exactly as it was", got)
	}
}

// An empty file is not a key -- seed_secret in scripts/setup.sh
// treats it the same way (`[ -s "$path" ]`), so a truncated or
// zero-length file must not be mistaken for one already in place.
func TestMintGeminiKeyTreatsAnEmptyFileAsAbsent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	keyPath := filepath.Join(dataDir, "secrets", "gemini-api-key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	store := secrets.New(filepath.Join(dataDir, "secrets"))
	err := secretsMintGeminiKey(store, dataDir, []string{"-project", "test-project"})
	// It must get past the "already there" guard and fail trying to
	// resolve the minter credential this empty store does not hold --
	// not return nil having silently skipped.
	if err == nil {
		t.Fatal("expected an empty key file to be treated as absent, and the mint to then fail with no minter credential")
	}
}

func TestMintGeminiKeyRequiresAProject(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store := secrets.New(filepath.Join(dataDir, "secrets"))
	if err := secretsMintGeminiKey(store, dataDir, nil); err == nil {
		t.Fatal("expected -project to be required")
	}
}

func TestWriteSecretFileIsOwnerOnlyAndExact(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "gemini-api-key")
	if err := writeSecretFile(path, "a-key-value"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a-key-value" {
		t.Errorf("contents = %q, want %q -- written with no trailing newline, the same shape readTrimmedFile and seed_secret expect", got, "a-key-value")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600: a credential must not be group- or world-readable", perm)
	}
}

// The temporary file writeSecretFile stages through must not survive a
// successful write -- a leftover would be a second copy of the key,
// sitting in the same directory with a name nothing cleans up.
func TestWriteSecretFileLeavesNoTemporaryBehind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gemini-api-key")
	if err := writeSecretFile(path, "a-key-value"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "gemini-api-key" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the key file itself", names)
	}
}
