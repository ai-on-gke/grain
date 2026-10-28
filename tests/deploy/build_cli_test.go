// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
