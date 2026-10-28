package staterepo

import (
	"os"
	"path/filepath"
	"testing"
)

// A {"file": ...} cell naming a symlink committed into the state repo
// must not be read through to wherever the link points (#2393).
func TestResolveFileValueRefusesSymlinks(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside the repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(repo, "note.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "notes")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "real.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(repo, "link-in.txt")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"note.txt", "notes/secret.txt", "link-in.txt"} {
		if got, err := resolveFileValue(repo, map[string]any{"file": rel}); err == nil {
			t.Errorf("resolveFileValue(%q) = %q, want an error", rel, got)
		}
	}
	got, err := resolveFileValue(repo, map[string]any{"file": "real.txt"})
	if err != nil || got != "inside" {
		t.Fatalf("resolveFileValue(real.txt) = %q, %v; want %q", got, err, "inside")
	}
}

// Export writes into a checkout that may hold committed symlinks; it must
// replace them, not write through them.
func TestExportWritesDoNotFollowSymlinks(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()

	link := filepath.Join(repo, "README.md")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileIfChanged(link, []byte("readme")); err != nil {
		t.Fatal(err)
	}
	assertRegularFile(t, link, "readme")

	if err := os.Symlink(victim, filepath.Join(repo, "body.md")); err != nil {
		t.Fatal(err)
	}
	if err := writeRepoFileIfChanged(repo, "body.md", []byte("body")); err != nil {
		t.Fatal(err)
	}
	assertRegularFile(t, filepath.Join(repo, "body.md"), "body")

	if err := os.Symlink(outside, filepath.Join(repo, "templates")); err != nil {
		t.Fatal(err)
	}
	if err := writeRepoFileIfChanged(repo, filepath.Join("templates", "victim.txt"), []byte("pwned")); err == nil {
		t.Error("writeRepoFileIfChanged through a directory symlink out of the repo succeeded, want an error")
	}

	if b, err := os.ReadFile(victim); err != nil || string(b) != "untouched" {
		t.Fatalf("file outside the repo = %q, %v; want it untouched", b, err)
	}
}

func assertRegularFile(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is %v, want a regular file", path, info.Mode())
	}
	if b, _ := os.ReadFile(path); string(b) != want {
		t.Fatalf("%s = %q, want %q", path, b, want)
	}
}
