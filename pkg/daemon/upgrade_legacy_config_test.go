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

package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

func TestNewUpgraderFallsBackWhenStoredConfigNamesReleaseAssets(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	dataDir := t.TempDir()
	refFile := filepath.Join(dataDir, "image.env")
	if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE=docker.io/grainai/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overrideFile := filepath.Join(dataDir, "upgrade-image-source.json")
	if err := os.WriteFile(overrideFile, []byte(`{"mode":"release-assets","releaseRepository":"github.com/ai-on-gke/grain"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAIN_UPGRADE_IMAGE_SOURCE", "")

	u := newUpgrader(Config{
		DataDir:             dataDir,
		UpgradeImage:        "docker.io/grainai/grain",
		UpgradeImageRefFile: refFile,
	}, nil, nil)
	if u == nil {
		t.Fatal("newUpgrader returned nil")
	}
	if got := u.EffectiveImageSource(); got != upgrade.ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource() = %q, want %q", got, upgrade.ImageSourceRegistry)
	}
	if !strings.Contains(buf.String(), "WARNING:") || !strings.Contains(buf.String(), "release-assets") {
		t.Fatalf("expected warning naming release-assets, got:\n%s", buf.String())
	}
	migrated, err := os.ReadFile(overrideFile)
	if err != nil {
		t.Fatalf("reading migrated override file: %v", err)
	}
	if !strings.Contains(string(migrated), `"mode": "registry"`) {
		t.Errorf("migrated override file = %s, want mode registry", migrated)
	}
}
