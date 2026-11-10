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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// ImageSource selects where ModeImage fetches candidate daemon and sandbox
// container images from.
type ImageSource string

const (
	// ImageSourceRegistry pulls container images from an OCI registry with
	// `docker pull` (the only supported image source).
	ImageSourceRegistry ImageSource = "registry"
)

// ErrImageSourceLocked is returned when SetImageSource is called on an
// Upgrader whose image source was locked by a daemon flag or environment variable.
var ErrImageSourceLocked = errors.New("upgrade image source is set by a daemon flag or environment variable and cannot be modified")

// ImageSourceOverride is the operator-editable subset of ImageConfig's image
// source settings persisted in ImageConfig.SourceOverrideFile when SourceLocked is false.
type ImageSourceOverride struct {
	Mode             string `json:"mode,omitempty"`
	Fallback         string `json:"fallback,omitempty"`
	FallbackExplicit bool   `json:"fallbackExplicit,omitempty"`
}

// SetImageSource updates and persists the live image source configuration when
// ModeImage is active and SourceLocked is false.
func (u *Upgrader) SetImageSource(ov ImageSourceOverride) error {
	if u.Mode() != ModeImage || u.cfg.Image == nil {
		return errors.New("upgrade image source only applies to container-image deployments (-upgrade-image)")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cfg.Image.SourceLocked {
		return ErrImageSourceLocked
	}
	mode := strings.TrimSpace(ov.Mode)
	if mode != "" && !strings.EqualFold(mode, "default") {
		norm, err := NormalizeImageSource(mode)
		if err != nil {
			return err
		}
		mode = string(norm)
	} else {
		mode = ""
	}
	var fb ImageSource
	fbRaw := strings.TrimSpace(ov.Fallback)
	if ov.FallbackExplicit || fbRaw != "" {
		parsed, err := ParseImageFallback(fbRaw)
		if err != nil {
			return err
		}
		fb = parsed
	}
	clean := ImageSourceOverride{
		Mode:             mode,
		Fallback:         string(fb),
		FallbackExplicit: ov.FallbackExplicit,
	}
	if p := strings.TrimSpace(u.cfg.Image.SourceOverrideFile); p != "" {
		raw, err := json.MarshalIndent(clean, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	u.applyImageSourceOverrideLocked(clean)
	u.tags.mu.Lock()
	u.tags.repo = ""
	u.tags.tags = nil
	u.tags.fetched = time.Time{}
	u.tags.mu.Unlock()
	return nil
}

func (u *Upgrader) loadImageSourceOverride() {
	if u.cfg.Image == nil || u.cfg.Image.SourceLocked {
		return
	}
	p := strings.TrimSpace(u.cfg.Image.SourceOverrideFile)
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var ov ImageSourceOverride
	if json.Unmarshal(raw, &ov) != nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(ov.Mode), "release-assets") {
		log.Printf("grain upgrade: ignoring removed image source %q in %s; using %q", ov.Mode, p, ImageSourceRegistry)
		ov.Mode = ""
	}
	if strings.EqualFold(strings.TrimSpace(ov.Fallback), "release-assets") {
		log.Printf("grain upgrade: ignoring removed fallback %q in %s", ov.Fallback, p)
		ov.Fallback, ov.FallbackExplicit = "", false
	}
	u.applyImageSourceOverrideLocked(ov)
}

func (u *Upgrader) applyImageSourceOverrideLocked(ov ImageSourceOverride) {
	if ov.Mode == "" || strings.EqualFold(ov.Mode, "default") {
		u.cfg.Image.Source = ""
	} else if norm, err := NormalizeImageSource(ov.Mode); err == nil {
		u.cfg.Image.Source = norm
	}
	if ov.FallbackExplicit || ov.Fallback != "" {
		fb, err := ParseImageFallback(ov.Fallback)
		if err == nil {
			u.cfg.Image.Fallback = fb
			u.cfg.Image.FallbackExplicit = ov.FallbackExplicit
		}
	} else {
		u.cfg.Image.Fallback = ""
		u.cfg.Image.FallbackExplicit = false
	}
}

// NormalizeImageSource normalizes a configured image source, defaulting empty
// to ImageSourceRegistry.
func NormalizeImageSource(raw string) (ImageSource, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ImageSourceRegistry):
		return ImageSourceRegistry, nil
	case "release-assets":
		return "", fmt.Errorf("upgrade image source %q has been removed; use %q", raw, ImageSourceRegistry)
	default:
		return "", fmt.Errorf("invalid upgrade image source %q: want %q", raw, ImageSourceRegistry)
	}
}

