package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithin(t *testing.T) {
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	root := filepath.Join(base, "work")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "sub"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(base+"/work-other", 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"out":      outside,
		"rel-out":  "../outside",
		"in":       filepath.Join(root, "sub"),
		"dangling": filepath.Join(outside, "missing"),
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ p, want string }{
		{"", root},
		{".", root},
		{"sub", filepath.Join(root, "sub")},
		{"sub/new/file.txt", filepath.Join(root, "sub", "new", "file.txt")},
		{"in/x", filepath.Join(root, "sub", "x")},
		{"sub/../x", filepath.Join(root, "x")},
		{filepath.Join(root, "sub"), filepath.Join(root, "sub")},
	} {
		got, err := Within(root, tc.p)
		if err != nil || got != tc.want {
			t.Errorf("Within(root, %q) = %q, %v; want %q", tc.p, got, err, tc.want)
		}
	}
	// A root reached through a symlink is the directory it points at.
	if got, err := Within(rootLink, "sub"); err != nil || got != filepath.Join(root, "sub") {
		t.Errorf("Within(root-link, sub) = %q, %v", got, err)
	}

	for _, p := range []string{
		"..",
		"../outside",
		"sub/../../outside",
		outside,
		base + "/work-other",
		"../work-other/x",
		"out",
		"out/secret.txt",
		"rel-out/new",
		"dangling",
		"/",
	} {
		got, err := Within(root, p)
		if err == nil {
			t.Errorf("Within(root, %q) = %q, want an error", p, got)
		}
	}
	if _, err := Within(root, "out/x"); !errors.Is(err, ErrOutside) {
		t.Errorf("an escape through a symlink is %v, want ErrOutside", err)
	}
	if _, err := Within(filepath.Join(base, "missing"), "x"); err == nil {
		t.Error("a root that does not exist was accepted")
	}
}
