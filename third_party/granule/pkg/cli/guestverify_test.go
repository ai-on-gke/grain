package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectChecksReadsAFileAndThenTheCommandLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checks")
	if err := os.WriteFile(path, []byte("[docker starts and answers]\nsystemctl start docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := collectChecks(path, []string{"test -e /run/granule/ready"})
	if err != nil {
		t.Fatalf("collectChecks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("collected %d checks, want 2: %+v", len(got), got)
	}
	if got[0].Name != "docker starts and answers" {
		t.Errorf("the file's check is named %q", got[0].Name)
	}
	// A one-liner is its own best description, and the verdict has to
	// name something its reader recognizes.
	if got[1].Name != "test -e /run/granule/ready" || got[1].Script != got[1].Name {
		t.Errorf("-check = %+v, want a check named after the command it runs", got[1])
	}
}

func TestCollectChecksRefusesAnEmptySet(t *testing.T) {
	// A verify with no checks would boot an image and pass it, which is
	// worse than not verifying at all: it produces a verdict nothing
	// stands behind.
	if _, err := collectChecks("", nil); err == nil {
		t.Fatal("collectChecks accepted an empty check set")
	}
}

// A check file that is not there has to fail before anything boots, not
// after the caller has waited three minutes for a VM.
func TestCollectChecksReportsAMissingFile(t *testing.T) {
	_, err := collectChecks(filepath.Join(t.TempDir(), "nope"), nil)
	if err == nil || !strings.Contains(err.Error(), "reading the check file") {
		t.Fatalf("err = %v, want it to name the file it could not read", err)
	}
}

// A check file that does not parse is reported against the file it came
// from: the message otherwise says "line 4" of nothing in particular.
func TestCollectChecksNamesTheFileItCouldNotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checks")
	if err := os.WriteFile(path, []byte("systemctl start docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := collectChecks(path, nil)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want it to name %s", err, path)
	}
}
