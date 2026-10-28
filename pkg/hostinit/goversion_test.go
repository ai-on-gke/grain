package hostinit

import (
	"os"
	"path/filepath"
	"testing"
)

const fixtureGoReleaseIndex = `[
  {
    "version": "go1.26.6",
    "stable": true,
    "files": [
      {"filename":"go1.26.6.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"archive"},
      {"filename":"go1.26.6.linux-arm64.tar.gz","os":"linux","arch":"arm64","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","kind":"archive"},
      {"filename":"go1.26.6.src.tar.gz","os":"","arch":"","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","kind":"source"}
    ]
  },
  {
    "version": "go1.25.1",
    "stable": true,
    "files": [
      {"filename":"go1.25.1.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","kind":"archive"}
    ]
  }
]`

func TestGoTarballSHA256FindsTheRightFile(t *testing.T) {
	t.Parallel()
	filename, sha, err := goTarballSHA256([]byte(fixtureGoReleaseIndex), "1.26.6", "linux", "amd64")
	if err != nil {
		t.Fatalf("goTarballSHA256: %v", err)
	}
	if filename != "go1.26.6.linux-amd64.tar.gz" {
		t.Errorf("filename = %q, want go1.26.6.linux-amd64.tar.gz", filename)
	}
	if want := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; sha != want {
		t.Errorf("sha256 = %q, want %q", sha, want)
	}
}

func TestGoTarballSHA256NeverReturnsTheSourceArchive(t *testing.T) {
	t.Parallel()
	// A "linux"/"amd64" lookup must never match the source tarball,
	// whose os/arch fields are empty -- an empty-string match would be a
	// digest for the wrong file entirely.
	_, _, err := goTarballSHA256([]byte(fixtureGoReleaseIndex), "1.26.6", "", "")
	if err == nil {
		t.Fatalf("goTarballSHA256(goos=\"\", goarch=\"\") should not resolve to the source archive's kind")
	}
}

func TestGoTarballSHA256ErrorsOnAnUnknownVersion(t *testing.T) {
	t.Parallel()
	if _, _, err := goTarballSHA256([]byte(fixtureGoReleaseIndex), "1.99.0", "linux", "amd64"); err == nil {
		t.Fatalf("expected an error for a version go.dev's index does not have")
	}
}

func TestGoTarballSHA256ErrorsOnAnUnknownArch(t *testing.T) {
	t.Parallel()
	if _, _, err := goTarballSHA256([]byte(fixtureGoReleaseIndex), "1.26.6", "linux", "riscv64"); err == nil {
		t.Fatalf("expected an error for an arch this release has no archive for")
	}
}

func TestGoBuildVersionReadsGoDirective(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.26.6\n\nrequire (\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := goBuildVersion(dir)
	if err != nil {
		t.Fatalf("goBuildVersion: %v", err)
	}
	if got != "1.26.6" {
		t.Errorf("goBuildVersion = %q, want 1.26.6", got)
	}
}

func TestGoBuildVersionPrefersToolchainLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.26.6\n\ntoolchain go1.26.10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := goBuildVersion(dir)
	if err != nil {
		t.Fatalf("goBuildVersion: %v", err)
	}
	if got != "1.26.10" {
		t.Errorf("goBuildVersion = %q, want 1.26.10 (the toolchain line, not the go directive)", got)
	}
}

func TestGoDirectiveVersionErrorsWithNoGoMod(t *testing.T) {
	t.Parallel()
	if _, err := goBuildVersion(t.TempDir()); err == nil {
		t.Fatalf("expected an error reading go.mod from a directory with none")
	}
}

func TestGoVersionAtLeast(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
		atLeast   bool
	}{
		{"1.26.6", "1.26.6", true},  // exact match
		{"1.27.1", "1.26.6", true},  // newer minor
		{"1.26.7", "1.26.6", true},  // newer patch
		{"1.26.5", "1.26.6", false}, // older patch
		{"1.25.9", "1.26.6", false}, // older minor
		{"2.0.0", "1.26.6", true},   // newer major
		{"1.26", "1.26.6", false},   // got has no patch, want does
	}
	for _, c := range cases {
		if got := goVersionAtLeast(c.got, c.want); got != c.atLeast {
			t.Errorf("goVersionAtLeast(%q, %q) = %v, want %v", c.got, c.want, got, c.atLeast)
		}
	}
}

func TestGoVersionFromOutput(t *testing.T) {
	t.Parallel()
	got, ok := goVersionFromOutput("go version go1.26.6 linux/amd64")
	if !ok || got != "1.26.6" {
		t.Errorf("goVersionFromOutput = (%q, %v), want (1.26.6, true)", got, ok)
	}
	if _, ok := goVersionFromOutput("not go output at all"); ok {
		t.Errorf("goVersionFromOutput should report false for unrecognized output")
	}
}
