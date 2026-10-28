package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
)

// TestCodexLoginFilesCoversEveryLoginARunCanShare: the refresher renews
// every login a codex run can be given -- each backend's, the UI-managed
// shared one and the operator's -codex-auth-file -- and nothing else in
// the secrets directory.
func TestCodexLoginFilesCoversEveryLoginARunCanShare(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), CodexAuthFile: filepath.Join(t.TempDir(), "operator-auth.json")}
	managed := ManagedCodexAuthFile(cfg.DataDir)
	backend := codex.BackendAuthFile(managed, "one")
	for _, path := range []string{managed, backend, filepath.Join(filepath.Dir(managed), "other-secret.json")} {
		if err := codex.WriteAuthFile(path, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	got := codexLoginFiles(cfg)
	for _, want := range []string{managed, backend, cfg.CodexAuthFile} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is not refreshed: %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("refreshing %v, want exactly the three logins", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(managed), "other-secret.json")); err != nil {
		t.Fatal(err)
	}
}
