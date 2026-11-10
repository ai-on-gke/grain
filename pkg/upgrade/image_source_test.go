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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

func TestEffectiveImageSourceDefaultsToRegistryEverywhere(t *testing.T) {
	const track installtrack.Track = "test-edition-registry-track"
	UnregisterTrackDefaults(track)
	t.Cleanup(func() { UnregisterTrackDefaults(track) })

	// Unconfigured track, no VerifyCmd -> registry (not release-assets).
	u := New(Config{Image: &ImageConfig{Repository: "docker.io/grainai/grain", Track: track}})
	if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource() = %q, want %q", got, ImageSourceRegistry)
	}
	if u.IsImageSourceExplicit() {
		t.Fatal("IsImageSourceExplicit() = true on unpinned track, want false")
	}
	if fb := u.EffectiveImageFallback(); fb != "" {
		t.Fatalf("EffectiveImageFallback() = %q, want empty", fb)
	}

	// Edition track defaults (matching the Google layer's TrackImageDefaults{Source, Fallback}).
	RegisterTrackDefaults(track, TrackImageDefaults{
		Source:   ImageSourceRegistry,
		Fallback: "none",
	})
	d, ok := TrackDefaultsFor(track)
	if !ok || d.Source != ImageSourceRegistry || d.Fallback != "none" {
		t.Fatalf("TrackDefaultsFor(%q) = (%+v, %v)", track, d, ok)
	}
	u = New(Config{Image: &ImageConfig{Repository: "us-docker.pkg.dev/example/grain/grain", Track: track}})
	if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource() with track defaults = %q, want %q", got, ImageSourceRegistry)
	}
	if !u.IsImageSourceExplicit() {
		t.Fatal("IsImageSourceExplicit() = false with registered Source, want true")
	}
	if fb := u.EffectiveImageFallback(); fb != "" {
		t.Fatalf("EffectiveImageFallback() with Fallback=none = %q, want empty", fb)
	}

	// Legacy "release-assets" value on ImageConfig.Source still resolves safely to registry.
	u = New(Config{Image: &ImageConfig{Repository: "docker.io/grainai/grain", Source: ImageSource("release-assets")}})
	if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource() with legacy release-assets = %q, want %q", got, ImageSourceRegistry)
	}
}

func TestNormalizeImageSourceAndFallbackRejectRemovedReleaseAssets(t *testing.T) {
	if got, err := NormalizeImageSource(""); err != nil || got != ImageSourceRegistry {
		t.Fatalf("NormalizeImageSource(\"\") = (%q, %v), want (%q, nil)", got, err, ImageSourceRegistry)
	}
	if got, err := NormalizeImageSource("registry"); err != nil || got != ImageSourceRegistry {
		t.Fatalf("NormalizeImageSource(\"registry\") = (%q, %v), want (%q, nil)", got, err, ImageSourceRegistry)
	}
	if _, err := NormalizeImageSource("release-assets"); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("NormalizeImageSource(\"release-assets\") error = %v, want removal message", err)
	}
	if fb, err := ParseImageFallback("none"); err != nil || fb != "" {
		t.Fatalf("ParseImageFallback(\"none\") = (%q, %v), want (\"\", nil)", fb, err)
	}
	if fb, err := ParseImageFallback("registry"); err != nil || fb != ImageSourceRegistry {
		t.Fatalf("ParseImageFallback(\"registry\") = (%q, %v), want (%q, nil)", fb, err, ImageSourceRegistry)
	}
	if _, err := ParseImageFallback("release-assets"); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("ParseImageFallback(\"release-assets\") error = %v, want removal message", err)
	}
}

func TestSetImageSourceAndLegacyOverrideMigration(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "upgrade-image-source.json")
	// An older deployment's persisted release-assets override is ignored cleanly on startup.
	if err := os.WriteFile(overridePath, []byte(`{"mode":"release-assets","fallback":"release-assets","fallbackExplicit":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u := New(Config{
		StatusFile: filepath.Join(dir, "status.json"),
		Image: &ImageConfig{
			Repository:         "docker.io/grainai/grain",
			RefFile:            filepath.Join(dir, "image.env"),
			SourceOverrideFile: overridePath,
		},
	})
	if got := u.EffectiveImageSource(); got != ImageSourceRegistry {
		t.Fatalf("EffectiveImageSource after legacy override = %q, want %q", got, ImageSourceRegistry)
	}
	if u.IsImageSourceExplicit() {
		t.Fatal("legacy release-assets override should be cleared to default")
	}

	if err := u.SetImageSource(ImageSourceOverride{Mode: "release-assets"}); err == nil {
		t.Fatal("SetImageSource(release-assets) succeeded, want error")
	}
	if err := u.SetImageSource(ImageSourceOverride{Mode: "registry"}); err != nil {
		t.Fatalf("SetImageSource(registry): %v", err)
	}
	if !u.IsImageSourceExplicit() || u.EffectiveImageSource() != ImageSourceRegistry {
		t.Fatalf("after SetImageSource(registry): explicit=%v source=%q", u.IsImageSourceExplicit(), u.EffectiveImageSource())
	}

	locked := New(Config{
		StatusFile: filepath.Join(dir, "status-locked.json"),
		Image: &ImageConfig{
			Repository:   "docker.io/grainai/grain",
			RefFile:      filepath.Join(dir, "image.env"),
			Source:       ImageSourceRegistry,
			SourceLocked: true,
		},
	})
	if err := locked.SetImageSource(ImageSourceOverride{Mode: "default"}); !errors.Is(err, ErrImageSourceLocked) {
		t.Fatalf("locked SetImageSource error = %v, want ErrImageSourceLocked", err)
	}
}
