package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointLoginDefs writes content to a temporary login.defs and points the
// package at it for the duration of the test. An empty content means the
// file does not exist at all.
func pointLoginDefs(t *testing.T, content string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "login.defs")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	old := loginDefsPath
	loginDefsPath = path
	t.Cleanup(func() { loginDefsPath = old })
}

// The regression this file exists for: grain's guest setup script runs
// useradd, which lives in /usr/sbin, and a root session whose PATH had
// no sbin directory on it failed with "useradd: not found".
func TestRootGetsTheSbinDirectories(t *testing.T) {
	pointLoginDefs(t, "")

	got := defaultPATH(0)
	for _, dir := range []string{"/usr/sbin", "/sbin"} {
		if !strings.Contains(got, dir) {
			t.Errorf("root PATH = %q, want %s on it", got, dir)
		}
	}
}

// An ordinary user does not get them, because that is the split login(1)
// itself makes and a session here is a login session.
func TestAnOrdinaryUserDoesNotGetTheSbinDirectories(t *testing.T) {
	pointLoginDefs(t, "")

	if got := defaultPATH(1000); strings.Contains(got, "sbin") {
		t.Errorf("PATH = %q, want no sbin directory on it", got)
	}
}

// A guest that has edited login.defs means it: that file is what login(1)
// and sshd(8) read to answer this same question.
func TestLoginDefsWins(t *testing.T) {
	pointLoginDefs(t, `
# ENV_SUPATH  PATH=/commented/out
ENV_SUPATH	PATH=/root/bin:/usr/sbin
ENV_PATH	PATH=/home/bin	# trailing comment
`)

	if got, want := defaultPATH(0), "/root/bin:/usr/sbin"; got != want {
		t.Errorf("root PATH = %q, want %q", got, want)
	}
	if got, want := defaultPATH(1000), "/home/bin"; got != want {
		t.Errorf("user PATH = %q, want %q", got, want)
	}
}

// A login.defs that says nothing about a key falls back rather than
// handing the session an empty PATH.
func TestAFileThatSetsNeitherKeyFallsBack(t *testing.T) {
	pointLoginDefs(t, "UMASK 022\n")

	if got, want := defaultPATH(0), fallbackSuPATH; got != want {
		t.Errorf("root PATH = %q, want the fallback %q", got, want)
	}
	if got, want := defaultPATH(1000), fallbackPATH; got != want {
		t.Errorf("user PATH = %q, want the fallback %q", got, want)
	}
}
