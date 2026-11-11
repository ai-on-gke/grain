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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentdrelease "github.com/ai-on-gke/grain/pkg/agentd/release"
)

func TestBuildAgentdScriptExistsAndIsExecutable(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(t), "scripts", "build-agentd.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("scripts/build-agentd.sh is not executable: mode %v", info.Mode())
	}

	cmd := exec.Command("bash", "-n", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n syntax check failed: %v\n%s", err, string(out))
	}
}

func TestBuildAgentdScriptProducesStaticSignedBinariesAndRotatedManifests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build test in short mode")
	}

	root := repoRoot(t)
	outDir := t.TempDir()
	keyDir := t.TempDir()

	// Key A (pinned in the built binary) rotates to Key B (used to sign the release),
	// exercising both build-agentd.sh and release-assets.sh agentd-sign/agentd-validate.
	pubA, privA, err := agentdrelease.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubB, privB, err := agentdrelease.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubAPEM, _ := agentdrelease.FormatPublicKeyPEM(pubA)
	privBPEM, _ := agentdrelease.FormatPrivateKeyPEM(privB)
	rot, err := agentdrelease.SignKeyRotation(privA, pubB, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	rotJSON, _ := json.Marshal([]agentdrelease.KeyRotation{rot})

	pubAPath := filepath.Join(keyDir, "pubA.pem")
	privBPath := filepath.Join(keyDir, "privB.pem")
	rotPath := filepath.Join(keyDir, "rotations.json")
	_ = os.WriteFile(pubAPath, pubAPEM, 0o644)
	_ = os.WriteFile(privBPath, privBPEM, 0o600)
	_ = os.WriteFile(rotPath, rotJSON, 0o644)

	const rel = "v0.0.0-build-agentd-test"
	buildCmd := exec.Command("bash", filepath.Join(root, "scripts", "build-agentd.sh"), outDir)
	buildCmd.Dir = root
	buildCmd.Env = append(os.Environ(),
		"RELEASE="+rel,
		"AGENTD_RELEASE_PUBLIC_KEY="+string(pubAPEM),
	)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build-agentd.sh failed: %v\n%s", err, out)
	}

	// Sign manifests via release-assets.sh agentd-sign (OpenSSL + jq) and verify
	// against Key A via both release-assets.sh agentd-validate and Go's VerifyBinary.
	signCmd := exec.Command("bash", filepath.Join(root, "scripts", "release-assets.sh"), "agentd-sign", outDir)
	signCmd.Dir = root
	signCmd.Env = append(os.Environ(),
		"AGENTD_RELEASE_SIGNING_KEY="+privBPath,
		"AGENTD_RELEASE_ROTATIONS="+rotPath,
		"AGENTD_RELEASE_PUBLIC_KEY_FILE="+pubAPath,
	)
	if out, err := signCmd.CombinedOutput(); err != nil {
		t.Fatalf("release-assets.sh agentd-sign failed: %v\n%s", err, out)
	}

	for _, plat := range agentdrelease.Platforms {
		binName, _ := agentdrelease.AssetName(plat)
		mfName, _ := agentdrelease.ManifestAssetName(plat)
		binBytes, err := os.ReadFile(filepath.Join(outDir, binName))
		if err != nil || len(binBytes) == 0 {
			t.Fatalf("missing binary %s: %v", binName, err)
		}
		mfRaw, err := os.ReadFile(filepath.Join(outDir, mfName))
		if err != nil {
			t.Fatalf("missing manifest %s: %v", mfName, err)
		}
		m, err := agentdrelease.ParseManifest(mfRaw)
		if err != nil {
			t.Fatalf("ParseManifest(%s): %v", mfName, err)
		}
		if _, err := agentdrelease.VerifyBinary(m, binBytes, pubA); err != nil {
			t.Errorf("VerifyBinary(%s) from pinned Key A: %v", plat, err)
		}

		// Check CGO_ENABLED=0 and version/public-key ldflags stamps.
		verOut, err := exec.Command("go", "version", "-m", filepath.Join(outDir, binName)).CombinedOutput()
		if err != nil {
			t.Fatalf("go version -m %s: %v\n%s", binName, err, verOut)
		}
		for _, want := range []string{
			"CGO_ENABLED=0",
			"pkg/version.Release=" + rel,
			"pkg/agentd/release.ReleasePublicKey=" + agentdrelease.PublicKeyString(pubA),
		} {
			if !strings.Contains(string(verOut), want) {
				t.Errorf("%s missing %q in go version -m:\n%s", binName, want, verOut)
			}
		}
	}
}
