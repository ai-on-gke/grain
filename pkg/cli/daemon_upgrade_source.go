package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// upgradeSourceFlags are the daemon flags that choose where -upgrade-image
// loads candidate images from: a repository's release assets or a registry.
type upgradeSourceFlags struct {
	image                                      *string
	source, fallback                           *string
	releaseRepo, releaseAsset, releaseChecksum *string
	sandboxReleaseAssets                       stringSliceFlag
}

// upgradeSourceSettings is upgradeSourceFlags resolved against the
// environment and validated.
type upgradeSourceSettings struct {
	source, fallback                           string
	sourceLocked, fallbackExplicit             bool
	releaseRepo, releaseAsset, releaseChecksum string
	sandboxReleaseAssets                       map[string]string
}

func registerUpgradeSourceFlags(fs *flag.FlagSet) *upgradeSourceFlags {
	f := &upgradeSourceFlags{}
	f.image = fs.String("upgrade-image", "",
		"image repository, with no tag, that CI publishes a tag per branch to (e.g. "+
			"ghcr.io/bwsalmon/grain/grain); set, the UI's Upgrade button pulls a branch's image instead of "+
			"building one, and -upgrade-image-ref-file is required")
	f.source = fs.String("upgrade-image-source", "",
		"where -upgrade-image loads candidate container images from: \"release-assets\" (gzip tarballs on the "+
			"repository's releases) or \"registry\" (docker pull); empty defaults to release-assets with registry "+
			"fallback when a release has no image assets (or GRAIN_UPGRADE_IMAGE_SOURCE)")
	f.fallback = fs.String("upgrade-image-fallback", "",
		"fallback image source when the primary source cannot fetch a candidate daemon image: \"none\", \"registry\", "+
			"or \"release-assets\" (or GRAIN_UPGRADE_IMAGE_FALLBACK)")
	f.releaseRepo = fs.String("upgrade-release-repo", "",
		"host-qualified repository (<host>/<owner>/<name>) whose releases carry upgrade image assets; defaults to "+
			"the repository mapped from -upgrade-image or registered by the edition (or GRAIN_UPGRADE_RELEASE_REPO)")
	f.releaseAsset = fs.String("upgrade-release-asset", "",
		"daemon tarball asset name on releases when images.json is not used (e.g. grain-image.tar.gz, or GRAIN_UPGRADE_RELEASE_ASSET)")
	f.releaseChecksum = fs.String("upgrade-release-checksum", "",
		"checksum asset name or suffix (starting with '.') for release-asset tarballs; defaults to .sha256 (or GRAIN_UPGRADE_RELEASE_CHECKSUM)")
	fs.Var(&f.sandboxReleaseAssets, "upgrade-sandbox-release-asset",
		"per-sandbox-image release asset mapping as <role-or-repo>=<asset> -- repeat for each image, e.g. "+
			"-upgrade-sandbox-release-asset=guest=grain-guest.tar.gz (or comma-separated in GRAIN_UPGRADE_SANDBOX_RELEASE_ASSETS)")
	return f
}

// resolve fills each flag not passed from its GRAIN_UPGRADE_* environment
// variable and validates the result. A source or fallback set either way is
// locked: Settings -> Upgrade shows it read-only.
func (f *upgradeSourceFlags) resolve(passed map[string]bool) (upgradeSourceSettings, error) {
	upgradeSourceLocked := passed["upgrade-image-source"]
	if !passed["upgrade-image-source"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_IMAGE_SOURCE")); env != "" {
			*f.source = env
			upgradeSourceLocked = true
		}
	}
	if raw := strings.TrimSpace(*f.source); raw != "" {
		norm, err := upgrade.NormalizeImageSource(raw)
		if err != nil {
			return upgradeSourceSettings{}, fmt.Errorf("-upgrade-image-source: %w", err)
		}
		*f.source = string(norm)
	}
	upgradeFallbackExplicit := passed["upgrade-image-fallback"]
	if !passed["upgrade-image-fallback"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_IMAGE_FALLBACK")); env != "" {
			*f.fallback = env
			upgradeFallbackExplicit = true
		}
	}
	if upgradeFallbackExplicit || strings.TrimSpace(*f.fallback) != "" {
		fb, err := upgrade.ParseImageFallback(*f.fallback)
		if err != nil {
			return upgradeSourceSettings{}, fmt.Errorf("-upgrade-image-fallback: %w", err)
		}
		*f.fallback = string(fb)
	}
	if !passed["upgrade-release-repo"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_RELEASE_REPO")); env != "" {
			*f.releaseRepo = env
		}
	}
	if raw := strings.TrimSpace(*f.releaseRepo); raw != "" {
		if _, _, _, err := upgrade.ParseReleaseRepository(raw); err != nil {
			return upgradeSourceSettings{}, fmt.Errorf("-upgrade-release-repo: %w", err)
		}
	}
	if !passed["upgrade-release-asset"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_RELEASE_ASSET")); env != "" {
			*f.releaseAsset = env
		}
	}
	if !passed["upgrade-release-checksum"] {
		if env := strings.TrimSpace(os.Getenv("GRAIN_UPGRADE_RELEASE_CHECKSUM")); env != "" {
			*f.releaseChecksum = env
		}
	}
	sandboxReleaseAssets, err := parseUpgradeSandboxReleaseAssets(
		f.sandboxReleaseAssets,
		os.Getenv("GRAIN_UPGRADE_SANDBOX_RELEASE_ASSETS"),
		passed["upgrade-sandbox-release-asset"],
	)
	if err != nil {
		return upgradeSourceSettings{}, err
	}
	return upgradeSourceSettings{
		source:               *f.source,
		sourceLocked:         upgradeSourceLocked,
		fallback:             *f.fallback,
		fallbackExplicit:     upgradeFallbackExplicit,
		releaseRepo:          strings.TrimSpace(*f.releaseRepo),
		releaseAsset:         strings.TrimSpace(*f.releaseAsset),
		releaseChecksum:      strings.TrimSpace(*f.releaseChecksum),
		sandboxReleaseAssets: sandboxReleaseAssets,
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
	cfg.UpgradeReleaseRepo, cfg.UpgradeReleaseAsset, cfg.UpgradeReleaseChecksum = s.releaseRepo, s.releaseAsset, s.releaseChecksum
	cfg.UpgradeSandboxReleaseAssets = s.sandboxReleaseAssets
	return nil
}

func parseUpgradeSandboxReleaseAssets(flagItems []string, envVal string, flagPassed bool) (map[string]string, error) {
	items := flagItems
	if !flagPassed && strings.TrimSpace(envVal) != "" {
		items = strings.Split(envVal, ",")
	}
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(items))
	for _, raw := range items {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		k, v, ok := strings.Cut(raw, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("-upgrade-sandbox-release-asset: invalid %q (want <role-or-repo>=<asset>)", raw)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
