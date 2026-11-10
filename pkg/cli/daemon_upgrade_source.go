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

package cli

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// upgradeSourceFlags are the daemon flags that choose where -upgrade-image
// pulls candidate images from.
type upgradeSourceFlags struct {
	image            *string
	source, fallback *string
}

// upgradeSourceSettings is upgradeSourceFlags resolved against the
// environment and validated.
type upgradeSourceSettings struct {
	source, fallback               string
	sourceLocked, fallbackExplicit bool
}

func registerUpgradeSourceFlags(fs *flag.FlagSet) *upgradeSourceFlags {
	f := &upgradeSourceFlags{}
	f.image = fs.String("upgrade-image", "",
		"image repository, with no tag, that CI publishes a tag per branch to (e.g. "+
			"docker.io/grainai/grain); set, the UI's Upgrade button pulls a branch's image instead of "+
			"building one, and -upgrade-image-ref-file is required")
	f.source = fs.String("upgrade-image-source", "",
		"where -upgrade-image pulls candidate container images from: \"registry\" (docker pull, the default; "+
			"legacy \"release-assets\" logs a deprecation warning and maps to \"registry\"; or GRAIN_UPGRADE_IMAGE_SOURCE)")
	f.fallback = fs.String("upgrade-image-fallback", "",
		"fallback image source when the primary source cannot fetch a candidate daemon image: \"none\" or \"registry\" "+
			"(legacy \"release-assets\" logs a deprecation warning and maps to \"none\"; or GRAIN_UPGRADE_IMAGE_FALLBACK)")
	return f
}

// resolve fills each flag not passed from its GRAIN_UPGRADE_* environment
// variable and validates the result. Legacy "release-assets" values are
// accepted with a deprecation warning so systemd units written by older
// setup.sh still start cleanly.
func (f *upgradeSourceFlags) resolve(passed map[string]bool) (upgradeSourceSettings, error) {
	upgradeSourceLocked := passed["upgrade-image-source"]
	if !passed["upgrade-image-source"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_IMAGE_SOURCE")); env != "" {
			*f.source = env
			upgradeSourceLocked = true
		}
	}
	if raw := strings.TrimSpace(*f.source); raw != "" {
		if strings.EqualFold(raw, "release-assets") {
			log.Printf("WARNING: -upgrade-image-source=%q is deprecated and has been removed; using %q instead", raw, upgrade.ImageSourceRegistry)
			*f.source = string(upgrade.ImageSourceRegistry)
		} else {
			norm, err := upgrade.NormalizeImageSource(raw)
			if err != nil {
				return upgradeSourceSettings{}, fmt.Errorf("-upgrade-image-source: %w", err)
			}
			*f.source = string(norm)
		}
	}
	upgradeFallbackExplicit := passed["upgrade-image-fallback"]
	if !passed["upgrade-image-fallback"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_IMAGE_FALLBACK")); env != "" {
			*f.fallback = env
			upgradeFallbackExplicit = true
		}
	}
	if upgradeFallbackExplicit || strings.TrimSpace(*f.fallback) != "" {
		raw := strings.TrimSpace(*f.fallback)
		if strings.EqualFold(raw, "release-assets") {
			log.Printf("WARNING: -upgrade-image-fallback=%q is deprecated and has been removed; disabling fallback", raw)
			*f.fallback = ""
		} else {
			fb, err := upgrade.ParseImageFallback(raw)
			if err != nil {
				return upgradeSourceSettings{}, fmt.Errorf("-upgrade-image-fallback: %w", err)
			}
			*f.fallback = string(fb)
		}
	}
	return upgradeSourceSettings{
		source:           *f.source,
		sourceLocked:     upgradeSourceLocked,
		fallback:         *f.fallback,
		fallbackExplicit: upgradeFallbackExplicit,
	}, nil
}

// apply resolves f and sets the upgrade image fields of cfg.
func (f *upgradeSourceFlags) apply(cfg *daemon.Config, passed map[string]bool) error {
	s, err := f.resolve(passed)
	if err != nil {
		return err
	}
	cfg.UpgradeImage = *f.image
	cfg.UpgradeImageSource, cfg.UpgradeImageSourceLocked = s.source, s.sourceLocked
	cfg.UpgradeImageFallback, cfg.UpgradeImageFallbackExplicit = s.fallback, s.fallbackExplicit
	return nil
}
