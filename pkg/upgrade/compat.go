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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// ImageSource names where ModeImage pulls candidate daemon and sandbox
// container images from. Only ImageSourceRegistry is supported; the type and
// TrackImageDefaults registration hooks remain so edition packages compiled
// against TrackImageDefaults{Source: ImageSourceRegistry} continue to build
// and legacy configurations can be migrated cleanly.
type ImageSource string

const (
	// ImageSourceRegistry pulls container images from an OCI registry with
	// `docker pull`.
	ImageSourceRegistry ImageSource = "registry"

	legacyReleaseAssetsSource = "release-assets"
)

// TrackImageDefaults holds edition-registered default upgrade image source
// settings for an installtrack.Track.
type TrackImageDefaults struct {
	Source ImageSource
}

var (
	trackDefaultsMu sync.RWMutex
	trackDefaults   = map[installtrack.Track]TrackImageDefaults{}

	legacySourceWarnMu sync.Mutex
	legacySourceWarned = map[string]bool{}
)

func warnLegacyReleaseAssetsOnce(where, message string) {
	legacySourceWarnMu.Lock()
	already := legacySourceWarned[where]
	legacySourceWarned[where] = true
	legacySourceWarnMu.Unlock()
	if !already {
		log.Printf("WARNING: %s", message)
	}
}

func resetLegacyReleaseAssetsWarnings() {
	legacySourceWarnMu.Lock()
	clear(legacySourceWarned)
	legacySourceWarnMu.Unlock()
}

// RegisterTrackDefaults registers default upgrade image source settings for
// track, replacing any prior registration for the same track. Any legacy
// "release-assets" value falls back to ImageSourceRegistry with a one-time
// warning.
func RegisterTrackDefaults(track installtrack.Track, d TrackImageDefaults) {
	if strings.EqualFold(strings.TrimSpace(string(d.Source)), legacyReleaseAssetsSource) {
		warnLegacyReleaseAssetsOnce("track:"+string(track), fmt.Sprintf(
			"grain upgrade: track %q registered retired image source %q; falling back to %q",
			track, d.Source, ImageSourceRegistry))
		d.Source = ImageSourceRegistry
	} else if strings.TrimSpace(string(d.Source)) == "" {
		d.Source = ImageSourceRegistry
	}
	trackDefaultsMu.Lock()
	defer trackDefaultsMu.Unlock()
	trackDefaults[track] = d
}

// UnregisterTrackDefaults removes registered defaults for track (for tests).
func UnregisterTrackDefaults(track installtrack.Track) {
	trackDefaultsMu.Lock()
	defer trackDefaultsMu.Unlock()
	delete(trackDefaults, track)
}

// TrackDefaultsFor returns any edition defaults registered for track.
func TrackDefaultsFor(track installtrack.Track) (TrackImageDefaults, bool) {
	trackDefaultsMu.RLock()
	defer trackDefaultsMu.RUnlock()
	d, ok := trackDefaults[track]
	return d, ok
}

// NormalizeImageSource normalizes a configured image source. Only "registry"
// (or empty, which defaults to "registry") is supported; a legacy
// "release-assets" value falls back to ImageSourceRegistry with a one-time
// warning so existing configurations do not prevent the daemon from starting.
func NormalizeImageSource(raw string) (ImageSource, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "", string(ImageSourceRegistry):
		return ImageSourceRegistry, nil
	case legacyReleaseAssetsSource:
		warnLegacyReleaseAssetsOnce("source:"+trimmed, fmt.Sprintf(
			"grain upgrade: image source %q is no longer supported; falling back to %q",
			raw, ImageSourceRegistry))
		return ImageSourceRegistry, nil
	default:
		return "", fmt.Errorf("invalid upgrade image source %q: want %q", raw, ImageSourceRegistry)
	}
}

// EffectiveImageSource returns the active image source for ModeImage, which is
// always ImageSourceRegistry.
func (u *Upgrader) EffectiveImageSource() ImageSource {
	return ImageSourceRegistry
}

type legacyImageSourceOverride struct {
	Mode              string `json:"mode,omitempty"`
	Fallback          string `json:"fallback,omitempty"`
	ReleaseRepository string `json:"releaseRepository,omitempty"`
	ReleaseAsset      string `json:"releaseAsset,omitempty"`
	Checksum          string `json:"checksum,omitempty"`
}

// migrateLegacyImageSource ensures a daemon whose stored configuration or
// ImageConfig still names "release-assets" starts cleanly on the registry
// source, logging a one-time warning and migrating any stored override file.
func (u *Upgrader) migrateLegacyImageSource() {
	if u.cfg.Image == nil {
		return
	}
	if raw := strings.TrimSpace(string(u.cfg.Image.Source)); raw != "" {
		if strings.EqualFold(raw, legacyReleaseAssetsSource) {
			warnLegacyReleaseAssetsOnce("cfg:"+raw, fmt.Sprintf(
				"grain upgrade: configured image source %q is no longer supported; falling back to %q",
				raw, ImageSourceRegistry))
		}
		u.cfg.Image.Source = ImageSourceRegistry
	}
	p := strings.TrimSpace(u.cfg.Image.SourceOverrideFile)
	if p == "" {
		return
	}
	MigrateLegacySourceOverrideFile(p)
}

// MigrateLegacySourceOverrideFile inspects path (for example
// <data-dir>/upgrade-image-source.json written by an earlier release). When it
// still names "release-assets" or release-asset fields, it logs a one-time
// warning and rewrites the file to {"mode": "registry"} so subsequent starts
// stay quiet and never fail.
func MigrateLegacySourceOverrideFile(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var ov legacyImageSourceOverride
	if json.Unmarshal(raw, &ov) != nil {
		return false
	}
	hasLegacyMode := strings.EqualFold(strings.TrimSpace(ov.Mode), legacyReleaseAssetsSource)
	hasLegacyFallback := strings.EqualFold(strings.TrimSpace(ov.Fallback), legacyReleaseAssetsSource)
	hasLegacyFields := strings.TrimSpace(ov.ReleaseRepository) != "" ||
		strings.TrimSpace(ov.ReleaseAsset) != "" ||
		strings.TrimSpace(ov.Checksum) != "" ||
		strings.TrimSpace(ov.Fallback) != ""
	if !hasLegacyMode && !hasLegacyFallback && !hasLegacyFields {
		return false
	}
	sourceDesc := strings.TrimSpace(ov.Mode)
	if sourceDesc == "" {
		sourceDesc = legacyReleaseAssetsSource
	}
	warnLegacyReleaseAssetsOnce("file:"+path, fmt.Sprintf(
		"grain upgrade: stored image source %q in %s is no longer supported; migrating to %q",
		sourceDesc, path, ImageSourceRegistry))
	migrated, err := json.MarshalIndent(legacyImageSourceOverride{Mode: string(ImageSourceRegistry)}, "", "  ")
	if err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr == nil {
			tmp := path + ".tmp"
			if wErr := os.WriteFile(tmp, append(migrated, '\n'), 0o644); wErr == nil {
				if rErr := os.Rename(tmp, path); rErr != nil {
					_ = os.Remove(tmp)
				}
			}
		}
	}
	return true
}
