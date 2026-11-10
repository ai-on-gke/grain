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

package upgrade

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

func captureUpgradeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	resetLegacyReleaseAssetsWarnings()
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		resetLegacyReleaseAssetsWarnings()
	})
	return &buf
}

func TestStoredReleaseAssetsConfigMigratesAndFallsBackToRegistry(t *testing.T) {
	buf := captureUpgradeLogs(t)
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "upgrade-image-source.json")
	if err := os.WriteFile(overridePath, []byte(`{
  "mode": "release-assets",
  "fallback": "release-assets",
  "releaseRepository": "github.com/ai-on-gke/grain",
  "releaseAsset": "grain-image.tar.gz"
}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pulled := filepath.Join(dir, "pulled")
	refFile := filepath.Join(dir, "image.env")
	statusFile := filepath.Join(dir, "status.json")
	const wantDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	u := New(Config{
		StatusFile: statusFile,
		Image: &ImageConfig{
			Repository:         "docker.io/grainai/grain",
			RefFile:            refFile,
			SourceOverrideFile: overridePath,
			PullCmd:            []string{"sh", "-c", `printf '%s\n' "$1" >> "` + pulled + `"`, "sh"},
			DigestCmd:          []string{"sh", "-c", `printf 'docker.io/grainai/grain@%s\n' "` + wantDigest + `"`, "sh"},
			ListCmd:            []string{"true"},
		},
	})

	if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource() = %q, want %q", got, ImageSourceRegistry)
	}
	logs := buf.String()
	if got := strings.Count(logs, "WARNING:"); got != 1 {
		t.Fatalf("expected exactly 1 one-time warning, got %d in:\n%s", got, logs)
	}
	if !strings.Contains(logs, "release-assets") || !strings.Contains(logs, "registry") {
		t.Fatalf("warning log should mention release-assets and registry, got:\n%s", logs)
	}

	raw, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatalf("reading migrated override file: %v", err)
	}
	var migrated map[string]any
	if err := json.Unmarshal(raw, &migrated); err != nil {
		t.Fatalf("unmarshaling migrated override file: %v", err)
	}
	if migrated["mode"] != "registry" {
		t.Errorf("migrated mode = %v, want registry (raw: %s)", migrated["mode"], raw)
	}
	if _, has := migrated["releaseRepository"]; has {
		t.Errorf("migrated file kept releaseRepository: %s", raw)
	}

	// Simulate a fresh daemon restart reading the migrated file: no second warning.
	resetLegacyReleaseAssetsWarnings()
	buf.Reset()
	u2 := New(Config{
		StatusFile: statusFile,
		Image: &ImageConfig{
			Repository:         "docker.io/grainai/grain",
			RefFile:            refFile,
			SourceOverrideFile: overridePath,
		},
	})
	if got := u2.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("after restart EffectiveImageSource() = %q, want %q", got, ImageSourceRegistry)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warning after file migration, got:\n%s", buf.String())
	}

	// Running an upgrade pulls from the container registry.
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start(main): %v", err)
	}
	<-u.Done()
	st, err := u.Status()
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if st.Phase != PhaseOK {
		t.Fatalf("Status.Phase = %q (detail %q), want %q", st.Phase, st.Detail, PhaseOK)
	}
	if got := strings.TrimSpace(readFile(t, pulled)); got != "docker.io/grainai/grain:main" {
		t.Errorf("pulled = %q, want docker.io/grainai/grain:main", got)
	}
}

func TestConfiguredReleaseAssetsSourceFallsBackWithOneTimeWarning(t *testing.T) {
	buf := captureUpgradeLogs(t)
	dir := t.TempDir()

	for range 3 {
		u := New(Config{
			StatusFile: filepath.Join(dir, "status.json"),
			Image: &ImageConfig{
				Repository: "docker.io/grainai/grain",
				RefFile:    filepath.Join(dir, "image.env"),
				Source:     ImageSource("release-assets"),
			},
		})
		if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
			t.Fatalf("EffectiveImageSource() = %q, want %q", got, ImageSourceRegistry)
		}
	}
	if got := strings.Count(buf.String(), "WARNING:"); got != 1 {
		t.Fatalf("expected 1 warning across repeated initializations, got %d:\n%s", got, buf.String())
	}

	buf.Reset()
	resetLegacyReleaseAssetsWarnings()
	norm, err := NormalizeImageSource("release-assets")
	if err != nil || norm != ImageSourceRegistry {
		t.Fatalf("NormalizeImageSource(release-assets) = (%q, %v), want (%q, nil)", norm, err, ImageSourceRegistry)
	}
	if _, err := NormalizeImageSource("release-assets"); err != nil {
		t.Fatalf("second NormalizeImageSource(release-assets): %v", err)
	}
	if got := strings.Count(buf.String(), "WARNING:"); got != 1 {
		t.Fatalf("expected 1 warning for NormalizeImageSource, got %d:\n%s", got, buf.String())
	}

	const track installtrack.Track = "compat-test-track"
	t.Cleanup(func() { UnregisterTrackDefaults(track) })
	RegisterTrackDefaults(track, TrackImageDefaults{Source: ImageSourceRegistry})
	if d, ok := TrackDefaultsFor(track); !ok || d.Source != ImageSourceRegistry {
		t.Fatalf("TrackDefaultsFor(%q) = (%+v, %v), want registry", track, d, ok)
	}
}