// ParseImageFallback normalizes a configured fallback image source. Empty,
// "none", or "off" disables fallback.
func ParseImageFallback(raw string) (ImageSource, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none", "off":
		return "", nil
	case string(ImageSourceRegistry):
		return ImageSourceRegistry, nil
	case "release-assets":
		return "", fmt.Errorf("upgrade image fallback %q has been removed; want \"none\" or %q", raw, ImageSourceRegistry)
	default:
		return "", fmt.Errorf("invalid upgrade image fallback %q: want \"none\" or %q", raw, ImageSourceRegistry)
	}
}

// TrackImageDefaults holds edition-registered default upgrade image source
// settings for an installtrack.Track. An edition registers its defaults from
// init() via RegisterTrackDefaults.
type TrackImageDefaults struct {
	Source   ImageSource
	Fallback ImageSource
}

var (
	trackDefaultsMu sync.RWMutex
	trackDefaults   = map[installtrack.Track]TrackImageDefaults{}
)

// RegisterTrackDefaults registers default upgrade image source settings for
// track, replacing any prior registration for the same track.
func RegisterTrackDefaults(track installtrack.Track, d TrackImageDefaults) {
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

// IsImageSourceExplicit reports whether the deployment's image source was
// explicitly pinned (via ImageConfig.Source or TrackImageDefaults.Source).
func (u *Upgrader) IsImageSourceExplicit() bool {
	if u.cfg.Image == nil {
		return false
	}
	if strings.TrimSpace(string(u.cfg.Image.Source)) != "" {
		return true
	}
	if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok && strings.TrimSpace(string(d.Source)) != "" {
		return true
	}
	return false
}

// EffectiveImageSource returns the active image source for ModeImage. Registry
// is the only supported image source; any empty or legacy "release-assets"
// value resolves to ImageSourceRegistry so no deployment is left on a source
// that no longer exists.
func (u *Upgrader) EffectiveImageSource() ImageSource {
	if u.cfg.Image == nil {
		return ImageSourceRegistry
	}
	if s := strings.TrimSpace(string(u.cfg.Image.Source)); s != "" {
		if strings.EqualFold(s, "release-assets") {
			return ImageSourceRegistry
		}
		if norm, err := NormalizeImageSource(s); err == nil {
			return norm
		}
		return u.cfg.Image.Source
	}
	if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok && d.Source != "" {
		if strings.EqualFold(string(d.Source), "release-assets") {
			return ImageSourceRegistry
		}
		if norm, err := NormalizeImageSource(string(d.Source)); err == nil {
			return norm
		}
		return d.Source
	}
	return ImageSourceRegistry
}

// EffectiveImageFallback returns the configured fallback image source for
// ModeImage, or "" when fallback is disabled or matches the primary source.
func (u *Upgrader) EffectiveImageFallback() ImageSource {
	if u.cfg.Image == nil {
		return ""
	}
	primary := u.EffectiveImageSource()
	var fb ImageSource
	if u.cfg.Image.FallbackExplicit {
		fb, _ = ParseImageFallback(string(u.cfg.Image.Fallback))
	} else if s := strings.TrimSpace(string(u.cfg.Image.Fallback)); s != "" {
		fb, _ = ParseImageFallback(s)
	} else if d, ok := TrackDefaultsFor(u.cfg.Image.Track); ok && d.Fallback != "" {
		fb, _ = ParseImageFallback(string(d.Fallback))
	}
	if fb == primary {
		return ""
	}
	return fb
}
