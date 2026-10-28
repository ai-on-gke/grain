package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildCLIScriptExistsAndIsExecutable(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(t), "scripts", "build-cli.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("scripts/build-cli.sh is not executable: mode %v", info.Mode())
	}

	cmd := exec.Command("bash", "-n", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n syntax check failed: %v\n%s", err, string(out))
	}
}

func TestBuildCLIScriptProducesBinariesAndTarballs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build test in short mode")
	}

	outDir := t.TempDir()
	scriptPath := filepath.Join(repoRoot(t), "scripts", "build-cli.sh")

	cmd := exec.Command("bash", scriptPath, outDir)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build-cli.sh failed: %v\n%s", err, string(out))
	}

	expectedFiles := []string{
		"grain-darwin-arm64",
		"grain-darwin-arm64.tar.gz",
		"grain-darwin-amd64",
		"grain-darwin-amd64.tar.gz",
		"grain-linux-arm64",
		"grain-linux-arm64.tar.gz",
		"grain-linux-amd64",
		"grain-linux-amd64.tar.gz",
		"checksums.txt",
	}

	for _, file := range expectedFiles {
		p := filepath.Join(outDir, file)
		if info, err := os.Stat(p); err != nil || info.Size() == 0 {
			t.Errorf("expected non-empty file %s, stat err: %v", file, err)
		}
	}
}
