package cli

// setup_test.go covers cmdSetupGCP's own flag validation -- the part
// reachable with no real GCP project or credential involved. The rest
// (gcpsetup.EnsureInfrastructure itself) is pkg/gcpsetup's own,
// network-free, fully covered by gcpsetup_test.go's fake Admin.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdSetupGCPRequiresProject(t *testing.T) {
	t.Parallel()
	err := cmdSetupGCP(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "-project") {
		t.Fatalf("cmdSetupGCP() = %v, want a -project error", err)
	}
}

func TestCmdSetupGCPMintKeyRequiresKeyOut(t *testing.T) {
	t.Parallel()
	err := cmdSetupGCP(context.Background(), []string{"-project", "proj", "-mint-key"})
	if err == nil || !strings.Contains(err.Error(), "-key-out") {
		t.Fatalf("cmdSetupGCP() = %v, want a -key-out error", err)
	}
}

func TestCmdSetupGCPRefusesToOverwriteAnExistingKeyFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"already":"here"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdSetupGCP(context.Background(), []string{"-project", "proj", "-mint-key", "-key-out", path})
	if err == nil || !strings.Contains(err.Error(), "already has content") {
		t.Fatalf("cmdSetupGCP() = %v, want a refusal to overwrite %s", err, path)
	}
}
