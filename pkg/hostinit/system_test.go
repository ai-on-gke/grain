package hostinit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRealSystemRunResolvesAgainstGivenEnvPath is the regression case for
// System.Run installing a toolchain and then being unable to find it: the
// command named is only on the PATH this call's own env adds, not on the
// PATH this test process itself inherited.
func TestRealSystemRunResolvesAgainstGivenEnvPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit based lookup doesn't apply on windows")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "onlyhere")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho found\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	sys := RealSystem{}
	out, err := sys.Run(context.Background(), "", []string{"PATH=" + dir}, "onlyhere")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "found" {
		t.Fatalf("output = %q, want %q", out, "found")
	}
}

// TestRealSystemRunFindsSbinToolsOffPath is the 10-05 walkthrough's H1:
// Debian keeps /usr/sbin off a non-root user's PATH, so getcap was "not
// found" and a non-root re-run reported an already-granted
// CAP_NET_BIND_SERVICE as a manual step (exit 10 on a deployment that was
// up). A name on no PATH is looked for in sbinDirs.
func TestRealSystemRunFindsSbinToolsOffPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable-bit based lookup doesn't apply on windows")
	}
	sbin := t.TempDir()
	if err := os.WriteFile(filepath.Join(sbin, "fakegetcap"), []byte("#!/bin/sh\necho \"$1 cap_net_bind_service=ep\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sbinDirs
	sbinDirs = []string{sbin}
	t.Cleanup(func() { sbinDirs = old })

	out, err := RealSystem{}.Run(context.Background(), "", []string{"PATH=" + t.TempDir() + ":/bin:/usr/bin"}, "fakegetcap", "/x/grain")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "/x/grain cap_net_bind_service=ep" {
		t.Fatalf("output = %q", out)
	}
}

func TestLookPathIn(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mytool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExe := filepath.Join(dir, "notexecutable")
	if err := os.WriteFile(notExe, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := lookPathIn("mytool", dir); err != nil || got != exe {
		t.Fatalf("lookPathIn(mytool) = %q, %v; want %q, nil", got, err, exe)
	}
	if _, err := lookPathIn("notexecutable", dir); err == nil {
		t.Fatalf("lookPathIn(notexecutable) succeeded, want error for non-executable file")
	}
	if _, err := lookPathIn("missing", dir); err == nil {
		t.Fatalf("lookPathIn(missing) succeeded, want error")
	}
}
